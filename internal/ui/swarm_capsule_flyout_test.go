package mcodeui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestSwarmCapsuleFlyout(t *testing.T) {
	entry, capture := setupPopoverFlyoutTest(t)
	const leadID = "capsule-flyout-lead"
	const beeID = "capsule-flyout-bee"
	key := leadID + "|" + beeID
	t.Cleanup(func() {
		swarmFloatMu.Lock()
		delete(swarmCapsuleOpen, key)
		delete(swarmCapsuleDismissed, key)
		swarmFloatMu.Unlock()
	})

	report := strings.Repeat("长汇报正文，用于确认内容按自然高度展开。\n", 100) + "\n自然高度末尾标记"
	host := ui.NewTester(func(c *ui.Context) {
		entry.services = c.Services()
		renderSwarmTimelineCapsules(c, &chatState{}, swarmCapsuleRowID, leadID, []SwarmBee{{
			ThreadID: beeID,
			Name:     "Aspen",
			Status:   "completed",
			Summary:  report,
		}})
	}, 800, 600)
	if err := host.Click("子线程 Aspen 已完成"); err != nil {
		t.Fatal(err)
	}
	if len(capture.options) != 1 || capture.options[0].Width != int(swarmCapsulePopoverWidth) {
		t.Fatalf("胶囊浮层参数错误: %+v", capture.options)
	}

	menu := ui.NewTester(popoverTestView(t, entry.services, "swarm-capsule-popover-"+key), int(swarmCapsulePopoverWidth), 2000)
	menu.Frame()
	if !menu.HasText("长汇报正文，用于确认内容按自然高度展开。") {
		t.Fatalf("浮层内容缺失: %v", menu.Texts())
	}
	if !menu.HasText("自然高度末尾标记") {
		t.Fatal("长汇报正文未完整渲染")
	}
	if bounds, ok := menu.Find("自然高度末尾标记"); !ok || bounds.Y <= 520 {
		t.Fatalf("长汇报未按自然高度展开: %+v, found=%v", bounds, ok)
	}
	headerBounds, okHeader := menu.Find("Aspen")
	if bounds, ok := menu.Find("打开浮窗"); !ok || !okHeader || bounds.Y <= headerBounds.Y || bounds.Y+bounds.H > 2000 {
		t.Fatalf("打开浮窗按钮未位于顶栏下方的底部操作栏: header=%+v btn=%+v found=%v", headerBounds, bounds, ok)
	}

	capture.closed[0]()
	host.Frame()
	swarmFloatMu.Lock()
	dismissed := swarmCapsuleDismissed[key]
	swarmFloatMu.Unlock()
	if !dismissed {
		t.Fatal("系统关闭后未记录 dismissed")
	}
	host.Frame()
	swarmFloatMu.Lock()
	dismissed = swarmCapsuleDismissed[key]
	swarmFloatMu.Unlock()
	if dismissed {
		t.Fatal("dismissed 不应在后续帧重复出现")
	}
	if err := host.Click("子线程 Aspen 已完成"); err != nil {
		t.Fatal(err)
	}
	menu = ui.NewTester(popoverTestView(t, entry.services, "swarm-capsule-popover-"+key), int(swarmCapsulePopoverWidth), 2000)
	if err := menu.Click("打开浮窗"); err != nil {
		t.Fatal(err)
	}
	host.Frame()
	swarmFloatMu.Lock()
	open := swarmCapsuleOpen[key]
	swarmFloatMu.Unlock()
	if open || len(capture.destroyedWindows) != 1 {
		t.Fatal("打开子线程浮窗后胶囊浮层未关闭")
	}
}

func TestSwarmReportCapsuleFlyout(t *testing.T) {
	entry, capture := setupPopoverFlyoutTest(t)
	fake := NewFakeClient()
	s := newChatState(fake)
	s.selectedThreadID = "report-flyout-thread"
	msg := Message{
		ID:   "report-flyout-message",
		Role: "user",
		Text: "[编排] 子代理汇报：Ives 已结束工作。\n\n## Ives：Fix parser (已完成)\n- 合并结果：已合并\n- 汇报总结：解析器已修复。",
	}
	host := ui.NewTester(func(c *ui.Context) {
		entry.services = c.Services()
		s.init(c)
		renderSwarmReportRow(c, s, msg)
	}, 800, 600)
	if err := host.Click("子代理汇报详情"); err != nil {
		t.Fatal(err)
	}
	if len(capture.options) != 1 || capture.options[0].Width != int(swarmCapsulePopoverWidth) {
		t.Fatalf("汇报胶囊未创建正确浮层: %+v", capture.options)
	}
	menu := ui.NewTester(popoverTestView(t, entry.services, "swarm-capsule-popover-report-flyout-message#0"), int(swarmCapsulePopoverWidth), 600)
	menu.Frame()
	if !menu.HasText("解析器已修复") {
		t.Fatalf("汇报浮层内容缺失: %v", menu.Texts())
	}
}

