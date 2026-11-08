# 旧总管代码删除计划

日期：2026-11-08。状态：调研完成，待执行。本文只制定删除步骤，没有运行构建、测试、lint 或类型检查。

调研对象是当前独立工作副本。任务给出的原仓库目录不在读取工具允许范围内，因此没有核对原仓库与工作副本是否一致。下文行号均来自当前工作副本；执行者应按符号定位，不应按固定行数截文件。

## 1. 删除边界与实施顺序

删除常驻运行时、事件唤醒、日报、巡检、账本、话题、总管动作工具、审批建议和文字决策置顶条。保留前台、feed、编号决策卡、任务结果路由、记忆、注意力事件源和收件箱。依据：

- docs/superpowers/plans/2026-11-08-steward-noop-redesign.md:8 ``本文把「总管」（代码里的 `steward`）从常驻会话改成 noop 那样的前台：每条用户消息做一次便宜、短、无状态的模型调用，由代码现拼上下文，模型只碰少量读工具与安全写工具；真正干活的是任务会话。旧的总管运行时、账本、话题、直通、唤醒、巡检、动作工具、置顶决策条全部退役。``
- docs/dev/05-steward-attention.md:133 ``注意力收件箱原样保留：`internal/attention` 负责事件存储、`dedupe_key` 去重和每日上限，接线在 `internal/app/attention_wiring.go`。``
- internal/app/app.go:1399 `wireFrontdesk(appInstance, opts.DataDir)`

采用四批，文件所有权互斥：**批次 1 → 批次 2 与批次 4 可并行 → 批次 3**。批次 3 只依赖批次 1、2，可以与尚未结束的批次 4 并行。不能四批从同一基线同时执行并各自保持可编译：批次 2 删除的 UI 类型仍被批次 1 的 app 适配引用，批次 3 删除的配置字段仍被批次 1、2 读取。每批交付边界必须包含该批全部文件；不允许把“删定义”和“删引用”拆成两个可交付状态。

关键证据：

- internal/app/client_steward.go:21 `var _ mcodeui.StewardTopicsClient = (*Client)(nil)`
- internal/app/steward_plan.go:139 `Provider:    cfg.PlanProvider,`
- internal/ui/settings_page_steward.go:330 `return &cfg.Orchestration.Steward.PassthroughOff`

本计划不把所有包含 `steward` 的代码一律删除。新 feed、决策表、前台配置仍沿用该命名。历史配置、历史会话和数据库迁移仍须可读；不删数据库表、不清空 settings、不重写用户历史。

- internal/app/frontdesk_migration.go:13 `oldID, _, err := db.GetSetting(ctx, "steward.thread_id")`
- internal/app/frontdesk_adapters.go:55 `goal = store.Goal{ID:uuid.NewString(), ProjectID:projectID, Title:"总管", Prompt:"总管", Status:"running", Owner:"steward", Policy:map[string]any{"maxConcurrency":1}}`

## 2. 文件处置清单

以下每项先给处置和原因，再给源码锚点。整删文件里的私有函数、常量、类型随文件一起删除；不逐一为内部实现创建替代物。外部引用的处置集中列在第 3 节。

### 2.1 `internal/steward`：整个包删除

生产文件逐个整删：

- `internal/steward/steward.go`：常驻会话与事件循环。internal/steward/steward.go:375 `func (r *Runtime) Ensure(ctx context.Context) (string, error) {`
- `internal/steward/actions.go`：旧动作次数护栏。internal/steward/actions.go:41 `type ActionGuard struct {`
- `internal/steward/hooks.go`：旧 MCP 接线；没有新前台调用者。internal/steward/hooks.go:21 `func (r *Runtime) Hooks(`
- `internal/steward/ledger.go`：旧账本。internal/steward/ledger.go:21 `const settingLedger = "steward.ledger"`
- `internal/steward/passthrough.go`：后台事件直通模型。internal/steward/passthrough.go:207 `func (r *Runtime) routeEvent(ctx context.Context, ev attention.Event) bool {`
- `internal/steward/prompt.go`：旧身份、话题阶段及开场提示；先迁出普通会话移交提示的语义。internal/steward/prompt.go:68 `func OrdinaryHandoffSection(name string) string {`
- `internal/steward/receipt.go`：话题结项回执。internal/steward/receipt.go:37 `func (r *Runtime) ReceiptTopic(ctx context.Context, threadID string) (bool, error) {`
- `internal/steward/rename.go`：运行时同步名字；前台展示继续直接读配置。internal/steward/rename.go:31 `func (r *Runtime) syncNames(ctx context.Context, cfg config.StewardConfig, threadID string) {`
- `internal/steward/rotation.go`：常驻模型上下文换新。internal/steward/rotation.go:64 `func (r *Runtime) ShouldRotate(threadID string, idle time.Duration, contextTokens int64) bool {`
- `internal/steward/topic_scope.go`：话题动作归属判定。internal/steward/topic_scope.go:7 `func (r *Runtime) TopicOwnsSubject(ctx context.Context, topicThreadID, subjectType, subjectID string) (bool, error) {`
- `internal/steward/topics.go`：旧话题模型与生命周期。internal/steward/topics.go:23 `const settingTopics = "steward.topics"`
- `internal/steward/wake.go`：唤醒文本与巡检文本。internal/steward/wake.go:48 `func buildWakeMessage(events []attention.Event, deferred bool) string {`

该目录所有测试随包整删，完整名单见批次 1 清单。其他包对 `Runtime`、`Topic`、`RotationBee`、`ActionGuard`、`DefaultAutoCloseAfter` 等的引用必须在同批清除，不能留下空壳包。

### 2.2 app 层

- `internal/app/steward_actions.go`：整删。仅服务于旧总管催办、中断、重跑自动化和移卡。internal/app/steward_actions.go:99 `type stewardActions struct {`
- `internal/app/steward_assign.go`：整删。新前台由任务服务派发。internal/app/steward_assign.go:39 `func (p *stewardAssigner) Request(ctx context.Context, threadID string, req mcp.AssignTaskInput) (mcp.AssignTaskResult, error) {`
- `internal/app/steward_plan.go`：整删。新设计以普通方案任务承担规划。internal/app/steward_plan.go:96 `func (p *stewardPlanner) Request(ctx context.Context, threadID string, req mcp.PlanRequestInput) (mcp.PlanRequestResult, error) {`
- `internal/app/steward_suggestion_stats.go`：整删。下线旧审批建议统计 UI，保留 store 的历史评分数据。internal/app/steward_suggestion_stats.go:24 `func (c *Client) StewardSuggestionStats(ctx context.Context) ([]mcodeui.SuggestionCategoryScore, error) {`
- `internal/app/steward_suggestions.go`：整删；同步清理收件箱中建议的装配和确认路径。internal/app/steward_suggestions.go:158 `func (c *Client) SuggestApproval(ctx context.Context, threadID, requestID, optionID, reason string) error {`
- `internal/app/steward_wiring.go`：先部分迁出，再整删。`ErrOwnerNotifySuppressed` 和 `SetOwnerNotifier` 是新前台仍需要的公共通知边界；其余运行时、身份、话题、动作、自身告警逻辑删除。internal/app/steward_wiring.go:544 `func (a *App) SetOwnerNotifier(fn func(title, body, threadID string) error) {`
- `internal/app/remote_steward.go`：部分删。保留 `steward.open` 对应的 `Open` 与服务注册；接口收窄为一个 `OpenStewardThread` 方法。删话题方法、修订冲突专用映射、去抖器和 `SetStewardWatcher` 接线。internal/app/remote_steward.go:41 `return s.client.OpenStewardThread(ctx)`
- `internal/app/client_steward.go`：部分删。保留 `StewardEnabled`、`StewardIdentity`、`OpenStewardThread`；重写 `StewardThreadID` 为 feed 查询；删所有 runtime 注入、唤醒计数、话题接口和话题操作。internal/app/client_steward.go:129 `return c.backend.EnsureStewardFeedThread(ctx, projectID)`
- `internal/app/goal_review_wake.go`：整删。`wireGoalReviewWake` 自身仍依赖旧 runtime，不能把它接回新前台；后台事件不得触发模型。该函数在当前 `app.go` 已无接线调用，清除 App 的字段与停止分支即可。internal/app/goal_review_wake.go:225 `func wireGoalReviewWake(a *App, stewardRT *steward.Runtime) *goalReviewWaker {`
- `internal/app/app.go`：部分删。删旧 runtime 导入、字段、getter、清理闭包及身份接线；保留通知锁、回调、注意力接线和 `wireFrontdesk`。internal/app/app.go:111 `steward     *steward.Runtime`
- `internal/app/client.go`：部分删。删 `steward` 和 `suggestions` 字段、Respond 后清建议分支。保留 `recordRejectPreference`：新前台创建的目标仍写 `Owner:"steward"`，把 `goalOwnerSteward` 常量迁到此函数附近，不能随 waker 丢失。internal/app/client.go:1718 `if err != nil || goal.Owner != goalOwnerSteward {`
- `internal/app/swarm_wiring.go`：部分删。删总管身份来源与 policy 包装；保留 Swarm 的完整工具、配额、子代理规则和通用 `prefaceDecorator`。internal/app/swarm_wiring.go:1460 `func prefaceDecorator(preface func(threadID string) string, decorate func(threadID, prompt string) string) func(threadID, prompt string) string {`
- `internal/app/client_attention.go`、`internal/app/remote_attention.go`：部分删，只删审批建议数据装配、订阅和转码。保留事件列表、计数、消解、事件订阅以及当前 `OwnerActionKinds` 策略，避免借删除扩大收件箱行为变更。internal/app/client_attention.go:55 `suggestions := c.pendingApprovalSuggestions(ctx)`
- `internal/app/remote_service.go`：部分删，只删话题 watcher 字段、方法、启动和停止调用。保留远程服务及 `SendOwnerPush`。internal/app/remote_service.go:264 `func (s *RemoteService) SendOwnerPush(title, body, threadID string) bool {`
- `internal/app/frontdesk_wiring.go`、`internal/app/frontdesk_adapters.go`、`internal/app/frontdesk_memory.go`、`internal/app/frontdesk_migration.go`：保留。第一份文件允许补通知安静时段检查，其余不改。internal/app/frontdesk_wiring.go:68 `desk:=&frontdesk.Desk{Client:frontdeskClient{dataDir:dataDir},Feed:services,Tasks:services,Decisions:services,Memory:newFrontdeskMemory(a.store,dataDir),Projects:services,ProjectPath:home,CLIFallback:cfg.Orchestration.Steward.CLIFallback,Generator:textgen.GenerateWithOptions,CLIOptions:textgen.Options{Provider:cfg.Orchestration.Steward.Provider}}`

