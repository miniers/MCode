package swarm

import (
	"reflect"
	"strings"
	"testing"
)

func TestFindTier(t *testing.T) {
	tiers := []Tier{
		{ID: "fast", Name: "Fast Tier", Provider: "gemini", Model: "flash"},
		{ID: "deep", Name: "Deep Work", Provider: "claude", Model: "opus"},
		{ID: "read", Name: "Read Only", ReadOnly: true},
	}

	tests := []struct {
		name    string
		ref     string
		wantID  string
		wantHit bool
	}{
		{name: "精确匹配 ID", ref: "fast", wantID: "fast", wantHit: true},
		{name: "大小写不敏感匹配 ID", ref: "FAST", wantID: "fast", wantHit: true},
		{name: "前后空白被裁剪", ref: "  deep  ", wantID: "deep", wantHit: true},
		{name: "按名称匹配", ref: "Fast Tier", wantID: "fast", wantHit: true},
		{name: "按名称大小写不敏感匹配", ref: "deep work", wantID: "deep", wantHit: true},
		{name: "空引用返回 false", ref: "   ", wantHit: false},
		{name: "未命中返回 false", ref: "unknown", wantHit: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := FindTier(tiers, tt.ref)
			if ok != tt.wantHit {
				t.Fatalf("FindTier(%q) ok=%v, want %v", tt.ref, ok, tt.wantHit)
			}
			if tt.wantHit && got.ID != tt.wantID {
				t.Errorf("FindTier(%q) id=%q, want %q", tt.ref, got.ID, tt.wantID)
			}
		})
	}
}

func TestFindTier_IDTakesPrecedenceOverName(t *testing.T) {
	tiers := []Tier{
		{ID: "alpha", Name: "Second"},
		{ID: "beta", Name: "alpha"},
	}
	got, ok := FindTier(tiers, "alpha")
	if !ok || got.ID != "alpha" {
		t.Fatalf("expected ID match to win, got %+v", got)
	}
}

func TestMentionsInMessage_Boundaries(t *testing.T) {
	// e.g. gpt-5 must not match gpt-5.1, must match 用 gpt-5 做
	if MentionsInMessage("请使用 gpt-5.1 完成", "gpt-5") {
		t.Errorf("gpt-5 must not match gpt-5.1")
	}
	if MentionsInMessage("gpt-5.1", "gpt-5") {
		t.Errorf("gpt-5 must not match gpt-5.1 at start of message")
	}
	if !MentionsInMessage("用 gpt-5 做", "gpt-5") {
		t.Errorf("gpt-5 must match 用 gpt-5 做")
	}
	if !MentionsInMessage("gpt-5", "gpt-5") {
		t.Errorf("exact match should succeed")
	}
	if !MentionsInMessage("使用 gpt-5 进行开发", "gpt-5") {
		t.Errorf("sentence match should succeed")
	}
	if MentionsInMessage("my-gpt-5", "gpt-5") {
		t.Errorf("hyphen should not be a boundary")
	}
	if MentionsInMessage("gpt-5_turbo", "gpt-5") {
		t.Errorf("underscore should not be a boundary")
	}
	if MentionsInMessage("openai/gpt-5-mini", "gpt-5") {
		t.Errorf("slash boundary rule should prevent partial match")
	}
	if MentionsInMessage("some message", "gp") {
		t.Errorf("ref shorter than 3 chars must return false")
	}
	if MentionsInMessage("some message", "  ") {
		t.Errorf("empty ref must return false")
	}
	if !MentionsInMessage("Please try (gpt-5)!", "gpt-5") {
		t.Errorf("punctuation boundaries should match")
	}
}

func TestAuthorizeDirectModel(t *testing.T) {
	enabled := func(p string) bool {
		return p == "openai" || p == "anthropic"
	}

	// 1. provider disabled
	if AuthorizeDirectModel("用 gpt-4o 做", "google", "gpt-4o", enabled) {
		t.Errorf("disabled provider must return false")
	}

	// 2. provider enabled with direct mention
	if !AuthorizeDirectModel("用 gpt-4o 做", "openai", "gpt-4o", enabled) {
		t.Errorf("enabled provider with mentioned model should be authorized")
	}

	// 3. empty model
	if AuthorizeDirectModel("用 gpt-4o 做", "openai", "", enabled) {
		t.Errorf("empty model must return false")
	}

	// 4. unmentioned model
	if AuthorizeDirectModel("用 claude 做", "openai", "gpt-4o", enabled) {
		t.Errorf("unmentioned model must return false")
	}

	// 5. model with slash: mentions part after last slash
	if !AuthorizeDirectModel("请用 claude-3-7-sonnet 测试", "anthropic", "anthropic/claude-3-7-sonnet", enabled) {
		t.Errorf("model with slash should be authorized when part after slash is mentioned")
	}

	// 6. model with slash: mentions full model
	if !AuthorizeDirectModel("请用 anthropic/claude-3-7-sonnet 测试", "anthropic", "anthropic/claude-3-7-sonnet", enabled) {
		t.Errorf("model with slash should be authorized when full model is mentioned")
	}
}

