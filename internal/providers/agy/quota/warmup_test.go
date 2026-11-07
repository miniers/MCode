package quota

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFilterGeminiTextCandidates(t *testing.T) {
	input := map[string]rawModelInfo{
		"gemini-2.5-flash": {
			DisplayName: "Gemini 2.5 Flash",
		},
		"gemini-2.5-pro": {
			DisplayName: "Gemini 2.5 Pro",
		},
		"claude-3-7-sonnet": {
			DisplayName: "Claude 3.7 Sonnet",
		},
		"gpt-4o": {
			DisplayName: "GPT-4o",
		},
		"imagen-3.0-generate-002": {
			DisplayName: "Imagen 3",
		},
		"gemini-live-audio": {
			DisplayName: "Gemini Live Audio",
		},
		"text-embedding-004": {
			DisplayName: "Text Embedding 004",
		},
		"gemini-embedding-exp": {
			DisplayName: "Gemini Embedding",
		},
		"deepseek-r1": {
			DisplayName: "DeepSeek R1",
		},
	}

	candidates := FilterGeminiTextCandidates(input)
	candMap := make(map[string]bool)
	for _, c := range candidates {
		candMap[c.ID] = true
	}

	if !candMap["gemini-2.5-flash"] || !candMap["gemini-2.5-pro"] {
		t.Fatalf("expected gemini text models to be included, got %+v", candMap)
	}
	if candMap["claude-3-7-sonnet"] || candMap["gpt-4o"] || candMap["deepseek-r1"] {
		t.Fatalf("third party models should be excluded: %+v", candMap)
	}
	if candMap["imagen-3.0-generate-002"] || candMap["gemini-live-audio"] || candMap["text-embedding-004"] || candMap["gemini-embedding-exp"] {
		t.Fatalf("non-text or embedding models should be excluded: %+v", candMap)
	}
}

func TestRankGeminiCandidates(t *testing.T) {
	candidates := []ModelCandidate{
		{ID: "gemini-heavy-custom", DisplayName: "Gemini Heavy"},
		{ID: "gemini-lite-custom", DisplayName: "Gemini Lite"},
		{ID: "gemini-unknown-a", DisplayName: "Gemini Unknown A"},
	}

	// 1. 测试 flash/lite 启发式优先于非 lite 模型（均无 models.dev 价格时）
	ranked := RankGeminiCandidates(candidates, "")
	if len(ranked) != 3 {
		t.Fatalf("unexpected count: %d", len(ranked))
	}
	if ranked[0].ID != "gemini-lite-custom" {
		t.Fatalf("expected lite model first by heuristic, got %s", ranked[0].ID)
	}

	// 2. 测试上次成功模型优先
	rankedWithLast := RankGeminiCandidates(candidates, "gemini-heavy-custom")
	if rankedWithLast[0].ID != "gemini-heavy-custom" {
		t.Fatalf("expected last success model first, got %s", rankedWithLast[0].ID)
	}
}

func TestRankGeminiCandidates_PricingAndUnknown(t *testing.T) {
	// 验证已知价格排序且未知价格不被视作 0 成本
	candidates := []ModelCandidate{
		{ID: "gemini-unknown-price", DisplayName: "Gemini Unknown Price"},
		{ID: "gemini-2.5-pro", DisplayName: "Gemini 2.5 Pro"},
		{ID: "gemini-2.5-flash", DisplayName: "Gemini 2.5 Flash"},
	}

	ranked := RankGeminiCandidates(candidates, "")
	if len(ranked) != 3 {
		t.Fatalf("unexpected count: %d", len(ranked))
	}
	// 价格已知的模型（flash、pro）应优先于未知价格模型，未知价格不当零排在最前
	if ranked[len(ranked)-1].ID != "gemini-unknown-price" {
		t.Fatalf("expected unknown price model not to be treated as zero, got first=%s, last=%s", ranked[0].ID, ranked[len(ranked)-1].ID)
	}
}

func TestIsModelNotFoundError(t *testing.T) {
	cases := []struct {
		code     int
		body     string
		expected bool
	}{
		{404, "models/gemini-old is not found", true},
		{400, "The requested model 'gemini-xxx' does not exist", true},
		{400, "model is not supported for generateContent", true},
		{404, "Unknown model: gemini-future", true},
		{404, "project 'proj-123' not found", false},
		{404, "resource not found", false},
		{400, "invalid argument: field X is missing", false},
		{400, "unsupported generationConfig.temperature parameter", false},
		{400, "unsupported generation_config parameter", false},
		{500, "internal server error", false},
		{503, "model is not available at this moment", false},
		{429, "model rate limit reached, not available", false},
		{401, "unauthorized access to model", false},
		{403, "forbidden access to model", false},
	}

	for _, c := range cases {
		actual := IsModelNotFoundError(c.code, c.body)
		if actual != c.expected {
			t.Errorf("IsModelNotFoundError(%d, %q) = %v; want %v", c.code, c.body, actual, c.expected)
		}
	}
}

