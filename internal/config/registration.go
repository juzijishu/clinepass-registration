package config

import (
	"fmt"
	"math"
	"net"
	"net/url"
	"strings"
)

type RegistrationSettings struct {
	Enabled             bool    `json:"enabled"`
	ManualSMS           bool    `json:"manual_sms"`
	SMSMaxAttempts      int     `json:"sms_max_attempts"`
	SMSWaitSeconds      int     `json:"sms_wait_seconds"`
	ManualWaitSeconds   int     `json:"manual_wait_seconds"`
	SMSAccountBudget    float64 `json:"sms_account_budget"`
	SMSBatchBudget      float64 `json:"sms_batch_budget"`
	TargetURL           string  `json:"target_url"`
	TargetAdminPassword string  `json:"target_admin_password"`
	TargetClientKeyID   string  `json:"target_client_key_id"`
}

func (r RegistrationSettings) Defaults() RegistrationSettings {
	if r.SMSMaxAttempts == 0 {
		r.SMSMaxAttempts = 3
	}
	if r.SMSWaitSeconds == 0 {
		r.SMSWaitSeconds = 120
	}
	if r.ManualWaitSeconds == 0 {
		r.ManualWaitSeconds = 1800
	}
	return r
}

func (r RegistrationSettings) Validate() error {
	if r.SMSMaxAttempts < 1 || r.SMSMaxAttempts > 20 || r.SMSWaitSeconds < 30 || r.SMSWaitSeconds > 600 {
		return fmt.Errorf("取号次数须为 1–20，接码等待须为 30–600 秒")
	}
	if r.ManualWaitSeconds < 60 || r.ManualWaitSeconds > 7200 {
		return fmt.Errorf("人工接码等待须为 60–7200 秒")
	}
	for _, v := range []float64{r.SMSAccountBudget, r.SMSBatchBudget} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 10000 {
			return fmt.Errorf("接码预算须为 0–10000 的有限金额")
		}
	}
	if r.TargetURL != "" {
		u, err := url.Parse(r.TargetURL)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
			return fmt.Errorf("号池地址须为不含凭据、路径或查询参数的服务地址")
		}
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback())) {
			return fmt.Errorf("号池地址须使用 HTTPS；本机回环地址可使用 HTTP")
		}
	}
	if r.Enabled && (r.TargetURL == "" || r.TargetAdminPassword == "" || strings.TrimSpace(r.TargetClientKeyID) == "") {
		return fmt.Errorf("启用注册流程前须配置号池地址、管理员密码和客户端密钥归属")
	}
	if r.Enabled && !r.ManualSMS && (r.SMSAccountBudget <= 0 || r.SMSBatchBudget <= 0) {
		return fmt.Errorf("启用注册流程前须配置接码预算")
	}
	return nil
}

// Subscription management is available independently of registration settings.
func (r RegistrationSettings) ValidateTarget() error {
	if strings.TrimSpace(r.TargetURL) == "" || strings.TrimSpace(r.TargetAdminPassword) == "" || strings.TrimSpace(r.TargetClientKeyID) == "" {
		return fmt.Errorf("请先配置号池地址、管理员密码和客户端密钥归属")
	}
	// Reuse URL validation without enabling unrelated registration requirements.
	return (RegistrationSettings{TargetURL: r.TargetURL}).Defaults().Validate()
}
