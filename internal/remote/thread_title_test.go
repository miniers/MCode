package remote

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// (a) 首次发消息后标题等于截取结果
func TestThreadTitle_FirstMessage(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	projID := "proj-title-a"
	backend.AddProject(wire.ProjectSummary{ID: projID, Name: "Title Project A", Path: "/path/a"})

	res, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: projID})
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	thID := res.ThreadID

	thBefore, err := backend.GetThread(ctx, thID)
	if err != nil {
		t.Fatalf("get thread before send: %v", err)
	}
	if thBefore.Title != defaultThreadTitle {
		t.Fatalf("expected initial title %q, got %q", defaultThreadTitle, thBefore.Title)
	}

	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: thID,
		Text:     "请帮我实现一个LRU缓存",
	})
	if err != nil {
		t.Fatalf("send message: %v", err)
	}

	waitThreadIdle(t, backend, ctx, thID)

	thAfter, err := backend.GetThread(ctx, thID)
	if err != nil {
		t.Fatalf("get thread after send: %v", err)
	}
	wantTitle := "请帮我实现一个LRU缓存"
	if thAfter.Title != wantTitle {
		t.Errorf("expected title %q, got %q", wantTitle, thAfter.Title)
	}
}

// (b) 多行和超过 40 字的中文消息截取正确
func TestThreadTitle_MultilineAndTruncation(t *testing.T) {
	// 1. 纯函数逻辑验证
	longChineseText := "\n\n  这是一段测试中文消息，第一行超过四十个字符了，用来测试标题截取功能是否正常工作，看看末尾会不会加上省略号呢？  \n第二行内容应该被忽略\n第三行内容也是"
	got := generateThreadTitle(longChineseText)

	// 第一行去掉首尾空白后为：
	// "这是一段测试中文消息，第一行超过四十个字符了，用来测试标题截取功能是否正常工作，看看末尾会不会加上省略号呢？" (54个字符)
	// 前 40 个字符加 "…"
	runes := []rune("这是一段测试中文消息，第一行超过四十个字符了，用来测试标题截取功能是否正常工作，看看末尾会不会加上省略号呢？")
	wantLong := string(runes[:40]) + "…"
	if got != wantLong {
		t.Errorf("generateThreadTitle long multiline mismatch:\ngot:  %s\nwant: %s", got, wantLong)
	}
	if len([]rune(got)) != 41 {
		t.Errorf("expected 41 runes (40 chars + 1 ellipsis), got %d", len([]rune(got)))
	}

	// 验证空白压缩
	spaceText := "  第一行 \t 测试   多个连续空白   压成一个空格 \t\n第二行"
	gotSpace := generateThreadTitle(spaceText)
	wantSpace := "第一行 测试 多个连续空白 压成一个空格"
	if gotSpace != wantSpace {
		t.Errorf("generateThreadTitle space compression mismatch:\ngot:  %s\nwant: %s", gotSpace, wantSpace)
	}

	// 验证正好 40 字（不应加省略号）
	exact40 := strings.Repeat("中", 40)
	gotExact40 := generateThreadTitle(exact40)
	if gotExact40 != exact40 {
		t.Errorf("generateThreadTitle exact 40 mismatch:\ngot:  %s\nwant: %s", gotExact40, exact40)
	}

	// 验证 41 字（超出加省略号）
	exact41 := strings.Repeat("中", 41)
	gotExact41 := generateThreadTitle(exact41)
	want41 := strings.Repeat("中", 40) + "…"
	if gotExact41 != want41 {
		t.Errorf("generateThreadTitle 41 mismatch:\ngot:  %s\nwant: %s", gotExact41, want41)
	}

	// 验证空内容/仅空白保持默认值
	blankText := "   \n\t  \n  "
	gotBlank := generateThreadTitle(blankText)
	if gotBlank != defaultThreadTitle {
		t.Errorf("generateThreadTitle blank expected %q, got %q", defaultThreadTitle, gotBlank)
	}

	// 2. 通过 SendMessage 端到端验证
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	projID := "proj-title-b"
	backend.AddProject(wire.ProjectSummary{ID: projID, Name: "Title Project B", Path: "/path/b"})

	res, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: projID})
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	thID := res.ThreadID

	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: thID,
		Text:     longChineseText,
	})
	if err != nil {
		t.Fatalf("send message: %v", err)
	}

	waitThreadIdle(t, backend, ctx, thID)

	th, err := backend.GetThread(ctx, thID)
	if err != nil {
		t.Fatalf("get thread: %v", err)
	}
	if th.Title != wantLong {
		t.Errorf("backend thread title mismatch:\ngot:  %s\nwant: %s", th.Title, wantLong)
	}
}

