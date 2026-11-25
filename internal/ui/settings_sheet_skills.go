package mcodeui

import (
	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
)

// 技能搜索的文本不在 config.Config 里，由 SettingsState 持有（见 getSkillsSearchText）。
func renderSkillsSearchBox(c *ui.Context, st *SettingsState, query *string) {
	SearchField(c, TextFieldProps{
		Key:         "input-skills-search",
		Value:       query,
		Label:       "搜索技能",
		Placeholder: "搜索技能",
		Grow:        true,
		Clearable:   true,
		ClearLabel:  "清除搜索",
		OnChange: func(string) {
			st.invalidate()
		},
	})
}

// 技能开关写入 agentconf 而非 config.Config，失败时保留原值。
func renderSkillToggle(c *ui.Context, st *SettingsState, key, title string, members []agentconf.Skill) {
	allEnabled := len(members) > 0
	for _, sk := range members {
		if !isSkillMemberEnabled(sk) {
			allEnabled = false
			break
		}
	}
	toggleFunc(
		func(*SettingsState) (bool, bool) { return allEnabled, false },
		func(st *SettingsState, on bool) { setSkillsEnabled(st, members, on) },
	).render(c, st, setting{ID: key, Title: title})
}
