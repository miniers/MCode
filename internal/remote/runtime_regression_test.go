package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mcode/internal/git"
	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

func runtimeBackend(t *testing.T, h harness.Harness) *MemoryBackend {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	hub := NewStreamHub(StreamHubOptions{})
	t.Cleanup(hub.Close)
	b := NewMemoryBackend(hub)
	t.Cleanup(b.Close)
	b.SetHarness(h, t.TempDir(), harness.ModeAgent)
	b.AddThread(wire.ThreadSummary{ID: newUUID(), Title: "runtime", Provider: string(h.Kind())}, nil)
	return b
}

func runtimeThread(b *MemoryBackend) string {
	for id := range b.threads {
		return id
	}
	return ""
}

func TestRuntimeRegression_RetryLimitAndOldAttempt(t *testing.T) {
	var calls atomic.Int32
	var oldEmit func(harness.Event)
	h := &customRunHarness{runFn: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		n := calls.Add(1)
		if n == 1 {
			oldEmit = emit
		} else {
			oldEmit(harness.Event{Type: harness.EventText, Text: "stale-attempt"})
		}
		emit(harness.Event{Type: harness.EventText, Text: "partial"})
		return errors.New("stream was interrupted")
	}}
	b := runtimeBackend(t, h)
	b.SetAfterFunc(func(_ time.Duration, f func()) func() bool { f(); return func() bool { return true } })
	id := runtimeThread(b)
	if _, err := b.SendMessage(context.Background(), wire.SendMessageParams{ThreadID: id, Text: "retry"}); err != nil {
		t.Fatal(err)
	}
	waitThreadIdle(t, b, context.Background(), id)
	if calls.Load() != 4 {
		t.Fatalf("got %d calls, want initial attempt plus 3 retries", calls.Load())
	}
	history, _ := b.ThreadHistory(context.Background(), wire.HistoryParams{ThreadID: id})
	for _, msg := range history.Messages {
		if strings.Contains(msg.Text, "stale-attempt") {
			t.Fatal("old retry callback changed history")
		}
	}
}

func TestRuntimeRegression_AuthErrorIsNotCrash(t *testing.T) {
	var calls atomic.Int32
	b := runtimeBackend(t, &customRunHarness{runFn: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		calls.Add(1)
		emit(harness.Event{Type: harness.EventText, Text: "partial"})
		return errors.New("authentication failed")
	}})
	id := runtimeThread(b)
	b.SendMessage(context.Background(), wire.SendMessageParams{ThreadID: id, Text: "auth"})
	waitThreadIdle(t, b, context.Background(), id)
	if calls.Load() != 1 {
		t.Fatal("authentication failure was retried as a process crash")
	}
}

func TestRuntimeRegression_ProviderExitRetriesOnceWithHistory(t *testing.T) {
	var calls atomic.Int32
	turns := make(chan harness.Turn, 2)
	h := &customRunHarness{kind: "agy", runFn: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		turns <- turn
		if calls.Add(1) == 1 {
			return errors.New("agy process exited with error: signal: killed")
		}
		emit(harness.Event{Type: harness.EventText, Text: "666"})
		return nil
	}}
	b := runtimeBackend(t, h)
	id := runtimeThread(b)
	if _, err := b.SendMessage(context.Background(), wire.SendMessageParams{ThreadID: id, Text: "retry after provider exit"}); err != nil {
		t.Fatal(err)
	}
	thread := waitThreadIdle(t, b, context.Background(), id)
	if calls.Load() != 2 {
		t.Fatalf("got %d Run calls, want 2", calls.Load())
	}
	first, second := <-turns, <-turns
	if second.SessionID != "" {
		t.Fatalf("second retry used provider session %q", second.SessionID)
	}
	if !strings.Contains(second.Prompt, "retry after provider exit") || first.Prompt == "" {
		t.Fatalf("retry did not retain the original history: first=%q second=%q", first.Prompt, second.Prompt)
	}
	if thread.IsRunning {
		t.Fatal("provider crash recovery left the thread running")
	}

	history, err := b.ThreadHistory(context.Background(), wire.HistoryParams{ThreadID: id})
	if err != nil {
		t.Fatal(err)
	}
	notice := "Provider 进程意外退出，已用本地历史重建上下文"
	noticeCount := 0
	completed := false
	for _, msg := range history.Messages {
		if msg.Text == notice {
			noticeCount++
		}
		if strings.Contains(msg.Text, "666") && msg.TurnStatus != nil && *msg.TurnStatus == "completed" {
			completed = true
		}
	}
	if noticeCount != 1 {
		t.Fatalf("got %d provider crash notices, want 1", noticeCount)
	}
	if !completed {
		t.Fatal("final recovered turn was not completed")
	}
}

