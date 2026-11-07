// Package cline 提供 Cline 的模型目录与价格读取：
// 模型目录来自包内置的初始列表（Cline 没有 HTTP 模型列表端点，它的模型
// 来自 ACP 探测加初始列表 seed），价格与上下文窗口来自 models.dev 的 cline-pass 条目。
package cline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"mcode/internal/providers/acp"
	"mcode/internal/providers/loginenv"
	"mcode/internal/providers/modelcache"
)

// ModelsDevURL 是 models.dev 模型目录的地址。
const ModelsDevURL = "https://models.dev/api.json"

// ModelsDevProviderKey 是 models.dev 里取 Cline 价格与上下文窗口所用的键：
const ModelsDevProviderKey = "cline-pass"

// Plan 是 Cline 的计费方式。
type Plan string

const (
	// PlanAPIKey 是按量付费（provider id 为 cline）。
	PlanAPIKey Plan = "cline"
	// PlanPass 是 ClinePass 订阅（provider id 为 cline-pass）。
	PlanPass Plan = "cline-pass"
)

// Model 是模型列表里的一行；价格字段先留 0，等 MergePricing 用 models.dev 补齐。
type Model struct {
	ID            string
	Name          string
	Detail        string
	Default       bool
	ContextWindow int
	InputPerMTok  float64
	OutputPerMTok float64
	// Secondary 表示该模型不属于当前登录渠道，默认收进“更多模型”。
	Secondary bool
}

// SeedModels 返回 Cline 的初始模型列表（每次调用返回新切片）。
func SeedModels() []Model {
	return []Model{
		{
			ID:      "cline-pass/deepseek-v4.1-flash",
			Name:    "DeepSeek V4.1 Flash",
			Detail:  "Smarter and more efficient, with 1M context window",
			Default: true,
		},
		{
			ID:     "cline-pass/deepseek-v4-pro",
			Name:   "DeepSeek V4 Pro",
			Detail: "Frontier reasoning and coding with 1M context window",
		},
		{
			ID:     "cline-pass/mimo-v2.6-flash",
			Name:   "MiMo-V2.6-Flash",
			Detail: "Mixture-of-Experts architecture with 309B total parameters",
		},
		{
			ID:     "cline-pass/glm-5.3",
			Name:   "GLM-5.3",
			Detail: "Z-AI's new top open-weights model",
		},
	}
}

// CatalogModels 返回初始列表并用 models.dev 的 cline-pass 价格补齐；
// 价格拉取失败时仍返回初始列表，同时返回错误。
func CatalogModels(ctx context.Context, hc *http.Client) ([]Model, error) {
	pricing, err := CachedPricing(ctx, hc, ModelsDevURL)
	if err != nil {
		return SeedModels(), err
	}
	return MergePricing(SeedModels(), pricing), nil
}

// Pricing 是 models.dev 的一个价格条目（美元/百万 token）与上下文窗口。
type Pricing struct {
	InputPerMTok  float64
	OutputPerMTok float64
	ContextWindow int
}

// ParsePricing 解析 models.dev 风格 JSON 里 cline-pass（找不到时退回 cline）的部分，
// 返回以模型 id 为键的表。
func ParsePricing(body []byte) (map[string]Pricing, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("cline: 解析模型价格: %w", err)
	}
	providerRaw := root[ModelsDevProviderKey]
	if providerRaw == nil {
		providerRaw = root[string(PlanAPIKey)]
	}
	if providerRaw == nil {
		return nil, fmt.Errorf("cline: 模型价格里没有 %s 条目", ModelsDevProviderKey)
	}
	var provider map[string]json.RawMessage
	if err := json.Unmarshal(providerRaw, &provider); err != nil {
		return nil, fmt.Errorf("cline: 解析 %s 条目: %w", ModelsDevProviderKey, err)
	}

	pricing := map[string]Pricing{}
	var models map[string]json.RawMessage
	if err := json.Unmarshal(provider["models"], &models); err != nil {
		return nil, fmt.Errorf("cline: 模型价格缺少 models: %w", err)
	}
	for key, modelRaw := range models {
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(modelRaw, &entry); err != nil {
			continue
		}
		id := firstText(entry, "id")
		if id == "" {
			id = key
		}
		var cost map[string]json.RawMessage
		if entry["cost"] != nil {
			_ = json.Unmarshal(entry["cost"], &cost)
		}
		var limit map[string]json.RawMessage
		if entry["limit"] != nil {
			_ = json.Unmarshal(entry["limit"], &limit)
		}
		pricing[id] = Pricing{
			InputPerMTok:  firstFloat(cost, "input"),
			OutputPerMTok: firstFloat(cost, "output"),
			ContextWindow: firstInt(limit, "context"),
		}
	}
	return pricing, nil
}

