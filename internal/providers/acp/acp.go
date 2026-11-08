package acp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"mcode/internal/harness"
	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
)

var _ harness.Harness = (*Harness)(nil)
var _ harness.HostToolsReporter = (*Harness)(nil)

// Options 包含 ACP 适配器的配置选项。
type Options struct {
	Agent     Agent
	Binary    string
	ExtraArgs []string
	Env       []string
	// BuiltinMCP/ExternalMCP 决定 session/new 与 session/load 中注入的 MCP 服务，
	// 内置服务按会话路由（BuiltinMCP.Endpoint(turn)）。
	BuiltinMCP  harness.BuiltinMCP
	ExternalMCP func(workDir, threadID string) []harness.MCPEndpoint
}

// launchSpacing 是相邻两次 ACP 进程启动的最小间隔（1.5 秒）：
// 同一瞬间批量启动多个 ACP agent（例如 Swarm 的多个 bee）会在 Agent 自身的本地存储
// （如 OpenCode 的 SQLite）上撞锁，导致某轮对话开局即失败；单个启动不受影响。
var launchSpacing = 1500 * time.Millisecond

var launchGate struct {
	mu   sync.Mutex
	next time.Time
}

// waitLaunchSlot 等待到下一个允许启动的时刻；ctx 取消时立即返回。
func waitLaunchSlot(ctx context.Context) error {
	launchGate.mu.Lock()
	now := time.Now()
	slot := now
	if launchGate.next.After(now) {
		slot = launchGate.next
	}
	launchGate.next = slot.Add(launchSpacing)
	launchGate.mu.Unlock()

	if wait := time.Until(slot); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

// Harness 实现了基于 ACP (Agent Communication Protocol) 的 harness.Harness 接口。
type Harness struct {
	opts Options
	// roots 按会话记录本轮正在运行的 agent 进程 PID，供 ProcessRootPIDs 使用。
	roots harness.ProcessRoots
	// mcpHTTP 记录 initialize 应答里已确认支持 HTTP MCP；确认前按需要 npx 的退化路径判断。
	mcpHTTP atomic.Bool
	// npxOnce/npxOK 缓存 npx 是否在 PATH 中，避免每次会话状态查询都扫一遍 PATH。
	npxOnce sync.Once
	npxOK   bool
}

// acpSessionModes 是 session/new 与 session/load 响应里广告的会话模式（ACP 的 modes 字段）。
// Droid 用它判断计划模式与运行时权限对应的档位。
type acpSessionModes struct {
	CurrentModeID  string `json:"currentModeId"`
	AvailableModes []struct {
		ID string `json:"id"`
	} `json:"availableModes"`
}

// IDs 返回会话广告的可用模式 ID 列表。
func (m acpSessionModes) IDs() []string {
	ids := make([]string, 0, len(m.AvailableModes))
	for _, mode := range m.AvailableModes {
		ids = append(ids, mode.ID)
	}
	return ids
}

// New 创建一个新的 ACP Harness 实例。若 Binary 为空则自动回退到 Agent 的默认可执行文件名。
func New(opts Options) *Harness {
	if opts.Binary == "" {
		opts.Binary = DefaultBinary(opts.Agent)
	}
	return &Harness{opts: opts}
}

// Kind 返回 Agent 后端的标识。
func (h *Harness) Kind() harness.Kind {
	return harness.Kind(h.opts.Agent)
}

// CatalogBinary 返回当前运行时配置的 CLI。
func (h *Harness) CatalogBinary() string { return h.opts.Binary }

// ProcessRootPIDs 返回指定会话当前运行的 agent 进程 PID，未在运行时返回 nil。
func (h *Harness) ProcessRootPIDs(threadID string) []int {
	return h.roots.PIDs(threadID)
}

// ConfirmsApprovalDelivery 返回 true，表示该 Provider 对每次 Approve/Interact 的答复都会调用 DeliveryAcker.AckDelivery。
func (h *Harness) ConfirmsApprovalDelivery() bool {
	return true
}

// HostToolsAvailable 报告会话能否用到 mcode 的 MCP 回执工具：
// 内置 MCP 未配置时没有工具可注入；Agent 声明支持 HTTP MCP 时直连内置端点，
// 否则退化条目由 npx 启动 mcp-remote，找不到 npx 就没有工具可用。
func (h *Harness) HostToolsAvailable() bool {
	if !h.opts.BuiltinMCP.Enabled() {
		return false
	}
	if h.mcpHTTP.Load() {
		return true
	}
	return h.npxAvailable()
}

// npxAvailable 报告 PATH 中是否有 npx；结果按 Harness 缓存一次。
func (h *Harness) npxAvailable() bool {
	h.npxOnce.Do(func() {
		_, err := lookupNpx("npx")
		h.npxOK = err == nil
	})
	return h.npxOK
}

// Available 检查 Agent 的可执行文件是否在登录环境的 PATH 中可用。
// 仅校验可执行文件是否存在，不启动进程、不触发登录；登录环境还在后台加载时先按可用处理，
// 避免首次拉供应商列表时为一次登录 Shell 子进程等待。
func (h *Harness) Available() (bool, string) {
	bin := h.opts.Binary
	if bin == "" {
		bin = DefaultBinary(h.opts.Agent)
	}
	if found, decided := loginenv.ProbePath(bin); found || !decided {
		return true, ""
	}
	return false, fmt.Sprintf("%s (%s) was not found in PATH", DisplayName(h.opts.Agent), bin)
}

// tailBuffer 是一个并发安全的保留末尾至多指定大小字节的缓冲区。
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.limit {
		b.buf = b.buf[len(b.buf)-b.limit:]
	}
	return n, nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

func buildEnv(base []string, workDir string, extraOpts []string, extraTurn []string) []string {
	env := make([]string, 0, len(base)+1+len(extraOpts)+len(extraTurn))
	for _, e := range base {
		if !strings.HasPrefix(e, "PWD=") {
			env = append(env, e)
		}
	}
	if workDir != "" {
		env = append(env, "PWD="+workDir)
	}
	env = append(env, extraOpts...)
	env = append(env, extraTurn...)
	return env
}

// resolveWorkDirPath 将 Agent 请求的相对或绝对路径解析为工作目录（或附加目录）内的绝对路径。
// 越出白名单（含 ".."、指向目录外的绝对路径，以及经符号链接逃出）一律拒绝。
// 相对路径只相对 workDir 解析；extraDirs 是工作目录之外额外放开的绝对目录。
func resolveWorkDirPath(workDir, path string, extraDirs ...string) (string, string, error) {
	root := filepath.Clean(workDir)
	// rootReal 是工作目录的真实路径；按真实路径比较才能拦住经符号链接的目录（如 macOS 的 /var → /private/var）。
	rootReal := root
	if real, err := filepath.EvalSymlinks(root); err == nil {
		rootReal = real
	}
	// roots/rootsReal 是本次放行的词法白名单与真实路径白名单，第一项恒为 workDir。
	roots := []string{root}
	rootsReal := []string{rootReal}
	seenRoot := map[string]bool{root: true}
	for _, dir := range extraDirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		clean := filepath.Clean(dir)
		if seenRoot[clean] {
			continue
		}
		seenRoot[clean] = true
		real := clean
		if resolved, err := filepath.EvalSymlinks(clean); err == nil {
			real = resolved
		}
		roots = append(roots, clean)
		rootsReal = append(rootsReal, real)
	}

	target := path
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	target = filepath.Clean(target)

	if !withinAnyRoot(target, roots) {
		return "", "", fmt.Errorf("access denied: path outside workdir")
	}

	// 词法检查挡不住符号链接，再解析真实路径；目标本身不存在时
	// 逐级上溯到最近的已存在祖先，再把剩余部分拼回去。
	resolved := target
	for probe := target; ; probe = filepath.Dir(probe) {
		real, err := filepath.EvalSymlinks(probe)
		if err == nil {
			suffix, relErr := filepath.Rel(probe, target)
			if relErr != nil {
				return "", "", relErr
			}
			resolved = filepath.Join(real, suffix)
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", "", err
		}
		if parent := filepath.Dir(probe); parent == probe {
			break
		}
	}

	// 真实路径同样按每个白名单目录再比一次，符号链接导出工作目录的路径在这里被拦住。
	for _, candidate := range rootsReal {
		if rel, err := relWithinRoot(candidate, resolved); err == nil {
			return resolved, rel, nil
		}
	}
	return "", "", fmt.Errorf("access denied: path outside workdir")
}

