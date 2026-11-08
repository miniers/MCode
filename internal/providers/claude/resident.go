package claude

// 本文件实现 Claude CLI 的常驻进程（混合模式）：Options.IdleTTL > 0 时每个线程留一个
// `claude -p --input-format stream-json` 子进程，每轮往 stdin 写一帧 user 消息并读到
// type:result 为止；空闲超时回收，启动指纹或会话对不上、进程失效时用 --resume 重建。
// IdleTTL <= 0 时完全不启用，Run 仍走 runOnce（一轮一个进程）。
//
// 协议要点（claude 2.1.x 实测）：
//   - stdin 每行一帧 JSON，用户消息帧必须带 type/message/parent_tool_use_id 四个字段；
//   - 一轮结束以 stdout 上的 type:result 为界，之后进程继续等下一条 user 帧；
//   - 打断用 control_request/subtype=interrupt，被打断那一轮会补一个 is_error 的 result，
//     进程随后回到空闲。
//
// 自发轮次（后台任务）：进程空闲时仍可能自己跑出一段输出——Bash 的 run_in_background 任务结束时
// CLI 会再跑一轮，帧序列是 system/task_notification（带 task_id）→ system/init → assistant → result，
// 没有对应的 user 帧。没有轮次挂接时这段输出原本会被丢弃；现在先按 task_started/task_notification
// 计数后台任务，再通知上层（harness.UnsolicitedTurnNotifier）用 Turn.Attach 补一轮，把这段输出接到
// 这一轮上。已知限制：CLI 启动指纹变化（模型、权限模式、MCP 配置等被换掉）导致进程重建时，旧进程
// 会连同它的后台任务一起结束，那一段输出仍然收不到。

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"mcode/internal/harness"
	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
)

// 编译期接口断言：上层按 io.Closer 或 CloseIdle() 探测释放常驻进程。
var _ io.Closer = (*Harness)(nil)

var _ interface{ CloseIdle() } = (*Harness)(nil)

var _ harness.Steerer = (*Harness)(nil)

var _ harness.UnsolicitedTurnNotifier = (*Harness)(nil)

var _ harness.ProcessExitNotifier = (*Harness)(nil)

const (
	// defaultMaxResident 是 Options.MaxResident 的默认值：每个 claude 进程都是一个 Node 运行时，
	// 常驻内存可观，不能让每个线程都留一个。
	defaultMaxResident = 4
	// sinkBuffer 是当轮行通道的缓冲：一轮阻塞在审批上时，读取协程还能再收这么多行。
	sinkBuffer = 256
	// unsolicitedBacklogLimit 是没有轮次挂接时缓存自发输出行的上限：超过后不再缓存，只记一次日志。
	unsolicitedBacklogLimit = 10000
	// gracefulCloseWait 是关 stdin 之后等 CLI 自行退出的时长，超时按进程组 SIGKILL。
	gracefulCloseWait = 2 * time.Second
	// stopTaskTimeout 是等待后台任务停止应答的最长时间。
	stopTaskTimeout = 5 * time.Second
	// approvalURLPlaceholder 与 approvalTokenPlaceholder 是计算复用指纹时顶替审批端点真实
	// URL 与令牌的占位符：真实值每个进程都会重新生成，不能进指纹。
	approvalURLPlaceholder   = "http://127.0.0.1:0/mcp"
	approvalTokenPlaceholder = "mcode-approval-token-placeholder"
)

// ResidentStats 是常驻进程的累计计数：复用率与各类重建原因一目了然。
type ResidentStats struct {
	// Spawns 是新起的常驻进程数（含重建）。
	Spawns int
	// Reuses 是直接复用已有进程的轮次数。
	Reuses int
	// HotSwitches 是复用进程时靠控制帧热切换设置的轮次数。
	HotSwitches int
	// Steers 是成功注入到运行中轮次的 steer 消息数。
	Steers int
	// Reaped 是空闲超时被回收的进程数。
	Reaped int
	// Rebuilds 按原因统计重建：process-exited / launch-changed / session-mismatch / recycled / apply-failed。
	Rebuilds map[string]int
}

// Stats 返回常驻进程累计计数的快照。
func (h *Harness) Stats() ResidentStats {
	h.statsMu.Lock()
	defer h.statsMu.Unlock()
	out := h.stats
	out.Rebuilds = make(map[string]int, len(h.stats.Rebuilds))
	for k, v := range h.stats.Rebuilds {
		out.Rebuilds[k] = v
	}
	return out
}

// bump 在 statsMu 下更新计数。
func (h *Harness) bump(update func(*ResidentStats)) {
	h.statsMu.Lock()
	update(&h.stats)
	h.statsMu.Unlock()
}

// noteRebuild 记录一次重建及原因。
func (h *Harness) noteRebuild(threadID, reason string) {
	h.bump(func(s *ResidentStats) {
		if s.Rebuilds == nil {
			s.Rebuilds = make(map[string]int)
		}
		s.Rebuilds[reason]++
	})
	h.logf("claude: resident rebuild thread=%s reason=%s", threadID, reason)
}

func (h *Harness) logf(format string, args ...any) {
	if h.opts.Logf != nil {
		h.opts.Logf(format, args...)
	}
}

// claudeLaunch 是一轮要用的启动参数与复用指纹。
type claudeLaunch struct {
	binary string
	// args 是含审批端点占位符的完整参数，启动前把占位符换成真实值。
	args []string
	key  string
	// soft 是本轮想要的、可在复用的进程里热切换的设置；hot 为真表示这些设置已从指纹里剔除
	// （切换模型、思考强度、权限模式不再重建进程），为假时它们仍在指纹里。
	soft softSettings
	hot  bool
}

// softSettings 是 CLI 支持在会话中途热切换的设置：set_model、apply_flag_settings(effortLevel)、
// set_permission_mode。
type softSettings struct {
	model, effort, perm string
}

// planLaunch 汇总本轮的启动参数（审批端点用占位符）并算出复用指纹。
func (h *Harness) planLaunch(turn harness.Turn) (claudeLaunch, error) {
	binary := h.opts.Binary
	if binary == "" {
		binary = DefaultBinary
	}

	var extra []string
	var external []harness.MCPEndpoint
	if h.opts.ExternalMCP != nil {
		external = h.opts.ExternalMCP(turn.WorkDir, turn.ThreadID)
		// 所有扫描到的服务都由代理接管，不再让 CLI 额外加载本地配置。
		extra = append(extra, "--strict-mcp-config")
	}
	if mcpArgs := buildMCPArgs(turn, approvalURLPlaceholder, approvalTokenPlaceholder, h.opts.BuiltinMCP, external); len(mcpArgs) > 0 {
		extra = append(extra, mcpArgs...)
	}
	merged, err := mergeMCPArgs(extra, h.opts.ExtraArgs)
	if err != nil {
		return claudeLaunch{}, err
	}

	args := buildArgs(turn, merged...)

	soft := softSettings{model: turn.Model, effort: turn.Effort, perm: permissionModeFor(turn)}
	if soft.model == "default" {
		soft.model = ""
	}
	// 只有模型与思考强度都显式给出、且不走沙箱（沙箱命令随权限模式生成）时才能热切换：
	// 否则进程启动时缺这些参数，之后没法靠控制帧补上，变化仍要重建。
	hot := turn.Sandbox == nil && soft.model != "" && soft.effort != ""
	keyArgs := args
	if hot {
		keyArgs = stripSoftFlags(args)
	}
	return claudeLaunch{binary: binary, args: args, key: launchKey(turn, binary, keyArgs, hot), soft: soft, hot: hot}, nil
}

