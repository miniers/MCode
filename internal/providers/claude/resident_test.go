package claude

// 本文件覆盖常驻进程模式（resident.go）：进程复用、指纹/会话不匹配重建、空闲回收、
// 取消保活、失效重试、EvictThread/Close，以及审批方按轮切换。
//
// 假 CLI 仍然由测试二进制自己充当（TestMain 看到 MCODE_TEST_FAKE_CLAUDE 就调用 runFakeClaude），
// 常驻模式在 runFakeClaude 里转发到本文件的 runFakeResidentClaude：
//   - resident:<fixture>：每收到一帧 user 消息就回放 fixture（含 result），进程继续存活；
//   - resident_die_after_first:<fixture>：回放完第一轮就退出，模拟空闲后进程已消失；
//   - resident_block:<fixture>：只回 fixture 首行（system/init）就阻塞，收到 interrupt 才收尾；
//   - resident_reject_control:<fixture>：同 resident，但对 set_model 等热切换控制帧回 error；
//   - resident_steer_merge:<fixture>：首轮只回 init 就等 steer，第二帧 user 到达后回显它并并入当前轮收尾；
//   - resident_steer_queue:<fixture>：首轮只回 init，第二帧 user 到达后先给首轮收尾，再回显它并跑完下一轮；
//   - resident_fail_stderr:<fixture>：收到首帧 user 后写 stderr 并以 1 退出，不产出任何输出。
//   - resident_bg_notify:<fixture>：正常回一轮，轮内先报 task_started；result 之后约 200ms 自己再跑一段
//     （task_updated/task_notification/background_tasks_changed/init/status/assistant/result），没有 user 回显；
//   - resident_bg_double:<fixture>：同上，但自发输出段里有两段正文与两个 result；
//   - resident_bg_hold:<fixture>：只报 task_started，之后不再吐任何帧（后台任务一直没结束）。
//   - resident_startup_init:<fixture>：进程启动后、读第一帧之前就先输出 fixture 首行（system/init），
//     之后按 resident 的正常流程处理用户帧（模拟真实 CLI 启动期就吐 init）。
// 用户帧条数由 MCODE_TEST_FAKE_USERS 记录，用例据此断言 Attach 轮没有多写用户帧。
// 每次启动的 argv 由 runFakeClaude 追加写进 MCODE_TEST_FAKE_LOG，用例据此数启动次数与查参数。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"mcode/internal/harness"
)

const (
	// fakeClaudeLogEnv 非空时假 CLI 每次启动把 argv 追加成一行写进该文件。
	fakeClaudeLogEnv = "MCODE_TEST_FAKE_LOG"
	// fakeResidentModePrefix 是常驻假 CLI 的模式前缀。
	fakeResidentModePrefix = "resident"
	// residentTestThreadID 是常驻用例统一使用的线程 id。
	residentTestThreadID = "thread-resident"
	// fakeFramesEnv 非空时假 CLI 把收到的每个控制帧（subtype 与请求体）追加成一行写进该文件。
	fakeFramesEnv = "MCODE_TEST_FAKE_FRAMES"
	// fakeInterruptedResult 是假 CLI 为被打断那一轮补发的 result（is_error=true）。
	fakeInterruptedResult = `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"interrupted","stop_reason":"error_during_execution"}`
	// fakeUsersEnv 非空时假 CLI 把收到的每个 user 帧追加成一行写进该文件。
	fakeUsersEnv = "MCODE_TEST_FAKE_USERS"
)

// residentBgDelay 是假 CLI 在 result 之后隔多久自己再跑一段，模拟后台任务结束后的自发续跑
// （真实 CLI 大约 14 秒，这里压缩到 200ms）。
const residentBgDelay = 200 * time.Millisecond

// fakeTaskStartedFrame 是轮内表示后台任务已启动的帧（模型用 Bash 的 run_in_background 时 CLI 会发）。
const fakeTaskStartedFrame = `{"type":"system","subtype":"task_started","task_id":"task-1","tool_use_id":"toolu_bg_1","description":"sleep 5"}`

// runFakeResidentClaude 实现常驻假 CLI：逐行读 stdin 的 stream-json 帧（单行可能很长，用 Reader
// 而不是默认缓冲的 Scanner），stdin 关闭（EOF）时按真实 CLI 的行为退出 0。
func runFakeResidentClaude(mode string) {
	name, fixture, found := strings.Cut(mode, ":")
	if !found || fixture == "" {
		_, _ = fmt.Fprintf(os.Stderr, "invalid resident mode %q\n", mode)
		os.Exit(2)
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to read fixture file %s: %v\n", fixture, err)
		os.Exit(2)
	}
	// 首行通常是 system/init：resident_block 只回这一行，然后等下一条帧。
	firstLine := data
	if idx := bytes.IndexByte(data, '\n'); idx >= 0 {
		firstLine = data[:idx+1]
	}

	state := &fakeResidentState{name: name, data: data, firstLine: firstLine, startupInit: name == "resident_startup_init"}
	// 启动期就吐 init：此时 harness 那边可能还没挂接轮次，用来覆盖「attach 之前的 init 不是自发轮次」。
	if state.startupInit {
		_, _ = os.Stdout.Write(firstLine)
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		line, readErr := reader.ReadBytes('\n')
		if frame := bytes.TrimSpace(line); len(frame) > 0 {
			if state.handle(frame) {
				// 模拟空闲后进程消失：这一轮已经回放完，直接退出。
				os.Exit(0)
			}
		}
		if readErr != nil {
			os.Exit(0)
		}
	}
}

// fakeResidentFrame 是假 CLI 从 stdin 读到的一帧：区分 user 消息与控制请求。
type fakeResidentFrame struct {
	Type      string `json:"type"`
	UUID      string `json:"uuid"`
	RequestID string `json:"request_id"`
	Request   struct {
		Subtype string `json:"subtype"`
	} `json:"request"`
}

// fakeResidentState 是假 CLI 的状态：已收到的 user 帧数决定 steer 模式下该回什么。
type fakeResidentState struct {
	name      string
	data      []byte
	firstLine []byte
	users     int
	// startupInit 为真时进程在读第一帧之前就先输出 firstLine（启动期的 system/init）。
	startupInit bool
}

// fakeControlResponse 按真实 CLI 的形状拼控制应答行。
func fakeControlResponse(requestID string, success bool) string {
	if success {
		return fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":%q,"response":{}}}`, requestID)
	}
	return fmt.Sprintf(`{"type":"control_response","response":{"subtype":"error","request_id":%q,"error":"rejected by fake"}}`, requestID)
}

// handle 处理一帧；返回 true 表示进程要立刻退出。
func (st *fakeResidentState) handle(frame []byte) bool {
	var parsed fakeResidentFrame
	if err := json.Unmarshal(frame, &parsed); err != nil {
		return false
	}
	out := os.Stdout
	switch parsed.Type {
	case "user":
		st.users++
		if path := os.Getenv(fakeUsersEnv); path != "" {
			appendFakeLine(path, string(frame))
		}
		rest := st.data[len(st.firstLine):]
		echo := fmt.Sprintf(`{"type":"user","uuid":%q,"isReplay":true,"message":{"role":"user","content":"steer"}}`+"\n", parsed.UUID)
		switch st.name {
		case "resident_block":
			_, _ = out.Write(st.firstLine)
		case "resident_fail_stderr":
			_, _ = fmt.Fprintln(os.Stderr, "fake: fatal startup error")
			os.Exit(1)
		case "resident_steer_merge":
			switch st.users {
			case 1:
				_, _ = out.Write(st.firstLine)
			case 2:
				_, _ = out.WriteString(echo)
				_, _ = out.Write(rest)
			default:
				_, _ = out.Write(st.data)
			}
		case "resident_steer_queue":
			switch st.users {
			case 1:
				_, _ = out.Write(st.firstLine)
			case 2:
				_, _ = out.Write(rest)
				_, _ = out.WriteString(echo)
				_, _ = out.Write(st.data)
			default:
				_, _ = out.Write(st.data)
			}
		case "resident_bg_immediate":
			_, _ = out.Write(st.firstLine)
			_, _ = out.WriteString(fakeTaskStartedFrame + "\n")
			_, _ = out.Write(rest)
			_, _ = out.WriteString(`{"type":"system","subtype":"task_notification","task_id":"task-1","status":"completed"}` + "\n")
			_, _ = out.WriteString(`{"type":"system","subtype":"init","session_id":"sess-123"}` + "\n")
			_, _ = out.WriteString(fakeAssistantFrame("紧随结果的续跑") + "\n")
			_, _ = out.WriteString(`{"type":"result","subtype":"success","is_error":false,"result":"续跑完成","stop_reason":"end_turn"}` + "\n")
		case "resident_bg_hold", "resident_bg_notify", "resident_bg_double":
			// 轮内先报后台任务已启动，再回正常的一轮；后台那次续跑由 runBackgroundTasks 另起协程发。
			_, _ = out.Write(st.firstLine)
			_, _ = out.WriteString(fakeTaskStartedFrame + "\n")
			_, _ = out.Write(rest)
			if st.users == 1 && st.name != "resident_bg_hold" {
				go st.runBackgroundTasks()
			}
		default:
			_, _ = out.Write(st.data)
		}
		return st.name == "resident_die_after_first"
	case "control_request":
		if path := os.Getenv(fakeFramesEnv); path != "" {
			if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				_, _ = fmt.Fprintf(f, "%s\t%s\n", parsed.Request.Subtype, frame)
				_ = f.Close()
			}
		}
		if parsed.Request.Subtype == "interrupt" {
			_, _ = out.WriteString(fakeControlResponse(parsed.RequestID, true) + "\n")
			_, _ = out.WriteString(fakeInterruptedResult + "\n")
			return false
		}
		if parsed.Request.Subtype == "stop_task" && st.name == "resident_stop_silent" {
			return false
		}
		_, _ = out.WriteString(fakeControlResponse(parsed.RequestID, st.name != "resident_reject_control") + "\n")
	}
	return false
}

// fixtureAbs 返回 testdata 下 fixture 的绝对路径（假 CLI 在子进程里按绝对路径读取）。
func fixtureAbs(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("解析 fixture 路径失败: %v", err)
	}
	return path
}

// residentTurn 构造一个满足常驻条件的轮次：把假 CLI 模式与启动记录写进 turn.Env。
func residentTurn(mode, logPath, workDir string) harness.Turn {
	return harness.Turn{
		ThreadID: residentTestThreadID,
		Prompt:   "这一轮的问题",
		WorkDir:  workDir,
		Mode:     harness.ModeAutoAcceptEdits,
		Env:      []string{fakeClaudeEnv + "=" + mode, fakeClaudeLogEnv + "=" + logPath},
	}
}

