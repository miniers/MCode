package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"mcode/internal/harness"
	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
)

var _ harness.Harness = (*Harness)(nil)

// DefaultBinary 是 Claude Code CLI 的默认可执行文件名。
const DefaultBinary = "claude"

// Options 包含 Claude 适配器的配置选项。
type Options struct {
	Binary      string
	ExtraArgs   []string
	BuiltinMCP  harness.BuiltinMCP
	ExternalMCP func(workDir, threadID string) []harness.MCPEndpoint
	// FirstOutputTimeout 为进程启动后等待首个 stdout 输出的上限，0 表示使用默认值。
	FirstOutputTimeout time.Duration
	// StallTimeout 为已收到输出后等待新输出的上限，0 表示使用默认值。
	StallTimeout time.Duration
	// IdleTTL 大于 0 时常驻进程生效：带 ThreadID 的普通轮次结束后 claude 进程按线程驻留这么久，
	// 下一轮直接复用，省掉 CLI 冷启动；为 0 时每轮一个进程，轮结束即退出。
	IdleTTL time.Duration
	// MaxResident 是同时驻留的进程数上限，超出时先回收最久没用的空闲进程；0 表示使用默认值。
	MaxResident int
	// InterruptGrace 是取消一轮时等被打断那一轮收尾（result）的上限，0 表示使用默认值。
	InterruptGrace time.Duration
	// MaxTurns 大于 0 时，常驻进程服务满这么多轮后下一轮改用 --resume 重建：长会话里 CLI 进程内存只增不减，
	// 定期换新可以释放；0 表示不按轮数回收。
	MaxTurns int
	// MaxAge 大于 0 时，常驻进程存活满这么久后下一轮改用 --resume 重建（同上）；0 表示不按存活时间回收。
	MaxAge time.Duration
	// MagpieRoutes 返回当前可用的 magpie 路由；nil 表示未启用。
	// 每轮调用一次，以便拿到设置页刚改完的配置。
	MagpieRoutes func() []MagpieRoute
	// Logf 非空时记录常驻进程的关键事件（新起、重建及原因、热切换、回收、steer），不记录普通复用。
	Logf func(format string, args ...any)
}

// 默认超时：CLI 在空闲后恢复会话时可能卡在会话锁、初始化握手或网络重试上，
// 超时后强杀进程组并把失败回报给上层，避免界面长时间无声挂起。
const (
	defaultFirstOutputTimeout = 90 * time.Second
	defaultStallTimeout       = 15 * time.Minute
	// resultDrainTimeout 是收到 result 之后等待进程退出的兜底时长：
	// CLI 此时已完成本轮，只是派生的子进程（MCP、shell 等）可能还占着 stdout 管道。
	resultDrainTimeout = 5 * time.Second
	// defaultInterruptGrace 是取消一轮后等被打断那一轮补 result 的默认时长。
	defaultInterruptGrace = 5 * time.Second
)

func (o Options) firstOutputTimeout() time.Duration {
	if o.FirstOutputTimeout > 0 {
		return o.FirstOutputTimeout
	}
	return defaultFirstOutputTimeout
}

func (o Options) stallTimeout() time.Duration {
	if o.StallTimeout > 0 {
		return o.StallTimeout
	}
	return defaultStallTimeout
}

func (o Options) interruptGrace() time.Duration {
	if o.InterruptGrace > 0 {
		return o.InterruptGrace
	}
	return defaultInterruptGrace
}

// Harness 实现了基于 Claude Code 命令行界面的 harness.Harness 接口。
type Harness struct {
	opts Options
	// procs 按会话记录正在运行的 agent 进程 PID，供 ProcessRootPIDs 定位命令子进程树。
	procs harness.ProcessRoots

	// mu 保护 residents 与 closed。
	mu sync.Mutex
	// residents 是按线程驻留的常驻进程，只有 Options.IdleTTL > 0 才会登记。
	residents map[string]*residentProc
	// closed 表示 harness 已被关闭或替换：后续轮次全部退回一轮一进程，收尾的进程也不再驻留。
	closed bool
	// unsolicitedHandler 是上层登记的自发轮次回调：常驻进程空闲时自己跑出新一轮就通知它补一轮。
	// 由 mu 保护；为 nil 时保持空闲输出直接丢弃的原有行为。
	unsolicitedHandler func(threadID string)
	// processExitHandler 是上层登记的进程退出回调：常驻进程退出、之后不会再为该会话产出事件时通知上层。
	// 由 mu 保护；为 nil 时保持原有静默退出行为。
	processExitHandler func(threadID string, before time.Time)

	// statsMu 保护 stats：常驻进程的复用、重建等计数，供排查与测试读取。
	statsMu sync.Mutex
	stats   ResidentStats
}

