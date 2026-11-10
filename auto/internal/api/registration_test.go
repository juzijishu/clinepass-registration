package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestRegistrationConfigMasksPasswordAndValidatesBeforeSave(t *testing.T) {
	s := testServer(t)
	w := request(s, http.MethodPatch, "/api/config", `{"registration":{"enabled":false,"sms_max_attempts":2,"sms_wait_seconds":90,"sms_account_budget":1.5,"sms_batch_budget":10,"target_url":"http://127.0.0.1:9997","target_admin_password":"synthetic-private-password","target_client_key_id":"legacy"}}`, s.cfg.AutoToken)
	if w.Code != 200 {
		t.Fatalf("workflow config not accepted: %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "synthetic-private-password") {
		t.Fatal("target password leaked")
	}
	w = request(s, http.MethodPatch, "/api/config", `{"registration":{"sms_max_attempts":-1}}`, s.cfg.AutoToken)
	if w.Code != 400 {
		t.Fatal("negative attempts must fail")
	}
	w = request(s, http.MethodGet, "/api/config", "", s.cfg.AutoToken)
	if !strings.Contains(w.Body.String(), `"sms_max_attempts":2`) {
		t.Fatal("invalid save changed accepted settings")
	}
}

func TestSMSCountryMustBeEnglandAndInvalidSavePreservesConfiguration(t *testing.T) {
	s := testServer(t)
	w := request(s, http.MethodPatch, "/api/config", `{"hero_sms_country":16}`, s.cfg.AutoToken)
	if w.Code != 200 {
		t.Fatal("England rejected")
	}
	w = request(s, http.MethodPatch, "/api/config", `{"hero_sms_country":4,"hero_sms_service":"changed"}`, s.cfg.AutoToken)
	if w.Code != 400 {
		t.Fatal("non-England country accepted")
	}
	settings, err := s.store.GetSettings()
	if err != nil || settings.HeroSMSCountry != 16 || settings.HeroSMSService == "changed" {
		t.Fatal("rejected configuration was persisted")
	}
}

// 人工接码不调用 HeroSMS：没有密钥、地区或限价也必须能保存并启用。
func TestManualSMSEnablesWithoutHeroSMSCredentials(t *testing.T) {
	s := testServer(t)
	body := `{"registration":{"enabled":true,"manual_sms":true,"manual_wait_seconds":600,` +
		`"target_url":"http://127.0.0.1:9997","target_admin_password":"synthetic-password","target_client_key_id":"legacy"}}`
	w := request(s, http.MethodPatch, "/api/config", body, s.cfg.AutoToken)
	if w.Code != 200 {
		t.Fatalf("manual SMS workflow rejected: %d %s", w.Code, w.Body.String())
	}
	settings, err := s.store.GetSettings()
	if err != nil || !settings.Registration.ManualSMS || !settings.Registration.Enabled {
		t.Fatalf("manual SMS workflow not persisted: %+v (%v)", settings.Registration, err)
	}
	if settings.Registration.ManualWaitSeconds != 600 {
		t.Fatalf("manual wait not saved: %d", settings.Registration.ManualWaitSeconds)
	}
	if w.Body.String() == "" || !strings.Contains(w.Body.String(), `"manual_sms":true`) {
		t.Fatal("manual mode missing from public config")
	}

	// 关掉人工接码后仍要求完整短信配置，避免半配置状态被误当成自动模式。
	off := `{"registration":{"enabled":true,"manual_sms":false,` +
		`"target_url":"http://127.0.0.1:9997","target_admin_password":"synthetic-password","target_client_key_id":"legacy"}}`
	if w = request(s, http.MethodPatch, "/api/config", off, s.cfg.AutoToken); w.Code != 400 {
		t.Fatalf("automatic SMS without HeroSMS credentials must fail, got %d", w.Code)
	}
}

func TestManualSMSWaitSecondsRejectedOutsideRange(t *testing.T) {
	s := testServer(t)
	for _, bad := range []string{"1", "-5", "7201"} {
		body := `{"registration":{"manual_sms":true,"manual_wait_seconds":` + bad + `}}`
		if w := request(s, http.MethodPatch, "/api/config", body, s.cfg.AutoToken); w.Code != 400 {
			t.Fatalf("manual wait %s accepted with %d", bad, w.Code)
		}
	}
}
