package mcodeui

import (
	"bytes"
	"testing"

	"github.com/egoist/mygo/ui"
)

func newMenuRowTester(build func(c *ui.Context)) *ui.Tester {
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("menu-row-test-wrap")).
			Width(280).
			Padding(Space2).
			Gap(Space1).
			Children(func() {
				build(c)
			})
	}, 360, 240)
	tt.Frame()
	return tt
}

func TestMenuRow(t *testing.T) {
	t.Run("ClickAndLabelFallback", func(t *testing.T) {
		clicked := 0
		tt := newMenuRowTester(func(c *ui.Context) {
			menuRow(c, menuRowProps{
				Key:   "row-open",
				Title: "打开文件",
				OnClick: func() {
					clicked++
				},
			})
		})
		info, ok := tt.AccessByRole(ui.RoleButton, "打开文件")
		if !ok {
			t.Fatal("Label 为空时应回落为 Title 作为无障碍名")
		}
		if info.Name != "打开文件" {
			t.Fatalf("无障碍名 = %q，期望 %q", info.Name, "打开文件")
		}
		if info.Disabled {
			t.Fatal("正常行无障碍节点不应为 Disabled")
		}
		if err := tt.Click("打开文件"); err != nil {
			t.Fatalf("点击菜单行失败: %v", err)
		}
		tt.Frame()
		if clicked != 1 {
			t.Fatalf("点击应触发一次 OnClick，实际=%d", clicked)
		}
	})

	t.Run("Disabled", func(t *testing.T) {
		clicked := 0
		tt := newMenuRowTester(func(c *ui.Context) {
			menuRow(c, menuRowProps{
				Key:      "row-disabled",
				Label:    "禁用操作",
				Title:    "删除",
				Disabled: true,
				OnClick: func() {
					clicked++
				},
			})
		})
		info, ok := tt.AccessByRole(ui.RoleButton, "禁用操作")
		if !ok {
			t.Fatal("未找到禁用菜单行的无障碍节点")
		}
		if !info.Disabled {
			t.Fatal("Disabled 行无障碍节点应标记为 Disabled")
		}
		tt.ClickAt(info.Bounds.X+info.Bounds.W/2, info.Bounds.Y+info.Bounds.H/2)
		tt.Frame()
		if clicked != 0 {
			t.Fatalf("Disabled 时点击不应触发 OnClick，实际=%d", clicked)
		}
	})

	t.Run("TrailingVisible", func(t *testing.T) {
		tt := newMenuRowTester(func(c *ui.Context) {
			menuRow(c, menuRowProps{
				Key:      "row-trailing",
				Title:    "命令面板",
				Trailing: "⌘K",
				Icon:     IconSettings(),
				Checked:  true,
			})
		})
		if !tt.HasText("命令面板") {
			t.Fatalf("标题文字应可见，实际 Texts=%v", tt.Texts())
		}
		if !tt.HasText("⌘K") {
			t.Fatalf("Trailing 文字「⌘K」应可见，实际 Texts=%v", tt.Texts())
		}
	})
}

func TestUIReview5MenuRowFocusRing(t *testing.T) {
	var focusVisible bool
	tt := newMenuRowTester(func(c *ui.Context) {
		btn := menuRow(c, menuRowProps{
			Key:   "row-focus",
			Title: "焦点行",
		})
		focusVisible = btn.FocusVisible()
	})

	if tt.Focused("焦点行") || focusVisible {
		t.Fatal("未聚焦时菜单行不应处于键盘焦点可见状态")
	}
	rect, ok := tt.Find("焦点行")
	if !ok {
		t.Fatal("未找到菜单行")
	}
	unfocusedImg := tt.Image()
	unfocusedPix := append([]byte(nil), unfocusedImg.Pix...)
	bg := unfocusedImg.At(0, 0)
	midY := int(rect.Y + rect.H/2)
	for dx := 1; dx <= 2; dx++ {
		if got := unfocusedImg.At(int(rect.X)-dx, midY); got != bg {
			t.Fatalf("未聚焦时菜单行外缘 x-%d 不应出现焦点环，got=%v want=%v", dx, got, bg)
		}
	}

	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if !tt.Focused("焦点行") || !focusVisible {
		t.Fatalf("按 Tab 后菜单行应获得 FocusVisible 键盘焦点，focused=%v focusVisible=%v", tt.Focused("焦点行"), focusVisible)
	}
	focusedImg := tt.Image()
	if bytes.Equal(unfocusedPix, focusedImg.Pix) {
		t.Fatal("按 Tab 聚焦后渲染帧应出现焦点环阴影，但像素未变化")
	}
	ringVisible := false
	for dx := 1; dx <= 2; dx++ {
		if focusedImg.At(int(rect.X)-dx, midY) != bg {
			ringVisible = true
			break
		}
	}
	if !ringVisible {
		t.Fatal("按 Tab 聚焦后菜单行外缘 1-2px 未检测到焦点环阴影像素")
	}
}

