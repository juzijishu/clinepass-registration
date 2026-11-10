package clinebypass

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"golang.org/x/crypto/scrypt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Opt-in contract test: the source checkout runs with entirely disposable state.
func TestAppendAgainstIsolatedClinebypass(t *testing.T) {
	source := os.Getenv("CLINEBYPASS_TEST_SOURCE")
	if source == "" {
		t.Skip("CLINEBYPASS_TEST_SOURCE is not set")
	}
	dir := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	password := "synthetic-admin-password"
	salt := make([]byte, 32)
	hash, err := scrypt.Key([]byte(password), salt, 16384, 8, 1, 64)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, value any) {
		t.Helper()
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("admin-auth.json", map[string]any{"version": 1, "initialized": true, "salt": hex.EncodeToString(salt), "hash": hex.EncodeToString(hash)})
	write("config.json", map[string]any{"port": port, "proxyKey": "synthetic-client-secret", "accounts": []any{map[string]any{"id": "keep-existing", "name": "保留账号", "key": "synthetic-old-key", "clientKeyId": "legacy", "enabled": false, "note": "retain-note", "headers": map[string]string{"X-Example": "retain-header"}, "maxConcurrent": 2, "maxRpm": 3, "priority": 4, "weight": 5}}, "providerProbeSchedule": map[string]any{"enabled": false, "intervalMs": 300000, "maxPerRound": 2, "maxPer24h": 100, "failureThreshold": 3}})
	cmd := exec.Command("node", "server.js")
	cmd.Dir = source
	var diagnostic bytes.Buffer
	cmd.Stderr = &diagnostic
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "DATA_DIR=" + dir, "PORT=" + strconv.Itoa(port), "BIND_HOST=127.0.0.1", "NODE_ENV=test"}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	ready := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		res, err := http.Get(base + "/api/meta")
		if err == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("isolated target did not become ready: %s", diagnostic.String())
	}
	client, err := New(base, password, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = client.Append("synthetic-source-account", "synthetic-created-key"); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Accounts []struct {
			ID      string            `json:"id"`
			Key     string            `json:"key"`
			Owner   string            `json:"clientKeyId"`
			Note    string            `json:"note"`
			MaxRPM  int               `json:"maxRpm"`
			Enabled bool              `json:"enabled"`
			Headers map[string]string `json:"headers"`
		} `json:"accounts"`
	}
	if json.Unmarshal(raw, &saved) != nil || len(saved.Accounts) != 2 {
		t.Fatal("real target append or duplicate reconciliation failed")
	}
	old := saved.Accounts[0]
	if old.ID != "keep-existing" || old.Note != "retain-note" || old.MaxRPM != 3 || old.Enabled || old.Headers["X-Example"] != "retain-header" {
		t.Fatal("real target changed existing fields")
	}
	created := saved.Accounts[1]
	if created.ID == "" || created.ID == old.ID || created.Owner != "legacy" || created.Key != "synthetic-created-key" {
		t.Fatal("real target did not assign identity/ownership")
	}
}
