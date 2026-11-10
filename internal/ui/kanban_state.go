package mcodeui

import (
	"sync"

	"mcode/internal/config"
	"mcode/internal/git"
)

var (
	kanbanMu     sync.Mutex
	kanbanStates = make(map[*chatState]*kanbanState)

	kanbanModalMu     sync.Mutex
	kanbanModalStates = make(map[*chatState]*kanbanModalState)
)

// kanbanModalState 是「任务与看板」大弹窗的本地状态，按会话保存，不使用包级开关。
type kanbanModalState struct {
	open bool
	// projectMenuOpen 是看板头部（任务与看板窗口标题栏）项目选择器的展开状态。
	projectMenuOpen bool
	// pickerMenuOpen 是「新建任务」弹窗内项目下拉的展开状态，与头部的 projectMenuOpen 互相独立。
	pickerMenuOpen bool
}

func getKanbanModalState(s *chatState) *kanbanModalState {
	if s == nil {
		return nil
	}
	kanbanModalMu.Lock()
	defer kanbanModalMu.Unlock()
	st, ok := kanbanModalStates[s]
	if !ok {
		st = &kanbanModalState{}
		kanbanModalStates[s] = st
	}
	return st
}

// kanbanProjectMenuOpen 报告头部项目选择器是否展开。
func kanbanProjectMenuOpen(s *chatState) bool {
	st := getKanbanModalState(s)
	if st == nil {
		return false
	}
	kanbanModalMu.Lock()
	defer kanbanModalMu.Unlock()
	return st.projectMenuOpen
}

// setKanbanProjectMenuOpen 设置项目选择器的展开状态。
func setKanbanProjectMenuOpen(s *chatState, open bool) {
	st := getKanbanModalState(s)
	if st == nil {
		return
	}
	kanbanModalMu.Lock()
	st.projectMenuOpen = open
	kanbanModalMu.Unlock()
	s.invalidate()
}

// setKanbanPickerMenuOpen 设置「新建任务」弹窗内项目下拉的展开状态（与头部菜单独立）。
func setKanbanPickerMenuOpen(s *chatState, open bool) {
	st := getKanbanModalState(s)
	if st == nil {
		return
	}
	kanbanModalMu.Lock()
	st.pickerMenuOpen = open
	kanbanModalMu.Unlock()
	s.invalidate()
}

type kanbanState struct {
	projectID    string
	scopeID      string
	initialized  bool
	loading      bool
	err          string
	board        KanbanBoard
	conflict     bool
	watchStop    func()
	loadID       int
	watchID      int
	watchLoading bool
	watchPending bool
	loadingScope string
	taskBusy     map[string]bool
	// viewMode 保存任务的展示方式，destination 独立保存窗口目的。
	viewMode         string
	destination      string
	automationSearch string
	goalID           string
	goalMenuOpen     bool
	newMenuOpen      bool
	goalDetailOpen   bool
	// overview 为 true 时内容区显示「全部项目」概览。
	overview bool
	// selecting 为 true 时进入多选模式（本轮只做选中态视觉）。
	selecting       bool
	selectedTaskIDs map[string]bool
	// searchText 按标题与备注过滤任务，看板与列表视图共用。
	searchText string

	// 待办列底部快速添加草稿
	quickAddOpen bool
	quickAddText string

	// 进行中 / 卡死卡片的行为菜单与删除确认
	rescueMenuTaskID string
	deleteTaskID     string

	// 新建任务草稿
	showNewTask      bool
	newTaskTitle     string
	newTaskNotes     string
	newTaskProjectID string
	newTaskBusy      bool
	newTaskErr       string
	newTaskVersion   int

	// 新建目标草稿
	showNewGoal      bool
	newGoalPrompt    string
	newGoalProjectID string
	newGoalBusy      bool
	newGoalErr       string
	newGoalVersion   int
	quickAddBusy     bool

	// 目标重新规划草稿
	replanGoalID   string
	replanFeedback string

	// 目标规划审查弹窗
	planReviewGoalID     string
	planReviewReplanOpen bool
	planReviewFeedback   string
	planReviewSeen       map[string]bool

	// 目标产物弹窗
	outputsGoalID string

	// 任务移动菜单状态与审查反馈
	moveMenuTaskID string
	reviewTaskID   string
	reviewFeedback string

	// 详情浮窗状态
	detailTaskID        string
	detailThreadID      string
	detailRevision      int
	detailLoading       bool
	detailErr           string
	detailMessages      []Message
	detailDiff          *diffPanelState
	detailFiles         []git.FileDiff
	detailDiffErr       string
	detailDiffTruncated bool
	detailLoadID        int
	detailFeedback      string
	detailConflict      bool
}

func kanbanTaskBusy(ks *kanbanState, taskID string) bool {
	kanbanMu.Lock()
	defer kanbanMu.Unlock()
	return ks != nil && ks.taskBusy[taskID]
}

func beginKanbanTask(ks *kanbanState, taskID string) bool {
	kanbanMu.Lock()
	defer kanbanMu.Unlock()
	if ks.taskBusy[taskID] {
		return false
	}
	if ks.taskBusy == nil {
		ks.taskBusy = make(map[string]bool)
	}
	ks.taskBusy[taskID] = true
	return true
}

func endKanbanTask(ks *kanbanState, taskID string) {
	kanbanMu.Lock()
	delete(ks.taskBusy, taskID)
	kanbanMu.Unlock()
}

func getKanbanState(s *chatState) *kanbanState {
	if s == nil {
		return nil
	}
	kanbanMu.Lock()
	defer kanbanMu.Unlock()
	st, ok := kanbanStates[s]
	if !ok {
		st = &kanbanState{}
		kanbanStates[s] = st
	}
	return st
}

// kanbanConfiguredDefaultView 返回配置中的默认视图，未知值回退到看板。
func kanbanConfiguredDefaultView(s *chatState) string {
	if st := getChatSettings(s); st != nil {
		st.mu.Lock()
		view := st.cfg.Workbench.KanbanDefaultView
		st.mu.Unlock()
		if view == config.KanbanViewList {
			return config.KanbanViewList
		}
	}
	return config.KanbanViewKanban
}

// kanbanModalOpen 报告指定会话的看板弹窗是否打开（由工作区视图 tasks 驱动）。
func kanbanModalOpen(s *chatState) bool {
	if s == nil {
		return false
	}
	ws := getWorkspaceState(s)
	if ws == nil {
		return false
	}
	workspaceMu.Lock()
	view := ws.view
	workspaceMu.Unlock()
	return view == "tasks"
}
