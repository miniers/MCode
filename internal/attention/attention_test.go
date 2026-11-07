package attention

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mcode/internal/store"
)

// testClock 是可注入的时间来源，供测试推进"当前时间"。
type testClock struct {
	mu sync.Mutex
	at time.Time
}

// Now 返回当前测试时间。
func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

// Advance 把测试时间向前推进 d。
func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// openAttentionService 用临时库创建待处理事件服务；当前时间锚定在真实时间附近，
// 因为滚动窗口比较的是存储层写入的 created_at。
func openAttentionService(t *testing.T, dailyCap int) (*Service, *testClock) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "attention.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	clock := &testClock{at: time.Now()}
	return New(st, Options{Now: clock.Now, DailyCap: dailyCap}), clock
}

// input 构造一条线程卡死事件输入。
func input(subjectID string, enteredAt time.Time) Input {
	return Input{
		Kind:        KindThreadStalled,
		SubjectType: SubjectThread,
		SubjectID:   subjectID,
		ProjectID:   "/p1",
		ThreadID:    subjectID,
		Title:       "会话疑似卡死",
		Detail:      "超过 10 分钟没有进展",
		EnteredAt:   enteredAt,
	}
}

func TestRecordDeduplicates(t *testing.T) {
	ctx := context.Background()
	svc, clock := openAttentionService(t, 0)
	entered := clock.Now()

	first, created, err := svc.Record(ctx, input("t1", entered))
	if err != nil {
		t.Fatalf("Record 失败: %v", err)
	}
	if !created {
		t.Fatal("首次记录应返回 true")
	}
	if first.ID == "" || first.ResolvedAt != nil {
		t.Fatalf("首次记录字段不符: %+v", first)
	}

	second, created, err := svc.Record(ctx, input("t1", entered))
	if err != nil {
		t.Fatalf("重复 Record 失败: %v", err)
	}
	if created {
		t.Fatal("同一进入时刻的重复记录应返回 false")
	}
	if second.ID != first.ID {
		t.Fatalf("重复记录应返回已有事件: %s != %s", second.ID, first.ID)
	}

	count, err := svc.UnresolvedCount(ctx)
	if err != nil {
		t.Fatalf("UnresolvedCount 失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("去重后应只有 1 条未消解事件，实际为 %d", count)
	}
}

func TestRecordDifferentEnteredAtIsNewEvent(t *testing.T) {
	ctx := context.Background()
	svc, clock := openAttentionService(t, 0)
	entered := clock.Now()

	if _, created, err := svc.Record(ctx, input("t1", entered)); err != nil || !created {
		t.Fatalf("首次记录失败: created=%v err=%v", created, err)
	}
	again, created, err := svc.Record(ctx, input("t1", entered.Add(time.Minute)))
	if err != nil {
		t.Fatalf("第二次记录失败: %v", err)
	}
	if !created {
		t.Fatal("不同进入时刻应视为新事件")
	}
	if again.ID == "" {
		t.Fatal("新事件应带 ID")
	}

	events, err := svc.List(ctx, store.AttentionEventFilter{UnresolvedOnly: true})
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("应有 2 条事件，实际为 %d", len(events))
	}
}

func TestRecordDailyCapStopsSeventh(t *testing.T) {
	ctx := context.Background()
	svc, clock := openAttentionService(t, 0)
	entered := clock.Now()

	for i := 0; i < defaultDailyCap; i++ {
		if _, created, err := svc.Record(ctx, input("t1", entered.Add(time.Duration(i)*time.Minute))); err != nil || !created {
			t.Fatalf("第 %d 条记录失败: created=%v err=%v", i+1, created, err)
		}
	}

	capped, created, err := svc.Record(ctx, input("t1", entered.Add(time.Duration(defaultDailyCap)*time.Minute)))
	if err != nil {
		t.Fatalf("超限记录不应报错: %v", err)
	}
	if created {
		t.Fatal("24 小时内第 7 条应被截停")
	}
	if capped.ID != "" {
		t.Fatalf("截停时应返回零值事件，实际为 %+v", capped)
	}

	count, err := svc.UnresolvedCount(ctx)
	if err != nil {
		t.Fatalf("UnresolvedCount 失败: %v", err)
	}
	if count != defaultDailyCap {
		t.Fatalf("截停后应仍为 %d 条，实际为 %d", defaultDailyCap, count)
	}

	// 其他对象不受该对象的截停影响。
	if _, created, err := svc.Record(ctx, input("t2", entered)); err != nil || !created {
		t.Fatalf("其他对象应可正常记录: created=%v err=%v", created, err)
	}
}

