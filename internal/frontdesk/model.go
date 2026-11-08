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
	}
	if selected < 0 {
		for i, s := range sources {
			if s.APIKey(secrets) != "" {
				selected = i
				break
			}
		}
	}
	if selected < 0 {
		selected = 0
		if model == "" {
			model = sources[0].DefaultModel
		}
	}
	src := sources[selected]
	if model == "" {
		model = cheapModel(src)
	}
	if model == "" {
		return apisource.Source{}, "", ErrNoModel
	}
	return src, model, nil
}
