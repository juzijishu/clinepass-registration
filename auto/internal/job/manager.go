package job

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/juzijishu/clinepass-registration/auto/internal/login"
	"github.com/juzijishu/clinepass-registration/internal/config"
	"github.com/juzijishu/clinepass-registration/internal/model"
	"github.com/juzijishu/clinepass-registration/internal/store"
)

type Manager struct {
	cfg   config.Config
	store *store.Store

	mu           sync.Mutex
	jobs         map[string]*model.Job
	subs         map[string][]chan model.JobEvent
	active       int
	activeCookie int
	queue        []string
	running      map[string]string
	warmCard     func()
	holdPump     bool
}

func New(cfg config.Config, st *store.Store) *Manager {
	if cfg.MaxConcurrent < 1 {
		cfg.MaxConcurrent = 1
	}
	return &Manager{
		cfg:     cfg,
		store:   st,
		jobs:    map[string]*model.Job{},
		subs:    map[string][]chan model.JobEvent{},
		running: map[string]string{},
	}
}

func (m *Manager) List() []model.Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		cp := cloneJob(j)
		out = append(out, cp)
	}
	return out
}

func (m *Manager) Get(id string) (*model.Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return nil, false
	}
	cp := cloneJob(j)
	return &cp, true
}

func cloneJob(j *model.Job) model.Job {
	cp := *j
	cp.Logs = append([]model.JobEvent{}, j.Logs...)
	return cp
}

func (m *Manager) Subscribe(jobID string) (chan model.JobEvent, func()) {
	ch := make(chan model.JobEvent, 64)
	m.mu.Lock()
	m.subs[jobID] = append(m.subs[jobID], ch)
	if j, ok := m.jobs[jobID]; ok {
		start := len(j.Logs) - cap(ch)
		if start < 0 {
			start = 0
		}
		for _, ev := range j.Logs[start:] {
			select {
			case ch <- ev:
			default:
			}
		}
	}
	m.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			list := m.subs[jobID]
			n := list[:0]
			for _, c := range list {
				if c != ch {
					n = append(n, c)
				}
			}
			m.subs[jobID] = n
			close(ch)
		})
	}
}

func (m *Manager) Enqueue(accountID string, autoPay bool) (*model.Job, error) {
	return m.enqueue(accountID, KindLogin, autoPay)
}

