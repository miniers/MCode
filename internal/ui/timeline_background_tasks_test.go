package mcodeui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// backgroundTasksTestCommandLine 造一条命令类工具消息。
func backgroundTasksTestCommandLine(id string, turn int, status, line string) Message {
	return Message{
		ID:         id,
		Role:       "tool",
		ToolName:   "Bash",
		ToolInput:  fmt.Sprintf(`{"command":%q}`, line),
		ToolStatus: status,
		TurnIndex:  turn,
	}
}

// backgroundTasksTestCommand 造一条执行 npm run build 的工具消息。
func backgroundTasksTestCommand(id string, turn int, status string) Message {
	return backgroundTasksTestCommandLine(id, turn, status, "npm run build")
}

// backgroundTasksTestBackground 造一条 provider 托管的后台任务消息：90 秒前创建，仍在运行。
func backgroundTasksTestBackground(id string) Message {
	m := backgroundTasksTestCommand(id, 1, "background")
	m.CreatedAt = time.Now().Add(-90 * time.Second).Format(time.RFC3339)
	return m
}

// backgroundTasksTestBackgroundAt 造一条带描述、指定创建时间的 provider 托管后台任务。
func backgroundTasksTestBackgroundAt(id, description string, createdAt time.Time) Message {
	m := backgroundTasksTestBackground(id)
	m.ToolInput = fmt.Sprintf(`{"command":"npm run build","description":%q}`, description)
	m.CreatedAt = createdAt.Format(time.RFC3339)
	return m
}

