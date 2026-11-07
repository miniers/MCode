package remote

import (
	"context"
	"encoding/json"
	"time"

	"mcode/internal/remote/wire"
)

// UndoThreadsBackend 定义 Dispatcher 所需的单会话撤销后端接口：归档或休眠成功后返回 10 秒内可撤销的令牌。
type UndoThreadsBackend interface {
	UpdateThreadWithUndo(ctx context.Context, params wire.ThreadUpdateParams) (wire.ThreadUpdateResult, string, error)
}

// ProvisionalThreadsBackend 定义 Dispatcher 所需的临时线程保留与释放后端接口。
type ProvisionalThreadsBackend interface {
	RetainThread(ctx context.Context, threadID string) error
	ScheduleProvisionalCleanup(threadID string)
}

// 编译期断言：保证 dispatcher 对上述接口的类型断言能够成立。
var (
	_ UndoThreadsBackend        = (*MemoryBackend)(nil)
	_ ProvisionalThreadsBackend = (*MemoryBackend)(nil)
)

// undoThreads 对 d.backend 进行类型断言，获取 UndoThreadsBackend 接口。
func (d *Dispatcher) undoThreads() (UndoThreadsBackend, bool) {
	if d.backend == nil {
		return nil, false
	}
	ops, ok := d.backend.(UndoThreadsBackend)
	return ops, ok
}

// provisionalThreads 对 d.backend 进行类型断言，获取 ProvisionalThreadsBackend 接口。
func (d *Dispatcher) provisionalThreads() (ProvisionalThreadsBackend, bool) {
	if d.backend == nil {
		return nil, false
	}
	ops, ok := d.backend.(ProvisionalThreadsBackend)
	return ops, ok
}

// UpdateThreadWithUndo 更新会话：当归档或休眠置 true 时，先为受影响会话拍快照，
// 成功后按 BulkArchive 的方式登记撤销操作，返回 10 秒内可用的撤销 token；其余更新直接透传，token 为空串。
func (b *MemoryBackend) UpdateThreadWithUndo(ctx context.Context, params wire.ThreadUpdateParams) (wire.ThreadUpdateResult, string, error) {
	archiving := params.Archived != nil && *params.Archived
	markDormant := params.Dormant != nil && *params.Dormant
	if !archiving && !markDormant {
		res, err := b.UpdateThread(ctx, params)
		return res, "", err
	}

	snapshots, projectID := b.undoSnapshots(params.ThreadID, archiving)

	res, err := b.UpdateThread(ctx, params)
	if err != nil {
		return wire.ThreadUpdateResult{}, "", err
	}
	if len(snapshots) == 0 {
		return res, "", nil
	}

	kind := "thread.dormant"
	if archiving {
		kind = "thread.archive"
	}
	token := generateBulkToken()
	now := b.bulk.getNow()

	b.bulk.mu.Lock()
	if b.bulk.ops == nil {
		b.bulk.ops = make(map[string]bulkOperation)
	}
	b.bulk.cleanupExpiredLocked(now)
	b.bulk.ops[token] = bulkOperation{
		kind:      kind,
		projectID: projectID,
		createdAt: now,
		snapshots: snapshots,
	}
	b.bulk.mu.Unlock()

	return res, token, nil
}

// undoSnapshots 在锁内为归档或休眠的目标会话拍摄撤销快照，返回快照与根会话所属项目 ID。
// 归档级联整个会话家族，休眠只级联 helper 子会话，与 UpdateThread 的级联判定保持一致。
func (b *MemoryBackend) undoSnapshots(rootID string, archiving bool) ([]bulkThreadSnapshot, string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	root, ok := b.threads[rootID]
	if !ok {
		return nil, ""
	}
	familyIDs := b.familyThreadIDsLocked(rootID)
	snapshots := make([]bulkThreadSnapshot, 0, len(familyIDs))
	for _, fid := range familyIDs {
		td, ok := b.threads[fid]
		if !ok {
			continue
		}
		// 休眠级联只作用于 helper 子会话，与 UpdateThread 的判定一致。
		if !archiving && fid != rootID && !td.isHelper {
			continue
		}
		var dormantAt *time.Time
		if td.dormantAt != nil {
			dormantCopy := *td.dormantAt
			dormantAt = &dormantCopy
		}
		snapshots = append(snapshots, bulkThreadSnapshot{
			ThreadID:   fid,
			IsArchived: td.summary.IsArchived,
			IsPinned:   td.summary.IsPinned,
			IsDormant:  td.summary.IsDormant,
			DormantAt:  dormantAt,
			HasUnread:  td.summary.HasUnread,
		})
	}
	return snapshots, root.summary.ProjectID
}

// handleThreadsRetain 处理 threads.retain：把临时会话转为正式会话。
func (d *Dispatcher) handleThreadsRetain(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.ThreadRetainParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadId' must be a UUID string")
	}

	ops, ok := d.provisionalThreads()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "provisional thread operations unavailable")
	}
	if err := ops.RetainThread(ctx, p.ThreadID); err != nil {
		return replyWithBackendErr(err)
	}
	return replySuccess(struct{}{})
}

// handleThreadsProvisionalRelease 处理 threads.provisional.release：调度临时会话的延迟自动清理。
func (d *Dispatcher) handleThreadsProvisionalRelease(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.ThreadProvisionalReleaseParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadId' must be a UUID string")
	}

	ops, ok := d.provisionalThreads()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "provisional thread operations unavailable")
	}
	ops.ScheduleProvisionalCleanup(p.ThreadID)
	return replySuccess(struct{}{})
}
