package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestAutomationDrawerOriginFilterAndHeader(t *testing.T) {
	t.Run("来源筛选逻辑仅保留总管自动化", func(t *testing.T) {
		steward := testAutomation("a-steward", "p1")
		steward.Origin = "steward"
		owner := testAutomation("a-owner", "p1")
		owner.Origin = "owner"
		manual := testAutomation("a-manual", "p1")
		manual.Origin = ""

		autos := []Automation{steward, owner, manual}
		if got := filterAutomationStatusWithOrigin(autos, "", 0, "", false, nil); len(got) != 3 {
			t.Fatalf("全部来源数量 = %d, want 3", len(got))
		}
		got := filterAutomationStatusWithOrigin(autos, "", 0, "steward", false, nil)
		if len(got) != 1 || got[0].ID != "a-steward" {
			t.Fatalf("总管来源筛选结果 = %+v, want [a-steward]", got)
		}
	})

	t.Run("宽屏侧栏来源筛选、未选空态与详情顶栏总管胶囊和状态", func(t *testing.T) {
		fake := NewFakeClient()
		fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "核心项目"}}, nil)
		steward := testAutomation("auto-steward", "p1")
		steward.Name = "总管巡检"
		steward.Origin = "steward"
		steward.LastRunStatus = "failed"
		manual := testAutomation("auto-manual", "p1")
		manual.Name = "手动巡检"
		manual.Origin = ""
		fake.SetAutomations([]Automation{steward, manual})

		tt, s := newWorkspaceTester(t, fake, "automations")
		waitFor(t, tt, func() bool { return tt.HasText("总管巡检") && tt.HasText("手动巡检") })

		if _, ok := tt.Find("automation-select-prompt"); !ok || !tt.HasText("未选择自动化") {
			t.Fatalf("未选择自动化时应展示统一空态 automation-select-prompt: %v", tt.Texts())
		}

		if err := tt.Click("automation-origin-steward"); err != nil {
			t.Fatalf("点击总管来源筛选失败: %v", err)
		}
		waitFor(t, tt, func() bool { return tt.HasText("总管巡检") && !tt.HasText("手动巡检") })
		if as := getAutomationState(s); as.originFilter != "steward" {
			t.Fatalf("originFilter = %q, want steward", as.originFilter)
		}

		if err := tt.Click("自动化项 总管巡检"); err != nil {
			t.Fatalf("选中总管自动化失败: %v", err)
		}
		waitFor(t, tt, func() bool {
			_, hasOrigin := tt.Find("automation-selected-header-origin")
			_, hasStatus := tt.Find("automation-selected-header-status")
			return hasOrigin && hasStatus
		})
		if !tt.HasText("上次失败") {
			t.Fatalf("详情顶栏缺少状态文案「上次失败」: %v", tt.Texts())
		}
		for _, label := range []string{"启用自动化", "立即运行", "自动化更多操作", "运行记录", "配置"} {
			if _, ok := tt.Find(label); !ok {
				t.Fatalf("详情抽屉缺少关键控件 %q", label)
			}
		}

		if err := tt.Click("automation-origin-all"); err != nil {
			t.Fatalf("切回全部来源失败: %v", err)
		}
		waitFor(t, tt, func() bool { return tt.HasText("手动巡检") })
		if err := tt.Click("自动化项 手动巡检"); err != nil {
			t.Fatalf("选中手动自动化失败: %v", err)
		}
		waitFor(t, tt, func() bool {
			_, hasOrigin := tt.Find("automation-selected-header-origin")
			return !hasOrigin
		})
	})

	t.Run("窄屏弹出Sheet不替换列表且关闭后静止不重绘", func(t *testing.T) {
		fake := NewFakeClient()
		fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "核心项目"}}, nil)
		steward := testAutomation("auto-steward", "p1")
		steward.Name = "总管巡检"
		steward.Origin = "steward"
		steward.LastRunStatus = "succeeded"
		fake.SetAutomations([]Automation{steward})

		var s *chatState
		tt := ui.NewTester(Root(fake, func(state *chatState) { s = state }), 840, 520)
		waitFor(t, tt, func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return len(s.projects) > 0
		})
		s.openWorkspace("automations")
		waitFor(t, tt, func() bool { return tt.HasText("总管巡检") })

		if _, ok := tt.Find("automation-detail-sheet"); ok {
			t.Fatal("未点击前不应弹出详情 Sheet")
		}
		if err := tt.Click("自动化项 总管巡检"); err != nil {
			t.Fatalf("窄屏点击自动化失败: %v", err)
		}
		waitFor(t, tt, func() bool {
			_, hasSheet := tt.Find("automation-detail-sheet")
			_, hasOrigin := tt.Find("automation-selected-header-origin")
			return hasSheet && hasOrigin
		})
		if _, ok := tt.Find("自动化项 总管巡检"); !ok {
			t.Fatal("窄屏弹出 Sheet 时不应整页替换列表")
		}
		if !tt.HasText("上次成功") {
			t.Fatalf("窄屏 Sheet 顶栏缺少状态文案「上次成功」: %v", tt.Texts())
		}

		tt.Key(0, ui.KeyEscape)
		waitFor(t, tt, func() bool {
			_, hasSheet := tt.Find("automation-detail-sheet")
			return !hasSheet && !getAutomationState(s).narrowDetail
		})

		for i := 0; i < 30; i++ {
			tt.Advance(20 * time.Millisecond)
			tt.Frame()
		}
		if tt.FramePending() {
			t.Fatal("窄屏 Sheet 关闭并推进时钟后不应继续请求空闲重绘")
		}
	})
}