func TestRecordResumesAfterWindow(t *testing.T) {
	ctx := context.Background()
	svc, clock := openAttentionService(t, 0)
	entered := clock.Now()

	for i := 0; i < defaultDailyCap; i++ {
		if _, created, err := svc.Record(ctx, input("t1", entered.Add(time.Duration(i)*time.Minute))); err != nil || !created {
			t.Fatalf("第 %d 条记录失败: created=%v err=%v", i+1, created, err)
		}
	}

	clock.Advance(25 * time.Hour)
	resumed, created, err := svc.Record(ctx, input("t1", clock.Now()))
	if err != nil {
		t.Fatalf("窗口过后记录失败: %v", err)
	}
	if !created {
		t.Fatal("滚动窗口过后应恢复写入")
	}
	if resumed.ID == "" {
		t.Fatal("恢复写入的事件应带 ID")
	}

	count, err := svc.UnresolvedCount(ctx)
	if err != nil {
		t.Fatalf("UnresolvedCount 失败: %v", err)
	}
	if count != defaultDailyCap+1 {
		t.Fatalf("应累计 %d 条未消解事件，实际为 %d", defaultDailyCap+1, count)
	}
}

func TestResolveOnlyUnresolvedRows(t *testing.T) {
	ctx := context.Background()
	svc, clock := openAttentionService(t, 0)

	ev, created, err := svc.Record(ctx, input("t1", clock.Now()))
	if err != nil || !created {
		t.Fatalf("记录失败: created=%v err=%v", created, err)
	}
	if err := svc.Resolve(ctx, ev.ID, ResolvedByUser); err != nil {
		t.Fatalf("Resolve 失败: %v", err)
	}
	count, err := svc.UnresolvedCount(ctx)
	if err != nil {
		t.Fatalf("UnresolvedCount 失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("消解后未消解条数应为 0，实际为 %d", count)
	}

	// 再次消解不报错，也不应产生新的变更。
	if err := svc.Resolve(ctx, ev.ID, ResolvedByUser); err != nil {
		t.Fatalf("重复消解应不报错: %v", err)
	}
	if err := svc.Resolve(ctx, "missing", ResolvedByUser); err != nil {
		t.Fatalf("消解不存在的事件应不报错: %v", err)
	}
}

func TestResolveSubjectOnlyUnresolvedRows(t *testing.T) {
	ctx := context.Background()
	svc, clock := openAttentionService(t, 0)
	entered := clock.Now()

	stalled, _, err := svc.Record(ctx, input("t1", entered))
	if err != nil {
		t.Fatalf("记录 thread_stalled 失败: %v", err)
	}
	failed := input("t1", entered)
	failed.Kind = KindThreadFailed
	if _, _, err := svc.Record(ctx, failed); err != nil {
		t.Fatalf("记录 thread_failed 失败: %v", err)
	}
	if _, _, err := svc.Record(ctx, input("t2", entered)); err != nil {
		t.Fatalf("记录 t2 失败: %v", err)
	}

	got, err := svc.ResolveSubject(ctx, SubjectThread, "t1", ResolvedByAuto, KindThreadStalled)
	if err != nil {
		t.Fatalf("ResolveSubject 失败: %v", err)
	}
	if got != 1 {
		t.Fatalf("按类型消解应只影响 1 条，实际为 %d", got)
	}

	rest, err := svc.ResolveSubject(ctx, SubjectThread, "t1", ResolvedByAuto)
	if err != nil {
		t.Fatalf("按对象全类型消解失败: %v", err)
	}
	if rest != 1 {
		t.Fatalf("kinds 为空时应消解剩余 1 条，实际为 %d", rest)
	}

	again, err := svc.ResolveSubject(ctx, SubjectThread, "t1", ResolvedByAuto)
	if err != nil {
		t.Fatalf("重复 ResolveSubject 失败: %v", err)
	}
	if again != 0 {
		t.Fatalf("已消解对象不应再返回条数，实际为 %d", again)
	}

	remaining, err := svc.List(ctx, store.AttentionEventFilter{UnresolvedOnly: true})
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(remaining) != 1 || remaining[0].SubjectID != "t2" {
		t.Fatalf("其他对象的未消解事件应保留，实际为 %+v", remaining)
	}

	// 已消解事件仍可读，且带消解信息。
	resolved, err := svc.List(ctx, store.AttentionEventFilter{})
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	var found bool
	for _, ev := range resolved {
		if ev.ID == stalled.ID {
			found = true
			if ev.ResolvedAt == nil || ev.ResolvedBy != ResolvedByAuto {
				t.Fatalf("消解字段不符: %+v", ev)
			}
		}
	}
	if !found {
		t.Fatal("已消解事件应仍出现在全量列表里")
	}
}

// eventCollector 收集 Watch 回调收到的事件。
type eventCollector struct {
	mu     sync.Mutex
	events []Event
}

// push 追加一条事件。
func (c *eventCollector) push(ev Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
}

// snapshot 返回已收集事件的副本。
func (c *eventCollector) snapshot() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Event, len(c.events))
	copy(out, c.events)
	return out
}

