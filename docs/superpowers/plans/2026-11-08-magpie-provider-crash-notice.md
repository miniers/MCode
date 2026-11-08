# Magpie 发送后出现 Provider 退出提示：静态诊断与实施方案

日期：2026-11-08。状态：静态分析完成，根因尚未实证。本次只创建此方案，不修改代码，不运行构建、测试或其他验证。

## 结论与证据边界

- **图标对应 Antigravity（provider kind 为 `agy`）**。蓝色「⋀」不是 mcode、Claude、Codex 或 Pi 的图标。依据当前资源和渲染代码，这次应优先追踪 AGY 适配器。没有原会话记录，仍需下次复现的 provider 日志确认实际执行者；回复头使用的是当前会话 provider，不能据此证明此前失败尝试的 provider 没有变过。
- **最有解释力的推断是 AGY 的退出收尾被当成整轮失败**：AGY 可以先发出成功 result 和正文，再因进程未退出而被适配器主动终止；`Wait` 的错误仍被包装成 `agy process exited...`，后端据此清空 session 并重跑。这能解释最终有 `666`，同时出现重建上下文提示。26s 与这条路径中的 15s 宽限期相容，但耗时本身不能证明走过该路径。
- **另一条可能路径是真实的首轮退出，随后后端重试成功**。AGY 在成功 result 之前异常退出，也会产生同样的 `process exited` 包装。最终答对并不能证明退出提示误报。
- **现有代码不支持直接断言“新增 Magpie 后复用了配置过期的 Claude 进程”**。Claude inject 模式每轮读取路由，注入后的环境变量进入进程复用指纹；Pi 和 Codex 也把 Magpie 路由数据计入复用键。直接在端点保存时重启全部 CLI 不属于本方案的首选修复。
- 后端对任意错误做字符串包含判断，确有误判入口：普通非零退出、上游 HTTP 错误正文中的 `out of memory` 等，都可能被统一解释成 provider 进程崩溃。**源码能证明分类过宽，不能证明本次命中的具体标记或原始错误。**

主代理已告知：目标会话不在数据库中；对应时间的 `mcode.log` 没有相关错误行。本次没有重新查询数据库，也没有重新读取用户日志。技能目录中的 ponytail、rtk 文件被只读工具的目录权限阻止读取；未绕过权限，以下结论直接依据仓库源码。

## 图标判定

以下是源码事实。每条路径之后为对应行的源码原文。

- 回复头取当前会话的 provider：internal/ui/timeline_rows.go:3080 `return th.Provider, model, at`
- 回复头按 provider 渲染：internal/ui/timeline_rows.go:3097 `renderProviderIcon(c, provider, 11)`
- 转入彩色品牌图标：internal/ui/composer.go:1782 `renderProviderIconColor(c, provider, size, ThemeSecondaryText(c))`
- `agy` 的资源键归一为 Antigravity：internal/ui/icons.go:919 `return "antigravity"`
- 彩色资源映射：internal/ui/icons.go:895 `"antigravity": iconProviderAntigravityColor,`
- 唯一匹配蓝色拱形描述的资源：internal/ui/icons_assets/color/provider-antigravity.svg:1 `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24"><g transform="translate(-4.208 -4.0182) scale(0.2393)"><g transform="translate(12 10)"><path fill="#3186FF" d="M89.6992 93.695C94.3659 97.195 101.366 94.8617 94.9492 88.445C75.6992 69.7783 79.7825 18.445 55.8659 18.445C31.9492 18.445 36.0325 69.7783 16.7825 88.445C9.78251 95.445 17.3658 97.195 22.0325 93.695C40.1159 81.445 38.9492 59.8617 55.8659 59.8617C72.7825 59.8617 71.6159 81.445 89.6992 93.695Z"/></g></g></svg>`
- AGY harness 的 kind：internal/providers/agy/agy.go:91 `return harness.Kind("agy")`
- 对照，mcode 单色图标是 brain：internal/ui/icons.go:798 `return iconBrain`
- 对照，Claude 资源映射：internal/ui/icons.go:893 `"claude":      iconProviderClaudeColor,`
- 对照，Codex 资源映射：internal/ui/icons.go:891 `"codex":       iconProviderCodexColor,`
- 对照，Pi 资源映射：internal/ui/icons.go:897 `"pi":          iconProviderPiColor,`

因此，“新建网关已连接、7 个模型”只说明端点配置与目录可用，并不能说明这条会话使用了该网关。AGY 不在 Claude/Codex/Pi 的 Magpie 注入路由支持列表中。

internal/app/agent_routing.go:34 `if agent != config.ProviderClaude && agent != config.ProviderCodex && agent != config.ProviderPi {`

## 标记 → 产生位置

检索范围为 `internal/providers`、`internal/remote`、`internal/agent`、`internal/app`。下表区分固定文本构造与外部文本透传；没有固定文本的位置，不虚构具体信号或内存错误的来源。

