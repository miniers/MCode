# 总管前台对齐 noop

本方案以任务 brief 内联的 noop 行为为准。只调整总管消息分流、任务收尾和结果展示；复用现有任务、回执和决策卡。保留看板，普通 Goal 的策略不变。本文是实施方案，尚未修改代码或执行验证。

## 1. 替换前台 systemPrompt

当前提示词没有明确区分简单问题与工作，也没有完整定义空回复、记忆和已有任务的纠正规则。

internal/frontdesk/context.go:14 `用户交代新工作时调用 create_task；补充已有任务时调用 amend_task。`

将 `systemPrompt` 完整替换为以下中文文本；工具字段沿用 mcode 的 `body`，不新增 noop 的 `details` 字段：

```text
你是 MoCo · 总管主会话的前台。每次处理一条用户消息，判断是直接回答、读取信息、交代工作、补充工作、回答决策卡，还是记住用户明确要求的长期偏好。

闲聊、致谢，以及能从当前上下文回答的简单问题，直接回复，不调用工具，不创建任务。
状态类问题，例如“X 怎么样了”“现在在跑什么”“今天发生了什么”，先看上下文。上下文足够就直接回答；不够时只调用必要的读工具，本轮回复留空，下一轮拿到结果再回答。没有证据的事实不要编造；能查就查，查不到就说不知道。不要把普通状态查询当成新工作；确实需要超过一分钟的调查时才创建任务。

真正的工作才调用 create_task，例如写代码、审查 PR、准备发版、调查，或任何预计超过一分钟的工作。标题写清要做的事；body 引用用户原话，并补上执行者需要的项目、已有结论、相关任务和限制。不要为同一项工作重复建任务。
针对已有任务的纠正、“不行”“再……”和补充要求，调用 amend_task，沿用那个任务的 id；任务已完成也一样，追加要求会重新打开同一个任务。不要因为任务已完成就另建任务。只有用户明确要求取消时才调用 cancel_task。
如果相关任务正在等待用户回答决策卡，用户这句话优先作为对该卡的回答，调用 answer_decision，不创建任务，也不用 amend_task 代替答卡。
快照里会提示五分钟内刚答过的决策卡。用户明确纠正或改变刚才的选择时，调用 answer_decision 对同一张卡改答；不要把改答另建成任务。没有明确关联时，不擅自把普通消息当成决策答案。

用户明确说“记住”“以后都这样”等长期偏好时，调用 remember；明确修改已有记忆时调用 edit_memory。对产品的抱怨、问题报告和改动要求属于工作，只创建或补充任务，不调用 remember。
你不直接写代码、合并、发版、部署、推远端、改设置、花钱或给人发消息。这些工作交给任务执行者。执行者需要用户作决定时，通过决策卡提出一个不超过三十字的问题和几个选项；前台不要代替用户批准。

用用户的语言回复，一到两句短话，具体说清做了什么或查到了什么。不要套模板，不寒暄，不写“好的我来”等填充语，不承诺流程，不说“做完带结果回来”“需要你拍板时再问你”。没有有用内容时回复为空；例如没有关联决策或任务的“A”“ok”“好的”，以及动作本身已足够说明结果的情况。
写工具会在回复发出之后执行。不要声称写入已成功或工作已完成；有必要说明动作时用“交给任务处理”等准确短话，也可以不回复。工具结果和上下文没有的信息不能当作事实。
最多四轮。读工具的结果在下一轮使用；不要在读结果之前先给结论。
```

工具无需改名：`create_task` 已有 `body`，且已经固定标记用户来源。

internal/frontdesk/tools.go:68 `	add("create_task", "创建任务", true, []string{"title"}, []string{"body", "project_id", "priority", "provider", "model"}, func(c context.Context, a map[string]string) (any, error) {`

保留现有三条决策路径：裸编号直接答卡、快照的五分钟改答提示、kanban 对已答卡五分钟内改答的约束。不把“用户只发 A 可以为空”覆盖到正在等待的决策卡上。

internal/frontdesk/desk.go:130 `	if m := decisionPattern.FindStringSubmatch(in.Text); m != nil {`

internal/frontdesk/context.go:10 `const decisionReanswerWindow = 5 * time.Minute`

internal/kanban/decisions.go:27 `		if d.AnsweredAt == nil || now.Sub(*d.AnsweredAt) > decisionReanswerWindow {`

空回复必须同时改执行逻辑，否则提示词允许的行为仍会被宿主换成模板。API 和 CLI 在“有效空回复、无工具”时直接结束；在“有效空回复、写入成功”时仅执行写入，不补“已建任务”。删掉 `toolConfirmation` 及两个调用点，不再自动拼接确认语。真实写入失败仍返回错误，不能误报成功，也不能再次 fallback 创建重复任务。

internal/frontdesk/exec.go:116 `					confirmation = "没办成：模型没有返回内容"`

internal/frontdesk/exec.go:161 `		return "已建任务「" + title + "」"`

internal/frontdesk/cli_fallback.go:82 `			confirmation = "没办成：模型没有返回内容"`

## 2. 模型失败与最大轮数：静默保留工作

