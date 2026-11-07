package credits

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// zaiQuotaURL 是 Coding Plan 额度端点，在 monitor 路径下，
// 与聊天、模型目录用的 baseURL（https://api.z.ai/api/coding/paas/v4）不是同一条。
const zaiQuotaURL = "https://api.z.ai/api/monitor/usage/quota/limit"

// zaiPlanWindows 读 z.ai Coding Plan 的滚动窗口（5 小时 / 每周 / MCP 工具月度）。
// 未实现重置卡列表（biz/customer-package-reset/list）的读取，见 README 说明。
func zaiPlanWindows(ctx context.Context, opts Options) ([]Window, error) {
	key := strings.TrimSpace(opts.APIKey)
	if key == "" {
		return nil, fmt.Errorf("%w: zai 需要 API key", ErrMissingKey)
	}
	url := zaiQuotaURL
	// z.ai 的额度端点在 monitor 路径下（与 Coding Plan 的 API 根不同主机路径），
	// 因此 BaseURL 直接覆盖额度端点地址。
	if trimmed := strings.TrimSpace(opts.BaseURL); trimmed != "" {
		url = trimmed
	}
	status, body, err := getJSON(ctx, opts.client(), url, key)
	if err != nil {
		return nil, err
	}
	switch {
	case status == 401 || status == 403:
		return nil, fmt.Errorf("zai: 额度接口拒绝了 API key（HTTP %d）", status)
	case status != 200:
		return nil, fmt.Errorf("zai: 额度接口返回 HTTP %d", status)
	}
	return parseZaiWindows(body)
}

// parseZaiWindows 解析额度响应：
// {"code": 200, "data": {"level": "...", "limits": [{"type": "CREDIT_LIMIT", "usage": N,
// "currentValue": N, "percentage": N, "nextResetTime": <epoch ms>, "unit": 3|4|5|6, "number": N}]}}
// percentage 缺失时用 currentValue/usage 计算，最终夹到 0–100。
func parseZaiWindows(raw []byte) ([]Window, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("zai: 解析额度: 响应为空")
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("zai: 解析额度: %w", err)
	}
	ok := false
	if code := jsonFloat(root, "code"); code != nil && int(*code) == 200 {
		ok = true
	}
	if success := jsonBool(root, "success"); success != nil && *success {
		ok = true
	}
	if !ok {
		return nil, fmt.Errorf("zai: 额度接口返回错误响应")
	}
	data := jsonRawObject(root["data"])
	if data == nil {
		return nil, fmt.Errorf("zai: 解析额度: 响应缺少 data")
	}

	// limits 是数组，先解成元素列表。
	if limitsRaw := data["limits"]; len(limitsRaw) == 0 || string(limitsRaw) == "null" {
		return nil, nil
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(data["limits"], &entries); err != nil {
		return nil, fmt.Errorf("zai: 解析额度: %w", err)
	}

	var windows []Window
	creditIndex := 0
	toolsIndex := 0
	for _, entry := range entries {
		percent := 0.0
		if reported := jsonFloat(entry, "percentage"); reported != nil {
			percent = *reported
		} else if usage := jsonFloat(entry, "usage"); usage != nil && *usage > 0 {
			current := jsonFloat(entry, "currentValue")
			if current == nil {
				current = new(float64)
			}
			percent = *current / *usage * 100
		}
		percent = math.Min(math.Max(percent, 0), 100)
		var resetsAt time.Time
		if reset := jsonFloat(entry, "nextResetTime"); reset != nil {
			resetsAt = time.UnixMilli(int64(*reset)).UTC()
		}

		switch jsonText(entry, "type") {
		case "CREDIT_LIMIT", "TOKENS_LIMIT":
			// 订阅额度窗口：ID 用 credit-N（N 为序号），
			// 周期能从 number+unit 换算出来时才给 Period，否则留空由标题推断。
			label := ""
			period := ""
			if minutes := zaiWindowMinutes(entry); minutes != nil {
				label = windowTitle(*minutes)
				period = windowPeriod(*minutes)
			} else if creditIndex == 0 {
				label, period = "5-hour limit", "5h"
			} else {
				label, period = "Weekly", "7d"
			}
			windows = append(windows, Window{
				ID:          fmt.Sprintf("credit-%d", creditIndex),
				Label:       label,
				Period:      period,
				UsedPercent: percent,
				ResetsAt:    resetsAt,
			})
			creditIndex++
		case "TIME_LIMIT":
			// MCP tools 月度配额是辅助窗口（ID tools-N），额度条不把它当订阅额度展示。
			windows = append(windows, Window{
				ID:          fmt.Sprintf("tools-%d", toolsIndex),
				Label:       "Monthly · MCP tools",
				Period:      "30d",
				UsedPercent: percent,
				ResetsAt:    resetsAt,
			})
			toolsIndex++
		default:
			continue
		}
	}
	return windows, nil
}

// zaiWindowMinutes 按接口的 unit 取值把 number 换算成分钟数：
// 3 → 小时、4 → 天、5 → 30 天、6 → 周；number 为 0 或 unit 未知时返回 nil。
func zaiWindowMinutes(entry map[string]json.RawMessage) *int {
	number := jsonFloat(entry, "number")
	if number == nil || *number <= 0 {
		return nil
	}
	unit := jsonFloat(entry, "unit")
	if unit == nil {
		return nil
	}
	var minutes int
	switch int(*unit) {
	case 3:
		minutes = int(*number * 60)
	case 4:
		minutes = int(*number * 1_440)
	case 6:
		minutes = int(*number * 10_080)
	case 5:
		minutes = int(*number * 43_200)
	default:
		return nil
	}
	return &minutes
}

// windowTitle 把分钟数转成窗口标题。
func windowTitle(minutes int) string {
	switch minutes {
	case 300:
		return "5-hour limit"
	case 1_440:
		return "Daily"
	case 10_080:
		return "Weekly"
	}
	if minutes >= 40_320 && minutes <= 44_640 {
		return "Monthly"
	}
	if minutes%1_440 == 0 {
		return fmt.Sprintf("%d-day limit", minutes/1_440)
	}
	return fmt.Sprintf("%d-hour limit", minutes/60)
}
