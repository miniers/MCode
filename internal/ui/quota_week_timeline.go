package mcodeui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

const (
	quotaWeekLabelWidth   float32 = 100
	quotaWeekDotRadius    float32 = 3.5
	quotaWeekDotStroke    float32 = 1.5
	quotaWeekDepleteAlpha float32 = 0.28
)

var quotaWeekDayNames = [7]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}

type quotaWeekRange struct {
	x0 float32
	x1 float32
}

type quotaWeekLane struct {
	account  string
	points   []float32
	depleted []quotaWeekRange
}

type quotaWeekTimelineLayout struct {
	start    time.Time
	nowX     float32
	lanes    []quotaWeekLane
	weekdays [7]string
	hasMarks bool
}

// buildQuotaWeekTimelineLayout 计算「本周重置」泳道时间线的纯布局数据：
// 时间轴从本地时区今天 0 点开始跨 7 天，所有 x 坐标按条宽 w 四舍五入到整像素；
// ResetsAt 为零或落在轴外的窗口不画点，UsedPercent >= 100 时生成从 now 到 ResetsAt 的耗尽区间。
func buildQuotaWeekTimelineLayout(now time.Time, w float32, groups []quotaAccountGroup) quotaWeekTimelineLayout {
	localNow := now.In(time.Local)
	start := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, time.Local)
	const weekSpan = 7 * 24 * time.Hour
	end := start.Add(weekSpan)

	roundX := func(t time.Time) float32 {
		if w <= 0 {
			return 0
		}
		frac := float64(t.Sub(start)) / float64(weekSpan)
		if frac < 0 {
			frac = 0
		} else if frac > 1 {
			frac = 1
		}
		return float32(math.Round(frac * float64(w)))
	}

	nowX := roundX(localNow)
	layout := quotaWeekTimelineLayout{
		start: start,
		nowX:  nowX,
	}
	for i := 0; i < 7; i++ {
		wd := start.Add(time.Duration(i) * 24 * time.Hour).Weekday()
		layout.weekdays[i] = quotaWeekDayNames[wd]
	}

	for _, g := range groups {
		if g.disabled {
			continue
		}
		lane := quotaWeekLane{account: g.account}
		for _, win := range g.windows {
			if win.ResetsAt.IsZero() {
				continue
			}
			if win.ResetsAt.Before(start) || win.ResetsAt.After(end) {
				continue
			}
			x := roundX(win.ResetsAt)
			lane.points = append(lane.points, x)
			layout.hasMarks = true
			if win.UsedPercent >= 100 && win.ResetsAt.After(localNow) {
				lane.depleted = append(lane.depleted, quotaWeekRange{x0: nowX, x1: x})
			}
		}
		layout.lanes = append(layout.lanes, lane)
	}
	return layout
}

// composerQuotaWeekGroupsAndNames 按浮层账号展示顺序整理泳道分组并提取去前缀的显示名。
func composerQuotaWeekGroupsAndNames(provider string, windows []ProviderQuotaWindow, models []trayQuotaGroupModel) ([]quotaAccountGroup, map[string]string) {
	rawGroups := groupQuotaWindowsByAccount(windows)
	byAccount := make(map[string]quotaAccountGroup, len(rawGroups))
	for _, g := range rawGroups {
		if g.disabled {
			continue
		}
		byAccount[g.account] = g
	}

	displayNames := make(map[string]string)
	ordered := make([]quotaAccountGroup, 0, len(byAccount))
	seen := make(map[string]bool, len(byAccount))
	for _, mg := range models {
		for _, ma := range mg.Accounts {
			name := strings.TrimSuffix(ma.DisplayName, " · 最近使用")
			if name != "" {
				displayNames[ma.Account] = name
			}
			if g, ok := byAccount[ma.Account]; ok && !seen[ma.Account] {
				ordered = append(ordered, g)
				seen[ma.Account] = true
			}
		}
	}
	for _, g := range rawGroups {
		if g.disabled || seen[g.account] {
			continue
		}
		ordered = append(ordered, g)
		seen[g.account] = true
		if _, ok := displayNames[g.account]; !ok {
			displayNames[g.account] = trayQuotaAccountDisplayName(g.account, provider, "", "")
		}
	}
	return ordered, displayNames
}

