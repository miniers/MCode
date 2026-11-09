package mcodeui

import (
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func recordStewardSettingsPage(t *testing.T, state *chatState) *string {
	t.Helper()
	setChatSettings(state, NewSettingsState(settingsTestDir(t), nil, state))
	old := settingsWindowOpener
	var page string
	settingsWindowOpener = func(_ *chatState, requested string) *mygo.Window {
		page = requested
		return nil
	}
	t.Cleanup(func() { settingsWindowOpener = old })
	return &page
}

func TestStewardOpenDisabledGoesToSettings(t *testing.T) {
	for _, threadID := range []string{"", "existing"} {
		t.Run("thread="+threadID, func(t *testing.T) {
			fake := &stewardEnabledFakeClient{stewardFakeClient: newStewardFakeClient(threadID)}
			state, _ := buildRoot(fake)
			t.Cleanup(state.close)
			page := recordStewardSettingsPage(t, state)
			state.openStewardThread()
			if *page != "steward" || fake.openCalls() != 0 {
				t.Fatalf("未启用时应打开总管设置且不创建会话：页面 %q，创建 %d 次", *page, fake.openCalls())
			}
		})
	}
}

func TestStewardOpenEnabledCreatesThread(t *testing.T) {
	fake := &stewardEnabledFakeClient{stewardFakeClient: newStewardFakeClient(""), enabled: true}
	addStewardTestThread(fake.stewardFakeClient)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	page := recordStewardSettingsPage(t, state)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return selectedThreadForTest(state) == "t1" })
	state.openStewardThread()
	waitFor(t, tt, func() bool { return selectedThreadForTest(state) == stewardTestThreadID })
	if *page != "" || fake.openCalls() != 1 {
		t.Fatalf("已启用时应创建会话而非打开设置：页面 %q，创建 %d 次", *page, fake.openCalls())
	}
}

func TestStewardPaletteSettingsEntry(t *testing.T) {
	fake := newStewardFakeClient("")
	fake.name = "阿默"
	state, _ := buildRoot(fake)
	t.Cleanup(state.close)
	page := recordStewardSettingsPage(t, state)
	for _, item := range stewardPaletteItems(state) {
		if item.id == "cmd-steward-settings" {
			if item.kind != "command" || item.title != "阿默设置" || item.run == nil {
				t.Fatalf("总管设置命令不正确：%+v", item)
			}
			item.run(state)
			if *page != "steward" {
				t.Fatalf("应打开总管设置，实际页面 %q", *page)
			}
			return
		}
	}
	t.Fatal("缺少总管设置命令")
}

func TestStewardHeaderSettingsEntry(t *testing.T) {
	fake := newStewardFakeClient(stewardTestThreadID)
	fake.name = "阿默"
	addStewardTestThread(fake)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	page := recordStewardSettingsPage(t, state)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return selectedThreadForTest(state) == "t1" })
	if _, ok := tt.Find("阿默设置"); ok {
		t.Fatal("普通会话不应显示总管设置按钮")
	}
	state.selectThread(stewardTestThreadID)
	waitFor(t, tt, func() bool { _, ok := tt.Find("阿默设置"); return ok })
	if err := tt.Click("阿默设置"); err != nil {
		t.Fatalf("点击总管设置失败：%v", err)
	}
	if *page != "steward" {
		t.Fatalf("应打开总管设置，实际页面 %q", *page)
	}
}
