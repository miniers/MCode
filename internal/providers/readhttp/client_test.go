package readhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// connIDKey 是测试里标记「请求来自第几条底层连接」的 context key。
type connIDKey struct{}

// stubTransport 是自定义 RoundTripper，用来验证无法克隆的连接池被原样保留。
type stubTransport struct {
	calls atomic.Int64
}

func (s *stubTransport) RoundTrip(*http.Request) (*http.Response, error) {
	s.calls.Add(1)
	return nil, errors.New("stub transport")
}

// streamErrorServer 是只对第一条底层连接制造 HTTP/2 流中断的测试服务：第一条
// 连接上的请求一律 panic(http.ErrAbortHandler)，服务端因此回 RST_STREAM
// INTERNAL_ERROR，客户端看到 "stream error: ...; INTERNAL_ERROR; received from
// peer"；之后的连接交给 handler 正常处理。
type streamErrorServer struct {
	*httptest.Server
	conns      atomic.Int64 // 建过的底层连接数
	requests   atomic.Int64 // 到达处理器的请求数
	failures   atomic.Int64 // 被中断的请求数
	okConn     atomic.Int64 // 最近一次成功请求所在的连接编号
	wrongProto atomic.Int64 // 非 HTTP/2 请求数
}

func newStreamErrorServer(t *testing.T, handler http.HandlerFunc) *streamErrorServer {
	t.Helper()
	s := &streamErrorServer{}
	s.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			s.wrongProto.Add(1)
		}
		s.requests.Add(1)
		if id, _ := r.Context().Value(connIDKey{}).(int64); id == 1 {
			s.failures.Add(1)
			panic(http.ErrAbortHandler)
		}
		if id, ok := r.Context().Value(connIDKey{}).(int64); ok {
			s.okConn.Store(id)
		}
		handler(w, r)
	}))
	s.EnableHTTP2 = true
	s.Config.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
		return context.WithValue(ctx, connIDKey{}, s.conns.Add(1))
	}
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}

// getBody 发一次 GET 并读完响应体。
func getBody(t *testing.T, client *http.Client, rawURL string) string {
	t.Helper()
	resp, err := client.Get(rawURL)
	if err != nil {
		t.Fatalf("GET %s: %v", rawURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应体: %v", err)
	}
	return string(body)
}

func TestNewClientCopiesClientSemantics(t *testing.T) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	redirect := func(*http.Request, []*http.Request) error { return nil }
	base := &http.Client{Timeout: 3 * time.Second, Jar: jar, CheckRedirect: redirect}

	client := NewClient(base)
	if client == base {
		t.Fatal("NewClient 应返回浅拷贝，不能复用原客户端")
	}
	if client.Timeout != base.Timeout {
		t.Errorf("Timeout = %v，想要 %v", client.Timeout, base.Timeout)
	}
	if client.Jar != jar {
		t.Error("Jar 没有被保留")
	}
	if client.CheckRedirect == nil {
		t.Error("CheckRedirect 没有被保留")
	}
	if base.Transport != nil {
		t.Errorf("原客户端的 Transport 被改成了 %T", base.Transport)
	}
	if _, ok := client.Transport.(*poolTransport); !ok {
		t.Errorf("Transport = %T，想要 *poolTransport", client.Transport)
	}

	defaults := NewClient(nil)
	if defaults.Timeout != 0 {
		t.Errorf("nil 基础客户端的 Timeout = %v，想要 0", defaults.Timeout)
	}
	if _, ok := defaults.Transport.(*poolTransport); !ok {
		t.Errorf("nil 基础客户端的 Transport = %T，想要 *poolTransport", defaults.Transport)
	}
}

func TestNewClientKeepsCustomRoundTripper(t *testing.T) {
	rt := &stubTransport{}
	base := &http.Client{Transport: rt}

	client := NewClient(base)
	if client.Transport != http.RoundTripper(rt) {
		t.Fatalf("自定义 RoundTripper 被换成了 %T", client.Transport)
	}
	if _, err := client.Get("http://example.invalid/models"); err == nil {
		t.Fatal("想要 stub 的错误")
	}
	if got := rt.calls.Load(); got != 1 {
		t.Errorf("stub 调用次数 = %d，想要 1", got)
	}
}

