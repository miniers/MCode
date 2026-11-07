package mcodeui

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// waitFor 辅助循环等待异步状态就绪并触发重绘帧。
// uiWaitTimeout 是 UI 测试等待条件成立的上限。
const uiWaitTimeout = 10 * time.Second

func waitFor(t *testing.T, tt *ui.Tester, cond func() bool) {
	t.Helper()
	// 条件满足即返回；上限放宽到 10s，避免全量并行测试负载高时偶发超时。
	deadline := time.Now().Add(uiWaitTimeout)
	for time.Now().Before(deadline) {
		tt.Frame()
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	tt.Frame()
	if !cond() {
		t.Fatalf("condition not met within deadline, current texts: %q", tt.Texts())
	}
}

// 场景 1：初始渲染能看到会话标题
func TestRootInitialRenderTitle(t *testing.T) {
	fake := NewFakeClient()
	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})
}

// 场景 2：点击头部“新建会话 ⌘N”进入待创建会话，输入首个字符后才调 CreateThread
func TestRootCreateThread(t *testing.T) {
	fake := NewFakeClient()
	var state *chatState
	tt := ui.NewTester(Root(fake, func(s *chatState) { state = s }), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	if err := tt.Click("新建会话 ⌘N"); err != nil {
		t.Fatalf("click '新建会话 ⌘N' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.pendingThread != nil
	})
	if got := fake.CreateThreadCalls(); got != 0 {
		t.Fatalf("点击新建会话不应立即创建会话，got %d", got)
	}

	// 输入首个字符才真正创建会话。
	tt.Frame()
	tt.Type("你好")
	waitFor(t, tt, func() bool {
		return fake.CreateThreadCalls() > 0
	})
}

// 场景 3：在输入框中 Type 文字后按 Enter，会调用 Send 并带上正确的文本
func TestRootSendOnEnter(t *testing.T) {
	fake := NewFakeClient()
	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	tt.Type("hello from mcode")
	tt.Key(0, ui.KeyEnter)

	waitFor(t, tt, func() bool {
		return fake.LastSentText() == "hello from mcode"
	})
}

// 场景 4：Compose 组字状态下按 Enter 不发送
func TestRootComposeEnterDoesNotSend(t *testing.T) {
	fake := NewFakeClient()
	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	fake.ResetCalls()

	// 模拟输入法组字输入
	tt.Compose("ni", 2)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()

	if fake.SendCalls() != 0 {
		t.Fatalf("expected 0 send calls during composing, got %d", fake.SendCalls())
	}
}

// 场景 5：触发 onApprovals 后能看到审批按钮，点击“允许”后调用 Respond，且 optionID 为对应值
func TestRootApprovalRespond(t *testing.T) {
	fake := NewFakeClient()
	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	appr := Approval{
		ThreadID:  "t1",
		RequestID: "req-100",
		Title:     "权限申请",
		Detail:    "需要执行外部终端命令",
		Options: []ApprovalOption{
			{ID: "opt-allow", Title: "允许", Role: "approve"},
			{ID: "opt-decline", Title: "拒绝", Role: "decline"},
		},
	}
	fake.TriggerApprovals("t1", []Approval{appr})

	waitFor(t, tt, func() bool {
		return tt.HasText("允许")
	})

	if err := tt.Click("允许"); err != nil {
		t.Fatalf("click '允许' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return fake.LastRespondOptionID() == "opt-allow"
	})
}

// 场景 6：触发 onUpdate 追加 assistant 消息后文本可见
func TestRootOnUpdateAssistantMessage(t *testing.T) {
	fake := NewFakeClient()
	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	replyText := "这是助手的新回复消息"
	update := ThreadUpdate{
		ThreadID: "t1",
		Messages: []Message{
			{
				ID:        "ast-1",
				Role:      "assistant",
				Kind:      "text",
				Text:      replyText,
				IsFinal:   true,
				TurnIndex: 1,
			},
		},
	}
	fake.TriggerUpdate(update)

	waitFor(t, tt, func() bool {
		return tt.HasText(replyText)
	})
}

// 场景 7：流式接收中文文本时按字符逐步展示（非整段一次性出现）
func TestRootStreamingChineseRevealsPartially(t *testing.T) {
	fake := NewFakeClient()
	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	chineseText := "秋天是一个收获的季节田野里金黄的稻谷随风起伏"
	update := ThreadUpdate{
		ThreadID: "t1",
		Thread: Thread{
			ID:        "t1",
			ProjectID: "p1",
			Title:     "测试会话1",
			IsRunning: true,
			UpdatedAt: time.Now(),
		},
		Messages: []Message{
			{
				ID:        "ast-stream-1",
				Role:      "assistant",
				Kind:      "text",
				Text:      chineseText,
				IsFinal:   false,
				TurnIndex: 1,
			},
		},
	}
	fake.TriggerUpdate(update)
	tt.Frame()

	tt.Advance(100 * time.Millisecond)

	if !tt.HasText("秋天") {
		t.Fatalf("expected prefix '秋天' to be visible at 100ms, current texts: %q", tt.Texts())
	}
	if tt.HasText(chineseText) {
		t.Fatalf("expected text to reveal partially at 100ms, but full text is already visible: %q", tt.Texts())
	}

	tt.Advance(500 * time.Millisecond)

	if !tt.HasText(chineseText) {
		t.Fatalf("expected full text %q to be visible after advance, current texts: %q", chineseText, tt.Texts())
	}
}

// 场景 8：历史消息（未运行会话）即使 IsFinal 为 false 也立即全部可见，不带逐字动画
func TestRootHistoryMessagesNoReveal(t *testing.T) {
	fake := NewFakeClient()
	historyText := "这是历史记录中一条未完成的正文内容包含二十个以上的无空格汉字测试立即显示"
	fake.messages["t1"] = []Message{
		{
			ID:        "ast-hist-1",
			Role:      "assistant",
			Kind:      "text",
			Text:      historyText,
			IsFinal:   false,
			TurnIndex: 1,
		},
	}

	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1") && tt.HasText(historyText)
	})

	// 不推进时钟（或仅推进 1ms），断言整段文字立即可见
	tt.Advance(time.Millisecond)
	if !tt.HasText(historyText) {
		t.Fatalf("expected full history text %q to be immediately visible without advancing clock, current texts: %q", historyText, tt.Texts())
	}
}

