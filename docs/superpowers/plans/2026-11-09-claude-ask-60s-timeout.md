# Claude 提问等待 60 秒取消：定位与修复方案

## 已确认根因与采用的修复

Claude CLI 的 HTTP 请求首字节预算约为 60 秒，审批服务在用户答复前不发送字节，因此超时并报告 `The operation timed out.`。修复采用 SSE 立即发送响应头并定时保活，同时将审批服务的单服务 timeout 设置为 24 小时。

状态：仓库侧取消链路已定位；CLI 定时器、环境变量和版本回归点未确认，暂不能给出已验证的根因修复。

根因结论（证据边界）：mcode 将审批等待绑定到入站 HTTP 请求的 context，该 context 取消后立即将提问结算为 `expired / caller_cancelled`；结合主线提供的恰好 60.0 秒记录，最符合现象的是 Claude 调用端超时后中止 HTTP 请求，但本次没有取得 CLI 源码或请求断开证据，不能断言具体计时器或发起者。

本次只新增本文档。未改代码，未运行构建、测试、lint、类型检查或运行时验证。未启动后台任务，未创建临时实验文件。数据库时间和 CLI 版本均采用主线已核实的信息，未自行复查。

1. **固定已确认的取消链路，排除服务端处理 MCP 取消通知。**

   internal/providers/claude/approval_mcp.go:340 `resp, approveErr = ia.Interact(r.Context(), approvalReq)`

   等待 context 直接来自 HTTP 请求。客户端连接断开、HTTP/2 请求取消或服务器关闭连接都可能使它取消；该行本身不能区分这些原因。

   internal/providers/claude/approval_mcp.go:167 `if req.ID == nil {`

   internal/providers/claude/approval_mcp.go:168 `w.WriteHeader(http.StatusAccepted)`

   所有无 ID 通知直接返回 HTTP 202，`notifications/cancelled` 没有解析请求 ID、查找等待任务或调用 cancel 的实现。因此，收到这类通知本身不会在当前实现中取消审批等待；客户端可以同时发送通知并断开原请求，真正传播到等待逻辑的仍是 HTTP context 取消。

   internal/providers/claude/approval_mcp.go:103 `_ = s.close()`

   internal/providers/claude/approval_mcp.go:120 `err = s.server.Close()`

   服务生命周期 context 结束也会关闭 HTTP 服务，使请求 context 取消。尚未排除 CLI 退出或宿主取消轮次导致的这条链路，不能仅凭 `caller_cancelled` 判断是 CLI 的单次 MCP 调用超时。

   internal/remote/approvals.go:519 `case <-ctx.Done():`

   internal/remote/approvals.go:544 `traceAskSettleWithNotice(a.b, requestID, storepkg.PendingAskExpired, askResolutionCallerCancelled, noticeThreadID)`

   `Interact` 的等待分支没有 60 秒计时器。结算状态证明 context 已取消，不证明取消原因。主线提供的 2026-11-09 09:36:12.306Z 至 09:37:12.306Z，以及 11-08 同类记录，强烈指向固定 60 秒限制；11-07 及更早能等待 1502/2772 秒，pi 能等待 529 秒，说明应优先检查 Claude 通道及近期 CLI 变化。

2. **先取得 CLI 的实际调用链，再选择修复方式；当前此步被工具访问范围阻塞。**

   internal/providers/claude/claude.go:399 `args = append(args, "--permission-prompt-tool", promptTool)`

   重点检查 permission-prompt-tool 发起的 MCP 请求，而不是仅查普通模型 MCP 工具调用。两条路径可能使用不同的超时配置。

   读取 `/Users/liukai/.local/share/claude/versions/` 时，工具返回 `path outside allowed directories`。按任务“遇到阻塞就停下，不要绕过”的要求，没有改用 shell 读取该目录或二进制。尚未检查本机 2.1.318、2.1.317、2.1.310，也未确认更早版本文件是否存在。

   后续由具备该目录访问能力的主代理，在设有超时、输出长度受限的只读命令中提取二进制字符串，搜索 `60000`、`MCP_TOOL_TIMEOUT`、`MCP_TIMEOUT`、`permission_prompt`、`permission-prompt-tool`、`resetTimeoutOnProgress`、`DEFAULT_REQUEST_TIMEOUT`。保留每个命中的调用上下文和版本，避免只凭变量名存在就认定有效。

   必须确认：permission 请求是否显式传入 timeout；未显式传入时 SDK 的默认值；变量读取值、单位及边界；timeout 到期是否 abort HTTP；该分支是否传 `_meta.progressToken`；是否启用 `resetTimeoutOnProgress`；是否还存在独立的总等待上限。字符串缺失或压缩代码无法还原时，如实记录未确认，不猜变量名或参数语义。

3. **在有权限且允许运行实验的环境中，用一次受控调用补齐因果证据。**

   internal/providers/claude/approval_mcp.go:231 `s.handleApproveCall(w, r, req.ID, callParams.Arguments)`

   临时假 MCP 服务应复现这条 HTTP permission 工具路径，支持 initialize、tools/list、tools/call；只记录方法、请求 ID、progressToken、收到请求/断开连接的相对时间及取消通知，不记录授权头。返回确定的无副作用工具审批结果。确保实际触发 `--permission-prompt-tool`，未进入 tools/call 的实验不能用来判断超时。

   先让响应延迟 75 秒，使用外层约 100 秒超时观察默认行为；再只改变经静态检查确认被该路径读取的超时变量，重复相同请求。外层超时从 CLI 启动计时，判定时从 tools/call 到达计时，须分别记录。证明默认组约 60 秒断开、变量组可在 75 秒收到结果后，才能认定变量修复有效。旧版本用同样输入对照，不能把 11-07 时间相关性当作已定位回归版本。

   如需进度实验，必须先确认请求携带 progressToken，且 CLI 对这次 permission 请求启用 resetTimeoutOnProgress。缺少任一条件则不要实现保活。当前服务只在结束时写 JSON 响应，不支持在等待期间发送进度；若采用该方案，需要实现 Streamable HTTP 的 SSE 响应并刷新消息，单纯发 HTTP 空白或其他请求的通知不能认定会重置客户端计时器。即使进度有效，也要检查最大总时长限制。

   实验文件仅放 `/tmp`；所有命令设超时；记录并终止所启动的进程及其子进程，删除临时文件。此步骤尚未执行。