// New 创建一个新的 Claude Harness 实例。
func New(opts Options) *Harness {
	return &Harness{opts: opts, residents: make(map[string]*residentProc)}
}

// Kind 返回后端的种类标识。
func (h *Harness) Kind() harness.Kind {
	return harness.Kind("claude")
}

// CatalogBinary 返回当前运行时配置的 CLI。
func (h *Harness) CatalogBinary() string { return h.opts.Binary }

// ConfirmsApprovalDelivery 返回 true，表示该 Provider 对每次 Approve/Interact 的答复都会调用 DeliveryAcker.AckDelivery。
func (h *Harness) ConfirmsApprovalDelivery() bool {
	return true
}

// ProcessRootPIDs 返回指定会话当前运行的 agent 进程 PID，未在运行时返回 nil。
func (h *Harness) ProcessRootPIDs(threadID string) []int {
	return h.procs.PIDs(threadID)
}

// nativeSubagentTools 是始终禁用的 Claude 原生子代理工具：编排（swarm）经 swarm 块或
// swarm_delegate MCP 工具派发，不依赖它们；bee 与未开编排的主线程同样不允许自行起子代理。
const nativeSubagentTools = "Agent,Task"

// buildArgs 组装调用 Claude 命令行所需的参数列表。
func buildArgs(turn harness.Turn, extra ...string) []string {
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--disallowed-tools", nativeSubagentTools,
	}

	// "default" 表示交给 CLI 自己的默认模型，此时不传 --model。
	if turn.Model != "" && turn.Model != "default" {
		args = append(args, "--model", turn.Model)
	}
	if turn.Effort != "" {
		args = append(args, "--effort", turn.Effort)
	}
	// 快速模式：以 --settings 覆写开启 fastMode。
	if turn.FastMode {
		args = append(args, "--settings", `{"fastMode":true}`)
	}

	if turn.SessionID != "" {
		args = append(args, "--resume", turn.SessionID)
	}

	perm := permissionModeFor(turn)
	// fullAccess 需要该标记才会在 headless（-p）下跳过权限确认，只传 --permission-mode 不够。
	if turn.Mode == harness.ModeFullAccess {
		args = append(args, "--allow-dangerously-skip-permissions")
	}
	args = append(args, "--permission-mode", perm)

	// 工作目录之外的附加目录（总管会话的已注册项目）用 --add-dir 逐个放开。
	seenDirs := make(map[string]bool, len(turn.AdditionalDirs))
	for _, dir := range turn.AdditionalDirs {
		if dir == "" || dir == turn.WorkDir || seenDirs[dir] {
			continue
		}
		seenDirs[dir] = true
		args = append(args, "--add-dir", dir)
	}

	if len(extra) > 0 {
		args = append(args, extra...)
	}

	return args
}

// maxUserImageBytes 是单张图片参与多模态输入的体积上限：Anthropic 的图片块要求 base64 后
// 不超过 5MB，这里按原文件字节数卡住上限，超出的图片只保留 Prompt 里的路径文本。
const maxUserImageBytes = 5 << 20

// userContentBlock 是 user 消息 content 数组里的一块：text 块带正文，image 块带 base64 来源。
type userContentBlock struct {
	Type   string       `json:"type"`
	Text   string       `json:"text,omitempty"`
	Source *imageSource `json:"source,omitempty"`
}

