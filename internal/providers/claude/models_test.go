package claude

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"mcode/internal/harness"
	"mcode/internal/providers/modelcache"
)

func TestParseHandshakeModels(t *testing.T) {
	line := []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"handshake","response":{"models":[{"value":"default","displayName":"Default (recommended)","description":"Use the CLI default model","supportedEffortLevels":["low","high"],"supportsFastMode":true},{"value":"claude-opus-4-6","description":"Most capable","supportsFastMode":false},{"displayName":"没有 value 的行"}]}}}`)

	got, ok := parseHandshakeModels(line)
	if !ok {
		t.Fatal("parseHandshakeModels() ok = false, want true")
	}
	want := []Model{
		{ID: "default", Name: "Default (recommended)", Description: "Use the CLI default model", Efforts: []string{"low", "high"}, SupportsFast: true},
		// displayName 缺失的条目用 value 作为名字；缺失 value 的条目被跳过。
		{ID: "claude-opus-4-6", Name: "claude-opus-4-6", Description: "Most capable"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseHandshakeModels() = %+v, want %+v", got, want)
	}
}

func TestParseHandshakeModelsIgnoresOtherLines(t *testing.T) {
	for _, line := range []string{
		"",
		`not json`,
		`{"type":"control_response","response":{"request_id":"other","response":{"models":[{"value":"x"}]}}}`,
		`{"type":"assistant","response":{"request_id":"handshake","response":{"models":[{"value":"x"}]}}}`,
	} {
		if models, ok := parseHandshakeModels([]byte(line)); ok {
			t.Errorf("parseHandshakeModels(%q) = %+v, true, want false", line, models)
		}
	}
}

func TestParseHandshakeModelsCapabilityFlags(t *testing.T) {
	line := []byte(`{"type":"control_response","response":{"request_id":"handshake","response":{"models":[{"value":"default","resolvedModel":"claude-opus-4-6","supportsAutoMode":false,"supportsThinkingOff":true},{"value":"claude-sonnet-4-6"}]}}}`)

	got, ok := parseHandshakeModels(line)
	if !ok {
		t.Fatal("parseHandshakeModels() ok = false, want true")
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ResolvedModel != "claude-opus-4-6" {
		t.Errorf("ResolvedModel = %q, want claude-opus-4-6", got[0].ResolvedModel)
	}
	if got[0].SupportsAutoMode == nil || *got[0].SupportsAutoMode {
		t.Errorf("SupportsAutoMode = %v, want 指向 false", got[0].SupportsAutoMode)
	}
	if got[0].SupportsThinkingOff == nil || !*got[0].SupportsThinkingOff {
		t.Errorf("SupportsThinkingOff = %v, want 指向 true", got[0].SupportsThinkingOff)
	}
	// 旧版 CLI 没报这两个字段：保持 nil，供调用方区分“目录没给”与明确的 false。
	if got[1].SupportsAutoMode != nil || got[1].SupportsThinkingOff != nil {
		t.Errorf("缺失字段应为 nil: auto=%v thinkingOff=%v", got[1].SupportsAutoMode, got[1].SupportsThinkingOff)
	}
}

// withModelsForTest 临时替换模型目录读取入口，让测试不真去起握手进程；models 为 nil 即目录未加载。
func withModelsForTest(t *testing.T, models []Model) {
	t.Helper()
	old := modelsForLookup
	modelsForLookup = func() []Model { return models }
	t.Cleanup(func() { modelsForLookup = old })
}

// boolFlag 返回指向 v 的指针，便于在表里写 Model 能力字段。
func boolFlag(v bool) *bool { return &v }

func TestAutoModeSupported(t *testing.T) {
	dir := []Model{
		{ID: "default", ResolvedModel: "claude-opus-4-6", SupportsAutoMode: boolFlag(true)},
		{ID: "claude-sonnet-4-6", SupportsAutoMode: boolFlag(false)},
		{ID: "claude-haiku-4-5", ResolvedModel: "claude-haiku-alias", SupportsAutoMode: boolFlag(true)},
		{ID: "claude-legacy"},
	}
	tests := []struct {
		name          string
		models        []Model
		model         string
		wantSupported bool
		wantKnown     bool
	}{
		{name: "目录未加载", models: nil, model: "claude-sonnet-4-6"},
		{name: "目录为空切片", models: []Model{}, model: "default"},
		{name: "空模型名按默认模型判断", models: dir, model: "", wantSupported: true, wantKnown: true},
		{name: "default 按默认模型判断", models: dir, model: "default", wantSupported: true, wantKnown: true},
		{name: "按 value 匹配支持", models: dir, model: "claude-haiku-4-5", wantSupported: true, wantKnown: true},
		{name: "按 value 匹配不支持", models: dir, model: "claude-sonnet-4-6", wantKnown: true},
		{name: "按 resolvedModel 匹配", models: dir, model: "claude-opus-4-6", wantSupported: true, wantKnown: true},
		{name: "resolvedModel 别名匹配", models: dir, model: "claude-haiku-alias", wantSupported: true, wantKnown: true},
		{name: "条目没报该字段算未知", models: dir, model: "claude-legacy"},
		{name: "目录里没有该模型", models: dir, model: "claude-unknown"},
		{name: "默认条目没报该字段算未知", models: []Model{{ID: "default"}}, model: "default"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withModelsForTest(t, tc.models)
			supported, known := autoModeSupported(tc.model)
			if supported != tc.wantSupported || known != tc.wantKnown {
				t.Errorf("autoModeSupported(%q) = (%v, %v), want (%v, %v)", tc.model, supported, known, tc.wantSupported, tc.wantKnown)
			}
		})
	}
}

func TestPermissionModeForDowngradesAutoByModel(t *testing.T) {
	dir := []Model{
		{ID: "default", SupportsAutoMode: boolFlag(false)},
		{ID: "claude-auto", SupportsAutoMode: boolFlag(true)},
		{ID: "claude-no-auto", SupportsAutoMode: boolFlag(false)},
		{ID: "claude-legacy"},
	}
	tests := []struct {
		name   string
		models []Model
		turn   harness.Turn
		want   string
	}{
		{name: "支持时保持 auto", models: dir, turn: harness.Turn{Mode: harness.ModeAuto, Model: "claude-auto"}, want: "auto"},
		{name: "不支持时降级为 acceptEdits", models: dir, turn: harness.Turn{Mode: harness.ModeAuto, Model: "claude-no-auto"}, want: "acceptEdits"},
		{name: "默认模型不支持时降级", models: dir, turn: harness.Turn{Mode: harness.ModeAuto}, want: "acceptEdits"},
		{name: "目录未加载时保持 auto", models: nil, turn: harness.Turn{Mode: harness.ModeAuto, Model: "claude-no-auto"}, want: "auto"},
		{name: "目录里没该模型时保持 auto", models: dir, turn: harness.Turn{Mode: harness.ModeAuto, Model: "claude-unknown"}, want: "auto"},
		{name: "目录没报该字段时保持 auto", models: dir, turn: harness.Turn{Mode: harness.ModeAuto, Model: "claude-legacy"}, want: "auto"},
		{name: "Plan交互模式仍不降级到 acceptEdits", models: dir, turn: harness.Turn{Mode: harness.ModeAuto, Model: "claude-no-auto", InteractionMode: "plan"}, want: "plan"},
		{name: "其他模式不受影响", models: dir, turn: harness.Turn{Mode: harness.ModeAutoAcceptEdits, Model: "claude-no-auto"}, want: "acceptEdits"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withModelsForTest(t, tc.models)
			if got := permissionModeFor(tc.turn); got != tc.want {
				t.Errorf("permissionModeFor() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildArgsDowngradesAutoWhenModelLacksSupport(t *testing.T) {
	withModelsForTest(t, []Model{{ID: "claude-no-auto", SupportsAutoMode: boolFlag(false)}})
	args := buildArgs(harness.Turn{Mode: harness.ModeAuto, Model: "claude-no-auto"})
	perm := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--permission-mode" {
			perm = args[i+1]
		}
	}
	if perm != "acceptEdits" {
		t.Errorf("--permission-mode = %q, want acceptEdits; args = %v", perm, args)
	}
}

// 找不到 CLI 时错误必须能被 errors.Is 认成 ErrNotInstalled，界面据此给出“未安装”文案。
func TestFetchModelsWithBinaryNotInstalled(t *testing.T) {
	_, err := fetchModelsWithBinary(context.Background(), "/nonexistent/mcode-claude-test")
	if !errors.Is(err, modelcache.ErrNotInstalled) {
		t.Fatalf("error = %v, want ErrNotInstalled", err)
	}
}

func withCatalogSnapshot(t *testing.T, snap modelcache.Snapshot[Model]) {
	t.Helper()
	old := catalogSnapshot
	catalogSnapshot = func() modelcache.Snapshot[Model] { return snap }
	t.Cleanup(func() { catalogSnapshot = old })
}

func withLoginPresent(t *testing.T, present bool) {
	t.Helper()
	old := loginPresent
	loginPresent = func() bool { return present }
	t.Cleanup(func() { loginPresent = old })
}

func TestCatalogNotice(t *testing.T) {
	notInstalled := errors.Join(modelcache.ErrNotInstalled, errors.New("lookpath failed"))
	cases := []struct {
		name  string
		snap  modelcache.Snapshot[Model]
		login bool
		want  string
	}{
		{"目录可用", modelcache.Snapshot[Model]{Models: []Model{{ID: "opus", Name: "Opus"}}}, false, ""},
		{"探测中", modelcache.Snapshot[Model]{Probing: true}, true, "正在探测 Claude Code 模型…"},
		{"未安装", modelcache.Snapshot[Model]{Err: notInstalled}, false, "未安装 Claude Code CLI"},
		{"未登录", modelcache.Snapshot[Model]{Err: errors.New("handshake failed")}, false, "未登录 Claude Code"},
		{"其他失败", modelcache.Snapshot[Model]{Err: errors.New("handshake failed")}, true, "探测 Claude Code 模型失败"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withCatalogSnapshot(t, tc.snap)
			withLoginPresent(t, tc.login)
			if got := CatalogNotice(); got != tc.want {
				t.Errorf("CatalogNotice() = %q, want %q", got, tc.want)
			}
		})
	}
}
