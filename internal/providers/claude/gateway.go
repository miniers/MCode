package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mcode/internal/providers/loginenv"
)

// gatewayHTTPClient 请求 Magpie 网关，测试中可替换。
var gatewayHTTPClient = &http.Client{Timeout: 10 * time.Second}

// readGatewayPlanLimits 读取 Claude Code 所指向的 Magpie 网关的额度。
// 返回值的 bool 表示当前上游是否被识别为 Magpie：false 时调用方应回退到原有逻辑。
func readGatewayPlanLimits(ctx context.Context) (PlanLimits, bool, error) {
	gateway, ok := gatewayConfig()
	if !ok {
		return PlanLimits{}, false, nil
	}
	if !isMagpieGateway(ctx, gateway) {
		return PlanLimits{}, false, nil
	}

	body, status, err := gatewayGet(ctx, gateway.cardsURL, gateway.token)
	if err != nil {
		return PlanLimits{}, true, fmt.Errorf("claude: 请求 Magpie 额度接口: %w", err)
	}
	if status == http.StatusForbidden {
		return PlanLimits{}, true, fmt.Errorf("claude: Magpie 网关 key 无权读取额度")
	}
	if status != http.StatusOK {
		return PlanLimits{}, true, fmt.Errorf("claude: Magpie 额度接口返回 %d: %s", status, strings.TrimSpace(string(body)))
	}

	limits, err := parseGatewayLimits(body)
	if err != nil {
		return PlanLimits{}, true, err
	}
	// 卡片接口不带 last/lastServedAt，用 kept 报告补齐「最近应答」线索；
	// 读取失败只是没有线索，不影响卡片读数与返回的 error。
	limits = applyGatewayServed(limits, readGatewayServed(ctx, gateway))
	// 经网关读到的额度标记来源，app 层据此决定是否用 usage/requests 记录匹配账号。
	limits.Source = "magpie"
	return limits, true, nil
}

// gatewayProbeTTL 是「上游是不是 Magpie」判定结果的缓存时长。
const gatewayProbeTTL = 60 * time.Second

// gatewayNow 返回当前时间，测试中可替换。
var gatewayNow = time.Now

var (
	gatewayProbeMu sync.Mutex
	// cachedGatewayProbe 记录最近一次探测结果；root 与 token 一起作为键（token 只用于比较）。
	cachedGatewayProbe struct {
		root  string
		token string
		ok    bool
		at    time.Time
	}
)

// GatewayEndpoint 返回 Claude Code 所指向的 Magpie 网关根地址（不含末尾的 / 与 /v1）与 key。
// 未配置网关或上游不是 Magpie 时 ok 为 false。判定结果按 root+token 缓存 60 秒，
// 避免每次读额度都探测一次根地址；换网关或换 key 时立即重新探测，探测失败不缓存。
func GatewayEndpoint(ctx context.Context) (baseURL, token string, ok bool) {
	gateway, configured := gatewayConfig()
	if !configured {
		return "", "", false
	}
	if !cachedIsMagpieGateway(ctx, gateway) {
		return "", "", false
	}
	return gateway.root, gateway.token, true
}

// cachedIsMagpieGateway 带缓存地判定上游是不是 Magpie。
func cachedIsMagpieGateway(ctx context.Context, gateway magpieGateway) bool {
	gatewayProbeMu.Lock()
	if cachedGatewayProbe.root == gateway.root && cachedGatewayProbe.token == gateway.token &&
		!cachedGatewayProbe.at.IsZero() && gatewayNow().Sub(cachedGatewayProbe.at) < gatewayProbeTTL {
		cached := cachedGatewayProbe.ok
		gatewayProbeMu.Unlock()
		return cached
	}
	gatewayProbeMu.Unlock()

	ok, err := probeMagpieGateway(ctx, gateway)
	if err != nil {
		// 网络失败的结果不缓存，下次调用重试。
		return false
	}

	gatewayProbeMu.Lock()
	cachedGatewayProbe.root = gateway.root
	cachedGatewayProbe.token = gateway.token
	cachedGatewayProbe.ok = ok
	cachedGatewayProbe.at = gatewayNow()
	gatewayProbeMu.Unlock()
	return ok
}

// resetGatewayProbeCache 清除网关探测缓存，供单元测试使用。
func resetGatewayProbeCache() {
	gatewayProbeMu.Lock()
	cachedGatewayProbe.root = ""
	cachedGatewayProbe.token = ""
	cachedGatewayProbe.ok = false
	cachedGatewayProbe.at = time.Time{}
	gatewayProbeMu.Unlock()
}

