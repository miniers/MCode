package agy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"mcode/internal/harness"
	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
)

var _ harness.Harness = (*Harness)(nil)
var _ harness.HostToolsReporter = (*Harness)(nil)

const DefaultBinary = "agy"

const (
	planModePromptGuard = `[System Instruction: You are in Plan Mode. Formulate and present a comprehensive implementation plan directly in your text response.
CRITICAL:
1. Do NOT call the ask_question tool (interactive prompts are automatically skipped in this environment; include any questions, trade-offs, or options directly in your text response).
2. Do NOT execute file changes, code edits, or modifying terminal commands to implement the plan in this turn. Only read/exploration tools and read-only inspection commands are permitted if needed to inspect the codebase.
3. Stop and finish your turn after presenting the plan so the user can review and approve it. Do NOT attempt to implement or execute the plan until approved.]

`
	questionBridgePromptGuard = `[System Instruction: For clarification, ask_question is connected to the mcode desktop app through a Hook. Use single-choice, multi-choice, or text questions. The Hook returns the actual user response in its reason while blocking the native auto-skip behavior; do not retry merely because the native tool reports it was blocked.]

`
	// visibleOutputPromptGuard 约束模型在工具循环里也必须留下可见正文。
	// agy 的 stream-json 只转发生成正文（agent_response）与工具步骤，模型的思考（thought）
	// 完全不下发（只在 usage 里给 thinking_tokens），所以模型若只在思考里推进、连续静默调工具，
	// 界面上就只剩一串工具卡片。
	visibleOutputPromptGuard = `[System Instruction: Keep your work visible in the transcript. Before or after each tool call, write at least one short sentence of normal reply text describing what you are doing and what you found; never run a long chain of tool calls silently. Always end the turn with a plain-text summary of the result instead of a bare tool call.]

`

	// transientRetryPrompt 用于在遭遇后端临时 400 错误自动续跑时，作为 stdin 的基础用户提示词，
	// 避免模型重复整轮已完成的任务步骤。
	transientRetryPrompt = "上一次请求被模型后端临时拒绝（INVALID_ARGUMENT 400），请从中断处继续完成原任务，不要重复已完成的步骤或已输出的内容。"
	// networkRetryPrompt 用于网络抖动（EOF/连接重置等）后在同一会话内自动续跑时，
	// 作为 stdin 的基础用户提示词，避免模型重复整轮已完成的任务步骤。
	networkRetryPrompt = "上一次请求因网络中断（连接被重置或提前断开）而失败，请从中断处继续完成原任务，不要重复已完成的步骤或已输出的内容。"
)

type Router interface {
	// Route 为本轮选择账号。conversationID 为空表示新会话；exclude 是本轮已因限流换掉的账号 id。
	Route(conversationID, model string, exclude []string) (accountID string, env []string, args []string, err error)
	// Bind 在拿到 agy 的 conversation id 后记录它与账号的绑定。
	Bind(conversationID, accountID string)
	// MarkLimited 报告账号被限流或额度耗尽，resetAt 未知时传零值；返回 false 表示不允许自动换号（strict 模式）。
	MarkLimited(conversationID, accountID string, resetAt time.Time) bool
}

type Options struct {
	Binary string
	Router Router
	// BuiltinMCP 是本轮要注入的内置 MCP 端点（mcode），未配置时为空。
	BuiltinMCP harness.BuiltinMCP
	// ExternalMCP 按工作目录返回用户配置的外部 MCP 端点。
	ExternalMCP func(workDir, threadID string) []harness.MCPEndpoint
}

type Harness struct {
	opts Options
	// roots 按会话记录本轮正在运行的 agent 进程 PID，供 ProcessRootPIDs 使用。
	roots harness.ProcessRoots
	// now 返回当前时间；nil 时用 time.Now，测试可注入假时钟。
	now func() time.Time
}

func New(opts Options) *Harness {
	if opts.Binary == "" {
		opts.Binary = DefaultBinary
	}
	return &Harness{opts: opts}
}

func (h *Harness) Kind() harness.Kind {
	return harness.Kind("agy")
}

// ProcessRootPIDs 返回指定会话当前运行的 agent 进程 PID，未在运行时返回 nil。
func (h *Harness) ProcessRootPIDs(threadID string) []int {
	return h.roots.PIDs(threadID)
}

// ConfirmsApprovalDelivery 返回 true，表示该 Provider 对每次 Approve/Interact 的答复都会调用 DeliveryAcker.AckDelivery。
func (h *Harness) ConfirmsApprovalDelivery() bool {
	return true
}

