package mcodeui

import (
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
)

// 应用内弹窗（ui.DialogBase）按住标题栏可拖动：
//   - 偏移按弹窗 key 记在包级表里，渲染时给整块面板套一层位移（Translate）；
//   - 拖动累加偏移，并限制面板不拖出窗口可视区域；
//   - 弹窗关闭后偏移清空，下次打开重新居中。
//
// 缩放用法：卡片宽高取 modalResizeSize，卡片子元素末尾调用 modalResizeGrips，
// 标题行调用 modalDragHandle，面板调用 modalDragPanel。
//
// 弹窗居中由 mygo 的遮罩（DialogBase）完成，卡片本身不参与定位，
// 所以这里只做视觉位移，不改变弹窗的布局与居中基准。

// modalDragMargin 是面板离窗口边缘至少保留的间距（pt）。
const modalDragMargin float32 = 8

// 各弹窗的拖拽 key：同一个弹窗的面板与标题行用同一个 key。
const (
	modalDragSettings          = "modal-drag-settings"
	modalDragScriptsSheet      = "modal-drag-scripts-sheet"
	modalDragArchiveDelete     = "modal-drag-archive-delete"
	modalDragArchiveDeleteAll  = "modal-drag-archive-delete-all"
	modalDragArchiveWorktree   = "modal-drag-archive-worktree"
	modalDragRevert            = "modal-drag-revert"
	modalDragRenameThread      = "modal-drag-rename-thread"
	modalDragDeleteThread      = "modal-drag-delete-thread"
	modalDragDeleteTopic       = "modal-drag-delete-topic"
	modalDragRemoveProject     = "modal-drag-remove-project"
	modalDragWorktreeManage    = "modal-drag-worktree-manage"
	modalDragWorktreeMove      = "modal-drag-worktree-move"
	modalDragGitHubClone       = "modal-drag-github-clone"
	modalDragRemotePairing     = "modal-drag-remote-pairing"
	modalDragContextCreate     = "modal-drag-context-create"
	modalDragContextDiscard    = "modal-drag-context-discard"
	modalDragContextPanel      = "modal-drag-context-panel"
	modalDragPlanReader        = "modal-drag-plan-reader"
	modalDragKanbanPlanReview  = "modal-drag-kanban-plan-review"
	modalDragKanbanGoalOutputs = "modal-drag-kanban-goal-outputs"
	modalDragKanbanTaskDetail  = "modal-drag-kanban-task-detail"
	modalDragKanbanNewTask     = "modal-drag-kanban-new-task"
	modalDragKanbanNewGoal     = "modal-drag-kanban-new-goal"
	modalDragTablePopup        = "modal-drag-table-popup"
)

// modalDragOffset 是一个弹窗的拖拽状态：当前偏移，加上上一帧面板的布局框
// （不含偏移），后者用来把面板限制在窗口里。
type modalDragOffset struct {
	dx, dy float32
	x, y   float32
	w, h   float32
	rw, rh float32
}

var (
	modalDragMu      sync.Mutex
	modalDragOffsets = make(map[string]*modalDragOffset)
)

// modalDragReset 清空弹窗的拖拽偏移，下次打开回到居中。
// 弹窗关闭时调用（渲染函数里的 !open 分支即可）。
func modalDragReset(key string) {
	if key == "" {
		return
	}
	modalDragMu.Lock()
	delete(modalDragOffsets, key)
	modalDragMu.Unlock()
}

// modalDragPanelInset 是弹窗面板四周相对窗口边缘保留的留白。
const modalDragPanelInset float32 = 40

// modalDragModal 渲染弹窗并给整块面板套上按 key 记录的拖拽偏移，弹窗已关闭时顺带复位偏移；
// 这里用 DialogBase 而不是 Modal，因为 Modal 给面板自带背景与内边距，卡片外会多出一圈黑框，
// 卡片的外观全部由 UI 自己负责。
func modalDragModal(c *ui.Context, key string, open *bool, fn func()) {
	if open == nil || !*open {
		modalDragReset(key)
		return
	}
	modalDragPanel(c, key, ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.Color{}.Alpha(ScrimOpacity))
		// 保留 ui.Modal 原本的窗口尺寸限制：只限制面板大小，不额外添加任何外观。
		winW, winH := c.Size()
		panel.MaxWidth(winW - modalDragPanelInset).MaxHeight(winH - modalDragPanelInset)
		fn()
	}))
}

// modalDragPanel 记录面板的布局框并应用拖拽偏移；偏移为零时不动。
// 用 ui.DialogBase 自己搭遮罩的弹窗直接对 panel 调用。
func modalDragPanel(c *ui.Context, key string, panel ui.Element) {
	if c == nil || key == "" {
		return
	}
	winW, winH := c.Size()
	box := panel.LayoutBounds()

	modalDragMu.Lock()
	st := modalDragOffsets[key]
	if st == nil {
		st = &modalDragOffset{}
		modalDragOffsets[key] = st
	}
	if box.W > 0 && box.H > 0 {
		st.x, st.y, st.w, st.h = box.X, box.Y, box.W, box.H
	}
	// 窗口缩小后也要把面板拉回可视区域。
	st.dx = clampModalDrag(st.dx, st.x, st.w, winW)
	st.dy = clampModalDrag(st.dy, st.y, st.h, winH)
	dx, dy := st.dx, st.dy
	modalDragMu.Unlock()

	if dx == 0 && dy == 0 {
		return
	}
	panel.Translate(dx, dy)
}

