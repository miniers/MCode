package mcodeui

import (
	"fmt"
	"sync"

	"github.com/egoist/mygo/ui"
)

// iconQueueGrip 是排队消息行首的六点拖拽手柄（lucide grip-vertical）。
var iconQueueGrip = parseLucideSVG(`<circle cx="9" cy="5" r="1"/><circle cx="9" cy="12" r="1"/><circle cx="9" cy="19" r="1"/><circle cx="15" cy="5" r="1"/><circle cx="15" cy="12" r="1"/><circle cx="15" cy="19" r="1"/>`)

// composerPolishState 存放输入区打磨的视图状态（D-9 拖拽覆盖层、D-14 排队区）。
// 这些状态只与渲染有关，因此用包级 map 按 *chatState 存放，不给 chatState 加字段。
type composerPolishState struct {
	fileDragOver bool
	// queueExpanded 按会话记住排队区是否展开。
	queueExpanded map[string]bool
	// queueDropBefore 按被拖拽项记住最近悬停行的落点（插到哪个 ID 之前）。
	queueDropBefore map[string]string
}

var (
	composerPolishMu     sync.Mutex
	composerPolishStates = make(map[*chatState]*composerPolishState)
)

// getComposerPolishState 取（必要时创建）会话的输入区视图状态；会话已关闭时顺手清理。
func getComposerPolishState(s *chatState) *composerPolishState {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()

	composerPolishMu.Lock()
	defer composerPolishMu.Unlock()
	if closed {
		delete(composerPolishStates, s)
		return nil
	}
	st := composerPolishStates[s]
	if st == nil {
		st = &composerPolishState{
			queueExpanded:   make(map[string]bool),
			queueDropBefore: make(map[string]string),
		}
		composerPolishStates[s] = st
	}
	return st
}

// setComposerFileDragOver 记录本帧聊天区是否处于文件拖拽悬停状态。
func setComposerFileDragOver(s *chatState, over bool) {
	st := getComposerPolishState(s)
	if st == nil {
		return
	}
	composerPolishMu.Lock()
	defer composerPolishMu.Unlock()
	st.fileDragOver = over
}

// composerFileDragOver 返回聊天区的文件拖拽悬停状态。
func composerFileDragOver(s *chatState) bool {
	st := getComposerPolishState(s)
	if st == nil {
		return false
	}
	composerPolishMu.Lock()
	defer composerPolishMu.Unlock()
	return st.fileDragOver
}

// queueExpandedFor 返回指定会话的排队区是否展开（默认折叠）。
func queueExpandedFor(s *chatState, threadID string) bool {
	st := getComposerPolishState(s)
	if st == nil {
		return false
	}
	composerPolishMu.Lock()
	defer composerPolishMu.Unlock()
	return st.queueExpanded[threadID]
}

// toggleQueueExpanded 翻转指定会话的排队区展开状态。
func toggleQueueExpanded(s *chatState, threadID string) {
	st := getComposerPolishState(s)
	if st == nil {
		return
	}
	composerPolishMu.Lock()
	defer composerPolishMu.Unlock()
	st.queueExpanded[threadID] = !st.queueExpanded[threadID]
}

// setQueueDropBefore 记录拖拽项的落点；beforeID 为空表示清空该拖拽项的记录。
func setQueueDropBefore(s *chatState, draggedID, beforeID string) {
	st := getComposerPolishState(s)
	if st == nil || draggedID == "" {
		return
	}
	composerPolishMu.Lock()
	defer composerPolishMu.Unlock()
	if beforeID == "" {
		delete(st.queueDropBefore, draggedID)
		return
	}
	st.queueDropBefore[draggedID] = beforeID
}

// queueDropBeforeFor 返回拖拽项最近一次悬停给出的落点。
func queueDropBeforeFor(s *chatState, draggedID string) string {
	st := getComposerPolishState(s)
	if st == nil {
		return ""
	}
	composerPolishMu.Lock()
	defer composerPolishMu.Unlock()
	return st.queueDropBefore[draggedID]
}

// 拖拽覆盖层的几何与配色（D-9）。
const (
	composerDropOverlayInset      float32 = 8
	composerDropOverlayRadius     float32 = 16
	composerDropOverlayBorder     float32 = 2
	composerDropOverlayBorderA    float32 = 0.6
	composerDropOverlayFillA      float32 = 0.035
	composerDropOverlayPillHeight float32 = 32
	// composerQueueThumbIconSize 是排队项占位缩略图里的纸夹图标边长。
	composerQueueThumbIconSize float32 = 11
)

