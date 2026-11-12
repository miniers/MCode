package app

// 本文件让跑在 Magpie 上的内置引擎会话也能读额度。引擎会话的 provider 有两种：
// mcode 的模型串形如 <端点 provider id>/<模型 ID>，端点由 magpieinject.ProviderOf 的
// 同一份映射（mcodeTargets）解析；Magpie 端点自身的 kind（magpie、magpie2…magpie4）
// 直接以端点命名，模型串不带前缀。两者都复用 pi 线程的额度读取与「在用」标记，
// 额度 key 与 pi 同形：mcode:<端点 provider id> 与 magpie、magpie2…（界面侧见
// internal/ui/composer_usage_panel.go 的 threadQuotaProvider）。

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"mcode/internal/apisource"
	"mcode/internal/config"
	"mcode/internal/providers/codex"
	"mcode/internal/providers/magpie"
	mcodeui "mcode/internal/ui"
)

// engineMcodeQuotaPrefix 是 mcode 线程额度 key 的前缀，后缀是 magpieinject 分配的
// 端点 provider id；pi 线程用同样的形状（pi:<provider id>）。
const engineMcodeQuotaPrefix = config.ProviderMcode + ":"

// engineMagpieKindPattern 匹配 Magpie 端点自身的 provider kind：magpie 与 magpie2…magpie4
// 是 config.CustomEndpointProviderKind 沿用的老槽位名。
var engineMagpieKindPattern = regexp.MustCompile(`^magpie([2-4])?$`)

// engineQuotaEndpoint 是一份 Magpie 额度对应的端点与额度 key。
type engineQuotaEndpoint struct {
	key        string
	baseURL    string
	apiKey     string
	endpointID string
	providerID string
}

// engineQuotaKey 报告额度 key 是否属于内置引擎会话（mcode:<provider id>、magpie、magpie2…或 custom-<id>）。
func engineQuotaKey(key string) bool {
	if providerID, ok := strings.CutPrefix(key, engineMcodeQuotaPrefix); ok {
		return strings.TrimSpace(providerID) != ""
	}
	if id, ok := strings.CutPrefix(key, "custom-"); ok {
		return strings.TrimSpace(id) != ""
	}
	return engineMagpieKindPattern.MatchString(key)
}

// engineQuotaEndpointForKey 按额度 key 找 Magpie 端点。mcode 的后缀就是模型串第一段
// （端点 provider id），与 resolveMcodeClient 用同一份 mcodeTargets 映射；其他端点
// 直接按注册的 provider kind 匹配，密钥从凭据存储读取。
func (c *Client) engineQuotaEndpointForKey(key string) (engineQuotaEndpoint, bool) {
	cfg, ok := c.loadConfig()
	if !ok {
		return engineQuotaEndpoint{}, false
	}
	c.providersMu.Lock()
	secrets := c.secrets
	c.providersMu.Unlock()
	return engineQuotaEndpointIn(cfg, secrets, strings.TrimSpace(key))
}

// engineQuotaEndpointIn 在给定配置与凭据里解析额度 key，拆出来便于测试。
func engineQuotaEndpointIn(cfg config.Config, secrets config.SecretStore, key string) (engineQuotaEndpoint, bool) {
	if providerID, ok := strings.CutPrefix(key, engineMcodeQuotaPrefix); ok {
		providerID = strings.TrimSpace(providerID)
		for _, target := range mcodeTargets(cfg, secrets) {
			if target.ProviderID != providerID || !target.HasKey() {
				continue
			}
			return engineQuotaEndpoint{
				key:        engineMcodeQuotaPrefix + target.ProviderID,
				baseURL:    target.BaseURL,
				apiKey:     target.APIKey,
				endpointID: target.Endpoint,
				providerID: target.ProviderID,
			}, true
		}
		return engineQuotaEndpoint{}, false
	}
	if !engineQuotaKey(key) {
		return engineQuotaEndpoint{}, false
	}
	for _, source := range apisource.Resolve(cfg, secrets) {
		if source.Kind != key || !source.Magpie {
			continue
		}
		apiKey := source.APIKey(secrets)
		if apiKey == "" {
			return engineQuotaEndpoint{}, false
		}
		return engineQuotaEndpoint{key: key, baseURL: source.BaseURL, apiKey: apiKey, endpointID: strings.TrimPrefix(source.Kind, "custom-")}, true
	}
	return engineQuotaEndpoint{}, false
}

// magpieQuotaWindowsFrom 把一个端点的 Magpie 额度卡片映射成界面窗口：字段与来源和
// pi 线程一致，来源统一标成 Magpie 网关。
func magpieQuotaWindowsFrom(windows []magpie.QuotaWindow) []mcodeui.ProviderQuotaWindow {
	out := make([]mcodeui.ProviderQuotaWindow, 0, len(windows))
	for _, w := range windows {
		out = append(out, mcodeui.ProviderQuotaWindow{
			ID:           w.ID,
			Period:       w.Period,
			Label:        w.Label,
			UsedPercent:  w.UsedPercent,
			ResetsAt:     w.ResetsAt,
			Account:      w.Account,
			Source:       codex.QuotaSourceMagpie,
			Provider:     w.Provider,
			User:         w.User,
			Kind:         w.Kind,
			Plan:         w.Plan,
			Alias:        w.Alias,
			Last:         w.Last,
			LastServedAt: w.LastServedAt,
			ReadAt:       w.ReadAt,
			Display:      w.Display,
			Text:         w.Text,
			Unlimited:    w.Unlimited,
			Balance:      w.Balance,
			Unit:         w.Unit,
		})
	}
	return out
}

