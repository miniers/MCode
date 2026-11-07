package omp

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/harness"
)

// TestOmpRunHelperProcess 是 Run 测试里替换 omp 的假进程（GO_WANT_OMP_RUN_HELPER=1 时生效，
// 否则正常测试运行时直接返回）：记录命令行参数与门禁环境变量，并按请求类型逐行回放 fixture。
//
// 关键在时间顺序：get_state 的应答（以及配置了扩展时的命令表应答）要等 Harness 把对应请求
// 写进 stdin 之后才写回；prompt 请求到达后再写 fixture 里剩下的事件行。否则 Harness 一看到
// 结束事件就杀掉进程，prompt 可能还没写进日志。
func TestOmpRunHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_OMP_RUN_HELPER") != "1" {
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
	if path := os.Getenv("FAKE_OMP_ARGS_FILE"); path != "" {
		if err := os.WriteFile(path, []byte(strings.Join(args, "\n")), 0o644); err != nil {
			os.Exit(2)
		}
	}

	// (b) 记录 Harness 注入的门禁文件路径与工作目录。
	if path := os.Getenv("FAKE_OMP_ENV_FILE"); path != "" {
		content := os.Getenv(GateEnvVar) + "\n" + os.Getenv("PWD") + "\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			os.Exit(2)
		}
	}

	// (c) 读 fixture 并按行拆分（去掉结尾换行，避免多出空行）。
	data, err := os.ReadFile(os.Getenv("FAKE_OMP_FIXTURE"))
	if err != nil {
		os.Exit(2)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")

	reader := bufio.NewReader(os.Stdin)
	logPath := os.Getenv("FAKE_OMP_LOG_FILE")
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

	next := 0
	writeLine := func(line string, replacements map[string]string) {
		for from, to := range replacements {
			line = strings.ReplaceAll(line, from, to)
		}
		_, _ = os.Stdout.WriteString(line + "\n")
	}
	// 每次写完立即落地，保证 Harness 杀进程时日志已落盘。
	for {
		raw, readErr := reader.ReadString('\n')
		line := strings.TrimRight(raw, "\n")
		if line != "" {
			logRequest(line)
			var request map[string]any
			if json.Unmarshal([]byte(line), &request) == nil {
				id, _ := request["id"].(string)
				switch request["type"] {
				case "get_available_commands":
					if next < len(lines) {
						writeLine(lines[next], map[string]string{"__COMMANDS_ID__": id})
						next++
					}
				case "get_state":
					if next < len(lines) {
						writeLine(lines[next], map[string]string{"__STATE_ID__": id})
						next++
					}
				case "prompt":
					for ; next < len(lines); next++ {
						writeLine(lines[next], map[string]string{"__PROMPT_ID__": id})
					}
				}
			}
		}
		if readErr != nil {
			break
		}
	}
	os.Exit(0)
}

// fakeOmp 是一个不联网、不依赖真实 omp 的替身：shell 包装脚本把参数转发给 Go helper 进程。
type fakeOmp struct {
	binary   string
	argsFile string
	logFile  string
	envFile  string
	env      []string
}

func newFakeOmp(t *testing.T, fixture string) *fakeOmp {
	t.Helper()
	dir := t.TempDir()

	// 用测试二进制自身作为替身进程，靠 GO_WANT_OMP_RUN_HELPER 重入到 TestOmpRunHelperProcess。
	// os.Args[0] 可能是相对路径，而子进程的 cmd.Dir 是 turn.WorkDir，所以先转成绝对路径。
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("解析测试二进制路径失败: %v", err)
	}

	binary := filepath.Join(dir, "omp")
	wrapper := "#!/bin/sh\nexec '" + self + "' -test.run='^TestOmpRunHelperProcess$' -- \"$@\"\n"
	if err := os.WriteFile(binary, []byte(wrapper), 0o755); err != nil {
		t.Fatalf("写入替身启动脚本失败: %v", err)
	}

	absoluteFixture, err := filepath.Abs(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("解析 fixture 路径失败: %v", err)
	}

	fake := &fakeOmp{
		binary:   binary,
		argsFile: filepath.Join(dir, "args.txt"),
		logFile:  filepath.Join(dir, "requests.jsonl"),
		envFile:  filepath.Join(dir, "env.txt"),
	}
	fake.env = []string{
		"GO_WANT_OMP_RUN_HELPER=1",
		"FAKE_OMP_FIXTURE=" + absoluteFixture,
		"FAKE_OMP_ARGS_FILE=" + fake.argsFile,
		"FAKE_OMP_LOG_FILE=" + fake.logFile,
		"FAKE_OMP_ENV_FILE=" + fake.envFile,
	}
	return fake
}

