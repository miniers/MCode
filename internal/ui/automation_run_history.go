package mcodeui

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
)

// formatRunDuration 输出运行耗时，如“12s”“3m 05s”。
func formatRunDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	seconds := int(d.Round(time.Second) / time.Second)
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds >= 3600 {
		return fmt.Sprintf("%dh %02dm", seconds/3600, (seconds%3600)/60)
	}
	return fmt.Sprintf("%dm %02ds", seconds/60, seconds%60)
}

// automationRunStatus 把运行状态映射为中文文案与配色类别。
func automationRunStatus(status string) (text string, kind string) {
	switch status {
	case "running":
		return "运行中", "running"
	case "succeeded", "success", "completed":
		return "成功", "succeeded"
	case "failed", "error":
		return "失败", "failed"
	case "skipped":
		return "已跳过", "stopped"
	case "stopped", "canceled", "cancelled":
		return "已停止", "stopped"
	case "":
		return "未知", ""
	}
	return status, ""
}

// automationRunTriggerBadge 返回运行触发方式徽标；计划触发与已跳过记录不显示。
func automationRunTriggerBadge(run AutomationRun) string {
	switch run.Trigger {
	case "catch_up":
		if run.Status == "skipped" {
			return ""
		}
		if run.MissedCount > 0 {
			return fmt.Sprintf("补跑 · 跳过 %d 次", run.MissedCount)
		}
		return "补跑"
	case "manual":
		return "手动"
	}
	return ""
}

