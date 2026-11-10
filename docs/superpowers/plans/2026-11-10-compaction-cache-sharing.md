# 压缩摘要请求复用主对话缓存

日期：2026-11-10。代码基线：`bd1592b`。状态：设计完成，未改业务代码，未运行测试。行号对应本工作副本；实现时用紧随锚点的源码定位。

## 1. 根因、历史与推荐结论

任务提供的生产统计：`e235d39` 于 2026-11-10 15:29:25 +08:00 上线后，terra 的压缩回合缓存率由 93–98% 降至 61–70%，每回合未命中输入由约 50–240k 增至 380–793k。本次没有重新验证生产数值。

`internal/agent/compact.go:536` — `	request := llm.Request{Model: model, System: summaryInstruction, Messages: prefix}`

独立 System、缺失 Tools 和只发送目标片段，使摘要请求无法复用主请求的完整前缀。另有一个源码确认的损失：工具结果无条件截断，进一步改变输入内容。

`internal/agent/compact.go:489` — `			truncate(&prefix[i])`

`internal/agent/compact.go:146` — `	result.Messages = append(append(append([]llm.Message(nil), messages[:start]...), summaryMessage(summary)), messages[end:]...)`

块摘要替换中间片段以后，主请求在替换点之后的自动前缀缓存失效。两种损失叠加；仅修复触发频率不能消除它们。

已读取相关 git 历史：`e235d39` 接入块写入和请求缓存断点，`0c82d0c` 接入块重放和稳定 recall 序号，`8541d26` 阻止无节省块在本轮反复尝试，`5142443` 增加总量下限，`c9df8a0` 将判定窗口封顶到 200k。后两项已在当前源码，保持不变。

`internal/agent/compact.go:68` — `	return (growth >= growthThreshold(window) && total >= window/2) || total >= normalPressure, total >= emergencyPressure`

推荐第一版：共享摘要请求使用主请求的 System、Tools、Model、Effort、MaxTokens，以及未经修改的 `messages[:end]`，另加一条 user 指令，仅总结 `[start,end)`。保留原有独立摘要作为超限及无效工具调用的回退。块摘要继续原位替换，不改 JSONL、重放、recall 或尾部保留策略。

共享路径保持原有工具选择设置，通过指令要求仅输出文本，并在结果层拒绝任何工具调用。这样既不执行工具，也不主动切换 Anthropic 的消息缓存配置。此处的“拒绝”是丢弃摘要响应并进入受限回退，不向主 agent 的工具执行器转交。

## 2. 调用方清单与影响范围

已执行有范围的源码检索：

```sh
rg -n 'compactBlock\(|compactMessages\(|summarize\(' internal/agent --glob '*.go'
rg -n 'summaryInstruction' internal/agent --glob '*.go'
```

以下是三个函数的全部直接调用方，不含函数定义：

| 被调用函数 | 调用位置与原文 |
| --- | --- |
| compactMessages | `internal/agent/agent.go:222` — `		result, err := compactMessages(ctx, opts.Client, opts.Model, input, window, overhead)` |
| compactMessages | `internal/agent/compact_test.go:363` — `	if _, err := compactMessages(context.Background(), client, "test", messages, 12000, 0); err != nil {` |
| compactMessages | `internal/agent/compact_test.go:390` — `	if _, err := compactMessages(context.Background(), client, "test", messages, 12000, 0); err != nil {` |
| compactMessages | `internal/agent/compact_test.go:413` — `	if _, err := compactMessages(context.Background(), client, "test", messages, 12000, 0); err != nil {` |
| compactMessages | `internal/agent/compact_test.go:425` — `	if _, err := compactMessages(context.Background(), client, "test", messages, 12000, 0); err != nil {` |
| compactBlock | `internal/agent/agent.go:242` — `		result, err := compactBlock(ctx, opts.Client, opts.Model, entries, window)` |
| summarize | `internal/agent/compact.go:137` — `	summary, usage, err := summarize(ctx, client, model, messages[start:end], window)` |
| summarize | `internal/agent/compact.go:460` — `	summary, usage, err := summarize(ctx, client, model, messages[:cut], window)` |

直接受影响的测试文件是 `internal/agent/compact_test.go`。关联验收文件为 `internal/agent/recall_test.go`、`internal/agent/agent_test.go`、`internal/agent/llm/openai_test.go`、`internal/agent/llm/anthropic_test.go`、`internal/agent/session/session_test.go`。不需要修改 session 存储实现。

以下断言也必须改成同时识别共享与独立摘要，不得继续把“System 等于 summaryInstruction”作为唯一识别条件：

`internal/agent/compact_test.go:251` — `	if req.System == summaryInstruction {`

`internal/agent/compact_test.go:275` — `		if request.System == summaryInstruction {`

`internal/agent/compact_test.go:296` — `	if req.System == summaryInstruction {`

`internal/agent/compact_test.go:341` — `	if err != nil || len(client.requests) != 2 || client.requests[1].System == summaryInstruction {`

