package mcodeui

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func inspectorFollowUpdate(s *chatState, tid, id, tool, input, status string) {
	s.mu.Lock()
	thread := Thread{ID: tid}
	for _, th := range s.threads {
		if th.ID == tid {
			thread = th
		}
	}
	s.mu.Unlock()
	s.onWatchUpdate(ThreadUpdate{ThreadID: tid, Thread: thread, Messages: []Message{{ID: id, Role: "tool", ToolName: tool, ToolInput: input, ToolStatus: status}}})
}

func inspectorFollowTestState(t *testing.T) (*FakeClient, *chatState, *ui.Tester) {
	t.Helper()
	fake, s, tt := openFileManagerForTest(t)
	t.Cleanup(s.close)
	fake.SetFileContents(map[string]FileContent{
		"a.txt":    {Path: "a.txt", Kind: "text", Text: strings.Repeat("a\n", 300)},
		"sub/b.go": {Path: "sub/b.go", Kind: "text", Text: strings.Repeat("package b\n", 300)},
	})
	return fake, s, tt
}

func inspectorFollowBadge(s *chatState, tid, id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := inspectorExistingSessionLocked(s, tid); st != nil && st.follow != nil {
		return st.follow.badges[id]
	}
	return 0
}

func inspectorFollowPaused(s *chatState) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := inspectorExistingSessionLocked(s, s.selectedThreadID)
	return st != nil && st.follow != nil && st.follow.paused
}

func inspectorFollowWaitFile(t *testing.T, tt *ui.Tester, s *chatState, path string) {
	t.Helper()
	waitFor(t, tt, func() bool {
		_, _, active := fileManagerSnapshot(s)
		return active == path && fileManagerTabText(s, path) != ""
	})
	tt.Advance(DurBase + 50*time.Millisecond)
}

func TestInspectorFollowRevealsAgentFileWithoutFocusSteal(t *testing.T) {
	_, s, tt := inspectorFollowTestState(t)
	card, ok := tt.Find("消息输入框")
	if !ok {
		t.Fatal("消息输入框不存在")
	}
	tt.ClickAt(card.X+card.W/2, card.Y+card.H/2)
	tt.Frame()
	inspectorFollowUpdate(s, "t1", "read-1", "read", `{"path":"sub/b.go","offset":120,"limit":3}`, "completed")
	inspectorFollowWaitFile(t, tt, s, "sub/b.go")
	// 定位入口没有调用 Focus；编辑器状态只改变滚动和高亮。
	st := getFileManagerState(s)
	fileManagerMu.Lock()
	tab := st.findTabLocked("sub/b.go")
	first, last := tab.hlFirst, tab.hlLast
	fileManagerMu.Unlock()
	if first != 120 || last != 122 || inspectorFollowPaused(s) || inspectorFollowBadge(s, "t1", sidePaneFileID) != 0 {
		t.Fatalf("定位范围或跟随状态错误: %d:%d paused=%v", first, last, inspectorFollowPaused(s))
	}
}

func TestInspectorFollowPausesOnUserNavigation(t *testing.T) {
	for _, action := range []string{"tree-select", "tree-expand", "file-tab", "review-file", "review-scope", "scroll"} {
		t.Run(action, func(t *testing.T) {
			_, s, tt := inspectorFollowTestState(t)
			inspectorFollowUpdate(s, "t1", "read-1", "read", `{"path":"a.txt"}`, "completed")
			inspectorFollowWaitFile(t, tt, s, "a.txt")
			switch action {
			case "tree-select":
				s.fileTreeSelect("a.txt", 0)
			case "tree-expand":
				s.fileTreeToggleDir("t1", "sub")
			case "file-tab":
				s.activateFileManagerTab("a.txt")
			case "review-file":
				selectReviewFile(s, getDiffPanelState(s), "t1", "a.txt")
			case "review-scope":
				s.selectReviewScope(getDiffPanelState(s), "session")
			case "scroll":
				st := getFileManagerState(s)
				for _, view := range st.codeViews {
					view.state.SetScrollY(500)
				}
				tt.Frame()
			}
			if !inspectorFollowPaused(s) {
				t.Fatal("用户导航未暂停跟随")
			}
			inspectorFollowUpdate(s, "t1", "read-2", "read", `{"path":"sub/b.go"}`, "completed")
			time.Sleep(inspectorFollowInterval + 10*time.Millisecond)
			tt.Frame()
			_, _, active := fileManagerSnapshot(s)
			if active != "a.txt" || inspectorFollowBadge(s, "t1", sidePaneFileID) != 1 {
				t.Fatal("用户接管后仍自动切换文件或未记录徽标")
			}
		})
	}
}

