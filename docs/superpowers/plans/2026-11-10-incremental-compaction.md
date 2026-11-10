# P1：增量分块上下文压缩

日期：2026-11-10  
状态：设计完成，未实现，未运行测试。  
代码基线：`8622c7e`。本文中的行号对应该基线。P0 合入后按同一行源码定位，不按旧行号机械修改。

## 1. 推荐结论与边界

实现单层、不可变、按时间递进的摘要块。每次只总结最早一段尚未压缩的原文，并在该段原位置替换成摘要。首条原始 user 消息固定保留，旧摘要块不进入普通摘要请求。块之前的消息内容、角色、工具参数、图片、thinking signature 保持不变。

P1 必须以 P0 为前提：

- `compactNow` 只在 JSONL 末尾 `Append` compaction entry，不再 `Rewrite`。
- 原始消息全部保留。`foldedFromEntries` 和只读 `recall` 已经存在。
- `summaryInstruction` 已经包含 KEEP VERBATIM、DROP、PRIORITY 规则。
- P1 不重新设计 `recall` 的参数、权限、输出限制或搜索算法。只修改它读取的折叠集合及稳定序号映射。

当前工作副本尚无上述 P0 实现。因此本文以现有函数作为锚点，明确列出与 P0 的接缝，不虚构 P0 新增文件或行号。

不引入 billion-context 依赖，不运行其脚本，不增加网络服务、后台压缩、向量索引或配置界面。第一版不做多层合并。紧急情况保留一次整段压缩，但同样保留首条 user、原文和 append-only 日志。

## 2. 已确认的根因与历史依据

### 2.1 当前实现改写历史前缀

`internal/agent/compact.go:49` — `func compactThreshold(window int) int {`

当前自动触发线是 `window - min(16384, window/4)`。它只看绝对上下文大小，不记录压缩后的新增量。

`internal/agent/compact.go:53` — `func keepRecentTokens(window int) int { return min(20000, window/4) }`

保留最近内容已有小窗口适配，可以继续复用。

`internal/agent/compact.go:241` — `	prefix := append([]llm.Message(nil), messages[:cut]...)`

摘要请求包含切点之前的整个前缀，也包含旧摘要。

`internal/agent/compact.go:315` — `	compacted := append([]llm.Message{summaryMessage(summary)}, messages[cut:]...)`

新摘要占据消息列表开头。历史消息的共同前缀随之改变，旧摘要反复进入下一次总结。

`internal/agent/compact.go:88` — `			messages = append([]llm.Message{summaryMessage(entry.Summary)}, messages[entry.KeepFrom:]...)`

重建算法同样只有“替换整个前缀”的语义。`KeepFrom` 是当时已重建视图的下标，不是原文的永久编号。

`internal/agent/agent.go:193` — `			if err := opts.Store.Rewrite(id, entries...); err != nil {`

当前分支还会删除原文。此项由 P0 修复，P1 不重复实现。

`internal/agent/agent.go:228` — `		if autoCompact && estimateContextTokens(messages, contextTokens, contextMeasured) > compactThreshold(window) {`

当前循环没有增长水位，也没有普通压力与紧急压力的区别。

**根因结论：** 持久层只有 `KeepFrom` 前缀替换，内存层也只产出一个头部摘要。仅降低触发阈值或缩短摘要不能保留旧块，也不能保留消息共同前缀。必须同时改变覆盖范围、重放方式与选段方式。

“全部 cache 失效”必须限定为历史消息缓存。System 和 tools 未变化时，已有独立缓存前缀仍可保留；不能声称每次压缩连这一部分也必然失效。

### 2.2 Git 历史约束

- `48f0ded` 修复了摘要请求过大时的裁剪：保留旧摘要、按完整工具组删除消息、在 assistant 开头前补 user。普通分块不再重写旧摘要，但不能丢失其工具配对和不修改原输入的保障。旧摘要保留规则只留在整段回退路径。
- `5ce4f14` 引入 `overhead`、摘要请求末尾指令，以及 `Rewrite` 后 `KeepFrom=1` 的兼容读取分支。P1 保留 overhead 口径和指令，只接在 P0 的 append-only 路径上。
- `3a98bac` 调整了小窗口余量、assistant 切点和缓存用量口径。P1 不改变 `CacheRead` 只计读缓存的统计方式。

以上结论来自有范围的 `git log` / `git show` 与源码阅读，不是运行复现。

### 2.3 外部参考的采用范围

只读参考仓库 billion-context 中的 `prune.ts`、`compress.ts`、`config.ts` 和 `DESIGN.md`：

- 采用范围覆盖、原位插入、首条 user 固定、工具配对边界及成功后重置增长水位。
- 采用约 50K 增长、20K 下限、75% / 95% 压力参考值。
- 不照搬 `system` 摘要角色、ref 映射、多 tier 状态机、pipeline、回收代龄或模型主动调用 compress。
- 外部默认参数不是性能证据。本文参数必须先通过确定性单测，再用真实使用量观察调整。

## 3. 数据结构与 JSONL 契约

### 3.1 原文序号

用 `seq` 表示原始 `message` entry 的稳定序号，**从 1 开始，右端开区间**：

1. 按 JSONL 文件顺序计数。
2. 只计能解码成 `messageEntry` 且 role 为 user / assistant / tool 的 entry。
3. compaction、其他元数据、摘要视图和修补的 tool result 不占序号。
4. 压缩、reload、recall、模型切换都不得重新编号。
5. `Store=nil` 时同样分配内存序号；`appendMessage` 成功后才推进。

