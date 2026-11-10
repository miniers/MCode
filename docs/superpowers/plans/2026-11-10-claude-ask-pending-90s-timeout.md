# Claude 待答提问与新消息超时：定位和实施方案

日期：2026-11-10。基线：工作副本 HEAD `106ecd3`。本次只增加本文档，不修改实现。

## 结论与证据边界

已确认三个独立缺陷：审批 HTTP 请求与 resident 轮次取消没有关联；等待用户时输出看门狗仍在计时；resident 超时文案始终打印首输出时长。另有一个直接相关的入口差异：“中断并发送”只取消轮次，没有像普通停止一样先撤卡。

**尚不能确认现场是“新轮复用旧进程，然后真的等了 90 秒”。** 普通新消息可能只是 Steer，且报错中的 `1m30s` 也可能来自真正等待了 15 分钟的旧轮。给定的 spawn 间隔与 transcript 的 interrupted 都不能独立区分这些路径。

未运行复现、构建和测试：任务末尾明确规定只读调研不跑构建与测试，故未使用前面的测试授权。一次包含运行日志检索的命令被工具拒绝，未重试；没有读取现场日志或 Claude transcript。本文中的现场时间来自任务提供的证据，不能当作本次重新核实的结果。

## 1. 用户发新消息时，实际有三条路径

- 普通纯文本发送进入 `SendMessage`。运行中、`WhenRunning` 为空、非 priority，且存在 session、Steerer、无附件和 composer options 时，会调用 Steer；这一分支没有检查待答卡片，也没有取消旧轮。源码中的 Steerer 分支本身没有以 `insertIntoRunningTurn` 为条件，不能仅凭设置名推断请求行为。

internal/remote/memory_backend.go:3162 `if isRunning && params.WhenRunning == "" && !priority {`

internal/remote/memory_backend.go:3169 `} else if steerer, ok := b.threadHarnessLocked(td.summary.Provider).(harness.Steerer); ok && td.sessionID != "" && len(params.Attachments) == 0 && params.Options == nil {`

internal/remote/memory_backend.go:3172 `err := steerer.Steer(ctx, sessionID, params.Text)`

- Claude Steer 仅登记 uuid、写 user 帧。它既不发 interrupt，也不向审批 MCP 返回拒绝。成功时用户消息仍使用旧 turnID；没有建立新的首输出计时器，也没有因 Steer 本身重置旧计时器。显式 queue 或不能注入的消息会排队，其他运行中请求可能被拒绝。

internal/providers/claude/resident.go:1166 `if err := target.sendUserFrameWithUUID(text, uuid); err != nil {`

internal/remote/memory_backend.go:3180 `msg := wire.ThreadMessage{ID: newUUID(), Role: "user", Text: params.Text, TurnID: &turnID, TurnIndex: index, CreatedAt: b.nowLocked().Format(time.RFC3339Nano)}`

internal/remote/memory_backend.go:3217 `if params.WhenRunning == "queue" || (isRunning && queueWhileRunning) {`

- GUI 的“插入”与“转向”不是同一个入口。开启插入时按钮使用 inject；转向走 `InterruptAndSend`，最终调用 `QueueInterruptAndSend`。该后端入口将新消息记入 pendingSend，再直接 runCancel，没有先撤卡，也没有调用 interruptLocked 的 8 秒兜底。

internal/ui/composer_send_state.go:43 `if insertIntoRunningTurn && (state == sendStateQueue || state == sendStateSteer) {`

internal/ui/model.go:2872 `err := sender.InterruptAndSend(ctx, tid, text, attachmentIDs)`

internal/app/client.go:3123 `_, err := c.backend.QueueInterruptAndSend(ctx, wire.ThreadsQueueInterruptAndSendParams{`

internal/remote/thread_ops_backend.go:2066 `td.pendingSend = &wire.FollowUp{`

internal/remote/thread_ops_backend.go:2075 `td.runCancel()`

- 普通停止则先撤卡，再取消运行。撤卡通过 withdrawCh 唤醒 Interact，返回明确的宿主撤回错误；审批 MCP 可以在 HTTP 连接仍存在时写回 deny。

internal/remote/memory_backend.go:5285 `batch := b.takeWithdrawBatchLocked(td, withdrawInterrupt)`

