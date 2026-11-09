package frontdesk

import (
	"strings"

	"mcode/internal/apisource"
	"mcode/internal/config"
)

func cheapModel(src apisource.Source) string {
	for _, model := range src.Models {
		lower := strings.ToLower(model)
		for _, part := range []string{"haiku", "mini", "flash", "deepseek-chat", "glm-4-flash"} {
			if strings.Contains(lower, part) {
				return model
			}
		}
	}
	return src.DefaultModel
}

// SelectSource 按用户配置和来源顺序选择前台模型。
// 不存在的 provider 视为旧配置，忽略其模型；匹配来源时尝试去掉模型前缀。
func SelectSource(sources []apisource.Source, secrets config.SecretStore, provider, model string) (apisource.Source, string, error) {
	if len(sources) == 0 {
		return apisource.Source{}, "", ErrNoModel
	}
	selected := -1
	if provider != "" {
		for i, s := range sources {
			if s.Kind == provider {
				selected = i
				break
			}
		}
		if selected < 0 {
			provider, model = "", ""
		}
	}
	if selected < 0 {
		for i, s := range sources {
			if s.APIKey(secrets) != "" && (strings.TrimSpace(model) != "" || strings.TrimSpace(cheapModel(s)) != "") {
				selected = i
				break
			}
		}
	}
	if selected < 0 {
		for i, s := range sources {
			if strings.TrimSpace(model) != "" || strings.TrimSpace(cheapModel(s)) != "" {
				selected = i
				break
			}
		}
	}
	if selected < 0 {
		return apisource.Source{}, "", ErrNoModel
	}
	src := sources[selected]
	if provider != "" && model != "" && len(src.Models) > 0 {
		candidate := model
		matched := false
		for {
			for _, available := range src.Models {
				if candidate == available {
					model = candidate
					matched = true
					break
				}
			}
			if matched {
				break
			}
			_, rest, ok := strings.Cut(candidate, "/")
			if !ok {
				break
			}
			candidate = rest
		}
	}
	if strings.TrimSpace(model) == "" {
		model = cheapModel(src)
	}
	if strings.TrimSpace(model) == "" {
		return apisource.Source{}, "", ErrNoModel
	}
	return src, model, nil
}
