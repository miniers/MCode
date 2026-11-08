package app

import (
	"sort"
	"time"

	"mcode/internal/automation"
	"mcode/internal/harness"
	"mcode/internal/kanban"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
	"mcode/internal/swarm"
	mcodeui "mcode/internal/ui"
)

// ProjectSummaryToUI 将 wire.ProjectSummary 转换为 mcodeui.Project。
func ProjectSummaryToUI(p wire.ProjectSummary) mcodeui.Project {
	return mcodeui.Project{
		ID:   p.ID,
		Name: p.Name,
		Path: p.Path,
	}
}

// ProjectsSummaryToUI 将切片 []wire.ProjectSummary 转换为 []mcodeui.Project。
func ProjectsSummaryToUI(list []wire.ProjectSummary) []mcodeui.Project {
	res := make([]mcodeui.Project, len(list))
	for i, p := range list {
		res[i] = ProjectSummaryToUI(p)
	}
	return res
}

// parseRFC3339Nano 解析 RFC3339Nano 格式时间，失败时兼容 RFC3339，若仍失败则返回零值。
func parseRFC3339Nano(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339, s)
		if err != nil {
			return time.Time{}
		}
	}
	return t
}

// ThreadSummaryToUI 将 wire.ThreadSummary 转换为 mcodeui.Thread。
// UpdatedAt 按 RFC3339Nano 解析，失败时用零值。
func ThreadSummaryToUI(s wire.ThreadSummary) mcodeui.Thread {
	var modelStr string
	if s.Model != nil {
		modelStr = *s.Model
	}
	interactionMode := s.InteractionMode
	if interactionMode == "" {
		interactionMode = "build"
	}
	return mcodeui.Thread{
		ID:                s.ID,
		ProjectID:         s.ProjectID,
		ParentThreadID:    derefString(s.ParentThreadID),
		IsHelper:          s.IsHelper,
		Title:             s.Title,
		Provider:          s.Provider,
		Model:             modelStr,
		RuntimeMode:       s.RuntimeMode,
		InteractionMode:   interactionMode,
		Effort:            derefString(s.Effort),
		Persona:           s.Persona,
		BeesProvider:      derefString(s.SwarmBeesProvider),
		BeesModel:         derefString(s.SwarmBeesModel),
		BeesEffort:        derefString(s.SwarmBeesEffort),
		BeeTiers:          s.SwarmBeeTiers,
		PairID:            derefString(s.SwarmPairID),
		SwarmMode:         s.SwarmMode,
		FastMode:          s.FastMode,
		SpeedTier:         derefString(s.SpeedTier),
		Pinned:            s.IsPinned,
		Dormant:           s.IsDormant,
		HasUnread:         s.HasUnread,
		IsRunning:         s.IsRunning,
		SwarmBusy:         s.SwarmBusy,
		BackgroundTasks:   s.BackgroundTasks,
		LastStatus:        derefString(s.LastStatus),
		HasPendingRequest: s.HasPendingRequest,
		UpdatedAt:         parseRFC3339Nano(s.UpdatedAt),
		// 侧边栏按用户最后发消息时间排序，缺失时留零值由 UI 侧回退到 UpdatedAt。
		LastUserMessageAt: parseRFC3339Nano(derefString(s.LastUserMessageAt)),

		// 分叉来源（源会话 ID 与分叉点消息 ID），非分叉会话为空。
		ForkedFromThreadID:  derefString(s.ForkedFromThreadID),
		ForkedFromMessageID: derefString(s.ForkedFromMessageID),

		// 会话级外部 MCP 禁用名单，供设置页展示与切换。
		MCPDisabled: append([]string(nil), s.MCPDisabled...),
	}
}