不修改已有原文 entry，不要求给全部历史回填 ID。P0 如果已有 recall 序号，必须复用同一分配规则；如果 P0 用“折叠列表下标”，必须将其改为原文序号，不能在每次压缩后重排。展示序号不必连续，但同一原文的序号必须稳定。

用 `source_hash` 防止损坏行导致隐式序号偏移后错误折叠。它是摘要输入范围原对象的 SHA-256；使用标准库，不引入依赖。普通块的输入是 `json.Marshal([]llm.Message)` 对应原文范围，**在 repair、截断和追加摘要指令之前**计算。整段回退的输入则是规范视图中被替换的片段，定义见下文。仅做范围端点检查不足以发现中间少了一条消息。

### 3.2 扩展现有 `compactionEntry`

保留 `type = "compaction"` 和 `summary`。新增字段采用 `omitempty`，旧记录不变。

普通块示例：

```json
{"type":"compaction","version":2,"mode":"block","start_seq":2,"end_seq":19,"source_hash":"<64 位小写十六进制>","summary":"<仅覆盖 seq 2 至 18 的摘要>"}
```

| 字段 | 普通块语义 |
| --- | --- |
| `version` | 固定为 `2`；缺省 / `1` 为旧格式 |
| `mode` | 固定为 `block` |
| `start_seq` | 第一条覆盖原文的序号，包含 |
| `end_seq` | 覆盖原文的结束序号，不包含 |
| `source_hash` | 上述原文范围的确定性摘要值 |
| `summary` | 已校验非空、可节省 token 的摘要正文 |
| `keep_from` | 不写；读取 `block` 时不得使用它 |

不增加 `block_id`、`level`、`active`、时间戳或“未来使用”的字段。单层块用覆盖范围定位，活跃状态由重放推导。普通块不得重叠，不得覆盖首条 user，不得覆盖已经隐藏的原文，也不得跨过已有摘要块。新块范围必须位于上一个普通块之后。

整段回退示例：

```json
{"type":"compaction","version":2,"mode":"full","keep_from":7,"source_hash":"<64 位小写十六进制>","summary":"<当前旧上下文的整段摘要>"}
```

`full` 的 `KeepFrom` 是**该 entry 生效前、已修补且固定首条 user 的规范视图**的切点。替换 `[1, KeepFrom)`，保留 `messages[0]`，结果为 `messages[:1] + summary + messages[KeepFrom:]`。`source_hash` 对替换片段 `[1, KeepFrom)` 计算。它允许消费旧块，不使用 `start_seq` / `end_seq`，也不是普通增量块。

保留 `full` 这个明确分支的原因：紧急回退可能消费旧摘要；旧式 Rewrite 会话的摘要还可能没有对应原文。仅用原文连续范围无法完整表达这两种情况。不要把这类快照伪装成不重叠的普通块。

### 3.3 旧会话兼容

- `version` 缺省 / `1`：按原有 `KeepFrom` 语义重放，包括先 `repairToolPairs`，以及文件以 `KeepFrom=1` 摘要 entry 开头的 Rewrite 特例。不得把旧 `KeepFrom` 当成 `seq`。
- 在第一个 version 2 entry 生效之前，以及重放结束时，规范化旧视图：将文件中仍存在的首条真实 user 原样固定到开头；旧头部摘要冻结在它之后；其余可见消息保持相对顺序，去掉首条 user 的重复副本。
- 初始连续旧 entry 的 `KeepFrom` 必须先按旧算法全部执行，不能每执行一条就插入首条 user，否则第二条旧 entry 的下标会偏移。
- P0 append-only 文件仍有开场原文，可以恢复开场 user。旧 Rewrite 文件已删除的原文无法恢复，只固定文件中第一条仍存在的真实 user；不得生成“恢复成功”的假记录。
- 只有摘要、尚无真实 user 的旧会话：不写新块；追加本轮 user 后将它固定，摘要放到其后。不得把旧摘要当成开场原文。
- 上述旧视图规范化会造成**一次性消息前缀变化**。这是兼容迁移成本，不得计作增量缓存收益。
- 从本版本开始只写 version 2。保留 version 2 reader 时可以关闭增量 writer；不得用“不认识 version 2”的旧二进制作为无损回退方式。
- 空摘要、非法范围、hash 不匹配、未知 version / mode：不执行覆盖、不更新增长水位，记录诊断；保留已接受视图及未覆盖原文。不得退而猜测 `KeepFrom`。

## 4. 重建算法与 recall

### 4.1 共用一次重放

在 `internal/agent/compact.go` 内增加小型内部 replay 结果，不单独造存储框架。`historyFromEntries` 仍返回 `[]llm.Message`，但它与 P0 的 `foldedFromEntries` 都调用同一重放器。

重放结果最少包含：

- 原文表：`seq -> 原始 llm.Message`。
- 规范可见视图及来源：原文 `seq`、摘要标记、摘要覆盖的原文集合。
- 首条 user 的 `seq`。
- 最后一个成功 compaction entry 当时的原文总数 `lastCompactSeq`。
- 活跃摘要的位置，供请求缓存断点使用。

来源信息放在 agent 内部的视图侧表，不靠 `summaryPrefix` 推断覆盖关系，也不写入用户原文。修补消息记录所属 assistant 的 `seq`，但自身不占 seq。