| 标记 | 来源判断 | 产生位置及源码原文 |
| --- | --- | --- |
| `signal: segmentation fault` | 范围内未找到直接构造此字面量的生产代码；可来自 Go `exec.Cmd.Wait` 的信号错误，经 `%w` 包装，也可来自 stderr/上游正文 | internal/providers/agy/agy.go:909 `waitErr := cmd.Wait()` |
| `signal: segmentation fault` | AGY 的 Wait 错误透传点 | internal/providers/agy/agy.go:1009 `return fmt.Errorf("agy process exited with error: %w%s", waitErr, retrySuffix)` |
| `signal: killed` | 同上，没有直接构造字面量；AGY 本地强杀也可能产生该 Wait 错误 | internal/providers/agy/agy.go:683 `_ = killProcessGroup(cmd, syscall.SIGKILL)` |
| `signal: killed` | Claude Wait 错误透传点，同样可携带前一行的 segmentation fault 信号文本 | internal/providers/claude/resident.go:1258 `base = fmt.Errorf("claude process exited with error: %w (stderr: %s)", waitErr, tail)` |
| `process exited` | AGY：限流且禁止换号时的非零退出，不能据此断言崩溃 | internal/providers/agy/agy.go:955 `return fmt.Errorf("agy process exited with error: %w (stderr: %s)%s", waitErr, stderrStr, retrySuffix)` |
| `process exited` | AGY：一般非零退出，stderr 非空 | internal/providers/agy/agy.go:1007 `return fmt.Errorf("agy process exited with error: %w (stderr: %s)%s", waitErr, strings.TrimSpace(stderrStr), retrySuffix)` |
| `process exited` | AGY：一般非零退出，stderr 为空 | internal/providers/agy/agy.go:1009 `return fmt.Errorf("agy process exited with error: %w%s", waitErr, retrySuffix)` |
| `process exited` | Claude 单轮进程：没有 result，Wait 失败 | internal/providers/claude/claude.go:767 `base = fmt.Errorf("claude process exited with error: %w (stderr: %s)", waitErr, tail)` |
| `process exited` | Claude 单轮进程：没有 result，即使 Wait 成功也产生此标记 | internal/providers/claude/claude.go:769 `base = fmt.Errorf("claude process exited without a result (stderr: %s)", tail)` |
| `process exited` | Claude 常驻进程：热设置过程中输出通道关闭 | internal/providers/claude/resident.go:1058 `return errors.New("process exited while applying settings")` |
| `process exited` | Claude 常驻进程：没有 result，Wait 失败 | internal/providers/claude/resident.go:1258 `base = fmt.Errorf("claude process exited with error: %w (stderr: %s)", waitErr, tail)` |
| `process exited` | Claude 常驻进程：没有 result，Wait 成功 | internal/providers/claude/resident.go:1260 `base = fmt.Errorf("claude process exited without a result (stderr: %s)", tail)` |
| `process exited` | ACP 通用适配器：prompt 没有优先报错、Wait 失败，stderr 非空 | internal/providers/acp/acp.go:1021 `return fmt.Errorf("acp process exited with error: %w (stderr: %s)", waitErr, tail)` |
| `process exited` | ACP 通用适配器：同上，stderr 为空 | internal/providers/acp/acp.go:1023 `return fmt.Errorf("acp process exited with error: %w", waitErr)` |
| `process exited` | Pi RPC 等待中的进程死亡哨兵 | internal/providers/pi/proc.go:32 `var errProcDead = errors.New("pi: process exited")` |
| `process exited` | Pi 当轮尚未完成时进程退出 | internal/providers/pi/run.go:600 `return fmt.Errorf("pi: process exited before the turn settled: %s", errMessage)` |
| `process exited` | Omp 当轮尚未完成时退出 | internal/providers/omp/omp.go:570 `return fmt.Errorf("omp: process exited before the turn settled: %s", errMessage)` |
| `app-server exited` | Codex：未产生事件，app-server 已死 | internal/providers/codex/codex.go:1327 `return true, fmt.Errorf("codex: app-server exited before the turn produced any event")` |
| `codex turn incomplete` | Codex：结束通道已触发，但 handler 没认定本轮完成 | internal/providers/codex/codex.go:1343 `return false, fmt.Errorf("codex turn incomplete (stderr: %s)", tail)` |
| `process closed connection unexpectedly` | Codex：异常结束且 stderr 为空时的错误事件 | internal/providers/codex/codex.go:1335 `errMsg = "codex process closed connection unexpectedly"` |
| `process closed connection unexpectedly` | Copilot：未成功完成且 stderr 为空时的错误事件 | internal/providers/copilot/copilot.go:837 `errMsg = "copilot process closed connection unexpectedly"` |
| `sigsegv` | 范围内未找到直接构造该字面量的生产代码；可从 stderr 或上游错误文本透传，Claude 包装示例 | internal/providers/claude/resident.go:1258 `base = fmt.Errorf("claude process exited with error: %w (stderr: %s)", waitErr, tail)` |
| `out of memory` | 范围内未找到直接构造该字面量的生产代码；可来自网关 HTTP 错误正文，不能据此证明本地进程死亡 | internal/agent/llm/openai.go:270 `return nil, &APIError{Status: httpResp.StatusCode, Body: string(body), Retryable: httpResp.StatusCode == 429 || httpResp.StatusCode >= 500}` |

