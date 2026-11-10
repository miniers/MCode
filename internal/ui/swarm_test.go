package mcodeui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
	"mcode/internal/swarm"
)

type delayedSwarmClient struct {
	*FakeClient
	started chan chan SwarmState
}

func (f *delayedSwarmClient) Swarm(ctx context.Context, threadID string) (SwarmState, error) {
	reply := make(chan SwarmState, 1)
	select {
	case f.started <- reply:
	case <-ctx.Done():
		return SwarmState{}, ctx.Err()
	}
	select {
	case st := <-reply:
		return st, nil
	case <-ctx.Done():
		return SwarmState{}, ctx.Err()
	}
}

// TestSwarmLoadCoalescesAndLatestWins 验证同一会话在途时不再并发发起 RPC：
// 第二次调用只置 loadPending，第一次返回后补发一次，补发的结果最终落地。
func TestSwarmLoadCoalescesAndLatestWins(t *testing.T) {
	fake := &delayedSwarmClient{FakeClient: NewFakeClient(), started: make(chan chan SwarmState)}
	s := newChatState(fake)
	s.selectedThreadID = "t1"
	s.loadSwarm("t1")

	first := receiveSwarmCall(t, fake.started, "第一次 loadSwarm")

	// 在途时再次调用：不发 RPC，只把 loadPending 置为 true。
	s.loadSwarm("t1")
	select {
	case <-fake.started:
		t.Fatal("在途加载期间不应发起第二次 RPC")
	case <-time.After(100 * time.Millisecond):
	}
	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	loading, pending := hp.loading, hp.loadPending
	swarmPanelMu.Unlock()
	if !loading || !pending {
		t.Fatalf("在途调用应保持 loading 并置 loadPending，got loading=%v pending=%v", loading, pending)
	}

	// 第一次返回后自动补发一次 RPC。
	sendSwarmReply(t, first, SwarmState{LeadThreadID: "t1", Available: true, Enabled: false})
	second := receiveSwarmCall(t, fake.started, "第一次返回后的补发")

	// 补发的结果最终落地，旧的响应不得覆盖它。
	sendSwarmReply(t, second, SwarmState{LeadThreadID: "t1", Available: true, Enabled: true})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		swarmPanelMu.Lock()
		done := !hp.loading && hp.state.Enabled
		swarmPanelMu.Unlock()
		if done {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("补发后的 Swarm 状态没有落地")
}

// receiveSwarmCall 等待测试替身收到一次 Swarm RPC，超时即失败，避免测试无限挂起。
func receiveSwarmCall(t *testing.T, started <-chan chan SwarmState, what string) chan SwarmState {
	t.Helper()
	select {
	case reply := <-started:
		return reply
	case <-time.After(2 * time.Second):
		t.Fatalf("%s 没有发起 RPC", what)
		return nil
	}
}

// sendSwarmReply 把响应交回 Swarm RPC 的调用方，超时即失败。
func sendSwarmReply(t *testing.T, reply chan<- SwarmState, st SwarmState) {
	t.Helper()
	select {
	case reply <- st:
	case <-time.After(2 * time.Second):
		t.Fatal("Swarm RPC 没有接收响应")
	}
}

func TestSwarmTargetFollowsSelectedThread(t *testing.T) {
	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t2"
	hp := getSwarmPanelState(s)
	hp.loadedFor = "t1"
	hp.state.LeadThreadID = "t1"
	if got := s.swarmTargetLeadID(); got != "t2" {
		t.Fatalf("target = %q, want t2", got)
	}
}

func TestSwarmHeaderUpdatesWhileClosed(t *testing.T) {
	fake := NewFakeClient()
	state := newChatState(fake)
	tt := ui.NewTester(func(c *ui.Context) {
		state.init(c)
		renderSwarmHeaderButton(c, state)
	}, 1000, 700)
	waitFor(t, tt, func() bool {
		hp := getSwarmPanelState(state)
		swarmPanelMu.Lock()
		defer swarmPanelMu.Unlock()
		return hp.loadedFor == "t1" && !hp.loading
	})
	fake.SetSwarmState("t1", SwarmState{
		LeadThreadID: "t1", Available: true, Enabled: true,
		Bees: []SwarmBee{{ThreadID: "h1", Status: "running"}, {ThreadID: "h2", Status: "running"}},
	})
	fake.TriggerSwarmChange("t1")
	waitFor(t, tt, func() bool { return tt.HasText("2") })
	if getSwarmPanelState(state).IsOpen() {
		t.Fatal("header update opened the panel")
	}
}

func TestSwarmPanelStates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		state   SwarmState
		err     string
		loading bool
		want    string
	}{
		{name: "loading", loading: true, want: "正在加载…"},
		{name: "error", err: "读取失败", want: "读取失败"},
		{name: "unavailable", want: "编排不可用"},
		{name: "head", state: SwarmState{Available: true, IsBee: true, LeadThreadID: "lead"}, want: "这是子代理会话"},
		{name: "empty", state: SwarmState{Available: true}, want: "暂无子代理"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newChatState(NewFakeClient())
			s.selectedThreadID = "t1"
			hp := getSwarmPanelState(s)
			hp.open, hp.loadedFor = true, "t1"
			hp.state, hp.err, hp.loading = tc.state, tc.err, tc.loading
			tt := ui.NewTester(func(c *ui.Context) { renderSwarmPanel(c, s) }, 800, 600)
			if !tt.HasText(tc.want) {
				t.Fatalf("missing %q in %v", tc.want, tt.Texts())
			}
		})
	}
}

type delayedSwarmSetClient struct {
	*FakeClient
	started chan struct{}
	release chan struct{}
}

func (f *delayedSwarmSetClient) SetSwarmEnabled(ctx context.Context, id string, enabled bool) (SwarmState, error) {
	// 用带缓冲的发送代替 close，重复调用不会 panic；等待放行时也监听 ctx，避免测试替身永久阻塞。
	select {
	case f.started <- struct{}{}:
	default:
	}
	select {
	case <-f.release:
	case <-ctx.Done():
		return SwarmState{}, ctx.Err()
	}
	return f.FakeClient.SetSwarmEnabled(ctx, id, enabled)
}

func TestSwarmActionDoesNotRestorePreviousThread(t *testing.T) {
	fake := &delayedSwarmSetClient{
		FakeClient: NewFakeClient(), started: make(chan struct{}, 1), release: make(chan struct{}),
	}
	s := newChatState(fake)
	s.selectedThreadID = "t1"
	s.setSwarmEnabled(true)
	select {
	case <-fake.started:
	case <-time.After(2 * time.Second):
		t.Fatal("SetSwarmEnabled 没有被调用")
	}
	s.mu.Lock()
	s.selectedThreadID = "t2"
	s.mu.Unlock()
	close(fake.release)

	hp := getSwarmPanelState(s)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		swarmPanelMu.Lock()
		done := hp.loadedFor == "t2" && !hp.loading && hp.state.LeadThreadID == "t2"
		swarmPanelMu.Unlock()
		if done {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("completed action did not refresh the currently selected thread")
}

// TestSwarmHelpers 测试 Swarm 纯函数辅助工具：状态标签、预览截断、进度文本、冲突判断。
func TestSwarmHelpers(t *testing.T) {
	// 1. 状态标签
	if got := swarmStatusLabel("running"); got != "运行中" {
		t.Errorf("expected 运行中, got %s", got)
	}
	if got := swarmStatusLabel("completed"); got != "已完成" {
		t.Errorf("expected 已完成, got %s", got)
	}
	if got := swarmStatusLabel("failed"); got != "失败" {
		t.Errorf("expected 失败, got %s", got)
	}
	if got := swarmStatusLabel("waiting_review"); got != "待审查" {
		t.Errorf("expected 待审查, got %s", got)
	}
	if got := swarmStatusLabel("stopped"); got != "空闲" {
		t.Errorf("expected 空闲, got %s", got)
	}
	if got := swarmStatusLabel("custom_status"); got != "空闲" {
		t.Errorf("expected 空闲, got %s", got)
	}

	// 2. 进度文本
	if got := swarmProgressText(SwarmState{}); got != "" {
		t.Errorf("expected empty progress for 0 bees, got %q", got)
	}
	st := SwarmState{
		Bees: []SwarmBee{
			{Status: "running"},
			{Status: "completed"},
			{Status: "failed"},
		},
	}
	if got := swarmProgressText(st); got != "2/3 已结束，1 个失败" {
		t.Errorf("expected 2/3 已结束，1 个失败, got %s", got)
	}

	// 3. 冲突判断
	if swarmHasConflicts(SwarmBee{}) {
		t.Errorf("expected false for empty conflict files")
	}
	if !swarmHasConflicts(SwarmBee{ConflictFiles: []string{"a.go"}}) {
		t.Errorf("expected true for non-empty conflict files")
	}

	// 4. 预览选择与截断
	// running 且 Activity 非空 -> 优先 Activity
	hRunning := SwarmBee{
		Status:   "running",
		Activity: "执行测试中",
		Summary:  "任务摘要",
		Task:     "原始任务",
	}
	if got := swarmBeePreview(hRunning); got != "执行测试中" {
		t.Errorf("expected Activity when running, got %s", got)
	}

	// 非 running 状态且 Summary 非空 -> 优先 Summary
	hFinished := SwarmBee{
		Status:   "completed",
		Activity: "历史动态",
		Summary:  "完成摘要",
		Task:     "原始任务",
	}
	if got := swarmBeePreview(hFinished); got != "完成摘要" {
		t.Errorf("expected Summary for completed bee, got %s", got)
	}

	// Summary 与 Activity 均为空 -> 使用 Task
	hTaskOnly := SwarmBee{
		Status: "stopped",
		Task:   "纯任务描述",
	}
	if got := swarmBeePreview(hTaskOnly); got != "纯任务描述" {
		t.Errorf("expected Task when others empty, got %s", got)
	}

	// 超过 160 runes 截断并带 …
	longText := strings.Repeat("字", 200)
	hLong := SwarmBee{
		Task: longText,
	}
	preview := swarmBeePreview(hLong)
	runes := []rune(preview)
	if len(runes) != 161 || runes[160] != '…' {
		t.Errorf("expected 161 runes with trailing ellipsis, got length %d", len(runes))
	}
}

// TestSwarmState_LoadSwarm 测试 SetSwarmState 后 loadSwarm 异步获取并存储状态。
func TestSwarmState_LoadSwarm(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)
	s.selectedThreadID = "t1"

	expected := SwarmState{
		LeadThreadID: "t1",
		Available:    true,
		Enabled:      true,
		Bees: []SwarmBee{
			{ThreadID: "bee-1", Index: 1, Name: "子任务1", Status: "running"},
		},
	}
	fake.SetSwarmState("t1", expected)

	s.loadSwarm("t1")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		hp := getSwarmPanelState(s)
		swarmPanelMu.Lock()
		loading := hp.loading
		st := hp.state
		swarmPanelMu.Unlock()
		if !loading && st.Enabled && len(st.Bees) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	defer swarmPanelMu.Unlock()
	if hp.loading || !hp.state.Enabled || len(hp.state.Bees) != 1 {
		t.Fatalf("loadSwarm failed to load expected state: %+v", hp.state)
	}
}

