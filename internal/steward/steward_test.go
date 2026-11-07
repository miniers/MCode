package steward

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mcode/internal/attention"
	"mcode/internal/config"
	"mcode/internal/kanban"
	"mcode/internal/mcp"
	"mcode/internal/remote"
	"mcode/internal/store"
)

// 依赖接口的编译期断言：生产类型必须直接满足包内接口。
var (
	_ Events   = (*attention.Service)(nil)
	_ Sessions = (*remote.MemoryBackend)(nil)
	_ Settings = (*store.Store)(nil)
)

// fakeClock 是可控时钟：Now 返回虚拟时间，After 注册等待者，Advance 推进并唤醒到点的等待者。
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWaiter
}

// fakeWaiter 记录一个等待到点唤醒的定时器。
type fakeWaiter struct {
	deadline time.Time
	ch       chan time.Time
}

// newFakeClock 返回一个从固定虚拟时间起步的时钟。
func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2025, 3, 4, 9, 0, 0, 0, time.Local)}
}

// Now 返回当前虚拟时间。
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// After 注册一个等待者，虚拟时间到达 deadline 时收到值。
func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, fakeWaiter{deadline: c.now.Add(d), ch: ch})
	return ch
}

// Advance 推进虚拟时间，并唤醒所有到点的等待者。
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	kept := c.waiters[:0]
	for _, w := range c.waiters {
		if w.deadline.After(c.now) {
			kept = append(kept, w)
			continue
		}
		select {
		case w.ch <- c.now:
		default:
		}
	}
	c.waiters = kept
}

// waiterCount 返回已挂上但未到点的等待者数量，用于判断聚合循环是否已挂上定时器。
func (c *fakeClock) waiterCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}

// resolvedCall 记录一次消解调用。
type resolvedCall struct {
	ID string
	By string
}

// fakeEvents 是 Events 的测试实现：可注入事件、记录消解与订阅取消、可覆盖未消解计数。
type fakeEvents struct {
	mu            sync.Mutex
	events        []attention.Event
	watchers      []func(attention.Event)
	resolved      []resolvedCall
	stopCalls     int
	listErr       error
	countOverride *int
}

// newFakeEvents 返回空的事件仓储。
func newFakeEvents() *fakeEvents { return &fakeEvents{} }

// Watch 登记订阅，返回的 stop 会把该订阅置空并计数。
func (f *fakeEvents) Watch(fn func(attention.Event)) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := len(f.watchers)
	f.watchers = append(f.watchers, fn)
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if idx < len(f.watchers) {
			f.watchers[idx] = nil
		}
		f.stopCalls++
	}
}

// List 按过滤条件返回事件。
func (f *fakeEvents) List(_ context.Context, filter store.AttentionEventFilter) ([]attention.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]attention.Event, 0, len(f.events))
	for _, ev := range f.events {
		if filter.UnresolvedOnly && ev.ResolvedAt != nil {
			continue
		}
		if filter.ProjectID != "" && ev.ProjectID != filter.ProjectID {
			continue
		}
		out = append(out, ev)
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	return out, nil
}

// UnresolvedCount 返回未消解条数；设置了覆盖值时直接返回覆盖值。
func (f *fakeEvents) UnresolvedCount(_ context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return 0, f.listErr
	}
	if f.countOverride != nil {
		return *f.countOverride, nil
	}
	n := 0
	for _, ev := range f.events {
		if ev.ResolvedAt == nil {
			n++
		}
	}
	return n, nil
}

// UnresolvedCountByKinds 返回指定类型的未消解条数；kinds 为空表示不按类型过滤，与 Service 一致。
func (f *fakeEvents) UnresolvedCountByKinds(_ context.Context, kinds []string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return 0, f.listErr
	}
	if f.countOverride != nil {
		return *f.countOverride, nil
	}
	allowed := make(map[string]bool, len(kinds))
	for _, kind := range kinds {
		allowed[kind] = true
	}
	n := 0
	for _, ev := range f.events {
		if ev.ResolvedAt != nil {
			continue
		}
		if len(allowed) > 0 && !allowed[ev.Kind] {
			continue
		}
		n++
	}
	return n, nil
}

// Resolve 记录消解调用并标记对应事件。
func (f *fakeEvents) Resolve(_ context.Context, id, by string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolved = append(f.resolved, resolvedCall{ID: id, By: by})
	for i := range f.events {
		if f.events[i].ID == id && f.events[i].ResolvedAt == nil {
			at := time.Now()
			f.events[i].ResolvedAt = &at
		}
	}
	return nil
}

// emit 添加一条事件并通知所有订阅者。
func (f *fakeEvents) emit(ev attention.Event) {
	f.mu.Lock()
	f.events = append(f.events, ev)
	watchers := make([]func(attention.Event), 0, len(f.watchers))
	for _, fn := range f.watchers {
		if fn != nil {
			watchers = append(watchers, fn)
		}
	}
	f.mu.Unlock()
	for _, fn := range watchers {
		fn(ev)
	}
}

// setCount 覆盖 UnresolvedCount 的返回值。
func (f *fakeEvents) setCount(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := n
	f.countOverride = &count
}

// seedUnresolved 直接放入 n 条未消解的会话失败事件（不触发订阅回调），供巡检统计使用。
func (f *fakeEvents) seedUnresolved(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := 1; i <= n; i++ {
		f.events = append(f.events, ev(fmt.Sprintf("seed-%02d", i), fmt.Sprintf("seed-thread-%02d", i), "proj-1"))
	}
}

// resolveCalls 返回全部消解调用。
func (f *fakeEvents) resolveCalls() []resolvedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]resolvedCall(nil), f.resolved...)
}

// stopCount 返回订阅取消的次数。
func (f *fakeEvents) stopCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopCalls
}

// sentMessage 记录一次投递。
type sentMessage struct {
	ThreadID string
	Text     string
}

// appendedMessage 记录一次直接写入的助手消息。
type appendedMessage struct {
	ThreadID string
	Text     string
}

// fakeSessions 是 Sessions 的测试实现：记录创建请求与投递内容，可模拟会话丢失与错误。
type fakeSessions struct {
	mu       sync.Mutex
	starts   []kanban.SessionRequest
	sent     []sentMessage
	wakes    []sentMessage
	appended []appendedMessage
	states   map[string]kanban.SessionState
	titles   map[string]string
	nextID   int
	startErr error
	stateErr error
	sendErr  error
	// wakeFails 让接下来几次 SendStewardWake 失败（每次递减），用来模拟投递暂时不可用。
	wakeFails int
	// wakeAttempts 记录 SendStewardWake 被调用的次数（含失败的那些）。
	wakeAttempts int
	// hostEventCount 记录 SendHostEvent 的调用次数：唤醒已改走隐藏入口，不应再走这条。
	hostEventCount int
	// startGate 非 nil 时，建会话先阻塞到它被关闭或 ctx 被取消，用来模拟耗时的首轮启动。
	startGate chan struct{}
	// startEntered 在建会话开始阻塞时关闭，供测试确认后台准备已进到建会话这一步。
	startEntered chan struct{}
	// startExited 在建会话因 ctx 取消（或被放行）而结束时关闭。
	startExited chan struct{}
	// startCtxErr 记录建会话结束时 ctx 的错误。
	startCtxErr error
}

// newFakeSessions 返回空的会话仓储。
func newFakeSessions() *fakeSessions {
	return &fakeSessions{states: make(map[string]kanban.SessionState), titles: make(map[string]string)}
}

// StartSession 记录创建请求并返回递增的会话 ID；startGate 非 nil 时先等它放行或 ctx 被取消。
func (f *fakeSessions) StartSession(ctx context.Context, req kanban.SessionRequest) (string, error) {
	f.mu.Lock()
	gate, entered, exited := f.startGate, f.startEntered, f.startExited
	f.mu.Unlock()
	if gate != nil {
		if entered != nil {
			close(entered)
		}
		var ctxErr error
		select {
		case <-gate:
		case <-ctx.Done():
			ctxErr = ctx.Err()
		}
		f.mu.Lock()
		f.startCtxErr = ctxErr
		f.mu.Unlock()
		if exited != nil {
			close(exited)
		}
		if ctxErr != nil {
			return "", ctxErr
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return "", f.startErr
	}
	f.starts = append(f.starts, req)
	f.nextID++
	id := fmt.Sprintf("steward-thread-%d", f.nextID)
	f.states[id] = kanban.SessionState{Running: true}
	f.titles[id] = req.Title
	return id, nil
}

// SessionState 返回会话状态，未知会话返回 store.ErrNotFound。
func (f *fakeSessions) SessionState(_ context.Context, threadID string) (kanban.SessionState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stateErr != nil {
		return kanban.SessionState{}, f.stateErr
	}
	state, ok := f.states[threadID]
	if !ok {
		return kanban.SessionState{}, store.ErrNotFound
	}
	return state, nil
}

// SendSwarmReport 记录投递内容。
func (f *fakeSessions) SendSwarmReport(_ context.Context, threadID, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, sentMessage{ThreadID: threadID, Text: text})
	return nil
}

// SendHostEvent 记录宿主事件投递内容，并计数；唤醒已改走 SendStewardWake，这里应为 0 次。
func (f *fakeSessions) SendHostEvent(_ context.Context, threadID, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
	f.hostEventCount++
	f.sent = append(f.sent, sentMessage{ThreadID: threadID, Text: text})
	return nil
}

// SendStewardWake 记录隐藏唤醒投递内容，同时计入 sent 让既有断言继续可用。
// wakeFails 大于 0 时这次投递失败并递减，之后的投递恢复正常。
func (f *fakeSessions) SendStewardWake(_ context.Context, threadID, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wakeAttempts++
	if f.wakeFails > 0 {
		f.wakeFails--
		return errSendUnavailable
	}
	if f.sendErr != nil {
		return f.sendErr
	}
	msg := sentMessage{ThreadID: threadID, Text: text}
	f.wakes = append(f.wakes, msg)
	f.sent = append(f.sent, msg)
	return nil
}

// AppendAssistantMessage 记录直接写入的助手消息（开场白走这条路径）。
func (f *fakeSessions) AppendAssistantMessage(threadID, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
	f.appended = append(f.appended, appendedMessage{ThreadID: threadID, Text: text})
	return nil
}

// lose 模拟会话已不存在。
func (f *fakeSessions) lose(threadID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.states, threadID)
}

