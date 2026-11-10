package mcodeui

import (
	"fmt"
	"sort"
	"time"

	"github.com/egoist/mygo/ui"
)

var automationHistoryPopovers = make(map[*automationState]*struct{ filter, more bool })

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

func automationRunStatus(status string) (string, string) {
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
	default:
		return status, ""
	}
}

func automationRunTriggerBadge(run AutomationRun) string {
	if run.Status == "skipped" {
		return "已跳过"
	}
	switch run.Trigger {
	case "catch_up":
		if run.MissedCount > 0 {
			return fmt.Sprintf("补跑 · 跳过 %d 次", run.MissedCount)
		}
		return "补跑"
	case "manual":
		return "手动"
	case "skipped":
		return "已跳过"
	}
	return ""
}

func automationRunDate(at, now time.Time) string {
	at, now = at.Local(), now.Local()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	day := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, now.Location())
	if day.Equal(today) {
		return "今天"
	}
	if day.Equal(today.AddDate(0, 0, -1)) {
		return "昨天"
	}
	return at.Format("2006-01-02")
}

func automationRunDuration(run AutomationRun) string {
	if run.FinishedAt.IsZero() {
		if run.Status == "running" {
			return "运行中"
		}
		return "—"
	}
	return formatRunDuration(run.FinishedAt.Sub(run.StartedAt))
}

func openAutomationRun(as *automationState, runID string) {
	as.detailTab = 0
	as.openRunID = runID
}

