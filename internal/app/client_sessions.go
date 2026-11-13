// 此文件包含 Client 的会话与消息方法。
package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	mcodeui "mcode/internal/ui"
)

// Projects 调用 ListProjects 获取已登记的项目列表。
func (c *Client) Projects(ctx context.Context) ([]mcodeui.Project, error) {
	res, err := c.backend.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	return ProjectsSummaryToUI(res.Projects), nil
}

// AddProject 调用 AddProjectPath 添加指定本地目录的项目。
func (c *Client) AddProject(ctx context.Context, path string) (mcodeui.Project, error) {
	p, err := c.backend.AddProjectPath(ctx, path)
	if err != nil {
		return mcodeui.Project{}, err
	}
	return ProjectSummaryToUI(p), nil
}

// Threads 调用 ListThreads 并按 projectID 过滤，通过循环分页取完全部会话。
func (c *Client) Threads(ctx context.Context, projectID string) ([]mcodeui.Thread, error) {
	var all []wire.ThreadSummary
	var cursor *string
	limit := 100
	for {
		params := wire.ThreadListParams{
			Cursor: cursor,
			Limit:  &limit,
		}
		if projectID != "" {
			params.ProjectID = &projectID
		}
		res, err := c.backend.ListThreads(ctx, params)
		if err != nil {
			return nil, err
		}
		for _, t := range res.Threads {
			if projectID == "" || t.ProjectID == projectID {
				all = append(all, t)
			}
		}
		if !res.HasMore || res.NextCursor == nil || *res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return ThreadsSummaryToUI(all), nil
}

// ArchivedThreads 调用 ListThreads 并查询所有已归档的会话列表，通过循环分页取完全部并按更新时间倒序返回。
func (c *Client) ArchivedThreads(ctx context.Context) ([]mcodeui.Thread, error) {
	if c.backend == nil {
		return nil, nil
	}
	var all []wire.ThreadSummary
	var cursor *string
	limit := 100
	incArchived := true
	for {
		params := wire.ThreadListParams{
			Cursor:          cursor,
			Limit:           &limit,
			IncludeArchived: &incArchived,
		}
		res, err := c.backend.ListThreads(ctx, params)
		if err != nil {
			return nil, err
		}
		for _, t := range res.Threads {
			if t.IsArchived {
				all = append(all, t)
			}
		}
		if !res.HasMore || res.NextCursor == nil || *res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return ThreadsSummaryToUI(all), nil
}

// History 调用 ThreadHistory，循环分页取完全部历史消息，并按时间正序返回。
func (c *Client) History(ctx context.Context, threadID string) ([]mcodeui.Message, error) {
	var pages [][]wire.ThreadMessage
	var cursor *string
	limit := 100
	for {
		res, err := c.backend.ThreadHistory(ctx, wire.HistoryParams{
			ThreadID: threadID,
			Cursor:   cursor,
			Limit:    &limit,
		})
		if err != nil {
			return nil, err
		}
		pages = append(pages, res.Messages)
		if !res.HasMore || res.NextCursor == nil || *res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}

	var total int
	for _, p := range pages {
		total += len(p)
	}
	all := make([]mcodeui.Message, 0, total)
	// ThreadHistory 按时间反向翻页，pages[0] 为最新页，倒序遍历还原为时间正序
	for i := len(pages) - 1; i >= 0; i-- {
		for _, m := range pages[i] {
			all = append(all, ThreadMessageToUI(m))
		}
	}
	return all, nil
}

// Providers 调用 ListProviders 获取可用的 Agent 提供方列表。
func (c *Client) Providers(ctx context.Context) ([]mcodeui.ProviderInfo, error) {
	res, err := c.backend.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	return ProvidersWireToUI(res.Providers), nil
}

// RefreshProviders 强制刷新供应商模型目录，并返回包含成功结果的最新快照。
func (c *Client) RefreshProviders(ctx context.Context) ([]mcodeui.ProviderInfo, error) {
	refreshErr := c.backend.RefreshProviderModels(ctx)
	providers, err := c.Providers(ctx)
	if err != nil {
		return nil, err
	}
	return providers, refreshErr
}

// CreateThreadWithOptions 调用 CreateThread 创建新线程，actionID 使用 crypto/rand 生成 UUID v4。
// 仅对非空字段设置 wire.ComposerOptions 的 Provider、Model、Effort、SwarmBeesProvider、SwarmBeesModel、SwarmBeesEffort 指针，
// Fast 为 true 时设置 FastMode；编排开关总是显式下发，避免被服务端的记忆补齐覆盖。
func (c *Client) CreateThreadWithOptions(ctx context.Context, projectID string, opts mcodeui.NewThreadOptions) (string, error) {
	params := wire.ThreadCreateParams{
		ProjectID: projectID,
		ActionID:  newUUID(),
	}
	if opts.Provider != "" || opts.Model != "" || opts.Effort != "" || opts.Fast || opts.BeesProvider != "" || opts.BeesModel != "" || opts.BeesEffort != "" || opts.PairID != "" {
		cOpts := &wire.ComposerOptions{}
		if opts.Provider != "" {
			p := opts.Provider
			cOpts.Provider = &p
		}
		if opts.Model != "" {
			m := opts.Model
			cOpts.Model = &m
		}
		if opts.Effort != "" {
			e := opts.Effort
			cOpts.Effort = &e
		}
		if opts.Fast {
			f := true
			cOpts.FastMode = &f
		}
		if opts.BeesProvider != "" {
			hp := opts.BeesProvider
			cOpts.SwarmBeesProvider = &hp
		}
		if opts.BeesModel != "" {
			hm := opts.BeesModel
			cOpts.SwarmBeesModel = &hm
		}
		if opts.BeesEffort != "" {
			he := opts.BeesEffort
			cOpts.SwarmBeesEffort = &he
		}
		// 选中的配对随创建参数下发；空串表示不传，由服务端按模型推断。
		if opts.PairID != "" {
			pid := opts.PairID
			cOpts.SwarmPairID = &pid
		}
		params.Options = cOpts
	}

	// 桌面端的编排开关总是显式下发：null 会被服务端当成“没传”而用记忆补齐覆盖。
	if params.Options == nil {
		params.Options = &wire.ComposerOptions{}
	}
	swarmEnabled := opts.SwarmEnabled
	params.Options.SwarmEnabled = &swarmEnabled

	// 默认工作区模式 (DefaultWorkspaceMode): local 或 worktree
	if cfg, ok := c.loadConfig(); ok {
		mode := cfg.SourceControl.DefaultWorkspaceMode
		if mode != "" {
			if params.Options == nil {
				params.Options = &wire.ComposerOptions{}
			}
			if params.Options.WorkspaceMode == nil {
				params.Options.WorkspaceMode = &mode
			}
		}
	}

	if opts.Provisional {
		provisional := true
		params.Provisional = &provisional
	}

	res, err := c.backend.CreateThread(ctx, params)
	if err != nil {
		return "", err
	}
	return res.ThreadID, nil
}

// CreateThread 调用 CreateThreadWithOptions 创建新线程，provider 为空时表示使用默认值。
func (c *Client) CreateThread(ctx context.Context, projectID, provider string) (string, error) {
	return c.CreateThreadWithOptions(ctx, projectID, mcodeui.NewThreadOptions{Provider: provider})
}

// Send 调用 SendMessage 发送消息，actionID 使用 crypto/rand 生成 UUID v4。
// delegated 为 true 表示消息被交给子线程处理，本会话不会开始新一轮。
func (c *Client) Send(ctx context.Context, threadID, text string) (delegated bool, err error) {
	res, err := c.backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     text,
		ActionID: newUUID(),
	})
	if err != nil {
		return false, err
	}
	return res.DelegatedThreadID != nil, nil
}

