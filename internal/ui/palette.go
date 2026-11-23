package mcodeui

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

var (
	paletteMu     sync.Mutex
	paletteStates = make(map[*chatState]*paletteState)

	// paletteVisibleBuildCount 记录 paletteVisibleItems 实际重算次数，供缓存回归测试观测。
	paletteVisibleBuildCount atomic.Int64
)

const (
	paletteWidth         float32 = 560
	paletteWindowInset   float32 = 40
	paletteCardInset     float32 = 80
	paletteListMaxHeight float32 = 360
)

// paletteState 维护单个聊天会话视图下的命令面板状态。
type paletteState struct {
	open            bool
	query           string
	prevQuery       string
	selected        int
	scrollSelection bool

	// 消息正文搜索结果：msgHits 是 msgQuery 的命中，msgPending 是已经排上防抖的查询串，
	// msgSeq 是请求序号（只有最新一次的异步结果会被采纳），msgTimer 是防抖定时器，
	// msgSearching 表示防抖或搜索请求仍在途。
	msgErr       string
	msgHits      []MessageSearchHit
	msgQuery     string
	msgPending   string
	msgSeq       uint64
	msgTimer     *time.Timer
	msgSearching bool

	// 候选列表缓存：只在面板打开、query 变化或消息搜索结果到达时重算。
	cachedItems []paletteItem
	cachedQuery string
	cacheValid  bool

	// 任务、目标与自动化内存缓存（受 paletteMu 保护，避免与后台 goroutine 竞态）。
	cachedTasks       []KanbanTask
	cachedGoals       []KanbanGoal
	cachedAutomations []Automation
	cachedDecisions   []StewardDecisionItem
	taskCacheSeq      uint64
	taskCacheVer      uint64
	cachedTaskVer     uint64
	taskCacheOpen     bool
}

// getPaletteState 获取或初始化当前 chatState 的命令面板状态。
func getPaletteState(s *chatState) *paletteState {
	if s == nil {
		return nil
	}
	paletteMu.Lock()
	defer paletteMu.Unlock()
	st, ok := paletteStates[s]
	if !ok {
		st = &paletteState{}
		paletteStates[s] = st
	}
	return st
}

// paletteItem 表示命令面板中的单个匹配候选条目。
// keywords 是只参与搜索、不显示的关键词（别名、别称），用来让「总管」「话题」这类说法也能命中。
type paletteItem struct {
	id          string
	kind        string
	title       string
	subtitle    string
	statusText  string
	statusColor func(c *ui.Context) ui.Color
	keywords    string
	shortcutID  string
	run         func(s *chatState)
}

// sidePanePaletteItems 由 side_pane.go 的注册表生成右侧分栏面板的命令面板开关项。
// 循环变量先拷贝到局部变量，避免所有 run 闭包捕获同一个值。
func sidePanePaletteItems() []paletteItem {
	items := make([]paletteItem, 0, len(sidePanes))
	for i := range sidePanes {
		p := &sidePanes[i]
		toggle := p.toggle
		title := "显示/隐藏" + p.title
		if p.id == sidePaneGitID {
			title = p.paletteTitle
		}
		items = append(items, paletteItem{
			id:         p.paletteID,
			shortcutID: p.actionID,
			kind:       "command",
			title:      title,
			run: func(cs *chatState) {
				if toggle != nil {
					toggle(cs)
				}
			},
		})
	}
	items = append(items, paletteItem{id: "cmd-toggle-inspector", shortcutID: "toggle-inspector", kind: "command", title: "显示/隐藏右栏", run: toggleInspector})
	return items
}

func readMemoryKanbanSnapshot(s *chatState) ([]KanbanTask, []KanbanGoal) {
	if s == nil {
		return nil, nil
	}
	kanbanMu.Lock()
	defer kanbanMu.Unlock()
	ks := kanbanStates[s]
	if ks == nil {
		return nil, nil
	}
	var tasks []KanbanTask
	if len(ks.board.Tasks) > 0 {
		tasks = make([]KanbanTask, len(ks.board.Tasks))
		copy(tasks, ks.board.Tasks)
	}
	var goals []KanbanGoal
	if len(ks.board.Goals) > 0 {
		goals = make([]KanbanGoal, len(ks.board.Goals))
		copy(goals, ks.board.Goals)
	}
	return tasks, goals
}

func readMemoryAutomationSnapshot(s *chatState) []Automation {
	if s == nil {
		return nil
	}
	automationMu.Lock()
	defer automationMu.Unlock()
	as := automationStates[s]
	if as == nil || len(as.autos) == 0 {
		return nil
	}
	autos := make([]Automation, len(as.autos))
	copy(autos, as.autos)
	return autos
}

func readMemoryDecisionSnapshot(s *chatState) []StewardDecisionItem {
	if s == nil {
		return nil
	}
	stewardOpenMu.Lock()
	st := stewardOpenStates[s]
	stewardOpenMu.Unlock()
	if st == nil {
		return nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.openDecisions) == 0 {
		return nil
	}
	decisions := make([]StewardDecisionItem, len(st.openDecisions))
	copy(decisions, st.openDecisions)
	return decisions
}

func paletteSlicesEqual[T any](a, b []T) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

func paletteTasksState(s *chatState) *chatState {
	tasksWindowMu.Lock()
	ts := tasksWindowState
	tasksWindowMu.Unlock()
	if ts == nil {
		return s
	}
	return ts
}

