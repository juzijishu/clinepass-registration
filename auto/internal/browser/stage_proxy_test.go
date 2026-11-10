package browser

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStageProxyWithRealBrowser(t *testing.T) {
	python := os.Getenv("STAGE_BROWSER_PYTHON")
	if python == "" {
		t.Skip("set STAGE_BROWSER_PYTHON to run isolated Chromium network acceptance")
	}
	var mu sync.Mutex
	observed := make(map[string]bool)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "test-session", Value: "synthetic", Path: "/"})
		} else if cookie, err := r.Cookie("test-session"); err != nil || cookie.Value != "synthetic" {
			t.Error("network switch lost browser cookies")
		}
		mu.Lock()
		observed[r.URL.Path] = r.Header.Get("X-Test-Proxy") == "yes"
		mu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html><body>local acceptance</body></html>")
	}))
	defer target.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			w.WriteHeader(400)
			return
		}
		conn, reader, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		tr := &http.Transport{Proxy: nil}
		defer tr.CloseIdleConnections()
		for {
			req, err := http.ReadRequest(reader.Reader)
			if err != nil {
				return
			}
			req.RequestURI = ""
			req.URL.Scheme = "http"
			req.URL.Host = r.Host
			req.Header.Set("X-Test-Proxy", "yes")
			response, err := tr.RoundTrip(req)
			if err != nil {
				return
			}
			err = response.Write(conn)
			response.Body.Close()
			if err != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	relay, err := StartStageRelay(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	_, source, _, _ := runtime.Caller(0)
	worker := filepath.Clean(filepath.Join(filepath.Dir(source), "../../worker"))
	input, _ := json.Marshal(map[string]string{"browser_proxy": relay.BrowserURL(), "network_control_url": relay.ControlURL(), "network_control_token": relay.ControlToken(), "target": target.URL, "worker": worker})
	script := `import sys,json,os
from unittest.mock import patch
from playwright.sync_api import sync_playwright
d=json.load(sys.stdin)
sys.path.insert(0,d['worker'])
from stage_network import StageNetwork
with sync_playwright() as p:
    browser=p.chromium.launch(executable_path=os.environ.get('STAGE_BROWSER_EXECUTABLE'),headless=True,proxy={'server':d['browser_proxy']},args=['--proxy-bypass-list=<-loopback>','--disable-quic','--disable-background-networking'])
    context=browser.new_context(service_workers='block')
    with patch('stage_network.AUTH_HOST','127.0.0.1'):
        StageNetwork.install(context,d)
        page=context.new_page()
        page.goto(d['target']+'/login',wait_until='domcontentloaded')
        page.goto(d['target']+'/radar-challenge/send',wait_until='domcontentloaded')
        page.evaluate("fetch('/radar-challenge/verify',{method:'POST',body:'synthetic-otp'}).then(r=>r.text())")
        page.goto(d['target']+'/complete',wait_until='domcontentloaded')
    browser.close()
`
	cmd := exec.Command(python, "-c", script)
	cmd.Stdin = strings.NewReader(string(input))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("isolated browser acceptance failed: %v\n%s", err, output)
	}
	mu.Lock()
	defer mu.Unlock()
	for path, wantProxy := range map[string]bool{"/login": true, "/radar-challenge/send": false, "/radar-challenge/verify": false, "/complete": true} {
		got, exists := observed[path]
		if !exists || got != wantProxy {
			t.Errorf("%s observed=%v proxy=%v want=%v", path, exists, got, wantProxy)
		}
	}
}

func TestStageProxySwitchClosesOldTunnelAndDoesNotChangeSMSProxy(t *testing.T) {
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodConnect {
			t.Error("expected CONNECT")
			w.WriteHeader(400)
			return
		}
		attempts.Add(1)
		dest, err := net.DialTimeout("tcp", req.Host, time.Second)
		if err != nil {
			w.WriteHeader(502)
			return
		}
		defer dest.Close()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		tunnel(conn, dest)
	}))
	defer upstream.Close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "reached") }))
	defer target.Close()
	r, err := StartStageRelay(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	sms, err := StartProxyRelay(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer sms.Close()
	read := func(proxy string) {
		t.Helper()
		tr := &http.Transport{Proxy: http.ProxyURL(mustURL(proxy)), DisableKeepAlives: true}
		defer tr.CloseIdleConnections()
		resp, err := (&http.Client{Transport: tr, Timeout: 3 * time.Second}).Get(target.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 || string(body) != "reached" {
			t.Fatalf("response %d %q", resp.StatusCode, body)
		}
	}
	control := func(token, mode string, want int) {
		t.Helper()
		req, _ := http.NewRequest("POST", r.ControlURL(), strings.NewReader(mode))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 3 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("control status %d", resp.StatusCode)
		}
	}
	read(r.BrowserURL())
	if attempts.Load() != 1 {
		t.Fatal("initial request did not use upstream")
	}
	conn, err := net.DialTimeout("tcp", r.ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "CONNECT "+strings.TrimPrefix(target.URL, "http://")+" HTTP/1.1\r\nHost: "+strings.TrimPrefix(target.URL, "http://")+"\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("tunnel failed %v", err)
	}
	control("wrong", "direct", 403)
	control(r.ControlToken(), "unknown", 400)
	control(r.ControlToken(), "direct", 200)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("old upstream tunnel remained open")
	} else if n, ok := err.(net.Error); ok && n.Timeout() {
		t.Fatal("old tunnel not closed")
	}
	before := attempts.Load()
	read(r.BrowserURL())
	if attempts.Load() != before {
		t.Fatal("verification request leaked to upstream")
	}
	read(sms.BrowserURL())
	if attempts.Load() != before+1 {
		t.Fatal("SMS API lost its upstream proxy")
	}
	control(r.ControlToken(), "proxy", 200)
	read(r.BrowserURL())
	if attempts.Load() != before+2 {
		t.Fatal("proxy was not restored")
	}
}

func TestStageProxyFailureNeverFallsBackToDirect(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer target.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer upstream.Close()
	r, err := StartStageRelay(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	tr := &http.Transport{Proxy: http.ProxyURL(mustURL(r.BrowserURL()))}
	defer tr.CloseIdleConnections()
	resp, err := (&http.Client{Transport: tr, Timeout: 3 * time.Second}).Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 502 || hits.Load() != 0 {
		t.Fatal("failed upstream was bypassed")
	}
	for _, raw := range []string{"", "file:///tmp/proxy", "http://localhost:7890/subscription?token=private"} {
		if relay, err := StartStageRelay(raw); err == nil {
			relay.Close()
			t.Fatal("invalid proxy accepted")
		}
	}
}

func TestHostNeedsDirectMatchesClineOwnedHosts(t *testing.T) {
	direct := []string{
		"authkit.cline.bot:443",
		"app.cline.bot:443",
		"api.cline.bot:443",
		"evil.cline.bot:8443",
		"auth.workos.com:443",
	}
	for _, address := range direct {
		if !hostNeedsDirect(address) {
			t.Errorf("hostNeedsDirect(%q) = false, want true", address)
		}
	}
	proxied := []string{
		"login.live.com:443",
		"login.microsoftonline.com:443",
		"account.live.com:443",
		"accounts.google.com:443",
		"cdn.example.test:443",
		"cline.bot.evil.test:443",
	}
	for _, address := range proxied {
		if hostNeedsDirect(address) {
			t.Errorf("hostNeedsDirect(%q) = true, want false", address)
		}
	}
}