func TestResolveBeeConfig_Tiers(t *testing.T) {
	fastModeTrue := true
	fastModeFalse := false

	tiers := []Tier{
		{ID: "fast", Name: "Fast", Provider: "gemini", Model: "flash", Effort: "low", FastMode: true, SystemPrompt: "fast-sys"},
		{ID: "read", Name: "Read", Provider: "gemini", Model: "flash", ReadOnly: true},
		{ID: "heavy", Name: "Heavy", Provider: "claude", Model: "opus", Effort: "high"},
	}
	pairs := []Pair{
		{
			ID:          "pair-main",
			Name:        "Main Pair",
			BeeProvider: "openai",
			BeeModel:    "gpt-4o",
			BeeTiers: map[string]TierOverride{
				"fast": {Provider: "google", Model: "gemini-2.5-flash", FastMode: &fastModeFalse},
			},
		},
	}

	// 1. default tier: empty d.Tier uses in.DefaultTier
	t.Run("default tier", func(t *testing.T) {
		cfg, notes := ResolveBeeConfig(ResolveInput{
			Delegation:  Delegation{Task: "t"},
			Tiers:       tiers,
			DefaultTier: "fast",
		})
		if cfg.Tier != "fast" || cfg.Provider != "gemini" || cfg.Model != "flash" || cfg.Effort != "low" || !cfg.FastMode || cfg.SystemPrompt != "fast-sys" {
			t.Errorf("default tier mismatch: %+v", cfg)
		}
		if len(notes) != 0 {
			t.Errorf("expected no notes, got %v", notes)
		}
	})

	// 2. unknown tier note: unknown tier produces note and applies default tier
	t.Run("unknown tier note", func(t *testing.T) {
		cfg, notes := ResolveBeeConfig(ResolveInput{
			Delegation:  Delegation{Tier: "unknown-tier"},
			Tiers:       tiers,
			DefaultTier: "fast",
		})
		if cfg.Tier != "fast" || cfg.Model != "flash" {
			t.Errorf("expected fallback to default tier fast, got %+v", cfg)
		}
		expectedNote := `未找到档位 "unknown-tier"，已按默认档位 "fast" 派发`
		if len(notes) != 1 || notes[0] != expectedNote {
			t.Errorf("expected note %q, got %v", expectedNote, notes)
		}
	})

	// 3. pair override: in.PairID has BeeTiers override for cfg.Tier
	t.Run("pair override", func(t *testing.T) {
		cfg, notes := ResolveBeeConfig(ResolveInput{
			Delegation:  Delegation{Tier: "fast"},
			Tiers:       tiers,
			Pairs:       pairs,
			PairID:      "pair-main",
			DefaultTier: "fast",
		})
		if cfg.Provider != "google" || cfg.Model != "gemini-2.5-flash" || cfg.FastMode != false {
			t.Errorf("expected pair override google/gemini-2.5-flash fastMode=false, got %s/%s fastMode=%v", cfg.Provider, cfg.Model, cfg.FastMode)
		}
		if len(notes) != 0 {
			t.Errorf("expected no notes, got %v", notes)
		}
	})

	// 4. thread override beating pair override
	t.Run("thread override beating pair override", func(t *testing.T) {
		cfg, notes := ResolveBeeConfig(ResolveInput{
			Delegation:  Delegation{Tier: "fast"},
			Tiers:       tiers,
			Pairs:       pairs,
			PairID:      "pair-main",
			DefaultTier: "fast",
			ThreadOverrides: map[string]TierOverride{
				"fast": {Provider: "anthropic", Model: "claude-3-5-haiku", FastMode: &fastModeTrue},
			},
		})
		if cfg.Provider != "anthropic" || cfg.Model != "claude-3-5-haiku" || !cfg.FastMode {
			t.Errorf("thread override should beat pair override, got %s/%s fastMode=%v", cfg.Provider, cfg.Model, cfg.FastMode)
		}
		if len(notes) != 0 {
			t.Errorf("expected no notes, got %v", notes)
		}
	})

	// 5. unauthorized model ignored with note
	t.Run("unauthorized model ignored with note", func(t *testing.T) {
		cfg, notes := ResolveBeeConfig(ResolveInput{
			Delegation:  Delegation{Tier: "fast", Provider: "openai", Model: "gpt-4o"},
			Tiers:       tiers,
			DefaultTier: "fast",
			UserMessage: "please do this task",
		})
		if cfg.DirectModel {
			t.Errorf("DirectModel should be false when model is unauthorized")
		}
		if cfg.Model != "flash" {
			t.Errorf("model should remain flash from tier, got %s", cfg.Model)
		}
		expectedNote := `用户本条消息未授权指定模型 "gpt-4o"，已按档位 "fast" 派发`
		if len(notes) != 1 || notes[0] != expectedNote {
			t.Errorf("expected note %q, got %v", expectedNote, notes)
		}
	})

	// 6. authorized model sets DirectModel
	t.Run("authorized model sets DirectModel", func(t *testing.T) {
		cfg, notes := ResolveBeeConfig(ResolveInput{
			Delegation:  Delegation{Tier: "fast", Provider: "openai", Model: "gpt-4o"},
			Tiers:       tiers,
			DefaultTier: "fast",
			UserMessage: "please use gpt-4o for this",
		})
		if !cfg.DirectModel {
			t.Errorf("DirectModel should be true when model is authorized")
		}
		if cfg.Provider != "openai" || cfg.Model != "gpt-4o" {
			t.Errorf("expected openai/gpt-4o, got %s/%s", cfg.Provider, cfg.Model)
		}
		if len(notes) != 0 {
			t.Errorf("expected no notes, got %v", notes)
		}
	})

	// 7. ReadOnly kept when direct model applied
	t.Run("ReadOnly kept when direct model applied", func(t *testing.T) {
		cfg, notes := ResolveBeeConfig(ResolveInput{
			Delegation:  Delegation{Tier: "read", Provider: "openai", Model: "gpt-4o"},
			Tiers:       tiers,
			DefaultTier: "fast",
			UserMessage: "please use gpt-4o for inspection",
		})
		if !cfg.ReadOnly {
			t.Errorf("ReadOnly should remain true")
		}
		if !cfg.DirectModel {
			t.Errorf("DirectModel should be true")
		}
		if cfg.Provider != "openai" || cfg.Model != "gpt-4o" {
			t.Errorf("expected openai/gpt-4o, got %s/%s", cfg.Provider, cfg.Model)
		}
		if len(notes) != 0 {
			t.Errorf("expected no notes, got %v", notes)
		}
	})
}

