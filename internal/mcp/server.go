package mcp

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mcode/internal/automation"
	"mcode/internal/kanban"
	"mcode/internal/nmem"
	"mcode/internal/projctx"
	"mcode/internal/store"
)

// ServerName 为对外的 MCP server 名。
const ServerName = "mcode"

// WorkDirHeader 为传递会话工作目录的 HTTP 请求头。
const WorkDirHeader = "X-Mcode-Workdir"

// maxRequestBody 限制单次 JSON-RPC 请求体的大小（32MiB），避免畸形大包耗尽内存。
const maxRequestBody = 32 << 20

// Options 包含内置 MCP 服务器初始化所需的依赖项。
type Options struct {
	Store      *store.Store
	Kanban     *kanban.Service
	Automation *automation.Service
	// WorktreeDir 为看板任务 worktree 根目录；用于把任务会话的工作目录映射回所属项目。
	WorktreeDir string
	// ResolveProjectID 用于通过工作目录解析所属项目 ID 回调。
	ResolveProjectID func(ctx context.Context, workDir string) (projectID string, ok bool)
	// KanbanEnabled 返回是否注册看板 MCP 工具；nil 时视为开启。
	KanbanEnabled func() bool
	// KanbanEntryOrdinary 返回普通会话的 tools/list 是否暴露 `kanban` 入口工具；nil 时视为开启。
	// 开启时普通会话只看到这个单一入口，看板启动的会话（执行、审核、规划）则按角色
	// 看到精简的类型化看板工具集；共享端点（无会话身份）不受影响。
	KanbanEntryOrdinary func() bool
	// CapabilityEnabled 按项目返回某项工具能力是否可用；nil 时视为全部开启。
	// 目前仅作为扩展点接入（app 层暂时留空），tools/list 与 tools/call 都已按它过滤。
	CapabilityEnabled func(project store.Project, capability string) bool
	// MemorySearch 语义检索当前项目记忆空间的长期记忆。
	// nil 表示未接入长期记忆，memory_search 会返回明确的未配置错误。
	MemorySearch func(ctx context.Context, projectPath string, q nmem.SearchQuery) ([]nmem.Memory, error)
	// MemoryGet 按 ID 读取一条记忆，只在当前项目空间与全局空间里找；nil 表示未接入。
	MemoryGet func(ctx context.Context, projectPath, id string) (nmem.Memory, error)
	// MemoryForget 忘掉一条记忆：服务端支持删除时直接删除，不支持时改为标记废弃，
	// 返回 deleted 为 false；nil 表示未接入。
	MemoryForget func(ctx context.Context, projectPath, id string) (deleted bool, err error)
	// ThreadSearch 全文检索当前项目空间里已同步的对话；nil 表示未接入。
	ThreadSearch func(ctx context.Context, projectPath, query string, limit int) ([]nmem.ThreadHit, error)
	// ThreadFetch 分页读取当前项目空间里一条已同步对话的消息；nil 表示未接入。
	ThreadFetch func(ctx context.Context, projectPath, threadID string, offset, limit int) (nmem.ThreadPage, error)
	// MemoryCommit 立即把一条提议写入长期记忆（用户在对话里明确要求记住时），
	// 返回更新后的提议与写入的空间；nil 表示未接入，提议留在收件箱等用户确认。
	MemoryCommit func(ctx context.Context, proposalID string) (store.MemoryProposal, string, error)
	// SwarmBees 返回该主会话的 bee 快照，供 swarm_report_get 的列表模式使用；nil 时列表模式不可用。
	SwarmBees func(leadThreadID string) []SwarmBeeSummary
}

// Server 实现内置 MCP Streamable HTTP 服务器。
type Server struct {
	opts       Options
	tokenStr   string
	serverURL  string
	listener   net.Listener
	httpServer *http.Server
	closeOnce  sync.Once

	autoMu     sync.RWMutex
	automation *automation.Service

	ctxMu       sync.Mutex
	ctxServices map[string]*projctx.Service

	swarmMu sync.RWMutex
	swarm   *SwarmHooks

	idempotency *IdempotencyCache
	registry    *Registry
}

// New 创建 MCP 服务器实例。Token 在 New 时生成，Handler 即可供测试使用。
func New(opts Options) *Server {
	tokenBytes := make([]byte, 32)
	_, _ = rand.Read(tokenBytes)
	token := hex.EncodeToString(tokenBytes)

	s := &Server{
		opts:        opts,
		tokenStr:    token,
		automation:  opts.Automation,
		ctxServices: make(map[string]*projctx.Service),
		idempotency: NewIdempotencyCache(256, 10*time.Minute),
	}
	s.registry = newRegistry(s)
	return s
}

