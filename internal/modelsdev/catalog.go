// Package modelsdev 提供 models.dev 模型能力目录：推理档位、Fast 模式、上下文上限。
//
// 数据来源按优先级叠加：内置快照（go:embed）→ 本地缓存 → 运行时刷新。
// 仅保留 mcode 支持的 provider 与能力字段，快照与缓存共用同一裁剪格式。
package modelsdev

import (
	_ "embed"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
)

//go:embed snapshot.json
var snapshotData []byte

// Providers 是 mcode 支持的 models.dev provider 键；抓取脚本的默认列表需与此保持一致。
var Providers = []string{"anthropic", "openai", "google", "cline-pass"}

// Cost 是模型价格，单位为美元/百万 token。
type Cost struct {
	Input      float64 `json:"input,omitempty"`
	Output     float64 `json:"output,omitempty"`
	CacheRead  float64 `json:"cache_read,omitempty"`
	CacheWrite float64 `json:"cache_write,omitempty"`
}

// rawModel 同时描述 models.dev 原始条目与裁剪后的快照条目；未知字段被忽略。
type rawModel struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Family      string `json:"family,omitempty"`
	Status      string `json:"status,omitempty"`
	ReleaseDate string `json:"release_date,omitempty"`
	Canonical   string `json:"canonical_model_id,omitempty"`
	Limit       struct {
		Context int `json:"context,omitempty"`
	} `json:"limit,omitempty"`
	ReasoningOptions []rawReasoning `json:"reasoning_options,omitempty"`
	Experimental     *struct {
		Modes map[string]struct{} `json:"modes,omitempty"`
	} `json:"experimental,omitempty"`
	Cost       *Cost          `json:"cost,omitempty"`
	Reasoning  bool           `json:"reasoning,omitempty"`
	Attachment bool           `json:"attachment,omitempty"`
	Knowledge  string         `json:"knowledge,omitempty"`
	Modalities *rawModalities `json:"modalities,omitempty"`
}

type rawModalities struct {
	Input  []string `json:"input,omitempty"`
	Output []string `json:"output,omitempty"`
}

type rawReasoning struct {
	Type   string    `json:"type"`
	Values []*string `json:"values,omitempty"`
}

type rawProviders map[string]struct {
	Models map[string]json.RawMessage `json:"models"`
}

// Info 是一个模型的能力摘要。
type Info struct {
	ID              string
	Name            string
	Family          string
	Efforts         []string // 由弱到强，已去掉 "none"；空表示没有可选档位
	Fast            bool
	ContextLimit    int
	ReleaseDate     string
	Deprecated      bool
	Cost            *Cost // 价格；models.dev 未提供或输入输出价格都为 0 时为 nil
	Reasoning       bool
	Attachment      bool
	InputModalities []string
	Knowledge       string
}

// Catalog 是并发安全的模型能力目录。
type Catalog struct {
	mu     sync.RWMutex
	models map[string]map[string]Info // provider -> 小写模型 id/canonical 末段 -> Info
}

// effortRank 决定档位排序；未知档位保持原相对位置。
var effortRank = map[string]int{
	"minimal": 1, "low": 2, "medium": 3, "high": 4,
	"xhigh": 5, "extra-high": 5, "extra_high": 5, "max": 6,
}

func orderEfforts(values []string) []string {
	out := append([]string(nil), values...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, oki := effortRank[strings.ToLower(out[i])]
		rj, okj := effortRank[strings.ToLower(out[j])]
		return oki && okj && ri < rj
	})
	return out
}

func (m rawModel) info() Info {
	in := Info{
		ID:           m.ID,
		Name:         m.Name,
		Family:       m.Family,
		ContextLimit: m.Limit.Context,
		Deprecated:   strings.EqualFold(m.Status, "deprecated"),
	}
	// models.dev 用 1970-01-01 占位未知发布日期，排序时会被当作最旧。
	if !strings.HasPrefix(m.ReleaseDate, "1970-01-01") {
		in.ReleaseDate = m.ReleaseDate
	}
	for _, opt := range m.ReasoningOptions {
		if opt.Type != "effort" {
			continue
		}
		var values []string
		for _, v := range opt.Values {
			if v != nil && !strings.EqualFold(*v, "none") && *v != "" {
				values = append(values, *v)
			}
		}
		in.Efforts = orderEfforts(values)
	}
	if m.Experimental != nil {
		_, in.Fast = m.Experimental.Modes["fast"]
	}
	// 输入输出都为 0 的 cost 没有信息量，按缺失处理。
	if m.Cost != nil && (m.Cost.Input != 0 || m.Cost.Output != 0) {
		cost := *m.Cost
		in.Cost = &cost
	}
	in.Reasoning = m.Reasoning
	in.Attachment = m.Attachment
	in.Knowledge = m.Knowledge
	if m.Modalities != nil {
		in.InputModalities = m.Modalities.Input
	}
	return in
}

