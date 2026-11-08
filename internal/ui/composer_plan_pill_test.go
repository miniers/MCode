package mcodeui

import "testing"

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