// SetAutomation 并发安全地设置自动化服务（因主应用中 automation 晚于 server 创建）。
func (s *Server) SetAutomation(svc *automation.Service) {
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	s.automation = svc
}

func (s *Server) getAutomation() *automation.Service {
	s.autoMu.RLock()
	defer s.autoMu.RUnlock()
	return s.automation
}

// URL 返回服务器监听地址，未启动时为空。形如 http://127.0.0.1:PORT/mcp。
func (s *Server) URL() string {
	return s.serverURL
}

// Token 返回身份校验 Bearer 令牌。
func (s *Server) Token() string {
	return s.tokenStr
}

// Start 监听 127.0.0.1 随机端口并启动 HTTP 服务。
func (s *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.listener = ln
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		_ = ln.Close()
		return fmt.Errorf("expected *net.TCPAddr, got %T", ln.Addr())
	}
	s.serverURL = fmt.Sprintf("http://127.0.0.1:%d/mcp", tcpAddr.Port)

	s.httpServer = &http.Server{
		Handler: s.Handler(),
	}

	go func() {
		_ = s.httpServer.Serve(ln)
	}()

	if ctx.Done() != nil {
		go func() {
			<-ctx.Done()
			_ = s.Close()
		}()
	}

	return nil
}

// Close 关闭 HTTP 服务与监听器，幂等。
func (s *Server) Close() error {
	var err error
	s.closeOnce.Do(func() {
		if s.httpServer != nil {
			err = s.httpServer.Close()
		} else if s.listener != nil {
			err = s.listener.Close()
		}
	})
	return err
}

// Handler 返回 HTTP 路由处理器，供内部运行或 httptest 测试使用。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", s.handleMCP)
	return mux
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mcp" {
		http.NotFound(w, r)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	auth := r.Header.Get("Authorization")
	expected := "Bearer " + s.tokenStr
	if subtle.ConstantTimeCompare([]byte(auth), []byte(expected)) != 1 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	s.serveRPC(w, r)
}

type threadScopeKey struct{}

// ServeThread 以 threadID 为作用域处理一次 MCP 请求；调用方（Proxy）须已完成方法与鉴权校验。
// 工具所属项目据该会话解析，不再依赖 WorkDirHeader。
func (s *Server) ServeThread(w http.ResponseWriter, r *http.Request, threadID string) {
	s.serveRPC(w, r.WithContext(context.WithValue(r.Context(), threadScopeKey{}, threadID)))
}

// threadRole 描述调用线程在看板中的角色，决定其可执行的看板写操作范围。
type threadRole int

const (
	// threadRoleOrdinary 为普通会话，不受看板写守卫限制。
	threadRoleOrdinary threadRole = iota
	// threadRoleTask 为某任务的执行线程。
	threadRoleTask
	// threadRoleReview 为某任务的评审线程，看板写操作对其只读。
	threadRoleReview
	// threadRolePlan 为某目标的规划线程。
	threadRolePlan
)

// label 返回线程角色的英文说明，用于工具错误信息。
func (r threadRole) label() string {
	switch r {
	case threadRoleTask:
		return "task execution thread"
	case threadRoleReview:
		return "review thread"
	case threadRolePlan:
		return "goal planning thread"
	default:
		return "ordinary thread"
	}
}

// scopeThreadID 返回请求上下文绑定的会话线程 ID；旧的工作目录调用路径下为空串。
func scopeThreadID(ctx context.Context) string {
	threadID, _ := ctx.Value(threadScopeKey{}).(string)
	return strings.TrimSpace(threadID)
}

// threadRole 判定当前调用线程在看板中的角色，ownTaskID 仅对任务执行线程与评审线程返回。
// 没有线程作用域（旧的 X-Mcode-Workdir 路径）时一律视为普通线程，保持原有行为。
func (s *Server) threadRole(ctx context.Context, projectID string) (role threadRole, ownTaskID string) {
	threadID := scopeThreadID(ctx)
	if threadID == "" || s.opts.Store == nil {
		return threadRoleOrdinary, ""
	}

	// 任务线程优先于评审线程：同一线程可能先后承担两种角色时以执行线程为准。
	tasks, err := s.opts.Store.ListTasks(ctx, projectID)
	if err == nil {
		for _, t := range tasks {
			if t.ThreadID != nil && *t.ThreadID == threadID {
				return threadRoleTask, t.ID
			}
		}
		for _, t := range tasks {
			if t.ReviewThreadID != nil && *t.ReviewThreadID == threadID {
				return threadRoleReview, t.ID
			}
		}
	}

	goals, err := s.opts.Store.ListGoals(ctx, projectID)
	if err == nil {
		for _, g := range goals {
			if g.PlanThreadID != nil && *g.PlanThreadID == threadID {
				return threadRolePlan, ""
			}
		}
	}

	return threadRoleOrdinary, ""
}