// SendWithAttachments 调用 SendMessage 向指定会话发送附带附件的消息。
// delegated 为 true 表示消息被交给子线程处理，本会话不会开始新一轮。
func (c *Client) SendWithAttachments(ctx context.Context, threadID, text string, attachmentIDs []string) (delegated bool, err error) {
	res, err := c.backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID:    threadID,
		Text:        text,
		ActionID:    newUUID(),
		Attachments: attachmentIDs,
	})
	if err != nil {
		return false, err
	}
	return res.DelegatedThreadID != nil, nil
}

// UploadAttachment 调用 AttachmentsUpload 上传单张图片附件，返回附件元数据。
func (c *Client) UploadAttachment(ctx context.Context, name, mimeType string, data []byte) (mcodeui.Attachment, error) {
	var mimePtr *string
	if mimeType != "" {
		m := mimeType
		mimePtr = &m
	}
	res, err := c.backend.AttachmentsUpload(ctx, wire.AttachmentsUploadParams{
		Name:     name,
		MimeType: mimePtr,
		Data:     base64.StdEncoding.EncodeToString(data),
	})
	if err != nil {
		return mcodeui.Attachment{}, err
	}
	return mcodeui.Attachment{
		ID:        res.AttachmentID,
		Name:      res.Name,
		MimeType:  res.MimeType,
		ByteCount: res.ByteCount,
	}, nil
}

