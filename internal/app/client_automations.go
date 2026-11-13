// 此文件包含 Client 的自动化方法。
package app

import (
	"context"
	"errors"
	"fmt"

	"mcode/internal/automation"
	mcodeui "mcode/internal/ui"
)

func (c *Client) notifyAutomationChanged() {
	c.kanbanMu.Lock()
	watchers := make([]func(), 0, len(c.automationWatchers))
	for _, fn := range c.automationWatchers {
		if fn != nil {
			watchers = append(watchers, fn)
		}
	}
	c.kanbanMu.Unlock()
	for _, fn := range watchers {
		fn()
	}
}

func (c *Client) WatchAutomations(onChange func()) (stop func()) {
	c.kanbanMu.Lock()
	c.nextWatchID++
	id := c.nextWatchID
	if c.automationWatchers == nil {
		c.automationWatchers = make(map[int]func())
	}
	c.automationWatchers[id] = onChange
	c.kanbanMu.Unlock()
	return func() {
		c.kanbanMu.Lock()
		delete(c.automationWatchers, id)
		c.kanbanMu.Unlock()
	}
}

// Automations 获取指定项目的自动化列表。
func (c *Client) Automations(ctx context.Context, projectID string) ([]mcodeui.Automation, error) {
	c.kanbanMu.Lock()
	a := c.automation
	c.kanbanMu.Unlock()
	if a == nil {
		return nil, wrapConflictErr(errors.New("自动化服务不可用"))
	}
	defs, err := a.List(ctx, projectID)
	if err != nil {
		return nil, wrapConflictErr(err)
	}
	res := make([]mcodeui.Automation, len(defs))
	for i, d := range defs {
		res[i] = AutomationToUI(d)
	}
	return res, nil
}

// CreateAutomation 创建自动化任务。
func (c *Client) CreateAutomation(ctx context.Context, in mcodeui.AutomationInput) (mcodeui.Automation, error) {
	c.kanbanMu.Lock()
	a := c.automation
	c.kanbanMu.Unlock()
	if a == nil {
		return mcodeui.Automation{}, wrapConflictErr(errors.New("自动化服务不可用"))
	}
	spec := AutomationInputToSpec(in, automation.Spec{})
	def, err := a.Create(ctx, automation.CreateInput{
		ProjectID: in.ProjectID,
		Name:      in.Name,
		Prompt:    in.Prompt,
		Spec:      spec,
		Enabled:   in.Enabled,
	})
	if err != nil {
		return mcodeui.Automation{}, wrapConflictErr(err)
	}
	return AutomationToUI(def), nil
}

// UpdateAutomation 更新自动化任务。
func (c *Client) UpdateAutomation(ctx context.Context, id string, in mcodeui.AutomationInput, revision int) (mcodeui.Automation, error) {
	c.kanbanMu.Lock()
	a := c.automation
	c.kanbanMu.Unlock()
	if a == nil {
		return mcodeui.Automation{}, wrapConflictErr(errors.New("自动化服务不可用"))
	}
	existing, err := a.Get(ctx, id)
	if err != nil {
		return mcodeui.Automation{}, wrapConflictErr(err)
	}
	spec := AutomationInputToSpec(in, existing.Spec)
	update := automation.UpdateInput{
		Name:   &in.Name,
		Prompt: &in.Prompt,
		Spec:   &spec,
	}
	if in.Enabled != existing.Enabled {
		update.Enabled = &in.Enabled
	}
	def, err := a.Update(ctx, id, update, revision)
	if err != nil {
		return mcodeui.Automation{}, wrapConflictErr(err)
	}
	return AutomationToUI(def), nil
}

// SetAutomationEnabled 只改变启用状态，显式启用时重置失败状态。
func (c *Client) SetAutomationEnabled(ctx context.Context, id string, enabled bool, revision int) (mcodeui.Automation, error) {
	c.kanbanMu.Lock()
	a := c.automation
	c.kanbanMu.Unlock()
	if a == nil {
		return mcodeui.Automation{}, wrapConflictErr(errors.New("自动化服务不可用"))
	}
	def, err := a.Update(ctx, id, automation.UpdateInput{Enabled: &enabled}, revision)
	if err != nil {
		return mcodeui.Automation{}, wrapConflictErr(err)
	}
	return AutomationToUI(def), nil
}

// DeleteAutomation 删除自动化任务。
func (c *Client) DeleteAutomation(ctx context.Context, id string) error {
	c.kanbanMu.Lock()
	a := c.automation
	c.kanbanMu.Unlock()
	if a == nil {
		return wrapConflictErr(errors.New("自动化服务不可用"))
	}
	return wrapConflictErr(a.Delete(ctx, id))
}

// RunAutomation 立即触发运行一次自动化任务。
func (c *Client) RunAutomation(ctx context.Context, id string) (mcodeui.AutomationRun, error) {
	c.kanbanMu.Lock()
	a := c.automation
	c.kanbanMu.Unlock()
	if a == nil {
		return mcodeui.AutomationRun{}, wrapConflictErr(errors.New("自动化服务不可用"))
	}
	run, err := a.RunNow(ctx, id)
	if err != nil {
		if errors.Is(err, automation.ErrSchedulerPassive) {
			return mcodeui.AutomationRun{}, fmt.Errorf("%w: %v", mcodeui.ErrAutomationSchedulerPassive, err)
		}
		return mcodeui.AutomationRun{}, wrapConflictErr(err)
	}
	return AutomationRunToUI(run), nil
}

// AutomationSchedulerStatus 返回当前实例的定时调度锁状态，供页面提示使用。
func (c *Client) AutomationSchedulerStatus(ctx context.Context) (mcodeui.AutomationSchedulerStatus, error) {
	c.kanbanMu.Lock()
	a := c.automation
	c.kanbanMu.Unlock()
	if a == nil {
		return mcodeui.AutomationSchedulerStatus{}, wrapConflictErr(errors.New("自动化服务不可用"))
	}
	st := a.SchedulerLockState()
	return mcodeui.AutomationSchedulerStatus{
		State: mcodeui.AutomationSchedulerState(st.State),
		Error: st.Error,
	}, nil
}

// AutomationRuns 获取指定自动化任务的历史运行记录。
func (c *Client) AutomationRuns(ctx context.Context, id string, limit int) ([]mcodeui.AutomationRun, error) {
	c.kanbanMu.Lock()
	a := c.automation
	c.kanbanMu.Unlock()
	if a == nil {
		return nil, wrapConflictErr(errors.New("自动化服务不可用"))
	}
	runs, err := a.Runs(ctx, id, limit)
	if err != nil {
		return nil, wrapConflictErr(err)
	}
	res := make([]mcodeui.AutomationRun, len(runs))
	for i, r := range runs {
		res[i] = AutomationRunToUI(r)
	}
	return res, nil
}
