package textgen

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// writeFakeEngineScript 写入一个假引擎脚本：把收到的参数逐行写入 argsPath，
// 并按 --output-last-message 指向的文件写出结果（codex 这样取输出）。
func writeFakeEngineScript(t *testing.T) (scriptPath, argsPath string) {
	t.Helper()
	dir := t.TempDir()
	scriptPath = filepath.Join(dir, "engine")
	argsPath = filepath.Join(dir, "args.txt")
	script := "#!/bin/sh\n" +
		"prev=''\n" +
		"out=''\n" +
		"for a in \"$@\"; do\n" +
		"  printf '%s\\n' \"$a\" >> '" + argsPath + "'\n" +
		"  if [ \"$prev\" = '--output-last-message' ]; then out=\"$a\"; fi\n" +
		"  prev=\"$a\"\n" +
		"done\n" +
		"cat >/dev/null\n" +
		"if [ -n \"$out\" ]; then printf 'generated' > \"$out\"; fi\n" +
		"printf 'generated'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake engine script: %v", err)
	}
	return scriptPath, argsPath
}

func readEngineArgs(t *testing.T, argsPath string) []string {
	t.Helper()
	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read engine args: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// hasArgSequence 判断 args 中是否存在连续等于 want 的片段。
func hasArgSequence(args, want []string) bool {
	if len(want) == 0 {
		return true
	}
	for i := 0; i+len(want) <= len(args); i++ {
		if slices.Equal(args[i:i+len(want)], want) {
			return true
		}
	}
	return false
}

func TestGenerateWithOptionsCLIInjection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake engine binary is a shell script, unavailable on Windows")
	}
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			dir := t.TempDir()
			script, argsPath := filepath.Join(dir, "engine"), filepath.Join(dir, "args")
			content := "#!/bin/sh\n" +
				"prev=''\n" +
				"out=''\n" +
				"for a in \"$@\"; do\n" +
				"  printf '%s\\n' \"$a\" >> '" + argsPath + "'\n" +
				"  if [ \"$prev\" = '--output-last-message' ]; then out=\"$a\"; fi\n" +
				"  prev=\"$a\"\n" +
				"done\n" +
				"cat >/dev/null\n" +
				"if [ -n \"$out\" ]; then printf '%s' \"$TEXTGEN_TEST_KEY\" > \"$out\"; else printf '%s' \"$TEXTGEN_TEST_KEY\"; fi\n"
			if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
				t.Fatal(err)
			}
			opts := Options{Provider: provider, BinaryPath: script, ExtraEnv: []string{"TEXTGEN_TEST_KEY=secret"}}
			if provider == "codex" {
				opts.ExtraArgs = []string{"-c", `model_provider="mcode-magpie"`}
			}
			out, err := GenerateWithOptions(context.Background(), opts, "prompt")
			if err != nil || out != "secret" {
				t.Fatalf("输出 = %q，错误 = %v", out, err)
			}
			args := readEngineArgs(t, argsPath)
			if strings.Contains(strings.Join(args, " "), "secret") {
				t.Fatal("密钥不应出现在参数中")
			}
			if provider == "codex" {
				idx := slices.Index(args, "-c")
				if args[0] != "exec" || idx <= 0 || idx+1 >= len(args)-1 || !hasArgSequence(args[idx:], opts.ExtraArgs) || args[len(args)-1] != "-" {
					t.Errorf("额外参数的位置不正确: %q", args)
				}
			}
		})
	}
}