func paletteWithTasksWindow(s *chatState, view string, fn func(ts *chatState)) {
	s.openTasksWindow(view)
	ts := paletteTasksState(s)
	fn(ts)
	ts.invalidate()
}

// buildPaletteItems 收集全部候选项目：命令优先，随后是所有会话与项目。
func buildPaletteItems(s *chatState) []paletteItem {
	if s == nil {
		return nil
	}

	items := make([]paletteItem, 0, 6)

	for _, command := range []struct {
		id, title, action string
	}{
		{"cmd-git-commit", "Git：提交", "commit"},
		{"cmd-git-push", "Git：推送", "push"},
		{"cmd-git-pull", "Git：拉取", "pull"},
		{"cmd-git-fetch", "Git：抓取", "fetch"},
		{"cmd-git-switch-branch", "Git：切换分支", "branch"},
		{"cmd-git-history", "Git：历史", "history"},
	} {
		command := command
		items = append(items, paletteItem{id: command.id, kind: "command", title: command.title, run: func(cs *chatState) {
			if command.action == "commit" {
				openReviewUncommitted(cs, "")
				return
			}
			inspectorSelect(cs, sidePaneGitID, false)
			ps := getGitPaneState(cs)
			if ps == nil {
				return
			}
			switch command.action {
			case "history":
				cs.invalidate()
			case "branch":
				ps.branchOpen = true
				cs.invalidate()
			default:
				ps.run(command.action)
			}
		}})
	}

	// 1. 命令优先
	items = append(items,
		paletteItem{
			id:         "cmd-new-thread",
			shortcutID: "new-thread",
			kind:       "command",
			title:      "新建会话",
			run: func(cs *chatState) {
				cs.closeWorkspace()
				cs.createNewThread()
			},
		},
		paletteItem{
			id:         "cmd-add-project",
			shortcutID: "add-project",
			kind:       "command",
			title:      "添加项目",
			run: func(cs *chatState) {
				cs.promptAddProject()
			},
		},
		paletteItem{
			id:         "cmd-open-settings",
			shortcutID: "open-settings",
			kind:       "command",
			title:      "打开设置",
			run: func(cs *chatState) {
				openSettingsWindow(cs, "")
			},
		},
	)
	for _, page := range settingsPages() {
		if page.id == "about" {
			continue
		}
		pageID := page.id
		items = append(items, paletteItem{
			id:       "cmd-settings-" + pageID,
			kind:     "command",
			title:    "设置：" + page.title,
			keywords: "设置 settings " + page.title + " " + strings.Join(page.keywords, " "),
			run: func(cs *chatState) {
				openSettingsWindow(cs, pageID)
			},
		})
	}
	items = append(items,
		paletteItem{
			id:    "cmd-check-update",
			kind:  "command",
			title: "检查更新",
			run:   openAboutAndCheckUpdate,
		},
		paletteItem{
			id:    "cmd-changelog",
			kind:  "command",
			title: "更新日志",
			run: func(cs *chatState) {
				if getChatSettings(cs) == nil {
					return
				}
				if versions := changelogVersions(); len(versions) > 0 {
					aboutChangelogExpanded[versions[0].Version] = true
				}
				openSettingsWindow(cs, "about")
				if st := currentSettingsWindowState(); st != nil {
					st.focusSettingsItem(settingsItemHit{Item: settingsItem{ID: "row-about-changelog", Page: "about"}})
				}
			},
		},
		paletteItem{
			id:    "cmd-open-tasks",
			kind:  "command",
			title: "打开任务",
			run: func(cs *chatState) {
				cs.openTasksWindow("tasks")
			},
		},
		paletteItem{
			id:    "cmd-open-automations",
			kind:  "command",
			title: "打开自动化",
			run: func(cs *chatState) {
				cs.openTasksWindow("automations")
			},
		},
		paletteItem{
			id:         "cmd-toggle-terminal",
			shortcutID: "toggle-terminal",
			kind:       "command",
			title:      "切换终端",
			run: func(cs *chatState) {
				toggleTerminal(cs)
			},
		},
	)
	items = append(items,
		paletteItem{id: "cmd-toggle-sidebar", shortcutID: "toggle-sidebar", kind: "command", title: "显示/隐藏侧边栏", run: toggleSidebarPersisted},
		paletteItem{id: "cmd-toggle-activity", shortcutID: "toggle-activity", kind: "command", title: "动态视图", run: func(cs *chatState) { cs.toggleActivityView() }},
		paletteItem{id: "cmd-show-shortcuts", shortcutID: "show-shortcuts", kind: "command", title: "显示快捷键", run: func(cs *chatState) { cs.toggleShortcutHelp() }},
	)
	items = append(items,
		paletteItem{id: "cmd-new-task", kind: "command", title: "新建任务", run: func(cs *chatState) {
			paletteWithTasksWindow(cs, "tasks", kanbanNewTaskAction)
		}},
		paletteItem{id: "cmd-new-goal", kind: "command", title: "新建目标", run: func(cs *chatState) {
			paletteWithTasksWindow(cs, "tasks", kanbanNewGoalAction)
		}},
		paletteItem{id: "cmd-toggle-kanban-view", kind: "command", title: "切换看板/列表", run: func(cs *chatState) {
			paletteWithTasksWindow(cs, "tasks", func(ts *chatState) {
				ks := getKanbanState(ts)
				kanbanMu.Lock()
				if ks.viewMode == config.KanbanViewList {
					ks.viewMode = config.KanbanViewKanban
				} else {
					ks.viewMode = config.KanbanViewList
				}
				kanbanMu.Unlock()
			})
		}},
		paletteItem{id: "cmd-tasks-attention", kind: "command", title: "待你处理", run: func(cs *chatState) {
			cs.openTasksWindowTarget(taskCenterTarget{Kind: "attention"})
		}},
	)
	if automationCanCreate(s) {
		items = append(items, paletteItem{id: "cmd-new-automation", kind: "command", title: "新建自动化", run: func(cs *chatState) {
			paletteWithTasksWindow(cs, "automations", func(ts *chatState) {
				ks := getKanbanState(ts)
				kanbanMu.Lock()
				scopeID := ks.scopeID
				kanbanMu.Unlock()
				if scopeID == "" {
					scopeID = cs.currentWorkspaceProjectID()
				}
				requestNewAutomation(ts, scopeID, nil)
			})
		}})
	}
	ts := paletteTasksState(s)
	var focused KanbanTask
	kanbanKeyboardMu.Lock()
	if kb := kanbanKeyboardStates[ts]; kb != nil && kb.focusID != "" {
		for _, task := range kb.tasks {
			if task.ID == kb.focusID {
				focused = task
				break
			}
		}
	}
	kanbanKeyboardMu.Unlock()
	if focused.ID != "" {
		items = append(items, paletteItem{id: "cmd-task-detail", kind: "command", title: "打开任务详情", run: func(cs *chatState) {
			paletteWithTasksWindow(cs, "tasks", func(ts *chatState) { ts.openTaskDetail(focused) })
		}})
		actions := kanbanActions(focused, kanbanTaskBusy(getKanbanState(ts), focused.ID))
		for _, opt := range actions.menu {
			if actions.targets[opt.status] == "review" {
				continue
			}
			target := opt.status
			items = append(items, paletteItem{id: "cmd-task-move-" + target, kind: "command", title: "移到" + opt.label, run: func(cs *chatState) {
				paletteWithTasksWindow(cs, "tasks", func(ts *chatState) { ts.performKanbanTarget(focused, target) })
			}})
		}
	}

	// 总管入口：与侧栏、项目右键菜单共用同一组函数。
	items = append(items, stewardPaletteItems(s)...)

	// 右侧分栏面板（改动、文件管理、网页预览）的开关项由 side_pane.go 的注册表生成。
	items = append(items, sidePanePaletteItems()...)
	items = append(items, paletteItem{
		id:         "cmd-toggle-diff",
		shortcutID: "toggle-diff",
		kind:       "command",
		title:      "Git：本轮改动",
		run:        func(cs *chatState) { cs.toggleDiffPanel() },
	})

	s.mu.Lock()
	selectedTID := s.selectedThreadID
	var selectedThread Thread
	for _, th := range s.threads {
		if th.ID == selectedTID {
			selectedThread = th
			break
		}
	}
	s.mu.Unlock()
	if selectedTID != "" {
		pinTitle, dormantTitle := "置顶会话", "休眠会话"
		if selectedThread.Pinned {
			pinTitle = "取消置顶会话"
		}
		if selectedThread.Dormant {
			dormantTitle = "重新激活会话"
		}
		items = append(items,
			paletteItem{id: "cmd-pin-thread", kind: "command", title: pinTitle, run: func(cs *chatState) { cs.setThreadPinned(selectedTID, !selectedThread.Pinned) }},
			paletteItem{id: "cmd-archive-thread", shortcutID: "archive-thread", kind: "command", title: "归档会话", run: func(cs *chatState) { cs.archiveThreadWithUndo(selectedThread.ProjectID, selectedTID) }},
			paletteItem{id: "cmd-dormant-thread", shortcutID: "dormant-thread", kind: "command", title: dormantTitle, run: func(cs *chatState) {
				if selectedThread.Dormant {
					cs.setThreadDormant(selectedTID, false)
				} else {
					cs.markDormantWithUndo(selectedThread.ProjectID, selectedTID)
				}
			}},
			paletteItem{id: "cmd-rename-thread", kind: "command", title: "重命名会话", run: func(cs *chatState) { cs.openRenameModal(selectedTID, selectedThread.Title) }},
		)
	}

	if s.openThreadWindow != nil && selectedTID != "" {
		items = append(items, paletteItem{
			id:    "cmd-open-thread-window",
			kind:  "command",
			title: "在新窗口打开当前会话",
			run: func(cs *chatState) {
				cs.mu.Lock()
				curTID := cs.selectedThreadID
				cs.mu.Unlock()
				if cs.openThreadWindow != nil && curTID != "" {
					cs.openThreadWindow(curTID)
				}
			},
		})
	}

	// 会话转发：选中会话时才出现，与右键菜单、⇧⌘D 共用 forwardSelectedThread。
	if selectedTID != "" {
		items = append(items, paletteItem{
			id:         "cmd-forward-thread",
			shortcutID: "forward-thread",
			kind:       "command",
			title:      "转发到新会话",
			keywords:   "转发 继续 新会话 forward continue",
			run: func(cs *chatState) {
				cs.forwardSelectedThread()
			},
		})
	}

	// 2. 全部会话与 3. 全部项目，在 s.mu 保护下读取数据
	s.mu.Lock()
	projects := make([]Project, len(s.projects))
	copy(projects, s.projects)

	threads := withoutProvisional(s.threads)

	projNames := make(map[string]string, len(projects))
	for _, p := range projects {
		projNames[p.ID] = p.Name
	}
	threadProjects := make(map[string]string, len(s.threads))
	for _, th := range s.threads {
		if th.ID != "" && th.ProjectID != "" {
			threadProjects[th.ID] = th.ProjectID
		}
	}
	s.mu.Unlock()

	for _, th := range threads {
		tID := th.ID
		title := th.Title
		if title == "" {
			title = "未命名会话"
		}
		subtitle := projNames[th.ProjectID]
		items = append(items, paletteItem{
			id:       "thread-" + tID,
			kind:     "thread",
			title:    title,
			subtitle: subtitle,
			run: func(cs *chatState) {
				cs.closeWorkspace()
				cs.selectThread(tID)
			},
		})
	}

	// 2.5 未答决策、任务、目标与自动化（只读内存快照，严禁 IO）
	var (
		tasks     []KanbanTask
		goals     []KanbanGoal
		autos     []Automation
		decisions []StewardDecisionItem
	)
	if p := getPaletteState(s); p != nil {
		paletteMu.Lock()
		if len(p.cachedTasks) > 0 {
			tasks = make([]KanbanTask, len(p.cachedTasks))
			copy(tasks, p.cachedTasks)
		}
		if len(p.cachedGoals) > 0 {
			goals = make([]KanbanGoal, len(p.cachedGoals))
			copy(goals, p.cachedGoals)
		}
		if len(p.cachedAutomations) > 0 {
			autos = make([]Automation, len(p.cachedAutomations))
			copy(autos, p.cachedAutomations)
		}
		if len(p.cachedDecisions) > 0 {
			decisions = make([]StewardDecisionItem, len(p.cachedDecisions))
			copy(decisions, p.cachedDecisions)
		}
		paletteMu.Unlock()
	}
	if len(tasks) == 0 || len(goals) == 0 {
		memTasks, memGoals := readMemoryKanbanSnapshot(s)
		if len(tasks) == 0 {
			tasks = memTasks
		}
		if len(goals) == 0 {
			goals = memGoals
		}
	}
	if len(autos) == 0 {
		autos = readMemoryAutomationSnapshot(s)
	}
	if len(decisions) == 0 {
		decisions = readMemoryDecisionSnapshot(s)
	}

	taskProjects := make(map[string]string, len(tasks))
	for _, t := range tasks {
		if t.ID != "" && t.ProjectID != "" {
			taskProjects[t.ID] = t.ProjectID
		}
	}

	for _, d := range decisions {
		if d.ID == "" {
			continue
		}
		if d.Status != "" && d.Status != "open" {
			continue
		}
		decision := d
		title := strings.TrimSpace(decision.Question)
		if title == "" {
			title = decision.ID
		}
		pid := ""
		if decision.TaskID != "" {
			pid = taskProjects[decision.TaskID]
		}
		if pid == "" && decision.ThreadID != "" {
			pid = threadProjects[decision.ThreadID]
		}
		projName := projNames[pid]
		subtitle := projName
		if subtitle == "" {
			subtitle = decision.TaskID
		}
		kwParts := []string{
			"总管 决策 待你处理 decision steward",
			decision.ID,
			decision.TaskID,
			projName,
		}
		items = append(items, paletteItem{
			id:         "decision-" + decision.ID,
			kind:       "decision",
			title:      title,
			subtitle:   subtitle,
			statusText: "待你处理",
			statusColor: func(c *ui.Context) ui.Color {
				return ThemeWarning(c)
			},
			keywords: strings.Join(kwParts, " "),
			run: func(cs *chatState) {
				cs.openTasksWindowTarget(taskCenterTarget{
					Kind:      "decision",
					ID:        decision.ID,
					ProjectID: pid,
					Title:     decision.Question,
				})
			},
		})
	}

	for _, t := range tasks {
		task := t
		title := task.Title
		if title == "" {
			title = task.ID
		}
		projName := projNames[task.ProjectID]
		badge := taskBadgeLabel(task)
		kwParts := []string{
			taskStatusLabel(task.Status),
			kanbanColumnLabel(kanbanDisplayColumn(task)),
			badge,
			projName,
			"任务 kanban task",
		}
		if isStewardTaskOrigin(task.Origin) {
			kwParts = append(kwParts, "总管")
		}
		items = append(items, paletteItem{
			id:         "task-" + task.ID,
			kind:       "task",
			title:      title,
			subtitle:   projName,
			statusText: badge,
			statusColor: func(c *ui.Context) ui.Color {
				return kanbanStatusColor(c, task.Status)
			},
			keywords: strings.Join(kwParts, " "),
			run: func(cs *chatState) {
				cs.openTasksWindowTarget(taskCenterTarget{
					Kind:      "task",
					ID:        task.ID,
					ProjectID: task.ProjectID,
					Title:     task.Title,
				})
			},
		})
	}

	for _, g := range goals {
		goal := g
		title := goal.Title
		if title == "" {
			title = goal.Prompt
		}
		if title == "" {
			title = goal.ID
		}
		projName := projNames[goal.ProjectID]
		statusText := goalStatusLabel(goal.Status)
		kwParts := []string{
			statusText,
			projName,
			"目标 goal",
		}
		items = append(items, paletteItem{
			id:         "goal-" + goal.ID,
			kind:       "goal",
			title:      title,
			subtitle:   projName,
			statusText: statusText,
			statusColor: func(c *ui.Context) ui.Color {
				return goalStatusBadgeColor(c, goal.Status)
			},
			keywords: strings.Join(kwParts, " "),
			run: func(cs *chatState) {
				cs.openTasksWindowTarget(taskCenterTarget{
					Kind:      "goal",
					ID:        goal.ID,
					ProjectID: goal.ProjectID,
					Title:     title,
				})
			},
		})
	}

	for _, a := range autos {
		auto := a
		title := auto.Name
		if title == "" {
			title = auto.ID
		}
		projName := projNames[auto.ProjectID]
		statusText, statusKind := automationCardStatus(auto, false)
		runLabel, _ := automationRunStatusDict(auto.LastRunStatus)
		kwParts := []string{
			statusText,
			runLabel,
			projName,
			"自动化 automation",
		}
		if isStewardAutomationOrigin(auto.Origin) {
			kwParts = append(kwParts, "总管")
		}
		items = append(items, paletteItem{
			id:         "automation-" + auto.ID,
			kind:       "automation",
			title:      title,
			subtitle:   projName,
			statusText: statusText,
			statusColor: func(c *ui.Context) ui.Color {
				return automationCardStatusColor(c, statusKind)
			},
			keywords: strings.Join(kwParts, " "),
			run: func(cs *chatState) {
				cs.openTasksWindowTarget(taskCenterTarget{
					Kind:      "automation",
					ID:        auto.ID,
					ProjectID: auto.ProjectID,
					Title:     title,
				})
			},
		})
	}

	projects = withoutProjects(projects, s.stewardProjectID(), s.chatsProjectID())
	for _, p := range projects {
		pID := p.ID
		items = append(items, paletteItem{
			id:       "project-" + pID,
			kind:     "project",
			title:    p.Name,
			subtitle: p.Path,
			run: func(cs *chatState) {
				cs.mu.Lock()
				projThreads := cs.threadsByProj[pID]
				var firstThreadID string
				firstThreadID = firstVisibleThreadID(projThreads)
				cs.mu.Unlock()

				if firstThreadID != "" {
					cs.closeWorkspace()
					cs.selectThread(firstThreadID)
				} else {
					cs.closeWorkspace()
					cs.createThread(pID)
				}
			},
		})
	}

	return items
}

