package magpiecfg

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const claudeOriginal = `{
  // 用户自己的注释
  "env": {
    "FOO": "bar",
    "ANTHROPIC_MODEL": "stale-model"
  },
  "model": "user-model"
}
`

// claudeTestEnv 准备一个独立的 home 与数据目录，返回数据目录和默认的
// settings.json 路径。
func claudeTestEnv(t *testing.T) (dataDir, settingsPath string) {
	t.Helper()
	home := withClaudeSandbox(t)
	return t.TempDir(), filepath.Join(home, ".claude", "settings.json")
}

func testClaudePlan(path string) Plan {
	return Plan{
		Agent: ClaudeAgent,
		Files: []FilePlan{{
			Path:   path,
			Format: FormatJSON,
			Fields: []Field{
				{Path: "env.ANTHROPIC_BASE_URL", Value: "https://magpie.test"},
				{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: "sk-test"},
				{Path: "model", Value: "gateway-model"},
				{Path: "env.NO_PROXY", Value: "localhost,127.0.0.1,::1"},
				{Path: "env.ANTHROPIC_MODEL", Delete: true},
			},
		}},
	}
}

func writeSettings(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建配置目录: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写配置: %v", err)
	}
}

func readSettings(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读配置: %v", err)
	}
	return string(b)
}

func setSetting(t *testing.T, path, key, value string) {
	t.Helper()
	if err := SetJSON(path, KV{Path: key, Value: value}); err != nil {
		t.Fatalf("改配置 %s: %v", key, err)
	}
}

func mustValue(t *testing.T, path, key, want string) {
	t.Helper()
	got, ok := GetJSON(path, key)
	if !ok {
		t.Fatalf("%s 应该存在，实际不存在", key)
	}
	if got != want {
		t.Fatalf("%s = %q，想要 %q", key, got, want)
	}
}

func mustAbsent(t *testing.T, path, key string) {
	t.Helper()
	if got, ok := GetJSON(path, key); ok {
		t.Fatalf("%s 应该不存在，实际是 %q", key, got)
	}
}

func assertNoState(t *testing.T, dataDir string) {
	t.Helper()
	for _, path := range []string{stashPath(dataDir, ClaudeAgent), appliedPath(dataDir, ClaudeAgent)} {
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("状态文件 %s 应该已被删除，err = %v", path, err)
		}
	}
}

func requireValidJSONC(t *testing.T, path string) {
	t.Helper()
	raw := readSettings(t, path)
	if !ValidJSONC([]byte(raw)) {
		t.Fatalf("%s 应该仍是有效的 JSONC：\n%s", path, raw)
	}
}

func TestConnectDriftReapplyDisconnect(t *testing.T) {
	dataDir, path := claudeTestEnv(t)
	writeSettings(t, path, claudeOriginal)
	plan := testClaudePlan(path)

	if err := Connect(dataDir, plan); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	requireValidJSONC(t, path)
	mustValue(t, path, "env.FOO", "bar")
	mustValue(t, path, "env.ANTHROPIC_BASE_URL", "https://magpie.test")
	mustValue(t, path, "env.ANTHROPIC_AUTH_TOKEN", "sk-test")
	mustValue(t, path, "env.NO_PROXY", "localhost,127.0.0.1,::1")
	mustValue(t, path, "model", "gateway-model")
	mustAbsent(t, path, "env.ANTHROPIC_MODEL")
	if raw := readSettings(t, path); !strings.Contains(raw, "用户自己的注释") {
		t.Fatalf("注释被删掉了：\n%s", raw)
	}

	status := Drift(dataDir, ClaudeAgent)
	if status.State != StatusApplied {
		t.Fatalf("Drift = %+v，想要 applied", status)
	}
	if status.Path != path {
		t.Fatalf("Drift.Path = %q，想要 %q", status.Path, path)
	}

	// 用户手改 model：drift 应该报告它。
	setSetting(t, path, "model", "user-edited")
	status = Drift(dataDir, ClaudeAgent)
	if status.State != StatusDrifted {
		t.Fatalf("手改后 Drift = %+v，想要 drifted", status)
	}
	if !slices.Contains(status.Drifted, "model") {
		t.Fatalf("Drifted = %v，应该包含 model", status.Drifted)
	}

	// Reapply 把我们写过的值恢复回去，且不把用户改的值记成原值。
	if err := Reapply(dataDir, plan); err != nil {
		t.Fatalf("Reapply: %v", err)
	}
	mustValue(t, path, "model", "gateway-model")
	if status := Drift(dataDir, ClaudeAgent); status.State != StatusApplied {
		t.Fatalf("Reapply 后 Drift = %+v，想要 applied", status)
	}

	if err := Disconnect(dataDir, ClaudeAgent); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	mustValue(t, path, "model", "user-model")
	mustValue(t, path, "env.ANTHROPIC_MODEL", "stale-model")
	mustValue(t, path, "env.FOO", "bar")
	mustAbsent(t, path, "env.ANTHROPIC_BASE_URL")
	mustAbsent(t, path, "env.ANTHROPIC_AUTH_TOKEN")
	mustAbsent(t, path, "env.NO_PROXY")
	if raw := readSettings(t, path); !strings.Contains(raw, "用户自己的注释") {
		t.Fatalf("恢复后注释被删掉了：\n%s", raw)
	}
	requireValidJSONC(t, path)
	assertNoState(t, dataDir)
}