func TestNewClientOwnsIndependentPool(t *testing.T) {
	orig := &http.Transport{MaxIdleConnsPerHost: 7, Proxy: http.ProxyFromEnvironment}
	base := &http.Client{Transport: orig}

	client := NewClient(base)
	if base.Transport != orig {
		t.Fatal("原 Transport 被替换了")
	}
	pool := client.Transport.(*poolTransport).current()
	if pool == orig {
		t.Fatal("新客户端必须使用克隆出的独立连接池")
	}
	if pool.MaxIdleConnsPerHost != 7 || pool.Proxy == nil {
		t.Errorf("克隆的连接池丢了配置: MaxIdleConnsPerHost=%d Proxy 已设置=%v", pool.MaxIdleConnsPerHost, pool.Proxy != nil)
	}
	if orig.MaxIdleConnsPerHost != 7 {
		t.Errorf("原 Transport 被修改: MaxIdleConnsPerHost=%d", orig.MaxIdleConnsPerHost)
	}
}

func TestPoolPreservesHTTP2Config(t *testing.T) {
	base := &http.Transport{
		ResponseHeaderTimeout: 2 * time.Minute,
		HTTP2:                 &http.HTTP2Config{SendPingTimeout: 15 * time.Second, PingTimeout: 15 * time.Second},
	}
	tr := NewClient(&http.Client{Transport: base}).Transport.(*poolTransport)
	first := tr.current()
	for _, pool := range []*http.Transport{first, tr.rotate(first)} {
		if pool.HTTP2 == nil || pool.HTTP2.SendPingTimeout != 15*time.Second || pool.HTTP2.PingTimeout != 15*time.Second || pool.ResponseHeaderTimeout != 2*time.Minute {
			t.Fatalf("连接池配置丢失: HTTP2=%+v ResponseHeaderTimeout=%v", pool.HTTP2, pool.ResponseHeaderTimeout)
		}
	}
}

func TestRoundTripPostRotatesWithoutRetry(t *testing.T) {
	srv := newStreamErrorServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	})
	client := NewClient(srv.Client())
	tr := client.Transport.(*poolTransport)
	before := tr.current()
	resp, err := client.Post(srv.URL+"/chat", "application/json", strings.NewReader(`{"message":"hello"}`))
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil || !isConnectionBroken(err) {
		t.Fatalf("预期连接中断错误，得到 %v", err)
	}
	if got := srv.requests.Load(); got != 1 {
		t.Fatalf("POST 请求数 = %d，想要 1", got)
	}
	if tr.current() == before {
		t.Fatal("POST 失败后应当换池")
	}
	if body := getBody(t, client, srv.URL+"/models"); body != "ok" {
		t.Fatalf("后续请求响应体 = %q，想要 ok", body)
	}
	if got := srv.okConn.Load(); got <= 1 {
		t.Fatalf("后续请求连接 = %d，想要新连接", got)
	}
	if got := srv.wrongProto.Load(); got != 0 {
		t.Fatalf("有 %d 个请求没走 HTTP/2", got)
	}
}

// TestRoundTripRecoversFromHTTP2StreamError 用真实连接验证恢复：第一条底层连接
// 上的请求被服务端 RST_STREAM 打断，读请求经一次换池重试后成功，并且后续读取
// 直接复用健康的新连接池。
func TestRoundTripRecoversFromHTTP2StreamError(t *testing.T) {
	srv := newStreamErrorServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	})
	client := NewClient(srv.Client())
	tr := client.Transport.(*poolTransport)
	before := tr.current()

	if body := getBody(t, client, srv.URL+"/models"); body != "ok" {
		t.Fatalf("响应体 = %q，想要 ok", body)
	}
	if got := srv.failures.Load(); got != 1 {
		t.Fatalf("被中断的请求数 = %d，想要 1（只额外重试一次）", got)
	}
	if got := srv.requests.Load(); got != 2 {
		t.Fatalf("请求数 = %d，想要 2", got)
	}
	if got := srv.conns.Load(); got != 2 {
		t.Fatalf("底层连接数 = %d，想要 2（坏连接 + 新连接）", got)
	}
	if got := srv.wrongProto.Load(); got != 0 {
		t.Fatalf("有 %d 个请求没走 HTTP/2", got)
	}
	if tr.current() == before {
		t.Fatal("遇到可恢复的流中断后应当换到新连接池")
	}
	healthyPool, healthyConn := tr.current(), srv.okConn.Load()
	if healthyConn == 0 || healthyConn == 1 {
		t.Fatalf("成功请求所在连接 = %d，想要第一条之后的连接", healthyConn)
	}

	// 后续读取直接用新池里的健康连接，不再回到旧池。
	if body := getBody(t, client, srv.URL+"/models"); body != "ok" {
		t.Fatalf("第二次读取响应体 = %q，想要 ok", body)
	}
	if got := srv.failures.Load(); got != 1 {
		t.Errorf("后续读取又被中断了 %d 次", got)
	}
	if got := srv.requests.Load(); got != 3 {
		t.Errorf("请求数 = %d，想要 3", got)
	}
	if got := srv.okConn.Load(); got != healthyConn {
		t.Errorf("后续读取所在连接 = %d，想要复用 %d", got, healthyConn)
	}
	if tr.current() != healthyPool {
		t.Error("后续读取又换了连接池")
	}
}

