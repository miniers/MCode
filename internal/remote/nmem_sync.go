package remote

// 本文件负责与 Nowledge Mem 的两项对接：
//   1. 首轮注入：把项目空间的今日 Working Memory 作为背景段追加到首轮指导词末尾；
//   2. 会话同步：把 mcode 会话按线程串行、可合并地导入 Nowledge Threads。
//
// 两条路径都只在宿主注入了 nmem.Client（b.nowledge 非 nil）且对应开关打开时才发请求，
// 任何失败都只记日志，绝不影响正常对话流程。

import (
	"context"
	"errors"
	"log"
	"strconv"
	"strings"
	"time"

	"mcode/internal/config"
	"mcode/internal/memspace"
	"mcode/internal/nmem"
	"mcode/internal/store"
	"mcode/internal/swarm"
)

const (
	// nowledgeThreadIDPrefix 是导入 Nowledge Threads 时给 mcode 会话 ID 加的前缀，
	// 避免与 Nowledge 自身的会话 ID 混在一起。
	nowledgeThreadIDPrefix = "mcode-"
	// nowledgeThreadSource 是导入时标记的来源。
	nowledgeThreadSource = "mcode"

	// nowledgeWorkingMemoryTimeout 是首轮注入 Working Memory 的整体超时（解析空间 + 拉正文）。
	nowledgeWorkingMemoryTimeout = 3 * time.Second
	// nowledgeWorkingMemoryTTL 是 Working Memory 段落的缓存时长；buildGuidance 每轮都会调用，
	// 缓存避免每次都发请求。
	nowledgeWorkingMemoryTTL = time.Minute

	// nowledgeSyncTimeout 是后台会话同步（轮次结束、归档）的整体超时。
	nowledgeSyncTimeout = 10 * time.Second
	// nowledgeSyncCompactTimeout 是压缩前同步的超时：必须赶在压缩开始前完成。
	nowledgeSyncCompactTimeout = 10 * time.Second
	// nowledgeSyncDeleteTimeout 是删除会话前同步的超时。
	nowledgeSyncDeleteTimeout = 5 * time.Second
	// nowledgeSyncMessageLimit 是单次同步读取的最大消息条数（取最近的一页）；服务器按前缀幂等去重。
	nowledgeSyncMessageLimit = 2000
	// nowledgeSyncMinMessages 是触发导入所需的最少有效消息条数。
	nowledgeSyncMinMessages = 2
	// nowledgeAutomationTitlePrefix 是自动化运行会话的标题前缀：internal/automation/service.go 的
	// startRunSession 新建会话时用 "Automation: <自动化名>" 作标题，而 store.Thread 没有单独的
	// 自动化标记字段，标题前缀因而是当前唯一可用的判断依据（用户自建同名标题的会话会被一并排除）。
	nowledgeAutomationTitlePrefix = "Automation: "
)

// nowledgeSyncIdleDelay 是线程空闲多久后才补一次同步的防抖延迟。
// 声明成变量而不是常量，便于测试把它改成毫秒级。
var nowledgeSyncIdleDelay = 20 * time.Minute

// nowledgeWorkingMemoryEntry 是 Working Memory 段落的短期缓存，由 b.nowledgeMu 保护。
type nowledgeWorkingMemoryEntry struct {
	key  string
	text string
	at   time.Time
}

// nowledgeSyncState 是单个会话的同步状态，由 b.mu 保护。
// 记录会一直保留到会话被删除，用于跨次比较有效消息数（消息没变化就不重复发请求）。
type nowledgeSyncState struct {
	// running 表示该会话正有一个后台同步在跑。
	running bool
	// pending 表示同步进行中又被触发，收尾后再补跑一次（多次触发合并成一次）。
	pending bool
	// timer 是防抖定时器：线程空闲 nowledgeSyncIdleDelay 后才真正同步；nil 表示没有待触发的定时器。
	timer *time.Timer
	// lastCount 是上次成功导入时的有效消息数，counted 为 false 表示还没成功导入过。
	lastCount int
	counted   bool
	// dirty 表示该会话还有未成功导入的工作；dirtyPersisted 表示这份脏状态已写进
	// app_settings 的 nowledge_sync_pending 集合（见 nmem_sync_pending.go）。
	// 这两个字段与 generation 只在同时持有 b.nowledgePendingMu 与 b.mu 时读写，
	// 以便把代次检查与持久集合的更新放进同一个临界区；其余字段仍只由 b.mu 保护。
	dirty          bool
	dirtyPersisted bool
	// generation 每次新的同步请求自增；导入成功时只有代次未变才清标记，
	// 避免用旧批次的成功清掉导入期间到达的新工作。
	generation uint64
}