### 2.3 MCP、remote、wire、存储与 policy

- `internal/mcp/tools_steward.go`：整删，包括旧专属工具、数据类型、白名单、guard、Server hook。新前台直接使用 `internal/frontdesk` 的工具，不重新注册同名旧工具。internal/mcp/tools_steward.go:657 `func (r *Registry) registerStewardTools(s *Server) {`
- `internal/mcp/registry.go`、`internal/mcp/server.go`、`internal/mcp/tools_automation.go`、`internal/mcp/tools_kanban_entry.go`：部分删；具体闭包引用见第 3 节。保留普通会话族入口与项目作用域校验。internal/mcp/registry.go:65 `r.registerStewardTools(s)`
- `internal/remote/steward_topics.go`：部分删。只保留 `steward.open` 的注册、参数校验、一个方法的服务接口与 handler；删所有 `steward.topics.*`、`steward.changed`、话题表读取、话题归属校验、专用冲突错误。为减少迁移可继续使用原文件名，后续不必为命名再搬文件。internal/remote/steward_topics.go:84 `RegisterMethod(stewardOpenMethod, handleStewardOpen)`
- `internal/remote/steward_tools.go`：整删。旧持久化会话 ID / 话题表提供的 MCP 免审批路径下线；不影响新前台的 Go 工具循环。internal/remote/steward_tools.go:110 `func (a *threadApprover) MCPToolAllowed(ctx context.Context, toolName string) bool {`
- `internal/remote/steward_score_trace.go`：整删。清除其在审批答复、取消、withdraw、启动恢复中的写入。internal/remote/steward_score_trace.go:68 `func TraceSuggestionRegistered(b *MemoryBackend, score store.StewardApprovalScore) {`
- `internal/remote/memory_backend.go`：部分删旧唤醒、跨项目附加目录、静默标记、评分调用和旧 predicate；保留新 `stewardDesk` 与发送分流。internal/remote/memory_backend.go:2883 `return b.acceptStewardMessage(ctx, params, desk)`
- `internal/remote/approvals.go`、`internal/remote/withdraw.go`、`internal/remote/boot_reconcile.go`、`internal/remote/session_alerts.go`：部分删旧豁免、评分与静默判断，保留普通审批、请求撤销、启动恢复和告警主体。internal/remote/approvals.go:258 `if req.Sensitive == "" && stewardTool && kind == harness.RequestKindTool {`
- `internal/remote/wire/steward_topics_types.go`：部分删，只留 `StewardOpenParams`、`StewardOpenResult`。internal/remote/wire/steward_topics_types.go:30 `type StewardOpenParams struct {`
- `internal/remote/wire/types.go`：部分删 `StewardSilentMarker`、`LastTurnSilent`；保留 `IsSteward` 并使其表示新 feed。internal/remote/wire/types.go:307 ``LastTurnSilent bool `json:"lastTurnSilent,omitempty"` ``
- `internal/remote/steward_feed.go`：保留，并新增不读磁盘的 feed ID getter；复用 feed 的早返回分支也要恢复内存 ID。internal/remote/steward_feed.go:35 `if _, err := b.store.GetThread(ctx, id); err == nil { return id, nil } else if !errors.Is(err, store.ErrNotFound) { return "", err }`
- `internal/remote/steward_decisions.go`、`internal/kanban/decisions.go`、`internal/store/steward_decisions.go`：保留，属于新编号卡链路。internal/remote/steward_decisions.go:37 `RegisterMethod(stewardDecisionsListMethod, handleStewardDecisionsList)`
- `internal/store/steward_scores.go` 及其测试、相关数据库迁移：保留历史表和查询实现，停止新写入；这是原设计明确保留的影子评分基线，不作为旧运行时继续接线。docs/superpowers/plans/2026-11-08-steward-noop-redesign.md:47 ``| 审批建议计分 | `internal/store/steward_scores.go:35` `type StewardApprovalScore struct {` | 原样保留但停止写入 | 历史数据留作 judge 阶段的影子评分基线。 |``
- `internal/harness/policy/policy.go`：部分删 `Capabilities.Steward`、`Sections.Steward` 和渲染插入点，更新关联测试；新前台自己构造上下文。internal/harness/policy/policy.go:71 `appendSection(caps.Steward, sections.Steward)`
- `internal/kanban/service.go`、`internal/kanban/driver_ext.go`、`internal/remote/kanban_coordinator.go`：部分删仅旧话题需要的自动升权限路径，保留普通目标开工与交互模式设置。internal/kanban/service.go:1240 `s.promoteTopicSession(ctx, started)`
- `internal/mcp/tools_kanban.go`：保留普通看板功能和历史目标的验收保护；将 `goalReviewGuard` 的报错从已删的 `notify_owner` 改为“向用户说明并等待明确确认”。这属于历史任务兼容，不保留话题工具或自动验收。internal/mcp/tools_kanban.go:1185 `res := toolError(CodeForbidden, reason+"需要先问用户：用 notify_owner 说明情况，等用户决定。", false,`

### 2.4 桌面 UI

- `internal/ui/steward_decision_bar.go`：整删。编号决策卡替代自由文本推断。internal/ui/steward_decision_bar.go:12 `const stewardDecisionPrefix = "需要你决定："`
- `internal/ui/steward_topic_card.go`：整删，包括新话题、任务书、验收、关闭与删除话题弹窗。internal/ui/steward_topic_card.go:12 `func renderStewardTopicCard(c *ui.Context, s *chatState, threadID string) {`
- `internal/ui/steward_topic_types.go`：整删话题 DTO 和可选客户端接口。internal/ui/steward_topic_types.go:50 `type StewardTopicsClient interface {`
- `internal/ui/steward_topics.go`：整删话题缓存、watcher、侧栏列表与操作。internal/ui/steward_topics.go:474 `func renderSidebarStewardTopics(c *ui.Context, s *chatState) {`
- `internal/ui/steward_split.go`：部分删，不能整删。删总管专属分割按钮、状态及动作；保留 `splitSessionNotice`、`splitDividerText`、`splitDividerRow`、`renderSplitDivider` 作为历史消息渲染，可留在原文件减少移动。internal/ui/timeline_rows.go:2774 `return renderSplitDivider(c, m)`
- `internal/ui/steward_timeline.go`：部分删，不能整删。删唤醒原因、静默巡视、总管紧凑思考、独立折叠选择和提示行；保留 `reasoningHeadlineRunes`、`reasoningHeadline`、`reasoningHeadlineFull`、`reasoningHeaderTitle`、`reasoningHeadlineText`、`stripOrderedListPrefix`、`reasoningHoldOpen`，它们是普通思考渲染依赖。internal/ui/timeline_rows.go:3226 `if full := reasoningHeadlineFull(m.Text, isLive); full != "" {`
- `internal/ui/sidebar_steward.go`：部分删。保留总管入口、头像、昵称、未读、收件箱徽标和项目过滤。删话题列表调用、`topicThreads` 参数与过滤、过时的“去开启”副标签；未启用时入口仍可打开设置。internal/ui/sidebar_steward.go:209 `renderSidebarStewardTopics(c, s)`
- `internal/ui/steward_client.go`：部分删。保留 `StewardClient`、昵称头像、启用缓存、异步打开与 pending 消费；仅删 `stewardWakesClient`、`stewardWakesToday`，更新“首次创建几十秒”的旧 CLI 注释。internal/ui/steward_client.go:32 `type StewardClient interface {`
- `internal/ui/suggestion_score.go`：整删展示层、评分缓存和今日提醒计数缓存；历史评分存储保留。internal/ui/suggestion_score.go:270 `var stewardScoreSection = settingsSection{`
- `internal/ui/settings_page_steward.go`、`internal/ui/settings_swarm_data.go`、`internal/ui/settings_page_swarm.go`、`internal/ui/settings.go`：部分删无效设置与缓存，详见第 4 节；保留总管设置页及其 sheet 注册。internal/ui/settings_sheet_steward.go:8 `Body:  func(c *ui.Context, st *SettingsState) { renderPageSteward(c, st) },`
- `internal/ui/chat.go`、`internal/ui/sidebar.go`、`internal/ui/palette.go`、`internal/ui/root.go`、`internal/ui/kanban_modal.go`：部分删话题卡、决策条、话题入口和返回条；保留普通聊天、前台回复、编号卡、收件箱和任务板。internal/ui/chat.go:254 `renderStewardDecisionBar(c, s, selectedID, messages)`
- `internal/ui/composer_complete.go`、`internal/ui/composer_usage_panel.go`：部分删总管专属 `/split` 与分割操作入口。internal/ui/composer_complete.go:1087 `splitStewardSession(s, tid)`
- `internal/ui/timeline_rows.go`：部分删 wake / silent 的产生、索引、合并和巡视渲染分支，以及 compact reasoning 分支；保留 host event、通用思考、历史分割线、新前台种类。internal/ui/timeline_rows.go:58 `const stewardDecisionKind = "steward_decision"`
- `internal/ui/attention_client.go`、`internal/ui/attention_inbox.go`：部分删旧审批建议类型、字段、确认按钮和渲染，保留收件箱本身。internal/ui/attention_inbox.go:676 `renderAttentionSuggestion(c, s, item.ID, sug)`
- `internal/ui/steward_reply.go`、`internal/ui/steward_decision_card.go`：保留。internal/ui/steward_reply.go:5 `func renderStewardReply(c *ui.Context, s *chatState, m Message) ui.Element {`

