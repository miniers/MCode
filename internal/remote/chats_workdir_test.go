package remote

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

func TestChatsDayWorkDir(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	mockH := &mockHarness{kind: "mock"}
	backend.RegisterHarness(mockH)

	chatsPath := t.TempDir()
	normalPath := t.TempDir()
	chatsProjID := newUUID()
	normalProjID := newUUID()
	backend.AddProject(wire.ProjectSummary{ID: chatsProjID, Name: "对话", Path: chatsPath})
	backend.AddProject(wire.ProjectSummary{ID: normalProjID, Name: "普通项目", Path: normalPath})
	backend.SetChatsProjectID(chatsProjID)

	chatsRes, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: chatsProjID, ActionID: newUUID()})
	if err != nil {
		t.Fatalf("CreateThread(chats) failed: %v", err)
	}
	normalRes, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: normalProjID, ActionID: newUUID()})
	if err != nil {
		t.Fatalf("CreateThread(normal) failed: %v", err)
	}

	wantChatsDir := filepath.Join(chatsPath, time.Now().Local().Format("2006-01-02"))
	gotChatsDir, ok := backend.ThreadWorkDir(chatsRes.ThreadID)
	if !ok || gotChatsDir != wantChatsDir {
		t.Fatalf("对话会话 ThreadWorkDir = (%q, %v), want (%q, true)", gotChatsDir, ok, wantChatsDir)
	}
	if _, err := os.Stat(wantChatsDir); !os.IsNotExist(err) {
		t.Fatalf("CreateThread 不应提前创建按日目录 %q (stat err=%v)", wantChatsDir, err)
	}

	gotNormalDir, ok := backend.ThreadWorkDir(normalRes.ThreadID)
	if !ok || gotNormalDir != normalPath {
		t.Fatalf("普通会话 ThreadWorkDir = (%q, %v), want (%q, true)", gotNormalDir, ok, normalPath)
	}

	t.Run("RunCreatesDayDirAndReportsMkdirFailure", func(t *testing.T) {
		if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
			ThreadID: chatsRes.ThreadID,
			Text:     "hello",
			ActionID: newUUID(),
		}); err != nil {
			t.Fatalf("SendMessage(chats) failed: %v", err)
		}
		summary := waitThreadIdle(t, backend, ctx, chatsRes.ThreadID)
		if summary.Attention == "failed" {
			t.Fatalf("正常对话轮次不应失败: %+v", summary)
		}
		if fi, err := os.Stat(wantChatsDir); err != nil || !fi.IsDir() {
			t.Fatalf("首轮启动后按日目录应被创建: err=%v fi=%v", err, fi)
		}
		calls := mockH.getTurnCalls()
		if len(calls) == 0 || calls[len(calls)-1].WorkDir != wantChatsDir {
			t.Fatalf("provider 收到的 WorkDir 不符: calls=%+v want=%q", calls, wantChatsDir)
		}

		// 当 chats 项目根路径被普通文件占用时，MkdirAll 失败应终止轮次、不调用 provider 且置失败态。
		blockedRoot := filepath.Join(t.TempDir(), "blocked-chats")
		if err := os.WriteFile(blockedRoot, []byte("file"), 0o644); err != nil {
			t.Fatal(err)
		}
		blockedHub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
		defer blockedHub.Close()
		blockedBackend := NewMemoryBackend(blockedHub)
		defer blockedBackend.Close()
		blockedHarness := &mockHarness{kind: "mock"}
		blockedBackend.RegisterHarness(blockedHarness)

		blockedProjID := newUUID()
		blockedBackend.AddProject(wire.ProjectSummary{ID: blockedProjID, Name: "对话", Path: blockedRoot})
		blockedBackend.SetChatsProjectID(blockedProjID)
		blockedRes, err := blockedBackend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: blockedProjID, ActionID: newUUID()})
		if err != nil {
			t.Fatalf("CreateThread(blocked) failed: %v", err)
		}
		if _, err := blockedBackend.SendMessage(ctx, wire.SendMessageParams{
			ThreadID: blockedRes.ThreadID,
			Text:     "should fail",
			ActionID: newUUID(),
		}); err != nil {
			t.Fatalf("SendMessage(blocked) failed: %v", err)
		}
		failedSummary := waitThreadIdle(t, blockedBackend, ctx, blockedRes.ThreadID)
		if failedSummary.Attention != "failed" || failedSummary.IsRunning {
			t.Fatalf("MkdirAll 失败后应结束运行并标为 failed, got %+v", failedSummary)
		}
		if gotCalls := blockedHarness.getTurnCalls(); len(gotCalls) != 0 {
			t.Fatalf("MkdirAll 失败时不应启动 provider, got %d calls", len(gotCalls))
		}
	})
}