// readArgs 返回替身进程收到的命令行参数。
func (f *fakeOmp) readArgs(t *testing.T) []string {
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
func (f *fakeOmp) readRequests(t *testing.T) []map[string]any {
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

// readEnv 返回替身进程看到的门禁文件路径与工作目录。
func (f *fakeOmp) readEnv(t *testing.T) (gatePath, workDir string) {
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
		workDir = lines[1]
	}
	return gatePath, workDir
}

// collectEvents 返回一个把事件追加进切片的 emit 函数。
func collectEvents(events *[]harness.Event) func(harness.Event) {
	return func(event harness.Event) { *events = append(*events, event) }
}

// assertArgs 比较替身进程收到的参数与期望。
func assertArgs(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("命令行参数错误:\n得到 %v\n期望 %v", got, want)
	}
}

func TestRunReplaysTurnEvents(t *testing.T) {
	fake := newFakeOmp(t, "rpc_text_tool_usage.jsonl")
	configPath := filepath.Join(t.TempDir(), "launch.yml")
	h := New(Options{Binary: fake.binary, Env: fake.env, ConfigPath: configPath})

	var events []harness.Event
	turn := harness.Turn{
		ID:      "turn-1",
		WorkDir: t.TempDir(),
		Prompt:  "看一下仓库",
		// 没有门禁扩展时只有完全访问模式允许启动（见 TestRunRefusesWithoutGate），
		// 本测试只验证事件翻译，用完全访问模式绕开门禁前置校验。
		Mode: harness.ModeFullAccess,
	}
	if err := h.Run(context.Background(), turn, collectEvents(&events)); err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}

	wantTypes := []harness.EventType{
		harness.EventSession,
		harness.EventText,
		harness.EventUsage,
		harness.EventToolStart,
		harness.EventToolEnd,
		harness.EventToolStart,
		harness.EventFileChange,
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
	if events[0].SessionID != "/tmp/omp-agent/sessions/session-1.json" {
		t.Errorf("会话事件应带 omp 回报的 sessionFile，得到 %q", events[0].SessionID)
	}
	if events[1].Text != "先看下仓库。" {
		t.Errorf("首个文本增量错误: %q", events[1].Text)
	}
	if usage := events[2].Usage; usage == nil || usage.InputTokens != 160 || usage.OutputTokens != 8 || usage.CachedTokens != 40 {
		t.Errorf("message_update 用量错误: %+v", events[2].Usage)
	}
	if start := events[3]; start.ToolID != "tc1" || start.ToolName != "bash" || inputString(start.Input, "command") != "ls -la" {
		t.Errorf("工具开始事件错误: %+v", start)
	}
	if end := events[4]; end.ToolID != "tc1" || end.Output != "main.go\n" || end.IsError {
		t.Errorf("工具结束事件错误: %+v", end)
	}
	if events[6].Path != "internal/app/app.go" {
		t.Errorf("文件改动事件错误: %+v", events[6])
	}
	if usage := events[10].Usage; usage == nil || usage.InputTokens != 200 || usage.OutputTokens != 20 || usage.CachedTokens != 50 {
		t.Errorf("message_end 用量（含缓存读写合并）错误: %+v", events[10].Usage)
	}
	if events[11].StopReason != "completed" {
		t.Errorf("结束原因应为 completed，得到 %q", events[11].StopReason)
	}

	assertArgs(t, fake.readArgs(t), []string{
		"--mode", "rpc-ui", "--approval-mode", "yolo",
		"--config", configPath,
	})

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
	if prompt["message"] != "看一下仓库" {
		t.Errorf("prompt 内容错误: %v", prompt["message"])
	}

	// 门禁文件路径与 PWD 应经环境变量传给子进程
	gatePath, workDir := fake.readEnv(t)
	if !strings.HasSuffix(gatePath, ".json") || gatePath == "" {
		t.Errorf("门禁文件路径未注入: %q", gatePath)
	}
	if workDir != turn.WorkDir {
		t.Errorf("子进程 PWD 应为 %q，得到 %q", turn.WorkDir, workDir)
	}

	// 当轮结束后进程应被回收，活动轮次清空
	if run := h.findRun(func(*activeRun) bool { return true }); run != nil {
		t.Errorf("Run 结束后不应残留活动轮次: %+v", run)
	}
}