历史显示建议：旧 `steward_wake` 用户消息在 UI 的 hidden-kind 兼容判断中仍隐藏，旧 `[[静默]]` 最终回复可按字面量隐藏，但不再做回合聚合、巡视计数或注入运行态；这是读取旧会话的兼容，不恢复旧执行代码。若要求最终源码连旧字面量都不保留，应单独决定历史展示方式，不能直接把原隐藏输入展示给用户。现有隐藏入口：

- internal/ui/timeline_rows.go:89 `return m.Kind == stewardWakeKind`
- internal/ui/timeline_rows.go:95 `if text == stewardSilentMarker {`

### 2.5 协议与 Web

- `docs/remote-control.schema.json`：部分删所有 `RemoteStewardTopic*` 与 `RemoteStewardChangedNotification` 定义，以及顶层、Request/Response 联合类型中的全部引用；保留 `RemoteStewardOpen*`、`RemoteStewardDecision*`、`isSteward`。更新 message kind 枚举，保留新前台种类；旧 `steward_wake` 如继续用于历史消息传输，标为历史只读，不再宣传为可产生类型。internal/remote/steward_feed.go:63 `msg := wire.ThreadMessage{ID: newUUID(), Role: role, Text: text, CreatedAt: now.Format(time.RFC3339Nano), IsFinalResponse: &final, Media: media}`
- docs/remote-control.schema.json:744 `"$ref": "#/$defs/RemoteStewardTopicsListRequest"`
- docs/remote-control.schema.json:10896 `"$ref": "#/$defs/RemoteStewardTopicsListRequest"`
- docs/remote-control.schema.json:14439 `"RemoteStewardTopic": {`
- docs/remote-control.schema.json:750 `"$ref": "#/$defs/RemoteStewardDecisionsListRequest"`
- `docs/remote-control-protocol.md`：部分删话题方法、话题变更通知；把 `steward.open` 改成打开全局前台 feed（`projectID` 仍表示入口来源），保留决策列表说明。docs/remote-control-protocol.md:55 ``- `steward.open`：打开该项目的总管主会话（没有就新建），结果为 `{ "threadID" }`。``
- `web/src/context/remoteStewardApi.ts`：部分删，只留 `supportsOpen`、`openSteward` 和仍被使用的错误格式化；删除话题调用、类型导出与 changed 订阅。web/src/context/remoteStewardApi.ts:18 `export const REMOTE_STEWARD_OPEN_METHOD = 'steward.open';`
- `web/src/components/StewardTopicRow.tsx`、`web/src/components/StewardTopicSheet.tsx`：整删。web/src/components/StewardTopicRow.tsx:7 `import type { RemoteStewardTopic } from '../types/generated';`
- web/src/components/StewardTopicSheet.tsx:13 `import type { Board, RemoteStewardTopic, Task } from '../types/generated';`
- `web/src/components/ThreadList.tsx`：部分删话题状态、加载、分组、操作和 sheet；保留 `StewardFixedRow`、身份展示与 `openSteward`。web/src/components/ThreadList.tsx:1260 `const threadID = await stewardApi.openSteward(projectID);`
- `web/src/components/ThreadView.tsx`：部分删文字决策条和总管分割入口；保留普通聊天与消息渲染，不删除编号决策卡相关行为。web/src/components/ThreadView.tsx:52 `import { stewardPendingDecision } from './timeline/stewardTimeline';`
- `web/src/components/ThreadTimeline.tsx`：部分删唤醒索引、静默巡视 Item、旧总管工作标题和独立思考偏好；通用思考渲染保留。web/src/components/ThreadTimeline.tsx:58 `import { reasoningHeadline, reasoningHeaderTitle, stewardWakeReason } from './timeline/stewardTimeline';`
- `web/src/components/timeline/stewardTimeline.ts`：部分删，保留通用 reasoning 摘要函数；删 wake、文字决策推断与其消息接口（无剩余使用时）。web/src/components/timeline/stewardTimeline.ts:26 `export const STEWARD_DECISION_PREFIX = '需要你决定：';`
- `web/src/components/Header.tsx`、`web/src/components/SettingsForm.tsx`、`web/src/services/storage.ts`：部分删旧总管思考折叠键、同步 effect、控件和 localStorage accessor；普通会话思考折叠保留。web/src/services/storage.ts:357 `export function getStoredStewardAutoCollapse(): boolean {`
- `web/src/context/remoteSplitApi.ts`、`web/src/components/ThreadComposer.tsx`：部分删总管特有的分割显示与调用，保留通用历史分割线和后端 `threads.split` 协议能力。web/src/components/ThreadComposer.tsx:202 `* 对应桌面 internal/ui/steward_split.go 的 splitSessionView.Show。`
- `web/src/utils/stewardIdentity.ts`：保留，昵称和头像仍有意义。web/src/components/ThreadList.tsx:47 `import { stewardAvatarText, stewardLabel } from '../utils/stewardIdentity';`
- `web/src/types/generated.ts`：通过生成器更新，禁止只手删类型而不改 schema。`web/src/types/contract.ts` 无本次检索命中的 steward 定义，默认保留。web/scripts/generate-contract.mjs:3 `const schema = JSON.parse(readFileSync(new URL('../../docs/remote-control.schema.json', import.meta.url), 'utf8'));`

## 3. 删除符号的外部引用与替换

本节列跨删除单元的引用边界。整删文件之间的调用随同一批删除；例如 Runtime 的私有 ledger/wake/topic 方法不需要替代实现。`internal/steward` 的导入方只有旧 app 适配及其测试，本批删除这些导入，不把旧类型搬进新包。

### 3.1 App 和首轮提示

1. 删 `App.steward`、`App.Steward()`、局部 `stewardRT`、`App.goalWake` 和停止逻辑。保留 `notifyMu`、`ownerNotify`。`wireGoalReviewWake` 当前无调用，不要新增一个接到 Desk 的替代 waker。

   - internal/app/app.go:46 `"mcode/internal/steward"`
   - internal/app/app.go:117 `goalWake *goalReviewWaker`
   - internal/app/app.go:154 `func (a *App) Steward() *steward.Runtime {`
   - internal/app/app.go:179 `if a.steward != nil {`
   - internal/app/app.go:182 `if a.goalWake != nil {`
   - internal/app/app.go:634 `var stewardRT *steward.Runtime`
   - internal/app/app.go:637 `if stewardRT != nil {`

2. `stewardIdentitySection` 删除后，删无 Swarm 分支的 `Sections.Steward`；通知轮 decorate 不再包装身份，使用 `prefaceDecorator(preface, nil)`。有 Swarm 分支调用去掉 `stewardPolicySources` 参数。`prefaceDecorator` 本身仍负责看板前言，不删。

   - internal/app/app.go:882 `Steward: stewardIdentitySection(stewardRT, threadID),`
   - internal/app/app.go:888 `return prependStewardPolicy(stewardIdentitySection(stewardRT, threadID), prompt)`
   - internal/app/app.go:893 `identity: func(threadID string) string { return stewardIdentitySection(stewardRT, threadID) },`
   - internal/app/swarm_wiring.go:1259 `sections.Steward = stewardPolicy(threadID)`
   - internal/app/swarm_wiring.go:1274 `return prependStewardPolicy(stewardPolicy(threadID), swarm.NativeSubagentRule+"\n\n---\n\n"+prompt)`
   - internal/app/swarm_wiring.go:1277 `return prependStewardPolicy(stewardPolicy(threadID), prompt)`
   - internal/app/swarm_wiring.go:1069 `return policy.Capabilities{Swarm: true, Kanban: true, Steward: true, Context: true, Automation: true}`

3. **保留首轮“可以交给总管”提示**，新建 `internal/app/frontdesk_handoff.go`（新增文件，不是现存路径）。实现 `frontdeskHandoffSection(backend, dataDir, threadID)`：空 ID、读配置失败或未启用返回空；识别 feed 返回空；只为普通会话生成短段落，说明可在总管主会话提出需求、由任务执行。名字现读 `cfg.Orchestration.Steward.Label()`。不导入旧包、不读旧话题表、不建立模型会话。现有 bee 排除仍在调用方，建议再用 backend 会话信息排除 helper / 有父会话和 provider 为 `steward` 的 feed。

   将旧文案中的“⇧⌘T / 交给总管”换成实际保留的“打开总管 / 在总管会话中说明需求”，避免指向本计划要删掉的话题快捷键。不要宣称有未实现的定时模型巡检。

   - internal/app/app.go:900 `backend.SetFirstTurnExtra(func(threadID string) string {`
   - internal/app/app.go:906 `return stewardHandoffSection(stewardRT, opts.DataDir, threadID)`
   - internal/app/steward_wiring.go:262 `if rt == nil || strings.TrimSpace(threadID) == "" {`
   - internal/steward/prompt.go:70 `需要跨会话长期跟进、按固定节奏定时做、或要同时派给多个子代理的事，建议用户用 ⇧⌘T 或命令面板里的「交给总管」移交给「%s」。`

4. `goalOwnerSteward` 不是 waker 专属语义；迁到 `internal/app/client.go` 的 `recordRejectPreference` 附近，保留用户打回偏好记录。删 `Client.steward`、`Client.suggestions`，普通 Respond 返回路径直接结束，不做建议缓存通知。

   - internal/app/client.go:124 `steward stewardThreadRuntime`
   - internal/app/client.go:127 `suggestions approvalSuggestionStore`
   - internal/app/client.go:773 `if err == nil && c.suggestions.remove(a.ThreadID, a.RequestID) {`
   - internal/app/goal_review_wake.go:18 `goalOwnerSteward = "steward"`