// HostToolsAvailable 报告会话是否能用到 mcode 的 MCP 回执工具：
// 内置 MCP 未配置时根本没有工具可注入；源 gemini 目录不存在或为空时
// prepareMCPGeminiDir 会放弃注入，同样没有工具。
// 注入判断依赖本轮的 routeArgs 与 turn 环境：多账号隔离时 gemini 目录由 Router 决定，
// 这里只能按已登录账号目录存在处理；单账号时按默认 HOME/.gemini 走同一判断。
func (h *Harness) HostToolsAvailable() bool {
	if !h.opts.BuiltinMCP.Enabled() {
		return false
	}
	if h.opts.Router != nil {
		return true
	}
	return !sessionMCPInjectSkipped(resolveSourceGeminiDir(nil, nil, nil))
}

func resolveBinary(bin string) string {
	if strings.ContainsAny(bin, "/"+string(filepath.Separator)) {
		return bin
	}
	if found, _ := loginenv.ProbePath(bin); found {
		return bin
	}
	if home := loginenv.HomeDirectory(); home != "" {
		candidate := filepath.Join(home, ".local", "bin", bin)
		if found, _ := loginenv.ProbePath(candidate); found {
			return candidate
		}
	}
	return bin
}

// Available 检查 agy 可执行文件是否可用：登录环境还在后台加载时先按可用处理，
// 避免首次拉供应商列表时为一次登录 Shell 子进程等待。
func (h *Harness) Available() (bool, string) {
	bin := h.opts.Binary
	if bin == "" {
		bin = DefaultBinary
	}
	bin = resolveBinary(bin)
	if found, decided := loginenv.ProbePath(bin); found || !decided {
		return true, ""
	}
	return false, fmt.Sprintf("agy (%s) was not found in PATH", bin)
}

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

func parseModelRow(slug string) (baseSlug string, effort string) {
	for _, eff := range []string{"high", "medium", "low"} {
		suffix := "-" + eff
		if strings.HasSuffix(slug, suffix) {
			return strings.TrimSuffix(slug, suffix), eff
		}
	}
	return slug, ""
}

func normalizeModel(model, effort string) (resolvedModel string, resolvedEffort string) {
	if model == "" {
		return "", ""
	}
	baseSlug, suffixEffort := parseModelRow(model)
	if strings.Contains(strings.ToLower(baseSlug), "claude") {
		return baseSlug, ""
	}
	// 显式 effort 优先，其次模型 slug 的旧式后缀，最后按模型取默认档位。
	resolvedEffort = effort
	if resolvedEffort == "" {
		resolvedEffort = suffixEffort
	}
	if resolvedEffort == "" {
		if baseSlug == "gpt-oss-120b" {
			resolvedEffort = "medium"
		} else {
			resolvedEffort = "high"
		}
	}
	if baseSlug == "gemini-3.1-pro" && resolvedEffort == "medium" {
		resolvedEffort = "high"
	} else if baseSlug == "gpt-oss-120b" {
		resolvedEffort = "medium"
	}
	return baseSlug, resolvedEffort
}

func modeArguments(mode harness.PermissionMode) []string {
	switch mode {
	case harness.ModeSupervised:
		// 监督模式：不注入 --mode 与跳过权限，保持 agy 默认审查行为
		// （headless 下未获批准的动作会被软拒绝）。
		return nil
	case harness.ModeReadOnly:
		// 只读模式：映射为 agy 的 plan 模式并附加 --dangerously-skip-permissions。
		// plan 模式下破坏性与修改性操作会被阻止，同时保证只读命令（sed/cat/grep/ls/head 等）
		// 在 headless 环境下不因缺少交互审批而被自动拒绝（对照 claude 的 plan 映射）。
		return []string{"--mode", "plan", "--dangerously-skip-permissions"}
	}
	// auto（含旧值 autoAcceptEdits）/ fullAccess / 空值与未知值：接受编辑并跳过权限询问。
	return []string{"--mode", "accept-edits", "--dangerously-skip-permissions"}
}

