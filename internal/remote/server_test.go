package remote

import (
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"

	"mcode/internal/testenv"
)

var testAssets = fstest.MapFS{
	"remote-index.html": {
		Data: []byte("<!DOCTYPE html><html><head><title>Remote</title></head><body>Remote App Index</body></html>"),
	},
	"remote-app.js": {
		Data: []byte("console.log('remote app payload');\n" + strings.Repeat("/* padding to exceed 1024 bytes */\n", 40)),
	},
	"remote-app.css": {
		Data: []byte("body { background: #121212; color: #fff; }"),
	},
	"remote-manifest.webmanifest": {
		Data: []byte(`{"name":"RemoteApp"}`),
	},
	"remote-boot.js": {
		Data: []byte("console.log('remote boot loader');"),
	},
	"remote-sw.js": {
		Data: []byte("// service worker stub"),
	},
	"remote-icon.svg": {
		Data: []byte("<svg viewBox='0 0 100 100'></svg>"),
	},
	"apple-touch-icon-180.png": {
		Data: []byte("\x89PNG\r\n\x1a\napple touch icon"),
	},
	"icon-192.png": {
		Data: []byte("\x89PNG\r\n\x1a\nicon 192"),
	},
	"icon-512.png": {
		Data: []byte("\x89PNG\r\n\x1a\nicon 512"),
	},
	"icon-maskable-512.png": {
		Data: []byte("\x89PNG\r\n\x1a\nmaskable icon 512"),
	},
	"assets/vendor-react-Bx1y2z.js": {
		Data: []byte("console.log('vendor react chunk');"),
	},
	"assets/katex-a1b2c3.woff2": {
		Data: []byte("wOF2 katex font payload"),
	},
}

func TestAcceptsGzipHeader(t *testing.T) {
	tests := []struct {
		header string
		want   bool
	}{
		{"gzip", true},
		{"gzip;q=0", false},
		{"gzip;q=0.0", false},
		{"GZIP;Q=0", false},
		{"gzip;q=0.5", true},
		{"*", true},
		{"*;q=0", false},
		{"br", false},
		{"", false},
		{"*;q=1, gzip;q=0", false},
		{"gzip;q=0, *;q=1", false},
		{"br, *; Q=0.5", true},
	}
	for _, tt := range tests {
		t.Run(tt.header, func(t *testing.T) {
			if got := acceptsGzipHeader(tt.header); got != tt.want {
				t.Errorf("acceptsGzipHeader(%q) = %v, want %v", tt.header, got, tt.want)
			}
		})
	}
}

func TestServer_StaticRoutesAndDirectoryTraversal(t *testing.T) {
	server := NewServer(ServerOptions{
		Assets:       testAssets,
		Entry:        EntryLAN,
		AllowedHosts: []string{"127.0.0.1", "localhost"},
	})
	ts := httptest.NewServer(server)
	defer ts.Close()

	client := ts.Client()

	// 1. 验证 GET / 与 GET /index.html 均重定向至 remote-index.html
	for _, path := range []string{"/", "/index.html"} {
		resp, err := client.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s failed: %v", path, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 for %s, got %d", path, resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if !strings.Contains(string(body), "Remote App Index") {
			t.Fatalf("unexpected content for %s: %s", path, string(body))
		}
		// 校验 CSP 与安全头
		if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
			t.Fatalf("missing or invalid CSP header: %s", csp)
		}
		if nosniff := resp.Header.Get("X-Content-Type-Options"); nosniff != "nosniff" {
			t.Fatalf("missing nosniff header: %s", nosniff)
		}
		if cacheControl := resp.Header.Get("Cache-Control"); cacheControl != "no-cache" {
			t.Fatalf("missing no-cache header: %s", cacheControl)
		}
	}

	// 2. 验证其他白名单静态文件
	for _, path := range []string{"/remote-boot.js", "/remote-app.js", "/remote-app.css", "/remote-manifest.webmanifest", "/remote-sw.js", "/remote-icon.svg", "/apple-touch-icon-180.png", "/icon-192.png", "/icon-512.png", "/icon-maskable-512.png"} {
		resp, err := client.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s failed: %v", path, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 for %s, got %d", path, resp.StatusCode)
		}
		if cacheControl := resp.Header.Get("Cache-Control"); cacheControl != "no-cache" {
			t.Fatalf("missing no-cache header for %s: %s", path, cacheControl)
		}
		_ = resp.Body.Close()
	}

	// 2.1 图标 PNG 必须映射到同名文件并返回 image/png（iOS/Android 图标不接受 SVG）
	for _, name := range []string{"/apple-touch-icon-180.png", "/icon-192.png", "/icon-512.png", "/icon-maskable-512.png"} {
		if mapped, ok := staticRouteMap[name]; !ok || mapped != strings.TrimPrefix(name, "/") {
			t.Fatalf("staticRouteMap[%s] = %q, ok=%v; want same-name file", name, mapped, ok)
		}
	}
	resp, err := client.Get(ts.URL + "/icon-192.png")
	if err != nil {
		t.Fatalf("GET /icon-192.png failed: %v", err)
	}
	if contentType := resp.Header.Get("Content-Type"); contentType != "image/png" {
		t.Fatalf("expected image/png for /icon-192.png, got %s", contentType)
	}
	_ = resp.Body.Close()

	// 3. 验证非白名单路径返回 404
	resp, err = client.Get(ts.URL + "/unknown.file")
	if err != nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown file, got %v, status=%d", err, resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 4. 验证路径穿越 (..) 与反斜杠 (\) 拒绝并返回 400
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/remote-app.js/../remote-index.html", nil)
	resp, err = client.Do(req)
	if err != nil || (resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound) {
		t.Fatalf("expected 400 for traversal path, got %v, status=%d", err, resp.StatusCode)
	}
	if resp != nil {
		_ = resp.Body.Close()
	}

	// 5. 验证非 GET/HEAD 请求返回 405
	postResp, err := client.Post(ts.URL+"/", "text/plain", strings.NewReader("bad"))
	if err != nil || postResp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for POST, got %v, status=%d", err, postResp.StatusCode)
	}
	if allow := postResp.Header.Get("Allow"); !strings.Contains(allow, "GET") {
		t.Fatalf("expected Allow header on 405 response")
	}
	_ = postResp.Body.Close()
}

