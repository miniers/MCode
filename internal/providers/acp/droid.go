package acp

import (
	"encoding/json"
	"strings"

	"mcode/internal/harness"
)

// Droid 会话模式 ID 与计划退出的审批选项 ID，取值沿用 Factory Droid 自己的 ACP 实现。
const (
	droidSpecMode       = "spec"
	droidNormalMode     = "normal"
	droidModeAutoLow    = "auto-low"
	droidModeAutoMedium = "auto-medium"
	droidModeAutoHigh   = "auto-high"

	droidProceedAutoHigh = "proceed_auto_run_high"
	droidProceedAutoMid  = "proceed_auto_run_medium"
	droidProceedAutoLow  = "proceed_auto_run_low"
	droidProceedOnce     = "proceed_once"
	droidCancelOption    = "cancel"
)

// droidAPIKeyEnv 是 Factory Droid 用于 API Key 认证的环境变量名。
const droidAPIKeyEnv = "FACTORY_API_KEY"

// DroidAuthMode 表示 Factory Droid 的认证方式。
type DroidAuthMode string

// 取值：
//   - apiKey：使用设置中保存的 API Key；
//   - browser：沿用 Droid 自带的浏览器登录。
const (
	DroidAuthAPIKey DroidAuthMode = "apiKey"
	DroidAuthWeb    DroidAuthMode = "browser"
)

// ParseDroidAuthMode 解析认证模式字符串。
// 未知值返回默认值 apiKey。
func ParseDroidAuthMode(s string) DroidAuthMode {
	switch DroidAuthMode(s) {
	case DroidAuthAPIKey:
		return DroidAuthAPIKey
	case DroidAuthWeb:
		return DroidAuthWeb
	default:
		return DroidAuthAPIKey
	}
}

// DroidAuthEnv 基于 base 环境变量计算 Droid 认证所需的环境变量。
// 复制 base，不修改原切片；先移除其中所有 FACTORY_API_KEY= 开头的项，
// 当 mode 为 API Key 且 apiKey 去掉首尾空白后非空时再追加。
func DroidAuthEnv(base []string, mode DroidAuthMode, apiKey string) []string {
	out := make([]string, 0, len(base)+1)
	for _, item := range base {
		if strings.HasPrefix(item, droidAPIKeyEnv+"=") {
			continue
		}
		out = append(out, item)
	}

	if mode == DroidAuthAPIKey {
		if key := strings.TrimSpace(apiKey); key != "" {
			out = append(out, droidAPIKeyEnv+"="+key)
		}
	}

	return out
}

// DroidRequestedMode 返回本轮应切换到的 Droid 会话模式 ID，空串表示不设置。
// 计划模式优先 spec、其次 normal，两者都未广告时不切换；非计划模式按运行时权限
// 映射到 Droid 的自动执行档位。readonly 在 Droid 没有对应档位，按不切换处理。
func DroidRequestedMode(interactionMode, runtimeMode string, available []string) string {
	has := func(id string) bool {
		for _, mode := range available {
			if mode == id {
				return true
			}
		}
		return false
	}

	if interactionMode == "plan" {
		if has(droidSpecMode) {
			return droidSpecMode
		}
		if has(droidNormalMode) {
			return droidNormalMode
		}
		return ""
	}

	switch harness.PermissionMode(runtimeMode) {
	case harness.ModeSupervised:
		return droidNormalMode
	case harness.ModeAutoAcceptEdits:
		return droidModeAutoLow
	case harness.ModeAuto:
		return droidModeAutoMedium
	case harness.ModeFullAccess:
		return droidModeAutoHigh
	default:
		return ""
	}
}

// DroidPlanExitApprovalID 返回计划退出时“执行计划”应选中的 optionId：
// fullAccess 选 proceed_auto_run_high，auto 选
// proceed_auto_run_medium，autoAcceptEdits 选 proceed_auto_run_low，选项缺失或
// supervised 时回落到 proceed_once。
func DroidPlanExitApprovalID(runtimeMode string, optionIDs []string) string {
	has := func(id string) bool {
		for _, optionID := range optionIDs {
			if optionID == id {
				return true
			}
		}
		return false
	}

	switch harness.PermissionMode(runtimeMode) {
	case harness.ModeFullAccess:
		if has(droidProceedAutoHigh) {
			return droidProceedAutoHigh
		}
	case harness.ModeAuto:
		if has(droidProceedAutoMid) {
			return droidProceedAutoMid
		}
	case harness.ModeAutoAcceptEdits:
		if has(droidProceedAutoLow) {
			return droidProceedAutoLow
		}
	}

	return droidProceedOnce
}

// DroidIsPlanExit 判断 session/request_permission 是否为 Droid 的计划退出。
// 工具调用命中 isPlanExitCall，或某个审批选项名称包含 spec。
func DroidIsPlanExit(title, name string, optionNames []string, rawInput json.RawMessage) bool {
	if DroidPlanExitToolCall(title, name, rawInput) {
		return true
	}
	for _, optionName := range optionNames {
		if strings.Contains(strings.ToLower(optionName), "spec") {
			return true
		}
	}
	return false
}

// DroidPlanExitToolCall 判断单个工具调用是否为 Droid 的计划退出。
// 标题为 "Approve Spec"、工具名为 ExitSpecMode/exit_spec_mode，
// 或 rawInput 同时带 plan 与 title 字段。
func DroidPlanExitToolCall(title, name string, rawInput json.RawMessage) bool {
	if title == "Approve Spec" {
		return true
	}
	switch name {
	case "ExitSpecMode", "exit_spec_mode":
		return true
	}
	if len(rawInput) == 0 {
		return false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rawInput, &fields); err != nil {
		return false
	}
	_, hasPlan := fields["plan"]
	_, hasTitle := fields["title"]
	return hasPlan && hasTitle
}

// DroidPlanMarkdown 从计划退出的 rawInput 里取出计划正文，取不到返回空串。
// 按键名优先级依次尝试。
func DroidPlanMarkdown(rawInput json.RawMessage) string {
	if len(rawInput) == 0 {
		return ""
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rawInput, &fields); err != nil {
		return ""
	}
	for _, key := range []string{"plan", "planContent", "markdown", "content", "spec"} {
		raw, ok := fields[key]
		if !ok {
			continue
		}
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			continue
		}
		if text = strings.TrimSpace(text); text != "" {
			return text
		}
	}
	return ""
}