func (s *Server) serveRPC(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "Request Entity Too Large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	// 没有 id 的通知（如 notifications/initialized）：返回 HTTP 202，响应体为空
	if req.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch req.Method {
	case "initialize":
		sessionBytes := make([]byte, 16)
		if _, err := rand.Read(sessionBytes); err != nil {
			writeRPCError(w, req.ID, -32603, "Could not create MCP session")
			return
		}
		w.Header().Set("Mcp-Session-Id", hex.EncodeToString(sessionBytes))
		var initParams struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &initParams)
		}
		protoVer := initParams.ProtocolVersion
		if protoVer == "" {
			protoVer = "2025-06-18"
		}
		res := map[string]any{
			"protocolVersion": protoVer,
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    ServerName,
				"version": "0.1.0",
			},
		}
		writeRPCResponse(w, req.ID, res, nil)

	case "notifications/initialized", "notifications/cancelled", "notifications/progress", "notifications/roots/list_changed":
		w.WriteHeader(http.StatusAccepted)

	case "ping":
		writeRPCResponse(w, req.ID, map[string]any{}, nil)

	case "tools/list":
		tools := s.registry.ListToolsFor(s.capabilityFilter(r))
		tools = s.filterToolsForThread(r, tools)
		tools = s.filterSwarmTool(r, tools)
		tools = s.filterUnconfiguredMemoryTools(tools)
		writeRPCResponse(w, req.ID, map[string]any{"tools": tools}, nil)

	case "tools/call":
		var callParams struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &callParams); err != nil {
			writeRPCError(w, req.ID, -32602, "Invalid params")
			return
		}
		s.handleToolCall(w, r, req.ID, callParams.Name, callParams.Arguments)

	default:
		writeRPCError(w, req.ID, -32601, "Method not found: "+req.Method)
	}
}

// capabilityFilter 返回本次调用方的能力判定函数；未配置回调或项目无法解析时返回 nil（全部放行，保持原有行为）。
func (s *Server) capabilityFilter(r *http.Request) func(capability string) bool {
	if s.opts.CapabilityEnabled == nil {
		return nil
	}
	project, err := s.resolveProject(r)
	if err != nil {
		return nil
	}
	return func(capability string) bool {
		if capabilityAlwaysEnabled(capability) {
			return true
		}
		return s.opts.CapabilityEnabled(project, capability)
	}
}

func (s *Server) resolveProject(r *http.Request) (store.Project, error) {
	if threadID, _ := r.Context().Value(threadScopeKey{}).(string); threadID != "" && s.opts.Store != nil {
		if th, err := s.opts.Store.GetThread(r.Context(), threadID); err == nil && th.ProjectID != "" {
			if projects, err := s.opts.Store.ListProjects(r.Context()); err == nil {
				for i := range projects {
					if projects[i].ID == th.ProjectID {
						return projects[i], nil
					}
				}
			}
		}
	}
	return s.ProjectForWorkDir(r.Context(), r.Header.Get(WorkDirHeader))
}