// nowledgeWorkingMemory 返回首轮注入用的 Working Memory 段落；未注入客户端、关闭注入开关、
// 空间里没有今日简报、或请求失败时返回空串。
func (b *MemoryBackend) nowledgeWorkingMemory(workDir string) string {
	c := b.nowledgeClient()
	if c == nil {
		return ""
	}
	cfg := b.nowledgeSettings()
	if !cfg.WorkingMemoryEnabled() {
		return ""
	}
	budget := cfg.WorkingMemoryBudget()

	ctx, cancel := context.WithTimeout(context.Background(), nowledgeWorkingMemoryTimeout)
	defer cancel()

	space := memspace.Resolve(ctx, c, workDir, cfg.SpaceFor(workDir))
	if space == "" {
		space = nmem.DefaultSpace
	}
	key := workDir + "\x00" + space + "\x00" + strconv.Itoa(budget)

	b.nowledgeMu.Lock()
	if e := b.nowledgeWMCache; e != nil && e.key == key && time.Since(e.at) < nowledgeWorkingMemoryTTL {
		text := e.text
		b.nowledgeMu.Unlock()
		return text
	}
	b.nowledgeMu.Unlock()

	content, err := c.WorkingMemory(ctx, space)
	if err != nil {
		log.Printf("读取 Nowledge Working Memory 失败（空间 %s）: %v", space, err)
		content = ""
	}
	text := formatNowledgeWorkingMemory(space, content, budget)

	b.nowledgeMu.Lock()
	b.nowledgeWMCache = &nowledgeWorkingMemoryEntry{key: key, text: text, at: time.Now()}
	b.nowledgeMu.Unlock()
	return text
}

// nowledgeMemoryToolsNote 是 Nowledge 接入时首轮注入的记忆工具说明。
const nowledgeMemoryToolsNote = "## Nowledge memory tools\n\n" +
	"When the user explicitly asks you to remember something, save it with the memory tool's propose action and user_requested set to true, " +
	"with scope global when it holds across all of the user's projects; do not write it to a memory file of your own. " +
	"After completing a task, if you made non-obvious decisions with reasons, hit time-consuming pitfalls, learned user preferences, or found reusable steps, " +
	"proactively propose a memory with the same action without user_requested (it enters the review inbox for user confirmation). " +
	"Limit proposals to at most one or two per task, search memories first to check for duplicates, and do not propose current-turn progress or anything obvious from the code. " +
	"Keep each memory short and lead with the conclusion. " +
	"To look up past decisions and how earlier problems were solved, search memories and past conversations."

// formatNowledgeWorkingMemory 把 Working Memory 正文包装成首轮注入段落：按预算（按 rune 计数）
// 截断正文，超预算时在末尾标注已截断；正文为空时返回空串。
func formatNowledgeWorkingMemory(space, content string, budget int) string {
	body := strings.TrimSpace(content)
	if body == "" {
		return ""
	}
	if budget > 0 {
		if runes := []rune(body); len(runes) > budget {
			body = string(runes[:budget]) + "\n\n(truncated: Working Memory exceeded the " + strconv.Itoa(budget) + "-character injection budget)"
		}
	}

	var sb strings.Builder
	sb.WriteString("## Nowledge Working Memory (space: ")
	sb.WriteString(space)
	sb.WriteString(")\n\n")
	sb.WriteString("This is the daily briefing from the user's memory system (Nowledge Mem). ")
	sb.WriteString("Treat it as background context only: if it conflicts with the project documents and guidance above, the project documents win.\n\n")
	sb.WriteString(body)
	return sb.String()
}