// 场景 9：运行中的会话显示“停止”按钮，点击后调用 Interrupt，停止后按钮消失
func TestRootStopButton(t *testing.T) {
	fake := NewFakeClient()
	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	// 用 onUpdate 推送 IsRunning 为 true 的会话
	updateRunning := ThreadUpdate{
		ThreadID: "t1",
		Thread: Thread{
			ID:        "t1",
			ProjectID: "p1",
			Title:     "测试会话1",
			IsRunning: true,
			UpdatedAt: time.Now(),
		},
	}
	fake.TriggerUpdate(updateRunning)

	// 界面上能看到“停止”
	waitFor(t, tt, func() bool {
		return tt.HasText("停止")
	})

	// 点击后假 Client 收到带正确 threadID 的 Interrupt 调用
	if err := tt.Click("停止"); err != nil {
		t.Fatalf("click '停止' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return fake.LastInterruptThreadID() == "t1"
	})

	// 推送 IsRunning 为 false 后，“停止”消失
	updateStopped := ThreadUpdate{
		ThreadID: "t1",
		Thread: Thread{
			ID:        "t1",
			ProjectID: "p1",
			Title:     "测试会话1",
			IsRunning: false,
			UpdatedAt: time.Now(),
		},
	}
	fake.TriggerUpdate(updateStopped)

	waitFor(t, tt, func() bool {
		return !tt.HasText("停止")
	})
}