internal/remote/memory_backend.go:5294 `td.runCancel()`

internal/remote/withdraw.go:54 `pr.withdraw(reason)`

internal/remote/approvals.go:612 `return deny, errRequestWithdrawn`

## 2. 卡片在哪里结算，CLI 是否收到结果

- resident 的审批服务随进程存活，使用独立 Background context。handleApproveCall 传给 Interact 的是 HTTP 请求 context，不是 runOnResident 的 ctx。setApprover 仅替换指针，已开始的请求仍持有旧 approver。因此“runCancel 已执行”不等于“审批 MCP 已结束”。

internal/providers/claude/resident.go:668 `srvCtx, srvCancel := context.WithCancel(context.Background())`

internal/providers/claude/approval_mcp.go:269 `approver := s.currentApprover()`

internal/providers/claude/approval_mcp.go:398 `resp, approveErr = ia.Interact(r.Context(), approvalReq)`

internal/providers/claude/approval_mcp.go:42 `s.approver = approver`

- 若 HTTP context 被 CLI 断开、超时或进程关闭取消，Interact 进入 canceled 路径，将卡片记为 expired/caller_cancelled，并移除 pending。给定的那条“提问已取消”提示来自此路径，不是专门的新消息处理逻辑。写库与提示异步执行，提示与错误的可见先后不证明因果先后。

internal/remote/approvals.go:519 `case <-ctx.Done():`

internal/remote/approvals.go:544 `traceAskSettleWithNotice(a.b, requestID, storepkg.PendingAskExpired, askResolutionCallerCancelled, noticeThreadID)`

internal/remote/approvals.go:574 `delete(td.pending, requestID)`

internal/remote/pending_ask_trace.go:30 `go fn()`

internal/remote/pending_ask_trace.go:108 `if err := b.appendSystemNotice(threadID, "提问已取消，未收到回答。"); err != nil {`

- MCP 对 Interact 错误会尝试返回 deny，但当前错误分支忽略写回错误；若 context 是因为连接已断才取消，不能声称 CLI 收到了拒绝。正常撤卡也不应靠断开 HTTP 来通知 CLI。

internal/providers/claude/approval_mcp.go:419 `"text": string(denyPayload(approveErr.Error())),`

internal/providers/claude/approval_mcp.go:423 `_ = writeResponse(callResult, nil)`

## 3. 为什么 41 分钟没有被 15 分钟看门狗杀掉

**当前代码没有等待审批时暂停 resident 看门狗的分支。** 首次等待默认 90 秒；每读到 stdout 行后改用 15 分钟。审批 SSE 的 15 秒心跳写到 HTTP ResponseWriter，没有直接进入 stdout sink，不能据此认定它刷新了输出计时器。

internal/providers/claude/claude.go:65 `defaultFirstOutputTimeout = 90 * time.Second`

internal/providers/claude/claude.go:66 `defaultStallTimeout       = 15 * time.Minute`

internal/providers/claude/resident.go:851 `timer := time.NewTimer(firstWait)`

internal/providers/claude/resident.go:988 `resetTimer(timer, h.opts.stallTimeout())`

internal/providers/claude/approval_mcp.go:363 `if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {`

据此只能给出带条件的时序，不能编造唯一现场时序：

1. 若 12:25:28 后 stdout 真正完全静默、机器持续运行、轮次仍在主 select 中，约 12:40:28 就应超时并杀进程，虽然错误文字仍写 1m30s。
2. 若直到约 13:06 才超时，则需要核实期间是否仍有 stdout/system/control 帧刷新计时器、宿主是否休眠/暂停、同步 emit 是否阻塞，或所看日志并非对应旧轮的完整事件。transcript 没有助手正文不等于 stdout 没有协议帧。这些是待排查项，不是已确认原因。
3. “直到 13:06:30 才再次 spawn”只证明这次是新进程；旧进程更早退出、之后无人发消息，同样不会有中间 spawn。也可能旧轮被 Steer 后才结束。必须结合旧 turnID、PID 退出时间、新消息 RPC 类型和真实超时阶段区分。

历史核对：`2645346` 增加审批 SSE 心跳及 24 小时 MCP 工具 timeout，没有修改 resident 输出看门狗。`9838837` 增加提问取消留痕与系统提示，没有建立轮次取消到 MCP context 的传播。git blame 显示错误文案和 interrupt 超时杀进程逻辑已经存在于较早基线，并非 SSE 改动新引入。

