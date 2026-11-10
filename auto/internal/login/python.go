package login

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/juzijishu/clinepass-registration/auto/internal/browser"
	"github.com/juzijishu/clinepass-registration/internal/cline"
	"github.com/juzijishu/clinepass-registration/internal/config"
	"github.com/juzijishu/clinepass-registration/internal/model"
)

type workerJob struct {
	Action   string         `json:"action"`
	Account  workerAccount  `json:"account"`
	Settings workerSettings `json:"settings"`
}

type workerAccount struct {
	OAuthClientID     string `json:"oauth_client_id,omitempty"`
	OAuthRefreshToken string `json:"oauth_refresh_token,omitempty"`
	DeliveryExtra     string `json:"delivery_extra,omitempty"`
	ID                string `json:"id"`
	Email             string `json:"email"`
	Password          string `json:"password"`
	RecoveryEmail     string `json:"recovery_email"`
	FingerprintSeed   int    `json:"fingerprint_seed"`
	LoginProvider     string `json:"login_provider"`
	CookiesJSON       string `json:"cookies_json"`
	CookieHeader      string `json:"cookie_header"`
}

type workerSettings struct {
	InviteURL           string  `json:"invite_url"`
	Headless            bool    `json:"headless"`
	Proxy               string  `json:"proxy"`
	BrowserProxy        string  `json:"browser_proxy,omitempty"`
	HeroSMSProxy        string  `json:"hero_sms_proxy,omitempty"`
	NetworkControlURL   string  `json:"network_control_url,omitempty"`
	NetworkControlToken string  `json:"network_control_token,omitempty"`
	HeroSMSAPIKey       string  `json:"hero_sms_api_key"`
	HeroSMSService      string  `json:"hero_sms_service"`
	HeroSMSCountry      int     `json:"hero_sms_country"`
	HeroSMSMaxPrice     float64 `json:"hero_sms_max_price"`
	ManualSMS           bool    `json:"manual_sms,omitempty"`
	ManualWaitSeconds   int     `json:"manual_wait_seconds,omitempty"`
	SMSMaxAttempts      int     `json:"sms_max_attempts,omitempty"`
	SMSWaitSeconds      int     `json:"sms_wait_seconds,omitempty"`
	SMSAccountRemain    float64 `json:"sms_account_remaining,omitempty"`
	SMSBatchRemain      float64 `json:"sms_batch_remaining,omitempty"`
	ProfileDir          string  `json:"profile_dir"`
	ScreenshotPath      string  `json:"screenshot_path"`
	CloakVersion        string  `json:"cloak_version"`
	CloakCacheDir       string  `json:"cloak_cache_dir"`
	CloakBinaryPath     string  `json:"cloak_binary_path"`
	LicenseKey          string  `json:"license_key"`
	VirtualDisplay      bool    `json:"virtual_display"`
	AutoPay             bool    `json:"auto_pay"`
	ManagerAPI          string  `json:"manager_api"`
}

type workerMsg struct {
	Type         string `json:"type"`
	Msg          string `json:"msg"`
	OK           bool   `json:"ok"`
	Error        string `json:"error"`
	Code         string `json:"code"`
	CookiesJSON  string `json:"cookies_json"`
	CookieHeader string `json:"cookie_header"`
	PaymentURL   string `json:"payment_url"`
	Paid         bool   `json:"paid"`
	PayError     string `json:"pay_error"`
	SMSCost      float64 `json:"sms_cost"`
}

// resolveWorkerHeadless 决定执行器是否无头运行。人工接码必须在真实窗口里
// 填空手机号和验证码，因此登录动作强制有界面；其余动作保持原设置。
func resolveWorkerHeadless(cfg config.Config, action string, log Logger) bool {
	if log == nil {
		log = func(string, ...any) {}
	}
	headless := browser.PrepareDisplay(cfg.Headless, log)
	if cfg.Registration.ManualSMS && action == "login" {
		if headless {
			log("人工接码需要可见窗口，已按有界面模式启动浏览器")
		}
		if browser.VirtualDisplay() != "" {
			log("警告：当前显示是虚拟屏幕 %s，本地看不到浏览器窗口；请用 VNC 或远程桌面连接，或改回自动接码", browser.VirtualDisplay())
		}
		return false
	}
	return headless
}