// stewardPaletteItems 生成总管会话入口候选条目。
func stewardPaletteItems(s *chatState) []paletteItem {
	if s == nil {
		return nil
	}
	if _, ok := s.stewardClient(); !ok {
		return nil
	}

	nickname, _ := s.stewardIdentity()
	name := stewardName(nickname)
	return []paletteItem{{
		id:       "cmd-open-steward",
		kind:     "command",
		title:    "打开" + name,
		keywords: "总管 默默 steward 会话",
		run: func(cs *chatState) {
			cs.openStewardThread()
		},
	}}
}

func matchSubsequence(target []rune, pattern []rune) bool {
	if len(pattern) == 0 {
		return true
	}
	pi := 0
	for _, r := range target {
		if r == pattern[pi] {
			pi++
			if pi == len(pattern) {
				return true
			}
		}
	}
	return false
}

// filterPaletteItems 根据输入过滤命令面板候选条目。
// 前缀匹配得 3 分，子串匹配得 2 分，字符子序列（模糊）匹配得 1 分，无匹配丢弃。
// 稳定按分数降序排序，相同分数保留原有相对顺序，最多返回 50 条。
func filterPaletteItems(items []paletteItem, query string) []paletteItem {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		if len(items) > 50 {
			return items[:50]
		}
		return items
	}

	type scoredItem struct {
		item  paletteItem
		score int
	}

	patternRunes := []rune(q)
	var matched []scoredItem

	for _, it := range items {
		target := strings.ToLower(it.title + " " + it.subtitle + " " + it.keywords)
		score := 0
		if strings.HasPrefix(target, q) {
			score = 3
		} else if strings.Contains(target, q) {
			score = 2
		} else if matchSubsequence([]rune(target), patternRunes) {
			score = 1
		}

		if score > 0 {
			matched = append(matched, scoredItem{item: it, score: score})
		}
	}

	sort.SliceStable(matched, func(i, j int) bool {
		return matched[i].score > matched[j].score
	})

	if len(matched) > 50 {
		matched = matched[:50]
	}

	result := make([]paletteItem, len(matched))
	for i, sm := range matched {
		result[i] = sm.item
	}
	return result
}

