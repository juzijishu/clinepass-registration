package clinebypass

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func targetFixture() map[string]any {
	return map[string]any{
		"accounts": []any{map[string]any{"id": "existing", "name": "保留", "key": "old-synthetic", "clientKeyId": "legacy", "enabled": false, "note": "retained", "proxyUrl": "socks5://127.0.0.1:9", "headers": map[string]any{"X-Example": "retained"}, "perModel": map[string]any{"model-a": map[string]any{"providers": []any{}}}, "maxConcurrent": 2, "weight": 3, "priority": 4}},
		"mode":     "round-robin", "active": 0, "concurrencyWaitMs": 1234, "poolFullWaitMs": nil,
		"errorRules": []any{}, "retryRules": []any{}, "accountPipeline": map[string]any{"sticky": false}, "accountWorkflow": map[string]any{"enabled": false}, "quotaProtection": map[string]any{"enabled": true}, "sessionProviderAffinityEnabled": false, "configurationRevision": strings.Repeat("a", 64),
	}
}

func TestAppendRetainsFullSnapshotAndReconcilesConflictOrLostResponse(t *testing.T) {
	for _, mode := range []string{"normal", "conflict", "lost-response"} {
		t.Run(mode, func(t *testing.T) {
			snapshot := targetFixture()
			saved := snapshot["accounts"].([]any)[0]
			posts := 0
			loggedOut := false
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/auth/login" {
					var in map[string]string
					json.NewDecoder(r.Body).Decode(&in)
					if len(in) != 1 || in["password"] != "synthetic-password" {
						t.Error("incorrect login contract")
					}
					http.SetCookie(w, &http.Cookie{Name: "session", Value: "synthetic", Path: "/"})
					fmt.Fprint(w, `{"ok":true,"pending":false,"csrf":"synthetic-csrf"}`)
					return
				}
				if cookie, err := r.Cookie("session"); err != nil || cookie.Value != "synthetic" {
					t.Error("missing session")
				}
				if r.Method == "POST" && (r.Header.Get("X-CSRF-Token") != "synthetic-csrf" || r.Header.Get("Origin") == "") {
					t.Error("missing CSRF/origin")
				}
				switch r.URL.Path {
				case "/api/auth/logout":
					loggedOut = true
					fmt.Fprint(w, `{"ok":true}`)
				case "/api/security/client-keys":
					fmt.Fprint(w, `{"keys":[{"id":"legacy","name":"Legacy"}]}`)
				case "/api/accounts":
					if r.Method == "GET" {
						json.NewEncoder(w).Encode(snapshot)
						return
					}
					posts++
					var body map[string]any
					json.NewDecoder(r.Body).Decode(&body)
					if body["expectedConfigurationRevision"] != snapshot["configurationRevision"] {
						t.Error("missing revision")
					}
					oldJSON, _ := json.Marshal(saved)
					newJSON, _ := json.Marshal(body["accounts"].([]any)[0])
					if string(oldJSON) != string(newJSON) {
						t.Error("existing account changed")
					}
					if _, ok := body["accounts"].([]any)[1].(map[string]any)["id"]; ok {
						t.Error("target API rejects caller-chosen IDs for new accounts")
					}
					for _, key := range []string{"mode", "active", "concurrencyWaitMs", "poolFullWaitMs", "errorRules", "retryRules", "accountPipeline", "accountWorkflow", "quotaProtection", "sessionProviderAffinityEnabled"} {
						a, _ := json.Marshal(body[key])
						b, _ := json.Marshal(snapshot[key])
						if string(a) != string(b) {
							t.Errorf("lost %s", key)
						}
					}
					if mode == "conflict" && posts == 1 {
						snapshot["configurationRevision"] = strings.Repeat("b", 64)
						snapshot["mode"] = "single"
						w.WriteHeader(409)
						return
					}
					snapshot["accounts"] = body["accounts"]
					if mode == "lost-response" {
						h, _, _ := w.(http.Hijacker).Hijack()
						h.Close()
						return
					}
					fmt.Fprint(w, `{"ok":true}`)
				default:
					t.Error("unexpected route")
					w.WriteHeader(404)
				}
			}))
			defer s.Close()
			c, err := New(s.URL, "synthetic-password", "legacy")
			if err != nil {
				t.Fatal(err)
			}
			if err = c.Append("account-test", "synthetic-new-key"); err != nil {
				t.Fatal(err)
			}
			if len(snapshot["accounts"].([]any)) != 2 || !loggedOut {
				t.Fatal("append/readback/logout incomplete")
			}
			if err = c.Append("account-test", "synthetic-new-key"); err != nil {
				t.Fatal(err)
			}
			want := 1
			if mode == "conflict" {
				want = 2
			}
			if posts != want {
				t.Fatal("duplicate write or conflict not retried")
			}
		})
	}
}

func TestAppendRejectsPendingLoginForeignOwnerAndRedirect(t *testing.T) {
	for _, mode := range []string{"pending", "foreign-owner", "redirect", "missing-revision", "unknown-owner"} {
		t.Run(mode, func(t *testing.T) {
			writes := 0
			leaked := false
			sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
			defer sink.Close()
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/auth/login":
					if mode == "redirect" {
						http.Redirect(w, r, sink.URL, 307)
						return
					}
					fmt.Fprintf(w, `{"ok":true,"pending":%t,"csrf":"test"}`, mode == "pending")
				case "/api/auth/logout":
					fmt.Fprint(w, `{"ok":true}`)
				case "/api/security/client-keys":
					if mode == "unknown-owner" {
						fmt.Fprint(w, `{"keys":[]}`)
					} else {
						fmt.Fprint(w, `{"keys":[{"id":"legacy"}]}`)
					}
				case "/api/accounts":
					if r.Method == "POST" {
						writes++
						fmt.Fprint(w, `{"ok":true}`)
						return
					}
					snapshot := targetFixture()
					if mode == "foreign-owner" {
						snapshot["accounts"] = []any{map[string]any{"key": "synthetic-new-key", "clientKeyId": "other"}}
					}
					if mode == "missing-revision" {
						delete(snapshot, "configurationRevision")
					}
					json.NewEncoder(w).Encode(snapshot)
				}
			}))
			defer s.Close()
			c, err := New(s.URL, "synthetic-password", "legacy")
			if err != nil {
				t.Fatal(err)
			}
			err = c.Append("account-test", "synthetic-new-key")
			if err == nil || writes != 0 || leaked {
				t.Fatal("unsafe target accepted")
			}
			if strings.Contains(err.Error(), "synthetic-password") || strings.Contains(err.Error(), "synthetic-new-key") {
				t.Fatal("secret leaked")
			}
		})
	}
}
