package magpiecfg

import (
	"reflect"
	"testing"
)

func TestClaudeDashed(t *testing.T) {
	cases := []struct {
		name string
		id   string
		want string
	}{
		{name: "版本号点改横线", id: "auto/claude-sonnet-4.5", want: "auto/claude-sonnet-4-5"},
		{name: "已是横线", id: "auto/claude-sonnet-5-5", want: "auto/claude-sonnet-5-5"},
		{name: "非 Claude 模型", id: "deepseek-v3.2", want: "deepseek-v3.2"},
		{name: "provider 前缀与 1m 标记保留", id: "relay/claude-opus-4.6-us[1m]", want: "relay/claude-opus-4-6-us[1m]"},
		{name: "effort 后缀保留", id: "auto/claude-sonnet-4.5:high", want: "auto/claude-sonnet-4-5:high"},
		{name: "大小写保留", id: "auto/CLAUDE-SONNET-4.5", want: "auto/CLAUDE-SONNET-4-5"},
		{name: "多段版本号", id: "claude-mythos-5.1.2", want: "claude-mythos-5-1-2"},
		{name: "非 Claude 版本号不动", id: "minimax-2.5", want: "minimax-2.5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClaudeDashed(tc.id); got != tc.want {
				t.Fatalf("ClaudeDashed(%q) = %q，想要 %q", tc.id, got, tc.want)
			}
		})
	}
}

func TestClaudeSpelled(t *testing.T) {
	// served 是网关 /v1/models 给出的原始 ID（点号形式），横线版是写给
	// Claude Code 的拼写。
	served := map[string]bool{
		"auto/claude-sonnet-4.5":   true,
		"relay/claude-opus-4.6-us": true,
		"auto/claude-opus-5-5":     true,
	}
	cases := []struct {
		name   string
		ref    string
		served map[string]bool
		want   string
	}{
		{
			name:   "网关服务点号版、横线版没人服务就改写",
			ref:    "auto/claude-sonnet-4.5",
			served: served,
			want:   "auto/claude-sonnet-4-5",
		},
		{
			name:   "served 里没有该模型则不改",
			ref:    "auto/claude-sonnet-4.5",
			served: map[string]bool{"auto/claude-sonnet-5-5": true},
			want:   "auto/claude-sonnet-4.5",
		},
		{
			name:   "served 为空不做点号改横线",
			ref:    "auto/claude-sonnet-4.5",
			served: nil,
			want:   "auto/claude-sonnet-4.5",
		},
		{
			name: "横线版本身也在服务时保持原样",
			ref:  "auto/claude-sonnet-4.5",
			served: map[string]bool{
				"auto/claude-sonnet-4.5": true,
				"auto/claude-sonnet-4-5": true,
			},
			want: "auto/claude-sonnet-4.5",
		},
		{
			name:   "保留 effort 与 1m 标记",
			ref:    "auto/claude-sonnet-4.5:high[1m]",
			served: served,
			want:   "auto/claude-sonnet-4-5:high[1m]",
		},
		{
			name:   "保留 1m 标记",
			ref:    "relay/claude-opus-4.6-us[1m]",
			served: served,
			want:   "relay/claude-opus-4-6-us[1m]",
		},
		{
			name:   "非 Claude 模型不改名",
			ref:    "deepseek-v3.2",
			served: map[string]bool{"deepseek-v3-2": true},
			want:   "deepseek-v3.2",
		},
		{
			name:   "已是横线且在服务",
			ref:    "auto/claude-opus-5-5",
			served: served,
			want:   "auto/claude-opus-5-5",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClaudeSpelled(tc.ref, tc.served); got != tc.want {
				t.Fatalf("ClaudeSpelled(%q) = %q，想要 %q", tc.ref, got, tc.want)
			}
		})
	}
}

func TestClaudeOneM(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		context int
		want    string
	}{
		{name: "正好一百万", id: "auto/claude-sonnet-5-5", context: 1000000, want: "auto/claude-sonnet-5-5[1m]"},
		{name: "超过一百万", id: "auto/claude-sonnet-5-5", context: 1500000, want: "auto/claude-sonnet-5-5[1m]"},
		{name: "差一个 token 不加", id: "auto/claude-sonnet-5-5", context: 999999, want: "auto/claude-sonnet-5-5"},
		{name: "窗口未知不加", id: "auto/claude-sonnet-5-5", context: 0, want: "auto/claude-sonnet-5-5"},
		{name: "已有标记不重复加", id: "auto/claude-sonnet-5-5[1m]", context: 1000000, want: "auto/claude-sonnet-5-5[1m]"},
		{name: "空 ID", id: "", context: 1000000, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MarkOneM(tc.id, tc.context); got != tc.want {
				t.Fatalf("MarkOneM(%q, %d) = %q，想要 %q", tc.id, tc.context, got, tc.want)
			}
		})
	}
}

func claudeTierWant(opus, sonnet, haiku, fable string) map[string]string {
	return map[string]string{"opus": opus, "sonnet": sonnet, "haiku": haiku, "fable": fable}
}

