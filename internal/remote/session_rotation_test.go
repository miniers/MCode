package remote

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
	"mcode/internal/testenv"
)

const rotationGuidance = "<rotation_guidance>"

// rotationTestBackend 构建带 store 的后端与线程；mock 每轮回一条正文并上报用量，
// withSession 为 true 时仅在第一次调用时建立 provider 会话 sess-1。
func rotationTestBackend(t *testing.T, withSession bool) (*MemoryBackend, *store.Store, *mockHarness, string) {
	t.Helper()
	testenv.TempHome(t)
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	t.Cleanup(hub.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "rotation.db"))
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var calls atomic.Int32
	mock := &mockHarness{runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		n := calls.Add(1)
		if withSession && n == 1 {
			emit(harness.Event{Type: harness.EventSession, SessionID: "sess-1"})
		}
		emit(harness.Event{Type: harness.EventUsage, Usage: &harness.Usage{InputTokens: 100, OutputTokens: 10, ContextTokens: 130000}})
		emit(harness.Event{Type: harness.EventText, Text: fmt.Sprintf("第 %d 轮回复", n)})
		return nil
	}}

	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	backend.SetHarness(mock, t.TempDir(), harness.ModeAgent)
	if err := backend.AttachStore(ctx, st); err != nil {
		t.Fatalf("AttachStore 失败: %v", err)
	}
	backend.SetGuidanceBuilder(func(string) string { return rotationGuidance })
	// 关掉默认开启的简洁回复引导，让 Prompt 开头就是本轮组装的内容。
	concise := false
	backend.SetGeneralSettingsFunc(func() config.Config {
		return config.Config{Session: config.SessionConfig{ConciseRepliesEnabled: &concise}}
	})
	backend.AddProject(wire.ProjectSummary{ID: "p_rotation", Path: t.TempDir()})
	created, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p_rotation"})
	if err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}
	return backend, st, mock, created.ThreadID
}

// runTwoTurns 发两条消息并返回 harness 收到的两次调用。
func runTwoTurns(t *testing.T, backend *MemoryBackend, mock *mockHarness, threadID string) []harness.Turn {
	t.Helper()
	sendGuidanceMessage(t, backend, threadID, "第一条消息", "00000000-0000-0000-0000-000000000501")
	sendGuidanceMessage(t, backend, threadID, "第二条消息", "00000000-0000-0000-0000-000000000502")
	calls := mock.getTurnCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 harness calls, got %d", len(calls))
	}
	return calls
}

// assertResumed 断言第二轮照常续接：带旧会话 ID，Prompt 不含换新内容也不重复 guidance。
func assertResumed(t *testing.T, second harness.Turn) {
	t.Helper()
	if second.SessionID != "sess-1" {
		t.Fatalf("第二轮应续接 sess-1, got %q", second.SessionID)
	}
	if strings.Contains(second.Prompt, "[mcode: recent conversation]") || strings.Contains(second.Prompt, rotationGuidance) {
		t.Fatalf("续接轮次不应带最近对话或重复 guidance, got %q", second.Prompt)
	}
	if !strings.HasSuffix(second.Prompt, "第二条消息") {
		t.Fatalf("续接轮次应以用户文本结尾, got %q", second.Prompt)
	}
}

func TestSessionRotation_NilRotatorKeepsResume(t *testing.T) {
	backend, _, mock, threadID := rotationTestBackend(t, true)
	calls := runTwoTurns(t, backend, mock, threadID)
	assertResumed(t, calls[1])
}

func TestSessionRotation_FalseKeepsResume(t *testing.T) {
	backend, _, mock, threadID := rotationTestBackend(t, true)
	var (
		mu    sync.Mutex
		asked []SessionRotationInfo
	)
	backend.SetSessionRotator(func(ctx context.Context, info SessionRotationInfo) (bool, string) {
		mu.Lock()
		asked = append(asked, info)
		mu.Unlock()
		return false, "不该出现的前言"
	})
	calls := runTwoTurns(t, backend, mock, threadID)
	assertResumed(t, calls[1])

	mu.Lock()
	defer mu.Unlock()
	// 第一轮还没有会话 ID，不问；第二轮要续接，问一次。
	if len(asked) != 1 {
		t.Fatalf("钩子应只被询问 1 次, got %d", len(asked))
	}
	if asked[0].ThreadID != threadID || asked[0].ContextTokens != 130000 || asked[0].Idle < 0 {
		t.Fatalf("钩子收到的会话现状不对: %+v", asked[0])
	}
}

