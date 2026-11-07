package agentconf

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// pluginRootVar 是插件 MCP 配置里指向插件安装目录的占位符，Claude Code 启动服务时会展开它。
const pluginRootVar = "${CLAUDE_PLUGIN_ROOT}"

var invalidPluginNameChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

type installedPluginsJSON struct {
	Plugins map[string][]struct {
		Scope       string `json:"scope"`
		InstallPath string `json:"installPath"`
	} `json:"plugins"`
	EnabledPlugins map[string]bool `json:"enabledPlugins"`
}

type pluginManifestJSON struct {
	// MCPServers 可以是内联对象，也可以是相对插件根目录的配置文件路径。
	MCPServers json.RawMessage `json:"mcpServers"`
}

// scanPluginMCP 读取已启用的 Claude Code 插件自带的 MCP 服务。
// 服务名与 Claude Code 保持一致（plugin_<插件>_<服务>），这样工具名 mcp__plugin_<插件>_<服务>__* 不变。
func scanPluginMCP(home string) []MCPServer {
	claudeDir := filepath.Join(home, ".claude")
	var installed installedPluginsJSON
	if !readJSONFile(filepath.Join(claudeDir, "plugins", "installed_plugins.json"), &installed) {
		return nil
	}
	enabled := installed.EnabledPlugins
	var settings struct {
		EnabledPlugins map[string]bool `json:"enabledPlugins"`
	}
	if readJSONFile(filepath.Join(claudeDir, "settings.json"), &settings) && settings.EnabledPlugins != nil {
		enabled = settings.EnabledPlugins
	}

	ids := make([]string, 0, len(installed.Plugins))
	for id := range installed.Plugins {
		if enabled[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	var servers []MCPServer
	for _, id := range ids {
		installs := installed.Plugins[id]
		if len(installs) == 0 || installs[0].InstallPath == "" {
			continue
		}
		pluginName, _, _ := strings.Cut(id, "@")
		servers = append(servers, scanPluginRoot(installs[0].InstallPath, pluginName)...)
	}
	return servers
}

// scanPluginRoot 解析单个插件：优先 plugin.json 的 mcpServers，其次插件根目录的 .mcp.json。
func scanPluginRoot(root, pluginName string) []MCPServer {
	var raws map[string]json.RawMessage
	source := ""

	var manifest pluginManifestJSON
	manifestPath := filepath.Join(root, ".claude-plugin", "plugin.json")
	if readJSONFile(manifestPath, &manifest) && len(manifest.MCPServers) > 0 {
		var ref string
		if json.Unmarshal(manifest.MCPServers, &ref) == nil {
			source = filepath.Join(root, ref)
			raws = readPluginMCPFile(source)
		} else if json.Unmarshal(manifest.MCPServers, &raws) == nil {
			source = manifestPath
		}
	}
	if len(raws) == 0 {
		source = filepath.Join(root, ".mcp.json")
		raws = readPluginMCPFile(source)
	}

	absSource, _ := filepath.Abs(source)
	names := make([]string, 0, len(raws))
	for name := range raws {
		names = append(names, name)
	}
	sort.Strings(names)

	var servers []MCPServer
	for _, name := range names {
		srv, err := parseClaudeItem(name, "global", absSource, raws[name])
		if err != nil {
			continue
		}
		srv.Name = invalidPluginNameChars.ReplaceAllString("plugin_"+pluginName+"_"+name, "_")
		srv.Command = strings.ReplaceAll(srv.Command, pluginRootVar, root)
		srv.URL = strings.ReplaceAll(srv.URL, pluginRootVar, root)
		for i, arg := range srv.Args {
			srv.Args[i] = strings.ReplaceAll(arg, pluginRootVar, root)
		}
		if srv.Env == nil {
			srv.Env = map[string]string{}
		}
		for k, v := range srv.Env {
			srv.Env[k] = strings.ReplaceAll(v, pluginRootVar, root)
		}
		// 与 Claude Code 一致，向插件服务暴露自身安装目录。
		srv.Env["CLAUDE_PLUGIN_ROOT"] = root
		servers = append(servers, srv)
	}
	return servers
}

// readPluginMCPFile 读取插件的 MCP 配置文件，兼容 {"mcpServers": {...}} 与直接以服务名为键两种写法。
func readPluginMCPFile(path string) map[string]json.RawMessage {
	var top map[string]json.RawMessage
	if !readJSONFile(path, &top) {
		return nil
	}
	if inner, ok := top["mcpServers"]; ok {
		var servers map[string]json.RawMessage
		if json.Unmarshal(inner, &servers) == nil {
			return servers
		}
		return nil
	}
	return top
}

func readJSONFile(path string, out any) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(data, out) == nil
}
