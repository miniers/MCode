package mcodeui

import (
	"math"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// 时间线内容列的最大宽度。标准档为 768，宽档在宽屏上放宽，
// 「按比例」档（配置里仍写作 full）按聊天区可用宽度乘百分比：100% 时等同原来的铺满，
// 比例越小两侧留白越多。
const (
	timelineContentMaxWidthStandard float32 = 768
	timelineContentMaxWidthWide     float32 = 1120
)

// composerDockMaxWidth 是停靠输入框的宽度上限，固定为标准档宽度，不跟随「内容宽度」设置：
// 输入框以及贴在它上方的审批卡、提问横幅都以此为对齐基准，
// 时间线正文则按 timelineContentMaxWidth 随设置变宽。
const composerDockMaxWidth float32 = timelineContentMaxWidthStandard

// 内容列左右内边距，与 Density 的 PadX 同值（紧凑档更窄）。
const contentColumnPadX float32 = Space4

// 用户消息气泡与用户消息编辑框的最大宽度，档位与内容列一致。
// userBubbleMaxWidthFull 是按比例档的上限：气泡取内容宽度的 75%，但不超过它，也不小于 600。
const userBubbleMaxWidthFull float32 = 960

// 按比例档的用户消息气泡：占内容宽度的比例与下限。
const (
	userBubbleFullRatio float32 = 0.75
	userBubbleFullMin   float32 = 600
)

// timelineContentMaxWidth 返回当前外观设置下时间线内容列的最大宽度。
// 拿不到设置状态时按标准档处理，保证默认外观与改前完全一致。
func timelineContentMaxWidth(s *chatState) float32 {
	setting, percent, padX := contentWidthPrefs(s)
	switch setting {
	case config.ContentWidthWide:
		return timelineContentMaxWidthWide
	case config.ContentWidthFull:
		// 内容列宽度含自身左右 padding，100% 时正好铺满聊天区，与原来的“全宽”观感一致。
		return contentColumnWidth(chatPaneWidth(s), percent, padX) + 2*padX
	}
	return timelineContentMaxWidthStandard
}

// userBubbleMaxWidth 返回用户消息气泡的最大宽度。
func userBubbleMaxWidth(s *chatState) float32 {
	setting, percent, padX := contentWidthPrefs(s)
	switch setting {
	case config.ContentWidthWide:
		return bubbleMaxWidthFor(timelineContentMaxWidthWide, setting, percent, padX)
	case config.ContentWidthFull:
		bubble := min(userBubbleMaxWidthFull, contentColumnWidth(chatPaneWidth(s), percent, padX)*userBubbleFullRatio)
		return max(bubble, userBubbleFullMin)
	}
	return bubbleMaxWidthFor(timelineContentMaxWidthStandard, setting, percent, padX)
}

// contentColumnWidth 返回内容列自身的可视宽度（不含左右 padding）。
// 按比例档取聊天区可用宽度乘百分比；窄窗口下不比标准档更窄，可用宽度不足标准档时退化为铺满。
// 聊天区宽度还没测出来（本帧尚未渲染）时按标准档处理。
func contentColumnWidth(paneW float32, percent int, padX float32) float32 {
	standard := timelineContentMaxWidthStandard - 2*padX
	if paneW <= 0 {
		return standard
	}
	width := paneW * float32(percent) / 100
	return max(width, min(standard, paneW-2*padX))
}

// contentWidthPrefs 一次读出内容宽度档位、按比例档的百分比与列内边距。
// 时间线每行都要换算宽度，所以只取一次设置快照，避免逐项查表重复抢 settingsMu。
func contentWidthPrefs(s *chatState) (setting string, percent int, padX float32) {
	st := getChatSettings(s)
	if st == nil {
		return "", config.DefaultContentWidthPercent, DensityComfortable.PadX
	}
	setting = st.cfg.Appearance.ContentWidth
	percent = st.cfg.Appearance.EffectiveContentWidthPercent()
	return setting, percent, densityOf(st.cfg.Appearance).PadX
}

// timelineColumnPadX 返回内容列左右内边距，与 timeline_rows.go 渲染时间线列时用的保持一致。
func timelineColumnPadX(s *chatState) float32 {
	return densityFor(s).PadX
}

// chatPaneWidth 返回最近一帧聊天区的可用宽度（窗口宽度扣掉左侧边栏与右侧分栏）。
// 还没渲染过时返回 0，调用方按标准档处理。
func chatPaneWidth(s *chatState) float32 {
	if s == nil {
		return 0
	}
	return math.Float32frombits(s.chatPaneWidthBits.Load())
}

// setChatPaneWidth 记录本帧聊天区可用宽度。用原子量存 float32 的位模式：
// 时间线逐行渲染都要读它，不该为这个值抢 s.mu。
func setChatPaneWidth(s *chatState, width float32) {
	if s == nil {
		return
	}
	s.chatPaneWidthBits.Store(math.Float32bits(max(width, 0)))
}

// chatAreaWidth 是聊天区与子代理浮窗共用的主区宽度口径：窗口宽度先扣掉左侧边栏，
// 再扣掉右侧分栏（文件 / 预览 / 改动）打开时的宽度与 1pt 分隔线。
func chatAreaWidth(c *ui.Context, s *chatState) float32 {
	if s == nil || c == nil {
		return 0
	}
	winW, _ := c.Size()
	s.mu.Lock()
	sidebarW, sidebarHidden := s.sidebarWidth, s.sidebarCollapsed
	s.mu.Unlock()
	paneW := mainAreaWidth(winW, sidebarW, sidebarHidden)
	if p := activeSidePane(s); p != nil {
		if w := p.widthPointer(s); w != nil {
			paneW = max(paneW-*w-1, 0)
		}
	}
	return paneW
}

// updateChatPaneWidth 在渲染入口记录本帧聊天区可用宽度，口径见 chatAreaWidth。
func updateChatPaneWidth(c *ui.Context, s *chatState) {
	if s == nil || c == nil {
		return
	}
	setChatPaneWidth(s, chatAreaWidth(c, s))
}
