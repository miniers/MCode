package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RateLimits 包含 Codex 账号的速率限制窗口及计划类型等信息。
type RateLimits struct {
	PlanType string
	Windows  []RateLimitWindow
	// Source 是额度实际来源：QuotaSourceOpenAI 为直连的 ChatGPT 登录，
	// QuotaSourceMagpie 为 Magpie 网关缓存；为空表示来源尚未确认。
	Source string
	// Accounts 是 Magpie 来源时各账号卡片的额度；直连来源时为空。
	Accounts []AccountQuota
	// ReadAt 是数据在来源端的读取时间：直连为本次读取时间，Magpie 取卡片自带的
	// readAt，未知时为零值（不能当作当前时间）。
	ReadAt time.Time
	// GatewayBaseURL 是 Magpie 网关根地址（不含 /v1），仅 Source 为 magpie 时有值。
	GatewayBaseURL string
	// GatewayToken 是读取该网关额度用的密钥，仅 Source 为 magpie 时有值；
	// 不打印、不持久化，序列化时一律忽略。
	GatewayToken string `json:"-"`
}

// RateLimitWindow 表示单个限流窗口。
type RateLimitWindow struct {
	ID                 string
	Title              string
	Period             string
	UsedPercent        float64
	WindowDurationMins int
	ResetsAt           *time.Time
}

// ParseRateLimitsResponse 解析 account/rateLimits/read 的 JSON-RPC 结果。
// 支持 rateLimitsByLimitId 字典与单个 rateLimits 对象，
// 提取 primary 和 secondary 窗口的用量百分比、窗口时长和重置时间。
func ParseRateLimitsResponse(raw json.RawMessage) (RateLimits, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return RateLimits{}, nil
	}

	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return RateLimits{}, fmt.Errorf("codex: unmarshal rate limits: %w", err)
	}

	var snapshots []map[string]any
	if byID, ok := root["rateLimitsByLimitId"].(map[string]any); ok && len(byID) > 0 {
		keys := make([]string, 0, len(byID))
		for k := range byID {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if snap, ok := byID[k].(map[string]any); ok {
				snapshots = append(snapshots, snap)
			}
		}
	} else if byID, ok := root["rate_limits_by_limit_id"].(map[string]any); ok && len(byID) > 0 {
		keys := make([]string, 0, len(byID))
		for k := range byID {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if snap, ok := byID[k].(map[string]any); ok {
				snapshots = append(snapshots, snap)
			}
		}
	} else if snap, ok := root["rateLimits"].(map[string]any); ok && len(snap) > 0 {
		snapshots = []map[string]any{snap}
	} else if snap, ok := root["rate_limits"].(map[string]any); ok && len(snap) > 0 {
		snapshots = []map[string]any{snap}
	}

	var rawPlan string
	var windows []RateLimitWindow

	for _, snap := range snapshots {
		if rawPlan == "" {
			rawPlan = getStringField(snap, "planType", "plan_type")
		}
		var limitName string
		if len(snapshots) > 1 {
			limitName = getStringField(snap, "limitName", "limit_name")
		}
		limitID := getStringField(snap, "limitId", "limit_id")
		if limitID == "" {
			limitID = "codex"
		}

		for _, key := range []string{"primary", "secondary"} {
			winRaw, ok := snap[key].(map[string]any)
			if !ok {
				continue
			}

			percent, ok := getFloatField(winRaw, "usedPercent", "used_percent")
			if !ok {
				continue
			}

			durationMins := getIntField(winRaw, "windowDurationMins", "window_duration_mins", "windowDurationMinutes")
			title := formatWindowTitle(durationMins, limitName)
			period := formatWindowPeriod(durationMins)

			var resetsAt *time.Time
			if r := parseResetTime(winRaw["resetsAt"]); r != nil {
				resetsAt = r
			} else if r := parseResetTime(winRaw["resets_at"]); r != nil {
				resetsAt = r
			}

			windows = append(windows, RateLimitWindow{
				ID:                 fmt.Sprintf("%s-%s", limitID, key),
				Title:              title,
				Period:             period,
				UsedPercent:        percent,
				WindowDurationMins: durationMins,
				ResetsAt:           resetsAt,
			})
		}
	}

	return RateLimits{
		PlanType: FormatCodexPlanName(rawPlan),
		Windows:  windows,
	}, nil
}

func cloneRateLimits(src RateLimits) RateLimits {
	res := RateLimits{
		PlanType:       src.PlanType,
		Source:         src.Source,
		ReadAt:         src.ReadAt,
		GatewayBaseURL: src.GatewayBaseURL,
		GatewayToken:   src.GatewayToken,
		Windows:        cloneRateLimitWindows(src.Windows),
	}
	if len(src.Accounts) > 0 {
		res.Accounts = make([]AccountQuota, len(src.Accounts))
		for i, acct := range src.Accounts {
			res.Accounts[i] = acct
			res.Accounts[i].Windows = cloneRateLimitWindows(acct.Windows)
		}
	}
	return res
}

