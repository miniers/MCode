package swarm

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadProfiles(t *testing.T) {
	dir := t.TempDir()

	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}

	good := write("good.json", `[
  {"name": "quick", "model": "flash", "effort": "low"},
  {"name": "", "model": "dropped"},
  {"name": "deep", "system_prompt": "be thorough", "allowed_tools": ["read"], "denied_tools": ["bash"]}
]`)
	bad := write("bad.json", `{ not an array`)
	empty := write("empty.json", `[]`)

	tests := []struct {
		name     string
		path     string
		wantNil  bool
		wantLen  int
		wantErr  bool
		validate func(t *testing.T, ps []BeeProfile)
	}{
		{name: "路径为空", path: "", wantNil: true},
		{name: "文件不存在", path: filepath.Join(dir, "missing.json"), wantNil: true},
		{name: "空数组", path: empty, wantLen: 0},
		{
			name:    "正常数组丢弃空名条目",
			path:    good,
			wantLen: 2,
			validate: func(t *testing.T, ps []BeeProfile) {
				if ps[0].Name != "quick" || ps[0].Model != "flash" || ps[0].Effort != "low" {
					t.Errorf("unexpected first profile: %+v", ps[0])
				}
				if ps[1].Name != "deep" || ps[1].SystemPrompt != "be thorough" {
					t.Errorf("unexpected second profile: %+v", ps[1])
				}
				if !reflect.DeepEqual(ps[1].AllowedTools, []string{"read"}) || !reflect.DeepEqual(ps[1].DeniedTools, []string{"bash"}) {
					t.Errorf("unexpected tools on second profile: %+v", ps[1])
				}
			},
		},
		{name: "坏 JSON", path: bad, wantErr: true, wantNil: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps, err := LoadProfiles(tt.path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil {
				if ps != nil {
					t.Fatalf("expected nil profiles, got %+v", ps)
				}
				return
			}
			if len(ps) != tt.wantLen {
				t.Fatalf("expected %d profiles, got %d (%+v)", tt.wantLen, len(ps), ps)
			}
			if tt.validate != nil {
				tt.validate(t, ps)
			}
		})
	}
}

func TestFindProfile(t *testing.T) {
	profiles := []BeeProfile{
		{Name: "Quick", Model: "flash"},
		{Name: "Deep Work", Model: "opus"},
	}
	tests := []struct {
		name    string
		ref     string
		want    string
		wantHit bool
	}{
		{name: "大小写不敏感", ref: "quick", want: "Quick", wantHit: true},
		{name: "带空白", ref: "  deep work  ", want: "Deep Work", wantHit: true},
		{name: "空引用", ref: "   ", wantHit: false},
		{name: "未知名", ref: "missing", wantHit: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := FindProfile(profiles, tt.ref)
			if ok != tt.wantHit {
				t.Fatalf("FindProfile(%q) ok=%v, want %v", tt.ref, ok, tt.wantHit)
			}
			if tt.wantHit && got.Name != tt.want {
				t.Errorf("FindProfile(%q) name=%q, want %q", tt.ref, got.Name, tt.want)
			}
			if !tt.wantHit && !reflect.DeepEqual(got, BeeProfile{}) {
				t.Errorf("FindProfile(%q) = %+v, want zero profile", tt.ref, got)
			}
		})
	}
}

func TestApplyProfilePrompt(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		sys    string
		want   string
	}{
		{name: "无系统提示词原样返回", prompt: "do it", sys: "", want: "do it"},
		{name: "纯空白系统提示词原样返回", prompt: "do it", sys: "  \n\t ", want: "do it"},
		{name: "系统提示词前置并以空行分隔", prompt: "do it", sys: "  be careful  ", want: "be careful\n\ndo it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := applyProfilePrompt(tt.prompt, tt.sys); got != tt.want {
				t.Errorf("applyProfilePrompt(%q, %q) = %q, want %q", tt.prompt, tt.sys, got, tt.want)
			}
		})
	}
}

