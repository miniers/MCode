package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/procutil"
)

// TestPiResidentHelperProcess 是驻留测试里替换 pi 的假进程（GO_WANT_PI_RESIDENT_HELPER=1 时生效）：
// 一行一个请求，逐条应答并回放事件，直到 stdin 关闭。每次启动往 FAKE_PI_START_FILE 追加一行，
// 每条请求（含命令行里的 --model）记进 FAKE_PI_LOG_FILE，测试据此数进程、查请求。
//
// 约定的 prompt 内容：
//   - "hang"：不回任何事件，等 abort（abort 应答后补 agent_settled）。
//   - "late"：agent_end 之后还有一段文字，随后才 agent_settled（驻留进程必须等到 settled）。
//   - "/ext"（或 " /ext"）：应答 disposition=handled，不启动 run。
//   - 其它：回显 "reply:<prompt>"，agent_end + agent_settled。
func TestPiResidentHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_PI_RESIDENT_HELPER") != "1" {
		return
	}
	appendLine := func(path, line string) {
		if path == "" {
			return
		}
		handle, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			os.Exit(2)
		}
		_, _ = handle.WriteString(strings.TrimRight(line, "\n") + "\n")
		_ = handle.Sync()
		_ = handle.Close()
	}
	appendLine(os.Getenv("FAKE_PI_START_FILE"), strings.Join(os.Args, " "))

	write := func(v map[string]any) {
		data, _ := json.Marshal(v)
		_, _ = os.Stdout.WriteString(string(data) + "\n")
	}
	respond := func(id, command string, data map[string]any) {
		resp := map[string]any{"type": "response", "id": id, "command": command, "success": true}
		if data != nil {
			resp["data"] = data
		}
		write(resp)
	}

	running := false
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			os.Exit(0)
		}
		appendLine(os.Getenv("FAKE_PI_LOG_FILE"), line)
		var req map[string]any
		if json.Unmarshal([]byte(line), &req) != nil {
			continue
		}
		id, _ := req["id"].(string)
		switch req["type"] {
		case "get_state":
			// model.contextWindow 是当前模型的上下文窗口上限，用量事件会带上它。
			respond(id, "get_state", map[string]any{
				"sessionFile": "/tmp/pi-resident/session-1.jsonl",
				"model":       map[string]any{"contextWindow": 123456},
			})
		case "set_model", "set_thinking_level":
			respond(id, req["type"].(string), nil)
		case "abort":
			respond(id, "abort", nil)
			if running {
				running = false
				write(map[string]any{"type": "agent_settled", "aborted": true})
			}
		case "prompt":
			message, _ := req["message"].(string)
			switch message {
			case "/ext", " /ext":
				respond(id, "prompt", map[string]any{"disposition": "handled"})
				continue
			case "hang":
				respond(id, "prompt", map[string]any{"disposition": "started"})
				running = true
				continue
			}
			respond(id, "prompt", map[string]any{"disposition": "started"})
			write(map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant"}})
			write(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "reply:" + message}})
			write(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": "reply:" + message}}, "stopReason": "end_turn", "usage": map[string]any{"input": 10, "output": 2, "totalTokens": 12}}})
			write(map[string]any{"type": "agent_end", "willRetry": false})
			if message == "late" {
				write(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "after-end"}})
			}
			write(map[string]any{"type": "agent_settled"})
		}
	}
}

// residentFake 是驻留测试用的假 pi：与 fakePi 同样走 shell 包装脚本重入测试二进制。
type residentFake struct {
	binary    string
	startFile string
	logFile   string
	env       []string
}

