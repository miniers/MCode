package mcodeui

import (
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/testenv"
)

// todoToolMsg 构造一条待办工具消息（TodoWrite 的正文格式与时间线里的真实消息一致）。
func todoToolMsg(id, input string) Message {
	return Message{ID: id, Role: "tool", Kind: "tool", Text: "TodoWrite " + input, ToolInput: input}
}

// claudeTaskMsg 模拟工具名和输入摘要后追加工具结果的消息格式。
func claudeTaskMsg(name, input, output string) Message {
	text := name + " " + input
	if output != "" {
		text += "\n" + output
	}
	return Message{Role: "tool", Kind: "tool", Text: text, ToolName: name, ToolInput: input, ToolStatus: "completed"}
}

func TestCollapseTodoMessages(t *testing.T) {
	plan := func(id string) Message {
		m := todoToolMsg(id, `{"plan":[{"step":"做事","status":"pending"}]}`)
		m.ToolName = "update_plan"
		return m
	}
	t.Run("每轮保留最新整份清单", func(t *testing.T) {
		msgs := []Message{{Role: "user", ID: "u1"}, plan("p1"), plan("p2"), plan("p3"),
			{Role: "assistant", ID: "a1", Text: "完成"}, {Role: "user", ID: "u2"}, plan("p4"), plan("p5")}
		original := append([]Message(nil), msgs...)
		got := collapseTodoMessages(msgs)
		want := []Message{msgs[0], msgs[3], msgs[4], msgs[5], msgs[7]}
		if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(msgs, original) {
			t.Fatalf("过滤结果或入参不符: got=%+v 原始=%+v", got, msgs)
		}
	})
	t.Run("Claude 任务跨轮聚合", func(t *testing.T) {
		msgs := []Message{{Role: "user", ID: "u1"},
			claudeTaskMsg("TaskCreate", `{"subject":"一"}`, "Task #1 created"),
			claudeTaskMsg("taskcreate", `{"subject":"二"}`, "Task #2 created"),
			claudeTaskMsg("TaskCreate", `{"subject":"三"}`, "Task #3 created"),
			claudeTaskMsg("TaskUpdate", `{"taskId":"1","status":"completed"}`, ""),
			{Role: "user", ID: "u2"},
			claudeTaskMsg("taskupdate", `{"taskId":"2","status":"in_progress"}`, "")}
		msgs[4].ID, msgs[6].ID = "t1", "t2"
		original := append([]Message(nil), msgs...)
		got := collapseTodoMessages(msgs)
		if len(got) != 4 || got[1].ID != "t1" || got[3].ID != "t2" || !reflect.DeepEqual(msgs, original) {
			t.Fatalf("过滤结果或入参不符: %+v", got)
		}
		for i, want := range [][]todoItem{
			{{Text: "一", Status: "completed"}, {Text: "二", Status: "pending"}, {Text: "三", Status: "pending"}},
			{{Text: "一", Status: "completed"}, {Text: "二", Status: "in_progress"}, {Text: "三", Status: "pending"}},
		} {
			items, ok := parseTodoItems(got[1+i*2].ToolInput)
			if !ok || !reflect.DeepEqual(items, want) {
				t.Fatalf("第 %d 轮清单 = %+v", i+1, items)
			}
		}
	})
	t.Run("两类待办互不影响", func(t *testing.T) {
		msgs := []Message{plan("p1"), claudeTaskMsg("TaskCreate", `{"subject":"一"}`, ""), plan("p2"), claudeTaskMsg("TaskUpdate", `{"taskId":"1","status":"completed"}`, "")}
		got := collapseTodoMessages(msgs)
		if len(got) != 2 || got[0].ID != "p2" {
			t.Fatalf("过滤结果 = %+v", got)
		}
	})
}