// derefString 解引用可空字符串，nil 返回空串。
func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ProviderWireToUI 将 wire.Provider 转换为 mcodeui.ProviderInfo。
func ProviderWireToUI(p wire.Provider) mcodeui.ProviderInfo {
	models := make([]mcodeui.ModelInfo, len(p.Models))
	for i, m := range p.Models {
		var efforts []string
		if len(m.Efforts) > 0 {
			efforts = make([]string, len(m.Efforts))
			copy(efforts, m.Efforts)
		}
		var cost *mcodeui.ModelCost
		if m.Cost != nil {
			cost = &mcodeui.ModelCost{
				Input:      m.Cost.Input,
				Output:     m.Cost.Output,
				CacheRead:  m.Cost.CacheRead,
				CacheWrite: m.Cost.CacheWrite,
			}
		}
		models[i] = mcodeui.ModelInfo{
			ID:            m.ID,
			Name:          m.Name,
			Detail:        derefString(m.Detail),
			Cost:          cost,
			IsDefault:     m.IsDefault,
			Efforts:       efforts,
			DefaultEffort: derefString(m.DefaultEffort),
			SupportsFast:  m.SupportsFast,
			ContextLimit:  int64(m.ContextLimit),
			Secondary:     m.Secondary,
		}
	}
	return mcodeui.ProviderInfo{
		ID:                p.ID,
		Name:              p.Name,
		Available:         p.Available,
		UnavailableReason: derefString(p.UnavailableReason),
		Models:            models,
	}
}

// ProvidersWireToUI 将切片 []wire.Provider 转换为 []mcodeui.ProviderInfo。
func ProvidersWireToUI(list []wire.Provider) []mcodeui.ProviderInfo {
	res := make([]mcodeui.ProviderInfo, len(list))
	for i, p := range list {
		res[i] = ProviderWireToUI(p)
	}
	return res
}

// ThreadsSummaryToUI 将切片 []wire.ThreadSummary 转换为 []mcodeui.Thread。
func ThreadsSummaryToUI(list []wire.ThreadSummary) []mcodeui.Thread {
	res := make([]mcodeui.Thread, len(list))
	for i, s := range list {
		res[i] = ThreadSummaryToUI(s)
	}
	return res
}

// uiKind 将协议命名映射为 UI 约定的命名（协议思考为 thinking、正文为 assistant，UI 思考为 reasoning、正文为 text）。
func uiKind(role, kind string) string {
	switch kind {
	case "thinking", "reasoning":
		return "reasoning"
	case "assistant", "text":
		return "text"
	case "":
		if role == "assistant" {
			return "text"
		}
		return ""
	case "tool":
		return "tool"
	default:
		return kind
	}
}

// ThreadMessageToUI 将 wire.ThreadMessage 转换为 mcodeui.Message。
// Kind 和 IsFinalResponse 的指针为 nil 时取零值。
// Media 转换自 m.Media，Width/Height 为 nil 时转为 0；无媒体时为 nil。
func ThreadMessageToUI(m wire.ThreadMessage) mcodeui.Message {
	var kind string
	if m.Kind != nil {
		kind = *m.Kind
	}
	var isFinal bool
	if m.IsFinalResponse != nil {
		isFinal = *m.IsFinalResponse
	}
	var turnID string
	if m.TurnID != nil {
		turnID = *m.TurnID
	}
	toolName := derefString(m.ToolName)
	toolInput := derefString(m.ToolInput)
	toolStatus := derefString(m.ToolStatus)
	toolCompletedAt := derefString(m.ToolCompletedAt)
	// 失败类别（仅有 kind 为 error 的消息才有），为空时界面回退到关键词识别。
	errKind := derefString(m.ErrKind)
	var media []mcodeui.MessageMedia
	if len(m.Media) > 0 {
		media = make([]mcodeui.MessageMedia, len(m.Media))
		for i, ref := range m.Media {
			var w, h int
			if ref.Width != nil {
				w = *ref.Width
			}
			if ref.Height != nil {
				h = *ref.Height
			}
			media[i] = mcodeui.MessageMedia{
				ID:       ref.ID,
				Name:     ref.Name,
				MimeType: ref.MimeType,
				Width:    w,
				Height:   h,
			}
		}
	}
	return mcodeui.Message{
		ID:              m.ID,
		Role:            m.Role,
		Kind:            uiKind(m.Role, kind),
		Text:            m.Text,
		IsFinal:         isFinal,
		TurnIndex:       m.TurnIndex,
		TurnID:          turnID,
		TurnStatus:      derefString(m.TurnStatus),
		TurnStartedAt:   derefString(m.TurnStartedAt),
		TurnCompletedAt: derefString(m.TurnCompletedAt),
		Media:           media,
		CreatedAt:       m.CreatedAt,
		ToolName:        toolName,
		ToolInput:       toolInput,
		ToolStatus:      toolStatus,
		ToolCompletedAt: toolCompletedAt,
		ErrKind:         errKind,
	}
}