// TestRotateKeepsNewerPool 验证迟到的旧池错误不会替换或关闭已经换过的健康池。
func TestRotateKeepsNewerPool(t *testing.T) {
	var connSeq atomic.Int64
	var okConn atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := r.Context().Value(connIDKey{}).(int64); ok {
			okConn.Store(id)
		}
		io.WriteString(w, "ok")
	}))
	srv.EnableHTTP2 = true
	srv.Config.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
		return context.WithValue(ctx, connIDKey{}, connSeq.Add(1))
	}
	srv.StartTLS()
	defer srv.Close()

	client := NewClient(srv.Client())
	tr := client.Transport.(*poolTransport)

	getBody(t, client, srv.URL+"/models")
	stale := tr.current()
	newer := tr.rotate(stale)
	if newer == stale {
		t.Fatal("rotate 应当换到新连接池")
	}
	if got := tr.rotate(stale); got != newer {
		t.Fatal("迟到的旧错误不能替换已经换过的健康池")
	}

	// 健康的新池没有被关闭：下一次读取仍然复用同一个连接。
	getBody(t, client, srv.URL+"/models")
	reused := okConn.Load()
	getBody(t, client, srv.URL+"/models")
	if got := okConn.Load(); got != reused {
		t.Fatalf("连接 = %d，想要复用 %d（新池被误关）", got, reused)
	}
	if tr.current() != newer {
		t.Error("健康池被替换了")
	}
}

// TestRotateClosesOldPoolIdleConnections 验证换池后旧池的空闲连接被回收，同时
// 原客户端自己的连接池不受影响。
func TestRotateClosesOldPoolIdleConnections(t *testing.T) {
	var connSeq atomic.Int64
	var ids sync.Map // net.Conn -> 连接编号
	closed := make(chan int64, 8)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := r.Context().Value(connIDKey{}).(int64)
		fmt.Fprintf(w, "conn-%d", id)
	}))
	srv.Config.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
		id := connSeq.Add(1)
		ids.Store(c, id)
		return context.WithValue(ctx, connIDKey{}, id)
	}
	srv.Config.ConnState = func(c net.Conn, cs http.ConnState) {
		if cs != http.StateClosed {
			return
		}
		if id, ok := ids.Load(c); ok {
			select {
			case closed <- id.(int64):
			default:
			}
		}
	}
	srv.Start()
	defer srv.Close()

	base := &http.Client{Transport: &http.Transport{}}
	client := NewClient(base)
	tr := client.Transport.(*poolTransport)

	if body := getBody(t, client, srv.URL+"/models"); body != "conn-1" {
		t.Fatalf("第一条连接 = %q，想要 conn-1", body)
	}
	old := tr.current()
	next := tr.rotate(old)
	if next == old {
		t.Fatal("rotate 应当换到新连接池")
	}
	if tr.current() != next {
		t.Fatal("当前连接池不是换上的新池")
	}

	// 旧池的空闲连接被关闭（换池时它已经空闲）。
	select {
	case id := <-closed:
		if id != 1 {
			t.Fatalf("被关闭的连接 = %d，想要 1", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("旧连接池的空闲连接没有被关闭")
	}

	// 原客户端自己的传输没有被关闭：还能正常建连读取。
	if body := getBody(t, base, srv.URL+"/models"); !strings.HasPrefix(body, "conn-") {
		t.Fatalf("原客户端的响应体 = %q", body)
	}
	// 新池也能正常读取。
	if body := getBody(t, client, srv.URL+"/models"); !strings.HasPrefix(body, "conn-") {
		t.Fatalf("新连接池的响应体 = %q", body)
	}
}

func TestNoRetryWhenContextCanceled(t *testing.T) {
	var requests atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		// 先取消客户端请求，再让流中断：已取消的请求不允许再重试。
		cancel()
		panic(http.ErrAbortHandler)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	client := NewClient(srv.Client())
	tr := client.Transport.(*poolTransport)
	before := tr.current()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(req); err == nil {
		t.Fatal("context 取消后应当返回错误")
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("请求数 = %d，想要 1（context 已取消不重试）", got)
	}
	if tr.current() != before {
		t.Error("context 已取消时不应换池")
	}
}

func TestNoRetryWhenContextAlreadyDone(t *testing.T) {
	srv := newStreamErrorServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	})
	client := NewClient(srv.Client())
	tr := client.Transport.(*poolTransport)
	before := tr.current()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(req); err == nil {
		t.Fatal("context 已截止时应当返回错误")
	}
	if got := srv.requests.Load(); got != 0 {
		t.Errorf("请求数 = %d，想要 0（context 已截止时不该发请求）", got)
	}
	if tr.current() != before {
		t.Error("context 已截止时不应换池")
	}
}