// readMagpieQuotaWindows 按真实路由供应商筛选额度卡片；没有路由时按 model 的 owned_by
// 推断，只有都未知时保留全部；已知上游没有匹配卡片时返回空。只给本会话实际应答的账号标「在用」。
// sessionID 是 Magpie trace 里的会话键：pi 是落盘的 provider 会话 ID，
// 内置引擎是线程 ID（引擎把线程 ID 当会话亲和键发给网关）。agent 为空时不补读用量记录，
// 只查 trace 与缓存：内置引擎的请求在网关侧没有可用的 agent 名。
func (c *Client) readMagpieQuotaWindows(ctx context.Context, baseURL, apiKey, agent, sessionID, model string) ([]mcodeui.ProviderQuotaWindow, error) {
	quotaCtx, cancel := context.WithTimeout(ctx, piMagpieFetchTimeout)
	defer cancel()
	windows, err := magpie.ReadQuotas(quotaCtx, baseURL, apiKey)
	if err != nil {
		return nil, err
	}
	out := magpieQuotaWindowsFrom(windows)
	// trace 记录会话实际应答的账号；本机端点读 trace，远程端点读 usage/requests 兜底。
	tracker := c.sessionTracker()
	tracker.noteSource(baseURL, apiKey)
	route, hasRoute := sessionRoute{}, false
	if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
		// 弹层可能被反复打开，fresh=false 时先看缓存，再用 10 秒节流控制远程读取。
		route, hasRoute = tracker.routeFor(ctx, baseURL, apiKey, agent, sessionID, false)
	}
	// 只有本会话的 trace 路由对得上 provider 与账号时才算在用；同一 provider 的其它
	// 账号、以及查不到映射时都不标。
	markRouteActive(out, route, hasRoute)
	upstream := strings.TrimSpace(route.Provider)
	if !hasRoute {
		upstream = ""
	}
	if upstream == "" && model != "" {
		upstream, _ = magpie.ModelProvider(quotaCtx, baseURL, apiKey, model)
	}
	return filterQuotaUpstream(out, upstream), nil
}

// filterQuotaUpstream 只保留已知上游的卡片；上游未知时保留全部。
func filterQuotaUpstream(windows []mcodeui.ProviderQuotaWindow, upstream string) []mcodeui.ProviderQuotaWindow {
	if upstream == "" {
		return windows
	}
	var filtered []mcodeui.ProviderQuotaWindow
	for _, w := range windows {
		if strings.EqualFold(w.Provider, upstream) {
			filtered = append(filtered, w)
		}
	}
	return filtered
}

// magpieQuotaModel 优先使用显式模型，否则读取线程模型，只去掉已识别的端点前缀。
func (c *Client) magpieQuotaModel(ctx context.Context, agent, endpointID, providerID string) string {
	model := mcodeui.QuotaModel(ctx)
	if strings.TrimSpace(model) == "" {
		current, err := readThreadModel(ctx, c.backend, mcodeui.QuotaThread(ctx))
		if err == nil {
			model = current
		}
	}
	model = strings.TrimSpace(model)
	if providerID != "" {
		model = strings.TrimPrefix(model, providerID+"/")
	}
	if model == "" {
		model = c.magpieQuotaDefaultModel(agent, endpointID)
	}
	return model
}

func (c *Client) magpieQuotaDefaultModel(agent, endpointID string) string {
	cfg, ok := c.loadConfig()
	if !ok || endpointID == "" {
		return ""
	}
	setting := config.MagpieAgentSetting(cfg, agent)
	if setting.DefaultEndpointID == endpointID {
		return strings.TrimSpace(setting.DefaultModel)
	}
	return ""
}

// codexQuotaEndpointID 只在地址能唯一定位配置端点时用于默认模型兜底。
func (c *Client) codexQuotaEndpointID(baseURL string) string {
	cfg, ok := c.loadConfig()
	if !ok {
		return ""
	}
	id := ""
	for _, ep := range cfg.Providers.Custom {
		if ep.Enabled && ep.Magpie && strings.TrimRight(strings.TrimSpace(ep.BaseURL), "/") == strings.TrimRight(strings.TrimSpace(baseURL), "/") {
			if id != "" {
				return ""
			}
			id = ep.ID
		}
	}
	return id
}

// engineQuotaWindows 读取引擎会话的额度窗口。key 形如 mcode:<provider id> 或 magpie 与
// magpie2…magpie4 或 custom-<id>；找不到有 key 的 Magpie 端点时按未实现处理。
func (c *Client) engineQuotaWindows(ctx context.Context, key string) ([]mcodeui.ProviderQuotaWindow, error) {
	endpoint, found := c.engineQuotaEndpointForKey(key)
	if !found {
		return nil, fmt.Errorf("ProviderQuota: %w", mcodeui.ErrNotImplemented)
	}
	model := c.magpieQuotaModel(ctx, "mcode", endpoint.endpointID, endpoint.providerID)
	return c.readMagpieQuotaWindows(ctx, endpoint.baseURL, endpoint.apiKey, "", mcodeui.QuotaThread(ctx), model)
}

// refreshEngineQuota 让网关重读端点上的全部额度卡片（手动刷新按钮用）：引擎会话在网关侧
// 没有可用的 agent 名，按账号刷新查不到路由，这里与 pi 的提供方级刷新一样整份重读。
func (c *Client) refreshEngineQuota(ctx context.Context, key string) error {
	endpoint, found := c.engineQuotaEndpointForKey(key)
	if !found {
		return nil
	}
	return magpie.RefreshQuotas(ctx, endpoint.baseURL, endpoint.apiKey, "")
}
