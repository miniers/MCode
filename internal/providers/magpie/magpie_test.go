package magpie

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mcode/internal/providers/readhttp"
)

// level 取出 thinkingLevelMap 里一个档位的映射，第二个返回值表示是否非 nil。
func level(levels map[string]*string, name string) (string, bool) {
	v, ok := levels[name]
	if !ok || v == nil {
		return "", false
	}
	return *v, true
}

func TestThinkingLevelsNormalizesNames(t *testing.T) {
	tests := []struct {
		name    string
		efforts []effort
		want    map[string]string
	}{
		{
			name: "小写档位",
			efforts: []effort{{Effort: "low"}, {Effort: "medium"}, {Effort: "high"},
				{Effort: "xhigh"}, {Effort: "max"}},
			want: map[string]string{"low": "low", "medium": "medium", "high": "high", "xhigh": "xhigh", "max": "max"},
		},
		{
			name:    "大小写、空白及重复档位",
			efforts: []effort{{Effort: "XHigh"}, {Effort: " Max "}, {Effort: "NONE"}, {Effort: "xhigh"}},
			want:    map[string]string{"xhigh": "XHigh", "max": "Max", "off": "NONE"},
		},
		{name: "空输入"},
		{name: "空白档位", efforts: []effort{{Effort: ""}, {Effort: " \t "}}},
		{name: "未知档位", efforts: []effort{{Effort: "ultra"}}, want: map[string]string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := thinkingLevels(tt.efforts)
			if tt.want == nil {
				if got != nil {
					t.Errorf("thinkingLevels = %v，想要 nil", got)
				}
				return
			}
			if len(got) != len(piLevels) {
				t.Errorf("档位数 = %d，想要 %d", len(got), len(piLevels))
			}
			for _, name := range piLevels {
				if _, exists := got[name]; !exists {
					t.Errorf("缺少档位 %q", name)
				}
				value, ok := level(got, name)
				want, supported := tt.want[name]
				if ok != supported || value != want {
					t.Errorf("档位 %q = %q,%v，想要 %q,%v", name, value, ok, want, supported)
				}
			}
		})
	}
}

func TestFetchModels(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("请求路径 = %q，想要 /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer gw-key" {
			t.Errorf("Authorization = %q，想要 Bearer gw-key", got)
		}
		atomic.AddInt32(&hits, 1)
		io.WriteString(w, `{"object":"list","data":[
			{"id":"jp/auto/claude-sonnet-5-5","display_name":"Claude Sonnet 5.5",
			 "magpie_label":"Claude Sonnet 5.5 · routing group · jp","reasoning":true,
			 "supported_reasoning_levels":[{"effort":"none"},{"effort":"low"},{"effort":"high"}],
			 "context_window":1000000,"context_length":1000000,"max_input_tokens":1000000,"max_output_tokens":128000,
			 "modalities":{"input":["text","image","pdf"],"output":["text"]}},
			{"id":"cline/gpt-x","display_name":"GPT X"}]}`)
	}))
	defer srv.Close()

	models, err := FetchModels(context.Background(), srv.URL+"/v1", "gw-key")
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("模型数 = %d，想要 2", len(models))
	}

	first := models[0]
	if first.ID != "jp/auto/claude-sonnet-5-5" {
		t.Errorf("ID = %q", first.ID)
	}
	// Name 保留网关给的名字全文，显示时由 ShortName 精简。
	if first.Name != "Claude Sonnet 5.5 · routing group · jp" {
		t.Errorf("Name = %q", first.Name)
	}
	if first.ContextWindow != 1000000 || first.MaxTokens != 128000 {
		t.Errorf("窗口 = %d / %d，想要 1000000 / 128000", first.ContextWindow, first.MaxTokens)
	}
	if !first.Reasoning {
		t.Error("Reasoning = false，想要 true")
	}
	// 只留 pi 认的两种输入。
	if strings.Join(first.Input, ",") != "text,image" {
		t.Errorf("Input = %v，想要 [text image]", first.Input)
	}
	if first.Cost != nil {
		t.Errorf("Cost = %v，想要 nil", first.Cost)
	}
	if v, ok := level(first.ThinkingLevelMap, "off"); !ok || v != "none" {
		t.Errorf("thinkingLevelMap[off] = %q,%v，想要 none", v, ok)
	}
	if v, ok := level(first.ThinkingLevelMap, "high"); !ok || v != "high" {
		t.Errorf("thinkingLevelMap[high] = %q,%v，想要 high", v, ok)
	}
	if _, ok := level(first.ThinkingLevelMap, "medium"); ok {
		t.Error("thinkingLevelMap[medium] 应当为 nil")
	}

	// 响应里没有元数据时用默认值。
	second := models[1]
	if second.Name != "GPT X" {
		t.Errorf("Name = %q，想要 GPT X", second.Name)
	}
	if second.ContextWindow != defaultContextWindow || second.MaxTokens != defaultMaxTokens {
		t.Errorf("默认窗口 = %d / %d，想要 %d / %d", second.ContextWindow, second.MaxTokens, defaultContextWindow, defaultMaxTokens)
	}
	if !second.Reasoning {
		t.Error("Reasoning 缺省时应当为 true")
	}
	if strings.Join(second.Input, ",") != "text,image" {
		t.Errorf("默认 Input = %v，想要 [text image]", second.Input)
	}
	if second.ThinkingLevelMap != nil {
		t.Errorf("ThinkingLevelMap = %v，想要 nil", second.ThinkingLevelMap)
	}

	// 5 分钟内的第二次读取走缓存，不再请求。
	if _, err := FetchModels(context.Background(), srv.URL+"/v1", "gw-key"); err != nil {
		t.Fatalf("FetchModels（第二次）: %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("请求次数 = %d，想要 1（缓存）", n)
	}
}

