package mcodeui

import "github.com/egoist/mygo/ui"

var pageExtensions = settingsPage{
	ID:    "extensions",
	Title: "扩展",
	Icon:  IconSparkles(),
	Keywords: []string{
		"skills", "skill.md", "prompts", "rules", "instructions", "custom skills", "workflows", "技能",
		"mcp", "model context protocol", "servers", "tools", "connect", "integrations", "github", "playwright", "notion", "linear", "slack", "figma", "supabase", "stripe", "resend", "email", "context7", "sentry", "postgres", "browser", "netlify", "deploy", "hosting",
		"nowledge", "nmem", "memory", "working memory", "threads", "sync", "记忆", "上下文",
	},
}

func init() {
	pageExtensions.Sections = append(
		withSettingsModule("技能", sectionSkills),
		withSettingsModule("MCP 服务", mcpSections...)...,
	)
	pageExtensions.Sections = append(pageExtensions.Sections, withSettingsModule("记忆", nowledgeSections...)...)
	pageExtensions.Sections = append(pageExtensions.Sections, withSettingsModule("记忆", advancedMemory)...)
	settingsTreePageDefs = append(settingsTreePageDefs, pageExtensions)
}

func renderPageExtensions(c *ui.Context, st *SettingsState) {
	renderSettingsTreePage(c, st, pageExtensions)
}