func runPythonOnce(cfg config.Config, acc model.Account, action string, budget SMSBudget, log Logger) (Result, error) {
	if log == nil {
		log = func(string, ...any) {}
	}
	python, script, err := findWorker()
	if err != nil {
		return Result{}, err
	}
	headless := resolveWorkerHeadless(cfg, action, log)
	invite := strings.TrimSpace(cfg.InviteURL)
	if invite == "" {
		invite = cline.AuthURL
	}
	if err := os.MkdirAll(cfg.ProfilesDir(), 0o755); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(cfg.ScreenshotsDir(), 0o755); err != nil {
		return Result{}, err
	}
	shot := filepath.Join(cfg.ScreenshotsDir(), acc.ID+".png")
	if action == "refresh" {
		shot = filepath.Join(cfg.ScreenshotsDir(), acc.ID+"-pay.png")
	}
	freeCloak := action == "cookie"
	license := browser.ResolveLicense(cfg.LicenseKey)
	version := cfg.CloakVersion
	if freeCloak {
		license = ""
		version = ""
		log("续 Cookie 使用免费 Cloak，不占用付费并发")
	}
	job := workerJob{
		Action: action,
		Account: workerAccount{
			OAuthClientID: acc.OAuthClientID, OAuthRefreshToken: acc.OAuthRefreshToken, DeliveryExtra: acc.DeliveryExtra,
			ID:              acc.ID,
			Email:           acc.Email,
			Password:        acc.Password,
			RecoveryEmail:   acc.RecoveryEmail,
			FingerprintSeed: acc.FingerprintSeed,
			LoginProvider:   model.NormalizeLoginProvider(acc.LoginProvider),
			CookiesJSON:     acc.CookiesJSON,
			CookieHeader:    acc.CookieHeader,
		},
		Settings: workerSettings{
			InviteURL:         invite,
			Headless:          headless,
			Proxy:             cfg.Proxy,
			HeroSMSAPIKey:     cfg.HeroSMSAPIKey,
			HeroSMSService:    cfg.HeroSMSService,
			HeroSMSCountry:    cfg.HeroSMSCountry,
			HeroSMSMaxPrice:   cfg.HeroSMSMaxPrice,
			ManualSMS:         cfg.Registration.ManualSMS,
			ManualWaitSeconds: cfg.Registration.ManualWaitSeconds,
			SMSMaxAttempts:    budget.MaxAttempts,
			SMSWaitSeconds:    budget.WaitSeconds,
			SMSAccountRemain:  budget.AccountRemain,
			SMSBatchRemain:    budget.BatchRemain,
			ProfileDir:        filepath.Join(cfg.ProfilesDir(), acc.ID),
			ScreenshotPath:    shot,
			CloakVersion:      version,
			CloakCacheDir:     cfg.CloakCacheDir,
			CloakBinaryPath:   cfg.CloakBinaryPath,
			LicenseKey:        license,
			VirtualDisplay:    browser.VirtualDisplay() != "",
			AutoPay:           cfg.AutoPay,
			ManagerAPI:        cfg.ManagerAPI(),
		},
	}
	if action == "login" {
		relay, err := browser.StartStageRelay(cfg.Proxy)
		if err != nil {
			return Result{}, err
		}
		defer relay.Close()
		smsRelay, err := browser.StartProxyRelay(cfg.Proxy)
		if err != nil {
			return Result{}, err
		}
		defer smsRelay.Close()
		job.Settings.BrowserProxy = relay.BrowserURL()
		job.Settings.HeroSMSProxy = smsRelay.BrowserURL()
		job.Settings.NetworkControlURL = relay.ControlURL()
		job.Settings.NetworkControlToken = relay.ControlToken()
	}
	payload, err := json.Marshal(job)
	if err != nil {
		return Result{}, err
	}
	log("登录引擎=python cloak humanize")
	cmd := exec.Command(python, script)
	browser.IsolateProcess(cmd)
	// Keep configuration paths relative to the repository, including an
	// explicitly configured LOGIN_PYTHON=./auto/worker/.venv/bin/python.
	cmd.Dir = filepath.Dir(filepath.Dir(filepath.Dir(script)))
	cmd.Stdin = strings.NewReader(string(payload))
	cmd.Env = workerEnv(cfg, freeCloak)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("启动登录工人失败: %w", err)
	}
	var last workerMsg
	gotResult := false
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var msg workerMsg
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			log("%s", line)
			continue
		}
		switch msg.Type {
		case "log":
			if msg.Msg != "" {
				log("%s", msg.Msg)
			}
		case "result":
			last = msg
			gotResult = true
		}
	}
	waitErr := cmd.Wait()
	if err := sc.Err(); err != nil {
		return Result{}, fmt.Errorf("读取登录工人输出失败: %w", err)
	}
	if !gotResult {
		if waitErr != nil {
			return Result{}, fmt.Errorf("登录工人异常退出: %w", waitErr)
		}
		return Result{}, fmt.Errorf("登录工人没有返回结果")
	}
	if !last.OK {
		return Result{SMSCost: last.SMSCost}, mapWorkerCode(last.Code, last.Error)
	}
	res := Result{
		Email:        acc.Email,
		CookiesJSON:  last.CookiesJSON,
		CookieHeader: last.CookieHeader,
		PaymentURL:   last.PaymentURL,
		WorkspaceID:  acc.WorkspaceID,
		APIKey:       acc.APIKey,
		UserID:       acc.UserID,
		Paid:         last.Paid,
		PayError:     last.PayError,
		SMSCost:      last.SMSCost,
	}
	if action != "refresh" && action != "cookie" {
		key, userID, err := createClineKey(cfg, res.CookieHeader, "", "", log)
		if err != nil {
			return Result{SMSCost: last.SMSCost}, err
		}
		res.APIKey = key
		res.UserID = userID
		res.WorkspaceID = userID
		log("用户 ID: %s", userID)
	}
	return res, nil
}

