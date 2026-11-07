package config

import "testing"

func mustMigrateV8ToV9(t *testing.T, raw map[string]any) map[string]any {
	t.Helper()
	out, err := migrateV8ToV9(raw)
	if err != nil {
		t.Fatalf("migrateV8ToV9 failed: %v", err)
	}
	return out
}

// 1. 旧开关为 true 时迁成 human，旧键删除。
func TestMigrateV8ToV9ReviewTrueBecomesHuman(t *testing.T) {
	out := mustMigrateV8ToV9(t, map[string]any{
		"version":       8,
		"orchestration": map[string]any{"review_before_merge": true},
	})

	wantRaw(t, out, "human", "orchestration", "review_mode")
	if _, exists := lookupRaw(t, out, "orchestration", "review_before_merge"); exists {
		t.Fatalf("旧键 orchestration.review_before_merge 应被删除")
	}
}

// 2. 旧开关为 false 时迁成 off。
func TestMigrateV8ToV9ReviewFalseBecomesOff(t *testing.T) {
	out := mustMigrateV8ToV9(t, map[string]any{
		"version":       8,
		"orchestration": map[string]any{"review_before_merge": false},
	})

	wantRaw(t, out, "off", "orchestration", "review_mode")
	if _, exists := lookupRaw(t, out, "orchestration", "review_before_merge"); exists {
		t.Fatalf("旧键 orchestration.review_before_merge 应被删除")
	}
}

// 3. 新键已存在时以新键为准，旧键仍被删除。
func TestMigrateV8ToV9KeepsExistingReviewMode(t *testing.T) {
	out := mustMigrateV8ToV9(t, map[string]any{
		"version": 8,
		"orchestration": map[string]any{
			"review_before_merge": true,
			"review_mode":         "ai",
		},
	})

	wantRaw(t, out, "ai", "orchestration", "review_mode")
	if _, exists := lookupRaw(t, out, "orchestration", "review_before_merge"); exists {
		t.Fatalf("旧键 orchestration.review_before_merge 应被删除")
	}
}

// 4. 没有 orchestration 段时什么都不写，不凭空造出只含 review_mode 的空段。
func TestMigrateV8ToV9SkipsMissingOrchestrationSection(t *testing.T) {
	out := mustMigrateV8ToV9(t, map[string]any{"version": 8})

	if _, exists := out["orchestration"]; exists {
		t.Fatalf("没有旧键时不应新增 orchestration 段: %+v", out["orchestration"])
	}
}

// 5. ReviewModeValue 归一化：空串与未知值按 off，ai/human 原样返回，大小写不敏感。
func TestOrchestrationReviewModeValue(t *testing.T) {
	cases := map[string]string{
		"":      ReviewModeOff,
		"x":     ReviewModeOff,
		"AI":    ReviewModeAI,
		"human": ReviewModeHuman,
	}
	for input, want := range cases {
		got := OrchestrationConfig{ReviewMode: input}.ReviewModeValue()
		if got != want {
			t.Fatalf("ReviewModeValue(%q) = %q, want %q", input, got, want)
		}
	}
}
