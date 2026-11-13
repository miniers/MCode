package magpiecfg

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mcode/internal/providers/magpie"
	"mcode/internal/testenv"
)

// piTestEnv 把 HOME 与 PI_CODING_AGENT_DIR 指到临时目录，返回 pi 配置目录。
func piTestEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	dir := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	return dir
}

// piFileField 返回 pi 计划里某个文件、某个字段的值。
func piFileField(t *testing.T, plan Plan, file, path string) (any, bool) {
	t.Helper()
	for _, fp := range plan.Files {
		if filepath.Base(fp.Path) != file {
			continue
		}
		for _, f := range fp.Fields {
			if f.Path == path {
				return f.Value, true
			}
		}
	}
	return nil, false
}

// TestPiPaths 校验默认目录、PI_CODING_AGENT_DIR 覆盖与相对路径拒绝。
func TestPiPaths(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("PI_CODING_AGENT_DIR", "")

	modelsPath, settingsPath, err := PiPaths()
	if err != nil {
		t.Fatalf("PiPaths: %v", err)
	}
	if want := filepath.Join(home, ".pi", "agent", "models.json"); modelsPath != want {
		t.Errorf("models 路径 = %q，想要 %q", modelsPath, want)
	}
	if want := filepath.Join(home, ".pi", "agent", "settings.json"); settingsPath != want {
		t.Errorf("settings 路径 = %q，想要 %q", settingsPath, want)
	}

	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", "  "+dir+"  ")
	modelsPath, settingsPath, err = PiPaths()
	if err != nil {
		t.Fatalf("PiPaths 覆盖: %v", err)
	}
	if want := filepath.Join(dir, "models.json"); modelsPath != want {
		t.Errorf("覆盖后 models 路径 = %q，想要 %q", modelsPath, want)
	}
	if want := filepath.Join(dir, "settings.json"); settingsPath != want {
		t.Errorf("覆盖后 settings 路径 = %q，想要 %q", settingsPath, want)
	}

	t.Setenv("PI_CODING_AGENT_DIR", "relative/dir")
	if _, _, err := PiPaths(); err == nil {
		t.Error("相对 PI_CODING_AGENT_DIR 应报错")
	}
}