func TestChatsDayWorkDirFromStore(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "chats.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	chatsPath := t.TempDir()
	normalPath := t.TempDir()
	chatsProjID := newUUID()
	normalProjID := newUUID()

	pastUTC := time.Date(2024, 5, 1, 12, 30, 0, 0, time.UTC)
	if err := s.UpsertProject(ctx, store.Project{
		ID:        chatsProjID,
		Name:      "对话",
		Path:      chatsPath,
		CreatedAt: pastUTC,
		UpdatedAt: pastUTC,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertProject(ctx, store.Project{
		ID:        normalProjID,
		Name:      "普通",
		Path:      normalPath,
		CreatedAt: pastUTC,
		UpdatedAt: pastUTC,
	}); err != nil {
		t.Fatal(err)
	}

	chatsThreadID := newUUID()
	normalThreadID := newUUID()
	if err := s.CreateThread(ctx, store.Thread{
		ID:        chatsThreadID,
		ProjectID: chatsProjID,
		Title:     "历史对话",
		Provider:  "mock",
		CreatedAt: pastUTC,
		UpdatedAt: pastUTC,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateThread(ctx, store.Thread{
		ID:        normalThreadID,
		ProjectID: normalProjID,
		Title:     "历史普通",
		Provider:  "mock",
		CreatedAt: pastUTC,
		UpdatedAt: pastUTC,
	}); err != nil {
		t.Fatal(err)
	}

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	if err := backend.AttachStore(ctx, s); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}
	backend.SetChatsProjectID(chatsProjID)

	wantChatsDir := filepath.Join(chatsPath, pastUTC.Local().Format("2006-01-02"))
	gotChatsDir, ok := backend.ThreadWorkDir(chatsThreadID)
	if !ok || gotChatsDir != wantChatsDir {
		t.Fatalf("从 store 恢复的对话会话 ThreadWorkDir = (%q, %v), want (%q, true)", gotChatsDir, ok, wantChatsDir)
	}
	gotNormalDir, ok := backend.ThreadWorkDir(normalThreadID)
	if !ok || gotNormalDir != normalPath {
		t.Fatalf("从 store 恢复的普通会话 ThreadWorkDir = (%q, %v), want (%q, true)", gotNormalDir, ok, normalPath)
	}
}

func TestListProjectsKind(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "projects_kind.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	backend.RegisterHarness(&mockHarness{kind: harness.Kind("mock")})
	if err := backend.AttachStore(ctx, s); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}

	chatsProjID := newUUID()
	stewardProjID := newUUID()
	normalProjID := newUUID()

	backend.AddProject(wire.ProjectSummary{ID: chatsProjID, Name: "对话", Path: t.TempDir()})
	backend.AddProject(wire.ProjectSummary{ID: stewardProjID, Name: "总管", Path: t.TempDir()})
	backend.AddProject(wire.ProjectSummary{ID: normalProjID, Name: "普通项目", Path: t.TempDir()})

	backend.SetChatsProjectID(chatsProjID)
	if _, err := backend.EnsureStewardFeedThread(ctx, stewardProjID); err != nil {
		t.Fatalf("EnsureStewardFeedThread: %v", err)
	}

	list, err := backend.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(list.Projects) != 3 {
		t.Fatalf("ListProjects 返回 %d 个项目, want 3", len(list.Projects))
	}

	kinds := make(map[string]string, len(list.Projects))
	for _, p := range list.Projects {
		kinds[p.ID] = p.Kind
	}
	if kinds[chatsProjID] != "chats" {
		t.Fatalf("chats 项目 Kind = %q, want %q", kinds[chatsProjID], "chats")
	}
	if kinds[stewardProjID] != "steward" {
		t.Fatalf("steward 项目 Kind = %q, want %q", kinds[stewardProjID], "steward")
	}
	if kinds[normalProjID] != "" {
		t.Fatalf("普通项目 Kind = %q, want empty", kinds[normalProjID])
	}
}

func TestMoveThreadToProject(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "move_to_project.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	mockH := &mockHarness{
		kind: "mock",
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{Type: harness.EventSession, SessionID: "sess-" + turn.ID})
			emit(harness.Event{Type: harness.EventText, Text: "reply"})
			return nil
		},
	}
	backend.RegisterHarness(mockH)
	var guidanceDirs []string
	backend.SetGuidanceBuilder(func(workDir string) string {
		guidanceDirs = append(guidanceDirs, workDir)
		return "[GUIDANCE:" + filepath.Base(workDir) + "]"
	})
	if err := backend.AttachStore(ctx, s); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}

	chatsPath := t.TempDir()
	targetPath := t.TempDir()
	chatsProjID := newUUID()
	targetProjID := newUUID()
	backend.AddProject(wire.ProjectSummary{ID: chatsProjID, Name: "对话", Path: chatsPath})
	backend.AddProject(wire.ProjectSummary{ID: targetProjID, Name: "目标项目", Path: targetPath})
	backend.SetChatsProjectID(chatsProjID)

	existingRes, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: targetProjID, ActionID: newUUID()})
	if err != nil {
		t.Fatalf("CreateThread(existing) failed: %v", err)
	}
	chatsRes, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: chatsProjID, ActionID: newUUID()})
	if err != nil {
		t.Fatalf("CreateThread(chats) failed: %v", err)
	}

	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: chatsRes.ThreadID,
		Text:     "首轮消息",
		ActionID: newUUID(),
	}); err != nil {
		t.Fatalf("SendMessage(1) failed: %v", err)
	}
	waitThreadIdle(t, backend, ctx, chatsRes.ThreadID)

	summary, err := backend.MoveThreadToProject(ctx, wire.ThreadsMoveToProjectParams{
		ThreadID:  chatsRes.ThreadID,
		ProjectID: targetProjID,
	})
	if err != nil {
		t.Fatalf("MoveThreadToProject failed: %v", err)
	}
	if summary.ProjectID != targetProjID {
		t.Fatalf("summary.ProjectID = %q, want %q", summary.ProjectID, targetProjID)
	}
	if gotDir, ok := backend.ThreadWorkDir(chatsRes.ThreadID); !ok || gotDir != targetPath {
		t.Fatalf("移动后 ThreadWorkDir = (%q, %v), want (%q, true)", gotDir, ok, targetPath)
	}
	backend.mu.Lock()
	sessID := backend.threads[chatsRes.ThreadID].sessionID
	needsHandoff := backend.threads[chatsRes.ThreadID].needsHandoff
	backend.mu.Unlock()
	if sessID != "" || !needsHandoff {
		t.Fatalf("移动后 sessionID = %q, needsHandoff = %v; want empty and true", sessID, needsHandoff)
	}

	// 移动后排在目标项目最前。
	targetThreads, err := backend.ListThreads(ctx, wire.ThreadListParams{ProjectID: &targetProjID})
	if err != nil {
		t.Fatalf("ListThreads(target) failed: %v", err)
	}
	if len(targetThreads.Threads) != 2 || targetThreads.Threads[0].ID != chatsRes.ThreadID || targetThreads.Threads[1].ID != existingRes.ThreadID {
		t.Fatalf("目标项目会话顺序不符: %+v", targetThreads.Threads)
	}

	// 移动后的下一轮应重新注入目标项目的指导词，并带本地历史交接。
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: chatsRes.ThreadID,
		Text:     "第二轮消息",
		ActionID: newUUID(),
	}); err != nil {
		t.Fatalf("SendMessage(2) failed: %v", err)
	}
	waitThreadIdle(t, backend, ctx, chatsRes.ThreadID)
	calls := mockH.getTurnCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 turn calls, got %d", len(calls))
	}
	if calls[1].WorkDir != targetPath || calls[1].SessionID != "" {
		t.Fatalf("第二轮 Turn 参数不符: WorkDir=%q SessionID=%q", calls[1].WorkDir, calls[1].SessionID)
	}
	if len(guidanceDirs) != 2 || guidanceDirs[1] != targetPath {
		t.Fatalf("移动后应重新为目标项目构建指导词: %v", guidanceDirs)
	}

	// 重新 AttachStore 后仍属于目标项目且排在最前。
	hub2 := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub2.Close()
	backend2 := NewMemoryBackend(hub2)
	defer backend2.Close()
	if err := backend2.AttachStore(ctx, s); err != nil {
		t.Fatalf("AttachStore(2): %v", err)
	}
	backend2.SetChatsProjectID(chatsProjID)
	reloaded, err := backend2.GetThread(ctx, chatsRes.ThreadID)
	if err != nil {
		t.Fatalf("GetThread after reload: %v", err)
	}
	if reloaded.ProjectID != targetProjID {
		t.Fatalf("重载后 ProjectID = %q, want %q", reloaded.ProjectID, targetProjID)
	}
	if gotDir, ok := backend2.ThreadWorkDir(chatsRes.ThreadID); !ok || gotDir != targetPath {
		t.Fatalf("重载后 ThreadWorkDir = (%q, %v), want (%q, true)", gotDir, ok, targetPath)
	}
}

