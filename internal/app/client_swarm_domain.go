// 此文件包含 Client 的Swarm方法。
package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"mcode/internal/config"
	"mcode/internal/swarm"
	mcodeui "mcode/internal/ui"
)

// SetSwarmManager 设置 Swarm 多协作者管理器。
func (c *Client) SetSwarmManager(m *swarm.Manager) {
	c.kanbanMu.Lock()
	c.swarm = m
	if m == nil {
		// 管理器已拆除：清掉排队中的合并通知，避免退出后再触发一次全量拉取。
		for id, timer := range c.swarmNotifyTimers {
			timer.Stop()
			delete(c.swarmNotifyTimers, id)
		}
	}
	c.kanbanMu.Unlock()
	if m != nil {
		m.SetOnChange(c.notifySwarmChanged)
		c.startQuestionAutoForward()
	} else {
		c.stopQuestionAutoForward()
	}
}

// startQuestionAutoForward 启动提问自动转交后台循环，重复调用不会叠加。
func (c *Client) startQuestionAutoForward() {
	c.autoForwardMu.Lock()
	defer c.autoForwardMu.Unlock()
	if c.autoForwardCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.autoForwardCancel = cancel
	go c.runQuestionAutoForward(ctx)
}

// stopQuestionAutoForward 停止提问自动转交后台循环。
func (c *Client) stopQuestionAutoForward() {
	c.autoForwardMu.Lock()
	cancel := c.autoForwardCancel
	c.autoForwardCancel = nil
	c.autoForwardMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// SetSwarmPipeline 设置 Swarm 自动合并流水线。
func (c *Client) SetSwarmPipeline(p *swarm.Pipeline) {
	c.kanbanMu.Lock()
	c.swarmPipeline = p
	c.kanbanMu.Unlock()
}

// notifySwarmChanged 在指定主会话的 Swarm 状态变更时通知所有监听者，
// 并重新发布该主会话摘要：Swarm 状态变更不走会话事件，只有重新发布才能让
// SwarmBusy 的变化被桌面通知与远程推送的提醒检测看到。
// 同一主会话在 swarmNotifyDelay 窗口内的多次变更合并成一次通知，保证最后一次一定送达。
func (c *Client) notifySwarmChanged(leadThreadID string) {
	if leadThreadID == "" {
		return
	}
	c.kanbanMu.Lock()
	if c.swarmNotifyTimers == nil {
		c.swarmNotifyTimers = make(map[string]*time.Timer)
	}
	if _, pending := c.swarmNotifyTimers[leadThreadID]; pending {
		// 窗口内已排队：到期时读的是最新状态，这里只需丢弃本次重复通知。
		c.kanbanMu.Unlock()
		return
	}
	c.swarmNotifyTimers[leadThreadID] = time.AfterFunc(swarmNotifyDelay, func() {
		c.flushSwarmChanged(leadThreadID)
	})
	c.kanbanMu.Unlock()
}

// flushSwarmChanged 关闭指定主会话的合并窗口并推送一次最新状态。
func (c *Client) flushSwarmChanged(leadThreadID string) {
	c.kanbanMu.Lock()
	delete(c.swarmNotifyTimers, leadThreadID)
	c.kanbanMu.Unlock()
	c.deliverSwarmChanged(leadThreadID)
}

// deliverSwarmChanged 立即把变更送达监听者（摘要重发 + 全部 watcher 回调）。
func (c *Client) deliverSwarmChanged(leadThreadID string) {
	if leadThreadID != "" && c.backend != nil {
		c.backend.RepublishThreadSummary(leadThreadID)
	}
	c.kanbanMu.Lock()
	watchers := make([]func(string), 0, len(c.swarmWatchers))
	for _, fn := range c.swarmWatchers {
		if fn != nil {
			watchers = append(watchers, fn)
		}
	}
	c.kanbanMu.Unlock()

	for _, fn := range watchers {
		fn(leadThreadID)
	}
}

// Swarm 获取指定会话或其所属主会话的 Swarm 状态。
func (c *Client) Swarm(ctx context.Context, threadID string) (mcodeui.SwarmState, error) {
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()
	if m == nil {
		return mcodeui.SwarmState{
			Available:           false,
			PlansOnly:           c.globalLeadPlansOnly(),
			PlansOnlyOverridden: false,
			Bees:                []mcodeui.SwarmBee{},
		}, nil
	}
	if lead, ok := m.BeeOwner(threadID); ok {
		st := m.State(lead)
		return c.swarmState(ctx, st, lead, true), nil
	}
	st := m.State(threadID)
	return c.swarmState(ctx, st, threadID, false), nil
}

func (c *Client) globalLeadPlansOnly() bool {
	dir := c.dataDir
	if dir == "" && c.remote != nil {
		dir = c.remote.dataDir
	}
	if dir != "" {
		if cfg, err := config.Load(dir); err == nil {
			config.ApplyEnv(&cfg, os.LookupEnv)
			return cfg.Orchestration.LeadPlansOnly
		}
	}
	cfg := config.Defaults()
	config.ApplyEnv(&cfg, os.LookupEnv)
	return cfg.Orchestration.LeadPlansOnly
}

func (c *Client) swarmStateToUI(st swarm.State, leadThreadID string, isBee, available bool) mcodeui.SwarmState {
	res := swarmStateToUI(st, leadThreadID, isBee, available)
	res.Queued = st.Queued
	res.Interrupted = st.Interrupted
	if st.PlansOnly != nil {
		res.PlansOnly = *st.PlansOnly
		res.PlansOnlyOverridden = true
	} else {
		res.PlansOnly = c.globalLeadPlansOnly()
		res.PlansOnlyOverridden = false
	}
	applySwarmThinking(st.Bees, res.Bees)
	return res
}

func (c *Client) swarmState(ctx context.Context, st swarm.State, leadID string, isBee bool) mcodeui.SwarmState {
	result := c.swarmStateToUI(st, leadID, isBee, true)
	if c.backend == nil {
		return result
	}
	for i := range result.Bees {
		h := &result.Bees[i]
		running := h.Status == string(swarm.StatusRunning)
		// 已结束 bee 的进度数据不会再变：命中缓存直接复用，跳过逐 bee 的会话/用量查询。
		// 这些查询随「累计 bee 总数」线性增长，每次都走单连接的 SQLite，是子代理多时
		// 状态推送变慢、会话消息写入被拖住的主因之一。
		if !running {
			if cached, ok := c.cachedSwarmBeeProgress(h.ThreadID); ok {
				h.Provider, h.Model, h.Activity = cached.provider, cached.model, cached.activity
				h.Tokens, h.ToolCalls = cached.tokens, cached.toolCalls
				continue
			}
		}
		// 查询失败时保留已有字段值，并且不把降级结果写进缓存。
		progress := swarmBeeProgress{provider: h.Provider, model: h.Model, activity: h.Activity, tokens: h.Tokens, toolCalls: h.ToolCalls}
		cacheable := true
		if thread, err := c.backend.GetThread(ctx, h.ThreadID); err == nil {
			progress.provider = thread.Provider
			if thread.Model != nil {
				progress.model = *thread.Model
			}
			if thread.LastActivityText != nil {
				progress.activity = *thread.LastActivityText
			}
		} else {
			cacheable = false
		}
		// 用量与工具计数共用一次历史遍历：Usage 内部还会再拉一遍历史，逐 bee 调用会让
		// 查询次数翻倍。
		if _, total, err := c.backend.ThreadUsage(ctx, h.ThreadID); err == nil {
			progress.tokens = int(total.InputTokens + total.OutputTokens)
		} else {
			cacheable = false
		}
		if messages, err := c.History(ctx, h.ThreadID); err == nil {
			progress.toolCalls = 0
			for _, msg := range messages {
				if msg.Kind == "tool" || msg.Role == "tool" {
					progress.toolCalls++
				}
				if running && msg.Role == "assistant" && msg.Kind != "reasoning" && msg.Text != "" {
					progress.activity = msg.Text
				}
			}
		} else {
			cacheable = false
		}
		h.Provider, h.Model, h.Activity = progress.provider, progress.model, progress.activity
		h.Tokens, h.ToolCalls = progress.tokens, progress.toolCalls
		if running {
			h.PendingQuestion = c.pendingBeeQuestion(ctx, h.ThreadID)
			continue
		}
		if cacheable {
			c.storeSwarmBeeProgress(h.ThreadID, progress)
		}
	}
	// 实时活动文本会覆盖「思考中」提示，最后按无进展时长补回。
	applySwarmThinking(st.Bees, result.Bees)
	return result
}

// cachedSwarmBeeProgress 读取已结束 bee 的进度缓存；未命中时返回 false，由调用方重新查询。
func (c *Client) cachedSwarmBeeProgress(threadID string) (swarmBeeProgress, bool) {
	c.kanbanMu.Lock()
	defer c.kanbanMu.Unlock()
	progress, ok := c.swarmBeeProgress[threadID]
	return progress, ok
}

// storeSwarmBeeProgress 缓存已结束 bee 的进度数据，供后续状态拉取复用。
func (c *Client) storeSwarmBeeProgress(threadID string, progress swarmBeeProgress) {
	c.kanbanMu.Lock()
	defer c.kanbanMu.Unlock()
	if c.swarmBeeProgress == nil {
		c.swarmBeeProgress = make(map[string]swarmBeeProgress)
	}
	// 缓存只需覆盖当前活跃的 bee，超过上限时整体丢弃重建，避免长跑进程里无限增长。
	if len(c.swarmBeeProgress) > 512 {
		c.swarmBeeProgress = make(map[string]swarmBeeProgress)
	}
	c.swarmBeeProgress[threadID] = progress
}

// SetSwarmEnabled 开启或关闭指定主会话的 Swarm 协作模式。
func (c *Client) SetSwarmEnabled(ctx context.Context, threadID string, enabled bool) (mcodeui.SwarmState, error) {
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()
	if m == nil {
		return mcodeui.SwarmState{}, errors.New("编排服务不可用")
	}

	leadID := threadID
	isBee := false
	if lead, ok := m.BeeOwner(threadID); ok {
		leadID = lead
		isBee = true
	}

	st := m.SetEnabled(leadID, enabled)
	return c.swarmStateToUI(st, leadID, isBee, true), nil
}

// SetSwarmPlansOnly 设置指定主会话的 Swarm 只规划开关覆盖。
func (c *Client) SetSwarmPlansOnly(ctx context.Context, threadID string, plansOnly bool) (mcodeui.SwarmState, error) {
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()
	if m == nil {
		return mcodeui.SwarmState{}, errors.New("编排服务不可用")
	}

	leadID := threadID
	isBee := false
	if lead, ok := m.BeeOwner(threadID); ok {
		leadID = lead
		isBee = true
	}

	v := plansOnly
	st := m.SetLeadPlansOnly(leadID, &v)
	return c.swarmStateToUI(st, leadID, isBee, true), nil
}

// StopSwarmBee 停止指定的子会话。
func (c *Client) StopSwarmBee(ctx context.Context, leadThreadID, beeThreadID string) error {
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()
	if m == nil {
		return errors.New("编排服务不可用")
	}
	if err := m.StopBee(ctx, leadThreadID, beeThreadID); err != nil {
		return err
	}
	return nil
}

// StopAllSwarmBees 停止指定主会话下的所有子会话。
func (c *Client) StopAllSwarmBees(ctx context.Context, leadThreadID string) error {
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()
	if m == nil {
		return errors.New("编排服务不可用")
	}
	if err := m.StopAll(ctx, leadThreadID); err != nil {
		return err
	}
	return nil
}

// ClearFinishedSwarmBees 清理指定主会话下已结束的子会话。
func (c *Client) ClearFinishedSwarmBees(ctx context.Context, leadThreadID string) error {
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()
	if m == nil {
		return errors.New("编排服务不可用")
	}
	if err := m.ClearFinished(ctx, leadThreadID); err != nil {
		return err
	}
	return nil
}

// pendingBeeQuestion 返回子线程当前待回答的提问，没有则返回 nil。
func (c *Client) pendingBeeQuestion(ctx context.Context, beeThreadID string) *mcodeui.SwarmBeeQuestion {
	approvals, err := c.Approvals(ctx, beeThreadID)
	if err != nil {
		return nil
	}
	for _, a := range approvals {
		if a.Kind != mcodeui.ApprovalKindQuestion {
			continue
		}
		q := &mcodeui.SwarmBeeQuestion{
			RequestID: a.RequestID,
			Questions: a.Questions,
			Forwarded: c.questionForwarded(beeThreadID, a.RequestID),
		}
		if delay := c.autoForwardQuestionDelay(); delay > 0 && !q.Forwarded {
			q.AutoForwardAt = c.noteQuestionSeen(beeThreadID+"/"+a.RequestID, time.Now()).Add(delay)
		}
		return q
	}
	return nil
}

// noteQuestionSeen 记录提问首次出现的时间并返回该时间，重复调用返回首次值。
func (c *Client) noteQuestionSeen(key string, now time.Time) time.Time {
	c.forwardedMu.Lock()
	defer c.forwardedMu.Unlock()
	if c.questionFirstSeen == nil {
		c.questionFirstSeen = make(map[string]time.Time)
	}
	if first, ok := c.questionFirstSeen[key]; ok {
		return first
	}
	c.questionFirstSeen[key] = now
	return now
}

// markQuestionAutoForwardFailed 记录一次自动转交失败，避免同一提问反复重试。
func (c *Client) markQuestionAutoForwardFailed(key string) {
	c.forwardedMu.Lock()
	defer c.forwardedMu.Unlock()
	if c.questionAutoForwardFailed == nil {
		c.questionAutoForwardFailed = make(map[string]bool)
	}
	c.questionAutoForwardFailed[key] = true
}

// questionAutoForwardFailedBefore 报告该提问的自动转交是否已经失败过。
func (c *Client) questionAutoForwardFailedBefore(key string) bool {
	c.forwardedMu.Lock()
	defer c.forwardedMu.Unlock()
	return c.questionAutoForwardFailed[key]
}

// dropStaleQuestionTimers 清理已不再待答的提问对应的计时与失败标记。
func (c *Client) dropStaleQuestionTimers(active map[string]bool) {
	c.forwardedMu.Lock()
	defer c.forwardedMu.Unlock()
	for key := range c.questionFirstSeen {
		if !active[key] {
			delete(c.questionFirstSeen, key)
		}
	}
	for key := range c.questionAutoForwardFailed {
		if !active[key] {
			delete(c.questionAutoForwardFailed, key)
		}
	}
}

// autoForwardQuestionDelay 返回配置中子线程提问自动转交主线程的等待时长，0 表示关闭。
func (c *Client) autoForwardQuestionDelay() time.Duration {
	dir := c.dataDir
	if dir == "" && c.remote != nil {
		dir = c.remote.dataDir
	}
	if dir != "" {
		if cfg, err := config.Load(dir); err == nil {
			config.ApplyEnv(&cfg, os.LookupEnv)
			return cfg.Orchestration.AutoForwardQuestionDelay()
		}
	}
	cfg := config.Defaults()
	config.ApplyEnv(&cfg, os.LookupEnv)
	return cfg.Orchestration.AutoForwardQuestionDelay()
}

// autoForwardQuestionsOnce 检查一轮待答提问，把超过配置等待时长的提问自动转交主线程回答。
func (c *Client) autoForwardQuestionsOnce(ctx context.Context, now time.Time) {
	delay := c.autoForwardQuestionDelay()
	if delay <= 0 {
		return
	}
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()
	if m == nil {
		return
	}
	active := make(map[string]bool)
	for _, leadID := range m.LeadIDs() {
		for _, h := range m.State(leadID).Bees {
			// 不按 bee 状态过滤：待答提问只存在于仍在进行的轮次，以后端请求列表为准。
			if h.Merged || h.MergeDiscarded {
				continue
			}
			q := c.pendingBeeQuestion(ctx, h.ThreadID)
			if q == nil || q.Forwarded {
				continue
			}
			key := h.ThreadID + "/" + q.RequestID
			active[key] = true
			if c.questionAutoForwardFailedBefore(key) {
				continue
			}
			if now.Sub(c.noteQuestionSeen(key, now)) < delay {
				continue
			}
			if err := c.forwardBeeQuestion(ctx, leadID, h.ThreadID, q.RequestID, delay); err != nil {
				log.Printf("自动转交子代理提问失败（%s）: %v", key, err)
				c.markQuestionAutoForwardFailed(key)
			}
		}
	}
	c.dropStaleQuestionTimers(active)
}

// runQuestionAutoForward 每 5 秒检查一次待答提问，超时无人回答时自动转交主线程。
func (c *Client) runQuestionAutoForward(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			c.autoForwardQuestionsOnce(ctx, now)
		}
	}
}