func renderAutomationRunHistory(c *ui.Context, s *chatState, as *automationState, isNew bool, selectedID string, selectedUnread int, includeArchived, runsLoading bool, runsErr string, runs []AutomationRun, runRead map[string]time.Time, runArchived map[string]bool, openRunID string) {
	if isNew || selectedID == "" {
		return
	}
	automationMu.Lock()
	view := automationRunsViewFor(as, selectedID)
	popovers := automationHistoryPopovers[as]
	if popovers == nil {
		popovers = &struct{ filter, more bool }{}
		automationHistoryPopovers[as] = popovers
	}
	failedOnly, unreadOnly := view.failedOnly, view.unreadOnly
	errText, retry := view.err, view.retry
	automationMu.Unlock()
	ui.Column(c.Key("automation-runs-section")).FillWidth().Gap(Space3).Children(func() {
		if errText != "" {
			ui.Row(c.Key("automation-runs-action-error")).FillWidth().Gap(Space2).Children(func() {
				ui.Text(c, errText).FontSize(TypeCaption).TextColor(ThemeDanger(c)).Grow(1)
				Button(c.Key("automation-runs-retry"), ButtonOpts{Text: "重试", Label: "重试", OnClick: retry, Variant: ButtonSecondary, Size: ControlXS})
			})
		}
		var selected *AutomationRun
		for i := range runs {
			if runs[i].ID == openRunID && openRunID != "" {
				selected = &runs[i]
				break
			}
		}
		if selected != nil {
			run := *selected
			ui.Column(c.Key("automation-run-detail-" + run.ID)).FillWidth().Gap(Space3).Transition(motionTransition(c, ui.ElementTransition{Duration: DurBase, Enter: &ui.Motion{Opacity: 0}})).Children(func() {
				ui.Row(c.Key("run-detail-header")).FillWidth().AlignItems(ui.Center).Gap(Space2).Children(func() {
					Button(c.Key("run-detail-back"), ButtonOpts{Text: "← 运行记录", Label: "返回运行记录", OnClick: func() { automationMu.Lock(); as.openRunID = ""; automationMu.Unlock(); s.invalidate() }, Variant: ButtonGhost, Size: ControlXS})
					status, kind := automationRunStatus(run.Status)
					ui.Text(c, status).TextColor(automationCardStatusColor(c, kind)).Grow(1)
					Button(c.Key("run-again"), ButtonOpts{Text: "再次运行", Disabled: as.runningIDs[selectedID], OnClick: func() { s.requestAutomationRun(selectedID) }, Variant: ButtonSecondary, Size: ControlXS})
					if run.ThreadID != "" {
						Button(c.Key("run-open-thread-"+run.ID), ButtonOpts{Text: "打开会话", Label: "run-open-thread-" + run.ID, OnClick: func() { s.revealThread(run.ThreadID) }, Variant: ButtonSecondary, Size: ControlXS})
					}
					IconButton(c.Key("run-detail-more"), IconButtonProps{Icon: IconMoreHorizontal(), Label: "运行详情更多操作", Variant: ButtonGhost, Size: ControlXS}).Menu(func(m *ui.Menu) {
						if m.Item("复制运行 ID").Chosen() {
							c.WriteClipboard(run.ID)
						}
					})
				})
				end := "运行中"
				if !run.FinishedAt.IsZero() {
					end = run.FinishedAt.Local().Format("2006-01-02 15:04:05")
				} else if run.Status != "running" {
					end = "—"
				}
				ui.Text(c, run.StartedAt.Local().Format("2006-01-02 15:04:05")+" → "+end+" · "+automationRunDuration(run)).FontSize(TypeCaption).TextColor(ThemeSecondaryText(c))
				if badge := automationRunTriggerBadge(run); badge != "" {
					ui.Text(c, badge).FontSize(TypeCaption).TextColor(ThemeSecondaryText(c))
				} else if run.Trigger == "scheduled" {
					ui.Text(c, "计划").FontSize(TypeCaption).TextColor(ThemeSecondaryText(c))
				}
				for _, part := range []struct{ key, label, text string }{{"summary", "摘要", run.Summary}, {"error", "错误", run.Error}} {
					if part.text == "" {
						continue
					}
					part := part
					ui.Column(c.Key("run-detail-" + part.key)).FillWidth().Gap(Space1).Children(func() {
						ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
							if part.key == "error" {
								ui.Icon(c, IconCircleX()).Size(IconSM, IconSM).TextColor(ThemeDanger(c))
							}
							ui.Text(c, part.label).Grow(1).FontSize(TypeCaption).TextColor(ThemeSecondaryText(c))
							CopyButton(c, CopyButtonProps{Key: "run-copy-" + part.key, Label: "复制" + part.label, Text: "复制", Value: func() string { return part.text }})
						})
						ui.Text(c, part.text).FillWidth().Shrink(1).FontSize(TypeBody).TextColor(ThemePrimaryText(c)).Selectable()
					})
				}
			})
			return
		}
		ui.Row(c.Key("automation-runs-header")).FillWidth().AlignItems(ui.Center).Gap(Space2).Children(func() {
			filterLabel := "全部"
			if failedOnly {
				filterLabel = "失败"
			}
			if unreadOnly || includeArchived {
				filterLabel += " •"
			}
			filter := Button(c.Key("automation-runs-filter-trigger"), ButtonOpts{Text: filterLabel + " ▾", Label: "筛选运行记录", Variant: ButtonSecondary, Size: ControlXS, OnClick: func() { popovers.filter = true }})
			appPopover(c.Key("automation-runs-filter-popover"), filter, &popovers.filter, nil, func() {
				popoverCard(c, ui.Column(c.Key("automation-runs-filters"))).Padding(Space2).Gap(Space1).Children(func() {
					for _, f := range []struct {
						label  string
						active bool
						click  func()
					}{
						{"全部", !failedOnly, func() { view.failedOnly = false }}, {"失败", failedOnly, func() { view.failedOnly = true }}, {"未读", unreadOnly, func() { view.unreadOnly = !view.unreadOnly }},
					} {
						f := f
						variant := ButtonGhost
						if f.active {
							variant = ButtonSecondary
						}
						Button(c.Key("automation-runs-filter-"+f.label), ButtonOpts{Text: f.label, Label: "筛选" + f.label, OnClick: func() { automationMu.Lock(); f.click(); automationMu.Unlock(); s.invalidate() }, Variant: variant, Size: ControlXS})
					}
					value := includeArchived
					ui.Switch(c.Key("automation-runs-include-archived"), &value).Label("显示已归档运行记录")
					ui.Text(c, "显示已归档").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
					if value != includeArchived {
						automationMu.Lock()
						as.includeArchived = value
						as.openRunID = ""
						automationMu.Unlock()
						s.invalidate()
						s.loadAutomationRuns(selectedID)
					}
				})
			})
			ui.Box(c).Grow(1)
			more := IconButton(c.Key("automation-runs-more"), IconButtonProps{Icon: IconMoreHorizontal(), Label: "运行记录更多操作", Variant: ButtonGhost, Size: ControlXS, OnClick: func() { popovers.more = true }})
			appPopover(c.Key("automation-runs-more-popover"), more, &popovers.more, nil, func() {
				popoverCard(c, ui.Column(c.Key("automation-runs-more-menu"))).Padding(Space2).Gap(Space1).Children(func() {
					Button(c.Key("automation-runs-mark-all-read"), ButtonOpts{Text: "全部标为已读", Label: "全部标为已读", Disabled: selectedUnread == 0, OnClick: func() { popovers.more = false; s.markAllAutomationRunsRead(selectedID) }, Variant: ButtonGhost, Size: ControlXS})
					Button(c.Key("automation-runs-refresh"), ButtonOpts{Text: "刷新记录", Label: "刷新记录", OnClick: func() { popovers.more = false; s.loadAutomationRuns(selectedID) }, Variant: ButtonGhost, Size: ControlXS})
				})
			})
		})
		if runsLoading && len(runs) == 0 {
			LoadingIndicator(c, nil, "正在读取运行记录…", IconSM)
			return
		}
		if runsErr != "" {
			ui.Text(c, "加载记录失败: "+runsErr).FontSize(TypeCaption).TextColor(ThemeDanger(c))
			return
		}
		if len(runs) == 0 {
			ui.Text(c, "暂无运行记录").FontSize(TypeCaption).TextColor(ThemeSecondaryText(c))
			return
		}
		ordered := append([]AutomationRun(nil), runs...)
		sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].StartedAt.After(ordered[j].StartedAt) })
		if len(ordered) > 100 {
			ordered = ordered[:100]
		}
		shown, lastDay := 0, ""
		ui.Column(c.Key("runs-list-col")).FillWidth().Gap(Space0).Transition(motionTransition(c, ui.ElementTransition{Duration: DurBase, Enter: &ui.Motion{Opacity: 0}})).Children(func() {
			for _, run := range ordered {
				run := run
				unread := automationRunUnread(runRead, run.ID)
				archived := runArchived[run.ID]
				_, kind := automationRunStatus(run.Status)
				if (failedOnly && kind != "failed") || (unreadOnly && !unread) || (archived && !includeArchived) {
					continue
				}
				shown++
				day := automationRunDate(run.StartedAt, time.Now())
				if day != lastDay {
					ui.Text(c, day).FontSize(TypeFootnote).FontWeight(weightFor(c, 500)).TextColor(ThemeSecondaryText(c)).Margin(Space5, 0, Space2, 0)
					lastDay = day
				}
				open := func() {
					automationMu.Lock()
					openAutomationRun(as, run.ID)
					automationMu.Unlock()
					s.invalidate()
					if unread {
						s.markAutomationRunRead(selectedID, run.ID)
					}
				}
				row := ui.Column(c.Key("run-item-"+run.ID)).Focusable().Label("运行记录 "+run.ID).FillWidth().Padding(Space2, Space3).Radius(RadiusSM).Gap(Space1).Transition(motionListItem(c))
				if row.Hovered() {
					row.Background(ThemeHover(c))
				}
				if row.Clicked() || (row.Focused() && (row.Shortcut(0, ui.KeyEnter) || row.Shortcut(0, ui.KeySpace))) {
					open()
				}
				row.Children(func() {
					summary := run.Summary
					if kind == "failed" && run.Error != "" {
						summary = run.Error
					}
					narrow := row.Bounds().W < 700
					renderSummary := func() {
						if summary != "" {
							ui.Text(c, summary).FontSize(TypeBody).TextColor(ThemeSecondaryText(c)).Grow(1).Shrink(1).MinWidth(0).SingleLine().Ellipsis("…")
						}
					}
					ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(Space2).Children(func() {
						status, kind := automationRunStatus(run.Status)
						ui.Row(c.Key("run-status-" + run.ID)).Shrink(0).AlignItems(ui.Center).Gap(Space2).Children(func() {
							if unread {
								StatusDot(c, "run-unread-dot-"+run.ID, StatusAttention, "未读运行记录")
							}
							icon := IconCircleSmall()
							switch kind {
							case "running":
								icon = IconLoader()
							case "succeeded":
								icon = IconCircleCheck()
							case "failed":
								icon = IconCircleX()
							case "stopped":
								icon = IconCircleStop()
							}
							ui.Icon(c, icon).Size(IconSM, IconSM).TextColor(automationCardStatusColor(c, kind))
							weight := 400
							if unread {
								weight = weightFor(c, 500)
							}
							ui.Text(c, status).FontSize(TypeBody).FontWeight(weight).TextColor(ThemePrimaryText(c)).SingleLine()
						})
						ui.Row(c.Key("run-start-" + run.ID)).Shrink(0).Children(func() {
							ui.Text(c, run.StartedAt.Local().Format("15:04")).FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
						})
						duration := automationRunDuration(run)
						if duration == "运行中" {
							duration = "—"
						}
						ui.Row(c.Key("run-duration-" + run.ID)).Shrink(0).Children(func() { ui.Text(c, duration).FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c)) })
						if narrow || summary == "" {
							ui.Box(c).Grow(1)
						} else {
							renderSummary()
						}
						IconButton(c.Key("run-more-"+run.ID), IconButtonProps{Icon: IconMoreHorizontal(), Label: "运行记录更多操作 " + run.ID, Variant: ButtonGhost, Size: ControlXS}).Menu(func(m *ui.Menu) {
							if m.Item("查看详情").Chosen() {
								open()
							}
							if m.Item("归档").Disabled(archived).Chosen() {
								s.setAutomationRunArchived(run.ID, true)
							}
							if m.Item("取消归档").Disabled(!archived).Chosen() {
								s.setAutomationRunArchived(run.ID, false)
							}
						})
					})
					if narrow && summary != "" {
						ui.Row(c).FillWidth().Padding(0, Space5).Children(renderSummary)
					}
				})
			}
			if shown == 0 {
				ui.Text(c, "最近 100 条中无匹配").FontSize(TypeCaption).TextColor(ThemeSecondaryText(c))
			}
			if len(runs) >= 100 {
				ui.Text(c, "仅显示最近 100 条").FontSize(TypeCaption).TextColor(ThemeSecondaryText(c))
			}
		})
	})
}
