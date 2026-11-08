package app

// 本文件把人设切换接到 magpie 的 persona-rules 插件：mcode 在每轮 prompt 最前面加
// [[persona:ID]] 标签，插件认出后注入对应人设正文。人设清单有三个来源，按优先级依次是：
// 本机 magpie 的插件配置、magpie web 管理接口（远端部署）、用户手动粘贴的列表。其余
// provider 一律不加标签，界面上的人设功能也随之隐藏。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"mcode/internal/config"
	"mcode/internal/providers/magpie"
	mcodeui "mcode/internal/ui"
)

// personaNone 是「不使用人设」的标签值：带上它才能把会话历史里残留的旧人设覆盖掉。
const personaNone = "none"

// personaReadTimeout 是一次读人设的上限：web 来源要联网，界面与每轮运行前都不该等太久。
// 读成功与读失败都有缓存（见 magpie/personas_web.go），多数轮次不会真的发请求。
const personaReadTimeout = 5 * time.Second

// personaSource 是 provider/model 命中的 Magpie 端点以及它配置的人设来源。
// 这里不取端点密钥：每轮运行前都会问一次，读 keychain 不值当；web 管理密钥由
// personasFor 的惰性函数在真正要发请求时才取。
type personaSource struct {
	// endpointID 是端点在配置里的 ID，用来定位密钥库里的 web 管理密钥与缓存。
	endpointID string
	// baseURL 是对话用的网关地址，本机来源据此读本机插件配置。
	baseURL string
	// webURL 是 magpie web 管理地址，为空表示没配 web 来源。
	webURL string
	// manualJSON 是用户手动粘贴的人设列表原文。
	manualJSON string
	// personaRules 表示用户声明已在 magpie 部署 persona-rules，手动列表才生效。
	personaRules bool
}

// personaSourceFor 返回 provider/model 对应 Magpie 端点的人设来源。
// provider 是 Magpie 自定义端点的 kind 时按端点比对；provider 为 pi 或 mcode 时模型 ID 的第一段
// 是 provider id（见 internal/ui/composer_usage_panel.go 的 threadQuotaProvider），
// 与端点分配的 providerID 比对。其余情况（非 Magpie、端点未启用、映射不到）返回 false。
// providerID 的派生与 piMagpieTargets 一致，同一份配置得到的 id 才对得上。
func personaSourceFor(cfg config.Config, provider, model string) (personaSource, bool) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return personaSource{}, false
	}
	piProviderID, fromPi := piProviderIDFromModel(provider, model)
	taken := make(map[string]bool, len(cfg.Providers.Custom))
	for _, ep := range cfg.Providers.Custom {
		if !ep.Enabled || !ep.Magpie {
			continue
		}
		providerID := magpie.ProviderID(ep.Name, taken)
		taken[providerID] = true
		if fromPi {
			if !strings.EqualFold(providerID, piProviderID) {
				continue
			}
		} else if !strings.EqualFold(config.CustomEndpointProviderKind(ep), provider) {
			continue
		}
		return personaSource{
			endpointID:   ep.ID,
			baseURL:      magpieEndpointBaseURL(ep.BaseURL),
			webURL:       strings.TrimSpace(ep.PersonaWebURL),
			manualJSON:   strings.TrimSpace(ep.PersonasJSON),
			personaRules: ep.PersonaRules,
		}, true
	}
	return personaSource{}, false
}

// magpieEndpointBaseURL 返回端点的 baseURL，口径与 piMagpieTargets 一致：
// 空值用默认本机地址，规范化失败时只去掉结尾斜杠。
func magpieEndpointBaseURL(rawBaseURL string) string {
	baseURL := strings.TrimSpace(rawBaseURL)
	if baseURL == "" {
		baseURL = config.DefaultMagpieBaseURL
	}
	if normalized, err := customEndpointBaseURL(baseURL); err == nil {
		return normalized
	}
	return strings.TrimRight(baseURL, "/")
}

// piProviderIDFromModel 判断 provider 是否指向 pi 或内置 mcode，并给出模型 ID 的第一段。
// 只有 provider 为 pi、mcode 或 pi:<provider id> 时第二个返回值才为 true。
// mcode 与 pi 共用模型串格式（Magpie 的 provider id + "/" + 模型 ID），因此同一套解析适用。
func piProviderIDFromModel(provider, model string) (string, bool) {
	if provider != "pi" && provider != "mcode" && !strings.HasPrefix(provider, "pi:") {
		return "", false
	}
	if head, _, ok := strings.Cut(strings.TrimSpace(model), "/"); ok && head != "" {
		return head, true
	}
	// 只带 pi:<provider id> 的调用（额度面板这类）没有模型 ID，直接用那一段。
	if rest, ok := strings.CutPrefix(provider, "pi:"); ok && rest != "" {
		return rest, true
	}
	return "", true
}

