// Package magpie 读取 magpie 网关的模型目录与额度，并把勾选的模型按 pi 的
// models.json 条目形状交给 pi 里的 mcode 扩展。
//
// pi 侧的 provider 由环境变量 MCODE_PI_MAGPIE 注入：变量里是一段 JSON 数组
// （[]PiProvider），pi 里的 mcode 扩展读出它并逐个调用 pi.registerProvider。
// 数组里每个 provider 的 models 条目的 name 已经是 ShortName 的结果。
package magpie

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"mcode/internal/apisource"
	"mcode/internal/config"
	"mcode/internal/providers/readhttp"
)

// PiEnvVar 是 mcode 把 pi provider 配置交给 pi 内 mcode 扩展的环境变量名。
const PiEnvVar = "MCODE_PI_MAGPIE"

const (
	// modelsTimeout 是读取 /v1/models 的超时。
	modelsTimeout = 15 * time.Second
	// quotasTimeout 是读取 /v1/magpie/quotas 的超时。
	quotasTimeout = 12 * time.Second
	// quotasRefreshTimeout 是让网关重读厂商额度的超时：网关内部最多等 30 秒，
	// 客户端留一点余量，避免把仍在进行的刷新提前掐断。
	quotasRefreshTimeout = 35 * time.Second
	// modelsCacheTTL 是模型列表在内存里的缓存时长。
	modelsCacheTTL = 5 * time.Minute
	// modelsFailureTTL 是拉取失败结果的缓存时长：端点故障时不要每次调用都发一遍请求。
	modelsFailureTTL = 30 * time.Second
	// errorBodyLimit 是错误信息里带回的响应体字节数。
	errorBodyLimit = 200
)

var (
	// modelsHTTPClient 是读取模型目录的专属客户端：长期复用、连接池独立，
	// 连接被服务端中途重置时由 readhttp 换池重试一次。
	modelsHTTPClient = readhttp.NewClient(&http.Client{Timeout: modelsTimeout})
	// quotasHTTPClient 是读取额度的专属客户端，与模型目录互不影响。
	quotasHTTPClient = readhttp.NewClient(&http.Client{Timeout: quotasTimeout})
	// quotasRefreshHTTPClient 是刷新额度的专属客户端：刷新要等网关向厂商读取，
	// 比普通读取慢得多。
	quotasRefreshHTTPClient = readhttp.NewClient(&http.Client{Timeout: quotasRefreshTimeout})
)

// 响应里拿不到的元数据用的默认值。
const (
	defaultContextWindow = 200000
	defaultMaxTokens     = 32000
)

// piLevels 是 pi 支持的思考档位，与 pi 的 EXTENDED_THINKING_LEVELS 一致。
var piLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// Cost 是模型的调用价格，单价为 USD / 百万 token（与网关的 magpie_price
// 以及 pi 的 models.json 一致），字段与 pi 的 models.json 同名字段一致。
type Cost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// USD 按各类 token 数算出调用费用（美元）。单价是每百万 token，
// 四项乘积相加后除以 1_000_000。input 是未命中缓存的输入 token，
// 不包含 cacheRead 与 cacheWrite；单价为 0 的部分不计费。
func (c Cost) USD(input, output, cacheRead, cacheWrite int64) float64 {
	return (float64(input)*c.Input + float64(output)*c.Output +
		float64(cacheRead)*c.CacheRead + float64(cacheWrite)*c.CacheWrite) / 1_000_000
}

// Model 是 magpie 的一个模型，字段与 pi 的 models.json 模型条目一一对应。
type Model struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Reasoning 表示 pi 可以为该模型提供思考档位。
	Reasoning bool `json:"reasoning"`
	// Input 是该模型接受的输入种类，pi 只认 text 与 image。
	Input         []string `json:"input"`
	ContextWindow int      `json:"contextWindow"`
	MaxTokens     int      `json:"maxTokens"`
	// Cost 为 nil 表示价格未知（网关默认不报价格）。
	Cost *Cost `json:"cost,omitempty"`
	// ThinkingLevelMap 把 pi 的思考档位映到模型自己的档位，nil 表示让 pi 用默认值。
	ThinkingLevelMap map[string]*string `json:"thinkingLevelMap,omitempty"`
	// Levels 是网关给出的该模型档位名，保持网关顺序、统一小写；没有档位时为 nil。
	// 它只供界面填模型目录用，不写进 pi 的 models.json。
	Levels []string `json:"-"`
}

