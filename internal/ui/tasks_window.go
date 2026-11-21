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

type taskCenterTarget struct {
	Kind      string
	ID        string
	ProjectID string
	Title     string
}

func (t taskCenterTarget) destinationView() string {
	switch t.Kind {
	case "automation", "automation_run":
		return "automations"
	case "decision":
		return "attention"
	default:
		return "tasks"
	}
}

// WithOpenTasksWindow 注入打开独立任务窗口的函数。
func WithOpenTasksWindow(fn func(view string)) Option {
	return func(s *chatState) {
		s.openTasksWindowFn = fn
	}
}

// WithOpenTasksWindowTarget 注入按统一深链打开任务窗口的函数。
func WithOpenTasksWindowTarget(fn func(taskCenterTarget)) Option {
	return func(s *chatState) {
		s.openTasksWindowTargetFn = fn
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
		if view == "automations" || view == "attention" {
			s.tasksWindowView = view
		}
		s.setTasksWindowView(view)
	}
}

// WithTasksWindowTarget 将根视图设为独立任务窗口并定位到指定深链目标。
func WithTasksWindowTarget(target taskCenterTarget) Option {
	return func(s *chatState) {
		view := target.destinationView()
		s.tasksWindowView = "tasks"
		if view == "automations" || view == "attention" {
			s.tasksWindowView = view
		}
		s.applyTasksWindowTarget(target)
	}
}

func (s *chatState) openTasksWindow(view string) {
	if s.openTasksWindowFn != nil {
		s.openTasksWindowFn(view)
		return
	}
	s.setTasksWindowView(view)
}

func (s *chatState) openTasksWindowTarget(target taskCenterTarget) {
	if s == nil {
		return
	}
	if target.Kind == "thread" && target.ID != "" {
		s.revealThread(target.ID)
		return
	}
	if s.openTasksWindowTargetFn != nil {
		s.openTasksWindowTargetFn(target)
		return
	}
	if s.openTasksWindowFn != nil {
		s.openTasksWindowFn(target.destinationView())
		return
	}
	s.applyTasksWindowTarget(target)
}

func (s *chatState) applyTasksWindowTarget(target taskCenterTarget) {
	if s == nil {
		return
	}
	dest := target.destinationView()
	s.setTasksWindowView(dest)

	ks := getKanbanState(s)
	if ks != nil {
		kanbanMu.Lock()
		scopeChanged := target.ProjectID != "" && ks.scopeID != target.ProjectID
		kanbanMu.Unlock()

		if target.ProjectID != "" && s.tasksWindowView == "" {
			ws := getWorkspaceState(s)
			if ws != nil {
				workspaceMu.Lock()
				ws.projectID = target.ProjectID
				workspaceMu.Unlock()
			}
		}

		if scopeChanged {
			s.setKanbanScope(target.ProjectID, false)
			if dest == "automations" {
				kanbanMu.Lock()
				ks.projectID = target.ProjectID
				ks.scopeID = target.ProjectID
				ks.overview = false
				kanbanMu.Unlock()
			}
		}
	}

	if dest == "automations" {
		if as := getAutomationState(s); as != nil {
			scopePID := target.ProjectID
			if scopePID == "" && ks != nil {
				kanbanMu.Lock()
				scopePID = ks.scopeID
				kanbanMu.Unlock()
			}
			automationMu.Lock()
			needReset := target.ProjectID != "" && as.projectID != scopePID
			if needReset {
				as.stopSchedulePreview()
				*as = automationState{
					loadID:           as.loadID,
					runsLoadID:       as.runsLoadID,
					saveID:           as.saveID,
					previewEpoch:     as.previewEpoch,
					runRequest:       as.runRequest + 1,
					selectionVersion: as.selectionVersion + 1,
					editSession:      as.editSession + 1,
					watchStop:        as.watchStop,
					runningIDs:       make(map[string]bool),
					busyIDs:          make(map[string]bool),
					projectID:        scopePID,
					filter:           as.filter,
					unreadOnly:       as.unreadOnly,
				}
			}
			needLoad := needReset || (!as.initialized && len(as.autos) == 0)
			automationMu.Unlock()
			if needLoad && s.client != nil {
				s.startAutomationWatch(scopePID)
				s.loadAutomations(scopePID)
			}
		}
	}

	s.tasksTargetMu.Lock()
	if target.ID != "" || target.Title != "" {
		cp := target
		s.pendingTasksTarget = &cp
	} else {
		s.pendingTasksTarget = nil
	}
	s.tasksTargetMu.Unlock()

	s.consumePendingTasksTarget()
}

