package claude

// 本文件实现 claude 的 Magpie 运行时注入：模型 ID 形如 <ProviderID>/<网关模型 ID>，
// 命中某条路由时本轮改用网关模型 ID，并把端点地址、key 与 NO_PROXY 追加进环境。
// 线程里保存的仍是带前缀的模型 ID，改写只作用于本轮副本。

import (
	"fmt"
	"strings"

	"mcode/internal/harness"
	"mcode/internal/magpieinject"
)

// MagpieRoute 是一条 claude 可用的 Magpie 路由。
type MagpieRoute struct {
	// ProviderID 是注入模型 ID 的前缀，与 magpieinject.Target.ProviderID 相同。
	ProviderID string
	// BaseURL 是端点配置里的原值，注入时再由 AnthropicBaseURL 换成根地址。
	BaseURL string
	APIKey  string
	// Models 是该端点勾选的网关模型 ID，只用于展示与排查，可空。
	Models []string
	// Default 是该端点生效中的默认模型 ID（不带 ProviderID 前缀）；只在用户
	// 手动指定且条件满足的那个端点上非空。
	Default string
}

// MagpieEnv 返回 Claude 连接 Magpie 所需的环境变量。
func MagpieEnv(baseURL, apiKey string) []string {
	return magpieinject.WithLoopbackNoProxy([]string{
		"ANTHROPIC_BASE_URL=" + magpieinject.AnthropicBaseURL(baseURL),
		"ANTHROPIC_AUTH_TOKEN=" + apiKey,
		"ANTHROPIC_MODEL=",
		"ANTHROPIC_API_KEY=",
	})
}

// magpieRouteFor 按第一个斜杠切分模型 ID：前缀与某条路由的 ProviderID 相等时，
// 返回该路由与斜杠后的剩余部分。没有斜杠、前缀为空、前缀不认识时返回零值路由与 nil，
// 表示不是 Magpie 模型，调用方照旧直连。前缀命中但剩余部分为空属于配置错误，返回错误。
// claude 官方模型 ID 不含斜杠，因此不会误命中。
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
			return MagpieRoute{}, "", fmt.Errorf("claude: magpie 模型 %q 缺少网关模型 ID", model)
		}
		return route, rest, nil
	}
	return MagpieRoute{}, "", nil
}

// magpieTurn 在命中路由时改写本轮模型与环境：模型换成网关模型 ID，环境追加网关地址、
// token 与两个清空项（避免登录环境里的旧模型名和旧 API Key 干扰），再补本机 NO_PROXY。
// 未命中时原样返回。返回的 turn 与环境切片都是副本，不改动调用方的底层数组。
func magpieTurn(turn harness.Turn, routes []MagpieRoute) (harness.Turn, error) {
	// 模型为空或 default 表示交给 CLI 默认模型；此时若有默认模型，就换成该
	// 路由能命中的引用形式（ProviderID/网关模型 ID）。用户显式选的模型不覆盖。
	if model := strings.TrimSpace(turn.Model); model == "" || model == "default" {
		for _, route := range routes {
			if route.ProviderID == "" {
				continue
			}
			if def := strings.TrimSpace(route.Default); def != "" {
				turn.Model = route.ProviderID + "/" + def
				break
			}
		}
	}
	route, model, err := magpieRouteFor(turn.Model, routes)
	if err != nil {
		return turn, err
	}
	if route.ProviderID == "" {
		return turn, nil
	}

	turn.Model = model
	env := append(append([]string(nil), turn.Env...), MagpieEnv(route.BaseURL, route.APIKey)[:4]...)
	turn.Env = magpieinject.WithLoopbackNoProxy(env)
	return turn, nil
}
