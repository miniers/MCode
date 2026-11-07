package mcodeui

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// swarmNumberControl 保留迁移前的显示规则，并在写入时按需吸附。
type swarmNumberControl struct {
	numberControl
	display func(int) int
	step    int
}

func (n swarmNumberControl) render(c *ui.Context, st *SettingsState, s setting) {
	n.renderDisabled(c, st, s, false)
}

func (n swarmNumberControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	cur := n.b.value(&st.cfg)
	if n.display != nil {
		cur = n.display(cur)
	}
	ui.Row(c.Key(s.ID + "-value")).AlignItems(ui.Center).Gap(Space2).Children(func() {
		textFunc(func(*SettingsState) (string, bool) {
			return strconv.Itoa(cur), false
		}, func(_ *SettingsState, text string) {
			parsed, err := strconv.Atoi(strings.TrimSpace(text))
			if err != nil {
				return
			}
			if n.step > 1 {
				parsed = int(math.Round(float64(parsed)/float64(n.step))) * n.step
			}
			parsed = min(max(parsed, n.min), n.max)
			if parsed != n.b.value(&st.cfg) {
				commitSettingsControl(st, func(cfg *config.Config) { n.b.write(cfg, parsed) })
			}
		}, strconv.Itoa(n.b.def)).
			withDisabled(func(*SettingsState) bool { return disabled }).
			renderDisabled(c, st, setting{ID: s.ID, Title: s.Title}, disabled)
		if n.unit != "" {
			ui.Text(c, n.unit).FontSize(TypeBody).TextColor(ThemeSecondaryText(c))
		}
	})
}

var advancedOrchestration = settingsSection{
	ID:    "advanced.orchestration",
	Title: "高级",
	Rows: []setting{
		{
			ID: "row-swarm-max-bees", Title: "并发上限", Lock: "swarm-max-bees",
			Aliases: []string{"子代理", "并发", "默认", "自动"},
			Control: number(val(func(c *config.Config) *int { return &c.Orchestration.MaxBees }, 4), "", config.MinSwarmMaxBees, config.MaxSwarmMaxBees),
		},
		{
			ID: "row-swarm-max-rounds", Title: "最多派发轮次",
			Aliases: []string{"子代理", "派发", "默认", "不限"},
			Control: swarmNumberControl{numberControl: number(val(func(c *config.Config) *int { return &c.Orchestration.MaxRounds }, 10), "轮", config.MinSwarmMaxRounds, config.MaxSwarmMaxRounds), display: func(v int) int { return max(0, v) }},
		},
		{
			ID: "row-swarm-bee-timeout", Title: "子代理运行超时", Lock: "swarm-bee-timeout",
			Aliases: []string{"子代理", "超时", "默认"},
			Control: swarmNumberControl{numberControl: number(val(func(c *config.Config) *int { return &c.Orchestration.BeeTimeoutMinutes }, config.DefaultSwarmBeeTimeoutMinutes), "分钟", config.MinSwarmBeeTimeoutMinutes, config.MaxSwarmBeeTimeoutMinutes), display: func(v int) int {
				if v <= 0 {
					return config.DefaultSwarmBeeTimeoutMinutes
				}
				return min(max(v, config.MinSwarmBeeTimeoutMinutes), config.MaxSwarmBeeTimeoutMinutes)
			}},
		},
		{
			ID: "row-swarm-request-timeout", Title: "请求总时长预算", Lock: "swarm-request-timeout",
			Aliases: []string{"子代理", "超时", "默认"},
			Control: swarmNumberControl{numberControl: number(val(func(c *config.Config) *int { return &c.Orchestration.RequestTimeoutMinutes }, config.DefaultSwarmRequestTimeoutMinutes), "分钟", config.MinSwarmRequestTimeoutMinutes, config.MaxSwarmRequestTimeoutMinutes), step: 10, display: func(v int) int {
				if v <= 0 {
					return config.DefaultSwarmRequestTimeoutMinutes
				}
				return min(max(v, config.MinSwarmRequestTimeoutMinutes), config.MaxSwarmRequestTimeoutMinutes)
			}},
		},
		{
			ID: "row-swarm-review-max-lines", Title: "自动通过行数上限", Lock: "swarm-review-bees",
			Aliases: []string{"审查", "行数", "默认"},
			Control: swarmNumberControl{numberControl: number(val(func(c *config.Config) *int { return &c.Orchestration.ReviewMaxChangedLines }, config.DefaultReviewMaxChangedLines), "行", config.MinReviewMaxChangedLines, config.MaxReviewMaxChangedLines), display: func(v int) int { return max(0, v) }},
			Visible: func(st *SettingsState) bool { return st.cfg.Orchestration.ReviewModeValue() == config.ReviewModeAI },
		},
	},
}

var stewardAdvancedSection = settingsSection{
	ID:    "sec-steward-advanced",
	Title: "高级",
	Rows: []setting{
		{
			ID: "row-steward-home-dir", Title: "记忆与状态目录",
			Aliases: []string{"总管", "存储", "目录", "默认"},
			Control: custom(func(c *ui.Context, st *SettingsState) {
				ui.Text(c, st.cfg.Orchestration.Steward.ResolvedHomeDir(st.dataDir)).FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
			}),
		},
		{
			ID: "row-steward-wake-limit", Title: "每天主动提醒上限",
			Aliases: []string{"总管", "提醒", "默认"},
			Control: swarmNumberControl{numberControl: number(val(func(c *config.Config) *int { return &c.Orchestration.Steward.DailyWakeLimit }, config.DefaultStewardDailyWakeLimit), "次/天", config.MinStewardDailyWakeLimit, config.MaxStewardDailyWakeLimit), display: func(v int) int {
				if v <= 0 {
					return config.DefaultStewardDailyWakeLimit
				}
				return v
			}},
			Enabled: func(st *SettingsState) bool { return st.cfg.Orchestration.Steward.Enabled },
			Status: func(st *SettingsState, _ bool) string {
				used, limit, ok := stewardWakesTodayCached(st)
				if !ok {
					return ""
				}
				return fmt.Sprintf("今天已提醒 %d 次，还剩 %d 次", used, max(0, limit-used))
			},
		},
	},
}
