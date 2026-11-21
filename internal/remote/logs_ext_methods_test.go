package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

type logsTestNotification struct {
	method string
	params wire.RemoteLogsEventNotificationParams
}

type logsRecordingConn struct {
	mu     sync.Mutex
	info   ClientInfo
	events []logsTestNotification
	notify chan logsTestNotification
}

func newLogsRecordingConn() *logsRecordingConn {
	return &logsRecordingConn{
		info:   ClientInfo{Authenticated: true, DeviceID: "dev-logs"},
		notify: make(chan logsTestNotification, 64),
	}
}

func (c *logsRecordingConn) Info() ClientInfo { return c.info }

func (c *logsRecordingConn) MarkAuthenticated(deviceID, label string) {
	c.info.Authenticated = true
	c.info.DeviceID = deviceID
	c.info.Label = label
}

func (c *logsRecordingConn) Notify(method string, params any) error {
	if method != "logs.event" {
		return nil
	}
	ev, ok := params.(wire.RemoteLogsEventNotificationParams)
	if !ok {
		return nil
	}
	c.mu.Lock()
	c.events = append(c.events, logsTestNotification{method: method, params: ev})
	c.mu.Unlock()
	select {
	case c.notify <- logsTestNotification{method: method, params: ev}:
	default:
	}
	return nil
}

func (c *logsRecordingConn) Close() error { return nil }

func (c *logsRecordingConn) snapshotEvents() []wire.RemoteLogsEventNotificationParams {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]wire.RemoteLogsEventNotificationParams, len(c.events))
	for i, ev := range c.events {
		out[i] = ev.params
	}
	return out
}

func callLogsRPC(t *testing.T, d *Dispatcher, conn Conn, method string, params any) (json.RawMessage, *wire.RPCError) {
	t.Helper()
	rawParams, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	req := fmt.Sprintf(`{"jsonrpc":"2.0","method":%q,"params":%s,"id":1}`, method, string(rawParams))
	rawResp := d.Handle(context.Background(), conn, []byte(req))
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *wire.RPCError  `json:"error"`
	}
	if err := json.Unmarshal(rawResp, &resp); err != nil {
		t.Fatalf("unmarshal response failed: %v, raw=%s", err, string(rawResp))
	}
	return resp.Result, resp.Error
}

func TestLogsExt_SubscribeAndRealtimeToolEvents(t *testing.T) {
	b, d, workDir, _, threadID := setupWorkspaceTestEnv(t)
	doneCh := make(chan struct{})
	h := &mockHarness{
		kind: harness.Kind("mock"),
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventToolStart, ToolID: "tool-1", ToolName: "Bash", Input: json.RawMessage(`{"cmd":"echo hi"}`)})
			// 第一条增量会触发 threads.event 发送，第二条紧接着的短增量会被 toolOutputStream 节流，
			// 验证被节流的增量仍会进入 LogStreamHub 并通过 logs.event 推送。
			emit(harness.Event{Type: harness.EventToolOutput, ToolID: "tool-1", Text: "first line\n"})
			emit(harness.Event{Type: harness.EventToolOutput, ToolID: "tool-1", Text: "throttled second line\n"})
			emit(harness.Event{Type: harness.EventToolEnd, ToolID: "tool-1", Output: "first line\nthrottled second line\n"})
			emit(harness.Event{Type: harness.EventDone})
			close(doneCh)
			return nil
		},
	}
	b.SetHarness(h, workDir, harness.ModeAgent)

	conn := newLogsRecordingConn()
	raw, rpcErr := callLogsRPC(t, d, conn, "logs.subscribe", wire.RemoteLogsSubscribeParams{ThreadID: threadID})
	if rpcErr != nil {
		t.Fatalf("logs.subscribe failed: %+v", rpcErr)
	}
	var subRes wire.RemoteLogsSubscribeResult
	if err := json.Unmarshal(raw, &subRes); err != nil {
		t.Fatal(err)
	}
	if subRes.ThreadID != threadID || len(subRes.Streams) != 0 || len(subRes.Chunks) != 0 {
		t.Fatalf("unexpected initial subscribe result: %+v", subRes)
	}

	if _, err := b.SendMessage(context.Background(), wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "run tool",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for mock harness run")
	}

	deadline := time.Now().Add(2 * time.Second)
	var allText strings.Builder
	var sawStart, sawDone bool
	var lastSeq int
	for time.Now().Before(deadline) {
		events := conn.snapshotEvents()
		allText.Reset()
		sawStart, sawDone = false, false
		lastSeq = 0
		monotonic := true
		for _, ev := range events {
			if ev.Sequence <= lastSeq {
				monotonic = false
			}
			lastSeq = ev.Sequence
			if ev.Stream != nil {
				if ev.Stream.Source != "tool" {
					t.Fatalf("expected source=tool, got %+v", ev.Stream)
				}
				if ev.Stream.Status == "running" {
					sawStart = true
				}
				if ev.Stream.Status == "done" {
					sawDone = true
				}
			}
			for _, ch := range ev.Chunks {
				allText.WriteString(ch.Text)
			}
		}
		if !monotonic {
			t.Fatalf("non-monotonic sequence in logs.event: %+v", events)
		}
		if sawStart && sawDone && strings.Contains(allText.String(), "throttled second line") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !sawStart || !sawDone || !strings.Contains(allText.String(), "first line\n") || !strings.Contains(allText.String(), "throttled second line\n") {
		t.Fatalf("missing expected logs.event stream/chunks: sawStart=%v sawDone=%v text=%q", sawStart, sawDone, allText.String())
	}
}