// launchKey 是进程复用指纹：启动时固定下来、之后无法更改的东西都算进去
// （二进制、完整 argv、工作目录、环境变量、是否沙箱、是否有审批通道）。
// hot 为真时 args 已剔除 model/effort/permission-mode（它们改用控制帧热切换）；
// FastMode、是否 fullAccess、是否有审批通道等变化仍会换新进程。
func launchKey(turn harness.Turn, binary string, args []string, hot bool) string {
	env := append([]string{}, buildEnv(loginenv.Environ(), turn.WorkDir, turn.Env)...)
	sort.Strings(env)

	sandbox := ""
	if turn.Sandbox != nil {
		// 沙箱按权限模式生成命令，类型或模式一变就得换进程。
		sandbox = fmt.Sprintf("sandbox:%T:%s", turn.Sandbox, turn.Mode)
	}

	parts := []string{
		binary,
		strings.Join(stripResume(args), "\x00"),
		turn.WorkDir,
		strings.Join(env, "\x01"),
		sandbox,
		strconv.FormatBool(approvalsActive(turn)),
		strconv.FormatBool(hot),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x02")))
	return hex.EncodeToString(sum[:])
}

// stripResume 去掉 --resume 及其值：会话 id 每轮都可能不同，复用判断由进程记录的会话 id
// 单独比对，不该让它影响启动指纹。
func stripResume(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--resume" {
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// stripSoftFlags 去掉可热切换的启动参数及其值（--model / --effort / --permission-mode）。
func stripSoftFlags(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--model", "--effort", "--permission-mode":
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// replaceApprovalEndpoint 把指纹用的占位审批端点换成这一进程真实的 URL 与令牌。
func replaceApprovalEndpoint(args []string, url, token string) []string {
	out := make([]string, len(args))
	for i, arg := range args {
		arg = strings.ReplaceAll(arg, approvalURLPlaceholder, url)
		out[i] = strings.ReplaceAll(arg, approvalTokenPlaceholder, token)
	}
	return out
}

// residentWanted 判断这一轮是否走常驻进程：辅助会话（Swarm bee）一般只跑一轮，
// 给它留常驻进程只会白占名额。
func (h *Harness) residentWanted(turn harness.Turn) bool {
	return h.opts.IdleTTL > 0 && turn.ThreadID != "" && !turn.Transient
}

// runAttach 接住常驻进程在空闲时自发跑出的一段输出（后台任务结束后的续跑）：不发新的用户帧，
// 只接到该线程现有的进程上把这段输出读完。进程不存在、已失效、正忙，或没有待接的自发输出时
// 直接返回 nil，不产生任何事件。
func (h *Harness) runAttach(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	if turn.ThreadID == "" || h.opts.IdleTTL <= 0 {
		return nil
	}
	p, ok := h.acquireAttached(turn.ThreadID)
	if !ok {
		return nil
	}
	return h.runOnResident(ctx, p, turn, claudeLaunch{}, emit, true)
}

// HasUnsolicitedOutput 供后端在线程空闲后判断是否需要补开 attach 轮。
func (h *Harness) HasUnsolicitedOutput(threadID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.residents[threadID]
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.dead && (p.unsolicited || len(p.backlog) > 0 || p.unsolicitedDone)
}

// StopBackgroundTask 请求停止指定常驻进程中的后台任务。
func (h *Harness) StopBackgroundTask(ctx context.Context, threadID, taskID string) error {
	if threadID == "" || taskID == "" {
		return errors.New("线程 ID 和后台任务 ID 不能为空")
	}
	h.mu.Lock()
	p := h.residents[threadID]
	if p == nil {
		h.mu.Unlock()
		return errors.New("后台任务所在的 claude 进程已退出")
	}
	p.mu.Lock()
	if p.dead {
		p.mu.Unlock()
		h.mu.Unlock()
		return errors.New("后台任务所在的 claude 进程已退出")
	}
	id := fmt.Sprintf("mcode-stop_task-%d", controlSeq.Add(1))
	waiter := make(chan controlResult, 1)
	if p.controlWaiters == nil {
		p.controlWaiters = make(map[string]chan controlResult)
	}
	p.controlWaiters[id] = waiter
	p.mu.Unlock()
	h.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.controlWaiters, id)
		p.mu.Unlock()
	}()
	if err := p.sendControlWithID(id, "stop_task", map[string]any{"task_id": taskID}); err != nil {
		return fmt.Errorf("发送停止后台任务请求: %w", err)
	}
	timer := time.NewTimer(stopTaskTimeout)
	defer timer.Stop()
	select {
	case result := <-waiter:
		if result.success {
			return nil
		}
		return fmt.Errorf("停止后台任务失败: %s", result.reason)
	case <-p.exited:
		return errors.New("后台任务所在的 claude 进程已退出")
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("等待停止后台任务应答超时")
	}
}

// acquireAttached 取出 attach 轮要接的常驻进程：只看会话表里的现有进程，没有进程、已死、正忙
// 或没有待接的自发输出时返回 false。与 acquireResident 不同，这里不比对启动指纹与会话，也不重建
// 进程：attach 轮只读这一段已经开始的输出。
func (h *Harness) acquireAttached(threadID string) (*residentProc, bool) {
	h.mu.Lock()
	p := h.residents[threadID]
	if p == nil {
		h.mu.Unlock()
		return nil, false
	}
	p.mu.Lock()
	ok := !p.busy && !p.dead && (p.unsolicited || len(p.backlog) > 0 || p.unsolicitedDone)
	if ok {
		p.busy = true
		p.turns++
		p.resetSteerLocked()
		if p.idle != nil {
			p.idle.Stop()
			p.idle = nil
		}
	}
	p.mu.Unlock()
	h.mu.Unlock()
	if !ok {
		return nil, false
	}
	h.bump(func(s *ResidentStats) { s.Reuses++ })
	return p, true
}

// runAttempt 执行一次尝试：常驻模式下复用或新起该线程的 claude 进程，其余情况
// （含同一线程上一轮还没收尾）沿用一轮一进程的 runOnce。
func (h *Harness) runAttempt(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	if !h.residentWanted(turn) {
		return h.runOnce(ctx, turn, emit)
	}
	spec, err := h.planLaunch(turn)
	if err != nil {
		return err
	}
	p, reused, ok, err := h.acquireResident(turn, spec)
	if err != nil {
		return err
	}
	if !ok {
		// harness 已关闭或该线程上一轮还在跑：这一轮用独立进程，行为与一轮一进程一致。
		return h.runOnce(ctx, turn, emit)
	}
	return h.runOnResident(ctx, p, turn, spec, emit, reused)
}

// acquireResident 取出本轮要用的常驻进程：该线程上有空闲、未失效、指纹与会话都对得上的进程
// 就复用，否则丢弃旧进程并按本轮参数新起一个（turn.SessionID 非空则带 --resume）。
// ok 为 false 表示这一轮不该用常驻进程，调用方应退回 runOnce。
func (h *Harness) acquireResident(turn harness.Turn, spec claudeLaunch) (p *residentProc, reused, ok bool, err error) {
	var stale *residentProc
	reason := ""
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, false, false, nil
	}
	if cur := h.residents[turn.ThreadID]; cur != nil {
		cur.mu.Lock()
		switch {
		case cur.busy:
			// 同一线程上一轮还没收尾：这一轮用独立进程，不动会话表里的那个。
			cur.mu.Unlock()
			h.mu.Unlock()
			return nil, false, false, nil
		case !cur.dead && cur.key == spec.key && cur.sessionID == turn.SessionID && !h.expiredLocked(cur):
			cur.busy = true
			cur.turns++
			cur.resetSteerLocked()
			if cur.idle != nil {
				cur.idle.Stop()
				cur.idle = nil
			}
			cur.mu.Unlock()
			h.mu.Unlock()
			h.bump(func(s *ResidentStats) { s.Reuses++ })
			return cur, true, true, nil
		default:
			stale = cur
			delete(h.residents, turn.ThreadID)
			switch {
			case cur.dead:
				reason = "process-exited"
			case cur.key != spec.key:
				reason = "launch-changed"
			case cur.sessionID != turn.SessionID:
				reason = "session-mismatch"
			default:
				reason = "recycled"
			}
		}
		cur.mu.Unlock()
	}
	h.mu.Unlock()

	if stale != nil {
		h.closeResident(stale)
		h.noteRebuild(turn.ThreadID, reason)
	}

	p, err = h.spawnResident(turn, spec)
	if err != nil {
		return nil, false, false, err
	}
	p.mu.Lock()
	p.busy = true
	p.turns = 1
	p.lastUsed = time.Now()
	p.mu.Unlock()
	h.bump(func(s *ResidentStats) { s.Spawns++ })
	if stale == nil {
		h.logf("claude: resident spawn thread=%s pid=%d", turn.ThreadID, p.pid())
	}

	// 并发两轮同一线程时后启动的这个抢不到槽位：本轮退回独立进程，别把先进来的进程挤掉。
	h.mu.Lock()
	taken := h.residents[turn.ThreadID] != nil
	if !taken {
		h.residents[turn.ThreadID] = p
	}
	h.mu.Unlock()
	if taken {
		h.closeResident(p)
		return nil, false, false, nil
	}
	h.trimResident()
	return p, false, true, nil
}

