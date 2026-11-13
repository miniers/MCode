package mcodeui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

// TestModelBrandKey 验证按模型名/ID 判断品牌，未命中已知品牌时回退 Provider 图标键。
func TestModelBrandKey(t *testing.T) {
	cases := []struct {
		name                 string
		provider, model, mod string
		want                 string
	}{
		{"Claude 名称", "pi", "claude-opus-4", "Claude Opus 4", "claude"},
		{"Opus 短名", "claude", "opus", "Opus", "claude"},
		{"Sonnet 短名", "codex", "sonnet-4-6", "Sonnet 4.6", "claude"},
		{"DeepSeek 名称", "pi", "deepseek-v4.1-flash", "DeepSeek V4.1 Flash", "deepseek"},
		{"Gemini 名称", "agy", "gemini-3-8-flash", "Gemini 3.8 Flash", "gemini"},
		{"Grok 名称", "cursor", "grok-5", "Grok 5", "grok"},
		{"GLM 名称", "cline", "cline-pass/glm-5.3-flash", "GLM-5.3 Flash", "zai"},
		{"Llama 名称", "meta", "llama-4-1", "Llama 4.1", "meta"},
		{"GPT 名称", "openai", "gpt-5-5", "GPT-5.5", "codex"},
		{"未知模型回退 provider", "cursor", "composer-1", "Composer", "cursor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := modelBrandKey(tc.provider, tc.model, tc.mod); got != tc.want {
				t.Fatalf("modelBrandKey(%q, %q, %q) = %q, want %q", tc.provider, tc.model, tc.mod, got, tc.want)
			}
		})
	}
}

// TestModelShortName 验证胶囊短名：去「 · 」后的附加说明、去 ID 前缀与 (auto)/(latest) 后缀、
// 去上下文窗口后缀、去品牌前缀，数字开头的名字与其他括号（如 (Thinking)）保持不变。
func TestModelShortName(t *testing.T) {
	cases := []struct {
		name, brand, want string
	}{
		{"Claude Opus 5.5", "claude", "Opus 5.5"},
		{"DeepSeek V4.1 Flash (auto)", "deepseek", "V4.1 Flash"},
		{"Claude 3.7 Sonnet", "claude", "Claude 3.7 Sonnet"},
		{"Gemini 3.8 Flash", "gemini", "Gemini 3.8 Flash"},
		{"GPT-5.5", "codex", "GPT-5.5"},
		{"Claude Sonnet 4.6 (Thinking)", "claude", "Sonnet 4.6 (Thinking)"},
		{"cline-pass/glm-5.3-flash", "zai", "GLM-5.3 Flash"},
		{"  Meta Llama 4.1 (latest) ", "meta", "Llama 4.1"},
		{"Claude Opus 5.5 (1M context)", "claude", "Opus 5.5"},
		{"Opus 5.5 (1M context)", "claude", "Opus 5.5"},
		{"Sonnet 5.5 (200K Context Window)", "claude", "Sonnet 5.5"},
		{"claude-opus-5-5[1m]", "claude", "Opus 5.5"},
		{"claude-opus-5-5 (auto)", "claude", "Opus 5.5"},
		{"deepseek-v4.1-flash", "deepseek", "V4.1 Flash"},
		{"gpt-5.6-luna", "codex", "GPT-5.6 Luna"},
		{"claude-sonnet-4-5-20250929", "claude", "Sonnet 4.5"},
		{"auto/claude-sonnet-5-5", "claude", "Sonnet 5.5"},
		{"DeepSeek V4.1 Flash (1M context) (auto)", "deepseek", "V4.1 Flash"},
		{"Foo (context)", "codex", "Foo (context)"},
		{"Claude Opus 5.5 · routing group", "claude", "Opus 5.5"},
		{"Claude Sonnet 5.5 · routing group · jp", "claude", "Sonnet 5.5"},
		{"DeepSeek V4.1 Flash (1M context) · routing group · jp", "deepseek", "V4.1 Flash"},
		{"GPT-5.5 · routing group", "codex", "GPT-5.5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := modelShortName(tc.name, tc.brand); got != tc.want {
				t.Fatalf("modelShortName(%q, %q) = %q, want %q", tc.name, tc.brand, got, tc.want)
			}
		})
	}
}