// TestReapplyAfterForeignEditKeepsUserKeys 校验用户在我们管理的键之外手改了
// 配置文件、随后又点重新写入时，断开不能按接管前的原文整份写回：那个键不是
// 我们管的，整份写回会抹掉用户的改动，只能退回字段级恢复。
func TestReapplyAfterForeignEditKeepsUserKeys(t *testing.T) {
	dataDir, path := claudeTestEnv(t)
	writeSettings(t, path, claudeOriginal)
	plan := testClaudePlan(path)
	if err := Connect(dataDir, plan); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// 用户同时改了我们管理的键和一个我们不管的键。
	setSetting(t, path, "model", "user-hand-model")
	setSetting(t, path, "env.MY_OWN", "keep-me")
	if status := Drift(dataDir, ClaudeAgent); status.State != StatusDrifted {
		t.Fatalf("Drift = %+v，想要 drifted", status)
	}

	if err := Reapply(dataDir, plan); err != nil {
		t.Fatalf("Reapply: %v", err)
	}
	mustValue(t, path, "model", "gateway-model")

	if err := Disconnect(dataDir, ClaudeAgent); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	// 用户改的那个键必须保留，我们写的字段仍按 stash 恢复。
	mustValue(t, path, "env.MY_OWN", "keep-me")
	mustValue(t, path, "model", "user-model")
	mustAbsent(t, path, "env.ANTHROPIC_BASE_URL")
	mustAbsent(t, path, "env.ANTHROPIC_AUTH_TOKEN")
	assertNoState(t, dataDir)
}

func TestDisconnectKeepsHandEditedField(t *testing.T) {
	dataDir, path := claudeTestEnv(t)
	writeSettings(t, path, claudeOriginal)
	if err := Connect(dataDir, testClaudePlan(path)); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	setSetting(t, path, "model", "hand-model")

	if err := Disconnect(dataDir, ClaudeAgent); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	mustValue(t, path, "model", "hand-model")
	mustAbsent(t, path, "env.ANTHROPIC_BASE_URL")
	mustValue(t, path, "env.ANTHROPIC_MODEL", "stale-model")
	assertNoState(t, dataDir)
}

func TestDisconnectRemovesFileWeCreated(t *testing.T) {
	dataDir, path := claudeTestEnv(t)
	if err := Connect(dataDir, testClaudePlan(path)); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Connect 应该创建 %s: %v", path, err)
	}
	if err := Disconnect(dataDir, ClaudeAgent); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s 恢复后只剩空对象，应该被删除，err = %v", path, err)
	}
	assertNoState(t, dataDir)
}