// trimResident 把驻留进程数压回上限：回收最久没用的空闲进程，正在跑的不动。
// 全部都在忙时可以暂时超出上限，等它们跑完在 release 里再收。
func (h *Harness) trimResident() {
	limit := h.opts.MaxResident
	if limit <= 0 {
		limit = defaultMaxResident
	}
	var evicted []*residentProc
	h.mu.Lock()
	for len(h.residents) > limit {
		var (
			oldest *residentProc
			usedAt time.Time
		)
		for _, cand := range h.residents {
			cand.mu.Lock()
			// 后台任务还在跑、或自发输出还没有轮次接住时不能回收：收掉进程会连同这段输出一起丢掉。
			idle := !cand.busy && !cand.dead && cand.bgTasks <= 0 && !cand.unsolicited
			candidateUsedAt := cand.lastUsed
			cand.mu.Unlock()
			if !idle {
				continue
			}
			if oldest == nil || candidateUsedAt.Before(usedAt) {
				oldest, usedAt = cand, candidateUsedAt
			}
		}
		if oldest == nil {
			break
		}
		delete(h.residents, oldest.threadID)
		evicted = append(evicted, oldest)
	}
	h.mu.Unlock()
	for _, victim := range evicted {
		h.closeResident(victim)
	}
}

// expiredLocked 判断进程是否已满 MaxTurns 轮或存活满 MaxAge（调用方持有 p.mu）：到期后下一轮改用 --resume 重建。
func (h *Harness) expiredLocked(p *residentProc) bool {
	// 后台任务还在跑或自发输出尚未接住时不能换进程：重建会连同这段输出一起丢掉。
	if p.bgTasks > 0 || p.unsolicited {
		return false
	}
	if h.opts.MaxTurns > 0 && p.turns >= h.opts.MaxTurns {
		return true
	}
	return h.opts.MaxAge > 0 && time.Since(p.born) >= h.opts.MaxAge
}

// forgetResident 把进程从会话表里摘掉（进程已退出或本轮失败要回收）。
func (h *Harness) forgetResident(p *residentProc) {
	h.mu.Lock()
	if h.residents[p.threadID] == p {
		delete(h.residents, p.threadID)
	}
	h.mu.Unlock()
}

// armIdle 开始空闲计时；代数在每次重新计时时递增，回收回调据此判断自己是否已被复用刷新。
func (h *Harness) armIdle(p *residentProc) {
	p.mu.Lock()
	if p.idle != nil {
		p.idle.Stop()
	}
	p.gen++
	gen := p.gen
	p.idle = time.AfterFunc(h.opts.IdleTTL, func() { h.reapResident(p, gen) })
	p.mu.Unlock()
}

// reapResident 回收空闲超时的驻留进程；等待期间又被复用时什么都不做。
func (h *Harness) reapResident(p *residentProc, gen uint64) {
	h.mu.Lock()
	p.mu.Lock()
	if p.busy || p.dead || p.gen != gen {
		p.mu.Unlock()
		h.mu.Unlock()
		return
	}
	if p.bgTasks > 0 || p.unsolicited {
		// 后台任务还在跑或自发输出还没有轮次接住：先留着，解锁后重新开始空闲计时。
		p.idle = nil
		p.mu.Unlock()
		h.mu.Unlock()
		h.armIdle(p)
		return
	}
	p.idle = nil
	p.mu.Unlock()
	if h.residents[p.threadID] == p {
		delete(h.residents, p.threadID)
	}
	h.mu.Unlock()
	h.bump(func(s *ResidentStats) { s.Reaped++ })
	h.logf("claude: resident reaped thread=%s idle>=%s", p.threadID, h.opts.IdleTTL)
	h.closeResident(p)
}

// release 在一轮结束后处理常驻进程：keep 为真就留下并重新开始空闲计时，否则关闭并摘掉。
func (h *Harness) release(p *residentProc, keep bool) {
	if keep {
		h.mu.Lock()
		// harness 已被替换（Close/CloseIdle）：不再驻留，免得旧进程与新 harness 的一起占内存。
		keep = !h.closed
		h.mu.Unlock()
	}
	if !keep {
		h.forgetResident(p)
		h.closeResident(p)
		return
	}
	p.mu.Lock()
	p.busy = false
	p.drained = false
	p.resetSteerLocked()
	p.lastUsed = time.Now()
	p.mu.Unlock()
	h.armIdle(p)
	// 并发高峰时登记阶段可能全忙、没法回收，这一轮收尾后补收。
	h.trimResident()
}

// Close 关闭全部常驻进程并等它们退出，之后的轮次全部退回一轮一进程。可重复调用。
func (h *Harness) Close() error {
	h.mu.Lock()
	h.closed = true
	residents := make([]*residentProc, 0, len(h.residents))
	for _, p := range h.residents {
		residents = append(residents, p)
	}
	h.residents = make(map[string]*residentProc)
	h.mu.Unlock()
	for _, p := range residents {
		h.closeResident(p)
	}
	return nil
}

// CloseIdle 只释放空闲的常驻进程：正在执行的轮次不受影响，忙的进程在收尾归还时直接退出
// （harness 已被标记关闭，不再驻留）。供“替换 harness”这类场景使用（显式关闭仍走 Close）。
func (h *Harness) CloseIdle() {
	var idle []*residentProc
	h.mu.Lock()
	h.closed = true
	for threadID, p := range h.residents {
		p.mu.Lock()
		busy := p.busy
		p.mu.Unlock()
		if busy {
			continue
		}
		delete(h.residents, threadID)
		idle = append(idle, p)
	}
	h.mu.Unlock()
	for _, p := range idle {
		h.closeResident(p)
	}
}

// EvictThread 关闭指定线程驻留的空闲进程；它正在跑的那一轮不受影响（忙则不动）。
func (h *Harness) EvictThread(threadID string) {
	if threadID == "" {
		return
	}
	var victim *residentProc
	h.mu.Lock()
	if p := h.residents[threadID]; p != nil {
		p.mu.Lock()
		busy := p.busy
		p.mu.Unlock()
		if !busy {
			delete(h.residents, threadID)
			victim = p
		}
	}
	h.mu.Unlock()
	if victim != nil {
		h.closeResident(victim)
	}
}

