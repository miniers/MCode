package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/attention"
	"mcode/internal/config"
	"mcode/internal/store"
)

// attentionTestKinds 是收件箱测试用的事件类型：前四类属于总管启用时的白名单，
// 后两类是交给总管处理的告知类事件。
var attentionTestKinds = []string{
	"thread_approval",
	"kanban_needs_approval",
	"kanban_review",
	"kanban_plan_failed",
	"thread_stalled",
	"thread_quota",
}

// newAttentionTestClient 构造挂了真实待处理事件服务的 Client，并按 stewardEnabled 写入配置。
func newAttentionTestClient(t *testing.T, stewardEnabled bool) *Client {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Orchestration.Steward.Enabled = stewardEnabled
	cfg.Orchestration.Steward.EnabledChoice = true
	if err := config.Save(dir, cfg); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "attention.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)
	for i, kind := range attentionTestKinds {
		if _, err := st.InsertAttentionEvent(ctx, store.AttentionEvent{
			Kind:        kind,
			SubjectType: "thread",
			SubjectID:   "subject-" + kind,
			ProjectID:   "/p1",
			ThreadID:    "subject-" + kind,
			Title:       kind,
			EnteredAt:   base.Add(time.Duration(i) * time.Minute),
			DedupeKey:   "dedupe-" + kind,
		}); err != nil {
			t.Fatalf("插入 %s 事件失败: %v", kind, err)
		}
	}

	client := &Client{dataDir: dir}
	client.SetAttention(attention.New(st, attention.Options{}))
	return client
}

// TestListAttentionWithStewardEnabled 覆盖总管启用时收件箱只列必须用户亲手处理的四类，
// 且角标计数与列表条数一致。
func TestListAttentionWithStewardEnabled(t *testing.T) {
	client := newAttentionTestClient(t, true)
	ctx := context.Background()

	items, err := client.ListAttention(ctx, 100)
	if err != nil {
		t.Fatalf("ListAttention 失败: %v", err)
	}
	count, err := client.AttentionUnresolvedCount(ctx)
	if err != nil {
		t.Fatalf("AttentionUnresolvedCount 失败: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("总管启用时应只列出 4 条，实际为 %d", len(items))
	}
	if count != len(items) {
		t.Fatalf("角标计数 %d 与列表条数 %d 不一致", count, len(items))
	}
	allowed := map[string]bool{}
	for _, kind := range attention.OwnerActionKinds {
		allowed[string(kind)] = true
	}
	for _, item := range items {
		if !allowed[item.Kind] {
			t.Fatalf("列出了白名单外的类型: %s", item.Kind)
		}
	}
}

// TestListAttentionWithStewardDisabled 覆盖总管未启用时收件箱行为不变，仍列全部未消解事件。
func TestListAttentionWithStewardDisabled(t *testing.T) {
	client := newAttentionTestClient(t, false)
	ctx := context.Background()

	items, err := client.ListAttention(ctx, 100)
	if err != nil {
		t.Fatalf("ListAttention 失败: %v", err)
	}
	count, err := client.AttentionUnresolvedCount(ctx)
	if err != nil {
		t.Fatalf("AttentionUnresolvedCount 失败: %v", err)
	}
	if len(items) != len(attentionTestKinds) {
		t.Fatalf("总管未启用时应列出全部 %d 条，实际为 %d", len(attentionTestKinds), len(items))
	}
	if count != len(items) {
		t.Fatalf("角标计数 %d 与列表条数 %d 不一致", count, len(items))
	}
	seen := map[string]bool{}
	for _, item := range items {
		seen[item.Kind] = true
	}
	for _, kind := range attentionTestKinds {
		if !seen[kind] {
			t.Fatalf("未列出 %s 类型的事件", kind)
		}
	}
}

// TestAttentionKindsFallsBackWhenConfigUnreadable 覆盖配置读取失败时按未启用处理。
func TestAttentionKindsFallsBackWhenConfigUnreadable(t *testing.T) {
	client := &Client{}
	if kinds := client.attentionKinds(); kinds != nil {
		t.Fatalf("配置读不到时应返回 nil（不过滤），实际为 %v", kinds)
	}
}
