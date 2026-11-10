package pi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/harness"
	"mcode/internal/procutil"
)

// TestPiRunHelperProcess 是 Run 测试里替换 pi 的假进程（GO_WANT_PI_RUN_HELPER=1 时生效，
// 否则正常测试运行时直接返回）：记录命令行参数，并按 fixture 回放事件流。
//
// 关键在时间顺序：把 stdout 分成两段写。先只写 fixture 第一行（get_state 的应答），
// 等 Harness 收到应答、把 prompt 写进 stdin 并落盘后，才写 fixture 的其余行（含结束事件）。
// 否则 Harness 一看到结束事件就杀掉进程，prompt 那一行可能还没写进日志。
// 环境变量名与 models_test.go 的 GO_WANT_PI_HELPER 区分开，避免两个替身互相触发。
func TestPiRunHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_PI_RUN_HELPER") != "1" {
		return
	}

	// (a) 把 `--` 之后的参数逐行写进参数记录文件。
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if path := os.Getenv("FAKE_PI_ARGS_FILE"); path != "" {
		if err := os.WriteFile(path, []byte(strings.Join(args, "\n")), 0o644); err != nil {
			os.Exit(2)
		}
	}

	// (b) 记录 Harness 注入的门禁文件路径及其内容，再读 fixture 按行拆分。
	if path := os.Getenv("FAKE_PI_ENV_FILE"); path != "" {
		gatePath := os.Getenv(GateEnvVar)
		var gateData []byte
		if gatePath != "" {
			gateData, _ = os.ReadFile(gatePath)
		}
		content := gatePath + "\n" + strings.TrimSpace(string(gateData)) + "\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			os.Exit(2)
		}
	}
	data, err := os.ReadFile(os.Getenv("FAKE_PI_FIXTURE"))
	if err != nil {
		os.Exit(2)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")

	reader := bufio.NewReader(os.Stdin)
	readLine := func() string {
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return ""
		}
		return strings.TrimRight(line, "\n")
	}

	// 每次写完立即 Sync + Close，保证 Harness 杀进程时日志已落盘。
	logPath := os.Getenv("FAKE_PI_LOG_FILE")
	logRequest := func(line string) {
		if logPath == "" {
			return
		}
		handle, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			os.Exit(2)
		}
		_, _ = handle.WriteString(strings.TrimRight(line, "\n") + "\n")
		_ = handle.Sync()
		_ = handle.Close()
	}

	// (c) 读第一条请求（get_state）并落盘；若是 get_state，用它的 id 替换 fixture 里的占位符。
	first := readLine()
	logRequest(first)
	var firstRequest map[string]any
	if json.Unmarshal([]byte(first), &firstRequest) == nil && firstRequest["type"] == "get_state" {
		if id, _ := firstRequest["id"].(string); id != "" {
			for i := range lines {
				lines[i] = strings.ReplaceAll(lines[i], "__STATE_ID__", id)
			}
		}
	}

	// (d) 先只写 fixture 第一行（get_state 的应答），等 Harness 把 prompt 送来。
	if len(lines) > 0 {
		_, _ = os.Stdout.WriteString(lines[0] + "\n")
	}

	// (e) 读下一条请求（prompt）并落盘后，再写 fixture 剩余的行。
	logRequest(readLine())
	if len(lines) > 1 {
		_, _ = os.Stdout.WriteString(strings.Join(lines[1:], "\n") + "\n")
	}

	// (f) 继续读 stdin 直到 EOF，逐行落盘。
	for {
		line := readLine()
		if line == "" {
			break
		}
		logRequest(line)
	}
	os.Exit(0)
}

// fakePi 是一个不联网、不依赖真实 pi 的替身：shell 包装脚本把参数转发给 Go helper 进程。
// 用 helper 进程而不是 TestMain 重入，避免与本包其他测试文件争抢唯一的 TestMain。
type fakePi struct {
	binary   string
	argsFile string
	logFile  string
	envFile  string
	env      []string
}

func newFakePi(t *testing.T, fixture string) *fakePi {
	t.Helper()
	dir := t.TempDir()

	// 用测试二进制自身作为替身进程，靠 GO_WANT_PI_RUN_HELPER 重入到 TestPiRunHelperProcess。
	// os.Args[0] 可能是相对路径，而子进程的 cmd.Dir 是 turn.WorkDir，所以先转成绝对路径。
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("解析测试二进制路径失败: %v", err)
	}

	binary := filepath.Join(dir, "pi")
	wrapper := "#!/bin/sh\nexec '" + self + "' -test.run='^TestPiRunHelperProcess$' -- \"$@\"\n"
	if err := os.WriteFile(binary, []byte(wrapper), 0o755); err != nil {
		t.Fatalf("写入替身启动脚本失败: %v", err)
	}

	absoluteFixture, err := filepath.Abs(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("解析 fixture 路径失败: %v", err)
	}

	fake := &fakePi{
		binary:   binary,
		argsFile: filepath.Join(dir, "args.txt"),
		logFile:  filepath.Join(dir, "requests.jsonl"),
		envFile:  filepath.Join(dir, "env.txt"),
	}
	fake.env = []string{
		"GO_WANT_PI_RUN_HELPER=1",
		"FAKE_PI_FIXTURE=" + absoluteFixture,
		"FAKE_PI_ARGS_FILE=" + fake.argsFile,
		"FAKE_PI_LOG_FILE=" + fake.logFile,
		"FAKE_PI_ENV_FILE=" + fake.envFile,
	}
	return fake
}