func TestRunResumesSessionAndBuildsArgs(t *testing.T) {
	fake := newFakeOmp(t, "rpc_session.jsonl")
	configPath := filepath.Join(t.TempDir(), "launch.yml")
	h := New(Options{Binary: fake.binary, Env: fake.env, ConfigPath: configPath})

	var events []harness.Event
	turn := harness.Turn{
		WorkDir:   t.TempDir(),
		Prompt:    "继续",
		Model:     "anthropic/claude-opus-4-6",
		Effort:    "high",
		Mode:      harness.ModeFullAccess,
		SessionID: "/tmp/omp-agent/sessions/session-resumed.json",
	}
	if err := h.Run(context.Background(), turn, collectEvents(&events)); err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}

	assertArgs(t, fake.readArgs(t), []string{
		"--mode", "rpc-ui", "--approval-mode", "yolo",
		"--config", configPath,
		"--model", "anthropic/claude-opus-4-6",
		"--thinking", "high",
		"--resume", "/tmp/omp-agent/sessions/session-resumed.json",
	})

	wantTypes := []harness.EventType{harness.EventSession, harness.EventText, harness.EventUsage, harness.EventDone}
	if len(events) != len(wantTypes) {
		t.Fatalf("事件数量应为 %d，得到 %d: %+v", len(wantTypes), len(events), events)
	}
	for i, want := range wantTypes {
		if events[i].Type != want {
			t.Errorf("第 %d 个事件应为 %v，得到 %v", i, want, events[i].Type)
		}
	}
	if events[0].SessionID != "/tmp/omp-agent/sessions/session-resumed.json" {
		t.Errorf("会话事件应带 omp 回报的 sessionFile，得到 %q", events[0].SessionID)
	}
}

func TestRunLoadsGateAndStarts(t *testing.T) {
	fake := newFakeOmp(t, "rpc_gate_loaded.jsonl")
	configPath := filepath.Join(t.TempDir(), "launch.yml")
	extensionPath := filepath.Join(t.TempDir(), "mcode-gate.ts")
	h := New(Options{Binary: fake.binary, Env: fake.env, ConfigPath: configPath, Extension: extensionPath})

	var events []harness.Event
	turn := harness.Turn{WorkDir: t.TempDir(), Prompt: "继续", Mode: harness.ModeSupervised}
	if err := h.Run(context.Background(), turn, collectEvents(&events)); err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}

	assertArgs(t, fake.readArgs(t), []string{
		"--mode", "rpc-ui", "--approval-mode", "yolo",
		"--config", configPath,
		"-e", extensionPath,
	})

	wantTypes := []harness.EventType{harness.EventSession, harness.EventText, harness.EventDone}
	if len(events) != len(wantTypes) {
		t.Fatalf("事件数量应为 %d，得到 %d: %+v", len(wantTypes), len(events), events)
	}
	if events[0].SessionID != "/tmp/omp-agent/sessions/gated.json" {
		t.Errorf("会话事件应带 sessionFile，得到 %q", events[0].SessionID)
	}

	// 请求顺序：先确认门禁加载，再取状态，最后才写 prompt
	var types []string
	for _, request := range fake.readRequests(t) {
		kind, _ := request["type"].(string)
		types = append(types, kind)
	}
	wantOrder := []string{"get_available_commands", "get_state", "prompt"}
	if strings.Join(types, ",") != strings.Join(wantOrder, ",") {
		t.Errorf("请求顺序应为 %v，得到 %v", wantOrder, types)
	}
}