// Summary 返回一行中文能力摘要；没有可展示的信息时返回空串。
func (i Info) Summary() string {
	var parts []string
	if i.ContextLimit >= 1000 {
		parts = append(parts, contextLabel(i.ContextLimit)+" 上下文")
	}
	if i.Reasoning {
		parts = append(parts, "支持推理")
	}
	for _, m := range i.InputModalities {
		if strings.EqualFold(m, "image") {
			parts = append(parts, "支持图像输入")
			break
		}
	}
	if i.Knowledge != "" {
		parts = append(parts, "知识截至 "+i.Knowledge)
	}
	return strings.Join(parts, " · ")
}

// contextLabel 把上下文上限写成 200K、1M 这样的短标签。
func contextLabel(n int) string {
	if n >= 1000000 {
		s := strconv.FormatFloat(float64(n)/1000000, 'f', 1, 64)
		return strings.TrimSuffix(s, ".0") + "M"
	}
	return strconv.Itoa(n/1000) + "K"
}

// parse 解码快照/缓存/原始 api.json，只保留 Providers 中的 provider；单个条目解码失败只丢弃该条。
// 返回裁剪后的条目（用于写缓存）与索引。
func parse(data []byte) (map[string]map[string]rawModel, error) {
	// 兼容 {"providers": {...}} 包裹形态与顶层直接是 provider 表的形态。
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, err
	}
	if wrapped, ok := top["providers"]; ok {
		if _, isProvider := top["anthropic"]; !isProvider {
			data = wrapped
		}
	}
	var root rawProviders
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	out := map[string]map[string]rawModel{}
	for _, p := range Providers {
		prov, ok := root[p]
		if !ok {
			continue
		}
		models := map[string]rawModel{}
		for key, raw := range prov.Models {
			var m rawModel
			if err := json.Unmarshal(raw, &m); err != nil || m.ID == "" {
				continue
			}
			models[key] = m
		}
		if len(models) > 0 {
			out[p] = models
		}
	}
	return out, nil
}

func index(parsed map[string]map[string]rawModel) map[string]map[string]Info {
	idx := make(map[string]map[string]Info, len(parsed))
	for p, models := range parsed {
		byID := make(map[string]Info, len(models))
		for key, m := range models {
			in := m.info()
			byID[strings.ToLower(key)] = in
			byID[strings.ToLower(m.ID)] = in
		}
		idx[p] = byID
	}
	return idx
}

// New 返回只含内置快照的目录；快照损坏时返回空目录。
func New() *Catalog {
	c := &Catalog{}
	c.load(snapshotData)
	return c
}

// load 解码并叠加数据；解码为空时保持现有数据，不拿空目录换掉有内容的目录。
func (c *Catalog) load(data []byte) bool {
	parsed, err := parse(data)
	if err != nil || len(parsed) == 0 {
		return false
	}
	idx := index(parsed)
	c.mu.Lock()
	c.models = idx
	c.mu.Unlock()
	return true
}

// Lookup 按 provider 与模型 id（大小写不敏感）精确查找。
func (c *Catalog) Lookup(provider, id string) (Info, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	in, ok := c.models[provider][strings.ToLower(strings.TrimSpace(id))]
	return in, ok
}

// Latest 返回 provider 下指定 family 中最新发布、未弃用的模型。
// 发布日期相同则取 id 更短者（别名优先于带日期后缀的快照 id）。
func (c *Catalog) Latest(provider, family string) (Info, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var best Info
	found := false
	for _, in := range c.models[provider] {
		if in.Family != family || in.Deprecated {
			continue
		}
		if !found || in.ReleaseDate > best.ReleaseDate ||
			(in.ReleaseDate == best.ReleaseDate && (len(in.ID) < len(best.ID) || (len(in.ID) == len(best.ID) && in.ID < best.ID))) {
			best, found = in, true
		}
	}
	return best, found
}
