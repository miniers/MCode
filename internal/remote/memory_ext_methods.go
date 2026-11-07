package remote

import (
	"context"
	"encoding/json"
	"strings"

	"mcode/internal/remote/wire"
)

// 本文件把桌面端的记忆面板开放给远程客户端：
//   - memory.proposals.list：按项目与状态列出提议；
//   - memory.proposals.accept：接受一条待审提议并写入 Nowledge Mem，返回更新后的提议，
//     可选带上编辑字段（先按编辑后的内容更新提议，再接受）；
//   - memory.proposals.reject：驳回一条待审提议，只改本地状态；
//   - memory.create：新建一条待审提议（等价于桌面端「新建记忆」）；
//   - memory.search：搜索记忆（等价于桌面端记忆面板的搜索页）。
//
// 桌面端同源逻辑见 internal/app/client.go 的 MemoryProposals / AcceptMemoryProposal /
// RejectMemoryProposal / CreateMemoryProposal / SearchMemories，面板见
// internal/ui/memory_panel.go 与 internal/ui/memory_panel_views.go。

// memoryExtService 是记忆面板服务，由 app 层注入（见 internal/app/remote_memory.go）。
// 未注入时这些 RPC 统一返回 MethodNotFound，前端据此隐藏「记忆」入口。
type memoryExtService interface {
	// ListProposals 按项目路径与状态列出提议；两个参数为空都表示不过滤。
	ListProposals(ctx context.Context, projectPath, status string) ([]wire.MemoryExtProposal, error)
	// AcceptProposal 接受一条待审提议并返回更新后的提议（状态改为 accepted）。
	// edit 非 nil 时先按编辑后的内容更新提议，再接受。
	AcceptProposal(ctx context.Context, id string, edit *wire.MemoryExtProposalEdit) (wire.MemoryExtProposal, error)
	// RejectProposal 驳回一条待审提议。
	RejectProposal(ctx context.Context, id string) error
	// CreateMemory 新建一条待审提议并返回它。
	CreateMemory(ctx context.Context, in wire.MemoryExtCreateInput) (wire.MemoryExtProposal, error)
	// SearchMemories 搜索记忆，最多返回 limit 条。
	SearchMemories(ctx context.Context, projectPath, query string, limit int) ([]wire.MemoryExtMemory, error)
}

const (
	// memoryExtServiceName 是记忆提议服务在服务注册表里的名字。
	memoryExtServiceName = "memory"
	// memoryExtChangedMethod 是提议状态变化后的广播方法名。
	memoryExtChangedMethod = "memory.proposals.changed"
)

// 提议状态取值，与 store 包的 MemoryProposalStatus* 常量一致。
const (
	memoryExtStatusPending  = "pending"
	memoryExtStatusAccepted = "accepted"
	memoryExtStatusRejected = "rejected"
)

// 记忆类型与搜索条数：默认类型取桌面端「新建记忆」对话框的默认值（memoryDefaultUnitType），
// 搜索默认 20 条（桌面端 SearchMemories 的口径），上限 50 条。
const (
	memoryExtDefaultUnitType    = "fact"
	memoryExtDefaultSearchLimit = 20
	memoryExtMaxSearchLimit     = 50
)

// 写入范围取值，与 store 包的 MemoryProposalScope* 常量一致。
const (
	memoryExtScopeProject = "project"
	memoryExtScopeGlobal  = "global"
)

func init() {
	RegisterMethod("memory.proposals.list", handleMemoryProposalsList)
	RegisterMethod("memory.proposals.accept", handleMemoryProposalsAccept)
	RegisterMethod("memory.proposals.reject", handleMemoryProposalsReject)
	RegisterMethod("memory.create", handleMemoryCreate)
	RegisterMethod("memory.search", handleMemorySearch)
}