func TestClaudeTierModels(t *testing.T) {
	threeChecked := []ClaudeSelection{
		{ID: "auto/claude-sonnet-5-5"},
		{ID: "auto/claude-opus-5-5"},
		{ID: "auto/claude-haiku-5-5"},
	}
	cases := []struct {
		name       string
		main       string
		selections []ClaudeSelection
		want       map[string]string
	}{
		{
			name:       "三个勾选模型，主模型 sonnet：各档指向勾选里的同系列模型",
			main:       "auto/claude-sonnet-5-5",
			selections: threeChecked,
			want: claudeTierWant(
				"auto/claude-opus-5-5", "auto/claude-sonnet-5-5", "auto/claude-haiku-5-5", "",
			),
		},
		{
			name:       "三个勾选模型，主模型 opus",
			main:       "auto/claude-opus-5-5",
			selections: threeChecked,
			want: claudeTierWant(
				"auto/claude-opus-5-5", "auto/claude-sonnet-5-5", "auto/claude-haiku-5-5", "",
			),
		},
		{
			name:       "主模型自己就是 haiku",
			main:       "auto/claude-haiku-5-5",
			selections: threeChecked,
			want: claudeTierWant(
				"auto/claude-opus-5-5", "auto/claude-sonnet-5-5", "auto/claude-haiku-5-5", "",
			),
		},
		{
			name:       "只勾选 sonnet 时 opus 档退回主模型",
			main:       "auto/claude-sonnet-5-5",
			selections: []ClaudeSelection{{ID: "auto/claude-sonnet-5-5"}},
			want: claudeTierWant(
				"auto/claude-sonnet-5-5", "auto/claude-sonnet-5-5", "auto/claude-sonnet-5-5", "",
			),
		},
		{
			name:       "前缀不同的同系列模型不跨前缀选取",
			main:       "auto/claude-sonnet-5-5",
			selections: []ClaudeSelection{{ID: "auto/claude-sonnet-5-5"}, {ID: "group/claude-opus-5-5"}},
			want: claudeTierWant(
				"auto/claude-sonnet-5-5", "auto/claude-sonnet-5-5", "auto/claude-sonnet-5-5", "",
			),
		},
		{
			name:       "同系列 opus 带日期与不带日期取不带日期",
			main:       "auto/claude-sonnet-5-5",
			selections: []ClaudeSelection{{ID: "auto/claude-sonnet-5-5"}, {ID: "auto/claude-opus-5-5-20250101"}, {ID: "auto/claude-opus-5-5"}},
			want: claudeTierWant(
				"auto/claude-opus-5-5", "auto/claude-sonnet-5-5", "auto/claude-sonnet-5-5", "",
			),
		},
		{
			name:       "勾选里有更新的 haiku 时取最新",
			main:       "auto/claude-opus-5-5",
			selections: []ClaudeSelection{{ID: "auto/claude-opus-5-5"}, {ID: "auto/claude-haiku-4-5-20250101"}, {ID: "auto/claude-haiku-5-5"}},
			want: claudeTierWant(
				"auto/claude-opus-5-5", "auto/claude-opus-5-5", "auto/claude-haiku-5-5", "",
			),
		},
		{
			name:       "同版本带日期与不带日期取不带日期",
			main:       "auto/claude-opus-5-5",
			selections: []ClaudeSelection{{ID: "auto/claude-haiku-5-5-20250101"}, {ID: "auto/claude-haiku-5-5"}},
			want: claudeTierWant(
				"auto/claude-opus-5-5", "auto/claude-opus-5-5", "auto/claude-haiku-5-5", "",
			),
		},
		{
			name:       "只有非 Claude 模型",
			main:       "auto/deepseek-v3-2",
			selections: []ClaudeSelection{{ID: "auto/deepseek-v3-2"}},
			want: claudeTierWant(
				"auto/deepseek-v3-2", "auto/deepseek-v3-2", "auto/deepseek-v3-2", "",
			),
		},
		{
			name:       "只有带点号的非 Claude 模型",
			main:       "auto/deepseek-v4.1-flash",
			selections: []ClaudeSelection{{ID: "auto/deepseek-v4.1-flash"}},
			want: claudeTierWant(
				"auto/deepseek-v4.1-flash", "auto/deepseek-v4.1-flash", "auto/deepseek-v4.1-flash", "",
			),
		},
		{
			name:       "主模型是 fable",
			main:       "auto/claude-fable-5-5",
			selections: []ClaudeSelection{{ID: "auto/claude-fable-5-5"}, {ID: "auto/claude-haiku-5-5"}},
			want: claudeTierWant(
				"auto/claude-fable-5-5", "auto/claude-fable-5-5", "auto/claude-haiku-5-5", "auto/claude-fable-5-5",
			),
		},
		{
			name:       "勾选里没有 haiku 时 haiku 档跟随主模型",
			main:       "auto/claude-sonnet-5-5",
			selections: []ClaudeSelection{{ID: "auto/claude-sonnet-5-5"}, {ID: "auto/claude-opus-5-5"}},
			want: claudeTierWant(
				"auto/claude-opus-5-5", "auto/claude-sonnet-5-5", "auto/claude-sonnet-5-5", "",
			),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClaudeTierModels(tc.main, tc.selections)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ClaudeTierModels(%q) = %#v，想要 %#v", tc.main, got, tc.want)
			}
		})
	}
}

