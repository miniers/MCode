package swarm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// ProfilesFileName 是旧版遗留的 Profile 文件名（hydra-profiles.json），仅用于导入旧数据；
// 编排现在从配置里的档位读取执行配置，不再写这个文件。
const ProfilesFileName = "hydra-profiles.json"

// BeeProfile 是一份用途定制的 bee 配置（如 "quick"、"deep"）：Lead 在委派里按名字
// 指名它，该 bee 就跑在 Profile 的模型与强度上。空字段表示继承配对的 bee 字段。
type BeeProfile struct {
	Name         string   `json:"name"`
	Provider     string   `json:"provider,omitempty"`
	Model        string   `json:"model,omitempty"`
	Effort       string   `json:"effort,omitempty"`
	SystemPrompt string   `json:"system_prompt,omitempty"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	DeniedTools  []string `json:"denied_tools,omitempty"`
}

// LoadProfiles 从 path 读取 Profile 数组：path 为空或文件不存在时返回 nil, nil；
// 文件内容是 BeeProfile 的 JSON 数组；Name 为空的条目会被丢弃。
func LoadProfiles(path string) ([]BeeProfile, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var raw []BeeProfile
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	var out []BeeProfile
	for _, p := range raw {
		if strings.TrimSpace(p.Name) == "" {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// FindProfile 按名字查找 Profile：name 先去除首尾空白，为空时返回 false；
// 之后大小写不敏感地匹配 Profile 的名字。
func FindProfile(ps []BeeProfile, name string) (BeeProfile, bool) {
	want := strings.TrimSpace(name)
	if want == "" {
		return BeeProfile{}, false
	}
	for _, p := range ps {
		if strings.EqualFold(strings.TrimSpace(p.Name), want) {
			return p, true
		}
	}
	return BeeProfile{}, false
}

// applyProfilePrompt 把 Profile 的系统提示词前置到任务提示词之前；
// systemPrompt 去空白后为空时原样返回 prompt。
func applyProfilePrompt(prompt, systemPrompt string) string {
	prefix := strings.TrimSpace(systemPrompt)
	if prefix == "" {
		return prompt
	}
	return prefix + "\n\n" + prompt
}

// BeeConfig 是一次 bee 派发最终生效的执行配置。
type BeeConfig struct {
	Provider     string
	Model        string
	Effort       string
	FastMode     bool
	SystemPrompt string
	Profile      string
	PairID       string
	Tier         string
	ReadOnly     bool
	// WritePaths 非空时该 bee 只能把匹配这些 glob 的改动合并回主工作区。
	WritePaths  []string
	DirectModel bool
}

// ResolveInput 是解析一次 bee 派发所需的全部输入。
type ResolveInput struct {
	Delegation      Delegation
	Profiles        []BeeProfile
	Pairs           []Pair
	Tiers           []Tier
	DefaultTier     string
	PairID          string
	ThreadOverrides map[string]TierOverride
	UserMessage     string
	ProviderEnabled func(string) bool
	// SessionBeeProvider/SessionBeeModel 是会话里显式选的子代理模型，优先于档位（含默认档位）自带的模型。
	SessionBeeProvider string
	SessionBeeModel    string
	// SessionBeeEffort 是会话里选的子代理推理强度，跟着 SessionBeeModel 一起生效，同样优先于档位自带的强度。
	SessionBeeEffort string
}

// ResolveBeeConfig 按「档位/Profile → 会话子代理模型 → 配对覆盖 → 线程覆盖 → 委派直属指定」的顺序逐层覆盖
// （非空才覆盖），返回最终配置以及给 Lead 的中文提示。这里不套用任何全局默认 Provider。
func ResolveBeeConfig(in ResolveInput) (BeeConfig, []string) {
	var cfg BeeConfig
	var notes []string
	d := in.Delegation

	// a. 确定档位引用
	ref := strings.TrimSpace(d.Tier)
	if ref == "" {
		ref = strings.TrimSpace(d.Profile)
	}
	if ref == "" {
		ref = strings.TrimSpace(in.DefaultTier)
	}

	// b. 匹配档位或降级匹配 legacy Profile
	if tier, ok := FindTier(in.Tiers, ref); ok {
		applyBeeFields(&cfg, tier.Provider, tier.Model, tier.Effort)
		cfg.FastMode = tier.FastMode
		if tier.SystemPrompt != "" {
			cfg.SystemPrompt = tier.SystemPrompt
		}
		cfg.ReadOnly = tier.ReadOnly
		cfg.WritePaths = append([]string(nil), tier.WritePaths...)
		cfg.Tier = tier.ID
	} else if ref != "" {
		if p, ok := FindProfile(in.Profiles, ref); ok {
			applyBeeFields(&cfg, p.Provider, p.Model, p.Effort)
			cfg.SystemPrompt = p.SystemPrompt
			cfg.Profile = p.Name
		} else if len(in.Tiers) == 0 {
			notes = append(notes, fmt.Sprintf("未找到 Profile %q，按默认配置派发", ref))
		} else {
			notes = append(notes, fmt.Sprintf("未找到档位 %q，已按默认档位 %q 派发", ref, in.DefaultTier))
			if defTier, ok := FindTier(in.Tiers, in.DefaultTier); ok {
				applyBeeFields(&cfg, defTier.Provider, defTier.Model, defTier.Effort)
				cfg.FastMode = defTier.FastMode
				if defTier.SystemPrompt != "" {
					cfg.SystemPrompt = defTier.SystemPrompt
				}
				cfg.ReadOnly = defTier.ReadOnly
				cfg.WritePaths = append([]string(nil), defTier.WritePaths...)
				cfg.Tier = defTier.ID
			}
		}
	}

	// b2. 会话里显式选的子代理模型优先于档位（包括默认档位）自带的模型；
	// 配对/线程覆盖与委派明确授权的直属模型仍在后面覆盖。
	sessionApplied := false
	if in.SessionBeeModel != "" {
		applyBeeFields(&cfg, in.SessionBeeProvider, in.SessionBeeModel, in.SessionBeeEffort)
		sessionApplied = true
	} else if in.SessionBeeEffort != "" {
		// 只选了强度没选模型时，仅沿用档位模型覆盖强度，不算会话模型生效。
		applyBeeFields(&cfg, "", "", in.SessionBeeEffort)
	}

	// c. Pair 覆盖（针对当前 lead 线程配对）
	if in.PairID != "" {
		if p, ok := FindPair(in.Pairs, in.PairID); ok {
			cfg.PairID = p.ID
			if p.BeeTiers != nil && cfg.Tier != "" {
				if ov, exists := p.BeeTiers[cfg.Tier]; exists {
					applyBeeFields(&cfg, ov.Provider, ov.Model, ov.Effort)
					if ov.FastMode != nil {
						cfg.FastMode = *ov.FastMode
					}
				}
			}
		}
	}

	// d. Thread 覆盖
	if in.ThreadOverrides != nil && cfg.Tier != "" {
		if ov, exists := in.ThreadOverrides[cfg.Tier]; exists {
			applyBeeFields(&cfg, ov.Provider, ov.Model, ov.Effort)
			if ov.FastMode != nil {
				cfg.FastMode = *ov.FastMode
			}
		}
	}

	// e. 委派自带直属字段
	if d.Pair != "" {
		if p, ok := FindPair(in.Pairs, d.Pair); ok {
			if MentionsInMessage(in.UserMessage, p.ID) || MentionsInMessage(in.UserMessage, p.Name) {
				cfg.PairID = p.ID
				cfg.DirectModel = true
				if p.BeeTiers != nil && cfg.Tier != "" {
					if ov, exists := p.BeeTiers[cfg.Tier]; exists {
						applyBeeFields(&cfg, ov.Provider, ov.Model, ov.Effort)
						if ov.FastMode != nil {
							cfg.FastMode = *ov.FastMode
						}
					} else {
						applyBeeFields(&cfg, p.BeeProvider, p.BeeModel, p.BeeEffort)
						cfg.FastMode = p.BeeFastMode
					}
				} else {
					applyBeeFields(&cfg, p.BeeProvider, p.BeeModel, p.BeeEffort)
					cfg.FastMode = p.BeeFastMode
				}
			} else {
				notes = append(notes, fmt.Sprintf("用户本条消息未授权配对 %q，已按档位 %q 派发", d.Pair, cfg.Tier))
			}
		} else {
			notes = append(notes, fmt.Sprintf("未找到配对 %q，按默认配置派发", d.Pair))
		}
	}

	if d.Provider != "" || d.Model != "" {
		if AuthorizeDirectModel(in.UserMessage, d.Provider, d.Model, in.ProviderEnabled) {
			applyBeeFields(&cfg, d.Provider, d.Model, "")
			cfg.DirectModel = true
		} else {
			target := d.Model
			if target == "" {
				target = d.Provider
			}
			notes = append(notes, fmt.Sprintf("用户本条消息未授权指定模型 %q，已按档位 %q 派发", target, cfg.Tier))
		}
	}

	// 会话选的子代理模型被后面的层（配对/线程覆盖、委派授权的直属模型）覆盖时，
	// 说明真正生效的模型，避免输出已经被覆盖的误导提示。
	if sessionApplied && cfg.Model != in.SessionBeeModel {
		notes = append(notes, fmt.Sprintf("会话子代理模型 %s 已被后续配置覆盖，实际使用 %s", providerModelLabel(in.SessionBeeProvider, in.SessionBeeModel), providerModelLabel(cfg.Provider, cfg.Model)))
	}

	return cfg, notes
}

// providerModelLabel 把 provider 与 model 拼成一条可读标签：provider 为空时只显示 model，避免出现「/gpt-5」。
func providerModelLabel(provider, model string) string {
	if provider == "" {
		return model
	}
	if model == "" {
		return provider
	}
	return provider + "/" + model
}

// applyBeeFields 用一层解析结果覆盖配置：只在值非空时覆盖；
// Provider 被改成不同值时，若该层没给 Model，则清空 Model，避免二者错配。
func applyBeeFields(cfg *BeeConfig, provider, model, effort string) {
	if provider != "" {
		if provider != cfg.Provider && model == "" {
			cfg.Model = ""
		}
		cfg.Provider = provider
	}
	if model != "" {
		cfg.Model = model
	}
	if effort != "" {
		cfg.Effort = effort
	}
}