// questionForwarded 报告子线程提问是否已转交主线程回答。
func (c *Client) questionForwarded(beeThreadID, requestID string) bool {
	c.forwardedMu.Lock()
	defer c.forwardedMu.Unlock()
	return c.forwardedQuestions[beeThreadID+"/"+requestID]
}

// markQuestionForwarded 记录子线程提问已转交主线程回答。
func (c *Client) markQuestionForwarded(beeThreadID, requestID string) {
	c.forwardedMu.Lock()
	defer c.forwardedMu.Unlock()
	if c.forwardedQuestions == nil {
		c.forwardedQuestions = make(map[string]bool)
	}
	c.forwardedQuestions[beeThreadID+"/"+requestID] = true
}

// ForwardBeeQuestion 把子线程的提问转给主线程回答：主线程在回复里写答案块，系统解析后自动提交。
func (c *Client) ForwardBeeQuestion(ctx context.Context, leadThreadID, beeThreadID, requestID string) error {
	return c.forwardBeeQuestion(ctx, leadThreadID, beeThreadID, requestID, 0)
}

// forwardBeeQuestion 实现提问转交；autoWaited 大于 0 表示超时自动转交，消息里附带等待时长说明。
func (c *Client) forwardBeeQuestion(ctx context.Context, leadThreadID, beeThreadID, requestID string, autoWaited time.Duration) error {
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()
	if m == nil {
		return errors.New("编排服务不可用")
	}
	if owner, ok := m.BeeOwner(beeThreadID); !ok || owner != leadThreadID {
		return errors.New("子线程不属于该会话")
	}

	approvals, err := c.Approvals(ctx, beeThreadID)
	if err != nil {
		return err
	}
	var target *mcodeui.Approval
	for i := range approvals {
		if approvals[i].RequestID == requestID && approvals[i].Kind == mcodeui.ApprovalKindQuestion {
			target = &approvals[i]
			break
		}
	}
	if target == nil {
		return errors.New("提问已结束")
	}

	questions := make([]swarm.ForwardQuestion, 0, len(target.Questions))
	for _, q := range target.Questions {
		choices := make([]string, 0, len(q.Choices))
		for _, ch := range q.Choices {
			choices = append(choices, ch.Label)
		}
		questions = append(questions, swarm.ForwardQuestion{
			ID:             q.ID,
			Header:         q.Header,
			Prompt:         q.Prompt,
			Choices:        choices,
			AllowsMultiple: q.AllowsMultiple,
			AllowsOther:    q.AllowsOther,
		})
	}

	name := ""
	for _, h := range m.State(leadThreadID).Bees {
		if h.ThreadID == beeThreadID {
			name = h.Name
			break
		}
	}
	var msg string
	if autoWaited > 0 {
		msg = swarm.BuildAutoForwardQuestionMessage(name, beeThreadID, requestID, autoWaited, questions)
	} else {
		msg = swarm.BuildForwardQuestionMessage(name, beeThreadID, requestID, questions)
	}
	if err := c.backend.SendSwarmReport(ctx, leadThreadID, msg); err != nil {
		return err
	}
	c.markQuestionForwarded(beeThreadID, requestID)
	c.notifySwarmChanged(leadThreadID)
	return nil
}

