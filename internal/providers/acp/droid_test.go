package acp

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestParseDroidAuthMode(t *testing.T) {
	tests := []struct {
		in   string
		want DroidAuthMode
	}{
		// 已知取值
		{"apiKey", DroidAuthAPIKey},
		{"browser", DroidAuthWeb},
		// 未知/大小写不符/带空白取值统一回落到默认 apiKey
		{"", DroidAuthAPIKey},
		{"web", DroidAuthAPIKey},
		{"APIKEY", DroidAuthAPIKey},
		{"Browser", DroidAuthAPIKey},
		{" apiKey", DroidAuthAPIKey},
	}
	for _, tc := range tests {
		if got := ParseDroidAuthMode(tc.in); got != tc.want {
			t.Errorf("ParseDroidAuthMode(%q) = %q, expected %q", tc.in, got, tc.want)
		}
	}
}

func TestDroidAuthEnv(t *testing.T) {
	tests := []struct {
		name   string
		base   []string
		mode   DroidAuthMode
		apiKey string
		want   []string
	}{
		{
			name:   "API Key 模式追加",
			base:   []string{"PATH=/bin", "HOME=/root"},
			mode:   DroidAuthAPIKey,
			apiKey: "secret-key",
			want:   []string{"PATH=/bin", "HOME=/root", "FACTORY_API_KEY=secret-key"},
		},
		{
			name:   "API Key 模式覆盖已有值",
			base:   []string{"PATH=/bin", "FACTORY_API_KEY=old", "HOME=/root"},
			mode:   DroidAuthAPIKey,
			apiKey: "new-key",
			want:   []string{"PATH=/bin", "HOME=/root", "FACTORY_API_KEY=new-key"},
		},
		{
			name:   "API Key 模式删除多个已有值",
			base:   []string{"FACTORY_API_KEY=a", "FACTORY_API_KEY=b"},
			mode:   DroidAuthAPIKey,
			apiKey: "k",
			want:   []string{"FACTORY_API_KEY=k"},
		},
		{
			name:   "Web 模式删除已有值",
			base:   []string{"PATH=/bin", "FACTORY_API_KEY=old"},
			mode:   DroidAuthWeb,
			apiKey: "secret-key",
			want:   []string{"PATH=/bin"},
		},
		{
			name:   "空 key 不追加",
			base:   []string{"PATH=/bin"},
			mode:   DroidAuthAPIKey,
			apiKey: "",
			want:   []string{"PATH=/bin"},
		},
		{
			name:   "纯空白 key 不追加",
			base:   []string{"PATH=/bin"},
			mode:   DroidAuthAPIKey,
			apiKey: "   ",
			want:   []string{"PATH=/bin"},
		},
		{
			name:   "key 首尾空白被去除",
			base:   nil,
			mode:   DroidAuthAPIKey,
			apiKey: "  k  ",
			want:   []string{"FACTORY_API_KEY=k"},
		},
		{
			name:   "前缀相似但非该变量不受影响",
			base:   []string{"FACTORY_API_KEY_EXTRA=1", "FACTORY_API_KEYX=2"},
			mode:   DroidAuthWeb,
			apiKey: "k",
			want:   []string{"FACTORY_API_KEY_EXTRA=1", "FACTORY_API_KEYX=2"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			original := append([]string(nil), tc.base...)
			got := DroidAuthEnv(tc.base, tc.mode, tc.apiKey)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("DroidAuthEnv() = %v, expected %v", got, tc.want)
			}
			// 原切片不被修改
			if !reflect.DeepEqual(tc.base, original) {
				t.Errorf("原始切片被修改: got %v, want %v", tc.base, original)
			}
		})
	}
}

func TestHarnessDroidAuthEnv(t *testing.T) {
	for _, tc := range []struct {
		name  string
		agent Agent
		mode  DroidAuthMode
		want  []string
		calls int
	}{
		{"apiKey", AgentDroid, DroidAuthAPIKey, []string{"PATH=/bin", "FACTORY_API_KEY=secret"}, 1},
		{"browser", AgentDroid, DroidAuthWeb, []string{"PATH=/bin"}, 0},
		{"default", AgentDroid, "", []string{"PATH=/bin"}, 0},
		{"other agent", AgentCline, DroidAuthAPIKey, []string{"PATH=/bin", "FACTORY_API_KEY=old"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			h := New(Options{Agent: tc.agent, DroidAuth: tc.mode, DroidAPIKey: func() string {
				calls++
				return "secret"
			}})
			if calls != 0 {
				t.Fatal("API key read during registration")
			}
			got := buildEnv(h.authEnv([]string{"PATH=/bin", "FACTORY_API_KEY=old"}), "", nil, nil)
			if !reflect.DeepEqual(got, tc.want) || calls != tc.calls {
				t.Errorf("env = %v, calls = %d; want %v, %d", got, calls, tc.want, tc.calls)
			}
		})
	}
}

