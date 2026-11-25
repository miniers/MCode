package mcodeui

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

func (s *chatState) openTaskDetail(task KanbanTask) {
	s.openTaskDetailFromGoal(task, false)
}

func (s *chatState) openTaskDetailFromGoal(task KanbanTask, fromGoal bool) {
	ks := getKanbanState(s)
	kanbanMu.Lock()
	if fromGoal && ks.goalDetailOpen {
		ks.detailGoalID = ks.goalID
	} else {
		ks.detailGoalID = ""
		ks.goalDetailOpen = false
	}
	ks.detailMoreOpen = false
	ks.detailDeleteOpen = false
	ks.detailCancelOpen = false
	ks.detailFocusReturnID = ""
	ks.detailTab = "result"
	if task.Status == "review" || task.Column == "review" {
		ks.detailTab = "diff"
	}
	kanbanMu.Unlock()
	s.loadTaskDetail(task, true)
}

func (s *chatState) closeTaskDetail() {
	ks := getKanbanState(s)
	if ks == nil {
		return
	}
	kanbanMu.Lock()
	if ks.detailCancel != nil {
		ks.detailCancel()
		ks.detailCancel = nil
	}
	if !ks.goalDetailOpen {
		ks.detailFocusReturnID = ks.detailTaskID
	}
	ks.detailTaskID = ""
	ks.detailGoalID = ""
	ks.detailMoreOpen = false
	ks.detailDeleteOpen = false
	ks.detailCancelOpen = false
	ks.detailLoading = false
	ks.detailErr = ""
	ks.detailMessages = nil
	if ks.detailReview != nil {
		ks.detailReview.reset()
	}
	ks.detailFiles = nil
	ks.detailDiffErr = ""
	ks.detailFeedback = ""
	ks.detailConflict = false
	ks.detailLoadID++
	kanbanMu.Unlock()
	s.mu.Lock()
	s.stewardBoard.detailTaskID = ""
	s.mu.Unlock()
	modalDragReset(modalDragKanbanTaskDetail)
	s.invalidate()
}

// renderTaskDetail 使用与宽屏详情栏相同的内容构建窄屏 sheet。
func renderTaskDetail(c *ui.Context, s *chatState, ks *kanbanState) {
	renderTaskDetailDrawerSheet(c, taskDetailDrawerSheetOpts{
		SheetKey: "kanban-detail-sheet",
		OnClose:  s.closeTaskDetail,
		Content:  func() { renderTaskDetailContent(c, s, ks) },
	})
}