目前 API 报错仅发报错，并不会保留消息为工作；轮数耗尽和无 Client 才进入会建任务的 fallback。应统一为：API 失败 → 至多尝试一次已启用的 CLI → CLI 也不可用则静默 amend/create；轮数耗尽走相同入口。保留日志，不给用户转发 401、404、CLI 缺失、未登录、超时或“达到最大轮数”。默认四轮保留，`MaxRounds > 4` 也钳到四轮，测试可设更小值。

internal/frontdesk/exec.go:71 `			if ok, cliErr := d.tryCLIFallback(ctx, in, snap); ok || cliErr != nil {`

internal/frontdesk/exec.go:74 `			d.reply(ctx, in.ThreadID, "前台模型不可用："+errorSummary(err))`

internal/frontdesk/exec.go:150 `	return d.fallback(ctx, in, snap, fmt.Errorf("达到最大轮数 %d", rounds))`

具体改法：API 错误分支直接调用 `fallback(ctx, in, snap, err)`，由 fallback 独占 CLI 尝试；删除 fallback 的 `prefix` 和成功后的全部 `d.reply`。CLI 的合法 `{"reply":"","calls":[]}` 是成功的静默回复，不能再落到建任务；格式损坏、缺字段、未知或不允许的工具、参数无效且未执行任何动作，才算 CLI 不可用。写入前校验整个 calls 列表，避免先执行一部分再 fallback 重复建任务。CLI 允许现有 Toolbox 中全部写工具，读工具仍不执行；状态信息不足时不伪造答案，交给静默任务。不要为 CLI 再建一套读工具循环。

internal/frontdesk/fallback.go:53 `	if ok, err := d.tryCLIFallback(ctx, in, snap); ok || err != nil {`

internal/frontdesk/fallback.go:61 `		prefix = "前台模型不可用：" + errorSummary(reasons[0]) + "，"`

internal/frontdesk/cli_fallback.go:54 `		if call.Tool != "create_task" && call.Tool != "amend_task" && call.Tool != "remember" && call.Tool != "answer_decision" {`

静默落任务按以下顺序，不做关键词分类器：

- `Inbound.ReplyTo` 非空：读取并验证该任务，直接 amend；不限字数、行数、任务年龄或是否已完成。明确目标失效时返回错误，不能悄悄另建。
- 否则，仅当消息一行以内、不超过四十个 rune，且本会话十分钟内有团队刚提及的明确任务时 amend。
- 否则，以用户消息首行 trim 后截断至八十个 rune 为标题，以完整原话为 body，创建 `Origin: owner` 任务。成功后不产生任何回复。
- amend/create 的持久化失败是消息未能交付，继续返回错误；`Enqueue` 可保留一条“任务未能保存，请重试”的简短提示并记详细日志，不能用“前台模型不可用”掩盖，也不能吞掉存储错误。

现有 fallback 未使用 `ReplyTo`，而“最近任务”实际是全库最近更新的 owner 任务；后台心跳会错误续长窗口。必须修掉这个语义，不能只删回复文字。

internal/frontdesk/desk.go:87 `	ThreadID, Text, ReplyTo string`

internal/frontdesk/fallback.go:64 `		if task, err := d.Tasks.RecentTask(ctx); err == nil && task.ID != "" && !task.UpdatedAt.After(d.now()) && d.now().Sub(task.UpdatedAt) <= 10*time.Minute {`

internal/app/frontdesk_adapters.go:185 `		if err == nil && m.Origin == "owner" && (latest.ID == "" || t.UpdatedAt.After(latest.UpdatedAt)) {`

最小接口调整：将 `TaskService.RecentTask` 改为 `RecentTask(ctx, feedThreadID) (store.Task, time.Time, error)`，时间代表团队提及的消息时间。适配器有界读取该 feed 最近消息，从最新的 assistant/system 消息中取 `steward_result.task`、`steward_decision.task_id`，或唯一明确出现的任务 id/完整标题，再读取任务；无法唯一关联就返回 NotFound。使用消息 `CreatedAt`，不改写任务的 `UpdatedAt` 来伪装提及时间。不用新表、新记忆或额外模型。前台和 CLI 正常提及任务时可依靠已有标题；静默建任务本来没有团队提及，因此不自动把下一句归到它。

internal/frontdesk/desk.go:42 `	RecentTask(context.Context) (store.Task, error)`

internal/store/model.go:152 ``	CreatedAt time.Time `json:"created_at"` ``

注意：生产入口当前没有传递显式 ReplyTo，不能声称已有完整“回复某任务”的 UI 链路。本次保留并实现该字段的处理，不扩展 JSON-RPC 协议；实际聊天关联由模型上下文和上述最近明确提及覆盖。

internal/app/frontdesk_wiring.go:106 `	r.desk.Enqueue(frontdesk.Inbound{ThreadID: id, Text: text, Images: parts})`

**ErrNoModel 建议保留唯一例外提示**：CLI 也没有成功处理时，先静默保存工作，再发一句“没有可用模型，请在主会话选择模型。”不加“已建任务”，不带技术报错。原因是该错误表示持续的配置缺失，静默会让用户反复等待一个无法分流的前台；它并不证明后台执行者也不可用，所以仍保留任务。只针对 `errors.Is(reason, ErrNoModel)`；没有 Client 的测试/调用场景不等同于无模型，不能一律提示。每条失败入站至多一条提示；CLI 成功则不提示。本方案不增加跨会话提醒去重设置。

