package mcodeui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"mcode/internal/config"
)

// miniEditorTopPadding 是迷你编辑器正文的顶部留白，为上方工具条让出固定高度。
const miniEditorTopPadding float32 = 40

// 迷你源码编辑器尺寸（860×640）。
const (
	MiniEditorDefaultWidth  = 860
	MiniEditorDefaultHeight = 640
	MiniEditorMinWidth      = 640
	MiniEditorMinHeight     = 420
)

// miniEditorSaveDelay 是停止输入后自动保存的延迟（测试中可缩短）。
var miniEditorSaveDelay = 800 * time.Millisecond

// isMarkdownPath 判断文件是否为 Markdown（决定是否提供源码/预览切换）。
func isMarkdownPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown", ".mdx":
		return true
	}
	return false
}

// shouldOverwriteFile 判断自动保存是否可以覆盖磁盘文件：
// 文件仍存在且内容等于编辑器上次读写的基线时才允许覆盖；
// 文件被外部改动或删除时返回 false，由调用方提示用户而不是静默覆盖。
func shouldOverwriteFile(baseline, onDisk string, exists bool) bool {
	if !exists {
		return false
	}
	return onDisk == baseline
}

// miniEditorState 是迷你源码编辑器窗口的状态。
// 文本字段由编辑区绑定在 UI 线程写入，后台计时器读写时加锁。
type miniEditorState struct {
	mu sync.Mutex

	path     string
	text     string // 编辑区当前文本
	baseline string // 上次读写的磁盘内容基线，用于外部改动检测
	loadErr  string // 读取失败（非 UTF-8/权限）时保持只读，永不写回
	preview  bool   // Markdown 预览开关
	saveErr  string
	saving   bool
	savedAt  time.Time

	timer        *time.Timer
	scheduledFor string // 已排入防抖保存的文本，避免每帧重启计时器
	services     ui.Services
	mdOpts       *markdown.Options
	workbench    config.WorkbenchConfig // 打开编辑器时的工作台设置快照
}

var (
	pendingMiniEditorWorkbenchMu sync.Mutex
	pendingMiniEditorWorkbench   *config.WorkbenchConfig
)

// stageMiniEditorWorkbench 记录当前会话的工作台设置，供随后打开的编辑器窗口使用。
func (s *chatState) stageMiniEditorWorkbench() {
	st := getChatSettings(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	wb := st.cfg.Workbench
	st.mu.Unlock()

	pendingMiniEditorWorkbenchMu.Lock()
	pendingMiniEditorWorkbench = &wb
	pendingMiniEditorWorkbenchMu.Unlock()
}

// takePendingMiniEditorWorkbench 取出并清除暂存的工作台设置。
func takePendingMiniEditorWorkbench() (config.WorkbenchConfig, bool) {
	pendingMiniEditorWorkbenchMu.Lock()
	defer pendingMiniEditorWorkbenchMu.Unlock()
	if pendingMiniEditorWorkbench == nil {
		return config.WorkbenchConfig{}, false
	}
	wb := *pendingMiniEditorWorkbench
	pendingMiniEditorWorkbench = nil
	return wb, true
}

// loadMiniEditorWorkbench 读取数据目录下的工作台设置；读取失败时返回零值（沿用内置默认）。
func loadMiniEditorWorkbench() config.WorkbenchConfig {
	dataDir := strings.TrimSpace(os.Getenv("MCODE_DATA"))
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return config.WorkbenchConfig{}
		}
		dataDir = filepath.Join(home, ".mcode")
	}
	cfg, err := config.Load(dataDir)
	if err != nil {
		return config.WorkbenchConfig{}
	}
	return cfg.Workbench
}

// newMiniEditorState 读取文件内容并构造编辑器状态；文件不存在时作为新建处理，保存时创建。
func newMiniEditorState(path string) *miniEditorState {
	workbench, ok := takePendingMiniEditorWorkbench()
	if !ok {
		workbench = loadMiniEditorWorkbench()
	}
	st := &miniEditorState{path: path, workbench: workbench}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if !utf8.Valid(data) {
			st.loadErr = "文件不是 UTF-8 文本，未打开（避免写坏原文件）"
			return st
		}
		st.text = string(data)
		st.baseline = string(data)
	case os.IsNotExist(err):
		// 新文件：首次保存时创建
	default:
		st.loadErr = "无法读取文件: " + err.Error()
	}
	return st
}

// scheduleSave 在停止输入约 800ms 后自动保存；重复输入会重置计时器。
func (st *miniEditorState) scheduleSave() {
	st.mu.Lock()
	if !st.workbench.EditorAutoSaveEnabled() {
		st.mu.Unlock()
		return
	}
	content := st.text
	st.scheduledFor = content
	if st.timer != nil {
		st.timer.Stop()
	}
	st.timer = time.AfterFunc(miniEditorSaveDelay, func() {
		st.saveIfUnchanged(content)
	})
	st.mu.Unlock()
}

