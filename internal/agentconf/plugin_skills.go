package agentconf

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 本文件只处理 Claude Code 插件自带的技能，不复用 mcp_plugins.go 里的同名辅助函数与类型：
// 类型名统一加 pluginSkill 前缀，避免与 MCP 插件扫描的类型、常量互相占用。

// pluginSkillInstalledJSON 对应 <home>/.claude/plugins/installed_plugins.json 的结构。
// plugins 的键形如 name@marketplace，值是安装记录数组（取第一项）。
type pluginSkillInstalledJSON struct {
	Plugins map[string][]struct {
		InstallPath string `json:"installPath"`
	} `json:"plugins"`
	EnabledPlugins map[string]bool `json:"enabledPlugins"`
}

// pluginSkillManifestJSON 只取插件清单里可能声明技能目录的字符串字段。
type pluginSkillManifestJSON struct {
	Skills string `json:"skills"`
}

// pluginSkillSource 是一个 Claude Code 插件提供的技能来源。
type pluginSkillSource struct {
	plugin string
	path   string
}

// pluginSkillSources 返回已启用 Claude Code 插件各自的技能目录，按插件 id 排序。
// 启用判定以 <home>/.claude/settings.json 的 enabledPlugins 为准，只有在它缺失时
// 才回退到 installed_plugins.json 的同名字段；读取或解析失败一律静默跳过该插件。
func pluginSkillSources(home string) []pluginSkillSource {
	if home == "" {
		return nil
	}
	claudeDir := filepath.Join(home, ".claude")

	var installed pluginSkillInstalledJSON
	if !pluginSkillReadJSON(filepath.Join(claudeDir, "plugins", "installed_plugins.json"), &installed) {
		return nil
	}

	enabled := installed.EnabledPlugins
	var settings struct {
		EnabledPlugins map[string]bool `json:"enabledPlugins"`
	}
	if pluginSkillReadJSON(filepath.Join(claudeDir, "settings.json"), &settings) && settings.EnabledPlugins != nil {
		enabled = settings.EnabledPlugins
	}

	ids := make([]string, 0, len(installed.Plugins))
	for id := range installed.Plugins {
		if enabled[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	var sources []pluginSkillSource
	for _, id := range ids {
		installs := installed.Plugins[id]
		if len(installs) == 0 || installs[0].InstallPath == "" {
			continue
		}
		root := installs[0].InstallPath
		pluginName, _, _ := strings.Cut(id, "@")
		sources = append(sources, pluginSkillSource{
			plugin: pluginName,
			path:   filepath.Join(root, pluginSkillRelativeDir(root)),
		})
	}
	return sources
}

// pluginSkillRelativeDir 返回插件内技能目录相对插件根目录的路径，默认 skills。
// plugin.json 里 strings 类型的 skills 字段（如 "./skills/"）优先。
func pluginSkillRelativeDir(root string) string {
	var manifest pluginSkillManifestJSON
	if !pluginSkillReadJSON(filepath.Join(root, ".claude-plugin", "plugin.json"), &manifest) {
		return "skills"
	}
	rel := strings.TrimSpace(manifest.Skills)
	if rel == "" {
		return "skills"
	}
	return rel
}

// pluginSkillReadJSON 读取并解析 JSON 文件，任何失败（不存在、无权限、格式错误）都返回 false。
func pluginSkillReadJSON(path string, out any) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(data, out) == nil
}