// TestBackgroundTaskExpandAndStop 验证展开拉日志、收起，以及任务停止失败时保留行。
func TestBackgroundTaskExpandAndStop(t *testing.T) {
	client := NewFakeClient()
	client.backgroundTaskLogText = "日志内容"
	client.backgroundTaskLogAvailable = true
	client.stopBackgroundTaskErr = errors.New("停止失败")
	s := newChatState(client)
	s.selectedThreadID = "t1"
	s.composerView.backgroundTasksOpen = true
	data := backgroundTasksItem{threadID: "t1", tasks: []Message{backgroundTasksTestBackground("bg1")}, commands: []Message{backgroundTasksTestCommandLine("c1", 1, "running", "echo command")}}
	tt := ui.NewTester(func(c *ui.Context) { renderBackgroundTasksPanel(c, s, "tasks", data, nil) }, 800, 500)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
	tt.Frame()
	clickText(t, tt, "npm run build")
	tt.Frame()
	deadline := time.Now().Add(time.Second)
	for {
		client.mu.Lock()
		calls := append([]string(nil), client.backgroundTaskLogCalls...)
		client.mu.Unlock()
		if len(calls) > 0 {
			if calls[0] != "t1/bg1" {
				t.Fatalf("日志请求目标 = %v", calls)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("未请求后台任务日志")
		}
		time.Sleep(time.Millisecond)
	}
	clickText(t, tt, "npm run build")
	tt.Frame()
	s.mu.Lock()
	expanded := s.composerView.backgroundTaskExpanded
	s.mu.Unlock()
	if expanded != "" {
		t.Fatalf("再次点击应收起，实际 %q", expanded)
	}
	client.mu.Lock()
	before := len(client.backgroundTaskLogCalls)
	client.mu.Unlock()
	s.mu.Lock()
	s.composerView.backgroundTaskLogNext = time.Time{}
	s.mu.Unlock()
	tt.Frame()
	client.mu.Lock()
	after := len(client.backgroundTaskLogCalls)
	client.mu.Unlock()
	if after != before {
		t.Fatal("收起后不应继续请求日志")
	}
	tt.Move(0, 0)
	tt.Frame()
	if _, ok := tt.Find("停止"); !ok {
		t.Fatal("任务行未悬停时应显示停止按钮")
	}
	clickText(t, tt, "停止")
	tt.Frame()
	deadline = time.Now().Add(time.Second)
	for {
		client.mu.Lock()
		calls := append([]string(nil), client.stopBackgroundTaskCalls...)
		client.mu.Unlock()
		if len(calls) > 0 {
			if calls[0] != "t1/bg1" {
				t.Fatalf("停止请求目标 = %v", calls)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("未请求停止后台任务")
		}
		time.Sleep(time.Millisecond)
	}
	for {
		s.mu.Lock()
		toast := s.toastMessage
		expanded = s.composerView.backgroundTaskExpanded
		s.mu.Unlock()
		if toast != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("停止失败未提示")
		}
		time.Sleep(time.Millisecond)
	}
	if expanded != "" {
		t.Fatal("点击停止不应展开任务")
	}
	tt.Frame()
	if _, ok := tt.Find("npm run build"); !ok {
		t.Fatal("停止失败应保留任务行")
	}
	commandOnly := ui.NewTester(func(c *ui.Context) {
		renderBackgroundTasksPanel(c, s, "commands", backgroundTasksItem{threadID: "t1", commands: data.commands}, nil)
	}, 800, 500)
	commandOnly.Frame()
	command, ok := commandOnly.Find("echo command")
	if !ok {
		t.Fatal("找不到命令行")
	}
	commandOnly.Move(command.X+command.W/2, command.Y+command.H/2)
	commandOnly.Frame()
	if _, ok := commandOnly.Find("停止"); ok {
		t.Fatal("命令行不应有停止按钮")
	}
}

// TestBackgroundTaskSingleLogScroll 验证切换任务时只有当前任务绑定日志滚动句柄。
func TestBackgroundTaskSingleLogScroll(t *testing.T) {
	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t1"
	s.composerView.backgroundTasksOpen = true
	data := backgroundTasksItem{threadID: "t1", tasks: []Message{
		backgroundTasksTestCommandLine("bg1", 1, "background", "first task"),
		backgroundTasksTestCommandLine("bg2", 1, "background", "second task"),
	}}
	tt := ui.NewTester(func(c *ui.Context) { renderBackgroundTasksPanel(c, s, "tasks", data, nil) }, 800, 500)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
	tt.Frame()
	clickText(t, tt, "first task")
	tt.Frame()
	if _, ok := tt.Find("后台任务日志 bg1"); !ok {
		t.Fatal("展开的任务应有日志滚动区")
	}
	if _, ok := tt.Find("后台任务日志 bg2"); ok {
		t.Fatal("收起的任务不应有日志滚动区")
	}
	clickText(t, tt, "second task")
	tt.Frame()
	if _, ok := tt.Find("后台任务日志 bg1"); ok {
		t.Fatal("切换任务后旧任务不应占用日志滚动句柄")
	}
	if _, ok := tt.Find("后台任务日志 bg2"); !ok {
		t.Fatal("切换后应显示新任务的日志滚动区")
	}
}

// TestBackgroundRunningCommands 验证后台命令口径：
// 回合还在跑的命令交给运行中工具组，已结束的命令不算后台，回合结束后仍在跑的才算；
// provider 托管的后台任务交给卡片里的 tasks 一支，不在这里重复。
func TestBackgroundRunningCommands(t *testing.T) {
	cases := []struct {
		name          string
		messages      []Message
		threadRunning bool
		runningTurn   int
		wantIDs       []string
	}{
		{
			name:          "回合未结束的运行中命令不计入",
			messages:      []Message{backgroundTasksTestCommand("c1", 2, "running")},
			threadRunning: true,
			runningTurn:   2,
			wantIDs:       nil,
		},
		{
			name: "回合已结束时早先回合的运行中命令计入",
			messages: []Message{
				backgroundTasksTestCommand("c1", 1, "running"),
				backgroundTasksTestCommand("c2", 2, "running"),
			},
			threadRunning: true,
			runningTurn:   2,
			wantIDs:       []string{"c1"},
		},
		{
			name:          "已结束命令不计入",
			messages:      []Message{backgroundTasksTestCommand("c1", 1, "completed")},
			threadRunning: false,
			wantIDs:       nil,
		},
		{
			name:          "回合结束后仍在跑的命令计入",
			messages:      []Message{backgroundTasksTestCommand("c1", 1, "running")},
			threadRunning: false,
			wantIDs:       []string{"c1"},
		},
		{
			name: "非命令工具即使运行中也不计入",
			messages: []Message{{
				ID:         "r1",
				Role:       "tool",
				ToolName:   "Read",
				ToolInput:  `{"file_path":"/workspace/main.go"}`,
				ToolStatus: "running",
				TurnIndex:  1,
			}},
			threadRunning: false,
			wantIDs:       nil,
		},
		{
			name:          "没有状态字段的命令工具按运行中处理",
			messages:      []Message{backgroundTasksTestCommand("c1", 1, "")},
			threadRunning: false,
			wantIDs:       []string{"c1"},
		},
		{
			name:          "provider 托管的后台任务不算后台命令",
			messages:      []Message{backgroundTasksTestBackground("bg1")},
			threadRunning: false,
			wantIDs:       nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := backgroundRunningCommands(tc.messages, tc.threadRunning, tc.runningTurn)
			var gotIDs []string
			for _, m := range got {
				gotIDs = append(gotIDs, m.ID)
			}
			if strings.Join(gotIDs, ",") != strings.Join(tc.wantIDs, ",") {
				t.Fatalf("backgroundRunningCommands() = %v, want %v", gotIDs, tc.wantIDs)
			}
		})
	}
}

