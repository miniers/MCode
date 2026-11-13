// 本文件包含 MemoryBackend 的会话状态与历史方法。
package remote

import (
	"context"
	"fmt"
	"log"
	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AddProject 预置或更新内存中的项目数据。
func (b *MemoryBackend) AddProject(p wire.ProjectSummary) {
	b.mu.Lock()
	b.projects[p.ID] = p
	s := b.store
	b.mu.Unlock()

	if s != nil {
		pCtx, pCancel := persistCtx()
		defer pCancel()
		if err := s.UpsertProject(pCtx, projectToStore(p)); err != nil {
			log.Printf("store upsert project %s error: %v", p.ID, err)
		}
	}
}

// AddProjectPath 把路径转换为绝对路径并 Clean，路径必须是已存在的目录，否则返回错误；
// 如果已有项目的 Path 相同，直接返回该项目；否则新建项目，ID 用 newUUID()，Name 用目录的 base 名，调用 AddProject 并返回。
func (b *MemoryBackend) AddProjectPath(ctx context.Context, path string) (wire.ProjectSummary, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return wire.ProjectSummary{}, fmt.Errorf("resolve absolute path: %w", err)
	}
	cleanPath := filepath.Clean(absPath)

	fi, err := os.Stat(cleanPath)
	if err != nil {
		return wire.ProjectSummary{}, fmt.Errorf("project path does not exist: %w", err)
	}
	if !fi.IsDir() {
		return wire.ProjectSummary{}, fmt.Errorf("project path is not a directory: %s", cleanPath)
	}

	b.mu.Lock()
	for _, p := range b.projects {
		if filepath.Clean(p.Path) == cleanPath {
			b.mu.Unlock()
			return p, nil
		}
	}
	b.mu.Unlock()

	name := filepath.Base(cleanPath)
	proj := wire.ProjectSummary{
		ID:          newUUID(),
		Name:        name,
		Path:        cleanPath,
		IsExpanded:  true,
		ThreadCount: 0,
	}
	b.AddProject(proj)
	return proj, nil
}

// AddThread 预置或更新内存中的会话及其消息历史。
func (b *MemoryBackend) AddThread(t wire.ThreadSummary, messages []wire.ThreadMessage) {
	b.mu.Lock()
	if t.RuntimeMode == "" {
		t.RuntimeMode = b.defaultRuntimeModeLocked()
	}
	if t.OrderKey == "" || t.OrderKey == "0" {
		t.OrderKey = formatOrderKey(0)
	}
	msgs := make([]wire.ThreadMessage, len(messages))
	copy(msgs, messages)
	persisted := make(map[string]bool, len(messages))
	var dormantAt *time.Time
	if t.IsDormant {
		nowTime := time.Now().UTC()
		dormantAt = &nowTime
	}
	var parentID string
	if t.ParentThreadID != nil {
		parentID = *t.ParentThreadID
	}
	t.ParentThreadID = optionalString(parentID)
	td := &memoryThread{
		summary:        t,
		sortOrder:      0,
		queueRevision:  1,
		messages:       msgs,
		messagesLoaded: true,
		persisted:      persisted,
		pending:        make(map[string]*pendingRequest),
		alwaysAllow:    make(map[string]bool),
		parentThreadID: parentID,
		isHelper:       t.IsHelper,
		dormantAt:      dormantAt,
	}
	b.threads[t.ID] = td
	s := b.store
	b.mu.Unlock()

	if s != nil {
		pCtx, pCancel := persistCtx()
		st := threadToStore(t, "", 0)
		st.DormantAt = dormantAt
		st.ParentThreadID = td.parentThreadID
		st.IsHelper = td.isHelper
		if err := s.CreateThread(pCtx, st); err != nil {
			log.Printf("store create thread %s error: %v", t.ID, err)
		}
		pCancel()
		b.persistMessages(t.ID, msgs)
	}
}