func TestMoveThreadToProjectRejects(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "move_rejects.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	backend.RegisterHarness(&mockHarness{kind: "mock"})
	if err := backend.AttachStore(ctx, s); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}

	chatsProjID := newUUID()
	stewardProjID := newUUID()
	normalProjID := newUUID()
	otherProjID := newUUID()
	backend.AddProject(wire.ProjectSummary{ID: chatsProjID, Name: "对话", Path: t.TempDir()})
	backend.AddProject(wire.ProjectSummary{ID: stewardProjID, Name: "总管", Path: t.TempDir()})
	backend.AddProject(wire.ProjectSummary{ID: normalProjID, Name: "普通项目1", Path: t.TempDir()})
	backend.AddProject(wire.ProjectSummary{ID: otherProjID, Name: "普通项目2", Path: t.TempDir()})
	backend.SetChatsProjectID(chatsProjID)
	if _, err := backend.EnsureStewardFeedThread(ctx, stewardProjID); err != nil {
		t.Fatalf("EnsureStewardFeedThread: %v", err)
	}

	chatsRes, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: chatsProjID, ActionID: newUUID()})
	if err != nil {
		t.Fatalf("CreateThread(chats): %v", err)
	}
	normalRes, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: normalProjID, ActionID: newUUID()})
	if err != nil {
		t.Fatalf("CreateThread(normal): %v", err)
	}

	// 1. 会话不存在
	if _, err := backend.MoveThreadToProject(ctx, wire.ThreadsMoveToProjectParams{ThreadID: newUUID(), ProjectID: normalProjID}); err == nil {
		t.Fatal("不存在的会话应被拒绝")
	}
	// 2. 非对话会话
	if _, err := backend.MoveThreadToProject(ctx, wire.ThreadsMoveToProjectParams{ThreadID: normalRes.ThreadID, ProjectID: otherProjID}); err == nil {
		t.Fatal("普通项目会话应被拒绝")
	}
	// 3. 目标项目不存在
	if _, err := backend.MoveThreadToProject(ctx, wire.ThreadsMoveToProjectParams{ThreadID: chatsRes.ThreadID, ProjectID: newUUID()}); err == nil {
		t.Fatal("不存在的目标项目应被拒绝")
	}
	// 4. 目标是对话项目
	if _, err := backend.MoveThreadToProject(ctx, wire.ThreadsMoveToProjectParams{ThreadID: chatsRes.ThreadID, ProjectID: chatsProjID}); err == nil {
		t.Fatal("目标是对话项目应被拒绝")
	}
	// 5. 目标是总管项目
	if _, err := backend.MoveThreadToProject(ctx, wire.ThreadsMoveToProjectParams{ThreadID: chatsRes.ThreadID, ProjectID: stewardProjID}); err == nil {
		t.Fatal("目标是总管项目应被拒绝")
	}
	// 6. 会话运行中
	backend.mu.Lock()
	backend.threads[chatsRes.ThreadID].runTurnID = "turn-running"
	backend.mu.Unlock()
	if _, err := backend.MoveThreadToProject(ctx, wire.ThreadsMoveToProjectParams{ThreadID: chatsRes.ThreadID, ProjectID: normalProjID}); err == nil {
		t.Fatal("运行中的会话应被拒绝")
	}
	backend.mu.Lock()
	backend.threads[chatsRes.ThreadID].runTurnID = ""
	backend.mu.Unlock()

	// 7. 已绑定 workDir
	backend.mu.Lock()
	backend.threads[chatsRes.ThreadID].workDir = "/some/wt"
	backend.mu.Unlock()
	if _, err := backend.MoveThreadToProject(ctx, wire.ThreadsMoveToProjectParams{ThreadID: chatsRes.ThreadID, ProjectID: normalProjID}); err == nil {
		t.Fatal("绑定 workDir 的会话应被拒绝")
	}
	backend.mu.Lock()
	backend.threads[chatsRes.ThreadID].workDir = ""
	backend.mu.Unlock()

	// 8. 自身是子会话
	childID := newUUID()
	backend.AddThread(wire.ThreadSummary{ID: childID, ProjectID: chatsProjID, ParentThreadID: &chatsRes.ThreadID, IsHelper: true}, nil)
	if _, err := backend.MoveThreadToProject(ctx, wire.ThreadsMoveToProjectParams{ThreadID: childID, ProjectID: normalProjID}); err == nil {
		t.Fatal("helper 子会话应被拒绝")
	}
}

