package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/juzijishu/clinepass-registration/internal/model"
)

func TestBatchPreviewIsForwardedToAuto(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/batches/preview" || r.Header.Get("Authorization") != "Bearer auto-only" {
			t.Error("invalid preview forwarding")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"text":"synthetic-input"}` {
			t.Error("preview payload changed")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"total":1,"importable":1,"items":[]}`)
	}))
	defer upstream.Close()
	s := newAutoTestServer(t, model.AutoConnection{URL: upstream.URL, Token: "auto-only"})
	w := autoTestRequest(s, http.MethodPost, "/api/batches/preview", `{"text":"synthetic-input"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"importable":1`) {
		t.Fatalf("preview forwarding status = %d", w.Code)
	}
}