// renderComposerQuotaWeekTimeline 在额度详情浮层账号列表下方渲染「本周重置」泳道时间线。
func renderComposerQuotaWeekTimeline(c *ui.Context, s *chatState, provider string, windows []ProviderQuotaWindow, models []trayQuotaGroupModel) {
	now := usagePanelNow()
	groups, displayNames := composerQuotaWeekGroupsAndNames(provider, windows, models)
	layout := buildQuotaWeekTimelineLayout(now, 100, groups)
	if !layout.hasMarks {
		if s != nil && s.composerView.quotaHoverAccount != "" {
			s.composerView.quotaHoverAccount = ""
			c.Invalidate()
		}
		return
	}

	multiAccount := len(layout.lanes) > 1
	trackColor := quotaBarTrackColor(c)
	depletedColor := ThemeDanger(c).Alpha(quotaWeekDepleteAlpha)
	nowColor := ThemeDanger(c)
	dotFill := ThemeSurfacePopover(c)
	dotStroke := ThemeAccent(c)

	hoveredAccount := ""

	ui.Column(c.Key("composer-quota-week-timeline")).
		FillWidth().
		Padding(0, Space4).
		Gap(Space2).
		Children(func() {
			ui.Row(c.Key("composer-quota-week-divider")).
				FillWidth().
				Height(Hairline).
				Background(ThemeDivider(c))

			ui.Text(c, "本周重置").
				FontSize(uiFont(TypeFootnote)).
				FontWeight(500).
				TextColor(ThemeSecondaryText(c)).
				SingleLine()

			ui.Column(c.Key("composer-quota-week-lanes")).
				FillWidth().
				Gap(Space1).
				Children(func() {
					for i, lane := range layout.lanes {
						accGroup := groups[i]
						laneKey := fmt.Sprintf("composer-quota-week-lane-%d", i)
						laneName := ""
						if multiAccount {
							laneName = displayNames[lane.account]
							if laneName == "" {
								laneName = trayQuotaAccountDisplayName(lane.account, provider, "", "")
							}
						}
						laneRow := ui.Row(c.Key(laneKey)).
							FillWidth().
							Height(ControlXS).
							AlignItems(ui.Center).
							Gap(Space3).
							Radius(RadiusSM)
						if multiAccount {
							laneRow.Label("本周重置泳道 " + laneName)
						} else {
							laneRow.Label("本周重置泳道")
						}
						if laneRow.Hovered() {
							hoveredAccount = lane.account
							laneRow.Background(ThemeOverlayHover(c))
						}
						laneRow.Children(func() {
							if multiAccount {
								ui.Row(c.Key(laneKey + "-label-box")).
									Width(quotaWeekLabelWidth).
									Shrink(0).
									AlignItems(ui.Center).
									Children(func() {
										ui.Text(c, laneName).
											FontSize(uiFont(TypeFootnote)).
											TextColor(ThemeSecondaryText(c)).
											SingleLine().
											Ellipsis("…").
											MinWidth(0).
											Shrink(1).
											Tooltip(laneName)
									})
							}

							track := ui.Box(c.Key(laneKey + "-track")).
								Grow(1).
								Width(0).
								Height(ControlXS)
							track.Draw(func(p *ui.Painter, r ui.Rect) {
								if r.W <= 0 || r.H <= 0 {
									return
								}
								painted := buildQuotaWeekTimelineLayout(now, r.W, []quotaAccountGroup{accGroup})
								if len(painted.lanes) == 0 {
									return
								}
								pl := painted.lanes[0]
								cy := r.Y + r.H/2
								barY := cy - quotaBarHeight/2

								// 泳道底色细条
								p.Fill(ui.Rect{X: r.X, Y: barY, W: r.W, H: quotaBarHeight}, trackColor, quotaBarRadius)

								// 额度耗尽区间（低透明度危险色）
								for _, rng := range pl.depleted {
									if rng.x1 <= rng.x0 {
										continue
									}
									p.Fill(ui.Rect{X: r.X + rng.x0, Y: barY, W: rng.x1 - rng.x0, H: quotaBarHeight}, depletedColor, quotaBarRadius)
								}

								// Now 线（1px 危险色竖线）
								p.Fill(ui.Rect{X: r.X + painted.nowX, Y: r.Y + Space0, W: Hairline, H: r.H - Space0*2}, nowColor, 0)

								// 重置点（空心小圆）
								for _, px := range pl.points {
									var dot ui.Path
									dot.Circle(r.X+px, cy, quotaWeekDotRadius)
									p.FillPath(&dot, dotFill)
									p.StrokePath(&dot, quotaWeekDotStroke, dotStroke)
								}
							})
						})
					}
				})

			// 轴下方 7 个周几刻度
			ui.Row(c.Key("composer-quota-week-weekdays")).
				FillWidth().
				Gap(Space3).
				Children(func() {
					if multiAccount {
						ui.Box(c.Key("composer-quota-week-weekdays-spacer")).
							Width(quotaWeekLabelWidth).
							Shrink(0)
					}
					ui.Row(c.Key("composer-quota-week-weekdays-track")).
						Grow(1).
						Width(0).
						Children(func() {
							for i, label := range layout.weekdays {
								ui.Box(c.Key(fmt.Sprintf("composer-quota-week-wd-%d", i))).
									Grow(1).
									Width(0).
									Center().
									Children(func() {
										ui.Text(c, label).
											FontSize(uiFont(TypeMicro)).
											TextColor(ThemeTertiaryText(c)).
											SingleLine()
									})
							}
						})
				})
		})

	if s != nil && s.composerView.quotaHoverAccount != hoveredAccount {
		s.composerView.quotaHoverAccount = hoveredAccount
		c.Invalidate()
	}
}