func TestMenuRowLongTrailingKeepsTitle(t *testing.T) {
	const (
		title   = "打包发布"
		longCmd = "./scripts/package_personal.sh --no-relaunch --window-size 1200x800 --verbose --skip-notarize"
	)

	assertRow := func(t *testing.T, wrap func(c *ui.Context, child func())) {
		t.Helper()
		var naturalTitleW float32
		tt := ui.NewTester(func(c *ui.Context) {
			naturalTitleW, _ = c.MeasureText(0, ui.Span{Text: title, Size: uiFont(TypeBody)})
			wrap(c, func() {
				menuRow(c, menuRowProps{
					Key:      "row-long-trailing",
					Label:    "脚本：打包发布",
					Icon:     IconTerminal(),
					Title:    title,
					Trailing: longCmd,
				})
			})
		}, 400, 240)
		tt.Frame()

		if !tt.HasText(title) {
			t.Fatalf("标题文字 %q 应可见，实际 Texts=%v", title, tt.Texts())
		}
		if naturalTitleW <= 0 {
			t.Fatalf("标题自然宽度应大于 0，实际=%v", naturalTitleW)
		}
		titleRect, ok := tt.Find(title)
		if !ok {
			t.Fatalf("未找到标题文本节点 %q", title)
		}
		if titleRect.W < naturalTitleW {
			t.Fatalf("标题布局宽度不足以完整显示: got %.2f, want >= %.2f", titleRect.W, naturalTitleW)
		}
		trailingRect, ok := tt.Find(longCmd)
		if !ok {
			t.Fatalf("未找到 Trailing 文本节点")
		}
		if trailingRect.W <= 0 || trailingRect.W > menuRowTrailingMaxWidth {
			t.Fatalf("Trailing 宽度应大于 0 且不超过上限 %.2f，实际=%.2f", menuRowTrailingMaxWidth, trailingRect.W)
		}
	}

	t.Run("MaxWidth320", func(t *testing.T) {
		assertRow(t, func(c *ui.Context, child func()) {
			ui.Column(c.Key("menu-row-long-trailing-wrap")).
				MaxWidth(320).
				Padding(Space1).
				Children(child)
		})
	})

	t.Run("NarrowWidth180", func(t *testing.T) {
		assertRow(t, func(c *ui.Context, child func()) {
			ui.Column(c.Key("menu-row-long-trailing-narrow")).
				Width(180).
				Padding(Space1).
				Children(child)
		})
	})
}

func TestUIReview8MenuFocusGroupArrows(t *testing.T) {
	open := true
	clicked2 := 0
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("menu-focus-group-wrap")).
			Width(320).
			Padding(Space2).
			Children(func() {
				trigger := Button(c.Key("menu-focus-group-trigger"), ButtonOpts{
					Text:  "菜单",
					Label: "菜单",
				})
				anchoredMenu(c, "menu-focus-group", trigger, &open, nil, 180, 240, 200, func() {
					popoverCard(c, ui.Column(c.Key("menu-focus-group-card")).Padding(Space1).Gap(Space0).Children(func() {
						menuRow(c, menuRowProps{Key: "row-1", Title: "第一项"})
						menuRow(c, menuRowProps{
							Key:   "row-2",
							Title: "第二项",
							OnClick: func() {
								clicked2++
							},
						})
						menuRow(c, menuRowProps{Key: "row-3", Title: "第三项"})
					}))
				})
			})
	}, 400, 300)
	tt.Frame()

	for i := 0; i < 8 && !tt.Focused("第一项"); i++ {
		tt.Key(0, ui.KeyTab)
		tt.Frame()
	}
	if !tt.Focused("第一项") {
		t.Fatal("按 Tab 后未能聚焦到第 1 项")
	}

	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if !tt.Focused("第二项") {
		t.Fatal("按 KeyDown 后第 2 项未聚焦")
	}

	tt.Key(0, ui.KeyUp)
	tt.Frame()
	if !tt.Focused("第一项") {
		t.Fatal("按 KeyUp 后未能回到第 1 项")
	}

	tt.Key(0, ui.KeyUp)
	tt.Frame()
	if !tt.Focused("第三项") {
		t.Fatal("在第 1 项按 KeyUp 后未能绕到第 3 项")
	}

	tt.Key(0, ui.KeyUp)
	tt.Frame()
	if !tt.Focused("第二项") {
		t.Fatal("在第 3 项按 KeyUp 后第 2 项未聚焦")
	}

	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if clicked2 != 1 {
		t.Fatalf("第 2 项聚焦时按 Enter 应触发一次 OnClick，实际=%d", clicked2)
	}
}
