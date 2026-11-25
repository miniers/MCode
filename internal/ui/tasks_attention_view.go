package mcodeui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

const (
	attentionPriorityDecision = 1
	attentionPriorityKanban   = 2
	attentionPriorityAuto     = 3
	attentionPriorityInbox    = 4
)

type taskCenterAttentionItem struct {
	Key              string
	Kind             string // "decision", "task", "goal", "automation", "automation_run", "attention"
	EntityID         string
	ProjectID        string
	ProjectName      string
	ThreadID         string
	AttentionEventID string
	SubjectType      string
	SubjectID        string
	Title            string
	Subtitle         string
	SourceLabel      string
	StatusLabel      string
	StatusSemantic   semanticStatus
	FromSteward      bool
	Priority         int
	CreatedAt        time.Time
}

func tasksCenterAttentionBadgeCount(s *chatState, board KanbanBoard, scopeID string) int {
	return len(buildTaskCenterAttentionItems(s, board, scopeID, ""))
}

// buildTaskCenterAttentionItems 从内存快照聚合四类待你处理事项，按优先级排序、去重并支持范围与关键词过滤。
func buildTaskCenterAttentionItems(s *chatState, board KanbanBoard, scopeID string, searchText string) []taskCenterAttentionItem {
	projNames := make(map[string]string)
	threadProjects := make(map[string]string)
	if s != nil {
		s.mu.Lock()
		for _, p := range s.projects {
			projNames[p.ID] = p.Name
		}
		for _, th := range s.threads {
			if th.ID != "" && th.ProjectID != "" {
				threadProjects[th.ID] = th.ProjectID
			}
		}
		s.mu.Unlock()
	}

	taskProjects := make(map[string]string, len(board.Tasks))
	for _, task := range board.Tasks {
		if task.ID != "" && task.ProjectID != "" {
			taskProjects[task.ID] = task.ProjectID
		}
	}

	var items []taskCenterAttentionItem
	seenDecisions := make(map[string]bool)
	seenTasks := make(map[string]int)
	seenGoals := make(map[string]int)
	seenAutos := make(map[string]int)

	// 1. 未答总管决策
	var decisions []StewardDecisionItem
	if s != nil {
		stewardOpenMu.Lock()
		st := stewardOpenStates[s]
		stewardOpenMu.Unlock()
		if st != nil {
			st.mu.Lock()
			if len(st.openDecisions) > 0 {
				decisions = append([]StewardDecisionItem(nil), st.openDecisions...)
			}
			st.mu.Unlock()
		}
	}
	for _, d := range decisions {
		if d.ID == "" || seenDecisions[d.ID] {
			continue
		}
		if d.Status != "" && d.Status != "open" {
			continue
		}
		pid := ""
		if d.TaskID != "" {
			pid = taskProjects[d.TaskID]
		}
		if pid == "" && d.ThreadID != "" {
			pid = threadProjects[d.ThreadID]
		}
		if scopeID != "" && pid != "" && pid != scopeID {
			continue
		}
		seenDecisions[d.ID] = true
		title := strings.TrimSpace(d.Question)
		if title == "" {
			title = d.ID
		}
		items = append(items, taskCenterAttentionItem{
			Key:            "decision-" + d.ID,
			Kind:           "decision",
			EntityID:       d.ID,
			ProjectID:      pid,
			ProjectName:    projNames[pid],
			ThreadID:       d.ThreadID,
			Title:          title,
			Subtitle:       strings.TrimSpace(d.Context),
			SourceLabel:    "总管决策",
			StatusLabel:    "待你处理",
			StatusSemantic: semanticWarning,
			FromSteward:    true,
			Priority:       attentionPriorityDecision,
			CreatedAt:      d.CreatedAt,
		})
	}

	// 2. 待你处理任务与待审方案目标
	for _, task := range board.Tasks {
		if task.ID == "" {
			continue
		}
		if scopeID != "" && task.ProjectID != scopeID {
			continue
		}
		if !tasksNeedsAttention(task) {
			continue
		}
		if _, exists := seenTasks[task.ID]; exists {
			continue
		}
		title := strings.TrimSpace(task.Title)
		if title == "" {
			title = task.ID
		}
		subtitle := strings.TrimSpace(task.LastFailureReason)
		if subtitle == "" {
			subtitle = strings.TrimSpace(task.LastActivity)
		}
		statusLabel := taskBadgeLabel(task)
		if task.Status == "needs_you" || statusLabel == "" {
			statusLabel = taskStatusLabel(task.Status)
		}
		if statusLabel == "" {
			statusLabel = "待你处理"
		}
		sem := taskStatusSemantic(task.Status)
		if sem == semanticNeutral {
			sem = taskStatusSemantic(kanbanDisplayColumn(task))
		}
		if sem == semanticNeutral {
			sem = semanticWarning
		}
		seenTasks[task.ID] = len(items)
		items = append(items, taskCenterAttentionItem{
			Key:            "task-" + task.ID,
			Kind:           "task",
			EntityID:       task.ID,
			ProjectID:      task.ProjectID,
			ProjectName:    projNames[task.ProjectID],
			ThreadID:       task.ThreadID,
			Title:          title,
			Subtitle:       subtitle,
			SourceLabel:    "任务",
			StatusLabel:    statusLabel,
			StatusSemantic: sem,
			FromSteward:    isStewardTaskOrigin(task.Origin),
			Priority:       attentionPriorityKanban,
			CreatedAt:      task.UpdatedAt,
		})
	}

	for _, goal := range board.Goals {
		if goal.ID == "" {
			continue
		}
		if scopeID != "" && goal.ProjectID != scopeID {
			continue
		}
		if goal.Status != "plan_review" && goal.Status != "plan_failed" {
			continue
		}
		if _, exists := seenGoals[goal.ID]; exists {
			continue
		}
		title := strings.TrimSpace(goal.Title)
		if title == "" {
			title = strings.TrimSpace(goal.Prompt)
		}
		if title == "" {
			title = goal.ID
		}
		subtitle := strings.TrimSpace(goal.StatusReason)
		if subtitle == "" {
			subtitle = strings.TrimSpace(goal.PlanSummary)
		}
		seenGoals[goal.ID] = len(items)
		items = append(items, taskCenterAttentionItem{
			Key:            "goal-" + goal.ID,
			Kind:           "goal",
			EntityID:       goal.ID,
			ProjectID:      goal.ProjectID,
			ProjectName:    projNames[goal.ProjectID],
			Title:          title,
			Subtitle:       subtitle,
			SourceLabel:    "目标",
			StatusLabel:    goalStatusLabel(goal.Status),
			StatusSemantic: goalStatusSemantic(goal.Status),
			Priority:       attentionPriorityKanban,
			CreatedAt:      goal.UpdatedAt,
		})
	}

	// 3. 失败或未读自动化
	var autos []Automation
	var autoUnread map[string]int
	if s != nil {
		automationMu.Lock()
		if as := automationStates[s]; as != nil {
			if len(as.autos) > 0 {
				autos = append([]Automation(nil), as.autos...)
			}
			if len(as.unread) > 0 {
				autoUnread = make(map[string]int, len(as.unread))
				for k, v := range as.unread {
					autoUnread[k] = v
				}
			}
		}
		automationMu.Unlock()
	}
	for _, a := range autos {
		if a.ID == "" {
			continue
		}
		if scopeID != "" && a.ProjectID != scopeID {
			continue
		}
		unread := autoUnread[a.ID]
		failed := a.LastRunStatus == "failed" || a.LastRunStatus == "error" || a.Status == "failed" || (!a.Enabled && a.Status == "failed" && a.ConsecutiveFailures > 0)
		if !failed && unread <= 0 {
			continue
		}
		if _, exists := seenAutos[a.ID]; exists {
			continue
		}
		title := strings.TrimSpace(a.Name)
		if title == "" {
			title = a.ID
		}
		subtitle := strings.TrimSpace(a.StopReason)
		if subtitle == "" && unread > 0 {
			subtitle = fmt.Sprintf("%d 条未读运行", unread)
		}
		statusLabel := "有未读"
		sem := semanticAccent
		if failed {
			statusLabel, _ = automationRunStatusDict(a.LastRunStatus)
			if statusLabel == "" || statusLabel == "未知" {
				statusLabel = "失败"
			}
			sem = semanticDanger
		}
		seenAutos[a.ID] = len(items)
		items = append(items, taskCenterAttentionItem{
			Key:            "automation-" + a.ID,
			Kind:           "automation",
			EntityID:       a.ID,
			ProjectID:      a.ProjectID,
			ProjectName:    projNames[a.ProjectID],
			Title:          title,
			Subtitle:       subtitle,
			SourceLabel:    "自动化",
			StatusLabel:    statusLabel,
			StatusSemantic: sem,
			FromSteward:    isStewardAutomationOrigin(a.Origin),
			Priority:       attentionPriorityAuto,
			CreatedAt:      a.LastRunAt,
		})
	}

	// 4. 其余未消解收件箱事件（若关联的任务或目标已在第 2 类收录，则合并其 AttentionEventID 以便快捷标记已处理）
	var inboxItems []AttentionItem
	if s != nil {
		s.mu.Lock()
		st := s.attentionInbox
		s.mu.Unlock()
		if st != nil {
			st.mu.Lock()
			if len(st.items) > 0 {
				inboxItems = append([]AttentionItem(nil), st.items...)
			}
			st.mu.Unlock()
		}
	}
	for _, ev := range inboxItems {
		if scopeID != "" && ev.ProjectID != "" && ev.ProjectID != scopeID {
			continue
		}
		if ev.SubjectType == "kanban_task" && ev.SubjectID != "" {
			if idx, ok := seenTasks[ev.SubjectID]; ok {
				if items[idx].AttentionEventID == "" {
					items[idx].AttentionEventID = ev.ID
				}
				continue
			}
		}
		if ev.SubjectType == "kanban_goal" && ev.SubjectID != "" {
			if idx, ok := seenGoals[ev.SubjectID]; ok {
				if items[idx].AttentionEventID == "" {
					items[idx].AttentionEventID = ev.ID
				}
				continue
			}
		}
		if ev.SubjectType == "automation" && ev.SubjectID != "" {
			if idx, ok := seenAutos[ev.SubjectID]; ok {
				if items[idx].AttentionEventID == "" {
					items[idx].AttentionEventID = ev.ID
				}
				continue
			}
		}

		kind := "attention"
		entityID := ev.ID
		sourceLabel := "收件箱"
		if ev.SubjectType == "automation_run" && ev.SubjectID != "" {
			kind = "automation_run"
			entityID = ev.SubjectID
			sourceLabel = "自动化"
		} else if ev.SubjectType == "automation" && ev.SubjectID != "" {
			kind = "automation"
			entityID = ev.SubjectID
			sourceLabel = "自动化"
		}
		title := strings.TrimSpace(ev.Title)
		if title == "" {
			title = attentionKindTitle(ev.Kind)
		}
		if title == "" {
			title = "待处理事项"
		}
		statusLabel := attentionKindTitle(ev.Kind)
		items = append(items, taskCenterAttentionItem{
			Key:              "attention-" + ev.ID,
			Kind:             kind,
			EntityID:         entityID,
			ProjectID:        ev.ProjectID,
			ProjectName:      projNames[ev.ProjectID],
			ThreadID:         ev.ThreadID,
			AttentionEventID: ev.ID,
			SubjectType:      ev.SubjectType,
			SubjectID:        ev.SubjectID,
			Title:            title,
			Subtitle:         strings.TrimSpace(ev.Detail),
			SourceLabel:      sourceLabel,
			StatusLabel:      statusLabel,
			StatusSemantic:   attentionEventSemantic(ev.Kind),
			Priority:         attentionPriorityInbox,
			CreatedAt:        ev.CreatedAt,
		})
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Priority != items[j].Priority {
			return items[i].Priority < items[j].Priority
		}
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.After(items[j].CreatedAt)
		}
		return items[i].Key < items[j].Key
	})

	query := strings.ToLower(strings.TrimSpace(searchText))
	if query == "" {
		return items
	}
	filtered := make([]taskCenterAttentionItem, 0, len(items))
	for _, item := range items {
		if attentionItemMatchesQuery(item, query) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func attentionEventSemantic(kind string) semanticStatus {
	_, tone := attentionKindMark(kind)
	switch tone {
	case ToneDanger:
		return semanticDanger
	case ToneWarning:
		return semanticWarning
	case ToneAccent:
		return semanticAccent
	default:
		return semanticNeutral
	}
}

func attentionItemMatchesQuery(item taskCenterAttentionItem, query string) bool {
	return strings.Contains(strings.ToLower(item.Title), query) ||
		strings.Contains(strings.ToLower(item.Subtitle), query) ||
		strings.Contains(strings.ToLower(item.StatusLabel), query) ||
		strings.Contains(strings.ToLower(item.ProjectName), query) ||
		strings.Contains(strings.ToLower(item.SourceLabel), query)
}

func renderTasksAttentionView(c *ui.Context, s *chatState, ks *kanbanState, p kanbanModalBodyProps) {
	items := buildTaskCenterAttentionItems(s, p.board, p.scopeID, p.searchText)
	if len(items) == 0 {
		EmptyState(c, EmptyStateProps{
			Key:     "tasks-attention-empty",
			Title:   "没有待你处理的事项",
			Compact: true,
		}).Fill().Center()
		return
	}

	kanbanMu.Lock()
	detailTaskID := ks.detailTaskID
	goalID := ks.goalID
	goalDetailOpen := ks.goalDetailOpen
	detailDecisionID := ks.detailDecisionID
	kanbanMu.Unlock()

	ui.Scroll(c.Key("tasks-attention-scroll")).Grow(1).FillWidth().MinHeight(0).Children(func() {
		ui.Column(c.Key("tasks-attention-list")).FillWidth().Gap(Space2).Children(func() {
			for _, item := range items {
				item := item
				selected := (item.Kind == "decision" && detailDecisionID == item.EntityID) ||
					(item.Kind == "task" && detailTaskID == item.EntityID) ||
					(item.Kind == "goal" && goalDetailOpen && goalID == item.EntityID)
				renderTasksAttentionItemRow(c, s, ks, p.board, item, selected)
			}
		})
	})
}

func renderTasksAttentionItemRow(c *ui.Context, s *chatState, ks *kanbanState, board KanbanBoard, item taskCenterAttentionItem, selected bool) {
	row := ui.Row(c.Key("tasks-attention-item-"+item.Key)).
		FillWidth().
		Padding(Space2, Space3).
		Radius(RadiusMD).
		Border(Hairline, ThemeBorder(c)).
		Gap(Space3).
		AlignItems(ui.Center).
		Transition(motionListItem(c))
	if selected {
		row.Background(ThemeSelected(c))
	} else if row.Hovered() {
		row.Background(ThemeHover(c))
	} else {
		row.Background(ThemeSurface(c))
	}

	row.Children(func() {
		btn := ui.ButtonBase(c.Key("tasks-attention-select-" + item.Key)).
			Label(item.Title).
			Grow(1).
			MinWidth(0).
			OnClick(func() {
				activateTasksAttentionItem(s, ks, board, item)
			})
		focusRing(c, btn)
		btn.Children(func() {
			ui.Column(c.Key("tasks-attention-body-" + item.Key)).FillWidth().MinWidth(0).Gap(Space1).Children(func() {
				ui.Row(c.Key("tasks-attention-top-" + item.Key)).FillWidth().MinWidth(0).AlignItems(ui.Center).Gap(Space2).Children(func() {
					if item.FromSteward {
						renderStewardOriginBadge(c, "tasks-attention-steward-"+item.Key)
					}
					if item.SourceLabel != "" {
						Badge(c, "tasks-attention-source-"+item.Key, item.SourceLabel, ToneNeutral).Shrink(0)
					}
					if item.StatusLabel != "" {
						renderSemanticStatusBadge(c, "tasks-attention-status-"+item.Key, item.StatusLabel, item.StatusSemantic)
					}
					ui.Text(c, item.Title).
						FontSize(uiFont(TypeBody)).
						FontWeight(500).
						TextColor(ThemePrimaryText(c)).
						SingleLine().
						Ellipsis("…").
						Tooltip(item.Title).
						Grow(1).
						MinWidth(0)
					if item.ProjectName != "" {
						ui.Text(c, item.ProjectName).
							FontSize(uiFont(TypeCaption)).
							TextColor(ThemeSecondaryText(c)).
							SingleLine().
							Shrink(0)
					}
				})
				if item.Subtitle != "" {
					ui.Text(c, item.Subtitle).
						FontSize(uiFont(TypeFootnote)).
						TextColor(ThemeSecondaryText(c)).
						SingleLine().
						Ellipsis("…").
						FillWidth()
				}
			})
		})

		if item.AttentionEventID != "" {
			evID := item.AttentionEventID
			Button(c, ButtonOpts{
				Key:     "tasks-attention-resolve-" + item.Key,
				Text:    "标记已处理",
				Label:   "标记已处理 " + item.Title,
				Size:    ControlXS,
				Variant: ButtonSecondary,
				OnClick: func() {
					s.resolveAttentionItem(evID)
				},
			}).Shrink(0)
		}
	})
}

func renderSemanticStatusBadge(c *ui.Context, key, text string, status semanticStatus) {
	color := semanticStatusColor(c, status)
	ui.Row(c.Key(key)).
		Label(text).
		Shrink(0).
		Padding(Space0, Space2).
		Radius(RadiusXS).
		Background(color.Alpha(0.12)).
		Children(func() {
			ui.Text(c, text).FontSize(uiFont(TypeCaption)).TextColor(color).SingleLine()
		})
}

func activateTasksAttentionItem(s *chatState, ks *kanbanState, board KanbanBoard, item taskCenterAttentionItem) {
	if s == nil {
		return
	}
	switch item.Kind {
	case "task":
		var matched *KanbanTask
		kanbanMu.Lock()
		if ks != nil {
			ks.detailDecisionID = ""
			for i := range ks.board.Tasks {
				if ks.board.Tasks[i].ID == item.EntityID {
					t := ks.board.Tasks[i]
					matched = &t
					break
				}
			}
		}
		kanbanMu.Unlock()
		if matched == nil {
			for i := range board.Tasks {
				if board.Tasks[i].ID == item.EntityID {
					t := board.Tasks[i]
					matched = &t
					break
				}
			}
		}
		if matched != nil {
			s.openTaskDetail(*matched)
		}
	case "goal":
		if ks == nil {
			return
		}
		s.closeTaskDetail()
		kanbanMu.Lock()
		ks.goalID = item.EntityID
		ks.goalDetailOpen = true
		ks.detailTaskID = ""
		ks.detailDecisionID = ""
		kanbanMu.Unlock()
		s.invalidate()
	case "decision":
		if ks == nil {
			return
		}
		s.closeTaskDetail()
		kanbanMu.Lock()
		ks.detailDecisionID = item.EntityID
		ks.detailTaskID = ""
		ks.goalDetailOpen = false
		kanbanMu.Unlock()
		s.invalidate()
	case "automation", "automation_run":
		s.applyTasksWindowTarget(taskCenterTarget{
			Kind:      item.Kind,
			ID:        item.EntityID,
			ProjectID: item.ProjectID,
			Title:     item.Title,
		})
	default:
		if item.ThreadID != "" {
			s.revealThread(item.ThreadID)
		} else if item.SubjectType != "" {
			s.openAttentionItem(AttentionItem{
				ID:          item.AttentionEventID,
				SubjectType: item.SubjectType,
				SubjectID:   item.SubjectID,
				ProjectID:   item.ProjectID,
				ThreadID:    item.ThreadID,
				Title:       item.Title,
			})
		}
	}
}

func renderStewardDecisionDetailContent(c *ui.Context, s *chatState, ks *kanbanState) {
	kanbanMu.Lock()
	decisionID := ks.detailDecisionID
	kanbanMu.Unlock()

	var decision StewardDecisionItem
	var found bool
	var decisionErr string
	if s != nil && decisionID != "" {
		stewardOpenMu.Lock()
		st := stewardOpenStates[s]
		stewardOpenMu.Unlock()
		if st != nil {
			st.mu.Lock()
			for _, d := range st.openDecisions {
				if d.ID == decisionID {
					decision = d
					found = true
					break
				}
			}
			decisionErr = st.decisionErrors[decisionID]
			st.mu.Unlock()
		}
	}

	var projectName string
	if found && s != nil {
		var projectID string
		if decision.TaskID != "" {
			kanbanMu.Lock()
			for _, task := range ks.board.Tasks {
				if task.ID == decision.TaskID && task.ProjectID != "" {
					projectID = task.ProjectID
					break
				}
			}
			kanbanMu.Unlock()
		}
		if projectID == "" && decision.ThreadID != "" {
			s.mu.Lock()
			for _, th := range s.threads {
				if th.ID == decision.ThreadID && th.ProjectID != "" {
					projectID = th.ProjectID
					break
				}
			}
			s.mu.Unlock()
		}
		if projectID != "" {
			projectName = projectNameFor(s, projectID)
		}
	}

	var onOpenThread func()
	if found && decision.ThreadID != "" {
		threadID := decision.ThreadID
		onOpenThread = func() {
			s.revealThread(threadID)
		}
	}

	ui.Column(c.Key("kanban-decision-detail")).Label("kanban-decision-detail").Fill().MinWidth(0).Gap(0).Children(func() {
		renderTaskDetailDrawerHeader(c, taskDetailDrawerHeaderOpts{
			Key:           "kanban-decision-detail-header",
			OriginSteward: true,
			StatusLabel:   "待你处理",
			StatusColor:   ThemeWarning(c),
			Title:         "决策详情",
			ProjectName:   projectName,
			ThreadID:      decision.ThreadID,
			OnOpenThread:  onOpenThread,
			CloseKey:      "kanban-decision-detail-close",
			CloseLabel:    "关闭决策详情",
			CloseTooltip:  "关闭决策详情",
			OnClose: func() {
				kanbanMu.Lock()
				ks.detailDecisionID = ""
				kanbanMu.Unlock()
				s.invalidate()
			},
		})

		if !found {
			ui.Column(c.Key("kanban-decision-detail-empty-wrap")).Grow(1).FillWidth().Center().Padding(Space5).Children(func() {
				renderTaskDetailDrawerEmpty(c, "kanban-decision-detail-empty", "决策已处理")
			})
			return
		}

		ui.Scroll(c.Key("kanban-decision-detail-scroll")).Grow(1).FillWidth().MinHeight(0).Children(func() {
			ui.Column(c.Key("kanban-decision-detail-body")).FillWidth().Padding(Space4, Space5).Gap(Space4).Children(func() {
				ui.Text(c, decision.Question).
					FontSize(uiFont(TypeTitle)).
					FontWeight(600).
					TextColor(ThemePrimaryText(c)).
					FillWidth().
					Wrap()

				if strings.TrimSpace(decision.Context) != "" {
					ui.Column(c.Key("kanban-decision-detail-context")).
						FillWidth().
						Padding(Space3, Space4).
						Radius(RadiusMD).
						Background(ThemeHover(c)).
						Children(func() {
							ui.Text(c, decision.Context).
								FontSize(uiFont(TypeFootnote)).
								TextColor(ThemeSecondaryText(c)).
								FillWidth().
								Wrap()
						})
				}

				if decisionErr != "" && !strings.HasPrefix(decisionErr, stewardDecisionSubmitting) {
					ui.Text(c, decisionErr).
						FontSize(uiFont(TypeFootnote)).
						TextColor(ThemeDanger(c)).
						FillWidth().
						Wrap()
				}

				ui.Column(c.Key("kanban-decision-detail-options")).FillWidth().Gap(Space2).Children(func() {
					for i, opt := range decision.Options {
						opt := opt
						recommended := decision.Recommended != "" && opt == decision.Recommended
						btn := ui.ButtonBase(c.Key(fmt.Sprintf("kanban-decision-option-%s-%d", decision.ID, i))).
							Label(opt).
							FillWidth().
							Padding(Space2, Space3).
							Radius(RadiusSM).
							OnClick(func() {
								answerStewardDecisionFromTasksCenter(s, decision.ID, opt)
							})
						focusRing(c, btn)
						bg := ThemeSurface(c)
						border := ThemeBorder(c)
						if recommended {
							bg = ThemeAccent(c).Alpha(0.08)
							border = ThemeAccent(c)
						}
						if btn.Hovered() {
							bg = ThemeHover(c)
						}
						btn.Border(Hairline, border).Background(bg).Children(func() {
							ui.Row(c.Key(fmt.Sprintf("kanban-decision-option-row-%s-%d", decision.ID, i))).
								FillWidth().
								AlignItems(ui.Center).
								Gap(Space2).
								Children(func() {
									ui.Text(c, opt).
										FontSize(uiFont(TypeBody)).
										TextColor(ThemePrimaryText(c)).
										Grow(1).
										Wrap().
										MinWidth(0)
									if recommended {
										renderRecommendedBadge(c, fmt.Sprintf("kanban-decision-recommended-%s-%d", decision.ID, i))
									}
								})
						})
					}
				})
			})
		})

		if decision.ThreadID != "" {
			threadID := decision.ThreadID
			ui.Row(c.Key("kanban-decision-detail-footer")).
				FillWidth().
				Padding(Space4, Space5).
				Gap(Space2).
				BorderWidth(Hairline, 0, 0, 0).
				BorderColor(ThemeBorder(c)).
				Children(func() {
					Button(c, ButtonOpts{
						Key:     "kanban-decision-open-thread",
						Text:    "打开总管会话",
						Label:   "打开总管会话",
						Variant: ButtonSecondary,
						Size:    ControlMD,
						OnClick: func() {
							s.revealThread(threadID)
						},
					})
				})
		}
	})
}

func answerStewardDecisionFromTasksCenter(s *chatState, id, answer string) {
	if s == nil || s.client == nil || id == "" {
		return
	}
	client, ok := s.client.(stewardDecisionAnswerClient)
	if !ok {
		return
	}
	st := getStewardOpenState(s)
	if st == nil {
		return
	}

	st.mu.Lock()
	var removed StewardDecisionItem
	removedAt := -1
	kept := make([]StewardDecisionItem, 0, len(st.openDecisions))
	for i, d := range st.openDecisions {
		if d.ID == id && removedAt < 0 {
			removed = d
			removedAt = i
			continue
		}
		kept = append(kept, d)
	}
	st.openDecisions = kept
	st.decisionCount = len(kept)
	if st.decisionErrors != nil {
		delete(st.decisionErrors, id)
	}
	st.mu.Unlock()
	s.invalidate()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := client.AnswerStewardDecision(ctx, id, answer)
		cancel()
		if err != nil {
			st.mu.Lock()
			if removedAt >= 0 {
				if removedAt > len(st.openDecisions) {
					removedAt = len(st.openDecisions)
				}
				restored := make([]StewardDecisionItem, 0, len(st.openDecisions)+1)
				restored = append(restored, st.openDecisions[:removedAt]...)
				restored = append(restored, removed)
				restored = append(restored, st.openDecisions[removedAt:]...)
				st.openDecisions = restored
				st.decisionCount = len(restored)
			}
			if st.decisionErrors == nil {
				st.decisionErrors = make(map[string]string)
			}
			st.decisionErrors[id] = err.Error()
			st.mu.Unlock()
			s.invalidate()
			return
		}
		refreshStewardOpenDecisions(s)
	}()
}