func TestMoveThreadToProjectWithChildren(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "move_with_children.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	backend.RegisterHarness(&mockHarness{kind: "mock"})
	if err := backend.AttachStore(ctx, s); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}

	chatsProjID := newUUID()
	targetProjID := newUUID()
	backend.AddProject(wire.ProjectSummary{ID: chatsProjID, Name: "对话", Path: t.TempDir()})
	backend.AddProject(wire.ProjectSummary{ID: targetProjID, Name: "目标项目", Path: t.TempDir()})
	backend.SetChatsProjectID(chatsProjID)

	// (c) 后代运行中时移动被拒绝，且父会话与后代的 ProjectID 都没有变化。
	runningParentRes, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: chatsProjID, ActionID: newUUID()})
	if err != nil {
		t.Fatalf("CreateThread(runningParent): %v", err)
	}
	runningChildID := newUUID()
	backend.AddThread(wire.ThreadSummary{
		ID:             runningChildID,
		ProjectID:      chatsProjID,
		ParentThreadID: &runningParentRes.ThreadID,
		IsHelper:       true,
		IsRunning:      true,
	}, nil)
	if _, err := backend.MoveThreadToProject(ctx, wire.ThreadsMoveToProjectParams{
		ThreadID:  runningParentRes.ThreadID,
		ProjectID: targetProjID,
	}); err == nil {
		t.Fatal("后代运行中时移动父会话应被拒绝")
	}
	for _, id := range []string{runningParentRes.ThreadID, runningChildID} {
		got, err := backend.GetThread(ctx, id)
		if err != nil {
			t.Fatalf("GetThread(%s): %v", id, err)
		}
		if got.ProjectID != chatsProjID {
			t.Fatalf("拒绝移动后会话 %s ProjectID = %q, want %q", id, got.ProjectID, chatsProjID)
		}
		st, err := s.GetThread(ctx, id)
		if err != nil {
			t.Fatalf("store.GetThread(%s): %v", id, err)
		}
		if st.ProjectID != chatsProjID {
			t.Fatalf("拒绝移动后 store 中会话 %s ProjectID = %q, want %q", id, st.ProjectID, chatsProjID)
		}
	}

	// (a)(b) 父会话带子会话和孙会话：三者一同移动，store 也更新；workDir 非空的后代保留其 workDir。
	parentRes, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: chatsProjID, ActionID: newUUID()})
	if err != nil {
		t.Fatalf("CreateThread(parent): %v", err)
	}
	childID := newUUID()
	grandchildID := newUUID()
	backend.AddThread(wire.ThreadSummary{
		ID:             childID,
		ProjectID:      chatsProjID,
		ParentThreadID: &parentRes.ThreadID,
		IsHelper:       true,
	}, []wire.ThreadMessage{{ID: newUUID(), Role: "user", Text: "子会话消息"}})
	backend.AddThread(wire.ThreadSummary{
		ID:             grandchildID,
		ProjectID:      chatsProjID,
		ParentThreadID: &childID,
		IsHelper:       true,
	}, nil)

	customWorkDir := t.TempDir()
	backend.mu.Lock()
	backend.threads[childID].sessionID = "child-sess"
	backend.threads[grandchildID].workDir = customWorkDir
	backend.threads[grandchildID].sessionID = "grandchild-sess"
	backend.mu.Unlock()

	var hookCalls int
	var hookThreads []wire.ThreadSummary
	removeListener := backend.AddThreadsChangedListener(func(ev wire.ThreadsChanged) {
		hookCalls++
		hookThreads = append(hookThreads, ev.Threads...)
	})
	defer removeListener()

	movedParent, err := backend.MoveThreadToProject(ctx, wire.ThreadsMoveToProjectParams{
		ThreadID:  parentRes.ThreadID,
		ProjectID: targetProjID,
	})
	if err != nil {
		t.Fatalf("MoveThreadToProject failed: %v", err)
	}
	if movedParent.ProjectID != targetProjID {
		t.Fatalf("movedParent.ProjectID = %q, want %q", movedParent.ProjectID, targetProjID)
	}
	if hookCalls != 1 || len(hookThreads) != 3 {
		t.Fatalf("threadsChangedHook calls=%d len=%d, want 1 and 3", hookCalls, len(hookThreads))
	}

	for _, id := range []string{parentRes.ThreadID, childID, grandchildID} {
		got, err := backend.GetThread(ctx, id)
		if err != nil {
			t.Fatalf("GetThread(%s): %v", id, err)
		}
		if got.ProjectID != targetProjID {
			t.Fatalf("移动后会话 %s ProjectID = %q, want %q", id, got.ProjectID, targetProjID)
		}
		st, err := s.GetThread(ctx, id)
		if err != nil {
			t.Fatalf("store.GetThread(%s): %v", id, err)
		}
		if st.ProjectID != targetProjID {
			t.Fatalf("移动后 store 中会话 %s ProjectID = %q, want %q", id, st.ProjectID, targetProjID)
		}
	}

	backend.mu.Lock()
	childSessionID := backend.threads[childID].sessionID
	childNeedsHandoff := backend.threads[childID].needsHandoff
	childReinject := backend.threads[childID].reinjectGuidance
	grandchildWorkDir := backend.threads[grandchildID].workDir
	grandchildSessionID := backend.threads[grandchildID].sessionID
	backend.mu.Unlock()

	if childSessionID != "" || !childNeedsHandoff || !childReinject {
		t.Fatalf("无 workDir 后代应重置会话状态: sessionID=%q needsHandoff=%v reinject=%v", childSessionID, childNeedsHandoff, childReinject)
	}
	if grandchildWorkDir != customWorkDir || grandchildSessionID != "grandchild-sess" {
		t.Fatalf("有 workDir 后代应保留独立目录与会话状态: workDir=%q sessionID=%q", grandchildWorkDir, grandchildSessionID)
	}
}

