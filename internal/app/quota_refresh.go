package app

// 本文件在每一轮对话结束后按会话刷新 Magpie 的额度卡片：网关只重读本会话实际应答
// 的那个账号，不碰其它账号。pi 按端点 key（pi:<pi provider id>）刷新，claude 与 codex
// 按提供方名刷新；查不到路由或直连提供方时什么都不做。

import (
	"context"
	"strings"

	"mcode/internal/providers/claude"
	"mcode/internal/providers/magpie"
	mcodeui "mcode/internal/ui"
)

// markRouteActive 只给本会话实际应答的账号对应的窗口标「在用」：provider 与账号都要
// 相等，账号为空或查不到路由时都不标，避免同一 provider 的其它账号被误标。
func markRouteActive(windows []mcodeui.ProviderQuotaWindow, route sessionRoute, ok bool) {
	if !ok {
		return
	}
	user := strings.TrimSpace(route.User)
	if user == "" {
		return
	}
	for i := range windows {
		w := &windows[i]
		if w.User == "" || !strings.EqualFold(w.Provider, route.Provider) || !strings.EqualFold(w.User, user) {
			continue
		}
		w.Active = true
	}
}

// RefreshThreadQuota 让 Magpie 网关只重读指定会话实际应答账号的额度卡片。
// provider 是额度 key：pi:<pi provider id>、mcode:<端点 provider id>、magpie 与
// magpie2…magpie4、claude 或 codex；其它提供方、直连提供方、找不到端点、会话没有
// trace 路由或路由缺账号时都不发请求，也不猜账号；同一「端点 + provider + user」
// 30 秒内只刷新一次，与网关服务端的去重窗口一致。
func (c *Client) RefreshThreadQuota(ctx context.Context, threadID, provider string) error {
	if c == nil {
		return nil
	}
	provider = strings.TrimSpace(provider)
	threadID = strings.TrimSpace(threadID)
	// 内置引擎的会话键就是线程 ID，不需要落盘的 provider 会话 ID。
	if threadID == "" || (c.agySessionForThread == nil && !engineQuotaKey(provider)) {
		return nil
	}
	switch {
	case engineQuotaKey(provider):
		endpoint, found := c.engineQuotaEndpointForKey(provider)
		if !found {
			return nil
		}
		return c.refreshRouteSession(ctx, endpoint.baseURL, endpoint.apiKey, "", threadID)
	case strings.HasPrefix(provider, "pi:"):
		providerID := strings.TrimSpace(strings.TrimPrefix(provider, "pi:"))
		if providerID == "" {
			return nil
		}
		target, found := c.piMagpieTargetFor(providerID)
		if !found {
			return nil
		}
		return c.refreshRouteAccount(ctx, target.baseURL, target.apiKey, "pi", threadID)
	case provider == "claude":
		baseURL, token, ok := claude.GatewayEndpoint(ctx)
		if !ok {
			return nil
		}
		return c.refreshRouteAccount(ctx, baseURL, token, "claude", threadID)
	case provider == "codex":
		baseURL, token, ok, _, err := c.codexMagpieEndpoint(ctx, threadID)
		if err != nil || !ok {
			return nil
		}
		return c.refreshRouteAccount(ctx, baseURL, token, "codex", threadID)
	default:
		return nil
	}
}

// refreshRouteAccount 按会话的 provider 会话 ID 查实际应答账号，再让网关只重读这个
// 账号。agent 是 pi、claude 或 codex；查不到路由或路由缺账号时什么都不做。
func (c *Client) refreshRouteAccount(ctx context.Context, baseURL, token, agent, threadID string) error {
	sessionID := strings.TrimSpace(c.agySessionForThread(ctx, threadID))
	return c.refreshRouteSession(ctx, baseURL, token, agent, sessionID)
}

// refreshRouteSession 是刷新单个账号的主体：sessionID 直接是 Magpie trace 里的会话键
// （pi、claude、codex 是落盘的 provider 会话 ID，内置引擎是线程 ID）。
func (c *Client) refreshRouteSession(ctx context.Context, baseURL, token, agent, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	tracker := c.sessionTracker()
	tracker.noteSource(baseURL, token)
	// 一轮对话刚结束，缓存里可能是故障转移前的账号，这里强制重读一次；30 秒去重
	// 仍生效，避免连续轮次把远程网关打爆。
	route, hasRoute := tracker.routeFor(ctx, baseURL, token, agent, sessionID, true)
	if !hasRoute || strings.TrimSpace(route.User) == "" {
		return nil
	}
	if !tracker.allowRefresh(traceSourceKey(baseURL), route.Provider, route.User) {
		return nil
	}
	return magpie.RefreshQuotasFor(ctx, baseURL, token, route.Provider, route.User)
}
