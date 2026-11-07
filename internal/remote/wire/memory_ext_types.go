package wire

// 记忆提议收件箱的远程扩展方法参数与结果类型。
// 这些方法由 internal/remote/memory_ext_methods.go 注册（memory.proposals.list / accept / reject），
// schema 尚未并入 docs/remote-control.schema.json，待 schema 维护者补齐后再按兼容规则演进。
//
// 字段与桌面端 mcodeui.MemoryProposal（internal/ui/client.go）一一对应：
// 提议先落在本地收件箱，用户接受后才写入 Nowledge Mem。

// MemoryExtProposal 是一条记忆提议。
// Status 取 pending / accepted / rejected，Source 取 agent / distill / user。
// CreatedAt、UpdatedAt 为 RFC3339（UTC），零值时间为空串。
type MemoryExtProposal struct {
	ID             string   `json:"id"`
	ProjectPath    string   `json:"projectPath"`
	ThreadID       string   `json:"threadID,omitempty"`
	Title          string   `json:"title"`
	Content        string   `json:"content"`
	UnitType       string   `json:"unitType,omitempty"`
	Importance     float64  `json:"importance"`
	Labels         []string `json:"labels,omitempty"`
	Source         string   `json:"source"`
	Status         string   `json:"status"`
	Scope          string   `json:"scope,omitempty"`
	RemoteMemoryID string   `json:"remoteMemoryID,omitempty"`
	Error          string   `json:"error,omitempty"`
	CreatedAt      string   `json:"createdAt"`
	UpdatedAt      string   `json:"updatedAt"`
}

// MemoryExtListParams 对应 memory.proposals.list 请求参数。
// ProjectID 为空表示全部项目；Status 为空表示不过滤状态。
type MemoryExtListParams struct {
	ProjectID string `json:"projectID,omitempty"`
	Status    string `json:"status,omitempty"`
}

// MemoryExtListResult 对应 memory.proposals.list 成功返回，按创建时间倒序。
type MemoryExtListResult struct {
	Proposals []MemoryExtProposal `json:"proposals"`
}

// MemoryExtIDParams 对应 memory.proposals.accept / memory.proposals.reject 请求参数。
type MemoryExtIDParams struct {
	ID string `json:"id"`
}

// MemoryExtOKResult 对应 memory.proposals.reject 成功返回。
type MemoryExtOKResult struct {
	OK bool `json:"ok"`
}

// MemoryExtChangedNotification 是 memory.proposals.changed 通知的载荷：
// 告诉客户端哪条提议的状态变了，收到后按需重新拉列表。
type MemoryExtChangedNotification struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// MemoryExtCreateParams 对应 memory.create 请求参数。
// 字段与桌面端「新建记忆」对话框一致（标题必填、内容与类型可选），重要度与写入范围是
// 桌面端「新建后编辑」才有的字段，这里作为可选字段一次带上。
type MemoryExtCreateParams struct {
	ProjectID  string   `json:"projectID"`
	ThreadID   string   `json:"threadID,omitempty"`
	Title      string   `json:"title"`
	Content    string   `json:"content,omitempty"`
	Type       string   `json:"type,omitempty"`
	Importance *float64 `json:"importance,omitempty"`
	Scope      string   `json:"scope,omitempty"`
}

// MemoryExtCreateInput 是传给服务层的创建入参：ProjectID 已换算成项目路径。
type MemoryExtCreateInput struct {
	ProjectPath string
	ThreadID    string
	Title       string
	Content     string
	Type        string
	Importance  *float64
	Scope       string
}

// MemoryExtSearchParams 对应 memory.search 请求参数。
// ProjectID 为空表示全部项目；Limit 为空时默认 20 条，上限 50 条。
type MemoryExtSearchParams struct {
	ProjectID string `json:"projectID,omitempty"`
	Query     string `json:"query"`
	Limit     *int   `json:"limit,omitempty"`
}

// MemoryExtMemory 是 memory.search 的一条结果，字段与桌面端搜索页展示的一致。
// CreatedAt 为 RFC3339（UTC），零值时间为空串。
type MemoryExtMemory struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Content    string   `json:"content"`
	UnitType   string   `json:"unitType,omitempty"`
	Importance float64  `json:"importance"`
	Labels     []string `json:"labels,omitempty"`
	CreatedAt  string   `json:"createdAt"`
}

// MemoryExtSearchResult 对应 memory.search 成功返回。
type MemoryExtSearchResult struct {
	Results []MemoryExtMemory `json:"results"`
}

// MemoryExtAcceptParams 对应 memory.proposals.accept 请求参数：除 id 外可选带上编辑字段，
// 提供时先按编辑后的内容更新提议再接受（等价于桌面端「保存编辑，再点接受」）。
type MemoryExtAcceptParams struct {
	ID         string   `json:"id"`
	Title      *string  `json:"title,omitempty"`
	Content    *string  `json:"content,omitempty"`
	Type       *string  `json:"type,omitempty"`
	Importance *float64 `json:"importance,omitempty"`
}

// MemoryExtProposalEdit 是接受前要写入的编辑字段；nil 字段表示不修改。
type MemoryExtProposalEdit struct {
	Title      *string
	Content    *string
	Type       *string
	Importance *float64
}

// Empty 报告是否没有任何编辑字段。
func (e *MemoryExtProposalEdit) Empty() bool {
	if e == nil {
		return true
	}
	return e.Title == nil && e.Content == nil && e.Type == nil && e.Importance == nil
}