// seed 预置一个已存在的会话，供“旧版本遗留会话”场景使用。
func (f *fakeSessions) seed(threadID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[threadID] = kanban.SessionState{Running: true}
}

// titleOf 返回建会话时记下的标题。
func (f *fakeSessions) titleOf(threadID string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	title, ok := f.titles[threadID]
	return title, ok
}

// startRequests 返回全部建会话请求。
func (f *fakeSessions) startRequests() []kanban.SessionRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]kanban.SessionRequest(nil), f.starts...)
}

// appendedMessages 返回全部直接写入的助手消息。
func (f *fakeSessions) appendedMessages() []appendedMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]appendedMessage(nil), f.appended...)
}

// messages 返回全部投递内容。
func (f *fakeSessions) messages() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage(nil), f.sent...)
}

// wakeMessages 返回走隐藏入口投递的内容。
func (f *fakeSessions) wakeMessages() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage(nil), f.wakes...)
}

// hostEvents 返回 SendHostEvent 的调用次数。
func (f *fakeSessions) hostEvents() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hostEventCount
}

// fakeSettings 是 Settings 的测试实现。
type fakeSettings struct {
	mu   sync.Mutex
	vals map[string]string
}

// newFakeSettings 返回空的设置仓储。
func newFakeSettings() *fakeSettings { return &fakeSettings{vals: make(map[string]string)} }

// GetSetting 读取设置。
func (f *fakeSettings) GetSetting(_ context.Context, key string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, ok := f.vals[key]
	return value, ok, nil
}

// SetSetting 写入设置。
func (f *fakeSettings) SetSetting(_ context.Context, key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vals[key] = value
	return nil
}

// get 直接读取设置值，供断言使用。
func (f *fakeSettings) get(key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.vals[key]
}

// set 直接写入设置值，用于预置状态。
func (f *fakeSettings) set(key, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vals[key] = value
}

// configHolder 持有可随时替换的配置，模拟运行中用户改配置。
type configHolder struct {
	mu  sync.Mutex
	cfg config.StewardConfig
}

// get 返回当前配置。
func (h *configHolder) get() config.StewardConfig {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg
}

// set 替换当前配置。
func (h *configHolder) set(cfg config.StewardConfig) {
	h.mu.Lock()
	h.cfg = cfg
	h.mu.Unlock()
}

// nameChange 记录一次改名调用。
type nameChange struct {
	// Target 是会话 ID 或项目目录。
	Target string
	// Name 是改成的新名字。
	Name string
}

// fakeNames 是 Names 的测试实现：按会话 ID 与目录保存名字并记录改名调用。
type fakeNames struct {
	mu             sync.Mutex
	sessions       *fakeSessions
	titles         map[string]string
	projects       map[string]string
	threadRenames  []nameChange
	projectRenames []nameChange
	titleErr       error
	projectErr     error
	renameErr      error
}

// newFakeNames 返回空的改名仓储；未预置标题时用 fakeSessions 建会话时记下的标题。
func newFakeNames(sessions *fakeSessions) *fakeNames {
	return &fakeNames{
		sessions: sessions,
		titles:   make(map[string]string),
		projects: make(map[string]string),
	}
}

// ThreadTitle 返回会话当前标题。
func (f *fakeNames) ThreadTitle(_ context.Context, threadID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.titleErr != nil {
		return "", f.titleErr
	}
	if title, ok := f.titles[threadID]; ok {
		return title, nil
	}
	if f.sessions != nil {
		if title, ok := f.sessions.titleOf(threadID); ok {
			return title, nil
		}
	}
	return "", nil
}

// RenameThread 记录并应用一次会话改名。
func (f *fakeNames) RenameThread(_ context.Context, threadID, title string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.renameErr != nil {
		return f.renameErr
	}
	f.titles[threadID] = title
	f.threadRenames = append(f.threadRenames, nameChange{Target: threadID, Name: title})
	return nil
}

// HomeProjectName 返回目录对应项目的名称；没有对应项目时返回空串。
func (f *fakeNames) HomeProjectName(_ context.Context, dir string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.projectErr != nil {
		return "", f.projectErr
	}
	return f.projects[filepath.Clean(dir)], nil
}

// RenameHomeProject 记录并应用一次项目改名。
func (f *fakeNames) RenameHomeProject(_ context.Context, dir, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.renameErr != nil {
		return f.renameErr
	}
	cleanDir := filepath.Clean(dir)
	f.projects[cleanDir] = name
	f.projectRenames = append(f.projectRenames, nameChange{Target: cleanDir, Name: name})
	return nil
}

// setTitle 预置会话当前标题，模拟旧版本遗留的旧名字。
func (f *fakeNames) setTitle(threadID, title string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.titles[threadID] = title
}

// setProject 预置目录对应项目的名称。
func (f *fakeNames) setProject(dir, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.projects[filepath.Clean(dir)] = name
}

// title 返回会话当前标题，口径与 ThreadTitle 一致。
func (f *fakeNames) title(threadID string) string {
	title, _ := f.ThreadTitle(context.Background(), threadID)
	return title
}

// project 返回目录对应项目的名称，口径与 HomeProjectName 一致。
func (f *fakeNames) project(dir string) string {
	name, _ := f.HomeProjectName(context.Background(), dir)
	return name
}

// threadRenameCount 返回会话改名的调用次数。
func (f *fakeNames) threadRenameCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.threadRenames)
}

// projectRenameCount 返回项目改名的调用次数。
func (f *fakeNames) projectRenameCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.projectRenames)
}

// stewardRig 把 Runtime 与各个假依赖、可控时钟组装在一起。
type stewardRig struct {
	t        *testing.T
	clk      *fakeClock
	events   *fakeEvents
	sessions *fakeSessions
	settings *fakeSettings
	names    *fakeNames
	cfg      *configHolder
	rt       *Runtime
	dataDir  string
}

// enabledConfig 返回启用的总管配置。
func enabledConfig() config.StewardConfig {
	return config.StewardConfig{
		Enabled:        true,
		Provider:       "anthropic",
		Model:          "claude-test-model",
		Effort:         "high",
		DailyWakeLimit: 5,
	}
}

// newStewardRig 构造测试用的 Runtime，测试结束自动 Stop。
func newStewardRig(t *testing.T, cfg config.StewardConfig) *stewardRig {
	t.Helper()
	h := &stewardRig{
		t:        t,
		clk:      newFakeClock(),
		events:   newFakeEvents(),
		sessions: newFakeSessions(),
		settings: newFakeSettings(),
		cfg:      &configHolder{cfg: cfg},
		dataDir:  t.TempDir(),
	}
	h.names = newFakeNames(h.sessions)
	h.rt = New(Deps{
		Events:   h.events,
		Sessions: h.sessions,
		Settings: h.settings,
		Config:   h.cfg.get,
		DataDir:  h.dataDir,
		EnsureHomeProject: func(context.Context, string) (string, error) {
			return "proj-steward", nil
		},
		// 话题会话的工作目录取“项目根”这个假目录：建会话只记录请求，不需要真实存在。
		ProjectRoot: func(_ context.Context, projectRef string) (string, string, error) {
			return projectRef, filepath.Join(h.dataDir, projectRef), nil
		},
		Names: h.names,
		Now:   h.clk.Now,
	})
	h.rt.after = h.clk.After
	t.Cleanup(h.rt.Stop)
	return h
}

// settle 给聚合循环一点真实时间消费通道里的事件（虚拟时钟不推进）。
func (h *stewardRig) settle() { time.Sleep(50 * time.Millisecond) }

// settleFor 多等一会儿，用于一批事件逐个入队完成的场景。
func (h *stewardRig) settleFor(d time.Duration) { time.Sleep(d) }

// startSteward 启动运行时，并等到聚合循环挂上第一个定时器：
// 否则测试可能在循环启动前就推进虚拟时钟，使巡检与聚合的截止时间整体后移。
func (h *stewardRig) startSteward() {
	h.t.Helper()
	h.rt.Start()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if h.clk.waiterCount() > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	h.t.Fatal("聚合循环没有在预期时间内挂上定时器")
}

// advance 推进虚拟时间并稍等循环反应。
func (h *stewardRig) advance(d time.Duration) {
	h.clk.Advance(d)
	time.Sleep(20 * time.Millisecond)
}

// advanceUntil 按 step 逐段推进虚拟时钟，直到 cond 成立；超出虚拟时间预算或真实时间上限则失败。
func (h *stewardRig) advanceUntil(desc string, step, budget time.Duration, cond func() bool) {
	h.t.Helper()
	if step <= 0 {
		h.t.Fatalf("推进步长必须为正数：%s", desc)
	}
	if cond() {
		return
	}
	realLimit := time.Now().Add(15 * time.Second)
	for elapsed := time.Duration(0); elapsed <= budget; elapsed += step {
		h.clk.Advance(step)
		time.Sleep(2 * time.Millisecond)
		if cond() {
			return
		}
		if time.Now().After(realLimit) {
			break
		}
	}
	// 循环协程可能还没跑完最后一步，给一次短暂的宽限。
	time.Sleep(100 * time.Millisecond)
	if !cond() {
		h.t.Fatalf("等待超时：%s", desc)
	}
}

// msgCountIs 返回「已投递 n 条」的判定函数。
func (h *stewardRig) msgCountIs(n int) func() bool {
	return func() bool { return len(h.sessions.messages()) == n }
}

// lastText 返回最近一条投递内容。
func (h *stewardRig) lastText() string {
	msgs := h.sessions.messages()
	if len(msgs) == 0 {
		return ""
	}
	return msgs[len(msgs)-1].Text
}

// ev 构造一条待处理事件。
func ev(id, threadID, projectID string) attention.Event {
	return attention.Event{
		ID:          id,
		Kind:        string(attention.KindThreadFailed),
		SubjectType: "thread",
		SubjectID:   threadID,
		ProjectID:   projectID,
		ThreadID:    threadID,
		Title:       "标题 " + id,
		Detail:      "详情 " + id,
		EnteredAt:   time.Date(2025, 3, 4, 9, 0, 0, 0, time.Local),
	}
}

// evOfKind 构造指定类型的事件：安静时段只放行审批、额度、失败三类，
// 需要验证「被拦下」的场景就用其他类型。
func evOfKind(kind attention.Kind, id, threadID, projectID string) attention.Event {
	event := ev(id, threadID, projectID)
	event.Kind = string(kind)
	return event
}

