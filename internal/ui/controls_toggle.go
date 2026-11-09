package mcodeui

import (
	"strconv"

	"github.com/egoist/mygo/ui"
)

const switchDisabledOpacity = 0.45

// switchOffTextMix 是关闭态轨道向文字色混合的比例，与 mygo 原生开关一致。
const switchOffTextMix float32 = 0.15

// switchTrackColor 返回开关轨道底色：关闭态是中性轨道，打开态用强调色；
// 窗口失焦时强调色降级。白色滑块与文字由 mygo 绘制，不受影响。
func switchTrackColor(c *ui.Context, active, on bool) ui.Color {
	theme := c.Theme()
	off := theme.Border.Mix(theme.Text, switchOffTextMix)
	if !on {
		return off
	}
	return inactiveAccentFor(active, theme.Accent, ThemeTertiaryText(c))
}

type Segment struct {
	Label    string
	Count    int
	Disabled bool
}

// Segmented 使用索引表示选项；Count 大于零时显示数量。
func Segmented(c *ui.Context, key any, selected *int, segments []Segment, onChange func(int)) ui.Element {
	return segmentedControl(c, key, selected, segments, onChange, false)
}

// SegmentedFill 与 Segmented 相同，但各段等分整条轨道：轨道被拉满时
// 每段宽度一致、点击位置与选项一一对应，用于独占一行的选择控件。
func SegmentedFill(c *ui.Context, key any, selected *int, segments []Segment, onChange func(int)) ui.Element {
	return segmentedControl(c, key, selected, segments, onChange, true)
}

func segmentedControl(c *ui.Context, key any, selected *int, segments []Segment, onChange func(int), fill bool) ui.Element {
	ctx := c
	if key != nil {
		ctx = c.Key(key)
	}
	if selected == nil {
		selected = new(int)
	}
	parts := ui.SegmentedBase(ctx, selected, len(segments))
	track := parts.Track.Padding(Space0).Gap(Space0).Radius(RadiusMD).
		Background(ThemeOverlay(c, OverlaySubtle))
	track.Children(func() {
		for i, segment := range segments {
			item := parts.Segment(i).Label(segment.Label).Padding(Space1, settingsCompactPadX).Radius(RadiusSM).Disabled(segment.Disabled).Transition(motionFeedback(c))
			if fill {
				item = item.Grow(1)
			}
			color := ThemeSecondaryText(c)
			if i == *selected {
				item.Background(ThemeSurface(c))
				color = ThemePrimaryText(c)
			} else if item.Hovered() && !segment.Disabled {
				item.Background(ThemeOverlay(c, OverlayFaint))
				color = ThemePrimaryText(c)
			}
			if segment.Disabled {
				color = ThemeTertiaryText(c)
			}
			if item.FocusVisible() {
				item.Border(1, ThemeAccent(c))
			}
			item.Children(func() {
				ui.Text(c, segment.Label).FontSize(TypeBody).TextColor(color).SingleLine()
				if segment.Count > 0 {
					ui.Text(c, strconv.Itoa(segment.Count)).FontSize(TypeCaption).TextColor(color).SingleLine().Transition(motionFeedback(c))
				}
			})
		}
	})
	if track.Changed() && onChange != nil {
		onChange(*selected)
	}
	return track
}

// Switch 使用原生开关并在状态变化时调用 onChange。
func Switch(c *ui.Context, key any, on *bool, label string, onChange func(bool)) ui.Element {
	ctx := c
	if key != nil {
		ctx = c.Key(key)
	}
	if label == "" {
		label = "切换"
	}
	if on == nil {
		on = new(bool)
	}
	switcher := ui.Switch(ctx, on).Label(label)
	if active := windowActiveFor(c); !active {
		switcher = switcher.Background(switchTrackColor(c, active, *on))
	}
	if switcher.Changed() && onChange != nil {
		onChange(*on)
	}
	return switcher
}

// SwitchDisabled 显示不可操作的开关。
func SwitchDisabled(c *ui.Context, key any, on bool, label string) ui.Element {
	ctx := c
	if key != nil {
		ctx = c.Key(key)
	}
	if label == "" {
		label = "切换"
	}
	switcher := ui.Switch(ctx, &on).Label(label)
	if active := windowActiveFor(c); !active {
		switcher = switcher.Background(switchTrackColor(c, active, on))
	}
	return switcher.Disabled(true).Opacity(switchDisabledOpacity)
}
