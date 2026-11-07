package credits

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// clineDefaultBaseURL 是 Cline API 的默认 baseURL。
const clineDefaultBaseURL = "https://api.cline.bot"

// clineLogin 是 Cline 的一次登录：令牌与端点。
type clineLogin struct {
	token   string
	baseURL string
}

// readCline 读 Cline 的预付费余额。
// 只有当前 provider 是按量付费的 `cline` 时才有余额，
// `cline-pass`（订阅）读的是方案窗口，见 clinePlanWindows。
func readCline(ctx context.Context, opts Options) (Credits, error) {
	if provider := clineProviderID(opts); provider != "" && provider != "cline" {
		return Credits{}, fmt.Errorf("credits: cline 当前 provider 为 %s，没有预付费余额", provider)
	}
	login, err := clineResolveLogin(opts, "cline")
	if err != nil {
		return Credits{}, err
	}
	client := opts.client()

	status, body, err := getJSON(ctx, client, login.baseURL+"/api/v1/users/me", login.token)
	if err != nil {
		return Credits{}, err
	}
	if err := clineStatusError("读取 Cline 账户", status); err != nil {
		return Credits{}, err
	}
	user, err := clineUnwrap(body)
	if err != nil {
		return Credits{}, err
	}

	// 有启用的组织就按组织读余额，否则按用户读。
	if organization := activeClineOrganization(user); organization != "" {
		balancePath := "/api/v1/organizations/" + pathComponent(organization) + "/balance"
		return clineReadBalance(ctx, opts, login, balancePath)
	}
	if id := jsonText(user, "id"); id != "" {
		return clineReadBalance(ctx, opts, login, "/api/v1/users/"+pathComponent(id)+"/balance")
	}
	return Credits{}, fmt.Errorf("cline: 账户响应里既没有启用的组织也没有用户 id")
}

// clineReadBalance 读某一个余额端点。Cline 的整数额度按一百万归一化成美元。
func clineReadBalance(ctx context.Context, opts Options, login clineLogin, path string) (Credits, error) {
	status, body, err := getJSON(ctx, opts.client(), login.baseURL+path, login.token)
	if err != nil {
		return Credits{}, err
	}
	if err := clineStatusError("读取 Cline 余额", status); err != nil {
		return Credits{}, err
	}
	payload, err := clineUnwrap(body)
	if err != nil {
		return Credits{}, err
	}
	balance := jsonFloat(payload, "balance")
	if balance == nil {
		return Credits{}, fmt.Errorf("cline: 余额响应缺少 balance")
	}
	dollars := *balance / 1_000_000
	return Credits{
		Provider: ProviderCline,
		Total:    dollars,
		Currency: "USD",
		Usable:   dollars > 0,
		ReadAt:   opts.now(),
	}, nil
}

// clinePlanWindows 读 ClinePass 的滚动窗口（GET /api/v1/users/me/plan/usage-limits）。
func clinePlanWindows(ctx context.Context, opts Options) ([]Window, error) {
	login, err := clineResolveLogin(opts, "cline-pass")
	if err != nil {
		return nil, err
	}
	status, body, err := getJSON(ctx, opts.client(), login.baseURL+"/api/v1/users/me/plan/usage-limits", login.token)
	if err != nil {
		return nil, err
	}
	if err := clineStatusError("读取 Cline 用量窗口", status); err != nil {
		return nil, err
	}
	payload, err := clineUnwrap(body)
	if err != nil {
		return nil, err
	}
	return parseClineWindows(payload)
}

// parseClineWindows 按官方订阅面板的顺序取 five_hour / weekly / monthly 三行，
// percentUsed 已经是 0–100。
func parseClineWindows(payload map[string]json.RawMessage) ([]Window, error) {
	var limits []map[string]json.RawMessage
	if err := json.Unmarshal(payload["limits"], &limits); err != nil {
		return nil, fmt.Errorf("cline: 用量窗口响应缺少 limits: %w", err)
	}
	windows := make([]Window, 0, len(limits))
	for _, entry := range []struct {
		kind, label string
		minutes     int
	}{
		{"five_hour", "5-hour limit", 300},
		{"weekly", "Weekly", 10_080},
		{"monthly", "Monthly", 43_200},
	} {
		for _, limit := range limits {
			if jsonText(limit, "type") != entry.kind {
				continue
			}
			percent := jsonFloat(limit, "percentUsed")
			if percent == nil {
				continue
			}
			used := *percent
			if used < 0 {
				used = 0
			}
			if used > 100 {
				used = 100
			}
			windows = append(windows, Window{
				ID:          entry.kind,
				Label:       entry.label,
				Period:      windowPeriod(entry.minutes),
				UsedPercent: used,
				ResetsAt:    parseISOTime(jsonText(limit, "resetsAt")),
			})
			break
		}
	}
	return windows, nil
}

// clineStatusError 把认证失败映射成错误；2xx 以外也报错。
func clineStatusError(action string, status int) error {
	switch status {
	case 200:
		return nil
	case 401, 403:
		// 遇到 401/403 时未实现 OAuth refresh token 刷新（见 README），
		// 因此直接报错让调用方重新登录。
		return fmt.Errorf("cline: %s: 登录已失效或被拒绝（HTTP %d）", action, status)
	default:
		return fmt.Errorf("cline: %s: 接口返回 HTTP %d", action, status)
	}
}

// clineUnwrap 解包响应：带 success 字段时必须为 true，并把 data 当结果；
// 不带 success 时原样返回整个 JSON。
func clineUnwrap(raw []byte) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("cline: 解析响应: 响应为空")
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("cline: 解析响应: %w", err)
	}
	if success := jsonBool(root, "success"); success != nil {
		if !*success {
			return nil, fmt.Errorf("cline: 接口返回 success=false")
		}
		data := jsonRawObject(root["data"])
		if data == nil {
			return nil, fmt.Errorf("cline: 响应缺少 data")
		}
		return data, nil
	}
	return root, nil
}

