package magpie

// 本文件解析用户手动粘贴的人设列表 JSON。远端 magpie 读不到本机配置文件、又没配 web
// 管理地址时，用户可以把 persona-rules 插件页导出的列表粘进设置里，mcode 只取 id、名称
// 与描述做切换与展示。手动列表没有正文，正文仍由 magpie 上的插件按 id 注入。

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ParsePersonasJSON 解析手动粘贴的人设列表，接受三种形态：{"personas":[…]}、裸数组
// […]、{"options":{"personas":[…]}}。每项只取 id、name、description：插件页导出的
// JSON 不含正文，有 text 也忽略。ID 不合法的条目跳过，重复 ID 只留第一个，名字为空回落
// 为 ID，最多 personaLimit 条。整体不是合法 JSON 或没有任何有效项时返回 error。
func ParsePersonasJSON(text string) ([]Persona, error) {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return nil, errors.New("人设列表为空")
	}
	var options []personaOption
	// 裸数组与对象分开解码：同一个 personaLimit 结构体两处复用，键名与插件配置一致。
	if strings.HasPrefix(raw, "[") {
		if err := json.Unmarshal([]byte(raw), &options); err != nil {
			return nil, fmt.Errorf("人设列表不是合法的 JSON 数组: %w", err)
		}
	} else {
		var payload struct {
			Personas []personaOption `json:"personas"`
			Options  struct {
				Personas []personaOption `json:"personas"`
			} `json:"options"`
		}
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			return nil, fmt.Errorf("人设列表不是合法的 JSON 对象: %w", err)
		}
		options = payload.Personas
		if len(options) == 0 {
			// 用户直接粘了插件页的整段导出（options.personas）。
			options = payload.Options.Personas
		}
	}

	out := make([]Persona, 0, len(options))
	seen := make(map[string]bool, len(options))
	for _, option := range options {
		// 与读插件配置同一套规则：ID 直接出现在标签里，带空格或超长的不合法。
		if !personaIDPattern.MatchString(option.ID) || seen[option.ID] {
			continue
		}
		seen[option.ID] = true
		name := strings.TrimSpace(option.Name)
		if name == "" {
			name = option.ID
		}
		out = append(out, Persona{
			ID:          option.ID,
			Name:        name,
			Description: strings.TrimSpace(option.Description),
		})
		if len(out) >= personaLimit {
			break
		}
	}
	if len(out) == 0 {
		return nil, errors.New("人设列表里没有可用条目（id 只能是字母、数字、下划线或连字符）")
	}
	return out, nil
}