// TestPrettifyKebabModelID 验证 kebab 模型 ID 的排版：8 位日期段丢弃、连续短数字段合成版本号、
// 缩写段全大写、v 版本段首字母大写、数字开头的段保持原样；含空格或无连字符的输入原样返回。
func TestPrettifyKebabModelID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"claude-opus-5-5", "Claude Opus 5.5"},
		{"claude-sonnet-4-5-20250929", "Claude Sonnet 4.5"},
		{"gpt-5.6-luna", "GPT-5.6 Luna"},
		{"glm-5.3-flash", "GLM-5.3 Flash"},
		{"deepseek-v4.1-flash", "Deepseek V4.1 Flash"},
		{"claude--opus", "Claude Opus"},
		{"Claude Opus 5.5", "Claude Opus 5.5"},
		{"DeepSeek V4.1 Flash", "DeepSeek V4.1 Flash"},
		{"Opus55", "Opus55"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := prettifyKebabModelID(tc.in); got != tc.want {
				t.Fatalf("prettifyKebabModelID(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestEffortSignalHeights 验证信号格高度从 4 线性升到 12，末格最高。
func TestEffortSignalHeights(t *testing.T) {
	if got := effortSignalHeights(1); got != nil {
		t.Fatalf("effortSignalHeights(1) = %v, want nil", got)
	}
	two := effortSignalHeights(2)
	if len(two) != 2 || two[0] != 4 || two[1] != 12 {
		t.Fatalf("effortSignalHeights(2) = %v, want [4 12]", two)
	}
	five := effortSignalHeights(5)
	if len(five) != 5 {
		t.Fatalf("effortSignalHeights(5) 长度 = %d, want 5", len(five))
	}
	for i := 1; i < len(five); i++ {
		if five[i] <= five[i-1] {
			t.Fatalf("effortSignalHeights(5) 未严格递增: %v", five)
		}
	}
	if five[0] != 4 || five[len(five)-1] != 12 {
		t.Fatalf("effortSignalHeights(5) = %v, want 首 4 末 12", five)
	}
}

// TestEffortSignalFilled 验证已点亮格数：未设置强度时回退默认档。
func TestEffortSignalFilled(t *testing.T) {
	efforts := []string{"low", "medium", "high", "xhigh", "max"}
	cases := []struct {
		name         string
		efforts      []string
		current, def string
		want         int
	}{
		{"空档位", nil, "medium", "", 0},
		{"当前档位", efforts, "high", "low", 3},
		{"未设置回退默认档", efforts, "", "xhigh", 4},
		{"都未设置回退中档", efforts, "", "", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := effortSignalFilled(tc.efforts, tc.current, tc.def); got != tc.want {
				t.Fatalf("effortSignalFilled(%v, %q, %q) = %d, want %d", tc.efforts, tc.current, tc.def, got, tc.want)
			}
		})
	}
}

// TestEffortSignalTip 验证悬停提示：五档示例、单档返回空串。
func TestEffortSignalTip(t *testing.T) {
	efforts := []string{"low", "medium", "high", "xhigh", "max"}
	if got, want := effortSignalTip(efforts, "medium", ""), "强度：中（最高：最大）"; got != want {
		t.Fatalf("effortSignalTip = %q, want %q", got, want)
	}
	if got := effortSignalTip([]string{"medium"}, "medium", ""); got != "" {
		t.Fatalf("effortSignalTip 单档 = %q, want 空串", got)
	}
}

// TestEffortSignalSampleColor 验证色标首尾取到首尾色。
func TestEffortSignalSampleColor(t *testing.T) {
	first, last := ui.RGB(10, 20, 30), ui.RGB(200, 210, 220)
	colors := []ui.Color{first, last}
	if got := effortSignalSampleColor(colors, 0, 5); got != first {
		t.Fatalf("首格取色 = %v, want %v", got, first)
	}
	if got := effortSignalSampleColor(colors, 4, 5); got != last {
		t.Fatalf("末格取色 = %v, want %v", got, last)
	}
	if got := effortSignalSampleColor([]ui.Color{first}, 3, 5); got != first {
		t.Fatalf("单色取色 = %v, want %v", got, first)
	}
}

// capsuleTestProviders 构造主代理 Claude、子代理 Pi/DeepSeek 的提供方列表。
func capsuleTestProviders() []ProviderInfo {
	return []ProviderInfo{
		{
			ID:        "claude",
			Name:      "Claude",
			Available: true,
			Models: []ModelInfo{
				{
					ID:            "claude-opus-5-5",
					Name:          "Claude Opus 5.5",
					IsDefault:     true,
					Efforts:       []string{"low", "medium", "high", "xhigh", "max"},
					DefaultEffort: "medium",
				},
			},
		},
		{
			ID:        "pi",
			Name:      "Pi",
			Available: true,
			Models: []ModelInfo{
				{ID: "deepseek-v4-1-flash", Name: "DeepSeek V4.1 Flash (auto)"},
			},
		},
	}
}

// capsuleTestProps 构造编组开启的模型胶囊属性：主代理 Claude Opus 5.5、子代理 DeepSeek V4.1 Flash。
func capsuleTestProps(providers []ProviderInfo) ModelPickerProps {
	return ModelPickerProps{
		Mode:         ModelPickerSwarm,
		SwarmOn:      true,
		KeyPrefix:    "t-capsule-",
		ScopeKey:     "t-capsule",
		State:        &ModelPickerState{},
		Providers:    providers,
		Selection:    ModelSelection{Provider: "claude", Model: "claude-opus-5-5", Effort: "medium"},
		Bees:         ModelSelection{Provider: "pi", Model: "deepseek-v4-1-flash"},
		CapsuleLabel: modelShortDisplayName(providers, "claude", "claude-opus-5-5"),
	}
}

// TestModelCapsuleShortNames 验证胶囊显示主代理与子代理短名，不再出现品牌全名与 (auto) 后缀。
func TestModelCapsuleShortNames(t *testing.T) {
	testenv.TempHome(t)
	providers := capsuleTestProviders()

	render := func(props ModelPickerProps) *ui.Tester {
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c).Width(420).Children(func() {
				renderModelPicker(c, props)
			})
		}, 460, 320)
		tt.Frame()
		return tt
	}

	tt := render(capsuleTestProps(providers))
	if !tt.HasText("Opus 5.5") || !tt.HasText("V4.1 Flash") {
		t.Fatalf("胶囊应显示两侧短名, got %v", tt.Texts())
	}
	if tt.HasText("Claude Opus 5.5") || tt.HasText("(auto)") {
		t.Fatalf("胶囊不应出现品牌全名或 (auto) 后缀, got %v", tt.Texts())
	}

	hidden := capsuleTestProps(providers)
	hidden.HideBeeName = true
	if tt := render(hidden); tt.HasText("V4.1 Flash") {
		t.Fatalf("窄宽度时应隐藏子代理短名, got %v", tt.Texts())
	}

	follow := capsuleTestProps(providers)
	follow.Bees = ModelSelection{}
	if tt := render(follow); tt.HasText("V4.1 Flash") {
		t.Fatalf("子代理跟随主代理时不应显示子代理短名, got %v", tt.Texts())
	}
}

