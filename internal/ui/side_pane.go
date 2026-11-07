package mcodeui

import (
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

// sidePane 描述主区右侧一个可拖拽的分栏面板：会话主区与它组成一行分栏（主区 Grow(1) +
// 7 DIP 拖拽手柄 + 面板固定宽度），同一时刻只允许一个面板打开。三个面板（文件管理、
// 网页预览、改动）的挂载、互斥、宽度夹取与宽度持久化都收拢在这里，root.go 只调用
// renderSidePaneSplit。
//
// 约定：isOpen/width 只读取面板状态，不加锁也不新建状态，调用方必须持有该面板自己的
// mutex（即 mu，与面板状态结构共用同一把锁）；close 与 render 自行加锁，调用方不得持锁调用。
type sidePane struct {
	id           string
	mu           *sync.Mutex
	isOpen       func(s *chatState) bool
	close        func(s *chatState)
	width        func(s *chatState) *float32
	minWidth     float32
	defaultWidth float32
	maxWidth     float32
	splitKey     string
	render       func(c *ui.Context, s *chatState)

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

// sidePanes 是右侧分栏面板注册表，顺序即优先级：文件管理 > 网页预览 > 改动。
// 三者互斥，正常不会同时打开；优先级只在状态异常（例如外部直接改了状态字段）时决定渲染哪一个。
//
// 用 init() 赋值而不是包级字面量：条目里的 toggle 会调用 toggleFileManager/togglePreview/
// toggleDiffPanel，它们最终引用 sidePanes（经 closeOtherSidePanes），字面量写法会构成初始化循环。
var sidePanes []sidePane

func init() {
	sidePanes = []sidePane{
		{
			id:           sidePaneFileID,
			mu:           &fileManagerMu,
			isOpen:       fileManagerPaneOpen,
			close:        closeFileManager,
			width:        fileManagerPaneWidth,
			minWidth:     FileManagerPanelMinWidth,
			defaultWidth: FileManagerPanelDefaultWidth,
			maxWidth:     FileManagerPanelMaxWidth,
			splitKey:     "chat-file-split",
			render:       renderFileManagerPanel,

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
			id:           sidePanePreviewID,
			mu:           &previewMu,
			isOpen:       previewPaneOpen,
			close:        closePreviewPaneIfOpen,
			width:        previewPaneWidth,
			minWidth:     PreviewPanelMinWidth,
			defaultWidth: PreviewPanelDefaultWidth,
			maxWidth:     PreviewPanelMaxWidth,
			splitKey:     "chat-preview-split",
			render:       renderPreviewPanel,

			actionID: "toggle-preview",
			toggle: func(s *chatState) {
				togglePreview(s)
			},
			menuLabel:    "显示/隐藏网页预览",
			menuAccel:    "Shift+CmdOrCtrl+B",
			paletteID:    "cmd-open-preview",
			paletteTitle: "打开 Web 预览",
		},
		{
			id:           sidePaneDiffID,
			mu:           &diffPanelMu,
			isOpen:       diffPaneOpen,
			close:        closeDiffPanelPane,
			width:        diffPaneWidth,
			minWidth:     DiffPanelMinWidth,
			defaultWidth: DiffPanelDefaultWidth,
			maxWidth:     DiffPanelMaxWidth,
			splitKey:     "chat-diff-split",
			render:       renderDiffPanel,

			actionID: "toggle-diff",
			toggle: func(s *chatState) {
				s.toggleDiffPanel()
			},
			menuLabel:    "显示/隐藏改动",
			menuAccel:    "CmdOrCtrl+D",
			paletteID:    "cmd-toggle-diff",
			paletteTitle: "显示/隐藏改动",
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

// fileManagerPaneOpen 返回文件管理面板是否展开；调用方持 fileManagerMu。
func fileManagerPaneOpen(s *chatState) bool {
	st := fileManagerStates[s]
	return st != nil && st.open
}

// fileManagerPaneWidth 返回文件管理面板的宽度字段指针；状态不存在时返回 nil。调用方持 fileManagerMu。
func fileManagerPaneWidth(s *chatState) *float32 {
	st := fileManagerStates[s]
	if st == nil {
		return nil
	}
	return &st.width
}

// previewPaneOpen 返回网页预览面板是否可见；调用方持 previewMu。
func previewPaneOpen(s *chatState) bool {
	st := previewStates[s]
	return st != nil && st.visible
}

// previewPaneWidth 返回网页预览面板的宽度字段指针；状态不存在时返回 nil。调用方持 previewMu。
func previewPaneWidth(s *chatState) *float32 {
	st := previewStates[s]
	if st == nil {
		return nil
	}
	return &st.width
}

// diffPaneOpen 返回改动面板是否展开；调用方持 diffPanelMu。
func diffPaneOpen(s *chatState) bool {
	st := diffPanelStates[s]
	return st != nil && st.open
}

// diffPaneWidth 返回改动面板的宽度字段指针；状态不存在时返回 nil。调用方持 diffPanelMu。
func diffPaneWidth(s *chatState) *float32 {
	st := diffPanelStates[s]
	if st == nil {
		return nil
	}
	return &st.width
}

// closePreviewPaneIfOpen 关闭右侧网页预览面板。
// 未打开时不新建状态、不销毁 WebView（直接从注册表里查，不经过会创建状态的 getPreviewState）。
func closePreviewPaneIfOpen(s *chatState) {
	previewMu.Lock()
	st := previewStates[s]
	visible := st != nil && st.visible
	previewMu.Unlock()
	if visible {
		closePreview(s)
	}
}

// closeDiffPanelPane 关闭右侧改动面板；未打开时不新建状态。
func closeDiffPanelPane(s *chatState) {
	diffPanelMu.Lock()
	st := diffPanelStates[s]
	if st != nil {
		st.open = false
	}
	diffPanelMu.Unlock()
}

// isOpenNow 在面板自己的锁内读取展开状态。
func (p *sidePane) isOpenNow(s *chatState) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.isOpen(s)
}

// widthPointer 在面板锁内把宽度夹取到该面板允许的范围并返回字段指针，供分栏直接读写：
// 小于最小值时回落到默认宽度（拖到极窄后回弹），大于最大值时取上限。
// 面板状态不存在时返回 nil，调用方按无侧栏处理。
func (p *sidePane) widthPointer(s *chatState) *float32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	ptr := p.width(s)
	if ptr == nil {
		return nil
	}
	if *ptr < p.minWidth {
		*ptr = p.defaultWidth
	} else if *ptr > p.maxWidth {
		*ptr = p.maxWidth
	}
	return ptr
}

// rawWidthPointer 在面板锁内返回宽度字段指针，不做任何夹取：拖动过程中必须允许宽度
// 压到最小宽度以下（松手时才决定收起还是回弹），否则每帧都会被 widthPointer 弹回默认宽度，
// 手柄会一跳一跳地抖。
func (p *sidePane) rawWidthPointer(s *chatState) *float32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.width(s)
}

// activeSidePane 返回当前打开的右侧面板；多个同时打开时按注册顺序取优先级最高的一个。
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

// closeOtherSidePanes 关闭除 keepID 之外所有已打开的右侧面板，是所有互斥关系的唯一入口。
func closeOtherSidePanes(s *chatState, keepID string) {
	if s == nil {
		return
	}
	for i := range sidePanes {
		p := &sidePanes[i]
		if p.id == keepID || !p.isOpenNow(s) {
			continue
		}
		p.close(s)
	}
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

// renderSidePaneHandle 渲染分栏拖拽手柄：7 DIP 命中区、1 DIP 常驻分割线，以及悬停/拖动时
// 叠加的 2 DIP 高亮竖线。手柄是分栏行的第二列，上边距让出标题栏、一直铺到窗口底部，
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
		Margin(headerInsetFor(paneMain), 0, 0, 0).
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
			*width = min(sidePaneDraggedWidth(*width, dx), p.maxWidth)
		}
	case released:
		// 松手：压得过窄就收起面板并把宽度复位到默认值，否则夹取到面板自己的范围。
		next, collapsed := sidePaneReleasedWidth(*width, p.minWidth, p.defaultWidth, p.maxWidth)
		*width = next
		sidePaneHandles.setDragging(c, p.splitKey, false)
		if collapsed {
			p.close(s)
		}
		persistUIState(s)
	default:
		// 只是点了一下（按下又松开却没移动）或根本没有按下。
		sidePaneHandles.setDragging(c, p.splitKey, false)
	}

	// 键盘可达：焦点在手柄上时左右各调 sidePaneHandleStep；面板在右侧，所以左箭头加宽。
	// 不做焦点陷阱，不做惯性，与 ui.Split 的方向键行为保持同一套语义（步长不同）。
	handle.OnShortcut(0, ui.KeyLeft, func() {
		*width = min(*width+sidePaneHandleStep, p.maxWidth)
		persistUIState(s)
	})
	handle.OnShortcut(0, ui.KeyRight, func() {
		*width = max(*width-sidePaneHandleStep, p.minWidth)
		persistUIState(s)
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
// 手柄与面板都从标题栏下沿开始，把标题栏那条横带整条让给主区。
// 拖动结束（含方向键调整）写盘持久化宽度，沿用 persistUIState 的节流落盘。
//
// 为什么主区不设 Clip、为什么面板要让出标题栏：聊天标题栏自身的内容（侧栏开关、新建、
// 编排、权限、Git 胶囊、标题、动作组、辅助面板组）接近整行宽度，主区被面板压窄后右侧
// 辅助组按钮会越出主区右边界。主区一旦 Clip，这些按钮的命中矩形就被裁掉，点击会落到
// 右侧面板上（表现是面板展开后再点头部按钮没反应，例如「浏览器预览」关不掉）；
// 面板若从窗口顶部铺到底部，也会盖住标题栏那条横带，同样会抢走头部按钮的点击。
func renderSidePaneSplit(c *ui.Context, s *chatState, renderChat func()) {
	if renderChat == nil {
		return
	}
	p := activeSidePane(s)
	if p == nil {
		renderChat()
		return
	}
	// 拖动中不夹取宽度：允许压到最小宽度以下，松手时再决定收起还是回弹。
	var width *float32
	if sidePaneHandles.dragging(c, p.splitKey) {
		width = p.rawWidthPointer(s)
	} else {
		width = p.widthPointer(s)
	}
	if width == nil {
		renderChat()
		return
	}
	// 面板读的是手柄改写后的宽度，所以主区、手柄在前，面板在后。
	ui.Row(c.Key(p.splitKey)).Fill().AlignItems(ui.Stretch).Children(func() {
		// 主区吃掉剩下的宽度；MinWidth(0) 让它在面板很宽时也能压到 0，不必再靠 Clip 裁掉内容。
		ui.Box(c.Key(p.splitKey + "/main")).Grow(1).Shrink(1).MinWidth(0).Children(renderChat)
		renderSidePaneHandle(c, s, p, width)
		// 面板宽度直接挂在 *width 上，语义不再有歧义；上边距让出标题栏横带。
		ui.Box(c.Key(p.splitKey+"/panel")).
			Width(*width).
			Shrink(0).
			Clip().
			Margin(headerInsetFor(paneMain), 0, 0, 0).
			Children(func() {
				p.render(c, s)
			})
	})
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
	widths := make(map[string]float32, len(sidePanes))
	for i := range sidePanes {
		p := &sidePanes[i]
		p.mu.Lock()
		ptr := p.width(s)
		if ptr != nil && *ptr > 0 {
			widths[p.id] = *ptr
		}
		p.mu.Unlock()
	}
	if len(widths) == 0 {
		return nil
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
