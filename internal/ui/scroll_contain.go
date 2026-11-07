package mcodeui

import "github.com/egoist/mygo/ui"

// containScroll 让 el 这层滚动容器在滚到内容边界后不再把滚轮交给外层，
// 等价于 CSS 的 overscroll-behavior: contain。
//
// mygo 会先把 InputScroll 交给命中链上的输入处理器，处理器返回 true 就终止传递，
// 滚动容器也都不再动（ui/input.go 的 engine.scroll）。这里只在「已经到头」时吃掉
// 滚轮，其余情况返回 false，让容器自己滚动；非滚动事件一律不管，子元素的点击、
// 勾选与悬停照常工作。s 为 nil 时原样返回，调用方无法跟踪滚动位置。
func containScroll(el ui.Element, s *ui.ScrollState) ui.Element {
	if s == nil {
		return el
	}
	el = el.TrackScroll(s)
	return el.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind != ui.InputScroll {
			return false
		}
		// 按住 Shift 且没有横向分量时，mygo 会把滚轮转成横向滚动，交给容器处理。
		if ev.Mods&ui.Shift != 0 && ev.DX == 0 {
			return false
		}
		// 内容没有溢出时没有可滚的边界，照常交给外层。
		if ev.DY == 0 || s.MaxY <= 0 {
			return false
		}
		// 正的 DY 表示向下滚（看后面的内容）：已经到底就拦住，返回负值则相反。
		if ev.DY > 0 && s.Y >= s.MaxY-0.5 {
			return true
		}
		if ev.DY < 0 && s.Y <= 0.5 {
			return true
		}
		return false
	})
}