// paletteVisibleItems 把过滤后的候选条目与消息正文搜索结果合成一份列表。
// 渲染与键盘上下键/Enter 都基于这份列表，保证选中下标与画面一致。
func paletteVisibleItems(s *chatState, p *paletteState, query string) []paletteItem {
	paletteVisibleBuildCount.Add(1)
	items := filterPaletteItems(buildPaletteItems(s), query)
	msgItems := paletteMessageItems(p, query)
	if len(msgItems) == 0 {
		return items
	}
	merged := make([]paletteItem, 0, len(items)+len(msgItems))
	merged = append(merged, items...)
	merged = append(merged, msgItems...)
	return merged
}

// paletteCachedVisibleItems 返回缓存的可见条目，仅在缓存失效、任务缓存更新或 query 变化时重算。
func paletteCachedVisibleItems(s *chatState, p *paletteState, query string) []paletteItem {
	if p == nil {
		return nil
	}
	paletteMu.Lock()
	ver := p.taskCacheVer
	paletteMu.Unlock()
	if !p.cacheValid || p.cachedQuery != query || p.cachedTaskVer != ver {
		p.cachedItems = paletteVisibleItems(s, p, query)
		p.cachedQuery = query
		p.cachedTaskVer = ver
		p.cacheValid = true
	}
	return p.cachedItems
}

