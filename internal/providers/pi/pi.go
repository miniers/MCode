// Package pi 实现了基于 Pi CLI `pi --mode rpc` 的 harness.Harness 适配器。
//
// 协议契约：
//   - `pi --mode rpc` 子进程，stdin 写 JSON 行请求（get_state、prompt、abort、set_model 等），
//     stdout 读 JSON 行事件（message_start/update/end、tool_execution_*、agent_end、agent_settled 等）。
//     Options.IdleTTL 为 0 时一轮一个进程，当轮结束即退出；大于 0 时按线程驻留，空闲超时才退出
//     （见 acquire / release）。
//   - 会话恢复：新进程以 `--session <pi 会话文件路径>` 启动；每轮 get_state 回报的
//     sessionFile 通过 EventSession 发出去，供下一轮继续恢复。驻留进程只在会话文件、启动参数
//     与环境都没变时复用，否则换新进程；模型与思考档位的变化用 set_model / set_thinking_level
//     就地切换，不重启。
//   - 审批：固定传 --no-approve（忽略项目本地不受信文件）；工具审批由门禁扩展负责。带扩展
//     （Options.Extension，-e）时，扩展把每次工具调用以 mcode-gate 门禁
//     （extension_ui_request）发回来，适配器按 turn.Approver/Interactor 应答；没有审批方时
//     一律拒绝，避免请求悬在管道里卡死当轮。
//   - MCP 接入：通过 MCODE_MCP_SERVERS 与 mcp_bridge.ts 扩展加载。内置 mcode 服务直接注册工具，
//     外部 MCP 服务通过 mcode_mcp_list 与 mcode_mcp_call 两级网关按需暴露，避免全量 schema 常驻上下文。
//
// 尚未实现的行为：compact（RPC 有 compact 请求，但 harness.Harness 没有对应接口）。
package pi

import (
	"context"
	"encoding/base64"
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
	"mcode/internal/procutil"
)

var (
	_ harness.Harness           = (*Harness)(nil)
	_ harness.Steerer           = (*Harness)(nil)
	_ harness.TurnStopper       = (*Harness)(nil)
	_ harness.DeliveryConfirmer = (*Harness)(nil)
	_ io.Closer                 = (*Harness)(nil)
)

// DefaultBinary 是 Pi CLI 的默认可执行文件名。
const DefaultBinary = "pi"

// Options 包含 Pi 适配器的配置选项。
type Options struct {
	Binary    string
	ExtraArgs []string
	// Extension 是传给 `-e` 的 pi 扩展路径；加载扩展后，工具门禁才会以
	// extension_ui_request 回来走 turn.Approver 审批。
	Extension string
	// GatePath 是运行模式注入文件路径，经 GateEnvVar 传给门禁扩展。为空时 Run 启动前会写一份临时文件并在当轮结束时删除。
	GatePath string
	// MagpieBridge 是 Magpie 账号桥扩展（magpie_bridge.ts）的文件路径，作为末尾的 `-e`
	// 加载；为空时既不加载桥，也不用 pi 之外的端点配置。
	MagpieBridge string
	// MagpieEnv 返回 MCODE_PI_MAGPIE 的 JSON（mcode 设置里的 Magpie 端点），返回空串表示
	// 本轮不注入、pi 用它自己的配置。apiKey 只随这个环境变量走，不进命令行参数。
	MagpieEnv func() string
	// InjectedModel 判断模型 ID 是否属于 mcode 注入的 Magpie provider，且 pi 自己的 models.json
	// 里没有该 provider（未被 global 接管）。这类模型 pi 不认识，Run 会改用 pi 的默认模型。
	InjectedModel func(model string) bool
	// OnMagpieMember 在 Magpie 桥回传实际路由到的上游账号时被调用。
	OnMagpieMember func(endpoint, provider, model string)
	// Env 追加到子进程环境，排在最前，turn.Env 里的同名变量优先级更高。
	Env []string
	// Sandbox 是默认沙箱；turn.Sandbox 为空时使用。
	Sandbox harness.Sandbox
	// BuiltinMCP 是 mcode 自己的 MCP 服务，ExternalMCP 按工作目录返回用户外部接入的服务。
	// 两者经 MCODE_MCP_SERVERS 交给内嵌的桥接扩展（mcp_bridge.ts，以第二个 -e 加载）：
	// 内置 mcode 服务工具直接注册，外部 MCP 服务通过 mcode_mcp_list / mcode_mcp_call 两级网关按需暴露。
	BuiltinMCP  harness.BuiltinMCP
	ExternalMCP func(workDir, threadID string) []harness.MCPEndpoint
	// Skills 返回该工作目录下需要显式加载的技能路径，以及是否走「完整清单」模式。
	// 第二个返回值为 true 时，参数里先加 --no-skills 关掉 pi 自己的技能发现，再由 --skill
	// 逐个带上返回的路径（列表为空也照样传 --no-skills）；为 false 时退回旧行为，
	// 只传返回的路径，不传 --no-skills。
	Skills func(workDir string) ([]string, bool)
	// IdleTTL 大于 0 时，带 ThreadID 的轮次结束后 pi 进程按线程驻留这么久，下一轮复用，
	// 省掉 Node 冷启动；为 0 时每轮一个进程，当轮结束即退出。
	IdleTTL time.Duration
	// MaxResident 是同时驻留（空闲）的进程数上限，超出时先回收最久没用的；0 取 defaultMaxResident。
	MaxResident int
}