func TestNoRetryOnHTTPStatus(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		var requests atomic.Int64
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			w.WriteHeader(status)
			io.WriteString(w, `{"error":"denied"}`)
		}))
		srv.EnableHTTP2 = true
		srv.StartTLS()

		client := NewClient(srv.Client())
		tr := client.Transport.(*poolTransport)
		before := tr.current()
		resp, err := client.Get(srv.URL + "/models")
		if err != nil {
			srv.Close()
			t.Fatalf("%d: %v", status, err)
		}
		resp.Body.Close()
		if resp.StatusCode != status {
			t.Errorf("状态码 = %d，想要 %d", resp.StatusCode, status)
		}
		if got := requests.Load(); got != 1 {
			t.Errorf("%d: 请求数 = %d，想要 1（HTTP 状态错误不重试）", status, got)
		}
		if tr.current() != before {
			t.Errorf("%d: HTTP 状态错误不应换池", status)
		}
		srv.Close()
	}
}

func TestNoReplayForNonIdempotentRequests(t *testing.T) {
	srv := newStreamErrorServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	})
	client := NewClient(srv.Client())
	tr := client.Transport.(*poolTransport)
	before := tr.current()

	resp, err := client.Post(srv.URL+"/models", "text/plain", strings.NewReader("payload"))
	if err == nil {
		resp.Body.Close()
		t.Fatal("POST 应当失败")
	}
	if got := srv.requests.Load(); got != 1 {
		t.Errorf("POST 请求数 = %d，想要 1（不重放）", got)
	}

	srv.requests.Store(0)
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/models", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(req); err == nil {
		t.Fatal("带 body 的 GET 应当失败")
	}
	if got := srv.requests.Load(); got != 1 {
		t.Errorf("带 body 的 GET 请求数 = %d，想要 1（不重放）", got)
	}
	if tr.current() != before {
		t.Error("不重放的请求失败时不应换池")
	}
}

// TestClientTimeoutCoversBothAttempts 验证重试沿用同一次 Do 的截止时间：第一次
// 尝试被流中断，第二次尝试挂到外层 Client.Timeout 才被取消，而且两次尝试从
// 客户端看到的截止时间完全相同。
//
// 客户端的 context deadline 不会通过 HTTP/2 传给服务器，所以这里在客户端侧
// 观察：Transport.Proxy 回调能拿到每次尝试的 req.Context()，其中带着
// Client.Timeout 算出来的 deadline。
func TestClientTimeoutCoversBothAttempts(t *testing.T) {
	var connSeq atomic.Int64
	secondStarted := make(chan struct{}, 1)
	secondCanceled := make(chan struct{}, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, _ := r.Context().Value(connIDKey{}).(int64); id == 1 {
			panic(http.ErrAbortHandler)
		}
		select {
		case secondStarted <- struct{}{}:
		default:
		}
		// 挂到客户端超时：服务器这边看不到客户端的 deadline，只能等取消信号。
		<-r.Context().Done()
		select {
		case secondCanceled <- struct{}{}:
		default:
		}
	}))
	srv.EnableHTTP2 = true
	srv.Config.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
		return context.WithValue(ctx, connIDKey{}, connSeq.Add(1))
	}
	srv.StartTLS()
	defer srv.Close()

	base := srv.Client()
	baseTransport, ok := base.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("基础 Transport = %T，想要 *http.Transport", base.Transport)
	}

	// 客户端侧记录每次尝试的截止时间；Proxy 返回 nil 表示不走代理。
	type attempt struct {
		deadline time.Time
		ok       bool
	}
	var mu sync.Mutex
	var attempts []attempt
	baseTransport.Proxy = func(req *http.Request) (*url.URL, error) {
		deadline, ok := req.Context().Deadline()
		mu.Lock()
		attempts = append(attempts, attempt{deadline: deadline, ok: ok})
		mu.Unlock()
		return nil, nil
	}

	client := NewClient(base)
	// 2 秒足够盖住一次 TLS 建连的抖动，又不会把测试拖得太久。
	client.Timeout = 2 * time.Second
	if _, err := client.Get(srv.URL + "/slow"); err == nil {
		t.Fatal("应当因客户端超时失败")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("错误 = %v，想要客户端超时", err)
	}

	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("没有发生第二次尝试")
	}

	mu.Lock()
	got := append([]attempt(nil), attempts...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("客户端看到的尝试次数 = %d，想要 2", len(got))
	}
	for i, a := range got {
		if !a.ok {
			t.Fatalf("第 %d 次尝试的请求 context 没有截止时间", i+1)
		}
	}
	if !got[0].deadline.Equal(got[1].deadline) {
		t.Fatalf("两次尝试的截止时间不同: %v vs %v", got[0].deadline, got[1].deadline)
	}

	select {
	case <-secondCanceled:
	case <-time.After(10 * time.Second):
		t.Fatal("超时后第二次尝试没有被取消")
	}
}