## 4. interrupt 与复用：能证明和不能证明的部分

- interruptResident 发控制帧并等待 result；收到控制应答本身不代表旧轮已结束。仓库假 CLI 总是立即补 result，所以现有测试没有覆盖 MCP 挂起时 interrupt 无效的情况。仓库没有 CLI 内部实现，不能静态证明真实 CLI 在所有 MCP 挂起状态都响应 interrupt。

internal/providers/claude/resident.go:1112 `go func() { _ = p.sendInterrupt() }()`

internal/providers/claude/resident.go:1132 `if parser.sawResult {`

internal/providers/claude/resident_test.go:202 `_, _ = out.WriteString(fakeInterruptedResult + "\n")`

- 5 秒内没收到 result：killResident 后返回 false；keep=false，release 摘除并关闭进程。**不存在“interrupt 5 秒无 result 仍 keep=true”的实现。** 下一次普通轮次不能复用这个已杀进程。

internal/providers/claude/claude.go:71 `defaultInterruptGrace = 5 * time.Second`

internal/providers/claude/resident.go:1137 `h.killResident(p)`

internal/providers/claude/resident.go:1138 `return false`

internal/providers/claude/resident.go:1006 `keep = h.interruptResident(p, sink, parser, emit)`

internal/providers/claude/resident.go:575 `h.forgetResident(p)`

- 真正的安全缺口是：收到 result 后，只检查 pending steers，没有检查是否仍有旧审批 HTTP 请求。若 CLI 补了 result 却没终止 MCP 调用，旧进程可能被留用；setApprover(nil) 不会结束旧请求。这个情形可用假 CLI 构造，但尚未证明发生在本次现场。

internal/providers/claude/resident.go:1133 `return !p.hasPendingSteers()`

internal/providers/claude/resident.go:984 `keep = !p.takeOrphan()`

- 复用只接受不 busy 的进程。若旧轮还在等待提问，则普通 Steer 是同一轮；并发另开 Run 则退回 runOnce，不是复用 busy resident。复用时若 hot settings 无应答，先触发的是 10 秒 apply timeout；有应答但用户帧之后无输出，才会触发首输出超时。

internal/providers/claude/resident.go:397 `case cur.busy:`

internal/providers/claude/resident.go:378 `return h.runOnce(ctx, turn, emit)`

internal/providers/claude/resident.go:402 `case !cur.dead && cur.key == spec.key && cur.sessionID == turn.SessionID && !h.expiredLocked(cur):`

internal/providers/claude/resident.go:786 `const settingsApplyTimeout = 10 * time.Second`

## 5. 方案取舍

| 方案 | 改动与代价 | 结论 |
| --- | --- | --- |
| 只延长超时或修改报错 | 卡片与 MCP 仍可能悬挂，等待更久 | 不采用 |
| Steer 写帧后取消待答问题，让 CLI 合并处理 | provider 改动较少；依赖 CLI 在 deny 后消费已排队 user，错误 result 还可能令 orphan steer 被丢弃 | 需真实 CLI 验证后才能作为优化 |
| 待答问题的新消息复用 pendingSend → 撤卡 → 中断 → 收尾后开新轮 | 复用已存在的中断发送与优先开轮机制；取消不干净时换进程，代价是少数请求需 resume 冷启动 | **推荐：行为确定，容易验收** |

范围：先覆盖 Claude 常驻会话、运行中普通文本新消息和已有“中断并发送”。显式 queue 继续表示排队；用户正常答卡不启动新轮；普通运行中没有待答问题时保留 Steer。暂不改变 UI、协议 schema 或引入依赖。

## 6. 给执行档的六步实现

### 第一步：建立审批请求的轮次归属与可观察等待状态

锚点：

internal/providers/claude/approval_mcp.go:33 `mu       sync.RWMutex`

internal/providers/claude/approval_mcp.go:398 `resp, approveErr = ia.Interact(r.Context(), approvalReq)`

internal/providers/claude/resident.go:797 `p.setApprover(turn.Approver, planExitModeFor(turn))`

