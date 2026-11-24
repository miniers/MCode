package mcodeui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func turnChecklistFinishedMessages() []Message {
	start := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	at := func(offset time.Duration) string { return start.Add(offset).Format(time.RFC3339) }
	todoInput := `{"todos":[{"content":"读仓库","status":"completed"},{"content":"改实现","status":"completed"},{"content":"跑验证","status":"completed"}]}`
	return []Message{
		{ID: "u1", Role: "user", Text: "按清单做完", TurnIndex: 1, IsFinal: true, CreatedAt: at(0)},
		{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "先看文件", TurnIndex: 1, CreatedAt: at(2 * time.Second)},
		{ID: "todo1", Role: "tool", ToolName: "TodoWrite", ToolInput: todoInput, Text: "TodoWrite " + todoInput, TurnIndex: 1, ToolStatus: "completed", CreatedAt: at(3 * time.Second)},
		{ID: "bash1", Role: "tool", Text: `Bash {"command":"go build ./..."}`, TurnIndex: 1, ToolStatus: "completed", CreatedAt: at(5 * time.Second)},
		{ID: "a1", Role: "assistant", Kind: "text", Text: "已经全部完成", TurnIndex: 1, IsFinal: true, CreatedAt: at(12 * time.Second)},
	}
}

func TestTurnChecklist_FinishedAndRunningHeaders(t *testing.T) {
	// 1. 结束态回合头显示「用时 12s」与「3/3」入口，默认收起；点开后展示逐行步骤，不触及用时折叠体。
	finished := turnChecklistFinishedMessages()
	tt := newTurnHeaderUsageTester(t, finished, map[int]TokenUsage{1: {Input: 100, Output: 50, DurationMs: 12000}}, "3/3")

	if !tt.HasText("用时 12s") || !tt.HasText("3/3") {
		t.Fatalf("结束态回合头缺少用时或步骤入口: %v", tt.Texts())
	}
	if tt.HasText("读仓库") || tt.HasText("go build") {
		t.Fatalf("默认状态不应提前展开清单或用时折叠体: %v", tt.Texts())
	}
	if tt.HasText("待办清单") {
		t.Fatalf("新清单卡片不应保留旧标题行: %v", tt.Texts())
	}

	// 点击步骤入口展开清单卡片，同时验证「用时」折叠体仍然收起。
	clickText(t, tt, "展开步骤清单，已完成 3/3 步")
	tt.Advance(motionUnfold + 40*time.Millisecond)
	for _, step := range []string{"读仓库", "改实现", "跑验证"} {
		if !tt.HasText(step) {
			t.Fatalf("展开清单后缺少 %q: %v", step, tt.Texts())
		}
	}
	if tt.HasText("go build") {
		t.Fatalf("点击步骤入口不应触发「用时」折叠体展开: %v", tt.Texts())
	}

	// 再点步骤入口收起清单，然后点击「用时 12s」确认折叠体内不再有 todo 卡片。
	clickText(t, tt, "收起步骤清单，已完成 3/3 步")
	tt.Advance(motionUnfold + 40*time.Millisecond)
	if tt.HasText("读仓库") {
		t.Fatalf("收起清单后不应再显示步骤: %v", tt.Texts())
	}

	clickText(t, tt, "用时 12s")
	tt.Advance(motionUnfold + 40*time.Millisecond)
	if !tt.HasText("go build") {
		t.Fatalf("点开用时折叠体后应看到命令调用: %v", tt.Texts())
	}
	if tt.HasText("读仓库") || tt.HasText("TodoWrite") {
		t.Fatalf("用时折叠体内不应再出现 todo 卡片或工具胶囊: %v", tt.Texts())
	}

	// 2. 运行态回合头显示「已处理 Ns」与「1/3」入口，默认收起（运行中进度由胶囊显示）；点击入口后展开。
	start := time.Now().Add(-10 * time.Second)
	runningTodo := `{"todos":[{"content":"步骤一","status":"completed"},{"content":"步骤二","status":"in_progress"},{"content":"步骤三","status":"pending"}]}`
	runningMsgs := []Message{
		{ID: "u1", Role: "user", Text: "正在执行", TurnIndex: 1, IsFinal: true, CreatedAt: start.Format(time.RFC3339)},
		{ID: "todo1", Role: "tool", ToolName: "TodoWrite", ToolInput: runningTodo, Text: "TodoWrite " + runningTodo, TurnIndex: 1, ToolStatus: "completed"},
	}
	runTT := newTurnHeaderRunningTester(t, runningMsgs, "1/3")
	runTT.Advance(motionUnfold + 40*time.Millisecond)
	if !runTT.HasText("已处理") || !runTT.HasText("1/3") {
		t.Fatalf("运行态回合头应显示计时与步骤入口: %v", runTT.Texts())
	}
	if _, ok := runTT.Find("展开步骤清单，已完成 1/3 步"); !ok {
		t.Fatalf("运行态步骤入口默认应处于收起态: %v", runTT.Texts())
	}
	for _, step := range []string{"步骤一", "步骤三"} {
		if runTT.HasText(step) {
			t.Fatalf("运行态清单卡片默认应收起，不应显示 %q: %v", step, runTT.Texts())
		}
	}
	// 点击运行态步骤入口展开，再点收起，不受工具影响。
	clickText(t, runTT, "展开步骤清单，已完成 1/3 步")
	runTT.Advance(motionUnfold + 40*time.Millisecond)
	for _, step := range []string{"步骤一", "步骤二", "步骤三"} {
		if !runTT.HasText(step) {
			t.Fatalf("点击展开后运行态清单应显示 %q: %v", step, runTT.Texts())
		}
	}
	clickText(t, runTT, "收起步骤清单，已完成 1/3 步")
	runTT.Advance(motionUnfold + 40*time.Millisecond)
	for _, step := range []string{"步骤一", "步骤三"} {
		if runTT.HasText(step) {
			t.Fatalf("点击收起后运行态清单应收起: %v", runTT.Texts())
		}
	}
}

