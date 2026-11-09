package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestAvatarContent(t *testing.T) {
	for _, tc := range []struct {
		name, text, want, absent string
		icon                     *ui.SVG
	}{
		{name: "  中文名 ", want: "中", absent: "中文名"},
		{name: "   ", absent: "中"},
		{name: "中文名", text: "👩‍💻", want: "👩‍💻", absent: "中"},
		{name: "中文名", text: "文字", icon: IconSearch(), absent: "文字"},
	} {
		t.Run(tc.name+tc.text, func(t *testing.T) {
			tester := ui.NewTester(func(c *ui.Context) {
				Avatar(c, AvatarProps{Name: tc.name, Text: tc.text, Icon: tc.icon, Size: 40, FontSize: 16}).Label("头像外框")
			}, 100, 100)
			if tc.want != "" && !tester.HasText(tc.want) {
				t.Errorf("头像没有显示 %q", tc.want)
			}
			if tc.absent != "" && tester.HasText(tc.absent) {
				t.Errorf("头像不应显示 %q", tc.absent)
			}
			if tc.icon != nil && tester.HasText("中") {
				t.Error("图标存在时不应显示名字首字")
			}
			r, ok := tester.Find("头像外框")
			if !ok || r.W != 40 || r.H != 40 {
				t.Errorf("头像尺寸 = %v，存在 = %v", r, ok)
			}
		})
	}
}

func TestAvatarInsideButton(t *testing.T) {
	clicks := 0
	tester := ui.NewTester(func(c *ui.Context) {
		ui.ButtonBase(c).Size(40, 40).Label("头像按钮").OnClick(func() { clicks++ }).Children(func() {
			Avatar(c, AvatarProps{Name: "中文", Size: 30, FontSize: 14})
		})
	}, 100, 100)
	if err := tester.Click("头像按钮"); err != nil {
		t.Fatal(err)
	}
	if clicks != 1 {
		t.Fatalf("点击次数 = %d", clicks)
	}
}
