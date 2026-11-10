package clinebypass

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"github.com/juzijishu/clinepass-registration/internal/config"
	"strings"
	"time"
)

type Client struct {
	base, password, owner, csrf string
	http                        *http.Client
}

func New(base, password, owner string) (*Client, error) {
	settings := config.RegistrationSettings{TargetURL: base, TargetAdminPassword: password, TargetClientKeyID: owner}
	if err := settings.ValidateTarget(); err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	jar, _ := cookiejar.New(nil)
	return &Client{base: strings.TrimRight(base, "/"), password: password, owner: owner, http: &http.Client{Transport: transport, Jar: jar, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) request(method, path string, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("号池请求无效")
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		return 0, fmt.Errorf("号池地址无效")
	}
	req.Header.Set("Origin", c.base)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("号池网络请求失败")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return res.StatusCode, fmt.Errorf("号池请求被拒绝（HTTP %d）", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, (16<<20)+1))
	if err != nil || len(raw) > 16<<20 {
		return res.StatusCode, fmt.Errorf("号池响应不完整或过大")
	}
	if out != nil && json.Unmarshal(raw, out) != nil {
		return res.StatusCode, fmt.Errorf("号池返回格式无效")
	}
	return res.StatusCode, nil
}

// Append uses the target's authoritative full-save contract and reconciles
// ambiguous writes by reading again before retrying. Responses never enter logs.
func (c *Client) Append(accountID, key string) error {
	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(key) == "" {
		return fmt.Errorf("入池账号或 Key 无效")
	}
	var login struct {
		OK      bool   `json:"ok"`
		Pending bool   `json:"pending"`
		CSRF    string `json:"csrf"`
	}
	_, err := c.request("POST", "/api/auth/login", map[string]string{"password": c.password}, &login)
	if err != nil {
		return err
	}
	c.csrf = login.CSRF
	defer func() { c.request("POST", "/api/auth/logout", map[string]any{}, nil); c.csrf = "" }()
	if !login.OK || login.Pending || login.CSRF == "" {
		return fmt.Errorf("号池管理员登录未完成，请先在目标服务完成密码初始化")
	}
	var inventory struct {
		Keys []struct {
			ID string `json:"id"`
		} `json:"keys"`
	}
	if _, err = c.request("GET", "/api/security/client-keys", nil, &inventory); err != nil {
		return err
	}
	found := false
	for _, entry := range inventory.Keys {
		if entry.ID == c.owner {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("号池客户端密钥归属不存在")
	}
	hash := sha256.Sum256([]byte(accountID))
	name := "订阅-" + hex.EncodeToString(hash[:16])
	for attempt := 0; attempt < 3; attempt++ {
		snapshot, err := c.snapshot()
		if err != nil {
			return err
		}
		exists, err := c.contains(snapshot, key, name)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
		var rows []json.RawMessage
		json.Unmarshal(snapshot["accounts"], &rows)
		// New IDs belong to the target server; a unique name avoids its legacy ID reuse.
		row, _ := json.Marshal(map[string]any{"name": name, "key": key, "clientKeyId": c.owner, "enabled": true})
		rows = append(rows, row)
		draft := make(map[string]json.RawMessage)
		for _, field := range []string{"mode", "active", "concurrencyWaitMs", "poolFullWaitMs", "errorRules", "retryRules", "accountPipeline", "accountWorkflow", "quotaProtection", "sessionProviderAffinityEnabled"} {
			if value, ok := snapshot[field]; ok {
				draft[field] = value
			} else {
				return fmt.Errorf("号池版本不兼容：缺少调度配置")
			}
		}
		draft["expectedConfigurationRevision"] = snapshot["configurationRevision"]
		draft["accounts"], _ = json.Marshal(rows)
		status, writeErr := c.request("POST", "/api/accounts", draft, nil)
		// Re-read even after a lost/invalid response; never infer that nothing saved.
		check, readErr := c.snapshot()
		if readErr != nil {
			return fmt.Errorf("入池结果待核对；Key 已保留，可稍后重试")
		}
		exists, err = c.contains(check, key, name)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
		if writeErr != nil && status != 0 && status != 409 && status < 500 {
			return writeErr
		}
	}
	return fmt.Errorf("入池未确认或配置持续变更；Key 已保留，可稍后重试")
}

func (c *Client) snapshot() (map[string]json.RawMessage, error) {
	var snapshot map[string]json.RawMessage
	if _, err := c.request("GET", "/api/accounts", nil, &snapshot); err != nil {
		return nil, err
	}
	var revision string
	var rows []map[string]json.RawMessage
	if json.Unmarshal(snapshot["configurationRevision"], &revision) != nil || len(revision) != 64 || json.Unmarshal(snapshot["accounts"], &rows) != nil || rows == nil {
		return nil, fmt.Errorf("号池快照无效或不支持版本校验")
	}
	if _, err := hex.DecodeString(revision); err != nil {
		return nil, fmt.Errorf("号池版本号无效")
	}
	for _, row := range rows {
		if row == nil {
			return nil, fmt.Errorf("号池账号快照无效")
		}
	}
	return snapshot, nil
}

func (c *Client) contains(snapshot map[string]json.RawMessage, key, name string) (bool, error) {
	var rows []struct {
		Name  string `json:"name"`
		Key   string `json:"key"`
		Owner string `json:"clientKeyId"`
	}
	if json.Unmarshal(snapshot["accounts"], &rows) != nil {
		return false, fmt.Errorf("号池账号字段无效")
	}
	exists := false
	for _, row := range rows {
		if row.Key == key {
			if row.Owner != c.owner {
				return false, fmt.Errorf("该 Key 已归属其他客户端，已停止")
			}
			exists = true
		}
		if row.Name == name && row.Key != key {
			return false, fmt.Errorf("入池账号名称冲突，需人工核对")
		}
	}
	return exists, nil
}