### 4.2 重放顺序

1. 遇到有效原文：分配 seq，保存未经修改的原对象，追加到可见视图。
2. 遇到旧 compaction：严格执行旧读取规则；保存被折叠的原文来源。旧摘要继承它消费的摘要的覆盖集合。
3. 每个 version 2 entry 生效前，先执行带来源的工具修补；遇到首个 version 2 entry 时再完成旧视图规范化。以后维持首条 user 在首位。writer 生成候选前采用相同顺序，确保 full 的下标和 hash 与 reader 一致。
4. 遇到 `block`：验证范围只引用本 entry 之前存在的原文，hash 匹配，范围未折叠，开场 user 不在范围内，左右边界均配对安全。找出该范围在视图中的连续片段，在原位置替换成 `summaryMessage`，记录覆盖来源。
5. 遇到 `full`：验证 `1 < KeepFrom < len(view)`、片段 hash 及配对边界；将 `[1, KeepFrom)` 原位替换为一条摘要。新摘要继承被消费摘要的覆盖集合，并加入本次移除的原文 seq；旧摘要仅从可见视图退出，JSONL 原 entry 不改。
6. 每个有效 compaction 成功后，将当时原文总数记为 `lastCompactSeq`，不是 `end_seq - 1`。因为保留尾部在压缩发生前已经存在，不应再次计入“新增量”。
7. 末尾执行同一套带来源的工具修补，产出规范视图与最终折叠集合。

实现中可以把原有 `repairToolPairs` 的核心循环改为处理内部 item，由公开 helper 解包消息；不要用消息内容相等来反查来源，也不要仅按 tool ID 建全局映射。同一个 tool ID 在不同工具组中可以重复。

### 4.3 `foldedFromEntries` / `recall` 配合

折叠集合是“**由已接受 compaction 覆盖、仍存在于原文表、但不再以原文形式出现在规范视图**”的消息，按 seq 升序输出：

- 不包括摘要、修补结果或首条固定 user。
- 单纯因 repair 丢弃的孤立 tool result 不算压缩覆盖；不能把所有不可见原文都误算成 folded。
- 多次压缩、整段回退后，同一原文只出现一次。
- 旧 Rewrite 文件丢失的原文不在集合中。recall 继续按 P0 的未找到语义返回，不从摘要伪造原文。
- `recall` 结果沿用 P0 的只读工具结果路径进入新消息；不把原文永久展开回原位置，也不修改块。
- recall 新生成的 tool result 是新的原文 entry；以后可以随自己的工具组压缩。查询所引用的旧 seq 不变。
- 本轮内连续两次压缩后，recall 必须能看到第二次的新折叠范围。P0 若缓存集合，成功 append 后必须刷新；不得只在 `Run` 开始加载一次。

普通路径示例，`U1` 为固定开场，`R` 为原文，`B` 为摘要：

```text
U1 | R2 ... R18 | R19 ... R40
U1 | B[2,19)    | R19 ... R40
U1 | B[2,19)    | B[19,31) | R31 ... R55
```

第二次请求与第一次请求在 `U1 | B[2,19)` 上保持相同内容。第二个摘要请求不发送 `B[2,19)`。recall 覆盖 seq 2 至 30。

## 5. 触发策略、切分与摘要请求

### 5.1 两套独立计数

**增长量 `growth`**：累计 `seq > lastCompactSeq` 的原文估算 token。初始水位为 0。它不含 system、tools、摘要和修补消息，reload 后按日志恢复，不在每次 `Run` 重置。普通模型响应的 usage 更新也不得覆盖这个水位。

**总量 `total`**：用最近一次响应 usage 作基线，之后追加消息用已有字符估算；包含 system / tools overhead。没有 usage 基线时使用 `tokensForMessages(view) + overhead`。压缩成功后使用 `After` 重置总量基线。

当前 `estimateContextTokens` 的增量返回不自动加入 overhead。调用方必须只在“没有基线”时补 overhead；有 usage 或 `After` 基线时不得再加一次。

### 5.2 参数和优先级

```text
growthThreshold(window) = min(50000, max(20000, window/2))
minChunkTokens(window)  = min(20000, window/4)
keepRecentTokens       = min(20000, window/4)   // 复用现有函数
normalPressure         = min(75% * window, compactThreshold(window))
emergencyPressure      = min(95% * window, compactThreshold(window))
maxChunkTokens         = 50000
```

取整使用整数除法。`compactThreshold` 保留原含义，作为输入 / 输出余量的安全上限，而非唯一的普通触发线。

- `growth >= growthThreshold`：尝试普通块，不要求先到压力线。
- 或 `total >= normalPressure`：普通压力触发，仍要求候选范围至少 `minChunkTokens`，防止反复压缩小片段。
- `total >= emergencyPressure`：允许不足最小块，必要时走整段回退。
- API 明确返回 context overflow：绕过增长门槛，直接整段回退；仍最多重发主请求一次。
- 每轮最多提交一次 compaction；不写“压缩到压力解除”的无界循环。

128K 窗口的增长门槛是 50K，普通压力是 96K，紧急线受原有 16384 余量限制为 111616，而不是机械等到 121600。8K 窗口增长门槛仍是 20K，但 6K 压力线会先触发，最小块只有 2K。小窗口两条压力线重合是有意的安全退化。

### 5.3 选择范围

