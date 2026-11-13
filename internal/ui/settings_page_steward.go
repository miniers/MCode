package mcodeui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// 打开安静时段开关时的默认时间。
const (
	stewardQuietDefaultStart = "00:00"
	stewardQuietDefaultEnd   = "06:00"
)

// stewardQuietClockOptions 返回安静时段的可选时刻：一天内的整点与半点。
// current 不在其中时追加，手改配置写入的时刻也能正确显示。
func stewardQuietClockOptions(current string) []string {
	options := make([]string, 0, 49)
	for minutes := 0; minutes < 24*60; minutes += 30 {
		options = append(options, fmt.Sprintf("%02d:%02d", minutes/60, minutes%60))
	}
	if current != "" && !slices.Contains(options, current) {
		options = append(options, current)
		slices.Sort(options)
	}
	return options
}

// renderStewardQuietClock 渲染安静时段的一个时刻选择框；选到与另一端相同的时刻时不保存，
// 配置归一化会把首尾相等的时段清空，界面表现为开关自己关掉。
func renderStewardQuietClock(c *ui.Context, st *SettingsState, key, label string, value *string, other string) {
	current := *value
	sel := ui.Select(c.Key(key), &current, stewardQuietClockOptions(current)).
		Label(label).Width(settingsFieldNarrowWidth)
	if !sel.Changed() || current == *value {
		return
	}
	if current == other {
		st.noteError("安静时段的开始和结束不能相同")
		return
	}
	*value = current
	st.save()
}

// stewardQuietOn 判断安静时段是否开启：首尾都为空的时段算关闭。
func stewardQuietOn(st *SettingsState) bool {
	return st.cfg.Orchestration.Steward.QuietStart != "" && st.cfg.Orchestration.Steward.QuietEnd != ""
}

func stewardEnabled(st *SettingsState) bool { return st == nil || st.cfg.Orchestration.Steward.Enabled }

var pageSteward = settingsPage{
	ID: "steward", Title: "总管", Icon: IconBot(),
	Sections: []settingsSection{stewardMainSection(), stewardModelSection, stewardQuietSection, stewardAdvancedSection},
}

// stewardMainSection 是第一个分节：总开关、昵称、头像与 CLI 降级。
func stewardMainSection() settingsSection {
	sec := settingsSection{ID: "sec-steward-main", Title: "总管", Rows: []setting{
		{ID: "row-steward-enabled", Title: "启用总管", Aliases: []string{"总管", "默默", "后台", "看板"}, Control: toggleFunc(
			func(st *SettingsState) (bool, bool) { return st.cfg.Orchestration.Steward.Enabled, false },
			func(st *SettingsState, on bool) {
				st.mu.Lock()
				st.cfgMu.Lock()
				st.cfg.Orchestration.Steward.Enabled = on
				st.cfg.Orchestration.Steward.EnabledChoice = true
				st.cfgMu.Unlock()
				st.mu.Unlock()
				st.save()
				invalidateStewardEnabledState(st.chat)
			})},
		// 昵称：允许留空，留空时界面各处都显示默认名「总管」。
		{ID: "row-steward-name", Title: "昵称", Aliases: []string{"总管", "默默", "名字"}, Control: textFunc(
			func(st *SettingsState) (string, bool) { return st.cfg.Orchestration.Steward.Name, false },
			func(st *SettingsState, text string) {
				st.mu.Lock()
				st.cfgMu.Lock()
				st.cfg.Orchestration.Steward.Name = strings.TrimSpace(text)
				st.cfgMu.Unlock()
				st.mu.Unlock()
				st.noteTextEdit()
			}, config.DefaultStewardName)},
		// 头像：填一个 emoji 或一个字，留空时界面用名字的第一个字（没有昵称就是「总」）。
		{ID: "row-steward-avatar", Title: "头像", Aliases: []string{"总管", "emoji"}, Control: textFunc(
			func(st *SettingsState) (string, bool) { return st.cfg.Orchestration.Steward.Avatar, false },
			func(st *SettingsState, text string) {
				// 与配置归一一致：只保留前 2 个 rune（组合 emoji 占两个 rune）。
				runes := []rune(strings.TrimSpace(text))
				if len(runes) > 2 {
					runes = runes[:2]
				}
				st.mu.Lock()
				st.cfgMu.Lock()
				st.cfg.Orchestration.Steward.Avatar = string(runes)
				st.cfgMu.Unlock()
				st.mu.Unlock()
				st.noteTextEdit()
			}, "🙂")},
		{ID: "row-steward-cli-fallback", Title: "没有 API Key 时用本机 CLI", Control: toggle(
			val(func(cfg *config.Config) *bool { return &cfg.Orchestration.Steward.CLIFallback }, false))},
	}}

	return sec
}