func TestTurnChecklistRunningCollapsedByDefault(t *testing.T) {
	start := time.Now().Add(-10 * time.Second)
	runningTodo := `{"todos":[{"content":"步骤一","status":"completed"},{"content":"步骤二","status":"in_progress"},{"content":"步骤三","status":"pending"}]}`
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "正在执行", TurnIndex: 1, IsFinal: true, CreatedAt: start.Format(time.RFC3339)},
		{ID: "todo1", Role: "tool", ToolName: "TodoWrite", ToolInput: runningTodo, Text: "TodoWrite " + runningTodo, TurnIndex: 1, ToolStatus: "completed"},
	}
	tt := newTurnHeaderRunningTester(t, msgs, "1/3")
	tt.Advance(motionUnfold + 40*time.Millisecond)
	if !tt.HasText("1/3") {
		t.Fatalf("运行中回合头应显示 1/3: %v", tt.Texts())
	}
	if _, ok := tt.Find("展开步骤清单，已完成 1/3 步"); !ok {
		t.Fatalf("运行中步骤入口默认应处于收起态: %v", tt.Texts())
	}
	if _, ok := tt.Find("待办进度 1/3"); !ok {
		t.Fatalf("运行中应同时显示输入区待办进度胶囊: %v", tt.Texts())
	}
	if tt.HasText("步骤一") || tt.HasText("步骤三") {
		t.Fatalf("运行中清单卡片默认不应展开: %v", tt.Texts())
	}

	clickText(t, tt, "展开步骤清单，已完成 1/3 步")
	tt.Advance(motionUnfold + 40*time.Millisecond)
	if _, ok := tt.Find("收起步骤清单，已完成 1/3 步"); !ok {
		t.Fatalf("点击入口后应切换为收起状态文案: %v", tt.Texts())
	}
	for _, step := range []string{"步骤一", "步骤二", "步骤三"} {
		if !tt.HasText(step) {
			t.Fatalf("点入口后应展开清单并显示 %q: %v", step, tt.Texts())
		}
	}
}

func TestTurnChecklist_NoChecklistTurnHeaderUnchanged(t *testing.T) {
	msgs := turnHeaderFinishedMessages()
	tt := newTurnHeaderUsageTester(t, msgs, map[int]TokenUsage{1: {Input: 1200, Output: 356, DurationMs: 12000}}, "用时 12s")
	for _, text := range tt.Texts() {
		if strings.Contains(text, "步") || text == "›" {
			t.Fatalf("没有清单的回合头不应出现步骤入口 %q: %v", text, tt.Texts())
		}
	}
}

func TestTurnChecklist_ToolGroupExcludesTodo(t *testing.T) {
	todoInput := `{"todos":[{"content":"计划一步","status":"completed"}]}`
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "干活", TurnIndex: 1, IsFinal: true},
		{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "想一下", TurnIndex: 1},
		{ID: "todo1", Role: "tool", ToolName: "TodoWrite", ToolInput: todoInput, Text: "TodoWrite " + todoInput, TurnIndex: 1},
	}
	// 只有 1 条思考 + 1 条 todo：去掉 todo 后不应形成空工具组或单工具组。
	s := newChatState(NewFakeClient())
	defer s.close()
	items := applyTurnWorkFold(s, "t1", buildTimelineItems(msgs))
	for _, it := range items {
		if it.kind == timelineItemToolGroup {
			t.Fatalf("仅思考和 todo 不应生成工具组: %+v", items)
		}
	}
	// 工具分类计数也不应把 todo 算进去。
	counts := countToolGroupCategories(msgs[1:])
	if counts.thoughts != 1 || counts.others != 0 || counts.total != 1 {
		t.Fatalf("工具组计数不应包含 todo: %+v", counts)
	}
}