// TestEnsureDisabled 验证配置未启用时既不建会话也不投递。
func TestEnsureDisabled(t *testing.T) {
	h := newStewardRig(t, config.StewardConfig{})
	ctx := context.Background()

	id, err := h.rt.Ensure(ctx)
	if err != nil {
		t.Fatalf("未启用时 Ensure 不应报错，得到 %v", err)
	}
	if id != "" {
		t.Fatalf("未启用时应返回空会话 ID，得到 %q", id)
	}

	h.startSteward()
	h.events.emit(ev("ev-1", "thread-1", "proj-1"))
	h.advance(10 * time.Minute)
	h.settle()

	if n := len(h.sessions.startRequests()); n != 0 {
		t.Fatalf("未启用时不应创建会话，实际创建 %d 个", n)
	}
	if n := len(h.sessions.messages()); n != 0 {
		t.Fatalf("未启用时不应投递，实际投递 %d 条", n)
	}
	if raw := h.settings.get(settingThreadID); raw != "" {
		t.Fatalf("未启用时不应写入会话 ID，得到 %q", raw)
	}
}

// TestEnsureCreatesAndReuses 验证 Ensure 的建会话字段、复用、失效重建与错误传播。
func TestEnsureCreatesAndReuses(t *testing.T) {
	cfg := enabledConfig()
	h := newStewardRig(t, cfg)
	ctx := context.Background()

	first, err := h.rt.Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure 失败: %v", err)
	}
	if first == "" {
		t.Fatal("Ensure 应返回会话 ID")
	}
	starts := h.sessions.startRequests()
	if len(starts) != 1 {
		t.Fatalf("应创建 1 个会话，实际 %d", len(starts))
	}
	req := starts[0]
	if want := cfg.Label(); req.Title != want {
		t.Fatalf("会话标题应为 %q，得到 %q", want, req.Title)
	}
	if want := cfg.ResolvedHomeDir(h.dataDir); req.WorkDir != want {
		t.Fatalf("工作目录应为 %q，得到 %q", want, req.WorkDir)
	}
	if req.ProjectID != "proj-steward" {
		t.Fatalf("项目 ID 应为 proj-steward，得到 %q", req.ProjectID)
	}
	// 不指定权限模式：空值由后端按全局默认权限档位落库，总管与普通会话同权。
	if req.RuntimeMode != "" {
		t.Fatalf("建总管会话不应指定权限模式，得到 %q", req.RuntimeMode)
	}
	if req.Provider != cfg.Provider || req.Model != cfg.Model || req.Effort != cfg.Effort {
		t.Fatalf("模型参数应取自配置，得到 %+v", req)
	}
	// 首条可见消息是开场白而不是指令：建会话不带 Prompt，开场白由运行时写成助手消息。
	if strings.TrimSpace(req.Prompt) != "" {
		t.Fatalf("建会话不应带首条指令，得到 %q", req.Prompt)
	}
	appended := h.sessions.appendedMessages()
	if len(appended) != 1 {
		t.Fatalf("应写入 1 条开场白，实际 %d 条", len(appended))
	}
	if want := OpeningMessage(cfg.Label()); appended[0].ThreadID != first || appended[0].Text != want {
		t.Fatalf("开场白应写入总管会话，得到 %+v，想要 %q", appended[0], want)
	}
	if raw := h.settings.get(settingThreadID); raw != first {
		t.Fatalf("会话 ID 应写回设置，得到 %q", raw)
	}
	if dir := cfg.ResolvedHomeDir(h.dataDir); dir == "" {
		t.Fatal("主目录不应为空")
	} else if _, statErr := os.Stat(dir); statErr != nil {
		t.Fatalf("主目录应已创建: %v", statErr)
	}
	if !h.rt.IsSteward(first) {
		t.Fatal("IsSteward 应识别总管会话")
	}
	if h.rt.IsSteward("") || h.rt.IsSteward("other-thread") {
		t.Fatal("IsSteward 不应识别其他会话")
	}

	again, err := h.rt.Ensure(ctx)
	if err != nil || again != first {
		t.Fatalf("应复用已有会话，得到 %q / %v", again, err)
	}
	if n := len(h.sessions.startRequests()); n != 1 {
		t.Fatalf("复用时不应重复创建，实际 %d 个", n)
	}

	h.sessions.lose(first)
	rebuilt, err := h.rt.Ensure(ctx)
	if err != nil {
		t.Fatalf("会话丢失后重建失败: %v", err)
	}
	if rebuilt == first {
		t.Fatal("会话丢失后应创建新会话")
	}
	if raw := h.settings.get(settingThreadID); raw != rebuilt {
		t.Fatalf("新会话 ID 应写回设置，得到 %q", raw)
	}
	if h.rt.IsSteward(first) || !h.rt.IsSteward(rebuilt) || h.rt.ThreadID() != rebuilt {
		t.Fatal("重建后 IsSteward 与 ThreadID 应指向新会话")
	}
}

// TestEnsureRenamesLegacyStewardNames 验证旧版本遗留的旧名字（会话标题与总管家目录项目名）
// 在 Ensure 复用时改成当前配置名，名字未变时不重复写库，用户改名后再次 Ensure 会再同步一次。
func TestEnsureRenamesLegacyStewardNames(t *testing.T) {
	cfg := enabledConfig()
	h := newStewardRig(t, cfg)
	ctx := context.Background()

	// 旧版本遗留的数据：会话与项目都叫「跟进者」。
	const threadID = "legacy-steward-thread"
	homeDir := cfg.ResolvedHomeDir(h.dataDir)
	h.settings.set(settingThreadID, threadID)
	h.sessions.seed(threadID)
	h.names.setTitle(threadID, "跟进者")
	h.names.setProject(homeDir, "跟进者")

	got, err := h.rt.Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure 失败: %v", err)
	}
	if got != threadID {
		t.Fatalf("应复用已有会话 %q，得到 %q", threadID, got)
	}
	want := cfg.Label()
	if title := h.names.title(threadID); title != want {
		t.Fatalf("会话标题应改为 %q，得到 %q", want, title)
	}
	if name := h.names.project(homeDir); name != want {
		t.Fatalf("总管项目名应改为 %q，得到 %q", want, name)
	}
	if n := h.names.threadRenameCount(); n != 1 {
		t.Fatalf("会话应改名 1 次，实际 %d 次", n)
	}
	if n := h.names.projectRenameCount(); n != 1 {
		t.Fatalf("项目应改名 1 次，实际 %d 次", n)
	}

	// 名字没变：再次 Ensure 不应再写库。
	if _, err := h.rt.Ensure(ctx); err != nil {
		t.Fatalf("再次 Ensure 失败: %v", err)
	}
	if n := h.names.threadRenameCount(); n != 1 {
		t.Fatalf("名字未变时不应重复改会话标题，实际 %d 次", n)
	}
	if n := h.names.projectRenameCount(); n != 1 {
		t.Fatalf("名字未变时不应重复改项目名，实际 %d 次", n)
	}

	// 用户在设置里改名后，下一次 Ensure 再同步一次。
	renamed := cfg
	renamed.Name = "小助"
	h.cfg.set(renamed)
	if _, err := h.rt.Ensure(ctx); err != nil {
		t.Fatalf("改名后 Ensure 失败: %v", err)
	}
	if title := h.names.title(threadID); title != "小助 · 总管" {
		t.Fatalf("会话标题应改为 %q，得到 %q", "小助 · 总管", title)
	}
	if name := h.names.project(homeDir); name != "小助 · 总管" {
		t.Fatalf("项目名应改为 %q，得到 %q", "小助 · 总管", name)
	}
	if n := h.names.threadRenameCount(); n != 2 {
		t.Fatalf("改名后会话标题应再改 1 次，累计 2 次，实际 %d 次", n)
	}
}

// TestEnsureMigratesLegacyDefaultName 验证昵称为空时，旧版本遗留的标题与项目名「默默」在 Ensure 时改成「总管」。
func TestEnsureMigratesLegacyDefaultName(t *testing.T) {
	cfg := enabledConfig()
	h := newStewardRig(t, cfg)
	ctx := context.Background()

	const threadID = "legacy-default-name-thread"
	homeDir := cfg.ResolvedHomeDir(h.dataDir)
	h.settings.set(settingThreadID, threadID)
	h.sessions.seed(threadID)
	h.names.setTitle(threadID, "默默")
	h.names.setProject(homeDir, "默默")

	if _, err := h.rt.Ensure(ctx); err != nil {
		t.Fatalf("Ensure 失败: %v", err)
	}
	if title := h.names.title(threadID); title != "总管" {
		t.Fatalf("旧标题应改成 %q，得到 %q", "总管", title)
	}
	if name := h.names.project(homeDir); name != "总管" {
		t.Fatalf("旧项目名应改成 %q，得到 %q", "总管", name)
	}
}

// TestEnsureSkipsNameSyncWhenUnavailable 验证改名能力未接线或改名失败时 Ensure 照常返回会话。
func TestEnsureSkipsNameSyncWhenUnavailable(t *testing.T) {
	h := newStewardRig(t, enabledConfig())
	ctx := context.Background()

	const threadID = "legacy-steward-thread"
	h.settings.set(settingThreadID, threadID)
	h.sessions.seed(threadID)
	h.names.setTitle(threadID, "跟进者")

	// 改名失败只记日志，不影响 Ensure 复用会话。
	h.names.renameErr = errors.New("boom")
	if id, err := h.rt.Ensure(ctx); err != nil || id != threadID {
		t.Fatalf("改名失败不应影响 Ensure，得到 %q / %v", id, err)
	}
	if title := h.names.title(threadID); title != "跟进者" {
		t.Fatalf("改名失败时应保持原标题，得到 %q", title)
	}

	// 改名能力未接线时 Ensure 照常可用，也不会去改名字。
	h.names.renameErr = nil
	h.rt.names = nil
	if id, err := h.rt.Ensure(ctx); err != nil || id != threadID {
		t.Fatalf("未接线改名能力时 Ensure 应照常返回会话，得到 %q / %v", id, err)
	}
	if title := h.names.title(threadID); title != "跟进者" {
		t.Fatalf("未接线改名能力时不应改标题，得到 %q", title)
	}
}

