package mcodeui

import (
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const (
	gitPRWindowHeight    = 720
	gitPRWindowMinWidth  = 720
	gitPRWindowMinHeight = 480
	gitPRWindowStateKey  = "git-pr-window"
)

var (
	gitPRWindowMu       sync.Mutex
	gitPRWindow         *mygo.Window
	gitPRWindowOwner    *chatState
	gitPRWindowServices ui.Services
	gitPRWindowClosing  bool
)

// gitPRWindowOpener 和 gitPRWindowCloser 是 openGitPRWindow / closeGitPRWindow 的出口，
// 测试里替换成桩以避免真的创建或关闭系统窗口（无主循环时 RunOnMain 会阻塞）。
// 这里不写初始值（直接赋真实函数会构成包级初始化循环），nil 表示使用真实实现。
var (
	gitPRWindowOpener func(s *chatState)
	gitPRWindowCloser func()
)

// openGitPRWindow 打开（或聚焦复用）全局唯一的 PR 独立窗口。
func openGitPRWindow(s *chatState) {
	if s == nil {
		return
	}
	if gitPRWindowOpener != nil {
		gitPRWindowOpener(s)
		return
	}
	openGitPRWindowImpl(s)
}

func openGitPRWindowImpl(s *chatState) {
	if s == nil {
		return
	}
	mygo.RunOnMain(func() {
		gitPRWindowMu.Lock()
		win := gitPRWindow
		if win != nil && !win.IsDestroyed() {
			prevOwner := gitPRWindowOwner
			if prevOwner != s {
				gitPRWindowOwner = s
			}
			services := gitPRWindowServices
			gitPRWindowMu.Unlock()
			if prevOwner != nil && prevOwner != s {
				prevOwner.resetGitPRDialogState()
			}
			services.Invalidate()
			if win.IsMinimized() {
				win.Restore()
			}
			win.Show()
			win.Focus()
			return
		}
		gitPRWindowMu.Unlock()

		var created *mygo.Window
		created = mygo.NewWindow(mygo.WindowOptions{
			Title:                "Pull Request",
			Width:                int(gitPRDialogWidth),
			Height:               gitPRWindowHeight,
			MinWidth:             gitPRWindowMinWidth,
			MinHeight:            gitPRWindowMinHeight,
			TitleBarStyle:        mygo.TitleBarHiddenInset,
			TrafficLightPosition: &mygo.Point{X: int(beeWindowTrafficX), Y: int(beeWindowTrafficY)},
			TitleBarHeight:       hiddenTitleBarHeight(titleBarGOOS, int(beeWindowStripHeight)),
			StateKey:             gitPRWindowStateKey,
			Content: ui.View(func(c *ui.Context) {
				setGitPRWindowServices(created, c.Services())
				renderGitPRWindow(c, currentGitPRWindowOwner(s))
			}),
		})

		gitPRWindowMu.Lock()
		gitPRWindow = created
		gitPRWindowOwner = s
		gitPRWindowClosing = false
		gitPRWindowMu.Unlock()

		created.OnClosed(func() {
			handleGitPRWindowClosed(created, s)
		})
	})
}

// closeGitPRWindow 关闭当前 PR 独立窗口（若存在）。
func closeGitPRWindow() {
	gitPRWindowMu.Lock()
	closing := gitPRWindowClosing
	gitPRWindowMu.Unlock()
	if closing {
		return
	}
	if gitPRWindowCloser != nil {
		gitPRWindowCloser()
		return
	}
	closeGitPRWindowImpl()
}

func closeGitPRWindowImpl() {
	mygo.RunOnMain(func() {
		gitPRWindowMu.Lock()
		win := gitPRWindow
		if win == nil || win.IsDestroyed() || gitPRWindowClosing {
			gitPRWindowMu.Unlock()
			return
		}
		// 先清空包级引用并置关闭中标志，避免 win.Close() 触发 OnClosed 时重入。
		gitPRWindowClosing = true
		gitPRWindow = nil
		gitPRWindowOwner = nil
		gitPRWindowServices = ui.Services{}
		gitPRWindowMu.Unlock()

		win.Close()

		gitPRWindowMu.Lock()
		gitPRWindowClosing = false
		gitPRWindowMu.Unlock()
	})
}

func handleGitPRWindowClosed(win *mygo.Window, fallbackOwner *chatState) {
	gitPRWindowMu.Lock()
	if win == nil || gitPRWindow != win || gitPRWindowClosing {
		gitPRWindowMu.Unlock()
		return
	}
	owner := gitPRWindowOwner
	if owner == nil {
		owner = fallbackOwner
	}
	// 先清空窗口引用并置关闭中标志，再回调 closeGitPRDialog 清状态，防止反向再调用 Close。
	gitPRWindowClosing = true
	gitPRWindow = nil
	gitPRWindowOwner = nil
	gitPRWindowServices = ui.Services{}
	gitPRWindowMu.Unlock()

	if owner != nil {
		owner.closeGitPRDialog()
	}

	gitPRWindowMu.Lock()
	gitPRWindowClosing = false
	gitPRWindowMu.Unlock()
}

func setGitPRWindowServices(win *mygo.Window, services ui.Services) {
	gitPRWindowMu.Lock()
	if gitPRWindow == win {
		gitPRWindowServices = services
	}
	gitPRWindowMu.Unlock()
}

func currentGitPRWindowOwner(fallback *chatState) *chatState {
	gitPRWindowMu.Lock()
	owner := gitPRWindowOwner
	gitPRWindowMu.Unlock()
	if owner != nil {
		return owner
	}
	return fallback
}

func invalidateGitPRWindow(s *chatState) {
	if s == nil {
		return
	}
	gitPRWindowMu.Lock()
	var services ui.Services
	if gitPRWindow != nil && gitPRWindowOwner == s {
		services = gitPRWindowServices
	}
	gitPRWindowMu.Unlock()
	services.Invalidate()
}
