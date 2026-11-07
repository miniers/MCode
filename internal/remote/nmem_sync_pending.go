package remote

// 本文件负责 Nowledge 会话同步的「持久待同步集合」：把还需要导入的会话 ID 记在
// app_settings 的 nowledge_sync_pending 键里，上传成功后才清除；进程退出或崩溃后
// 下次启动由 ResumeNowledgeSync 补传。集合只存会话 ID，不保存任何正文。
//
// 并发约定：
//   - nowledgePendingMu 串行化整份集合的读改写，generation 检查与增代记账也在同一临界区内；
//     否则并发的 schedule 会看到过期的 dirtyPersisted 而跳过记账，导致标记丢失；
//   - 加锁顺序固定为 nowledgePendingMu → b.mu，数据库读写期间不持 b.mu；
//   - HTTP 请求期间（importNowledgeThread 的 ImportThread）两把锁都不持。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"mcode/internal/store"
)

const (
	// nowledgeSyncPendingKey 是 app_settings 里保存待同步会话 ID 集合的键。
	nowledgeSyncPendingKey = "nowledge_sync_pending"
	// nowledgePendingOpTimeout 是集合单次读写的上限。这里不直接用 persistCtx：该 context
	// 派生自 b.ctx，Close 取消后能立刻中止在飞的数据库操作，让关闭时不必等满超时。
	nowledgePendingOpTimeout = 5 * time.Second
	// nowledgePendingFinalTimeout 是 Close 前最后一次本地落盘的总预算（整个收尾共用，
	// 不按会话累加），与已取消的 b.ctx 无关。
	nowledgePendingFinalTimeout = time.Second
)

// nowledgePendingStore 取出 store；已进入关停流程或未挂 store 时 ok 为 false。
func (b *MemoryBackend) nowledgePendingStore() (*store.Store, bool) {
	if b.ctx.Err() != nil {
		return nil, false
	}
	b.mu.Lock()
	s := b.store
	b.mu.Unlock()
	return s, s != nil
}

// nowledgePendingCtx 返回集合读写用的有界 context。
func (b *MemoryBackend) nowledgePendingCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(b.ctx, nowledgePendingOpTimeout)
}

// readNowledgePendingLocked 读出持久待同步集合（去重并排序）；调用方必须持有 b.nowledgePendingMu。
func (b *MemoryBackend) readNowledgePendingLocked(ctx context.Context, s *store.Store) ([]string, error) {
	raw, ok, err := s.GetSetting(ctx, nowledgeSyncPendingKey)
	if err != nil {
		return nil, err
	}
	if !ok || strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, fmt.Errorf("解析 Nowledge 待同步集合失败: %w", err)
	}
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// writeNowledgePendingLocked 覆盖写入持久待同步集合；调用方必须持有 b.nowledgePendingMu。
func (b *MemoryBackend) writeNowledgePendingLocked(ctx context.Context, s *store.Store, ids []string) error {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	data, err := json.Marshal(sorted)
	if err != nil {
		return err
	}
	return s.SetSetting(ctx, nowledgeSyncPendingKey, string(data))
}

// persistNowledgePendingLocked 把会话加入持久集合，成功返回 true。
// 失败只记录日志：内存脏状态保留，后续排期或恢复会重试，不会被误标成已持久化。
func (b *MemoryBackend) persistNowledgePendingLocked(threadID string) bool {
	s, ok := b.nowledgePendingStore()
	if !ok {
		return false
	}
	ctx, cancel := b.nowledgePendingCtx()
	defer cancel()
	ids, err := b.readNowledgePendingLocked(ctx, s)
	if err != nil {
		log.Printf("读取 Nowledge 待同步集合失败（会话 %s）: %v", threadID, err)
		return false
	}
	for _, id := range ids {
		if id == threadID {
			return true
		}
	}
	if err := b.writeNowledgePendingLocked(ctx, s, append(ids, threadID)); err != nil {
		log.Printf("写入 Nowledge 待同步集合失败（会话 %s）: %v", threadID, err)
		return false
	}
	return true
}

// dropNowledgePendingLocked 从持久集合移除会话，成功（含本来就不在集合里）返回 true；
// 调用方必须持有 b.nowledgePendingMu。
func (b *MemoryBackend) dropNowledgePendingLocked(threadID string) bool {
	s, ok := b.nowledgePendingStore()
	if !ok {
		return false
	}
	ctx, cancel := b.nowledgePendingCtx()
	defer cancel()
	ids, err := b.readNowledgePendingLocked(ctx, s)
	if err != nil {
		log.Printf("读取 Nowledge 待同步集合失败（会话 %s）: %v", threadID, err)
		return false
	}
	kept := make([]string, 0, len(ids))
	removed := false
	for _, id := range ids {
		if id == threadID {
			removed = true
			continue
		}
		kept = append(kept, id)
	}
	if !removed {
		return true
	}
	if err := b.writeNowledgePendingLocked(ctx, s, kept); err != nil {
		log.Printf("清理 Nowledge 待同步集合失败（会话 %s）: %v", threadID, err)
		return false
	}
	return true
}

// markNowledgePending 记一次同步请求：在 pending 锁内增代、置内存脏标记并写入持久集合，
// 同一会话的多次请求合并成一次写入。返回本次请求的代次，导入成功时据此判断是否又有新工作。
// 调用方不得持有 b.mu。
func (b *MemoryBackend) markNowledgePending(threadID string) uint64 {
	b.nowledgePendingMu.Lock()
	defer b.nowledgePendingMu.Unlock()

	b.mu.Lock()
	state := b.nowledgeSyncStateLocked(threadID)
	state.generation++
	state.dirty = true
	gen := state.generation
	// 已经持久化且仍脏：同一会话的多次排期合并成一次写入。
	persisted := state.dirty && state.dirtyPersisted
	b.mu.Unlock()

	if persisted {
		return gen
	}
	if b.persistNowledgePendingLocked(threadID) {
		b.mu.Lock()
		state.dirtyPersisted = true
		b.mu.Unlock()
	}
	return gen
}

