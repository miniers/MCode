package magpie

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestModelProvider(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/v1/models" {
			t.Errorf("请求路径 = %q", r.URL.Path)
		}
		io.WriteString(w, `{"data":[
			{"id":"auto/model","owned_by":" DeepSeek "},
			{"id":"unknown","owned_by":"  "}]}`)
	}))
	defer srv.Close()
	for _, tc := range []struct {
		model string
		want  string
	}{
		{"auto/model", "deepseek"},
		{"model", ""},
		{"AUTO/model", ""},
		{"unknown", ""},
		{"missing", ""},
	} {
		provider, ok := ModelProvider(context.Background(), srv.URL+"/v1", "key", tc.model)
		if provider != tc.want || ok != (tc.want != "") {
			t.Errorf("ModelProvider(%q) = %q, %v", tc.model, provider, ok)
		}
	}
	if hits.Load() != 1 {
		t.Errorf("模型目录未复用缓存，请求次数 = %d", hits.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if provider, ok := ModelProvider(ctx, srv.URL+"/uncached", "key", "auto/model"); provider != "" || ok {
		t.Errorf("目录读取失败应返回未知: %q, %v", provider, ok)
	}
}
