package magpie

// 本文件经 magpie web 管理端口读 persona-rules 的人设列表：magpie 部署在远端时本机没有
// 配置文件可读，用 `magpie web` 的管理地址与 key 就能拿到同一份插件配置。
//
// 协议（见 magpie 的 internal/gui/web.go 的 webGuard 与 internal/gui/plugins.go 的
// GET /api/plugins）：GET <webURL>/api/plugins?k=<key> 时服务端先把 key 换成 cookie 并
// 303 到去掉 k 的同一路径，所以这里用带 cookiejar 的 client 跟随重定向，最终拿到
// {"plugins":[…]} 的 JSON。key 与带 k= 的完整 URL 都不能出现在返回的 error 或日志里。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// personaWebTimeout 是读一次 web 管理接口的超时（每轮运行前都会问一次人设）。
	personaWebTimeout = 5 * time.Second
	// personaWebMaxBytes 是响应体上限：插件列表本身不大，4 MiB 足够且能挡住异常响应。
	personaWebMaxBytes = 4 << 20
	// personaWebCacheTTL 是读成功的缓存时长（含「插件已关/不存在」这种空结果）。
	personaWebCacheTTL = 60 * time.Second
	// personaWebErrCacheTTL 是读失败的缓存时长：失败也不该每轮都卡满 5 秒。
	personaWebErrCacheTTL = 15 * time.Second
)

// personaWebCache 是按缓存键分开的 web 人设缓存，写法参照 magpie.go 的 modelsCache。
var personaWebCache = struct {
	sync.Mutex
	entries map[string]personaWebCacheEntry
}{entries: map[string]personaWebCacheEntry{}}

// personaWebCacheEntry 是一条缓存：人设列表或失败原因、有效期与写入时间。
type personaWebCacheEntry struct {
	personas []Persona
	err      error
	ttl      time.Duration
	written  time.Time
}

// webPluginList 是 GET /api/plugins 响应里本文件用到的部分。
type webPluginList struct {
	Plugins []webPluginEntry `json:"plugins"`
}

// webPluginEntry 是插件列表里的一条：magpie 的 plugin.Entry 内嵌，off 与 options 是它的键。
// options 先留原文，等确认是 persona-rules 再解码。
type webPluginEntry struct {
	Spec    string          `json:"spec"`
	Off     bool            `json:"off,omitempty"`
	Options json.RawMessage `json:"options,omitempty"`
}

// PersonaCacheKey 拼出 web 人设缓存的键：同一个端点换了管理地址要分开缓存。
func PersonaCacheKey(endpointID, webURL string) string {
	return strings.TrimSpace(endpointID) + "|" + strings.TrimRight(strings.TrimSpace(webURL), "/")
}

// InvalidatePersonaCache 使某个缓存键失效；key 为空表示全清。保存端点或测试来源后调用。
func InvalidatePersonaCache(key string) {
	personaWebCache.Lock()
	defer personaWebCache.Unlock()
	if key == "" {
		personaWebCache.entries = map[string]personaWebCacheEntry{}
		return
	}
	delete(personaWebCache.entries, key)
}

// ReadWebPersonas 经 web 管理接口读 persona-rules 的人设列表。key 是在真正要发请求时
// 才调用的惰性取密钥函数（避免每轮都访问系统密钥链），返回空串表示没配密钥。
// 插件被关掉或不存在时返回空列表与 nil（界面上照样能选「不用人设」）；只有网络不通、
// 鉴权失败、响应格式不对等才返回 error，文案里不含 key 与带 k= 的完整 URL。
// cacheKey 为空表示不缓存。
func ReadWebPersonas(ctx context.Context, cacheKey, webURL string, key func() string) ([]Persona, error) {
	baseURL, err := personaWebBaseURL(webURL)
	if err != nil {
		return nil, err
	}
	if personas, err, ok := personaWebCacheGet(cacheKey); ok {
		return personas, err
	}
	personas, err := fetchWebPersonas(ctx, baseURL, key)
	personaWebCacheSet(cacheKey, personas, err)
	return personas, err
}