func TestMoveThreadKeepsDescendantWorkDir(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "move_keeps_descendant_workdir.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	backend.RegisterHarness(&mockHarness{kind: "mock"})
	if err := backend.AttachStore(ctx, s); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}

	chatsProjID := newUUID()
	targetProjID := newUUID()
	backend.AddProject(wire.ProjectSummary{ID: chatsProjID, Name: "对话", Path: t.TempDir()})
	backend.AddProject(wire.ProjectSummary{ID: targetProjID, Name: "目标项目", Path: t.TempDir()})
	backend.SetChatsProjectID(chatsProjID)

	parentRes, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: chatsProjID, ActionID: newUUID()})
	if err != nil {
		t.Fatalf("CreateThread(parent): %v", err)
	}
	if err := s.UpdateThreadSession(ctx, parentRes.ThreadID, "parent-sess"); err != nil {
		t.Fatalf("UpdateThreadSession(parent): %v", err)
	}
	backend.mu.Lock()
	backend.threads[parentRes.ThreadID].sessionID = "parent-sess"
	backend.mu.Unlock()

	childID := newUUID()
	backend.AddThread(wire.ThreadSummary{
		ID:             childID,
		ProjectID:      chatsProjID,
		ParentThreadID: &parentRes.ThreadID,
		IsHelper:       true,
	}, nil)

	customWorkDir := t.TempDir()
	if err := s.UpdateThreadWorkDir(ctx, childID, customWorkDir, true); err != nil {
		t.Fatalf("UpdateThreadWorkDir(child): %v", err)
	}
	if err := s.UpdateThreadSession(ctx, childID, "child-sess"); err != nil {
		t.Fatalf("UpdateThreadSession(child): %v", err)
	}
	backend.mu.Lock()
	backend.threads[childID].workDir = customWorkDir
	backend.threads[childID].summary.WorktreeRequested = true
	backend.threads[childID].sessionID = "child-sess"
	backend.mu.Unlock()

	if _, err := backend.MoveThreadToProject(ctx, wire.ThreadsMoveToProjectParams{
		ThreadID:  parentRes.ThreadID,
		ProjectID: targetProjID,
	}); err != nil {
		t.Fatalf("MoveThreadToProject failed: %v", err)
	}

	// 验证 store 中子会话保留独立工作目录与 session_id，且已迁入目标项目；父会话已重置。
	stParent, err := s.GetThread(ctx, parentRes.ThreadID)
	if err != nil {
		t.Fatalf("store.GetThread(parent): %v", err)
	}
	if stParent.ProjectID != targetProjID || stParent.WorktreePath != "" || stParent.SessionID != "" {
		t.Fatalf("父会话在 store 中应已迁入目标项目并清空工作目录与 session: %+v", stParent)
	}
	stChild, err := s.GetThread(ctx, childID)
	if err != nil {
		t.Fatalf("store.GetThread(child): %v", err)
	}
	if stChild.ProjectID != targetProjID || stChild.WorktreePath != customWorkDir || stChild.SessionID != "child-sess" {
		t.Fatalf("子会话在 store 中应保留独立工作目录与 session_id: %+v", stChild)
	}

	// 重建 backend 并重载 store，验证重启后子会话仍保留 workDir 与 sessionID。
	hub2 := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub2.Close()
	backend2 := NewMemoryBackend(hub2)
	defer backend2.Close()
	if err := backend2.AttachStore(ctx, s); err != nil {
		t.Fatalf("AttachStore(2): %v", err)
	}
	backend2.SetChatsProjectID(chatsProjID)

	reloadedChild, err := backend2.GetThread(ctx, childID)
	if err != nil {
		t.Fatalf("GetThread(child) after reload: %v", err)
	}
	if reloadedChild.ProjectID != targetProjID {
		t.Fatalf("重载后子会话 ProjectID = %q, want %q", reloadedChild.ProjectID, targetProjID)
	}
	if gotDir, ok := backend2.ThreadWorkDir(childID); !ok || gotDir != customWorkDir {
		t.Fatalf("重载后子会话 ThreadWorkDir = (%q, %v), want (%q, true)", gotDir, ok, customWorkDir)
	}
	backend2.mu.Lock()
	reloadedSess := backend2.threads[childID].sessionID
	backend2.mu.Unlock()
	if reloadedSess != "child-sess" {
		t.Fatalf("重载后子会话 sessionID = %q, want %q", reloadedSess, "child-sess")
	}
}