func TestPlanPill_ClaudeTasks(t *testing.T) {
	msgs := []Message{
		{Role: "user", Text: "开工"},
		claudeTaskMsg("TaskCreate", `{"subject":"读代码","description":"阅读"}`, "Task #1 created successfully: 读代码"),
		claudeTaskMsg("taskcreate", `{"subject":"改代码","description":"修改"}`, "Task #2 created successfully: 改代码"),
		claudeTaskMsg("TaskCreate", `{"subject":"检查","description":"检查"}`, "Task #3 created successfully: 检查"),
		claudeTaskMsg("TaskUpdate", `{"taskId":"1","status":"completed"}`, "Updated task #1"),
	}
	view := buildPlanPillView(msgs, false)
	if !view.Visible || view.Done != 1 || view.Total != 3 || view.Current != "改代码" {
		t.Fatalf("unexpected view: %+v", view)
	}
	msgs = append(msgs, claudeTaskMsg("taskupdate", `{"taskId":"2","status":"deleted"}`, "Deleted task #2"))
	view = buildPlanPillView(msgs, false)
	if view.Done != 1 || view.Total != 2 || view.Items[1].Text != "检查" {
		t.Fatalf("unexpected view after deletion: %+v", view)
	}
	msgs = append(msgs, claudeTaskMsg("TaskUpdate", `{"taskId":"3","subject":"完成检查","status":"in_progress"}`, "Updated task #3"))
	view = buildPlanPillView(msgs, false)
	if view.Current != "完成检查" {
		t.Fatalf("unexpected view after update: %+v", view)
	}
	msgs = append(msgs, claudeTaskMsg("TaskUpdate", `{"taskId":"3","status":"completed"}`, "Updated task #3"))
	if shouldShowPlanPill(msgs, false) || shouldShowPlanPill(msgs, true) {
		t.Fatal("expected pill hidden when all Claude tasks are completed")
	}
}

func TestPlanPill_ClaudeTaskFallbackIDs(t *testing.T) {
	msgs := []Message{
		claudeTaskMsg("TaskCreate", `{"subject":"一","description":"第一项"}`, "创建成功"),
		claudeTaskMsg("TaskCreate", `{"subject":"二","description":"第二项"}`, "创建成功"),
		claudeTaskMsg("TaskUpdate", `{"taskId":"2","status":"completed"}`, "更新成功"),
	}
	view := buildPlanPillView(msgs, true)
	if !view.Visible || view.Done != 1 || view.Total != 2 || view.Items[1].Status != "completed" {
		t.Fatalf("unexpected view: %+v", view)
	}
}

func TestPlanPill_FullPlanAfterClaudeTasks(t *testing.T) {
	msgs := []Message{
		claudeTaskMsg("TaskCreate", `{"subject":"旧任务","description":"旧"}`, "Task #7 created successfully: 旧任务"),
		{Role: "tool", Kind: "tool", ToolName: "update_plan", ToolInput: `{"plan":[{"step":"新任务","status":"in_progress"}]}`, Text: `update_plan {"plan":[{"step":"新任务","status":"in_progress"}]}`},
	}
	items, idx := latestTodoPlan(msgs)
	if idx != 1 || len(items) != 1 || items[0] != (todoItem{Text: "新任务", Status: "in_progress"}) {
		t.Fatalf("unexpected plan: %v idx=%d", items, idx)
	}
}

// 没有待办消息时不显示胶囊，latestTodoPlan 也返回空。
func TestPlanPill_NoTodo(t *testing.T) {
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "帮我改一下输入框"},
		{ID: "a1", Role: "assistant", Text: "好的，我先读代码。"},
		{ID: "t1", Role: "tool", Kind: "tool", Text: "bash ls", ToolInput: `{"command":"ls"}`},
	}
	if items, idx := latestTodoPlan(msgs); len(items) != 0 || idx != -1 {
		t.Fatalf("expected no todo items, got %v idx=%d", items, idx)
	}
	if shouldShowPlanPill(msgs, true) {
		t.Fatal("expected pill hidden without todo list")
	}
}