// TestFetchModelsPrices 校验请求带 X-Magpie-Prices，带价的模型行解析成 Cost，
// 不报价时 Cost 为 nil。
func TestFetchModelsPrices(t *testing.T) {
	resetModelsCache(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Magpie-Prices"); got != "1" {
			t.Errorf("X-Magpie-Prices = %q，想要 1", got)
		}
		io.WriteString(w, `{"object":"list","data":[
			{"id":"a/priced","magpie_price":{"input":3,"output":12,"cache_read":0.3,"cache_write":3.75}},
			{"id":"a/free","magpie_price":{"input":0,"output":0,"cache_read":0,"cache_write":0}},
			{"id":"a/plain"}]}`)
	}))
	defer srv.Close()

	models, err := FetchModels(context.Background(), srv.URL+"/v1", "gw-key")
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("模型数 = %d，想要 3", len(models))
	}
	want := Cost{Input: 3, Output: 12, CacheRead: 0.3, CacheWrite: 3.75}
	if got := models[0].Cost; got == nil || *got != want {
		t.Errorf("Cost = %+v，想要 %+v", got, want)
	}
	// 网关报价 0 表示确实免费，与未知价格（nil）区分。
	if got := models[1].Cost; got == nil || *got != (Cost{}) {
		t.Errorf("零价 Cost = %+v，想要零值而非 nil", got)
	}
	if got := models[2].Cost; got != nil {
		t.Errorf("未报价 Cost = %+v，想要 nil", got)
	}
}

func TestCostUSD(t *testing.T) {
	tests := []struct {
		name                                 string
		cost                                 Cost
		input, output, cacheRead, cacheWrite int64
		want                                 float64
	}{
		{name: "全部为 0"},
		{
			name: "只有输入和输出",
			cost: Cost{Input: 3, Output: 15}, input: 1_000_000, output: 200_000,
			want: 6,
		},
		{
			name:  "带缓存读写",
			cost:  Cost{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75},
			input: 1000, output: 500, cacheRead: 100_000, cacheWrite: 20_000,
			want: 0.1155,
		},
		{
			name: "单价为 0 的部分不计费",
			cost: Cost{Input: 2}, input: 1_000_000, cacheRead: 500_000,
			want: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cost.USD(tt.input, tt.output, tt.cacheRead, tt.cacheWrite)
			if math.Abs(got-tt.want) > 1e-12 {
				t.Errorf("USD = %v，想要 %v", got, tt.want)
			}
		})
	}
}

// TestFetchModelsLevels 校验网关给的原始档位保留在 Model.Levels：小写、去空白、
// 丢空串、保持网关顺序，不进 pi 的 models.json，且 CachedModel 能只读取到。
func TestFetchModelsLevels(t *testing.T) {
	resetModelsCache(t)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		io.WriteString(w, `{"object":"list","data":[
			{"id":"cline/gpt-x","supported_reasoning_levels":[{"effort":"none"},{"effort":" Low "},{"effort":"HIGH"},{"effort":""}]},
			{"id":"cline/plain"}]}`)
	}))
	defer srv.Close()

	base := srv.URL + "/v1"
	models, err := FetchModels(context.Background(), base, "key-levels")
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if got := strings.Join(models[0].Levels, ","); got != "none,low,high" {
		t.Errorf("Levels = %v，想要 [none low high]", models[0].Levels)
	}
	if models[1].Levels != nil {
		t.Errorf("没有档位时 Levels = %v，想要 nil", models[1].Levels)
	}
	// Levels 不写进 pi 的 models.json。
	env, err := PiEnvJSON([]PiProvider{{Provider: "magpie-jp", Models: models}})
	if err != nil {
		t.Fatalf("PiEnvJSON: %v", err)
	}
	if strings.Contains(env, "Levels") || strings.Contains(env, `"levels"`) {
		t.Errorf("JSON = %s，不该包含 Levels", env)
	}

	// 缓存命中时 CachedModel 不发请求，返回同样的档位。
	got, ok := CachedModel(base, "key-levels", "cline/gpt-x")
	if !ok {
		t.Fatal("CachedModel 未命中")
	}
	if strings.Join(got.Levels, ",") != "none,low,high" {
		t.Errorf("CachedModel Levels = %v，想要 [none low high]", got.Levels)
	}
	// 改返回值不污染缓存。
	got.Levels[0] = "max"
	again, ok := CachedModel(base, "key-levels", "cline/gpt-x")
	if !ok || strings.Join(again.Levels, ",") != "none,low,high" {
		t.Errorf("再次 CachedModel = %v,%v，想要 [none low high],true", again.Levels, ok)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("请求次数 = %d，想要 1（CachedModel 不发请求）", n)
	}

	// 未缓存的端点、不存在的 ID、失败缓存都返回 false。
	if _, ok := CachedModel(srv.URL+"/other", "key-levels", "cline/gpt-x"); ok {
		t.Error("未缓存的端点应当未命中")
	}
	if _, ok := CachedModel(base, "key-levels", "cline/missing"); ok {
		t.Error("不存在的 ID 应当未命中")
	}
	storeModelsError(srv.URL+"/failed\x00key-levels", errors.New("偶发故障"), time.Now())
	if _, ok := CachedModel(srv.URL+"/failed", "key-levels", "cline/gpt-x"); ok {
		t.Error("失败缓存应当未命中")
	}
}

