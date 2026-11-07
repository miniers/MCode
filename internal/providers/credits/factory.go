package credits

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// factoryLimitsURL 是 Droid CLI 的 `/limits` 读的端点。
const factoryLimitsURL = "https://api.factory.ai/api/billing/limits"

// FactoryClient 解析 Factory（Droid）的认证令牌，并读它经 Factory API 报出的方案额度。
//
// 令牌来源：settings 里的 Factory API key 以 FACTORY_API_KEY 环境变量交给 CLI；
// mcode 在环境变量缺失时额外兜底读一次
// `$HomeDir/.factory/auth.json`（本地登录的 Droid CLI 会把凭据写在那里），
// 这个文件回退是 mcode 自己的行为，本包不实现令牌刷新。
type FactoryClient struct {
	// Options 提供 APIKey、Env、HomeDir 与 HTTPClient。
	Options Options
}

// NewFactoryClient 构造一个 Factory 客户端。
func NewFactoryClient(opts Options) *FactoryClient {
	return &FactoryClient{Options: opts}
}

// Token 返回 Factory 认证令牌，Droid 与余额/额度读取共享同一凭证。
func (c *FactoryClient) Token(_ context.Context) (string, error) {
	opts := c.Options
	if key := strings.TrimSpace(opts.APIKey); key != "" {
		return key, nil
	}
	if key := strings.TrimSpace(opts.envValue("FACTORY_API_KEY")); key != "" {
		return key, nil
	}
	if key := factoryStoredToken(opts); key != "" {
		return key, nil
	}
	return "", fmt.Errorf("%w: droid 需要 Factory API key", ErrMissingKey)
}

// PlanWindows 读 Droid 经 Factory API 报出的方案额度窗口。
func (c *FactoryClient) PlanWindows(ctx context.Context) ([]Window, error) {
	return factoryPlanWindows(ctx, c.Options)
}

// factoryStoredToken 读本地登录的令牌文件（仅环境变量缺失时的兜底）。
func factoryStoredToken(opts Options) string {
	home := opts.home()
	if home == "" {
		return ""
	}
	for _, name := range []string{"auth.json", "credentials.json"} {
		data, err := os.ReadFile(filepath.Join(home, ".factory", name))
		if err != nil {
			continue
		}
		var root map[string]json.RawMessage
		if err := json.Unmarshal(data, &root); err != nil {
			continue
		}
		for _, key := range []string{"apiKey", "accessToken", "access_token", "token"} {
			if value := strings.TrimSpace(jsonText(root, key)); value != "" {
				return value
			}
		}
		// 有些写入把凭据放在 "auth" 子对象里。
		auth := jsonRawObject(root["auth"])
		for _, key := range []string{"apiKey", "accessToken", "access_token", "token"} {
			if value := strings.TrimSpace(jsonText(auth, key)); value != "" {
				return value
			}
		}
	}
	return ""
}

// factoryPlanWindows 读并解析 Factory 的额度端点。
func factoryPlanWindows(ctx context.Context, opts Options) ([]Window, error) {
	client := NewFactoryClient(opts)
	token, err := client.Token(ctx)
	if err != nil {
		return nil, err
	}
	// BaseURL 直接覆盖额度端点地址（Factory 只有一个额度端点）。
	url := opts.baseURL(factoryLimitsURL)
	status, body, err := getJSON(ctx, opts.client(), url, token)
	if err != nil {
		return nil, err
	}
	switch {
	case status == 401 || status == 403:
		return nil, fmt.Errorf("factory: 额度接口拒绝了 API key（HTTP %d）", status)
	case status != 200:
		return nil, fmt.Errorf("factory: 额度接口返回 HTTP %d", status)
	}
	return parseFactoryWindows(body, opts.now())
}

// parseFactoryWindows 解析额度响应：
// {"limits": {"standard": {"fiveHour": {...}, "weekly": {...}, "monthly": {...}}, "core": {...}},
// "extraUsageBalanceCents": N}
// standard 池总会显示；core 池只在有活跃窗口且用量大于 0 时才显示。
// 每个桶是 {"usedPercent": N, "windowEnd": "<ISO8601>"}；接口在窗口结束后仍报上一次的用量，
// CLI 把这种窗口当作已重置，因此这里也读成 0 且没有重置时间。
func parseFactoryWindows(raw []byte, now time.Time) ([]Window, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("factory: 解析额度: 响应为空")
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("factory: 解析额度: %w", err)
	}
	pools := jsonRawObject(root["limits"])
	standard := jsonRawObject(pools["standard"])
	if standard == nil {
		return nil, fmt.Errorf("factory: 解析额度: 响应缺少 limits.standard")
	}
	windows := factoryPoolWindows(standard, "standard", "Standard", now)
	if core := jsonRawObject(pools["core"]); core != nil {
		coreWindows := factoryPoolWindows(core, "core", "Droid Core", now)
		for _, window := range coreWindows {
			// Droid Core 池只在活跃窗口里已产生用量时才显示。
			if window.UsedPercent > 0 {
				windows = append(windows, coreWindows...)
				break
			}
		}
	}
	if len(windows) == 0 {
		return nil, nil
	}
	return windows, nil
}

// factoryPoolWindows 把一个池的三个桶读成窗口。
// key 是池名（standard / core），与标签前缀一起组成窗口 ID（如 `standard-5h`）。
func factoryPoolWindows(pool map[string]json.RawMessage, key, prefix string, now time.Time) []Window {
	var windows []Window
	for _, bucket := range []struct {
		key, label string
		minutes    int
	}{
		{"fiveHour", "5-hour limit", 300},
		{"weekly", "Weekly", 10_080},
		{"monthly", "Monthly", 43_200},
	} {
		entry := jsonRawObject(pool[bucket.key])
		if entry == nil {
			continue
		}
		used := jsonFloat(entry, "usedPercent")
		if used == nil {
			continue
		}
		end := parseISOTime(jsonText(entry, "windowEnd"))
		// 窗口已结束的读数按 0 处理。
		active := !end.IsZero() && end.After(now)
		percent := 0.0
		resetsAt := time.Time{}
		if active {
			percent = *used
			if percent < 0 {
				percent = 0
			}
			if percent > 100 {
				percent = 100
			}
			resetsAt = end
		}
		windows = append(windows, Window{
			ID:          key + "-" + bucket.key,
			Label:       prefix + " · " + bucket.label,
			Period:      windowPeriod(bucket.minutes),
			UsedPercent: percent,
			ResetsAt:    resetsAt,
		})
	}
	return windows
}
