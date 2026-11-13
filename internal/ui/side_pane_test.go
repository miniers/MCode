package mcodeui

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func TestSidePaneWindowsChatBounds(t *testing.T) {
	withTitleBarGOOS(t, "windows")
	s := sidePaneTestState(t)
	defer s.close()
	message := strings.Repeat("长消息", 100)
	s.mu.Lock()
	s.messages = []Message{{ID: "m1", Role: "user", Text: message, TurnIndex: 1}}
	s.mu.Unlock()
	stubSidePanePanelRender(t, sidePaneFileID)
	openFilePaneForTest(t, s)
	tt := ui.NewTester(func(c *ui.Context) {
		renderSidePaneSplit(c, s, func() { renderChat(c, s) })
	}, 900, 700)
	tt.SetTitleBar(ui.TitleBar{Height: HeaderHeight, Right: 138})
	tt.Frame()
	handle, ok := tt.Find(sidePaneHandleLabel)
	if !ok {
		t.Fatal("找不到分栏手柄")
	}
	node, ok := tt.Find(message)
	if !ok {
		t.Fatal("找不到用户消息")
	}
	if node.X+node.W > handle.X {
		t.Errorf("用户消息右缘 %v 超出聊天主区右缘 %v", node.X+node.W, handle.X)
	}
}

