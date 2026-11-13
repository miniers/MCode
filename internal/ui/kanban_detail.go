package mcodeui

import (
	"fmt"

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
	ks.detailLoading = false
	ks.detailErr = ""
	ks.detailMessages = nil
	ks.detailDiff = nil
	ks.detailFiles = nil
	ks.detailDiffErr = ""
	ks.detailFeedback = ""
	ks.detailConflict = false
	ks.detailLoadID++
	kanbanMu.Unlock()
	modalDragReset(modalDragKanbanTaskDetail)
	s.invalidate()
}

// renderTaskDetail 使用与宽屏详情栏相同的内容构建窄屏 sheet。
func renderTaskDetail(c *ui.Context, s *chatState, ks *kanbanState) {
	winWidth, winHeight := c.Size()
	sheetWidth, sheetHeight := min(settingsSheetWidth, winWidth-Space8), winHeight-Space8
	open := true
	ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
		backdrop.Absolute().Left(0).Top(0).Right(0).Bottom(0).Center().Background(ui.Color{}.Alpha(ScrimOpacity))
		panel.MaxWidth(winWidth - Space8).MaxHeight(winHeight - Space8).Padding(0)
		ui.Column(c.Key("kanban-detail-sheet")).Width(sheetWidth).Height(sheetHeight).
			Background(ThemeSurface(c)).Radius(RadiusLG).Transition(motionDialog(c)).Children(func() {
			renderTaskDetailContent(c, s, ks)
		})
	})
	if !open || c.Shortcut(0, ui.KeyEscape) {
		s.closeTaskDetail()
	}
}

