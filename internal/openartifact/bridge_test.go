package openartifact

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/procattr"
)

type fakeHost struct {
	mu           sync.Mutex
	wakeErr      error
	wakeCalls    []wakeCall
	openResult   bool
	openCalls    []openCall
	wakeBlockCh  chan struct{}
	openBlockCh  chan struct{}
	failWakeText string
}

type wakeCall struct {
	threadID string
	text     string
}

type openCall struct {
	threadID string
	url      string
}

func (h *fakeHost) DeliverWake(threadID, text string) error {
	if h.wakeBlockCh != nil {
		<-h.wakeBlockCh
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.wakeCalls = append(h.wakeCalls, wakeCall{threadID: threadID, text: text})
	if h.failWakeText != "" && strings.Contains(text, h.failWakeText) {
		return errors.New("deliver failed")
	}
	return h.wakeErr
}

func (h *fakeHost) OpenURL(threadID, url string) bool {
	if h.openBlockCh != nil {
		<-h.openBlockCh
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.openCalls = append(h.openCalls, openCall{threadID: threadID, url: url})
	return h.openResult
}

func newHelperBridge(t *testing.T, mode string, host Host, extraEnv ...string) (*Bridge, *[]string, *sync.Mutex) {
	t.Helper()
	var logMu sync.Mutex
	var logs []string

	b := New(Options{
		Host: host,
		Logf: func(format string, args ...any) {
			logMu.Lock()
			logs = append(logs, fmt.Sprintf(format, args...))
			logMu.Unlock()
		},
	})
	b.cmdFactory = func(ctx context.Context) (*exec.Cmd, error) {
		cmd := procattr.TerminateOnCancel(procattr.HideWindow(exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperProcess$")))
		env := append(os.Environ(), "GO_WANT_OPENARTIFACT_HELPER=1", "OPENARTIFACT_HELPER_MODE="+mode)
		env = append(env, extraEnv...)
		cmd.Env = env
		return cmd, nil
	}
	return b, &logs, &logMu
}

func TestBridgeHelloCapsAndSkillAndListTools(t *testing.T) {
	host := &fakeHost{openResult: true}
	b, logs, logMu := newHelperBridge(t, "basic", host)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.Start(ctx); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	defer func() { _ = b.Close() }()

	if !b.Ready() {
		t.Fatal("Start 后 Ready() 应为 true")
	}

	tools, err := b.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools 失败: %v", err)
	}
	if len(tools) != 3 || tools[0].Name != "Artifact" || tools[1].Name != "ArtifactData" || tools[2].Name != "ArtifactComments" {
		t.Fatalf("ListTools 返回异常: %+v", tools)
	}

	skill, err := b.Skill(ctx)
	if err != nil {
		t.Fatalf("Skill 失败: %v", err)
	}
	if skill != "mcode-entry-skill" {
		t.Fatalf("Skill 返回 %q，期望 %q", skill, "mcode-entry-skill")
	}

	if err := b.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	if b.Ready() {
		t.Fatal("Close 后 Ready() 应为 false")
	}

	logMu.Lock()
	joined := strings.Join(*logs, "\n")
	logMu.Unlock()
	if !strings.Contains(joined, "helper-stderr-ready") {
		t.Fatalf("未收到子进程 stderr 日志，日志内容: %q", joined)
	}
}

func TestBridgeCallToolAttachAndCaller(t *testing.T) {
	host := &fakeHost{openResult: true}
	b, _, _ := newHelperBridge(t, "attach-and-call", host)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.Start(ctx); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	defer func() { _ = b.Close() }()

	res1, err := b.CallTool(ctx, "thread-1", "/tmp/proj", "Artifact", json.RawMessage(`{"action":"quickstart"}`))
	if err != nil {
		t.Fatalf("首次 CallTool 失败: %v", err)
	}
	if !strings.Contains(string(res1), `"attachCount":1`) {
		t.Fatalf("首次 CallTool 应先触发一次 session.attach，返回: %s", res1)
	}

	res2, err := b.CallTool(ctx, "thread-1", "/tmp/proj", "Artifact", json.RawMessage(`{"action":"list"}`))
	if err != nil {
		t.Fatalf("第二次 CallTool 失败: %v", err)
	}
	if !strings.Contains(string(res2), `"attachCount":1`) {
		t.Fatalf("同一会话第二次 CallTool 不应重复 attach，返回: %s", res2)
	}

	if err := b.SessionStatus(ctx, "thread-1", "running"); err != nil {
		t.Fatalf("SessionStatus 失败: %v", err)
	}
	if err := b.Detach(ctx, "thread-1"); err != nil {
		t.Fatalf("Detach 失败: %v", err)
	}

	res3, err := b.CallTool(ctx, "thread-1", "/tmp/proj", "Artifact", json.RawMessage(`{"action":"read"}`))
	if err != nil {
		t.Fatalf("Detach 后再次 CallTool 失败: %v", err)
	}
	if !strings.Contains(string(res3), `"attachCount":2`) {
		t.Fatalf("Detach 后再次 CallTool 应重新 attach，返回: %s", res3)
	}
}

func TestBridgeWakeDeliver(t *testing.T) {
	host := &fakeHost{failWakeText: "should-fail"}
	b, _, _ := newHelperBridge(t, "wake-deliver", host)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.Start(ctx); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	defer func() { _ = b.Close() }()

	raw, err := b.CallTool(ctx, "thread-1", "/tmp/proj", "Artifact", json.RawMessage(`{"action":"trigger_wake"}`))
	if err != nil {
		t.Fatalf("CallTool 失败: %v", err)
	}
	var got struct {
		OKStatus   string `json:"okStatus"`
		FailStatus string `json:"failStatus"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("解析结果失败: %v", err)
	}
	if got.OKStatus != "queued" {
		t.Fatalf("成功投递状态 = %q，期望 queued", got.OKStatus)
	}
	if got.FailStatus != "failed" {
		t.Fatalf("失败投递状态 = %q，期望 failed", got.FailStatus)
	}

	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.wakeCalls) != 2 {
		t.Fatalf("DeliverWake 调用次数 = %d，期望 2", len(host.wakeCalls))
	}
	const wantFence = "<<<UNTRUSTED-DATA-nonce123>>>\n请检查页面\n<<<END-UNTRUSTED-DATA-nonce123>>>"
	if host.wakeCalls[0].threadID != "thread-1" || host.wakeCalls[0].text != wantFence {
		t.Fatalf("DeliverWake 参数被改动: %+v", host.wakeCalls[0])
	}
}

func TestBridgeHostOpenAndUnknownMethod(t *testing.T) {
	host := &fakeHost{openResult: true}
	b, _, _ := newHelperBridge(t, "host-open", host)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.Start(ctx); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	defer func() { _ = b.Close() }()

	raw, err := b.CallTool(ctx, "thread-9", "/tmp/proj", "Artifact", json.RawMessage(`{"action":"publish"}`))
	if err != nil {
		t.Fatalf("CallTool 失败: %v", err)
	}
	var got struct {
		Opened      bool   `json:"opened"`
		UnknownCode int    `json:"unknownCode"`
		UnknownData string `json:"unknownData"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("解析结果失败: %v", err)
	}
	if !got.Opened {
		t.Fatal("host.open 应返回 opened=true")
	}
	if got.UnknownCode != -32601 || got.UnknownData != "method_not_found" {
		t.Fatalf("未知方法错误不符合 JSON-RPC 规范: %+v", got)
	}

	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.openCalls) != 1 || host.openCalls[0].threadID != "thread-9" || host.openCalls[0].url != "http://127.0.0.1:4800/a/art-1" {
		t.Fatalf("OpenURL 调用参数异常: %+v", host.openCalls)
	}
}

func TestBridgeReattachedInboxAndReturn(t *testing.T) {
	host := &fakeHost{failWakeText: "return-me"}
	b, _, _ := newHelperBridge(t, "reattached", host)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.Start(ctx); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	defer func() { _ = b.Close() }()

	raw, err := b.CallTool(ctx, "thread-1", "/tmp/proj", "Artifact", json.RawMessage(`{"action":"verify_return"}`))
	if err != nil {
		t.Fatalf("CallTool 失败: %v", err)
	}
	var got struct {
		ReturnedCount int      `json:"returnedCount"`
		ReturnedTexts []string `json:"returnedTexts"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("解析结果失败: %v", err)
	}
	if got.ReturnedCount != 1 || !reflect.DeepEqual(got.ReturnedTexts, []string{"return-me"}) {
		t.Fatalf("inbox.return 内容异常: %+v", got)
	}
}

func TestBridgeProcessExitAndRestart(t *testing.T) {
	host := &fakeHost{openResult: true}
	b, _, _ := newHelperBridge(t, "exit-on-call", host)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.Start(ctx); err != nil {
		t.Fatalf("首次 Start 失败: %v", err)
	}

	if _, err := b.CallTool(ctx, "thread-1", "/tmp/proj", "Artifact", json.RawMessage(`{"action":"crash"}`)); err == nil {
		t.Fatal("子进程退出时 CallTool 应返回错误")
	}

	deadline := time.Now().Add(2 * time.Second)
	for b.Ready() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if b.Ready() {
		t.Fatal("子进程退出后 Ready() 应为 false")
	}
	if _, err := b.ListTools(ctx); !errors.Is(err, ErrNotReady) {
		t.Fatalf("子进程退出后 ListTools 应返回 ErrNotReady，实际: %v", err)
	}

	// 切换为正常模式后重新拉起。
	b.cmdFactory = func(c context.Context) (*exec.Cmd, error) {
		cmd := procattr.TerminateOnCancel(procattr.HideWindow(exec.CommandContext(c, os.Args[0], "-test.run=^TestHelperProcess$")))
		cmd.Env = append(os.Environ(), "GO_WANT_OPENARTIFACT_HELPER=1", "OPENARTIFACT_HELPER_MODE=basic")
		return cmd, nil
	}
	if err := b.Start(ctx); err != nil {
		t.Fatalf("重新 Start 失败: %v", err)
	}
	defer func() { _ = b.Close() }()
	if !b.Ready() {
		t.Fatal("重新 Start 后 Ready() 应为 true")
	}
	tools, err := b.ListTools(ctx)
	if err != nil || len(tools) != 3 {
		t.Fatalf("重新 Start 后 ListTools 异常: %v, %+v", err, tools)
	}
}

func TestBridgeCloseKillsStuckProcessAndCancelCall(t *testing.T) {
	host := &fakeHost{openResult: true}
	b, _, _ := newHelperBridge(t, "ignore-eof", host)
	b.closeGrace = 100 * time.Millisecond
	b.killWait = 200 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.Start(ctx); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	callCtx, callCancel := context.WithTimeout(ctx, 80*time.Millisecond)
	_, err := b.CallTool(callCtx, "thread-1", "/tmp/proj", "Artifact", json.RawMessage(`{"action":"hang"}`))
	callCancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("超时 CallTool 期望 DeadlineExceeded，实际: %v", err)
	}

	start := time.Now()
	if err := b.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Close 强杀卡死进程耗时过长: %v", elapsed)
	}
}

func TestBridgeConcurrentCalls(t *testing.T) {
	host := &fakeHost{openResult: true}
	b, _, _ := newHelperBridge(t, "attach-and-call", host)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.Start(ctx); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	defer func() { _ = b.Close() }()

	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := range 6 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			thread := fmt.Sprintf("thread-%d", idx%2)
			if _, err := b.CallTool(ctx, thread, "/tmp/proj", "Artifact", json.RawMessage(`{"action":"list"}`)); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("并发 CallTool 失败: %v", err)
	}
}

func TestBridgeNodeBinDirEnv(t *testing.T) {
	base := []string{"FOO=bar", "Path=" + filepath.Join("/usr", "bin")}
	got := buildEnv(base, "/opt/node/bin")
	wantPath := "Path=" + "/opt/node/bin" + string(os.PathListSeparator) + filepath.Join("/usr", "bin")
	if got[1] != wantPath {
		t.Fatalf("buildEnv Path = %q，期望 %q", got[1], wantPath)
	}
	if base[1] != "Path="+filepath.Join("/usr", "bin") {
		t.Fatal("buildEnv 不应原地修改调用方切片")
	}
}

// TestHelperProcess 作为假 open-artifact stdio 桥接子进程运行。
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_OPENARTIFACT_HELPER") != "1" {
		return
	}
	mode := os.Getenv("OPENARTIFACT_HELPER_MODE")
	_, _ = fmt.Fprintln(os.Stderr, "helper-stderr-ready")

	runHelperBridge(mode, os.Stdin, os.Stdout)
	os.Exit(0)
}

func runHelperBridge(mode string, in io.Reader, out io.Writer) {
	var writeMu sync.Mutex
	writeLine := func(v any) {
		data, _ := json.Marshal(v)
		data = append(data, '\n')
		writeMu.Lock()
		_, _ = out.Write(data)
		writeMu.Unlock()
	}

	var mu sync.Mutex
	subNext := 0
	pendingSub := make(map[string]chan rpcMessage)
	attachedSessions := make(map[string]int)
	attachCount := 0
	var returnedMsgs []json.RawMessage
	reattachDone := make(chan struct{})
	var reattachOnce sync.Once

	callHost := func(method string, params any) rpcMessage {
		mu.Lock()
		subNext++
		id := fmt.Sprintf("d%d", subNext)
		ch := make(chan rpcMessage, 1)
		pendingSub[id] = ch
		mu.Unlock()

		writeLine(map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"method":  method,
			"params":  params,
		})
		return <-ch
	}

	br := bufio.NewReader(in)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				var msg rpcMessage
				if jsonErr := json.Unmarshal(trimmed, &msg); jsonErr == nil {
					if msg.Method == "" && hasRPCID(msg.ID) {
						idKey := strings.Trim(string(bytes.TrimSpace(msg.ID)), `"`)
						mu.Lock()
						ch := pendingSub[idKey]
						delete(pendingSub, idKey)
						mu.Unlock()
						if ch != nil {
							ch <- msg
						}
					} else if msg.Method != "" {
						switch msg.Method {
						case "hello":
							var hp struct {
								Harness string `json:"harness"`
								Token   string `json:"token"`
								Caps    struct {
									AgentName string          `json:"agentName"`
									Tools     string          `json:"tools"`
									Wake      json.RawMessage `json:"wake"`
									Open      any             `json:"open"`
								} `json:"caps"`
							}
							_ = json.Unmarshal(msg.Params, &hp)
							if hp.Harness != HarnessName || hp.Token != "" || hp.Caps.AgentName != "mcode" || hp.Caps.Tools != "native" || len(hp.Caps.Wake) == 0 || hp.Caps.Open != true {
								writeLine(map[string]any{
									"jsonrpc": "2.0",
									"id":      msg.ID,
									"error": map[string]any{
										"code":    -32602,
										"message": fmt.Sprintf("unexpected hello params: %s", string(msg.Params)),
									},
								})
								continue
							}
							writeLine(map[string]any{
								"jsonrpc": "2.0",
								"method":  "bridge.state",
								"params":  map[string]string{"state": "connected"},
							})
							writeLine(map[string]any{
								"jsonrpc": "2.0",
								"id":      msg.ID,
								"result": map[string]any{
									"protocol":   1,
									"daemon":     "0.16.2",
									"connection": "conn-1",
									"features":   []string{"skill.get", "inbox.return"},
								},
							})
							if mode == "reattached" {
								go func() {
									writeLine(map[string]any{
										"jsonrpc": "2.0",
										"method":  "bridge.reattached",
										"params": map[string]any{
											"params": map[string]any{
												"session":   "thread-1",
												"workspace": "/tmp/proj",
											},
											"inbox": []any{
												map[string]any{"kind": "wake", "text": "deliver-ok", "at": "2026-11-26T00:00:00Z"},
												map[string]any{"kind": "wake", "text": "return-me", "at": "2026-11-26T00:00:01Z"},
											},
										},
									})
								}()
							}

						case "tools.list":
							writeLine(map[string]any{
								"jsonrpc": "2.0",
								"id":      msg.ID,
								"result": map[string]any{
									"tools": []any{
										map[string]any{"name": "Artifact", "description": "d1", "inputSchema": map[string]any{"type": "object"}},
										map[string]any{"name": "ArtifactData", "description": "d2", "inputSchema": map[string]any{"type": "object"}},
										map[string]any{"name": "ArtifactComments", "description": "d3", "inputSchema": map[string]any{"type": "object"}},
									},
								},
							})

						case "skill.get":
							var sp map[string]string
							_ = json.Unmarshal(msg.Params, &sp)
							if sp["form"] != "entry" || sp["tools"] != "native" || sp["opens"] != "host" || sp["agentName"] != "mcode" {
								writeLine(map[string]any{
									"jsonrpc": "2.0",
									"id":      msg.ID,
									"error":   map[string]any{"code": -32602, "message": "invalid skill.get params"},
								})
								continue
							}
							writeLine(map[string]any{
								"jsonrpc": "2.0",
								"id":      msg.ID,
								"result":  map[string]any{"name": "artifact", "body": "mcode-entry-skill"},
							})

						case "session.attach":
							var sp struct {
								Session   string `json:"session"`
								Workspace string `json:"workspace"`
							}
							_ = json.Unmarshal(msg.Params, &sp)
							mu.Lock()
							attachCount++
							attachedSessions[sp.Session]++
							mu.Unlock()
							writeLine(map[string]any{
								"jsonrpc": "2.0",
								"id":      msg.ID,
								"result":  map[string]any{"key": sp.Session, "inbox": []any{}},
							})

						case "session.status", "session.detach":
							writeLine(map[string]any{
								"jsonrpc": "2.0",
								"id":      msg.ID,
								"result":  map[string]any{"ok": true},
							})

						case "inbox.return":
							var ir struct {
								Session   string            `json:"session"`
								Workspace string            `json:"workspace"`
								Messages  []json.RawMessage `json:"messages"`
							}
							_ = json.Unmarshal(msg.Params, &ir)
							mu.Lock()
							returnedMsgs = append(returnedMsgs, ir.Messages...)
							mu.Unlock()
							reattachOnce.Do(func() { close(reattachDone) })
							writeLine(map[string]any{
								"jsonrpc": "2.0",
								"id":      msg.ID,
								"result":  map[string]any{"ok": true, "returned": len(ir.Messages)},
							})

						case "tools.call":
							var tc struct {
								Name      string `json:"name"`
								Arguments struct {
									Action string `json:"action"`
								} `json:"arguments"`
								Caller struct {
									Session   string `json:"session"`
									Workspace string `json:"workspace"`
								} `json:"caller"`
							}
							_ = json.Unmarshal(msg.Params, &tc)
							if tc.Caller.Session == "" || tc.Caller.Workspace == "" {
								writeLine(map[string]any{
									"jsonrpc": "2.0",
									"id":      msg.ID,
									"error":   map[string]any{"code": -32602, "message": "missing caller"},
								})
								continue
							}
							switch tc.Arguments.Action {
							case "crash":
								return
							case "hang":
								continue
							case "trigger_wake":
								go func(id json.RawMessage) {
									r1 := callHost("wake.deliver", map[string]any{
										"session":    "thread-1",
										"workspace":  "/tmp/proj",
										"artifact":   "open-artifact://artifact/a1",
										"artifactId": "a1",
										"text":       "<<<UNTRUSTED-DATA-nonce123>>>\n请检查页面\n<<<END-UNTRUSTED-DATA-nonce123>>>",
									})
									r2 := callHost("wake.deliver", map[string]any{
										"session":    "thread-1",
										"workspace":  "/tmp/proj",
										"artifact":   "open-artifact://artifact/a1",
										"artifactId": "a1",
										"text":       "should-fail",
									})
									var s1, s2 struct {
										Status string `json:"status"`
									}
									_ = json.Unmarshal(r1.Result, &s1)
									_ = json.Unmarshal(r2.Result, &s2)
									writeLine(map[string]any{
										"jsonrpc": "2.0",
										"id":      id,
										"result":  map[string]any{"okStatus": s1.Status, "failStatus": s2.Status},
									})
								}(msg.ID)
							case "publish":
								go func(id json.RawMessage) {
									r1 := callHost("host.open", map[string]any{
										"session":  "thread-9",
										"url":      "open-artifact://artifact/art-1",
										"view_url": "http://127.0.0.1:4800/a/art-1",
									})
									r2 := callHost("unknown.method", map[string]any{})
									var openRes struct {
										Opened bool `json:"opened"`
									}
									_ = json.Unmarshal(r1.Result, &openRes)
									errCode := 0
									errData := ""
									if r2.Error != nil {
										errCode = r2.Error.Code
										if r2.Error.Data != nil {
											errData = r2.Error.Data.Code
										}
									}
									writeLine(map[string]any{
										"jsonrpc": "2.0",
										"id":      id,
										"result": map[string]any{
											"opened":      openRes.Opened,
											"unknownCode": errCode,
											"unknownData": errData,
										},
									})
								}(msg.ID)
							case "verify_return":
								go func(id json.RawMessage) {
									select {
									case <-reattachDone:
									case <-time.After(3 * time.Second):
									}
									mu.Lock()
									var texts []string
									for _, rm := range returnedMsgs {
										var item struct {
											Text string `json:"text"`
										}
										_ = json.Unmarshal(rm, &item)
										texts = append(texts, item.Text)
									}
									cnt := len(returnedMsgs)
									mu.Unlock()
									writeLine(map[string]any{
										"jsonrpc": "2.0",
										"id":      id,
										"result":  map[string]any{"returnedCount": cnt, "returnedTexts": texts},
									})
								}(msg.ID)
							default:
								mu.Lock()
								cnt := attachCount
								mu.Unlock()
								writeLine(map[string]any{
									"jsonrpc": "2.0",
									"id":      msg.ID,
									"result":  map[string]any{"attachCount": cnt},
								})
							}
						}
					}
				}
			}
		}
		if err != nil {
			if mode == "ignore-eof" {
				time.Sleep(10 * time.Second)
				return
			}
			return
		}
	}
}
