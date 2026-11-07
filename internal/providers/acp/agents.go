package acp

import (
	"os"

	"mcode/internal/harness"
)

// Agent 表示支持 ACP 协议的 Agent 种类。
type Agent string

const (
	AgentCline    Agent = "cline"
	AgentCursor   Agent = "cursor"
	AgentOpenCode Agent = "opencode"
	AgentGrok     Agent = "grok"
	AgentDevin    Agent = "devin"
	AgentDroid    Agent = "droid"
)

// Agents 返回所有已支持的 ACP Agent 列表。
func Agents() []Agent {
	return []Agent{
		AgentCline,
		AgentCursor,
		AgentOpenCode,
		AgentGrok,
		AgentDevin,
		AgentDroid,
	}
}

// DefaultBinary 返回 Agent 的默认可执行文件名。
// 规则：
// Cursor 为 "cursor-agent"，其余对应其自身标识。
func DefaultBinary(a Agent) string {
	switch a {
	case AgentCline:
		return "cline"
	case AgentCursor:
		return "cursor-agent"
	case AgentOpenCode:
		return "opencode"
	case AgentGrok:
		return "grok"
	case AgentDevin:
		return "devin"
	case AgentDroid:
		return "droid"
	default:
		return string(a)
	}
}

// DisplayName 返回 Agent 的人类可读显示名称。
func DisplayName(a Agent) string {
	switch a {
	case AgentCline:
		return "Cline"
	case AgentCursor:
		return "Cursor"
	case AgentOpenCode:
		return "OpenCode"
	case AgentGrok:
		return "Grok"
	case AgentDevin:
		return "Devin"
	case AgentDroid:
		return "Factory Droid"
	default:
		return string(a)
	}
}

// LaunchArgs 返回不同权限模式下各 ACP Agent 的启动命令行参数。
// 规则：
//   - readonly 模式按 supervised 处理；
//   - cline: ["--acp"]
//   - droid: ["exec", "--output-format", "acp"]
//   - cursor: auto -> ["--auto-review", "acp"], fullAccess -> ["--force", "acp"], 其他 -> ["acp"]
//   - grok:
//     supervised -> ["--permission-mode", "default", "agent", "stdio"]
//     auto -> ["--permission-mode", "auto", "agent", "stdio"]
//     fullAccess -> ["agent", "--always-approve", "stdio"]
//   - devin:
//     supervised -> ["--permission-mode", "auto", "acp"]
//     auto -> ["--permission-mode", "smart", "acp"]
//     fullAccess -> ["--permission-mode", "dangerous", "acp"]
//   - opencode 及默认: ["acp"]
func LaunchArgs(a Agent, mode harness.PermissionMode) []string {
	// readonly 模式按 supervised 处理
	if mode == harness.ModeReadOnly {
		mode = harness.ModeSupervised
	} else if mode == harness.ModeAutoAcceptEdits {
		mode = harness.ModeAuto
	}

	switch a {
	case AgentCline:
		return []string{"--acp"}

	case AgentDroid:
		return []string{"exec", "--output-format", "acp"}

	case AgentCursor:
		switch mode {
		case harness.ModeAuto:
			return []string{"--auto-review", "acp"}
		case harness.ModeFullAccess:
			return []string{"--force", "acp"}
		default:
			return []string{"acp"}
		}

	case AgentGrok:
		switch mode {
		case harness.ModeSupervised:
			return []string{"--permission-mode", "default", "agent", "stdio"}
		case harness.ModeAuto:
			return []string{"--permission-mode", "auto", "agent", "stdio"}
		case harness.ModeFullAccess:
			return []string{"agent", "--always-approve", "stdio"}
		default:
			return []string{"--permission-mode", "default", "agent", "stdio"}
		}

	case AgentDevin:
		switch mode {
		case harness.ModeSupervised:
			return []string{"--permission-mode", "auto", "acp"}
		case harness.ModeAuto:
			return []string{"--permission-mode", "smart", "acp"}
		case harness.ModeFullAccess:
			return []string{"--permission-mode", "dangerous", "acp"}
		default:
			return []string{"--permission-mode", "auto", "acp"}
		}

	case AgentOpenCode:
		fallthrough
	default:
		return []string{"acp"}
	}
}

// ExtraEnv 返回对应 Agent 所需的额外预设环境变量：
// - Cline 默认通过 CLINE_PROVIDER 指定上游提供方
func ExtraEnv(a Agent) []string {
	switch a {
	case AgentCline:
		// 已有 CLINE_PROVIDER（进程环境）时不覆盖；否则取 providers.json 里生效的提供方，
		// 让 cline-pass 账号只拿到订阅模型，而不是整个 openrouter 目录。
		if os.Getenv("CLINE_PROVIDER") != "" {
			return nil
		}
		if id := ClineProviderID(); id != "" {
			return []string{"CLINE_PROVIDER=" + id}
		}
		return nil
	default:
		return nil
	}
}

// Capabilities 描述 ACP Agent 暴露的交互能力。
type Capabilities struct {
	SupportsPlanMode  bool
	SupportsSubagents bool
}

// AgentCapabilities 返回对应 Agent 的能力标记：
//   - droid: plan mode=true, subagents=true
//   - cursor: plan mode=true, subagents=false
//   - cline: plan mode=true, subagents=false
//   - opencode/opencodeGo: plan mode=true, subagents=true
//   - grok: plan mode=true, subagents=false
//   - devin: plan mode=true, subagents=false
func AgentCapabilities(a Agent) Capabilities {
	switch a {
	case AgentDroid:
		return Capabilities{SupportsPlanMode: true, SupportsSubagents: true}
	case AgentOpenCode:
		return Capabilities{SupportsPlanMode: true, SupportsSubagents: true}
	case AgentCursor:
		return Capabilities{SupportsPlanMode: true, SupportsSubagents: false}
	case AgentCline:
		return Capabilities{SupportsPlanMode: true, SupportsSubagents: false}
	case AgentGrok:
		return Capabilities{SupportsPlanMode: true, SupportsSubagents: false}
	case AgentDevin:
		return Capabilities{SupportsPlanMode: true, SupportsSubagents: false}
	default:
		return Capabilities{}
	}
}