// spawnResident 启动一个 `claude -p --input-format stream-json` 常驻进程：
// 审批服务与进程同生共死，stdout 交给唯一的读取协程。
func (h *Harness) spawnResident(turn harness.Turn, spec claudeLaunch) (*residentProc, error) {
	var cleanups []func()
	abandon := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}

	// 审批服务与进程同生共死：用不随单轮 ctx 取消的 ctx 启动，URL 与令牌固定写进 --mcp-config，
	// 每一轮只替换审批方。
	var (
		approval    *approvalServer
		approvalURL string
		approvalTok string
	)
	if approvalsActive(turn) {
		srvCtx, srvCancel := context.WithCancel(context.Background())
		srv, err := startApprovalServer(srvCtx, turn.Approver)
		if err != nil {
			srvCancel()
			return nil, fmt.Errorf("claude: start approval server: %w", err)
		}
		approval = srv
		approvalURL = srv.url()
		approvalTok = srv.token()
		cleanups = append(cleanups, func() {
			_ = srv.close()
			srvCancel()
		})
	}

	args := replaceApprovalEndpoint(spec.args, approvalURL, approvalTok)
	// 常驻进程改成从 stdin 逐行读 JSON 帧（一轮一帧），不再靠 -p 后面的位置参数拿 prompt。
	// --replay-user-messages 让 CLI 把已消费的 user 帧（带 uuid）回显到 stdout，steer 靠它判断
	// 插话是并入了当前轮还是排成了下一轮。
	args = append(args, "--input-format", "stream-json", "--replay-user-messages")
	// --mcp-config 的内联 JSON 里有 MCP 的 Bearer 令牌，放在 argv 里 ps 就能看到；
	// 改成 0600 临时文件、argv 只传路径，进程退出时删除。
	args, cleanupMCPConfig, err := externalizeMCPConfig(args)
	if err != nil {
		abandon()
		return nil, err
	}
	cleanups = append(cleanups, cleanupMCPConfig)

	// 进程的生命周期不属于任何一轮：用不会取消的 ctx 构造命令，取消一轮靠 interrupt 控制帧。
	var cmd *exec.Cmd
	if turn.Sandbox != nil {
		var cleanup func()
		cmd, cleanup, err = turn.Sandbox.Command(context.Background(), turn.WorkDir, turn.Mode, spec.binary, args...)
		if err != nil {
			abandon()
			return nil, fmt.Errorf("claude: sandbox command: %w", err)
		}
		if cleanup != nil {
			cleanups = append(cleanups, cleanup)
		}
	} else {
		cmd = procattr.HideWindow(exec.Command(spec.binary, args...))
	}

	// 与 runOnce 一致：独立进程组便于整树终止，WaitDelay 避免残留子进程占着管道让 Wait 卡住。
	setProcessGroup(cmd)
	cmd.WaitDelay = 3 * time.Second

	baseEnv := loginenv.Environ()
	if cmd.Env != nil {
		baseEnv = cmd.Env
	}
	cmd.Dir = turn.WorkDir
	cmd.Env = buildEnv(baseEnv, turn.WorkDir, turn.Env)
	_ = EnsureSharedLayoutFromEnv(cmd.Env)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		abandon()
		return nil, fmt.Errorf("claude: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		abandon()
		return nil, fmt.Errorf("claude: stdout pipe: %w", err)
	}
	stderrBuf := &tailBuffer{limit: 8192}
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		abandon()
		return nil, fmt.Errorf("claude: start: %w", err)
	}

	p := &residentProc{
		threadID: turn.ThreadID,
		key:      spec.key,
		cmd:      cmd,
		stdin:    stdin,
		stdout:   stdout,
		stderr:   stderrBuf,
		approval: approval,
		cleanups: cleanups,
		exited:   make(chan struct{}),
		// owner 供读取协程在自发输出时取上层登记的通知回调。
		owner: h,
		// 新进程带着 --resume 启动，会话 id 先按本轮的记下；system/init 到达后再校正。
		sessionID: turn.SessionID,
		lastUsed:  time.Now(),
		born:      time.Now(),
		applied:   spec.soft,
		// 进程一出生就属于这一轮：读取协程在 acquireResident 把 busy 置真之前就已经跑起来，
		// 若此时 busy 还是假，启动期的 system/init 会被当成自发轮次（见 deliver）。
		busy: true,
	}
	h.procs.Set(turn.ThreadID, p.pid())
	go p.readLoop(func() {
		h.procs.Clear(p.threadID, p.pid())
		h.forgetResident(p)
		var before time.Time
		h.mu.Lock()
		fn := h.processExitHandler
		if q := h.residents[p.threadID]; q != nil && q != p {
			before = q.born
		}
		h.mu.Unlock()
		if fn != nil {
			fn(p.threadID, before)
		}
	})
	return p, nil
}

// settingsApplyTimeout 是复用进程时等热切换控制帧应答的上限。
const settingsApplyTimeout = 10 * time.Second

// runOnResident 在常驻进程上跑一轮：挂接 stdout 行通道、（复用时）热切换设置、写一帧 user 消息，
// 然后读到 result 为止。收到 result 后进程留下继续服务下一轮；进程失效或超时按 runOnce 相同口径回报失败。
// 轮中有 steer 插话时：并入当前轮的（回显出现在 result 之前）照常收尾；排成下一轮的（result 时回显
// 还没出现）把两个 result 串成同一轮，只在最后一个 result 上报完成。
func (h *Harness) runOnResident(ctx context.Context, p *residentProc, turn harness.Turn, spec claudeLaunch, emit func(harness.Event), reused bool) error {
	keep := false
	defer func() { h.release(p, keep) }()

	// 审批方随轮次更换：HTTP 处理协程在轮内读取它，轮结束置空（nil 时保持既有的“一律拒绝”行为）。
	p.setApprover(turn.Approver)
	defer p.setApprover(nil)

	// 复用的进程不会再发 system/init，这里补一次会话事件，保证上层照旧拿到会话 id。
	if reused {
		if sessionID := p.session(); sessionID != "" {
			_ = harness.Emit(emit, harness.Event{Type: harness.EventSession, SessionID: sessionID})
		}
	}

	sink, backlog, unsolicitedDone, pending := p.attach()
	defer p.detach(sink)

	parser := newParser()
	// chained 表示这一个 result 到达时还有 steer 排在后面，本轮要继续读下一个 result。
	chained, resultErrored := false, false
	// drainedDone/drainedStop 记住排空阶段被吞掉的那个 EventDone：attach 轮要在返回前补报一次。
	drainedDone, drainedStop := false, ""
	// draining 为真时吞掉排空阶段到达的 EventDone：自发输出并入本轮，整轮最多只上报一个结束事件。
	draining := pending || len(backlog) > 0 || unsolicitedDone
	out := func(e harness.Event) {
		switch e.Type {
		case harness.EventSession:
			// 新进程要在首轮从 system/init 学到会话 id，供下一轮判断能否复用。
			if e.SessionID != "" {
				p.setSession(e.SessionID)
			}
		case harness.EventError:
			resultErrored = true
			if e.ErrKind == harness.ErrKindAuthRequired && UsesThirdPartyAPI(p.cmd.Env) {
				e.ErrKind = harness.ErrKindAPIKeyRejected
			}
		case harness.EventDone:
			errored := resultErrored
			resultErrored = false
			if draining {
				// 这一段是排空阶段的收尾，不单独上报：本轮结束时再报 EventDone。
				drainedDone, drainedStop = true, e.StopReason
				return
			}
			if p.endOrChain(errored) {
				chained = true
				return
			}
		}
		emit(e)
	}

	// attach 轮接的是一段已经开始的输出：手上已经有行（缓存或 result 已到）时按轮内停顿口径等待，
	// 什么都没有时按首输出口径等待。
	firstWait := h.opts.firstOutputTimeout()
	if turn.Attach && (len(backlog) > 0 || unsolicitedDone) {
		firstWait = h.opts.stallTimeout()
	}
	timer := time.NewTimer(firstWait)
	defer timer.Stop()

	sawOutput := false
	if draining {
		// 排空自发输出：先处理没有轮次接收时缓存下来的行，再按需要从 sink 读到它的 result。
		// 这一段 CLI 已经在跑了，steer 照常可以注入。
		p.openSteer()
		// unsolicitedDone 为真时 result 已经到过（可能因超限被丢），排空到 backlog 末尾即结束，不再等 sink。
		drained := unsolicitedDone
		for i, line := range backlog {
			sawOutput = true
			if p.noteReplayEcho(line) {
				continue
			}
			_ = parser.handleLine(line, out)
			if !parser.sawResult {
				continue
			}
			if i < len(backlog)-1 {
				// 中间那一段的收尾：换新解析器继续，后面的行同样并入本轮。
				parser = newParser()
				continue
			}
			drained = true
		}
		for !drained {
			select {
			case line, ok := <-sink.ch:
				if !ok {
					// 进程已退出：这一轮没有正常收尾。
					return residentExitFailure(p, sawOutput)
				}
				sawOutput = true
				p.openSteer()
				if p.noteReplayEcho(line) {
					resetTimer(timer, h.opts.stallTimeout())
					continue
				}
				_ = parser.handleLine(line, out)
				if parser.sawResult {
					drained = true
					break
				}
				resetTimer(timer, h.opts.stallTimeout())
			case <-timer.C:
				h.killResident(p)
				// 这一段已经在跑了（CLI 自己开的），超时口径按「轮内停顿」算，与首输出无关。
				msg := fmt.Sprintf("claude process produced no output while draining unsolicited output within %s", firstWait)
				return residentTimeoutFailure(p, msg)
			case <-ctx.Done():
				p.closeSteer()
				keep = h.interruptResident(p, sink, parser, emit)
				return ctx.Err()
			}
		}
		draining = false
		p.clearUnsolicited()
		// 排空阶段已经用过解析器：换新的，写用户帧之后从干净状态重新开始。
		parser = newParser()
		resetTimer(timer, h.opts.firstOutputTimeout())
	}

	// 复用时先把模型、思考强度、权限模式热切换到本轮想要的值，应答齐了再写用户帧。
	// 排空要排在前面：自发输出的行不能被这里当成无关应答吞掉。attach 轮不写用户帧，也就不切设置。
	if !turn.Attach && reused && spec.hot {
		if err := h.applySettings(ctx, p, sink, spec.soft); err != nil {
			if ctx.Err() != nil {
				// 还没写用户帧，进程仍是空闲状态，留着下一轮用。
				keep = true
				return ctx.Err()
			}
			h.killResident(p)
			h.noteRebuild(turn.ThreadID, "apply-failed")
			return residentApplyFailure(p, err)
		}
		resetTimer(timer, h.opts.firstOutputTimeout())
	}

	if turn.Attach {
		// 不写用户帧：这一段输出就是本轮的全部内容，进程照常留给下一轮复用。
		if drainedDone {
			// 排空阶段吞掉的那个 EventDone 在这里补报，自发轮次同样只上报一个结束事件。
			_ = harness.Emit(emit, harness.Event{Type: harness.EventDone, StopReason: drainedStop})
		}
		keep = !p.takeOrphan()
		p.markDrained(sink)
		return nil
	}

	// 写 stdin 放在独立协程：prompt 可能超过管道缓冲，CLI 不读 stdin 时阻塞在这里也不能让
	// 输出看门狗和取消失效。图片以 image 块与文本一起进同一条 user 帧。
	writeDone := make(chan error, 1)
	go func() {
		err := p.sendUserFrame(buildUserContent(turn))
		if err == nil {
			// 用户帧已经写进 stdin：上报本轮已被收下（Run 层保证每轮只报一次），
			// 且在管道写完之后、CLI 可能产出正文之前上报。
			emitTurnAccepted(emit)
		}
		writeDone <- err
	}()

	for {
		select {
		case err := <-writeDone:
			writeDone = nil
			if err != nil {
				return residentWriteFailure(p, err)
			}
			// 用户帧已经写进去：此后的 steer 帧不会排到它前面。
			p.openSteer()
		case line, ok := <-sink.ch:
			if !ok {
				// 进程已退出：本轮没有正常收尾。
				return residentExitFailure(p, sawOutput)
			}
			sawOutput = true
			p.openSteer()
			if p.noteReplayEcho(line) {
				resetTimer(timer, h.opts.stallTimeout())
				continue
			}
			_ = parser.handleLine(line, out)
			if parser.sawResult {
				if chained {
					// 排队的 steer 已经在路上：换新解析器等它的回显与 result，没有任何输出就按首输出超时处理。
					chained = false
					parser = newParser()
					resetTimer(timer, h.opts.firstOutputTimeout())
					continue
				}
				// 收到 result 立即结束本轮，不再等排空；排队中的 steer 没有被消费时进程不能留。
				keep = !p.takeOrphan()
				p.markDrained(sink)
				return nil
			}
			resetTimer(timer, h.opts.stallTimeout())
		case <-timer.C:
			h.killResident(p)
			msg := fmt.Sprintf("claude process produced no output within %s", h.opts.firstOutputTimeout())
			return residentTimeoutFailure(p, msg)
		case <-ctx.Done():
			// 先确认用户帧已经写完（或写失败），否则 interrupt 可能抢在 user 帧之前到达 CLI，
			// 被打断的就成了上一轮，本轮用户帧则在取消之后才开启一轮，造成错位。
			if writeDone != nil {
				waitWrite := time.NewTimer(h.opts.interruptGrace())
				select {
				case <-writeDone:
				case <-waitWrite.C:
				}
				waitWrite.Stop()
				writeDone = nil
			}
			p.closeSteer()
			keep = h.interruptResident(p, sink, parser, emit)
			return ctx.Err()
		}
	}
}

