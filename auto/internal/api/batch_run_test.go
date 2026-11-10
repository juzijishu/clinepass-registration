package api

import (
	"path/filepath"
	"testing"

	"github.com/juzijishu/clinepass-registration/internal/model"
	"github.com/juzijishu/clinepass-registration/internal/store"
)

func TestBatchRunStateAndTotals(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "run.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	batch, _, err := s.CreateBatch(model.CreateBatchInput{Text: "a@x.com----pw\nb@x.com----pw\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddSMSDebit("a1", batch.ID, 0.06); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSMSDebit("a2", batch.ID, 0.04); err != nil {
		t.Fatal(err)
	}

	sum, err := s.GetBatchSummary(batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sum.SmsSpent != 0.10 {
		t.Fatalf("花费汇总应为 0.10，实际 %v", sum.SmsSpent)
	}
	if sum.InPool != 0 {
		t.Fatalf("尚未入池时应为 0，实际 %d", sum.InPool)
	}

	// 全部账号结束后批次进入 done。
	sum.Ready, sum.Failed = sum.Total, 0
	run := batchRun(sum, nil, nil)
	if run["state"] != "done" || run["done"] != true {
		t.Fatalf("全部账号已终态应判定为 done，实际 %v", run)
	}

	// 有任务在跑时优先显示运行中。
	run = batchRun(sum, []model.Job{{AccountID: "a1", Status: "running"}}, map[string]bool{"a1": true})
	if run["state"] != "running" || run["running"] != 1 {
		t.Fatalf("有运行中任务时应判定 running，实际 %v", run)
	}

	// 其他批次的任务不算进本批。
	run = batchRun(sum, []model.Job{{AccountID: "zzz", Status: "running"}}, map[string]bool{"a1": true})
	if run["state"] != "done" || run["running"] != 0 {
		t.Fatalf("批次外任务不应计入，实际 %v", run)
	}

	// 入池数只统计 state=imported 的账号。
	list, err := s.ListByBatchMeta(batch.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("批次应有 2 个账号: %v %d", err, len(list))
	}
	if err = s.BeginSubscriptionKey(list[0].ID, "user-1"); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveSubscriptionKey(list[0].ID, "user-1", "key-1"); err != nil {
		t.Fatal(err)
	}
	if sum, err = s.GetBatchSummary(batch.ID); err != nil || sum.InPool != 0 {
		t.Fatalf("只保存 Key 未入池不应计数: %v %d", err, sum.InPool)
	}
	if err = s.MarkSubscriptionImported(list[0].ID); err != nil {
		t.Fatal(err)
	}
	if sum, err = s.GetBatchSummary(batch.ID); err != nil || sum.InPool != 1 {
		t.Fatalf("入池后应计 1，实际 %d (%v)", sum.InPool, err)
	}
}