5. feed ID 不再从 runtime 获取。在 `internal/remote/steward_feed.go` 新增 `StewardFeedThreadID() string`，用 `b.mu` 保护的内存字段读取；`EnsureStewardFeedThread` 新建和复用分支均维护该字段。`Client.StewardThreadID` 读配置确认启用后返回该 getter；getter 不触碰磁盘，不能在每帧读 settings。摘要 `IsSteward` 根据 feed 的 provider / 内存 ID 计算，保留 helper 排除，删除 `SetStewardPredicate` 与旧函数型字段。

   - internal/app/client_steward.go:76 `rt := c.stewardRuntime()`
   - internal/remote/memory_backend.go:275 `stewardFor func(threadID string) bool`
   - internal/remote/memory_backend.go:549 `return b.stewardFor(td.summary.ID)`
   - internal/remote/steward_feed.go:38 `summary := wire.ThreadSummary{ID: newUUID(), ProjectID: projectID, Title: "总管", Provider: "steward", UpdatedAt: now.Format(time.RFC3339Nano)}`

6. `SetOwnerNotifier`、`ErrOwnerNotifySuppressed` 移到新增的 `internal/app/frontdesk_notify.go`，名称保持不变。旧 `notifyOwner`、旧失败提示哨兵和 runtime 自身告警全部删除；新 `notifyFrontdesk` 保留。该公共边界在 cmd 仍有编译引用，不能整删后再让 cmd 改名。

   - cmd/mcode/main.go:522 `a.SetOwnerNotifier(func(title, body, threadID string) error {`
   - cmd/mcode/main.go:528 `if !errors.Is(err, app.ErrOwnerNotifySuppressed) {`
   - cmd/mcode/native.go:305 `return fmt.Errorf("当前处于安静时段，桌面通知未弹出：%w", app.ErrOwnerNotifySuppressed)`
   - internal/app/frontdesk_wiring.go:53 `var err error; if fn!=nil {err=fn(title,body,thread)}`

### 3.2 MCP 删除闭包

1. 删 `registerStewardTools` 注册。删 `Server.stewardMu` / `steward`，以及 call/list 中的旧 scoped 判断；保留普通项目隔离，将 `project_id` 判断还原成不带总管例外的普通判断。删 `topicPlanRole`（仅话题工具列表使用）。不删普通类型化看板工具与族入口。

   - internal/mcp/server.go:91 `steward   *StewardHooks`
   - internal/mcp/server.go:546 `if allowed, scoped := stewardScopedCallAllowed(s.stewardHooks(), r.Context(), toolName); scoped && !allowed {`
   - internal/mcp/server.go:581 `if rawProject, ok := args["project_id"]; ok && !(stewardCrossProjectTools[toolName] && s.stewardAvailable(r.Context())) {`
   - internal/mcp/server.go:690 `hooks := s.stewardHooks()`
   - internal/mcp/server.go:697 `return filterTopicTools(hooks, tools, s.topicPlanRole(r))`
   - internal/mcp/server.go:700 `tools = excludeStewardTools(tools)`
   - internal/mcp/server.go:760 `func (s *Server) topicPlanRole(r *http.Request) bool {`

2. `tools_automation.go` 去掉创建自动化时的总管动作 guard；普通 `automation_create` 继续存在。删 `automationFamily.exclude` 里已不存在的总管工具常量。`tools_kanban_entry.go` 删除 `kanbanExcludedTools` 及其初始化字段，不留重复字符串替代已删常量。

   - internal/mcp/tools_automation.go:481 `if stewardUsable(tc.Server.stewardHooks(), tc.Context) {`
   - internal/mcp/tools_automation.go:482 `if _, guard := stewardActionGuard(tc, stewardAutomationCreateToolName); guard != nil {`
   - internal/mcp/tools_automation.go:833 `exclude:     map[string]bool{stewardAutomationRunNowToolName: true},`
   - internal/mcp/tools_kanban_entry.go:16 `exclude:     kanbanExcludedTools(),`
   - internal/mcp/tools_kanban_entry.go:22 `out := make(map[string]bool, len(stewardToolNames)+len(stewardActionToolNames))`

3. `AttentionItem`、`StewardPendingRequest`、`ThreadStatusItem`、`ProjectItem`、`PlanRequestInput/Result`、`AssignTaskInput/Result`、`LedgerItem`、`StewardHooks` 全随旧 MCP 文件删；其跨包调用位于同批删除的 `internal/steward/hooks.go` 与 app 旧接线/plan/assign/suggestions 文件。不能把这些旧 DTO 留作“可能将来用”。

   - internal/mcp/tools_steward.go:299 `type ThreadStatusItem struct {`
   - internal/mcp/tools_steward.go:327 `type PlanRequestInput struct {`
   - internal/mcp/tools_steward.go:345 `type AssignTaskInput struct {`
   - internal/mcp/tools_steward.go:365 `type LedgerItem struct {`
   - internal/app/steward_wiring.go:408 `hooks := rt.Hooks(a.stewardThreadStatus, a.notifyOwner)`
   - internal/app/steward_wiring.go:490 `a.mcp.SetSteward(hooks)`

4. 测试辅助函数是额外编译边界：删 `tools_steward_test.go` 前，把普通 MCP 调用 helper 的最小实现移入新增 `internal/mcp/tool_call_test_helpers_test.go`，改名 `callThreadTool`；改写普通 diff 测试的调用，删除其中只验证总管工具可见性的分支。

   - internal/mcp/tools_steward_test.go:17 `func callStewardTool(t *testing.T, srv *Server, threadID, name string, args map[string]any) (string, bool) {`
   - internal/mcp/tools_kanban_diff_test.go:100 `text, isError := callStewardTool(t, srv, "thread-ordinary", "kanban_task_diff", map[string]any{})`
   - internal/mcp/tools_kanban_diff_test.go:221 `srv.SetSteward(newStewardTestHooks(nil, nil, nil))`

### 3.3 remote、评分、事件与权限

1. 删除 `stewardBuiltinMCPTool` / `stewardMainThread` / `stewardTopicThread` 后，删除 approvals 中先算豁免和直接放行的两个片段；删除 `MCPToolAllowed` 方法让普通 harness 回落现有权限判断，不返回恒 true。`hardlock_approver.go` 原样保留。

   - internal/remote/approvals.go:195 `stewardTool := a.b.stewardBuiltinMCPTool(ctx, a.threadID, req.ToolName)`
   - internal/remote/steward_tools.go:114 `return a.b.stewardBuiltinMCPTool(ctx, a.threadID, toolName)`

2. 删除跨项目附加目录方法和唯一运行调用点；前台本身不启动 harness。保留普通任务自己的 worktree、driver 和权限设置。

   - internal/remote/memory_backend.go:6439 `func (b *MemoryBackend) stewardAdditionalDirs(ctx context.Context, threadID, workDir string) []string {`
   - internal/remote/memory_backend.go:6636 `additionalDirs := b.stewardAdditionalDirs(runCtx, threadID, workDir)`

3. 删除 `SendStewardWake` 和消息 kind 产生常量，删 waker 的调用；保留 `SendHostEvent`、`SendSwarmReport` 和普通追加消息入口。`steward_events_test.go` 只删 wake 用例，其他三个用例保留。

   - internal/remote/memory_backend.go:3049 `func (b *MemoryBackend) SendStewardWake(ctx context.Context, threadID, text string) error {`
   - internal/app/goal_review_wake.go:604 `err := w.host.SendStewardWake(sendCtx, threadID, strings.Join(texts, "\n\n"))`
   - internal/remote/steward_events_test.go:62 `func TestSendHostEventMarksKind(t *testing.T) {`

4. 评分 trace 的全部外部调用点：删响应前的 `score` 快照和响应后的 trace；删取消与 withdraw trace；删启动恢复里只处理旧评分行的调用，但不删 ask 恢复。这样真正停止历史评分表的后台写入。

   - internal/remote/memory_backend.go:8250 `score := stewardScoreForResponse(pr.req, pr.toolName, params.RequestID, optID, AnswerSourceOf(ctx))`
   - internal/remote/memory_backend.go:8258 `traceSuggestionAnswered(b, score)`
   - internal/remote/approvals.go:551 `traceSuggestionUnanswered(a.b, requestID, "canceled")`
   - internal/remote/withdraw.go:59 `traceSuggestionUnanswered(b, id, string(reason))`
   - internal/remote/boot_reconcile.go:34 `if err := s.MarkStewardScoreUnanswered(ctx, ask.RequestID, bootAskResolution); err != nil {`
   - internal/app/steward_suggestions.go:311 `remote.TraceSuggestionRegistered(c.backend, store.StewardApprovalScore{`
   - internal/app/steward_suggestions.go:317 `SuggestedRole:   remote.ApprovalRoleFamily(stewardOptionRole(approval.Options, optionID), optionID),`

5. 删除 `LastTurnSilent` 的三处赋值、wire 字段、远程 alert 检查及相关专属测试。普通会话若恰好输出 `[[静默]]`，不再触发运行期完成提醒抑制；新前台本就不走旧 harness 收尾路径。

   - internal/remote/memory_backend.go:3665 `td.summary.LastTurnSilent = false`
   - internal/remote/memory_backend.go:6333 `td.summary.LastTurnSilent = false`
   - internal/remote/memory_backend.go:7769 `td.summary.LastTurnSilent = isNormal && strings.TrimSpace(hookLastAssistantText) == wire.StewardSilentMarker`
   - internal/remote/session_alerts.go:236 `if kind == SessionAlertDone && summary.LastTurnSilent {`

6. 话题远程调用与 watcher 必须同批删。`remoteStewardService` 收窄后，`stewardExtService` 也只保留 Open；旧错误映射不再需要。服务启动和停止不能留下对去抖器的引用。

   - internal/app/remote_steward.go:184 `a.backend.SetService("steward", remoteStewardService{client: client})`
   - internal/app/remote_steward.go:189 `a.remote.SetStewardWatcher(client.WatchStewardTopics)`
   - internal/app/remote_service.go:236 `debouncer := newStewardChangeDebouncer(stewardChangedDebounce, func() {`
   - internal/app/remote_service.go:241 `dispatcher.NotifyStewardChanged("")`
   - internal/app/remote_service.go:346 `s.startStewardWatchLocked()`
   - internal/app/remote_service.go:449 `s.stopStewardWatchLocked()`
   - internal/remote/steward_topics.go:106 `d.NotifyAuthed(stewardExtChangedMethod, wire.StewardExtChanged{ProjectID: strings.TrimSpace(projectID)})`

