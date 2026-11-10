package config

import (
	"strings"
	"testing"
)

func manualBase() RegistrationSettings {
	return RegistrationSettings{
		Enabled:             true,
		ManualSMS:           true,
		TargetURL:           "https://pool.example.com",
		TargetAdminPassword: "synthetic-password",
		TargetClientKeyID:   "legacy",
	}
}

func TestManualSMSDefaultsAndValidation(t *testing.T) {
	r := RegistrationSettings{}.Defaults()
	if r.ManualWaitSeconds != 1800 {
		t.Fatalf("manual wait default=%d, want 1800", r.ManualWaitSeconds)
	}
	if r.ManualSMS {
		t.Fatal("manual SMS must stay opt-in")
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("unset defaults must validate: %v", err)
	}
}

func TestManualWaitSecondsRange(t *testing.T) {
	for _, bad := range []int{-1, 1, 59, 7201, 100000} {
		r := RegistrationSettings{ManualWaitSeconds: bad}.Defaults()
		if err := r.Validate(); err == nil {
			t.Fatalf("manual wait %d must be rejected", bad)
		}
	}
	for _, ok := range []int{60, 900, 1800, 7200} {
		r := RegistrationSettings{ManualWaitSeconds: ok}.Defaults()
		if err := r.Validate(); err != nil {
			t.Fatalf("manual wait %d rejected: %v", ok, err)
		}
	}
}

func TestManualSMSNeedsNoSMSBudget(t *testing.T) {
	r := manualBase().Defaults()
	if r.SMSAccountBudget != 0 || r.SMSBatchBudget != 0 {
		t.Fatal("fixture must start without budgets")
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("manual SMS must not require a dialling budget: %v", err)
	}
}

func TestAutomaticSMSStillNeedsBudget(t *testing.T) {
	r := manualBase()
	r.ManualSMS = false
	r = r.Defaults()
	err := r.Validate()
	if err == nil {
		t.Fatal("automatic SMS without budget must be rejected")
	}
	if msg := err.Error(); !strings.Contains(msg, "接码预算") {
		t.Fatalf("unexpected message: %s", msg)
	}
	r.SMSAccountBudget = 1
	r.SMSBatchBudget = 10
	if err := r.Validate(); err != nil {
		t.Fatalf("automatic SMS with budgets rejected: %v", err)
	}
}

// The pool target is required in both modes: registration is never useful
// without somewhere to deliver the key.
func TestEnabledRegistrationAlwaysNeedsPoolTarget(t *testing.T) {
	for _, manual := range []bool{true, false} {
		r := manualBase()
		r.ManualSMS = manual
		r.TargetURL = ""
		r = r.Defaults()
		if err := r.Validate(); err == nil {
			t.Fatalf("manual=%v: missing pool target must be rejected", manual)
		}
	}
}