改法：审批服务保持进程生命周期，增加很小的“当前轮次”状态：轮次代数、取消函数、该代活跃审批请求数及状态变更通知。runOnResident 开始登记带 ctx 的轮次，结束时先封闭该代请求入口并取消其在途请求。每个 HTTP 请求原子取得 approver、轮次代数和派生 context；派生 context 同时受 HTTP 连接和轮次结束控制，不能取消 r.Context 本身。锁外执行 Interact、cancel 和通知，禁止持服务锁调用上层。请求结束只注销自己的代数，不能减少新轮计数。无当前轮次仍返回 deny。

等待状态从实际调用 Interact/Approve 前开始，最终 HTTP 响应尝试结束后解除；HTTP 写回失败也解除，所有返回分支必须覆盖。错误响应同样检查 writeResponse 错误；新增日志限于 thread、代数、请求种类、阶段，不记录问题正文或认证信息。

验收：取消轮次、结束轮次、HTTP 断开都能唤醒旧 Interact；连接存在时收到一条完整 JSON/SSE deny；旧请求不跨入下一轮，计数不为负；正常答案与 SSE 保活仍工作。

测试：在 `internal/providers/claude/approval_mcp_test.go` 增加 channel 驱动的轮次取消、代数切换、两个并发审批、错误响应写失败用例，复用现有 fakeApprover 与 HTTP/SSE 请求手法。无需真实 CLI。

### 第二步：等待用户时暂停 resident 输出看门狗，并修正超时文案

锚点：

internal/providers/claude/resident.go:847 `firstWait := h.opts.firstOutputTimeout()`

internal/providers/claude/resident.go:896 `case <-timer.C:`

internal/providers/claude/resident.go:989 `case <-timer.C:`

internal/providers/claude/resident.go:991 `msg := fmt.Sprintf("claude process produced no output within %s", h.opts.firstOutputTimeout())`

改法：在 runOnResident 内集中维护当前阶段及实际 duration，每次启表都同步更新；首输出、轮内停顿、自发输出排空三个阶段分别报告真实预算。两个输出等待循环都订阅第一步的状态通知：当前代活跃审批数大于 0 时停表并排空已触发信号；清零后给完整 stallTimeout 等 CLI 恢复。timer 分支杀进程前再读取当前代等待状态，避免审批开始与旧 timer 同时就绪时误杀。多请求中只结束一条不能恢复计时。ctx.Done、进程退出和 interruptGrace 继续生效，不能因用户等待暂停中断兜底或 settingsApplyTimeout。

若审批通知期间仍有 stdout，则解析正常进行，但不要重新启用已暂停的表。排空阶段的错误也使用当前阶段实际 duration，不能继续打印最初 firstWait。加入一条超时日志，记录阶段、预算、sawOutput、等待数、PID 与 threadID，以后能区分真实 90 秒和误标 90 秒。

验收：等待超过两个缩短的 stallTimeout，进程存活、卡片仍可答；答复后重新启用停顿检测；无审批时首输出与停顿超时仍能杀进程并报告各自时长。

测试：`internal/providers/claude/resident_test.go` 增加 wait/pause/resume、并发审批、审批开始与 timer 就绪竞争、draining 等待用例。首输出设为 2s、stall 设为 200ms，确认 init 后保持静默时错误写 200ms 而非 2s；首输出完全静默另测其 200ms 预算。不要把计时预算误差设成严格毫秒相等。

### 第三步：待答问题的新消息进入已有中断发送通道

锚点：

internal/remote/memory_backend.go:3162 `if isRunning && params.WhenRunning == "" && !priority {`

internal/remote/thread_ops_backend.go:2066 `td.pendingSend = &wire.FollowUp{`

internal/remote/thread_ops_backend.go:2075 `td.runCancel()`

internal/remote/memory_backend.go:5285 `batch := b.takeWithdrawBatchLocked(td, withdrawInterrupt)`

internal/remote/memory_backend.go:9211 `if fromPendingSend {`

改法：在普通文本 Steer 分支之前，检查当前 Claude 轮次是否存在尚未 handled 且 providerWaiting 的 Question。若是，把这条文本登记为 pendingSend，并调用已有 interruptLocked；锁外依次 commitQueueChange、finishWithdraw 与 action 状态广播，返回已接收/待发送信息。沿用“旧 pendingSend 移回队首”的现有规则，不能覆盖丢失前一条用户消息。新文本只允许在旧轮收尾后由 startNextQueuedTurnLocked 开新轮，不在中断后立刻 Run。

