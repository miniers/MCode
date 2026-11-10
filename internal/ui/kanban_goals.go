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
	ui.Column(c.Key("kanban-goal-detail")).Label("kanban-goal-detail").Fill().Padding(Space5).Gap(Space3).Transition(motionSidePanel(c, false)).Children(func() {
		Button(c.Key("kanban-goal-detail-close"), ButtonOpts{Text: "关闭", Label: "关闭目标详情", Variant: ButtonGhost, OnClick: func() { kanbanMu.Lock(); ks.goalDetailOpen = false; kanbanMu.Unlock(); s.invalidate() }})
		if goal == nil {
			ui.Text(c, "目标已不存在").TextColor(ThemeSecondaryText(c))
			return
		}
		ui.Text(c, goal.Title).FontSize(TypeTitle).FontWeight(600).TextColor(ThemePrimaryText(c))
		ui.Text(c, goalStatusBadgeText(goal.Status)).TextColor(ThemeSecondaryText(c))
		ui.Scroll(c.Key("kanban-goal-detail-scroll")).Grow(1).FillWidth().Children(func() {
			ui.Column(c.Key("kanban-goal-detail-body")).FillWidth().Gap(Space3).Children(func() {
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
		ui.Row(c.Key("kanban-goal-detail-actions")).Gap(Space2).Children(func() {
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
											LoadingIndicator(c, nil, "规划中，请稍候…", IconLG)
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