// 最新的待办消息整份覆盖旧的清单。
func TestPlanPill_LatestOverridesOlder(t *testing.T) {
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "开工"},
		todoToolMsg("t1", `{"todos":[{"content":"读代码","status":"completed"},{"content":"改代码","status":"pending"}]}`),
		todoToolMsg("t2", `{"plan":[{"step":"改代码","status":"in_progress"},{"step":"跑测试","status":"pending"}]}`),
	}
	items, idx := latestTodoPlan(msgs)
	if idx != 2 {
		t.Fatalf("expected latest todo message at index 2, got %d", idx)
	}
	want := []todoItem{{Text: "改代码", Status: "in_progress"}, {Text: "跑测试", Status: "pending"}}
	if len(items) != len(want) {
		t.Fatalf("expected %d items, got %v", len(want), items)
	}
	for i := range want {
		if items[i] != want[i] {
			t.Fatalf("item %d = %+v, want %+v", i, items[i], want[i])
		}
	}
	view := buildPlanPillView(msgs, false)
	if !view.Visible || view.Done != 0 || view.Total != 2 || view.Current != "改代码" {
		t.Fatalf("unexpected view: %+v", view)
	}
}

// 待办全部完成时不显示胶囊（运行中也不显示）。
func TestPlanPill_AllCompletedHidden(t *testing.T) {
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "开工"},
		todoToolMsg("t1", `{"todos":[{"content":"读代码","status":"completed"},{"content":"改代码","status":"done"}]}`),
	}
	if shouldShowPlanPill(msgs, false) {
		t.Fatal("expected pill hidden when all todos completed (idle)")
	}
	if shouldShowPlanPill(msgs, true) {
		t.Fatal("expected pill hidden when all todos completed (running)")
	}
}

// 线程没在运行、且最新待办早于最后一条用户消息时视为上一轮的旧进度，不显示。
func TestPlanPill_StaleAfterUserMessage(t *testing.T) {
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "开工"},
		todoToolMsg("t1", `{"todos":[{"content":"读代码","status":"completed"},{"content":"改代码","status":"in_progress"}]}`),
		{ID: "a1", Role: "assistant", Text: "改完了。"},
		{ID: "u2", Role: "user", Text: "再改一处"},
	}
	if shouldShowPlanPill(msgs, false) {
		t.Fatal("expected pill hidden when todo predates the last user message")
	}
	// 同一份待办在运行中仍然显示。
	if !shouldShowPlanPill(msgs, true) {
		t.Fatal("expected pill shown while thread is running")
	}
	// 待办排在最后一条用户消息之后（这一轮的新进度）时，空闲也显示。
	msgs = append(msgs, todoToolMsg("t2", `{"todos":[{"content":"读代码","status":"completed"},{"content":"改代码","status":"pending"}]}`))
	if !shouldShowPlanPill(msgs, false) {
		t.Fatal("expected pill shown when todo comes after the last user message")
	}
	if view := buildPlanPillView(msgs, false); view.Done != 1 || view.Total != 2 || view.Current != "改代码" {
		t.Fatalf("view = %+v", view)
	}
}

// bee 汇报以 Role=user 写入主会话，但不算用户消息，不应让未完成的待办胶囊过期。
func TestPlanPill_SwarmReportNotStale(t *testing.T) {
	base := []Message{
		{ID: "u1", Role: "user", Text: "开工"},
		todoToolMsg("t1", `{"todos":[{"content":"读代码","status":"completed"},{"content":"改代码","status":"in_progress"}]}`),
	}
	withReport := append(append([]Message(nil), base...), Message{
		ID:   "r1",
		Role: "user",
		Text: "[编排] 子代理汇报：Aria 已结束工作。",
	})
	if !shouldShowPlanPill(withReport, false) {
		t.Fatal("expected pill shown when only a swarm report follows an unfinished todo plan")
	}
	if view := buildPlanPillView(withReport, false); !view.Visible {
		t.Fatalf("expected visible plan pill view after swarm report, got %+v", view)
	}

	withUser := append(append([]Message(nil), base...), Message{
		ID:   "u2",
		Role: "user",
		Text: "再改一处",
	})
	if shouldShowPlanPill(withUser, false) {
		t.Fatal("expected pill hidden when a real user message follows the todo plan")
	}
	if view := buildPlanPillView(withUser, false); view.Visible {
		t.Fatalf("expected hidden plan pill view after real user message, got %+v", view)
	}
}

