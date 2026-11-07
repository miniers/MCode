package magpiecfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// codexTestPath 把 CODEX_HOME 指到临时目录，返回这个目录下的 config.toml。
func codexTestPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	return filepath.Join(dir, "config.toml")
}

// codexTestPlan 生成一份指向假网关的 codex 计划。
func codexTestPlan(t *testing.T, model string) Plan {
	t.Helper()
	plan, err := CodexPlan(CodexOptions{
		BaseURL: "https://magpie.example/v1/",
		APIKey:  "gw-key",
		Model:   model,
	})
	if err != nil {
		t.Fatalf("CodexPlan 失败: %v", err)
	}
	return plan
}

func codexTestDoc(t *testing.T, path string) map[string]any {
	t.Helper()
	doc, err := GetTOMLDocument(path)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", path, err)
	}
	return doc
}

func codexTestValue(t *testing.T, doc map[string]any, key string) any {
	t.Helper()
	v, ok := LookupTOMLValue(doc, key)
	if !ok {
		t.Fatalf("缺少键 %s", key)
	}
	return v
}

// 用户原有的 config.toml：注释、顶层键、另一个 provider 表和普通表都在。
const codexOriginalConfig = `# 用户自己的 codex 配置
model = "gpt-5-codex"
model_provider = "openai"

# 另一个 provider
[model_providers.other]
name = "Other"
base_url = "https://other.example/v1"

[other_table]
enabled = true
retries = 3
`

func TestCodexBaseURL(t *testing.T) {
	cases := map[string]string{
		"https://magpie.example/v1":                     "https://magpie.example/v1",
		"https://magpie.example/v1/":                    "https://magpie.example/v1",
		"https://magpie.example":                        "https://magpie.example/v1",
		"https://magpie.example/chat/completions":       "https://magpie.example/v1",
		" https://magpie.example/v1/chat/completions/ ": "https://magpie.example/v1",
	}
	for in, want := range cases {
		if got := codexBaseURL(in); got != want {
			t.Errorf("codexBaseURL(%q) = %q，想要 %q", in, got, want)
		}
	}
}

func TestCodexPlanRejectsEmptyOptions(t *testing.T) {
	if _, err := CodexPlan(CodexOptions{APIKey: "k", Model: "m"}); err == nil {
		t.Error("空 base URL 应报错")
	}
	if _, err := CodexPlan(CodexOptions{BaseURL: "https://m.example/v1", Model: "m"}); err == nil {
		t.Error("空 API Key 应报错")
	}
	if _, err := CodexPlan(CodexOptions{BaseURL: "https://m.example/v1", APIKey: "k"}); err == nil {
		t.Error("空模型应报错")
	}

	path := codexTestPath(t)
	plan := codexTestPlan(t, "gpt-6.1-terra")
	if plan.Agent != CodexAgent || len(plan.Files) != 1 || plan.Files[0].Format != FormatTOML || plan.Files[0].Path != path {
		t.Errorf("plan = %+v，想要 agent=%s format=toml path=%s", plan, CodexAgent, path)
	}
}