func TestDroidRequestedMode(t *testing.T) {
	allModes := []string{"normal", "spec", "auto-low", "auto-medium", "auto-high"}

	tests := []struct {
		name        string
		interaction string
		runtimeMode string
		available   []string
		want        string
	}{
		// 计划模式：优先 spec
		{"计划模式有 spec", "plan", "supervised", allModes, "spec"},
		{"计划模式只有 normal", "plan", "fullAccess", []string{"normal", "auto-high"}, "normal"},
		{"计划模式都没有 spec 和 normal", "plan", "auto", []string{"auto-medium"}, ""},
		{"计划模式没有可用模式", "plan", "supervised", nil, ""},
		// 非计划模式按运行时权限映射
		{"supervised 映射 normal", "build", "supervised", allModes, "normal"},
		{"autoAcceptEdits 映射 auto-low", "build", "autoAcceptEdits", allModes, "auto-low"},
		{"auto 映射 auto-medium", "build", "auto", allModes, "auto-medium"},
		{"fullAccess 映射 auto-high", "build", "fullAccess", allModes, "auto-high"},
		// readonly 与空值在 Droid 没有对应档位，不切换
		{"readonly 不切换", "build", "readonly", allModes, ""},
		{"空权限不切换", "build", "", allModes, ""},
		// 空交互模式等同 build
		{"空交互模式按 build 处理", "", "auto", allModes, "auto-medium"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DroidRequestedMode(tc.interaction, tc.runtimeMode, tc.available); got != tc.want {
				t.Errorf("DroidRequestedMode(%q, %q, %v) = %q, expected %q", tc.interaction, tc.runtimeMode, tc.available, got, tc.want)
			}
		})
	}
}

func TestDroidPlanExitApprovalID(t *testing.T) {
	allOptions := []string{"proceed_auto_run_high", "proceed_auto_run_medium", "proceed_auto_run_low", "proceed_once", "cancel"}

	tests := []struct {
		name        string
		runtimeMode string
		optionIDs   []string
		want        string
	}{
		{"fullAccess 选高档自动执行", "fullAccess", allOptions, "proceed_auto_run_high"},
		{"auto 选中档自动执行", "auto", allOptions, "proceed_auto_run_medium"},
		{"autoAcceptEdits 选低档自动执行", "autoAcceptEdits", allOptions, "proceed_auto_run_low"},
		{"supervised 固定 proceed_once", "supervised", allOptions, "proceed_once"},
		{"readonly 回落 proceed_once", "readonly", allOptions, "proceed_once"},
		// 目标选项缺失时的回退
		{"fullAccess 缺高档回落 proceed_once", "fullAccess", []string{"proceed_auto_run_medium", "proceed_once"}, "proceed_once"},
		{"auto 缺中档回落 proceed_once", "auto", []string{"proceed_auto_run_low", "proceed_once"}, "proceed_once"},
		{"autoAcceptEdits 缺低档回落 proceed_once", "autoAcceptEdits", []string{"proceed_once", "cancel"}, "proceed_once"},
		{"选项列表为空回落 proceed_once", "auto", nil, "proceed_once"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DroidPlanExitApprovalID(tc.runtimeMode, tc.optionIDs); got != tc.want {
				t.Errorf("DroidPlanExitApprovalID(%q, %v) = %q, expected %q", tc.runtimeMode, tc.optionIDs, got, tc.want)
			}
		})
	}
}

func TestDroidIsPlanExit(t *testing.T) {
	tests := []struct {
		name        string
		title       string
		toolName    string
		optionNames []string
		rawInput    string
		want        bool
	}{
		{"标题为 Approve Spec", "Approve Spec", "ExitSpecMode", nil, "", true},
		{"工具名为 ExitSpecMode", "", "ExitSpecMode", nil, "", true},
		{"工具名为 exit_spec_mode", "", "exit_spec_mode", nil, "", true},
		{"rawInput 同时带 plan 与 title", "", "other", nil, `{"plan":"步骤","title":"计划"}`, true},
		{"rawInput 只有 plan", "", "other", nil, `{"plan":"步骤"}`, false},
		{"选项名称含 Spec", "", "other", []string{"Allow Spec", "Cancel"}, "", true},
		{"选项名称仅为 proceed_once", "", "other", []string{"Proceed Once", "Cancel"}, "", false},
		{"普通工具调用", "Run tests", "bash", []string{"Proceed Once"}, `{"command":"go test"}`, false},
		{"空输入", "", "", nil, "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var raw json.RawMessage
			if tc.rawInput != "" {
				raw = json.RawMessage(tc.rawInput)
			}
			if got := DroidIsPlanExit(tc.title, tc.toolName, tc.optionNames, raw); got != tc.want {
				t.Errorf("DroidIsPlanExit(%q, %q, %v, %s) = %v, expected %v", tc.title, tc.toolName, tc.optionNames, tc.rawInput, got, tc.want)
			}
		})
	}
}

func TestDroidPlanMarkdown(t *testing.T) {
	tests := []struct {
		name     string
		rawInput string
		want     string
	}{
		{"取 plan 正文", `{"plan":"第一步\n第二步"}`, "第一步\n第二步"},
		{"plan 为空时回退 markdown", `{"plan":"   ","markdown":"markdown 正文"}`, "markdown 正文"},
		{"优先 plan 而非 content", `{"content":"内容","plan":"计划"}`, "计划"},
		{"spec 键", `{"spec":"spec 正文"}`, "spec 正文"},
		{"无可取字段", `{"other":"x"}`, ""},
		{"非对象输入", `"text"`, ""},
		{"空输入", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var raw json.RawMessage
			if tc.rawInput != "" {
				raw = json.RawMessage(tc.rawInput)
			}
			if got := DroidPlanMarkdown(raw); got != tc.want {
				t.Errorf("DroidPlanMarkdown(%s) = %q, expected %q", tc.rawInput, got, tc.want)
			}
		})
	}
}