func TestRetryableClassification(t *testing.T) {
	get := func() *http.Request {
		req, err := http.NewRequest(http.MethodGet, "https://example.invalid/models", nil)
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	getWithBody := func() *http.Request {
		req, err := http.NewRequest(http.MethodGet, "https://example.invalid/models", strings.NewReader("payload"))
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	post := func() *http.Request {
		req, err := http.NewRequest(http.MethodPost, "https://example.invalid/models", nil)
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	canceled := func() *http.Request {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.invalid/models", nil)
		if err != nil {
			t.Fatal(err)
		}
		return req
	}

	streamErr := errors.New("stream error: stream ID 357; INTERNAL_ERROR; received from peer")
	cases := []struct {
		name string
		req  func() *http.Request
		err  error
		want bool
	}{
		{"EOF", get, io.EOF, true},
		{"unexpected EOF", get, io.ErrUnexpectedEOF, true},
		{"连接重置", get, &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, true},
		{"broken pipe", get, &net.OpError{Op: "write", Net: "tcp", Err: syscall.EPIPE}, true},
		{"HTTP/2 流中断", get, streamErr, true},
		{"包装后的流中断", get, fmt.Errorf("openai: request models: %w", streamErr), true},
		{"HTTP/2 连接丢失", get, errors.New("http2: client connection lost"), true},
		{"普通错误", get, errors.New("boom"), false},
		{"证书错误", get, errors.New("x509: certificate signed by unknown authority"), false},
		{"URL 错误", get, &url.Error{Op: "Get", URL: "https://[::1", Err: errors.New("missing ']' in host")}, false},
		{"context 取消", get, context.Canceled, false},
		{"超时", get, context.DeadlineExceeded, false},
		{"其他流错误码", get, errors.New("stream error: stream ID 3; CANCEL"), false},
		{"连接级错误码", get, errors.New("connection error: INTERNAL_ERROR"), false},
		{"POST", post, io.EOF, false},
		{"带 body 的 GET", getWithBody, io.EOF, false},
		{"context 已取消", canceled, io.EOF, false},
	}
	for _, tc := range cases {
		if got := retryable(tc.req(), tc.err); got != tc.want {
			t.Errorf("%s: retryable = %v，想要 %v", tc.name, got, tc.want)
		}
	}
}

// getResult 是后台 GET 的结果，供带通道控制的测试使用。
type getResult struct {
	body string
	err  error
}

// goGet 在后台发一次 GET 并读完响应体，结果从返回的通道取；调用方用 waitWithin
// 收结果，测试不会挂死。
func goGet(client *http.Client, rawURL string) <-chan getResult {
	out := make(chan getResult, 1)
	go func() {
		resp, err := client.Get(rawURL)
		if err != nil {
			out <- getResult{err: err}
			return
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		out <- getResult{body: string(body), err: err}
	}()
	return out
}

// waitWithin 在限定时间内等待一个信号，超时直接失败。
func waitWithin[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("等待超时：%s", what)
	}
	var zero T
	return zero
}

// connTracker 给测试服务记录底层连接编号，并在连接关闭时发出该编号。
type connTracker struct {
	seq    atomic.Int64
	ids    sync.Map // net.Conn -> int64
	closed chan int64
}

func newConnTracker() *connTracker {
	return &connTracker{closed: make(chan int64, 8)}
}

func (c *connTracker) connContext(ctx context.Context, conn net.Conn) context.Context {
	id := c.seq.Add(1)
	c.ids.Store(conn, id)
	return context.WithValue(ctx, connIDKey{}, id)
}

func (c *connTracker) connState(conn net.Conn, state http.ConnState) {
	if state != http.StateClosed {
		return
	}
	if id, ok := c.ids.Load(conn); ok {
		select {
		case c.closed <- id.(int64):
		default:
		}
	}
}

// getBodyIdle 发一次 GET 读完响应体，并等到连接真的回到客户端空闲池，用于需要
// 「连接已经空闲」这一前提的测试。
func getBodyIdle(t *testing.T, client *http.Client, rawURL string) string {
	t.Helper()
	idle := make(chan error, 1)
	trace := &httptrace.ClientTrace{
		PutIdleConn: func(err error) {
			select {
			case idle <- err:
			default:
			}
		},
	}
	ctx := httptrace.WithClientTrace(context.Background(), trace)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", rawURL, err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("读响应体: %v", err)
	}
	if err := waitWithin(t, idle, "连接回到空闲池"); err != nil {
		t.Fatalf("连接没有回到空闲池: %v", err)
	}
	return string(body)
}

// TestNewClientInheritsDefaultClient 验证 NewClient(nil) 继承 http.DefaultClient
// 的现有配置，并且不修改这个全局对象。本包没有并行测试，这里临时替换全局
// 客户端并在 Cleanup 里恢复。
func TestNewClientInheritsDefaultClient(t *testing.T) {
	saved := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = saved })

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	redirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	custom := &http.Client{Timeout: 7 * time.Second, Jar: jar, CheckRedirect: redirect}
	http.DefaultClient = custom

	client := NewClient(nil)
	if client == custom {
		t.Fatal("NewClient 应返回浅拷贝，不能复用 http.DefaultClient")
	}
	if client.Timeout != 7*time.Second {
		t.Errorf("Timeout = %v，想要继承的 %v", client.Timeout, 7*time.Second)
	}
	if client.Jar != jar {
		t.Error("Jar 没有被继承")
	}
	if client.CheckRedirect == nil {
		t.Error("CheckRedirect 没有被继承")
	}
	if _, ok := client.Transport.(*poolTransport); !ok {
		t.Errorf("Transport = %T，想要 *poolTransport", client.Transport)
	}
	if http.DefaultClient != custom || custom.Transport != nil {
		t.Error("全局 http.DefaultClient 被修改了")
	}
}