// handleMemoryProposalsList 按项目与状态列出记忆提议。
func handleMemoryProposalsList(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.MemoryExtListParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	status := strings.TrimSpace(p.Status)
	if !memoryExtValidStatus(status) {
		return nil, ExtInvalidParams("Invalid params: 'status' must be pending, accepted or rejected")
	}
	svc, ok := backendService[memoryExtService](d.backend, memoryExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	projectPath, err := memoryExtProjectPath(ctx, d, p.ProjectID)
	if err != nil {
		return nil, err
	}
	// Nowledge 未配置或 store 不可用的原始错误直接回给前端，由它显示「暂不可用」的原因。
	proposals, err := svc.ListProposals(ctx, projectPath, status)
	if err != nil {
		return nil, err
	}
	if proposals == nil {
		// 统一输出空数组，别让客户端处理 null。
		proposals = []wire.MemoryExtProposal{}
	}
	return wire.MemoryExtListResult{Proposals: proposals}, nil
}

// handleMemoryProposalsAccept 接受一条待审提议并广播状态变化。
// 可选的编辑字段（title/content/type/importance）会先写入提议，再执行接受。
func handleMemoryProposalsAccept(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.MemoryExtAcceptParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(p.ID)
	if id == "" {
		return nil, ExtInvalidParams("Invalid params: 'id' is required")
	}
	edit, err := memoryExtAcceptEdit(p)
	if err != nil {
		return nil, err
	}
	svc, ok := backendService[memoryExtService](d.backend, memoryExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	proposal, err := svc.AcceptProposal(ctx, id, edit)
	if err != nil {
		return nil, err
	}
	d.NotifyAuthed(memoryExtChangedMethod, wire.MemoryExtChangedNotification{
		ID:     proposal.ID,
		Status: proposal.Status,
	})
	return proposal, nil
}

// handleMemoryProposalsReject 驳回一条待审提议并广播状态变化。
func handleMemoryProposalsReject(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.MemoryExtIDParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(p.ID)
	if id == "" {
		return nil, ExtInvalidParams("Invalid params: 'id' is required")
	}
	svc, ok := backendService[memoryExtService](d.backend, memoryExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	if err := svc.RejectProposal(ctx, id); err != nil {
		return nil, err
	}
	d.NotifyAuthed(memoryExtChangedMethod, wire.MemoryExtChangedNotification{
		ID:     id,
		Status: memoryExtStatusRejected,
	})
	return wire.MemoryExtOKResult{OK: true}, nil
}

// memoryExtValidStatus 判断状态筛选值是否合法；空串表示不过滤。
func memoryExtValidStatus(status string) bool {
	switch status {
	case "", memoryExtStatusPending, memoryExtStatusAccepted, memoryExtStatusRejected:
		return true
	}
	return false
}

// memoryExtProjectPath 把项目 ID 换算成收件箱使用的项目路径。
// 项目 ID 为空表示全部项目，直接返回空路径；找不到对应项目时返回参数错误，
// 避免「未知项目」被当成全部项目而列出别的项目的提议。
func memoryExtProjectPath(ctx context.Context, d *Dispatcher, projectID string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "", nil
	}
	if d.backend == nil {
		return "", ExtNotImplemented()
	}
	projects, err := d.backend.ListProjects(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range projects.Projects {
		if p.ID != projectID {
			continue
		}
		path := strings.TrimSpace(p.Path)
		if path == "" {
			// 没有工作目录的项目在收件箱里没有键，按未知项目处理，避免列出全部项目的提议。
			return "", ExtInvalidParams("Invalid params: unknown projectID")
		}
		return path, nil
	}
	return "", ExtInvalidParams("Invalid params: unknown projectID")
}

// handleMemoryCreate 新建一条待审记忆提议并广播收件箱变化。
// 与桌面端「新建记忆」一致：先落在本地收件箱等用户确认，不直接写入 Nowledge Mem。
func handleMemoryCreate(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.MemoryExtCreateParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(p.ProjectID)
	if projectID == "" {
		return nil, ExtInvalidParams("Invalid params: 'projectID' is required")
	}
	title := strings.TrimSpace(p.Title)
	if title == "" {
		return nil, ExtInvalidParams("Invalid params: 'title' is required")
	}
	threadID := strings.TrimSpace(p.ThreadID)
	if threadID != "" && !isValidUUID(threadID) {
		return nil, ExtInvalidParams("Invalid params: 'threadID' must be a valid UUID")
	}
	if p.Importance != nil && (*p.Importance < 0 || *p.Importance > 1) {
		return nil, ExtInvalidParams("Invalid params: 'importance' must be between 0 and 1")
	}
	scope := strings.TrimSpace(p.Scope)
	if scope != "" && scope != memoryExtScopeProject && scope != memoryExtScopeGlobal {
		return nil, ExtInvalidParams("Invalid params: 'scope' must be project or global")
	}
	unitType := strings.TrimSpace(p.Type)
	if unitType == "" {
		unitType = memoryExtDefaultUnitType
	}

	svc, ok := backendService[memoryExtService](d.backend, memoryExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	projectPath, err := memoryExtProjectPath(ctx, d, projectID)
	if err != nil {
		return nil, err
	}

	proposal, err := svc.CreateMemory(ctx, wire.MemoryExtCreateInput{
		ProjectPath: projectPath,
		ThreadID:    threadID,
		Title:       title,
		Content:     p.Content,
		Type:        unitType,
		Importance:  p.Importance,
		Scope:       scope,
	})
	if err != nil {
		return nil, err
	}
	d.NotifyAuthed(memoryExtChangedMethod, wire.MemoryExtChangedNotification{
		ID:     proposal.ID,
		Status: proposal.Status,
	})
	return proposal, nil
}

// handleMemorySearch 搜索记忆，返回结果条数不超过 limit（默认 20，上限 50）。
func handleMemorySearch(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.MemoryExtSearchParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	query := strings.TrimSpace(p.Query)
	if query == "" {
		return nil, ExtInvalidParams("Invalid params: 'query' is required")
	}
	limit := memoryExtDefaultSearchLimit
	if p.Limit != nil {
		if *p.Limit <= 0 || *p.Limit > memoryExtMaxSearchLimit {
			return nil, ExtInvalidParams("Invalid params: 'limit' must be between 1 and 50")
		}
		limit = *p.Limit
	}

	svc, ok := backendService[memoryExtService](d.backend, memoryExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	projectPath, err := memoryExtProjectPath(ctx, d, p.ProjectID)
	if err != nil {
		return nil, err
	}

	results, err := svc.SearchMemories(ctx, projectPath, query, limit)
	if err != nil {
		return nil, err
	}
	// 服务层已按 limit 截断，这里兜底一次，保证响应条数不超过上限。
	if len(results) > limit {
		results = results[:limit]
	}
	if results == nil {
		// 统一输出空数组，别让客户端处理 null。
		results = []wire.MemoryExtMemory{}
	}
	return wire.MemoryExtSearchResult{Results: results}, nil
}

// memoryExtAcceptEdit 校验并整理接受前要写入的编辑字段；没有任何编辑字段时返回 nil（按原样接受）。
func memoryExtAcceptEdit(p wire.MemoryExtAcceptParams) (*wire.MemoryExtProposalEdit, error) {
	edit := &wire.MemoryExtProposalEdit{
		Title:      p.Title,
		Content:    p.Content,
		Type:       p.Type,
		Importance: p.Importance,
	}
	if edit.Empty() {
		return nil, nil
	}
	if edit.Title != nil {
		title := strings.TrimSpace(*edit.Title)
		if title == "" {
			return nil, ExtInvalidParams("Invalid params: 'title' must not be empty")
		}
		edit.Title = &title
	}
	if edit.Type != nil {
		unitType := strings.TrimSpace(*edit.Type)
		if unitType == "" {
			return nil, ExtInvalidParams("Invalid params: 'type' must not be empty")
		}
		edit.Type = &unitType
	}
	if edit.Importance != nil && (*edit.Importance < 0 || *edit.Importance > 1) {
		return nil, ExtInvalidParams("Invalid params: 'importance' must be between 0 and 1")
	}
	return edit, nil
}
