package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestAutomationShellActionsAndLeaveGuard(t *testing.T) {
	s := &chatState{}
	as := getAutomationState(s)
	called := 0
	next := func() { called++ }
	if !prepareAutomationLeave(s, next) || called != 0 {
		t.Fatal("无修改时应由调用者继续")
	}
	as.editorOpen = true
	as.name = "修改"
	if prepareAutomationLeave(s, next) || !as.confirmDiscard || as.pendingLeave == nil {
		t.Fatal("未保护未保存修改")
	}
	if !handleAutomationEscape(s) || as.pendingLeave != nil || called != 0 {
		t.Fatal("取消确认后不应离开")
	}
}

func TestAutomationShellCreateChooseProject(t *testing.T) {
	s := &chatState{projects: []Project{{ID: "p1"}, {ID: "p2"}}}
	as := getAutomationState(s)
	as.projectID = ""
	var choose func(string)
	requestNewAutomation(s, "", func(callback func(string)) { choose = callback })
	if choose == nil || as.editorOpen {
		t.Fatal("应先选择项目")
	}
	choose("p2")
	choose("p1")
	if !as.editorOpen || as.editorProjectID != "p2" || as.projectID != "" {
		t.Fatal("所选项目或范围不正确")
	}
	as.editorOpen = false
	requestNewAutomation(s, "p1", func(func(string)) { t.Fatal("单项目不应选择项目") })
	if !as.editorOpen || as.editorProjectID != "p1" {
		t.Fatal("单项目新建失败")
	}
}

func TestAutomationShellEscape(t *testing.T) {
	s := &chatState{}
	as := getAutomationState(s)
	as.editorOpen = true
	as.name = "修改"
	if !handleAutomationEscape(s) || !as.confirmDiscard {
		t.Fatal("未保护草稿")
	}
	if !handleAutomationEscape(s) || as.confirmDiscard || !as.editorOpen {
		t.Fatal("未关闭确认")
	}
	as.editorOpen = false
	as.openRunID = "run"
	as.narrowDetail = true
	if !handleAutomationEscape(s) || as.openRunID != "" || !as.narrowDetail {
		t.Fatal("未先返回运行列表")
	}
	if !handleAutomationEscape(s) || as.narrowDetail {
		t.Fatal("未返回自动化列表")
	}
	if handleAutomationEscape(s) {
		t.Fatal("无可关闭内容时不应消费")
	}
}

func TestAutomationShellLeaveDiscardRunsNextOnce(t *testing.T) {
	tt, s := newWorkspaceTester(t, NewFakeClient(), "automations")
	waitFor(t, tt, func() bool { return tt.HasText("此项目没有自动化") || tt.HasText("还没有自动化") })
	requestNewAutomation(s, "p1", nil)
	waitFor(t, tt, func() bool { return tt.Focused("automation-name-input") })
	tt.Type("草稿")
	called := 0
	next := func() { called++ }
	if prepareAutomationLeave(s, next) {
		t.Fatal("草稿未阻止离开")
	}
	waitFor(t, tt, func() bool { return tt.HasText("继续编辑") })
	if err := tt.Click("继续编辑"); err != nil {
		t.Fatal(err)
	}
	if called != 0 {
		t.Fatal("继续编辑执行了回调")
	}
	if prepareAutomationLeave(s, next) {
		t.Fatal("草稿未阻止离开")
	}
	waitFor(t, tt, func() bool { return tt.HasText("放弃") })
	if err := tt.Click("放弃"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if called != 1 {
		t.Fatalf("回调执行 %d 次", called)
	}
}

func TestAutomationShellClearSearchIsolation(t *testing.T) {
	s := &chatState{}
	as := getAutomationState(s)
	as.initialized, as.filter, as.unreadOnly = true, 1, true
	autoSearch, taskSearch := "自动化", "任务"
	tt := ui.NewTester(func(c *ui.Context) {
		renderAutomationWorkspaceInScope(c, s, automationWorkspaceProps{SearchText: autoSearch, ClearSearch: func() { autoSearch = "" }})
	}, 800, 600)
	tt.Frame()
	if err := tt.Click("清除筛选"); err != nil {
		t.Fatal(err)
	}
	if autoSearch != "" || taskSearch != "任务" || as.filter != 0 || as.unreadOnly {
		t.Fatal("清除筛选影响了其他搜索或未重置筛选")
	}
}

func TestAutomationVisualEmptyStates(t *testing.T) {
	for _, tc := range []struct {
		name, scope, search, want string
		loading                   bool
		err                       string
	}{
		{name: "all", want: "还没有自动化"},
		{name: "project", scope: "p1", want: "此项目没有自动化"},
		{name: "filtered", search: "x", want: "无匹配结果"},
		{name: "loading", loading: true, want: "正在加载自动化…"},
		{name: "error", err: "读取失败", want: "读取失败"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &chatState{}
			as := getAutomationState(s)
			as.initialized, as.projectID, as.loading, as.err = true, tc.scope, tc.loading, tc.err
			tt := ui.NewTester(func(c *ui.Context) {
				renderAutomationWorkspaceInScope(c, s, automationWorkspaceProps{ScopeID: tc.scope, SearchText: tc.search})
			}, 800, 600)
			tt.Frame()
			if !tt.HasText(tc.want) {
				t.Fatalf("缺少 %s", tc.want)
			}
			for _, text := range []string{"还没有自动化", "此项目没有自动化", "无匹配结果", "正在加载自动化…", "读取失败"} {
				if text != tc.want && tt.HasText(text) {
					t.Fatalf("错误显示 %s", text)
				}
			}
		})
	}
}