// move 移动选中的条目索引，并在 [0, n-1] 范围内 clamp。
func (p *paletteState) move(delta, n int) {
	p.scrollSelection = true
	if n <= 0 {
		p.selected = 0
		return
	}
	p.selected += delta
	if p.selected < 0 {
		p.selected = 0
	} else if p.selected >= n {
		p.selected = n - 1
	}
}

// openPalette 打开命令面板，并重置查询与选中项；同步预热并异步刷新任务/目标/自动化缓存。
func openPalette(s *chatState) {
	p := getPaletteState(s)
	if p == nil {
		return
	}
	p.open = true
	p.query = ""
	p.prevQuery = ""
	p.selected = 0
	p.scrollSelection = true
	p.cachedItems = nil
	p.cachedQuery = ""
	p.cacheValid = false
	clearMessageSearch(p)

	var (
		preTasks     []KanbanTask
		preGoals     []KanbanGoal
		preAutos     []Automation
		preDecisions []StewardDecisionItem
	)
	if s != nil {
		tasksWindowMu.Lock()
		ts := tasksWindowState
		tasksWindowMu.Unlock()

		preTasks, preGoals = readMemoryKanbanSnapshot(s)
		if len(preTasks) == 0 && len(preGoals) == 0 && ts != nil && ts != s {
			preTasks, preGoals = readMemoryKanbanSnapshot(ts)
		}

		preAutos = readMemoryAutomationSnapshot(s)
		if len(preAutos) == 0 && ts != nil && ts != s {
			preAutos = readMemoryAutomationSnapshot(ts)
		}

		preDecisions = readMemoryDecisionSnapshot(s)
		if len(preDecisions) == 0 && ts != nil && ts != s {
			preDecisions = readMemoryDecisionSnapshot(ts)
		}
	}

	paletteMu.Lock()
	if len(preTasks) > 0 {
		p.cachedTasks = preTasks
	}
	if len(preGoals) > 0 {
		p.cachedGoals = preGoals
	}
	if len(preAutos) > 0 {
		p.cachedAutomations = preAutos
	}
	if len(preDecisions) > 0 {
		p.cachedDecisions = preDecisions
	}
	p.taskCacheOpen = true
	p.taskCacheSeq++
	seq := p.taskCacheSeq
	paletteMu.Unlock()

	if s != nil && s.client != nil {
		client := s.client
		decClient, hasDecClient := client.(stewardDecisionListClient)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			var (
				board     KanbanBoard
				boardErr  error
				autos     []Automation
				autoErr   error
				decisions []StewardDecisionItem
				decErr    error
				wg        sync.WaitGroup
			)
			wg.Add(2)
			go func() {
				defer wg.Done()
				board, boardErr = client.KanbanBoard(ctx, "")
			}()
			go func() {
				defer wg.Done()
				autos, autoErr = client.Automations(ctx, "")
			}()
			if hasDecClient {
				wg.Add(1)
				go func() {
					defer wg.Done()
					decisions, decErr = decClient.StewardOpenDecisions(ctx)
				}()
			}
			wg.Wait()

			paletteMu.Lock()
			if !p.taskCacheOpen || p.taskCacheSeq != seq {
				paletteMu.Unlock()
				return
			}
			changed := false
			if boardErr == nil {
				if !paletteSlicesEqual(p.cachedTasks, board.Tasks) {
					p.cachedTasks = append([]KanbanTask(nil), board.Tasks...)
					changed = true
				}
				if !paletteSlicesEqual(p.cachedGoals, board.Goals) {
					p.cachedGoals = append([]KanbanGoal(nil), board.Goals...)
					changed = true
				}
			}
			if autoErr == nil {
				if !paletteSlicesEqual(p.cachedAutomations, autos) {
					p.cachedAutomations = append([]Automation(nil), autos...)
					changed = true
				}
			}
			if hasDecClient && decErr == nil {
				if !paletteSlicesEqual(p.cachedDecisions, decisions) {
					p.cachedDecisions = append([]StewardDecisionItem(nil), decisions...)
					changed = true
				}
			}
			if changed {
				p.taskCacheVer++
			}
			paletteMu.Unlock()

			if changed {
				s.invalidate()
			}
		}()
	}

	if s != nil {
		s.invalidate()
	}
}