补充透传证据：上述三个非固定信号/内存标记也可能出现在 AGY 的 result.error 中，而不只来自 Wait 或 stderr；Anthropic HTTP 错误正文也会原样进入 APIError。

- internal/providers/agy/agy.go:879 `resultErr = s`
- internal/providers/agy/agy.go:1011 `return fmt.Errorf("agy error: %s%s", resultErr, retrySuffix)`
- internal/agent/llm/anthropic.go:215 `return nil, &APIError{Status: status, Body: string(body), Retryable: status == 429 || status == 500 || status == 502 || status == 503 || status == 529}`
- internal/agent/llm/types.go:89 `return fmt.Sprintf("llm: API error (status %d): %s", e.Status, e.Body)`

后端自身是这些标记的消费者，不是信号的产生者。

- internal/remote/memory_backend.go:6259 `for _, marker := range []string{"signal: segmentation fault", "signal: killed", "process exited", "app-server exited", "codex turn incomplete", "process closed connection unexpectedly", "sigsegv", "out of memory"} {`
- internal/remote/memory_backend.go:6260 `if strings.Contains(text, marker) {`

Copilot 有一个排查细节：连接异常文字只放在错误事件里，而 Run 返回 `copilot turn incomplete...`；后端优先取 Run 错误，所以不是每次连接异常都必然命中该标记。

- internal/providers/copilot/copilot.go:841 `return fmt.Errorf("copilot turn incomplete (stderr: %s)", tail)`
- internal/remote/memory_backend.go:7437 `candidateErr = runErr.Error()`
- internal/remote/memory_backend.go:7443 `candidateErr = errorEventText`

## 两条优先排查路径

### 路径一：AGY 已成功，随后收尾退出失败，被误判为需要重做整轮（推断，优先）

顺序是：收到 result → 关闭 stdin 并开始 15s 宽限计时 → 解析到 SUCCESS/WAITING 并发送 Done → 继续读到 EOF → Wait 返回非零退出 → 返回 `agy process exited...` → 后端清空 session、加 notice、重跑。

- result 到达即开始关闭输入：internal/providers/agy/agy.go:821 `closeTurnInput()`
- 本地收尾宽限计时：internal/providers/agy/agy.go:680 `resultWatchdog = time.AfterFunc(15*time.Second, func() {`
- 本地主动终止：internal/providers/agy/agy.go:681 `_ = killProcessGroup(cmd, syscall.SIGTERM)`
- 强杀兜底：internal/providers/agy/agy.go:683 `_ = killProcessGroup(cmd, syscall.SIGKILL)`
- 终态成功分支：internal/providers/agy/agy.go:863 `case "SUCCESS", "WAITING":`
- 成功终态已发送：internal/providers/agy/agy.go:875 `doneEmitted = true`
- 随后仍判断 Wait 错误：internal/providers/agy/agy.go:941 `if waitErr != nil || resultErr != "" {`
- 把退出编码成崩溃标记：internal/providers/agy/agy.go:1009 `return fmt.Errorf("agy process exited with error: %w%s", waitErr, retrySuffix)`
- 后端收到 Done 不将其记为成功凭据：internal/remote/memory_backend.go:7399 `// 忽略或无需单独处理`
- 后端按错误文本重试：internal/remote/memory_backend.go:7500 `if !crashedRetried && isProviderCrashError(candidateErr) && runCtx.Err() == nil {`
- 后端向用户展示提示：internal/remote/memory_backend.go:7536 `_ = b.AppendNotice(threadID, "Provider 进程意外退出，已用本地历史重建上下文")`
- 后端确实重做而非单纯显示提示：internal/remote/memory_backend.go:7543 `continue`

这属于**协议终态成功之后，进程清理失败被当成任务失败**。进程被信号终止可以是真实事件，但“需要丢掉上下文并重新发送”在这种情况下是误处理。也可能没有走宽限期，而是成功 result 后 CLI 自行 exit(1)；当前代码同样不能区分。

这条路径还有潜在重复工具副作用风险。修复应放在 AGY 适配器保留协议成功终态，不应让后端看到正文就一概忽略错误。

### 路径二：AGY 尚未成功就真实退出，后端重试后答出 666（推断）