func TestConnectAgainKeepsOriginalValues(t *testing.T) {
	dataDir, path := claudeTestEnv(t)
	writeSettings(t, path, claudeOriginal)
	plan := testClaudePlan(path)
	if err := Connect(dataDir, plan); err != nil {
		t.Fatalf("第一次 Connect: %v", err)
	}
	if err := Connect(dataDir, plan); err != nil {
		t.Fatalf("第二次 Connect: %v", err)
	}
	if err := Disconnect(dataDir, ClaudeAgent); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	// 第二次 connect 时当前值就是我们的值，不能把它当成原值。
	mustValue(t, path, "model", "user-model")
	mustValue(t, path, "env.ANTHROPIC_MODEL", "stale-model")
	assertNoState(t, dataDir)
}

func TestKeepThenDisconnectLeavesConfigAlone(t *testing.T) {
	dataDir, path := claudeTestEnv(t)
	writeSettings(t, path, claudeOriginal)
	if err := Connect(dataDir, testClaudePlan(path)); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	before := readSettings(t, path)
	if err := Keep(dataDir, ClaudeAgent); err != nil {
		t.Fatalf("Keep: %v", err)
	}
	if status := Drift(dataDir, ClaudeAgent); status.State != StatusKept {
		t.Fatalf("Keep 后 Drift = %+v，想要 kept", status)
	}
	if err := Disconnect(dataDir, ClaudeAgent); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if after := readSettings(t, path); after != before {
		t.Fatalf("Kept 状态下 Disconnect 不应改配置：\n前：%s\n后：%s", before, after)
	}
	assertNoState(t, dataDir)
}

func TestConnectRejectsBrokenJSONC(t *testing.T) {
	dataDir, path := claudeTestEnv(t)
	broken := "{\n  \"model\": \"x\",\n  // 注释没有闭合\n"
	writeSettings(t, path, broken)

	if err := Connect(dataDir, testClaudePlan(path)); err == nil {
		t.Fatal("Connect 应该拒绝无法解析的 JSONC")
	}
	if got := readSettings(t, path); got != broken {
		t.Fatalf("Connect 失败后文件被改动了：\n%s", got)
	}
	assertNoState(t, dataDir)
}

func TestDriftNoneAndMissing(t *testing.T) {
	dataDir, path := claudeTestEnv(t)
	if status := Drift(dataDir, ClaudeAgent); status.State != StatusNone {
		t.Fatalf("未接管时 Drift = %+v，想要 none", status)
	}
	writeSettings(t, path, claudeOriginal)
	if err := Connect(dataDir, testClaudePlan(path)); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("删除配置文件: %v", err)
	}
	if status := Drift(dataDir, ClaudeAgent); status.State != StatusMissing {
		t.Fatalf("文件被删后 Drift = %+v，想要 missing", status)
	}
}

const pickerOriginal = `{
  // 用户自己的注释
  "env": {
    "FOO": "bar"
  },
  "model": "user-model",
  "modelPicker": {
    "replaceBuiltInOptions": true,
    "options": [
      {
        "model": "legacy-model",
        "label": "Legacy",
        "description": "用户自己的选项"
      }
    ]
  }
}
`

const (
	pickerOriginalRaw = `{"options":[{"description":"用户自己的选项","label":"Legacy","model":"legacy-model"}],"replaceBuiltInOptions":true}`
	pickerWrittenRaw  = `{"options":[{"description":"via magpie","label":"Sonnet","model":"claude-sonnet-4-5"}],"replaceBuiltInOptions":true}`
)

func pickerValue() map[string]any {
	return map[string]any{
		"replaceBuiltInOptions": true,
		"options": []any{
			map[string]any{
				"model":       "claude-sonnet-4-5",
				"label":       "Sonnet",
				"description": "via magpie",
			},
		},
	}
}

func testPickerPlan(path string) Plan {
	return Plan{Agent: ClaudeAgent, Files: []FilePlan{{
		Path:   path,
		Format: FormatJSON,
		Fields: []Field{
			{Path: "model", Value: "gateway-model"},
			{Path: "modelPicker", Value: pickerValue()},
		},
	}}}
}

