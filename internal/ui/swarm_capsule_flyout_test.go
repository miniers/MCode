package mcodeui

import (
	"strings"
	"testing"

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

	report := strings.Repeat("长汇报正文，用于确认内容按自然高度展开。\n", 100) + "自然高度末尾标记"
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
	if !menu.HasText("工作汇报") || !menu.HasText("长汇报正文，用于确认内容按自然高度展开。") {
		t.Fatalf("浮层内容缺失: %v", menu.Texts())
	}
	if !menu.HasText("自然高度末尾标记") {
		t.Fatal("长汇报正文未完整渲染")
	}
	if bounds, ok := menu.Find("打开浮窗"); !ok || bounds.Y <= 520 {
		t.Fatalf("长汇报未按自然高度展开: %+v, found=%v", bounds, ok)
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