// defaultMaxResident 是 Options.MaxResident 的默认值：一个 pi 进程是一个 Node 运行时，
// 常驻内存可观，不能让每个线程都留一个。
const defaultMaxResident = 4

// excludedDelegateTools 是 mcode 会话里不再暴露的委派工具（billion-context 的 acp_delegate
// 三件套，约 6.6k 字符）。mcode 自己的 bee 负责改代码，pi-subagents 的 subagent 负责只读
// 调查，这一套能力被完全覆盖且更弱（没有 workflow 编排，也不能并行）。
const excludedDelegateTools = "acp_delegate,acp_delegate_wait,acp_delegate_cancel"

// Harness 实现了基于 Pi RPC 模式的 harness.Harness 接口。
type Harness struct {
	opts Options

	// mu 保护 runs 与 procs；activeRun.sessionID 同样由该锁保护。
	mu   sync.Mutex
	runs []*activeRun
	// procs 是按线程驻留的 pi 进程（只有 IdleTTL > 0 才会登记）。
	procs map[string]*proc
	// closed 表示该 harness 已被替换或关闭：收尾的进程不再回池，直接回收。
	closed bool
	// roots 按会话记录正在运行的 agent 进程 PID，供 ProcessRootPIDs 使用。
	roots harness.ProcessRoots
}

// activeRun 是一轮正在执行的 pi 进程，供 Steer 与 ForceStopTurn 定位。
type activeRun struct {
	turnID    string
	sessionID string
	cmd       *exec.Cmd
	writer    *procutil.LineWriter
}

// New 创建一个新的 Pi Harness 实例。
func New(opts Options) *Harness {
	return &Harness{opts: opts, procs: make(map[string]*proc)}
}

// Close 终止所有驻留的 pi 进程并等它们退出，可重复调用。
func (h *Harness) Close() error {
	h.mu.Lock()
	h.closed = true
	procs := make([]*proc, 0, len(h.procs))
	for _, p := range h.procs {
		procs = append(procs, p)
	}
	h.procs = make(map[string]*proc)
	h.mu.Unlock()
	for _, p := range procs {
		p.closeAndWait()
	}
	return nil
}

// CloseIdle 只释放空闲驻留的 pi 进程，正在执行的轮次不受影响：它把 harness 标为已关闭，
// 忙的进程在收尾归还进程池时直接退出，而不是再驻留一个没人用的 Node 运行时。
// 供“替换 harness”这类场景使用（显式关闭仍走 Close）。
func (h *Harness) CloseIdle() {
	var idle []*proc
	h.mu.Lock()
	h.closed = true
	for threadID, p := range h.procs {
		p.mu.Lock()
		busy := p.busy
		p.mu.Unlock()
		if busy {
			continue
		}
		delete(h.procs, threadID)
		idle = append(idle, p)
	}
	h.mu.Unlock()
	for _, p := range idle {
		p.closeAndWait()
	}
}

// Kind 返回后端的种类标识。
func (h *Harness) Kind() harness.Kind {
	return harness.Kind("pi")
}

// CatalogBinary 返回当前运行时配置的 CLI。
func (h *Harness) CatalogBinary() string { return h.opts.Binary }

// ProcessRootPIDs 返回指定会话当前运行的 agent 进程 PID，未在运行时返回 nil。
func (h *Harness) ProcessRootPIDs(threadID string) []int {
	return h.roots.PIDs(threadID)
}

// ConfirmsApprovalDelivery 返回 true，表示该 Provider 对每次 Approve/Interact 的答复都会调用
// DeliveryAcker.AckDelivery。
func (h *Harness) ConfirmsApprovalDelivery() bool {
	return true
}