// mustJSONValue 按 JSON 语义比较一个键与期望值。
func mustJSONValue(t *testing.T, path, key, want string) {
	t.Helper()
	got, ok := GetJSONRaw(path, key)
	if !ok {
		t.Fatalf("%s 应该存在，实际不存在", key)
	}
	canon, ok := canonicalRawJSON(want)
	if !ok {
		t.Fatalf("测试里的期望值不是有效 JSON: %s", want)
	}
	if got != canon {
		t.Fatalf("%s = %s，想要 %s", key, got, canon)
	}
}

func TestConnectReplacesObjectDriftAndRestores(t *testing.T) {
	dataDir, path := claudeTestEnv(t)
	writeSettings(t, path, pickerOriginal)
	if err := Connect(dataDir, testPickerPlan(path)); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if got := readSettings(t, path); !strings.Contains(got, "// 用户自己的注释") {
		t.Fatalf("Connect 丢失了注释：\n%s", got)
	}
	mustJSONValue(t, path, "modelPicker", pickerWrittenRaw)
	mustValue(t, path, "model", "gateway-model")
	if status := Drift(dataDir, ClaudeAgent); status.State != StatusApplied || len(status.Drifted) != 0 {
		t.Fatalf("Connect 后 Drift = %+v，想要 applied", status)
	}

	// 只改缩进和键序，内容不变，不应算漂移。
	writeSettings(t, path, `{
  // 用户自己的注释
  "model": "gateway-model",
  "modelPicker": {"options": [{"label":"Sonnet","model":"claude-sonnet-4-5","description":"via magpie"}], "replaceBuiltInOptions": true},
  "env": { "FOO": "bar" }
}
`)
	if status := Drift(dataDir, ClaudeAgent); status.State != StatusApplied {
		t.Fatalf("只改缩进后 Drift = %+v，想要 applied", status)
	}

	if err := Disconnect(dataDir, ClaudeAgent); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	mustJSONValue(t, path, "modelPicker", pickerOriginalRaw)
	mustValue(t, path, "model", "user-model")
	if got := readSettings(t, path); !strings.Contains(got, "// 用户自己的注释") {
		t.Fatalf("Disconnect 丢失了注释：\n%s", got)
	}
	requireValidJSONC(t, path)
	assertNoState(t, dataDir)
}

func TestDisconnectRemovesObjectKeyThatWasAbsent(t *testing.T) {
	dataDir, path := claudeTestEnv(t)
	writeSettings(t, path, `{
  "env": {"FOO": "bar"},
  "model": "user-model"
}
`)
	if err := Connect(dataDir, testPickerPlan(path)); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	mustJSONValue(t, path, "modelPicker", pickerWrittenRaw)
	if err := Disconnect(dataDir, ClaudeAgent); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	mustAbsent(t, path, "modelPicker")
	mustValue(t, path, "model", "user-model")
	assertNoState(t, dataDir)
}

func TestDisconnectPrunesParentObjectWeCreated(t *testing.T) {
	dataDir, path := claudeTestEnv(t)
	writeSettings(t, path, `{
  "model": "user-model"
}
`)
	plan := Plan{Agent: ClaudeAgent, Files: []FilePlan{{
		Path:   path,
		Format: FormatJSON,
		Fields: []Field{{Path: "providers.mcode-magpie", Value: map[string]any{"name": "Magpie"}}},
	}}}
	if err := Connect(dataDir, plan); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	mustValue(t, path, "providers.mcode-magpie.name", "Magpie")
	// 用户自己加了一个键：断开时按字段恢复（整份原文写回只用于文件仍是我们
	// 最后写入的样子），这样才会走到剪枝那条路。
	setSetting(t, path, "note", "mine")
	if err := Disconnect(dataDir, ClaudeAgent); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	mustAbsent(t, path, "providers")
	mustValue(t, path, "note", "mine")
	mustValue(t, path, "model", "user-model")
	requireValidJSONC(t, path)
	assertNoState(t, dataDir)
}

