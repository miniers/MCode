package mcodeui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func newAccessTestClient(t *testing.T) *FakeClient {
	t.Helper()
	fake := newGitFakeClient()
	workDir := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake.SetThreadWorkDir(workDir)
	fake.SetFileTree(fileManagerTestTree())
	fake.SetFileContents(map[string]FileContent{
		"a.txt": {Path: "a.txt", Kind: "text", Text: "hello", TotalSize: 5},
	})
	fake.SetMessages("t1", []Message{
		{ID: "m1", Role: "user", Text: "你好，请检查这个项目", TurnIndex: 1, IsFinal: true},
		{ID: "m2", Role: "assistant", Text: "好的，我已经查看了相关文件。", TurnIndex: 1, IsFinal: true},
	})
	fake.SetThreadDiff("t1", "", ThreadDiff{Diff: toolDiffTestUnified})
	return fake
}

func settleAccessFrames(tt *ui.Tester) {
	for i := 0; i < 6; i++ {
		tt.Advance(100 * time.Millisecond)
		tt.Frame()
	}
}

func assertButtonsHaveNames(t *testing.T, tt *ui.Tester) {
	t.Helper()
	for i, a := range tt.AccessAll() {
		if a.Role == ui.RoleButton && a.Name == "" {
			t.Errorf("按钮[%d] 无障碍名称为空: Bounds=%v Value=%q Description=%q RoleDescription=%q KeyShortcuts=%q HasPopup=%v Disabled=%v Offscreen=%v",
				i, a.Bounds, a.Value, a.Description, a.RoleDescription, a.KeyShortcuts, a.HasPopup, a.Disabled, a.Offscreen)
		}
	}
}

func TestMainWindowButtonsHaveNames(t *testing.T) {
	testenv.TempHome(t)
	setAppReduceMotion(t, true)
	stubTerminals(t)

	fake := newAccessTestClient(t)
	tt, state := newWorkspaceTester(t, fake, "chat")
	t.Cleanup(state.close)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1") && tt.HasText("好的，我已经查看了相关文件。")
	})
	settleAccessFrames(tt)

	t.Run("main_window", func(t *testing.T) {
		assertButtonsHaveNames(t, tt)
	})

	t.Run("file_manager", func(t *testing.T) {
		inspectorSelect(state, sidePaneFileID, false)
		waitFor(t, tt, func() bool { return inspectorPaneOpen(state, sidePaneFileID) && tt.HasText("a.txt") })
		settleAccessFrames(tt)
		assertButtonsHaveNames(t, tt)
		inspectorClose(state)
		settleAccessFrames(tt)
	})

	t.Run("git_pane", func(t *testing.T) {
		inspectorSelect(state, sidePaneGitID, false)
		waitFor(t, tt, func() bool { return inspectorPaneOpen(state, sidePaneGitID) })
		settleAccessFrames(tt)
		assertButtonsHaveNames(t, tt)
		inspectorClose(state)
		settleAccessFrames(tt)
	})

	t.Run("git_turn_scope", func(t *testing.T) {
		state.toggleDiffPanel()
		waitFor(t, tt, func() bool { return inspectorPaneOpen(state, sidePaneGitID) })
		settleAccessFrames(tt)
		assertButtonsHaveNames(t, tt)
		inspectorClose(state)
		settleAccessFrames(tt)
	})

	t.Run("preview_panel", func(t *testing.T) {
		inspectorSelect(state, sidePanePreviewID, false)
		waitFor(t, tt, func() bool { return inspectorPaneOpen(state, sidePanePreviewID) })
		settleAccessFrames(tt)
		assertButtonsHaveNames(t, tt)
		inspectorClose(state)
		settleAccessFrames(tt)
	})

	t.Run("context_panel", func(t *testing.T) {
		state.headerToggleContextPanel()
		waitFor(t, tt, func() bool { return getContextPanelState(state).IsOpen() })
		settleAccessFrames(tt)
		assertButtonsHaveNames(t, tt)
		state.headerToggleContextPanel()
		waitFor(t, tt, func() bool { return !getContextPanelState(state).IsOpen() })
		settleAccessFrames(tt)
	})

	t.Run("terminal_panel", func(t *testing.T) {
		state.headerToggleTerminal()
		waitFor(t, tt, func() bool { return getTerminalState(state).visible })
		settleAccessFrames(tt)
		assertButtonsHaveNames(t, tt)
		state.headerToggleTerminal()
		waitFor(t, tt, func() bool { return !getTerminalState(state).visible })
		settleAccessFrames(tt)
	})
}
