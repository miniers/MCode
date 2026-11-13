package mcodeui

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

type stewardResult struct {
	Task    string `json:"task"`
	Thread  string `json:"thread"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Status  string `json:"status"`
	Owner   bool   `json:"owner,omitempty"`
}

// stewardTaskSnapshot 是任务页信息带所需的最小看板快照。
type stewardTaskSnapshot struct {
	ID            string
	Status        string
	Column        string
	ResultSummary string
	WaitKind      string
	WakeAt        string
}

func parseStewardResult(text string) (stewardResult, bool) {
	var result stewardResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return stewardResult{}, false
	}
	if result.Task == "" || result.Title == "" || (result.Status != "done" && result.Status != "failed") {
		return stewardResult{}, false
	}
	return result, true
}

func renderStewardResult(c *ui.Context, s *chatState, m Message, liveID, lastLiveID string, isLastAssistant, hasUsage bool, turnUsage TokenUsage, turnDur time.Duration, suppressTurnEnd bool) ui.Element {
	if m.Role == "system" {
		return renderSystemMessage(c, s, m)
	}
	result, ok := parseStewardResult(m.Text)
	if !ok {
		return renderAssistantMessage(c, s, m, liveID, lastLiveID, isLastAssistant, hasUsage, turnUsage, turnDur, suppressTurnEnd)
	}

	text := result.Title + "：" + result.Summary
	if result.Status == "failed" {
		text = "没能完成「" + result.Title + "」：" + result.Summary
	} else if result.Owner {
		text = result.Summary
	}
	m.Text = text
	return ui.Column(c.Key("steward-result-" + m.ID)).
		FillWidth().MaxWidth(timelineContentMaxWidth(s)).AlignSelf(ui.Center).
		Children(func() {
			renderAssistantMessage(c, s, m, liveID, lastLiveID, isLastAssistant, hasUsage, turnUsage, turnDur, suppressTurnEnd)
			if result.Thread != "" {
				ui.Row(c.Key("steward-result-actions-"+m.ID)).Margin(2, 0, 0, 0).Children(func() {
					if result.Owner {
						task := KanbanTask{ID: result.Task, ThreadID: result.Thread, Title: result.Title, Status: result.Status, ResultSummary: result.Summary}
						for _, live := range s.stewardBoardTasks() {
							if live.ID == result.Task {
								task.Status, task.Column = live.Status, live.Column
								break
							}
						}
						renderStewardTaskChip(c, s, "steward-result-open-"+m.ID, task)
					} else {
						Button(c.Key("steward-result-open-"+m.ID), ButtonOpts{
							Text:    "打开任务",
							Variant: ButtonSecondary,
							Size:    ControlMD,
							OnClick: func() { openStewardTask(s, result) },
						})
					}
				})
			}
		})
}

func openStewardTask(s *chatState, result stewardResult) {
	if s == nil || result.Thread == "" || result.Task == "" {
		return
	}
	markStewardTaskSeenByID(s, result.Task)
	s.mu.Lock()
	s.stewardTaskThreadID = result.Thread
	s.stewardTask = stewardTaskSnapshot{ID: result.Task, Status: result.Status, ResultSummary: result.Summary}
	s.mu.Unlock()
	s.selectThread(result.Thread)
}

// stopStewardTaskWatchLocked 调用方必须持有 s.mu。
func stopStewardTaskWatchLocked(s *chatState) {
	s.stewardTaskLoadSeq++
	if s.stewardTaskWatchStop != nil {
		s.stewardTaskWatchStop()
		s.stewardTaskWatchStop = nil
	}
}

func stopStewardTaskWatch(s *chatState) {
	s.mu.Lock()
	stopStewardTaskWatchLocked(s)
	s.mu.Unlock()
}

func loadStewardTaskForThread(s *chatState, threadID string) {
	s.mu.Lock()
	if s.closed || s.selectedThreadID != threadID {
		s.mu.Unlock()
		return
	}
	s.stewardTaskLoadSeq++
	seq := s.stewardTaskLoadSeq
	s.mu.Unlock()
	go func() {
		board, err := s.client.KanbanBoard(context.Background(), "")
		if err != nil {
			return
		}
		for _, task := range board.Tasks {
			if task.ThreadID != threadID {
				continue
			}
			s.mu.Lock()
			started := false
			if !s.closed && s.selectedThreadID == threadID && s.stewardTaskLoadSeq == seq {
				s.stewardTaskThreadID = threadID
				s.stewardTask = stewardTaskSnapshot{
					ID:            task.ID,
					Status:        task.Status,
					Column:        task.Column,
					ResultSummary: task.ResultSummary,
					WaitKind:      task.WaitKind,
					WakeAt:        task.WakeAt,
				}
				if s.stewardTaskWatchStop == nil {
					s.stewardTaskWatchStop = s.client.WatchKanban(func() {
						s.mu.Lock()
						showing := !s.closed && s.selectedThreadID == threadID && s.stewardTaskThreadID == threadID
						s.mu.Unlock()
						if showing {
							loadStewardTaskForThread(s, threadID)
						}
					})
					started = true
				}
			}
			s.mu.Unlock()
			s.invalidate()
			if started {
				// 补拉订阅建立前可能发生的变更。
				loadStewardTaskForThread(s, threadID)
			}
			return
		}
		s.mu.Lock()
		if !s.closed && s.selectedThreadID == threadID && s.stewardTaskLoadSeq == seq && s.stewardTaskThreadID == threadID {
			s.stewardTaskThreadID = ""
			s.stewardTask = stewardTaskSnapshot{}
			stopStewardTaskWatchLocked(s)
		}
		s.mu.Unlock()
		s.invalidate()
	}()
}

func renderStewardTaskBand(c *ui.Context, s *chatState, selectedID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if selectedID == "" || selectedID != s.stewardTaskThreadID {
		s.mu.Unlock()
		return
	}
	snapshot := s.stewardTask
	s.mu.Unlock()

	task := KanbanTask{ID: snapshot.ID, Status: snapshot.Status, Column: snapshot.Column}
	if task.Status == "needs_approval" && task.Column == "" {
		task.Column = "needs_you"
	}
	label := kanbanStatusBadge(task)
	if label == "" {
		label = "未知状态"
	}
	reason := strings.TrimSpace(snapshot.ResultSummary)
	if snapshot.Status == "waiting" {
		if reason == "" {
			switch snapshot.WaitKind {
			case "human":
				reason = "等待你的决定"
			case "machine":
				reason = "等待条件满足"
			case "children":
				reason = "等待子任务"
			default:
				reason = "等待中"
			}
		}
		if at := stewardWakeLocalTime(snapshot.WakeAt); at != "" {
			reason += " · " + at
		}
	}
	band := ui.Row(c.Key("steward-task-band-"+snapshot.ID)).
		FillWidth().MaxWidth(timelineContentMaxWidth(s)).AlignSelf(ui.Center).
		AlignItems(ui.Center).Gap(Space3).Padding(Space2, Space3).
		Radius(RadiusSM).Background(ThemeHover(c))
	alpha := band.AnimateTo("enter", 1, ui.Animation{Duration: animateDuration(c, DurBase), Ease: motionEaseStandard})
	band.Opacity(alpha).Children(func() {
		kanbanToneBadge(c, "steward-task-status-"+snapshot.ID, label, kanbanStatusColor(c, snapshot.Status))
		if reason != "" {
			ui.Text(c, reason).Grow(1).MinWidth(0).SingleLine().Ellipsis("…").
				FontSize(scaledFont(TypeBody)).TextColor(ThemeSecondaryText(c))
		}
	})
}

func stewardWakeLocalTime(value string) string {
	if value == "" {
		return ""
	}
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return ""
	}
	return at.Local().Format("2006-01-02 15:04")
}
