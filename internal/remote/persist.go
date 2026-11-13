package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/startuptrace"
	"mcode/internal/store"
	"mcode/internal/swarm"
)

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

const (
	// followUpEventRetention 是排队审计事件的保留期；启动时清理一次，不做后台定时任务。
	followUpEventRetention = 30 * 24 * time.Hour
	// interruptedToolNotice 是崩溃收尾时追加到工具消息正文的说明。store 不依赖 remote，文案由调用方传入。
	interruptedToolNotice = "工具执行被中断：宿主重启，未收到结果。"
	// backgroundToolNotice 是启动时收敛 provider 托管后台任务的说明。
	backgroundToolNotice = "宿主重启，后台任务已中断，输出未收回。"
	// residentExitToolNotice 是常驻进程退出时收敛 provider 托管后台任务的说明。
	residentExitToolNotice = "常驻进程已退出，后台任务已中断，输出未收回。"
)

func parseTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

func projectToStore(p wire.ProjectSummary) store.Project {
	return store.Project{
		ID:   p.ID,
		Name: p.Name,
		Path: p.Path,
	}
}

func projectFromStore(p store.Project) wire.ProjectSummary {
	return wire.ProjectSummary{
		ID:          p.ID,
		Name:        p.Name,
		Path:        p.Path,
		IsExpanded:  true,
		ThreadCount: 0,
	}
}

func threadToStore(t wire.ThreadSummary, sessionID string, sortOrder int64, continuation ...string) store.Thread {
	var modelStr string
	if t.Model != nil {
		modelStr = *t.Model
	}
	updatedAt, err := parseTime(t.UpdatedAt)
	if err != nil || updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	// 侧边栏排序依据（用户最后发消息时间）；重启后靠 store 里的这一列保持顺序。
	lastUserMessageAt := parseOptionalTime(t.LastUserMessageAt)
	mode := harness.NormalizeMode(t.RuntimeMode)
	if mode == "" {
		mode = "fullAccess"
	}
	var cont string
	if len(continuation) > 0 {
		cont = continuation[0]
	}
	return store.Thread{
		ID:                t.ID,
		ProjectID:         t.ProjectID,
		Title:             t.Title,
		Provider:          t.Provider,
		Model:             modelStr,
		SessionID:         sessionID,
		UpdatedAt:         updatedAt,
		LastUserMessageAt: lastUserMessageAt,
		Archived:          t.IsArchived,
		Pinned:            t.IsPinned,
		RuntimeMode:       mode,
		SortOrder:         sortOrder,
		Continuation:      cont,

		InteractionMode: normalizeInteractionMode(t.InteractionMode),
		Effort:          derefString(t.Effort),
		Persona:         t.Persona,
		BeesProvider:    derefString(t.SwarmBeesProvider),
		BeesModel:       derefString(t.SwarmBeesModel),
		BeesEffort:      derefString(t.SwarmBeesEffort),

		FastMode:  t.FastMode,
		SpeedTier: derefString(t.SpeedTier),

		MCPDisabled: t.MCPDisabled,

		IsDormant:     t.IsDormant,
		IsProvisional: t.IsProvisional,
		HasUnread:     t.HasUnread,

		SwarmPairID: derefString(t.SwarmPairID),
		BeeTiers:    t.SwarmBeeTiers,
		SwarmMode:   t.SwarmMode,

		ParentThreadID: derefString(t.ParentThreadID),
		IsHelper:       t.IsHelper,

		ForkedFromThreadID:  derefString(t.ForkedFromThreadID),
		ForkedFromMessageID: derefString(t.ForkedFromMessageID),

		LastStatus: derefString(t.LastStatus),
	}
}

// parseOptionalTime 解析可空时间串；nil、空串或解析失败都返回 nil（字段缺失不影响排序回退）。
func parseOptionalTime(p *string) *time.Time {
	if p == nil || *p == "" {
		return nil
	}
	at, err := parseTime(*p)
	if err != nil || at.IsZero() {
		return nil
	}
	return &at
}

// optionalTime 把 nil 或零值时间转为 wire 可空时间串，非零值时返回 RFC3339Nano 字符串指针。
func optionalTime(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	s := formatTime(*t)
	return &s
}

// normalizeInteractionMode 把非 build/plan 的交互模式（含早期写入的 "chat"）归一为 "build"。
func normalizeInteractionMode(mode string) string {
	if mode == "plan" {
		return "plan"
	}
	return "build"
}

