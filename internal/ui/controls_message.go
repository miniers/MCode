package mcodeui

import (
	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

const userBubbleRatio float32 = 0.85

// MessageRole 标识消息卡片的角色。
type MessageRole string

const (
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
	MessageRoleTool      MessageRole = "tool"
	MessageRoleSystem    MessageRole = "system"
)

// MessageCardProps 定义消息卡片的角色和最大宽度。
type MessageCardProps struct {
	Key      string
	Role     MessageRole
	MaxWidth float32
}

type messageCardAppearance struct {
	surface bool
	border  bool
	radius  float32
	padY    float32
	padX    float32
	font    float32
	center  bool
}

func messageCardStyle(role MessageRole) messageCardAppearance {
	switch role {
	case MessageRoleUser:
		return messageCardAppearance{surface: true, radius: RadiusLG, padY: Space4, padX: Space5, font: TypeProse}
	case MessageRoleTool:
		return messageCardAppearance{surface: true, border: true, radius: RadiusLG, padY: Space4, padX: Space4, font: TypeBody}
	case MessageRoleSystem:
		return messageCardAppearance{font: TypeFootnote, center: true}
	default:
		return messageCardAppearance{font: TypeProse}
	}
}

func bubbleMaxWidthFor(columnWidth float32, setting string, percent int, padX float32) float32 {
	if setting == config.ContentWidthFull {
		bubble := min(userBubbleMaxWidthFull, contentColumnWidth(columnWidth, percent, padX)*userBubbleFullRatio)
		return max(bubble, userBubbleFullMin)
	}
	return columnWidth * userBubbleRatio
}

// MessageCard 渲染消息的角色外壳，正文由调用方提供。
func MessageCard(c *ui.Context, p MessageCardProps, body func()) ui.Element {
	ctx := c
	if p.Key != "" {
		ctx = c.Key(p.Key)
	}
	style := messageCardStyle(p.Role)
	card := ui.Column(ctx).Radius(style.radius).Padding(style.padY, style.padX).
		FontSize(scaledFont(style.font))
	if p.MaxWidth > 0 {
		card.MaxWidth(p.MaxWidth)
	}
	if style.surface {
		card.Background(ThemeSurfaceCard(c))
	}
	if style.border {
		card.Border(Space1/4, ThemeBorder(c))
	}
	if style.center {
		card.FillWidth().AlignItems(ui.Center).TextAlign(ui.Center).TextColor(ThemeTertiaryText(c))
	}
	return card.Children(func() {
		if body != nil {
			body()
		}
	})
}

// ToolChipProps 定义工具状态、标题和交互。
type ToolChipProps struct {
	Key      string
	State    ToolState
	Title    string
	Detail   string
	Duration string
	Icon     *ui.SVG
	Expanded *bool
	OnClick  func()
}

type toolMarkerShape int

const (
	toolMarkerHollow toolMarkerShape = iota
	toolMarkerSpinner
	toolMarkerCheck
	toolMarkerAlert
)

func toolStateMarker(state ToolState) (toolMarkerShape, Tone) {
	switch state {
	case ToolRunning:
		return toolMarkerSpinner, ToneAccent
	case ToolDone:
		return toolMarkerCheck, ToneSuccess
	case ToolError:
		return toolMarkerAlert, ToneDanger
	default:
		return toolMarkerHollow, ToneNeutral
	}
}

// ToolChip 渲染工具调用的状态和可选详情。
func ToolChip(c *ui.Context, p ToolChipProps) ui.Element {
	ctx := c
	if p.Key != "" {
		ctx = c.Key(p.Key)
	}
	chip := ui.ButtonBase(ctx).Label(p.Title).Height(ControlSM).Radius(RadiusSM).
		Padding(0, Space2).Gap(Space2).AlignItems(ui.Center)
	if p.OnClick != nil {
		chip.OnClick(p.OnClick)
	} else if p.Expanded != nil {
		chip.OnClick(func() { *p.Expanded = !*p.Expanded })
	}
	if chip.Hovered() {
		chip.Background(ThemeOverlayHover(c))
	}
	return chip.Children(func() {
		shape, tone := toolStateMarker(p.State)
		color := ThemeTertiaryText(c)
		switch tone {
		case ToneAccent:
			color = ThemeAccent(c)
		case ToneSuccess:
			color = ThemeSuccess(c)
		case ToneDanger:
			color = ThemeDanger(c)
		}
		switch shape {
		case toolMarkerSpinner:
			if !reduceMotion(c) {
				ui.Spinner(c).Size(IconSM, IconSM).TextColor(color)
				break
			}
			fallthrough
		case toolMarkerHollow:
			ui.Box(c).Size(IconSM, IconSM).Radius(RadiusPill).Border(Space1/4, color)
		case toolMarkerCheck:
			ui.Icon(c, IconCheck()).Size(IconSM, IconSM).TextColor(color)
		case toolMarkerAlert:
			ui.Icon(c, IconAlertTriangle()).Size(IconSM, IconSM).TextColor(color)
		}
		if p.Icon != nil {
			ui.Icon(c, p.Icon).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
		}
		ui.Text(c, p.Title).FontSize(scaledFont(TypeBody)).TextColor(ThemePrimaryText(c)).SingleLine()
		if p.Detail != "" {
			ui.Text(c, p.Detail).FontSize(scaledFont(TypeCode)).TextColor(ThemeTertiaryText(c)).SingleLine().Ellipsis("…").Shrink(1)
		}
		if p.Duration != "" {
			ui.Text(c, p.Duration).FontSize(scaledFont(TypeFootnote)).FontFeatures("tnum").TextColor(ThemeTertiaryText(c)).SingleLine()
		}
		if p.Expanded != nil {
			icon := IconChevronRight()
			if *p.Expanded {
				icon = IconChevronDown()
			}
			ui.Icon(c, icon).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
		}
	})
}
