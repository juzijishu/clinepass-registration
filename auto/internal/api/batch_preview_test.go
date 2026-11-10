package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/juzijishu/clinepass-registration/internal/model"
)

func TestBatchPreviewMasksCredentialsAndDoesNotWrite(t *testing.T) {
	s := testServer(t)
	body, _ := json.Marshal(model.CreateBatchInput{LoginProvider: "google", Text: "\ufeff# accounts\r\n\r\nalice@outlook.com----synthetic-password----11111111-2222-3333-4444-555555555555----synthetic-refresh----recovery@example.test----supplier-extra\r\n"})
	w := request(s, http.MethodPost, "/api/batches/preview", string(body), s.cfg.AutoToken)
	if w.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200", w.Code)
	}
	var wire struct {
		Total      int `json:"total"`
		Importable int `json:"importable"`
		Items      []struct {
			Line     int    `json:"line"`
			Email    string `json:"email"`
			Provider string `json:"login_provider"`
			Status   string `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Total != 1 || wire.Importable != 1 || len(wire.Items) != 1 || wire.Items[0].Line != 3 || wire.Items[0].Provider != "microsoft" || wire.Items[0].Status != "valid" || wire.Items[0].Email != "a***@outlook.com" {
		t.Fatal("preview must preserve line numbers, detect Microsoft and mask email")
	}
	for _, private := range []string{"alice@", "synthetic-password", "synthetic-refresh", "supplier-extra", "recovery@example.test", "11111111-2222"} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatal("private import field leaked")
		}
	}
	count, err := s.store.CountBatches()
	if err != nil || count != 0 || len(s.jobs.List()) != 0 {
		t.Fatal("preview must not write or schedule work")
	}
	if request(s, http.MethodPost, "/api/batches/preview", string(body), "").Code != http.StatusUnauthorized {
		t.Fatal("preview requires Auto authentication")
	}
}

func TestBatchPreviewReportsFileAndStoredDuplicatesWithoutReplacingCredentials(t *testing.T) {
	s := testServer(t)
	_, _, err := s.store.CreateBatch(model.CreateBatchInput{Text: "existing@example.test----original-password"})
	if err != nil {
		t.Fatal(err)
	}
	in := model.CreateBatchInput{LoginProvider: "microsoft", Text: "existing@example.test----replacement\nnew@example.test----first-password\nNEW@example.test----second-password"}
	body, _ := json.Marshal(in)
	w := request(s, http.MethodPost, "/api/batches/preview", string(body), s.cfg.AutoToken)
	var out struct{ Total, Importable, Duplicates, Invalid int }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Total != 3 || out.Importable != 1 || out.Duplicates != 2 || out.Invalid != 0 {
		t.Fatalf("duplicate preview status = %d", w.Code)
	}
	w = request(s, http.MethodPost, "/api/batches", string(body), s.cfg.AutoToken)
	var created struct {
		Batch  model.BatchSummary `json:"batch"`
		Errors []string           `json:"errors"`
	}
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &created) != nil || created.Batch.Total != 1 || len(created.Errors) != 2 {
		t.Fatalf("duplicate import status = %d", w.Code)
	}
	a, err := s.store.GetByEmail("existing@example.test")
	if err != nil || a.Password != "original-password" {
		t.Fatal("import overwrote existing credentials")
	}
	a, err = s.store.GetByEmail("new@example.test")
	if err != nil || a.Password != "first-password" || a.LoginProvider != "microsoft" {
		t.Fatal("duplicate import must preserve the first row and chosen provider")
	}
	if len(s.jobs.List()) != 0 {
		t.Fatal("import must not schedule work")
	}
}

func TestBatchPreviewAndImportShareValidation(t *testing.T) {
	for _, text := range []string{
		"valid@example.test----pw\nnot-an-email----synthetic-private",
		"valid@example.test----pw\nName <other@example.test>----synthetic-private",
		"valid@example.test----pw\nother@example.test----pw----bad-recovery",
		"valid@example.test----pw\nother@example.test----pw----extra----synthetic-private",
	} {
		s := testServer(t)
		body, _ := json.Marshal(model.CreateBatchInput{Text: text})
		w := request(s, http.MethodPost, "/api/batches/preview", string(body), s.cfg.AutoToken)
		var out struct {
			Invalid, Importable int
			Items               []struct {
				Line   int
				Status string
			}
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Invalid != 1 || out.Importable != 1 || len(out.Items) != 2 || out.Items[1].Line != 2 || out.Items[1].Status != "invalid" {
			t.Fatalf("invalid row preview status = %d", w.Code)
		}
		if strings.Contains(w.Body.String(), "synthetic-private") {
			t.Fatal("validation echoed raw credentials")
		}
		w = request(s, http.MethodPost, "/api/batches", string(body), s.cfg.AutoToken)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid import status = %d", w.Code)
		}
		count, _ := s.store.CountBatches()
		if count != 0 {
			t.Fatal("invalid input created a partial batch")
		}
	}
}

func TestBatchImportLimitsAndProviderAreValidatedForPreviewAndSave(t *testing.T) {
	s := testServer(t)
	for _, in := range []model.CreateBatchInput{
		{Text: "# empty\n"},
		{Text: strings.Repeat("a@example.test----pw\n", 1001)},
		{Text: "a@example.test----" + strings.Repeat("x", 1<<20)},
		{Text: "a@example.test----pw", LoginProvider: "unsupported"},
	} {
		body, _ := json.Marshal(in)
		for _, path := range []string{"/api/batches/preview", "/api/batches"} {
			w := request(s, http.MethodPost, path, string(body), s.cfg.AutoToken)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("%s accepted invalid import: %d", path, w.Code)
			}
		}
	}
	count, _ := s.store.CountBatches()
	if count != 0 {
		t.Fatal("invalid import persisted")
	}
}