func TestResolveBeeConfig(t *testing.T) {
	pairs := []Pair{
		{ID: "pair-deep", Name: "Deep", BeeProvider: "claude", BeeModel: "opus", BeeEffort: "high", BeeFastMode: true},
		{ID: "pair-quick", Name: "Quick", BeeProvider: "claude", BeeModel: "haiku"},
	}
	profiles := []BeeProfile{
		{Name: "quick", Model: "flash", Effort: "low", SystemPrompt: "fast", AllowedTools: []string{"read"}},
		{Name: "same-provider", Provider: "claude", SystemPrompt: "stay"},
		{Name: "other-provider", Provider: "gemini", SystemPrompt: "elsewhere"},
	}

	tests := []struct {
		name      string
		in        ResolveInput
		want      BeeConfig
		wantNotes []string
	}{
		{
			name: "无任何来源时保持零值",
			in:   ResolveInput{Delegation: Delegation{Task: "t"}},
			want: BeeConfig{},
		},
		{
			name: "配对命中时取 bee 字段",
			in:   ResolveInput{Delegation: Delegation{Pair: "pair-deep"}, Pairs: pairs, UserMessage: "pair-deep"},
			want: BeeConfig{Provider: "claude", Model: "opus", Effort: "high", FastMode: true, PairID: "pair-deep", DirectModel: true},
		},
		{
			name:      "配对未命中时给出提示",
			in:        ResolveInput{Delegation: Delegation{Pair: "nope"}, Pairs: pairs},
			want:      BeeConfig{},
			wantNotes: []string{`未找到配对 "nope"，按默认配置派发`},
		},
		{
			name: "Profile 命中时生效",
			in: ResolveInput{
				Delegation: Delegation{Profile: "quick"},
				Profiles:   profiles,
			},
			want: BeeConfig{
				Model:        "flash",
				Effort:       "low",
				SystemPrompt: "fast",
				Profile:      "quick",
			},
		},
		{
			name:      "Profile 未命中时给出提示",
			in:        ResolveInput{Delegation: Delegation{Profile: "nope"}, Profiles: profiles},
			want:      BeeConfig{},
			wantNotes: []string{`未找到 Profile "nope"，按默认配置派发`},
		},
		{
			name: "委派自带 provider/model 在授权时覆盖",
			in: ResolveInput{
				Delegation:  Delegation{Profile: "quick", Provider: "codex", Model: "gpt"},
				Profiles:    profiles,
				UserMessage: "gpt",
			},
			want: BeeConfig{
				Provider:     "codex",
				Model:        "gpt",
				Effort:       "low",
				SystemPrompt: "fast",
				Profile:      "quick",
				DirectModel:  true,
			},
		},
		{
			name: "同 Provider 且该层无 Model 时保留原 Model",
			in: ResolveInput{
				Delegation: Delegation{Tier: "t1"},
				Tiers:      []Tier{{ID: "t1", Provider: "claude", Model: "opus"}},
				ThreadOverrides: map[string]TierOverride{
					"t1": {Provider: "claude", Effort: "high"},
				},
			},
			want: BeeConfig{
				Provider: "claude",
				Model:    "opus",
				Effort:   "high",
				Tier:     "t1",
			},
		},
		{
			name: "Provider 变更且该层无 Model 时清空 Model",
			in: ResolveInput{
				Delegation: Delegation{Tier: "t1"},
				Tiers:      []Tier{{ID: "t1", Provider: "claude", Model: "opus"}},
				ThreadOverrides: map[string]TierOverride{
					"t1": {Provider: "gemini", Effort: "high"},
				},
			},
			want: BeeConfig{
				Provider: "gemini",
				Effort:   "high",
				Tier:     "t1",
			},
		},
		{
			name: "配对与 Profile 都未命中时按顺序给出两条提示",
			in: ResolveInput{
				Delegation: Delegation{Pair: "p", Profile: "q"},
				Pairs:      pairs,
				Profiles:   profiles,
			},
			want: BeeConfig{},
			wantNotes: []string{
				`未找到 Profile "q"，按默认配置派发`,
				`未找到配对 "p"，按默认配置派发`,
			},
		},
		{
			name: "会话子代理模型优先于档位模型",
			in: ResolveInput{
				Delegation:         Delegation{Tier: "t1"},
				Tiers:              []Tier{{ID: "t1", Provider: "claude", Model: "opus"}},
				SessionBeeProvider: "codex",
				SessionBeeModel:    "gpt-5",
			},
			want: BeeConfig{Provider: "codex", Model: "gpt-5", Tier: "t1"},
		},
		{
			name: "默认档位不覆盖会话子代理模型",
			in: ResolveInput{
				Delegation:         Delegation{Task: "t"},
				Tiers:              []Tier{{ID: "t1", Provider: "claude", Model: "opus"}},
				DefaultTier:        "t1",
				SessionBeeProvider: "codex",
				SessionBeeModel:    "gpt-5",
			},
			want: BeeConfig{Provider: "codex", Model: "gpt-5", Tier: "t1"},
		},
		{
			name: "会话只有 model 时保留档位 provider",
			in: ResolveInput{
				Delegation:      Delegation{Tier: "t1"},
				Tiers:           []Tier{{ID: "t1", Provider: "claude", Model: "opus"}},
				SessionBeeModel: "gpt-5",
			},
			want: BeeConfig{Provider: "claude", Model: "gpt-5", Tier: "t1"},
		},
		{
			name: "会话子代理强度优先于档位强度",
			in: ResolveInput{
				Delegation:         Delegation{},
				Tiers:              []Tier{{ID: "t1", Provider: "codex", Model: "gpt-5.1-codex", Effort: "medium"}},
				DefaultTier:        "t1",
				SessionBeeProvider: "codex",
				SessionBeeModel:    "gpt-6.1",
				SessionBeeEffort:   "xhigh",
			},
			want: BeeConfig{Provider: "codex", Model: "gpt-6.1", Effort: "xhigh", Tier: "t1"},
		},
		{
			name: "线程档位覆盖的强度仍覆盖会话子代理强度",
			in: ResolveInput{
				Delegation:         Delegation{Tier: "t1"},
				Tiers:              []Tier{{ID: "t1", Provider: "codex", Model: "gpt-5.1-codex", Effort: "medium"}},
				SessionBeeProvider: "codex",
				SessionBeeModel:    "gpt-6.1",
				SessionBeeEffort:   "xhigh",
				ThreadOverrides: map[string]TierOverride{
					"t1": {Effort: "high"},
				},
			},
			want: BeeConfig{Provider: "codex", Model: "gpt-6.1", Effort: "high", Tier: "t1"},
		},
		{
			name: "会话子代理强度为空时保留档位强度",
			in: ResolveInput{
				Delegation:         Delegation{Tier: "t1"},
				Tiers:              []Tier{{ID: "t1", Provider: "codex", Model: "gpt-5.1-codex", Effort: "medium"}},
				SessionBeeProvider: "codex",
				SessionBeeModel:    "gpt-6.1",
			},
			want: BeeConfig{Provider: "codex", Model: "gpt-6.1", Effort: "medium", Tier: "t1"},
		},
		{
			name: "会话只选强度时保留档位模型",
			in: ResolveInput{
				Delegation:       Delegation{Tier: "t1"},
				Tiers:            []Tier{{ID: "t1", Provider: "codex", Model: "gpt-5.1-codex", Effort: "medium"}},
				SessionBeeEffort: "xhigh",
			},
			want: BeeConfig{Provider: "codex", Model: "gpt-5.1-codex", Effort: "xhigh", Tier: "t1"},
		},
		{
			name: "会话子代理模型被线程覆盖时提示不带空的 provider",
			in: ResolveInput{
				Delegation:      Delegation{Tier: "t1"},
				Tiers:           []Tier{{ID: "t1", Provider: "claude", Model: "opus"}},
				SessionBeeModel: "gpt-5",
				ThreadOverrides: map[string]TierOverride{
					"t1": {Provider: "gemini", Model: "flash"},
				},
			},
			want:      BeeConfig{Provider: "gemini", Model: "flash", Tier: "t1"},
			wantNotes: []string{`会话子代理模型 gpt-5 已被后续配置覆盖，实际使用 gemini/flash`},
		},
		{
			name: "委派明确授权的直属模型仍覆盖会话子代理模型",
			in: ResolveInput{
				Delegation:         Delegation{Tier: "t1", Provider: "codex", Model: "gpt"},
				Tiers:              []Tier{{ID: "t1", Provider: "claude", Model: "opus"}},
				SessionBeeProvider: "gemini",
				SessionBeeModel:    "flash",
				UserMessage:        "请用 gpt 跑",
			},
			want:      BeeConfig{Provider: "codex", Model: "gpt", Tier: "t1", DirectModel: true},
			wantNotes: []string{`会话子代理模型 gemini/flash 已被后续配置覆盖，实际使用 codex/gpt`},
		},
		{
			name: "档位模型与会话子代理模型相同时不提示",
			in: ResolveInput{
				Delegation:         Delegation{Tier: "t1"},
				Tiers:              []Tier{{ID: "t1", Provider: "claude", Model: "opus"}},
				SessionBeeProvider: "claude",
				SessionBeeModel:    "opus",
			},
			want: BeeConfig{Provider: "claude", Model: "opus", Tier: "t1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, notes := ResolveBeeConfig(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("config = %+v, want %+v", got, tt.want)
			}
			if !reflect.DeepEqual(notes, tt.wantNotes) {
				t.Errorf("notes = %v, want %v", notes, tt.wantNotes)
			}
		})
	}
}