// KanbanBoard 返回空任务看板（各列表初始化为非 nil 空切片）。
func (b *MemoryBackend) KanbanBoard(ctx context.Context) (wire.KanbanBoard, error) {
	return wire.KanbanBoard{
		SchemaVersion: 1,
		Revision:      1,
		Projects:      []wire.KanbanProject{},
		Tasks:         []wire.KanbanTask{},
		Goals:         []wire.KanbanGoal{},
	}, nil
}

// ListProjects 返回已登记的项目列表。
func (b *MemoryBackend) ListProjects(ctx context.Context) (wire.ProjectList, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	counts := make(map[string]int, len(b.projects))
	for _, t := range b.threads {
		counts[t.summary.ProjectID]++
	}

	list := make([]wire.ProjectSummary, 0, len(b.projects))
	for _, p := range b.projects {
		if c, ok := counts[p.ID]; ok {
			p.ThreadCount = c
		} else {
			p.ThreadCount = 0
		}
		list = append(list, p)
	}
	// map 遍历无序，不排序的话侧边栏项目顺序每次刷新都会变
	sort.Slice(list, func(i, j int) bool {
		ni, nj := strings.ToLower(list[i].Name), strings.ToLower(list[j].Name)
		if ni != nj {
			return ni < nj
		}
		return list[i].ID < list[j].ID
	})

	return wire.ProjectList{
		Projects: list,
		Revision: 1,
	}, nil
}

// ListThreads 返回已登记的会话列表，支持基本的 projectID 过滤、归档过滤与游标分页。
func (b *MemoryBackend) ListThreads(ctx context.Context, params wire.ThreadListParams) (wire.ThreadList, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	type threadSortItem struct {
		summary   wire.ThreadSummary
		sortOrder int64
	}

	var candidates []threadSortItem
	for _, t := range b.threads {
		s := b.summaryForPublishLocked(t)
		if params.ProjectID != nil && *params.ProjectID != "" && s.ProjectID != *params.ProjectID {
			continue
		}
		includeArchived := params.IncludeArchived != nil && *params.IncludeArchived
		if !includeArchived && s.IsArchived {
			continue
		}
		if b.swarmEnabledFor != nil {
			s.SwarmEnabled = b.swarmEnabledFor(s.ID)
		}
		s.OrderKey = formatOrderKey(t.sortOrder)
		candidates = append(candidates, threadSortItem{
			summary:   s,
			sortOrder: t.sortOrder,
		})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].sortOrder != candidates[j].sortOrder {
			return candidates[i].sortOrder < candidates[j].sortOrder
		}
		ti, errI := parseTime(candidates[i].summary.UpdatedAt)
		tj, errJ := parseTime(candidates[j].summary.UpdatedAt)
		if errI == nil && errJ == nil && !ti.Equal(tj) {
			return ti.After(tj)
		}
		return candidates[i].summary.UpdatedAt > candidates[j].summary.UpdatedAt
	})

	var filtered []wire.ThreadSummary
	for _, c := range candidates {
		filtered = append(filtered, c.summary)
	}

	startIdx := 0
	if params.Cursor != nil && *params.Cursor != "" {
		if c, err := strconv.Atoi(*params.Cursor); err == nil && c >= 0 && c < len(filtered) {
			startIdx = c
		}
	}

	total := len(filtered)
	endIdx := total
	if params.Limit != nil && *params.Limit > 0 {
		endIdx = startIdx + *params.Limit
		if endIdx > total {
			endIdx = total
		}
	}

	var paged []wire.ThreadSummary
	if startIdx < total {
		paged = filtered[startIdx:endIdx]
	} else {
		paged = []wire.ThreadSummary{}
	}

	hasMore := endIdx < total
	var nextCursor *string
	if hasMore {
		nc := strconv.Itoa(endIdx)
		nextCursor = &nc
	}

	// C-ST-07：列表接口顺带淘汰冷归档会话的消息，避免全部历史长期占用内存。
	b.evictColdThreadsLocked()

	return wire.ThreadList{
		Threads:    paged,
		Revision:   1,
		HasMore:    hasMore,
		NextCursor: nextCursor,
	}, nil
}

