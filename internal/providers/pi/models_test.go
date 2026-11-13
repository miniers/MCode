package pi

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestPiHelperProcess 是 ListModels 测试里替换 execCommand 用的假 pi 进程：
// 把 PI_HELPER_FIXTURE 指定的 jsonl 原样写到 stdout，模拟 pi 的 RPC 应答。
func TestPiHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_PI_HELPER") != "1" {
		return
	}
	data, err := os.ReadFile(os.Getenv("PI_HELPER_FIXTURE"))
	if err != nil {
		os.Exit(2)
	}
	_, _ = os.Stdout.Write(data)
	os.Exit(0)
}

// loadPiTestdata 读 testdata 下的 fixture。
func loadPiTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读 fixture %s 失败: %v", name, err)
	}
	return data
}

// writeConfigDir 造一个临时配置目录并写入给定的文件。
func writeConfigDir(t *testing.T, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatalf("写 %s 失败: %v", name, err)
		}
	}
	return dir
}

// stubExecCommand 把包级 execCommand 换成回放 fixture 的假进程，返回恢复函数。
func stubExecCommand(t *testing.T) func() {
	t.Helper()
	previous := execCommand
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, os.Args[0], "-test.run=TestPiHelperProcess")
	}
	return func() { execCommand = previous }
}

func TestParseModels(t *testing.T) {
	fetched := ParseFetchedModels(loadPiTestdata(t, "models_store.json"))
	settings := piRawObject(loadPiTestdata(t, "settings.json"))

	models, err := ParseModels(loadPiTestdata(t, "models_available.json"), fetched, settings)
	if err != nil {
		t.Fatalf("ParseModels 失败: %v", err)
	}
	if len(models) != 4 {
		t.Fatalf("期望 4 个模型，实际 %+v", models)
	}

	glm := models[0]
	if glm.ID != "cline-pass/cline-pass/glm-5.3" || glm.Provider != "cline-pass" || glm.Name != "GLM-5.3" {
		t.Errorf("第 0 项不符: %+v", glm)
	}
	if len(glm.ThinkingLevels) != 0 || glm.DefaultThinking != "" {
		t.Errorf("不支持思考的模型不应有等级: %+v", glm)
	}

	lhm := models[1]
	if lhm.ID != "gateway/low-high-max" || lhm.Provider != "gateway" || lhm.Name != "Gateway Low/High/Max" {
		t.Errorf("第 1 项不符: %+v", lhm)
	}
	// models-store.json 的条目整份取代模型自报的 thinkingLevelMap：null 掉的与未映射的
	// xhigh 都被藏起来，只剩 low/high/max。
	wantLevels := []string{"low", "high", "max"}
	if !equalStrings(lhm.ThinkingLevels, wantLevels) {
		t.Errorf("low-high-max 的思考等级 = %v，期望 %v", lhm.ThinkingLevels, wantLevels)
	}
	if lhm.DefaultThinking != "max" {
		t.Errorf("low-high-max 的默认等级 = %q，期望 settings 里的 max", lhm.DefaultThinking)
	}

	plain := models[2]
	if plain.ID != "gateway/plain" || plain.Name != "Gateway Plain" {
		t.Errorf("第 2 项不符: %+v", plain)
	}
	wantPlain := []string{"off", "minimal", "low", "medium", "high"}
	if !equalStrings(plain.ThinkingLevels, wantPlain) {
		t.Errorf("plain 的思考等级 = %v，期望 %v", plain.ThinkingLevels, wantPlain)
	}
	if plain.DefaultThinking != "minimal" {
		t.Errorf("plain 的默认等级 = %q，期望 settings 里按模型的 minimal", plain.DefaultThinking)
	}

	noName := models[3]
	if noName.ID != "gateway/no-name" || noName.Name != "no-name" {
		t.Errorf("没报名字的模型应退化为 pi 的 id: %+v", noName)
	}

	for _, model := range models {
		if model.IsDefault {
			t.Errorf("没有 state 时不应有当前模型: %+v", model)
		}
	}
}

