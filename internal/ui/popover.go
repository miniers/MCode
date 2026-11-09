package mcodeui

import "github.com/egoist/mygo/ui"

// popoverCardShadow 给已有的弹出层卡片补上统一的外阴影。
// 该入口没有 *ui.Context（调用方在构建元素时拿不到），无法按深浅主题调整透明度，
// 只按 ElevPopover 的层叠透明度叠加；能拿到 c 时请改用 popoverCard 或 applyElevation。
func popoverCardShadow(card ui.Element) ui.Element {
	for _, layer := range ElevPopover {
		card = card.Shadow(0, layer.Y, layer.Blur, 0, ui.Color{}.Alpha(layer.Alpha))
	}
	return card
}

// popoverRiseEpsilon 是判断面板落点时的容差（DIP），避免像素取整把贴边算成翻转。
const popoverRiseEpsilon float32 = 1

// popoverRiseOffset 返回浮层出现时该移开的距离（DIP）：面板落在锚点下方时从下往上抬，
// 落点被翻到锚点上方时从上往下落，方向跟着落点走。
// 面板还没量出高度（打开后的首帧）时按「落在下方」处理；那一帧浮层完全透明，看不出来。
func popoverRiseOffset(panel, anchor ui.Element) float32 {
	pb := panel.Bounds()
	ab := anchor.Bounds()
	if pb.H == 0 || pb.Y+pb.H > ab.Y+popoverRiseEpsilon {
		return motionRiseOffset
	}
	return -motionRiseOffset
}

// appPopover 是 ui.Popover 的去外层样式版本：面板自身不做 Padding/Background/
// Border/Shadow，由 fn 内容中的那一层卡片承担全部外观，避免出现“黑框套卡片”。
// 调用方只需传入与原先一致的 c.Key(...) 作为锚点；onDismiss 可为 nil。
//
// 返回值在面板打开时非空，可用 Dismissed() 判断本轮是否因点外部或 Esc 被关闭。
// 注意：按钮自己的 OnClick 里若把开合状态取反，点按钮本身时会与 PopoverBase 写入的
// 关闭相互抵消（先关后开），需要 onDismiss 或“打开时不取反”的判断来避免。
func appPopover(c *ui.Context, anchor ui.Element, open *bool, onDismiss func(), fn func()) ui.Element {
	return anchoredPopover(c, anchoredPopoverProps{Anchor: anchor, Open: open, OnDismiss: onDismiss}, func(_ ui.Element) { fn() })
}

// appPopoverAbove 是 appPopover 的向上展开版本：面板的左下角贴在锚点左上角，
// 用于输入框上方的胶囊，避免浮层遮住输入框；空间不够时由框架翻转或夹紧位置。
// 除落点外与 appPopover 完全一致，外观仍由 fn 内容中的卡片承担。
func appPopoverAbove(c *ui.Context, anchor ui.Element, open *bool, onDismiss func(), fn func()) ui.Element {
	return anchoredPopover(c, anchoredPopoverProps{Anchor: anchor, Open: open, Align: AboveStart, OnDismiss: onDismiss}, func(_ ui.Element) { fn() })
}

// anchoredMenu 在 Overlay 里渲染锚定在 trigger 下方的菜单面板：与触发按钮左对齐，
// 宽度限定在 [minWidth, maxWidth] 之间，高度不超过 maxHeight（超出部分由调用方的
// Scroll 内部滚动），纵向放不下时由 AttachTo 翻到按钮上方，并夹在窗口可视区域内。
//
// 面板就是 PopoverBase 的面板本身：尺寸约束与锚点直接作用在可见卡片的容器上，
// 点外部或 Esc 关闭时写回 open 并调用 onDismiss（可为 nil）。
func anchoredMenu(c *ui.Context, key string, trigger ui.Element, open *bool, onDismiss func(), minWidth, maxWidth, maxHeight float32, children func()) {
	anchoredPopover(c, anchoredPopoverProps{Key: key, Anchor: trigger, Open: open, MinWidth: minWidth, MaxWidth: maxWidth, MaxHeight: maxHeight, OnDismiss: onDismiss}, func(panel ui.Element) {
		panel.Label(key)
		children()
	})
}

// popoverCard 给直接由菜单项拼成的弹出层内容套上唯一的一层卡片外观
// （圆角、表面底色、边框与阴影），取代原先 ui.Popover 面板自带的样式。
func popoverCard(c *ui.Context, column ui.Element) ui.Element {
	return popoverCardStyle(c, column)
}