// cloneRateLimitWindows 复制窗口列表，重置时间指针各自独立。
func cloneRateLimitWindows(windows []RateLimitWindow) []RateLimitWindow {
	out := make([]RateLimitWindow, len(windows))
	for i, w := range windows {
		out[i] = w
		if w.ResetsAt != nil {
			tCopy := *w.ResetsAt
			out[i].ResetsAt = &tCopy
		}
	}
	return out
}

func getFloatField(m map[string]any, keys ...string) (float64, bool) {
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		switch val := v.(type) {
		case float64:
			return val, true
		case int:
			return float64(val), true
		case int64:
			return float64(val), true
		case json.Number:
			if f, err := val.Float64(); err == nil {
				return f, true
			}
		case string:
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				return f, true
			}
		}
	}
	return 0, false
}

func getIntField(m map[string]any, keys ...string) int {
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		switch val := v.(type) {
		case int:
			return val
		case float64:
			return int(val)
		case int64:
			return int(val)
		case json.Number:
			if i, err := val.Int64(); err == nil {
				return int(i)
			}
		case string:
			if i, err := strconv.Atoi(val); err == nil {
				return i
			}
		}
	}
	return 0
}

func parseResetTime(v any) *time.Time {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case float64:
		if val <= 0 {
			return nil
		}
		if val > 1e10 {
			t := time.UnixMilli(int64(val)).UTC()
			return &t
		}
		sec := int64(val)
		nsec := int64((val - float64(sec)) * 1e9)
		t := time.Unix(sec, nsec).UTC()
		return &t
	case int64:
		if val <= 0 {
			return nil
		}
		if val > 1e10 {
			t := time.UnixMilli(val).UTC()
			return &t
		}
		t := time.Unix(val, 0).UTC()
		return &t
	case int:
		val64 := int64(val)
		if val64 <= 0 {
			return nil
		}
		if val64 > 1e10 {
			t := time.UnixMilli(val64).UTC()
			return &t
		}
		t := time.Unix(val64, 0).UTC()
		return &t
	case json.Number:
		if f, err := val.Float64(); err == nil {
			return parseResetTime(f)
		}
	case string:
		val = strings.TrimSpace(val)
		if val == "" {
			return nil
		}
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return parseResetTime(f)
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z07:00"} {
			if t, err := time.Parse(layout, val); err == nil {
				ut := t.UTC()
				return &ut
			}
		}
	}
	return nil
}

func formatWindowTitle(mins int, name string) string {
	var title string
	switch mins {
	case 300:
		title = "5-hour limit"
	case 1440:
		title = "Daily"
	case 10080:
		title = "Weekly"
	default:
		if mins >= 40320 && mins <= 44640 {
			title = "Monthly"
		} else if mins > 0 && mins%1440 == 0 {
			title = fmt.Sprintf("%d-day limit", mins/1440)
		} else if mins > 0 {
			title = fmt.Sprintf("%d-hour limit", max(1, mins/60))
		} else {
			title = "Usage limit"
		}
	}
	if name != "" {
		title += " · " + name
	}
	return title
}

func formatWindowPeriod(mins int) string {
	switch mins {
	case 300:
		return "5h"
	case 1440:
		return "1d"
	case 10080:
		return "7d"
	default:
		if mins >= 40320 && mins <= 44640 {
			return "30d"
		}
		if mins > 0 && mins%1440 == 0 {
			return fmt.Sprintf("%dd", mins/1440)
		}
		if mins > 0 && mins%60 == 0 {
			return fmt.Sprintf("%dh", mins/60)
		}
		return ""
	}
}

// FormatCodexPlanName 将 Codex 的计划标识格式化为用户界面展示名称。
func FormatCodexPlanName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	switch strings.ToLower(raw) {
	case "pro":
		return "Pro 20x"
	case "prolite", "pro lite":
		return "Pro 5x"
	case "ent26", "hc":
		return "Enterprise"
	case "self_serve_business_prolite":
		return "Business Premium"
	case "self_serve_business_usage_based":
		return "Business"
	case "enterprise_cbp_automation":
		return "Enterprise (Automation)"
	case "enterprise_cbp_usage_based":
		return "Enterprise"
	case "education":
		return "Edu"
	case "edu_plus":
		return "Edu Plus"
	case "edu_pro":
		return "Edu Pro"
	default:
		return strings.ToUpper(raw[:1]) + raw[1:]
	}
}