func TestServer_AssetChunkRoutes(t *testing.T) {
	server := NewServer(ServerOptions{
		Assets:       testAssets,
		Entry:        EntryLAN,
		AllowedHosts: []string{"127.0.0.1", "localhost"},
	})
	ts := httptest.NewServer(server)
	defer ts.Close()

	client := ts.Client()

	// 1. 合法 chunk 与字体：200、正确 Content-Type、不可变长缓存
	cases := []struct {
		path        string
		contentType string
		body        string
	}{
		{"/assets/vendor-react-Bx1y2z.js", "text/javascript; charset=utf-8", "vendor react chunk"},
		{"/assets/katex-a1b2c3.woff2", "font/woff2", "katex font payload"},
	}
	for _, tc := range cases {
		resp, err := client.Get(ts.URL + tc.path)
		if err != nil {
			t.Fatalf("GET %s failed: %v", tc.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 for %s, got %d", tc.path, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); ct != tc.contentType {
			t.Fatalf("unexpected Content-Type for %s: %s", tc.path, ct)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
			t.Fatalf("expected immutable cache header for %s, got %s", tc.path, cc)
		}
		if !strings.Contains(string(body), tc.body) {
			t.Fatalf("unexpected body for %s: %s", tc.path, string(body))
		}
	}

	// 2. 非法路径：穿越、编码穿越、子目录、不在名单内的扩展名全部拒绝
	for _, path := range []string{
		"/assets/../remote-index.html",
		"/assets/%2e%2e/x.js",
		"/assets/a/b.js",
		"/assets/vendor-react-Bx1y2z.js.map",
		"/assets/page-abc123.html",
		"/assets/./vendor-react-Bx1y2z.js",
		"/assets/",
	} {
		req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		if err != nil {
			t.Fatalf("build request %s failed: %v", path, err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s failed: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected 400 or 404 for %s, got %d", path, resp.StatusCode)
		}
	}

	// 3. 不存在的 chunk：404，交给启动诊断走刷新恢复
	resp, err := client.Get(ts.URL + "/assets/missing-chunk-deadbeef.js")
	if err != nil {
		t.Fatalf("GET missing chunk failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for missing chunk, got %d", resp.StatusCode)
	}

	// 4. 固定路由不受影响：仍是 200 + no-cache
	resp, err = client.Get(ts.URL + "/remote-app.js")
	if err != nil {
		t.Fatalf("GET /remote-app.js failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for /remote-app.js, got %d", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("expected no-cache for /remote-app.js, got %s", cc)
	}
}

func TestServer_ETagAndGzip(t *testing.T) {
	server := NewServer(ServerOptions{
		Assets:       testAssets,
		Entry:        EntryLAN,
		AllowedHosts: []string{"127.0.0.1", "localhost"},
	})
	ts := httptest.NewServer(server)
	defer ts.Close()

	client := ts.Client()

	// 首次读取获取 ETag
	resp, err := client.Get(ts.URL + "/remote-app.js")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	etag := resp.Header.Get("ETag")
	_ = resp.Body.Close()
	if etag == "" {
		t.Fatalf("expected non-empty ETag")
	}

	// 带 If-None-Match 请求返回 304
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/remote-app.js", nil)
	req.Header.Set("If-None-Match", etag)
	resp304, err := client.Do(req)
	if err != nil || resp304.StatusCode != http.StatusNotModified {
		t.Fatalf("expected 304 Not Modified, got %v, status=%d", err, resp304.StatusCode)
	}
	_ = resp304.Body.Close()

	// Gzip 压缩：请求带 Accept-Encoding: gzip (资源 > 1KB)
	reqGzip, _ := http.NewRequest(http.MethodGet, ts.URL+"/remote-app.js", nil)
	reqGzip.Header.Set("Accept-Encoding", "gzip")
	// 关闭 Go 自带传输层的自动解压以检验服务器输出的真实 Header
	transport := &http.Transport{DisableCompression: true}
	rawClient := &http.Client{Transport: transport}
	respGzip, err := rawClient.Do(reqGzip)
	if err != nil {
		t.Fatalf("GET gzip failed: %v", err)
	}
	defer respGzip.Body.Close()

	if respGzip.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("expected Content-Encoding: gzip, got %s", respGzip.Header.Get("Content-Encoding"))
	}
	gzReader, err := gzip.NewReader(respGzip.Body)
	if err != nil {
		t.Fatalf("gzip NewReader failed: %v", err)
	}
	decompressed, err := io.ReadAll(gzReader)
	if err != nil {
		t.Fatalf("decompress failed: %v", err)
	}
	if !strings.Contains(string(decompressed), "remote app payload") {
		t.Fatalf("decompressed content mismatch")
	}
}

func TestServer_MissingAssets_Returns503(t *testing.T) {
	// 空 Assets 模拟静态资源未打包
	server := NewServer(ServerOptions{
		Assets:       fstest.MapFS{},
		Entry:        EntryLAN,
		AllowedHosts: []string{"127.0.0.1", "localhost"},
	})
	ts := httptest.NewServer(server)
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/")
	if err != nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable when asset is missing, got %v, status=%d", err, resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestServer_ForwardedHeaders_Rejected403(t *testing.T) {
	server := NewServer(ServerOptions{
		Assets:       testAssets,
		Entry:        EntryLAN,
		AllowedHosts: []string{"127.0.0.1", "localhost"},
	})
	ts := httptest.NewServer(server)
	defer ts.Close()

	client := ts.Client()

	forwardingHeaders := []string{
		"Forwarded",
		"X-Forwarded-For",
		"X-Forwarded-Host",
		"X-Forwarded-Port",
		"X-Forwarded-Proto",
	}

	for _, h := range forwardingHeaders {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
		req.Header.Set(h, "10.0.0.1")
		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for header %s, got %v, status=%d", h, err, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
}

func TestServer_HostAndOrigin_Validation(t *testing.T) {
	server := NewServer(ServerOptions{
		Assets:       testAssets,
		Entry:        EntryLAN,
		AllowedHosts: []string{"127.0.0.1", "localhost"},
	})
	ts := httptest.NewServer(server)
	defer ts.Close()

	client := ts.Client()

	// 1. 非法 Host 拒绝 403
	reqBadHost, _ := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	reqBadHost.Host = "evil.attacker.com"
	resp, err := client.Do(reqBadHost)
	if err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for untrusted Host, got %v, status=%d", err, resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 2. 非法 Origin 拒绝 403
	reqBadOrigin, _ := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	reqBadOrigin.Header.Set("Origin", "http://evil.com")
	resp, err = client.Do(reqBadOrigin)
	if err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for mismatched Origin, got %v, status=%d", err, resp.StatusCode)
	}
	_ = resp.Body.Close()
}

// TestServer_HostAndOrigin_FQDNRootDot 验证 AllowedHosts 为 FQDN 时，
// 带末尾根点的 Host/Origin（如 https://mcode.example.ts.net./）与不带点的一样通过校验，
// 其他域名仍被拒绝。
func TestServer_HostAndOrigin_FQDNRootDot(t *testing.T) {
	serveStatus := func(t *testing.T, entry Entry, listenAddr, host, origin string) int {
		t.Helper()
		server := NewServer(ServerOptions{
			Assets:       testAssets,
			ListenAddr:   listenAddr,
			Entry:        entry,
			AllowedHosts: []string{"mcode.example.ts.net"},
		})
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = host
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec.Code
	}

	cases := []struct {
		name       string
		entry      Entry
		listenAddr string
		host       string
		origin     string
		want       int
	}{
		{
			name:       "tailscale accepts host with root dot",
			entry:      EntryTailscale,
			listenAddr: "0.0.0.0:443",
			host:       "mcode.example.ts.net.",
			want:       http.StatusOK,
		},
		{
			name:       "tailscale accepts host and origin with root dot",
			entry:      EntryTailscale,
			listenAddr: "0.0.0.0:443",
			host:       "mcode.example.ts.net.",
			origin:     "https://mcode.example.ts.net.",
			want:       http.StatusOK,
		},
		{
			name:       "tailscale mixed root dot host and plain origin",
			entry:      EntryTailscale,
			listenAddr: "0.0.0.0:443",
			host:       "mcode.example.ts.net.",
			origin:     "https://mcode.example.ts.net",
			want:       http.StatusOK,
		},
		{
			name:       "tailscale still accepts host without root dot",
			entry:      EntryTailscale,
			listenAddr: "0.0.0.0:443",
			host:       "mcode.example.ts.net",
			origin:     "https://mcode.example.ts.net",
			want:       http.StatusOK,
		},
		{
			name:       "tailscale rejects other host with root dot",
			entry:      EntryTailscale,
			listenAddr: "0.0.0.0:443",
			host:       "evil.attacker.com.",
			want:       http.StatusForbidden,
		},
		{
			name:       "tailscale rejects other origin with root dot",
			entry:      EntryTailscale,
			listenAddr: "0.0.0.0:443",
			host:       "mcode.example.ts.net",
			origin:     "https://evil.attacker.com.",
			want:       http.StatusForbidden,
		},
		{
			name:       "lan accepts host and origin with root dot",
			entry:      EntryLAN,
			listenAddr: "127.0.0.1:8080",
			host:       "mcode.example.ts.net.",
			origin:     "http://mcode.example.ts.net.",
			want:       http.StatusOK,
		},
		{
			name:       "lan rejects other host with root dot",
			entry:      EntryLAN,
			listenAddr: "127.0.0.1:8080",
			host:       "evil.attacker.com.",
			want:       http.StatusForbidden,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := serveStatus(t, tc.entry, tc.listenAddr, tc.host, tc.origin); got != tc.want {
				t.Fatalf("ServeHTTP status = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestExtractClientIP 验证各入口对端地址都走同一套清洗逻辑：
// 去端口、方括号、IPv6 zone 与 IPv4 映射前缀。
func TestExtractClientIP(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		want       string
	}{
		{
			name:       "tailnet ipv4 with port",
			remoteAddr: "100.64.0.5:1234",
			want:       "100.64.0.5",
		},
		{
			name:       "ipv4 mapped ipv6 with port",
			remoteAddr: "[::ffff:100.64.0.5]:1234",
			want:       "100.64.0.5",
		},
		{
			name:       "ipv6 with zone and port",
			remoteAddr: "[fd7a:115c:a1e0::1%eth0]:1234",
			want:       "fd7a:115c:a1e0::1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractClientIP(tc.remoteAddr); got != tc.want {
				t.Fatalf("extractClientIP(%q) = %q, want %q", tc.remoteAddr, got, tc.want)
			}
		})
	}
}

type mockEchoHandler struct{}

func (m *mockEchoHandler) Handle(ctx context.Context, c Conn, raw []byte) []byte {
	return []byte(fmt.Sprintf(`{"echo":%s}`, string(raw)))
}

func TestServer_WebSocketRoundtripAndNotify(t *testing.T) {
	server := NewServer(ServerOptions{
		Handler:      &mockEchoHandler{},
		Assets:       testAssets,
		Entry:        EntryLAN,
		AllowedHosts: []string{"127.0.0.1", "localhost"},
	})
	ts := httptest.NewServer(server)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/ws"
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("websocket Dial failed: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	// 发送消息并接收 Echo 响应
	sendMsg := `{"method":"ping"}`
	if err := c.Write(ctx, websocket.MessageText, []byte(sendMsg)); err != nil {
		t.Fatalf("ws Write failed: %v", err)
	}

	typ, received, err := c.Read(ctx)
	if err != nil || typ != websocket.MessageText {
		t.Fatalf("ws Read failed: %v", err)
	}
	if !strings.Contains(string(received), `{"echo":{"method":"ping"}}`) {
		t.Fatalf("unexpected ws echo response: %s", string(received))
	}

	// 验证 Notify 推送
	server.connMu.RLock()
	var firstConn Conn
	for sc := range server.conns {
		firstConn = sc
		break
	}
	server.connMu.RUnlock()

	if firstConn == nil {
		t.Fatalf("no active server connection found")
	}

	if err := firstConn.Notify("threads.event", map[string]string{"state": "running"}); err != nil {
		t.Fatalf("Notify failed: %v", err)
	}

	_, notifData, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("Read notification failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(notifData, &parsed); err != nil {
		t.Fatalf("unmarshal notification failed: %v", err)
	}
	if parsed["jsonrpc"] != "2.0" || parsed["method"] != "threads.event" {
		t.Fatalf("invalid notification payload: %s", string(notifData))
	}
}

func TestServer_UnauthenticatedTimeout(t *testing.T) {
	// 使用可注入的极短超时测试（50ms）
	server := NewServer(ServerOptions{
		Assets:        testAssets,
		Entry:         EntryLAN,
		AllowedHosts:  []string{"127.0.0.1", "localhost"},
		UnauthTimeout: 50 * time.Millisecond,
	})
	ts := httptest.NewServer(server)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/ws"
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("websocket Dial failed: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	// 不调用 MarkAuthenticated，等待 100ms 超时触发关闭
	time.Sleep(100 * time.Millisecond)

	// 此时连接应当已被服务端断开，读取会返回错误
	_, _, err = c.Read(ctx)
	if err == nil {
		t.Fatalf("expected connection to be closed by unauthenticated timeout")
	}
}

func TestServer_MaxConnectionsLimit(t *testing.T) {
	// 设置上限为 2 个并发连接
	server := NewServer(ServerOptions{
		Assets:         testAssets,
		Entry:          EntryLAN,
		AllowedHosts:   []string{"127.0.0.1", "localhost"},
		MaxConnections: 2,
	})
	ts := httptest.NewServer(server)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/ws"

	c1, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Dial 1 failed: %v", err)
	}
	defer c1.Close(websocket.StatusNormalClosure, "")

	c2, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Dial 2 failed: %v", err)
	}
	defer c2.Close(websocket.StatusNormalClosure, "")

	// 第 3 个连接必须被拒绝
	_, resp, err := websocket.Dial(ctx, wsURL, nil)
	if err == nil {
		t.Fatalf("expected 3rd connection to be rejected by MaxConnections limit")
	}
	if resp != nil && resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on connection overflow, got status=%d", resp.StatusCode)
	}
}

func TestServer_BackpressureQueueFullClose(t *testing.T) {
	testenv.TempHome(t)
	// 设置发送队列容量为 1
	server := NewServer(ServerOptions{
		Assets:        testAssets,
		Entry:         EntryLAN,
		AllowedHosts:  []string{"127.0.0.1", "localhost"},
		SendQueueSize: 1,
	})
	ts := httptest.NewServer(server)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/ws"
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	var target *wsConn
	// Dial 返回只代表升级握手结束，服务端可能尚未完成 addConn。
	for target == nil {
		server.connMu.RLock()
		for sc := range server.conns {
			target = sc
			break
		}
		server.connMu.RUnlock()
		if target != nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for server connection registration")
		case <-time.After(time.Millisecond):
		}
	}

	// 客户端不读取消息，服务端循环发送约 64KB 载荷打满内核缓冲区与发送队列，触发背压熔断
	deadline := time.Now().Add(5 * time.Second)
	largePayload := strings.Repeat("A", 64*1024)
	var queueFullErr error
	for {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for send queue to fill and trigger backpressure")
		}
		err := target.Notify("test.burst", largePayload)
		if err != nil {
			queueFullErr = err
			break
		}
	}

	if queueFullErr == nil {
		t.Fatalf("expected send queue full error when bursting messages")
	}

	// 等待连接关闭生效并验证客户端感知到连接断开
	time.Sleep(50 * time.Millisecond)
	_, _, err = c.Read(ctx)
	if err == nil {
		t.Fatalf("expected client to observe connection termination after backpressure trigger")
	}
}

func TestServer_HTTPTimeoutsConfigured(t *testing.T) {
	server := NewServer(ServerOptions{Entry: EntryLAN, AllowedHosts: []string{"127.0.0.1", "localhost"}})
	srv := server.newHTTPServer(nil)

	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Fatalf("expected 10s ReadHeaderTimeout, got %v", srv.ReadHeaderTimeout)
	}
	if srv.IdleTimeout != 120*time.Second {
		t.Fatalf("expected 120s IdleTimeout, got %v", srv.IdleTimeout)
	}
	// WriteTimeout 按整条连接计时，会掐断需要长期推送的 WebSocket，必须保持未设置
	if srv.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout must stay unset to keep websocket connections alive, got %v", srv.WriteTimeout)
	}
}

