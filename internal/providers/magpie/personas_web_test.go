package magpie

// 本文件测经 web 管理接口读人设：用 httptest 服务器模拟 magpie 的 webGuard（303 + 种
// cookie）与 GET /api/plugins，逐条覆盖正常、插件关掉、插件缺失、鉴权失败、非 200、
// 文案脱敏、缓存命中与失败短缓存。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// personaWebTestKey 是测试用的 web 管理密钥，断言脱敏时也用它。
const personaWebTestKey = "test-web-key-0123456789"

// personaWebTestServer 是模拟 magpie web 管理端口的服务器。
type personaWebTestServer struct {
	*httptest.Server
	hits *int32
}

// newPersonaWebTestServer 起服务：带 k 的请求与 magpie 一样 303 到去掉 k 的同一路径并
// 种 cookie，之后只认 cookie；status 非 0 时直接返回它（模拟鉴权失败等）。
func newPersonaWebTestServer(t *testing.T, key, body string, status int) personaWebTestServer {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/plugins" {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&hits, 1)
		if status != 0 {
			http.Error(w, "boom", status)
			return
		}
		if got := r.URL.Query().Get("k"); got != "" {
			if got != key {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "magpie", Value: key, Path: "/", HttpOnly: true})
			target := *r.URL
			query := target.Query()
			query.Del("k")
			target.RawQuery = query.Encode()
			http.Redirect(w, r, target.String(), http.StatusSeeOther)
			return
		}
		if cookie, err := r.Cookie("magpie"); err != nil || cookie.Value != key {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return personaWebTestServer{Server: srv, hits: &hits}
}

// personaWebPluginBody 拼出 web 接口的插件列表响应：entry 直接给一条插件。
func personaWebPluginBody(t *testing.T, entries ...map[string]any) string {
	t.Helper()
	// nil 切片会被序列化成 null，而真实接口在没有插件时返回空数组，这里补齐成 []。
	if entries == nil {
		entries = []map[string]any{}
	}
	raw, err := json.Marshal(map[string]any{"plugins": entries})
	if err != nil {
		t.Fatalf("生成插件列表失败: %v", err)
	}
	return string(raw)
}

// personaWebEntry 是 persona-rules 插件在 web 接口里的条目。
func personaWebEntry(options map[string]any) map[string]any {
	return map[string]any{
		"spec":    "@magpie-community/middleware-persona-rules@latest",
		"options": options,
	}
}

// resetPersonaWebCache 清空缓存，避免用例之间互相干扰。
func resetPersonaWebCache(t *testing.T) {
	t.Helper()
	InvalidatePersonaCache("")
	t.Cleanup(func() { InvalidatePersonaCache("") })
}

func TestReadWebPersonas(t *testing.T) {
	resetPersonaWebCache(t)
	srv := newPersonaWebTestServer(t, personaWebTestKey, personaWebPluginBody(t, personaWebEntry(map[string]any{
		"personas": []map[string]any{
			{"id": "coder", "name": "编码", "description": "只写代码", "text": "你是资深工程师"},
			{"id": "writer", "description": "写文档", "text": "你是技术写作"},
		},
	})), 0)

	got, err := ReadWebPersonas(context.Background(), PersonaCacheKey("magpie", srv.URL), srv.URL,
		func() string { return personaWebTestKey })
	if err != nil {
		t.Fatalf("ReadWebPersonas 出错: %v", err)
	}
	want := []Persona{
		{ID: "coder", Name: "编码", Description: "只写代码"},
		// 名字为空时回落为 ID。
		{ID: "writer", Name: "writer", Description: "写文档"},
	}
	if len(got) != len(want) {
		t.Fatalf("ReadWebPersonas = %v，想要 %v", personaIDs(got), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 条 = %+v，想要 %+v", i, got[i], want[i])
		}
	}
	// 303 + cookie 让一次读取发两次请求：先带 k 换 cookie，再带 cookie 拿列表。
	if hits := atomic.LoadInt32(srv.hits); hits != 2 {
		t.Fatalf("请求次数 = %d，想要 2（换 cookie + 取列表）", hits)
	}
}