// saveIfUnchanged 在没有新输入时把内容写回磁盘；写前检测磁盘是否被外部改动。
func (st *miniEditorState) saveIfUnchanged(content string) {
	st.mu.Lock()
	if st.loadErr != "" || st.text != content || content == st.baseline {
		st.mu.Unlock()
		return
	}
	st.saving = true
	path, baseline, text := st.path, st.baseline, st.text
	st.mu.Unlock()

	onDisk, err := os.ReadFile(path)
	switch {
	case err != nil && !os.IsNotExist(err):
		st.setSaveErr("读取文件失败: " + err.Error())
		return
	case os.IsNotExist(err):
		if baseline != "" {
			st.setSaveErr("文件已被删除，未自动保存")
			return
		}
	case !shouldOverwriteFile(baseline, string(onDisk), true):
		st.setSaveErr("文件已被外部改动，未覆盖；可点“重新加载”查看磁盘内容")
		return
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		st.setSaveErr("创建目录失败: " + err.Error())
		return
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		st.setSaveErr("保存失败: " + err.Error())
		return
	}

	st.mu.Lock()
	st.baseline = text
	st.saving = false
	st.saveErr = ""
	st.savedAt = time.Now()
	st.mu.Unlock()
	st.invalidate()
}

// saveNow 立即落盘（窗口关闭前的 flush），与防抖保存共用冲突检测。
func (st *miniEditorState) saveNow() {
	st.mu.Lock()
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
	text := st.text
	st.mu.Unlock()
	st.saveIfUnchanged(text)
}

// setSaveErr 记录保存错误并触发重绘。
func (st *miniEditorState) setSaveErr(msg string) {
	st.mu.Lock()
	st.saving = false
	st.saveErr = msg
	st.mu.Unlock()
	st.invalidate()
}

// reload 重新从磁盘读取文件，丢弃未保存的编辑（用于外部改动冲突后的恢复）。
func (st *miniEditorState) reload() {
	data, err := os.ReadFile(st.path)
	if err != nil {
		st.setSaveErr("重新加载失败: " + err.Error())
		return
	}
	if !utf8.Valid(data) {
		st.setSaveErr("文件不是 UTF-8 文本，未重新加载")
		return
	}
	st.mu.Lock()
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
	st.text = string(data)
	st.baseline = string(data)
	st.scheduledFor = st.text
	st.saveErr = ""
	st.loadErr = ""
	st.savedAt = time.Now()
	st.mu.Unlock()
	st.invalidate()
}

// invalidate 请求编辑器窗口重绘。
func (st *miniEditorState) invalidate() {
	st.mu.Lock()
	services := st.services
	st.mu.Unlock()
	services.Invalidate()
}