// GetThread 获取单个会话的摘要信息。
func (b *MemoryBackend) GetThread(ctx context.Context, threadID string) (wire.ThreadSummary, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	td, ok := b.threads[threadID]
	if !ok {
		return wire.ThreadSummary{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		return wire.ThreadSummary{}, err
	}
	b.ensureMessagesLoadedLocked(td)
	b.touchThreadLocked(td)
	summary := b.summaryForPublishLocked(td)
	summary.ParentThreadID = optionalString(td.parentThreadID)
	summary.IsHelper = td.isHelper
	if b.swarmEnabledFor != nil {
		summary.SwarmEnabled = b.swarmEnabledFor(threadID)
	}
	return summary, nil
}

// ThreadUsage 查询指定会话每一轮的 token 用量以及全会话合计。
func (b *MemoryBackend) ThreadUsage(ctx context.Context, threadID string) (turns map[int]harness.Usage, total harness.Usage, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, ok := b.threads[threadID]; !ok {
		return nil, harness.Usage{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}

	rawTurns := b.usage[threadID]
	turns = make(map[int]harness.Usage, len(rawTurns))
	for turnIdx, u := range rawTurns {
		turns[turnIdx] = u
	}
	return turns, b.usageTotalsLocked(threadID), nil
}

// usageTotalsLocked 汇总指定会话的累计用量；调用方需持有 b.mu。
// 会话用量面板与编排记录的 bee tokens 共用本函数，保证两处口径一致。
func (b *MemoryBackend) usageTotalsLocked(threadID string) harness.Usage {
	var total harness.Usage
	for _, u := range b.usage[threadID] {
		total.InputTokens += u.InputTokens
		total.OutputTokens += u.OutputTokens
		total.CachedTokens += u.CachedTokens
	}
	return total
}

// SessionUsage 实现 kanban.SessionUsageReader：返回子会话累计的输入+输出 token（与会话用量面板同口径）
// 以及工具调用次数（按 tool 消息统计，与 ThreadActivityFromMessages 一致）。
func (b *MemoryBackend) SessionUsage(ctx context.Context, threadID string) (tokens int, toolCalls int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	td, ok := b.threads[threadID]
	if !ok {
		return 0, 0, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}

	total := b.usageTotalsLocked(threadID)
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		return 0, 0, err
	}
	b.ensureMessagesLoadedLocked(td)
	c := &td.toolCount
	if !c.valid || c.msgCount != len(td.messages) || c.loaded != td.messagesLoaded || c.evicted != td.messagesEvicted {
		c.count = 0
		for _, msg := range td.messages {
			if msg.Role == "tool" || (msg.Kind != nil && *msg.Kind == "tool") {
				c.count++
			}
		}
		c.msgCount, c.loaded, c.evicted, c.valid = len(td.messages), td.messagesLoaded, td.messagesEvicted, true
	}
	return int(total.InputTokens + total.OutputTokens), c.count, nil
}

// ThreadHistory 返回指定会话的历史消息页面（支持反向翻页语义）。
func (b *MemoryBackend) ThreadHistory(ctx context.Context, params wire.HistoryParams) (wire.HistoryPage, error) {
	b.mu.Lock()
	page, err := b.threadHistoryLocked(params)
	b.mu.Unlock()
	if err == nil {
		b.publishCompactSuggestion(params.ThreadID)
	}
	return page, err
}

func (b *MemoryBackend) threadHistoryLocked(params wire.HistoryParams) (wire.HistoryPage, error) {
	td, ok := b.threads[params.ThreadID]
	if !ok {
		return wire.HistoryPage{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", params.ThreadID),
		}
	}
	if err := b.ensureMessagesLocked(td, params.ThreadID); err != nil {
		return wire.HistoryPage{}, err
	}
	b.ensureMessagesLoadedLocked(td)
	b.touchThreadLocked(td)

	allMsgs := td.messages
	total := len(allMsgs)

	limit := 50
	if params.Limit != nil && *params.Limit > 0 {
		limit = *params.Limit
	}

	endIdx := total
	if params.Cursor != nil && *params.Cursor != "" {
		if c, err := strconv.Atoi(*params.Cursor); err == nil && c >= 0 && c <= total {
			endIdx = c
		}
	}

	startIdx := endIdx - limit
	if startIdx < 0 {
		startIdx = 0
	}

	var pageMsgs []wire.ThreadMessage
	if startIdx < endIdx {
		pageMsgs = make([]wire.ThreadMessage, endIdx-startIdx)
		copy(pageMsgs, allMsgs[startIdx:endIdx])
	} else {
		pageMsgs = []wire.ThreadMessage{}
	}
	turnsByID := make(map[string]store.Turn)
	if b.store != nil && len(pageMsgs) > 0 {
		ctx, cancel := persistCtx()
		turns, err := b.store.ListTurns(ctx, params.ThreadID)
		cancel()
		if err != nil {
			log.Printf("store list turns %s error: %v", params.ThreadID, err)
		} else {
			for _, turn := range turns {
				turnsByID[turn.ID] = turn
			}
		}
	}
	for id, turn := range td.turnTiming {
		turnsByID[id] = turn
	}
	for i := range pageMsgs {
		applyTurnTiming(&pageMsgs[i], turnsByID)
	}

	hasMore := startIdx > 0
	var nextCursor *string
	if hasMore {
		nc := strconv.Itoa(startIdx)
		nextCursor = &nc
	}

	return wire.HistoryPage{
		ThreadID:   params.ThreadID,
		Messages:   pageMsgs,
		NextCursor: nextCursor,
		HasMore:    hasMore,
	}, nil
}

// Subscribe 登记会话订阅并返回包含当前水位的快照。
func (b *MemoryBackend) Subscribe(ctx context.Context, c Conn, threadID string) (wire.SubscribeResult, error) {
	runID, seq := b.hub.Subscribe(c, threadID)
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		b.hub.Unsubscribe(c, threadID)
		return wire.SubscribeResult{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}
	summary := b.summaryForPublishLocked(td)
	history, _ := b.threadHistoryLocked(wire.HistoryParams{ThreadID: threadID})
	b.mu.Unlock()
	if runID == "" {
		runID = b.hub.BeginRun(threadID)
	}

	return wire.SubscribeResult{
		ThreadID:   threadID,
		Subscribed: true,
		Snapshot: &wire.ThreadStreamSnapshot{
			ThreadID: threadID,
			History:  history,
			Thread:   summary,
			RunID:    runID,
			Sequence: seq,
		},
	}, nil
}

// Unsubscribe 取消连接对指定会话的订阅。
func (b *MemoryBackend) Unsubscribe(ctx context.Context, c Conn, threadID string) error {
	b.hub.Unsubscribe(c, threadID)
	return nil
}

// SetProvisionalCleanupDelay 设置临时线程清理延迟时间，供测试或配置使用。
func (b *MemoryBackend) SetProvisionalCleanupDelay(d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.provisionalCleanupDelay = d
}

// CanDiscardEmptyProvisional 判断临时线程是否为空且可被自动废弃清理。
// 同时满足以下条件才为 true：是临时线程、没有任何消息、没有进行中的轮次（IsRunning）、Follow-Up 队列为空。
func (b *MemoryBackend) CanDiscardEmptyProvisional(threadID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	td, ok := b.threads[threadID]
	if !ok {
		return false
	}
	if !td.summary.IsProvisional {
		return false
	}
	// 消息被淘汰时按需回载，避免把有历史的会话误判为空。
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		return false
	}
	b.ensureMessagesLoadedLocked(td)
	b.touchThreadLocked(td)
	if len(td.messages) > 0 {
		return false
	}
	if td.summary.IsRunning {
		return false
	}
	if len(td.followUps) > 0 || td.pendingSend != nil {
		return false
	}
	return true
}