func (m *Manager) EnqueueRefresh(accountID string, autoPay bool) (*model.Job, error) {
	acc, err := m.store.Get(accountID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(acc.CookiesJSON) == "" || strings.TrimSpace(acc.WorkspaceID) == "" {
		return nil, fmt.Errorf("没有可用 Cookie")
	}
	return m.enqueue(accountID, KindRefresh, autoPay)
}

func (m *Manager) EnqueueCookie(accountID string) (*model.Job, error) {
	return m.enqueue(accountID, KindCookie, false)
}

func (m *Manager) EnqueueCookieKeep() ([]*model.Job, error) {
	list, err := m.store.ListPoolAccountsRaw()
	if err != nil {
		return nil, err
	}
	out := make([]*model.Job, 0, len(list))
	for _, acc := range list {
		if strings.TrimSpace(acc.CookiesJSON) == "" && strings.TrimSpace(acc.CookieHeader) == "" {
			continue
		}
		j, err := m.EnqueueCookie(acc.ID)
		if err != nil {
			continue
		}
		out = append(out, j)
	}
	return out, nil
}

func (m *Manager) accountBusy(accountID string) bool {
	if _, ok := m.running[accountID]; ok {
		return true
	}
	for _, id := range m.queue {
		if j := m.jobs[id]; j != nil && j.AccountID == accountID && (j.Status == "queued" || j.Status == "running") {
			return true
		}
	}
	return false
}

func (m *Manager) enqueue(accountID, kind string, autoPay bool) (*model.Job, error) {
	acc, err := m.store.Get(accountID)
	if err != nil {
		return nil, err
	}
	if kind == "" {
		kind = KindLogin
	}
	if kind == KindCookie {
		if strings.TrimSpace(acc.CookiesJSON) == "" && strings.TrimSpace(acc.CookieHeader) == "" {
			return nil, fmt.Errorf("没有可用 Cookie，无法续期")
		}
	} else if kind == KindSubscription {
		if strings.TrimSpace(acc.CookieHeader) == "" {
			return nil, fmt.Errorf("请先保存正常登录取得的授权会话")
		}
		cfg := m.snapshotConfig()
		if settings, err := m.store.GetSettings(); err == nil {
			cfg = store.ApplySettings(cfg, settings)
		}
		if err := cfg.Registration.ValidateTarget(); err != nil {
			return nil, err
		}
		autoPay = false
	} else if acc.PaidAt > 0 {
		return nil, fmt.Errorf("已经支付，跳过提取")
	}
	job := &model.Job{
		ID:             newID(),
		AccountID:      acc.ID,
		Email:          acc.Email,
		Kind:           kind,
		AutoPay:        autoPay,
		Status:         "queued",
		Stage:          StageQueued,
		StageLabel:     stageLabel(StageQueued),
		StageStartedAt: time.Now().Unix(),
	}
	m.mu.Lock()
	if m.accountBusy(accountID) {
		m.mu.Unlock()
		return nil, fmt.Errorf("该账号已有浏览器任务在排队或进行中")
	}
	m.jobs[job.ID] = job
	m.queue = append(m.queue, job.ID)
	m.mu.Unlock()
	if kind != KindCookie {
		_ = m.store.UpdateStatus(acc.ID, "queued", "")
	}
	if autoPay {
		m.mu.Lock()
		warm := m.warmCard
		m.mu.Unlock()
		if warm != nil {
			warm()
		}
	}
	m.pump()
	// A worker can already be writing progress by now; never expose its pointer.
	snapshot, _ := m.Get(job.ID)
	return snapshot, nil
}

func (m *Manager) Pump() {
	m.pump()
}

func (m *Manager) HoldPump() {
	m.mu.Lock()
	m.holdPump = true
	m.mu.Unlock()
}

func (m *Manager) snapshotConfig() config.Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

func (m *Manager) SetCloakBinaryPath(path string) {
	m.mu.Lock()
	m.cfg.CloakBinaryPath = strings.TrimSpace(path)
	m.mu.Unlock()
}

func (m *Manager) SetCardWarmer(fn func()) {
	m.mu.Lock()
	m.warmCard = fn
	m.mu.Unlock()
}

func (m *Manager) maxConcurrentLocked() int {
	if st, err := m.store.GetSettings(); err == nil && st.MaxConcurrent >= 1 {
		return st.MaxConcurrent
	}
	if m.cfg.MaxConcurrent < 1 {
		return 1
	}
	return m.cfg.MaxConcurrent
}

func (m *Manager) cookieConcurrentLocked() int {
	if st, err := m.store.GetSettings(); err == nil && st.CookieKeepConcurrency >= 1 {
		return st.CookieKeepConcurrency
	}
	return 4
}

func (m *Manager) pump() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.holdPump {
		return
	}
	loginMax := m.maxConcurrentLocked()
	cookieMax := m.cookieConcurrentLocked()
	for {
		idx := -1
		for i, id := range m.queue {
			job := m.jobs[id]
			if job == nil {
				m.queue = append(m.queue[:i], m.queue[i+1:]...)
				idx = -2
				break
			}
			if job.Kind == KindCookie {
				if m.activeCookie < cookieMax {
					idx = i
					break
				}
				continue
			}
			if m.active < loginMax {
				idx = i
				break
			}
		}
		if idx == -2 {
			continue
		}
		if idx < 0 {
			return
		}
		id := m.queue[idx]
		m.queue = append(m.queue[:idx], m.queue[idx+1:]...)
		job := m.jobs[id]
		if job.Kind == KindCookie {
			m.activeCookie++
		} else {
			m.active++
		}
		m.running[job.AccountID] = job.ID
		go m.run(job)
	}
}

