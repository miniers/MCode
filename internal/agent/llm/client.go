package llm

import (
	"net/http"
	"time"
)

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