// TestCodexConnectWritesAndDisconnects 覆盖写入、读取、drift、重复写入与逐字节
// 还原：原文件里的注释、其它表和顶层键都必须原样回来。
func TestCodexConnectWritesAndDisconnects(t *testing.T) {
	path := codexTestPath(t)
	writeTestFile(t, path, codexOriginalConfig)
	dataDir := t.TempDir()
	plan := codexTestPlan(t, "gpt-6.1-terra")

	if err := Connect(dataDir, plan); err != nil {
		t.Fatalf("Connect 失败: %v", err)
	}
	doc := codexTestDoc(t, path)
	for key, want := range map[string]any{
		"model":                                                  "gpt-6.1-terra",
		"model_provider":                                         CodexProviderID,
		"model_providers.mcode-magpie.name":                      "Magpie",
		"model_providers.mcode-magpie.base_url":                  "https://magpie.example/v1",
		"model_providers.mcode-magpie.wire_api":                  "responses",
		"model_providers.mcode-magpie.requires_openai_auth":      true,
		"model_providers.mcode-magpie.experimental_bearer_token": "gw-key",
	} {
		if got := codexTestValue(t, doc, key); got != want {
			t.Errorf("%s = %#v，想要 %#v", key, got, want)
		}
	}
	if got := codexTestValue(t, doc, "model_providers.other.name"); got != "Other" {
		t.Errorf("其它 provider 表被改动: %#v", got)
	}
	if got := codexTestValue(t, doc, "other_table.retries"); got != int64(3) {
		t.Errorf("其它表被改动: %#v", got)
	}
	if st := Drift(dataDir, CodexAgent); st.State != StatusApplied {
		t.Fatalf("写入后 Drift = %+v，想要 applied", st)
	}

	before := readTestFile(t, path)
	if err := Connect(dataDir, plan); err != nil {
		t.Fatalf("重复 Connect 失败: %v", err)
	}
	if after := readTestFile(t, path); after != before {
		t.Errorf("重复 Connect 改动了文件:\n前=%q\n后=%q", before, after)
	}

	if err := Disconnect(dataDir, CodexAgent); err != nil {
		t.Fatalf("Disconnect 失败: %v", err)
	}
	if got := readTestFile(t, path); got != codexOriginalConfig {
		t.Fatalf("断开后未逐字节还原:\n得到 %q\n想要 %q", got, codexOriginalConfig)
	}
	assertNoState(t, dataDir)
}

