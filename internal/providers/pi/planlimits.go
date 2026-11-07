package pi

// 本文件读 Pi 自己登录的 provider（OAuth 令牌存在 <config>/auth.json）的方案额度窗口，
// 读出来交给 credits.Window 这份额度契约。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"mcode/internal/providers/credits"
)

const (
	piAnthropicUsageURL = "https://api.anthropic.com/api/oauth/usage"
	piCodexUsageURL     = "https://chatgpt.com/backend-api/wham/usage"
	piUsageTimeout      = 15 * time.Second
)

// piFractionRegexp 匹配 ISO 8601 时间里的小数秒（丢掉微秒）。
var piFractionRegexp = regexp.MustCompile(`\.\d+`)

// PlanLimitsOptions 是一次方案额度读取的依赖与来源配置，测试时全部可替换。
type PlanLimitsOptions struct {
	// Env 是环境变量来源；为 nil 时读进程环境。
	Env map[string]string
	// HomeDir 覆盖用户主目录；为空时用 env["HOME"] 或 os.UserHomeDir()。
	HomeDir string
	// HTTPClient 为空时使用带 15 秒超时的默认 client。
	HTTPClient *http.Client
	// AnthropicURL / CodexURL 覆盖默认用量端点，测试时指向 httptest 服务器。
	AnthropicURL string
	CodexURL     string
	// Now 覆盖当前时间；为空时用 time.Now。
	Now func() time.Time
}

func (o PlanLimitsOptions) client() *http.Client {
	if o.HTTPClient != nil {
		return o.HTTPClient
	}
	return &http.Client{Timeout: piUsageTimeout}
}

func (o PlanLimitsOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o PlanLimitsOptions) anthropicURL() string {
	if url := strings.TrimSpace(o.AnthropicURL); url != "" {
		return url
	}
	return piAnthropicUsageURL
}

func (o PlanLimitsOptions) codexURL() string {
	if url := strings.TrimSpace(o.CodexURL); url != "" {
		return url
	}
	return piCodexUsageURL
}