// TestEnsureCreatesWithConfiguredName 验证配置名不是默认名时，新建的总管会话与家目录项目都用配置名。
func TestEnsureCreatesWithConfiguredName(t *testing.T) {
	cfg := enabledConfig()
	cfg.Name = "小助"
	h := newStewardRig(t, cfg)
	homeDir := cfg.ResolvedHomeDir(h.dataDir)
	// ensureStewardProject 以占位名建项目，随后由名字同步改成配置名。
	h.names.setProject(homeDir, "总管")

	threadID, err := h.rt.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure 失败: %v", err)
	}
	if title := h.names.title(threadID); title != "小助 · 总管" {
		t.Fatalf("新建会话标题应为 %q，得到 %q", "小助 · 总管", title)
	}
	if n := h.names.threadRenameCount(); n != 0 {
		t.Fatalf("新建会话标题已正确，不应再改名，实际 %d 次", n)
	}
	if name := h.names.project(homeDir); name != "小助 · 总管" {
		t.Fatalf("项目名应为 %q，得到 %q", "小助 · 总管", name)
	}
}

// TestRuntimeIdentitySection 验证运行时只给总管会话产出身份段，并现读当前名字。
func TestRuntimeIdentitySection(t *testing.T) {
	cfg := enabledConfig()
	cfg.Name = " 小助 "
	h := newStewardRig(t, cfg)

	if got := h.rt.IdentitySection("other-thread"); got != "" {
		t.Fatalf("非总管会话不应有身份段，得到 %q", got)
	}

	threadID, err := h.rt.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure 失败: %v", err)
	}
	section := h.rt.IdentitySection(threadID)
	for _, want := range []string{"「小助 · 总管」", "用户的同事", "steward_assign_task"} {
		if !strings.Contains(section, want) {
			t.Fatalf("身份段应包含 %q，实际为：\n%s", want, section)
		}
	}
}

// TestEnsureErrors 验证建会话失败、查询状态失败都会报错而不是静默吞掉。
func TestEnsureErrors(t *testing.T) {
	h := newStewardRig(t, enabledConfig())
	ctx := context.Background()

	h.sessions.startErr = errors.New("boom")
	if _, err := h.rt.Ensure(ctx); err == nil || !strings.Contains(err.Error(), "创建总管会话失败") {
		t.Fatalf("建会话失败应返回包装错误，得到 %v", err)
	}
	h.sessions.startErr = nil

	h.settings.set(settingThreadID, "ghost-thread")
	h.sessions.stateErr = errors.New("io error")
	if _, err := h.rt.Ensure(ctx); err == nil || !strings.Contains(err.Error(), "查询总管会话状态失败") {
		t.Fatalf("查询状态失败应返回包装错误，得到 %v", err)
	}
	h.sessions.stateErr = nil

	id, err := h.rt.Ensure(ctx)
	if err != nil || id == "" {
		t.Fatalf("会话不存在时应重建，得到 %q / %v", id, err)
	}
}

// TestAggregationWindowAndCooldown 验证 20 秒聚合与 2 分钟冷却。
func TestAggregationWindowAndCooldown(t *testing.T) {
	h := newStewardRig(t, enabledConfig())
	h.startSteward()

	h.events.emit(ev("ev-a", "thread-9", "proj-1"))
	h.settle()

	h.advance(19 * time.Second)
	if n := len(h.sessions.messages()); n != 0 {
		t.Fatalf("20 秒聚合窗口内不应投递，实际投递 %d 条", n)
	}

	h.advanceUntil("20 秒后投递首次唤醒", time.Second, 25*time.Second, h.msgCountIs(1))
	first := h.sessions.messages()[0]
	if first.ThreadID != h.rt.ThreadID() {
		t.Fatalf("应投递到总管会话，得到 %q", first.ThreadID)
	}
	for _, want := range []string{"ev-a", "会话失败", "标题 ev-a", "详情 ev-a", "相关会话：thread-9", "attention_resolve"} {
		if !strings.Contains(first.Text, want) {
			t.Fatalf("唤醒消息应包含 %q，实际为：\n%s", want, first.Text)
		}
	}

	// 冷却：紧接着的第二条事件要等到距上次唤醒满 2 分钟。
	h.events.emit(ev("ev-b", "thread-10", "proj-1"))
	h.settle()
	h.advance(45 * time.Second)
	h.settle()
	if n := len(h.sessions.messages()); n != 1 {
		t.Fatalf("2 分钟冷却内不应再次投递，实际投递 %d 条", n)
	}

	h.advanceUntil("冷却结束后投递第二次唤醒", 5*time.Second, 3*time.Minute, h.msgCountIs(2))
	if last := h.lastText(); !strings.Contains(last, "ev-b") {
		t.Fatalf("第二次唤醒应带上新事件，实际为：\n%s", last)
	}
}

// TestOwnThreadEventsDropped 验证总管自身会话的事件不会唤醒它自己。
func TestOwnThreadEventsDropped(t *testing.T) {
	h := newStewardRig(t, enabledConfig())
	ctx := context.Background()
	self, err := h.rt.Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure 失败: %v", err)
	}

	h.startSteward()
	h.events.emit(ev("ev-self", self, "proj-1"))
	h.advance(3 * time.Minute)
	h.settle()

	if n := len(h.sessions.messages()); n != 0 {
		t.Fatalf("总管自身会话的事件应被丢弃，实际投递 %d 条", n)
	}
}

// TestApprovalAndQuotaWakeSteward 验证额度用尽先投递，审批跨批次保留，宽限期后才唤醒总管。
func TestApprovalAndQuotaWakeSteward(t *testing.T) {
	h := newStewardRig(t, enabledConfig())
	h.startSteward()
	approvalFlushAt := h.clk.Now().Add(ownerActionGrace + aggregateWindow)

	approval := ev("ev-approval", "thread-1", "proj-1")
	approval.Kind = string(attention.KindThreadApproval)
	h.events.emit(approval)
	quota := ev("ev-quota", "thread-2", "proj-1")
	quota.Kind = string(attention.KindThreadQuota)
	h.events.emit(quota)
	h.settle()

	h.advanceUntil("额度用尽先唤醒总管", time.Second, time.Minute, h.msgCountIs(1))
	text := h.lastText()
	if !strings.Contains(text, "ev-quota") || !strings.Contains(text, "额度用尽") || strings.Contains(text, "ev-approval") {
		t.Fatalf("首次唤醒应只包含额度用尽事件，实际为：\n%s", text)
	}

	// 额度事件投递后 seen 被清空，重复审批通知仍不能延长原来的宽限期。
	h.advance(time.Minute)
	h.events.emit(approval)
	h.settle()
	h.advanceUntil("宽限期结束后审批唤醒总管", time.Second, approvalFlushAt.Sub(h.clk.Now())+time.Second, h.msgCountIs(2))
	text = h.lastText()
	for _, want := range []string{"ev-approval", "会话等待审批"} {
		if !strings.Contains(text, want) {
			t.Fatalf("审批唤醒消息应包含 %q，实际为：\n%s", want, text)
		}
	}
	if strings.Contains(text, "ev-quota") {
		t.Fatalf("已投递的额度事件不应重复投递，实际为：\n%s", text)
	}
}

// TestApprovalResolvedWithinGraceDoesNotWake 验证审批在宽限期内被用户处理后不再唤醒总管。
func TestApprovalResolvedWithinGraceDoesNotWake(t *testing.T) {
	h := newStewardRig(t, enabledConfig())
	h.startSteward()
	approval := evOfKind(attention.KindThreadApproval, "ev-approval", "thread-1", "proj-1")
	h.events.emit(approval)
	h.settle()
	h.advance(ownerActionGrace / 2)
	if n := len(h.sessions.messages()); n != 0 {
		t.Fatalf("审批宽限期内不应唤醒总管，实际投递 %d 条", n)
	}

	if err := h.events.Resolve(context.Background(), approval.ID, "user"); err != nil {
		t.Fatalf("消解审批事件失败: %v", err)
	}
	resolvedAt := h.clk.Now()
	approval.ResolvedAt = &resolvedAt
	h.events.emit(approval)
	h.settle()
	h.advance(ownerActionGrace)
	h.advance(aggregateWindow)
	if n := len(h.sessions.messages()); n != 0 {
		t.Fatalf("宽限期内已消解的审批不应唤醒总管，实际投递 %d 条", n)
	}
}

// TestApprovalWakesAfterGrace 验证未处理的审批宽限期后进入聚合，并带上未处理时长说明。
func TestApprovalWakesAfterGrace(t *testing.T) {
	h := newStewardRig(t, enabledConfig())
	h.startSteward()
	h.events.emit(evOfKind(attention.KindThreadApproval, "ev-approval", "thread-1", "proj-1"))
	h.settle()
	h.advance(ownerActionGrace - time.Second)
	if n := len(h.sessions.messages()); n != 0 {
		t.Fatalf("审批宽限期内不应唤醒总管，实际投递 %d 条", n)
	}
	h.advance(time.Second)
	h.settle()
	if n := len(h.sessions.messages()); n != 0 {
		t.Fatalf("宽限期到期后仍应等待聚合窗口，实际投递 %d 条", n)
	}
	h.advance(aggregateWindow - time.Second)
	if n := len(h.sessions.messages()); n != 0 {
		t.Fatalf("审批聚合窗口内不应投递，实际投递 %d 条", n)
	}
	h.advanceUntil("宽限期及聚合窗口结束后投递审批", time.Second, time.Minute, h.msgCountIs(1))
	text := h.lastText()
	for _, want := range []string{"ev-approval", fmt.Sprintf("用户已超过 %d 分钟未处理", ownerActionGrace/time.Minute)} {
		if !strings.Contains(text, want) {
			t.Fatalf("审批唤醒消息应包含 %q，实际为：\n%s", want, text)
		}
	}
}

// TestFailedResolvedWithinAggregationDoesNotWake 验证非审批事件在聚合窗口内消解后不再投递。
func TestFailedResolvedWithinAggregationDoesNotWake(t *testing.T) {
	h := newStewardRig(t, enabledConfig())
	h.startSteward()
	failed := evOfKind(attention.KindThreadFailed, "ev-failed", "thread-1", "proj-1")
	h.events.emit(failed)
	h.settle()
	h.advance(aggregateWindow / 2)
	if err := h.events.Resolve(context.Background(), failed.ID, "user"); err != nil {
		t.Fatalf("消解失败事件失败: %v", err)
	}
	resolvedAt := h.clk.Now()
	failed.ResolvedAt = &resolvedAt
	h.events.emit(failed)
	h.settle()
	h.advance(aggregateWindow)
	if n := len(h.sessions.messages()); n != 0 {
		t.Fatalf("聚合窗口内已消解的失败事件不应投递，实际投递 %d 条", n)
	}
}

