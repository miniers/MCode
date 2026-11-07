package app

// 本文件实现内置 MCP 记忆工具（internal/mcp/tools_memory.go）依赖的 Nowledge Mem 能力，
// 以及桌面端接受提议与 memory_propose(user_requested) 共用的写入逻辑。
// 读取只限当前项目空间与全局空间：agent 拿不到别的项目空间里的记忆和对话。

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sort"
	"strings"

	"mcode/internal/config"
	"mcode/internal/memspace"
	"mcode/internal/nmem"
	"mcode/internal/store"
)

// nowledgeTools 把 Nowledge Mem 客户端适配成 mcp.Options 里的记忆回调。
type nowledgeTools struct {
	client  *nmem.Client
	dataDir string
	store   *store.Store
}

// space 解析项目对应的空间 ID：配置里的空间覆盖优先，否则按仓库目录名匹配；都为空时用 Default 空间。
func (n *nowledgeTools) space(ctx context.Context, projectPath string) string {
	spaceOverride := ""
	if c, err := config.Load(n.dataDir); err == nil {
		config.ApplyEnv(&c, os.LookupEnv)
		spaceOverride = c.Memory.SpaceFor(projectPath)
	}
	space := memspace.Resolve(ctx, n.client, projectPath, spaceOverride)
	if space == "" {
		space = nmem.DefaultSpace
	}
	return space
}

func (n *nowledgeTools) search(ctx context.Context, projectPath string, q nmem.SearchQuery) ([]nmem.Memory, error) {
	return searchWithGlobal(ctx, n.client, n.space(ctx, projectPath), q)
}

// get 先在项目空间里找，找不到再到全局空间里找。
func (n *nowledgeTools) get(ctx context.Context, projectPath, id string) (nmem.Memory, error) {
	space := n.space(ctx, projectPath)
	m, err := n.client.GetMemory(ctx, space, id)
	if isNmemStatus(err, http.StatusNotFound) && !strings.EqualFold(space, nmem.GlobalSpace) {
		return n.client.GetMemory(ctx, nmem.GlobalSpace, id)
	}
	return m, err
}

// forget 先在项目空间里确认这条记忆存在，再到全局空间里找，然后忘掉它：
// 服务端支持删除时真删，不支持时改为标记废弃。先查再删是为了不改到别的项目空间的记忆。
func (n *nowledgeTools) forget(ctx context.Context, projectPath, id string) (bool, error) {
	space := n.space(ctx, projectPath)
	if _, err := n.client.GetMemory(ctx, space, id); isNmemStatus(err, http.StatusNotFound) && !strings.EqualFold(space, nmem.GlobalSpace) {
		space = nmem.GlobalSpace
	} else if err != nil {
		return false, err
	}
	return n.client.ForgetMemory(ctx, space, id)
}

func (n *nowledgeTools) searchThreads(ctx context.Context, projectPath, query string, limit int) ([]nmem.ThreadHit, error) {
	return n.client.SearchThreads(ctx, n.space(ctx, projectPath), query, limit)
}

func (n *nowledgeTools) fetchThread(ctx context.Context, projectPath, threadID string, offset, limit int) (nmem.ThreadPage, error) {
	return n.client.FetchThread(ctx, n.space(ctx, projectPath), threadID, offset, limit)
}

// commit 立即写入一条提议（用户在对话里明确要求记住时），返回更新后的提议与写入的空间。
func (n *nowledgeTools) commit(ctx context.Context, proposalID string) (store.MemoryProposal, string, error) {
	if n.store == nil {
		return store.MemoryProposal{}, "", errors.New("记忆收件箱不可用")
	}
	p, err := n.store.GetMemoryProposal(ctx, proposalID)
	if err != nil {
		return store.MemoryProposal{}, "", err
	}
	return commitMemoryProposal(ctx, n.store, n.client, proposalSpace(p, n.space(ctx, p.ProjectPath)), p)
}

