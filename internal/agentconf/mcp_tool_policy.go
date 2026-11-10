package agentconf

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"mcode/internal/fsutil"
)

// DefaultMCPFoldThreshold 是工具折叠的默认阈值：单个服务过滤后的工具数严格大于它时才折叠。
const DefaultMCPFoldThreshold = 40

// MCPToolPolicy 是 MCP 工具级开关的持久化结构，按 服务名 -> 工具名 组织。
// Disabled 命中的工具不会暴露给 Agent；AutoApprove 命中的工具跳过用户审批。
// FoldThreshold 控制工具折叠：0 表示使用 DefaultMCPFoldThreshold，负数表示关闭折叠，
// 正数表示自定义阈值（工具数严格大于该值才折叠）。
type MCPToolPolicy struct {
	Disabled      map[string]map[string]bool `json:"disabled"`
	AutoApprove   map[string]map[string]bool `json:"autoApprove"`
	FoldThreshold int                        `json:"foldThreshold,omitempty"`
}

// EffectiveFoldThreshold 返回折叠阈值：关闭折叠时返回 -1，否则返回实际生效的阈值。
func (p MCPToolPolicy) EffectiveFoldThreshold() int {
	if p.FoldThreshold < 0 {
		return -1
	}
	if p.FoldThreshold == 0 {
		return DefaultMCPFoldThreshold
	}
	return p.FoldThreshold
}

// MCPToolPolicyPath 返回数据目录下的策略文件路径。
func MCPToolPolicyPath(dataDir string) string {
	return filepath.Join(dataDir, "mcp_tool_policy.json")
}

// ResolveMCPToolPolicyPath 按默认数据目录规则（MCODE_DATA 环境变量，其次 ~/.mcode）解析策略文件路径；
// 与 ScanMCP 使用的数据目录来源保持一致。
func ResolveMCPToolPolicyPath() string {
	dir := resolveDataDir(Options{})
	if dir == "" {
		return ""
	}
	return MCPToolPolicyPath(dir)
}

// LoadMCPToolPolicy 读取策略文件；path 为空或文件不存在时返回空策略且不报错。
// 成功返回的策略两个 map 保证非 nil，便于调用方直接写入。
func LoadMCPToolPolicy(path string) (MCPToolPolicy, error) {
	if path == "" {
		return emptyMCPToolPolicy(), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return emptyMCPToolPolicy(), nil
		}
		return MCPToolPolicy{}, err
	}
	var policy MCPToolPolicy
	if err := json.Unmarshal(raw, &policy); err != nil {
		return MCPToolPolicy{}, err
	}
	policy.normalize()
	return policy, nil
}

// emptyMCPToolPolicy 返回两个 map 均已初始化的空策略。
func emptyMCPToolPolicy() MCPToolPolicy {
	p := MCPToolPolicy{}
	p.normalize()
	return p
}

// SaveMCPToolPolicy 原子写入策略文件（临时文件 + rename），文件权限 0600。
// 目录不存在时以 0700 权限递归创建；写入失败时原文件内容不受破坏。
func SaveMCPToolPolicy(path string, p MCPToolPolicy) error {
	if path == "" {
		return errors.New("mcp tool policy path is empty")
	}
	p.normalize()

	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	return fsutil.WriteFileAtomic(path, data, 0o600)
}

// normalize 保证两层 map 非空，便于调用方直接写入。
func (p *MCPToolPolicy) normalize() {
	if p.Disabled == nil {
		p.Disabled = make(map[string]map[string]bool)
	}
	if p.AutoApprove == nil {
		p.AutoApprove = make(map[string]map[string]bool)
	}
}

// IsDisabled 判断指定服务的工具是否被禁用。
func (p MCPToolPolicy) IsDisabled(server, tool string) bool {
	return p.Disabled[server][tool]
}

// IsAutoApproved 判断指定服务的工具是否免审批。
func (p MCPToolPolicy) IsAutoApproved(server, tool string) bool {
	return p.AutoApprove[server][tool]
}

// SplitMCPToolName 解析 mcp__<server>__<tool> 形式的 MCP 工具名。
// 前缀后的第一段为服务名，其余部分为工具名（与界面展示的拆分规则一致）。
func SplitMCPToolName(name string) (server, tool string, ok bool) {
	rest, found := strings.CutPrefix(name, "mcp__")
	if !found {
		return "", "", false
	}
	server, tool, found = strings.Cut(rest, "__")
	if !found || server == "" || tool == "" {
		return "", "", false
	}
	return server, tool, true
}