// nowledgeSyncConfig 返回可用的客户端与配置：未注入客户端、或关闭了会话同步时 ok 为 false，
// 调用方据此直接跳过（不发任何请求）。
func (b *MemoryBackend) nowledgeSyncConfig() (*nmem.Client, config.MemoryConfig, bool) {
	c := b.nowledgeClient()
	if c == nil {
		return nil, config.MemoryConfig{}, false
	}
	cfg := b.nowledgeSettings()
	if !cfg.ThreadSyncEnabled() {
		return nil, config.MemoryConfig{}, false
	}
	return c, cfg, true
}

// nowledgeShouldSyncThread 判断该会话是否需要同步到 Nowledge：
// bee/子会话（IsHelper 或带父会话）与自动化运行会话默认不同步，由各自开关单独开启。
func nowledgeShouldSyncThread(thread store.Thread, cfg config.MemoryConfig) bool {
	if thread.IsHelper || thread.ParentThreadID != "" {
		return cfg.HelperThreadSyncEnabled()
	}
	if strings.HasPrefix(strings.TrimSpace(thread.Title), nowledgeAutomationTitlePrefix) {
		return cfg.AutomationThreadSyncEnabled()
	}
	return true
}

// nowledgeSyncStateLocked 返回该会话的同步状态，没有则创建；调用方必须持有 b.mu。
func (b *MemoryBackend) nowledgeSyncStateLocked(threadID string) *nowledgeSyncState {
	if b.nowledgeSync == nil {
		b.nowledgeSync = make(map[string]*nowledgeSyncState)
	}
	state, ok := b.nowledgeSync[threadID]
	if !ok {
		state = &nowledgeSyncState{}
		b.nowledgeSync[threadID] = state
	}
	return state
}

// scheduleNowledgeSync 为该线程安排一次防抖同步：每次调用都把定时器往后推，
// 线程真正空闲 nowledgeSyncIdleDelay 后才触发一次导入。未注入客户端或关闭同步开关时不排期。
func (b *MemoryBackend) scheduleNowledgeSync(threadID string) {
	if _, _, ok := b.nowledgeSyncConfig(); !ok {
		return
	}
	// 先落持久待同步标记（同一会话多次排期合并成一次写入），再排防抖定时器：
	// 这样即使进程在定时器触发前退出，下次启动仍能补传。
	b.markNowledgePending(threadID)

	b.mu.Lock()
	defer b.mu.Unlock()
	// Close 已取消 b.ctx：关停后不再排期，避免定时器在 wg.Wait 之后才触发。
	if b.ctx.Err() != nil {
		return
	}
	state := b.nowledgeSyncStateLocked(threadID)
	if state.timer != nil {
		state.timer.Stop()
	}
	state.timer = time.AfterFunc(nowledgeSyncIdleDelay, func() { b.fireNowledgeSyncTimer(threadID) })
}

// fireNowledgeSyncTimer 是防抖定时器的回调：清掉定时器标记后交给 triggerNowledgeSync。
func (b *MemoryBackend) fireNowledgeSyncTimer(threadID string) {
	b.mu.Lock()
	if state, ok := b.nowledgeSync[threadID]; ok {
		state.timer = nil
	}
	b.mu.Unlock()
	b.triggerNowledgeSync(threadID)
}

// cancelNowledgeSyncTimer 停掉并清除该线程待触发的防抖定时器，返回之前是否有待同步的定时器。
func (b *MemoryBackend) cancelNowledgeSyncTimer(threadID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	state, ok := b.nowledgeSync[threadID]
	if !ok || state.timer == nil {
		return false
	}
	state.timer.Stop()
	state.timer = nil
	return true
}