AGY 每次尝试都启动独立子进程，不走 Claude 的 resident 池；有任意非零 Wait 结果时，一般失败分支就包含 `process exited`。因此首轮真实异常退出、第二轮成功也完全符合已知现象。

- 每次尝试新建进程：internal/providers/agy/agy.go:510 `cmd = procattr.HideWindow(exec.CommandContext(ctx, bin, allArgs...))`
- 每次设置环境：internal/providers/agy/agy.go:521 `cmd.Env = buildEnv(baseEnv, turn.WorkDir, turn.Env, routeEnv)`
- Wait 来源：internal/providers/agy/agy.go:909 `waitErr := cmd.Wait()`
- 可能有业务错误并非崩溃：internal/providers/agy/agy.go:955 `return fmt.Errorf("agy process exited with error: %w (stderr: %s)%s", waitErr, stderrStr, retrySuffix)`
- 后端只允许一次此类重试：internal/remote/memory_backend.go:7501 `crashedRetried = true`
- 后端丢弃 provider 会话：internal/remote/memory_backend.go:7507 `td.sessionID = ""`
- 后端准备本地历史：internal/remote/memory_backend.go:7539 `currPrompt = buildProviderHandoffPrompt(historyAfterLastSplit(priorMsgs), harnessPrompt)`

下次看到 `done=true` 且无 resultErr，再看到非零 Wait，支持路径一；看到 `done=false` 且信号退出，支持路径二的真实中断；看到普通 exit status 和鉴权/限流错误，说明 `process exited` 的分类过宽。没有原始错误，当前无法在这些情况中定案。

## 新增 Magpie、不重启时的发送路径

### 设置保存

设置页保存的是端点，并刷新 provider/模型列表，不会自动把当前会话切换为该网关的 provider。

- internal/ui/settings_providers.go:293 `saved, err := client.SaveCustomEndpoint(context.Background(), ep, apiKey, webKey)`
- internal/ui/settings_providers.go:304 `refreshCustomEndpointProviders(st, client)`
- internal/ui/settings_providers.go:321 `st.refreshModels()`
- internal/app/custom_endpoints.go:258 `if err := config.Update(c.dataDir, func(latest *config.Config) error {`
- internal/app/custom_endpoints.go:283 `c.backend.RegisterHarness(newCustomEndpointHarness(epCfg, c.secrets, c.dataDir, c.mcp))`

自定义端点执行已走进程内 mcode 引擎，其 Kind 保留为来源 ID；它不是旧 Claude 常驻子进程。

- internal/app/custom_endpoints.go:64 `return newSourceEngineHarness(legacy, fallback, secrets, dataDir, mcpInjection)`
- internal/app/engine.go:37 `opts.Kind = kind`
- internal/app/engine.go:72 `current := mcodeCurrentConfig(cfg, secrets)`

### Claude resident：env 固定，但变更已进入复用指纹

- 每轮重新读配置：internal/app/agent_magpie.go:24 `cfg, err := config.Load(dataDir)`
- 每轮在启动计划之前注入：internal/providers/claude/claude.go:547 `turn, err = magpieTurn(turn, h.opts.MagpieRoutes())`
- 再决定 resident 复用：internal/providers/claude/claude.go:552 `err := h.runAttempt(ctx, turn, emit)`
- 端点地址进入 turn.Env：internal/providers/claude/magpie.go:32 `"ANTHROPIC_BASE_URL=" + magpieinject.AnthropicBaseURL(baseURL),`
- token 也进入 turn.Env：internal/providers/claude/magpie.go:33 `"ANTHROPIC_AUTH_TOKEN=" + apiKey,`
- 指纹读取本轮环境：internal/providers/claude/resident.go:188 `env := append([]string{}, buildEnv(loginenv.Environ(), turn.WorkDir, turn.Env)...)`
- 环境属于指纹：internal/providers/claude/resident.go:201 `strings.Join(env, "\x01"),`
- 只有指纹和 session 都相同才复用：internal/providers/claude/resident.go:401 `case !cur.dead && cur.key == spec.key && cur.sessionID == turn.SessionID && !h.expiredLocked(cur):`
- 不匹配时的已有重建原因：internal/providers/claude/resident.go:420 `reason = "launch-changed"`
- 启动时固定子进程环境：internal/providers/claude/resident.go:721 `cmd.Env = buildEnv(baseEnv, turn.WorkDir, turn.Env)`
- 已有相关测试：internal/providers/claude/magpie_test.go:114 `func TestMagpieTurnChangesResidentLaunchKey(t *testing.T) {`

模型、effort、权限可热切换，但地址和 key 没有从指纹剔除。新增端点被选中且 inject 路由有效时，不能仅因为未重启就推断会用旧端点。未识别的路由前缀确实会原样交给 CLI，但这是路由未命中，不是 resident 存了一份旧路由表；也没有代码证明 CLI 会因此 segfault。