// view 构建编辑器窗口的界面：标题栏、行号列 + 多行编辑区、Markdown 源码/预览切换与保存状态。
func (st *miniEditorState) view(c *ui.Context) {
	st.mu.Lock()
	if st.services == (ui.Services{}) {
		st.services = c.Services()
	}
	loadErr := st.loadErr
	saveErr := st.saveErr
	preview := st.preview
	path := st.path
	st.mu.Unlock()

	// 停止输入后自动保存：内容偏离基线且未排队时重置 800ms 计时器
	st.mu.Lock()
	dirty := st.text != st.baseline && st.loadErr == ""
	needsSchedule := st.workbench.EditorAutoSaveEnabled() && dirty && st.text != st.scheduledFor
	fontSize := st.editorFontSizeLocked()
	st.mu.Unlock()
	if needsSchedule {
		st.scheduleSave()
	}

	ui.Column(c.Key("mini-editor-root")).Fill().Background(ThemeWindowBg(c)).Children(func() {
		// 标题区：文件路径 + 保存状态 + Markdown 源码/预览切换
		ui.Row(c.Key("mini-editor-header")).
			FillWidth().
			Padding(miniEditorTopPadding, Space5, Space3, Space5).
			AlignItems(ui.Center).
			Gap(Space3).
			Children(func() {
				ui.Icon(c, IconFile()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
				ui.Text(c, path).
					FontSize(TypeFootnote).
					TextColor(ThemePrimaryText(c)).
					Grow(1).
					SingleLine()

				ui.Text(c, miniEditorStatusText(st)).
					FontSize(TypeCaption).
					TextColor(ThemeSecondaryText(c))

				if isMarkdownPath(path) {
					label := "预览"
					if preview {
						label = "源码"
					}
					Button(c.Key("mini-editor-preview-toggle"), ButtonOpts{
						Text:    label,
						Variant: ButtonSecondary,
						OnClick: func() {
							st.mu.Lock()
							st.preview = !st.preview
							st.mu.Unlock()
							st.invalidate()
						},
					})
				}
			})

		if loadErr != "" {
			ui.Row(c.Key("mini-editor-load-error")).
				FillWidth().
				Padding(Space2, Space5).
				Background(ThemeDanger(c).Alpha(0.10)).
				Children(func() {
					ui.Text(c, loadErr).
						FontSize(TypeFootnote).
						TextColor(ThemeDanger(c))
				})
		}
		if saveErr != "" {
			ui.Row(c.Key("mini-editor-save-error")).
				FillWidth().
				Padding(Space2, Space5).
				AlignItems(ui.Center).
				Gap(Space3).
				Background(ThemeWarning(c).Alpha(0.12)).
				Children(func() {
					ui.Text(c, saveErr).
						FontSize(TypeFootnote).
						TextColor(ThemeWarning(c)).
						Grow(1)
					Button(c.Key("mini-editor-reload-btn"), ButtonOpts{
						Text:    "重新加载",
						Variant: ButtonSecondary,
						OnClick: func() {
							st.reload()
						},
					})
				})
		}

		if preview && isMarkdownPath(path) {
			// 预览模式：用 mygo markdown 组件渲染当前文本
			ui.Scroll(c.Key("mini-editor-preview-scroll")).Grow(1).FillWidth().Children(func() {
				ui.Column(c.Key("mini-editor-preview")).FillWidth().Padding(Space6).Children(func() {
					st.mu.Lock()
					if st.mdOpts == nil {
						theme := markdown.DefaultTheme(c.Theme())
						st.mdOpts = &markdown.Options{Theme: theme}
					}
					opts := st.mdOpts
					text := st.text
					st.mu.Unlock()
					markdown.View(c.Key("mini-editor-preview-view"), text, opts)
				})
			})
			return
		}

		// 编辑模式：行号列 + 多行编辑区，两者同处一个滚动容器
		st.mu.Lock()
		text := st.text
		st.mu.Unlock()
		lineCount := strings.Count(text, "\n") + 1

		ui.Scroll(c.Key("mini-editor-scroll")).Grow(1).FillWidth().Children(func() {
			ui.Row(c.Key("mini-editor-body")).FillWidth().AlignItems(ui.Start).Children(func() {
				// 行号列
				ui.Column(c.Key("mini-editor-gutter")).
					Padding(Space2, Space3).
					AlignItems(ui.End).
					Children(func() {
						for i := 1; i <= lineCount; i++ {
							ui.Text(c, strconv.Itoa(i)).
								Font("monospace").
								FontSize(fontSize).
								LineHeight(1.5).
								TextColor(ThemeSecondaryText(c).Alpha(0.7))
						}
					})

				// 多行编辑区
				area := ui.TextArea(c.Key("mini-editor-textarea"), &st.text).
					Lines(24, 400).
					Font("monospace").
					FontSize(fontSize).
					LineHeight(1.5).
					TextColor(ThemePrimaryText(c)).
					Background(ui.Transparent).
					BorderWidth(0).
					Padding(Space2, Space3).
					Grow(1).
					FillWidth()
				if loadErr != "" {
					area.ReadOnly(true)
				}
			})
		})
	})
}

// clampEditorFontSize 把配置里的编辑器字号规范到可渲染范围：0 或未设置回退默认值，
// 再夹到 MinEditorFontSize 与 MaxEditorFontSize 之间。
func clampEditorFontSize(size float32) float32 {
	switch {
	case size <= 0:
		size = float32(config.DefaultEditorFontSize)
	case size < float32(config.MinEditorFontSize):
		size = float32(config.MinEditorFontSize)
	case size > float32(config.MaxEditorFontSize):
		size = float32(config.MaxEditorFontSize)
	}
	return size
}

// editorFontSizeLocked 返回编辑器等宽字号；调用方持有 st.mu。
func (st *miniEditorState) editorFontSizeLocked() float32 {
	return clampEditorFontSize(float32(st.workbench.EditorFontSize))
}

// miniEditorStatusText 返回保存状态文案。
func miniEditorStatusText(st *miniEditorState) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	switch {
	case st.loadErr != "":
		return "只读"
	case st.saving:
		return "保存中…"
	case st.saveErr != "":
		return "保存失败"
	case st.text != st.baseline:
		return "编辑中"
	case st.savedAt.IsZero():
		return "已打开"
	default:
		return "已保存"
	}
}

// openMiniEditorFromPicker 从文件选择器选择文件后打开迷你源码编辑器。
// 目前没有“最近引用文件”的概念，统一走文件选择器。
func openMiniEditorFromPicker(s *chatState) {
	if s == nil {
		return
	}
	go func() {
		var parent *mygo.Window
		s.mu.Lock()
		if s.window != nil {
			parent = s.window()
		}
		s.mu.Unlock()

		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Title:  "打开源码文件",
			Parent: parent,
		})
		if err != nil {
			s.setToast("打开文件选择框失败: " + err.Error())
			return
		}
		if len(paths) == 0 {
			return
		}
		s.stageMiniEditorWorkbench()
		openMiniEditorPath(paths[0])
	}()
}

// openMiniEditorPath 打开编辑器窗口；注入的打开函数（测试）优先。
var openMiniEditorWindowFn func(path string) *mygo.Window = OpenMiniEditor

func openMiniEditorPath(path string) {
	if path == "" {
		return
	}
	if openMiniEditorWindowFn != nil {
		openMiniEditorWindowFn(path)
	}
}