func TestDisconnectKeepsEmptyParentObjectUserHad(t *testing.T) {
	dataDir, path := claudeTestEnv(t)
	writeSettings(t, path, `{
  "providers": {},
  "model": "user-model"
}
`)
	plan := Plan{Agent: ClaudeAgent, Files: []FilePlan{{
		Path:   path,
		Format: FormatJSON,
		Fields: []Field{{Path: "providers.mcode-magpie", Value: map[string]any{"name": "Magpie"}}},
	}}}
	if err := Connect(dataDir, plan); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	setSetting(t, path, "note", "mine")
	if err := Disconnect(dataDir, ClaudeAgent); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if got, ok := GetJSONRaw(path, "providers"); !ok || got != "{}" {
		t.Fatalf("用户原本就有的空对象被删了: %q %v", got, ok)
	}
	mustAbsent(t, path, "providers.mcode-magpie")
	mustValue(t, path, "note", "mine")
	requireValidJSONC(t, path)
	assertNoState(t, dataDir)
}

func TestReapplyRestoresKeyDroppedFromPlan(t *testing.T) {
	dataDir, path := claudeTestEnv(t)
	writeSettings(t, path, pickerOriginal)
	if err := Connect(dataDir, testPickerPlan(path)); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	shrunk := testPickerPlan(path)
	shrunk.Files[0].Fields = shrunk.Files[0].Fields[:1] // 只留 model，去掉 modelPicker
	if err := Reapply(dataDir, shrunk); err != nil {
		t.Fatalf("Reapply: %v", err)
	}
	mustJSONValue(t, path, "modelPicker", pickerOriginalRaw)
	if status := Drift(dataDir, ClaudeAgent); status.State != StatusApplied {
		t.Fatalf("去掉字段后 Drift = %+v，想要 applied", status)
	}
	applied, err := loadApplied(dataDir, ClaudeAgent)
	if err != nil {
		t.Fatalf("loadApplied: %v", err)
	}
	if _, ok := applied.Files[path].Fields["modelPicker"]; ok {
		t.Fatal("不再写的键不应该继续记录在 applied 里")
	}
	if err := Disconnect(dataDir, ClaudeAgent); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	mustJSONValue(t, path, "modelPicker", pickerOriginalRaw)
	mustValue(t, path, "model", "user-model")
	if got := readSettings(t, path); !strings.Contains(got, "// 用户自己的注释") {
		t.Fatalf("恢复后丢失了注释：\n%s", got)
	}
	requireValidJSONC(t, path)
	assertNoState(t, dataDir)
}

func TestReapplyDroppedKeyIsNoLongerTracked(t *testing.T) {
	dataDir, path := claudeTestEnv(t)
	writeSettings(t, path, pickerOriginal)
	if err := Connect(dataDir, testPickerPlan(path)); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	shrunk := testPickerPlan(path)
	shrunk.Files[0].Fields = shrunk.Files[0].Fields[:1]
	if err := Reapply(dataDir, shrunk); err != nil {
		t.Fatalf("Reapply: %v", err)
	}
	// 恢复后的键不再被跟踪：用户再改它不影响 Drift。
	if err := SetJSON(path, KV{Path: "modelPicker", Value: map[string]any{"options": []any{}}}); err != nil {
		t.Fatalf("改配置: %v", err)
	}
	if status := Drift(dataDir, ClaudeAgent); status.State != StatusApplied {
		t.Fatalf("不被跟踪的键被改后 Drift = %+v，想要 applied", status)
	}
}

func TestReapplyAddedKeyRestoredOnDisconnect(t *testing.T) {
	dataDir, path := claudeTestEnv(t)
	writeSettings(t, path, `{
  "env": {"FOO": "bar", "NO_PROXY": "user-proxy"},
  "model": "user-model"
}
`)
	base := Plan{Agent: ClaudeAgent, Files: []FilePlan{{
		Path:   path,
		Format: FormatJSON,
		Fields: []Field{
			{Path: "model", Value: "gateway-model"},
		},
	}}}
	if err := Connect(dataDir, base); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	grown := base
	grown.Files = slices.Clone(base.Files)
	grown.Files[0].Fields = append(slices.Clone(base.Files[0].Fields), Field{Path: "env.NO_PROXY", Value: "localhost,127.0.0.1,::1"})
	if err := Reapply(dataDir, grown); err != nil {
		t.Fatalf("Reapply: %v", err)
	}
	mustValue(t, path, "env.NO_PROXY", "localhost,127.0.0.1,::1")
	if err := Disconnect(dataDir, ClaudeAgent); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	mustValue(t, path, "env.NO_PROXY", "user-proxy")
	mustValue(t, path, "model", "user-model")
}

