package copilot

// 本文件读 Copilot CLI 的 account.getQuota：
// 把配额快照解析成各滚动窗口的已用百分比与重置时间（结果复用 credits.Window 展示子集）。

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"mcode/internal/providers/credits"
)

// PlanLimitsMethod 是 Copilot CLI 上报订阅配额所用的 RPC 方法名。
const PlanLimitsMethod = "account.getQuota"

// quotaWindow 是 readPlanLimits 里 titles 常量表的一项：快照键与展示标题。
type quotaWindow struct {
	key   string
	title string
}

// quotaWindows 是各滚动窗口的快照键与展示标题，顺序即展示顺序。
var quotaWindows = []quotaWindow{
	{"premium_interactions", "Premium requests"},
	{"chat", "Chat messages"},
	{"completions", "Code completions"},
}

// PlanLimits 是一次 Copilot 方案额度读取的结果，对应 PlanLimits 的展示子集。
type PlanLimits struct {
	// Plan 是订阅类型文案，形如 "Copilot Individual"；响应未给出时为空串。
	Plan string
	// Windows 是各滚动窗口，顺序即展示顺序；为空表示无可展示窗口。
	Windows []credits.Window
	// ReadAt 是本次读取的时间。
	ReadAt time.Time
}

// Caller 是调用 Copilot CLI RPC 的注入点，
// 签名对应 JSONRPCConnection.request(method, params)。
type Caller func(ctx context.Context, method string, params any) (json.RawMessage, error)

// ReadPlanLimits 以空参数调用 account.getQuota，
// 再把响应交给 ParsePlanLimits 解析。
func ReadPlanLimits(ctx context.Context, call Caller, now time.Time) (PlanLimits, error) {
	raw, err := call(ctx, PlanLimitsMethod, map[string]any{})
	if err != nil {
		return PlanLimits{}, err
	}
	return ParsePlanLimits(raw, now)
}

// ParsePlanLimits 按 readPlanLimits 的逻辑解析 account.getQuota 响应：对三个已知快照
// 逐一取值，跳过缺失、无限配额（isUnlimitedEntitlement）与非正配额的快照；百分比优先
// 取 100-remainingPercentage，缺该字段时用 usedRequests/entitlementRequests 推算，
// 并限制在 0–100。响应不是合法 JSON 时返回错误；窗口为空不报错。
func ParsePlanLimits(raw json.RawMessage, now time.Time) (PlanLimits, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return PlanLimits{}, fmt.Errorf("copilot: 解析 account.getQuota 响应: %w", err)
	}
	snapshots := planObject(root["quotaSnapshots"])
	windows := make([]credits.Window, 0, len(quotaWindows))
	for _, entry := range quotaWindows {
		snapshot := planObject(snapshots[entry.key])
		if snapshot == nil {
			continue
		}
		if unlimited, ok := planBool(snapshot["isUnlimitedEntitlement"]); ok && unlimited {
			continue
		}
		entitlement, ok := planDouble(snapshot["entitlementRequests"])
		if !ok || entitlement <= 0 {
			continue
		}
		percent := 0.0
		if remaining, ok := planDouble(snapshot["remainingPercentage"]); ok {
			percent = clampUsedPercent(100 - remaining)
		} else {
			used, _ := planDouble(snapshot["usedRequests"])
			percent = clampUsedPercent(used / entitlement * 100)
		}
		windows = append(windows, credits.Window{
			Label:       entry.title,
			UsedPercent: percent,
			ResetsAt:    parseResetDate(planText(snapshot["resetDate"])),
		})
	}
	return PlanLimits{
		Plan:    planLabel(planFromRaw(root)),
		Windows: windows,
		ReadAt:  now,
	}, nil
}

// planFromRaw 读取订阅类型。先从另一条 RPC（account.getCurrentAuth）的
// authInfo.copilotUser.copilot_plan 取值，这里同样优先该路径，并兼容它在顶层出现的形态。
func planFromRaw(root map[string]json.RawMessage) string {
	if plan := planText(root["copilot_plan"]); plan != "" {
		return plan
	}
	user := planObject(planObject(root["authInfo"])["copilotUser"])
	return planText(user["copilot_plan"])
}

// planLabel 拼装方案名：首字母大写并加
// "Copilot " 前缀（如 "individual" -> "Copilot Individual"）；空串返回空串。
func planLabel(raw string) string {
	if raw == "" {
		return ""
	}
	runes := []rune(raw)
	runes[0] = unicode.ToUpper(runes[0])
	return "Copilot " + string(runes)
}

// parseResetDate 先试带/不带小数秒的 ISO 8601，
// 再试纯日期（如 "2026-11-01"）；都不匹配时返回零值。
func parseResetDate(text string) time.Time {
	text = strings.TrimSpace(text)
	if text == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

// clampUsedPercent 把百分比限制在 0–100。
func clampUsedPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

// planObject 把一个 JSON 值解成对象；缺失、null 或类型不符时返回 nil。
func planObject(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

// planText 读取字符串值；缺失、null 或类型不符时返回空串。
func planText(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return ""
	}
	return text
}

// planDouble 读取数值，兼容数字与数字字符串；缺失或非法时 ok 为 false。
func planDouble(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err == nil {
		return value, true
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(text), 64); err == nil {
			return parsed, true
		}
	}
	return 0, false
}

// planBool 读取布尔值；缺失、null 或类型不符时 ok 为 false。
func planBool(raw json.RawMessage) (bool, bool) {
	if len(raw) == 0 {
		return false, false
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, false
	}
	return value, true
}