func TestRuntimeRegression_ProviderCrashRecoveryLogsCause(t *testing.T) {
	var logs bytes.Buffer
	previousWriter := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousWriter) })

	const secret = "sk-test-secret-123456"
	var calls atomic.Int32
	h := &customRunHarness{kind: "agy", runFn: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		if calls.Add(1) == 1 {
			return errors.New("agy process exited with error: signal: killed; Authorization: Bearer " + secret)
		}
		emit(harness.Event{Type: harness.EventText, Text: "recovered"})
		return nil
	}}
	b := runtimeBackend(t, h)
	id := runtimeThread(b)
	if _, err := b.SendMessage(context.Background(), wire.SendMessageParams{ThreadID: id, Text: "log provider crash"}); err != nil {
		t.Fatal(err)
	}
	waitThreadIdle(t, b, context.Background(), id)

	got := logs.String()
	for _, want := range []string{"provider crash recovery", "thread=" + id, "provider=agy", "signal: killed"} {
		if !strings.Contains(got, want) {
			t.Errorf("crash recovery log missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, secret) {
		t.Fatalf("crash recovery log leaked bearer token: %s", got)
	}
}

func TestRuntimeRegression_RecallThenSend(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "turns.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	started := make(chan struct{}, 2)
	var calls atomic.Int32
	b := runtimeBackend(t, &customRunHarness{runFn: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		calls.Add(1)
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}})
	if err := b.AttachStore(ctx, s); err != nil {
		t.Fatal(err)
	}
	b.AddProject(wire.ProjectSummary{ID: newUUID(), Name: "project"})
	var projectID string
	for id := range b.projects {
		projectID = id
	}
	thread, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: projectID})
	if err != nil {
		t.Fatal(err)
	}
	first, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: thread.ThreadID, Text: "original prompt"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	taken, err := b.RecallTurn(ctx, thread.ThreadID)
	if err != nil || taken.Prompt != "original prompt" {
		t.Fatalf("recall: %+v %v", taken, err)
	}
	if _, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: thread.ThreadID, Text: "edited prompt"}); err != nil {
		t.Fatal(err)
	}
	<-started
	b.InterruptCurrent(ctx, thread.ThreadID)
	waitThreadIdle(t, b, ctx, thread.ThreadID)
	turns, err := s.ListTurns(ctx, thread.ThreadID)
	if err != nil || len(turns) != 2 {
		t.Fatalf("turns: %+v %v", turns, err)
	}
	if turns[0].ID != *first.TurnID || turns[0].Status != "taken_back" || turns[1].TurnIndex <= turns[0].TurnIndex {
		t.Fatalf("withdrawn turn was overwritten or its index reused: %+v", turns)
	}
	history, _, err := s.ListMessages(ctx, thread.ThreadID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range history {
		if msg.TurnID == taken.TurnID {
			t.Fatal("withdrawn turn reappeared in persisted history")
		}
	}
}

type steerStateHarness struct {
	started chan struct{}
	release chan struct{}
	text    chan string
}

