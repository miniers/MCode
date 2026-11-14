package agentconf

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
	"mcode/internal/fsutil"
)

// CanDeleteExternalMCPServer 判断来源是否为允许改写的 Claude/Codex 配置。
func CanDeleteExternalMCPServer(opts Options, srv MCPServer) bool {
	return externalMCPDeleteKind(opts, srv) != ""
}

func externalMCPDeleteKind(opts Options, srv MCPServer) string {
	if srv.ReadOnly || srv.Name == "" || !filepath.IsAbs(srv.Source) {
		return ""
	}
	home, project, err := resolveOptions(opts)
	if err != nil {
		return ""
	}
	switch srv.Agent {
	case "claude":
		if sameFilePath(srv.Source, filepath.Join(home, ".claude.json")) && (srv.Scope == "global" || srv.Scope == "project" && project != "") {
			return "claude"
		}
		if project != "" && srv.Scope == "project" && sameFilePath(srv.Source, filepath.Join(project, ".mcp.json")) {
			return "json"
		}
	case "codex":
		if srv.Scope == "global" && sameFilePath(srv.Source, filepath.Join(home, ".codex", "config.toml")) || project != "" && srv.Scope == "project" && sameFilePath(srv.Source, filepath.Join(project, ".codex", "config.toml")) {
			return "toml"
		}
	}
	return ""
}

// DeleteExternalMCPServer 从白名单内的来源文件删除服务，再清理 mcode 侧状态。
func DeleteExternalMCPServer(opts Options, srv MCPServer) error {
	kind := externalMCPDeleteKind(opts, srv)
	if kind == "" {
		return errors.New("该 MCP 来源不允许删除")
	}
	raw, err := os.ReadFile(srv.Source)
	if err != nil {
		return err
	}
	info, err := os.Stat(srv.Source)
	if err != nil {
		return err
	}
	var out []byte
	if kind == "toml" {
		out, err = deleteExternalMCPTOML(raw, srv.Name)
	} else {
		path := []string{"mcpServers", srv.Name}
		if kind == "claude" && srv.Scope == "project" {
			_, project, _ := resolveOptions(opts)
			var cfg claudeConfigFileJSON
			if err = json.Unmarshal(raw, &cfg); err != nil {
				return err
			}
			path = nil
			for _, key := range claudeProjectKeyCandidates(project) {
				if _, ok := cfg.Projects[key].MCPServers[srv.Name]; ok {
					path = []string{"projects", key, "mcpServers", srv.Name}
					break
				}
			}
			if path == nil {
				return errors.New("目标 MCP 服务不存在")
			}
		}
		out, err = deleteExternalMCPJSON(raw, path)
	}
	if err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(srv.Source, out, info.Mode().Perm()); err != nil {
		return err
	}
	if err := cleanupExternalMCPState(opts, srv); err != nil {
		return fmt.Errorf("已从 %s 删除服务，但清理 mcode 状态失败：%w", srv.Source, err)
	}
	return nil
}

// 按解码器的原文偏移删除成员，避免重排其它键或重新编码值。
func deleteExternalMCPJSON(raw []byte, path []string) ([]byte, error) {
	if !json.Valid(raw) {
		return nil, errors.New("无效的 MCP JSON 配置")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("MCP JSON 路径不是对象")
	}
	previousEnd := -1
	for d.More() {
		start := int(d.InputOffset())
		for start < len(raw) && (raw[start] == ',' || strings.ContainsRune(" \t\r\n", rune(raw[start]))) {
			start++
		}
		key, err := d.Token()
		if err != nil {
			return nil, err
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, err
		}
		end := int(d.InputOffset())
		if key == path[0] {
			if len(path) > 1 {
				edited, err := deleteExternalMCPJSON(value, path[1:])
				if err != nil {
					return nil, err
				}
				valueStart := end - len(value)
				out := append([]byte(nil), raw[:valueStart]...)
				out = append(out, edited...)
				return append(out, raw[end:]...), nil
			}
			from, to := start, end
			if d.More() {
				for to < len(raw) && raw[to] != ',' {
					to++
				}
				to++
			} else if previousEnd >= 0 {
				from = previousEnd
			}
			out := append([]byte(nil), raw[:from]...)
			out = append(out, raw[to:]...)
			if !json.Valid(out) {
				return nil, errors.New("删除后的 MCP JSON 配置无效")
			}
			return out, nil
		}
		previousEnd = end
	}
	return nil, errors.New("目标 MCP 服务不存在")
}