// readGateEnv 返回替身进程收到的门禁文件路径（GateEnvVar）与启动时的文件内容。
func (f *fakePi) readGateEnv(t *testing.T) (gatePath, content string) {
	t.Helper()
	data, err := os.ReadFile(f.envFile)
	if err != nil {
		t.Fatalf("读取环境记录失败: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > 0 {
		gatePath = lines[0]
	}
	if len(lines) > 1 {
		content = lines[1]
	}
	return gatePath, content
}

// readArgs 返回替身进程收到的命令行参数。
func (f *fakePi) readArgs(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(f.argsFile)
	if err != nil {
		t.Fatalf("读取参数记录失败: %v", err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// readRequests 返回替身进程在 stdin 上收到的请求。
func (f *fakePi) readRequests(t *testing.T) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(f.logFile)
	if err != nil {
		t.Fatalf("读取请求记录失败: %v", err)
	}
	var requests []map[string]any
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var request map[string]any
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			t.Fatalf("请求行不是合法 JSON: %q", line)
		}
		requests = append(requests, request)
	}
	return requests
}

func collectEvents(events *[]harness.Event) func(harness.Event) {
	return func(event harness.Event) { *events = append(*events, event) }
}

func TestRunReplaysTurnEvents(t *testing.T) {
	fake := newFakePi(t, "rpc_text_tool_usage.jsonl")
	h := New(Options{Binary: fake.binary, Env: fake.env})

	var events []harness.Event
	turn := harness.Turn{
		ID:      "turn-1",
		WorkDir: t.TempDir(),
		Prompt:  "看一下 main.go",
		Mode:    harness.ModeSupervised,
	}
	if err := h.Run(context.Background(), turn, collectEvents(&events)); err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}

	wantTypes := []harness.EventType{
		harness.EventSession,
		harness.EventTurnAccepted,
		harness.EventText,
		harness.EventUsage,
		harness.EventToolStart,
		harness.EventToolEnd,
		harness.EventReasoning,
		harness.EventText,
		harness.EventUsage,
		harness.EventDone,
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("事件数量应为 %d，得到 %d: %+v", len(wantTypes), len(events), events)
	}
	for i, want := range wantTypes {
		if events[i].Type != want {
			t.Errorf("第 %d 个事件应为 %v，得到 %v", i, want, events[i].Type)
		}
	}
	if events[0].SessionID != "/tmp/pi-agent/sessions/session-1.json" {
		t.Errorf("会话事件应带 pi 回报的 sessionFile，得到 %q", events[0].SessionID)
	}

	// prompt 必须经 stdin 写出去，而不是塞进命令行参数
	var prompt map[string]any
	for _, request := range fake.readRequests(t) {
		if request["type"] == "prompt" {
			prompt = request
		}
	}
	if prompt == nil {
		t.Fatalf("未收到 prompt 请求: %+v", fake.readRequests(t))
	}
	if prompt["message"] != "看一下 main.go" {
		t.Errorf("prompt 内容错误: %v", prompt["message"])
	}

	// 当轮结束后进程应被回收，活动轮次清空
	if run := h.findRun(func(*activeRun) bool { return true }); run != nil {
		t.Errorf("Run 结束后不应残留活动轮次: %+v", run)
	}
}

// TestRunEmitsTurnAcceptedOnce 验证 prompt 写入成功后恰好上报一次 EventTurnAccepted，
// 且排在第一条正文事件之前。
func TestRunEmitsTurnAcceptedOnce(t *testing.T) {
	fake := newFakePi(t, "rpc_text_tool_usage.jsonl")
	h := New(Options{Binary: fake.binary, Env: fake.env})

	var events []harness.Event
	turn := harness.Turn{
		ID:      "turn-1",
		WorkDir: t.TempDir(),
		Prompt:  "看一下 main.go",
		Mode:    harness.ModeSupervised,
	}
	if err := h.Run(context.Background(), turn, collectEvents(&events)); err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}

	accepted, firstContent, count := -1, -1, 0
	for i, event := range events {
		switch event.Type {
		case harness.EventTurnAccepted:
			count++
			if accepted < 0 {
				accepted = i
			}
		case harness.EventText, harness.EventReasoning, harness.EventToolStart:
			if firstContent < 0 {
				firstContent = i
			}
		}
	}
	if count != 1 {
		t.Fatalf("EventTurnAccepted 应恰好出现一次，实际 %d 次: %+v", count, events)
	}
	if firstContent >= 0 && accepted > firstContent {
		t.Fatalf("EventTurnAccepted 应早于第一条正文事件: %+v", events)
	}
}

func TestRunResumesSessionAndBuildsArgs(t *testing.T) {
	fake := newFakePi(t, "rpc_session.jsonl")
	h := New(Options{Binary: fake.binary, Env: fake.env, Extension: "/tmp/ext/pi.ts"})

	var events []harness.Event
	turn := harness.Turn{
		WorkDir:   t.TempDir(),
		Prompt:    "继续",
		Model:     "openrouter/anthropic/claude-sonnet-4",
		Effort:    "high",
		Mode:      harness.ModeSupervised,
		SessionID: "/tmp/pi-agent/sessions/session-resumed.json",
	}
	if err := h.Run(context.Background(), turn, collectEvents(&events)); err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}

	wantArgs := []string{
		"--mode", "rpc",
		"-e", "/tmp/ext/pi.ts",
		"--no-approve",
		"--tools", "+grep,+find,+ls", "-xt", excludedDelegateTools,
		"--provider", "openrouter",
		"--model", "anthropic/claude-sonnet-4",
		"--thinking", "high",
		"--session", "/tmp/pi-agent/sessions/session-resumed.json",
	}
	if got := fake.readArgs(t); strings.Join(got, " ") != strings.Join(wantArgs, " ") {
		t.Fatalf("命令行参数错误:\n得到 %v\n期望 %v", got, wantArgs)
	}

	if len(events) == 0 || events[0].Type != harness.EventSession {
		t.Fatalf("应以会话事件开头: %+v", events)
	}
	if events[0].SessionID != "/tmp/pi-agent/sessions/session-resumed.json" {
		t.Errorf("会话事件应带 pi 回报的 sessionFile，得到 %q", events[0].SessionID)
	}
}

func TestRunReportsTurnError(t *testing.T) {
	fake := newFakePi(t, "rpc_error.jsonl")
	h := New(Options{Binary: fake.binary, Env: fake.env})

	var events []harness.Event
	turn := harness.Turn{WorkDir: t.TempDir(), Prompt: "写一半", Mode: harness.ModeSupervised}
	// 结束标记是 agent_end(不重试)，用量与错误都在事件里，Run 本身不算失败
	if err := h.Run(context.Background(), turn, collectEvents(&events)); err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}

	var sawError, sawDone bool
	for _, event := range events {
		if event.Type == harness.EventError && event.Err == "rate limit exceeded" {
			sawError = true
		}
		if event.Type == harness.EventDone && event.StopReason == "error" {
			sawDone = true
		}
	}
	if !sawError || !sawDone {
		t.Fatalf("应同时收到错误与带 error 原因的结束事件: %+v", events)
	}
}

func TestRunRequiresPrompt(t *testing.T) {
	h := New(Options{Binary: "pi"})
	if err := h.Run(context.Background(), harness.Turn{WorkDir: t.TempDir()}, nil); err == nil {
		t.Fatal("没有 prompt 与图片时应直接报错，而不是启动进程等待")
	}
}