func TestGenerateWithOptionsEffort(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake engine binary is a shell script, unavailable on Windows")
	}

	cases := []struct {
		name     string
		provider string
		want     []string
		absent   string
	}{
		{"claude 用 --effort", "claude", []string{"--effort", "high"}, "--effort"},
		{"pi 用 --thinking", "pi", []string{"--thinking", "high"}, "--thinking"},
		{"codex 用 model_reasoning_effort", "codex", []string{"--config", `model_reasoning_effort="high"`}, "high"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script, argsPath := writeFakeEngineScript(t)
			out, err := GenerateWithOptions(context.Background(), Options{
				Provider:   tc.provider,
				Model:      "some-model",
				Effort:     "high",
				BinaryPath: script,
			}, "prompt")
			if err != nil {
				t.Fatalf("GenerateWithOptions(%s) failed: %v", tc.provider, err)
			}
			if out != "generated" {
				t.Errorf("output = %q, want %q", out, "generated")
			}
			if args := readEngineArgs(t, argsPath); !hasArgSequence(args, tc.want) {
				t.Errorf("%s 未透传 effort: args = %q, want sequence %q", tc.provider, args, tc.want)
			}

			// 未指定 effort 时不额外传参（codex 保持原有的 low 默认值）。
			script, argsPath = writeFakeEngineScript(t)
			if _, err := GenerateWithOptions(context.Background(), Options{
				Provider:   tc.provider,
				Model:      "some-model",
				BinaryPath: script,
			}, "prompt"); err != nil {
				t.Fatalf("GenerateWithOptions(%s) without effort failed: %v", tc.provider, err)
			}
			args := readEngineArgs(t, argsPath)
			if slices.Contains(args, tc.absent) {
				t.Errorf("%s 默认不应传 %q: args = %q", tc.provider, tc.absent, args)
			}
		})
	}

	// codex 的默认推理强度仍是 low，保持原有行为。
	t.Run("codex 默认 low", func(t *testing.T) {
		script, argsPath := writeFakeEngineScript(t)
		if _, err := GenerateWithOptions(context.Background(), Options{Provider: "codex", Model: "m", BinaryPath: script}, "p"); err != nil {
			t.Fatalf("GenerateWithOptions(codex) failed: %v", err)
		}
		if args := readEngineArgs(t, argsPath); !hasArgSequence(args, []string{"--config", `model_reasoning_effort="low"`}) {
			t.Errorf("codex 默认 effort 应为 low: args = %q", args)
		}
	})
}

func TestChatCompletionsURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://api.openai.com/v1", "https://api.openai.com/v1/chat/completions"},
		{"https://api.openai.com/v1/", "https://api.openai.com/v1/chat/completions"},
		{"  https://proxy.local/v1  ", "https://proxy.local/v1/chat/completions"},
		{"https://proxy.local/v1/chat/completions", "https://proxy.local/v1/chat/completions"},
		{"https://proxy.local/v1/chat/completions/", "https://proxy.local/v1/chat/completions"},
		{"https://proxy.local/openai/deployments/d/chat/completions", "https://proxy.local/openai/deployments/d/chat/completions"},
	}
	for _, tc := range cases {
		if got := chatCompletionsURL(tc.in); got != tc.want {
			t.Errorf("chatCompletionsURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGenerateOpenAICompatibleEffortAndURL(t *testing.T) {
	var paths []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"  ok  "}}]}`))
	}))
	defer srv.Close()

	// BaseURL 已指向 /chat/completions：直接使用，不重复拼接。
	out, err := GenerateOpenAICompatibleWithOptions(context.Background(), OpenAICompatibleOptions{
		BaseURL: srv.URL + "/v1/chat/completions/",
		APIKey:  "k",
		Model:   "m",
		Effort:  "high",
	}, "p")
	if err != nil || out != "ok" {
		t.Fatalf("out=%q err=%v", out, err)
	}

	// 只给到 /v1：补全 /chat/completions；未指定 effort 时不带 reasoning_effort。
	if _, err := GenerateOpenAICompatible(context.Background(), srv.URL+"/v1/", "k", "m", "p"); err != nil {
		t.Fatalf("GenerateOpenAICompatible failed: %v", err)
	}

	if want := []string{"/v1/chat/completions", "/v1/chat/completions"}; !slices.Equal(paths, want) {
		t.Fatalf("request paths = %q, want %q", paths, want)
	}
	if got := bodies[0]["reasoning_effort"]; got != "high" {
		t.Errorf("reasoning_effort = %v, want high (body=%v)", got, bodies[0])
	}
	if _, ok := bodies[1]["reasoning_effort"]; ok {
		t.Errorf("未指定 effort 时不应带 reasoning_effort: body=%v", bodies[1])
	}
}

func TestTruncateUTF8KeepsRuneBoundary(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"abc", 10, "abc"},
		{"中文字", 4, "中"},
		{"中文字", 5, "中"},
		{"中文字", 6, "中文"},
		{"中文字", 9, "中文字"},
		{"", 3, ""},
		{"中文", 0, ""},
		{"中文", -1, ""},
	}
	for _, tc := range cases {
		got := truncateUTF8(tc.in, tc.max)
		if got != tc.want {
			t.Errorf("truncateUTF8(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("truncateUTF8(%q, %d) 产生了非法 UTF-8: %q", tc.in, tc.max, got)
		}
	}

	// 字节切点落在多字节字符中间时必须回退，不能切出半个字符。
	mixed := "x" + strings.Repeat("中a", 4000)
	got := truncateUTF8(mixed, 12000)
	if !utf8.ValidString(got) {
		t.Fatalf("truncateUTF8 结果不是合法 UTF-8")
	}
	if len(got) > 12000 || len(got) < 11997 {
		t.Errorf("截断长度异常: %d", len(got))
	}
}

// TestPromptsTruncateOnRuneBoundary 验证提示词的截断不会产生非法 UTF-8（乱码）。
func TestPromptsTruncateOnRuneBoundary(t *testing.T) {
	// 奇数字节前缀让各处字节上限落在中文字符中间。
	mixed := "x" + strings.Repeat("中a", 5000)
	stat := "y" + strings.Repeat("中文", 2000)

	prompt := CommitMessagePrompt(stat, mixed, "")
	if !utf8.ValidString(prompt) {
		t.Fatalf("CommitMessagePrompt 产生了非法 UTF-8")
	}
	if len(prompt) > 3000+12000+2000 {
		t.Errorf("CommitMessagePrompt 过长: %d", len(prompt))
	}

	title := TitlePromptWithAttachments(mixed, mixed, nil)
	if !utf8.ValidString(title) {
		t.Fatalf("TitlePromptWithAttachments 产生了非法 UTF-8")
	}
	if len(title) > 2000*2+2000 {
		t.Errorf("TitlePromptWithAttachments 过长: %d", len(title))
	}

	branch := BranchNamePrompt(mixed)
	if !utf8.ValidString(branch) {
		t.Fatalf("BranchNamePrompt 产生了非法 UTF-8")
	}
	if len(branch) > 2000+1000 {
		t.Errorf("BranchNamePrompt 过长: %d", len(branch))
	}
}

// TestGenerateWithOptionsPiExtensionsAndEnv 验证 pi 收到 -e 扩展参数与追加的环境变量。
func TestGenerateWithOptionsPiExtensionsAndEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake engine binary is a shell script, unavailable on Windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "pi")
	argsPath := filepath.Join(dir, "args.txt")
	content := "#!/bin/sh\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> '" + argsPath + "'; done\n" +
		"cat >/dev/null\n" +
		"printf '%s' \"$FOO_MAGPIE\"\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake pi script: %v", err)
	}

	const model = "magpie-jp/auto/claude-sonnet-4"
	out, err := GenerateWithOptions(context.Background(), Options{
		Provider:   "pi",
		Model:      model,
		Extensions: []string{"/tmp/bridge.ts"},
		ExtraEnv:   []string{"FOO_MAGPIE=bar"},
		BinaryPath: script,
	}, "prompt")
	if err != nil {
		t.Fatalf("GenerateWithOptions(pi) failed: %v", err)
	}
	if out != "bar" {
		t.Errorf("子进程未看到 FOO_MAGPIE=bar: output = %q", out)
	}

	args := readEngineArgs(t, argsPath)
	idx := slices.Index(args, "--no-extensions")
	if idx < 0 {
		t.Fatalf("参数里缺少 --no-extensions: %q", args)
	}
	if !hasArgSequence(args[idx+1:], []string{"-e", "/tmp/bridge.ts"}) {
		t.Errorf("扩展应在 --no-extensions 之后以 -e 追加: args = %q", args)
	}
	modelIdx := slices.Index(args, "--model")
	if modelIdx < 0 || modelIdx+1 >= len(args) {
		t.Fatalf("参数里缺少 --model: %q", args)
	}
	if args[modelIdx+1] != model {
		t.Errorf("--model 应为整串 %q，得到 %q", model, args[modelIdx+1])
	}
}