// forgetNowledgeSync 丢掉某个会话的同步状态（会话已删除时调用）。
func (b *MemoryBackend) forgetNowledgeSync(threadID string) {
	b.mu.Lock()
	if state, ok := b.nowledgeSync[threadID]; ok {
		if state.timer != nil {
			state.timer.Stop()
			state.timer = nil
		}
		// 仍在跑的同步收尾时会用到这份 state，保留到那次收尾为止。
		if !state.running {
			delete(b.nowledgeSync, threadID)
		}
	}
	b.mu.Unlock()
	// 会话已删除：持久待同步集合里也不应再留它（正文已不存在，补传没有意义）。
	b.forgetNowledgePending(threadID)
}

// nowledgeSyncCount 返回上次成功导入时的有效消息数；还没有成功导入过时 ok 为 false。
func (b *MemoryBackend) nowledgeSyncCount(threadID string) (int, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if state, ok := b.nowledgeSync[threadID]; ok && state.counted {
		return state.lastCount, true
	}
	return 0, false
}

// setNowledgeSyncCount 记录本次成功导入的有效消息数（调用方不得持有 b.mu）。
func (b *MemoryBackend) setNowledgeSyncCount(threadID string, count int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	state := b.nowledgeSyncStateLocked(threadID)
	state.lastCount = count
	state.counted = true
}

// ResumeNowledgeSync 读取持久待同步集合，把上次退出时没传完的会话重新交给后台同步：
// 退出时不再等待网络，未完成的部分留到下次启动补传。
// 配置未启用或客户端未注入时既不发请求也不清标记，留待后续调用重试；
// 本函数不等待网络。恢复不是新工作：只确保已有待同步任务在后台跑起来，
// 不增代、不设补跑标记，重复或并发调用都不会追加请求。
func (b *MemoryBackend) ResumeNowledgeSync() {
	if _, _, ok := b.nowledgeSyncConfig(); !ok {
		return
	}
	for _, threadID := range b.resumeNowledgePendingIDs() {
		b.startNowledgeSync(threadID, false)
	}
}

// triggerNowledgeSync 按线程串行地在后台触发一次同步：同一线程已有同步在跑时只标记补跑，
// 让下一次收尾时再跑一遍（多次触发合并成一次），既不并发也不堆积。
// 这里代表真正的新工作，因此会增代并确保持久待同步标记存在。
func (b *MemoryBackend) triggerNowledgeSync(threadID string) {
	b.startNowledgeSync(threadID, true)
}

// startNowledgeSync 是同步启动的唯一入口：newWork 为 true 表示调用方带来了新工作
// （增代、设补跑标记、取消防抖定时器），false 表示恢复（只让已有待同步任务跑起来）。
// running 的检查与预留在 b.mu 内完成，并发的恢复或触发不会为同一会话启动第二个 goroutine。
func (b *MemoryBackend) startNowledgeSync(threadID string, newWork bool) {
	if _, _, ok := b.nowledgeSyncConfig(); !ok {
		return
	}
	if newWork {
		// 立即同步时不再保留待触发的防抖定时器（归档也走这条路径）。
		b.cancelNowledgeSyncTimer(threadID)
		// 每次新工作都要增代并确保持久标记存在：正在跑的批次成功收尾时看到代次已变，
		// 就不会把这次请求要做的补跑清掉。
		b.markNowledgePending(threadID)
	}

	b.mu.Lock()
	// Close 已取消 b.ctx：关停后不再启动新的同步；检查与 wg.Add 在同一临界区内，
	// 保证 wg.Add 不会排到 Close 的 wg.Wait 之后。
	if b.ctx.Err() != nil {
		b.mu.Unlock()
		return
	}
	state := b.nowledgeSyncStateLocked(threadID)
	if state.running {
		// 恢复只是确保已有任务在跑：已经在跑就原子跳过，不设补跑，也不增代。
		if newWork {
			state.pending = true
		}
		b.mu.Unlock()
		return
	}
	state.running = true
	b.wg.Add(1)
	b.mu.Unlock()

	go func() {
		defer b.wg.Done()
		for {
			b.syncNowledgeThread(threadID, nowledgeSyncTimeout)

			b.mu.Lock()
			if b.ctx.Err() != nil {
				// 已进入关停：不再补跑，未传完的会话留给下次启动的恢复。
				state.pending = false
				state.running = false
				b.mu.Unlock()
				return
			}
			if state.pending {
				state.pending = false
				b.mu.Unlock()
				continue
			}
			// 保留 state 不删：里面的 lastCount 用于下次比较消息是否变过。
			state.running = false
			b.mu.Unlock()
			return
		}
	}()
}