// TestPiPlanFields 校验两份 JSON 的路径、字段、模型缺省值与 provider 顺序。
func TestPiPlanFields(t *testing.T) {
	dir := piTestEnv(t)
	high := "high"

	plan, err := PiPlan(PiOptions{
		Providers: []magpie.PiProvider{
			{
				Provider: "jp",
				Endpoint: "jp-endpoint",
				BaseURL:  "https://magpie.example/v1/",
				APIKey:   "gw-key",
				Models: []magpie.Model{
					{
						ID:               "glm-4.6",
						Name:             "GLM 4.6",
						Reasoning:        true,
						Input:            []string{"text", "image"},
						ContextWindow:    200_000,
						MaxTokens:        32_000,
						Cost:             &magpie.Cost{Input: 0.6, Output: 2.2, CacheRead: 0.1, CacheWrite: 0.2},
						ThinkingLevelMap: map[string]*string{"high": &high},
					},
					{ID: "deepseek-r1"},
					{ID: " glm-4.6 "},
					{ID: " "},
				},
			},
			{
				Provider: "backup",
				BaseURL:  "https://backup.example/v1",
				APIKey:   "$secret",
				Models:   []magpie.Model{{ID: "acme/model-x"}},
			},
		},
		DefaultProvider: "backup",
		DefaultModel:    "acme/model-x",
	})
	if err != nil {
		t.Fatalf("PiPlan: %v", err)
	}
	if plan.Agent != PiAgent {
		t.Errorf("Agent = %q，想要 %q", plan.Agent, PiAgent)
	}
	if len(plan.Files) != 2 {
		t.Fatalf("文件数 = %d，想要 2", len(plan.Files))
	}
	if got := plan.Files[0].Path; got != filepath.Join(dir, "models.json") {
		t.Errorf("models 路径 = %q", got)
	}
	if got := plan.Files[1].Path; got != filepath.Join(dir, "settings.json") {
		t.Errorf("settings 路径 = %q", got)
	}
	for _, fp := range plan.Files {
		if fp.Format != FormatJSON {
			t.Errorf("%s 的 Format = %v，想要 JSON", fp.Path, fp.Format)
		}
	}

	jp, ok := piFileField(t, plan, "models.json", "providers.jp")
	if !ok {
		t.Fatal("models.json 缺少 providers.jp")
	}
	provider, ok := jp.(map[string]any)
	if !ok {
		t.Fatalf("providers.jp 类型 = %T", jp)
	}
	if got := provider["baseUrl"]; got != "https://magpie.example/v1" {
		t.Errorf("baseUrl = %v", got)
	}
	if got := provider["apiKey"]; got != "gw-key" {
		t.Errorf("apiKey = %v", got)
	}
	if got := provider["api"]; got != "openai-completions" {
		t.Errorf("api = %v", got)
	}
	models, ok := provider["models"].([]map[string]any)
	if !ok {
		t.Fatalf("models 类型 = %T", provider["models"])
	}
	if len(models) != 2 {
		t.Fatalf("模型数 = %d，想要 2（去重、跳过空 ID）", len(models))
	}
	first := models[0]
	if got := first["id"]; got != "glm-4.6" {
		t.Errorf("首个模型 id = %v", got)
	}
	if got := first["name"]; got != "GLM 4.6" {
		t.Errorf("name = %v", got)
	}
	if got := first["reasoning"]; got != true {
		t.Errorf("reasoning = %v", got)
	}
	if got := first["input"].([]string); !reflect.DeepEqual(got, []string{"text", "image"}) {
		t.Errorf("input = %v", got)
	}
	cost := first["cost"].(map[string]any)
	if got := cost["input"]; got != 0.6 {
		t.Errorf("cost.input = %v", got)
	}
	if got := cost["cacheWrite"]; got != 0.2 {
		t.Errorf("cost.cacheWrite = %v", got)
	}
	if got := first["contextWindow"]; got != 200_000 {
		t.Errorf("contextWindow = %v", got)
	}
	if got := first["maxTokens"]; got != 32_000 {
		t.Errorf("maxTokens = %v", got)
	}
	levels, ok := first["thinkingLevelMap"].(map[string]*string)
	if !ok || levels["high"] == nil || *levels["high"] != "high" {
		t.Errorf("thinkingLevelMap = %+v", first["thinkingLevelMap"])
	}
	compat := first["compat"].(map[string]any)
	if got := compat["sendSessionAffinityHeaders"]; got != true {
		t.Errorf("compat.sendSessionAffinityHeaders = %v", got)
	}
	if _, ok := compat["thinkingFormat"]; ok {
		t.Errorf("普通模型不应带 thinkingFormat: %+v", compat)
	}

	second := models[1]
	if got := second["name"]; got != "deepseek-r1" {
		t.Errorf("空 name 应回退 id，得到 %v", got)
	}
	if got := second["input"].([]string); !reflect.DeepEqual(got, []string{"text"}) {
		t.Errorf("空 input 应回退 [text]，得到 %v", got)
	}
	if got := second["contextWindow"]; got != piDefaultContextWindow {
		t.Errorf("contextWindow 默认 = %v", got)
	}
	if got := second["maxTokens"]; got != piDefaultMaxTokens {
		t.Errorf("maxTokens 默认 = %v", got)
	}
	cost = second["cost"].(map[string]any)
	for _, key := range []string{"input", "output", "cacheRead", "cacheWrite"} {
		if got := cost[key]; got != 0.0 {
			t.Errorf("无价格时 cost.%s = %v", key, got)
		}
	}
	compat = second["compat"].(map[string]any)
	if got := compat["thinkingFormat"]; got != "deepseek" {
		t.Errorf("deepseek thinkingFormat = %v", got)
	}
	if got := compat["requiresReasoningContentOnAssistantMessages"]; got != true {
		t.Errorf("deepseek requiresReasoningContentOnAssistantMessages = %v", got)
	}
	if _, ok := second["thinkingLevelMap"]; ok {
		t.Error("nil thinkingLevelMap 不应写入")
	}

	backup, ok := piFileField(t, plan, "models.json", "providers.backup")
	if !ok {
		t.Fatal("models.json 缺少 providers.backup")
	}
	if got := backup.(map[string]any)["apiKey"]; got != "$$secret" {
		t.Errorf("以 $ 开头的 key 应转义成 $$secret，得到 %v", got)
	}

	if got, _ := piFileField(t, plan, "settings.json", "defaultProvider"); got != "backup" {
		t.Errorf("defaultProvider = %v", got)
	}
	if got, _ := piFileField(t, plan, "settings.json", "defaultModel"); got != "acme/model-x" {
		t.Errorf("defaultModel = %v", got)
	}
}