// personasFor 按优先级读出 provider/model 对应端点的人设列表：
// ① 网关在本机且本机插件配置里有非空列表时用它；
// ② 配置了 web 管理地址时以 web 接口结果为准（空列表也算结果：magpie 上插件已关或不存在），
// 出错（网络不通、鉴权失败、没配密钥）才继续往下一档回落；
// ③ 用户声明部署了 persona-rules 且手动列表能解析时用它；
// ④ 都没有就是空列表。非 Magpie 的 provider 永远返回空列表。
func personasFor(ctx context.Context, cfg config.Config, secrets config.SecretStore, provider, model string) []magpie.Persona {
	src, ok := personaSourceFor(cfg, provider, model)
	if !ok {
		return nil
	}
	// ① 本机来源：ReadPersonas 对非回环地址直接返回空，等于顺手做了本机判断。
	if local, err := magpie.ReadPersonas(src.baseURL); err == nil && len(local) > 0 {
		return local
	}
	// ② web 来源：缓存键带上端点 ID 与管理地址，改动地址后不会读到旧结果。
	if src.webURL != "" {
		cacheKey := magpie.PersonaCacheKey(src.endpointID, src.webURL)
		web, err := magpie.ReadWebPersonas(ctx, cacheKey, src.webURL, func() string {
			// 惰性取密钥：缓存命中时根本不碰密钥库。
			return personaWebKey(secrets, src.endpointID)
		})
		if err == nil {
			return web
		}
	}
	// ③ 手动来源：只有用户勾了「已部署 persona-rules」才生效，否则粘贴的列表不参与。
	if src.personaRules && src.manualJSON != "" {
		if manual, err := magpie.ParsePersonasJSON(src.manualJSON); err == nil {
			return manual
		}
	}
	return nil
}