// applySettings 把复用进程的模型、思考强度、权限模式切到本轮想要的值，并等控制帧应答。
// 权限模式每轮都发一次：CLI 自己会改（EnterPlanMode / ExitPlanMode），记录的值未必还准。
func (h *Harness) applySettings(ctx context.Context, p *residentProc, sink *lineSink, want softSettings) error {
	p.mu.Lock()
	have := p.applied
	p.mu.Unlock()

	pending := make(map[string]func(*softSettings))
	send := func(subtype string, fields map[string]any, done func(*softSettings)) error {
		id, err := p.sendControl(subtype, fields)
		if err != nil {
			return err
		}
		pending[id] = done
		return nil
	}
	switched := false
	if want.model != "" && want.model != have.model {
		switched = true
		if err := send("set_model", map[string]any{"model": want.model}, func(s *softSettings) { s.model = want.model }); err != nil {
			return err
		}
	}
	if want.effort != "" && want.effort != have.effort {
		switched = true
		fields := map[string]any{"settings": map[string]any{"effortLevel": want.effort}}
		if err := send("apply_flag_settings", fields, func(s *softSettings) { s.effort = want.effort }); err != nil {
			return err
		}
	}
	if want.perm != have.perm {
		switched = true
	}
	if err := send("set_permission_mode", map[string]any{"mode": want.perm}, func(s *softSettings) { s.perm = want.perm }); err != nil {
		return err
	}
	if switched {
		h.bump(func(s *ResidentStats) { s.HotSwitches++ })
		h.logf("claude: resident hot switch thread=%s model=%s effort=%s perm=%s", p.threadID, want.model, want.effort, want.perm)
	}

	timer := time.NewTimer(settingsApplyTimeout)
	defer timer.Stop()
	for len(pending) > 0 {
		select {
		case line, ok := <-sink.ch:
			if !ok {
				return errors.New("process exited while applying settings")
			}
			id, success, reason, isResp := parseControlResponse(line)
			if !isResp {
				// 切换设置期间 CLI 还会吐 system/status、local-command 回显之类，与本轮无关。
				continue
			}
			done, known := pending[id]
			if !known {
				continue
			}
			if !success {
				return fmt.Errorf("control request rejected: %s", reason)
			}
			p.mu.Lock()
			done(&p.applied)
			p.mu.Unlock()
			delete(pending, id)
		case <-timer.C:
			return errors.New("timed out applying settings")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// parseControlResponse 解析 CLI 对控制帧的应答行；isResp 为假表示这不是应答。
func parseControlResponse(line []byte) (id string, success bool, reason string, isResp bool) {
	if !bytes.Contains(line, []byte(`"control_response"`)) {
		return "", false, "", false
	}
	var msg struct {
		Type     string `json:"type"`
		Response struct {
			Subtype   string `json:"subtype"`
			RequestID string `json:"request_id"`
			Error     string `json:"error"`
		} `json:"response"`
	}
	if err := json.Unmarshal(line, &msg); err != nil || msg.Type != "control_response" {
		return "", false, "", false
	}
	return msg.Response.RequestID, msg.Response.Subtype == "success", msg.Response.Error, true
}

// interruptResident 处理一轮中途被取消：发 interrupt 控制帧，用 InterruptGrace 等被打断那一轮
// 的 result 收尾。返回 true 表示进程已经回到空闲、可以留下。
// 被打断那一轮的收尾事件（is_error 的 EventError、EventDone）不报给上层：
// 上层已经按取消处理本轮，多一个结束事件会把它当成失败或正常完成；已经产出的文本与用量照常上报。
// 仍有排队中的 steer 没被消费时进程不能留：它会在下一轮之前白跑一轮，输出还会串到下一轮。
func (h *Harness) interruptResident(p *residentProc, sink *lineSink, parser *parser, emit func(harness.Event)) bool {
	// interrupt 帧独立协程写入：上一帧若卡在写满的管道上，这里也不能跟着卡死（等不到就杀进程）。
	go func() { _ = p.sendInterrupt() }()

	filtered := func(e harness.Event) {
		if e.Type == harness.EventError || e.Type == harness.EventDone {
			return
		}
		emit(e)
	}
	timer := time.NewTimer(h.opts.interruptGrace())
	defer timer.Stop()
	for {
		select {
		case line, ok := <-sink.ch:
			if !ok {
				return false
			}
			if p.noteReplayEcho(line) {
				continue
			}
			_ = parser.handleLine(line, filtered)
			if parser.sawResult {
				return !p.hasPendingSteers()
			}
		case <-timer.C:
			// 打断后没能收尾：进程状态不可信，强杀并让本轮回收它。
			h.killResident(p)
			return false
		}
	}
}

// Steer 把一条用户消息注入正在执行的轮次（harness.Steerer）。消息以带 uuid 的 user 帧写进常驻进程，
// CLI 在工具执行阶段会把它并入当前轮，在文本生成阶段则排成下一轮——后者由 Run 把两个 result 串成同一轮。
// 该会话没有处于可注入状态的常驻轮次时返回 harness.ErrNotRunning，调用方退回排队。
func (h *Harness) Steer(ctx context.Context, sessionID string, text string) error {
	if sessionID == "" {
		return harness.ErrNotRunning
	}
	var target *residentProc
	h.mu.Lock()
	for _, p := range h.residents {
		if p.session() == sessionID {
			target = p
			break
		}
	}
	h.mu.Unlock()
	if target == nil {
		return harness.ErrNotRunning
	}
	uuid := newFrameUUID()
	if !target.beginSteer(uuid) {
		return harness.ErrNotRunning
	}
	if err := target.sendUserFrameWithUUID(text, uuid); err != nil {
		target.cancelSteer(uuid)
		return fmt.Errorf("claude: steer: %w", err)
	}
	h.bump(func(s *ResidentStats) { s.Steers++ })
	h.logf("claude: resident steer thread=%s", target.threadID)
	return nil
}

// newFrameUUID 生成 user 帧的 uuid（v4）；CLI 在 --replay-user-messages 下按它回显。
func newFrameUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 随机源不可用时退回时间戳，只要同一进程内不重复即可。
		return fmt.Sprintf("mcode-%d", time.Now().UnixNano())
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// replayEchoUUID 识别 --replay-user-messages 回显的 user 帧并返回它的 uuid。
func replayEchoUUID(line []byte) (string, bool) {
	if !bytes.Contains(line, []byte(`"isReplay"`)) {
		return "", false
	}
	var msg struct {
		Type     string `json:"type"`
		UUID     string `json:"uuid"`
		IsReplay bool   `json:"isReplay"`
	}
	if err := json.Unmarshal(line, &msg); err != nil || msg.Type != "user" || !msg.IsReplay {
		return "", false
	}
	return msg.UUID, true
}

// killResident 强杀进程组并关掉读端，让读取协程立刻收尾。
func (h *Harness) killResident(p *residentProc) {
	p.mu.Lock()
	p.dead = true
	p.mu.Unlock()
	_ = killProcessGroup(p.cmd, syscall.SIGKILL)
	_ = p.stdout.Close()
}

// closeResident 关闭一个常驻进程并等它收尾：先关 stdin 让 CLI 自己退出，gracefulCloseWait 内
// 没退出就按进程组 SIGKILL 并关掉读端；审批服务、MCP 配置临时文件与沙箱资源由读取协程统一清理。
// 可重复调用；调用方不要持有会话表锁（这里会等进程退出）。
func (h *Harness) closeResident(p *residentProc) {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		if p.idle != nil {
			p.idle.Stop()
			p.idle = nil
		}
		p.dead = true
		p.mu.Unlock()
		go func() {
			_ = p.stdin.Close()
			select {
			case <-p.exited:
			case <-time.After(gracefulCloseWait):
				_ = killProcessGroup(p.cmd, syscall.SIGKILL)
				_ = p.stdout.Close()
			}
		}()
	})
	<-p.exited
	h.procs.Clear(p.threadID, p.pid())
}

// residentWriteFailure 把“本轮的用户帧没写进 CLI stdin”包装成失败：进程已失效且这一轮还没产出
// 任何输出，可以用 --resume 重启并重发同一 prompt。
func residentWriteFailure(p *residentProc, err error) error {
	p.mu.Lock()
	p.dead = true
	p.mu.Unlock()
	base := fmt.Errorf("claude: write prompt: %w", err)
	report := base.Error()
	if tail := strings.TrimSpace(p.stderr.String()); tail != "" {
		report += " (stderr: " + tail + ")"
	}
	return &processFailure{err: base, report: report, retryable: true}
}

// residentExitFailure 把“常驻进程本轮读取中途退出”包装成失败，口径与 runOnce 一致：
// 已经产出过输出的不再重试（重跑会重放工具副作用），没有任何输出的才可以重启重试。
func residentExitFailure(p *residentProc, sawOutput bool) error {
	tail := strings.TrimSpace(p.stderr.String())
	waitErr := p.wait()
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
	// 与 runOnce 同口径：没有任何输出，且 stderr 为空或进程被信号杀死（空闲后被回收、被外部终止）
	// 才重试；带着 stderr 报错退出（鉴权失败、参数错误之类）重试也不会好，直接上报。
	retryable := !sawOutput && (tail == "" || processSignaled(waitErr))
	return &processFailure{err: base, report: report, retryable: retryable}
}

// residentApplyFailure 把“热切换设置失败”包装成可重试的失败：进程已被杀掉，重试会用本轮参数重建。
func residentApplyFailure(p *residentProc, cause error) error {
	base := fmt.Errorf("claude: apply settings: %w", cause)
	report := base.Error()
	if tail := strings.TrimSpace(p.stderr.String()); tail != "" {
		report += " (stderr: " + tail + ")"
	}
	return &processFailure{err: base, report: report, retryable: true}
}

// residentTimeoutFailure 把输出看门狗超时包装成失败：消息与 stderr 尾部的口径同 runOnce。
// 用户帧已经写进 CLI（写失败走另一条分支），因此不重试，避免把已经开始执行的轮次跑两遍。
func residentTimeoutFailure(p *residentProc, msg string) error {
	tail := strings.TrimSpace(p.stderr.String())
	errMsg := msg
	if tail != "" {
		errMsg += " (stderr: " + tail + ")"
	}
	return &processFailure{err: errors.New(errMsg), report: errMsg}
}

// resetTimer 重置计时器：先停表并排掉可能已触发的信号，再按新时长重新计时。
func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

// lineSink 是当前这一轮接收 stdout 行的通道：ch 由读取协程在 EOF 时关闭，quit 在轮次结束时
// 关闭，让读取协程不会卡在一个已经没人读的通道上。
type lineSink struct {
	ch      chan []byte
	quit    chan struct{}
	drained chan struct{}
}

// stdinUserFrame 是写到 CLI stdin 的一帧用户消息。stream-json 输入每行一帧，
// 四个字段都要有：本轮用户消息没有父工具调用，parent_tool_use_id 为 null。
// UUID 非空时 CLI 在 --replay-user-messages 下会带着它回显这条消息（steer 靠它对账）。
type stdinUserFrame struct {
	Type            string           `json:"type"`
	UUID            string           `json:"uuid,omitempty"`
	Message         stdinUserMessage `json:"message"`
	ParentToolUseID *string          `json:"parent_tool_use_id"`
}

type stdinUserMessage struct {
	Role string `json:"role"`
	// Content 是纯文本或 text+image 块数组（见 buildUserContent）：无图片时仍是字符串，
	// 序列化结果与改造前完全一致。
	Content any `json:"content"`
}

// controlSeq 为控制帧生成不重复的 request_id。
var controlSeq atomic.Uint64

type controlResult struct {
	success bool
	reason  string
}

// residentProc 是一个驻留的 `claude -p --input-format stream-json` 子进程。
type residentProc struct {
	threadID string
	// key 是启动指纹：指纹不同的轮次不能复用这个进程（模型、权限模式、MCP 配置等变了）。
	key    string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr *tailBuffer
	// approval 是这个进程专用的审批服务，与进程同生共死，URL/令牌已写进启动参数。
	approval *approvalServer
	// cleanups 在进程退出后统一执行（审批服务、MCP 配置临时文件、沙箱资源）。
	cleanups []func()
	// owner 是该进程所属的 harness：自发输出时用它取上层登记的通知回调。
	owner *Harness

	// exited 在读取协程收尾（含 Wait 与清理）之后关闭，waitErr 此后可读。
	exited  chan struct{}
	waitErr error

	writeMu    sync.Mutex
	closeOnce  sync.Once
	finishOnce sync.Once

	// 以下字段由 mu 保护。
	mu        sync.Mutex
	sessionID string
	dead      bool
	busy      bool
	// drained 表示本轮消费者已收到结束本轮的 result，不再读 sink。
	drained        bool
	lastUsed       time.Time
	idle           *time.Timer
	gen            uint64
	sink           *lineSink
	controlWaiters map[string]chan controlResult
	// bgTasks 是 CLI 侧仍在跑的后台任务数：system/task_started 加一，system/task_notification 减一。
	// 大于零时不能回收或重建进程，否则后台任务会连同它的输出一起丢掉。
	bgTasks int
	// unsolicited 为真表示进程在没有轮次挂接时开始了一段自发输出（后台任务结束后的续跑），
	// 这一段还没有被 Turn.Attach 的轮次接住。
	unsolicited bool
	// unsolicitedDone 为真表示自发输出的 result 已经在没有 sink 时到达：排空不必再等 sink。
	unsolicitedDone bool
	// backlog 是自发输出期间没有轮次接收时缓存下来的行，attach 时原子地交给排空方。
	backlog [][]byte
	// backlogDropped 记录 backlog 是否因超过上限丢过行：超限只记一次日志。
	backlogDropped bool
	// turns 是这个进程已服务的轮数，born 是启动时间；MaxTurns/MaxAge 据此回收长寿进程。
	turns int
	born  time.Time
	// applied 是进程当前生效的可热切换设置，复用时与本轮想要的对比。
	applied softSettings
	// steerOpen 为真时这一轮可以接受 steer 插话；pendingSteers 是已写入但还没看到回显的 steer uuid；
	// orphan 表示本轮结束时仍有排队中的 steer（再留着进程会让它在下一轮之前白跑一轮）。
	steerOpen     bool
	pendingSteers []string
	orphan        bool
}

// readLoop 是进程 stdout 的唯一读取者：按行送到当前轮挂接的通道，空闲时直接丢弃；
// EOF 之后回收进程、跑清理并关闭 exited，onExit 用来把进程从会话表与 PID 表里摘掉。
func (p *residentProc) readLoop(onExit func()) {
	reader := bufio.NewReader(p.stdout)
	for {
		raw, err := reader.ReadBytes('\n')
		if line := bytes.TrimSpace(raw); len(line) > 0 {
			p.deliver(line)
		}
		if err != nil {
			break
		}
	}
	p.waitErr = p.cmd.Wait()

	p.mu.Lock()
	p.dead = true
	p.controlWaiters = nil
	sink := p.sink
	p.sink = nil
	p.mu.Unlock()
	if sink != nil {
		close(sink.ch)
	}
	p.finish()
	close(p.exited)
	if onExit != nil {
		onExit()
	}
}

// SetUnsolicitedTurnHandler 登记常驻进程自发产出输出时的通知回调（harness.UnsolicitedTurnNotifier）。
// 回调在线程空闲、CLI 自己开始新一轮时触发一次，参数是线程 ID；传 nil 关闭通知，行为回到空闲时
// 直接丢弃输出。
func (h *Harness) SetUnsolicitedTurnHandler(fn func(threadID string)) {
	h.mu.Lock()
	h.unsolicitedHandler = fn
	h.mu.Unlock()
}

// unsolicitedHandlerNow 返回当前登记的回调快照。调用方不能持有 p.mu 时调用：锁序是先 h.mu 后 p.mu。
func (h *Harness) unsolicitedHandlerNow() func(threadID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.unsolicitedHandler
}

// SetProcessExitHandler 登记常驻进程退出时的通知回调（harness.ProcessExitNotifier）。
// 回调在进程退出、之后不会再为该会话产出事件时触发一次，参数是线程 ID 与替代进程出生时间（无替代时为零值）；传 nil 关闭通知。
func (h *Harness) SetProcessExitHandler(fn func(threadID string, before time.Time)) {
	h.mu.Lock()
	h.processExitHandler = fn
	h.mu.Unlock()
}

// processExitHandlerNow 返回当前登记的退出回调快照。锁序同 unsolicitedHandlerNow：先 h.mu 后 p.mu。
func (h *Harness) processExitHandlerNow() func(threadID string, before time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.processExitHandler
}

// finish 执行进程级清理（审批服务、MCP 配置临时文件、沙箱资源），只跑一次。
func (p *residentProc) finish() {
	p.finishOnce.Do(func() {
		for i := len(p.cleanups) - 1; i >= 0; i-- {
			p.cleanups[i]()
		}
	})
}

// parseStdoutFrame 解析 deliver 需要关心的帧字段。只有 type 可能是 system 或 result 的行才做
// JSON 解析：stream_event、assistant 这些行每行都解析太费，而且与本处记账无关。
func parseStdoutFrame(line []byte) (typ, subtype string) {
	if !bytes.Contains(line, []byte(`"system"`)) && !bytes.Contains(line, []byte(`"result"`)) {
		return "", ""
	}
	var frame struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal(line, &frame); err != nil {
		return "", ""
	}
	return frame.Type, frame.Subtype
}

// deliver 把一行 stdout 交给当前轮；没有轮挂接（空闲）时原本直接丢弃，现在额外做两件事：
// 按 task_started/task_notification 计数后台任务（空闲回收与换进程据此让路），并在 CLI 自己跑出
// 新一轮（后台任务结束后的续跑）时通知上层补一轮，把这一段行先缓存下来喂给那个轮次。
// 没有登记通知回调时行为与以前完全一致：空闲时的输出直接丢弃。
//
// 启动期 busy 为真且尚未 attach 时，system/init 不是自发轮次，不能触发通知；
// 本轮 result 已被消费后则按空闲状态处理，后台任务计数始终有效。
func (p *residentProc) deliver(line []byte) {
	if bytes.Contains(line, []byte(`"control_response"`)) {
		id, success, reason, isResp := parseControlResponse(line)
		if isResp {
			p.mu.Lock()
			waiter := p.controlWaiters[id]
			delete(p.controlWaiters, id)
			p.mu.Unlock()
			if waiter != nil {
				select {
				case waiter <- controlResult{success: success, reason: reason}:
				default:
				}
				return
			}
		}
	}
	typ, subtype := parseStdoutFrame(line)
	candidate := typ == "system" && (subtype == "task_notification" || subtype == "init")
	// 只有候选帧才去取回调快照：避免每一行 stdout 都去抢 harness 的表锁。
	var handler func(threadID string)
	if candidate && p.owner != nil {
		handler = p.owner.unsolicitedHandlerNow()
	}

	notify, dropped := false, false
	p.mu.Lock()
	switch {
	case typ == "system" && subtype == "task_started":
		p.bgTasks++
	case typ == "system" && subtype == "task_notification" && p.bgTasks > 0:
		p.bgTasks--
	}
	sink := p.sink
	if sink == nil || p.drained {
		notify, dropped = p.deliverIdleLocked(line, handler)
		sink = nil
	}
	p.mu.Unlock()

	// 回调必须在锁外、新协程里跑：它最终会回来开一轮并调用 acquireResident（锁序 h.mu → p.mu）。
	if notify {
		go handler(p.threadID)
	}
	if dropped && p.owner != nil {
		p.owner.logf("claude: resident thread=%s unsolicited backlog capped at %d lines", p.threadID, unsolicitedBacklogLimit)
	}
	if sink == nil {
		return
	}
	select {
	case sink.ch <- line:
		// drained 关闭与发送同时就绪时，select 仍可能选中发送，须再次搬走该行。
		p.markDrainedIfNeeded(sink, handler)
	case <-sink.quit:
		p.mu.Lock()
		if p.drained {
			notify, dropped = p.deliverIdleLocked(line, handler)
		}
		p.mu.Unlock()
		p.reportIdle(handler, notify, dropped)
	case <-sink.drained:
		p.mu.Lock()
		notify, dropped = p.deliverIdleLocked(line, handler)
		p.mu.Unlock()
		p.reportIdle(handler, notify, dropped)
	}
}

// deliverIdleLocked 在持有 p.mu 时缓存空闲输出，返回是否需要通知或记录超限。
func (p *residentProc) deliverIdleLocked(line []byte, handler func(string)) (notify, dropped bool) {
	typ, subtype := parseStdoutFrame(line)
	idle := !(p.busy && !p.drained)
	if typ == "result" && idle {
		p.unsolicitedDone = true
	}
	if typ == "system" && (subtype == "task_notification" || subtype == "init") && idle && !p.unsolicited && handler != nil && p.threadID != "" {
		p.unsolicited = true
		notify = true
	}
	if p.unsolicited {
		switch {
		case len(p.backlog) < unsolicitedBacklogLimit:
			p.backlog = append(p.backlog, append([]byte(nil), line...))
		case !p.backlogDropped:
			p.backlogDropped = true
			dropped = true
		}
	}
	return
}

// reportIdle 在锁外派发自发输出通知和超限日志。
func (p *residentProc) reportIdle(handler func(string), notify, dropped bool) {
	if notify {
		go handler(p.threadID)
	}
	if dropped && p.owner != nil {
		p.owner.logf("claude: resident thread=%s unsolicited backlog capped at %d lines", p.threadID, unsolicitedBacklogLimit)
	}
}

// markDrained 标记本轮结束，并按顺序转移 sink 中尚未消费的行。
func (p *residentProc) markDrained(sink *lineSink) {
	var handler func(string)
	if p.owner != nil {
		handler = p.owner.unsolicitedHandlerNow()
	}
	p.mu.Lock()
	if !p.drained {
		p.drained = true
		close(sink.drained)
	}
	notify, dropped := p.drainSinkLocked(sink, handler)
	p.mu.Unlock()
	p.reportIdle(handler, notify, dropped)
}

// markDrainedIfNeeded 处理发送与 drained 同时就绪时留在旧 sink 的行。
func (p *residentProc) markDrainedIfNeeded(sink *lineSink, handler func(string)) {
	p.mu.Lock()
	var notify, dropped bool
	select {
	case <-sink.drained:
		notify, dropped = p.drainSinkLocked(sink, handler)
	default:
	}
	p.mu.Unlock()
	p.reportIdle(handler, notify, dropped)
}

func (p *residentProc) drainSinkLocked(sink *lineSink, handler func(string)) (notify, dropped bool) {
	for {
		select {
		case line, ok := <-sink.ch:
			if !ok {
				return
			}
			n, d := p.deliverIdleLocked(line, handler)
			notify, dropped = notify || n, dropped || d
		default:
			return
		}
	}
}

// attach 把当前轮登记为 stdout 行的接收方，并原子地取走自发输出期间缓存下来的行、
// 「result 已到达」标记与「正在自发输出」标记，交给调用方先排空。取走 backlog 与设置 sink 在
// 同一个临界区里完成，结束后到达的行直接进 sink，因此排空不会丢行也不会乱序。
// 进程已死时返回一个已关闭的通道，调用方会立刻按“本轮没有任何输出”处理。
func (p *residentProc) attach() (sink *lineSink, backlog [][]byte, unsolicitedDone, pending bool) {
	sink = &lineSink{ch: make(chan []byte, sinkBuffer), quit: make(chan struct{}), drained: make(chan struct{})}
	p.mu.Lock()
	p.drained = false
	backlog = p.backlog
	p.backlog = nil
	p.backlogDropped = false
	unsolicitedDone = p.unsolicitedDone
	p.unsolicitedDone = false
	pending = p.unsolicited
	if p.dead {
		close(sink.ch)
	} else {
		p.sink = sink
	}
	p.mu.Unlock()
	return sink, backlog, unsolicitedDone, pending
}

// clearUnsolicited 在排空结束后清掉自发输出状态。
func (p *residentProc) clearUnsolicited() {
	p.mu.Lock()
	p.unsolicited = false
	p.unsolicitedDone = false
	p.mu.Unlock()
}

// detach 注销当轮的接收方，之后到达的行被丢弃。
func (p *residentProc) detach(sink *lineSink) {
	p.mu.Lock()
	if p.sink == sink {
		p.sink = nil
	}
	p.mu.Unlock()
	close(sink.quit)
}

// sendFrame 在写锁内把一帧 JSON 写进 CLI stdin。
func (p *residentProc) sendFrame(frame any) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_, err = p.stdin.Write(data)
	return err
}