// stewardModelSection 包含前台模型。
var stewardModelSection = settingsSection{
	ID:      "sec-steward-model",
	Title:   "模型",
	Visible: stewardEnabled,
	Rows: []setting{
		{
			ID:      "row-steward-model",
			Title:   "前台模型",
			Aliases: []string{"总管", "模型", "通用设置"},
			Control: custom(renderStewardModelControl),
		},
		{
			ID:      "row-steward-task-model",
			Title:   "任务模型",
			Aliases: []string{"总管", "模型", "子任务", "下发", "编排", "配对", "swarm"},
			Control: custom(renderStewardTaskModelControl),
		},
	},
}

// renderStewardTaskModelControl 画总管下发任务的模型胶囊：选编排配对即任务走编排，选单个模型即不走编排；
// 选过之后可退回自动。
func renderStewardTaskModelControl(c *ui.Context, st *SettingsState) {
	catalog := settingsModelPickerCatalog(st)
	sel := stewardTaskModelSelection(st.cfg, catalog)
	pairs := readySwarmPairs(st.cfg, st.cfg.Orchestration.Pairs, catalog.Providers, catalog.Pins)
	pair, hasPair := stewardTaskPair(st.cfg, pairs)
	if hasPair {
		sel = ModelSelection{}
	}
	ui.Row(c.Key("wrap-steward-task-model")).
		AlignItems(ui.Center).
		Gap(Space1).
		Children(func() {
			props := swarmModelCapsuleProps(st, "steward-task-model", "steward-task-model", sel, "自动", "选择任务模型", func(next ModelSelection) {
				st.cfg.Orchestration.Steward.TaskProvider = next.Provider
				st.cfg.Orchestration.Steward.TaskModel = next.Model
				st.cfg.Orchestration.Steward.TaskPairID = ""
				st.save()
			}, catalog, true)
			props.Pairs = pairs
			props.OnApplyPair = func(next SwarmPair) {
				st.cfg.Orchestration.Steward.TaskProvider = ""
				st.cfg.Orchestration.Steward.TaskModel = ""
				st.cfg.Orchestration.Steward.TaskPairID = next.ID
				st.save()
			}
			if hasPair {
				props.PairID = pair.ID
				props.CapsuleLabel = swarmPairTitle(pair, catalog.Providers)
				props.Tooltip = swarmPairTooltip(pair, "", catalog.Providers)
			}
			renderModelPicker(c, catalog.apply(props))

			if hasPair || !swarmEmptySelection(sel) {
				btnClear := ui.ButtonBase(c.Key("btn-steward-task-model-clear")).
					Label("自动").
					Padding(settingsBadgePadY, Space3).
					Radius(RadiusSM).
					Background(ui.Transparent)
				{
					scale := motionPressScale(c, btnClear, "button-press-scale", btnClear.Pressed())
					btnClear.Scale(scale, scale)
				}
				if btnClear.Hovered() {
					btnClear.Background(ThemeHover(c))
				}
				if btnClear.Clicked() {
					st.cfg.Orchestration.Steward.TaskProvider = ""
					st.cfg.Orchestration.Steward.TaskModel = ""
					st.cfg.Orchestration.Steward.TaskPairID = ""
					st.save()
				}
				btnClear.Children(func() {
					ui.Text(c, "自动").
						FontSize(uiFont(TypeCaption)).
						TextColor(ThemeSecondaryText(c))
				})
			}
		})
}

// renderStewardModelControl 画模型胶囊，以及选过模型后出现的「自动」退回按钮。
func renderStewardModelControl(c *ui.Context, st *SettingsState) {
	catalog := frontdeskModelPickerCatalog(settingsModelPickerCatalog(st), st.cfg)
	sel := stewardModelSelection(st.cfg, catalog)
	ui.Row(c.Key("wrap-steward-model")).
		AlignItems(ui.Center).
		Gap(Space1).
		Children(func() {
			renderSwarmModelCapsuleCatalog(c, st, "steward-model", "steward-model", sel, st.stewardAutoLabel(catalog, sel), "选择前台模型", func(next ModelSelection) {
				st.cfg.Orchestration.Steward.Provider = next.Provider
				st.cfg.Orchestration.Steward.Model = next.Model
				st.save()
			}, catalog, true)

			// 选过模型后可以退回自动选择。
			if !swarmEmptySelection(sel) {
				btnClear := ui.ButtonBase(c.Key("btn-steward-model-clear")).
					Label("自动").
					Padding(settingsBadgePadY, Space3).
					Radius(RadiusSM).
					Background(ui.Transparent)
				{
					scale := motionPressScale(c, btnClear, "button-press-scale", btnClear.Pressed())
					btnClear.Scale(scale, scale)
				}
				if btnClear.Hovered() {
					btnClear.Background(ThemeHover(c))
				}
				if btnClear.Clicked() {
					st.cfg.Orchestration.Steward.Provider = ""
					st.cfg.Orchestration.Steward.Model = ""
					st.save()
				}
				btnClear.Children(func() {
					ui.Text(c, "自动").
						FontSize(uiFont(TypeCaption)).
						TextColor(ThemeSecondaryText(c))
				})
			}
		})
}

