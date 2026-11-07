package remote

import (
	"context"
	"fmt"
	"strings"

	"mcode/internal/harness"
	"mcode/internal/kanban"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

var (
	_ kanban.SessionDriver      = (*MemoryBackend)(nil)
	_ kanban.ProjectResolver    = (*MemoryBackend)(nil)
	_ kanban.SessionMessenger   = (*MemoryBackend)(nil)
	_ kanban.SessionInterrupter = (*MemoryBackend)(nil)
)

// ProjectRoot 实现 kanban.ProjectResolver 接口，获取项目的根目录绝对路径。
func (b *MemoryBackend) ProjectRoot(ctx context.Context, projectID string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	proj, ok := b.projects[projectID]
	if !ok {
		return "", store.ErrNotFound
	}
	return proj.Path, nil
}

// StartSession 实现 kanban.SessionDriver 接口，为指定项目新建会话、绑定独立工作目录、更新标题，
// 并在 Prompt 非空时发送首条指令；Prompt 为空（如总管会话）时只建会话不发消息，由调用方另行写开场白。
func (b *MemoryBackend) StartSession(ctx context.Context, req kanban.SessionRequest) (string, error) {
	var provider, model, runtimeMode string
	var prefs ThreadComposerPrefs
	if req.LeadThreadID != "" {
		lead, err := b.GetThread(ctx, req.LeadThreadID)
		if err != nil {
			return "", err
		}
		provider = derefString(lead.SwarmBeesProvider)
		model = derefString(lead.SwarmBeesModel)
		if provider == "" {
			provider = lead.Provider
			if model == "" {
				model = derefString(lead.Model)
			}
		}
		runtimeMode = lead.RuntimeMode
		prefs.InteractionMode = normalizeInteractionMode(lead.InteractionMode)
		prefs.Effort = derefString(lead.SwarmBeesEffort)
	}
	if req.InteractionMode != "" {
		prefs.InteractionMode = req.InteractionMode
	}
	// Bee 偏好覆盖：Provider/Model/Effort 非空时覆盖上面从 Lead 推导出的偏好。
	// Provider 非空时以本请求为准（Model 为空表示让该 provider 使用默认模型）。
	if req.Provider != "" {
		provider = req.Provider
		model = req.Model
	} else if req.Model != "" {
		model = req.Model
	}
	if req.Effort != "" {
		prefs.Effort = req.Effort
	}
	// 权限模式同理：非空时覆盖上面从 Lead 继承的值，为空则保持原行为（沿用 Lead 或后端默认）。
	// 合法性由 createThread 统一校验，这里不重复判断。
	if req.RuntimeMode != "" {
		runtimeMode = req.RuntimeMode
	}
	createActionID := newUUID()
	// 系统发起的会话（编排子代理、看板任务、自动化）不是用户主动新建的：
	// 不读也不写模型记忆，它们带的子代理/档位模型不能顶替用户的新会话选择。
	res, err := b.createThread(ctx, wire.ThreadCreateParams{
		ProjectID:   req.ProjectID,
		ActionID:    createActionID,
		Options:     &wire.ComposerOptions{Provider: optionalString(provider)},
		Model:       optionalString(model),
		RuntimeMode: optionalString(runtimeMode),
	}, false)
	if err != nil {
		return "", err
	}
	threadID := res.ThreadID
	if req.LeadThreadID != "" || req.InteractionMode != "" || req.Effort != "" {
		if _, err := b.SetThreadComposerPrefs(ctx, threadID, prefs); err != nil {
			return "", err
		}
	}
	// ThreadComposerPrefs 没有 FastMode 字段，快速模式改用既有的 SetThreadFastMode 应用。
	// 新建会话尚无速率档位，speedTier 传空。
	if req.FastMode {
		if _, err := b.SetThreadFastMode(ctx, threadID, true, ""); err != nil {
			return "", err
		}
	}

	b.mu.Lock()
	td, ok := b.threads[threadID]
	if ok {
		td.workDir = req.WorkDir
		if req.Title != "" {
			td.summary.Title = req.Title
		}
	}
	s := b.store
	b.mu.Unlock()

	if s != nil && req.Title != "" {
		_ = s.UpdateThreadTitle(ctx, threadID, req.Title)
	}

	// Prompt 去空白后为空：只要会话（含标题与偏好），不发起第一轮。
	if strings.TrimSpace(req.Prompt) == "" {
		return threadID, nil
	}

	sendActionID := newUUID()
	_, err = b.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     req.Prompt,
		ActionID: sendActionID,
	})
	if err != nil {
		return "", err
	}

	return threadID, nil
}

