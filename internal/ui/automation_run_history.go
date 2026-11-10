package mcodeui

import (
	"fmt"
	"sort"
	"time"

	"github.com/egoist/mygo/ui"
)

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
	failedOnly, unreadOnly := view.failedOnly, view.unreadOnly
	errText, retry := view.err, view.retry
	automationMu.Unlock()
	ui.Box(c.Key("automation-runs-divider")).FillWidth().Height(Hairline).Background(ThemeDivider(c)).Margin(Space2, 0, Space2, 0)
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
			ui.Text(c, "运行记录").FontSize(TypeBody).FontWeight(600).TextColor(ThemePrimaryText(c)).Grow(1)
			Button(c.Key("automation-runs-mark-all-read"), ButtonOpts{Text: "全部标为已读", Label: "全部标为已读", Disabled: selectedUnread == 0, OnClick: func() { s.markAllAutomationRunsRead(selectedID) }, Variant: ButtonSecondary, Size: ControlXS})
			ui.Text(c, "显示已归档").FontSize(TypeCaption).TextColor(ThemeSecondaryText(c))
			value := includeArchived
			ui.Switch(c.Key("automation-runs-include-archived"), &value).Label("显示已归档运行记录")
			if value != includeArchived {
				automationMu.Lock()
				as.includeArchived = value
				as.openRunID = ""
				automationMu.Unlock()
				s.invalidate()
				s.loadAutomationRuns(selectedID)
			}
			IconButton(c.Key("automation-runs-refresh"), IconButtonProps{Icon: IconRefreshCw(), Label: "刷新记录", OnClick: func() { s.loadAutomationRuns(selectedID) }, Variant: ButtonGhost, Size: ControlMD})
		})
		ui.Row(c.Key("automation-runs-filters")).Gap(Space2).Children(func() {
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
		ui.Column(c.Key("runs-list-col")).FillWidth().Gap(Space2).Transition(motionTransition(c, ui.ElementTransition{Duration: DurBase, Enter: &ui.Motion{Opacity: 0}})).Children(func() {
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
					ui.Text(c, day).FontSize(TypeCaption).FontWeight(600).TextColor(ThemeSecondaryText(c))
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
				row := ui.Row(c.Key("run-item-"+run.ID)).Label("运行记录 "+run.ID).FillWidth().Padding(Space2, Space3).Radius(RadiusSM).AlignItems(ui.Center).Gap(Space2).Transition(motionListItem(c))
				if row.Hovered() {
					row.Background(ThemeOverlay(c, 0.08))
				} else {
					row.Background(ThemeOverlay(c, 0.04))
				}
				if row.Clicked() {
					open()
				}
				row.Children(func() {
					status, kind := automationRunStatus(run.Status)
					ui.Row(c.Key("run-status-" + run.ID)).Width(90).Shrink(0).AlignItems(ui.Center).Gap(Space1).Children(func() {
						if unread {
							StatusDot(c, "run-unread-dot-"+run.ID, StatusAttention, "未读运行记录")
						}
						ui.Text(c, status).FontSize(TypeCaption).TextColor(automationCardStatusColor(c, kind))
					})
					ui.Row(c.Key("run-start-" + run.ID)).Width(75).Shrink(0).Children(func() {
						ui.Text(c, run.StartedAt.Local().Format("15:04:05")).FontSize(TypeCaption).TextColor(ThemeSecondaryText(c))
					})
					ui.Row(c.Key("run-duration-" + run.ID)).Width(65).Shrink(0).Children(func() { ui.Text(c, automationRunDuration(run)).FontSize(TypeCaption).TextColor(ThemeSecondaryText(c)) })
					if badge := automationRunTriggerBadge(run); badge != "" {
						ui.Row(c.Key("run-trigger-pill-"+run.ID)).Padding(Space0, Space2).Radius(RadiusXS).Background(ThemeOverlay(c, 0.10)).Children(func() {
							ui.Text(c, badge).FontSize(TypeCaption).TextColor(ThemeSecondaryText(c)).SingleLine()
						})
					}
					ui.Box(c).Grow(1)
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
