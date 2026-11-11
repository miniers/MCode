package llm

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"mcode/internal/providers/readhttp"
)

// UserAgent 是自研引擎对外 HTTP 请求的 User-Agent，启动时由 app 设置为 mcode-app/<版本>。
var UserAgent = "mcode-app"

var streamIdleTimeout = 2 * time.Minute

const http2PingTimeout = 15 * time.Second

func retryAfter(header string) time.Duration {
	header = strings.TrimSpace(header)
	if seconds, err := strconv.ParseInt(header, 10, 64); err == nil {
		if seconds > 0 && seconds <= int64((1<<63-1)/int64(time.Second)) {
			return time.Duration(seconds) * time.Second
		}
		return 0
	}
	if date, err := http.ParseTime(header); err == nil {
		if delay := time.Until(date); delay > 0 {
			return delay
		}
	}
	return 0
}

// defaultHTTPClient 是未显式配置 HTTPClient 时使用的共享客户端。
// 限制响应头等待时间，并通过 HTTP/2 ping 发现和淘汰断网后的死连接。
// 流被对端重置时换池，避免后续请求继续使用坏连接。
// 不设置 Client.Timeout，避免流式长回答被整体掐断。
var defaultHTTPClient = newDefaultHTTPClient()

func newDefaultTransport() *http.Transport {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		transport = &http.Transport{}
	}
	clone := transport.Clone()
	clone.ResponseHeaderTimeout = 2 * time.Minute
	clone.HTTP2 = &http.HTTP2Config{SendPingTimeout: http2PingTimeout, PingTimeout: http2PingTimeout}
	return clone
}

func newDefaultHTTPClient() *http.Client {
	return readhttp.NewClient(&http.Client{Transport: newDefaultTransport()})
}
