package agentconf

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Options 包含扫描配置的选项。
// Home 为空时用 os.UserHomeDir()；ProjectDir 为空表示只扫全局；DataDir 指定 mcode 数据根目录。
type Options struct {
	Home       string
	ProjectDir string
	DataDir    string
	// WithCredentials 仅供运行时代理读取，设置页扫描不得开启。
	WithCredentials bool
}

// Skill 表示扫描到的 Skill 信息。
// Agent 取来源标识（如 "claude"、"codex"、"cursor"、"factory"、"antigravity"、"gemini"）；
// Scope 取 "global"、"project" 或 "external"；Path 为 SKILL.md 的绝对路径。
// Plugin 只在技能来自 Claude Code 插件时非空，填插件名，此时 Scope 为 "external"。
type Skill struct {
	Agent       string
	Scope       string
	Plugin      string
	Name        string
	Description string
	Path        string
	Enabled     bool
	Disabled    bool
}

// ID 返回技能的唯一标识符。
func (s Skill) ID() string {
	if s.Name != "" {
		return s.Name
	}
	return filepath.Base(filepath.Dir(s.Path))
}

// MCPServer 表示扫描到的 MCP 服务配置。
// Transport 取 "stdio"、"http"、"sse"；EnvKeys 只放排序后的环境变量名。
// Env、Headers 仅在 WithCredentials 模式下填充，禁止序列化到界面或日志。
// SecretEnvKeys 是调用方显式声明、必须交由凭据库托管的变量名（如目录预设的 DSN），
// 除它之外仍按变量名推断敏感项。
type MCPServer struct {
	Agent         string
	Scope         string
	Name          string
	Transport     string
	Command       string
	Args          []string
	URL           string
	EnvKeys       []string
	SecretEnvKeys []string `json:"-"`
	Source        string
	Env           map[string]string `json:"-"`
	Headers       map[string]string `json:"-"`
	Disabled      bool
	// Untrusted 表示服务来自当前仓库内的配置文件，用户确认信任前不得执行其命令。
	Untrusted bool `json:"untrusted,omitempty"`
	// ReadOnly 表示服务来自外部 Agent 配置（agy、pi），仅展示：不可编辑、不可停用、不注入会话。
	ReadOnly bool `json:"readOnly,omitempty"`
	// DisabledByEngine 表示该服务在其他引擎（Claude Code）的配置里已被关闭，仅做展示；
	// 与 Disabled 分开，mcode 是否注入仍由 mcode 自己的开关决定。
	DisabledByEngine bool `json:"disabledByEngine,omitempty"`
	// Auth 是目录条目声明的登录方式；nil 表示未声明，按服务器元数据判断。
	Auth *MCPCatalogAuth `json:"auth,omitempty"`
}

// AuthID 将凭据绑定到服务器名称及地址，避免同名的不同远程服务共用令牌。
func (s MCPServer) AuthID() string {
	return fmt.Sprintf("%s-%x", s.Name, sha256.Sum256([]byte(s.URL)))
}

// resolveOptions 解析 Options 中的默认路径。
func resolveOptions(opts Options) (home string, projectDir string, err error) {
	home = opts.Home
	if home == "" {
		home, err = os.UserHomeDir()
		if err != nil {
			return "", "", err
		}
	}
	if absHome, err := filepath.Abs(home); err == nil {
		home = absHome
	}

	projectDir = opts.ProjectDir
	if projectDir != "" {
		if absProj, err := filepath.Abs(projectDir); err == nil {
			projectDir = absProj
		}
	}
	return home, projectDir, nil
}

// resolveDataDir 解析 mcode 的数据目录，优先使用 opts.DataDir，其次 MCODE_DATA 环境变量，默认回退至 ~/.mcode。
func resolveDataDir(opts Options) string {
	if opts.DataDir != "" {
		if absData, err := filepath.Abs(opts.DataDir); err == nil {
			return absData
		}
		return opts.DataDir
	}
	if env := os.Getenv("MCODE_DATA"); env != "" {
		if absEnv, err := filepath.Abs(env); err == nil {
			return absEnv
		}
		return env
	}
	home, _, err := resolveOptions(opts)
	if err == nil && home != "" {
		return filepath.Join(home, ".mcode")
	}
	return ""
}

// sortSkills 按 Agent、Scope (global 在前)、Plugin、Name 稳定排序。
// 同一 Agent 同一 Scope 内先按插件名再按技能名，插件技能不会和同名普通技能交错。
func sortSkills(skills []Skill) {
	sort.SliceStable(skills, func(i, j int) bool {
		if skills[i].Agent != skills[j].Agent {
			return skills[i].Agent < skills[j].Agent
		}
		if skills[i].Scope != skills[j].Scope {
			return skills[i].Scope == "global"
		}
		if skills[i].Plugin != skills[j].Plugin {
			return skills[i].Plugin < skills[j].Plugin
		}
		return skills[i].Name < skills[j].Name
	})
}

// sortMCPServers 按 Agent、Scope (global 在前)、Name 排序。
func sortMCPServers(servers []MCPServer) {
	sort.Slice(servers, func(i, j int) bool {
		if servers[i].Agent != servers[j].Agent {
			return servers[i].Agent < servers[j].Agent
		}
		if servers[i].Scope != servers[j].Scope {
			return servers[i].Scope == "global"
		}
		return servers[i].Name < servers[j].Name
	})
}