// FetchPricing 下载并解析 models.dev 目录；hc 为空时用默认客户端。
func FetchPricing(ctx context.Context, hc *http.Client, url string) (map[string]Pricing, error) {
	if url == "" {
		url = ModelsDevURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("cline: 构造价格请求: %w", err)
	}
	req.Header.Set("User-Agent", "mcode/1.0")
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cline: 请求模型价格: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cline: 读取模型价格: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cline: 读取模型价格: 接口返回 HTTP %d", resp.StatusCode)
	}
	return ParsePricing(body)
}

// pricingCacheTTL 是 FetchPricing 结果的内存缓存时长。
const pricingCacheTTL = 10 * time.Minute

var (
	pricingMu        sync.Mutex
	pricingCache     map[string]Pricing
	pricingFetchedAt time.Time
)

// CachedPricing 是带 10 分钟内存缓存的 FetchPricing；失败不写缓存。
func CachedPricing(ctx context.Context, hc *http.Client, url string) (map[string]Pricing, error) {
	pricingMu.Lock()
	if pricingCache != nil && time.Since(pricingFetchedAt) < pricingCacheTTL {
		cached := pricingCache
		pricingMu.Unlock()
		return cached, nil
	}
	pricingMu.Unlock()

	// 缓存未命中：不持锁抓取，避免慢请求挡住其他调用方。
	fetched, err := FetchPricing(ctx, hc, url)
	if err != nil {
		return nil, err
	}
	pricingMu.Lock()
	pricingCache = fetched
	pricingFetchedAt = time.Now()
	pricingMu.Unlock()
	return fetched, nil
}

// resetPricingCache 清空价格缓存，仅供测试使用。
func resetPricingCache() {
	pricingMu.Lock()
	pricingCache = nil
	pricingFetchedAt = time.Time{}
	pricingMu.Unlock()
}

// MergePricing 用价格表补齐模型里仍为 0 的字段，返回新切片；
// 先按模型 id 精确匹配，再按去掉 provider 前缀（如 cline-pass/）的最后一段匹配。
func MergePricing(models []Model, p map[string]Pricing) []Model {
	merged := make([]Model, len(models))
	copy(merged, models)
	if len(p) == 0 {
		return merged
	}
	for i := range merged {
		price, ok := p[merged[i].ID]
		if !ok {
			if leaf := lastPathComponent(merged[i].ID); leaf != "" {
				price, ok = p[leaf]
			}
		}
		if !ok {
			continue
		}
		if merged[i].ContextWindow == 0 {
			merged[i].ContextWindow = price.ContextWindow
		}
		if merged[i].InputPerMTok == 0 {
			merged[i].InputPerMTok = price.InputPerMTok
		}
		if merged[i].OutputPerMTok == 0 {
			merged[i].OutputPerMTok = price.OutputPerMTok
		}
	}
	return merged
}

// lastPathComponent 取 "cline-pass/deepseek-v4.1-flash" 的最后一段。
func lastPathComponent(value string) string {
	if index := strings.LastIndex(value, "/"); index >= 0 {
		return value[index+1:]
	}
	return value
}

// firstText 取若干候选字段里第一个非空字符串。
func firstText(entry map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		var value string
		if err := json.Unmarshal(entry[key], &value); err == nil && value != "" {
			return value
		}
	}
	return ""
}