// modalDragHandle 把弹窗的标题行变成拖拽把手：按住拖动累加偏移，
// 面板始终留在窗口可视区域内。标题行里的按钮（如「取消」）照常响应点击，
// 因为在按钮上按下的指针由按钮自己接管，不会走到把手。
func modalDragHandle(c *ui.Context, key string, handle ui.Element) ui.Element {
	handle.Cursor(ui.CursorGrab)
	dx, dy, held := handle.Dragged()
	if !held || (dx == 0 && dy == 0) {
		return handle
	}

	winW, winH := c.Size()
	modalDragMu.Lock()
	st := modalDragOffsets[key]
	if st == nil {
		st = &modalDragOffset{}
		modalDragOffsets[key] = st
	}
	st.dx = clampModalDrag(st.dx+dx, st.x, st.w, winW)
	st.dy = clampModalDrag(st.dy+dy, st.y, st.h, winH)
	modalDragMu.Unlock()

	handle.Cursor(ui.CursorGrabbing)
	c.Invalidate()
	return handle
}

// modalResizeSize 返回用户尺寸或默认尺寸，并限制在窗口内。
func modalResizeSize(key string, defW, defH, minW, minH, winW, winH float32) (w, h float32) {
	w, h = defW, defH
	modalDragMu.Lock()
	if st := modalDragOffsets[key]; st != nil {
		if st.rw > 0 { w = st.rw }
		if st.rh > 0 { h = st.rh }
	}
	modalDragMu.Unlock()
	return max(minW, min(w, winW-modalDragPanelInset)), max(minH, min(h, winH-modalDragPanelInset))
}

// modalResizeUserHeight 判断用户是否手动调整过高度，供随内容取高的面板保留用户尺寸。
func modalResizeUserHeight(key string) bool {
	modalDragMu.Lock()
	defer modalDragMu.Unlock()
	st := modalDragOffsets[key]
	return st != nil && st.rh > 0
}

// modalResizeApply 按实际尺寸变化补偿位移，保持对边不动。
func modalResizeApply(edge string, dx, dy, w, h, offX, offY, minW, minH, maxW, maxH float32) (float32, float32, float32, float32) {
	oldW, oldH := w, h
	if strings.Contains(edge, "left") { w -= dx }
	if strings.Contains(edge, "right") { w += dx }
	if strings.Contains(edge, "top") { h -= dy }
	if strings.Contains(edge, "bottom") { h += dy }
	w = max(minW, min(w, maxW))
	h = max(minH, min(h, maxH))
	if strings.Contains(edge, "left") { offX -= (w-oldW)/2 }
	if strings.Contains(edge, "right") { offX += (w-oldW)/2 }
	if strings.Contains(edge, "top") { offY -= (h-oldH)/2 }
	if strings.Contains(edge, "bottom") { offY += (h-oldH)/2 }
	return w, h, offX, offY
}

// modalResizeGrips 放在卡片子元素末尾，使边角抓手处于最上层。
func modalResizeGrips(c *ui.Context, key string, w, h float32) {
	modalResizeGripsMin(c, key, w, h, 360, 240)
}

func modalResizeGripsMin(c *ui.Context, key string, w, h, minW, minH float32) {
	winW, winH := c.Size()
	grips := []struct { edge string; x, y, w, h float32; cursor ui.Cursor; corner bool }{
		{"left", 0, 12, 6, max(0, h-24), ui.CursorResizeEW, false},
		{"right", w-6, 12, 6, max(0, h-24), ui.CursorResizeEW, false},
		{"top", 12, 0, max(0, w-24), 6, ui.CursorResizeNS, false},
		{"bottom", 12, h-6, max(0, w-24), 6, ui.CursorResizeNS, false},
		{"top-left", 0, 0, 12, 12, ui.CursorResizeNWSE, true},
		{"top-right", w-12, 0, 12, 12, ui.CursorResizeNESW, true},
		{"bottom-left", 0, h-12, 12, 12, ui.CursorResizeNESW, true},
		{"bottom-right", w-12, h-12, 12, 12, ui.CursorResizeNWSE, true},
	}
	for _, g := range grips {
		grip := ui.Box(c.Key(key+"-resize-"+g.edge)).Absolute().Left(g.x).Top(g.y).Width(g.w).Height(g.h).Cursor(g.cursor).Label("调整弹窗大小")
		if g.corner && grip.DoubleClicked() {
			modalDragReset(key)
			c.Invalidate()
			continue
		}
		dx, dy, held := grip.Dragged()
		if !held || (dx == 0 && dy == 0) { continue }
		modalDragMu.Lock()
		st := modalDragOffsets[key]
		if st == nil { st = &modalDragOffset{}; modalDragOffsets[key] = st }
		st.rw, st.rh, st.dx, st.dy = modalResizeApply(g.edge, dx, dy, w, h, st.dx, st.dy, minW, minH, winW-modalDragPanelInset, winH-modalDragPanelInset)
		modalDragMu.Unlock()
		c.Invalidate()
	}
}

// clampModalDrag 把偏移限制在让面板留出 modalDragMargin 边距的范围内；
// 面板比窗口还大时无法完全放下，就居中摆放。
func clampModalDrag(offset, pos, size, win float32) float32 {
	if size <= 0 || win <= 0 {
		return offset
	}
	lo := modalDragMargin - pos
	hi := win - modalDragMargin - pos - size
	if hi < lo {
		return (lo + hi) / 2
	}
	return max(lo, min(offset, hi))
}