把 `QueueInterruptAndSend` 真正中断分支的直接 runCancel 同样改为 interruptLocked，保留其排队与 Swarm 分支；核对 `QueueSendNow` 的同类直接取消位置，只在确实走相同待答中断场景时复用撤卡操作。可提取一个小的锁内 pendingSend 登记函数供这两个入口使用，避免复制中断状态机。

本步对 `SendMessage` 的新增路由使用原来可 Steer 的输入条件（无附件、无 options），避免通过 FollowUp 丢失 composer options；已有 `QueueInterruptAndSend` 的附件固定与消费逻辑照用。带 options/附件的普通发送当前本来就不走 Steer，本任务不改变其排队契约，作为明确的范围限制。

验收：默认普通文本在待答时不调用 Steer；先撤卡并给旧请求返回 deny，再取消旧轮；pendingSend 在中断收尾后仍会出队，且只产生一轮新消息。不能简单设 WhenRunning=queue：普通队列在中断后不会自动出队，而 pendingSend 会。显式 queue、无提问普通 Steer 与普通停止的原有行为不变。

测试：`internal/remote/thread_ops_backend_test.go` 补中断发送遇到挂起提问；`internal/remote/runtime_regression_test.go` 补默认 SendMessage 遇到 Claude Question 的路由；使用已有可控 harness，断言 withdraw 信号先于 runCtx 取消、旧问只结算一次、新文本只开轮一次、旧 turnID 与新 turnID 不同，显式 queue 不触发撤卡。沿用 `internal/remote/pending_ask_trace_test.go` 的同步留痕手法验证 settled/withdrawn 不被迟来的 caller_cancelled 覆盖。

### 第四步：只把确实回到空闲的进程留给下一轮

锚点：

internal/providers/claude/resident.go:1006 `keep = h.interruptResident(p, sink, parser, emit)`

internal/providers/claude/resident.go:1133 `return !p.hasPendingSteers()`

internal/providers/claude/resident.go:984 `keep = !p.takeOrphan()`

internal/providers/claude/resident.go:936 `keep = !p.takeOrphan()`

改法：在进入 interruptResident 前先封闭并取消当前代审批请求；保留现有 5 秒 interruptGrace。取消时曾存在在途审批的进程采用保守策略：不再复用，即使收到了 result 也 keep=false，由现有 release 完成关闭。这样不依赖真实 CLI 的 MCP interrupt 细节。无在途审批的常规中断仍可保留进程。

正常 result/Attach 结束也要检查该代是否还有审批请求；有则取消并 keep=false，不能仅 setApprover(nil) 后复用。轮次封闭与检查必须原子完成，避免“检查无请求后又注册旧轮请求”的窗口。不得为了复用无限等待 HTTP handler：连接写阻塞时直接回收进程，现有 close/kill 结束连接。计数与代数检查通过审批服务的小接口完成，不读取裸 map。

验收：MCP 挂起时 interrupt 不补 result，在 grace 内退出并换进程；补 result 但 MCP 不退出，同样不能复用；普通取消仍通过已有保活用例。新进程携带原 sessionID 的 --resume，不因可恢复的提问取消清空会话。

测试：`internal/providers/claude/resident_test.go` 增加无 interrupt result 与假空闲 result 两种用例，断言旧 PID 已消失、旧 Interact 已结束、下一轮启动数为 2 且 argv 含 --resume；保留 TestResident_InterruptKeepsProcessAlive 作为无审批基线。

### 第五步：用假 CLI 分别复现两种“90 秒”解释

锚点：

internal/providers/claude/claude_test.go:24 `func TestMain(m *testing.M) {`

internal/providers/claude/resident_test.go:66 `func runFakeResidentClaude(mode string) {`

internal/providers/claude/resident_test.go:200 `if parsed.Request.Subtype == "interrupt" {`

internal/providers/claude/resident_test.go:251 `func fakeStartArgvs(t *testing.T, logPath string) []string {`