internal/frontdesk/client.go:12 `var ErrNoModel = errors.New("没有可用的前台模型")`

internal/frontdesk/model.go:26 `		return apisource.Source{}, "", ErrNoModel`

需要更新的 `desk_test.go` 用例及精确期望（省略 snapshot 本身已有的 `list` 时，下表均指 loop/fallback 事件）：

| 现有用例 | 新期望 |
| --- | --- |
| `TestLoop/轮数耗尽` | 四个 `list` 后只有 `create:原话:owner`，没有 `reply:`；CLI 至多一次 |
| `TestLoop/写工具无文字` | 只有 `create:工作:owner` |
| `TestFallback` | API 401 后 `create:工作:owner`；最近明确提及 t 的“继续”只有 `amend:t:继续` |
| `TestCLI/关闭`、`损坏` | 只有 `create:工作:owner`；调用次数仍为零、一次 |
| `TestCLI/正常` | 保留 `reply:收到` 在 `create:任务:owner` 之前的顺序断言 |
| `TestCLI/丢弃读` | 改成“读工具不允许且不提交回复”，进入静默 `create:工作:owner`，不发未校验的“收到” |
| `TestFrontdeskBehaviors/无效写入和未知工具返回错误` | 下一轮仍收到原工具错误；模型随后合法空答时无回复，不产生“模型没有返回内容” |
| `TestFrontdeskBehaviors/空文本的干净轮返回失败` | 改名为允许静默回复；事件为空、无任务、返回 nil |
| `TestSilentFallbackBoundaries` | 原四个边界断言保留；fake 返回明确提及时间，不再以 UpdatedAt 表示；注入 Feed，明确断言零回复 |
| `TestVisibleFallbackAndBusy` | 改名；普通模型失败/主动兜底均为 busy:true、list、create、busy:false；ErrNoModel 在 create 后多一条上述配置提示 |
| `TestModelFailureCLIFallback` | 合法“收到”保持一条 reply；损坏输出针对已明确提及 t 的短消息，仅 amend；没有明确提及时仅 create |

上述用例的当前入口与断言锚点：

internal/frontdesk/desk_test.go:91 `func TestLoop(t *testing.T) {`

internal/frontdesk/desk_test.go:135 `func TestFallback(t *testing.T) {`

internal/frontdesk/desk_test.go:154 `func TestCLI(t *testing.T) {`

internal/frontdesk/desk_test.go:311 `				if !reflect.DeepEqual(f.events, []string{"reply:没办成：模型没有返回内容"}) || msg.Role != "tool" || !msg.IsError || len(msg.Content) == 0 || !strings.Contains(msg.Content[0].Text, tc.message) {`

internal/frontdesk/desk_test.go:318 `	t.Run("空文本的干净轮返回失败", func(t *testing.T) {`

internal/frontdesk/desk_test.go:343 `func TestSilentFallbackBoundaries(t *testing.T) {`

internal/frontdesk/desk_test.go:408 `func TestVisibleFallbackAndBusy(t *testing.T) {`

internal/frontdesk/desk_test.go:469 `func TestModelFailureCLIFallback(t *testing.T) {`

新增覆盖：显式 ReplyTo 优先于最近任务、done 原 id 重开、四十字/十分钟含边界、未来提及时间无效、跨 feed 不串任务、短消息但没有团队提及要新建、首行含 CRLF、API 404/空响应/CLI 缺失/超时均静默、CLI 空答不建任务、CLI 有效空答写入不补确认、CLI 写入失败不重复创建、MaxRounds 超四钳制，以及闲聊和上下文内状态回答零写工具。保留裸编号、五位编号、五分钟改答和队列顺序测试。提示词路由测试可检查发送给模型的 prompt 和 scripted 调用契约，不能把 scripted 通过误说成真实模型必然正确分类。

## 3. owner 任务跳过固定审批与 review

**范围只认双重归属**：任务 `TaskMeta.Origin == "owner"`，且关联 Goal 的 `Owner == "steward"`。复用 `goalOwnerSteward`，提供一个 kanban 内部判定；Goal 不存在、meta 无效或不匹配时沿用旧行为。不能只看标题“总管”，也不能对所有 owner 或所有 steward Goal 内任务统一放开。普通 Goal 的 owner 任务、总管里的 delegated/followup 任务都保持原策略。

internal/app/frontdesk_adapters.go:113 `	t := store.Task{ID: uuid.NewString(), ProjectID: &projectID, GoalID: &goal.ID, Title: in.Title, Notes: in.Body, Status: "todo", Priority: string(priority), DependsOn: []string{}, Meta: kanban.EncodeTaskMeta(kanban.TaskMeta{Origin: "owner", Provider: in.Provider, Model: in.Model})}`

internal/kanban/service.go:37 `const goalOwnerSteward = "steward"`

现有停点与最小处理：