// ProjectForWorkDir 将会话目录解析回所属项目，供内置工具和外部代理共用。
func (s *Server) ProjectForWorkDir(ctx context.Context, workDir string) (store.Project, error) {
	if s.opts.Store == nil {
		return store.Project{}, errors.New("无法确定当前项目")
	}
	projects, err := s.opts.Store.ListProjects(ctx)
	if err != nil || len(projects) == 0 {
		return store.Project{}, errors.New("无法确定当前项目")
	}

	workDir = strings.TrimSpace(workDir)
	if workDir != "" {
		cleanWorkDir := filepath.Clean(workDir)

		// 项目可指向仓库子目录，项目内 worktree 需按任务归属回解。
		roots := []string{s.opts.WorktreeDir}
		for dir := filepath.Dir(cleanWorkDir); ; dir = filepath.Dir(dir) {
			if filepath.Base(dir) == "worktrees" && filepath.Base(filepath.Dir(dir)) == ".mcode" {
				roots = append(roots, dir)
				break
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
		for _, root := range roots {
			if root == "" {
				continue
			}
			cleanRoot := filepath.Clean(root)
			if rel, err := filepath.Rel(cleanRoot, cleanWorkDir); err == nil &&
				rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				taskID := rel
				if idx := strings.Index(rel, string(filepath.Separator)); idx != -1 {
					taskID = rel[:idx]
				}
				if taskID != "" {
					if task, err := s.opts.Store.GetTask(ctx, taskID); err == nil && task.ProjectID != nil {
						for i := range projects {
							if projects[i].ID == *task.ProjectID {
								return projects[i], nil
							}
						}
					}
				}
			}
		}
	}

	// 在单项目兜底之前调用 ResolveProjectID
	if s.opts.ResolveProjectID != nil && workDir != "" {
		if projID, ok := s.opts.ResolveProjectID(ctx, workDir); ok && projID != "" {
			for i := range projects {
				if projects[i].ID == projID {
					return projects[i], nil
				}
			}
		}
	}

	if workDir != "" {
		cleanWorkDir := filepath.Clean(workDir)
		var bestMatch *store.Project
		bestLen := -1

		for i := range projects {
			p := projects[i]
			cleanProjPath := filepath.Clean(p.Path)
			if cleanWorkDir == cleanProjPath {
				if len(cleanProjPath) > bestLen {
					bestMatch = &p
					bestLen = len(cleanProjPath)
				}
			} else if strings.HasPrefix(cleanWorkDir, cleanProjPath+string(filepath.Separator)) {
				if len(cleanProjPath) > bestLen {
					bestMatch = &p
					bestLen = len(cleanProjPath)
				}
			}
		}

		if bestMatch != nil {
			return *bestMatch, nil
		}

	}

	// 找不到时若只有一个项目则用它；否则工具返回错误“无法确定当前项目”。
	if workDir == "" && len(projects) == 1 {
		return projects[0], nil
	}

	return store.Project{}, errors.New("无法确定当前项目")
}

func (s *Server) getContextService(projectPath string) (*projctx.Service, error) {
	s.ctxMu.Lock()
	defer s.ctxMu.Unlock()

	cleanPath := filepath.Clean(projectPath)
	if svc, ok := s.ctxServices[cleanPath]; ok {
		return svc, nil
	}
	svc, err := projctx.New(cleanPath)
	if err != nil {
		return nil, err
	}
	s.ctxServices[cleanPath] = svc
	return svc, nil
}

func (s *Server) handleToolCall(w http.ResponseWriter, r *http.Request, reqID *json.RawMessage, rawName string, rawArgs json.RawMessage) {
	// 单个工具 panic 不应中断整条连接：捕获后回写格式化的 internal 错误信封。
	var panicCacheKey string
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		if panicCacheKey != "" {
			// 释放未完成的幂等占位，避免 panic 后同一 request_id 一直判定为进行中。
			s.idempotency.Abandon(panicCacheKey)
		}
		s.writeToolResult(w, reqID, toolError(CodeInternal, fmt.Sprintf("Tool %q panicked: %v", rawName, recovered), true))
	}()

	toolName := rawName
	prefix := "mcp__" + ServerName + "__"
	if strings.HasPrefix(toolName, prefix) {
		toolName = strings.TrimPrefix(toolName, prefix)
	}

	// 入口工具（kanban、automation、memory）在此改写成内部的类型化工具，后续参数校验、
	// 项目校验、幂等与写串行化（按内部工具名判定）全部原样生效。
	if family := entryFamilyOf(toolName); family != nil {
		rewrittenName, rewrittenArgs, entryErr := s.rewriteEntryCall(family, rawArgs)
		if entryErr != nil {
			s.writeToolResult(w, reqID, *entryErr)
			return
		}
		toolName, rawArgs = rewrittenName, rewrittenArgs
	}

	tool, ok := s.registry.Get(toolName)
	if !ok {
		res := ToolResult{
			Text:    fmt.Sprintf("Unknown tool: %s", rawName),
			IsError: true,
		}
		s.writeToolResult(w, reqID, res)
		return
	}

	project, err := s.resolveProject(r)
	if err != nil {
		s.writeToolResult(w, reqID, toolError(CodeNotFound, "无法确定当前项目", false))
		return
	}

	// 能力不可用的工具按未知工具拒绝，避免绕过 tools/list 的裁剪直接调用。
	if tool.Capability != "" && s.opts.CapabilityEnabled != nil &&
		!capabilityAlwaysEnabled(tool.Capability) && !s.opts.CapabilityEnabled(project, tool.Capability) {
		s.writeToolResult(w, reqID, toolError(CodeNotFound, fmt.Sprintf("Unknown tool: %s", rawName), false))
		return
	}

	var args map[string]any
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			s.writeToolResult(w, reqID, toolError(CodeInvalidArguments, "Arguments must be a valid JSON object", false))
			return
		}
	}
	if args == nil {
		args = make(map[string]any)
	}
	if invalid := validateArguments(tool, args); invalid != nil {
		s.writeToolResult(w, reqID, *invalid)
		return
	}

	// project_id 只允许指向当前会话所属项目，避免工具跨项目写入。
	if rawProject, ok := args["project_id"]; ok {
		projectID, isString := rawProject.(string)
		if !isString || strings.TrimSpace(projectID) != project.ID {
			s.writeToolResult(w, reqID, toolError(CodeInvalidArguments, "project_id must match the current conversation project.", false))
			return
		}
	}

	tc := ToolContext{
		Context: r.Context(),
		Project: project,
		WorkDir: strings.TrimSpace(r.Header.Get(WorkDirHeader)),
		Server:  s,
	}

	idempKey := extractIdempotencyKey(args)
	var cacheKey string

	if tool.IsWrite && idempKey != "" {
		cacheKey = s.idempotency.Key(project.ID, toolName, idempKey)
		fingerprint := Fingerprint(args, "request_id", "requestId", "idempotency_key")
		status, cached := s.idempotency.Begin(cacheKey, fingerprint)
		switch status {
		case BeginReplay:
			s.writeToolResult(w, reqID, *cached)
			return
		case BeginConflict:
			s.writeToolResult(w, reqID, toolError(CodeRequestIDConflict, fmt.Sprintf("request_id '%s' was already used with different arguments.", idempKey), false))
			return
		case BeginInFlight:
			s.writeToolResult(w, reqID, toolError(CodeRequestInProgress, fmt.Sprintf("A request with request_id '%s' is still running.", idempKey), true))
			return
		case BeginProceed:
			panicCacheKey = cacheKey
		}
	}

	res := func() ToolResult {
		if tool.IsWrite && strings.HasPrefix(toolName, "kanban_") && s.opts.Kanban != nil {
			// 会话标识来自传输层，不接受工具参数伪造；旧客户端按工作目录分组。
			sessionID := strings.TrimSpace(r.Header.Get("Mcp-Session-Id"))
			if sessionID == "" {
				sessionID = "workdir:" + project.ID + ":" + filepath.Clean(r.Header.Get(WorkDirHeader))
			} else {
				sessionID = "mcp:" + sessionID
			}
			taskID, _ := args["task_id"].(string)
			release, err := s.opts.Kanban.AcquireWrite(sessionID, strings.TrimSpace(taskID), idempKey)
			if err != nil {
				return toolError(CodeConflict, err.Error(), true)
			}
			defer release()
		}
		return tool.Handler(tc, args)
	}()

	if tool.IsWrite && idempKey != "" {
		// partial_failure 表示写操作已生效（例如卡片已创建但未启动），必须缓存结果，
		// 否则同一 request_id 的重试会重复创建资源；其余错误可安全重试。
		if res.IsError && toolErrorCode(res) != CodePartialFailure {
			s.idempotency.Abandon(cacheKey)
		} else {
			s.idempotency.Finish(cacheKey, res)
		}
	}

	s.writeToolResult(w, reqID, res)
}

