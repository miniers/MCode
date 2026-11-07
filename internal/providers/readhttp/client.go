// Package readhttp 提供只读 GET 请求用的 HTTP 客户端。
//
// 模型目录与额度这类只读请求是幂等的：连接在服务端被中途重置时（例如 HTTP/2
// 的 "stream error: stream ID 357; INTERNAL_ERROR; received from peer"），
// 换一个新的连接池重试一次通常就能成功，而且坏掉的连接不会被后续读取再选中。
// 聊天等有副作用的请求不要用这里的客户端。
package readhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
)

// NewClient 返回 base 的浅拷贝，并把 Transport 换成独占连接池的包装：
// Timeout、Jar、CheckRedirect 等客户端语义保持不变，原客户端、原 Transport
// 和全局 http.DefaultTransport 都不会被修改或关闭。base 为 nil 时以
// http.DefaultClient 为浅拷贝来源，继承用户给默认客户端配置的 Timeout、Jar、
// CheckRedirect 等，但不会改动全局对象。
func NewClient(base *http.Client) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	client := *base
	client.Transport = newPoolTransport(base.Transport)
	return &client
}

// newPoolTransport 从 base 克隆出独立连接池的包装。
func newPoolTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	orig, ok := base.(*http.Transport)
	if !ok {
		// 自定义 RoundTripper 没法安全克隆出独立连接池：保留它原有的行为，
		// 不偷偷换成默认传输，因此这种客户端只做普通请求，不换池重试。
		return base
	}
	t := &poolTransport{clone: orig.Clone}
	t.pool = t.clone()
	return t
}

// poolTransport 持有一代可替换的连接池。每次请求只取当前池的快照，网络 I/O
// 不持锁；换池时也只多克隆一次连接池。
type poolTransport struct {
	clone func() *http.Transport

	mu   sync.Mutex
	pool *http.Transport // 当前连接池，受 mu 保护
}

// RoundTrip 在一次 http.Client.Do 内部完成恢复：失败后换池重试一次，重试沿用
// 请求 context 与外层 Client.Timeout，不会重开超时预算。
//
// 每个响应体都被包一层：读完或关闭时对已经退役的旧池再回收一次空闲连接。
// HTTP/2 连接上还有在途流时不会被关闭，只有等在途响应陆续结束后再扫，退役
// 的连接才会真正释放。
func (t *poolTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	pool := t.current()
	resp, err := pool.RoundTrip(req)
	if resp != nil {
		resp.Body = &reclaimBody{ReadCloser: resp.Body, transport: t, pool: pool}
		return resp, err
	}
	if err == nil {
		return resp, err
	}
	if !retryable(req, err) {
		t.reclaim(pool)
		return resp, err
	}
	// 克隆一份请求再重试，避免把已经用过的请求对象交给另一个连接池。
	retry := req.Clone(req.Context())
	next := t.rotate(pool)
	resp, err = next.RoundTrip(retry)
	if resp != nil {
		resp.Body = &reclaimBody{ReadCloser: resp.Body, transport: t, pool: next}
		return resp, err
	}
	// 重试也失败：这次请求不会再用到任何一代池，退役的那一代可以回收空闲连接。
	t.reclaim(pool)
	t.reclaim(next)
	return resp, err
}

// current 取当前连接池的快照；持锁时间只够读一个指针。
func (t *poolTransport) current() *http.Transport {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pool
}

// rotate 把失败的那一代连接池换成新池。并发请求已经换过池时直接复用当前池，
// 避免迟到的旧错误把健康的新池又换掉。
func (t *poolTransport) rotate(failed *http.Transport) *http.Transport {
	t.mu.Lock()
	if t.pool != failed {
		pool := t.pool
		t.mu.Unlock()
		return pool
	}
	next := t.clone()
	t.pool = next
	t.mu.Unlock()

	// 只关闭旧池的空闲连接：仍在旧池上进行的读取自然跑完，读完或关闭时由
	// reclaimBody 再扫一次，退役的连接最终也会被回收；原客户端与聊天用的
	// 连接池完全不受影响。
	failed.CloseIdleConnections()
	return next
}

// reclaim 对已经退役的连接池再回收一次空闲连接；池还是当前池时什么都不做，
// 避免把正在使用的连接关掉。旧池上仍在进行的读取不受影响：
// CloseIdleConnections 只关空闲连接。
func (t *poolTransport) reclaim(pool *http.Transport) {
	t.mu.Lock()
	retired := t.pool != pool
	t.mu.Unlock()
	if retired {
		pool.CloseIdleConnections()
	}
}

// CloseIdleConnections 让 http.Client.CloseIdleConnections 作用在当前连接池上；
// 原客户端、http.DefaultTransport 和其他代连接池都不受影响。
func (t *poolTransport) CloseIdleConnections() {
	t.current().CloseIdleConnections()
}

// reclaimBody 在响应读完或关闭后对退役的旧池再回收一次空闲连接。它只做这一
// 件事，读写行为与原来的 Body 完全一致。
//
// Close 时的那一次扫描是确定性的：标准库的 HTTP/2 响应体在 Close 返回前已经
// 把流从连接上摘掉，此时扫描能真正关掉不再有在途流的退役连接。
type reclaimBody struct {
	io.ReadCloser
	transport *poolTransport
	pool      *http.Transport
	swept     atomic.Bool // Read 侧只扫一次，Close 一定再扫一次
}

func (b *reclaimBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && b.swept.CompareAndSwap(false, true) {
		b.transport.reclaim(b.pool)
	}
	return n, err
}

func (b *reclaimBody) Close() error {
	err := b.ReadCloser.Close()
	b.transport.reclaim(b.pool)
	return err
}

// retryable 判断这次失败是否值得换池重试一次。范围刻意收窄：只认无请求体的
// GET，且错误确实代表连接中断。请求 context 已取消或截止、证书与 URL 配置
// 错误、HTTP 状态错误，以及已经拿到响应后的 Body 读取失败都不在这里重试。
func retryable(req *http.Request, err error) bool {
	if req.Method != http.MethodGet {
		return false
	}
	if req.Body != nil && req.Body != http.NoBody {
		return false
	}
	ctx := req.Context()
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return isConnectionBroken(err)
}

// isConnectionBroken 只认确实代表连接中断的错误；普通 error 与任意 net.Error
// 都不算，避免把配置、证书等问题也重试一遍。
func isConnectionBroken(err error) bool {
	for _, target := range []error{io.EOF, io.ErrUnexpectedEOF, syscall.ECONNRESET, syscall.EPIPE} {
		if errors.Is(err, target) {
			return true
		}
	}
	msg := err.Error()
	// 连接在请求过程中被对端断开时，标准库 HTTP/2 客户端给出的固定文本。
	if strings.Contains(msg, "http2: client connection lost") {
		return true
	}
	// 标准库把 HTTP/2 的 StreamError 放在 net/http/internal/http2 里，没有导出
	// 类型可做 errors.As；这里只认「服务端主动重置单个流」这一种已知形态：
	// stream error: stream ID 357; INTERNAL_ERROR; received from peer。
	return strings.Contains(msg, "stream error: stream ID ") &&
		strings.Contains(msg, "; INTERNAL_ERROR; received from peer")
}