func TestTierPromptSection(t *testing.T) {
	if TierPromptSection(nil, "fast", nil) != "" {
		t.Errorf("expected empty string for empty tiers")
	}

	tiers := []Tier{
		{ID: "fast", Description: "Quick responses"},
		{ID: "read", Description: "Read only tasks", ReadOnly: true},
	}
	got := TierPromptSection(tiers, "fast", nil)
	if !reflect.ValueOf(got).IsValid() || got == "" {
		t.Fatalf("expected non-empty prompt section")
	}
	for _, want := range []string{
		"Pick it by what the brief already settles",
		"never goes to that tier",
		"never raise it",
		"path:line (never a bare filename, never an absolute path, never a line range)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt section missing %q:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "default tier fast"); n != 2 {
		t.Errorf("expected default tier twice in prompt section, got %d:\n%s", n, got)
	}
	if strings.Contains(got, "chosen by the task's difficulty") {
		t.Errorf("prompt section still mentions task difficulty selection:\n%s", got)
	}

	// resolved 为 nil 时档位行保持原样，不出现运行模型后缀。
	// 后缀以 " [runs on" 开头；仅匹配 "runs on" 会被规则原文里的
	// "an entry without \"tier\" runs on the default tier" 误伤。
	if strings.Contains(got, " [runs on") {
		t.Errorf("nil resolved should not append runtime suffix:\n%s", got)
	}
	if !strings.Contains(got, "- fast: Quick responses\n") || !strings.Contains(got, "- read (read-only): Read only tasks\n") {
		t.Errorf("nil resolved changed tier lines:\n%s", got)
	}

	// resolved 缺少某个档位时，只有该档位行保持原样。
	partial := TierPromptSection(tiers, "fast", map[string]ResolvedTier{
		"fast": {Provider: "pi", Model: "model-x", Effort: "high"},
	})
	if !strings.Contains(partial, "- fast: Quick responses [runs on pi/model-x, effort high]\n") {
		t.Errorf("missing effort suffix in tier line:\n%s", partial)
	}
	if !strings.Contains(partial, "- read (read-only): Read only tasks\n") {
		t.Errorf("tier missing from resolved should keep its line:\n%s", partial)
	}

	// 有 effort 写 effort；无 effort 只写 provider/model。
	suffixed := TierPromptSection(tiers, "fast", map[string]ResolvedTier{
		"fast": {Provider: "pi", Model: "model-x", Effort: "high"},
		"read": {Provider: "openai", Model: "gpt-4o-mini"},
	})
	for _, want := range []string{
		"- fast: Quick responses [runs on pi/model-x, effort high]\n",
		"- read (read-only): Read only tasks [runs on openai/gpt-4o-mini]\n",
	} {
		if !strings.Contains(suffixed, want) {
			t.Errorf("resolved tier line missing %q:\n%s", want, suffixed)
		}
	}
	if n := strings.Count(suffixed, "effort"); n != 1 {
		t.Errorf("expected effort only on the tier that resolved one, got %d:\n%s", n, suffixed)
	}
}

