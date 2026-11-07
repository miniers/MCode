package remote

import (
	"testing"

	"mcode/internal/config"
)

// 测试 SetSwarmConfigFunc 与 swarmConfig 的 Getter/Setter 行为，
// 验证设置包含 BeeTiers 的配对后，swarmConfig 能正确返回该配置；未设置或重置为 nil 时返回零值。
func TestMemoryBackend_SwarmConfigGetterSetter(t *testing.T) {
	backend := &MemoryBackend{}

	// 未设置时返回零值配置
	zeroCfg := backend.swarmConfig()
	if len(zeroCfg.Pairs) != 0 {
		t.Fatalf("expected empty pairs in unset swarmConfig, got %d", len(zeroCfg.Pairs))
	}

	fastMode := true
	testCfg := config.OrchestrationConfig{
		Pairs: []config.SwarmPair{
			{
				ID:             "pair-test",
				Name:           "测试配对",
				LeadProvider:   "claude",
				LeadModel:      "claude-3-7-sonnet",
				WorkerProvider: "codex",
				WorkerModel:    "o3-mini",
				BeeTiers: map[string]config.SwarmTierOverride{
					"tier-1": {
						Provider: "claude",
						Model:    "claude-3-7-sonnet",
						Effort:   "medium",
						FastMode: &fastMode,
					},
				},
			},
		},
	}

	backend.SetSwarmConfigFunc(func() config.OrchestrationConfig {
		return testCfg
	})

	got := backend.swarmConfig()
	if len(got.Pairs) != 1 {
		t.Fatalf("expected 1 pair, got %d", len(got.Pairs))
	}
	pair := got.Pairs[0]
	if pair.ID != "pair-test" {
		t.Fatalf("expected pair ID pair-test, got %s", pair.ID)
	}
	if len(pair.BeeTiers) != 1 {
		t.Fatalf("expected 1 bee tier, got %d", len(pair.BeeTiers))
	}
	tier, ok := pair.BeeTiers["tier-1"]
	if !ok {
		t.Fatalf("expected tier-1 in BeeTiers")
	}
	if tier.Provider != "claude" || tier.Model != "claude-3-7-sonnet" || tier.Effort != "medium" {
		t.Fatalf("unexpected tier values: %+v", tier)
	}
	if tier.FastMode == nil || *tier.FastMode != true {
		t.Fatalf("expected FastMode true, got %v", tier.FastMode)
	}

	// 测试设置为 nil 时重置为零值
	backend.SetSwarmConfigFunc(nil)
	clearedCfg := backend.swarmConfig()
	if len(clearedCfg.Pairs) != 0 {
		t.Fatalf("expected empty pairs after SetSwarmConfigFunc(nil), got %d", len(clearedCfg.Pairs))
	}
}