将现有 `findCut` 的单个切点改为内部 `findCompactRange` 结果，至少返回视图 `[start, end)` 和对应原文 `[startSeq, endSeq)`：

1. 从固定首条 user 和已冻结摘要之后，选择最早一个连续的、尚未折叠的原文区间。不得跨越旧摘要。
2. 从尾部倒数 `keepRecentTokens(window)`，向更早的合法边界对齐。至少保留最新一条完整消息；最新真实 user 必须保留。不能为了增大压缩段把当前问题压掉。
3. 在长单用户回合内，允许在 assistant 工具组之间切分，不强迫等待下一个 user。
4. 候选块最多约 50K token；到达上限时在最近的完整工具组末尾结束。剩余原文保留，等待下一次增长或压力触发。
5. 普通触发下，尾部不足保留目标或候选不足最小块时返回 nil；不得沿用旧“随便找最近切点”逻辑形成极小摘要。紧急 / overflow 路径可以缩短尾部，但不得移除最新 user 或拆开它后面的工具组。
6. 如果唯一超大内容是固定开场 user，或最新 user 本身，压缩其他小消息不能解决问题。无有效节省时保持原文，由已有 context-exhausted 路径返回，不循环请求摘要。

来源修补导致原文 seq 不连续时，普通块不得跨过缺口宣称覆盖一段完整区间。先使用最早的完整连续区间；没有可用区间且已到紧急压力时走 full。普通块的“不可覆盖已隐藏原文”包含这类异常，不能靠缺失来源的假 seq 填洞。

### 5.4 工具组边界

一组是 assistant 的全部 `ToolCalls`、它自己的 reasoning / ThinkingBlocks，以及随后到下一条 assistant 之前的结果区间。组内有 steer user 时也按整体保护，不能只用“下条消息 role 不是 tool”判断安全。

- 左边界不得落在 tool result 上，也不得位于未完成的调用组内部。
- 右边界必须位于整组之后或下一组之前。
- 并行 N 个调用必须连同 N 个结果一起折叠或保留。
- 取消 / reload 缺失结果时，沿用 repair 生成错误结果；该结果与所属 assistant 一起处理，不参与 seq。
- 孤立结果沿用现有 repair 行为，不为它单独生成摘要块。
- Replay 必须重新验证边界；不能假定 JSONL 里的范围一定由正确版本写出。

### 5.5 摘要请求内容

普通请求只发送当前范围的副本、固定的区间说明和末尾总结指令：

- 不发送任何旧摘要，不发送完整保留尾部，不附带主请求 tools、动态状态或整个 system。
- 不为了给摘要模型交代任务而再次附带固定首条 user。它已经保留在主请求里；否则一个超长开场会让每个块的摘要请求都溢出。
- 保留 P0 的 KEEP VERBATIM / DROP / PRIORITY 规则。将总述改为“只总结提供的这段历史”；未在此段出现的目标和决定不得推测补写。
- 起始是 assistant 时，补一条摘要专用 user 说明。末尾复用现有“最后是 user 则追加 Part，否则追加 user”方式，必须复制 Content 后再改。
- 摘要输出上限设置为 `max(1, min(4096, window/8))`。不要让 Anthropic 摘要请求沿用 32000 输出默认值。
- 摘要输入预算扣除 `summaryInstruction`、区间说明、末尾指令、上述输出上限和 1024 安全余量。主请求 overhead 不能原样当成摘要请求 overhead。
- 可保留工具长文本的首尾裁剪，但只改请求副本；原文与 `source_hash` 不受影响。
- 普通块预算不足时，从右侧退到更早的完整组边界，并同步缩小覆盖范围；不得丢弃整条输入后仍宣称它被当前摘要覆盖。单个超大组仍装不下时，不写普通块，交给紧急整段路径。

摘要成功条件：响应非 nil，正文非空，`StopReason` 不是 `length`，且新总量小于旧总量。节省量至少为 `min(1024, max(1, 被替换片段估算 token/4))`。否则不 append、不修改视图、不推进水位、不发成功事件。

整段回退允许旧摘要进入摘要请求，也保留现有超长输入裁剪策略；这是明确的有损降级，P0 的原文与 recall 仍可补查。普通分块不得借用这条路径偷偷重写旧块。

## 6. 摘要角色、位置与 prompt cache

### 6.1 角色

`internal/agent/compact.go:175` — `	return llm.Message{Role: "user", Content: []llm.Part{{Type: "text", Text: summaryPrefix + "\n\n" + summary}}}`

继续使用 user 摘要，保留 `summaryPrefix`，以免破坏现有识别逻辑。它是历史资料，不是新的用户指令。不要把摘要挪进动态 system，也不要照搬外部项目的 system 消息角色。

`internal/agent/llm/anthropic.go:157` — `		messages = append(messages, map[string]any{"role": msg.Role, "content": blocks})`

Anthropic 当前会把消息 role 直接写到 wire。中途插入 system 不能直接采用。user 摘要可使用现有适配器，并允许与固定开场 user 相邻。普通摘要在覆盖片段原位置出现，永远不在首条 user 前面，也不在当前问题后面。

`isSummaryMessage` 仍可供 user-turn 判断使用，但覆盖判断和缓存候选必须来自 replay 元数据，不能把用户恰好输入 `summaryPrefix` 的文本当成压缩块。

### 6.2 Anthropic 断点必须小幅调整

