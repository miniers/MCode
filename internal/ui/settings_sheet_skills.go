package mcodeui

import (
	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
)

// 技能搜索的文本不在 config.Config 里，由 SettingsState 持有（见 getSkillsSearchText）。
func renderSkillsSearchBox(c *ui.Context, st *SettingsState, query *string) {
	field := SearchField(c, TextFieldProps{
		Key:         "input-skills-search",
		Value:       query,
		Label:       "搜索技能",
		Placeholder: "搜索技能",
		Grow:        true,
		OnChange: func(string) {
			st.invalidate()
		},
	})
	field.Margin(0, 0, 12, 0)
}

// 技能开关写入 agentconf 而非 config.Config，失败时保留原值。
func renderSkillToggle(c *ui.Context, st *SettingsState, key string, sk agentconf.Skill) {
	toggleFunc(
		func(*SettingsState) (bool, bool) { return sk.Enabled || !sk.Disabled, false },
		func(st *SettingsState, on bool) { applySkillEnabled(st, sk, on) },
	).render(c, st, setting{ID: key, Title: sk.Name})
}
