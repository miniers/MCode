package mcodeui

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestParseStewardResult(t *testing.T) {
	result, ok := parseStewardResult(`{"task":"task-1","thread":"thread-1","title":"整理任务","summary":"已完成","status":"done"}`)
	if !ok || result.Task != "task-1" || result.Thread != "thread-1" || result.Status != "done" {
		t.Fatalf("解析结果错误: %+v, %v", result, ok)
	}
	for _, text := range []string{"旧的纯文本结果", `{"task":"task-1","title":"缺少状态","summary":"结果"}`, `{"task":"task-1","thread":"thread-1","title":"错误状态","summary":"结果","status":"waiting"}`} {
		if _, ok := parseStewardResult(text); ok {
			t.Fatalf("无效结果不应解析成功: %q", text)
		}
	}
}

func TestStewardTaskResultOpensThreadAndShowsBand(t *testing.T) {
	fake := NewFakeClient()
	fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "项目"}}, map[string][]Thread{
		"p1": {
			{ID: "feed", ProjectID: "p1", Title: "总管"},
			{ID: "task-thread", ProjectID: "p1", Title: "任务线程"},
			{ID: "other", ProjectID: "p1", Title: "其他线程"},
		},
	})
	resultText, _ := json.Marshal(stewardResult{Task: "task-1", Thread: "task-thread", Title: "整理任务", Summary: "已完成", Status: "done", Owner: true})
	fake.SetMessages("feed", []Message{{ID: "result-1", Role: "assistant", Kind: stewardResultKind, Text: string(resultText), TurnIndex: 1, IsFinal: true}})
	fake.SetMessages("task-thread", nil)
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{{ID: "task-1", ThreadID: "task-thread", Status: "waiting", ResultSummary: "等待文件", WaitKind: "machine", WakeAt: time.Now().Add(time.Hour).Format(time.RFC3339)}}})

	state, view := buildRoot(fake, WithInitialThread("feed"))
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("打开任务") })
	if !tt.HasText("已完成") || tt.HasText("整理任务：已完成") {
		t.Fatalf("用户任务结果应只显示摘要: %q", tt.Texts())
	}
	if err := tt.Click("打开任务"); err != nil {
		t.Fatalf("点击打开任务失败: %v", err)
	}
	waitFor(t, tt, func() bool { return selectedThreadForTest(state) == "task-thread" && tt.HasText("等待中") })
	if !tt.HasText("等待文件") {
		t.Fatalf("任务信息带缺少结果摘要: %q", tt.Texts())
	}

	state.selectThread("other")
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	if tt.HasText("等待文件") || tt.HasText("等待中") {
		t.Fatalf("切换线程后任务信息带仍存在: %q", tt.Texts())
	}
}

func TestStewardResultCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, role, text, want string
		system                 bool
	}{
		{"旧 JSON", "assistant", `{"task":"task-1","title":"整理任务","summary":"已完成","status":"done"}`, "整理任务：已完成", false},
		{"失败", "assistant", `{"task":"task-1","title":"整理任务","summary":"缺少文件","status":"failed","owner":true}`, "没能完成「整理任务」：缺少文件", false},
		{"系统消息", "system", "整理任务\n已完成", "整理任务\n已完成", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newChatState(NewFakeClient())
			msg := Message{ID: "result", Role: tc.role, Kind: stewardResultKind, Text: tc.text, IsFinal: true}
			tt := ui.NewTester(func(c *ui.Context) {
				renderMessageItem(c, s, msg, "", "", true, false, TokenUsage{}, 0, false)
			}, 800, 300)
			if !tt.HasText(tc.want) {
				t.Fatalf("结果正文不匹配: %q", tt.Texts())
			}
			if _, ok := tt.Find("sys-pill-result"); ok != tc.system {
				t.Fatalf("系统消息样式不匹配: %q", tt.Texts())
			}
		})
	}
}

func TestStewardTaskBandForSelectedThread(t *testing.T) {
	fake := NewFakeClient()
	fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "项目"}}, map[string][]Thread{
		"p1": {{ID: "t1", ProjectID: "p1", Title: "任务线程"}, {ID: "t2", ProjectID: "p1", Title: "其他线程"}},
	})
	wakeAt := "2026-11-09T12:00:00Z"
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{{ID: "task-1", ThreadID: "t1", Status: "waiting", WaitKind: "machine", WakeAt: wakeAt}}})
	state, view := buildRoot(fake, WithInitialThread("t2"))
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return selectedThreadForTest(state) == "t2" })
	if tt.HasText("等待中") || tt.HasText("等待条件满足") {
		t.Fatalf("普通线程不应显示任务信息带: %q", tt.Texts())
	}

	state.selectThread("t1")
	waitFor(t, tt, func() bool {
		return tt.HasText("等待中") && tt.HasText("等待条件满足 · "+stewardWakeLocalTime(wakeAt))
	})
	state.selectThread("t2")
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	if tt.HasText("等待中") || tt.HasText("等待条件满足 · "+stewardWakeLocalTime(wakeAt)) {
		t.Fatalf("切换到普通线程后任务信息带仍存在: %q", tt.Texts())
	}
}

func TestStewardResultPlainTextFallback(t *testing.T) {
	fake := NewFakeClient()
	fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "项目"}}, map[string][]Thread{
		"p1": {{ID: "feed", ProjectID: "p1", Title: "总管"}},
	})
	fake.SetMessages("feed", []Message{{ID: "result-1", Role: "assistant", Kind: stewardResultKind, Text: "旧的纯文本结果", TurnIndex: 1, IsFinal: true}})
	state, view := buildRoot(fake, WithInitialThread("feed"))
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("旧的纯文本结果") })
	if tt.HasText("打开任务") {
		t.Fatal("旧纯文本结果不应显示打开任务按钮")
	}
}
