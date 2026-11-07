package quota

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func init() {
	// 测试中调小退避与重试时间，加速执行
	backoffBaseInterval = 1 * time.Millisecond
	tokenRetryInterval = 1 * time.Millisecond
}

func TestQuota_LoadCodeAssistAndModelsAndFusion(t *testing.T) {
	var modelsCallCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			res := map[string]any{
				"cloudaicompanionProject": "test-project-123",
				"paidTier": map[string]any{
					"name": "Standard Paid",
				},
			}
			_ = json.NewEncoder(w).Encode(res)

		case "/v1internal:fetchAvailableModels":
			atomic.AddInt32(&modelsCallCount, 1)
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)

			// 首次如果带 project，模拟 403
			if body["project"] != nil && atomic.LoadInt32(&modelsCallCount) == 1 {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"message":"Permission denied with project"}}`))
				return
			}

			// 重试（无 project）返回各模型额度
			res := map[string]any{
				"models": map[string]any{
					"gemini-1.5-flash": map[string]any{
						"quotaInfo": map[string]any{
							"remainingFraction": 0.8, // used = 20%
							"resetTime":         "2026-10-17T15:00:00Z",
						},
					},
					"gemini-1.5-pro": map[string]any{
						"quotaInfo": map[string]any{
							"remainingFraction": 0.3, // used = 70%
							"resetTime":         "2026-10-17T15:00:00Z",
						},
					},
					"claude-3-5-sonnet": map[string]any{
						"quotaInfo": map[string]any{
							"remainingFraction": 0.5, // used = 50%
							"resetTime":         "2026-10-17T16:00:00Z",
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(res)

		case "/v1internal:retrieveUserQuotaSummary":
			res := map[string]any{
				"groups": []map[string]any{
					{
						"displayName": "Gemini Models",
						"buckets": []map[string]any{
							{
								"window":            "5h",
								"remainingFraction": 0.6, // used = 40%
								"resetTime":         "2026-10-17T15:00:00Z",
							},
							{
								"window":            "7d",
								"remainingFraction": 0.75, // used = 25%
								"resetTime":         "2026-10-24T00:00:00Z",
							},
						},
					},
					// Claude and GPT models 故意缺失 5h bucket，测试用组内模型最大 UsedPercent 作为分组值
				},
			}
			_ = json.NewEncoder(w).Encode(res)

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &Client{
		HTTPClient: server.Client(),
		Hosts:      []string{server.URL},
	}

	res, err := client.Fetch(context.Background(), "mock-token", "")
	if err != nil {
		t.Fatalf("unexpected fetch error: %v", err)
	}

	if res.ProjectID != "test-project-123" {
		t.Errorf("expected projectID test-project-123, got %s", res.ProjectID)
	}
	if res.Tier != "Standard Paid" {
		t.Errorf("expected tier 'Standard Paid', got %s", res.Tier)
	}

	// 检查 403 去 project 重试是否发生
	if atomic.LoadInt32(&modelsCallCount) < 2 {
		t.Errorf("expected fetchAvailableModels to be retried on 403, got %d calls", modelsCallCount)
	}

	// 检查 Gemini Models 5h
	gemini5h, ok := res.Groups[GroupGemini]
	if !ok {
		t.Fatalf("missing group %s in result", GroupGemini)
	}
	if math.Abs(gemini5h.UsedPercent-40.0) > 1e-9 {
		t.Errorf("expected gemini 5h used 40%%, got %f", gemini5h.UsedPercent)
	}

	// 检查 Gemini Models 7d
	gemini7d, ok := res.Groups[GroupGemini+" 7d"]
	if !ok {
		t.Fatalf("missing group %s 7d in result", GroupGemini)
	}
	if math.Abs(gemini7d.UsedPercent-25.0) > 1e-9 {
		t.Errorf("expected gemini 7d used 25%%, got %f", gemini7d.UsedPercent)
	}

	// 检查模型融合：
	// gemini-1.5-flash 自身 used 20%，与组 5h (40%) 融合，取较大值 -> 40%
	flash := res.Groups["gemini-1.5-flash"]
	if math.Abs(flash.UsedPercent-40.0) > 1e-9 {
		t.Errorf("expected gemini-1.5-flash used 40%%, got %f", flash.UsedPercent)
	}

	// gemini-1.5-pro 自身 used 70%，与组 5h (40%) 融合，取较大值 -> 70%
	pro := res.Groups["gemini-1.5-pro"]
	if math.Abs(pro.UsedPercent-70.0) > 1e-9 {
		t.Errorf("expected gemini-1.5-pro used 70%%, got %f", pro.UsedPercent)
	}

	// 检查缺失 5h 分组兜底：
	// Claude and GPT models 缺失 5h，取 claude-3-5-sonnet 的 50% 作为组值
	claudeGroup, ok := res.Groups[GroupClaudeGPT]
	if !ok {
		t.Fatalf("missing group %s in result", GroupClaudeGPT)
	}
	if math.Abs(claudeGroup.UsedPercent-50.0) > 1e-9 {
		t.Errorf("expected %s used 50%%, got %f", GroupClaudeGPT, claudeGroup.UsedPercent)
	}
}

func TestQuota_LoadCodeAssistTierFallback(t *testing.T) {
	// 测试 paidTier 为空，且 ineligibleTiers 有限制时的 Restricted 兜底
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			res := map[string]any{
				"ineligibleTiers": []map[string]any{{"reasonCode": "NOT_ELIGIBLE"}},
				"allowedTiers": []map[string]any{
					{"id": "free_tier", "name": "Free Tier", "isDefault": true},
				},
			}
			_ = json.NewEncoder(w).Encode(res)
		case "/v1internal:fetchAvailableModels":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": map[string]any{}})
		case "/v1internal:retrieveUserQuotaSummary":
			_ = json.NewEncoder(w).Encode(map[string]any{"groups": []any{}})
		}
	}))
	defer server.Close()

	client := &Client{
		HTTPClient: server.Client(),
		Hosts:      []string{server.URL},
	}
	res, err := client.Fetch(context.Background(), "mock-token", "")
	if err != nil {
		t.Fatalf("unexpected fetch error: %v", err)
	}
	if res.Tier != "Free Tier (Restricted)" {
		t.Errorf("expected 'Free Tier (Restricted)', got %s", res.Tier)
	}
}

func TestQuota_HostFailover(t *testing.T) {
	// 第 1 个主机总是返回 500
	badServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer badServer.Close()

	// 第 2 个主机正常返回
	goodServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_ = json.NewEncoder(w).Encode(map[string]any{"project": "proj-ok", "paidTier": map[string]any{"name": "OK Tier"}})
		case "/v1internal:fetchAvailableModels":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": map[string]any{}})
		case "/v1internal:retrieveUserQuotaSummary":
			_ = json.NewEncoder(w).Encode(map[string]any{"groups": []any{}})
		}
	}))
	defer goodServer.Close()

	client := &Client{
		HTTPClient: goodServer.Client(),
		Hosts:      []string{badServer.URL, goodServer.URL},
	}

	res, err := client.Fetch(context.Background(), "mock-token", "")
	if err != nil {
		t.Fatalf("expected failover to goodServer, got error: %v", err)
	}
	if res.ProjectID != "proj-ok" {
		t.Errorf("expected proj-ok, got %s", res.ProjectID)
	}
}

func TestQuota_ErrorMappings(t *testing.T) {
	// 401 映射 ErrUnauthorized
	server401 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}))
	defer server401.Close()

	client401 := &Client{HTTPClient: server401.Client(), Hosts: []string{server401.URL}}
	_, err := client401.Fetch(context.Background(), "token", "")
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}

	// 403 USER_INELIGIBLE 映射 ErrForbidden
	server403 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"USER_INELIGIBLE"}}`))
	}))
	defer server403.Close()

	client403 := &Client{HTTPClient: server403.Client(), Hosts: []string{server403.URL}}
	_, err = client403.Fetch(context.Background(), "token", "")
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
	var fErr *ForbiddenError
	if !errors.As(err, &fErr) {
		t.Fatalf("expected ForbiddenError type, got %T", err)
	}
	if fErr.Reason != "用户不符合使用条件 (USER_INELIGIBLE)" {
		t.Errorf("unexpected forbidden reason: %s", fErr.Reason)
	}
}

func TestQuota_RefreshAccessToken(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		call := atomic.AddInt32(&attempts, 1)
		if call == 1 {
			// 模拟第一次 invalid_grant 触发重试
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}

		res := map[string]any{
			"access_token": "new-access-token",
			"expires_in":   3600,
		}
		_ = json.NewEncoder(w).Encode(res)
	}))
	defer server.Close()

	token, exp, err := RefreshAccessToken(context.Background(), server.Client(), server.URL, "cid", "csec", "rtok")
	if err != nil {
		t.Fatalf("unexpected refresh error: %v", err)
	}
	if token != "new-access-token" {
		t.Errorf("expected new-access-token, got %s", token)
	}
	if exp.Before(time.Now()) {
		t.Errorf("expected future expiry, got %v", exp)
	}
	if atomic.LoadInt32(&attempts) != 2 {
		t.Errorf("expected 2 attempts with retry, got %d", attempts)
	}
}
