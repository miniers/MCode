package mcodeui

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// stubThemeFilePicker 替换文件选择器并在测试结束时恢复。
func stubThemeFilePicker(t *testing.T, path string, ok bool, err error) {
	t.Helper()
	prev := pickThemeFileDialog
	pickThemeFileDialog = func() (string, bool, error) { return path, ok, err }
	t.Cleanup(func() { pickThemeFileDialog = prev })
}

// waitFileImport 反复推进渲染帧直到导入结果落定（后台 goroutine 完成后才断言）。
func waitFileImport(t *testing.T, tt interface {
	Frame()
	HasText(string) bool
}, st *SettingsState, before int) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		page := getThemesSheetState(st)
		if len(st.cfg.Appearance.Themes) != before || (page.message != "" && time.Now().Before(page.messageUntil)) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestThemesImportFromFile(t *testing.T) {
	dir := t.TempDir()
	piPath := filepath.Join(dir, "pi.json")
	if err := os.WriteFile(piPath, []byte(piThemeClipboard), 0o600); err != nil {
		t.Fatal(err)
	}
	mcodePath := filepath.Join(dir, "mcode.json")
	mcodeJSON := `{"name":"from file","base":"light","colors":{"accent":"#123456"}}`
	if err := os.WriteFile(mcodePath, []byte(mcodeJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("pi format", func(t *testing.T) {
		st, tt := newThemesTester(t)
		stubThemeFilePicker(t, piPath, true, nil)
		page := getThemesSheetState(st)
		before := len(st.cfg.Appearance.Themes)
		importSheetThemeFromFile(st, page)
		if !waitFileImport(t, tt, st, before) {
			t.Fatal("从文件导入 pi 主题超时")
		}
		if len(st.cfg.Appearance.Themes) != before {
			t.Fatalf("导入后主题数 = %d，期望保持 %d", len(st.cfg.Appearance.Themes), before)
		}
		if !tt.HasText("无法导入") {
			t.Fatal("从文件导入 pi 主题应提示无法导入")
		}
	})

	t.Run("mcode format", func(t *testing.T) {
		st, tt := newThemesTester(t)
		stubThemeFilePicker(t, mcodePath, true, nil)
		page := getThemesSheetState(st)
		before := len(st.cfg.Appearance.Themes)
		importSheetThemeFromFile(st, page)
		if !waitFileImport(t, tt, st, before) {
			t.Fatal("从文件导入 mcode 主题超时")
		}
		theme, ok := st.cfg.Appearance.ThemeByID(page.editingID)
		if !ok || theme.Name != "from file" || theme.Colors["accent"] != "#123456" {
			t.Fatalf("导入 mcode 主题后未选中该主题：%+v", theme)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		st, tt := newThemesTester(t)
		stubThemeFilePicker(t, "", false, nil)
		page := getThemesSheetState(st)
		before := len(st.cfg.Appearance.Themes)
		importSheetThemeFromFile(st, page)
		time.Sleep(200 * time.Millisecond)
		tt.Frame()
		if len(st.cfg.Appearance.Themes) != before {
			t.Fatal("取消选择不应新增主题")
		}
		if page.message != "" || tt.HasText("无法导入") {
			t.Fatalf("取消选择不应提示失败：%q", page.message)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		st, tt := newThemesTester(t)
		bigPath := filepath.Join(dir, "big.json")
		big := make([]byte, themeFileSizeLimit+1)
		if err := os.WriteFile(bigPath, big, 0o600); err != nil {
			t.Fatal(err)
		}
		stubThemeFilePicker(t, bigPath, true, nil)
		page := getThemesSheetState(st)
		before := len(st.cfg.Appearance.Themes)
		importSheetThemeFromFile(st, page)
		if !waitFileImport(t, tt, st, before) {
			t.Fatal("超大文件未给出失败提示")
		}
		if len(st.cfg.Appearance.Themes) != before {
			t.Fatal("超过 1 MiB 的文件不应被导入")
		}
		if !tt.HasText("无法导入") {
			t.Fatal("超大文件应提示无法导入")
		}
	})
}