// withinAnyRoot 报告 target 是否落在任意一个 root 内（纯词法判断）。
func withinAnyRoot(target string, roots []string) bool {
	for _, root := range roots {
		if _, err := relWithinRoot(root, target); err == nil {
			return true
		}
	}
	return false
}

// relWithinRoot 返回 target 相对 root 的路径，越界时报错。
func relWithinRoot(root, target string) (string, error) {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("access denied: path outside workdir")
	}
	return rel, nil
}

// mcpRemotePackage 是退化条目要跑的 npx 包，版本固定：不写版本时 npx 每轮都去 npm
// 解析并可能拉到新的 latest，Agent 启动由此受网络与上游发布节奏影响（新版本改参数名或
// 挂掉会让所有不支持 HTTP MCP 的 Agent 一起失效）。升级：npm view mcp-remote version
// 取当前稳定版，改这里的版本号后跑 internal/providers/acp 的测试。
const mcpRemotePackage = "mcp-remote@0.15.2"

// lookupNpx 是 npx 的 PATH 查找函数，测试中可替换。
var lookupNpx = exec.LookPath

// mcpServerEntry 构造 session/new 与 session/load 的单个 `mcpServers` 条目：
// Agent 支持 HTTP MCP 时直连，
// 否则退化为经 `npx mcpRemotePackage` 转发的 stdio 条目。
//
// 退化条目里令牌不进 argv：子进程的 argv 同机其他用户用 ps 就能读到，所以 argv 的
// --header 只写 `${MCODE_MCP_HEADER_<i>}` 这样的变量名，真实值放进条目的 env。
// mcp-remote 启动时会用自身环境变量展开 header 值里的 ${VAR}
// （0.15.2 dist 的 parseCommandLineArgs → substituteEnvVars），取值发生在子进程内。
// 冒号后不留空格是照上游 README 的建议：部分客户端不会转义 args 里的空格，
// 而 env 的值含空格不受影响，整个 "Bearer xxx" 就都塞进变量里。
func mcpServerEntry(name, rawURL string, headers [][2]string, allowsHTTP bool) map[string]any {
	if allowsHTTP {
		list := make([]any, 0, len(headers))
		for _, header := range headers {
			list = append(list, map[string]any{"name": header[0], "value": header[1]})
		}
		return map[string]any{
			"type":    "http",
			"name":    name,
			"url":     rawURL,
			"headers": list,
		}
	}

	args := []any{"-y", mcpRemotePackage, rawURL}
	env := make([]any, 0, len(headers))
	for i, header := range headers {
		// 下标在单个条目内唯一（各条目独立进程，互不影响）。
		key := fmt.Sprintf("MCODE_MCP_HEADER_%d", i)
		args = append(args, "--header", header[0]+":${"+key+"}")
		env = append(env, map[string]any{"name": key, "value": header[1]})
	}
	return map[string]any{
		"name":    name,
		"command": "npx",
		"args":    args,
		"env":     env,
	}
}

