package mcodeui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

// TestIsMarkdownPath 验证 Markdown 后缀判断。
func TestIsMarkdownPath(t *testing.T) {
	tests := map[string]bool{
		"README.md":       true,
		"docs/a.markdown": true,
		"page.mdx":        true,
		"NOTES.MD":        true,
		"main.go":         false,
		"a.md.txt":        false,
		"plain":           false,
	}
	for path, want := range tests {
		if got := isMarkdownPath(path); got != want {
			t.Errorf("isMarkdownPath(%q) = %v, want %v", path, got, want)
		}
	}
}

// TestShouldOverwriteFile 验证自动保存的覆盖判定（内容与基线一致才允许覆盖）。
func TestShouldOverwriteFile(t *testing.T) {
	tests := []struct {
		name     string
		baseline string
		onDisk   string
		exists   bool
		want     bool
	}{
		{"内容未变可覆盖", "a\n", "a\n", true, true},
		{"外部改动后不覆盖", "a\n", "b\n", true, false},
		{"文件被删除不覆盖", "a\n", "", false, false},
		{"空基线（新文件）已存在时不覆盖", "", "x\n", true, false},
		{"空基线（新文件）且内容为空可覆盖", "", "", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldOverwriteFile(tc.baseline, tc.onDisk, tc.exists); got != tc.want {
				t.Errorf("shouldOverwriteFile(%q, %q, %v) = %v, want %v",
					tc.baseline, tc.onDisk, tc.exists, got, tc.want)
			}
		})
	}
}