// 原文件不存在时创建，断开成空文件后删除。
func TestCodexConnectCreatesAndRemovesFile(t *testing.T) {
	path := codexTestPath(t)
	dataDir := t.TempDir()
	plan := codexTestPlan(t, "gpt-6.1-terra")

	if err := Connect(dataDir, plan); err != nil {
		t.Fatalf("Connect 失败: %v", err)
	}
	raw := readTestFile(t, path)
	if i, j := strings.Index(raw, `model = "gpt-6.1-terra"`), strings.Index(raw, "[model_providers.mcode-magpie]"); i < 0 || j < 0 || i > j {
		t.Errorf("顶层键应在表头之前:\n%s", raw)
	}
	doc := codexTestDoc(t, path)
	if got := codexTestValue(t, doc, "model_providers.mcode-magpie.requires_openai_auth"); got != true {
		t.Errorf("requires_openai_auth = %#v，想要 true", got)
	}

	if err := Disconnect(dataDir, CodexAgent); err != nil {
		t.Fatalf("Disconnect 失败: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("断开后原不存在的 config.toml 应被删除，得到 %v", err)
	}
	assertNoState(t, dataDir)
}

// 用户已经有同名表时整表接管，断开后原有的键（包括 mcode 不认识的整数键与
// 布尔值）都回来，且布尔仍是 bool 而不是字面文本。
func TestCodexExistingTableRestored(t *testing.T) {
	path := codexTestPath(t)
	original := "[model_providers.mcode-magpie]\nname = \"Mine\"\nrequires_openai_auth = false\ncustom = 7\n"
	writeTestFile(t, path, original)
	dataDir := t.TempDir()

	if err := Connect(dataDir, codexTestPlan(t, "gpt-6.1-terra")); err != nil {
		t.Fatalf("Connect 失败: %v", err)
	}
	doc := codexTestDoc(t, path)
	if got := codexTestValue(t, doc, "model_providers.mcode-magpie.name"); got != "Magpie" {
		t.Errorf("接管后 name = %#v，想要 Magpie", got)
	}
	if got := codexTestValue(t, doc, "model_providers.mcode-magpie.requires_openai_auth"); got != true {
		t.Errorf("接管后 requires_openai_auth = %#v，想要 true", got)
	}
	if _, ok := LookupTOMLValue(doc, "model_providers.mcode-magpie.custom"); ok {
		t.Error("整表接管时 mcode 不认识的键应被替换")
	}

	if err := Disconnect(dataDir, CodexAgent); err != nil {
		t.Fatalf("Disconnect 失败: %v", err)
	}
	doc = codexTestDoc(t, path)
	if got := codexTestValue(t, doc, "model_providers.mcode-magpie.name"); got != "Mine" {
		t.Errorf("断开后 name = %#v，想要 Mine", got)
	}
	if got := codexTestValue(t, doc, "model_providers.mcode-magpie.custom"); got != int64(7) {
		t.Errorf("断开后 custom = %#v，想要 7", got)
	}
	if got := codexTestValue(t, doc, "model_providers.mcode-magpie.requires_openai_auth"); got != false {
		t.Errorf("断开后 requires_openai_auth = %#v，想要 bool false", got)
	}
	if _, ok := LookupTOMLValue(doc, "model"); ok {
		t.Error("断开后 model 应被删除")
	}
	assertNoState(t, dataDir)
}

func TestCodexDriftAndReapply(t *testing.T) {
	path := codexTestPath(t)
	writeTestFile(t, path, codexOriginalConfig)
	dataDir := t.TempDir()
	plan := codexTestPlan(t, "gpt-6.1-terra")

	if err := Connect(dataDir, plan); err != nil {
		t.Fatalf("Connect 失败: %v", err)
	}
	raw := readTestFile(t, path)
	edited := strings.Replace(raw, `model = "gpt-6.1-terra"`, `model = "user-picked"`, 1)
	if edited == raw {
		t.Fatal("测试前提失败：没找到写入的 model 行")
	}
	writeTestFile(t, path, edited)

	st := Drift(dataDir, CodexAgent)
	if st.State != StatusDrifted {
		t.Fatalf("手改后 Drift = %+v，想要 drifted", st)
	}
	if len(st.Drifted) != 1 || st.Drifted[0] != "model" {
		t.Errorf("Drifted = %v，想要 [model]", st.Drifted)
	}

	if err := Reapply(dataDir, plan); err != nil {
		t.Fatalf("Reapply 失败: %v", err)
	}
	if got := codexTestValue(t, codexTestDoc(t, path), "model"); got != "gpt-6.1-terra" {
		t.Errorf("重新写入后 model = %#v，想要 gpt-6.1-terra", got)
	}
	if st := Drift(dataDir, CodexAgent); st.State != StatusApplied {
		t.Errorf("重新写入后 Drift = %+v，想要 applied", st)
	}

	// 改表里的 base_url：整张表看成一个字段，Drifted 报表名。
	raw = readTestFile(t, path)
	edited = strings.Replace(raw, `base_url = "https://magpie.example/v1"`, `base_url = "https://evil.example/v1"`, 1)
	if edited == raw {
		t.Fatal("测试前提失败：没找到写入的 base_url 行")
	}
	writeTestFile(t, path, edited)
	st = Drift(dataDir, CodexAgent)
	if st.State != StatusDrifted {
		t.Fatalf("改表后 Drift = %+v，想要 drifted", st)
	}
	if len(st.Drifted) != 1 || st.Drifted[0] != "model_providers.mcode-magpie" {
		t.Errorf("Drifted = %v，想要表名", st.Drifted)
	}
	if err := Reapply(dataDir, plan); err != nil {
		t.Fatalf("Reapply 失败: %v", err)
	}
	if got := codexTestValue(t, codexTestDoc(t, path), "model_providers.mcode-magpie.base_url"); got != "https://magpie.example/v1" {
		t.Errorf("重新写入后 base_url = %#v，想要计划里的地址", got)
	}
	if st := Drift(dataDir, CodexAgent); st.State != StatusApplied {
		t.Errorf("重新写入后 Drift = %+v，想要 applied", st)
	}

	jsonPlan := Plan{Agent: CodexAgent, Files: []FilePlan{{Path: path, Format: FormatJSON, Fields: []Field{{Path: "model", Value: "x"}}}}}
	if err := Reapply(dataDir, jsonPlan); err == nil {
		t.Error("TOML 接管状态下应拒绝 JSON 计划")
	}
}

func TestCodexKeepKeepsWrittenConfig(t *testing.T) {
	path := codexTestPath(t)
	writeTestFile(t, path, codexOriginalConfig)
	dataDir := t.TempDir()
	if err := Connect(dataDir, codexTestPlan(t, "gpt-6.1-terra")); err != nil {
		t.Fatalf("Connect 失败: %v", err)
	}
	written := readTestFile(t, path)

	if err := Keep(dataDir, CodexAgent); err != nil {
		t.Fatalf("Keep 失败: %v", err)
	}
	if st := Drift(dataDir, CodexAgent); st.State != StatusKept {
		t.Errorf("Keep 后 Drift = %+v，想要 kept", st)
	}
	if err := Disconnect(dataDir, CodexAgent); err != nil {
		t.Fatalf("Disconnect 失败: %v", err)
	}
	if got := readTestFile(t, path); got != written {
		t.Errorf("Keep 后断开不应还原文件:\n得到 %q\n想要 %q", got, written)
	}
	assertNoState(t, dataDir)
}

func TestCodexInvalidTOMLRejected(t *testing.T) {
	path := codexTestPath(t)
	bad := "model = \"unterminated\n"
	writeTestFile(t, path, bad)
	dataDir := t.TempDir()

	err := Connect(dataDir, codexTestPlan(t, "gpt-6.1-terra"))
	if err == nil {
		t.Fatal("非法 TOML 应被拒绝")
	}
	if !strings.Contains(err.Error(), "不是有效的 TOML") {
		t.Errorf("错误信息 = %v，想要提到 TOML", err)
	}
	if got := readTestFile(t, path); got != bad {
		t.Errorf("非法 TOML 不应被改动，得到 %q", got)
	}
	assertNoState(t, dataDir)
}

// validatePlan 的 TOML 分支拒绝写不成 TOML 的值，接受标量、整数、浮点与整表。
func TestValidatePlanTOMLFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	rejected := []struct {
		name  string
		field Field
	}{
		{"对象标量", Field{Path: "model", Value: map[string]any{"a": 1}}},
		{"数组标量", Field{Path: "model", Value: []any{"a"}}},
		{"表值不是 map", Field{Path: "model_providers.x", TOMLTable: true, Value: "x"}},
		{"表内数组", Field{Path: "model_providers.x", TOMLTable: true, Value: map[string]any{"a": []any{1}}}},
	}
	for _, c := range rejected {
		plan := Plan{Agent: CodexAgent, Files: []FilePlan{{Path: path, Format: FormatTOML, Fields: []Field{c.field}}}}
		if err := validatePlan(plan); err == nil {
			t.Errorf("%s: 应被拒绝", c.name)
		}
	}

	ok := Plan{Agent: CodexAgent, Files: []FilePlan{{Path: path, Format: FormatTOML, Fields: []Field{
		{Path: "model", Value: "gpt-6.1-terra"},
		{Path: "count", Value: 3},
		{Path: "ratio", Value: 0.5},
		{Path: "flag", Value: true},
		{Path: "model_providers.x", TOMLTable: true, Value: map[string]any{"n": int64(1), "b": false, "f": 1.5}},
	}}}}
	if err := validatePlan(ok); err != nil {
		t.Errorf("合法的 TOML 计划被拒绝: %v", err)
	}
}