// TestEventPolicyFiltersWake 验证按事件类型过滤即时唤醒：自动化正常完成默认留给巡检，
// 会话失败照常进入待投递批次，配置成 patrol 后也不进入。
func TestEventPolicyFiltersWake(t *testing.T) {
	t.Run("automation_finished 默认不立刻唤醒", func(t *testing.T) {
		h := newStewardRig(t, enabledConfig())
		h.startSteward()
		h.events.emit(evOfKind(attention.KindAutomationFinished, "ev-auto", "thread-1", "proj-1"))
		h.advance(3 * time.Minute)
		h.settle()
		if n := len(h.sessions.messages()); n != 0 {
			t.Fatalf("自动化完成默认应留给巡检，实际投递 %d 条", n)
		}

		// 巡检只数唤醒策略为 wake 的事件：automation_finished 默认是 patrol，
		// 仍在事件列表与收件箱里，但不再由巡检单独唤醒。
		h.advance(3 * time.Hour)
		h.settle()
		if n := len(h.sessions.messages()); n != 0 {
			t.Fatalf("自动化完成默认不应由巡检唤醒，实际投递 %d 条", n)
		}
	})

	t.Run("thread_failed 照常立刻唤醒", func(t *testing.T) {
		h := newStewardRig(t, enabledConfig())
		h.startSteward()
		failed := evOfKind(attention.KindThreadFailed, "ev-failed", "thread-1", "proj-1")
		h.events.emit(failed)
		h.advanceUntil("会话失败事件唤醒总管", time.Second, 5*time.Minute, h.msgCountIs(1))
		if text := h.lastText(); !strings.Contains(text, "ev-failed") {
			t.Fatalf("唤醒消息应包含 ev-failed，实际为：\n%s", text)
		}
	})

	t.Run("thread_failed 配置为 patrol 后不立刻唤醒", func(t *testing.T) {
		cfg := enabledConfig()
		cfg.EventPolicies = map[string]string{"thread_failed": " patrol "}
		h := newStewardRig(t, cfg)
		h.startSteward()
		h.events.emit(evOfKind(attention.KindThreadFailed, "ev-failed", "thread-1", "proj-1"))
		h.advance(3 * time.Minute)
		h.settle()
		if n := len(h.sessions.messages()); n != 0 {
			t.Fatalf("配置为 patrol 的事件不应立刻唤醒，实际投递 %d 条", n)
		}
	})
}

// TestRideNotesRideAlongNextWake 验证顺风车通知自己不触发唤醒，只随下一次事件唤醒送达；
// 投递后清空不重复，超过 maxRideNotes 时只保留最新几条。
func TestRideNotesRideAlongNextWake(t *testing.T) {
	h := newStewardRig(t, enabledConfig())
	h.startSteward()

	h.rt.enqueueRideNote("顺风车-旧摘要")
	h.advance(3 * time.Minute)
	h.settle()
	if n := len(h.sessions.messages()); n != 0 {
		t.Fatalf("顺风车通知不应自己触发唤醒，实际投递 %d 条", n)
	}

	h.events.emit(ev("ev-ride-1", "thread-1", "proj-1"))
	h.advanceUntil("事件唤醒并带上顺风车摘要", time.Second, 5*time.Minute, h.msgCountIs(1))
	text := h.lastText()
	for _, want := range []string{"ev-ride-1", "话题直通", "顺风车-旧摘要"} {
		if !strings.Contains(text, want) {
			t.Fatalf("唤醒消息应包含 %q，实际为：\n%s", want, text)
		}
	}

	// 投递后 rides 已清空：再来一条事件不应重复带上旧摘要。
	h.events.emit(ev("ev-ride-2", "thread-2", "proj-1"))
	h.advanceUntil("第二条事件再次唤醒", time.Second, 5*time.Minute, h.msgCountIs(2))
	if text := h.lastText(); strings.Contains(text, "顺风车-旧摘要") || strings.Contains(text, "话题直通") {
		t.Fatalf("投递后不应重复包含旧的顺风车摘要，实际为：\n%s", text)
	}

	// 超过上限时只保留最新 maxRideNotes 条。
	for i := 0; i < maxRideNotes+5; i++ {
		h.rt.enqueueRideNote(fmt.Sprintf("顺风车-%02d", i))
	}
	h.settle()
	h.events.emit(ev("ev-ride-3", "thread-3", "proj-1"))
	h.advanceUntil("第三条事件唤醒", time.Second, 5*time.Minute, h.msgCountIs(3))
	text = h.lastText()
	if strings.Contains(text, "顺风车-00") || strings.Contains(text, "顺风车-04") {
		t.Fatalf("超过 %d 条时应丢掉最旧的，实际为：\n%s", maxRideNotes, text)
	}
	if !strings.Contains(text, "顺风车-05") || !strings.Contains(text, fmt.Sprintf("顺风车-%02d", maxRideNotes+4)) {
		t.Fatalf("应保留最新 %d 条顺风车通知，实际为：\n%s", maxRideNotes, text)
	}
}

// TestWakeGoesHidden 验证事件唤醒与兜底巡检都走隐藏入口 SendStewardWake，不再走 SendHostEvent。
func TestWakeGoesHidden(t *testing.T) {
	h := newStewardRig(t, enabledConfig())
	h.startSteward()

	h.events.emit(ev("ev-hidden", "thread-1", "proj-1"))
	h.advanceUntil("事件唤醒总管", time.Second, 5*time.Minute, h.msgCountIs(1))

	h.events.setCount(1)
	h.advanceUntil("兜底巡检再次投递", 10*time.Minute, 3*time.Hour, h.msgCountIs(2))

	if got := len(h.sessions.wakeMessages()); got != 2 {
		t.Fatalf("事件唤醒与巡检都应走 SendStewardWake，实际 %d 条", got)
	}
	if got := h.sessions.hostEvents(); got != 0 {
		t.Fatalf("唤醒不应走 SendHostEvent，实际调用 %d 次", got)
	}
}

// TestPatrolCountsQuotaButSkipsApproval 验证巡检只数总管该管的事件：额度用尽照常计入，
// 等审批属于用户亲手处理的白名单，不再让巡检反复催。
func TestPatrolCountsQuotaButSkipsApproval(t *testing.T) {
	h := newStewardRig(t, enabledConfig())
	quota := ev("ev-quota", "thread-1", "proj-1")
	quota.Kind = string(attention.KindThreadQuota)
	approval := ev("ev-approval", "thread-2", "proj-1")
	approval.Kind = string(attention.KindThreadApproval)
	h.events.mu.Lock()
	h.events.events = append(h.events.events, quota, approval)
	h.events.mu.Unlock()
	h.startSteward()

	h.advanceUntil("巡检只带额度用尽事件", 10*time.Minute, 3*time.Hour, h.msgCountIs(1))
	text := h.lastText()
	for _, want := range []string{"兜底巡检", "1 条待处理事件"} {
		if !strings.Contains(text, want) {
			t.Fatalf("巡检消息应包含 %q，实际为：\n%s", want, text)
		}
	}
}

// TestResolvedNotificationDropped 验证消解通知不会触发唤醒。
func TestResolvedNotificationDropped(t *testing.T) {
	h := newStewardRig(t, enabledConfig())
	h.startSteward()

	resolved := ev("ev-done", "thread-2", "proj-1")
	at := time.Date(2025, 3, 4, 9, 30, 0, 0, time.Local)
	resolved.ResolvedAt = &at
	h.events.emit(resolved)
	h.advance(3 * time.Minute)
	h.settle()

	if n := len(h.sessions.messages()); n != 0 {
		t.Fatalf("消解通知不应唤醒总管，实际投递 %d 条", n)
	}
}

// TestBatchTruncation 验证单批超过 20 条时截断并提示剩余条数。
func TestBatchTruncation(t *testing.T) {
	h := newStewardRig(t, enabledConfig())
	h.startSteward()

	for i := 1; i <= 25; i++ {
		h.events.emit(ev(fmt.Sprintf("ev-%02d", i), fmt.Sprintf("thread-%02d", i), "proj-1"))
	}
	h.settleFor(200 * time.Millisecond)

	h.advanceUntil("投递聚合唤醒", time.Second, 25*time.Second, h.msgCountIs(1))
	text := h.lastText()
	if !strings.Contains(text, "ev-01") || !strings.Contains(text, "ev-20") {
		t.Fatalf("前 20 条都应出现，实际为：\n%s", text)
	}
	if strings.Contains(text, "ev-21") {
		t.Fatalf("第 21 条及之后不应逐条列出，实际为：\n%s", text)
	}
	if !strings.Contains(text, "另有 5 条") {
		t.Fatalf("应提示剩余 5 条，实际为：\n%s", text)
	}
	if !strings.Contains(text, "attention_list") {
		t.Fatalf("应提示用 attention_list 查看剩余事件，实际为：\n%s", text)
	}
}

// TestDailyWakeLimitAndNextDayReset 验证每日唤醒上限、达到上限后丢弃与次日归零。
func TestDailyWakeLimitAndNextDayReset(t *testing.T) {
	cfg := enabledConfig()
	cfg.DailyWakeLimit = 2
	h := newStewardRig(t, cfg)
	h.startSteward()

	h.events.emit(ev("ev-1", "thread-1", "proj-1"))
	h.settle()
	h.advanceUntil("第一次唤醒", time.Second, 30*time.Second, h.msgCountIs(1))

	h.events.emit(ev("ev-2", "thread-2", "proj-1"))
	h.settle()
	h.advanceUntil("第二次唤醒", 5*time.Second, 3*time.Minute, h.msgCountIs(2))

	if got := h.settings.get(settingWakeCount); got != "2" {
		t.Fatalf("唤醒次数应持久化为 2，得到 %q", got)
	}
	if got := h.settings.get(settingWakeDay); got != h.clk.Now().Format(dateLayout) {
		t.Fatalf("唤醒日期应持久化为当天，得到 %q", got)
	}
	if got := h.settings.get(settingLastWakeAt); got == "" {
		t.Fatal("上次唤醒时间应持久化")
	}

	h.events.emit(ev("ev-3", "thread-3", "proj-1"))
	h.settle()
	h.advance(5 * time.Minute)
	h.settle()
	if n := len(h.sessions.messages()); n != 2 {
		t.Fatalf("达到每日上限后应丢弃，实际投递 %d 条", n)
	}
	if got := h.settings.get(settingWakeCount); got != "2" {
		t.Fatalf("达到上限后计数不应变化，得到 %q", got)
	}

	// 次日：兜底巡检不再打扰（未消解数按 0 计），事件唤醒应恢复。
	h.events.setCount(0)
	h.advance(24 * time.Hour)
	h.settle()
	h.events.emit(ev("ev-4", "thread-4", "proj-1"))
	h.settle()
	h.advanceUntil("次日恢复唤醒", time.Second, time.Minute, h.msgCountIs(3))
	if got := h.settings.get(settingWakeCount); got != "1" {
		t.Fatalf("次日计数应从 0 重新计数，得到 %q", got)
	}
}

