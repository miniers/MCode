package mcodeui

import (
	"context"
	"errors"
	"strings"
	"time"

	"mcode/internal/git"
)

func (s *chatState) syncKanbanProject(projectID string) {
	ks := getKanbanState(s)
	kanbanMu.Lock()
	changed := !ks.initialized || ks.projectID != projectID
	if changed {
		ks.projectID = projectID
	}
	kanbanMu.Unlock()
	if changed {
		s.setKanbanScope(projectID, projectID == "")
	}
}

func (s *chatState) setKanbanScope(projectID string, all bool) {
	ks := getKanbanState(s)
	kanbanMu.Lock()
	changed := ks.initialized && ks.scopeID != projectID
	leavingAutomation := s.tasksWindowView != "" && changed && ks.destination == "automations"
	kanbanMu.Unlock()
	if leavingAutomation && !prepareAutomationLeave(s, func() { s.setKanbanScope(projectID, all); s.invalidate() }) {
		return
	}
	if changed {
		s.closeTaskDetail()
		kanbanKeyboardClearSelection(s)
	}
	kanbanMu.Lock()
	if changed {
		if projectID != "" && ks.goalID != "" {
			valid := false
			for _, goal := range ks.board.Goals {
				if goal.ID == ks.goalID && goal.ProjectID == projectID {
					valid = true
					break
				}
			}
			if !valid {
				ks.goalID = ""
			}
		}
		ks.goalDetailOpen = false
		ks.selecting = false
		ks.selectedTaskIDs = nil
	}
	ks.scopeID = projectID
	ks.overview = all
	ks.initialized = true
	ks.loadID++
	ks.watchLoading = false
	ks.watchPending = false
	ks.board = KanbanBoard{}
	ks.err = ""
	ks.quickAddOpen = false
	kanbanMu.Unlock()
	s.startKanbanWatch(projectID)
	s.loadKanban(projectID)
}

func (s *chatState) refreshKanban() {
	ks := getKanbanState(s)
	kanbanMu.Lock()
	scope := ks.scopeID
	kanbanMu.Unlock()
	s.loadKanban(scope)
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
		st.loadID++
		st.watchID++
		st.watchLoading = false
		st.watchPending = false
		if st.watchStop != nil {
			st.watchStop()
			st.watchStop = nil
		}
	}
}

// startKanbanWatch 订阅看板数据变更。
func (s *chatState) startKanbanWatch(projectID string) {
	if s == nil || s.client == nil {
		return
	}
	ks := getKanbanState(s)
	if ks == nil {
		return
	}

	kanbanMu.Lock()
	ks.watchID++
	watchID := ks.watchID
	if ks.watchStop != nil {
		ks.watchStop()
		ks.watchStop = nil
	}
	kanbanMu.Unlock()

	stop := s.client.WatchKanban(func() {
		kanbanMu.Lock()
		valid := ks.initialized && ks.watchID == watchID && ks.scopeID == projectID
		kanbanMu.Unlock()
		if !valid {
			return
		}
		s.loadKanban(projectID)
	})

	kanbanMu.Lock()
	if ks.initialized && ks.watchID == watchID && ks.scopeID == projectID {
		ks.watchStop = stop
	} else {
		stop()
	}
	kanbanMu.Unlock()
}