// buildEnv 构造子进程环境变量列表：
// 1. 以 base 为基础（沙箱提供了 cmd.Env 时用它，否则用登录 Shell 环境），剔除 PWD=；
// 2. 注入 PWD、本轮 turn.Env 与路由 env，最后追加 agy 自动更新阻断开关；
// 3. 同名变量按“后出现者覆盖”去重，保持首次出现的位置。
func buildEnv(base []string, turnWorkDir string, turnEnv, routeEnv []string) []string {
	var cleanBase []string
	for _, e := range base {
		if !strings.HasPrefix(e, "PWD=") {
			cleanBase = append(cleanBase, e)
		}
	}
	var pwdEnv []string
	if turnWorkDir != "" {
		pwdEnv = []string{"PWD=" + turnWorkDir}
	}
	autoUpdateEnv := []string{"AGY_CLI_DISABLE_AUTO_UPDATE=true"}

	all := make([]string, 0, len(cleanBase)+len(pwdEnv)+len(turnEnv)+len(routeEnv)+1)
	all = append(all, cleanBase...)
	all = append(all, pwdEnv...)
	all = append(all, turnEnv...)
	all = append(all, routeEnv...)
	all = append(all, autoUpdateEnv...)

	order := make([]string, 0, len(all))
	seen := make(map[string]int)
	for _, kv := range all {
		k := kv
		if idx := strings.IndexByte(kv, '='); idx != -1 {
			k = kv[:idx]
		}
		if pos, ok := seen[k]; ok {
			order[pos] = kv
		} else {
			seen[k] = len(order)
			order = append(order, kv)
		}
	}
	return order
}

func buildArgs(turnWorkDir, conversationID, model, effort string, mode harness.PermissionMode, routeArgs []string, additionalDirs []string) []string {
	args := []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--print-timeout", "30m",
	}
	if turnWorkDir != "" {
		args = append(args, "--add-dir", turnWorkDir)
	}
	// 工作目录之外的附加目录（总管会话的已注册项目）同样逐个放开。
	for _, dir := range additionalDirs {
		if dir == "" || dir == turnWorkDir {
			continue
		}
		args = append(args, "--add-dir", dir)
	}

	resModel, resEffort := normalizeModel(model, effort)
	if resModel != "" {
		args = append(args, "--model", resModel)
	}
	if resEffort != "" {
		args = append(args, "--effort", resEffort)
	}

	args = append(args, modeArguments(mode)...)

	if conversationID != "" {
		args = append(args, "--conversation", conversationID)
	}

	if len(routeArgs) > 0 {
		args = append(args, routeArgs...)
	}
	return args
}

var rateLimitKeywords = []string{
	"quota",
	"usage limit",
	"resource_exhausted",
	"额度",
	"用量",
	"429",
	"rate limit",
	"too many requests",
}

func isRateLimited(text string) bool {
	lower := strings.ToLower(text)
	for _, kw := range rateLimitKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// isTransientInvalidArgument 检测是否为后端偶发的临时 400 参数错误。
// 文本里同时包含 INVALID_ARGUMENT 和 'code 400' 时返回 true（不区分大小写）。
func isTransientInvalidArgument(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "invalid_argument") && strings.Contains(lower, "code 400")
}

var networkErrorKeywords = []string{
	": eof",
	"unexpected eof",
	"connection reset",
	"broken pipe",
	"i/o timeout",
	"tls handshake timeout",
	"connection refused",
	"no such host",
	"http2: ",
	"stream error",
}

// transientHTTPStatusRegex 匹配带语境词的 HTTP 5xx（500/502/503/504）状态码报错。
// 只认 5xx，避免把 400/401/403 以及普通数字误判成网络瞬时错误。
var transientHTTPStatusRegex = regexp.MustCompile(`(?i)(?:\bhttp\b|\bstatus\b|\bcode\b|api error)[^\n]{0,48}?\b(?:500|502|503|504)\b`)

// isTransientNetworkError 检测是否为可重试的网络抖动（不区分大小写）。
// 限流与 400/401/403 等确定性错误不算网络抖动，交给各自的分支处理。
func isTransientNetworkError(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if strings.HasSuffix(lower, "eof") {
		return true
	}
	for _, kw := range networkErrorKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return transientHTTPStatusRegex.MatchString(text)
}