func deleteExternalMCPTOML(raw []byte, name string) ([]byte, error) {
	var cfg map[string]any
	if err := toml.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	servers, _ := cfg["mcp_servers"].(map[string]any)
	if _, ok := servers[name]; !ok {
		return nil, errors.New("目标 MCP 服务不存在")
	}
	var p unstable.Parser
	p.Reset(raw)
	var out []byte
	cursor := 0
	deleting := false
	for p.NextExpression() {
		node := p.Expression()
		if node.Kind == unstable.Table || node.Kind == unstable.ArrayTable {
			keys := node.Key()
			var parts []string
			for keys.Next() {
				parts = append(parts, string(keys.Node().Data))
			}
			deleting = len(parts) >= 2 && parts[0] == "mcp_servers" && parts[1] == name
		}
		if !deleting || node.Kind != unstable.Table && node.Kind != unstable.ArrayTable && node.Kind != unstable.KeyValue {
			continue
		}
		start := int(node.Raw.Offset)
		end := start + int(node.Raw.Length)
		if node.Kind != unstable.KeyValue {
			start = int(node.Child().Raw.Offset)
			end = start
		}
		start = bytes.LastIndexByte(raw[:start], '\n') + 1
		if newline := bytes.IndexByte(raw[end:], '\n'); newline >= 0 {
			end += newline + 1
		} else {
			end = len(raw)
		}
		out = append(out, raw[cursor:start]...)
		cursor = end
	}
	if err := p.Error(); err != nil {
		return nil, err
	}
	out = append(out, raw[cursor:]...)
	cfg = nil
	if err := toml.Unmarshal(out, &cfg); err != nil {
		return nil, err
	}
	servers, _ = cfg["mcp_servers"].(map[string]any)
	if _, ok := servers[name]; ok {
		return nil, errors.New("删除后目标 MCP 服务仍存在")
	}
	return out, nil
}

func cleanupExternalMCPState(opts Options, srv MCPServer) error {
	servers, err := ScanMCP(opts)
	if err != nil {
		return err
	}
	sameName, sameKey := false, false
	key := srv.Agent + ":" + srv.Scope + ":" + srv.Name
	for _, other := range servers {
		sameName = sameName || other.Name == srv.Name
		sameKey = sameKey || other.Agent+":"+other.Scope+":"+other.Name == key
	}
	paths := []string{getMCPConfigPath(opts, "global")}
	if opts.ProjectDir != "" {
		paths = append(paths, getMCPConfigPath(opts, "project"))
	}
	for _, path := range paths {
		cfg, err := loadCustomMCPConfig(path)
		if err != nil {
			return err
		}
		changed := false
		filter := func(keys []string, target string, remove bool) []string {
			if !remove {
				return keys
			}
			kept := make([]string, 0, len(keys))
			for _, k := range keys {
				if k == target {
					changed = true
				} else {
					kept = append(kept, k)
				}
			}
			return kept
		}
		cfg.DisabledServers = filter(cfg.DisabledServers, srv.Name, !sameName)
		cfg.NewThreadDisabled = filter(cfg.NewThreadDisabled, key, !sameKey)
		if changed {
			if err := saveCustomMCPConfig(path, cfg); err != nil {
				return err
			}
		}
	}
	if sameName {
		return nil
	}
	policyPath := MCPToolPolicyPath(resolveDataDir(opts))
	policy, err := LoadMCPToolPolicy(policyPath)
	if err != nil {
		return err
	}
	_, disabled := policy.Disabled[srv.Name]
	_, approved := policy.AutoApprove[srv.Name]
	if !disabled && !approved {
		return nil
	}
	delete(policy.Disabled, srv.Name)
	delete(policy.AutoApprove, srv.Name)
	return SaveMCPToolPolicy(policyPath, policy)
}
