package mcodeui

import (
	"fmt"
	"strconv"
	"sync"

	"github.com/egoist/mygo/ui"
)

// sidebarStateGroup 是侧栏活跃会话按状态分出的组别。
type sidebarStateGroup int

const (
	sidebarStateGroupNone sidebarStateGroup = iota
	sidebarStateGroupNeedsYou
	sidebarStateGroupRunning
)

// sidebarThreadTitleOffset 是会话行标题相对行左边缘的偏移：状态图标 14 + 行内 Gap 8。
// 组头与分页栏的左内边距都追加这个偏移，让文字与会话标题对齐。
const sidebarThreadTitleOffset float32 = 22

// sidebarStateGroupOf 按优先级给会话归类：等你处理优先于运行中。
// busy 由调用方预先判定（侧栏用 threadStillBusy），这里只做纯逻辑分流。
func sidebarStateGroupOf(th Thread, busy bool) sidebarStateGroup {
	if th.HasPendingRequest {
		return sidebarStateGroupNeedsYou
	}
	if busy {
		return sidebarStateGroupRunning
	}
	return sidebarStateGroupNone
}

// splitSidebarStateGroups 把 active 按状态保序切成三段：等你处理、运行中、其余。
// 只追加到新切片，不修改入参，也不重新排序。
func splitSidebarStateGroups(active []Thread, busy func(Thread) bool) (needsYou, running, rest []Thread) {
	for _, th := range active {
		isBusy := busy != nil && busy(th)
		switch sidebarStateGroupOf(th, isBusy) {
		case sidebarStateGroupNeedsYou:
			needsYou = append(needsYou, th)
		case sidebarStateGroupRunning:
			running = append(running, th)
		default:
			rest = append(rest, th)
		}
	}
	return needsYou, running, rest
}

// sidebarStateGroupLatch 是「指针停在列表里时不挪行」的悬停闩锁：
// 指针在列表内的那些帧沿用上一帧的归属表，避免会话状态刚变就从指针下跳走。
// 指针离开列表后的下一帧恢复正常重算并归位。
type sidebarStateGroupLatch struct {
	mu sync.Mutex
	// frameHovered 表示本帧是否有会话行被悬停。
	frameHovered bool
	// pointerInside 表示上一帧结束时指针是否在列表内。
	pointerInside bool
	// pendingRegroup 表示冻结期间已有会话的当前分组和冻结分组不一致：
	// 指针离开后的那一帧仍沿用旧归属，需要再请求一帧把行放回正确的组。
	pendingRegroup bool
	// groups 记录 threadID → 上一帧的分组归属。
	groups map[string]sidebarStateGroup
}

var sidebarStateGroupLatchState sidebarStateGroupLatch

// beginFrame 在每个侧栏帧开始时清空本帧的悬停标记。
func (l *sidebarStateGroupLatch) beginFrame() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.frameHovered = false
}

// markRowHovered 标记本帧有会话行被指针悬停。
func (l *sidebarStateGroupLatch) markRowHovered() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.frameHovered = true
}

// endFrame 在侧栏帧结束时把本帧的悬停标记固化为「指针在列表内」，
// 并报告是否需要再请求一帧：指针刚离开、且冻结期间有会话待归位时，本帧仍沿用
// 旧归属，只有下一帧才会归位；不主动请求重绘的话界面会一直停在冻结的分组上。
func (l *sidebarStateGroupLatch) endFrame() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	needFrame := !l.frameHovered && l.pointerInside && l.pendingRegroup
	l.pointerInside = l.frameHovered
	l.frameHovered = false
	l.pendingRegroup = false
	return needFrame
}

// split 按闩锁规则切分 active：
// 上一帧指针在列表内时，仍在 active 中的会话沿用归属表里的旧分组，新出现的会话按当前
// 状态归类；否则全部按当前状态重新归类。归属表只保留本次 active 中的会话。
// busy 在锁外调用，避免回调里再取 UI 锁造成反向加锁。
func (l *sidebarStateGroupLatch) split(active []Thread, busy func(Thread) bool) (needsYou, running, rest []Thread) {
	current := make([]sidebarStateGroup, len(active))
	for i, th := range active {
		current[i] = sidebarStateGroupOf(th, busy != nil && busy(th))
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	next := make(map[string]sidebarStateGroup, len(active))
	pending := false
	for i, th := range active {
		group := current[i]
		if l.pointerInside {
			if old, ok := l.groups[th.ID]; ok {
				if old != group {
					// 冻结期间状态变了：指针离开后需要一帧把这一行归位。
					pending = true
				}
				group = old
			}
		}
		next[th.ID] = group
		switch group {
		case sidebarStateGroupNeedsYou:
			needsYou = append(needsYou, th)
		case sidebarStateGroupRunning:
			running = append(running, th)
		default:
			rest = append(rest, th)
		}
	}
	l.groups = next
	l.pendingRegroup = l.pointerInside && pending
	return needsYou, running, rest
}

// renderSidebarStateGroupHeader 渲染项目内的状态分组标题，非交互、不响应点击。
// key 带上项目 ID 与标题，Label 带上项目名与计数，保证同一页内唯一。
func renderSidebarStateGroupHeader(c *ui.Context, projectID, projectName, title string, count int, indent float32) {
	label := title + " · " + projectName + " · " + strconv.Itoa(count)
	ui.Row(c.Key("state-group-"+projectID+"-"+title)).
		Label(label).
		Padding(0, 0, 0, indent).
		AlignItems(ui.Center).
		Children(func() {
			ui.Text(c, fmt.Sprintf("%s  %d", title, count)).
				FontSize(scaledFont(TypeCaption)).
				FontWeight(600).
				TextColor(ThemeTertiaryText(c)).
				SingleLine()
		})
}
