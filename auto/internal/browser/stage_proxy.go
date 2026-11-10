package browser

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	xproxy "golang.org/x/net/proxy"
)

// A single browser keeps its cookies. Only its outbound connections change.
type stageDialer struct {
	mu         sync.Mutex
	upstream   xproxy.Dialer
	direct     bool
	closed     bool
	generation uint64
	active     map[*stageConn]bool
}

type stageConn struct {
	net.Conn
	owner *stageDialer
}

func (c *stageConn) Close() error {
	c.owner.mu.Lock()
	delete(c.owner.active, c)
	c.owner.mu.Unlock()
	return c.Conn.Close()
}

// clineDirectHosts 是登录最后一步所在的自有域：OAuth 回调、Radar 评分与
// 应用落地都在这里。这些域走真实住宅线路直连可显著降低风控分，且按目标
// 地址分流不需要切换线路，避免回调连接被中断。
var clineDirectHosts = []string{
	"authkit.cline.bot",
	"app.cline.bot",
	"api.cline.bot",
	".cline.bot",
	".workos.com",
}

func hostNeedsDirect(address string) bool {
	host := address
	if h, _, err := net.SplitHostPort(address); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	for _, suffix := range clineDirectHosts {
		if strings.HasPrefix(suffix, ".") {
			if strings.HasSuffix(host, suffix) || host == strings.TrimPrefix(suffix, ".") {
				return true
			}
			continue
		}
		if host == suffix {
			return true
		}
	}
	return false
}

func (d *stageDialer) Dial(network, address string) (net.Conn, error) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, net.ErrClosed
	}
	upstream, direct, generation := d.upstream, d.direct, d.generation
	d.mu.Unlock()
	if direct || (upstream != nil && hostNeedsDirect(address)) {
		upstream = &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}
	}
	c, err := upstream.Dial(network, address)
	if err != nil {
		return nil, errors.New("连接指定网络失败")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || generation != d.generation {
		c.Close()
		return nil, net.ErrClosed
	}
	wrapped := &stageConn{Conn: c, owner: d}
	d.active[wrapped] = true
	return wrapped, nil
}

func (d *stageDialer) switchMode(direct bool, closeAll bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || (!closeAll && direct == d.direct) {
		return
	}
	d.direct = direct
	d.closed = closeAll
	d.generation++
	for c := range d.active {
		_ = c.Conn.Close()
		delete(d.active, c)
	}
}
func (d *stageDialer) Close() error { d.switchMode(false, true); return nil }

func StartProxyRelay(upstream string) (*Relay, error) {
	d, err := explicitProxyDialer(upstream)
	if err != nil {
		return nil, err
	}
	return startRelay(&stageDialer{upstream: d, active: make(map[*stageConn]bool)}, nil)
}

func StartStageRelay(upstream string) (*Relay, error) {
	dialer, err := explicitProxyDialer(upstream)
	if err != nil {
		return nil, err
	}
	d := &stageDialer{upstream: dialer, active: make(map[*stageConn]bool)}
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return nil, errors.New("生成网络控制凭据失败")
	}
	token := hex.EncodeToString(raw)
	control := func(conn net.Conn, req *http.Request) {
		status := http.StatusOK
		if req.Method != http.MethodPost {
			status = http.StatusMethodNotAllowed
		} else if subtle.ConstantTimeCompare([]byte(req.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			status = http.StatusForbidden
		} else {
			body, err := io.ReadAll(io.LimitReader(req.Body, 16))
			mode := string(body)
			if err != nil || (mode != "direct" && mode != "proxy") {
				status = http.StatusBadRequest
			} else {
				d.switchMode(mode == "direct", false)
			}
		}
		_, _ = fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", status, http.StatusText(status))
	}
	return startControlledRelay(d, nil, token, control)
}

func (r *Relay) ControlURL() string   { return r.BrowserURL() + "/_stage" }
func (r *Relay) ControlToken() string { return r.token }

type httpTunnelDialer struct {
	address, hostname, authorization string
	secure                           bool
}
type bufferedTunnel struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedTunnel) Read(p []byte) (int, error) { return c.reader.Read(p) }

func explicitProxyDialer(raw string) (xproxy.Dialer, error) {
	u, err := parseProxyURL(raw)
	if err != nil || u == nil || u.Hostname() == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("请配置 HTTP/SOCKS5 代理地址，不能使用订阅链接")
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "socks", "socks5", "socks5h":
		return socksDialer(raw)
	case "http", "https":
		port := u.Port()
		if port == "" {
			port = "80"
			if scheme == "https" {
				port = "443"
			}
		}
		d := &httpTunnelDialer{address: net.JoinHostPort(u.Hostname(), port), hostname: u.Hostname(), secure: scheme == "https"}
		if u.User != nil {
			password, _ := u.User.Password()
			d.authorization = "Basic " + base64.StdEncoding.EncodeToString([]byte(u.User.Username()+":"+password))
		}
		return d, nil
	default:
		return nil, errors.New("只支持 HTTP/HTTPS/SOCKS5 代理")
	}
}

func (d *httpTunnelDialer) Dial(network, address string) (net.Conn, error) {
	nd := &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}
	var conn net.Conn
	var err error
	if d.secure {
		conn, err = tls.DialWithDialer(nd, "tcp", d.address, &tls.Config{ServerName: d.hostname, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = nd.Dial("tcp", d.address)
	}
	if err != nil {
		return nil, errors.New("代理连接失败")
	}
	ok := false
	defer func() {
		if !ok {
			_ = conn.Close()
		}
	}()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	header := "CONNECT " + address + " HTTP/1.1\r\nHost: " + address + "\r\n"
	if d.authorization != "" {
		header += "Proxy-Authorization: " + d.authorization + "\r\n"
	}
	if _, err = io.WriteString(conn, header+"\r\n"); err != nil {
		return nil, errors.New("代理握手失败")
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, errors.New("代理拒绝连接")
	}
	_ = conn.SetDeadline(time.Time{})
	ok = true
	return &bufferedTunnel{Conn: conn, reader: reader}, nil
}