// TestSwarmState_StopSwarmBee 测试 stopSwarmBee 记录被停止的 bee ID。
func TestSwarmState_StopSwarmBee(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)
	s.selectedThreadID = "t1"

	fake.SetSwarmState("t1", SwarmState{
		LeadThreadID: "t1",
		Available:    true,
		Bees: []SwarmBee{
			{ThreadID: "bee-stop-target", Status: "running"},
		},
	})

	s.stopSwarmBee("bee-stop-target")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		bees := fake.StoppedBees()
		if len(bees) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	bees := fake.StoppedBees()
	if len(bees) == 0 || bees[0] != "bee-stop-target" {
		t.Fatalf("expected bee-stop-target to be recorded in stoppedBees, got: %v", bees)
	}
}

// TestSwarmState_TriggerSwarmChange 测试通过 WatchSwarm 订阅在 TriggerSwarmChange 时触发重新加载。
func TestSwarmState_TriggerSwarmChange(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)
	s.selectedThreadID = "t1"

	// 展开面板以注册 WatchSwarm
	s.toggleSwarmPanel()

	// 初始状态加载完成后，更新 fake 状态并触发通知
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		hp := getSwarmPanelState(s)
		swarmPanelMu.Lock()
		loading := hp.loading
		swarmPanelMu.Unlock()
		if !loading {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	updated := SwarmState{
		LeadThreadID: "t1",
		Available:    true,
		Enabled:      true,
		Bees: []SwarmBee{
			{ThreadID: "bee-updated", Index: 1, Name: "新派发任务", Status: "running"},
		},
	}
	fake.SetSwarmState("t1", updated)
	fake.TriggerSwarmChange("t1")

	for time.Now().Before(deadline) {
		hp := getSwarmPanelState(s)
		swarmPanelMu.Lock()
		st := hp.state
		loading := hp.loading
		swarmPanelMu.Unlock()
		if !loading && len(st.Bees) == 1 && st.Bees[0].ThreadID == "bee-updated" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	defer swarmPanelMu.Unlock()
	if len(hp.state.Bees) != 1 || hp.state.Bees[0].ThreadID != "bee-updated" {
		t.Fatalf("expected state updated via TriggerSwarmChange, got: %+v", hp.state)
	}
}

// TestSwarm_RenderNoPanic 渲染测试：带有一个运行中 bee 和一个冲突 bee 的面板正常渲染不 panic。
func TestSwarm_RenderNoPanic(t *testing.T) {
	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t1"
	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.open, hp.loadedFor = true, "t1"
	hp.setStateLocked(SwarmState{
		LeadThreadID: "t1",
		Available:    true,
		Enabled:      true,
		Bees: []SwarmBee{
			{
				ThreadID:  "bee-r",
				Index:     1,
				Name:      "运行Bee",
				Task:      "进行多模块协作与分析",
				Activity:  "正在执行单元测试",
				Status:    "running",
				Tokens:    12500,
				ToolCalls: 8,
				Provider:  "anthropic",
				Model:     "claude-3-5-sonnet",
			},
			{
				ThreadID:      "bee-c",
				Index:         2,
				Name:          "冲突Bee",
				Task:          "修改系统核心配置文件",
				Summary:       "合并产生分支冲突",
				Status:        "failed",
				Tokens:        800,
				ToolCalls:     1,
				ConflictFiles: []string{"config.go", "app.go"},
				MergeError:    "自动合并失败，请手动解决冲突",
			},
		},
	})
	swarmPanelMu.Unlock()

	// 直接展开汇总面板渲染（编排按钮现在只切换浮窗，不再展开面板）。
	tt := ui.NewTester(func(c *ui.Context) { renderSwarmPanel(c, s) }, 1000, 700)

	// 断言渲染出两个 Bee 的关键文本且无 panic
	waitFor(t, tt, func() bool {
		return tt.HasText("#1 运行Bee") &&
			tt.HasText("冲突文件: config.go, app.go") &&
			tt.HasText("合并错误: 自动合并失败，请手动解决冲突")
	})
}

// TestParseSwarmReport 验证对 BuildReport 格式汇报文本的完整解析能力（包括标题、状态、多行总结等）。
func TestParseSwarmReport(t *testing.T) {
	text := `[编排] 子代理汇报：Ives, Dune 已结束工作。

## Ives：修复时间线解析器 (已完成)
- 合并结果：已合并
- 汇报总结：修复了时间线中的空行解析逻辑。
第二行说明详情。
第三行总结内容。

## Dune：运行自动化回归测试 (失败)
- 合并结果：合并失败（conflict in main.go）
- 汇报总结：自动化测试用例运行超时。`

	title, entries := parseSwarmReport(text)
	expectedTitle := "子代理汇报：Ives, Dune 已结束工作。"
	if title != expectedTitle {
		t.Errorf("title = %q, want %q", title, expectedTitle)
	}

	if len(entries) != 2 {
		t.Fatalf("entries count = %d, want 2", len(entries))
	}

	// 验证第一个 entry（带多行 Summary）
	e1 := entries[0]
	if e1.Name != "Ives" {
		t.Errorf("e1.Name = %q, want %q", e1.Name, "Ives")
	}
	if e1.Task != "修复时间线解析器" {
		t.Errorf("e1.Task = %q, want %q", e1.Task, "修复时间线解析器")
	}
	if e1.Status != "已完成" {
		t.Errorf("e1.Status = %q, want %q", e1.Status, "已完成")
	}
	if e1.MergeResult != "已合并" {
		t.Errorf("e1.MergeResult = %q, want %q", e1.MergeResult, "已合并")
	}
	expectedSummary := "修复了时间线中的空行解析逻辑。\n第二行说明详情。\n第三行总结内容。"
	if e1.Summary != expectedSummary {
		t.Errorf("e1.Summary = %q, want %q", e1.Summary, expectedSummary)
	}

	// 验证第二个 entry
	e2 := entries[1]
	if e2.Name != "Dune" {
		t.Errorf("e2.Name = %q, want %q", e2.Name, "Dune")
	}
	if e2.Task != "运行自动化回归测试" {
		t.Errorf("e2.Task = %q, want %q", e2.Task, "运行自动化回归测试")
	}
	if e2.Status != "失败" {
		t.Errorf("e2.Status = %q, want %q", e2.Status, "失败")
	}
	if e2.MergeResult != "合并失败（conflict in main.go）" {
		t.Errorf("e2.MergeResult = %q, want %q", e2.MergeResult, "合并失败（conflict in main.go）")
	}
	if e2.Summary != "自动化测试用例运行超时。" {
		t.Errorf("e2.Summary = %q, want %q", e2.Summary, "自动化测试用例运行超时。")
	}
}

func TestSwarmReportContract(t *testing.T) {
	if swarmReportPrefix != swarm.ReportPrefix {
		t.Fatal("汇报生成端与界面端的前缀不一致")
	}
	summary := "完成修复。\n\n## 验证步骤\n- 合并结果：这是正文，不是元数据\n- 汇报总结：保留原文\n```markdown\n## Dune：示例 (失败)\n- 合并结果：未合并\n```\n最后一行。"
	text := swarm.BuildReport([]swarm.Bee{
		{Name: "Ives", Task: "修复解析器", Status: swarm.StatusCompleted, Summary: summary,
			ChangedFiles: []string{"parser.go", "含空格 文件.go"}, ConflictFiles: []string{"parser.go"}},
		{Name: "Dune", Task: "运行测试", Status: swarm.StatusFailed, Summary: "测试失败"},
	})
	_, entries := parseSwarmReport(text)
	if len(entries) != 2 {
		t.Fatalf("汇报项数量 = %d，预期 2: %+v", len(entries), entries)
	}
	if entries[0].Summary != summary {
		t.Fatalf("汇报正文被改写: %q", entries[0].Summary)
	}
	if !entries[0].filesRecorded || strings.Join(entries[0].ChangedFiles, "|") != "parser.go|含空格 文件.go" ||
		strings.Join(entries[0].ConflictFiles, "|") != "parser.go" {
		t.Fatalf("汇报文件快照丢失: %+v", entries[0])
	}
}

func TestSwarmReportFilesSurviveBeeCleanup(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)
	s.selectedThreadID = "t1"
	msg := Message{ID: "snapshot", Role: "user", Text: swarm.BuildReport([]swarm.Bee{
		{Name: "Ives", Task: "修复解析器", Status: swarm.StatusCompleted, Summary: "修复完成",
			ChangedFiles: []string{"parser.go"}, ConflictFiles: []string{"conflict.go"}},
	})}
	tt := ui.NewTester(func(c *ui.Context) {
		s.init(c)
		renderSwarmReportRow(c, s, msg)
	}, 800, 700)
	if err := tt.Click("子代理汇报详情"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("parser.go") || !tt.HasText("conflict.go") || !tt.HasText("修复完成") {
		t.Fatalf("清理 bee 后未显示历史文件快照: %v", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	tt.Frame()
	if tt.HasText("修复完成") {
		t.Fatal("Escape 未关闭详情浮窗")
	}
}

// 汇报兜底卡片的正文要去掉「[编排] 」前缀，时间线上不显示内部标记。
func TestSwarmReportFallbackBodyStripsNoticePrefix(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)
	s.selectedThreadID = "t1"
	msg := Message{ID: "notice-body", Role: "user", Text: "[编排] 子代理汇报：Ives 已结束工作。\n补充一句：正文细节。"}
	tt := ui.NewTester(func(c *ui.Context) {
		s.init(c)
		renderSwarmReportRow(c, s, msg)
	}, 800, 700)
	if err := tt.Click("子代理汇报详情"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("补充一句：正文细节。") {
		t.Fatalf("兜底正文未展开，当前文本: %v", tt.Texts())
	}
	for _, prefix := range []string{"[编排]", "[Hydra]"} {
		if tt.HasText(prefix) {
			t.Fatalf("正文不应出现前缀 %q，当前文本: %v", prefix, tt.Texts())
		}
	}
}

func TestSwarmReportTruncatedCodeBlock(t *testing.T) {
	text := swarm.BuildReport([]swarm.Bee{
		{Name: "Ives", Task: "修复解析器", Status: swarm.StatusFailed, Summary: "```go\n未结束的代码块"},
		{Name: "Dune", Task: "运行测试", Status: swarm.StatusCompleted, Summary: "通过"},
	})
	_, entries := parseSwarmReport(text)
	if len(entries) != 2 || entries[1].Summary != "通过" {
		t.Fatalf("未闭合代码块吞掉了后续 bee: %+v", entries)
	}
}

func TestSwarmReportLegacyDoesNotMatchReusedBee(t *testing.T) {
	for _, tc := range []struct {
		name, loadedFor, task, summary string
		wantFiles                      bool
	}{
		{"匹配原任务", "t1", "旧任务", "旧汇报", true},
		{"名字复用", "t1", "新任务", "新汇报", false},
		{"其他会话", "t2", "旧任务", "旧汇报", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newChatState(NewFakeClient())
			s.selectedThreadID = "t1"
			hp := getSwarmPanelState(s)
			hp.loadedFor = tc.loadedFor
			hp.state.Bees = []SwarmBee{{
				Name: "Ives", Task: tc.task, Status: "completed", Summary: tc.summary,
				ChangedFiles: []string{"current.go"},
			}}
			// 旧前缀 [Hydra] 协作者汇报：… 仍按汇报解析（历史会话兼容）。
			_, entries := parseSwarmReport("[Hydra] 协作者汇报：Ives 已结束工作。\n\n## Ives：旧任务 (已完成)\n- 合并结果：已合并\n- 汇报总结：旧汇报")
			tt := ui.NewTester(func(c *ui.Context) {
				renderSwarmReportEntryCard(c, s, hp, "legacy", 0, entries[0])
			}, 800, 600)
			if tt.HasText("current.go") != tc.wantFiles {
				t.Fatalf("文件匹配错误: %v", tt.Texts())
			}
		})
	}
}

func TestSwarmAvatarStackOverlaps(t *testing.T) {
	tt := ui.NewTester(func(c *ui.Context) {
		renderSwarmAvatarStack(c, "test", []swarmReportEntry{
			{Name: "Ives"}, {Name: "Dune"}, {Name: "Cove"}, {Name: "Ember"}, {Name: "Aspen"},
		})
	}, 400, 100)
	first, ok1 := tt.Find("I")
	second, ok2 := tt.Find("D")
	distance := (second.X + second.W/2) - (first.X + first.W/2)
	if !ok1 || !ok2 || distance < 17.99 || distance > 18.01 || !tt.HasText("+1") {
		t.Fatalf("头像未按 4px 重叠或缺少计数: first=%+v second=%+v texts=%v", first, second, tt.Texts())
	}
}

// TestSwarmReportStatusSummary 验证状态统计的顺序和过滤逻辑。
func TestSwarmReportStatusSummary(t *testing.T) {
	entries := []swarmReportEntry{
		{Status: "已完成"},
		{Status: "失败"},
		{Status: "已完成"},
		{Status: "运行中"},
	}
	got := swarmReportStatusSummary(entries)
	want := "2 已完成 · 1 失败 · 1 运行中"
	if got != want {
		t.Errorf("swarmReportStatusSummary = %q, want %q", got, want)
	}

	if empty := swarmReportStatusSummary(nil); empty != "" {
		t.Errorf("swarmReportStatusSummary(nil) = %q, want empty", empty)
	}
}

// TestSwarmAvatarColorStability 验证头像颜色哈希的稳定性与确定性。
func TestSwarmAvatarColorStability(t *testing.T) {
	c1 := swarmAvatarColor("Ives")
	c2 := swarmAvatarColor("Ives")
	if c1 != c2 {
		t.Errorf("expected swarmAvatarColor to be deterministic for same name: %v vs %v", c1, c2)
	}
	c3 := swarmAvatarColor("Dune")
	c4 := swarmAvatarColor("Dune")
	if c3 != c4 {
		t.Errorf("expected swarmAvatarColor to be deterministic for Dune: %v vs %v", c3, c4)
	}
}

// TestRenderSwarmReportRow_Smoke 渲染测试：验证 Swarm 汇报行及 Popover 正常渲染不 panic。
func TestRenderSwarmReportRow_Smoke(t *testing.T) {
	fake := NewFakeClient()
	fake.SetSwarmState("t1", SwarmState{
		LeadThreadID: "t1",
		Available:    true,
		Enabled:      true,
		Bees: []SwarmBee{
			{
				ThreadID:     "h1",
				Name:         "Ives",
				Task:         "Fix parser",
				Status:       "completed",
				ChangedFiles: []string{"parser.go", "lexer.go"},
			},
		},
	})
	s := newChatState(fake)
	s.selectedThreadID = "t1"

	msg := Message{
		ID:   "report-msg-1",
		Role: "user",
		Text: "[编排] 子代理汇报：Ives 已结束工作。\n\n## Ives：Fix parser (已完成)\n- 合并结果：已合并\n- 汇报总结：解析器已修复。",
	}

	tt := ui.NewTester(func(c *ui.Context) {
		s.init(c)
		renderSwarmReportRow(c, s, msg)
	}, 800, 600)

	if !tt.HasText("Ives 已完成工作") {
		t.Errorf("expected per-bee button to show name and status, got %v", tt.Texts())
	}
	if tt.HasText("子代理汇报：Ives 已结束工作。") {
		t.Errorf("report row should no longer render the merged title")
	}
	// 汇报胶囊与运行中的子线程胶囊共用同一套横向流式布局：排在行首，不再靠右对齐、不再独占整行。
	if bounds, ok := tt.Find("子代理汇报详情"); !ok || bounds.X > 200 || bounds.X+bounds.W >= 800 {
		t.Fatalf("汇报胶囊未按行首对齐或仍占满整行: %+v", bounds)
	}

	if tt.HasText("Fix parser") {
		t.Fatal("点击前不应显示详情")
	}
	if err := tt.Click("子代理汇报详情"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("Fix parser") })
}

// TestSwarmCapsuleUnifiedAppearance 验证运行中的子线程胶囊与已完成的汇报胶囊共用同一套外观：
// 高度一致，且两者都排在行首，不再是“一个靠左、一个靠右”的两套布局。
func TestSwarmCapsuleUnifiedAppearance(t *testing.T) {
	fake := NewFakeClient()
	fake.SetSwarmState("t1", SwarmState{
		LeadThreadID: "t1",
		Available:    true,
		Enabled:      true,
		Bees:         []SwarmBee{{ThreadID: "h1", Name: "Ives", Status: "completed"}},
	})
	report := Message{
		ID:   "report-msg-unified",
		Role: "user",
		Text: "[编排] 子代理汇报：Ives 已结束工作。\n\n## Ives：Fix parser (已完成)\n- 汇报总结：解析器已修复。",
	}
	s := newChatState(fake)
	s.selectedThreadID = "t1"

	tt := ui.NewTester(func(c *ui.Context) {
		s.init(c)
		ui.Column(c.Key("capsule-compare")).
			FillWidth().
			Children(func() {
				renderSwarmTimelineCapsules(c, s, swarmCapsuleRowID, "t1", []SwarmBee{{ThreadID: "h1", Name: "Ives", Status: "completed"}})
				renderSwarmReportRow(c, s, report)
			})
	}, 800, 400)

	running, ok := tt.Find("子线程 Ives 已完成")
	if !ok {
		t.Fatalf("找不到运行中（已完成）子线程胶囊: %v", tt.Texts())
	}
	done, ok := tt.Find("子代理汇报详情")
	if !ok {
		t.Fatalf("找不到汇报胶囊: %v", tt.Texts())
	}
	if running.H != done.H {
		t.Fatalf("两类胶囊高度不一致: running=%+v done=%+v", running, done)
	}
	if running.X != done.X || done.Y <= running.Y {
		t.Fatalf("两类胶囊未按同一套流式布局排列: running=%+v done=%+v", running, done)
	}
}

// TestRenderSwarmReportRow_SplitsPerBee 验证多个 bee 的汇报拆成各自按钮，弹窗只显示对应 bee 的详情。
func TestRenderSwarmReportRow_SplitsPerBee(t *testing.T) {
	fake := NewFakeClient()
	fake.SetSwarmState("t1", SwarmState{
		LeadThreadID: "t1",
		Available:    true,
		Enabled:      true,
		Bees: []SwarmBee{
			{ThreadID: "h1", Name: "Ives", Task: "Fix parser", Status: "completed"},
			{ThreadID: "h2", Name: "Flint", Task: "Fix lexer", Status: "failed"},
		},
	})
	s := newChatState(fake)
	s.selectedThreadID = "t1"

	msg := Message{
		ID:   "report-msg-2",
		Role: "user",
		Text: "[编排] 子代理汇报：Ives, Flint 已结束工作。\n\n## Ives：Fix parser (已完成)\n- 合并结果：已合并\n- 汇报总结：解析器已修复。\n\n## Flint：Fix lexer (失败)\n- 合并结果：未合并\n- 汇报总结：词法器未修复。",
	}

	tt := ui.NewTester(func(c *ui.Context) {
		s.init(c)
		renderSwarmReportRow(c, s, msg)
	}, 800, 600)

	if !tt.HasText("Ives 已完成工作") || !tt.HasText("Flint 失败") {
		t.Fatalf("expected one button per bee, got %v", tt.Texts())
	}
	if tt.HasText("Fix parser") || tt.HasText("Fix lexer") {
		t.Fatal("点击前不应显示任何 bee 的详情")
	}

	ives, ok := tt.Find("Ives 已完成工作")
	if !ok {
		t.Fatal("找不到 Ives 的汇报按钮")
	}
	// 同一行的多枚胶囊横向流式排列（同一 Y），不再每枚占一整行竖着排。
	if flint, ok := tt.Find("Flint 失败"); !ok || flint.Y != ives.Y || flint.X <= ives.X {
		t.Fatalf("多枚汇报胶囊未横向排列: ives=%+v flint=%+v", ives, flint)
	}
	tt.ClickAt(ives.X+ives.W/2, ives.Y+ives.H/2)
	waitFor(t, tt, func() bool { return tt.HasText("Fix parser") })
	if tt.HasText("Fix lexer") {
		t.Fatal("打开 Ives 详情时不应同时显示 Flint 的详情")
	}
}

// TestSwarmReportPillText 验证汇报胶囊文案：已完成带 "工作" 后缀，失败/已停止直出状态。
func TestSwarmReportPillText(t *testing.T) {
	cases := []struct {
		name string
		in   swarmReportEntry
		want string
	}{
		{name: "completed", in: swarmReportEntry{Name: "Cove", Status: "已完成"}, want: "Cove 已完成工作"},
		{name: "empty status", in: swarmReportEntry{Name: "Cove", Status: ""}, want: "Cove 已完成工作"},
		{name: "failed", in: swarmReportEntry{Name: "Larch", Status: "失败"}, want: "Larch 失败"},
		{name: "stopped", in: swarmReportEntry{Name: "Moss", Status: "已停止"}, want: "Moss 已停止"},
		{name: "empty name", in: swarmReportEntry{Name: "", Status: "失败"}, want: "子代理 失败"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := swarmReportPillText(tc.in); got != tc.want {
				t.Errorf("swarmReportPillText(%+v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSwarmReview_OpenAndParseDiff 测试 openSwarmReview 触发 SwarmMergeDiff 并正确解析 diff 为 reviewFiles。
func TestSwarmReview_OpenAndParseDiff(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)
	s.selectedThreadID = "lead-1"
	fake.SetSwarmState("lead-1", SwarmState{
		LeadThreadID: "lead-1",
		Available:    true,
		Enabled:      true,
		Bees: []SwarmBee{
			{ThreadID: "bee-1", Name: "Bee1", Status: "completed", PendingMerge: true},
		},
	})
	sampleDiff := `diff --git a/foo.go b/foo.go
index 1111111..2222222 100644
--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,4 @@
 package main
+// new line
`
	fake.SetSwarmMergeDiff("bee-1", ThreadDiff{Diff: sampleDiff})

	s.openSwarmReview("bee-1")

	hp := getSwarmPanelState(s)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		swarmPanelMu.Lock()
		done := hp.reviewOpen && !hp.reviewLoading && len(hp.reviewFiles) == 1 && hp.reviewErr == ""
		swarmPanelMu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	swarmPanelMu.Lock()
	defer swarmPanelMu.Unlock()
	if !hp.reviewOpen {
		t.Fatal("review was not opened")
	}
	if hp.reviewLoading {
		t.Fatal("review is still loading")
	}
	if hp.reviewErr != "" {
		t.Fatalf("unexpected reviewErr: %s", hp.reviewErr)
	}
	if len(hp.reviewFiles) != 1 || hp.reviewFiles[0].NewPath != "foo.go" {
		t.Fatalf("unexpected reviewFiles: %+v", hp.reviewFiles)
	}
}

// TestSwarmReview_ApproveMerge 测试 approveSwarmMerge 记录调用。
func TestSwarmReview_ApproveMerge(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)
	s.selectedThreadID = "lead-1"
	fake.SetSwarmState("lead-1", SwarmState{
		LeadThreadID: "lead-1",
		Available:    true,
		Enabled:      true,
		Bees: []SwarmBee{
			{ThreadID: "bee-1", Name: "Bee1", Status: "completed", PendingMerge: true},
		},
	})

	s.approveSwarmMerge("bee-1")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		calls := fake.ApproveMergeCalls()
		if len(calls) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	calls := fake.ApproveMergeCalls()
	if len(calls) == 0 || calls[0] != "bee-1" {
		t.Fatalf("expected bee-1 in ApproveMergeCalls, got: %v", calls)
	}
}

// TestSwarmReview_DiscardMerge 测试 discardSwarmMerge 记录调用。
func TestSwarmReview_DiscardMerge(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)
	s.selectedThreadID = "lead-1"
	fake.SetSwarmState("lead-1", SwarmState{
		LeadThreadID: "lead-1",
		Available:    true,
		Enabled:      true,
		Bees: []SwarmBee{
			{ThreadID: "bee-1", Name: "Bee1", Status: "completed", PendingMerge: true},
		},
	})

	s.discardSwarmMerge("bee-1")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		calls := fake.DiscardMergeCalls()
		if len(calls) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	calls := fake.DiscardMergeCalls()
	if len(calls) == 0 || calls[0] != "bee-1" {
		t.Fatalf("expected bee-1 in DiscardMergeCalls, got: %v", calls)
	}
}

// prepareSwarmDiscardReview 准备一个已打开合并审查面板、且 bee-1 待审查的 chatState。
// 审查面板直接读 hp.state 的派生字段（Available/Bees/PendingMerge），只设置 reviewOpen
// 而不同步 state 会让 renderSwarmPanel 提前返回「编排不可用」，渲染不出「放弃」按钮。
func prepareSwarmDiscardReview(fake *FakeClient) *chatState {
	fake.SetSwarmState("lead-1", SwarmState{
		LeadThreadID: "lead-1",
		Available:    true,
		Enabled:      true,
		Bees: []SwarmBee{
			{ThreadID: "bee-1", Name: "Bee1", Status: "completed", PendingMerge: true},
		},
	})
	s := newChatState(fake)
	s.selectedThreadID = "lead-1"

	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.open = true
	hp.loadedFor = "lead-1"
	hp.setStateLocked(SwarmState{
		LeadThreadID: "lead-1",
		Available:    true,
		Enabled:      true,
		Bees: []SwarmBee{
			{ThreadID: "bee-1", Name: "Bee1", Status: "completed", PendingMerge: true},
		},
	})
	hp.reviewOpen = true
	hp.reviewBeeID = "bee-1"
	hp.reviewDiff = &diffPanelState{collapsed: make(map[string]bool)}
	hp.reviewFiles = []git.FileDiff{{OldPath: "foo.go", NewPath: "foo.go", Status: "modified"}}
	swarmPanelMu.Unlock()
	return s
}

// TestSwarmReview_DiscardConfirm 测试「放弃」先弹二次确认，确认后才真正放弃。
func TestSwarmReview_DiscardConfirm(t *testing.T) {
	fake := NewFakeClient()
	s := prepareSwarmDiscardReview(fake)

	tt := ui.NewTester(func(c *ui.Context) {
		s.init(c)
		renderSwarmPanel(c, s)
	}, 900, 700)

	if err := tt.Click("放弃"); err != nil {
		t.Fatalf("click '放弃' failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("放弃这些改动？") })
	if !tt.HasText("子代理的改动不会合入工作区，此操作不可撤销") {
		t.Fatalf("确认框应说明后果，实际文本：%v", tt.Texts())
	}
	if calls := fake.DiscardMergeCalls(); len(calls) != 0 {
		t.Fatalf("确认前不应调用 DiscardMerge，实际：%v", calls)
	}

	if err := tt.Click("放弃改动"); err != nil {
		t.Fatalf("click '放弃改动' failed: %v", err)
	}
	waitFor(t, tt, func() bool { return len(fake.DiscardMergeCalls()) > 0 })

	calls := fake.DiscardMergeCalls()
	if len(calls) == 0 || calls[0] != "bee-1" {
		t.Fatalf("确认后应放弃 bee-1，实际：%v", calls)
	}
}

// TestSwarmReview_DiscardConfirmCancel 测试「取消」只关闭二次确认，不放弃改动。
func TestSwarmReview_DiscardConfirmCancel(t *testing.T) {
	fake := NewFakeClient()
	s := prepareSwarmDiscardReview(fake)

	tt := ui.NewTester(func(c *ui.Context) {
		s.init(c)
		renderSwarmPanel(c, s)
	}, 900, 700)

	if err := tt.Click("放弃"); err != nil {
		t.Fatalf("click '放弃' failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("放弃这些改动？") })

	if err := tt.Click("取消"); err != nil {
		t.Fatalf("click '取消' failed: %v", err)
	}
	waitFor(t, tt, func() bool { return !tt.HasText("放弃这些改动？") })

	// 取消只关确认框，不应触发放弃；留一点时间捕捉可能的异步误调用。
	time.Sleep(100 * time.Millisecond)
	tt.Frame()
	if calls := fake.DiscardMergeCalls(); len(calls) != 0 {
		t.Fatalf("取消后不应调用 DiscardMerge，实际：%v", calls)
	}
}

// prepareSwarmReviewState 准备一个已打开合并审查面板、且 bee-1 处于指定审查状态的 chatState。
func prepareSwarmReviewState(fake *FakeClient, bee SwarmBee) (*chatState, SwarmState) {
	state := SwarmState{
		LeadThreadID: "lead-1",
		Available:    true,
		Enabled:      true,
		Bees:         []SwarmBee{bee},
	}
	fake.SetSwarmState("lead-1", state)
	s := newChatState(fake)
	s.selectedThreadID = "lead-1"

	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.open = true
	hp.loadedFor = "lead-1"
	hp.setStateLocked(state)
	hp.reviewOpen = true
	hp.reviewBeeID = bee.ThreadID
	hp.reviewDiff = &diffPanelState{collapsed: make(map[string]bool)}
	hp.reviewFiles = []git.FileDiff{{OldPath: "foo.go", NewPath: "foo.go", Status: "modified"}}
	swarmPanelMu.Unlock()
	return s, state
}

// TestSwarmReview_RunningShowsProgress 测试 AI 审查进行中的 bee 只展示进度，不给批准与放弃入口。
func TestSwarmReview_RunningShowsProgress(t *testing.T) {
	fake := NewFakeClient()
	s, st := prepareSwarmReviewState(fake, SwarmBee{
		ThreadID:     "bee-1",
		Name:         "Bee1",
		Status:       "completed",
		PendingMerge: true,
		ReviewState:  "running",
	})

	tt := ui.NewTester(func(c *ui.Context) {
		s.init(c)
		renderSwarmMergeReview(c, s, st)
	}, 800, 600)

	if !tt.HasText("审查模型正在审查…") {
		t.Fatalf("审查中应显示进度，实际文本：%v", tt.Texts())
	}
	if tt.HasText("批准合并") || tt.HasText("放弃") {
		t.Fatalf("审查中不应显示批准或放弃按钮，实际文本：%v", tt.Texts())
	}
}

// TestSwarmReview_RejectedShowsReason 测试审查未通过的 bee 展示审查意见，不给批准入口。
func TestSwarmReview_RejectedShowsReason(t *testing.T) {
	fake := NewFakeClient()
	s, st := prepareSwarmReviewState(fake, SwarmBee{
		ThreadID:     "bee-1",
		Name:         "Bee1",
		Status:       "completed",
		ReviewState:  "rejected",
		ReviewReason: "改动不完整，缺少测试",
	})

	tt := ui.NewTester(func(c *ui.Context) {
		s.init(c)
		renderSwarmMergeReview(c, s, st)
	}, 800, 600)

	if !tt.HasText("审查意见：改动不完整，缺少测试") {
		t.Fatalf("审查未通过应显示审查意见，实际文本：%v", tt.Texts())
	}
	if !tt.HasText("改动未合入，补丁已保留，可让主代理派子代理按审查意见返工") {
		t.Fatalf("审查未通过应提示补丁已保留，实际文本：%v", tt.Texts())
	}
	if tt.HasText("批准合并") || tt.HasText("放弃") {
		t.Fatalf("审查未通过不应显示批准或放弃按钮，实际文本：%v", tt.Texts())
	}
}

// TestSwarmReview_ShowsReviewLayerAndConcerns 测试审查漏斗结果：决定层、估算 token 与审查疑点。
func TestSwarmReview_ShowsReviewLayerAndConcerns(t *testing.T) {
	fake := NewFakeClient()
	s, st := prepareSwarmReviewState(fake, SwarmBee{
		ThreadID:              "bee-1",
		Name:                  "Bee1",
		Status:                "completed",
		ReviewState:           "rejected",
		ReviewLayer:           "l2",
		ReviewTokens:          3200,
		ReviewTokensEstimated: true,
		ReviewConcerns:        []string{"foo.go:10 缺少错误处理", "bar.go:20 测试不足"},
	})

	tt := ui.NewTester(func(c *ui.Context) {
		s.init(c)
		renderSwarmMergeReview(c, s, st)
	}, 800, 600)

	if !tt.HasText("L2 模型审查") {
		t.Fatalf("应显示审查决定层，实际文本：%v", tt.Texts())
	}
	if !tt.HasText("约 3.2k tokens") {
		t.Fatalf("应显示估算 token 数，实际文本：%v", tt.Texts())
	}
	if !tt.HasText("审查疑点") {
		t.Fatalf("应显示审查疑点小标题，实际文本：%v", tt.Texts())
	}
	for _, concern := range []string{"foo.go:10 缺少错误处理", "bar.go:20 测试不足"} {
		if !tt.HasText(concern) {
			t.Fatalf("应显示审查疑点 %q，实际文本：%v", concern, tt.Texts())
		}
	}
}

// TestSwarmReview_HidesFunnelInfoWhenEmpty 测试旧数据或 off 档的 bee 不渲染任何审查漏斗内容。
func TestSwarmReview_HidesFunnelInfoWhenEmpty(t *testing.T) {
	fake := NewFakeClient()
	s, st := prepareSwarmReviewState(fake, SwarmBee{
		ThreadID:     "bee-1",
		Name:         "Bee1",
		Status:       "completed",
		ReviewState:  "rejected",
		ReviewReason: "改动不完整，缺少测试",
	})

	tt := ui.NewTester(func(c *ui.Context) {
		s.init(c)
		renderSwarmMergeReview(c, s, st)
	}, 800, 600)

	if tt.HasText("审查疑点") {
		t.Fatalf("无审查疑点时不应显示小标题，实际文本：%v", tt.Texts())
	}
	for _, label := range []string{"L0 确定性检查", "L1 规则放行", "L2 模型审查", "L3 审查会话"} {
		if tt.HasText(label) {
			t.Fatalf("无决定层时不应显示 %q，实际文本：%v", label, tt.Texts())
		}
	}
}

func TestReviewLayerLabel(t *testing.T) {
	cases := map[string]string{
		"l0":    "L0 确定性检查",
		"l1":    "L1 规则放行",
		"l2":    "L2 模型审查",
		"l3":    "L3 审查会话",
		"human": "人工审查",
		"other": "other",
	}
	for layer, want := range cases {
		if got := reviewLayerLabel(layer); got != want {
			t.Errorf("reviewLayerLabel(%q) = %q, want %q", layer, got, want)
		}
	}
}

func TestFormatReviewTokenCount(t *testing.T) {
	cases := []struct {
		n         int
		estimated bool
		want      string
	}{
		{999, false, "999 tokens"},
		{1000, false, "1.0k tokens"},
		{3200, true, "约 3.2k tokens"},
		{420, true, "约 420 tokens"},
	}
	for _, tc := range cases {
		if got := formatReviewTokenCount(tc.n, tc.estimated); got != tc.want {
			t.Errorf("formatReviewTokenCount(%d, %v) = %q, want %q", tc.n, tc.estimated, got, tc.want)
		}
	}
}

// TestSwarmReview_OpenReviewThread 测试有审查会话时点击「查看审查会话」关闭审查面板并切到审查会话。
func TestSwarmReview_OpenReviewThread(t *testing.T) {
	fake := NewFakeClient()
	s, st := prepareSwarmReviewState(fake, SwarmBee{
		ThreadID:       "bee-1",
		Name:           "Bee1",
		Status:         "completed",
		ReviewState:    "rejected",
		ReviewReason:   "改动不完整，缺少测试",
		ReviewThreadID: "review-1",
	})

	tt := ui.NewTester(func(c *ui.Context) {
		s.init(c)
		renderSwarmMergeReview(c, s, st)
	}, 800, 600)

	if !tt.HasText("查看审查会话") {
		t.Fatalf("有审查会话时应显示「查看审查会话」按钮，实际文本：%v", tt.Texts())
	}
	if err := tt.Click("查看审查会话"); err != nil {
		t.Fatalf("click '查看审查会话' failed: %v", err)
	}

	hp := getSwarmPanelState(s)
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		selected := s.selectedThreadID
		s.mu.Unlock()
		swarmPanelMu.Lock()
		open := hp.reviewOpen
		swarmPanelMu.Unlock()
		return !open && selected == "review-1"
	})
}

// TestSwarmReview_OpenReviewThreadHiddenWithoutThread 测试没有审查会话时不显示「查看审查会话」按钮。
func TestSwarmReview_OpenReviewThreadHiddenWithoutThread(t *testing.T) {
	fake := NewFakeClient()
	s, st := prepareSwarmReviewState(fake, SwarmBee{
		ThreadID:     "bee-1",
		Name:         "Bee1",
		Status:       "completed",
		ReviewState:  "rejected",
		ReviewReason: "改动不完整，缺少测试",
	})

	tt := ui.NewTester(func(c *ui.Context) {
		s.init(c)
		renderSwarmMergeReview(c, s, st)
	}, 800, 600)

	if tt.HasText("查看审查会话") {
		t.Fatalf("无审查会话时不应显示「查看审查会话」按钮，实际文本：%v", tt.Texts())
	}
}

// TestSwarmReview_DiffError 测试 SwarmMergeDiff 出错时 reviewErr 非空。
func TestSwarmReview_DiffError(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)
	s.selectedThreadID = "lead-1"
	fake.SetSwarmState("lead-1", SwarmState{
		LeadThreadID: "lead-1",
		Available:    true,
		Enabled:      true,
		Bees: []SwarmBee{
			{ThreadID: "bee-1", Name: "Bee1", Status: "completed", PendingMerge: true},
		},
	})
	fake.SetSwarmMergeDiffError(context.DeadlineExceeded)

	s.openSwarmReview("bee-1")

	hp := getSwarmPanelState(s)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		swarmPanelMu.Lock()
		done := hp.reviewOpen && !hp.reviewLoading && hp.reviewErr != ""
		swarmPanelMu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	swarmPanelMu.Lock()
	defer swarmPanelMu.Unlock()
	if hp.reviewErr == "" {
		t.Fatal("expected non-empty reviewErr on SwarmMergeDiff failure")
	}
}

// TestSwarmReview_RenderSmoke 冒烟测试：合并审查器渲染（包含 PendingMerge 与带 ConflictFiles 的 bee）不 panic。
func TestSwarmReview_RenderSmoke(t *testing.T) {
	fake := NewFakeClient()
	fake.SetSwarmState("lead-1", SwarmState{
		LeadThreadID: "lead-1",
		Available:    true,
		Enabled:      true,
		Bees: []SwarmBee{
			{
				ThreadID:     "bee-1",
				Name:         "Bee1",
				Status:       "completed",
				PendingMerge: true,
			},
			{
				ThreadID:      "bee-2",
				Name:          "Bee2",
				Status:        "completed",
				Merged:        false,
				ConflictFiles: []string{"main.go", "util.go"},
				MergeError:    "自动合并发生冲突",
			},
		},
	})
	s := newChatState(fake)
	s.selectedThreadID = "lead-1"

	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.open = true
	hp.loadedFor = "lead-1"
	hp.reviewOpen = true
	hp.reviewBeeID = "bee-1"
	hp.reviewDiff = &diffPanelState{collapsed: make(map[string]bool)}
	hp.reviewFiles = []git.FileDiff{
		{
			OldPath: "foo.go",
			NewPath: "foo.go",
			Status:  "modified",
			Hunks: []git.Hunk{
				{
					Header: "@@ -1,3 +1,4 @@",
					Lines: []git.Line{
						{Kind: ' ', Text: "package main", OldNo: 1, NewNo: 1},
						{Kind: '+', Text: "// added comment", NewNo: 2},
					},
				},
			},
		},
	}
	swarmPanelMu.Unlock()

	st, _ := fake.Swarm(context.Background(), "lead-1")

	tt := ui.NewTester(func(c *ui.Context) {
		s.init(c)
		renderSwarmMergeReview(c, s, st)
	}, 800, 600)

	if !tt.HasText("合并审查") {
		t.Fatal("expected '合并审查' title")
	}
	if !tt.HasText("批准合并") {
		t.Fatal("expected '批准合并' button for PendingMerge bee")
	}
	if !tt.HasText("放弃") {
		t.Fatal("expected '放弃' button for PendingMerge bee")
	}

	// 切换选中 bee-2（带冲突文件与 MergeError）
	swarmPanelMu.Lock()
	hp.reviewBeeID = "bee-2"
	hp.reviewFiles = nil
	swarmPanelMu.Unlock()

	tt.Frame()

	if !tt.HasText("冲突文件: main.go, util.go") {
		t.Fatalf("expected conflict files displayed, got texts: %v", tt.Texts())
	}
	if !tt.HasText("合并错误: 自动合并发生冲突") {
		t.Fatalf("expected merge error displayed, got texts: %v", tt.Texts())
	}
}

func TestSwarmReview_SessionChangeClearsReview(t *testing.T) {
	s := newChatState(NewFakeClient())
	hp := getSwarmPanelState(s)
	hp.loadedFor = "lead-1"
	hp.reviewOpen = true
	hp.reviewBeeID = "bee-1"
	hp.reviewFiles = []git.FileDiff{{NewPath: "old.go"}}
	hp.reviewErr = "旧会话错误"
	hp.reviewLoading = true
	hp.reviewBusy = true
	loadID, actionID := hp.reviewLoadID, hp.reviewActionID

	s.loadSwarm("lead-2")

	swarmPanelMu.Lock()
	defer swarmPanelMu.Unlock()
	if hp.reviewOpen || hp.reviewBeeID != "" || len(hp.reviewFiles) != 0 || hp.reviewErr != "" || hp.reviewLoading || hp.reviewBusy {
		t.Fatalf("切换会话未清除审查状态: %+v", hp)
	}
	if hp.reviewLoadID == loadID || hp.reviewActionID == actionID {
		t.Fatal("切换会话未使旧异步请求失效")
	}
}

func TestSwarmReview_RemovedBeeReloadsDiff(t *testing.T) {
	fake := NewFakeClient()
	fake.SetSwarmMergeDiff("bee-2", ThreadDiff{Diff: `diff --git a/new.go b/new.go
new file mode 100644
--- /dev/null
+++ b/new.go
@@ -0,0 +1 @@
+package main
`})
	s := newChatState(fake)
	s.selectedThreadID = "lead-1"
	hp := getSwarmPanelState(s)
	hp.reviewOpen = true
	hp.reviewBeeID = "removed-bee"
	hp.reviewDiff = &diffPanelState{collapsed: make(map[string]bool)}
	hp.reviewFiles = []git.FileDiff{{NewPath: "old.go", Status: "modified"}}
	st := SwarmState{LeadThreadID: "lead-1", Bees: []SwarmBee{
		{ThreadID: "bee-2", Name: "Bee2", Status: "completed", PendingMerge: true},
	}}
	tt := ui.NewTester(func(c *ui.Context) {
		s.init(c)
		renderSwarmMergeReview(c, s, st)
	}, 800, 600)
	if tt.HasText("old.go") {
		t.Fatal("新 bee 不应展示已移除 bee 的差异")
	}
	waitFor(t, tt, func() bool { return tt.HasText("new.go") })
}

// recordingSwarmClient 记录 Swarm 会话开关的调用，供开关与只规划测试断言（FakeClient 自身不暴露这些记录）。
type recordingSwarmClient struct {
	*FakeClient
	mu             sync.Mutex
	enabledCalls   []swarmEnabledCall
	plansOnlyCalls []swarmPlansOnlyCall
}

type swarmEnabledCall struct {
	threadID string
	enabled  bool
}

type swarmPlansOnlyCall struct {
	threadID  string
	plansOnly bool
}

func (f *recordingSwarmClient) SetSwarmEnabled(ctx context.Context, threadID string, enabled bool) (SwarmState, error) {
	f.mu.Lock()
	f.enabledCalls = append(f.enabledCalls, swarmEnabledCall{threadID: threadID, enabled: enabled})
	f.mu.Unlock()
	return f.FakeClient.SetSwarmEnabled(ctx, threadID, enabled)
}

func (f *recordingSwarmClient) SetSwarmPlansOnly(ctx context.Context, threadID string, plansOnly bool) (SwarmState, error) {
	f.mu.Lock()
	f.plansOnlyCalls = append(f.plansOnlyCalls, swarmPlansOnlyCall{threadID: threadID, plansOnly: plansOnly})
	f.mu.Unlock()
	return f.FakeClient.SetSwarmPlansOnly(ctx, threadID, plansOnly)
}

func (f *recordingSwarmClient) enabledSnapshot() []swarmEnabledCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]swarmEnabledCall(nil), f.enabledCalls...)
}

func (f *recordingSwarmClient) plansOnlySnapshot() []swarmPlansOnlyCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]swarmPlansOnlyCall(nil), f.plansOnlyCalls...)
}