// derefString 取指针字符串值，nil 视为空串。
func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// optionalString 把空串转为 nil，非空串转为指针。
func optionalString(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func threadFromStore(t store.Thread) wire.ThreadSummary {
	var modelPtr *string
	m := t.Model
	modelPtr = &m
	runtimeMode := harness.NormalizeMode(t.RuntimeMode)
	// 早期版本写入过 schema 不认的 "standard"
	if !isValidRuntimeMode(runtimeMode) {
		runtimeMode = "fullAccess"
	}
	return wire.ThreadSummary{
		ID:                t.ID,
		ProjectID:         t.ProjectID,
		Title:             t.Title,
		Provider:          t.Provider,
		Model:             modelPtr,
		IsRunning:         false,
		IsArchived:        t.Archived,
		IsPinned:          t.Pinned,
		IsDormant:         t.IsDormant,
		HasUnread:         t.HasUnread,
		Attention:         "normal",
		Section:           "recent",
		OrderKey:          formatOrderKey(t.SortOrder),
		UpdatedAt:         formatTime(t.UpdatedAt),
		LastUserMessageAt: optionalTime(t.LastUserMessageAt),
		RuntimeMode:       runtimeMode,
		InteractionMode:   normalizeInteractionMode(t.InteractionMode),
		Effort:            optionalString(t.Effort),
		Persona:           t.Persona,
		SwarmBeesProvider: optionalString(t.BeesProvider),
		SwarmBeesModel:    optionalString(t.BeesModel),
		SwarmBeesEffort:   optionalString(t.BeesEffort),
		FastMode:          t.FastMode,
		SpeedTier:         optionalString(t.SpeedTier),
		MCPDisabled:       t.MCPDisabled,
		WorktreeRequested: t.WorktreeRequested,
		SwarmEnabled:      false,
		SwarmPairID:       optionalString(t.SwarmPairID),
		SwarmBeeTiers:     t.BeeTiers,
		SwarmMode:         swarm.NormalizeMode(t.SwarmMode),
		IsProvisional:     t.IsProvisional,
		ParentThreadID:    optionalString(t.ParentThreadID),
		IsHelper:          t.IsHelper,
		LastStatus:        optionalString(t.LastStatus),

		ForkedFromThreadID:  optionalString(t.ForkedFromThreadID),
		ForkedFromMessageID: optionalString(t.ForkedFromMessageID),
	}
}

func messageToStore(threadID string, m wire.ThreadMessage) store.Message {
	var kindStr string
	if m.Kind != nil {
		kindStr = *m.Kind
	}
	var turnIDStr string
	if m.TurnID != nil {
		turnIDStr = *m.TurnID
	}
	var isFinal bool
	if m.IsFinalResponse != nil {
		isFinal = *m.IsFinalResponse
	}
	var seq int64
	if m.Seq != nil {
		seq = int64(*m.Seq)
	}
	createdAt, err := parseTime(m.CreatedAt)
	if err != nil || createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	var media []store.MediaRef
	if len(m.Media) > 0 {
		media = make([]store.MediaRef, len(m.Media))
		for i, ref := range m.Media {
			media[i] = store.MediaRef{
				ID:        ref.ID,
				Source:    ref.Source,
				Name:      ref.Name,
				MimeType:  ref.MimeType,
				ByteCount: ref.ByteCount,
				Width:     ref.Width,
				Height:    ref.Height,
			}
		}
	}
	return store.Message{
		ID:              m.ID,
		ThreadID:        threadID,
		Seq:             seq,
		Role:            m.Role,
		Kind:            kindStr,
		Text:            m.Text,
		TurnIndex:       m.TurnIndex,
		TurnID:          turnIDStr,
		IsFinal:         isFinal,
		CreatedAt:       createdAt,
		ToolName:        m.ToolName,
		ToolInput:       m.ToolInput,
		ToolStatus:      m.ToolStatus,
		ToolCompletedAt: m.ToolCompletedAt,
		ErrKind:         m.ErrKind,
		Media:           media,
	}
}

func messageFromStore(m store.Message) wire.ThreadMessage {
	var turnIDPtr *string
	if m.TurnID != "" {
		tid := m.TurnID
		turnIDPtr = &tid
	}
	var kindPtr *string
	if m.Kind != "" {
		k := m.Kind
		kindPtr = &k
	}
	isFinal := m.IsFinal
	seqInt := int(m.Seq)
	var media []wire.MediaRef
	if len(m.Media) > 0 {
		media = make([]wire.MediaRef, len(m.Media))
		for i, ref := range m.Media {
			media[i] = wire.MediaRef{
				ID:        ref.ID,
				Source:    ref.Source,
				Name:      ref.Name,
				MimeType:  ref.MimeType,
				ByteCount: ref.ByteCount,
				Width:     ref.Width,
				Height:    ref.Height,
			}
		}
	}
	return wire.ThreadMessage{
		ID:              m.ID,
		Role:            m.Role,
		Text:            m.Text,
		CreatedAt:       formatTime(m.CreatedAt),
		TurnIndex:       m.TurnIndex,
		TurnID:          turnIDPtr,
		Kind:            kindPtr,
		IsFinalResponse: &isFinal,
		Seq:             &seqInt,
		ToolName:        m.ToolName,
		ToolInput:       m.ToolInput,
		ToolStatus:      m.ToolStatus,
		ToolCompletedAt: m.ToolCompletedAt,
		ErrKind:         m.ErrKind,
		Media:           media,
	}
}

// AttachStore 从 store 读取全部项目、线程（含归档）和消息恢复到内存，并持有 store 引用开启写穿。
func (b *MemoryBackend) AttachStore(ctx context.Context, s *store.Store) error {
	if s == nil {
		return nil
	}

	projects, err := s.ListProjects(ctx)
	if err != nil {
		return fmt.Errorf("list projects from store: %w", err)
	}

	loadedProjects := make(map[string]wire.ProjectSummary, len(projects))
	for _, p := range projects {
		loadedProjects[p.ID] = projectFromStore(p)
	}

	threads, badThreadIDs, err := s.ListThreadsTolerant(ctx)
	if err != nil {
		return fmt.Errorf("list threads from store: %w", err)
	}
	startuptrace.Mark(fmt.Sprintf("AttachStore threads listed n=%d", len(threads)))
	// 线程行解码失败：写入隔离表并跳过该会话，其余数据照常加载
	for _, id := range badThreadIDs {
		if qErr := s.QuarantineThread(ctx, id, "thread row decode failed", nil); qErr != nil {
			log.Printf("store quarantine thread error (thread %s): %v", id, qErr)
		}
		log.Printf("store thread row decode warning (thread %s): skipped", id)
	}

	allFollowUps, followUpsErr := s.ListAllFollowUps(ctx)
	allUsage, usageErr := s.ListAllTurnUsage(ctx)

	loadedThreads := make(map[string]*memoryThread, len(threads))
	loadedUsage := make(map[string]map[int]harness.Usage)
	for _, t := range threads {
		summary := threadFromStore(t)
		storeFollowUps := allFollowUps[t.ID]
		if followUpsErr != nil {
			storeFollowUps, err = s.ListFollowUps(ctx, t.ID)
		}
		if followUpsErr != nil && err != nil {
			// follow-up 解码失败不再中止整体加载：隔离该会话并跳过 follow-up 数据
			if qErr := s.QuarantineThread(ctx, t.ID, "follow ups decode failed", nil); qErr != nil {
				log.Printf("store quarantine thread error (thread %s): %v", t.ID, qErr)
			}
			log.Printf("store follow ups decode warning (thread %s): skipped, %v", t.ID, err)
			storeFollowUps = nil
		}
		wireFollowUps := make([]wire.FollowUp, 0, len(storeFollowUps))
		for _, fu := range storeFollowUps {
			wireAtts := make([]wire.Attachment, 0, len(fu.AttachmentMeta))
			for _, meta := range fu.AttachmentMeta {
				byteCount := meta.ByteCount
				wireAtts = append(wireAtts, wire.Attachment{
					ID:        meta.ID,
					Name:      meta.Name,
					MimeType:  meta.MimeType,
					ByteCount: &byteCount,
				})
				if b.attachments != nil {
					b.attachments.register(&attachmentRecord{
						id:        meta.ID,
						name:      meta.Name,
						mimeType:  meta.MimeType,
						byteCount: meta.ByteCount,
						createdAt: time.Now(),
						path:      meta.Path,
						queueRefs: 1,
					})
				}
			}
			wireFollowUps = append(wireFollowUps, wire.FollowUp{
				ID:          fu.ID,
				Text:        fu.Text,
				Attachments: wireAtts,
				CreatedAt:   formatTime(fu.CreatedAt),
				BundleID:    nil,
				Kind:        fu.Kind,
				Intent:      fu.Intent,
			})
		}

		rev := int(t.FollowUpRevision)
		if rev == 0 {
			rev = 1
		}

		// 启动时仅恢复元数据，消息不预先全量加载进内存，首次访问时惰性加载
		loadedThreads[t.ID] = &memoryThread{
			summary:        summary,
			sortOrder:      t.SortOrder,
			queueRevision:  rev,
			followUps:      wireFollowUps,
			messages:       nil,
			messagesLoaded: false,
			continuation:   t.Continuation,
			// 恢复的线程已有会话，说明转发上下文已经送达过。
			continuationSent: t.Continuation != "" && t.SessionID != "",
			workDir:          t.WorktreePath,
			sessionID:        t.SessionID,
			needsHandoff:     false,
			persisted:        make(map[string]bool),
			pending:          make(map[string]*pendingRequest),
			alwaysAllow:      make(map[string]bool),
			parentThreadID:   t.ParentThreadID,
			isHelper:         t.IsHelper,
			dormantAt:        t.DormantAt,
		}

		usages := allUsage[t.ID]
		if usageErr != nil {
			usages, err = s.ListTurnUsage(ctx, t.ID)
			if err != nil {
				return fmt.Errorf("list turn usage for thread %s: %w", t.ID, err)
			}
		}
		if len(usages) > 0 {
			threadUsage := make(map[int]harness.Usage, len(usages))
			for _, u := range usages {
				usage := harness.Usage{
					InputTokens:  u.InputTokens,
					OutputTokens: u.OutputTokens,
					CachedTokens: u.CachedTokens,
					// 旧库没有这一列时读到 0，界面自动回退到「输入 + 输出」。
					ContextTokens: u.ContextTokens,
					// 窗口上限与费用同样来自落库，旧库缺列时读到 0，界面回退到模型目录里的静态值。
					ContextWindow: u.ContextWindow,
					CostUSD:       u.CostUSD,
				}
				// 逐请求速度样本按 JSON 文本落库；解析失败就当作没有样本，不影响用量本身。
				if u.RequestsJSON != "" {
					if err := json.Unmarshal([]byte(u.RequestsJSON), &usage.Requests); err != nil {
						usage.Requests = nil
					}
				}
				threadUsage[u.TurnIndex] = usage
			}
			loadedUsage[t.ID] = threadUsage
		}

		if p, ok := loadedProjects[t.ProjectID]; ok {
			p.ThreadCount++
			loadedProjects[t.ProjectID] = p
		}
	}
	startuptrace.Mark("AttachStore per-thread queries")

	// 启动纠正：上次进程被杀或崩溃时，可能留下 status 为 running/starting 的轮次。
	// 只由主宿主收尾为 interrupted，非主宿主不能把其他进程的活跃轮次视为残留。
	b.finalizeOrphanTurns(ctx, s, loadedThreads)
	startuptrace.Mark("AttachStore finalizeOrphanTurns")

	// 排队审计事件只保留 30 天；启动清理失败不影响加载。
	pruneCtx, pruneCancel := persistCtx()
	if n, err := s.PruneFollowUpEvents(pruneCtx, time.Now().UTC().Add(-followUpEventRetention)); err != nil {
		log.Printf("store prune follow-up events error: %v", err)
	} else if n > 0 {
		log.Printf("store pruned %d follow-up events", n)
	}
	pruneCancel()
	startuptrace.Mark("AttachStore prune follow-up events")

	b.mu.Lock()
	b.store = s
	b.stewardFeedID = ""
	b.stewardFeedLoaded = false
	b.projects = loadedProjects
	b.threads = loadedThreads
	b.usage = loadedUsage
	b.mu.Unlock()
	b.StewardFeedThreadID()

	// 启动对账：上次进程遗留的待答卡片标失效、停在途中的投递标 unknown，各写一条时间线提示。
	// 必须在 store 与内存线程表挂载之后调用：提示走 appendSystemNotice，依赖 b.threads 定位会话。
	b.reconcileBoot(ctx, s)
	startuptrace.Mark("AttachStore reconcileBoot")
	return nil
}

// Store 返回已挂载的持久化 store，未挂载时返回 nil。
func (b *MemoryBackend) Store() *store.Store {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.store
}

// finalizeOrphanTurns 收尾上次进程遗留的未收尾轮次，并自愈历史残留的悬空工具消息。
// 只在启动加载阶段由持有数据目录主宿主锁的 Store 执行；其他宿主可以同时打开数据库，
// 但不能把持锁宿主的 running/starting 轮次视为上次进程异常退出的残留。
// 落库失败仅记录日志，不影响整体加载。
// 先做轮次事务（工具消息与 turn 同事务），再收尾 provider 托管的后台任务（background），
// 最后处理不属于未收尾轮次的历史残留；
// 同时刷新内存摘要的 LastStatus，让侧栏、看板与会话卡片看到终态而不是“进行中”。
func (b *MemoryBackend) finalizeOrphanTurns(ctx context.Context, s *store.Store, threads map[string]*memoryThread) {
	if !s.IsPrimary() {
		log.Printf("store: 另一个宿主进程持有数据目录，跳过启动轮次与工具消息对账")
		return
	}
	turns, err := s.ListUnfinishedTurns(ctx)
	if err != nil {
		log.Printf("store list unfinished turns error: %v", err)
		return
	}

	// 当前内存中正在执行的轮次不当作孤儿；启动加载时该集合通常为空，保留是为了防止未来运行期误调用。
	active := make(map[string]struct{}, len(threads))
	for _, td := range threads {
		if td.runTurnID != "" {
			active[td.runTurnID] = struct{}{}
		}
	}
	orphanMsgs, orphanErr := s.ListOrphanRunningToolMessages(ctx, active)
	if orphanErr != nil {
		log.Printf("store list orphan running tool messages error: %v", orphanErr)
	}

	unfinished := make(map[string]struct{}, len(turns))
	now := time.Now().UTC()
	for _, t := range turns {
		unfinished[t.ID] = struct{}{}
		writeCtx, writeCancel := persistCtx()
		// 结束时间按最后活动时间估算。
		err := s.FinalizeInterruptedTurn(writeCtx, t.ID, now, now, "宿主重启，本轮未正常收尾，已标记为中断", interruptedToolNotice)
		writeCancel()
		if err != nil {
			log.Printf("store finalize orphan turn %s error: %v", t.ID, err)
			continue
		}
		statusCtx, statusCancel := persistCtx()
		if err := s.UpdateThreadLastStatus(statusCtx, t.ThreadID, "interrupted"); err != nil && !errors.Is(err, store.ErrNotFound) {
			log.Printf("store update thread last status %s error: %v", t.ThreadID, err)
		}
		statusCancel()
		if td, ok := threads[t.ThreadID]; ok {
			lastStatus := "interrupted"
			td.summary.LastStatus = &lastStatus
		}
	}

	// 后台任务收尾：provider 托管的后台任务（如 Bash run_in_background）属于已结束的回合，
	// 不由上面的整轮事务覆盖；宿主重启后它们必然已随 provider 进程消失，统一收敛为 failed，
	// 否则界面上会永久显示为运行中。
	b.finalizeBackgroundToolMessages(ctx, s, threads, now, backgroundToolNotice)

	if orphanErr != nil {
		return
	}
	for _, m := range orphanMsgs {
		if _, ok := unfinished[m.TurnID]; ok {
			// 归属未收尾轮次的消息由上面的整轮事务统一处理，成功或失败都跳过，避免破坏原子性。
			continue
		}
		msgCtx, msgCancel := persistCtx()
		if _, err := s.FinalizeOrphanToolMessage(msgCtx, m.ID, interruptedToolNotice); err != nil {
			log.Printf("store finalize orphan tool message %s error: %v", m.ID, err)
		}
		msgCancel()
	}
}

func persistCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

// finalizeBackgroundToolMessages 把落库的 background 工具消息收敛为 failed，
// 并同步已加载进内存的同名消息（启动时内存消息通常还没加载，这里做兜底）。
// 落库失败仅记日志：对账失败不应阻断启动，下次启动会重试。
func (b *MemoryBackend) finalizeBackgroundToolMessages(ctx context.Context, s *store.Store, threads map[string]*memoryThread, completedAt time.Time, notice string) {
	writeCtx, writeCancel := persistCtx()
	n, err := s.FinalizeBackgroundToolMessages(writeCtx, completedAt, notice)
	writeCancel()
	if err != nil {
		log.Printf("store finalize background tool messages error: %v", err)
		return
	}
	if n == 0 {
		return
	}
	completedAtStr := formatTime(completedAt)
	for _, td := range threads {
		changed := false
		for idx := range td.messages {
			if td.messages[idx].ToolStatus == nil || *td.messages[idx].ToolStatus != "background" {
				continue
			}
			failed := "failed"
			stamp := completedAtStr
			td.messages[idx].ToolStatus = &failed
			td.messages[idx].ToolCompletedAt = &stamp
			td.messages[idx].Text = appendToolNoticeText(td.messages[idx].Text, notice)
			changed = true
		}
		if changed {
			// 摘要里的后台任务计数按工具状态统计，状态改写后要重新算。
			td.bgCount.invalidate()
		}
	}
}

// appendToolNoticeText 幂等追加中断说明，口径与 store.appendToolNotice 一致。
func appendToolNoticeText(text, notice string) string {
	if notice == "" || strings.Contains(text, notice) {
		return text
	}
	if strings.TrimSpace(text) == "" {
		return notice
	}
	return text + "\n\n" + notice
}

// ensureMessagesLoadedLocked 确保指定会话的历史消息已从 store 恢复到内存中。
// 本函数负责首次惰性加载，与 ensureMessagesLocked（负责淘汰回载）协同，二者均会同时维护 messagesLoaded 与 messagesEvicted。
// 调用方需持有 b.mu 互斥锁。已加载则直接返回；加载失败时只记日志并保持未加载态，
// 让下一次访问重试，否则一次 store 故障后本进程内该会话历史恒为空。
func (b *MemoryBackend) ensureMessagesLoadedLocked(td *memoryThread) {
	if td == nil {
		return
	}
	if td.messagesLoaded && !td.messagesEvicted {
		return
	}
	s := b.store
	if s == nil {
		td.messagesLoaded = true
		return
	}

	ctx, cancel := persistCtx()
	defer cancel()

	var allStoreMsgs []store.Message
	var badMsgIDs []string
	beforeSeq := int64(0)
	var loadErr error
	for {
		page, hasMore, badIDs, err := s.ListMessagesTolerant(ctx, td.summary.ID, beforeSeq, 100)
		if err != nil {
			loadErr = err
			break
		}
		badMsgIDs = append(badMsgIDs, badIDs...)
		if len(page) == 0 {
			break
		}
		allStoreMsgs = append(page, allStoreMsgs...)
		if !hasMore {
			break
		}
		beforeSeq = page[0].Seq
	}

	if loadErr != nil {
		// 保持 messagesLoaded/messagesEvicted 原状：冷淘汰的会话下次仍走 ensureMessagesLocked 回载。
		log.Printf("store list messages error (thread %s): %v", td.summary.ID, loadErr)
		return
	}

	// 单条消息解码失败：照常加载能读出的消息，并隔离坏消息所在会话
	if len(badMsgIDs) > 0 {
		if qErr := s.QuarantineThread(ctx, td.summary.ID, "message decode failed", badMsgIDs); qErr != nil {
			log.Printf("store quarantine thread error (thread %s): %v", td.summary.ID, qErr)
		}
		log.Printf("store message decode warning (thread %s): %d undecodable message(s) quarantined, bad ids %v", td.summary.ID, len(badMsgIDs), badMsgIDs)
	}

	wireMsgs := make([]wire.ThreadMessage, len(allStoreMsgs))
	if td.persisted == nil {
		td.persisted = make(map[string]bool, len(allStoreMsgs)+len(td.messages))
	}
	storeIDs := make(map[string]bool, len(allStoreMsgs))
	for i, m := range allStoreMsgs {
		wireMsgs[i] = messageFromStore(m)
		td.persisted[m.ID] = true
		storeIDs[m.ID] = true
	}

	// 如果加载前内存中已有新写入的消息，按 ID 去重，只追加不在已加载 store 消息集合中的内存消息并保持顺序
	for _, m := range td.messages {
		if !storeIDs[m.ID] {
			wireMsgs = append(wireMsgs, m)
		}
	}

	td.messages = wireMsgs
	td.messagesLoaded = true
	td.messagesEvicted = false

	if td.sessionID == "" && len(wireMsgs) > 0 {
		td.needsHandoff = true
	}
}

// persistMessages 把指定会话的消息写穿到 store。
// 持锁时拷贝出待写消息与已持久化状态，释放锁后再执行数据库操作；错误仅记录日志。
func (b *MemoryBackend) persistMessages(threadID string, msgs []wire.ThreadMessage) {
	type opItem struct {
		msg   store.Message
		isNew bool
	}

	b.mu.Lock()
	s := b.store
	if s == nil {
		b.mu.Unlock()
		return
	}
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return
	}

	ops := make([]opItem, 0, len(msgs))
	seen := make(map[string]bool, len(msgs))
	for _, m := range msgs {
		// persisted 只在写库成功后置位：写失败的消息下一轮仍按新增重试，
		// 否则它会一直落到 UpdateMessage 分支，在进程内永久不入库。
		isNew := !td.persisted[m.ID] && !seen[m.ID]
		seen[m.ID] = true
		ops = append(ops, opItem{
			msg:   messageToStore(threadID, m),
			isNew: isNew,
		})
	}
	// 只重试内存中仍有的消息，不为重试触发历史加载。
	for _, m := range td.messages {
		if td.persistRetry[m.ID] && !seen[m.ID] {
			ops = append(ops, opItem{msg: messageToStore(threadID, m)})
			seen[m.ID] = true
		}
	}
	b.mu.Unlock()

	for _, item := range ops {
		if !item.isNew {
			ctx, cancel := persistCtx()
			err := s.UpdateMessage(ctx, item.msg)
			cancel()
			if err == nil {
				b.mu.Lock()
				if td := b.threads[threadID]; td != nil {
					delete(td.persistRetry, item.msg.ID)
				}
				b.mu.Unlock()
				continue
			}
			b.mu.Lock()
			if td := b.threads[threadID]; td != nil {
				if td.persistRetry == nil {
					td.persistRetry = make(map[string]bool)
				}
				td.persistRetry[item.msg.ID] = true
			}
			b.mu.Unlock()
			if !errors.Is(err, store.ErrNotFound) {
				log.Printf("store update message error (thread %s, msg %s): %v", threadID, item.msg.ID, err)
				continue
			}
			// 库里没有这条消息（上次追加失败或已被清理）时回退为追加，避免它永远不入库。
		}
		ctx, cancel := persistCtx()
		err := b.appendPersistedMessage(ctx, s, threadID, item.msg)
		cancel()
		if err != nil {
			log.Printf("store append message error (thread %s, msg %s): %v", threadID, item.msg.ID, err)
		}
	}
}

