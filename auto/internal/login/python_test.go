package login

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/juzijishu/clinepass-registration/internal/config"
)

func TestEngineDefaultPython(t *testing.T) {
	t.Setenv("LOGIN_ENGINE", "")
	if Engine() != "python" {
		t.Fatalf("default=%q", Engine())
	}
	t.Setenv("LOGIN_ENGINE", "go")
	if Engine() != "go" {
		t.Fatalf("go=%q", Engine())
	}
}

func TestMapWorkerCode(t *testing.T) {
	if !errors.Is(mapWorkerCode("radar_denied", "x"), ErrRadarDenied) {
		t.Fatal("radar")
	}
	if !errors.Is(mapWorkerCode("banned", "x"), ErrAccountBanned) {
		t.Fatal("banned")
	}
	if !errors.Is(mapWorkerCode("sms_relogin", "x"), ErrSMSNeedRelogin) {
		t.Fatal("sms")
	}
	if !errors.Is(mapWorkerCode("sms_timeout", "x"), ErrPhoneTimeout) {
		t.Fatal("timeout")
	}
	if !errors.Is(mapWorkerCode("manual_timeout", "x"), ErrManualTimeout) {
		t.Fatal("manual timeout")
	}
	if !errors.Is(mapWorkerCode("authkit_stuck", "boom"), ErrAuthkitStuck) {
		t.Fatal("authkit")
	}
	if IsAuthkitFailure(mapWorkerCode("radar_denied", "x")) {
		t.Fatal("radar should not retry")
	}
	pending := mapWorkerCode("authkit_callback_pending", "x")
	if !errors.Is(pending, ErrAuthkitCallbackPending) || IsAuthkitFailure(pending) {
		t.Fatal("an unfinished callback must not cause another automatic login")
	}
}

// A manual handoff must surface a visible window; automatic runs keep the
// configured headless mode.
func TestResolveWorkerHeadlessForcesWindowForManualLogin(t *testing.T) {
	manual := config.Config{Headless: true, Registration: config.RegistrationSettings{ManualSMS: true}}
	if resolveWorkerHeadless(manual, "login", nil) {
		t.Fatal("manual login must run with a visible window")
	}
	// 续 Cookie 与刷新不需要人工操作，保持无头。
	if !resolveWorkerHeadless(manual, "cookie", nil) {
		t.Fatal("cookie keepalive must keep headless mode")
	}
	if !resolveWorkerHeadless(manual, "refresh", nil) {
		t.Fatal("refresh must keep headless mode")
	}
	// 关闭人工接码后恢复无头。
	auto := config.Config{Headless: true}
	if !resolveWorkerHeadless(auto, "login", nil) {
		t.Fatal("automatic login must keep the configured headless mode")
	}
}

// The worker reads these keys by name; a tag change would silently disable
// manual handoff and send the account to HeroSMS instead.
func TestWorkerJobCarriesManualSMSContract(t *testing.T) {
	cfg := config.Config{Registration: config.RegistrationSettings{ManualSMS: true, ManualWaitSeconds: 900}}
	job := workerJob{
		Action:   "login",
		Settings: workerSettings{ManualSMS: cfg.Registration.ManualSMS, ManualWaitSeconds: cfg.Registration.ManualWaitSeconds},
	}
	payload, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(payload, &back); err != nil {
		t.Fatal(err)
	}
	settings, ok := back["settings"].(map[string]any)
	if !ok {
		t.Fatalf("settings missing from payload: %s", payload)
	}
	if settings["manual_sms"] != true {
		t.Fatalf("manual_sms not serialized as a bool: %v", settings["manual_sms"])
	}
	if settings["manual_wait_seconds"] != float64(900) {
		t.Fatalf("manual_wait_seconds not serialized: %v", settings["manual_wait_seconds"])
	}
	// 关闭时也要显式传 false，避免执行器沿用上一次的进程内状态。
	off, _ := json.Marshal(workerSettings{})
	var offBack map[string]any
	_ = json.Unmarshal(off, &offBack)
	if _, present := offBack["manual_sms"]; present {
		t.Fatal("omitempty must drop manual_sms when it is false")
	}
}

func TestFindWorker(t *testing.T) {
	python, script, err := findWorker()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatal(script, err)
	}
	if python == "" {
		t.Fatal("empty python")
	}
}