// MessagesWireToUI 将切片 []wire.ThreadMessage 转换为 []mcodeui.Message。
func MessagesWireToUI(list []wire.ThreadMessage) []mcodeui.Message {
	res := make([]mcodeui.Message, len(list))
	for i, m := range list {
		res[i] = ThreadMessageToUI(m)
	}
	return res
}

// ApprovalOptionWireToUI 将 wire.RemoteRequestOption 转换为 mcodeui.ApprovalOption。
func ApprovalOptionWireToUI(o wire.RemoteRequestOption) mcodeui.ApprovalOption {
	return mcodeui.ApprovalOption{
		ID:    o.ID,
		Title: o.Title,
		Role:  o.Role,
	}
}

// ApprovalOptionsWireToUI 将切片 []wire.RemoteRequestOption 转换为 []mcodeui.ApprovalOption。
func ApprovalOptionsWireToUI(list []wire.RemoteRequestOption) []mcodeui.ApprovalOption {
	res := make([]mcodeui.ApprovalOption, len(list))
	for i, o := range list {
		res[i] = ApprovalOptionWireToUI(o)
	}
	return res
}

// RemoteRequestToUI 将 wire.RemoteRequest 转换为 mcodeui.Approval。
// 指针字段为 nil 时取空串。
func RemoteRequestToUI(r wire.RemoteRequest) mcodeui.Approval {
	var turnID string
	if r.TurnID != nil {
		turnID = *r.TurnID
	}
	var title string
	if r.Title != nil {
		title = *r.Title
	}
	var detail string
	if r.Detail != nil {
		detail = *r.Detail
	}
	var reason string
	if r.Reason != nil {
		reason = *r.Reason
	}
	var diff string
	if r.Diff != nil {
		diff = *r.Diff
	}
	return mcodeui.Approval{
		ThreadID:      r.ThreadID,
		RunID:         r.RunID,
		TurnID:        turnID,
		RequestID:     r.RequestID,
		Kind:          r.Kind,
		Title:         title,
		Detail:        detail,
		Reason:        reason,
		Options:       ApprovalOptionsWireToUI(r.Options),
		Questions:     QuestionsWireToUI(r.Questions),
		Diff:          diff,
		DeliveryError: derefString(r.DeliveryError),
	}
}

// QuestionsWireToUI 将 []wire.RemoteQuestion 转换为 []mcodeui.ApprovalQuestion，空切片返回 nil。
func QuestionsWireToUI(list []wire.RemoteQuestion) []mcodeui.ApprovalQuestion {
	if len(list) == 0 {
		return nil
	}
	res := make([]mcodeui.ApprovalQuestion, len(list))
	for i, q := range list {
		var choices []mcodeui.ApprovalQuestionChoice
		if len(q.Choices) > 0 {
			choices = make([]mcodeui.ApprovalQuestionChoice, len(q.Choices))
			for j, c := range q.Choices {
				var d string
				if c.Detail != nil {
					d = *c.Detail
				}
				choices[j] = mcodeui.ApprovalQuestionChoice{Label: c.Label, Detail: d}
			}
		}
		res[i] = mcodeui.ApprovalQuestion{
			ID:             q.ID,
			Header:         q.Header,
			Prompt:         q.Prompt,
			Choices:        choices,
			AllowsMultiple: q.AllowsMultiple,
			AllowsOther:    q.AllowsOther,
			IsSecret:       q.IsSecret,
		}
	}
	return res
}

// RequestsWireToUI 将切片 []wire.RemoteRequest 转换为 []mcodeui.Approval。
func RequestsWireToUI(list []wire.RemoteRequest) []mcodeui.Approval {
	res := make([]mcodeui.Approval, len(list))
	for i, r := range list {
		res[i] = RemoteRequestToUI(r)
	}
	return res
}

// ThreadUpdateFromWire 将 wire.ThreadEvent 转换为 mcodeui.ThreadUpdate。
func ThreadUpdateFromWire(ev wire.ThreadEvent) mcodeui.ThreadUpdate {
	var removed []string
	if ev.Payload.RemovedMessageIDs != nil {
		removed = make([]string, len(ev.Payload.RemovedMessageIDs))
		copy(removed, ev.Payload.RemovedMessageIDs)
	}
	return mcodeui.ThreadUpdate{
		ThreadID:   ev.ThreadID,
		Thread:     ThreadSummaryToUI(ev.Payload.Thread),
		Messages:   MessagesWireToUI(ev.Payload.Messages),
		RemovedIDs: removed,
		// 帧体超过上限时后端改发 resync 帧，帧里不带消息，只能靠重拉补齐。
		Resync: ev.Kind == "resync",
	}
}