func TestWatchReceivesRecordAndResolve(t *testing.T) {
	ctx := context.Background()
	svc, clock := openAttentionService(t, 0)
	collector := &eventCollector{}
	stop := svc.Watch(collector.push)
	defer stop()

	ev, created, err := svc.Record(ctx, input("t1", clock.Now()))
	if err != nil || !created {
		t.Fatalf("记录失败: created=%v err=%v", created, err)
	}
	got := collector.snapshot()
	if len(got) != 1 {
		t.Fatalf("记录后应收到 1 条通知，实际为 %d", len(got))
	}
	if got[0].ID != ev.ID || got[0].ResolvedAt != nil {
		t.Fatalf("记录通知内容不符: %+v", got[0])
	}

	if err := svc.Resolve(ctx, ev.ID, ResolvedByUser); err != nil {
		t.Fatalf("Resolve 失败: %v", err)
	}
	got = collector.snapshot()
	if len(got) != 2 {
		t.Fatalf("消解后应收到 2 条通知，实际为 %d", len(got))
	}
	if got[1].ID != ev.ID || got[1].ResolvedAt == nil {
		t.Fatalf("消解通知应带消解时间: %+v", got[1])
	}
}

func TestWatchStopIsIdempotent(t *testing.T) {
	ctx := context.Background()
	svc, clock := openAttentionService(t, 0)
	collector := &eventCollector{}
	stop := svc.Watch(collector.push)

	if _, created, err := svc.Record(ctx, input("t1", clock.Now())); err != nil || !created {
		t.Fatalf("记录失败: created=%v err=%v", created, err)
	}
	stop()
	stop()

	if _, created, err := svc.Record(ctx, input("t2", clock.Now())); err != nil || !created {
		t.Fatalf("记录失败: created=%v err=%v", created, err)
	}
	if got := collector.snapshot(); len(got) != 1 {
		t.Fatalf("stop 后不应再收到通知，实际收到 %d 条", len(got))
	}

	// 多个订阅者互不影响。
	other := &eventCollector{}
	stopOther := svc.Watch(other.push)
	if _, created, err := svc.Record(ctx, input("t3", clock.Now())); err != nil || !created {
		t.Fatalf("记录失败: created=%v err=%v", created, err)
	}
	if got := other.snapshot(); len(got) != 1 {
		t.Fatalf("新订阅者应收到通知，实际收到 %d 条", len(got))
	}
	if got := collector.snapshot(); len(got) != 1 {
		t.Fatalf("已停止的订阅者不应收到通知，实际收到 %d 条", len(got))
	}
	stopOther()
}

func TestConcurrentRecord(t *testing.T) {
	ctx := context.Background()
	svc, clock := openAttentionService(t, 1000)
	entered := clock.Now()

	const workers = 20
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			subject := "t" + string(rune('a'+i))
			if _, _, err := svc.Record(ctx, input(subject, entered)); err != nil {
				t.Errorf("并发 Record 失败: %v", err)
			}
		}(i)
	}
	wg.Wait()

	count, err := svc.UnresolvedCount(ctx)
	if err != nil {
		t.Fatalf("UnresolvedCount 失败: %v", err)
	}
	if count != workers {
		t.Fatalf("并发记录后应有 %d 条未消解事件，实际为 %d", workers, count)
	}
}