// SessionState 实现 kanban.SessionDriver 接口，查询指定会话的运行中、审批中、失败状态以及最后一条 assistant 回复。
func (b *MemoryBackend) SessionState(ctx context.Context, threadID string) (kanban.SessionState, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	td, ok := b.threads[threadID]
	if !ok {
		return kanban.SessionState{}, store.ErrNotFound
	}
	// C-ST-07：最后一条 assistant 回复取自内存消息，先确保历史已回载。
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		return kanban.SessionState{}, err
	}
	b.touchThreadLocked(td)

	// 没有真实运行实例时（runTurnID 为空且未在取消收尾）不能判定为运行中：
	// 异常收尾遗留的 summary.IsRunning 粘滞标记以实际运行实例为准纠正，
	// 避免 Swarm 调度看门狗将已结束的子代理永久视作 running。
	running := td.runTurnID != "" || td.runCancel != nil
	if !running && td.summary.IsRunning {
		td.summary.IsRunning = false
	}
	awaitingApproval := len(td.pending) > 0
	// 待答复里有提问类请求时单独标出等待输入：Hyrda 看门狗对这两种等待都要豁免，
	// 但看板关注标志要把「等用户回答问题」和「等审批」区分开。
	awaitingInput := false
	for _, pr := range td.pending {
		if pr.req.Kind == string(harness.RequestKindQuestion) {
			awaitingInput = true
			break
		}
	}

	failed := false
	failureReason := ""
	if td.summary.LastStatus != nil {
		st := strings.ToLower(*td.summary.LastStatus)
		if st == "failed" || st == "error" {
			failed = true
			failureReason = *td.summary.LastStatus
		}
	}
	if td.summary.Attention == "failed" {
		failed = true
		if failureReason == "" {
			failureReason = "attention: failed"
		}
	}

	// 查询最近一轮轮次记录：
	// 1. 判定 failed 时，优先从最近一轮提取真实错误信息（去掉首尾空白）；
	// 2. LastStatus 为空且不在运行时，回退读取最近一轮的状态以判断是否被用户中断。
	var recentTurn *store.Turn
	if b.store != nil && (failed || (!running && derefString(td.summary.LastStatus) == "")) {
		if turns, err := b.store.ListRecentTurns(ctx, threadID, 1); err == nil && len(turns) > 0 {
			recentTurn = &turns[len(turns)-1]
		}
	}

	if failed && recentTurn != nil {
		if errMsg := strings.TrimSpace(recentTurn.ErrorMessage); errMsg != "" {
			failureReason = errMsg
		}
	}

	// 最近一轮是否被用户中断结束：只看执行轮次状态是否表示被中断。
	// mcode 后端不给 summary.LastStatus 赋值，实际生效的是 updateTurnFinish 写入 turn 表的状态
	// （见 memory_backend.go 的 "interrupted"），因此 LastStatus 为空时回退到最近一轮的状态；
	// 仅在不在运行时为真，否则会把正在跑的用户请求当成被中断。
	interrupted := interruptedTurnStatus(derefString(td.summary.LastStatus))
	if !running && derefString(td.summary.LastStatus) == "" && recentTurn != nil {
		interrupted = interruptedTurnStatus(recentTurn.Status)
	}

	lastAssistantText := lastAssistantReplyText(td.messages)

	// 进展指纹：拼入消息条数、最后一条消息的标识与文本长度（流式增量会持续变长）、
	// 工具触碰过的文件数、待审批请求数，以及会话更新时间与活动文案。
	// 任一项变化都说明会话有新输出，因此 bee 在流式写文本或调用工具时指纹每隔几秒就会变。
	lastMsgID := ""
	lastMsgLen := 0
	if n := len(td.messages); n > 0 {
		lastMsgID = td.messages[n-1].ID
		lastMsgLen = len(td.messages[n-1].Text)
	}
	progress := fmt.Sprintf("%d|%s|%d|%d|%d|%s|%s",
		len(td.messages), lastMsgID, lastMsgLen, len(td.touchedPaths), len(td.pending),
		td.summary.UpdatedAt, derefString(td.summary.LastActivityText))

	return kanban.SessionState{
		Running:              running,
		Failed:               failed,
		AwaitingApproval:     awaitingApproval,
		AwaitingInput:        awaitingInput,
		FailureReason:        failureReason,
		LastAssistantText:    lastAssistantText,
		Interrupted:          interrupted,
		Progress:             progress,
		LastFileChangeAt:     td.lastFileChangeAt,
		HostToolsUnavailable: b.hostToolsUnavailableLocked(td.summary.Provider),
	}, nil
}