| 节点 | 当前行为 | 总管 owner 的新行为 |
| --- | --- | --- |
| 新建/排队 | 适配器直接写 todo，running Goal 调度，无单独的来源审批函数 | 保持；不要照搬 noop 增加 needsApproval 配置或状态 |
| 执行中审批/输入 | 会话 AwaitingApproval 或 AwaitingInput 映射 needs_approval | 真实权限请求和用户问题继续等待，不能伪装 done；删除固定流程产生的审批，不抹掉真实请求 |
| `needs_decision` 回执 | 创建现有决策卡并停 needs_approval | 保留；用户答案和改答沿用 AnswerDecision |
| 缺回执 | 追问两次后升级 needs_approval | 总管 owner 改为有界重试，最终 failed，不能要求用户替系统补回执 |
| 成功回执 | scheduler 无论 AutoReview 开关最终都先落 review；watchdog 也可能落 review | 在普通策略之前，直接设置 done；不调用 ReviewTask，不生成固定评审会话 |
| AutoReview 关闭 | 不是直接 done，而是 review 等人工 | 对上述双重归属任务完全忽略 AutoReview；其他任务不变 |
| autoAccept | scheduler 先 review 再 ReviewTask，且 [命令]/[你验] 阻止自动接受 | 不作为总管修复开关；保留普通看板行为 |

启动没有独立来源审批，调度器直接调用内部启动：

internal/kanban/scheduler.go:343 `					started, err := g.svc.startTaskInternal(ctx, t.ID, t.Revision)`

成功必须同时覆盖 scheduler 和 watchdog；只改其一会被另一路抢先消费回执并送审。

internal/kanban/scheduler.go:281 `					t.Status = string(TaskStatusReview)`

internal/kanban/watchdog.go:241 `					case autoReview:`

internal/kanban/service.go:264 `	return true`

真实等待不能靠改卡片状态消除。当前 runtime 档位沿用用户全局默认；本方案不把 owner 强制改成 fullAccess，不把所有 pending 请求自动 allow。noop 的“用户交代的任务不等初始批准”不等于覆盖用户明确选择的工具权限。低权限档位仍可能弹工具审批，这是保留的真实权限边界，不属于固定 review。要验收“全程无工具审批”，应在用户既有允许该操作的档位下验收；若产品要求连低权限档位也免批，需要另做明确的权限策略决策，不能在本修复里暗改。

internal/remote/kanban_driver.go:69 `	// 权限模式同理：非空时覆盖上面从 Lead 继承的值，为空则保持原行为（沿用 Lead 或后端默认）。`

internal/kanban/scheduler.go:196 `		if st.AwaitingApproval || st.AwaitingInput {`

internal/kanban/watchdog.go:182 `		if st.AwaitingApproval || st.AwaitingInput {`

继续保留危险操作的决策卡硬锁和答卡后的 override。执行者提示词要求合并、发版、部署、设置、花钱和对外发送在需要决策时提交 needs_decision。

internal/remote/hardlock_approver.go:83 `	return "危险操作已锁定，需要用户答决策卡后解锁：" + lock.Reason, true`

internal/kanban/decisions.go:56 `				meta.GateOverride, meta.DecisionID = d.ID, ""`

**执行者提示词必须一并去掉“永远等用户验收”**。`startTaskInternal` 读取 Goal 后得到上述范围布尔值，传给 `buildStartPrompt`；该函数增加一个明确布尔参数，更新现有调用和测试。总管 owner 分支不写下面的固定人工评审规则，改为：“你是这项工作的负责人，自行决定需要哪些调查、实现和检查。完成后提交 done；确实需要用户选择时才提交 needs_decision。不要为普通完成另请验收。”普通分支原文保留。

internal/kanban/service.go:707 `	b.WriteString("\n- 不要绕过人工评审直接把卡片结算为 Done，验收结论由用户给出。")`

**done 不偷偷合并**。现有 `ReviewTask(accept)` 同时负责主工作区合并，不能机械地把它当作“跳过评审”的实现。总管 owner 直接 done 表示执行产出已交付；隔离 worktree 和分支保留，summary 明说产出位置和链接。用户要求合并时由执行者按决策答案执行，完成后再提交结果。不要在调度器自动调用 autoMerge 或删除尚未集成的 worktree；普通看板 ReviewAccept 的合并和清理不动。这是本方案选择的最小行为，避免新增隐藏合并流程。

internal/kanban/service.go:774 `		if !s.autoMergeTaskLocked(ctx, &task) {`

internal/kanban/service.go:704 ``		b.WriteString("`）。不要在其中再创建新的 git worktree，所有改动直接在此工作区完成并提交。")``

**失败复用已有重试预算**：总管 owner 的 runtime Failed、ResultFailed、缺回执耗尽，统一经回执收尾的同一段判定处理。沿用 Goal 的 `maxAttempts`，默认二次；尚有预算时退 todo，保留失败原因供下一轮 prompt，scheduler 本 tick 不重启；耗尽置 failed，只通知一次最终失败。watchdog 不提前推失败，不在 ResultFailed 首次出现就发主会话。不要让一个失败把整个常驻总管 Goal 置 paused。其他 Goal 保留现有 rejected/paused 行为。

internal/kanban/scheduler.go:173 `	maxAttempts := getPolicyInt(goal.Policy, "maxAttempts", 2)`