func renderAutomationRunHistory(c *ui.Context, s *chatState, as *automationState, isNew bool, selectedID string, selectedUnread int, includeArchived, runsLoading bool, runsErr string, runs []AutomationRun, runRead map[string]time.Time, runArchived map[string]bool, openRunID string) {
	// 运行记录区域（编辑态展示）
	if !isNew && selectedID != "" {
		ui.Box(c.Key("automation-runs-divider")).
			FillWidth().
			Height(Hairline).
			Background(ThemeDivider(c)).
			Margin(Space2, 0, Space2, 0)

		ui.Column(c.Key("automation-runs-section")).
			FillWidth().
			Gap(Space3).
			Children(func() {
				ui.Row(c.Key("automation-runs-header")).
					FillWidth().
					AlignItems(ui.Center).
					Gap(Space2).
					Children(func() {
						ui.Text(c, "运行记录").
							FontSize(TypeBody).
							FontWeight(600).
							TextColor(ThemePrimaryText(c)).
							Grow(1)

						// 全部标为已读：当前没有未读记录时置灰
						Button(c.Key("automation-runs-mark-all-read"), ButtonOpts{
							Text:     "全部标为已读",
							Label:    "全部标为已读",
							Disabled: selectedUnread == 0,
							OnClick: func() {
								s.markAllAutomationRunsRead(selectedID)
							},
							Variant: ButtonSecondary,
							Size:    ControlXS,
						})

						// 显示已归档开关：切换后按新条件重新拉取运行记录
						ui.Text(c, "显示已归档").
							FontSize(TypeCaption).
							TextColor(ThemeSecondaryText(c))
						includeArchivedVal := includeArchived
						ui.Switch(c.Key("automation-runs-include-archived"), &includeArchivedVal).
							Label("显示已归档运行记录")
						if includeArchivedVal != includeArchived {
							automationMu.Lock()
							as.includeArchived = includeArchivedVal
							as.openRunID = ""
							automationMu.Unlock()
							s.invalidate()
							s.loadAutomationRuns(selectedID)
						}

						IconButton(c.Key("automation-runs-refresh"), IconButtonProps{
							Icon:  IconRefreshCw(),
							Label: "刷新记录",
							OnClick: func() {
								s.loadAutomationRuns(selectedID)
							},
							Variant: ButtonGhost,
							Size:    ControlMD,
						})
					})

				if runsLoading && len(runs) == 0 {
					ui.Row(c.Key("runs-loading")).
						Gap(Space2).
						AlignItems(ui.Center).
						Children(func() {
							LoadingIndicator(c, nil, "正在读取运行记录…", IconSM)
						})
				} else if runsErr != "" {
					ui.Text(c, "加载记录失败: "+runsErr).
						FontSize(TypeCaption).
						TextColor(ThemeDanger(c))
				} else if len(runs) == 0 {
					ui.Text(c, "暂无运行记录").
						FontSize(TypeCaption).
						TextColor(ThemeSecondaryText(c))
				} else {
					ui.Column(c.Key("runs-list-col")).
						FillWidth().
						Gap(Space2).
						Children(func() {
							for _, r := range runs {
								runVal := r
								runUnread := automationRunUnread(runRead, runVal.ID)
								runIsArchived := runArchived[runVal.ID]
								runOpen := openRunID == runVal.ID
								// 展开/收起该条记录的详情；打开时把未读记录标为已读
								toggleRunDetail := func() {
									automationMu.Lock()
									if as.openRunID == runVal.ID {
										as.openRunID = ""
									} else {
										as.openRunID = runVal.ID
									}
									automationMu.Unlock()
									s.invalidate()
									if runUnread {
										s.markAutomationRunRead(selectedID, runVal.ID)
									}
								}
								runRow := ui.Column(c.Key("run-item-"+runVal.ID)).
									Label("运行记录 "+runVal.ID).
									FillWidth().
									Padding(Space2, Space3).
									Radius(RadiusSM).
									Gap(Space0)
								if runOpen {
									runRow.Background(ThemeSelected(c))
								} else if runRow.Hovered() {
									runRow.Background(ThemeOverlay(c, 0.08))
								} else {
									runRow.Background(ThemeOverlay(c, 0.04))
								}
								if runRow.Clicked() {
									toggleRunDetail()
								}
								runRow.Children(func() {
									runStatusText, runStatusKind := automationRunStatus(runVal.Status)
									statusColor := automationCardStatusColor(c, runStatusKind)
									pillWeight := 500
									if runUnread {
										pillWeight = 600
									}
									ui.Row(c.Key("run-item-top-" + runVal.ID)).
										FillWidth().
										AlignItems(ui.Center).
										Gap(Space2).
										Children(func() {
											// 未读标记：小圆点 + 加粗标题
											if runUnread {
												StatusDot(c, "run-unread-dot-"+runVal.ID, StatusAttention, "未读运行记录")
											}
											ui.Row(c.Key("run-status-pill-"+runVal.ID)).
												Padding(Space0, Space2).
												Radius(RadiusXS).
												Background(statusColor.Alpha(0.12)).
												Children(func() {
													ui.Text(c, runStatusText).
														FontSize(TypeCaption).
														FontWeight(pillWeight).
														TextColor(statusColor)
												})
											if runBadge := automationRunTriggerBadge(runVal); runBadge != "" {
												ui.Row(c.Key("run-trigger-pill-"+runVal.ID)).
													Padding(Space0, Space2).
													Radius(RadiusXS).
													Background(ThemeOverlay(c, 0.10)).
													Children(func() {
														ui.Text(c, runBadge).
															FontSize(TypeCaption).
															FontWeight(500).
															TextColor(ThemeSecondaryText(c))
													})
											}
											ui.Text(c, runVal.StartedAt.Local().Format("01-02 15:04:05")).
												FontSize(TypeCaption).
												TextColor(ThemeSecondaryText(c))
											if !runVal.FinishedAt.IsZero() {
												ui.Text(c, "耗时 "+formatRunDuration(runVal.FinishedAt.Sub(runVal.StartedAt))).
													FontSize(TypeCaption).
													TextColor(ThemeSecondaryText(c))
											}
											if runIsArchived {
												ui.Row(c.Key("run-archived-pill-"+runVal.ID)).
													Padding(Space0, Space2).
													Radius(RadiusXS).
													Background(ThemeOverlay(c, 0.10)).
													Children(func() {
														ui.Text(c, "已归档").
															FontSize(TypeCaption).
															FontWeight(500).
															TextColor(ThemeSecondaryText(c))
													})
											}
											ui.Box(c.Key("run-item-spacer-" + runVal.ID)).Grow(1)
											if runVal.ThreadID != "" {
												threadID := runVal.ThreadID
												Button(c.Key("run-open-thread-"+runVal.ID), ButtonOpts{
													Icon:  IconArrowUpRight(),
													Text:  "打开会话",
													Label: "run-open-thread-" + runVal.ID,
													OnClick: func() {
														s.revealThread(threadID)
													},
													Variant: ButtonSecondary,
													Size:    ControlXS,
												})
											}
											// 每行更多菜单：标为已读、查看详情、归档/取消归档、打开会话
											IconButton(c.Key("run-more-"+runVal.ID), IconButtonProps{
												Icon:    IconMoreHorizontal(),
												Label:   "运行记录更多操作 " + runVal.ID,
												Variant: ButtonGhost,
												Size:    ControlXS,
											}).Menu(func(m *ui.Menu) {
												if m.Item("标为已读").Disabled(!runUnread).Chosen() {
													s.markAutomationRunRead(selectedID, runVal.ID)
												}
												if m.Item("查看详情").Chosen() {
													toggleRunDetail()
												}
												m.Separator()
												if m.Item("归档").Disabled(runIsArchived).Chosen() {
													s.setAutomationRunArchived(runVal.ID, true)
												}
												if m.Item("取消归档").Disabled(!runIsArchived).Chosen() {
													s.setAutomationRunArchived(runVal.ID, false)
												}
												if runVal.ThreadID != "" {
													m.Separator()
													if m.Item("打开会话").Chosen() {
														s.revealThread(runVal.ThreadID)
													}
												}
											})
										})
									if runVal.Summary != "" {
										summary := ui.Text(c, runVal.Summary).FontSize(TypeCaption)
										if runUnread {
											summary.FontWeight(600).TextColor(ThemePrimaryText(c))
										} else {
											summary.FontWeight(500).TextColor(ThemeSecondaryText(c))
										}
										if !runOpen {
											summary.SingleLine()
										}
									}
									if runVal.Error != "" {
										errText := ui.Text(c, runVal.Error).FontSize(TypeCaption).TextColor(ThemeDanger(c))
										if !runOpen {
											errText.SingleLine()
										}
									}
									if runOpen {
										// 详情：记录 ID 与已读/归档状态，摘要与错误改为整段展示
										readLabel := "未读"
										if !runUnread {
											readLabel = "已读"
										}
										if runIsArchived {
											readLabel = readLabel + " · 已归档"
										}
										ui.Text(c, "记录 "+runVal.ID+" · "+readLabel).
											FontSize(TypeCaption).
											TextColor(ThemeSecondaryText(c)).
											SingleLine()
									}
								})
							}
						})
				}
			})
	}
}
