package mcodeui

import "testing"

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
