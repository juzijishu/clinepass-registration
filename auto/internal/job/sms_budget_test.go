package job

import (
	"path/filepath"
	"testing"

	"github.com/juzijishu/clinepass-registration/internal/config"
	"github.com/juzijishu/clinepass-registration/internal/model"
	"github.com/juzijishu/clinepass-registration/internal/store"
)

func budgetManager(t *testing.T) (*Manager, model.Account) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "budget.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	a, err := s.CreatePaidAccount(model.CreatePaidAccountInput{Email: "budget@example.com", CookieHeader: "session=synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	return New(config.Config{}, s), a
}

func TestSMSBudgetUnlimitedWhenBothBudgetsAreZero(t *testing.T) {
	m, acc := budgetManager(t)
	b, err := m.smsBudget(config.Config{Registration: config.RegistrationSettings{SMSAccountBudget: 0, SMSBatchBudget: 0}}, acc)
	if err != nil {
		t.Fatal(err)
	}
	if b.AccountRemain != 0 || b.BatchRemain != 0 {
		t.Fatalf("zero budgets must mean unlimited, got %+v", b)
	}
}

func TestSMSBudgetSubtractsSpentAndCapsAttempts(t *testing.T) {
	m, acc := budgetManager(t)
	if err := m.store.AddSMSDebit(acc.ID, acc.BatchID, 0.30); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		HeroSMSMaxPrice: 0.05,
		Registration: config.RegistrationSettings{
			SMSMaxAttempts: 5, SMSAccountBudget: 0.35, SMSBatchBudget: 1,
		},
	}
	b, err := m.smsBudget(cfg, acc)
	if err != nil {
		t.Fatal(err)
	}
	if b.AccountRemain != 0.05 || b.BatchRemain != 0.70 {
		t.Fatalf("remainder mismatch: %+v", b)
	}
	if b.MaxAttempts != 1 {
		t.Fatalf("0.05 只剩一次取号，得到 %d", b.MaxAttempts)
	}
}

func TestSMSBudgetStopsBeforeLoginWhenExhausted(t *testing.T) {
	m, acc := budgetManager(t)
	if err := m.store.AddSMSDebit(acc.ID, acc.BatchID, 0.35); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		HeroSMSMaxPrice: 0.05,
		Registration:    config.RegistrationSettings{SMSMaxAttempts: 3, SMSAccountBudget: 0.35, SMSBatchBudget: 10},
	}
	if _, err := m.smsBudget(cfg, acc); err == nil {
		t.Fatal("账号预算用完时必须拒绝启动登录")
	}
	// 账号还有额度，但整批预算已经卡住。
	cfg.Registration.SMSAccountBudget = 10
	cfg.Registration.SMSBatchBudget = 0.35
	if _, err := m.smsBudget(cfg, acc); err == nil {
		t.Fatal("整批预算用完时必须拒绝启动登录")
	}
}

// 人工接码不调用 HeroSMS：即使预算为 0 或已耗尽，也不能拦下登录。
func TestManualSMSSkipsBudgetGate(t *testing.T) {
	m, acc := budgetManager(t)
	if err := m.store.AddSMSDebit(acc.ID, acc.BatchID, 5.00); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		HeroSMSMaxPrice: 0.05,
		Registration:    config.RegistrationSettings{ManualSMS: true, SMSMaxAttempts: 3, SMSAccountBudget: 0.35, SMSBatchBudget: 1},
	}
	b, err := m.smsBudget(cfg, acc)
	if err != nil {
		t.Fatalf("manual SMS must not be blocked by an exhausted budget: %v", err)
	}
	if b.AccountRemain != 0 || b.BatchRemain != 0 {
		t.Fatalf("manual SMS must not consume dialling budget: %+v", b)
	}

	// 人工模式同样不要求限价：缺少限价不能拦下登录。
	noPrice := config.Config{
		Registration: config.RegistrationSettings{ManualSMS: true, SMSAccountBudget: 1, SMSBatchBudget: 1},
	}
	if _, err := m.smsBudget(noPrice, acc); err != nil {
		t.Fatalf("manual SMS must not require a per-number price cap: %v", err)
	}
}