// appendPersistedMessage 追加一条消息，成功后才置持久化标记并回填 seq。
// 写库期间不持有 b.mu；调用方需传入解锁状态下取得的 store。
func (b *MemoryBackend) appendPersistedMessage(ctx context.Context, s *store.Store, threadID string, msg store.Message) error {
	saved, err := s.AppendMessage(ctx, msg)
	if err != nil {
		return err
	}
	b.mu.Lock()
	if td, ok := b.threads[threadID]; ok {
		if td.persisted == nil {
			td.persisted = make(map[string]bool)
		}
		td.persisted[saved.ID] = true
		delete(td.persistRetry, saved.ID)
		b.ensureMessagesLoadedLocked(td)
		for idx := range td.messages {
			if td.messages[idx].ID == saved.ID {
				seqInt := int(saved.Seq)
				td.messages[idx].Seq = &seqInt
				break
			}
		}
	}
	b.mu.Unlock()
	return nil
}

// persistTurnUsage 把指定会话与轮次的用量写穿到 store。
func (b *MemoryBackend) persistTurnUsage(threadID string, turnIndex int, u harness.Usage) {
	b.mu.Lock()
	s := b.store
	b.mu.Unlock()
	if s == nil {
		return
	}

	ctx, cancel := persistCtx()
	defer cancel()
	// 逐请求速度样本是本轮至今的完整列表，整段覆盖写入，空列表写空串。
	requestsJSON := ""
	if len(u.Requests) > 0 {
		if raw, err := json.Marshal(u.Requests); err == nil {
			requestsJSON = string(raw)
		} else {
			log.Printf("marshal turn usage requests error (thread %s, turn %d): %v", threadID, turnIndex, err)
		}
	}
	if err := s.UpsertTurnUsage(ctx, store.TurnUsage{
		ThreadID:     threadID,
		TurnIndex:    turnIndex,
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		CachedTokens: u.CachedTokens,
		// 单次请求的上下文大小只影响上下文窗口占用的口径，计费用量仍由上面三项累计。
		ContextTokens: u.ContextTokens,
		// 窗口上限与本轮费用供用量面板展示（重启后仍需保留）。
		ContextWindow: u.ContextWindow,
		CostUSD:       u.CostUSD,
		RequestsJSON:  requestsJSON,
	}); err != nil {
		log.Printf("store upsert turn usage error (thread %s, turn %d): %v", threadID, turnIndex, err)
	}
}