// TestCachedModels 校验 CachedModels 只读缓存：FetchModels 成功后能拿到同样的列表，
// 没拉过的端点和失败缓存都返回 false，且不发请求。
func TestCachedModels(t *testing.T) {
	resetModelsCache(t)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		io.WriteString(w, `{"object":"list","data":[{"id":"cline/gpt-x","context_window":300000},{"id":"cline/plain"}]}`)
	}))
	defer srv.Close()

	base := srv.URL + "/v1"
	if _, ok := CachedModels(base, "key-list"); ok {
		t.Fatal("没拉过时 CachedModels 应当未命中")
	}
	want, err := FetchModels(context.Background(), base, "key-list")
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	got, ok := CachedModels(base, "key-list")
	if !ok || len(got) != len(want) {
		t.Fatalf("CachedModels = %v,%v，想要 %v,true", got, ok, want)
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].ContextWindow != want[i].ContextWindow {
			t.Fatalf("第 %d 个模型 = %+v，想要 %+v", i, got[i], want[i])
		}
	}
	// 改返回值不污染缓存。
	got[0].ID = "changed"
	again, ok := CachedModels(base, "key-list")
	if !ok || again[0].ID != "cline/gpt-x" {
		t.Fatalf("再次 CachedModels = %v,%v", again, ok)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("请求次数 = %d，想要 1（CachedModels 不发请求）", n)
	}

	// 失败缓存与没拉过的端点都返回 false。
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":"boom"}`)
	}))
	defer bad.Close()
	if _, err := FetchModels(context.Background(), bad.URL+"/v1", "key-list"); err == nil {
		t.Fatal("失败端点应当返回错误")
	}
	if _, ok := CachedModels(bad.URL+"/v1", "key-list"); ok {
		t.Error("失败缓存应当未命中")
	}
	if _, ok := CachedModels(srv.URL+"/other/v1", "key-list"); ok {
		t.Error("没拉过的端点应当未命中")
	}
}

func TestFetchModelsErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"bad key"}`)
	}))
	defer srv.Close()

	_, err := FetchModels(context.Background(), srv.URL+"/v1", "")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("错误 = %v，想要含状态码 401", err)
	}
	if _, err := FetchModels(context.Background(), "  ", "gw-key"); err == nil {
		t.Fatal("baseURL 为空时应当报错")
	}
}

// resetModelsCache 清空模型列表缓存，避免用例之间互相影响。
func resetModelsCache(t *testing.T) {
	t.Helper()
	modelsCache.Lock()
	defer modelsCache.Unlock()
	modelsCache.entries = map[string]modelsCacheEntry{}
}

// ageModelsCache 把一条缓存条目的写入时间往前推 d，用来模拟时间流逝。
func ageModelsCache(t *testing.T, key string, d time.Duration) {
	t.Helper()
	modelsCache.Lock()
	defer modelsCache.Unlock()
	e, ok := modelsCache.entries[key]
	if !ok {
		t.Fatalf("缓存里没有 %q 这个键", key)
	}
	e.written = e.written.Add(-d)
	modelsCache.entries[key] = e
}

// TestFetchModelsCachesFailure 校验端点返回 500 后 30 秒内的重复调用不再发请求，
// 超过 30 秒后重新请求，端点恢复就拿到模型列表。
func TestFetchModelsCachesFailure(t *testing.T) {
	resetModelsCache(t)
	var hits int32
	var fail atomic.Bool
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":"boom"}`)
			return
		}
		io.WriteString(w, `{"object":"list","data":[{"id":"cline/gpt-x"}]}`)
	}))
	defer srv.Close()

	base := srv.URL + "/v1"
	cacheKey := base + "\x00" + "key-fail"
	_, first := FetchModels(context.Background(), base, "key-fail")
	if first == nil || !strings.Contains(first.Error(), "500") {
		t.Fatalf("第一次错误 = %v，想要含状态码 500", first)
	}
	// 30 秒内的第二次调用走失败缓存，错误相同且不再发请求。
	_, second := FetchModels(context.Background(), base, "key-fail")
	if second == nil || second.Error() != first.Error() {
		t.Fatalf("第二次错误 = %v，想要与第一次相同: %v", second, first)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("请求次数 = %d，想要 1（失败缓存）", n)
	}

	// 超过 30 秒后重新请求，端点已恢复就返回模型列表。
	ageModelsCache(t, cacheKey, modelsFailureTTL+time.Second)
	fail.Store(false)
	models, err := FetchModels(context.Background(), base, "key-fail")
	if err != nil {
		t.Fatalf("恢复后的 FetchModels: %v", err)
	}
	if len(models) != 1 || models[0].ID != "cline/gpt-x" {
		t.Fatalf("模型 = %v，想要 cline/gpt-x", models)
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Errorf("请求次数 = %d，想要 2（失败缓存已过期）", n)
	}
}

