package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestTextFieldChange(t *testing.T) {
	value, changed := "", ""
	tester := ui.NewTester(func(c *ui.Context) {
		TextField(c, TextFieldProps{Value: &value, Label: "输入内容", OnChange: func(s string) { changed = s }})
	}, 300, 100)
	if err := tester.Click("输入内容"); err != nil {
		t.Fatal(err)
	}
	tester.Type("测试")
	if value != "测试" || changed != "测试" {
		t.Fatalf("值 = %q，回调 = %q", value, changed)
	}
}

func TestTextFieldClear(t *testing.T) {
	value, changed := "", "未调用"
	tester := ui.NewTester(func(c *ui.Context) {
		TextField(c, TextFieldProps{Value: &value, Label: "输入内容", Clearable: true, OnChange: func(s string) { changed = s }})
	}, 300, 100)
	if _, ok := tester.Find("清空"); ok {
		t.Fatal("空值显示了清空按钮")
	}
	if err := tester.Click("输入内容"); err != nil {
		t.Fatal(err)
	}
	tester.Type("内容")
	if _, ok := tester.Find("清空"); !ok {
		t.Fatal("非空值没有清空按钮")
	}
	if err := tester.Click("清空"); err != nil {
		t.Fatal(err)
	}
	if value != "" || changed != "" {
		t.Fatalf("清空后值 = %q，回调 = %q", value, changed)
	}
}

func TestTextFieldDisabled(t *testing.T) {
	value, called := "内容", false
	tester := ui.NewTester(func(c *ui.Context) {
		TextField(c, TextFieldProps{Value: &value, Label: "禁用输入", Clearable: true, Disabled: true, OnChange: func(string) { called = true }})
	}, 300, 100)
	if err := tester.Click("禁用输入"); err != nil {
		t.Fatal(err)
	}
	tester.Type("测试")
	if err := tester.Click("清空"); err != nil {
		t.Fatal(err)
	}
	if called || value != "内容" {
		t.Fatalf("禁用控件发生变化：值 = %q，回调 = %v", value, called)
	}
}

func TestInputBorderColor(t *testing.T) {
	colors := inputColors{border: ui.Color{R: 1}, strong: ui.Color{R: 2}, accent: ui.Color{R: 3}}
	for _, tc := range []struct {
		name                               string
		focused, hovered, disabled, active bool
		want                               ui.Color
	}{
		{"默认", false, false, false, true, colors.border},
		{"悬停", false, true, false, true, colors.strong},
		{"聚焦优先", true, true, false, true, colors.accent},
		{"禁用", true, true, true, true, colors.border},
		{"窗口失焦", true, true, false, false, colors.border},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := inputBorderColor(tc.focused, tc.hovered, tc.disabled, tc.active, colors); got != tc.want {
				t.Errorf("边框颜色 = %v，期望 %v", got, tc.want)
			}
		})
	}
}

func TestTextFieldSubmitOnEnter(t *testing.T) {
	value, submitted := "", ""
	tester := ui.NewTester(func(c *ui.Context) {
		TextField(c, TextFieldProps{Value: &value, Label: "提交输入", OnSubmit: func(s string) { submitted = s }})
	}, 300, 100)
	if err := tester.Click("提交输入"); err != nil {
		t.Fatal(err)
	}
	tester.Type("文件名")
	tester.Key(0, ui.KeyEnter)
	tester.Frame()
	if submitted != "文件名" || value != "文件名" {
		t.Fatalf("回车提交 = %q，值 = %q", submitted, value)
	}
}

func TestTextFieldWidth(t *testing.T) {
	value := ""
	tester := ui.NewTester(func(c *ui.Context) {
		TextField(c, TextFieldProps{Value: &value, Label: "定宽输入", Width: 220}).Label("定宽外框")
	}, 500, 100)
	r, ok := tester.Find("定宽外框")
	if !ok {
		t.Fatal("找不到定宽输入")
	}
	if r.W != 220 {
		t.Fatalf("宽度 = %v，期望 220", r.W)
	}
}

func TestSearchFieldHeight(t *testing.T) {
	query := ""
	tester := ui.NewTester(func(c *ui.Context) {
		SearchField(c, TextFieldProps{Value: &query, Label: "搜索输入"}).Label("搜索外框")
	}, 300, 100)
	r, ok := tester.Find("搜索外框")
	if !ok {
		t.Fatal("找不到搜索输入")
	}
	if r.H != ControlMD {
		t.Fatalf("搜索框高 = %v，期望 %v", r.H, ControlMD)
	}
}
