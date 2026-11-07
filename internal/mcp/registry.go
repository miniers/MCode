package mcp

import (
	"context"

	"mcode/internal/store"
)

// ToolContext 包含工具执行所需的上下文与关联项目。
type ToolContext struct {
	Context context.Context
	Project store.Project
	// WorkDir 为调用方会话的工作目录（来自 WorkDirHeader），空串表示未知。
	WorkDir string
	Server  *Server
}

// ToolHandler 定义工具逻辑处理函数。
type ToolHandler func(tc ToolContext, args map[string]any) ToolResult

// 工具能力分组常量。Capability 为空的工具无需能力校验。
const (
	CapKanban     = "kanban"
	CapContext    = "context"
	CapAutomation = "automation"
	CapMemory     = "memory"
)

// capabilityAlwaysEnabled 判断能力分组是否不受 CapabilityEnabled 裁剪。
// 记忆提议只写入本地待审收件箱，不会直接写远端记忆，因此始终可用。
func capabilityAlwaysEnabled(capability string) bool {
	return capability == CapMemory
}

// ToolDefinition 描述单个 MCP 工具的元数据与处理函数。
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	IsWrite     bool           `json:"-"`
	Handler     ToolHandler    `json:"-"`
	// Capability 为工具所属能力分组；空串表示无需能力校验。
	Capability string `json:"-"`
}

// Registry 管理所有已注册的 MCP 工具。
type Registry struct {
	tools map[string]ToolDefinition
	order []string
}

func newRegistry(s *Server) *Registry {
	r := &Registry{
		tools: make(map[string]ToolDefinition),
		order: make([]string, 0),
	}
	if s.opts.KanbanEnabled == nil || s.opts.KanbanEnabled() {
		r.registerKanbanTools(s)
	}
	r.registerContextTools(s)
	r.registerAutomationTools(s)
	r.registerSwarmTools(s)
	r.registerSwarmReportTools(s)
	r.registerMemoryTools(s)
	r.registerStewardTools(s)
	return r
}

// Register 注册一个工具。
func (r *Registry) Register(tool ToolDefinition) {
	if _, exists := r.tools[tool.Name]; !exists {
		r.order = append(r.order, tool.Name)
	}
	r.tools[tool.Name] = tool
}

// Get 查询工具定义。
func (r *Registry) Get(name string) (ToolDefinition, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// ListTools 返回用于 tools/list 响应的工具定义列表。
func (r *Registry) ListTools() []ToolDefinition {
	return r.ListToolsFor(nil)
}

// ListToolsFor 按调用方能力过滤工具；allowed 为 nil 或工具 Capability 为空时一律放行。
func (r *Registry) ListToolsFor(allowed func(capability string) bool) []ToolDefinition {
	list := make([]ToolDefinition, 0, len(r.order))
	for _, name := range r.order {
		if t, ok := r.tools[name]; ok {
			if allowed != nil && t.Capability != "" && !allowed(t.Capability) {
				continue
			}
			list = append(list, t)
		}
	}
	return list
}