internal/kanban/scheduler.go:223 `			if t.AttemptCount < maxAttempts {`

internal/kanban/scheduler.go:236 `				goal.Status = string(GoalStatusPaused)`

internal/kanban/receipt.go:403 `		t.Status = string(TaskStatusFailed)`

**缺回执不变成“请用户验收”**：保留现有两次追问；仅总管 owner 在追问耗尽后进入上述 failed 重试路径。hostToolsUnavailable 或不支持 followup 的旧路径，若有最后助手正文，则对总管 owner 补成完整 `ExecutorResult{Status: done, Summary: ...}` 再收尾；没有正文则按失败重试，不凭空生成答案。普通任务仍维持原兼容行为。

internal/kanban/receipt.go:364 `		return finishDecision{kind: finishDone, summary: s.lastAssistantText(ctx, t)}`

**用户待办从源头消失**：attention 看到 needs_approval/review 就会记待办，侧栏再显示数量。直接落 done，且不瞬时落 review，即可消除不必要待办；不需要隐藏所有总管任务，否则真正的决策与权限问题也会被藏掉。attention 仅需针对该范围重试中的非终态失败原因不记失败事件；普通任务不改。既有 review 卡可在 scheduler 遇到时对双重归属任务做惰性收尾：仅回执/Meta 明确是 done 且没有待答决策、活跃评审或真实 pending 请求时转 done；无法证明完成就重新执行获取回执，不做全库迁移。

internal/app/attention_wiring.go:501 `				records = append(records, kanbanAttentionAction{subjectID: id, kind: attention.KindKanbanNeedsApproval, snapshot: task})`

internal/app/attention_wiring.go:505 `				records = append(records, kanbanAttentionAction{subjectID: id, kind: attention.KindKanbanReview, snapshot: task})`

internal/app/attention_wiring.go:508 `		if task.FailureReason != "" && (!seen || before.FailureReason != task.FailureReason) {`

internal/ui/sidebar_steward.go:195 `			renderAttentionCountBadge(c, "sidebar-steward-attention", fmt.Sprintf("待处理 %d 条", count), count).`

## 4. 结果回报：聊天里就是用户的答案

链路已经具备所需 summary，不需要再调一个模型“汇总执行者”。执行者调用 kanban_submit_result → SubmitExecutorResult 保存完整 ExecutorResult → scheduler/watchdog 消费回执、落库 → notifyResult → frontdeskResultHandler → AppendStewardMessage。元描述来自执行者填写的内容；标题前缀是 UI 和推送层各自拼的，两处都要改。

internal/kanban/result.go:22 ``	Summary        string           `json:"summary"` ``

internal/kanban/receipt.go:160 `		Result:   res,`

internal/kanban/scheduler.go:295 `				g.svc.notifyResult(ctx, up, decision.result)`

internal/kanban/watchdog.go:268 `					w.svc.notifyResult(ctx, up, decision.result)`

internal/app/frontdesk_wiring.go:160 `	a.kanban.SetOnResult(frontdeskResultHandler(a, dataDir))`

internal/app/frontdesk_wiring.go:187 `			payload, err := json.Marshal(map[string]any{"task": task.ID, "thread": threadID, "title": task.Title, "summary": result.Summary, "status": "done"})`

internal/ui/steward_task_page.go:46 `	text := result.Title + "：" + result.Summary`

internal/app/frontdesk_wiring.go:238 `				body = fmt.Sprintf("%s：%s", task.Title, result.Summary)`

最小呈现方案保留 `steward_result` 结构化载荷和任务链接能力，新增可选 `owner` 布尔值；它只是展示来源，不是新的任务状态或新协议方法。owner 聊天成功结果只渲染 Summary 本身，推送正文也仅 Summary，不加标题，不截取成“已汇总给用户”。旧载荷没有 owner 字段按旧展示兼容。非 owner 成功结果用 role=system，正文“标题\nsummary”，不发桌面推送，也不增加未读；`steward_result` 的 system 分支交给现成系统消息渲染。不要让种类匹配优先于 role 又把它画成助手回答。

internal/app/frontdesk_wiring.go:174 `		owner := meta.Origin == "owner" && meta.ParentID == ""`

internal/app/frontdesk_wiring.go:230 `		if err := a.backend.AppendStewardMessage(ctx, feedID, "assistant", kind, text); err != nil {`

internal/ui/timeline_rows.go:2626 `	case m.Kind == stewardResultKind:`

internal/remote/steward_feed.go:136 `	unread := role != "user" && kind != "steward_reply" && kind != "steward_decision_answer"`

因此必须补读并小改 `internal/remote/steward_feed.go`：仅排除 `role == system && kind == steward_result` 的未读，不改其他系统提醒的语义。这是实现“安静系统消息”必需的越出 brief 列举文件之处。owner 的 NoChange 也不能吞掉用户主动要求的答案；只对非 owner 例行任务保留无变化静默。子任务结果可保留安静系统回报，最终由 lead 提交面向用户的答案。

internal/app/frontdesk_wiring.go:182 `			if meta.NoChange {`

在第 3 步的总管 owner 执行者 prompt 中追加以下完整回报约束：

