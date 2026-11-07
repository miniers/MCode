package mcodeui

import "github.com/egoist/mygo/ui"

type PopoverAlign int

const (
	BelowStart PopoverAlign = iota
	BelowEnd
	AboveStart
	AboveEnd
)

type PopoverProps struct {
	Key       any
	Anchor    ui.Element
	Open      *bool
	Align     PopoverAlign
	Width     float32
	MaxWidth  float32
	MaxHeight float32
	OnDismiss func()
}

type MenuItemOpts struct {
	Icon     *ui.SVG
	Shortcut string
	Danger   bool
	Disabled bool
	OnClick  func()
}

// popoverEntrance 返回入场进度对应的缩放、纵向偏移与透明度。
func popoverEntrance(progress float32) (scale, y, opacity float32) {
	progress = max(0, min(1, progress))
	return 0.95 + 0.05*progress, Space1 * (1 - progress), progress
}

func PopoverCard(c *ui.Context, p PopoverProps, content func()) ui.Element {
	if p.Open == nil || !*p.Open {
		return ui.Element{}
	}
	ctx := c
	if p.Key != nil {
		ctx = c.Key(p.Key)
	}
	panel := ui.PopoverBase(ctx, p.Anchor, p.Open, func(panel ui.Element) {
		switch p.Align {
		case BelowEnd:
			panel.AttachTo(p.Anchor, ui.AnchorBottomRight, ui.AnchorTopRight)
		case AboveStart:
			panel.AttachTo(p.Anchor, ui.AnchorTopLeft, ui.AnchorBottomLeft)
		case AboveEnd:
			panel.AttachTo(p.Anchor, ui.AnchorTopRight, ui.AnchorBottomRight)
		}
		if p.MaxWidth > 0 {
			panel.MaxWidth(p.MaxWidth)
		}
		if p.MaxHeight > 0 {
			panel.MaxHeight(p.MaxHeight)
		}
		progress := float32(1)
		if !reduceMotion(c) {
			progress = panel.Keyframes("popover-enter", []ui.Keyframe{{Value: 0}, {At: animateDuration(c, DurBase), Value: 1, Ease: EaseSnap}})
		}
		scale, y, opacity := popoverEntrance(progress)
		panel.Scale(scale, scale).Translate(0, y).Opacity(opacity)
		card := ui.Column(c).Radius(RadiusLG).Padding(Space2).Background(ThemeSurfacePopover(c)).Border(Space1/4, ThemeHairline(c))
		if p.Width > 0 {
			card.Width(p.Width)
		}
		if p.MaxWidth > 0 {
			card.MaxWidth(p.MaxWidth)
		}
		if p.MaxHeight > 0 {
			card.MaxHeight(p.MaxHeight)
		}
		applyElevation(c, card, ElevPopover).Children(func() {
			if content != nil {
				content()
			}
		})
	})
	// PopoverBase 在构建内容之后才写入 dismissed。
	if panel.Dismissed() && p.OnDismiss != nil {
		p.OnDismiss()
	}
	return panel
}

func MenuItem(c *ui.Context, key any, label string, opts MenuItemOpts) ui.Element {
	ctx := c
	if key != nil {
		ctx = c.Key(key)
	}
	item := ui.ButtonBase(ctx).Label(label).Height(ControlSM).FillWidth().Radius(RadiusSM).Padding(0, Space2).Gap(Space2).AlignItems(ui.Center)
	if opts.Disabled {
		item.Disabled(true)
	} else if opts.OnClick != nil {
		item.OnClick(opts.OnClick)
	}
	if !opts.Disabled && item.Hovered() {
		item.Background(ThemeOverlayHover(c))
	}
	fg := ThemePrimaryText(c)
	if opts.Danger {
		fg = ThemeDanger(c)
	}
	if opts.Disabled {
		fg = ThemeTertiaryText(c)
	}
	return item.Children(func() {
		if opts.Icon != nil {
			ui.Icon(c, opts.Icon).Size(IconSM, IconSM).TextColor(fg)
		}
		ui.Text(c, label).FontSize(TypeBody).TextColor(fg).SingleLine()
		if opts.Shortcut != "" {
			ui.Spacer(c)
			KbdRow(c, nil, opts.Shortcut)
		}
	})
}

