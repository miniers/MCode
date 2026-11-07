package commandcode

// 本文件读 `/alpha/billing` 路由的订阅滚动用量窗口，结果复用 credits.Window 这份额度契约。
// 余额（/alpha/billing/credits 的 credits 字段）已由 internal/providers/credits 提供，
// 这里不重复实现。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mcode/internal/providers/credits"
)

// commandCodeDefaultBaseURL 是 CLI 的 API 基地址。
const commandCodeDefaultBaseURL = "https://api.commandcode.ai"

// commandCodePlanLimitsPath 是 CLI 自己 /usage 读的用量路由。
const commandCodePlanLimitsPath = "/alpha/billing/credits"

// commandCodeLimitsTimeout 是一次用量读取的超时（15 秒）。
const commandCodeLimitsTimeout = 15 * time.Second

// PlanLimitsOptions 是一次方案额度读取的依赖与来源配置，测试时全部可替换。
type PlanLimitsOptions struct {
	// APIKey 是调用方直接给出的密钥，优先于环境变量与本地登录文件。
	APIKey string
	// BaseURL 覆盖默认端点地址（https://api.commandcode.ai）。
	BaseURL string
	// HTTPClient 为空时使用带 15 秒超时的默认 client。
	HTTPClient *http.Client
	// Env 是环境变量来源；为 nil 时读进程环境，非 nil 时只查这张表（测试用）。
	Env map[string]string
	// HomeDir 覆盖用户主目录；为空时用 env["HOME"] 或 os.UserHomeDir()。
	HomeDir string
}

func (o PlanLimitsOptions) client() *http.Client {
	if o.HTTPClient != nil {
		return o.HTTPClient
	}
	return &http.Client{Timeout: commandCodeLimitsTimeout}
}

func (o PlanLimitsOptions) baseURL() string {
	if base := strings.TrimSpace(o.BaseURL); base != "" {
		return strings.TrimRight(base, "/")
	}
	return commandCodeDefaultBaseURL
}

// envValue 读取环境变量：Options.Env 非 nil 时只查它，否则查进程环境。
func (o PlanLimitsOptions) envValue(key string) string {
	if o.Env != nil {
		return o.Env[key]
	}
	return os.Getenv(key)
}

// home 解析用户主目录：HomeDir、env["HOME"]、os.UserHomeDir() 依次兜底。
func (o PlanLimitsOptions) home() string {
	if home := strings.TrimSpace(o.HomeDir); home != "" {
		return home
	}
	if home := strings.TrimSpace(o.envValue("HOME")); home != "" {
		return home
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// apiKey 按 CLI 的顺序解析密钥：
// APIKey 参数、COMMAND_CODE_API_KEY 环境变量、最后是 `~/.commandcode/auth.json`
// （`cmd login` 写入的 apiKey）。与 internal/providers/credits 的
// commandCodeAPIKey / commandCodeStoredKey 同一顺序，这里是本包可注入的版本。
func (o PlanLimitsOptions) apiKey() string {
	if key := strings.TrimSpace(o.APIKey); key != "" {
		return key
	}
	if key := strings.TrimSpace(o.envValue("COMMAND_CODE_API_KEY")); key != "" {
		return key
	}
	return o.storedKey()
}

// storedKey 读 `~/.commandcode/auth.json` 里的 apiKey；读不到时返回空串。
func (o PlanLimitsOptions) storedKey() string {
	home := o.home()
	if home == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, ".commandcode", "auth.json"))
	if err != nil {
		return ""
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return ""
	}
	return strings.TrimSpace(jsonText(root["apiKey"]))
}

// PlanLimits 读取订阅的滚动用量窗口：
// GET /alpha/billing/credits 的 windowLimits 给出 fiveHour 与 weekly 两个窗口，
// cap 与 used 都是美元（订阅按金额限流，不按请求数），resetAt 是 epoch 毫秒，
// 0 表示窗口尚未开始。
//
// 没有可用的密钥、或没有任何带 cap 的窗口时返回 (nil, nil)；接口返回非 200 时返回错误。
// 窗口标题带上 cap（如 "5-hour limit · $3.00"）。方案名（/alpha/billing/subscriptions 的
// data.planId）不在 credits.Window 契约内，不在此包上报，见 README.md。
func PlanLimits(ctx context.Context, opts PlanLimitsOptions) ([]credits.Window, error) {
	key := opts.apiKey()
	if key == "" {
		return nil, nil
	}
	body, err := getCommandCodeJSON(ctx, opts, commandCodePlanLimitsPath, key)
	if err != nil {
		return nil, err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("commandcode: 解析用量响应: %w", err)
	}
	windows := parseCommandCodeWindows(root)
	if len(windows) == 0 {
		return nil, nil
	}
	return windows, nil
}

// commandCodeWindowKeys 是展示的两个用量窗口，顺序即展示顺序。
var commandCodeWindowKeys = []struct{ key, title string }{
	{"fiveHour", "5-hour limit"},
	{"weekly", "Weekly"},
}

// parseCommandCodeWindows 解析 windowLimits：cap 缺失或非正数的窗口跳过，
// 已用百分比按 used / cap 计算并封顶 100；resetAt 为 0（窗口未开始）时不给重置时间。
func parseCommandCodeWindows(root map[string]json.RawMessage) []credits.Window {
	limits := jsonObject(root["windowLimits"])
	var windows []credits.Window
	for _, entry := range commandCodeWindowKeys {
		window := jsonObject(limits[entry.key])
		if window == nil {
			continue
		}
		capUSD, ok := jsonFloat(window["cap"])
		if !ok || capUSD <= 0 {
			continue
		}
		used, _ := jsonFloat(window["used"])
		percent := used / capUSD * 100
		if percent > 100 {
			percent = 100
		}
		if percent < 0 {
			percent = 0
		}
		var resetsAt time.Time
		if reset, ok := jsonFloat(window["resetAt"]); ok && reset > 0 {
			resetsAt = time.UnixMilli(int64(reset))
		}
		windows = append(windows, credits.Window{
			Label:       entry.title + " · " + moneyUSD(capUSD),
			UsedPercent: percent,
			ResetsAt:    resetsAt,
		})
	}
	return windows
}

// moneyUSD 复用 credits 包的金额展示（"$3.00"）：
func moneyUSD(value float64) string {
	return credits.Credits{Total: value, Currency: "USD"}.AmountText()
}

// getCommandCodeJSON 以 Bearer 密钥请求一条 /alpha 路由；非 200 时返回带状态码的错误。
func getCommandCodeJSON(ctx context.Context, opts PlanLimitsOptions, path, key string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, opts.baseURL()+path, nil)
	if err != nil {
		return nil, fmt.Errorf("commandcode: 构造用量请求: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Accept", "application/json")
	response, err := opts.client().Do(request)
	if err != nil {
		return nil, fmt.Errorf("commandcode: 请求 %s: %w", path, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("commandcode: 用量接口 %s 返回 HTTP %d", path, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("commandcode: 读取用量响应: %w", err)
	}
	return body, nil
}

// jsonObject 把一个 JSON 值解成对象；缺失、null 或类型不符时返回 nil。
func jsonObject(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil
	}
	return object
}

// jsonText 读取字符串值；缺失、null 或类型不符时返回空串。
func jsonText(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return ""
	}
	return text
}

// jsonFloat 读取数值，兼容数字与数字字符串；缺失或非法时 ok 为 false。
func jsonFloat(raw json.RawMessage) (float64, bool) {
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
