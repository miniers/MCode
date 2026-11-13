package claude

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
)

// PlanLimits 包含 Claude 账号的订阅方案与额度窗口。
type PlanLimits struct {
	Windows  []PlanLimitWindow
	PlanType string
	// Source 是额度来源：经 Magpie 网关读到时为 "magpie"，OAuth 直连时为空。
	Source string
}

// PlanLimitWindow 表示单个额度窗口。
type PlanLimitWindow struct {
	// ID 标识窗口来源：session、weekly_all、five_hour、seven_day 或 extra-usage。
	ID string
	// Account 是所属账号（网关多卡片时的别名或邮箱），为空表示单账号。
	Account     string
	Label       string
	UsedPercent float64
	ResetsAt    *time.Time
	// Provider 是卡片在 Magpie 网关里的 provider id（claude、claude-tui 等），OAuth 直连时为空。
	Provider string
	// User 是卡片对应的账号（订阅账号通常是邮箱，插件卡片是 login），
	// 用于和 Magpie 的 usage/requests 记录里的 providerAccount 对齐；OAuth 直连时为空。
	User string
	// Kind 是卡片的种类：subscription、plan 或 balance。
	Kind string
	// Last 表示这张卡片是 Magpie 记录里最近一次应答请求的账号。
	Last bool
	// LastServedAt 是该账号最近一次经网关应答请求的时间，零值表示未知（最近 30 天未使用）。
	LastServedAt time.Time
	// ReadAt 是卡片额度被读取的时间，零值表示未知。
	ReadAt time.Time
}

// PlanLimitsURL Claude Code OAuth 用量接口地址，测试中可替换。
var PlanLimitsURL = "https://api.anthropic.com/api/oauth/usage"

// planLimitsHTTPClient 执行用量请求，测试中可替换。
var planLimitsHTTPClient = &http.Client{Timeout: 15 * time.Second}

// planLimitsEnv 读取登录环境，测试中可替换。
var planLimitsEnv = loginenv.Environ

// securityExecutable 读取 macOS 钥匙串的 security 可执行文件路径，测试中可替换。
var securityExecutable = "/usr/bin/security"

// keychainLogin 读取 macOS 钥匙串中的登录凭据，测试中可替换，避免触碰真实钥匙串。
var keychainLogin = readLoginFromKeychain

const planLimitsCacheTTL = 60 * time.Second

var (
	planLimitsMu       sync.RWMutex
	cachedPlanLimits   PlanLimits
	cachedPlanLimitsAt time.Time
)

// ReadPlanLimits 读取 Claude 账号的方案限额用量：
// 优先读 Magpie 网关（配置了网关就只读网关，失败直接报错，不回退 OAuth）；
// 上游不是 Magpie 时，再读本地凭据（.credentials.json 或 macOS 钥匙串）并请求 Claude Code 的 OAuth 用量接口。
// 结果在内存中缓存 60 秒，避免频繁读取凭据与网络请求。
func ReadPlanLimits(ctx context.Context) (PlanLimits, error) {
	planLimitsMu.RLock()
	if !cachedPlanLimitsAt.IsZero() && time.Since(cachedPlanLimitsAt) < planLimitsCacheTTL {
		res := clonePlanLimits(cachedPlanLimits)
		planLimitsMu.RUnlock()
		return res, nil
	}
	planLimitsMu.RUnlock()

	planLimitsMu.Lock()
	defer planLimitsMu.Unlock()
	// 双重检查
	if !cachedPlanLimitsAt.IsZero() && time.Since(cachedPlanLimitsAt) < planLimitsCacheTTL {
		return clonePlanLimits(cachedPlanLimits), nil
	}

	gatewayLimits, isMagpie, gatewayErr := readGatewayPlanLimits(ctx)
	if isMagpie {
		// 配置了 Magpie 网关就只读网关：失败直接返回网关错误，不回退 OAuth。
		if gatewayErr != nil {
			return PlanLimits{}, gatewayErr
		}
		cachedPlanLimits = gatewayLimits
		cachedPlanLimitsAt = time.Now()
		return clonePlanLimits(gatewayLimits), nil
	}

	env := append(slices.Clone(planLimitsEnv()), "CLAUDE_CONFIG_DIR="+configDirectory())
	if UsesThirdPartyAPI(env) {
		return PlanLimits{}, fmt.Errorf("claude: 第三方 API 不支持额度查询")
	}

	login, err := readLogin()
	if err != nil {
		return PlanLimits{}, err
	}

	raw, err := fetchUsage(ctx, login.accessToken)
	if err != nil {
		return PlanLimits{}, err
	}

	limits, err := ParsePlanLimits(raw)
	if err != nil {
		return PlanLimits{}, err
	}
	limits.PlanType = planType(login.subscriptionType, login.rateLimitTier)

	cachedPlanLimits = limits
	cachedPlanLimitsAt = time.Now()
	return clonePlanLimits(limits), nil
}