// syncNowledgeThread 同步执行一次会话导入，超时 timeout；失败只记日志，不影响调用方。
// 这里的调用都是“必须立即落盘”的时机（压缩、删除、归档），所以先取消待触发的防抖定时器，
// 避免同一会话马上被定时器重复导入。
//
// 生产上除了后台同步 goroutine，删除与压缩路径也会直接调用本函数，因此入口自己登记生命周期：
// Close 的 wg.Wait 会等这次调用（含其中的 GetThread/ListMessages）结束才返回，
// 之后不会再有调用方使用 store。
func (b *MemoryBackend) syncNowledgeThread(threadID string, timeout time.Duration) {
	if !b.beginNowledgeSyncOp() {
		// 已进入关停：不再发起新请求，未传完的会话留给下次启动的恢复。
		return
	}
	defer b.wg.Done()

	b.cancelNowledgeSyncTimer(threadID)
	c, cfg, ok := b.nowledgeSyncConfig()
	if !ok {
		return
	}
	// 网络 context 派生自 b.ctx：退出时取消 b.cancel 能立刻打断在飞的 HTTP，
	// 不必等满 timeout；被取消/超时/出错时待同步标记保留，下次启动继续补传。
	ctx, cancel := context.WithTimeout(b.ctx, timeout)
	defer cancel()
	if err := b.importNowledgeThread(ctx, c, cfg, threadID); err != nil {
		log.Printf("同步会话到 Nowledge 失败（会话 %s）: %v", threadID, err)
	}
}

// beginNowledgeSyncOp 为一次同步入口登记生命周期：在 b.mu 内原子检查 b.ctx 并 wg.Add(1)，
// 与 Close 的 cancel → 加锁 → Wait 顺序配合，保证 Add 不会排到 Wait 之后（关停后直接返回 false）。
// 已在后台 goroutine 内调用时是嵌套登记，不会与 Wait 产生竞态。
func (b *MemoryBackend) beginNowledgeSyncOp() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ctx.Err() != nil {
		return false
	}
	b.wg.Add(1)
	return true
}

// importNowledgeThread 把会话的全部有效消息幂等导入 Nowledge；会话不存在或有效消息不足时
// 直接返回，不算失败。
func (b *MemoryBackend) importNowledgeThread(ctx context.Context, c *nmem.Client, cfg config.MemoryConfig, threadID string) error {
	// 发请求前先确保持久待同步标记存在，并记下本次快照代次：成功收尾时只有代次未变才清标记。
	gen := b.beginNowledgeImport(threadID)

	b.mu.Lock()
	s := b.store
	b.mu.Unlock()
	if s == nil {
		return nil
	}

	thread, err := s.GetThread(ctx, threadID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// 会话已删除：一并丢掉它的同步状态（含消息数记录、待触发定时器与持久标记）。
			b.forgetNowledgeSync(threadID)
			return nil
		}
		return err
	}

	// 编排 bee 子会话与自动化运行会话默认不同步（见 nowledgeShouldSyncThread）。
	// 按规则确定无需传输：清掉残留标记（代次已变则保留，说明期间又有新工作）。
	if !nowledgeShouldSyncThread(thread, cfg) {
		b.clearNowledgePending(threadID, gen)
		return nil
	}

	msgs, _, err := s.ListMessages(ctx, threadID, 0, nowledgeSyncMessageLimit)
	if err != nil {
		return err
	}
	converted := nowledgeMessages(msgs)
	if len(converted) < nowledgeSyncMinMessages {
		b.clearNowledgePending(threadID, gen)
		return nil
	}
	// 消息没变化就不重复导入：服务端每次导入都可能重跑摘要与提炼，白花 token。
	if last, ok := b.nowledgeSyncCount(threadID); ok && last == len(converted) {
		b.clearNowledgePending(threadID, gen)
		return nil
	}

	workDir := b.projectPath(thread.ProjectID)
	space := memspace.Resolve(ctx, c, workDir, cfg.SpaceFor(workDir))
	if space == "" {
		space = nmem.DefaultSpace
	}

	err = c.ImportThread(ctx, nmem.Thread{
		ID:       nowledgeThreadIDPrefix + threadID,
		Title:    thread.Title,
		Source:   nowledgeThreadSource,
		Space:    space,
		Messages: converted,
		Metadata: nowledgeThreadMetadata(thread, workDir),
	})
	if err != nil {
		// 取消、超时或 HTTP 出错都保留待同步标记，下次启动或下次排期继续补传。
		return err
	}
	b.setNowledgeSyncCount(threadID, len(converted))
	b.clearNowledgePending(threadID, gen)
	return nil
}

