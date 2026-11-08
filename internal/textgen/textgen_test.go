package textgen

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestCheapestModel(t *testing.T) {
	if got := CheapestModel("claude"); got != "haiku" {
		t.Errorf("CheapestModel(claude) = %q, want %q", got, "haiku")
	}
	if got := CheapestModel("codex"); got != "gpt-5.4-mini" {
		t.Errorf("CheapestModel(codex) = %q, want %q", got, "gpt-5.4-mini")
	}
	if got := CheapestModel("memory"); got != "echo" {
		t.Errorf("CheapestModel(memory) = %q, want %q", got, "echo")
	}
	if got := CheapestModel("mcode"); got != "default" {
		t.Errorf("CheapestModel(mcode) = %q, want %q", got, "default")
	}
	if got := CheapestModel("unknown"); got != "" {
		t.Errorf("CheapestModel(unknown) = %q, want empty", got)
	}
}

func TestTitlePrompt(t *testing.T) {
	prompt := TitlePrompt("请帮我重构 internal/config 包", "好的，我已经分析了 config 包结构")
	if !strings.Contains(prompt, "重构 internal/config 包") {
		t.Errorf("prompt missing user message: %s", prompt)
	}
	if !strings.Contains(prompt, "已经分析了 config 包结构") {
		t.Errorf("prompt missing assistant reply: %s", prompt)
	}
	if !strings.Contains(prompt, "30 个汉字") {
		t.Errorf("prompt missing length rule: %s", prompt)
	}

	// 验证超长文本截断
	longUser := strings.Repeat("用户输入", 1000)
	longPrompt := TitlePrompt(longUser, "")
	if len(longPrompt) > 5000 {
		t.Errorf("long prompt not truncated properly: len=%d", len(longPrompt))
	}
}

func TestCommitMessagePrompt(t *testing.T) {
	prompt := CommitMessagePrompt("1 file changed, 10 insertions(+)", "diff --git a/foo.go b/foo.go", "遵守 Conventional Commits")
	if !strings.Contains(prompt, "1 file changed") {
		t.Errorf("prompt missing diffStat: %s", prompt)
	}
	if !strings.Contains(prompt, "diff --git") {
		t.Errorf("prompt missing diffPatch: %s", prompt)
	}
	if !strings.Contains(prompt, "遵守 Conventional Commits") {
		t.Errorf("prompt missing instructions: %s", prompt)
	}
	if !strings.Contains(prompt, "72 个字符") {
		t.Errorf("prompt missing 72 chars rule: %s", prompt)
	}
}

func TestBranchNamePrompt(t *testing.T) {
	prompt := BranchNamePrompt("修复登录页面白屏问题")
	if !strings.Contains(prompt, "修复登录页面白屏问题") {
		t.Errorf("prompt missing taskText: %s", prompt)
	}
	if !strings.Contains(prompt, "kebab-case") {
		t.Errorf("prompt missing kebab-case rule: %s", prompt)
	}
}