func renderTaskDetailContent(c *ui.Context, s *chatState, ks *kanbanState) {
	kanbanMu.Lock()
	taskID, threadID, revision := ks.detailTaskID, ks.detailThreadID, ks.detailRevision
	tab, loading, detailErr, diffErr := ks.detailTab, ks.detailLoading, ks.detailErr, ks.detailDiffErr
	messages, dp, files, truncated := ks.detailMessages, ks.detailDiff, ks.detailFiles, ks.detailDiffTruncated
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
	ui.Column(c.Key("kanban-task-detail")).Label("kanban-task-detail").Fill().MinWidth(0).
		Gap(0).Children(func() {
		ui.Row(c.Key("kanban-detail-header")).FillWidth().Padding(Space4, Space5).AlignItems(ui.Center).Gap(Space3).Children(func() {
			if goalBack != "" {
				label := "← 返回目标"
				for _, goal := range goals {
					if goal.ID == goalBack {
						label = "← " + goal.Title
						break
					}
				}
				Button(c.Key("kanban-task-detail-back"), ButtonOpts{Text: label, Label: "返回目标", Variant: ButtonGhost, OnClick: s.closeTaskDetail})
			}
			ui.Text(c, "任务详情").FontWeight(600).TextColor(ThemePrimaryText(c)).Grow(1)
			if task != nil {
				busy := kanbanTaskBusy(ks, task.ID)
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
						Button(c.Key("kanban-detail-delete"), ButtonOpts{Text: "删除", Label: "kanban-detail-delete", Variant: ButtonDanger, Disabled: busy, OnClick: func() { ks.detailMoreOpen = false; ks.detailDeleteOpen = true }})
					})
				})
			}
			IconButton(c.Key("kanban-task-detail-close"), IconButtonProps{Icon: IconX(), Label: "kanban-task-detail-close", Tooltip: "关闭任务详情", Variant: ButtonGhost, OnClick: func() {
				kanbanMu.Lock()
				ks.goalDetailOpen = false
				kanbanMu.Unlock()
				s.closeTaskDetail()
			}})
		})
		if task == nil {
			ui.Text(c, "任务已不存在").TextColor(ThemeSecondaryText(c))
			return
		}
		ui.Text(c, task.Title).Padding(0, Space5).FontSize(uiFont(TypeTitle)).FontWeight(600).TextColor(ThemePrimaryText(c))
		if ks.detailDeleteOpen {
			ui.DialogBase(c.Key("kanban-detail-delete-dialog"), &ks.detailDeleteOpen, func(backdrop, panel ui.Element) {
				backdrop.Absolute().Left(0).Top(0).Right(0).Bottom(0).Center().Background(ui.Color{}.Alpha(ScrimOpacity))
				panel.MaxWidth(settingsSheetWidth).Padding(0)
				ui.Column(c.Key("kanban-detail-delete-card")).Padding(Space5).Gap(Space3).Background(ThemeSurface(c)).Radius(RadiusLG).Transition(motionDialog(c)).Children(func() {
					ui.Text(c, "确认删除该任务？此操作不可撤销").TextColor(ThemePrimaryText(c))
					ui.Row(c).Gap(Space2).Children(func() {
						Button(c.Key("kanban-detail-delete-cancel"), ButtonOpts{Text: "取消", Label: "kanban-detail-delete-cancel", OnClick: func() { ks.detailDeleteOpen = false }})
						Button(c.Key("kanban-detail-delete-confirm"), ButtonOpts{Text: "确认删除", Label: "kanban-detail-delete-confirm", Variant: ButtonDanger, Disabled: kanbanTaskBusy(ks, task.ID), OnClick: func() { ks.detailDeleteOpen = false; s.deleteKanbanTask(*task) }})
					})
				})
			})
		}
		if task.ThreadID != threadID || task.Revision != revision {
			s.loadTaskDetail(*task, false)
		}
		ui.Row(c.Key("kanban-detail-meta")).Padding(Space3, Space5).Gap(Space3).Children(func() {
			ui.Text(c, goalOutputStatusLabel(task.Status)).TextColor(ThemeSecondaryText(c))
			for _, goal := range goals {
				if goal.ID == task.GoalID {
					goal := goal
					Button(c.Key("kanban-detail-goal"), ButtonOpts{Text: goal.Title, Label: "打开目标", Variant: ButtonGhost, OnClick: func() {
						kanbanMu.Lock()
						ks.goalID = goal.ID
						ks.goalDetailOpen = true
						kanbanMu.Unlock()
						s.closeTaskDetail()
					}})
					break
				}
			}
			if task.ReviewStatus == "merge_failed" {
				reason := task.LastFailureReason
				if reason == "" {
					reason = task.ReviewFeedback
				}
				ui.Text(c, "合并失败: "+reason).TextColor(ThemeDanger(c))
			}
		})
		ui.Row(c.Key("kanban-detail-tabs")).Padding(0, Space5).Gap(Space2).Children(func() {
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
					if loading {
						LoadingIndicator(c, nil, "正在加载改动…", IconSM)
					} else if diffErr != "" {
						ui.Text(c, "改动加载失败: "+diffErr).TextColor(ThemeDanger(c))
						Button(c.Key("kanban-detail-diff-retry"), ButtonOpts{Text: "重试", Label: "重试改动", OnClick: func() { s.loadTaskDetail(*task, false) }})
					} else if len(files) == 0 {
						ui.Text(c, "无改动").TextColor(ThemeSecondaryText(c))
					} else {
						renderDiffFileList(c, s, dp, files)
						if truncated {
							ui.Text(c, "改动过多，已截断").TextColor(ThemeWarning(c))
						}
					}
				case "session":
					if task.ThreadID != "" {
						Button(c.Key("kanban-detail-execution-thread"), ButtonOpts{Text: "执行会话", Label: "执行会话", OnClick: func() { s.revealThread(task.ThreadID) }})
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
					for _, section := range []struct{ title, text string }{{"结果", task.ResultSummary}, {"需求", task.Notes}, {"验收标准", task.AcceptanceCriteria}, {"评审反馈", task.ReviewFeedback}, {"评审总结", task.ReviewSummary}, {"失败原因", task.LastFailureReason}, {"等待条件", task.WaitKind}, {"唤醒时间", task.WakeAt}} {
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
				ui.TextInput(c.Key("kanban-task-detail-feedback-input"), &ks.detailFeedback).Label("kanban-task-detail-feedback").Placeholder("评审反馈").FillWidth()
				busy := kanbanTaskBusy(ks, task.ID) || (conflict && boardErr != "")
				ui.Row(c.Key("kanban-task-detail-review-actions")).Gap(Space3).Children(func() {
					Button(c.Key("kanban-task-detail-reject"), ButtonOpts{Text: "退回修改", Label: "kanban-task-detail-reject", Variant: ButtonSecondary, Disabled: busy || !task.CanReview, OnClick: func() { s.reviewTask(task.ID, "reject", ks.detailFeedback, task.Revision) }})
					Button(c.Key("kanban-task-detail-accept"), ButtonOpts{Text: "接受", Label: "kanban-task-detail-accept", Variant: ButtonPrimary, Disabled: busy || !task.CanApprove, OnClick: func() { s.reviewTask(task.ID, "accept", ks.detailFeedback, task.Revision) }})
				})
			} else if kanbanActions(*task, false).primary == "开始" {
				Button(c.Key("kanban-task-detail-start"), ButtonOpts{Text: "开始", Label: "kanban-task-detail-start", Variant: ButtonPrimary, Loading: kanbanTaskBusy(ks, task.ID), Disabled: kanbanTaskBusy(ks, task.ID) || task.Blocked, OnClick: func() { s.performKanbanTarget(*task, "in_progress") }})
			}
		})
	})
}
