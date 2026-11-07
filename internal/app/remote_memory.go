package app

// 本文件把桌面端的记忆面板接到远程扩展方法上（internal/remote/memory_ext_methods.go）：
// memory.proposals.list / accept / reject 与 memory.create / memory.search 只依赖这里注入的 "memory" 服务。
//
// 桌面端实现见 internal/app/client.go 的 MemoryProposals / AcceptMemoryProposal /
// RejectMemoryProposal / CreateMemoryProposal / SearchMemories：它们依赖 memoryStoreConn() 懒建的
// 第二个 store 连接，以及 nmemClient()（Nowledge Mem 客户端），因此必须用 *Client 而不是 App 上的 backend。

import (
	"context"
	"errors"
	"time"

	"mcode/internal/remote/wire"
	mcodeui "mcode/internal/ui"
)

// remoteMemoryExt 把 *Client 的记忆提议收件箱适配为远程协议接口。
type remoteMemoryExt struct {
	client *Client
}

func init() {
	registerRemoteWiring(func(a *App) {
		if a == nil || a.backend == nil {
			return
		}
		client := a.remoteClient()
		if client == nil {
			return
		}
		a.backend.SetService("memory", remoteMemoryExt{client: client})
	})
}

// ListProposals 列出指定项目与状态的记忆提议（按创建时间倒序）。
// projectPath 为空表示全部项目，status 为空表示不过滤。
func (x remoteMemoryExt) ListProposals(ctx context.Context, projectPath, status string) ([]wire.MemoryExtProposal, error) {
	if x.client == nil {
		return nil, errors.New("记忆服务不可用")
	}
	// store 不可用（含 Nowledge 未配置）等错误原样上抛，由 RPC 层回给前端显示原因。
	items, err := x.client.MemoryProposals(projectPath, status)
	if err != nil {
		return nil, err
	}
	out := make([]wire.MemoryExtProposal, 0, len(items))
	for _, p := range items {
		out = append(out, memoryExtProposalToWire(p))
	}
	return out, nil
}

// AcceptProposal 接受一条待审提议并写入 Nowledge Mem，返回更新后的提议。
// edit 非 nil 时先按编辑后的内容保存（等价于桌面端先保存编辑），再执行接受。
// 写入失败时 Client 会把错误回写到提议上并保持 pending，这里把原始错误回给前端。
func (x remoteMemoryExt) AcceptProposal(ctx context.Context, id string, edit *wire.MemoryExtProposalEdit) (wire.MemoryExtProposal, error) {
	if x.client == nil {
		return wire.MemoryExtProposal{}, errors.New("记忆服务不可用")
	}
	if !edit.Empty() {
		if err := x.applyProposalEdits(id, edit); err != nil {
			return wire.MemoryExtProposal{}, err
		}
	}
	p, err := x.client.AcceptMemoryProposal(id)
	if err != nil {
		return wire.MemoryExtProposal{}, err
	}
	return memoryExtProposalToWire(p), nil
}

// applyProposalEdits 把编辑字段写入待审提议；nil 字段保持原值。
// 提议不在待审状态时也照常写入，状态校验留给接受步骤（与桌面端一致：编辑与接受是两次操作）。
func (x remoteMemoryExt) applyProposalEdits(id string, edit *wire.MemoryExtProposalEdit) error {
	cur, err := x.proposalByID(id)
	if err != nil {
		return err
	}
	updated := cur
	if edit.Title != nil {
		updated.Title = *edit.Title
	}
	if edit.Content != nil {
		updated.Content = *edit.Content
	}
	if edit.Type != nil {
		updated.UnitType = *edit.Type
	}
	if edit.Importance != nil {
		updated.Importance = *edit.Importance
	}
	return x.client.UpdateMemoryProposal(updated)
}

