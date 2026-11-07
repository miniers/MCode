package mcodeui

import "github.com/egoist/mygo"

// ForegroundThreadReporter 由应用层实现：界面把「用户当前正在看的会话」告诉应用层，
// 供总管之类的后台逻辑判断该会话正被人看着、暂不打扰。
// 界面层用类型断言 client.(ForegroundThreadReporter) 取用：Client 未实现该接口时
// 上报直接跳过，因此两侧可以分批落地，界面侧不需要跟着改。
type ForegroundThreadReporter interface {
	// SetForegroundThread 记录当前前台会话。threadID 为空表示用户当前没有在看任何会话
	// （窗口失焦、窗口隐藏，或窗口里没有选中会话）。
	SetForegroundThread(threadID string)
}

// foregroundWindowFocused 报告窗口此刻是否真在前台：窗口可见、获得焦点，且应用本身
// 没有被隐藏（⌘H）。判定口径与 cmd/mcode/native.go 里的 isForeground 保持一致。
// 抽成变量是为了测试里替换掉真实窗口探测。
var foregroundWindowFocused = func(win *mygo.Window) bool {
	if win == nil || win.IsDestroyed() || !win.IsVisible() || !win.IsFocused() {
		return false
	}
	if app := mygo.App; app != nil && app.IsReady() && app.IsHidden() {
		return false
	}
	return true
}

// foregroundReportDispatch 把跨层上报放到渲染之外执行，默认新开 goroutine，
// 免得应用层实现慢一点就拖住一帧；测试替换成同步执行，好在调用后立刻断言。
var foregroundReportDispatch = func(fn func()) { go fn() }

// reportForegroundThread 把当前前台会话上报给应用层：窗口在前台且选中了会话时取该
// 会话 ID，否则上报空串。每帧调用一次，只有值变化时才真正跨层上报，避免每帧都打扰应用层。
func (s *chatState) reportForegroundThread() {
	if s == nil {
		return
	}
	reporter, ok := s.client.(ForegroundThreadReporter)
	if !ok {
		return
	}
	s.mu.Lock()
	closed := s.closed
	selected := s.selectedThreadID
	last := s.reportedForegroundThread
	s.mu.Unlock()
	if closed {
		return
	}
	next := ""
	if foregroundWindowFocused(s.currentWindow()) {
		next = selected
	}
	if next == last {
		return
	}
	// 先记账再上报：多窗口各自持有一个 chatState，各自只在自己这一路上去重。
	s.mu.Lock()
	s.reportedForegroundThread = next
	s.mu.Unlock()
	foregroundReportDispatch(func() { reporter.SetForegroundThread(next) })
}

// currentWindow 返回该状态所属的窗口；窗口回调未注入时（测试、子代理嵌入会话）返回 nil，
// 由 foregroundWindowFocused 判定为不在前台。
func (s *chatState) currentWindow() *mygo.Window {
	if s == nil || s.window == nil {
		return nil
	}
	return s.window()
}