func (m *Manager) run(job *model.Job) {
	defer func() {
		m.mu.Lock()
		if job.Kind == KindCookie {
			m.activeCookie--
		} else {
			m.active--
		}
		delete(m.running, job.AccountID)
		m.mu.Unlock()
		m.chainSubscription(job)
		m.pump()
	}()

	m.setStatus(job, "running", "")
	if job.Kind != KindCookie {
		_ = m.store.UpdateStatus(job.AccountID, "running", "")
	}
	acc, err := m.store.Get(job.AccountID)
	if err != nil {
		m.fail(job, err.Error())
		return
	}
	if job.Kind == KindSubscription {
		m.runSubscription(job, acc)
		return
	}
	startMsg := "开始登录 %s"
	switch job.Kind {
	case KindRefresh:
		startMsg = "开始刷新支付链接 %s"
	case KindCookie:
		startMsg = "开始用浏览器续 Cookie %s"
	}
	m.logf(job, "info", startMsg, acc.Email)

	cfg := m.snapshotConfig()
	if st, err := m.store.GetSettings(); err == nil {
		cfg = store.ApplySettings(cfg, st)
	}
	cfg.AutoPay = job.AutoPay
	if job.AutoPay {
		m.logf(job, "info", "自动支付=开；后台只备一张卡，登录同时开卡")
	}
	if job.Kind == KindLogin && cfg.Registration.ManualSMS {
		m.logf(job, "info", "已开启人工等待：流程会停在号码页，浏览器和网络保持不动，你完成后自动继续")
	}

	var res login.Result
	switch job.Kind {
	case KindCookie:
		res, err = login.RefreshCookies(cfg, acc, func(format string, args ...any) {
			m.logf(job, "info", format, args...)
		})
	case KindRefresh:
		res, err = login.RefreshPayment(cfg, acc, func(format string, args ...any) {
			m.logf(job, "info", format, args...)
		})
	default:
		var budget login.SMSBudget
		if budget, err = m.smsBudget(cfg, acc); err == nil {
			res, err = m.runLoginWithAuthkitRetry(cfg, acc, budget, job)
		}
	}
	if cost := res.SMSCost; cost > 0 {
		if e := m.store.AddSMSDebit(acc.ID, acc.BatchID, cost); e != nil {
			m.logf(job, "error", "保存接码花费失败: %v", e)
		}
	}
	if err != nil {
		if errors.Is(err, login.ErrPhoneTimeout) {
			m.logf(job, "info", "手机号超时，跳过")
		}
		if errors.Is(err, login.ErrAccountBanned) {
			m.logf(job, "info", "账号已被封禁，跳过")
		}
		if errors.Is(err, login.ErrRadarDenied) {
			m.logf(job, "info", "AuthKit Radar 拦截，跳过")
		}
		msg := login.CompactMessage(err.Error())
		m.fail(job, msg)
		if job.Kind != KindCookie {
			acc.Status = "failed"
			acc.LastError = msg
			_ = m.store.SaveLoginResult(acc)
		} else {
			_ = m.store.SetAccountLastError(acc.ID, msg)
		}
		return
	}
	if job.Kind == KindCookie {
		m.logf(job, "info", "正在保存 Cookie")
		if err := m.store.SaveCookies(acc.ID, res.CookiesJSON, res.CookieHeader); err != nil {
			m.fail(job, err.Error())
			return
		}
		m.logf(job, "info", "Cookie 已更新")
		m.setStatus(job, "success", "")
		return
	}
	acc.Status = "ready"
	m.logf(job, "info", "正在保存账号和支付结果")
	acc.LastError = ""
	acc.WorkspaceID = res.WorkspaceID
	acc.APIKey = res.APIKey
	acc.UserID = res.UserID
	acc.CookiesJSON = res.CookiesJSON
	acc.CookieHeader = res.CookieHeader
	acc.PaymentURL = res.PaymentURL
	if res.CookiesJSON != "" {
		acc.CookiesJSON = res.CookiesJSON
		acc.CookieHeader = res.CookieHeader
	}
	if err := m.store.SaveLoginResult(acc); err != nil {
		m.fail(job, err.Error())
		return
	}
	if res.Paid {
		if err := m.store.SetAccountPaid(acc.ID, true); err != nil {
			m.fail(job, err.Error())
			return
		}
		m.logf(job, "info", "自动支付成功")
	} else if job.AutoPay {
		msg := login.CompactMessage(res.PayError)
		if msg == "" {
			msg = "自动支付未完成"
		}
		m.mu.Lock()
		setStageLocked(job, StagePayment, time.Now().Unix())
		m.mu.Unlock()
		m.logf(job, "error", "%s", msg)
		_ = m.store.UpdateStatus(acc.ID, "ready", msg)
		m.setStatus(job, "failed", msg)
		return
	}
	if job.Kind == KindRefresh {
		m.logf(job, "info", "刷新完成")
	} else {
		m.logf(job, "info", "登录完成")
	}
	m.setStatus(job, "success", "")
}

const authkitAccountRetries = 2

func (m *Manager) runLoginWithAuthkitRetry(cfg config.Config, acc model.Account, budget login.SMSBudget, job *model.Job) (login.Result, error) {
	var last error
	var spent float64
	for attempt := 0; attempt <= authkitAccountRetries; attempt++ {
		if attempt > 0 {
			m.logf(job, "info", "AuthKit 页面异常，同一账号第 %d 次重试", attempt)
		}
		res, err := login.Run(cfg, acc, budget.After(spent), func(format string, args ...any) {
			m.logf(job, "info", format, args...)
		})
		spent += res.SMSCost
		res.SMSCost = spent
		if err == nil {
			return res, nil
		}
		last = err
		if errors.Is(err, login.ErrAccountBanned) || errors.Is(err, login.ErrRadarDenied) || !login.IsAuthkitFailure(err) {
			return res, err
		}
	}
	return login.Result{SMSCost: spent}, last
}