// UsageHarnessToUI 将 harness.Usage 转换为 mcodeui.TokenUsage。
func UsageHarnessToUI(u harness.Usage) mcodeui.TokenUsage {
	var requests []mcodeui.RequestSpeed
	if len(u.Requests) > 0 {
		requests = make([]mcodeui.RequestSpeed, len(u.Requests))
		for i, r := range u.Requests {
			requests[i] = mcodeui.RequestSpeed{OutputTokens: r.OutputTokens, DurationMs: r.DurationMs}
		}
	}
	return mcodeui.TokenUsage{
		Input:         u.InputTokens,
		Output:        u.OutputTokens,
		Cached:        u.CachedTokens,
		DurationMs:    u.DurationMs,
		Requests:      requests,
		ContextTokens: u.ContextTokens,
		ContextWindow: u.ContextWindow,
		CostUSD:       u.CostUSD,
	}
}

// ThreadUsageHarnessToUI 将 harness 轮次与总用量转换为 mcodeui.ThreadUsage。
func ThreadUsageHarnessToUI(turns map[int]harness.Usage, total harness.Usage) mcodeui.ThreadUsage {
	uiTurns := make(map[int]mcodeui.TokenUsage, len(turns))
	for k, v := range turns {
		uiTurns[k] = UsageHarnessToUI(v)
	}
	return mcodeui.ThreadUsage{
		Turns: uiTurns,
		Total: UsageHarnessToUI(total),
	}
}

// ThreadActivityTopTools 是常用工具排行的上限。
const ThreadActivityTopTools = 5

// ThreadActivityFromMessages 从已持久化的消息推导会话活动统计：请求数按助手消息段估算
// （排除思考行，一次模型请求产出一段回复），工具调用按 tool 消息统计，并给出按次数降序的
// 常用工具（最多 ThreadActivityTopTools 项，次数相同按名称排序，保证顺序稳定）。
func ThreadActivityFromMessages(msgs []mcodeui.Message) (requests, toolCalls int, top []mcodeui.ToolCallCount) {
	counts := make(map[string]int)
	for _, m := range msgs {
		switch {
		case m.Role == "assistant" && m.Kind != "reasoning" && m.Kind != "thinking":
			requests++
		case m.Role == "tool":
			toolCalls++
			name := m.ToolName
			if name == "" {
				name = "未知工具"
			}
			counts[name]++
		}
	}
	top = make([]mcodeui.ToolCallCount, 0, len(counts))
	for name, count := range counts {
		top = append(top, mcodeui.ToolCallCount{Name: name, Count: count})
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].Count != top[j].Count {
			return top[i].Count > top[j].Count
		}
		return top[i].Name < top[j].Name
	})
	if len(top) > ThreadActivityTopTools {
		top = top[:ThreadActivityTopTools]
	}
	return requests, toolCalls, top
}