// deep 档的写入白名单随档位解析进入 BeeConfig；未知档位回落默认档时也带上默认档的白名单。
// 解析结果是切片拷贝，改档位上的白名单不影响已解析的配置。
func TestResolveBeeConfigWritePaths(t *testing.T) {
	deepPaths := []string{"docs/superpowers/plans/**/*.md"}
	tiers := []Tier{
		{ID: "read", ReadOnly: true},
		{ID: "edit"},
		{ID: "deep", WritePaths: deepPaths},
	}

	got, notes := ResolveBeeConfig(ResolveInput{Delegation: Delegation{Tier: "deep"}, Tiers: tiers, DefaultTier: "edit"})
	if len(notes) != 0 {
		t.Fatalf("不该有提示，得到 %v", notes)
	}
	if !reflect.DeepEqual(got.WritePaths, deepPaths) {
		t.Fatalf("deep 档的 WritePaths = %v, want %v", got.WritePaths, deepPaths)
	}

	fallback, notes := ResolveBeeConfig(ResolveInput{Delegation: Delegation{Tier: "nope"}, Tiers: tiers, DefaultTier: "deep"})
	if len(notes) != 1 {
		t.Fatalf("未知档位应给出一条提示，得到 %v", notes)
	}
	if !reflect.DeepEqual(fallback.WritePaths, deepPaths) {
		t.Fatalf("回落默认档的 WritePaths = %v, want %v", fallback.WritePaths, deepPaths)
	}

	paths := []string{"docs/**/*.md"}
	copied, _ := ResolveBeeConfig(ResolveInput{
		Delegation: Delegation{Tier: "deep"},
		Tiers:      []Tier{{ID: "deep", WritePaths: paths}},
	})
	paths[0] = "mutated"
	if !reflect.DeepEqual(copied.WritePaths, []string{"docs/**/*.md"}) {
		t.Fatalf("BeeConfig 应复制白名单切片，得到 %v", copied.WritePaths)
	}

	plain, _ := ResolveBeeConfig(ResolveInput{Delegation: Delegation{Tier: "edit"}, Tiers: tiers})
	if len(plain.WritePaths) != 0 {
		t.Fatalf("edit 档不该有限制，得到 %v", plain.WritePaths)
	}
}