// PiProvider 是一个 magpie 端点作为 pi provider 的配置，供 pi 内的 mcode
// 扩展调用 pi.registerProvider 时使用。
type PiProvider struct {
	// Provider 是 pi 里的 provider id，见 ProviderID。
	Provider string `json:"provider"`
	// Endpoint 是配置里的端点名，仅用于显示。
	Endpoint string  `json:"endpoint"`
	BaseURL  string  `json:"baseUrl"`
	APIKey   string  `json:"apiKey"`
	Models   []Model `json:"models"`
}

// PiEnvJSON 把各端点的 pi provider 编码成 MCODE_PI_MAGPIE 的取值。
// 没有端点时返回空串，表示不注入任何 provider。
func PiEnvJSON(providers []PiProvider) (string, error) {
	if len(providers) == 0 {
		return "", nil
	}
	b, err := json.Marshal(providers)
	if err != nil {
		return "", fmt.Errorf("magpie: 编码 pi provider 配置失败: %w", err)
	}
	return string(b), nil
}

// modelsCache 是模型列表的内存缓存，按 baseURL + apiKey 区分。
var modelsCache = struct {
	sync.Mutex
	entries map[string]modelsCacheEntry
}{entries: map[string]modelsCacheEntry{}}

// modelsCacheEntry 是一条缓存：模型列表（失败时为 nil）与写入时间。
type modelsCacheEntry struct {
	models []Model
	// err 非 nil 表示这是一条失败缓存，值为上次拉取失败的错误。
	err     error
	written time.Time
}

// entryTTL 按条目类型给出缓存时长：失败条目只留 modelsFailureTTL。
func entryTTL(e modelsCacheEntry) time.Duration {
	if e.err != nil {
		return modelsFailureTTL
	}
	return modelsCacheTTL
}

// effort 是模型自己的一个思考档位，/v1/models 的 supported_reasoning_levels。
type effort struct {
	Effort string `json:"effort"`
}

// modalities 是模型接受的输入、输出种类，/v1/models 的 modalities。
type modalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

// price 是网关报的价格，字段名是 snake_case（/v1/models 的 magpie_price）。
type price struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

// modelRow 是 /v1/models 的一行，只解码用得到的字段：magpie 的条目比
// OpenAI 标准的多（对照 magpie 的 internal/gateway/gateway.go 的 modelObject）。
type modelRow struct {
	ID string `json:"id"`
	// Name 是模型自己的名字。
	Name string `json:"display_name"`
	// Label 是 magpie 给各客户端列表用的名字，"模型名 · provider 名"。
	Label string `json:"magpie_label"`
	// Reasoning 缺省（nil）时按支持思考处理。
	Reasoning *bool `json:"reasoning"`
	// Efforts 是该模型自己的思考档位。
	Efforts []effort `json:"supported_reasoning_levels"`
	// 上下文窗口以三种名字给出，值相同。
	ContextWindow   int `json:"context_window"`
	ContextLength   int `json:"context_length"`
	MaxInputTokens  int `json:"max_input_tokens"`
	MaxOutputTokens int `json:"max_output_tokens"`
	// Modalities 只在模型有额外种类（音频、pdf 等）时出现。
	Modalities *modalities `json:"modalities"`
	// Price 只在网关被要求报价格时出现（X-Magpie-Prices 头）。
	Price *price `json:"magpie_price"`
}

// model 把一行响应转成 Model，响应里没有的元数据用默认值。
func (row modelRow) model(id string) Model {
	m := Model{
		ID:            id,
		Name:          firstNonEmpty(row.Label, row.Name),
		Reasoning:     row.Reasoning == nil || *row.Reasoning,
		Input:         piInputs(row.Modalities),
		ContextWindow: firstPositive(row.ContextWindow, row.ContextLength, row.MaxInputTokens, defaultContextWindow),
		MaxTokens:     firstPositive(row.MaxOutputTokens, defaultMaxTokens),
	}
	if row.Price != nil {
		m.Cost = &Cost{Input: row.Price.Input, Output: row.Price.Output, CacheRead: row.Price.CacheRead, CacheWrite: row.Price.CacheWrite}
	}
	m.ThinkingLevelMap = thinkingLevels(row.Efforts)
	m.Levels = effortNames(row.Efforts)
	return m
}