func TestUserEditedObjectKeptOnDisconnect(t *testing.T) {
	dataDir, path := claudeTestEnv(t)
	writeSettings(t, path, pickerOriginal)
	if err := Connect(dataDir, testPickerPlan(path)); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	edited := map[string]any{
		"replaceBuiltInOptions": false,
		"options": []any{map[string]any{
			"model": "my-model", "label": "Mine", "description": "我自己改的",
		}},
	}
	if err := SetJSON(path, KV{Path: "modelPicker", Value: edited}); err != nil {
		t.Fatalf("手改: %v", err)
	}
	if status := Drift(dataDir, ClaudeAgent); status.State != StatusDrifted || !slices.Contains(status.Drifted, "modelPicker") {
		t.Fatalf("手改后 Drift = %+v，想要 drifted 且包含 modelPicker", status)
	}
	editedRaw, err := canonicalJSON(edited)
	if err != nil {
		t.Fatalf("编码手改值: %v", err)
	}
	if err := Disconnect(dataDir, ClaudeAgent); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	mustJSONValue(t, path, "modelPicker", editedRaw)
	mustValue(t, path, "model", "user-model")
	assertNoState(t, dataDir)
}

func TestReapplyRewritesObjectOverUserEdit(t *testing.T) {
	dataDir, path := claudeTestEnv(t)
	writeSettings(t, path, pickerOriginal)
	if err := Connect(dataDir, testPickerPlan(path)); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := SetJSON(path, KV{Path: "modelPicker", Value: map[string]any{"options": []any{}}}); err != nil {
		t.Fatalf("手改: %v", err)
	}
	if err := Reapply(dataDir, testPickerPlan(path)); err != nil {
		t.Fatalf("Reapply: %v", err)
	}
	mustJSONValue(t, path, "modelPicker", pickerWrittenRaw)
	if status := Drift(dataDir, ClaudeAgent); status.State != StatusApplied {
		t.Fatalf("Reapply 后 Drift = %+v，想要 applied", status)
	}
}

func TestKeepForeignObjectUntouched(t *testing.T) {
	dataDir, path := claudeTestEnv(t)
	writeSettings(t, path, pickerOriginal)
	plan := Plan{Agent: ClaudeAgent, Files: []FilePlan{{
		Path:   path,
		Format: FormatJSON,
		Fields: []Field{
			{Path: "model", Value: "gateway-model"},
			{Path: "modelPicker", Value: pickerValue(), KeepForeign: true},
		},
	}}}
	if err := Connect(dataDir, plan); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	mustJSONValue(t, path, "modelPicker", pickerOriginalRaw)
	mustValue(t, path, "model", "gateway-model")
	applied, err := loadApplied(dataDir, ClaudeAgent)
	if err != nil {
		t.Fatalf("loadApplied: %v", err)
	}
	if _, ok := applied.Files[path].Fields["modelPicker"]; ok {
		t.Fatal("KeepForeign 跳过的键不应该记录在 applied 里")
	}
	stash, err := loadStash(dataDir, ClaudeAgent)
	if err != nil {
		t.Fatalf("loadStash: %v", err)
	}
	if _, ok := stash.Files[path]["modelPicker"]; ok {
		t.Fatal("KeepForeign 跳过的键不应该记进 stash")
	}
	if status := Drift(dataDir, ClaudeAgent); status.State != StatusApplied {
		t.Fatalf("Drift = %+v，想要 applied", status)
	}
	if err := Disconnect(dataDir, ClaudeAgent); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	mustJSONValue(t, path, "modelPicker", pickerOriginalRaw)
	mustValue(t, path, "model", "user-model")
	assertNoState(t, dataDir)
}