4. **首选条件式最小修复：仅在确认环境变量覆盖 permission 调用超时后，修改公共 buildEnv。**

   internal/providers/claude/claude.go:410 `func buildEnv(base []string, workDir string, extra []string) []string {`

   internal/providers/claude/claude.go:424 `if !hasEnvKey(env, "CLAUDE_CODE_ENABLE_TODO_TOOLS=") {`

   internal/providers/claude/claude.go:427 `env = append(env, extra...)`

   若第 2、3 步证实 `MCP_TOOL_TIMEOUT` 确实作用于 permission 请求，且单位为毫秒、允许下述值，则在追加 extra 前按现有 hasEnvKey 模式注入默认 `MCP_TOOL_TIMEOUT=86400000`（24 小时），base 已有值时保留，extra 继续最后覆盖；更新函数说明。24 小时是明确的有限上限，不应描述成无限等待。如果真实生效变量、单位或允许范围不同，以已取得的调用链和实验结果为准。未确认前不要直接合入这个候选值。

   internal/providers/claude/claude.go:652 `cmd.Env = buildEnv(baseEnv, turn.WorkDir, turn.Env)`

   internal/providers/claude/resident.go:721 `cmd.Env = buildEnv(baseEnv, turn.WorkDir, turn.Env)`

   公共 buildEnv 可同时覆盖一次性及常驻子进程，避免只在 runOnce 注入导致常驻路径继续失败。现存常驻 CLI 进程必须重建才能读取新环境。该全局变量也可能延长其他 MCP 工具等待，应在变更说明中明确这一实际影响；若 CLI 支持已证实的单服务配置，优先缩小到 mcode_approval。

   若 permission 路径硬编码 60 秒且忽略环境变量，环境注入方案不可用。只有第 3 步证实进度能重置该请求后，才考虑在 approval_mcp.go 增加进度协议；这比环境注入改动更大。不要用脱离请求的 context 绕开：HTTP 响应已无法送达，保留可答卡片会造成答案丢失或悬挂。

5. **为最终选中的实现安排测试，主代理负责执行。**

   internal/providers/claude/claude_test.go:239 `func TestBuildEnv(t *testing.T) {`

   若采用环境变量方案，修改该表驱动测试的默认期望，并加入“默认超时值”“base 已设置时不重复注入”“extra 覆盖默认值”“extra 覆盖 base 值”四个子用例。断言完整环境列表及最后生效值，确保既有 PWD、子代理模型和待办工具行为不变。新增 `TestBuildEnv_MCPToolTimeoutPrecedence` 也可，但不要和表内用例重复。

   internal/providers/claude/approval_mcp_test.go:529 `func TestApprovalServer_AskUserQuestion(t *testing.T) {`

   新增 `TestApprovalServer_AskUserQuestion_RequestCancellation`：通过真实本机 HTTP 请求发起问题，等待 Interactor 进入阻塞后取消客户端 context，断言 Interactor 的 context 取消且处理结束，保护正常取消能力。新增 `TestApprovalServer_CancelNotificationDoesNotCancelRequest`：另一个 HTTP 请求发送无 ID 的 notifications/cancelled，断言返回 202、原 Interactor 仍等待，最后主动取消原请求并回收服务。使用信号同步和短截止时间，不在单元测试里等待 60 秒。

   若实际选择 SSE 进度方案，再增加 `TestApprovalServer_ProgressWithToken`、`TestApprovalServer_NoProgressWithoutToken`、`TestApprovalServer_ProgressStopsOnCancellation`，分别断言 token 原样回传、无 token 不发送进度、取消后停止发送且终结等待。客户端是否因此超过 60 秒仍必须由真实 CLI 实验验证，不能由 Go 服务单测代替。

6. **若 CLI 无可用覆盖或进度路径，沿用现有取消提示并准确表达限制。**

   internal/remote/pending_ask_trace.go:108 `if err := b.appendSystemNotice(threadID, "提问已取消，未收到回答。"); err != nil {`

   现有代码已为取消的问题追加系统提示，无需再增加一套提示机制。`caller_cancelled` 也包含普通取消，不能统一改成“提问已超时”。只有取得明确超时原因并能与用户取消区分后，才在现有通知路径选择“提问已超时”；若只能使用等待时长推断，文案应保留不确定性，例如“提问连接已断开，未收到回答。”。不要仅凭持续约 60 秒写入确定的 timeout 原因。

   本轮未继续排查 UI、通知测试或版本锁定配置，因为 CLI 根因证据尚缺且任务限定范围。若最终确认是 CLI 回归且 mcode 无可用协议或配置修复，可提供经同样实验验证的旧版本临时回退建议，由主代理按现有 CLI 版本管理方式实施；不要自动替换用户二进制。

未完成：CLI 二进制与旧版本比较、超时变量的实际适用性、HTTP 断开观测、progressToken/resetTimeoutOnProgress 及总时长限制、最小运行实验。当前方案可供主代理继续定位与选择修复，但不可作为“MCP_TOOL_TIMEOUT 已解决问题”的证据。