// TestFetchModelsDoesNotCacheCanceledCall 校验调用方自己取消的结果不进失败缓存，
// 之后用正常 ctx 调用会真的发请求。
func TestFetchModelsDoesNotCacheCanceledCall(t *testing.T) {
	resetModelsCache(t)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":"boom"}`)
	}))
	defer srv.Close()

	base := srv.URL + "/v1"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FetchModels(ctx, base, "key-cancel"); err == nil {
		t.Fatal("ctx 已取消时应当报错")
	}
	// 服务端没收到请求，缓存里也不该有失败条目。
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("取消的调用请求次数 = %d，想要 0", n)
	}
	_, err := FetchModels(context.Background(), base, "key-cancel")
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("正常 ctx 的错误 = %v，想要含状态码 500", err)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("请求次数 = %d，想要 1（取消的结果未被缓存）", n)
	}
}

// TestFetchModelsKeepsSuccessOverFailure 校验成功缓存未过期时失败写入不会覆盖它。
func TestFetchModelsKeepsSuccessOverFailure(t *testing.T) {
	resetModelsCache(t)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		io.WriteString(w, `{"object":"list","data":[{"id":"cline/gpt-x"}]}`)
	}))
	defer srv.Close()

	base := srv.URL + "/v1"
	if _, err := FetchModels(context.Background(), base, "key-keep"); err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	storeModelsError(base+"\x00"+"key-keep", errors.New("偶发故障"), time.Now())
	models, err := FetchModels(context.Background(), base, "key-keep")
	if err != nil {
		t.Fatalf("FetchModels（失败写入之后）: %v", err)
	}
	if len(models) != 1 || models[0].ID != "cline/gpt-x" {
		t.Fatalf("模型 = %v，想要 cline/gpt-x", models)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("请求次数 = %d，想要 1（成功缓存仍有效）", n)
	}
}

// TestFetchModelsFailureCachePerAPIKey 校验失败缓存按 apiKey 分开，一个 key 失败
// 不影响另一个 key 的正常拉取。
func TestFetchModelsFailureCachePerAPIKey(t *testing.T) {
	resetModelsCache(t)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.Header.Get("Authorization") == "Bearer bad-key" {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":"boom"}`)
			return
		}
		io.WriteString(w, `{"object":"list","data":[{"id":"cline/gpt-x"}]}`)
	}))
	defer srv.Close()

	base := srv.URL + "/v1"
	if _, err := FetchModels(context.Background(), base, "bad-key"); err == nil {
		t.Fatal("bad-key 应当报错")
	}
	models, err := FetchModels(context.Background(), base, "good-key")
	if err != nil {
		t.Fatalf("good-key 的 FetchModels: %v", err)
	}
	if len(models) != 1 || models[0].ID != "cline/gpt-x" {
		t.Fatalf("模型 = %v，想要 cline/gpt-x", models)
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Errorf("请求次数 = %d，想要 2（两个 key 各一次）", n)
	}
}

func TestShortName(t *testing.T) {
	cases := []struct {
		name string
		m    Model
		want string
	}{
		{"名字取 · 之前", Model{ID: "jp/auto/claude-sonnet-5-5", Name: "Claude Sonnet 5.5 · routing group · jp"}, "Claude Sonnet 5.5 (auto)"},
		{"无名字取 ID 末段", Model{ID: "anthropic/claude-opus-4-7"}, "claude-opus-4-7"},
		{"auto 前缀", Model{ID: "auto/gpt-5.6-sol"}, "gpt-5.6-sol (auto)"},
		{"已有 (auto) 不重复", Model{ID: "x/group/grok-4", Name: "Grok 4 (auto)"}, "Grok 4 (auto)"},
	}
	for _, c := range cases {
		if got := ShortName(c.m); got != c.want {
			t.Errorf("%s：ShortName = %q，想要 %q", c.name, got, c.want)
		}
	}
}