// sidePaneTestState 构造一个带工作目录的 chatState，供三个右侧面板的互斥与优先级用例使用。
func sidePaneTestState(t *testing.T) *chatState {
	t.Helper()
	testenv.TempHome(t)
	fake := NewFakeClient()
	workDir := filepath.Join(t.TempDir(), "workspace")
	fake.SetThreadWorkDir(workDir)
	fake.SetFileTree(fileManagerTestTree())
	fake.SetFileContents(map[string]FileContent{
		"a.txt": {Path: "a.txt", Kind: "text", Text: "hello", TotalSize: 5},
	})
	s := newChatState(fake)
	s.mu.Lock()
	s.projects = []Project{{ID: "p1", Name: "mcode-test", Path: workDir}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1", Title: "测试会话1"}}
	s.selectedThreadID = "t1"
	s.mu.Unlock()
	return s
}

// paneOpenFlags 读取三个面板的展开状态（各自锁内读，避免与渲染并发时读到半更新状态）。
func paneOpenFlags(s *chatState) (file, preview, diff bool) {
	fileManagerMu.Lock()
	file = fileManagerPaneOpen(s)
	fileManagerMu.Unlock()
	previewMu.Lock()
	preview = previewPaneOpen(s)
	previewMu.Unlock()
	diffPanelMu.Lock()
	diff = diffPaneOpen(s)
	diffPanelMu.Unlock()
	return file, preview, diff
}

// 打开任意一个面板都应关掉另外两个，包括改动与预览之间（旧实现里这两个可以同时为真）。
func TestSidePaneMutualExclusion(t *testing.T) {
	cases := []struct {
		name string
		open func(s *chatState)
		want string
	}{
		{"file", toggleFileManager, sidePaneFileID},
		{"preview", togglePreview, sidePanePreviewID},
		{"diff", func(s *chatState) { s.toggleDiffPanel() }, sidePaneDiffID},
		{"git", toggleGitPane, sidePaneGitID},
	}
	// 每个用例都从“另一个面板已打开”开始，覆盖所有有向组合。
	for _, from := range cases {
		for _, to := range cases {
			if from.name == to.name {
				continue
			}
			t.Run(from.name+"->"+to.name, func(t *testing.T) {
				s := sidePaneTestState(t)
				defer s.close()
				from.open(s)
				to.open(s)

				file, preview, diff := paneOpenFlags(s)
				got := map[string]bool{sidePaneFileID: file, sidePanePreviewID: preview, sidePaneDiffID: diff, sidePaneGitID: inspectorPaneOpen(s, sidePaneGitID)}
				if !got[to.want] {
					t.Fatalf("打开 %s 后它应为打开状态, got %+v", to.want, got)
				}
				if got[from.name] {
					t.Fatalf("打开 %s 后 %s 应被关闭, got %+v", to.name, from.name, got)
				}
			})
		}
	}
}

// Active 独立于注册顺序；只有选中的目的会显示。
func TestActiveSidePanePriority(t *testing.T) {
	s := sidePaneTestState(t)
	defer s.close()

	if p := activeSidePane(s); p != nil {
		t.Fatalf("没有面板打开时应返回 nil, got %q", p.id)
	}

	for _, id := range []string{sidePaneDiffID, sidePaneGitID, sidePanePreviewID, sidePaneFileID} {
		inspectorSelect(s, id, false)
		if p := activeSidePane(s); p == nil || p.id != id {
			t.Fatalf("Active=%s, got %+v", id, p)
		}
		inspectorSelect(s, id, false)
		if activeSidePane(s) == nil {
			t.Fatal("重复点击目的标签不应收起")
		}
	}
}

// 宽度夹取：小于最小值回落到默认宽度，大于最大值取上限，范围内保持原值。
func TestSidePaneWidthClamp(t *testing.T) {
	s := sidePaneTestState(t)
	defer s.close()

	if getFileManagerState(s) == nil {
		t.Fatal("expected file manager state")
	}
	cases := []struct {
		name string
		in   float32
		want float32
	}{
		{"低于最小", inspectorMinWidth - 20, inspectorDefaultWidth},
		{"高于最大", inspectorMaxWidth + 200, inspectorMaxWidth},
		{"区间内", inspectorMinWidth + 40, inspectorMinWidth + 40},
	}
	p := sidePaneByID(sidePaneFileID)
	if p == nil {
		t.Fatal("注册表里缺少文件管理面板")
	}
	for _, tc := range cases {
		inspectorSession(s).Width = tc.in

		ptr := p.widthPointer(s)
		if ptr == nil {
			t.Fatalf("%s: widthPointer 返回 nil", tc.name)
		}
		if *ptr != tc.want {
			t.Fatalf("%s: 夹取后宽度 = %v, want %v", tc.name, *ptr, tc.want)
		}
	}

	// 状态不存在时不应新建，直接返回 nil。
	empty := &chatState{}
	if ptr := p.widthPointer(empty); ptr != nil {
		t.Fatalf("没有面板状态时应返回 nil, got %v", *ptr)
	}
	fileManagerMu.Lock()
	_, created := fileManagerStates[empty]
	fileManagerMu.Unlock()
	if created {
		t.Fatal("widthPointer 不应新建面板状态")
	}
}

// sidePaneByID 按 ID 取注册表中的面板。
func sidePaneByID(id string) *sidePane {
	for i := range sidePanes {
		if sidePanes[i].id == id {
			return &sidePanes[i]
		}
	}
	return nil
}

// 宽度偏好往返：按会话写进 inspector_sessions，旧 side_pane_widths 只读不写。
func TestSidePaneWidthPreferenceRoundTrip(t *testing.T) {
	dataDir := settingsTestDir(t)
	t.Setenv("MCODE_DATA", dataDir)
	testenv.TempHome(t)
	t.Cleanup(waitUIStateWrites)

	s := newChatState(NewFakeClient())
	defer s.close()
	WithUIStatePersistence()(s)
	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.mu.Unlock()

	pref := inspectorSession(s)
	s.mu.Lock()
	pref.Width = 620
	s.mu.Unlock()
	inspectorRemember(s)
	waitUIStateWrites()

	file, ok := loadUIStateFile()
	if !ok {
		t.Fatal("ui-state.json 应已写入")
	}
	if got := file.InspectorSessions["t1"].Width; got != 620 {
		t.Fatalf("inspector_sessions.t1.width = %v, want 620", got)
	}
	if len(file.SidePaneWidths) != 0 {
		t.Fatalf("旧键 side_pane_widths 不应被写入: %v", file.SidePaneWidths)
	}

	// JSON 往返：字段名固定为 side_pane_widths。
	data, err := json.Marshal(UIState{SidePaneWidths: map[string]float32{sidePaneDiffID: 360}})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var back UIState
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if back.SidePaneWidths[sidePaneDiffID] != 360 {
		t.Fatalf("side_pane_widths 往返丢失: %s", string(data))
	}

	// 恢复：新窗口拿到偏好后，首次创建状态就用偏好宽度（越界时夹到该面板上限）。
	restored := loadUIState()
	other := newChatState(NewFakeClient())
	defer other.close()
	other.mu.Lock()
	other.selectedThreadID = "t1"
	other.mu.Unlock()
	applyUIState(other, restored)
	if width := inspectorSession(other).Width; width != 620 {
		t.Fatalf("恢复后的共享宽度应为 620, got %v", width)
	}

	// 偏好越界时按优先级夹到最大宽度。
	outOfRange := newChatState(NewFakeClient())
	defer outOfRange.close()
	setSidePaneWidthPreferences(outOfRange, map[string]float32{sidePanePreviewID: PreviewPanelMaxWidth + 300})
	previewWidth := inspectorSession(outOfRange).Width
	if previewWidth != inspectorMaxWidth {
		t.Fatalf("越界偏好应夹到 %v, got %v", inspectorMaxWidth, previewWidth)
	}
}

// 关窗后三个面板状态都不再残留（含此前漏删的 fileManagerStates）。
func TestWindowCloseReleasesSidePaneStates(t *testing.T) {
	s := newChatState(NewFakeClient())
	_ = getFileManagerState(s)
	_ = getPreviewState(s)
	_ = getDiffPanelState(s)
	setSidePaneWidthPreferences(s, map[string]float32{sidePaneFileID: 400})

	s.close()

	fileManagerMu.Lock()
	_, fileLeft := fileManagerStates[s]
	fileManagerMu.Unlock()
	if fileLeft {
		t.Fatal("关窗后 fileManagerStates 不应残留")
	}
	previewMu.Lock()
	_, previewLeft := previewStates[s]
	previewMu.Unlock()
	if previewLeft {
		t.Fatal("关窗后 previewStates 不应残留")
	}
	diffPanelMu.Lock()
	_, diffLeft := diffPanelStates[s]
	diffPanelMu.Unlock()
	if diffLeft {
		t.Fatal("关窗后 diffPanelStates 不应残留")
	}
	sidePanePrefsMu.Lock()
	_, prefLeft := sidePanePrefs[s]
	sidePanePrefsMu.Unlock()
	if prefLeft {
		t.Fatal("关窗后侧栏宽度偏好不应残留")
	}
}

// 未开启持久化的窗口拖拽分栏不写盘（沿用 WithUIStatePersistence 的开关语义）。
func TestSidePaneWidthPersistSkippedWithoutPersistence(t *testing.T) {
	dataDir := settingsTestDir(t)
	t.Setenv("MCODE_DATA", dataDir)
	testenv.TempHome(t)
	t.Cleanup(waitUIStateWrites)

	s := newChatState(NewFakeClient())
	defer s.close()
	getFileManagerState(s)
	persistUIState(s)
	waitUIStateWrites()

	if _, ok := loadUIStateFile(); ok {
		t.Fatal("未开启 WithUIStatePersistence 的窗口不应写盘")
	}
}

// 保证注册表覆盖三个面板且顺序即优先级。
func TestSidePanesRegistryOrder(t *testing.T) {
	want := []string{sidePaneFileID, sidePaneDiffID, sidePaneGitID, sidePanePreviewID}
	if len(sidePanes) != len(want) {
		t.Fatalf("注册表应包含 %d 个面板, got %d", len(want), len(sidePanes))
	}
	for i, id := range want {
		if sidePanes[i].id != id {
			t.Fatalf("第 %d 个面板 = %q, want %q", i, sidePanes[i].id, id)
		}
		if sidePanes[i].splitKey == "" || sidePanes[i].render == nil {
			t.Fatalf("面板 %q 缺少 splitKey/close/render", id)
		}
	}
	// 预览面板的关闭沿用旧语义：未打开时不调 closePreview（不销毁 WebView），
	// 只确保状态存在并保持不可见。
	pv := sidePaneByID(sidePanePreviewID)
	empty := &chatState{}
	pv.close(empty)
	previewMu.Lock()
	stEmpty := previewStates[empty]
	delete(previewStates, empty)
	previewMu.Unlock()
	if stEmpty != nil && stEmpty.visible {
		t.Fatal("关闭未打开的预览面板后不应变为可见")
	}
}

// 注册表里每个面板都要带齐开关入口的注册信息，且动作 id 必须在快捷键表里找得到，
// 否则菜单 Accelerator 会退化成 fallback、快捷键也触发不到该面板。
func TestSidePaneEntryPointRegistry(t *testing.T) {
	shortcutIDs := make(map[string]bool)
	for _, sc := range shortcutDefaults() {
		shortcutIDs[sc.id] = true
	}
	for i := range sidePanes {
		p := &sidePanes[i]
		if p.actionID == "" || p.menuLabel == "" || p.menuAccel == "" ||
			p.paletteID == "" || p.paletteTitle == "" || p.toggle == nil {
			t.Fatalf("面板 %q 的开关入口注册信息不完整: %+v", p.id, p)
		}
		if !shortcutIDs[p.actionID] {
			t.Fatalf("面板 %q 的 actionID %q 不在 shortcutDefaults 中", p.id, p.actionID)
		}
		if _, _, ok := shortcutBindingFor(p.actionID); !ok {
			t.Fatalf("面板 %q 的 actionID %q 没有有效绑定", p.id, p.actionID)
		}
	}
}

// sidePaneByAction 命中三个动作 id，未知 id 返回 nil。
func TestSidePaneByAction(t *testing.T) {
	cases := map[string]string{
		"toggle-file-manager": sidePaneFileID,
		"toggle-preview":      sidePanePreviewID,
		"toggle-diff":         sidePaneDiffID,
		"toggle-git-pane":     sidePaneGitID,
	}
	for actionID, wantID := range cases {
		p := sidePaneByAction(actionID)
		if p == nil {
			t.Fatalf("sidePaneByAction(%q) 返回 nil", actionID)
		}
		if p.id != wantID {
			t.Fatalf("sidePaneByAction(%q).id = %q, want %q", actionID, p.id, wantID)
		}
	}
	if p := sidePaneByAction(""); p != nil {
		t.Fatalf("sidePaneByAction(\"\") 应返回 nil, got %q", p.id)
	}
	if p := sidePaneByAction("toggle-quantum"); p != nil {
		t.Fatalf("未知动作 id 应返回 nil, got %q", p.id)
	}
}

// 注册表生成的命令面板项数量与 id/标题/kind 必须与注册表一致，且 run 真的能开关面板。
func TestSidePanePaletteItems(t *testing.T) {
	items := sidePanePaletteItems()
	if len(items) != len(sidePanes)+1 {
		t.Fatalf("命令面板项应有 %d 条, got %d", len(sidePanes)+1, len(items))
	}
	s := sidePaneTestState(t)
	defer s.close()

	for i := range sidePanes {
		p := &sidePanes[i]
		it := items[i]
		title := "显示/隐藏" + p.title
		if p.id == sidePaneGitID {
			title = p.paletteTitle
		}
		if it.id != p.paletteID || it.title != title || it.kind != "command" {
			t.Fatalf("第 %d 条命令面板项 = {%q %q %q}, want {%q %q command}",
				i, it.id, it.title, it.kind, p.paletteID, "显示/隐藏"+p.title)
		}
		if it.run == nil {
			t.Fatalf("命令面板项 %q 缺少 run", it.id)
		}
		it.run(s)
		if got := activeSidePane(s); got == nil || got.id != p.id {
			t.Fatalf("执行命令面板项 %q 后应打开面板 %q, got %+v", it.id, p.id, got)
		}
	}
}

// 注册表生成的菜单项数量与标签、点击派发的动作 id 都要与注册表一致。
func TestSidePaneMenuItems(t *testing.T) {
	items := sidePaneMenuItems()
	if len(items) != len(sidePanes) {
		t.Fatalf("菜单项应有 %d 条, got %d", len(sidePanes), len(items))
	}
	for i := range sidePanes {
		p := &sidePanes[i]
		it := items[i]
		label := "显示/隐藏" + p.title
		if p.id == sidePaneGitID {
			label = p.menuLabel
		}
		if it.Label != label {
			t.Fatalf("第 %d 个菜单项标签 = %q, want %q", i, it.Label, "显示/隐藏"+p.title)
		}
		if it.Accelerator == "" || it.Click == nil {
			t.Fatalf("菜单项 %q 缺少 Accelerator 或 Click", it.Label)
		}
	}
}

// --- 分栏拖拽手柄（任务 D）---

// sidePaneSplitTestWidth/Height 是分栏测试窗口的尺寸，足够放下 420 DIP 的默认面板宽度。
const (
	sidePaneSplitTestWidth  float32 = 1000
	sidePaneSplitTestHeight float32 = 700
)

// stubSidePanePanelRender 把注册表里某个面板的渲染换成占位文本，避免分栏测试依赖真实面板内容。
// sidePanes 是包级注册表，测试串行执行，收尾时还原。
func stubSidePanePanelRender(t *testing.T, id string) {
	t.Helper()
	p := sidePaneByID(id)
	if p == nil {
		t.Fatalf("注册表里缺少面板 %q", id)
	}
	orig := p.render
	p.render = func(c *ui.Context, s *chatState) {
		ui.Text(c, "面板占位")
	}
	t.Cleanup(func() { p.render = orig })
}

// newSidePaneSplitTester 建一个只渲染分栏的 headless 窗口：主区与面板都是占位文本。
// 视图里不建任何会自己请求帧的东西（例如带 AfterInput 的滚动容器），否则
// AnimateTo 会被 ReduceMotion 直接跳到目标值，高亮线看不到淡入。
func newSidePaneSplitTester(t *testing.T, s *chatState) *ui.Tester {
	t.Helper()
	tt := ui.NewTester(func(c *ui.Context) {
		renderSidePaneSplit(c, s, func() { ui.Text(c, "会话主区") })
	}, int(sidePaneSplitTestWidth), int(sidePaneSplitTestHeight))
	tt.Frame()
	return tt
}

// openFilePaneForTest 直接把文件管理面板置为展开（不走 toggleFileManager 的目录加载），
// 返回展开时的面板宽度。
func openFilePaneForTest(t *testing.T, s *chatState) float32 {
	t.Helper()
	if getFileManagerState(s) == nil {
		t.Fatal("文件管理面板状态应可创建")
	}
	inspectorSelect(s, sidePaneFileID, false)
	return inspectorSession(s).Width
}

func TestInspectorTopInsetByPlatform(t *testing.T) {
	for _, tc := range []struct {
		name      string
		goos      string
		bar       ui.TitleBar
		width     int
		inset     float32
		tabHeight float32
		overlay   bool
	}{
		{"macOS", "darwin", ui.TitleBar{Height: 32, Left: 78}, 1000, 0, HeaderHeight, false},
		{"Windows 窗口", "windows", ui.TitleBar{Height: 53, Right: 138}, 1000, HeaderHeight, diffHeaderHeight, false},
		{"Windows 全屏", "windows", ui.TitleBar{}, 1000, 0, HeaderHeight, false},
		{"macOS 浮层", "darwin", ui.TitleBar{}, 600, HeaderHeight, diffHeaderHeight, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withTitleBarGOOS(t, tc.goos)
			s := sidePaneTestState(t)
			defer s.close()
			stubSidePanePanelRender(t, sidePaneFileID)
			openFilePaneForTest(t, s)
			tt := ui.NewTester(func(c *ui.Context) {
				renderSidePaneSplit(c, s, func() {
					ui.Column(c).Fill().Children(func() {
						ui.Row(c.Key("chat-header")).FillWidth().Height(HeaderHeight).Label("测试聊天标题栏")
					})
				})
			}, tc.width, 700)
			tt.SetTitleBar(tc.bar)
			tt.Advance(DurSheet + 50*time.Millisecond)
			chat, ok := tt.Find("测试聊天标题栏")
			if !ok {
				t.Fatal("找不到聊天标题栏")
			}
			tab, ok := tt.Find("文件")
			if !ok {
				t.Fatal("找不到文件标签")
			}
			if tab.Y != chat.Y+tc.inset || tab.H != tc.tabHeight {
				t.Fatalf("标签位置和高度 = %+v，聊天标题栏 = %+v，期望顶部偏移 %v、高度 %v", tab, chat, tc.inset, tc.tabHeight)
			}
			if tc.overlay {
				img := tt.Image()
				x, y := tc.width-3, int(tab.Y+tab.H)-1
				if img.At(x, y) == img.At(x, y-1) {
					t.Fatal("浮层标签行底部应有分隔线")
				}
			}
			if !tc.overlay {
				handle, ok := tt.Find(sidePaneHandleLabel)
				if !ok || handle.Y != chat.Y+tc.inset {
					t.Fatalf("分栏手柄顶部 = %+v，存在=%v，期望 %v", handle, ok, chat.Y+tc.inset)
				}
			}
			if err := tt.Click("改动"); err != nil || inspectorSession(s).Active != sidePaneDiffID {
				t.Fatalf("标签点击失败: %v", err)
			}
			if err := tt.Click("关闭右栏"); err != nil || inspectorSession(s).Open {
				t.Fatalf("关闭按钮点击失败: %v", err)
			}
		})
	}
}

