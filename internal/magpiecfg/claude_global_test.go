package magpiecfg

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// 本文件按移植说明第 8 节 T6 写端到端场景：用真实的 Connect/Reapply/
// Disconnect 改动 t.TempDir() 里的 settings.json，验证写进去的整套键、断开
// 后文件逐字节还原，以及状态文件被清干净。配置目录按 paths_test.go 的方式由
// withClaudeSandbox 指到临时 home，不读真实 home、不依赖网络。

// claudeGlobalFullOriginal 是场景 1 的原始配置：除了用户自己的键，还塞了
// magpie 会删掉的过期键（ANTHROPIC_MODEL、fable 档、档位说明、指向网关的
// 子代理模型），用来验证断开时它们都能还回去。
const claudeGlobalFullOriginal = `{
  // 交接前的全局配置
  "env": {
    "FOO": "bar",
    "ANTHROPIC_MODEL": "user-model",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "user-sonnet",
    "ANTHROPIC_DEFAULT_FABLE_MODEL": "user-fable",
    "ANTHROPIC_DEFAULT_OPUS_MODEL_DESCRIPTION": "旧说明",
    "CLAUDE_CODE_SUBAGENT_MODEL": "auto/claude-sonnet-5-5",
    "NO_PROXY": "corp.internal"
  },
  "model": "user-model"
}
`

// claudeGlobalPlainOriginal 是有 env、没有 picker 的普通配置；连接再断开后
// 必须逐字节回到这份内容（注释、缩进、键序都不变）。
const claudeGlobalPlainOriginal = `{
  // 只跑本地模型
  "env": {
    "FOO": "bar",
    "NO_PROXY": "corp"
  },
  "model": "user-model"
}
`

// claudeGlobalNoEnvOriginal 是原本没有 env 的配置，用来验证「断开后 magpie
// 写的 env 键全部消失」。
const claudeGlobalNoEnvOriginal = `{
  // 只钉住了一个模型
  "model": "user-model"
}
`

// claudeGlobalMagpieOriginal 是之前被 magpie 写过的配置：picker 每行都以
// via magpie 结尾（会被接管重写），档位变量也在。picker 的写法是
// json.MarshalIndent 的规范渲染（键按字典序、两空格步长），这样断开时按
// stash 原值恢复才能逐字节相等。
const claudeGlobalMagpieOriginal = `{
  // 用户自己的注释，连接时要原样保留
  "env": {
    "FOO": "bar",
    "NO_PROXY": "corp",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "claude-opus-4-5"
  },
  "model": "claude-sonnet-4-5",
  "modelPicker": {
    "options": [
      {
        "description": "via magpie",
        "label": "Sonnet",
        "model": "claude-sonnet-4-5"
      }
    ],
    "replaceBuiltInOptions": true
  }
}
`

// claudeGlobalJSON 把 settings.json 解析成 map：先抹掉注释，再比较，忽略
// 缩进与键序。
func claudeGlobalJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	text := readSettings(t, path)
	stripped, ok := blankComments([]byte(text))
	if !ok {
		t.Fatalf("settings.json 的注释无法抹掉：\n%s", text)
	}
	var doc map[string]any
	if err := json.Unmarshal(stripped, &doc); err != nil {
		t.Fatalf("解析 settings.json：%v\n%s", err, text)
	}
	return doc
}

