package mcodeui

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"mcode/internal/testenv"
)

// TestUIStateRoundTrip 验证 ui-state.json 的保存与读取往返。
func TestUIStateRoundTrip(t *testing.T) {
	dataDir := settingsTestDir(t)
	t.Setenv("MCODE_DATA", dataDir)
	t.Cleanup(waitUIStateWrites)

	want := UIState{SidebarWidth: 350, SidebarHidden: true}
	if err := saveUIState(want); err != nil {
		t.Fatalf("saveUIState failed: %v", err)
	}
	path := filepath.Join(dataDir, uiStateFileName)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s written: %v", path, err)
	}
	got := loadUIState()
	if got.SidebarWidth != want.SidebarWidth || got.SidebarHidden != want.SidebarHidden {
		t.Fatalf("loadUIState = %+v, want %+v", got, want)
	}

	// 覆盖写入：越界宽度写入前先夹取
	if err := saveUIState(UIState{SidebarWidth: 500, SidebarHidden: false}); err != nil {
		t.Fatalf("saveUIState failed: %v", err)
	}
	got = loadUIState()
	if got.SidebarWidth != SidebarMaxWidth || got.SidebarHidden {
		t.Fatalf("越界宽度应被夹取到 %v，got %+v", float32(SidebarMaxWidth), got)
	}
}

