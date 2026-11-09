package llm

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// UserAgent 是自研引擎对外 HTTP 请求的 User-Agent，启动时由 app 设置为 mcode/<版本>。
var UserAgent = "mcode"

var streamIdleTimeout = 2 * time.Minute

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
// 只限制等待响应头的时间：端点连上后不返回时能及时发现，同时不设置
// Client.Timeout，避免流式长回答被整体掐断。
var defaultHTTPClient = newDefaultHTTPClient()

func newDefaultHTTPClient() *http.Client {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		transport = &http.Transport{}
	}
	clone := transport.Clone()
	clone.ResponseHeaderTimeout = 2 * time.Minute
	return &http.Client{Transport: clone}
}