func TestBuildArgs(t *testing.T) {
	tests := []struct {
		name   string
		opts   Options
		turn   harness.Turn
		mcp    string
		magpie string
		skills []string
		// fullSkills 为 true 时走完整清单模式（带 --no-skills）。
		fullSkills bool
		want       []string
	}{
		{
			name: "缺省为无审批",
			turn: harness.Turn{},
			want: []string{"--mode", "rpc", "--no-approve", "--tools", "+grep,+find,+ls", "-xt", excludedDelegateTools},
		},
		{
			name: "只读模式同样交给扩展门禁",
			turn: harness.Turn{Mode: harness.ModeReadOnly},
			want: []string{"--mode", "rpc", "--no-approve", "--tools", "+grep,+find,+ls", "-xt", excludedDelegateTools},
		},
		{
			name: "supervised 无审批",
			turn: harness.Turn{Mode: harness.ModeSupervised},
			want: []string{"--mode", "rpc", "--no-approve", "--tools", "+grep,+find,+ls", "-xt", excludedDelegateTools},
		},
		{
			name: "auto 也是 --no-approve",
			turn: harness.Turn{Mode: harness.ModeAuto},
			want: []string{"--mode", "rpc", "--no-approve", "--tools", "+grep,+find,+ls", "-xt", excludedDelegateTools},
		},
		{
			name: "fullAccess 也是 --no-approve",
			turn: harness.Turn{Mode: harness.ModeFullAccess},
			want: []string{"--mode", "rpc", "--no-approve", "--tools", "+grep,+find,+ls", "-xt", excludedDelegateTools},
		},
		{
			name:       "完整清单模式逐个带技能",
			turn:       harness.Turn{Mode: harness.ModeSupervised},
			skills:     []string{"/path/to/skill-a", "/path/to/skill-b"},
			fullSkills: true,
			want: []string{
				"--mode", "rpc", "--no-approve", "--tools", "+grep,+find,+ls", "-xt", excludedDelegateTools, "--no-skills",
				"--skill", "/path/to/skill-a",
				"--skill", "/path/to/skill-b",
			},
		},
		{
			name:       "完整清单模式列表为空也带 --no-skills",
			turn:       harness.Turn{Mode: harness.ModeSupervised},
			fullSkills: true,
			want:       []string{"--mode", "rpc", "--no-approve", "--tools", "+grep,+find,+ls", "-xt", excludedDelegateTools, "--no-skills"},
		},
		{
			name:   "旧模式没有 --no-skills",
			turn:   harness.Turn{Mode: harness.ModeSupervised},
			skills: []string{"/path/to/skill-a"},
			want: []string{
				"--mode", "rpc", "--no-approve", "--tools", "+grep,+find,+ls", "-xt", excludedDelegateTools,
				"--skill", "/path/to/skill-a",
			},
		},
		{
			name:   "显式传入项目技能",
			turn:   harness.Turn{Mode: harness.ModeSupervised},
			skills: []string{"/path/to/skill-a", "/path/to/skill-b"},
			want: []string{
				"--mode", "rpc", "--no-approve", "--tools", "+grep,+find,+ls", "-xt", excludedDelegateTools,
				"--skill", "/path/to/skill-a",
				"--skill", "/path/to/skill-b",
			},
		},
		{
			name:   "技能与额外参数同时存在",
			opts:   Options{ExtraArgs: []string{"--verbose"}},
			turn:   harness.Turn{Mode: harness.ModeSupervised},
			skills: []string{"/path/to/skill-a"},
			want: []string{
				"--mode", "rpc", "--no-approve", "--tools", "+grep,+find,+ls", "-xt", excludedDelegateTools,
				"--skill", "/path/to/skill-a",
				"--verbose",
			},
		},
		{
			name: "扩展与额外参数",
			opts: Options{Extension: "/tmp/pi-ext.ts", ExtraArgs: []string{"--verbose"}},
			turn: harness.Turn{Mode: harness.ModeSupervised},
			want: []string{"--mode", "rpc", "-e", "/tmp/pi-ext.ts", "--no-approve", "--tools", "+grep,+find,+ls", "-xt", excludedDelegateTools, "--verbose"},
		},
		{
			name:   "MCP 与 Magpie 桥跟在门禁扩展后面",
			opts:   Options{Extension: "/tmp/pi-ext.ts"},
			turn:   harness.Turn{Mode: harness.ModeSupervised},
			mcp:    "/tmp/mcp.ts",
			magpie: "/tmp/magpie.ts",
			want: []string{
				"--mode", "rpc", "-e", "/tmp/pi-ext.ts",
				"-e", "/tmp/mcp.ts", "-e", "/tmp/magpie.ts",
				"--no-approve", "--tools", "+grep,+find,+ls", "-xt", excludedDelegateTools,
			},
		},
		{
			name: "没有 Magpie 端点时不加 -e",
			turn: harness.Turn{Mode: harness.ModeSupervised},
			want: []string{"--mode", "rpc", "--no-approve", "--tools", "+grep,+find,+ls", "-xt", excludedDelegateTools},
		},
		{
			name: "provider 加模型与思考档位",
			turn: harness.Turn{
				Model:  "openrouter/anthropic/claude-sonnet-4",
				Effort: "medium",
			},
			want: []string{
				"--mode", "rpc", "--no-approve", "--tools", "+grep,+find,+ls", "-xt", excludedDelegateTools,
				"--provider", "openrouter", "--model", "anthropic/claude-sonnet-4",
				"--thinking", "medium",
			},
		},
		{
			name: "不带 provider 的裸模型名",
			turn: harness.Turn{Model: "claude-sonnet-4"},
			want: []string{"--mode", "rpc", "--no-approve", "--tools", "+grep,+find,+ls", "-xt", excludedDelegateTools, "--model", "claude-sonnet-4"},
		},
		{
			name: "恢复会话",
			turn: harness.Turn{SessionID: "/tmp/pi/session.json"},
			want: []string{"--mode", "rpc", "--no-approve", "--tools", "+grep,+find,+ls", "-xt", excludedDelegateTools, "--session", "/tmp/pi/session.json"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := buildArgs(test.opts, test.turn, test.mcp, test.magpie, test.skills, test.fullSkills)
			if strings.Join(got, " ") != strings.Join(test.want, " ") {
				t.Fatalf("参数错误:\n得到 %v\n期望 %v", got, test.want)
			}
		})
	}
}

// TestMagpieInjection 校验 Magpie 桥的注入条件：扩展路径与端点 JSON 都非空才注入，
// 任一为空时完全保持现状，pi 用它自己的配置。
func TestMagpieInjection(t *testing.T) {
	const endpoints = `[{"provider":"magpie-jp","endpoint":"432db46a","baseUrl":"https://x/v1","apiKey":"sk-test"}]`
	tests := []struct {
		name    string
		opts    Options
		want    string
		wantEnv string
	}{
		{
			name: "没有桥文件路径",
			opts: Options{MagpieEnv: func() string { return endpoints }},
		},
		{
			name: "没有 MagpieEnv 回调",
			opts: Options{MagpieBridge: "/tmp/magpie.ts"},
		},
		{
			name: "MagpieEnv 返回空串",
			opts: Options{MagpieBridge: "/tmp/magpie.ts", MagpieEnv: func() string { return "  " }},
		},
		{
			name:    "路径与端点都齐了",
			opts:    Options{MagpieBridge: "/tmp/magpie.ts", MagpieEnv: func() string { return endpoints }},
			want:    "/tmp/magpie.ts",
			wantEnv: endpoints,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bridge, envValue := test.opts.magpieInjection()
			if bridge != test.want || envValue != test.wantEnv {
				t.Fatalf("注入结果错误: 得到 %q / %q，期望 %q / %q", bridge, envValue, test.want, test.wantEnv)
			}
		})
	}
}