func TestWorkerEnvFreeCloakDropsLicense(t *testing.T) {
	t.Setenv("CLOAKBROWSER_LICENSE_KEY", "ck_parent")
	t.Setenv("CLOAKBROWSER_VERSION", "151.0")
	cfg := config.Config{LicenseKey: "ck_paid", CloakVersion: "151.0.7922.108.2"}
	paid := workerEnv(cfg, false)
	if !containsEnv(paid, "CLOAKBROWSER_LICENSE_KEY=ck_paid") {
		t.Fatalf("paid env %#v", paid)
	}
	free := workerEnv(cfg, true)
	for _, kv := range free {
		if strings.HasPrefix(kv, "CLOAKBROWSER_LICENSE_KEY=") || strings.HasPrefix(kv, "CLOAKBROWSER_VERSION=") {
			t.Fatalf("free cloak leaked %s", kv)
		}
	}
}

func containsEnv(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}

func TestWorkerEnvUsesAutoCallbackToken(t *testing.T) {
	t.Setenv("AUTO_TOKEN", "parent-token")
	t.Setenv("AUTO_API_TOKEN", "stale-callback-token")
	env := workerEnv(config.Config{AutoToken: "active-auto-token"}, false)
	if !containsEnv(env, "AUTO_API_TOKEN=active-auto-token") {
		t.Fatal("missing callback token")
	}
	if containsEnv(env, "AUTO_API_TOKEN=stale-callback-token") || containsEnv(env, "AUTO_TOKEN=parent-token") {
		t.Fatal("stale parent token inherited")
	}
}

func TestWorkerEnvForcesUTF8(t *testing.T) {
	t.Setenv("PYTHONIOENCODING", "gbk")
	t.Setenv("PYTHONUTF8", "0")
	t.Setenv("PYTHONUNBUFFERED", "0")
	for _, freeCloak := range []bool{false, true} {
		env := workerEnv(config.Config{}, freeCloak)
		for key, want := range map[string]string{
			"PYTHONIOENCODING": "utf-8",
			"PYTHONUTF8":       "1",
			"PYTHONUNBUFFERED": "1",
		} {
			count := 0
			for _, entry := range env {
				name, value, _ := strings.Cut(entry, "=")
				if strings.EqualFold(name, key) {
					count++
					if name != key || value != want {
						t.Errorf("freeCloak=%v: %s=%q, want %q", freeCloak, name, value, want)
					}
				}
			}
			if count != 1 {
				t.Errorf("freeCloak=%v: %s has %d entries, want one", freeCloak, key, count)
			}
		}
	}
}

func TestWorkerUTF8RoundTrip(t *testing.T) {
	python := strings.TrimSpace(os.Getenv("LOGIN_PYTHON"))
	if python == "" {
		t.Skip("set LOGIN_PYTHON to exercise real Python pipes without a browser")
	}
	t.Setenv("PYTHONIOENCODING", "gbk")
	t.Setenv("PYTHONUTF8", "0")
	t.Setenv("PYTHONUNBUFFERED", "0")
	want := `微软下一步 成功；等待微软页面下一步；C:\账号\截图.png 😀`
	payload, err := json.Marshal(map[string]string{"text": want})
	if err != nil {
		t.Fatal(err)
	}
	// This fixture uses the same pipe/environment contract as the worker but
	// does not import browser code or contact an account or payment provider.
	const script = `import json, sys
job = json.load(sys.stdin)
print(json.dumps({"type": "log", "msg": job["text"]}, ensure_ascii=False))
sys.stderr.write(job["text"])
print(json.dumps({"type": "result", "ok": True, "error": job["text"]}, ensure_ascii=False))
`
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "-c", script)
	cmd.Env = workerEnv(config.Config{}, false)
	cmd.Stdin = bytes.NewReader(payload)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("Python pipe round trip: %v, stderr=%s", err, stderr.String())
	}
	if !utf8.Valid(out) || !utf8.Valid(stderr.Bytes()) {
		t.Fatal("Python emitted non-UTF-8 bytes")
	}
	lines := bytes.Split(bytes.TrimSpace(out), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatalf("got %d protocol messages, want two", len(lines))
	}
	var logged, result workerMsg
	if err := json.Unmarshal(lines[0], &logged); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(lines[1], &result); err != nil {
		t.Fatal(err)
	}
	if logged.Type != "log" || logged.Msg != want || result.Type != "result" || !result.OK || result.Error != want || stderr.String() != want {
		t.Fatalf("Chinese/emoji did not round-trip: log=%q result=%q stderr=%q", logged.Msg, result.Error, stderr.String())
	}
}

func TestFindRepoRoot(t *testing.T) {
	root, err := FindRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "auto", "worker", "login.py")); err != nil {
		t.Fatal(root, err)
	}
}