// legacyTurn 构造一个只能走一轮一进程的轮次（假 CLI 直接回放 fixture 后退出）。
func legacyTurn(fixture, logPath, workDir string) harness.Turn {
	return harness.Turn{
		ThreadID: residentTestThreadID,
		Prompt:   "走旧路径的一轮",
		WorkDir:  workDir,
		Mode:     harness.ModeAutoAcceptEdits,
		Env:      []string{fakeClaudeEnv + "=" + fixture, fakeClaudeLogEnv + "=" + logPath},
	}
}

// collectEvents 返回把事件追加进 dst 的 emit 回调。
func collectEvents(dst *[]harness.Event) func(harness.Event) {
	return func(e harness.Event) { *dst = append(*dst, e) }
}

// fakeStartArgvs 读出假 CLI 的启动记录：每行是一次启动的 argv（以空格拼起来）。
func fakeStartArgvs(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("读取启动记录失败: %v", err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// waitFor 轮询等待条件成立（上限 timeout），避免固定 sleep 带来的抖动。
func waitFor(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 %s 超时（%s）", desc, timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// residentCount 返回 harness 当前登记的常驻进程数。
func residentCount(h *Harness) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.residents)
}

// hasEvent 判断事件序列里是否出现过指定类型。
func hasEvent(events []harness.Event, typ harness.EventType) bool {
	for _, e := range events {
		if e.Type == typ {
			return true
		}
	}
	return false
}

// eventTexts 拼出所有文本事件的正文，供断言模型回复内容。
func eventTexts(events []harness.Event) string {
	var sb strings.Builder
	for _, e := range events {
		if e.Type == harness.EventText {
			sb.WriteString(e.Text)
		}
	}
	return sb.String()
}

// sessionFrom 取最后一个会话事件里的会话 id。
func sessionFrom(events []harness.Event) string {
	session := ""
	for _, e := range events {
		if e.Type == harness.EventSession {
			session = e.SessionID
		}
	}
	return session
}

// runCancelAfter 跑一轮并在收到第 n 个事件时取消 ctx；返回这一轮的事件与 Run 的错误。
// 送达信号（EventTurnAccepted）不计入序号：它只表示输入已交给 CLI，不算一轮的输出事件。
func runCancelAfter(h *Harness, turn harness.Turn, n int) ([]harness.Event, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var events []harness.Event
	seen := 0
	err := h.Run(ctx, turn, func(e harness.Event) {
		events = append(events, e)
		if e.Type == harness.EventTurnAccepted {
			return
		}
		seen++
		if seen == n {
			cancel()
		}
	})
	return events, err
}

func TestResident_StopBackgroundTask(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	for _, tc := range []struct {
		name   string
		mode   string
		reason string
	}{
		{name: "success", mode: "resident"},
		{name: "error", mode: "resident_reject_control", reason: "rejected by fake"},
		{name: "timeout", mode: "resident_stop_silent", reason: "context deadline exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			framesPath := filepath.Join(dir, "frames.log")
			h := New(Options{Binary: os.Args[0], IdleTTL: time.Minute})
			defer h.Close()
			h.SetUnsolicitedTurnHandler(func(string) {})
			turn := residentTurn(tc.mode+":"+fixture, filepath.Join(dir, "starts.log"), dir)
			turn.Env = append(turn.Env, fakeFramesEnv+"="+framesPath)
			if err := h.Run(context.Background(), turn, func(harness.Event) {}); err != nil {
				t.Fatalf("启动常驻进程失败: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := h.StopBackgroundTask(ctx, residentTestThreadID, "task-1")
			if tc.reason == "" && err != nil {
				t.Fatalf("停止后台任务失败: %v", err)
			}
			if tc.reason != "" && (err == nil || !strings.Contains(err.Error(), tc.reason)) {
				t.Fatalf("期望错误包含 %q，实际: %v", tc.reason, err)
			}
			data, err := os.ReadFile(framesPath)
			if err != nil {
				t.Fatalf("读取控制帧失败: %v", err)
			}
			parts := strings.SplitN(strings.TrimSpace(string(data)), "\t", 2)
			if len(parts) != 2 || parts[0] != "stop_task" {
				t.Fatalf("控制帧不正确: %s", data)
			}
			var frame struct {
				Type string `json:"type"`
				Request struct {
					Subtype string `json:"subtype"`
					TaskID string `json:"task_id"`
				} `json:"request"`
			}
			if err := json.Unmarshal([]byte(parts[1]), &frame); err != nil || frame.Type != "control_request" || frame.Request.Subtype != "stop_task" || frame.Request.TaskID != "task-1" {
				t.Fatalf("停止任务请求不正确: %s, %v", data, err)
			}
			if h.HasUnsolicitedOutput(residentTestThreadID) {
				t.Fatal("控制应答不应进入自发输出")
			}
			h.mu.Lock()
			p := h.residents[residentTestThreadID]
			h.mu.Unlock()
			p.mu.Lock()
			waiters := len(p.controlWaiters)
			p.mu.Unlock()
			if waiters != 0 {
				t.Fatalf("控制应答等待者未清理: %d", waiters)
			}
		})
	}
}

func TestResident_StopBackgroundTaskMissingProcess(t *testing.T) {
	h := New(Options{Binary: os.Args[0], IdleTTL: time.Minute})
	defer h.Close()
	if err := h.StopBackgroundTask(context.Background(), residentTestThreadID, "task-1"); err == nil || !strings.Contains(err.Error(), "后台任务所在的 claude 进程已退出") {
		t.Fatalf("缺少常驻进程时应返回退出错误: %v", err)
	}
}

func TestResident_ReusesProcessWithinThread(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	logPath := filepath.Join(t.TempDir(), "starts.log")
	h := New(Options{Binary: os.Args[0], IdleTTL: time.Minute})
	defer h.Close()

	turn := residentTurn("resident:"+fixture, logPath, t.TempDir())

	var first []harness.Event
	if err := h.Run(context.Background(), turn, collectEvents(&first)); err != nil {
		t.Fatalf("第一轮 Run 返回错误: %v", err)
	}
	if !hasEvent(first, harness.EventDone) {
		t.Fatalf("第一轮应以 EventDone 收尾: %+v", first)
	}
	if pids := h.ProcessRootPIDs(residentTestThreadID); len(pids) == 0 {
		t.Fatalf("第一轮结束后线程应留下常驻进程 PID")
	}

	// 第二轮带上第一轮上报的会话 id：指纹与会话都对得上，应当复用同一个进程。
	turn.SessionID = "sess-123"
	var second []harness.Event
	if err := h.Run(context.Background(), turn, collectEvents(&second)); err != nil {
		t.Fatalf("第二轮 Run 返回错误: %v", err)
	}

	starts := fakeStartArgvs(t, logPath)
	if len(starts) != 1 {
		t.Fatalf("两轮应复用同一个常驻进程，实际启动 %d 次: %v", len(starts), starts)
	}
	if !strings.Contains(starts[0], "--input-format stream-json") {
		t.Errorf("常驻进程的 argv 应带 --input-format stream-json: %s", starts[0])
	}
	if len(second) == 0 || second[0].Type != harness.EventSession || second[0].SessionID != "sess-123" {
		t.Fatalf("复用进程时第二轮开头应先补发会话事件: %+v", second)
	}
	if !hasEvent(second, harness.EventDone) {
		t.Fatalf("第二轮应以 EventDone 收尾: %+v", second)
	}
}

func TestResident_RebuildsOnSessionMismatch(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	logPath := filepath.Join(t.TempDir(), "starts.log")
	h := New(Options{Binary: os.Args[0], IdleTTL: time.Minute})
	defer h.Close()

	turn := residentTurn("resident:"+fixture, logPath, t.TempDir())
	if err := h.Run(context.Background(), turn, func(harness.Event) {}); err != nil {
		t.Fatalf("第一轮 Run 返回错误: %v", err)
	}

	// 第二轮换了会话：进程里的会话对不上，必须用 --resume 重建。
	turn.SessionID = "sess-other"
	if err := h.Run(context.Background(), turn, func(harness.Event) {}); err != nil {
		t.Fatalf("第二轮 Run 返回错误: %v", err)
	}

	starts := fakeStartArgvs(t, logPath)
	if len(starts) != 2 {
		t.Fatalf("会话不匹配时应重建进程，实际启动 %d 次: %v", len(starts), starts)
	}
	if strings.Contains(starts[0], "--resume") {
		t.Errorf("第一轮没有会话 id，argv 不该带 --resume: %s", starts[0])
	}
	if !strings.Contains(starts[1], "--resume sess-other") {
		t.Errorf("重建的进程应带 --resume sess-other: %s", starts[1])
	}
}

func TestResident_RebuildsOnFingerprintChange(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	cases := []struct {
		name   string
		mutate func(turn *harness.Turn)
	}{
		{name: "模型变化", mutate: func(turn *harness.Turn) { turn.Model = "claude-test-model" }},
		{name: "权限模式变化", mutate: func(turn *harness.Turn) { turn.Mode = harness.ModeReadOnly }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "starts.log")
			h := New(Options{Binary: os.Args[0], IdleTTL: time.Minute})
			defer h.Close()

			turn := residentTurn("resident:"+fixture, logPath, t.TempDir())
			turn.SessionID = "sess-123"
			if err := h.Run(context.Background(), turn, func(harness.Event) {}); err != nil {
				t.Fatalf("第一轮 Run 返回错误: %v", err)
			}

			// 启动指纹里的东西变了：进程不能复用，必须重建。
			tc.mutate(&turn)
			if err := h.Run(context.Background(), turn, func(harness.Event) {}); err != nil {
				t.Fatalf("第二轮 Run 返回错误: %v", err)
			}

			if starts := fakeStartArgvs(t, logPath); len(starts) != 2 {
				t.Fatalf("指纹变化时应重建进程，实际启动 %d 次: %v", len(starts), starts)
			}
		})
	}
}

func TestResident_FallsBackToSingleProcess(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	cases := []struct {
		name      string
		idleTTL   time.Duration
		threadID  string
		transient bool
	}{
		{name: "IdleTTL为零", idleTTL: 0, threadID: residentTestThreadID},
		{name: "ThreadID为空", idleTTL: time.Minute, threadID: ""},
		{name: "Transient为true", idleTTL: time.Minute, threadID: residentTestThreadID, transient: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "starts.log")
			h := New(Options{Binary: os.Args[0], IdleTTL: tc.idleTTL})
			defer h.Close()

			turn := legacyTurn(fixture, logPath, t.TempDir())
			turn.ThreadID = tc.threadID
			turn.Transient = tc.transient

			var events []harness.Event
			if err := h.Run(context.Background(), turn, collectEvents(&events)); err != nil {
				t.Fatalf("走旧路径的 Run 返回错误: %v", err)
			}
			if !hasEvent(events, harness.EventDone) {
				t.Fatalf("这一轮应以 EventDone 收尾: %+v", events)
			}

			starts := fakeStartArgvs(t, logPath)
			if len(starts) != 1 {
				t.Fatalf("旧路径只应启动一次进程，实际 %d 次: %v", len(starts), starts)
			}
			if strings.Contains(starts[0], "--input-format") {
				t.Errorf("旧路径的 argv 不该带 --input-format: %s", starts[0])
			}
			if n := residentCount(h); n != 0 {
				t.Errorf("不满足常驻条件时不该登记常驻进程，实际 %d 个", n)
			}
		})
	}
}