```text
结束时调用 kanban_submit_result。summary 会原样发给交代这项工作的用户。
用用户的语言写一到三句，直接回答用户的问题，或说明完成了什么、查到了什么，并附有用的文件、提交、PR 或其他产出链接。
不要加任务标题，不要写“已汇总给用户”“供主代理参考”“任务已完成”这类元描述，不要让用户再打开任务才能看到答案。
只陈述已验证的事实。尚未运行检查、代码仍在独立分支、尚未合并或部署时，要如实说明；不要把隔离工作区中的改动说成已经集成。
需要用户决定时用 needs_decision，question 是不超过三十字的一个问题，并给出几个明确选项；不要在普通完成时请求用户验收。
```

宿主不做字符串替换来删除“已汇总给用户”，也不裁切 summary 的句数，避免损坏答案和链接。通过 prompt 和真实任务验收约束输出质量。

internal/kanban/service.go:708 `	b.WriteString("\n\n## 结束方式\n结束时必须调用 kanban_submit_result，status 选择 done、failed、waiting、delegated 或 needs_decision，并填写对应结果。")`

失败仅在第 3 步预算耗尽后发一句 `没能完成「标题」：原因`；原因来自执行者的可读 summary，runtime 原始 API 错误只入日志，使用现有错误分类转成短原因，不转发 JSON、堆栈或令牌。needs_decision 仍沿用现有卡片，不为完成/失败生成决策卡。收到相同执行回执的两路巡视只允许成功 CAS 的一方通知；旧 review 惰性转 done 不重发历史已回报的结果。

internal/app/frontdesk_wiring.go:207 `		case kanban.ResultNeedsDecision:`

internal/kanban/result.go:73 `			return fmt.Errorf("needs_decision 必须提供问题和至少两个选项")`

**建议保留“打开任务”按钮**。它已有实现和测试，是可选查看执行细节的入口；没有新说明文字，也不承担“打开才能得到答案”的职责。只要主会话完整展示 summary，就无需删除入口、任务信息带或相关状态。没有 Thread 的结果仍不显示按钮。

internal/ui/steward_task_page.go:55 `			if result.Thread != "" {`

internal/ui/steward_task_page.go:58 `						Text:    "打开任务",`

另外，直接问答现在走 `steward_reply`，却被固定为单行省略号。把该组件正文改为可换行、可选择的正常正文字号，继续不画助手头部和操作栏；否则即使模型直接答“今天发生了什么”，窄窗口仍会截断答案。复用现有 ui.Text，不新增组件、不引入 CrychicUI、不新增动效。

internal/ui/steward_reply.go:9 `			ui.Text(c, m.Text).FontSize(scaledFont(TypeFootnote)).TextColor(ThemeSecondaryText(c)).`

internal/ui/steward_reply.go:10 `				SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)`

## 5. 保留“总管”Goal，作为常驻队列

保留每个真实项目下现有的总管 Goal 和任务关联，不另建任务系统、不隐藏或删除看板卡。创建时继续直接 `store.CreateGoal` 写 running，再 `CreateGoalTask` 原子关联，绝不改走 kanban.CreateGoalWithOptions。这条路径本来不触发 planner；无需为了“不规划”再添加 policy 开关。

internal/app/frontdesk_adapters.go:97 `		goal = store.Goal{ID: uuid.NewString(), ProjectID: projectID, Title: "总管", Prompt: "总管", Status: "running", Owner: "steward", Policy: map[string]any{"maxConcurrency": 1}}`

internal/app/frontdesk_adapters.go:98 `		if err = s.store.CreateGoal(ctx, goal); err != nil {`

internal/app/frontdesk_adapters.go:114 `	if err := s.store.CreateGoalTask(ctx, t, goal.Revision); err != nil {`

普通 Goal 的 planning/plan_review、planner 和协调员照旧。总管 Goal 不设置 PlanThreadID，就不会注入目标协调员身份，也不显示计划审批。

internal/kanban/service.go:1226 `		s.runPlan(createdGoal, nil, "")`

internal/kanban/service.go:1610 `			latest.Status = string(GoalStatusPlanReview)`

internal/kanban/coordinator.go:36 `		if goals[i].PlanThreadID != nil && *goals[i].PlanThreadID == threadID {`

需要补的两个小洞：

- 复用 Goal 要同时匹配 `Title == 总管 && Owner == steward`，不能捡到用户自己建的同名目标。已有 completed 的 steward 总管 Goal 在需要新建或 amend 重开时恢复 running，再读取新 revision；cancelled 不自动恢复，避免覆盖用户取消。暂停若来自历史自动失败，按第 3 步修复不再产生；显式 paused 保持用户选择。
- scheduler 不把常驻总管队列在所有任务 done 后自动 completed；只对 `Owner == steward && Title == 总管 && PlanThreadID 为空` 的容器跳过自动完成。这样最近 done 任务收到 amend 时仍能用原 id 排队，避免每次完成就新建一个同名 Goal。普通 steward 创建的真实计划 Goal 不受影响。

internal/app/frontdesk_adapters.go:91 `		if g.Title == "总管" && g.Status == "running" {`

internal/kanban/scheduler.go:380 `		if allCompleted && goal.Status == string(GoalStatusRunning) {`