7. 删话题自动升权限的 service 回调、方法、driver 接口、remote 实现和编译断言；保留其他 driver 扩展。

   - internal/kanban/driver_ext.go:16 `type ReadOnlySessionPromoter interface {`
   - internal/remote/kanban_coordinator.go:12 `var _ kanban.ReadOnlySessionPromoter = (*MemoryBackend)(nil)`
   - internal/remote/kanban_coordinator.go:33 `func (b *MemoryBackend) PromoteReadOnlySession(ctx context.Context, threadID string) error {`

### 3.4 收件箱与 UI 删除闭包

1. app 不再提供建议后，UI 按可选接口自然退化；批次 1 先去掉 app 装配，批次 2 再删 UI 类型。`remote_attention.go` 的建议转码函数与调用同删，但 wire 的历史可选建议形状可暂时保留，避免本次扩大协议字段删除。

   - internal/app/client_attention.go:60 `item.Suggestions = suggestions[ev.ThreadID]`
   - internal/app/client_attention.go:94 `stopSuggestions := c.suggestions.watch(onChange)`
   - internal/app/remote_attention.go:61 `item.Suggestions = attentionSuggestionsToWire(suggestions[ev.ThreadID])`
   - internal/app/remote_attention.go:113 `func attentionSuggestionsToWire(suggestions []mcodeui.AttentionSuggestion) []wire.AttentionExtSuggestion {`
   - internal/ui/attention_client.go:25 `Suggestions []AttentionSuggestion`
   - internal/ui/attention_inbox.go:402 `func (s *chatState) confirmAttentionSuggestion(itemID string, sug AttentionSuggestion) {`

2. 删除话题 UI 文件前，清除所有外部入口：聊天顶部、侧栏刷新与过滤、根快捷键、命令面板、看板返回条及 sidebar overlays。`stewardTopicProjectTarget` 仅为旧“交给”入口服务，随入口删除。`withoutStewardThread` 改成只过滤 feed，去掉 `topicThreads` 参数。

   - internal/ui/chat.go:252 `renderStewardTopicCard(c, s, selectedID)`
   - internal/ui/sidebar.go:267 `s.refreshStewardTopics()`
   - internal/ui/sidebar.go:268 `s.ensureStewardTopicsWatch()`
   - internal/ui/sidebar.go:270 `topicThreads := s.stewardTopicThreadIDs()`
   - internal/ui/sidebar.go:538 `s.openStewardStartTopicDialog(pVal.ID, pVal.Name)`
   - internal/ui/sidebar.go:1365 `func (s *chatState) stewardTopicProjectTarget() (string, string) {`
   - internal/ui/sidebar.go:1494 `renderStewardTopicOverlays(c, s)`
   - internal/ui/root.go:318 `case "steward-start-topic":`
   - internal/ui/palette.go:308 `projectID, projectName := s.stewardTopicProjectTarget()`
   - internal/ui/palette.go:322 `cs.openStewardStartTopicDialog(projectID, projectName)`
   - internal/ui/kanban_modal.go:793 `renderStewardPlanReviewReturnBar(c, s)`

3. `steward_split.go` 留渲染、删控制。删除 `/split` 本地命令及其总管专属过滤；删除用量面板内分割控制。后端通用 `threads.split` 和 rotation 基础设施保留，本次不迁移普通会话功能。

   - internal/ui/composer_complete.go:122 `var stewardOnlyLocalSlashCommands = map[string]bool{"split": true}`
   - internal/ui/composer_usage_panel.go:867 `if view := splitSessionViewFor(s, th); view.Show {`
   - internal/ui/composer_usage_panel.go:885 `splitStewardSession(s, th.ID)`
   - internal/remote/session_rotation.go:78 `type SessionRotator func(ctx context.Context, info SessionRotationInfo) (rotate bool, preamble string)`

4. `isAutoCollapseReasoningEnabled` 直接读通用 `st.cfg.Session.AutoCollapseReasoningEnabled()`；去掉为识别总管而读取选中会话的锁。旧紧凑思考分支删掉，通用思考摘要与滚动时保持展开逻辑保留。

   - internal/ui/timeline_rows.go:1886 `isSteward := stewardSelected(s, threadID)`
   - internal/ui/timeline_rows.go:1889 `return reasoningAutoCollapse(st.cfg, isSteward)`
   - internal/ui/timeline_rows.go:3188 `compact := stewardCompactReasoning(s)`
   - internal/ui/timeline_rows.go:3211 `hold, forget := reasoningHoldOpen(reasoningWasLive[m.ID], autoCollapse, atBottom)`
   - internal/ui/timeline_rows.go:5532 `if running && !open && stewardCompactReasoning(s) {`

5. 时间线生成端删 wake 索引、`wakeReason` 工作标题和 silent 合并 Item；渲染端删 silent 巡视组件及对应状态字段，不能只让函数返回空字符串。保留历史 hidden-kind 小判断、新前台 reply、result、decision、decision_answer 与通用 host_event。

   - internal/ui/timeline_rows.go:951 `if m.Role == "user" && m.Kind == stewardWakeKind {`
   - internal/ui/timeline_rows.go:1404 `wakeReason = stewardWakeReason(head.wakeText)`
   - internal/ui/timeline_rows.go:5791 `return stewardSilentHeaderText(item.silentRounds, item.finishedAt)`
   - internal/ui/timeline_rows.go:5897 `ui.Text(c, stewardSilentHeaderText(item.silentRounds, item.finishedAt)).FontSize(scaledFont(TypeBody)).TextColor(color)`

## 4. 设置逐项决策

判定“仍使用”只计算生产消费者；仅有 UI、默认值、Normalize 或测试读取，不足以证明设置仍有功能。

| 字段 / 设置 | 决策与改法 | 源码证据 |
|---|---|---|
| `Enabled`、`EnabledChoice` | 保留。启用决定是否接 Desk；Choice 防止旧迁移改回用户选择。 | internal/app/frontdesk_wiring.go:63 `if !cfg.Orchestration.Steward.Enabled {return}` |
| `Name`、`Avatar` 及命名 helper | 保留，供入口和设置页展示。 | internal/app/client_steward.go:114 `return strings.TrimSpace(cfg.Orchestration.Steward.Name), strings.TrimSpace(cfg.Orchestration.Steward.Avatar)` |
| `Provider`、`Model` | 保留，前台 API 来源选择和 CLI 降级仍读取。设置标题改为“前台模型”；避免继续声称跟随普通 CLI 的全部模型行为。 | internal/app/frontdesk_wiring.go:37 `src,model,err:=frontdesk.SelectSource(apisource.Resolve(cfg,secrets),secrets,cfg.Orchestration.Steward.Provider,cfg.Orchestration.Steward.Model); if err!=nil{return nil,err}` |
| `CLIFallback` | 保留，默认关闭。补齐远程快照及写入 switch，不能仅在白名单宣称可写。 | internal/remote/settings_ext_methods.go:69 `SettingsKeyStewardCLIFallback                 = "steward.cli_fallback"` |
| `Effort` | 删除。当前前台只传 Provider/Model；不传 `req.Effort`，CLIOptions 也未传该字段。先删 UI 胶囊取值、赋值与清空，再删 config 字段及 Normalize。推荐不为了保留一个失效控件扩展前台。 | internal/ui/settings_page_steward.go:218 `Effort:   st.cfg.Orchestration.Steward.Effort,` |
| `PlanProvider`、`PlanModel`、`PlanEffort` | 删除。生产执行消费者是将删的 stewardPlanner，UI 的方案模型不属于通用 kanban 规划模型。删 `swarmPlanModelSetting` 及其包装渲染函数；保留其余 Swarm 模型设置。 | internal/app/steward_plan.go:139 `Provider:    cfg.PlanProvider,` |
| `DailyWakeLimit`、`WakeLimit`、相关默认/上下限 | 删除。用户消息触发前台不受“每天唤醒次数”控制。删高级页行、缓存、Client 唤醒计数接口，再删 config 字段/helper/default。 | internal/ui/settings_swarm_data.go:108 `ID: "row-steward-wake-limit", Title: "每天主动提醒上限",` |
| `DailyReport`、`DailyReportAt`、`DailyReportClock` | 删除。消费者只在将删的 goal waker。删设置分节与日报轮询，不改成后台调用前台。 | internal/app/goal_review_wake.go:706 `hour, minute := cfg.Orchestration.Steward.DailyReportClock()` |
| `EventPolicies`、`EventPolicy`、wake/patrol 常量和默认策略表 | 删除。新后台事件直接进入任务结果和收件箱。 | internal/config/config.go:1415 `func (s StewardConfig) EventPolicy(kind string) string {` |
| `RotateIdleMinutes`、`RotateContextTokens` | 删除。只用于旧 runtime 长上下文换新；通用 SessionRotator API 可保留，不再读取这两个字段。 | internal/steward/rotation.go:88 `func (r *Runtime) rotateTokensThreshold() int64 {` |
| `PassthroughOff`、`Passthrough` | 删除。直通模型路径下线；不要删除 attention 的存储/去重/自动消解。 | internal/config/config.go:1392 `func (s StewardConfig) Passthrough() bool { return !s.PassthroughOff }` |
| `AutoCollapseReasoningOff`、`AutoCollapseReasoningEnabled`、`steward.auto_collapse_reasoning` | 删除。前台不保留 reasoning，普通会话继续使用 session 通用开关。删桌面设置、远程 key/快照/set、Web 同步和本地存储接口。 | internal/ui/settings_page_steward.go:165 `return st.cfg.Orchestration.Steward.AutoCollapseReasoningEnabled(), false` |
| `HomeDir`、`ResolvedHomeDir` | 保留。前台仍在该目录登记 home 项目并运行 CLI 降级。高级页“记忆与状态目录”建议改为“主目录”，避免暗示还有旧账本。 | internal/app/frontdesk_wiring.go:64 `home:=cfg.Orchestration.Steward.ResolvedHomeDir(dataDir)` |
| `QuietStart`、`QuietEnd`、`QuietWindow`、`ParseClock` | 保留，但只控制通知，不延迟前台回复或任务落库。桌面已有生产消费者；新前台的 Web owner push 还会绕过桌面安静时段，批次 1 在 notifyFrontdesk 进入两个发送通道前统一检查。 | cmd/mcode/native.go:303 `if inQuiet, _ := cfg.Orchestration.Steward.QuietWindow(now()); inQuiet {` |
| `NoProgressMinutes`、`NoProgressOff`、`NoProgressAfter` | 保留，属于后台无进展检测，不能随 runtime 删。无需在本次新增设置 UI。 | internal/app/progress_watchdog.go:209 `threshold := cfg.Orchestration.Steward.NoProgressAfter()` |