// sendUserFrame 写入本轮的用户消息帧；content 是 prompt 原文或 text+image 块数组。
func (p *residentProc) sendUserFrame(content any) error {
	return p.sendUserFrameWithUUID(content, "")
}

// sendUserFrameWithUUID 写入带 uuid 的用户消息帧（steer 用，content 始终是纯文本）。
func (p *residentProc) sendUserFrameWithUUID(content any, uuid string) error {
	return p.sendFrame(stdinUserFrame{
		Type:    "user",
		UUID:    uuid,
		Message: stdinUserMessage{Role: "user", Content: content},
	})
}

// sendControl 写入一帧控制请求并返回它的 request_id，CLI 会回一行同 id 的 control_response。
func (p *residentProc) sendControl(subtype string, fields map[string]any) (string, error) {
	id := fmt.Sprintf("mcode-%s-%d", subtype, controlSeq.Add(1))
	return id, p.sendControlWithID(id, subtype, fields)
}

func (p *residentProc) sendControlWithID(id, subtype string, fields map[string]any) error {
	request := map[string]any{"subtype": subtype}
	for k, v := range fields {
		request[k] = v
	}
	return p.sendFrame(map[string]any{"type": "control_request", "request_id": id, "request": request})
}

// sendInterrupt 写入打断控制帧：CLI 回一行 control_response，被打断的那一轮补一个 result。
func (p *residentProc) sendInterrupt() error {
	_, err := p.sendControl("interrupt", nil)
	return err
}