- internal/providers/claude/magpie.go:57 `return MagpieRoute{}, "", nil`
- internal/providers/claude/magpie.go:82 `return turn, nil`
- internal/providers/claude/resident.go:1030 `if err := send("set_model", map[string]any{"model": want.model}, func(s *softSettings) { s.model = want.model }); err != nil {`

Claude 的 global 模式另有边界：全局配置文件内容不直接进入上述 argv/env 哈希，而全局重写入口只写文件。该模式需要另行确认 CLI 是否热读文件，不能用 inject 模式的证据保证它。它也无法解释已识别出的 AGY 图标，故不作为本次首选修复。

- internal/app/agent_magpie.go:34 `if routing.Mode != config.MagpieModeInject || !routing.Active() {`
- internal/app/agent_magpie_settings.go:214 `BaseURL:    magpieinject.AnthropicBaseURL(target.BaseURL),`
- internal/app/agent_magpie_settings.go:594 `err = magpiecfg.Reapply(dataDir, plan)`

### Codex 与 Pi

- Codex 每轮重新注入：internal/providers/codex/codex.go:1137 `turn, route, err = magpieTurn(turn, h.opts.MagpieRoutes())`
- Codex 路由键按每轮路由生成（键的内容为 ProviderID、BaseURL、APIKey 的哈希）：internal/providers/codex/codex.go:625 `proc, dead := h.pickLocked(turn, magpieRouteKey(route))`
- Codex 复用检查路由键：internal/providers/codex/codex.go:462 `return p.workDir == turn.WorkDir && p.sandbox == turn.Sandbox && p.routeKey == routeKey`
- Pi 每轮取最新注入清单：internal/providers/pi/run.go:66 `spec.magpieBridge, spec.magpieEnv = h.opts.magpieInjection()`
- Pi 将清单计入指纹：internal/providers/pi/run.go:109 `spec.magpieEnv,`

### mcode 内置引擎与未知端点

mcode 每次解析读取当前配置。普通未知端点错误不含任何硬编码崩溃标记，不会自然触发这条提示。

- internal/app/mcode_agent.go:298 `current := mcodeCurrentConfig(cfg, secrets)`
- internal/app/mcode_agent.go:299 `return resolveMcodeClient(current, mcodeCurrentSecrets(secrets), model, affinityKey)`
- internal/app/mcode_agent.go:172 `return nil, "", fmt.Errorf("没有找到模型 %s 对应的 Magpie 端点，请在设置中连接 Magpie", model)`
- OpenAI 客户端配置只设置地址等字段：internal/app/mcode_agent.go:180 `BaseURL:       target.BaseURL,`
- Claude 模型走 Anthropic HTTP 客户端：internal/app/mcode_agent.go:216 `return llm.NewAnthropic(clientCfg), modelID`
- 其他模型走 OpenAI HTTP 客户端：internal/app/mcode_agent.go:220 `return llm.NewOpenAI(cfg), modelID`

但上游 HTTP 正文会进入 APIError，最终原样返回后端：上游若返回 `out of memory` 或 `process exited`，当前字符串分类会把远端服务错误误认成本地 provider 崩溃。模型字符串本身也被插值进未知端点错误，所以不能说任意模型输入下“绝不可能命中”；普通 Magpie 模型 ID 不会凭空生成这些标记。

- internal/agent/llm/types.go:89 `return fmt.Sprintf("llm: API error (status %d): %s", e.Status, e.Body)`
- internal/agent/agent.go:249 `return fail(err)`
- internal/providers/mcode/mcode.go:232 `return runErr`

这属于源码可达的另一类误判，不是已证实的本次路径。仅凭“Magpie-tomax 已连接”不能把它排到有 AGY 图标证据的路径之前。

## 编号修改步骤

以下是后续 edit bee 可以直接实施的最小方案。本次未实施。步骤 1 留下实证；步骤 2 修复源码已明确可达的 AGY 成功收尾误处理；步骤 3 为对应回归测试。先不替换整套错误分类机制。

1. **补两处诊断日志，保留原始错误语义并复用现有脱敏。**

   - 文件 `internal/remote/memory_backend.go`。行锚点如下；在该行之后、清空 session 之前添加 `log.Printf`，格式固定为 `provider crash recovery: thread=%s turn=%s provider=%s model=%q session_present=%t run_error=%t error_kind=%q err=%q`。参数依次为 `threadID`、`turnID`、`h.Kind()`、`turn.Model`、`currSessionID != ""`、`runErr != nil`、`candidateErrKind`、`RedactSecrets(candidateErr)`。这样能关联真正失败的 provider，并区分 Run 错误与错误事件。不要打印 Env、APIKey 或完整启动参数。

   internal/remote/memory_backend.go:7501 `crashedRetried = true`

   - 文件 `internal/providers/agy/agy.go`。在 `combinedErr` 计算之后、既有失败处理之前，对 `waitErr != nil || resultErr != ""` 写一条日志：`agy: turn exit thread=%s conversation=%s done=%t result_error=%q wait_error=%q stderr=%q`。参数使用 `turn.ThreadID`、`conversationID`、`doneEmitted`、脱敏后的 `resultErr`、脱敏后的 `fmt.Sprint(waitErr)`、脱敏后的 `stderrStr`。复用 `mcode/internal/redact` 的 `redact.Secrets`，文件 import 按需加入它。该日志在路径一被修复为成功后也必须保留，便于解释额外耗时。

   internal/providers/agy/agy.go:939 `combinedErr := strings.TrimSpace(rawErrStr + "\n" + stderrStr)`

   - 复用依据：remote 已提供包装，不另写脱敏器。

   internal/remote/redact.go:7 `return redact.Secrets(s)`

