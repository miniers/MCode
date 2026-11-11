package mcodeui

import (
	"math"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

// 右侧分栏面板的标识，同时用作宽度偏好的键（ui-state.json 的 side_pane_widths）。
const (
	sidePaneFileID    = "file"
	sidePanePreviewID = "preview"
	sidePaneDiffID    = "diff"
)

// sidePane 只描述目的和旧入口；开关、选择和宽度由 Inspector 拥有。
type sidePane struct {
	id       string
	title    string
	splitKey string
	render   func(c *ui.Context, s *chatState)

	// 开关入口的注册信息：快捷键/菜单动作 id（与 shortcuts.go 一致）、菜单与命令面板的
	// 展示文案，以及真正的开关函数。root.go 的快捷键分发、app_menu.go 与 palette.go 的
	// 条目都由这些字段生成，不再各写一遍。
	actionID     string
	toggle       func(s *chatState)
	menuLabel    string
	menuAccel    string
	paletteID    string
	paletteTitle string
}

// sidePanes 的顺序就是顶部目的标签顺序。
var sidePanes []sidePane

func init() {
	sidePanes = []sidePane{
		{
			id:       sidePaneFileID,
			title:    "文件",
			splitKey: "chat-inspector-split",
			render:   renderFileManagerPanel,

			actionID: "toggle-file-manager",
			toggle: func(s *chatState) {
				toggleFileManager(s)
			},
			menuLabel:    "文件管理",
			menuAccel:    "Shift+CmdOrCtrl+F",
			paletteID:    "cmd-toggle-file-manager",
			paletteTitle: "显示/隐藏文件面板",
		},
		{
			id:       sidePaneDiffID,
			title:    "改动",
			splitKey: "chat-inspector-split",
			render:   renderDiffPanel,

			actionID: "toggle-diff",
			toggle: func(s *chatState) {
				s.toggleDiffPanel()
			},
			menuLabel:    "显示/隐藏改动",
			menuAccel:    "CmdOrCtrl+D",
			paletteID:    "cmd-toggle-diff",
			paletteTitle: "显示/隐藏改动",
		},
		{
			id: sidePanePreviewID, title: "浏览器", splitKey: "chat-inspector-split",
			render: renderPreviewPanel, actionID: "toggle-preview", toggle: togglePreview,
			menuLabel: "显示/隐藏网页预览", menuAccel: "Shift+CmdOrCtrl+B",
			paletteID: "cmd-open-preview", paletteTitle: "打开 Web 预览",
		},
	}
}

// sidePaneByAction 按快捷键/菜单动作 id 取注册表中的面板，未知 id 返回 nil。
// root.go 用它把这三条开关动作统一派发到各面板自己的 toggle。
func sidePaneByAction(id string) *sidePane {
	if id == "" {
		return nil
	}
	for i := range sidePanes {
		if sidePanes[i].actionID == id {
			return &sidePanes[i]
		}
	}
	return nil
}

func fileManagerPaneOpen(s *chatState) bool {
	return inspectorPaneOpen(s, sidePaneFileID)
}

// fileManagerPaneWidth 返回文件管理面板的宽度字段指针；状态不存在时返回 nil。调用方持 fileManagerMu。
func previewPaneOpen(s *chatState) bool {
	return inspectorPaneOpen(s, sidePanePreviewID)
}

// previewPaneWidth 返回网页预览面板的宽度字段指针；状态不存在时返回 nil。调用方持 previewMu。
func diffPaneOpen(s *chatState) bool {
	return inspectorPaneOpen(s, sidePaneDiffID)
}

// diffPaneWidth 返回改动面板的宽度字段指针；状态不存在时返回 nil。调用方持 diffPanelMu。
func closeDiffPanelPane(s *chatState) {
	if inspectorPaneOpen(s, sidePaneDiffID) {
		inspectorClose(s)
	}
}

// isOpenNow 在面板自己的锁内读取展开状态。
func (p *sidePane) isOpenNow(s *chatState) bool {
	return inspectorPaneOpen(s, p.id)
}

func (p *sidePane) close(s *chatState) {
	if p.isOpenNow(s) {
		inspectorClose(s)
	}
}

// widthPointer 在面板锁内把宽度夹取到该面板允许的范围并返回字段指针，供分栏直接读写：
// 小于最小值时回落到默认宽度（拖到极窄后回弹），大于最大值时取上限。
// 面板状态不存在时返回 nil，调用方按无侧栏处理。
func (p *sidePane) widthPointer(s *chatState) *float32 {
	ptr := p.rawWidthPointer(s)
	if ptr == nil {
		return nil
	}
	if *ptr < inspectorMinWidth || math.IsNaN(float64(*ptr)) || math.IsInf(float64(*ptr), 0) {
		*ptr = inspectorDefaultWidth
	} else if *ptr > inspectorMaxWidth {
		*ptr = inspectorMaxWidth
	}
	return ptr
}

// rawWidthPointer 在面板锁内返回宽度字段指针，不做任何夹取：拖动过程中必须允许宽度
// 压到最小宽度以下（松手时才决定收起还是回弹），否则每帧都会被 widthPointer 弹回默认宽度，
// 手柄会一跳一跳地抖。
func (p *sidePane) rawWidthPointer(s *chatState) *float32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := inspectorExistingSessionLocked(s, s.selectedThreadID)
	if st == nil {
		return nil
	}
	return &st.Width
}