`internal/agent/llm/anthropic.go:79` — `		body["system"] = []any{map[string]any{"type": "text", "text": req.System, "cache_control": map[string]string{"type": "ephemeral"}}}`

System 断点原样保留。

`internal/agent/llm/anthropic.go:163` — `				blocks[len(blocks)-1]["cache_control"] = map[string]string{"type": "ephemeral"}`

当前只有最后一条 user 的消息断点。历史工具组很多时，仅保持前缀内容不变不能保证服务端能找到较早缓存断点。推荐增加最多两个明确的稳定前缀断点，而不是仅宣称“改成块就全部命中”。

改法：

1. 在 `llm.Request` 增加 `CacheBreakpoints []int`，使用 `json:"-"`；下标对应 `Request.Messages`。这是请求元数据，不写入原文 Message 或 JSONL。
2. Agent 在最终组装每次主请求时，从 replay 元数据选择最近两个活跃摘要块的末端；不足两个时可用固定开场 user 补一个。对普通请求、steer 后请求、overflow 重发都重算下标。
3. Anthropic 序列化时建立 input message index 到实际 wire block 的映射。在对应消息最后一个可缓存 block 上加 `cache_control`，不要把原始下标直接当 wire message 下标；tool 消息会合并为 user 的 tool_result。
4. 保留现有最后 user 断点，去重。System 1 个、稳定前缀最多 2 个、最后 user 1 个，合计不超过 4 个。
5. 不在 thinking / redacted_thinking 上加标记。由 Agent 只传合法摘要 / 固定 user；序列化端忽略越界、空内容和不合适的候选。无候选时完全保留旧行为。

滚动例子：请求已有 B1 时给 B1 断点；插入 B2 后仍保留 B1，同时给 B2 断点。下一次压缩不改变 B1、B2 的内容。超过两个块后滚动断点，不增加无限 checkpoint 列表。

这里保证的是**模型输入内容**在压缩点之前保持相同，不是整个 HTTP JSON 逐字节相同：`cache_control` 是会移动的协议元数据，当前实现本来也会移动最后 user 断点。单测分别检查内容前缀和断点预算。真实 cache 命中还受最小可缓存长度、TTL、模型、网关与服务端查找策略影响；本次不访问网络，不能宣称命中率。

OpenAI 保留现有自动前缀缓存机制，不增加专用缓存字段。测试它忽略 `Request.CacheBreakpoints`，且消息前缀及 system / tools 内容不变。

## 7. 多层合并：第一版不做

| 方案 | 收益 | 代价 | 结论 |
| --- | --- | --- | --- |
| 单层不可变块 + 紧急整段回退 | 实现最小；平时不重写旧摘要；recall 可补查 | 摘要累计后仍占窗口；极长会话最终降级 | 推荐 |
| 每 N 块自动合并 | 限制摘要数量 | 重写前缀；增加父子覆盖、活跃块、递归召回与持久化恢复测试 | 本次不做 |
| 不压缩，只依赖 recall | 无摘要丢失 | 当前问题缺少必要工作状态，不能替代上下文整理 | 不采用 |

以每块约 50K 输入、最多 4K 摘要估算，摘要总量不会无限免费。监测“冻结摘要 + 固定开场 + system/tools”占比即可发现上限。到达紧急线、且普通块不再能节省空间时，用一次 `full` 快照代替所有旧块，不设计多 tier。以后只有真实会话证明整段回退过于频繁，再单独设计两层合并。

## 8. 分步实施清单

每步必须独立新增 / 修改对应测试。先落读取与纯函数，再接 writer。P0 未合入前不得开始切换 writer。

### 步骤 1：版本化 entry 与共用 replay

**锚点**

`internal/agent/compact.go:29` — `type compactionEntry struct {`

`internal/agent/compact.go:57` — `func historyFromEntries(entries []json.RawMessage) []llm.Message {`

`internal/agent/compact.go:96` — `func repairToolPairs(messages []llm.Message) []llm.Message {`

**具体改法**

- 按第 3 节扩展结构。新增内部原文 seq、来源 item、hash 计算和 replay 结果。
- 统一旧格式、`block`、`full` 的验证与重放；保留公开读取 helper 的现有用途。
- 普通块原位替换；规范化旧会话的固定开场。来源修补与消息修补共用逻辑。
- 暂不切换生产 writer，不修改 `session.Store`。

**测试**

- 保留 `TestCompactionReplayRepairsBeforeKeepFrom` 对旧格式修补后下标的语义；规范输出断言加入固定开场。
- 保留 `TestRepairToolPairs`，补并行调用、组内 steer、重复 call ID、修补结果不占 seq。
- 新增 `TestCompactionReplayBlocksInPlace`、`TestCompactionReplayLegacyThenBlocks`、`TestCompactionReplayRewrittenLegacy`。
- 新增表驱动 `TestCompactionReplayRejectsInvalidBlock`：空摘要、负数 / 空 / 越界范围、覆盖固定 user、重叠、跨块、拆工具组、未来 seq、hash 不匹配、未知版本。
- 新增 `TestCompactionReplayDamagedSourceDoesNotFoldWrongRange`：范围中间原文丢失或损坏时 hash 拦截。

**验收标准**

同一 JSONL 重放两次完全一致。旧 `KeepFrom` 不被误解释为 seq。两个普通块都可见。非法块不吞原文。旧 Rewrite 文件仍可继续对话，但不伪造已丢失原文。

### 步骤 2：让 folded / recall 复用来源和序号