// sidePaneHandlePoint 用无障碍标签找到分栏手柄，返回它的中心点。
func sidePaneHandlePoint(t *testing.T, tt *ui.Tester) (float32, float32) {
	t.Helper()
	r, ok := tt.Find(sidePaneHandleLabel)
	if !ok {
		t.Fatalf("找不到分栏手柄, texts=%v", tt.Texts())
	}
	return r.X + r.W/2, r.Y + r.H/2
}

// 高亮线的目标值只由「是否拖动」「悬停时长」决定：不足 motionHoverDelay 保持 0。
func TestSidePaneHandleInkTarget_Schedule(t *testing.T) {
	cases := []struct {
		name     string
		hovered  bool
		dragging bool
		elapsed  time.Duration
		want     float32
	}{
		{"未悬停", false, false, 0, 0},
		{"悬停不足一半时间", true, false, motionHoverDelay / 2, 0},
		{"悬停 149ms", true, false, motionHoverDelay - time.Millisecond, 0},
		{"悬停满 150ms", true, false, motionHoverDelay, sidePaneHandleInkAlpha},
		{"停更久", true, false, 10 * motionHoverDelay, sidePaneHandleInkAlpha},
		{"拖动中（指针已经离开手柄）", false, true, 0, sidePaneHandleInkAlpha},
	}
	for _, tc := range cases {
		if got := sidePaneHandleInkTarget(tc.hovered, tc.dragging, tc.elapsed); got != tc.want {
			t.Errorf("%s: sidePaneHandleInkTarget(%v, %v, %v) = %v, want %v",
				tc.name, tc.hovered, tc.dragging, tc.elapsed, got, tc.want)
		}
	}
}