// TestSwarmHaloStyle 覆盖光环样式纯函数：关闭时不画；运行中比空闲亮；
// 普通模式画外层柔光，性能模式只画单色细描边；两种模式都不含动画/定时重绘。
func TestSwarmHaloStyle(t *testing.T) {
	// 关闭：任何模式、任何运行数都不画光环
	for _, perf := range []bool{false, true} {
		if got := swarmHaloStyle(false, 0, perf); got.Width != 0 || got.Alpha != 0 || got.Glow {
			t.Fatalf("关闭时光环样式 = %+v, want 零值", got)
		}
		if got := swarmHaloStyle(false, 3, perf); got.Width != 0 || got.Alpha != 0 || got.Glow {
			t.Fatalf("关闭且有运行 bee 时光环样式 = %+v, want 零值", got)
		}
	}

	// 普通模式：内层实色描边 + 外层柔光；运行中比空闲更亮
	idle := swarmHaloStyle(true, 0, false)
	running := swarmHaloStyle(true, 2, false)
	if idle.Width <= 0 || idle.Alpha <= 0 || idle.Alpha >= 1 || !idle.Glow {
		t.Fatalf("普通模式空闲光环 = %+v, want 有描边、亮度在 (0,1) 且画外层柔光", idle)
	}
	if running.Alpha <= idle.Alpha || running.Alpha > 1 || !running.Glow {
		t.Fatalf("普通模式运行中光环 = %+v, want 比空闲更亮且画外层柔光", running)
	}
	if running.Width != idle.Width {
		t.Fatalf("普通模式运行/空闲描边宽度应一致 = %v/%v", running.Width, idle.Width)
	}

	// 性能模式：只画单色细描边，不画外层柔光；亮度规则与普通模式一致
	perfIdle := swarmHaloStyle(true, 0, true)
	perfRunning := swarmHaloStyle(true, 2, true)
	if perfIdle.Glow || perfRunning.Glow {
		t.Fatalf("性能模式不应画外层柔光: idle=%+v running=%+v", perfIdle, perfRunning)
	}
	if perfIdle.Width <= 0 || perfIdle.Width >= idle.Width {
		t.Fatalf("性能模式描边应更细: %v, 普通模式 %v", perfIdle.Width, idle.Width)
	}
	if perfIdle.Alpha != idle.Alpha || perfRunning.Alpha != running.Alpha {
		t.Fatalf("性能模式亮度应与普通模式一致: %+v/%+v vs %+v/%+v", perfIdle, perfRunning, idle, running)
	}
	if perfRunning.Alpha <= perfIdle.Alpha {
		t.Fatalf("性能模式运行中应比空闲亮: %v <= %v", perfRunning.Alpha, perfIdle.Alpha)
	}

	// 纯函数：相同输入得到相同输出，没有动画/定时重绘带来的状态漂移
	if swarmHaloStyle(true, 1, false) != swarmHaloStyle(true, 1, false) {
		t.Fatal("swarmHaloStyle 应为确定性纯函数")
	}
}

