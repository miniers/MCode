package mcodeui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/agentconf"
	"mcode/internal/testenv"
)

func setupAttachEntryDownloads(t *testing.T, names ...string) {
	t.Helper()
	testenv.TempHome(t)
	clearDownloadsCache()
	dir := downloadsDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestAttachEntry_MenuOrderAndActions 验证入口浮层从上到下的顺序为：
// 选择文件或文件夹…、粘贴剪贴板内容、最近下载（含条目）、MCP 服务器；
// 并验证选择文件与粘贴剪贴板均调用各自既有入口。
func TestAttachEntry_MenuOrderAndActions(t *testing.T) {
	setupAttachEntryDownloads(t, "recent.txt")
	stubMCPMenuScan(t, []agentconf.MCPServer{{Agent: "codex", Scope: "project", Name: "demo"}})

	fake := NewFakeClient()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}},
		map[string][]Thread{"p1": {{ID: "t1", ProjectID: "p1", Title: "测试会话1", Provider: "codex", UpdatedAt: time.Now()}}},
	)
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.mu.Lock()
	s.projects = []Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1", Title: "测试会话1", Provider: "codex", UpdatedAt: time.Now()}}
	s.mu.Unlock()

	closeCount := 0
	tt := ui.NewTester(func(c *ui.Context) {
		renderAttachMenuPanel(c, s, "t1", func() {
			closeCount++
		})
	}, 420, 600)

	waitFor(t, tt, func() bool { return tt.HasText("recent.txt") })

	pickRect, ok := tt.Find("选择文件或文件夹…")
	if !ok {
		t.Fatal("缺少「选择文件或文件夹…」")
	}
	pasteRect, ok := tt.Find("粘贴剪贴板内容")
	if !ok {
		t.Fatal("缺少「粘贴剪贴板内容」")
	}
	dlRect, ok := tt.Find("最近下载")
	if !ok {
		t.Fatal("缺少「最近下载」")
	}
	fileRect, ok := tt.Find("recent.txt")
	if !ok {
		t.Fatal("默认展开状态下应可见 recent.txt")
	}
	mcpRect, ok := tt.Find("MCP 服务器")
	if !ok {
		t.Fatal("缺少「MCP 服务器」")
	}

	if !(pickRect.Y < pasteRect.Y && pasteRect.Y < dlRect.Y && dlRect.Y < fileRect.Y && fileRect.Y < mcpRect.Y) {
		t.Fatalf("浮层项纵向顺序不符合预期: pick=%v paste=%v dl=%v file=%v mcp=%v",
			pickRect.Y, pasteRect.Y, dlRect.Y, fileRect.Y, mcpRect.Y)
	}

	// 验证「选择文件或文件夹…」调用 promptChooseFiles 路径并关闭浮层。
	pickedFile := filepath.Join(t.TempDir(), "picked.txt")
	if err := os.WriteFile(pickedFile, []byte("picked"), 0644); err != nil {
		t.Fatal(err)
	}
	oldPicker := pickFilesDialog
	t.Cleanup(func() { pickFilesDialog = oldPicker })
	pickFilesDialog = func() ([]string, bool, error) {
		return []string{pickedFile}, true, nil
	}
	if err := tt.Click("选择文件或文件夹…"); err != nil {
		t.Fatalf("点击选择文件失败: %v", err)
	}
	if closeCount != 1 {
		t.Fatalf("点击选择文件后应关闭浮层: closeCount=%d", closeCount)
	}
	waitAttachmentFiles(t, s, 1, "")

	// 验证「粘贴剪贴板内容」调用 tryPasteImage / clipboardReader 路径并关闭浮层。
	oldClip := clipboardReader
	t.Cleanup(func() { clipboardReader = oldClip })
	clipCalled := 0
	clipboardReader = func() (bool, string, string, []byte, []string) {
		clipCalled++
		return true, "clip.png", "image/png", []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, nil
	}
	if err := tt.Click("粘贴剪贴板内容"); err != nil {
		t.Fatalf("点击粘贴剪贴板失败: %v", err)
	}
	if clipCalled != 1 {
		t.Fatalf("期望调用 clipboardReader 1 次，实际 %d", clipCalled)
	}
	if closeCount != 2 {
		t.Fatalf("点击粘贴剪贴板后应关闭浮层: closeCount=%d", closeCount)
	}
	s.mu.Lock()
	attCount := len(s.attachments)
	s.mu.Unlock()
	if attCount != 2 {
		t.Fatalf("期望新增剪贴板附件后共 2 个附件，实际 %d", attCount)
	}
}