func (*steerStateHarness) Kind() harness.Kind { return "steer-state" }
func (h *steerStateHarness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	emit(harness.Event{Type: harness.EventSession, SessionID: "steer-session"})
	close(h.started)
	select {
	case <-h.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}
func (h *steerStateHarness) Steer(ctx context.Context, sessionID, text string) error {
	h.text <- text
	return nil
}

func TestRuntimeRegression_SteerAndExplicitQueue(t *testing.T) {
	h := &steerStateHarness{started: make(chan struct{}), release: make(chan struct{}), text: make(chan string, 1)}
	b := runtimeBackend(t, h)
	ctx := context.Background()
	id := runtimeThread(b)
	first, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: id, Text: "start"})
	if err != nil {
		t.Fatal(err)
	}
	<-h.started
	steered, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: id, Text: "new guidance"})
	if err != nil || steered.TurnID == nil || *steered.TurnID != *first.TurnID {
		t.Fatalf("steer: %+v %v", steered, err)
	}
	if got := <-h.text; got != "new guidance" {
		t.Fatal(got)
	}
	queued, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: id, Text: "later", WhenRunning: "queue"})
	if err != nil || queued.Queued == nil || !*queued.Queued {
		t.Fatalf("queue: %+v %v", queued, err)
	}
	// 中断不会自动消费普通跟进，避免 mock 再次启动。
	b.InterruptCurrent(ctx, id)
	waitThreadIdle(t, b, ctx, id)
}

func TestRuntimeRegression_QueueEditAndBundle(t *testing.T) {
	b := runtimeBackend(t, &customRunHarness{})
	id := runtimeThread(b)
	bundle := newUUID()
	a, z := newUUID(), newUUID()
	b.mu.Lock()
	b.threads[id].followUps = []wire.FollowUp{{ID: a, Text: "first", BundleID: &bundle}, {ID: z, Text: "second", BundleID: &bundle}}
	b.mu.Unlock()
	ctx := context.Background()
	opened, err := b.QueueEdit(ctx, wire.QueueEditParams{ThreadID: id, Action: "begin", FollowUpID: a, Draft: &wire.RuntimeDraft{Text: "my draft", Attachments: []string{}}})
	if err != nil || opened.Draft.Text != "first" {
		t.Fatalf("begin: %+v %v", opened, err)
	}
	saved, err := b.QueueEdit(ctx, wire.QueueEditParams{ThreadID: id, Action: "save", Draft: &wire.RuntimeDraft{Text: "edited", Attachments: []string{}}})
	if err != nil || saved.Draft.Text != "my draft" {
		t.Fatalf("save: %+v %v", saved, err)
	}
	b.mu.Lock()
	st, text, changed := b.startNextQueuedTurnLocked(b.threads[id], true, true)
	b.mu.Unlock()
	if !changed || st == nil || text != "edited\n\nsecond" {
		t.Fatalf("bundle not consumed together: %q", text)
	}
	b.InterruptCurrent(ctx, id)
}

