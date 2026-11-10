package mcodeui

import (
	"context"

	"github.com/egoist/mygo/ui"
)

func buildTasksRoot(client Client, opts ...Option) (*chatState, func(*ui.Context)) {
	s := newChatState(client)
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s, tasksRootView(s)
}

func tasksRootView(s *chatState) func(*ui.Context) {
	ks := getKanbanState(s)
	ks.persistTasksDetailWidth = true
	ks.detailWidth = loadTasksDetailWidth()
	started := false
	return func(c *ui.Context) {
		applyAppTheme(c)
		if !started {
			started = true
			s.setUIServices(c.Services())
			if s.client != nil {
				go func() {
					projects, err := s.client.Projects(context.Background())
					s.updateWorkspace(func() {
						if err != nil {
							s.setToast("获取项目失败: " + err.Error())
							return
						}
						s.mu.Lock()
						if !s.closed {
							s.projects = projects
						}
						s.mu.Unlock()
					})
				}()
				go func() {
					if providers, ok := s.reloadProviderCatalog(); ok {
						s.initNewThreadProvider(providers)
						s.invalidate()
					}
				}()
			}
		}
		s.applyWorkspaceUpdates()
		if c.Shortcut(ui.Cmd, ui.KeyF) {
			kanbanKeyboardSearchFocus(s, true)
		}
		if c.Shortcut(0, ui.KeyEscape) {
			consumeKanbanEscape(s)
		}
		ui.Column(c.Key("root-shell")).Fill().Children(func() {
			s.mu.Lock()
			if s.toastMessage != "" {
				c.Toast(s.toastMessage)
				s.toastMessage = ""
			}
			s.mu.Unlock()
			renderKanbanWindow(c, s)
			renderKanbanNewTaskSheet(c, s)
			renderKanbanNewGoalSheet(c, s)
		})
	}
}