func TestServer_UnauthenticatedConnectionsHaveSeparateQuota(t *testing.T) {
	server := NewServer(ServerOptions{MaxConnections: 8})

	// 未认证连接占满独立配额后直接拒绝，不能再像以前那样挤占已认证配额
	for i := 0; i < maxUnauthConnections; i++ {
		server.conns[&wsConn{}] = struct{}{}
	}
	if server.reserveConn() {
		t.Fatalf("expected rejection once %d unauthenticated connections are open", maxUnauthConnections)
	}
	for c := range server.conns {
		delete(server.conns, c)
	}

	// 已认证连接走 MaxConnections 配额：未认证配额不影响它们继续接入
	for i := 0; i < maxUnauthConnections; i++ {
		c := &wsConn{}
		c.info.Authenticated = true
		server.conns[c] = struct{}{}
	}
	if !server.reserveConn() {
		t.Fatalf("expected reservation to succeed while the authenticated quota has room")
	}
	server.releaseReservation()

	// 已认证连接达到 MaxConnections 后才拒绝新连接
	for i := maxUnauthConnections; i < 8; i++ {
		c := &wsConn{}
		c.info.Authenticated = true
		server.conns[c] = struct{}{}
	}
	if server.reserveConn() {
		t.Fatalf("expected rejection once the authenticated quota is full")
	}
}

