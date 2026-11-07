package harness

import (
	"context"
	"fmt"
	"strings"
)

// Decision 表示权限判决结果。
type Decision int

const (
	DecisionAllow Decision = iota + 1
	DecisionDeny
	DecisionAsk
)

func (d Decision) String() string {
	switch d {
	case DecisionAllow:
		return "allow"
	case DecisionDeny:
		return "deny"
	case DecisionAsk:
		return "ask"
	default:
		return fmt.Sprintf("Decision(%d)", d)
	}
}

// Rule 定义针对工具名称与主参数模式的权限规则。
type Rule struct {
	Tool     string
	Pattern  string
	Decision Decision
}

// matchPattern 使用双指针回溯算法匹配通配符：
// '*' 匹配任意长度字符（含 '/' 与空格），'?' 匹配单个字符（按 rune 计），其余字符字面匹配。
func matchPattern(pattern, target string) bool {
	if pattern == target || pattern == "*" {
		return true
	}
	p := []rune(pattern)
	t := []rune(target)

	pi, ti := 0, 0
	starIdx := -1
	matchIdx := 0

	for ti < len(t) {
		if pi < len(p) && (p[pi] == '?' || p[pi] == t[ti]) {
			pi++
			ti++
		} else if pi < len(p) && p[pi] == '*' {
			starIdx = pi
			matchIdx = ti
			pi++
		} else if starIdx != -1 {
			matchIdx++
			ti = matchIdx
			pi = starIdx + 1
		} else {
			return false
		}
	}

	for pi < len(p) && p[pi] == '*' {
		pi++
	}

	return pi == len(p)
}

// MCPToolAllower 由审批方可选实现：对 MCP 工具给出「在权限模式之外直接放行」的结论。
//
// 只有掌握会话身份的审批方（例如总管的会话的审批方）才需要实现它：总管在只读档下要靠
// 内置 MCP 服务器上的工具分诊，而内置服务器自己会按白名单拦截越权调用。未实现该接口的
// 审批方行为完全不变，调用方继续走 Decide 的常规判定。
type MCPToolAllower interface {
	MCPToolAllowed(ctx context.Context, toolName string) bool
}

// MCPToolAllowed 询问审批方是否对这次 MCP 工具调用直接放行。approver 未实现 MCPToolAllower
// （含 nil）时返回 false，调用方保持原有判定。
func MCPToolAllowed(ctx context.Context, approver Approver, toolName string) bool {
	allower, ok := approver.(MCPToolAllower)
	if !ok || allower == nil {
		return false
	}
	return allower.MCPToolAllowed(ctx, toolName)
}

// onlyReadsCommand 只放行可直接识别的单条只读命令，复杂 shell 表达式交给审批。
func onlyReadsCommand(command string) bool {
	if strings.ContainsAny(command, "\r\n;&|<>`$\\\"'") {
		return false
	}
	tokens := strings.Fields(command)
	if len(tokens) == 0 {
		return false
	}
	if tokens[0] == "git" {
		if len(tokens) < 2 {
			return false
		}
		switch tokens[1] {
		case "status", "log", "diff", "show", "blame", "ls-files":
			for _, arg := range tokens[2:] {
				if strings.HasPrefix(arg, "-") {
					return false
				}
			}
			return true
		case "branch":
			return len(tokens) == 2
		default:
			return false
		}
	}
	for _, arg := range tokens[1:] {
		if strings.HasPrefix(arg, "-") {
			return false
		}
	}
	switch tokens[0] {
	case "ls", "cat", "head", "tail", "wc", "grep", "rg", "find", "tree", "du", "stat", "file", "pwd", "echo", "which", "sort", "uniq", "diff", "cd":
		return true
	default:
		return false
	}
}

// Decide 根据权限模式、预设规则列表、工具属性裁决是否放行、拒绝或请求审批。
func Decide(mode PermissionMode, rules []Rule, toolName string, arg string, readOnlyTool bool) Decision {
	// ModeReadOnly 下对非只读工具始终拒绝，优先于任何规则或模式默认设置
	if mode == ModeReadOnly && !readOnlyTool {
		return DecisionDeny
	}

	var (
		hasDeny  bool
		hasAsk   bool
		hasAllow bool
		matched  bool
	)

	for _, r := range rules {
		if !matchPattern(r.Tool, toolName) {
			continue
		}
		// Pattern 为空表示匹配该工具的任意参数
		if r.Pattern != "" && !matchPattern(r.Pattern, arg) {
			continue
		}

		matched = true
		switch r.Decision {
		case DecisionDeny:
			hasDeny = true
		case DecisionAsk:
			hasAsk = true
		case DecisionAllow:
			hasAllow = true
		}
	}

	// 1) 有匹配规则时，按 Deny > Ask > Allow 取最严格的判定
	if matched {
		if hasDeny {
			return DecisionDeny
		}
		if hasAsk {
			return DecisionAsk
		}
		if hasAllow {
			return DecisionAllow
		}
	}

	// 2) 无匹配规则时的默认策略
	switch mode {
	case ModeFullAccess:
		return DecisionAllow
	case ModeReadOnly:
		if readOnlyTool {
			return DecisionAllow
		}
		return DecisionDeny
	case ModeSupervised:
		if readOnlyTool {
			return DecisionAllow
		}
		return DecisionAsk
	case ModeAutoAcceptEdits, ModeAuto:
		if readOnlyTool || toolName == "write_file" || toolName == "edit_file" ||
			((toolName == "bash" || toolName == "shell_command" || toolName == "run_command") && onlyReadsCommand(arg)) {
			return DecisionAllow
		}
		return DecisionAsk
	default:
		if readOnlyTool {
			return DecisionAllow
		}
		return DecisionAsk
	}
}