internal/store/kanban.go:74 ``		AND status IN ('plan_review', 'running', 'paused')`,``

amend 保留原任务 id；done/failed/cancelled/waiting 已有重开基础，补 rejected 的历史兼容，清理旧 ResultStatus、Summary、NoChange、失败及评审残留和本次重开应重置的 AttemptCount，避免新要求直接继承耗尽预算。任务若有 open DecisionID，转答卡而非重开；legacy review 交第 3 步收尾后再追加，或在无活跃评审/真实请求时允许同 id 回 todo。并让关联的 completed 总管容器恢复 running。in_progress 仍给当前执行会话追加消息，不另起任务。

internal/app/frontdesk_adapters.go:136 `	case "waiting", "done", "failed", "cancelled":`

internal/app/frontdesk_adapters.go:128 `		_, err = s.backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: *t.ThreadID, Text: text})`

主会话不输出 Goal 名、计划状态、创建模板或流程承诺；看板仍可按需查看。侧栏、决策卡及当前导航结构保留。

internal/ui/sidebar_steward.go:170 `		s.openStewardThread()`

## 6. 四组并行实施，文件互不重叠

以下是未来实施者的修改白名单。本文只创建方案文档。四组可并行编码；先约定接口和 payload，再由主代理合并并验证，不要求每组单独检出都能编译跨组接口。

| 组 | 可修改文件（没有“等”或隐含共享文件） | 边界契约 |
| --- | --- | --- |
| A 前台分流 | `internal/frontdesk/context.go`、`internal/frontdesk/desk.go`、`internal/frontdesk/exec.go`、`internal/frontdesk/fallback.go`、`internal/frontdesk/cli_fallback.go`、`internal/frontdesk/tools.go`、`internal/frontdesk/desk_test.go` | `RecentTask(ctx, feedThreadID) (store.Task, time.Time, error)`；保留 Inbound.ReplyTo；写入后不自动补回复 |
| B 任务执行与收尾 | `internal/kanban/service.go`、`internal/kanban/scheduler.go`、`internal/kanban/watchdog.go`、`internal/kanban/receipt.go`、`internal/kanban/service_test.go`、`internal/kanban/scheduler_test.go`、`internal/kanban/watchdog_features_test.go`、`internal/kanban/receipt_test.go`、`internal/kanban/acceptance_test.go` | 双重归属限定自动完成；`buildStartPrompt` 增加布尔参数并更新本组调用；沿用 ExecutorResult，不改状态枚举 |
| C 适配器、回报与待办 | `internal/app/frontdesk_adapters.go`、`internal/app/frontdesk_wiring.go`、`internal/app/frontdesk_wiring_test.go`、`internal/app/attention_wiring.go`、`internal/app/attention_wiring_test.go`、`internal/remote/steward_feed.go`、`internal/remote/steward_feed_test.go` | 实现 A 的 RecentTask；成功 owner 结果保留 JSON 且新增 `owner:true`；非 owner 系统结果为 plain 标题换行 summary；最终失败才发消息 |
| D 结果与正文展示 | `internal/ui/steward_task_page.go`、`internal/ui/steward_task_page_test.go`、`internal/ui/steward_reply.go`、`internal/ui/steward_reply_test.go` | 读取可选 owner；owner 成功只显示 summary；system result 走现成系统渲染；保留按钮，直接回复允许换行 |

A 的锚点、改法、测试和验收：

internal/frontdesk/context.go:12 ``const systemPrompt = `你是主会话的前台，每条用户消息单独处理一次。``

internal/frontdesk/exec.go:111 `			if strings.TrimSpace(text) == "" {`

internal/frontdesk/fallback.go:78 `	task, err := d.Tasks.CreateTask(ctx, CreateTaskInput{Title: title, Body: in.Text, Origin: "owner"})`

- 执行第 1、2 步；工具描述可以补充“工作才建任务”“原 id 重开”，不加工具或改变现有参数 schema。
- 更新第 2 步列出的全部现有用例及 fake，新增所列边界测试。
- 验收：简单问答无任务；读工具先读再答；正常空回复无模板；API/CLI 故障和耗尽不丢工作、不发错误；仅 ErrNoModel 有配置提示；决策改答仍有效。

B 的锚点、改法、测试和验收：

internal/kanban/service.go:613 `	prompt := buildStartPrompt(task, deps, branchName, inWorktree)`

internal/kanban/receipt.go:387 `func (s *Service) applyExecutorResult(ctx context.Context, t *store.Task, res ExecutorResult, now time.Time) error {`

internal/kanban/scheduler.go:272 `			default:`

internal/kanban/watchdog.go:236 `				default:`