func TestRuntimeRegression_ProviderHandoff(t *testing.T) {
	prompts := make(chan harness.Turn, 2)
	old := &customRunHarness{kind: "old", runFn: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		emit(harness.Event{Type: harness.EventSession, SessionID: "old-session"})
		emit(harness.Event{Type: harness.EventText, Text: "old conclusion"})
		return nil
	}}
	b := runtimeBackend(t, old)
	b.RegisterHarness(&customRunHarness{kind: "new", runFn: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		prompts <- turn
		return nil
	}})
	ctx := context.Background()
	id := runtimeThread(b)
	b.SendMessage(ctx, wire.SendMessageParams{ThreadID: id, Text: "old request"})
	waitThreadIdle(t, b, ctx, id)
	provider := "new"
	if _, err := b.UpdateThread(ctx, wire.ThreadUpdateParams{ThreadID: id, Provider: &provider}); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	b.threads[id].continuation = "continuation context"
	b.mu.Unlock()
	b.AddThread(wire.ThreadSummary{ID: newUUID(), Title: "reference"}, []wire.ThreadMessage{{Role: "user", Text: "referenced goal"}})
	b.SendMessage(ctx, wire.SendMessageParams{ThreadID: id, Text: "new request @reference"})
	waitThreadIdle(t, b, ctx, id)
	turn := <-prompts
	if turn.SessionID != "" || !strings.Contains(turn.Prompt, "old conclusion") || !strings.Contains(turn.Prompt, "old request") {
		t.Fatalf("missing history handoff: %+v", turn)
	}
	if !strings.Contains(turn.Prompt, "continuation context") || !strings.Contains(turn.Prompt, "referenced goal") || !strings.Contains(turn.Prompt, "new request @reference") {
		t.Fatalf("continuation replaced mention context or the current request: %s", turn.Prompt)
	}
}

func TestRuntimeRegression_ContinuationSurvivesRecall(t *testing.T) {
	started := make(chan struct{}, 2)
	prompts := make(chan harness.Turn, 2)
	h := &customRunHarness{runFn: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		prompts <- turn
		emit(harness.Event{Type: harness.EventSession, SessionID: "continuation-session"})
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}}
	b := runtimeBackend(t, h)
	ctx := context.Background()
	id := runtimeThread(b)
	b.mu.Lock()
	b.threads[id].continuation = "continuation context"
	b.mu.Unlock()

	if _, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: id, Text: "first request"}); err != nil {
		t.Fatal(err)
	}
	<-started
	if got := (<-prompts).Prompt; !strings.Contains(got, "continuation context") {
		t.Fatalf("first turn missing the forwarded continuation: %s", got)
	}
	if _, err := b.RecallTurn(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: id, Text: "second request"}); err != nil {
		t.Fatal(err)
	}
	<-started
	if got := (<-prompts).Prompt; !strings.Contains(got, "continuation context") {
		t.Fatalf("continuation lost after take back: %s", got)
	}
	b.InterruptCurrent(ctx, id)
	waitThreadIdle(t, b, ctx, id)
}

func TestRuntimeRegression_ContinuationDeliveredOncePerSession(t *testing.T) {
	prompts := make(chan harness.Turn, 2)
	h := &customRunHarness{runFn: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		prompts <- turn
		emit(harness.Event{Type: harness.EventSession, SessionID: "stable-session"})
		emit(harness.Event{Type: harness.EventText, Text: "done"})
		return nil
	}}
	b := runtimeBackend(t, h)
	ctx := context.Background()
	id := runtimeThread(b)
	b.mu.Lock()
	b.threads[id].continuation = "continuation context"
	b.mu.Unlock()

	for _, text := range []string{"first request", "second request"} {
		if _, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: id, Text: text}); err != nil {
			t.Fatal(err)
		}
		waitThreadIdle(t, b, ctx, id)
	}
	if got := (<-prompts).Prompt; !strings.Contains(got, "continuation context") {
		t.Fatalf("first turn missing the forwarded continuation: %s", got)
	}
	if got := (<-prompts).Prompt; strings.Contains(got, "continuation context") {
		t.Fatalf("continuation repeated inside the same provider session: %s", got)
	}
	b.mu.Lock()
	kept := b.threads[id].continuation
	b.mu.Unlock()
	if kept != "continuation context" {
		t.Fatalf("continuation cleared from the thread: %q", kept)
	}
}

