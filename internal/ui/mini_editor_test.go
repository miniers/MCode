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
	off := false
	settings.cfg.Workbench.EditorAutoSave = &off
	setChatSettings(s, settings)

	s.stageMiniEditorWorkbench()
	st := newMiniEditorState(filepath.Join(t.TempDir(), "a.go"))
	if st.workbench.EditorAutoSave == nil || *st.workbench.EditorAutoSave {
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

// TestUIReview20_MiniEditorOnCloseConfirmDiscard 验证保存失败后再次关窗弹出放弃确认框，
// 取消保留窗口、确认后放行，且文本等于基线时直接放行。
func TestUIReview20_MiniEditorOnCloseConfirmDiscard(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "discard.go")
	if err := os.WriteFile(path, []byte("init\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	st := newMiniEditorState(path)
	disabled := false
	st.workbench.EditorAutoSave = &disabled
	st.mu.Lock()
	st.text = "edited\n"
	st.mu.Unlock()

	if err := os.WriteFile(path, []byte("external\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 首次关窗：保存失败并拦截关闭，暂不弹确认框。
	ev1 := &mygo.CloseEvent{}
	st.onClose(ev1)
	if !ev1.DefaultPrevented() {
		t.Fatalf("首次关窗保存失败应取消关闭")
	}
	st.mu.Lock()
	blocked := st.closeBlocked
	confirmOpen := st.confirmDiscard
	st.mu.Unlock()
	if !blocked || confirmOpen {
		t.Fatalf("首次拦截状态不对: closeBlocked=%v confirmDiscard=%v", blocked, confirmOpen)
	}

	// 第二次关窗（仍未回到基线）：取消关闭并打开确认框。
	ev2 := &mygo.CloseEvent{}
	st.onClose(ev2)
	if !ev2.DefaultPrevented() {
		t.Fatalf("第二次关窗应取消关闭并弹出确认框")
	}

	tt := ui.NewTesterWith(func(c *ui.Context) {
		applyAppTheme(c)
		st.view(c)
	}, MiniEditorDefaultWidth, MiniEditorDefaultHeight, ui.TesterOptions{
		Preferences: ui.Preferences{ReduceMotion: true},
	})
	tt.Frame()
	if !tt.HasText("放弃未保存的修改？") || !tt.HasText("放弃") || !tt.HasText("取消") {
		t.Fatalf("第二次关窗后应展示放弃修改确认框: %v", tt.Texts())
	}

	// 点取消：关闭确认框，窗口保持打开（再关一次仍会拦截并重开确认框）。
	if err := tt.Click("取消"); err != nil {
		t.Fatalf("点击取消失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.confirmDiscard && !tt.HasText("放弃未保存的修改？")
	})
	ev3 := &mygo.CloseEvent{}
	st.onClose(ev3)
	if !ev3.DefaultPrevented() {
		t.Fatalf("取消确认框后再次关窗仍应取消关闭")
	}
	tt.Frame()
	if !tt.HasText("放弃未保存的修改？") {
		t.Fatalf("再次关窗后应重新打开确认框: %v", tt.Texts())
	}

	// 点放弃：确认关闭并放行后续关窗事件，不覆盖外部文件。
	if err := tt.Click("放弃"); err != nil {
		t.Fatalf("点击放弃失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.confirmDiscard && st.closingConfirmed
	})
	ev4 := &mygo.CloseEvent{}
	st.onClose(ev4)
	if ev4.DefaultPrevented() {
		t.Fatalf("确认放弃后应允许关闭窗口")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "external\n" {
		t.Fatalf("放弃修改不应覆盖磁盘内容: data=%q err=%v", data, err)
	}

	// 文本等于基线时，即使曾被拦截也直接放行且不弹确认框。
	stSame := newMiniEditorState(path)
	stSame.mu.Lock()
	stSame.closeBlocked = true
	stSame.saveErr = "旧错误"
	stSame.mu.Unlock()
	evSame := &mygo.CloseEvent{}
	stSame.onClose(evSame)
	if evSame.DefaultPrevented() {
		t.Fatalf("文本等于基线时应直接放行")
	}
	stSame.mu.Lock()
	sameConfirm := stSame.confirmDiscard
	stSame.mu.Unlock()
	if sameConfirm {
		t.Fatalf("文本等于基线时不应弹出确认框")
	}
}

// TestUIReview20_MiniEditorCmdSSaves 验证关闭与开启自动保存时 ⌘S 均立即落盘，失败走保存错误行。
func TestUIReview20_MiniEditorCmdSSaves(t *testing.T) {
	t.Run("关闭自动保存时⌘S立即保存", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "manual.go")
		if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		st := newMiniEditorState(path)
		disabled := false
		st.workbench.EditorAutoSave = &disabled
		st.mu.Lock()
		st.text = "v2\n"
		st.mu.Unlock()

		tt := ui.NewTesterWith(func(c *ui.Context) {
			applyAppTheme(c)
			st.view(c)
		}, MiniEditorDefaultWidth, MiniEditorDefaultHeight, ui.TesterOptions{
			Preferences: ui.Preferences{ReduceMotion: true},
		})
		tt.Frame()
		if data, err := os.ReadFile(path); err != nil || string(data) != "v1\n" {
			t.Fatalf("未按 ⌘S 前不应保存: data=%q err=%v", data, err)
		}

		tt.Key(ui.Cmd, ui.KeyS)
		tt.Frame()
		if data, err := os.ReadFile(path); err != nil || string(data) != "v2\n" {
			t.Fatalf("关闭自动保存时 ⌘S 应立即保存: data=%q err=%v", data, err)
		}
	})

	t.Run("关闭自动保存时⌘S失败走保存错误行", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "conflict.go")
		if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		st := newMiniEditorState(path)
		disabled := false
		st.workbench.EditorAutoSave = &disabled
		st.mu.Lock()
		st.text = "mine\n"
		st.mu.Unlock()
		if err := os.WriteFile(path, []byte("other\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		tt := ui.NewTesterWith(func(c *ui.Context) {
			applyAppTheme(c)
			st.view(c)
		}, MiniEditorDefaultWidth, MiniEditorDefaultHeight, ui.TesterOptions{
			Preferences: ui.Preferences{ReduceMotion: true},
		})
		tt.Frame()
		tt.Key(ui.Cmd, ui.KeyS)
		tt.Frame()
		if !tt.HasText("保存失败") || !tt.HasText("重新加载") {
			t.Fatalf("⌘S 失败应走保存错误行: %v", tt.Texts())
		}
	})

	t.Run("开启自动保存时⌘S跳过防抖立即保存", func(t *testing.T) {
		old := miniEditorSaveDelay
		miniEditorSaveDelay = 10 * time.Second
		t.Cleanup(func() { miniEditorSaveDelay = old })

		path := filepath.Join(t.TempDir(), "auto.go")
		if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		st := newMiniEditorState(path)
		enabled := true
		st.workbench.EditorAutoSave = &enabled
		st.mu.Lock()
		st.text = "v2\n"
		st.mu.Unlock()

		tt := ui.NewTesterWith(func(c *ui.Context) {
			applyAppTheme(c)
			st.view(c)
		}, MiniEditorDefaultWidth, MiniEditorDefaultHeight, ui.TesterOptions{
			Preferences: ui.Preferences{ReduceMotion: true},
		})
		tt.Frame()
		tt.Key(ui.Cmd, ui.KeyS)
		tt.Frame()
		if data, err := os.ReadFile(path); err != nil || string(data) != "v2\n" {
			t.Fatalf("开启自动保存时 ⌘S 应跳过防抖立即保存: data=%q err=%v", data, err)
		}
	})
}

// TestUIReview20_MiniEditorTooltipAndErrorShrink 验证标题路径 Tooltip 与窄宽下错误条不挤掉重新加载按钮。
func TestUIReview20_MiniEditorTooltipAndErrorShrink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "very_long_directory_name_for_tooltip_check", "main.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := newMiniEditorState(path)
	st.mu.Lock()
	st.saveErr = "文件已被外部改动，未覆盖；可点“重新加载”查看磁盘内容，这是一段较长的错误提示用于验证窄窗口下不挤出按钮"
	st.mu.Unlock()

	tt := ui.NewTesterWith(func(c *ui.Context) {
		applyAppTheme(c)
		st.view(c)
	}, 420, MiniEditorMinHeight, ui.TesterOptions{
		Preferences: ui.Preferences{ReduceMotion: true},
	})
	tt.Frame()

	reloadRect, ok := tt.Find("重新加载")
	if !ok {
		t.Fatalf("窄宽下应仍能找到重新加载按钮: %v", tt.Texts())
	}
	if reloadRect.X+reloadRect.W > 420 {
		t.Fatalf("错误文字应收缩不挤出重新加载按钮: reloadRect=%+v", reloadRect)
	}

	pathRect, ok := tt.Find(path)
	if !ok {
		t.Fatalf("标题栏应能定位到路径元素: %v", tt.Texts())
	}
	tt.Move(pathRect.X+pathRect.W/2, pathRect.Y+pathRect.H/2)
	tt.Advance(time.Second)
	tt.Frame()
	// 悬停后工具提示会把完整路径文本挂到浮层中；检查按钮与文本均正常渲染不崩溃。
	if !tt.HasText(path) {
		t.Fatalf("悬停标题栏路径后应保留完整路径展示: %v", tt.Texts())
	}
}