// TestParseModelsSlashModelID 锁定模型 id 本身含斜杠的情形（magpie 的 jp/group/…）：
// RPC 的 id 不带 provider 前缀，完整 id 只加一层 provider；推理档位照模型自报的
// reasoning / thinkingLevelMap 展开（medium/minimal/off/xhigh 为 null 被隐藏）。
// 这条链断了，UI 的推理强度卡片就只能显示出“该模型未提供可调的推理强度”。
func TestParseModelsSlashModelID(t *testing.T) {
	raw := []byte(`{"models":[{"provider":"magpie","id":"jp/group/auto-deepseek-v4-1-flash","name":"Deepseek V4.1 Flash · jp","reasoning":true,"thinkingLevelMap":{"high":"high","low":"low","max":"max","medium":null,"minimal":null,"off":null,"xhigh":null}}]}`)
	// models-store.json 里只有别的 provider，不应影响这条模型的档位。
	fetched := ParseFetchedModels([]byte(`{"openai":{"models":[{"id":"gpt-4","provider":"openai","reasoning":false}]}}`))

	models, err := ParseModels(raw, fetched, nil)
	if err != nil {
		t.Fatalf("ParseModels 失败: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("期望 1 个模型，实际 %+v", models)
	}
	model := models[0]
	if model.ID != "magpie/jp/group/auto-deepseek-v4-1-flash" {
		t.Errorf("完整 id = %q，期望只加一层 provider 前缀", model.ID)
	}
	if !equalStrings(model.ThinkingLevels, []string{"low", "high", "max"}) {
		t.Errorf("思考等级 = %v，期望 [low high max]", model.ThinkingLevels)
	}
	// settings 里的默认等级 medium 不在档位里时退回第一个可用的档位。
	if model.DefaultThinking != "low" {
		t.Errorf("默认等级 = %q，期望 low", model.DefaultThinking)
	}
	// pi harness 就是靠这两步把完整 id 拆成 --provider / --model。
	if provider, ok := ProviderOf(model.ID); !ok || provider != "magpie" {
		t.Errorf("ProviderOf = %q, %v", provider, ok)
	}
	if id, ok := ModelOf(model.ID); !ok || id != "jp/group/auto-deepseek-v4-1-flash" {
		t.Errorf("ModelOf = %q, %v", id, ok)
	}
}

func TestParseModelsBareArray(t *testing.T) {
	raw := []byte(`[{"provider":"p","id":"m","name":"M","reasoning":true}]`)
	models, err := ParseModels(raw, nil, nil)
	if err != nil {
		t.Fatalf("ParseModels 失败: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("期望 1 个模型，实际 %+v", models)
	}
	if models[0].ID != "p/m" || len(models[0].ThinkingLevels) != 5 || models[0].DefaultThinking != "medium" {
		t.Errorf("裸数组解析不符: %+v", models[0])
	}
}

func TestParseModelsUnrecognized(t *testing.T) {
	if _, err := ParseModels([]byte(`{"foo":1}`), nil, nil); err == nil {
		t.Error("无法识别的载荷应报错")
	}
}

func TestPiSupportedThinkingLevels(t *testing.T) {
	cases := []struct {
		name    string
		model   string
		fetched string
		want    []string
	}{
		{
			name:  "不在 thinkingLevelMap 里的 xhigh 与 max 默认关闭",
			model: `{"reasoning":true,"thinkingLevelMap":{"xhigh":null}}`,
			want:  []string{"off", "minimal", "low", "medium", "high"},
		},
		{
			name:  "模型显式映射的 max 才出现",
			model: `{"reasoning":true,"thinkingLevelMap":{"low":"low","max":"max"}}`,
			want:  []string{"off", "minimal", "low", "medium", "high", "max"},
		},
		{
			name:  "不支持思考的模型没有等级",
			model: `{"reasoning":false}`,
			want:  nil,
		},
		{
			name:    "fetched 条目整份取代模型自报的字段",
			model:   `{"reasoning":true,"thinkingLevelMap":{"off":"off","minimal":"minimal","low":"low","medium":"medium","high":"high","xhigh":"xhigh","max":"max"}}`,
			fetched: `{}`,
			want:    []string{"off", "minimal", "low", "medium", "high"},
		},
		{
			name:    "fetched 说不能思考就没有等级",
			model:   `{"reasoning":true}`,
			fetched: `{"reasoning":false}`,
			want:    nil,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var fetched map[string]json.RawMessage
			if testCase.fetched != "" {
				fetched = piRawObject([]byte(testCase.fetched))
			}
			got := piSupportedThinkingLevels(piRawObject([]byte(testCase.model)), fetched)
			if !equalStrings(got, testCase.want) {
				t.Errorf("思考等级 = %v，期望 %v", got, testCase.want)
			}
		})
	}
}

func TestPiDefaultThinkingLevel(t *testing.T) {
	perModel := piRawObject([]byte(`{"modelThinkingLevels":{"p/m":"high"}}`))
	if got := piDefaultThinkingLevel("p/m", perModel); got != "high" {
		t.Errorf("按模型的等级 = %q，期望 high", got)
	}
	if got := piDefaultThinkingLevel(" p/m ", perModel); got != "high" {
		t.Errorf("模型 id 应先去掉空白 = %q，期望 high", got)
	}
	global := piRawObject([]byte(`{"defaultThinkingLevel":"low"}`))
	if got := piDefaultThinkingLevel("p/m", global); got != "low" {
		t.Errorf("全局默认 = %q，期望 low", got)
	}
	if got := piDefaultThinkingLevel("p/m", map[string]json.RawMessage{}); got != "medium" {
		t.Errorf("都没配置时 = %q，期望 medium", got)
	}
	if got := piDefaultThinkingLevel("p/m", nil); got != "medium" {
		t.Errorf("没有 settings 时 = %q，期望 medium", got)
	}
}

func TestProviderAndModelOf(t *testing.T) {
	if provider, ok := ProviderOf("cline-pass/cline-pass/glm-5.3"); !ok || provider != "cline-pass" {
		t.Errorf("ProviderOf = %q, %v", provider, ok)
	}
	if model, ok := ModelOf("cline-pass/cline-pass/glm-5.3"); !ok || model != "cline-pass/glm-5.3" {
		t.Errorf("ModelOf = %q, %v", model, ok)
	}
	if _, ok := ProviderOf("noSlash"); ok {
		t.Error("没有斜杠时 ProviderOf 应返回 false")
	}
	if _, ok := ModelOf("noSlash"); ok {
		t.Error("没有斜杠时 ModelOf 应返回 false")
	}
	if _, ok := ModelOf("trailing/"); ok {
		t.Error("斜杠后为空时 ModelOf 应返回 false")
	}
}

func TestParseFetchedModels(t *testing.T) {
	models := ParseFetchedModels(loadPiTestdata(t, "models_store.json"))
	if len(models) != 2 {
		t.Fatalf("期望 2 条 fetched 条目，实际 %+v", models)
	}
	if _, ok := models["gateway/low-high-max"]; !ok {
		t.Errorf("缺少 gateway/low-high-max: %+v", models)
	}
	if _, ok := models["gateway/fetched-only"]; !ok {
		t.Errorf("缺少 gateway/fetched-only: %+v", models)
	}

	explicit := ParseFetchedModels([]byte(`{"other":{"models":[{"id":"m1","provider":"explicit"}]}}`))
	if _, ok := explicit["explicit/m1"]; !ok {
		t.Errorf("条目自报 provider 时应以它为准: %+v", explicit)
	}

	if unparsable := ParseFetchedModels([]byte(`not json`)); len(unparsable) != 0 {
		t.Errorf("无法解析时应返回空表: %+v", unparsable)
	}
}

func TestListModelsReplay(t *testing.T) {
	dir := writeConfigDir(t, map[string][]byte{
		"settings.json":     loadPiTestdata(t, "settings.json"),
		"models-store.json": loadPiTestdata(t, "models_store.json"),
	})
	fixture, err := filepath.Abs(filepath.Join("testdata", "models_rpc.jsonl"))
	if err != nil {
		t.Fatalf("解析 fixture 路径失败: %v", err)
	}
	restore := stubExecCommand(t)
	defer restore()

	env := map[string]string{
		"PI_CODING_AGENT_DIR": dir,
		"HOME":                dir,
		"GO_WANT_PI_HELPER":   "1",
		"PI_HELPER_FIXTURE":   fixture,
	}
	models, err := ListModels(context.Background(), "/usr/local/bin/pi", env, dir)
	if err != nil {
		t.Fatalf("ListModels 失败: %v", err)
	}
	if len(models) != 4 {
		t.Fatalf("期望 4 个模型，实际 %+v", models)
	}
	if !equalStrings(models[1].ThinkingLevels, []string{"low", "high", "max"}) {
		t.Errorf("模型目录应带上 fetched 的思考等级: %+v", models[1])
	}
	if models[1].DefaultThinking != "max" {
		t.Errorf("模型目录应带上默认等级: %+v", models[1])
	}

	// state 报的是 provider/model 形状，应能认出当前模型。
	if models[2].ID != "gateway/plain" || !models[2].IsDefault {
		t.Errorf("当前模型应标为默认: %+v", models[2])
	}
	for _, index := range []int{0, 1, 3} {
		if models[index].IsDefault {
			t.Errorf("第 %d 项不该是当前模型: %+v", index, models[index])
		}
	}
}

func TestListModelsWithoutModelsResponse(t *testing.T) {
	reply := writeConfigDir(t, map[string][]byte{
		"reply": []byte(`{"id":"state","data":{"model":{"provider":"gateway","id":"plain"}}}` + "\n"),
	})
	restore := stubExecCommand(t)
	defer restore()

	env := map[string]string{
		"PI_CODING_AGENT_DIR": reply,
		"GO_WANT_PI_HELPER":   "1",
		"PI_HELPER_FIXTURE":   filepath.Join(reply, "reply"),
	}
	if _, err := ListModels(context.Background(), "pi", env, reply); err == nil {
		t.Error("没取到模型列表时应报错")
	}
	if _, err := ListModels(context.Background(), "", env, reply); err == nil {
		t.Error("没给可执行文件时应报错")
	}
}

func TestBinaryAvailable(t *testing.T) {
	if _, ok := BinaryAvailable(""); ok {
		t.Error("空名字不应算可用")
	}
	if _, ok := BinaryAvailable("mcode-pi-binary-that-does-not-exist"); ok {
		t.Error("不存在的二进制不应算可用")
	}

	dir := t.TempDir()
	binary := filepath.Join(dir, "mcode-pi-test-binary")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("写假二进制失败: %v", err)
	}
	// 裸名查找走的是缓存的登录 shell PATH，setenv 改不到它，所以这里用含 '/' 的绝对路径直接校验。
	path, ok := BinaryAvailable(binary)
	if !ok || path != binary {
		t.Errorf("BinaryAvailable = %q, %v，期望 %q, true", path, ok, binary)
	}
}