// wakeLimitNotices 返回用户可见的「今天到上限了」说明：它走的是可见助手消息，不是隐藏唤醒。
func (h *stewardRig) wakeLimitNotices() []string {
	var out []string
	for _, msg := range h.sessions.appendedMessages() {
		if strings.HasPrefix(msg.Text, "今天已经主动找你") {
			out = append(out, msg.Text)
		}
	}
	return out
}

// TestWakeLimitNoticeOncePerDay 验证每日上限用完后当天只向用户说明一次，次日可以再说一次。
func TestWakeLimitNoticeOncePerDay(t *testing.T) {
	cfg := enabledConfig()
	cfg.DailyWakeLimit = 1
	h := newStewardRig(t, cfg)
	h.startSteward()

	h.events.emit(ev("ev-1", "thread-1", "proj-1"))
	h.settle()
	h.advanceUntil("第一次唤醒", time.Second, 30*time.Second, h.msgCountIs(1))
	h.settle()
	if used, limit := h.rt.WakesRemainingToday(); used != 1 || limit != 1 {
		t.Fatalf("用满上限时应报 used=1 limit=1，得到 used=%d limit=%d", used, limit)
	}

	// 两批超上限的事件：到上限后不再唤醒，但说明只能有一条。
	for _, batch := range [][]string{{"ev-2", "ev-3"}, {"ev-4", "ev-5"}} {
		for _, id := range batch {
			h.events.emit(ev(id, "thread-1", "proj-1"))
		}
		h.settle()
		h.advance(5 * time.Minute)
		h.settle()
	}
	if n := len(h.sessions.messages()); n != 1 {
		t.Fatalf("达到上限后不应再唤醒，实际投递 %d 条", n)
	}
	notices := h.wakeLimitNotices()
	if len(notices) != 1 {
		t.Fatalf("同一天只应说明一次上限，实际 %d 条：%v", len(notices), notices)
	}
	if !strings.Contains(notices[0], "今天已经主动找你 1 次了") {
		t.Fatalf("说明里应带上限次数，实际为：%s", notices[0])
	}
	if got := h.settings.get(settingWakeLimitNoticePrefix + h.clk.Now().Format(dateLayout)); got != "1" {
		t.Fatalf("上限说明应记为当天已发，得到 %q", got)
	}

	// 次日：计数从 0 开始，上限说明也可以再来一次。
	h.events.setCount(0)
	h.advance(24 * time.Hour)
	h.settle()
	if used, limit := h.rt.WakesRemainingToday(); used != 0 || limit != 1 {
		t.Fatalf("次日应报 used=0 limit=1，得到 used=%d limit=%d", used, limit)
	}
	h.events.emit(ev("ev-6", "thread-2", "proj-1"))
	h.settle()
	h.advanceUntil("次日唤醒", time.Second, time.Minute, h.msgCountIs(2))
	h.events.emit(ev("ev-7", "thread-3", "proj-1"))
	h.settle()
	h.advance(5 * time.Minute)
	h.settle()
	if n := len(h.sessions.messages()); n != 2 {
		t.Fatalf("次日超过上限后仍不应再唤醒，实际投递 %d 条", n)
	}
	if got := len(h.wakeLimitNotices()); got != 2 {
		t.Fatalf("次日应再说明一次上限，实际共 %d 条", got)
	}
}

// TestUserActivityDefersWake 验证用户刚和总管说过话时不插话：事件留在积压，用户停下来后照常送达。
func TestUserActivityDefersWake(t *testing.T) {
	h := newStewardRig(t, enabledConfig())
	var busy atomic.Bool
	busy.Store(true)
	h.rt.userActedRecently = func(context.Context, string) bool { return busy.Load() }
	h.startSteward()

	h.events.emit(ev("ev-u1", "thread-1", "proj-1"))
	h.settle()
	for i := 0; i < 10; i++ {
		h.advance(time.Minute)
	}
	h.settle()
	if n := len(h.sessions.messages()); n != 0 {
		t.Fatalf("用户发过言时不应插话，实际投递 %d 条", n)
	}
	if got := h.settings.get(settingWakeCount); got != "" {
		t.Fatalf("推迟期间不应记唤醒，得到 %q", got)
	}

	busy.Store(false)
	h.advanceUntil("用户停下来后照常送达", 10*time.Second, 3*time.Minute, h.msgCountIs(1))
	if text := h.lastText(); !strings.Contains(text, "ev-u1") {
		t.Fatalf("推迟的事件应照常送达，实际为：\n%s", text)
	}
}

// TestPatrolWake 验证兜底巡检：满 10 分钟检查、有未消解事件且距上次唤醒满 2 小时才投递。
func TestPatrolWake(t *testing.T) {
	h := newStewardRig(t, enabledConfig())
	h.events.seedUnresolved(3)
	h.events.setCount(3)
	h.startSteward()

	h.advanceUntil("首次兜底巡检投递", 10*time.Minute, 12*time.Minute, h.msgCountIs(1))
	text := h.lastText()
	for _, want := range []string{"兜底巡检", "3 条", "attention_list"} {
		if !strings.Contains(text, want) {
			t.Fatalf("巡检消息应包含 %q，实际为：\n%s", want, text)
		}
	}

	h.advance(10 * time.Minute)
	h.settle()
	if n := len(h.sessions.messages()); n != 1 {
		t.Fatalf("距上次唤醒不足 2 小时不应巡检投递，实际投递 %d 条", n)
	}

	h.advanceUntil("满 2 小时后巡检再次投递", 5*time.Minute, 3*time.Hour, h.msgCountIs(2))
}

// TestPatrolSkippedWithoutUnresolved 验证没有未消解事件时巡检不打扰用户。
func TestPatrolSkippedWithoutUnresolved(t *testing.T) {
	h := newStewardRig(t, enabledConfig())
	h.events.setCount(0)
	h.startSteward()

	h.advance(11 * time.Minute)
	h.settle()
	if n := len(h.sessions.messages()); n != 0 {
		t.Fatalf("没有未消解事件时不应巡检投递，实际投递 %d 条", n)
	}
}

// TestPatrolSkipsOwnerActionKinds 验证只剩用户亲手处理的审批与验收事件时巡检不唤醒：
// 这些事件收件箱已经展示，反复催只会白耗唤醒额度。
func TestPatrolSkipsOwnerActionKinds(t *testing.T) {
	h := newStewardRig(t, enabledConfig())
	approval := ev("ev-approval", "thread-1", "proj-1")
	approval.Kind = string(attention.KindThreadApproval)
	review := ev("ev-review", "thread-2", "proj-1")
	review.Kind = string(attention.KindKanbanReview)
	h.events.mu.Lock()
	h.events.events = append(h.events.events, approval, review)
	h.events.mu.Unlock()
	h.startSteward()

	h.advance(11 * time.Minute)
	h.settle()
	if n := len(h.sessions.messages()); n != 0 {
		t.Fatalf("只有用户白名单事件时不应巡检投递，实际投递 %d 条", n)
	}
}

// TestPatrolWakesForStewardOwnedEvent 验证政策为 wake 且不属于用户白名单的事件照常触发巡检。
func TestPatrolWakesForStewardOwnedEvent(t *testing.T) {
	h := newStewardRig(t, enabledConfig())
	failed := ev("ev-kanban", "thread-1", "proj-1")
	failed.Kind = string(attention.KindKanbanFailed)
	h.events.mu.Lock()
	h.events.events = append(h.events.events, failed)
	h.events.mu.Unlock()
	h.startSteward()

	h.advanceUntil("看板失败事件触发巡检", 10*time.Minute, 3*time.Hour, h.msgCountIs(1))
	if text := h.lastText(); !strings.Contains(text, "1 条待处理事件") {
		t.Fatalf("巡检消息应统计看板失败事件，实际为：\n%s", text)
	}
}

// TestStopHaltsDelivery 验证 Stop 取消订阅、幂等，并且之后不再投递（Stop 返回即代表 goroutine 已退出）。
func TestStopHaltsDelivery(t *testing.T) {
	h := newStewardRig(t, enabledConfig())
	h.startSteward()

	h.rt.Stop()
	if got := h.events.stopCount(); got != 1 {
		t.Fatalf("Stop 应取消订阅 1 次，实际 %d 次", got)
	}
	h.rt.Stop()

	h.events.emit(ev("ev-1", "thread-1", "proj-1"))
	h.advance(30 * time.Minute)
	h.settle()

	if n := len(h.sessions.messages()); n != 0 {
		t.Fatalf("Stop 之后不应投递，实际投递 %d 条", n)
	}
	if n := len(h.sessions.startRequests()); n != 0 {
		t.Fatalf("Stop 之后不应再创建会话，实际创建 %d 个", n)
	}
}

// TestStopWaitsForBackgroundEnsure 验证 Stop 会取消并等待后台的总管会话准备。
// 否则宿主关 store 之后这次准备仍在写库，退出时报「database is closed」。
func TestStopWaitsForBackgroundEnsure(t *testing.T) {
	h := newStewardRig(t, enabledConfig())
	h.startSteward()

	gate := make(chan struct{})
	entered := make(chan struct{})
	exited := make(chan struct{})
	h.sessions.mu.Lock()
	h.sessions.startGate = gate
	h.sessions.startEntered = entered
	h.sessions.startExited = exited
	h.sessions.mu.Unlock()

	h.rt.EnsureInBackground(time.Minute)
	// 先等后台真的进到建会话这一步，再让 Stop 去取消它。
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("后台准备没有在预期时间内开始建会话")
	}

	stopped := make(chan struct{})
	go func() {
		h.rt.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop 没有在预期时间内返回")
	}

	// Stop 返回即代表后台准备已退出：建会话那一步必须已经收尾。
	select {
	case <-exited:
	default:
		t.Fatal("Stop 返回时后台建会话仍在进行")
	}
	h.sessions.mu.Lock()
	ctxErr := h.sessions.startCtxErr
	h.sessions.mu.Unlock()
	if !errors.Is(ctxErr, context.Canceled) {
		t.Fatalf("后台准备应因 Stop 取消而结束，实际 ctx 错误 %v", ctxErr)
	}
}

