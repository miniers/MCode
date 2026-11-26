package mcodeui

import (
	"sync"

	"github.com/egoist/mygo/ui"
)

type PopoverAlign int

const (
	BelowStart PopoverAlign = iota
	BelowEnd
	AboveStart
	AboveEnd
)

type PopoverProps struct {
	Key       any
	Open      *bool
	Align     PopoverAlign
	Width     float32
	MaxWidth  float32
	MaxHeight float32
	OnDismiss func()
}

type anchoredPopoverProps struct {
	Key                           any
	Open                          *bool
	Align                         PopoverAlign
	MinWidth, MaxWidth, MaxHeight float32
	OnDismiss                     func()
}

var (
	popoverToggleMu        sync.Mutex
	popoverAnchorDismissed = make(map[*bool]struct{})
)

type popoverOpenMarkerKey struct{}

type popoverOpenMarker struct {
	open bool
}

// popoverOpenState 返回当前窗口根元素上的浮层打开标记。
// mcode 可通过 OpenThreadWindow 同时打开多个独立聊天窗口，因此不能用包级变量；
// ui.Context 虽不直接暴露 *mygo.Window，但 c.Root() 指向当前窗口唯一的根节点，
// 用 ui.Local(c.Root(), ...) 即可按窗口隔离存储并在关闭窗口时随引擎释放。
func popoverOpenState(c *ui.Context) *popoverOpenMarker {
	if c == nil {
		return nil
	}
	return ui.Local(c.Root(), popoverOpenMarkerKey{}, func() popoverOpenMarker {
		return popoverOpenMarker{}
	})
}

// markPopoverOpen 在当前窗口记录「本帧有浮层保持打开」。
func markPopoverOpen(c *ui.Context) {
	if st := popoverOpenState(c); st != nil {
		st.open = true
	}
}

// takePopoverOpen 取出当前窗口上一帧是否有浮层打开，并清零以便本帧重新置位。
func takePopoverOpen(c *ui.Context) bool {
	st := popoverOpenState(c)
	if st == nil {
		return false
	}
	wasOpen := st.open
	st.open = false
	return wasOpen
}

// togglePopoverOpen 统一切换浮层开合状态并防止锚点二次点击「先关后开」：
// 若当前已打开则置为 false；若刚因交互锚点被 PopoverBase 关闭则消费该标记并保持 false；
// 其余情况置为 true。返回值表示调用后浮层是否处于打开状态。
func togglePopoverOpen(open *bool) bool {
	if open == nil {
		return false
	}
	popoverToggleMu.Lock()
	_, dismissedByAnchor := popoverAnchorDismissed[open]
	delete(popoverAnchorDismissed, open)
	popoverToggleMu.Unlock()
	if *open {
		*open = false
		return false
	}
	if dismissedByAnchor {
		*open = false
		return false
	}
	*open = true
	return true
}

func markPopoverAnchorDismissed(open *bool) {
	if open == nil {
		return
	}
	popoverToggleMu.Lock()
	popoverAnchorDismissed[open] = struct{}{}
	popoverToggleMu.Unlock()
}

func clearStalePopoverAnchorDismissed(open *bool, anchor ui.Element) {
	if open == nil {
		return
	}
	popoverToggleMu.Lock()
	_, exists := popoverAnchorDismissed[open]
	popoverToggleMu.Unlock()
	if !exists {
		return
	}
	if !anchor.Pressed() && !anchor.Clicked() {
		popoverToggleMu.Lock()
		delete(popoverAnchorDismissed, open)
		popoverToggleMu.Unlock()
	}
}

func anchoredPopover(c *ui.Context, anchor ui.Element, p anchoredPopoverProps, content func(ui.Element)) ui.Element {
	if p.Open == nil {
		return ui.Element{}
	}
	if !*p.Open {
		clearStalePopoverAnchorDismissed(p.Open, anchor)
		return ui.Element{}
	}
	ctx := c
	if p.Key != nil {
		ctx = c.Key(p.Key)
	}
	panel := ui.PopoverBase(ctx, anchor, p.Open, func(panel ui.Element) {
		switch p.Align {
		case BelowEnd:
			panel.AttachTo(anchor, ui.AnchorBottomRight, ui.AnchorTopRight).Margin(Space1, 0, 0, 0)
		case AboveStart:
			panel.AttachTo(anchor, ui.AnchorTopLeft, ui.AnchorBottomLeft).Margin(0, 0, Space1, 0)
		case AboveEnd:
			panel.AttachTo(anchor, ui.AnchorTopRight, ui.AnchorBottomRight).Margin(0, 0, Space1, 0)
		default:
			panel.AttachTo(anchor, ui.AnchorBottomLeft, ui.AnchorTopLeft).Margin(Space1, 0, 0, 0)
		}
		if p.MinWidth > 0 {
			panel.MinWidth(p.MinWidth)
		}
		if p.MaxWidth > 0 {
			panel.MaxWidth(p.MaxWidth)
		}
		if p.MaxHeight > 0 {
			panel.MaxHeight(p.MaxHeight)
		}
		panel.Transition(motionPopover(c, popoverRiseOffset(panel, anchor)))
		if content != nil {
			content(panel)
		}
	})
	if panel.Dismissed() {
		if anchor.Pressed() || anchor.Clicked() {
			markPopoverAnchorDismissed(p.Open)
		}
		if p.OnDismiss != nil {
			p.OnDismiss()
		}
	}
	if *p.Open {
		markPopoverOpen(c)
	}
	return panel
}

func popoverCardStyle(c *ui.Context, card ui.Element) ui.Element {
	return applyElevation(c, card.Radius(RadiusLG).Background(ThemeSurfacePopover(c)).Border(Hairline, ThemeHairline(c)), ElevPopover)
}

func PopoverCard(c *ui.Context, anchor ui.Element, p PopoverProps, content func()) ui.Element {
	return anchoredPopover(c, anchor, anchoredPopoverProps{Key: p.Key, Open: p.Open, Align: p.Align, MaxWidth: p.MaxWidth, MaxHeight: p.MaxHeight, OnDismiss: p.OnDismiss}, func(_ ui.Element) {
		card := popoverCardStyle(c, ui.Column(c).Padding(Space2))
		if p.Width > 0 {
			card.Width(p.Width)
		}
		if p.MaxWidth > 0 {
			card.MaxWidth(p.MaxWidth)
		}
		if p.MaxHeight > 0 {
			card.MaxHeight(p.MaxHeight)
		}
		card.Children(func() {
			if content != nil {
				content()
			}
		})
	})
}