// TestAttachEntry_DownloadsCollapseAndExpand 验证「最近下载」点击收起与展开，
// 且动画推进至结束后不再请求下一帧；开启减少动效时直接到位且不请求动画帧。
func TestAttachEntry_DownloadsCollapseAndExpand(t *testing.T) {
	prevReduce := appReduceMotion.Swap(false)
	t.Cleanup(func() { appReduceMotion.Store(prevReduce) })

	setupAttachEntryDownloads(t, "collapse-sample.txt")

	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)

	tt := ui.NewTester(func(c *ui.Context) {
		renderAttachMenuPanel(c, s, "", nil)
	}, 420, 600)

	waitFor(t, tt, func() bool { return tt.HasText("collapse-sample.txt") })

	// 收起
	if err := tt.Click("最近下载"); err != nil {
		t.Fatalf("点击最近下载标题收起失败: %v", err)
	}
	tt.Advance(motionUnfold + 50*time.Millisecond)
	if tt.HasText("collapse-sample.txt") {
		t.Fatalf("收起后不应显示下载列表项，当前文本: %q", tt.Texts())
	}
	if tt.FramePending() {
		t.Fatal("收起动画结束后不应继续请求下一帧")
	}

	// 再点展开
	if err := tt.Click("最近下载"); err != nil {
		t.Fatalf("点击最近下载标题展开失败: %v", err)
	}
	tt.Advance(motionUnfold + 50*time.Millisecond)
	if !tt.HasText("collapse-sample.txt") {
		t.Fatalf("再次点击后应展开显示下载列表项，当前文本: %q", tt.Texts())
	}
	if tt.FramePending() {
		t.Fatal("展开动画结束后不应继续请求下一帧")
	}

	// 减少动效模式下直接到终点且不挂起动画帧。
	appReduceMotion.Store(true)
	if err := tt.Click("最近下载"); err != nil {
		t.Fatalf("减少动效下收起失败: %v", err)
	}
	tt.Frame()
	if tt.HasText("collapse-sample.txt") {
		t.Fatal("减少动效下收起应立即隐藏列表")
	}
	if tt.FramePending() {
		t.Fatal("减少动效下收起不应请求下一帧")
	}
	if err := tt.Click("最近下载"); err != nil {
		t.Fatalf("减少动效下展开失败: %v", err)
	}
	tt.Frame()
	if !tt.HasText("collapse-sample.txt") {
		t.Fatal("减少动效下展开应立即显示列表")
	}
	if tt.FramePending() {
		t.Fatal("减少动效下展开不应请求下一帧")
	}
}

// TestAttachEntry_MCPSubpageTransition 验证切入「MCP 服务器」二级页与返回一级页的过渡在结束后不再请求帧。
func TestAttachEntry_MCPSubpageTransition(t *testing.T) {
	prevReduce := appReduceMotion.Swap(false)
	t.Cleanup(func() { appReduceMotion.Store(prevReduce) })

	setupAttachEntryDownloads(t, "mcp-back-sample.txt")
	stubMCPMenuScan(t, []agentconf.MCPServer{{Agent: "codex", Scope: "project", Name: "demo"}})

	fake := NewFakeClient()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}},
		map[string][]Thread{"p1": {{ID: "t1", ProjectID: "p1", Title: "测试会话1", Provider: "codex", UpdatedAt: time.Now()}}},
	)
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.mu.Lock()
	s.projects = []Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1", Title: "测试会话1", Provider: "codex", UpdatedAt: time.Now()}}
	s.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) {
		renderAttachMenuPanel(c, s, "t1", nil)
	}, 420, 600)
	waitFor(t, tt, func() bool { return tt.HasText("MCP 服务器") })

	if err := tt.Click("MCP 服务器"); err != nil {
		t.Fatalf("点击 MCP 服务器失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("demo · codex") })
	tt.Advance(DurSnap + 50*time.Millisecond)
	if tt.FramePending() {
		t.Fatal("MCP 二级页切入动画结束后不应继续请求下一帧")
	}

	if err := tt.Click("返回"); err != nil {
		t.Fatalf("点击返回失败: %v", err)
	}
	tt.Advance(DurSnap + 50*time.Millisecond)
	if !tt.HasText("选择文件或文件夹…") || !tt.HasText("最近下载") {
		t.Fatalf("返回后应回到一级页，当前文本: %q", tt.Texts())
	}
	if tt.FramePending() {
		t.Fatal("返回一级页过渡结束后不应继续请求下一帧")
	}
}

// TestAttachEntry_DropOverlayRulesFromConstants 验证拖放覆盖层显示由常量计算的 20 MB 与 50 MB 规则提示。
func TestAttachEntry_DropOverlayRulesFromConstants(t *testing.T) {
	prevReduce := appReduceMotion.Swap(false)
	t.Cleanup(func() { appReduceMotion.Store(prevReduce) })

	s := &chatState{}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("attach-entry-drop-overlay-root")).Fill().Children(func() {
			renderComposerDropOverlay(c, s)
		})
	}, 600, 300)

	tt.Frame()
	if tt.HasText("松开以添加附件") {
		t.Fatalf("未拖拽时不应显示拖放覆盖层，当前文本: %q", tt.Texts())
	}

	setComposerFileDragOver(s, true)
	tt.Frame()
	tt.Advance(motionFast + 50*time.Millisecond)

	wantSingle := formatFileSize(maxAttachmentBytes)
	wantTotal := formatFileSize(maxTotalAttachmentBytes)
	if wantSingle != "20 MB" || wantTotal != "50 MB" {
		t.Fatalf("常量格式化结果异常: single=%q total=%q", wantSingle, wantTotal)
	}

	for _, want := range []string{"松开以添加附件", wantSingle, wantTotal, "目录会自动展开为文件"} {
		if !tt.HasText(want) {
			t.Fatalf("拖放覆盖层缺少文案 %q，当前文本: %q", want, tt.Texts())
		}
	}
	if tt.FramePending() {
		t.Fatal("拖放覆盖层淡入动画结束后不应继续请求下一帧")
	}
}