// TestEnsureInBackgroundWithoutRuntime 验证没有运行时，后台准备是空操作，不会建会话。
func TestEnsureInBackgroundWithoutRuntime(t *testing.T) {
	h := newStewardRig(t, enabledConfig())

	h.rt.EnsureInBackground(time.Millisecond)
	time.Sleep(50 * time.Millisecond)

	if n := len(h.sessions.startRequests()); n != 0 {
		t.Fatalf("未 Start 时不应建会话，实际创建 %d 个", n)
	}
	if got := h.settings.get(settingThreadID); got != "" {
		t.Fatalf("未 Start 时不应写总管会话 ID，实际 %q", got)
	}

	// Stop 之后再调用同样是空操作。
	stopped := newStewardRig(t, enabledConfig())
	stopped.startSteward()
	stopped.rt.Stop()
	stopped.rt.EnsureInBackground(time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	if n := len(stopped.sessions.startRequests()); n != 0 {
		t.Fatalf("Stop 之后不应建会话，实际创建 %d 个", n)
	}
}

// TestHooks 验证 MCP 钩子的映射、消解者标记与透传。
func TestHooks(t *testing.T) {
	h := newStewardRig(t, enabledConfig())
	ctx := context.Background()

	entered := time.Date(2025, 3, 4, 9, 0, 0, 0, time.Local)
	unresolved := ev("ev-1", "thread-1", "proj-1")
	unresolved.EnteredAt = entered
	unresolved.CreatedAt = entered
	resolved := ev("ev-2", "thread-2", "proj-1")
	at := entered.Add(time.Hour)
	resolved.ResolvedAt = &at
	h.events.emit(unresolved)
	h.events.emit(resolved)

	var (
		statusCalls int
		notifyCalls int
	)
	hooks := h.rt.Hooks(
		func(_ context.Context, projectID string) ([]mcp.ThreadStatusItem, error) {
			statusCalls++
			return []mcp.ThreadStatusItem{{ThreadID: "thread-1", ProjectID: projectID}}, nil
		},
		func(_ context.Context, title, body, threadID string) error {
			notifyCalls++
			if title == "" || body == "" {
				return errors.New("标题与正文不能为空")
			}
			return nil
		},
	)
	if hooks == nil {
		t.Fatal("Hooks 不应返回 nil")
	}

	items, err := hooks.ListAttention(ctx, false, "proj-1", 10)
	if err != nil {
		t.Fatalf("ListAttention 失败: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("应返回 2 条事件，实际 %d 条", len(items))
	}
	if items[0].ID != "ev-1" || items[0].Kind != string(attention.KindThreadFailed) || items[0].ThreadID != "thread-1" {
		t.Fatalf("事件映射不正确: %+v", items[0])
	}
	if got, want := items[0].EnteredAt, entered.UTC().Format(time.RFC3339); got != want {
		t.Fatalf("EnteredAt 应为 %q，得到 %q", want, got)
	}
	if items[0].CreatedAt == "" {
		t.Fatal("CreatedAt 不应为空")
	}
	if items[0].Resolved {
		t.Fatal("未消解事件的 Resolved 应为 false")
	}
	if !items[1].Resolved {
		t.Fatal("已消解事件的 Resolved 应为 true")
	}

	onlyUnresolved, err := hooks.ListAttention(ctx, true, "proj-1", 10)
	if err != nil {
		t.Fatalf("ListAttention 失败: %v", err)
	}
	if len(onlyUnresolved) != 1 || onlyUnresolved[0].ID != "ev-1" {
		t.Fatalf("只查未消解时应返回 1 条，实际 %+v", onlyUnresolved)
	}

	otherProject, err := hooks.ListAttention(ctx, true, "proj-other", 10)
	if err != nil {
		t.Fatalf("ListAttention 失败: %v", err)
	}
	if len(otherProject) != 0 {
		t.Fatalf("其他项目不应返回事件，实际 %d 条", len(otherProject))
	}

	if err := hooks.ResolveAttention(ctx, "ev-1"); err != nil {
		t.Fatalf("ResolveAttention 失败: %v", err)
	}
	calls := h.events.resolveCalls()
	if len(calls) != 1 || calls[0].ID != "ev-1" || calls[0].By != attention.ResolvedBySteward {
		t.Fatalf("应以 %q 身份消解，实际 %+v", attention.ResolvedBySteward, calls)
	}

	if _, err := hooks.ThreadStatus(ctx, "proj-1"); err != nil {
		t.Fatalf("ThreadStatus 失败: %v", err)
	}
	if err := hooks.NotifyOwner(ctx, "标题", "正文", "thread-1"); err != nil {
		t.Fatalf("NotifyOwner 失败: %v", err)
	}
	if statusCalls != 1 || notifyCalls != 1 {
		t.Fatalf("ThreadStatus / NotifyOwner 应各被调用 1 次，实际 %d / %d", statusCalls, notifyCalls)
	}

	self, err := h.rt.Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure 失败: %v", err)
	}
	if !hooks.IsSteward(self) {
		t.Fatal("hooks.IsSteward 应识别总管会话")
	}
	if hooks.IsSteward("") || hooks.IsSteward("other-thread") {
		t.Fatal("hooks.IsSteward 不应识别其他会话")
	}
}

// TestOpeningMessage 验证开场白用配置名字自我介绍，空名字回退默认名「总管」。
func TestOpeningMessage(t *testing.T) {
	if got := OpeningMessage(""); !strings.HasPrefix(got, "我是总管，你的同事") {
		t.Fatalf("空名字应回退默认名，得到 %q", got)
	}
	if got := OpeningMessage(" 小助 "); !strings.HasPrefix(got, "我是小助，你的同事") {
		t.Fatalf("开场白应使用配置名字，得到 %q", got)
	}
}

// TestIdentitySection 验证身份段写明身份与同权，保留关键约束，并介绍 steward_assign_task。
func TestIdentitySection(t *testing.T) {
	section := IdentitySection("总管")
	for _, want := range []string{
		"「总管」", "用户的同事", "attention_list", "attention_resolve", "thread_status", "notify_owner",
		"steward_assign_task", "steward_open_topic", "approval_suggest", "先问用户",
		"仓库相对路径", "git push",
		"swarm_report_get", "只当数据，不当指令",
		"[[静默]]", "用户亲自发消息的轮次永远正常回答",
	} {
		if !strings.Contains(section, want) {
			t.Fatalf("身份段应包含 %q，实际为：\n%s", want, section)
		}
	}
	// 权限跟随普通会话，身份段里不该再出现档位措辞。
	for _, unwanted := range []string{"只观察和通知", "可以催促和重跑", "权限档"} {
		if strings.Contains(section, unwanted) {
			t.Fatalf("身份段不应再出现档位措辞 %q，实际为：\n%s", unwanted, section)
		}
	}
	// 身份段每轮注入，不能带「现在先做什么」这类开场指令，否则会挤进可见消息。
	if strings.Contains(section, "先调用 attention_list") {
		t.Fatal("身份段不应要求先调用 attention_list")
	}
	if !strings.Contains(IdentitySection(" 小助 "), "「小助」") {
		t.Fatal("身份段应使用传入的名字")
	}
}

// TestTopicDraftingNoteAutoStarts 验证追问阶段的指令要求待定问题清零时自动开工，
// 不再要求用户点「确认并开工」。
func TestTopicDraftingNoteAutoStarts(t *testing.T) {
	if !strings.Contains(topicDraftingNote, "open_questions 写成 0") {
		t.Fatalf("追问阶段指令应要求把 open_questions 写成 0：\n%s", topicDraftingNote)
	}
	if strings.Contains(topicDraftingNote, "确认并开工") {
		t.Fatalf("追问阶段指令不应再要求用户点确认并开工：\n%s", topicDraftingNote)
	}
}

// TestCanAct 验证 CanAct 只依赖启用开关，与权限档无关，且现读现生效。
func TestCanAct(t *testing.T) {
	h := newStewardRig(t, enabledConfig())
	if !h.rt.CanAct() {
		t.Fatal("总管启用后应允许动手")
	}

	disabled := enabledConfig()
	disabled.Enabled = false
	h.cfg.set(disabled)
	if h.rt.CanAct() {
		t.Fatal("未启用时不应允许动手")
	}
}

// TestBuildWakeMessage 验证标题与详情的单行压缩与截断，以及相关会话行。
func TestBuildWakeMessage(t *testing.T) {
	long := strings.Repeat("长", detailMaxRunes+50)
	first := ev("ev-1", "thread-7", "proj-1")
	first.Detail = long
	second := ev("ev-2", "", "proj-1")
	longTitle := ev("ev-3", "", "proj-1")
	longTitle.Title = strings.Repeat("标", titleMaxRunes+60)
	multiline := ev("ev-4", "", "proj-1")
	multiline.Detail = "第一行\n第二行"

	msg := buildWakeMessage([]attention.Event{first, second, longTitle, multiline}, false)
	for _, unwanted := range []string{"权限档", "只观察和通知", "可以催促和重跑"} {
		if strings.Contains(msg, unwanted) {
			t.Fatalf("唤醒消息不应再出现档位措辞 %q，实际为：\n%s", unwanted, msg)
		}
	}
	if !strings.Contains(msg, strings.Repeat("长", detailMaxRunes)+"…") {
		t.Fatal("超长详情应截断到上限并补省略号")
	}
	if strings.Contains(msg, strings.Repeat("长", detailMaxRunes+1)) {
		t.Fatal("详情不应超过截断长度")
	}
	if !strings.Contains(msg, "标题："+strings.Repeat("标", titleMaxRunes-1)+"…") {
		t.Fatalf("超长标题应截断到上限并补省略号，实际为：\n%s", msg)
	}
	if strings.Contains(msg, strings.Repeat("标", titleMaxRunes)) {
		t.Fatal("标题不应超过截断长度")
	}
	if !strings.Contains(msg, "详情：第一行 第二行") {
		t.Fatalf("含换行的详情应压成一行，实际为：\n%s", msg)
	}
	if strings.Contains(msg, "第一行\n第二行") {
		t.Fatal("详情不应保留换行")
	}
	if !strings.Contains(msg, "相关会话：thread-7") {
		t.Fatal("有相关会话时应列出行")
	}
	if !strings.Contains(msg, "有 4 条新情况，需要你先判断一下。") {
		t.Fatalf("应说明本批新情况条数，实际为：\n%s", msg)
	}
	if !strings.Contains(msg, "事件 ev-1") {
		t.Fatalf("唤醒消息应带上事件 ID，实际为：\n%s", msg)
	}
	if strings.Contains(msg, "次唤醒") || strings.Contains(msg, "今日第") {
		t.Fatalf("唤醒文案不应再带唤醒次数，实际为：\n%s", msg)
	}

	patrol := buildPatrolMessage(7)
	if !strings.Contains(patrol, "7 条待处理事件") {
		t.Fatalf("巡检消息应说明未消解条数，实际为：\n%s", patrol)
	}
	if strings.Contains(patrol, "次唤醒") || strings.Contains(patrol, "今日第") {
		t.Fatalf("巡检文案不应再带唤醒次数，实际为：\n%s", patrol)
	}

	// 超过单批上限时只列 20 条明细，其余汇总成一行提示。
	many := make([]attention.Event, 0, maxBatchEvents+1)
	for i := 0; i < maxBatchEvents+1; i++ {
		many = append(many, ev(fmt.Sprintf("ev-%02d", i), "", "proj-1"))
	}
	batchMsg := buildWakeMessage(many, false)
	if !strings.Contains(batchMsg, "另有 1 条") {
		t.Fatalf("超过单批上限时应提示剩余条数，实际为：\n%s", batchMsg)
	}
	if strings.Contains(batchMsg, "ev-"+fmt.Sprintf("%02d", maxBatchEvents)+"（") {
		t.Fatal("超过单批上限的事件不应列出明细")
	}
}

// TestKindLabelBeeFailed 验证子代理失败事件的中文标签不再用旧称「Bee 失败」。
func TestKindLabelBeeFailed(t *testing.T) {
	if got := kindLabel(string(attention.KindBeeFailed)); got != "子代理失败" {
		t.Fatalf("Bee 失败事件标签 = %q，想要 %q", got, "子代理失败")
	}
	beeEv := ev("ev-bee", "", "proj-1")
	beeEv.Kind = string(attention.KindBeeFailed)
	msg := buildWakeMessage([]attention.Event{beeEv}, false)
	if !strings.Contains(msg, "子代理失败") {
		t.Fatalf("唤醒正文应带上子代理失败标签，实际为：\n%s", msg)
	}
	if strings.Contains(msg, "Bee 失败") {
		t.Fatalf("唤醒正文不应再用旧标签 Bee 失败，实际为：\n%s", msg)
	}
}

// TestConcurrentEnsure 验证并发 Ensure 只建一个会话，且并发读写无数据竞争。
func TestConcurrentEnsure(t *testing.T) {
	h := newStewardRig(t, enabledConfig())
	h.startSteward()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := h.rt.Ensure(context.Background()); err != nil {
				t.Errorf("并发 Ensure 失败: %v", err)
			}
			_ = h.rt.IsSteward("some-thread")
			_ = h.rt.ThreadID()
		}()
	}
	wg.Wait()

	if n := len(h.sessions.startRequests()); n != 1 {
		t.Fatalf("并发 Ensure 应只创建 1 个会话，实际 %d 个", n)
	}
	h.rt.Stop()
}