// TestHandleSetStatusMagpieMember 校验 Magpie 桥的账号回传：setStatus 通知解析后
// 交给 OnMagpieMember，且不回写任何应答。
func TestHandleSetStatusMagpieMember(t *testing.T) {
	setStatus := func(t *testing.T, raw string) *rpcEvent {
		t.Helper()
		event, err := parseEvent([]byte(raw))
		if err != nil {
			t.Fatalf("解析 setStatus 帧失败: %v", err)
		}
		return event
	}

	t.Run("回传账号", func(t *testing.T) {
		var got [][3]string
		h := New(Options{OnMagpieMember: func(endpoint, provider, model string) {
			got = append(got, [3]string{endpoint, provider, model})
		}})
		sink := &bufferWriteCloser{}
		h.handleExtensionUI(context.Background(), harness.Turn{}, procutil.NewLineWriter(sink), setStatus(t,
			`{"type":"extension_ui_request","id":"status-1","method":"setStatus","statusKey":"mcode-magpie-member",`+
				`"statusText":"{\"endpoint\":\"432db46a\",\"provider\":\"magpie-jp\",\"model\":\"deepseek-v4.1-flash\"}"}`))

		if len(got) != 1 || got[0] != [3]string{"432db46a", "magpie-jp", "deepseek-v4.1-flash"} {
			t.Fatalf("账号回传错误: %+v", got)
		}
		if sink.String() != "" {
			t.Fatalf("setStatus 是通知，不应写回任何应答: %q", sink.String())
		}
	})

	t.Run("其他键不触发", func(t *testing.T) {
		var called bool
		h := New(Options{OnMagpieMember: func(string, string, string) { called = true }})
		h.handleExtensionUI(context.Background(), harness.Turn{}, procutil.NewLineWriter(&bufferWriteCloser{}), setStatus(t,
			`{"type":"extension_ui_request","id":"status-2","method":"setStatus","statusKey":"other","statusText":"{}"}`))
		if called {
			t.Fatal("其他状态键不应触发账号回传")
		}
	})

	t.Run("没有回调也不报错", func(t *testing.T) {
		sink := &bufferWriteCloser{}
		New(Options{}).handleExtensionUI(context.Background(), harness.Turn{}, procutil.NewLineWriter(sink), setStatus(t,
			`{"type":"extension_ui_request","id":"status-3","method":"setStatus","statusKey":"mcode-magpie-member","statusText":"{\"endpoint\":\"e\"}"}`))
		if sink.String() != "" {
			t.Fatalf("没有回调时不应写回任何应答: %q", sink.String())
		}
	})
}

func TestWithNodeEnvProxy(t *testing.T) {
	env := withNodeEnvProxy([]string{"PATH=/bin", "NODE_OPTIONS=--max-old-space-size=512"})
	if env[1] != "NODE_OPTIONS=--max-old-space-size=512 --use-env-proxy" {
		t.Fatalf("应追加 --use-env-proxy: %v", env)
	}

	// turn.Env 里的 NODE_OPTIONS 排在后面优先，应改最后一个
	env = withNodeEnvProxy([]string{"NODE_OPTIONS=--a", "PATH=/bin", "NODE_OPTIONS=--b"})
	if env[2] != "NODE_OPTIONS=--b --use-env-proxy" || env[0] != "NODE_OPTIONS=--a" {
		t.Fatalf("应只修改最后一个 NODE_OPTIONS: %v", env)
	}

	env = withNodeEnvProxy([]string{"NODE_OPTIONS=--use-env-proxy"})
	if len(env) != 1 {
		t.Fatalf("已带代理开关时不应重复追加: %v", env)
	}

	env = withNodeEnvProxy([]string{"PATH=/bin"})
	if len(env) != 2 || env[1] != "NODE_OPTIONS=--use-env-proxy" {
		t.Fatalf("缺失时应补默认值: %v", env)
	}
}

func TestWithLoopbackNoProxy(t *testing.T) {
	// 既没有 NO_PROXY 也没有 no_proxy 时，两者均新建
	env := withLoopbackNoProxy([]string{"PATH=/bin"})
	hasUpper, hasLower := false, false
	for _, e := range env {
		if strings.HasPrefix(e, "NO_PROXY=") {
			hasUpper = true
			if e != "NO_PROXY=localhost,127.0.0.1,::1" {
				t.Fatalf("NO_PROXY 新建值错误: %s", e)
			}
		}
		if strings.HasPrefix(e, "no_proxy=") {
			hasLower = true
			if e != "no_proxy=localhost,127.0.0.1,::1" {
				t.Fatalf("no_proxy 新建值错误: %s", e)
			}
		}
	}
	if !hasUpper || !hasLower {
		t.Fatalf("应同时新建 NO_PROXY 与 no_proxy: %v", env)
	}

	// 已有项保留且不重复追加
	env = withLoopbackNoProxy([]string{"NO_PROXY=example.com,127.0.0.1", "no_proxy=localhost"})
	for _, e := range env {
		if strings.HasPrefix(e, "NO_PROXY=") {
			if e != "NO_PROXY=example.com,127.0.0.1,localhost,::1" {
				t.Fatalf("NO_PROXY 合并值错误: %s", e)
			}
		}
		if strings.HasPrefix(e, "no_proxy=") {
			if e != "no_proxy=localhost,127.0.0.1,::1" {
				t.Fatalf("no_proxy 合并值错误: %s", e)
			}
		}
	}
}

func TestExtraEnvOfflineSwitchesPlacement(t *testing.T) {
	// PI_SKIP_VERSION_CHECK 与 PI_TELEMETRY 放在 extraEnv 最前面，可被 opts.Env 或 turn.Env 覆盖
	opts := Options{Env: []string{"PI_TELEMETRY=1"}}
	turn := harness.Turn{}
	extraEnv := []string{"PI_SKIP_VERSION_CHECK=1", "PI_TELEMETRY=0"}
	extraEnv = append(extraEnv, opts.Env...)
	extraEnv = append(extraEnv, turn.Env...)
	env := buildEnv([]string{"PATH=/bin"}, "", extraEnv)
	// 最后一个同名变量生效
	lastTelemetry, lastSkip := "", ""
	for _, e := range env {
		if strings.HasPrefix(e, "PI_TELEMETRY=") {
			lastTelemetry = e
		}
		if strings.HasPrefix(e, "PI_SKIP_VERSION_CHECK=") {
			lastSkip = e
		}
	}
	if lastTelemetry != "PI_TELEMETRY=1" {
		t.Fatalf("opts.Env 未能覆盖 PI_TELEMETRY: %s", lastTelemetry)
	}
	if lastSkip != "PI_SKIP_VERSION_CHECK=1" {
		t.Fatalf("默认应关闭版本检查: %s", lastSkip)
	}
}

