package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSettingsSheetHeaderFooterHooks(t *testing.T) {
	origLen := len(settingsTreeSheetDefs)
	shell := settingsSheet{
		ID:             "shell-fixture",
		Title:          "外壳",
		MinWidth:       720,
		BodyOwnsScroll: true,
		Header: func(c *ui.Context, st *SettingsState) {
			ui.Text(c, "身份区").Grow(1).SingleLine()
		},
		Footer: func(c *ui.Context, st *SettingsState) {
			ui.Text(c, "底栏")
		},
		Body: func(c *ui.Context, st *SettingsState) {
			ui.Text(c, "正文")
		},
	}
	settingsTreeSheetDefs = append(settingsTreeSheetDefs, shell)

	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	openSettingsSheet(st, "shell-fixture", "")
	t.Cleanup(func() {
		closeSettingsSheet(st)
		settingsTreeSheetDefs = settingsTreeSheetDefs[:origLen]
	})

	tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreeSheet(c, st, shell) }, 1200, 1000)
	tt.Frame()
	if !tt.HasText("身份区") {
		t.Fatal("缺少 Header 内容")
	}
	if !tt.HasText("底栏") {
		t.Fatal("缺少 Footer 内容")
	}
	if _, ok := tt.Find("设置面板 外壳"); !ok {
		t.Fatal("卡片无障碍 Label 未使用 Title")
	}
	if !tt.HasText("正文") {
		t.Fatal("缺少正文内容")
	}
}

func TestSettingsSheetProviderMinWidth(t *testing.T) {
	sh, ok := settingsTreeSheetOf("provider")
	if !ok {
		t.Fatal("缺少 provider 面板")
	}
	if sh.Width != 720 || sh.MinWidth != 720 {
		t.Fatalf("provider 尺寸 = Width %v / MinWidth %v，期望都是 720", sh.Width, sh.MinWidth)
	}
}