func mapWorkerCode(code, msg string) error {
	switch strings.TrimSpace(code) {
	case "radar_denied":
		return ErrRadarDenied
	case "banned":
		return ErrAccountBanned
	case "authkit_callback_pending":
		return ErrAuthkitCallbackPending
	case "sms_relogin":
		return ErrSMSNeedRelogin
	case "sms_timeout":
		return ErrPhoneTimeout
	case "manual_timeout":
		return ErrManualTimeout
	case "authkit_stuck":
		if msg == "" {
			return ErrAuthkitStuck
		}
		return CompactError(fmt.Errorf("%w: %s", ErrAuthkitStuck, msg))
	default:
		if strings.TrimSpace(msg) == "" {
			return fmt.Errorf("登录工人失败")
		}
		return CompactError(fmt.Errorf("%s", msg))
	}
}

func findWorker() (python, script string, err error) {
	root, err := FindRepoRoot()
	if err != nil {
		return "", "", err
	}
	script = filepath.Join(root, "auto", "worker", "login.py")
	if st, e := os.Stat(script); e != nil || st.IsDir() {
		return "", "", fmt.Errorf("未找到登录工人 %s", script)
	}
	if p := strings.TrimSpace(os.Getenv("LOGIN_PYTHON")); p != "" {
		return p, script, nil
	}
	venv := filepath.Join(root, "auto", "worker", ".venv", "bin", "python")
	if runtime.GOOS == "windows" {
		venv = filepath.Join(root, "auto", "worker", ".venv", "Scripts", "python.exe")
	}
	if st, e := os.Stat(venv); e == nil && !st.IsDir() {
		return venv, script, nil
	}
	if p, e := exec.LookPath("python3"); e == nil {
		return p, script, nil
	}
	if p, e := exec.LookPath("python"); e == nil {
		return p, script, nil
	}
	if runtime.GOOS == "windows" {
		return "", "", fmt.Errorf("未找到 Python：请安装 Python 3.10 或更高版本并重新打开终端，再使用 auto/start.cmd 安装执行器依赖并启动 Auto")
	}
	return "", "", fmt.Errorf("未找到 Python，请按 auto/README.md 的对应系统步骤创建 auto/worker/.venv 并安装执行器依赖")
}

func FindRepoRoot() (string, error) {
	var starts []string
	if cwd, err := os.Getwd(); err == nil {
		starts = append(starts, cwd)
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		starts = append(starts, dir, filepath.Dir(dir))
	}
	for _, start := range starts {
		dir := start
		for i := 0; i < 6; i++ {
			if looksLikeRoot(dir) {
				return dir, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return "", fmt.Errorf("未找到 auto/worker/login.py，请在项目根目录启动")
}

func looksLikeRoot(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "auto", "worker", "login.py"))
	return err == nil
}

func workerEnv(cfg config.Config, freeCloak bool) []string {
	env := make([]string, 0, 32)
	for _, kv := range os.Environ() {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || browser.IsProxyEnv(k) {
			continue
		}
		if strings.EqualFold(k, "PYTHONIOENCODING") || strings.EqualFold(k, "PYTHONUTF8") || strings.EqualFold(k, "PYTHONUNBUFFERED") {
			continue
		}
		if strings.EqualFold(k, "CLOAKBROWSER_CACHE_DIR") {
			continue
		}
		if strings.EqualFold(k, "AUTO_API_TOKEN") || strings.EqualFold(k, "AUTO_TOKEN") {
			continue
		}
		if freeCloak && (strings.EqualFold(k, "CLOAKBROWSER_LICENSE_KEY") || strings.EqualFold(k, "CLOAKBROWSER_VERSION")) {
			continue
		}
		env = append(env, kv)
	}
	// The JSON pipe is UTF-8 in both directions. Windows redirected Python
	// streams otherwise use the system code page even when the console is UTF-8.
	env = append(env, "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1", "PYTHONUNBUFFERED=1")
	// Local payment/card callbacks use the same authenticated Auto API as the
	// manager. Keep this secret in the child environment, never in process args.
	env = append(env, "AUTO_API_TOKEN="+cfg.AutoToken)
	if !freeCloak {
		if key := browser.ResolveLicense(cfg.LicenseKey); key != "" {
			env = append(env, "CLOAKBROWSER_LICENSE_KEY="+key)
			if cfg.CloakVersion != "" {
				env = append(env, "CLOAKBROWSER_VERSION="+cfg.CloakVersion)
			}
		}
	}
	cacheDir := strings.TrimSpace(cfg.CloakCacheDir)
	if cacheDir == "" {
		cacheDir = strings.TrimSpace(os.Getenv("CLOAKBROWSER_CACHE_DIR"))
	}
	if cacheDir != "" {
		env = append(env, "CLOAKBROWSER_CACHE_DIR="+cacheDir)
	}
	if cfg.CloakBinaryPath != "" {
		env = append(env, "CLOAKBROWSER_BINARY_PATH="+cfg.CloakBinaryPath)
	}
	return env
}
