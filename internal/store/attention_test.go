package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// openAttentionStore 打开一个临时 Store；待处理事件不挂外键，无需预置项目与会话。
func openAttentionStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "attention_events.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// newAttentionEvent 构造一条测试用待处理事件。
func newAttentionEvent(dedupeKey string) AttentionEvent {
	entered := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	return AttentionEvent{
		Kind:        "thread_stalled",
		SubjectType: "thread",
		SubjectID:   "thread-1",
		ProjectID:   "/p1",
		ThreadID:    "thread-1",
		Title:       "会话疑似卡死",
		Detail:      "超过 10 分钟没有进展",
		EnteredAt:   entered,
		DedupeKey:   dedupeKey,
	}
}

func TestInsertAttentionEventDefaults(t *testing.T) {
	ctx := context.Background()
	s := openAttentionStore(t)

	created, err := s.InsertAttentionEvent(ctx, newAttentionEvent("k1"))
	if err != nil {
		t.Fatalf("InsertAttentionEvent 失败: %v", err)
	}
	if created.ID == "" {
		t.Fatal("期望自动生成事件 ID")
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatal("期望自动填充创建时间与更新时间")
	}
	if created.ResolvedAt != nil {
		t.Fatal("新建事件不应带消解时间")
	}
	if got, want := created.EnteredAt.UTC(), time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("进入时刻被改写: %v", got)
	}

	byID, err := s.GetAttentionEvent(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetAttentionEvent 失败: %v", err)
	}
	if byID.Title != "会话疑似卡死" || byID.Detail != "超过 10 分钟没有进展" {
		t.Fatalf("按 ID 读回内容不一致: %+v", byID)
	}
	if byID.ThreadID != "thread-1" || byID.ProjectID != "/p1" {
		t.Fatalf("按 ID 读回跳转信息不一致: %+v", byID)
	}

	byKey, err := s.GetAttentionEventByDedupeKey(ctx, "k1")
	if err != nil {
		t.Fatalf("GetAttentionEventByDedupeKey 失败: %v", err)
	}
	if byKey.ID != created.ID {
		t.Fatalf("去重键读回的事件 ID 不一致: %s != %s", byKey.ID, created.ID)
	}
}

func TestAttentionEventNotFound(t *testing.T) {
	ctx := context.Background()
	s := openAttentionStore(t)

	if _, err := s.GetAttentionEvent(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("按 ID 读取不存在的事件应返回 ErrNotFound，实际为: %v", err)
	}
	if _, err := s.GetAttentionEventByDedupeKey(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("按去重键读取不存在的事件应返回 ErrNotFound，实际为: %v", err)
	}
}