func TestInspectorFollowBadgeResumesAtLatest(t *testing.T) {
	_, s, tt := inspectorFollowTestState(t)
	s.openFileManagerFileExternal("t1", "a.txt")
	inspectorFollowWaitFile(t, tt, s, "a.txt")
	inspectorFollowUpdate(s, "t1", "read-1", "read", `{"path":"a.txt","start_line":30}`, "completed")
	inspectorFollowUpdate(s, "t1", "read-2", "read", `{"path":"sub/b.go","start_line":100}`, "completed")
	tt.Frame()
	if inspectorFollowBadge(s, "t1", sidePaneFileID) != 2 {
		t.Fatal("暂停时未累加徽标")
	}
	clickText(t, tt, "文件")
	inspectorFollowWaitFile(t, tt, s, "sub/b.go")
	if inspectorFollowPaused(s) || inspectorFollowBadge(s, "t1", sidePaneFileID) != 0 || !isFileManagerOpen(s) {
		t.Fatal("点击当前目的徽标未恢复跟随，或错误收起了右栏")
	}
	s.openFileManagerFileExternal("t1", "a.txt")
	if inspectorFollowBadge(s, "t1", sidePaneFileID) != 0 || !inspectorFollowPaused(s) {
		t.Fatal("手动 reveal 未接管或未清理文件徽标")
	}
}

func TestInspectorFollowDoesNotOpenHiddenPane(t *testing.T) {
	_, s, tt := inspectorFollowTestState(t)
	inspectorClose(s)
	inspectorFollowUpdate(s, "t1", "read-1", "read", `{"path":"a.txt"}`, "completed")
	time.Sleep(inspectorFollowInterval + 10*time.Millisecond)
	tt.Frame()
	if inspectorSession(s).Open || len(fileManagerTabPaths(s)) != 0 || inspectorFollowBadge(s, "t1", sidePaneFileID) != 1 {
		t.Fatal("agent 打开了收起的右栏或遗漏了徽标")
	}
	inspectorSelect(s, sidePaneDiffID, false)
	tt.Advance(DurSheet + 50*time.Millisecond)
	inspectorFollowUpdate(s, "t1", "read-2", "read", `{"path":"sub/b.go"}`, "completed")
	time.Sleep(inspectorFollowInterval + 10*time.Millisecond)
	tt.Frame()
	if inspectorSession(s).Active != sidePaneDiffID || len(fileManagerTabPaths(s)) != 0 {
		t.Fatal("读取事件切换了右栏目的")
	}
}

func TestInspectorFollowDeduplicatesToolUpdates(t *testing.T) {
	_, s, tt := inspectorFollowTestState(t)
	inspectorFollowPause(s, "t1")
	for range 3 {
		inspectorFollowUpdate(s, "t1", "same", "read", `{"path":"a.txt"}`, "running")
	}
	inspectorFollowUpdate(s, "t1", "same", "read", `{"path":"a.txt"}`, "completed")
	inspectorFollowUpdate(s, "t1", "same", "read", `{"path":"a.txt"}`, "completed")
	if got := inspectorFollowBadge(s, "t1", sidePaneFileID); got != 2 {
		t.Fatalf("重复工具增量未去重: %d", got)
	}
	clickText(t, tt, "文件")
	inspectorFollowWaitFile(t, tt, s, "a.txt")
	inspectorFollowUpdate(s, "t1", "batch-1", "read", `{"path":"sub/b.go"}`, "completed")
	inspectorFollowUpdate(s, "t1", "batch-2", "read", `{"path":"a.txt","start_line":40}`, "completed")
	tt.Frame()
	_, _, active := fileManagerSnapshot(s)
	if active != "a.txt" {
		t.Fatal("限频窗口内提前切换到了中间文件")
	}
	waitFor(t, tt, func() bool { return inspectorFollowBadge(s, "t1", sidePaneFileID) == 0 })
	fileManagerMu.Lock()
	first := getFileManagerStateForFollowTest(s).findTabLocked("a.txt").hlFirst
	fileManagerMu.Unlock()
	if first != 40 {
		t.Fatal("未合并到最新目标")
	}
}

// 调用方持 fileManagerMu，不再通过兼容访问器重复获取同一把锁。
func getFileManagerStateForFollowTest(s *chatState) *fileManagerState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return inspectorExistingSessionLocked(s, s.selectedThreadID).Files
}