// TestCloseIdleConnectionsDelegatesToCurrentPool 验证 client.CloseIdleConnections()
// 真的关掉了自己连接池里的空闲连接：之后读取必须新建连接，而原客户端已经建立的
// 连接仍然可以复用。
func TestCloseIdleConnectionsDelegatesToCurrentPool(t *testing.T) {
	tracker := newConnTracker()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := r.Context().Value(connIDKey{}).(int64)
		fmt.Fprintf(w, "conn-%d", id)
	}))
	srv.Config.ConnContext = tracker.connContext
	srv.Config.ConnState = tracker.connState
	srv.Start()
	defer srv.Close()

	base := &http.Client{Transport: &http.Transport{}}
	client := NewClient(base)

	if body := getBodyIdle(t, client, srv.URL+"/models"); body != "conn-1" {
		t.Fatalf("新客户端的第一条连接 = %q，想要 conn-1", body)
	}
	if body := getBodyIdle(t, base, srv.URL+"/models"); body != "conn-2" {
		t.Fatalf("原客户端的第一条连接 = %q，想要 conn-2", body)
	}
	// 两边的空闲连接都在复用，说明下面关掉的是真正的空闲连接。
	if body := getBodyIdle(t, client, srv.URL+"/models"); body != "conn-1" {
		t.Fatalf("新客户端没有复用空闲连接: %q", body)
	}
	if body := getBodyIdle(t, base, srv.URL+"/models"); body != "conn-2" {
		t.Fatalf("原客户端没有复用空闲连接: %q", body)
	}

	client.CloseIdleConnections()

	if id := waitWithin(t, tracker.closed, "client.CloseIdleConnections() 关闭空闲连接"); id != 1 {
		t.Fatalf("被关闭的连接 = %d，想要 1（只关自己的池）", id)
	}

	// 后续读取必须新建连接，说明旧连接确实没了。
	if body := getBodyIdle(t, client, srv.URL+"/models"); body != "conn-3" {
		t.Fatalf("关闭空闲连接后的连接 = %q，想要新建的 conn-3", body)
	}
	// 原客户端的连接没被影响：仍然复用 conn-2。
	if body := getBodyIdle(t, base, srv.URL+"/models"); body != "conn-2" {
		t.Fatalf("原客户端的连接被影响了: %q", body)
	}
}