func newResidentFake(t *testing.T) *residentFake {
	t.Helper()
	dir := t.TempDir()
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("解析测试二进制路径失败: %v", err)
	}
	binary := filepath.Join(dir, "pi")
	wrapper := "#!/bin/sh\nexec '" + self + "' -test.run='^TestPiResidentHelperProcess$' -- \"$@\"\n"
	if err := os.WriteFile(binary, []byte(wrapper), 0o755); err != nil {
		t.Fatalf("写入替身启动脚本失败: %v", err)
	}
	fake := &residentFake{
		binary:    binary,
		startFile: filepath.Join(dir, "starts.txt"),
		logFile:   filepath.Join(dir, "requests.jsonl"),
	}
	fake.env = []string{
		"GO_WANT_PI_RESIDENT_HELPER=1",
		"FAKE_PI_START_FILE=" + fake.startFile,
		"FAKE_PI_LOG_FILE=" + fake.logFile,
	}
	return fake
}

// starts 返回假 pi 被启动的次数。
func (f *residentFake) starts(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(f.startFile)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("读取启动记录失败: %v", err)
	}
	return len(strings.Split(strings.TrimSpace(string(data)), "\n"))
}

// requestTypes 按顺序返回假 pi 收到的请求类型（形如 "prompt" 或 "set_model"）。
func (f *residentFake) requestTypes(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(f.logFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("读取请求记录失败: %v", err)
	}
	var types []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var req map[string]any
		if json.Unmarshal([]byte(line), &req) == nil {
			types = append(types, req["type"].(string))
		}
	}
	return types
}

// request 返回第一条指定类型的请求。
func (f *residentFake) request(t *testing.T, typ string) map[string]any {
	t.Helper()
	data, _ := os.ReadFile(f.logFile)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var req map[string]any
		if json.Unmarshal([]byte(line), &req) == nil && req["type"] == typ {
			return req
		}
	}
	return nil
}

func (f *residentFake) harness(t *testing.T, opts Options) *Harness {
	t.Helper()
	opts.Binary = f.binary
	opts.Env = append(opts.Env, f.env...)
	h := New(opts)
	t.Cleanup(func() { _ = h.Close() })
	return h
}

// runTurn 跑一轮并返回事件与当轮回报的会话 id。
func runResidentTurn(t *testing.T, h *Harness, turn harness.Turn) ([]harness.Event, string) {
	t.Helper()
	var events []harness.Event
	if err := h.Run(context.Background(), turn, collectEvents(&events)); err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}
	session := ""
	for _, event := range events {
		if event.Type == harness.EventSession {
			session = event.SessionID
		}
	}
	return events, session
}

// pooledCount 返回当前登记的驻留进程数。
func pooledCount(h *Harness) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.procs)
}

func eventTexts(events []harness.Event) string {
	var out strings.Builder
	for _, event := range events {
		if event.Type == harness.EventText {
			out.WriteString(event.Text)
		}
	}
	return out.String()
}

func lastEventType(events []harness.Event) harness.EventType {
	if len(events) == 0 {
		return ""
	}
	return events[len(events)-1].Type
}

// TestResidentReportsContextWindowFromState 验证用量事件带上 get_state 里的 model.contextWindow。
func TestResidentReportsContextWindowFromState(t *testing.T) {
	fake := newResidentFake(t)
	gate := filepath.Join(t.TempDir(), "gate.json")
	h := fake.harness(t, Options{IdleTTL: time.Minute, GatePath: gate})
	events, _ := runResidentTurn(t, h, harness.Turn{ThreadID: "th-1", WorkDir: t.TempDir(), Prompt: "one"})

	reported := false
	for _, event := range events {
		if event.Type != harness.EventUsage || event.Usage == nil {
			continue
		}
		reported = true
		if event.Usage.ContextWindow != 123456 {
			t.Fatalf("用量事件的上下文窗口应为 123456，得到 %d", event.Usage.ContextWindow)
		}
	}
	if !reported {
		t.Fatalf("没有收到用量事件: %+v", events)
	}
}