// beginNowledgeImport 在网络导入前确保待同步标记已持久化，并返回本次导入的快照代次。
// 这样即使进程在导入途中退出，下次启动仍能补传。调用方不得持有 b.mu。
func (b *MemoryBackend) beginNowledgeImport(threadID string) uint64 {
	b.nowledgePendingMu.Lock()
	defer b.nowledgePendingMu.Unlock()

	b.mu.Lock()
	state := b.nowledgeSyncStateLocked(threadID)
	state.dirty = true
	gen := state.generation
	persisted := state.dirtyPersisted
	b.mu.Unlock()

	if persisted {
		return gen
	}
	if b.persistNowledgePendingLocked(threadID) {
		b.mu.Lock()
		state.dirtyPersisted = true
		b.mu.Unlock()
	}
	return gen
}

// clearNowledgePending 在确认当前批次无需再传后清标记：只有代次仍与快照一致才清。
// 导入期间到达的新消息或新同步请求已经增代，标记会保留给后续补跑，
// 不会被这一批的旧成功清掉。调用方不得持有 b.mu 或 pending 锁。
func (b *MemoryBackend) clearNowledgePending(threadID string, gen uint64) {
	b.nowledgePendingMu.Lock()
	defer b.nowledgePendingMu.Unlock()

	b.mu.Lock()
	state, ok := b.nowledgeSync[threadID]
	if !ok {
		b.mu.Unlock()
		return
	}
	if state.generation != gen {
		b.mu.Unlock()
		return
	}
	state.dirty = false
	persisted := state.dirtyPersisted
	b.mu.Unlock()

	// 这里仍持有 pending 锁，代次不会在本函数内变化；删除失败时保留 dirtyPersisted，
	// 让后续清理再试一次，不会把“没删掉的集合项”当成已清理。
	if persisted && b.dropNowledgePendingLocked(threadID) {
		b.mu.Lock()
		state.dirtyPersisted = false
		b.mu.Unlock()
	}
}

// forgetNowledgePending 会话已删除时清掉持久标记：正文已不存在，再补传没有意义。
// 调用方不得持有 b.mu 或 pending 锁。
func (b *MemoryBackend) forgetNowledgePending(threadID string) {
	b.nowledgePendingMu.Lock()
	defer b.nowledgePendingMu.Unlock()

	b.mu.Lock()
	if state, ok := b.nowledgeSync[threadID]; ok {
		state.dirty = false
		state.dirtyPersisted = false
	}
	b.mu.Unlock()

	b.dropNowledgePendingLocked(threadID)
}

// resumeNowledgePendingIDs 读出持久待同步集合，供 ResumeNowledgeSync 使用；读失败只记日志，
// 集合原样保留，后续启动或再次调用可以重试。
func (b *MemoryBackend) resumeNowledgePendingIDs() []string {
	b.nowledgePendingMu.Lock()
	defer b.nowledgePendingMu.Unlock()

	s, ok := b.nowledgePendingStore()
	if !ok {
		return nil
	}
	ctx, cancel := b.nowledgePendingCtx()
	defer cancel()
	ids, err := b.readNowledgePendingLocked(ctx, s)
	if err != nil {
		log.Printf("读取 Nowledge 待同步集合失败，稍后重试: %v", err)
		return nil
	}
	return ids
}

// flushNowledgePendingFinal 在 Close 的 wg.Wait 之后、store 关闭之前做一次集合合并落盘：
// 退出取消后才排期（或收尾时才排期）的会话此时 b.ctx 已取消，正常的持久化会跳过数据库，
// 这里补上，否则本地已有消息、重启却没有待同步标记。
//
// 只读写本地 SQLite，不发任何网络；整个收尾共享一个短预算；集合里已有这些 ID 时不重复写入。
// 失败只记日志，不声称已经保证成功。调用方不得持有 b.mu 或 pending 锁。
func (b *MemoryBackend) flushNowledgePendingFinal() {
	b.nowledgePendingMu.Lock()
	defer b.nowledgePendingMu.Unlock()

	b.mu.Lock()
	s := b.store
	var missing []string
	for id, state := range b.nowledgeSync {
		if state.dirty && !state.dirtyPersisted {
			missing = append(missing, id)
		}
	}
	b.mu.Unlock()
	if s == nil || len(missing) == 0 {
		return
	}
	sort.Strings(missing)

	ctx, cancel := context.WithTimeout(context.Background(), nowledgePendingFinalTimeout)
	defer cancel()
	ids, err := b.readNowledgePendingLocked(ctx, s)
	if err != nil {
		log.Printf("退出前读取 Nowledge 待同步集合失败，未落盘 %v: %v", missing, err)
		return
	}
	merged := append([]string(nil), ids...)
	seen := make(map[string]struct{}, len(merged)+len(missing))
	for _, id := range merged {
		seen[id] = struct{}{}
	}
	added := false
	for _, id := range missing {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		merged = append(merged, id)
		added = true
	}
	if added {
		if err := b.writeNowledgePendingLocked(ctx, s, merged); err != nil {
			log.Printf("退出前写入 Nowledge 待同步集合失败，未落盘 %v: %v", missing, err)
			return
		}
	}

	// 数据库写入已结束，再回到 b.mu 标记“已持久化”，避免持锁做 DB I/O。
	b.mu.Lock()
	for _, id := range missing {
		if state, ok := b.nowledgeSync[id]; ok && state.dirty {
			state.dirtyPersisted = true
		}
	}
	b.mu.Unlock()
}