func TestMagpieEnvPairsIncludesBridgePath(t *testing.T) {
	// 端点清单与桥路径都配置时，两个环境变量都要进子进程：vision 子进程靠桥路径加载同一个桥。
	pairs := magpieEnvPairs(`[{"name":"m1"}]`, "/tmp/mcode-magpie-bridge.ts")
	got := map[string]string{}
	for _, pair := range pairs {
		name, value, ok := strings.Cut(pair, "=")
		if !ok {
			t.Fatalf("环境变量格式错误: %s", pair)
		}
		got[name] = value
	}
	if got[MagpieEnvVar] != `[{"name":"m1"}]` {
		t.Fatalf("%s 应为端点清单，得到 %q", MagpieEnvVar, got[MagpieEnvVar])
	}
	if got[MagpieBridgeEnvVar] != "/tmp/mcode-magpie-bridge.ts" {
		t.Fatalf("%s 应为桥路径，得到 %q", MagpieBridgeEnvVar, got[MagpieBridgeEnvVar])
	}

	// 没配端点清单时不注入任何 Magpie 变量，行为与启用 Magpie 前一致。
	if pairs = magpieEnvPairs("", "/tmp/mcode-magpie-bridge.ts"); len(pairs) != 0 {
		t.Fatalf("端点清单为空时不应注入 Magpie 环境变量: %v", pairs)
	}
	// 只有端点清单时只注入 MagpieEnvVar。
	pairs = magpieEnvPairs("[]", "")
	if len(pairs) != 1 || pairs[0] != MagpieEnvVar+"=[]" {
		t.Fatalf("没有桥路径时应只注入端点清单: %v", pairs)
	}
}

func TestExitedErrorRedactsSecretsAndHome(t *testing.T) {
	h := New(Options{})
	stderrBuf := procutil.NewTailBuffer(8192)
	home, _ := os.UserHomeDir()
	rawErr := "failed with token sk-abcdef1234567890 at " + home + "/project"
	stderrBuf.Write([]byte(rawErr))
	p := &proc{
		stderr: stderrBuf,
		exited: make(chan struct{}),
	}
	close(p.exited)
	state := newStreamState()
	var emitted harness.Event
	err := h.exitedError(p, state, func(ev harness.Event) {
		emitted = ev
	})
	if err == nil {
		t.Fatal("期望返回错误，实际为 nil")
	}
	if strings.Contains(err.Error(), "sk-abcdef1234567890") {
		t.Fatalf("最终错误文本中不应出现 token 原文: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "sk-***") {
		t.Fatalf("最终错误文本应包含脱敏后的 sk-***: %s", err.Error())
	}
	if home != "" && home != "/" {
		if strings.Contains(err.Error(), home) {
			t.Fatalf("最终错误文本不应包含 HOME 原文路径: %s", err.Error())
		}
		if !strings.Contains(err.Error(), "~/project") {
			t.Fatalf("最终错误文本应包含 ~/project: %s", err.Error())
		}
	}
	if strings.Contains(emitted.Err, "sk-abcdef1234567890") {
		t.Fatalf("事件 Err 中不应出现 token 原文: %s", emitted.Err)
	}
}

// bufferWriteCloser 让测试用 bytes.Buffer 充当可关闭的 stdin 管道。
type bufferWriteCloser struct {
	bytes.Buffer
}

func (b *bufferWriteCloser) Close() error { return nil }

// decodeSingleRequest 解析替身管道上唯一的一条请求。
func decodeSingleRequest(t *testing.T, raw string) map[string]any {
	t.Helper()
	var request map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &request); err != nil {
		t.Fatalf("请求不是合法 JSON: %q", raw)
	}
	return request
}

func TestSteer(t *testing.T) {
	h := New(Options{})
	if err := h.Steer(context.Background(), "sess-1", "换个思路"); err != harness.ErrNotRunning {
		t.Fatalf("无活动进程应返回 ErrNotRunning，得到 %v", err)
	}
	if err := h.Steer(context.Background(), "", "换个思路"); err != harness.ErrNotRunning {
		t.Fatalf("空会话 id 应返回 ErrNotRunning，得到 %v", err)
	}

	sink := &bufferWriteCloser{}
	h.registerRun(&activeRun{turnID: "turn-1", sessionID: "sess-1", writer: procutil.NewLineWriter(sink)})
	if err := h.Steer(context.Background(), "sess-1", "换个思路"); err != nil {
		t.Fatalf("Steer 失败: %v", err)
	}
	request := decodeSingleRequest(t, sink.String())
	if request["type"] != rpcTypeSteer || request["message"] != "换个思路" {
		t.Fatalf("steer 应写入 steer 请求: %+v", request)
	}

	// 强停之后不应再能定位该轮次
	h.ForceStopTurn("turn-1")
	h.unregisterRun(h.runs[0])
	if err := h.Steer(context.Background(), "sess-1", "再来一次"); err != harness.ErrNotRunning {
		t.Fatalf("轮次结束后应返回 ErrNotRunning，得到 %v", err)
	}
}

func TestForceStopTurn(t *testing.T) {
	h := New(Options{})
	// 未命中任何活动轮次时不应 panic
	h.ForceStopTurn("missing")

	sink := &bufferWriteCloser{}
	h.registerRun(&activeRun{turnID: "turn-9", sessionID: "sess-9", writer: procutil.NewLineWriter(sink)})
	h.ForceStopTurn("turn-9")
	if err := h.runs[0].writer.WriteLine([]byte("x")); err == nil {
		t.Fatal("强停应关闭活动轮次的 stdin")
	}
}

// recordingApprover 记录审批请求并返回固定判决。
type recordingApprover struct {
	decision harness.Decision
	requests []harness.ApprovalRequest
}

func (a *recordingApprover) Approve(_ context.Context, req harness.ApprovalRequest) (harness.Decision, error) {
	a.requests = append(a.requests, req)
	return a.decision, nil
}

// recordingInteractor 在 Approver 之上实现 Interactor。
type recordingInteractor struct {
	recordingApprover
	response harness.ApprovalResponse
}

