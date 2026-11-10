package store

import (
	"github.com/juzijishu/clinepass-registration/internal/model"
	"path/filepath"
	"testing"
)

func TestSubscriptionCheckpointSurvivesRestartAndPreservesAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subscription.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := s.CreateBatch(model.CreateBatchInput{Text: "owner@example.com----synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.ListByBatch(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	a := list[0]
	a.CookieHeader = "session=synthetic"
	a.PaymentURL = "https://example.com/checkout"
	a.APIKey = "old-key"
	if err = s.SaveLoginResult(a); err != nil {
		t.Fatal(err)
	}
	if err = s.BeginSubscriptionKey(a.ID, "usr-test"); err != nil {
		t.Fatal(err)
	}
	if err = s.BeginSubscriptionKey(a.ID, "usr-test"); err == nil {
		t.Fatal("uncertain creation must not be repeated")
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	checkpoint, err := s.GetSubscription(a.ID)
	if err != nil || checkpoint.State != "key_pending" {
		t.Fatal("pending checkpoint lost")
	}
	if err = s.SaveSubscriptionKey(a.ID, "usr-other", "new-key"); err == nil {
		t.Fatal("wrong user accepted")
	}
	if err = s.SaveSubscriptionKey(a.ID, "usr-test", "new-key"); err != nil {
		t.Fatal(err)
	}
	a, err = s.Get(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.APIKey != "new-key" || a.UserID != "usr-test" || a.CookieHeader != "session=synthetic" || a.Password != "synthetic" || a.PaymentURL != "https://example.com/checkout" {
		t.Fatal("account fields lost")
	}
	checkpoint, err = s.GetSubscription(a.ID)
	if err != nil || checkpoint.State != "key_saved" || checkpoint.Key != "new-key" {
		t.Fatal("key not atomically checkpointed")
	}
	if err = s.MarkSubscriptionImported(a.ID); err != nil {
		t.Fatal(err)
	}
	checkpoint, _ = s.GetSubscription(a.ID)
	if checkpoint.State != "imported" {
		t.Fatal("completion not persisted")
	}
}