// activeSidePane 只由当前会话的单一 Active 决定。
func activeSidePane(s *chatState) *sidePane {
	if s == nil {
		return nil
	}
	for i := range sidePanes {
		if sidePanes[i].isOpenNow(s) {
			return &sidePanes[i]
		}
	}
	return nil
}

// 分栏手柄的几何与交互参数。
const (
	// sidePaneHandleWidth 是分栏手柄的命中区宽度（DIP）：比 1 DIP 的分割线宽，指针容易抓住。
	sidePaneHandleWidth float32 = 7
	// sidePaneHandleStep 是手柄获得焦点后，左右方向键每次调整面板宽度的幅度（DIP）。
	sidePaneHandleStep float32 = 8
	// sidePaneHandleInkAlpha 是悬停/拖动时高亮竖线的不透明度。
	sidePaneHandleInkAlpha float32 = 0.5
	// sidePaneHandleLabel 是手柄的无障碍标签。
	sidePaneHandleLabel = "调整面板宽度"
	// sidePaneHandleHighlightLabel 是高亮竖线的无障碍标签：只在亮起时存在，headless 测试靠它确认可见性。
	sidePaneHandleHighlightLabel = "分栏高亮"
)

// sidePaneHandleEntry 是一个分栏手柄的跨帧状态。
type sidePaneHandleEntry struct {
	enteredAt time.Time // 指针进入手柄的时刻；零值表示当前没有悬停
	dragging  bool      // 上一帧是否在拖动：拖动中面板宽度不夹取，允许压到最小宽度以下
}

// sidePaneHandleState 按 splitKey 保存每个手柄的悬停进入时刻与拖动状态。
// mygo 只给元素暴露「此刻是否悬停/按下」，而 150ms 悬停延迟、松手收起都需要跨帧记忆，
// 所以与 sidebarHover 一样放包级表；表按窗口 Context 隔离，换窗口整表清零。
type sidePaneHandleState struct {
	mu    sync.Mutex
	ctx   *ui.Context
	items map[string]*sidePaneHandleEntry
}

var sidePaneHandles = sidePaneHandleState{items: make(map[string]*sidePaneHandleEntry)}

// entryLocked 取该窗口该手柄的状态；切换窗口时先清空整表。调用方须持锁。
func (st *sidePaneHandleState) entryLocked(ctx *ui.Context, key string) *sidePaneHandleEntry {
	if st.ctx != ctx {
		st.ctx = ctx
		st.items = make(map[string]*sidePaneHandleEntry)
	}
	e := st.items[key]
	if e == nil {
		e = &sidePaneHandleEntry{}
		st.items[key] = e
	}
	return e
}

// observe 推进悬停状态机：返回指针已停在该手柄上的时长，以及还需等多久高亮才会亮起。
// 调用方拿 wait 调 c.After：悬停未满 motionHoverDelay 时不会有帧，靠它把到点那一帧请出来。
func (st *sidePaneHandleState) observe(ctx *ui.Context, key string, now time.Time, hovered bool) (elapsed, wait time.Duration) {
	st.mu.Lock()
	defer st.mu.Unlock()
	e := st.entryLocked(ctx, key)
	if !hovered {
		e.enteredAt = time.Time{}
		return 0, 0
	}
	if e.enteredAt.IsZero() {
		e.enteredAt = now
	}
	if elapsed = now.Sub(e.enteredAt); elapsed < motionHoverDelay {
		return elapsed, motionHoverDelay - elapsed
	}
	return elapsed, 0
}