func renderTaskDetailContent(c *ui.Context, s *chatState, ks *kanbanState) {
	kanbanMu.Lock()
	taskID, threadID, revision := ks.detailTaskID, ks.detailThreadID, ks.detailRevision
	tab, loading, detailErr, diffErr := ks.detailTab, ks.detailLoading, ks.detailErr, ks.detailDiffErr
	if ks.detailReview == nil {
		ks.detailReview = newChangeReviewState()
	}
	messages, detailReview, files, truncated := ks.detailMessages, ks.detailReview, ks.detailFiles, ks.detailDiffTruncated
	conflict, boardLoading, boardErr := ks.detailConflict, ks.loading, ks.err
	tasks, goals := ks.board.Tasks, ks.board.Goals
	goalBack := ks.detailGoalID
	kanbanMu.Unlock()
	if taskID == "" {
		return
	}
	var task *KanbanTask
	for i := range tasks {
		if tasks[i].ID == taskID {
			task = &tasks[i]
			break
		}
	}
	var leading func()
	if goalBack != "" {
		label := "← 返回目标"
		for _, goal := range goals {
			if goal.ID == goalBack {
				label = "← " + goal.Title
				break
			}
		}
		leading = func() {
			Button(c.Key("kanban-task-detail-back"), ButtonOpts{Text: label, Label: "返回目标", Variant: ButtonGhost, OnClick: s.closeTaskDetail})
		}
	}
	closeDetail := func() {
		kanbanMu.Lock()
		ks.goalDetailOpen = false
		kanbanMu.Unlock()
		s.closeTaskDetail()
	}
	ui.Column(c.Key("kanban-task-detail")).Label("kanban-task-detail").Fill().MinWidth(0).
		Gap(0).Children(func() {
		if task == nil {
			renderTaskDetailDrawerHeader(c, taskDetailDrawerHeaderOpts{
				Key:           "kanban-detail-header",
				Leading:       leading,
				FallbackTitle: "任务详情",
				CloseKey:      "kanban-task-detail-close",
				CloseLabel:    "kanban-task-detail-close",
				CloseTooltip:  "关闭任务详情",
				OnClose:       closeDetail,
			})
			ui.Column(c.Key("kanban-task-detail-empty-wrap")).Grow(1).FillWidth().Center().Padding(Space5).Children(func() {
				renderTaskDetailDrawerEmpty(c, "kanban-task-detail-empty", "任务已不存在")
			})
			return
		}
		busy := kanbanTaskBusy(ks, task.ID)
		openExecutionThread := func(target KanbanTask) {
			if target.ThreadID == "" {
				return
			}
			if s.tasksWindowView == "" {
				s.closeTaskDetail()
				if isStewardTaskOrigin(target.Origin) {
					openStewardTask(s, stewardResult{
						Task:    target.ID,
						Thread:  target.ThreadID,
						Status:  target.Status,
						Summary: target.ResultSummary,
					})
					return
				}
			}
			s.revealThread(target.ThreadID)
		}
		var onOpenThread func()
		if task.ThreadID != "" {
			curTask := *task
			onOpenThread = func() { openExecutionThread(curTask) }
		}
		renderTaskDetailDrawerHeader(c, taskDetailDrawerHeaderOpts{
			Key:           "kanban-detail-header",
			Leading:       leading,
			OriginSteward: isStewardTaskOrigin(task.Origin),
			StatusLabel:   taskBadgeLabel(*task),
			StatusColor:   kanbanStatusColor(c, task.Status),
			ProjectName:   projectNameFor(s, task.ProjectID),
			ThreadID:      task.ThreadID,
			OnOpenThread:  onOpenThread,
			ExtraActions: func() {
				if s.tasksWindowView == "" && s.openTasksWindowTargetFn != nil {
					curTask := *task
					Button(c.Key("kanban-detail-open-tasks-window"), ButtonOpts{
						Text:    "在任务窗打开",
						Label:   "在任务窗打开",
						Variant: ButtonGhost,
						Size:    ControlSM,
						OnClick: func() {
							s.closeTaskDetail()
							s.openTasksWindowTarget(taskCenterTarget{
								Kind:      "task",
								ID:        curTask.ID,
								ProjectID: curTask.ProjectID,
								Title:     curTask.Title,
							})
						},
					})
				}
				more := IconButton(c.Key("kanban-detail-more"), IconButtonProps{Icon: IconMoreHorizontal(), Label: "任务更多操作", Variant: ButtonGhost, OnClick: func() { ks.detailMoreOpen = true }})
				appPopover(c.Key("kanban-detail-more-popover"), more, &ks.detailMoreOpen, nil, func() {
					popoverCard(c, ui.Column(c.Key("kanban-detail-more-menu"))).Padding(Space2).Gap(Space1).Children(func() {
						for _, opt := range kanbanActions(*task, busy).menu {
							opt := opt
							if opt.status == "in_progress" && task.CanStart || opt.status == "done" && task.Status == "review" {
								continue
							}
							Button(c.Key("kanban-detail-move-"+opt.status), ButtonOpts{Text: "移至" + opt.label, Label: "kanban-detail-move-" + opt.status, Variant: ButtonGhost, OnClick: func() { ks.detailMoreOpen = false; s.performKanbanTarget(*task, opt.status) }})
						}
						Button(c.Key("kanban-detail-copy-id"), ButtonOpts{Text: "复制任务 ID", Variant: ButtonGhost, OnClick: func() { c.WriteClipboard(task.ID); ks.detailMoreOpen = false }})
						if kanbanTaskCanCancel(task.Status) {
							Button(c.Key("kanban-detail-cancel"), ButtonOpts{Text: "取消任务", Label: "kanban-detail-cancel", Variant: ButtonGhost, Disabled: busy, OnClick: func() { ks.detailMoreOpen = false; ks.detailCancelOpen = true }})
						}
						Button(c.Key("kanban-detail-delete"), ButtonOpts{Text: "删除", Label: "kanban-detail-delete", Variant: ButtonDanger, Disabled: busy, OnClick: func() { ks.detailMoreOpen = false; ks.detailDeleteOpen = true }})
					})
				})
			},
			CloseKey:     "kanban-task-detail-close",
			CloseLabel:   "kanban-task-detail-close",
			CloseTooltip: "关闭任务详情",
			OnClose:      closeDetail,
		})
		if ks.detailEditField == "title" && ks.detailEditTaskID == task.ID {
			in := ui.TextInput(c.Key("kanban-detail-title-edit"), &ks.detailEditTitle).Label("任务标题").FillWidth().AutoFocus()
			in.OnTextKey(func(k ui.TextKey) bool {
				if k.Key == ui.KeyEscape {
					ks.detailEditField = ""
					s.invalidate()
					return true
				}
				if k.Key == ui.KeyEnter && k.Mods == 0 {
					ks.detailEditTitle = k.Text()
					s.commitKanbanDetailEdit(*task, "title")
					return true
				}
				return false
			})
			focused := in.Focused()
			if focused && !ks.detailEditHadFocus {
				in.SetTextSelection(0, len([]rune(ks.detailEditTitle)))
				ks.detailEditHadFocus = true
			} else if focused {
				ks.detailEditHadFocus = true
			} else if ks.detailEditHadFocus {
				ks.detailEditHadFocus = false
				s.commitKanbanDetailEdit(*task, "title")
			}
		} else {
			title := ui.Text(c.Key("kanban-detail-title"), task.Title).Padding(0, Space5).FontSize(uiFont(TypeTitle)).FontWeight(600).TextColor(ThemePrimaryText(c)).Focusable()
			if title.Clicked() && s.kanbanEditAvailable() {
				ks.detailEditField, ks.detailEditTaskID = "title", task.ID
				ks.detailEditTitle, ks.detailEditOrigTitle = task.Title, task.Title
				ks.detailEditHadFocus = false
			}
		}
		RenderConfirmDialog(c, ConfirmDialog{
			Key:             "kanban-detail-delete-dialog",
			Open:            ks.detailDeleteOpen,
			Tone:            ConfirmDanger,
			Title:           "删除该任务？",
			Body:            "此操作不可撤销",
			ConfirmLabel:    "删除",
			ConfirmA11y:     "kanban-detail-delete-confirm",
			CancelLabel:     "取消",
			CancelA11y:      "kanban-detail-delete-cancel",
			ConfirmDisabled: kanbanTaskBusy(ks, task.ID),
			OnConfirm:       func() { ks.detailDeleteOpen = false; s.deleteKanbanTask(*task) },
			OnCancel:        func() { ks.detailDeleteOpen = false },
		})
		renderCancelTaskConfirmDialog(c, "kanban-detail-cancel-dialog", ks.detailCancelOpen && kanbanTaskCanCancel(task.Status), kanbanTaskBusy(ks, task.ID),
			func() { ks.detailCancelOpen = false; s.cancelKanbanTask(task.ID, task.Revision) },
			func() { ks.detailCancelOpen = false },
		)
		if task.ThreadID != threadID || task.Revision != revision {
			s.loadTaskDetail(*task, false)
		}
		var goalLink *KanbanGoal
		for i := range goals {
			if goals[i].ID == task.GoalID {
				goalLink = &goals[i]
				break
			}
		}
		if goalLink != nil || task.ReviewStatus == "merge_failed" {
			ui.Row(c.Key("kanban-detail-meta")).Padding(Space3, Space5).Gap(Space3).Children(func() {
				if goalLink != nil {
					goal := *goalLink
					Button(c.Key("kanban-detail-goal"), ButtonOpts{Text: goal.Title, Label: "打开目标", Variant: ButtonGhost, OnClick: func() {
						kanbanMu.Lock()
						ks.goalID = goal.ID
						ks.goalDetailOpen = true
						kanbanMu.Unlock()
						s.closeTaskDetail()
					}})
				}
				if task.ReviewStatus == "merge_failed" {
					reason := task.LastFailureReason
					if reason == "" {
						reason = task.ReviewFeedback
					}
					ui.Text(c, "合并失败: "+reason).TextColor(ThemeDanger(c))
				}
			})
		}
		ui.Row(c.Key("kanban-detail-tabs")).Padding(Space3, Space5, 0, Space5).Gap(Space2).Children(func() {
			for _, item := range []struct{ id, label string }{{"result", "结果"}, {"diff", "改动"}, {"session", "会话"}} {
				item := item
				variant := ButtonGhost
				if tab == item.id {
					variant = ButtonSecondary
				}
				Button(c.Key("kanban-detail-tab-"+item.id), ButtonOpts{Text: item.label, Label: "kanban-detail-tab-" + item.id, Variant: variant, OnClick: func() {
					kanbanMu.Lock()
					ks.detailTab = item.id
					kanbanMu.Unlock()
					s.loadTaskDetail(*task, false)
					s.invalidate()
				}})
			}
		})
		ui.Scroll(c.Key("kanban-task-detail-scroll-" + tab)).Grow(1).FillWidth().Children(func() {
			ui.Column(c.Key("kanban-task-detail-body-"+tab)).FillWidth().Padding(Space3, Space5).Gap(Space3).Transition(motionFeedback(c)).Children(func() {
				if conflict {
					ui.Text(c, "任务已被其他地方修改，请核对刷新后的任务与改动后重试").TextColor(ThemeDanger(c))
					if boardLoading {
						ui.Text(c, "正在刷新任务…").TextColor(ThemeSecondaryText(c))
					} else if boardErr != "" {
						ui.Text(c, "刷新失败: "+boardErr).TextColor(ThemeDanger(c))
						Button(c.Key("kanban-task-detail-refresh"), ButtonOpts{Text: "重新刷新", Label: "kanban-task-detail-refresh", Variant: ButtonSecondary, OnClick: s.refreshKanban})
					}
				}
				switch tab {
				case "diff":
					renderChangeReviewDiff(c, "kanban-detail-diff", detailReview, changeReviewDiffOpts{
						Files:              files,
						Loading:            loading,
						LoadingText:        "正在加载改动…",
						Err:                diffErr,
						ErrPrefix:          "改动加载失败: ",
						Truncated:          truncated,
						EmptyText:          "无改动",
						OnRetry:            func() { s.loadTaskDetail(*task, false) },
						RetryKey:           "kanban-detail-diff-retry",
						RetryLabel:         "重试改动",
						MaxExpandedDefault: changeReviewDefaultMaxExpand,
						ShowReadToggle:     true,
						ScopeKey:           taskID + ":" + threadID,
						Invalidate:         s.invalidate,
					})
				case "session":
					if task.ThreadID != "" {
						curTask := *task
						Button(c.Key("kanban-detail-execution-thread"), ButtonOpts{Text: "执行会话", Label: "执行会话", OnClick: func() { openExecutionThread(curTask) }})
					}
					if task.ReviewThreadID != "" {
						Button(c.Key("kanban-detail-review-thread"), ButtonOpts{Text: "评审会话", Label: "评审会话", OnClick: func() { s.revealThread(task.ReviewThreadID) }})
					}
					if loading {
						LoadingIndicator(c, nil, "正在加载日志…", IconSM)
					} else if detailErr != "" {
						ui.Text(c, "日志加载失败: "+detailErr).TextColor(ThemeDanger(c))
						Button(c.Key("kanban-detail-log-retry"), ButtonOpts{Text: "重试", Label: "重试日志", OnClick: func() { s.loadTaskDetail(*task, false) }})
					} else {
						for i, msg := range messages {
							ui.Text(c.Key(fmt.Sprintf("kanban-detail-msg-%d", i)), msg.Text).TextColor(ThemePrimaryText(c))
						}
					}
				default:
					if ks.detailEditField == "notes" && ks.detailEditTaskID == task.ID {
						in := ui.TextArea(c.Key("kanban-detail-notes-edit"), &ks.detailEditNotes).Label("任务备注").Lines(3, 8).FillWidth().AutoFocus()
						in.OnTextKey(func(k ui.TextKey) bool {
							if k.Key == ui.KeyEscape {
								ks.detailEditField = ""
								s.invalidate()
								return true
							}
							if k.Key == ui.KeyEnter && k.Mods == ui.Cmd {
								s.commitKanbanDetailEdit(*task, "notes")
								return true
							}
							return false
						})
						focused := in.Focused()
						if focused {
							ks.detailEditHadFocus = true
						} else if ks.detailEditHadFocus {
							ks.detailEditHadFocus = false
							s.commitKanbanDetailEdit(*task, "notes")
						}
					} else if strings.TrimSpace(task.Notes) != "" {
						notes := ui.Text(c.Key("kanban-detail-notes"), task.Notes).Focusable()
						if notes.Clicked() && s.kanbanEditAvailable() {
							ks.detailEditField, ks.detailEditTaskID = "notes", task.ID
							ks.detailEditNotes, ks.detailEditOrigNotes = task.Notes, task.Notes
							ks.detailEditHadFocus = false
						}
					}
					for _, section := range []struct{ title, text string }{{"结果", task.ResultSummary}, {"验收标准", task.AcceptanceCriteria}, {"评审反馈", task.ReviewFeedback}, {"评审总结", task.ReviewSummary}, {"失败原因", task.LastFailureReason}, {"等待条件", task.WaitKind}, {"唤醒时间", task.WakeAt}} {
						if section.text != "" {
							ui.Text(c, section.title+": "+section.text).TextColor(ThemePrimaryText(c))
						}
					}
					for _, id := range task.DependsOn {
						found := false
						for _, dep := range tasks {
							if dep.ID == id {
								dep := dep
								found = true
								Button(c.Key("kanban-detail-dep-"+id), ButtonOpts{Text: dep.Title, Label: dep.Title, Variant: ButtonGhost, OnClick: func() { s.openTaskDetail(dep) }})
								break
							}
						}
						if !found {
							ui.Text(c, "缺失的前置任务: "+id).TextColor(ThemeDanger(c))
						}
					}
				}
			})
		})
		ui.Column(c.Key("kanban-task-detail-footer")).FillWidth().Padding(Space4, Space5).Gap(Space3).Children(func() {
			if task.CanReview || task.Column == "review" {
				busy := kanbanTaskBusy(ks, task.ID) || (conflict && boardErr != "")
				renderChangeReviewApprovalBar(c, "kanban-task-detail", changeReviewApprovalBarOpts{
					ShowFeedback:        true,
					FeedbackKey:         "kanban-task-detail-feedback-input",
					FeedbackLabel:       "kanban-task-detail-feedback",
					FeedbackPlaceholder: "评审反馈",
					Feedback:            &ks.detailFeedback,
					Secondary: &changeReviewApprovalAction{
						Key:      "kanban-task-detail-reject",
						Text:     "退回修改",
						Label:    "kanban-task-detail-reject",
						Variant:  ButtonSecondary,
						Disabled: busy || !task.CanReview,
						OnClick:  func() { s.reviewTask(task.ID, "reject", ks.detailFeedback, task.Revision) },
					},
					Primary: &changeReviewApprovalAction{
						Key:      "kanban-task-detail-accept",
						Text:     "接受",
						Label:    "kanban-task-detail-accept",
						Variant:  ButtonPrimary,
						Disabled: busy || !task.CanApprove,
						OnClick:  func() { s.reviewTask(task.ID, "accept", ks.detailFeedback, task.Revision) },
					},
				})
			} else if kanbanActions(*task, false).primary == "开始" {
				Button(c.Key("kanban-task-detail-start"), ButtonOpts{Text: "开始", Label: "kanban-task-detail-start", Variant: ButtonPrimary, Loading: kanbanTaskBusy(ks, task.ID), Disabled: kanbanTaskBusy(ks, task.ID) || task.Blocked, OnClick: func() { s.performKanbanTarget(*task, "in_progress") }})
			}
		})
	})
}

func renderCancelTaskConfirmDialog(c *ui.Context, key string, open, busy bool, onConfirm, onCancel func()) {
	RenderConfirmDialog(c, ConfirmDialog{
		Key:             key,
		Open:            open,
		Tone:            ConfirmDanger,
		Title:           "取消任务",
		Body:            "会停止正在运行的执行会话",
		ConfirmLabel:    "取消任务",
		ConfirmA11y:     key + "-confirm",
		CancelLabel:     "取消",
		CancelA11y:      key + "-cancel",
		ConfirmDisabled: busy,
		OnConfirm:       onConfirm,
		OnCancel:        onCancel,
	})
}