func TestProviderID(t *testing.T) {
	for _, name := range []string{"mcode", "anthropic", "openai", "deepseek", "zai", "meta"} {
		if got := ProviderID(name, nil); got != "magpie-"+name {
			t.Errorf("ProviderID(%q) = %q", name, got)
		}
	}
	if got := ProviderID("Anthropic", nil); got != "magpie-anthropic" {
		t.Errorf("ProviderID(Anthropic) = %q", got)
	}
	if got := ProviderID("magpie_jp", nil); got != "magpie-jp" {
		t.Errorf("ProviderID(magpie_jp) = %q，想要 magpie-jp", got)
	}
	if got := ProviderID("  ", nil); got != "magpie-mcode" {
		t.Errorf("ProviderID(空) = %q，想要 magpie-mcode", got)
	}
	if got := ProviderID("Magpie", nil); got != "magpie-mcode" {
		t.Errorf("ProviderID(Magpie) = %q，想要 magpie-mcode", got)
	}
	if got := ProviderID("magpie", nil); got != "magpie-mcode" {
		t.Errorf("ProviderID(magpie) = %q，想要 magpie-mcode", got)
	}
	if got := ProviderID("jp", nil); got != "jp" {
		t.Errorf("ProviderID(jp) = %q，想要 jp", got)
	}
	if got := ProviderID(" JP 东京 ", nil); got != "jp" {
		t.Errorf("ProviderID(JP 东京) = %q，想要 jp", got)
	}
	if got := ProviderID("a__b", nil); got != "a-b" {
		t.Errorf("ProviderID(a__b) = %q，想要 a-b", got)
	}

	taken := map[string]bool{"magpie-jp": true, "magpie-jp-2": true}
	if got := ProviderID("magpie_jp", taken); got != "magpie-jp-3" {
		t.Errorf("ProviderID 冲突 = %q，想要 magpie-jp-3", got)
	}
}

func TestReadQuotas(t *testing.T) {
	const reset = "2026-01-02T03:04:05Z"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/magpie/quotas" {
			t.Errorf("请求路径 = %q，想要 /v1/magpie/quotas", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer gw-key" {
			t.Errorf("Authorization = %q", got)
		}
		io.WriteString(w, `{"object":"list","data":[
			{"provider":"cline","windows":[
				{"name":"5 hours","used":42.5,"resetsAt":"`+reset+`"},
				{"name":"week","used":120}]},
			{"provider":"x/remote","windows":[{"name":"7 days","used":-3}]},
			{"provider":"office/~keys","windows":[{"name":"5 hours","used":10}]},
			{"provider":"codex","windows":[]}]}`)
	}))
	defer srv.Close()

	windows, err := ReadQuotas(context.Background(), srv.URL+"/v1", "gw-key")
	if err != nil {
		t.Fatalf("ReadQuotas: %v", err)
	}
	// cline 两个窗口加远端卡片一个；带 ~ 的与没有窗口的跳过。
	if len(windows) != 3 {
		t.Fatalf("窗口数 = %d，想要 3：%+v", len(windows), windows)
	}

	first := windows[0]
	if first.ID != "cline/5 hours" || first.Account != "cline" || first.Label != "5 hours" {
		t.Errorf("第一个窗口 = %+v", first)
	}
	if first.Period != "5h" {
		t.Errorf("Period = %q，想要 5h", first.Period)
	}
	if first.UsedPercent != 42.5 {
		t.Errorf("UsedPercent = %v，想要 42.5", first.UsedPercent)
	}
	want, _ := time.Parse(time.RFC3339, reset)
	if !first.ResetsAt.Equal(want) {
		t.Errorf("ResetsAt = %v，想要 %v", first.ResetsAt, want)
	}

	// 未用百分比夹到 100。
	if windows[1].Period != "7d" || windows[1].UsedPercent != 100 {
		t.Errorf("week 窗口 = %+v，想要 7d / 100", windows[1])
	}

	// 远端 magpie 的卡片保留 provider 前缀；负的用量夹到 0，没有重置时间。
	remote := windows[2]
	if remote.Account != "x/remote" || remote.Period != "7d" || remote.UsedPercent != 0 {
		t.Errorf("远端窗口 = %+v", remote)
	}
	if !remote.ResetsAt.IsZero() {
		t.Errorf("ResetsAt = %v，想要零值", remote.ResetsAt)
	}
}

// TestReadQuotasSameProviderCards 校验同一 provider 的多张卡片（如登了两个 Claude 账号）
// 分成不同账户：优先用 alias/user，都没有时按返回顺序加序号；单卡片的 provider 名称不变。
func TestReadQuotasSameProviderCards(t *testing.T) {
	const named = `{"object":"list","data":[
		{"provider":"claude","alias":"家里的号","windows":[
			{"name":"5 hours","used":10},{"name":"7 days","used":20}]},
		{"provider":"claude","user":"b@example.com","windows":[
			{"name":"5 hours","used":30},{"name":"7 days","used":40}]},
		{"provider":"cline","windows":[{"name":"5 hours","used":50}]}]}`
	const plain = `{"object":"list","data":[
		{"provider":"claude","windows":[{"name":"5 hours","used":10}]},
		{"provider":"claude","windows":[{"name":"5 hours","used":20}]}]}`

	for _, tc := range []struct {
		name     string
		body     string
		accounts []string
	}{
		{"alias 与 user 区分", named, []string{
			"claude · 家里的号", "claude · 家里的号", "claude · b@example.com", "claude · b@example.com", "cline"}},
		{"没有标识时按返回顺序", plain, []string{"claude", "claude #2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			windows, err := ReadQuotas(context.Background(), srv.URL+"/v1", "gw-key")
			if err != nil {
				t.Fatalf("ReadQuotas: %v", err)
			}
			if len(windows) != len(tc.accounts) {
				t.Fatalf("窗口数 = %d，想要 %d：%+v", len(windows), len(tc.accounts), windows)
			}
			ids := make(map[string]bool, len(windows))
			for i, w := range windows {
				if w.Account != tc.accounts[i] {
					t.Errorf("第 %d 个窗口的账户 = %q，想要 %q", i, w.Account, tc.accounts[i])
				}
				if ids[w.ID] {
					t.Errorf("窗口 ID 重复：%q", w.ID)
				}
				ids[w.ID] = true
			}
			if got := AccountProvider(windows[0].Account); got != "claude" {
				t.Errorf("AccountProvider(%q) = %q，想要 claude", windows[0].Account, got)
			}
		})
	}

	// 两个 Claude 账号的窗口各归各的卡片，单卡片的 cline 名称不变。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, named)
	}))
	defer srv.Close()
	windows, err := ReadQuotas(context.Background(), srv.URL+"/v1", "gw-key")
	if err != nil {
		t.Fatalf("ReadQuotas: %v", err)
	}
	if windows[0].Label != "5 hours" || windows[0].UsedPercent != 10 || windows[1].UsedPercent != 20 {
		t.Errorf("第一个 Claude 账号的窗口 = %+v / %+v", windows[0], windows[1])
	}
	if windows[2].Label != "5 hours" || windows[2].UsedPercent != 30 || windows[3].UsedPercent != 40 {
		t.Errorf("第二个 Claude 账号的窗口 = %+v / %+v", windows[2], windows[3])
	}
	if windows[2].Account == windows[0].Account {
		t.Errorf("两个 Claude 账号的账户相同：%q", windows[2].Account)
	}
	if windows[4].Account != "cline" || windows[4].ID != "cline/5 hours" {
		t.Errorf("单卡片 provider 的窗口 = %+v", windows[4])
	}
}

func TestReadQuotasForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":"held"}`)
	}))
	defer srv.Close()

	_, err := ReadQuotas(context.Background(), srv.URL+"/v1", "gw-key")
	if err == nil {
		t.Fatal("403 时应当报错")
	}
	if !strings.Contains(err.Error(), "没有读取额度的权限（Quotas）") || !strings.Contains(err.Error(), "403") {
		t.Errorf("错误 = %v，想要含 403 与权限说明", err)
	}
}

func TestPiEnvJSON(t *testing.T) {
	got, err := PiEnvJSON(nil)
	if err != nil || got != "" {
		t.Fatalf("PiEnvJSON(nil) = %q, %v，想要空串", got, err)
	}

	got, err = PiEnvJSON([]PiProvider{{
		Provider: "magpie-jp",
		Endpoint: "magpie_jp",
		BaseURL:  "http://127.0.0.1:3425/v1",
		APIKey:   "gw-key",
		Models:   []Model{{ID: "cline/gpt-x", Name: "GPT X", Reasoning: true, Input: []string{"text"}}},
	}})
	if err != nil {
		t.Fatalf("PiEnvJSON: %v", err)
	}
	var decoded []PiProvider
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("解码 %q: %v", got, err)
	}
	if len(decoded) != 1 || decoded[0].Provider != "magpie-jp" || len(decoded[0].Models) != 1 {
		t.Fatalf("解码结果 = %+v", decoded)
	}
	if !strings.Contains(got, `"baseUrl":"http://127.0.0.1:3425/v1"`) || !strings.Contains(got, `"provider":"magpie-jp"`) {
		t.Errorf("JSON = %s，字段名应当是 provider/baseUrl", got)
	}
}