func TestSwarmReportPopoverFooterLayout(t *testing.T) {
	const (
		constrainedH = float32(360)
		agentName    = "Gwen-Architect-Reviewer"
		taskText     = "核查报告浮层顶栏全宽展示与底部固定操作栏"
		longModel    = "anthropic/claude-opus-4-5-20251101-thinking-extended-identifier"
	)
	longReport := strings.Repeat("这是一段很长的汇报正文，用于验证浮层滚动区受限后底部操作栏仍然固定在可见区域内。\n", 80) + "\n长汇报末尾标记"

	assertLayout := func(t *testing.T, tt *ui.Tester) {
		t.Helper()
		if tt.HasText("任务目标") || tt.HasText("工作汇报") {
			t.Fatalf("不应再出现「任务目标」或「工作汇报」前缀: %v", tt.Texts())
		}
		if !tt.HasText(taskText) {
			t.Fatalf("任务引用条内容缺失: %v", tt.Texts())
		}
		if _, ok := tt.Find("18 次工具调用"); !ok {
			t.Fatalf("顶栏「18 次工具调用」未完整显示（可能被截断）: %v", tt.Texts())
		}
		if _, ok := tt.Find("6 个文件"); !ok {
			t.Fatalf("顶栏「6 个文件」未完整显示（可能被截断）: %v", tt.Texts())
		}
		headerRect, ok := tt.Find(agentName)
		if !ok {
			t.Fatalf("未找到顶栏名字 %q: %v", agentName, tt.Texts())
		}

		openFloatRect, ok := tt.Find("打开浮窗")
		if !ok {
			t.Fatalf("未找到「打开浮窗」按钮: %v", tt.Texts())
		}
		openThreadRect, ok := tt.Find("查看会话")
		if !ok {
			t.Fatalf("未找到「查看会话」按钮: %v", tt.Texts())
		}
		copyRect, ok := tt.Find("复制汇报")
		if !ok {
			t.Fatalf("未找到「复制汇报」按钮: %v", tt.Texts())
		}
		for _, item := range []struct {
			name string
			rect ui.Rect
		}{
			{"打开浮窗", openFloatRect},
			{"查看会话", openThreadRect},
			{"复制汇报", copyRect},
		} {
			if item.rect.Y <= headerRect.Y {
				t.Fatalf("操作按钮 %q 应位于顶栏下方: header=%+v btn=%+v", item.name, headerRect, item.rect)
			}
			if item.rect.Y < 0 || item.rect.Y+item.rect.H > constrainedH {
				t.Fatalf("操作按钮 %q 超出可见高度 %.0f: btn=%+v", item.name, constrainedH, item.rect)
			}
		}
		if !(openFloatRect.X < openThreadRect.X && openThreadRect.X < copyRect.X) {
			t.Fatalf("底栏按钮顺序错误: openFloat=%+v openThread=%+v copy=%+v", openFloatRect, openThreadRect, copyRect)
		}
	}

	t.Run("时间线胶囊路径", func(t *testing.T) {
		entry, _ := setupPopoverFlyoutTest(t)
		const leadID = "footer-layout-lead"
		const beeID = "footer-layout-bee"
		key := leadID + "|" + beeID
		t.Cleanup(func() {
			swarmFloatMu.Lock()
			delete(swarmCapsuleOpen, key)
			delete(swarmCapsuleDismissed, key)
			swarmFloatMu.Unlock()
		})

		started := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
		finished := started.Add(73 * time.Second)
		s := newChatState(NewFakeClient())
		t.Cleanup(s.close)
		host := ui.NewTesterWith(func(c *ui.Context) {
			entry.services = c.Services()
			renderSwarmTimelineCapsules(c, s, swarmCapsuleRowID, leadID, []SwarmBee{{
				ThreadID:     beeID,
				Name:         agentName,
				Task:         taskText,
				Status:       "completed",
				Tier:         "read",
				ReadOnly:     true,
				Model:        longModel,
				DirectModel:  true,
				ToolCalls:    18,
				ChangedFiles: []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go"},
				StartedAt:    started,
				FinishedAt:   &finished,
				Summary:      longReport,
			}})
		}, 800, 600, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true, TextScale: 1}})
		if err := host.Click("子线程 " + agentName + " 已完成"); err != nil {
			t.Fatal(err)
		}

		menu := ui.NewTesterWith(popoverTestView(t, entry.services, "swarm-capsule-popover-"+key), int(swarmCapsulePopoverWidth), int(constrainedH), ui.TesterOptions{
			Preferences: ui.Preferences{ReduceMotion: true, TextScale: 1},
		})
		menu.Frame()
		assertLayout(t, menu)
	})

	t.Run("汇报条目卡片路径", func(t *testing.T) {
		s := newChatState(NewFakeClient())
		t.Cleanup(s.close)
		s.selectedThreadID = "footer-report-lead"
		hp := getSwarmPanelState(s)

		reportEntry := swarmReportEntry{
			Name:          agentName,
			Task:          taskText,
			Status:        "已完成",
			DurationMs:    73000,
			ToolCalls:     18,
			ChangedFiles:  []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go"},
			LinesAdded:    248,
			LinesDeleted:  64,
			Tier:          "只读",
			Model:         longModel,
			DirectModel:   true,
			BeeID:         "footer-report-bee",
			Summary:       longReport,
			statsRecorded: true,
			filesRecorded: true,
		}

		tt := ui.NewTesterWith(func(c *ui.Context) {
			renderSwarmReportEntryCard(c, s, hp, "msg-footer-layout", 0, reportEntry)
		}, int(swarmCapsulePopoverWidth), int(constrainedH), ui.TesterOptions{
			Preferences: ui.Preferences{ReduceMotion: true, TextScale: 1},
		})
		tt.Frame()
		assertLayout(t, tt)
	})
}