// SwarmMergeDiff 获取 bee 待合并改动（Diff 为 unified diff 文本，FilesChanged 为改动文件）。
func (c *Client) SwarmMergeDiff(ctx context.Context, leadThreadID, beeThreadID string) (mcodeui.ThreadDiff, error) {
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()
	if m == nil {
		return mcodeui.ThreadDiff{}, errors.New("编排服务不可用")
	}
	patch, err := m.MergeDiff(ctx, leadThreadID, beeThreadID)
	if err != nil {
		return mcodeui.ThreadDiff{}, err
	}
	var filesChanged []string
	st := m.State(leadThreadID)
	for _, h := range st.Bees {
		if h.ThreadID == beeThreadID {
			filesChanged = make([]string, len(h.ChangedFiles))
			copy(filesChanged, h.ChangedFiles)
			break
		}
	}
	return mcodeui.ThreadDiff{
		Diff:         string(patch),
		FilesChanged: filesChanged,
	}, nil
}

// ApproveSwarmMerge 批准并应用 bee 的改动。
func (c *Client) ApproveSwarmMerge(ctx context.Context, leadThreadID, beeThreadID string) error {
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()
	if m == nil {
		return errors.New("编排服务不可用")
	}
	if _, err := m.ApproveMerge(ctx, leadThreadID, beeThreadID); err != nil {
		return err
	}
	return nil
}

