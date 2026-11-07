package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
	"mcode/internal/git"
	"mcode/internal/kanban"
)

var (
	kanbanMu     sync.Mutex
	kanbanStates = make(map[*chatState]*kanbanState)

	kanbanModalMu     sync.Mutex
	kanbanModalStates = make(map[*chatState]*kanbanModalState)
)

// kanbanDragPayload 是看板卡片拖拽传递的载荷数据。
type kanbanDragPayload struct {
	TaskID   string
	Status   string
	Revision int
	CanStart bool
}

// kanbanViewAutomations 是分段切换里的「自动化」视图，与看板 / 列表共用本地视图状态。
const kanbanViewAutomations = "automations"

const (
	// kanbanBadgeIconSize 是任务卡状态徽标里的小图标边长。
	kanbanBadgeIconSize float32 = 10
	// kanbanDetailChromeHeight 是任务详情弹窗标题与底部操作占用的总高度。
	kanbanDetailChromeHeight float32 = 70
	// kanbanDetailLogMaxHeight 是详情里执行日志滚动区的最大高度。
	kanbanDetailLogMaxHeight float32 = 180
	// kanbanDetailDiffMaxHeight 是详情里改动差异滚动区的最大高度。
	kanbanDetailDiffMaxHeight float32 = 220
	// kanbanCardHoverBorder 是任务卡悬停时的描边宽度。
	kanbanCardHoverBorder float32 = 1.5
)

// kanbanModalState 是「任务与看板」大弹窗的本地状态，按会话保存，不使用包级开关。
type kanbanModalState struct {
	open bool
	// projectMenuOpen 是看板头部（任务与看板窗口标题栏）项目选择器的展开状态。
	projectMenuOpen bool
	// pickerMenuOpen 是「新建任务」弹窗内项目下拉的展开状态，与头部的 projectMenuOpen 互相独立。
	pickerMenuOpen bool
}

// goalStatusBadgeText 把目标状态翻译成界面文案；未知状态统一显示「处理中」，
// 避免后端新增状态时把内部枚举串直接展示给用户。
func goalStatusBadgeText(status string) string {
	switch status {
	case "planning":
		return "规划中"
	case "running":
		return "运行中"
	case "completed":
		return "已完成"
	case "plan_failed":
		return "规划失败"
	case "plan_review":
		return "规划就绪"
	case "paused":
		return "已暂停"
	case "cancelled":
		return "已取消"
	}
	return "处理中"
}

// goalStatusBadgeColor 返回目标状态标签的强调色，与 goalStatusBadgeText 的映射保持一致。
func goalStatusBadgeColor(c *ui.Context, status string) ui.Color {
	switch status {
	case "planning", "running", "plan_review":
		return ThemeAccent(c)
	case "completed":
		return ThemeSuccess(c)
	case "plan_failed":
		return ThemeDanger(c)
	case "paused":
		return ThemeWarning(c)
	}
	return ThemeSecondaryText(c)
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
	projectID   string
	initialized bool
	loading     bool
	err         string
	board       KanbanBoard
	conflict    bool
	watchStop   func()
	loadID      int
	// viewMode 是当前会话的看板 / 列表 / 自动化视图；空值表示尚未应用默认视图。
	viewMode string
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
	showNewTask  bool
	newTaskTitle string
	newTaskNotes string

	// 新建目标草稿
	showNewGoal   bool
	newGoalPrompt string

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

// stopKanbanWatch 停止当前看板的实时更新订阅。
func stopKanbanWatch(s *chatState) {
	if s == nil {
		return
	}
	kanbanMu.Lock()
	defer kanbanMu.Unlock()
	if st, ok := kanbanStates[s]; ok {
		st.initialized = false
		if st.watchStop != nil {
			st.watchStop()
			st.watchStop = nil
		}
	}
}

// startKanbanWatch 订阅看板数据变更。
func (s *chatState) startKanbanWatch(projectID string) {
	if s == nil || s.client == nil || projectID == "" {
		return
	}
	ks := getKanbanState(s)
	if ks == nil {
		return
	}

	kanbanMu.Lock()
	if ks.watchStop != nil {
		ks.watchStop()
		ks.watchStop = nil
	}
	kanbanMu.Unlock()

	stop := s.client.WatchKanban(func() {
		s.loadKanban(projectID)
	})

	kanbanMu.Lock()
	ks.watchStop = stop
	kanbanMu.Unlock()
}

// loadKanban 异步加载看板数据。
func (s *chatState) loadKanban(projectID string) {
	if s == nil || s.client == nil || projectID == "" {
		return
	}
	ks := getKanbanState(s)
	if ks == nil {
		return
	}

	kanbanMu.Lock()
	if ks.projectID != projectID {
		kanbanMu.Unlock()
		return
	}
	ks.loadID++
	loadID := ks.loadID
	ks.loading = true
	ks.err = ""
	kanbanMu.Unlock()
	s.invalidate()

	go func(pid string) {
		ctx := context.Background()
		board, err := s.client.KanbanBoard(ctx, pid)

		s.updateWorkspace(func() {
			kanbanMu.Lock()
			defer kanbanMu.Unlock()
			if ks.projectID != pid || ks.loadID != loadID {
				return
			}
			ks.loading = false
			if err != nil {
				if errors.Is(err, ErrRevisionConflict) {
					ks.conflict = true
				} else {
					ks.err = err.Error()
				}
			} else {
				ks.board = board
				ks.err = ""
			}
			s.invalidate()
		})
	}(projectID)
}

// createTask 提交新建任务。
func (s *chatState) createTask(projectID, title, notes string) {
	if s == nil || s.client == nil {
		return
	}
	ks := getKanbanState(s)
	if ks == nil {
		return
	}

	go func() {
		ctx := context.Background()
		_, err := s.client.CreateTask(ctx, projectID, title, notes)
		if err != nil {
			if errors.Is(err, ErrRevisionConflict) {
				kanbanMu.Lock()
				ks.conflict = true
				kanbanMu.Unlock()
			} else {
				s.setToast("创建任务失败: " + err.Error())
			}
			s.invalidate()
			return
		}
		s.updateWorkspace(func() {
			if ks.projectID == projectID {
				ks.newTaskTitle = ""
				ks.newTaskNotes = ""
				ks.showNewTask = false
			}
		})
		s.loadKanban(projectID)
	}()
}

// createGoal 提交新建目标。
func (s *chatState) createGoal(projectID, prompt string) {
	if s == nil || s.client == nil {
		return
	}
	ks := getKanbanState(s)
	if ks == nil {
		return
	}

	go func() {
		ctx := context.Background()
		_, err := s.client.CreateGoal(ctx, projectID, prompt)
		if err != nil {
			if errors.Is(err, ErrRevisionConflict) {
				kanbanMu.Lock()
				ks.conflict = true
				kanbanMu.Unlock()
			} else {
				s.setToast("创建目标失败: " + err.Error())
			}
			s.invalidate()
			return
		}
		s.updateWorkspace(func() {
			if ks.projectID == projectID {
				ks.newGoalPrompt = ""
				ks.showNewGoal = false
			}
		})
		s.loadKanban(projectID)
	}()
}

// submitQuickAdd 提交待办列底部的快速添加，成功后清空草稿并收起输入框。
func (s *chatState) submitQuickAdd(projectID string) {
	if s == nil || s.client == nil {
		return
	}
	ks := getKanbanState(s)
	if ks == nil {
		return
	}
	kanbanMu.Lock()
	title := strings.TrimSpace(ks.quickAddText)
	kanbanMu.Unlock()
	if title == "" {
		return
	}

	go func() {
		ctx := context.Background()
		_, err := s.client.CreateTask(ctx, projectID, title, "")
		if err != nil {
			if errors.Is(err, ErrRevisionConflict) {
				kanbanMu.Lock()
				ks.conflict = true
				kanbanMu.Unlock()
			} else {
				s.setToast("创建任务失败: " + err.Error())
			}
			s.invalidate()
			return
		}
		s.updateWorkspace(func() {
			if ks.projectID == projectID {
				ks.quickAddText = ""
				ks.quickAddOpen = false
			}
		})
		s.loadKanban(projectID)
	}()
}

// kanbanTaskEditClient 是看板编辑能力的可选接口：删除任务与提醒执行中的任务会话。
// 旧客户端或测试替身没有实现时，界面只在不可用时给出提示。
type kanbanTaskEditClient interface {
	DeleteTask(ctx context.Context, taskID string, revision int) error
	NudgeTask(ctx context.Context, taskID string, revision int) (KanbanTask, error)
}

// kanbanEditClient 返回实现了看板编辑接口的客户端；旧客户端或测试替身返回 false。
func (s *chatState) kanbanEditClient() (kanbanTaskEditClient, bool) {
	if s == nil || s.client == nil {
		return nil, false
	}
	c, ok := s.client.(kanbanTaskEditClient)
	return c, ok
}

// nudgeTaskAgent 请求后端给任务会话追加一条继续推进的提醒；Client 不支持时只给出提示。
func (s *chatState) nudgeTaskAgent(task KanbanTask) {
	if s == nil {
		return
	}
	client, ok := s.kanbanEditClient()
	ks := getKanbanState(s)
	if !ok || ks == nil {
		s.setToast("当前版本的 Client 未提供提醒 Agent 的方法，已跳过")
		return
	}
	go func() {
		_, err := client.NudgeTask(context.Background(), task.ID, task.Revision)
		if err != nil {
			s.setToast("提醒 Agent 失败：" + err.Error())
		} else {
			s.setToast("已提醒 Agent")
		}
		// 结果回来后收起救援菜单，避免菜单停留在已经过期的状态。
		s.updateWorkspace(func() {
			kanbanMu.Lock()
			ks.rescueMenuTaskID = ""
			kanbanMu.Unlock()
		})
	}()
}

// deleteKanbanTask 确认删除任务：成功后清掉确认状态并刷新看板，失败给出中文提示。
func (s *chatState) deleteKanbanTask(task KanbanTask) {
	if s == nil {
		return
	}
	client, ok := s.kanbanEditClient()
	ks := getKanbanState(s)
	if !ok || ks == nil {
		s.setToast("当前版本的 Client 未提供删除任务的方法，已跳过")
		return
	}
	pid := ks.projectID
	go func() {
		if err := client.DeleteTask(context.Background(), task.ID, task.Revision); err != nil {
			s.setToast("删除任务失败：" + err.Error())
			s.invalidate()
			return
		}
		s.updateWorkspace(func() {
			kanbanMu.Lock()
			if ks.deleteTaskID == task.ID {
				ks.deleteTaskID = ""
			}
			ks.rescueMenuTaskID = ""
			kanbanMu.Unlock()
		})
		s.setToast("已删除任务")
		s.loadKanban(pid)
	}()
}

// interruptTaskToTodo 中断任务会话并把它移回待办；任务没有会话时只做移动。
func (s *chatState) interruptTaskToTodo(task KanbanTask) {
	if s == nil {
		return
	}
	if s.client != nil && task.ThreadID != "" {
		threadID := task.ThreadID
		go func() {
			_, _ = s.client.Interrupt(context.Background(), threadID)
		}()
	}
	s.moveTask(task.ID, "todo", task.Revision)
}

// kanbanTaskBlockReason 从任务已有字段拼出阻塞原因，供「启动」按钮的提示展示。
func kanbanTaskBlockReason(task KanbanTask) string {
	var parts []string
	if task.Blocked && len(task.DependsOn) > 0 {
		parts = append(parts, fmt.Sprintf("等待前置任务 %d 项完成", len(task.DependsOn)))
	}
	if task.LastFailureReason != "" {
		parts = append(parts, task.LastFailureReason)
	}
	for _, flag := range task.AttentionFlags {
		// 后端下发的关注标志是 snake_case 标识，这里转成中文
		switch flag {
		case "failed":
			parts = append(parts, "上次执行失败")
		case "stuck":
			parts = append(parts, "会话长时间无响应（卡死）")
		case "awaiting_approval":
			parts = append(parts, "等待审批")
		case "awaiting_input":
			parts = append(parts, "等待你的补充信息")
		case "needs_review":
			parts = append(parts, "等待审查")
		default:
			parts = append(parts, flag)
		}
	}
	return strings.Join(parts, "；")
}

// moveTask 移动任务到指定状态。
func (s *chatState) moveTask(taskID, status string, revision int) {
	if s == nil || s.client == nil {
		return
	}
	ks := getKanbanState(s)
	if ks == nil {
		return
	}

	pid := ks.projectID
	go func() {
		ctx := context.Background()
		_, err := s.client.MoveTask(ctx, taskID, status, revision)
		if err != nil {
			if errors.Is(err, ErrRevisionConflict) {
				kanbanMu.Lock()
				ks.conflict = true
				kanbanMu.Unlock()
			} else {
				s.setToast("移动任务失败: " + err.Error())
			}
			s.invalidate()
			return
		}
		s.loadKanban(pid)
	}()
}

// startTask 启动任务。
func (s *chatState) startTask(taskID string, revision int) {
	if s == nil || s.client == nil {
		return
	}
	ks := getKanbanState(s)
	if ks == nil {
		return
	}

	pid := ks.projectID
	go func() {
		ctx := context.Background()
		_, err := s.client.StartTask(ctx, taskID, revision)
		if err != nil {
			if errors.Is(err, ErrRevisionConflict) {
				kanbanMu.Lock()
				ks.conflict = true
				kanbanMu.Unlock()
			} else {
				s.setToast("启动任务失败: " + err.Error())
			}
			s.invalidate()
			return
		}
		s.loadKanban(pid)
		s.loadThreads(pid, false)
	}()
}

// reviewTask 验收任务（accept / reject）。
func (s *chatState) reviewTask(taskID, decision, feedback string, revision int) {
	if s == nil || s.client == nil {
		return
	}
	ks := getKanbanState(s)
	if ks == nil {
		return
	}

	pid := ks.projectID
	go func() {
		ctx := context.Background()
		_, err := s.client.ReviewTask(ctx, taskID, decision, feedback, revision)
		if err != nil {
			if errors.Is(err, ErrRevisionConflict) {
				s.updateWorkspace(func() {
					kanbanMu.Lock()
					if ks.projectID != pid {
						kanbanMu.Unlock()
						return
					}
					ks.conflict = true
					if ks.detailTaskID == taskID {
						ks.detailConflict = true
					}
					kanbanMu.Unlock()
					s.loadKanban(pid)
				})
			} else {
				s.setToast("验收任务失败: " + err.Error())
			}
			s.invalidate()
			return
		}
		s.updateWorkspace(func() {
			kanbanMu.Lock()
			if ks.projectID == pid {
				ks.reviewTaskID = ""
				ks.reviewFeedback = ""
				if ks.detailTaskID == taskID {
					ks.detailTaskID = ""
					ks.detailLoading = false
					ks.detailErr = ""
					ks.detailMessages = nil
					ks.detailDiff = nil
					ks.detailFiles = nil
					ks.detailDiffErr = ""
					ks.detailDiffTruncated = false
					ks.detailFeedback = ""
					ks.detailConflict = false
					ks.detailLoadID++
				}
			}
			kanbanMu.Unlock()
		})
		s.loadKanban(pid)
	}()
}

// openTaskDetail 打开指定任务的详情浮窗，并异步加载其日志与差异。
func (s *chatState) openTaskDetail(task KanbanTask) {
	s.loadTaskDetail(task, true)
}

// loadTaskDetail 刷新任务日志与差异，后台更新时保留审查草稿。
func (s *chatState) loadTaskDetail(task KanbanTask, reset bool) {
	if s == nil {
		return
	}
	ks := getKanbanState(s)
	if ks == nil {
		return
	}

	kanbanMu.Lock()
	ks.detailTaskID = task.ID
	ks.detailThreadID = task.ThreadID
	ks.detailRevision = task.Revision
	ks.detailLoading = (task.ThreadID != "")
	ks.detailErr = ""
	ks.detailMessages = nil
	ks.detailDiff = &diffPanelState{collapsed: make(map[string]bool)}
	ks.detailFiles = nil
	ks.detailDiffErr = ""
	ks.detailDiffTruncated = false
	if reset {
		ks.detailFeedback = ""
		ks.detailConflict = false
	}
	ks.detailLoadID++
	curLoadID := ks.detailLoadID
	targetTaskID := task.ID
	threadID := task.ThreadID
	projectID := ks.projectID
	kanbanMu.Unlock()
	s.invalidate()

	if threadID == "" || s.client == nil {
		return
	}

	go func(tid string, loadID int) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		var wg sync.WaitGroup
		var msgs []Message
		var histErr error
		var diffResp ThreadDiff
		var diffErr error

		wg.Add(2)
		go func() {
			defer wg.Done()
			msgs, histErr = s.client.History(ctx, tid)
		}()
		go func() {
			defer wg.Done()
			diffResp, diffErr = s.client.Diff(ctx, tid, "")
		}()
		wg.Wait()

		var files []git.FileDiff
		var parseErr error
		if diffErr == nil && diffResp.Diff != "" {
			files, parseErr = git.ParseUnifiedDiff([]byte(diffResp.Diff))
		}

		s.updateWorkspace(func() {
			kanbanMu.Lock()
			defer kanbanMu.Unlock()
			if ks.projectID != projectID || ks.detailLoadID != loadID || ks.detailTaskID != targetTaskID {
				return
			}
			ks.detailLoading = false
			if histErr != nil {
				ks.detailErr = histErr.Error()
				ks.detailMessages = nil
			} else {
				ks.detailErr = ""
				ks.detailMessages = msgs
			}

			if diffErr != nil {
				ks.detailDiffErr = diffErr.Error()
				ks.detailFiles = nil
				ks.detailDiffTruncated = false
			} else if diffResp.FailureReason != "" {
				ks.detailDiffErr = diffResp.FailureReason
				ks.detailFiles = nil
				ks.detailDiffTruncated = false
			} else if parseErr != nil {
				ks.detailDiffErr = parseErr.Error()
				ks.detailFiles = nil
				ks.detailDiffTruncated = false
			} else {
				ks.detailDiffErr = ""
				ks.detailFiles = files
				ks.detailDiffTruncated = diffResp.Truncated
			}
			s.invalidate()
		})
	}(threadID, curLoadID)
}