// TestModelCapsuleFullNameTip 验证悬停提示在用途说明后补上模型全名与强度，
// 单档模型不带强度、base 为空时不产生开头换行。
func TestModelCapsuleFullNameTip(t *testing.T) {
	providers := capsuleTestProviders()
	sel := ModelSelection{Provider: "claude", Model: "claude-opus-5-5", Effort: "medium"}

	got := modelCapsuleFullNameTip("生成提交信息与会话标题的模型", providers, sel)
	for _, want := range []string{
		"生成提交信息与会话标题的模型",
		"当前模型：Claude Opus 5.5",
		"强度：中（最高：最大）",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("提示缺少 %q, got %q", want, got)
		}
	}

	single := ModelSelection{Provider: "pi", Model: "deepseek-v4-1-flash"}
	if got := modelCapsuleFullNameTip("说明", providers, single); strings.Contains(got, "强度：") {
		t.Fatalf("单档模型不应带强度, got %q", got)
	}

	empty := modelCapsuleFullNameTip("", providers, sel)
	if strings.HasPrefix(empty, "\n") {
		t.Fatalf("base 为空时不应以换行开头, got %q", empty)
	}
	if !strings.Contains(empty, "当前模型：Claude Opus 5.5") {
		t.Fatalf("base 为空时应只返回模型信息, got %q", empty)
	}
}

// TestModelCapsuleTwoLineFitsChin 验证双行模型胶囊的几何：两行行高够放下字号，
// 总高不超过发送按钮，胶囊不会把输入框底栏撑高。
func TestModelCapsuleTwoLineFitsChin(t *testing.T) {
	if !modelCapsuleTwoLine {
		t.Skip("胶囊当前为单行布局")
	}
	if modelCapsuleLeadLineHeight < FontSizeCapsuleText {
		t.Fatalf("主模型行高 %.1f 小于字号 %.1f，两行会重叠", modelCapsuleLeadLineHeight, FontSizeCapsuleText)
	}
	if modelPickerBeeLineHeight < FontSizeProjectName {
		t.Fatalf("子代理行高 %.1f 小于字号 %.1f，文字会溢出", modelPickerBeeLineHeight, FontSizeProjectName)
	}
	if total := modelCapsuleLeadLineHeight + modelCapsuleLineGap + modelPickerBeeLineHeight; total > ControlMD {
		t.Fatalf("双行胶囊总高 %.1f 超过发送按钮高度 %.1f", total, ControlMD)
	}
	if FontSizeCapsuleText >= TypeBody || FontSizeProjectName >= TypeBody {
		t.Fatalf("胶囊字号应小于正文字号 %.1f，实际 %.1f/%.1f", TypeBody, FontSizeCapsuleText, FontSizeProjectName)
	}
}
