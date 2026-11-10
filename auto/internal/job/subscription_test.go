package job

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"github.com/juzijishu/clinepass-registration/internal/cline"
	"github.com/juzijishu/clinepass-registration/internal/config"
	"github.com/juzijishu/clinepass-registration/internal/model"
	"github.com/juzijishu/clinepass-registration/internal/store"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func subscriptionAccount(t *testing.T) (*store.Store, model.Account) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "subscription.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	a, err := s.CreatePaidAccount(model.CreatePaidAccountInput{Email: "owner@example.com", CookieHeader: "session=synthetic", APIKey: "previous-key"})
	if err != nil {
		t.Fatal(err)
	}
	return s, a
}

func TestSubscriptionUsesSharedQueueButNeverAutoPayOrBrowserLogin(t *testing.T) {
	s, a := subscriptionAccount(t)
	m := New(config.Config{Registration: config.RegistrationSettings{TargetURL: "http://127.0.0.1:1", TargetAdminPassword: "synthetic", TargetClientKeyID: "legacy"}}, s)
	m.HoldPump()
	settings, _ := s.GetSettings()
	settings.Registration = m.cfg.Registration
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if _, err := m.EnqueueSubscription(a.ID); err != nil {
		t.Fatal(err)
	}
	jobs := m.List()
	if len(jobs) != 1 || jobs[0].Kind != "subscription" || jobs[0].AutoPay {
		t.Fatal("incorrect job kind or payment enabled")
	}
	if _, err := m.EnqueueCookie(a.ID); err == nil {
		t.Fatal("same account queued twice")
	}
}

func TestSubscriptionQueueRunsDirectSessionCheckAndPublishesSafeFailure(t *testing.T) {
	s, a := subscriptionAccount(t)
	official := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/users/me" {
			t.Error("unexpected subscription action")
		}
		w.WriteHeader(401)
		fmt.Fprint(w, "synthetic-private-response")
	}))
	defer official.Close()
	previous := http.DefaultTransport
	transport := previous.(*http.Transport).Clone()
	transport.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "api.cline.bot:443" {
			return nil, fmt.Errorf("unexpected destination")
		}
		cfg := official.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
		cfg.ServerName = official.Certificate().DNSNames[0]
		return (&tls.Dialer{Config: cfg}).DialContext(ctx, network, official.Listener.Addr().String())
	}
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = previous; transport.CloseIdleConnections() }()
	settings, _ := s.GetSettings()
	settings.Registration = config.RegistrationSettings{TargetURL: "http://127.0.0.1:1", TargetAdminPassword: "synthetic", TargetClientKeyID: "legacy"}
	s.SaveSettings(settings)
	m := New(config.Config{}, s)
	queued, err := m.EnqueueSubscription(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		j, _ := m.Get(queued.ID)
		if j.Status == "failed" {
			if j.Stage != "subscription" || j.AutoPay || strings.Contains(j.Error, "synthetic-private-response") {
				t.Fatal("unsafe job state")
			}
			for _, event := range j.Logs {
				if strings.Contains(event.Message, "synthetic-private-response") {
					t.Fatal("response leaked to job feed")
				}
			}
			saved, _ := s.Get(a.ID)
			if saved.APIKey != "previous-key" || saved.CookieHeader != "session=synthetic" {
				t.Fatal("existing auth state lost")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("subscription job did not finish")
}

func TestSubscriptionPersistsKeyBeforeImportAndReusesItAfterFailure(t *testing.T) {
	s, a := subscriptionAccount(t)
	m := New(config.Config{}, s)
	creates := 0
	cancelled := false
	var calls []string
	official := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/v1/users/me":
			fmt.Fprint(w, `{"success":true,"data":{"id":"usr-test","email":"owner@example.com"}}`)
		case "/api/v1/users/me/plan":
			if r.Method == "DELETE" {
				cancelled = true
				w.WriteHeader(204)
				return
			}
			cancel := "null"
			end := time.Now().Add(time.Hour).Unix()
			if cancelled {
				cancel = fmt.Sprint(end)
			}
			fmt.Fprintf(w, `{"success":true,"data":{"subscriptionId":"sub-test","currentPeriodEnd":%d,"cancelAt":%s,"plan":{"id":"test"}}}`, end, cancel)
		case "/api/v1/api-keys":
			creates++
			fmt.Fprint(w, `{"success":true,"data":{"secret_key":"created-synthetic-key"}}`)
		}
	}))
	defer official.Close()
	failTarget := true
	var snapshot map[string]any
	imported := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failTarget {
			w.WriteHeader(503)
			fmt.Fprint(w, "synthetic-secret-error")
			return
		}
		switch r.URL.Path {
		case "/api/auth/login":
			fmt.Fprint(w, `{"ok":true,"csrf":"synthetic"}`)
		case "/api/auth/logout":
			fmt.Fprint(w, `{"ok":true}`)
		case "/api/security/client-keys":
			fmt.Fprint(w, `{"keys":[{"id":"legacy"}]}`)
		case "/api/accounts":
			if r.Method == "POST" {
				cp, _ := s.GetSubscription(a.ID)
				saved, _ := s.Get(a.ID)
				if cp.Key != "created-synthetic-key" || saved.APIKey != cp.Key {
					t.Error("import happened before durable key save")
				}
				json.NewDecoder(r.Body).Decode(&snapshot)
				snapshot["configurationRevision"] = strings.Repeat("a", 64)
				imported = true
				fmt.Fprint(w, `{"ok":true}`)
				return
			}
			if snapshot == nil {
				fmt.Fprintf(w, `{"accounts":[],"configurationRevision":"%s","mode":"round-robin","active":0,"concurrencyWaitMs":2000,"poolFullWaitMs":null,"errorRules":[],"retryRules":[],"accountPipeline":{},"accountWorkflow":{},"quotaProtection":{},"sessionProviderAffinityEnabled":false}`, strings.Repeat("a", 64))
			} else {
				json.NewEncoder(w).Encode(snapshot)
			}
		}
	}))
	defer target.Close()
	cfg := config.Config{Registration: config.RegistrationSettings{TargetURL: target.URL, TargetAdminPassword: "synthetic", TargetClientKeyID: "legacy"}}
	c := cline.NewDirect(a.CookieHeader)
	c.SetBase(official.URL, official.URL)
	report := func(string, string) {}
	err := m.completeSubscription(a, cfg, c, report)
	if err == nil || strings.Contains(err.Error(), "synthetic-secret-error") {
		t.Fatal("target error missing or leaked")
	}
	if strings.Join(calls, ",") != "GET /api/v1/users/me,GET /api/v1/users/me/plan,DELETE /api/v1/users/me/plan,GET /api/v1/users/me/plan,POST /api/v1/api-keys" {
		t.Fatal("incorrect lifecycle order")
	}
	cp, _ := s.GetSubscription(a.ID)
	if cp.State != "key_saved" || creates != 1 || imported {
		t.Fatal("checkpoint not retained")
	}
	failTarget = false
	a, _ = s.Get(a.ID)
	if err = m.completeSubscription(a, cfg, c, report); err != nil {
		t.Fatal(err)
	}
	cp, _ = s.GetSubscription(a.ID)
	if creates != 1 || !imported || cp.State != "imported" {
		t.Fatal("retry must reuse saved key and confirm import")
	}
}

