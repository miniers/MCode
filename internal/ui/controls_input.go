package mcodeui

import "github.com/egoist/mygo/ui"

type TextFieldProps struct {
	Key         any
	Value       *string
	Placeholder string
	Label       string
	Size        float32
	// Width 非零时固定宽度；Grow 让输入框占满剩余空间，FillWidth 铺满整行。
	Width     float32
	MinWidth  float32
	Grow      bool
	FillWidth bool
	// Multiline 用 ui.TextArea 渲染，外观与单行一致；MinLines、MaxLines 限定可见行数。
	Multiline bool
	MinHeight float32
	MinLines  int
	MaxLines  int
	Icon      *ui.SVG
	Clearable bool
	// ClearLabel 是清空按钮的无障碍标签，空时用「清空」。
	ClearLabel string
	Password   bool
	ReadOnly   bool
	Focused    bool
	AutoFocus  bool
	Disabled   bool
	OnChange   func(string)
	OnSubmit   func(string)
	// OnFocusChange 每帧报告输入框是否持有焦点，供调用方跟踪失焦时机。
	OnFocusChange func(bool)
	OnKeyDown     func(k ui.TextKey) bool
}

type inputColors struct {
	border, strong, accent ui.Color
}

func inputBorderColor(focused, hovered, disabled, windowActive bool, colors inputColors) ui.Color {
	if disabled || !windowActive {
		return colors.border
	}
	if focused {
		return colors.accent
	}
	if hovered {
		return colors.strong
	}
	return colors.border
}

func TextField(c *ui.Context, p TextFieldProps) ui.Element {
	return renderTextField(c, p, false)
}

func SearchField(c *ui.Context, p TextFieldProps) ui.Element {
	if p.Icon == nil {
		p.Icon = IconSearch()
	}
	return renderTextField(c, p, true)
}

func renderTextField(c *ui.Context, p TextFieldProps, search bool) ui.Element {
	ctx := c
	if p.Key != nil {
		ctx = c.Key(p.Key)
	}
	value := p.Value
	if value == nil {
		value = new(string)
	}
	height, radius, font := ControlMD, RadiusMD, TypeBody
	if search {
		height, radius, font = ControlMD, RadiusPill, TypeFootnote
	}
	if p.Size > 0 {
		height = p.Size
	}
	background, text := ThemeSurfaceRaised(c), ThemePrimaryText(c)
	if p.Disabled {
		background, text = ThemeSurfaceCanvas(c), ThemeTertiaryText(c)
	}
	label := p.Label
	if label == "" {
		label = p.Placeholder
	}
	if p.Multiline {
		return renderTextArea(c, ctx, p, value, label, radius, font, background, text)
	}
	row := ui.Row(ctx).Height(height).Radius(radius).Padding(0, Space3).
		Gap(Space2).AlignItems(ui.Center).Background(background)
	if p.Width > 0 {
		row.Width(p.Width)
	}
	if p.MinWidth > 0 {
		row.MinWidth(p.MinWidth)
	}
	if p.Grow {
		row.Grow(1)
	}
	if p.FillWidth {
		row.FillWidth()
	}
	row.Children(func() {
		if p.Icon != nil {
			ui.Icon(c, p.Icon).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
		}
		in := ui.TextInput(ctx.Key("input"), value).
			Placeholder(p.Placeholder).PlaceholderColor(ThemeTertiaryText(c)).
			FontSize(font).TextColor(text).BorderWidth(0).
			Background(ui.Transparent).Padding(0).Grow(1).Disabled(p.Disabled)
		if label != "" {
			// 标签挂在输入框上，让按 Label 定位的点击直接命中输入框。
			in.Label(label)
		}
		if p.Password {
			in.Password()
		}
		if p.ReadOnly {
			in.ReadOnly(true)
		}
		if !p.Disabled {
			if row.Clicked() || p.Focused {
				in.Focus()
			}
			if p.AutoFocus {
				in.AutoFocus()
			}
			bindTextFieldInput(p, in, value)
		}
		const inputBorderStrongOpacity = 0.22
		row.Border(1, inputBorderColor(in.Focused(), row.Hovered(), p.Disabled, windowActiveFor(c), inputColors{
			border: ThemeBorder(c), strong: ThemeOverlay(c, inputBorderStrongOpacity), accent: ThemeAccent(c),
		}))
		if p.Clearable && *value != "" {
			clearLabel := p.ClearLabel
			if clearLabel == "" {
				clearLabel = "清空"
			}
			IconButton(ctx, IconButtonProps{
				Key: "clear", Icon: IconX(), Label: clearLabel, Size: ControlXS,
				Variant: ButtonGhost, Disabled: p.Disabled,
				OnClick: func() {
					*value = ""
					if p.OnChange != nil {
						p.OnChange("")
					}
				},
			})
		}
	})
	return row
}

// renderTextArea 渲染多行输入：沿用单行的圆角与底色，边框跟随焦点变色。
func renderTextArea(c *ui.Context, ctx *ui.Context, p TextFieldProps, value *string, label string, radius, font float32, background, text ui.Color) ui.Element {
	area := ui.TextArea(ctx, value).
		Radius(radius).
		Padding(Space2, Space3).
		Background(background).
		Placeholder(p.Placeholder).
		PlaceholderColor(ThemeTertiaryText(c)).
		FontSize(font).
		TextColor(text).
		Disabled(p.Disabled)
	if p.MinLines > 0 || p.MaxLines > 0 {
		area.Lines(p.MinLines, p.MaxLines)
	}
	if p.MinHeight > 0 {
		area.MinHeight(p.MinHeight)
	}
	if p.Width > 0 {
		area.Width(p.Width)
	}
	if p.MinWidth > 0 {
		area.MinWidth(p.MinWidth)
	}
	if p.Grow {
		area.Grow(1)
	}
	if p.FillWidth {
		area.FillWidth()
	}
	if p.ReadOnly {
		area.ReadOnly(true)
	}
	if label != "" {
		area.Label(label)
	}
	if !p.Disabled {
		if p.Focused {
			area.Focus()
		}
		if p.AutoFocus {
			area.AutoFocus()
		}
		if area.Changed() && p.OnChange != nil {
			p.OnChange(*value)
		}
		if p.OnFocusChange != nil {
			p.OnFocusChange(area.Focused())
		}
	}
	return area
}

// bindTextFieldInput 接上单行输入的回调：改动、回车提交、焦点变化与按键拦截。
func bindTextFieldInput(p TextFieldProps, in ui.Element, value *string) {
	if in.Changed() && p.OnChange != nil {
		p.OnChange(*value)
	}
	if in.Submitted() && p.OnSubmit != nil {
		p.OnSubmit(*value)
	}
	if p.OnFocusChange != nil {
		p.OnFocusChange(in.Focused())
	}
	if p.OnKeyDown != nil {
		in.OnTextKey(p.OnKeyDown)
	}
}