func TestResidentReusesProcessAcrossTurns(t *testing.T) {
	fake := newResidentFake(t)
	gate := filepath.Join(t.TempDir(), "gate.json")
	h := fake.harness(t, Options{IdleTTL: time.Minute, GatePath: gate})
	dir := t.TempDir()

	first, session := runResidentTurn(t, h, harness.Turn{ThreadID: "th-1", WorkDir: dir, Prompt: "one", Mode: harness.ModeSupervised})
	if got := eventTexts(first); got != "reply:one" || lastEventType(first) != harness.EventDone {
		t.Fatalf("第一轮事件异常: %q %+v", got, first)
	}
	if session != "/tmp/pi-resident/session-1.jsonl" {
		t.Fatalf("会话 id 异常: %q", session)
	}

	// 第二轮切到 autoAcceptEdits（与 supervised 同属 --no-approve）：门禁文件必须被重写，进程不重启。
	second, _ := runResidentTurn(t, h, harness.Turn{ThreadID: "th-1", WorkDir: dir, Prompt: "two", Mode: harness.ModeAutoAcceptEdits, SessionID: session})
	if got := eventTexts(second); got != "reply:two" || lastEventType(second) != harness.EventDone {
		t.Fatalf("第二轮事件异常: %q %+v", got, second)
	}
	if n := fake.starts(t); n != 1 {
		t.Fatalf("两轮应共用一个进程，启动了 %d 次", n)
	}
	want := []string{"get_state", "prompt", "get_state", "prompt"}
	if got := fake.requestTypes(t); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("请求序列应为 %v，得到 %v", want, got)
	}
	content, err := os.ReadFile(gate)
	if err != nil || !strings.Contains(string(content), `"runtimeMode":"auto"`) {
		t.Fatalf("第二轮的门禁文件应反映 autoAcceptEdits 模式: %q %v", content, err)
	}

	if err := h.Close(); err != nil {
		t.Fatalf("Close 返回错误: %v", err)
	}
	if n := pooledCount(h); n != 0 {
		t.Fatalf("Close 之后不应残留驻留进程: %d", n)
	}
}

func TestResidentSwitchesModelAndThinkingInPlace(t *testing.T) {
	fake := newResidentFake(t)
	h := fake.harness(t, Options{IdleTTL: time.Minute})
	dir := t.TempDir()

	_, session := runResidentTurn(t, h, harness.Turn{ThreadID: "th-1", WorkDir: dir, Prompt: "one", Model: "anthropic/sonnet", Effort: "low"})
	runResidentTurn(t, h, harness.Turn{ThreadID: "th-1", WorkDir: dir, Prompt: "two", Model: "openai/gpt", Effort: "high", SessionID: session})

	if n := fake.starts(t); n != 1 {
		t.Fatalf("换模型不应重启进程，启动了 %d 次", n)
	}
	setModel := fake.request(t, "set_model")
	if setModel == nil || setModel["provider"] != "openai" || setModel["modelId"] != "gpt" {
		t.Fatalf("set_model 请求异常: %+v", setModel)
	}
	thinking := fake.request(t, "set_thinking_level")
	if thinking == nil || thinking["level"] != "high" {
		t.Fatalf("set_thinking_level 请求异常: %+v", thinking)
	}

	// 同一模型同一档位再来一轮：什么都不用发。
	before := len(fake.requestTypes(t))
	runResidentTurn(t, h, harness.Turn{ThreadID: "th-1", WorkDir: dir, Prompt: "three", Model: "openai/gpt", Effort: "high", SessionID: session})
	got := fake.requestTypes(t)[before:]
	if strings.Join(got, ",") != "get_state,prompt" {
		t.Fatalf("模型与档位没变时只应有 get_state 与 prompt，得到 %v", got)
	}
}