// Commands 获取指定会话可用的斜杠命令列表（包含内置命令与技能）。
func (c *Client) Commands(ctx context.Context, threadID string) ([]mcodeui.SlashCommand, error) {
	var threadIDPtr *string
	if threadID != "" {
		t := threadID
		threadIDPtr = &t
	}
	res, err := c.backend.ThreadCommands(ctx, wire.ThreadsCommandsParams{
		ThreadID: threadIDPtr,
	})
	if err != nil {
		return nil, err
	}
	cmds := make([]mcodeui.SlashCommand, len(res.Commands))
	for i, cmd := range res.Commands {
		cmds[i] = mcodeui.SlashCommand{
			Name:      cmd.Name,
			Detail:    cmd.Detail,
			Kind:      cmd.Kind,
			IsBuiltIn: cmd.IsBuiltIn,
		}
	}
	return cmds, nil
}

// Interrupt 调用 InterruptCurrent 中断指定会话当前轮次回复，没有在运行时什么也不做。
// alreadyStopped 为 true 表示后端确认该会话本就没有在运行的轮次。
func (c *Client) Interrupt(ctx context.Context, threadID string) (alreadyStopped bool, err error) {
	res, err := c.backend.InterruptCurrent(ctx, threadID)
	if err != nil {
		return false, err
	}
	return res.State == wire.InterruptStateAlreadyStopped, nil
}

// KillRunningCommand 终止指定会话里正在运行的那条命令的子进程树，不中断本轮对话；
// messageID 用于定位该次工具调用的开始时间，为空时退回本轮开始时间。返回被杀掉的进程数。
func (c *Client) KillRunningCommand(ctx context.Context, threadID, messageID string) (killed int, err error) {
	if c.backend == nil {
		return 0, errors.New("backend 不可用")
	}
	return c.backend.KillRunningCommand(ctx, threadID, messageID)
}

// RunningCommandActivity 返回按工具消息 ID 归集的命令进程快照。
func (c *Client) RunningCommandActivity(ctx context.Context, threadID string) (map[string][]mcodeui.CommandProc, error) {
	if c.backend == nil {
		return nil, errors.New("backend 不可用")
	}
	activity, err := c.backend.RunningCommandActivity(ctx, threadID)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]mcodeui.CommandProc, len(activity))
	for id, processes := range activity {
		result[id] = make([]mcodeui.CommandProc, len(processes))
		for i, p := range processes {
			result[id][i] = mcodeui.CommandProc{PID: p.PID, PPID: p.PPID, StartTime: p.StartTime, CPUTime: p.CPUTime, RSS: p.RSS, Command: p.Command}
		}
	}
	return result, nil
}

// BackgroundTaskLog 获取指定后台任务的日志。
func (c *Client) BackgroundTaskLog(ctx context.Context, threadID, messageID string) (string, bool, error) {
	if c.backend == nil {
		return "", false, errors.New("backend 不可用")
	}
	return c.backend.BackgroundTaskLog(ctx, threadID, messageID)
}

// StopBackgroundTask 停止指定后台任务。
func (c *Client) StopBackgroundTask(ctx context.Context, threadID, messageID string) (bool, error) {
	if c.backend == nil {
		return false, errors.New("backend 不可用")
	}
	return c.backend.StopBackgroundTask(ctx, threadID, messageID)
}

// SplitSession 分割会话：在时间线末尾写一条分割线，从下一条消息起发给模型的上下文不再带
// 分割线以上的历史。已处于待分割状态时幂等成功。
func (c *Client) SplitSession(ctx context.Context, threadID string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	err := c.backend.RequestSplit(threadID)
	switch {
	case errors.Is(err, remote.ErrSplitRunning):
		return errors.New("这一轮还在进行，等它结束再分割")
	case errors.Is(err, remote.ErrSplitEmpty):
		return errors.New("会话还没有内容，不需要分割")
	}
	return err
}