// stewardQuietSection 在启用安静时段后显示具体时间范围。
var stewardQuietSection = settingsSection{ID: "sec-steward-quiet", Title: "安静时段", Visible: stewardEnabled, Rows: []setting{
	{ID: "row-steward-no-progress", Title: "无进展提醒", Aliases: []string{"看门狗", "watchdog", "卡住", "无进展"}, Control: custom(renderStewardNoProgress)},
	{
		ID:      "row-steward-quiet-enabled",
		Title:   "启用安静时段",
		Aliases: []string{"总管", "免打扰", "提醒"},
		Control: toggleFunc(
			func(st *SettingsState) (bool, bool) { return stewardQuietOn(st), false },
			func(st *SettingsState, on bool) {
				st.mu.Lock()
				st.cfgMu.Lock()
				if on {
					st.cfg.Orchestration.Steward.QuietStart = stewardQuietDefaultStart
					st.cfg.Orchestration.Steward.QuietEnd = stewardQuietDefaultEnd
				} else {
					st.cfg.Orchestration.Steward.QuietStart = ""
					st.cfg.Orchestration.Steward.QuietEnd = ""
				}
				st.cfgMu.Unlock()
				st.mu.Unlock()
				st.save()
			})},
	{ID: "row-steward-quiet-range", Title: "时间范围",
		Aliases: []string{"总管", "免打扰", "本地时间", "午夜"},
		Visible: func(st *SettingsState) bool { return st == nil || stewardQuietOn(st) },
		Control: custom(renderStewardQuietRange)},
}}

func renderStewardNoProgress(c *ui.Context, st *SettingsState) {
	minutes := st.cfg.Orchestration.Steward.NoProgressMinutes
	if minutes <= 0 {
		minutes = config.DefaultNoProgressMinutes
	}
	minuteLabel := fmt.Sprintf("%d 分钟", minutes)
	current := minuteLabel
	if st.cfg.Orchestration.Steward.NoProgressOff {
		current = "关闭"
	}
	options := []string{"关闭", "15 分钟", "30 分钟", "45 分钟", "60 分钟", "120 分钟"}
	if !slices.Contains(options, minuteLabel) {
		options = append(options, minuteLabel)
	}
	old := current
	pick := ui.Select(c.Key("row-steward-no-progress-select"), &current, options).
		Label("无进展提醒").Width(settingsFieldNarrowWidth)
	if !pick.Changed() && current == old {
		return
	}
	if current == "关闭" {
		st.cfg.Orchestration.Steward.NoProgressOff = true
	} else if n, err := strconv.Atoi(strings.TrimSuffix(current, " 分钟")); err == nil && n > 0 {
		st.cfg.Orchestration.Steward.NoProgressOff = false
		st.cfg.Orchestration.Steward.NoProgressMinutes = n
	} else {
		return
	}
	st.save()
}

// renderStewardQuietRange 画安静时段的起止时刻。
func renderStewardQuietRange(c *ui.Context, st *SettingsState) {
	ui.Row(c.Key("wrap-steward-quiet-range")).
		Gap(Space3).
		AlignItems(ui.Center).
		Children(func() {
			renderStewardQuietClock(c, st, "sel-steward-quiet-start", "安静时段开始", &st.cfg.Orchestration.Steward.QuietStart, st.cfg.Orchestration.Steward.QuietEnd)
			ui.Text(c, "至").FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
			renderStewardQuietClock(c, st, "sel-steward-quiet-end", "安静时段结束", &st.cfg.Orchestration.Steward.QuietEnd, st.cfg.Orchestration.Steward.QuietStart)
		})
}

func init() { settingsTreePageDefs = append(settingsTreePageDefs, pageSteward) }

// renderPageSteward 渲染「总管」设置页。
// 这里只负责配置（昵称、头像、模型、安静时段与主目录），不含运行逻辑。
func renderPageSteward(c *ui.Context, st *SettingsState) {
	renderSettingsTreePage(c, st, pageSteward)
}