func TestResident_ReapsIdleProcess(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	logPath := filepath.Join(t.TempDir(), "starts.log")
	h := New(Options{Binary: os.Args[0], IdleTTL: 150 * time.Millisecond})
	defer h.Close()

	turn := residentTurn("resident:"+fixture, logPath, t.TempDir())
	if err := h.Run(context.Background(), turn, func(harness.Event) {}); err != nil {
		t.Fatalf("第一轮 Run 返回错误: %v", err)
	}

	// IdleTTL 只有 150ms，这里不再断言“跑完立刻有 PID”（机器卡顿就可能已经回收），只等它变空。
	waitFor(t, 3*time.Second, "空闲超时后常驻进程应被回收", func() bool {
		return len(h.ProcessRootPIDs(residentTestThreadID)) == 0
	})

	// 进程已被回收：这一轮重新拉起，启动记录变成 2 行。
	if err := h.Run(context.Background(), turn, func(harness.Event) {}); err != nil {
		t.Fatalf("回收后的第二轮 Run 返回错误: %v", err)
	}
	if starts := fakeStartArgvs(t, logPath); len(starts) != 2 {
		t.Fatalf("回收后应重新拉起进程，实际启动 %d 次: %v", len(starts), starts)
	}
}

func TestResident_InterruptKeepsProcessAlive(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	logPath := filepath.Join(t.TempDir(), "starts.log")
	h := New(Options{Binary: os.Args[0], IdleTTL: time.Minute, InterruptGrace: 3 * time.Second})
	defer h.Close()

	turn := residentTurn("resident_block:"+fixture, logPath, t.TempDir())

	// 首轮只回了 system/init 就被取消：Run 按取消返回，且不冒充“完成”或“失败”上报。
	first, err := runCancelAfter(h, turn, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后 Run 应返回 context.Canceled，实际 %v", err)
	}
	if hasEvent(first, harness.EventError) || hasEvent(first, harness.EventDone) {
		t.Fatalf("被打断那一轮不该向上层发 EventError/EventDone: %+v", first)
	}
	if pids := h.ProcessRootPIDs(residentTestThreadID); len(pids) == 0 {
		t.Fatalf("打断后进程应继续驻留，实际 PID 已清空")
	}

	// 第二轮沿用同一个会话：进程还在，仍复用（启动记录继续是 1 行），再次被打断后依旧存活。
	// 复用进程会先补发一个会话事件，所以这一轮在收到第二个事件（假 CLI 的 init 行）后取消。
	turn.SessionID = "sess-123"
	second, err := runCancelAfter(h, turn, 2)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("第二轮取消后 Run 应返回 context.Canceled，实际 %v", err)
	}
	if hasEvent(second, harness.EventError) || hasEvent(second, harness.EventDone) {
		t.Fatalf("第二轮被打断时也不该发 EventError/EventDone: %+v", second)
	}
	if starts := fakeStartArgvs(t, logPath); len(starts) != 1 {
		t.Fatalf("打断不该换进程，实际启动 %d 次: %v", len(starts), starts)
	}
	if pids := h.ProcessRootPIDs(residentTestThreadID); len(pids) == 0 {
		t.Fatalf("第二轮打断后进程应继续驻留")
	}
}

func TestResident_RetriesAfterReusedProcessDied(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	logPath := filepath.Join(t.TempDir(), "starts.log")
	h := New(Options{Binary: os.Args[0], IdleTTL: time.Minute})
	defer h.Close()

	// 假 CLI 回放完第一轮就退出：第二轮的写入会失败（或读到 EOF 且没有任何输出）。
	turn := residentTurn("resident_die_after_first:"+fixture, logPath, t.TempDir())
	var first []harness.Event
	if err := h.Run(context.Background(), turn, collectEvents(&first)); err != nil {
		t.Fatalf("第一轮 Run 返回错误: %v", err)
	}
	if !hasEvent(first, harness.EventDone) {
		t.Fatalf("第一轮应以 EventDone 收尾: %+v", first)
	}

	turn.SessionID = "sess-123"
	var second []harness.Event
	if err := h.Run(context.Background(), turn, collectEvents(&second)); err != nil {
		t.Fatalf("复用进程失效后应自动用 --resume 重试成功，实际错误: %v", err)
	}
	if !hasEvent(second, harness.EventDone) {
		t.Fatalf("重试成功后第二轮应以 EventDone 收尾: %+v", second)
	}
	if hasEvent(first, harness.EventError) || hasEvent(second, harness.EventError) {
		t.Fatalf("内部重试不该向上层报 EventError: first=%+v second=%+v", first, second)
	}

	starts := fakeStartArgvs(t, logPath)
	if len(starts) != 2 {
		t.Fatalf("失效后应重建一次进程，实际启动 %d 次: %v", len(starts), starts)
	}
	if !strings.Contains(starts[1], "--resume sess-123") {
		t.Errorf("重建的进程应带 --resume sess-123: %s", starts[1])
	}
}

func TestResident_EvictThreadAndClose(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	logPath := filepath.Join(t.TempDir(), "starts.log")
	h := New(Options{Binary: os.Args[0], IdleTTL: time.Minute})
	defer h.Close()

	pidEmpty := func() bool { return len(h.ProcessRootPIDs(residentTestThreadID)) == 0 }

	turn := residentTurn("resident:"+fixture, logPath, t.TempDir())
	if err := h.Run(context.Background(), turn, func(harness.Event) {}); err != nil {
		t.Fatalf("第一轮 Run 返回错误: %v", err)
	}
	if pidEmpty() {
		t.Fatalf("第一轮结束后应留下常驻进程 PID")
	}

	h.EvictThread(residentTestThreadID)
	waitFor(t, 3*time.Second, "EvictThread 后常驻进程应被回收", pidEmpty)

	// 被驱逐后这一轮重新拉起进程。
	turn.SessionID = "sess-123"
	if err := h.Run(context.Background(), turn, func(harness.Event) {}); err != nil {
		t.Fatalf("驱逐后的第二轮 Run 返回错误: %v", err)
	}
	if starts := fakeStartArgvs(t, logPath); len(starts) != 2 {
		t.Fatalf("驱逐后应重新拉起进程，实际启动 %d 次: %v", len(starts), starts)
	}

	h.Close()
	waitFor(t, 3*time.Second, "Close 后常驻进程应被回收", pidEmpty)

	// Close 之后常驻模式整体失效：这一轮退回一轮一进程（argv 里没有 --input-format）。
	if err := h.Run(context.Background(), legacyTurn(fixture, logPath, t.TempDir()), func(harness.Event) {}); err != nil {
		t.Fatalf("Close 之后走旧路径的 Run 返回错误: %v", err)
	}
	starts := fakeStartArgvs(t, logPath)
	if len(starts) != 3 {
		t.Fatalf("Close 之后应再启动一次一次性进程，实际启动 %d 次: %v", len(starts), starts)
	}
	if strings.Contains(starts[2], "--input-format") {
		t.Errorf("Close 之后的 argv 不该带 --input-format: %s", starts[2])
	}
	if n := residentCount(h); n != 0 {
		t.Errorf("Close 之后不该再登记常驻进程，实际 %d 个", n)
	}
}

// countingApprover 记录被调用次数与最近一次请求，用于验证审批方按轮切换。
type countingApprover struct {
	mu    sync.Mutex
	count int
	last  harness.ApprovalRequest
}

func (c *countingApprover) Approve(ctx context.Context, req harness.ApprovalRequest) (harness.Decision, error) {
	c.mu.Lock()
	c.count++
	c.last = req
	c.mu.Unlock()
	return harness.DecisionAllow, nil
}

func (c *countingApprover) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

func (c *countingApprover) lastTool() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last.ToolName
}

func TestResident_ApproverSwitchesBetweenTurns(t *testing.T) {
	a := &countingApprover{}
	b := &countingApprover{}
	server, cleanup := newTestApprovalServer(t, a)
	defer cleanup()

	if payload := callApproveTool(t, server, "Bash", `{"command":"git status"}`); payload["behavior"] != "allow" {
		t.Fatalf("第一轮审批应放行: %+v", payload)
	}
	if a.calls() != 1 || b.calls() != 0 {
		t.Fatalf("第一轮的审批只该到 A，实际 A=%d B=%d", a.calls(), b.calls())
	}

	// 下一轮换了审批方：常驻进程的审批服务跨轮复用，只替换审批方。
	server.setApprover(b)
	if payload := callApproveTool(t, server, "Bash", `{"command":"git diff"}`); payload["behavior"] != "allow" {
		t.Fatalf("第二轮审批应放行: %+v", payload)
	}
	if b.calls() != 1 {
		t.Fatalf("第二轮审批该到 B，实际 B=%d", b.calls())
	}
	if tool := b.lastTool(); tool != "Bash" {
		t.Errorf("B 收到的请求应带工具名 Bash，实际 %q", tool)
	}
	if a.calls() != 1 {
		t.Fatalf("换审批方后 A 不该再收到请求，实际 A=%d", a.calls())
	}

	// 轮次结束后 runOnResident 会把审批方置空：此时审批一律拒绝，也不再打扰任何审批方。
	server.setApprover(nil)
	if payload := callApproveTool(t, server, "Bash", `{"command":"rm -rf /tmp/mcode-test"}`); payload["behavior"] != "deny" {
		t.Errorf("没有审批方时应一律拒绝，实际 %+v", payload)
	}
	if a.calls() != 1 || b.calls() != 1 {
		t.Errorf("没有审批方时不该再调用审批方，实际 A=%d B=%d", a.calls(), b.calls())
	}
	if approver := server.currentApprover(); approver != nil {
		t.Errorf("currentApprover 应返回 nil，实际 %#v", approver)
	}
}

