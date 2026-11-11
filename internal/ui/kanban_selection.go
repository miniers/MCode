package mcodeui

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

type kanbanBatchFailure struct{ Title, Reason string }
type kanbanBatchState struct {
	running       bool
	cancel        context.CancelFunc
	failures      []kanbanBatchFailure
	expanded      bool
	confirmDelete int
}

var kanbanBatchMu sync.Mutex
var kanbanBatchStates = make(map[*chatState]*kanbanBatchState)

// 不要在持有 kanbanMu 时获取 kanbanBatchMu；项目切换和关闭钩子会在释放 kanbanMu 后调用此函数。
func kanbanBatchCancel(s *chatState) {
	kanbanBatchMu.Lock()
	st := kanbanBatchStates[s]
	var cancel context.CancelFunc
	if st != nil {
		cancel = st.cancel
	}
	delete(kanbanBatchStates, s)
	kanbanBatchMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func kanbanBatchEligible(s *chatState, tasks []KanbanTask) (moveable, deletable []KanbanTask) {
	if s == nil {
		return
	}
	ks := getKanbanState(s)
	_, canDelete := s.kanbanEditClient()
	for _, task := range tasks {
		busy := kanbanTaskBusy(ks, task.ID)
		if kanbanActions(task, busy).targets["todo"] != "" {
			moveable = append(moveable, task)
		}
		if canDelete && task.Status != "in_progress" && !busy {
			deletable = append(deletable, task)
		}
	}
	return
}

func kanbanBatchRun(s *chatState, kind string, tasks []KanbanTask) {
	if s == nil || len(tasks) == 0 {
		return
	}
	kanbanBatchMu.Lock()
	st := kanbanBatchStates[s]
	if st == nil {
		st = &kanbanBatchState{}
		kanbanBatchStates[s] = st
	}
	if st.running {
		kanbanBatchMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	st.running, st.cancel, st.failures = true, cancel, nil
	kanbanBatchMu.Unlock()
	go func() {
		failures := make([]kanbanBatchFailure, 0)
		for _, task := range tasks {
			if ctx.Err() != nil {
				break
			}
			itemCtx, itemCancel := context.WithTimeout(ctx, 10*time.Second)
			var err error
			switch kind {
			case "todo":
				_, err = s.client.MoveTask(itemCtx, task.ID, "todo", task.Revision)
			case "delete":
				client, ok := s.kanbanEditClient()
				if !ok {
					err = fmt.Errorf("删除不可用")
				} else {
					err = client.DeleteTask(itemCtx, task.ID, task.Revision)
				}
			default:
				err = fmt.Errorf("未知批量操作")
			}
			itemCancel()
			if ctx.Err() != nil {
				break
			}
			if err != nil {
				failures = append(failures, kanbanBatchFailure{Title: task.Title, Reason: err.Error()})
				continue
			}
			kanbanKeyboardMu.Lock()
			if keyboard := kanbanKeyboardStates[s]; keyboard != nil {
				delete(keyboard.selected, task.ID)
			}
			kanbanKeyboardMu.Unlock()
		}
		kanbanBatchMu.Lock()
		if kanbanBatchStates[s] == st {
			st.running, st.cancel = false, nil
			st.failures = failures
		}
		kanbanBatchMu.Unlock()
		if ctx.Err() != nil {
			return
		}
		s.refreshKanban()
		s.invalidate()
	}()
}

func renderKanbanSelectionBar(c *ui.Context, s *chatState, ks *kanbanState) {
	kanbanKeyboardMu.Lock()
	var tasks []KanbanTask
	if keyboard := kanbanKeyboardStates[s]; keyboard != nil {
		for _, task := range keyboard.tasks {
			if keyboard.selected[task.ID] {
				tasks = append(tasks, task)
			}
		}
	}
	kanbanKeyboardMu.Unlock()
	if len(tasks) == 0 {
		return
	}
	moveable, deletable := kanbanBatchEligible(s, tasks)
	kanbanBatchMu.Lock()
	st := kanbanBatchStates[s]
	if st == nil {
		st = &kanbanBatchState{}
		kanbanBatchStates[s] = st
	}
	running, failed, expanded := st.running, len(st.failures), st.expanded
	confirmDelete := st.confirmDelete
	failures := append([]kanbanBatchFailure(nil), st.failures...)
	kanbanBatchMu.Unlock()
	ui.Column(c.Key("kanban-selection-wrap")).FillWidth().Gap(Space2).Transition(motionDialog(c)).Children(func() {
		ui.Row(c.Key("kanban-selection-bar")).FillWidth().Padding(Space3, Space4).Radius(RadiusMD).Background(ThemeHover(c)).AlignItems(ui.Center).Gap(Space3).Children(func() {
			ui.Text(c, fmt.Sprintf("已选 %d 项", len(tasks))).FontSize(uiFont(TypeFootnote)).FontWeight(600).TextColor(ThemePrimaryText(c)).SingleLine().Shrink(0)
			Button(c.Key("kanban-selection-move"), ButtonOpts{Text: fmt.Sprintf("移回待办 %d/%d", len(moveable), len(tasks)), Label: "移回待办", Disabled: running || len(moveable) == 0, Variant: ButtonSecondary, Size: ControlSM, OnClick: func() { kanbanBatchRun(s, "todo", moveable) }})
			Button(c.Key("kanban-selection-delete"), ButtonOpts{Text: fmt.Sprintf("删除 %d/%d", len(deletable), len(tasks)), Label: "kanban-selection-delete", Disabled: running || len(deletable) == 0, Variant: ButtonSecondary, Size: ControlSM, OnClick: func() {
				kanbanBatchMu.Lock()
				if st := kanbanBatchStates[s]; st != nil {
					st.confirmDelete = len(deletable)
				}
				kanbanBatchMu.Unlock()
				s.invalidate()
			}})
			Button(c.Key("kanban-selection-clear"), ButtonOpts{Text: "取消选择", Label: "取消选择", Disabled: running, Variant: ButtonSecondary, Size: ControlSM, OnClick: func() { kanbanKeyboardClearSelection(s); s.invalidate() }})
			if failed > 0 {
				Button(c.Key("kanban-selection-failures"), ButtonOpts{Text: fmt.Sprintf("%d 项失败", failed), OnClick: func() {
					kanbanBatchMu.Lock()
					if st := kanbanBatchStates[s]; st != nil {
						st.expanded = !st.expanded
					}
					kanbanBatchMu.Unlock()
					s.invalidate()
				}, Variant: ButtonGhost, Size: ControlSM})
			}
		})
		if expanded {
			for i, failure := range failures {
				ui.Text(c, failure.Title+"："+failure.Reason).Key(fmt.Sprintf("kanban-selection-failure-%d", i)).FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
			}
		}
	})
	if confirmDelete > 0 {
		RenderConfirmDialog(c, ConfirmDialog{Key: "kanban-batch-delete", Open: true, Tone: ConfirmDanger, Title: fmt.Sprintf("删除 %d 项任务？", confirmDelete), ConfirmLabel: "删除", ConfirmA11y: "confirm-kanban-batch-delete-confirm", CancelLabel: "取消", CancelA11y: "confirm-kanban-batch-delete-cancel", OnConfirm: func() {
			kanbanBatchMu.Lock()
			if st := kanbanBatchStates[s]; st != nil {
				st.confirmDelete = 0
			}
			kanbanBatchMu.Unlock()
			kanbanBatchRun(s, "delete", deletable)
		}, OnCancel: func() {
			kanbanBatchMu.Lock()
			if st := kanbanBatchStates[s]; st != nil {
				st.confirmDelete = 0
			}
			kanbanBatchMu.Unlock()
			s.invalidate()
		}})
	}
}
