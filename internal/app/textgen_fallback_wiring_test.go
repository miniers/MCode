package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"mcode/internal/config"
)

func TestGenerateTextCLIDoesNotCollectMagpie(t *testing.T) {
	withoutCLI(t)
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer server.Close()
	cfg := mcodeTestConfig(config.CustomEndpointConfig{ID: "edge", Enabled: true, Magpie: true, BaseURL: server.URL})
	cfg.Providers.Mcode.Enabled = false
	cfg.Providers.Claude.Bin = "/nonexistent/claude"
	secrets := mcodeTestSecrets(map[string]string{config.CustomEndpointSecretAccount("edge"): "gateway-key"})

	_, _ = generateText(context.Background(), cfg, secrets, "claude", "", "", "p")
	if got := hits.Load(); got != 0 {
		t.Fatalf("CLI 不应请求 Magpie，实际 %d 次", got)
	}
}

func TestGenerateTextAPISourceDoesNotCollectOtherSource(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_, _ = io.WriteString(w, `{"data":[]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, mcodeTextgenStream)
	}))
	defer first.Close()
	var otherHits atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHits.Add(1)
	}))
	defer second.Close()
	cfg := mcodeTestConfig(
		config.CustomEndpointConfig{ID: "a", Enabled: true, BaseURL: first.URL, Model: "plain-model"},
		config.CustomEndpointConfig{ID: "b", Enabled: true, BaseURL: second.URL, Model: "plain-model"},
	)

	out, err := generateText(context.Background(), cfg, nil, strings.ToUpper(config.CustomEndpointKind("a")), "", "", "p")
	if err != nil || out != "会话标题" {
		t.Fatalf("输出 = %q，错误 = %v", out, err)
	}
	if got := otherHits.Load(); got != 0 {
		t.Fatalf("不应请求其他来源，实际 %d 次", got)
	}
}
