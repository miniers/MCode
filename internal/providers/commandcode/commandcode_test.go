package commandcode

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mcode/internal/harness"
)

func TestKind(t *testing.T) {
	h := New(Options{})
	if got := h.Kind(); got != harness.Kind("commandcode") {
		t.Fatalf("Kind 应为 commandcode，得到 %q", got)
	}
	if !h.ConfirmsApprovalDelivery() {
		t.Fatal("回复文件写完后会 AckDelivery，ConfirmsApprovalDelivery 应为 true")
	}
}

func TestBuildArgs(t *testing.T) {
	const modPath = "/tmp/mcode/approvals/mcode-approvals.ts"
	base := []string{
		"-p", "--output-format", "json", "--skip-onboarding", "--no-auto-update",
		"--trust", "--tools-enable", "todo_write", "--max-turns", "400",
	}
	withBase := func(rest ...string) []string {
		return append(slices.Clone(base), rest...)
	}

	tests := []struct {
		name      string
		turn      harness.Turn
		sessionID string
		modPath   string
		extra     []string
		want      []string
	}{
		{
			name:      "监督模式带模型、effort 与会话",
			turn:      harness.Turn{Model: "claude-sonnet-5", Effort: "xhigh", Mode: harness.ModeSupervised},
			sessionID: "sess-1",
			modPath:   modPath,
			want: withBase(
				"--model", "claude-sonnet-5", "--effort", "xhigh",
				"--yolo", "--mod", modPath, "--resume", "sess-1",
			),
		},
		{
			name:    "表外模型不带 effort",
			turn:    harness.Turn{Model: "meituan/longcat-2.0:free", Effort: "high", Mode: harness.ModeSupervised},
			modPath: modPath,
			want:    withBase("--model", "meituan/longcat-2.0:free", "--yolo", "--mod", modPath),
		},
		{
			name:    "模型名大小写不敏感",
			turn:    harness.Turn{Model: "Claude-Sonnet-5", Effort: "max", Mode: harness.ModeSupervised},
			modPath: modPath,
			want:    withBase("--model", "Claude-Sonnet-5", "--effort", "max", "--yolo", "--mod", modPath),
		},
		{
			name:    "effort 不在该模型的档位里",
			turn:    harness.Turn{Model: "claude-sonnet-5", Effort: "ultra", Mode: harness.ModeSupervised},
			modPath: modPath,
			want:    withBase("--model", "claude-sonnet-5", "--yolo", "--mod", modPath),
		},
		{
			name:    "计划模式用 CLI 的只读模式",
			turn:    harness.Turn{InteractionMode: "plan", Mode: harness.ModeSupervised},
			modPath: modPath,
			want:    withBase("--permission-mode", "plan", "--mod", modPath),
		},
		{
			name:    "完全访问不加 mod",
			turn:    harness.Turn{Mode: harness.ModeFullAccess},
			modPath: modPath,
			want:    withBase("--yolo"),
		},
		{
			name:    "完全访问的计划模式同样不加 mod",
			turn:    harness.Turn{InteractionMode: "plan", Mode: harness.ModeFullAccess},
			modPath: modPath,
			want:    withBase("--permission-mode", "plan"),
		},
		{
			name:    "没有 mod 路径时不带 --mod",
			turn:    harness.Turn{Mode: harness.ModeSupervised},
			modPath: "",
			want:    withBase("--yolo"),
		},
		{
			name:    "ExtraArgs 追加在末尾",
			turn:    harness.Turn{Mode: harness.ModeSupervised},
			modPath: modPath,
			extra:   []string{"--verbose"},
			want:    withBase("--yolo", "--mod", modPath, "--verbose"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opts := Options{ExtraArgs: test.extra}
			got := buildArgs(opts, test.turn, test.sessionID, test.modPath, "")
			if !slices.Equal(got, test.want) {
				t.Fatalf("参数不符:\n得到 %v\n期望 %v", got, test.want)
			}
		})
	}
}

func TestTakesEffort(t *testing.T) {
	tests := []struct {
		model  string
		effort string
		want   bool
	}{
		{"claude-sonnet-5", "xhigh", true},
		{"Claude-Sonnet-5", "max", true},
		{"deepseek/deepseek-v4.1-flash", "low", true},
		{"deepseek/deepseek-v4.1-flash", "xhigh", false},
		{"meituan/longcat-2.0:free", "high", false},
		{"", "high", false},
	}
	for _, test := range tests {
		if got := takesEffort(test.effort, test.model); got != test.want {
			t.Errorf("takesEffort(%q, %q) = %v，期望 %v", test.effort, test.model, got, test.want)
		}
	}
}

