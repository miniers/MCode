package mcodeui

import (
	"fmt"
	"math"
	"time"

	"github.com/egoist/mygo/ui"
)

type planLimit struct {
	Label       string
	UsedPercent float64
	ResetsAt    time.Time // ResetsAt 为零值表示未知
	// Err 是该行额度读取的错误文本（为空表示没有错误），RefreshedAt 是最近一次成功刷新时间
	// （零值表示未知）。两者由调用方按需填充，未填充时渲染行为与原来一致。
	Err         string
	RefreshedAt time.Time
}

// limitBarColor 按配额分档取色，与托盘、额度浮窗共用 quotaLevelColor。
func limitBarColor(c *ui.Context, percent float64) ui.Color {
	return quotaLevelColor(c, percent)
}

// formatLimitReset 生成中文的重置时间文案：
// resetsAt 零值返回空串；
// 已过去返回 "即将重置"；
// 24 小时内返回 "X 小时 Y 分钟后重置"（不足 1 小时只写 "Y 分钟后重置"）；
// 7 天内返回 "周三 14:30 重置"（中文星期：周日..周六）；
// 更久返回 "1月2日 重置"
func formatLimitReset(now, resetsAt time.Time) string {
	if resetsAt.IsZero() {
		return ""
	}
	if !resetsAt.After(now) {
		return "即将重置"
	}

	loc := now.Location()
	if loc == nil {
		loc = time.Local
	}
	rAt := resetsAt.In(loc)
	diff := rAt.Sub(now)

	if diff < 24*time.Hour {
		totalSeconds := int64(diff.Seconds())
		hours := int(totalSeconds / 3600)
		minutes := int((totalSeconds % 3600) / 60)
		if hours > 0 {
			return fmt.Sprintf("%d 小时 %d 分钟后重置", hours, minutes)
		}
		if minutes < 1 {
			minutes = 1
		}
		return fmt.Sprintf("%d 分钟后重置", minutes)
	}

	if diff < 7*24*time.Hour {
		weekdays := [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
		return fmt.Sprintf("%s %s 重置", weekdays[rAt.Weekday()], rAt.Format("15:04"))
	}

	return rAt.Format("1月2日") + " 重置"
}

// renderPlanLimitsView 渲染方案用量限额面板组件。
// limits 为空且 bankedResets==0 时不渲染任何东西；
// 标题行文案 "方案用量限额"，planName 非空时追加 " · "+planName，次要色，refreshing 为真时右侧显示加载指示器；
// 每个 limit 一行（LimitRow）：左侧 Label 主色 13 号字，右侧依次为重置时间（为空不显示）和百分比，次要色 11 号字；
// 数据过期时在百分比后追加 usageStaleSuffix；未登录/不支持用灰色说明行，出错用警告色并保留原错误。
// 下方进度条尺寸取 quota_style.go 的共用常量，底槽浅灰叠层，填充宽度按百分比（0..100），颜色 limitBarColor；行间距 8。
// bankedResets>0 时底部加一行 "储存的重置次数" 和数字。
func renderPlanLimitsView(c *ui.Context, key, planName string, limits []planLimit, bankedResets int, refreshing bool) {
	if len(limits) == 0 && bankedResets == 0 {
		return
	}

	headerText := "方案用量限额"
	if planName != "" {
		headerText += " · " + planName
	}

	ui.Column(c.Key(key)).
		FillWidth().
		Padding(settingsRowPadY, settingsRowPadX).
		Gap(settingsCardPad).
		Children(func() {
			// 标题行
			ui.Row(c.Key(key + "-header")).
				FillWidth().
				AlignItems(ui.Center).
				Children(func() {
					ui.Text(c, headerText).
						FontSize(uiFont(TypeBody)).
						TextColor(ThemeSecondaryText(c))

					if refreshing {
						ui.Box(c.Key(key + "-header-spacer")).Grow(1)
						LoadingIndicator(c, nil, "", IconSM, ThemeSecondaryText(c))
					}
				})

			// 额度行（LimitRow）
			now := time.Now()
			for i, lim := range limits {
				idx := i
				limit := lim
				status := usageStatusForData(limit.Err, true, limit.RefreshedAt, now)

				ui.Column(c.Key(fmt.Sprintf("%s-limit-%d", key, idx))).
					FillWidth().
					Gap(Space3).
					Children(func() {
						// 文本行：左侧 Label 主色 13 号字，右侧重置时间与百分比 次要色 11 号字
						ui.Row(c.Key(fmt.Sprintf("%s-limit-%d-row", key, idx))).
							FillWidth().
							AlignItems(ui.Center).
							Gap(Space3).
							Children(func() {
								ui.Text(c, limit.Label).
									FontSize(uiFont(TypeBody)).
									TextColor(ThemePrimaryText(c))

								ui.Box(c.Key(fmt.Sprintf("%s-limit-%d-spacer", key, idx))).Grow(1)

								resetStr := formatLimitReset(now, limit.ResetsAt)
								if resetStr != "" {
									ui.Text(c, resetStr).
										FontSize(uiFont(TypeCaption)).
										TextColor(ThemeSecondaryText(c))
								}

								percentStr := fmt.Sprintf("%d%%", int(math.Round(limit.UsedPercent)))
								ui.Text(c, percentStr).
									FontSize(uiFont(TypeCaption)).
									TextColor(quotaPercentColor(c, limit.UsedPercent))

								if usageStatusForData("", true, limit.RefreshedAt, now).Stale() {
									ui.Text(c, usageStaleSuffix).
										FontSize(uiFont(TypeCaption)).
										TextColor(ThemeSecondaryText(c))
								}
							})

						// 未登录/不支持用灰色说明行，出错用警告色并保留原错误。
						renderUsageStatusLine(c, status, 11)

						// 进度条：尺寸与底槽走 quota_style.go 的共用样式，填充宽度按百分比（0..100），颜色 limitBarColor
						clamped := math.Max(0, math.Min(100, limit.UsedPercent))
						ratio := float32(clamped / 100.0)
						barColor := limitBarColor(c, limit.UsedPercent)

						ui.Row(c.Key(fmt.Sprintf("%s-limit-%d-bar", key, idx))).
							FillWidth().
							Height(quotaBarHeight).
							Radius(quotaBarRadius).
							Background(quotaBarTrackColor(c)).
							Clip().
							Children(func() {
								if ratio > 0 {
									ui.Box(c.Key(fmt.Sprintf("%s-limit-%d-fill", key, idx))).
										FillHeight().
										Width(0).
										Grow(ratio).
										Radius(quotaBarRadius).
										Background(barColor)
								}
								if ratio < 1 {
									ui.Box(c.Key(fmt.Sprintf("%s-limit-%d-empty", key, idx))).
										Width(0).
										Grow(1 - ratio)
								}
							})
					})
			}

			// 储存的重置次数（BankedResetsView 只读版）
			if bankedResets > 0 {
				ui.Row(c.Key(key + "-banked-resets")).
					AlignItems(ui.Center).
					Gap(Space2).
					Children(func() {
						ui.Text(c, "储存的重置次数").
							FontSize(uiFont(TypeBody)).
							TextColor(ThemePrimaryText(c))
						ui.Text(c, fmt.Sprintf("%d", bankedResets)).
							FontSize(uiFont(TypeBody)).
							TextColor(ThemeSecondaryText(c))
					})
			}
		})
}

// withPlanLimitsErr 把额度刷新错误挂到第一行，避免每个窗口重复同一条错误说明。
func withPlanLimitsErr(limits []planLimit, errText string) {
	if len(limits) > 0 {
		limits[0].Err = errText
	}
}