`internal/agent/compact_test.go:573` — `		if request.System == summaryInstruction {`

`internal/agent/compact_test.go:606` — `	if summaryRequest.System != summaryInstruction || len(summaryRequest.Tools) != 0 || len(summaryRequest.Messages) != 3 {`

`internal/agent/compact_test.go:724` — `	if client.requests[1].System != summaryInstruction || !isSummaryMessage(client.requests[2].Messages[0]) {`

建议测试内用共享指令的固定起始句识别共享摘要；独立回退仍按 System 识别。标记只用于测试定位，不增加生产请求字段，也不写入会话。

## 3. 请求设计与取舍

### 3.1 主请求快照及新签名

`internal/agent/agent.go:184` — `	request := llm.Request{Model: opts.Model, Effort: opts.Effort, System: opts.System}`

`internal/agent/agent.go:266` — `	setRequestMessages := func() {`

将当前 setRequestMessages 中“复制消息、重新扫描摘要断点”的逻辑提为 Run 内局部函数 `requestForMessages(input []llm.Message) llm.Request`，定义在压缩闭包之前。它复制当前 `request` 值，设置独立外层消息切片，重新生成 CacheBreakpoints，返回快照。setRequestMessages 保留现有无参调用形式，内部改为 `request = requestForMessages(messages)`。

不要在压缩入口直接复用 `request.Messages`：第一次自动压缩发生在本轮 setRequestMessages 之前；发生工具调用、reload 或溢出重试时，旧值也可能过期。compactNow 用当前 input 创建快照；compactBlockNow 用本次加载的 entries 经 historyFromEntries 得到的消息创建快照。这份 entries 同时传给块规划，确保选区与快照来自同一次加载。

`internal/agent/agent.go:279` — `		request.System += "\n\n" + gate.PlanModePrompt()`

`internal/agent/agent.go:282` — `		request.Tools = append(request.Tools, llm.ToolSpec{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters})`

快照读取已经构造完成的 System 和 Tools，包含 PlanModePrompt、工具原顺序、描述及 Parameters 原字节。不能用 opts.System / opts.Tools 重新拼装，以免遗漏 plan 或 recall。闭包可以提前定义，但只能在现有初始化完成之后调用。

拟改签名如下；类型均为现有类型，不引入新配置结构：

```go
func compactBlock(ctx context.Context, client llm.Client, base llm.Request, entries []json.RawMessage, window, overhead int) (*compactionResult, error)
func compactMessages(ctx context.Context, client llm.Client, base llm.Request, window, overhead int) (*compactionResult, error)
func summarize(ctx context.Context, client llm.Client, base llm.Request, start, end, window, overhead int) (string, llm.Usage, error)
func summarizeTruncated(ctx context.Context, client llm.Client, model string, messages []llm.Message, window int) (string, llm.Usage, error)
```

compactMessages 使用 base.Messages 计算 cut，调用 summarize 的范围为 `[0,cut)`。compactBlock 仍从 replay items 规划 `[start,end)`，base.Messages 必须是同一 entries 的重放视图。summarizeTruncated 是现有 summarize 主体改名后的回退，不再负责共享前缀。

overhead 用于共享请求预算，不在 compactBlock 内重复计入 Before/After；保留 compactBlockNow 当前为 Before/After 加 overhead 的位置。整段压缩的 Before/After 仍在 compactMessages 内包含 overhead。

### 3.2 精确共享到哪里

令 P 为目标之前的原文及冻结摘要，B 为目标段，T 为保留尾部。主输入是 `P | B | T`。

| 摘要输入 | 缓存与费用 | 结论 |
| --- | --- | --- |
| 独立 System + B | 无共同对话前缀；输入较短 | 仅作受限回退 |
| 原 System/Tools + P + B + 指令 | 前缀到 end 保持不变；不额外发送 T | 推荐 |
| 原 System/Tools + P + B + T + 指令 | 可覆盖更长已缓存输入；读取 T 仍有费用，指令更难限定目标，超限概率更高 | 第一版不采用 |
| 将范围标记插入 P 或 B | 标记之前以外的共同前缀被主动打断 | 不采用 |

共享路径只允许新建外层消息切片和末尾 user 消息。保留 Content、图片数据、ToolCallID、ToolCalls、Arguments、Reasoning、ReasoningSignature、ThinkingBlocks 的值及顺序，不截断工具结果，不补开头 user，不修改已有 user 的 Content。不原地排序 Tools 或修改其 schema。

如果截取后起点不是 user，或边界不是当前配对规则允许的完整消息组，直接走独立回退；不要通过修改共享前缀来修补。块规划和 findCut 本来就负责正常的安全边界。

CacheBreakpoints 复制并仅保留下标 `< end` 的既有断点，不把新指令当成摘要块。Anthropic 仍按现有逻辑限制最多四个断点；不额外改其序列化策略。

### 3.3 末尾用户指令