// TestResidentRealCLI 用真实 claude CLI 冒烟常驻模式：默认跳过，MCODE_TEST_REAL_CLAUDE=1 时运行。
// 两轮之间的 PID 相同才算复用了同一个进程；第二轮要能回忆起第一轮埋下的暗号。
func TestResidentRealCLI(t *testing.T) {
	if os.Getenv("MCODE_TEST_REAL_CLAUDE") != "1" {
		t.Skip("设置 MCODE_TEST_REAL_CLAUDE=1 才跑真实 claude CLI 冒烟测试")
	}
	bin, err := exec.LookPath(DefaultBinary)
	if err != nil {
		t.Skipf("未找到 claude CLI: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	h := New(Options{Binary: bin, IdleTTL: time.Minute})
	defer h.Close()

	turn := harness.Turn{
		ThreadID: residentTestThreadID,
		WorkDir:  t.TempDir(),
		Model:    "haiku",
		Mode:     harness.ModeAutoAcceptEdits,
		Prompt:   "Remember the secret word PINEAPPLE. Reply with exactly: OK",
	}

	var first []harness.Event
	if err := h.Run(ctx, turn, collectEvents(&first)); err != nil {
		t.Fatalf("第一轮 Run 返回错误: %v", err)
	}
	if !hasEvent(first, harness.EventDone) {
		t.Fatalf("第一轮应以 EventDone 收尾: %+v", first)
	}
	session := sessionFrom(first)
	if session == "" {
		t.Fatalf("第一轮应上报会话 id: %+v", first)
	}
	firstPIDs := h.ProcessRootPIDs(residentTestThreadID)
	if len(firstPIDs) == 0 {
		t.Fatalf("第一轮结束后应留下常驻进程 PID")
	}

	turn.SessionID = session
	turn.Prompt = "What was the secret word? Reply with the word only."
	var second []harness.Event
	if err := h.Run(ctx, turn, collectEvents(&second)); err != nil {
		t.Fatalf("第二轮 Run 返回错误: %v", err)
	}
	if !hasEvent(second, harness.EventDone) {
		t.Fatalf("第二轮应以 EventDone 收尾: %+v", second)
	}

	secondPIDs := h.ProcessRootPIDs(residentTestThreadID)
	if len(firstPIDs) != 1 || len(secondPIDs) != 1 || firstPIDs[0] != secondPIDs[0] {
		t.Fatalf("两轮之间应复用同一个进程，实际第一轮 %v、第二轮 %v", firstPIDs, secondPIDs)
	}
	if text := eventTexts(second); !strings.Contains(strings.ToUpper(text), "PINEAPPLE") {
		t.Fatalf("第二轮应回忆起暗号 PINEAPPLE，实际回复: %q", text)
	}
}

// readFakeFrames 读出假 CLI 记录的控制帧，每行 "subtype\t请求体"。
func readFakeFrames(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("读取控制帧记录失败: %v", err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// countFrames 统计指定 subtype 的控制帧数量。
func countFrames(frames []string, subtype string) int {
	n := 0
	for _, f := range frames {
		if strings.HasPrefix(f, subtype+"\t") {
			n++
		}
	}
	return n
}

// hotTurn 构造可热切换的轮次：模型与思考强度都显式给出。
func hotTurn(mode, logPath, framesPath, workDir string) harness.Turn {
	turn := residentTurn(mode, logPath, workDir)
	turn.Env = append(turn.Env, fakeFramesEnv+"="+framesPath)
	turn.Model = "model-a"
	turn.Effort = "low"
	return turn
}

func TestResident_HotSwitchesSettingsWithoutRestart(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	dir := t.TempDir()
	logPath := filepath.Join(dir, "starts.log")
	framesPath := filepath.Join(dir, "frames.log")
	h := New(Options{Binary: os.Args[0], IdleTTL: time.Minute})
	defer h.Close()

	turn := hotTurn("resident:"+fixture, logPath, framesPath, t.TempDir())
	if err := h.Run(context.Background(), turn, func(harness.Event) {}); err != nil {
		t.Fatalf("第一轮 Run 返回错误: %v", err)
	}

	// 模型、思考强度、权限模式全变：进程不重建，靠控制帧热切换。
	turn.SessionID = "sess-123"
	turn.Model = "model-b"
	turn.Effort = "high"
	turn.Mode = harness.ModeReadOnly
	var second []harness.Event
	if err := h.Run(context.Background(), turn, collectEvents(&second)); err != nil {
		t.Fatalf("第二轮 Run 返回错误: %v", err)
	}
	if !hasEvent(second, harness.EventDone) {
		t.Fatalf("第二轮应以 EventDone 收尾: %+v", second)
	}
	if starts := fakeStartArgvs(t, logPath); len(starts) != 1 {
		t.Fatalf("热切换不该重建进程，实际启动 %d 次: %v", len(starts), starts)
	}
	frames := readFakeFrames(t, framesPath)
	if countFrames(frames, "set_model") != 1 || !strings.Contains(strings.Join(frames, "\n"), `"model":"model-b"`) {
		t.Errorf("应发一次 set_model(model-b): %v", frames)
	}
	if countFrames(frames, "apply_flag_settings") != 1 || !strings.Contains(strings.Join(frames, "\n"), `"effortLevel":"high"`) {
		t.Errorf("应发一次 apply_flag_settings(effortLevel=high): %v", frames)
	}
	if countFrames(frames, "set_permission_mode") != 1 || !strings.Contains(strings.Join(frames, "\n"), `"mode":"plan"`) {
		t.Errorf("应发一次 set_permission_mode(plan): %v", frames)
	}

	// 第三轮设置不变：模型与强度不再重发，权限模式每轮仍会确认一次。
	if err := h.Run(context.Background(), turn, func(harness.Event) {}); err != nil {
		t.Fatalf("第三轮 Run 返回错误: %v", err)
	}
	frames = readFakeFrames(t, framesPath)
	if countFrames(frames, "set_model") != 1 || countFrames(frames, "apply_flag_settings") != 1 {
		t.Errorf("设置没变时不该重发 set_model/apply_flag_settings: %v", frames)
	}
	if countFrames(frames, "set_permission_mode") != 2 {
		t.Errorf("权限模式应每轮确认一次: %v", frames)
	}
	stats := h.Stats()
	if stats.Spawns != 1 || stats.Reuses != 2 || stats.HotSwitches != 1 {
		t.Errorf("计数不符: %+v", stats)
	}
}

func TestResident_HotSwitchRejectedRebuilds(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	dir := t.TempDir()
	logPath := filepath.Join(dir, "starts.log")
	framesPath := filepath.Join(dir, "frames.log")
	h := New(Options{Binary: os.Args[0], IdleTTL: time.Minute})
	defer h.Close()

	turn := hotTurn("resident_reject_control:"+fixture, logPath, framesPath, t.TempDir())
	if err := h.Run(context.Background(), turn, func(harness.Event) {}); err != nil {
		t.Fatalf("第一轮 Run 返回错误: %v", err)
	}

	// CLI 拒绝热切换：杀掉进程，内部重试用本轮参数 --resume 重建，上层看不到错误。
	turn.SessionID = "sess-123"
	turn.Model = "model-b"
	var second []harness.Event
	if err := h.Run(context.Background(), turn, collectEvents(&second)); err != nil {
		t.Fatalf("热切换失败后应自动重建成功，实际错误: %v", err)
	}
	if !hasEvent(second, harness.EventDone) || hasEvent(second, harness.EventError) {
		t.Fatalf("重建后第二轮应正常收尾且不报错: %+v", second)
	}
	starts := fakeStartArgvs(t, logPath)
	if len(starts) != 2 {
		t.Fatalf("应重建一次进程，实际启动 %d 次: %v", len(starts), starts)
	}
	if !strings.Contains(starts[1], "--resume sess-123") || !strings.Contains(starts[1], "--model model-b") {
		t.Errorf("重建的进程应带 --resume 与新模型: %s", starts[1])
	}
	if got := h.Stats().Rebuilds["apply-failed"]; got != 1 {
		t.Errorf("应记录一次 apply-failed 重建，实际 %d", got)
	}
}

func TestResident_RebuildReasonsAndRecycle(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	logPath := filepath.Join(t.TempDir(), "starts.log")
	var logged []string
	var logMu sync.Mutex
	h := New(Options{Binary: os.Args[0], IdleTTL: time.Minute, MaxTurns: 2, Logf: func(format string, args ...any) {
		logMu.Lock()
		logged = append(logged, fmt.Sprintf(format, args...))
		logMu.Unlock()
	}})
	defer h.Close()

	turn := residentTurn("resident:"+fixture, logPath, t.TempDir())
	run := func() {
		t.Helper()
		if err := h.Run(context.Background(), turn, func(harness.Event) {}); err != nil {
			t.Fatalf("Run 返回错误: %v", err)
		}
	}
	run() // 第 1 轮：新起
	turn.SessionID = "sess-123"
	run() // 第 2 轮：复用（进程已服务 2 轮，达到 MaxTurns）
	run() // 第 3 轮：到期，用 --resume 重建
	if starts := fakeStartArgvs(t, logPath); len(starts) != 2 || !strings.Contains(starts[1], "--resume sess-123") {
		t.Fatalf("到达 MaxTurns 后应用 --resume 重建，启动记录: %v", starts)
	}
	turn.SessionID = "sess-other"
	run() // 第 4 轮：会话对不上
	stats := h.Stats()
	if stats.Rebuilds["recycled"] != 1 || stats.Rebuilds["session-mismatch"] != 1 {
		t.Errorf("重建原因计数不符: %+v", stats)
	}
	logMu.Lock()
	joined := strings.Join(logged, "\n")
	logMu.Unlock()
	if !strings.Contains(joined, "reason=recycled") || !strings.Contains(joined, "reason=session-mismatch") {
		t.Errorf("Logf 应记录重建原因: %s", joined)
	}
}

func TestResident_ExitWithStderrIsNotRetried(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	logPath := filepath.Join(t.TempDir(), "starts.log")
	h := New(Options{Binary: os.Args[0], IdleTTL: time.Minute})
	defer h.Close()

	// 启动即带着 stderr 报错退出（鉴权失败之类）：重试也不会好，只启动一次并上报错误。
	turn := residentTurn("resident_fail_stderr:"+fixture, logPath, t.TempDir())
	var events []harness.Event
	err := h.Run(context.Background(), turn, collectEvents(&events))
	if err == nil {
		t.Fatalf("进程带 stderr 退出时 Run 应返回错误")
	}
	if starts := fakeStartArgvs(t, logPath); len(starts) != 1 {
		t.Fatalf("带 stderr 的失败不该重试，实际启动 %d 次: %v", len(starts), starts)
	}
	var reported string
	for _, e := range events {
		if e.Type == harness.EventError {
			reported = e.Err
		}
	}
	if !strings.Contains(reported, "fatal startup error") {
		t.Errorf("上报的错误应带 stderr 内容: %q", reported)
	}
}

// steerWhenOpen 在 Run 进行中反复尝试 Steer，直到该轮进入可注入状态；返回最后一次错误。
func steerWhenOpen(h *Harness, sessionID, text string) error {
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := h.Steer(context.Background(), sessionID, text)
		if err == nil || !errors.Is(err, harness.ErrNotRunning) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// runWithSteer 跑一轮，并在收到首个 system/init 对应的会话事件后注入一条 steer。
func runWithSteer(t *testing.T, h *Harness, turn harness.Turn) ([]harness.Event, error) {
	t.Helper()
	var (
		mu       sync.Mutex
		events   []harness.Event
		steerErr = make(chan error, 1)
		once     sync.Once
	)
	err := h.Run(context.Background(), turn, func(e harness.Event) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
		if e.Type == harness.EventSession {
			once.Do(func() { go func() { steerErr <- steerWhenOpen(h, e.SessionID, "补充要求") }() })
		}
	})
	select {
	case sErr := <-steerErr:
		if sErr != nil {
			t.Fatalf("Steer 返回错误: %v", sErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("Steer 没有返回")
	}
	mu.Lock()
	defer mu.Unlock()
	return append([]harness.Event(nil), events...), err
}

func countEvents(events []harness.Event, typ harness.EventType) int {
	n := 0
	for _, e := range events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func TestResident_SteerMergedIntoCurrentTurn(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	logPath := filepath.Join(t.TempDir(), "starts.log")
	h := New(Options{Binary: os.Args[0], IdleTTL: time.Minute})
	defer h.Close()

	turn := residentTurn("resident_steer_merge:"+fixture, logPath, t.TempDir())
	events, err := runWithSteer(t, h, turn)
	if err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}
	// steer 并入当前轮：只有一个 result，只报一次完成。
	if n := countEvents(events, harness.EventDone); n != 1 {
		t.Fatalf("并入当前轮时应只有一个 EventDone，实际 %d: %+v", n, events)
	}
	if len(h.ProcessRootPIDs(residentTestThreadID)) == 0 {
		t.Errorf("steer 被消费后进程应继续驻留")
	}
	if got := h.Stats().Steers; got != 1 {
		t.Errorf("Steers 计数应为 1，实际 %d", got)
	}
}

func TestResident_SteerQueuedChainsTwoResults(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	logPath := filepath.Join(t.TempDir(), "starts.log")
	h := New(Options{Binary: os.Args[0], IdleTTL: time.Minute})
	defer h.Close()

	turn := residentTurn("resident_steer_queue:"+fixture, logPath, t.TempDir())
	events, err := runWithSteer(t, h, turn)
	if err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}
	// steer 排成了下一轮：两个 result 串成同一轮，只在最后一个上报完成，两轮用量都要记。
	if n := countEvents(events, harness.EventDone); n != 1 {
		t.Fatalf("串联两个 result 时应只有一个 EventDone，实际 %d: %+v", n, events)
	}
	if n := countEvents(events, harness.EventUsage); n != 2 {
		t.Errorf("两个 result 的用量都应上报，实际 %d", n)
	}
	if got := strings.Count(eventTexts(events), "Hello world!"); got != 2 {
		t.Errorf("两轮的回复文本都应上报，实际 %d 次: %q", got, eventTexts(events))
	}
	if len(h.ProcessRootPIDs(residentTestThreadID)) == 0 {
		t.Errorf("两轮都收尾后进程应继续驻留")
	}
	if starts := fakeStartArgvs(t, logPath); len(starts) != 1 {
		t.Errorf("steer 不该换进程，实际启动 %d 次", len(starts))
	}
}

func TestResident_SteerWithoutRunningTurn(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	logPath := filepath.Join(t.TempDir(), "starts.log")
	h := New(Options{Binary: os.Args[0], IdleTTL: time.Minute})
	defer h.Close()

	if err := h.Steer(context.Background(), "", "x"); !errors.Is(err, harness.ErrNotRunning) {
		t.Errorf("空会话 id 应返回 ErrNotRunning，实际 %v", err)
	}
	turn := residentTurn("resident:"+fixture, logPath, t.TempDir())
	if err := h.Run(context.Background(), turn, func(harness.Event) {}); err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}
	// 一轮已经收尾：进程空闲，不接受 steer，调用方应退回排队。
	if err := h.Steer(context.Background(), "sess-123", "x"); !errors.Is(err, harness.ErrNotRunning) {
		t.Errorf("轮次结束后 Steer 应返回 ErrNotRunning，实际 %v", err)
	}
}

func TestResident_EndOrChain(t *testing.T) {
	p := &residentProc{busy: true, steerOpen: true, pendingSteers: []string{"u2"}}
	if !p.endOrChain(false) {
		t.Fatalf("有排队中的 steer 且 result 不是错误时应继续等下一个 result")
	}
	p.pendingSteers = nil
	if p.endOrChain(false) || p.steerOpen {
		t.Fatalf("没有排队 steer 时应收尾并关闭入口")
	}
	p = &residentProc{busy: true, steerOpen: true, pendingSteers: []string{"u2"}}
	if p.endOrChain(true) {
		t.Fatalf("result 是错误时不再等待下一个 result")
	}
	if !p.takeOrphan() {
		t.Fatalf("错误收尾时仍有排队 steer，进程应被标记为不可留")
	}
}

// realClaudeHarness 为真实 CLI 冒烟准备 harness 与带超时的 ctx（默认跳过）。
func realClaudeHarness(t *testing.T) (*Harness, context.Context) {
	t.Helper()
	if os.Getenv("MCODE_TEST_REAL_CLAUDE") != "1" {
		t.Skip("设置 MCODE_TEST_REAL_CLAUDE=1 才跑真实 claude CLI 冒烟测试")
	}
	bin, err := exec.LookPath(DefaultBinary)
	if err != nil {
		t.Skipf("未找到 claude CLI: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	t.Cleanup(cancel)
	h := New(Options{Binary: bin, IdleTTL: time.Minute})
	t.Cleanup(func() { _ = h.Close() })
	return h, ctx
}

// TestResidentRealCLIHotSwitchAndResume 用真实 CLI 验证：模型、思考强度、权限模式热切换不换进程；
// 进程被外部杀掉后用 --resume 重建，会话 id 不变、上下文还在。
func TestResidentRealCLIHotSwitchAndResume(t *testing.T) {
	h, ctx := realClaudeHarness(t)

	turn := harness.Turn{
		ThreadID: residentTestThreadID,
		WorkDir:  t.TempDir(),
		Model:    "haiku",
		Effort:   "low",
		Mode:     harness.ModeAutoAcceptEdits,
		Prompt:   "Remember the secret word PINEAPPLE. Reply with exactly: OK",
	}
	var first []harness.Event
	if err := h.Run(ctx, turn, collectEvents(&first)); err != nil {
		t.Fatalf("第一轮 Run 返回错误: %v", err)
	}
	session := sessionFrom(first)
	if session == "" || !hasEvent(first, harness.EventDone) {
		t.Fatalf("第一轮应上报会话并收尾: %+v", first)
	}
	firstPIDs := h.ProcessRootPIDs(residentTestThreadID)

	// 热切换：换模型、换强度、切到只读（plan）模式，进程应保持不变。
	turn.SessionID = session
	turn.Model = "sonnet"
	turn.Effort = "medium"
	turn.Mode = harness.ModeReadOnly
	turn.Prompt = "What was the secret word? Reply with the word only."
	var second []harness.Event
	if err := h.Run(ctx, turn, collectEvents(&second)); err != nil {
		t.Fatalf("第二轮 Run 返回错误: %v", err)
	}
	if hasEvent(second, harness.EventError) || !hasEvent(second, harness.EventDone) {
		t.Fatalf("热切换后的第二轮应正常收尾: %+v", second)
	}
	if pids := h.ProcessRootPIDs(residentTestThreadID); len(pids) != 1 || len(firstPIDs) != 1 || pids[0] != firstPIDs[0] {
		t.Fatalf("热切换不该换进程: 第一轮 %v、第二轮 %v", firstPIDs, pids)
	}
	if got := h.Stats().HotSwitches; got != 1 {
		t.Errorf("HotSwitches 应为 1，实际 %d", got)
	}
	if !strings.Contains(strings.ToUpper(eventTexts(second)), "PINEAPPLE") {
		t.Errorf("热切换后应仍记得暗号: %q", eventTexts(second))
	}

	// 外部杀掉进程：下一轮用 --resume 重建，会话 id 应保持不变。
	if err := syscall.Kill(firstPIDs[0], syscall.SIGKILL); err != nil {
		t.Fatalf("杀进程失败: %v", err)
	}
	waitFor(t, 5*time.Second, "进程被杀后应从 PID 表消失", func() bool {
		return len(h.ProcessRootPIDs(residentTestThreadID)) == 0
	})
	turn.Mode = harness.ModeAutoAcceptEdits
	turn.Prompt = "Once more: what was the secret word? Reply with the word only."
	var third []harness.Event
	if err := h.Run(ctx, turn, collectEvents(&third)); err != nil {
		t.Fatalf("重建后的第三轮 Run 返回错误: %v", err)
	}
	if got := sessionFrom(third); got != session {
		t.Errorf("--resume 重建后会话 id 应不变: 原 %q、现 %q", session, got)
	}
	if !strings.Contains(strings.ToUpper(eventTexts(third)), "PINEAPPLE") {
		t.Errorf("重建后应仍记得暗号: %q", eventTexts(third))
	}
	if pids := h.ProcessRootPIDs(residentTestThreadID); len(pids) != 1 || pids[0] == firstPIDs[0] {
		t.Errorf("重建后应是新进程: 旧 %v、新 %v", firstPIDs, pids)
	}
}

// TestResidentRealCLISteerDuringTool 用真实 CLI 验证：工具执行期间 steer 并入当前轮，
// 只收到一次完成，回复里带上了插话的要求，进程继续驻留。
func TestResidentRealCLISteerDuringTool(t *testing.T) {
	h, ctx := realClaudeHarness(t)

	turn := harness.Turn{
		ThreadID: residentTestThreadID,
		WorkDir:  t.TempDir(),
		Model:    "haiku",
		Mode:     harness.ModeFullAccess,
		Prompt:   "Run this exact bash command and then tell me its output: sleep 10; echo STEERDONE",
	}
	var (
		mu     sync.Mutex
		events []harness.Event
		once   sync.Once
		steer  = make(chan error, 1)
	)
	err := h.Run(ctx, turn, func(e harness.Event) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
		if e.Type == harness.EventToolStart {
			once.Do(func() {
				go func() {
					steer <- steerWhenOpen(h, sessionFromLocked(&mu, &events), "Also end your reply with the word BANANA.")
				}()
			})
		}
	})
	if err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}
	if sErr := <-steer; sErr != nil {
		t.Fatalf("Steer 返回错误: %v", sErr)
	}
	mu.Lock()
	defer mu.Unlock()
	if n := countEvents(events, harness.EventDone); n != 1 {
		t.Fatalf("应只有一个 EventDone，实际 %d: %+v", n, events)
	}
	if !strings.Contains(strings.ToUpper(eventTexts(events)), "BANANA") {
		t.Errorf("回复应带上 steer 的要求 BANANA: %q", eventTexts(events))
	}
	if len(h.ProcessRootPIDs(residentTestThreadID)) == 0 {
		t.Errorf("steer 之后进程应继续驻留")
	}
}

// sessionFromLocked 在加锁下读取事件序列里的会话 id。
func sessionFromLocked(mu *sync.Mutex, events *[]harness.Event) string {
	mu.Lock()
	defer mu.Unlock()
	return sessionFrom(*events)
}

// bufWriteCloser 把 bytes.Buffer 包成 io.WriteCloser，供直接验证写到 stdin 的帧。
type bufWriteCloser struct{ *bytes.Buffer }

func (bufWriteCloser) Close() error { return nil }

// decodeUserFrame 解出缓冲区里唯一一帧 user 消息。
func decodeUserFrame(t *testing.T, buf *bytes.Buffer) stdinUserMessage {
	t.Helper()
	var frame struct {
		Type    string           `json:"type"`
		Message stdinUserMessage `json:"message"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &frame); err != nil {
		t.Fatalf("解析 user 帧失败: %v（帧：%s）", err, buf.String())
	}
	if frame.Type != "user" || frame.Message.Role != "user" {
		t.Fatalf("帧形状不对: %+v", frame)
	}
	return frame.Message
}

// TestResident_UserFrameCarriesImageBlocks 验证常驻路径的 user 帧：带图片时 content 是
// text+image 块数组，无图片的轮次与 steer 帧仍是纯文本字符串。
func TestResident_UserFrameCarriesImageBlocks(t *testing.T) {
	imgPath := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(imgPath, []byte{0x89, 'P', 'N', 'G'}, 0o600); err != nil {
		t.Fatalf("写测试图片失败: %v", err)
	}
	newProc := func() (*residentProc, *bytes.Buffer) {
		buf := &bytes.Buffer{}
		return &residentProc{stdin: bufWriteCloser{buf}}, buf
	}

	t.Run("带图片的轮次是text+image块", func(t *testing.T) {
		p, buf := newProc()
		if err := p.sendUserFrame(buildUserContent(harness.Turn{Prompt: "看图", Images: []string{imgPath}})); err != nil {
			t.Fatalf("sendUserFrame 返回错误: %v", err)
		}
		msg := decodeUserFrame(t, buf)
		blocks, ok := msg.Content.([]any)
		if !ok || len(blocks) != 2 {
			t.Fatalf("content 应是 text+image 两块，实际 %s", buf.String())
		}
		first, _ := blocks[0].(map[string]any)
		if first["type"] != "text" || first["text"] != "看图" {
			t.Errorf("首个块应是 prompt 文本，实际 %#v", blocks[0])
		}
		second, _ := blocks[1].(map[string]any)
		source, _ := second["source"].(map[string]any)
		if second["type"] != "image" || source["type"] != "base64" || source["media_type"] != "image/png" {
			t.Errorf("图片块不对: %#v", second)
		}
		if source["data"] != "iVBORw==" {
			t.Errorf("图片块应带原始字节的 base64，实际 %v", source["data"])
		}
	})

	t.Run("无图片的轮次仍是字符串", func(t *testing.T) {
		p, buf := newProc()
		if err := p.sendUserFrame(buildUserContent(harness.Turn{Prompt: "这一轮的问题"})); err != nil {
			t.Fatalf("sendUserFrame 返回错误: %v", err)
		}
		msg := decodeUserFrame(t, buf)
		if got, ok := msg.Content.(string); !ok || got != "这一轮的问题" {
			t.Fatalf("无图片时 content 应是字符串，实际 %s", buf.String())
		}
		// 原始帧也得是改造前的形状（content 为 JSON 字符串）。
		if !bytes.Contains(buf.Bytes(), []byte(`"content":"这一轮的问题"`)) {
			t.Errorf("无图片的帧应保持原有形状: %s", buf.String())
		}
	})

	t.Run("steer帧仍是纯文本", func(t *testing.T) {
		p, buf := newProc()
		if err := p.sendUserFrameWithUUID("插一句", "uuid-steer"); err != nil {
			t.Fatalf("sendUserFrameWithUUID 返回错误: %v", err)
		}
		msg := decodeUserFrame(t, buf)
		if got, ok := msg.Content.(string); !ok || got != "插一句" {
			t.Fatalf("steer 帧的 content 应是纯文本，实际 %s", buf.String())
		}
		if !bytes.Contains(buf.Bytes(), []byte(`"uuid":"uuid-steer"`)) {
			t.Errorf("steer 帧应带 uuid: %s", buf.String())
		}
	})
}

// TestResident_ImagesDoNotChangeFingerprint 验证图片只进 user 帧、不进复用指纹：
// 同一线程先跑带图片的一轮、再跑无图片的一轮，应复用同一个常驻进程。
func TestResident_ImagesDoNotChangeFingerprint(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	logPath := filepath.Join(t.TempDir(), "starts.log")
	imgPath := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(imgPath, []byte{0x89, 'P', 'N', 'G'}, 0o600); err != nil {
		t.Fatalf("写测试图片失败: %v", err)
	}

	h := New(Options{Binary: os.Args[0], IdleTTL: time.Minute})
	defer h.Close()
	base := residentTurn("resident:"+fixture, logPath, t.TempDir())

	withImage := base
	withImage.Images = []string{imgPath}
	var first []harness.Event
	if err := h.Run(context.Background(), withImage, collectEvents(&first)); err != nil {
		t.Fatalf("带图片的一轮返回错误: %v", err)
	}
	// 复用还要求会话对得上：第二轮必须带着首轮上报的会话 id，否则会因会话不匹配重建进程
	// （见 TestResident_RebuildsOnSessionMismatch），失败信息就会误指到指纹上。
	base.SessionID = sessionFrom(first)
	if base.SessionID == "" {
		t.Fatalf("首轮应上报会话 id，否则本轮测不出复用: %+v", first)
	}
	if err := h.Run(context.Background(), base, func(harness.Event) {}); err != nil {
		t.Fatalf("无图片的一轮返回错误: %v", err)
	}

	starts := fakeStartArgvs(t, logPath)
	if len(starts) != 1 {
		t.Fatalf("图片不该进入复用指纹，应只有一次启动，实际 %d 次: %v", len(starts), starts)
	}
}

// appendFakeLine 把一行追加进 path：假 CLI 用它记录收到的用户帧。
func appendFakeLine(path, line string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = f.WriteString(line + "\n")
	_ = f.Close()
}

// fakeAssistantFrame 按真实形状拼一条 assistant 正文帧。
func fakeAssistantFrame(text string) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"text","text":%q}]}}`, text)
}

// fakeBackgroundRun 拼出后台任务结束后的整段帧：真实 CLI 依次吐 task_updated、task_notification、
// background_tasks_changed、init、status，然后才是 assistant 正文与 result，中间没有 user 回显。
func fakeBackgroundRun(texts ...string) string {
	var b strings.Builder
	b.WriteString(`{"type":"system","subtype":"task_updated","task_id":"task-1","status":"completed"}` + "\n")
	b.WriteString(`{"type":"system","subtype":"task_notification","task_id":"task-1","tool_use_id":"toolu_bg_1","status":"completed"}` + "\n")
	b.WriteString(`{"type":"system","subtype":"background_tasks_changed","tasks":[]}` + "\n")
	b.WriteString(`{"type":"system","subtype":"init","session_id":"sess-123"}` + "\n")
	b.WriteString(`{"type":"system","subtype":"status","status":"running"}` + "\n")
	for _, text := range texts {
		b.WriteString(fakeAssistantFrame(text) + "\n")
		b.WriteString(`{"type":"result","subtype":"success","is_error":false,"result":"续跑完成","stop_reason":"end_turn"}` + "\n")
	}
	return b.String()
}

// runBackgroundTasks 模拟后台任务结束后的自发续跑：等一会儿再一次性吐出整段帧。
// 一次写完是为了避免与主协程回放 fixture 交错（这一段之间没有用户帧介入）。
func (st *fakeResidentState) runBackgroundTasks() {
	texts := []string{"后台任务完成后的续跑"}
	if st.name == "resident_bg_double" {
		texts = []string{"第一段续跑", "第二段续跑"}
	}
	time.Sleep(residentBgDelay)
	_, _ = os.Stdout.WriteString(fakeBackgroundRun(texts...))
}

// residentState 取线程常驻进程的内部状态快照，供用例等待自发输出就绪（同包测试才看得到这些字段）：
// pending 表示没有轮次接收的自发输出已经开始，done 表示它的 result 已经到过，background 是未结束的后台任务数。
func residentState(h *Harness, threadID string) (pending, done bool, background int, ok bool) {
	h.mu.Lock()
	p := h.residents[threadID]
	h.mu.Unlock()
	if p == nil {
		return false, false, 0, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.unsolicited, p.unsolicitedDone, p.bgTasks, true
}

// attachOf 返回同一线程上的接续轮：不写用户帧，只把进程上已经开始的那段输出读完。
func attachOf(turn harness.Turn) harness.Turn {
	turn.Attach = true
	turn.Prompt = ""
	return turn
}

// countUserFrames 数假 CLI 收到的用户帧条数；没有记录文件时算 0（Attach 轮本来就不写用户帧）。
func countUserFrames(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("读取用户帧记录失败: %v", err)
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

// TestResident_StartupInitDoesNotStartUnsolicitedTurn 覆盖启动期的 system/init：它在 attach 之前到达，
// 但进程刚起、这一轮正要开始，不是自发轮次。若把它当真，先开一个 Attach 轮并置 unsolicited，
// 用户这一轮就会进排空阶段等一个不会到来的 result，直到超时杀进程。
func TestResident_StartupInitDoesNotStartUnsolicitedTurn(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	logPath := filepath.Join(t.TempDir(), "starts.log")
	// 超时放宽到 20s 但不开到默认的 90s：这个用例真要能失败，也是靠超时失败，得让它尽快结束。
	h := New(Options{Binary: os.Args[0], IdleTTL: time.Minute, FirstOutputTimeout: 20 * time.Second, StallTimeout: 20 * time.Second})
	defer h.Close()

	calls := make(chan string, 8)
	h.SetUnsolicitedTurnHandler(func(threadID string) { calls <- threadID })

	turn := residentTurn("resident_startup_init:"+fixture, logPath, t.TempDir())
	var first []harness.Event
	if err := h.Run(context.Background(), turn, collectEvents(&first)); err != nil {
		t.Fatalf("启动期发过 system/init 的进程首轮 Run 返回错误: %v", err)
	}
	if text := eventTexts(first); !strings.Contains(text, "Hello world!") {
		t.Fatalf("首轮应拿到正文，实际 %q", text)
	}
	if got := countEvents(first, harness.EventDone); got != 1 {
		t.Fatalf("首轮应只上报一个 EventDone，实际 %d 个", got)
	}
	if pending, _, _, ok := residentState(h, residentTestThreadID); ok && pending {
		t.Fatal("启动期的 system/init 不该被当成自发输出缓存下来")
	}
	select {
	case threadID := <-calls:
		t.Fatalf("启动期的 system/init 不是自发轮次，不该触发回调，收到 %q", threadID)
	default:
	}

	// 进程留着复用：第二轮不该重新起进程。
	turn.SessionID = sessionFrom(first)
	var next []harness.Event
	if err := h.Run(context.Background(), turn, collectEvents(&next)); err != nil {
		t.Fatalf("第二轮 Run 返回错误: %v", err)
	}
	if text := eventTexts(next); !strings.Contains(text, "Hello world!") {
		t.Fatalf("第二轮应拿到正文，实际 %q", text)
	}
	if got := countEvents(next, harness.EventDone); got != 1 {
		t.Fatalf("第二轮应只上报一个 EventDone，实际 %d 个", got)
	}
	if starts := fakeStartArgvs(t, logPath); len(starts) != 1 {
		t.Fatalf("第二轮应复用进程，实际启动 %d 次: %v", len(starts), starts)
	}
	if stats := h.Stats(); stats.Spawns != 1 || stats.Reuses != 1 {
		t.Fatalf("应起 1 次进程、复用 1 次，实际 Spawns=%d Reuses=%d", stats.Spawns, stats.Reuses)
	}
	select {
	case threadID := <-calls:
		t.Fatalf("整段用例里启动期 init 都不该触发回调，收到 %q", threadID)
	default:
	}
}

// TestResident_ImmediateUnsolicitedAfterResult 覆盖 result 后立即到来的自发输出。
func TestResident_ImmediateUnsolicitedAfterResult(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	logPath := filepath.Join(t.TempDir(), "starts.log")
	usersPath := filepath.Join(t.TempDir(), "users.log")
	h := New(Options{Binary: os.Args[0], IdleTTL: time.Minute})
	defer h.Close()
	calls := make(chan string, 8)
	h.SetUnsolicitedTurnHandler(func(threadID string) { calls <- threadID })
	if h.HasUnsolicitedOutput(residentTestThreadID) {
		t.Fatal("不存在进程时不应有自发输出")
	}
	turn := residentTurn("resident_bg_immediate:"+fixture, logPath, t.TempDir())
	turn.Env = append(turn.Env, fakeUsersEnv+"="+usersPath)
	var first []harness.Event
	err := h.Run(context.Background(), turn, func(e harness.Event) {
		first = append(first, e)
		if e.Type == harness.EventDone {
			// 等待续跑帧进入旧 sink，确定覆盖 result 与 detach 之间的窗口。
			waitFor(t, 10*time.Second, "续跑帧进入旧 sink", func() bool {
				h.mu.Lock()
				p := h.residents[residentTestThreadID]
				h.mu.Unlock()
				if p == nil {
					return false
				}
				p.mu.Lock()
				defer p.mu.Unlock()
				return p.sink != nil && len(p.sink.ch) >= 4
			})
		}
	})
	if err != nil || !strings.Contains(eventTexts(first), "Hello world!") || countEvents(first, harness.EventDone) != 1 {
		t.Fatalf("首轮结果异常: err=%v events=%+v", err, first)
	}
	select {
	case threadID := <-calls:
		if threadID != residentTestThreadID {
			t.Fatalf("回调线程错误: %q", threadID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("没有收到自发输出回调")
	}
	if !h.HasUnsolicitedOutput(residentTestThreadID) {
		t.Fatal("续跑输出应等待 attach")
	}
	h.mu.Lock()
	p := h.residents[residentTestThreadID]
	p.mu.Lock()
	backlog := append([][]byte(nil), p.backlog...)
	p.mu.Unlock()
	h.mu.Unlock()
	if len(backlog) != 4 || !bytes.Contains(backlog[0], []byte(`"task_notification"`)) || !bytes.Contains(backlog[1], []byte(`"init"`)) || !bytes.Contains(backlog[2], []byte("紧随结果的续跑")) || !bytes.Contains(backlog[3], []byte(`"result"`)) {
		t.Fatalf("续跑帧有丢失或乱序: %q", backlog)
	}
	var attached []harness.Event
	if err := h.Run(context.Background(), attachOf(turn), collectEvents(&attached)); err != nil {
		t.Fatalf("attach 返回错误: %v", err)
	}
	if text := eventTexts(attached); text != "紧随结果的续跑" {
		t.Fatalf("续跑正文不完整: %q", text)
	}
	if countEvents(attached, harness.EventDone) != 1 || countUserFrames(t, usersPath) != 1 {
		t.Fatalf("attach 的结束事件或用户帧数量异常: %+v", attached)
	}
	if h.HasUnsolicitedOutput(residentTestThreadID) {
		t.Fatal("attach 排空后不应有自发输出")
	}
	select {
	case threadID := <-calls:
		t.Fatalf("自发输出重复通知: %q", threadID)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestResident_UnsolicitedTurnNotifiesOnce 覆盖自发轮次的通知：后台任务结束后 CLI 自己跑的这一段
// 只通知一次（task_notification 之后的 init 不该再触发），回调带上线程 id，且不带用户帧。
func TestResident_UnsolicitedTurnNotifiesOnce(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	logPath := filepath.Join(t.TempDir(), "starts.log")
	usersPath := filepath.Join(t.TempDir(), "users.log")
	h := New(Options{Binary: os.Args[0], IdleTTL: time.Minute})
	defer h.Close()

	calls := make(chan string, 8)
	h.SetUnsolicitedTurnHandler(func(threadID string) { calls <- threadID })

	turn := residentTurn("resident_bg_notify:"+fixture, logPath, t.TempDir())
	turn.Env = append(turn.Env, fakeUsersEnv+"="+usersPath)
	if err := h.Run(context.Background(), turn, func(harness.Event) {}); err != nil {
		t.Fatalf("第一轮 Run 返回错误: %v", err)
	}

	select {
	case threadID := <-calls:
		if threadID != residentTestThreadID {
			t.Fatalf("回调应带上线程 id，实际 %q", threadID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("后台任务结束后应触发自发轮次回调")
	}
	waitFor(t, 10*time.Second, "自发输出的 result 到达", func() bool {
		_, done, background, ok := residentState(h, residentTestThreadID)
		return ok && done && background == 0
	})
	// 同一段自发输出里 init 跟在 notification 后面，不该再通知一次。
	select {
	case threadID := <-calls:
		t.Fatalf("同一段自发输出只该通知一次，又收到 %q", threadID)
	case <-time.After(200 * time.Millisecond):
	}
	if got := countUserFrames(t, usersPath); got != 1 {
		t.Errorf("自发输出不该带用户帧，用户帧应只有首轮那 1 条，实际 %d", got)
	}
}

// TestResident_AttachDrainsUnsolicitedTurn 覆盖 Attach：不发用户帧，接上已有进程把自发输出读完，
// 只上报一个 EventDone，进程留着给下一轮复用。
func TestResident_AttachDrainsUnsolicitedTurn(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	logPath := filepath.Join(t.TempDir(), "starts.log")
	usersPath := filepath.Join(t.TempDir(), "users.log")
	h := New(Options{Binary: os.Args[0], IdleTTL: time.Minute})
	defer h.Close()

	turn := residentTurn("resident_bg_notify:"+fixture, logPath, t.TempDir())
	turn.Env = append(turn.Env, fakeUsersEnv+"="+usersPath)
	attach := attachOf(turn)

	var mu sync.Mutex
	var attachEvents []harness.Event
	var attachErr error
	attached := make(chan struct{}, 1)
	h.SetUnsolicitedTurnHandler(func(string) {
		var events []harness.Event
		err := h.Run(context.Background(), attach, collectEvents(&events))
		mu.Lock()
		attachEvents, attachErr = events, err
		mu.Unlock()
		attached <- struct{}{}
	})

	var first []harness.Event
	if err := h.Run(context.Background(), turn, collectEvents(&first)); err != nil {
		t.Fatalf("第一轮 Run 返回错误: %v", err)
	}
	select {
	case <-attached:
	case <-time.After(10 * time.Second):
		t.Fatal("Attach 轮没有收尾")
	}

	mu.Lock()
	events, err := attachEvents, attachErr
	mu.Unlock()
	if err != nil {
		t.Fatalf("Attach 轮返回错误: %v", err)
	}
	if text := eventTexts(events); !strings.Contains(text, "后台任务完成后的续跑") {
		t.Fatalf("Attach 轮应收到自发输出的正文，实际 %q", text)
	}
	if got := countEvents(events, harness.EventDone); got != 1 {
		t.Fatalf("Attach 轮应只上报一个 EventDone，实际 %d 个", got)
	}
	if got := countUserFrames(t, usersPath); got != 1 {
		t.Fatalf("Attach 轮不该写用户帧，用户帧应只有首轮那 1 条，实际 %d", got)
	}

	// 进程留下复用：下一轮普通消息照常拿到回复，不重新起进程。
	turn.SessionID = sessionFrom(first)
	var next []harness.Event
	if err := h.Run(context.Background(), turn, collectEvents(&next)); err != nil {
		t.Fatalf("接续之后的普通轮返回错误: %v", err)
	}
	if text := eventTexts(next); !strings.Contains(text, "Hello world!") {
		t.Fatalf("普通轮应拿到 fixture 的回复，实际 %q", text)
	}
	if starts := fakeStartArgvs(t, logPath); len(starts) != 1 {
		t.Fatalf("Attach 轮应复用进程，实际启动 %d 次: %v", len(starts), starts)
	}

	// 没有待接的自发输出时 Attach 直接返回：不发事件，也不占用进程。
	var idle []harness.Event
	if err := h.Run(context.Background(), attach, collectEvents(&idle)); err != nil {
		t.Fatalf("没有待接输出时 Attach 应返回 nil，实际 %v", err)
	}
	if len(idle) != 0 {
		t.Fatalf("没有待接输出时 Attach 不该上报事件，实际 %+v", idle)
	}
}

// TestResident_UnsolicitedTurnWithoutHandlerKeepsOldBehavior 覆盖没注册回调时的旧行为：
// 自发输出没有人接（不会自己开轮），进程照旧复用，等下一轮普通消息才把这段输出并进来。
func TestResident_UnsolicitedTurnWithoutHandlerKeepsOldBehavior(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	logPath := filepath.Join(t.TempDir(), "starts.log")
	usersPath := filepath.Join(t.TempDir(), "users.log")
	h := New(Options{Binary: os.Args[0], IdleTTL: time.Minute})
	defer h.Close()

	turn := residentTurn("resident_bg_notify:"+fixture, logPath, t.TempDir())
	turn.Env = append(turn.Env, fakeUsersEnv+"="+usersPath)
	var first []harness.Event
	if err := h.Run(context.Background(), turn, collectEvents(&first)); err != nil {
		t.Fatalf("第一轮 Run 返回错误: %v", err)
	}
	// 没注册回调时不会自己开轮：进程上这段输出只能靠内部缓存，等下一轮再接。
	waitFor(t, 10*time.Second, "自发输出进入缓存", func() bool {
		_, done, _, ok := residentState(h, residentTestThreadID)
		return ok && done
	})
	if got := countUserFrames(t, usersPath); got != 1 {
		t.Fatalf("没有回调时不该自己开轮，用户帧应只有首轮那 1 条，实际 %d", got)
	}

	turn.SessionID = sessionFrom(first)
	var next []harness.Event
	if err := h.Run(context.Background(), turn, collectEvents(&next)); err != nil {
		t.Fatalf("下一轮普通消息返回错误: %v", err)
	}
	if text := eventTexts(next); !strings.Contains(text, "Hello world!") {
		t.Fatalf("普通轮应照常拿到回复，实际 %q", text)
	}
	if got := countEvents(next, harness.EventDone); got != 1 {
		t.Fatalf("普通轮应只上报一个 EventDone，实际 %d 个", got)
	}
	if starts := fakeStartArgvs(t, logPath); len(starts) != 1 {
		t.Fatalf("普通轮应复用进程，实际启动 %d 次: %v", len(starts), starts)
	}
}

// TestResident_NormalTurnDrainsUnsolicitedFirst 覆盖自发输出进行中来了普通消息：先把自发那段并进本轮，
// 再写用户帧，正文顺序是自发在前、用户轮回复在后，整轮只上报一个 EventDone。
func TestResident_NormalTurnDrainsUnsolicitedFirst(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	logPath := filepath.Join(t.TempDir(), "starts.log")
	usersPath := filepath.Join(t.TempDir(), "users.log")
	h := New(Options{Binary: os.Args[0], IdleTTL: time.Minute})
	defer h.Close()
	h.SetUnsolicitedTurnHandler(func(string) {})

	turn := residentTurn("resident_bg_notify:"+fixture, logPath, t.TempDir())
	turn.Env = append(turn.Env, fakeUsersEnv+"="+usersPath)
	var first []harness.Event
	if err := h.Run(context.Background(), turn, collectEvents(&first)); err != nil {
		t.Fatalf("第一轮 Run 返回错误: %v", err)
	}
	waitFor(t, 10*time.Second, "自发输出的 result 到达", func() bool {
		_, done, _, ok := residentState(h, residentTestThreadID)
		return ok && done
	})

	turn.SessionID = sessionFrom(first)
	var next []harness.Event
	if err := h.Run(context.Background(), turn, collectEvents(&next)); err != nil {
		t.Fatalf("插进自发输出的普通轮返回错误: %v", err)
	}
	text := eventTexts(next)
	background := strings.Index(text, "后台任务完成后的续跑")
	reply := strings.Index(text, "Hello world!")
	if background < 0 || reply < 0 {
		t.Fatalf("本轮应同时带上自发正文与用户轮回复，实际 %q", text)
	}
	if background > reply {
		t.Fatalf("自发输出应排在用户轮回复之前，实际 %q", text)
	}
	if got := countEvents(next, harness.EventDone); got != 1 {
		t.Fatalf("整轮应只上报一个 EventDone，实际 %d 个", got)
	}
	if got := countUserFrames(t, usersPath); got != 2 {
		t.Fatalf("两轮各写一条用户帧，实际 %d 条", got)
	}
}

// TestResident_AttachDrainsBacklogWithTwoResults 覆盖没有轮次接收时缓存了两段输出（两个 result）：
// Attach 把它们全部并入同一轮，只上报一个 EventDone。
func TestResident_AttachDrainsBacklogWithTwoResults(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	logPath := filepath.Join(t.TempDir(), "starts.log")
	usersPath := filepath.Join(t.TempDir(), "users.log")
	h := New(Options{Binary: os.Args[0], IdleTTL: time.Minute})
	defer h.Close()
	h.SetUnsolicitedTurnHandler(func(string) {})

	turn := residentTurn("resident_bg_double:"+fixture, logPath, t.TempDir())
	turn.Env = append(turn.Env, fakeUsersEnv+"="+usersPath)
	if err := h.Run(context.Background(), turn, func(harness.Event) {}); err != nil {
		t.Fatalf("第一轮 Run 返回错误: %v", err)
	}
	// 等两段都进缓存（done 由第二段的 result 置上），再一次性接住。
	waitFor(t, 10*time.Second, "两段自发输出都进入缓存", func() bool {
		_, done, background, ok := residentState(h, residentTestThreadID)
		return ok && done && background == 0
	})

	var events []harness.Event
	if err := h.Run(context.Background(), attachOf(turn), collectEvents(&events)); err != nil {
		t.Fatalf("Attach 轮返回错误: %v", err)
	}
	text := eventTexts(events)
	for _, want := range []string{"第一段续跑", "第二段续跑"} {
		if !strings.Contains(text, want) {
			t.Fatalf("缓存里的两段输出都应并入同一轮，缺少 %q，实际 %q", want, text)
		}
	}
	if got := countEvents(events, harness.EventDone); got != 1 {
		t.Fatalf("Attach 轮应只上报一个 EventDone，实际 %d 个", got)
	}
	if got := countUserFrames(t, usersPath); got != 1 {
		t.Fatalf("Attach 轮不该写用户帧，实际 %d 条", got)
	}
}

// TestResident_BackgroundTasksHoldResident 覆盖后台任务期间不回收：只看到 task_started 时，
// 短 IdleTTL 下进程也要留着；等 notification 到达并排空后才照常回收。
func TestResident_BackgroundTasksHoldResident(t *testing.T) {
	t.Run("只有task_started时不回收", func(t *testing.T) {
		fixture := fixtureAbs(t, "basic.jsonl")
		logPath := filepath.Join(t.TempDir(), "starts.log")
		h := New(Options{Binary: os.Args[0], IdleTTL: 60 * time.Millisecond})
		defer h.Close()

		turn := residentTurn("resident_bg_hold:"+fixture, logPath, t.TempDir())
		if err := h.Run(context.Background(), turn, func(harness.Event) {}); err != nil {
			t.Fatalf("第一轮 Run 返回错误: %v", err)
		}
		waitFor(t, 10*time.Second, "后台任务计数加一", func() bool {
			_, _, background, ok := residentState(h, residentTestThreadID)
			return ok && background == 1
		})
		time.Sleep(5 * h.opts.IdleTTL)
		if got := residentCount(h); got != 1 {
			t.Fatalf("后台任务没结束就不能回收进程，实际剩 %d 个", got)
		}
	})

	t.Run("排空后正常回收", func(t *testing.T) {
		fixture := fixtureAbs(t, "basic.jsonl")
		logPath := filepath.Join(t.TempDir(), "starts.log")
		h := New(Options{Binary: os.Args[0], IdleTTL: 60 * time.Millisecond})
		defer h.Close()

		attach := attachOf(residentTurn("resident_bg_notify:"+fixture, logPath, t.TempDir()))
		attached := make(chan error, 1)
		h.SetUnsolicitedTurnHandler(func(string) {
			attached <- h.Run(context.Background(), attach, func(harness.Event) {})
		})

		turn := residentTurn("resident_bg_notify:"+fixture, logPath, t.TempDir())
		if err := h.Run(context.Background(), turn, func(harness.Event) {}); err != nil {
			t.Fatalf("第一轮 Run 返回错误: %v", err)
		}
		select {
		case err := <-attached:
			if err != nil {
				t.Fatalf("Attach 轮返回错误: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Attach 轮没有收尾")
		}
		waitFor(t, 10*time.Second, "排空后进程被空闲回收", func() bool { return residentCount(h) == 0 })
	})
}

// TestResident_AttachWithoutResidentDoesNothing 覆盖 Attach 时进程不存在：直接返回 nil，
// 不起新进程，也不发任何事件。
func TestResident_AttachWithoutResidentDoesNothing(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	logPath := filepath.Join(t.TempDir(), "starts.log")
	h := New(Options{Binary: os.Args[0], IdleTTL: time.Minute})
	defer h.Close()

	var events []harness.Event
	if err := h.Run(context.Background(), attachOf(residentTurn("resident:"+fixture, logPath, t.TempDir())), collectEvents(&events)); err != nil {
		t.Fatalf("进程不存在时 Attach 应返回 nil，实际 %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("进程不存在时 Attach 不该发事件，实际 %+v", events)
	}
	if starts := fakeStartArgvs(t, logPath); len(starts) != 0 {
		t.Fatalf("Attach 不该起新进程，实际启动 %d 次: %v", len(starts), starts)
	}
}

// TestResidentProcessExitNotifiesHandler 覆盖常驻进程退出时的回调通知：进程因 EOF 退出后，
// ProcessExitHandler 拿到正确的 threadID 并被调用一次；未登记 handler 时行为不变。
func TestResidentProcessExitNotifiesHandler(t *testing.T) {
	fixture := fixtureAbs(t, "basic.jsonl")
	logPath := filepath.Join(t.TempDir(), "starts.log")
	h := New(Options{Binary: os.Args[0], IdleTTL: time.Minute})
	defer h.Close()

	var _ harness.ProcessExitNotifier = h

	type exitReport struct {
		threadID string
		before   time.Time
	}
	calls := make(chan exitReport, 4)
	h.SetProcessExitHandler(func(threadID string, before time.Time) {
		calls <- exitReport{threadID: threadID, before: before}
	})

	turn := residentTurn("resident_die_after_first:"+fixture, logPath, t.TempDir())
	if err := h.Run(context.Background(), turn, func(harness.Event) {}); err != nil {
		t.Fatalf("第一轮 Run 返回错误: %v", err)
	}

	select {
	case rep := <-calls:
		if rep.threadID != residentTestThreadID {
			t.Fatalf("回调应带上正确的 threadID，期望 %q，实际 %q", residentTestThreadID, rep.threadID)
		}
		if !rep.before.IsZero() {
			t.Fatalf("无替代进程时 before 应为零值，实际 %v", rep.before)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("常驻进程退出后应触发 ProcessExitHandler")
	}

	select {
	case extra := <-calls:
		t.Fatalf("ProcessExitHandler 只该触发一次，多收到 %+v", extra)
	case <-time.After(200 * time.Millisecond):
	}

	// 没登记 handler 时行为不变（不 panic、不影响退出）
	h2 := New(Options{Binary: os.Args[0], IdleTTL: time.Minute})
	defer h2.Close()
	turn2 := residentTurn("resident_die_after_first:"+fixture, filepath.Join(t.TempDir(), "starts2.log"), t.TempDir())
	if err := h2.Run(context.Background(), turn2, func(harness.Event) {}); err != nil {
		t.Fatalf("未登记 handler 时 Run 返回错误: %v", err)
	}
	waitFor(t, 3*time.Second, "未登记 handler 时进程正常退出", func() bool {
		return len(h2.ProcessRootPIDs(residentTestThreadID)) == 0
	})
}