// RetainThread 将临时线程转为正式线程（IsProvisional=false，落盘，取消待执行的清理定时器，广播 threads.changed）；对非临时线程不做任何事。
func (b *MemoryBackend) RetainThread(ctx context.Context, threadID string) error {
	return b.retainThreadInternal(ctx, threadID)
}

func (b *MemoryBackend) retainThreadInternal(ctx context.Context, threadID string) error {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return store.ErrNotFound
	}
	if !td.summary.IsProvisional {
		b.mu.Unlock()
		return nil
	}
	td.summary.IsProvisional = false
	if timer, ok := b.provisionalTimers[threadID]; ok {
		timer.Stop()
		delete(b.provisionalTimers, threadID)
	}
	nowStr := b.nowLocked().Format(time.RFC3339Nano)
	td.summary.UpdatedAt = nowStr
	currentSummary := b.summaryForPublishLocked(td)
	hook := b.threadsChangedHook
	s := b.store
	dormantAt := td.dormantAt
	b.mu.Unlock()

	if s != nil {
		pCtx, pCancel := persistCtx()
		if err := s.UpdateThreadLifecycle(pCtx, threadID, currentSummary.IsDormant, dormantAt, currentSummary.IsProvisional, currentSummary.HasUnread); err != nil {
			log.Printf("store update thread lifecycle error (thread %s): %v", threadID, err)
		}
		pCancel()
	}

	payload := wire.ThreadsChanged{
		Threads: []wire.ThreadSummary{currentSummary},
	}
	b.hub.Publish(threadID, nil, nil, currentSummary)
	if hook != nil {
		hook(payload)
	}
	return nil
}

