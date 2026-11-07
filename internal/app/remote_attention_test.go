package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/attention"
	"mcode/internal/config"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// newRemoteAttentionTestExt 构造挂了真实待处理事件服务的远程扩展，并按 stewardEnabled 写入配置，
// 插入一条 thread_approval（用户需亲手处理）和一条 thread_failed（告知类，总管处理）。
func newRemoteAttentionTestExt(t *testing.T, stewardEnabled bool) remoteAttentionExt {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Orchestration.Steward.Enabled = stewardEnabled
	cfg.Orchestration.Steward.EnabledChoice = true
	if err := config.Save(dir, cfg); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "remote-attention.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)
	for i, kind := range []string{"thread_approval", "thread_failed"} {
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
	return remoteAttentionExt{client: client}
}

// TestRemoteAttentionFollowsStewardKinds 覆盖网页端列表与计数同口径：总管启用时只算需要用户亲手处理的类型。
func TestRemoteAttentionFollowsStewardKinds(t *testing.T) {
	ctx := context.Background()

	enabled := newRemoteAttentionTestExt(t, true)
	res, err := enabled.List(ctx, wire.AttentionExtListParams{Limit: 100})
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].Kind != string(attention.KindThreadApproval) {
		t.Fatalf("总管启用时应只列出 thread_approval 一条，实际为 %+v", res.Items)
	}
	count, err := enabled.UnresolvedCount(ctx)
	if err != nil {
		t.Fatalf("UnresolvedCount 失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("总管启用时计数应为 1，实际为 %d", count)
	}

	disabled := newRemoteAttentionTestExt(t, false)
	res, err = disabled.List(ctx, wire.AttentionExtListParams{Limit: 100})
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("总管未启用时应列出 2 条，实际为 %d", len(res.Items))
	}
	count, err = disabled.UnresolvedCount(ctx)
	if err != nil {
		t.Fatalf("UnresolvedCount 失败: %v", err)
	}
	if count != 2 {
		t.Fatalf("总管未启用时计数应为 2，实际为 %d", count)
	}
}
