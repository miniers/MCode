package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSettingsWindowKeepsChatActions(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	state, view := buildRoot(NewFakeClient(), WithSettings(settingsTestDir(t), nil))
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })

	st := getChatSettings(state)
	st.Open()
	state.enqueueAction("toggle-sidebar", 0)
	collapsed := state.sidebarCollapsed
	tt.Frame()

	if _, ok := tt.Find("消息输入框"); !ok {
		t.Fatal("设置打开时聊天输入框应保留在界面中")
	}
	if !st.IsOpen() || state.sidebarCollapsed == collapsed {
		t.Fatal("设置打开时工作区菜单动作应正常执行")
	}
	st.Close()
}
