package mcodeui

import (
	"os"
	"path/filepath"
	"testing"

	"mcode/internal/config"
)

func TestSettingsWindowCloseSavesPendingDrafts(t *testing.T) {
	dir := settingsTestDir(t)
	st := NewSettingsState(dir, nil, nil)
	st.cfg.SourceControl.CommitInstructions = "关闭前保存"
	st.noteTextEdit()
	draft := shortcutDraftFor("toggle-diff")
	*draft = "Alt+Cmd+D"
	st.noteShortcutDraftEdit("toggle-diff")
	t.Cleanup(func() { setShortcutOverrides(nil) })
	if !st.prepareWindowClose() {
		t.Fatal("保存失败")
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SourceControl.CommitInstructions != "关闭前保存" || cfg.Shortcuts.ShortcutOverrides["toggle-diff"] != "Alt+Cmd+D" {
		t.Fatal("待保存草稿未写入")
	}
}

func TestSettingsWindowCloseKeepsTextOnWriteError(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	badDir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(badDir, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	st.dataDir = badDir
	st.cfg.SourceControl.CommitInstructions = "草稿"
	st.noteTextEdit()
	if st.prepareWindowClose() || !st.textSavePending || st.cfg.SourceControl.CommitInstructions != "草稿" || st.errorMessage == "" {
		t.Fatal("写盘失败时应保留草稿与错误")
	}
	st.dataDir = settingsTestDir(t)
	if !st.prepareWindowClose() {
		t.Fatal("修复写盘位置后应允许关闭")
	}
}

func TestSettingsWindowCloseKeepsInvalidShortcut(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	draft := shortcutDraftFor("toggle-diff")
	*draft = "Alt+"
	st.noteShortcutDraftEdit("toggle-diff")
	if st.prepareWindowClose() || len(st.shortcutDirtyIDs) == 0 || st.errorMessage == "" {
		t.Fatal("无效草稿应阻止关闭并保留错误")
	}
	*draft = "Alt+Cmd+D"
	if !st.prepareWindowClose() {
		t.Fatal("修正后应允许关闭")
	}
	t.Cleanup(func() { setShortcutOverrides(nil) })
}
