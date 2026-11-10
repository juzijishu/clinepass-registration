package store

import (
	"path/filepath"
	"testing"
)

func TestSMSDebitAccumulatesPerAccountAndBatch(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.AddSMSDebit("a1", "b1", 0.06); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSMSDebit("a1", "b1", 0.04); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSMSDebit("a2", "b1", 1.5); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSMSDebit("a3", "b2", 9); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSMSDebit("a9", "", 0); err != nil {
		t.Fatal("zero cost must be ignored:", err)
	}

	account, batch, err := s.SMSSpent("a1", "b1")
	if err != nil {
		t.Fatal(err)
	}
	if account != 10 || batch != 160 {
		t.Fatalf("account=%d batch=%d, want 10 and 160 cents", account, batch)
	}

	// 没有批次归属的账号不应把其他批次的花费算进来。
	if _, batch, err = s.SMSSpent("a9", ""); err != nil || batch != 0 {
		t.Fatalf("empty batch should report 0, got %d (err=%v)", batch, err)
	}
}
