// Package credits 读取各 provider 的预付费余额与方案额度（订阅用量窗口），
// 实现为纯 Go、可测试的服务。
//
// 余额只覆盖 deepseek、commandcode 与 cline 三家；
// z.ai 没有余额端点，因此本包只提供它的方案额度窗口。
package credits

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrUnsupported 表示该 provider 没有对应的余额或方案额度读取实现。
	ErrUnsupported = errors.New("credits: 该 provider 不支持此读取")
	// ErrMissingKey 表示缺少读取所需的凭据（API key 或本地登录令牌）。
	ErrMissingKey = errors.New("credits: 缺少凭据")
)

// Provider 标识。
const (
	ProviderDeepSeek    = "deepseek"
	ProviderZai         = "zai"
	ProviderCline       = "cline"
	ProviderCommandCode = "commandcode"
	ProviderDroid       = "droid"
)

// Credits 是一个按量付费 provider 的预付费余额。
type Credits struct {
	// Provider 是产出该余额的 provider 标识。
	Provider string
	// Total 是还能花的钱。
	Total float64
	// Currency 是 provider 报出的货币代码（"USD"、"CNY"）；provider 不报时为空串。
	Currency string
	// ToppedUp / Granted / Monthly 分别是充值、赠送与订阅每月份额，provider 不拆分时为 nil。
	ToppedUp *float64
	Granted  *float64
	Monthly  *float64
	// Usable 为 false 表示 provider 说该账户已不能再被扣费，此后每个请求都会失败。
	Usable bool
	// ReadAt 是本次读取的时间。
	ReadAt time.Time
}

// AmountText 返回带货币符号的金额，如 "$12.34"、"¥5.00"。
func (c Credits) AmountText() string { return money(c.Total, c.Currency) }

// BreakdownText 返回拆分文案，如 "Monthly $10.00 · Topped up $2.00"；
// provider 只报一个数字时返回空串。
func (c Credits) BreakdownText() string {
	var parts []string
	if c.Monthly != nil && *c.Monthly > 0 {
		parts = append(parts, "Monthly "+money(*c.Monthly, c.Currency))
	}
	if c.ToppedUp != nil && *c.ToppedUp > 0 {
		parts = append(parts, "Topped up "+money(*c.ToppedUp, c.Currency))
	}
	if c.Granted != nil && *c.Granted > 0 {
		parts = append(parts, "Granted "+money(*c.Granted, c.Currency))
	}
	return strings.Join(parts, " · ")
}

// Depleted 表示不可用或余额已耗尽。
func (c Credits) Depleted() bool { return !c.Usable || c.Total <= 0 }

// Window 是一个订阅方案的使用窗口。
type Window struct {
	// ID 标识窗口来源（如 z.ai 的 credit-0 / tools-0、Droid 的 standard-fiveHour），
	// 上层按它识别 MCP tools 等辅助窗口，不再只看标题文本。
	ID string
	// Label 是窗口标题（如 "5-hour limit"、"Standard · Weekly"）。
	Label string
	// Period 是窗口周期标记（5h、1d、7d、30d）；provider 未给出周期时为空串。
	Period string
	// UsedPercent 是已用百分比，取值 0–100。
	UsedPercent float64
	// ResetsAt 是下一次重置时间；provider 未给出时为 time.Time 零值。
	ResetsAt time.Time
}

// windowPeriod 把周期分钟数写成短标记（5h、1d、7d、30d）；非整小时整天的周期返回空串。
// 与 codex 侧 formatWindowPeriod 的写法保持一致，UI 直接按标记选周期标签。
func windowPeriod(minutes int) string {
	switch minutes {
	case 300:
		return "5h"
	case 1_440:
		return "1d"
	case 10_080:
		return "7d"
	}
	if minutes >= 40_320 && minutes <= 44_640 {
		return "30d"
	}
	if minutes <= 0 {
		return ""
	}
	if minutes%1_440 == 0 {
		return fmt.Sprintf("%dd", minutes/1_440)
	}
	if minutes%60 == 0 {
		return fmt.Sprintf("%dh", minutes/60)
	}
	return ""
}

// Options 是一次读取的依赖与来源配置，测试时全部可替换。
type Options struct {
	// APIKey 是调用方直接给出的密钥，优先于环境变量与本地文件。
	APIKey string
	// BaseURL 覆盖默认端点地址；为空时用各家的默认地址。
	BaseURL string
	// HTTPClient 为空时使用带超时的默认 client。
	HTTPClient *http.Client
	// Env 是环境变量来源；为 nil 时读进程环境，非 nil 时只查这张表（测试用）。
	Env map[string]string
	// HomeDir 覆盖用户主目录；为空时用 os.UserHomeDir()。
	HomeDir string
	// Now 覆盖当前时间；为空时用 time.Now。
	Now func() time.Time
}

const defaultHTTPTimeout = 15 * time.Second