func TestResidentRestartsOnSkillsChange(t *testing.T) {
	fake := newResidentFake(t)
	var currentSkills []string
	var currentFull bool
	h := fake.harness(t, Options{
		IdleTTL: time.Minute,
		Skills: func(workDir string) ([]string, bool) {
			return currentSkills, currentFull
		},
	})
	dir := t.TempDir()

	currentSkills = []string{"/path/to/skill-a"}
	_, session := runResidentTurn(t, h, harness.Turn{ThreadID: "th-1", WorkDir: dir, Prompt: "one", Mode: harness.ModeSupervised})
	if n := fake.starts(t); n != 1 {
		t.Fatalf("第一轮启动次数期望 1，得到 %d", n)
	}

	// 相同技能第二轮复用进程
	runResidentTurn(t, h, harness.Turn{ThreadID: "th-1", WorkDir: dir, Prompt: "two", Mode: harness.ModeSupervised, SessionID: session})
	if n := fake.starts(t); n != 1 {
		t.Fatalf("技能未变应复用进程，启动次数期望 1，得到 %d", n)
	}

	// 技能列表变化后应重启进程
	currentSkills = []string{"/path/to/skill-b"}
	runResidentTurn(t, h, harness.Turn{ThreadID: "th-1", WorkDir: dir, Prompt: "three", Mode: harness.ModeSupervised, SessionID: session})
	if n := fake.starts(t); n != 2 {
		t.Fatalf("技能变化后应重启进程，启动次数期望 2，得到 %d", n)
	}
}

// TestResidentRestartsOnSkillModeChange 覆盖清单模式切换：技能列表一样，但从不带 --no-skills
// 的旧模式变成完整清单模式（多了 --no-skills）也是不同的启动参数，必须换进程。
func TestResidentRestartsOnSkillModeChange(t *testing.T) {
	fake := newResidentFake(t)
	var currentFull bool
	h := fake.harness(t, Options{
		IdleTTL: time.Minute,
		Skills: func(workDir string) ([]string, bool) {
			return []string{"/path/to/skill-a"}, currentFull
		},
	})
	dir := t.TempDir()

	_, session := runResidentTurn(t, h, harness.Turn{ThreadID: "th-1", WorkDir: dir, Prompt: "one", Mode: harness.ModeSupervised})
	if n := fake.starts(t); n != 1 {
		t.Fatalf("第一轮启动次数期望 1，得到 %d", n)
	}

	// 技能列表不变，只切换清单模式：也要重启
	currentFull = true
	runResidentTurn(t, h, harness.Turn{ThreadID: "th-1", WorkDir: dir, Prompt: "two", Mode: harness.ModeSupervised, SessionID: session})
	if n := fake.starts(t); n != 2 {
		t.Fatalf("清单模式变化后应重启进程，启动次数期望 2，得到 %d", n)
	}
}

func TestResidentSwitchesModelWithEmptyEffortResetsThinking(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	fake := newResidentFake(t)
	h := fake.harness(t, Options{IdleTTL: time.Minute})
	dir := t.TempDir()

	_, session := runResidentTurn(t, h, harness.Turn{ThreadID: "th-1", WorkDir: dir, Prompt: "one", Model: "anthropic/sonnet", Effort: ""})
	runResidentTurn(t, h, harness.Turn{ThreadID: "th-1", WorkDir: dir, Prompt: "two", Model: "openai/gpt", Effort: "", SessionID: session})

	if n := fake.starts(t); n != 1 {
		t.Fatalf("换模型不应重启进程，启动了 %d 次", n)
	}
	setModel := fake.request(t, "set_model")
	if setModel == nil || setModel["provider"] != "openai" || setModel["modelId"] != "gpt" {
		t.Fatalf("set_model 请求异常: %+v", setModel)
	}
	thinking := fake.request(t, "set_thinking_level")
	if thinking == nil || thinking["level"] != "medium" {
		t.Fatalf("set_thinking_level 请求异常: %+v", thinking)
	}
}