func TestReadQuotasExtendedFields(t *testing.T) {
	const respJSON = `{"object":"list","data":[{
		"provider":"custom",
		"kind":"plan",
		"plan":"Pro",
		"user":"me@example.com",
		"alias":"工作",
		"readAt":"2026-10-31T08:00:00Z",
		"lastServedAt":"2026-11-01T09:30:00Z",
		"last":true,
		"windows":[
			{
				"name":"Credits",
				"used":47.8,
				"display":"$4.78 / $10.00",
				"amount":4.78,
				"limit":10.0,
				"unit":"USD",
				"runsOutAt":"2026-11-01T00:00:00Z"
			},
			{
				"name":"每日签到",
				"text":true,
				"display":"已签到 15 天"
			},
			{
				"name":"钱包",
				"balance":500.5,
				"unit":"积分"
			},
			{
				"name":"5 hours",
				"resetSecs":3600
			}
		]
	}]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, respJSON)
	}))
	defer srv.Close()

	before := time.Now()
	windows, err := ReadQuotas(context.Background(), srv.URL+"/v1", "gw-key")
	after := time.Now()
	if err != nil {
		t.Fatalf("ReadQuotas: %v", err)
	}
	if len(windows) != 4 {
		t.Fatalf("窗口数 = %d，想要 4", len(windows))
	}

	// 1. Credits 窗口
	w0 := windows[0]
	if w0.Label != "Credits" {
		t.Errorf("w0.Label = %q, want 'Credits'", w0.Label)
	}
	if w0.Display != "$4.78 / $10.00" {
		t.Errorf("w0.Display = %q, want '$4.78 / $10.00'", w0.Display)
	}
	if w0.Amount != 4.78 || w0.Limit != 10.0 || w0.Unit != "USD" {
		t.Errorf("w0 amount/limit/unit = %v/%v/%q, want 4.78/10.0/USD", w0.Amount, w0.Limit, w0.Unit)
	}
	if w0.UsedPercent != 47.8 {
		t.Errorf("w0.UsedPercent = %v, want 47.8", w0.UsedPercent)
	}
	wantRunsOut, _ := time.Parse(time.RFC3339, "2026-11-01T00:00:00Z")
	if !w0.RunsOutAt.Equal(wantRunsOut) {
		t.Errorf("w0.RunsOutAt = %v, want %v", w0.RunsOutAt, wantRunsOut)
	}

	// 2. Text 窗口
	w1 := windows[1]
	if !w1.Text {
		t.Errorf("w1.Text = false, want true")
	}
	if w1.Display != "已签到 15 天" {
		t.Errorf("w1.Display = %q, want '已签到 15 天'", w1.Display)
	}

	// 3. Balance 窗口
	w2 := windows[2]
	if w2.Balance == nil || *w2.Balance != 500.5 {
		t.Errorf("w2.Balance = %v, want 500.5", w2.Balance)
	}
	if w2.Unit != "积分" {
		t.Errorf("w2.Unit = %q, want '积分'", w2.Unit)
	}

	// 4. 只带 resetSecs 的窗口
	w3 := windows[3]
	if w3.ResetsAt.IsZero() {
		t.Errorf("w3.ResetsAt 应当非零")
	}
	minExpected := before.Add(3600 * time.Second)
	maxExpected := after.Add(3600 * time.Second)
	if w3.ResetsAt.Before(minExpected) || w3.ResetsAt.After(maxExpected) {
		t.Errorf("w3.ResetsAt = %v, 不在 [%v, %v] 之间", w3.ResetsAt, minExpected, maxExpected)
	}

	// 卡片级字段：每个窗口都带上所属卡片的 provider、种类与最近应答信息。
	wantReadAt, _ := time.Parse(time.RFC3339, "2026-10-31T08:00:00Z")
	wantLastServedAt, _ := time.Parse(time.RFC3339, "2026-11-01T09:30:00Z")
	for _, w := range windows {
		if w.Provider != "custom" {
			t.Errorf("%s: Provider = %q, want 'custom'", w.ID, w.Provider)
		}
		if w.Kind != "plan" || w.Plan != "Pro" || w.User != "me@example.com" || w.Alias != "工作" {
			t.Errorf("%s: kind/plan/user/alias = %q/%q/%q/%q, want plan/Pro/me@example.com/工作",
				w.ID, w.Kind, w.Plan, w.User, w.Alias)
		}
		if !w.ReadAt.Equal(wantReadAt) {
			t.Errorf("%s: ReadAt = %v, want %v", w.ID, w.ReadAt, wantReadAt)
		}
		if !w.LastServedAt.Equal(wantLastServedAt) {
			t.Errorf("%s: LastServedAt = %v, want %v", w.ID, w.LastServedAt, wantLastServedAt)
		}
		if !w.Last {
			t.Errorf("%s: Last = false, want true", w.ID)
		}
	}
}

// connIDKey 在测试里标记「请求来自第几条底层连接」。
type connIDKey struct{}

// newAbortFirstConnServer 起一个 HTTP/2 测试服务：第一条底层连接上的请求一律
// 用 panic(http.ErrAbortHandler) 让服务端回 RST_STREAM（客户端看到
// "stream error: ...; INTERNAL_ERROR; received from peer"），之后的连接正常处理。
func newAbortFirstConnServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	var conns, requests atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if id, _ := r.Context().Value(connIDKey{}).(int64); id == 1 {
			panic(http.ErrAbortHandler)
		}
		handler(w, r)
	}))
	srv.EnableHTTP2 = true
	srv.Config.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
		return context.WithValue(ctx, connIDKey{}, conns.Add(1))
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, &conns, &requests
}

// TestFetchModelsRecoversFromStreamError 校验生产路径确实用上了专属读取客户端：
// 首条连接被流中断时换池重试一次即成功，第二次读取仍走原有缓存。
func TestFetchModelsRecoversFromStreamError(t *testing.T) {
	srv, conns, requests := newAbortFirstConnServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"object":"list","data":[{"id":"jp/auto/claude-sonnet-5-5","display_name":"Claude Sonnet 5.5"}]}`)
	})
	restore := modelsHTTPClient
	modelsHTTPClient = readhttp.NewClient(srv.Client())
	t.Cleanup(func() { modelsHTTPClient = restore })

	models, err := FetchModels(context.Background(), srv.URL+"/v1", "gw-key-recover")
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if len(models) != 1 || models[0].ID != "jp/auto/claude-sonnet-5-5" {
		t.Fatalf("模型 = %+v", models)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("请求数 = %d，想要 2（首条中断 + 一次重试）", got)
	}
	if got := conns.Load(); got != 2 {
		t.Fatalf("底层连接数 = %d，想要 2（换池后新连接）", got)
	}

	// 缓存语义不变：同一个 key 再读不再发请求。
	if _, err := FetchModels(context.Background(), srv.URL+"/v1", "gw-key-recover"); err != nil {
		t.Fatalf("第二次 FetchModels: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Errorf("请求数 = %d，想要 2（命中缓存）", got)
	}
}

