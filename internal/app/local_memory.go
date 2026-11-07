package app

import (
	"context"
	"errors"
	"sync"

	"mcode/internal/config"
	"mcode/internal/localmem"
	"mcode/internal/nmem"
	"mcode/internal/projctx"
	"mcode/internal/store"
)

var memoryBackendMu sync.RWMutex
var memoryBackendMode = config.MemoryBackendNmem

func setMemoryBackend(mode string) {
	memoryBackendMu.Lock()
	defer memoryBackendMu.Unlock()
	memoryBackendMode = mode
}

func getMemoryBackend() string {
	memoryBackendMu.RLock()
	defer memoryBackendMu.RUnlock()
	return memoryBackendMode
}

// resolveMemoryBackend 按配置解析实际使用的记忆后端。auto 只在「确实没有配置 Nowledge Mem」时
// 才退回本地文档：已配置但读取失败（例如配置文件损坏）仍视为 nmem，此时记忆工具按未配置处理，
// 不降级到本地空文档，避免把用户的真实记忆当成空文档写入。
func resolveMemoryBackend(mode string, client *nmem.Client, clientErr error) string {
	switch mode {
	case config.MemoryBackendLocal:
		return config.MemoryBackendLocal
	case config.MemoryBackendNmem:
		return config.MemoryBackendNmem
	default:
		if client != nil {
			return config.MemoryBackendNmem
		}
		if clientErr != nil && !errors.Is(clientErr, nmem.ErrNotConfigured) {
			return config.MemoryBackendNmem
		}
		return config.MemoryBackendLocal
	}
}

// localMemoryWriteError 把写入本地文档的失败翻译成给用户看的说明；原始错误由调用方返回与记录。
func localMemoryWriteError(err error) string {
	switch {
	case errors.Is(err, localmem.ErrNotFound):
		return "目标记忆不存在"
	case errors.Is(err, projctx.ErrConflict):
		return "文档被同时修改，请重试"
	default:
		return "写入本地文档失败：" + err.Error()
	}
}

type localMemoryTools struct {
	docs    *localmem.Docs
	threads localmem.Threads
	store   *store.Store
}

func (l *localMemoryTools) search(ctx context.Context, projectPath string, q nmem.SearchQuery) ([]nmem.Memory, error) {
	return l.docs.Search(ctx, projectPath, q)
}
func (l *localMemoryTools) get(ctx context.Context, projectPath, id string) (nmem.Memory, error) {
	return l.docs.Get(ctx, projectPath, id)
}
func (l *localMemoryTools) forget(ctx context.Context, projectPath, id string) (bool, error) {
	return l.docs.Forget(ctx, projectPath, id)
}
func (l *localMemoryTools) searchThreads(ctx context.Context, projectPath, query string, limit int) ([]nmem.ThreadHit, error) {
	return l.threads.Search(ctx, projectPath, query, limit)
}
func (l *localMemoryTools) fetchThread(ctx context.Context, projectPath, id string, offset, limit int) (nmem.ThreadPage, error) {
	return l.threads.Fetch(ctx, projectPath, id, offset, limit)
}
func (l *localMemoryTools) commit(ctx context.Context, id string) (store.MemoryProposal, string, error) {
	if l.store == nil {
		return store.MemoryProposal{}, "", errors.New("记忆收件箱不可用")
	}
	p, err := l.store.GetMemoryProposal(ctx, id)
	if err != nil {
		return store.MemoryProposal{}, "", err
	}
	p, space, writeErr := l.docs.Commit(ctx, p)
	updateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), memoryRequestTimeout)
	defer cancel()
	if writeErr != nil {
		p.Error = localMemoryWriteError(writeErr)
		if err := l.store.UpdateMemoryProposal(updateCtx, p); err != nil {
			return p, space, errors.Join(writeErr, err)
		}
		return p, space, writeErr
	}
	p.Status = store.MemoryProposalStatusAccepted
	p.Error = ""
	if err := l.store.UpdateMemoryProposal(updateCtx, p); err != nil {
		return p, space, err
	}
	return p, space, nil
}
