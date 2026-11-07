package cline

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"mcode/internal/providers/acp"
)

// readFixture 读 testdata 下的 fixture。
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读 fixture %s 失败: %v", name, err)
	}
	return data
}

// redirectTransport 把请求改写到测试服务器，让硬编码的 models.dev URL 也能被 httptest 服务。
type redirectTransport struct {
	target *url.URL
	rt     http.RoundTripper
}

func (t redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	return t.rt.RoundTrip(clone)
}

// clientFor 返回一个把 models.dev 请求转发到 server 的客户端。
func clientFor(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("解析测试服务器地址失败: %v", err)
	}
	return &http.Client{Transport: redirectTransport{target: target, rt: server.Client().Transport}}
}

func TestSeedModels(t *testing.T) {
	models := SeedModels()
	if len(models) != 4 {
		t.Fatalf("期望 4 个模型，实际 %+v", models)
	}
	wantIDs := []string{
		"cline-pass/deepseek-v4.1-flash",
		"cline-pass/deepseek-v4-pro",
		"cline-pass/mimo-v2.6-flash",
		"cline-pass/glm-5.3",
	}
	wantNames := []string{"DeepSeek V4.1 Flash", "DeepSeek V4 Pro", "MiMo-V2.6-Flash", "GLM-5.3"}
	for i, want := range wantIDs {
		if models[i].ID != want || models[i].Name != wantNames[i] {
			t.Errorf("第 %d 项不符: %+v", i, models[i])
		}
		if models[i].Detail == "" {
			t.Errorf("第 %d 项缺少 detail: %+v", i, models[i])
		}
	}
	for i, model := range models {
		wantDefault := i == 0
		if model.Default != wantDefault {
			t.Errorf("第 %d 项 Default = %v，期望 %v", i, model.Default, wantDefault)
		}
	}

	// 每次调用返回副本：改动一次的结果不影响下一次。
	models[0].Name = "changed"
	models[0].Default = false
	if again := SeedModels(); again[0].Name != "DeepSeek V4.1 Flash" || !again[0].Default {
		t.Errorf("返回值被共享: %+v", again[0])
	}
}

func TestCatalogModelsMergesPricing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(readFixture(t, "models_dev_cline.json"))
	}))
	defer server.Close()

	resetPricingCache()
	defer resetPricingCache()

	models, err := CatalogModels(context.Background(), clientFor(t, server))
	if err != nil {
		t.Fatalf("CatalogModels 失败: %v", err)
	}
	if len(models) != 4 {
		t.Fatalf("期望 4 个模型，实际 %+v", models)
	}
	flash := models[0]
	if flash.InputPerMTok != 0.15 || flash.OutputPerMTok != 0.6 || flash.ContextWindow != 1000000 {
		t.Errorf("deepseek-v4.1-flash 价格未合并: %+v", flash)
	}
	glm := models[3]
	if glm.InputPerMTok != 0.6 || glm.OutputPerMTok != 2.2 || glm.ContextWindow != 200000 {
		t.Errorf("glm-5.3 价格未合并: %+v", glm)
	}
	if mimo := models[2]; mimo.InputPerMTok != 0 || mimo.ContextWindow != 0 {
		t.Errorf("models.dev 未列出的模型应保持 0: %+v", mimo)
	}
	if !flash.Default || flash.Name != "DeepSeek V4.1 Flash" {
		t.Errorf("初始列表字段应保留: %+v", flash)
	}
}

func TestCatalogModelsPricingFailureKeepsSeed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	resetPricingCache()
	defer resetPricingCache()

	models, err := CatalogModels(context.Background(), clientFor(t, server))
	if err == nil {
		t.Fatal("价格拉取失败时应返回错误")
	}
	if len(models) != 4 {
		t.Fatalf("失败时仍应返回 4 个初始模型，实际 %+v", models)
	}
	if models[0].ID != "cline-pass/deepseek-v4.1-flash" || !models[0].Default {
		t.Errorf("初始列表不符: %+v", models[0])
	}
}

func TestParsePricing(t *testing.T) {
	pricing, err := ParsePricing(readFixture(t, "models_dev_cline.json"))
	if err != nil {
		t.Fatalf("ParsePricing 失败: %v", err)
	}
	if len(pricing) != 2 {
		t.Fatalf("期望 2 条价格，实际 %+v", pricing)
	}
	flash, ok := pricing["deepseek-v4.1-flash"]
	if !ok {
		t.Fatalf("缺少 deepseek-v4.1-flash: %+v", pricing)
	}
	if flash.InputPerMTok != 0.15 || flash.OutputPerMTok != 0.6 || flash.ContextWindow != 1000000 {
		t.Errorf("deepseek-v4.1-flash 价格不符: %+v", flash)
	}
	if glm := pricing["glm-5.3"]; glm.InputPerMTok != 0.6 || glm.OutputPerMTok != 2.2 || glm.ContextWindow != 200000 {
		t.Errorf("glm-5.3 价格不符: %+v", glm)
	}
}

func TestParsePricingMissingProvider(t *testing.T) {
	if _, err := ParsePricing([]byte(`{"openai":{"models":{}}}`)); err == nil {
		t.Error("没有 cline-pass 条目时应报错")
	}
}