必须始终新增一条 user 消息。即使目标最后一条是 user，也不能把指令拼进它的 Content，否则 OpenAI 序列化后的该条消息从正文开始即发生变化。

指令以固定起始句“你正在为当前会话生成压缩摘要。”开头，后接动态范围与规则。动态范围应表述为：

> 只总结本指令之前的会话消息中，零基下标 `[start,end)` 的消息，即第 `start+1` 条至第 `end` 条。计数不含 System、Tools 和本条指令。前面的消息仅供理解语境，不要再次总结；本条指令也不属于摘要内容。目标之后的近期消息未随此请求发送，不要补写其内容。只输出摘要正文，不调用任何工具，不继续执行原任务。

下标基于 llm.Request.Messages，不能数 OpenAI 的额外 system message，也不能数 Anthropic 合并后的 provider messages。指令明确按角色消息计数；它是模型输入中的范围提示，写入持久层的覆盖范围仍完全由代码决定。块记录的 FromSeq / ToSeq 继续用 replay item 稳定序号，而且是两端包含；不要把右端开放的消息下标 end 直接写成 ToSeq。

随后嵌入 summaryInstruction 的现有规则，将首句笼统的“conversation so far”收窄为刚刚指定的目标区间，其余规则完整保留。范围限定优先说明旧摘要仅为上下文；实现时共用规则文本，避免维护两份保真要求。规则必须覆盖：

- 用户目标、决策及原因、变更文件及原因、未完成工作和关键事实。
- 原样保留路径、函数签名和标识符、错误、命令、确切数字、用户原始请求、约束、未完成工作。
- 删除冗长日志、重复文件内容、已完成探索；死路仅保留一行教训。
- 篇幅优先级：用户目标 > 决策和原因 > 精确产物 > 结论 > 教训。
- 只输出摘要，无前言、无 Markdown 代码围栏。

旧摘要可以出现在 P，不能被选进普通块 B；它可以帮助理解代词和目标，不能被当作再次覆盖的范围。范围提示不能保证模型百分之百不重复内容，需要用真实任务抽样检查摘要质量。

### 3.4 工具调用处理与 provider 限制

`internal/agent/llm/types.go:55` — `type Request struct {`

`internal/agent/llm/openai.go:212` — `		body["tool_choice"] = "auto"`

`internal/agent/llm/anthropic.go:199` — `	if len(req.Tools) > 0 {`

当前 Request 没有 ToolChoice。OpenAI 在有工具时硬编码 auto，Anthropic 未输出 tool_choice。因此当前代码不支持请求层指定 none。

第一版不新增这个字段。共享摘要保留两种 provider 当前的工具选择行为，末尾指令禁止调用工具；收到 response 后，在 messageText 之前检查 `len(response.Message.ToolCalls)`。只要非零，即使同时有正常文本，也丢弃该响应正文、保留 Usage，然后进行一次无 Tools 的独立摘要回退。summarizeTruncated 也必须拒绝工具调用，错误交由现有兜底处理。任何响应均不进入主工具执行循环，不把调用及伪造结果写入 JSONL。

这是刻意的取舍：API 层允许模型提出工具调用，但本地摘要路径从不执行它。它避免为 provider 分支增加客户端类型判断，并保留 Anthropic 消息缓存条件。若某些模型持续违反摘要指令，会反复走独立回退，必须作为上线风险观察。

若后续确需 API 强制 none，最小改法是给 Request 增加 `ToolChoice string`（零值保持正常行为），OpenAI 用非空值覆盖 auto，Anthropic 将 none 编码为 `{"type":"none"}`；并修改 TestOpenAIToolChoice、新增 TestAnthropicToolChoice。不应使用移除 Tools 作为共享路径的禁用手段。该变体不列入本次推荐实施，因为 Anthropic 官方明确：改变 tool_choice 会失效 Messages cache。

已直接读取官方正文：

- https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-use-with-prompt-caching
- https://platform.claude.com/docs/en/build-with-claude/prompt-caching

前者的失效表还列出 thinking、effort、图片有无等影响。因此共享路径沿用 Effort 和 MaxTokens；不能为了减少摘要输出而顺便关闭 thinking。Anthropic user/tool 合并、自动断点、lookback、TTL 和先前是否写入缓存，均可能限制实际命中。相同输入是必要条件，不能作为所有 provider 的命中保证。

### 3.5 预算、回退和费用累计

优先构建 `base.Messages[:end] + 独立指令`。预检计算 `tokensForMessages(candidate) + sharedOverhead`，指令必须计入，要求不超过 compactThreshold(window)。sharedOverhead 按 base.System 和 base.Tools 当前内容、沿用现有字符估算法重新计算，并取它与传入 overhead 的较大值；首次成功压缩后动态 addRecall 可能使 Run 开始时的 overhead 过期，不能漏算新增工具。这个预算值不回写现有 Before/After 记账。沿用当前已封顶的 window，第一版保守地不利用真实大窗口额外容量；不改变 200k 压缩判定上限。保留 MaxTokens / Effort 可能产生高于现有 reserveTokens 的实际输出预算，字符估算、图片估算和网关限制也可能不精确，因此预检只能提前筛选，仍必须处理实际拒绝。