func TestRunFailsWhenGateMissing(t *testing.T) {
	fake := newFakeOmp(t, "rpc_gate_missing.jsonl")
	extensionPath := filepath.Join(t.TempDir(), "mcode-gate.ts")
	h := New(Options{Binary: fake.binary, Env: fake.env, Extension: extensionPath})

	var events []harness.Event
	turn := harness.Turn{WorkDir: t.TempDir(), Prompt: "继续", Mode: harness.ModeSupervised}
	err := h.Run(context.Background(), turn, collectEvents(&events))
	if err == nil || !strings.Contains(err.Error(), "tool gate") {
		t.Fatalf("门禁未加载时应报错，得到 %v", err)
	}
	sawError := false
	for _, event := range events {
		if event.Type == harness.EventError && strings.Contains(event.Err, "tool gate") {
			sawError = true
		}
	}
	if !sawError {
		t.Fatalf("应发出门禁未加载的错误事件: %+v", events)
	}

	// 门禁没加载就不能把 prompt 发出去
	for _, request := range fake.readRequests(t) {
		if request["type"] == "prompt" {
			t.Fatalf("门禁未加载时不应写入 prompt: %+v", request)
		}
	}
}

func TestRunRefusesWithoutGate(t *testing.T) {
	// omp 始终以 --approval-mode yolo 启动，没有门禁扩展时工具会被全部放行：
	// 除完全访问以外任何权限模式都必须拒绝启动，而不是静默降级。
	for _, mode := range []harness.PermissionMode{
		harness.ModeReadOnly,
		harness.ModeSupervised,
		harness.ModeAutoAcceptEdits,
		harness.ModeAuto,
	} {
		t.Run(string(mode), func(t *testing.T) {
			h := New(Options{Binary: "omp"})
			turn := harness.Turn{WorkDir: t.TempDir(), Prompt: "看一下仓库", Mode: mode}
			err := h.Run(context.Background(), turn, nil)
			if err == nil || !strings.Contains(err.Error(), "门禁扩展") {
				t.Fatalf("没有门禁扩展时 %s 模式应拒绝启动，得到 %v", mode, err)
			}
		})
	}
}

func TestRunRequiresPrompt(t *testing.T) {
	h := New(Options{Binary: "omp"})
	if err := h.Run(context.Background(), harness.Turn{WorkDir: t.TempDir()}, nil); err == nil {
		t.Fatal("没有 prompt 与图片时应直接报错，而不是启动进程等待")
	}
}