2. **让 AGY 协议成功终态优先于退出清理错误，避免成功轮重做。**

   - 文件 `internal/providers/agy/agy.go`，在步骤 1 的 AGY 日志之后、现有失败分支之前，加入 `if waitErr != nil && doneEmitted && resultErr == "" { return nil }`。复用当前 `doneEmitted`，不要另建“收到正文即成功”的推断。这里 `doneEmitted` 在 SUCCESS/WAITING 分支设为 true；后面的网络错误兜底虽然也设置它，但随即返回，不会回到此检查。
   - 此判断必须留在既有 `ctx.Err()` 检查之后，用户取消仍返回取消错误。`resultErr != ""` 的权限拒绝、ERROR result、正文中断均沿用原逻辑。不要在 `memory_backend.go` 全局忽略 Done 后错误，内置 agent 的 defer 也会发送 Done，不能将所有 provider 的 Done 都视为成功证明。
   - 该选择把合法 SUCCESS/WAITING 视为轮次完成凭据：它同时覆盖本地宽限期发信号和 CLI 在成功后非零退出。成功后的清理失败只留诊断日志，不清空 session，不重发。它并不缩短现有 15s 等待；如果后续实证显示收尾延迟普遍，再单独处理性能问题。

   internal/providers/agy/agy.go:941 `if waitErr != nil || resultErr != "" {`

   internal/providers/agy/agy.go:917 `if ctx.Err() != nil {`

   internal/providers/agy/agy.go:875 `doneEmitted = true`

   internal/agent/agent.go:121 `emit(harness.Event{Type: harness.EventDone, StopReason: stopReason})`

3. **新增与更新回归测试，复用现有 fake 子进程和 runtimeBackend。**

   - 文件 `internal/providers/agy/agy_test.go`：在 `runFakeAgy` 添加 `success_nonzero_exit` 模式，输出 init、SUCCESS result（response 为 `666`），随后 `os.Exit(1)`。添加 `TestAgy_SuccessResultSurvivesNonzeroExit`：Run 返回 nil；正文精确为 `666`；Done 仅一次；无 EventError；日志有 `done=true` 和非零 wait_error。再用 WAITING 作表驱动子例，验证既有成功语义不变。
   - 同文件添加 `failure_nonzero_exit` 模式，输出 ERROR result 后 exit(1)；添加 `TestAgy_FailedResultStillReturnsExitError`：Run 非 nil 且含 `process exited`，不得因已有正文而成功。另加“输出正文但没有 SUCCESS/WAITING result 就 exit(1)”子例，确保未完成的任务仍报错。
   - 更新 `TestAgy_DeniedActionWithoutOutputProducesError`，加入 SUCCESS result 携带 denied_actions 后 exit(1) 的子例：`resultErr` 不为空，仍返回错误。更新 `TestAgy_ClosesStdinAfterResult`：除既有正文和 Done 断言外，加无 EventError 断言，保留 stdin EOF 正常退出路径。
   - 文件 `internal/remote/runtime_regression_test.go` 添加 `TestRuntimeRegression_ProviderExitRetriesOnceWithHistory`。使用 `customRunHarness{kind: "agy"}`，首轮返回 `errors.New("agy process exited with error: signal: killed")`，第二轮输出 `666` 并成功。断言仅两次 Run、只有一条崩溃 notice、第二次 turn.SessionID 为空、已有前轮历史仍在、最终轮次完成。以此保证步骤 2 没有通过禁用后端恢复来隐藏真实退出。
   - 同文件添加 `TestRuntimeRegression_ProviderCrashRecoveryLogsCause`：捕获并恢复标准 logger 的输出，首轮返回带假 Bearer token 的退出错误，第二轮成功；断言日志含 thread、turn、`provider=agy`、模型、退出原因，且不含假 token。该测试不使用 `t.Parallel`，避免全局 logger 干扰。
   - 若要专门验证本地宽限期强杀，新增 `TestAgy_SuccessResultSurvivesForcedCleanup`：将 `agy.go` 中 15s 提为包内 `resultExitGrace`（默认保持 15s，沿用端点测试现有的可缩短时限做法），测试暂设较短值并用 Cleanup 还原；fake 输出 SUCCESS 后即使 stdin EOF 也等待，由适配器终止；断言 Run 为 nil 且无额外正文或错误事件。fake 自身与父 context 均需有上限，该测试不并行。这个时限提取只服务真实子进程回归，不引入公共 Options 字段。

   复用锚点：

   internal/providers/agy/agy_test.go:75 `func runFakeAgy(mode string) {`

   internal/providers/agy/agy_test.go:1027 `func TestAgy_ClosesStdinAfterResult(t *testing.T) {`

   internal/providers/agy/agy_test.go:1109 `func TestAgy_DeniedActionWithoutOutputProducesError(t *testing.T) {`

   internal/remote/runtime_regression_test.go:21 `func runtimeBackend(t *testing.T, h harness.Harness) *MemoryBackend {`

   internal/remote/runtime_state_test.go:665 `type customRunHarness struct {`

   internal/app/custom_endpoints.go:22 `var customEndpointTestTimeout = 10 * time.Second`

