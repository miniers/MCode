package mcodeui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// attentionFakeClient 在内存假 Client 上补齐收件箱接口：记录消解调用，并支持手动触发变更回调。
type attentionFakeClient struct {
	*FakeClient

	inboxMu  sync.Mutex
	items    []AttentionItem
	resolved []string
	watchers map[int]func()
	nextID   int
	// resolveErr 非 nil 时「标记已处理」返回它，用来验证失败回滚。
	resolveErr error
	// listErr 非 nil 时收件箱列表返回它，用来验证首次加载失败的提示与重试。
	listErr error

	// 总管会话：stewardID 是当前会话 ID（空串表示还没有），stewardOpens 记录创建次数。
	stewardID    string
	stewardOpens int
}

var _ AttentionClient = (*attentionFakeClient)(nil)
var _ StewardClient = (*attentionFakeClient)(nil)

func newAttentionFakeClient(items ...AttentionItem) *attentionFakeClient {
	return &attentionFakeClient{
		FakeClient: NewFakeClient(),
		items:      items,
		watchers:   make(map[int]func()),
	}
}

func (f *attentionFakeClient) ListAttention(_ context.Context, limit int) ([]AttentionItem, error) {
	f.inboxMu.Lock()
	defer f.inboxMu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := append([]AttentionItem(nil), f.items...)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *attentionFakeClient) ResolveAttention(_ context.Context, id string) error {
	f.inboxMu.Lock()
	defer f.inboxMu.Unlock()
	f.resolved = append(f.resolved, id)
	if f.resolveErr != nil {
		return f.resolveErr
	}
	kept := f.items[:0]
	for _, item := range f.items {
		if item.ID != id {
			kept = append(kept, item)
		}
	}
	f.items = kept
	return nil
}

func (f *attentionFakeClient) AttentionUnresolvedCount(_ context.Context) (int, error) {
	f.inboxMu.Lock()
	defer f.inboxMu.Unlock()
	return len(f.items), nil
}

func (f *attentionFakeClient) WatchAttention(onChange func()) func() {
	f.inboxMu.Lock()
	f.nextID++
	id := f.nextID
	f.watchers[id] = onChange
	f.inboxMu.Unlock()
	return func() {
		f.inboxMu.Lock()
		delete(f.watchers, id)
		f.inboxMu.Unlock()
	}
}

func (f *attentionFakeClient) addItems(items ...AttentionItem) {
	f.inboxMu.Lock()
	f.items = append(f.items, items...)
	f.inboxMu.Unlock()
}

// failResolve 让后续的「标记已处理」返回错误，用来验证失败回滚。
func (f *attentionFakeClient) failResolve(err error) {
	f.inboxMu.Lock()
	defer f.inboxMu.Unlock()
	f.resolveErr = err
}

// failList 让后续的收件箱列表拉取返回 err；传 nil 恢复正常。
func (f *attentionFakeClient) failList(err error) {
	f.inboxMu.Lock()
	defer f.inboxMu.Unlock()
	f.listErr = err
}

func (f *attentionFakeClient) triggerAttentionChange() {
	f.inboxMu.Lock()
	callbacks := make([]func(), 0, len(f.watchers))
	for _, cb := range f.watchers {
		callbacks = append(callbacks, cb)
	}
	f.inboxMu.Unlock()
	for _, cb := range callbacks {
		cb()
	}
}

func (f *attentionFakeClient) resolvedIDs() []string {
	f.inboxMu.Lock()
	defer f.inboxMu.Unlock()
	return append([]string(nil), f.resolved...)
}

func (f *attentionFakeClient) watchCount() int {
	f.inboxMu.Lock()
	defer f.inboxMu.Unlock()
	return len(f.watchers)
}

func (f *attentionFakeClient) StewardThreadID() string {
	f.inboxMu.Lock()
	defer f.inboxMu.Unlock()
	return f.stewardID
}

func (f *attentionFakeClient) OpenStewardThread(_ context.Context) (string, error) {
	f.inboxMu.Lock()
	defer f.inboxMu.Unlock()
	f.stewardOpens++
	if f.stewardID == "" {
		f.stewardID = "t1"
	}
	return f.stewardID, nil
}