// projectPath 返回项目在磁盘上的根目录；项目不存在时返回空串。
func (b *MemoryBackend) projectPath(projectID string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if proj, ok := b.projects[projectID]; ok {
		return proj.Path
	}
	return ""
}

// nowledgeMessages 把 store 消息过滤成纯对话：只保留用户与助手的正文文本，
// 跳过工具调用、思考、错误横幅、系统通知等非对话内容，正文为空的消息同样跳过。
func nowledgeMessages(msgs []store.Message) []nmem.Message {
	out := make([]nmem.Message, 0, len(msgs))
	for _, m := range msgs {
		role, ok := nowledgeMessageRole(m)
		if !ok {
			continue
		}
		text := strings.TrimSpace(m.Text)
		if text == "" {
			continue
		}
		out = append(out, nmem.Message{Role: role, Content: text, Timestamp: m.CreatedAt})
	}
	return out
}

// nowledgeMessageRole 返回消息在 Nowledge 对话中的角色；不属于对话正文时 ok 为 false。
// 判断依据是 Message 的 Role 与 Kind：user 只接受正文（空/ text / user），assistant 只接受
// 正文（空 / assistant / text），其余 kind（thinking、tool、error、info）与 system 角色一律跳过；
// 编排派发与子代理回报走的是 user 角色的系统消息，按 swarm 前缀剔除。
func nowledgeMessageRole(m store.Message) (string, bool) {
	kind := strings.ToLower(strings.TrimSpace(m.Kind))
	switch strings.ToLower(strings.TrimSpace(m.Role)) {
	case "user":
		if swarm.IsNotice(m.Text) || swarm.IsBeePrompt(m.Text) {
			return "", false
		}
		switch kind {
		case "", "text", "user":
			return "user", true
		}
	case "assistant":
		switch kind {
		case "", "assistant", "text":
			return "assistant", true
		}
	}
	return "", false
}

// nowledgeThreadMetadata 组装导入时携带的元数据：项目路径、会话 ID、provider、model，
// 能拿到编排角色信息（子会话的父会话与工作模式）时一并带上。
func nowledgeThreadMetadata(thread store.Thread, workDir string) map[string]any {
	meta := map[string]any{
		"project_path": workDir,
		"thread_id":    thread.ID,
		"provider":     thread.Provider,
		"model":        thread.Model,
	}
	if thread.ParentThreadID != "" {
		meta["parent_thread_id"] = thread.ParentThreadID
		meta["is_helper"] = thread.IsHelper
	}
	if mode := strings.TrimSpace(thread.SwarmMode); mode != "" {
		meta["swarm_mode"] = mode
	}
	if provider := strings.TrimSpace(thread.BeesProvider); provider != "" {
		meta["bees_provider"] = provider
	}
	if model := strings.TrimSpace(thread.BeesModel); model != "" {
		meta["bees_model"] = model
	}
	return meta
}
