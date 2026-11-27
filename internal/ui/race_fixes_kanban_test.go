package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/backup"
)

func TestRaceAutomationShellList(t *testing.T) {
	s := &chatState{
		projects: []Project{{ID: "p1", Name: "P1"}, {ID: "p2", Name: "P2"}},
	}
	as := getAutomationState(s)
	automationMu.Lock()
	as.projectID = ""
	as.initialized = true
	as.autos = []Automation{testAutomation("a1", "p1")}
	as.unread = map[string]int{"a1": 1}
	automationMu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) {
		renderAutomationScopedBody(c, s, automationWorkspaceProps{ScopeID: ""})
	}, 800, 600)

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			default:
			}
			automationMu.Lock()
			if i%2 == 0 {
				as.autos = []Automation{testAutomation("a1", "p1"), testAutomation("a2", "p2")}
			} else {
				as.autos = []Automation{testAutomation("a1", "p1")}
			}
			as.unread = map[string]int{"a1": i % 3}
			automationMu.Unlock()

			s.mu.Lock()
			s.projects = []Project{
				{ID: "p1", Name: fmt.Sprintf("P1-%d", i)},
				{ID: "p2", Name: "P2"},
			}
			s.mu.Unlock()
		}
	}()

	for range 80 {
		tt.Frame()
	}

	close(done)
	wg.Wait()
}

func TestRaceKanbanModalProjects(t *testing.T) {
	s := newChatState(nil)
	s.mu.Lock()
	s.projects = []Project{{ID: "p1", Name: "P1"}}
	s.mu.Unlock()
	s.tasksWindowView = "automations"
	s.openWorkspace("tasks")

	ks := getKanbanState(s)
	kanbanMu.Lock()
	ks.initialized = true
	ks.destination = "automations"
	ks.detailWidth = 420
	kanbanMu.Unlock()

	as := getAutomationState(s)
	automationMu.Lock()
	as.projectID = ""
	as.initialized = true
	as.chooseProjectOpen = true
	automationMu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) {
		renderTasksShell(c, s)
	}, 960, 600)

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			default:
			}
			s.mu.Lock()
			if i%2 == 0 {
				s.projects = []Project{{ID: "p1", Name: fmt.Sprintf("P1-%d", i)}, {ID: "p2", Name: "P2"}}
			} else {
				s.projects = []Project{{ID: "p1", Name: fmt.Sprintf("P1-%d", i)}}
			}
			s.mu.Unlock()

			kanbanMu.Lock()
			ks.detailWidth = 380 + float32(i%40)
			kanbanMu.Unlock()
		}
	}()

	for range 80 {
		tt.Frame()
	}

	close(done)
	wg.Wait()
}

func TestRaceBackupPasswordDialog(t *testing.T) {
	preserveBackupUI(t)
	oldStage := backupStage
	t.Cleanup(func() {
		backupStage = oldStage
	})
	backupStage = func(context.Context, backup.StageOptions) (backup.Result, error) {
		return backup.Result{}, errors.New("test stop")
	}

	st := NewSettingsState(t.TempDir(), nil, nil)
	backupUI.mu.Lock()
	backupUI.mode = "restore"
	backupUI.source = "/tmp/backup-0.mcodebackup"
	backupUI.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) {
		renderBackupPasswordDialog(c, st)
	}, 800, 600)

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			default:
			}
			backupUI.mu.Lock()
			backupUI.source = fmt.Sprintf("/tmp/backup-%d.mcodebackup", i)
			backupUI.mode = "restore"
			backupUI.mu.Unlock()
		}
	}()

	for i := range 60 {
		backupPasswordDraft.password = "secret"
		tt.Frame()
		if i%3 == 0 {
			tt.Key(0, ui.KeyEnter)
			tt.Frame()
		}
	}

	close(done)
	wg.Wait()
	waitBackupIdle(t)
}