// KanbanTaskToUI 将 store.Task 与 kanban.Derived 转换为 mcodeui.KanbanTask。
// 字符串型可空字段为 nil 时转成空串。
func KanbanTaskToUI(t store.Task, d kanban.Derived) mcodeui.KanbanTask {
	var projectID string
	if t.ProjectID != nil {
		projectID = *t.ProjectID
	}
	var threadID string
	if t.ThreadID != nil {
		threadID = *t.ThreadID
	}
	var goalID string
	if t.GoalID != nil {
		goalID = *t.GoalID
	}
	var lastFailureReason string
	if t.LastFailureReason != nil {
		lastFailureReason = *t.LastFailureReason
	}
	var lastActivity string
	if t.LastActivityText != nil {
		lastActivity = *t.LastActivityText
	}
	var reviewSummary string
	if t.ReviewSummary != nil {
		reviewSummary = *t.ReviewSummary
	}
	deps := make([]string, len(t.DependsOn))
	copy(deps, t.DependsOn)

	var resultSummary, waitKind, wakeAt string
	if meta, err := kanban.ParseTaskMeta(t.Meta); err == nil {
		resultSummary = meta.Summary
		waitKind = meta.WaitKind
		wakeAt = meta.WakeAt
	}

	flags := make([]string, len(d.AttentionFlags))
	copy(flags, d.AttentionFlags)

	return mcodeui.KanbanTask{
		ID:                t.ID,
		ProjectID:         projectID,
		ThreadID:          threadID,
		GoalID:            goalID,
		Title:             t.Title,
		Notes:             t.Notes,
		Status:            t.Status,
		Priority:          t.Priority,
		Column:            string(d.DisplayColumn),
		DependsOn:         deps,
		Blocked:           d.Blocked,
		CanStart:          d.CanStart,
		CanMove:           d.CanMove,
		CanReview:         d.CanReview,
		AttentionFlags:    flags,
		LastFailureReason: lastFailureReason,
		LastActivity:      lastActivity,
		ReviewSummary:     reviewSummary,
		ResultSummary:     resultSummary,
		WaitKind:          waitKind,
		WakeAt:            wakeAt,
		AttemptCount:      t.AttemptCount,
		Revision:          t.Revision,
		UpdatedAt:         t.UpdatedAt,
	}
}

// KanbanGoalToUI 将 store.Goal 转换为 mcodeui.KanbanGoal。
// 字符串型可空字段为 nil 时转成空串。
func KanbanGoalToUI(g store.Goal, done, total int) mcodeui.KanbanGoal {
	var statusReason string
	if g.StatusReason != nil {
		statusReason = *g.StatusReason
	}
	var planSummary string
	if g.PlanSummary != nil {
		planSummary = *g.PlanSummary
	}
	var resultSummary string
	if g.ResultSummary != nil {
		resultSummary = *g.ResultSummary
	}

	return mcodeui.KanbanGoal{
		ID:            g.ID,
		ProjectID:     g.ProjectID,
		Title:         g.Title,
		Prompt:        g.Prompt,
		Status:        g.Status,
		StatusReason:  statusReason,
		PlanSummary:   planSummary,
		ResultSummary: resultSummary,
		Done:          done,
		Total:         total,
		Revision:      g.Revision,
		UpdatedAt:     g.UpdatedAt,
	}
}

func readProgressCount(m map[string]any, key string) (int, bool) {
	if m == nil {
		return 0, false
	}
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case float32:
		return int(n), true
	default:
		return 0, false
	}
}

// KanbanBoardToUI 将 kanban.Board 转换为 mcodeui.KanbanBoard，并按 projectID 过滤。
// projectID 为空时返回全部。
func KanbanBoardToUI(b kanban.Board, projectID string) mcodeui.KanbanBoard {
	taskGoalStats := make(map[string]struct{ total, done int })
	for _, tv := range b.Tasks {
		if tv.Task.GoalID != nil && *tv.Task.GoalID != "" {
			gid := *tv.Task.GoalID
			stat := taskGoalStats[gid]
			stat.total++
			if tv.Task.Status == "done" || tv.Task.Status == string(kanban.TaskStatusDone) {
				stat.done++
			}
			taskGoalStats[gid] = stat
		}
	}

	uiTasks := make([]mcodeui.KanbanTask, 0, len(b.Tasks))
	for _, tv := range b.Tasks {
		if projectID != "" {
			if tv.Task.ProjectID == nil || *tv.Task.ProjectID != projectID {
				continue
			}
		}
		uiTasks = append(uiTasks, KanbanTaskToUI(tv.Task, tv.Derived))
	}

	uiGoals := make([]mcodeui.KanbanGoal, 0, len(b.Goals))
	for _, g := range b.Goals {
		if projectID != "" && g.ProjectID != projectID {
			continue
		}
		total, okTotal := readProgressCount(g.Progress, "total")
		done, okDone := readProgressCount(g.Progress, "done")
		if !okTotal || !okDone {
			stat := taskGoalStats[g.ID]
			total = stat.total
			done = stat.done
		}
		uiGoals = append(uiGoals, KanbanGoalToUI(g, done, total))
	}

	return mcodeui.KanbanBoard{
		Revision: b.Revision,
		Tasks:    uiTasks,
		Goals:    uiGoals,
	}
}

