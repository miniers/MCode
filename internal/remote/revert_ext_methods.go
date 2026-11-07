package remote

import (
	"context"
	"encoding/json"
	"errors"

	"mcode/internal/git"
	"mcode/internal/remote/wire"
)

// 撤销轮次的远程实现：把桌面端「撤销这一轮改动」的两个步骤开放给 Web 端。
//   - threads.revert.preview 只算差异、不动工作区，用于弹确认框；
//   - threads.revert.apply 真正还原文件并回滚该轮及其之后的历史。
//
// 两者都落在 MemoryBackend 上（见 memory_backend.go 的 PreviewTurnRevert /
// ExecuteTurnRevert），这里用接口断言取用；后端不具备该能力时统一返回 MethodNotFound，
// 前端据此隐藏撤销入口。业务拒绝（例如会话正在运行）按 operationRejected 回包，
// 保留后端原始文案，避免统一脱敏后前端只剩一条笼统提示。

// revertExtBackend 是撤销轮次需要的 MemoryBackend 能力。
type revertExtBackend interface {
	PreviewTurnRevert(ctx context.Context, threadID, turnID string) (*git.RestorePreview, error)
	ExecuteTurnRevert(ctx context.Context, threadID, turnID string) (*git.RestorePreview, error)
}

func init() {
	RegisterMethod("threads.revert.preview", handleThreadsRevertPreview)
	RegisterMethod("threads.revert.apply", handleThreadsRevertApply)
}

// revertExtBackendFrom 取出后端的撤销能力，未实现时返回 false。
func revertExtBackendFrom(d *Dispatcher) (revertExtBackend, bool) {
	if d == nil || d.backend == nil {
		return nil, false
	}
	svc, ok := d.backend.(revertExtBackend)
	return svc, ok
}

// revertExtParams 严格解码参数并校验 threadID / turnID 均为合法 UUID。
func revertExtParams(params json.RawMessage) (wire.ThreadsRevertParams, error) {
	var p wire.ThreadsRevertParams
	if err := decodeExtParams(params, &p); err != nil {
		return p, err
	}
	if !isValidUUID(p.ThreadID) {
		return p, ExtInvalidParams("Invalid params: 'threadID' must be a valid UUID")
	}
	if !isValidUUID(p.TurnID) {
		return p, ExtInvalidParams("Invalid params: 'turnID' must be a valid UUID")
	}
	return p, nil
}

// handleThreadsRevertPreview 实现 threads.revert.preview：返回该轮及其之后所有轮次的改动清单。
func handleThreadsRevertPreview(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	p, err := revertExtParams(params)
	if err != nil {
		return nil, err
	}
	backend, ok := revertExtBackendFrom(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	preview, perr := backend.PreviewTurnRevert(ctx, p.ThreadID, p.TurnID)
	if perr != nil {
		return nil, revertExtWrapError(perr)
	}
	return revertExtPreviewResult(p.ThreadID, p.TurnID, preview), nil
}

// handleThreadsRevertApply 实现 threads.revert.apply：执行撤销并返回实际执行的预览。
func handleThreadsRevertApply(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	p, err := revertExtParams(params)
	if err != nil {
		return nil, err
	}
	backend, ok := revertExtBackendFrom(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	preview, perr := backend.ExecuteTurnRevert(ctx, p.ThreadID, p.TurnID)
	if perr != nil {
		return nil, revertExtWrapError(perr)
	}
	return wire.ThreadsRevertApplyResult{
		ThreadsRevertPreviewResult: revertExtPreviewResult(p.ThreadID, p.TurnID, preview),
		OK:                         true,
	}, nil
}

// revertExtPreviewResult 把 git.RestorePreview 转成协议层结构，并算好三类计数。
// Files 与 Kept 始终为非 nil 切片，前端可以直接渲染空列表。
func revertExtPreviewResult(threadID, turnID string, preview *git.RestorePreview) wire.ThreadsRevertPreviewResult {
	res := wire.ThreadsRevertPreviewResult{
		ThreadID: threadID,
		TurnID:   turnID,
		Kept:     []string{},
		Files:    []wire.ThreadsRevertFile{},
	}
	if preview == nil {
		return res
	}
	res.Base = preview.Base
	res.Finish = preview.Finish
	res.Files = make([]wire.ThreadsRevertFile, 0, len(preview.Files))
	for _, file := range preview.Files {
		restorable := !file.IsKept
		if file.IsKept {
			res.Kept = append(res.Kept, file.Path)
			res.KeptCount++
		} else {
			res.Restorable++
		}
		res.Files = append(res.Files, wire.ThreadsRevertFile{
			Path:       file.Path,
			Additions:  file.Additions,
			Deletions:  file.Deletions,
			Kept:       file.IsKept,
			InBase:     file.InBase,
			Restorable: restorable,
		})
	}
	res.Total = len(res.Files)
	return res
}

// revertExtWrapError 把业务错误转成可读的拒绝错误，已是 *ExtError 时原样返回。
func revertExtWrapError(err error) error {
	if err == nil {
		return nil
	}
	var extErr *ExtError
	if errors.As(err, &extErr) {
		return extErr
	}
	return &ExtError{Code: wire.ErrCodeOperationRejected, Message: err.Error()}
}