// Approvals 调用 ListRequests 查询待处理的交互与审批请求。
func (c *Client) Approvals(ctx context.Context, threadID string) ([]mcodeui.Approval, error) {
	res, err := c.backend.ListRequests(ctx, threadID)
	if err != nil {
		return nil, err
	}
	return RequestsWireToUI(res.Requests), nil
}

// Respond 调用 RespondRequest 答复审批请求，TurnID 为空时传 nil。
func (c *Client) Respond(ctx context.Context, a mcodeui.Approval, optionID string) error {
	var turnID *string
	if a.TurnID != "" {
		t := a.TurnID
		turnID = &t
	}
	var optID *string
	if optionID != "" {
		o := optionID
		optID = &o
	}
	_, err := c.backend.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID:  a.ThreadID,
		TurnID:    turnID,
		RunID:     a.RunID,
		RequestID: a.RequestID,
		OptionID:  optID,
	})
	return err
}

// RespondAnswers 答复提问或计划类请求：answers 键为题目 ID，值为所选或填写的内容；skip 为 true 表示跳过。
func (c *Client) RespondAnswers(ctx context.Context, a mcodeui.Approval, optionID string, answers map[string][]string, skip bool) error {
	var turnID *string
	if a.TurnID != "" {
		t := a.TurnID
		turnID = &t
	}
	var optID *string
	if optionID != "" {
		o := optionID
		optID = &o
	}
	params := wire.RequestsRespondParams{
		ThreadID:  a.ThreadID,
		TurnID:    turnID,
		RunID:     a.RunID,
		RequestID: a.RequestID,
		OptionID:  optID,
	}
	if len(answers) > 0 {
		raw, err := json.Marshal(answers)
		if err != nil {
			return err
		}
		params.Answers = raw
	}
	if skip {
		s := true
		params.Skip = &s
	}
	_, err := c.backend.RespondRequest(ctx, params)
	return err
}

func (c *localConn) Info() remote.ClientInfo {
	return remote.ClientInfo{
		Authenticated: true,
		DeviceID:      "local-ui",
		Label:         "local-ui",
		Entry:         remote.EntryLAN,
	}
}

func (c *localConn) MarkAuthenticated(deviceID, label string) {}

func (c *localConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

func (c *localConn) Notify(method string, params any) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	threadID := c.threadID
	onApprovals := c.onApprovals
	c.mu.Unlock()

	raw, err := json.Marshal(params)
	if err != nil {
		return nil
	}

	switch method {
	case "threads.event":
		var ev wire.ThreadEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return nil
		}
		if ev.ThreadID != threadID {
			return nil
		}
		c.eventMu.Lock()
		defer c.eventMu.Unlock()
		if !c.snapshotApplied {
			c.pendingEvents = append(c.pendingEvents, ev)
		} else {
			c.applyEventLocked(ev)
		}

	case "requests.changed":
		var reqChanged wire.RequestsChangedParams
		if err := json.Unmarshal(raw, &reqChanged); err != nil {
			return nil
		}
		if reqChanged.ThreadID != threadID {
			return nil
		}
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return nil
		}
		if onApprovals != nil {
			onApprovals(RequestsWireToUI(reqChanged.Requests))
		}
	}

	return nil
}

// applyEventLocked 的水位比较与回调共用 eventMu，避免旧帧在新帧之后覆盖界面。
func (c *localConn) applyEventLocked(ev wire.ThreadEvent) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed || (ev.RunID == c.runID && ev.Sequence <= c.lastSeq) {
		return
	}
	c.runID, c.lastSeq = ev.RunID, ev.Sequence
	if c.onUpdate != nil {
		c.onUpdate(ThreadUpdateFromWire(ev))
	}
}