func TestKeepForeignWritesAbsentKey(t *testing.T) {
	dataDir, path := claudeTestEnv(t)
	writeSettings(t, path, `{
  "model": "user-model"
}
`)
	plan := Plan{Agent: ClaudeAgent, Files: []FilePlan{{
		Path:   path,
		Format: FormatJSON,
		Fields: []Field{
			{Path: "modelPicker", Value: pickerValue(), KeepForeign: true},
		},
	}}}
	if err := Connect(dataDir, plan); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	mustJSONValue(t, path, "modelPicker", pickerWrittenRaw)
	if err := Disconnect(dataDir, ClaudeAgent); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	mustAbsent(t, path, "modelPicker")
}

func TestDisconnectRestoresEmptyString(t *testing.T) {
	dataDir, path := claudeTestEnv(t)
	writeSettings(t, path, `{
  "env": {"FOO": ""}
}
`)
	plan := Plan{Agent: ClaudeAgent, Files: []FilePlan{{
		Path:   path,
		Format: FormatJSON,
		Fields: []Field{
			{Path: "env.FOO", Value: "magpie-value"},
		},
	}}}
	if err := Connect(dataDir, plan); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	mustValue(t, path, "env.FOO", "magpie-value")
	if err := Disconnect(dataDir, ClaudeAgent); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	mustValue(t, path, "env.FOO", "")
}

func TestOldFormatStateStillWorks(t *testing.T) {
	dataDir, path := claudeTestEnv(t)
	writeSettings(t, path, `{
  "env": {"FOO": "bar"},
  "model": "gateway-model"
}
`)
	// 旧版本的状态文件里，字符串值没有引号。
	oldStash, err := json.Marshal(map[string]any{
		"fields": map[string]any{
			"model": map[string]any{"value": "user-model", "had": true},
		},
	})
	if err != nil {
		t.Fatalf("编码旧 stash: %v", err)
	}
	writeTestFile(t, stashPath(dataDir, ClaudeAgent), string(oldStash))
	oldApplied, err := json.Marshal(map[string]any{
		"path":    path,
		"created": false,
		"kept":    false,
		"fields": map[string]any{
			"model": map[string]any{"value": "gateway-model", "had": true},
		},
	})
	if err != nil {
		t.Fatalf("编码旧 applied: %v", err)
	}
	writeTestFile(t, appliedPath(dataDir, ClaudeAgent), string(oldApplied))

	if status := Drift(dataDir, ClaudeAgent); status.State != StatusApplied {
		t.Fatalf("旧格式状态文件下 Drift = %+v，想要 applied", status)
	}
	if err := Disconnect(dataDir, ClaudeAgent); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	mustValue(t, path, "model", "user-model")
	assertNoState(t, dataDir)
}

// TestConnectMultipleFilesAndDisconnect 覆盖一次计划写两份文件：drift 带文件名
// 前缀、reapply 恢复、断开后两份文件逐字节还原。
func TestConnectMultipleFilesAndDisconnect(t *testing.T) {
	dataDir := t.TempDir()
	dir := t.TempDir()
	providersPath := filepath.Join(dir, "providers.json")
	modelsPath := filepath.Join(dir, "models.json")
	originalProviders := "{\n  \"user\": true,\n  \"model\": \"user-model\"\n}\n"
	originalModels := "{\n  \"version\": 1,\n  \"providers\": {\"other\": {\"name\": \"Other\"}}\n}\n"
	writeTestFile(t, providersPath, originalProviders)
	writeTestFile(t, modelsPath, originalModels)

	plan := Plan{Agent: ClineAgent, Files: []FilePlan{
		{
			Path:   providersPath,
			Format: FormatJSON,
			Fields: []Field{
				{Path: "lastUsedProvider", Value: "mcode-magpie"},
				{Path: "providers.mcode-magpie", Value: map[string]any{"name": "Magpie"}},
			},
		},
		{
			Path:   modelsPath,
			Format: FormatJSON,
			Fields: []Field{
				{Path: "version", Value: 2},
				{Path: "providers.mcode-magpie", Value: map[string]any{"name": "Magpie"}},
			},
		},
	}}
	if err := Connect(dataDir, plan); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	mustValue(t, providersPath, "lastUsedProvider", "mcode-magpie")
	mustJSONValue(t, modelsPath, "version", "2")
	if status := Drift(dataDir, ClineAgent); status.State != StatusApplied {
		t.Fatalf("Drift = %+v，想要 applied", status)
	}

	// 手改第二个文件：Drift 要用文件名前缀区分来源。
	if err := SetJSON(modelsPath, KV{Path: "version", Value: 3}); err != nil {
		t.Fatalf("手改 models.json: %v", err)
	}
	status := Drift(dataDir, ClineAgent)
	if status.State != StatusDrifted || !reflect.DeepEqual(status.Drifted, []string{"models.json: version"}) {
		t.Fatalf("Drift = %+v，想要 drifted / [models.json: version]", status)
	}
	if err := Reapply(dataDir, plan); err != nil {
		t.Fatalf("Reapply: %v", err)
	}
	mustJSONValue(t, modelsPath, "version", "2")

	if err := Disconnect(dataDir, ClineAgent); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if got := readTestFile(t, providersPath); got != originalProviders {
		t.Errorf("providers.json 未逐字节还原:\n得到 %q\n想要 %q", got, originalProviders)
	}
	if got := readTestFile(t, modelsPath); got != originalModels {
		t.Errorf("models.json 未逐字节还原:\n得到 %q\n想要 %q", got, originalModels)
	}
	assertNoState(t, dataDir)
}