// (c) 第二次发消息不再改标题
func TestThreadTitle_SecondMessageDoesNotOverwrite(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	projID := "proj-title-c"
	backend.AddProject(wire.ProjectSummary{ID: projID, Name: "Title Project C", Path: "/path/c"})

	res, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: projID})
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	thID := res.ThreadID

	// 发送首条消息
	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: thID,
		Text:     "第一条消息作为标题",
	})
	if err != nil {
		t.Fatalf("send first message: %v", err)
	}
	waitThreadIdle(t, backend, ctx, thID)

	th1, err := backend.GetThread(ctx, thID)
	if err != nil {
		t.Fatalf("get thread after first msg: %v", err)
	}
	if th1.Title != "第一条消息作为标题" {
		t.Fatalf("expected title %q, got %q", "第一条消息作为标题", th1.Title)
	}

	// 发送第二条消息
	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: thID,
		Text:     "第二条消息不应该覆盖标题",
	})
	if err != nil {
		t.Fatalf("send second message: %v", err)
	}
	waitThreadIdle(t, backend, ctx, thID)

	th2, err := backend.GetThread(ctx, thID)
	if err != nil {
		t.Fatalf("get thread after second msg: %v", err)
	}
	if th2.Title != "第一条消息作为标题" {
		t.Errorf("expected title to remain %q, but got %q", "第一条消息作为标题", th2.Title)
	}
}

// (d) 标题已不是默认值时不覆盖
func TestThreadTitle_CustomTitleNotOverwritten(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	projID := "proj-title-d"
	backend.AddProject(wire.ProjectSummary{ID: projID, Name: "Title Project D", Path: "/path/d"})

	customTitle := "手动自定义的标题"
	thID := "thread-custom-title"
	backend.AddThread(wire.ThreadSummary{
		ID:        thID,
		ProjectID: projID,
		Title:     customTitle,
		Provider:  "memory",
	}, nil)

	_, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: thID,
		Text:     "发条新消息试试能不能覆盖自定义标题",
	})
	if err != nil {
		t.Fatalf("send message: %v", err)
	}
	waitThreadIdle(t, backend, ctx, thID)

	th, err := backend.GetThread(ctx, thID)
	if err != nil {
		t.Fatalf("get thread: %v", err)
	}
	if th.Title != customTitle {
		t.Errorf("expected title to remain %q, but got %q", customTitle, th.Title)
	}
}

// (e) 验证重启（重新加载）后标题保留，并验证 Harness Turn 流程
func TestThreadTitle_PersistenceAcrossReload(t *testing.T) {
	ctx := context.Background()
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "mcode.db")

	s1, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store 1: %v", err)
	}

	hub1 := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	backend1 := NewMemoryBackend(hub1)
	if err := backend1.AttachStore(ctx, s1); err != nil {
		t.Fatalf("attach store 1: %v", err)
	}

	off := false
	backend1.SetGeneralSettingsFunc(func() config.Config { return config.Config{Session: config.SessionConfig{ConciseRepliesEnabled: &off}} })

	// 注册带 harness.Turn 的 Mock，验证旧权限档规范化
	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventText, Text: "harness echo"})
			emit(harness.Event{Type: harness.EventDone})
			return nil
		},
	}
	backend1.SetHarness(mock, "/mock/workdir", harness.ModeAgent)

	projID := "proj-title-persist"
	backend1.AddProject(wire.ProjectSummary{
		ID:   projID,
		Name: "Persist Project",
		Path: "/mock/workdir",
	})

	res, err := backend1.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: projID,
	})
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	thID := res.ThreadID

	sentText := "重启持久化验证消息"
	_, err = backend1.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: thID,
		Text:     sentText,
	})
	if err != nil {
		t.Fatalf("send message: %v", err)
	}

	waitThreadIdle(t, backend1, ctx, thID)

	th1, err := backend1.GetThread(ctx, thID)
	if err != nil {
		t.Fatalf("get thread from backend1: %v", err)
	}
	if th1.Title != sentText {
		t.Fatalf("expected title before reload to be %q, got %q", sentText, th1.Title)
	}

	// 校验 mockHarness 收到的 Turn
	calls := mock.getTurnCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 turn call, got %d", len(calls))
	}
	if calls[0].Mode != harness.ModeAuto {
		t.Errorf("expected ModeAuto, got %v", calls[0].Mode)
	}
	if calls[0].Prompt != sentText {
		t.Errorf("expected prompt %q, got %q", sentText, calls[0].Prompt)
	}

	// 关闭第一组实例
	backend1.Close()
	hub1.Close()
	_ = s1.Close()

	// 重新打开数据库和新 Backend，模拟服务重启
	s2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store 2: %v", err)
	}
	defer s2.Close()

	hub2 := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub2.Close()
	backend2 := NewMemoryBackend(hub2)
	defer backend2.Close()
	backend2.SetGeneralSettingsFunc(func() config.Config { return config.Config{Session: config.SessionConfig{ConciseRepliesEnabled: &off}} })

	if err := backend2.AttachStore(ctx, s2); err != nil {
		t.Fatalf("attach store 2: %v", err)
	}

	th2, err := backend2.GetThread(ctx, thID)
	if err != nil {
		t.Fatalf("get thread after reload: %v", err)
	}
	if th2.Title != sentText {
		t.Errorf("expected persisted title after reload %q, got %q", sentText, th2.Title)
	}
}