// newFakeCommand 写一个 shell 替身脚本充当 `cmd`：记录参数、把 stdin 的 prompt 落盘、
// 回放 fixture 行，最后按给定退出码退出。不启动真实的 cmd，也不联网。
func newFakeCommand(t *testing.T, fixture, exitCode string) (binary, argsFile, promptFile string, env []string) {
	t.Helper()
	dir := t.TempDir()
	absoluteFixture, err := filepath.Abs(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("解析 fixture 路径失败: %v", err)
	}
	argsFile = filepath.Join(dir, "args.txt")
	promptFile = filepath.Join(dir, "prompt.txt")
	binary = filepath.Join(dir, "cmd")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > \"$FAKE_CMD_ARGS\"\n" +
		"cat > \"$FAKE_CMD_PROMPT\"\n" +
		"cat \"$FAKE_CMD_FIXTURE\"\n" +
		"exit \"${FAKE_CMD_EXIT:-0}\"\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatalf("写入替身脚本失败: %v", err)
	}
	env = []string{
		"FAKE_CMD_ARGS=" + argsFile,
		"FAKE_CMD_PROMPT=" + promptFile,
		"FAKE_CMD_FIXTURE=" + absoluteFixture,
		"FAKE_CMD_EXIT=" + exitCode,
	}
	return binary, argsFile, promptFile, env
}

// readLines 读取逐行落盘的文本文件。
func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// containsSequence 判断 haystack 里是否连续出现 needle。
func containsSequence(haystack, needle []string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if slices.Equal(haystack[i:i+len(needle)], needle) {
			return true
		}
	}
	return false
}

func TestRunWithFakeCommand(t *testing.T) {
	binary, argsFile, promptFile, env := newFakeCommand(t, "events_success.ndjson", "0")
	h := New(Options{Binary: binary, Env: env})

	var events []harness.Event
	turn := harness.Turn{
		ID:        "turn-1",
		WorkDir:   t.TempDir(),
		Prompt:    "看一下 main.go",
		SessionID: "old-sess",
		Mode:      harness.ModeSupervised,
	}
	if err := h.Run(context.Background(), turn, func(event harness.Event) { events = append(events, event) }); err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}

	args := readLines(t, argsFile)
	for _, want := range [][]string{
		{"-p"},
		{"--output-format", "json"},
		{"--skip-onboarding"},
		{"--no-auto-update"},
		{"--trust"},
		{"--tools-enable", "todo_write"},
		{"--max-turns", "400"},
		{"--yolo"},
		{"--resume", "old-sess"},
	} {
		if !containsSequence(args, want) {
			t.Fatalf("参数缺少 %v: %v", want, args)
		}
	}
	modIndex := slices.Index(args, "--mod")
	if modIndex < 0 || modIndex+1 >= len(args) || !strings.HasSuffix(args[modIndex+1], "mcode-approvals.ts") {
		t.Fatalf("--mod 应指向审批 mod 文件: %v", args)
	}

	// prompt 写到 stdin 后关闭：替身脚本读到的内容应与输入一致。
	promptData, err := os.ReadFile(promptFile)
	if err != nil || string(promptData) != "看一下 main.go" {
		t.Fatalf("prompt 应原样写到 stdin，得到 %q (err=%v)", promptData, err)
	}

	wantTypes := []harness.EventType{
		harness.EventSession,
		harness.EventReasoning,
		harness.EventText,
		harness.EventText,
		harness.EventToolStart,
		harness.EventToolEnd,
		harness.EventUsage,
		harness.EventDone,
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("事件数量应为 %d，得到 %d: %+v", len(wantTypes), len(events), events)
	}
	for i, want := range wantTypes {
		if events[i].Type != want {
			t.Fatalf("第 %d 个事件应为 %s，得到 %s", i, want, events[i].Type)
		}
	}
	if events[0].SessionID != "cli-sess-1" {
		t.Fatalf("首个事件应带 CLI 会话 id，得到 %+v", events[0])
	}
	if events[7].StopReason != "completed" {
		t.Fatalf("结束原因应为 completed，得到 %q", events[7].StopReason)
	}

	// CLI 报出的新会话 id 已按旧 id 记住，下一轮 resume 到它。
	if got := h.resolveSession("old-sess"); got != "cli-sess-1" {
		t.Fatalf("应记住 CLI 会话 id 映射，得到 %q", got)
	}
}

