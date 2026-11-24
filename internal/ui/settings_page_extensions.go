package mcodeui

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

type extensionsTab int

const (
	extensionsTabSkills extensionsTab = iota
	extensionsTabMCP
	extensionsTabMemory
)

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
	pageExtensions.Sections = append(pageExtensions.Sections, sectionSkills)
	pageExtensions.Sections = append(pageExtensions.Sections, mcpSections...)
	pageExtensions.Sections = append(pageExtensions.Sections, nowledgeSections...)
	pageExtensions.Sections = append(pageExtensions.Sections, advancedMemory)
	settingsTreePageDefs = append(settingsTreePageDefs, pageExtensions)
}

func extensionsTabValid(tab extensionsTab) extensionsTab {
	if tab < extensionsTabSkills || tab > extensionsTabMemory {
		return extensionsTabSkills
	}
	return tab
}

func extensionsTabSections(tab extensionsTab) []settingsSection {
	switch tab {
	case extensionsTabMCP:
		return mcpSections
	case extensionsTabMemory:
		sections := make([]settingsSection, 0, len(nowledgeSections)+1)
		sections = append(sections, nowledgeSections...)
		sections = append(sections, advancedMemory)
		return sections
	default:
		return []settingsSection{sectionSkills}
	}
}

func extensionsTabForLegacyPage(id string) (extensionsTab, bool) {
	switch id {
	case "skills":
		return extensionsTabSkills, true
	case "mcp":
		return extensionsTabMCP, true
	case "nowledge":
		return extensionsTabMemory, true
	}
	return 0, false
}

// extensionsTabForRowID 按行 ID 判断它归属扩展页的哪一段，供搜索直达与旧深链自动切段。
func extensionsTabForRowID(id string) (extensionsTab, bool) {
	if id == "" {
		return 0, false
	}
	for _, row := range sectionSkills.Rows {
		if row.ID == id {
			return extensionsTabSkills, true
		}
	}
	for _, sec := range mcpSections {
		for _, row := range sec.Rows {
			if row.ID == id {
				return extensionsTabMCP, true
			}
		}
	}
	for _, sec := range nowledgeSections {
		for _, row := range sec.Rows {
			if row.ID == id {
				return extensionsTabMemory, true
			}
		}
	}
	for _, row := range advancedMemory.Rows {
		if row.ID == id {
			return extensionsTabMemory, true
		}
	}
	switch {
	case strings.HasPrefix(id, "row-mcp-"), strings.HasPrefix(id, "block-mcp-"):
		return extensionsTabMCP, true
	case strings.HasPrefix(id, "row-nowledge-"), id == "row-memory-backend":
		return extensionsTabMemory, true
	case strings.HasPrefix(id, "row-skills-"), strings.HasPrefix(id, "block-skills"):
		return extensionsTabSkills, true
	}
	return 0, false
}

func extensionsTabForTarget(id, sheet string) (extensionsTab, bool) {
	if tab, ok := extensionsTabForRowID(id); ok {
		return tab, true
	}
	switch sheet {
	case "mcp-add", "mcp-detail":
		return extensionsTabMCP, true
	}
	return 0, false
}

// syncExtensionsTab 在渲染前读取当前段；若待跳转或高亮的行属于扩展页内某一段，先切过去保证该行会渲染。
func syncExtensionsTab(st *SettingsState) extensionsTab {
	if st == nil {
		return extensionsTabSkills
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if tab, ok := extensionsTabForRowID(st.jumpToItemID); ok {
		st.extensionsTab = tab
	} else if tab, ok := extensionsTabForRowID(st.highlightItemID); ok {
		st.extensionsTab = tab
	}
	st.extensionsTab = extensionsTabValid(st.extensionsTab)
	return st.extensionsTab
}

func setExtensionsTab(st *SettingsState, tab extensionsTab) {
	if st == nil {
		return
	}
	tab = extensionsTabValid(tab)
	st.mu.Lock()
	changed := st.extensionsTab != tab
	st.extensionsTab = tab
	if targetTab, ok := extensionsTabForRowID(st.jumpToItemID); ok && targetTab != tab {
		st.jumpToItemID = ""
	}
	if targetTab, ok := extensionsTabForRowID(st.highlightItemID); ok && targetTab != tab {
		st.highlightItemID = ""
	}
	st.mu.Unlock()
	if changed {
		st.invalidate()
	}
}

func renderPageExtensions(c *ui.Context, st *SettingsState) {
	tab := syncExtensionsTab(st)
	ui.Column(c.Key("extensions-page")).FillWidth().Gap(settingsSectionGap).Children(func() {
		selected := int(tab)
		SegmentedFill(c, "extensions-tab", &selected, []Segment{
			{Label: "技能"},
			{Label: "MCP 服务"},
			{Label: "记忆"},
		}, func(next int) {
			setExtensionsTab(st, extensionsTab(next))
		}).FillWidth()

		page := pageExtensions
		page.Sections = extensionsTabSections(tab)
		renderSettingsTreePage(c, st, page)
	})
}