// AutomationScheduleToUI 将 automation.Schedule 转换为 mcodeui.AutomationSchedule。
func AutomationScheduleToUI(s automation.Schedule) mcodeui.AutomationSchedule {
	var date time.Time
	if s.Date != nil {
		date = *s.Date
	}
	return mcodeui.AutomationSchedule{
		Type:       s.Type,
		Seconds:    s.Seconds,
		Date:       date,
		TimeOfDay:  s.TimeOfDay,
		Timezone:   s.Timezone,
		DayOfWeek:  s.DayOfWeek,
		Expression: s.Expression,
	}
}

// AutomationScheduleFromUI 将 mcodeui.AutomationSchedule 转换为 automation.Schedule。
func AutomationScheduleFromUI(s mcodeui.AutomationSchedule) automation.Schedule {
	var date *time.Time
	if !s.Date.IsZero() {
		date = &s.Date
	}
	return automation.Schedule{
		Type:       s.Type,
		Seconds:    s.Seconds,
		Date:       date,
		TimeOfDay:  s.TimeOfDay,
		Timezone:   s.Timezone,
		DayOfWeek:  s.DayOfWeek,
		Expression: s.Expression,
	}
}

// AutomationToUI 将 automation.Definition 转换为 mcodeui.Automation。
func AutomationToUI(d automation.Definition) mcodeui.Automation {
	var projectID string
	if d.ProjectID != nil {
		projectID = *d.ProjectID
	}
	var nextRunAt time.Time
	if d.NextRunAt != nil {
		nextRunAt = *d.NextRunAt
	}
	var lastRunAt time.Time
	if d.LastRunAt != nil {
		lastRunAt = *d.LastRunAt
	}

	return mcodeui.Automation{
		ID:                     d.ID,
		ProjectID:              projectID,
		Name:                   d.Name,
		Prompt:                 d.Prompt,
		Status:                 d.Status,
		StopWhen:               d.Spec.StopWhen,
		LastRunStatus:          d.Spec.State.LastRunStatus,
		StopReason:             d.Spec.State.StopReason,
		Schedule:               AutomationScheduleToUI(d.Spec.Schedule),
		Enabled:                d.Enabled,
		NextRunAt:              nextRunAt,
		LastRunAt:              lastRunAt,
		TotalRunCount:          d.Spec.State.TotalRunCount,
		ConsecutiveFailures:    d.Spec.State.ConsecutiveFailures,
		MaxConsecutiveFailures: d.Spec.MaxConsecutiveFailures,
		MaxIterations:          d.Spec.MaxIterations,
		MaxRuns:                d.Spec.EffectiveMaxRuns(),
		CatchUp:                d.Spec.CatchUpEnabled(),
		Revision:               d.Revision,
		Provider:               d.Spec.Provider,
		Model:                  d.Spec.Model,
		Effort:                 d.Spec.Effort,
		RuntimeMode:            d.Spec.RuntimeMode,
		RunMode:                d.Spec.NormalizedRunMode(),
	}
}

// AutomationRunToUI 将 store.AutomationRun 转换为 mcodeui.AutomationRun。
func AutomationRunToUI(r store.AutomationRun) mcodeui.AutomationRun {
	var threadID string
	if r.ThreadID != nil {
		threadID = *r.ThreadID
	}
	var summary string
	if r.ResultSummary != nil {
		summary = *r.ResultSummary
	}
	var errStr string
	if r.Error != nil {
		errStr = *r.Error
	}
	var finishedAt time.Time
	if r.FinishedAt != nil {
		finishedAt = *r.FinishedAt
	}

	return mcodeui.AutomationRun{
		ID:           r.ID,
		AutomationID: r.AutomationID,
		ThreadID:     threadID,
		Status:       r.Status,
		Summary:      summary,
		Error:        errStr,
		Trigger:      r.Trigger,
		MissedCount:  r.MissedCount,
		StartedAt:    r.StartedAt,
		FinishedAt:   finishedAt,
	}
}