func (a *recordingInteractor) Interact(_ context.Context, req harness.ApprovalRequest) (harness.ApprovalResponse, error) {
	a.requests = append(a.requests, req)
	return a.response, nil
}

func TestAnswerGate(t *testing.T) {
	gateLine := []byte(`{"type":"extension_ui_request","id":"req-1","method":"select","params":{"title":"mcode-gate {\"kind\":\"command\",\"toolName\":\"bash\",\"input\":{\"command\":\"ls -la\"},\"toolCallId\":\"tc1\"}","options":["Allow","Deny"]}}`)
	event, err := parseEvent(gateLine)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("审批方放行", func(t *testing.T) {
		h := New(Options{})
		sink := &bufferWriteCloser{}
		approver := &recordingApprover{decision: harness.DecisionAllow}
		h.handleExtensionUI(context.Background(), harness.Turn{Approver: approver}, procutil.NewLineWriter(sink), event)

		response := decodeSingleRequest(t, sink.String())
		if response["type"] != rpcTypeExtensionUIResp || response["id"] != "req-1" || response["value"] != "Allow" {
			t.Fatalf("门禁应答错误: %+v", response)
		}
		if len(approver.requests) != 1 || approver.requests[0].Kind != harness.RequestKindCommand {
			t.Fatalf("审批请求错误: %+v", approver.requests)
		}
	})

	t.Run("没有审批方时拒绝", func(t *testing.T) {
		h := New(Options{})
		sink := &bufferWriteCloser{}
		h.handleExtensionUI(context.Background(), harness.Turn{}, procutil.NewLineWriter(sink), event)
		response := decodeSingleRequest(t, sink.String())
		if response["value"] != "Deny" {
			t.Fatalf("没有审批方时应拒绝: %+v", response)
		}
	})

	t.Run("Interactor 支持会话级允许", func(t *testing.T) {
		h := New(Options{})
		sink := &bufferWriteCloser{}
		interactor := &recordingInteractor{
			response: harness.ApprovalResponse{
				Decision: harness.DecisionAllow,
				Answers:  map[string][]string{"req-1": {"Allow for session"}},
			},
		}
		h.handleExtensionUI(context.Background(), harness.Turn{Approver: interactor}, procutil.NewLineWriter(sink), event)
		response := decodeSingleRequest(t, sink.String())
		if response["value"] != "Allow for session" {
			t.Fatalf("会话级允许应答错误: %+v", response)
		}
	})
}

func TestAnswerQuestion(t *testing.T) {
	t.Run("确认类提问", func(t *testing.T) {
		confirmLine := []byte(`{"type":"extension_ui_request","id":"q-1","method":"confirm","params":{"title":"是否继续？","message":"会覆盖文件"}}`)
		event, err := parseEvent(confirmLine)
		if err != nil {
			t.Fatal(err)
		}
		h := New(Options{})
		sink := &bufferWriteCloser{}
		interactor := &recordingInteractor{
			response: harness.ApprovalResponse{
				Decision: harness.DecisionAllow,
				Answers:  map[string][]string{"q-1": {"Yes"}},
			},
		}
		h.handleExtensionUI(context.Background(), harness.Turn{Approver: interactor}, procutil.NewLineWriter(sink), event)

		response := decodeSingleRequest(t, sink.String())
		if response["confirmed"] != true {
			t.Fatalf("确认应答错误: %+v", response)
		}
		if gotten := interactor.requests[0].Questions; len(gotten) != 1 || !strings.Contains(gotten[0].Prompt, "会覆盖文件") {
			t.Fatalf("提问内容错误: %+v", gotten)
		}
	})

	t.Run("选择题", func(t *testing.T) {
		selectLine := []byte(`{"type":"extension_ui_request","id":"q-2","method":"select","params":{"title":"选一个","options":["a","b"]}}`)
		event, err := parseEvent(selectLine)
		if err != nil {
			t.Fatal(err)
		}
		h := New(Options{})
		sink := &bufferWriteCloser{}
		interactor := &recordingInteractor{
			response: harness.ApprovalResponse{
				Decision: harness.DecisionAllow,
				Answers:  map[string][]string{"q-2": {"b"}},
			},
		}
		h.handleExtensionUI(context.Background(), harness.Turn{Approver: interactor}, procutil.NewLineWriter(sink), event)

		response := decodeSingleRequest(t, sink.String())
		if response["value"] != "b" {
			t.Fatalf("选择应答错误: %+v", response)
		}
	})

	t.Run("没有答案时取消", func(t *testing.T) {
		selectLine := []byte(`{"type":"extension_ui_request","id":"q-3","method":"input","params":{"title":"补充说明"}}`)
		event, err := parseEvent(selectLine)
		if err != nil {
			t.Fatal(err)
		}
		h := New(Options{})
		sink := &bufferWriteCloser{}
		h.handleExtensionUI(context.Background(), harness.Turn{}, procutil.NewLineWriter(sink), event)
		response := decodeSingleRequest(t, sink.String())
		if response["cancelled"] != true {
			t.Fatalf("无答案时应取消: %+v", response)
		}
	})
}

func TestAnswerSelectRootFields(t *testing.T) {
	t.Run("根对象上的门禁标题", func(t *testing.T) {
		line := []byte(`{"type":"extension_ui_request","id":"root-1","method":"select","title":"mcode-gate {\"kind\":\"command\",\"toolName\":\"bash\",\"input\":{\"command\":\"ls -la\"},\"toolCallId\":\"tc1\"}","options":["Allow","Deny"]}`)
		event, err := parseEvent(line)
		if err != nil {
			t.Fatal(err)
		}
		approver := &recordingApprover{decision: harness.DecisionAllow}
		sink := &bufferWriteCloser{}
		New(Options{}).handleExtensionUI(context.Background(), harness.Turn{Approver: approver}, procutil.NewLineWriter(sink), event)

		response := decodeSingleRequest(t, sink.String())
		if response["value"] != "Allow" {
			t.Fatalf("根对象上的门禁标题应被识别: %+v", response)
		}
		if len(approver.requests) != 1 || approver.requests[0].ToolName != "bash" {
			t.Fatalf("门禁应交给审批方: %+v", approver.requests)
		}
	})

	t.Run("根对象上的选择题", func(t *testing.T) {
		line := []byte(`{"type":"extension_ui_request","id":"root-2","method":"select","title":"选一个","options":["a","b"]}`)
		event, err := parseEvent(line)
		if err != nil {
			t.Fatal(err)
		}
		interactor := &recordingInteractor{
			response: harness.ApprovalResponse{
				Decision: harness.DecisionAllow,
				Answers:  map[string][]string{"root-2": {"b"}},
			},
		}
		sink := &bufferWriteCloser{}
		New(Options{}).handleExtensionUI(context.Background(), harness.Turn{Approver: interactor}, procutil.NewLineWriter(sink), event)

		response := decodeSingleRequest(t, sink.String())
		if response["value"] != "b" {
			t.Fatalf("根对象上的选项应答错误: %+v", response)
		}
		questions := interactor.requests[0].Questions
		if len(questions) != 1 || questions[0].Prompt != "选一个" || len(questions[0].Choices) != 2 {
			t.Fatalf("提问内容错误: %+v", questions)
		}
	})

	t.Run("根标题加字符串 message 的确认", func(t *testing.T) {
		line := []byte(`{"type":"extension_ui_request","id":"root-3","method":"confirm","title":"是否继续？","message":"会覆盖文件"}`)
		event, err := parseEvent(line)
		if err != nil {
			t.Fatal(err)
		}
		interactor := &recordingInteractor{
			response: harness.ApprovalResponse{
				Decision: harness.DecisionAllow,
				Answers:  map[string][]string{"root-3": {"Yes"}},
			},
		}
		sink := &bufferWriteCloser{}
		New(Options{}).handleExtensionUI(context.Background(), harness.Turn{Approver: interactor}, procutil.NewLineWriter(sink), event)

		response := decodeSingleRequest(t, sink.String())
		if response["confirmed"] != true {
			t.Fatalf("确认应答错误: %+v", response)
		}
		questions := interactor.requests[0].Questions
		if len(questions) != 1 || !strings.Contains(questions[0].Prompt, "会覆盖文件") {
			t.Fatalf("message 应拼进提示: %+v", questions)
		}
	})
}