// RefreshGatewayQuotas 让 Magpie 网关重新向厂商读取 Claude 额度卡片（含非首选账号）。
// 返回值的 bool 表示当前上游是否被识别为 Magpie：false 时调用方无需处理。
func RefreshGatewayQuotas(ctx context.Context) (bool, error) {
	gateway, ok := gatewayConfig()
	if !ok {
		return false, nil
	}
	if !isMagpieGateway(ctx, gateway) {
		return false, nil
	}

	// 只刷新 claude 卡片；不带 provider 会连 codex 等其他卡片一起重读。
	target := gateway.refreshURL + "?provider=claude"
	body, status, err := gatewayPost(ctx, target, gateway.token)
	if err != nil {
		return true, fmt.Errorf("claude: 请求 Magpie 额度刷新: %w", err)
	}
	if status == http.StatusForbidden {
		return true, fmt.Errorf("claude: Magpie 网关 key 无权刷新额度")
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return true, fmt.Errorf("claude: Magpie 额度刷新返回 %d: %s", status, strings.TrimSpace(string(body)))
	}
	return true, nil
}

// magpieGateway 是从 Claude Code 配置中解析出的网关地址与 key。
type magpieGateway struct {
	// root 是网关根地址，不含末尾的 / 与 /v1，其余地址都由它拼出。
	root       string
	infoURL    string
	cardsURL   string
	servedURL  string
	refreshURL string
	token      string
}

// gatewayConfig 合并进程环境与 settings.json 的 env 块（进程环境优先）。
func gatewayConfig() (magpieGateway, bool) {
	env := gatewayEnvironment()
	value := func(key string) string { return env[key] }

	base := value("ANTHROPIC_BASE_URL")
	token := value("ANTHROPIC_AUTH_TOKEN")
	if token == "" {
		token = value("ANTHROPIC_API_KEY")
	}
	if base == "" || token == "" {
		return magpieGateway{}, false
	}

	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" || u.Hostname() == "api.anthropic.com" {
		return magpieGateway{}, false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return magpieGateway{}, false
	}

	// 去掉末尾的 / 与 /v1，再拼出网关根地址与额度地址。
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/v1")
	u.RawQuery = ""
	u.Fragment = ""
	root := strings.TrimRight(u.String(), "/")

	return magpieGateway{
		root:       root,
		infoURL:    root + "/",
		cardsURL:   root + "/v1/magpie/quotas/cards",
		servedURL:  root + "/v1/magpie/quotas?kept=1",
		refreshURL: root + "/v1/magpie/quotas/refresh",
		token:      token,
	}, true
}