// persistThreadWorkDir 把会话迁入 worktree 后的工作目录写穿到 store，同时清空旧 session。
func (b *MemoryBackend) persistThreadWorkDir(threadID, workDir string, worktreeRequested bool) {
	b.mu.Lock()
	s := b.store
	b.mu.Unlock()
	if s == nil {
		return
	}

	ctx, cancel := persistCtx()
	defer cancel()
	if err := s.UpdateThreadWorkDir(ctx, threadID, workDir, worktreeRequested); err != nil {
		log.Printf("store update thread workdir error (thread %s): %v", threadID, err)
	}
}

// persistThreadTitle 把指定会话的新标题写穿到 store。
func (b *MemoryBackend) persistThreadTitle(threadID, title string) {
	b.mu.Lock()
	s := b.store
	b.mu.Unlock()
	if s == nil {
		return
	}

	ctx, cancel := persistCtx()
	defer cancel()
	if err := s.UpdateThreadTitle(ctx, threadID, title); err != nil {
		log.Printf("store update thread title error (thread %s): %v", threadID, err)
	}
}

// persistThreadPinned 把指定会话的置顶状态写穿到 store。
func (b *MemoryBackend) persistThreadPinned(threadID string, pinned bool) {
	b.mu.Lock()
	s := b.store
	b.mu.Unlock()
	if s == nil {
		return
	}

	ctx, cancel := persistCtx()
	defer cancel()
	if err := s.SetThreadPinned(ctx, threadID, pinned); err != nil {
		log.Printf("store update thread pinned error (thread %s): %v", threadID, err)
	}
}