// ResetPlanLimitsCache 清除限额内存缓存，供单元测试使用。
func ResetPlanLimitsCache() {
	planLimitsMu.Lock()
	cachedPlanLimits = PlanLimits{}
	cachedPlanLimitsAt = time.Time{}
	planLimitsMu.Unlock()
}

// 用量接口返回的 JSON 结构。
type usageResponse struct {
	Limits []usageLimit `json:"limits"`
	// FiveHour / SevenDay 是接口的顶层窗口字段，新接口在 Limits 为空时仍返回它们。
	FiveHour   *usageWindow `json:"five_hour"`
	SevenDay   *usageWindow `json:"seven_day"`
	ExtraUsage *usageExtra  `json:"extra_usage"`
}

// usageLimit 对应 limits 数组中的一行（5 小时、每周、按模型）。
type usageLimit struct {
	Kind     string
	Percent  *float64
	Group    *string
	Scope    *usageScope
	ResetsAt *string
}

type usageScope struct {
	Model struct {
		DisplayName *string
	}
}

type usageWindow struct {
	Utilization *float64
	ResetsAt    *string
}

type usageExtra struct {
	IsEnabled   *bool
	Utilization *float64
}

// jsonFloat 读取数值字段，兼容接口可能返回数字或数字字符串两种写法；
// 字段缺失、null 或类型不符时返回 nil（按缺失处理），不中断整个解析。
func jsonFloat(raw json.RawMessage) *float64 {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return &f
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if v, err := strconv.ParseFloat(strings.TrimSpace(text), 64); err == nil {
			return &v
		}
	}
	return nil
}

// jsonText 读取字符串字段；字段缺失、null 或类型不符时返回 nil。
func jsonText(raw json.RawMessage) *string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return nil
	}
	return &text
}

// jsonBool 读取布尔字段；字段缺失、null 或类型不符时返回 nil。
func jsonBool(raw json.RawMessage) *bool {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	return &value
}