// dragging 报告上一帧该手柄是否在拖动。
func (st *sidePaneHandleState) dragging(ctx *ui.Context, key string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.entryLocked(ctx, key).dragging
}

// setDragging 记录该手柄这一帧是否在拖动。
func (st *sidePaneHandleState) setDragging(ctx *ui.Context, key string, dragging bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.entryLocked(ctx, key).dragging = dragging
}

// resetSidePaneHandlesForTest 清空手柄状态，避免测试之间串味。
func resetSidePaneHandlesForTest() {
	sidePaneHandles.mu.Lock()
	sidePaneHandles.ctx = nil
	sidePaneHandles.items = make(map[string]*sidePaneHandleEntry)
	sidePaneHandles.mu.Unlock()
}

// sidePaneHandleInkTarget 返回高亮竖线的目标不透明度：拖动中一直亮，悬停满 motionHoverDelay 才亮，
// 其余时刻为 0（移开后立即回到 0，真淡出由调用方的 AnimateTo 完成）。
func sidePaneHandleInkTarget(hovered, dragging bool, elapsed time.Duration) float32 {
	if dragging || (hovered && elapsed >= motionHoverDelay) {
		return sidePaneHandleInkAlpha
	}
	return 0
}

// sidePaneDraggedWidth 把一帧的拖动位移换算成面板宽度：分栏左侧是会话主区、右侧是面板，
// 所以手柄往左拖（dx 为负）面板变宽。宽度下限取 0：允许压到最小宽度以下，松手时再决定收起还是回弹。
// （不支持从右到左排版：mcode 用中文界面，手柄位移直接按窗口横轴算。）
func sidePaneDraggedWidth(width, dx float32) float32 {
	return max(0, width-dx)
}

// sidePaneReleasedWidth 决定松手后的宽度：窄于最小宽度的一半就收起面板（collapsed 为真，
// 宽度回到默认值），否则夹取到该面板的 [minWidth, maxWidth] 区间。
// 阈值取一半而不是最小宽度本身，避免「刚好压到 220 就被关掉」的误伤。
func sidePaneReleasedWidth(width, minWidth, defaultWidth, maxWidth float32) (float32, bool) {
	if width < minWidth*0.5 {
		return defaultWidth, true
	}
	return min(max(width, minWidth), maxWidth), false
}

// inspectorTopInset 仅在窗口右上角有系统按钮时将右栏下移让位。
func inspectorTopInset(c *ui.Context) float32 {
	if titleBarTrailingInset(c) > 0 {
		return headerInsetFor(paneMain)
	}
	return 0
}