// 拖动位移的符号：手柄往左拖（dx 为负）面板变宽，宽度不会变负。
func TestSidePaneDraggedWidth_Signs(t *testing.T) {
	if got := sidePaneDraggedWidth(420, -40); got != 460 {
		t.Errorf("手柄左移 40 应加宽到 460, got %v", got)
	}
	if got := sidePaneDraggedWidth(420, 40); got != 380 {
		t.Errorf("手柄右移 40 应收窄到 380, got %v", got)
	}
	if got := sidePaneDraggedWidth(30, 100); got != 0 {
		t.Errorf("宽度不应变负, got %v", got)
	}
}

// 松手判定：窄于最小宽度的一半收起并复位，否则夹取到 [最小, 最大]；正好一半不收起。
func TestSidePaneReleasedWidth_CollapseAndClamp(t *testing.T) {
	const (
		minW = FileManagerPanelMinWidth
		defW = FileManagerPanelDefaultWidth
		maxW = FileManagerPanelMaxWidth
	)
	if w, collapsed := sidePaneReleasedWidth(minW*0.5-1, minW, defW, maxW); !collapsed || w != defW {
		t.Errorf("过窄松手应收起并复位到 %v, got %v collapsed=%v", defW, w, collapsed)
	}
	if w, collapsed := sidePaneReleasedWidth(minW*0.5, minW, defW, maxW); collapsed || w != minW {
		t.Errorf("正好一半不应收起，夹到最小宽度 %v, got %v collapsed=%v", minW, w, collapsed)
	}
	if w, collapsed := sidePaneReleasedWidth(minW+10, minW, defW, maxW); collapsed || w != minW+10 {
		t.Errorf("区间内宽度应保持 %v, got %v collapsed=%v", minW+10, w, collapsed)
	}
	if w, collapsed := sidePaneReleasedWidth(maxW+100, minW, defW, maxW); collapsed || w != maxW {
		t.Errorf("超上限应夹到 %v, got %v collapsed=%v", maxW, w, collapsed)
	}
}