func TestTurnChecklist_IncrementalUpdateRefreshesHeader(t *testing.T) {
	s := &chatState{}
	t1 := todoToolMsg("todo1", `{"todos":[{"content":"一步","status":"pending"},{"content":"两步","status":"pending"}]}`)
	t1.ToolName = "TodoWrite"
	t1.TurnIndex = 1
	msgs := []Message{
		{ID: "u1", Role: "user", Text: "任务", TurnIndex: 1},
		t1,
		{ID: "a1", Role: "assistant", Text: "完成一半", TurnIndex: 1, IsFinal: true},
	}
	helperBuildAndAssertIncremental(t, s, "th1", msgs, nil, "初始 0/2")
	var firstTodo Message
	for _, it := range s.timelineCacheItems {
		if it.kind == timelineItemTurnWork {
			firstTodo = it.msg
		}
	}
	if items, ok := todoItemsFromMessage(firstTodo); !ok {
		t.Fatalf("回合头应挂载清单消息: %+v", s.timelineCacheItems)
	} else if done, total := todoProgress(items); done != 0 || total != 2 {
		t.Fatalf("初始进度 = %d/%d, want 0/2", done, total)
	}

	// 更新同一条清单或追加新清单后，增量重建后的回合头必须跟着更新。
	t2 := todoToolMsg("todo2", `{"todos":[{"content":"一步","status":"completed"},{"content":"两步","status":"completed"}]}`)
	t2.ToolName = "TodoWrite"
	t2.TurnIndex = 1
	msgs = append(msgs, t2)
	helperBuildAndAssertIncremental(t, s, "th1", msgs, nil, "更新 2/2")
	var updatedTodo Message
	for _, it := range s.timelineCacheItems {
		if it.kind == timelineItemTurnWork {
			updatedTodo = it.msg
		}
	}
	if items, ok := todoItemsFromMessage(updatedTodo); !ok {
		t.Fatalf("更新后回合头应挂载新清单: %+v", s.timelineCacheItems)
	} else if done, total := todoProgress(items); done != 2 || total != 2 {
		t.Fatalf("更新后进度 = %d/%d, want 2/2", done, total)
	}
}

func TestTurnChecklist_NoNextFrameAfterAnimationSettles(t *testing.T) {
	for _, reduced := range []bool{false, true} {
		t.Run(fmt.Sprintf("ReduceMotion=%v", reduced), func(t *testing.T) {
			setAppReduceMotion(t, reduced)
			todoInput := `{"todos":[{"content":"已完成项","status":"completed"},{"content":"进行中项","status":"in_progress"},{"content":"待处理项","status":"pending"}]}`
			msgs := []Message{
				{ID: "u1", Role: "user", Text: "看看清单", TurnIndex: 1, IsFinal: true},
				{ID: "todo1", Role: "tool", ToolName: "TodoWrite", ToolInput: todoInput, Text: "TodoWrite " + todoInput, TurnIndex: 1, ToolStatus: "completed"},
				{ID: "a1", Role: "assistant", Kind: "text", Text: "好了", TurnIndex: 1, IsFinal: true},
			}
			fake := NewFakeClient()
			fake.messages["t1"] = msgs
			_, view := buildRoot(fake)
			tt := ui.NewTesterWith(view, 1000, 700, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: reduced}})
			waitFor(t, tt, func() bool { return tt.HasText("1/3") })

			// 静止时不应有帧在跑。
			for i := 0; i < 30; i++ {
				tt.Advance(20 * time.Millisecond)
				tt.Frame()
			}
			if tt.FramePending() {
				t.Fatalf("静止态（ReduceMotion=%v）不应继续请求下一帧", reduced)
			}

			// 展开清单卡片，推进时钟到动画结束后不再请求下一帧。
			clickText(t, tt, "展开步骤清单，已完成 1/3 步")
			if reduced {
				tt.Frame()
				if tt.FramePending() {
					t.Fatal("ReduceMotion 开启时展开应直接到终点，不再请求下一帧")
				}
			} else {
				for i := 0; i < 30; i++ {
					tt.Advance(20 * time.Millisecond)
					tt.Frame()
				}
				if tt.FramePending() {
					t.Fatal("展开动画结束后不应继续请求下一帧")
				}
			}
			if !tt.HasText("已完成项") || !tt.HasText("待处理项") {
				t.Fatalf("展开后应显示清单项: %v", tt.Texts())
			}

			// 收起清单卡片，推进时钟到动画结束后不再请求下一帧。
			clickText(t, tt, "收起步骤清单，已完成 1/3 步")
			if reduced {
				tt.Frame()
				if tt.FramePending() {
					t.Fatal("ReduceMotion 开启时收起应直接到终点，不再请求下一帧")
				}
			} else {
				for i := 0; i < 30; i++ {
					tt.Advance(20 * time.Millisecond)
					tt.Frame()
				}
				if tt.FramePending() {
					t.Fatal("收起动画结束后不应继续请求下一帧")
				}
			}
			if tt.HasText("已完成项") || tt.HasText("待处理项") {
				t.Fatalf("收起后不应再显示清单项: %v", tt.Texts())
			}
		})
	}
}