func TestServer_PingKeepsHealthyConnection(t *testing.T) {
	server := NewServer(ServerOptions{
		Handler:       &mockEchoHandler{},
		Assets:        testAssets,
		Entry:         EntryLAN,
		AllowedHosts:  []string{"127.0.0.1", "localhost"},
		PingInterval:  40 * time.Millisecond,
		PongTimeout:   60 * time.Millisecond,
		UnauthTimeout: 5 * time.Second,
	})
	ts := httptest.NewServer(server)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/ws"
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("websocket Dial failed: %v", err)
	}
	// 客户端持续读取才会回 pong
	received := make(chan []byte, 8)
	go func() {
		for {
			_, p, readErr := c.Read(ctx)
			if readErr != nil {
				return
			}
			select {
			case received <- p:
			default:
			}
		}
	}()
	defer c.Close(websocket.StatusNormalClosure, "")

	target := waitForServerConn(t, ctx, server)
	time.Sleep(300 * time.Millisecond) // 跨过至少两轮 ping

	if err := target.Notify("threads.event", map[string]string{"state": "running"}); err != nil {
		t.Fatalf("healthy connection should survive ping rounds, got %v", err)
	}
	select {
	case p := <-received:
		if !strings.Contains(string(p), "threads.event") {
			t.Fatalf("unexpected notification payload: %s", string(p))
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for notification on a healthy connection")
	}
}