func TestCodexConfigPath(t *testing.T) {
	home := t.TempDir()
	origHome, origLookup := homeDir, lookupEnv
	t.Cleanup(func() {
		homeDir, lookupEnv = origHome, origLookup
	})

	homeDir = func() string { return home }
	lookupEnv = func(string) (string, bool) { return "", false }
	path, err := CodexConfigPath()
	if err != nil {
		t.Fatalf("CodexConfigPath 失败: %v", err)
	}
	if want := filepath.Join(home, ".codex", "config.toml"); path != want {
		t.Errorf("缺省路径 = %q，想要 %q", path, want)
	}

	codexHome := t.TempDir()
	lookupEnv = func(key string) (string, bool) {
		if key == "CODEX_HOME" {
			return codexHome, true
		}
		return "", false
	}
	path, err = CodexConfigPath()
	if err != nil {
		t.Fatalf("CodexConfigPath 失败: %v", err)
	}
	if want := filepath.Join(codexHome, "config.toml"); path != want {
		t.Errorf("CODEX_HOME 路径 = %q，想要 %q", path, want)
	}

	homeDir = func() string { return "" }
	lookupEnv = func(string) (string, bool) { return "", false }
	if _, err := CodexConfigPath(); err == nil {
		t.Error("主目录未知时应报错")
	}
}