补充证据与明确改法：

- 前台 `Effort` 没有执行消费，以前台构造完整表达式为准；其 CLIOptions 只有 Provider。internal/app/frontdesk_wiring.go:68 `desk:=&frontdesk.Desk{Client:frontdeskClient{dataDir:dataDir},Feed:services,Tasks:services,Decisions:services,Memory:newFrontdeskMemory(a.store,dataDir),Projects:services,ProjectPath:home,CLIFallback:cfg.Orchestration.Steward.CLIFallback,Generator:textgen.GenerateWithOptions,CLIOptions:textgen.Options{Provider:cfg.Orchestration.Steward.Provider}}`
- `swarmPlanModelSetting` 的实际存储字段就是总管专用字段。internal/ui/settings_page_swarm.go:1257 `Provider: st.cfg.Orchestration.Steward.PlanProvider,`
- 安静时段不能只保留空控件；Web push 当前没有配置检查。internal/app/remote_service.go:271 `go push.Send(context.Background(), remote.PushPayload{`
- `notifyFrontdesk` 统一检查时不改变注意力收件箱持久化与徽标，只抑制这次外部通知。internal/app/frontdesk_wiring.go:85 `if err:=a.backend.AppendStewardMessage(ctx,feedID,"assistant",kind,text); err!=nil {log.Printf("前台写入结果失败: %v",err);return}`
- `settings.get` 当前列了独立 reasoning，却没列 CLI fallback；补 `SettingsKeyStewardCLIFallback: cfg.Orchestration.Steward.CLIFallback`，并在 `applySettingsValue` 用 `decodeSettingsBool` 写入，删除 reasoning case。internal/app/remote_settings.go:159 `remote.SettingsKeyStewardAutoCollapseReasoning:       cfg.Orchestration.Steward.AutoCollapseReasoningEnabled(),`
- internal/app/remote_settings.go:610 `case remote.SettingsKeyStewardAutoCollapseReasoning:`
- settings 白名单删除 reasoning 常量与列表项，保留启用、名字、头像、安静起止、CLI fallback。internal/remote/settings_ext_methods.go:118 `SettingsKeyStewardAutoCollapseReasoning,`

配置兼容已从代码确认：Load 先迁移 raw，再把结果 `json.Unmarshal` 到 Defaults；自定义 Config.UnmarshalJSON 也调用普通 Unmarshal，不使用 `DisallowUnknownFields`。因此删除 Go struct 字段后，旧配置多出的键会被忽略。正常 save 会不再输出那些已删字段；不需要迁移时主动重写用户配置，也不能改变全局解析的宽松行为。

- internal/config/load.go:66 `if err := json.Unmarshal(migratedRaw, &cfg); err != nil {`
- internal/config/config.go:469 `return json.Unmarshal(data, (*configAlias)(c))`
- internal/config/config_test.go:3114 `func TestStewardConfigIgnoresRemovedTierKey(t *testing.T) {`

批次 3 必须补一个旧配置兼容测试：旧键同时包含 wake、report、policies、rotation、passthrough、reasoning、effort、plan 三字段；Load 成功，保留字段不丢；save 后旧键不再写出，二次 Load 成功。该测试验证真实向后兼容，不镜像实现。

## 5. 测试处置与守护

### 删除测试与保留测试的原则

- 旧包测试全删；旧 app actions/assign/plan/suggestions/stats/rotation/split/topic 测试全删。`steward_wiring_test.go` 不整删所有行为：先把公共通知边界相关测试移至新增 `internal/app/frontdesk_notify_test.go`，删 runtime 专属测试。internal/app/steward_wiring_test.go:156 `a.SetOwnerNotifier(func(title, body, threadID string) error {`
- `client_steward_test.go` 重写 runtime fake 和 Ensure 断言，保留启用、身份、打开已有 feed/新建 feed 的测试。internal/app/client_steward_test.go:44 `client.SetSteward(rt)`
- `remote_steward_test.go` 只留 Open 服务转发与不可用分支，删除话题、冲突映射、去抖器测试。internal/app/remote_steward_test.go:78 `svc := remoteStewardService{client: client}`
- `steward_test_helpers_test.go` 保留：`disableStewardForTest` 仍用于 app 通用测试关闭前台启动，名字无需为删除而重命名。internal/app/client_test.go:273 `disableStewardForTest(t, dataDir)`
- `tools_steward_test.go`、`tools_steward_ledger_test.go`、`tools_steward_topic_test.go` 整删；先迁出普通调用 helper，保留看板 diff 和普通作用域覆盖。internal/mcp/tools_kanban_diff_test.go:125 `text, isError := callStewardTool(t, srv, "thread-ordinary", "kanban_task_diff", map[string]any{"task_id": " task-1 "})`
- `remote/steward_tools_test.go`、`steward_additional_dirs_test.go`、`steward_score_trace_test.go` 整删；`approvals_test.go` 里依赖旧 helper 的段落改为普通 backend fixture，保留普通审批行为，不把安全测试顺手删除。internal/remote/approvals_test.go:2712 `setTopicThreads(t, backend, topicTestThreadID)`
- `remote/steward_topics_test.go` 重写为只测 Open，删除 topics 与 changed；`steward_marker_test.go` 改测 feed 标识、普通线程和 helper 排除；`steward_events_test.go` 只删 wake 用例。internal/remote/steward_marker_test.go:31 `backend.SetStewardPredicate(func(threadID string) bool {`
- `remote/memory_backend_test.go`、`session_alerts_test.go` 只删 LastTurnSilent 专属测试；普通 session alert 和 backend 测试保留。internal/remote/session_alerts_test.go:155 `func TestSessionAlertDetector_LastTurnSilent(t *testing.T) {`
- `ui/steward_decision_bar_test.go`、`steward_topic_card_test.go`、`steward_topics_test.go`、`suggestion_score_test.go` 整删；`steward_split_test.go`、`steward_timeline_test.go` 部分保留通用分割线与 reasoning 测试。internal/ui/steward_timeline_test.go:155 `hold, forget := reasoningHoldOpen(tc.wasLive, tc.autoCollapse, tc.atEnd)`
- `ui/palette_test.go` 删话题入口测试，打开总管测试改用 `newStewardFakeClient`；`chat_steward_empty_test.go` 把原定义在已删 topic 测试的 thread ID 改为本地测试常量，避免辅助符号悬空。internal/ui/chat_steward_empty_test.go:14 `fake := newStewardFakeClient(stewardTopicsMainThreadID)`
- `ui/steward_section_test.go`、`steward_project_filter_test.go` 保留并修改 feed-only 过滤的签名期望；`steward_client_test.go` 只删 wake 能力覆盖；`settings_page_steward_test.go` 删旧设置断言，保留名字、头像、启用、CLI fallback、模型与安静时段。internal/ui/steward_client_test.go:262 `func TestStewardWakesTodayDisabled(t *testing.T) {`
- `config/config_test.go` 部分删旧字段/default/helper 测试；保留启用迁移、身份、QuietWindow、NoProgress、HomeDir 和旧未知键兼容，新增完整旧配置测试。internal/config/config_test.go:3425 `func TestStewardNoProgressAfterOff(t *testing.T) {`
- 新前台、feed、编号决策卡、kanban 五态结果和 store 历史评分测试全部保留。internal/remote/steward_feed_test.go:16 `func TestStewardFeed(t *testing.T) {`

### 守护与快照

1. 更新 `internal/ui/testdata/settings_tree.golden`：删除 reasoning、disabled hint、plan model、日报、score、passthrough、wake limit，保留有效行并补现有 CLI fallback 行。使用声明式树的更新入口，检查只发生预期变化。

   - internal/ui/testdata/settings_tree.golden:236 `row row-steward-auto-collapse-reasoning 自动折叠思考过程`
   - internal/ui/testdata/settings_tree.golden:240 `row row-steward-model-plan 方案模型`
   - internal/ui/testdata/settings_tree.golden:244 `section sec-steward-daily-report 日报`
   - internal/ui/testdata/settings_tree.golden:247 `section sec-steward-score 建议计分`
   - internal/ui/testdata/settings_tree.golden:254 `row row-steward-wake-limit 每天主动提醒上限`
   - internal/ui/settings_tree_test.go:14 `var update = flag.Bool("update", false, "rewrite settings tree golden files")`

2. `settings_tree_test.go` 中拿 `PassthroughOff` 当测试存储字段的通用控件测试，改用仍存在的布尔字段（例如 `Session` 的相关开关或测试自己的 config 字段选择），保留 inverted/锁定重置的测试意图。`settings_reset_test.go` 的 wake limit 示例改用保留字段。

   - internal/ui/settings_tree_test.go:50 `passthroughOff := val(func(c *config.Config) *bool { return &c.Orchestration.Steward.PassthroughOff }, false)`
   - internal/ui/settings_reset_test.go:32 `dst.Orchestration.Steward.DailyWakeLimit = 99`