func (c *localConn) applySnapshot(snap *wire.ThreadStreamSnapshot) {
	c.eventMu.Lock()
	defer c.eventMu.Unlock()
	if snap != nil {
		c.runID, c.lastSeq = snap.RunID, snap.Sequence
		if c.onUpdate != nil {
			c.onUpdate(mcodeui.ThreadUpdate{
				ThreadID: c.threadID,
				Thread:   ThreadSummaryToUI(snap.Thread),
				Messages: MessagesWireToUI(snap.History.Messages),
			})
		}
	}
	// 轮次保持首次出现的顺序，同一轮次内按序号回放；快照所属轮次最先处理。
	runOrder := map[string]int{c.runID: 0}
	for _, ev := range c.pendingEvents {
		if _, ok := runOrder[ev.RunID]; !ok {
			runOrder[ev.RunID] = len(runOrder)
		}
	}
	sort.SliceStable(c.pendingEvents, func(i, j int) bool {
		a, b := c.pendingEvents[i], c.pendingEvents[j]
		if a.RunID != b.RunID {
			return runOrder[a.RunID] < runOrder[b.RunID]
		}
		return a.Sequence < b.Sequence
	})
	for _, ev := range c.pendingEvents {
		c.applyEventLocked(ev)
	}
	c.pendingEvents = nil
	c.snapshotApplied = true
}

// Watch 订阅指定会话的事件与审批，返回可重复调用的 stop 函数。
func (c *Client) Watch(threadID string, onUpdate func(mcodeui.ThreadUpdate), onApprovals func([]mcodeui.Approval)) (stop func()) {
	conn := &localConn{
		threadID:    threadID,
		onUpdate:    onUpdate,
		onApprovals: onApprovals,
	}

	ctx := context.Background()
	res, err := c.backend.Subscribe(ctx, conn, threadID)
	if err != nil {
		_ = conn.Close()
		log.Printf("Watch: failed to subscribe thread %s: %v", threadID, err)
		return func() {}
	}

	// 订阅快照是后端对会话状态的权威回答（含 IsRunning）。会话列表里的运行标记只是
	// 某次加载时的快照；若一轮的结束事件在未订阅该会话时发出，界面会一直显示进行中，
	// 打开会话也不会自愈。这里立刻把快照交给界面，使打开会话即以真实状态为准。
	conn.applySnapshot(res.Snapshot)

	var once sync.Once
	stop = func() {
		once.Do(func() {
			_ = conn.Close()
			_ = c.backend.Unsubscribe(context.Background(), conn, threadID)
		})
	}
	return stop
}

// Usage 调用 backend.ThreadUsage 获取指定会话的 token 用量统计，并从已持久化的消息
// 推导请求数与工具调用等活动统计（不改动存储格式，历史数据也能算）。
func (c *Client) Usage(ctx context.Context, threadID string) (mcodeui.ThreadUsage, error) {
	turns, total, err := c.backend.ThreadUsage(ctx, threadID)
	if err != nil {
		return mcodeui.ThreadUsage{}, err
	}
	u := ThreadUsageHarnessToUI(turns, total)
	if msgs, hErr := c.History(ctx, threadID); hErr == nil {
		u.Requests, u.ToolCalls, u.TopTools = ThreadActivityFromMessages(msgs)
	}
	return u, nil
}

// SetThreadRuntimeMode 调用 backend.UpdateThread 设置指定会话的运行模式。
func (c *Client) SetThreadRuntimeMode(threadID, mode string) error {
	ctx := context.Background()
	_, err := c.backend.UpdateThread(ctx, wire.ThreadUpdateParams{
		ThreadID:    threadID,
		RuntimeMode: &mode,
	})
	return err
}

// SetThreadAgent 调用 backend.UpdateThread 设置指定会话的 Agent 与模型。
// provider 为空时表示不修改供应商。
func (c *Client) SetThreadAgent(threadID, provider, model string) error {
	ctx := context.Background()
	params := wire.ThreadUpdateParams{
		ThreadID: threadID,
	}
	if provider != "" {
		params.Provider = &provider
	}
	if model != "" || provider == "" {
		params.Model = &model
	}
	_, err := c.backend.UpdateThread(ctx, params)
	return err
}

// RenameThread 调用 backend.UpdateThread 更新会话标题。
func (c *Client) RenameThread(threadID, title string) error {
	ctx := context.Background()
	_, err := c.backend.UpdateThread(ctx, wire.ThreadUpdateParams{
		ThreadID: threadID,
		Title:    &title,
	})
	return err
}

// SetThreadPinned 调用 backend.UpdateThread 设置会话置顶状态。
func (c *Client) SetThreadPinned(threadID string, pinned bool) error {
	ctx := context.Background()
	_, err := c.backend.UpdateThread(ctx, wire.ThreadUpdateParams{
		ThreadID: threadID,
		Pinned:   &pinned,
	})
	return err
}