func TestInspectorFollowRejectsStaleThreadAndUnsafePath(t *testing.T) {
	_, s, tt := inspectorFollowTestState(t)
	inspectorFollowUpdate(s, "deleted", "stale", "read", `{"path":"a.txt"}`, "completed")
	s.mu.Lock()
	stale := s.inspector.Sessions["deleted"]
	s.threads = append(s.threads, Thread{ID: "t2", ProjectID: "p1"})
	s.mu.Unlock()
	if stale != nil {
		t.Fatal("过期会话增量创建了右栏槽")
	}
	inspectorFollowUpdate(s, "t2", "other", "read", `{"path":"sub/b.go"}`, "completed")
	for i, path := range []string{"", "../secret", "sub/../a.txt", "/etc/passwd", "sub/…", "sub/file...", "a\n.txt"} {
		inspectorFollowUpdate(s, "t1", fmt.Sprint("unsafe-", i), "read", fmt.Sprintf(`{"path":%q}`, path), "completed")
	}
	inspectorFollowUpdate(s, "t1", "truncated", "read", `{"path":"a.txt`, "completed")
	time.Sleep(inspectorFollowInterval + 10*time.Millisecond)
	tt.Frame()
	if len(fileManagerTabPaths(s)) != 0 || inspectorFollowBadge(s, "t2", sidePaneFileID) != 1 || inspectorFollowBadge(s, "t1", sidePaneFileID) != 8 {
		t.Fatal("不安全路径被打开或跨会话活动串入当前会话")
	}
	inspectorFollowUpdate(s, "t1", "absolute", "read", `{"path":"/tmp/mcode-file-manager-test/a.txt"}`, "completed")
	inspectorFollowWaitFile(t, tt, s, "a.txt")
	s.mu.Lock()
	s.selectedThreadID = "t2"
	s.mu.Unlock()
	inspectorSwitchThread(s, "t1")
	if inspectorFollowBadge(s, "t2", sidePaneFileID) != 1 || len(fileManagerTabPaths(s)) != 0 {
		t.Fatal("切会话时丢失徽标或带入另一会话的文件")
	}
}

func TestInspectorFollowWriteRefreshesReviewWithoutChangingPurpose(t *testing.T) {
	fake := NewFakeClient()
	list, full := reviewFixture()
	fake.SetReviewSnapshot("t1", "turn", "", "", list)
	fake.SetReviewSnapshot("t1", "turn", "turn-1", "", list)
	fake.SetReviewSnapshot("t1", "turn", "turn-1", "a.go", full)
	s, tt, dp := newReviewTester(t, fake)
	inspectorFollowUpdate(s, "t1", "write", "write", `{"path":"a.go","content":"ignored"}`, "completed")
	waitFor(t, tt, func() bool {
		diffPanelMu.Lock()
		defer diffPanelMu.Unlock()
		return dp.selectedPath == "a.go" && !dp.loading
	})
	if inspectorFollowPaused(s) || inspectorSession(s).Active != sidePaneDiffID || inspectorFollowBadge(s, "t1", sidePaneDiffID) != 0 || inspectorFollowBadge(s, "t1", sidePaneFileID) != 1 {
		t.Fatal("写入事件未定位改动，或改变了目的/跟随状态")
	}
}

func TestInspectorFollowConcurrentUpdatesAndDispose(t *testing.T) {
	s := sidePaneTestState(t)
	st := inspectorSession(s)
	var wg sync.WaitGroup
	for worker := range 4 {
		wg.Go(func() {
			for i := range 30 {
				inspectorFollowUpdate(s, "t1", fmt.Sprintf("%d-%d", worker, i), "read", `{"path":"a.txt"}`, "completed")
				inspectorFollowPause(s, "t1")
			}
		})
	}
	wg.Wait()
	inspectorDeleteSession(s, "t1")
	s.mu.Lock()
	cleared := st.follow == nil
	s.mu.Unlock()
	s.close()
	if !cleared {
		t.Fatal("会话释放未清理跟随状态和计时器")
	}
}

func TestInspectorFollowReduceMotionJumpsToLine(t *testing.T) {
	_, s, tt := inspectorFollowTestState(t)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	inspectorFollowUpdate(s, "t1", "read-1", "read", `{"path":"a.txt","start_line":150}`, "completed")
	inspectorFollowWaitFile(t, tt, s, "a.txt")
	tt.Frame()
	for _, view := range getFileManagerState(s).codeViews {
		if view.state.ScrollY() <= 0 {
			t.Fatal("减少动效时未直接定位到目标行")
		}
	}
	if inspectorFollowPaused(s) {
		t.Fatal("自动定位被误判为用户滚动")
	}
}