func TestBuildArgs(t *testing.T) {
	planPrompt := ompPlanPrompt
	tests := []struct {
		name string
		opts Options
		turn harness.Turn
		want []string
	}{
		{
			name: "缺省：任何权限模式都以 yolo 启动",
			turn: harness.Turn{},
			want: []string{"--mode", "rpc-ui", "--approval-mode", "yolo"},
		},
		{
			name: "只读模式同样以 yolo 启动",
			turn: harness.Turn{Mode: harness.ModeReadOnly},
			want: []string{"--mode", "rpc-ui", "--approval-mode", "yolo"},
		},
		{
			name: "supervised 同样以 yolo 启动",
			turn: harness.Turn{Mode: harness.ModeSupervised},
			want: []string{"--mode", "rpc-ui", "--approval-mode", "yolo"},
		},
		{
			name: "autoAcceptEdits 同样以 yolo 启动",
			turn: harness.Turn{Mode: harness.ModeAutoAcceptEdits},
			want: []string{"--mode", "rpc-ui", "--approval-mode", "yolo"},
		},
		{
			name: "auto 同样以 yolo 启动",
			turn: harness.Turn{Mode: harness.ModeAuto},
			want: []string{"--mode", "rpc-ui", "--approval-mode", "yolo"},
		},
		{
			name: "fullAccess 同样以 yolo 启动",
			turn: harness.Turn{Mode: harness.ModeFullAccess},
			want: []string{"--mode", "rpc-ui", "--approval-mode", "yolo"},
		},
		{
			name: "启动配置与门禁扩展",
			opts: Options{ConfigPath: "/tmp/omp-launch.yml", Extension: "/tmp/mcode-gate.ts"},
			turn: harness.Turn{Mode: harness.ModeSupervised},
			want: []string{"--mode", "rpc-ui", "--approval-mode", "yolo", "--config", "/tmp/omp-launch.yml", "-e", "/tmp/mcode-gate.ts"},
		},
		{
			name: "带 provider 前缀的模型",
			turn: harness.Turn{Model: "anthropic/claude-opus-4-6"},
			want: []string{"--mode", "rpc-ui", "--approval-mode", "yolo", "--model", "anthropic/claude-opus-4-6"},
		},
		{
			name: "default 交给 omp 自己的默认模型",
			turn: harness.Turn{Model: "default"},
			want: []string{"--mode", "rpc-ui", "--approval-mode", "yolo"},
		},
		{
			name: "裸模型名不透传",
			turn: harness.Turn{Model: "claude-opus-4-6"},
			want: []string{"--mode", "rpc-ui", "--approval-mode", "yolo"},
		},
		{
			name: "合法思考档位",
			turn: harness.Turn{Effort: "xhigh"},
			want: []string{"--mode", "rpc-ui", "--approval-mode", "yolo", "--thinking", "xhigh"},
		},
		{
			name: "未知思考档位被丢弃",
			turn: harness.Turn{Effort: "ultra"},
			want: []string{"--mode", "rpc-ui", "--approval-mode", "yolo"},
		},
		{
			name: "计划模式追加只读规则",
			turn: harness.Turn{InteractionMode: "plan"},
			want: []string{"--mode", "rpc-ui", "--approval-mode", "yolo", "--append-system-prompt", planPrompt},
		},
		{
			name: "上层策略文本与计划规则拼接",
			opts: Options{SystemPrompt: "Swarm 调度策略"},
			turn: harness.Turn{InteractionMode: "plan"},
			want: []string{
				"--mode", "rpc-ui", "--approval-mode", "yolo",
				"--append-system-prompt", "Swarm 调度策略\n\n" + planPrompt,
			},
		},
		{
			name: "恢复会话",
			turn: harness.Turn{SessionID: "/tmp/omp-agent/sessions/session.json"},
			want: []string{"--mode", "rpc-ui", "--approval-mode", "yolo", "--resume", "/tmp/omp-agent/sessions/session.json"},
		},
		{
			name: "参数顺序固定，额外参数排在最后",
			opts: Options{ConfigPath: "/tmp/omp-launch.yml", Extension: "/tmp/mcode-gate.ts", ExtraArgs: []string{"--no-skills"}},
			turn: harness.Turn{
				Model:           "anthropic/claude-opus-4-6",
				Effort:          "medium",
				InteractionMode: "plan",
				SessionID:       "/tmp/omp-agent/sessions/session.json",
			},
			want: []string{
				"--mode", "rpc-ui", "--approval-mode", "yolo",
				"--config", "/tmp/omp-launch.yml",
				"-e", "/tmp/mcode-gate.ts",
				"--model", "anthropic/claude-opus-4-6",
				"--thinking", "medium",
				"--append-system-prompt", planPrompt,
				"--resume", "/tmp/omp-agent/sessions/session.json",
				"--no-skills",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := buildArgs(test.opts, test.turn)
			if strings.Join(got, " ") != strings.Join(test.want, " ") {
				t.Fatalf("参数错误:\n得到 %v\n期望 %v", got, test.want)
			}
		})
	}
}

