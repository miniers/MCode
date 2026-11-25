package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func TestPopoverAnchorToggle(t *testing.T) {
	for _, tc := range []struct {
		name    string
		useCard bool
	}{
		{name: "appPopover", useCard: false},
		{name: "PopoverCard", useCard: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			open := false
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Box(c.Key("toggle-shell")).Fill().Children(func() {
					anchor := Button(c, ButtonOpts{
						Key:     "toggle-anchor",
						Text:    "触发浮层",
						Label:   "触发浮层",
						Variant: ButtonSecondary,
						OnClick: func() {
							togglePopoverOpen(&open)
						},
					})
					if tc.useCard {
						PopoverCard(c, anchor, PopoverProps{
							Key:   "toggle-popover-card",
							Open:  &open,
							Width: 140,
							OnDismiss: func() {
								open = false
							},
						}, func() {
							ui.Text(c, "浮层内容")
						})
					} else {
						appPopover(c.Key("toggle-app-popover"), anchor, &open, func() {
							open = false
						}, func() {
							popoverCard(c, ui.Column(c).Width(140).Padding(Space2).Children(func() {
								ui.Text(c, "浮层内容")
							}))
						})
					}
				})
			}, 400, 300)

			assertClosed := func(step string) {
				t.Helper()
				if open {
					t.Fatalf("%s: expected open=false", step)
				}
				tt.Advance(motionPop + 60*time.Millisecond)
				tt.Frame()
				if open {
					t.Fatalf("%s: open bounced back to true", step)
				}
				if _, ok := tt.Find("浮层内容"); ok {
					t.Fatalf("%s: popover content should be hidden", step)
				}
			}

			assertOpened := func(step string) {
				t.Helper()
				if !open {
					t.Fatalf("%s: expected open=true", step)
				}
				tt.Advance(motionPop + 60*time.Millisecond)
				tt.Frame()
				if !open {
					t.Fatalf("%s: open unexpectedly closed", step)
				}
				if _, ok := tt.Find("浮层内容"); !ok {
					t.Fatalf("%s: popover content should be visible, texts=%q", step, tt.Texts())
				}
			}

			// (a) 点锚点打开 -> 再点锚点关闭（确认 *open == false 不回弹）-> 再点锚点能重新打开。
			if err := tt.Click("触发浮层"); err != nil {
				t.Fatalf("open click failed: %v", err)
			}
			assertOpened("scenario (a) first open")

			if err := tt.Click("触发浮层"); err != nil {
				t.Fatalf("second click on anchor failed: %v", err)
			}
			assertClosed("scenario (a) second click close")

			if err := tt.Click("触发浮层"); err != nil {
				t.Fatalf("third click on anchor failed: %v", err)
			}
			assertOpened("scenario (a) reopen")

			// (b) 点空白处关闭 -> 紧接着点锚点一次即能打开。
			tt.ClickAt(350, 250)
			tt.Frame()
			assertClosed("scenario (b) outside click close")

			if err := tt.Click("触发浮层"); err != nil {
				t.Fatalf("click anchor after outside dismiss failed: %v", err)
			}
			assertOpened("scenario (b) single click reopen")

			// (c) 按 Esc 关闭 -> 紧接着点锚点一次即能打开。
			tt.Key(0, ui.KeyEscape)
			tt.Frame()
			assertClosed("scenario (c) escape close")

			if err := tt.Click("触发浮层"); err != nil {
				t.Fatalf("click anchor after escape failed: %v", err)
			}
			assertOpened("scenario (c) single click reopen")
		})
	}
}

func TestPopoverAnchorToggle_ConsumesDismissedWithoutReopen(t *testing.T) {
	open := false
	markPopoverAnchorDismissed(&open)

	// 刚因点锚点触发 PopoverBase 关闭后的同周期 OnClick：应消费标记并保持 false。
	if got := togglePopoverOpen(&open); got || open {
		t.Fatalf("first toggle after anchor dismiss = (%v, open=%v), want (false, false)", got, open)
	}
	// 再次点击应恢复正常打开。
	if got := togglePopoverOpen(&open); !got || !open {
		t.Fatalf("next toggle = (%v, open=%v), want (true, true)", got, open)
	}
	// 打开状态下点击应关闭并清理残留标记。
	markPopoverAnchorDismissed(&open)
	if got := togglePopoverOpen(&open); got || open {
		t.Fatalf("toggle while open = (%v, open=%v), want (false, false)", got, open)
	}
	if got := togglePopoverOpen(&open); !got || !open {
		t.Fatalf("reopen after close = (%v, open=%v), want (true, true)", got, open)
	}
	if togglePopoverOpen(nil) {
		t.Fatal("togglePopoverOpen(nil) should return false")
	}
}

func TestPopoverAnchorToggle_HeaderMoreMenu(t *testing.T) {
	testenv.TempHome(t)

	fake := NewFakeClient()
	s, root := buildRoot(fake)
	tt := ui.NewTester(root, 1200, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("更多工具")
		return ok
	})
	hs := getChatHeaderState(s)

	waitClosed := func(step string) {
		t.Helper()
		waitFor(t, tt, func() bool {
			if hs.auxMenuOpen {
				return false
			}
			_, ok := tt.Find("项目文档")
			return !ok
		})
		if hs.auxMenuOpen {
			t.Fatalf("%s: expected auxMenuOpen=false", step)
		}
	}

	waitOpened := func(step string) {
		t.Helper()
		waitFor(t, tt, func() bool {
			if !hs.auxMenuOpen {
				return false
			}
			_, ok := tt.Find("项目文档")
			return ok
		})
	}

	// (a) 点锚点打开 -> 再点锚点关闭 -> 再点锚点重新打开。
	if err := tt.Click("更多工具"); err != nil {
		t.Fatalf("open more menu failed: %v", err)
	}
	waitOpened("header (a) first open")

	if err := tt.Click("更多工具"); err != nil {
		t.Fatalf("close more menu by anchor failed: %v", err)
	}
	waitClosed("header (a) second click close")

	if err := tt.Click("更多工具"); err != nil {
		t.Fatalf("reopen more menu failed: %v", err)
	}
	waitOpened("header (a) reopen")

	// (b) 点空白处关闭 -> 点锚点一次打开。
	tt.ClickAt(600, 350)
	tt.Frame()
	waitClosed("header (b) outside click close")

	if err := tt.Click("更多工具"); err != nil {
		t.Fatalf("reopen after outside click failed: %v", err)
	}
	waitOpened("header (b) single click reopen")

	// (c) 按 Esc 关闭 -> 点锚点一次打开。
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	waitClosed("header (c) escape close")

	if err := tt.Click("更多工具"); err != nil {
		t.Fatalf("reopen after escape failed: %v", err)
	}
	waitOpened("header (c) single click reopen")
}