// TestBackgroundProviderTasks 验证 tasks 一支的口径：只看 ToolStatus=background，
// 不限工具类别、不看会话是否在跑、不按回合过滤。
func TestBackgroundProviderTasks(t *testing.T) {
	readBackground := Message{
		ID:         "bg-read",
		Role:       "tool",
		ToolName:   "Read",
		ToolInput:  `{"file_path":"/workspace/main.go"}`,
		ToolStatus: "background",
		TurnIndex:  3,
	}
	messages := []Message{
		backgroundTasksTestBackground("bg1"),
		readBackground,
		backgroundTasksTestCommand("c1", 1, "running"),
		backgroundTasksTestCommand("c2", 1, "completed"),
	}
	got := backgroundProviderTasks(messages)
	var ids []string
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "bg1,bg-read" {
		t.Fatalf("backgroundProviderTasks() = %v, want [bg1 bg-read]", ids)
	}
}

// TestBackgroundRunningBees 验证子智能体口径：只统计 running，排队与已结束都不算「在动」。
func TestBackgroundRunningBees(t *testing.T) {
	bees := []SwarmBee{
		{ThreadID: "b1", Name: "Aspen", Status: "running"},
		{ThreadID: "b2", Name: "Birch", Status: "queued"},
		{ThreadID: "b3", Name: "Cove", Status: "completed"},
		{ThreadID: "b4", Name: "Dune", Status: "running"},
	}
	got := backgroundRunningBees(bees)
	var ids []string
	for _, h := range got {
		ids = append(ids, h.ThreadID)
	}
	if strings.Join(ids, ",") != "b1,b4" {
		t.Fatalf("backgroundRunningBees() = %v, want [b1 b4]", ids)
	}
}

// TestBackgroundTasksTitle 验证卡片标题：三类合计个数，总数为 0 时返回空串。
func TestBackgroundTasksTitle(t *testing.T) {
	cases := []struct {
		name     string
		tasks    int
		commands int
		bees     int
		want     string
	}{
		{"只有后台任务", 1, 0, 0, "后台任务 · 1 个运行中"},
		{"只有命令", 0, 1, 0, "后台任务 · 1 个运行中"},
		{"只有子智能体", 0, 0, 1, "后台任务 · 1 个运行中"},
		{"三类都有", 1, 1, 1, "后台任务 · 3 个运行中"},
		{"多命令多子智能体", 2, 3, 2, "后台任务 · 7 个运行中"},
		{"都为 0", 0, 0, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := backgroundTasksTitle(tc.tasks, tc.commands, tc.bees); got != tc.want {
				t.Fatalf("backgroundTasksTitle(%d, %d, %d) = %q, want %q", tc.tasks, tc.commands, tc.bees, got, tc.want)
			}
		})
	}
}