func TestRuntimeRegression_RecallHistoryWaitHasDeadline(t *testing.T) {
	b := runtimeBackend(t, &customRunHarness{})
	id := runtimeThread(b)
	history := make(chan struct{})
	defer close(history)
	b.mu.Lock()
	b.threads[id].historyOp = history
	b.mu.Unlock()
	b.SendMessage(context.Background(), wire.SendMessageParams{ThreadID: id, Text: "blocked starting turn"})
	timer := make(chan func(), 1)
	b.SetAfterFunc(func(delay time.Duration, f func()) func() bool {
		if delay != 10*time.Second {
			t.Errorf("recall deadline = %s", delay)
		}
		timer <- f
		return func() bool { return true }
	})
	result := make(chan error, 1)
	go func() { _, err := b.RecallTurn(context.Background(), id); result <- err }()
	select {
	case fire := <-timer:
		fire()
	case <-time.After(time.Second):
		t.Fatal("recall did not install deadline")
	}
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "timeout") {
			t.Fatalf("expected history timeout, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("history wait ignored recall deadline")
	}
}

type watchdogStateHarness struct {
	started chan struct{}
	forced  chan string
	release chan struct{}
}

func (*watchdogStateHarness) Kind() harness.Kind { return "watchdog-state" }
func (h *watchdogStateHarness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	emit(harness.Event{Type: harness.EventText, Text: "partial response"})
	close(h.started)
	<-h.release
	emit(harness.Event{Type: harness.EventText, Text: "zombie output"})
	return nil
}
func (h *watchdogStateHarness) ForceStopTurn(turnID string) {
	h.forced <- turnID
	close(h.release)
}

func TestRuntimeRegression_WatchdogForcesProviderAtEightSeconds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := &watchdogStateHarness{started: make(chan struct{}), forced: make(chan string, 1), release: make(chan struct{})}
	s, err := store.Open(filepath.Join(t.TempDir(), "watchdog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	b := runtimeBackend(t, h)
	ctx := context.Background()
	if err := b.AttachStore(ctx, s); err != nil {
		t.Fatal(err)
	}
	projectID := newUUID()
	b.AddProject(wire.ProjectSummary{ID: projectID, Name: "watchdog"})
	thread, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: projectID})
	if err != nil {
		t.Fatal(err)
	}
	var fire func()
	timers := 0
	b.SetAfterFunc(func(delay time.Duration, f func()) func() bool {
		if delay != 8*time.Second {
			t.Errorf("watchdog delay = %s, want 8s", delay)
		}
		fire = f
		timers++
		return func() bool { return true }
	})
	id := thread.ThreadID
	sent, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: id, Text: "run"})
	if err != nil {
		t.Fatal(err)
	}
	<-h.started
	b.InterruptCurrent(ctx, id)
	b.InterruptCurrent(ctx, id)
	if timers != 1 {
		t.Fatalf("repeated interrupts restarted watchdog %d times", timers)
	}
	fire()
	select {
	case stopped := <-h.forced:
		if stopped != *sent.TurnID {
			t.Fatal("wrong turn stopped")
		}
	case <-time.After(time.Second):
		t.Fatal("watchdog did not force-stop provider")
	}
	b.Close()
	history, _ := b.ThreadHistory(ctx, wire.HistoryParams{ThreadID: id})
	for _, msg := range history.Messages {
		if strings.Contains(msg.Text, "zombie output") {
			t.Fatal("zombie output accepted after force-stop")
		}
	}
	messages, _, err := s.ListMessages(ctx, id, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var partialSaved bool
	for _, msg := range messages {
		partialSaved = partialSaved || msg.Text == "partial response"
	}
	if !partialSaved {
		t.Fatal("watchdog dropped the partial response from persisted history")
	}
	record, err := s.GetTurn(ctx, *sent.TurnID)
	if err != nil || record.Status != "interrupted" || record.CompletedAt == nil {
		t.Fatalf("watchdog terminal record: %+v %v", record, err)
	}
}