// withNodeEnvProxy 保证 pi 的 Node 子进程走环境变量里的代理（对 NODE_OPTIONS 的处理）。
// 同名变量以最后一个为准，因此从后往前找。
func withNodeEnvProxy(env []string) []string {
	for i := len(env) - 1; i >= 0; i-- {
		if !strings.HasPrefix(env[i], "NODE_OPTIONS=") {
			continue
		}
		if strings.Contains(env[i], "--use-env-proxy") {
			return env
		}
		env[i] += " --use-env-proxy"
		return env
	}
	return append(env, "NODE_OPTIONS=--use-env-proxy")
}

// mergeNoProxy 把 localhost、127.0.0.1、::1 合并进逗号分隔的代理忽略名单：
// 保留原值，已有的项不重复追加。
func mergeNoProxy(val string) string {
	required := []string{"localhost", "127.0.0.1", "::1"}
	if strings.TrimSpace(val) == "" {
		return strings.Join(required, ",")
	}
	parts := strings.Split(val, ",")
	seen := make(map[string]bool)
	for _, p := range parts {
		trimmed := strings.ToLower(strings.TrimSpace(p))
		if trimmed != "" {
			seen[trimmed] = true
		}
	}
	res := val
	for _, req := range required {
		if !seen[strings.ToLower(req)] {
			if res != "" && !strings.HasSuffix(res, ",") {
				res += ","
			}
			res += req
			seen[strings.ToLower(req)] = true
		}
	}
	return res
}

// withLoopbackNoProxy 把 localhost、127.0.0.1、::1 合并进 NO_PROXY 和 no_proxy：
// 保留原值，已有的项不重复追加，变量不存在就新建。
func withLoopbackNoProxy(env []string) []string {
	keys := []string{"NO_PROXY", "no_proxy"}
	result := make([]string, len(env))
	copy(result, env)

	for _, key := range keys {
		prefix := key + "="
		found := false
		for i := len(result) - 1; i >= 0; i-- {
			if strings.HasPrefix(result[i], prefix) {
				val := result[i][len(prefix):]
				result[i] = prefix + mergeNoProxy(val)
				found = true
				break
			}
		}
		if !found {
			result = append(result, prefix+mergeNoProxy(""))
		}
	}
	return result
}

// buildEnv 构造子进程环境变量列表：
// 1. 从 base 中剔除所有以 "PWD=" 开头的项，避免原样继承父进程的环境变量 PWD；
// 2. 当 workDir 非空时追加 "PWD=" + workDir，保证子进程 PWD 与工作目录一致；
// 3. 最后追加 extra。extra 中自带的 PWD 因排在末尾而优先。
func buildEnv(base []string, workDir string, extra []string) []string {
	env := make([]string, 0, len(base)+1+len(extra))
	for _, entry := range base {
		if !strings.HasPrefix(entry, "PWD=") {
			env = append(env, entry)
		}
	}
	if workDir != "" {
		env = append(env, "PWD="+workDir)
	}
	env = append(env, extra...)
	return env
}

// splitModelRef 拆分 pi 的 "provider/model" 形式；不含斜杠时只返回模型名。
func splitModelRef(model string) (provider, modelID string, qualified bool) {
	if index := strings.Index(model, "/"); index > 0 && index < len(model)-1 {
		return model[:index], model[index+1:], true
	}
	return "", model, false
}