// TestLoadUIStateDefaults 验证文件缺失或损坏时回落到默认状态。
func TestLoadUIStateDefaults(t *testing.T) {
	dataDir := settingsTestDir(t)
	t.Setenv("MCODE_DATA", dataDir)
	t.Cleanup(waitUIStateWrites)

	got := loadUIState()
	if got.SidebarWidth != SidebarDefaultWidth || got.SidebarHidden {
		t.Fatalf("缺少文件时应回落到默认状态，got %+v", got)
	}

	if err := os.WriteFile(filepath.Join(dataDir, uiStateFileName), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	got = loadUIState()
	if got.SidebarWidth != SidebarDefaultWidth || got.SidebarHidden {
		t.Fatalf("文件损坏时应回落到默认状态，got %+v", got)
	}
}

// TestClampSidebarWidth 验证宽度夹取到 [SidebarMinWidth, SidebarMaxWidth]，非正值回落到默认宽度。
func TestClampSidebarWidth(t *testing.T) {
	tests := []struct {
		in   float32
		want float32
	}{
		{0, SidebarDefaultWidth},
		{-10, SidebarDefaultWidth},
		{100, SidebarMinWidth},
		{SidebarMinWidth, SidebarMinWidth},
		{300, 300},
		{SidebarMaxWidth, SidebarMaxWidth},
		{999, SidebarMaxWidth},
	}
	for _, tc := range tests {
		if got := clampSidebarWidth(tc.in); got != tc.want {
			t.Errorf("clampSidebarWidth(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestMainAreaWidth 验证侧边栏显示/隐藏时主工作区的可用宽度。
func TestMainAreaWidth(t *testing.T) {
	// 隐藏：主区占满窗口
	if got := mainAreaWidth(1320, 300, true); got != 1320 {
		t.Errorf("隐藏时 mainAreaWidth = %v, want 1320", got)
	}
	// 显示：减去侧边栏与 1pt 分隔线
	if got := mainAreaWidth(1320, 300, false); got != 1019 {
		t.Errorf("显示时 mainAreaWidth = %v, want 1019", got)
	}
	// 窗口过窄时不为负
	if got := mainAreaWidth(200, 420, false); got != 0 {
		t.Errorf("窄窗口 mainAreaWidth = %v, want 0", got)
	}
}

// TestApplyUIState 验证启动时恢复侧边栏宽度与显隐状态。
func TestApplyUIState(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	WithUIStatePersistence()(s)
	applyUIState(s, UIState{SidebarWidth: 320, SidebarHidden: true})
	if s.sidebarWidth != 320 || !s.sidebarCollapsed {
		t.Fatalf("applyUIState 未生效: width=%v hidden=%v", s.sidebarWidth, s.sidebarCollapsed)
	}
	// 越界宽度夹取后应用
	applyUIState(s, UIState{SidebarWidth: 900})
	if s.sidebarWidth != SidebarMaxWidth || s.sidebarCollapsed {
		t.Fatalf("applyUIState 应夹取宽度并恢复显示: width=%v hidden=%v", s.sidebarWidth, s.sidebarCollapsed)
	}
}

// TestToggleSidebarPersisted 验证切换侧边栏后持久化显隐状态。
func TestToggleSidebarPersisted(t *testing.T) {
	dataDir := settingsTestDir(t)
	t.Setenv("MCODE_DATA", dataDir)
	t.Cleanup(waitUIStateWrites)
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	WithUIStatePersistence()(s)

	toggleSidebarPersisted(s)
	if !s.sidebarCollapsed {
		t.Fatalf("切换后 sidebarCollapsed 应为 true")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if loadUIState().SidebarHidden {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	st := loadUIState()
	if !st.SidebarHidden {
		t.Fatalf("切换后应把 sidebar_hidden 写入 ui-state.json")
	}
	if st.SidebarWidth != SidebarDefaultWidth {
		t.Fatalf("切换后宽度应保持默认值 %v, got %v", float32(SidebarDefaultWidth), st.SidebarWidth)
	}
}

// TestPersistUIStateWritesWidth 验证拖拽宽度后持久化（Split 的 OnChange 走同一路径）。
func TestPersistUIStateWritesWidth(t *testing.T) {
	dataDir := settingsTestDir(t)
	t.Setenv("MCODE_DATA", dataDir)
	t.Cleanup(waitUIStateWrites)
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	WithUIStatePersistence()(s)

	s.mu.Lock()
	s.sidebarWidth = 320
	s.mu.Unlock()
	persistUIState(s)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if loadUIState().SidebarWidth == 320 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("拖拽后应把 sidebar_width=320 写入 ui-state.json, got %+v", loadUIState())
}

// TestReleaseChatUIState 验证窗口关闭后清理界面状态注册表。
func TestReleaseChatUIState(t *testing.T) {
	s := newChatState(NewFakeClient())
	_ = getChatUIState(s)
	releaseChatUIState(s)
	chatUIMu.Lock()
	_, ok := chatUIStates[s]
	chatUIMu.Unlock()
	if ok {
		t.Fatalf("关闭后应清理界面状态")
	}
}

func TestReviewUIStateChangesDuringWrite(t *testing.T) {
	s, _, dp, key, _ := reviewCommentsSetup(t)
	t.Setenv("MCODE_DATA", t.TempDir())
	WithUIStatePersistence()(s)
	t.Cleanup(func() {
		waitUIStateWrites()
		uiStateBeforeWriteHook = nil
	})
	var once sync.Once
	readKey := reviewReadKey(key, ReviewFile{NewHash: "changed"})
	uiStateBeforeWriteHook = func() {
		once.Do(func() {
			toggleReviewRead(dp, readKey)
			selectReviewFile(s, dp, "t1", "new.go")
			persistInspectorPreference("t2", pendingInspectorPreference{Preference: InspectorPreference{Width: 500}})
		})
	}
	selectReviewFile(s, dp, "t1", "old.go")
	waitUIStateWrites()
	file, ok := loadUIStateFile()
	if !ok || file.ReviewSessions["t1"].SelectedPath != "new.go" || !reflect.DeepEqual(file.ReviewSessions["t1"].ReadKeys, []string{readKey}) || file.InspectorSessions["t2"].Width != 500 {
		t.Fatalf("并发写者丢失新偏好或其他字段: %+v", file)
	}
}

func TestReviewUIStateNormalizesReadKeys(t *testing.T) {
	pref := reviewPreference{SelectedPath: "../outside", ReadKeys: []string{"invalid"}}
	key := reviewDiffKey{workspace: "/workspace", scope: "turn", path: "a.go"}
	for i := 0; i <= reviewReadMarkerLimit; i++ {
		pref.ReadKeys = append(pref.ReadKeys, reviewReadKey(key, ReviewFile{NewHash: string(rune(i + 1))}))
	}
	last := pref.ReadKeys[len(pref.ReadKeys)-1]
	pref.ReadKeys = append(pref.ReadKeys, last)
	normalized := normalizeReviewPreference(pref)
	if normalized.SelectedPath != "" || len(normalized.ReadKeys) != reviewReadMarkerLimit || normalized.ReadKeys[0] != pref.ReadKeys[2] || normalized.ReadKeys[len(normalized.ReadKeys)-1] != last {
		t.Fatal("没有清洗路径、去重或淘汰最旧已读键")
	}
}

func TestInspectorPreferencesRoundTrip(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	want := map[string]InspectorPreference{
		"t1": {Open: true, Active: sidePaneDiffID, Width: 620},
		"t2": {Active: sidePanePreviewID, Width: 400},
	}
	if err := saveUIState(UIState{InspectorSessions: want}); err != nil {
		t.Fatal(err)
	}
	if got := loadUIState().InspectorSessions; !reflect.DeepEqual(got, want) {
		t.Fatalf("偏好往返失败: got %+v, want %+v", got, want)
	}
	if err := saveUIState(UIState{InspectorSessions: map[string]InspectorPreference{}}); err != nil {
		t.Fatal(err)
	}
	if got := loadUIState().InspectorSessions; !reflect.DeepEqual(got, want) {
		t.Fatal("空 map 删除了已有偏好")
	}
}

func TestInspectorLegacyWidthsMigration(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	for _, tc := range []struct {
		name   string
		widths map[string]float32
		want   float32
	}{
		{"preview", map[string]float32{"preview": 610, "file": 550, "diff": 390}, 610},
		{"file", map[string]float32{"file": 550, "diff": 390}, 550},
		{"diff", map[string]float32{"diff": 390}, 390},
		{"default", nil, inspectorDefaultWidth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MCODE_DATA", t.TempDir())
			t.Cleanup(waitUIStateWrites)
			if err := saveUIState(UIState{SidePaneWidths: tc.widths}); err != nil {
				t.Fatal(err)
			}
			s := newChatState(NewFakeClient())
			defer s.close()
			WithUIStatePersistence()(s)
			applyUIState(s, loadUIState())
			s.selectedThreadID = tc.name
			st := inspectorSession(s)
			if st.Open || st.Active != sidePaneFileID || st.Width != tc.want {
				t.Fatalf("旧宽度迁移错误: %+v", st)
			}
			inspectorRemember(s)
			waitUIStateWrites()
			if got := loadUIState().SidePaneWidths; !reflect.DeepEqual(got, tc.widths) {
				t.Fatalf("旧宽度被改写: %+v", got)
			}
		})
	}
}

func TestInspectorPreferenceMergePreservesOtherFields(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	initial := uiStateFile{
		UIState: UIState{SidebarWidth: 330, TerminalDockHeight: 280, InspectorSessions: map[string]InspectorPreference{
			"t2": {Open: true, Active: sidePaneDiffID, Width: 500},
		}},
		ProjectOrder: []string{"p2", "p1"},
		LastModel:    ModelSelection{Provider: "test", Model: "model"},
		LastSwarm:    &LastSwarmSelection{Enabled: new(true)},
	}
	if err := saveUIStateFile(initial); err != nil {
		t.Fatal(err)
	}
	s := newChatState(NewFakeClient())
	defer s.close()
	WithUIStatePersistence()(s)
	applyUIState(s, loadUIState())
	s.selectedThreadID = "t1"
	inspectorSelect(s, sidePaneFileID, false)
	waitUIStateWrites()
	got, _ := loadUIStateFile()
	if len(got.InspectorSessions) != 2 || !reflect.DeepEqual(got.InspectorSessions["t2"], initial.InspectorSessions["t2"]) ||
		!reflect.DeepEqual(got.ProjectOrder, initial.ProjectOrder) || got.LastModel != initial.LastModel ||
		!reflect.DeepEqual(got.LastSwarm, initial.LastSwarm) || got.TerminalDockHeight != 280 {
		t.Fatalf("会话写者覆盖了其他字段: %+v", got)
	}
	persistUIState(s)
	waitUIStateWrites()
	if got := loadUIState().InspectorSessions; len(got) != 2 {
		t.Fatal("侧栏写者丢失会话偏好")
	}
	if err := saveUIState(UIState{InspectorSessions: map[string]InspectorPreference{"t3": {Width: 600}}}); err != nil {
		t.Fatal(err)
	}
	if got := loadUIState().InspectorSessions; len(got) != 3 {
		t.Fatal("按值保存丢失其他会话偏好")
	}
}

func TestInspectorPreferenceInvalidValues(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	for _, tc := range []struct{ width, want float32 }{
		{float32(math.NaN()), 480}, {float32(math.Inf(1)), 480}, {float32(math.Inf(-1)), 480},
		{-1, 480}, {0, 480}, {100, 320}, {900, 800}, {620, 620},
	} {
		s := newChatState(NewFakeClient())
		applyUIState(s, UIState{InspectorSessions: map[string]InspectorPreference{
			"t1": {Open: true, Active: "unknown", Width: tc.width}, "": {Open: true, Width: 500},
		}})
		s.selectedThreadID = "t1"
		st := inspectorSession(s)
		if !st.Open || st.Active != sidePaneFileID || st.Width != tc.want {
			t.Fatalf("非法偏好未修正: %+v", st)
		}
		s.close()
		if err := saveUIState(UIState{InspectorSessions: map[string]InspectorPreference{"t1": {Width: tc.width}, "": {Width: 500}}}); err != nil {
			t.Fatal(err)
		}
		got := loadUIState().InspectorSessions
		if got["t1"].Width != tc.want || got["t1"].Active != sidePaneFileID {
			t.Fatalf("写盘未修正偏好: %+v", got)
		}
		if _, ok := got[""]; ok {
			t.Fatal("空会话键落盘")
		}
	}
}

func TestInspectorPreferenceChangesDuringWrite(t *testing.T) {
	for _, deleting := range []bool{false, true} {
		name := "update"
		if deleting {
			name = "delete"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("MCODE_DATA", t.TempDir())
			t.Cleanup(func() {
				waitUIStateWrites()
				uiStateBeforeWriteHook = nil
			})
			late := InspectorPreference{Open: true, Active: sidePaneDiffID, Width: 700}
			var once sync.Once
			uiStateBeforeWriteHook = func() {
				once.Do(func() {
					persistInspectorPreference("t1", pendingInspectorPreference{Preference: late, Delete: deleting})
					persistInspectorPreference("t2", pendingInspectorPreference{Preference: late})
				})
			}
			persistInspectorPreference("t1", pendingInspectorPreference{Preference: InspectorPreference{Width: 500}})
			waitUIStateWrites()
			got := loadUIState().InspectorSessions
			if deleting {
				if _, ok := got["t1"]; ok {
					t.Fatal("旧写者快照复活了删除的偏好")
				}
			} else if !reflect.DeepEqual(got["t1"], late) {
				t.Fatalf("写盘期间的新偏好被旧快照覆盖: %+v", got)
			}
			if !reflect.DeepEqual(got["t2"], late) {
				t.Fatalf("写盘期间其他会话偏好丢失: %+v", got)
			}
		})
	}
}

// 文件树的展开、活动文件与滚动锚点按会话落盘并在新窗口中恢复；
// 恢复时只展开仍然存在的目录。
func TestFileTreeRestoresSessionPosition(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)

	s := newChatState(NewFakeClient())
	defer s.close()
	WithUIStatePersistence()(s)
	s.selectedThreadID = "t1"
	st := inspectorSession(s)
	func() {
		fileManagerMu.Lock()
		defer fileManagerMu.Unlock()
		tree := ensureFileTreeLocked(st.Files)
		tree.outline.Open.Add("sub")
		tree.outline.Open.Add("sub/deep")
		tree.restoreExpanded["gone"] = true
		tree.selection.Add("a.txt")
		tree.anchor = "a.txt"
		st.Files.active = "a.txt"
		st.Files.showList = false
	}()
	inspectorRemember(s)
	waitUIStateWrites()

	pref := loadUIState().InspectorSessions["t1"]
	if !reflect.DeepEqual(pref.FileExpanded, []string{"gone", "sub", "sub/deep"}) ||
		pref.FileActive != "a.txt" || pref.FileAnchor != "a.txt" {
		t.Fatalf("树位置未正确落盘: %+v", pref)
	}

	s2 := newChatState(NewFakeClient())
	defer s2.close()
	WithUIStatePersistence()(s2)
	applyUIState(s2, loadUIState())
	s2.selectedThreadID = "t1"
	st2 := inspectorSession(s2)
	var restoredExpanded int
	var restoredActive, restoredAnchor string
	var opened []string
	var openFile, pending string
	var goneOpen, subOpen, deepOpen bool
	func() {
		fileManagerMu.Lock()
		defer fileManagerMu.Unlock()
		tree2 := ensureFileTreeLocked(st2.Files)
		restoredExpanded = len(tree2.restoreExpanded)
		restoredActive, restoredAnchor = tree2.restoreActive, tree2.restoreAnchor
		opened, openFile = fileTreeApplyListingLocked(st2.Files, tree2, "", FileListing{Entries: []FileEntry{
			{Name: "sub", Path: "sub", IsDir: true},
			{Name: "a.txt", Path: "a.txt"},
		}})
		fileTreeApplyListingLocked(st2.Files, tree2, "sub", FileListing{Entries: []FileEntry{
			{Name: "deep", Path: "sub/deep", IsDir: true},
		}})
		goneOpen = tree2.outline.Open.Has("gone")
		subOpen = tree2.outline.Open.Has("sub")
		deepOpen = tree2.outline.Open.Has("sub/deep")
		pending = tree2.pendingOpen
	}()

	if restoredExpanded != 3 || restoredActive != "a.txt" || restoredAnchor != "a.txt" {
		t.Fatalf("恢复偏好未挂到树状态: expanded=%d active=%q anchor=%q", restoredExpanded, restoredActive, restoredAnchor)
	}
	if fmtOpen := opened; len(fmtOpen) != 1 || fmtOpen[0] != "sub" {
		t.Fatalf("根目录应继续加载已展开的 sub，得到 %v", fmtOpen)
	}
	if openFile != "a.txt" || pending != "a.txt" {
		t.Fatalf("活动文件未安排恢复: openFile=%q pending=%q", openFile, pending)
	}
	if goneOpen || !subOpen || !deepOpen {
		t.Fatalf("恢复展开状态不符: gone=%v sub=%v deep=%v", goneOpen, subOpen, deepOpen)
	}
}