// CreateMemory 新建一条待审提议（等价于桌面端「新建记忆」：先入收件箱，确认后才写入 Nowledge Mem）。
// 桌面端新建对话框只带标题、正文与类型，这里把重要度与写入范围一并补成一次调用；
// 需要补写时调用 UpdateMemoryProposal，写完再读回收件箱里的最新版本作为返回值。
func (x remoteMemoryExt) CreateMemory(ctx context.Context, in wire.MemoryExtCreateInput) (wire.MemoryExtProposal, error) {
	if x.client == nil {
		return wire.MemoryExtProposal{}, errors.New("记忆服务不可用")
	}
	p, err := x.client.CreateMemoryProposal(in.ProjectPath, in.ThreadID, in.Title, in.Content)
	if err != nil {
		return wire.MemoryExtProposal{}, err
	}
	merged := p
	if in.Type != "" {
		merged.UnitType = in.Type
	}
	if in.Importance != nil {
		merged.Importance = *in.Importance
	}
	if in.Scope != "" {
		merged.Scope = in.Scope
	}
	if merged.UnitType == p.UnitType && merged.Importance == p.Importance && merged.Scope == p.Scope {
		return memoryExtProposalToWire(merged), nil
	}
	if err := x.client.UpdateMemoryProposal(merged); err != nil {
		// 补写失败时提议已落在收件箱（带默认值），让用户在面板里补全，不删半成品。
		return wire.MemoryExtProposal{}, err
	}
	if saved, err := x.proposalByID(p.ID); err == nil {
		merged = saved
	}
	return memoryExtProposalToWire(merged), nil
}

// SearchMemories 搜索记忆，返回条数不超过 limit（桌面端 SearchMemories 自身最多返回 20 条）。
func (x remoteMemoryExt) SearchMemories(ctx context.Context, projectPath, query string, limit int) ([]wire.MemoryExtMemory, error) {
	if x.client == nil {
		return nil, errors.New("记忆服务不可用")
	}
	items, err := x.client.SearchMemories(projectPath, query)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	out := make([]wire.MemoryExtMemory, 0, len(items))
	for _, m := range items {
		out = append(out, wire.MemoryExtMemory{
			ID:         m.ID,
			Title:      m.Title,
			Content:    m.Content,
			UnitType:   m.UnitType,
			Importance: m.Importance,
			Labels:     append([]string(nil), m.Labels...),
			CreatedAt:  memoryExtFormatTime(m.CreatedAt),
		})
	}
	return out, nil
}

// proposalByID 在收件箱里按 ID 找一条提议（不限项目与状态）；找不到时与 Client 同文案报错。
func (x remoteMemoryExt) proposalByID(id string) (mcodeui.MemoryProposal, error) {
	items, err := x.client.MemoryProposals("", "")
	if err != nil {
		return mcodeui.MemoryProposal{}, err
	}
	for _, p := range items {
		if p.ID == id {
			return p, nil
		}
	}
	return mcodeui.MemoryProposal{}, errors.New("记忆提议不存在或已被删除")
}

// RejectProposal 驳回一条待审提议：只改本地状态，不碰远端。
func (x remoteMemoryExt) RejectProposal(ctx context.Context, id string) error {
	if x.client == nil {
		return errors.New("记忆服务不可用")
	}
	return x.client.RejectMemoryProposal(id)
}

// memoryExtProposalToWire 把桌面端提议转成协议类型；Labels 复制一份，避免共享底层数组。
func memoryExtProposalToWire(p mcodeui.MemoryProposal) wire.MemoryExtProposal {
	return wire.MemoryExtProposal{
		ID:             p.ID,
		ProjectPath:    p.ProjectPath,
		ThreadID:       p.ThreadID,
		Title:          p.Title,
		Content:        p.Content,
		UnitType:       p.UnitType,
		Importance:     p.Importance,
		Labels:         append([]string(nil), p.Labels...),
		Source:         p.Source,
		Status:         p.Status,
		Scope:          p.Scope,
		RemoteMemoryID: p.RemoteMemoryID,
		Error:          p.Error,
		CreatedAt:      memoryExtFormatTime(p.CreatedAt),
		UpdatedAt:      memoryExtFormatTime(p.UpdatedAt),
	}
}

// memoryExtFormatTime 把时间格式化为 RFC3339（UTC）；零值时间输出空串。
func memoryExtFormatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