// renderSidePaneHandle 渲染分栏拖拽手柄：7 DIP 命中区、1 DIP 常驻分割线，以及悬停/拖动时
// 叠加的 2 DIP 高亮竖线。手柄是分栏行的第二列，按需让出系统按钮、一直铺到窗口底部，
// 拖动产生的宽度变化同一帧就作用到右侧面板上。
//
// 为什么手柄必须显式参与纵向拉伸：Row/Column 在交叉轴上按内容自然高度对齐（mygo 的
// flexLayout 只在 alignAuto 时才回退到 Stretch，而 Row() 默认把 align 设成 Center），
// 手柄自己只有两个绝对定位的竖线子元素，不拉伸时盒子高度会塌成 0，命中区面积为 0，
// Hovered 永远为假、pointerDown 也打不到它，悬停高亮和拖动因此完全不生效。
//
// 为什么不用 ui.Split：它的分隔条与手柄是内部节点，既拿不到悬停/拖动状态，也改不了
// 「悬停 150ms 才亮」的延迟和 2 DIP 的线宽；而且它的 *size 是第一个窗格（会话主区）的宽度，
// 与这里的字段名正好相反（见并行的 ui.Split 实现：first 取 Width(*size)）。
func renderSidePaneHandle(c *ui.Context, s *chatState, p *sidePane, width *float32) {
	key := p.splitKey + "/handle"
	handle := ui.Box(c.Key(key)).
		Width(sidePaneHandleWidth).
		Shrink(0).
		Margin(inspectorTopInset(c), 0, 0, 0).
		Focusable().
		Role(ui.RoleSplitter).
		Label(sidePaneHandleLabel).
		Cursor(ui.CursorResizeEW)

	hovered := handle.Hovered()
	elapsed, wait := sidePaneHandles.observe(c, p.splitKey, c.Now(), hovered)
	if wait > 0 {
		// 悬停还没满 motionHoverDelay：到点时请求一帧，把高亮线亮起来。
		c.After(wait)
	}

	dx, _, held := handle.Dragged()
	_, _, released := handle.DragEnded()
	switch {
	case held:
		sidePaneHandles.setDragging(c, p.splitKey, true)
		if dx != 0 {
			// 上限在拖动中也不越：越过的部分直接夹掉，松手时就不会突然被拉回。
			*width = min(sidePaneDraggedWidth(*width, dx), inspectorMaxWidth)
		}
	case released:
		// 松手：压得过窄就收起面板并把宽度复位到默认值，否则夹取到面板自己的范围。
		next, collapsed := sidePaneReleasedWidth(*width, inspectorMinWidth, inspectorDefaultWidth, inspectorMaxWidth)
		*width = next
		sidePaneHandles.setDragging(c, p.splitKey, false)
		if collapsed {
			p.close(s)
		}
		inspectorRemember(s)
	default:
		// 只是点了一下（按下又松开却没移动）或根本没有按下。
		sidePaneHandles.setDragging(c, p.splitKey, false)
	}

	// 键盘可达：焦点在手柄上时左右各调 sidePaneHandleStep；面板在右侧，所以左箭头加宽。
	// 不做焦点陷阱，不做惯性，与 ui.Split 的方向键行为保持同一套语义（步长不同）。
	handle.OnShortcut(0, ui.KeyLeft, func() {
		*width = min(*width+sidePaneHandleStep, inspectorMaxWidth)
		persistUIState(s)
		inspectorRemember(s)
	})
	handle.OnShortcut(0, ui.KeyRight, func() {
		*width = max(*width-sidePaneHandleStep, inspectorMinWidth)
		persistUIState(s)
		inspectorRemember(s)
	})

	ink := handle.AnimateTo("ink", sidePaneHandleInkTarget(hovered, held, elapsed), ui.Animation{Duration: animateDuration(c, motionFast), Ease: motionEaseStandard})
	handle.Children(func() {
		// 常驻 1 DIP 分割线，垂直居中。
		ui.Box(c.Key(key + "/line")).
			Absolute().
			Top(0).
			Bottom(0).
			Left((sidePaneHandleWidth - 1) / 2).
			Width(1).
			Background(ThemeBorder(c))
		// 高亮竖线：2 DIP，透明度由动画值驱动，淡到 0 后不再建节点。
		if ink > 0 {
			ui.Box(c.Key(key + "/ink")).
				Absolute().
				Top(0).
				Bottom(0).
				Left((sidePaneHandleWidth - motionHandleLine) / 2).
				Width(motionHandleLine).
				Background(ThemeAccent(c).Alpha(ink)).
				Label(sidePaneHandleHighlightLabel)
		}
	})
}

// renderSidePaneSplit 渲染主区与其右侧面板的分栏：没有面板打开时只渲染会话主区，
// 否则先夹取宽度，再按「主区 Grow(1) + 7 DIP 手柄 + 面板 Width(宽度)」排一行；
// 手柄与面板仅在右上角有系统按钮时从标题栏下沿开始。
// 拖动结束（含方向键调整）写盘持久化宽度，沿用 persistUIState 的节流落盘。
func renderSidePaneSplit(c *ui.Context, s *chatState, renderChat func()) {
	if renderChat == nil {
		return
	}
	inspectorValidateWorkspace(s)
	if st := inspectorSession(s); st != nil {
		inspectorSyncVisibility(st)
	}
	host := ui.Row(c.Key("chat-inspector-split")).Fill().AlignItems(ui.Stretch)
	bounds := host.Bounds()
	windowW, windowH := c.Size()
	available := bounds.W
	if available <= 0 {
		available = windowW
		c.After(time.Nanosecond)
	}
	p := activeSidePane(s)
	if p == nil {
		host.Children(renderChat)
		return
	}
	width := p.rawWidthPointer(s)
	if !sidePaneHandles.dragging(c, p.splitKey) {
		width = p.widthPointer(s)
	}
	if width == nil {
		host.Children(renderChat)
		return
	}
	overlay, actual := inspectorLayout(available, *width)
	if sidePaneHandles.dragging(c, p.splitKey) && !overlay {
		actual = min(*width, available-inspectorChatMinWidth-sidePaneHandleWidth)
	}
	host.Children(func() {
		ui.Box(c.Key("chat-inspector-main")).Grow(1).Shrink(1).MinWidth(0).Children(renderChat)
		if overlay {
			return
		}
		renderSidePaneHandle(c, s, p, width)
		_, actual = inspectorLayout(available, *width)
		if sidePaneHandles.dragging(c, p.splitKey) {
			actual = min(*width, available-inspectorChatMinWidth-sidePaneHandleWidth)
		}
		ui.Column(c.Key("inspector-shell")).Width(actual).Shrink(0).Clip().
			Margin(inspectorTopInset(c), 0, 0, 0).
			Transition(motionSidePanel(c, false)).Children(func() { renderInspectorBody(c, s, actual) })
	})
	if overlay {
		open := true
		if bounds.W <= 0 {
			bounds = ui.Rect{W: available, H: windowH}
		}
		ui.DialogBase(c.Key("inspector-overlay"), &open, func(backdrop, panel ui.Element) {
			backdrop.Left(bounds.X).Top(bounds.Y + headerInsetFor(paneMain)).
				Right(max(0, windowW-bounds.X-bounds.W)).Bottom(max(0, windowH-bounds.Y-bounds.H))
			panel.Absolute().Right(0).Top(0).Bottom(0).MaxWidth(actual).MaxHeight(max(0, bounds.H-headerInsetFor(paneMain)))
			ui.Column(c.Key("inspector-overlay-card")).Width(actual).FillHeight().Clip().
				Background(ThemeWindowBg(c)).Transition(motionSidePanel(c, false)).
				Children(func() { renderInspectorBody(c, s, actual) })
		})
		if !open {
			inspectorClose(s)
		}
	}
}