var rfc3339Regex = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})`)

func parseResetTime(text string) time.Time {
	match := rfc3339Regex.FindString(text)
	if match != "" {
		if t, err := time.Parse(time.RFC3339Nano, match); err == nil {
			return t
		}
		if t, err := time.Parse(time.RFC3339, match); err == nil {
			return t
		}
	}
	return time.Time{}
}

type userMsgPayload struct {
	Event   string         `json:"event"`
	Message userMsgContent `json:"message"`
}

type userMsgContent struct {
	Content string `json:"content"`
}

type streamEnvelope struct {
	Event          string          `json:"event"`
	ConversationID string          `json:"conversation_id"`
	StepUpdate     json.RawMessage `json:"step_update"`
	Result         json.RawMessage `json:"result"`
}

type stepUpdatePayload struct {
	StepIndex int    `json:"step_index"`
	StepType  string `json:"step_type"`
	TextDelta string `json:"text_delta"`
	// 同一个正文也可能出现在 text / content / message 字段里（不同 agy 版本命名不一）。
	Text     string          `json:"text"`
	Content  string          `json:"content"`
	Message  string          `json:"message"`
	ToolName string          `json:"tool_name"`
	ToolID   string          `json:"tool_id"`
	State    string          `json:"state"`
	ToolInfo json.RawMessage `json:"tool_info"`
	Usage    *struct {
		InputTokens     int64 `json:"input_tokens"`
		OutputTokens    int64 `json:"output_tokens"`
		CacheReadTokens int64 `json:"cache_read_tokens"`
	} `json:"usage"`
}

type toolInfoPayload struct {
	Name       string          `json:"name"`
	ID         string          `json:"id"`
	Parameters json.RawMessage `json:"parameters"`
	Output     any             `json:"output"`
	Error      *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type resultPayload struct {
	Status         string          `json:"status"`
	Error          any             `json:"error"`
	ConversationID string          `json:"conversation_id"`
	Response       string          `json:"response"`
	Usage          json.RawMessage `json:"usage"`
	DeniedActions  []struct {
		Action      string `json:"action"`
		DisplayName string `json:"display_name"`
	} `json:"denied_actions"`
}

func (h *Harness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	bin := h.opts.Binary
	if bin == "" {
		bin = DefaultBinary
	}
	bin = resolveBinary(bin)
	if path, err := loginenv.LookPath(bin); err == nil {
		bin = path
	}

	conversationID := turn.SessionID
	var exclude []string
	var invalidArgRetried bool
	var networkRetried bool
	const maxRetries = 3

	// 本轮每次模型请求的输出速度样本：重试仍属同一轮，样本继续累积。
	// 计时器与样本以零值起步（未计时、空样本）。
	now := h.now
	if now == nil {
		now = time.Now
	}
	var requestClock harness.RequestClock
	var requestSpeeds []harness.RequestSpeed

	// 启动提问桥接服务（PreToolUse hook 拦截 ask_question）
	var bridge *QuestionBridge
	if b, err := StartQuestionBridge(ctx, turn.Approver); err != nil {
		log.Printf("agy: start question bridge failed: %v", err)
	} else if b != nil {
		bridge = b
		defer func() {
			if err := bridge.Close(); err != nil {
				log.Printf("agy: close question bridge failed: %v", err)
			}
		}()
	}

	for retryCount := 0; retryCount <= maxRetries; retryCount++ {
		accountID := "system"
		var routeEnv []string
		var routeArgs []string

		if h.opts.Router != nil {
			var err error
			accountID, routeEnv, routeArgs, err = h.opts.Router.Route(conversationID, turn.Model, exclude)
			if err != nil {
				return err
			}
		}

		allArgs := buildArgs(turn.WorkDir, conversationID, turn.Model, turn.Effort, turn.Mode, routeArgs, turn.AdditionalDirs)
		// 会话级 MCP 注入：内置与外部端点写进临时 gemini 目录的 config/mcp_config.json，
		// 用 --gemini_dir 覆盖本轮读取的配置，不碰用户的全局 antigravity 配置。
		if mcpArgs, mcpCleanup, err := prepareMCPGeminiDir(turn, routeArgs, routeEnv, h.opts.BuiltinMCP, h.opts.ExternalMCP); err != nil {
			log.Printf("agy: prepare session mcp config failed: %v", err)
		} else if len(mcpArgs) > 0 {
			allArgs = append(allArgs, mcpArgs...)
			defer mcpCleanup()
		}
		if bridge != nil {
			allArgs = append(allArgs, "--add-dir", bridge.Dir())
		}

		var cmd *exec.Cmd
		var cleanup func()
		var err error

		if turn.Sandbox != nil {
			cmd, cleanup, err = turn.Sandbox.Command(ctx, turn.WorkDir, turn.Mode, bin, allArgs...)
			if err != nil {
				return fmt.Errorf("agy: sandbox command: %w", err)
			}
		} else {
			cmd = procattr.HideWindow(exec.CommandContext(ctx, bin, allArgs...))
		}

		// 与 claude/codex 适配器一致：沙箱提供了 cmd.Env 时以其为基础环境，否则用登录 Shell 环境。
		baseEnv := loginenv.Environ()
		if cmd.Env != nil {
			baseEnv = cmd.Env
		}

		setProcessGroup(cmd)
		cmd.Dir = turn.WorkDir
		cmd.Env = buildEnv(baseEnv, turn.WorkDir, turn.Env, routeEnv)
		cmd.WaitDelay = 3 * time.Second

		stderrBuf := &tailBuffer{limit: 8192}
		cmd.Stderr = stderrBuf

		stdinPipe, err := cmd.StdinPipe()
		if err != nil {
			if cleanup != nil {
				cleanup()
			}
			return fmt.Errorf("agy: stdin pipe: %w", err)
		}

		stdoutPipe, err := cmd.StdoutPipe()
		if err != nil {
			_ = stdinPipe.Close()
			if cleanup != nil {
				cleanup()
			}
			return fmt.Errorf("agy: stdout pipe: %w", err)
		}

		if err := cmd.Start(); err != nil {
			_ = stdinPipe.Close()
			_ = stdoutPipe.Close()
			if cleanup != nil {
				cleanup()
			}
			tail := strings.TrimSpace(stderrBuf.String())
			if tail != "" {
				return fmt.Errorf("agy start failed: %w (stderr: %s)", err, tail)
			}
			return fmt.Errorf("agy start failed: %w", err)
		}
		// 重试循环里每轮都重新登记；Clear 只在 PID 仍匹配时清理，不会误删后续重试的记录。
		agentPID := cmd.Process.Pid
		h.roots.Set(turn.ThreadID, agentPID)
		defer h.roots.Clear(turn.ThreadID, agentPID)
		var endQuotaUse func()
		if tracker, ok := h.opts.Router.(interface{ QuotaUse(string) func() }); ok {
			endQuotaUse = tracker.QuotaUse(accountID)
		}

		promptText := turn.Prompt
		if invalidArgRetried {
			promptText = transientRetryPrompt
		} else if networkRetried {
			promptText = networkRetryPrompt
		}
		if turn.InteractionMode == "plan" {
			if !strings.HasPrefix(promptText, planModePromptGuard) {
				promptText = planModePromptGuard + promptText
			}
		} else if bridge != nil {
			if !strings.HasPrefix(promptText, questionBridgePromptGuard) {
				promptText = questionBridgePromptGuard + promptText
			}
		}
		// 放在最后拼接：前面的幂等检查都针对 turn.Prompt 原始前缀，先加会让它们失效。
		if !strings.Contains(promptText, visibleOutputPromptGuard) {
			promptText = visibleOutputPromptGuard + promptText
		}

		// 异步写入 user prompt
		userMsg := userMsgPayload{
			Event: "user",
			Message: userMsgContent{
				Content: promptText,
			},
		}
		data, _ := json.Marshal(userMsg)
		data = append(data, '\n')

		// 先起取消协程，再写 stdin：旧顺序把同步 Write 放在取消协程之前，prompt 超过管道
		// 缓冲（长上下文/长指令）且子进程还没开始读 stdin 时，Write 会永久阻塞，ctx 取消也救不了。
		shutdownCh := make(chan struct{})
		go func() {
			select {
			case <-shutdownCh:
				return
			case <-ctx.Done():
				_ = killProcessGroup(cmd, syscall.SIGTERM)
				time.Sleep(3 * time.Second)
				_ = killProcessGroup(cmd, syscall.SIGKILL)
			}
		}()

		// 写入放到独立协程：主循环立即开始读 stdout，不被子进程的读 stdin 时机阻塞。
		// 子进程被杀死或 stdin 被关闭后，内核返回 EPIPE，写协程自行结束（错误忽略）。
		go func() {
			select {
			case <-ctx.Done():
				return
			default:
			}
			_, _ = stdinPipe.Write(data)
		}()

		var streamedText bool
		var streamedTextBuf strings.Builder
		// 以下信号用于判断网络中断时本轮助手正文是否已经完整收尾：
		// lastEventIsText 表示最近一次对外 emit 的内容型事件是正文，而不是工具开始/结束；
		// lastReplyStepState 记录最近一个回复型 step 的 State（部分 agy 版本为空）；
		// doneEmitted 表示已经 emit 过 EventDone，补发成功标记时不要重复。
		var lastEventIsText bool
		var lastReplyStepState string
		var doneEmitted bool

		// syncText 处理「可能是整轮全文、也可能只是最后一段」的正文：
		// 已发送过的内容不再重复，能作为前缀扩展时只补差额，其余情况整段追加。
		syncText := func(text string) {
			trimmed := strings.TrimSpace(text)
			if trimmed == "" {
				return
			}
			sent := streamedTextBuf.String()
			if sent != "" && strings.Contains(sent, trimmed) {
				return
			}
			var rest string
			if strings.HasPrefix(text, sent) {
				rest = text[len(sent):]
				if strings.TrimSpace(rest) == "" {
					return
				}
			} else {
				rest = text
			}
			streamedText = true
			streamedTextBuf.WriteString(rest)
			_ = harness.Emit(emit, harness.Event{
				Type: harness.EventText,
				Text: rest,
			})
			lastEventIsText = true
		}

		var resultErr string
		var resultWatchdog *time.Timer
		stdinClosed := false
		startedTools := make(map[string]bool)
		// finishedTools 记录已 emit 过 ToolEnd 的工具，与 startedTools 配合可推出是否有工具正在进行中。
		finishedTools := make(map[string]bool)
		// lastStepContext 记录本轮最后一次模型请求实际带入的上下文大小（输入 + 输出）。
		// result 里的 usage 是整轮多步累计的计费用量，不能当上下文占用，所以只能从单步 usage 取。
		var lastStepContext int64

		// closeTurnInput 在本轮结束时关闭 stdin，让子进程退出。
		// 实测 agy 1.4.0：stream-json 模式下输出 result 后并不会自行退出，而是继续等待
		// stdin 的下一轮输入；只有在 stdin 收到 EOF 后才退出。若等到 stdout EOF 才关
		// stdin（旧实现），进程会一直挂着，整轮会话被拖到 print-timeout 才结束。
		// 兜底：关闭 stdin 后子进程仍未在宽限期内退出时，强杀整个进程组避免挂死。
		closeTurnInput := func() {
			if stdinClosed {
				return
			}
			stdinClosed = true
			_ = stdinPipe.Close()
			resultWatchdog = time.AfterFunc(15*time.Second, func() {
				_ = killProcessGroup(cmd, syscall.SIGTERM)
				time.Sleep(3 * time.Second)
				_ = killProcessGroup(cmd, syscall.SIGKILL)
			})
		}

		reader := bufio.NewReader(stdoutPipe)
		for {
			line, readErr := reader.ReadBytes('\n')
			if len(line) > 0 {
				line = bytes.TrimSpace(line)
				if len(line) > 0 {
					var env streamEnvelope
					if err := json.Unmarshal(line, &env); err == nil {
						switch env.Event {
						case "init":
							if env.ConversationID != "" {
								conversationID = env.ConversationID
								_ = harness.Emit(emit, harness.Event{
									Type:      harness.EventSession,
									SessionID: conversationID,
								})
								if h.opts.Router != nil {
									h.opts.Router.Bind(conversationID, accountID)
								}
							}

						case "step_update":
							var step stepUpdatePayload
							if err := json.Unmarshal(env.StepUpdate, &step); err == nil {
								if step.StepType == "tool" {
									// 工具步会打断「正文已收尾」的 step 信号：之后必须再有回复型 step 收尾才算完成。
									lastReplyStepState = ""
								} else {
									lastReplyStepState = step.State
								}
								switch step.StepType {
								case "agent_response":
									// 同一帧可能同时带正文和 usage：先按内容帧开始计时，再结算本次请求。
									if hasStepText(step) {
										requestClock.Begin(now())
									}
									if step.Usage != nil {
										// 单步 usage 就是这一次模型请求的用量，可直接当作本轮上下文占用。
										if speed, ok := requestClock.Finish(now(), step.Usage.OutputTokens); ok {
											requestSpeeds = append(requestSpeeds, speed)
										}
										lastStepContext = step.Usage.InputTokens + step.Usage.OutputTokens
										_ = harness.Emit(emit, harness.Event{
											Type: harness.EventUsage,
											Usage: &harness.Usage{
												InputTokens:   step.Usage.InputTokens,
												OutputTokens:  step.Usage.OutputTokens,
												CachedTokens:  step.Usage.CacheReadTokens,
												ContextTokens: lastStepContext,
												Requests:      requestSpeedSamples(requestSpeeds),
											},
										})
									}
									// text_delta 明确是增量，直接追加；text/content/message 可能是全文，走去重合并。
									if step.TextDelta != "" {
										streamedText = true
										streamedTextBuf.WriteString(step.TextDelta)
										_ = harness.Emit(emit, harness.Event{
											Type: harness.EventText,
											Text: step.TextDelta,
										})
										lastEventIsText = true
									}
									syncText(step.Text)
									syncText(step.Content)
									syncText(step.Message)

								case "tool":
									toolName := step.ToolName
									var info toolInfoPayload
									if len(step.ToolInfo) > 0 && string(step.ToolInfo) != "null" {
										_ = json.Unmarshal(step.ToolInfo, &info)
										if toolName == "" {
											toolName = info.Name
										}
									}
									toolID := info.ID
									if toolID == "" {
										toolID = step.ToolID
									}
									if toolID == "" {
										toolID = fmt.Sprintf("agy-tool-%d", step.StepIndex)
									}

									if !startedTools[toolID] {
										startedTools[toolID] = true
										_ = harness.Emit(emit, harness.Event{
											Type:     harness.EventToolStart,
											ToolID:   toolID,
											ToolName: toolName,
											Input:    info.Parameters,
										})
										lastEventIsText = false
										if step.State == "ACTIVE" {
											continue
										}
									}

									if step.State == "ERROR" || (step.State == "DONE" && info.Error != nil) {
										errMsg := "The tool failed."
										if info.Error != nil && info.Error.Message != "" {
											errMsg = info.Error.Message
										}
										_ = harness.Emit(emit, harness.Event{
											Type:    harness.EventToolEnd,
											ToolID:  toolID,
											Output:  errMsg,
											IsError: true,
										})
										finishedTools[toolID] = true
										lastEventIsText = false
									} else if step.State == "DONE" {
										outputStr := ""
										if s, ok := info.Output.(string); ok {
											outputStr = s
										} else if info.Output != nil {
											b, _ := json.Marshal(info.Output)
											outputStr = string(b)
										}
										_ = harness.Emit(emit, harness.Event{
											Type:    harness.EventToolEnd,
											ToolID:  toolID,
											Output:  outputStr,
											IsError: false,
										})
										finishedTools[toolID] = true
										lastEventIsText = false
									}
								}
							}

						case "result":
							// result 到达即代表本轮结束：立即关闭 stdin 触发子进程退出，
							// 继续读完 stdout 剩余输出后再 Wait（顺序见循环下方）。
							closeTurnInput()
							var res resultPayload
							if err := json.Unmarshal(env.Result, &res); err == nil {
								if res.ConversationID != "" {
									conversationID = res.ConversationID
									_ = harness.Emit(emit, harness.Event{
										Type:      harness.EventSession,
										SessionID: conversationID,
									})
									if h.opts.Router != nil {
										h.opts.Router.Bind(conversationID, accountID)
									}
								}

								if len(res.Usage) > 0 && string(res.Usage) != "null" {
									var u struct {
										InputTokens     int64 `json:"input_tokens"`
										OutputTokens    int64 `json:"output_tokens"`
										CacheReadTokens int64 `json:"cache_read_tokens"`
									}
									if err := json.Unmarshal(res.Usage, &u); err == nil {
										if u.InputTokens > 0 || u.OutputTokens > 0 || u.CacheReadTokens > 0 {
											_ = harness.Emit(emit, harness.Event{
												Type: harness.EventUsage,
												Usage: &harness.Usage{
													InputTokens:  u.InputTokens,
													OutputTokens: u.OutputTokens,
													CachedTokens: u.CacheReadTokens,
													// 计费仍按 result 的累计用量记，上下文占用跟着最后一次单步 usage。
													ContextTokens: lastStepContext,
													Requests:      requestSpeedSamples(requestSpeeds),
												},
											})
										}
									}
								}

								// 正文兜底：result.response 可能是整轮全文、也可能只是最后一段，
								// 还可能与已流式内容重复，统一走去重合并。
								syncText(res.Response)

								switch res.Status {
								case "SUCCESS", "WAITING":
									if !streamedText && strings.TrimSpace(res.Response) == "" && len(res.DeniedActions) > 0 {
										actionName := res.DeniedActions[0].DisplayName
										if actionName == "" {
											actionName = res.DeniedActions[0].Action
										}
										resultErr = fmt.Sprintf("action denied: %s", actionName)
									}
									_ = harness.Emit(emit, harness.Event{
										Type:       harness.EventDone,
										StopReason: res.Status,
									})
									doneEmitted = true
								default:
									if res.Error != nil {
										if s, ok := res.Error.(string); ok {
											resultErr = s
										} else {
											b, _ := json.Marshal(res.Error)
											resultErr = string(b)
										}
									}
									if resultErr == "" {
										resultErr = fmt.Sprintf("result status: %s", res.Status)
									}
								}
							}
						}
					}
				}
			}
			if readErr != nil {
				if errors.Is(readErr, io.EOF) {
					break
				}
				break
			}
		}

		// 读到 stdout EOF：本轮输出已完整，撤销宽限期强杀计时器再收尾，避免误杀。
		if resultWatchdog != nil {
			resultWatchdog.Stop()
		}
		_ = stdinPipe.Close()
		close(shutdownCh)

		waitErr := cmd.Wait()
		if endQuotaUse != nil {
			endQuotaUse()
		}
		if cleanup != nil {
			cleanup()
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		// 检查子进程报错或非零退出
		var rawErrStr string
		if waitErr != nil {
			rawErrStr = waitErr.Error()
			if resultErr != "" {
				rawErrStr = resultErr + "\n" + rawErrStr
			}
		} else if resultErr != "" {
			rawErrStr = resultErr
		}

		stderrStr := stderrBuf.String()
		if strings.Contains(stderrStr, "no output produced") && !streamedText && resultErr == "" {
			resultErr = "headless mode auto-denied tool permission without output"
			if rawErrStr == "" {
				rawErrStr = resultErr
			}
		}
		combinedErr := strings.TrimSpace(rawErrStr + "\n" + stderrStr)

		if waitErr != nil || resultErr != "" {
			retrySuffix := ""
			if invalidArgRetried || networkRetried {
				retrySuffix = "（已自动续跑 1 次）"
			}

			// 匹配限流关键词
			if isRateLimited(combinedErr) {
				resetAt := parseResetTime(combinedErr)
				if h.opts.Router != nil {
					allowSwitch := h.opts.Router.MarkLimited(conversationID, accountID, resetAt)
					if !allowSwitch {
						// 严格单号模式或其他禁止自动换号情况：直接报错
						if waitErr != nil {
							return fmt.Errorf("agy process exited with error: %w (stderr: %s)%s", waitErr, stderrStr, retrySuffix)
						}
						return fmt.Errorf("agy error: %s%s", resultErr, retrySuffix)
					}
					// 允许自动换号，且本轮输出内容没有被截断（即尚未向用户流式输出正文）
					if !streamedText {
						exclude = append(exclude, accountID)
						if retryCount < maxRetries {
							// 继续换号重试
							continue
						}
						return fmt.Errorf("agy: rate limit failover exceeded 3 attempts (last error: %s)", combinedErr)
					}
				}
			}

			// 网络抖动（EOF/连接重置等）常发生在正文已完整输出之后：此时任务实际已完成，
			// 不能再把整轮判失败，否则上层会重做已经完成的检查。
			if isTransientNetworkError(combinedErr) {
				bodyCompleted := streamedText && (lastEventIsText || lastReplyStepState == "DONE")
				noActiveTool := len(startedTools) == len(finishedTools)
				if bodyCompleted && noActiveTool {
					log.Printf("agy: ignore transient network error after completed output on conversation %s: %s", conversationID, strings.TrimSpace(combinedErr))
					if !doneEmitted {
						// 没有可用的事件备注字段（Err 只属于 EventError 语义），说明只写进日志。
						_ = harness.Emit(emit, harness.Event{
							Type:       harness.EventDone,
							StopReason: "SUCCESS",
						})
					}
					return nil
				}

				// 正文尚未收尾：在同一会话内自动续跑 1 次，不改 exclude、不换号。
				if !networkRetried && conversationID != "" {
					networkRetried = true
					retryCount-- // 抵消 continue 后的 retryCount++，不占用限流换号重试配额
					log.Printf("agy: transient network error on conversation %s, retrying once from interruption point: %s", conversationID, strings.TrimSpace(combinedErr))
					continue
				}
			}

			// 后端临时 400 错误自动续跑 1 次：不改 exclude、不换号，会话 ID 保持不变，无论是否已有流式正文均续跑
			if isTransientInvalidArgument(combinedErr) && !invalidArgRetried && conversationID != "" {
				invalidArgRetried = true
				retryCount-- // 抵消 continue 后的 retryCount++，不占用限流换号重试配额
				log.Printf("agy: transient INVALID_ARGUMENT (code 400) detected on conversation %s, retrying once from interruption point", conversationID)
				continue
			}

			if waitErr != nil {
				if strings.TrimSpace(stderrStr) != "" {
					return fmt.Errorf("agy process exited with error: %w (stderr: %s)%s", waitErr, strings.TrimSpace(stderrStr), retrySuffix)
				}
				return fmt.Errorf("agy process exited with error: %w%s", waitErr, retrySuffix)
			}
			return fmt.Errorf("agy error: %s%s", resultErr, retrySuffix)
		}

		return nil
	}

	return nil
}

// hasStepText 报告 agent_response 步骤是否带正文：delta 是增量，text/content/message 可能是全文。
func hasStepText(step stepUpdatePayload) bool {
	return step.TextDelta != "" || strings.TrimSpace(step.Text) != "" ||
		strings.TrimSpace(step.Content) != "" || strings.TrimSpace(step.Message) != ""
}

// requestSpeedSamples 返回本轮请求速度样本的拷贝，避免事件里带上内部切片。
func requestSpeedSamples(samples []harness.RequestSpeed) []harness.RequestSpeed {
	if len(samples) == 0 {
		return nil
	}
	return append([]harness.RequestSpeed(nil), samples...)
}