有以下任一情况，进入一次 summarizeTruncated：

1. 预检超预算，或无法原样构成合法前缀：不发共享请求，直接回退。
2. 共享请求返回 llm.IsContextOverflow 可识别的上下文错误，或明确的 APIError HTTP 413 请求体过大：保留该次 Usage，再回退。
3. 共享响应包含工具调用：保留 Usage，丢弃内容，再回退。

取消、超时、鉴权、429、529、普通服务错误、空响应和空摘要保持现有错误语义，不为了试另一种提示而重复付费；尤其不能把任意 400 当作超限。若网关有独有长度错误，先取得明确报错样本，再扩充判定与测试。本方案不臆测字符串。

一次逻辑摘要最多发出“共享一次 + 独立一次”两次 Stream 调用，不递归重试、不启动后台任务。llm 客户端内部既有 HTTP 重试不在此处新增。整段压缩发生在主请求 overflow 重试内时，也遵守同一上界；主请求自身的“最多因 overflow 重发一次”保持不变。

回退只接收目标 B，继续使用原 summaryInstruction 作为 System，不传 Tools，保留已有的工具结果截断、必要时按工具组丢弃、旧摘要保留、assistant 开头补 user 及尾部指令行为。截断模式属于明确放弃共享的路径，不得再次尝试拼回 P。为回退预算补计其自身 summaryInstruction 和末尾指令；工具配对与历史摘要保留规则保持原样。

无论返回成功或失败，累计两个响应非空时的 Usage 四个分量：Input、Output、CacheRead、CacheWrite。第一次已收费但第二次失败也必须返回累计值，交给 addCompactionUsage 计入回合；不能只返回最后一次 Usage。

`internal/agent/agent.go:202` — `		usage.InputTokens += int64(result.Usage.Input + result.Usage.CacheRead + result.Usage.CacheWrite)`

`internal/agent/agent.go:204` — `		usage.CachedTokens += int64(result.Usage.CacheRead)`

共享失败和回退失败均不写 compaction entry；自动压缩失败后原上下文继续执行，且本轮停止再次自动尝试的行为保持不变。块无节省时仍计费用，且不写 entry、不反复重试。

## 4. 压缩后主请求还可减少多少失效

| 做法 | 共同前缀与代价 | 建议 |
| --- | --- | --- |
| 块摘要原位替换 | 保住 start 之前的开场消息和旧块，B 摘要及后方 T 重新处理 | 保留 |
| 摘要移到消息开头 | 更早改动前缀，旧块及更多内容失效，并改变重放结果 | 不采用 |
| 摘要移到末尾并删除 B | 第一处差异仍在 B 原位置，缓存不会因后移摘要恢复；改变消息时间关系和 entry 语义 | 不采用 |
| 保留 B，再在末尾加摘要 | 最大程度保留缓存，但不腾出上下文，持续增加输入 | 不能满足压缩目标 |
| 少留尾部 T | 共同前缀长度并不增加；重新处理的尾部更短，但更多近期细节进入有损摘要，切点和 recall 需求改变 | 第一版不调整 |
| 多留 T、缩小 B | 失效尾部更长，压缩节省更少，更容易很快再次触发 | 不作为缓存优化 |

在“删除 B、保持顺序、保留旧块与 JSONL 现有语义”这些约束下，首个差异最早就是 start，原位替换已达到能够保留的前缀上界；不能声称它在任意摘要质量与尾部长度取舍中都全局最优。

当前 keepRecentTokens 为最多 20k。可在本次修复验证以后，独立比较 20k 与 10k 尾部，但必须同时观察近期细节丢失、recall 调用量、下轮再次压缩率及总费用，不能仅凭命中率决定。本次不同时改变两个变量。

另一个已确认的首次压缩成本：首次成功压缩会 addRecall，改变 Tools，因此下一主请求的工具前缀也可能整体失效。

`internal/agent/agent.go:231` — `			addRecall()`

`internal/agent/recall_test.go:130` — `			t.Fatal("premature recall")`

若从首次请求就注册 recall，可稳定工具集，但会改变现有“产生折叠历史后才提供 recall”的行为和测试约定。本次保持该行为，统计时区分首次与后续压缩，不能把首次工具集变化误算成共享失败；后续已有 recall 的压缩才更适合衡量中间替换的固定成本。

按缓存读为原价 10% 的假设计价，旧摘要 B 原价约 B；共享路径若全部命中，输入等价成本约 `0.1*(P+B)+instruction`。P 很长而 B 很短时，即使命中，共享也可能更贵；网关冷缓存时更可能退化。不同模型价格并不一致，本次不写死“9 倍前缀”等计费门槛。必须用未命中输入和计价等效输入同时验收，不能只看缓存率。