// activeClineOrganization 取第一个 active 且有 id 的组织。
func activeClineOrganization(user map[string]json.RawMessage) string {
	var organizations []map[string]json.RawMessage
	if err := json.Unmarshal(user["organizations"], &organizations); err != nil {
		return ""
	}
	for _, organization := range organizations {
		if active := jsonBool(organization, "active"); active != nil && *active {
			if id := jsonText(organization, "organizationId"); id != "" {
				return id
			}
		}
	}
	return ""
}

// clineProviderID 解析当前 provider id：
// 先看 CLINE_PROVIDER，再看 providers.json 的 lastUsedProvider，
// 最后在只存在 cline-pass 时报 cline-pass。
func clineProviderID(opts Options) string {
	if explicit := strings.TrimSpace(opts.envValue("CLINE_PROVIDER")); explicit != "" {
		return explicit
	}
	root, _, ok := clineConfiguration(opts)
	if !ok {
		return ""
	}
	if lastUsed := jsonText(root, "lastUsedProvider"); lastUsed != "" {
		return lastUsed
	}
	providers := jsonRawObject(root["providers"])
	if providers == nil {
		return ""
	}
	if providers["cline-pass"] != nil && providers["cline"] == nil {
		return "cline-pass"
	}
	return ""
}

// clineConfiguration 读 Cline 的 settings/providers.json：
// $CLINE_DATA_DIR/settings/providers.json，其次 ~/.cline/data/settings/providers.json，
// 再次 ~/.cline/settings/providers.json。
func clineConfiguration(opts Options) (map[string]json.RawMessage, string, bool) {
	home := opts.home()
	if home == "" {
		return nil, "", false
	}
	dataDirectory := strings.TrimSpace(opts.envValue("CLINE_DATA_DIR"))
	if dataDirectory == "" {
		dataDirectory = filepath.Join(home, ".cline", "data")
	}
	candidates := []string{
		filepath.Join(dataDirectory, "settings", "providers.json"),
		filepath.Join(home, ".cline", "settings", "providers.json"),
	}
	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		var root map[string]json.RawMessage
		if err := json.Unmarshal(data, &root); err != nil {
			continue
		}
		return root, candidate, true
	}
	return nil, "", false
}

// clineResolveLogin 解析某 provider（cline 或 cline-pass）的令牌与端点：
// CLINE_API_KEY 环境变量优先，其次是 settings.auth.accessToken（按官方 OAuth 规则加
// "workos:" 前缀），最后是 settings.apiKey。cline-pass 与 cline 共享 OAuth 存储。
func clineResolveLogin(opts Options, provider string) (clineLogin, error) {
	root, _, _ := clineConfiguration(opts)
	var settings map[string]json.RawMessage
	var sharedSettings map[string]json.RawMessage
	if root != nil {
		providers := jsonRawObject(root["providers"])
		if entry := jsonRawObject(providers[provider]); entry != nil {
			settings = jsonRawObject(entry["settings"])
		}
		if provider == "cline-pass" {
			if entry := jsonRawObject(providers["cline"]); entry != nil {
				sharedSettings = jsonRawObject(entry["settings"])
			}
		}
	}

	base := clineDefaultBaseURL
	if value := jsonText(settings, "baseUrl"); value != "" {
		base = value
	} else if value := jsonText(sharedSettings, "baseUrl"); value != "" {
		base = value
	}
	baseURL, err := parseHTTPBaseURL(base)
	if err != nil {
		return clineLogin{}, err
	}

	if key := strings.TrimSpace(opts.envValue("CLINE_API_KEY")); key != "" {
		return clineLogin{token: key, baseURL: baseURL}, nil
	}
	if accessToken := clineAuthString(settings, sharedSettings, "accessToken"); accessToken != "" {
		return clineLogin{token: clineAccountToken(accessToken), baseURL: baseURL}, nil
	}
	key := clineAuthString(settings, sharedSettings, "apiKey")
	if key == "" {
		return clineLogin{}, fmt.Errorf("%w: %s 未找到 Cline 登录", ErrMissingKey, provider)
	}
	return clineLogin{token: key, baseURL: baseURL}, nil
}

// clineAuthString 先看 provider 自己的 auth，再看共享的 cline auth。
func clineAuthString(settings, sharedSettings map[string]json.RawMessage, key string) string {
	if value := strings.TrimSpace(jsonText(jsonRawObject(settings["auth"]), key)); value != "" {
		return value
	}
	return strings.TrimSpace(jsonText(jsonRawObject(sharedSettings["auth"]), key))
}

// clineAccountToken 返回给 API 用的账户令牌：官方 OAuth 的账户令牌要加 workos: 前缀。
func clineAccountToken(value string) string {
	if strings.HasPrefix(strings.ToLower(value), "workos:") {
		return value
	}
	return "workos:" + value
}

// parseHTTPBaseURL 校验 baseUrl 是 http/https 且有主机。
func parseHTTPBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("cline: baseUrl 非法: %q", raw)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", fmt.Errorf("cline: baseUrl 协议非法: %q", raw)
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

// pathComponent 把值转成 URL 路径段（转义保留字符）。
func pathComponent(value string) string {
	return url.PathEscape(value)
}

// parseISOTime 解析 Cline 与 Factory 写出的 ISO 8601 时间（可能带小数秒）。
func parseISOTime(text string) time.Time {
	if strings.TrimSpace(text) == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}
