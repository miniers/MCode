package mcodeui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"
)

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

func renderGoalDetailContent(c *ui.Context, s *chatState, ks *kanbanState) {
	kanbanMu.Lock()
	goalID, board, reviewGoalID := ks.goalID, ks.board, ks.planReviewGoalID
	replanID, feedback := ks.replanGoalID, ks.replanFeedback
	kanbanMu.Unlock()
	var goal *KanbanGoal
	for i := range board.Goals {
		if board.Goals[i].ID == goalID {
			goal = &board.Goals[i]
			break
		}
	}
	ui.Column(c.Key("kanban-goal-detail")).Label("kanban-goal-detail").Fill().Children(func() {
		ui.Row(c.Key("kanban-goal-detail-header")).FillWidth().Padding(Space4, Space5).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "目标详情").FontWeight(600).TextColor(ThemePrimaryText(c)).Grow(1)
			IconButton(c.Key("kanban-goal-detail-close"), IconButtonProps{Icon: IconX(), Label: "关闭目标详情", Tooltip: "关闭目标详情", Variant: ButtonGhost, OnClick: func() { kanbanMu.Lock(); ks.goalDetailOpen = false; kanbanMu.Unlock(); s.invalidate() }})
		})
		if goal == nil {
			ui.Text(c, "目标已不存在").TextColor(ThemeSecondaryText(c))
			return
		}
		ui.Text(c, goal.Title).Padding(0, Space5).FontSize(uiFont(TypeTitle)).FontWeight(600).TextColor(ThemePrimaryText(c))
		ui.Text(c, goalStatusBadgeText(goal.Status)).Padding(Space3, Space5).TextColor(ThemeSecondaryText(c))
		ui.Scroll(c.Key("kanban-goal-detail-scroll")).Grow(1).FillWidth().TrackScroll(&ks.goalDetailScroll).Children(func() {
			ui.Column(c.Key("kanban-goal-detail-body")).FillWidth().Padding(Space3, Space5).Gap(Space3).Children(func() {
				if goal.PlanSummary != "" {
					ui.Text(c, goal.PlanSummary).TextColor(ThemePrimaryText(c))
				}
				if goal.StatusReason != "" {
					ui.Text(c, goal.StatusReason).TextColor(ThemeSecondaryText(c))
				}
				var tasks []KanbanTask
				for _, task := range board.Tasks {
					if task.GoalID == goalID {
						tasks = append(tasks, task)
					}
				}
				numbers := make(map[string]int, len(tasks))
				for i, task := range tasks {
					numbers[task.ID] = i + 1
				}
				for i, layer := range planLayers(tasks) {
					ui.Text(c, fmt.Sprintf("第 %d 层", i+1)).TextColor(ThemeSecondaryText(c))
					for _, task := range layer {
						renderPlanReviewTaskCard(c, s, task, numbers)
						if task.AcceptanceCriteria != "" {
							ui.Text(c, task.AcceptanceCriteria).TextColor(ThemeSecondaryText(c))
						}
					}
				}
				ui.Text(c, "产物").FontWeight(600).TextColor(ThemePrimaryText(c))
				for _, output := range goalOutputs(board, goalID) {
					renderGoalOutputRow(c, s, ks, output)
				}
				if replanID == goalID {
					ui.TextInput(c.Key("goal-replan-input-"+goalID), &ks.replanFeedback).Label("goal-replan-input").FillWidth()
					Button(c.Key("goal-replan-submit-"+goalID), ButtonOpts{Text: "提交", Label: "提交重新规划", OnClick: func() { s.replanGoal(goalID, feedback, goal.Revision) }})
				}
			})
		})
		ui.Row(c.Key("kanban-goal-detail-actions")).Padding(Space4, Space5).Gap(Space2).Children(func() {
			if goal.Status == "plan_review" && reviewGoalID != goalID {
				Button(c.Key("goal-plan-review"), ButtonOpts{Text: "审阅计划", Label: "审阅计划", OnClick: func() { kanbanMu.Lock(); ks.planReviewGoalID = goalID; kanbanMu.Unlock(); s.invalidate() }})
			}
			if goal.Status == "plan_review" && reviewGoalID == goalID {
				Button(c.Key("goal-plan-start"), ButtonOpts{Text: "确认并开始", Label: "确认并开始", Variant: ButtonPrimary, OnClick: func() { s.startGoal(goalID, goal.Revision) }})
			}
			if goal.Status == "paused" {
				Button(c.Key("goal-resume"), ButtonOpts{Text: "继续", Label: "继续", OnClick: func() { s.startGoal(goalID, goal.Revision) }})
			}
			if goal.Status == "running" {
				Button(c.Key("goal-pause"), ButtonOpts{Text: "暂停", Label: "暂停", OnClick: func() { s.pauseGoal(goalID, goal.Revision) }})
			}
			if goal.Status == "plan_review" || goal.Status == "plan_failed" || goal.Status == "paused" {
				Button(c.Key("goal-replan-toggle"), ButtonOpts{Text: "重新规划", Label: "重新规划", OnClick: func() { kanbanMu.Lock(); ks.replanGoalID = goalID; kanbanMu.Unlock(); s.invalidate() }})
			}
			if goal.Status != "completed" && goal.Status != "cancelled" {
				Button(c.Key("goal-cancel"), ButtonOpts{Text: "取消目标", Label: "取消目标", Variant: ButtonDanger, OnClick: func() { s.cancelGoal(goalID, goal.Revision) }})
			}
		})
	})
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
						FontSize(uiFont(TypeBody)).
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
								FontSize(uiFont(TypeCaption)).
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
							FontSize(uiFont(TypeCaption)).
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
					FontSize(uiFont(TypeFootnote)).
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
			s.openTaskDetailFromGoal(task, true)
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
							FontSize(uiFont(TypeCaption)).
							FontWeight(600).
							TextColor(ThemeAccent(c))
					})

				ui.Text(c, task.Title).
					FontSize(uiFont(TypeBody)).
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
								FontSize(uiFont(TypeCaption)).
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
				FontSize(uiFont(TypeCaption)).
				TextColor(ThemeSecondaryText(c))
		}

		if task.Notes != "" {
			ui.Text(c, task.Notes).
				FontSize(uiFont(TypeCaption)).
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
	case "waiting":
		return "等待中"
	case "failed":
		return "失败"
	case "cancelled":
		return "已取消"
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
						FontSize(uiFont(TypeBody)).
						FontWeight(500).
						TextColor(ThemePrimaryText(c)).
						Grow(1)

					ui.Row(c.Key("kanban-goal-output-status-"+task.ID)).
						Padding(Space0, Space1).
						Radius(RadiusXS).
						Background(ThemeHover(c)).
						Children(func() {
							ui.Text(c, goalOutputStatusLabel(task.Status)).
								FontSize(uiFont(TypeCaption)).
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
								s.openTaskDetailFromGoal(task, true)
							},
						})
					}
				})

			if out.Summary != "" {
				ui.Text(c, out.Summary).
					FontSize(uiFont(TypeCaption)).
					TextColor(ThemeSecondaryText(c)).
					SingleLine()
			}
		})
}