func TestInsertAttentionEventUniqueDedupeKey(t *testing.T) {
	ctx := context.Background()
	s := openAttentionStore(t)

	if _, err := s.InsertAttentionEvent(ctx, newAttentionEvent("dup")); err != nil {
		t.Fatalf("首次插入失败: %v", err)
	}
	second := newAttentionEvent("dup")
	second.SubjectID = "thread-2"
	if _, err := s.InsertAttentionEvent(ctx, second); err == nil {
		t.Fatal("相同去重键应触发唯一约束错误")
	}
	count, err := s.CountUnresolvedAttentionEvents(ctx)
	if err != nil {
		t.Fatalf("CountUnresolvedAttentionEvents 失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("唯一约束失败后应仍只有 1 条未消解事件，实际为 %d", count)
	}
}

func TestCountAttentionEventsSince(t *testing.T) {
	ctx := context.Background()
	s := openAttentionStore(t)

	for _, key := range []string{"a1", "a2"} {
		ev := newAttentionEvent(key)
		if _, err := s.InsertAttentionEvent(ctx, ev); err != nil {
			t.Fatalf("插入 %s 失败: %v", key, err)
		}
	}
	other := newAttentionEvent("b1")
	other.Kind = "thread_failed"
	if _, err := s.InsertAttentionEvent(ctx, other); err != nil {
		t.Fatalf("插入 b1 失败: %v", err)
	}

	count, err := s.CountAttentionEventsSince(ctx, "thread_stalled", "thread", "thread-1", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("CountAttentionEventsSince 失败: %v", err)
	}
	if count != 2 {
		t.Fatalf("滚动窗口内应统计到 2 条同类事件，实际为 %d", count)
	}

	count, err = s.CountAttentionEventsSince(ctx, "thread_stalled", "thread", "thread-1", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("CountAttentionEventsSince 失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("窗口之外应统计到 0 条，实际为 %d", count)
	}

	count, err = s.CountAttentionEventsSince(ctx, "thread_stalled", "thread", "thread-2", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("CountAttentionEventsSince 失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("其他对象应统计到 0 条，实际为 %d", count)
	}
}

func TestResolveAttentionEvent(t *testing.T) {
	ctx := context.Background()
	s := openAttentionStore(t)

	created, err := s.InsertAttentionEvent(ctx, newAttentionEvent("r1"))
	if err != nil {
		t.Fatalf("插入失败: %v", err)
	}
	at := time.Date(2026, 5, 2, 9, 30, 0, 0, time.UTC)
	changed, err := s.ResolveAttentionEvent(ctx, created.ID, at, "user")
	if err != nil {
		t.Fatalf("ResolveAttentionEvent 失败: %v", err)
	}
	if !changed {
		t.Fatal("首次消解应返回有行被更新")
	}

	got, err := s.GetAttentionEvent(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetAttentionEvent 失败: %v", err)
	}
	if got.ResolvedAt == nil || !got.ResolvedAt.Equal(at) {
		t.Fatalf("消解时间不符: %v", got.ResolvedAt)
	}
	if got.ResolvedBy != "user" {
		t.Fatalf("消解者不符: %s", got.ResolvedBy)
	}

	changed, err = s.ResolveAttentionEvent(ctx, created.ID, at.Add(time.Hour), "auto")
	if err != nil {
		t.Fatalf("二次消解失败: %v", err)
	}
	if changed {
		t.Fatal("已消解的行不应再被更新")
	}

	changed, err = s.ResolveAttentionEvent(ctx, "missing", at, "user")
	if err != nil {
		t.Fatalf("消解不存在的事件应不报错: %v", err)
	}
	if changed {
		t.Fatal("消解不存在的事件应返回 false")
	}
}

func TestResolveAttentionEventsBySubject(t *testing.T) {
	ctx := context.Background()
	s := openAttentionStore(t)

	stalled := newAttentionEvent("s1")
	failed := newAttentionEvent("s2")
	failed.Kind = "thread_failed"
	approval := newAttentionEvent("s3")
	approval.Kind = "thread_approval"
	otherSubject := newAttentionEvent("s4")
	otherSubject.SubjectID = "thread-2"

	for _, ev := range []AttentionEvent{stalled, failed, approval, otherSubject} {
		if _, err := s.InsertAttentionEvent(ctx, ev); err != nil {
			t.Fatalf("插入 %s 失败: %v", ev.DedupeKey, err)
		}
	}

	at := time.Date(2026, 5, 2, 9, 30, 0, 0, time.UTC)
	resolved, err := s.ResolveAttentionEventsBySubject(ctx, "thread", "thread-1", []string{"thread_stalled", "thread_failed"}, at, "auto")
	if err != nil {
		t.Fatalf("ResolveAttentionEventsBySubject 失败: %v", err)
	}
	if len(resolved) != 2 {
		t.Fatalf("应消解 2 条事件，实际为 %d", len(resolved))
	}
	for _, ev := range resolved {
		if ev.ResolvedAt == nil || !ev.ResolvedAt.Equal(at) || ev.ResolvedBy != "auto" {
			t.Fatalf("返回的消解行字段不完整: %+v", ev)
		}
		if ev.SubjectID != "thread-1" {
			t.Fatalf("不应消解其他对象的事件: %+v", ev)
		}
	}

	count, err := s.CountUnresolvedAttentionEvents(ctx)
	if err != nil {
		t.Fatalf("CountUnresolvedAttentionEvents 失败: %v", err)
	}
	if count != 2 {
		t.Fatalf("应剩 2 条未消解事件，实际为 %d", count)
	}

	rest, err := s.ResolveAttentionEventsBySubject(ctx, "thread", "thread-1", nil, at, "auto")
	if err != nil {
		t.Fatalf("按对象全类型消解失败: %v", err)
	}
	if len(rest) != 1 || rest[0].Kind != "thread_approval" {
		t.Fatalf("kinds 为空时应消解剩余全部类型，实际为 %+v", rest)
	}

	again, err := s.ResolveAttentionEventsBySubject(ctx, "thread", "thread-1", nil, at, "auto")
	if err != nil {
		t.Fatalf("重复消解失败: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("已消解对象不应再返回行，实际为 %d", len(again))
	}
}

func TestListAttentionEvents(t *testing.T) {
	ctx := context.Background()
	s := openAttentionStore(t)

	base := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	for i, kind := range []string{"thread_stalled", "thread_failed", "kanban_failed"} {
		ev := newAttentionEvent(kind)
		ev.Kind = kind
		ev.EnteredAt = base.Add(time.Duration(i) * time.Minute)
		if _, err := s.InsertAttentionEvent(ctx, ev); err != nil {
			t.Fatalf("插入 %s 失败: %v", kind, err)
		}
	}
	otherProject := newAttentionEvent("p2")
	otherProject.ProjectID = "/p2"
	if _, err := s.InsertAttentionEvent(ctx, otherProject); err != nil {
		t.Fatalf("插入 /p2 事件失败: %v", err)
	}

	all, err := s.ListAttentionEvents(ctx, AttentionEventFilter{})
	if err != nil {
		t.Fatalf("ListAttentionEvents 失败: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("应列出 4 条事件，实际为 %d", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].CreatedAt.Before(all[i].CreatedAt) {
			t.Fatal("列表应按创建时间倒序")
		}
	}

	byProject, err := s.ListAttentionEvents(ctx, AttentionEventFilter{ProjectID: "/p2"})
	if err != nil {
		t.Fatalf("按项目过滤失败: %v", err)
	}
	if len(byProject) != 1 || byProject[0].ProjectID != "/p2" {
		t.Fatalf("按项目过滤结果不符: %+v", byProject)
	}

	limited, err := s.ListAttentionEvents(ctx, AttentionEventFilter{Limit: 2})
	if err != nil {
		t.Fatalf("限制条数失败: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("应只返回 2 条，实际为 %d", len(limited))
	}

	if _, err := s.ResolveAttentionEvent(ctx, all[0].ID, base, "user"); err != nil {
		t.Fatalf("消解失败: %v", err)
	}
	unresolved, err := s.ListAttentionEvents(ctx, AttentionEventFilter{UnresolvedOnly: true})
	if err != nil {
		t.Fatalf("按未消解过滤失败: %v", err)
	}
	if len(unresolved) != 3 {
		t.Fatalf("未消解事件应为 3 条，实际为 %d", len(unresolved))
	}
}

// insertAttentionKinds 插入一批指定类型的事件，返回事件列表（按插入顺序）。
func insertAttentionKinds(t *testing.T, s *Store, kinds []string) []AttentionEvent {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	out := make([]AttentionEvent, 0, len(kinds))
	for i, kind := range kinds {
		ev := newAttentionEvent("kind-" + kind)
		ev.Kind = kind
		ev.SubjectID = "subject-" + kind
		ev.EnteredAt = base.Add(time.Duration(i) * time.Minute)
		created, err := s.InsertAttentionEvent(ctx, ev)
		if err != nil {
			t.Fatalf("插入 %s 失败: %v", kind, err)
		}
		out = append(out, created)
	}
	return out
}

func TestListAttentionEventsByKinds(t *testing.T) {
	ctx := context.Background()
	s := openAttentionStore(t)

	insertAttentionKinds(t, s, []string{"thread_approval", "kanban_review", "thread_stalled"})

	filtered, err := s.ListAttentionEvents(ctx, AttentionEventFilter{
		UnresolvedOnly: true,
		Kinds:          []string{"thread_approval", "kanban_review"},
	})
	if err != nil {
		t.Fatalf("按类型过滤失败: %v", err)
	}
	if len(filtered) != 2 {
		t.Fatalf("按类型过滤应返回 2 条，实际为 %d", len(filtered))
	}
	for _, ev := range filtered {
		if ev.Kind != "thread_approval" && ev.Kind != "kanban_review" {
			t.Fatalf("返回了白名单外的类型: %s", ev.Kind)
		}
	}

	all, err := s.ListAttentionEvents(ctx, AttentionEventFilter{UnresolvedOnly: true})
	if err != nil {
		t.Fatalf("未指定类型时列表失败: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("未指定类型时应返回全部 3 条，实际为 %d", len(all))
	}

	none, err := s.ListAttentionEvents(ctx, AttentionEventFilter{Kinds: []string{"bee_failed"}})
	if err != nil {
		t.Fatalf("无匹配类型时列表失败: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("无匹配类型时应返回空列表，实际为 %d", len(none))
	}
}

func TestCountUnresolvedAttentionEventsByKinds(t *testing.T) {
	ctx := context.Background()
	s := openAttentionStore(t)

	inserted := insertAttentionKinds(t, s, []string{"thread_approval", "kanban_needs_approval", "kanban_review", "thread_stalled"})

	kinds := []string{"thread_approval", "kanban_needs_approval", "kanban_review"}
	count, err := s.CountUnresolvedAttentionEventsByKinds(ctx, kinds)
	if err != nil {
		t.Fatalf("按类型计数失败: %v", err)
	}
	if count != 3 {
		t.Fatalf("按类型计数应为 3，实际为 %d", count)
	}

	all, err := s.CountUnresolvedAttentionEventsByKinds(ctx, nil)
	if err != nil {
		t.Fatalf("不传类型计数失败: %v", err)
	}
	total, err := s.CountUnresolvedAttentionEvents(ctx)
	if err != nil {
		t.Fatalf("CountUnresolvedAttentionEvents 失败: %v", err)
	}
	if all != 4 || all != total {
		t.Fatalf("不传类型时应与全量口径一致（4），实际为 %d / %d", all, total)
	}

	if _, err := s.ResolveAttentionEvent(ctx, inserted[0].ID, time.Now().UTC(), "user"); err != nil {
		t.Fatalf("消解失败: %v", err)
	}
	afterResolve, err := s.CountUnresolvedAttentionEventsByKinds(ctx, kinds)
	if err != nil {
		t.Fatalf("消解后按类型计数失败: %v", err)
	}
	if afterResolve != 2 {
		t.Fatalf("消解后按类型计数应为 2，实际为 %d", afterResolve)
	}
}