// fetchWebPersonas 发一次请求并把插件列表转成人设：条目 off 为 true 或
// options.enabled 为 false 视为未启用，条目缺失也视为未启用（都是空列表、非错误）。
func fetchWebPersonas(ctx context.Context, baseURL string, key func() string) ([]Persona, error) {
	webKey := ""
	if key != nil {
		webKey = strings.TrimSpace(key())
	}
	if webKey == "" {
		return nil, errors.New("未配置 magpie web 管理密钥")
	}

	ctx, cancel := context.WithTimeout(ctx, personaWebTimeout)
	defer cancel()

	// 服务端会先 303 到去掉 k 的同一路径，cookie 得跟着重定向一起走。
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, errors.New("初始化 web 管理会话失败")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/plugins?k="+url.QueryEscape(webKey), nil)
	if err != nil {
		return nil, errors.New("web 管理地址格式不正确")
	}
	resp, err := (&http.Client{Jar: jar}).Do(req)
	if err != nil {
		return nil, personaWebError(err, webKey)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, errors.New("鉴权失败（key 不对或地址不是 magpie web 管理端口）")
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("web 管理接口返回 HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, personaWebMaxBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("读取 web 管理接口超时")
		}
		return nil, errors.New("读取 web 管理接口失败")
	}
	if len(body) > personaWebMaxBytes {
		return nil, errors.New("web 管理接口的响应超过 4 MiB")
	}
	var list webPluginList
	// 登录页是 HTML、别的接口是别的结构：解不出插件数组都不算拿到了插件列表。
	if err := json.Unmarshal(body, &list); err != nil || list.Plugins == nil {
		return nil, errors.New("响应不是 magpie 的插件列表")
	}

	for _, entry := range list.Plugins {
		if !personaPluginCalled(entry.Spec) {
			continue
		}
		var option pluginOption
		if len(entry.Options) > 0 {
			if err := json.Unmarshal(entry.Options, &option); err != nil {
				return nil, errors.New("响应不是 magpie 的插件列表")
			}
		}
		if entry.Off || (option.Enabled != nil && !*option.Enabled) {
			return nil, nil
		}
		// web 能拿到正文，人设过滤口径与本机读配置一致。
		return collectPersonas(option.Personas), nil
	}
	return nil, nil
}

// personaWebBaseURL 校验并规范化 web 管理地址：只允许 http/https 且必须有主机，
// 去掉末尾斜杠（与 config 里的规范化一致）。
func personaWebBaseURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("web 管理地址必须是 http(s)://主机:端口")
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("web 管理地址必须是 http(s)://主机:端口")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// personaWebError 把 net/http 的错误变成不含 key 与完整 URL 的原因：*url.Error 的文案
// 会带上请求 URL（含 ?k=），这里只保留底层原因并清掉残留的 key。
func personaWebError(err error, webKey string) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("连接 web 管理地址超时")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return errors.New("连接 web 管理地址超时")
	}
	reason := err.Error()
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		reason = urlErr.Err.Error()
	}
	if reason = redactWebKey(reason, webKey); reason == "" {
		reason = "无法连接 web 管理地址"
	}
	return fmt.Errorf("无法连接 web 管理地址：%s", reason)
}

// redactWebKey 清掉文案里可能出现的 key 与 ?k= 查询串，并截短到 200 字节。
func redactWebKey(text, webKey string) string {
	if webKey != "" {
		text = strings.ReplaceAll(text, url.QueryEscape(webKey), "***")
		text = strings.ReplaceAll(text, webKey, "***")
	}
	const marker = "?k="
	var b strings.Builder
	for {
		idx := strings.Index(text, marker)
		if idx < 0 {
			b.WriteString(text)
			break
		}
		b.WriteString(text[:idx])
		b.WriteString("?k=***")
		rest := text[idx+len(marker):]
		end := strings.IndexAny(rest, " \"'\t\n")
		if end < 0 {
			break
		}
		text = rest[end:]
	}
	out := b.String()
	if len(out) > 200 {
		out = out[:200]
	}
	return strings.TrimSpace(out)
}

// personaWebCacheGet 读一条未过期的缓存；ok 为 false 表示没命中（含缓存键为空）。
func personaWebCacheGet(key string) (personas []Persona, err error, ok bool) {
	if key == "" {
		return nil, nil, false
	}
	personaWebCache.Lock()
	defer personaWebCache.Unlock()
	entry, hit := personaWebCache.entries[key]
	if !hit || time.Since(entry.written) >= entry.ttl {
		return nil, nil, false
	}
	return append([]Persona(nil), entry.personas...), entry.err, true
}

// personaWebCacheSet 写入一条缓存；成功与失败用不同的 TTL，缓存键为空时不写。
func personaWebCacheSet(key string, personas []Persona, err error) {
	if key == "" {
		return
	}
	entry := personaWebCacheEntry{
		personas: append([]Persona(nil), personas...),
		err:      err,
		ttl:      personaWebCacheTTL,
		written:  time.Now(),
	}
	if err != nil {
		entry.personas = nil
		entry.ttl = personaWebErrCacheTTL
	}
	personaWebCache.Lock()
	defer personaWebCache.Unlock()
	personaWebCache.entries[key] = entry
}
