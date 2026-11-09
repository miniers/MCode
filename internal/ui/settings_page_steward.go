package mcodeui

import (
	"fmt"
	"slices"
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

// stewardSections 拼出「总管」面板的分节：未启用时只留总开关，
// 模型与安静时段整节隐藏，避免改了配置却以为总管已在工作。
// st 为 nil 时按「全部行可见」拼装，供检索索引登记行 ID（索引不关心当前配置）。
func stewardSections(st *SettingsState) []settingsSection {
	enabled := st == nil || st.cfg.Orchestration.Steward.Enabled
	sections := []settingsSection{stewardMainSection(st)}
	if enabled {
		sections = append(sections,
			stewardModelSection,
			stewardQuietSection(st),
		)
	}
	sections = append(sections, stewardAdvancedSection)
	return sections
}

// stewardMainSection 是第一个分节：昵称、头像、总开关与 CLI fallback。
func stewardMainSection(st *SettingsState) settingsSection {
	sec := settingsSection{ID: "sec-steward-main", Title: "总管", Rows: []setting{
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
		{ID: "row-steward-enabled", Title: "启用总管", Aliases: []string{"总管", "默默", "后台", "看板"}, Control: toggleFunc(
			func(st *SettingsState) (bool, bool) { return st.cfg.Orchestration.Steward.Enabled, false },
			func(st *SettingsState, on bool) {
				st.mu.Lock()
				st.cfgMu.Lock()
				st.cfg.Orchestration.Steward.Enabled = on
				// 记下用户亲手动过启用开关，之后的迁移不再改写 enabled。
				st.cfg.Orchestration.Steward.EnabledChoice = true
				st.cfgMu.Unlock()
				st.mu.Unlock()
				st.save()
				// 侧栏的启用状态是带短缓存的，改完开关立刻让它重读，不必等缓存过期。
				invalidateStewardEnabledState(st.chat)
			})},
		{ID: "row-steward-cli-fallback", Title: "没有 API Key 时用本机 CLI", Control: toggle(
			val(func(cfg *config.Config) *bool { return &cfg.Orchestration.Steward.CLIFallback }, false))},
	}}

	return sec
}

// stewardModelSection 包含前台模型。
var stewardModelSection = settingsSection{
	ID:    "sec-steward-model",
	Title: "模型",
	Rows: []setting{
		{
			ID:      "row-steward-model",
			Title:   "前台模型",
			Aliases: []string{"总管", "模型", "通用设置"},
			Control: custom(renderStewardModelControl),
		},
	},
}

// renderStewardModelControl 画模型胶囊，以及选过模型后出现的「跟随通用设置」退回按钮。
func renderStewardModelControl(c *ui.Context, st *SettingsState) {
	sel := ModelSelection{
		Provider: st.cfg.Orchestration.Steward.Provider,
		Model:    st.cfg.Orchestration.Steward.Model,
	}
	ui.Row(c.Key("wrap-steward-model")).
		AlignItems(ui.Center).
		Gap(Space1).
		Children(func() {
			renderSwarmModelCapsuleCatalog(c, st, "steward-model", "steward-model", sel, "跟随通用设置", "选择前台模型", func(next ModelSelection) {
				st.cfg.Orchestration.Steward.Provider = next.Provider
				st.cfg.Orchestration.Steward.Model = next.Model
				st.save()
			}, frontdeskModelPickerCatalog(settingsModelPickerCatalog(st), st.cfg), true)

			// 选过模型后可以退回「跟随通用设置」，与编排页「与主代理相同」的做法一致。
			if !swarmEmptySelection(sel) {
				btnClear := ui.ButtonBase(c.Key("btn-steward-model-clear")).
					Label("跟随通用设置").
					Padding(settingsBadgePadY, Space3).
					Radius(RadiusSM).
					Background(ui.Transparent)
				if btnClear.Hovered() {
					btnClear.Background(ThemeHover(c))
				}
				if btnClear.Clicked() {
					st.cfg.Orchestration.Steward.Provider = ""
					st.cfg.Orchestration.Steward.Model = ""
					st.save()
				}
				btnClear.Children(func() {
					ui.Text(c, "跟随通用设置").
						FontSize(TypeCaption).
						TextColor(ThemeSecondaryText(c))
				})
			}
		})
}

// stewardQuietSection 是「安静时段」分节：这段时间总管不主动找你，
// 事件留到结束后汇总；时段打开后才显示具体时间范围。
func stewardQuietSection(st *SettingsState) settingsSection {
	sec := settingsSection{ID: "sec-steward-quiet", Title: "安静时段", Rows: []setting{{
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
			})}}}
	if st == nil || stewardQuietOn(st) {
		sec.Rows = append(sec.Rows, setting{ID: "row-steward-quiet-range", Title: "时间范围",
			Aliases: []string{"总管", "免打扰", "本地时间", "午夜"},
			Control: custom(renderStewardQuietRange)})
	}
	return sec
}

// renderStewardQuietRange 画安静时段的起止时刻。
func renderStewardQuietRange(c *ui.Context, st *SettingsState) {
	ui.Row(c.Key("wrap-steward-quiet-range")).
		Gap(Space3).
		AlignItems(ui.Center).
		Children(func() {
			renderStewardQuietClock(c, st, "sel-steward-quiet-start", "安静时段开始", &st.cfg.Orchestration.Steward.QuietStart, st.cfg.Orchestration.Steward.QuietEnd)
			ui.Text(c, "至").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
			renderStewardQuietClock(c, st, "sel-steward-quiet-end", "安静时段结束", &st.cfg.Orchestration.Steward.QuietEnd, st.cfg.Orchestration.Steward.QuietStart)
		})
}

// 面板正文每帧按当前配置挑行，检索索引却要看到全部行：st 为 nil 的一份声明补进面板，
// 索引才能收录这些行 ID（settings_page_steward.go 的 init 先于面板注册执行）。
func init() {
	sheetSteward.Sections = stewardSections(nil)
	registerStewardSheetSections()
}

// registerStewardSheetSections 把分节同步给已登记的面板副本。
func registerStewardSheetSections() {
	for i := range settingsTreeSheetDefs {
		if settingsTreeSheetDefs[i].ID == sheetSteward.ID {
			settingsTreeSheetDefs[i].Sections = sheetSteward.Sections
		}
	}
}

// renderPageSteward 渲染「总管」设置面板。
// 这里只负责配置（昵称、头像、模型、安静时段与主目录），不含运行逻辑。
func renderPageSteward(c *ui.Context, st *SettingsState) {
	ui.Column(c.Key("steward-sections-container")).Gap(settingsSectionGap).Children(func() {
		for _, sec := range stewardSections(st) {
			if sec.Visible != nil && !sec.Visible(st) {
				continue
			}
			renderSettingsTreeSection(c, st, sec, true)
		}
	})
}