func TestLogsExt_ScriptOutputStreaming(t *testing.T) {
	b, d, workDir, projectID, threadID := setupWorkspaceTestEnv(t)
	conn := newLogsRecordingConn()

	raw, rpcErr := callLogsRPC(t, d, conn, "logs.subscribe", wire.RemoteLogsSubscribeParams{ThreadID: threadID})
	if rpcErr != nil {
		t.Fatalf("logs.subscribe failed: %+v", rpcErr)
	}
	var subRes wire.RemoteLogsSubscribeResult
	if err := json.Unmarshal(raw, &subRes); err != nil {
		t.Fatal(err)
	}

	// 保存并运行一个项目脚本，验证 cappedOutput.onWrite 驱动 logs.event。
	_, err := b.SetProjectScriptsByProjectID(projectID, []ProjectScript{
		{
			ID:      "11111111-1111-4111-8111-111111111111",
			Name:    "echo-test",
			Command: "echo script-live-output",
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	scriptID := "11111111-1111-4111-8111-111111111111"
	runRes, err := b.ScriptsRun(context.Background(), wire.ScriptsRunParams{
		ThreadID: threadID,
		Kind:     "project",
		ScriptID: &scriptID,
	})
	if err != nil {
		t.Fatalf("ScriptsRun failed: %v", err)
	}
	if runRes.RunID == "" {
		t.Fatal("expected non-empty runID")
	}

	deadline := time.Now().Add(5 * time.Second)
	var sawScriptDone bool
	var gotText strings.Builder
	for time.Now().Before(deadline) {
		events := conn.snapshotEvents()
		gotText.Reset()
		sawScriptDone = false
		for _, ev := range events {
			if ev.Stream != nil && ev.Stream.StreamID == runRes.RunID && ev.Stream.Source == "script" && ev.Stream.Status == "done" {
				sawScriptDone = true
			}
			for _, ch := range ev.Chunks {
				if ch.StreamID == runRes.RunID {
					gotText.WriteString(ch.Text)
				}
			}
		}
		if sawScriptDone && strings.Contains(gotText.String(), "script-live-output") {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !sawScriptDone || !strings.Contains(gotText.String(), "script-live-output") {
		t.Fatalf("expected script logs.event output and done status, sawDone=%v text=%q dir=%s", sawScriptDone, gotText.String(), workDir)
	}
}

func TestLogsExt_SecretRedaction(t *testing.T) {
	b, d, _, _, threadID := setupWorkspaceTestEnv(t)
	conn := newLogsRecordingConn()
	if _, rpcErr := callLogsRPC(t, d, conn, "logs.subscribe", wire.RemoteLogsSubscribeParams{ThreadID: threadID}); rpcErr != nil {
		t.Fatalf("subscribe failed: %+v", rpcErr)
	}

	secretOpenAI := "sk-abcdefghijklmnopqrstuvwxyz123456"
	secretGitHub := "ghp_abcdefghijklmnopqrstuvwxyz1234567890"
	lh := b.ensureLogHub()
	lh.Start(threadID, "stream-sec", "tool", "Bash token="+secretOpenAI)
	lh.Append(threadID, "stream-sec", "tool", "Bash", "output key "+secretGitHub+"\n")
	lh.Finish(threadID, "stream-sec", "done")

	events := conn.snapshotEvents()
	if len(events) == 0 {
		t.Fatal("expected logs.event notifications")
	}
	rawEvents, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawEvents), secretOpenAI) || strings.Contains(string(rawEvents), secretGitHub) {
		t.Fatalf("raw secret leaked in logs.event: %s", string(rawEvents))
	}

	rawSnap, rpcErr := callLogsRPC(t, d, conn, "logs.subscribe", wire.RemoteLogsSubscribeParams{ThreadID: threadID})
	if rpcErr != nil {
		t.Fatalf("re-subscribe failed: %+v", rpcErr)
	}
	if strings.Contains(string(rawSnap), secretOpenAI) || strings.Contains(string(rawSnap), secretGitHub) {
		t.Fatalf("raw secret leaked in subscribe snapshot: %s", string(rawSnap))
	}
}

func TestLogStreamHub_RingBufferEvictionAndPayloadCap(t *testing.T) {
	hub := NewLogStreamHub()
	defer hub.Close()

	threadID := "thread-ring"
	// 1. 无订阅者时不启动 timer，但写入环缓冲且超 500 条时 FIFO 淘汰并标记 truncated=true。
	for i := 0; i < 520; i++ {
		hub.Append(threadID, "s-1", "tool", "Bash", fmt.Sprintf("line-%03d\n", i))
	}
	hub.mu.Lock()
	if st := hub.threads[threadID]; st != nil && st.timer != nil {
		hub.mu.Unlock()
		t.Fatal("timer must not be started when there are no subscribers")
	}
	hub.mu.Unlock()

	snap := hub.Subscribe(nil, threadID)
	if !snap.Truncated {
		t.Fatal("expected truncated=true after >500 chunks")
	}
	if len(snap.Chunks) != maxLogHubThreadChunks {
		t.Fatalf("expected %d chunks, got %d", maxLogHubThreadChunks, len(snap.Chunks))
	}
	if !strings.Contains(snap.Chunks[0].Text, "line-020") {
		t.Fatalf("expected FIFO eviction keeping line-020 first, got %q", snap.Chunks[0].Text)
	}

	// 2. 超过 20 条 stream 时保留最近 20 条。
	for i := 0; i < 25; i++ {
		id := fmt.Sprintf("stream-%02d", i)
		hub.Start(threadID, id, "tool", id)
		hub.Finish(threadID, id, "done")
	}
	snap = hub.Subscribe(nil, threadID)
	if len(snap.Streams) != maxLogHubStreams {
		t.Fatalf("expected %d streams, got %d", maxLogHubStreams, len(snap.Streams))
	}

	// 3. 超过 256 KiB 环缓冲淘汰。
	threadBytes := "thread-bytes"
	bigChunk := strings.Repeat("x", 40*1024)
	for i := 0; i < 8; i++ { // 320 KiB > 256 KiB
		hub.Append(threadBytes, "s-big", "script", "big", bigChunk)
	}
	snapBytes := hub.Subscribe(nil, threadBytes)
	if !snapBytes.Truncated {
		t.Fatal("expected truncated=true when ring buffer exceeds 256 KiB")
	}
	totalBytes := 0
	for _, ch := range snapBytes.Chunks {
		totalBytes += len(ch.Text)
	}
	if totalBytes > maxLogHubThreadBytes {
		t.Fatalf("ring buffer bytes %d exceeds max %d", totalBytes, maxLogHubThreadBytes)
	}

	// 4. 单次 logs.event 推送载荷上限 64 KiB。
	conn := newLogsRecordingConn()
	threadCap := "thread-cap"
	hub.Subscribe(conn, threadCap)
	hugeUTF8 := strings.Repeat("测", 30*1024) // 90 KiB
	hub.Append(threadCap, "s-huge", "tool", "Huge", hugeUTF8)

	events := conn.snapshotEvents()
	if len(events) == 0 {
		t.Fatal("expected immediate flush for >= 4KB append")
	}
	frame, err := json.Marshal(rpcNotification{JSONRPC: "2.0", Method: "logs.event", Params: events[0]})
	if err != nil {
		t.Fatal(err)
	}
	if len(frame) > maxLogHubEventPayloadBytes {
		t.Fatalf("logs.event payload %d exceeds 64 KiB cap", len(frame))
	}
	if len(events[0].Chunks) == 0 || !utf8.ValidString(events[0].Chunks[0].Text) {
		t.Fatalf("capped chunk text invalid UTF-8 or empty")
	}
}

func TestLogsExt_UnsubscribeRemoveConnAndUnknownThread(t *testing.T) {
	b, d, _, _, threadID := setupWorkspaceTestEnv(t)
	conn1 := newLogsRecordingConn()
	conn2 := newLogsRecordingConn()

	// 未知会话与空 threadID
	_, rpcErr := callLogsRPC(t, d, conn1, "logs.subscribe", wire.RemoteLogsSubscribeParams{ThreadID: "   "})
	if rpcErr == nil || rpcErr.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for empty threadID, got %+v", rpcErr)
	}
	_, rpcErr = callLogsRPC(t, d, conn1, "logs.subscribe", wire.RemoteLogsSubscribeParams{ThreadID: "non-existent-thread"})
	if rpcErr == nil || rpcErr.Code != wire.ErrCodeResourceNotFound {
		t.Fatalf("expected ErrCodeResourceNotFound for unknown threadID, got %+v", rpcErr)
	}

	if _, rpcErr := callLogsRPC(t, d, conn1, "logs.subscribe", wire.RemoteLogsSubscribeParams{ThreadID: threadID}); rpcErr != nil {
		t.Fatalf("conn1 subscribe failed: %+v", rpcErr)
	}
	if _, rpcErr := callLogsRPC(t, d, conn2, "logs.subscribe", wire.RemoteLogsSubscribeParams{ThreadID: threadID}); rpcErr != nil {
		t.Fatalf("conn2 subscribe failed: %+v", rpcErr)
	}

	// conn1 取消订阅，conn2 断开移除
	rawUnsub, rpcErr := callLogsRPC(t, d, conn1, "logs.unsubscribe", wire.RemoteLogsUnsubscribeParams{ThreadID: threadID})
	if rpcErr != nil {
		t.Fatalf("logs.unsubscribe failed: %+v", rpcErr)
	}
	var unsubRes wire.RemoteLogsUnsubscribeResult
	if err := json.Unmarshal(rawUnsub, &unsubRes); err != nil || !unsubRes.Unsubscribed {
		t.Fatalf("unexpected unsubscribe result: %s err=%v", string(rawUnsub), err)
	}
	b.RemoveConn(conn2)

	lh := b.ensureLogHub()
	lh.Start(threadID, "after-unsub", "tool", "Bash")
	lh.Append(threadID, "after-unsub", "tool", "Bash", "should not push\n")
	lh.Finish(threadID, "after-unsub", "done")

	time.Sleep(80 * time.Millisecond)
	if len(conn1.snapshotEvents()) != 0 {
		t.Fatalf("conn1 should not receive events after unsubscribe, got %+v", conn1.snapshotEvents())
	}
	if len(conn2.snapshotEvents()) != 0 {
		t.Fatalf("conn2 should not receive events after RemoveConn, got %+v", conn2.snapshotEvents())
	}
}

func TestLogsExt_SchemaAndCapabilities(t *testing.T) {
	data, err := os.ReadFile("../../docs/remote-control.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}

	for _, m := range []string{"logs.subscribe", "logs.unsubscribe"} {
		foundReq := false
		for name, raw := range doc.Defs {
			var d struct {
				Properties struct {
					Method struct {
						Const string `json:"const"`
					} `json:"method"`
				} `json:"properties"`
			}
			if json.Unmarshal(raw, &d) == nil && d.Properties.Method.Const == m {
				foundReq = true
				if strings.Count(string(data), `"$ref": "#/$defs/`+name+`"`) != 2 {
					t.Fatalf("%s request $ref count != 2", m)
				}
				respName := strings.TrimSuffix(name, "Request") + "Response"
				if strings.Count(string(data), `"$ref": "#/$defs/`+respName+`"`) != 2 {
					t.Fatalf("%s response (%s) $ref count != 2", m, respName)
				}
				break
			}
		}
		if !foundReq {
			t.Fatalf("schema missing %s", m)
		}

		registered := false
		for _, impl := range ImplementedMethods() {
			if impl == m {
				registered = true
				break
			}
		}
		if !registered {
			t.Fatalf("ImplementedMethods missing %s", m)
		}
	}

	if _, ok := doc.Defs["LogsEventNotification"]; !ok {
		t.Fatal("schema missing LogsEventNotification")
	}
	if strings.Count(string(data), `"$ref": "#/$defs/LogsEventNotification"`) != 1 {
		t.Fatal("LogsEventNotification must be referenced once in top-level oneOf")
	}

	hasFeature := false
	for _, f := range DefaultFeatures() {
		if f == "logs.stream" {
			hasFeature = true
			break
		}
	}
	if !hasFeature {
		t.Fatalf("DefaultFeatures missing logs.stream: %v", DefaultFeatures())
	}
}