func TestBuildEnv(t *testing.T) {
	env := buildEnv([]string{"PATH=/bin", "PWD=/old"}, "/work", []string{"A=1"})
	if strings.Join(env, " ") != "PATH=/bin PWD=/work A=1" {
		t.Fatalf("应剔除继承的 PWD 并补工作目录: %v", env)
	}

	env = buildEnv([]string{"PWD=/old"}, "", []string{"PWD=/custom"})
	if strings.Join(env, " ") != "PWD=/custom" {
		t.Fatalf("extra 里的 PWD 应排在末尾优先: %v", env)
	}

	env = buildEnv([]string{"PWD=/old"}, "/work", []string{"PWD=/custom"})
	if strings.Join(env, " ") != "PWD=/work PWD=/custom" {
		t.Fatalf("extra 里的 PWD 应覆盖工作目录默认值: %v", env)
	}
}

func TestPrepareLaunchFiles(t *testing.T) {
	h := New(Options{})
	opts, cleanup, err := h.prepareLaunchFiles(harness.Turn{Mode: harness.ModeAutoAcceptEdits, InteractionMode: "plan"})
	if err != nil {
		t.Fatalf("prepareLaunchFiles 失败: %v", err)
	}
	if opts.ConfigPath == "" || opts.GatePath == "" {
		t.Fatalf("应写出启动配置与门禁文件: %+v", opts)
	}

	gateData, err := os.ReadFile(opts.GatePath)
	if err != nil {
		t.Fatalf("读取门禁文件失败: %v", err)
	}
	var state gateState
	if err := json.Unmarshal(gateData, &state); err != nil {
		t.Fatalf("门禁文件不是合法 JSON: %v", err)
	}
	if state.RuntimeMode != "auto" || state.InteractionMode != "plan" || state.FinalReport || state.AllowTask {
		t.Errorf("运行模式注入内容错误: %+v", state)
	}

	settings, err := os.ReadFile(opts.ConfigPath)
	if err != nil {
		t.Fatalf("读取启动配置失败: %v", err)
	}
	if !strings.Contains(string(settings), "autoResume: false") {
		t.Errorf("启动配置应关掉自动恢复: %q", string(settings))
	}

	cleanup()
	if _, err := os.Stat(opts.GatePath); !os.IsNotExist(err) {
		t.Errorf("清理应删除临时门禁文件: %v", err)
	}
	if _, err := os.Stat(opts.ConfigPath); !os.IsNotExist(err) {
		t.Errorf("清理应删除临时启动配置: %v", err)
	}

	// 调用方给了路径时只覆写内容，不删文件
	dir := t.TempDir()
	configPath := filepath.Join(dir, "launch.yml")
	gatePath := filepath.Join(dir, "gate.json")
	// 预建调用方自己的配置文件，用于验证它不会被覆写
	if err := os.WriteFile(configPath, []byte("caller: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	provided, cleanupProvided, err := New(Options{ConfigPath: configPath, GatePath: gatePath}).
		prepareLaunchFiles(harness.Turn{Mode: harness.ModeFullAccess})
	if err != nil {
		t.Fatalf("prepareLaunchFiles 失败: %v", err)
	}
	if provided.ConfigPath != configPath || provided.GatePath != gatePath {
		t.Fatalf("应沿用调用方给的路径: %+v", provided)
	}
	cleanupProvided()
	for _, path := range []string{configPath, gatePath} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("调用方给的路径不应被清理: %v", err)
		}
	}
	// 调用方传入的配置归调用方所有，内容不应被覆写
	kept, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("读取调用方配置失败: %v", err)
	}
	if string(kept) != "caller: true\n" {
		t.Errorf("调用方配置不应被覆写: %q", string(kept))
	}
}