// 悬停不足 motionHoverDelay 不出高亮线，满 150ms 后出现，移开后淡出消失。
func TestSidePaneSplit_HandleHighlightsAfterDelay(t *testing.T) {
	resetSidePaneHandlesForTest()
	s := sidePaneTestState(t)
	defer s.close()
	stubSidePanePanelRender(t, sidePaneFileID)
	openFilePaneForTest(t, s)

	tt := newSidePaneSplitTester(t, s)
	hx, hy := sidePaneHandlePoint(t, tt)
	if _, ok := tt.Find(sidePaneHandleHighlightLabel); ok {
		t.Fatal("未悬停时不应有高亮线")
	}

	tt.Move(hx, hy)
	if _, ok := tt.Find(sidePaneHandleHighlightLabel); ok {
		t.Fatal("刚移入手柄时不应有高亮线")
	}
	tt.Advance(motionHoverDelay / 2)
	if _, ok := tt.Find(sidePaneHandleHighlightLabel); ok {
		t.Fatalf("悬停 %v 仍不足 150ms，不应有高亮线", motionHoverDelay/2)
	}
	tt.Advance(motionHoverDelay)
	tt.Advance(motionFast)
	if _, ok := tt.Find(sidePaneHandleHighlightLabel); !ok {
		t.Fatalf("悬停满 150ms 后应出现高亮线, texts=%v", tt.Texts())
	}

	tt.Move(hx-200, hy)
	tt.Advance(2 * motionFast)
	tt.Frame()
	if _, ok := tt.Find(sidePaneHandleHighlightLabel); ok {
		t.Fatal("移开后高亮线应淡出消失")
	}
}

