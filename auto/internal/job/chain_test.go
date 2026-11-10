package job

import (
	"path/filepath"
	"testing"

	"github.com/juzijishu/clinepass-registration/internal/config"
	"github.com/juzijishu/clinepass-registration/internal/model"
	"github.com/juzijishu/clinepass-registration/internal/store"
)

func chainedManager(t *testing.T, enabled bool) (*Manager, model.Account) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "chain.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	acc, err := s.CreatePaidAccount(model.CreatePaidAccountInput{Email: "chain@example.com", CookieHeader: "session=synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Registration: config.RegistrationSettings{
		Enabled: enabled, SMSMaxAttempts: 3, SMSAccountBudget: 1, SMSBatchBudget: 5,
		TargetURL: "http://127.0.0.1:1", TargetAdminPassword: "synthetic", TargetClientKeyID: "legacy",
	}}
	m := New(cfg, s)
	m.HoldPump()
	settings, _ := s.GetSettings()
	settings.Registration = cfg.Registration
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	return m, acc
}

func subscriptionJobs(m *Manager) []model.Job {
	out := []model.Job{}
	for _, j := range m.List() {
		if j.Kind == KindSubscription {
			out = append(out, j)
		}
	}
	return out
}

func TestLoginSuccessChainsSubscriptionWhenEnabled(t *testing.T) {
	m, acc := chainedManager(t, true)
	m.chainSubscription(&model.Job{AccountID: acc.ID, Kind: KindLogin, Status: "success"})
	if jobs := subscriptionJobs(m); len(jobs) != 1 {
		t.Fatalf("登录成功且开启自动串联后应排入 1 个订阅任务，实际 %d", len(jobs))
	}
}

func TestLoginDoesNotChainWhenDisabledOrFailedOrUnpaid(t *testing.T) {
	m, acc := chainedManager(t, false)
	m.chainSubscription(&model.Job{AccountID: acc.ID, Kind: KindLogin, Status: "success"})
	if jobs := subscriptionJobs(m); len(jobs) != 0 {
		t.Fatalf("未开启自动串联时不应入队，实际 %d", len(jobs))
	}

	m, acc = chainedManager(t, true)
	m.chainSubscription(&model.Job{AccountID: acc.ID, Kind: KindLogin, Status: "failed"})
	if jobs := subscriptionJobs(m); len(jobs) != 0 {
		t.Fatalf("登录失败不应入队，实际 %d", len(jobs))
	}

	m, acc = chainedManager(t, true)
	if err := m.store.SetAccountPaid(acc.ID, false); err != nil {
		t.Fatal(err)
	}
	m.chainSubscription(&model.Job{AccountID: acc.ID, Kind: KindLogin, Status: "success"})
	if jobs := subscriptionJobs(m); len(jobs) != 0 {
		t.Fatalf("未支付不应入队，实际 %d", len(jobs))
	}
}

func TestChainSkipsImportedAndPendingKey(t *testing.T) {
	m, acc := chainedManager(t, true)
	if err := m.store.BeginSubscriptionKey(acc.ID, "user-1"); err != nil {
		t.Fatal(err)
	}
	m.chainSubscription(&model.Job{AccountID: acc.ID, Kind: KindLogin, Status: "success"})
	if jobs := subscriptionJobs(m); len(jobs) != 0 {
		t.Fatalf("Key 创建结果不明时不能自动重试，实际 %d", len(jobs))
	}
}
