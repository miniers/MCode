package mcodeui

import (
	"os"
	"path/filepath"
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
	workDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workDir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "docs", "design.html"), []byte("<h1>hi</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	client := NewFakeClient()
	client.SetThreadWorkDir(workDir)
	s := &chatState{client: client, selectedThreadID: "t1"}
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
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "first.html"), []byte("<h1>1</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	client := NewFakeClient()
	client.SetThreadWorkDir(workDir)
	s := &chatState{client: client, selectedThreadID: "t1"}
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

func TestPreviewOpenResolveAgentTarget(t *testing.T) {
	workDir := t.TempDir()
	absHTML := filepath.Join(workDir, "docs", "a.html")
	if err := os.MkdirAll(filepath.Dir(absHTML), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absHTML, []byte("<h1>a</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}

	rel, err := resolveAgentPreviewTarget(workDir, "docs/a.html")
	if err != nil || rel != "docs/a.html" {
		t.Fatalf("相对路径解析失败: rel=%q err=%v", rel, err)
	}

	rel, err = resolveAgentPreviewTarget(workDir, absHTML)
	if err != nil || rel != "docs/a.html" {
		t.Fatalf("绝对路径解析失败: rel=%q err=%v", rel, err)
	}

	if _, err := resolveAgentPreviewTarget(workDir, "../x.html"); err == nil {
		t.Fatal("工作区外路径应返回错误")
	}
	if _, err := resolveAgentPreviewTarget(workDir, "docs/missing.html"); err == nil {
		t.Fatal("不存在的 HTML 文件应返回错误")
	}
	if _, err := resolveAgentPreviewTarget("", "docs/a.html"); err == nil {
		t.Fatal("空 workDir 应返回错误")
	}
}

func TestPreviewOpenBeeRoutesToLead(t *testing.T) {
	t.Run("lead_currently_displayed", func(t *testing.T) {
		workDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(workDir, "docs"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workDir, "docs", "design.html"), []byte("<h1>bee</h1>"), 0o644); err != nil {
			t.Fatal(err)
		}
		client := &inspectorPreviewTestClient{
			FakeClient: NewFakeClient(),
			target:     "http://127.0.0.1:4321/p/bee/docs/design.html",
		}
		client.SetThreadWorkDir(workDir)
		s := &chatState{client: client, selectedThreadID: "lead-1"}
		defer inspectorDisposeAllSessions(s)
		registerTestChatWindow(t, s)

		swarmPanelMu.Lock()
		swarmPanelStates[s] = &swarmPanelState{
			state: SwarmState{
				LeadThreadID: "lead-1",
				Bees:         []SwarmBee{{ThreadID: "bee-1", Name: "Aspen", Status: "running"}},
			},
		}
		swarmPanelMu.Unlock()
		t.Cleanup(func() {
			swarmPanelMu.Lock()
			delete(swarmPanelStates, s)
			swarmPanelMu.Unlock()
		})

		if err := OpenPreviewForThread("bee-1", "docs/design.html"); err != nil {
			t.Fatalf("OpenPreviewForThread 失败: %v", err)
		}

		s.mu.Lock()
		leadSlot := s.inspector.Sessions["lead-1"]
		beeSlot := s.inspector.Sessions["bee-1"]
		var pending, pendingBee string
		if leadSlot != nil {
			pending = leadSlot.pendingPreview
			pendingBee = leadSlot.pendingPreviewThreadID
		}
		s.mu.Unlock()
		if beeSlot != nil && beeSlot.pendingPreview != "" {
			t.Fatalf("不应写进 bee 槽, got=%q", beeSlot.pendingPreview)
		}
		if pending != "docs/design.html" || pendingBee != "bee-1" {
			t.Fatalf("主会话待打开项错误: pending=%q bee=%q", pending, pendingBee)
		}

		consumePendingPreview(s, "lead-1")
		waitInspectorPreview(t, s, "bee-1", "docs/design.html")

		calls := client.callsSnapshot()
		if len(calls) != 1 || calls[0].threadID != "bee-1" || calls[0].path != "docs/design.html" {
			t.Fatalf("InspectorPreviewURL 参数错误: %+v", calls)
		}
		tab := getPreviewState(s)
		previewMu.Lock()
		gotPreviewThread := ""
		if tab != nil {
			gotPreviewThread = tab.previewThreadID
		}
		previewMu.Unlock()
		if gotPreviewThread != "bee-1" {
			t.Fatalf("标签 previewThreadID=%q, want %q", gotPreviewThread, "bee-1")
		}
	})

	t.Run("lead_not_displayed_remembers_bee_until_switch", func(t *testing.T) {
		workDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(workDir, "docs"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workDir, "docs", "design.html"), []byte("<h1>bee2</h1>"), 0o644); err != nil {
			t.Fatal(err)
		}
		client := &inspectorPreviewTestClient{
			FakeClient: NewFakeClient(),
			target:     "http://127.0.0.1:4321/p/bee2/docs/design.html",
		}
		client.SetThreadWorkDir(workDir)
		s := &chatState{
			client:           client,
			selectedThreadID: "other",
			threads: []Thread{
				{ID: "lead-2", Title: "主会话"},
				{ID: "bee-2", ParentThreadID: "lead-2", IsHelper: true, Title: "Birch"},
			},
		}
		defer inspectorDisposeAllSessions(s)
		registerTestChatWindow(t, s)

		if err := OpenPreviewForThread("bee-2", "docs/design.html"); err != nil {
			t.Fatalf("OpenPreviewForThread 失败: %v", err)
		}

		s.mu.Lock()
		leadSlot := s.inspector.Sessions["lead-2"]
		var pending, pendingBee string
		if leadSlot != nil {
			pending = leadSlot.pendingPreview
			pendingBee = leadSlot.pendingPreviewThreadID
		}
		s.mu.Unlock()
		if pending != "docs/design.html" || pendingBee != "bee-2" {
			t.Fatalf("待打开项未记住来源 bee: pending=%q bee=%q", pending, pendingBee)
		}
		if inspectorPaneOpen(s, sidePanePreviewID) {
			t.Fatal("主会话不在显示时不应打开当前预览面板")
		}

		s.mu.Lock()
		s.selectedThreadID = "lead-2"
		s.mu.Unlock()
		inspectorSwitchThread(s, "other")
		waitInspectorPreview(t, s, "bee-2", "docs/design.html")

		calls := client.callsSnapshot()
		if len(calls) != 1 || calls[0].threadID != "bee-2" || calls[0].path != "docs/design.html" {
			t.Fatalf("切回主会话后应按 bee 打开预览: %+v", calls)
		}
		tab := getPreviewState(s)
		previewMu.Lock()
		gotPreviewThread := ""
		if tab != nil {
			gotPreviewThread = tab.previewThreadID
		}
		previewMu.Unlock()
		if gotPreviewThread != "bee-2" {
			t.Fatalf("切回主会话后标签 previewThreadID=%q, want %q", gotPreviewThread, "bee-2")
		}
	})
}

func TestPreviewOpenReloadsExisting(t *testing.T) {
	client := &inspectorPreviewTestClient{
		FakeClient: NewFakeClient(),
		target:     "http://127.0.0.1:4321/p/bee/docs/design.html",
	}
	s := newInspectorPreviewState(t, client)
	s.mu.Lock()
	s.selectedThreadID = "lead-1"
	s.mu.Unlock()

	if !s.openInspectorPreview("bee-1", "docs/design.html") {
		t.Fatal("首次打开本地预览失败")
	}
	waitInspectorPreview(t, s, "bee-1", "docs/design.html")
	first := getPreviewState(s)
	page := inspectorAttachPage(s)

	if !s.openInspectorPreview("lead-1", "docs/other.html") {
		t.Fatal("打开第二标签失败")
	}
	waitInspectorPreview(t, s, "lead-1", "docs/other.html")
	if getPreviewState(s) == first {
		t.Fatal("未切换到新标签")
	}

	if !s.openInspectorPreview("bee-1", "docs/design.html") {
		t.Fatal("再次打开已有标签失败")
	}
	if getPreviewState(s) != first {
		t.Fatal("未激活已有标签")
	}
	if calls := client.callsSnapshot(); len(calls) != 2 {
		t.Fatalf("命中已有标签不应重新签发 URL, calls=%+v", calls)
	}
	if got := page.loaded.Load(); got != 1 {
		t.Fatalf("命中已有标签后应重新加载页面 1 次, 实际 %d 次", got)
	}
}