// effortNames 把网关给的档位名规范成小写、去空白，丢掉空串，保持网关顺序。
func effortNames(efforts []effort) []string {
	var names []string
	for _, e := range efforts {
		name := strings.ToLower(strings.TrimSpace(e.Effort))
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	return names
}

// thinkingLevels 忽略大小写和首尾空白匹配档位，映射值保留网关首次提供的原文（去除首尾空白）。
// pi 的 off 对应网关的 none，不支持的档位置 nil；没有有效档位时返回 nil。
func thinkingLevels(efforts []effort) map[string]*string {
	if len(efforts) == 0 {
		return nil
	}
	names := make(map[string]string, len(efforts))
	for _, e := range efforts {
		original := strings.TrimSpace(e.Effort)
		name := strings.ToLower(original)
		if name == "" {
			continue
		}
		if _, exists := names[name]; !exists {
			names[name] = original
		}
	}
	if len(names) == 0 {
		return nil
	}
	levels := make(map[string]*string, len(piLevels))
	for _, level := range piLevels {
		own := level
		if level == "off" {
			own = "none"
		}
		levels[level] = nil
		if mapped, ok := names[own]; ok {
			levels[level] = &mapped
		}
	}
	return levels
}

// piInputs 是模型接受的输入里 pi 认的两种；一个都没有时按文本加图片处理。
func piInputs(modes *modalities) []string {
	if modes == nil {
		return []string{"text", "image"}
	}
	var inputs []string
	for _, kind := range modes.Input {
		if kind != "text" && kind != "image" {
			continue
		}
		if !slices.Contains(inputs, kind) {
			inputs = append(inputs, kind)
		}
	}
	if len(inputs) == 0 {
		return []string{"text", "image"}
	}
	return inputs
}

// FetchModels 读取 magpie 的模型目录，成功结果按 baseURL + apiKey 缓存 5 分钟，
// 失败结果缓存 30 秒，避免端点故障时反复请求。请求固定带价格头（常量），
// 不影响缓存键。返回的切片是副本，调用方可以随意改动。
func FetchModels(ctx context.Context, baseURL, apiKey string) ([]Model, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	key := strings.TrimSpace(apiKey)
	if base == "" {
		return nil, errors.New("magpie: 未配置端点地址")
	}
	cacheKey := base + "\x00" + key
	now := time.Now()
	if models, cachedErr, ok := cachedModels(cacheKey, now); ok {
		if cachedErr != nil {
			return nil, cachedErr
		}
		return models, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("magpie: 创建模型列表请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	// 网关只检查该头非空，值无含义；与 magpie 自己的客户端一样用 1。
	req.Header.Set("X-Magpie-Prices", "1")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := modelsHTTPClient.Do(req)
	if err != nil {
		failure := fmt.Errorf("magpie: 请求模型列表失败: %w", err)
		storeModelsFailure(ctx, cacheKey, failure, now)
		return nil, failure
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		failure := fmt.Errorf("magpie: 读取模型列表响应失败: %w", err)
		storeModelsFailure(ctx, cacheKey, failure, now)
		return nil, failure
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		failure := fmt.Errorf("magpie: 模型列表返回 %d: %s", resp.StatusCode, snippet(body))
		storeModelsFailure(ctx, cacheKey, failure, now)
		return nil, failure
	}
	var payload struct {
		Data []modelRow `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		failure := fmt.Errorf("magpie: 解析模型列表失败: %w", err)
		storeModelsFailure(ctx, cacheKey, failure, now)
		return nil, failure
	}
	models := make([]Model, 0, len(payload.Data))
	for _, row := range payload.Data {
		if id := strings.TrimSpace(row.ID); id != "" {
			models = append(models, row.model(id))
		}
	}
	storeModels(cacheKey, models, now)
	return models, nil
}

// CachedModel 从模型列表缓存里取一个模型，不会发网络请求：缓存未命中、缓存的是
// 失败记录或没有该 ID 的模型时返回零值与 false。返回的是副本，调用方可以随意改动。
func CachedModel(baseURL, apiKey, id string) (Model, bool) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	key := strings.TrimSpace(apiKey)
	models, cachedErr, ok := cachedModels(base+"\x00"+key, time.Now())
	if !ok || cachedErr != nil {
		return Model{}, false
	}
	for _, m := range models {
		if m.ID == id {
			return cloneModel(m), true
		}
	}
	return Model{}, false
}

// CachedModels 从模型列表缓存里取整份列表，不会发网络请求：缓存未命中或缓存的是
// 失败记录时返回 nil 与 false。返回的是副本，调用方可以随意改动。
func CachedModels(baseURL, apiKey string) ([]Model, bool) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	key := strings.TrimSpace(apiKey)
	models, cachedErr, ok := cachedModels(base+"\x00"+key, time.Now())
	if !ok || cachedErr != nil {
		return nil, false
	}
	return models, true
}

// cachedModels 返回未过期的缓存，没有则返回 false。命中的是失败缓存时
// models 为 nil、err 为上次的错误，调用方直接把它还给调用方。
func cachedModels(key string, now time.Time) ([]Model, error, bool) {
	modelsCache.Lock()
	defer modelsCache.Unlock()
	e, ok := modelsCache.entries[key]
	if !ok {
		return nil, nil, false
	}
	if now.Sub(e.written) >= entryTTL(e) {
		delete(modelsCache.entries, key)
		return nil, nil, false
	}
	if e.err != nil {
		return nil, e.err, true
	}
	return cloneModels(e.models), nil, true
}

// storeModelsFailure 记下一次拉取失败，modelsFailureTTL 内不再重复请求。
// 调用方自己取消或超时的结果不缓存：那是这次调用的处境，不是端点的状态，
// 缓存了会传染给别的调用方。
func storeModelsFailure(ctx context.Context, key string, err error, now time.Time) {
	if errors.Is(err, context.Canceled) || ctx.Err() != nil {
		return
	}
	storeModelsError(key, err, now)
}

// storeModelsError 记下失败缓存，超时按 modelsFailureTTL 计算。
// 未过期的成功条目不会被失败覆盖，否则端点偶发故障会把好好的模型列表换掉。
func storeModelsError(key string, err error, now time.Time) {
	if err == nil {
		return
	}
	modelsCache.Lock()
	defer modelsCache.Unlock()
	if e, ok := modelsCache.entries[key]; ok && e.err == nil && now.Sub(e.written) < modelsCacheTTL {
		return
	}
	for k, e := range modelsCache.entries {
		if now.Sub(e.written) >= entryTTL(e) {
			delete(modelsCache.entries, k)
		}
	}
	modelsCache.entries[key] = modelsCacheEntry{err: err, written: now}
}

// storeModels 记下模型列表，顺手清掉已过期的缓存条目。
func storeModels(key string, models []Model, now time.Time) {
	modelsCache.Lock()
	defer modelsCache.Unlock()
	for k, e := range modelsCache.entries {
		if now.Sub(e.written) >= entryTTL(e) {
			delete(modelsCache.entries, k)
		}
	}
	modelsCache.entries[key] = modelsCacheEntry{models: cloneModels(models), written: now}
}

// cloneModels 复制一遍模型列表，避免调用方改到缓存。
func cloneModels(models []Model) []Model {
	out := make([]Model, len(models))
	for i, m := range models {
		out[i] = cloneModel(m)
	}
	return out
}

// cloneModel 复制一个模型，Levels 单独复制一份，免得调用方改到缓存。
func cloneModel(m Model) Model {
	m.Levels = slices.Clone(m.Levels)
	return m
}

// ShortName 是模型在界面上的显示名：有名字时取第一个 " · " 之前的一段
// （magpie 的名字形如"模型名 · provider 名"），否则取 ID 最后一个 "/" 之后
// 的部分；路由组（auto/、group/）在末尾标上 (auto)。
func ShortName(m Model) string {
	name := strings.TrimSpace(m.Name)
	if name != "" {
		if head, _, ok := strings.Cut(name, " · "); ok {
			name = strings.TrimSpace(head)
		}
	} else {
		name = m.ID
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		name = strings.TrimSpace(name)
	}
	if isRoutingGroup(m.ID) && !strings.HasSuffix(name, " (auto)") {
		name += " (auto)"
	}
	return name
}

// isRoutingGroup 判断模型 ID 是不是路由组：以 auto/、group/ 开头，或含
// /auto/、/group/ 段。
func isRoutingGroup(id string) bool {
	if strings.HasPrefix(id, "auto/") || strings.HasPrefix(id, "group/") {
		return true
	}
	return strings.Contains(id, "/auto/") || strings.Contains(id, "/group/")
}

// ProviderID 把端点名转成 pi 的 provider id：小写，非 [a-z0-9-] 的字符换成
// "-"，连续的 "-" 合并，去首尾 "-"；避开内置来源，与 taken 冲突时依次加后缀。
func ProviderID(name string, taken map[string]bool) string {
	base := slug(name)
	if base == "" || base == "magpie" {
		base = "magpie-mcode"
	} else if base == "mcode" || slices.Contains(apisource.Kinds(config.Config{}), base) {
		base = "magpie-" + base
	}
	id := base
	for i := 2; taken[id]; i++ {
		id = base + "-" + strconv.Itoa(i)
	}
	return id
}

// slug 把任意端点名压成 provider id 可用的字符集合。
func slug(name string) string {
	var b strings.Builder
	dashed := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dashed = false
			continue
		}
		if !dashed && b.Len() > 0 {
			b.WriteByte('-')
			dashed = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// QuotaWindow 是 magpie 某张卡片的一个额度窗口。
type QuotaWindow struct {
	// ID 是 "<account>/<label>"。
	ID string
	// Account 是卡片的账户名：通常就是卡片的 provider id（如 cline），远端 magpie
	// 的卡片保留前缀；同一 provider 有多张卡片时用「 · 」接上标识（如 claude · 家里的号）。
	Account string
	// Label 是窗口名（如 "5 hours"、"week"）。
	Label string
	// Period 是由窗口名推断的周期：5h、1d、7d、30d，推断不出时为空。
	Period string
	// UsedPercent 是已用百分比，夹在 0–100。
	UsedPercent float64
	// ResetsAt 是窗口重置时间，响应里没有或解析不了时为零值。
	ResetsAt time.Time
	// Display 是展示文本（如 "$12.30 / $40"）。
	Display string
	// Text 为 true 表示该窗口仅显示文本，无用量占比。
	Text bool
	// Unlimited 为 true 表示不限量。
	Unlimited bool
	// Amount 是窗口在当前单位下的已用量。
	Amount float64
	// Limit 是窗口总量。
	Limit float64
	// Unit 是额度单位（如 "credits"）。
	Unit string
	// Balance 是剩余数量，nil 表示未提供。
	Balance *float64
	// RunsOutAt 是额度过期时间，响应里没有或解析不了时为零值。
	RunsOutAt time.Time
	// Provider 是卡片所属的 provider id（如 claude），不含 Account 里的「 · 」标识。
	Provider string
	// Kind 是卡片的种类：subscription（订阅账号）、plan（用 key 买的方案）
	// 或 balance（key 的余额）。
	Kind string
	// User 是卡片的账号（邮箱、login 等），方案卡没有账号时可能为空。
	User string
	// Alias 是用户给账号起的名字，没有时为空。
	Alias string
	// Plan 是卡片的方案名（如 "Max"），没有时为空。
	Plan string
	// ReadAt 是窗口或余额的实际读取时间，未知时为零值。
	ReadAt time.Time
	// LastServedAt 是该卡片最近一次经网关应答请求的时间；30 天内没有应答过时为零值。
	LastServedAt time.Time
	// Last 表示这张卡片是最近一次应答的那张。
	Last bool
}

// quotasResponse 是 /v1/magpie/quotas 的响应，对照 magpie 的
// internal/provider/quotas.go 的 Quota 与 QuotaSpan。同一 provider 可能有多张
// 卡片（如登了两个 Claude 账号），卡片之间用 Alias（用户起的名字）、User（账号）
// 区分；两者都没有时只能按返回顺序区分，见 ReadQuotas。
type quotasResponse struct {
	Data []struct {
		Provider     string `json:"provider"`
		Alias        string `json:"alias"`
		User         string `json:"user"`
		Kind         string `json:"kind"`
		Plan         string `json:"plan"`
		ReadAt       string `json:"readAt"`
		LastServedAt string `json:"lastServedAt"`
		Last         bool   `json:"last"`
		Windows      []struct {
			Unlimited bool     `json:"unlimited"`
			Name      string   `json:"name"`
			Used      float64  `json:"used"`
			ResetsAt  string   `json:"resetsAt"`
			ResetSecs int64    `json:"resetSecs"`
			Display   string   `json:"display"`
			Text      bool     `json:"text"`
			Amount    float64  `json:"amount"`
			Limit     float64  `json:"limit"`
			Unit      string   `json:"unit"`
			RunsOutAt string   `json:"runsOutAt"`
			RunsOut   float64  `json:"runsOut"`
			Balance   *float64 `json:"balance"`
		} `json:"windows"`
	} `json:"data"`
}

// AccountProvider 取额度账户（QuotaWindow.Account）所属的 provider：同一 provider 的
// 多张卡片时账户形如 "claude · <标识>"，取「 · 」前的一段；没有标识时是 "claude"。
func AccountProvider(account string) string {
	name, _, _ := strings.Cut(strings.TrimSpace(account), " · ")
	return name
}

// ReadQuotas 读取 magpie 各卡片的额度窗口：每张卡片的每个窗口一条，跳过
// provider 名带 "~"（远端 key 的方案卡）与没有窗口的卡片。
func ReadQuotas(ctx context.Context, baseURL, apiKey string) ([]QuotaWindow, error) {
	origin := strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(baseURL), "/"), "/v1")
	if origin == "" {
		return nil, errors.New("magpie: 未配置端点地址")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/v1/magpie/quotas", nil)
	if err != nil {
		return nil, fmt.Errorf("magpie: 创建额度请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if key := strings.TrimSpace(apiKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := quotasHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("magpie: 请求额度失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("magpie: 读取额度响应失败: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("magpie: 该 key 没有读取额度的权限（Quotas）: %d %s", resp.StatusCode, snippet(body))
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("magpie: 额度返回 %d: %s", resp.StatusCode, snippet(body))
	}
	var payload quotasResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("magpie: 解析额度响应失败: %w", err)
	}
	// 同一 provider 出现多张卡片就是多个账号（如两个 Claude 账号），先数一遍卡片数。
	cards := make(map[string]int)
	for _, card := range payload.Data {
		account := strings.TrimSpace(card.Provider)
		if account == "" || strings.Contains(account, "~") || len(card.Windows) == 0 {
			continue
		}
		cards[account]++
	}
	var out []QuotaWindow
	seen := make(map[string]int)   // provider 已出现几张卡片
	taken := make(map[string]bool) // 已用的账户名，避免标识相同或缺失时再合并
	usedID := make(map[string]bool)
	for _, card := range payload.Data {
		provider := strings.TrimSpace(card.Provider)
		if provider == "" || strings.Contains(provider, "~") {
			continue
		}
		seen[provider]++
		account := provider
		if cards[provider] > 1 {
			// 多张卡片：优先用户起的名字，其次账号，都没有时按返回顺序加序号。
			if id := strings.TrimSpace(card.Alias); id != "" {
				account = provider + " · " + id
			} else if id := strings.TrimSpace(card.User); id != "" {
				account = provider + " · " + id
			}
		}
		if taken[account] {
			// 标识重复或缺失时退回序号，保证两张卡片的账户名不同。
			for n := seen[provider]; taken[account]; n++ {
				account = fmt.Sprintf("%s #%d", provider, n)
			}
		}
		taken[account] = true
		for _, w := range card.Windows {
			label := strings.TrimSpace(w.Name)
			id := account + "/" + label
			if usedID[id] {
				for n := 2; usedID[id]; n++ {
					id = fmt.Sprintf("%s/%s #%d", account, label, n)
				}
			}
			usedID[id] = true
			resetsAt := parseTime(w.ResetsAt)
			if resetsAt.IsZero() && w.ResetSecs > 0 {
				resetsAt = time.Now().Add(time.Duration(w.ResetSecs) * time.Second)
			}
			out = append(out, QuotaWindow{
				ID:           id,
				Account:      account,
				Label:        label,
				Period:       periodOf(label),
				UsedPercent:  clamp(w.Used, 0, 100),
				ResetsAt:     resetsAt,
				Display:      w.Display,
				Text:         w.Text,
				Unlimited:    w.Unlimited,
				Amount:       w.Amount,
				Limit:        w.Limit,
				Unit:         w.Unit,
				Balance:      w.Balance,
				RunsOutAt:    parseTime(w.RunsOutAt),
				Provider:     provider,
				Kind:         strings.TrimSpace(card.Kind),
				User:         strings.TrimSpace(card.User),
				Alias:        strings.TrimSpace(card.Alias),
				Plan:         strings.TrimSpace(card.Plan),
				ReadAt:       parseTime(card.ReadAt),
				LastServedAt: parseTime(card.LastServedAt),
				Last:         card.Last,
			})
		}
	}
	return out, nil
}

// RefreshQuotas 让 magpie 网关重新向厂商读取额度卡片：provider 非空时只刷新该
// provider 的全部账号（包括非首选账号），为空时重读全部卡片。网关的刷新接口是 POST，
// 返回刷新后的卡片列表；这里只关心刷新是否成功，调用方随后用 ReadQuotas 取新数据。
func RefreshQuotas(ctx context.Context, baseURL, apiKey, provider string) error {
	return RefreshQuotasFor(ctx, baseURL, apiKey, provider, "")
}

// RefreshQuotasFor 与 RefreshQuotas 相同，但 user 非空时只重读该 provider 下的这一张
// 卡片（provider 为空时 user 不生效）。网关对同一 provider|user 有 30 秒去重。
func RefreshQuotasFor(ctx context.Context, baseURL, apiKey, provider, user string) error {
	origin := strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(baseURL), "/"), "/v1")
	if origin == "" {
		return errors.New("magpie: 未配置端点地址")
	}
	target := origin + "/v1/magpie/quotas/refresh"
	params := url.Values{}
	if name := strings.TrimSpace(provider); name != "" {
		params.Set("provider", name)
		if who := strings.TrimSpace(user); who != "" {
			params.Set("user", who)
		}
	}
	if query := params.Encode(); query != "" {
		target += "?" + query
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return fmt.Errorf("magpie: 创建额度刷新请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if key := strings.TrimSpace(apiKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := quotasRefreshHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("magpie: 请求额度刷新失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("magpie: 读取额度刷新响应失败: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("magpie: 该 key 没有刷新额度的权限（Quotas）: %d %s", resp.StatusCode, snippet(body))
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return fmt.Errorf("magpie: 刷新额度返回 %d: %s", resp.StatusCode, snippet(body))
	}
	return nil
}

// periodOf 由窗口名推断周期，推断不出时返回空串。
func periodOf(label string) string {
	name := strings.ToLower(label)
	switch {
	case strings.Contains(name, "5 hour"):
		return "5h"
	case strings.Contains(name, "week"), strings.Contains(name, "7 day"):
		return "7d"
	case strings.Contains(name, "month"), strings.Contains(name, "30 day"):
		return "30d"
	case strings.Contains(name, "1 day"), name == "day":
		return "1d"
	}
	return ""
}

// parseTime 解析 RFC3339 时间，解析不了时返回零值。
func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t
}

// clamp 把 v 夹在 lo 与 hi 之间。
func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// firstNonEmpty 返回第一个非空（TrimSpace 之后）的值。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// firstPositive 返回第一个正数。
func firstPositive(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

// snippet 截取响应体开头一段，用在错误信息里。
func snippet(body []byte) string {
	if len(body) > errorBodyLimit {
		body = body[:errorBodyLimit]
	}
	return strings.TrimSpace(strings.ToValidUTF8(string(body), ""))
}
