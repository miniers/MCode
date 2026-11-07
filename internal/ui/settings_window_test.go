package mcodeui

import "testing"

func TestSettingsWindowRejectsMissingOwner(t *testing.T) {
	if openSettingsWindow(nil, "") != nil {
		t.Fatal("缺少所属窗口时不应创建设置窗口")
	}
	owner := newChatState(NewFakeClient())
	defer owner.close()
	if openSettingsWindow(owner, "") != nil {
		t.Fatal("缺少设置状态时不应创建设置窗口")
	}
}

func TestSettingsWindowCloseUnrelatedOwner(t *testing.T) {
	if !closeSettingsWindowForOwner(newChatState(NewFakeClient())) {
		t.Fatal("没有关联窗口时应允许关闭")
	}
}