// closeTaskDetail 关闭任务详情浮窗并清空详情状态。
func (s *chatState) closeTaskDetail() {
	if s == nil {
		return
	}
	ks := getKanbanState(s)
	if ks == nil {
		return
	}
	kanbanMu.Lock()
	ks.detailTaskID = ""
	ks.detailLoading = false
	ks.detailErr = ""
	ks.detailMessages = nil
	ks.detailDiff = nil
	ks.detailFiles = nil
	ks.detailDiffErr = ""
	ks.detailDiffTruncated = false
	ks.detailFeedback = ""
	ks.detailConflict = false
	ks.detailLoadID++
	kanbanMu.Unlock()
	modalDragReset(modalDragKanbanTaskDetail)
	s.invalidate()
}

// closeGoalPlanReviewSheet 关闭规划审查弹窗并清空打回草稿。
func (s *chatState) closeGoalPlanReviewSheet() {
	if s == nil {
		return
	}
	ks := getKanbanState(s)
	if ks == nil {
		return
	}
	kanbanMu.Lock()
	ks.planReviewGoalID = ""
	ks.planReviewReplanOpen = false
	ks.planReviewFeedback = ""
	kanbanMu.Unlock()
	modalDragReset(modalDragKanbanPlanReview)
	s.invalidate()
}

// closeGoalOutputsModal 关闭目标产物弹窗。
func (s *chatState) closeGoalOutputsModal() {
	if s == nil {
		return
	}
	ks := getKanbanState(s)
	if ks == nil {
		return
	}
	kanbanMu.Lock()
	ks.outputsGoalID = ""
	kanbanMu.Unlock()
	modalDragReset(modalDragKanbanGoalOutputs)
	s.invalidate()
}

// startGoal 启动目标。
func (s *chatState) startGoal(goalID string, revision int) {
	if s == nil || s.client == nil {
		return
	}
	ks := getKanbanState(s)
	if ks == nil {
		return
	}

	pid := ks.projectID
	go func() {
		ctx := context.Background()
		_, err := s.client.StartGoal(ctx, goalID, revision)
		if err != nil {
			if errors.Is(err, ErrRevisionConflict) {
				kanbanMu.Lock()
				ks.conflict = true
				kanbanMu.Unlock()
			} else {
				s.setToast("启动目标失败: " + err.Error())
			}
			s.invalidate()
			return
		}
		s.loadKanban(pid)
	}()
}

// pauseGoal 暂停目标。
func (s *chatState) pauseGoal(goalID string, revision int) {
	if s == nil || s.client == nil {
		return
	}
	ks := getKanbanState(s)
	if ks == nil {
		return
	}

	pid := ks.projectID
	go func() {
		ctx := context.Background()
		_, err := s.client.PauseGoal(ctx, goalID, revision)
		if err != nil {
			if errors.Is(err, ErrRevisionConflict) {
				kanbanMu.Lock()
				ks.conflict = true
				kanbanMu.Unlock()
			} else {
				s.setToast("暂停目标失败: " + err.Error())
			}
			s.invalidate()
			return
		}
		s.loadKanban(pid)
	}()
}

// cancelGoal 取消目标。
func (s *chatState) cancelGoal(goalID string, revision int) {
	if s == nil || s.client == nil {
		return
	}
	ks := getKanbanState(s)
	if ks == nil {
		return
	}

	pid := ks.projectID
	go func() {
		ctx := context.Background()
		_, err := s.client.CancelGoal(ctx, goalID, revision)
		if err != nil {
			if errors.Is(err, ErrRevisionConflict) {
				kanbanMu.Lock()
				ks.conflict = true
				kanbanMu.Unlock()
			} else {
				s.setToast("取消目标失败: " + err.Error())
			}
			s.invalidate()
			return
		}
		s.loadKanban(pid)
	}()
}

// replanGoal 重新规划目标。
func (s *chatState) replanGoal(goalID, feedback string, revision int) {
	if s == nil || s.client == nil {
		return
	}
	ks := getKanbanState(s)
	if ks == nil {
		return
	}

	pid := ks.projectID
	go func() {
		ctx := context.Background()
		_, err := s.client.ReplanGoal(ctx, goalID, feedback, revision)
		if err != nil {
			if errors.Is(err, ErrRevisionConflict) {
				kanbanMu.Lock()
				ks.conflict = true
				kanbanMu.Unlock()
			} else {
				s.setToast("重新规划目标失败: " + err.Error())
			}
			s.invalidate()
			return
		}
		s.updateWorkspace(func() {
			if ks.projectID == pid {
				ks.replanGoalID = ""
				ks.replanFeedback = ""
			}
		})
		s.loadKanban(pid)
	}()
}

// planLayers 按依赖关系用 Kahn 算法把规划任务分层：第 1 层是无依赖任务，
// 有环时剩余任务并入最后一层。dependsOn 里指向集合外的 ID 视为不存在。
func planLayers(tasks []KanbanTask) [][]KanbanTask {
	if len(tasks) == 0 {
		return nil
	}

	index := make(map[string]int, len(tasks))
	for i, t := range tasks {
		index[t.ID] = i
	}
	dependents := make([][]int, len(tasks))
	indegree := make([]int, len(tasks))
	for i, t := range tasks {
		for _, dep := range t.DependsOn {
			j, ok := index[dep]
			if !ok || j == i {
				continue
			}
			dependents[j] = append(dependents[j], i)
			indegree[i]++
		}
	}

	placed := make([]bool, len(tasks))
	var frontier []int
	for i := range tasks {
		if indegree[i] == 0 {
			frontier = append(frontier, i)
		}
	}

	var layers [][]KanbanTask
	for len(frontier) > 0 {
		layer := make([]KanbanTask, 0, len(frontier))
		var next []int
		for _, i := range frontier {
			if placed[i] {
				continue
			}
			placed[i] = true
			layer = append(layer, tasks[i])
			for _, d := range dependents[i] {
				indegree[d]--
				if indegree[d] == 0 && !placed[d] {
					next = append(next, d)
				}
			}
		}
		layers = append(layers, layer)
		frontier = next
	}

	var rest []KanbanTask
	for i, t := range tasks {
		if !placed[i] {
			rest = append(rest, t)
		}
	}
	if len(rest) > 0 {
		layers = append(layers, rest)
	}
	return layers
}

// kanbanColumnDef 是看板列的定义：状态键、列名与列头圆形状态图标的颜色。
type kanbanColumnDef struct {
	key   string
	label string
	color func(*ui.Context) ui.Color
}