func (o Options) client() *http.Client {
	if o.HTTPClient != nil {
		return o.HTTPClient
	}
	return &http.Client{Timeout: defaultHTTPTimeout}
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o Options) home() string {
	if strings.TrimSpace(o.HomeDir) != "" {
		return strings.TrimSpace(o.HomeDir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// envValue 读取环境变量：Options.Env 非 nil 时只查它，否则查进程环境。
func (o Options) envValue(key string) string {
	if o.Env != nil {
		return o.Env[key]
	}
	return os.Getenv(key)
}

// baseURL 返回去掉尾部斜杠的 BaseURL，为空时返回 fallback。
func (o Options) baseURL(fallback string) string {
	if b := strings.TrimSpace(o.BaseURL); b != "" {
		return strings.TrimRight(b, "/")
	}
	return strings.TrimRight(fallback, "/")
}

// ExposesCredits 表示该 provider 是否报预付费余额：只有按量付费的三家报。
// z.ai 没有余额端点，因此不在此列。
func ExposesCredits(provider string) bool {
	switch normalizeProvider(provider) {
	case ProviderDeepSeek, ProviderCline, ProviderCommandCode:
		return true
	default:
		return false
	}
}

// Read 读取指定 provider 的预付费余额。
// 不支持的 provider 返回 ErrUnsupported；缺少凭据返回 ErrMissingKey。
func Read(ctx context.Context, provider string, opts Options) (Credits, error) {
	switch normalizeProvider(provider) {
	case ProviderDeepSeek:
		return readDeepSeek(ctx, opts)
	case ProviderCline:
		return readCline(ctx, opts)
	case ProviderCommandCode:
		return readCommandCode(ctx, opts)
	case ProviderZai:
		// z.ai 只有 Coding Plan 额度端点、没有余额端点，这里不发明端点。
		return Credits{}, fmt.Errorf("%w: zai", ErrUnsupported)
	default:
		return Credits{}, fmt.Errorf("%w: %s", ErrUnsupported, provider)
	}
}

// ReadPlanWindows 读取指定 provider 的方案使用窗口：
// zai（Coding Plan 额度）、cline（ClinePass）、droid（经 Factory API）。
// 不支持的 provider 返回 ErrUnsupported。
func ReadPlanWindows(ctx context.Context, provider string, opts Options) ([]Window, error) {
	switch normalizeProvider(provider) {
	case ProviderZai:
		return zaiPlanWindows(ctx, opts)
	case ProviderCline:
		return clinePlanWindows(ctx, opts)
	case ProviderDroid, "factory":
		return factoryPlanWindows(ctx, opts)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, provider)
	}
}

func normalizeProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

// currencyFormats 是 money() 需要的货币符号表，
// 纯 Go 侧用一张小表覆盖实际会出现的币种。fraction 是小数位数（日元、韩元为 0）。
var currencyFormats = map[string]struct {
	symbol   string
	fraction int
}{
	"USD": {"$", 2},
	"CNY": {"¥", 2},
	"RMB": {"¥", 2},
	"EUR": {"€", 2},
	"GBP": {"£", 2},
	"JPY": {"¥", 0},
	"KRW": {"₩", 0},
	"HKD": {"HK$", 2},
	"TWD": {"NT$", 2},
	"SGD": {"S$", 2},
	"AUD": {"A$", 2},
	"CAD": {"C$", 2},
	"CHF": {"CHF ", 2},
	"INR": {"₹", 2},
}

// money 给金额戴上 provider 报出的货币，
// 数字始终按英文格式（千分位 + 两位小数），未知币种回退成纯数字加原币种文本，
// 空币种只输出数字。
func money(value float64, currency string) string {
	code := strings.ToUpper(strings.TrimSpace(currency))
	if format, ok := currencyFormats[code]; ok {
		sign := ""
		if math.Signbit(value) {
			sign = "-"
		}
		return sign + format.symbol + formatNumber(math.Abs(value), format.fraction)
	}
	number := formatNumber(value, 2)
	if strings.TrimSpace(currency) == "" {
		return number
	}
	return number + " " + currency
}

// formatNumber 按英文格式渲染金额：千分位分隔 + 指定小数位。
func formatNumber(value float64, fraction int) string {
	sign := ""
	if math.Signbit(value) {
		sign = "-"
	}
	text := strconv.FormatFloat(math.Abs(value), 'f', fraction, 64)
	intPart := text
	fracPart := ""
	if dot := strings.IndexByte(text, '.'); dot >= 0 {
		intPart, fracPart = text[:dot], text[dot:]
	}
	return sign + groupDigits(intPart) + fracPart
}

// groupDigits 给整数部分插入千分位。
func groupDigits(digits string) string {
	if len(digits) <= 3 {
		return digits
	}
	var b strings.Builder
	head := len(digits) % 3
	if head > 0 {
		b.WriteString(digits[:head])
		b.WriteByte(',')
	}
	for i := head; i < len(digits); i += 3 {
		b.WriteString(digits[i : i+3])
		if i+3 < len(digits) {
			b.WriteByte(',')
		}
	}
	return b.String()
}

// getJSON 发起带 Bearer 认证的 GET 请求，返回状态码与响应体。
func getJSON(ctx context.Context, client *http.Client, url, token string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("credits: 构造请求: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("credits: 请求 %s: %w", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("credits: 读取 %s 响应: %w", url, err)
	}
	return resp.StatusCode, body, nil
}

// jsonRawObject 把一段 JSON 解成通用对象；失败或为空返回 nil。
func jsonRawObject(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

// jsonText 读取字符串字段；缺失、null 或类型不符时返回空串。
func jsonText(m map[string]json.RawMessage, key string) string {
	if m == nil {
		return ""
	}
	var text string
	if err := json.Unmarshal(m[key], &text); err != nil {
		return ""
	}
	return text
}

// jsonFloat 读取数值字段，兼容数字与数字字符串两种写法；缺失或非法时返回 nil。
func jsonFloat(m map[string]json.RawMessage, key string) *float64 {
	if m == nil {
		return nil
	}
	return parseFloatRaw(m[key])
}

func parseFloatRaw(raw json.RawMessage) *float64 {
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

// jsonBool 读取布尔字段；缺失、null 或类型不符时返回 nil。
func jsonBool(m map[string]json.RawMessage, key string) *bool {
	if m == nil {
		return nil
	}
	if len(m[key]) == 0 || string(m[key]) == "null" {
		return nil
	}
	var value bool
	if err := json.Unmarshal(m[key], &value); err != nil {
		return nil
	}
	return &value
}