func TestReadWebPersonasPluginOff(t *testing.T) {
	resetPersonaWebCache(t)
	body := personaWebPluginBody(t, map[string]any{
		"spec":    "@magpie-community/middleware-persona-rules@latest",
		"off":     true,
		"options": map[string]any{"personas": []map[string]any{{"id": "coder", "text": "正文"}}},
	})
	srv := newPersonaWebTestServer(t, personaWebTestKey, body, 0)

	got, err := ReadWebPersonas(context.Background(), "", srv.URL, func() string { return personaWebTestKey })
	if err != nil {
		t.Fatalf("插件关掉不该是错误: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("插件关掉时人设 = %v，想要空", personaIDs(got))
	}
}

func TestReadWebPersonasPluginOptionDisabled(t *testing.T) {
	resetPersonaWebCache(t)
	body := personaWebPluginBody(t, personaWebEntry(map[string]any{
		"enabled": false,
		"personas": []map[string]any{
			{"id": "coder", "text": "正文"},
		},
	}))
	srv := newPersonaWebTestServer(t, personaWebTestKey, body, 0)

	got, err := ReadWebPersonas(context.Background(), "", srv.URL, func() string { return personaWebTestKey })
	if err != nil {
		t.Fatalf("options.enabled 为 false 不该是错误: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("options.enabled 为 false 时人设 = %v，想要空", personaIDs(got))
	}
}

func TestReadWebPersonasPluginMissing(t *testing.T) {
	resetPersonaWebCache(t)
	srv := newPersonaWebTestServer(t, personaWebTestKey,
		personaWebPluginBody(t, map[string]any{"spec": "@magpie-community/middleware-something@latest"}), 0)

	got, err := ReadWebPersonas(context.Background(), "", srv.URL, func() string { return personaWebTestKey })
	if err != nil {
		t.Fatalf("没有 persona-rules 条目不该是错误: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("没有 persona-rules 条目时人设 = %v，想要空", personaIDs(got))
	}
}

func TestReadWebPersonasAuthFailed(t *testing.T) {
	resetPersonaWebCache(t)
	srv := newPersonaWebTestServer(t, personaWebTestKey, personaWebPluginBody(t), 0)

	_, err := ReadWebPersonas(context.Background(), "", srv.URL, func() string { return "wrong-key" })
	if err == nil {
		t.Fatal("key 不对时应该返回错误")
	}
	if !strings.Contains(err.Error(), "鉴权失败") {
		t.Fatalf("错误文案 = %q，想要说明鉴权失败", err.Error())
	}
	if strings.Contains(err.Error(), "wrong-key") || strings.Contains(err.Error(), "k=") {
		t.Fatalf("错误文案不能含密钥或带 k= 的地址: %q", err.Error())
	}
}

func TestReadWebPersonasNoKey(t *testing.T) {
	resetPersonaWebCache(t)
	srv := newPersonaWebTestServer(t, personaWebTestKey, personaWebPluginBody(t), 0)
	key := func() string { return "" }

	_, err := ReadWebPersonas(context.Background(), "", srv.URL, key)
	if err == nil {
		t.Fatal("没配密钥时应该返回错误")
	}
	if hits := atomic.LoadInt32(srv.hits); hits != 0 {
		t.Fatalf("没配密钥时不该发请求，实际 %d 次", hits)
	}
}

func TestReadWebPersonasNon200(t *testing.T) {
	resetPersonaWebCache(t)
	srv := newPersonaWebTestServer(t, personaWebTestKey, "", http.StatusInternalServerError)

	_, err := ReadWebPersonas(context.Background(), "", srv.URL, func() string { return personaWebTestKey })
	if err == nil {
		t.Fatal("非 200 响应应该返回错误")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("错误文案 = %q，想要带状态码", err.Error())
	}
}

func TestReadWebPersonasNotPluginList(t *testing.T) {
	resetPersonaWebCache(t)
	// 登录页是 HTML：解不出插件数组时不能当成「没有人设」。
	srv := newPersonaWebTestServer(t, personaWebTestKey, "<html>sign in</html>", 0)

	_, err := ReadWebPersonas(context.Background(), "", srv.URL, func() string { return personaWebTestKey })
	if err == nil {
		t.Fatal("不是插件列表时应该返回错误")
	}
}

func TestReadWebPersonasTooLarge(t *testing.T) {
	resetPersonaWebCache(t)
	huge := strings.Repeat("a", personaWebMaxBytes+1)
	body := `{"plugins":[],"pad":"` + huge + `"}`
	srv := newPersonaWebTestServer(t, personaWebTestKey, body, 0)

	_, err := ReadWebPersonas(context.Background(), "", srv.URL, func() string { return personaWebTestKey })
	if err == nil {
		t.Fatal("超过 4 MiB 的响应应该返回错误")
	}
	if !strings.Contains(err.Error(), "4 MiB") {
		t.Fatalf("错误文案 = %q，想要说明超过大小上限", err.Error())
	}
}

func TestReadWebPersonasErrorOmitsKey(t *testing.T) {
	resetPersonaWebCache(t)
	// 先起一个服务器拿到地址，再关掉它：连接失败的错误文案也不能带密钥。
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	_, err := ReadWebPersonas(context.Background(), "", url, func() string { return personaWebTestKey })
	if err == nil {
		t.Fatal("连不上时应该返回错误")
	}
	if strings.Contains(err.Error(), personaWebTestKey) || strings.Contains(err.Error(), "k=") {
		t.Fatalf("错误文案不能含密钥或带 k= 的地址: %q", err.Error())
	}
}

func TestPersonaWebErrorRedactsKey(t *testing.T) {
	// 底层错误里往往带着完整请求地址（含 ?k=<key>），原样返回就漏密钥了。
	err := personaWebError(
		errors.New("GET http://127.0.0.1:3430/api/plugins?k="+personaWebTestKey+": connection refused"),
		personaWebTestKey,
	)
	got := err.Error()
	if strings.Contains(got, personaWebTestKey) {
		t.Fatalf("错误文案含密钥: %q", got)
	}
	if strings.Contains(got, "?k="+personaWebTestKey) {
		t.Fatalf("错误文案含带 k= 的地址: %q", got)
	}
	if !strings.Contains(got, "无法连接 web 管理地址") {
		t.Fatalf("错误文案 = %q，想要说明连不上", got)
	}
}

func TestRedactWebKey(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		webKey  string
		wantNot []string
	}{
		{name: "查询串", text: "GET http://h/api/plugins?k=abc123 done", webKey: "abc123", wantNot: []string{"abc123"}},
		{name: "裸 key", text: "key abc123 leaked", webKey: "abc123", wantNot: []string{"abc123"}},
		// 转义过的 key 也要清掉：URL 里的 key 往往是 query-escaped 的。
		{name: "转义 key", text: "key a%2Fb leaked", webKey: "a/b", wantNot: []string{"a/b", "a%2Fb"}},
		{name: "没有 key 的文案", text: "connection refused", webKey: "", wantNot: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactWebKey(tc.text, tc.webKey)
			for _, banned := range tc.wantNot {
				if strings.Contains(got, banned) {
					t.Fatalf("redactWebKey(%q) = %q，不该含 %q", tc.text, got, banned)
				}
			}
		})
	}
	if got := redactWebKey(strings.Repeat("x", 300), ""); len(got) > 200 {
		t.Fatalf("redactWebKey 没截短: %d 字节", len(got))
	}
}

func TestReadWebPersonasCacheHit(t *testing.T) {
	resetPersonaWebCache(t)
	srv := newPersonaWebTestServer(t, personaWebTestKey, personaWebPluginBody(t, personaWebEntry(map[string]any{
		"personas": []map[string]any{{"id": "coder", "text": "正文"}},
	})), 0)
	cacheKey := PersonaCacheKey("magpie", srv.URL)
	key := func() string { return personaWebTestKey }

	first, err := ReadWebPersonas(context.Background(), cacheKey, srv.URL, key)
	if err != nil {
		t.Fatalf("第一次读取出错: %v", err)
	}
	hitsAfterFirst := atomic.LoadInt32(srv.hits)
	calls := 0
	second, err := ReadWebPersonas(context.Background(), cacheKey, srv.URL, func() string {
		calls++
		return personaWebTestKey
	})
	if err != nil {
		t.Fatalf("第二次读取出错: %v", err)
	}
	if atomic.LoadInt32(srv.hits) != hitsAfterFirst {
		t.Fatalf("缓存命中时又发了请求：%d → %d", hitsAfterFirst, *srv.hits)
	}
	if calls != 0 {
		t.Fatalf("缓存命中时不该调取密钥函数，实际调用 %d 次", calls)
	}
	if len(first) != 1 || len(second) != 1 || second[0].ID != "coder" {
		t.Fatalf("两次结果不一致: %v / %v", personaIDs(first), personaIDs(second))
	}

	// 改过管理地址就是另一个缓存键，必须重新请求。
	otherKey := PersonaCacheKey("magpie", srv.URL+"x")
	if _, err := ReadWebPersonas(context.Background(), otherKey, srv.URL, key); err != nil {
		t.Fatalf("换缓存键读取出错: %v", err)
	}
	if atomic.LoadInt32(srv.hits) == hitsAfterFirst {
		t.Fatal("换了缓存键不该命中旧结果")
	}
}

func TestReadWebPersonasCachesEmptyResult(t *testing.T) {
	resetPersonaWebCache(t)
	srv := newPersonaWebTestServer(t, personaWebTestKey, personaWebPluginBody(t), 0)
	cacheKey := PersonaCacheKey("magpie", srv.URL)

	if _, err := ReadWebPersonas(context.Background(), cacheKey, srv.URL, func() string { return personaWebTestKey }); err != nil {
		t.Fatalf("第一次读取出错: %v", err)
	}
	hits := atomic.LoadInt32(srv.hits)
	if _, err := ReadWebPersonas(context.Background(), cacheKey, srv.URL, func() string { return personaWebTestKey }); err != nil {
		t.Fatalf("第二次读取出错: %v", err)
	}
	if atomic.LoadInt32(srv.hits) != hits {
		t.Fatalf("空结果的缓存没生效：%d → %d", hits, *srv.hits)
	}
}

func TestReadWebPersonasFailureCacheIsShort(t *testing.T) {
	resetPersonaWebCache(t)
	srv := newPersonaWebTestServer(t, personaWebTestKey, "", http.StatusInternalServerError)
	cacheKey := PersonaCacheKey("magpie", srv.URL)

	if _, err := ReadWebPersonas(context.Background(), cacheKey, srv.URL, func() string { return personaWebTestKey }); err == nil {
		t.Fatal("第一次读取应该失败")
	}
	if _, err := ReadWebPersonas(context.Background(), cacheKey, srv.URL, func() string { return personaWebTestKey }); err == nil {
		t.Fatal("第二次读取应该还是失败")
	}
	if hits := atomic.LoadInt32(srv.hits); hits != 1 {
		t.Fatalf("失败结果应被缓存，实际请求 %d 次", hits)
	}
	if personaWebErrCacheTTL >= personaWebCacheTTL {
		t.Fatalf("失败缓存 %v 应短于成功缓存 %v", personaWebErrCacheTTL, personaWebCacheTTL)
	}

	// 过期后要重新请求：直接改写入时间，免得测试真的等 15 秒。
	personaWebCache.Lock()
	entry := personaWebCache.entries[cacheKey]
	entry.written = time.Now().Add(-2 * personaWebErrCacheTTL)
	personaWebCache.entries[cacheKey] = entry
	personaWebCache.Unlock()
	if _, err := ReadWebPersonas(context.Background(), cacheKey, srv.URL, func() string { return personaWebTestKey }); err == nil {
		t.Fatal("缓存过期后读取应该失败")
	}
	if hits := atomic.LoadInt32(srv.hits); hits != 2 {
		t.Fatalf("缓存过期后应重新请求，实际 %d 次", hits)
	}
}

func TestInvalidatePersonaCache(t *testing.T) {
	resetPersonaWebCache(t)
	srv := newPersonaWebTestServer(t, personaWebTestKey, personaWebPluginBody(t, personaWebEntry(map[string]any{
		"personas": []map[string]any{{"id": "coder", "text": "正文"}},
	})), 0)
	cacheKey := PersonaCacheKey("magpie", srv.URL)

	if _, err := ReadWebPersonas(context.Background(), cacheKey, srv.URL, func() string { return personaWebTestKey }); err != nil {
		t.Fatalf("第一次读取出错: %v", err)
	}
	hits := atomic.LoadInt32(srv.hits)
	InvalidatePersonaCache(cacheKey)
	if _, err := ReadWebPersonas(context.Background(), cacheKey, srv.URL, func() string { return personaWebTestKey }); err != nil {
		t.Fatalf("失效后读取出错: %v", err)
	}
	if atomic.LoadInt32(srv.hits) == hits {
		t.Fatal("失效后应重新请求")
	}
	// key 为空表示全清。
	if _, err := ReadWebPersonas(context.Background(), cacheKey, srv.URL, func() string { return personaWebTestKey }); err != nil {
		t.Fatalf("再次读取出错: %v", err)
	}
	hits = atomic.LoadInt32(srv.hits)
	InvalidatePersonaCache("")
	if _, err := ReadWebPersonas(context.Background(), cacheKey, srv.URL, func() string { return personaWebTestKey }); err != nil {
		t.Fatalf("全清后读取出错: %v", err)
	}
	if atomic.LoadInt32(srv.hits) == hits {
		t.Fatal("全清后应重新请求")
	}
}

func TestPersonaCacheKey(t *testing.T) {
	if got, want := PersonaCacheKey("magpie", "https://h:3430/"), "magpie|https://h:3430"; got != want {
		t.Fatalf("PersonaCacheKey = %q，想要 %q", got, want)
	}
	if PersonaCacheKey("magpie", "https://h:3430") == PersonaCacheKey("magpie", "https://h:3431") {
		t.Fatal("不同管理地址的缓存键必须不同")
	}
	if PersonaCacheKey("a", "https://h") == PersonaCacheKey("b", "https://h") {
		t.Fatal("不同端点的缓存键必须不同")
	}
}

func TestPersonaWebBaseURL(t *testing.T) {
	ok := []string{"http://127.0.0.1:3430", "https://magpie.example.com:3430/", " https://h "}
	for _, raw := range ok {
		if _, err := personaWebBaseURL(raw); err != nil {
			t.Fatalf("personaWebBaseURL(%q) 出错: %v", raw, err)
		}
	}
	bad := []string{"", "magpie.example.com:3430", "ftp://h", "https://h?x=1", "https://h#f", "https://u:p@h"}
	for _, raw := range bad {
		if _, err := personaWebBaseURL(raw); err == nil {
			t.Fatalf("personaWebBaseURL(%q) 应该报错", raw)
		}
	}
}