// hostToolsUnavailableLocked 按线程实际使用的 provider 查 harness，报告它的会话是否能调到
// mcode 的 MCP 回执工具。查找方式与 runHarness 一致（threadHarnessLocked）：
// 找不到 harness 或 harness 不声明该能力时返回 false，保持旧的追问行为。
func (b *MemoryBackend) hostToolsUnavailableLocked(provider string) bool {
	h := b.threadHarnessLocked(provider)
	if h == nil {
		return false
	}
	reporter, ok := h.(harness.HostToolsReporter)
	if !ok {
		return false
	}
	return !reporter.HostToolsAvailable()
}

// isReasoningMessageKind 判断消息 Kind 是否属于推理/思考类，这类消息不是给用户的最终汇报。
// Kind 取值来源：internal/store/thread_archive.go:500 与 internal/remote/memory_backend.go:6699。
func isReasoningMessageKind(kind *string) bool {
	if kind == nil {
		return false
	}
	switch *kind {
	case "reasoning", "thinking":
		return true
	}
	return false
}

// lastAssistantReplyText 从后往前挑选子代理的最终回复：
// 优先取 Role 为 assistant、IsFinalResponse 为真且非推理类的消息；
// 找不到时退回最后一条非推理类、文本非空的 assistant 消息；都没有则返回空字符串。
func lastAssistantReplyText(messages []wire.ThreadMessage) string {
	fallback := ""
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role != "assistant" || isReasoningMessageKind(m.Kind) || strings.TrimSpace(m.Text) == "" {
			continue
		}
		if m.IsFinalResponse != nil && *m.IsFinalResponse {
			return m.Text
		}
		if fallback == "" {
			fallback = m.Text
		}
	}
	return fallback
}

// interruptedTurnStatus 判断轮次状态是否表示「被用户中断或取消结束」，
// 与中断或取消结束的轮次状态对应；中断、取消与手动停止都算这种结束。
func interruptedTurnStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "interrupted", "cancelled", "canceled", "stopped":
		return true
	}
	return false
}

// SendFollowUp 实现 kanban.SessionMessenger 接口，向会话投递规划与催促使消息。
// 这条消息是宿主发的指令（“请规划”“请接着做”），不是用户说的话，因此走 SendHostEvent：
// 它按 host_event 落库，界面上显示为事件卡而不是用户气泡，且运行中排队、空闲时直接发送。
// 早先改用 SendSwarmReport 只是为了绕过运行中的拒绝，代价是消息冒充了用户发言。
func (b *MemoryBackend) SendFollowUp(ctx context.Context, threadID, text string) error {
	return b.SendHostEvent(ctx, threadID, text)
}

// InterruptSession 实现 kanban.SessionInterrupter 接口，中断指定会话正在运行的一轮；结果与失败原因一律丢弃。
func (b *MemoryBackend) InterruptSession(ctx context.Context, threadID string) error {
	_, err := b.InterruptCurrent(ctx, threadID)
	return err
}