// imageSource 是 Anthropic 图片块的来源：图片以 base64 内联在帧里。
type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// buildUserContent 把本轮 Prompt 与拖入的图片组装成 user 消息的 content。
// 没有可用图片时直接返回 prompt 原文（content 仍序列化成字符串，与改造前字节一致）；
// 有图片时返回 text 块 + 若干 image 块。图片类型由 harness.ImageMediaType 判定（扩展名优先，
// 扩展名不可靠时按文件内容嗅探），读取失败、不是图片、超过体积上限的图片跳过该图
// （它的路径文本已由上层拼进 Prompt，作为读取失败时的回退），并记一行日志说明原因。
func buildUserContent(turn harness.Turn) any {
	var images []userContentBlock
	for _, imgPath := range turn.Images {
		mediaType, ok := harness.ImageMediaType(imgPath)
		if !ok {
			log.Printf("claude: skip image %s: unsupported image type %q", imgPath, filepath.Ext(imgPath))
			continue
		}
		info, err := os.Stat(imgPath)
		if err != nil {
			log.Printf("claude: skip image %s: %v", imgPath, err)
			continue
		}
		if info.Size() > maxUserImageBytes {
			log.Printf("claude: skip image %s: %d bytes exceeds %d", imgPath, info.Size(), maxUserImageBytes)
			continue
		}
		data, err := os.ReadFile(imgPath)
		if err != nil {
			log.Printf("claude: skip image %s: %v", imgPath, err)
			continue
		}
		images = append(images, userContentBlock{Type: "image", Source: &imageSource{
			Type:      "base64",
			MediaType: mediaType,
			Data:      base64.StdEncoding.EncodeToString(data),
		}})
	}
	if len(images) == 0 {
		return turn.Prompt
	}
	return append([]userContentBlock{{Type: "text", Text: turn.Prompt}}, images...)
}

// stdinPrompt 返回一次性进程写到 stdin 的内容：无图片时就是 prompt 原文；有图片时改成
// 一帧 stream-json user 消息（形状与常驻路径的 stdin 帧一致），由 CLI 按 content 数组解析。
func stdinPrompt(turn harness.Turn) string {
	if len(turn.Images) == 0 {
		return turn.Prompt
	}
	data, err := json.Marshal(stdinUserFrame{
		Type:    "user",
		Message: stdinUserMessage{Role: "user", Content: buildUserContent(turn)},
	})
	if err != nil {
		// 帧里只有字符串与 base64，序列化不会失败；真失败了退回纯文本 prompt。
		log.Printf("claude: marshal user frame: %v", err)
		return turn.Prompt
	}
	return string(data) + "\n"
}

// permissionModeFor 返回本轮 --permission-mode 的取值：Plan 交互模式优先于运行模式，只规划不改动。
func permissionModeFor(turn harness.Turn) string {
	perm := "acceptEdits"
	switch turn.Mode {
	case harness.ModeReadOnly:
		perm = "plan"
	case harness.ModeSupervised:
		perm = "default"
	case harness.ModeAutoAcceptEdits, harness.ModeAuto, "":
		perm = "auto"
	case harness.ModeFullAccess:
		perm = "bypassPermissions"
	default:
		perm = "acceptEdits"
	}
	if turn.InteractionMode == "plan" {
		perm = "plan"
	}
	// 模型能力降级：目录里明确写着所选模型不支持 auto 时改用 acceptEdits，并提示用户；
	// 目录未加载或条目没这个字段时保持 auto，交给 CLI 自己报错。
	if perm == "auto" {
		if supported, known := autoModeSupported(turn.Model); known && !supported {
			log.Printf("claude: 模型 %s 不支持 auto 权限模式，降级为 acceptEdits", autoModeModelLabel(turn.Model))
			perm = "acceptEdits"
		}
	}
	return perm
}

// autoModeModelLabel 把空模型名换成目录里的默认模型名，仅用于日志可读性。
func autoModeModelLabel(model string) string {
	if model == "" {
		return "default"
	}
	return model
}

type mcpServerConfig struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

type mcpConfig struct {
	MCPServers map[string]mcpServerConfig `json:"mcpServers"`
}

// approvalsActive 判断本轮是否需要审批通道：有审批方就启用。fullAccess 下 CLI 本就不会为普通
// 工具调用审批通道，但 AskUserQuestion / EnterPlanMode / ExitPlanMode 只有带
// --permission-prompt-tool 时 CLI 才会提供，且 Plan 模式退出落地也依赖它（否则死锁）。
func approvalsActive(turn harness.Turn) bool {
	return turn.Approver != nil
}