// 右栏让出 Windows 标题栏时顶部有横向分割线；顶到窗口顶部（macOS）时没有。
func TestSidePaneSplit_TopDividerBelowTitleBar(t *testing.T) {
	resetSidePaneHandlesForTest()
	s := sidePaneTestState(t)
	defer s.close()
	stubSidePanePanelRender(t, sidePaneFileID)
	openFilePaneForTest(t, s)

	tt := newSidePaneSplitTester(t, s)
	withTitleBarGOOS(t, "windows")
	tt.SetTitleBar(ui.TitleBar{Height: 53, Right: 138})
	if _, ok := tt.Find(sidePaneTopDividerLabel); !ok {
		t.Fatal("Windows 右栏让出标题栏时应有顶部分割线")
	}

	withTitleBarGOOS(t, "darwin")
	tt.SetTitleBar(ui.TitleBar{Height: 32, Left: 78})
	if _, ok := tt.Find(sidePaneTopDividerLabel); ok {
		t.Fatal("macOS 右栏顶到窗口顶部时不应有顶部分割线")
	}
}

// 拖到最小宽度一半以下松手：面板收起，宽度复位到默认值。
func TestSidePaneSplit_AutoCollapsesWhenTooNarrow(t *testing.T) {
	resetSidePaneHandlesForTest()
	s := sidePaneTestState(t)
	defer s.close()
	stubSidePanePanelRender(t, sidePaneFileID)
	start := openFilePaneForTest(t, s)

	tt := newSidePaneSplitTester(t, s)
	hx, hy := sidePaneHandlePoint(t, tt)

	tt.Press(hx, hy)
	// 分三次往右拖，把 420 DIP 的面板压到 80 DIP（< 220/2）。
	tt.Move(hx+150, hy)
	tt.Move(hx+300, hy)
	tt.Move(hx+340, hy)
	file, _, _ := paneOpenFlags(s)
	if !file {
		t.Fatal("拖动过程中面板不应提前收起")
	}
	tt.Release(hx+340, hy)

	file, _, _ = paneOpenFlags(s)
	if file {
		t.Fatal("压到最小宽度一半以下松手后面板应收起")
	}
	width := inspectorSession(s).Width
	if width != start {
		t.Fatalf("收起后宽度应复位到默认值 %v, got %v", start, width)
	}
}