// 场景 8：多项目：两个项目的名称和各自的会话标题都可见
func TestRootMultipleProjectsAndThreadsVisible(t *testing.T) {
	fake := NewFakeClient()
	now := time.Now()
	fake.SetProjectsAndThreads(
		[]Project{
			{ID: "p1", Name: "项目Alpha", Path: "/path/to/alpha"},
			{ID: "p2", Name: "项目Beta", Path: "/path/to/beta"},
		},
		map[string][]Thread{
			"p1": {{ID: "t1", ProjectID: "p1", Title: "会话Alpha1", UpdatedAt: now}},
			"p2": {{ID: "t2", ProjectID: "p2", Title: "会话Beta1", UpdatedAt: now.Add(-time.Minute)}},
		},
	)

	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("项目Alpha") && tt.HasText("会话Alpha1") &&
			tt.HasText("项目Beta") && tt.HasText("会话Beta1")
	})
}

// 场景 9：点击某个项目的“+”后进入该项目的待创建会话，输入后 CreateThread 收到该项目 ID
func TestRootCreateThreadForProject(t *testing.T) {
	fake := NewFakeClient()
	now := time.Now()
	fake.SetProjectsAndThreads(
		[]Project{
			{ID: "p1", Name: "项目Alpha", Path: "/path/to/alpha"},
			{ID: "p2", Name: "项目Beta", Path: "/path/to/beta"},
		},
		map[string][]Thread{
			"p1": {{ID: "t1", ProjectID: "p1", Title: "会话Alpha1", UpdatedAt: now}},
			"p2": {{ID: "t2", ProjectID: "p2", Title: "会话Beta1", UpdatedAt: now.Add(-time.Minute)}},
		},
	)

	state, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("项目Alpha") && tt.HasText("项目Beta")
	})

	// 1. 点击第一个项目的加号按钮
	if err := tt.Click("在此项目中新建会话"); err != nil {
		t.Fatalf("click '在此项目中新建会话' failed: %v", err)
	}

	pendingProject := func() string {
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.pendingThread == nil {
			return ""
		}
		return state.pendingThread.ProjectID
	}
	waitFor(t, tt, func() bool { return pendingProject() == "p1" })
	if got := fake.CreateThreadCalls(); got != 0 {
		t.Fatalf("待创建会话不应立即创建，got %d", got)
	}

	// 2. 点击项目Beta 同行的加号按钮
	betaBox, ok := tt.Find("项目Beta")
	if !ok {
		t.Fatalf("element for '项目Beta' not found")
	}
	tt.ClickAt(SidebarDefaultWidth-ListInset-RowHorizontalPadding-11, betaBox.Y+betaBox.H/2)

	waitFor(t, tt, func() bool { return pendingProject() == "p2" })

	// 3. 输入触发创建，CreateThread 收到待创建会话的项目 ID
	state.noteComposerDraft("", "你好")
	waitFor(t, tt, func() bool {
		return fake.LastCreatedProjectID() == "p2"
	})
}

// 场景 10：通过注入的假对话框“添加项目”后，AddProject 收到对应路径，新项目名可见
func TestRootAddProjectWithDialog(t *testing.T) {
	fake := NewFakeClient()
	fakePath := "/Users/test/new-awesome-project"
	var dialogInvoked atomic.Bool

	state, view := buildRoot(fake, WithPickFolder(func() (string, bool) {
		dialogInvoked.Store(true)
		return fakePath, true
	}))

	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	if err := tt.Click("添加项目"); err != nil {
		t.Fatalf("click '添加项目' failed: %v", err)
	}
	tt.Frame()
	// 添加项目入口先展开浮层，选择文件夹项才触发选择对话框
	if err := tt.Click("选择文件夹…"); err != nil {
		t.Fatalf("click '选择文件夹…' failed: %v", err)
	}

	waitFor(t, tt, dialogInvoked.Load)

	// AddProject 收到对应路径
	waitFor(t, tt, func() bool {
		return fake.HasAddProjectCall(fakePath)
	})

	// 新项目名可见
	waitFor(t, tt, func() bool {
		return tt.HasText("new-awesome-project")
	})

	// 添加后右侧默认打开新项目的待创建会话
	addedProjectID := func() string {
		state.mu.Lock()
		defer state.mu.Unlock()
		for _, p := range state.projects {
			if p.Path == fakePath {
				return p.ID
			}
		}
		return ""
	}
	pendingProjectID := func() string {
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.pendingThread == nil {
			return ""
		}
		return state.pendingThread.ProjectID
	}
	waitFor(t, tt, func() bool {
		id := addedProjectID()
		return id != "" && pendingProjectID() == id
	})
}