func TestServer_PingClosesUnresponsiveConnection(t *testing.T) {
	server := NewServer(ServerOptions{
		Assets:        testAssets,
		Entry:         EntryLAN,
		AllowedHosts:  []string{"127.0.0.1", "localhost"},
		PingInterval:  40 * time.Millisecond,
		PongTimeout:   60 * time.Millisecond,
		UnauthTimeout: 5 * time.Second,
	})
	ts := httptest.NewServer(server)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/ws"
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("websocket Dial failed: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	// 客户端不读取也就不会回 pong（半开连接），连续两轮错过 pong 后服务端应主动断开
	time.Sleep(400 * time.Millisecond)
	if _, _, err := c.Read(ctx); err == nil {
		t.Fatalf("expected server to close connection that never answers ping")
	}
}

func waitForServerConn(t *testing.T, ctx context.Context, server *Server) *wsConn {
	t.Helper()
	for {
		server.connMu.RLock()
		var target *wsConn
		for sc := range server.conns {
			target = sc
			break
		}
		server.connMu.RUnlock()
		if target != nil {
			return target
		}
		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for server connection registration")
		case <-time.After(time.Millisecond):
		}
	}
}

// TestServer_HostAndOrigin_SecureEntryPortNormalization 覆盖安全入口（TLS / Tailscale）
// 的 Host 与 Origin 端口归一化：监听 443 时 Host 不带端口应通过，其他端口必须带端口。
func TestServer_HostAndOrigin_SecureEntryPortNormalization(t *testing.T) {
	serveStatus := func(t *testing.T, entry Entry, listenAddr, host, origin string) int {
		t.Helper()
		server := NewServer(ServerOptions{
			Assets:       testAssets,
			ListenAddr:   listenAddr,
			Entry:        entry,
			AllowedHosts: []string{"mcode.example.ts.net"},
		})
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = host
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec.Code
	}

	cases := []struct {
		name       string
		entry      Entry
		listenAddr string
		host       string
		origin     string
		want       int
	}{
		{
			name:       "tls on 443 accepts host without port",
			entry:      EntryTLS,
			listenAddr: "0.0.0.0:443",
			host:       "mcode.example.ts.net",
			want:       http.StatusOK,
		},
		{
			name:       "tls on 8443 rejects host without port",
			entry:      EntryTLS,
			listenAddr: "0.0.0.0:8443",
			host:       "mcode.example.ts.net",
			want:       http.StatusForbidden,
		},
		{
			name:       "tls on 8443 accepts host with port",
			entry:      EntryTLS,
			listenAddr: "0.0.0.0:8443",
			host:       "mcode.example.ts.net:8443",
			want:       http.StatusOK,
		},
		{
			name:       "tailscale on 443 accepts host without port",
			entry:      EntryTailscale,
			listenAddr: "0.0.0.0:443",
			host:       "mcode.example.ts.net",
			want:       http.StatusOK,
		},
		{
			name:       "tailscale accepts https origin",
			entry:      EntryTailscale,
			listenAddr: "0.0.0.0:443",
			host:       "mcode.example.ts.net",
			origin:     "https://mcode.example.ts.net",
			want:       http.StatusOK,
		},
		{
			name:       "tailscale rejects http origin",
			entry:      EntryTailscale,
			listenAddr: "0.0.0.0:443",
			host:       "mcode.example.ts.net",
			origin:     "http://mcode.example.ts.net",
			want:       http.StatusForbidden,
		},
		{
			name:       "tailscale rejects origin with mismatched port",
			entry:      EntryTailscale,
			listenAddr: "0.0.0.0:8443",
			host:       "mcode.example.ts.net:8443",
			origin:     "https://mcode.example.ts.net",
			want:       http.StatusForbidden,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := serveStatus(t, tc.entry, tc.listenAddr, tc.host, tc.origin); got != tc.want {
				t.Fatalf("ServeHTTP status = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestServer_ServeTLSListener 用已完成握手的 TLS listener（模拟 tsnet 的 ListenTLS）
// 验证普通 https 请求与 /ws 升级都可用，且不会二次包 TLS。
func TestServer_ServeTLSListener(t *testing.T) {
	// 用 httptest 生成的自签证书，手工构造一个已完成握手的 listener。
	certSrv := httptest.NewUnstartedServer(http.NotFoundHandler())
	certSrv.StartTLS()
	cfg := certSrv.TLS.Clone()
	certSrv.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer ln.Close()

	srv := NewServer(ServerOptions{
		Handler:      &mockEchoHandler{},
		Assets:       testAssets,
		ListenAddr:   ln.Addr().String(),
		Entry:        EntryTailscale,
		AllowedHosts: []string{ln.Addr().String()},
	})

	tlsLn := tls.NewListener(ln, cfg)
	go func() {
		_ = srv.ServeTLSListener(tlsLn, &tls.Config{})
	}()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer func() {
		shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
		Timeout: 3 * time.Second,
	}

	resp, err := client.Get("https://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatalf("GET / over completed TLS listener failed: %v", err)
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		t.Fatalf("read response body: %v", readErr)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Remote App Index") {
		t.Fatalf("unexpected response: status=%d body=%s", resp.StatusCode, string(body))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "wss://" + ln.Addr().String() + "/ws"
	wsClient, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		t.Fatalf("websocket Dial over completed TLS listener failed: %v", err)
	}
	defer wsClient.Close(websocket.StatusNormalClosure, "")

	if err := wsClient.Write(ctx, websocket.MessageText, []byte(`{"method":"ping"}`)); err != nil {
		t.Fatalf("ws Write failed: %v", err)
	}
	typ, received, err := wsClient.Read(ctx)
	if err != nil || typ != websocket.MessageText {
		t.Fatalf("ws Read failed: %v", err)
	}
	if !strings.Contains(string(received), `{"echo":{"method":"ping"}}`) {
		t.Fatalf("unexpected ws echo response: %s", string(received))
	}

	// Tailscale 入口的 Address 只保留 r.RemoteAddr 的 host 部分（不带端口）。
	conn := waitForServerConn(t, ctx, srv)
	if got := conn.Info().Address; got != "127.0.0.1" {
		t.Errorf("tailscale entry Address = %q, want %q", got, "127.0.0.1")
	}
}

// TestServer_ClientInfoWhoIs 验证 WhoIs 成功时写入展示字段、失败时连接仍建立且字段留空。
func TestServer_ClientInfoWhoIs(t *testing.T) {
	cases := []struct {
		name     string
		whoIs    func(ctx context.Context, remoteAddr string) (ClientIdentity, error)
		wantUser string
		wantNode string
	}{
		{
			name: "identity resolved",
			whoIs: func(ctx context.Context, remoteAddr string) (ClientIdentity, error) {
				return ClientIdentity{LoginName: "alice@example.com", NodeName: "iphone"}, nil
			},
			wantUser: "alice@example.com",
			wantNode: "iphone",
		},
		{
			name: "whois error leaves fields empty",
			whoIs: func(ctx context.Context, remoteAddr string) (ClientIdentity, error) {
				return ClientIdentity{LoginName: "ignored", NodeName: "ignored"}, errors.New("whois unavailable")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := NewServer(ServerOptions{
				Handler:      &mockEchoHandler{},
				Assets:       testAssets,
				Entry:        EntryLAN,
				AllowedHosts: []string{"127.0.0.1", "localhost"},
				WhoIs:        tc.whoIs,
			})
			ts := httptest.NewServer(server)
			defer ts.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/ws"
			wsClient, _, err := websocket.Dial(ctx, wsURL, nil)
			if err != nil {
				t.Fatalf("websocket Dial failed: %v", err)
			}
			defer wsClient.Close(websocket.StatusNormalClosure, "")

			conn := waitForServerConn(t, ctx, server)
			info := conn.Info()
			if info.TailnetUser != tc.wantUser {
				t.Errorf("TailnetUser = %q, want %q", info.TailnetUser, tc.wantUser)
			}
			if info.TailnetNode != tc.wantNode {
				t.Errorf("TailnetNode = %q, want %q", info.TailnetNode, tc.wantNode)
			}
			// WhoIs 只用于展示：它不能把连接变成已认证。
			if info.Authenticated {
				t.Errorf("WhoIs must not authenticate the connection")
			}
		})
	}
}

// TestServer_AnswerRoute 验证作答路径在路由层的行为：LAN 入口恒 403，安全入口交给注入的处理器，
// 未接线时视为不存在；POST 必须在静态 GET/HEAD 检查之前分发。
func TestServer_AnswerRoute(t *testing.T) {
	newReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, answerPath, strings.NewReader("{}"))
		req.Host = "mcode.example.ts.net"
		return req
	}

	t.Run("LAN entry always forbidden", func(t *testing.T) {
		called := false
		server := NewServer(ServerOptions{
			Assets:       testAssets,
			ListenAddr:   "192.168.1.10:43117",
			Entry:        EntryLAN,
			AllowedHosts: []string{"192.168.1.10"},
			AnswerHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			}),
		})
		req := httptest.NewRequest(http.MethodPost, answerPath, strings.NewReader("{}"))
		req.Host = "192.168.1.10:43117"
		req.RemoteAddr = "192.168.1.50:5555"
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 on LAN entry, got %d", rec.Code)
		}
		if called {
			t.Fatal("answer handler must not run on LAN entry")
		}
	})

	t.Run("secure entry dispatches to handler", func(t *testing.T) {
		called := false
		server := NewServer(ServerOptions{
			Assets:       testAssets,
			ListenAddr:   "0.0.0.0:443",
			Entry:        EntryTLS,
			AllowedHosts: []string{"mcode.example.ts.net"},
			AnswerHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			}),
		})
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, newReq())

		if rec.Code != http.StatusNoContent {
			t.Fatalf("expected 204 from injected handler, got %d", rec.Code)
		}
		if !called {
			t.Fatal("expected answer handler to be called on secure entry")
		}
	})

	t.Run("secure entry without handler is 404", func(t *testing.T) {
		server := NewServer(ServerOptions{
			Assets:       testAssets,
			ListenAddr:   "0.0.0.0:443",
			Entry:        EntryTLS,
			AllowedHosts: []string{"mcode.example.ts.net"},
		})
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, newReq())

		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404 when handler is not wired, got %d", rec.Code)
		}
	})

	t.Run("GET reaches endpoint and returns 405", func(t *testing.T) {
		server := NewServer(ServerOptions{
			Assets:       testAssets,
			ListenAddr:   "0.0.0.0:443",
			Entry:        EntryTLS,
			AllowedHosts: []string{"mcode.example.ts.net"},
			AnswerHandler: NewAnswerEndpoint(AnswerEndpointOptions{
				Entry: EntryTLS,
			}),
		})
		req := httptest.NewRequest(http.MethodGet, answerPath, nil)
		req.Host = "mcode.example.ts.net"
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405 from endpoint, got %d", rec.Code)
		}
	})
}