## 5. 分步实施与验收

### 步骤 1：集中构建主请求消息快照，改压缩函数接缝

`internal/agent/agent.go:266` — `	setRequestMessages := func() {`

`internal/agent/agent.go:222` — `		result, err := compactMessages(ctx, opts.Client, opts.Model, input, window, overhead)`

`internal/agent/agent.go:242` — `		result, err := compactBlock(ctx, opts.Client, opts.Model, entries, window)`

具体改法：按 3.1 提取局部 requestForMessages，保持 setRequestMessages 的两个正常请求调用位置。压缩闭包分别传入当前 input 的快照和同次 entries；采用列明的新签名。更新四个测试直接调用方。保留初始化顺序、after/before overhead 口径和压缩失败行为。

新增 `TestCompactionRequestSnapshotIncludesPlanAndRecall`：已有折叠记录、plan 开启、固定工具顺序、非空 Effort/MaxTokens；验证摘要快照包含完整 System（含 plan）、Tools（含 recall）、当前新消息，而不是上一请求的 Messages。新增 `TestCompactionRequestSnapshotDoesNotMutateMainRequest`：生成摘要输入后主请求的消息、Content、工具参数和 CacheBreakpoints 保持原值。

验收：正常主请求 body 除已有业务变化外保持相同；compactNow 与 compactBlockNow 都不遗漏动态配置；已有工具执行、Plan 和用量测试不回归。

### 步骤 2：共享摘要前缀，保留独立裁剪回退

`internal/agent/compact.go:473` — `func summarize(ctx context.Context, client llm.Client, model string, messages []llm.Message, window int) (string, llm.Usage, error) {`

`internal/agent/compact.go:529` — `	instruction := llm.Part{Type: "text", Text: "请按系统提示的要求，总结以上对话，用于在新的上下文中继续工作。只输出摘要。"}`

具体改法：现有主体改名 summarizeTruncated；新的 summarize 按 3.2–3.5 创建候选、追加范围指令、做预算预检并调用共享请求；仅在规定条件下回退。共享路径禁止截断和末条 user 合并；回退维持截断和配对保障。两种压缩函数只负责选择范围及生成 entry，不复制请求逻辑。

新增测试：

- `TestSummarizeSharesMainRequestPrefix`：固定主请求，目标从中间开始，包含长工具结果、图片、工具调用参数、reasoning 和 thinking signature。断言共享请求的 System、Tools、Model、Effort、MaxTokens 与主请求逐项相同，`Messages[:end]` 与主请求同段 DeepEqual；对 ToolSpec.Parameters 另做字节相等断言。
- `TestSummarizeAppendsSeparateUserInstruction`：目标最后一条为 user；断言原 user 内容不变，末尾新增一条 user，指令完整包含范围和五类摘要规则。
- `TestSummarizeRangeExcludesFrozenSummaryAndTail`：P 含旧摘要，B 含可识别目标；断言 P 仍在请求里、范围 start 跳过旧摘要、T 不发送、生成 entry 覆盖仍仅 B。
- `TestSummarizeBudgetFallback`：刚好容纳和增加一个消息/指令后超限两组；超限时只发一次独立请求，System 为 summaryInstruction、Tools 为空、输入被限制，原消息不变。计入 overhead 和指令，避免只按 B 算预算。
- `TestSummarizeGatewayOverflowFallback`：表驱动 context overflow 和 413；共享失败后仅回退一次，第二次仍失败时返回错误及累计 Usage。
- `TestSummarizeDoesNotFallbackOnUnrelatedError`：401、429、529、普通 400、取消、空响应和空摘要不进入替代请求循环。

调整现有裁剪测试，让其明确覆盖回退而非共享路径：TestCompactionTruncatesLargeToolResult、TestCompactionPreservesSummaryWhenDroppingOldMessages、TestCompactionPrependsUserBeforeAssistant。原 TestCompactionAppendsInstructionToUser 改为共享路径新增独立 user 的断言；回退的旧合并行为可留为子测试。TestCompactionBeforeRequest 的大输入仍可覆盖独立裁剪，但不要将它作为共享成功的证明。

验收：可共享时不改一条既有输入；超限请求必有有界回退；失败不改变可见历史。

### 步骤 3：阻断工具执行，完整累计回退费用

`internal/agent/compact.go:547` — `	summary := strings.TrimSpace(messageText(response.Message))`

`internal/agent/agent.go:198` — `	addCompactionUsage := func(result *compactionResult) {`

具体改法：两条摘要路径都在抽文本前检查 ToolCalls；共享响应有调用则回退，独立回退有调用则失败。两次响应 Usage 在 summarize 中相加，所有返回路径都保留已产生用量。不要改变默认 tool_choice，也不修改 gate、工具调度或 UI。