// mcpServers 生成本轮注入的 MCP 服务列表：内置服务按线程路由，外部端点一并写入，
// 名为 mcode 或 mcode_approval 的外部端点跳过（与 Claude/Codex 适配器一致）。
func (h *Harness) mcpServers(turn harness.Turn, allowsHTTP bool) []any {
	servers := make([]any, 0, 4)

	if url, token, workdirHeader := h.opts.BuiltinMCP.Endpoint(turn); url != "" {
		var headers [][2]string
		if token != "" {
			headers = append(headers, [2]string{"Authorization", "Bearer " + token})
		}
		if workdirHeader {
			headers = append(headers, [2]string{"X-Mcode-Workdir", turn.WorkDir})
		}
		servers = append(servers, mcpServerEntry("mcode", url, headers, allowsHTTP))
	}

	if h.opts.ExternalMCP != nil {
		for _, ep := range h.opts.ExternalMCP(turn.WorkDir, turn.ThreadID) {
			if ep.Name == "mcode" || ep.Name == "mcode_approval" {
				continue
			}
			var headers [][2]string
			if ep.Token != "" {
				headers = append(headers, [2]string{"Authorization", "Bearer " + ep.Token})
			}
			servers = append(servers, mcpServerEntry(ep.Name, ep.URL, headers, allowsHTTP))
		}
	}

	return servers
}