3. `text_scale_guard_test.go` 白名单删除确实整删的 `steward_decision_bar.go`、`steward_topic_card.go`、`steward_topics.go`。保留仍存在的 `sidebar_steward.go`、`steward_split.go`、`steward_timeline.go`、新 reply/card。不要为测试通过直接缩减扫描范围。

   - internal/ui/text_scale_guard_test.go:19 `"sidebar_steward.go": true, "sidebar_bulk.go": true, "steward_decision_bar.go": true,`
   - internal/ui/text_scale_guard_test.go:21 `"steward_split.go": true, "steward_timeline.go": true, "steward_topic_card.go": true,`

4. `modal_style_guard_test.go` **保留且无需修改**：它通过 glob 扫生产 `.go`，没有话题文件白名单。删除旧弹窗后仍覆盖剩余组件；新编号卡不得引入 `ui.Modal` / `ui.Popover`。

   - internal/ui/modal_style_guard_test.go:14 `paths, err := filepath.Glob("*.go")`
   - internal/ui/modal_style_guard_test.go:40 `if sel.Sel.Name != "Modal" && sel.Sel.Name != "Popover" {`

5. `timeline_turn_header_test.go` 仍使用保留的通用摘要常量，不能随 `steward_timeline` 名称误删；`timeline_rows_test.go` 删除旧聚合/字面量锁定测试，保留历史隐藏输入和新消息渲染测试。

   - internal/ui/timeline_turn_header_test.go:75 `long := strings.Repeat("要", reasoningHeadlineRunes+5)`
   - internal/ui/timeline_rows_test.go:2601 `func TestStewardSilentMarkerValue(t *testing.T) {`

## 6. 四个执行批次

以下清单是每批允许修改、删除或新增的文件全集。目录删除清单明确展开，未列文件保持不动。若编译暴露额外引用，先把实际路径、符号和行号记录到交付说明，不能用扩大测试删除范围来掩盖。验收命令供执行者运行，本次计划编写没有执行它们。

### 批次 1：关闭旧执行闭包，接稳前台入口与通知

依赖：无。内部必须原子交付；完成后旧包及其导入消失，UI 的可选旧类型暂时仍可存在，但 app 不再实现它们。

编号步骤：

1. 按第 3.1 节迁出公共通知符号，新增普通首轮提示实现与 feed ID getter；重写 Client 入口和摘要标记。先做这一步再删 wiring。锚点：internal/app/steward_wiring.go:544 `func (a *App) SetOwnerNotifier(fn func(title, body, threadID string) error) {`
2. 删除 App/runtime/waker 字段和身份 policy；保留原首轮 extra hook，换成 `frontdeskHandoffSection`；迁出 `goalOwnerSteward`。锚点：internal/app/app.go:906 `return stewardHandoffSection(stewardRT, opts.DataDir, threadID)`
3. 清掉 MCP 旧注册、Server hooks、白名单与 automation/kanban family 的依赖；通用 helper 测试迁出；删除旧包和 app actions/assign/plan/suggestions/stats。锚点：internal/mcp/registry.go:65 `r.registerStewardTools(s)`
4. 收窄远程服务为 Open，移除话题 watcher；删除旧免审批、附加目录、wake、评分 trace、LastTurnSilent；保留 feed、决策、收件箱和 store 历史评分。锚点：internal/remote/steward_topics.go:84 `RegisterMethod(stewardOpenMethod, handleStewardOpen)`
5. 删除话题开工自动升权限。历史目标验收 guard 保留但不再提示调用已删工具。锚点：internal/kanban/service.go:1240 `s.promoteTopicSession(ctx, started)`
6. 在 `notifyFrontdesk` 开头按现读配置检查 QuietWindow，抑制两条外部通知通道，不阻塞消息落库；保留原通知锁。改写本批测试。锚点：internal/app/frontdesk_wiring.go:54 `if a.remote!=nil && a.remote.SendOwnerPush(title,body,thread) {return nil}`

允许文件全集（`删除` 以外均为修改，注明 `新增` 的除外）：

- 删除 `internal/steward/actions.go`、`internal/steward/hooks.go`、`internal/steward/ledger.go`、`internal/steward/passthrough.go`、`internal/steward/prompt.go`、`internal/steward/receipt.go`、`internal/steward/rename.go`、`internal/steward/rotation.go`、`internal/steward/steward.go`、`internal/steward/topic_scope.go`、`internal/steward/topics.go`、`internal/steward/wake.go`。
- 删除 `internal/steward/actions_test.go`、`internal/steward/delivery_retry_test.go`、`internal/steward/hooks_test.go`、`internal/steward/ledger_test.go`、`internal/steward/passthrough_test.go`、`internal/steward/prompt_test.go`、`internal/steward/receipt_test.go`、`internal/steward/rotation_test.go`、`internal/steward/split_preamble_test.go`、`internal/steward/steward_test.go`、`internal/steward/topic_scope_test.go`、`internal/steward/topics_test.go`、`internal/steward/wake_test.go`。
- 删除 `internal/app/steward_actions.go`、`internal/app/steward_assign.go`、`internal/app/steward_plan.go`、`internal/app/steward_suggestion_stats.go`、`internal/app/steward_suggestions.go`、`internal/app/steward_wiring.go`、`internal/app/goal_review_wake.go`。
- 删除 `internal/app/steward_actions_test.go`、`internal/app/steward_assign_test.go`、`internal/app/steward_plan_test.go`、`internal/app/steward_suggestion_stats_test.go`、`internal/app/steward_suggestions_test.go`、`internal/app/steward_wiring_rotation_test.go`、`internal/app/steward_wiring_split_test.go`、`internal/app/goal_review_wake_test.go`、`internal/app/client_steward_topic_test.go`；`internal/app/steward_wiring_test.go` 的通知测试迁出后删除。
- `internal/app/app.go`、`internal/app/client.go`、`internal/app/client_steward.go`、`internal/app/client_steward_test.go`、`internal/app/remote_steward.go`、`internal/app/remote_steward_test.go`、`internal/app/client_attention.go`、`internal/app/client_attention_test.go`、`internal/app/remote_attention.go`、`internal/app/remote_attention_test.go`、`internal/app/remote_service.go`、`internal/app/swarm_wiring.go`、`internal/app/frontdesk_wiring.go`、`internal/app/frontdesk_wiring_test.go`。
- 新增 `internal/app/frontdesk_handoff.go`、`internal/app/frontdesk_handoff_test.go`、`internal/app/frontdesk_notify.go`、`internal/app/frontdesk_notify_test.go`。
- 删除 `internal/mcp/tools_steward.go`、`internal/mcp/tools_steward_test.go`、`internal/mcp/tools_steward_ledger_test.go`、`internal/mcp/tools_steward_topic_test.go`。
- `internal/mcp/registry.go`、`internal/mcp/server.go`、`internal/mcp/tools_automation.go`、`internal/mcp/tools_kanban_entry.go`、`internal/mcp/tools_kanban.go`、`internal/mcp/tools_kanban_diff_test.go`、`internal/mcp/tools_kanban_review_guard_test.go`；新增 `internal/mcp/tool_call_test_helpers_test.go`。
- 删除 `internal/remote/steward_tools.go`、`internal/remote/steward_score_trace.go`、`internal/remote/steward_tools_test.go`、`internal/remote/steward_additional_dirs_test.go`、`internal/remote/steward_score_trace_test.go`。
- `internal/remote/steward_topics.go`、`internal/remote/steward_topics_test.go`、`internal/remote/steward_feed.go`、`internal/remote/steward_feed_test.go`、`internal/remote/steward_marker_test.go`、`internal/remote/steward_events_test.go`、`internal/remote/memory_backend.go`、`internal/remote/memory_backend_test.go`、`internal/remote/approvals.go`、`internal/remote/approvals_test.go`、`internal/remote/withdraw.go`、`internal/remote/boot_reconcile.go`、`internal/remote/session_alerts.go`、`internal/remote/session_alerts_test.go`、`internal/remote/wire/steward_topics_types.go`、`internal/remote/wire/types.go`、`internal/remote/kanban_coordinator.go`。
- `internal/harness/policy/policy.go`、`internal/harness/policy/policy_test.go`、`internal/kanban/service.go`、`internal/kanban/driver_ext.go`、`internal/kanban/coordinator_test.go`。

验收命令：`go build ./...`；`go vet ./...`；`go test ./internal/app ./internal/mcp ./internal/remote ./internal/harness/policy ./internal/kanban ./internal/frontdesk ./internal/store`。另做定点检索确认无 `mcode/internal/steward` 导入、无 `wireSteward` / `wireGoalReviewWake` / `SetSteward` 调用、无旧评分 trace 调用。编译通过不等于通知和 UI 已实测；feed 重启复用、首轮提示和安静时段要由上述真实行为测试覆盖。

### 批次 2：删除桌面话题界面和失效设置控件

依赖：批次 1。可与批次 4 并行。此时配置字段仍保留到批次 3，不能提前删 config。

编号步骤：

1. 删除话题卡、话题类型、话题缓存和决策置顶条，按第 3.4 节清所有入口；总管打开入口保留。锚点：internal/ui/chat.go:252 `renderStewardTopicCard(c, s, selectedID)`
2. `sidebar_steward` 改成仅 feed 过滤，保留收件箱徽标；删 wake 客户端接口。锚点：internal/ui/sidebar_steward.go:216 `func withoutStewardThread(stewardThread string, topicThreads map[string]bool, projects []Project, threads []Thread, threadsByProj map[string][]Thread) ([]Project, []Thread, map[string][]Thread) {`
3. 部分清理 `steward_split`、`steward_timeline` 和 timeline rows；保留通用 reasoning、历史分割线、新 reply/card 与历史隐藏输入。锚点：internal/ui/timeline_rows.go:2774 `return renderSplitDivider(c, m)`
4. 删除旧审批建议 UI 和所有评分/提醒计数缓存；注意力事件路径原样保留。锚点：internal/ui/settings.go:188 `stewardWake stewardWakeTodayState`
5. 设置页只保留启用、昵称、头像、CLI fallback、前台 Provider/Model、安静时段和主目录；删所有 Effort/Plan 字段的 UI 读取。`swarmPlanModelSetting` 包装函数也删，普通 Swarm 模型控件保留。锚点：internal/ui/settings_page_steward.go:74 `stewardDailyReportSection,`
6. 改写测试、通用 config 控件测试的假字段和 golden，移除删除文件的 text-scale 白名单；modal guard 原样运行。锚点：internal/ui/settings_tree_test.go:379 `// TestSettingsTree_Snapshot 锁定声明式页面树的可见结构，-update 时重写 golden。`