// ScheduleProvisionalCleanup 调度临时线程的延迟自动清理。
// 如果延迟后 CanDiscardEmptyProvisional 仍为 true，就调用 b.DeleteThread 静默删除。
func (b *MemoryBackend) ScheduleProvisionalCleanup(threadID string) {
	b.mu.Lock()
	if timer, ok := b.provisionalTimers[threadID]; ok {
		timer.Stop()
	}
	delay := b.provisionalCleanupDelay
	if delay <= 0 {
		delay = 2 * time.Second
	}
	var timer *time.Timer
	timer = time.AfterFunc(delay, func() {
		b.mu.Lock()
		if cur, ok := b.provisionalTimers[threadID]; ok && cur == timer {
			delete(b.provisionalTimers, threadID)
		}
		b.mu.Unlock()

		if b.CanDiscardEmptyProvisional(threadID) {
			_ = b.DeleteThread(context.Background(), threadID)
		}
	})
	b.provisionalTimers[threadID] = timer
	b.mu.Unlock()
}

func (b *MemoryBackend) reactivateThreadIfDormant(ctx context.Context, threadID string) {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok || !td.summary.IsDormant {
		b.mu.Unlock()
		return
	}
	td.summary.IsDormant = false
	td.dormantAt = nil
	nowStr := b.nowLocked().Format(time.RFC3339Nano)
	td.summary.UpdatedAt = nowStr
	currentSummary := b.summaryForPublishLocked(td)
	hook := b.threadsChangedHook
	s := b.store
	b.mu.Unlock()

	if s != nil {
		pCtx, pCancel := persistCtx()
		if err := s.UpdateThreadLifecycle(pCtx, threadID, currentSummary.IsDormant, nil, currentSummary.IsProvisional, currentSummary.HasUnread); err != nil {
			log.Printf("store update thread lifecycle error (thread %s): %v", threadID, err)
		}
		pCancel()
	}

	payload := wire.ThreadsChanged{
		Threads: []wire.ThreadSummary{currentSummary},
	}
	b.hub.Publish(threadID, nil, nil, currentSummary)
	if hook != nil {
		hook(payload)
	}
}