// 档位带写入白名单时提示词追加 writes only 标注与配套规则句；
// 没有这类档位时输出与改动前完全一致。
func TestTierPromptSection_WritePaths(t *testing.T) {
	plain := []Tier{
		{ID: "fast", Description: "Quick responses"},
		{ID: "read", Description: "Read only tasks", ReadOnly: true},
	}
	const rule = "A tier marked writes only cannot land code: send it to locate the cause and design the change, have it write the plan into its allowed paths when the plan is long, and report numbered steps with path:line anchors; then dispatch the implementation to the default tier in the next round."
	if got := TierPromptSection(plain, "fast", nil); strings.Contains(got, "writes only") || strings.Contains(got, rule) {
		t.Errorf("无白名单档位时不应出现 writes only 标注或规则句:\n%s", got)
	} else if !strings.Contains(got, "keeps its tier; never raise it; ") {
		t.Errorf("无白名单档位时应保留原有 keeps its tier; never raise it; 文本:\n%s", got)
	}

	guarded := []Tier{
		{ID: "fast", Description: "Quick responses"},
		{ID: "plan", Description: "Design the change", WritePaths: []string{"docs/plans/*.md", "docs/notes/*.md"}},
	}
	got := TierPromptSection(guarded, "fast", nil)
	if !strings.Contains(got, "- plan (writes only docs/plans/*.md, docs/notes/*.md): Design the change\n") {
		t.Errorf("档位行缺少 writes only 标注:\n%s", got)
	}
	if !strings.Contains(got, rule) {
		t.Errorf("缺少 writes only 规则句:\n%s", got)
	}
	if !strings.Contains(got, "Re-sending a failed or stopped task keeps its tier. "+tierWriteOnlyRule+" Never raise it; ") {
		t.Errorf("规则句插入后原有句子被改写或丢失:\n%s", got)
	}
}

// MatchWritePaths 按 glob 判断仓库相对路径是否在白名单内：空白名单不限制，
// ** 匹配零个或多个路径段，其他段按单段通配符匹配。
func TestMatchWritePaths(t *testing.T) {
	patterns := []string{"docs/superpowers/plans/**/*.md"}

	tests := []struct {
		name     string
		patterns []string
		path     string
		want     bool
	}{
		{name: "空白名单不限制", patterns: nil, path: "internal/app/main.go", want: true},
		{name: "空的白名单切片不限制", patterns: []string{}, path: "main.go", want: true},
		{name: "直接位于 plans 下的 md 命中", patterns: patterns, path: "docs/superpowers/plans/a.md", want: true},
		{name: "深层目录的 md 命中", patterns: patterns, path: "docs/superpowers/plans/x/y/b.md", want: true},
		{name: "** 匹配零个路径段", patterns: patterns, path: "docs/superpowers/plans/c.md", want: true},
		{name: "同目录的 go 文件不命中", patterns: patterns, path: "docs/superpowers/plans/a.go", want: false},
		{name: "白名单外的路径不命中", patterns: patterns, path: "internal/x.go", want: false},
		{name: "多个 pattern 任一命中", patterns: []string{"docs/**/*.md", "internal/*.go"}, path: "internal/x.go", want: true},
		{name: "单段通配符不跨目录", patterns: []string{"internal/*.go"}, path: "internal/app/x.go", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchWritePaths(tt.patterns, tt.path); got != tt.want {
				t.Fatalf("MatchWritePaths(%v, %q) = %v, want %v", tt.patterns, tt.path, got, tt.want)
			}
		})
	}
}