// TestRetiredPoolClosesConnAfterInflightResponse 验证 IdleConnTimeout=0 的退役
// HTTP/2 连接不会因为轮换时还有在途响应就永久留着：在途响应不被提前打断，等它
// 读完并关闭后旧连接被回收，健康的新池照常复用。
func TestRetiredPoolClosesConnAfterInflightResponse(t *testing.T) {
	tracker := newConnTracker()
	var failOnce atomic.Bool
	holdStarted := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseInflight := func() { releaseOnce.Do(func() { close(release) }) }
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hold":
			io.WriteString(w, "held")
			w.(http.Flusher).Flush()
			holdStarted <- struct{}{}
			<-release
			io.WriteString(w, "-done")
		case "/fail":
			// 旧连接上的第一次中断，换池后的重试成功。
			if failOnce.CompareAndSwap(false, true) {
				panic(http.ErrAbortHandler)
			}
			io.WriteString(w, "recovered")
		default:
			io.WriteString(w, "ok")
		}
	}))
	srv.EnableHTTP2 = true
	srv.Config.ConnContext = tracker.connContext
	srv.Config.ConnState = tracker.connState
	srv.StartTLS()
	defer srv.Close()
	// 任何提前失败都要先放行阻塞在 /hold 上的 handler，否则 srv.Close() 会一直等它。
	defer releaseInflight()

	base := srv.Client()
	baseTransport, ok := base.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("基础 Transport = %T，想要 *http.Transport", base.Transport)
	}
	// 不靠空闲超时回收：退役的连接必须由响应体包装主动关掉。
	baseTransport.IdleConnTimeout = 0

	client := NewClient(base)
	// 只给这个测试的客户端加有限超时，避免失败路径上的后台请求无限挂住。
	client.Timeout = 10 * time.Second
	tr := client.Transport.(*poolTransport)

	getBody(t, client, srv.URL+"/warm")
	old := tr.current()

	hold := goGet(client, srv.URL+"/hold")
	waitWithin(t, holdStarted, "服务端开始处理在途请求")

	// 另一个流失败触发轮换，此时旧池上还有在途响应。
	if body := getBody(t, client, srv.URL+"/fail"); body != "recovered" {
		t.Fatalf("重试响应体 = %q，想要 recovered", body)
	}
	next := tr.current()
	if next == old {
		t.Fatal("流中断后应当换到新连接池")
	}
	if got := tracker.seq.Load(); got != 2 {
		t.Fatalf("底层连接数 = %d，想要 2", got)
	}
	// 在途响应还没结束，旧连接不能被提前关掉。
	select {
	case id := <-tracker.closed:
		t.Fatalf("在途响应结束前旧连接就被关闭了: conn-%d", id)
	default:
	}

	releaseInflight()
	res := waitWithin(t, hold, "在途响应结束")
	if res.err != nil {
		t.Fatalf("在途响应被中断: %v", res.err)
	}
	if res.body != "held-done" {
		t.Fatalf("在途响应体 = %q，想要 held-done", res.body)
	}

	if id := waitWithin(t, tracker.closed, "退役的旧 HTTP/2 连接被回收"); id != 1 {
		t.Fatalf("被关闭的连接 = %d，想要 1", id)
	}

	// 健康的新池没有被关：后续读取复用同一条连接。
	if body := getBody(t, client, srv.URL+"/warm"); body != "ok" {
		t.Fatalf("新池读取响应体 = %q，想要 ok", body)
	}
	if tr.current() != next {
		t.Error("健康的新池被替换了")
	}
	if got := tracker.seq.Load(); got != 2 {
		t.Errorf("底层连接数 = %d，想要 2（新池复用已有连接）", got)
	}
}

// TestPersistentStreamErrorRetriesOnce 验证持续故障只额外尝试一次：每条连接上的
// 请求都被服务端中断，最终返回可诊断的 INTERNAL_ERROR，不会无限换池。
func TestPersistentStreamErrorRetriesOnce(t *testing.T) {
	tracker := newConnTracker()
	var requests atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		panic(http.ErrAbortHandler)
	}))
	srv.EnableHTTP2 = true
	srv.Config.ConnContext = tracker.connContext
	srv.Config.ConnState = tracker.connState
	srv.StartTLS()
	defer srv.Close()

	client := NewClient(srv.Client())
	tr := client.Transport.(*poolTransport)
	before := tr.current()

	_, err := client.Get(srv.URL + "/models")
	if err == nil {
		t.Fatal("持续故障应当返回错误")
	}
	if !strings.Contains(err.Error(), "INTERNAL_ERROR") {
		t.Errorf("错误 = %v，想要可诊断的 INTERNAL_ERROR", err)
	}
	if got := requests.Load(); got != 2 {
		t.Errorf("请求数 = %d，想要 2（一次尝试 + 一次重试）", got)
	}
	if got := tracker.seq.Load(); got != 2 {
		t.Errorf("底层连接数 = %d，想要 2", got)
	}
	if tr.current() == before {
		t.Error("第一次失败后应当换池")
	}
}