// Run 执行一轮 ACP 交互。
func (h *Harness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	bin := h.opts.Binary
	if bin == "" {
		bin = DefaultBinary(h.opts.Agent)
	}

	launchArgs := LaunchArgs(h.opts.Agent, turn.Mode)
	allArgs := append(append([]string{}, launchArgs...), h.opts.ExtraArgs...)

	var cmd *exec.Cmd
	var cleanup func()
	var err error

	if turn.Sandbox != nil {
		cmd, cleanup, err = turn.Sandbox.Command(ctx, turn.WorkDir, turn.Mode, bin, allArgs...)
		if err != nil {
			return fmt.Errorf("acp: sandbox command: %w", err)
		}
	} else {
		cmd = procattr.HideWindow(exec.CommandContext(ctx, bin, allArgs...))
	}
	if cleanup != nil {
		defer cleanup()
	}

	setProcessGroup(cmd)
	cmd.WaitDelay = 3 * time.Second
	cmd.Dir = turn.WorkDir

	combinedOptsEnv := append(ExtraEnv(h.opts.Agent), h.opts.Env...)
	cmd.Env = buildEnv(loginenv.Environ(), turn.WorkDir, combinedOptsEnv, turn.Env)

	stderrBuf := &tailBuffer{limit: 8192}
	cmd.Stderr = stderrBuf

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("acp: stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("acp: stdout pipe: %w", err)
	}

	if err := waitLaunchSlot(ctx); err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		tail := strings.TrimSpace(stderrBuf.String())
		if tail != "" {
			return fmt.Errorf("acp start failed: %w (stderr: %s)", err, tail)
		}
		return fmt.Errorf("acp start failed: %w", err)
	}
	// 按会话记录 agent 进程 PID，供「终止运行中的命令」定位命令子进程树。
	agentPID := cmd.Process.Pid
	h.roots.Set(turn.ThreadID, agentPID)
	defer h.roots.Clear(turn.ThreadID, agentPID)

	conn := NewConn(stdoutPipe, stdinPipe)
	defer conn.Close()

	var sessionID string
	var sessionMu sync.Mutex

	setSessionID := func(id string) {
		sessionMu.Lock()
		defer sessionMu.Unlock()
		sessionID = id
	}
	getSessionID := func() string {
		sessionMu.Lock()
		defer sessionMu.Unlock()
		return sessionID
	}

	// 注册对端发来的请求处理器
	conn.SetRequestHandler(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "fs/read_text_file":
			var p struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}
			target, _, err := resolveWorkDirPath(turn.WorkDir, p.Path, turn.AdditionalDirs...)
			if err != nil {
				return nil, err
			}
			content, err := os.ReadFile(target)
			if err != nil {
				return nil, err
			}
			return map[string]any{"content": string(content)}, nil

		case "fs/write_text_file":
			var p struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}
			target, rel, err := resolveWorkDirPath(turn.WorkDir, p.Path, turn.AdditionalDirs...)
			if err != nil {
				return nil, err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(target, []byte(p.Content), 0644); err != nil {
				return nil, err
			}
			_ = harness.Emit(emit, harness.Event{
				Type: harness.EventFileChange,
				Path: rel,
			})
			return map[string]any{}, nil

		case "session/request_permission":
			var p struct {
				ToolCall struct {
					ToolCallID string          `json:"toolCallId"`
					Name       string          `json:"name"`
					ToolName   string          `json:"toolName"`
					Title      string          `json:"title"`
					Kind       string          `json:"kind"`
					RawInput   json.RawMessage `json:"rawInput"`
					Input      json.RawMessage `json:"input"`
				} `json:"toolCall"`
				Options []struct {
					OptionID string `json:"optionId"`
					Name     string `json:"name"`
					Kind     string `json:"kind"`
				} `json:"options"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}

			toolID := p.ToolCall.ToolCallID
			toolName := p.ToolCall.Name
			if toolName == "" {
				toolName = p.ToolCall.ToolName
			}
			if toolName == "" {
				toolName = p.ToolCall.Title
			}
			input := p.ToolCall.Input
			if len(input) == 0 {
				input = p.ToolCall.RawInput
			}
			summary := p.ToolCall.Title
			if summary == "" {
				summary = toolName
			}

			// Droid 的计划退出（Approve Spec / ExitSpecMode）单独走计划类审批。
			optionIDs := make([]string, 0, len(p.Options))
			optionNames := make([]string, 0, len(p.Options))
			for _, opt := range p.Options {
				optionIDs = append(optionIDs, opt.OptionID)
				optionNames = append(optionNames, opt.Name)
			}
			planExitName := p.ToolCall.Name
			if planExitName == "" {
				planExitName = p.ToolCall.ToolName
			}
			planExit := h.opts.Agent == AgentDroid && DroidIsPlanExit(p.ToolCall.Title, planExitName, optionNames, input)

			var decision harness.Decision
			var approveReq harness.ApprovalRequest
			var approveErr error
			if turn.Approver != nil {
				approveReq = harness.ApprovalRequest{
					ToolID:   toolID,
					ToolName: toolName,
					Input:    input,
					Summary:  summary,
				}
				if planExit {
					// 计划正文随计划类请求带给前端（planCompleted 事件）。
					approveReq.Kind = harness.RequestKindPlan
					approveReq.Summary = "执行计划"
					approveReq.Plan = DroidPlanMarkdown(input)
				}
				dec, err := turn.Approver.Approve(ctx, approveReq)
				if err != nil {
					approveErr = err
					harness.AckDelivery(turn.Approver, approveReq, err)
					decision = harness.DecisionDeny
				} else {
					decision = dec
				}
			} else {
				decision = harness.DecisionDeny
				if turn.Mode == harness.ModeFullAccess {
					decision = harness.DecisionAllow
				} else if !planExit && (turn.Mode == harness.ModeAuto || turn.Mode == harness.ModeAutoAcceptEdits) {
					name := toolName
					arg := ""
					var values map[string]any
					_ = json.Unmarshal(input, &values)
					if p.ToolCall.Kind == "execute" || name == "bash" || name == "shell_command" || name == "run_command" {
						command, _ := values["command"].(string)
						name, arg = "bash", command
					} else if p.ToolCall.Kind == "edit" {
						name = "edit_file"
					}
					if harness.Decide(turn.Mode, nil, name, arg, p.ToolCall.Kind == "read") == harness.DecisionAllow {
						decision = harness.DecisionAllow
					}
				}
			}

			var selectedID string
			if planExit {
				// 批准时按用户所选档位选 Droid 的高档自动执行选项（Plan 回合的 Mode 是只读），驳回固定 cancel。
				if decision == harness.DecisionAllow {
					runMode := turn.Mode
					if turn.PlanExitMode != "" {
						runMode = turn.PlanExitMode
					}
					selectedID = DroidPlanExitApprovalID(string(runMode), optionIDs)
				} else {
					selectedID = droidCancelOption
				}
			} else if decision == harness.DecisionAllow {
				for _, opt := range p.Options {
					if opt.Kind == "allow_once" || opt.Kind == "allow_always" ||
						strings.Contains(strings.ToLower(opt.Name), "allow") ||
						strings.Contains(strings.ToLower(opt.Name), "proceed") {
						selectedID = opt.OptionID
						break
					}
				}
				if selectedID == "" && len(p.Options) > 0 {
					selectedID = p.Options[0].OptionID
				}
			} else {
				for _, opt := range p.Options {
					if opt.Kind == "reject_once" || opt.Kind == "reject_always" ||
						strings.Contains(strings.ToLower(opt.Name), "reject") ||
						strings.Contains(strings.ToLower(opt.Name), "cancel") ||
						strings.Contains(strings.ToLower(opt.Name), "deny") {
						selectedID = opt.OptionID
						break
					}
				}
				if selectedID == "" && len(p.Options) > 0 {
					selectedID = p.Options[len(p.Options)-1].OptionID
				}
			}

			payload := map[string]any{
				"outcome": map[string]any{
					"outcome":  "selected",
					"optionId": selectedID,
				},
			}
			if turn.Approver != nil && approveErr == nil {
				return acpApprovalResponse{
					payload: payload,
					onAck: func(writeErr error) {
						harness.AckDelivery(turn.Approver, approveReq, writeErr)
					},
				}, nil
			}
			return payload, nil

		default:
			return nil, errors.New("method not found")
		}
	})

	// 注册通知分发处理器
	conn.SetNotificationHandler(func(method string, params json.RawMessage) {
		if method != "session/update" {
			return
		}
		var p struct {
			Update struct {
				SessionUpdate string `json:"sessionUpdate"`
				Content       struct {
					Text string `json:"text"`
				} `json:"content"`
				ToolCallID string          `json:"toolCallId"`
				Name       string          `json:"name"`
				ToolName   string          `json:"toolName"`
				Title      string          `json:"title"`
				RawInput   json.RawMessage `json:"rawInput"`
				Input      json.RawMessage `json:"input"`
				Status     string          `json:"status"`
				Output     string          `json:"output"`
				Error      any             `json:"error"`
				Diff       string          `json:"diff"`
				Path       string          `json:"path"`
				Patch      string          `json:"patch"`
				Used       int64           `json:"used"`
				Size       int64           `json:"size"`
			} `json:"update"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return
		}
		u := p.Update

		switch u.SessionUpdate {
		case "agent_message_chunk":
			if u.Content.Text != "" {
				_ = harness.Emit(emit, harness.Event{
					Type: harness.EventText,
					Text: u.Content.Text,
				})
			}

		case "agent_thought_chunk":
			if u.Content.Text != "" {
				_ = harness.Emit(emit, harness.Event{
					Type: harness.EventReasoning,
					Text: u.Content.Text,
				})
			}

		case "tool_call":
			toolName := u.Name
			if toolName == "" {
				toolName = u.ToolName
			}
			if toolName == "" {
				toolName = u.Title
			}
			input := u.Input
			if len(input) == 0 {
				input = u.RawInput
			}
			_ = harness.Emit(emit, harness.Event{
				Type:     harness.EventToolStart,
				ToolID:   u.ToolCallID,
				ToolName: toolName,
				Input:    input,
			})

		case "tool_call_update":
			isErr := u.Status == "failed" || u.Status == "error" || u.Error != nil
			_ = harness.Emit(emit, harness.Event{
				Type:    harness.EventToolEnd,
				ToolID:  u.ToolCallID,
				Output:  u.Output,
				IsError: isErr,
			})

		case "usage_update":
			if u.Used > 0 {
				_ = harness.Emit(emit, harness.Event{
					Type: harness.EventUsage,
					Usage: &harness.Usage{
						InputTokens: u.Used,
					},
				})
			}
		}

		// 提取可能存在的文件变更 / diff
		diffPath := u.Path
		diffPatch := u.Patch
		if diffPatch == "" && u.Diff != "" {
			diffPatch = u.Diff
		}
		if diffPath != "" {
			_ = harness.Emit(emit, harness.Event{
				Type:  harness.EventFileChange,
				Path:  diffPath,
				Patch: diffPatch,
			})
		}
	})

	// 监听 ctx 取消，发送 session/cancel 并优雅杀灭进程组
	shutdownCh := make(chan struct{})
	defer close(shutdownCh)

	go func() {
		select {
		case <-shutdownCh:
			return
		case <-ctx.Done():
			// 1. 发送 session/cancel 通知
			if currID := getSessionID(); currID != "" {
				_ = conn.Notify("session/cancel", map[string]any{"sessionId": currID})
			}
			// 2. 向进程组发送 SIGTERM
			_ = killProcessGroup(cmd, syscall.SIGTERM)
			// 3. 超时 3 秒后强制 SIGKILL 兜底
			time.Sleep(3 * time.Second)
			_ = killProcessGroup(cmd, syscall.SIGKILL)
		}
	}()

	// 步骤 1: initialize
	var initResp struct {
		AgentCapabilities struct {
			LoadSession bool `json:"loadSession"`
			// MCPCapabilities 声明 Agent 是否直接支持 HTTP MCP；不支持时退化为 mcp-remote。
			MCPCapabilities struct {
				HTTP bool `json:"http"`
			} `json:"mcpCapabilities"`
		} `json:"agentCapabilities"`
		AuthMethods []struct {
			ID string `json:"id"`
		} `json:"authMethods"`
	}
	initParams := map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs": map[string]any{
				"readTextFile":  true,
				"writeTextFile": true,
			},
			"terminal": false,
		},
		"clientInfo": map[string]any{
			"name":    "mcode",
			"title":   "mcode",
			"version": "1.0.0",
		},
	}
	if err := conn.Call(ctx, "initialize", initParams, &initResp); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		tail := strings.TrimSpace(stderrBuf.String())
		if tail != "" {
			return fmt.Errorf("acp initialize failed: %w (stderr: %s)", err, tail)
		}
		return fmt.Errorf("acp initialize failed: %w", err)
	}

	// Cline 没有推理强度的 ACP 配置项：开会话之前把强度写进它自己的 providers.json。
	if h.opts.Agent == AgentCline && turn.Effort != "" {
		_ = UpdateClineReasoning(turn.Effort)
	}

	// 步骤 2 & 3: session/load 或 session/new
	canLoad := initResp.AgentCapabilities.LoadSession
	authMethods := initResp.AuthMethods
	// 会话级的 MCP 服务列表：Agent 声明支持 HTTP MCP 时直连，否则退化为 npx mcp-remote。
	allowsHTTP := initResp.AgentCapabilities.MCPCapabilities.HTTP
	// 记下能力，供 HostToolsAvailable 判断是否真的需要 npx。
	h.mcpHTTP.Store(allowsHTTP)
	servers := h.mcpServers(turn, allowsHTTP)

	// sessionOptions 缓存会话响应里的 configOptions，用于下发 effort 与快速模式。
	var sessionOptions []map[string]any
	// sessionModes 缓存会话响应里广告的会话模式，用于下发计划模式与运行时权限档位。
	var sessionModes acpSessionModes

	createOrResume := func() (string, error) {
		type sessionSetupResult struct {
			SessionID     string           `json:"sessionId"`
			ConfigOptions []map[string]any `json:"configOptions"`
			Modes         acpSessionModes  `json:"modes"`
		}

		if turn.SessionID != "" {
			if !canLoad {
				// Agent 不支持 session/load 时明确报错，
				// 而不是静默开一个丢失上下文的新会话。
				return "", fmt.Errorf("this ACP agent (%s) cannot resume the original session", DisplayName(h.opts.Agent))
			}
			var res sessionSetupResult
			err := conn.Call(ctx, "session/load", map[string]any{
				"sessionId":  turn.SessionID,
				"cwd":        turn.WorkDir,
				"mcpServers": servers,
			}, &res)
			if err != nil {
				return "", err
			}
			sessionOptions = res.ConfigOptions
			sessionModes = res.Modes
			return turn.SessionID, nil
		}

		var res sessionSetupResult
		err := conn.Call(ctx, "session/new", map[string]any{
			"cwd":        turn.WorkDir,
			"mcpServers": servers,
		}, &res)
		if err != nil {
			return "", err
		}
		sessionOptions = res.ConfigOptions
		sessionModes = res.Modes
		return res.SessionID, nil
	}

	sid, err := createOrResume()
	// 若需要且包含 auth 错误，且非 Droid，尝试执行 authenticate 后重试
	if err != nil && len(authMethods) > 0 && h.opts.Agent != AgentDroid && strings.Contains(strings.ToLower(err.Error()), "auth") {
		_ = conn.Call(ctx, "authenticate", map[string]any{"methodId": authMethods[0].ID}, nil)
		sid, err = createOrResume()
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		tail := strings.TrimSpace(stderrBuf.String())
		if tail != "" {
			return fmt.Errorf("acp session setup failed: %w (stderr: %s)", err, tail)
		}
		return fmt.Errorf("acp session setup failed: %w", err)
	}

	setSessionID(sid)

	// 步骤 4: 发出 harness.EventSession
	_ = harness.Emit(emit, harness.Event{
		Type:      harness.EventSession,
		SessionID: sid,
	})

	// 步骤 5: 设置模型
	// "default" 是模型目录的占位行，交给 Agent 自己的默认模型，不发 set_model。
	if turn.Model != "" && turn.Model != "default" {
		_ = conn.Call(ctx, "session/set_model", map[string]any{
			"sessionId": sid,
			"modelId":   turn.Model,
		}, nil)
	}

	// 步骤 5.5: 计划模式与运行时权限档位
	// Droid 通过会话广告的会话模式切档：
	// 只有目标模式与会话当前模式不同才下发；Agent 未广告目标模式时 set_mode 会失败，忽略即可。
	if h.opts.Agent == AgentDroid {
		wanted := DroidRequestedMode(turn.InteractionMode, string(turn.Mode), sessionModes.IDs())
		if wanted != "" && wanted != sessionModes.CurrentModeID {
			_ = conn.Call(ctx, "session/set_mode", map[string]any{
				"sessionId": sid,
				"modeId":    wanted,
			}, nil)
		}
	}

	// 推理强度与快速模式：
	// 二者都通过会话广告的配置项下发（session/set_config_option）；Agent 未广告对应配置项时不传。
	// effort 与当前值相同则跳过；除 droid 外下发失败忽略。
	effortConfigID, currentEffort, fastConfigID, currentFast := selectACPConfigOptions(sessionOptions)
	if turn.Effort != "" && effortConfigID != "" && turn.Effort != currentEffort {
		err := conn.Call(ctx, "session/set_config_option", map[string]any{
			"sessionId": sid,
			"configId":  effortConfigID,
			"value":     turn.Effort,
		}, nil)
		if err != nil && h.opts.Agent == AgentDroid {
			return fmt.Errorf("acp set effort: %w", err)
		}
	}
	if fastConfigID != "" {
		wanted := "false"
		if turn.FastMode {
			wanted = "true"
		}
		if wanted != currentFast {
			err := conn.Call(ctx, "session/set_config_option", map[string]any{
				"sessionId": sid,
				"configId":  fastConfigID,
				"value":     wanted,
			}, nil)
			if err != nil && h.opts.Agent == AgentDroid {
				return fmt.Errorf("acp set fast mode: %w", err)
			}
		}
	}

	// 步骤 6: session/prompt
	promptItems := []any{
		map[string]any{
			"type": "text",
			"text": turn.Prompt,
		},
	}
	for _, imgPath := range turn.Images {
		// 非图片附件（PDF、docx 等）不当图片下发，其路径已由上层写进 prompt。
		// 图片类型由 harness.ImageMediaType 判定：扩展名优先，扩展名不可靠时按文件内容嗅探。
		mediaType, ok := harness.ImageMediaType(imgPath)
		if !ok {
			log.Printf("acp: skip image %s: unsupported image type %q", imgPath, filepath.Ext(imgPath))
			continue
		}
		data, readErr := os.ReadFile(imgPath)
		if readErr == nil {
			promptItems = append(promptItems, map[string]any{
				"type":     "image",
				"mimeType": mediaType,
				"data":     base64.StdEncoding.EncodeToString(data),
			})
		}
	}

	var promptResult struct {
		StopReason string `json:"stopReason"`
	}
	promptErr := conn.Call(ctx, "session/prompt", map[string]any{
		"sessionId": sid,
		"prompt":    promptItems,
	}, &promptResult)

	if promptErr == nil {
		_ = harness.Emit(emit, harness.Event{
			Type:       harness.EventDone,
			StopReason: promptResult.StopReason,
		})
	}

	_ = conn.Close()

	if ctx.Err() != nil {
		_ = killProcessGroup(cmd, syscall.SIGKILL)
	}

	waitErr := cmd.Wait()

	if ctx.Err() != nil {
		return ctx.Err()
	}
	if promptErr != nil {
		tail := strings.TrimSpace(stderrBuf.String())
		if tail != "" {
			return fmt.Errorf("acp prompt failed: %w (stderr: %s)", promptErr, tail)
		}
		return fmt.Errorf("acp prompt failed: %w", promptErr)
	}
	if waitErr != nil {
		tail := strings.TrimSpace(stderrBuf.String())
		if tail != "" {
			return fmt.Errorf("acp process exited with error: %w (stderr: %s)", waitErr, tail)
		}
		return fmt.Errorf("acp process exited with error: %w", waitErr)
	}

	return nil
}

