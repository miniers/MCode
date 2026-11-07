package mcodeui

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
	t.Setenv("HOME", t.TempDir())
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
	t.Setenv("HOME", t.TempDir())
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
	t.Setenv("HOME", t.TempDir())
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
