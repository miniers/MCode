package mcodeui

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestSidebarFooterLabels(t *testing.T) {
	state, view := buildRoot(NewFakeClient(), WithSettings(settingsTestDir(t), nil))
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	for _, label := range []string{"添加项目", "任务", "自动化", "设置"} {
		if _, ok := tt.Find(label); !ok {
			t.Errorf("找不到底栏按钮 %q", label)
		}
	}
}

func TestSidebarFooter_WorkspaceButtons(t *testing.T) {
	for _, tc := range []struct {
		label string
		empty string
	}{
		{label: "任务", empty: "还没有任务"},
		{label: "自动化", empty: "此项目没有自动化"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			fake := NewFakeClient()
			fake.SetKanbanBoard(KanbanBoard{})
			fake.SetAutomations(nil)
			state, view := buildRoot(fake)
			t.Cleanup(state.close)
			tt := ui.NewTester(view, 1000, 700)
			waitFor(t, tt, func() bool {
				_, ok := tt.Find(tc.label)
				return tt.HasText("测试会话1") && ok
			})

			if err := tt.Click(tc.label); err != nil {
				t.Fatalf("点击底栏 %q 失败: %v", tc.label, err)
			}
			waitFor(t, tt, func() bool {
				return tt.HasText(tc.empty)
			})
		})
	}
}

// TestSidebarFooter_KanbanReviewCount 验证 refreshKanbanBadge 缓存待审阅任务数：
// 只统计 Column 为 review 或带 CanReview 标记的任务，并在 30 秒内节流。
func TestSidebarFooter_KanbanReviewCount(t *testing.T) {
	fake := NewFakeClient()
	fake.kanbanBoard = KanbanBoard{
		Revision: 1,
		Tasks: []KanbanTask{
			{ID: "k1", ProjectID: "p1", Column: "review"},
			{ID: "k2", ProjectID: "p1", Column: "todo", CanReview: true},
			{ID: "k3", ProjectID: "p1", Column: "todo"},
			{ID: "k4", ProjectID: "p1", Column: "done"},
		},
	}
	s := newChatState(fake)

	if got := sidebarKanbanReviewCount(s); got != 0 {
		t.Fatalf("初始缓存应为 0，got %d", got)
	}

	refreshKanbanBadge(s)

	deadline := time.Now().Add(2 * time.Second)
	for sidebarKanbanReviewCount(s) != 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := sidebarKanbanReviewCount(s); got != 2 {
		t.Fatalf("待审阅任务数 = %d, want 2", got)
	}
	if calls := fake.KanbanBoardCalls(); calls != 1 {
		t.Fatalf("KanbanBoard 调用次数 = %d, want 1", calls)
	}

	// 30 秒节流：立即再次刷新不会发起新请求
	refreshKanbanBadge(s)
	if calls := fake.KanbanBoardCalls(); calls != 1 {
		t.Fatalf("节流期内不应再次请求，KanbanBoard 调用次数 = %d, want 1", calls)
	}
}

// TestSidebarFooter_KanbanBadgeSkipsNilClient 验证 client 为空时刷新静默跳过。
func TestSidebarFooter_KanbanBadgeSkipsNilClient(t *testing.T) {
	s := newChatState(nil)

	refreshKanbanBadge(s)

	if got := sidebarKanbanReviewCount(s); got != 0 {
		t.Fatalf("无 client 时缓存应保持 0，got %d", got)
	}
}