func TestResidentSwitchesModelWithEmptyEffortUsesSettingsDefault(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{"defaultThinkingLevel":"low"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	fake := newResidentFake(t)
	h := fake.harness(t, Options{IdleTTL: time.Minute})
	dir := t.TempDir()

	_, session := runResidentTurn(t, h, harness.Turn{ThreadID: "th-1", WorkDir: dir, Prompt: "one", Model: "anthropic/sonnet", Effort: ""})
	runResidentTurn(t, h, harness.Turn{ThreadID: "th-1", WorkDir: dir, Prompt: "two", Model: "openai/gpt", Effort: "", SessionID: session})

	if n := fake.starts(t); n != 1 {
		t.Fatalf("换模型不应重启进程，启动了 %d 次", n)
	}
	setModel := fake.request(t, "set_model")
	if setModel == nil || setModel["provider"] != "openai" || setModel["modelId"] != "gpt" {
		t.Fatalf("set_model 请求异常: %+v", setModel)
	}
	thinking := fake.request(t, "set_thinking_level")
	if thinking == nil || thinking["level"] != "low" {
		t.Fatalf("set_thinking_level 请求异常: %+v", thinking)
	}
}

func TestResidentRestartsWhenLaunchKeyChanges(t *testing.T) {
	fake := newResidentFake(t)
	h := fake.harness(t, Options{IdleTTL: time.Minute})
	dir := t.TempDir()

	_, session := runResidentTurn(t, h, harness.Turn{ThreadID: "th-1", WorkDir: dir, Prompt: "one", Mode: harness.ModeSupervised})
	// 工作目录变化属于启动参数，只能换进程。
	dir2 := t.TempDir()
	runResidentTurn(t, h, harness.Turn{ThreadID: "th-1", WorkDir: dir2, Prompt: "two", Mode: harness.ModeFullAccess, SessionID: session})
	if n := fake.starts(t); n != 2 {
		t.Fatalf("工作目录变化应换进程，启动了 %d 次", n)
	}

	// 默认模型换成显式模型：启动时没传 --model，无法就地切换。
	runResidentTurn(t, h, harness.Turn{ThreadID: "th-1", WorkDir: dir, Prompt: "three", Mode: harness.ModeFullAccess, Model: "a/b", SessionID: session})
	if n := fake.starts(t); n != 3 {
		t.Fatalf("default 换显式模型应换进程，启动了 %d 次", n)
	}

	// 新对话（没有 SessionID）不能接着旧进程的会话。
	runResidentTurn(t, h, harness.Turn{ThreadID: "th-1", WorkDir: dir, Prompt: "four", Mode: harness.ModeFullAccess, Model: "a/b"})
	if n := fake.starts(t); n != 4 {
		t.Fatalf("新对话应换进程，启动了 %d 次", n)
	}
	if n := pooledCount(h); n != 1 {
		t.Fatalf("同一线程只应留一个驻留进程: %d", n)
	}
}

func TestResidentWithoutThreadIDOrTTLDoesNotPool(t *testing.T) {
	fake := newResidentFake(t)
	dir := t.TempDir()

	h := fake.harness(t, Options{IdleTTL: time.Minute})
	runResidentTurn(t, h, harness.Turn{WorkDir: dir, Prompt: "one"})
	runResidentTurn(t, h, harness.Turn{WorkDir: dir, Prompt: "two"})
	if n := fake.starts(t); n != 2 || pooledCount(h) != 0 {
		t.Fatalf("没有 ThreadID 时每轮一个进程且不驻留: starts=%d pooled=%d", n, pooledCount(h))
	}

	h = fake.harness(t, Options{})
	runResidentTurn(t, h, harness.Turn{ThreadID: "th-1", WorkDir: dir, Prompt: "three"})
	if n := pooledCount(h); n != 0 {
		t.Fatalf("IdleTTL 为 0 时不应驻留: %d", n)
	}
}

func TestResidentWaitsForSettledNotAgentEnd(t *testing.T) {
	fake := newResidentFake(t)
	h := fake.harness(t, Options{IdleTTL: time.Minute})

	events, _ := runResidentTurn(t, h, harness.Turn{ThreadID: "th-1", WorkDir: t.TempDir(), Prompt: "late"})
	if got := eventTexts(events); got != "reply:lateafter-end" {
		t.Fatalf("驻留进程应等到 agent_settled，agent_end 之后的文字也要收到: %q", got)
	}
	if lastEventType(events) != harness.EventDone {
		t.Fatalf("done 应是最后一个事件: %+v", events)
	}
}

func TestResidentHandledPromptFinishesTurn(t *testing.T) {
	fake := newResidentFake(t)
	h := fake.harness(t, Options{IdleTTL: time.Minute})

	events, _ := runResidentTurn(t, h, harness.Turn{ThreadID: "th-1", WorkDir: t.TempDir(), Prompt: "/ext"})
	if lastEventType(events) != harness.EventDone {
		t.Fatalf("被扩展命令消化的 prompt 没有 agent_settled，也应结束当轮: %+v", events)
	}
}

func TestResidentCancelKeepsProcessForNextTurn(t *testing.T) {
	fake := newResidentFake(t)
	h := fake.harness(t, Options{IdleTTL: time.Minute})
	dir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- h.Run(ctx, harness.Turn{ThreadID: "th-1", WorkDir: dir, Prompt: "hang"}, func(harness.Event) {})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for fake.request(t, "prompt") == nil {
		if time.Now().After(deadline) {
			t.Fatal("hang 这轮的 prompt 没有送达")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-errCh:
		if err != context.Canceled {
			t.Fatalf("取消后 Run 应返回 context.Canceled，得到 %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取消后 Run 没有返回")
	}

	abort := fake.request(t, "abort")
	if abort == nil || abort["id"] == "" {
		t.Fatalf("驻留进程取消时应发带 id 的 abort: %+v", abort)
	}

	_, _ = runResidentTurn(t, h, harness.Turn{ThreadID: "th-1", WorkDir: dir, Prompt: "after", SessionID: "/tmp/pi-resident/session-1.jsonl"})
	if n := fake.starts(t); n != 1 {
		t.Fatalf("abort 成功后进程应留给下一轮，启动了 %d 次", n)
	}
}

func TestResidentIdleTimeoutReapsProcess(t *testing.T) {
	fake := newResidentFake(t)
	h := fake.harness(t, Options{IdleTTL: 80 * time.Millisecond})

	runResidentTurn(t, h, harness.Turn{ThreadID: "th-1", WorkDir: t.TempDir(), Prompt: "one"})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if pooledCount(h) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("空闲超时后驻留进程没有被回收")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestResidentEvictsLeastRecentlyUsedBeyondLimit(t *testing.T) {
	fake := newResidentFake(t)
	h := fake.harness(t, Options{IdleTTL: time.Minute, MaxResident: 1})

	runResidentTurn(t, h, harness.Turn{ThreadID: "th-a", WorkDir: t.TempDir(), Prompt: "one"})
	runResidentTurn(t, h, harness.Turn{ThreadID: "th-b", WorkDir: t.TempDir(), Prompt: "two"})

	h.mu.Lock()
	_, hasA := h.procs["th-a"]
	_, hasB := h.procs["th-b"]
	h.mu.Unlock()
	if hasA || !hasB {
		t.Fatalf("超出上限时应淘汰最久没用的空闲进程: th-a=%v th-b=%v", hasA, hasB)
	}
}

func TestResidentSkipsTransientTurns(t *testing.T) {
	fake := newResidentFake(t)
	h := fake.harness(t, Options{IdleTTL: time.Minute})
	dir := t.TempDir()

	runResidentTurn(t, h, harness.Turn{ThreadID: "bee-1", WorkDir: dir, Prompt: "one", Transient: true})
	if n := pooledCount(h); n != 0 {
		t.Fatalf("辅助会话不应占用驻留名额，登记了 %d 个", n)
	}
	runResidentTurn(t, h, harness.Turn{ThreadID: "lead-1", WorkDir: dir, Prompt: "one"})
	if n := pooledCount(h); n != 1 {
		t.Fatalf("普通会话仍应驻留，登记了 %d 个", n)
	}
}

// 并发高峰里全部在忙时 register 回收不了任何进程；其中一轮收尾后要补收，把数量压回上限。
func TestResidentTrimsAfterBusyPeakEnds(t *testing.T) {
	fake := newResidentFake(t)
	h := fake.harness(t, Options{IdleTTL: time.Minute, MaxResident: 1})
	dir := t.TempDir()

	startHang := func(threadID string) (cancel func(), done <-chan error) {
		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() {
			errCh <- h.Run(ctx, harness.Turn{ThreadID: threadID, WorkDir: dir, Prompt: "hang"}, func(harness.Event) {})
		}()
		return cancel, errCh
	}
	cancelA, doneA := startHang("th-a")
	cancelB, doneB := startHang("th-b")
	defer func() {
		cancelB()
		<-doneB
	}()

	deadline := time.Now().Add(5 * time.Second)
	for pooledCount(h) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("两个并发轮次都应登记为驻留进程")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancelA()
	select {
	case <-doneA:
	case <-time.After(5 * time.Second):
		t.Fatal("取消后 Run 没有返回")
	}

	h.mu.Lock()
	_, hasA := h.procs["th-a"]
	_, hasB := h.procs["th-b"]
	h.mu.Unlock()
	if hasA || !hasB {
		t.Fatalf("th-a 收尾后应被补收，正在跑的 th-b 不动: th-a=%v th-b=%v", hasA, hasB)
	}
}

func TestResidentRestartsAfterProcessDies(t *testing.T) {
	fake := newResidentFake(t)
	h := fake.harness(t, Options{IdleTTL: time.Minute})
	dir := t.TempDir()

	_, session := runResidentTurn(t, h, harness.Turn{ThreadID: "th-1", WorkDir: dir, Prompt: "one"})
	h.mu.Lock()
	victim := h.procs["th-1"]
	h.mu.Unlock()
	if victim == nil {
		t.Fatal("第一轮之后应有驻留进程")
	}
	_ = killProcessGroup(victim.cmd, os.Kill)
	<-victim.exited

	runResidentTurn(t, h, harness.Turn{ThreadID: "th-1", WorkDir: dir, Prompt: "two", SessionID: session})
	if n := fake.starts(t); n != 2 {
		t.Fatalf("进程死后下一轮应重新启动，启动了 %d 次", n)
	}
	// 重启的进程带着会话文件恢复。
	data, _ := os.ReadFile(fake.startFile)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if !strings.Contains(lines[1], "--session "+session) {
		t.Fatalf("重启应以 --session 恢复: %q", lines[1])
	}
}

type blockingWriteCloser struct {
	started chan struct{}
	release chan struct{}
	closed  chan struct{}
}

func (w *blockingWriteCloser) Write(p []byte) (int, error) {
	close(w.started)
	<-w.release
	return len(p), nil
}

func (w *blockingWriteCloser) Close() error {
	close(w.closed)
	return nil
}

// TestProcCloseNotBlockedByWriterLock 回归：写端握着 lineWriter 的锁卡在管道 Write 上时，
// close 仍要立刻返回，发信号和关 stdin 都不能等那把锁。
func TestProcCloseNotBlockedByWriterLock(t *testing.T) {
	sink := &blockingWriteCloser{started: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	defer func() {
		select {
		case <-sink.release:
		default:
			close(sink.release)
		}
	}()
	p := &proc{writer: procutil.NewLineWriter(sink), exited: make(chan struct{})}
	go func() { _ = p.writer.WriteLine([]byte("x")) }()
	<-sink.started

	done := make(chan struct{})
	go func() {
		p.close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close 被 writer 的写锁卡住")
	}
	close(sink.release)
	select {
	case <-sink.closed:
	case <-time.After(time.Second):
		t.Fatal("close 返回后 stdin 没有被关掉")
	}
	close(p.exited) // 让 close 里等 killGrace 的那条协程退出
}