func TestWarmup_ExecutionAndSwitching(t *testing.T) {
	primaryCalled := 0
	backupCalled := 0
	fetchModelsCalled := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:generateContent":
			var payload struct {
				Model   string `json:"model"`
				Project string `json:"project"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)

			switch payload.Model {
			case "gemini-primary":
				primaryCalled++
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"message":"model gemini-primary not found"}}`))
			case "gemini-backup":
				backupCalled++
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"candidates":[]}`))
			default:
				http.Error(w, "unknown", http.StatusBadRequest)
			}
		case "/v1internal:fetchAvailableModels":
			fetchModelsCalled++
			// 刷新返回新候选 gemini-backup
			data := map[string]any{
				"models": map[string]any{
					"gemini-primary": map[string]any{
						"displayName": "Gemini Primary",
					},
					"gemini-backup": map[string]any{
						"displayName": "Gemini Backup",
					},
				},
			}
			_ = json.NewEncoder(w).Encode(data)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := Client{
		HTTPClient: srv.Client(),
		Hosts:      []string{srv.URL},
	}

	candidates := []ModelCandidate{
		{ID: "gemini-primary", DisplayName: "Gemini Primary"},
	}

	usedModel, err := client.Warmup(context.Background(), "mock-token", "", candidates, "gemini-primary")
	if err != nil {
		t.Fatalf("expected successful switch to backup, got err: %v", err)
	}
	if usedModel != "gemini-backup" {
		t.Fatalf("expected gemini-backup to be used, got %s", usedModel)
	}
	if primaryCalled != 1 || backupCalled != 1 || fetchModelsCalled != 1 {
		t.Fatalf("expected 1 call each, got primary=%d, backup=%d, fetchModels=%d", primaryCalled, backupCalled, fetchModelsCalled)
	}
}

func TestWarmup_NoSwitchOnNetworkOrRateLimit(t *testing.T) {
	primaryCalled := 0
	backupCalled := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)

		if payload.Model == "gemini-primary" {
			primaryCalled++
			http.Error(w, "resource exhausted", http.StatusTooManyRequests)
			return
		}
		backupCalled++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := Client{
		HTTPClient: srv.Client(),
		Hosts:      []string{srv.URL},
	}

	candidates := []ModelCandidate{
		{ID: "gemini-primary", DisplayName: "Gemini Primary"},
		{ID: "gemini-backup", DisplayName: "Gemini Backup"},
	}

	_, err := client.Warmup(context.Background(), "mock-token", "", candidates, "gemini-primary")
	if err == nil {
		t.Fatal("expected 429 error, got nil")
	}
	if primaryCalled != 1 {
		t.Fatalf("primary called %d times; want 1", primaryCalled)
	}
	if backupCalled != 0 {
		t.Fatalf("backup should never be called on 429 rate limit, got %d calls", backupCalled)
	}
}

func TestWarmup_ThirdPartyCandidateNotRequested(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := Client{
		HTTPClient: srv.Client(),
		Hosts:      []string{srv.URL},
	}

	// 传入任意第三方模型候选
	thirdParties := []ModelCandidate{
		{ID: "claude-3-7-sonnet", DisplayName: "Claude 3.7 Sonnet"},
		{ID: "gpt-4o", DisplayName: "GPT-4o"},
		{ID: "deepseek-r1", DisplayName: "DeepSeek R1"},
	}

	_, err := client.Warmup(context.Background(), "mock-token", "", thirdParties, "")
	if err == nil || !strings.Contains(err.Error(), "no available Gemini models") {
		t.Fatalf("expected no available Gemini models error, got: %v", err)
	}
	if requests != 0 {
		t.Fatalf("third party candidates must not generate any HTTP request, got %d", requests)
	}
}

func TestWarmup_EmptyCandidatesError(t *testing.T) {
	client := Client{}
	_, err := client.Warmup(context.Background(), "token", "", nil, "")
	if err == nil || !strings.Contains(err.Error(), "no available Gemini models") {
		t.Fatalf("expected no available Gemini models error, got: %v", err)
	}
}