// 场景 11：多 provider 支持：在未手动选择时新建会话使用默认第一个可用 provider
func TestRootMultiProvidersSelectionAndDisplay(t *testing.T) {
	fake := NewFakeClient()
	now := time.Now()
	fake.SetProviders([]ProviderInfo{
		{ID: "claude", Name: "Claude", Available: true},
		{ID: "codex", Name: "Codex", Available: true},
	})
	fake.SetProjectsAndThreads(
		[]Project{
			{ID: "p1", Name: "项目Alpha", Path: "/path/to/alpha"},
		},
		map[string][]Thread{
			"p1": {{ID: "t1", ProjectID: "p1", Title: "会话Alpha1", Provider: "claude", UpdatedAt: now}},
		},
	)

	state, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)

	// 1. 界面上能看到会话标题
	waitFor(t, tt, func() bool {
		return tt.HasText("会话Alpha1")
	})

	// 2. 点击项目的加号按钮新建会话（进入待创建会话）
	if err := tt.Click("在此项目中新建会话"); err != nil {
		t.Fatalf("click '在此项目中新建会话' failed: %v", err)
	}

	// 3. 待创建会话的 provider 为默认第一个可用 provider ("claude")，创建时沿用它
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.pendingThread != nil && state.pendingThread.Options.Provider == "claude"
	})
	state.noteComposerDraft("", "你好")
	waitFor(t, tt, func() bool {
		return fake.LastCreatedProvider() == "claude"
	})
}

func TestFormatTokens(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "0"},
		{356, "356"},
		{999, "999"},
		{1000, "1.0k"},
		{1200, "1.2k"},
		{8400, "8.4k"},
		{12300, "12.3k"},
		{12345, "12.3k"},
	}

	for _, tc := range tests {
		got := formatTokens(tc.input)
		if got != tc.want {
			t.Errorf("formatTokens(%d) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestRootThreadUsageDisplay(t *testing.T) {
	fake := NewFakeClient()
	fake.messages["t1"] = []Message{
		{
			ID:        "u1",
			Role:      "user",
			Kind:      "text",
			Text:      "第一轮用户问题",
			TurnIndex: 1,
			IsFinal:   true,
		},
		{
			ID:        "a1",
			Role:      "assistant",
			Kind:      "text",
			Text:      "第一轮助手回答",
			TurnIndex: 1,
			IsFinal:   true,
		},
		{
			ID:        "u2",
			Role:      "user",
			Kind:      "text",
			Text:      "第二轮用户问题",
			TurnIndex: 2,
			IsFinal:   true,
		},
		{
			ID:        "a2",
			Role:      "assistant",
			Kind:      "text",
			Text:      "第二轮助手回答",
			TurnIndex: 2,
			IsFinal:   true,
		},
	}
	fake.SetThreadUsage("t1", ThreadUsage{
		Turns: map[int]TokenUsage{
			1: {Input: 1200, Output: 356, Cached: 8400},
			2: {Input: 2500, Output: 600, Cached: 0},
		},
		Total: TokenUsage{
			Input:  3700,
			Output: 956,
			Cached: 8400,
		},
	})

	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("已完成 · 输出 356") &&
			tt.HasText("已完成 · 输出 600") &&
			tt.HasText("会话统计")
	})
}

