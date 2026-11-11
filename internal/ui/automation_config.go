package mcodeui

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

func automationConfigLines(a Automation) []string {
	lines := []string{a.Prompt}
	schedule := automationScheduleText(a.Schedule)
	if a.Schedule.Timezone != "" {
		schedule += " · " + a.Schedule.Timezone
	}
	lines = append(lines, schedule)
	for _, field := range []struct{ name, value string }{
		{"运行模式", a.RunMode}, {"供应商", a.Provider}, {"模型", a.Model},
		{"推理强度", a.Effort}, {"权限", a.RuntimeMode}, {"停止条件", a.StopWhen},
	} {
		if strings.TrimSpace(field.value) != "" {
			value := field.value
			if field.name == "权限" {
				value = automationRuntimeModeLabel(value)
			}
			lines = append(lines, field.name+"："+value)
		}
	}
	maxRuns := a.MaxRuns
	if maxRuns == 0 {
		maxRuns = a.MaxIterations
	}
	if maxRuns == 0 {
		lines = append(lines, "不限")
	} else {
		lines = append(lines, fmt.Sprintf("最多 %d 次", maxRuns))
	}
	if a.CatchUp {
		lines = append(lines, "错过后补跑：开启")
	} else {
		lines = append(lines, "错过后补跑：关闭")
	}
	if a.MaxConsecutiveFailures > 0 {
		lines = append(lines, fmt.Sprintf("连续失败 %d 次后暂停", a.MaxConsecutiveFailures))
	}
	return lines
}

func renderAutomationConfig(c *ui.Context, a Automation) {
	ui.Column(c.Key("automation-config")).FillWidth().Gap(Space7).Transition(motionTransition(c, ui.ElementTransition{Duration: DurBase, Enter: &ui.Motion{Opacity: 0}})).Children(func() {
		ui.Text(c, a.Prompt).FontSize(uiFont(TypeBody)).FillWidth().TextColor(ThemePrimaryText(c)).Selectable()
		section := func(key, title string, body func()) {
			ui.Column(c.Key(key)).FillWidth().Gap(Space3).Children(func() {
				ui.Text(c, title).FontSize(uiFont(TypeBody)).FontWeight(500).TextColor(ThemePrimaryText(c))
				body()
			})
		}
		field := func(name, value string) {
			if strings.TrimSpace(value) != "" {
				ui.Text(c, name+"："+value).FontSize(uiFont(TypeFootnote)).FillWidth().TextColor(ThemePrimaryText(c)).Selectable()
			}
		}
		section("automation-config-schedule", "计划", func() {
			schedule := automationScheduleText(a.Schedule)
			if a.Schedule.Timezone != "" {
				schedule += " · " + a.Schedule.Timezone
			}
			field("调度", schedule)
			status := "已暂停"
			if a.Enabled {
				status = "已启用"
			}
			field("状态", status)
		})
		section("automation-config-execution", "执行", func() {
			field("运行模式", a.RunMode)
			field("供应商", a.Provider)
			field("模型", a.Model)
			field("推理强度", a.Effort)
			if a.RuntimeMode != "" {
				field("权限", automationRuntimeModeLabel(a.RuntimeMode))
			}
			if a.RunMode == "heartbeat" {
				ui.Text(c, "心跳模式沿用原会话设置").FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
			}
		})
		section("automation-config-limits", "停止与补跑", func() {
			field("停止条件", a.StopWhen)
			maxRuns := a.MaxRuns
			if maxRuns == 0 {
				maxRuns = a.MaxIterations
			}
			limit := "不限"
			if maxRuns != 0 {
				limit = fmt.Sprintf("最多 %d 次", maxRuns)
			}
			field("运行次数", limit)
			catchUp := "关闭"
			if a.CatchUp {
				catchUp = "开启"
			}
			field("错过后补跑", catchUp)
			if a.MaxConsecutiveFailures > 0 {
				field("暂停", fmt.Sprintf("连续失败 %d 次后暂停", a.MaxConsecutiveFailures))
			}
		})
	})
}