**锚点**

`internal/agent/compact.go:55` — `// historyFromEntries 按文件顺序重建会话消息。`

此处接 P0 新增的 `foldedFromEntries`，以及 P0 recall 实现；P0 未合入，具体新增行号不在本工作副本中。

**具体改法**

- `foldedFromEntries` 从步骤 1 的 replay 结果导出原文，不再单独按 KeepFrom 推算。
- 统一 seq，排除固定 user、摘要、修补项；原文全文不截断。
- recall 只改数据来源与本轮刷新，不改工具权限、参数或输出上限。

**测试**

- 在 `internal/agent/compact_test.go` 新增 `TestFoldedFromEntriesMultipleBlocks`、`TestFoldedFromEntriesAfterFullFallback`、`TestFoldedFromEntriesExcludesPinnedUser`。
- 在 P0 实际测试文件追加：压缩前后同一 seq 取原文一致；第二次块压缩后可立即搜索；消费旧块后无重复；旧 Rewrite 缺失原文返回未找到。

**验收标准**

两块覆盖的所有现存原文均可查，seq 不漂移。一次 full 之后旧原文仍可查。模型请求中不自动重新展开原文。

### 步骤 3：实现增长与压力的纯判断

**锚点**

`internal/agent/compact.go:49` — `func compactThreshold(window int) int {`

`internal/agent/compact.go:141` — `func estimateContextTokens(messages []llm.Message, baselineTokens, baselineMessages int) int {`

**具体改法**

- 保留 `compactThreshold` 安全上限；新增第 5 节的增长、普通压力和紧急压力判断。
- 从 replay 水位与 append 记录累计增长，不用总上下文的前后差值代替增长。
- 为没有 usage 的首次加载补 overhead，有基线后不重复补。

**测试**

- `TestCompactThreshold` 保持原有三个断言，另加 128K 安全上限。
- 新增 `TestCompactionTriggerGrowthAndPressure`：阈值前后、正好等于阈值、128K / 200K / 8K、小窗口压力线重合、冻结摘要不计增长、system/tools 只影响压力。
- 新增 `TestCompactionGrowthSurvivesReload`：不能在每个短 `Run` 中清零。
- 新增 `TestCompactionGrowthCountsOnlyAfterLastEntry`：旧保留尾部不重复计增量，失败 entry 不清水位。

**验收标准**

50K 增长可以在未到窗口压力时触发；不到增长门槛也可由压力触发。8K 窗口不等待 20K 新增量。估算无 overhead 漏算或重复计算。

### 步骤 4：范围规划与工具配对

**锚点**

`internal/agent/compact.go:193` — `func findCut(messages []llm.Message, window int) int {`

`internal/agent/compact.go:216` — `func userCutAtOrBefore(messages []llm.Message, i int) int {`

`internal/agent/compact.go:226` — `func hasSummarizablePrefix(messages []llm.Message, cut int) bool {`

**具体改法**

- 普通模式改用范围规划，旧切点逻辑只作为 full 回退的起点。
- 按第 5 节保护固定 user、旧块、最新问题和最近尾部。
- 两侧按完整工具组对齐，限制单块及摘要输入预算；不靠最后 repair 掩盖错误选段。

**测试**

- 修改 `TestFindCutCanStartAfterInitialUser`：新断言是 start 在固定 user 后，end 在 assistant 组边界；长单回合仍可分块。
- 修改 `TestCompactionKeepsToolPairs`：检查摘要请求与保留视图两边的所有并行结果，不只查一对。
- `TestCompactionSkippedWithoutCut` 保留“只有一条超大开场不压缩”的语义。
- 新增 `TestCompactRangeKeepsPinnedUserAndOldBlocks`、`TestCompactRangeRetainsLatestUser`、`TestCompactRangeCapsChunk`、`TestCompactRangeDoesNotSplitSteeredToolGroup`。
- 新增单个超大工具组和不足最小块的普通 / 紧急分支测试。

**验收标准**

任何合法输出都不存在“call 在块里、result 在尾部”或反向情况。旧块从不成为普通模式输入。范围最多一次、无死循环。

### 步骤 5：普通块摘要与 full 回退候选

**锚点**

`internal/agent/compact.go:235` — `func compactMessages(ctx context.Context, client llm.Client, model string, messages []llm.Message, window, overhead int, emit func(harness.Event)) (*compactionResult, error) {`

`internal/agent/compact.go:303` — `	request := llm.Request{Model: model, System: summaryInstruction, Messages: prefix}`

`internal/agent/compact.go:317` — `	emit(harness.Event{Type: harness.EventContextCompacted, CompactTokensBefore: int64(before), CompactTokensAfter: int64(after)})`

**具体改法**

- 让摘要函数消费明确范围 / mode，返回候选 entry、替换视图和已知 Usage。
- 普通模式只总结所选原文，原位替换；full 模式总结 `[1, cut)`，首条 user 不动。
- 按输入预算及输出上限构造请求，复制后截断，验证空响应、length 和节省量。
- 摘要函数不再提前 emit 成功事件。事件移到持久化和视图提交之后。
- 已返回 Usage 的摘要请求即使因不节省被拒绝，也要计费；如后续 full 又调用一次，两个用量各累计一次，不把最终主请求重复累计。

**测试**