允许文件全集：

- 删除 `internal/ui/steward_decision_bar.go`、`internal/ui/steward_decision_bar_test.go`、`internal/ui/steward_topic_card.go`、`internal/ui/steward_topic_card_test.go`、`internal/ui/steward_topic_types.go`、`internal/ui/steward_topics.go`、`internal/ui/steward_topics_test.go`、`internal/ui/suggestion_score.go`、`internal/ui/suggestion_score_test.go`。
- `internal/ui/steward_split.go`、`internal/ui/steward_split_test.go`、`internal/ui/steward_timeline.go`、`internal/ui/steward_timeline_test.go`、`internal/ui/sidebar_steward.go`、`internal/ui/steward_client.go`、`internal/ui/steward_client_test.go`、`internal/ui/steward_section_test.go`、`internal/ui/steward_project_filter_test.go`、`internal/ui/chat_steward_empty_test.go`。
- `internal/ui/chat.go`、`internal/ui/sidebar.go`、`internal/ui/palette.go`、`internal/ui/palette_test.go`、`internal/ui/root.go`、`internal/ui/kanban_modal.go`、`internal/ui/composer_complete.go`、`internal/ui/composer_usage_panel.go`、`internal/ui/timeline_rows.go`、`internal/ui/timeline_rows_test.go`、`internal/ui/attention_client.go`、`internal/ui/attention_inbox.go`、`internal/ui/attention_inbox_test.go`。
- `internal/ui/settings.go`、`internal/ui/settings_page_steward.go`、`internal/ui/settings_page_steward_test.go`、`internal/ui/settings_swarm_data.go`、`internal/ui/settings_page_swarm.go`、`internal/ui/settings_tree_test.go`、`internal/ui/settings_reset_test.go`、`internal/ui/testdata/settings_tree.golden`、`internal/ui/text_scale_guard_test.go`。

验收命令：`go build ./...`；`go vet ./...`；`go test ./internal/ui`；更新快照时先运行 `go test ./internal/ui -run '^TestSettingsTree_Snapshot$' -update`，审阅 golden 后再运行正常测试。`TestNoStyledModalOrPopover`、新决策卡测试、普通 reasoning/滚动和收件箱测试都必须保留执行。

### 批次 3：删除无消费者的配置字段，收拢远程设置

依赖：批次 1、2。可与批次 4 并行，文件没有重叠。

编号步骤：

1. 按第 4 节删旧字段及 helper、默认常量、Normalize、Defaults 赋值。保留迁移框架和 unknown-key 宽松解析，不再新增旧字段迁移。锚点：internal/config/config.go:1739 `if cfg.Orchestration.Steward.DailyWakeLimit <= 0 {`
2. 删除远程 reasoning key、白名单项、快照与写入 case；补 CLI fallback 快照/写入。名字、头像、启用和 quiet 写入校验照旧。锚点：internal/remote/settings_ext_methods.go:70 `SettingsKeyStewardAutoCollapseReasoning       = "steward.auto_collapse_reasoning"`
3. 改写 config 旧测试和 remote settings 测试；补旧配置兼容及 CLI fallback round-trip。保留配置 v6/v7 启用迁移测试。锚点：internal/config/config_test.go:3182 `func TestMigrateV6ToV7StewardEnabled(t *testing.T) {`

允许文件全集：

- `internal/config/config.go`、`internal/config/config_test.go`。
- `internal/remote/settings_ext_methods.go`、`internal/remote/settings_ext_methods_test.go`。
- `internal/app/remote_settings.go`、`internal/app/remote_settings_test.go`。

验收命令：`go build ./...`；`go vet ./...`；`go test ./internal/config ./internal/app ./internal/remote ./internal/ui ./internal/frontdesk`。定点检索 `DailyWakeLimit`、`DailyReportClock`、`PassthroughOff`、`AutoCollapseReasoningOff`、总管 `PlanProvider/PlanModel/PlanEffort` 与 `Effort` 时，只允许历史文档、迁移 raw key 和兼容测试 fixture；不能再有生产消费者。

### 批次 4：删除 Web 话题调用与协议声明

依赖：批次 1；可与批次 2、3 并行。所有 Web 与协议文件只归本批，生成器不属于其他批次所有权。

编号步骤：

1. `remoteStewardApi` 收窄到 open；`ThreadList` 删除话题列表、changed 订阅、sheet 与动作，保留固定总管入口。锚点：web/src/components/ThreadList.tsx:1205 `const { supportsTopics, supportsOpen, supportsStart, supportsConfirm, supportsReplan, supportsClose, supportsDelete, supportsReviewTask, listTopics } = stewardApi;`
2. 删除文字决策条和总管专属分割入口；timeline helper 仅留通用 reasoning，历史 wake 隐藏兼容保留，不保留巡视聚合状态。锚点：web/src/components/ThreadView.tsx:2124 `() => (isStewardThread ? stewardPendingDecision(messages) : null),`
3. 删除 Header、SettingsForm、storage 的旧 reasoning 设置；保留普通 session 偏好；给总管页补现有 `steward.cli_fallback` 控件，使用 `hasKey` 以兼容旧宿主。锚点：web/src/components/Header.tsx:214 `const remoteStewardAutoCollapse = settingsApi.snapshot?.values['steward.auto_collapse_reasoning'];`
4. 修改 schema 与 protocol，删 topic/changed 的定义和所有联合引用；生成 `generated.ts`，同步相关测试。不要把新 decision 类型删掉。锚点：docs/remote-control.schema.json:14561 `"method": { "const": "steward.decisions.list" },`

允许文件全集：

- 删除 `web/src/components/StewardTopicRow.tsx`、`web/src/components/StewardTopicSheet.tsx`。
- `web/src/context/remoteStewardApi.ts`、`web/src/components/ThreadList.tsx`、`web/src/components/ThreadView.tsx`、`web/src/components/ThreadTimeline.tsx`、`web/src/components/timeline/stewardTimeline.ts`、`web/src/components/Header.tsx`、`web/src/components/SettingsForm.tsx`、`web/src/services/storage.ts`、`web/src/context/remoteSplitApi.ts`、`web/src/components/ThreadComposer.tsx`、`web/src/components/ThreadTimeline.test.ts`、`web/src/components/ThreadComposer.test.tsx`、`web/src/types/generated.ts`。
- `docs/remote-control.schema.json`、`docs/remote-control-protocol.md`。

验收命令：`go build ./...`；`go vet ./...`；`go test ./internal/remote ./internal/app ./internal/ui`；在 `web` 目录运行 `npm run generate:contract`、`npm run build`、`npm test`。生成器依赖 schema 的 `$ref` 闭合，构建包含生成步骤，不能只手改 generated 类型。

- web/package.json:9 `"build": "npm run generate:contract && tsc && vite build",`
- web/package.json:11 `"test": "node scripts/run-tests.mjs"`

## 7. 风险与未确定事项

1. **运行时桥接藏有新前台仍需的边界。** 最危险的是把 wiring 整删后同时丢掉通知导出符号，或 `StewardThreadID` 仍从恒 nil 的 runtime 读取，导致前台已运行但侧栏入口不存在。按批次 1 先迁符号、再改 feed getter、最后删包。internal/ui/sidebar_steward.go:22 `threadID := s.stewardThreadID()`
2. **按文件名整删会破坏普通会话与测试。** 分割线、reasoning 摘要、MCP 测试 helper 和 topic 测试中的共享常量都跨文件使用；本计划明确部分保留和迁出，不能简单 `rm *steward*`。internal/ui/timeline_rows.go:3226 `if full := reasoningHeadlineFull(m.Text, isLive); full != "" {`
3. **设置仍显示但没有行为，或有行为却被误删。** `NoProgress*`、QuietWindow 和 HomeDir 必须保留；Effort / Plan / wake / report / passthrough / reasoning 要等消费者删完再删；Web owner push 需要统一 quiet 检查。internal/app/progress_watchdog.go:209 `threshold := cfg.Orchestration.Steward.NoProgressAfter()`

明确未验证：本计划没有执行任何验证命令，不能承诺当前基线可编译，也没有做桌面或 Web 实测。允许文件清单来自静态引用调研；执行者必须在批次交付时运行列出的验收。

需保留的现状边界：前台 `wireFrontdesk` 只在启动接线时检查 Enabled，CLI fallback 也在构造时读取。实时启停/改 CLI 开关是否已经有独立重接线机制，本次未确认，不把它猜成已支持；推荐本次维持生命周期，只删除旧消费者，另由主代理确认设置保存后的实际生效时机。Provider/Model 当前每次 Stream 现读，不能与 Enabled 混称为同一种刷新行为。

- internal/app/frontdesk_wiring.go:35 `cfg,err:=config.Load(c.dataDir); if err!=nil{return nil,err}`
- internal/app/frontdesk_wiring.go:63 `if !cfg.Orchestration.Steward.Enabled {return}`

历史兼容的推荐选择：保留旧配置未知键读取、旧会话归档、历史 hidden kind、历史评分表、历史目标验收保护；删除全部旧执行和交互入口。若执行者希望继续缩减通用 SessionRotator/threads.split、历史注意力 wire 的可选建议字段或历史评分 store，应另立后续清理，不纳入本次四批，避免同时改变已存在的公共协议与历史数据语义。
