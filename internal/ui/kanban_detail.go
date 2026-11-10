package mcodeui

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// openTaskDetail 打开指定任务的详情浮窗，并异步加载其日志与差异。
func (s *chatState) openTaskDetail(task KanbanTask) {
	s.loadTaskDetail(task, true)
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
														LoadingIndicator(c, nil, "正在加载日志…", IconSM)
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
														LoadingIndicator(c, nil, "正在加载改动…", IconSM)
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