// TestPlanPillRunningNoFramePending 验证运行中待办胶囊不再通过 c.After 持续请求整窗重建。
func TestPlanPillRunningNoFramePending(t *testing.T) {
	setAppReduceMotion(t, false)
	s := &chatState{
		threads:  []Thread{{ID: "thread", IsRunning: true}},
		messages: []Message{todoToolMsg("plan", `{"todos":[{"content":"进行中","status":"in_progress"}]}`)},
	}
	builds := 0
	tt := ui.NewTester(func(c *ui.Context) {
		builds++
		renderComposerPlanBar(c, s, "thread")
	}, 400, 100)
	tt.Frame()
	beforeBuilds := builds
	tt.Advance(time.Second)
	if builds != beforeBuilds {
		t.Errorf("待办胶囊不应重建组件树：%d → %d", beforeBuilds, builds)
	}
	if tt.FramePending() {
		t.Error("待办胶囊运行中不应持续请求下一帧")
	}
}

func TestPlanPillCurrentOps(t *testing.T) {
	testenv.TempHome(t)

	t.Run("只取当前项开始之后的工具调用并排除待办工具", func(t *testing.T) {
		msgs := []Message{
			{ID: "u1", Role: "user", Text: "开工"},
			todoToolMsg("p1", `{"todos":[{"content":"读代码","status":"in_progress"},{"content":"改代码","status":"pending"}]}`),
			{ID: "r0", Role: "tool", Kind: "tool", ToolName: "Read", ToolInput: `{"file_path":"/repo/before.go"}`, ToolStatus: "completed"},
			todoToolMsg("p2", `{"todos":[{"content":"读代码","status":"completed"},{"content":"改代码","status":"in_progress"}]}`),
			{ID: "r1", Role: "tool", Kind: "tool", ToolName: "Read", ToolInput: `{"file_path":"/repo/internal/ui/a.go"}`, ToolStatus: "completed"},
			{ID: "e1", Role: "tool", Kind: "tool", ToolName: "Edit", ToolInput: `{"file_path":"/repo/internal/ui/b.go"}`, ToolStatus: "completed"},
			{ID: "b1", Role: "tool", Kind: "tool", ToolName: "Bash", ToolInput: `{"command":"go vet ./internal/ui/","description":"静态检查"}`, ToolStatus: "completed"},
		}
		view := buildPlanPillView(msgs, true)
		// 只保留当前项开始之后最近的 1 条，之前的 Read/Edit 被挤掉。
		if len(view.CurrentOps) != 1 {
			t.Fatalf("CurrentOps 长度 = %d, want 1: %+v", len(view.CurrentOps), view.CurrentOps)
		}
		if view.CurrentOps[0].Name != "$" || view.CurrentOps[0].Target != "静态检查" {
			t.Fatalf("op[0] = %+v", view.CurrentOps[0])
		}

		// Claude 增量任务模式：当前项开始之后的 TaskCreate 等待办工具也必须被排除。
		claudeMsgs := []Message{
			{ID: "cu1", Role: "user", Text: "开工"},
			claudeTaskMsg("TaskCreate", `{"subject":"第一步"}`, "Task #1 created"),
			claudeTaskMsg("TaskCreate", `{"subject":"第二步"}`, "Task #2 created"),
			claudeTaskMsg("TaskUpdate", `{"taskId":"1","status":"in_progress"}`, "Updated task #1"),
			{ID: "cr0", Role: "tool", Kind: "tool", ToolName: "Read", ToolInput: `{"file_path":"/repo/old.go"}`, ToolStatus: "completed"},
			claudeTaskMsg("TaskUpdate", `{"taskId":"1","status":"completed"}`, "Updated task #1"),
			claudeTaskMsg("TaskUpdate", `{"taskId":"2","status":"in_progress"}`, "Updated task #2"),
			{ID: "cr1", Role: "tool", Kind: "tool", ToolName: "Read", ToolInput: `{"file_path":"/repo/cur1.go"}`, ToolStatus: "completed"},
			claudeTaskMsg("TaskCreate", `{"subject":"第三步"}`, "Task #3 created"),
			{ID: "ce1", Role: "tool", Kind: "tool", ToolName: "Edit", ToolInput: `{"file_path":"/repo/cur2.go"}`, ToolStatus: "completed"},
		}
		claudeView := buildPlanPillView(claudeMsgs, true)
		if len(claudeView.CurrentOps) != 1 || claudeView.CurrentOps[0].Target != "cur2.go" {
			t.Fatalf("Claude CurrentOps = %+v", claudeView.CurrentOps)
		}
	})

	t.Run("最多保留最近1条", func(t *testing.T) {
		msgs := []Message{
			{ID: "u1", Role: "user", Text: "开工"},
			todoToolMsg("p1", `{"todos":[{"content":"进行中","status":"in_progress"}]}`),
		}
		for i := 1; i <= 10; i++ {
			msgs = append(msgs, Message{
				ID:         fmt.Sprintf("r%02d", i),
				Role:       "tool",
				Kind:       "tool",
				ToolName:   "Read",
				ToolInput:  fmt.Sprintf(`{"file_path":"/repo/f%02d.go"}`, i),
				ToolStatus: "completed",
			})
		}
		view := buildPlanPillView(msgs, true)
		if len(view.CurrentOps) != 1 || view.CurrentOps[0].Target != "f10.go" {
			t.Fatalf("CurrentOps = %+v, want 仅 f10.go", view.CurrentOps)
		}
	})

	t.Run("缓存在messagesRev不变时不重算", func(t *testing.T) {
		s := newChatState(NewFakeClient())
		defer s.close()
		s.selectedThreadID = "t1"
		s.threads = []Thread{{ID: "t1", IsRunning: true}}
		s.messages = []Message{
			todoToolMsg("p1", `{"todos":[{"content":"进行中","status":"in_progress"},{"content":"后续","status":"pending"}]}`),
			{ID: "r1", Role: "tool", Kind: "tool", ToolName: "Read", ToolInput: `{"file_path":"/repo/a.go"}`, ToolStatus: "completed"},
		}
		s.messagesRev = 1

		before := planPillBuilds.Load()
		if _, ok := composerPlanView(s, "t1"); !ok {
			t.Fatal("首次构建应返回可见胶囊")
		}
		afterFirst := planPillBuilds.Load()
		if afterFirst != before+1 {
			t.Fatalf("首次构建计数 = %d, want %d", afterFirst, before+1)
		}
		for i := 0; i < 3; i++ {
			if _, ok := composerPlanView(s, "t1"); !ok {
				t.Fatal("缓存命中应返回可见胶囊")
			}
		}
		if got := planPillBuilds.Load(); got != afterFirst {
			t.Fatalf("messagesRev 不变时不应重算: want=%d got=%d", afterFirst, got)
		}

		s.mu.Lock()
		s.messages = append(s.messages, Message{
			ID:         "r2",
			Role:       "tool",
			Kind:       "tool",
			ToolName:   "Edit",
			ToolInput:  `{"file_path":"/repo/b.go"}`,
			ToolStatus: "completed",
		})
		s.messagesRev++
		s.mu.Unlock()

		if _, ok := composerPlanView(s, "t1"); !ok {
			t.Fatal("messagesRev 变更后应返回可见胶囊")
		}
		if got := planPillBuilds.Load(); got != afterFirst+1 {
			t.Fatalf("messagesRev 变更后应重算一次: want=%d got=%d", afterFirst+1, got)
		}
		if ops := s.planPillCache.CurrentOps; len(ops) != 1 || ops[0].Target != "b.go" {
			t.Fatalf("更新后 CurrentOps = %+v, want 仅 b.go", ops)
		}
	})
}