func TestClaudeTierLight(t *testing.T) {
	selections := []ClaudeSelection{
		{ID: "auto/claude-opus-5-5"},
		{ID: "auto/claude-haiku-5-5"},
		{ID: "auto/claude-haiku-4-5-20250101"},
	}
	cases := []struct {
		name string
		main string
		want string
	}{
		{name: "同前缀寻找", main: "auto/claude-opus-5-5", want: "auto/claude-haiku-5-5"},
		{name: "主模型已是 haiku 返回空", main: "auto/claude-haiku-5-5", want: ""},
		{name: "非 Claude 模型返回空", main: "auto/deepseek-v3-2", want: ""},
		{name: "主模型带 1m 标记", main: "auto/claude-opus-5-5[1m]", want: "auto/claude-haiku-5-5"},
		{name: "没有 provider 前缀的 ID 不找", main: "claude-opus-5-5", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudeLight(tc.main, selections); got != tc.want {
				t.Fatalf("claudeLight(%q) = %q，想要 %q", tc.main, got, tc.want)
			}
		})
	}
}

func TestClaudeTierEnv(t *testing.T) {
	cases := []struct {
		tier string
		want string
	}{
		{tier: "opus", want: "ANTHROPIC_DEFAULT_OPUS_MODEL"},
		{tier: "sonnet", want: "ANTHROPIC_DEFAULT_SONNET_MODEL"},
		{tier: "haiku", want: "ANTHROPIC_DEFAULT_HAIKU_MODEL"},
		{tier: "fable", want: "ANTHROPIC_DEFAULT_FABLE_MODEL"},
	}
	for _, tc := range cases {
		if got := TierEnv(tc.tier); got != tc.want {
			t.Fatalf("TierEnv(%q) = %q，想要 %q", tc.tier, got, tc.want)
		}
	}
}

func TestClaudeTierAboutEnv(t *testing.T) {
	want := []string{
		"ANTHROPIC_DEFAULT_OPUS_MODEL_NAME",
		"ANTHROPIC_DEFAULT_OPUS_MODEL_DESCRIPTION",
		"ANTHROPIC_DEFAULT_OPUS_MODEL_SUPPORTED_CAPABILITIES",
		"ANTHROPIC_DEFAULT_SONNET_MODEL_NAME",
		"ANTHROPIC_DEFAULT_SONNET_MODEL_DESCRIPTION",
		"ANTHROPIC_DEFAULT_SONNET_MODEL_SUPPORTED_CAPABILITIES",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL_DESCRIPTION",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL_SUPPORTED_CAPABILITIES",
		"ANTHROPIC_DEFAULT_FABLE_MODEL_NAME",
		"ANTHROPIC_DEFAULT_FABLE_MODEL_DESCRIPTION",
		"ANTHROPIC_DEFAULT_FABLE_MODEL_SUPPORTED_CAPABILITIES",
	}
	if got := ClaudeAboutEnv(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ClaudeAboutEnv() = %#v，想要 %#v", got, want)
	}
}

func TestClaudeTierFable(t *testing.T) {
	cases := []struct {
		name string
		main string
		want bool
	}{
		{name: "Fable 横线版", main: "auto/claude-fable-5-5", want: true},
		{name: "Fable 点号版", main: "auto/claude-fable-5.5", want: true},
		{name: "Fable 带 1m 标记", main: "auto/claude-fable-5-5[1m]", want: true},
		{name: "Opus 不是 Fable", main: "auto/claude-opus-5-5", want: false},
		{name: "非 Claude 模型不是 Fable", main: "auto/deepseek-v4.1-flash", want: false},
		{name: "版本号缺失不算 Fable", main: "auto/claude-fable-latest", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClaudeIsFableName(tc.main); got != tc.want {
				t.Fatalf("ClaudeIsFableName(%q) = %v，想要 %v", tc.main, got, tc.want)
			}
		})
	}
}

func TestClaudeTierServed(t *testing.T) {
	served := map[string]bool{
		"auto/claude-sonnet-5-5": true,
		"auto/claude-sonnet-4-5": true,
	}
	cases := []struct {
		name string
		ref  string
		want bool
	}{
		{name: "服务中的 ID", ref: "auto/claude-sonnet-5-5", want: true},
		{name: "去掉 effort 与 1m 后命中", ref: "auto/claude-sonnet-5-5:high[1m]", want: true},
		{name: "点号 ID 不在服务集合", ref: "auto/claude-sonnet-4.5", want: false},
		{name: "不在服务集合", ref: "auto/deepseek-v3-2", want: false},
		{name: "空 ID", ref: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClaudeServed(tc.ref, served); got != tc.want {
				t.Fatalf("ClaudeServed(%q) = %v，想要 %v", tc.ref, got, tc.want)
			}
		})
	}
}