// buildMCPArgs 根据 Turn 的审批需求与 Harness 的内置 MCP 配置，构造合并后的 MCP 命令行参数。
// 整个命令行最多只生成一个 --mcp-config 参数。
func buildMCPArgs(turn harness.Turn, approvalURL, approvalToken string, builtin harness.BuiltinMCP, external []harness.MCPEndpoint) []string {
	servers := make(map[string]mcpServerConfig)
	var promptTool string

	if approvalsActive(turn) && approvalURL != "" {
		servers["mcode_approval"] = mcpServerConfig{
			Type: "http",
			URL:  approvalURL,
			Headers: map[string]string{
				"Authorization": "Bearer " + approvalToken,
			},
		}
		promptTool = "mcp__mcode_approval__approve"
	}

	if url, token, workdirHeader := builtin.Endpoint(turn); url != "" {
		servers["mcode"] = mcpServerConfig{
			Type: "http",
			URL:  url,
			Headers: map[string]string{
				"Authorization": "Bearer " + token,
			},
		}
		if workdirHeader {
			servers["mcode"].Headers["X-Mcode-Workdir"] = turn.WorkDir
		}
	}

	for _, ep := range external {
		if ep.Name == "mcode" || ep.Name == "mcode_approval" {
			continue
		}
		headers := make(map[string]string)
		if ep.Token != "" {
			headers["Authorization"] = "Bearer " + ep.Token
		}
		servers[ep.Name] = mcpServerConfig{
			Type:    "http",
			URL:     ep.URL,
			Headers: headers,
		}
	}

	if len(servers) == 0 {
		return nil
	}

	cfg := mcpConfig{MCPServers: servers}
	data, _ := json.Marshal(cfg)
	args := []string{"--mcp-config", string(data)}
	if promptTool != "" {
		args = append(args, "--permission-prompt-tool", promptTool)
	}
	return args
}

// buildEnv 构造子进程环境变量列表：
// 1. 从 base 中剔除所有以 "PWD=" 开头的项，避免原样继承父进程的环境变量 PWD；
// 2. 当 workDir 非空时追加 "PWD=" + workDir，保证子进程 PWD 与工作目录一致；
// 3. 默认追加 "CLAUDE_CODE_SUBAGENT_MODEL=haiku"，把 Claude Code 的原生子代理固定为 haiku；
// 如果 base 中已存在同名项（用户在外部环境里设置过）则尊重它、不重复追加；
// 4. 最后追加 extra（即 turn.Env）。如果 turn.Env 中自带了 PWD 或 CLAUDE_CODE_SUBAGENT_MODEL，由于追加在末尾，其优先级高于此处注入的值（在子进程中以最后出现的项为准）。
func buildEnv(base []string, workDir string, extra []string) []string {
	env := make([]string, 0, len(base)+2+len(extra))
	for _, e := range base {
		if !strings.HasPrefix(e, "PWD=") {
			env = append(env, e)
		}
	}
	if workDir != "" {
		env = append(env, "PWD="+workDir)
	}
	if !hasEnvKey(env, "CLAUDE_CODE_SUBAGENT_MODEL=") {
		env = append(env, "CLAUDE_CODE_SUBAGENT_MODEL=haiku")
	}
	// 让模型使用 TaskCreate/TaskUpdate 记录待办，界面据此显示待办进度。
	if !hasEnvKey(env, "CLAUDE_CODE_ENABLE_TODO_TOOLS=") {
		env = append(env, "CLAUDE_CODE_ENABLE_TODO_TOOLS=1")
	}
	env = append(env, extra...)
	return env
}

// hasEnvKey 判断环境变量列表 env 中是否已有以 prefix 开头的项。
func hasEnvKey(env []string, prefix string) bool {
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

// tailBuffer 是一个保留末尾至多指定字节大小的缓冲区。
type tailBuffer struct {
	limit int
	buf   []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.limit {
		b.buf = b.buf[len(b.buf)-b.limit:]
	}
	return n, nil
}

func (b *tailBuffer) String() string {
	return string(b.buf)
}

// promptTracker 记录 prompt 是否已经写进子进程 stdin。
// exec.Cmd 在 Start 之后才从 Stdin 读取，所以这里读到写入即表示 CLI 已经收到（或正在接收）prompt：
// 此时本轮失败若再用同一个 prompt 重跑，工具调用等副作用会被静默重放，因此不再自动重试。
type promptTracker struct {
	src         io.Reader
	onDelivered func() // 第一次真正读出 prompt 时回调（用于上报 EventTurnAccepted），可为 nil
	mu          sync.Mutex
	wrote       bool
	once        sync.Once
}

func (p *promptTracker) Read(b []byte) (int, error) {
	n, err := p.src.Read(b)
	if n > 0 {
		p.mu.Lock()
		p.wrote = true
		p.mu.Unlock()
		// 在把字节交给 exec 之前回调：此时字节还没写进管道，CLI 不可能已经产出正文。
		if p.onDelivered != nil {
			p.once.Do(p.onDelivered)
		}
	}
	return n, err
}

// delivered 返回 prompt 是否已经开始写入子进程。
func (p *promptTracker) delivered() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.wrote
}

