package mcodeui

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

func renderCommandActivityView(c *ui.Context, s *chatState, m Message) {
	st := commandActivitySnapshot(s, m.ID)
	if len(st.processes) == 0 && len(st.history) == 0 {
		return
	}
	_, stalled := s.runningCommandBadge(c, m)
	color := ThemeAccent(c)
	if stalled {
		color = ThemeWarning(c)
	}
	peak := float64(100)
	for _, value := range st.history {
		peak = max(peak, value)
	}
	ui.Column(c.Key("command-activity-" + m.ID)).FillWidth().Gap(Space1).Children(func() {
		ui.Row(c.Key("command-summary-" + m.ID)).FillWidth().Gap(Space2).AlignItems(ui.Center).Children(func() {
			ui.Row(c.Key("command-history-" + m.ID)).Grow(1).Height(scaledFont(TypeCaption)).Gap(Hairline).AlignItems(ui.End).Children(func() {
				for i, value := range st.history {
					bar := ui.Box(c.Key(fmt.Sprintf("command-history-%s-%d", m.ID, i))).Width(Space0).Background(color)
					height := float32(value/peak) * scaledFont(TypeCaption)
					bar.Height(bar.AnimateTo("height", height, ui.Animation{Duration: animateDuration(c, DurBase), Ease: ui.EaseOut}))
				}
			})
			ui.Text(c, fmt.Sprintf("%.0f%%", st.cpu)).Font(terminalFontFamily()).FontSize(scaledFont(TypeCaption)).TextColor(color).SingleLine()
			if st.rss >= 1024*1024 {
				ui.Text(c, "·").FontSize(scaledFont(TypeCaption)).TextColor(ThemeSecondaryText(c))
				ui.Text(c, formatFileSize(st.rss)).Font(terminalFontFamily()).FontSize(scaledFont(TypeCaption)).TextColor(ThemeSecondaryText(c)).SingleLine()
			}
		})
		for i, p := range st.processes {
			row := ui.Row(c.Key(fmt.Sprintf("command-process-%s-%d", m.ID, i))).FillWidth().Height(ControlXS).Radius(RadiusXS).Clip().Padding(0, Space2).Gap(Space2).AlignItems(ui.Center)
			opacity := motionEnter(c, row, "enter", i)
			if p.cpu < 1 {
				opacity *= 0.5
			}
			row.Opacity(opacity)
			row.Children(func() {
				fill := ui.Box(c.Key(fmt.Sprintf("command-fill-%s-%d", m.ID, i))).Absolute().Left(0).Top(0).Bottom(0).Radius(RadiusXS).Background(ThemeAccent(c).Alpha(0.08)).PassThrough()
				width := row.LayoutBounds().W * float32(min(max(p.cpu, 0), 100)/100)
				fill.Width(fill.AnimateTo("width", width, ui.Animation{Duration: animateDuration(c, DurBase), Ease: ui.EaseOut}))
				ui.Text(c, p.name).FontSize(scaledFont(TypeCaption)).FontWeight(500).TextColor(ThemePrimaryText(c)).SingleLine()
				ui.Text(c, p.object).FontSize(scaledFont(TypeCaption)).TextColor(ThemeSecondaryText(c)).Grow(1).Shrink(1).MinWidth(0).SingleLine().Ellipsis("…")
				cpuColor := ThemeSecondaryText(c)
				if stalled {
					cpuColor = ThemeWarning(c)
				}
				ui.Text(c, fmt.Sprintf("%.0f%%", p.cpu)).Font(terminalFontFamily()).FontSize(scaledFont(TypeCaption)).TextColor(cpuColor).SingleLine()
			})
		}
	})
}