// renderComposerDropOverlay 在时间线与输入区整块区域上方铺拖拽提示层。
// 覆盖层不接收指针（PassThrough + IgnoreInput），文件拖放由聊天区整体处理；
// mygo 没有虚线描边，这里用 2pt 实线 accent 描边替代。
func renderComposerDropOverlay(c *ui.Context, s *chatState) {
	target := float32(0)
	if composerFileDragOver(s) {
		target = 1
	}

	layer := ui.Column(c.Key("composer-drop-overlay")).
		Absolute().
		Left(composerDropOverlayInset).
		Top(composerDropOverlayInset).
		Right(composerDropOverlayInset).
		Bottom(composerDropOverlayInset).
		Radius(composerDropOverlayRadius).
		Background(ThemeAccent(c).Alpha(composerDropOverlayFillA)).
		Border(composerDropOverlayBorder, ThemeAccent(c).Alpha(composerDropOverlayBorderA)).
		Justify(ui.Center).
		AlignItems(ui.Center).
		// 整层包括药丸都不接收指针：拖放事件由聊天区整体处理。
		PassThrough().
		IgnoreInput(true)

	alpha := layer.AnimateWith("composer-drop-overlay/alpha", target, animateDuration(c, motionFast), motionEaseStandard)
	layer.Opacity(alpha)
	if alpha <= 0.01 {
		return
	}

	layer.Children(func() {
		ui.Row(c.Key("composer-drop-overlay-pill")).
			Height(composerDropOverlayPillHeight).
			Radius(RadiusPill).
			Padding(0, Space4).
			Gap(Space2).
			AlignItems(ui.Center).
			Background(ThemeSurface(c)).
			Border(Hairline, ThemeBorder(c)).
			Children(func() {
				ui.Icon(c, iconPaperclip).
					Size(IconSM, IconSM).
					TextColor(ThemeAccent(c))
				ui.Text(c, "松开以添加附件").
					FontSize(TypeBody).
					TextColor(ThemePrimaryText(c))
			})
	})
}

// renderQueueGrip 是排队行首的六点拖拽手柄，只有手柄是拖拽源。
func renderQueueGrip(c *ui.Context, item QueuedFollowUp) {
	grip := ui.Row(c.Key("composer-queue-grip-" + item.ID)).
		Width(Space4).
		Height(ControlKbd).
		Justify(ui.Center).
		AlignItems(ui.Center).
		Cursor(ui.CursorPointer).
		Tooltip("拖动调整排队顺序").
		Drag(item.ID)
	grip.Children(func() {
		ui.Icon(c, iconQueueGrip).
			Size(IconSM, IconSM).
			TextColor(ThemeSecondaryText(c))
	})
}

// renderQueueAttachmentThumb 画排队项行首的 20x20 占位缩略图。
// 排队数据里只有附件数量、没有附件字节，因此这里用纸夹占位而不是真实图片。
func renderQueueAttachmentThumb(c *ui.Context, item QueuedFollowUp) {
	thumb := ui.Row(c.Key("composer-queue-thumb-"+item.ID)).
		Width(ControlXS).
		Height(ControlXS).
		Radius(RadiusXS).
		Background(ThemeHover(c)).
		Border(Hairline, ThemeBorder(c)).
		Justify(ui.Center).
		AlignItems(ui.Center).
		Tooltip(fmt.Sprintf("%d 个附件", item.AttachmentCount))
	thumb.Children(func() {
		ui.Icon(c, iconPaperclip).
			Size(composerQueueThumbIconSize, composerQueueThumbIconSize).
			TextColor(ThemeSecondaryText(c))
	})
}

// renderQueueDropLine 在目标行上沿画 2pt accent 插入线，提示松手后的落点。
func renderQueueDropLine(c *ui.Context, itemID string) {
	ui.Box(c.Key("composer-queue-drop-line-" + itemID)).
		Absolute().
		Left(0).
		Right(0).
		Top(-3).
		Height(Space0).
		Radius(RadiusPill).
		Background(ThemeAccent(c))
}

// queueCollapsedSummary 返回折叠行里的首条排队消息摘要（12pt 单行）。
func queueCollapsedSummary(items []QueuedFollowUp) string {
	if len(items) == 0 {
		return ""
	}
	text := items[0].Text
	if text == "" {
		if items[0].AttachmentCount > 0 {
			return fmt.Sprintf("%d 个附件", items[0].AttachmentCount)
		}
		return "（空消息）"
	}
	return truncateRunes(text, 24)
}