// 看板启动的会话按角色只看到其职责所需的类型化看板工具。
// 依据：协调员提示词（internal/kanban/coordinator.go 第 99 行）点名 read_board、goal_status、
// read_card、create_draft、update_card、move_card、start_task 与 read_goal；
// 执行线程只需要看板、卡片与进度回写；评审线程只看不写。
var (
	kanbanPlanRoleTools = map[string]bool{
		"kanban_read_board":   true,
		"kanban_goal_status":  true,
		"kanban_read_card":    true,
		"kanban_task_diff":    true,
		"kanban_review_task":  true,
		"kanban_create_draft": true,
		"kanban_update_card":  true,
		"kanban_move_card":    true,
		"kanban_start_task":   true,
		"kanban_read_goal":    true,
	}
	kanbanTaskRoleTools = map[string]bool{
		"kanban_read_board":    true,
		"kanban_read_card":     true,
		"kanban_list_tasks":    true,
		"kanban_update_card":   true,
		"kanban_submit_result": true,
	}
	kanbanReviewRoleTools = map[string]bool{
		"kanban_read_board": true,
		"kanban_read_card":  true,
		"kanban_list_tasks": true,
		"kanban_task_diff":  true,
	}
)

// filterToolsForThread 按会话角色裁剪 tools/list 结果：普通会话只留族入口工具，
// 看板线程只留该角色需要的类型化看板工具。
// 普通会话要看任务 diff、跑自动化或检索记忆时经入口工具的对应 action 调用，因此类型化工具仍全部隐藏。
// 共享端点（无会话作用域）只隐藏入口工具、保留全部类型化工具
// （调用仍可转发），非看板工具（context_*）一律不受影响。
func (s *Server) filterToolsForThread(r *http.Request, tools []ToolDefinition) []ToolDefinition {
	if scopeThreadID(r.Context()) == "" {
		out := make([]ToolDefinition, 0, len(tools))
		for _, tool := range tools {
			if entryFamilyOf(tool.Name) != nil {
				continue
			}
			out = append(out, tool)
		}
		return out
	}

	// 项目解析失败时按普通会话处理：入口工具对所有角色都能转发，不会丢功能。
	role := threadRoleOrdinary
	if project, err := s.resolveProject(r); err == nil {
		role, _ = s.threadRole(r.Context(), project.ID)
	}

	if role == threadRoleOrdinary {
		entryEnabled := s.opts.KanbanEntryOrdinary == nil || s.opts.KanbanEntryOrdinary()
		out := make([]ToolDefinition, 0, len(tools))
		for _, tool := range tools {
			switch {
			case entryFamilyByTool(tool.Name) != nil:
				// 普通会话改用族入口工具，类型化工具全部隐藏。
			case tool.Name == kanbanEntryToolName:
				if entryEnabled {
					out = append(out, tool)
				}
			default:
				out = append(out, tool)
			}
		}
		return out
	}

	allowed := kanbanTaskRoleTools
	switch role {
	case threadRolePlan:
		allowed = kanbanPlanRoleTools
	case threadRoleReview:
		allowed = kanbanReviewRoleTools
	}

	out := make([]ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		if entryFamilyOf(tool.Name) != nil {
			continue
		}
		if strings.HasPrefix(tool.Name, kanbanToolNamePrefix) && !allowed[tool.Name] {
			continue
		}
		out = append(out, tool)
	}
	return out
}