func TestRuntimeRegression_ControlRPCRoutes(t *testing.T) {
	b := runtimeBackend(t, &customRunHarness{})
	id := runtimeThread(b)
	d := NewDispatcher(&fakeAuth{}, b)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}
	for _, method := range []string{"threads.turn.recall", "threads.provider.limitText", "threads.compact", "threads.queue.edit"} {
		if !d.methods[method] {
			t.Fatalf("%s missing from capability router", method)
		}
		params := map[string]any{"threadID": id}
		if method == "threads.queue.edit" {
			params["action"] = "cancel"
		}
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		response := d.handleSingle(context.Background(), conn, raw)
		if response == nil || (response.Error != nil && response.Error.Code == wire.MethodNotFound) {
			t.Fatalf("%s is unreachable: %+v", method, response)
		}
	}
}

func TestRuntimeRegression_CompactSuggestionAndLimitText(t *testing.T) {
	b := runtimeBackend(t, &customRunHarness{})
	id := runtimeThread(b)
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	b.SetClock(func() time.Time { return now })
	var offer atomic.Int32
	b.hub.AddObserver(func(_ string, method string, params any) {
		if method == "threads.compactSuggestion" {
			offer.Add(1)
		}
	})
	b.mu.Lock()
	td := b.threads[id]
	td.sessionID = "idle-session"
	td.summary.UpdatedAt = now.Add(-2 * time.Hour).Format(time.RFC3339Nano)
	td.messages = []wire.ThreadMessage{{Role: "user", Text: "last request"}, {Role: "tool", Text: "already ran tests"}}
	b.usage[id] = map[int]harness.Usage{0: {InputTokens: 100001}}
	b.mu.Unlock()
	b.ThreadHistory(context.Background(), wire.HistoryParams{ThreadID: id})
	if offer.Load() != 1 {
		t.Fatal("no compact offer for long-idle high-token session")
	}
	text, err := b.LimitHandoffText(context.Background(), id)
	if err != nil || !strings.Contains(text.Text, "last request") || !strings.Contains(text.Text, "already ran tests") {
		t.Fatalf("limit handoff: %+v %v", text, err)
	}
}

func TestRuntimeRegression_PersistCheckpointFilesTokensAndError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	root := t.TempDir()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is unavailable")
	}
	if out, err := exec.Command(gitBin, "init", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(root, "existing.txt"), []byte("pre-existing change"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(filepath.Join(t.TempDir(), "turns.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	b := runtimeBackend(t, &customRunHarness{runFn: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		if err := os.WriteFile(filepath.Join(turn.WorkDir, "changed.txt"), []byte("turn change"), 0600); err != nil {
			return err
		}
		emit(harness.Event{Type: harness.EventUsage, Usage: &harness.Usage{InputTokens: 10, OutputTokens: 4}})
		return errors.New("authentication failed")
	}})
	if err := b.AttachStore(ctx, s); err != nil {
		t.Fatal(err)
	}
	b.SetGitRunner(git.NewRunner(gitBin, 2))
	project := newUUID()
	b.AddProject(wire.ProjectSummary{ID: project, Name: "checkpoints", Path: root})
	thread, err := b.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: project})
	if err != nil {
		t.Fatal(err)
	}
	sent, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: thread.ThreadID, Text: "modify"})
	if err != nil {
		t.Fatal(err)
	}
	waitThreadIdle(t, b, ctx, thread.ThreadID)
	record, err := s.GetTurn(ctx, *sent.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if record.BaseCheckpoint == "" || record.FinishCheckpoint == "" || record.BaseCheckpoint == record.FinishCheckpoint {
		t.Fatalf("missing actual start/end snapshots: %+v", record)
	}
	if record.Status != "failed" || record.ErrorMessage != "authentication failed" || record.InputTokens != 10 || record.OutputTokens != 4 || record.CompletedAt == nil {
		t.Fatalf("incomplete terminal record: %+v", record)
	}
	if len(record.TouchedFiles) != 1 || record.TouchedFiles[0] != "changed.txt" {
		t.Fatalf("pre-existing dirty files leaked into turn diff: %+v", record.TouchedFiles)
	}
}
