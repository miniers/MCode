package mcodeui

import (
	"path"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

// fileManagerCodePreview 是某一栏代码预览缓存的编辑器状态：state 跨帧保留选区与滚动位置，
// content / fontSize 记录上次同步进去的文本与字号，用来判断这一帧是否需要更新。
type fileManagerCodePreview struct {
	state    *editor.State
	content  string
	fontSize float32
}

// fileManagerCodePreviewTab 报告该 tab 的预览是否由代码编辑器直接占满一栏。
// 代码编辑器自带滚动，这种预览不能再套外层 ui.Scroll，否则会出现两层滚动；
// 图片、错误、加载中、空文件和 Markdown 预览仍走原来的滚动容器。
func fileManagerCodePreviewTab(tab fileManagerTabSnapshot) bool {
	if tab.fileErr != "" || tab.kind == "image" || tab.kind == "binary" || tab.content == "" {
		return false
	}
	return !(tab.mdPreview && isMarkdownPath(tab.path))
}

// newFileManagerCodeState 按预览内容新建一份只读编辑器状态。
func newFileManagerCodeState(filePath, content string, size float32) *editor.State {
	buffer := editor.NewBuffer(content)
	buffer.SetLanguage(editor.LanguageFor(filePath))
	return &editor.State{
		Buffer:       buffer,
		Font:         ui.Font{Size: size},
		LineNumbers:  editor.LineNumbersOn,
		ReadOnly:     true,
		NoActiveLine: true,
		SoftWrap:     false,
		Label:        path.Base(filePath),
	}
}

// fileManagerCodeViewFor 返回 st 窗口里 key 对应的编辑器状态，并把文本与字号同步到最新。
// st 为 nil 时退化为每帧新建一个临时状态，不缓存。
// 文本变化时：新内容以旧内容为前缀（「加载更多」追加新块）就只把多出来的部分插到末尾，
// 这样编辑器的滚动位置与已有选区都不会跳回开头；否则用 Buffer.Reset 整体换文本。
func fileManagerCodeViewFor(st *fileManagerState, key, filePath, content string, size float32) *editor.State {
	if st == nil {
		return newFileManagerCodeState(filePath, content, size)
	}
	if st.codeViews == nil {
		st.codeViews = make(map[string]*fileManagerCodePreview)
	}
	view := st.codeViews[key]
	if view == nil {
		view = &fileManagerCodePreview{
			state:    newFileManagerCodeState(filePath, content, size),
			content:  content,
			fontSize: size,
		}
		st.codeViews[key] = view
		return view.state
	}
	if view.content != content {
		if strings.HasPrefix(content, view.content) {
			// Buffer.Edit 的坐标是编辑前文本的字节偏移，追加就是「末尾到末尾」插入。
			buffer := view.state.Buffer
			if rest := content[len(view.content):]; rest != "" {
				end := buffer.Len()
				buffer.Edit(editor.Edit{Start: end, End: end, Text: rest})
				// 预览是只读快照，追加不该把缓冲区标成「已修改」。
				// 撤销历史会留下一步（Buffer 没有单独清历史的公开方法），
				// 但只读编辑器不响应 Undo，用户碰不到。
				buffer.MarkSaved()
			}
		} else {
			view.state.Buffer.Reset(content)
		}
		view.content = content
	}
	if view.fontSize != size {
		// Theme 留空：DefaultTheme 会跟随当前窗口的浅色 / 深色主题。
		view.state.Font.Size = size
		view.fontSize = size
	}
	return view.state
}

// renderFileManagerCodePreview 渲染只读代码编辑器：按文件名识别语言做语法高亮，显示行号，
// 可选中复制，滚动与选区都由编辑器自己管理。字号跟随设置里的编辑器字号。
func renderFileManagerCodePreview(c *ui.Context, s *chatState, tab fileManagerTabSnapshot, keyPrefix string) ui.Element {
	manager := getFileManagerState(s)
	key := keyPrefix + tab.path
	st := fileManagerCodeViewFor(manager, key, tab.path, tab.content, fileManagerPreviewFontSize(s))
	e := ui.Column(c.Key(keyPrefix+"code-view-container")).
		Grow(1).
		FillWidth().
		MinWidth(0).
		MinHeight(0)
	if tab.reveal {
		lineCount := st.Buffer.LineCount()
		switch {
		case tab.hlFirst > lineCount && tab.hasMore && !tab.limitReached:
			s.loadMoreFileManagerTab(tab.path)
		case st.ViewportHeight() <= 0:
			s.invalidate()
		case lineCount > 0:
			st.GoTo(editor.Pos{Line: min(tab.hlFirst, lineCount) - 1})
			e.Jump("line-highlight", 0)
			fileManagerMu.Lock()
			if live := manager.findTabLocked(tab.path); live != nil && live.revealSeq == tab.revealSeq {
				live.reveal = false
			}
			fileManagerMu.Unlock()
		}
	}
	if tab.hlFirst > 0 && tab.hlLast >= tab.hlFirst {
		progress := float32(1)
		if reduceMotion(c) {
			e.Jump("line-highlight", 1)
		} else {
			progress = e.AnimateTo("line-highlight", 1, ui.Animation{Duration: DurBase, Ease: EaseSnap})
		}
		st.LineBackgrounds = []editor.LineBackground{{First: tab.hlFirst - 1, Last: tab.hlLast - 1, Color: ThemeAccentSubtle(c).Alpha(0.12 * progress)}}
	} else {
		st.LineBackgrounds = nil
	}
	e.Children(func() {
		editor.View(c.Key(keyPrefix+"code-view"), st).
			Grow(1).
			FillWidth().
			MinWidth(0).
			MinHeight(0)
	})
	return e
}

// pruneFileManagerCodeViews 删除 st 窗口本帧不再渲染的缓存项：关掉的 tab 与不再显示该 tab 的栏位。
// 只清理本窗口自己的缓存，不影响其他窗口已打开的预览。
func pruneFileManagerCodeViews(st *fileManagerState, live map[string]bool) {
	if st == nil {
		return
	}
	for key := range st.codeViews {
		if !live[key] {
			delete(st.codeViews, key)
		}
	}
}