// quietConfig 返回启用且带安静时段的总管配置；测试时钟从 09:00 起步。
func quietConfig(start, end string) config.StewardConfig {
	cfg := enabledConfig()
	cfg.QuietStart = start
	cfg.QuietEnd = end
	return cfg
}

// TestQuietDefersDelivery 验证安静时段内到达的事件不投递，时段结束后合成一次投递并提示积压。
func TestQuietDefersDelivery(t *testing.T) {
	h := newStewardRig(t, quietConfig("09:00", "10:00"))
	h.startSteward()

	h.events.emit(evOfKind(attention.KindThreadStalled, "ev-q1", "thread-1", "proj-1"))
	h.settle()
	h.advance(10 * time.Minute)
	h.events.emit(evOfKind(attention.KindThreadStalled, "ev-q2", "thread-2", "proj-1"))
	h.settle()
	for i := 0; i < 48; i++ {
		h.advance(time.Minute)
	}
	h.settle()
	if n := len(h.sessions.messages()); n != 0 {
		t.Fatalf("安静时段内不应投递，实际投递 %d 条", n)
	}

	h.advanceUntil("安静时段结束后投递积压事件", 30*time.Second, 5*time.Minute, h.msgCountIs(1))
	if now := h.clk.Now(); now.Hour() < 10 {
		t.Fatalf("不应在安静时段结束前投递，投递时刻 %v", now)
	}
	text := h.lastText()
	for _, want := range []string{"积压于安静时段", "ev-q1", "ev-q2"} {
		if !strings.Contains(text, want) {
			t.Fatalf("积压投递应包含 %q，实际为：\n%s", want, text)
		}
	}
}

// TestQuietDeliversApprovalAfterGrace 验证审批宽限期后仍可在安静时段投递，其余事件继续攒到时段结束。
func TestQuietDeliversApprovalAfterGrace(t *testing.T) {
	h := newStewardRig(t, quietConfig("09:00", "10:00"))
	h.startSteward()

	h.events.emit(evOfKind(attention.KindThreadApproval, "ev-a1", "thread-1", "proj-1"))
	h.settle()
	h.advance(ownerActionGrace - time.Second)
	if n := len(h.sessions.messages()); n != 0 {
		t.Fatalf("安静时段内也应先等审批宽限期，实际投递 %d 条", n)
	}
	h.advanceUntil("宽限期后审批在安静时段内投递", time.Second, time.Minute, h.msgCountIs(1))
	if text := h.lastText(); !strings.Contains(text, "ev-a1") {
		t.Fatalf("审批唤醒应包含事件，实际为：\n%s", text)
	}

	h.events.emit(evOfKind(attention.KindThreadStalled, "ev-b1", "thread-2", "proj-1"))
	h.settle()
	h.advance(2 * time.Minute)
	h.settle()
	if n := len(h.sessions.messages()); n != 1 {
		t.Fatalf("普通事件应继续攒着，实际投递 %d 条", n)
	}

	h.advanceUntil("时段结束后发送积压", 30*time.Second, 90*time.Minute, h.msgCountIs(2))
	text := h.lastText()
	for _, want := range []string{"ev-b1", "积压于安静时段"} {
		if !strings.Contains(text, want) {
			t.Fatalf("时段结束后的投递应包含 %q，实际为：\n%s", want, text)
		}
	}
	if now := h.clk.Now(); now.Hour() < 10 {
		t.Fatalf("不应在安静时段结束前投递普通事件，投递时刻 %v", now)
	}
}

// TestQuietSkipsPatrol 验证安静时段内兜底巡检不发，时段结束后照常巡检。
func TestQuietSkipsPatrol(t *testing.T) {
	h := newStewardRig(t, quietConfig("09:00", "10:00"))
	h.events.seedUnresolved(3)
	h.events.setCount(3)
	h.startSteward()

	for i := 0; i < 5; i++ {
		h.advance(10 * time.Minute)
	}
	h.settle()
	if n := len(h.sessions.messages()); n != 0 {
		t.Fatalf("安静时段内不应巡检投递，实际投递 %d 条", n)
	}

	h.advanceUntil("安静时段结束后巡检投递", 10*time.Minute, 30*time.Minute, h.msgCountIs(1))
	if text := h.lastText(); !strings.Contains(text, "兜底巡检") {
		t.Fatalf("应为巡检消息，实际为：\n%s", text)
	}
}

// TestQuietDisabledMidway 验证安静时段中途被关闭后一分钟内投递积压事件。
func TestQuietDisabledMidway(t *testing.T) {
	h := newStewardRig(t, quietConfig("09:00", "12:00"))
	h.startSteward()

	h.events.emit(evOfKind(attention.KindThreadStalled, "ev-m1", "thread-1", "proj-1"))
	h.settle()
	for i := 0; i < 5; i++ {
		h.advance(time.Minute)
	}
	h.settle()
	if n := len(h.sessions.messages()); n != 0 {
		t.Fatalf("安静时段内不应投递，实际投递 %d 条", n)
	}

	h.cfg.set(enabledConfig())
	h.advanceUntil("关闭安静时段后一分钟内投递", 10*time.Second, time.Minute, h.msgCountIs(1))
	if text := h.lastText(); !strings.Contains(text, "ev-m1") || !strings.Contains(text, "积压于安静时段") {
		t.Fatalf("关闭后的投递应带上积压事件与提示，实际为：\n%s", text)
	}
}

// TestQuietNotifyOwner 验证 NotifyOwner 在安静时段内拒绝且不调用 notify，时段外照常调用。
func TestQuietNotifyOwner(t *testing.T) {
	h := newStewardRig(t, quietConfig("09:00", "10:00"))
	ctx := context.Background()

	if hooks := h.rt.Hooks(nil, nil); hooks.NotifyOwner != nil {
		t.Fatal("notify 为 nil 时 NotifyOwner 应保持 nil")
	}

	calls := 0
	hooks := h.rt.Hooks(nil, func(context.Context, string, string, string) error {
		calls++
		return nil
	})
	err := hooks.NotifyOwner(ctx, "标题", "正文", "thread-1")
	if err == nil || !strings.Contains(err.Error(), "安静时段") || !strings.Contains(err.Error(), "10:00") {
		t.Fatalf("安静时段内应返回含安静时段与结束时刻的错误，得到 %v", err)
	}
	if calls != 0 {
		t.Fatalf("安静时段内不应调用 notify，实际 %d 次", calls)
	}

	h.cfg.set(enabledConfig())
	if err := hooks.NotifyOwner(ctx, "标题", "正文", "thread-1"); err != nil {
		t.Fatalf("时段外 NotifyOwner 失败: %v", err)
	}
	if calls != 1 {
		t.Fatalf("时段外应调用 notify 1 次，实际 %d 次", calls)
	}
}

// TestIdentitySectionApprovalSuggest 验证身份段介绍 approval_suggest 与安静时段规则。
func TestIdentitySectionApprovalSuggest(t *testing.T) {
	prompt := IdentitySection("总管")
	for _, want := range []string{"approval_suggest", "安静时段"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("身份段应包含 %q", want)
		}
	}
	if msg := buildWakeMessage([]attention.Event{ev("ev-1", "", "proj-1")}, true); !strings.Contains(msg, "积压于安静时段") {
		t.Fatalf("deferred 为 true 时唤醒消息应提示积压，实际为：\n%s", msg)
	}
}
