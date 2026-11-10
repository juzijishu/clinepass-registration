package api

import (
	"encoding/json"
	"net/http"
	"github.com/juzijishu/clinepass-registration/internal/config"
	"github.com/juzijishu/clinepass-registration/internal/model"
	"testing"
)

func TestSubscriptionAPIRequiresSessionConfigAndAuthentication(t *testing.T) {
	s := testServer(t)
	s.jobs.HoldPump()
	a, err := s.store.CreatePaidAccount(model.CreatePaidAccountInput{Email: "owner@example.com", CookieHeader: "session=synthetic", APIKey: "previous"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/accounts/" + a.ID + "/subscription"
	if request(s, "POST", path, `{}`, "").Code != http.StatusUnauthorized {
		t.Fatal("missing authentication")
	}
	if request(s, "POST", path, `{}`, s.cfg.AutoToken).Code != http.StatusBadRequest {
		t.Fatal("incomplete config accepted")
	}
	settings, _ := s.store.GetSettings()
	settings.Registration = config.RegistrationSettings{TargetURL: "http://127.0.0.1:1", TargetAdminPassword: "synthetic", TargetClientKeyID: "legacy"}
	s.store.SaveSettings(settings)
	if request(s, "POST", path, `{"auto_pay":true}`, s.cfg.AutoToken).Code != http.StatusBadRequest {
		t.Fatal("unexpected options accepted")
	}
	w := request(s, "POST", path, `{}`, s.cfg.AutoToken)
	var j model.Job
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &j) != nil || j.Kind != "subscription" || j.AutoPay {
		t.Fatalf("enqueue status %d", w.Code)
	}
	if request(s, "POST", path, `{}`, s.cfg.AutoToken).Code != http.StatusBadRequest {
		t.Fatal("duplicate accepted")
	}
}
