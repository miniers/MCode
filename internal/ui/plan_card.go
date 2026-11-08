package mcodeui

import (
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// 计划正文直接在卡片里按 Markdown 渲染：待批卡片停靠在输入框上方，正文放进限高滚动区就地读完；
// 历史卡片在时间线里，展开后整篇铺开。

const (
	// planBodyHeightRatio 是待批计划正文滚动区相对窗口高度的上限比例。
	planBodyHeightRatio float32 = 0.45
	// planBodyMinHeight 是窗口很矮时滚动区高度上限的下限。
	planBodyMinHeight float32 = 160
)

// renderPlanMarkdown 渲染计划正文；maxHeight > 0 时放进限高滚动区，否则整篇铺开。
func renderPlanMarkdown(c *ui.Context, s *chatState, key, plan string, maxHeight float32) {
	if plan == "" {
		return
	}
	if maxHeight <= 0 {
		markdown.View(c.Key(key+"-md"), plan, &s.mdStaticOpts)
		return
	}
	ui.Scroll(c.Key(key)).FillWidth().MaxHeight(maxHeight).Children(func() {
		markdown.View(c.Key(key+"-md"), plan, &s.mdStaticOpts)
	})
}