// promptNotDelivered 返回 prompt 还没有写进子进程 stdin（没有 prompt 时视为未投递）。
func promptNotDelivered(prompt *promptTracker) bool {
	return prompt == nil || !prompt.delivered()
}

// emitTurnAccepted 上报本轮用户 prompt 已经写进 CLI 的 stdin（claude 的强保证）。
func emitTurnAccepted(emit func(harness.Event)) {
	_ = harness.Emit(emit, harness.Event{Type: harness.EventTurnAccepted})
}

// processFailure 表示子进程没有正常收尾。
// retryable 为 true 表示进程在产出任何输出之前就失效（空闲后子进程已被回收、管道已断、被杀等），
// 此时可以用 resume 重新拉起并把同一 prompt 再发一遍；report 是上报给上层（界面）的错误文本。
type processFailure struct {
	err       error
	report    string
	retryable bool
}

func (e *processFailure) Error() string { return e.err.Error() }

func (e *processFailure) Unwrap() error { return e.err }

// Run 执行一轮 Claude Code 交互。
//
// 混合模式：Options.IdleTTL > 0 时每个会话线程留一个常驻 claude 子进程（不随本轮 ctx 退出），
// 每轮往它的 stdin 写一帧 user 消息并读到 result 为止，省掉 CLI 冷启动；空闲超时回收，
// 启动指纹或会话 id 对不上、进程已失效时用 --resume 重建。IdleTTL <= 0 时仍是单轮短进程模型。
// 两种情况下的重试口径相同：只有“本轮未产出任何输出即失效”才自动用 resume 重新拉起并把同一
// prompt 再发一遍，仍失败才把错误回报给上层；同时用输出看门狗覆盖“进程还活着但一直没有输出”。
func (h *Harness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	// stdin 写入协程与读输出的 goroutine 都会上报事件（accepted 在写入侧上报），这里统一串行化，
	// 同时保证同一轮的重试不会把 EventTurnAccepted 报两次。
	var (
		emitMu   sync.Mutex
		accepted bool
	)
	deliver := emit
	emit = func(e harness.Event) {
		emitMu.Lock()
		defer emitMu.Unlock()
		if e.Type == harness.EventTurnAccepted {
			if accepted {
				return
			}
			accepted = true
		}
		deliver(e)
	}
	if turn.Attach {
		// 接住自发输出的一轮：只接到该线程现有的常驻进程上，不写用户帧、不起新进程、不重试。
		return h.runAttach(ctx, turn, emit)
	}
	if h.opts.MagpieRoutes != nil {
		var err error
		turn, err = magpieTurn(turn, h.opts.MagpieRoutes())
		if err != nil {
			return err
		}
	}
	err := h.runAttempt(ctx, turn, emit)
	var failure *processFailure
	if !errors.As(err, &failure) {
		return err
	}
	if failure.retryable && ctx.Err() == nil {
		// 内部重试：第一次失败不上报为错误，否则重试成功时后端仍会把本轮判为失败（见后端 candidateErr 的 errorEvent 分支）。
		// 常驻路径重试时旧进程已被收起，acquireResident 会带 --resume 新起一个。
		err = h.runAttempt(ctx, turn, emit)
		if !errors.As(err, &failure) {
			return err
		}
	}
	// 无法重试或重试后仍失败：作为最终错误上报，界面据此结束“回复中”并展示错误。
	_ = harness.Emit(emit, harness.Event{
		Type: harness.EventError,
		Err:  failure.report,
	})
	return err
}