// buildArgs 组装调用 pi RPC 模式所需的命令行参数。
// mcpExtension 与 magpieExtension 非空时分别作为第二、第三个 -e 加载 MCP 桥接扩展与
// Magpie 账号桥（-e 可重复），顺序保持在门禁扩展之后。
// skills 包含需要显式加载的技能路径列表，每个路径以一组成对的 --skill 追加在 --no-approve 之后。
// fullSkills 为 true 时先在 --no-approve 之后加 --no-skills：关掉 pi 自己的技能发现（含
// settings.json 的 skills 条目与 packages 带来的技能），由 mcode 给出的清单决定加载哪些。
func buildArgs(opts Options, turn harness.Turn, mcpExtension, magpieExtension string, skills []string, fullSkills bool) []string {
	args := []string{"--mode", "rpc"}
	if opts.Extension != "" {
		args = append(args, "-e", opts.Extension)
	}
	if mcpExtension != "" {
		args = append(args, "-e", mcpExtension)
	}
	if magpieExtension != "" {
		args = append(args, "-e", magpieExtension)
	}
	args = append(args, "--no-approve")
	// pi 默认只开 read / bash / edit / write，计划模式下就只剩 read，模型只能猜路径。用 +name
	// 形式补上只读检索工具：既保留默认集合，也不会变成白名单把扩展自定义工具（vision、
	// ask_user_question）和 MCP 工具挡在外面。
	args = append(args, "--tools", "+grep,+find,+ls")
	// 委派只留两条路：改代码靠 swarm_delegate 的 bee，只读调查靠 subagent。
	args = append(args, "-xt", excludedDelegateTools)
	if fullSkills {
		args = append(args, "--no-skills")
	}
	for _, s := range skills {
		args = append(args, "--skill", s)
	}

	// "default" 是模型目录的占位行，交给 pi 自己的默认模型解析，不传 --model。
	if turn.Model != "" && turn.Model != "default" {
		if provider, modelID, qualified := splitModelRef(turn.Model); qualified {
			args = append(args, "--provider", provider, "--model", modelID)
		} else {
			args = append(args, "--model", modelID)
		}
	}
	if turn.Effort != "" {
		args = append(args, "--thinking", turn.Effort)
	}
	if turn.SessionID != "" {
		args = append(args, "--session", turn.SessionID)
	}

	return append(args, opts.ExtraArgs...)
}

// imagePayloads 读取本地图片并编码为 prompt 请求携带的图片载荷；读失败时返回错误。
// 不是图片的附件（PDF、docx、表格、压缩包等）跳过并打日志：它们的路径已由上层写进
// Prompt，agent 可以用文件工具读取。图片类型由 harness.ImageMediaType 判定：扩展名优先，
// 扩展名不可靠时按文件内容嗅探（会话媒体目录里的历史文件没有扩展名）。
func imagePayloads(paths []string) ([]imagePayload, error) {
	var images []imagePayload
	for _, path := range paths {
		mimeType, ok := harness.ImageMediaType(path)
		if !ok {
			log.Printf("pi: skip image %s: unsupported image type %q", path, filepath.Ext(path))
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("pi: read image %s: %w", path, err)
		}
		images = append(images, imagePayload{
			Type:     "image",
			Data:     base64.StdEncoding.EncodeToString(data),
			MimeType: mimeType,
		})
	}
	return images, nil
}

// Steer 向指定会话正在执行的轮次注入指引：以 steer 请求写入其 stdin。
// 找不到对应会话的活动进程时返回 harness.ErrNotRunning。
func (h *Harness) Steer(ctx context.Context, sessionID string, text string) error {
	if sessionID == "" {
		return harness.ErrNotRunning
	}
	run := h.findRun(func(r *activeRun) bool { return r.sessionID == sessionID })
	if run == nil {
		return harness.ErrNotRunning
	}
	line, err := requestLine(steerRequest(nextRequestID(), text))
	if err != nil {
		return err
	}
	if err := run.writer.WriteLine(line); err != nil {
		return fmt.Errorf("pi: steer: %w", err)
	}
	return nil
}

// ForceStopTurn 强停指定轮次的 pi 进程组，不影响同一 Harness 的其他会话。
func (h *Harness) ForceStopTurn(turnID string) {
	run := h.findRun(func(r *activeRun) bool { return r.turnID == turnID })
	if run == nil {
		return
	}
	// 先杀进程组再关 stdin：子进程不再读 stdin 而管道写满时，writer.close() 会卡在 l.mu 上，
	// 先关 writer 会让 SIGKILL 永远等不到执行。
	_ = killProcessGroup(run.cmd, syscall.SIGKILL)
	_ = run.writer.Close()
}

// registerRun 记录当轮的活动进程。
func (h *Harness) registerRun(run *activeRun) {
	h.mu.Lock()
	h.runs = append(h.runs, run)
	h.mu.Unlock()
}

// unregisterRun 移除当轮的活动进程。
func (h *Harness) unregisterRun(run *activeRun) {
	h.mu.Lock()
	for i, candidate := range h.runs {
		if candidate == run {
			h.runs = append(h.runs[:i], h.runs[i+1:]...)
			break
		}
	}
	h.mu.Unlock()
}

// setRunSession 在 pi 回报会话 id 之后更新活动进程的会话标识。
func (h *Harness) setRunSession(run *activeRun, sessionID string) {
	h.mu.Lock()
	run.sessionID = sessionID
	h.mu.Unlock()
}

// findRun 返回第一个匹配的活动进程。
func (h *Harness) findRun(match func(*activeRun) bool) *activeRun {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, run := range h.runs {
		if match(run) {
			return run
		}
	}
	return nil
}