// TestConcurrentLateStreamErrorReusesHealthyPool 验证并发下迟到的旧池错误：第一个
// 流失败并换池恢复后，第二个旧流再失败时直接复用已经建好的健康池，不替换也不
// 关闭它，后续读取还能复用新连接。
func TestConcurrentLateStreamErrorReusesHealthyPool(t *testing.T) {
	tracker := newConnTracker()
	var okConn atomic.Int64
	arrived := make(chan string, 4)
	release1 := make(chan struct{})
	release2 := make(chan struct{})
	var release1Once, release2Once sync.Once
	releaseStream1 := func() { release1Once.Do(func() { close(release1) }) }
	releaseStream2 := func() { release2Once.Do(func() { close(release2) }) }
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := r.Context().Value(connIDKey{}).(int64)
		if r.URL.Path == "/warm" {
			io.WriteString(w, "ok")
			return
		}
		if id == 1 {
			arrived <- r.URL.Path
			if r.URL.Path == "/fail2" {
				<-release2
			} else {
				<-release1
			}
			panic(http.ErrAbortHandler)
		}
		okConn.Store(id)
		io.WriteString(w, "recovered")
	}))
	srv.EnableHTTP2 = true
	srv.Config.ConnContext = tracker.connContext
	srv.Config.ConnState = tracker.connState
	srv.StartTLS()
	defer srv.Close()
	// 两个阻塞的 handler 都要在任何失败路径上先放行，且都排在 srv.Close() 之前执行。
	defer releaseStream1()
	defer releaseStream2()

	client := NewClient(srv.Client())
	// 只给这个测试的客户端加有限超时，避免失败路径上的后台请求无限挂住。
	client.Timeout = 10 * time.Second
	tr := client.Transport.(*poolTransport)
	getBody(t, client, srv.URL+"/warm")
	old := tr.current()
	if got := tracker.seq.Load(); got != 1 {
		t.Fatalf("预热后的底层连接数 = %d，想要 1", got)
	}

	fail1 := goGet(client, srv.URL+"/fail1")
	fail2 := goGet(client, srv.URL+"/fail2")
	seen := make(map[string]bool, 2)
	for len(seen) < 2 {
		seen[waitWithin(t, arrived, "两个请求都到旧连接上")] = true
	}

	// 第一个流失败并完成恢复。
	releaseStream1()
	res := waitWithin(t, fail1, "第一个流恢复")
	if res.err != nil {
		t.Fatalf("第一个流重试失败: %v", res.err)
	}
	if res.body != "recovered" {
		t.Fatalf("第一个流响应体 = %q，想要 recovered", res.body)
	}
	next := tr.current()
	if next == old {
		t.Fatal("第一个流失败后应当换池")
	}
	if got := tracker.seq.Load(); got != 2 {
		t.Fatalf("底层连接数 = %d，想要 2", got)
	}
	if got := okConn.Load(); got != 2 {
		t.Fatalf("恢复请求所在连接 = %d，想要 2", got)
	}

	// 迟到的第二个旧流错误：必须复用已经建好的新池。
	releaseStream2()
	res = waitWithin(t, fail2, "迟到的旧流错误恢复")
	if res.err != nil {
		t.Fatalf("迟到的旧流错误重试失败: %v", res.err)
	}
	if res.body != "recovered" {
		t.Fatalf("迟到错误的响应体 = %q，想要 recovered", res.body)
	}
	if tr.current() != next {
		t.Fatal("迟到的旧错误替换了健康的新池")
	}
	if got := tracker.seq.Load(); got != 2 {
		t.Fatalf("底层连接数 = %d，想要 2（复用已建好的新池）", got)
	}
	if got := okConn.Load(); got != 2 {
		t.Fatalf("迟到错误的重试所在连接 = %d，想要复用 2", got)
	}

	// 后续读取仍然复用新池的连接。
	if body := getBody(t, client, srv.URL+"/warm"); body != "ok" {
		t.Fatalf("后续读取响应体 = %q，想要 ok", body)
	}
	if tr.current() != next {
		t.Error("后续读取又换了连接池")
	}
	if got := tracker.seq.Load(); got != 2 {
		t.Errorf("底层连接数 = %d，想要 2", got)
	}
}