func TestSessionRotation_TrueStartsFreshSession(t *testing.T) {
	backend, st, mock, threadID := rotationTestBackend(t, true)
	const preamble = "前言：你在接手一段旧对话。"
	backend.SetSessionRotator(func(ctx context.Context, info SessionRotationInfo) (bool, string) {
		return true, preamble
	})
	calls := runTwoTurns(t, backend, mock, threadID)

	second := calls[1]
	if second.SessionID != "" {
		t.Fatalf("换新轮次不应带旧会话 ID, got %q", second.SessionID)
	}
	prompt := second.Prompt
	if !strings.HasPrefix(prompt, preamble) {
		t.Fatalf("换新轮次应以前言开头, got %q", prompt)
	}
	if strings.Contains(prompt, "[mcode: history from before the agent switch]") {
		t.Fatalf("换新轮次不应叠加整段历史交接, got %q", prompt)
	}
	guidanceAt := strings.Index(prompt, rotationGuidance)
	recentAt := strings.Index(prompt, "[mcode: recent conversation]")
	if guidanceAt < 0 || recentAt < 0 || guidanceAt > recentAt {
		t.Fatalf("换新轮次应重新注入 guidance 并带最近对话, got %q", prompt)
	}
	if !strings.Contains(prompt, "User: 第一条消息") || !strings.Contains(prompt, "Assistant: 第 1 轮回复") {
		t.Fatalf("最近对话应保留原文, got %q", prompt)
	}
	if !strings.HasSuffix(prompt, "第二条消息") {
		t.Fatalf("换新轮次应以用户文本结尾, got %q", prompt)
	}

	th, err := st.GetThread(context.Background(), threadID)
	if err != nil {
		t.Fatalf("GetThread 失败: %v", err)
	}
	if th.SessionID != "" {
		t.Fatalf("数据库里的会话 ID 应已清空, got %q", th.SessionID)
	}

	backend.mu.Lock()
	var noticed bool
	for _, msg := range backend.threads[threadID].messages {
		if msg.Role == "system" && strings.HasPrefix(msg.Text, "已换新会话") {
			noticed = true
		}
	}
	backend.mu.Unlock()
	if !noticed {
		t.Fatal("换新后应在会话里留一条系统提示")
	}
}

func TestSessionRotation_NoSessionSkipsRotator(t *testing.T) {
	backend, _, mock, threadID := rotationTestBackend(t, false)
	var asked atomic.Int32
	backend.SetSessionRotator(func(ctx context.Context, info SessionRotationInfo) (bool, string) {
		asked.Add(1)
		return true, "前言"
	})
	runTwoTurns(t, backend, mock, threadID)
	if n := asked.Load(); n != 0 {
		t.Fatalf("没有会话 ID 时不应询问钩子, got %d", n)
	}
}

func TestSessionRotation_TimeoutKeepsResume(t *testing.T) {
	old := sessionRotatorTimeout
	sessionRotatorTimeout = 50 * time.Millisecond
	t.Cleanup(func() { sessionRotatorTimeout = old })

	backend, _, mock, threadID := rotationTestBackend(t, true)
	backend.SetSessionRotator(func(ctx context.Context, info SessionRotationInfo) (bool, string) {
		<-ctx.Done()
		return true, "超时后的前言"
	})
	calls := runTwoTurns(t, backend, mock, threadID)
	assertResumed(t, calls[1])
}

func TestSessionRotation_PanicKeepsResume(t *testing.T) {
	backend, _, mock, threadID := rotationTestBackend(t, true)
	backend.SetSessionRotator(func(ctx context.Context, info SessionRotationInfo) (bool, string) {
		panic("策略出错")
	})
	calls := runTwoTurns(t, backend, mock, threadID)
	assertResumed(t, calls[1])
}

func TestBuildRecentConversationPromptLimits(t *testing.T) {
	var history []wire.ThreadMessage
	for i := 0; i < 8; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		history = append(history, wire.ThreadMessage{Role: role, Text: fmt.Sprintf("消息%d", i) + strings.Repeat("长", 3000)})
	}
	got := buildRecentConversationPrompt(history, "本轮")
	if !strings.HasSuffix(got, "本轮") {
		t.Fatalf("应以本轮文本结尾, got %q", got)
	}
	if strings.Contains(got, "消息0") || strings.Contains(got, "消息4") {
		t.Fatalf("超出条数或总量的更早消息应被舍弃")
	}
	if !strings.Contains(got, "消息7") || !strings.Contains(got, "消息5") {
		t.Fatalf("最新消息应保留")
	}
	if strings.Contains(got, strings.Repeat("长", 2401)) {
		t.Fatalf("单条正文不应超过 2400 rune")
	}
}
