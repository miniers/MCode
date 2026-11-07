package codex

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"mcode/internal/harness"
	"mcode/internal/magpieinject"
)

// MagpieRoute 是一条 Codex 可用的 Magpie 路由。
type MagpieRoute struct {
	ProviderID string
	BaseURL    string
	APIKey     string
	Default    string
}

func magpieRouteFor(model string, routes []MagpieRoute) (MagpieRoute, string, error) {
	providerID, rest, ok := strings.Cut(model, "/")
	if !ok || providerID == "" {
		return MagpieRoute{}, "", nil
	}
	for _, route := range routes {
		if route.ProviderID != providerID {
			continue
		}
		if strings.TrimSpace(rest) == "" {
			return MagpieRoute{}, "", fmt.Errorf("codex: magpie 模型 %q 缺少网关模型 ID", model)
		}
		return route, rest, nil
	}
	return MagpieRoute{}, "", nil
}

func magpieTurn(turn harness.Turn, routes []MagpieRoute) (harness.Turn, *MagpieRoute, error) {
	if model := strings.TrimSpace(turn.Model); model == "" || model == "default" {
		for _, route := range routes {
			if route.ProviderID != "" {
				if def := strings.TrimSpace(route.Default); def != "" {
					turn.Model = route.ProviderID + "/" + def
					break
				}
			}
		}
	}
	route, model, err := magpieRouteFor(turn.Model, routes)
	if err != nil {
		return turn, nil, err
	}
	if route.ProviderID == "" {
		return turn, nil, nil
	}
	turn.Model = model
	return turn, &route, nil
}

// tomlString 将值转成 TOML 基本字符串。
func tomlString(value string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, "\\u%04X", r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func magpieArgs(route MagpieRoute) []string {
	return []string{
		"-c", `model_provider="mcode-magpie"`,
		"-c", `model_providers.mcode-magpie.name="Magpie"`,
		"-c", "model_providers.mcode-magpie.base_url=" + tomlString(route.BaseURL),
		"-c", `model_providers.mcode-magpie.wire_api="responses"`,
		"-c", `model_providers.mcode-magpie.env_key="MCODE_MAGPIE_API_KEY"`,
		"-c", `model_providers.mcode-magpie.requires_openai_auth=false`,
	}
}

// MagpieArgs 返回 Codex 连接 Magpie 所需的配置覆盖参数。
func MagpieArgs(route MagpieRoute) []string { return magpieArgs(route) }

func magpieEnv(route MagpieRoute) []string {
	return magpieinject.WithLoopbackNoProxy([]string{"MCODE_MAGPIE_API_KEY=" + route.APIKey})
}

// MagpieEnv 返回 Codex 连接 Magpie 所需的环境变量。
func MagpieEnv(route MagpieRoute) []string { return magpieEnv(route) }

func magpieRouteKey(route *MagpieRoute) string {
	if route == nil {
		return ""
	}
	// 长度前缀避免不同字段的拼接结果发生歧义。
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s%d:%s%d:%s", len(route.ProviderID), route.ProviderID, len(route.BaseURL), route.BaseURL, len(route.APIKey), route.APIKey)))
	return fmt.Sprintf("%x", sum[:8])
}