// TestSidebarAnimatedWidth 验证侧边栏过渡动画的宽度插值纯函数。
func TestSidebarAnimatedWidth(t *testing.T) {
	tests := []struct {
		name       string
		target     float32
		progress   float32
		collapsing bool
		want       float32
	}{
		{"折叠起点：保持完整宽度", 268, 0, true, 268},
		{"折叠一半：宽度减半", 268, 0.5, true, 134},
		{"折叠完成：宽度归零", 268, 1, true, 0},
		{"展开起点：宽度为零", 268, 0, false, 0},
		{"展开一半：宽度减半", 268, 0.5, false, 134},
		{"展开完成：恢复完整宽度", 268, 1, false, 268},
		{"进度下界钳位", 268, -1, false, 0},
		{"进度上界钳位", 268, 3, false, 268},
		{"目标宽度非正时归零", 0, 1, false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sidebarAnimatedWidth(tc.target, tc.progress, tc.collapsing); got != tc.want {
				t.Errorf("sidebarAnimatedWidth(%v, %v, %v) = %v, want %v",
					tc.target, tc.progress, tc.collapsing, got, tc.want)
			}
		})
	}
}

// TestRootSidebarSlideAnimation 验证侧边栏折叠/展开的过渡动画与动画结束后的静态布局切换。
func TestRootSidebarSlideAnimation(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	fake := NewFakeClient()
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	if _, ok := tt.Find("新建会话 ⌘N"); !ok {
		t.Fatalf("初始应显示新建会话按钮")
	}

	// ⌘B 折叠：动画开始时侧边栏内容仍按目标宽度渲染（只裁切，不消失）
	tt.Key(ui.Cmd, ui.KeyB)
	tt.Frame()
	if !state.sidebarCollapsed {
		t.Fatalf("⌘B 后 sidebarCollapsed 应为 true")
	}
	if _, ok := tt.Find("新建会话 ⌘N"); !ok {
		t.Fatalf("动画刚开始时侧边栏内容应保持渲染（裁切）")
	}

	// 推进约一半：显示进度应严格介于 0 与 1 之间
	tt.Advance(110 * time.Millisecond)
	if p := sidebarVisibleProgress(state); p <= 0.02 || p >= 0.98 {
		t.Fatalf("折叠动画中途显示进度应介于 0 与 1 之间, got %v", p)
	}
	if _, ok := tt.Find("测试会话1"); !ok {
		t.Fatalf("动画期间侧边栏内容仍应按目标宽度布局")
	}

	// 推进到动画结束：回到无 Sidebar 的静态布局
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if p := sidebarVisibleProgress(state); p > 0.02 {
		t.Fatalf("折叠动画结束后显示进度应为 0, got %v", p)
	}
	if _, ok := tt.Find("root-split"); ok {
		t.Fatalf("折叠结束后应回到只渲染主区的静态布局（无侧边栏分栏）")
	}
	// 侧栏收起后头部出现会话列表入口，作为侧栏内容被移除的正面校验。
	if _, ok := tt.Find("会话列表"); !ok {
		t.Fatalf("折叠结束后头部应显示会话列表入口")
	}

	// 再按一次 ⌘B 展开
	tt.Key(ui.Cmd, ui.KeyB)
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if p := sidebarVisibleProgress(state); p < 0.98 {
		t.Fatalf("展开动画结束后显示进度应为 1, got %v", p)
	}
	if _, ok := tt.Find("新建会话 ⌘N"); !ok {
		t.Fatalf("展开结束后应恢复侧边栏")
	}
}

// TestRootSidebarStatePersisted 验证切换侧边栏会把显隐状态写入 ui-state.json。
func TestRootSidebarStatePersisted(t *testing.T) {
	dataDir := settingsTestDir(t)
	t.Setenv("MCODE_DATA", dataDir)
	t.Cleanup(waitUIStateWrites)
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	state, view := buildRoot(fake, WithUIStatePersistence())
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })

	tt.Key(ui.Cmd, ui.KeyB)
	tt.Advance(400 * time.Millisecond)
	tt.Frame()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if loadUIState().SidebarHidden {
			return
		}
		tt.Frame()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("切换后应把 sidebar_hidden 写入 ui-state.json, got %+v", loadUIState())
}