// proposalSpace 返回提议要写入的空间：scope 为 global 时写全局空间，否则写项目空间。
func proposalSpace(p store.MemoryProposal, projectSpace string) string {
	if p.Scope == store.MemoryProposalScopeGlobal {
		return nmem.GlobalSpace
	}
	return projectSpace
}

// commitMemoryProposal 把一条待审提议写入 Nowledge Mem：TargetMemoryID 非空时改写该记忆，否则新增。
// 成功时状态置为 accepted 并记下远端 ID；失败时把错误回写到提议上、保持 pending。
// 返回的空间是记忆实际所在的空间（服务端没返回时用 space）。
func commitMemoryProposal(ctx context.Context, st *store.Store, cl *nmem.Client, space string, p store.MemoryProposal) (store.MemoryProposal, string, error) {
	in := nmem.MemoryInput{
		Title:      p.Title,
		Content:    p.Content,
		UnitType:   p.UnitType,
		Importance: p.Importance,
		Labels:     p.Labels,
		Source:     "mcode",
		Metadata: map[string]any{
			"project_path": p.ProjectPath,
			"thread_id":    p.ThreadID,
			"proposal_id":  p.ID,
		},
	}
	writeCtx, cancelWrite := context.WithTimeout(ctx, memoryRequestTimeout)
	var (
		mem    nmem.Memory
		addErr error
	)
	if p.TargetMemoryID != "" {
		mem, addErr = cl.UpdateMemory(writeCtx, p.TargetMemoryID, in)
	} else {
		mem, addErr = cl.AddMemory(writeCtx, space, in)
	}
	cancelWrite()
	if mem.Space != "" {
		space = mem.Space
	}

	updateCtx, cancelUpdate := context.WithTimeout(context.WithoutCancel(ctx), memoryRequestTimeout)
	defer cancelUpdate()
	if addErr != nil {
		p.Error = memoryWriteError(addErr).Error()
		if err := st.UpdateMemoryProposal(updateCtx, p); err != nil {
			return p, space, errors.Join(memoryWriteError(addErr), err)
		}
		return p, space, memoryWriteError(addErr)
	}
	p.Status = store.MemoryProposalStatusAccepted
	p.RemoteMemoryID = mem.ID
	if p.RemoteMemoryID == "" {
		p.RemoteMemoryID = p.TargetMemoryID
	}
	p.Error = ""
	if err := st.UpdateMemoryProposal(updateCtx, p); err != nil {
		return p, space, err
	}
	return p, space, nil
}

// searchWithGlobal 检索项目空间的记忆。项目空间以 shared 模式共享全局空间时，服务端会一并带出全局记忆；
// Default 空间的共享设置不能修改，所以落到 Default 时这里补查一次全局空间，按相似度合并、按 ID 去重。
func searchWithGlobal(ctx context.Context, cl *nmem.Client, space string, q nmem.SearchQuery) ([]nmem.Memory, error) {
	mems, err := cl.Search(ctx, space, q)
	if err != nil || !strings.EqualFold(space, nmem.DefaultSpace) {
		return mems, err
	}
	global, err := cl.Search(ctx, nmem.GlobalSpace, q)
	if err != nil {
		// 全局空间不存在或暂时不可用时，不影响项目空间的检索结果。
		return mems, nil
	}
	seen := make(map[string]bool, len(mems))
	for _, m := range mems {
		seen[m.ID] = true
	}
	for _, m := range global {
		if !seen[m.ID] {
			mems = append(mems, m)
		}
	}
	sort.SliceStable(mems, func(i, j int) bool { return mems[i].Score > mems[j].Score })
	if q.Limit > 0 && len(mems) > q.Limit {
		mems = mems[:q.Limit]
	}
	return mems, nil
}

// isNmemStatus 判断远端是否返回了指定的 HTTP 状态码。
func isNmemStatus(err error, status int) bool {
	var apiErr *nmem.APIError
	return errors.As(err, &apiErr) && apiErr.Status == status
}