- `TestCompactionTruncatesLargeToolResult` 改为固定开场较短、长工具正文位于候选范围；继续验证裁剪只发生在请求副本。
- `TestCompactionPreservesSummaryWhenDroppingOldMessages` 仅测试 full；新增普通路径测试，断言请求根本不含旧摘要。
- `TestCompactionPrependsUserBeforeAssistant` 与 `TestCompactionAppendsInstructionToUser` 保留请求首尾兼容断言。调整 fixture，不能把固定首条 user 当作可压缩内容。
- 新增 `TestIncrementalCompactionDoesNotRewriteOldBlock`、`TestIncrementalSummaryRequestContainsOnlyRange`、`TestCompactionRejectsEmptyLengthAndNoSavings`。
- 新增响应失败 / 取消时消息、hash、水位不变，以及明确 `MaxTokens` 的测试。

**验收标准**

第二次普通摘要请求没有第一次摘要文本。首条 user 与旧块 marshal 后内容相同。未提交候选不产生成功事件。超长或无节省摘要不会形成压缩循环。

### 步骤 6：接入 Run，保证 append 后提交

**锚点**

`internal/agent/agent.go:83` — `	appendMessage := func(message llm.Message) error {`

`internal/agent/agent.go:155` — `		messages = historyFromEntries(entries)`

`internal/agent/agent.go:181` — `	compactNow := func() (*compactionResult, error) {`

`internal/agent/agent.go:228` — `		if autoCompact && estimateContextTokens(messages, contextTokens, contextMeasured) > compactThreshold(window) {`

`internal/agent/agent.go:236` — `		if _, err := appendSteers(); err != nil {`

`internal/agent/agent.go:266` — `				result, compactErr := compactNow()`

**具体改法**

- `Run` 读取 replay 元数据；append 成功后同步 seq、视图来源与增长。
- 将本轮 `appendSteers` 放在压缩判断之前，使新增插话计入本次压力检查；overflow 前刷新请求视图。
- 按“生成候选 → `Store.Append` → 更新内存与水位 → 刷新 folded 数据 → emit”提交。`Store=nil` 保留同样顺序，省略 I/O。
- 普通失败保留现有 `autoCompact=false` 本轮熔断。API overflow 的一次 full 不受熔断影响。
- 持久化失败不是摘要服务失败：保留旧视图并返回错误，不再尝试另写 full，也不作为成功继续。
- overflow 直接 full，最多重发主请求一次，保留已有流文本 retract 行为。

**测试**

- 修改 `TestCompactionBeforeRequest`：短开场 + 大历史；摘要在开场后而非 index 0；原文仍在 JSONL；检查 version / mode / 覆盖范围，不再要求 `KeepFrom=1`。
- 修改 `TestReloadUsesSummary`：固定开场在 index 0，摘要在对应区间；reload 视图与提交视图一致。
- 将 `TestMultipleCompactionsKeepLatest` 改为普通模式保留两块，fixture 使用足够的新原文而不是巨大旧摘要；另加 full 模式只保留新快照的测试。
- `TestAutoCompactionFailureStopsRetrying` 改用可压缩历史，不用巨大的固定开场；仍只发一次失败摘要请求。
- `TestCompactionUsageCountsTowardTurn` 保持 151 / 12 / 20 的既有计费语义；补两次候选请求与拒绝摘要的用量测试。
- 修改 `TestOverflowCompactsAndRetries`、`TestOverflowTwiceReturnsError`：用确有节省的历史，首条 user 保留，仍为 3 次请求且仅重发一次。
- 修改 `TestDefaultContextWindow`：大内容放在历史而不是唯一开场，继续验证默认 128K 触发。
- 新增 `TestCompactionAppendFailureDoesNotCommit`、`TestCompactionAfterSteerCountsPressure`、`TestCompactionWithoutStorePreservesSequence`。

**验收标准**

两轮压缩后文件包含全部原文和两个块 entry，reload 与内存相同。I/O 失败无成功事件、无水位推进。摘要失败不阻塞正常模型请求；溢出最多重发一次。

### 步骤 7：接入 Anthropic 稳定断点

**锚点**

`internal/agent/llm/types.go:55` — `type Request struct {`

`internal/agent/llm/anthropic.go:99` — `	messages := make([]map[string]any, 0, len(req.Messages))`

`internal/agent/llm/anthropic.go:159` — `	for i := len(messages) - 1; i >= 0; i-- {`

`internal/agent/agent.go:239` — `		request.Messages = append([]llm.Message(nil), messages...)`

**具体改法**

- 按第 6 节增加请求断点元数据、输入到 wire 映射及最多四断点限制。
- 主请求每次组装后设置断点；摘要请求保持简单，不带历史块断点。
- 不修改 OpenAI 序列化格式，不把断点或覆盖元数据写进 system、摘要正文、原文 JSONL。

**测试**

`internal/agent/llm/anthropic_test.go:106` — `func TestAnthropicRequest(t *testing.T) {`

保留原测试的 system 和最后 tool_result 断点断言。新增 `TestAnthropicStableCompactionBreakpoints`，覆盖两块、相邻 user、并行 tool_result 合并、候选越界、与最后 user 重复、system 缺省、thinking 不标记和最多四断点。

`internal/agent/llm/openai_test.go:120` — `func TestOpenAIRequest(t *testing.T) {`

新增 `TestOpenAICompactionBreakpoints`，验证同样请求带 / 不带 `CacheBreakpoints` 时 OpenAI body 完全相同。Agent 侧补 overflow / steer 后断点下标指向正确摘要的断言。

