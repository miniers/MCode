package mcodeui

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// 模型选择弹层里的两处过渡都复用框架的逐帧插值（Element.AnimateTo）：目标值变化时
// 框架按 Animation 的时长与曲线逐帧推进，值到达目标后自动停止请求重绘，
// 因此这里只给目标值、时长与曲线，不自己维护起始时间或逐帧 tick。
const (
	// modelPickerTabSlideDuration 是主导/执行分段指示块滑动的时长（切换选中态的短促过渡）。
	modelPickerTabSlideDuration = 180 * time.Millisecond
	// modelPickerPageDuration 是二级页（模型列表、更多模型）滑入淡入的时长。
	modelPickerPageDuration = 200 * time.Millisecond
	// modelPickerPageSlide 是二级页换页时内容起始的横向偏移（pt）。
	modelPickerPageSlide = float32(10)
)

// modelPickerTabSlideKey 与 modelPickerPageProgressKey 分别是分段指示块位置、二级页换页
// 进度在元素动画通道里的键名；同一元素上的不同动画必须用不同的键。
const (
	// modelPickerTabSlideKey 是分段指示块位置的动画键。
	modelPickerTabSlideKey = "side-indicator"
	// modelPickerPageProgressKey 是二级页换页进度的动画键。
	modelPickerPageProgressKey = "page-progress"
)

// modelPickerPageDepth 返回某一侧当前渲染的页面深度：
// 0 为推理强度卡片、1 为模型列表、2 为列表里的“更多模型”分组。
func modelPickerPageDepth(p ModelPickerProps, state *ModelPickerState, side ModelPickerSide) int {
	if !state.listOpen(p.ScopeKey, side) {
		return 0
	}
	if isModelPanelMoreOpen(p.ScopeKey, modelPickerSideKey(side)) {
		return 2
	}
	return 1
}

// modelPickerPageTransition 给二级页内容加上换页时的横向滑入与淡入。
// depth 是当前页的深度：深度变化的那一帧把进度重置为 0 并记下方向（变深视为进入、
// 变浅视为返回），进度随后按 modelPickerPageDuration ease-out 插值到 1；
// 进度 0 时内容朝来向偏移 modelPickerPageSlide 且完全透明，进度 1 时回到原位并全不透明。
func modelPickerPageTransition(c *ui.Context, el ui.Element, state *ModelPickerState, depth int) {
	if state == nil {
		return
	}
	if depth != state.pageDepth {
		if depth < state.pageDepth {
			state.pageDir = -1
		} else {
			state.pageDir = 1
		}
		state.pageDepth = depth
		// 换页那一帧把进度拉回起点，让下面的 AnimateTo 从 0 走到 1 播出入场。
		el.Jump(modelPickerPageProgressKey, 0)
	}
	dir := state.pageDir
	if dir == 0 {
		// 首次渲染时没有换页方向：按进入处理，且进度通道初值即为 1，不播放动画。
		dir = 1
	}
	progress := el.AnimateTo(modelPickerPageProgressKey, 1, ui.Animation{
		Duration: animateDuration(c, modelPickerPageDuration),
		Ease:     ui.EaseOut,
	})
	el.Translate(dir*(1-progress)*modelPickerPageSlide, 0).Opacity(progress)
}

// modelPickerTabSlide 驱动主导/执行分段指示块的位置：sel 为 0（主导段）或 1（执行段），
// 返回当前帧插值出的位置。切换分段时框架按 modelPickerTabSlideDuration ease-out 逐帧
// 滑动，动画结束后停止请求重绘。
func modelPickerTabSlide(c *ui.Context, el ui.Element, sel float32) float32 {
	return el.AnimateTo(modelPickerTabSlideKey, sel, ui.Animation{
		Duration: animateDuration(c, modelPickerTabSlideDuration),
		Ease:     ui.EaseOut,
	})
}
