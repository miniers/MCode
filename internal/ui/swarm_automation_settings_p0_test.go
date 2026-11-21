package mcodeui

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
	"mcode/internal/git"
)

func TestSwarmApproveDisabled(t *testing.T) {
	sampleFiles := []git.FileDiff{
		{
			OldPath: "foo.go",
			NewPath: "foo.go",
			Status:  "modified",
			Hunks: []git.Hunk{
				{
					Header: "@@ -1,1 +1,2 @@",
					Lines: []git.Line{
						{Kind: ' ', Text: "package main", OldNo: 1, NewNo: 1},
						{Kind: '+', Text: "// added", NewNo: 2},
					},
				},
			},
		},
	}

	for _, tc := range []struct {
		name    string
		loading bool
		files   []git.FileDiff
	}{
		{name: "loading", loading: true, files: sampleFiles},
		{name: "empty_files", loading: false, files: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := NewFakeClient()
			s := newChatState(fake)
			s.selectedThreadID = "lead-1"
			fake.SetSwarmState("lead-1", SwarmState{
				LeadThreadID: "lead-1",
				Available:    true,
				Enabled:      true,
				Bees: []SwarmBee{
					{ThreadID: "bee-1", Name: "Bee1", Status: "completed", PendingMerge: true},
				},
			})

			hp := getSwarmPanelState(s)
			swarmPanelMu.Lock()
			hp.open = true
			hp.loadedFor = "lead-1"
			hp.reviewOpen = true
			hp.reviewBeeID = "bee-1"
			hp.reviewLoading = tc.loading
			hp.reviewDiff = &diffPanelState{collapsed: make(map[string]bool)}
			hp.reviewFiles = tc.files
			swarmPanelMu.Unlock()

			st, _ := fake.Swarm(context.Background(), "lead-1")
			tt := ui.NewTester(func(c *ui.Context) {
				s.init(c)
				renderSwarmMergeReview(c, s, st)
			}, 800, 600)
			tt.Frame()

			btn, ok := tt.AccessByRole(ui.RoleButton, "批准合并")
			if !ok {
				t.Fatal("未找到「批准合并」按钮")
			}
			if !btn.Disabled {
				t.Fatalf("期望「批准合并」处于禁用态")
			}
			if err := tt.Click("批准合并"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if calls := fake.ApproveMergeCalls(); len(calls) != 0 {
				t.Fatalf("禁用态点击仍调用了 approveSwarmMerge: %v", calls)
			}
			for _, label := range []string{"放弃", "关闭"} {
				if _, ok := tt.AccessByRole(ui.RoleButton, label); !ok {
					t.Fatalf("未找到相邻按钮 %q", label)
				}
			}
		})
	}

	t.Run("enabled", func(t *testing.T) {
		fake := NewFakeClient()
		s := newChatState(fake)
		s.selectedThreadID = "lead-1"
		fake.SetSwarmState("lead-1", SwarmState{
			LeadThreadID: "lead-1",
			Available:    true,
			Enabled:      true,
			Bees: []SwarmBee{
				{ThreadID: "bee-1", Name: "Bee1", Status: "completed", PendingMerge: true},
			},
		})

		hp := getSwarmPanelState(s)
		swarmPanelMu.Lock()
		hp.open = true
		hp.loadedFor = "lead-1"
		hp.reviewOpen = true
		hp.reviewBeeID = "bee-1"
		hp.reviewLoading = false
		hp.reviewDiff = &diffPanelState{collapsed: make(map[string]bool)}
		hp.reviewFiles = sampleFiles
		swarmPanelMu.Unlock()

		st, _ := fake.Swarm(context.Background(), "lead-1")
		tt := ui.NewTester(func(c *ui.Context) {
			s.init(c)
			renderSwarmMergeReview(c, s, st)
		}, 800, 600)
		tt.Frame()

		btn, ok := tt.AccessByRole(ui.RoleButton, "批准合并")
		if !ok || btn.Disabled {
			t.Fatalf("期望「批准合并」可用: ok=%v disabled=%v", ok, btn.Disabled)
		}
		if err := tt.Click("批准合并"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, tt, func() bool {
			calls := fake.ApproveMergeCalls()
			return len(calls) == 1 && calls[0] == "bee-1"
		})
	})
}