// SetThreadArchived 调用 backend.UpdateThread 设置会话归档状态。
func (c *Client) SetThreadArchived(threadID string, archived bool) error {
	ctx := context.Background()
	_, err := c.backend.UpdateThread(ctx, wire.ThreadUpdateParams{
		ThreadID: threadID,
		Archived: &archived,
	})
	return err
}

// SetThreadDormant 调用 backend.UpdateThread 设置会话休眠状态。
func (c *Client) SetThreadDormant(threadID string, dormant bool) error {
	ctx := context.Background()
	_, err := c.backend.UpdateThread(ctx, wire.ThreadUpdateParams{
		ThreadID: threadID,
		Dormant:  &dormant,
	})
	return err
}

// ArchiveThreadWithUndo 归档指定会话，返回 10 秒内可通过 UndoBulkOperation 撤销的令牌。
func (c *Client) ArchiveThreadWithUndo(threadID string) (string, error) {
	if c.backend == nil {
		return "", errors.New("backend 不可用")
	}
	ctx := context.Background()
	archived := true
	_, token, err := c.backend.UpdateThreadWithUndo(ctx, wire.ThreadUpdateParams{
		ThreadID: threadID,
		Archived: &archived,
	})
	return token, err
}

// MarkDormantWithUndo 休眠指定会话，返回 10 秒内可通过 UndoBulkOperation 撤销的令牌。
func (c *Client) MarkDormantWithUndo(threadID string) (string, error) {
	if c.backend == nil {
		return "", errors.New("backend 不可用")
	}
	ctx := context.Background()
	dormant := true
	_, token, err := c.backend.UpdateThreadWithUndo(ctx, wire.ThreadUpdateParams{
		ThreadID: threadID,
		Dormant:  &dormant,
	})
	return token, err
}

// RetainThread 将临时会话转为正式会话。
func (c *Client) RetainThread(threadID string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.RetainThread(context.Background(), threadID)
}

// ReleaseProvisionalThread 释放临时会话，调度延迟自动清理。
func (c *Client) ReleaseProvisionalThread(threadID string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	c.backend.ScheduleProvisionalCleanup(threadID)
	return nil
}

// DeleteThread 调用 backend.DeleteThread 删除指定会话。
// 若会话运行在 mcode 管理的 worktree 中且 AutoCleanupWorktreesOnDeleteEnabled 开启，
// 删除会话后会自动执行 git worktree remove --force 并删除对应的任务分支。
func (c *Client) DeleteThread(threadID string) error {
	ctx := context.Background()

	var (
		wtPath    string
		wtBranch  string
		repoRoot  string
		isMcodeWT bool
	)
	if c.backend != nil {
		if workDir, ok := c.backend.ThreadWorkDir(threadID); ok && workDir != "" {
			if st, err := c.backend.GitState(ctx, threadID); err == nil {
				repoRoot = st.WorkDir
				for _, wt := range st.Worktrees {
					if filepath.Clean(wt.Path) == filepath.Clean(workDir) && !wt.Current {
						wtPath = wt.Path
						wtBranch = wt.Branch
						isMcodeWT = true
						break
					}
				}
			}
			if !isMcodeWT {
				dataDir := c.effectiveDataDir()
				if dataDir != "" {
					managedDir := filepath.Join(dataDir, "worktrees")
					swarmDir := filepath.Join(dataDir, "swarm-worktrees")
					// 旧版本建在 hydra-worktrees 下，过渡期两个目录都认。
					legacySwarmDir := filepath.Join(dataDir, "hydra-worktrees")
					cleanWD := filepath.Clean(workDir)
					if strings.HasPrefix(cleanWD, filepath.Clean(managedDir)) || strings.HasPrefix(cleanWD, filepath.Clean(swarmDir)) || strings.HasPrefix(cleanWD, filepath.Clean(legacySwarmDir)) {
						wtPath = workDir
						isMcodeWT = true
					}
				}
			}
		}
	}

	cleanupEnabled := true
	if cfg, ok := c.loadConfig(); ok {
		cleanupEnabled = cfg.SourceControl.AutoCleanupWorktreesOnDeleteEnabled()
	}

	if err := c.backend.DeleteThread(ctx, threadID); err != nil {
		return err
	}

	if isMcodeWT && cleanupEnabled && wtPath != "" && c.gitRunner() != nil {
		if repoRoot == "" {
			repoRoot = wtPath
		}
		_ = c.gitRunner().WorktreeRemove(ctx, repoRoot, wtPath, true)
		if wtBranch != "" {
			_ = c.gitRunner().BranchDelete(ctx, repoRoot, wtBranch, true)
		}
		_ = os.RemoveAll(wtPath)
	}

	return nil
}

