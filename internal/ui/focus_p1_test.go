package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestFocusP1TimelineFoldHeadersKeyboard 验证工具组、回合工作组、思考过程折叠头可聚焦、具备按钮语义，且按 Enter/Space 切换展开收起。
func TestFocusP1TimelineFoldHeadersKeyboard(t *testing.T) {
	t.Run("工具组折叠头", func(t *testing.T) {
		s := newChatState(NewFakeClient())
		tools := []Message{{ID: "tool-1", ToolName: "read", ToolStatus: "completed"}}
		tt := ui.NewTester(func(c *ui.Context) {
			renderToolGroupItem(c, s, tools, "group-focus-1", "", false)
		}, 800, 300)
		tt.Frame()

		info, ok := tt.AccessByRole(ui.RoleButton, "展开")
		if !ok {
			t.Fatal("工具组折叠头应具备 RoleButton 与「展开」标签")
		}
		if !info.Focusable {
			t.Fatal("工具组折叠头应可聚焦")
		}

		tt.Key(0, ui.KeyTab)
		if !tt.Focused("展开") {
			t.Fatal("按 Tab 后工具组折叠头应获焦")
		}

		tt.Key(0, ui.KeyEnter)
		s.mu.Lock()
		openAfterEnter := s.expandedTool["group-focus-1"]
		s.mu.Unlock()
		if !openAfterEnter {
			t.Fatal("聚焦工具组折叠头按 Enter 后应展开")
		}

		tt.Key(0, ui.KeySpace)
		s.mu.Lock()
		openAfterSpace := s.expandedTool["group-focus-1"]
		s.mu.Unlock()
		if openAfterSpace {
			t.Fatal("聚焦工具组折叠头按 Space 后应收起")
		}
	})

	t.Run("回合工作组折叠头", func(t *testing.T) {
		s := newChatState(NewFakeClient())
		item := timelineItem{
			kind:        timelineItemTurnWork,
			id:          "tw-focus-1",
			turnIndex:   1,
			workElapsed: "5.0s",
			tools:       []Message{{ID: "tool-1", ToolName: "read", ToolStatus: "completed"}},
		}
		tt := ui.NewTester(func(c *ui.Context) {
			renderTurnWorkItem(c, s, item, TokenUsage{}, false)
		}, 800, 400)
		tt.Frame()

		openLabel := turnWorkHeaderAccessLabel(item, false)
		info, ok := tt.AccessByRole(ui.RoleButton, openLabel)
		if !ok {
			t.Fatalf("回合工作组折叠头应具备 RoleButton 与 %q 标签", openLabel)
		}
		if !info.Focusable {
			t.Fatal("回合工作组折叠头应可聚焦")
		}

		tt.Key(0, ui.KeyTab)
		if !tt.Focused(openLabel) {
			t.Fatal("按 Tab 后回合工作组折叠头应获焦")
		}

		tt.Key(0, ui.KeyEnter)
		s.mu.Lock()
		openAfterEnter := s.expandedTool[item.id]
		s.mu.Unlock()
		if !openAfterEnter {
			t.Fatal("聚焦回合工作组折叠头按 Enter 后应展开")
		}

		tt.Key(0, ui.KeySpace)
		s.mu.Lock()
		openAfterSpace := s.expandedTool[item.id]
		s.mu.Unlock()
		if openAfterSpace {
			t.Fatal("聚焦回合工作组折叠头按 Space 后应收起")
		}
	})

	t.Run("思考过程折叠头", func(t *testing.T) {
		s := newChatState(NewFakeClient())
		m := Message{ID: "reason-focus-1", Role: "assistant", Kind: "reasoning", Text: "先分析调用链再修改"}
		tt := ui.NewTester(func(c *ui.Context) {
			renderReasoningMessage(c, s, m, "", false, false, TokenUsage{}, 0)
		}, 800, 300)
		tt.Frame()

		info, ok := tt.AccessByRole(ui.RoleButton, "展开思考过程")
		if !ok {
			t.Fatal("思考过程折叠头应具备 RoleButton 与「展开思考过程」标签")
		}
		if !info.Focusable {
			t.Fatal("思考过程折叠头应可聚焦")
		}

		tt.Key(0, ui.KeyTab)
		if !tt.Focused("展开思考过程") {
			t.Fatal("按 Tab 后思考过程折叠头应获焦")
		}

		tt.Key(0, ui.KeyEnter)
		s.mu.Lock()
		openAfterEnter := s.expandedReasoning[m.ID]
		s.mu.Unlock()
		if !openAfterEnter {
			t.Fatal("聚焦思考过程折叠头按 Enter 后应展开")
		}

		tt.Key(0, ui.KeySpace)
		s.mu.Lock()
		openAfterSpace := s.expandedReasoning[m.ID]
		s.mu.Unlock()
		if openAfterSpace {
			t.Fatal("聚焦思考过程折叠头按 Space 后应收起")
		}
	})
}

// TestFocusP1FindAndBackgroundTaskButtons 验证收口后的查找条与后台任务按钮具备 RoleButton、无障碍标签，且热区不小于 ControlSM。
func TestFocusP1FindAndBackgroundTaskButtons(t *testing.T) {
	assertButtonMinHitArea := func(t *testing.T, tt *ui.Tester, label string) {
		t.Helper()
		info, ok := tt.AccessByRole(ui.RoleButton, label)
		if !ok {
			t.Fatalf("未找到 RoleButton 按钮 %q", label)
		}
		if info.Bounds.W < ControlSM || info.Bounds.H < ControlSM {
			t.Fatalf("按钮 %q 热区 %vx%v 小于 ControlSM (%v)", label, info.Bounds.W, info.Bounds.H, ControlSM)
		}
	}

	t.Run("会话内查找条按钮", func(t *testing.T) {
		s := newChatState(NewFakeClient())
		f := getChatFindState(s)
		f.open = true
		tt := ui.NewTester(func(c *ui.Context) {
			renderChatFindBar(c, s, "t1", 1, nil, nil)
		}, 800, 300)
		tt.Frame()

		assertButtonMinHitArea(t, tt, "上一个匹配 "+shortcutDisplay("⇧↩"))
		assertButtonMinHitArea(t, tt, "下一个匹配 "+shortcutDisplay("↩"))
		assertButtonMinHitArea(t, tt, "区分大小写")
		assertButtonMinHitArea(t, tt, "关闭查找 Esc")
	})

	t.Run("后台任务浮层与等待条按钮", func(t *testing.T) {
		s := newChatState(NewFakeClient())
		s.selectedThreadID = "t1"
		s.composerView.backgroundTasksOpen = true
		task := backgroundTasksTestBackground("bg-focus-1")
		data := backgroundTasksItem{
			threadID: "t1",
			tasks:    []Message{task},
		}
		tt := ui.NewTester(func(c *ui.Context) {
			renderBackgroundTasksPanel(c, s, "tasks", data, nil)
		}, 800, 500)
		tt.Frame()

		assertButtonMinHitArea(t, tt, backgroundTaskDisplayTitle(task))
		assertButtonMinHitArea(t, tt, "停止")

		s.mu.Lock()
		s.messages = []Message{task}
		s.mu.Unlock()
		waitTester := ui.NewTester(func(c *ui.Context) {
			renderBackgroundWaitBar(c, s, "wait-focus")
		}, 800, 200)
		waitTester.Frame()

		assertButtonMinHitArea(t, waitTester, "等待后台任务")
	})
}