- 在现有 Service/receipt 中集中双重归属判断和总管结果策略，不另建 pipeline。两路成功都先判总管 owner，直接 done；失败共用预算；缺回执有界重试；队列常驻；已有 review 惰性兼容。
- 更新 `TestStartTask`、`TestStartPromptMentionsSubmitResult`、`TestBuildStartPromptAcceptanceCriteria` 的签名调用，普通 prompt 原有断言不减。新增总管 prompt 直接面向用户、没有固定验收要求的断言。
- scheduler/watchdog 各测 owner+steward 在 AutoReview true/false、autoAccept true/false 下均 done；owner+普通 Goal、非 owner+steward 仍沿用旧 review 路径。覆盖真实 pending/needs_decision 仍阻塞，缺回执与 ResultFailed 首次不通知、最终仅一次、Goal 不暂停，hostToolsUnavailable 有正文可正常回报，无正文失败。
- 覆盖 scheduler/watchdog 前后顺序和重复 tick 不重复通知；常驻容器不 completed、普通 Goal 仍 completed；legacy review 安全收尾。保留 [命令]/[你验] 普通看板测试；验证总管 done 不触发宿主 squash/清理，普通 ReviewAccept 仍保留原行为。
- 验收：在既有权限允许的情况下，用户工作从 todo 到 in_progress 再到 done，不经过固定 review/验收；真实决策卡仍能等待、答复和继续。

C 的锚点、改法、测试和验收：

internal/app/frontdesk_adapters.go:119 `func (s *frontdeskServices) AmendTask(ctx context.Context, id, text string) error {`

internal/app/frontdesk_adapters.go:177 `func (s *frontdeskServices) RecentTask(ctx context.Context) (store.Task, error) {`

internal/app/frontdesk_wiring.go:163 `func frontdeskResultHandler(a *App, dataDir string) func(context.Context, store.Task, kanban.ExecutorResult) {`

internal/app/attention_wiring.go:683 `		next[view.Task.ID] = kanbanSnapshotFromTask(view.Task)`

- 执行第 2 步的最近明确提及适配器、第 4 步来源/回报/静默、第 5 步容器识别与重开；不增加数据库字段。任务重开清理旧元数据但保留用户原始 notes 与任务 id；open 决策优先答卡。
- attention 扫描从 Board 的 Goal 建归属表，只有双重归属且非终态的自动重试才抑制失败原因事件；保留真实审批/决策与普通看板全部提醒，旧待办离开状态自动消解。
- 更新 `TestFrontdeskTaskAndMigration`：同名普通 Goal 不复用，已有总管 Goal 复用、不启动 planner、completed 恢复、新任务正确原子关联。补同 id amend 重开及预算清理、进行中追加、决策优先和跨 feed 最近提及测试。
- 更新 `TestFrontdeskResultRouting`：owner JSON summary 原样且带 owner；push body 精确等于 summary；owner NoChange 仍回答；非 owner 为 system、标题换行 summary、无 push/无 unread；只有 needs_decision 出卡。最终失败格式准确、重试中零消息；安静时段仍保存结果而不推送。
- 更新 attention 测试：总管正常完成没有 review 待办，重试不刷失败待办，最终失败/真实决策仍有待办；普通 Goal 的原有断言不改。remote feed 测试只新增安静系统结果不置 unread 的覆盖，普通助手和其他系统通知保持原值。
- 验收：主会话收到的是执行者给用户的答案；失败不提前打扰；旧任务补充不会生出另一个 Goal 或任务；普通看板现有行为不变。

D 的锚点、改法、测试和验收：

internal/ui/steward_task_page.go:40 `func renderStewardResult(c *ui.Context, s *chatState, m Message, liveID, lastLiveID string, isLastAssistant, hasUsage bool, turnUsage TokenUsage, turnDur time.Duration, suppressTurnEnd bool) ui.Element {`

internal/ui/steward_task_page_test.go:23 `func TestStewardTaskResultOpensThreadAndShowsBand(t *testing.T) {`

internal/ui/steward_reply_test.go:12 `func TestRenderStewardReplyPlainLine(t *testing.T) {`

- 给 stewardResult 加可选 Owner 字段，owner done 不加标题；failed 文案只拼一次；role=system 先走现有 renderSystemMessage。保留旧 JSON/纯文本兼容，不新建结果卡。
- `steward_reply` 去掉 SingleLine/Ellipsis，正文可换行和选择，保留无头像、无头部、无额外说明的布局。
- 更新解析/渲染测试：owner summary 精确显示、无“标题：”前缀；非 owner 系统结果安静展示；旧 JSON/纯文本仍可读；“打开任务”仍打开原执行会话并显示已有信息带；窄窗口长问答不截断，链接可见，决策卡样式不变。
- 验收：问题的答案在主会话完整可读，查看任务只是可选动作；界面没有新增说明文字或流程状态标签。

集成后的人工验收序列：先发闲聊、上下文内问题和“现在在跑什么”，再发真实代码工作，再对已完成工作说“不行，再……”，最后模拟 API 404、四轮耗尽和无模型配置。检查任务数、任务 id、主会话正文、决策卡和侧栏待办；用同一个普通 Goal 回归原 review/autoAccept 行为。本文没有执行构建、测试、lint、类型检查、GUI 验证或其他验证；这些全部交主代理在其检出中完成。

明确的实现选择与限制：保留任务按钮；ErrNoModel 是唯一可见模型配置提示；保留用户选择的运行时权限边界；总管 done 不暗中合并独立分支；不扩展 ReplyTo 的 UI/RPC 传输。若后续要求覆盖低权限工具审批或提供显式聊天回复目标，需单独落实权限/协议契约，不应把这些未实现能力写成已完成。