// UnmarshalJSON 按以下字段名解析：kind/percent/group/scope.model.display_name/resets_at。
func (l *usageLimit) UnmarshalJSON(raw []byte) error {
	var m struct {
		Kind     json.RawMessage `json:"kind"`
		Percent  json.RawMessage `json:"percent"`
		Group    json.RawMessage `json:"group"`
		ResetsAt json.RawMessage `json:"resets_at"`
		Scope    *struct {
			Model struct {
				DisplayName json.RawMessage `json:"display_name"`
			} `json:"model"`
		} `json:"scope"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	if kind := jsonText(m.Kind); kind != nil {
		l.Kind = *kind
	}
	l.Percent = jsonFloat(m.Percent)
	l.Group = jsonText(m.Group)
	l.ResetsAt = jsonText(m.ResetsAt)
	if m.Scope != nil {
		l.Scope = &usageScope{}
		l.Scope.Model.DisplayName = jsonText(m.Scope.Model.DisplayName)
	}
	return nil
}

// UnmarshalJSON 按顶层窗口字段解析：utilization 与 resets_at。
func (w *usageWindow) UnmarshalJSON(raw []byte) error {
	var m struct {
		Utilization json.RawMessage `json:"utilization"`
		ResetsAt    json.RawMessage `json:"resets_at"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	w.Utilization = jsonFloat(m.Utilization)
	w.ResetsAt = jsonText(m.ResetsAt)
	return nil
}

// UnmarshalJSON 按额外用量字段解析：is_enabled 与 utilization。
func (e *usageExtra) UnmarshalJSON(raw []byte) error {
	var m struct {
		IsEnabled   json.RawMessage `json:"is_enabled"`
		Utilization json.RawMessage `json:"utilization"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	e.IsEnabled = jsonBool(m.IsEnabled)
	e.Utilization = jsonFloat(m.Utilization)
	return nil
}

// ParsePlanLimits 解析 Claude OAuth 用量接口的响应体。
// 优先读 limits 数组（session、weekly_all 与按模型行），
// limits 为空或缺失时整个回退到顶层 five_hour 与 seven_day，额外用量开启时追加一行。
func ParsePlanLimits(raw []byte) (PlanLimits, error) {
	if len(raw) == 0 {
		return PlanLimits{}, fmt.Errorf("claude: 解析限额: 响应为空")
	}
	var resp usageResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return PlanLimits{}, fmt.Errorf("claude: 解析限额: %w", err)
	}

	windows := make([]PlanLimitWindow, 0, len(resp.Limits))
	for _, limit := range resp.Limits {
		if limit.Percent == nil {
			continue
		}
		var id, label string
		switch limit.Kind {
		case "session":
			id, label = "session", "5-hour limit"
		case "weekly_all":
			id, label = "weekly_all", "Weekly · all models"
		default:
			group := limit.Group
			if group == nil || *group == "" {
				group = strPtr("Usage")
			}
			label = titleCase(*group)
			if limit.Scope != nil && limit.Scope.Model.DisplayName != nil && *limit.Scope.Model.DisplayName != "" {
				label += " · " + *limit.Scope.Model.DisplayName
			}
			id = limit.Kind
			if id == "" {
				id = "limit"
			}
		}
		windows = append(windows, PlanLimitWindow{
			ID:          id,
			Label:       label,
			UsedPercent: *limit.Percent,
			ResetsAt:    parseResetTime(limit.ResetsAt),
		})
	}
	if len(windows) == 0 {
		// limits 为空或缺失（或没有任何有效的 percent 行）时，回退到顶层的
		// five_hour 与 seven_day。
		if w := topLevelWindow(resp.FiveHour, "five_hour", "5-hour limit"); w != nil {
			windows = append(windows, *w)
		}
		if w := topLevelWindow(resp.SevenDay, "seven_day", "Weekly · all models"); w != nil {
			windows = append(windows, *w)
		}
	}
	if resp.ExtraUsage != nil && resp.ExtraUsage.IsEnabled != nil && *resp.ExtraUsage.IsEnabled && resp.ExtraUsage.Utilization != nil {
		windows = append(windows, PlanLimitWindow{
			ID:          "extra-usage",
			Label:       "Extra usage",
			UsedPercent: *resp.ExtraUsage.Utilization,
		})
	}

	return PlanLimits{Windows: windows}, nil
}

func topLevelWindow(w *usageWindow, id, label string) *PlanLimitWindow {
	if w == nil || w.Utilization == nil {
		return nil
	}
	return &PlanLimitWindow{
		ID:          id,
		Label:       label,
		UsedPercent: *w.Utilization,
		ResetsAt:    parseResetTime(w.ResetsAt),
	}
}

// parseResetTime 解析重置时间：接口写微秒时间戳（如 2026-10-18T12:00:00.123456Z），
// ISO 8601 解析器不认小数部分，先剔除。
func parseResetTime(text *string) *time.Time {
	if text == nil || strings.TrimSpace(*text) == "" {
		return nil
	}
	trimmed := stripFraction(*text)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z07:00"} {
		if t, err := time.Parse(layout, trimmed); err == nil {
			ut := t.UTC()
			return &ut
		}
	}
	return nil
}

// stripFraction 去掉时间戳中的小数秒，如 ".123456"。
func stripFraction(text string) string {
	start := strings.Index(text, ".")
	if start < 0 {
		return text
	}
	end := start + 1
	for end < len(text) && text[end] >= '0' && text[end] <= '9' {
		end++
	}
	return text[:start] + text[end:]
}

// claudeLogin Claude Code 存储的登录凭据。
type claudeLogin struct {
	accessToken      string
	subscriptionType string
	rateLimitTier    string
}

// planType 组合订阅方案与速率档位，如 "Max (20x)"。
func planType(subscriptionType, rateLimitTier string) string {
	if subscriptionType == "" {
		return ""
	}
	name := strings.ToUpper(subscriptionType[:1]) + subscriptionType[1:]
	if tier := rateLimitTierSuffix(rateLimitTier); tier != "" {
		name += " (" + tier + ")"
	}
	return name
}

// rateLimitTierSuffix 从 "default_claude_max_20x" 这类档位标识里取出 "20x"。
func rateLimitTierSuffix(tier string) string {
	for i := 0; i+1 < len(tier); i++ {
		if tier[i] < '0' || tier[i] > '9' {
			continue
		}
		j := i
		for j < len(tier) && tier[j] >= '0' && tier[j] <= '9' {
			j++
		}
		if j < len(tier) && (tier[j] == 'x' || tier[j] == 'X') {
			return tier[i : j+1]
		}
	}
	return ""
}

// readLogin 读取 Claude Code 登录凭据的位置：
// $CLAUDE_CONFIG_DIR/.credentials.json（未设置时用 ~/.claude/.credentials.json），
// 文件不存在时回退到 macOS 钥匙串。
func readLogin() (claudeLogin, error) {
	configDir := configDirectory()
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return claudeLogin{}, fmt.Errorf("claude: 解析用户主目录: %w", err)
		}
		configDir = filepath.Join(home, ".claude")
	}

	file := filepath.Join(configDir, ".credentials.json")
	if data, err := os.ReadFile(file); err == nil {
		if login, ok := parseLogin(data); ok {
			return login, nil
		}
	}
	if login, ok := keychainLogin(configDir); ok {
		return login, nil
	}
	return claudeLogin{}, fmt.Errorf("claude: 未找到 %s 中的登录凭据", file)
}

// readLoginFromKeychain 读取 macOS 钥匙串中的 Claude Code 登录；
// 配置目录被搬迁时，钥匙串条目的服务名后带该目录 SHA-256 的前 8 位十六进制。
func readLoginFromKeychain(configDir string) (claudeLogin, bool) {
	if runtime.GOOS != "darwin" {
		return claudeLogin{}, false
	}
	service := "Claude Code-credentials"
	if configDir != "" {
		sum := sha256.Sum256([]byte(configDir))
		service += "-" + hex.EncodeToString(sum[:])[:8]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := procattr.HideWindow(exec.CommandContext(ctx, securityExecutable, "find-generic-password", "-s", service, "-w")).Output()
	if err != nil {
		return claudeLogin{}, false
	}
	return parseLogin([]byte(strings.TrimSpace(string(out))))
}

// parseLogin 解析凭据文件：token 位于 claudeAiOauth.accessToken。
func parseLogin(data []byte) (claudeLogin, bool) {
	var root struct {
		ClaudeAiOauth struct {
			AccessToken      string `json:"accessToken"`
			SubscriptionType string `json:"subscriptionType"`
			RateLimitTier    string `json:"rateLimitTier"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return claudeLogin{}, false
	}
	if root.ClaudeAiOauth.AccessToken == "" {
		return claudeLogin{}, false
	}
	return claudeLogin{
		accessToken:      root.ClaudeAiOauth.AccessToken,
		subscriptionType: root.ClaudeAiOauth.SubscriptionType,
		rateLimitTier:    root.ClaudeAiOauth.RateLimitTier,
	}, true
}

// fetchUsage 请求 Claude Code 的 OAuth 用量接口。
func fetchUsage(ctx context.Context, token string) ([]byte, error) {
	client := planLimitsHTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, PlanLimitsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("claude: 构造用量请求: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("claude: 请求用量接口: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("claude: 读取用量响应: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("claude: 用量接口返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func clonePlanLimits(src PlanLimits) PlanLimits {
	res := PlanLimits{
		PlanType: src.PlanType,
		Source:   src.Source,
		Windows:  make([]PlanLimitWindow, len(src.Windows)),
	}
	for i, w := range src.Windows {
		res.Windows[i] = w
		if w.ResetsAt != nil {
			tCopy := *w.ResetsAt
			res.Windows[i].ResetsAt = &tCopy
		}
	}
	return res
}

func strPtr(s string) *string {
	return &s
}

// titleCase 将 group 标识首字母大写，如 "extra" → "Extra"。
func titleCase(s string) string {
	if s == "" {
		return ""
	}
	if s[0] >= 'a' && s[0] <= 'z' {
		return string(s[0]-'a'+'A') + s[1:]
	}
	return s
}