func TestAnswerSelectRejectsEmptyRequest(t *testing.T) {
	line := []byte(`{"type":"extension_ui_request","id":"empty-1","method":"select","params":{}}`)
	event, err := parseEvent(line)
	if err != nil {
		t.Fatal(err)
	}
	interactor := &recordingInteractor{
		response: harness.ApprovalResponse{
			Decision: harness.DecisionAllow,
			Answers:  map[string][]string{"empty-1": {"a"}},
		},
	}
	sink := &bufferWriteCloser{}
	New(Options{}).handleExtensionUI(context.Background(), harness.Turn{Approver: interactor}, procutil.NewLineWriter(sink), event)

	response := decodeSingleRequest(t, sink.String())
	if response["cancelled"] != true {
		t.Fatalf("没有标题也没有选项时应直接取消: %+v", response)
	}
	if len(interactor.requests) != 0 {
		t.Fatalf("畸形请求不应弹给交互方: %+v", interactor.requests)
	}
}

func TestImagePayloadsFailsOnMissingImage(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(imagePath, []byte("png-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	missingPath := filepath.Join(dir, "missing.png")
	_, err := imagePayloads([]string{imagePath, missingPath})
	if err == nil {
		t.Fatal("期望读取不存在的图片时返回错误，实际为 nil")
	}
	if !strings.Contains(err.Error(), "pi: read image") || !strings.Contains(err.Error(), missingPath) {
		t.Fatalf("错误信息未包含路径或前缀: %v", err)
	}
}

func TestImagePayloadsSkipsNonImage(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "a.png")
	if err := os.WriteFile(imagePath, []byte("png-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	pdfPath := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(pdfPath, []byte("%PDF-1.7"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 非图片附件不当图片下发：它的路径已由 prompt 文本给出。
	images, err := imagePayloads([]string{imagePath, pdfPath})
	if err != nil {
		t.Fatalf("非图片附件不应报错: %v", err)
	}
	if len(images) != 1 {
		t.Fatalf("非图片附件应被跳过，得到 %+v", images)
	}
	if images[0].MimeType != "image/png" {
		t.Fatalf("只应下发 a.png，得到 %+v", images[0])
	}
}

func TestRunFailsEarlyOnUnreadableImage(t *testing.T) {
	h := New(Options{})
	var emitted []harness.Event
	err := h.Run(context.Background(), harness.Turn{
		Prompt: "hello",
		Images: []string{"/nonexistent/image.png"},
	}, func(ev harness.Event) {
		emitted = append(emitted, ev)
	})
	if err == nil {
		t.Fatal("期望返回错误，实际为 nil")
	}
	if len(emitted) != 1 || emitted[0].Type != harness.EventError {
		t.Fatalf("期望 emit EventError，实际得到: %+v", emitted)
	}
}

func TestInstallGate(t *testing.T) {
	// 目录不存在时应一并建出来（app 启动时给的是临时目录下的子目录）。
	dir := filepath.Join(t.TempDir(), "pi")
	path, err := InstallGate(dir)
	if err != nil {
		t.Fatalf("InstallGate 失败: %v", err)
	}
	if filepath.Base(path) != "mcode-gate.ts" {
		t.Fatalf("门禁扩展文件名应为 mcode-gate.ts，得到 %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取门禁扩展失败: %v", err)
	}
	source := string(data)
	for _, want := range []string{
		`pi.on("tool_call"`,
		"process.env.MCODE_PI_GATE",
		`ctx.ui.select("mcode-gate " + JSON.stringify(payload)`,
		`["Allow", "Allow for session", "Deny"]`,
	} {
		if !strings.Contains(source, want) {
			t.Errorf("门禁扩展应包含 %q", want)
		}
	}

	// 重复安装按覆盖处理，路径不变。
	again, err := InstallGate(dir)
	if err != nil || again != path {
		t.Fatalf("重复 InstallGate 应返回同一路径: %q %v", again, err)
	}
}

func TestGateContent(t *testing.T) {
	data, err := gateContent(harness.Turn{Mode: harness.ModeFullAccess, InteractionMode: "plan"})
	if err != nil {
		t.Fatalf("gateContent 失败: %v", err)
	}
	var state gateState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("门禁内容不是合法 JSON: %v", err)
	}
	if state.RuntimeMode != "fullAccess" || state.InteractionMode != "plan" || state.FinalReport {
		t.Fatalf("运行模式注入内容错误: %+v", state)
	}

	// 空交互模式按 build 写入。
	data, err = gateContent(harness.Turn{Mode: harness.ModeSupervised})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if state.RuntimeMode != "supervised" || state.InteractionMode != "build" {
		t.Fatalf("空交互模式应按 build 写入: %+v", state)
	}

	tests := []struct {
		mode harness.PermissionMode
		want string
	}{
		{harness.ModeReadOnly, "readonly"},
		{harness.ModeSupervised, "supervised"},
		{harness.ModeAutoAcceptEdits, "auto"},
		{harness.ModeAuto, "auto"},
		{harness.ModeFullAccess, "fullAccess"},
		{"", "supervised"},
	}
	for _, test := range tests {
		if got := runtimeModeName(test.mode); got != test.want {
			t.Errorf("runtimeModeName(%q) = %q，期望 %q", test.mode, got, test.want)
		}
	}
}

func TestPrepareGateFile(t *testing.T) {
	opts, cleanup, err := New(Options{}).prepareGateFile(harness.Turn{Mode: harness.ModeAuto, InteractionMode: "plan"})
	if err != nil {
		t.Fatalf("prepareGateFile 失败: %v", err)
	}
	if opts.GatePath == "" {
		t.Fatal("应写出门禁文件")
	}
	data, err := os.ReadFile(opts.GatePath)
	if err != nil {
		t.Fatalf("读取门禁文件失败: %v", err)
	}
	var state gateState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("门禁文件不是合法 JSON: %v", err)
	}
	if state.RuntimeMode != "auto" || state.InteractionMode != "plan" {
		t.Errorf("门禁文件内容错误: %+v", state)
	}

	cleanup()
	if _, err := os.Stat(opts.GatePath); !os.IsNotExist(err) {
		t.Errorf("清理应删除临时门禁文件: %v", err)
	}

	// 调用方给了路径时只覆写内容，不删文件。
	path := filepath.Join(t.TempDir(), "gate.json")
	provided, cleanupProvided, err := New(Options{GatePath: path}).prepareGateFile(harness.Turn{Mode: harness.ModeFullAccess})
	if err != nil {
		t.Fatalf("prepareGateFile 失败: %v", err)
	}
	if provided.GatePath != path {
		t.Fatalf("应沿用调用方给的路径: %+v", provided)
	}
	cleanupProvided()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("调用方给的路径不应被清理: %v", err)
	}
}

