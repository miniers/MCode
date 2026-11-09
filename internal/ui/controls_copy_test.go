package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestCopyButtonModes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mode       CopyButtonMode
		text, icon bool
	}{
		{"inline", CopyInline, true, true},
		{"icon", CopyIcon, false, true},
		{"text", CopyText, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tester := ui.NewTester(func(c *ui.Context) {
				CopyButton(c, CopyButtonProps{Label: "复制按钮", Text: "复制内容", Mode: tc.mode, Value: func() string { return "内容" }})
			}, 300, 100)
			if got := tester.HasText("复制内容"); got != tc.text {
				t.Fatalf("文字可见 = %v，期望 %v", got, tc.text)
			}
			if _, got := tester.Find("复制图标"); got != tc.icon {
				t.Fatalf("图标可见 = %v，期望 %v", got, tc.icon)
			}
		})
	}
}

func TestCopyButtonClick(t *testing.T) {
	for _, tc := range []struct {
		name               string
		disabled, nilValue bool
		want               int
	}{
		{"正常", false, false, 1},
		{"禁用", true, false, 0},
		{"无内容回调", false, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values, copies := 0, 0
			var value func() string
			if !tc.nilValue {
				value = func() string { values++; return "内容" }
			}
			tester := ui.NewTester(func(c *ui.Context) {
				CopyButton(c, CopyButtonProps{Label: "复制按钮", Text: "复制", Disabled: tc.disabled, Value: value, OnCopy: func() { copies++ }})
			}, 300, 100)
			if values != 0 || copies != 0 {
				t.Fatal("点击前调用了回调")
			}
			if err := tester.Click("复制按钮"); err != nil {
				t.Fatal(err)
			}
			if values != tc.want || copies != tc.want {
				t.Fatalf("Value 调用 %d 次，OnCopy 调用 %d 次，期望 %d 次", values, copies, tc.want)
			}
		})
	}
}

func TestCopyButtonCopiedIcon(t *testing.T) {
	tester := ui.NewTester(func(c *ui.Context) {
		CopyButton(c, CopyButtonProps{Label: "复制按钮", Text: "复制", Copied: true})
	}, 300, 100)
	if _, ok := tester.Find("复制成功"); !ok {
		t.Fatal("没有显示成功图标")
	}
	if _, ok := tester.Find("复制图标"); ok {
		t.Fatal("成功时仍显示复制图标")
	}
}