// TestPiEscapeAPIKey 校验字面量 key 的转义规则。
func TestPiEscapeAPIKey(t *testing.T) {
	cases := map[string]string{
		"plain-key": "plain-key",
		"$ENV_KEY":  "$$ENV_KEY",
		"!cmd":      "$!cmd",
		"a$b":       "a$b",
	}
	for in, want := range cases {
		if got := piEscapeAPIKey(in); got != want {
			t.Errorf("piEscapeAPIKey(%q) = %q，想要 %q", in, got, want)
		}
	}
}

// TestPiPlanDefaultsAndValidation 校验默认模型回退与各类非法输入。
func TestPiPlanDefaultsAndValidation(t *testing.T) {
	piTestEnv(t)
	valid := func() []magpie.PiProvider {
		return []magpie.PiProvider{
			{Provider: "jp", BaseURL: "https://jp.example/v1", APIKey: "k1", Models: []magpie.Model{{ID: "a"}, {ID: "b"}}},
			{Provider: "backup", BaseURL: "https://backup.example/v1", APIKey: "k2", Models: []magpie.Model{{ID: "c"}}},
		}
	}

	cases := []struct {
		name            string
		opts            PiOptions
		wantProvider    string
		wantModel       string
		wantErrContains string
	}{
		{name: "默认有效", opts: PiOptions{Providers: valid(), DefaultProvider: "jp", DefaultModel: "b"}, wantProvider: "jp", wantModel: "b"},
		{name: "默认模型不在勾选", opts: PiOptions{Providers: valid(), DefaultProvider: "jp", DefaultModel: "z"}, wantProvider: "jp", wantModel: "a"},
		{name: "默认 provider 不存在", opts: PiOptions{Providers: valid(), DefaultProvider: "ghost", DefaultModel: "a"}, wantProvider: "jp", wantModel: "a"},
		{name: "没有 provider", opts: PiOptions{}, wantErrContains: "没有可写的 provider"},
		{name: "空 provider ID", opts: PiOptions{Providers: []magpie.PiProvider{{BaseURL: "https://x/v1", APIKey: "k", Models: []magpie.Model{{ID: "a"}}}}}, wantErrContains: "provider ID 为空"},
		{name: "空 URL", opts: PiOptions{Providers: []magpie.PiProvider{{Provider: "p", APIKey: "k", Models: []magpie.Model{{ID: "a"}}}}}, wantErrContains: "BaseURL 为空"},
		{name: "空 key", opts: PiOptions{Providers: []magpie.PiProvider{{Provider: "p", BaseURL: "https://x/v1", Models: []magpie.Model{{ID: "a"}}}}}, wantErrContains: "API Key 为空"},
		{name: "重复 provider", opts: PiOptions{Providers: []magpie.PiProvider{{Provider: "p", BaseURL: "https://x/v1", APIKey: "k", Models: []magpie.Model{{ID: "a"}}}, {Provider: "p", BaseURL: "https://y/v1", APIKey: "k", Models: []magpie.Model{{ID: "b"}}}}}, wantErrContains: "重复"},
		{name: "没有模型", opts: PiOptions{Providers: []magpie.PiProvider{{Provider: "p", BaseURL: "https://x/v1", APIKey: "k", Models: []magpie.Model{{ID: " "}}}}}, wantErrContains: "没有可写的模型"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := PiPlan(tc.opts)
			if tc.wantErrContains != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Fatalf("err = %v，想要包含 %q", err, tc.wantErrContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("PiPlan: %v", err)
			}
			if got, _ := piFileField(t, plan, "settings.json", "defaultProvider"); got != tc.wantProvider {
				t.Errorf("defaultProvider = %v，想要 %q", got, tc.wantProvider)
			}
			if got, _ := piFileField(t, plan, "settings.json", "defaultModel"); got != tc.wantModel {
				t.Errorf("defaultModel = %v，想要 %q", got, tc.wantModel)
			}
		})
	}
}

func piTestPlan(t *testing.T) Plan {
	t.Helper()
	plan, err := PiPlan(PiOptions{
		Providers: []magpie.PiProvider{{
			Provider: "jp",
			BaseURL:  "https://magpie.example/v1",
			APIKey:   "gw-key",
			Models:   []magpie.Model{{ID: "glm-4.6", Name: "GLM 4.6"}, {ID: "glm-4.5"}},
		}},
		DefaultProvider: "jp",
		DefaultModel:    "glm-4.6",
	})
	if err != nil {
		t.Fatalf("PiPlan: %v", err)
	}
	return plan
}