// personaWebKey 读端点保存的 magpie web 管理密钥；没有密钥库或端点 ID 为空时返回空串。
// 文案与日志都不该出现它的明文，调用方只把它交给 magpie.ReadWebPersonas。
func personaWebKey(secrets config.SecretStore, endpointID string) string {
	if secrets == nil || strings.TrimSpace(endpointID) == "" {
		return ""
	}
	value, err := secrets.Get(config.CustomEndpointWebKeySecretAccount(endpointID))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

// MagpiePersonas 列出 provider/model 对应 Magpie 端点的人设。
// 不是 Magpie 端点、读不到来源或插件没启用时返回空列表与 nil：界面据此隐藏人设切换。
func (c *Client) MagpiePersonas(ctx context.Context, provider, model string) ([]mcodeui.MagpiePersona, error) {
	cfg, ok := c.loadConfig()
	if !ok {
		return nil, nil
	}
	c.providersMu.Lock()
	secrets := c.secrets
	c.providersMu.Unlock()

	// 界面刷新只等这么久：web 来源在缓存过期时真的会联网。
	ctx, cancel := context.WithTimeout(ctx, personaReadTimeout)
	defer cancel()
	personas := personasFor(ctx, cfg, secrets, provider, model)
	if len(personas) == 0 {
		return nil, nil
	}
	res := make([]mcodeui.MagpiePersona, 0, len(personas))
	for _, p := range personas {
		res = append(res, mcodeui.MagpiePersona{ID: p.ID, Name: p.Name, Description: p.Description})
	}
	return res, nil
}

// TestMagpiePersonas 测试一份（可能还没保存的）Magpie 端点的人设来源，供设置页的
// 「测试」按钮用。webKey 为空且端点 ID 对应已保存端点时回落读密钥库里已保存的密钥。
// 返回的 Message 是给用户看的中文文案，失败时是已脱敏的原因，不含密钥与带 k= 的地址。
func (c *Client) TestMagpiePersonas(ctx context.Context, ep mcodeui.CustomEndpoint, webKey string) (mcodeui.PersonaSourceTestResult, error) {
	webURL, err := normalizePersonaWebURL(ep.PersonaWebURL)
	if err != nil {
		return mcodeui.PersonaSourceTestResult{}, err
	}
	endpointID := strings.TrimSpace(ep.ID)
	c.providersMu.Lock()
	secrets := c.secrets
	c.providersMu.Unlock()

	// 表单里没填密钥时用已保存的那份：用户只改了地址也要能测通。
	key := strings.TrimSpace(webKey)
	if key == "" {
		key = personaWebKey(secrets, endpointID)
	}
	// 测试结果不写缓存（cacheKey 传空），但先清掉旧缓存，免得存端点后读到改动前的结果。
	if webURL != "" {
		magpie.InvalidatePersonaCache(magpie.PersonaCacheKey(endpointID, webURL))
		personas, err := magpie.ReadWebPersonas(ctx, "", webURL, func() string { return key })
		if err != nil {
			return mcodeui.PersonaSourceTestResult{Source: "web", Message: err.Error()}, nil
		}
		if len(personas) == 0 {
			return mcodeui.PersonaSourceTestResult{
				OK: true, Source: "web", Message: "magpie 上 persona-rules 未启用或不存在",
			}, nil
		}
		return mcodeui.PersonaSourceTestResult{
			OK:      true,
			Source:  "web",
			Count:   len(personas),
			Message: fmt.Sprintf("已从 magpie 读到 %d 个人设", len(personas)),
		}, nil
	}

	if manual := strings.TrimSpace(ep.PersonasJSON); manual != "" {
		personas, err := magpie.ParsePersonasJSON(manual)
		if err != nil {
			return mcodeui.PersonaSourceTestResult{Source: "manual", Message: err.Error()}, nil
		}
		return mcodeui.PersonaSourceTestResult{
			OK:      true,
			Source:  "manual",
			Count:   len(personas),
			Message: fmt.Sprintf("手动列表有效，共 %d 个人设", len(personas)),
		}, nil
	}

	// 只剩本机来源可测：远端地址读不到本机配置文件，直接说没配来源。
	baseURL := magpieEndpointBaseURL(ep.BaseURL)
	if magpie.IsLocalGateway(baseURL) {
		personas, err := magpie.ReadPersonas(baseURL)
		if err != nil {
			return mcodeui.PersonaSourceTestResult{Source: "local", Message: err.Error()}, nil
		}
		if len(personas) == 0 {
			return mcodeui.PersonaSourceTestResult{
				OK: true, Source: "local", Message: "本机 magpie 上 persona-rules 未启用或不存在",
			}, nil
		}
		return mcodeui.PersonaSourceTestResult{
			OK:      true,
			Source:  "local",
			Count:   len(personas),
			Message: fmt.Sprintf("已从本机 magpie 读到 %d 个人设", len(personas)),
		}, nil
	}

	return mcodeui.PersonaSourceTestResult{Message: "未配置人设来源"}, nil
}

// personaTagFor 返回放在 prompt 最前面的标签前缀（含结尾的两个换行）。
// persona 为空、不是 Magpie 端点或读不到人设时返回空串（不加标签）；persona 为 none
// 或在人设列表里时给出对应标签；persona 已不在列表里（人设被删掉、历史里残留旧标签）
// 也回落到 none，保证旧人设不会继续生效。
func personaTagFor(cfg config.Config, secrets config.SecretStore, provider, model, persona string) string {
	persona = strings.TrimSpace(persona)
	if persona == "" {
		return ""
	}
	// 这里没有 ctx：每轮运行前调用一次，自己兜一个超时，靠缓存让多数轮次不发请求。
	ctx, cancel := context.WithTimeout(context.Background(), personaReadTimeout)
	defer cancel()
	personas := personasFor(ctx, cfg, secrets, provider, model)
	if len(personas) == 0 {
		return ""
	}
	if persona == personaNone {
		return personaTagText(personaNone)
	}
	for _, p := range personas {
		if p.ID == persona {
			return personaTagText(persona)
		}
	}
	return personaTagText(personaNone)
}

// personaTagText 拼出标签前缀本身。
func personaTagText(persona string) string {
	return "[[persona:" + persona + "]]\n\n"
}

// personaTag 按当前最新的配置算这一轮的标签：每轮重新读配置与 secrets，设置改完
// 下一次运行就生效，不闭包捕获启动时的快照。
func (c *Client) personaTag(provider, model, persona string) string {
	if strings.TrimSpace(persona) == "" {
		return ""
	}
	cfg, ok := c.loadConfig()
	if !ok {
		return ""
	}
	c.providersMu.Lock()
	secrets := c.secrets
	c.providersMu.Unlock()
	return personaTagFor(cfg, secrets, provider, model, persona)
}