func (s *chatState) consumePendingTasksTarget() {
	if s == nil {
		return
	}
	s.tasksTargetMu.Lock()
	if s.pendingTasksTarget == nil {
		s.tasksTargetMu.Unlock()
		return
	}
	target := *s.pendingTasksTarget
	s.tasksTargetMu.Unlock()

	clearPending := func() {
		s.tasksTargetMu.Lock()
		if s.pendingTasksTarget != nil && *s.pendingTasksTarget == target {
			s.pendingTasksTarget = nil
		}
		s.tasksTargetMu.Unlock()
	}

	switch target.Kind {
	case "task":
		if target.ID == "" {
			clearPending()
			return
		}
		ks := getKanbanState(s)
		if ks == nil {
			return
		}
		var matched *KanbanTask
		kanbanMu.Lock()
		for i := range ks.board.Tasks {
			if ks.board.Tasks[i].ID == target.ID {
				t := ks.board.Tasks[i]
				matched = &t
				break
			}
		}
		kanbanMu.Unlock()
		if matched == nil {
			return
		}
		clearPending()
		kanbanMu.Lock()
		ks.detailDecisionID = ""
		kanbanMu.Unlock()
		s.openTaskDetail(*matched)
		return
	case "goal":
		if target.ID == "" {
			clearPending()
			return
		}
		ks := getKanbanState(s)
		if ks == nil {
			return
		}
		matched := false
		kanbanMu.Lock()
		for _, g := range ks.board.Goals {
			if g.ID == target.ID {
				ks.goalID = g.ID
				ks.goalDetailOpen = true
				ks.detailTaskID = ""
				ks.detailDecisionID = ""
				matched = true
				break
			}
		}
		kanbanMu.Unlock()
		if !matched {
			return
		}
		clearPending()
		s.invalidate()
		return
	case "decision":
		if target.ID == "" {
			clearPending()
			return
		}
		ks := getKanbanState(s)
		if ks == nil {
			return
		}
		kanbanMu.Lock()
		ks.detailDecisionID = target.ID
		ks.detailTaskID = ""
		ks.goalDetailOpen = false
		kanbanMu.Unlock()
		clearPending()
		s.invalidate()
		return
	case "automation":
		if target.ID == "" {
			clearPending()
			return
		}
		as := getAutomationState(s)
		if as == nil {
			return
		}
		var matched *Automation
		automationMu.Lock()
		for i := range as.autos {
			if as.autos[i].ID == target.ID {
				a := as.autos[i]
				matched = &a
				break
			}
		}
		automationMu.Unlock()
		if matched == nil {
			return
		}
		clearPending()
		as.selectAutomation(*matched, s)
		return
	case "automation_run":
		if target.ID == "" && target.Title == "" {
			clearPending()
			return
		}
		as := getAutomationState(s)
		if as == nil {
			return
		}
		var matched *Automation
		automationMu.Lock()
		if target.ID != "" {
			for i := range as.autos {
				if as.autos[i].ID == target.ID {
					a := as.autos[i]
					matched = &a
					break
				}
			}
		}
		if matched == nil && target.Title != "" {
			preferredPID := target.ProjectID
			if preferredPID == "" {
				preferredPID = as.projectID
			}
			for i := range as.autos {
				if as.autos[i].Name == target.Title {
					if preferredPID != "" && as.autos[i].ProjectID == preferredPID {
						a := as.autos[i]
						matched = &a
						break
					}
					if matched == nil {
						a := as.autos[i]
						matched = &a
					}
				}
			}
		}
		if matched == nil {
			automationMu.Unlock()
			return
		}
		as.selectionVersion++
		as.fillAutomation(*matched)
		as.detailTab = 0
		as.narrowDetail = true
		as.runs = nil
		as.runRead = nil
		as.runArchived = nil
		if matched.ID != target.ID && target.ID != "" {
			as.openRunID = target.ID
		} else {
			as.openRunID = ""
		}
		as.runsErr = ""
		automationMu.Unlock()

		clearPending()
		s.loadAutomationRuns(matched.ID)
		s.invalidate()
		return
	default:
		clearPending()
		return
	}
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
	if view != "automations" && view != "attention" {
		view = "tasks"
	}
	kanbanMu.Lock()
	leaving := ks.destination == "automations" && view != "automations"
	kanbanMu.Unlock()
	if leaving && !prepareAutomationLeave(s, func() { s.setTasksWindowView(view); s.invalidate() }) {
		return
	}
	kanbanMu.Lock()
	ks.destination = view
	kanbanMu.Unlock()
	if s.tasksWindowView == "" {
		s.openWorkspace(view)
	}
}

// OpenTasksWindow 创建或复用全局唯一的任务与看板窗口。
func OpenTasksWindow(client Client, view string, opts ...Option) *mygo.Window {
	return openOrFocusTasksWindow(
		client,
		func(st *chatState) {
			st.setTasksWindowView(view)
		},
		WithTasksWindowMode(view),
		opts...,
	)
}

// OpenTasksWindowTarget 创建或复用全局唯一的任务窗口并定位到指定深链目标。
func OpenTasksWindowTarget(client Client, target taskCenterTarget, opts ...Option) *mygo.Window {
	if target.Kind == "thread" && target.ID != "" {
		return OpenThreadWindow(client, target.ID, "")
	}
	return openOrFocusTasksWindow(
		client,
		func(st *chatState) {
			st.applyTasksWindowTarget(target)
		},
		WithTasksWindowTarget(target),
		opts...,
	)
}

func openOrFocusTasksWindow(client Client, onReuse func(*chatState), initOpt Option, opts ...Option) *mygo.Window {
	var win *mygo.Window

	mygo.RunOnMain(func() {
		tasksWindowMu.Lock()
		defer tasksWindowMu.Unlock()

		if tasksWindow != nil && !tasksWindow.IsDestroyed() {
			win = tasksWindow
			onReuse(tasksWindowState)
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
			initOpt,
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
		closingConfirmed := false
		win.OnClose(func(e *mygo.CloseEvent) {
			if closingConfirmed {
				return
			}
			if !prepareAutomationLeave(state, func() { closingConfirmed = true; win.Close() }) {
				e.PreventDefault()
			}
		})
		win.OnClosed(func() {
			kanbanBatchCancel(state)
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