// TestSwarmPanelPositionClampAndDock 覆盖浮动面板位置的越界限制与左右停靠。
func TestSwarmPanelPositionClampAndDock(t *testing.T) {
	const panelW, panelH = float32(400), float32(300)
	const winW, winH = float32(1000), float32(700)
	const margin = float32(8)

	// 越界：左上角与右下角都被拉回 8 边距内
	if x, y := clampSwarmPanelPos(-100, -50, panelW, panelH, winW, winH); x != margin || y != margin {
		t.Fatalf("左上越界 clamp = (%v,%v), want (%v,%v)", x, y, margin, margin)
	}
	wantRight, wantBottom := winW-panelW-margin, winH-panelH-margin
	if x, y := clampSwarmPanelPos(5000, 5000, panelW, panelH, winW, winH); x != wantRight || y != wantBottom {
		t.Fatalf("右下越界 clamp = (%v,%v), want (%v,%v)", x, y, wantRight, wantBottom)
	}

	// 容器内中间：位置不变，也不停靠
	if x, y := clampSwarmPanelPos(300, 200, panelW, panelH, winW, winH); x != 300 || y != 200 {
		t.Fatalf("中间 clamp = (%v,%v), want (300,200)", x, y)
	}
	if x, _ := dockSwarmPanelPos(300, 200, panelW, panelH, winW, winH); x != 300 {
		t.Fatalf("中间不应停靠, x = %v", x)
	}

	// 靠近左边（距边 23）与右边（距边 23）时吸附到最近一侧
	if x, _ := dockSwarmPanelPos(margin+23, 200, panelW, panelH, winW, winH); x != margin {
		t.Fatalf("靠近左边应吸附到 %v, x = %v", margin, x)
	}
	if x, _ := dockSwarmPanelPos(wantRight-23, 200, panelW, panelH, winW, winH); x != wantRight {
		t.Fatalf("靠近右边应吸附到 %v, x = %v", wantRight, x)
	}

	// 容器缩小：旧位置重新 clamp 后仍在容器内，并吸附到右边缘
	x, y := dockSwarmPanelPos(wantRight, wantBottom, panelW, panelH, 500, 400)
	if x < margin || y < margin || x+panelW > 500-margin || y+panelH > 400-margin {
		t.Fatalf("容器缩小后位置越界 = (%v,%v)", x, y)
	}
	if x != 500-panelW-margin {
		t.Fatalf("容器缩小后应贴右边, x = %v, want %v", x, 500-panelW-margin)
	}

	// 面板比容器还宽：至少贴住左边
	if x, _ := dockSwarmPanelPos(300, 200, 600, panelH, 500, 400); x != margin {
		t.Fatalf("面板宽于容器时应贴左边, x = %v", x)
	}
}

