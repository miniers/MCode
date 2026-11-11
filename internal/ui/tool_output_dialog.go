package mcodeui

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// 「显示完整输出」弹窗：工具详情里的 30 行预览放不下时，用独立弹窗展示可滚动的等宽全文，
// 并给出复制与关闭动作；时间线本身不再展开全文，避免超长输出把聊天流撑坏。
type toolOutputDialogState struct {
	title string // 工具标题（如「运行 go test ./...」）
	text  string // 打开时的全文快照，之后消息继续流式更新也不改这里的内容
}

const (
	// toolOutputDialogKey 是弹窗的拖拽 key，同时用作元素 key 前缀。
	toolOutputDialogKey = "tool-output-dialog"
	// toolOutputMaxHeight 是详情正文滚动区的最大高度（紧凑模式 180），再高就在区内滚动。
	toolOutputMaxHeight        float32 = 220
	toolOutputMaxHeightCompact float32 = 180
	// toolOutputPreviewLines 是详情区保留的首/尾预览行数。
	toolOutputPreviewLines = 30
	// toolOutputFollowThreshold 是「算作贴底」的像素阈值：离开底部超过它就不再跟随新输出。
	toolOutputFollowThreshold float32 = 4
)

// toolOutputScrollStateFor 返回该工具消息详情正文的滚动状态，首次使用时创建。
// 只在 UI 线程调用（renderToolDetail），因此不经过 s.mu。
const (
	// toolDialogMargin 是工具输出弹窗相对窗口宽高预留的边距（两侧合计）。
	toolDialogMargin float32 = 80
	// toolDialogMinBodyHeight 是正文滚动区在窗口很矮时保留的最小高度。
	toolDialogMinBodyHeight float32 = 120
	// toolDialogChromeHeight 是标题行与底部按钮占用的总高度。
	toolDialogChromeHeight float32 = 220
)

func (s *chatState) toolOutputScrollStateFor(id string) *ui.ScrollState {
	if s.toolOutputScroll == nil {
		s.toolOutputScroll = make(map[string]*ui.ScrollState)
	}
	st := s.toolOutputScroll[id]
	if st == nil {
		st = &ui.ScrollState{}
		s.toolOutputScroll[id] = st
	}
	return st
}

// openToolOutputDialog 打开「显示完整输出」弹窗并保存一份文本快照。
func (s *chatState) openToolOutputDialog(m Message, text string) {
	if s == nil || strings.TrimSpace(text) == "" {
		return
	}
	verb, target, detail := toolCallTitle(m)
	title := strings.Join(strings.Fields(toolCallRowLabel(toolCallVerb(verb, m), target, detail)), " ")
	s.mu.Lock()
	s.toolOutputDialog = &toolOutputDialogState{title: title, text: text}
	s.mu.Unlock()
	s.invalidate()
}

// closeToolOutputDialog 关闭「显示完整输出」弹窗，并复位拖拽偏移（下次打开重新居中）。
func (s *chatState) closeToolOutputDialog() {
	if s == nil {
		return
	}
	modalDragReset(toolOutputDialogKey)
	s.mu.Lock()
	open := s.toolOutputDialog != nil
	s.toolOutputDialog = nil
	s.mu.Unlock()
	if open {
		s.invalidate()
	}
}

// renderToolOutputDialog 渲染「显示完整输出」弹窗。它在会话根上渲染，同一时刻只有一个弹窗：
// 时间线行在虚拟列表内部，弹窗挂在行里不会成为根级弹窗。
func renderToolOutputDialog(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	s.mu.Lock()
	dlg := s.toolOutputDialog
	s.mu.Unlock()
	if dlg == nil {
		return
	}

	var appCfg config.AppearanceConfig
	if st := getChatSettings(s); st != nil {
		appCfg = st.cfg.Appearance
	}
	fontFam := codeFontFamily(appCfg)
	codeSz := codeFontSize()

	winW, winH := c.Size()
	width, height := modalResizeSize(toolOutputDialogKey, 640, max(360, winH-toolDialogMargin), 360, 240, winW, winH)
	open := true
	modalDragModal(c, toolOutputDialogKey, &open, func() {
		ui.Column(c.Key(toolOutputDialogKey + "-card")).
			Label(toolOutputDialogKey + "-card").
			Width(width).
			Height(height).
			Background(ThemeSurface(c)).
			BorderColor(ThemeBorder(c)).
			BorderWidth(Hairline).
			Radius(RadiusLG).
			Padding(Space6).
			Gap(Space4).
			Children(func() {
				// 标题行同时是弹窗的拖拽把手。
				modalDragHandle(c, toolOutputDialogKey, ui.Row(c.Key(toolOutputDialogKey+"-title-row")).FillWidth().Gap(Space3).Children(func() {
					ui.Text(c, dlg.title).
						FontSize(scaledFont(TypeTitle)).
						FontWeight(600).
						TextColor(ThemePrimaryText(c))
					ui.Text(c, fmt.Sprintf("共 %d 行", len(strings.Split(dlg.text, "\n")))).
						FontSize(scaledFont(TypeCaption)).
						TextColor(ThemeSecondaryText(c))
				}))
				ui.Scroll(c.Key(toolOutputDialogKey + "-scroll")).
					FillWidth().
					Grow(1).
					MinHeight(0).
					Children(func() {
						ui.Text(c, dlg.text).
							Font(fontFam).
							FontSize(scaledFont(codeSz)).
							LineHeight(1.4).
							TextColor(ThemeSecondaryText(c)).
							Selectable()
					})
				ui.Row(c.Key(toolOutputDialogKey + "-actions")).
					FillWidth().
					Justify(ui.End).
					Gap(Space3).
					Children(func() {
						// 「复制输出」全页唯一，免得按可见文字或无障碍名定位时撞到时间线里的「复制」。
						Button(c.Key(toolOutputDialogKey+"-copy"), ButtonOpts{
							Text:    "复制输出",
							Variant: ButtonSecondary,
							OnClick: func() {
								c.WriteClipboard(dlg.text)
							},
						})
						Button(c.Key(toolOutputDialogKey+"-close"), ButtonOpts{
							Text:    "关闭",
							Variant: ButtonSecondary,
							OnClick: func() {
								s.closeToolOutputDialog()
							},
						})
					})
				modalResizeGrips(c, toolOutputDialogKey, width, height)
			})
	})
	if !open {
		s.closeToolOutputDialog()
	}
}