// steer 状态：以下方法都在 p.mu 下维护 steerOpen / pendingSteers / orphan。

// resetSteerLocked 在新一轮开始时清空 steer 状态（调用方持有 p.mu）。
func (p *residentProc) resetSteerLocked() {
	p.steerOpen = false
	p.pendingSteers = nil
	p.orphan = false
}

// openSteer 在本轮用户帧写出后允许 steer 注入。
func (p *residentProc) openSteer() {
	p.mu.Lock()
	if p.busy && !p.dead {
		p.steerOpen = true
	}
	p.mu.Unlock()
}

// closeSteer 关闭 steer 入口（本轮正在收尾或被取消）。
func (p *residentProc) closeSteer() {
	p.mu.Lock()
	p.steerOpen = false
	p.mu.Unlock()
}

// beginSteer 登记一条即将写入的 steer；本轮不在可注入状态时返回 false。
func (p *residentProc) beginSteer(uuid string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.busy || p.dead || !p.steerOpen {
		return false
	}
	p.pendingSteers = append(p.pendingSteers, uuid)
	return true
}

// cancelSteer 撤销一条没能写出的 steer。
func (p *residentProc) cancelSteer(uuid string) {
	p.mu.Lock()
	p.removePendingLocked(uuid)
	p.mu.Unlock()
}