**验收标准**

B2 插入后 B1 的内容和可缓存端点仍存在。Anthropic 没有第五个断点，没有在 reasoning 上标记。OpenAI 请求无新协议字段。

### 步骤 8：端到端回归与关闭增量 writer

**锚点**

`internal/agent/compact_test.go:438` — `func TestMultipleCompactionsKeepLatest(t *testing.T) {`

`internal/agent/agent.go:261` — `			if llm.IsContextOverflow(err) && !overflowRetried {`

**具体改法**

- 增加组合场景：legacy → B1 → reload → B2 → recall → overflow full → reload。
- 增量模式选择集中在 `compactNow` 的一个内部策略分支，临时关闭时只改该分支，仍保留 version 2 reader 与 `full` writer。不加新的用户配置、环境变量或跨层状态。
- 单测直接传入模式覆盖退回整段路径，不通过修改全局环境变量测试。

**测试与验收**

- 新增 `TestCompactionRoundTripLegacyBlocksRecallFull`：每阶段原文可查、固定 user 不变、内存 / reload 一致。
- 新增 `TestCompactionImmutablePrefixAcrossBlocks`：比较两个请求在新块起点之前的 role、Content、ToolCalls、ThinkingBlocks 和 signatures；HTTP 比较时把移动的 `cache_control` 单独断言。
- 新增 `TestCompactionFullModeReadsExistingBlocks`：关闭增量 writer 后仍能读取全部块并生成一个 full entry。
- 新增 `TestCompactionPinnedOrLatestUserCannotBeShrunk`：不产生无效摘要请求循环，原 context overflow 可返回。

执行者在实现完成后运行以下有范围的检查；本设计任务不运行：

```sh
go test ./internal/agent/ -run 'Test(Compact|Compaction|Incremental|FindCut|Folded|RepairToolPairs|AutoCompaction|ReloadUsesSummary|MultipleCompactions|Overflow|DefaultContextWindow)' -timeout 120s
go test ./internal/agent/llm/ -run 'Test(AnthropicRequest|AnthropicStableCompactionBreakpoints|OpenAIRequest|OpenAICompactionBreakpoints)' -timeout 120s
```

调用上述命令的工具也必须设置不超过约 130 秒的外层超时。recall 新测试命令在 P0 合入后按其实际包路径补齐，禁止猜测路径。不得跑全量测试或启动 mcode 本体。

## 9. 风险、失败与回退规则

| 条件 | 行为 | 不得执行 |
| --- | --- | --- |
| 普通块摘要 API 失败、空响应、length | 原视图继续，本轮关闭普通自动压缩；已知 Usage 照计 | 不在同一不可用 API 上无界重试 |
| 普通候选无范围 / 无节省，尚未紧急 | 跳过，等待增长或压力变化 | 不写空块，不推进水位 |
| 紧急压力下普通选段装不下 / 无法节省 | 最多尝试一次 full | 不对失败候选先 append 再撤回 |
| 主请求 context overflow | 直接 full，成功后仅重发一次主请求 | 不重复普通块 → full → 再 full |
| 摘要服务 401 / 403 / 429、网络失败、context 已取消 | 不为同一个错误立即多发 full；保留既有失败处理 / 取消 | 不把取消当作压缩成功 |
| full 仍无范围 / 无节省 / 请求失败 | 保持最后有效视图；已有 overflow 按原错误返回 | 不丢首条 / 最新 user 来强行“成功” |
| Store.Append 失败 | 不提交视图，返回持久化错误 | 不 Rewrite，不尝试另写 full |
| range / hash / tool 边界损坏 | 忽略该覆盖并记录诊断；不更改已接受块 | 不悄悄隐藏别的原文 |

其他明确限制：

- 首条 user 可能很长。固定它是缓存与任务语义的取舍，不保证任意超大开场都能继续。测试必须覆盖不可压缩的情况。
- 旧 Rewrite 删除的原文不可恢复；P1 不解决历史数据丢失。
- 块摘要仍是有损文本。P0 recall 是恢复细节的路径，不保证模型主动使用它。
- 摘要数增加会占用窗口；第一版用紧急 full 兜底，不宣称无限上下文。
- 普通块 hash 防止重放错误折叠，不替代会话写入并发控制。沿用同一 SessionID 单个 `Run` writer 的现有假设，不扩展成多写者事务系统。
- 保留 version 2 reader 才能安全停用增量 writer。直接降级旧二进制会忽略普通块或误读 full 的 `KeepFrom`，不是支持的回退路径。
- 缓存收益尚未运行验证。确定性验收先证明不重写消息前缀及正确 checkpoint；真实 `CacheRead` 和成本改善由主代理后续在允许联网的环境观察。

## 10. 本次交付与未做

仅新增本方案文档。未修改任何 Go 文件、JSONL、测试或依赖。只读检查了指定 agent / llm 实现、相关测试、会话加载语义、有关 Git 提交，以及指定外部参考源码。未运行构建、测试、mcode 或外部脚本，未访问网络，未启动后台任务。

文档静态检查通过：37 个源码锚点的路径、单行行号和引用原文逐项匹配。此检查只验证方案引用，不验证算法或缓存效果。

执行前唯一接缝是 P0 的实际 `foldedFromEntries` / recall 文件及序号表示；按步骤 2 对接，不重复实现 P0。所有缓存收益和实现正确性仍须由上述测试及后续真实使用量验证。
