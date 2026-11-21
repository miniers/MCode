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
			if s.tasksWindowView != "" && !kanbanModalOpen(s) {
				s.openWorkspace("tasks")
			}
			if s.client != nil {
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), settingsRPCTimeout)
					projects, err := s.client.Projects(ctx)
					cancel()
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
				if _, ok := s.attentionClient(); ok {
					s.refreshAttentionInbox()
					s.ensureAttentionWatch()
				}
				if _, ok := s.client.(stewardDecisionListClient); ok {
					refreshStewardOpenDecisions(s)
				}
				kanbanMu.Lock()
				scopeID := ks.scopeID
				kanbanMu.Unlock()
				as := getAutomationState(s)
				automationMu.Lock()
				needAuto := as != nil && !as.initialized && !as.loading
				if needAuto {
					as.projectID = scopeID
				}
				automationMu.Unlock()
				if needAuto {
					s.startAutomationWatch(scopeID)
					s.loadAutomations(scopeID)
				}
			}
		}
		s.applyWorkspaceUpdates()
		if c.Shortcut(ui.Cmd, ui.KeyF) {
			kanbanKeyboardSearchFocus(s, true)
		}
		if c.Shortcut(0, ui.KeyEscape) {
			kanbanMu.Lock()
			automations := ks.destination == "automations"
			hasModal := ks.newMenuOpen || ks.showNewTask || ks.showNewGoal || ks.quickAddOpen
			hasOtherDetail := ks.goalDetailOpen || ks.detailTaskID != "" || ks.planReviewGoalID != "" || ks.outputsGoalID != ""
			hasDecision := ks.detailDecisionID != ""
			kanbanMu.Unlock()
			if automations && handleAutomationEscape(s) {
				// handled by automation escape chain
			} else if !automations && !hasModal && !hasOtherDetail && hasDecision {
				if kanbanKeyboardClearSelection(s) {
					s.invalidate()
				} else {
					kanbanMu.Lock()
					ks.detailDecisionID = ""
					kanbanMu.Unlock()
					s.invalidate()
				}
			} else {
				consumeKanbanEscape(s)
			}
		}
		ui.Column(c.Key("root-shell")).Fill().Children(func() {
			s.mu.Lock()
			if s.toastMessage != "" {
				c.Toast(s.toastMessage)
				s.toastMessage = ""
			}
			s.mu.Unlock()
			renderTasksShell(c, s)
			renderKanbanNewTaskSheet(c, s)
			renderKanbanNewGoalSheet(c, s)
		})
	}
}
