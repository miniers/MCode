package magpieinject

// 本文件是按勾选生成注入模型列表的通用逻辑，对应原来 pi 的 selectedPiModels。

import (
	"strings"

	"mcode/internal/providers/magpie"
)

// DefaultModel 返回网关没列出某个已勾选模型时用的默认元数据。
// 每次调用都构造新的 Input 切片，调用方修改返回值不会污染默认值。
func DefaultModel() magpie.Model {
	return magpie.Model{
		Reasoning:     true,
		Input:         []string{"text", "image"},
		ContextWindow: 200000,
		MaxTokens:     32000,
	}
}

// Selected 按勾选顺序取出模型：网关没取到的 ID（或整次拉取失败）用默认元数据补上，
// 每个模型的 Name 换成界面显示用的 ShortName。空 ID 跳过。
func Selected(ids []string, fetched []magpie.Model) []magpie.Model {
	byID := make(map[string]magpie.Model, len(fetched))
	for _, m := range fetched {
		byID[m.ID] = m
	}
	out := make([]magpie.Model, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		m, ok := byID[id]
		if !ok {
			m = DefaultModel()
			m.ID = id
		}
		m.Name = magpie.ShortName(m)
		out = append(out, m)
	}
	return out
}
