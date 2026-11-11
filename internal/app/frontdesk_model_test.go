package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"mcode/internal/config"
	"mcode/internal/frontdesk"
	"mcode/internal/providers/magpie"
)

func TestFrontdeskSourcesMagpieCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"large-model"},{"id":"gemini-flash"}]}`)
	}))
	defer server.Close()
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "edge", Name: "Magpie", Enabled: true, Magpie: true, BaseURL: server.URL + "/v1"})
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gateway-key"})

	sources := frontdeskSources(cfg, secrets)
	if len(sources) == 0 || len(sources[0].Models) != 0 {
		t.Fatalf("缓存未命中时模型列表应为空：%+v", sources)
	}
	if _, err := magpie.FetchModels(context.Background(), server.URL+"/v1", "gateway-key"); err != nil {
		t.Fatal(err)
	}
	sources = frontdeskSources(cfg, secrets)
	if len(sources) == 0 || len(sources[0].Models) != 2 {
		t.Fatalf("未读取缓存模型：%+v", sources)
	}
	// 端点没有默认模型：「自动」取目录里第一个，不按名字挑 flash 一类的小模型。
	_, model, err := frontdesk.SelectSource(sources, secrets, "", "")
	if err != nil || model != "large-model" {
		t.Fatalf("选择模型 = %q，错误 %v", model, err)
	}
}
