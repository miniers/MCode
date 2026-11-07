package app

import (
	"context"
	"errors"

	mcodeui "mcode/internal/ui"
)

// AutomationRunsState 返回运行记录及其已读/归档状态。
// includeArchived 为 true 时一并返回已归档记录；运行记录默认列表不含已归档。
func (c *Client) AutomationRunsState(ctx context.Context, id string, limit int, includeArchived bool) (mcodeui.AutomationRunsState, error) {
	c.kanbanMu.Lock()
	a := c.automation
	c.kanbanMu.Unlock()
	if a == nil {
		return mcodeui.AutomationRunsState{}, wrapConflictErr(errors.New("自动化服务不可用"))
	}
	runs, err := a.RunsFiltered(ctx, id, limit, includeArchived)
	if err != nil {
		return mcodeui.AutomationRunsState{}, wrapConflictErr(err)
	}
	state := mcodeui.AutomationRunsState{
		Runs:   make([]mcodeui.AutomationRun, len(runs)),
		States: make([]mcodeui.AutomationRunReadState, len(runs)),
	}
	for i, r := range runs {
		state.Runs[i] = AutomationRunToUI(r)
		meta := mcodeui.AutomationRunReadState{RunID: r.ID, Archived: r.Archived}
		if r.ReadAt != nil {
			meta.ReadAt = *r.ReadAt
		}
		state.States[i] = meta
	}
	return state, nil
}

// MarkAutomationRunRead 把单条运行记录标记为已读。
func (c *Client) MarkAutomationRunRead(ctx context.Context, runID string) error {
	c.kanbanMu.Lock()
	a := c.automation
	c.kanbanMu.Unlock()
	if a == nil {
		return wrapConflictErr(errors.New("自动化服务不可用"))
	}
	return wrapConflictErr(a.MarkRunRead(ctx, runID))
}

// MarkAllAutomationRunsRead 把指定自动化下的运行记录全部标为已读，返回受影响条数。
func (c *Client) MarkAllAutomationRunsRead(ctx context.Context, automationID string) (int, error) {
	c.kanbanMu.Lock()
	a := c.automation
	c.kanbanMu.Unlock()
	if a == nil {
		return 0, wrapConflictErr(errors.New("自动化服务不可用"))
	}
	n, err := a.MarkAllRunsRead(ctx, automationID)
	if err != nil {
		return 0, wrapConflictErr(err)
	}
	return int(n), nil
}

// SetAutomationRunArchived 归档或取消归档单条运行记录。
func (c *Client) SetAutomationRunArchived(ctx context.Context, runID string, archived bool) error {
	c.kanbanMu.Lock()
	a := c.automation
	c.kanbanMu.Unlock()
	if a == nil {
		return wrapConflictErr(errors.New("自动化服务不可用"))
	}
	return wrapConflictErr(a.ArchiveRun(ctx, runID, archived))
}

// AutomationUnreadCounts 返回项目下每个自动化的未读运行记录数。
func (c *Client) AutomationUnreadCounts(ctx context.Context, projectID string) (map[string]int, error) {
	c.kanbanMu.Lock()
	a := c.automation
	c.kanbanMu.Unlock()
	if a == nil {
		return map[string]int{}, nil
	}
	counts, err := a.UnreadCounts(ctx, projectID)
	if err != nil {
		return nil, wrapConflictErr(err)
	}
	return counts, nil
}