func TestSubscriptionStopsBeforeKeyOnUncertainCancellationAndNeverRetriesAmbiguousCreation(t *testing.T) {
	for _, mode := range []string{"cancel-unconfirmed", "key-unknown", "wrong-profile"} {
		t.Run(mode, func(t *testing.T) {
			s, a := subscriptionAccount(t)
			m := New(config.Config{}, s)
			creates := 0
			cancelled := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/users/me":
					email := "owner@example.com"
					if mode == "wrong-profile" {
						email = "other@example.com"
					}
					fmt.Fprintf(w, `{"success":true,"data":{"id":"usr-test","email":"%s"}}`, email)
				case "/api/v1/users/me/plan":
					if r.Method == "DELETE" {
						cancelled = true
						w.WriteHeader(204)
						return
					}
					end := time.Now().Add(time.Hour).Unix()
					cancel := "null"
					if cancelled && mode != "cancel-unconfirmed" {
						cancel = fmt.Sprint(end)
					}
					fmt.Fprintf(w, `{"success":true,"data":{"subscriptionId":"sub-test","currentPeriodEnd":%d,"cancelAt":%s,"plan":{}}}`, end, cancel)
				case "/api/v1/api-keys":
					creates++
					w.WriteHeader(502)
					fmt.Fprint(w, "synthetic-private-response")
				}
			}))
			defer server.Close()
			cfg := config.Config{Registration: config.RegistrationSettings{TargetURL: server.URL, TargetAdminPassword: "synthetic", TargetClientKeyID: "legacy"}}
			c := cline.NewDirect(a.CookieHeader)
			c.SetBase(server.URL, server.URL)
			for i := 0; i < 2; i++ {
				if err := m.completeSubscription(a, cfg, c, func(string, string) {}); err == nil || strings.Contains(err.Error(), "synthetic-private-response") {
					t.Fatal("uncertain result accepted or leaked")
				}
			}
			want := 0
			if mode == "key-unknown" {
				want = 1
			}
			if creates != want {
				t.Fatal("unsafe key creation or duplicate create")
			}
			saved, _ := s.Get(a.ID)
			if saved.APIKey != "previous-key" {
				t.Fatal("old key overwritten on failure")
			}
		})
	}
}