// TestDisconnectWritesBackOriginalText 校验首次接管时记下的原文快照在断开时
// 真的被写回。原文里的对象值写在同一行，字段级恢复会把它按 json.MarshalIndent
// 重新渲染（多了换行与缩进），永远还原不成逐字节相同，只有快照分支做得到。
func TestDisconnectWritesBackOriginalText(t *testing.T) {
	dataDir, path := claudeTestEnv(t)
	original := "{\n  \"model\": \"user-model\",\n  \"providers\": {\"mcode-magpie\": {\"name\": \"Old\"}}\n}\n"
	writeSettings(t, path, original)
	plan := Plan{Agent: ClaudeAgent, Files: []FilePlan{{
		Path:   path,
		Format: FormatJSON,
		Fields: []Field{
			{Path: "model", Value: "gateway-model"},
			{Path: "providers.mcode-magpie", Value: map[string]any{"name": "Magpie"}},
		},
	}}}
	if err := Connect(dataDir, plan); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	mustValue(t, path, "model", "gateway-model")
	mustValue(t, path, "providers.mcode-magpie.name", "Magpie")

	if err := Disconnect(dataDir, ClaudeAgent); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if got := readSettings(t, path); got != original {
		t.Fatalf("断开后应按原文逐字节写回:\n得到 %q\n想要 %q", got, original)
	}
	assertNoState(t, dataDir)
}

// TestConnectMultipleFilesRollsBackOnFailure 校验第二份文件写入失败时，第一份
// 已写的文件也被回滚，且不留下状态。
func TestConnectMultipleFilesRollsBackOnFailure(t *testing.T) {
	dataDir := t.TempDir()
	dir := t.TempDir()
	providersPath := filepath.Join(dir, "providers.json")
	modelsPath := filepath.Join(dir, "models.json")
	originalProviders := "{\n  \"model\": \"user-model\"\n}\n"
	originalModels := "{\n  \"version\": 1\n}\n"
	writeTestFile(t, providersPath, originalProviders)
	writeTestFile(t, modelsPath, originalModels)

	plan := Plan{Agent: ClineAgent, Files: []FilePlan{
		{Path: providersPath, Format: FormatJSON, Fields: []Field{{Path: "model", Value: "new-model"}}},
		// json.Marshal 不认识 chan，写入第二份文件时才会失败。
		{Path: modelsPath, Format: FormatJSON, Fields: []Field{{Path: "bad", Value: make(chan int)}}},
	}}
	if err := Connect(dataDir, plan); err == nil {
		t.Fatal("第二份文件不可序列化时应报错")
	}
	if got := readTestFile(t, providersPath); got != originalProviders {
		t.Errorf("失败后 providers.json 应回滚，得到 %q", got)
	}
	if got := readTestFile(t, modelsPath); got != originalModels {
		t.Errorf("失败后 models.json 应保持原样，得到 %q", got)
	}
	assertNoState(t, dataDir)
}