type DialogProps struct {
	Key       string
	Open      *bool
	Title     string
	Width     float32
	Draggable bool
	Resizable bool
	Footer    func()
	OnClose   func()
}

func Dialog(c *ui.Context, p DialogProps, body func()) ui.Element {
	if p.Open == nil || !*p.Open {
		if p.Draggable || p.Resizable {
			modalDragReset(p.Key)
		}
		return ui.Element{}
	}
	panel := ui.DialogBase(c.Key(p.Key), p.Open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.Color{}.Alpha(ScrimOpacity))
		w, h := c.Size()
		panel.MaxWidth(w - Space8).MaxHeight(h - Space8)
		card := ui.Column(c).Label("dialog-card-" + p.Key).Radius(RadiusXL).Padding(Space6).Gap(Space4).Background(ThemeSurfacePopover(c))
		var width, height float32
		if p.Resizable {
			width = p.Width
			if width <= 0 {
				width = 560
			}
			width, height = modalResizeSize(p.Key, width, 420, 360, 240, w, h)
			card.Width(width).Height(height)
		} else if p.Width > 0 {
			card.Width(p.Width)
		}
		card.MaxWidth(w - Space8).MaxHeight(h - Space8)
		applyElevation(c, card, ElevDialog).Children(func() {
			title := ui.Row(c).FillWidth()
			if p.Draggable {
				title = modalDragHandle(c, p.Key, title)
			}
			title.Children(func() {
				ui.Text(c, p.Title).FontSize(TypeTitle).FontWeight(weightFor(c, 600)).TextColor(ThemePrimaryText(c))
			})
			if body != nil {
				body()
			}
			if p.Footer != nil {
				ui.Row(c).FillWidth().Justify(ui.End).Gap(Space3).Children(p.Footer)
			}
			if p.Resizable {
				modalResizeGrips(c, p.Key, width, height)
			}
		})
	})
	if p.Draggable || p.Resizable {
		modalDragPanel(c, p.Key, panel)
	}
	if panel.Dismissed() {
		if p.Draggable || p.Resizable {
			modalDragReset(p.Key)
		}
		if p.OnClose != nil {
			p.OnClose()
		}
	}
	return panel
}

type ConfirmProps struct {
	DialogProps
	Tone            ConfirmTone
	Body            string
	ConfirmLabel    string
	CancelLabel     string
	OnConfirm       func()
	OnCancel        func()
	ConfirmDisabled bool
}

// ConfirmDialogPreset 避免与已有 ConfirmDialog 类型重名。
func ConfirmDialogPreset(c *ui.Context, p ConfirmProps) ui.Element {
	confirm := p.ConfirmLabel
	if confirm == "" {
		if p.Tone == ConfirmNotice {
			confirm = "知道了"
		} else {
			confirm = "确认"
		}
	}
	cancel := p.CancelLabel
	if cancel == "" {
		cancel = "取消"
	}
	closeWith := func(fn func()) {
		if p.Open != nil {
			*p.Open = false
		}
		if fn != nil {
			fn()
		}
	}
	p.DialogProps.OnClose = func() {
		if p.Tone == ConfirmNotice && p.OnConfirm != nil {
			p.OnConfirm()
		} else if p.OnCancel != nil {
			p.OnCancel()
		}
	}
	p.DialogProps.Footer = func() {
		if p.Tone != ConfirmNotice {
			Button(c, ButtonOpts{Text: cancel, Variant: ButtonSecondary, OnClick: func() { closeWith(p.OnCancel) }})
		}
		variant := ButtonPrimary
		if p.Tone == ConfirmDanger {
			variant = ButtonDanger
		}
		Button(c, ButtonOpts{Text: confirm, Variant: variant, Disabled: p.ConfirmDisabled, OnClick: func() { closeWith(p.OnConfirm) }}).AutoFocus()
	}
	return Dialog(c, p.DialogProps, func() {
		if p.Body != "" {
			ui.Text(c, p.Body).FontSize(TypeBody).TextColor(ThemeSecondaryText(c))
		}
	})
}