func renderInspectorBody(c *ui.Context, s *chatState, width float32) {
	st := inspectorSession(s)
	if st == nil {
		return
	}
	renderInspectorHeader(c, s, st)
	if !st.Open {
		return
	}
	fileManagerMu.Lock()
	st.Files.width = width
	fileManagerMu.Unlock()
	if p := activeSidePane(s); p != nil {
		ui.Box(c.Key("inspector-body")).Grow(1).MinHeight(0).FillWidth().Children(func() { p.render(c, s) })
	}
}

// sidePanePrefs 保存每个窗口从 ui-state.json 恢复的侧栏宽度偏好（面板 ID -> 宽度），
// 独立于面板状态存放：状态可能还没创建（例如预览从未打开），偏好仍要在首次创建状态时生效。
var (
	sidePanePrefsMu sync.Mutex
	sidePanePrefs   = make(map[*chatState]map[string]float32)
)

// setSidePaneWidthPreferences 记录从 ui-state.json 恢复的侧栏宽度偏好；空表表示没有偏好。
func setSidePaneWidthPreferences(s *chatState, widths map[string]float32) {
	if s == nil {
		return
	}
	var cp map[string]float32
	for id, w := range widths {
		if w <= 0 {
			continue
		}
		if cp == nil {
			cp = make(map[string]float32, len(widths))
		}
		cp[id] = w
	}
	sidePanePrefsMu.Lock()
	if cp == nil {
		delete(sidePanePrefs, s)
	} else {
		sidePanePrefs[s] = cp
	}
	sidePanePrefsMu.Unlock()
}

// sidePaneInitialWidth 返回面板首次创建状态时应使用的宽度：把偏好宽度夹取到该面板范围内，
// 没有偏好或偏好过窄（小于最小值）时用默认宽度。
func sidePaneInitialWidth(s *chatState, id string, minWidth, defaultWidth, maxWidth float32) float32 {
	sidePanePrefsMu.Lock()
	w := sidePanePrefs[s][id]
	sidePanePrefsMu.Unlock()
	if w <= 0 || w < minWidth {
		return defaultWidth
	}
	if w > maxWidth {
		return maxWidth
	}
	return w
}

// sidePaneWidthsForPersist 返回写盘用的各面板宽度（在各自面板锁内读取），
// 从未创建过状态的面板不记录（保持 nil，避免把默认宽度固化成偏好）。
func sidePaneWidthsForPersist(s *chatState) map[string]float32 {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := inspectorExistingSessionLocked(s, s.selectedThreadID)
	if st == nil {
		return nil
	}
	widths := make(map[string]float32, len(sidePanes))
	for _, p := range sidePanes {
		widths[p.id] = st.Width
	}
	return widths
}

// releaseSidePanePreferences 在窗口关闭时清掉宽度偏好，避免按 chatState 累积。
func releaseSidePanePreferences(s *chatState) {
	if s == nil {
		return
	}
	sidePanePrefsMu.Lock()
	delete(sidePanePrefs, s)
	sidePanePrefsMu.Unlock()
}