// gatewayEnvironmentValue 读取一个网关环境变量，口径同 CLI 子进程（写法同 configDirectory）：
// 子进程环境来自登录 Shell（loginenv.Environ），只在 .zshrc 里 export 时本进程环境里并没有该变量，
// 只读进程环境就会看不到网关地址与 key，额度面板跟着走错分支；
// 进程环境显式给出该变量时以它为准（哪怕为空，便于调用方与测试覆盖），
// 只有进程环境里没有它时才去读登录 Shell 环境。
func gatewayEnvironmentValue(key string) string {
	if value, ok := os.LookupEnv(key); ok {
		return strings.TrimSpace(value)
	}
	prefix := key + "="
	for _, entry := range loginenv.Environ() {
		if value, ok := strings.CutPrefix(entry, prefix); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// gatewayEnvironment 读取网关环境（进程环境或登录 Shell 环境，口径同 CLI 子进程），
// 再用 settings.json 的 env 块补齐缺失项。
func gatewayEnvironment() map[string]string {
	env := make(map[string]string)
	for _, key := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if v := gatewayEnvironmentValue(key); v != "" {
			env[key] = v
		}
	}

	configDir := configDirectory()
	if configDir != "" {
		env["CLAUDE_CONFIG_DIR"] = configDir
	} else {
		if home, err := os.UserHomeDir(); err == nil {
			configDir = filepath.Join(home, ".claude")
		}
	}
	if configDir != "" {
		if settings := readSettingsEnv(configDir); settings != nil {
			for key, value := range settings {
				if env[key] == "" && value != "" {
					env[key] = value
				}
			}
		}
	}
	return env
}

// readSettingsEnv 读取 settings.json 中 env 对象的字符串值。
func readSettingsEnv(configDir string) map[string]string {
	data, err := os.ReadFile(filepath.Join(configDir, "settings.json"))
	if err != nil {
		return nil
	}
	var settings struct {
		Env map[string]any `json:"env"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil
	}
	env := make(map[string]string, len(settings.Env))
	for key, value := range settings.Env {
		if text, ok := value.(string); ok {
			env[key] = text
		}
	}
	return env
}

// isMagpieGateway 通过根地址的 {"name":"magpie"} 识别上游；其他响应一律视为非 Magpie。
func isMagpieGateway(ctx context.Context, gateway magpieGateway) bool {
	ok, err := probeMagpieGateway(ctx, gateway)
	return err == nil && ok
}

// probeMagpieGateway 探测根地址，返回是否 Magpie 以及网络错误（只有网络错误才返回 err）。
func probeMagpieGateway(ctx context.Context, gateway magpieGateway) (bool, error) {
	body, status, err := gatewayGet(ctx, gateway.infoURL, gateway.token)
	if err != nil {
		return false, err
	}
	if status != http.StatusOK {
		return false, nil
	}
	var info struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return false, nil
	}
	return info.Name == "magpie", nil
}

// gatewayGet 执行带 Bearer key 的 GET 请求并读回响应体。
func gatewayGet(ctx context.Context, target, token string) ([]byte, int, error) {
	return gatewayDo(ctx, http.MethodGet, target, token)
}

// gatewayPost 执行带 Bearer key 的 POST 请求并读回响应体。
func gatewayPost(ctx context.Context, target, token string) ([]byte, int, error) {
	return gatewayDo(ctx, http.MethodPost, target, token)
}

func gatewayDo(ctx context.Context, method, target, token string) ([]byte, int, error) {
	client := gatewayHTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// magpieCardsResponse 对应 /v1/magpie/quotas/cards 的返回：每个账号一张卡片。
type magpieCardsResponse struct {
	Data []magpieCard `json:"data"`
}

type magpieCard struct {
	Provider string `json:"provider"`
	Plan     string `json:"plan"`
	User     string `json:"user"`
	// Kind 是卡片种类：subscription、plan 或 balance。
	Kind string `json:"kind"`
	// Alias 是 magpie 里配置的账号名，只有 claude-tui 卡片带该字段。
	Alias   string         `json:"alias"`
	Windows []magpieWindow `json:"windows"`
	// Last 标记最近一次应答请求的账号，LastServedAt 是该账号最近一次应答的时间。
	Last         bool            `json:"last"`
	LastServedAt json.RawMessage `json:"lastServedAt"`
	// ReadAt 是卡片额度被读取的时间。
	ReadAt json.RawMessage `json:"readAt"`
}

// magpieServedResponse 对应 /v1/magpie/quotas?kept=1 的返回：同一份缓存卡片，
// 但带上 provider、kind、user 与最近一次应答时间，不触发厂商读取。
type magpieServedResponse struct {
	Data []magpieServedCard `json:"data"`
}

type magpieServedCard struct {
	Provider     string          `json:"provider"`
	Kind         string          `json:"kind"`
	User         string          `json:"user"`
	Last         bool            `json:"last"`
	LastServedAt json.RawMessage `json:"lastServedAt"`
}

type magpieWindow struct {
	Name     string          `json:"name"`
	Used     json.RawMessage `json:"used"`
	ResetsAt json.RawMessage `json:"resetsAt"`
	Error    string          `json:"error"`
}

// parseGatewayLimits 解析卡片响应，只保留 Claude 账号的窗口。
// claude-tui-boost 这类插件在网关里的 provider 是 "claude-tui"，与订阅账号的 "claude" 一样保留。
func parseGatewayLimits(raw []byte) (PlanLimits, error) {
	var resp magpieCardsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return PlanLimits{}, fmt.Errorf("claude: 解析 Magpie 额度: %w", err)
	}

	cards := make([]magpieCard, 0, len(resp.Data))
	for _, card := range resp.Data {
		if card.Provider == "claude" || card.Provider == "claude-tui" {
			cards = append(cards, card)
		}
	}

	var plans []string
	windows := make([]PlanLimitWindow, 0)
	for _, card := range cards {
		if plan := titleCase(card.Plan); plan != "" && !containsString(plans, plan) {
			plans = append(plans, plan)
		}
		// 账号名优先用 magpie 配置的别名，缺失时回退到 user（订阅账号是邮箱）。
		account := strings.TrimSpace(card.Alias)
		if account == "" {
			account = card.User
		}
		prefix := ""
		if len(cards) > 1 && account != "" {
			prefix = account + " · "
		}
		readAt := gatewayTime(card.ReadAt)
		lastServedAt := gatewayTime(card.LastServedAt)
		for _, rawWindow := range card.Windows {
			used := jsonFloat(rawWindow.Used)
			if rawWindow.Name == "" || used == nil {
				continue
			}
			id, label := gatewayWindow(rawWindow.Name)
			windowAccount := ""
			if len(cards) > 1 {
				windowAccount = account
			}
			windows = append(windows, PlanLimitWindow{
				ID:           id,
				Account:      windowAccount,
				Label:        prefix + label,
				UsedPercent:  *used,
				ResetsAt:     parseResetTime(jsonText(rawWindow.ResetsAt)),
				Provider:     card.Provider,
				User:         card.User,
				Kind:         card.Kind,
				Last:         card.Last,
				LastServedAt: lastServedAt,
				ReadAt:       readAt,
			})
		}
	}

	return PlanLimits{
		PlanType: strings.Join(plans, " · "),
		Windows:  windows,
	}, nil
}

// readGatewayServed 读取 kept 报告里的最近应答时间。老版本网关不认 kept=1 可能退回
// 一次最长 12 秒的真实读取，因此单独限时 3 秒；任何失败都只是没有线索，不向上报错。
func readGatewayServed(ctx context.Context, gateway magpieGateway) []magpieServedCard {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	body, status, err := gatewayGet(ctx, gateway.servedURL, gateway.token)
	if err != nil || status != http.StatusOK {
		return nil
	}
	var resp magpieServedResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil
	}
	return resp.Data
}

// applyGatewayServed 把 kept 报告的最近应答时间按 (provider, kind, user) 填进对应窗口，
// 并用它给 claude 卡片里最近应答的那个账号补上 Last 标记。
// 网关的 last 是全 provider 最新的那张卡，不能直接照抄，只在 claude 卡片内部判定。
func applyGatewayServed(limits PlanLimits, served []magpieServedCard) PlanLimits {
	if len(limits.Windows) == 0 || len(served) == 0 {
		return limits
	}

	type servedKey struct {
		provider string
		kind     string
		user     string
	}
	times := make(map[servedKey]time.Time, len(served))
	for _, card := range served {
		// 只认 claude 订阅卡片：其他 provider 的 last 与这里的窗口无关，
		// 没有 user 或时间解析不出时也不猜账号。
		if card.Provider != "claude" || strings.TrimSpace(card.User) == "" {
			continue
		}
		at := gatewayTime(card.LastServedAt)
		if at.IsZero() {
			continue
		}
		key := servedKey{provider: card.Provider, kind: card.Kind, user: card.User}
		if at.After(times[key]) {
			times[key] = at
		}
	}
	if len(times) == 0 {
		return limits
	}

	hadLast := false
	for _, w := range limits.Windows {
		if w.Provider == "claude" && w.Last {
			hadLast = true
			break
		}
	}

	for i := range limits.Windows {
		w := &limits.Windows[i]
		// 卡片自身已有时间时不覆盖。
		if !w.LastServedAt.IsZero() {
			continue
		}
		at, ok := times[servedKey{provider: w.Provider, kind: w.Kind, user: w.User}]
		if ok {
			w.LastServedAt = at
		}
	}
	if hadLast {
		return limits
	}

	// 只在 claude 卡片里挑最近应答的账号；最新的时间有并列时无法确定是哪个账号，不标 Last。
	latestUser := ""
	var latestAt time.Time
	tied := false
	for _, w := range limits.Windows {
		if w.Provider != "claude" || w.User == "" || w.LastServedAt.IsZero() {
			continue
		}
		switch {
		case latestUser == "" || w.LastServedAt.After(latestAt):
			latestUser, latestAt = w.User, w.LastServedAt
			tied = false
		case w.User != latestUser && w.LastServedAt.Equal(latestAt):
			tied = true
		}
	}
	if latestUser == "" || tied {
		return limits
	}
	for i := range limits.Windows {
		w := &limits.Windows[i]
		if w.Provider == "claude" && w.User == latestUser {
			w.Last = true
		}
	}
	return limits
}

// gatewayTime 解析卡片里的 RFC3339 时间字段；缺失或无法解析时返回零值（表示未知）。
func gatewayTime(raw json.RawMessage) time.Time {
	if t := parseResetTime(jsonText(raw)); t != nil {
		return *t
	}
	return time.Time{}
}

// gatewayWindow 把网关窗口名映射到现有的 session / weekly 窗口，其余保留原名。
func gatewayWindow(name string) (id, label string) {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "5 hour"):
		return "session", "5-hour limit"
	case strings.Contains(lower, "7 day"):
		return "weekly_all", "Weekly · all models"
	default:
		return "gateway", name
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