新增 `TestSummarizeToolCallsNeverExecute`：共享响应同时含文本及工具调用，独立响应正常；工具 Run 计数为零，只写独立响应生成的一个 compaction entry。独立响应仍含工具调用时，不能写入 entry，也不能发压缩完成事件。

新增 `TestSummarizeFallbackUsageAccumulates`：共享 overflow 且非空 response 带用量，独立响应另带用量；成功、独立失败、工具调用被拒三组断言四分量总数。扩展 TestCompactionUsageCountsTowardTurn，确认 InputTokens 包含 read/write、CachedTokens 只含 read，失败共享也收费。

补充序列化测试 `TestOpenAISummaryPrefixMatchesMainRequest`：比较 requestBody 生成的 system、tools 及消息前缀，包含最后原消息为 user 和工具结果的两种情况。补充 `TestAnthropicSummaryPreservesCacheConfiguration`：thinking/effort 与 tools 不变，既有摘要断点有效、断点总数不超过四、签名保留；比较内容前缀时单独剥离会移动的 cache_control 标记，不把 provider 的 user 合并当成 llm.Message 下标。保留 TestOpenAIToolChoice 的默认 auto / 无工具省略断言。

验收：摘要不能触发文件修改或其他工具；正常 agent 仍可使用工具；两次摘要费用没有漏记或重复计入。

### 步骤 4：锁定重放契约与现有兜底

`internal/agent/compact.go:148` — `	result.Entry = compactionEntry{Type: compactionEntryType, Summary: summary, FromSeq: items[start].seq, ToSeq: items[end-1].seq}`

`internal/agent/compact.go:467` — `	result.Entry = compactionEntry{Type: compactionEntryType, Summary: summary, KeepFrom: cut}`

`internal/agent/compact.go:190` — `func replayEntries(entries []json.RawMessage) ([]replayItem, []foldedMessage) {`

具体改法：不修改 compactionEntry、historyFromEntries、replayEntries、稳定 seq、session Append 或 recall。共享指令仅存在于临时请求，JSONL 仍只追加原有 compaction entry。保持 Store.Append 成功后才更新 messages 和发 EventContextCompacted。

`internal/agent/compact_test.go:193` — `		if strings.Contains(messageText(msg), "first summary") {`

修改 TestRunTwoBlockCompactions：“旧摘要不能出现在整个摘要请求”改为“旧摘要可出现在 P，但本次目标起点必须在旧摘要之后”。保留两次正常主请求 start 之前前缀相等、断点、reload、recall 和块稳定序号断言。

新增 `TestReplayCompactionCacheSharingCompatibility`，用固定原始 JSONL 字符串覆盖：旧 KeepFrom、多次旧压缩、旧 Rewrite 文件以 KeepFrom=1 起始、块与旧压缩混用、损坏块、缺失工具结果修复。硬编码期望 visible messages 和 folded seq，不能以另一次调用同一 replay 函数作为唯一预期。分别验证共享摘要成功和独立回退生成的原字段 entry 重载结果一致，JSONL 原字节是新文件前缀、临时摘要指令未被持久化。

保留并运行：TestCompactionReplayRepairsBeforeKeepFrom、TestReplayBlocks、TestReplayLegacyThenBlock、TestReplayInvalidBlocks、TestRecallStableSequence、TestRecallSequenceAcrossBlocks、TestRecallAvailability、TestAppendLoad、TestAppendAfterPartialLine；同时保留 TestOverflowCompactsAndRetries、TestOverflowTwiceReturnsError、TestAutoCompactionFailureStopsRetrying、TestBlockWithoutSavingsCountsUsage、TestBlockWithoutSavingsStopsRetrying、TestBlockAppendFailureDoesNotChangeMessages、TestReloadUsesSummary、TestMultipleCompactionsKeepLatest。摘要请求识别按第 2 节统一修改。

验收：相同旧 JSONL 得到逐字段相同的历史与 recall 索引；整段压缩、overflow 一次重试、自动压缩失败继续、无节省停止重试、落盘失败不改内存全部通过。

### 步骤 5：限定范围验证与分阶段上线

`internal/agent/compact_test.go:167` — `func TestRunTwoBlockCompactions(t *testing.T) {`

`internal/agent/llm/openai_test.go:375` — `func TestOpenAIToolChoice(t *testing.T) {`

`internal/agent/llm/anthropic_test.go:163` — `func TestAnthropicStableCompactionBreakpoints(t *testing.T) {`

实现后离线运行下列定向测试；设进程和 go test 双重超时，禁用网络依赖解析、禁用 cgo，不启动 mcode 本体。命令是待执行验收步骤，本次未运行：