func TestRuntimeModeName(t *testing.T) {
	tests := []struct {
		mode harness.PermissionMode
		want string
	}{
		{harness.ModeReadOnly, "supervised"},
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

func TestSteer(t *testing.T) {
	h := New(Options{})
	if err := h.Steer(context.Background(), "sess-1", "换个思路"); err != harness.ErrNotRunning {
		t.Fatalf("无活动进程应返回 ErrNotRunning，得到 %v", err)
	}
	if err := h.Steer(context.Background(), "", "换个思路"); err != harness.ErrNotRunning {
		t.Fatalf("空会话 id 应返回 ErrNotRunning，得到 %v", err)
	}

	sink := &bufferWriteCloser{}
	h.registerRun(&activeRun{turnID: "turn-1", sessionID: "sess-1", writer: &lineWriter{w: sink}})
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
	h.registerRun(&activeRun{turnID: "turn-9", sessionID: "sess-9", writer: &lineWriter{w: sink}})
	h.ForceStopTurn("turn-9")
	if err := h.runs[0].writer.write([]byte("x")); err == nil {
		t.Fatal("强停应关闭活动轮次的 stdin")
	}
}

func TestImagePayloadsSkipsUnreadable(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(imagePath, []byte("png-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	images := imagePayloads([]string{imagePath, filepath.Join(dir, "missing.png")})
	if len(images) != 1 {
		t.Fatalf("读不到的图片应被跳过，得到 %+v", images)
	}
	if images[0].Type != "image" || images[0].MimeType != "image/png" || images[0].Data == "" {
		t.Fatalf("图片载荷错误: %+v", images[0])
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
	images := imagePayloads([]string{imagePath, pdfPath})
	if len(images) != 1 {
		t.Fatalf("非图片附件应被跳过，得到 %+v", images)
	}
	if images[0].MimeType != "image/png" {
		t.Fatalf("只应下发 a.png，得到 %+v", images[0])
	}
}

func TestInstallGate(t *testing.T) {
	// 目录不存在时应一并建出来（app 启动时给的是临时目录下的子目录）。
	dir := filepath.Join(t.TempDir(), "omp")
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
		`pi.registerCommand("mcode-gate"`,
		`pi.on("tool_call"`,
		"process.env.MCODE_OMP_GATE",
		`ctx.ui.select("mcode-gate " + JSON.stringify(payload)`,
		`["Approve", "Deny"]`,
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

func TestPlanBlocksTool(t *testing.T) {
	tests := []struct {
		interactionMode string
		toolName        string
		want            bool
	}{
		{"plan", "bash", true},
		{"plan", "write", true},
		{"plan", "mcp__x__y", true},
		{"plan", "read", false},
		{"plan", "grep", false},
		{"plan", "yield", false},
		{"build", "bash", false},
		{"", "bash", false},
	}
	for _, test := range tests {
		turn := harness.Turn{InteractionMode: test.interactionMode}
		if got := planBlocksTool(turn, test.toolName); got != test.want {
			t.Errorf("planBlocksTool(%q, %q) = %v，期望 %v", test.interactionMode, test.toolName, got, test.want)
		}
	}
}

// gateSelectEvent 构造一条门禁 select 帧（omp 的 extension_ui_request）。
func gateSelectEvent(t *testing.T, toolName string) *rpcEvent {
	t.Helper()
	title := `mcode-gate {"kind":"command","toolName":"` + toolName + `","input":{"command":"ls -la"},"toolCallId":"tc1"}`
	params, err := json.Marshal(map[string]any{"title": title, "options": []string{"Approve", "Deny"}})
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

func TestAnswerApprovalByInteractionMode(t *testing.T) {
	t.Run("计划模式直接拒绝且不问审批方", func(t *testing.T) {
		approver := &recordingApprover{decision: harness.DecisionAllow}
		sink := &bufferWriteCloser{}
		New(Options{}).handleExtensionUI(context.Background(),
			harness.Turn{Approver: approver, InteractionMode: "plan"}, &lineWriter{w: sink}, gateSelectEvent(t, "bash"))

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
			harness.Turn{Approver: approver, InteractionMode: "plan"}, &lineWriter{w: sink}, gateSelectEvent(t, "read"))

		response := decodeSingleRequest(t, sink.String())
		if response["value"] != "Approve" {
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
			harness.Turn{Approver: approver, Mode: harness.ModeSupervised}, &lineWriter{w: sink}, gateSelectEvent(t, "bash"))

		response := decodeSingleRequest(t, sink.String())
		if response["value"] != "Approve" {
			t.Fatalf("审批方放行时应回写 Approve: %+v", response)
		}
		if len(approver.requests) != 1 || approver.requests[0].Kind != harness.RequestKindCommand {
			t.Fatalf("supervised 应把命令抛给审批方: %+v", approver.requests)
		}
	})
}