// loadKanban 异步加载看板数据。
func (s *chatState) loadKanban(projectID string) {
	if s == nil || s.client == nil {
		return
	}
	ks := getKanbanState(s)
	if ks == nil {
		return
	}

	kanbanMu.Lock()
	if ks.scopeID != projectID {
		kanbanMu.Unlock()
		return
	}
	if ks.watchLoading && ks.loadingScope == projectID {
		ks.watchPending = true
		kanbanMu.Unlock()
		return
	}
	ks.loadID++
	loadID := ks.loadID
	ks.watchLoading = true
	ks.loadingScope = projectID
	ks.loading = true
	ks.err = ""
	kanbanMu.Unlock()
	s.invalidate()

	go func(pid string) {
		ctx := context.Background()
		board, err := s.client.KanbanBoard(ctx, pid)

		s.updateWorkspace(func() {
			kanbanMu.Lock()
			if ks.scopeID != pid || ks.loadID != loadID {
				kanbanMu.Unlock()
				return
			}
			ks.watchLoading = false
			pending := ks.watchPending
			ks.watchPending = false
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
			kanbanMu.Unlock()
			if pending {
				s.loadKanban(pid)
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

	kanbanMu.Lock()
	if ks.newTaskBusy {
		kanbanMu.Unlock()
		return
	}
	ks.newTaskBusy = true
	ks.newTaskErr = ""
	draftTitle, draftNotes, draftProject := ks.newTaskTitle, ks.newTaskNotes, ks.newTaskProjectID
	version := ks.newTaskVersion
	kanbanMu.Unlock()
	s.invalidate()
	go func() {
		ctx := context.Background()
		_, err := s.client.CreateTask(ctx, projectID, title, notes)
		s.updateWorkspace(func() {
			kanbanMu.Lock()
			ks.newTaskBusy = false
			if err != nil {
				ks.newTaskErr = err.Error()
			} else if ks.newTaskVersion == version && ks.newTaskProjectID == draftProject && ks.newTaskTitle == draftTitle && ks.newTaskNotes == draftNotes {
				ks.newTaskTitle = ""
				ks.newTaskNotes = ""
				ks.showNewTask = false
			}
			scope := ks.scopeID
			kanbanMu.Unlock()
			if err == nil {
				s.loadKanban(scope)
			}
		})
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

	kanbanMu.Lock()
	if ks.newGoalBusy {
		kanbanMu.Unlock()
		return
	}
	ks.newGoalBusy = true
	ks.newGoalErr = ""
	draftPrompt, draftProject := ks.newGoalPrompt, ks.newGoalProjectID
	version := ks.newGoalVersion
	kanbanMu.Unlock()
	s.invalidate()
	go func() {
		ctx := context.Background()
		_, err := s.client.CreateGoal(ctx, projectID, prompt)
		s.updateWorkspace(func() {
			kanbanMu.Lock()
			ks.newGoalBusy = false
			if err != nil {
				ks.newGoalErr = err.Error()
			} else if ks.newGoalVersion == version && ks.newGoalProjectID == draftProject && ks.newGoalPrompt == draftPrompt {
				ks.newGoalPrompt = ""
				ks.showNewGoal = false
			}
			scope := ks.scopeID
			kanbanMu.Unlock()
			if err == nil {
				s.loadKanban(scope)
			}
		})
	}()
}

// submitQuickAdd 提交待办列底部的快速添加，成功后清空草稿并保留输入框。
func (s *chatState) submitQuickAdd() {
	if s == nil || s.client == nil {
		return
	}
	ks := getKanbanState(s)
	if ks == nil {
		return
	}
	kanbanMu.Lock()
	projectID := ks.scopeID
	draft := ks.quickAddText
	title := strings.TrimSpace(draft)
	if projectID == "" || title == "" || ks.quickAddBusy {
		kanbanMu.Unlock()
		return
	}
	ks.quickAddBusy = true
	kanbanMu.Unlock()

	go func() {
		ctx := context.Background()
		_, err := s.client.CreateTask(ctx, projectID, title, "")
		kanbanMu.Lock()
		ks.quickAddBusy = false
		kanbanMu.Unlock()
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
			kanbanMu.Lock()
			if ks.scopeID == projectID && ks.quickAddText == draft {
				ks.quickAddText = ""
			}
			scope := ks.scopeID
			kanbanMu.Unlock()
			s.loadKanban(scope)
		})
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
	if !beginKanbanTask(ks, task.ID) {
		return
	}
	go func() {
		defer endKanbanTask(ks, task.ID)
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
		s.refreshKanban()
	}()
}

// interruptTaskToTodo 中断任务会话并把它移回待办；任务没有会话时只做移动。
func (s *chatState) interruptTaskToTodo(task KanbanTask) {
	if s == nil || s.client == nil {
		return
	}
	ks := getKanbanState(s)
	if !beginKanbanTask(ks, task.ID) {
		return
	}
	go func() {
		defer endKanbanTask(ks, task.ID)
		if task.ThreadID != "" {
			_, _ = s.client.Interrupt(context.Background(), task.ThreadID)
		}
		if _, err := s.client.MoveTask(context.Background(), task.ID, "todo", task.Revision); err != nil {
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
		s.refreshKanban()
	}()
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

	if !beginKanbanTask(ks, taskID) {
		return
	}
	go func() {
		defer endKanbanTask(ks, taskID)
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
		s.refreshKanban()
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
	if !beginKanbanTask(ks, taskID) {
		return
	}
	go func() {
		defer endKanbanTask(ks, taskID)
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
		s.refreshKanban()
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
	if !beginKanbanTask(ks, taskID) {
		return
	}
	go func() {
		defer endKanbanTask(ks, taskID)
		ctx := context.Background()
		updated, err := s.client.ReviewTask(ctx, taskID, decision, feedback, revision)
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
					s.refreshKanban()
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
				for i := range ks.board.Tasks {
					if ks.board.Tasks[i].ID == taskID {
						ks.board.Tasks[i] = updated
						break
					}
				}
				if ks.detailTaskID == taskID && decision == "accept" && updated.Status == "done" {
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
		if decision == "accept" && updated.Status == "done" {
			s.setToast("任务已完成")
		}
		s.refreshKanban()
	}()
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
	if ks.detailCancel != nil {
		ks.detailCancel()
		ks.detailCancel = nil
	}
	changed := ks.detailTaskID != task.ID || ks.detailThreadID != task.ThreadID
	ks.detailTaskID = task.ID
	ks.detailThreadID = task.ThreadID
	ks.detailRevision = task.Revision
	ks.detailLoading = (task.ThreadID != "")
	if changed {
		ks.detailErr = ""
		ks.detailMessages = nil
		ks.detailDiff = &diffPanelState{collapsed: make(map[string]bool)}
		ks.detailFiles = nil
		ks.detailDiffErr = ""
		ks.detailDiffTruncated = false
		ks.detailLogLoaded = false
		ks.detailDiffLoaded = false
	}
	if reset {
		ks.detailFeedback = ""
		ks.detailConflict = false
	}
	ks.detailLoadID++
	curLoadID := ks.detailLoadID
	targetTaskID := task.ID
	threadID := task.ThreadID
	projectID := ks.projectID
	loadLog := ks.detailTab == "session" && !ks.detailLogLoaded
	loadDiff := ks.detailTab == "diff" && !ks.detailDiffLoaded
	ks.detailLoading = threadID != "" && (loadLog || loadDiff)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	ks.detailCancel = cancel
	kanbanMu.Unlock()
	s.invalidate()

	if threadID == "" || s.client == nil || (!loadLog && !loadDiff) {
		cancel()
		return
	}

	go func(tid string, loadID int) {
		defer cancel()

		var msgs []Message
		var histErr error
		var diffResp ThreadDiff
		var diffErr error

		if loadLog {
			msgs, histErr = s.client.History(ctx, tid)
		} else {
			diffResp, diffErr = s.client.Diff(ctx, tid, "")
		}

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
			if loadLog && histErr != nil {
				ks.detailErr = histErr.Error()
			} else if loadLog {
				ks.detailErr = ""
				ks.detailMessages = msgs
				ks.detailLogLoaded = true
			}

			if !loadDiff {
				s.invalidate()
				return
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
				ks.detailDiffLoaded = true
			}
			s.invalidate()
		})
	}(threadID, curLoadID)
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
		s.refreshKanban()
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
		s.refreshKanban()
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
		s.refreshKanban()
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
		s.refreshKanban()
	}()
}