// TestBackgroundTasksStatusText 验证胶囊实时状态文案：时长按 formatToolRunDuration，
// hasStart 为假时省略时长，more 为 0 时省略「+N」，超过 24 个字符按 rune 截断加省略号。
func TestBackgroundTasksStatusText(t *testing.T) {
	cases := []struct {
		name     string
		task     string
		elapsed  time.Duration
		hasStart bool
		more     int
		want     string
	}{
		{"时长与剩余个数", "运行 ui 包全量测试", 36 * time.Second, true, 1, "运行 ui 包全量测试 · 36s +1"},
		{"缺开始时间", "拉取依赖", time.Minute, false, 0, "拉取依赖"},
		{"有时长无剩余", "拉取依赖", 3*time.Minute + 5*time.Second, true, 0, "拉取依赖 · 3m05s"},
		{"负时长按 0", "拉取依赖", -time.Second, true, 0, "拉取依赖 · 0s"},
		{
			"超长截断",
			"运行全部后端与前端的端到端集成测试套件并修复失败用例",
			0, false, 2,
			"运行全部后端与前端的端到端集成测试套件并修复失败… +2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := backgroundTasksStatusText(tc.task, tc.elapsed, tc.hasStart, tc.more); got != tc.want {
				t.Fatalf("backgroundTasksStatusText() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBackgroundTasksLead 验证胶囊取「最早开始」的后台项：三类混合时比较各自时间戳，
// 缺时间戳的项排最后，全缺时退回第一项且 start 为零值。
func TestBackgroundTasksLead(t *testing.T) {
	base := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	taskWith := func(id, description string, created *time.Time) Message {
		m := backgroundTasksTestCommand(id, 1, "background")
		m.ToolInput = fmt.Sprintf(`{"command":"go test ./...","description":%q}`, description)
		if created != nil {
			m.CreatedAt = created.Format(time.RFC3339)
		}
		return m
	}
	timed := func(offset time.Duration) *time.Time {
		at := base.Add(offset)
		return &at
	}

	t.Run("三类混合取最早", func(t *testing.T) {
		it := backgroundTasksItem{
			tasks:    []Message{taskWith("bg1", "整理结果", timed(10*time.Second))},
			commands: []Message{taskWith("c1", "运行 ui 包全量测试", timed(2*time.Second))},
			bees:     []SwarmBee{{Name: "Aspen", StartedAt: base.Add(5 * time.Second)}},
		}
		name, start, ok := it.lead()
		if !ok || name != "运行 ui 包全量测试" {
			t.Fatalf("lead() = %q, %v, want 运行 ui 包全量测试", name, ok)
		}
		if !start.Equal(base.Add(2 * time.Second)) {
			t.Fatalf("lead start = %v, want %v", start, base.Add(2*time.Second))
		}
	})

	t.Run("子智能体最早", func(t *testing.T) {
		it := backgroundTasksItem{
			tasks: []Message{taskWith("bg1", "整理结果", timed(10*time.Second))},
			bees:  []SwarmBee{{Name: "Aspen", StartedAt: base.Add(1 * time.Second)}},
		}
		name, _, _ := it.lead()
		if name != "Aspen" {
			t.Fatalf("lead() name = %q, want Aspen", name)
		}
	})

	t.Run("缺时间戳排最后", func(t *testing.T) {
		it := backgroundTasksItem{
			tasks:    []Message{taskWith("bg1", "没有时间的任务", nil)},
			commands: []Message{taskWith("c1", "运行 ui 包全量测试", timed(-time.Minute))},
		}
		name, _, _ := it.lead()
		if name != "运行 ui 包全量测试" {
			t.Fatalf("lead() name = %q, want 运行 ui 包全量测试", name)
		}
	})

	t.Run("全部缺时间取第一项", func(t *testing.T) {
		it := backgroundTasksItem{
			tasks: []Message{taskWith("bg1", "没有时间的任务", nil)},
			bees:  []SwarmBee{{Name: "Aspen"}},
		}
		name, start, ok := it.lead()
		if !ok || name != "没有时间的任务" {
			t.Fatalf("lead() = %q, %v, want 没有时间的任务", name, ok)
		}
		if !start.IsZero() {
			t.Fatalf("lead start = %v, want 零值", start)
		}
	})

	t.Run("没有后台项", func(t *testing.T) {
		if _, _, ok := (backgroundTasksItem{}).lead(); ok {
			t.Fatalf("空后台项时 lead() 的 ok 应为 false")
		}
	})
}

// TestRenderBackgroundTasksPanelCard 验证 Tester 渲染：浮层面板始终展开，
// 直接列出后台任务、命令与 bee，并有续跑提示与扫光；没有后台项时面板不渲染。
func TestRenderBackgroundTasksPanelCard(t *testing.T) {
	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t1"
	data := backgroundTasksItem{
		threadID: "t1",
		leadID:   "lead-bg",
		tasks:    []Message{backgroundTasksTestBackground("bg1")},
		commands: []Message{backgroundTasksTestCommand("c1", 1, "running")},
		bees:     []SwarmBee{{ThreadID: "b1", Name: "Birch", Status: "running"}},
	}
	title := "后台任务 · 3 个运行中"

	tt := ui.NewTester(func(c *ui.Context) {
		renderBackgroundTasksPanel(c, s, "background-tasks-t1", data, nil)
	}, 800, 500)
	for i := 0; i < 3; i++ {
		tt.Frame()
	}

	if !tt.HasText(title) {
		t.Fatalf("缺少后台任务面板标题，当前文本 %v", tt.Texts())
	}
	// 表头单独显示「后台任务」与计数胶囊，标题 Label 之外也能看到这两段可见文本。
	if !tt.HasText("后台任务") || !tt.HasText("3 个运行中") {
		t.Fatalf("表头应显示标题与计数胶囊，当前文本 %v", tt.Texts())
	}
	if _, ok := tt.Find(backgroundTasksShimmerLabel); !ok {
		t.Fatalf("有项在跑时应渲染扫光条，当前文本 %v", tt.Texts())
	}
	// 面板体始终展开：不点任何东西就能看到条目、实时时长与续跑提示。
	if !tt.HasText("npm run build") {
		t.Fatalf("面板应直接列出命令短标题，当前文本 %v", tt.Texts())
	}
	// 面板里的条目只显示实时时长（90 秒前创建，渲染成 1m30s 这类文本），没有「后台 ·」前缀。
	// 允许测试执行耗时让秒数多走 1~2 秒，只校验分钟数与秒数后缀。
	gotDuration := false
	for _, text := range tt.Texts() {
		if strings.HasPrefix(text, "1m") && strings.HasSuffix(text, "s") {
			gotDuration = true
			break
		}
	}
	if !gotDuration {
		t.Fatalf("面板里的后台任务应显示实时运行时长，当前文本 %v", tt.Texts())
	}
	if !tt.HasText("Birch") || !tt.HasText("运行中") {
		t.Fatalf("面板应直接列出 bee 名字与状态，当前文本 %v", tt.Texts())
	}
	if tt.HasText("完成后 AI 会自动继续") {
		t.Fatalf("后台任务浮层不应显示续跑提示，当前文本 %v", tt.Texts())
	}

	// 没有后台项时面板不渲染，页面上不出现「后台」。
	empty := ui.NewTester(func(c *ui.Context) {
		renderBackgroundTasksPanel(c, s, "background-tasks-t1", backgroundTasksItem{threadID: "t1"}, nil)
	}, 800, 400)
	for i := 0; i < 3; i++ {
		empty.Frame()
	}
	if empty.HasText("后台") {
		t.Fatalf("没有后台项时不应渲染，当前文本 %v", empty.Texts())
	}
	if _, ok := empty.Find(backgroundTasksShimmerLabel); ok {
		t.Fatalf("没有后台项时不应渲染扫光条")
	}
}

// TestRenderBackgroundTasksPanelLimitsRows 验证条目超过 5 条时只列前 5 条，末尾给出「还有 N 个」。
func TestRenderBackgroundTasksPanelLimitsRows(t *testing.T) {
	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t1"
	var tasks []Message
	for i := 1; i <= 6; i++ {
		m := backgroundTasksTestCommandLine(fmt.Sprintf("bg%d", i), 1, "background", fmt.Sprintf("go test pkg%d", i))
		m.CreatedAt = time.Now().Add(-time.Duration(i) * time.Second).Format(time.RFC3339)
		tasks = append(tasks, m)
	}
	data := backgroundTasksItem{threadID: "t1", tasks: tasks}

	tt := ui.NewTester(func(c *ui.Context) {
		renderBackgroundTasksPanel(c, s, "background-tasks-t1", data, nil)
	}, 800, 600)
	for i := 0; i < 3; i++ {
		tt.Frame()
	}

	if !tt.HasText("后台任务 · 6 个运行中") {
		t.Fatalf("标题应显示全部 6 个，当前文本 %v", tt.Texts())
	}
	if !tt.HasText("pkg5") {
		t.Fatalf("前 5 条应完整列出，当前文本 %v", tt.Texts())
	}
	if tt.HasText("pkg6") {
		t.Fatalf("第 6 条不应单独列出，当前文本 %v", tt.Texts())
	}
	if !tt.HasText("还有 1 个") {
		t.Fatalf("超出条目应汇总为「还有 1 个」，当前文本 %v", tt.Texts())
	}
}

// TestRenderBackgroundTasksPanelBeeClickOpensFloat 验证点击 bee 行会走既有浮窗：
// 选中该子线程、记为手动打开，并回调 onNavigate 让输入区收起后台任务浮层。
func TestRenderBackgroundTasksPanelBeeClickOpensFloat(t *testing.T) {
	const leadID = "lead-bg-click"
	resetSwarmAutoHideFor(leadID)
	t.Cleanup(func() {
		resetSwarmAutoHideFor(leadID)
		swarmFloatMu.Lock()
		delete(swarmSelectedBeeID, leadID)
		delete(swarmPulseTime, leadID+"|b1")
		swarmFloatMu.Unlock()
	})

	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t1"
	data := backgroundTasksItem{
		threadID: "t1",
		leadID:   leadID,
		bees:     []SwarmBee{{ThreadID: "b1", Name: "Birch", Status: "running", Activity: "编写代码中"}},
	}

	navigated := false
	tt := ui.NewTester(func(c *ui.Context) {
		renderBackgroundTasksPanel(c, s, "background-tasks-t1", data, func() { navigated = true })
	}, 800, 400)
	tt.Frame()

	label := backgroundTasksBeeLabel("Birch")
	if !tt.HasText(label) {
		t.Fatalf("面板应直接显示 bee 行按钮 Label %q，当前文本 %v", label, tt.Texts())
	}
	if err := tt.Click(label); err != nil {
		t.Fatalf("点击 bee 行: %v", err)
	}
	tt.Frame()

	swarmFloatMu.Lock()
	selected := swarmSelectedBeeID[leadID]
	handled := swarmBeeAutoHideHandled[leadID]["b1"]
	swarmFloatMu.Unlock()
	if selected != "b1" || !handled {
		t.Fatalf("点击 bee 行后应打开并选中浮窗：selected=%q handled=%v", selected, handled)
	}
	if !navigated {
		t.Fatalf("点击 bee 行后应回调 onNavigate 关闭后台任务浮层")
	}
}

// TestRenderBackgroundTasksPanelBeeAlignedLeft 验证 bee 行内容靠左：
// 行内按钮默认会水平居中，这里断言名字的左边界紧贴行左边界内部，而不是漂在面板正中。
func TestRenderBackgroundTasksPanelBeeAlignedLeft(t *testing.T) {
	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t1"
	data := backgroundTasksItem{
		threadID: "t1",
		leadID:   "lead-bg-align",
		bees:     []SwarmBee{{ThreadID: "b1", Name: "Birch", Status: "running", Activity: "编写代码中"}},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		renderBackgroundTasksPanel(c, s, "background-tasks-t1", data, nil)
	}, 800, 400)
	for i := 0; i < 3; i++ {
		tt.Frame()
	}

	row, ok := tt.Find(backgroundTasksBeeLabel("Birch"))
	if !ok {
		t.Fatalf("找不到 bee 行，当前文本 %v", tt.Texts())
	}
	name, ok := tt.Find("Birch")
	if !ok {
		t.Fatalf("找不到 bee 名字，当前文本 %v", tt.Texts())
	}
	if offset := name.X - row.X; offset > 40 {
		t.Fatalf("bee 名字应靠左，实际距行左边界 %.1f 像素（行 X=%.1f，名字 X=%.1f）", offset, row.X, name.X)
	}
}

// TestRenderBackgroundTasksPanelReduceMotion 验证减少动效时不注册扫光动画，只显示静态标题。
func TestRenderBackgroundTasksPanelReduceMotion(t *testing.T) {
	setAppReduceMotion(t, true)
	s := newChatState(NewFakeClient())
	data := backgroundTasksItem{
		threadID: "t1",
		commands: []Message{backgroundTasksTestCommand("c1", 1, "running")},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		renderBackgroundTasksPanel(c, s, "background-tasks-t1", data, nil)
	}, 800, 400)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	if !tt.HasText("后台任务 · 1 个运行中") {
		t.Fatalf("减少动效时标题仍应显示，当前文本 %v", tt.Texts())
	}
	if _, ok := tt.Find(backgroundTasksShimmerLabel); ok {
		t.Fatalf("减少动效时不应渲染扫光条")
	}
}

// TestRenderBackgroundTasksPanelBeeShowsElapsed 验证 bee 行右侧显示运行时长：
// 90 秒前启动的 bee 渲染成「1m 30s」这类文本，减少动效时也能看到，不依赖扫光。
func TestRenderBackgroundTasksPanelBeeShowsElapsed(t *testing.T) {
	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t1"
	data := backgroundTasksItem{
		threadID: "t1",
		leadID:   "lead-bg-elapsed",
		bees: []SwarmBee{{
			ThreadID:  "b1",
			Name:      "Birch",
			Status:    "running",
			StartedAt: time.Now().Add(-90 * time.Second),
		}},
	}

	tt := ui.NewTester(func(c *ui.Context) {
		renderBackgroundTasksPanel(c, s, "background-tasks-t1", data, nil)
	}, 800, 400)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
	for i := 0; i < 3; i++ {
		tt.Frame()
	}

	// 按可见文字定位，不用 c.Key。
	if !tt.HasText("Birch") {
		t.Fatalf("面板应直接列出 bee 名字，当前文本 %v", tt.Texts())
	}
	// 允许测试执行耗时让秒数多走 1~2 秒，只校验分钟前缀与秒数后缀。
	gotElapsed := false
	for _, text := range tt.Texts() {
		if strings.HasPrefix(text, "1m") && strings.HasSuffix(text, "s") {
			gotElapsed = true
			break
		}
	}
	if !gotElapsed {
		t.Fatalf("bee 行应显示运行时长，当前文本 %v", tt.Texts())
	}
}

// TestTimelineBackgroundTaskCard 验证时间线不再出现后台任务卡片：
// ToolStatus=background 的工具消息只以工具行留在时间线里，后台任务改由输入区上方的实时胶囊展示，
// 点开胶囊才渲染浮层；时间线末尾只有一行等待条，详情在胶囊浮层里。
// 胶囊的 Label 仍是「后台任务 · 1 个运行中」，正文则是最早任务的短标题加计时。
func TestTimelineBackgroundTaskCard(t *testing.T) {
	base := []Message{
		{ID: "u1", Role: "user", Text: "跑一下构建", TurnIndex: 1, IsFinal: true},
		backgroundTasksTestBackground("bg1"),
		{ID: "a1", Role: "assistant", Kind: "text", Text: "已交给后台", TurnIndex: 1, IsFinal: true},
	}
	tt := newTimelineTester(t, base, "已交给后台")
	if _, ok := tt.Find("后台任务 · 1 个运行中"); !ok {
		t.Fatalf("后台任务应改由输入区胶囊呈现，当前文本 %v", tt.Texts())
	}
	// 胶囊正文以最早任务名开头，后面跟实时计时。
	gotLiveText := false
	for _, text := range tt.Texts() {
		if strings.HasPrefix(text, "npm run build · ") {
			gotLiveText = true
			break
		}
	}
	if !gotLiveText {
		t.Fatalf("胶囊正文应显示最早任务名与计时，当前文本 %v", tt.Texts())
	}
	// 时间线末尾应有且只有一行等待条，不再重复续跑提示。
	if _, ok := tt.Find("等待后台任务"); !ok {
		t.Fatalf("时间线末尾应显示等待后台任务条，当前文本 %v", tt.Texts())
	}
	gotWaitText := false
	for _, text := range tt.Texts() {
		if strings.HasPrefix(text, "等待 1 个后台任务") {
			gotWaitText = true
			break
		}
	}
	if !gotWaitText {
		t.Fatalf("等待条应显示后台任务数，当前文本 %v", tt.Texts())
	}
	// 浮层默认关闭，其表头计数胶囊「1 个运行中」与旧卡片都不应出现在可见文本里。
	if hasExactText(tt, "1 个运行中") {
		t.Fatalf("时间线末尾不应再出现后台任务卡片或浮层: %v", tt.Texts())
	}
	if tt.HasText("完成后 AI 会自动继续") {
		t.Fatalf("时间线不应再提示完成后自动继续: %v", tt.Texts())
	}

	// 命令已 completed：既没有卡片，也没有胶囊。
	completed := append([]Message(nil), base...)
	completed[1] = backgroundTasksTestCommand("bg1", 1, "completed")
	done := newTimelineTester(t, completed, "跑一下构建")
	if hasExactText(done, "1 个运行中") {
		t.Fatalf("状态变回 completed 后不应再出现后台任务卡片: %v", done.Texts())
	}
	if _, ok := done.Find("后台任务 · 1 个运行中"); ok {
		t.Fatalf("状态变回 completed 后不应再显示后台任务胶囊: %v", done.Texts())
	}
}

// TestTimelineBackgroundToolChip 验证 background 工具胶囊：不是运行中命令，
// 展开详情没有终止按钮，耗时显示「后台 · 已运行时长」，动词用进行时。
func TestTimelineBackgroundToolChip(t *testing.T) {
	m := backgroundTasksTestBackground("bg1")
	if !isToolBackground(m) {
		t.Fatalf("isToolBackground(%q) = false, want true", m.ToolStatus)
	}
	if isToolRunning(m) {
		t.Fatalf("background 不应被当成 running")
	}
	if toolIsRunningCommand(m) {
		t.Fatalf("background 命令不应算运行中命令（否则会冒出终止按钮与停滞徽标）")
	}
	if got := toolCallVerb("$", m); got != "$" {
		t.Fatalf("命令的动词应保持 $，got %q", got)
	}

	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t1"
	s.expandedTool[m.ID] = true
	tt := ui.NewTester(func(c *ui.Context) {
		renderSingleToolItem(c, s, m)
	}, 900, 500)
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	if !tt.HasText("后台 ·") {
		t.Fatalf("胶囊应显示后台运行时长，当前文本 %v", tt.Texts())
	}
	if tt.HasText("终止此命令") {
		t.Fatalf("background 工具不应出现终止按钮，当前文本 %v", tt.Texts())
	}
}

// TestToolCallVerbTenseBackground 验证读取类工具的 background 状态用进行时态。
func TestToolCallVerbTenseBackground(t *testing.T) {
	m := Message{ID: "bg-read", Role: "tool", ToolName: "Read", ToolStatus: "background", ToolInput: `{"file_path":"/workspace/main.go"}`}
	if got := toolCallVerb("读取", m); got != "正在读取" {
		t.Fatalf("background 读取工具动词 = %q, want 正在读取", got)
	}
}
