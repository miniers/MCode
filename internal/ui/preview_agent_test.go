package mcodeui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
)

func registerTestChatWindow(t *testing.T, s *chatState) *mygo.Window {
	t.Helper()
	win := &mygo.Window{}
	chatWindowsMu.Lock()
	chatWindows[win] = s
	chatWindowsMu.Unlock()
	t.Cleanup(func() {
		chatWindowsMu.Lock()
		delete(chatWindows, win)
		chatWindowsMu.Unlock()
	})
	return win
}

func TestPreviewOpenRequiresWindow(t *testing.T) {
	err := OpenPreviewForThread("t1", "http://localhost:5173/")
	if err == nil || !strings.Contains(err.Error(), "桌面窗口未打开") {
		t.Fatalf("无聊天窗口时应返回桌面窗口未打开, 实际: %v", err)
	}
}

func TestPreviewOpenPendingWhenNotCurrentThread(t *testing.T) {
	s := &chatState{selectedThreadID: "t1"}
	defer inspectorDisposeAllSessions(s)
	registerTestChatWindow(t, s)

	if err := OpenPreviewForThread("t2", "docs/design.html"); err != nil {
		t.Fatalf("记录非当前会话预览失败: %v", err)
	}

	s.mu.Lock()
	slot := s.inspector.Sessions["t2"]
	var pending string
	if slot != nil {
		pending = slot.pendingPreview
	}
	s.mu.Unlock()

	if pending != "docs/design.html" {
		t.Fatalf("pendingPreview=%q, want %q", pending, "docs/design.html")
	}
	if inspectorPaneOpen(s, sidePanePreviewID) {
		t.Fatal("非当前会话记录预览时不应打开当前预览面板")
	}
}

func TestPreviewOpenOverwritePending(t *testing.T) {
	s := &chatState{selectedThreadID: "t1"}
	defer inspectorDisposeAllSessions(s)
	registerTestChatWindow(t, s)

	if err := OpenPreviewForThread("t2", "first.html"); err != nil {
		t.Fatal(err)
	}
	if err := OpenPreviewForThread("t2", "http://localhost:5173/"); err != nil {
		t.Fatal(err)
	}

	s.mu.Lock()
	got := s.inspector.Sessions["t2"].pendingPreview
	s.mu.Unlock()
	if got != "http://localhost:5173/" {
		t.Fatalf("第二次写入应覆盖第一次, got=%q", got)
	}
}

func TestPreviewOpenConsumeClearsPending(t *testing.T) {
	client := &inspectorPreviewTestClient{
		FakeClient: NewFakeClient(),
		target:     "http://127.0.0.1:4321/p/tok/docs/design.html",
	}
	s := &chatState{client: client, selectedThreadID: "t1"}
	defer inspectorDisposeAllSessions(s)

	// 1. 非当前会话先写入 URL，切过去后消费并清空。
	ok, isCurrent := setPendingPreview(s, "t2", "http://localhost:5173/")
	if !ok || isCurrent {
		t.Fatalf("setPendingPreview ok=%v isCurrent=%v, want true/false", ok, isCurrent)
	}
	s.mu.Lock()
	s.selectedThreadID = "t2"
	s.mu.Unlock()
	inspectorSwitchThread(s, "t1")

	s.mu.Lock()
	afterURL := s.inspector.Sessions["t2"].pendingPreview
	s.mu.Unlock()
	if afterURL != "" {
		t.Fatalf("消费后 pendingPreview 应清空, got=%q", afterURL)
	}
	if !inspectorPaneOpen(s, sidePanePreviewID) {
		t.Fatal("消费 URL 后应打开预览面板")
	}
	tab := getPreviewState(s)
	previewMu.Lock()
	gotURL := ""
	if tab != nil {
		gotURL = tab.url
	}
	previewMu.Unlock()
	if gotURL != "http://localhost:5173/" {
		t.Fatalf("预览 URL 不符合预期: got=%q", gotURL)
	}

	// 2. 当前会话消费本地文件，应清空 pendingPreview 并请求 InspectorPreviewURL。
	ok, isCurrent = setPendingPreview(s, "t2", "docs/design.html")
	if !ok || !isCurrent {
		t.Fatalf("setPendingPreview ok=%v isCurrent=%v, want true/true", ok, isCurrent)
	}
	consumePendingPreview(s, "t2")

	s.mu.Lock()
	afterFile := s.inspector.Sessions["t2"].pendingPreview
	s.mu.Unlock()
	if afterFile != "" {
		t.Fatalf("消费文件后 pendingPreview 应清空, got=%q", afterFile)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if calls := client.callsSnapshot(); len(calls) == 1 {
			if calls[0].threadID != "t2" || calls[0].path != "docs/design.html" {
				t.Fatalf("InspectorPreviewURL 参数异常: %+v", calls[0])
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if calls := client.callsSnapshot(); len(calls) != 1 {
		t.Fatalf("期望调用 1 次 InspectorPreviewURL, 实际 %d 次", len(calls))
	}

	// 3. 客户端不支持本地预览时弹出提示并清空 pendingPreview。
	plain := &chatState{client: NewFakeClient(), selectedThreadID: "t3"}
	defer inspectorDisposeAllSessions(plain)
	setPendingPreview(plain, "t3", "docs/design.html")
	consumePendingPreview(plain, "t3")

	plain.mu.Lock()
	toast := plain.toastMessage
	rem := plain.inspector.Sessions["t3"].pendingPreview
	plain.mu.Unlock()
	if rem != "" {
		t.Fatalf("失败提示后 pendingPreview 也应已清空, got=%q", rem)
	}
	if toast != "无法打开预览" {
		t.Fatalf("toast=%q, want %q", toast, "无法打开预览")
	}
}

func TestPreviewOpenPrefersViewingWindow(t *testing.T) {
	s1 := &chatState{selectedThreadID: "t1"}
	s2 := &chatState{selectedThreadID: "t2"}
	defer inspectorDisposeAllSessions(s1)
	defer inspectorDisposeAllSessions(s2)
	registerTestChatWindow(t, s1)
	registerTestChatWindow(t, s2)

	if err := OpenPreviewForThread("t1", "http://localhost:5173/"); err != nil {
		t.Fatalf("OpenPreviewForThread 失败: %v", err)
	}

	s1.mu.Lock()
	st1 := s1.inspector.Sessions["t1"]
	s1.mu.Unlock()
	s2.mu.Lock()
	st2 := s2.inspector.Sessions["t2"]
	s2.mu.Unlock()

	if st1 == nil || st1.pendingPreview != "http://localhost:5173/" {
		t.Fatalf("应写入正在查看 t1 的窗口, st1=%+v", st1)
	}
	if st2 != nil && st2.pendingPreview != "" {
		t.Fatalf("不应写入其他窗口, st2=%+v", st2)
	}
}