// persistThreadArchived 把指定会话的归档状态写穿到 store。
func (b *MemoryBackend) persistThreadArchived(threadID string, archived bool) {
	b.mu.Lock()
	s := b.store
	b.mu.Unlock()
	if s == nil {
		return
	}

	ctx, cancel := persistCtx()
	defer cancel()
	if err := s.SetThreadArchived(ctx, threadID, archived); err != nil {
		log.Printf("store update thread archived error (thread %s): %v", threadID, err)
	}
	// 归档是会话的收尾点，此时补一次 Nowledge 同步（后台串行，失败只记日志）。
	if archived {
		b.triggerNowledgeSync(threadID)
	}
}

// persistThreadRuntimeMode 把指定会话的新运行模式写穿到 store。
func (b *MemoryBackend) persistThreadRuntimeMode(threadID, mode string) {
	b.mu.Lock()
	s := b.store
	b.mu.Unlock()
	if s == nil {
		return
	}

	ctx, cancel := persistCtx()
	defer cancel()
	if err := s.UpdateThreadRuntimeMode(ctx, threadID, mode); err != nil {
		log.Printf("store update thread runtime mode error (thread %s): %v", threadID, err)
	}
}

// persistThreadModel 把指定会话的新模型写穿到 store。
func (b *MemoryBackend) persistThreadModel(threadID, model string) {
	b.mu.Lock()
	s := b.store
	b.mu.Unlock()
	if s == nil {
		return
	}

	ctx, cancel := persistCtx()
	defer cancel()
	if err := s.UpdateThreadModel(ctx, threadID, model); err != nil {
		log.Printf("store update thread model error (thread %s): %v", threadID, err)
	}
}