// smsBudget 按已发生花费算出本次任务还能用多少接码额度；两个预算都为 0 时不限制。
// 人工接码不调用 HeroSMS，不占用取号预算。
func (m *Manager) smsBudget(cfg config.Config, acc model.Account) (login.SMSBudget, error) {
	reg := cfg.Registration
	b := login.SMSBudget{MaxAttempts: reg.SMSMaxAttempts, WaitSeconds: reg.SMSWaitSeconds}
	if reg.ManualSMS {
		return b, nil
	}
	if reg.SMSAccountBudget <= 0 && reg.SMSBatchBudget <= 0 {
		return b, nil
	}
	accountCents, batchCents, err := m.store.SMSSpent(acc.ID, acc.BatchID)
	if err != nil {
		return b, fmt.Errorf("读取接码花费失败")
	}
	remaining := func(budget float64, cents int64) float64 {
		if budget <= 0 {
			return 0
		}
		// 花费按分记账，剩余额度同样取整到分，避免浮点误差把额度判成不足。
		return math.Round((budget-float64(cents)/100)*100) / 100
	}
	b.AccountRemain = remaining(reg.SMSAccountBudget, accountCents)
	b.BatchRemain = remaining(reg.SMSBatchBudget, batchCents)
	// 只有配置了预算的那一侧参与限制；配置了但已用完就直接停。
	limit := math.Inf(1)
	if reg.SMSAccountBudget > 0 {
		limit = math.Min(limit, b.AccountRemain)
	}
	if reg.SMSBatchBudget > 0 {
		limit = math.Min(limit, b.BatchRemain)
	}
	if limit < 1e-9 {
		return b, fmt.Errorf("接码预算已用完，未开始登录")
	}
	price := cfg.HeroSMSMaxPrice
	if price <= 0 {
		return b, fmt.Errorf("启用接码预算时必须先设置单号限价")
	}
	if allowed := int(limit/price + 1e-9); allowed < b.MaxAttempts {
		b.MaxAttempts = allowed
	}
	if b.MaxAttempts < 1 {
		return b, fmt.Errorf("剩余接码预算不足一次取号，未开始登录")
	}
	return b, nil
}

func (m *Manager) fail(job *model.Job, msg string) {
	msg = login.CompactMessage(msg)
	m.logf(job, "error", "%s", msg)
	m.setStatus(job, "failed", msg)
	if job.Kind != KindCookie {
		_ = m.store.UpdateStatus(job.AccountID, "failed", msg)
	}
}

func (m *Manager) setStatus(job *model.Job, status, errMsg string) {
	m.mu.Lock()
	job.Status = status
	job.Error = login.CompactMessage(errMsg)
	now := time.Now().Unix()
	if status == "running" && job.StartedAt == 0 {
		job.StartedAt = now
		setStageLocked(job, StageBrowser, now)
	}
	if status == "success" || status == "failed" {
		job.EndedAt = now
	}
	if status == "success" {
		setStageLocked(job, StageDone, now)
	}
	m.mu.Unlock()
}

func (m *Manager) logf(job *model.Job, level, format string, args ...any) {
	message, detail, level, stage := presentLog(fmt.Sprintf(format, args...), level)
	if message == "" {
		return
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if stage != "" && job.Kind != KindSubscription && job.Status != "success" && job.Status != "failed" {
		setStageLocked(job, stage, now.Unix())
	}
	ev := model.JobEvent{
		JobID:     job.ID,
		AccountID: job.AccountID,
		Level:     level,
		Stage:     job.Stage,
		Message:   message,
		Detail:    detail,
		Repeat:    1,
		Sequence:  1,
		Time:      now.UnixMilli(),
	}
	merged := false
	if n := len(job.Logs); n > 0 {
		last := &job.Logs[n-1]
		ev.Sequence = last.Sequence + 1
		if last.Level == ev.Level && last.Stage == ev.Stage && last.Message == ev.Message && last.Detail == ev.Detail {
			last.Repeat++
			last.Time = ev.Time
			ev = *last
			merged = true
		}
	}
	if !merged {
		job.Logs = append(job.Logs, ev)
		if len(job.Logs) > maxJobLogs {
			copy(job.Logs, job.Logs[len(job.Logs)-maxJobLogs:])
			job.Logs = job.Logs[:maxJobLogs]
		}
	}
	// Cancellation closes channels under this same lock, preventing send/close races.
	for _, ch := range m.subs[job.ID] {
		select {
		case ch <- ev:
		default:
		}
	}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