// closePalette 关闭命令面板。
func closePalette(s *chatState) {
	p := getPaletteState(s)
	if p == nil {
		return
	}
	p.open = false
	p.cachedItems = nil
	p.cachedQuery = ""
	p.cacheValid = false
	paletteMu.Lock()
	p.taskCacheOpen = false
	p.taskCacheSeq++
	paletteMu.Unlock()
	clearMessageSearch(p)
	if s != nil {
		s.invalidate()
	}
}

// togglePalette 切换命令面板的打开/关闭状态。
func togglePalette(s *chatState) {
	p := getPaletteState(s)
	if p == nil {
		return
	}
	if p.open {
		closePalette(s)
	} else {
		openPalette(s)
	}
}

// executePaletteSelection 执行当前选中的条目并关闭面板。
func executePaletteSelection(s *chatState, items []paletteItem) {
	p := getPaletteState(s)
	if p == nil {
		return
	}
	var target *paletteItem
	if p.selected >= 0 && p.selected < len(items) {
		target = &items[p.selected]
	}
	closePalette(s)
	if target != nil && target.run != nil {
		target.run(s)
	}
}

// renderCommandPalette 渲染命令面板模态框。
func renderCommandPalette(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	p := getPaletteState(s)
	if p == nil || !p.open {
		return
	}

	filtered := paletteCachedVisibleItems(s, p, p.query)

	if len(filtered) > 0 {
		if p.selected < 0 {
			p.selected = 0
		} else if p.selected >= len(filtered) {
			p.selected = len(filtered) - 1
		}
	} else {
		p.selected = 0
	}

	width, _ := c.Size()
	// 这里用 DialogBase 而不是 Modal，因为 Modal 给面板自带背景与内边距，卡片外会多出一圈黑框，
	// 面板只负责限制大小，外观全部由卡片自己负责。
	ui.DialogBase(c, &p.open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.Color{}.Alpha(ScrimOpacity))
		winW, winH := c.Size()
		panel.MaxWidth(winW - paletteWindowInset).MaxHeight(winH - paletteWindowInset)
		if mods, key, ok := shortcutBindingFor("command-palette"); ok && backdrop.OverlayShortcut(mods, key) {
			closePalette(s)
		}
		ui.Column(c.Key("command-palette-card")).
			Width(paletteWidth).
			MaxWidth(width - paletteCardInset).
			Background(ThemeSurface(c)).
			BorderColor(ThemeBorder(c)).
			BorderWidth(Hairline).
			Radius(RadiusLG).
			Padding(Space3).
			Gap(Space3).
			Children(func() {
				// 顶部搜索输入框
				ui.Row(c.Key("palette-input-wrapper")).
					FillWidth().
					Radius(RadiusMD).
					Background(ThemeHover(c).Alpha(0.25)).
					BorderColor(ThemeBorder(c)).
					BorderWidth(Hairline).
					Padding(Space2, Space3).
					AlignItems(ui.Center).
					Gap(Space3).
					Children(func() {
						ui.Icon(c, IconSearch()).
							Size(IconSM, IconSM).
							TextColor(ThemeSecondaryText(c))

						in := ui.TextInput(c.Key("palette-query-input"), &p.query).
							Label("搜索会话、任务、自动化、项目或消息").
							Placeholder("搜索会话、任务、自动化、项目或消息").
							FontSize(uiFont(TypeFootnote)).
							TextColor(ThemePrimaryText(c)).
							Background(ui.Transparent).
							BorderWidth(0).
							Grow(1).
							AutoFocus()

						if in.Changed() || p.query != p.prevQuery {
							p.prevQuery = p.query
							p.selected = 0
							p.scrollSelection = true
							scheduleMessageSearch(s, p, p.query)
						}

						in.OnTextKey(func(k ui.TextKey) bool {
							if k.Text() != p.prevQuery {
								p.prevQuery = k.Text()
								p.selected = 0
								scheduleMessageSearch(s, p, k.Text())
							}
							current := paletteCachedVisibleItems(s, p, k.Text())
							if k.Key == ui.KeyUp {
								p.move(-1, len(current))
								s.invalidate()
								return true
							}
							if k.Key == ui.KeyDown {
								p.move(1, len(current))
								s.invalidate()
								return true
							}
							if k.Key == ui.KeyEnter {
								executePaletteSelection(s, current)
								return true
							}
							if k.Key == ui.KeyEscape {
								closePalette(s)
								return true
							}
							return false
						})
					})

				// 候选项列表或空状态
				if len(filtered) == 0 {
					ui.Row(c.Key("palette-empty-row")).
						FillWidth().
						Padding(Space7, 0).
						Justify(ui.Center).
						Children(func() {
							title := "没有匹配项"
							color := ThemeSecondaryText(c)
							if p.msgSearching {
								title = "搜索中…"
							} else if p.msgErr != "" {
								title = "搜索失败：" + p.msgErr
								color = ThemeDanger(c)
							}
							ui.Text(c, title).
								SingleLine().
								MinWidth(0).
								Shrink(1).
								Ellipsis("…").
								FontSize(uiFont(TypeFootnote)).
								TextColor(color)
						})
				} else {
					ui.Scroll(c.Key("palette-scroll")).
						MaxHeight(paletteListMaxHeight).
						FillWidth().
						Children(func() {
							ui.Column(c.Key("palette-items-list")).
								FillWidth().
								Gap(Space0).
								Children(func() {
									for i, it := range filtered {
										idx := i
										item := it
										isSelected := (p.selected == idx)

										row := ui.ButtonBase(c.Key("palette-item-"+item.id)).
											Label(item.title).
											FillWidth().
											Padding(Space3, Space3).
											Radius(RadiusSM).
											AlignItems(ui.Center).
											Justify(ui.Start).
											Gap(Space3).
											Cursor(ui.CursorPointer)

										if row.Hovered() && p.selected != idx {
											p.selected = idx
											isSelected = true
											s.invalidate()
										}
										if isSelected {
											row.Background(ThemeSelected(c))
											if p.scrollSelection {
												row.ScrollIntoView()
											}
										} else if row.Hovered() {
											row.Background(ThemeHover(c))
										}

										if row.Clicked() {
											p.selected = idx
											executePaletteSelection(s, filtered)
										}

										kindLabel := "命令"
										switch item.kind {
										case "thread":
											kindLabel = "会话"
										case "decision":
											kindLabel = "决策"
										case "task":
											kindLabel = "任务"
										case "goal":
											kindLabel = "目标"
										case "automation":
											kindLabel = "自动化"
										case "project":
											kindLabel = "项目"
										case "message":
											kindLabel = "消息"
										}

										row.Children(func() {
											ui.Box(c.Key("palette-kind-tag-"+item.id)).
												Padding(Space0, Space2).
												Radius(RadiusXS).
												Background(ThemeHover(c)).
												Children(func() {
													ui.Text(c, kindLabel).
														FontSize(uiFont(TypeCaption)).
														TextColor(ThemeSecondaryText(c))
												})

											ui.Text(c, item.title).
												FontSize(uiFont(TypeFootnote)).
												TextColor(ThemePrimaryText(c)).
												Grow(1).
												Shrink(1).
												MinWidth(0).
												SingleLine().
												Ellipsis("…")

											if item.statusText != "" {
												statusCol := ThemeSecondaryText(c)
												if item.statusColor != nil {
													statusCol = item.statusColor(c)
												}
												ui.Text(c, item.statusText).
													FontSize(uiFont(TypeCaption)).
													TextColor(statusCol).
													Shrink(0).
													SingleLine()
											}

											if item.subtitle != "" {
												ui.Text(c, item.subtitle).
													FontSize(uiFont(TypeCaption)).
													TextColor(ThemeSecondaryText(c)).
													Shrink(1).
													MinWidth(0).
													SingleLine().
													Ellipsis("…")
											}
											if mods, key, ok := shortcutBindingFor(item.shortcutID); ok {
												Kbd(c, "palette-kbd-"+item.id, formatShortcutKeys(mods, key))
											}
										})
									}
									if p.msgSearching {
										renderPaletteStatusRow(c, "palette-msg-searching", "搜索中…", ThemeSecondaryText(c))
									} else if p.msgErr != "" {
										renderPaletteStatusRow(c, "palette-msg-error", "搜索失败："+p.msgErr, ThemeDanger(c))
									}
								})
						})
				}

				// 面板页脚：左侧按键提示，右侧当前可见结果数。
				ui.Column(c.Key("palette-footer")).FillWidth().Gap(0).Children(func() {
					ui.Box(c.Key("palette-footer-divider")).FillWidth().Height(Hairline).Background(ThemeBorder(c))
					ui.Row(c.Key("palette-footer-row")).
						FillWidth().
						Height(ControlMD).
						AlignItems(ui.Center).
						Gap(Space4).
						Children(func() {
							renderPaletteFooterHint(c, "palette-footer-nav", "↑↓", "选择")
							renderPaletteFooterHint(c, "palette-footer-enter", "↵", "执行")
							renderPaletteFooterHint(c, "palette-footer-esc", "esc", "关闭")
							ui.Spacer(c)
							ui.Text(c, fmt.Sprintf("%d 项结果", len(filtered))).
								FontSize(uiFont(TypeCaption)).
								TextColor(ThemeSecondaryText(c))
						})
				})
			})
	})
	if !p.open {
		paletteMu.Lock()
		p.taskCacheOpen = false
		p.taskCacheSeq++
		paletteMu.Unlock()
	}
	p.scrollSelection = false
}