// selectACPConfigOptions 从会话响应的 configOptions 里找出推理强度与快速模式配置项。
// 规则：
// effort 选项按 category/id（thought_level/effort/reasoning_effort）识别，取值需构成推理强度刻度；
// Fast 选项是 id 为 "fast"、category 为 model_config 的布尔项。
func selectACPConfigOptions(options []map[string]any) (effortConfigID, currentEffort, fastConfigID, currentFast string) {
	for _, option := range options {
		id, _ := option["id"].(string)
		if id == "" {
			continue
		}
		key, _ := option["category"].(string)
		if key == "" {
			key = id
		}
		current, _ := option["currentValue"].(string)
		switch key {
		case "thought_level", "effort", "reasoning_effort":
			if !hasACPEffortScale(option) {
				continue
			}
			effortConfigID, currentEffort = id, current
		case "model_config":
			if id == "fast" {
				fastConfigID, currentFast = id, current
			}
		}
	}
	return
}

// hasACPEffortScale 判断配置项取值（含一层嵌套 options）里是否含推理强度刻度。
// 只有取值构成刻度才算 effort 选项，布尔 thinking 不算。
func hasACPEffortScale(option map[string]any) bool {
	scale := map[string]bool{
		"none": true, "low": true, "medium": true, "high": true,
		"xhigh": true, "max": true, "extra-high": true,
	}
	values, _ := option["options"].([]any)
	for _, v := range values {
		valueMap, ok := v.(map[string]any)
		if !ok {
			continue
		}
		entries := []any{valueMap}
		if nested, ok := valueMap["options"].([]any); ok {
			entries = nested
		}
		for _, entry := range entries {
			entryMap, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			if s, _ := entryMap["value"].(string); scale[strings.ToLower(s)] {
				return true
			}
		}
	}
	return false
}

type acpApprovalResponse struct {
	payload any
	onAck   func(err error)
}

func (a acpApprovalResponse) ResponsePayload() any {
	return a.payload
}

func (a acpApprovalResponse) Ack(err error) {
	if a.onAck != nil {
		a.onAck(err)
	}
}
