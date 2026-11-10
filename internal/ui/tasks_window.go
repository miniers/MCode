package mcodeui

import (
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

var (
	tasksWindowMu    sync.Mutex
	tasksWindow      *mygo.Window
	tasksWindowState *chatState
)

// WithOpenTasksWindow 注入打开独立任务窗口的函数。
func WithOpenTasksWindow(fn func(view string)) Option {
	return func(s *chatState) {
		s.openTasksWindowFn = fn
	}
}

// WithRevealThreadInMain 注入将会话切换转交给主窗口的函数。
func WithRevealThreadInMain(fn func(threadID string)) Option {
	return func(s *chatState) {
		s.revealThreadInMainFn = fn
	}
}

func (s *chatState) revealThread(threadID string) {
	if s.tasksWindowView != "" && s.revealThreadInMainFn != nil {
		s.revealThreadInMainFn(threadID)
		return
	}
	s.openWorkspace("chat")
	s.selectThread(threadID)
}

// WithTasksWindowMode 将根视图设为独立任务窗口。
func WithTasksWindowMode(view string) Option {
	return func(s *chatState) {
		s.tasksWindowView = "tasks"
		if view == "automations" {
			s.tasksWindowView = view
		}
		s.setTasksWindowView(view)
	}
}

func (s *chatState) openTasksWindow(view string) {
	if s.openTasksWindowFn != nil {
		s.openTasksWindowFn(view)
		return
	}
	s.openWorkspace(view)
}

func (s *chatState) closeTasksWindow() {
	if s.window != nil {
		if win := s.window(); win != nil {
			win.Close()
			return
		}
	}
	s.closeWorkspace()
}

func (s *chatState) setTasksWindowView(view string) {
	ks := getKanbanState(s)
	kanbanMu.Lock()
	ks.destination = "tasks"
	if view == "automations" {
		ks.destination = "automations"
	}
	kanbanMu.Unlock()
}

// OpenTasksWindow 创建或复用全局唯一的任务与看板窗口。
func OpenTasksWindow(client Client, view string, opts ...Option) *mygo.Window {
	var win *mygo.Window

	mygo.RunOnMain(func() {
		tasksWindowMu.Lock()
		defer tasksWindowMu.Unlock()

		if tasksWindow != nil && !tasksWindow.IsDestroyed() {
			win = tasksWindow
			tasksWindowState.setTasksWindowView(view)
			tasksWindowState.invalidate()
			if win.IsMinimized() {
				win.Restore()
			}
			win.Show()
			win.Focus()
			return
		}

		allOpts := make([]Option, 0, len(opts)+2)
		allOpts = append(allOpts, opts...)
		allOpts = append(allOpts,
			WithTasksWindowMode(view),
			WithWindow(func() *mygo.Window { return win }),
		)
		state, content := buildTasksRoot(client, allOpts...)
		win = mygo.NewWindow(mygo.WindowOptions{
			Title:                "任务",
			Width:                1140,
			Height:               720,
			MinWidth:             840,
			MinHeight:            520,
			TitleBarStyle:        mygo.TitleBarHiddenInset,
			TrafficLightPosition: &mygo.Point{X: 18, Y: 16},
			TitleBarHeight:       hiddenTitleBarHeight(titleBarGOOS, int(HeaderHeight)),
			StateKey:             "tasks-window",
			Content:              ui.View(content),
		})
		tasksWindow = win
		tasksWindowState = state
		win.OnClosed(func() {
			tasksWindowMu.Lock()
			if tasksWindow == win {
				tasksWindow = nil
				tasksWindowState = nil
			}
			tasksWindowMu.Unlock()
			state.close()
		})
	})

	return win
}
