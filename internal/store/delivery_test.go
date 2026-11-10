package store

import (
	"encoding/json"
	"strings"
	"path/filepath"
	"testing"

	"github.com/juzijishu/clinepass-registration/internal/model"
)

func TestMicrosoftDeliveryRecoveryIsFifthColumn(t *testing.T) {
	email, password, recovery := ParseRaw("a@outlook.com----pw----11111111-2222-3333-4444-555555555555----synthetic-refresh----recovery@example.test----extra")
	if email != "a@outlook.com" || password != "pw" || recovery != "recovery@example.test" {
		t.Fatal("six-column delivery must use column five for recovery email")
	}
}

func TestMicrosoftDeliveryPrivateRoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil { t.Fatal(err) }
	defer s.Close()
	b, _, err := s.CreateBatch(model.CreateBatchInput{Text:"\ufeffa@outlook.com----pw----11111111-2222-3333-4444-555555555555----synthetic-refresh----recovery@example.test----supplier-extra\r\n"})
	if err != nil { t.Fatal(err) }
	accounts, err := s.ListByBatch(b.ID)
	if err != nil || len(accounts)!=1 { t.Fatal("private account round trip failed") }
	a:=accounts[0]
	if a.OAuthClientID!="11111111-2222-3333-4444-555555555555" || a.OAuthRefreshToken!="synthetic-refresh" || a.DeliveryExtra!="supplier-extra" || a.LoginProvider!="microsoft" { t.Fatal("private delivery credentials were lost") }
	for _, value := range []any{a, a.Public(), a.ListPublic()} {
		raw, err:=json.Marshal(value)
		if err!=nil {t.Fatal(err)}
		if strings.Contains(string(raw),"synthetic-refresh") || strings.Contains(string(raw),"supplier-extra") {t.Fatal("delivery credentials leaked into JSON")}
	}
}

func TestBatchRejectsMalformedDeliveryBeforeWriting(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil { t.Fatal(err) }
	defer s.Close()
	_, _, err = s.CreateBatch(model.CreateBatchInput{LoginProvider:"microsoft", Text:"valid@outlook.com----pw\ninvalid@outlook.com----pw----not-a-client-id----token----recovery@example.test----extra"})
	if err == nil { t.Fatal("malformed six-column credentials must reject the batch") }
	count, err := s.CountBatches()
	if err != nil || count != 0 { t.Fatal("validation failure must not create a partial batch") }
}