func (p *residentProc) removePendingLocked(uuid string) {
	for i, cur := range p.pendingSteers {
		if cur == uuid {
			p.pendingSteers = append(p.pendingSteers[:i:i], p.pendingSteers[i+1:]...)
			return
		}
	}
}

// noteReplayEcho 处理 --replay-user-messages 的回显行：回显出现说明对应的 steer 已被 CLI 消费。
// 返回 true 表示这一行是回显，不必再交给解析器。
func (p *residentProc) noteReplayEcho(line []byte) bool {
	uuid, ok := replayEchoUUID(line)
	if !ok {
		return false
	}
	p.mu.Lock()
	p.removePendingLocked(uuid)
	p.mu.Unlock()
	return true
}

// endOrChain 在一个 result 到达时决定本轮是否继续：还有排队中的 steer（回显没出现）且这个 result
// 不是错误，就继续等下一个 result；否则关闭 steer 入口，并标记仍有排队 steer 的进程不可留。
func (p *residentProc) endOrChain(errored bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.pendingSteers) > 0 && !errored {
		return true
	}
	p.steerOpen = false
	if len(p.pendingSteers) > 0 {
		p.orphan = true
	}
	return false
}

// takeOrphan 返回并清除“结束时仍有排队中的 steer”标记。
func (p *residentProc) takeOrphan() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	orphan := p.orphan
	p.orphan = false
	return orphan
}

func (p *residentProc) hasPendingSteers() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pendingSteers) > 0
}

// setApprover 替换本轮使用的审批方；常驻进程的审批服务覆盖多轮，轮与轮之间没有审批方。
func (p *residentProc) setApprover(approver harness.Approver) {
	if p.approval != nil {
		p.approval.setApprover(approver)
	}
}

func (p *residentProc) session() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessionID
}

func (p *residentProc) setSession(sessionID string) {
	p.mu.Lock()
	p.sessionID = sessionID
	p.mu.Unlock()
}

// wait 返回进程退出结果；读取协程关闭 exited 之后才可读 waitErr。
func (p *residentProc) wait() error {
	<-p.exited
	return p.waitErr
}

// pid 返回进程 PID（未启动成功时为 0）。
func (p *residentProc) pid() int {
	if p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}