func TestAutomationEditorUnsavedCancel(t *testing.T) {
	t.Run("clean_closes_directly", func(t *testing.T) {
		tt, s := newWorkspaceTester(t, NewFakeClient(), "automations")
		waitFor(t, tt, func() bool { return tt.HasText("此项目没有自动化") || tt.HasText("还没有自动化") })
		requestNewAutomation(s, "p1", nil)
		waitFor(t, tt, func() bool { return tt.Focused("automation-name-input") })

		if err := tt.Click("取消编辑"); err != nil {
			t.Fatal(err)
		}
		as := getAutomationState(s)
		waitFor(t, tt, func() bool { return !as.editorOpen })
		if tt.HasText("放弃未保存的修改？") || as.confirmDiscard {
			t.Fatal("未改动草稿不应弹出放弃确认框")
		}
	})

	t.Run("cancel_button_dirty_confirm", func(t *testing.T) {
		tt, s := newWorkspaceTester(t, NewFakeClient(), "automations")
		waitFor(t, tt, func() bool { return tt.HasText("此项目没有自动化") || tt.HasText("还没有自动化") })
		requestNewAutomation(s, "p1", nil)
		waitFor(t, tt, func() bool { return tt.Focused("automation-name-input") })
		tt.Type("草稿")
		tt.Frame()

		if err := tt.Click("取消编辑"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, tt, func() bool { return tt.HasText("放弃未保存的修改？") })
		as := getAutomationState(s)
		if !as.editorOpen || !as.confirmDiscard {
			t.Fatalf("有未保存修改时应保持编辑器打开并显示确认框: editorOpen=%v confirmDiscard=%v", as.editorOpen, as.confirmDiscard)
		}
		if !tt.Focused("继续编辑") {
			t.Fatal("确认框应默认聚焦「继续编辑」(CancelAutoFocus)")
		}
		// 默认聚焦取消按钮时按 Enter 应继续编辑、不丢弃草稿。
		tt.Key(0, ui.KeyEnter)
		waitFor(t, tt, func() bool { return !as.confirmDiscard && !tt.HasText("放弃未保存的修改？") })
		if !as.editorOpen || as.name != "草稿" {
			t.Fatalf("继续编辑后编辑器或草稿丢失: editorOpen=%v name=%q", as.editorOpen, as.name)
		}

		if err := tt.Click("取消编辑"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, tt, func() bool { return tt.HasText("放弃未保存的修改？") })
		if err := tt.Click("放弃"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, tt, func() bool { return !as.editorOpen && !as.confirmDiscard })
	})

	t.Run("escape_dirty_confirm", func(t *testing.T) {
		tt, s := newWorkspaceTester(t, NewFakeClient(), "automations")
		waitFor(t, tt, func() bool { return tt.HasText("此项目没有自动化") || tt.HasText("还没有自动化") })
		requestNewAutomation(s, "p1", nil)
		waitFor(t, tt, func() bool { return tt.Focused("automation-name-input") })
		tt.Type("未保存Esc")
		tt.Frame()

		tt.Key(0, ui.KeyEscape)
		waitFor(t, tt, func() bool { return tt.HasText("放弃未保存的修改？") })
		as := getAutomationState(s)
		if !as.editorOpen || !as.confirmDiscard {
			t.Fatalf("Esc 关闭脏草稿应弹出确认框: editorOpen=%v confirmDiscard=%v", as.editorOpen, as.confirmDiscard)
		}
		if err := tt.Click("继续编辑"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, tt, func() bool { return !as.confirmDiscard && !tt.HasText("放弃未保存的修改？") })
		if !as.editorOpen {
			t.Fatal("点击继续编辑后编辑器被关闭")
		}

		tt.Key(0, ui.KeyEscape)
		waitFor(t, tt, func() bool { return tt.HasText("放弃未保存的修改？") })
		if err := tt.Click("放弃"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, tt, func() bool { return !as.editorOpen && !as.confirmDiscard })
	})
}

func TestProviderRowsKeyboard(t *testing.T) {
	tt, st := openProvidersPageTest(t, NewFakeClient())
	waitFor(t, tt, func() bool { return !tt.HasText("检查中…") })
	tt.Frame()

	focusByTab := func(label string) {
		t.Helper()
		for i := 0; i < 80 && !tt.Focused(label); i++ {
			tt.Key(0, ui.KeyTab)
		}
		if !tt.Focused(label) {
			t.Fatalf("Tab 未能聚焦到 %q", label)
		}
	}

	claudeLabel := providerDisplayName("claude")

	// Enter 激活供应商行
	focusByTab(claudeLabel)
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool {
		sheet, _ := settingsTreeSheetState(st)
		_, id := currentProviderSheet(st)
		return sheet == "provider" && id == "claude"
	})
	closeSettingsSheet(st)
	tt.Frame()

	// Space 激活供应商行
	focusByTab(claudeLabel)
	tt.Key(0, ui.KeySpace)
	waitFor(t, tt, func() bool {
		sheet, _ := settingsTreeSheetState(st)
		_, id := currentProviderSheet(st)
		return sheet == "provider" && id == "claude"
	})
	closeSettingsSheet(st)
	tt.Frame()

	// Enter 激活「添加供应商」
	focusByTab("添加供应商")
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool {
		sheet, _ := settingsTreeSheetState(st)
		return sheet == "provider-add"
	})
	closeSettingsSheet(st)
	tt.Frame()

	// Space 激活「添加供应商」
	focusByTab("添加供应商")
	tt.Key(0, ui.KeySpace)
	waitFor(t, tt, func() bool {
		sheet, _ := settingsTreeSheetState(st)
		return sheet == "provider-add"
	})
}

func TestNumberControlValidation(t *testing.T) {
	st, tt, _ := newNumberFieldTestState(t)

	// 1. 非数字回车提交：回退旧值并显示范围错误提示
	if err := tt.Click("编辑比例宽度"); err != nil {
		t.Fatalf("click percent input failed: %v", err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("abc")
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool { return tt.HasText("请输入 60–100 之间的整数") })
	if got := numberFieldText(tt); got != "85" {
		t.Fatalf("非法输入提交后显示 %q，期望回退 85", got)
	}
	if got := st.cfg.Appearance.ContentWidthPercent; got != config.DefaultContentWidthPercent {
		t.Fatalf("非法输入写入了配置：%d", got)
	}

	// 2. 再次修改输入时清除错误提示
	tt.Type("7")
	waitFor(t, tt, func() bool { return !tt.HasText("请输入 60–100 之间的整数") })

	// 3. 越界失焦提交：回退旧值并显示范围错误提示
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("900")
	if err := tt.Click("紧凑侧边栏"); err != nil {
		t.Fatalf("click elsewhere failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("请输入 60–100 之间的整数") })
	if got := numberFieldText(tt); got != "85" {
		t.Fatalf("越界输入失焦后显示 %q，期望回退 85", got)
	}

	// 4. 提交合法值：落盘且清除错误提示
	if err := tt.Click("编辑比例宽度"); err != nil {
		t.Fatalf("click percent input failed: %v", err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("75")
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool {
		return st.cfg.Appearance.ContentWidthPercent == 75 && !tt.HasText("请输入 60–100 之间的整数")
	})
}