// 列定义：五列依次为待办、进行中、待你处理、待审阅、已完成。
var kanbanColumnDefs = []kanbanColumnDef{
	{key: "todo", label: "待办", color: func(c *ui.Context) ui.Color { return kanbanStatusColor(c, "todo") }},
	{key: "in_progress", label: "进行中", color: func(c *ui.Context) ui.Color { return kanbanStatusColor(c, "in_progress") }},
	{key: "needs_you", label: "待你处理", color: func(c *ui.Context) ui.Color { return kanbanStatusColor(c, "needs_you") }},
	{key: "review", label: "待审阅", color: func(c *ui.Context) ui.Color { return kanbanStatusColor(c, "review") }},
	{key: "done", label: "已完成", color: func(c *ui.Context) ui.Color { return kanbanStatusColor(c, "done") }},
}

// moveTargetOption 是移动菜单里的一个可选目标状态。
type moveTargetOption struct {
	status string
	label  string
	key    string
}

// 可移动的目标状态选项
var moveTargetOptions = []moveTargetOption{
	{status: "todo", label: "待办", key: "kanban-move-todo"},
	{status: "review", label: "审查", key: "kanban-move-review"},
	{status: "done", label: "完成", key: "kanban-move-done"},
	{status: "rejected", label: "驳回", key: "kanban-move-rejected"},
}

// moveTargetsFor 返回 from 状态可选的移动目标：跳过当前状态所在列，并按后端
// kanban.CanTransition 过滤，避免给 done / rejected 这类只剩「待办」的卡片提供
// 必定被后端拒绝的目标（否则选中只会得到「移动任务失败」）。
func moveTargetsFor(from string) []moveTargetOption {
	out := make([]moveTargetOption, 0, len(moveTargetOptions))
	for _, opt := range moveTargetOptions {
		if opt.status == from {
			continue
		}
		if !kanban.CanTransition(kanban.TaskStatus(from), kanban.TaskStatus(opt.status)) {
			continue
		}
		out = append(out, opt)
	}
	return out
}

// renderGoalCard 渲染单个目标卡片。
func renderGoalCard(c *ui.Context, s *chatState, ks *kanbanState, goal KanbanGoal, replanID, replanFeedback string) {
	primaryText := ThemePrimaryText(c)
	secondaryText := ThemeSecondaryText(c)

	ui.Column(c.Key("goal-card-"+goal.ID)).
		Grow(1).
		Padding(Space3, Space4).
		Radius(RadiusMD).
		Background(ThemeSurface(c)).
		BorderWidth(Hairline).
		BorderColor(ThemeBorder(c)).
		Gap(Space2).
		Children(func() {
			// 标题与状态标签
			ui.Row(c.Key("goal-header-" + goal.ID)).
				FillWidth().
				AlignItems(ui.Center).
				Gap(Space3).
				Children(func() {
					title := goal.Title
					if title == "" {
						title = goal.Prompt
					}
					ui.Text(c, title).
						FontSize(TypeBody).
						FontWeight(600).
						TextColor(primaryText).
						Grow(1).
						SingleLine()

					// 状态标签：文案与颜色分开映射，未知状态也不会露出原始英文。
					badgeColor := goalStatusBadgeColor(c, goal.Status)
					badgeText := goalStatusBadgeText(goal.Status)
					ui.Row(c.Key("goal-status-badge-"+goal.ID)).
						Padding(Space0, Space2).
						Radius(RadiusXS).
						Background(badgeColor.Alpha(0.12)).
						Children(func() {
							ui.Text(c, badgeText).
								FontSize(TypeCaption).
								FontWeight(500).
								TextColor(badgeColor)
						})
				})

			// 进度条与数字
			if goal.Total > 0 {
				ratio := float32(goal.Done) / float32(goal.Total)
				if ratio < 0 {
					ratio = 0
				} else if ratio > 1 {
					ratio = 1
				}
				ui.Row(c.Key("goal-progress-row-" + goal.ID)).
					FillWidth().
					AlignItems(ui.Center).
					Gap(Space3).
					Children(func() {
						// 进度条轨道
						ui.Row(c.Key("goal-progress-track-" + goal.ID)).
							Grow(1).
							Height(Space1).
							Radius(RadiusXS).
							Background(ThemeHover(c)).
							Children(func() {
								if ratio > 0 {
									ui.Box(c.Key("goal-progress-fill-" + goal.ID)).
										FillHeight().Width(0).Grow(ratio).
										Radius(RadiusXS).Background(ThemeAccent(c))
								}
								if ratio < 1 {
									ui.Box(c.Key("goal-progress-remaining-" + goal.ID)).
										Width(0).Grow(1 - ratio)
								}
							})

						ui.Text(c, fmt.Sprintf("%d/%d", goal.Done, goal.Total)).
							FontSize(TypeCaption).
							TextColor(secondaryText)
					})
			}

			// 描述信息
			desc := goal.StatusReason
			if desc == "" {
				desc = goal.PlanSummary
			}
			if desc == "" {
				desc = goal.ResultSummary
			}
			if desc != "" {
				ui.Text(c, desc).
					FontSize(TypeFootnote).
					TextColor(secondaryText).
					SingleLine()
			}

			// 操作按钮行
			ui.Row(c.Key("goal-actions-" + goal.ID)).
				FillWidth().
				AlignItems(ui.Center).
				Gap(Space2).
				Children(func() {
					// plan_review 或 paused 时显示开始
					if goal.Status == "plan_review" || goal.Status == "paused" {
						Button(c.Key("goal-start-"+goal.ID), ButtonOpts{
							Text:    "开始",
							Label:   "开始",
							Variant: ButtonPrimary,
							OnClick: func() {
								s.startGoal(goal.ID, goal.Revision)
							},
						})
					}

					// plan_review 时提供规划审查入口
					if goal.Status == "plan_review" {
						Button(c.Key("goal-plan-review-"+goal.ID), ButtonOpts{
							Text:    "审查规划",
							Label:   "审查规划",
							Variant: ButtonSecondary,
							OnClick: func() {
								kanbanMu.Lock()
								if ks.planReviewSeen == nil {
									ks.planReviewSeen = make(map[string]bool)
								}
								ks.planReviewSeen[goal.ID] = true
								ks.planReviewGoalID = goal.ID
								ks.planReviewReplanOpen = false
								ks.planReviewFeedback = ""
								kanbanMu.Unlock()
								s.invalidate()
							},
						})
					}

					// running 时显示暂停
					if goal.Status == "running" {
						Button(c.Key("goal-pause-"+goal.ID), ButtonOpts{
							Text:    "暂停",
							Label:   "暂停",
							Variant: ButtonSecondary,
							OnClick: func() {
								s.pauseGoal(goal.ID, goal.Revision)
							},
						})
					}

					// 未完成时显示取消
					if goal.Status != "completed" && goal.Status != "cancelled" {
						Button(c.Key("goal-cancel-"+goal.ID), ButtonOpts{
							Text:    "取消",
							Label:   "取消",
							Variant: ButtonDanger,
							OnClick: func() {
								s.cancelGoal(goal.ID, goal.Revision)
							},
						})
					}

					if goal.Status == "plan_failed" || goal.Status == "paused" || goal.Status == "plan_review" {
						Button(c.Key("goal-replan-toggle-"+goal.ID), ButtonOpts{
							Text:    "重新规划",
							Label:   "重新规划",
							Variant: ButtonSecondary,
							OnClick: func() {
								kanbanMu.Lock()
								if ks.replanGoalID == goal.ID {
									ks.replanGoalID = ""
								} else {
									ks.replanGoalID = goal.ID
								}
								kanbanMu.Unlock()
								s.invalidate()
							},
						})
					}

					// 目标产物查看入口（D-KB-035）
					Button(c.Key("goal-outputs-"+goal.ID), ButtonOpts{
						Text:    "目标产物",
						Label:   "goal-outputs-" + goal.ID,
						Variant: ButtonSecondary,
						OnClick: func() {
							kanbanMu.Lock()
							ks.outputsGoalID = goal.ID
							kanbanMu.Unlock()
							s.invalidate()
						},
					})
				})

			// 重新规划反馈输入框
			if replanID == goal.ID {
				ui.Row(c.Key("goal-replan-form-" + goal.ID)).
					FillWidth().
					AlignItems(ui.Center).
					Gap(Space2).
					Children(func() {
						ui.TextInput(c.Key("goal-replan-input-"+goal.ID), &ks.replanFeedback).
							Placeholder("填写规划反馈与建议...").
							Label("goal-replan-input").
							Grow(1)
						Button(c.Key("goal-replan-submit-"+goal.ID), ButtonOpts{
							Text:    "提交",
							Label:   "提交",
							Variant: ButtonPrimary,
							OnClick: func() {
								s.replanGoal(goal.ID, replanFeedback, goal.Revision)
							},
						})
					})
			}
		})
}