// TestListModelsWithMagpieBridge 校验 ListModelsWith 的 Magpie 注入：
// 桥扩展路径与端点 JSON 都非空时才在固定参数后追加 `-e bridge` 并设置 MCODE_PI_MAGPIE。
func TestListModelsWithMagpieBridge(t *testing.T) {
	dir := writeConfigDir(t, map[string][]byte{
		"settings.json":     loadPiTestdata(t, "settings.json"),
		"models-store.json": loadPiTestdata(t, "models_store.json"),
	})
	fixture, err := filepath.Abs(filepath.Join("testdata", "models_rpc.jsonl"))
	if err != nil {
		t.Fatalf("解析 fixture 路径失败: %v", err)
	}
	env := map[string]string{
		"PI_CODING_AGENT_DIR": dir,
		"HOME":                dir,
		"GO_WANT_PI_HELPER":   "1",
		"PI_HELPER_FIXTURE":   fixture,
	}

	// run 跑一次 ListModelsWith，返回假进程实际收到的参数与环境。
	run := func(t *testing.T, bridge, magpieEnv string) ([]string, []string) {
		t.Helper()
		previous := execCommand
		defer func() { execCommand = previous }()
		var gotArgs []string
		var captured *exec.Cmd
		execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
			gotArgs = append([]string{}, args...)
			captured = exec.CommandContext(ctx, os.Args[0], "-test.run=TestPiHelperProcess")
			return captured
		}
		models, err := ListModelsWith(context.Background(), "pi", env, dir, bridge, magpieEnv)
		if err != nil {
			t.Fatalf("ListModelsWith 失败: %v", err)
		}
		if len(models) != 4 {
			t.Fatalf("期望 4 个模型，实际 %+v", models)
		}
		if captured == nil {
			t.Fatal("没有捕获到子进程")
		}
		return gotArgs, captured.Env
	}

	const endpoints = `[{"provider":"magpie-jp","endpoint":"432db46a","baseUrl":"https://x/v1","apiKey":"sk-test"}]`

	t.Run("不注入时保持固定参数", func(t *testing.T) {
		args, environ := run(t, "", "")
		if !equalStrings(args, piModelArgs) {
			t.Fatalf("参数不应变化:\n得到 %v\n期望 %v", args, piModelArgs)
		}
		if value, ok := testEnvValue(environ, MagpieEnvVar); ok {
			t.Fatalf("不应设置 %s: %q", MagpieEnvVar, value)
		}
	})

	t.Run("注入时追加桥扩展与环境变量", func(t *testing.T) {
		args, environ := run(t, "/tmp/magpie.ts", endpoints)
		want := append(append([]string{}, piModelArgs...), "-e", "/tmp/magpie.ts")
		if !equalStrings(args, want) {
			t.Fatalf("参数错误:\n得到 %v\n期望 %v", args, want)
		}
		if value, ok := testEnvValue(environ, MagpieEnvVar); !ok || value != endpoints {
			t.Fatalf("%s 环境变量错误: %q, %v", MagpieEnvVar, value, ok)
		}
	})

	t.Run("只有一半时也不注入", func(t *testing.T) {
		for _, item := range []struct{ bridge, magpieEnv string }{{"/tmp/magpie.ts", ""}, {"", endpoints}} {
			args, environ := run(t, item.bridge, item.magpieEnv)
			if !equalStrings(args, piModelArgs) {
				t.Fatalf("参数不应变化: %v", args)
			}
			if _, ok := testEnvValue(environ, MagpieEnvVar); ok {
				t.Fatal("不应设置 MCODE_PI_MAGPIE")
			}
		}
	})
}