// stewardOpenCount 在锁内读取总管会话的创建次数。
func (f *attentionFakeClient) stewardOpenCount() int {
	f.inboxMu.Lock()
	defer f.inboxMu.Unlock()
	return f.stewardOpens
}

// attentionItem 构造一条测试用事件，详情带 ID 便于断言，创建时间按 age 往前推。
func attentionItem(id, kind, subjectType, threadID, title string, age time.Duration) AttentionItem {
	return AttentionItem{
		ID:          id,
		Kind:        kind,
		SubjectType: subjectType,
		ThreadID:    threadID,
		Title:       title,
		Detail:      "详情 " + id,
		CreatedAt:   time.Now().Add(-age),
	}
}

// textIndex 返回文本在上一帧中的位置，找不到时返回 -1。
func textIndex(tt *ui.Tester, want string) int {
	for i, text := range tt.Texts() {
		if text == want {
			return i
		}
	}
	return -1
}

// hasExactText 判断是否存在去掉首尾空白后恰好等于 want 的文本。
// HasText 是子串匹配，会误命中说明文字（如权限说明里的「其他操作询问」），分组标题断言要用它。
func hasExactText(tt *ui.Tester, want string) bool {
	for _, text := range tt.Texts() {
		if strings.TrimSpace(text) == want {
			return true
		}
	}
	return false
}

// selectedThreadForTest 在锁内读取当前选中会话，避免与后台 goroutine 竞争。
func selectedThreadForTest(s *chatState) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selectedThreadID
}

// 场景 1：客户端没实现收件箱接口时不渲染入口行（侧栏其余内容照常）。
func TestAttentionInboxHiddenWithoutAttentionClient(t *testing.T) {
	fake := NewFakeClient()
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	if tt.HasText("待处理") {
		t.Fatalf("未实现 AttentionClient 时不应渲染入口，当前文本: %q", tt.Texts())
	}
}

// 场景 2：入口行显示未处理数量胶囊。
func TestAttentionInboxShowsUnresolvedCount(t *testing.T) {
	fake := newAttentionFakeClient(
		attentionItem("a1", "thread_failed", "thread", "t1", "会话1出错", time.Minute),
		attentionItem("a2", "thread_failed", "thread", "t1", "会话1再次出错", 2*time.Minute),
		attentionItem("a3", "kanban_review", "kanban_task", "", "看板任务待验收", 3*time.Minute),
	)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("待处理 3 条") })
	// 订阅只建立一次：多帧之后仍然只有一个 watcher。
	waitFor(t, tt, func() bool { return fake.watchCount() == 1 })
	for i := 0; i < 5; i++ {
		tt.Frame()
	}
	if got := fake.watchCount(); got != 1 {
		t.Fatalf("订阅应只建立一次，实际 watcher 数 %d", got)
	}
}

// 场景 3：浮层按类型分组，组标题与组内条数正确，顺序与登记顺序一致。
func TestAttentionInboxPopoverGroupsAndOrder(t *testing.T) {
	fake := newAttentionFakeClient(
		attentionItem("a1", "thread_failed", "thread", "t1", "会话1出错", time.Minute),
		attentionItem("a2", "kanban_review", "kanban_task", "", "看板任务待验收", 2*time.Minute),
		attentionItem("a3", "thread_failed", "thread", "t1", "会话1再次出错", 3*time.Minute),
		attentionItem("a4", "future_kind", "thread", "t1", "未知类型事件", 4*time.Minute),
	)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("待处理 4 条") })
	if err := tt.Click("待处理"); err != nil {
		t.Fatalf("点击入口失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("会话出错") })

	for _, want := range []string{"会话出错", "看板待验收", "其他", "未知类型事件"} {
		if !tt.HasText(want) {
			t.Fatalf("浮层缺少文本 %q，当前文本: %q", want, tt.Texts())
		}
	}
	if tt.HasText("仅显示最近 100 条") {
		t.Fatalf("只有 4 条时不应提示截断，当前文本: %q", tt.Texts())
	}

	// 分组顺序：会话出错 → 看板待验收 → 其他；同组内保持列表顺序。
	failed, review, other := textIndex(tt, "会话出错"), textIndex(tt, "看板待验收"), textIndex(tt, "其他")
	if failed < 0 || review < 0 || other < 0 || !(failed < review && review < other) {
		t.Fatalf("分组顺序错误: 会话出错=%d 看板待验收=%d 其他=%d", failed, review, other)
	}
	first, second := textIndex(tt, "会话1出错"), textIndex(tt, "会话1再次出错")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("同组内应保持列表顺序: 会话1出错=%d 会话1再次出错=%d", first, second)
	}
	// 组标题右侧的条数：会话出错组有 2 条。
	if textIndex(tt, "2") < 0 {
		t.Fatalf("缺少分组条数，当前文本: %q", tt.Texts())
	}
}