func TestRunInterruptedOnExit130(t *testing.T) {
	binary, _, _, env := newFakeCommand(t, "events_interrupted.ndjson", "130")
	h := New(Options{Binary: binary, Env: env})

	var events []harness.Event
	turn := harness.Turn{
		ID:      "turn-9",
		WorkDir: t.TempDir(),
		Prompt:  "停一下",
		Mode:    harness.ModeSupervised,
	}
	if err := h.Run(context.Background(), turn, func(event harness.Event) { events = append(events, event) }); err != nil {
		t.Fatalf("退出码 130 是用户中止，不应是错误: %v", err)
	}
	for _, event := range events {
		if event.Type == harness.EventError {
			t.Fatalf("中止不应发错误事件: %+v", event)
		}
	}
	last := events[len(events)-1]
	if last.Type != harness.EventDone || last.StopReason != "interrupted" {
		t.Fatalf("中止应以 interrupted 收尾，得到 %+v", last)
	}
}

func TestForceStopTurn(t *testing.T) {
	h := New(Options{})
	// 未命中任何活动轮次时不应 panic。
	h.ForceStopTurn("missing")

	// cmd 为 nil 时 killProcessGroup 直接返回，这里只验证注册与收尾路径。
	run := &activeRun{turnID: "turn-1", done: make(chan struct{})}
	h.registerRun(run)
	h.ForceStopTurn("turn-1")
	close(run.done)
	h.unregisterRun(run)
	if len(h.runs) != 0 {
		t.Fatal("强停后活动轮次应已收尾")
	}
}

func TestResolveAndRememberSession(t *testing.T) {
	h := New(Options{})
	if got := h.resolveSession(""); got != "" {
		t.Fatalf("空会话 id 应原样返回，得到 %q", got)
	}
	if got := h.resolveSession("sess-a"); got != "sess-a" {
		t.Fatalf("没有映射时应原样返回，得到 %q", got)
	}
	h.rememberSession("sess-a", "cli-a")
	if got := h.resolveSession("sess-a"); got != "cli-a" {
		t.Fatalf("应返回记住的 CLI 会话 id，得到 %q", got)
	}
	// 首个 turn 的 SessionID 为空，调用方通过 EventSession 把新 id 带回来，因此空键不记。
	h.rememberSession("", "cli-b")
	if got := h.resolveSession(""); got != "" {
		t.Fatalf("空键不应记录映射，得到 %q", got)
	}
}

func TestGrantsFollowNewSessionID(t *testing.T) {
	h := New(Options{})
	grants := h.grantsFor("old")
	grants["command"] = true
	h.grantsFollow("old", "cli-new")
	if !h.grantsFor("cli-new")["command"] {
		t.Fatal("会话级放行应跟着 CLI 新报出的会话 id 继续生效")
	}
}

func TestGrantsForEmptySessionAreIsolated(t *testing.T) {
	h := New(Options{})
	first := h.grantsFor("")
	first["command"] = true
	if h.grantsFor("")["command"] {
		t.Fatal("首个 turn 的会话级放行不应泄漏给其他新会话")
	}
	h.adoptGrants("cli-first", first)
	if !h.grantsFor("cli-first")["command"] {
		t.Fatal("拿到 CLI 会话 id 后，放行应登记到该会话名下")
	}
	if h.grantsFor("cli-other")["command"] {
		t.Fatal("其他会话不应继承该放行")
	}
}

func TestPromptTextListsExistingImages(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(existing, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := promptText(harness.Turn{
		Prompt: "看这张图",
		Images: []string{existing, filepath.Join(dir, "missing.png")},
	})
	want := "看这张图\n\nAttached files (read them with read_file):\n- " + existing
	if got != want {
		t.Fatalf("附件列表不符:\n得到 %q\n期望 %q", got, want)
	}
	if got := promptText(harness.Turn{Prompt: "你好"}); got != "你好" {
		t.Fatalf("没有附件时 prompt 应原样返回，得到 %q", got)
	}
}
