package remote

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// boolPtr 是测试辅助函数。
func testBoolPtr(b bool) *bool {
	return &b
}

// TestSessionSettings_ConciseRepliesInjection 测试 ConciseReplies 开启时注入引导，关闭时不注入，以及与 PromptDecorator 共存时的顺序。
func TestSessionSettings_ConciseRepliesInjection(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	var receivedPrompt string
	var promptMu sync.Mutex

	mock := &mockHarness{
		kind: "mock",
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			promptMu.Lock()
			receivedPrompt = turn.Prompt
			promptMu.Unlock()
			emit(harness.Event{Type: harness.EventText, Text: "ok"})
			emit(harness.Event{Type: harness.EventDone, StopReason: "completed"})
			return nil
		},
	}
	backend.RegisterHarness(mock)

	// 1. 默认状态（未配置或 ConciseRepliesEnabled 为 nil / true）：应该注入 ConciseRepliesPrompt
	thID := "thread-concise-replies-1"
	backend.AddThread(wire.ThreadSummary{
		ID:       thID,
		Title:    "ConciseReplies Test 1",
		Provider: "mock",
	}, nil)

	_, err := backend.SendMessage(context.Background(), wire.SendMessageParams{
		ThreadID: thID,
		Text:     "请帮忙写一段代码",
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	promptMu.Lock()
	p1 := receivedPrompt
	promptMu.Unlock()

	if !strings.HasPrefix(p1, "[简洁回复与专注执行引导]") {
		t.Errorf("expected prompt to start with ConciseReplies guidance, got: %s", p1)
	}
	if !strings.Contains(p1, "请帮忙写一段代码") {
		t.Errorf("expected prompt to contain user text, got: %s", p1)
	}

	// 2. 测试与 PromptDecorator 共存的顺序：ConciseReplies 在最前，接着是 Decorator 注入，最后是用户提示词
	backend.SetPromptDecorator(func(threadID, prompt string) string {
		return "[Swarm Policy]\n\n" + prompt
	})

	thID2 := "thread-concise-replies-2"
	backend.AddThread(wire.ThreadSummary{
		ID:       thID2,
		Title:    "ConciseReplies Test 2",
		Provider: "mock",
	}, nil)

	_, err = backend.SendMessage(context.Background(), wire.SendMessageParams{
		ThreadID: thID2,
		Text:     "实现一个功能",
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	promptMu.Lock()
	p2 := receivedPrompt
	promptMu.Unlock()

	expectedPrefix := ConciseRepliesPrompt + "\n\n[Swarm Policy]\n\n"
	if !strings.HasPrefix(p2, expectedPrefix) {
		t.Errorf("expected ConciseReplies guidance before Swarm Policy and user prompt, got: %s", p2)
	}

	// 3. 关闭 ConciseReplies：不注入 ConciseRepliesPrompt
	backend.SetGeneralSettingsFunc(func() config.Config {
		return config.Config{Session: config.SessionConfig{
			ConciseRepliesEnabled: testBoolPtr(false),
		}}
	})

	thID3 := "thread-concise-replies-3"
	backend.AddThread(wire.ThreadSummary{
		ID:       thID3,
		Title:    "ConciseReplies Test 3",
		Provider: "mock",
	}, nil)

	_, err = backend.SendMessage(context.Background(), wire.SendMessageParams{
		ThreadID: thID3,
		Text:     "关闭后的提示",
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	promptMu.Lock()
	p3 := receivedPrompt
	promptMu.Unlock()

	if strings.Contains(p3, "[简洁回复与专注执行引导]") {
		t.Errorf("expected no ConciseReplies guidance when disabled, got: %s", p3)
	}
	if !strings.HasPrefix(p3, "[Swarm Policy]\n\n") {
		t.Errorf("expected Swarm Policy at front when ConciseReplies is disabled, got: %s", p3)
	}
}

// TestSessionSettings_UsageLimitRecognition 测试额度错误识别及重置时间解析。
func TestSessionSettings_UsageLimitRecognition(t *testing.T) {
	// 1. isUsageLimitError 关键字匹配
	validErrors := []string{
		"Claude: usage limit reached for today",
		"You have hit your limit on fast requests",
		"error code: usage_limit_exceeded",
		"Monthly spend limit reached",
		"Account's rate limit reached",
		"HTTP 429 Too Many Requests",
		"status 402 Payment Required: out of credits",
		"Resource_exhausted: quota will reset soon",
		"insufficient_quota: exhausted your capacity",
	}
	for _, text := range validErrors {
		if !isUsageLimitError(text) {
			t.Errorf("expected %q to be recognized as usage limit error", text)
		}
	}

	invalidErrors := []string{
		"syntax error: unexpected token",
		"connection reset by peer",
		"context canceled",
		"file not found: main.go",
	}
	for _, text := range invalidErrors {
		if isUsageLimitError(text) {
			t.Errorf("expected %q NOT to be recognized as usage limit error", text)
		}
	}

	// 2. parseResetTime 时间解析
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	// a. Unix 时间戳
	tsText := "usage limit reached | 1760702400"
	target, _, ok := parseResetTime(tsText, now)
	if !ok || target.Unix() != 1760702400 {
		t.Errorf("timestamp parsing failed, got %v (ok=%v)", target, ok)
	}

	// b. retry-after-ms 毫秒
	msText := "Provider requested retry-after-ms: 30000 wait"
	target, _, ok = parseResetTime(msText, now)
	if !ok || target.Sub(now) != 30*time.Second {
		t.Errorf("retry-after-ms parsing failed, diff: %v", target.Sub(now))
	}

	// c. 自然语言时长
	durText := "Please try again in ~158 min"
	target, _, ok = parseResetTime(durText, now)
	if !ok || target.Sub(now) != 158*time.Minute {
		t.Errorf("duration min parsing failed, diff: %v", target.Sub(now))
	}

	// d. 复合时长
	durText2 := "resets after 18h31m10s"
	target, _, ok = parseResetTime(durText2, now)
	expectedSec := 18*3600 + 31*60 + 10
	if !ok || target.Sub(now) != time.Duration(expectedSec)*time.Second {
		t.Errorf("compound duration parsing failed, diff: %v", target.Sub(now))
	}

	// e. 默认值
	target, delay, ok := parseResetTime("quota exceeded", now)
	if ok || delay != 60*time.Second {
		t.Errorf("expected fallback to 60s default, got delay=%v, ok=%v", delay, ok)
	}
}

// TestSessionSettings_AutoContinueAndCancel 测试开启 AutoContinueAfterLimit 时额度用尽自动续跑，以及用户发送/中断时取消等待。
func TestSessionSettings_AutoContinueAndCancel(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	backend.SetGeneralSettingsFunc(func() config.Config {
		return config.Config{Session: config.SessionConfig{
			AutoContinueAfterLimit: true,
		}}
	})

	var turnCount int
	var turnMu sync.Mutex

	mock := &mockHarness{
		kind: "mock",
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			turnMu.Lock()
			turnCount++
			count := turnCount
			turnMu.Unlock()

			if count == 1 {
				// 第一轮因额度耗尽失败
				return errors.New("usage limit reached: please retry in 100ms")
			}
			// 第二轮（续跑）成功
			emit(harness.Event{Type: harness.EventText, Text: "resumed work completed"})
			emit(harness.Event{Type: harness.EventDone, StopReason: "completed"})
			return nil
		},
	}
	backend.RegisterHarness(mock)

	thID := "thread-auto-continue-1"
	backend.AddThread(wire.ThreadSummary{
		ID:       thID,
		Title:    "Auto Continue Test",
		Provider: "mock",
	}, nil)

	// 1. 发送消息，第一轮失败，进入自动续跑排程
	_, err := backend.SendMessage(context.Background(), wire.SendMessageParams{
		ThreadID: thID,
		Text:     "开始执行长任务",
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	backend.mu.Lock()
	waitActive := backend.autoContinueWaits[thID] != nil
	backend.mu.Unlock()

	if !waitActive {
		t.Fatalf("expected autoContinueWait to be active after usage limit failure")
	}

	// 2. 测试取消：用户主动发消息时，取消自动续跑排程
	backend.cancelAutoContinue(thID)

	backend.mu.Lock()
	waitActiveAfterCancel := backend.autoContinueWaits[thID] != nil
	backend.mu.Unlock()

	if waitActiveAfterCancel {
		t.Errorf("expected autoContinueWait to be cancelled")
	}
}

// TestSessionSettings_FinishThreadBranches 测试 FinishThread 按设置在 dormant 与 archive 之间正确分支。
func TestSessionSettings_FinishThreadBranches(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()

	// 1. 设置为 dormant：普通会话在 dormant 与 reopen 间切换
	backend.SetGeneralSettingsFunc(func() config.Config {
		return config.Config{Session: config.SessionConfig{
			ThreadFinishAction: "dormant",
		}}
	})

	thID1 := "thread-finish-settle"
	backend.AddThread(wire.ThreadSummary{
		ID:        thID1,
		Title:     "Settle Test",
		Provider:  "memory",
		IsDormant: false,
	}, nil)

	// 初次调用：未休眠 -> 休眠
	if err := backend.FinishThread(ctx, thID1); err != nil {
		t.Fatalf("FinishThread dormant failed: %v", err)
	}
	th, _ := backend.GetThread(ctx, thID1)
	if !th.IsDormant {
		t.Errorf("expected thread to be dormant")
	}
	if th.IsArchived {
		t.Errorf("expected thread NOT to be archived")
	}

	// 二次调用：已休眠 -> reopen (IsDormant = false)
	if err := backend.FinishThread(ctx, thID1); err != nil {
		t.Fatalf("FinishThread reopen failed: %v", err)
	}
	th, _ = backend.GetThread(ctx, thID1)
	if th.IsDormant {
		t.Errorf("expected thread to be reopened (IsDormant=false)")
	}

	// 2. 设置为 archive：直接归档
	backend.SetGeneralSettingsFunc(func() config.Config {
		return config.Config{Session: config.SessionConfig{
			ThreadFinishAction: "archive",
		}}
	})

	thID2 := "thread-finish-archive"
	backend.AddThread(wire.ThreadSummary{
		ID:         thID2,
		Title:      "Archive Test",
		Provider:   "memory",
		IsArchived: false,
	}, nil)

	if err := backend.FinishThread(ctx, thID2); err != nil {
		t.Fatalf("FinishThread archive failed: %v", err)
	}
	th, _ = backend.GetThread(ctx, thID2)
	if !th.IsArchived {
		t.Errorf("expected thread to be archived")
	}

	// 3. 辅助会话判断：根据 IsHelper 或 ParentThreadID 判断
	thID3 := "thread-finish-helper"
	s3 := wire.ThreadSummary{
		ID:         thID3,
		Title:      "Helper Test",
		Provider:   "memory",
		IsArchived: false,
	}
	backend.AddThread(s3, nil)

	if isHelperThread(s3) {
		t.Errorf("expected isHelperThread to return false for normal thread")
	}

	parentID := "thread-parent"
	helperByFlag := wire.ThreadSummary{
		ID:       "helper-flag",
		IsHelper: true,
	}
	if !isHelperThread(helperByFlag) {
		t.Errorf("expected isHelperThread to return true when IsHelper is true")
	}

	helperByParent := wire.ThreadSummary{
		ID:             "helper-parent",
		ParentThreadID: &parentID,
	}
	if !isHelperThread(helperByParent) {
		t.Errorf("expected isHelperThread to return true when ParentThreadID is set")
	}

	// 4. 不存在的会话返回 store.ErrNotFound
	if err := backend.FinishThread(ctx, "non-existent-thread"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected store.ErrNotFound for non-existent thread, got: %v", err)
	}
}

// TestSessionSettings_QueueWhileRunning 测试 QueueWhileRunning 打开时，运行中发送消息退回到排队而非报错拒绝。
func TestSessionSettings_QueueWhileRunning(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	// 阻塞型 harness，使会话维持在 isRunning 状态
	blockCh := make(chan struct{})
	defer close(blockCh)

	mock := &mockHarness{
		kind: "mock",
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventText, Text: "working..."})
			<-blockCh
			emit(harness.Event{Type: harness.EventDone, StopReason: "completed"})
			return nil
		},
	}
	backend.RegisterHarness(mock)

	thID := "thread-queue-running"
	backend.AddThread(wire.ThreadSummary{
		ID:       thID,
		Title:    "QueueWhileRunning Test",
		Provider: "mock",
	}, nil)

	// 启动第一轮
	_, err := backend.SendMessage(context.Background(), wire.SendMessageParams{
		ThreadID: thID,
		Text:     "任务 1",
	})
	if err != nil {
		t.Fatalf("SendMessage 1 failed: %v", err)
	}

	time.Sleep(20 * time.Millisecond)

	// 1. 当 QueueWhileRunning 为 false（默认）：运行中发消息应报错拒绝
	backend.SetGeneralSettingsFunc(func() config.Config {
		return config.Config{Session: config.SessionConfig{QueueWhileRunning: false}}
	})

	_, err = backend.SendMessage(context.Background(), wire.SendMessageParams{
		ThreadID: thID,
		Text:     "任务 2 (默认关闭)",
	})
	if err == nil {
		t.Errorf("expected SendMessage to fail when thread is already running and QueueWhileRunning=false")
	}

	// 2. 当 QueueWhileRunning 为 true：运行中发消息应自动排队入队，返回 Queued: true
	backend.SetGeneralSettingsFunc(func() config.Config {
		return config.Config{Session: config.SessionConfig{QueueWhileRunning: true}}
	})

	res, err := backend.SendMessage(context.Background(), wire.SendMessageParams{
		ThreadID: thID,
		Text:     "任务 3 (开启排队)",
	})
	if err != nil {
		t.Fatalf("SendMessage with QueueWhileRunning=true failed: %v", err)
	}
	if res.Queued == nil || !*res.Queued {
		t.Errorf("expected res.Queued to be true, got: %+v", res.Queued)
	}
	if res.FollowUpID == nil {
		t.Errorf("expected res.FollowUpID to be set for queued message")
	}

	// 3. 当 QueueWhileRunning 为 true 时，QueueInterruptAndSend 也会退回排队而非打断轮次
	qRes, err := backend.QueueInterruptAndSend(context.Background(), wire.ThreadsQueueInterruptAndSendParams{
		ThreadID: thID,
		Text:     testStrPtr("任务 4 (InterruptAndSend)"),
	})
	if err != nil {
		t.Fatalf("QueueInterruptAndSend failed: %v", err)
	}
	if qRes.State != wire.ThreadsQueueInterruptStateQueued {
		t.Errorf("expected State to be queued, got: %s", qRes.State)
	}
}

// TestSessionSettings_InsertIntoRunningTurnFallsBackToQueue 验证开启「运行中的消息插入当前回合」后，
// Provider 未实现 harness.Steerer 时，运行中的普通消息退回排队而不是被拒绝。
func TestSessionSettings_InsertIntoRunningTurnFallsBackToQueue(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	started := make(chan struct{}, 1)
	mock := &mockHarness{
		kind: "mock",
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			select {
			case started <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return ctx.Err()
		},
	}
	backend.RegisterHarness(mock)

	thID := "thread-insert-running-turn"
	backend.AddThread(wire.ThreadSummary{
		ID:       thID,
		Title:    "InsertIntoRunningTurn Test",
		Provider: "mock",
	}, nil)

	ctx := context.Background()
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: thID, Text: "任务 1"}); err != nil {
		t.Fatalf("SendMessage 1 failed: %v", err)
	}
	<-started

	// 只开启插入开关，不开启 QueueWhileRunning：Provider 没有中途注入能力时仍应退回排队。
	backend.SetGeneralSettingsFunc(func() config.Config {
		return config.Config{Session: config.SessionConfig{InsertIntoRunningTurn: true}}
	})

	res, err := backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: thID, Text: "任务 2"})
	if err != nil {
		t.Fatalf("SendMessage with InsertIntoRunningTurn=true failed: %v", err)
	}
	if res.Queued == nil || !*res.Queued || res.FollowUpID == nil {
		t.Fatalf("expected running message to be queued when provider does not implement Steerer, got %+v", res)
	}

	backend.InterruptCurrent(ctx, thID)
	waitThreadIdle(t, backend, ctx, thID)
}