func TestMergePricing(t *testing.T) {
	models := []Model{
		{ID: "cline-pass/deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash"},
		{ID: "cline-pass/glm-5.3", Name: "GLM-5.3", ContextWindow: 4096, InputPerMTok: 9.9},
		{ID: "unknown-model", Name: "Unknown"},
	}
	pricing := map[string]Pricing{
		"deepseek-v4.1-flash": {InputPerMTok: 0.15, OutputPerMTok: 0.6, ContextWindow: 1000000},
		"glm-5.3":             {InputPerMTok: 0.6, OutputPerMTok: 2.2, ContextWindow: 200000},
	}

	merged := MergePricing(models, pricing)
	if len(merged) != 3 {
		t.Fatalf("长度变了: %+v", merged)
	}
	if merged[0].InputPerMTok != 0.15 || merged[0].OutputPerMTok != 0.6 || merged[0].ContextWindow != 1000000 {
		t.Errorf("带前缀 id 应被补齐: %+v", merged[0])
	}
	// 已有值保留，只补 0 的字段。
	if merged[1].ContextWindow != 4096 || merged[1].InputPerMTok != 9.9 || merged[1].OutputPerMTok != 2.2 {
		t.Errorf("已有值不应被覆盖: %+v", merged[1])
	}
	if merged[2].InputPerMTok != 0 || merged[2].ContextWindow != 0 {
		t.Errorf("未知模型应保持 0: %+v", merged[2])
	}
	// 返回新切片，原切片不变。
	if models[0].InputPerMTok != 0 || models[1].OutputPerMTok != 0 {
		t.Errorf("原切片被改动了: %+v", models)
	}
}

func TestCachedPricingCaches(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write(readFixture(t, "models_dev_cline.json"))
	}))
	defer server.Close()

	resetPricingCache()

	for i := 0; i < 2; i++ {
		pricing, err := CachedPricing(context.Background(), server.Client(), server.URL)
		if err != nil {
			t.Fatalf("CachedPricing 失败: %v", err)
		}
		if len(pricing) != 2 {
			t.Fatalf("价格条数不符: %+v", pricing)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("第二次应命中缓存，实际请求 %d 次", got)
	}
	resetPricingCache()
}

func TestProbedModelsDefault(t *testing.T) {
	got := probedModels([]acp.ProbedModel{
		{ID: "cline-pass/glm-5.3", Name: "GLM-5.3"},
		{ID: "anthropic/claude-opus-5-5", Current: true},
	})
	if len(got) != 2 || got[0].Default || !got[1].Default || got[1].Name != "claude-opus-5-5" {
		t.Fatalf("current model should be default and nameless rows use the id tail: %+v", got)
	}
	got = probedModels([]acp.ProbedModel{{ID: "a/b"}, {ID: "a/c"}})
	if !got[0].Default || got[1].Default {
		t.Fatalf("first row should default when none is current: %+v", got)
	}
}

// 标记次要模型：保留全部模型、当前渠道的主模型在前且全部非次要，默认只落在主模型上。
func TestMarkProviderModelsSecondary(t *testing.T) {
	got := markProviderModels([]Model{
		{ID: "cline-free/glm-5.3", Default: true},
		{ID: "cline-pass/deepseek-v4.1-flash"},
		{ID: "cline-free/kimi-k3"},
		{ID: "cline-pass/opus-5"},
	}, "cline-pass")
	wantIDs := []string{"cline-pass/deepseek-v4.1-flash", "cline-pass/opus-5", "cline-free/glm-5.3", "cline-free/kimi-k3"}
	if len(got) != len(wantIDs) {
		t.Fatalf("应保留全部模型: %+v", got)
	}
	for i, id := range wantIDs {
		if got[i].ID != id {
			t.Fatalf("第 %d 个模型应为 %q，实际 %+v", i, id, got)
		}
	}
	if got[0].Secondary || got[1].Secondary {
		t.Fatalf("当前渠道的模型不应标为次要: %+v", got)
	}
	if !got[2].Secondary || !got[3].Secondary {
		t.Fatalf("其他渠道的模型应标为次要: %+v", got)
	}
	// 原默认落在次要模型上：清掉，改由第一个主模型兜底。
	if !got[0].Default || got[2].Default || got[3].Default {
		t.Fatalf("默认只应落在主模型上: %+v", got)
	}

	// 主模型自带默认时保持不动。
	got = markProviderModels([]Model{
		{ID: "cline-pass/a", Default: true},
		{ID: "cline-free/b", Default: true},
	}, "cline-pass")
	if !got[0].Default || got[1].Default || got[0].Secondary || !got[1].Secondary {
		t.Fatalf("主模型的默认应保留、次要模型的默认应清掉: %+v", got)
	}

	// 没有任何主模型时原样返回，全部保持非次要。
	got = markProviderModels([]Model{{ID: "cline-free/a", Default: true}, {ID: "stealth/b"}}, "cline-pass")
	if got[0].Secondary || got[1].Secondary || !got[0].Default || got[0].ID != "cline-free/a" || got[1].ID != "stealth/b" {
		t.Fatalf("没有主模型时应原样返回: %+v", got)
	}
}
