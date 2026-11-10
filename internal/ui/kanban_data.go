package mcodeui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"mcode/internal/git"
)

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
