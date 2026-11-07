package app

import (
	"testing"
)

// 测试 swarmThreadContextFrom 在有效 JSON 包含 fast_mode 指针时的解析与转换。
func TestSwarmThreadContextFrom_ValidJSON(t *testing.T) {
	tiersJSON := `{"planner": {"provider": "anthropic", "model": "claude-3-7-sonnet", "effort": "high", "fast_mode": true}, "coder": {"provider": "openai", "model": "gpt-4o", "effort": "low", "fast_mode": false}}`
	msg := "请帮我实现该功能"
	pairID := "pair-1"
	enabledFn := func(p string) bool {
		return p == "anthropic"
	}

	tc := swarmThreadContextFrom(msg, pairID, tiersJSON, "openai", "gpt-5", "xhigh", enabledFn)

	if tc.UserMessage != msg {
		t.Fatalf("expected UserMessage %q, got %q", msg, tc.UserMessage)
	}
	if tc.PairID != pairID {
		t.Fatalf("expected PairID %q, got %q", pairID, tc.PairID)
	}
	if len(tc.BeeTiers) != 2 {
		t.Fatalf("expected 2 bee tiers, got %d", len(tc.BeeTiers))
	}

	planner, ok := tc.BeeTiers["planner"]
	if !ok {
		t.Fatalf("missing planner tier override")
	}
	if planner.Provider != "anthropic" || planner.Model != "claude-3-7-sonnet" || planner.Effort != "high" {
		t.Fatalf("unexpected planner override: %+v", planner)
	}
	if planner.FastMode == nil || *planner.FastMode != true {
		t.Fatalf("expected planner FastMode to be true, got %v", planner.FastMode)
	}

	coder, ok := tc.BeeTiers["coder"]
	if !ok {
		t.Fatalf("missing coder tier override")
	}
	if coder.Provider != "openai" || coder.Model != "gpt-4o" || coder.Effort != "low" {
		t.Fatalf("unexpected coder override: %+v", coder)
	}
	if coder.FastMode == nil || *coder.FastMode != false {
		t.Fatalf("expected coder FastMode to be false, got %v", coder.FastMode)
	}

	if tc.BeeProvider != "openai" || tc.BeeModel != "gpt-5" {
		t.Fatalf("unexpected BeeProvider/BeeModel: %s/%s", tc.BeeProvider, tc.BeeModel)
	}
	if tc.BeeEffort != "xhigh" {
		t.Fatalf("unexpected BeeEffort: %s", tc.BeeEffort)
	}

	if tc.ProviderEnabled == nil || !tc.ProviderEnabled("anthropic") || tc.ProviderEnabled("openai") {
		t.Fatalf("unexpected ProviderEnabled behavior")
	}
}

// 测试 swarmThreadContextFrom 在空字符串输入时返回 nil BeeTiers。
func TestSwarmThreadContextFrom_EmptyString(t *testing.T) {
	tc := swarmThreadContextFrom("test message", "pair-2", "", "", "", "", func(string) bool { return true })
	if tc.UserMessage != "test message" || tc.PairID != "pair-2" {
		t.Fatalf("unexpected UserMessage or PairID: %+v", tc)
	}
	if tc.BeeTiers != nil {
		t.Fatalf("expected nil BeeTiers for empty string, got: %+v", tc.BeeTiers)
	}
}

// 测试 swarmThreadContextFrom 在非法 JSON 输入时容错记录日志并返回 nil BeeTiers。
func TestSwarmThreadContextFrom_InvalidJSON(t *testing.T) {
	tc := swarmThreadContextFrom("test message", "pair-3", "{invalid-json", "", "", "", func(string) bool { return true })
	if tc.UserMessage != "test message" || tc.PairID != "pair-3" {
		t.Fatalf("unexpected UserMessage or PairID: %+v", tc)
	}
	if tc.BeeTiers != nil {
		t.Fatalf("expected nil BeeTiers for invalid json, got: %+v", tc.BeeTiers)
	}
}