// renderGoalPlanReviewModal 渲染目标规划审查弹窗：任务按依赖拓扑分层展示，
// 可点击单卡进入任务详情，也可直接开始执行或打回重做。
func renderGoalPlanReviewModal(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	ks := getKanbanState(s)
	if ks == nil {
		return
	}

	kanbanMu.Lock()
	goalID := ks.planReviewGoalID
	boardLoading := ks.loading
	boardErr := ks.err
	conflict := ks.conflict
	goals := ks.board.Goals
	tasks := ks.board.Tasks
	replanOpen := ks.planReviewReplanOpen
	feedbackDraft := ks.planReviewFeedback
	kanbanMu.Unlock()

	if goalID == "" {
		return
	}

	var curGoal *KanbanGoal
	for i := range goals {
		if goals[i].ID == goalID {
			curGoal = &goals[i]
			break
		}
	}
	if curGoal == nil {
		// 看板还在加载或刚报错时目标暂时查不到：保留 goalID 等这一轮加载结束，
		// 否则刚点开的审查弹窗会自己关掉。
		if boardLoading || boardErr != "" {
			return
		}
		// 目标已不存在（换项目或已被删除），直接关闭弹窗
		s.closeGoalPlanReviewSheet()
		return
	}
	goal := *curGoal

	var goalTasks []KanbanTask
	for _, t := range tasks {
		if t.GoalID == goal.ID {
			goalTasks = append(goalTasks, t)
		}
	}
	// 序号沿用规划顺序，依赖标签用同一套编号
	numbers := make(map[string]int, len(goalTasks))
	for i, t := range goalTasks {
		numbers[t.ID] = i + 1
	}
	layers := planLayers(goalTasks)

	planning := goal.Status == "planning"
	planReady := goal.Status == "plan_review"
	canStart := planReady && len(goalTasks) > 0
	busy := boardLoading || (conflict && boardErr != "")

	winWidth, winHeight := c.Size()
	modalWidth := float32(680)
	if winWidth > 100 && modalWidth > winWidth-80 {
		modalWidth = winWidth - 80
	}
	modalMaxH := float32(620)
	if winHeight > 100 && modalMaxH > winHeight-80 {
		modalMaxH = winHeight - 80
	}
	modalWidth, modalMaxH = modalResizeSize(modalDragKanbanPlanReview, modalWidth, modalMaxH, min(360, max(0, winWidth-modalDragPanelInset)), min(240, max(0, winHeight-modalDragPanelInset)), winWidth, winHeight)
	contentMaxH := modalMaxH - 220
	if contentMaxH < 120 {
		contentMaxH = 120
	}

	open := true
	modalDragModal(c, modalDragKanbanPlanReview, &open, func() {
		if !open || c.Shortcut(0, ui.KeyEscape) {
			s.closeGoalPlanReviewSheet()
			return
		}

		ui.Column(c.Key("kanban-goal-plan-review")).
			Label("kanban-goal-plan-review").
			Width(modalWidth).
			Height(modalMaxH).
			Background(ThemeSurface(c)).
			BorderWidth(Hairline).
			BorderColor(ThemeBorder(c)).
			Radius(RadiusLG).
			Padding(Space5).
			Gap(Space4).
			Children(func() {
				// (a) 顶部栏：标题、目标标题与规划摘要、关闭按钮，整行兼作弹窗的拖拽把手
				modalDragHandle(c, modalDragKanbanPlanReview, ui.Row(c.Key("kanban-plan-review-header")).
					FillWidth().
					AlignItems(ui.Center).
					Children(func() {
						ui.Column(c.Key("kanban-plan-review-title-col")).
							Grow(1).
							Gap(Space1).
							Children(func() {
								ui.Text(c, "审查规划").
									FontSize(TypeTitle).
									FontWeight(600).
									TextColor(ThemePrimaryText(c))

								title := goal.Title
								if title == "" {
									title = goal.Prompt
								}
								if title != "" {
									ui.Text(c, title).
										FontSize(TypeBody).
										TextColor(ThemeSecondaryText(c)).
										SingleLine()
								}

								summary := goal.PlanSummary
								if summary == "" {
									summary = goal.StatusReason
								}
								if summary != "" {
									ui.Text(c, summary).
										FontSize(TypeFootnote).
										TextColor(ThemeSecondaryText(c))
								}
							})

						closeBtn := ui.ButtonBase(c.Key("kanban-plan-review-close")).
							Label("kanban-plan-review-close").
							Radius(RadiusSM).
							Padding(Space1, Space3).
							OnClick(func() {
								s.closeGoalPlanReviewSheet()
							})
						if closeBtn.Hovered() {
							closeBtn.Background(ThemeHover(c))
						}
						if closeBtn.Clicked() {
							s.closeGoalPlanReviewSheet()
						}
						closeBtn.Children(func() {
							ui.Row(c.Key("kanban-plan-review-close-inner")).
								Gap(Space1).
								AlignItems(ui.Center).
								Children(func() {
									ui.Icon(c, IconX()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
									ui.Text(c, "关闭").
										FontSize(TypeFootnote).
										TextColor(ThemeSecondaryText(c))
								})
						})
					}))

				// (b) 版本冲突与刷新失败提示
				if conflict {
					ui.Row(c.Key("kanban-plan-review-conflict")).
						FillWidth().
						Padding(Space3, Space3).
						Radius(RadiusSM).
						Background(ThemeDanger(c).Alpha(0.12)).
						Children(func() {
							ui.Text(c, "目标已在别处被修改，请刷新后重试").
								FontSize(TypeFootnote).
								TextColor(ThemeDanger(c))
						})
				}
				if boardErr != "" {
					ui.Text(c, "刷新失败: "+boardErr).
						FontSize(TypeFootnote).
						TextColor(ThemeDanger(c))
				}

				// (c) 规划内容：重新规划中 / 空规划 / 分层任务
				ui.Scroll(c.Key("kanban-plan-review-scroll")).
					FillWidth().
					MaxHeight(contentMaxH).
					Children(func() {
						ui.Column(c.Key("kanban-plan-review-body")).
							FillWidth().
							Gap(Space3).
							Children(func() {
								switch {
								case planning:
									ui.Column(c.Key("kanban-plan-review-planning")).
										FillWidth().
										Center().
										Gap(Space3).
										Padding(Space7, Space4).
										Children(func() {
											ui.Spinner(c).Size(IconLG, IconLG).TextColor(ThemeAccent(c))
											ui.Text(c, "规划中，请稍候…").
												FontSize(TypeFootnote).
												TextColor(ThemeSecondaryText(c))
										})
								case len(goalTasks) == 0:
									ui.Row(c.Key("kanban-plan-review-empty")).
										FillWidth().
										Padding(Space6, Space3).
										Radius(RadiusSM).
										Background(ThemeHover(c).Alpha(0.15)).
										Children(func() {
											ui.Text(c, "规划未产出任务").
												FontSize(TypeBody).
												TextColor(ThemeSecondaryText(c))
										})
								default:
									for li, layer := range layers {
										layerVal := layer
										ui.Column(c.Key(fmt.Sprintf("kanban-plan-review-layer-%d", li))).
											FillWidth().
											Gap(Space2).
											Children(func() {
												ui.Text(c, fmt.Sprintf("第 %d 层 · %d 项", li+1, len(layerVal))).
													FontSize(TypeCaption).
													FontWeight(500).
													TextColor(ThemeSecondaryText(c))
												for _, t := range layerVal {
													renderPlanReviewTaskCard(c, s, t, numbers)
												}
											})
									}
								}
							})
					})

				// (d) 打回重做的反馈输入框
				if replanOpen {
					ui.Row(c.Key("kanban-plan-review-replan-form")).
						FillWidth().
						AlignItems(ui.Center).
						Gap(Space2).
						Children(func() {
							ui.TextInput(c.Key("kanban-plan-review-feedback"), &ks.planReviewFeedback).
								Placeholder("填写打回反馈与建议...").
								Label("kanban-plan-review-feedback").
								Grow(1)
							Button(c.Key("kanban-plan-review-replan-submit"), ButtonOpts{
								Text:     "提交打回",
								Label:    "提交打回",
								Variant:  ButtonDanger,
								Disabled: busy || !planReady || strings.TrimSpace(feedbackDraft) == "",
								OnClick: func() {
									s.replanGoal(goal.ID, feedbackDraft, goal.Revision)
								},
							})
						})
				}

				// (e) 底部操作栏
				ui.Row(c.Key("kanban-plan-review-footer")).
					FillWidth().
					AlignItems(ui.Center).
					Gap(Space3).
					Children(func() {
						ui.Text(c, fmt.Sprintf("%d 项任务 · %d 层", len(goalTasks), len(layers))).
							FontSize(TypeCaption).
							TextColor(ThemeSecondaryText(c)).
							Grow(1)

						Button(c.Key("kanban-plan-review-replan-toggle"), ButtonOpts{
							Text:     "打回重做",
							Label:    "打回重做",
							Variant:  ButtonSecondary,
							Disabled: busy || !planReady,
							OnClick: func() {
								kanbanMu.Lock()
								ks.planReviewReplanOpen = !ks.planReviewReplanOpen
								if !ks.planReviewReplanOpen {
									ks.planReviewFeedback = ""
								}
								kanbanMu.Unlock()
								s.invalidate()
							},
						})

						Button(c.Key("kanban-plan-review-later"), ButtonOpts{
							Text:    "稍后",
							Label:   "稍后",
							Variant: ButtonSecondary,
							OnClick: func() {
								s.closeGoalPlanReviewSheet()
							},
						})

						if canStart {
							Button(c.Key("kanban-plan-review-start"), ButtonOpts{
								Text:     "开始执行",
								Label:    "开始执行",
								Variant:  ButtonPrimary,
								Disabled: busy,
								OnClick: func() {
									s.closeGoalPlanReviewSheet()
									s.startGoal(goal.ID, goal.Revision)
								},
							})
						}
					})
				modalResizeGrips(c, modalDragKanbanPlanReview, modalWidth, modalMaxH)
			})
	})
	if !open {
		s.closeGoalPlanReviewSheet()
	}
}

// renderPlanReviewTaskCard 渲染规划审查里的单张任务卡，点击进入任务详情编辑。
func renderPlanReviewTaskCard(c *ui.Context, s *chatState, task KanbanTask, numbers map[string]int) {
	// 依赖标签按序号排序，仅保留仍在规划里的依赖
	depNums := make([]int, 0, len(task.DependsOn))
	for _, dep := range task.DependsOn {
		if n, ok := numbers[dep]; ok {
			depNums = append(depNums, n)
		}
	}
	sort.Ints(depNums)

	card := ui.Column(c.Key("kanban-plan-review-task-"+task.ID)).
		Label("任务卡片 "+task.Title).
		FillWidth().
		Padding(Space3, Space3).
		Radius(RadiusMD).
		Background(ThemeHover(c).Alpha(0.20)).
		BorderWidth(Hairline).
		BorderColor(ThemeBorder(c)).
		Gap(Space1).
		OnClick(func() {
			s.openTaskDetail(task)
		})

	card.Children(func() {
		ui.Row(c.Key("kanban-plan-review-task-head-" + task.ID)).
			FillWidth().
			AlignItems(ui.Center).
			Gap(Space3).
			Children(func() {
				ui.Row(c.Key("kanban-plan-review-task-number-" + task.ID)).
					Width(ControlXS).
					Height(ControlXS).
					Radius(RadiusMD).
					Center().
					Background(ThemeAccent(c).Alpha(0.15)).
					Children(func() {
						ui.Text(c, fmt.Sprintf("%d", numbers[task.ID])).
							FontSize(TypeCaption).
							FontWeight(600).
							TextColor(ThemeAccent(c))
					})

				ui.Text(c, task.Title).
					FontSize(TypeBody).
					FontWeight(500).
					TextColor(ThemePrimaryText(c)).
					Grow(1)

				if task.Status != "" && task.Status != "todo" {
					ui.Row(c.Key("kanban-plan-review-task-status-"+task.ID)).
						Padding(Space0, Space1).
						Radius(RadiusXS).
						Background(ThemeHover(c)).
						Children(func() {
							ui.Text(c, task.Status).
								FontSize(TypeCaption).
								TextColor(ThemeSecondaryText(c))
						})
				}
			})

		if len(depNums) > 0 {
			labels := make([]string, 0, len(depNums))
			for _, n := range depNums {
				labels = append(labels, fmt.Sprintf("#%d", n))
			}
			ui.Text(c, "依赖："+strings.Join(labels, "、")).
				FontSize(TypeCaption).
				TextColor(ThemeSecondaryText(c))
		}

		if task.Notes != "" {
			ui.Text(c, task.Notes).
				FontSize(TypeCaption).
				TextColor(ThemeSecondaryText(c).Alpha(0.80)).
				SingleLine()
		}
	})
}

// goalOutputTask 是目标产物弹窗中的单条任务汇总。
type goalOutputTask struct {
	Task    KanbanTask
	Summary string
	HasDiff bool
}

// goalOutputs 从看板快照按目标汇总任务产物：只保留 GoalID 匹配的任务，
// 按看板列顺序（待办→进行中→等你处理→审查→完成）与标题稳定排序。
func goalOutputs(board KanbanBoard, goalID string) []goalOutputTask {
	goalID = strings.TrimSpace(goalID)
	if goalID == "" {
		return nil
	}
	var out []goalOutputTask
	for _, task := range board.Tasks {
		if task.GoalID != goalID {
			continue
		}
		out = append(out, goalOutputTask{
			Task:    task,
			Summary: goalOutputSummary(task),
			HasDiff: task.ThreadID != "",
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := goalOutputColumnRank(out[i].Task.Column), goalOutputColumnRank(out[j].Task.Column)
		if ri != rj {
			return ri < rj
		}
		return out[i].Task.Title < out[j].Task.Title
	})
	return out
}

// goalOutputColumnRank 返回任务所在列的展示顺序，未知列排在最后。
func goalOutputColumnRank(column string) int {
	for i, def := range kanbanColumnDefs {
		if def.key == column {
			return i
		}
	}
	return len(kanbanColumnDefs)
}

// goalOutputSummary 提取任务的完成说明/Notes 摘要，优先完成说明（评审总结）。
func goalOutputSummary(task KanbanTask) string {
	if strings.TrimSpace(task.ReviewSummary) != "" {
		return task.ReviewSummary
	}
	return task.Notes
}

// goalOutputStatusLabel 将任务状态映射为界面文案，未知状态原样返回。
func goalOutputStatusLabel(status string) string {
	switch status {
	case "todo":
		return "待办"
	case "in_progress":
		return "进行中"
	case "needs_approval":
		return "待审批"
	case "review":
		return "审查"
	case "done":
		return "完成"
	case "rejected":
		return "已驳回"
	default:
		return status
	}
}

// renderGoalOutputsModal 渲染目标产物弹窗：按目标汇总任务的标题、状态与完成说明，
// 有会话的任务提供「查看 Diff」跳转并复用任务详情浮窗（内含单任务 Diff 面板）。
// 数据只取 KanbanBoard 快照，不新增 Client 方法。
func renderGoalOutputsModal(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	ks := getKanbanState(s)
	if ks == nil {
		return
	}

	kanbanMu.Lock()
	goalID := ks.outputsGoalID
	board := ks.board
	kanbanMu.Unlock()

	if goalID == "" {
		return
	}

	var curGoal *KanbanGoal
	for i := range board.Goals {
		if board.Goals[i].ID == goalID {
			curGoal = &board.Goals[i]
			break
		}
	}
	if curGoal == nil {
		// 目标已不存在（换项目或已被删除），直接关闭弹窗
		s.closeGoalOutputsModal()
		return
	}
	goal := *curGoal
	outputs := goalOutputs(board, goal.ID)
	doneCount := 0
	for _, out := range outputs {
		if out.Task.Status == "done" {
			doneCount++
		}
	}

	winWidth, winHeight := c.Size()
	modalWidth := float32(640)
	if winWidth > 100 && modalWidth > winWidth-80 {
		modalWidth = winWidth - 80
	}
	modalMaxH := float32(560)
	if winHeight > 100 && modalMaxH > winHeight-80 {
		modalMaxH = winHeight - 80
	}
	modalWidth, modalMaxH = modalResizeSize(modalDragKanbanGoalOutputs, modalWidth, modalMaxH, min(360, max(0, winWidth-modalDragPanelInset)), min(240, max(0, winHeight-modalDragPanelInset)), winWidth, winHeight)
	contentMaxH := modalMaxH - 180
	if contentMaxH < 120 {
		contentMaxH = 120
	}

	open := true
	modalDragModal(c, modalDragKanbanGoalOutputs, &open, func() {
		if !open || c.Shortcut(0, ui.KeyEscape) {
			s.closeGoalOutputsModal()
			return
		}

		ui.Column(c.Key("kanban-goal-outputs")).
			Label("kanban-goal-outputs").
			Width(modalWidth).
			Height(modalMaxH).
			Background(ThemeSurface(c)).
			BorderWidth(Hairline).
			BorderColor(ThemeBorder(c)).
			Radius(RadiusLG).
			Padding(Space5).
			Gap(Space4).
			Children(func() {
				// (a) 顶部栏：标题、目标标题与结果摘要、关闭按钮，整行兼作弹窗的拖拽把手
				modalDragHandle(c, modalDragKanbanGoalOutputs, ui.Row(c.Key("kanban-goal-outputs-header")).
					FillWidth().
					AlignItems(ui.Center).
					Children(func() {
						ui.Column(c.Key("kanban-goal-outputs-title-col")).
							Grow(1).
							Gap(Space1).
							Children(func() {
								ui.Text(c, "目标产物").
									FontSize(TypeTitle).
									FontWeight(600).
									TextColor(ThemePrimaryText(c))

								title := goal.Title
								if title == "" {
									title = goal.Prompt
								}
								if title != "" {
									ui.Text(c, title).
										FontSize(TypeBody).
										TextColor(ThemeSecondaryText(c)).
										SingleLine()
								}

								if goal.ResultSummary != "" {
									ui.Text(c, goal.ResultSummary).
										FontSize(TypeFootnote).
										TextColor(ThemeSecondaryText(c))
								}
							})

						closeBtn := ui.ButtonBase(c.Key("kanban-goal-outputs-close")).
							Label("kanban-goal-outputs-close").
							Radius(RadiusSM).
							Padding(Space1, Space3).
							OnClick(func() {
								s.closeGoalOutputsModal()
							})
						if closeBtn.Hovered() {
							closeBtn.Background(ThemeHover(c))
						}
						if closeBtn.Clicked() {
							s.closeGoalOutputsModal()
						}
						closeBtn.Children(func() {
							ui.Row(c.Key("kanban-goal-outputs-close-inner")).
								Gap(Space1).
								AlignItems(ui.Center).
								Children(func() {
									ui.Icon(c, IconX()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
									ui.Text(c, "关闭").
										FontSize(TypeFootnote).
										TextColor(ThemeSecondaryText(c))
								})
						})
					}))

				// (b) 产物列表：无任务时显示空态
				ui.Scroll(c.Key("kanban-goal-outputs-scroll")).
					FillWidth().
					MaxHeight(contentMaxH).
					Children(func() {
						ui.Column(c.Key("kanban-goal-outputs-body")).
							FillWidth().
							Gap(Space3).
							Children(func() {
								if len(outputs) == 0 {
									ui.Row(c.Key("kanban-goal-outputs-empty")).
										FillWidth().
										Padding(Space6, Space3).
										Radius(RadiusSM).
										Background(ThemeHover(c).Alpha(0.15)).
										Children(func() {
											ui.Text(c, "该目标暂无任务产出").
												FontSize(TypeBody).
												TextColor(ThemeSecondaryText(c))
										})
									return
								}
								for _, out := range outputs {
									renderGoalOutputRow(c, s, ks, out)
								}
							})
					})

				// (c) 底部：任务计数与关闭
				ui.Row(c.Key("kanban-goal-outputs-footer")).
					FillWidth().
					AlignItems(ui.Center).
					Gap(Space3).
					Children(func() {
						ui.Text(c, fmt.Sprintf("%d 项任务 · 已完成 %d 项", len(outputs), doneCount)).
							FontSize(TypeCaption).
							TextColor(ThemeSecondaryText(c)).
							Grow(1)

						Button(c.Key("kanban-goal-outputs-footer-close"), ButtonOpts{
							Text:    "关闭",
							Label:   "关闭",
							Variant: ButtonSecondary,
							OnClick: func() {
								s.closeGoalOutputsModal()
							},
						})
					})
				modalResizeGrips(c, modalDragKanbanGoalOutputs, modalWidth, modalMaxH)
			})
	})
	if !open {
		s.closeGoalOutputsModal()
	}
}

// renderGoalOutputRow 渲染目标产物弹窗中的单条任务行。
func renderGoalOutputRow(c *ui.Context, s *chatState, ks *kanbanState, out goalOutputTask) {
	task := out.Task

	ui.Column(c.Key("kanban-goal-output-row-"+task.ID)).
		FillWidth().
		Padding(Space3, Space3).
		Radius(RadiusMD).
		Background(ThemeHover(c).Alpha(0.20)).
		BorderWidth(Hairline).
		BorderColor(ThemeBorder(c)).
		Gap(Space1).
		Children(func() {
			ui.Row(c.Key("kanban-goal-output-head-" + task.ID)).
				FillWidth().
				AlignItems(ui.Center).
				Gap(Space3).
				Children(func() {
					ui.Text(c, task.Title).
						FontSize(TypeBody).
						FontWeight(500).
						TextColor(ThemePrimaryText(c)).
						Grow(1)

					ui.Row(c.Key("kanban-goal-output-status-"+task.ID)).
						Padding(Space0, Space1).
						Radius(RadiusXS).
						Background(ThemeHover(c)).
						Children(func() {
							ui.Text(c, goalOutputStatusLabel(task.Status)).
								FontSize(TypeCaption).
								TextColor(ThemeSecondaryText(c))
						})

					if out.HasDiff {
						Button(c.Key("kanban-goal-output-diff-"+task.ID), ButtonOpts{
							Text:    "查看 Diff",
							Label:   "kanban-goal-output-diff-" + task.ID,
							Size:    ControlXS,
							Variant: ButtonSecondary,
							OnClick: func() {
								// 关闭产物弹窗，复用任务详情浮窗中的单任务 Diff 面板
								kanbanMu.Lock()
								ks.outputsGoalID = ""
								kanbanMu.Unlock()
								s.openTaskDetail(task)
							},
						})
					}
				})

			if out.Summary != "" {
				ui.Text(c, out.Summary).
					FontSize(TypeCaption).
					TextColor(ThemeSecondaryText(c)).
					SingleLine()
			}
		})
}

// filterKanbanTasks 按查询串过滤任务：标题、备注、最近活动或失败原因包含该串即保留，
// 不区分大小写；空串原样返回。
func filterKanbanTasks(tasks []KanbanTask, query string) []KanbanTask {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return tasks
	}
	out := make([]KanbanTask, 0, len(tasks))
	for _, task := range tasks {
		if strings.Contains(strings.ToLower(task.Title), query) ||
			strings.Contains(strings.ToLower(task.Notes), query) ||
			strings.Contains(strings.ToLower(task.LastActivity), query) ||
			strings.Contains(strings.ToLower(task.LastFailureReason), query) {
			out = append(out, task)
		}
	}
	return out
}

// kanbanStatusLabel 返回任务状态的界面文案，未知状态原样返回。
func kanbanStatusLabel(status string) string {
	switch status {
	case "todo":
		return "待办"
	case "in_progress":
		return "进行中"
	case "needs_you":
		return "等你处理"
	case "needs_approval":
		return "待审批"
	case "review":
		return "审查"
	case "done":
		return "完成"
	case "rejected":
		return "已驳回"
	default:
		return status
	}
}

// kanbanListGroup 是列表视图里按状态聚合的一组任务。
type kanbanListGroup struct {
	status string
	label  string
	tasks  []KanbanTask
}

// kanbanListGroups 按固定状态顺序分组任务，未知状态追加在末尾；
// 组内按更新时间倒序，更新时间相同则按标题排序。
func kanbanListGroups(tasks []KanbanTask) []kanbanListGroup {
	order := []string{"todo", "in_progress", "needs_you", "review", "done"}
	groups := make([]kanbanListGroup, 0, len(order))
	index := make(map[string]int, len(order))
	for _, status := range order {
		index[status] = len(groups)
		groups = append(groups, kanbanListGroup{status: status, label: kanbanStatusLabel(status)})
	}

	for _, task := range tasks {
		status := kanbanDisplayColumn(task)
		if status == "" {
			status = "unknown"
		}
		idx, ok := index[status]
		if !ok {
			idx = len(groups)
			index[status] = idx
			groups = append(groups, kanbanListGroup{status: status, label: kanbanStatusLabel(status)})
		}
		groups[idx].tasks = append(groups[idx].tasks, task)
	}

	out := make([]kanbanListGroup, 0, len(groups))
	for _, group := range groups {
		if len(group.tasks) == 0 {
			continue
		}
		sort.SliceStable(group.tasks, func(i, j int) bool {
			if !group.tasks[i].UpdatedAt.Equal(group.tasks[j].UpdatedAt) {
				return group.tasks[i].UpdatedAt.After(group.tasks[j].UpdatedAt)
			}
			return group.tasks[i].Title < group.tasks[j].Title
		})
		out = append(out, group)
	}
	return out
}

// renderKanbanListView 渲染按状态分组的任务列表视图。
func renderKanbanListView(c *ui.Context, s *chatState, tasks []KanbanTask) {
	ui.Scroll(c.Key("kanban-list-scroll")).
		Grow(1).
		FillWidth().
		Children(func() {
			ui.Column(c.Key("kanban-list-root")).
				FillWidth().
				Gap(Space4).
				Children(func() {
					for _, group := range kanbanListGroups(tasks) {
						group := group
						ui.Column(c.Key("kanban-list-group-" + group.status)).
							Label("kanban-list-group-" + group.status).
							FillWidth().
							Gap(Space2).
							Children(func() {
								ui.Text(c, fmt.Sprintf("%s · %d", group.label, len(group.tasks))).
									FontSize(TypeFootnote).
									FontWeight(600).
									TextColor(ThemeSecondaryText(c))

								for _, task := range group.tasks {
									task := task
									renderKanbanListRow(c, s, task)
								}
							})
					}
				})
		})
}

// renderKanbanListRow 渲染列表视图中的单条任务行，点击打开与看板卡片相同的详情。
func renderKanbanListRow(c *ui.Context, s *chatState, task KanbanTask) {
	updated := "—"
	if !task.UpdatedAt.IsZero() {
		updated = task.UpdatedAt.Local().Format("2006-01-02 15:04")
	}

	ui.Row(c.Key("kanban-list-row-"+task.ID)).
		Label("kanban-list-row-"+task.ID).
		FillWidth().
		Padding(Space3, Space3).
		Radius(RadiusMD).
		Background(ThemeHover(c).Alpha(0.20)).
		BorderWidth(Hairline).
		BorderColor(ThemeBorder(c)).
		AlignItems(ui.Center).
		Gap(Space3).
		OnClick(func() {
			s.openTaskDetail(task)
		}).
		Children(func() {
			ui.Text(c, task.Title).
				FontSize(TypeBody).
				FontWeight(500).
				TextColor(ThemePrimaryText(c)).
				Grow(1).
				SingleLine()

			ui.Row(c.Key("kanban-list-status-"+task.ID)).
				Padding(Space0, Space1).
				Radius(RadiusXS).
				Background(ThemeHover(c)).
				Children(func() {
					ui.Text(c, kanbanStatusLabel(task.Status)).
						FontSize(TypeCaption).
						TextColor(ThemeSecondaryText(c))
				})

			ui.Text(c, "更新于 "+updated).
				FontSize(TypeCaption).
				TextColor(ThemeSecondaryText(c))
		})
}

// renderTodoQuickAdd 渲染待办列底部的「+ 添加任务」：点击展开单行输入，回车提交、Esc 取消。
func renderTodoQuickAdd(c *ui.Context, s *chatState, ks *kanbanState) {
	kanbanMu.Lock()
	open := ks.quickAddOpen
	projectID := ks.projectID
	kanbanMu.Unlock()

	if projectID == "" {
		return
	}

	if !open {
		Button(c.Key("kanban-quick-add-toggle"), ButtonOpts{
			Icon:    IconPlus(),
			Text:    "添加任务",
			Label:   "kanban-quick-add-toggle",
			Variant: ButtonSecondary,
			OnClick: func() {
				kanbanMu.Lock()
				ks.quickAddOpen = true
				ks.quickAddText = ""
				kanbanMu.Unlock()
				s.invalidate()
			},
		})
		return
	}

	ui.Row(c.Key("kanban-quick-add-row")).
		Label("kanban-quick-add-row").
		FillWidth().
		Padding(Space2, Space3).
		Radius(RadiusMD).
		Background(ThemeSurface(c)).
		BorderWidth(Hairline).
		BorderColor(ThemeAccent(c).Alpha(0.60)).
		AlignItems(ui.Center).
		Gap(Space2).
		Children(func() {
			ui.Icon(c, IconPlus()).Size(inlineIconSize, inlineIconSize).TextColor(ThemeAccent(c))

			in := ui.TextInput(c.Key("kanban-quick-add-input"), &ks.quickAddText).
				Placeholder("添加任务...").
				Label("kanban-quick-add-input").
				FontSize(TypeFootnote).
				BorderWidth(0).
				Background(ui.Transparent).
				Padding(0).
				Grow(1).
				AutoFocus()

			in.OnTextKey(func(k ui.TextKey) bool {
				switch k.Key {
				case ui.KeyEnter:
					s.submitQuickAdd(projectID)
					return true
				}
				return false
			})

			// Esc 登记为输入框自己的快捷键：根窗口的 Esc（关闭工作区）会先于 OnTextKey 收到按键。
			if in.Shortcut(0, ui.KeyEscape) {
				kanbanMu.Lock()
				ks.quickAddOpen = false
				ks.quickAddText = ""
				kanbanMu.Unlock()
				s.invalidate()
			}
		})
}

// kanbanTaskCardState 汇总看板列与任务卡片共用的渲染状态。
type kanbanTaskCardState struct {
	moveMenuTaskID, reviewTaskID, reviewFeedback string
	goals                                        []KanbanGoal
	rescueMenuTaskID, deleteTaskID               string
	selecting                                    bool
}

// projectNameFor 返回项目显示名，找不到时返回空字符串。
func projectNameFor(s *chatState, projectID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, project := range s.projects {
		if project.ID == projectID {
			return project.Name
		}
	}
	return ""
}

// kanbanPriorityStyle 返回任务优先级的徽标样式：文案、图标与颜色。
// 服务端默认值是 "none"，此时 ok 为 false，卡片不渲染优先级徽标。
func kanbanPriorityStyle(c *ui.Context, p string) (label string, icon *ui.SVG, color ui.Color, ok bool) {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "", "none":
		return "", nil, ui.Color{}, false
	case "urgent":
		return "紧急", IconFlame(), ThemeDanger(c), true
	case "high":
		return "高", IconChevronsUp(), ThemeWarning(c), true
	case "medium":
		return "中", IconCircleSmall(), ThemeCaution(c), true
	case "low":
		return "低", IconCircleSmall(), ThemeAccent(c), true
	default:
		return p, IconCircleSmall(), ThemeSecondaryText(c), true
	}
}

// kanbanStatusColor 返回看板列对应的状态色。
func kanbanStatusColor(c *ui.Context, col string) ui.Color {
	switch col {
	case "todo":
		return ThemeAccent(c)
	case "in_progress":
		return ThemeWarning(c)
	case "needs_you":
		return ThemePlan(c)
	case "review":
		return ThemeCaution(c)
	case "done":
		return ThemeSuccess(c)
	case "rejected":
		return ThemeDanger(c)
	default:
		return ThemeSecondaryText(c)
	}
}

// kanbanToneBadge 渲染使用指定颜色的小胶囊。
func kanbanToneBadge(c *ui.Context, key, label string, color ui.Color) {
	ui.Row(c.Key(key)).
		Label(key).
		Padding(Space0, Space2).
		Radius(RadiusMD).
		Background(color.Alpha(0.15)).
		Children(func() {
			ui.Text(c, label).FontSize(TypeCaption).FontWeight(600).TextColor(color)
		})
}

// renderTaskCard 渲染单张任务卡片：项目胶囊与右上角状态 / 审查徽标、标题、备注预览、
// 打开审查会话按钮，以及底部的 agent 信息与「开始」按钮。
func renderTaskCard(c *ui.Context, s *chatState, ks *kanbanState, task KanbanTask, st kanbanTaskCardState) {
	primaryText := ThemePrimaryText(c)
	secondaryText := ThemeSecondaryText(c)

	card := ui.Column(c.Key("kanban-task-card-"+task.ID)).
		Label("kanban-task-card-"+task.ID).
		FillWidth().
		Padding(Space3, Space3).
		Radius(RadiusMD).
		Background(ThemeSurface(c).Alpha(0.55)).
		BorderWidth(Hairline).
		BorderColor(ThemeOverlay(c, 0.08)).
		Gap(Space3).
		OnClick(func() {
			// 多选模式下点击待办卡片只切换选中态，不打开详情。
			if st.selecting && kanbanDisplayColumn(task) == "todo" {
				kanbanMu.Lock()
				if ks.selectedTaskIDs == nil {
					ks.selectedTaskIDs = make(map[string]bool)
				}
				ks.selectedTaskIDs[task.ID] = !ks.selectedTaskIDs[task.ID]
				kanbanMu.Unlock()
				s.invalidate()
				return
			}
			s.openTaskDetail(task)
		})
	// 悬停时提亮底色与描边，让指针下的卡片更清晰
	if card.Hovered() {
		card.Background(ThemeSurface(c).Alpha(0.60)).BorderColor(ThemeAccent(c).Alpha(0.35))
	}
	if st.selecting && kanbanDisplayColumn(task) == "todo" {
		kanbanMu.Lock()
		selected := ks.selectedTaskIDs[task.ID]
		kanbanMu.Unlock()
		if selected {
			card.BorderWidth(kanbanCardHoverBorder).
				BorderColor(ThemeAccent(c)).
				Background(ThemeSurface(c).Alpha(0.75))
		}
	}

	// 右键菜单：与卡片内的按钮复用同一批调用
	card.ContextMenu(func(m *ui.Menu) {
		if m.Item("查看详情").Chosen() {
			s.openTaskDetail(task)
		}
		if task.CanStart {
			m.Separator()
			startLabel := "开始"
			if kanbanDisplayColumn(task) == "in_progress" {
				startLabel = "继续"
			}
			if m.Item(startLabel).Chosen() {
				if task.Blocked || len(task.AttentionFlags) > 0 {
					s.setToast("暂不可启动：" + kanbanTaskBlockReason(task))
					return
				}
				s.startTask(task.ID, task.Revision)
			}
		}
		if task.CanMove {
			m.Separator()
			for _, opt := range moveTargetsFor(task.Status) {
				optVal := opt
				if m.Item("移动到 " + optVal.label).Chosen() {
					s.moveTask(task.ID, optVal.status, task.Revision)
				}
			}
		}
	})

	// 若任务允许移动，挂载原生拖拽源
	if task.CanMove {
		card.Drag(kanbanDragPayload{
			TaskID:   task.ID,
			Status:   task.Status,
			Revision: task.Revision,
			CanStart: task.CanStart,
		})
	}

	card.Children(func() {
		// 头部：选中框、项目胶囊，右侧优先显示状态 / 审查徽标
		ui.Row(c.Key("kanban-task-title-row-" + task.ID)).
			FillWidth().
			AlignItems(ui.Center).
			Gap(Space2).
			Children(func() {
				if st.selecting && kanbanDisplayColumn(task) == "todo" {
					kanbanMu.Lock()
					selected := ks.selectedTaskIDs[task.ID]
					kanbanMu.Unlock()
					icon := IconSquare()
					color := secondaryText
					if selected {
						icon = IconCircleCheck()
						color = ThemeAccent(c)
					}
					ui.Icon(c, icon).Size(IconSM, IconSM).TextColor(color)
				}

				if task.Blocked {
					ui.Icon(c, IconFlame()).Size(inlineIconSize, inlineIconSize).TextColor(ThemeWarning(c))
				}

				if name := projectNameFor(s, task.ProjectID); name != "" {
					ui.Row(c.Key("kanban-task-project-"+task.ID)).
						Padding(Space0, Space1).
						Radius(RadiusXS).
						Background(ThemeOverlay(c, 0.06)).
						Children(func() {
							ui.Text(c, name).
								FontSize(TypeCaption).
								FontWeight(500).
								TextColor(secondaryText).
								SingleLine()
						})
				}

				if pLabel, pIcon, pColor, ok := kanbanPriorityStyle(c, task.Priority); ok {
					ui.Row(c.Key("kanban-task-prio-"+task.ID)).
						Padding(Space0, Space2).
						Radius(RadiusMD).
						Gap(Space1).
						AlignItems(ui.Center).
						Background(pColor.Alpha(0.15)).
						Children(func() {
							ui.Icon(c, pIcon).Size(cardGlyphSize, cardGlyphSize).TextColor(pColor)
							ui.Text(c, pLabel).
								FontSize(TypeCaption).
								FontWeight(600).
								TextColor(pColor)
						})
				}

				ui.Box(c.Key("kanban-task-head-spacer-" + task.ID)).Grow(1)

				// 状态与审查徽标：颜色与列头圆点保持一致
				if label := kanbanStatusBadge(task); label != "" {
					kanbanToneBadge(c, "kanban-task-status-"+task.ID, label, kanbanStatusColor(c, kanbanDisplayColumn(task)))
				}
			})

		// 标题
		ui.Text(c, task.Title).
			FontSize(TypeBody).
			FontWeight(600).
			MaxLines(2).
			TextColor(primaryText)

		// 阻塞与注意标记
		if task.Blocked || len(task.AttentionFlags) > 0 {
			ui.Row(c.Key("kanban-task-flags-" + task.ID)).
				FillWidth().
				AlignItems(ui.Center).
				Wrap().
				Gap(Space2).
				Children(func() {
					if task.Blocked {
						ui.Row(c.Key("kanban-task-blocked-"+task.ID)).
							Padding(Space0, Space1).
							Radius(RadiusXS).
							Background(ThemeDanger(c).Alpha(0.12)).
							Gap(Space1).
							Children(func() {
								ui.Icon(c, IconFlame()).
									Size(kanbanBadgeIconSize, kanbanBadgeIconSize).
									TextColor(ThemeDanger(c))
								ui.Text(c, "阻塞").
									FontSize(TypeCaption).
									TextColor(ThemeDanger(c))
							})
					}
					for idx, flag := range task.AttentionFlags {
						fVal := flag
						ui.Row(c.Key(fmt.Sprintf("kanban-task-flag-%s-%d", task.ID, idx))).
							Padding(Space0, Space1).
							Radius(RadiusXS).
							Background(ThemeWarning(c).Alpha(0.12)).
							Children(func() {
								ui.Text(c, fVal).
									FontSize(TypeCaption).
									TextColor(ThemeWarning(c))
							})
					}
				})
		}

		// 备注预览：最多两行
		if task.Notes != "" {
			ui.Text(c, task.Notes).
				FontSize(TypeCaption).
				TextColor(secondaryText).
				MaxLines(2)
		}

		// 目标提示：任务来自目标规划时，用一行次要文字显示所属目标
		if task.GoalID != "" {
			goalHint := "目标任务 " + task.GoalID
			for _, g := range st.goals {
				if g.ID == task.GoalID {
					if title := strings.TrimSpace(g.Title); title != "" {
						goalHint = title
					} else if prompt := strings.TrimSpace(g.Prompt); prompt != "" {
						goalHint = prompt
					}
					break
				}
			}
			ui.Row(c.Key("kanban-task-goal-" + task.ID)).
				FillWidth().
				AlignItems(ui.Center).
				Gap(Space1).
				Children(func() {
					ui.Icon(c, IconWandSparkles()).Size(kanbanBadgeIconSize, kanbanBadgeIconSize).TextColor(ThemePlan(c))
					ui.Text(c, goalHint).
						FontSize(TypeCaption).
						FontWeight(500).
						TextColor(ThemePlan(c)).
						SingleLine()
				})
		}

		// 最近活动或失败原因
		activity := task.LastFailureReason
		if activity == "" {
			activity = task.LastActivity
		}
		if activity != "" {
			ui.Text(c, activity).
				FontSize(TypeCaption).
				TextColor(secondaryText.Alpha(0.80)).
				SingleLine()
		}

		// 审查会话入口：有审查会话时显示「打开审查会话」
		if task.ThreadID != "" && kanbanDisplayColumn(task) != "todo" {
			Button(c.Key("kanban-task-review-open-"+task.ID), ButtonOpts{
				Text:    "打开审查会话",
				Label:   "kanban-task-review-open-" + task.ID,
				Size:    ControlXS,
				Variant: ButtonSecondary,
				OnClick: func() {
					s.closeWorkspace()
					s.loadThreads(task.ProjectID, false)
					s.revealThread(task.ThreadID)
				},
			})
		}

		// 操作按钮区：CanStart, CanMove, CanReview
		ui.Row(c.Key("kanban-task-actions-" + task.ID)).
			FillWidth().
			Wrap().
			AlignItems(ui.Center).
			Gap(Space2).
			Children(func() {
				if task.ThreadID != "" {
					Button(c.Key("kanban-task-"+task.ID+"-open"), ButtonOpts{
						Text: "会话", Label: "kanban-task-" + task.ID + "-open",
						OnClick: func() {
							s.closeWorkspace()
							s.loadThreads(task.ProjectID, false)
							s.revealThread(task.ThreadID)
						},
						Variant: ButtonSecondary,
					})
				}
				// 已完成任务可以重新打开，移回待办
				if task.Status == "done" || task.Column == "done" {
					Button(c.Key("kanban-task-"+task.ID+"-reopen"), ButtonOpts{
						Text:    "重新打开",
						Label:   "kanban-task-" + task.ID + "-reopen",
						Variant: ButtonSecondary,
						OnClick: func() {
							s.moveTask(task.ID, "todo", task.Revision)
						},
					})
				}

				// 移动菜单按钮
				if task.CanMove {
					Button(c.Key("kanban-task-"+task.ID+"-move"), ButtonOpts{
						Text:    "移动",
						Label:   "kanban-task-" + task.ID + "-move",
						Variant: ButtonSecondary,
						OnClick: func() {
							kanbanMu.Lock()
							if ks.moveMenuTaskID == task.ID {
								ks.moveMenuTaskID = ""
							} else {
								ks.moveMenuTaskID = task.ID
							}
							kanbanMu.Unlock()
							s.invalidate()
						},
					})
				}
				ui.Box(c.Key("kanban-task-actions-spacer-" + task.ID)).Grow(1)

				// 开始：被阻塞时点击或悬停说明原因，不启动任务。
				if task.CanStart {
					label := "开始"
					if kanbanDisplayColumn(task) == "in_progress" {
						label = "继续"
					}
					startBtn := Button(c.Key("kanban-task-"+task.ID+"-start"), ButtonOpts{
						Text:    label,
						Label:   "kanban-task-" + task.ID + "-start",
						Size:    ControlXS,
						Variant: ButtonPrimary,
						OnClick: func() {
							if task.Blocked || len(task.AttentionFlags) > 0 {
								s.setToast("暂不可启动：" + kanbanTaskBlockReason(task))
								return
							}
							s.startTask(task.ID, task.Revision)
						},
					})
					if task.Blocked || len(task.AttentionFlags) > 0 {
						startBtn.Tooltip("暂不可启动：" + kanbanTaskBlockReason(task))
					}
				}

				// 卡片救援菜单：进行中或卡死的卡片提供会话救援入口
				if task.Status == "in_progress" || len(task.AttentionFlags) > 0 {
					Button(c.Key("kanban-task-"+task.ID+"-more"), ButtonOpts{
						Icon:    IconChevronRight(),
						Label:   "kanban-task-" + task.ID + "-more",
						Size:    ControlXS,
						Variant: ButtonSecondary,
						OnClick: func() {
							kanbanMu.Lock()
							if ks.rescueMenuTaskID == task.ID {
								ks.rescueMenuTaskID = ""
							} else {
								ks.rescueMenuTaskID = task.ID
								ks.moveMenuTaskID = ""
							}
							ks.deleteTaskID = ""
							kanbanMu.Unlock()
							s.invalidate()
						},
					})
				}
			})

		// 移动选项菜单展开
		if st.moveMenuTaskID == task.ID {
			ui.Row(c.Key("kanban-task-move-options-"+task.ID)).
				FillWidth().
				Wrap().
				Padding(Space1, Space2).
				Radius(RadiusSM).
				Background(ThemeHover(c)).
				AlignItems(ui.Center).
				Gap(Space1).
				Children(func() {
					ui.Text(c, "移动到:").
						FontSize(TypeCaption).
						TextColor(secondaryText)

					for _, opt := range moveTargetsFor(task.Status) {
						optVal := opt
						Button(c.Key(optVal.key), ButtonOpts{
							Text:    optVal.label,
							Label:   optVal.key,
							Size:    ControlXS,
							Variant: ButtonSecondary,
							OnClick: func() {
								kanbanMu.Lock()
								ks.moveMenuTaskID = ""
								kanbanMu.Unlock()
								s.moveTask(task.ID, optVal.status, task.Revision)
							},
						})
					}
				})
		}

		// 救援菜单展开：打开会话、提醒 Agent、中断并重置、删除
		if st.rescueMenuTaskID == task.ID {
			ui.Row(c.Key("kanban-task-rescue-options-"+task.ID)).
				Label("kanban-task-rescue-options-"+task.ID).
				FillWidth().
				Wrap().
				Padding(Space1, Space2).
				Radius(RadiusSM).
				Background(ThemeHover(c)).
				AlignItems(ui.Center).
				Gap(Space1).
				Children(func() {
					ui.Text(c, "救援:").
						FontSize(TypeCaption).
						TextColor(secondaryText)

					if task.ThreadID != "" {
						Button(c.Key("kanban-task-rescue-open-"+task.ID), ButtonOpts{
							Text:    "打开会话",
							Label:   "kanban-task-rescue-open-" + task.ID,
							Size:    ControlXS,
							Variant: ButtonSecondary,
							OnClick: func() {
								kanbanMu.Lock()
								ks.rescueMenuTaskID = ""
								kanbanMu.Unlock()
								s.closeWorkspace()
								s.loadThreads(task.ProjectID, false)
								s.revealThread(task.ThreadID)
							},
						})
					}

					Button(c.Key("kanban-task-rescue-nudge-"+task.ID), ButtonOpts{
						Text:    "提醒 Agent",
						Label:   "kanban-task-rescue-nudge-" + task.ID,
						Size:    ControlXS,
						Variant: ButtonSecondary,
						OnClick: func() {
							s.nudgeTaskAgent(task)
						},
					})

					Button(c.Key("kanban-task-rescue-interrupt-"+task.ID), ButtonOpts{
						Text:    "中断并重置为待办",
						Label:   "kanban-task-rescue-interrupt-" + task.ID,
						Size:    ControlXS,
						Variant: ButtonSecondary,
						OnClick: func() {
							kanbanMu.Lock()
							ks.rescueMenuTaskID = ""
							kanbanMu.Unlock()
							s.interruptTaskToTodo(task)
						},
					})

					Button(c.Key("kanban-task-rescue-delete-"+task.ID), ButtonOpts{
						Text:    "删除",
						Label:   "kanban-task-rescue-delete-" + task.ID,
						Size:    ControlXS,
						Variant: ButtonDanger,
						OnClick: func() {
							kanbanMu.Lock()
							ks.deleteTaskID = task.ID
							kanbanMu.Unlock()
							s.invalidate()
						},
					})
				})
		}

		// 删除二次确认：确认后调用客户端删除任务，成功时刷新看板。
		if st.deleteTaskID == task.ID {
			ui.Row(c.Key("kanban-task-delete-confirm-"+task.ID)).
				FillWidth().
				Wrap().
				AlignItems(ui.Center).
				Padding(Space1, Space2).
				Radius(RadiusSM).
				Background(ThemeDanger(c).Alpha(0.10)).
				Gap(Space2).
				Children(func() {
					ui.Text(c, "确认删除该任务？此操作不可撤销").
						FontSize(TypeCaption).
						TextColor(ThemeDanger(c)).
						Grow(1)
					Button(c.Key("kanban-task-delete-cancel-"+task.ID), ButtonOpts{
						Text:    "取消",
						Label:   "kanban-task-delete-cancel-" + task.ID,
						Size:    ControlXS,
						Variant: ButtonSecondary,
						OnClick: func() {
							kanbanMu.Lock()
							ks.deleteTaskID = ""
							kanbanMu.Unlock()
							s.invalidate()
						},
					})
					Button(c.Key("kanban-task-delete-confirm-yes-"+task.ID), ButtonOpts{
						Text:    "确认删除",
						Label:   "kanban-task-delete-confirm-yes-" + task.ID,
						Size:    ControlXS,
						Variant: ButtonDanger,
						OnClick: func() {
							s.deleteKanbanTask(task)
						},
					})
				})
		}

		// 验收驳回反馈输入框
		if st.reviewTaskID == task.ID {
			ui.Row(c.Key("kanban-task-reject-form-" + task.ID)).
				FillWidth().
				AlignItems(ui.Center).
				Gap(Space2).
				Children(func() {
					ui.TextInput(c.Key("kanban-task-reject-feedback-"+task.ID), &st.reviewFeedback).
						Placeholder("驳回反馈原因...").
						Label("kanban-task-reject-feedback").
						Grow(1)

					Button(c.Key("kanban-task-reject-submit-"+task.ID), ButtonOpts{
						Text:    "确认驳回",
						Label:   "确认驳回",
						Variant: ButtonDanger,
						OnClick: func() {
							s.reviewTask(task.ID, "reject", st.reviewFeedback, task.Revision)
						},
					})
				})
		}
	})
}

// renderTaskDetail 渲染看板任务卡片的详情浮窗。
func renderTaskDetail(c *ui.Context, s *chatState, ks *kanbanState) {
	if s == nil || ks == nil {
		return
	}

	kanbanMu.Lock()
	taskID := ks.detailTaskID
	detailThreadID := ks.detailThreadID
	detailRevision := ks.detailRevision
	boardLoading := ks.loading
	boardErr := ks.err
	loading := ks.detailLoading
	detailErr := ks.detailErr
	messages := ks.detailMessages
	dp := ks.detailDiff
	files := ks.detailFiles
	diffErr := ks.detailDiffErr
	truncated := ks.detailDiffTruncated
	conflict := ks.detailConflict
	tasks := ks.board.Tasks
	kanbanMu.Unlock()

	if taskID == "" {
		return
	}

	var curTask *KanbanTask
	for i := range tasks {
		if tasks[i].ID == taskID {
			curTask = &tasks[i]
			break
		}
	}
	if curTask == nil {
		s.closeTaskDetail()
		return
	}
	task := *curTask
	if task.ThreadID != detailThreadID || task.Revision != detailRevision {
		s.loadTaskDetail(task, false)
		loading = task.ThreadID != ""
		messages = nil
		files = nil
		detailErr = ""
		diffErr = ""
		truncated = false
	}

	colLabel := task.Column
	for _, cd := range kanbanColumnDefs {
		if cd.key == task.Column {
			colLabel = cd.label
			break
		}
	}

	winWidth, winHeight := c.Size()
	modalWidth := float32(660)
	if winWidth > 100 && modalWidth > winWidth-80 {
		modalWidth = winWidth - 80
	}
	modalMaxH := float32(620)
	if winHeight > 100 && modalMaxH > winHeight-80 {
		modalMaxH = winHeight - 80
	}

	modalWidth, modalMaxH = modalResizeSize(modalDragKanbanTaskDetail, modalWidth, modalMaxH, min(360, max(0, winWidth-modalDragPanelInset)), min(240, max(0, winHeight-modalDragPanelInset)), winWidth, winHeight)
	open := true
	modalDragModal(c, modalDragKanbanTaskDetail, &open, func() {
		if !open || c.Shortcut(0, ui.KeyEscape) {
			s.closeTaskDetail()
			return
		}

		ui.Column(c.Key("kanban-task-detail")).
			Label("kanban-task-detail").
			Width(modalWidth).
			Height(modalMaxH).
			Background(ThemeSurface(c)).
			BorderWidth(Hairline).
			BorderColor(ThemeBorder(c)).
			Radius(RadiusLG).
			Padding(Space5).
			Gap(Space4).
			Children(func() {
				// (a) 顶部栏：标题、状态和列徽章、关闭按钮，整行兼作弹窗的拖拽把手
				modalDragHandle(c, modalDragKanbanTaskDetail, ui.Row(c.Key("kanban-task-detail-header")).
					FillWidth().
					AlignItems(ui.Center).
					Children(func() {
						ui.Column(c.Key("kanban-task-detail-title-col")).
							Grow(1).
							Gap(Space1).
							Children(func() {
								ui.Text(c, task.Title).
									FontSize(TypeTitle).
									FontWeight(600).
									TextColor(ThemePrimaryText(c))

								ui.Row(c.Key("kanban-task-detail-badges")).
									Gap(Space2).
									AlignItems(ui.Center).
									Children(func() {
										if task.Status != "" {
											ui.Row(c.Key("kanban-task-detail-status-badge")).
												Padding(Space0, Space2).
												Radius(RadiusXS).
												Background(ThemeHover(c)).
												Children(func() {
													ui.Text(c, "状态: "+task.Status).
														FontSize(TypeCaption).
														TextColor(ThemeSecondaryText(c))
												})
										}
										if colLabel != "" {
											ui.Row(c.Key("kanban-task-detail-col-badge")).
												Padding(Space0, Space2).
												Radius(RadiusXS).
												Background(ThemeHover(c)).
												Children(func() {
													ui.Text(c, "列: "+colLabel).
														FontSize(TypeCaption).
														TextColor(ThemeSecondaryText(c))
												})
										}
									})
							})

						closeBtn := ui.ButtonBase(c.Key("kanban-task-detail-close")).
							Label("kanban-task-detail-close").
							Radius(RadiusSM).
							Padding(Space1, Space3).
							OnClick(func() {
								s.closeTaskDetail()
							})
						if closeBtn.Hovered() {
							closeBtn.Background(ThemeHover(c))
						}
						if closeBtn.Clicked() {
							s.closeTaskDetail()
						}
						closeBtn.Children(func() {
							ui.Row(c.Key("kanban-task-detail-close-inner")).
								Gap(Space1).
								AlignItems(ui.Center).
								Children(func() {
									ui.Icon(c, IconX()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
									ui.Text(c, "关闭").
										FontSize(TypeFootnote).
										TextColor(ThemeSecondaryText(c))
								})
						})
					}))

				// 主内容区域（可滚动）
				ui.Scroll(c.Key("kanban-task-detail-scroll")).
					FillWidth().
					MaxHeight(modalMaxH - kanbanDetailChromeHeight).
					Children(func() {
						ui.Column(c.Key("kanban-task-detail-body")).
							FillWidth().
							Gap(Space4).
							Children(func() {
								// 冲突提示
								if conflict {
									ui.Row(c.Key("kanban-task-detail-conflict-row")).
										FillWidth().
										Padding(Space3, Space3).
										Radius(RadiusSM).
										Background(ThemeDanger(c).Alpha(0.12)).
										Children(func() {
											ui.Text(c, "任务已被其他地方修改，请核对刷新后的任务与改动后重试").
												FontSize(TypeFootnote).
												TextColor(ThemeDanger(c))
										})
									if boardLoading {
										ui.Text(c, "正在刷新任务…").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
									} else if boardErr != "" {
										ui.Text(c, "刷新失败: "+boardErr).FontSize(TypeFootnote).TextColor(ThemeDanger(c))
										Button(c.Key("kanban-task-detail-refresh"), ButtonOpts{
											Text:  "重新刷新",
											Label: "kanban-task-detail-refresh",
											OnClick: func() {
												s.loadKanban(ks.projectID)
											},
											Variant: ButtonSecondary,
										})
									}
								}

								// (b) 备注 Notes，以及失败原因、审查总结
								if task.Notes != "" {
									ui.Row(c.Key("kanban-task-detail-notes-row")).
										FillWidth().
										Padding(Space2, Space3).
										Radius(RadiusSM).
										Background(ThemeHover(c).Alpha(0.20)).
										Children(func() {
											ui.Text(c, "备注: "+task.Notes).
												FontSize(TypeFootnote).
												TextColor(ThemeSecondaryText(c))
										})
								}
								if task.LastFailureReason != "" {
									ui.Row(c.Key("kanban-task-detail-failure-row")).
										FillWidth().
										Padding(Space2, Space3).
										Radius(RadiusSM).
										Background(ThemeDanger(c).Alpha(0.10)).
										Children(func() {
											ui.Text(c, "失败原因: "+task.LastFailureReason).
												FontSize(TypeFootnote).
												TextColor(ThemeDanger(c))
										})
								}
								if task.ReviewSummary != "" {
									ui.Row(c.Key("kanban-task-detail-review-summary-row")).
										FillWidth().
										Padding(Space2, Space3).
										Radius(RadiusSM).
										Background(ThemeHover(c).Alpha(0.20)).
										Children(func() {
											ui.Text(c, "审查总结: "+task.ReviewSummary).
												FontSize(TypeFootnote).
												TextColor(ThemeSecondaryText(c))
										})
								}

								// (c) & (d) 日志与改动
								if task.ThreadID == "" {
									ui.Row(c.Key("kanban-task-detail-no-thread")).
										FillWidth().
										Padding(Space4, Space3).
										Radius(RadiusSM).
										Background(ThemeHover(c).Alpha(0.15)).
										Children(func() {
											ui.Text(c, "任务尚未运行，暂无日志与改动").
												FontSize(TypeBody).
												TextColor(ThemeSecondaryText(c))
										})
								} else {
									// (c)「运行日志」分区
									ui.Column(c.Key("kanban-task-detail-log-section")).
										FillWidth().
										Gap(Space2).
										Children(func() {
											ui.Text(c, "运行日志").
												FontSize(TypeBody).
												FontWeight(600).
												TextColor(ThemePrimaryText(c))

											if loading {
												ui.Row(c.Key("kanban-task-detail-log-loading")).
													Gap(Space3).
													AlignItems(ui.Center).
													Padding(Space3, 0).
													Children(func() {
														ui.Spinner(c).Size(IconSM, IconSM).TextColor(ThemeAccent(c))
														ui.Text(c, "正在加载日志…").
															FontSize(TypeFootnote).
															TextColor(ThemeSecondaryText(c))
													})
											} else if detailErr != "" {
												ui.Row(c.Key("kanban-task-detail-log-err")).
													Padding(Space2, Space3).
													Radius(RadiusSM).
													Background(ThemeDanger(c).Alpha(0.12)).
													Children(func() {
														ui.Text(c, "日志加载失败: "+detailErr).
															FontSize(TypeFootnote).
															TextColor(ThemeDanger(c))
													})
											} else if len(messages) == 0 {
												ui.Row(c.Key("kanban-task-detail-log-empty")).
													Padding(Space3, 0).
													Children(func() {
														ui.Text(c, "暂无日志").
															FontSize(TypeFootnote).
															TextColor(ThemeSecondaryText(c))
													})
											} else {
												ui.Scroll(c.Key("kanban-task-detail-log-scroll")).
													FillWidth().
													MaxHeight(kanbanDetailLogMaxHeight).
													Children(func() {
														ui.Column(c.Key("kanban-task-detail-log-list")).
															FillWidth().
															Gap(Space2).
															Children(func() {
																for i, m := range messages {
																	msgVal := m
																	txt := msgVal.Text
																	role := msgVal.Role
																	if role == "" {
																		role = "unknown"
																	}
																	ui.Column(c.Key(fmt.Sprintf("kanban-detail-msg-%d", i))).
																		FillWidth().
																		Padding(Space1, Space2).
																		Radius(RadiusXS).
																		Background(ThemeHover(c).Alpha(0.15)).
																		Gap(Space0).
																		Children(func() {
																			ui.Text(c, "["+role+"]").
																				FontSize(TypeCaption).
																				FontWeight(600).
																				TextColor(ThemeAccent(c))
																			ui.Text(c, txt).
																				FontSize(TypeFootnote).
																				TextColor(ThemePrimaryText(c))
																		})
																}
															})
													})
											}
										})

									// (d)「改动」分区
									ui.Column(c.Key("kanban-task-detail-diff-section")).
										FillWidth().
										Gap(Space2).
										Children(func() {
											ui.Text(c, "改动").
												FontSize(TypeBody).
												FontWeight(600).
												TextColor(ThemePrimaryText(c))

											if loading {
												ui.Row(c.Key("kanban-task-detail-diff-loading")).
													Gap(Space3).
													AlignItems(ui.Center).
													Padding(Space3, 0).
													Children(func() {
														ui.Spinner(c).Size(IconSM, IconSM).TextColor(ThemeAccent(c))
														ui.Text(c, "正在加载改动…").
															FontSize(TypeFootnote).
															TextColor(ThemeSecondaryText(c))
													})
											} else if diffErr != "" {
												ui.Row(c.Key("kanban-task-detail-diff-err")).
													Padding(Space2, Space3).
													Radius(RadiusSM).
													Background(ThemeDanger(c).Alpha(0.12)).
													Children(func() {
														ui.Text(c, "改动加载失败: "+diffErr).
															FontSize(TypeFootnote).
															TextColor(ThemeDanger(c))
													})
											} else if len(files) == 0 {
												ui.Row(c.Key("kanban-task-detail-diff-empty")).
													Padding(Space3, 0).
													Children(func() {
														ui.Text(c, "无改动").
															FontSize(TypeFootnote).
															TextColor(ThemeSecondaryText(c))
													})
											} else {
												ui.Scroll(c.Key("kanban-task-detail-diff-scroll")).
													FillWidth().
													MaxHeight(kanbanDetailDiffMaxHeight).
													Children(func() {
														ui.Column(c.Key("kanban-task-detail-diff-content")).
															FillWidth().
															Children(func() {
																renderDiffFileList(c, s, dp, files)

																if truncated {
																	ui.Row(c.Key("kanban-task-detail-diff-truncated")).
																		Padding(Space2, Space3).
																		Radius(RadiusSM).
																		Background(ThemeWarning(c).Alpha(0.12)).
																		Margin(Space1, 0, 0, 0).
																		Children(func() {
																			ui.Text(c, "改动过多，已截断").
																				FontSize(TypeFootnote).
																				TextColor(ThemeWarning(c))
																		})
																}
															})
													})
											}
										})
								}

								// (e) 审查区
								if task.CanReview || task.Column == "review" {
									ui.Column(c.Key("kanban-task-detail-review-section")).
										FillWidth().
										Padding(Space3, 0, 0, 0).
										BorderWidth(Hairline, 0, 0, 0).
										BorderColor(ThemeBorder(c)).
										Gap(Space3).
										Children(func() {
											ui.Text(c, "任务审查").
												FontSize(TypeBody).
												FontWeight(600).
												TextColor(ThemePrimaryText(c))

											ui.TextInput(c.Key("kanban-task-detail-feedback-input"), &ks.detailFeedback).
												Placeholder("输入审查/驳回反馈...").
												Label("kanban-task-detail-feedback").
												FillWidth()

											ui.Row(c.Key("kanban-task-detail-review-actions")).
												FillWidth().
												Justify(ui.End).
												Gap(Space3).
												Children(func() {
													Button(c.Key("kanban-task-detail-accept"), ButtonOpts{
														Text:     "批准",
														Label:    "kanban-task-detail-accept",
														Variant:  ButtonPrimary,
														Disabled: loading || boardLoading || (conflict && boardErr != ""),
														OnClick: func() {
															s.reviewTask(task.ID, "accept", ks.detailFeedback, task.Revision)
														},
													})

													Button(c.Key("kanban-task-detail-reject"), ButtonOpts{
														Text:     "驳回",
														Label:    "kanban-task-detail-reject",
														Variant:  ButtonDanger,
														Disabled: loading || boardLoading || (conflict && boardErr != ""),
														OnClick: func() {
															s.reviewTask(task.ID, "reject", ks.detailFeedback, task.Revision)
														},
													})
												})
										})
								}
							})
					})
				modalResizeGrips(c, modalDragKanbanTaskDetail, modalWidth, modalMaxH)
			})
	})
	if !open {
		s.closeTaskDetail()
	}
}