// testEnvValue 取 KEY=VALUE 列表里的取值；不存在时 ok 为 false。
func testEnvValue(environ []string, key string) (string, bool) {
	prefix := key + "="
	for _, entry := range environ {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix), true
		}
	}
	return "", false
}

// equalStrings 比较两个字符串切片。
func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestParseModelsContextWindow 锁定上下文窗口的取数顺序：模型自报的值优先，
// 缺失或非正数时回落到 models-store.json 的同一模型，两处都没有就是 0。
// 这条链断了，界面上的上下文占用圆环对 pi 模型恒定无数据。
func TestParseModelsContextWindow(t *testing.T) {
	cases := []struct {
		name    string
		model   string
		fetched string
		want    int
	}{
		{
			name:  "只有模型自报的 contextWindow",
			model: `{"provider":"gateway","id":"m","contextWindow":200000}`,
			want:  200000,
		},
		{
			name:    "模型没报时回落到 fetched",
			model:   `{"provider":"gateway","id":"m"}`,
			fetched: `{"gateway":{"models":[{"id":"m","provider":"gateway","contextWindow":1000000}]}}`,
			want:    1000000,
		},
		{
			name:    "模型自报的值优先于 fetched",
			model:   `{"provider":"gateway","id":"m","contextWindow":200000}`,
			fetched: `{"gateway":{"models":[{"id":"m","provider":"gateway","contextWindow":1000000}]}}`,
			want:    200000,
		},
		{
			name:    "两处都没有时为 0",
			model:   `{"provider":"gateway","id":"m"}`,
			fetched: `{"gateway":{"models":[{"id":"m","provider":"gateway"}]}}`,
			want:    0,
		},
		{
			name:  "非正数的 contextWindow 视为未知",
			model: `{"provider":"gateway","id":"m","contextWindow":0}`,
			want:  0,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var fetched map[string]json.RawMessage
			if testCase.fetched != "" {
				fetched = ParseFetchedModels([]byte(testCase.fetched))
			}
			raw := []byte(`{"models":[` + testCase.model + `]}`)
			models, err := ParseModels(raw, fetched, nil)
			if err != nil {
				t.Fatalf("ParseModels 失败: %v", err)
			}
			if len(models) != 1 {
				t.Fatalf("期望 1 个模型，实际 %+v", models)
			}
			if models[0].ContextWindow != testCase.want {
				t.Errorf("ContextWindow = %d，期望 %d", models[0].ContextWindow, testCase.want)
			}
		})
	}
}
