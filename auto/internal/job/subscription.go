package job

import (
	"fmt"
	"github.com/juzijishu/clinepass-registration/internal/cline"
	"github.com/juzijishu/clinepass-registration/internal/clinebypass"
	"github.com/juzijishu/clinepass-registration/internal/config"
	"github.com/juzijishu/clinepass-registration/internal/model"
	"github.com/juzijishu/clinepass-registration/internal/store"
	"strings"
	"time"
)

func (m *Manager) EnqueueSubscription(accountID string) (*model.Job, error) {
	return m.enqueue(accountID, KindSubscription, false)
}

// EnqueueBatchSubscription 把批次里已支付、还没入池的账号一起排进订阅后处理。
func (m *Manager) EnqueueBatchSubscription(batchID string) ([]*model.Job, error) {
	list, err := m.store.ListByBatchMeta(batchID)
	if err != nil {
		return nil, err
	}
	cfg := m.snapshotConfig()
	if settings, err := m.store.GetSettings(); err == nil {
		cfg = store.ApplySettings(cfg, settings)
	}
	if err := cfg.Registration.ValidateTarget(); err != nil {
		return nil, err
	}
	out := []*model.Job{}
	for _, acc := range list {
		if acc.PaidAt == 0 || !m.needsSubscription(acc.ID) {
			continue
		}
		j, err := m.enqueue(acc.ID, KindSubscription, false)
		if err != nil {
			continue
		}
		out = append(out, j)
	}
	return out, nil
}

// needsSubscription 跳过已入池和 Key 创建结果不明的账号，后者必须人工核对。
func (m *Manager) needsSubscription(accountID string) bool {
	cp, err := m.store.GetSubscription(accountID)
	if err != nil {
		return false
	}
	return cp.State != "imported" && cp.State != "key_pending"
}

// chainSubscription 在登录成功且已支付时接着跑取消续订、建 Key 和入池。
func (m *Manager) chainSubscription(job *model.Job) {
	if job.Kind != KindLogin || job.Status != "success" {
		return
	}
	cfg := m.snapshotConfig()
	if settings, err := m.store.GetSettings(); err == nil {
		cfg = store.ApplySettings(cfg, settings)
	}
	if !cfg.Registration.Enabled || cfg.Registration.ValidateTarget() != nil {
		return
	}
	acc, err := m.store.Get(job.AccountID)
	if err != nil || acc.PaidAt == 0 || strings.TrimSpace(acc.CookieHeader) == "" {
		return
	}
	if !m.needsSubscription(acc.ID) {
		return
	}
	j, err := m.enqueue(acc.ID, KindSubscription, false)
	if err != nil {
		m.logf(job, "info", "自动订阅未入队：%s", err)
		return
	}
	m.logf(job, "info", "已自动排队关闭续订并入池（任务 %s）", j.ID)
}

func (m *Manager) runSubscription(job *model.Job, acc model.Account) {
	cfg := m.snapshotConfig()
	settings, err := m.store.GetSettings()
	if err != nil {
		m.fail(job, "读取后处理设置失败")
		return
	}
	cfg = store.ApplySettings(cfg, settings)
	report := func(stage, message string) {
		m.mu.Lock()
		setStageLocked(job, stage, time.Now().Unix())
		m.mu.Unlock()
		m.logf(job, "info", "%s", message)
	}
	if err = m.completeSubscription(acc, cfg, cline.NewDirect(acc.CookieHeader), report); err != nil {
		m.fail(job, err.Error())
		return
	}
	if err = m.store.SetAccountPaid(acc.ID, true); err != nil {
		m.fail(job, "入池已确认，但保存订阅标记失败；可重试核对")
		return
	}
	if err = m.store.UpdateStatus(acc.ID, "ready", ""); err != nil {
		m.fail(job, "入池已确认，但保存账号状态失败；可重试核对")
		return
	}
	m.logf(job, "info", "自动续订已关闭，Key 已保存并确认入池")
	m.setStatus(job, "success", "")
}

func (m *Manager) completeSubscription(acc model.Account, cfg config.Config, c *cline.Client, report func(string, string)) error {
	if err := cfg.Registration.ValidateTarget(); err != nil {
		return err
	}
	if strings.TrimSpace(acc.CookieHeader) == "" {
		return fmt.Errorf("请先保存正常登录取得的授权会话")
	}
	report(StageSubscription, "校验当前会话与账号邮箱")
	userID, err := c.VerifiedIdentity(acc.Email)
	if err != nil {
		return err
	}
	if acc.UserID != "" && acc.UserID != userID {
		return fmt.Errorf("保存的用户身份与当前会话不一致，已停止")
	}
	cp, err := m.store.GetSubscription(acc.ID)
	if err != nil {
		return fmt.Errorf("读取后处理断点失败")
	}
	if cp.State != "" && cp.UserID != userID {
		return fmt.Errorf("后处理断点与当前会话身份不一致")
	}
	if cp.State == "key_pending" {
		return fmt.Errorf("上次 Key 创建结果不明，需在官方后台人工核对；不会再次创建")
	}
	if cp.State != "" && cp.State != "key_saved" && cp.State != "imported" {
		return fmt.Errorf("后处理状态未知，需人工核对")
	}
	report(StageRenewal, "检查有效订阅，关闭自动续订并复查当前权益")
	if err = c.DisableRenewal(); err != nil {
		return fmt.Errorf("未确认有效订阅、续订关闭及权益保留，已停止；请在官方后台核对")
	}
	if cp.State == "" {
		report(StageKey, "记录创建断点并创建 Key")
		if err = m.store.BeginSubscriptionKey(acc.ID, userID); err != nil {
			return fmt.Errorf("保存 Key 创建断点失败，已停止")
		}
		key, createErr := c.CreateAPIKey("subscription-" + acc.ID)
		if createErr != nil {
			return fmt.Errorf("Key 创建结果不明，需在官方后台人工核对；不会自动重复创建")
		}
		if err = m.store.SaveSubscriptionKey(acc.ID, userID, key); err != nil {
			return fmt.Errorf("官方已返回 Key，但本地保存未确认；需人工核对，不会重复创建")
		}
		cp.Key = key
	} else if strings.TrimSpace(cp.Key) == "" || acc.APIKey != cp.Key {
		return fmt.Errorf("已保存的 Key 与账号记录不一致，需人工核对")
	} else {
		report(StageKey, "复用已保存的 Key")
	}
	report(StageImport, "追加到目标号池并回读确认")
	target, err := clinebypass.New(cfg.Registration.TargetURL, cfg.Registration.TargetAdminPassword, cfg.Registration.TargetClientKeyID)
	if err != nil {
		return err
	}
	if err = target.Append(acc.ID, cp.Key); err != nil {
		return err
	}
	if err = m.store.MarkSubscriptionImported(acc.ID); err != nil {
		return fmt.Errorf("目标入池已确认，保存完成标记失败；可重试核对")
	}
	return nil
}