// RemoveProject 调用 backend.RemoveProject 从侧边栏移除指定项目。
func (c *Client) RemoveProject(projectID string) error {
	ctx := context.Background()
	return c.backend.RemoveProject(ctx, projectID)
}

// Diff 调用 backend.GetDiff 获取指定会话或轮次的代码差异。
// turnID 为空表示整个会话。
func (c *Client) Diff(ctx context.Context, threadID, turnID string) (mcodeui.ThreadDiff, error) {
	var tID *string
	if turnID != "" {
		t := turnID
		tID = &t
	}
	res, err := c.backend.GetDiff(ctx, wire.ThreadsGetDiffParams{
		ThreadID: threadID,
		TurnID:   tID,
	})
	if err != nil {
		return mcodeui.ThreadDiff{}, err
	}

	var failureReason string
	if res.FailureReason != nil {
		failureReason = *res.FailureReason
	}
	filesChanged := res.FilesChanged
	if filesChanged == nil {
		filesChanged = []string{}
	}
	turns := make([]mcodeui.DiffTurn, len(res.Turns))
	for i, t := range res.Turns {
		turns[i] = mcodeui.DiffTurn{
			TurnID:     t.TurnID,
			Index:      t.Index,
			Status:     t.Status,
			HasChanges: t.HasChanges,
		}
	}
	return mcodeui.ThreadDiff{
		Diff:          res.Diff,
		Truncated:     res.Truncated,
		FilesChanged:  filesChanged,
		Turns:         turns,
		FailureReason: failureReason,
	}, nil
}

// BranchThread 调用 backend.BranchThread 从指定助手答复分支出新会话。
func (c *Client) BranchThread(ctx context.Context, threadID, messageID string) (string, error) {
	if c.backend == nil {
		return "", errors.New("backend 不可用")
	}
	res, err := c.backend.BranchThread(ctx, wire.ThreadsBranchParams{ThreadID: threadID, MessageID: messageID})
	if err != nil {
		return "", err
	}
	return res.ThreadID, nil
}

// ForwardThread 调用 backend.ForwardThread 将已有会话的历史上下文转发为新会话。
func (c *Client) ForwardThread(ctx context.Context, threadID string) (string, error) {
	if c.backend == nil {
		return "", errors.New("backend 不可用")
	}
	res, err := c.backend.ForwardThread(ctx, wire.ThreadsContinueParams{
		ThreadID: threadID,
		ActionID: newUUID(),
	})
	if err != nil {
		return "", err
	}
	return res.ThreadID, nil
}

// ThreadForwardedContext 调用 backend.ThreadForwardedContext 读取转发会话带入的历史上下文。
func (c *Client) ThreadForwardedContext(ctx context.Context, threadID string) (mcodeui.ForwardedContext, bool, error) {
	if c.backend == nil {
		return mcodeui.ForwardedContext{}, false, errors.New("backend 不可用")
	}
	res, ok := c.backend.ThreadForwardedContext(threadID)
	return mcodeui.ForwardedContext{
		SourceTitle: res.SourceTitle,
		Content:     res.Content,
		Truncated:   res.Truncated,
	}, ok, nil
}

// SetThreadComposerPrefs 调用 backend.SetThreadComposerPrefs 保存会话的 composer 偏好。
func (c *Client) SetThreadComposerPrefs(threadID string, prefs mcodeui.ComposerPrefs) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	_, err := c.backend.SetThreadComposerPrefs(context.Background(), threadID, remote.ThreadComposerPrefs{
		InteractionMode: prefs.InteractionMode,
		Effort:          prefs.Effort,
		BeesProvider:    prefs.BeesProvider,
		BeesModel:       prefs.BeesModel,
		BeesEffort:      prefs.BeesEffort,
		Persona:         prefs.Persona,
	})
	return err
}