func TestRunInjectsGateFile(t *testing.T) {
	fake := newFakePi(t, "rpc_session.jsonl")
	h := New(Options{Binary: fake.binary, Env: fake.env})

	var events []harness.Event
	turn := harness.Turn{WorkDir: t.TempDir(), Prompt: "继续", Mode: harness.ModeFullAccess, InteractionMode: "plan"}
	if err := h.Run(context.Background(), turn, collectEvents(&events)); err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}

	gatePath, content := fake.readGateEnv(t)
	if !strings.HasSuffix(gatePath, ".json") || gatePath == "" {
		t.Fatalf("门禁文件路径未注入: %q", gatePath)
	}
	var state gateState
	if err := json.Unmarshal([]byte(content), &state); err != nil {
		t.Fatalf("启动时的门禁文件不是合法 JSON: %q", content)
	}
	if state.RuntimeMode != "fullAccess" || state.InteractionMode != "plan" {
		t.Fatalf("启动时的门禁内容错误: %+v", state)
	}
}

func TestAnswerGateInPlanMode(t *testing.T) {
	gateEvent := func(t *testing.T, toolName string) *rpcEvent {
		t.Helper()
		title := `mcode-gate {"kind":"command","toolName":"` + toolName + `","input":{"command":"ls -la"},"toolCallId":"tc1"}`
		params, err := json.Marshal(map[string]any{"title": title, "options": []string{"Allow", "Deny"}})
		if err != nil {
			t.Fatal(err)
		}
		line, err := json.Marshal(map[string]any{
			"type":   "extension_ui_request",
			"id":     "req-1",
			"method": "select",
			"params": json.RawMessage(params),
		})
		if err != nil {
			t.Fatal(err)
		}
		event, err := parseEvent(line)
		if err != nil {
			t.Fatalf("解析门禁帧失败: %v", err)
		}
		return event
	}

	t.Run("计划模式直接拒绝且不问审批方", func(t *testing.T) {
		approver := &recordingApprover{decision: harness.DecisionAllow}
		sink := &bufferWriteCloser{}
		New(Options{}).handleExtensionUI(context.Background(),
			harness.Turn{Approver: approver, InteractionMode: "plan"}, procutil.NewLineWriter(sink), gateEvent(t, "bash"))

		response := decodeSingleRequest(t, sink.String())
		if response["value"] != "Deny" {
			t.Fatalf("计划模式下的命令应直接拒绝: %+v", response)
		}
		if len(approver.requests) != 0 {
			t.Fatalf("计划模式不应把请求抛给审批方: %+v", approver.requests)
		}
	})

	t.Run("计划模式放行只读工具", func(t *testing.T) {
		approver := &recordingApprover{decision: harness.DecisionAllow}
		sink := &bufferWriteCloser{}
		New(Options{}).handleExtensionUI(context.Background(),
			harness.Turn{Approver: approver, InteractionMode: "plan"}, procutil.NewLineWriter(sink), gateEvent(t, "read"))

		response := decodeSingleRequest(t, sink.String())
		if response["value"] != "Allow" {
			t.Fatalf("计划模式下的只读工具应放行: %+v", response)
		}
		if len(approver.requests) != 1 || approver.requests[0].ToolName != "read" {
			t.Fatalf("只读工具应照常走审批: %+v", approver.requests)
		}
	})

	t.Run("supervised 向上抛出审批", func(t *testing.T) {
		approver := &recordingApprover{decision: harness.DecisionAllow}
		sink := &bufferWriteCloser{}
		New(Options{}).handleExtensionUI(context.Background(),
			harness.Turn{Approver: approver, Mode: harness.ModeSupervised}, procutil.NewLineWriter(sink), gateEvent(t, "bash"))

		response := decodeSingleRequest(t, sink.String())
		if response["value"] != "Allow" {
			t.Fatalf("审批方放行时应回写 Allow: %+v", response)
		}
		if len(approver.requests) != 1 || approver.requests[0].Kind != harness.RequestKindCommand {
			t.Fatalf("supervised 应把命令抛给审批方: %+v", approver.requests)
		}
	})
}

// 直连（本轮没有 Magpie 注入）时，线程里残留的注入模型改用 pi 默认模型，
// 避免 pi 报 Unknown provider。
func TestRunDropsInjectedModelWhenNotInjected(t *testing.T) {
	fake := newFakePi(t, "rpc_session.jsonl")
	h := New(Options{
		Binary: fake.binary, Env: fake.env, Extension: "/tmp/ext/pi.ts",
		InjectedModel: func(model string) bool { return strings.HasPrefix(model, "magpie-jp/") },
	})

	var events []harness.Event
	turn := harness.Turn{
		WorkDir: t.TempDir(),
		Prompt:  "继续",
		Model:   "magpie-jp/auto/deepseek-v4.1-flash",
		Mode:    harness.ModeSupervised,
	}
	if err := h.Run(context.Background(), turn, collectEvents(&events)); err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}
	for _, arg := range fake.readArgs(t) {
		if arg == "--provider" || arg == "--model" {
			t.Fatalf("直连时不应传 %s: %v", arg, fake.readArgs(t))
		}
	}
}