// planBarHarness 只渲染待办条和它下面的输入卡片占位，便于单独验证条的展开、收起与定时。
func planBarHarness(t *testing.T, s *chatState, reduce bool) *ui.Tester {
	t.Helper()
	setAppReduceMotion(t, reduce)
	tt := ui.NewTesterWith(func(c *ui.Context) {
		ui.Column(c.Key("plan-bar-host")).FillWidth().Gap(Space3).Children(func() {
			renderComposerPlanBar(c, s, "t1")
			ui.Box(c.Key("plan-bar-card")).Label("输入卡片").FillWidth().Height(60).Background(ThemeSurface(c))
		})
	}, 600, 500, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: reduce}})
	tt.Frame()
	return tt
}

// planBarState 造一个会话 t1，消息是一条用户消息加一份待办清单（以及可选的后续消息）。
func planBarState(running bool, todos string, after ...Message) *chatState {
	msgs := []Message{{ID: "u1", Role: "user", Text: "开工"}, todoToolMsg("p1", todos)}
	return &chatState{
		threads:  []Thread{{ID: "t1", IsRunning: running}},
		messages: append(msgs, after...),
	}
}

const planBarThreeSteps = `{"todos":[{"content":"第一步","status":"completed"},{"content":"第二步","status":"in_progress"},{"content":"第三步","status":"pending"}]}`