```sh
perl -e 'alarm 60; exec @ARGV' env CGO_ENABLED=0 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test -timeout 45s ./internal/agent -run '^(Test(Summarize.*|Compaction.*|Run.*Compaction.*|Block.*|Replay.*|Recall.*|AutoCompaction.*|Overflow.*|NoStoreSkipsGrowthCompaction|ReloadUsesSummary|MultipleCompactionsKeepLatest|TextAndToolConversation|ResumeAndPlan|MaxTokensFromOptions|Usage.*))$'
perl -e 'alarm 60; exec @ARGV' env CGO_ENABLED=0 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test -timeout 45s ./internal/agent/llm -run '^(Test(OpenAI(SummaryPrefixMatchesMainRequest|ToolChoice|CompactionBreakpoints)|Anthropic(SummaryPreservesCacheConfiguration|StableCompactionBreakpoints|Request|ThinkingRoundTrip|AdaptiveThinking|AdaptiveEffortByModel)|DefaultAnthropicMaxTokens|IsContextOverflow.*))$'
perl -e 'alarm 60; exec @ARGV' env CGO_ENABLED=0 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test -timeout 45s ./internal/agent/session -run '^Test(AppendLoad|AppendAfterPartialLine|LoadPartialLastLine|LoadSkipsInvalidLines|ConcurrentAppend)$'
```

若离线依赖不存在，记录阻塞，不下载。先针对已在使用的 terra / deepseek OpenAI 兼容路由观察，记录实际上线时间与模型、网关版本。先验证构造和费用，再扩展流量。若加权缓存率提高而每回合未命中输入或计价等效输入变差，不算验收通过。回退只需要恢复摘要请求构造代码；本方案未写入新 entry 格式，不要求会话迁移。

## 6. 上线后只读 SQL 验证口径

`internal/store/store.go:2347` — `	INSERT INTO turn_usage (thread_id, turn_index, input_tokens, output_tokens, cached_tokens, context_tokens, context_window, cost_usd, requests_json, updated_at)`

`internal/remote/memory_backend.go:58` — `const contextCompactedKind = "context_compacted"`

`internal/remote/memory_backend.go:1701` — `		turnIndex = td.messages[len(td.messages)-1].TurnIndex`

用 messages.kind=context_compacted 标识“成功产生压缩事件的回合”，按 thread_id + turn_index 关联 turn_usage；用 EXISTS 避免一轮多个压缩事件导致用量重复累计。包含未压缩回合作为对照。provider 过滤严格使用任务给定的两个值。

口径：

- 输入总量 = sum(input_tokens)，其中已经包含缓存读和写。
- 加权缓存率 = sum(cached_tokens) / sum(input_tokens)，不取每轮百分比的算术平均。
- 未命中输入 = input_tokens - cached_tokens；其中仍含 cache write，不能称为 provider 的纯 uncached input。
- 10% 缓存读假设下的输入原价等价 token = input_tokens - 0.9*cached_tokens。它不包含输出价格，也没有单独处理 Anthropic 缓存写溢价，不等于真实美元账单。
- context_tokens 是回合末的上下文状态，不是回合累计输入，不可当作 input_tokens 的分母。
- updated_at 是用量记录最后更新时刻，可能晚于回合开始；切换附近跨界回合需另行排除。保留小时级趋势和相同线程/模型样本，不能把模型、输出量、工具数量或触发修复带来的差异全归因于本修复。

以下可直接执行的查询默认比较 P1-B 上线前后一小时，以复核时间口径。实际评估修复时，把 periods 中四个带时区时间改成修复上线前、后相等长度且避开切换边界的区间；不要伪造尚未发生的修复上线时刻。使用 julianday 统一解析 RFC3339 时间。

本次数据库只读检查工具调用被拒绝，未读取生产 schema 或执行此 SQL；字段与关联方式均来自源码。SQL 的实库执行仍是待验收项。

```sh
perl -e 'alarm 30; exec @ARGV' sqlite3 -readonly "$HOME/.mcode/mcode.db" <<'SQL'
.timeout 5000
.headers on
.mode column
PRAGMA query_only = ON;
WITH
periods(period, start_at, end_at) AS (
  VALUES
    ('before', '2026-11-10T14:29:25+08:00', '2026-11-10T15:29:25+08:00'),
    ('after',  '2026-11-10T15:29:25+08:00', '2026-11-10T16:29:25+08:00')
),
samples AS (
  SELECT p.period, t.provider, u.thread_id, u.turn_index,
         u.input_tokens, u.cached_tokens, u.output_tokens,
         u.context_tokens, u.context_window,
         CASE WHEN EXISTS (
           SELECT 1 FROM messages m
           WHERE m.thread_id = u.thread_id
             AND m.turn_index = u.turn_index
             AND m.kind = 'context_compacted'
         ) THEN 1 ELSE 0 END AS compacted,
         CASE WHEN EXISTS (
           SELECT 1 FROM messages m
           WHERE m.thread_id = u.thread_id
             AND m.turn_index < u.turn_index
             AND m.kind = 'context_compacted'
         ) THEN 'later' ELSE 'first_or_none' END AS compaction_stage
  FROM turn_usage u
  JOIN threads t ON t.id = u.thread_id
  JOIN periods p ON julianday(u.updated_at) >= julianday(p.start_at)
                AND julianday(u.updated_at) < julianday(p.end_at)
  WHERE t.provider IN ('mcode', 'custom-432db46a')
    AND u.input_tokens > 0
)
SELECT period, provider, compacted, compaction_stage, context_window,
       COUNT(*) AS turns, COUNT(DISTINCT thread_id) AS threads,
       SUM(input_tokens) AS input_total,
       SUM(cached_tokens) AS cached_total,
       ROUND(100.0 * SUM(cached_tokens) / SUM(input_tokens), 2) AS cache_pct,
       SUM(input_tokens - cached_tokens) AS noncached_total,
       ROUND(AVG(input_tokens - cached_tokens), 0) AS noncached_per_turn,
       ROUND(AVG(input_tokens - 0.9 * cached_tokens), 0) AS input_equiv_per_turn,
       ROUND(AVG(output_tokens), 0) AS output_per_turn,
       ROUND(AVG(context_tokens), 0) AS final_context_per_turn,
       SUM(CASE WHEN cached_tokens < 0 OR cached_tokens > input_tokens
                THEN 1 ELSE 0 END) AS invalid_usage_rows
FROM samples
GROUP BY period, provider, compacted, compaction_stage, context_window
ORDER BY provider, compacted DESC, compaction_stage, context_window, period;
SQL
```