4. **只在日志证实是 API 错误误分类时，追加 APIError 排除；不与 AGY 根因混写。**

   - 文件 `internal/remote/memory_backend.go`，在崩溃判断之前定义 `var apiErr *llm.APIError`，用 `errors.As(runErr, &apiErr)` 得到是否为结构化 API 错误；崩溃分支增加排除此类错误的条件，复用 `mcode/internal/agent/llm` 的现有类型，保留普通错误结束和已有重试流程。不要简单删除所有 `process exited` 标记，以免破坏真退出恢复。
   - 文件 `internal/remote/runtime_regression_test.go` 添加 `TestRuntimeRegression_APIErrorIsNotProviderCrash`，表驱动 APIError.Body 为 `out of memory`、`process exited`，Kind 分别为 mcode 与自定义来源。直接从 mock 返回 `*llm.APIError`（及 `%w` 包装子例），断言后端仅调用一次、不追加崩溃 notice、不清 session、保留原始 API 错误。注意这只数后端 Run 次数，真实 agent 内部对 Retryable 的有限重试仍保留。
   - 仅错误事件没有结构化 runErr 的路径不能靠 `errors.As` 完整解决。若日志证实该分支，需要再给 harness 错误事件加明确类别；本方案不先做全 provider 的错误协议重构。

   internal/agent/llm/types.go:82 `type APIError struct {`

   internal/remote/memory_backend.go:7500 `if !crashedRetried && isProviderCrashError(candidateErr) && runCtx.Err() == nil {`

## 是否应在保存网关后回收常驻进程

**本轮建议：不为 inject 模式额外回收。** Claude、Codex、Pi 已有路由复用键；AGY 不使用 Claude resident；自定义端点由内置引擎驱动。保存时只因“有端点改动”就重启全部 provider，缺少因果证据。

现成的回收机制和边界如下，若以后实证为 global 配置过期，可直接复用：

- app 层替换入口：internal/app/provider_settings.go:225 `func (c *Client) releaseReplacedHarness(kind string) {`
- 先回收空闲进程：internal/app/provider_settings.go:231 `closer.CloseIdle()`
- 再注销旧 harness：internal/app/provider_settings.go:234 `c.backend.UnregisterHarness(kind)`
- Claude 的 CloseIdle 会将旧 harness 标记为关闭，忙进程收尾再退出，不能只调用它却继续永久保留同一个 harness：internal/providers/claude/resident.go:610 `h.closed = true`
- 单会话空闲回收：internal/providers/claude/resident.go:628 `func (h *Harness) EvictThread(threadID string) {`
- 自动指纹变化已有重建：internal/providers/claude/resident.go:432 `h.closeResident(stale)`
- 空闲 TTL 回收有日志：internal/providers/claude/resident.go:561 `h.logf("claude: resident reaped thread=%s idle>=%s", p.threadID, h.opts.IdleTTL)`

如果后续证据明确要求“端点保存后刷新使用全局配置的 Claude harness”，准确接入顺序是：