// firstFloat 取若干候选字段里第一个可解析的数字。
func firstFloat(entry map[string]json.RawMessage, keys ...string) float64 {
	for _, key := range keys {
		var value float64
		if err := json.Unmarshal(entry[key], &value); err == nil {
			return value
		}
	}
	return 0
}

// firstInt 取若干候选字段里第一个可解析的整数。
func firstInt(entry map[string]json.RawMessage, keys ...string) int {
	for _, key := range keys {
		var value int
		if err := json.Unmarshal(entry[key], &value); err == nil {
			return value
		}
	}
	return 0
}

var modelCache modelcache.Cache[Model]

// CachedModels 返回 Cline 经 ACP 探测到的模型目录；
// 目录保留账号的全部渠道模型，不属于当前渠道的标为 Secondary 交给界面折叠。
// 尚未探测到时返回 SeedModels，并在后台起一次 `cline --acp` 会话读取目录
// （找不到 CLI、未登录或失败后 10 分钟内不再重试）。
func CachedModels() []Model {
	if models := modelCache.Get(fetchCachedModels); len(models) > 0 {
		return models
	}
	return SeedModels()
}

// RefreshModels 强制重新探测 Cline 的模型目录。
func RefreshModels(ctx context.Context, binary string) error {
	return modelCache.Refresh(ctx, func(ctx context.Context) ([]Model, error) {
		return fetchModelsWithBinary(ctx, binary)
	})
}

func fetchCachedModels(ctx context.Context) ([]Model, error) {
	return fetchModelsWithBinary(ctx, acp.DefaultBinary(acp.AgentCline))
}

func fetchModelsWithBinary(ctx context.Context, binary string) ([]Model, error) {
	if binary == "" {
		binary = acp.DefaultBinary(acp.AgentCline)
	}
	var models []Model
	bin, err := loginenv.LookPath(binary)
	if err == nil {
		var probed []acp.ProbedModel
		probed, err = acp.ProbeModels(ctx, acp.AgentCline, bin)
		models = probedModels(probed)
		if provider := acp.ClineProviderID(); provider != "" {
			models = markProviderModels(models, provider)
		}
	}
	return models, err
}

// markProviderModels 标记 id 不属于 provider（形如 cline-pass/…）的模型为次要模型：
// 账号走 ClinePass 订阅时，cline-free 等其他渠道的模型默认收进界面上的“更多模型”。
// 主模型排在前、次要模型排在后，各组内保持原顺序；没有任何主模型时原样返回，全部保持非次要。
// Default 只落在主模型上：原来的 Default 若是次要模型就清掉，主模型里没有 Default 时取第一个主模型。
func markProviderModels(models []Model, provider string) []Model {
	prefix := provider + "/"
	primary := make([]Model, 0, len(models))
	secondary := make([]Model, 0, len(models))
	for _, m := range models {
		if strings.HasPrefix(m.ID, prefix) {
			primary = append(primary, m)
			continue
		}
		m.Secondary = true
		secondary = append(secondary, m)
	}
	if len(primary) == 0 {
		return models
	}
	hasDefault := false
	for _, m := range primary {
		hasDefault = hasDefault || m.Default
	}
	for i := range secondary {
		secondary[i].Default = false
	}
	if !hasDefault {
		primary[0].Default = true
	}
	return append(primary, secondary...)
}

// probedModels 把 ACP 广告的模型转成目录行：Agent 当前选中的模型为默认，没有时取第一行。
func probedModels(probed []acp.ProbedModel) []Model {
	out := make([]Model, 0, len(probed))
	hasDefault := false
	for _, p := range probed {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			name = lastPathComponent(p.ID)
		}
		out = append(out, Model{ID: p.ID, Name: name, Detail: p.Description, Default: p.Current && !hasDefault})
		hasDefault = hasDefault || p.Current
	}
	if !hasDefault && len(out) > 0 {
		out[0].Default = true
	}
	return out
}