// renderPaletteStatusRow 在命令面板列表末尾渲染非交互的状态/错误提示行。
func renderPaletteStatusRow(c *ui.Context, key, text string, color ui.Color) {
	ui.Row(c.Key(key)).
		FillWidth().
		Padding(Space3, Space3).
		Radius(RadiusSM).
		AlignItems(ui.Center).
		Justify(ui.Start).
		Gap(Space3).
		Children(func() {
			ui.Text(c, text).
				SingleLine().
				MinWidth(0).
				Shrink(1).
				Ellipsis("…").
				FontSize(uiFont(TypeFootnote)).
				TextColor(color)
		})
}

// renderPaletteFooterHint 渲染页脚的一组键帽加说明文字，整组带无障碍标签便于测试与读屏。
func renderPaletteFooterHint(c *ui.Context, key, kbd, label string) {
	ui.Row(c.Key(key)).Gap(Space1).AlignItems(ui.Center).Label(kbd + " " + label).Children(func() {
		Kbd(c, key+"-kbd", kbd)
		ui.Text(c, label).FontSize(uiFont(TypeCaption)).TextColor(ThemeSecondaryText(c))
	})
}

func openAboutAndCheckUpdate(cs *chatState) {
	if getChatSettings(cs) == nil {
		return
	}
	openSettingsWindow(cs, "about")
	if st := currentSettingsWindowState(); st != nil {
		startUpdateCheck(st)
	}
}
