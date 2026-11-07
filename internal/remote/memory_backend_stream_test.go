package remote

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

// 流式钩子先收到 reset=true 的空增量，再按顺序收到每条正文增量。
func TestMemoryBackend_AssistantStreamHook(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	release := make(chan struct{})
	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventText, Text: "第一段"})
			emit(harness.Event{Type: harness.EventText, Text: "，第二段"})
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			emit(harness.Event{Type: harness.EventText, Text: "，收尾"})
			return nil
		},
	}
	backend.SetHarness(mock, t.TempDir(), harness.ModeAgent)

	type streamCall struct {
		threadID  string
		projectID string
		reset     bool
		delta     string
	}
	var mu sync.Mutex
	var calls []streamCall
	backend.SetAssistantStreamHook(func(threadID, projectID string, reset bool, delta string) {
		mu.Lock()
		calls = append(calls, streamCall{threadID: threadID, projectID: projectID, reset: reset, delta: delta})
		mu.Unlock()
	})
	snapshot := func() []streamCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]streamCall(nil), calls...)
	}

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-0000000000aa"
	projectID := "00000000-0000-0000-0000-0000000000ab"
	backend.AddThread(wire.ThreadSummary{ID: threadID, ProjectID: projectID, Title: "Stream Hook"}, nil)

	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "开始",
		ActionID: "00000000-0000-0000-0000-0000000000ac",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for len(snapshot()) < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
	waitThreadIdle(t, backend, ctx, threadID)

	got := snapshot()
	if len(got) != 4 {
		t.Fatalf("expected reset + 3 deltas, got %+v", got)
	}
	if !got[0].reset || got[0].delta != "" {
		t.Errorf("expected first call reset=true with empty delta, got %+v", got[0])
	}
	wantDeltas := []string{"第一段", "，第二段", "，收尾"}
	for i, want := range wantDeltas {
		call := got[i+1]
		if call.reset || call.delta != want {
			t.Errorf("delta %d mismatch: %+v, want %q", i, call, want)
		}
	}
	for _, call := range got {
		if call.threadID != threadID || call.projectID != projectID {
			t.Errorf("unexpected thread/project in call: %+v", call)
		}
	}
}

// 运行中轮次记录写文件类工具触碰的路径：相对工作目录、去重排序；只读工具不计入。
func TestMemoryBackend_CurrentTurnTouchedPaths(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	workDir := t.TempDir()
	absInput, err := json.Marshal(map[string]string{"file_path": filepath.Join(workDir, "internal", "remote", "a.go")})
	if err != nil {
		t.Fatalf("marshal abs input: %v", err)
	}
	release := make(chan struct{})
	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventToolStart, ToolID: "call-1", ToolName: "Edit", Input: absInput})
			emit(harness.Event{Type: harness.EventToolStart, ToolID: "call-2", ToolName: "Write", Input: json.RawMessage(`{"file_path": "internal/remote/b.go"}`)})
			emit(harness.Event{Type: harness.EventToolStart, ToolID: "call-3", ToolName: "Read", Input: json.RawMessage(`{"file_path": "internal/remote/read-only.go"}`)})
			emit(harness.Event{Type: harness.EventToolStart, ToolID: "call-4", ToolName: "apply_patch", Input: json.RawMessage(`{"patch": "*** Begin Patch\n*** Update File: pkg/patch.go\n*** End Patch"}`)})
			emit(harness.Event{Type: harness.EventToolStart, ToolID: "call-5", ToolName: "Edit", Input: json.RawMessage(`{"file_path": "internal/remote/b.go"}`)})
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		},
	}
	backend.SetHarness(mock, workDir, harness.ModeAgent)

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-0000000000ba"
	backend.AddThread(wire.ThreadSummary{ID: threadID, ProjectID: "p1", Title: "Touched"}, nil)

	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "改文件",
		ActionID: "00000000-0000-0000-0000-0000000000bb",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	want := []string{"internal/remote/a.go", "internal/remote/b.go", "pkg/patch.go"}

	deadline := time.Now().Add(3 * time.Second)
	var paths []string
	var active bool
	for time.Now().Before(deadline) {
		paths, active = backend.CurrentTurnTouchedPaths(threadID)
		if active && len(paths) == len(want) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !active {
		t.Fatalf("expected an active turn while the harness is blocked")
	}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("unexpected touched paths: %v, want %v", paths, want)
	}

	close(release)
	waitThreadIdle(t, backend, ctx, threadID)

	// 轮次结束后路径保留到下一轮开始，active 变为 false。
	paths, active = backend.CurrentTurnTouchedPaths(threadID)
	if active {
		t.Errorf("expected inactive turn after finish")
	}
	if !reflect.DeepEqual(paths, want) {
		t.Errorf("expected touched paths kept after finish, got %v, want %v", paths, want)
	}
}