// filterUnconfiguredMemoryTools 在 tools/list 中过滤未配置回调的记忆工具，并为 `memory` 入口
// 重算 enum：未接入的操作不应出现在可选项里。全部操作都未接入时连入口一起隐藏。
// 未接入时 tools/call 的行为不变（仍返回现有错误）。
func (s *Server) filterUnconfiguredMemoryTools(tools []ToolDefinition) []ToolDefinition {
	out := make([]ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		if tool.Name == memoryFamily.entry {
			if len(s.registry.entryActions(memoryFamily, s.memoryToolConfigured)) == 0 {
				continue
			}
			out = append(out, memoryFamily.definition(s.registry, s.memoryToolConfigured))
			continue
		}
		if !s.memoryToolConfigured(tool.Name) {
			continue
		}
		out = append(out, tool)
	}
	return out
}

// memoryToolConfigured 报告某个记忆工具是否已接入回调；非记忆工具一律返回 true。
func (s *Server) memoryToolConfigured(toolName string) bool {
	switch toolName {
	case "memory_search":
		return s.opts.MemorySearch != nil
	case "memory_get":
		return s.opts.MemoryGet != nil
	case "memory_forget":
		return s.opts.MemoryForget != nil
	case "thread_search":
		return s.opts.ThreadSearch != nil
	case "thread_fetch_messages":
		return s.opts.ThreadFetch != nil
	}
	return true
}

func (s *Server) writeToolResult(w http.ResponseWriter, reqID *json.RawMessage, res ToolResult) {
	callResult := map[string]any{
		"content": []map[string]any{
			{
				"type": "text",
				"text": res.Text,
			},
		},
		"isError": res.IsError,
	}
	writeRPCResponse(w, reqID, callResult, nil)
}
