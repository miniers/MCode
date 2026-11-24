package mcodeui

import (
	"reflect"
	"testing"
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

func TestClaudeTaskPlan(t *testing.T) {
	msgs := []Message{
		{Role: "user", Text: "开工"},
		claudeTaskMsg("TaskCreate", `{"subject":"读代码","description":"阅读"}`, "Task #1 created successfully: 读代码"),
		claudeTaskMsg("taskcreate", `{"subject":"改代码","description":"修改"}`, "Task #2 created successfully: 改代码"),
		claudeTaskMsg("TaskCreate", `{"subject":"检查","description":"检查"}`, "Task #3 created successfully: 检查"),
		claudeTaskMsg("TaskUpdate", `{"taskId":"1","status":"completed"}`, "Updated task #1"),
	}
	items, idx := claudeTaskPlan(msgs)
	want := []todoItem{
		{Text: "读代码", Status: "completed"},
		{Text: "改代码", Status: "pending"},
		{Text: "检查", Status: "pending"},
	}
	if idx != 4 || !reflect.DeepEqual(items, want) {
		t.Fatalf("unexpected tasks: items=%+v idx=%d", items, idx)
	}
	msgs = append(msgs, claudeTaskMsg("taskupdate", `{"taskId":"2","status":"deleted"}`, "Deleted task #2"))
	items, idx = claudeTaskPlan(msgs)
	want = []todoItem{
		{Text: "读代码", Status: "completed"},
		{Text: "检查", Status: "pending"},
	}
	if idx != 5 || !reflect.DeepEqual(items, want) {
		t.Fatalf("unexpected tasks after deletion: items=%+v idx=%d", items, idx)
	}
	msgs = append(msgs, claudeTaskMsg("TaskUpdate", `{"taskId":"3","subject":"完成检查","status":"in_progress"}`, "Updated task #3"))
	items, idx = claudeTaskPlan(msgs)
	want[1] = todoItem{Text: "完成检查", Status: "in_progress"}
	if idx != 6 || !reflect.DeepEqual(items, want) {
		t.Fatalf("unexpected tasks after update: items=%+v idx=%d", items, idx)
	}
}

func TestClaudeTaskFallbackIDs(t *testing.T) {
	msgs := []Message{
		claudeTaskMsg("TaskCreate", `{"subject":"一","description":"第一项"}`, "创建成功"),
		claudeTaskMsg("TaskCreate", `{"subject":"二","description":"第二项"}`, "创建成功"),
		claudeTaskMsg("TaskUpdate", `{"taskId":"2","status":"completed"}`, "更新成功"),
	}
	items, idx := claudeTaskPlan(msgs)
	want := []todoItem{
		{Text: "一", Status: "pending"},
		{Text: "二", Status: "completed"},
	}
	if idx != 2 || !reflect.DeepEqual(items, want) {
		t.Fatalf("unexpected tasks: items=%+v idx=%d", items, idx)
	}
}

func TestLatestTodoPlan(t *testing.T) {
	t.Run("整份清单覆盖更早的Claude任务", func(t *testing.T) {
		msgs := []Message{
			claudeTaskMsg("TaskCreate", `{"subject":"旧任务","description":"旧"}`, "Task #7 created successfully: 旧任务"),
			{Role: "tool", Kind: "tool", ToolName: "update_plan", ToolInput: `{"plan":[{"step":"新任务","status":"in_progress"}]}`, Text: `update_plan {"plan":[{"step":"新任务","status":"in_progress"}]}`},
		}
		items, idx := latestTodoPlan(msgs)
		if idx != 1 || len(items) != 1 || items[0] != (todoItem{Text: "新任务", Status: "in_progress"}) {
			t.Fatalf("unexpected plan: %v idx=%d", items, idx)
		}
	})

	t.Run("没有待办消息返回空", func(t *testing.T) {
		msgs := []Message{
			{ID: "u1", Role: "user", Text: "帮我改一下输入框"},
			{ID: "a1", Role: "assistant", Text: "好的，我先读代码。"},
			{ID: "t1", Role: "tool", Kind: "tool", Text: "bash ls", ToolInput: `{"command":"ls"}`},
		}
		if items, idx := latestTodoPlan(msgs); len(items) != 0 || idx != -1 {
			t.Fatalf("expected no todo items, got %v idx=%d", items, idx)
		}
	})

	t.Run("最新整份清单覆盖旧清单", func(t *testing.T) {
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
		if !reflect.DeepEqual(items, want) {
			t.Fatalf("got %+v, want %+v", items, want)
		}
	})
}