1. 在 `internal/app/custom_endpoints.go` 的 config.Update 成功分支之后，且现有自定义端点 RegisterHarness 逻辑完成之后，重新加载 cfg；仅针对被修改的 Magpie 端点确实被某个 global agent 选择、且 URL/key/enabled 等运行配置发生变化时处理，名称等展示字段变化不回收。失败返回路径不能动运行中的 harness。启用/删除要同步覆盖，避免只有保存生效。
2. 先复用 `agentMagpieReapplyGlobal` 将全局文件写成新端点配置；仅销毁进程而不重写文件，会让下一进程仍读旧配置。全局文件写入成功后再调用 `c.releaseReplacedHarness(config.ProviderClaude)`，以最新配置构造并 RegisterHarness 新的 Claude harness。`buildProviderHarness` 当前不支持 Claude，不能套用它而让 Claude 被注销后不再注册；构造应复用启动处 `claude.New(claude.WithResident(...))` 的 Binary、MCP、MagpieRoutes 和 resident 配置。
3. UI 的 `saveCustomEndpoint` 保持调用后端；回收不放在 UI goroutine，也不使用会杀掉正在执行轮次的 `Close()`。还需在 global 的重新应用成功入口处理模式/勾选变化，而非只覆盖端点保存。
4. 对应条件实施时，在 `internal/app/provider_settings_test.go` 新增 `TestSaveCustomEndpoint_ReplacesAffectedGlobalHarness`：记录 CloseIdle/注册调用，断言 URL/key 变化会替换、仅改名不替换、保存失败不替换；并验证忙进程由旧 harness 完成，新轮使用新 harness。此项未获运行日志支持，不属于本轮建议立即修改的步骤。

接入行锚点：

- internal/app/custom_endpoints.go:279 `magpie.InvalidatePersonaCache(magpie.PersonaCacheKey(id, personaWebURL))`
- internal/app/custom_endpoints.go:283 `c.backend.RegisterHarness(newCustomEndpointHarness(epCfg, c.secrets, c.dataDir, c.mcp))`
- internal/app/agent_magpie_settings.go:580 `func (c *Client) agentMagpieReapplyGlobal(ctx context.Context, dataDir, agent, saved string, secrets config.SecretStore) error {`
- internal/app/app.go:1043 `primaryHarness = claude.New(claude.WithResident(claude.Options{`
- internal/app/app.go:1824 `func buildProviderHarness(cfg config.Config, secrets config.SecretStore, id string, mcpInjection providerMCP) (harness.Harness, bool) {`

## 如何拿到实证

下次复现前先合入步骤 1 的日志。复现时记录发送时间、当前会话 provider、选中的完整模型 ID、Magpie 模式（inject/global/off）和端点 ID；不要只记网关显示名。查看 `~/.mcode/logs/mcode.log` 对应时间的以下信息：

- `provider crash recovery:`：真正触发恢复的 provider、model、原始错误（经过现有脱敏）、是否来自 Run。
- `agy: turn exit`：该次失败是否已经发出成功 Done，result_error、wait_error 和 stderr 分别是什么。
- 若 provider 实际为 Claude：关联同 thread 的 `claude: resident spawn`、`claude: resident rebuild` 与 reason、`claude: resident hot switch`、`claude: resident reaped`。spawn/reaped 本身不是异常证据，不能拿另一个线程的 resident 日志解释当前会话。

判定方式：

| 日志组合 | 能支持的判断 |
| --- | --- |
| `provider=agy`，`done=true`，result_error 为空，Wait 非零或信号退出 | 路径一：成功后的收尾错误；不应重建上下文或重发 |
| `provider=agy`，`done=false`，Wait 为 segmentation fault/SIGKILL | 路径二：完成前真实退出；仍需判断是否本地主动终止 |
| 普通 exit status + 鉴权、限流、参数错误 | 进程退出是真实的，但归为不可恢复的“崩溃”不准确；保留业务原因再决定重试 |
| `llm: API error` + 正文含标记 | API 错误命中字符串；不能证明本地 provider 进程崩溃 |
| `provider=claude` + inject 的地址/key 变化后出现 `launch-changed` | 已走已有进程替换机制，不能归因为继续沿用旧 resident env |
| global 模式旧进程仍使用旧配置，重写并换进程后恢复 | 才支持配置过期方向；再执行上面的条件回收方案 |

若要严格证明“本地主动强杀”，可在 AGY 宽限期回调发 SIGTERM 前加同 thread、conversation、PID 的 `agy: result cleanup timeout` 日志，再与非零 Wait 日志关联；直接读跨 goroutine 的普通 bool 会产生数据竞争，不为日志临时引入这种状态。本轮所列 AGY 日志足以区分成功收尾与完成前失败，但不能单独证明是哪一方发出了信号。

`memory_backend.go` 现有恢复分支没有记录 candidateErr；因此**需要**在提示附近、清空 session 之前补步骤 1 的日志。只改提示文案或只看最终的 `666` 都不能补回丢失的错误证据。

## 未完成与交接注意事项

- 没有此次错误原文、存续会话或逐次 provider 事件，未确认本次命中哪一个标记；没有断言 Magpie 本身故障。
- 没有修改上述任何 Go 文件；所有修改步骤、测试名和条件方案均待主代理安排实施。
- 没有运行构建、测试、lint、类型检查或验证命令，也没有启动后台任务。代码锚点来自本工作副本的只读工具输出。
- 主代理优先审阅并实施步骤 1、2、3。步骤 4 和 global 回收方案以新增日志证据为条件，避免把未经证实的多条路径一起改掉。