func TestSidebarFooter_AttentionBadge(t *testing.T) {
	fake := newAttentionFakeClient(
		AttentionItem{ID: "review", Kind: "kanban_review"},
		AttentionItem{ID: "failed", Kind: "kanban_failed"},
		AttentionItem{ID: "thread", Kind: "thread_failed"},
	)
	s := newChatState(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(func(c *ui.Context) { renderSidebarBottomBar(c, s) }, 500, 100)
	waitFor(t, tt, func() bool { _, ok := tt.Find("2"); return ok })
	if fake.KanbanBoardCalls() != 0 || fake.watchCount() != 1 {
		t.Fatal("收件箱可用时应订阅收件箱，不应轮询看板")
	}

	s.resolveAttentionItem("review")
	waitFor(t, tt, func() bool { _, ok := tt.Find("1"); return ok })
	waitFor(t, tt, func() bool { return len(fake.resolvedIDs()) == 1 })

	fake.addItems(AttentionItem{ID: "again", Kind: "kanban_review"})
	fake.triggerAttentionChange()
	waitFor(t, tt, func() bool { _, ok := tt.Find("2"); return ok })
}

func TestSidebarFooter_AttentionBadgeFallback(t *testing.T) {
	fake := NewFakeClient()
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{{ID: "review", Column: "review"}}})
	s := newChatState(fake)
	t.Cleanup(s.close)
	if count, ok := sidebarKanbanAttentionCount(s); ok || count != 0 {
		t.Fatalf("无收件箱客户端时应返回 (0, false)，得到 (%d, %v)", count, ok)
	}
	tt := ui.NewTester(func(c *ui.Context) { renderSidebarBottomBar(c, s) }, 500, 100)
	waitFor(t, tt, func() bool { _, ok := tt.Find("1"); return ok })
	if fake.KanbanBoardCalls() != 1 {
		t.Fatalf("应轮询看板一次，实际 %d 次", fake.KanbanBoardCalls())
	}
}

func TestEmptyChatAddProject(t *testing.T) {
	fake := NewFakeClient()
	fake.SetProjectsAndThreads(nil, nil)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("暂无项目") })
	if tt.HasText("新用户引导") || tt.HasText("选择或新建一个会话") {
		t.Fatalf("无项目时应显示添加项目入口: %q", tt.Texts())
	}
	count := 0
	for _, text := range tt.Texts() {
		if text == "添加项目" {
			count++
		}
	}
	if count < 4 {
		t.Fatalf("主区缺少添加项目按钮: %q", tt.Texts())
	}
}

func TestEmptyChatAddProjectCloneClosesPopover(t *testing.T) {
	fake := NewFakeClient()
	fake.SetProjectsAndThreads(nil, nil)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("暂无项目") })
	clicked := false
	for _, node := range tt.AccessAll() {
		if node.Role == ui.RoleButton && node.Name == "添加项目" && node.Bounds.X > SidebarDefaultWidth {
			tt.ClickAt(node.Bounds.X+node.Bounds.W/2, node.Bounds.Y+node.Bounds.H/2)
			clicked = true
			break
		}
	}
	if !clicked {
		t.Fatal("找不到主区域添加项目按钮")
	}
	waitFor(t, tt, func() bool { return tt.HasText("从 GitHub 克隆…") })
	if err := tt.Click("从 GitHub 克隆…"); err != nil {
		t.Fatalf("点击克隆失败: %v", err)
	}
	if getSidebarFooterState(state).chatAddProjectOpen {
		t.Fatal("克隆后空白页添加项目浮层仍然打开")
	}
}