// DiscardSwarmMerge 放弃 bee 的改动。
func (c *Client) DiscardSwarmMerge(ctx context.Context, leadThreadID, beeThreadID string) error {
	c.kanbanMu.Lock()
	m := c.swarm
	c.kanbanMu.Unlock()
	if m == nil {
		return errors.New("编排服务不可用")
	}
	if err := m.DiscardMerge(ctx, leadThreadID, beeThreadID); err != nil {
		return err
	}
	return nil
}

// WatchSwarm 监听 Swarm 状态变更，回调接收发生变更的主会话 ID。
func (c *Client) WatchSwarm(onChange func(leadThreadID string)) (stop func()) {
	c.kanbanMu.Lock()
	c.nextWatchID++
	id := c.nextWatchID
	if c.swarmWatchers == nil {
		c.swarmWatchers = make(map[int]func(string))
	}
	c.swarmWatchers[id] = onChange
	c.kanbanMu.Unlock()

	return func() {
		c.kanbanMu.Lock()
		delete(c.swarmWatchers, id)
		c.kanbanMu.Unlock()
	}
}

// SwarmPairs 获取所有 Swarm 主导与执行模型配对列表。
func (c *Client) SwarmPairs(ctx context.Context) ([]mcodeui.SwarmPair, error) {
	dir := c.effectiveDataDir()
	if dir == "" {
		return nil, errors.New("dataDir 未初始化")
	}
	cfg, err := config.Load(dir)
	if err != nil {
		return nil, err
	}
	res := make([]mcodeui.SwarmPair, len(cfg.Orchestration.Pairs))
	copy(res, cfg.Orchestration.Pairs)
	return res, nil
}