// SetThreadFastMode 调用 backend.SetThreadFastMode 保存会话的快速模式与速率档位。
func (c *Client) SetThreadFastMode(threadID string, fastMode bool, speedTier string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	_, err := c.backend.SetThreadFastMode(context.Background(), threadID, fastMode, speedTier)
	return err
}

// SetThreadSwarmMode 调用 backend.SetThreadSwarmMode 保存会话级 Swarm 工作模式。
func (c *Client) SetThreadSwarmMode(threadID, mode string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	_, err := c.backend.SetThreadSwarmMode(context.Background(), threadID, mode)
	return err
}

// SetThreadBeeTiers 调用 backend.SetThreadBeeTiers 保存会话的 Swarm Bee 档位覆盖配置。
func (c *Client) SetThreadBeeTiers(threadID, beeTiers string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.SetThreadBeeTiers(context.Background(), threadID, beeTiers)
}

// SetThreadSwarmPairID 调用 backend.SetThreadSwarmPairID 记录会话选中的 Swarm 配对。
func (c *Client) SetThreadSwarmPairID(threadID, pairID string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.SetThreadSwarmPairID(context.Background(), threadID, pairID)
}

// WatchThreadSummaries 调用 backend.Hub() 注册全局事件观察者，把任一会话的运行态切换转发给 onChange。
// 会话列表加载得到的运行标记只是某次快照：没有订阅 Watch 的会话（切走后结束、在 web 端被停止等）
// 收不到结束事件，侧栏左侧的动态图标会一直转下去，这里补一个全局的运行态来源。
func (c *Client) WatchThreadSummaries(onChange func(update mcodeui.Thread)) (stop func()) {
	if c.backend == nil || onChange == nil {
		return func() {}
	}
	hub := c.backend.Hub()
	if hub == nil {
		return func() {}
	}
	return hub.AddObserver(func(_ string, method string, params any) {
		if method != "thread.summary" {
			return
		}
		sum, ok := params.(wire.ThreadSummary)
		if !ok {
			return
		}
		onChange(ThreadSummaryToUI(sum))
	})
}

// BulkMarkRead 批量将指定会话标记为已读。
func (c *Client) BulkMarkRead(ctx context.Context, projectID string, threadIDs []string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	_, err := c.backend.BulkMarkRead(ctx, projectID, threadIDs)
	return err
}

// BulkArchive 批量归档指定会话。
func (c *Client) BulkArchive(ctx context.Context, projectID string, threadIDs []string) (string, error) {
	if c.backend == nil {
		return "", errors.New("backend 不可用")
	}
	token, _, err := c.backend.BulkArchive(ctx, projectID, threadIDs)
	return token, err
}

// UndoBulkOperation 根据 token 撤销上一次的批量操作。
func (c *Client) UndoBulkOperation(ctx context.Context, projectID, token string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	return c.backend.UndoBulkOperation(ctx, projectID, token)
}

// RecallTurn 收回指定会话最近一轮消息。
func (c *Client) RecallTurn(ctx context.Context, threadID string) (string, error) {
	if c.backend == nil {
		return "", errors.New("backend 不可用")
	}
	return c.backend.RecallDraft(ctx, threadID)
}

// PreviewRevert 预览撤销指定会话中某一轮代码变更的效果。
func (c *Client) PreviewRevert(ctx context.Context, threadID, turnID string) (*mcodeui.RevertPreview, error) {
	if c.backend == nil {
		return nil, errors.New("backend 不可用")
	}
	preview, err := c.backend.PreviewTurnRevert(ctx, threadID, turnID)
	if err != nil {
		return nil, err
	}
	files := make([]mcodeui.RevertFile, 0, len(preview.Files))
	for _, f := range preview.Files {
		files = append(files, mcodeui.RevertFile{
			Path:      f.Path,
			IsKept:    f.IsKept,
			Additions: f.Additions,
			Deletions: f.Deletions,
		})
	}
	return &mcodeui.RevertPreview{
		TurnID:    turnID,
		Files:     files,
		Conflicts: preview.Kept(),
	}, nil
}

// ExecuteRevert 执行撤销指定会话中某一轮代码变更。
func (c *Client) ExecuteRevert(ctx context.Context, threadID, turnID string) error {
	if c.backend == nil {
		return errors.New("backend 不可用")
	}
	_, err := c.backend.ExecuteTurnRevert(ctx, threadID, turnID)
	return err
}