func TestSidebarFooterUnified(t *testing.T) {
	countText := func(texts []string, target string) int {
		n := 0
		for _, s := range texts {
			if s == target {
				n++
			}
		}
		return n
	}

	t.Run("layout_across_widths", func(t *testing.T) {
		for _, width := range []int{200, 280, 360} {
			t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
				s := newChatState(NewFakeClient())
				t.Cleanup(s.close)
				setChatSettings(s, NewSettingsState(settingsTestDir(t), nil, s))
				s.mu.Lock()
				s.sidebarWidth = float32(width)
				s.mu.Unlock()

				tt := ui.NewTester(func(c *ui.Context) { renderSidebarBottomBar(c, s) }, width, 100)
				tt.Frame()

				texts := tt.Texts()
				addBtn, ok := tt.AccessByRole(ui.RoleButton, "添加项目")
				if !ok {
					t.Fatalf("宽度 %d 缺少「添加项目」按钮: texts=%q", width, texts)
				}
				if countText(texts, "添加项目") < 2 || addBtn.Bounds.W <= ControlSM {
					t.Fatalf("宽度 %d 左侧「添加项目」应保持带文字主按钮形态: bounds=%+v texts=%q", width, addBtn.Bounds, texts)
				}

				for _, label := range []string{"任务", "自动化", "设置"} {
					if countText(texts, label) > 1 {
						t.Fatalf("宽度 %d 不应出现右侧文字按钮 %q: texts=%q", width, label, texts)
					}
				}

				kanbanBtn, ok := tt.AccessByRole(ui.RoleButton, "任务")
				if !ok {
					t.Fatalf("宽度 %d 缺少「任务」图标按钮", width)
				}
				autoBtn, ok := tt.AccessByRole(ui.RoleButton, "自动化")
				if !ok {
					t.Fatalf("宽度 %d 缺少「自动化」图标按钮", width)
				}
				settingsBtn, ok := tt.AccessByRole(ui.RoleButton, "设置")
				if !ok {
					t.Fatalf("宽度 %d 缺少「设置」图标按钮", width)
				}

				rightBtns := []struct {
					label string
					info  ui.AccessInfo
				}{
					{"任务", kanbanBtn},
					{"自动化", autoBtn},
					{"设置", settingsBtn},
				}
				for _, b := range rightBtns {
					if b.info.Bounds.W != ControlSM || b.info.Bounds.H != ControlSM {
						t.Fatalf("宽度 %d 右侧按钮 %q 应为 ControlSM(%v) 紧凑图标按钮: %+v", width, b.label, ControlSM, b.info.Bounds)
					}
				}

				all := []ui.Rect{addBtn.Bounds, kanbanBtn.Bounds, autoBtn.Bounds, settingsBtn.Bounds}
				for i, r := range all {
					if r.X < -0.5 || r.X+r.W > float32(width)+0.5 {
						t.Fatalf("宽度 %d 按钮 %d 越界: %+v", width, i, r)
					}
					if i > 0 && all[i-1].X+all[i-1].W > r.X+0.5 {
						t.Fatalf("宽度 %d 按钮顺序或间距重叠: prev=%+v cur=%+v", width, all[i-1], r)
					}
				}
			})
		}
	})

	t.Run("kanban_badge_number_only", func(t *testing.T) {
		t.Run("zero_hidden", func(t *testing.T) {
			fake := newAttentionFakeClient()
			s := newChatState(fake)
			t.Cleanup(s.close)
			tt := ui.NewTester(func(c *ui.Context) { renderSidebarBottomBar(c, s) }, 280, 100)
			tt.Frame()

			if tt.HasText("0") {
				t.Fatalf("任务角标计数为 0 时不应显示: texts=%q", tt.Texts())
			}
			if _, ok := tt.Find("0"); ok {
				t.Fatalf("任务角标计数为 0 时不应存在 0 节点")
			}
		})

		t.Run("positive_number_only", func(t *testing.T) {
			fake := newAttentionFakeClient(
				AttentionItem{ID: "r1", Kind: "kanban_review"},
				AttentionItem{ID: "r2", Kind: "kanban_failed"},
				AttentionItem{ID: "r3", Kind: "kanban_approval"},
			)
			s := newChatState(fake)
			t.Cleanup(s.close)
			tt := ui.NewTester(func(c *ui.Context) { renderSidebarBottomBar(c, s) }, 280, 100)
			waitFor(t, tt, func() bool { _, ok := tt.Find("3"); return ok })

			texts := tt.Texts()
			if countText(texts, "任务") > 1 {
				t.Fatalf("任务按钮与角标不应包含文字标签「任务」: texts=%q", texts)
			}
			if countText(texts, "3") < 1 {
				t.Fatalf("任务角标应显示数字 3: texts=%q", texts)
			}
		})
	})
}
