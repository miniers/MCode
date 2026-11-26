package mcodeui

import (
	"os"
	"path/filepath"
	"testing"
)

// piThemeClipboard 是粘贴到“从剪贴板导入”里的 pi 主题片段。
const piThemeClipboard = `{
	"name": "pi dark",
	"appearance": "dark",
	"vars": {
		"text": "okhsl(225 5% 80%)",
		"muted": "okhsl(229 8% 55%)",
		"violet": "okhsl(295 60% 70%)",
		"blueBg": "okhsl(235 19% 18%)"
	},
	"colors": {
		"text": "text",
		"muted": "muted",
		"dim": "okhsl(229 7% 45%)",
		"accent": "violet",
		"error": "okhsl(20 91% 60%)",
		"warning": "okhsl(83 99% 60%)",
		"success": "okhsl(159 75% 55%)",
		"borderMuted": "okhsl(235 7% 30%)",
		"userMessageBg": "blueBg",
		"mdHeading": "okhsl(83 99% 60%)",
		"mdLink": "okhsl(231 68% 65%)",
		"mdCode": "violet",
		"mdCodeBlock": "okhsl(159 40% 20%)",
		"mdQuote": "muted",
		"mdQuoteBorder": "okhsl(235 7% 40%)",
		"mdListBullet": "violet",
		"syntaxKeyword": "okhsl(231 68% 65%)"
	}
}`

func TestThemesImportPiList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.json")
	if err := os.WriteFile(path, []byte(piThemeClipboard), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := piThemeDir
	piThemeDir = func() string { return dir }
	prevBuiltin := piBuiltinThemePaths
	piBuiltinThemePaths = func() []string { return nil }
	t.Cleanup(func() {
		piThemeDir = prev
		piBuiltinThemePaths = prevBuiltin
	})
	files := listPiThemeFiles()
	if len(files) != 1 || files[0].name != "pi dark" {
		t.Fatalf("pi 主题列表 = %+v", files)
	}
	st, tt := newThemesTester(t)
	page := getThemesSheetState(st)
	before := len(st.cfg.Appearance.Themes)
	if !importPiThemeFile(st, page, files[0].path) {
		t.Fatal("导入 pi 主题失败")
	}
	tt.Frame()
	if len(st.cfg.Appearance.Themes) != before+1 {
		t.Fatalf("导入后主题数 = %d，期望 %d", len(st.cfg.Appearance.Themes), before+1)
	}
	if theme, ok := st.cfg.Appearance.ThemeByID(page.editingID); !ok || theme.Name != "pi dark" {
		t.Fatalf("导入后未选中新主题：%+v，ok=%v", theme, ok)
	}
	if err := tt.Click("添加主题"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("导入pi主题"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("pi dark") {
		t.Fatal("列表未显示主题名")
	}
}

func TestThemesImportPiListEmpty(t *testing.T) {
	prev := piThemeDir
	piThemeDir = func() string { return t.TempDir() }
	prevBuiltin := piBuiltinThemePaths
	piBuiltinThemePaths = func() []string { return nil }
	t.Cleanup(func() {
		piThemeDir = prev
		piBuiltinThemePaths = prevBuiltin
	})
	st, tt := newThemesTester(t)
	if err := tt.Click("添加主题"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("导入pi主题"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("没有主题") {
		t.Fatal("空目录应显示空态")
	}
	if err := tt.Click("从文件导入"); err == nil {
		t.Fatal("空列表不应改走文件选择")
	}
	_ = st
}

func TestImportSheetThemeRejectsPiJSON(t *testing.T) {
	st, tt := newThemesTester(t)
	page := getThemesSheetState(st)
	before := len(st.cfg.Appearance.Themes)
	if importSheetTheme(st, page, piThemeClipboard) {
		t.Fatal("从剪贴板导入 pi 主题不应成功")
	}
	tt.Frame()
	if len(st.cfg.Appearance.Themes) != before {
		t.Fatalf("导入后主题数 = %d，期望保持 %d", len(st.cfg.Appearance.Themes), before)
	}
	count := len(st.cfg.Appearance.Themes)
	if !importSheetTheme(st, page, `{"name":"plain","base":"light","colors":{"accent":"#123456"}}`) {
		t.Fatal("mcode 格式导入失败")
	}
	if len(st.cfg.Appearance.Themes) != count+1 {
		t.Fatal("mcode 格式导入未生效")
	}
}