func TestCleanTitle(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{
			input: `"用户登录逻辑重构"`,
			want:  "用户登录逻辑重构",
		},
		{
			input: "标题：优化数据库连接池配置。",
			want:  "优化数据库连接池配置",
		},
		{
			input: "Title: 《修复文件保存错误》！",
			want:  "修复文件保存错误",
		},
		{
			input: "第一行简要标题\n第二行多余解释和详细步骤\n第三行",
			want:  "第一行简要标题",
		},
		{
			input: "这是一个非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常长的标题输出超过三十个汉字",
			want:  "这是一个非常非常非常非常非常非常非常非常非常非常非常非常非常", // 正好 30 字
		},
		{
			input: "好的，为您生成的标题是：登录重构",
			want:  "", // 包含无效开头“好的”
		},
		{
			input: "抱歉，需要更多上下文才能生成标题",
			want:  "", // 包含“抱歉”与“更多上下文”
		},
		{
			input: "   ",
			want:  "",
		},
	}

	for _, tc := range tests {
		got := CleanTitle(tc.input)
		if got != tc.want {
			t.Errorf("CleanTitle(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestCleanCommitMessage(t *testing.T) {
	inputWithFences := "```git\nfeat(config): add utility model role\n\n- support utility in ModelRoles\n```"
	got := CleanCommitMessage(inputWithFences)
	want := "feat(config): add utility model role\n\n- support utility in ModelRoles"
	if got != want {
		t.Errorf("CleanCommitMessage failed:\ngot:  %q\nwant: %q", got, want)
	}

	inputPlain := "fix: handle nil pointer on empty diff\n\nCloses #123"
	gotPlain := CleanCommitMessage(inputPlain)
	if gotPlain != inputPlain {
		t.Errorf("CleanCommitMessage plain failed:\ngot:  %q\nwant: %q", gotPlain, inputPlain)
	}
}

func TestCleanBranchName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{
			input: "fix-login-error",
			want:  "fix-login-error",
		},
		{
			input: "Fix Login Error & Crash!",
			want:  "fix-login-error-crash",
		},
		{
			input: "mcode/feature-task-ui",
			want:  "feature-task-ui",
		},
		{
			input: "`add_new_feature_for_user_auth`",
			want:  "add-new-feature-for-user-auth",
		},
	}

	for _, tc := range tests {
		got := CleanBranchName(tc.input)
		if got != tc.want {
			t.Errorf("CleanBranchName(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestGenerateMemory(t *testing.T) {
	out, err := Generate(context.Background(), "memory", "", "测试提示词")
	if err != nil {
		t.Fatalf("Generate memory failed: %v", err)
	}
	if out == "" {
		t.Errorf("Generate memory returned empty string")
	}
}

// writeFakePiScript 在临时目录写入一个假 pi 脚本，它会保存参数与 stdin 并输出 "generated"。
func writeFakePiScript(t *testing.T) (scriptPath, argsPath, stdinPath string) {
	t.Helper()
	dir := t.TempDir()
	scriptPath = filepath.Join(dir, "pi")
	argsPath = filepath.Join(dir, "args.txt")
	stdinPath = filepath.Join(dir, "stdin.txt")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > '" + argsPath + "'\n" +
		"cat > '" + stdinPath + "'\n" +
		"printf 'generated'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake pi script: %v", err)
	}
	return scriptPath, argsPath, stdinPath
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestGeneratePiUsesStdinAndFlags(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake pi binary is a shell script, unavailable on Windows")
	}

	t.Run("with model", func(t *testing.T) {
		scriptPath, argsPath, stdinPath := writeFakePiScript(t)

		out, err := Generate(context.Background(), "pi", "anthropic/haiku", "hello prompt", scriptPath)
		if err != nil {
			t.Fatalf("Generate pi failed: %v", err)
		}
		if out != "generated" {
			t.Errorf("Generate pi output = %q, want %q", out, "generated")
		}

		args := strings.Split(strings.TrimSpace(readFileString(t, argsPath)), "\n")
		for _, want := range []string{"-p", "--no-tools", "--no-session", "--model", "anthropic/haiku"} {
			if !slices.Contains(args, want) {
				t.Errorf("args missing %q: %q", want, args)
			}
		}

		if got := readFileString(t, stdinPath); got != "hello prompt" {
			t.Errorf("stdin = %q, want %q", got, "hello prompt")
		}
	})

	t.Run("default model", func(t *testing.T) {
		scriptPath, argsPath, _ := writeFakePiScript(t)

		if _, err := Generate(context.Background(), "pi", "default", "hello prompt", scriptPath); err != nil {
			t.Fatalf("Generate pi failed: %v", err)
		}

		args := strings.Split(strings.TrimSpace(readFileString(t, argsPath)), "\n")
		if slices.Contains(args, "--model") {
			t.Errorf("args must not contain --model: %q", args)
		}
	})
}