// persistThreadProvider 把指定会话的新供应商写穿到 store。
func (b *MemoryBackend) persistThreadProvider(threadID, provider string) {
	b.mu.Lock()
	s := b.store
	b.mu.Unlock()
	if s == nil {
		return
	}

	ctx, cancel := persistCtx()
	defer cancel()
	if err := s.UpdateThreadProvider(ctx, threadID, provider); err != nil {
		log.Printf("store update thread provider error (thread %s): %v", threadID, err)
	}
}

var unreadPersistLocks sync.Map

// persistThreadUnread 把指定会话的未读状态写穿到 store。
func (b *MemoryBackend) persistThreadUnread(threadID string, hasUnread bool) {
	lock, _ := unreadPersistLocks.LoadOrStore(threadID, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	b.mu.Lock()
	s := b.store
	td := b.threads[threadID]
	if td == nil {
		b.mu.Unlock()
		return
	}
	hasUnread = td.summary.HasUnread
	b.mu.Unlock()
	if s == nil {
		return
	}

	ctx, cancel := persistCtx()
	defer cancel()
	if err := s.SetThreadUnread(ctx, threadID, hasUnread); err != nil {
		log.Printf("store update thread unread error (thread %s): %v", threadID, err)
	}
}

// persistThreadLifecycle 把指定会话的生命周期状态写穿到 store。
func (b *MemoryBackend) persistThreadLifecycle(threadID string, isDormant bool, dormantAt *time.Time, isProvisional bool, hasUnread bool) {
	b.mu.Lock()
	s := b.store
	b.mu.Unlock()
	if s == nil {
		return
	}

	ctx, cancel := persistCtx()
	defer cancel()
	if err := s.UpdateThreadLifecycle(ctx, threadID, isDormant, dormantAt, isProvisional, hasUnread); err != nil {
		log.Printf("store update thread lifecycle error (thread %s): %v", threadID, err)
	}
}