// TestReadQuotasRecoversFromStreamError 校验额度读取同样接入恢复逻辑。
func TestReadQuotasRecoversFromStreamError(t *testing.T) {
	srv, conns, requests := newAbortFirstConnServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"object":"list","data":[{"provider":"cline","windows":[{"name":"5 hours","used":42.5}]}]}`)
	})
	restore := quotasHTTPClient
	quotasHTTPClient = readhttp.NewClient(srv.Client())
	t.Cleanup(func() { quotasHTTPClient = restore })

	windows, err := ReadQuotas(context.Background(), srv.URL+"/v1", "gw-key-recover")
	if err != nil {
		t.Fatalf("ReadQuotas: %v", err)
	}
	if len(windows) != 1 || windows[0].ID != "cline/5 hours" || windows[0].UsedPercent != 42.5 {
		t.Fatalf("窗口 = %+v", windows)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("请求数 = %d，想要 2（首条中断 + 一次重试）", got)
	}
	if got := conns.Load(); got != 2 {
		t.Fatalf("底层连接数 = %d，想要 2（换池后新连接）", got)
	}
}

func TestRefreshQuotas(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider string
		want     string
	}{
		{"指定 provider", "claude", "claude"},
		{"provider 做 URL 转义", "my provider/账号", "my provider/账号"},
		{"provider 为空时不带参数", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath, gotQuery, gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				gotQuery = r.URL.Query().Get("provider")
				gotAuth = r.Header.Get("Authorization")
				io.WriteString(w, `{"object":"list","data":[{"provider":"claude","windows":[]}]}`)
			}))
			defer srv.Close()

			if err := RefreshQuotas(context.Background(), srv.URL+"/v1", "gw-key", tc.provider); err != nil {
				t.Fatalf("RefreshQuotas: %v", err)
			}
			if gotMethod != http.MethodPost {
				t.Errorf("方法 = %q，想要 POST", gotMethod)
			}
			if gotPath != "/v1/magpie/quotas/refresh" {
				t.Errorf("请求路径 = %q，想要 /v1/magpie/quotas/refresh", gotPath)
			}
			if gotQuery != tc.want {
				t.Errorf("provider 参数 = %q，想要 %q", gotQuery, tc.want)
			}
			if gotAuth != "Bearer gw-key" {
				t.Errorf("Authorization = %q，想要 Bearer gw-key", gotAuth)
			}
		})
	}
}

// TestRefreshQuotasFor 校验按账号刷新时的 query 拼装：provider 与 user 都做 URL 转义，
// user 为空时只带 provider，provider 为空时 user 也不发。
func TestRefreshQuotasFor(t *testing.T) {
	for _, tc := range []struct {
		name         string
		provider     string
		user         string
		wantQuery    string
		wantProvider string
		wantUser     string
	}{
		{
			name: "provider 与 user", provider: "claude", user: "a b@example.com",
			wantQuery: "provider=claude&user=a+b%40example.com", wantProvider: "claude", wantUser: "a b@example.com",
		},
		{
			name: "provider 与 user 做 URL 转义", provider: "my provider/账号", user: "u&x=y",
			wantQuery:    "provider=my+provider%2F%E8%B4%A6%E5%8F%B7&user=u%26x%3Dy",
			wantProvider: "my provider/账号", wantUser: "u&x=y",
		},
		{
			name: "user 为空时省略", provider: "claude",
			wantQuery: "provider=claude", wantProvider: "claude",
		},
		{
			name: "provider 为空时 user 也不发", user: "a@example.com",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath, gotQuery, gotProvider, gotUser string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				gotQuery = r.URL.RawQuery
				gotProvider = r.URL.Query().Get("provider")
				gotUser = r.URL.Query().Get("user")
				io.WriteString(w, `{"object":"list","data":[]}`)
			}))
			defer srv.Close()

			if err := RefreshQuotasFor(context.Background(), srv.URL+"/v1", "gw-key", tc.provider, tc.user); err != nil {
				t.Fatalf("RefreshQuotasFor: %v", err)
			}
			if gotMethod != http.MethodPost {
				t.Errorf("方法 = %q，想要 POST", gotMethod)
			}
			if gotPath != "/v1/magpie/quotas/refresh" {
				t.Errorf("请求路径 = %q，想要 /v1/magpie/quotas/refresh", gotPath)
			}
			if gotQuery != tc.wantQuery {
				t.Errorf("query = %q，想要 %q", gotQuery, tc.wantQuery)
			}
			if gotProvider != tc.wantProvider {
				t.Errorf("provider 参数 = %q，想要 %q", gotProvider, tc.wantProvider)
			}
			if gotUser != tc.wantUser {
				t.Errorf("user 参数 = %q，想要 %q", gotUser, tc.wantUser)
			}
		})
	}
}

func TestRefreshQuotasError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "vendor exploded", http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := RefreshQuotas(context.Background(), srv.URL+"/v1", "", "claude")
	if err == nil {
		t.Fatal("RefreshQuotas 没有返回错误")
	}
	for _, want := range []string{"500", "vendor exploded"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误 = %q，想要包含 %q", err, want)
		}
	}
}