// runOnce 执行一次进程尝试：启动 CLI、读取 stream-json 输出，并把进程未正常收尾的情况包装成 *processFailure。
// 错误事件由 Run 统一上报，这样内部重试失败的首轮不会污染本轮结果。
func (h *Harness) runOnce(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	bin := h.opts.Binary
	if bin == "" {
		bin = DefaultBinary
	}

	var approvalURL, approvalToken string
	if approvalsActive(turn) {
		srv, err := startApprovalServer(ctx, turn.Approver)
		if err != nil {
			return fmt.Errorf("claude: start approval server: %w", err)
		}
		defer srv.close()
		approvalURL = srv.url()
		approvalToken = srv.token()
	}

	var extra []string
	var external []harness.MCPEndpoint
	if h.opts.ExternalMCP != nil {
		external = h.opts.ExternalMCP(turn.WorkDir, turn.ThreadID)
		// 所有扫描到的服务都由代理接管，不再让 CLI 额外加载本地配置。
		extra = append(extra, "--strict-mcp-config")
	}
	if mcpArgs := buildMCPArgs(turn, approvalURL, approvalToken, h.opts.BuiltinMCP, external); len(mcpArgs) > 0 {
		extra = append(extra, mcpArgs...)
	}
	merged, mergeErr := mergeMCPArgs(extra, h.opts.ExtraArgs)
	if mergeErr != nil {
		return mergeErr
	}

	args := buildArgs(turn, merged...)
	// 带图片时改用 stream-json 输入：文本与 image 块要在同一帧 user 消息里送达。
	// 常驻路径（spawnResident）本来就固定用 stream-json，这里只给一次性路径补上缺少的参数；
	// 一次性路径没有 --replay-user-messages，写完这一帧就关闭 stdin，进程随之正常退出。
	if len(turn.Images) > 0 {
		args = append(args, "--input-format", "stream-json")
	}

	// --mcp-config 的内联 JSON 里有 MCP 的 Bearer 令牌，放在 argv 里同机其他用户用 ps 就能看到；
	// 改成写 0600 临时文件、argv 只传路径（claude 的 --mcp-config 支持文件路径），本轮结束后删除。
	args, cleanupMCPConfig, err := externalizeMCPConfig(args)
	if err != nil {
		return err
	}
	defer cleanupMCPConfig()

	var cmd *exec.Cmd
	var cleanup func()

	if turn.Sandbox != nil {
		cmd, cleanup, err = turn.Sandbox.Command(ctx, turn.WorkDir, turn.Mode, bin, args...)
		if err != nil {
			return fmt.Errorf("claude: sandbox command: %w", err)
		}
	} else {
		cmd = procattr.HideWindow(exec.CommandContext(ctx, bin, args...))
	}
	if cleanup != nil {
		defer cleanup()
	}

	// 使用进程组管理子进程生命周期，避免 ctx 取消时 Claude 派生的子进程（MCP、shell 脚本等）成为孤儿进程泄漏，
	// 并设置 WaitDelay 避免 stdout 管道被未退出的子进程占用导致 Wait 永久阻塞。
	setProcessGroup(cmd)
	cmd.Cancel = func() error {
		return killProcessGroup(cmd, syscall.SIGTERM)
	}
	cmd.WaitDelay = 3 * time.Second

	baseEnv := loginenv.Environ()
	if cmd.Env != nil {
		baseEnv = cmd.Env
	}
	cmd.Dir = turn.WorkDir
	cmd.Env = buildEnv(baseEnv, turn.WorkDir, turn.Env)
	_ = EnsureSharedLayoutFromEnv(cmd.Env)

	// prompt 通过 stdin 写入，不放在参数里，以免参数过长或被当成参数解析
	var prompt *promptTracker
	if turn.Prompt != "" || len(turn.Images) > 0 {
		prompt = &promptTracker{src: strings.NewReader(stdinPrompt(turn)), onDelivered: func() {
			// prompt 已经开始写进子进程 stdin：本轮输入已被 CLI 收下。
			emitTurnAccepted(emit)
		}}
		cmd.Stdin = prompt
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("claude: stdout pipe: %w", err)
	}

	stderrBuf := &tailBuffer{limit: 8192}
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("claude: start: %w", err)
	}
	// 记录 agent 进程 PID，本轮结束后清理，供「终止运行中的命令」按会话定位进程树。
	agentPID := cmd.Process.Pid
	h.procs.Set(turn.ThreadID, agentPID)
	defer h.procs.Clear(turn.ThreadID, agentPID)

	p := newParser()
	reader := bufio.NewReader(stdout)

	// 输出看门狗：启动后 firstOutputTimeout 内没有任何输出，或此后 stallTimeout 内没有新输出，
	// 就强杀整个进程组并关闭管道读端，让读取循环立刻收尾。这样 CLI 卡在会话锁、MCP 握手或网络重试时
	// 不会无声挂起，而是把失败回报给上层（界面显示错误并结束本轮）。
	var (
		watchdogMu    sync.Mutex
		watchdogFired string
	)
	fireWatchdog := func(reason string) {
		watchdogMu.Lock()
		watchdogFired = reason
		watchdogMu.Unlock()
		_ = killProcessGroup(cmd, syscall.SIGKILL)
		// 兜底关闭读端：万一仍有子进程占着管道写端，读取循环也能立即结束。
		_ = stdout.Close()
	}
	watchdog := time.AfterFunc(h.opts.firstOutputTimeout(), func() {
		fireWatchdog(fmt.Sprintf("produced no output within %s", h.opts.firstOutputTimeout()))
	})
	defer watchdog.Stop()

	sawOutput := false
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				sawOutput = true
				_ = p.handleLine(trimmed, emit)
				// 有新输出：静默计时重置。收到 result 后 CLI 应立刻退出，只留短兜底等残留子进程释放管道。
				if p.sawResult {
					watchdog.Reset(resultDrainTimeout)
				} else {
					watchdog.Reset(h.opts.stallTimeout())
				}
			}
		}
		if readErr != nil {
			break
		}
	}

	// 读取循环已结束，先停表，避免进程正常退出后被看门狗误杀。
	watchdog.Stop()
	watchdogMu.Lock()
	timedOut := watchdogFired
	watchdogMu.Unlock()

	// 读完 stdout 之后，如果 ctx 已取消，补发一次 SIGKILL 给进程组作为兜底
	if ctx.Err() != nil {
		_ = killProcessGroup(cmd, syscall.SIGKILL)
	}

	waitErr := cmd.Wait()

	if ctx.Err() != nil {
		return ctx.Err()
	}

	tail := strings.TrimSpace(stderrBuf.String())

	// 看门狗触发且本轮还没有 result：进程被卡死，把超时作为错误回报，而不是无声挂起。
	if timedOut != "" && !p.sawResult {
		errMsg := "claude process " + timedOut
		if tail != "" {
			errMsg += " (stderr: " + tail + ")"
		}
		// 启动后一直没有输出、stderr 也没有说明（空闲后进程卡死或已被回收）、且 prompt 还没写进
		// 子进程 stdin 时，允许用 resume 重启重试一次；已经产出过输出或 prompt 已投递则不再重试，
		// 避免把已经开始的轮次（含工具副作用）跑两遍。
		return &processFailure{err: errors.New(errMsg), report: errMsg, retryable: !sawOutput && tail == "" && promptNotDelivered(prompt)}
	}

	if !p.sawResult {
		// stdout 已结束但没有 result 行：本轮没有正常收尾（进程被杀、静默退出、管道提前断开），
		// 按失败回报给上层，不再把“无输出的退出”当成成功。
		var base error
		if waitErr != nil {
			base = fmt.Errorf("claude process exited with error: %w (stderr: %s)", waitErr, tail)
		} else {
			base = fmt.Errorf("claude process exited without a result (stderr: %s)", tail)
		}
		report := tail
		if report == "" {
			report = base.Error()
		}
		// 进程在产出任何输出之前失效（无 stdout、也没有 stderr 说明或被信号杀死）且 prompt 还没
		// 写进 stdin 时，允许用 resume 重启重试一次；prompt 已投递说明 CLI 可能已开始执行工具，
		// 重跑会重放副作用，因此不重试。
		retryable := !sawOutput && (tail == "" || processSignaled(waitErr)) && promptNotDelivered(prompt)
		return &processFailure{err: base, report: report, retryable: retryable}
	}

	return nil
}

// processSignaled 判断等待结果是否表示进程被信号终止（而非正常退出）。
func processSignaled(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	return exitErr.ProcessState != nil && !exitErr.ProcessState.Exited()
}