// 拖动 40 DIP 后面板变宽 40，且松手时宽度被写进 ui-state.json。
func TestSidePaneSplit_DragPersistsWidth(t *testing.T) {
	dataDir := settingsTestDir(t)
	t.Setenv("MCODE_DATA", dataDir)
	t.Cleanup(waitUIStateWrites)

	resetSidePaneHandlesForTest()
	s := sidePaneTestState(t)
	defer s.close()
	WithUIStatePersistence()(s)
	stubSidePanePanelRender(t, sidePaneFileID)
	start := openFilePaneForTest(t, s)

	tt := newSidePaneSplitTester(t, s)
	hx, hy := sidePaneHandlePoint(t, tt)

	tt.Press(hx, hy)
	tt.Move(hx-40, hy) // 手柄左移 40：面板加宽 40
	tt.Release(hx-40, hy)

	width := inspectorSession(s).Width
	if width != start+40 {
		t.Fatalf("拖动 40 DIP 后面板宽度应为 %v, got %v", start+40, width)
	}

	waitUIStateWrites()
	file, ok := loadUIStateFile()
	if !ok {
		t.Fatal("ui-state.json 应已写入")
	}
	if got := file.InspectorSessions[s.selectedThreadID].Width; got != start+40 {
		t.Fatalf("落盘的面板宽度 = %v, want %v", got, start+40)
	}
}