// claudeGlobalEnvKeys 返回 env 下的键，按字典序。
func claudeGlobalEnvKeys(t *testing.T, path string) []string {
	t.Helper()
	doc := claudeGlobalJSON(t, path)
	env, ok := doc["env"].(map[string]any)
	if !ok {
		t.Fatalf("env 不是对象：%#v", doc["env"])
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// claudeGlobalPickerOnDisk 读磁盘上的 modelPicker；键不存在时 ok 为 false。
func claudeGlobalPickerOnDisk(t *testing.T, path string) (rows []map[string]any, replace, ok bool) {
	t.Helper()
	picker, exists := claudeGlobalJSON(t, path)["modelPicker"]
	if !exists {
		return nil, false, false
	}
	obj, isMap := picker.(map[string]any)
	if !isMap {
		t.Fatalf("modelPicker 不是对象：%#v", picker)
	}
	list, isList := obj["options"].([]any)
	if !isList {
		t.Fatalf("modelPicker.options 不是数组：%#v", obj["options"])
	}
	rows = make([]map[string]any, 0, len(list))
	for _, item := range list {
		row, isRow := item.(map[string]any)
		if !isRow {
			t.Fatalf("modelPicker 的行不是对象：%#v", item)
		}
		rows = append(rows, row)
	}
	replace, _ = obj["replaceBuiltInOptions"].(bool)
	return rows, replace, true
}

// claudeGlobalPlan 构造计划，失败即终止。
func claudeGlobalPlan(t *testing.T, opts ClaudeOptions) Plan {
	t.Helper()
	plan, err := ClaudePlan(opts)
	if err != nil {
		t.Fatalf("ClaudePlan: %v", err)
	}
	return plan
}

// claudeGlobalDisconnect 走一次 Disconnect，失败即终止。
func claudeGlobalDisconnect(t *testing.T, dataDir string) {
	t.Helper()
	if err := Disconnect(dataDir, ClaudeAgent); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
}

// TestClaudeGlobalClaudeModels 是场景 1：三个 Claude 模型勾选后，settings.json
// 里的完整键集合（移植说明第 2 节表 1–11）与值，以及断开后整份文档还原。
func TestClaudeGlobalClaudeModels(t *testing.T) {
	dataDir, path := claudeTestEnv(t)
	writeSettings(t, path, claudeGlobalFullOriginal)
	original := claudeGlobalJSON(t, path)

	selections := claudeTestClaudeSelections()
	opts := claudeTestOptions(selections...)
	// Served 用网关实际服务的 ID：子代理旧值指向它时才会被删。
	opts.Served = []string{selections[0].ID, selections[1].ID, selections[2].ID}
	plan := claudeGlobalPlan(t, opts)

	if err := Connect(dataDir, plan); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	wantKeys := []string{
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL",
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_SMALL_FAST_MODEL",
		"FOO",
		"NO_PROXY",
	}
	if got := claudeGlobalEnvKeys(t, path); !slices.Equal(got, wantKeys) {
		t.Fatalf("env 键集合 = %v，想要 %v", got, wantKeys)
	}
	mustValue(t, path, "env.ANTHROPIC_BASE_URL", "https://magpie.test")
	mustValue(t, path, "env.ANTHROPIC_AUTH_TOKEN", "sk-test")
	mustValue(t, path, "env.ANTHROPIC_SMALL_FAST_MODEL", "auto/claude-haiku-5-5")
	mustValue(t, path, "model", "auto/claude-opus-5-5")
	mustValue(t, path, "env.ANTHROPIC_DEFAULT_OPUS_MODEL", "auto/claude-opus-5-5")
	mustValue(t, path, "env.ANTHROPIC_DEFAULT_SONNET_MODEL", "auto/claude-sonnet-5-5")
	mustValue(t, path, "env.ANTHROPIC_DEFAULT_HAIKU_MODEL", "auto/claude-haiku-5-5")
	mustAbsent(t, path, "env.ANTHROPIC_DEFAULT_FABLE_MODEL")
	mustAbsent(t, path, "env.ANTHROPIC_MODEL")
	mustAbsent(t, path, "env.CLAUDE_CODE_SUBAGENT_MODEL")
	mustAbsent(t, path, "env.ANTHROPIC_DEFAULT_OPUS_MODEL_DESCRIPTION")
	mustValue(t, path, "env.NO_PROXY", "corp.internal,localhost,127.0.0.1,::1")
	mustValue(t, path, "env.FOO", "bar")

	rows, replace, ok := claudeGlobalPickerOnDisk(t, path)
	if !ok {
		t.Fatal("modelPicker 应该被写入")
	}
	if !replace {
		t.Fatal("modelPicker 应该带 replaceBuiltInOptions: true")
	}
	wantRows := []map[string]any{
		{"model": "auto/claude-opus-5-5", "label": "Claude Opus 5.5", "description": "via magpie"},
		{"model": "auto/claude-sonnet-5-5", "label": "Claude Sonnet 5.5", "description": "via magpie"},
		{"model": "auto/claude-haiku-5-5", "label": "Claude Haiku 5.5", "description": "via magpie"},
	}
	if !reflect.DeepEqual(rows, wantRows) {
		t.Fatalf("modelPicker 行 = %#v，想要 %#v", rows, wantRows)
	}
	requireValidJSONC(t, path)

	claudeGlobalDisconnect(t, dataDir)
	if got := claudeGlobalJSON(t, path); !reflect.DeepEqual(got, original) {
		t.Fatalf("断开后配置 = %#v，想要 %#v", got, original)
	}
	assertNoState(t, dataDir)
}

// TestClaudeGlobalSingleNonClaudeModel 是场景 2：只勾一个非 Claude 模型时，
// 所有键都指向它、fable 档被删、picker 只有一行且不带 [1m]。
func TestClaudeGlobalSingleNonClaudeModel(t *testing.T) {
	dataDir, path := claudeTestEnv(t)
	writeSettings(t, path, claudeGlobalPlainOriginal)
	original := readSettings(t, path)
	plan := claudeGlobalPlan(t, claudeTestOptions(ClaudeSelection{ID: "auto/deepseek-v3-2"}))

	if err := Connect(dataDir, plan); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	for _, key := range []string{
		"model",
		"env.ANTHROPIC_SMALL_FAST_MODEL",
		"env.ANTHROPIC_DEFAULT_OPUS_MODEL",
		"env.ANTHROPIC_DEFAULT_SONNET_MODEL",
		"env.ANTHROPIC_DEFAULT_HAIKU_MODEL",
	} {
		mustValue(t, path, key, "auto/deepseek-v3-2")
	}
	mustAbsent(t, path, "env.ANTHROPIC_DEFAULT_FABLE_MODEL")
	mustAbsent(t, path, "env.ANTHROPIC_MODEL")

	rows, replace, ok := claudeGlobalPickerOnDisk(t, path)
	if !ok || !replace {
		t.Fatalf("modelPicker 应该被写入且带 replace: ok = %v, replace = %v", ok, replace)
	}
	wantRows := []map[string]any{
		{"model": "auto/deepseek-v3-2", "label": "deepseek-v3-2", "description": "via magpie"},
	}
	if !reflect.DeepEqual(rows, wantRows) {
		t.Fatalf("modelPicker 行 = %#v，想要 %#v", rows, wantRows)
	}

	claudeGlobalDisconnect(t, dataDir)
	if got := readSettings(t, path); got != original {
		t.Fatalf("断开后文件没有逐字节恢复：\n%s", got)
	}
	assertNoState(t, dataDir)
}

// TestClaudeGlobalTakesOverMagpiePicker 是场景 3：原文件已有 magpie 写的
// modelPicker 与档位变量，连接时接管重写，断开后逐字节恢复。
func TestClaudeGlobalTakesOverMagpiePicker(t *testing.T) {
	dataDir, path := claudeTestEnv(t)
	writeSettings(t, path, claudeGlobalMagpieOriginal)
	plan := claudeGlobalPlan(t, claudeTestOptions())

	if err := Connect(dataDir, plan); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if text := readSettings(t, path); !strings.Contains(text, "用户自己的注释") {
		t.Fatalf("连接不应动用户的注释：\n%s", text)
	}
	mustValue(t, path, "model", "auto/claude-opus-5-5")
	mustValue(t, path, "env.ANTHROPIC_DEFAULT_OPUS_MODEL", "auto/claude-opus-5-5")
	rows, _, ok := claudeGlobalPickerOnDisk(t, path)
	if !ok || len(rows) != 3 {
		t.Fatalf("magpie 的 picker 应该被接管成 3 行，实际 %d 行（ok = %v）", len(rows), ok)
	}

	claudeGlobalDisconnect(t, dataDir)
	if got := readSettings(t, path); got != claudeGlobalMagpieOriginal {
		t.Fatalf("断开后文件没有逐字节恢复：\n%s", got)
	}
	assertNoState(t, dataDir)
}

// TestClaudeGlobalRestoresFile 是场景 4：连接再断开后整份文件（含注释、缩进、
// 键序）与连接前一致；原本没有 env 时 magpie 写的键全部消失。
func TestClaudeGlobalRestoresFile(t *testing.T) {
	t.Run("有 env 的文件整份恢复", func(t *testing.T) {
		dataDir, path := claudeTestEnv(t)
		writeSettings(t, path, claudeGlobalPlainOriginal)
		plan := claudeGlobalPlan(t, claudeTestOptions())

		if err := Connect(dataDir, plan); err != nil {
			t.Fatalf("Connect: %v", err)
		}
		mustValue(t, path, "model", "auto/claude-opus-5-5")
		if _, _, ok := claudeGlobalPickerOnDisk(t, path); !ok {
			t.Fatal("连接后应该有 modelPicker")
		}

		claudeGlobalDisconnect(t, dataDir)
		if got := readSettings(t, path); got != claudeGlobalPlainOriginal {
			t.Fatalf("断开后文件没有逐字节恢复：\n%s", got)
		}
		assertNoState(t, dataDir)
	})

	t.Run("原本没有 env 时断开后 env 键整个消失", func(t *testing.T) {
		dataDir, path := claudeTestEnv(t)
		writeSettings(t, path, claudeGlobalNoEnvOriginal)
		plan := claudeGlobalPlan(t, claudeTestOptions())

		if err := Connect(dataDir, plan); err != nil {
			t.Fatalf("Connect: %v", err)
		}
		mustValue(t, path, "env.ANTHROPIC_BASE_URL", "https://magpie.test")

		claudeGlobalDisconnect(t, dataDir)
		for _, f := range plan.Files[0].Fields {
			// model 原本就有，断开时按 stash 还原，不在「全部消失」的范围里。
			if f.Path != "model" {
				mustAbsent(t, path, f.Path)
			}
		}
		mustValue(t, path, "model", "user-model")
		// 原文没有 env：连接时把这个由 mcode 创建的祖先对象记进
		// fieldState.Pruned，断开时连同 magpie 的键一起删掉，不再留下空的
		// env: {}；若走的是原文快照回写，拿回的原文本来也没有 env。
		mustAbsent(t, path, "env")
		if text := readSettings(t, path); !strings.Contains(text, "只钉住了一个模型") {
			t.Fatalf("用户的注释应该还在：\n%s", text)
		}
		assertNoState(t, dataDir)
	})
}

// TestClaudeGlobalReapplyThenDisconnect 是场景 5：勾选从 A、B、C 缩到 A 后
// Reapply，B、C 不残留；再 Disconnect，文件恢复原样且状态文件清空。
func TestClaudeGlobalReapplyThenDisconnect(t *testing.T) {
	dataDir, path := claudeTestEnv(t)
	writeSettings(t, path, claudeGlobalPlainOriginal)
	original := readSettings(t, path)

	a := ClaudeSelection{ID: "auto/claude-opus-7-7", Label: "Opus 7.7"}
	b := ClaudeSelection{ID: "auto/claude-sonnet-7-7", Label: "Sonnet 7.7"}
	c := ClaudeSelection{ID: "auto/claude-haiku-7-7", Label: "Haiku 7.7"}

	if err := Connect(dataDir, claudeGlobalPlan(t, claudeTestOptions(a, b, c))); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	mustValue(t, path, "model", a.ID)
	mustValue(t, path, "env.ANTHROPIC_DEFAULT_HAIKU_MODEL", c.ID)
	if rows, _, ok := claudeGlobalPickerOnDisk(t, path); !ok || len(rows) != 3 {
		t.Fatalf("连接后 picker 应该有 3 行，实际 %d 行（ok = %v）", len(rows), ok)
	}

	if err := Reapply(dataDir, claudeGlobalPlan(t, claudeTestOptions(a))); err != nil {
		t.Fatalf("Reapply: %v", err)
	}
	mustValue(t, path, "model", a.ID)
	for _, tier := range []string{"opus", "sonnet", "haiku"} {
		mustValue(t, path, "env."+TierEnv(tier), a.ID)
	}
	mustAbsent(t, path, "env.ANTHROPIC_DEFAULT_FABLE_MODEL")
	rows, _, ok := claudeGlobalPickerOnDisk(t, path)
	if !ok || len(rows) != 1 || rows[0]["model"] != a.ID {
		t.Fatalf("Reapply 后 picker 应该只剩 A，实际 %#v（ok = %v）", rows, ok)
	}
	text := readSettings(t, path)
	for _, gone := range []string{b.ID, c.ID} {
		if strings.Contains(text, gone) {
			t.Fatalf("Reapply 后不该残留 %s：\n%s", gone, text)
		}
	}

	claudeGlobalDisconnect(t, dataDir)
	if got := readSettings(t, path); got != original {
		t.Fatalf("断开后文件没有逐字节恢复：\n%s", got)
	}
	assertNoState(t, dataDir)
}