// TestPiConnectRestore 校验写入后保留 pi 原有配置，切回时恢复原文件。
func TestPiConnectRestore(t *testing.T) {
	dir := piTestEnv(t)
	modelsPath := filepath.Join(dir, "models.json")
	settingsPath := filepath.Join(dir, "settings.json")
	modelsOrig := `{"providers":{"anthropic":{"baseUrl":"https://api.anthropic.com","apiKey":"x","models":[]}},"version":1}`
	settingsOrig := `{"defaultProvider":"anthropic","defaultModel":"claude-sonnet-4-5","theme":"dark"}`
	writeTestFile(t, modelsPath, modelsOrig)
	writeTestFile(t, settingsPath, settingsOrig)

	plan := piTestPlan(t)
	dataDir := t.TempDir()
	if err := Connect(dataDir, plan); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if got, ok := GetJSON(modelsPath, "providers.anthropic.baseUrl"); !ok || got != "https://api.anthropic.com" {
		t.Errorf("原有 anthropic provider 被覆盖: %q %v", got, ok)
	}
	if got, ok := GetJSON(modelsPath, "providers.jp.baseUrl"); !ok || got != "https://magpie.example/v1" {
		t.Errorf("providers.jp.baseUrl = %q %v", got, ok)
	}
	if got, ok := GetJSON(settingsPath, "defaultProvider"); !ok || got != "jp" {
		t.Errorf("defaultProvider = %q %v", got, ok)
	}
	if got, ok := GetJSON(settingsPath, "defaultModel"); !ok || got != "glm-4.6" {
		t.Errorf("defaultModel = %q %v", got, ok)
	}
	if got, ok := GetJSON(settingsPath, "theme"); !ok || got != "dark" {
		t.Errorf("原有 theme 被覆盖: %q %v", got, ok)
	}
	if state := Drift(dataDir, PiAgent); state.State != StatusApplied {
		t.Errorf("Drift 状态 = %q，想要 applied", state.State)
	}

	if err := Disconnect(dataDir, PiAgent); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if got := readTestFile(t, modelsPath); got != modelsOrig {
		t.Errorf("models.json 未恢复：\n%s", got)
	}
	if got := readTestFile(t, settingsPath); got != settingsOrig {
		t.Errorf("settings.json 未恢复：\n%s", got)
	}
}

// TestPiDriftReapplyKeep 校验被改动的 default* 能被 Reapply 重写，Keep 后切回仍保留用户改动。
func TestPiDriftReapplyKeep(t *testing.T) {
	dir := piTestEnv(t)
	dataDir := t.TempDir()
	modelsPath := filepath.Join(dir, "models.json")
	settingsPath := filepath.Join(dir, "settings.json")
	settingsOrig := `{"defaultProvider":"anthropic","defaultModel":"claude-sonnet-4-5"}`
	writeTestFile(t, modelsPath, `{}`)
	writeTestFile(t, settingsPath, settingsOrig)

	plan := piTestPlan(t)
	if err := Connect(dataDir, plan); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := SetJSON(settingsPath, KV{Path: "defaultModel", Value: "user-picked"}); err != nil {
		t.Fatalf("改默认模型: %v", err)
	}
	if state := Drift(dataDir, PiAgent); state.State != StatusDrifted {
		t.Fatalf("手改后状态 = %q，想要 drifted", state.State)
	}
	if err := Reapply(dataDir, plan); err != nil {
		t.Fatalf("Reapply: %v", err)
	}
	if got, _ := GetJSON(settingsPath, "defaultModel"); got != "glm-4.6" {
		t.Errorf("Reapply 后 defaultModel = %q", got)
	}

	if err := SetJSON(settingsPath, KV{Path: "defaultModel", Value: "user-picked"}); err != nil {
		t.Fatalf("再改默认模型: %v", err)
	}
	if err := Keep(dataDir, PiAgent); err != nil {
		t.Fatalf("Keep: %v", err)
	}
	if err := Disconnect(dataDir, PiAgent); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if got := readTestFile(t, settingsPath); got == settingsOrig {
		t.Error("Keep 后切回不应恢复原文件")
	}
	if got, _ := GetJSON(settingsPath, "defaultModel"); got != "user-picked" {
		t.Errorf("Keep 后 defaultModel = %q，想要用户改动", got)
	}
}