再按小时观察压缩回合的加权缓存率、未命中输入和计价等效输入，防止两个任意时间段产生误判：

```sh
perl -e 'alarm 30; exec @ARGV' sqlite3 -readonly "$HOME/.mcode/mcode.db" <<'SQL'
.timeout 5000
.headers on
.mode column
PRAGMA query_only = ON;
SELECT strftime('%Y-%m-%d %H:00', u.updated_at, '+8 hours') AS hour_utc8,
       t.provider, u.context_window,
       COUNT(*) AS compacted_turns,
       ROUND(100.0 * SUM(u.cached_tokens) / SUM(u.input_tokens), 2) AS cache_pct,
       ROUND(AVG(u.input_tokens - u.cached_tokens), 0) AS noncached_per_turn,
       ROUND(AVG(u.input_tokens - 0.9*u.cached_tokens), 0) AS input_equiv_per_turn,
       ROUND(AVG(u.output_tokens), 0) AS output_per_turn
FROM turn_usage u
JOIN threads t ON t.id = u.thread_id
WHERE t.provider IN ('mcode', 'custom-432db46a')
  AND julianday(u.updated_at) >= julianday('2026-11-10T12:00:00+08:00')
  AND julianday(u.updated_at) < julianday('2026-11-11T00:00:00+08:00')
  AND u.input_tokens > 0
  AND EXISTS (
    SELECT 1 FROM messages m
    WHERE m.thread_id = u.thread_id
      AND m.turn_index = u.turn_index
      AND m.kind = 'context_compacted'
  )
GROUP BY hour_utc8, t.provider, u.context_window
ORDER BY hour_utc8, t.provider, u.context_window;
SQL
```

WHERE 的日期同样需要随修复上线窗口更新；查询没有修改数据库。provider 不是模型标识，这两条查询也不会自动只筛出 terra。对比 terra / deepseek 时必须在外层增加已核实的线程 ID 筛选，不能根据 provider 名臆断模型。

成功事件口径不覆盖“摘要请求失败”或“摘要成功但块无节省”的回合；它们仍可能收费。上线验收还需结合定向测试和已有日志统计 fallback、空摘要及无节省频率，并检查所有回合总体的等效输入是否上升。不要把 turn_usage 汇总解释成逐请求缓存收益，也不要假设 requests_json 已含每个摘要请求的独立记录。

建议验收标准：同一路由、相近工作量、相同窗口、已有 recall 的后续压缩回合，加权缓存率回升，同时 noncached_per_turn 和 input_equiv_per_turn 降低；非压缩回合无明显退化。至少保留两段有足够样本的稳定时间窗，列出样本数和输出量；没有样本时标为待验证，不给出“节省一半”承诺。

## 7. 风险与未完成项

最大风险是网关缓存实现与模型指令遵循：相同 System/Tools 和消息前缀不保证缓存命中；长 P 的缓存读成本、频繁工具调用回退和冷缓存可能抵消收益。Anthropic 改 tool_choice 会明确失效消息缓存，故本方案不采用强制 none 的共享请求。

模型按消息计数选段也有摘要越界或重复的风险；代码控制持久覆盖范围，真实摘要质量仍需抽样核对。共享路径保留主 System 后，强系统指令可能干扰摘要；受限回退负责可用性，不能把它包装成共享成功。

本次仅完成源码、调用方、相关历史及官方缓存规则的定位与设计。未实现、未运行测试、未访问生产数据库、未发布；SQL 实库校验、真实网关缓存收益和摘要质量验证均未完成。不引入依赖、不引入 cgo、不改 UI、不改 JSONL 协议、不改变 recall 和尾部保留策略。