func testStrPtr(s string) *string {
	return &s
}

// TestSessionSettings_ShouldOfferCompact 测试 ShouldOfferCompact 判断逻辑。
func TestSessionSettings_ShouldOfferCompact(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	thID := "thread-compact-offer"
	pastTime := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339Nano)

	backend.AddThread(wire.ThreadSummary{
		ID:        thID,
		Title:     "Compact Offer Test",
		Provider:  "claude",
		UpdatedAt: pastTime,
	}, nil)

	// 设置 sessionID 与使用量
	backend.mu.Lock()
	backend.threads[thID].sessionID = "sess-12345"
	if backend.usage == nil {
		backend.usage = make(map[string]map[int]harness.Usage)
	}
	backend.usage[thID] = map[int]harness.Usage{
		0: {InputTokens: 60000, OutputTokens: 50000}, // 110,000 tokens >= 100,000
	}
	backend.mu.Unlock()

	// 1. 默认设置下（AskCompactBeforeResume 为 nil 默认开启）：应返回 true
	if !backend.ShouldOfferCompact(thID) {
		t.Errorf("expected ShouldOfferCompact to return true")
	}

	// 2. 如果 AskCompactBeforeResume 关闭：应返回 false
	backend.SetGeneralSettingsFunc(func() config.Config {
		return config.Config{Session: config.SessionConfig{
			AskCompactBeforeResume: testBoolPtr(false),
		}}
	})
	if backend.ShouldOfferCompact(thID) {
		t.Errorf("expected ShouldOfferCompact to return false when AskCompactBeforeResume is false")
	}

	// 3. 恢复开启，但 tokens 不足 100,000：应返回 false
	backend.SetGeneralSettingsFunc(func() config.Config {
		return config.Config{Session: config.SessionConfig{
			AskCompactBeforeResume: testBoolPtr(true),
		}}
	})
	backend.mu.Lock()
	backend.usage[thID] = map[int]harness.Usage{
		0: {InputTokens: 20000, OutputTokens: 10000}, // 30,000 tokens < 100,000
	}
	backend.mu.Unlock()

	if backend.ShouldOfferCompact(thID) {
		t.Errorf("expected ShouldOfferCompact to return false when tokens < 100,000")
	}

	// 4. 最新一轮带上下文窗口信息：上下文 25k / 窗口 32k，达到 70%（22.4k）阈值，空闲 2 小时：应返回 true
	backend.mu.Lock()
	backend.usage[thID] = map[int]harness.Usage{
		0: {InputTokens: 20000, OutputTokens: 10000},
		1: {InputTokens: 24000, OutputTokens: 1000, ContextTokens: 25000, ContextWindow: 32000},
	}
	backend.mu.Unlock()

	if !backend.ShouldOfferCompact(thID) {
		t.Errorf("expected ShouldOfferCompact to return true for context 25k / window 32k")
	}

	// 5. 上下文占 20k、窗口 32k（未达 22.4k）：应返回 false
	backend.mu.Lock()
	backend.usage[thID] = map[int]harness.Usage{
		0: {InputTokens: 20000, OutputTokens: 10000},
		1: {InputTokens: 19000, OutputTokens: 1000, ContextTokens: 20000, ContextWindow: 32000},
	}
	backend.mu.Unlock()

	if backend.ShouldOfferCompact(thID) {
		t.Errorf("expected ShouldOfferCompact to return false for context 20k / window 32k")
	}

	// 6. 同一份上下文用量、但缺少窗口字段：退回累计口径（30,000 < 100,000）：应返回 false
	backend.mu.Lock()
	backend.usage[thID] = map[int]harness.Usage{
		0: {InputTokens: 20000, OutputTokens: 10000},
		1: {InputTokens: 19000, OutputTokens: 1000, ContextTokens: 20000},
	}
	backend.mu.Unlock()

	if backend.ShouldOfferCompact(thID) {
		t.Errorf("expected ShouldOfferCompact to return false when context window is missing")
	}
}

// TestCompactSuggestionThreshold 测试后端侧压缩建议阈值的窗口换算。
func TestCompactSuggestionThreshold(t *testing.T) {
	tests := []struct {
		name   string
		window int64
		want   int64
	}{
		{"窗口未知时用固定 100k", 0, 100000},
		{"32k 窗口取 70%", 32000, 22400},
		{"200k 窗口封顶 100k", 200000, 100000},
		{"1M 窗口封顶 100k", 1000000, 100000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := compactSuggestionThreshold(tc.window); got != tc.want {
				t.Errorf("compactSuggestionThreshold(%d) = %d, want %d", tc.window, got, tc.want)
			}
		})
	}
}