// ReadPlanLimits 读出 Pi 持有的 OAuth 登录所对应的方案窗口，按 auth.json 里
// provider 键名的排序依次请求。
//
// 没有可读的登录、没有 live token 或所有 provider 都没给出窗口时返回 (nil, nil)；
// 用量端点返回非 200 时返回带状态码的错误。
func ReadPlanLimits(ctx context.Context, opts PlanLimitsOptions) ([]credits.Window, error) {
	file := filepath.Join(ConfigDir(opts.Env, opts.HomeDir), "auth.json")
	data, err := os.ReadFile(file)
	if err != nil {
		// 没有 auth.json（或读不到）就等同于没有登录，不是错误。
		return nil, nil
	}
	root := piRawObject(data)
	if root == nil {
		return nil, nil
	}
	keys := make([]string, 0, len(root))
	for key := range root {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	now := opts.now()
	var windows []credits.Window
	for _, key := range keys {
		entry := piRawObject(root[key])
		if entry == nil || piString(entry, "type") != "oauth" {
			continue
		}
		token := piString(entry, "access")
		if token == "" {
			continue
		}
		// 过期令牌在用量端点上一无所获，直接跳过。
		if expires := piNumber(entry, "expires"); expires != nil && time.UnixMilli(int64(*expires)).Before(now) {
			continue
		}
		var found []credits.Window
		var readErr error
		switch key {
		case "anthropic":
			found, readErr = readAnthropicLimits(ctx, token, opts)
		case "openai-codex", "openai":
			found, readErr = readCodexLimits(ctx, token, opts)
		default:
			continue
		}
		if readErr != nil {
			return nil, readErr
		}
		windows = append(windows, found...)
	}
	if len(windows) == 0 {
		return nil, nil
	}
	return windows, nil
}

// readAnthropicLimits 请求 Anthropic 的 OAuth 用量端点，它的形状与 Claude Code
// 自己读的一致。
func readAnthropicLimits(ctx context.Context, token string, opts PlanLimitsOptions) ([]credits.Window, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, opts.anthropicURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("pi: 构造 Anthropic 用量请求: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("anthropic-beta", "oauth-2025-04-20")
	request.Header.Set("Accept", "application/json")

	status, body, err := piGetJSON(opts.client(), request)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("pi: Anthropic 用量端点返回 %d", status)
	}
	root := piRawObject(body)
	if root == nil {
		return nil, fmt.Errorf("pi: 无法解析 Anthropic 用量响应")
	}
	return piClaudeWindows(root, "Claude"), nil
}

// readCodexLimits 请求 ChatGPT 配额展示背后的用量载荷，含主次两个窗口。
func readCodexLimits(ctx context.Context, token string, opts PlanLimitsOptions) ([]credits.Window, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, opts.codexURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("pi: 构造 Codex 用量请求: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	// 端点按账号划分配额，而账号只写在令牌自己的 claim 里。
	if accountID := piCodexAccountID(token); accountID != "" {
		request.Header.Set("ChatGPT-Account-Id", accountID)
	}

	status, body, err := piGetJSON(opts.client(), request)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("pi: Codex 用量端点返回 %d", status)
	}
	root := piRawObject(body)
	if root == nil {
		return nil, fmt.Errorf("pi: 无法解析 Codex 用量响应")
	}
	rateLimit := root
	if nested := piRawObject(root["rate_limit"]); nested != nil {
		rateLimit = nested
	}
	now := opts.now()
	var windows []credits.Window
	for _, raw := range []json.RawMessage{rateLimit["primary_window"], rateLimit["secondary_window"]} {
		if window := piCodexWindow(raw, now); window != nil {
			windows = append(windows, *window)
		}
	}
	return windows, nil
}

// piClaudeWindows 解析 Claude 用量：limits 里的每一行
// （session、weekly、按模型的 weekly）各是一个窗口，为空时退回顶层的 five_hour 与
// seven_day，另外追加打开了的 extra usage。
func piClaudeWindows(root map[string]json.RawMessage, titlePrefix string) []credits.Window {
	var windows []credits.Window
	for _, item := range piRawArray(root["limits"]) {
		limit := piRawObject(item)
		percent := piNumber(limit, "percent")
		if percent == nil {
			continue
		}
		kind := piString(limit, "kind")
		group := piPlanName(piString(limit, "group"))
		if group == "" {
			group = "Usage"
		}
		var scope string
		if scopeObject := piRawObject(limit["scope"]); scopeObject != nil {
			if model := piRawObject(scopeObject["model"]); model != nil {
				scope = piString(model, "display_name")
			}
		}
		base := group
		switch kind {
		case "session":
			base = "5-hour limit"
		case "weekly_all":
			base = "Weekly · all models"
		default:
			if scope != "" {
				base = group + " · " + scope
			}
		}
		windows = append(windows, credits.Window{
			Label:       piUsageTitle(titlePrefix, base),
			UsedPercent: *percent,
			ResetsAt:    piParseResetTime(piString(limit, "resets_at")),
		})
	}
	if len(windows) == 0 {
		for _, fallback := range []struct{ key, title string }{
			{"five_hour", "5-hour limit"},
			{"seven_day", "Weekly · all models"},
		} {
			window := piRawObject(root[fallback.key])
			percent := piNumber(window, "utilization")
			if percent == nil {
				continue
			}
			windows = append(windows, credits.Window{
				Label:       piUsageTitle(titlePrefix, fallback.title),
				UsedPercent: *percent,
				ResetsAt:    piParseResetTime(piString(window, "resets_at")),
			})
		}
	}
	if extra := piRawObject(root["extra_usage"]); extra != nil {
		if enabled := piBool(extra, "is_enabled"); enabled != nil && *enabled {
			if percent := piNumber(extra, "utilization"); percent != nil {
				windows = append(windows, credits.Window{
					Label:       piUsageTitle(titlePrefix, "Extra usage"),
					UsedPercent: *percent,
				})
			}
		}
	}
	return windows
}

// piCodexWindow 解析一个 Codex 窗口；缺 used_percent 或 limit_window_seconds 时返回 nil。
func piCodexWindow(raw json.RawMessage, now time.Time) *credits.Window {
	window := piRawObject(raw)
	if window == nil {
		return nil
	}
	percent := piNumber(window, "used_percent")
	seconds := piNumber(window, "limit_window_seconds")
	if percent == nil || seconds == nil {
		return nil
	}
	var resetsAt time.Time
	if at := piNumber(window, "reset_at"); at != nil {
		resetsAt = time.Unix(int64(*at), 0)
	} else if after := piNumber(window, "reset_after_seconds"); after != nil {
		resetsAt = now.Add(time.Duration(*after) * time.Second)
	}
	minutes := int(*seconds) / 60
	return &credits.Window{
		Label:       "Codex · " + piWindowTitle(&minutes),
		UsedPercent: *percent,
		ResetsAt:    resetsAt,
	}
}

// piWindowTitle 返回用量窗口的展示标题。
func piWindowTitle(minutes *int) string {
	if minutes == nil {
		return "Usage limit"
	}
	switch *minutes {
	case 300:
		return "5-hour limit"
	case 1440:
		return "Daily"
	case 10080:
		return "Weekly"
	}
	if *minutes >= 40320 && *minutes <= 44640 {
		return "Monthly"
	}
	if *minutes%1440 == 0 {
		return fmt.Sprintf("%d-day limit", *minutes/1440)
	}
	hours := *minutes / 60
	if hours < 1 {
		hours = 1
	}
	return fmt.Sprintf("%d-hour limit", hours)
}

// piPlanName 返回方案名：首字母大写。
func piPlanName(raw string) string {
	if raw == "" {
		return ""
	}
	runes := []rune(raw)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

// piUsageTitle 给窗口标题加 provider 前缀。
func piUsageTitle(prefix, base string) string {
	if prefix == "" {
		return base
	}
	return prefix + " · " + base
}

// piParseResetTime 解析用量端点给的 ISO 8601 重置时间；Claude 会写微秒，
// 小数部分整段丢掉（ISO 8601 解析器不吃微秒）。
func piParseResetTime(text string) time.Time {
	trimmed := piFractionRegexp.ReplaceAllString(strings.TrimSpace(text), "")
	if trimmed == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05"} {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

// piCodexAccountID 从令牌 payload 的 OpenAI auth claim 里取账号 id，不需要额外请求。
func piCodexAccountID(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payload := strings.NewReplacer("-", "+", "_", "/").Replace(parts[1])
	if remainder := len(payload) % 4; remainder != 0 {
		payload += strings.Repeat("=", 4-remainder)
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return ""
	}
	root := piRawObject(data)
	if root == nil {
		return ""
	}
	auth := piRawObject(root["https://api.openai.com/auth"])
	return piString(auth, "chatgpt_account_id")
}

// piGetJSON 发起请求并读回状态码与响应体。
func piGetJSON(client *http.Client, request *http.Request) (int, []byte, error) {
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("pi: 请求 %s: %w", request.URL, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return response.StatusCode, nil, fmt.Errorf("pi: 读取 %s 响应: %w", request.URL, err)
	}
	return response.StatusCode, body, nil
}