// AddSwarmPair 添加一组 Swarm 主导与执行模型配对。
func (c *Client) AddSwarmPair(ctx context.Context, p mcodeui.SwarmPair) (mcodeui.SwarmPair, error) {
	dir := c.effectiveDataDir()
	if dir == "" {
		return mcodeui.SwarmPair{}, errors.New("dataDir 未初始化")
	}
	var added mcodeui.SwarmPair
	err := config.Update(dir, func(latest *config.Config) error {
		added = latest.Orchestration.AddPair(p)
		return nil
	})
	if err != nil {
		return mcodeui.SwarmPair{}, err
	}
	return added, nil
}

// UpdateSwarmPair 更新一组已有的 Swarm 主导与执行模型配对。
func (c *Client) UpdateSwarmPair(ctx context.Context, p mcodeui.SwarmPair) error {
	dir := c.effectiveDataDir()
	if dir == "" {
		return errors.New("dataDir 未初始化")
	}
	return config.Update(dir, func(latest *config.Config) error {
		if !latest.Orchestration.UpdatePair(p) {
			return fmt.Errorf("swarm pair not found: %s", p.ID)
		}
		return nil
	})
}

// RemoveSwarmPair 根据 ID 移除一组 Swarm 配对。
func (c *Client) RemoveSwarmPair(ctx context.Context, id string) error {
	dir := c.effectiveDataDir()
	if dir == "" {
		return errors.New("dataDir 未初始化")
	}
	return config.Update(dir, func(latest *config.Config) error {
		if !latest.Orchestration.RemovePair(id) {
			return fmt.Errorf("swarm pair not found: %s", id)
		}
		return nil
	})
}