// TestSwarmPopOutBeeID 覆盖标题栏弹出按钮的目标选择：优先运行中的 bee，其次第一个。
func TestSwarmPopOutBeeID(t *testing.T) {
	if got := swarmPopOutBeeID(SwarmState{}); got != "" {
		t.Fatalf("没有 bee 时目标 = %q, want 空", got)
	}
	st := SwarmState{Bees: []SwarmBee{
		{ThreadID: "bee-done", Status: "completed"},
		{ThreadID: "bee-run", Status: "running"},
	}}
	if got := swarmPopOutBeeID(st); got != "bee-run" {
		t.Fatalf("有运行 bee 时目标 = %q, want bee-run", got)
	}
	if got := swarmPopOutBeeID(SwarmState{Bees: []SwarmBee{{ThreadID: "bee-only", Status: "completed"}}}); got != "bee-only" {
		t.Fatalf("没有运行 bee 时目标 = %q, want bee-only", got)
	}
}

// TestSwarmSwitchCallsClient 覆盖会话开关与只规划：调用 client 并带着正确的值，随后状态刷新。
func TestSwarmSwitchCallsClient(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := &recordingSwarmClient{FakeClient: NewFakeClient()}
	s := newChatState(fake)
	s.selectedThreadID = "t1"
	hp := getSwarmPanelState(s)

	deadline := time.Now().Add(2 * time.Second)

	s.setSwarmEnabled(true)
	for time.Now().Before(deadline) {
		if len(fake.enabledSnapshot()) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	calls := fake.enabledSnapshot()
	if len(calls) != 1 || calls[0].threadID != "t1" || !calls[0].enabled {
		t.Fatalf("SetSwarmEnabled 调用 = %+v, want [{t1 true}]", calls)
	}

	// 状态随之刷新为已开启
	for time.Now().Before(deadline) {
		swarmPanelMu.Lock()
		enabled := hp.state.Enabled
		swarmPanelMu.Unlock()
		if enabled {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	swarmPanelMu.Lock()
	enabled := hp.state.Enabled
	swarmPanelMu.Unlock()
	if !enabled {
		t.Fatal("开启 Swarm 后状态未更新")
	}

	// 关闭，并打开只规划
	s.setSwarmEnabled(false)
	s.setSwarmPlansOnly(true)
	for time.Now().Before(deadline) {
		if len(fake.enabledSnapshot()) >= 2 && len(fake.plansOnlySnapshot()) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	calls = fake.enabledSnapshot()
	if len(calls) != 2 || calls[1].threadID != "t1" || calls[1].enabled {
		t.Fatalf("第二次 SetSwarmEnabled 调用 = %+v, want 第二次为 t1/false", calls)
	}
	plans := fake.plansOnlySnapshot()
	if len(plans) != 1 || plans[0].threadID != "t1" || !plans[0].plansOnly {
		t.Fatalf("SetSwarmPlansOnly 调用 = %+v, want [{t1 true}]", plans)
	}
}

// TestSwarmPopOutBeeUsesOpener 覆盖弹出 bee：以 bee 的线程 ID 调用窗口打开器，空 bee ID 不调用。
func TestSwarmPopOutBeeUsesOpener(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t1"
	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.setStateLocked(SwarmState{
		LeadThreadID: "t1",
		Available:    true,
		Enabled:      true,
		Bees:         []SwarmBee{{ThreadID: "bee-pop", Name: "Dune"}},
	})
	swarmPanelMu.Unlock()

	oldOpener := beeWindowOpener
	t.Cleanup(func() { beeWindowOpener = oldOpener })
	var opened []string
	beeWindowOpener = func(parent *chatState, beeID string) {
		if parent != s {
			t.Errorf("窗口打开器收到的父状态 = %p, want %p", parent, s)
		}
		opened = append(opened, beeID)
	}

	s.popOutSwarmBee("bee-pop")
	if len(opened) != 1 || opened[0] != "bee-pop" {
		t.Fatalf("窗口打开器收到的 bee = %v, want [bee-pop]", opened)
	}

	s.popOutSwarmBee("")
	if len(opened) != 1 {
		t.Fatalf("空 bee ID 不应触发窗口打开器, got %v", opened)
	}
}

func TestSwarmPopoverDismissHelpers(t *testing.T) {
	// 汇报浮窗：关闭时点击打开；打开时点击（PopoverBase 已写入关闭）保持关闭；被 PopoverBase 关闭后保持关闭。
	if !shouldKeepSwarmReportOpen(false, false) {
		t.Error("closed report popover should open on click")
	}
	if shouldKeepSwarmReportOpen(true, false) {
		t.Error("click on an open report popover must not reopen it in the same frame")
	}
	if shouldKeepSwarmReportOpen(true, true) || shouldKeepSwarmReportOpen(false, true) {
		t.Error("dismissed report popover must stay closed")
	}
}

// TestSwarmIdlePopover 覆盖没有 bee 时的编排按钮：左键切换浮窗显示，第二次点击收起。
func TestSwarmIdlePopover(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t1"
	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.loadedFor = "t1"
	hp.setStateLocked(SwarmState{LeadThreadID: "t1", Available: true, Enabled: true})
	swarmPanelMu.Unlock()
	t.Cleanup(func() {
		swarmFloatMu.Lock()
		delete(swarmFloatForced, "t1")
		delete(swarmFloatPanelDismissed, "t1")
		delete(swarmFloatDismissedBees, "t1")
		swarmFloatMu.Unlock()
	})

	tt := ui.NewTester(func(c *ui.Context) { renderSwarmHeaderButton(c, s) }, 900, 600)
	clickSwarmButton := func() {
		tt.Frame()
		if err := tt.Click("编排"); err != nil {
			if keyErr := tt.Click("chat-header-swarm-btn"); keyErr != nil {
				t.Fatalf("点击 Swarm 按钮失败: %v", err)
			}
		}
	}

	clickSwarmButton()
	if !swarmFloatForcedFor("t1") {
		t.Fatal("没有 bee 时点击编排按钮应强制显示空团队面板")
	}

	clickSwarmButton()
	if swarmFloatForcedFor("t1") {
		t.Fatal("再次点击编排按钮应收起浮窗")
	}
}

// TestSwarmHeaderToggleHitArea 验证「编排」开关的命中区是 32×32，绘制直径仍是 ControlMD。
func TestSwarmHeaderToggleHitArea(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if swarmToggleSize != ControlMD {
		t.Fatalf("swarmToggleSize = %v, 期望 ControlMD(%v)", swarmToggleSize, ControlMD)
	}
	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t1"
	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.loadedFor = "t1"
	hp.setStateLocked(SwarmState{LeadThreadID: "t1", Available: true, Enabled: true})
	swarmPanelMu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) { renderSwarmHeaderButton(c, s) }, 900, 600)
	tt.Frame()
	rect, ok := tt.Find("编排")
	if !ok {
		t.Fatal("没有找到 Label 为「编排」的开关")
	}
	if rect.W != ControlLG || rect.H != ControlLG {
		t.Errorf("编排开关命中区 = %v×%v, 期望 %v×%v", rect.W, rect.H, ControlLG, ControlLG)
	}
}

// TestSwarmHeaderWaitingBadge 验证编排按钮的「等你回答」角标：有子线程等回答时显示等待数量。
func TestSwarmHeaderWaitingBadge(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t1"
	s.threads = []Thread{
		{ID: "h1", HasPendingRequest: true},
		{ID: "h2", HasPendingRequest: true},
		{ID: "h3"},
	}
	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.loadedFor = "t1"
	hp.setStateLocked(SwarmState{
		LeadThreadID: "t1", Available: true, Enabled: true,
		Bees: []SwarmBee{
			{ThreadID: "h1", Name: "Birch", Status: "completed"},
			{ThreadID: "h2", Name: "Isle", Status: "completed"},
			{ThreadID: "h3", Name: "Cove", Status: "completed"},
		},
	})
	swarmPanelMu.Unlock()
	t.Cleanup(func() {
		swarmFloatMu.Lock()
		delete(swarmFloatForced, "t1")
		delete(swarmFloatPanelDismissed, "t1")
		delete(swarmFloatDismissedBees, "t1")
		swarmFloatMu.Unlock()
	})

	tt := ui.NewTester(func(c *ui.Context) { renderSwarmHeaderButton(c, s) }, 900, 600)
	tt.Frame()

	// 三个 bee 都是终态，运行中计数为 0，因此文本 "2" 只可能来自等待角标。
	if !tt.HasText("2") {
		t.Fatalf("应显示等待回答的角标 2，实际文本：%v", tt.Texts())
	}
}

// TestSwarmHeaderReviewBadge 验证会话头显示「待审查 N」：统计 PendingMerge 的非 bee 子代理。
func TestSwarmHeaderReviewBadge(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fake.SetSwarmState("t1", SwarmState{
		LeadThreadID: "t1", Available: true, Enabled: true,
		Bees: []SwarmBee{
			{ThreadID: "h1", Name: "Birch", Status: "completed", PendingMerge: true},
		},
	})
	s := newChatState(fake)
	s.selectedThreadID = "t1"
	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.loadedFor = "t1"
	hp.setStateLocked(SwarmState{
		LeadThreadID: "t1", Available: true, Enabled: true,
		Bees: []SwarmBee{
			{ThreadID: "h1", Name: "Birch", Status: "completed", PendingMerge: true},
			{ThreadID: "h2", Name: "Isle", Status: "completed"},
		},
	})
	swarmPanelMu.Unlock()
	t.Cleanup(func() {
		swarmFloatMu.Lock()
		delete(swarmFloatForced, "t1")
		delete(swarmFloatPanelDismissed, "t1")
		delete(swarmFloatDismissedBees, "t1")
		swarmFloatMu.Unlock()
	})

	tt := ui.NewTester(func(c *ui.Context) { renderSwarmHeaderButton(c, s) }, 900, 600)
	tt.Frame()

	if !tt.HasText("待审查 1") {
		t.Fatalf("应显示「待审查 1」角标，实际文本：%v", tt.Texts())
	}
}

// U-009：一次瞬时加载失败只应记下错误，不能清空上次成功的编排状态——
// 否则编排开关与 lead 路由会被打回默认。
func TestLoadSwarmKeepsStateOnError(t *testing.T) {
	fake := NewFakeClient()
	fake.SetSwarmState("t1", SwarmState{
		LeadThreadID: "t1",
		Available:    true,
		Enabled:      true,
		Bees:         []SwarmBee{{ThreadID: "h1", Name: "子代理一", Status: "running"}},
	})
	s := newChatState(fake)
	s.selectedThreadID = "t1"
	t.Cleanup(func() { closeEmbeddedBees(s) })

	s.loadSwarm("t1")
	waitForAutoDormant(t, func() bool {
		hp := getSwarmPanelState(s)
		swarmPanelMu.Lock()
		defer swarmPanelMu.Unlock()
		return !hp.loading && hp.err == "" && len(hp.state.Bees) == 1
	})

	fake.SetSwarmError(errors.New("boom"))
	s.loadSwarm("t1")
	waitForAutoDormant(t, func() bool {
		hp := getSwarmPanelState(s)
		swarmPanelMu.Lock()
		defer swarmPanelMu.Unlock()
		return !hp.loading && hp.err != ""
	})

	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	st, errMsg := hp.state, hp.err
	swarmPanelMu.Unlock()
	if errMsg == "" {
		t.Fatal("加载失败时应记录错误信息")
	}
	if !st.Enabled || st.LeadThreadID != "t1" || len(st.Bees) != 1 {
		t.Fatalf("失败后应保留上次成功的编排状态，实际 Enabled=%v lead=%q bees=%d",
			st.Enabled, st.LeadThreadID, len(st.Bees))
	}
}
