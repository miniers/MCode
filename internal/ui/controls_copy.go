package mcodeui

import "github.com/egoist/mygo/ui"

type CopyButtonMode int

const (
	CopyInline CopyButtonMode = iota
	CopyIcon
	CopyText
)

type CopyButtonProps struct {
	Key                  any
	Label, Tooltip, Text string
	Value                func() string
	Mode                 CopyButtonMode
	Copied, Disabled     bool
	OnCopy               func()
}

func CopyButton(c *ui.Context, p CopyButtonProps) ui.Element {
	ctx := c
	if p.Key != nil {
		ctx = c.Key(p.Key)
	}
	button := ui.ButtonBase(ctx).Label(p.Label).Tooltip(p.Tooltip).
		Height(uiSize(ControlXS)).Padding(Space0, Space2).Radius(RadiusSM).
		Gap(Space1).AlignItems(ui.Center).Disabled(p.Disabled)
	if p.Mode == CopyIcon {
		button.Width(ControlXS).Justify(ui.Center)
	}
	if !p.Disabled && button.Hovered() {
		button.Background(ThemeHover(c))
	}
	if !p.Disabled && p.Value != nil {
		button.OnClick(func() {
			c.WriteClipboard(p.Value())
			if p.OnCopy != nil {
				p.OnCopy()
			}
		})
	}
	return button.Children(func() {
		color := ThemeSecondaryText(c)
		if p.Mode == CopyText && !p.Disabled {
			color = ThemeAccent(c)
		}
		if p.Copied {
			color = ThemeSuccess(c)
		}
		if p.Mode != CopyText {
			icon, label := IconCopy(), "复制图标"
			if p.Copied {
				icon, label = IconCheck(), "复制成功"
			}
			feedback := motionFeedback(c)
			if p.Copied && !reduceMotion(c) {
				feedback.Enter = &ui.Motion{Opacity: 0}
			}
			ui.Icon(c.Key(label), icon).Size(IconSM, IconSM).Label(label).TextColor(color).Transition(feedback)
		}
		if p.Mode != CopyIcon && p.Text != "" {
			ui.Text(c, p.Text).FontSize(uiFont(TypeCaption)).TextColor(color).Transition(motionFeedback(c))
		}
	})
}
