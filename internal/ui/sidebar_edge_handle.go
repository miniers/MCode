package mcodeui

import (
	"sync"

	"github.com/egoist/mygo/ui"
)

// 侧边栏折叠后，窗口左缘保留一条窄热区，向右拖拽即可把侧边栏拉回来。

const (
	// sidebarEdgeHandleWidth 是折叠态下窗口左缘拖拽热区的宽度（pt）。
	sidebarEdgeHandleWidth = 5
	// sidebarEdgeExpandThreshold 是向右拖拽多少 pt 后展开侧边栏。
	sidebarEdgeExpandThreshold = 24
)

// edgeDragShouldExpand 判断向右的累计拖拽位移是否达到展开侧边栏的阈值。
func edgeDragShouldExpand(dx float32) bool {
	return dx >= sidebarEdgeExpandThreshold
}

// Dragged 只报每帧增量，累计位移按 chatState 记在侧表里（与 sidebarSearch 同法）。
var (
	sidebarEdgeDragMu  sync.Mutex
	sidebarEdgeDragAcc = make(map[*chatState]float32)
)

// accumulateEdgeDrag 累加本帧拖拽增量并返回累计位移。
func accumulateEdgeDrag(s *chatState, dx float32) float32 {
	sidebarEdgeDragMu.Lock()
	defer sidebarEdgeDragMu.Unlock()
	total := sidebarEdgeDragAcc[s] + dx
	sidebarEdgeDragAcc[s] = total
	return total
}

// resetEdgeDrag 清空该状态的拖拽累计位移。
func resetEdgeDrag(s *chatState) {
	sidebarEdgeDragMu.Lock()
	delete(sidebarEdgeDragAcc, s)
	sidebarEdgeDragMu.Unlock()
}

// renderSidebarEdgeHandle 渲染侧边栏折叠时窗口左缘的拖拽热区：5pt 宽、全高、
// 透明的绝对定位元素，悬停时显示水平双向调整光标；向右拖拽累计超过阈值即展开
// 侧边栏并重置拖拽状态。侧边栏展开时不渲染任何元素，布局与折叠前一致。
func renderSidebarEdgeHandle(c *ui.Context, s *chatState) ui.Element {
	s.mu.Lock()
	collapsed := s.sidebarCollapsed
	s.mu.Unlock()
	if !collapsed {
		return ui.Element{}
	}

	handle := ui.Box(c.Key("sidebar-edge-handle")).
		Absolute().
		Left(0).
		Top(0).
		Width(sidebarEdgeHandleWidth).
		FillHeight().
		Cursor(ui.CursorResizeEW).
		Label("展开侧边栏").
		Tooltip("向右拖拽展开侧边栏")

	if dx, _, held := handle.Dragged(); held {
		if edgeDragShouldExpand(accumulateEdgeDrag(s, dx)) {
			// 达到阈值：重置拖拽状态并展开侧边栏（与 ⌘B 一致，写入 UI 状态）；展开后本热区不再渲染。
			resetEdgeDrag(s)
			toggleSidebarPersisted(s)
		}
	} else {
		resetEdgeDrag(s)
	}

	return handle
}