// TestTurnChecklistChip 验证清单入口是没有箭头的 done/total 小胶囊，回合头图标与文字同尺寸居中，
// 点胶囊只开合清单卡片，动画结束后不再续帧。
func TestTurnChecklistChip(t *testing.T) {
	for _, reduced := range []bool{false, true} {
		t.Run(fmt.Sprintf("ReduceMotion=%v", reduced), func(t *testing.T) {
			setAppReduceMotion(t, reduced)
			start := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
			at := func(offset time.Duration) string { return start.Add(offset).Format(time.RFC3339) }
			todoInput := `{"todos":[{"content":"读仓库","status":"completed"},{"content":"改实现","status":"completed"},{"content":"跑验证","status":"pending"},{"content":"收尾","status":"pending"}]}`
			msgs := []Message{
				{ID: "u1", Role: "user", Text: "按清单做", TurnIndex: 1, IsFinal: true, CreatedAt: at(0)},
				{ID: "todo1", Role: "tool", ToolName: "TodoWrite", ToolInput: todoInput, Text: "TodoWrite " + todoInput, TurnIndex: 1, ToolStatus: "completed", CreatedAt: at(3 * time.Second)},
				{ID: "bash1", Role: "tool", Text: `Bash {"command":"go build ./..."}`, TurnIndex: 1, ToolStatus: "failed", CreatedAt: at(5 * time.Second)},
				{ID: "a1", Role: "assistant", Kind: "text", Text: "先到这里", TurnIndex: 1, IsFinal: true, CreatedAt: at(12 * time.Second)},
			}
			fake := NewFakeClient()
			fake.messages["t1"] = msgs
			_, view := buildRoot(fake)
			tt := ui.NewTesterWith(view, 1000, 700, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: reduced}})
			waitFor(t, tt, func() bool { return tt.HasText("2/4") })

			// 入口只剩 done/total：没有 › 文字，也没有「步」字。
			if tt.HasText("›") {
				t.Fatalf("清单入口不应再有 › 文字: %v", tt.Texts())
			}
			// Texts() 也包含无障碍 Label「展开步骤清单，已完成 2/4 步」，它按约定保留「步」，所以只查可见文字。
			for _, text := range tt.Texts() {
				if text == "2/4 步" {
					t.Fatalf("清单入口文字不应含「步」: %v", tt.Texts())
				}
			}

			// 回合头：警告图标与 chevron 都是 IconSM 见方，且与文字垂直居中对齐。
			var header ui.Rect
			for _, text := range tt.Texts() {
				if strings.HasPrefix(text, "用时") {
					header, _ = tt.Find(text)
				}
			}
			if header.W <= 0 {
				t.Fatalf("找不到回合头文字: %v", tt.Texts())
			}
			for _, label := range []string{"工具执行失败", "折叠箭头"} {
				icon, ok := tt.Find(label)
				if !ok {
					t.Fatalf("回合头缺少图标 %q: %v", label, tt.Texts())
				}
				if icon.W != IconSM || icon.H != IconSM {
					t.Fatalf("%s 尺寸 = %vx%v, want %vx%v", label, icon.W, icon.H, IconSM, IconSM)
				}
				if d := (icon.Y + icon.H/2) - (header.Y + header.H/2); d > 1 || d < -1 {
					t.Fatalf("%s 与文字垂直中心相差 %v", label, d)
				}
			}

			// 点清单入口只展开清单卡片，不展开工作过程。
			clickText(t, tt, "展开步骤清单，已完成 2/4 步")
			for i := 0; i < 30; i++ {
				tt.Advance(20 * time.Millisecond)
				tt.Frame()
			}
			for _, step := range []string{"读仓库", "改实现", "跑验证", "收尾"} {
				if !tt.HasText(step) {
					t.Fatalf("展开清单后缺少 %q: %v", step, tt.Texts())
				}
			}
			if tt.HasText("go build") {
				t.Fatalf("点清单入口不应展开工作过程: %v", tt.Texts())
			}
			if tt.FramePending() {
				t.Fatalf("动画结束后（ReduceMotion=%v）不应继续请求下一帧", reduced)
			}
		})
	}
}
