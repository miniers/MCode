package textgen

import (
	"strings"

	"mcode/internal/providers/loginenv"
)

// lookPath 是 CLI 查找函数，抽成包级变量便于测试替换；默认使用登录环境的 PATH。
var lookPath = loginenv.LookPath

// providerCLI 列出各 provider 对应的 CLI 名称。
var providerCLI = map[string]string{
	"claude": "claude",
	"codex":  "codex",
	"pi":     "pi",
}

// SupportsProvider 判断 provider 是否被 GenerateWithOptions 支持。
// memory 是供测试使用的回显 provider，项目内也算支持，但不会进入审查的生产回退顺序。
func SupportsProvider(provider string) bool {
	switch normalizeProvider(provider) {
	case "claude", "codex", "pi", "memory":
		return true
	default:
		return false
	}
}

// AvailableProvider 判断 provider 的 CLI 是否能在登录环境的 PATH 中找到。
// 这里不能复用 resolveCLI：它查不到时会原样返回 bin，无法区分「未安装」。
func AvailableProvider(provider string) bool {
	bin, ok := providerCLI[normalizeProvider(provider)]
	if !ok {
		return false
	}
	path, err := lookPath(bin)
	return err == nil && path != ""
}

// ReviewProviderOrder 返回审查时尝试 provider 的顺序：
// explicit 受支持时排第一，随后按 claude、codex、pi 的顺序补上其余 provider，并去重。
// memory 等测试用 provider 不进入结果；不支持或无法使用的 explicit 会被忽略。
// pi 没有 CheapestModel，调用方必须显式给出 model，否则 pi 不会进入结果，
// 避免把其他 provider 的模型名误传给 pi。
func ReviewProviderOrder(explicit, model string) []string {
	hasModel := strings.TrimSpace(model) != ""
	usable := func(provider string) bool {
		if !SupportsProvider(provider) || provider == "memory" {
			return false
		}
		if provider == "pi" && !hasModel {
			return false
		}
		return true
	}

	order := make([]string, 0, 3)
	seen := make(map[string]bool, 3)
	add := func(provider string) {
		if provider == "" || seen[provider] || !usable(provider) {
			return
		}
		seen[provider] = true
		order = append(order, provider)
	}

	add(normalizeProvider(explicit))
	for _, provider := range []string{"claude", "codex", "pi"} {
		add(provider)
	}
	return order
}

func normalizeProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}