// 有未完成待办时条出现在输入卡片正上方，状态胶囊行里不再有待办胶囊。
func TestComposerPlanBarAboveCard(t *testing.T) {
	testenv.TempHome(t)
	s, view := buildRoot(NewFakeClient())
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })

	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.messages = []Message{
		{ID: "m1", Role: "user", Text: "开始", TurnIndex: 1},
		{ID: "m2", Role: "tool", ToolName: "todowrite", TurnIndex: 1, ToolInput: planBarThreeSteps},
		backgroundTasksTestBackground("m3"),
	}
	s.messagesRev++
	seedCapsuleThread(s, false, "")
	s.mu.Unlock()
	s.invalidate()

	waitFor(t, tt, func() bool { _, ok := tt.Find("待办进度 1/3"); return ok })
	head, _ := tt.Find("待办进度 1/3")
	card, ok := tt.Find("消息输入框")
	if !ok {
		t.Fatalf("找不到输入卡片: %v", tt.Texts())
	}
	if head.Y >= card.Y || head.Y+head.H > card.Y+1 || card.Y-(head.Y+head.H) > planBarOverlap {
		t.Fatalf("待办条应紧贴输入卡片上沿: head=%+v card=%+v", head, card)
	}
	if head.H != ControlLG {
		t.Fatalf("条头高度 = %v, want %v", head.H, ControlLG)
	}
	row, ok := tt.Find("输入区状态胶囊")
	if !ok {
		t.Fatal("后台任务胶囊应仍在胶囊行里")
	}
	if row.Contains(head.X+head.W/2, head.Y+head.H/2) {
		t.Fatalf("待办不应出现在胶囊行里: row=%+v head=%+v", row, head)
	}
	if !tt.HasText("第二步") || !tt.HasText("1/3") {
		t.Fatalf("条头应显示计数与当前项: %v", tt.Texts())
	}
}