// AutomationInputToSpec 在 base 基础上合并用户可编辑字段，并保留 State 与 ConfidenceThreshold。
// base 为零值时 ConfidenceThreshold 取 0.8，MaxConsecutiveFailures 为 0 时取 3。
func AutomationInputToSpec(in mcodeui.AutomationInput, base automation.Spec) automation.Spec {
	res := base
	res.Schedule = AutomationScheduleFromUI(in.Schedule)
	res.StopWhen = in.StopWhen
	maxRuns := in.MaxRuns
	// 兼容只设置旧字段 MaxIterations 的调用方。
	if maxRuns == 0 && in.MaxIterations > 0 {
		maxRuns = in.MaxIterations
	}
	res.MaxRuns = maxRuns
	// MaxIterations 是 MaxRuns 的旧镜像，写入时保持一致。
	res.MaxIterations = maxRuns
	if in.CatchUp != nil {
		res.CatchUp = in.CatchUp
	}
	res.MaxConsecutiveFailures = in.MaxConsecutiveFailures
	// 模型与权限模式同样以入参为准：空串表示恢复默认（跟随后端默认行为）。
	res.Provider = in.Provider
	res.Model = in.Model
	res.Effort = in.Effort
	res.RuntimeMode = in.RuntimeMode
	if res.ConfidenceThreshold == 0 {
		res.ConfidenceThreshold = 0.8
	}
	if res.MaxConsecutiveFailures == 0 {
		res.MaxConsecutiveFailures = 3
	}
	return res
}

// swarmBeeToUI 将 swarm.Bee 转换为 mcodeui.SwarmBee。
func swarmBeeToUI(h swarm.Bee) mcodeui.SwarmBee {
	var finishedAt *time.Time
	if h.FinishedAt != nil {
		t := *h.FinishedAt
		finishedAt = &t
	}
	conflictFiles := make([]string, len(h.ConflictFiles))
	copy(conflictFiles, h.ConflictFiles)
	changedFiles := make([]string, len(h.ChangedFiles))
	copy(changedFiles, h.ChangedFiles)

	var cred *mcodeui.SwarmCredibility
	if h.Credibility != nil {
		unverified := make([]string, len(h.Credibility.Unverified))
		copy(unverified, h.Credibility.Unverified)
		cred = &mcodeui.SwarmCredibility{
			Cited:      h.Credibility.Cited,
			Verified:   h.Credibility.Verified,
			Unverified: unverified,
			Summary:    h.Credibility.Summary(),
		}
	}

	return mcodeui.SwarmBee{
		ThreadID:              h.ThreadID,
		Name:                  h.Name,
		Task:                  h.Task,
		Status:                string(h.Status),
		Summary:               h.Summary,
		Activity:              h.Activity,
		Provider:              h.Provider,
		Model:                 h.Model,
		MergeError:            h.MergeError,
		Index:                 h.Index,
		ToolCalls:             h.ToolCalls,
		Tokens:                h.Tokens,
		StartedAt:             h.StartedAt,
		FinishedAt:            finishedAt,
		Merged:                h.Merged,
		PendingMerge:          h.PendingMerge,
		ReviewState:           string(h.ReviewState),
		ReviewReason:          h.ReviewReason,
		ReviewThreadID:        h.ReviewThreadID,
		ReviewLayer:           h.ReviewLayer,
		ReviewConcerns:        append([]string(nil), h.ReviewConcerns...),
		ReviewTokens:          h.ReviewTokens,
		ReviewTokensEstimated: h.ReviewTokensEstimated,
		MergeDiscarded:        h.MergeDiscarded,
		ConflictFiles:         conflictFiles,
		ChangedFiles:          changedFiles,
		Tier:                  h.Tier,
		ReadOnly:              h.ReadOnly,
		DirectModel:           h.DirectModel,
		ReadOnlyViolation:     h.ReadOnlyViolation,
		ShadowRepo:            h.ShadowRepo,
		SharedNoGit:           h.SharedNoGit,
		SharedNoGitReason:     h.SharedNoGitReason,
		Credibility:           cred,
	}
}

// swarmStateToUI 将 swarm.State 转换为 mcodeui.SwarmState。
func swarmStateToUI(st swarm.State, leadThreadID string, isBee, available bool) mcodeui.SwarmState {
	bees := make([]mcodeui.SwarmBee, len(st.Bees))
	for i, h := range st.Bees {
		bees[i] = swarmBeeToUI(h)
	}
	return mcodeui.SwarmState{
		LeadThreadID: leadThreadID,
		Available:    available,
		Enabled:      st.Enabled,
		IsBee:        isBee,
		Bees:         bees,
	}
}