改法：扩展现有测试二进制假 CLI，不新写 shell 脚本、不依赖 curl。新模式从 --mcp-config 临时 JSON 解析本进程审批 URL/token；首轮吐 init 与 AskUserQuestion tool_use，另起 goroutine 发真实的本机 HTTP tools/call，Accept 包含 text/event-stream。主 stdin 循环保持可读，记录 user 与 interrupt，但在审批没有最终响应时不回复新 user。最终 SSE JSON 到达后记录 deny/allow，再按模式返回 result。所有网络只访问测试本机监听器；通过 approver 入场 channel 与 fakeFrames/fakeUsers 日志握手，不固定 sleep 41 分钟。

两种红测必须分开说明，不把构造条件当现场事实：

1. **Steer + 误标时长**：init 后提问挂起，在缩短的 stallTimeout 内发 Steer；旧代码没有结束 HTTP 提问，最终等待的是 stallTimeout，错误却打印 FirstOutputTimeout。断言同一 PID、没有新 Run、审批未收到新消息触发的结果，以及实际耗时符合 stall 而非 first。
2. **旧审批跨轮 + 真实首输出超时**：专门的故障模式在审批 HTTP 仍挂起时对 interrupt 吐 result；旧代码会 keep=true。下一 Run 指定相同 session/指纹，假 CLI 响应 settings 控制帧但不响应新 user，FirstOutputTimeout 设 200ms，得到首输出超时。此测试证明复用漏洞可发生，不证明真实 CLI 一定会产生这种帧序列。

修复后主验收用 remote 第三步行为或 provider 级“取消旧 Run 后开始新 Run”驱动：旧请求明确 deny 或旧进程被替换，新进程回复固定文本和一个 EventDone；无 EventError，无 produced no output；第二条 user 不发到仍挂问的旧进程，不自动重放已经开始执行的工具副作用。用不改变启动指纹的临时 marker 控制首进程/恢复进程行为。

### 第六步：限定回归与交接

锚点：

internal/remote/memory_backend.go:6344 `nextStarted, nextText, queueChanged = b.startNextQueuedTurnLocked(td, isNormal && !hasBees, isNormal)`

internal/providers/claude/resident.go:1284 `// 用户帧已经写进 CLI（写失败走另一条分支），因此不重试，避免把已经开始执行的轮次跑两遍。`

改法：完成上述新增回归并运行下列两条包级检查。测试必须同时覆盖新消息抢占、用户正常答复、长等待、显式停止、grace 后杀进程，以及正常无审批复用；不要修改 unrelated 测试来消除失败，不设置真实 CLI 冒烟开关。若某次超时已经杀掉成功投递 user 的进程，继续保持不可自动重试，不能用盲目重试掩盖问题。

```text
go test ./internal/providers/claude/... -timeout 300s
go test ./internal/remote/... -timeout 300s
```

本次结果：两条命令均未运行；没有成功复现或测试通过的声明。没有启动后台任务、mcode 本体或访问网络。执行档需要报告新增回归在修复前的失败点、修复后的结果，以及两条包级检查的真实输出状态。

最终验收：Claude 提问挂起时普通文本新消息会撤销旧问并在干净进程状态上得到回复；等待用户本身不触发 stallTimeout；取消无响应时在 interruptGrace 后确定回收；超时日志可区分 first/stall/draining 及真实预算。

## 注意事项与未完成部分

- 实际 41 分钟时序仍未证实，需主代理在可读取现场证据的环境核对：新消息 RPC 类型、旧 PID 退出时间、是否有 stdout 协议帧、旧新 turnID 和宿主休眠。本文不把“旧问仍挂起却被复用”列为已确认现场根因。
- 推荐方案会回收取消时有审批的 CLI，相关后台任务也可能随进程终止；这是保证新消息不串入旧 MCP 状态的明确取舍。后续若有真实 CLI 的完整收尾证据，再考虑放宽复用。
- 普通带 options/附件的新消息保持既有排队语义；若产品要求它们也抢占待答问题，需要另行完整保存 composer options，不能直接塞进当前 FollowUp 而丢字段。
- 本方案针对 resident。runOnce 的 watchdog 也没有等待用户豁免且回调文案固定首输出预算；如本轮要求覆盖非驻留模式，应在同一轮次等待机制下单独补 runOnce 回归，不能宣称本方案已覆盖它。
