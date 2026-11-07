package magpiecfg

import (
	"path/filepath"
	"testing"
	"time"
)

// clineFileField 返回 cline 计划里某个文件、某个字段的值。
func clineFileField(t *testing.T, plan Plan, file, path string) (any, bool) {
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

func clineTestOptions(now time.Time) ClineOptions {
	return ClineOptions{
		BaseURL: "https://magpie.example/v1/",
		APIKey:  "gw-key",
		Main:    "glm-4.6",
		Models: []ClineModel{
			{ID: "glm-4.6", Name: "GLM 4.6", ContextWindow: 200_000, MaxTokens: 32_000, Vision: true, Reasoning: true},
			{ID: "glm-4.5", Name: "GLM 4.5"},
		},
		Now: now,
	}
}

// TestClinePlanFields 校验两份 JSON 的路径、格式与关键字段。
func TestClinePlanFields(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLINE_DATA_DIR", dataDir)
	now := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)

	plan, err := ClinePlan(clineTestOptions(now))
	if err != nil {
		t.Fatalf("ClinePlan: %v", err)
	}
	if plan.Agent != ClineAgent {
		t.Errorf("Agent = %q，想要 %q", plan.Agent, ClineAgent)
	}
	if len(plan.Files) != 2 {
		t.Fatalf("文件数 = %d，想要 2", len(plan.Files))
	}
	if got := plan.Files[0].Path; got != filepath.Join(dataDir, "settings", "providers.json") {
		t.Errorf("providers 路径 = %q", got)
	}
	if got := plan.Files[1].Path; got != filepath.Join(dataDir, "settings", "models.json") {
		t.Errorf("models 路径 = %q", got)
	}
	for _, fp := range plan.Files {
		if fp.Format != FormatJSON {
			t.Errorf("%s 的格式 = %q，想要 JSON", fp.Path, fp.Format)
		}
	}

	if got, _ := clineFileField(t, plan, "providers.json", "lastUsedProvider"); got != ClineProvider {
		t.Errorf("lastUsedProvider = %v，想要 %s", got, ClineProvider)
	}
	settingsPath := "providers." + ClineProvider
	entry, ok := clineFileField(t, plan, "providers.json", settingsPath)
	if !ok {
		t.Fatalf("providers.json 缺少 %s", settingsPath)
	}
	entryMap, _ := entry.(map[string]any)
	settings, _ := entryMap["settings"].(map[string]any)
	for key, want := range map[string]any{
		"provider": ClineProvider,
		"apiKey":   "gw-key",
		"model":    "glm-4.6",
		"baseUrl":  "https://magpie.example/v1",
	} {
		if got := settings[key]; got != want {
			t.Errorf("settings.%s = %v，想要 %v", key, got, want)
		}
	}
	headers, _ := settings["headers"].(map[string]string)
	if headers["User-Agent"] != "cline" {
		t.Errorf("headers = %#v，想要 User-Agent: cline", headers)
	}
	if entryMap["tokenSource"] != "manual" {
		t.Errorf("tokenSource = %v，想要 manual", entryMap["tokenSource"])
	}
	if got := entryMap["updatedAt"]; got != "2026-11-05T08:00:00.000Z" {
		t.Errorf("updatedAt = %v，想要固定时间", got)
	}

	modelEntry, ok := clineFileField(t, plan, "models.json", settingsPath)
	if !ok {
		t.Fatalf("models.json 缺少 %s", settingsPath)
	}
	modelMap, _ := modelEntry.(map[string]any)
	provider, _ := modelMap["provider"].(map[string]any)
	if provider["name"] != "Magpie" || provider["baseUrl"] != "https://magpie.example/v1" ||
		provider["defaultModelId"] != "glm-4.6" || provider["client"] != "openai-compatible" {
		t.Errorf("models.json provider = %#v", provider)
	}
	catalog, _ := modelMap["models"].(map[string]any)
	row, _ := catalog["glm-4.6"].(map[string]any)
	if row["name"] != "GLM 4.6" || row["contextWindow"] != 200_000 || row["supportsVision"] != true || row["supportsReasoning"] != true {
		t.Errorf("glm-4.6 元数据 = %#v", row)
	}
}

// TestClinePlanMainFallsBack 校验主模型不在模型列表里时取第一条。
func TestClinePlanMainFallsBack(t *testing.T) {
	t.Setenv("CLINE_DATA_DIR", t.TempDir())
	opts := clineTestOptions(time.Now())
	opts.Main = "missing"
	plan, err := ClinePlan(opts)
	if err != nil {
		t.Fatalf("ClinePlan: %v", err)
	}
	entry, _ := clineFileField(t, plan, "providers.json", "providers."+ClineProvider)
	settings, _ := entry.(map[string]any)["settings"].(map[string]any)
	if settings["model"] != "glm-4.6" {
		t.Errorf("model = %v，想要回落到第一条", settings["model"])
	}
}

// TestClinePlanRejectsEmpty 校验缺少必填项时报错。
func TestClinePlanRejectsEmpty(t *testing.T) {
	t.Setenv("CLINE_DATA_DIR", t.TempDir())
	if _, err := ClinePlan(ClineOptions{APIKey: "k", Models: []ClineModel{{ID: "m"}}}); err == nil {
		t.Error("空 BaseURL 应报错")
	}
	if _, err := ClinePlan(ClineOptions{BaseURL: "https://m.example/v1", Models: []ClineModel{{ID: "m"}}}); err == nil {
		t.Error("空 API Key 应报错")
	}
	if _, err := ClinePlan(ClineOptions{BaseURL: "https://m.example/v1", APIKey: "k"}); err == nil {
		t.Error("空模型应报错")
	}
	if _, err := ClinePlan(ClineOptions{BaseURL: "https://m.example/v1", APIKey: "k", Models: []ClineModel{{Name: "无 ID"}}}); err == nil {
		t.Error("空模型 ID 应报错")
	}
}

// TestClinePaths 校验 CLINE_DATA_DIR、~/.cline/data 与旧路径的优先级。
func TestClinePaths(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLINE_DATA_DIR", dataDir)
	providersPath, modelsPath, err := ClinePaths()
	if err != nil {
		t.Fatalf("ClinePaths: %v", err)
	}
	if providersPath != filepath.Join(dataDir, "settings", "providers.json") {
		t.Errorf("providers 路径 = %q，想要 CLINE_DATA_DIR 下", providersPath)
	}
	if modelsPath != filepath.Join(dataDir, "settings", "models.json") {
		t.Errorf("models 路径 = %q，想要与 providers 同目录", modelsPath)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLINE_DATA_DIR", "")
	legacy := filepath.Join(home, ".cline", "settings", "providers.json")
	writeTestFile(t, legacy, "{}")
	providersPath, modelsPath, err = ClinePaths()
	if err != nil {
		t.Fatalf("ClinePaths: %v", err)
	}
	if providersPath != legacy {
		t.Errorf("providers 路径 = %q，存在旧文件时想要 %q", providersPath, legacy)
	}
	if modelsPath != filepath.Join(home, ".cline", "settings", "models.json") {
		t.Errorf("models 路径 = %q，想要与旧 providers 同目录", modelsPath)
	}

	home = t.TempDir()
	t.Setenv("HOME", home)
	providersPath, _, err = ClinePaths()
	if err != nil {
		t.Fatalf("ClinePaths: %v", err)
	}
	if providersPath != filepath.Join(home, ".cline", "data", "settings", "providers.json") {
		t.Errorf("默认路径 = %q，想要 ~/.cline/data 下", providersPath)
	}
}
