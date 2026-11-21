package mcodeui

import (
	"fmt"
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
	if got := planPillText(buildPlanPillView(msgs, false)); got != "1/2 · 改代码" {
		t.Fatalf("planPillText = %q", got)
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
		renderComposerPlanPill(c, s, "thread")
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

func TestPlanPillMilestones(t *testing.T) {
	testenv.TempHome(t)

	t.Run("2到6项时出点且状态正确", func(t *testing.T) {
		s := newChatState(NewFakeClient())
		defer s.close()
		s.selectedThreadID = "t1"
		s.threads = []Thread{{ID: "t1", IsRunning: true}}
		s.messages = []Message{{
			ID:        "todo-4",
			Role:      "tool",
			Kind:      "tool",
			ToolName:  "TodoWrite",
			ToolInput: `{"todos":[{"content":"第一步","status":"completed"},{"content":"第二步","status":"completed"},{"content":"第三步","status":"in_progress"},{"content":"第四步","status":"pending"}]}`,
		}}
		s.messagesRev = 1

		capsule, ok := composerPlanCapsule(nil, s, "t1")
		if !ok {
			t.Fatal("应生成待办胶囊")
		}
		if capsule.Text != "第三步" || capsule.CountText != "2/4" {
			t.Fatalf("胶囊文本或计数不符: Text=%q CountText=%q", capsule.Text, capsule.CountText)
		}
		want := []milestoneState{milestoneCompleted, milestoneCompleted, milestoneInProgress, milestonePending}
		if !reflect.DeepEqual(capsule.Milestones, want) {
			t.Fatalf("Milestones = %v, want %v", capsule.Milestones, want)
		}

		tt := ui.NewTester(func(c *ui.Context) {
			renderComposerCapsuleRow(c, s, []composerCapsule{capsule})
		}, 600, 120)
		tt.Frame()

		textRect, ok := tt.Find("第三步")
		if !ok {
			t.Fatalf("应能找到当前步文字，可见文字: %v", tt.Texts())
		}
		dotsRect, ok := tt.Find("里程碑")
		if !ok {
			t.Fatal("2..6 项时应渲染里程碑点容器")
		}
		countRect, ok := tt.Find("2/4")
		if !ok {
			t.Fatalf("应能找到计数 2/4，可见文字: %v", tt.Texts())
		}
		if !(textRect.X < dotsRect.X && dotsRect.X < countRect.X) {
			t.Fatalf("里程碑点应位于当前步文字与计数之间: text=%+v dots=%+v count=%+v", textRect, dotsRect, countRect)
		}
		for _, label := range []string{"步骤 1 已完成", "步骤 2 已完成", "步骤 3 进行中", "步骤 4 未开始"} {
			if _, ok := tt.Find(label); !ok {
				t.Fatalf("缺少里程碑点 %q", label)
			}
		}

		// 边界：2 项和 6 项均出点。
		for _, total := range []int{2, 6} {
			s.mu.Lock()
			todos := ""
			for i := 0; i < total; i++ {
				if i > 0 {
					todos += ","
				}
				status := "pending"
				if i == 0 {
					status = "in_progress"
				}
				todos += fmt.Sprintf(`{"content":"项%d","status":%q}`, i+1, status)
			}
			s.messages = []Message{{ID: fmt.Sprintf("todo-%d", total), Role: "tool", Kind: "tool", ToolName: "TodoWrite", ToolInput: fmt.Sprintf(`{"todos":[%s]}`, todos)}}
			s.messagesRev++
			s.mu.Unlock()
			capN, ok := composerPlanCapsule(nil, s, "t1")
			if !ok || len(capN.Milestones) != total {
				t.Fatalf("%d 项时应出 %d 个点, ok=%v len=%d", total, total, ok, len(capN.Milestones))
			}
		}
	})

	t.Run("超过6项或少于2项不出点", func(t *testing.T) {
		for _, total := range []int{1, 7} {
			s := newChatState(NewFakeClient())
			s.selectedThreadID = "t1"
			s.threads = []Thread{{ID: "t1", IsRunning: true}}
			todos := ""
			for i := 0; i < total; i++ {
				if i > 0 {
					todos += ","
				}
				status := "pending"
				if i == 0 && total > 1 {
					status = "completed"
				} else if i == 1 || (total == 1 && i == 0) {
					status = "in_progress"
				}
				todos += fmt.Sprintf(`{"content":"步骤%d","status":%q}`, i+1, status)
			}
			s.messages = []Message{{
				ID:        fmt.Sprintf("todo-%d", total),
				Role:      "tool",
				Kind:      "tool",
				ToolName:  "TodoWrite",
				ToolInput: fmt.Sprintf(`{"todos":[%s]}`, todos),
			}}
			s.messagesRev = 1

			capsule, ok := composerPlanCapsule(nil, s, "t1")
			if !ok {
				s.close()
				t.Fatalf("%d 项时胶囊应可见", total)
			}
			if len(capsule.Milestones) != 0 {
				s.close()
				t.Fatalf("%d 项时不应生成里程碑点，got %v", total, capsule.Milestones)
			}
			tt := ui.NewTester(func(c *ui.Context) {
				renderComposerCapsuleRow(c, s, []composerCapsule{capsule})
			}, 600, 120)
			tt.Frame()
			if _, ok := tt.Find("里程碑"); ok {
				s.close()
				t.Fatalf("%d 项时不应渲染里程碑点", total)
			}
			s.close()
		}
	})

	t.Run("其它胶囊不受影响", func(t *testing.T) {
		tt := ui.NewTester(func(c *ui.Context) {
			renderComposerCapsuleRow(c, nil, []composerCapsule{{
				Key:   "plain-pill",
				Label: "普通胶囊",
				Text:  "普通提示",
			}})
		}, 400, 100)
		tt.Frame()
		if !tt.HasText("普通提示") {
			t.Fatal("普通胶囊正文应正常显示")
		}
		if _, ok := tt.Find("里程碑"); ok {
			t.Fatal("普通胶囊不应渲染里程碑点")
		}
	})
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
		if len(view.CurrentOps) != 3 {
			t.Fatalf("CurrentOps 长度 = %d, want 3: %+v", len(view.CurrentOps), view.CurrentOps)
		}
		if view.CurrentOps[0].Name != "读取" || view.CurrentOps[0].Target != "a.go" {
			t.Fatalf("op[0] = %+v", view.CurrentOps[0])
		}
		if view.CurrentOps[1].Name != "编辑" || view.CurrentOps[1].Target != "b.go" {
			t.Fatalf("op[1] = %+v", view.CurrentOps[1])
		}
		if view.CurrentOps[2].Name != "$" || view.CurrentOps[2].Target != "静态检查" {
			t.Fatalf("op[2] = %+v", view.CurrentOps[2])
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
		if len(claudeView.CurrentOps) != 2 {
			t.Fatalf("Claude CurrentOps 长度 = %d, want 2: %+v", len(claudeView.CurrentOps), claudeView.CurrentOps)
		}
		if claudeView.CurrentOps[0].Target != "cur1.go" || claudeView.CurrentOps[1].Target != "cur2.go" {
			t.Fatalf("Claude CurrentOps = %+v", claudeView.CurrentOps)
		}

		// 弹层渲染：减少动效下直接展开且不持续请求帧；无操作时不显示占位文字。
		setAppReduceMotion(t, true)
		opts := ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}}
		tt := ui.NewTesterWith(func(c *ui.Context) {
			renderPlanPopover(c, view)
		}, 500, 400, opts)
		tt.Frame()
		if !tt.HasText("a.go") || !tt.HasText("b.go") || !tt.HasText("静态检查") {
			t.Fatalf("弹层应列出当前步操作，可见文字: %v", tt.Texts())
		}
		if tt.HasText("before.go") {
			t.Fatal("弹层不应包含当前步之前的工具操作")
		}
		if tt.FramePending() {
			t.Fatal("减少动效时弹层展开不应请求后续帧")
		}

		emptyView := view
		emptyView.CurrentOps = nil
		ttEmpty := ui.NewTesterWith(func(c *ui.Context) {
			renderPlanPopover(c, emptyView)
		}, 500, 400, opts)
		ttEmpty.Frame()
		if ttEmpty.HasText("a.go") || ttEmpty.HasText("暂无操作") || ttEmpty.HasText("无操作") {
			t.Fatalf("无操作时不应显示占位文字，可见文字: %v", ttEmpty.Texts())
		}
	})

	t.Run("最多保留最近8条", func(t *testing.T) {
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
		if len(view.CurrentOps) != 8 {
			t.Fatalf("CurrentOps 长度 = %d, want 8", len(view.CurrentOps))
		}
		if view.CurrentOps[0].Target != "f03.go" || view.CurrentOps[7].Target != "f10.go" {
			t.Fatalf("CurrentOps 首尾 = %q .. %q, want f03.go .. f10.go", view.CurrentOps[0].Target, view.CurrentOps[7].Target)
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
		if _, ok := composerPlanCapsule(nil, s, "t1"); !ok {
			t.Fatal("首次构建应返回可见胶囊")
		}
		afterFirst := planPillBuilds.Load()
		if afterFirst != before+1 {
			t.Fatalf("首次构建计数 = %d, want %d", afterFirst, before+1)
		}
		for i := 0; i < 3; i++ {
			if _, ok := composerPlanCapsule(nil, s, "t1"); !ok {
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

		if _, ok := composerPlanCapsule(nil, s, "t1"); !ok {
			t.Fatal("messagesRev 变更后应返回可见胶囊")
		}
		if got := planPillBuilds.Load(); got != afterFirst+1 {
			t.Fatalf("messagesRev 变更后应重算一次: want=%d got=%d", afterFirst+1, got)
		}
		if len(s.planPillCache.CurrentOps) != 2 {
			t.Fatalf("更新后 CurrentOps 长度 = %d, want 2", len(s.planPillCache.CurrentOps))
		}
	})
}
