package mcodeui

import (
	"sync"

	"github.com/egoist/mygo/ui"
)

var (
	workspaceMu     sync.Mutex
	workspaceStates = make(map[*chatState]*workspaceState)
)

// workspaceState 维护工作区面板的视图与项目归属状态。
type workspaceState struct {
	view      string // "chat", "tasks", "automations"
	projectID string // 当前工作区关联的项目 ID
	updates   []func()
}

// 表单绑定只能由 UI 线程修改，后台结果在下一次构建前应用。
func (s *chatState) updateWorkspace(fn func()) {
	ws := getWorkspaceState(s)
	workspaceMu.Lock()
	ws.updates = append(ws.updates, fn)
	workspaceMu.Unlock()
	s.invalidate()
}

func (s *chatState) applyWorkspaceUpdates() {
	ws := getWorkspaceState(s)
	workspaceMu.Lock()
	updates := ws.updates
	ws.updates = nil
	workspaceMu.Unlock()
	for _, fn := range updates {
		fn()
	}
}

func getWorkspaceState(s *chatState) *workspaceState {
	if s == nil {
		return nil
	}
	workspaceMu.Lock()
	defer workspaceMu.Unlock()
	st, ok := workspaceStates[s]
	if !ok {
		st = &workspaceState{
			view: "chat",
		}
		workspaceStates[s] = st
	}
	return st
}

// openWorkspace 打开指定工作区视图（"tasks" 或 "automations"）。
// 若 projectID 为空，则优先取当前选中会话所属的项目，再不行就取 s.projects 的第一个。
func (s *chatState) openWorkspace(view string) {
	if s == nil {
		return
	}
	ws := getWorkspaceState(s)
	if ws == nil {
		return
	}

	if view != "tasks" {
		stopKanbanWatch(s)
	}
	if view != "automations" {
		stopAutomationWatch(s)
	}

	s.mu.Lock()
	selectedTid := s.selectedThreadID
	threads := make([]Thread, len(s.threads))
	copy(threads, s.threads)
	projects := make([]Project, len(s.projects))
	copy(projects, s.projects)
	s.mu.Unlock()

	workspaceMu.Lock()
	defer workspaceMu.Unlock()

	ws.view = view
	if ws.projectID == "" {
		var pid string
		if selectedTid != "" {
			for _, th := range threads {
				if th.ID == selectedTid {
					pid = th.ProjectID
					break
				}
			}
		}
		if pid == "" && len(projects) > 0 {
			pid = projects[0].ID
		}
		if pid != "" {
			ws.projectID = pid
		}
	}

	s.invalidate()
}

// closeWorkspace 将当前工作区视图恢复为聊天视图 ("chat")。
func (s *chatState) closeWorkspace() {
	if s == nil {
		return
	}
	ws := getWorkspaceState(s)
	if ws == nil {
		return
	}
	workspaceMu.Lock()
	ws.view = "chat"
	workspaceMu.Unlock()
	stopKanbanWatch(s)
	stopAutomationWatch(s)
	s.invalidate()
}

// currentWorkspaceProjectID 获取当前工作区生效的项目 ID。
func (s *chatState) currentWorkspaceProjectID() string {
	if s == nil {
		return ""
	}
	ws := getWorkspaceState(s)
	if ws == nil {
		return ""
	}
	workspaceMu.Lock()
	pid := ws.projectID
	workspaceMu.Unlock()

	if pid != "" {
		return pid
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.selectedThreadID != "" {
		for _, th := range s.threads {
			if th.ID == s.selectedThreadID && th.ProjectID != "" {
				workspaceMu.Lock()
				ws.projectID = th.ProjectID
				workspaceMu.Unlock()
				return th.ProjectID
			}
		}
	}
	if len(s.projects) > 0 {
		workspaceMu.Lock()
		ws.projectID = s.projects[0].ID
		workspaceMu.Unlock()
		return s.projects[0].ID
	}
	return ""
}

// setWorkspaceProject 设置当前工作区聚焦的项目 ID。
func (s *chatState) setWorkspaceProject(projectID string) {
	if s == nil {
		return
	}
	ws := getWorkspaceState(s)
	if ws == nil {
		return
	}
	workspaceMu.Lock()
	ws.projectID = projectID
	workspaceMu.Unlock()
	s.invalidate()
}

func renderWorkspaceProjectPicker(c *ui.Context, s *chatState, projectID string) {
	s.mu.Lock()
	projects := append([]Project(nil), s.projects...)
	s.mu.Unlock()
	ui.Row(c.Key("workspace-projects")).Wrap().Gap(Space2).AlignItems(ui.Center).Children(func() {
		for _, p := range projects {
			selected := p.ID == projectID
			btn := ui.ButtonBase(c.Key("workspace-project-"+p.ID)).
				Label("workspace-project-"+p.ID).Padding(Space1, Space3).Radius(RowCornerRadius).Center()
			if selected {
				btn.Background(ThemeSelected(c))
			} else if btn.Hovered() {
				btn.Background(ThemeHover(c))
			}
			btn.OnClick(func() { s.setWorkspaceProject(p.ID) })
			btn.Children(func() {
				color := ThemeSecondaryText(c)
				if selected {
					color = ThemePrimaryText(c)
				}
				ui.Text(c, p.Name).FontSize(TypeFootnote).FontWeight(500).TextColor(color)
			})
		}
	})
}