// 场景 4：点「标记已处理」调用 ResolveAttention 并刷新列表与数量。
func TestAttentionInboxResolveRefreshesList(t *testing.T) {
	fake := newAttentionFakeClient(
		attentionItem("a1", "thread_failed", "thread", "t1", "会话1出错", time.Minute),
		attentionItem("a2", "thread_failed", "thread", "t1", "会话1再次出错", 2*time.Minute),
	)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("待处理 2 条") })
	if err := tt.Click("待处理"); err != nil {
		t.Fatalf("点击入口失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("会话1出错") })

	if err := tt.Click("标记已处理"); err != nil {
		t.Fatalf("点击标记已处理失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		ids := fake.resolvedIDs()
		return len(ids) == 1 && ids[0] == "a1"
	})
	waitFor(t, tt, func() bool { return tt.HasText("待处理 1 条") })
	waitFor(t, tt, func() bool { return !tt.HasText("会话1出错") })
	if !tt.HasText("会话1再次出错") {
		t.Fatalf("未消解的事项应保留，当前文本: %q", tt.Texts())
	}
}

// 场景 5：没有未处理事项时不显示数量胶囊，浮层显示空态文案。
func TestAttentionInboxEmptyState(t *testing.T) {
	fake := newAttentionFakeClient()
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	if tt.HasText("待处理 0 条") {
		t.Fatalf("数量为 0 时不应显示胶囊，当前文本: %q", tt.Texts())
	}
	if err := tt.Click("待处理"); err != nil {
		t.Fatalf("点击入口失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText(attentionInboxEmptyText) })
	if !tt.HasText("各会话在后台推进。需要你批准或回答的事会出现在这里。") {
		t.Fatalf("空态文案应为「各会话在后台推进。需要你批准或回答的事会出现在这里。」，当前文本: %q", tt.Texts())
	}

	// 快捷键「打开待处理收件箱」与点击入口行共用同一份 open 状态：再按一次收起。
	state.toggleAttentionInbox()
	waitFor(t, tt, func() bool { return !tt.HasText(attentionInboxEmptyText) })
	// 再按一次重新打开。
	state.toggleAttentionInbox()
	waitFor(t, tt, func() bool { return tt.HasText(attentionInboxEmptyText) })
}

// 场景 6：点击条目本身跳到对应会话并关闭浮层，但不自动标记已处理。
func TestAttentionInboxClickItemRevealsThread(t *testing.T) {
	fake := newAttentionFakeClient(
		attentionItem("a1", "thread_stalled", "thread", "t1", "会话1卡住", time.Minute),
	)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("待处理 1 条") })
	if err := tt.Click("待处理"); err != nil {
		t.Fatalf("点击入口失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("会话1卡住") })

	if err := tt.Click("会话1卡住"); err != nil {
		t.Fatalf("点击条目失败: %v", err)
	}
	waitFor(t, tt, func() bool { return selectedThreadForTest(state) == "t1" })
	waitFor(t, tt, func() bool { return !tt.HasText("标记已处理") })
	if ids := fake.resolvedIDs(); len(ids) != 0 {
		t.Fatalf("跳转不应自动标记已处理，实际消解: %v", ids)
	}
}

// 场景 6b：目标规划失败事件按「规划失败」分组，点击条目打开任务窗口并收起浮层。
func TestAttentionInboxKanbanPlanFailedOpensTasks(t *testing.T) {
	fake := newAttentionFakeClient(
		attentionItem("a1", "kanban_plan_failed", "kanban_goal", "", "收件箱重构目标", time.Minute),
	)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	opened := make(chan string, 1)
	state.openTasksWindowFn = func(view string) { opened <- view }
	t.Cleanup(func() { state.openTasksWindowFn = nil })

	waitFor(t, tt, func() bool { return tt.HasText("待处理 1 条") })
	if err := tt.Click("待处理"); err != nil {
		t.Fatalf("点击入口失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("收件箱重构目标") })
	if !tt.HasText("规划失败") {
		t.Fatalf("分组标题应为「规划失败」，当前文本: %q", tt.Texts())
	}

	if err := tt.Click("收件箱重构目标"); err != nil {
		t.Fatalf("点击条目失败: %v", err)
	}
	select {
	case got := <-opened:
		if got != "tasks" {
			t.Fatalf("打开的任务窗口视图 = %q, 期望 tasks", got)
		}
	default:
		t.Fatalf("点击规划失败事件应打开任务窗口")
	}
	waitFor(t, tt, func() bool { return !tt.HasText("标记已处理") })
	if ids := fake.resolvedIDs(); len(ids) != 0 {
		t.Fatalf("跳转不应自动标记已处理，实际消解: %v", ids)
	}
}

// 场景 7：订阅回调触发节流刷新后数量更新。
func TestAttentionInboxWatchRefreshesCount(t *testing.T) {
	fake := newAttentionFakeClient(
		attentionItem("a1", "automation_failed", "automation_run", "", "自动化失败", time.Minute),
	)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("待处理 1 条") })
	fake.addItems(attentionItem("a2", "bee_failed", "bee", "t1", "子代理失败", 0))
	fake.triggerAttentionChange()
	waitFor(t, tt, func() bool { return tt.HasText("待处理 2 条") })
}

// 子代理失败分组在收件箱里以「子代理失败」为标题，标题与条目都不出现内部术语。
func TestAttentionInboxBeeFailedGroupTitle(t *testing.T) {
	fake := newAttentionFakeClient(
		attentionItem("a1", "bee_failed", "bee", "t1", "子代理执行出错", time.Minute),
	)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("待处理 1 条") })
	if err := tt.Click("待处理"); err != nil {
		t.Fatalf("点击入口失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("子代理执行出错") })
	if !tt.HasText("子代理失败") {
		t.Fatalf("分组标题应为「子代理失败」，当前文本: %q", tt.Texts())
	}
	if tt.HasText("bee 失败") {
		t.Fatalf("分组标题不应显示内部术语，当前文本: %q", tt.Texts())
	}
}

// 总管会话已存在时，收件箱浮层标题行给出跳转入口：点击跳到该会话并收起浮层，
// 不发起新的创建。
func TestAttentionInboxStewardEntry(t *testing.T) {
	fake := newAttentionFakeClient(
		attentionItem("a1", "thread_failed", "thread", "t1", "会话1出错", time.Minute),
	)
	fake.stewardID = "t1"
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	// 总管会话存在时侧栏只有合并行，浮层要靠行内右侧的未处理数徽标打开。
	waitFor(t, tt, func() bool { return tt.HasText("待处理 1 条") })
	if err := tt.Click("待处理 1 条"); err != nil {
		t.Fatalf("点击默认段徽标失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("打开与总管的对话") })

	if err := tt.Click("打开与总管的对话"); err != nil {
		t.Fatalf("点击总管入口失败: %v", err)
	}
	waitFor(t, tt, func() bool { return selectedThreadForTest(state) == "t1" })
	// 浮层要跟着收起，事件列表不再显示。
	waitFor(t, tt, func() bool { return !tt.HasText("标记已处理") })
	if fake.stewardOpenCount() != 0 {
		t.Fatalf("会话已存在时不应再次创建，实际创建 %d 次", fake.stewardOpenCount())
	}
}

// 场景 8：标记已处理失败时条目放回原位、计数加回，浮层顶部给一句提示。
func TestAttentionInboxResolveFailureRollsBack(t *testing.T) {
	fake := newAttentionFakeClient(
		attentionItem("a1", "thread_failed", "thread", "t1", "会话1出错", time.Minute),
		attentionItem("a2", "thread_failed", "thread", "t1", "会话1再次出错", 2*time.Minute),
	)
	fake.failResolve(errors.New("rpc error: host gone"))
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("待处理 2 条") })
	if err := tt.Click("待处理"); err != nil {
		t.Fatalf("点击入口失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("会话1出错") })

	if err := tt.Click("标记已处理"); err != nil {
		t.Fatalf("点击标记已处理失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("标记没成功，请重试") })
	waitFor(t, tt, func() bool { return tt.HasText("待处理 2 条") })

	st := getAttentionInboxState(state)
	st.mu.Lock()
	count, errText := st.count, st.err
	ids := make([]string, 0, len(st.items))
	for _, item := range st.items {
		ids = append(ids, item.ID)
	}
	st.mu.Unlock()
	if errText == "" {
		t.Fatal("标记失败后应记下提示文案")
	}
	if count != 2 {
		t.Fatalf("失败后计数应加回 2，实际 %d", count)
	}
	if len(ids) != 2 || ids[0] != "a1" || ids[1] != "a2" {
		t.Fatalf("失败后条目应回到原位，实际 %v", ids)
	}
	i1, i2 := textIndex(tt, "会话1出错"), textIndex(tt, "会话1再次出错")
	if i1 < 0 || i2 < 0 || i1 > i2 {
		t.Fatalf("列表顺序应保持原样，实际 a1=%d a2=%d", i1, i2)
	}
}

// 无进展事件归入自己的「任务无进展」分组，而不是「其他」。
func TestAttentionInboxGroupsThreadNoProgress(t *testing.T) {
	fake := newAttentionFakeClient(
		attentionItem("a1", "thread_no_progress", "thread", "t1", "会话1长时间无进展", time.Minute),
	)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("待处理 1 条") })
	if err := tt.Click("待处理"); err != nil {
		t.Fatalf("点击入口失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("会话1长时间无进展") })
	if !hasExactText(tt, "任务无进展") {
		t.Fatalf("分组标题应为「任务无进展」，当前文本: %q", tt.Texts())
	}
	if hasExactText(tt, "其他") {
		t.Fatalf("已登记类型不应归入「其他」，当前文本: %q", tt.Texts())
	}
}

// TestAttentionInboxShowsLoadErrorAndRetry 验证首次加载失败后浮层不再停在「加载中…」，
// 而是显示错误行与唯一的「重试加载收件箱」按钮；点重试后能立刻恢复列表。
func TestAttentionInboxShowsLoadErrorAndRetry(t *testing.T) {
	fake := newAttentionFakeClient()
	fake.failList(errors.New("收件箱坏了"))
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("待处理") })
	if err := tt.Click("待处理"); err != nil {
		t.Fatalf("点击入口失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("加载收件箱失败：收件箱坏了") })
	if tt.HasText("加载中…") {
		t.Fatalf("加载失败后不应显示加载中，当前文本: %q", tt.Texts())
	}

	// 重试按钮按 a11y Label 唯一定位；恢复数据后点它应立即拉到列表，不被节流窗口拦住。
	fake.failList(nil)
	fake.addItems(attentionItem("a1", "thread_failed", "thread", "t1", "会话1出错", time.Minute))
	if err := tt.Click("重试加载收件箱"); err != nil {
		t.Fatalf("找不到重试按钮: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("会话1出错") })
	if tt.HasText("加载收件箱失败：收件箱坏了") {
		t.Fatalf("重试成功后应清掉错误行，当前文本: %q", tt.Texts())
	}
}