// TestMiniEditorLoadSaveAndConflict 验证打开文件、保存、外部改动冲突与重新加载。
func TestMiniEditorLoadSaveAndConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	st := newMiniEditorState(path)
	if st.text != "package main\n" || st.baseline != "package main\n" || st.loadErr != "" {
		t.Fatalf("打开文件后应载入内容: text=%q baseline=%q loadErr=%q", st.text, st.baseline, st.loadErr)
	}
	if got := miniEditorStatusText(st); got != "已打开" {
		t.Fatalf("初始状态文案 = %q, want 已打开", got)
	}

	// 编辑后保存：磁盘内容更新、状态变为已保存
	st.text = "package main\n\nfunc main() {}\n"
	if got := miniEditorStatusText(st); got != "编辑中" {
		t.Fatalf("偏离基线时文案 = %q, want 编辑中", got)
	}
	st.saveIfUnchanged(st.text)
	if st.saveErr != "" {
		t.Fatalf("保存不应失败: %q", st.saveErr)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != st.text {
		t.Fatalf("保存后磁盘内容应更新, data=%q err=%v", data, err)
	}
	if got := miniEditorStatusText(st); got != "已保存" {
		t.Fatalf("保存后文案 = %q, want 已保存", got)
	}

	// 外部改动后不覆盖，保留磁盘内容并提示冲突
	if err := os.WriteFile(path, []byte("// external\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st.text = "// mine\n"
	st.saveIfUnchanged(st.text)
	if st.saveErr == "" {
		t.Fatalf("外部改动后应提示冲突")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "// external\n" {
		t.Fatalf("冲突时不应覆盖磁盘内容, data=%q err=%v", data, err)
	}
	if got := miniEditorStatusText(st); got != "保存失败" {
		t.Fatalf("冲突后文案 = %q, want 保存失败", got)
	}

	// 重新加载后恢复一致，可继续保存
	st.reload()
	if st.text != "// external\n" || st.saveErr != "" {
		t.Fatalf("重新加载后应恢复磁盘内容并清除错误: text=%q saveErr=%q", st.text, st.saveErr)
	}
	st.text = "// edited\n"
	st.saveIfUnchanged(st.text)
	if data, err := os.ReadFile(path); err != nil || string(data) != "// edited\n" {
		t.Fatalf("重新加载后应可保存, data=%q err=%v", data, err)
	}
}

// TestMiniEditorNonUTF8ReadOnly 验证非 UTF-8 文件只读打开且永不写回。
func TestMiniEditorNonUTF8ReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "binary.go")
	raw := []byte{0xff, 0xfe, 0x00, 0x01}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	st := newMiniEditorState(path)
	if st.loadErr == "" {
		t.Fatalf("非 UTF-8 文件应只读打开")
	}
	if got := miniEditorStatusText(st); got != "只读" {
		t.Fatalf("只读文案 = %q, want 只读", got)
	}

	st.text = "被改坏的内容"
	st.saveIfUnchanged(st.text)
	if st.saveErr != "" {
		t.Fatalf("只读状态不应尝试写盘: %q", st.saveErr)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != string(raw) {
		t.Fatalf("只读时不应改动原文件, data=%v err=%v", data, err)
	}
}

// TestMiniEditorSaveCreatesNewFile 验证文件不存在时首次保存创建文件与上级目录。
func TestMiniEditorSaveCreatesNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "doc.md")
	st := newMiniEditorState(path)
	if st.loadErr != "" || st.text != "" {
		t.Fatalf("新文件应作为空文档打开: loadErr=%q text=%q", st.loadErr, st.text)
	}

	st.text = "# 标题\n"
	st.saveIfUnchanged(st.text)
	if st.saveErr != "" {
		t.Fatalf("新建保存不应失败: %q", st.saveErr)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "# 标题\n" {
		t.Fatalf("应创建文件与目录, data=%q err=%v", data, err)
	}
}

// TestMiniEditorDebouncedAutoSave 验证停止输入后的防抖自动保存。
func TestMiniEditorDebouncedAutoSave(t *testing.T) {
	old := miniEditorSaveDelay
	miniEditorSaveDelay = 20 * time.Millisecond
	t.Cleanup(func() { miniEditorSaveDelay = old })

	path := filepath.Join(t.TempDir(), "a.go")
	if err := os.WriteFile(path, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := newMiniEditorState(path)
	enabled := true
	st.workbench.EditorAutoSave = &enabled

	st.text = "b\n"
	st.scheduleSave()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && string(data) == "b\n" {
			st.mu.Lock()
			baseline := st.baseline
			st.mu.Unlock()
			if baseline != "b\n" {
				t.Fatalf("保存后基线应推进, got %q", baseline)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("防抖保存未在超时前落盘")
}

// TestMiniEditorEditorFontSize 验证字号读取配置、0 值回退默认并限制在 10 到 18 之间。
func TestMiniEditorEditorFontSize(t *testing.T) {
	t.Cleanup(func() { setActiveTheme(config.AppearanceConfig{}) })
	tests := []struct {
		configured int
		want       float32
	}{
		{0, float32(config.DefaultEditorFontSize)},
		{9, float32(config.MinEditorFontSize)},
		{12, 12},
		{18, float32(config.MaxEditorFontSize)},
		{99, float32(config.MaxEditorFontSize)},
	}
	for _, tc := range tests {
		setActiveTheme(config.AppearanceConfig{Theme: "custom", Themes: []config.ThemeDef{{ID: "custom", Base: config.ThemeLight, Fonts: map[string]int{"editor": tc.configured}}}})
		st := &miniEditorState{}
		st.mu.Lock()
		got := st.editorFontSizeLocked()
		st.mu.Unlock()
		if got != tc.want {
			t.Errorf("EditorFontSize=%d 时字号 = %v, want %v", tc.configured, got, tc.want)
		}
	}
}

// TestMiniEditorUsesStagedWorkbench 验证从聊天窗口打开时使用当前会话的工作台设置快照。
func TestMiniEditorUsesStagedWorkbench(t *testing.T) {
	s := newChatState(nil)
	settings := NewSettingsState(settingsTestDir(t), nil, s)
	settings.cfg.Workbench.EditorFontSize = 15
	setChatSettings(s, settings)

	s.stageMiniEditorWorkbench()
	st := newMiniEditorState(filepath.Join(t.TempDir(), "a.go"))
	if st.workbench.EditorFontSize != 15 {
		t.Fatalf("编辑器应使用会话设置快照: %+v", st.workbench)
	}
}

// TestMiniEditorAutoSaveDisabled 验证关闭自动保存后 scheduleSave 不再创建计时器。
func TestMiniEditorAutoSaveDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.go")
	if err := os.WriteFile(path, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := newMiniEditorState(path)
	disabled := false
	st.workbench.EditorAutoSave = &disabled

	st.text = "b\n"
	st.scheduleSave()

	st.mu.Lock()
	timer := st.timer
	scheduledFor := st.scheduledFor
	st.mu.Unlock()
	if timer != nil || scheduledFor != "" {
		t.Fatalf("关闭自动保存后不应排队保存: timer=%v scheduledFor=%q", timer, scheduledFor)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "a\n" {
		t.Fatalf("关闭自动保存不应改动磁盘: data=%q err=%v", data, err)
	}
}

// TestMiniEditorViewRendersAndTogglesPreview 验证编辑器窗口渲染与 Markdown 预览切换。
func TestMiniEditorViewRendersAndTogglesPreview(t *testing.T) {
	path := filepath.Join(t.TempDir(), "README.md")
	body := "# 标题\n\n预览正文内容\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	st := newMiniEditorState(path)
	tt := ui.NewTester(st.view, MiniEditorDefaultWidth, MiniEditorDefaultHeight)

	if !tt.HasText("README.md") || !tt.HasText("已打开") {
		t.Fatalf("编辑器标题区应显示文件路径与状态: %q", tt.Texts())
	}
	if _, ok := tt.Find("预览"); !ok {
		t.Fatalf("Markdown 文件应提供预览切换按钮")
	}

	// 切到预览：按钮文案变为“源码”，正文以 Markdown 渲染
	if err := tt.Click("预览"); err != nil {
		t.Fatalf("点击预览失败: %v", err)
	}
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	if _, ok := tt.Find("源码"); !ok {
		t.Fatalf("预览模式下按钮文案应为“源码”")
	}
	if !tt.HasText("预览正文内容") {
		t.Fatalf("预览应渲染 Markdown 正文: %q", tt.Texts())
	}

	// 切回源码编辑
	if err := tt.Click("源码"); err != nil {
		t.Fatalf("点击源码失败: %v", err)
	}
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	if _, ok := tt.Find("预览"); !ok {
		t.Fatalf("源码模式下按钮文案应为“预览”")
	}
}

// TestMiniEditorOpenInjection 验证打开函数的注入点。
func TestMiniEditorOpenInjection(t *testing.T) {
	old := openMiniEditorWindowFn
	var opened []string
	openMiniEditorWindowFn = func(path string) *mygo.Window {
		opened = append(opened, path)
		return nil
	}
	t.Cleanup(func() { openMiniEditorWindowFn = old })

	openMiniEditorPath("/tmp/demo.go")
	if len(opened) != 1 || opened[0] != "/tmp/demo.go" {
		t.Fatalf("应调用注入的打开函数, got %v", opened)
	}
	openMiniEditorPath("")
	if len(opened) != 1 {
		t.Fatalf("空路径不应打开窗口, got %v", opened)
	}
}