// familyThreadIDsLocked 返回以 rootID 为根的线程家族（包括其直接与递归子线程、helper 会话），根节点排在首位。
// 调用方必须持有 b.mu。
func (b *MemoryBackend) familyThreadIDsLocked(rootID string) []string {
	if _, ok := b.threads[rootID]; !ok {
		return nil
	}
	result := []string{rootID}
	visited := map[string]bool{rootID: true}
	queue := []string{rootID}

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		for tid, td := range b.threads {
			if visited[tid] {
				continue
			}
			if td.parentThreadID == curr {
				visited[tid] = true
				result = append(result, tid)
				queue = append(queue, tid)
			}
		}
	}
	return result
}

// ThreadDormantAt 返回指定线程的休眠时间与是否存在标记。
func (b *MemoryBackend) ThreadDormantAt(threadID string) (time.Time, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	td, ok := b.threads[threadID]
	if !ok || td.dormantAt == nil {
		return time.Time{}, false
	}
	return *td.dormantAt, true
}

// SetThreadDormantAt 直接设置指定会话的休眠时间（直接写内存和 store，不触发级联），给批量休眠和撤销使用。
func (b *MemoryBackend) SetThreadDormantAt(ctx context.Context, threadID string, t time.Time) error {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return store.ErrNotFound
	}
	if t.IsZero() {
		td.dormantAt = nil
		td.summary.IsDormant = false
	} else {
		dormantCopy := t
		td.dormantAt = &dormantCopy
		td.summary.IsDormant = true
	}
	nowStr := b.nowLocked().Format(time.RFC3339Nano)
	td.summary.UpdatedAt = nowStr
	currentSummary := b.summaryForPublishLocked(td)
	hook := b.threadsChangedHook
	s := b.store
	dormantAt := td.dormantAt
	b.mu.Unlock()

	if s != nil {
		pCtx, pCancel := persistCtx()
		if err := s.UpdateThreadLifecycle(pCtx, threadID, currentSummary.IsDormant, dormantAt, currentSummary.IsProvisional, currentSummary.HasUnread); err != nil {
			log.Printf("store update thread lifecycle error (thread %s): %v", threadID, err)
		}
		pCancel()
	}

	payload := wire.ThreadsChanged{
		Threads: []wire.ThreadSummary{currentSummary},
	}
	b.hub.Publish(threadID, nil, nil, currentSummary)
	if hook != nil {
		hook(payload)
	}
	return nil
}

// SetThreadRelation 设置会话的父会话 ID 与 helper 标记，供测试与子会话关系建立使用。
func (b *MemoryBackend) SetThreadRelation(threadID, parentThreadID string, isHelper bool) {
	b.mu.Lock()
	var currentSummary wire.ThreadSummary
	var found bool
	if td, ok := b.threads[threadID]; ok {
		td.parentThreadID = parentThreadID
		td.isHelper = isHelper
		td.summary.ParentThreadID = optionalString(parentThreadID)
		td.summary.IsHelper = isHelper
		currentSummary = b.summaryForPublishLocked(td)
		found = true
	}
	// 父会话只发布父会话自己的摘要：直接复用子会话摘要会让 UI 按事件的 ThreadID
	// 用子会话的 IsRunning 覆盖父会话卡片，导致父会话永久卡在运行中。
	var parentSummary wire.ThreadSummary
	var parentFound bool
	if parentThreadID != "" {
		if ptd, ok := b.threads[parentThreadID]; ok {
			parentSummary = b.summaryForPublishLocked(ptd)
			parentFound = true
		}
	}
	hook := b.threadsChangedHook
	s := b.store
	b.mu.Unlock()

	if found {
		b.hub.Publish(threadID, nil, nil, currentSummary)
		if parentFound {
			b.hub.Publish(parentThreadID, nil, nil, parentSummary)
		}
		if hook != nil {
			hook(wire.ThreadsChanged{
				Threads: []wire.ThreadSummary{currentSummary},
			})
		}
	}

	if s != nil {
		pCtx, pCancel := persistCtx()
		_ = s.UpdateThreadRelation(pCtx, threadID, parentThreadID, isHelper)
		pCancel()
	}
}