// 点条头展开清单，再点条头或按 Esc 收起；不做点外部收起。
func TestComposerPlanBarToggle(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		t.Run(fmt.Sprintf("reduce=%v", reduce), func(t *testing.T) {
			s := planBarState(false, planBarThreeSteps)
			tt := planBarHarness(t, s, reduce)
			if tt.HasText("第三步") {
				t.Fatalf("默认应收起: %v", tt.Texts())
			}
			clickText(t, tt, "待办进度 1/3")
			tt.Advance(DurBase + 60*time.Millisecond)
			if !tt.HasText("第一步") || !tt.HasText("第三步") {
				t.Fatalf("展开后应列出全部待办: %v", tt.Texts())
			}
			if tt.HasText("待办清单") {
				t.Fatal("展开列表不应再有标题行")
			}
			// 点外部不收起。
			card, _ := tt.Find("输入卡片")
			tt.ClickAt(card.X+card.W/2, card.Y+card.H/2)
			tt.Advance(DurBase + 60*time.Millisecond)
			if !tt.HasText("第三步") {
				t.Fatal("点外部不应收起")
			}
			clickText(t, tt, "待办进度 1/3")
			tt.Advance(DurBase + 60*time.Millisecond)
			if tt.HasText("第三步") {
				t.Fatalf("再点条头应收起: %v", tt.Texts())
			}

			clickText(t, tt, "待办进度 1/3")
			tt.Advance(DurBase + 60*time.Millisecond)
			tt.Key(0, ui.KeyEscape)
			tt.Advance(DurBase + 60*time.Millisecond)
			if tt.HasText("第三步") {
				t.Fatalf("Esc 应收起: %v", tt.Texts())
			}
			if !tt.HasText("第二步") {
				t.Fatal("收起后条头仍应显示当前项")
			}
		})
	}
}

// 进行中项下只显示最近 1 条工具操作；没有操作时不显示占位文字。
func TestComposerPlanBarCurrentOpOnly(t *testing.T) {
	ops := []Message{
		{ID: "r1", Role: "tool", Kind: "tool", ToolName: "Read", ToolInput: `{"file_path":"/repo/internal/ui/a.go"}`, ToolStatus: "completed"},
		{ID: "e1", Role: "tool", Kind: "tool", ToolName: "Edit", ToolInput: `{"file_path":"/repo/internal/ui/b.go"}`, ToolStatus: "completed"},
	}
	s := planBarState(true, planBarThreeSteps, ops...)
	tt := planBarHarness(t, s, true)
	clickText(t, tt, "待办进度 1/3")
	if !tt.HasText("b.go") || tt.HasText("a.go") {
		t.Fatalf("进行中项下应只有最近一条操作 b.go: %v", tt.Texts())
	}

	empty := planBarState(true, planBarThreeSteps)
	ttEmpty := planBarHarness(t, empty, true)
	clickText(t, ttEmpty, "待办进度 1/3")
	if ttEmpty.HasText("暂无操作") || ttEmpty.HasText("无操作") || ttEmpty.HasText("b.go") {
		t.Fatalf("无操作时不应显示占位文字: %v", ttEmpty.Texts())
	}
}