func TestConcurrentRecordSameSubjectDeduplicates(t *testing.T) {
	ctx := context.Background()
	svc, clock := openAttentionService(t, 1000)
	entered := clock.Now()

	const workers = 10
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			if _, _, err := svc.Record(ctx, input("same", entered)); err != nil {
				t.Errorf("并发 Record 失败: %v", err)
			}
		}()
	}
	wg.Wait()

	count, err := svc.UnresolvedCount(ctx)
	if err != nil {
		t.Fatalf("UnresolvedCount 失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("并发写入同一进入时刻应只落 1 条，实际为 %d", count)
	}
}

// recordKinds 为每种类型记录一条事件，对象 ID 与类型一一对应。
func recordKinds(t *testing.T, svc *Service, clock *testClock, kinds []Kind) {
	t.Helper()
	ctx := context.Background()
	for i, kind := range kinds {
		in := input(fmt.Sprintf("subject-%d", i), clock.Now())
		in.Kind = kind
		if _, _, err := svc.Record(ctx, in); err != nil {
			t.Fatalf("记录 %s 失败: %v", kind, err)
		}
	}
}

// TestListOwnerActionKindsOnly 覆盖总管启用时按白名单只返回四类必须用户亲手处理的事件。
func TestListOwnerActionKindsOnly(t *testing.T) {
	svc, clock := openAttentionService(t, 0)
	ctx := context.Background()

	recordKinds(t, svc, clock, []Kind{
		KindThreadApproval, KindKanbanNeedsApproval, KindKanbanReview, KindKanbanPlanFailed,
		KindThreadStalled, KindThreadQuota, KindBeeFailed,
	})

	whitelisted, err := svc.List(ctx, store.AttentionEventFilter{
		UnresolvedOnly: true,
		Kinds:          OwnerActionKindNames(),
	})
	if err != nil {
		t.Fatalf("按白名单列出失败: %v", err)
	}
	if len(whitelisted) != 4 {
		t.Fatalf("白名单应只返回 4 条，实际为 %d", len(whitelisted))
	}
	allowed := map[string]bool{}
	for _, kind := range OwnerActionKinds {
		allowed[string(kind)] = true
	}
	for _, ev := range whitelisted {
		if !allowed[ev.Kind] {
			t.Fatalf("返回了白名单外的类型: %s", ev.Kind)
		}
	}

	all, err := svc.List(ctx, store.AttentionEventFilter{UnresolvedOnly: true})
	if err != nil {
		t.Fatalf("不传类型列出失败: %v", err)
	}
	if len(all) != 7 {
		t.Fatalf("不传类型时应返回全部 7 条，实际为 %d", len(all))
	}

	count, err := svc.UnresolvedCountByKinds(ctx, OwnerActionKindNames())
	if err != nil {
		t.Fatalf("按白名单计数失败: %v", err)
	}
	if count != len(whitelisted) {
		t.Fatalf("白名单计数 %d 与列表条数 %d 不一致", count, len(whitelisted))
	}

	total, err := svc.UnresolvedCountByKinds(ctx, nil)
	if err != nil {
		t.Fatalf("不传类型计数失败: %v", err)
	}
	if total != len(all) {
		t.Fatalf("不传类型计数 %d 与列表条数 %d 不一致", total, len(all))
	}

	if err := svc.Resolve(ctx, whitelisted[0].ID, ResolvedByUser); err != nil {
		t.Fatalf("消解失败: %v", err)
	}
	afterResolve, err := svc.UnresolvedCountByKinds(ctx, OwnerActionKindNames())
	if err != nil {
		t.Fatalf("消解后计数失败: %v", err)
	}
	if afterResolve != 3 {
		t.Fatalf("消解后白名单计数应为 3，实际为 %d", afterResolve)
	}
}

// TestKindThreadNoProgressValue 锁定无进展事件的类型字符串，协议与配置表都按它匹配。
func TestKindThreadNoProgressValue(t *testing.T) {
	if got := string(KindThreadNoProgress); got != "thread_no_progress" {
		t.Fatalf("KindThreadNoProgress = %q，想要 %q", got, "thread_no_progress")
	}
}
