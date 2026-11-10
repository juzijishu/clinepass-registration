package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"github.com/juzijishu/clinepass-registration/internal/model"
	"testing"
)

func TestSubscriptionForwardPreservesTargetAndAuth(t *testing.T) {
	reached := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		if r.URL.Path != "/api/accounts/synthetic-id/subscription" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer auto-only" {
			t.Error("incorrect subscription forwarding")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{}` {
			t.Error("payload changed")
		}
		w.WriteHeader(202)
		io.WriteString(w, `{"kind":"subscription","status":"queued"}`)
	}))
	defer upstream.Close()
	s := newAutoTestServer(t, model.AutoConnection{URL: upstream.URL, Token: "auto-only"})
	if w := autoTestRequest(s, "POST", "/api/auto/accounts/synthetic-id/subscription", `{}`); w.Code != 202 || !reached {
		t.Fatalf("forward status %d", w.Code)
	}
}