// 可见的清单变成全部完成：显示「全部完成」，停 2.4s 后整条收起消失，之后不再请求帧。
func TestComposerPlanBarAllDone(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		t.Run(fmt.Sprintf("reduce=%v", reduce), func(t *testing.T) {
			s := planBarState(true, `{"todos":[{"content":"甲","status":"completed"},{"content":"乙","status":"in_progress"}]}`)
			tt := planBarHarness(t, s, reduce)
			if _, ok := tt.Find("待办进度 1/2"); !ok {
				t.Fatalf("未完成的清单应显示条: %v", tt.Texts())
			}

			s.mu.Lock()
			s.messages = append(s.messages, todoToolMsg("p2", `{"todos":[{"content":"甲","status":"completed"},{"content":"乙","status":"completed"}]}`))
			s.messagesRev++
			s.mu.Unlock()
			tt.Frame()
			if !tt.HasText("全部完成") || !tt.HasText("2/2") {
				t.Fatalf("应显示全部完成: %v", tt.Texts())
			}
			tt.Advance(planBarDoneHold - 300*time.Millisecond)
			if !tt.HasText("全部完成") {
				t.Fatalf("停留期内条不应消失: %v", tt.Texts())
			}
			tt.Advance(300*time.Millisecond + DurBase + 100*time.Millisecond)
			if tt.HasText("全部完成") || tt.HasText("2/2") {
				t.Fatalf("停留结束后条应消失: %v", tt.Texts())
			}
			if tt.FramePending() {
				t.Fatal("条消失后不应再请求下一帧")
			}
			tt.Advance(5 * time.Second)
			if tt.FramePending() || tt.HasText("全部完成") {
				t.Fatal("条消失后推进时钟不应再有帧或文字")
			}
			if s.composerView.planPillOpen || s.composerView.planBarShown {
				t.Fatalf("条消失后展开与显示状态应复位: %+v", s.composerView)
			}
		})
	}
}

// 打开线程时清单本来就已全部完成的，不显示条。
func TestComposerPlanBarAlreadyCompletedHidden(t *testing.T) {
	s := planBarState(false, `{"todos":[{"content":"甲","status":"completed"},{"content":"乙","status":"completed"}]}`)
	tt := planBarHarness(t, s, false)
	if tt.HasText("全部完成") || tt.HasText("2/2") {
		t.Fatalf("已全部完成的清单不应显示条: %v", tt.Texts())
	}
	if tt.FramePending() {
		t.Fatal("没有条时不应请求帧")
	}
}

// 展开与收起的动画走完后不再请求下一帧（含减少动效），点一行可展开长文本再收回。
func TestComposerPlanBarSettles(t *testing.T) {
	long := "这是一条很长很长的待办正文，" + "用来验证默认单行省略、点一行展开成多行并且再点收回，" + "它的长度远远超过条的可用宽度"
	todos := fmt.Sprintf(`{"todos":[{"content":"第一步","status":"in_progress"},{"content":%q,"status":"pending"}]}`, long)
	// 长文本放在待办项上：条头只显示当前（进行中）项，Find 才不会先命中条头。
	for _, reduce := range []bool{false, true} {
		t.Run(fmt.Sprintf("reduce=%v", reduce), func(t *testing.T) {
			s := planBarState(false, todos)
			tt := planBarHarness(t, s, reduce)
			tt.Advance(time.Second)
			if tt.FramePending() {
				t.Fatal("静止的条不应请求帧")
			}
			clickText(t, tt, "待办进度 0/2")
			tt.Advance(DurBase + 100*time.Millisecond)
			if tt.FramePending() {
				t.Fatal("展开动画结束后不应请求帧")
			}
			collapsed, ok := tt.Find(long)
			if !ok {
				t.Fatalf("展开后应能找到长文本行: %v", tt.Texts())
			}
			clickText(t, tt, long)
			tt.Advance(DurBase + 100*time.Millisecond)
			expanded, _ := tt.Find(long)
			if expanded.H <= collapsed.H {
				t.Fatalf("点一行应展开成多行: 收起 %+v 展开 %+v", collapsed, expanded)
			}
			clickText(t, tt, long)
			tt.Advance(DurBase + 100*time.Millisecond)
			if again, _ := tt.Find(long); math.Abs(float64(again.H-collapsed.H)) > 1 {
				t.Fatalf("再点应收回单行: %+v vs %+v", again, collapsed)
			}
			clickText(t, tt, "待办进度 0/2")
			tt.Advance(DurBase + 100*time.Millisecond)
			if tt.FramePending() {
				t.Fatal("收起动画结束后不应请求帧")
			}
			if tt.HasText(long) {
				t.Fatalf("收起后清单应消失: %v", tt.Texts())
			}
		})
	}
}
