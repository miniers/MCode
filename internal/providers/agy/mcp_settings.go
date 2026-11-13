package agy

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"mcode/internal/fsutil"
	"mcode/internal/harness"
)

const (
	// mcpBuiltinServerName 是内置 MCP 服务在 antigravity 配置里的名字。
	mcpBuiltinServerName = "mcode"
	// mcpReservedServerName 是仅用于审批的保留服务名，不作为工具来源。
	mcpReservedServerName = "mcode_approval"
	// mcpConfigFileName 是 antigravity 读取的 MCP 配置文件名，位于 gemini 目录的 config 子目录下。
	mcpConfigFileName = "mcp_config.json"
)

// mcpServerEntry 是 antigravity mcp_config.json 里的单个 HTTP 服务条目。
type mcpServerEntry struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

// mcpSettingsEntries 汇总本轮需要注入的 MCP 服务。
// 内置端点走 Endpoint(turn) 取地址与令牌，外部端点跳过内置与审批保留名。
func mcpSettingsEntries(turn harness.Turn, builtin harness.BuiltinMCP, external []harness.MCPEndpoint) map[string]mcpServerEntry {
	servers := make(map[string]mcpServerEntry)

	if url, token, workdirHeader := builtin.Endpoint(turn); url != "" {
		headers := make(map[string]string)
		if token != "" {
			headers["Authorization"] = "Bearer " + token
		}
		if workdirHeader && turn.WorkDir != "" {
			headers["X-Mcode-Workdir"] = turn.WorkDir
		}
		servers[mcpBuiltinServerName] = mcpServerEntry{URL: url, Headers: headers}
	}

	for _, ep := range external {
		name := strings.TrimSpace(ep.Name)
		url := strings.TrimSpace(ep.URL)
		if name == "" || url == "" {
			continue
		}
		if name == mcpBuiltinServerName || name == mcpReservedServerName {
			continue
		}
		headers := make(map[string]string)
		if ep.Token != "" {
			headers["Authorization"] = "Bearer " + ep.Token
		}
		servers[name] = mcpServerEntry{URL: url, Headers: headers}
	}

	return servers
}

// mcpSettingsJSON 生成 mcp_config.json 的内容。
func mcpSettingsJSON(servers map[string]mcpServerEntry) ([]byte, error) {
	return json.MarshalIndent(map[string]any{"mcpServers": servers}, "", "  ")
}

// prepareMCPGeminiDir 在临时目录里搭出一套会话级 gemini 目录：
// config/mcp_config.json 写入本轮的 MCP 服务，其余条目软链接到源目录，
// 从而既不动用户的全局 antigravity 配置，又保留登录态、设置与会话历史。
// 返回追加给 agy 的参数（--gemini_dir=...）与清理函数；没有端点时不生成目录。
func prepareMCPGeminiDir(turn harness.Turn, routeArgs, routeEnv []string, builtin harness.BuiltinMCP, external func(workDir, threadID string) []harness.MCPEndpoint) (args []string, cleanup func(), err error) {
	var endpoints []harness.MCPEndpoint
	if external != nil {
		endpoints = external(turn.WorkDir, turn.ThreadID)
	}
	servers := mcpSettingsEntries(turn, builtin, endpoints)
	if len(servers) == 0 {
		return nil, nil, nil
	}

	sourceDir := resolveSourceGeminiDir(routeArgs, turn.Env, routeEnv)
	// 源目录不存在或为空说明这一轮 agy 本来就没有登录态，临时目录只会让它更找不到凭据，
	// 因此放弃注入，交给 agy 自己按原样报未登录。
	if sessionMCPInjectSkipped(sourceDir) {
		log.Printf("agy: source gemini dir %q missing or empty, skip mcp injection", sourceDir)
		return nil, nil, nil
	}

	tmpDir, err := os.MkdirTemp("", "mcode-agy-mcp-")
	if err != nil {
		return nil, nil, fmt.Errorf("create session gemini dir: %w", err)
	}
	removeDir := func() { _ = os.RemoveAll(tmpDir) }

	// config 目录必须自己物化：若直接软链接到源目录，写 mcp_config.json 会改到用户的全局配置。
	if err := linkEntriesExcept(sourceDir, tmpDir, "config"); err != nil {
		removeDir()
		return nil, nil, err
	}

	configDir := filepath.Join(tmpDir, "config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		removeDir()
		return nil, nil, fmt.Errorf("create session config dir: %w", err)
	}
	if err := linkEntriesExcept(filepath.Join(sourceDir, "config"), configDir, mcpConfigFileName); err != nil {
		removeDir()
		return nil, nil, err
	}

	data, err := mcpSettingsJSON(servers)
	if err != nil {
		removeDir()
		return nil, nil, fmt.Errorf("marshal mcp settings: %w", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, mcpConfigFileName), data, 0o600); err != nil {
		removeDir()
		return nil, nil, fmt.Errorf("write mcp settings: %w", err)
	}

	return []string{"--gemini_dir=" + tmpDir}, removeDir, nil
}

// resolveSourceGeminiDir 返回本轮 agy 本会读取的 gemini 目录：
// 账号隔离时取路由参数里的 --gemini_dir，否则取 HOME 下的 .gemini。
func resolveSourceGeminiDir(routeArgs, turnEnv, routeEnv []string) string {
	for _, arg := range routeArgs {
		if path, ok := strings.CutPrefix(arg, "--gemini_dir="); ok && path != "" {
			return path
		}
	}
	home := lastEnvValue(routeEnv, "HOME")
	if home == "" {
		home = lastEnvValue(turnEnv, "HOME")
	}
	if home == "" {
		if dir, err := os.UserHomeDir(); err == nil {
			home = dir
		}
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".gemini")
}

// sessionMCPInjectSkipped 报告源 gemini 目录是否让本轮放弃 MCP 注入。
// 注入逻辑与 HostToolsAvailable 共用这一判断，避免两边得出不一致的结论：
// 放弃注入时会话里就不会有 mcode 的宿主工具。
func sessionMCPInjectSkipped(sourceDir string) bool {
	return !dirHasEntries(sourceDir)
}

// dirHasEntries 报告目录是否存在且非空。
func dirHasEntries(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

// lastEnvValue 返回 KEY=VALUE 列表中最后出现的取值，与子进程“后者覆盖前者”的语义一致。
func lastEnvValue(env []string, key string) string {
	value := ""
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			value = strings.TrimPrefix(kv, prefix)
		}
	}
	return value
}

// linkEntriesExcept 把源目录条目软链接进目标目录，跳过 skip 中的文件名与已存在的条目。
// 目录条目也是软链接，因此在源目录里新建文件仍会落到原位置，不会被临时目录吞掉。
func linkEntriesExcept(sourceDir, targetDir string, skip ...string) error {
	if sourceDir == "" {
		return nil
	}
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", sourceDir, err)
	}
	skipped := make(map[string]struct{}, len(skip))
	for _, name := range skip {
		skipped[name] = struct{}{}
	}
	for _, entry := range entries {
		name := entry.Name()
		if _, ok := skipped[name]; ok {
			continue
		}
		linkPath := filepath.Join(targetDir, name)
		if _, statErr := os.Lstat(linkPath); statErr == nil {
			continue
		}
		if err := fsutil.LinkOrCopy(filepath.Join(sourceDir, name), linkPath); err != nil {
			return fmt.Errorf("link %s: %w", linkPath, err)
		}
	}
	return nil
}
