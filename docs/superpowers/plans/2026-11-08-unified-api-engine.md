# 统一 API 引擎 + Magpie 深度集成

**目标：** 删掉「内置 mcode 供应商」这一层。所有 API Key 来源都由同一个引擎 harness 运行，kind 即来源；设置页只剩「智能体」「API Key」两组，Magpie 保留独立页。

```
config + secrets → internal/apisource（预设 / 自定义端点 / Magpie / Anthropic 只在这里解析成 Source）
                 → internal/app/engine.go（每个来源一个 providermcode.Harness，Kind 即来源）
                 → internal/providers/mcode（gate、JSONL 会话、压缩、steer、bash、todo、MCP 网关）
                 → internal/agent/llm（openai chat completions / anthropic messages）
                 → harness 事件 → internal/remote（目录、刷新、handoff、额度）→ internal/ui（一个列表）
```

## 现状与差距

| # | 差距 | 证据 |
| --- | --- | --- |
| 1 | 引擎的 Kind 写死，缺少 remote 依赖的可选接口 | `internal/providers/mcode/mcode.go:85` `func (*Harness) Kind() harness.Kind { return harness.Kind("mcode") }`；remote 侧靠鸭子类型取 `DisplayName`/`ModelIDs`/`Available`/`RefreshModels`，见 `internal/remote/memory_backend.go:8525` `if dn, ok := h.(interface{ DisplayName() string }); ok {` |
| 2 | 引擎端点解析只认 Magpie 与 `anthropic/` 前缀 | `internal/app/mcode_agent.go:119` `if target, modelID, ok := magpieinject.ProviderOf(model, targets); ok {`；`internal/app/mcode_agent.go:132` `baseURL = "https://api.anthropic.com"` |
| 3 | 请求构造与旧循环不一致 | `internal/agent/llm/openai.go:200` `httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(encoded))`；无 `tool_choice`；`internal/agent/llm/openai.go:190` `body["reasoning_effort"] = effort` |
| 4 | 老会话不落盘，切换后静默失忆 | `internal/agent/agent.go:120` `if opts.Store != nil && opts.SessionID != "" && opts.Store.Exists(id) {`；会话文件由 `internal/agent/session/session.go:114` `func (s *Store) Exists(id string) bool {` 判定 |
| 5 | 目录与请求用的端点集合不同 | `internal/app/mcode_agent.go:109` `targets := mcodeTargets(cfg, secrets)` 与 `internal/app/mcode_agent.go:243` `targets := piMagpieAllTargets(cfg, secrets)` |
| 6 | Magpie 价格、档位、额度未被利用 | `internal/providers/magpie/magpie.go:177` `// Price 只在网关被要求报价格时出现（X-Magpie-Prices 头）。`；`internal/app/client.go:4996` `return nil, fmt.Errorf("ProviderQuota: %w", mcodeui.ErrNotImplemented)` |
| 7 | 设置页把 CLI 行与 API 行混在一张卡片，mcode 有多处特判 | `internal/ui/settings_page_providers_agents.go:1751` `entries := append(cliProviderListEntries(st, lib), apiProviderListEntries(st)...)`；`internal/ui/settings_page_providers_agents.go:558` `if id == "mcode" {` |
| 8 | 默认来源只由 SetHarness 决定 | `internal/remote/memory_backend.go:1919` `if b.defaultProvider == "" {` |

## 阶段 1：引擎补齐（不动来源，可单独发布）

**用户可见的变化**

- 思考档位按模型真实支持情况发送：`deepseek-reasoner` 不再带 `reasoning_effort`，`xhigh`/`max` 不再被压成 `high`。
- 工具模式请求恒带 `tool_choice: "auto"`，修复部分网关不执行工具。
- 升级后第一次继续旧引擎会话时看到一次交接（历史进入提示词），而不是直接失忆。

**兼容策略**

- `kind` 仍是 `mcode`，会话目录仍是 `dataDir/mcode/sessions`，配置与 A 循环一行不动。
- 所有新增 Option 为空时行为与现在完全一致；`ConfirmsApprovalDelivery` 默认对齐 A 的 `true`。
- A 包保留到阶段 6，阶段 1 只补引擎能力。

**验收标准**

- `go test ./internal/agent/llm/... ./internal/providers/mcode/... ./internal/remote/...` 通过。
- 手测：端点填 `http://127.0.0.1:3425/v1/chat/completions` 时只拼一次路径；带工具的请求体含 `tool_choice:"auto"`；`deepseek-reasoner` 请求体无 `reasoning_effort`。
- 把某线程的 session 指向不存在的 JSONL，下一次发送的 prompt 含历史交接段（`internal/remote/runtime_prompts.go:436` `func buildProviderHandoffPrompt(history []wire.ThreadMessage, prompt string) string {`）。

**任务 1.1 引擎的 provider 外壳**

允许改动：`internal/providers/mcode/mcode.go`、`internal/providers/mcode/shell.go`（新增）、`internal/providers/mcode/shell_test.go`（新增）。

1. `internal/providers/mcode/mcode.go:21` `type Options struct {`：新增 `Kind harness.Kind`、`DisplayName string`、`ModelIDs func() []string`、`RefreshModels func(context.Context) error`、`Available func() (bool, string)`、`ConfirmsApprovalDelivery bool`。
2. `internal/providers/mcode/mcode.go:85` `func (*Harness) Kind() harness.Kind { return harness.Kind("mcode") }`：改为返回 `h.kind`，`internal/providers/mcode/mcode.go:80` `func New(opts Options) *Harness {` 里空值时填 `mcode`。
3. 新增 `internal/providers/mcode/shell.go`：`DisplayName`、`ModelIDs`、`RefreshModels`、`Available`、`ConfirmsApprovalDelivery`、`PreserveSessionsFrom`、`SessionExists`，全部按 Option 是否有值降级（无值时不暴露能力，remote 的鸭子类型自然跳过）。
4. `SessionExists` 先用 `StoreDir` 下会话文件名判断；命名与 `internal/agent/session/session.go:114` `func (s *Store) Exists(id string) bool {` 对齐。

测试（新增 `internal/providers/mcode/shell_test.go`）：空 Options 时 Kind 为 `mcode`、DisplayName 非空、`ConfirmsApprovalDelivery` 为 true、ModelIDs 为空、RefreshModels 空实现不报错、Available 默认 `(true, "")`；带 Kind 的 Options 返回对应 Kind。

**任务 1.2 请求构造对齐**

允许改动：`internal/agent/llm/openai.go`、`internal/agent/llm/types.go`、`internal/agent/llm/openai_test.go`。

1. `internal/agent/llm/openai.go:200` `httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(encoded))`：先判断 base 是否已以 `/chat/completions` 结尾，已结尾则直接使用（对齐 `internal/providers/openai/openai.go:394` `if strings.HasSuffix(base, "/chat/completions") {`）。
2. `internal/agent/llm/openai.go:161` `body := map[string]any{"model": req.Model, "messages": messages, "stream": true, "stream_options": map[string]bool{"include_usage": true}}`：`len(req.Tools) > 0` 时补 `body["tool_choice"] = "auto"`。
3. `internal/agent/llm/openai.go:18` `type OpenAIConfig struct {`：新增 `Effort func(model, effort string) (string, bool)`；`internal/agent/llm/openai.go:190` `body["reasoning_effort"] = effort` 改为先问钩子，钩子为 nil 时退回现有映射，并内置一条门控：模型名含 `deepseek-reasoner` 时不发。
4. `internal/agent/llm/types.go:50` `MaxTokens int` 已可用，本任务只补测试，不改语义。

测试（更新 `internal/agent/llm/openai_test.go`）：base 含 `/chat/completions` 时只请求一次该路径；有工具带 `tool_choice`，无工具不带；`deepseek-reasoner` 无 `reasoning_effort`；钩子返回 false 时不发。

**任务 1.3 会话缺失时交接**

依赖：任务 1.1（`SessionExists` 实现）。本节已在当前实现中落地。

允许改动：`internal/harness/session.go`（新增）、`internal/remote/memory_backend.go`、`internal/remote/session_handoff_test.go`（新增）。

1. 新增 `internal/harness/session.go`：定义 `SessionChecker`（`SessionExists(sessionID string) bool`），与 `internal/harness/harness.go:204` `type Harness interface {` 分开，保持可选接口风格（同类参考 `internal/harness/harness.go:225` `type Steerer interface {`）。
2. 判断放在每轮开始读取 `needsHandoff` 的地方，而不是切换 provider 的分支：`internal/remote/memory_backend.go:6505` `needsHandoff := td.needsHandoff` 之后补一条，当前 harness 实现 `SessionChecker` 且 `!SessionExists(sessionID)` 且会话 ID 非空时置 `needsHandoff`；只影响本轮，不写回线程状态。切换 provider 的分支 `internal/remote/memory_backend.go:4494` `td.needsHandoff = true` 保持原样。
3. 会话是否存在由 `internal/agent/session/session.go:114` `func (s *Store) Exists(id string) bool {` 判定；任务 1.1 的 `SessionExists` 用 `StoreDir` 构造 `session.Store` 调它。
4. 复用现有交接分支 `internal/remote/memory_backend.go:6649` `} else if !rotated && !compactOnly && !rotationInfo.Manual && needsHandoff && len(priorHistory) > 0 {`，不新增拼接逻辑。

测试（新增 `internal/remote/session_handoff_test.go`）：假 harness 的 `SessionExists` 返回 false 且线程有历史时，首轮 prompt 含 `buildProviderHandoffPrompt` 的标记；返回 true 时 prompt 不变；不实现接口的 harness（A）完全不受影响。

## 阶段 2：按来源实例化引擎，替换 A 的接线

**用户可见的变化**

- `deepseek`、`zai`、`meta`、`openai`、自定义端点、`magpie`/`magpie2..4` 全部用上引擎：bash、压缩、steer、`ask_user_question`、`todo_write`、外接 MCP、JSONL 会话。
- 模型选择器不再出现 `mcode` 供应商；每个来源有自己的条目与模型。
- 预设来源只需填 Key，模型即可选。

**兼容策略**

- 所有来源的 kind 保持旧值（`internal/config/config.go:1512` `func CustomEndpointProviderKind(ep CustomEndpointConfig) string {` 的返回值不变），`threads.provider`（`internal/store/store.go:58` `provider TEXT NOT NULL,`）无需迁移。
- `mcode` 仍注册为别名，老线程照常解析；它不出现在任何列表里。
- A 包保留到阶段 6，阶段 2 只解除接线，回滚只需回退本阶段。

**验收标准**

- 三个预设（`deepseek`、`zai`、`meta`）、一个自定义端点、一个 Magpie 端点各建线程，工具调用、压缩、重启续聊（JSONL 命中）都正常。
- `ProviderModels("deepseek")` 返回预设模型，`ProviderModels("mcode")` 回退别名目录。
- 老 `mcode` 线程、老 `deepseek` 线程继续时各触发一次交接，不丢上下文。
- `go test ./internal/apisource/... ./internal/providers/mcode/... ./internal/app/... ./internal/remote/...` 通过。

**任务 2.1 来源描述层 `internal/apisource`**

允许改动：`internal/apisource/source.go`（新增）、`internal/apisource/presets.go`（新增）、`internal/apisource/magpie.go`（新增）、`internal/apisource/source_test.go`（新增）、`internal/apisource/magpie_test.go`（新增）。

1. `internal/apisource/source.go`：定义 `Source{Kind, Label, Protocol, BaseURL, APIKey, Headers, DefaultModel, Models []string, InputKinds, MaxTokens, Efforts func(model string) []string}` 与 `Resolve(cfg config.Config, secrets config.SecretStore) []Source`。
2. `internal/apisource/presets.go`：把 `internal/providers/openai/presets.go:13` `type Preset struct {` 的元数据（ID、名称、默认模型、模型表、注册链接、Coding Plan 标记）搬过来，并把 `internal/providers/openai/presets.go:130` `func (p Preset) ResolveBaseURL(override string, codingPlan bool) string {` 的解析逻辑原样搬入；A 的 presets 只留阶段 2 还在用的字段。
3. `internal/apisource/magpie.go`：把 `internal/app/mcode_agent.go:98` `func mcodeTargets(cfg config.Config, secrets config.SecretStore) []magpieinject.Target {` 的解析搬过来，并让目录与请求共用同一份 `[]Source`；阶段 2 直接取全部 Magpie 端点，阶段 3 再补缓存预热与网关档位/窗口。
4. 凭据读取：按 `internal/apisource/source.go` 内的统一函数实现「账号 → 环境变量」两段回退，语义对齐 `internal/providers/openai/openai.go:375` 的 `getAPIKey`；Anthropic 先读 `anthropic.apiKey`，未命中回退 `mcode.anthropicApiKey`（`internal/app/mcode_agent.go:29` `const mcodeAnthropicAPIKeyAccount = "mcode.anthropicApiKey"`）。
5. 自定义端点：直接消费 `internal/config/config.go:1512` 生成的 kind 与端点配置，不另造 ID 规则。

测试（新增 `internal/apisource/source_test.go`、`internal/apisource/magpie_test.go`）：表驱动覆盖四种来源与 Key 回退；断言 `Resolve` 对同一配置返回稳定的端点集合，目录与请求都从这一个集合取。

**任务 2.2 引擎按来源构建**

依赖：任务 2.1。

允许改动：`internal/providers/mcode/source.go`（新增）、`internal/providers/mcode/source_test.go`（新增）。

1. `internal/providers/mcode/source.go`：`NewForSource(src apisource.Source, opts Options) *Harness`；`Protocol` 为 Anthropic 时用 `llm.NewAnthropic`，否则 `llm.NewOpenAI`；把 `internal/app/mcode_agent.go:143` `func mcodeMagpieConfig(target magpieinject.Target, affinityKey, modelID string) llm.OpenAIConfig {` 的头（`internal/app/mcode_agent.go:149` `"x-client-request-id": affinityKey,`）与 `EchoReasoning` 逻辑搬过来。
2. Kind、DisplayName、ModelIDs、Available、ConfirmsApprovalDelivery 全部来自 `src`，配合任务 1.1 的外壳。
3. `SessionExists` 用 `Options.StoreDir` + `internal/agent/session/session.go:114` `func (s *Store) Exists(id string) bool {` 的文件命名判断；会话目录仍由 `internal/app/mcode_agent.go:190` `storeDir = filepath.Join(dataDir, "mcode", "sessions")` 提供，文件名前缀用 kind 避免来源间串会话。
4. `Effort` 钩子接任务 1.2 的 `OpenAIConfig.Effort`：阶段 2 用来源静态档位，阶段 3 换成网关 Levels。

测试（新增 `internal/providers/mcode/source_test.go`）：两种协议各发一次请求到 `httptest` 服务，断言路径、鉴权头、`tool_choice`、`max_tokens`；Anthropic 走 `/v1/messages`，OpenAI 走 `/chat/completions`；`SessionExists` 只认本 kind 前缀。

**任务 2.3 app 接线并下线 A**

依赖：任务 2.1、任务 2.2；可与任务 2.4 并行，但发布时必须同批。

允许改动：`internal/app/engine.go`（新增）、`internal/app/mcode_agent.go`、`internal/app/app.go`、`internal/app/custom_endpoints.go`、`cmd/mcode-server/main.go`。

1. 新增 `internal/app/engine.go`：`buildAPIHarnesses(cfg, secrets, dataDir, mcp) map[string]harness.Harness`，遍历 `apisource.Resolve` 逐个 `providermcode.NewForSource`；`mcode` 别名只在启动时查到 `threads.provider = "mcode"` 的记录才注册。
2. `internal/app/app.go:1902` `return openai.New(opts), true`、`internal/app/app.go:1958` `backend.RegisterHarness(openai.New(openai.Options{`、`internal/app/custom_endpoints.go:51` `return openai.New(openai.Options{`、`cmd/mcode-server/main.go:326` `backend.RegisterHarness(openai.New(openai.Options{` 四处改为引擎构造。
3. `internal/app/mcode_agent.go:98` 的 `mcodeTargets` 与 `internal/app/mcode_agent.go:243` `targets := piMagpieAllTargets(cfg, secrets)` 删除，改调 `internal/apisource`；`internal/app/mcode_agent.go:187` `func buildMcodeHarness(cfg config.Config, secrets config.SecretStore, dataDir string, mcpInjection providerMCP) harness.Harness {` 收缩为只建别名。
4. `internal/app/app.go:1974` `backend.RegisterHarness(buildMcodeHarness(cfg, secrets, dataDir, mcpInjection))` 换成本任务的循环；`internal/app/custom_endpoints.go:279` `c.backend.RegisterHarness(newCustomEndpointHarness(epCfg, c.secrets, c.mcp))` 保存端点后重建对应来源实例。
5. `cmd/mcode-server/main.go:326` 的分支保持「只有 claude/codex 模式才注册」，新增按 `settings.Provider` 注册对应 API 来源；会话目录在 server 里显式落到 `filepath.Join(dataDir, "mcode", "sessions")`（server 没有 dataDir 时用现有配置目录推导）。

测试（新增 `internal/app/engine_test.go`，更新 `internal/app/custom_endpoints_test.go`）：预设、自定义、Magpie、Anthropic 各生成正确 kind 与 BaseURL；保存自定义端点后新 harness 立即生效；`mcode` 别名不进入 provider 列表。

**任务 2.4 目录、刷新、选择器一致**

依赖：任务 2.1。

允许改动：`internal/remote/api_catalog.go`（新增）、`internal/remote/mcode_catalog.go`（删除）、`internal/remote/models.go`、`internal/remote/refresh_models.go`、`internal/ui/model_picker.go`。

1. 新增 `internal/remote/api_catalog.go`：`SetAPICatalog(fn func(kind string) []wire.ProviderModel)`，并保留 `internal/remote/mcode_catalog.go:15` `func SetMcodeModels(fn func() []wire.ProviderModel) {` 为薄封装（阶段 6 删）。
2. `internal/remote/models.go:605` `case "mcode":` 改为查 API 目录；`internal/remote/models.go:436` `if provider != "claude" && provider != "codex" && provider != "pi" {` 的特判改为「有 API 目录的来源」。
3. `internal/remote/refresh_models.go:43` `case "mcode":` 改为遍历 API kind，通用刷新已有 `internal/remote/refresh_models.go:102` `if r, ok := h.(interface{ RefreshModels(context.Context) error }); ok {`。
4. `internal/ui/model_picker.go:650` `case "commandcode", "cline", "mcode", "pi", "omp", "copilot", "droid":` 与后端 `internal/remote/models.go:426` `func cliCatalogProvider(provider string) bool {` 对齐，改成同一份判定，删掉 UI 侧的 `mcode` 与自定义前缀硬编码。

测试（更新 `internal/remote/models_test.go`、`internal/remote/refresh_models_test.go`、`internal/ui/model_picker_test.go`）：`deepseek`/`custom-<id>`/`magpie` 出现在 `ProviderModels` 与选择器；`mcode` 只有别名目录；刷新覆盖每个 API kind。

## 阶段 3：Magpie 深度集成

**用户可见的变化**

- 引擎直接使用各 Magpie 端点的全部模型：连上网关后目录在后台预热，不再借用 pi 的勾选。
- 档位来自网关给出的 Levels，上下文窗口来自网关元数据；不再固定五档。
- Claude 模型走网关的 `/v1/messages`，缓存、思考块、工具回放与 CLI 一致。
- 用量面板显示本轮费用；额度与「在用账号」对引擎会话可用。

**兼容策略**

- 不新增引擎专用的勾选：不把引擎加入 `MagpieAgents` 白名单，也不在勾选矩阵中加列；pi 的勾选保持原样，引擎不读它。
- 没有 Magpie 网关时，预设与自定义来源的行为不变；价格缺失时费用行隐藏（`internal/ui/composer_usage_panel.go:787` 已是该行为）。
- 引擎请求只在来源声明了 Anthropic 协议时改路径，其余仍是 chat completions。

**验收标准**

- 启用一个 Magpie 端点后，它的全部模型出现在选择器里，无需重启，也无需先在 pi 里勾选。
- 档位顺序与网关 Levels 一致；上下文窗口取网关缓存里的值。
- 网关返回 `magpie_price` 时，一轮对话后用量面板出现四位小数费用；不返回时无费用行。
- Claude 模型请求命中 `/v1/messages`；DeepSeek 模型仍命中 `/chat/completions`。
- `ProviderQuota("api:magpie")` 返回窗口，面板显示「在用账号」。
- `go test ./internal/apisource/... ./internal/providers/magpie/... ./internal/providers/mcode/... ./internal/app/...` 通过。

**任务 3.1 引擎的 Magpie 模型目录**

允许改动：`internal/app/mcode_agent.go`、`internal/apisource/magpie.go`、`internal/app/mcode_agent_test.go`（新增或更新）、`internal/apisource/magpie_test.go`（新增）。

1. 目录取各 Magpie 端点的全部模型：`internal/app/mcode_agent.go:224` `func mcodeModelCatalog() []wire.ProviderModel {` 与 `internal/app/mcode_agent.go:243` `targets := piMagpieAllTargets(cfg, secrets)` 不再读 pi 勾选，改为遍历全部端点。
2. 模型缓存加后台预热：复用 `internal/app/agent_magpie.go:126` `func warmMagpieModels(target magpieinject.Target) {`，连接或启动后异步拉一次 `FetchModels`，目录先返回缓存，预热完成后刷新。
3. 档位取网关 Levels，上下文窗口取网关元数据（`internal/providers/magpie/magpie.go:330` `func CachedModel(baseURL, apiKey, id string) (Model, bool) {` 的 `ContextWindow`），目录行不再写死五档（`internal/app/mcode_agent.go:264` `Efforts:   []string{"low", "medium", "high", "xhigh", "max"},`）。
4. 这部分已在 `internal/app/mcode_agent.go` 先行落地；后续把这段逻辑迁到 `internal/apisource` 时保持「全部端点、缓存预热、网关 Levels、网关窗口」这一语义。

测试（新增或更新 `internal/app/mcode_agent_test.go`、`internal/apisource/magpie_test.go`）：两个端点共 5 个模型时目录有 5 行；预热后目录补齐新模型；档位顺序与 Levels 一致；窗口取缓存值。

**任务 3.2 协议路由与价格请求**

允许改动：`internal/apisource/magpie.go`、`internal/providers/magpie/magpie.go`、`internal/providers/magpie/magpie_test.go`、`internal/agent/llm/anthropic.go`、`internal/agent/llm/anthropic_test.go`。

1. `internal/apisource/magpie.go`：端点集合取全部 Magpie 端点，与目录同源；模型 ID 属于 Claude 家族时 `Protocol` 置为 Anthropic，BaseURL 用 `internal/magpieinject/magpieinject.go:116` `// AnthropicBaseURL 把端点的 /v1 地址换成 Claude Code 需要的根地址：` 的换算。
2. `internal/providers/magpie/magpie.go:269` `func FetchModels(ctx context.Context, baseURL, apiKey string) ([]Model, error) {`：请求带 `X-Magpie-Prices`，值取网关文档列出的价格字段集；返回的 `magpie_price` 落到 `internal/providers/magpie/magpie.go:192` `m.Cost = &Cost{Input: row.Price.Input, Output: row.Price.Output, CacheRead: row.Price.CacheRead, CacheWrite: row.Price.CacheWrite}`。字段名与头格式落地前对网关实现核对。
3. `internal/agent/llm/anthropic.go:14` `type AnthropicConfig struct {`：新增 `AuthHeader string`（空 = `x-api-key`，`bearer` = `Authorization: Bearer`）；`internal/agent/llm/anthropic.go:168` `httpReq.Header.Set("x-api-key", c.cfg.APIKey)` 按其分支，网关按 `ANTHROPIC_AUTH_TOKEN` 的语义走 Bearer（参考 `internal/providers/claude/magpie.go:33` `"ANTHROPIC_AUTH_TOKEN=" + apiKey,`）。
4. 保留 x-api-key 路径给官方 Anthropic，默认值不变。

测试（更新 `internal/providers/magpie/magpie_test.go`、`internal/agent/llm/anthropic_test.go`）：`FetchModels` 带价格头且解析 Cost；两种鉴权头各一次；Claude 家族模型解析为 Anthropic 协议与根地址。

**任务 3.3 价格、档位、限额落到引擎**

允许改动：`internal/apisource/pricing.go`（新增）、`internal/providers/mcode/source.go`、`internal/providers/mcode/source_test.go`。

1. 新增 `internal/apisource/pricing.go`：`func CostUSD(cost *magpie.Cost, in, out, cacheRead, cacheWrite int64) float64`，按四项乘价；未报价或 nil 返回 0。
2. `internal/providers/mcode/source.go`：每轮 usage 事件填 `internal/harness/event.go:65` `CostUSD float64`，落库链路已通（`internal/app/convert.go:389` `CostUSD:       u.CostUSD,`）。
3. 档位：`internal/providers/mcode/source.go` 的 `Efforts` 用 `internal/providers/magpie/magpie.go:87` `// Levels 是网关给出的该模型档位名，保持网关顺序、统一小写；没有档位时为 nil。` 的 Levels（与任务 3.1 的目录同源），不再写死五档。
4. 请求约束：`internal/providers/magpie/magpie.go:82` `MaxTokens     int` 作为 `llm.Request.MaxTokens`（`internal/agent/llm/types.go:50`）；`internal/providers/magpie/magpie.go:80` `Input         []string` 不含 `image` 时剥离图片块。

测试（更新 `internal/providers/mcode/source_test.go`）：四项价格求和；无价时 0；档位顺序与网关一致；MaxTokens 出现在请求体；不支持图片的模型请求里没有图片块。

**任务 3.4 额度与在用账号**

允许改动：`internal/app/api_quota.go`（新增）、`internal/app/client.go`、`internal/app/quota_refresh.go`、`internal/ui/composer_usage_panel.go`。

1. 新增 `internal/app/api_quota.go`：`ProviderQuota("api:<kind>")` 找到该来源的 Magpie 端点后复用 `internal/app/pi_magpie.go:428` `func (c *Client) piMagpieQuota(ctx context.Context, providerID string) ([]mcodeui.ProviderQuotaWindow, error) {`（内部读额度的位置 `internal/app/pi_magpie.go:380` `windows, err := magpie.ReadQuotas(quotaCtx, target.baseURL, target.apiKey)`）。
2. `internal/app/client.go:4931` `func (c *Client) ProviderQuota(ctx context.Context, provider string) ([]mcodeui.ProviderQuotaWindow, error) {` 加 `api:` 分支，引擎来源不再落到 `internal/app/client.go:4996` `return nil, fmt.Errorf("ProviderQuota: %w", mcodeui.ErrNotImplemented)`。
3. `internal/app/quota_refresh.go:50` `case strings.HasPrefix(provider, "pi:"):` 旁加 `api:` 前缀，刷新与线程用量一致。
4. `internal/ui/composer_usage_panel.go:175` `return "pi:" + head` 所在函数（`internal/ui/composer_usage_panel.go:168` `func threadQuotaProvider(th *Thread) string {`）对 API 来源返回 `api:<kind>`；「在用账号」复用 `internal/app/quota_tracker.go:251` `func (t *sessionAccountTracker) noteSource(baseURL, apiKey string) {` 与 `internal/app/quota_tracker.go:346` 的路由。

测试（新增 `internal/app/api_quota_test.go`，更新 `internal/app/quota_refresh_test.go`）：`api:magpie` 返回窗口、未知来源仍 ErrNotImplemented、刷新覆盖 api 前缀、面板 provider 键正确。

## 阶段 4：bee 默认跑内置引擎

**目标**

swarm 的 bee 就是给内置引擎用的，和 pi 一样。bee 是 remote 里的普通线程，provider 来自档位或 lead 的 `SwarmBeesProvider`（`internal/remote/kanban_driver.go:43` `provider = derefString(lead.SwarmBeesProvider)`），swarm 里没有针对 pi 的特殊分支；本阶段把 bee 的默认档位与执行路径落到引擎上。

**用户可见的变化**

- 不装任何 CLI、只配 API Key 也能建 swarm：bee 的档位直接选引擎来源与 Magpie 模型。
- bee 与 lead 用同一份模型目录、同一套工具与权限，运行方式对用户不再区分「内置」与「外部程序」。

**兼容策略**

- 已显式配置了 pi 或其他 CLI 档位的老用户不受影响：档位里写了 provider 就照原样走，只有未配置时才落到引擎默认值。
- `internal/swarm/profile.go:84` `Provider     string` 与 `Model` 的默认值改为引擎来源 + Magpie 模型；显式配置优先。

**验收标准**

- 只有 API Key、没有任何 CLI 时，新建 swarm 能跑完一轮并把结果写回看板。
- 老配置（档位显式写 `pi`）行为与现在一致。
- bee 内 panic 不会拖垮 app；停止 bee 后没有遗留 bash 子进程。

**任务 4.1 bee 运行时缺口调研**

允许改动：无（只出结论）。

具体任务待 bee 运行时缺口调研完成后补充。已知必查项：

1. 进程内 panic 隔离：`Run` 需要 recover，避免一个 bee 拖垮整个 app。
2. 停止与进程根：取消 bee 时清理 bash 子进程（进程组）。
3. 只读档位的权限与无人值守审批：`internal/harness/permission.go:137` `if mode == ModeReadOnly && !readOnlyTool {` 的拒绝路径不能卡在等审批。
4. 多个 bee 并发安全：共享 harness 是否可重入，必要时每个 bee 独立实例。
5. 档位的 provider 与模型默认值：`internal/swarm/profile.go:84` `Provider     string` 默认指向哪个来源、`Model` 默认指向哪个 Magpie 模型。

## 阶段 5：设置页两栏化与默认来源

**用户可见的变化**

- 「模型与供应商」只剩两组：智能体、API Key；`mcode` 行消失。
- Magpie 仍是独立页：地址、Key、模型矩阵都在原处，API Key 组里没有 Magpie 行。
- OpenAI 有了自己的设置行；填完 Key 模型直接出现在选择器里。
- 没有装任何 CLI、只配 API Key 时，新会话默认用最近使用的 API 来源；没有记录时按固定顺序兜底：Magpie 端点、anthropic、openai、deepseek、zai、meta、其余自定义端点（与 `internal/apisource/source.go:112` `func Resolve(cfg config.Config, secrets config.SecretStore) []Source {` 的顺序一致）。

**兼容策略**

- 已有配置不改；老线程的 provider 仍是具体 kind，别名 `mcode` 仍注册。
- Magpie 独立页的入口与状态不动，本阶段只把 CLI 行与 API 行拆成两组。
- 自定义端点仍是 API 组第二段，kind 与存储格式不变。

**验收标准**

- 列表先显示已配置、再显示未配置；两组内各自排序；`mcode` 任何位置都不出现。
- 点 API 行只显示 Key、BaseURL（可空）、模型；点智能体行仍显示原有 tab。
- 添加入口只有「智能体」「API Key」两类；自定义端点从 API Key 里进入；Magpie 仍从独立页进入。
- 无 CLI 且只配一个 API 来源时，侧栏新建会话默认选中它；有记录时选最近用过的来源。
- `go test ./internal/ui/... ./internal/app/... ./internal/remote/...` 通过。

**任务 5.1 列表与图标**

允许改动：`internal/ui/settings_page_providers_agents.go`、`internal/ui/settings_page_models.go`、`internal/ui/icons.go`。

1. `internal/ui/settings_page_providers_agents.go:81` `var cliProviderIDs = []string{"copilot", "mcode", "pi", "omp", "commandcode", "droid", "cline", "cursor", "opencode", "grok", "devin"}` 删除 `mcode`；对应删除 `internal/ui/settings_page_providers_agents.go:410`、`internal/ui/settings_page_providers_agents.go:558`、`internal/ui/settings_page_providers_agents.go:888`、`internal/ui/settings_page_providers_agents.go:971`、`internal/ui/settings_page_providers_agents.go:1405`、`internal/ui/settings_page_providers_agents.go:1847`、`internal/ui/settings_page_providers_agents.go:1935` 的 `mcode` 特判。
2. `internal/ui/settings_page_providers_agents.go:1743` `func renderProvidersCard(c *ui.Context, st *SettingsState, lib *modelsLibraryView) {` 分成两段渲染：智能体取 `cliProviderListEntries`，API Key 取 `apiProviderListEntries`（`internal/ui/settings_providers.go:913` `func apiProviderListEntries(st *SettingsState) []providerListEntry {`）再加预设。
3. `internal/ui/settings_page_providers_agents.go:333` `func providerRowName(st *SettingsState, id string) string {` 对 API 来源返回来源显示名（Anthropic、DeepSeek、Z.ai、Meta、OpenAI、Magpie）。
4. `internal/ui/icons.go:915` `func providerIconKey(id string) string {` 增 `anthropic`/`magpie` 的键；颜色表已有 deepseek/zai/meta，补两个即可。
5. `internal/ui/settings_page_models.go:567` `renderProvidersCard(c, st, modelsPageLibrary(c, st))` 保留唯一入口，页面标题与分组不变。

测试（更新 `internal/ui` 布局测试）：`mcode` 不在任何可见行；两组顺序稳定；行数 = 智能体数 + API 来源数。

**任务 5.2 添加入口与详情 sheet**

依赖：任务 5.1（复用 provider 列表结构）。

允许改动：`internal/ui/settings_providers.go`、`internal/ui/provider_sheet_layout.go`、`internal/ui/settings_page_magpie.go`。

1. `internal/ui/settings_providers.go:1233` `func renderProviderAddSheetBody(c *ui.Context, st *SettingsState, cliCandidates []providerListEntry) {` 只留两类：智能体、API Key；自定义端点移入 API Key 段的「自定义」子分组；Magpie 连接仍从独立页进入。
2. `internal/ui/provider_sheet_layout.go:20` `func providerSheetSections(st *SettingsState, kind, id string, lib *modelsLibraryView) []providerSheetSection {`：API 类 kind（preset、custom、anthropic）走同一套「Key / 地址 / 模型」sheet；CLI 分支保持原样。
3. `internal/ui/settings_page_magpie.go:25` `var pageMagpie = settingsPage{` 保持独立页与矩阵入口，不并入 API Key 组。

测试（更新 `internal/ui` sheet 测试）：API sheet 字段齐备且 BaseURL 可空；添加自定义端点仍写回同一存储；Magpie 独立页仍能连接与勾选。

**任务 5.3 后端设置与默认来源**

允许改动：`internal/app/provider_settings.go`、`internal/remote/memory_backend.go`、`internal/ui/sidebar.go`。

1. `internal/app/provider_settings.go:30` `{id: "mcode", name: "mcode", kind: "cli"},` 删除；`internal/app/provider_settings.go:69` `if id == "mcode" {` 的账号分支改为按来源；补 `openai` 的 spec（默认模型）。
2. `internal/remote/memory_backend.go:1919` `if b.defaultProvider == "" {` 附近改成显式策略：CLI 仍按 claude → codex 兜底；API 来源取最近使用的（读 `threads.provider`），没有记录时按 `internal/apisource/source.go:112` `func Resolve(cfg config.Config, secrets config.SecretStore) []Source {` 的返回顺序取第一个可用来源。
3. `internal/ui/sidebar.go:244` `if s.newThreadProvider == "" {` 的回退跳过 `mcode` 别名，只从可见来源里挑。

测试（更新 `internal/app/provider_settings_test.go`、新增 `internal/remote/default_provider_test.go`、`internal/ui/sidebar_test.go`）：无 CLI 时默认命中最近使用的 API 来源；没有记录时按 `Resolve` 顺序；只有别名可解析时不会选中它。

## 阶段 6：删掉旧循环与收尾

**用户可见的变化**

- 工具名统一为 `read`/`write`/`edit`，自动模式不再因为旧名字反复弹审批，图标一致。
- 标题、提交信息等一次性生成自动挑最便宜的可用模型；上游 429/5xx 自动切同家族备选。

**兼容策略**

- 历史消息里旧的 `read_file` 等名字仍按映射渲染，不迁移存储。
- 外部 MCP 工具名不变（`mcode_mcp_list`/`mcode_mcp_call`），审批白名单按 mcp 前缀一次性兼容。
- A 包在本阶段删除后才算完成退役；阶段 1–5 任何一步都可单独回滚。

**验收标准**

- `go build ./...` 与 `go test ./...` 通过，`internal/providers/openai` 目录不存在。
- 旧 `deepseek` 线程继续可用（kind 未变），会话照常续。
- auto 模式下引擎的 `write`/`edit` 不再走 Ask；`read` 不弹审批。
- 标题生成选到目录里最便宜的可用模型；主模型 429 时切到同家族备选并提示一次。

**任务 6.1 删除 A 与 textgen 退场**

依赖：阶段 2–5 已发布。

允许改动：`internal/providers/openai/openai.go`、`internal/providers/openai/presets.go`、`internal/providers/openai/tools.go`、`internal/app/app.go`、`internal/app/custom_endpoints.go`、`internal/app/textgen_dispatch.go`、`internal/app/textgen_mcode_test.go`。

1. 删除 `internal/providers/openai` 整个包（含 `internal/providers/openai/tools.go:40` `Name: "read_file",`）。
2. `internal/app/app.go` 与 `internal/app/custom_endpoints.go` 清理残留 import 与 A 分支（`internal/app/app.go:1902` 在阶段 2 已移除，这里只收尾）。
3. `internal/app/textgen_dispatch.go:288` `if lower == config.ProviderMcode {` 删除，改走阶段 2 保留的引擎生成器（`internal/app/mcode_agent.go:76` `func registerMcodeTextGenerator(cfg config.Config, secrets config.SecretStore) {`）。

测试：删除 `internal/app/textgen_mcode_test.go` 里依赖 A 的断言，改测引擎生成器；`go build ./...` 必须无未使用 import。

**任务 6.2 工具名与审批白名单**

允许改动：`internal/harness/permission.go`、`internal/agent/gate/gate.go`、`internal/ui/timeline_rows.go`、`internal/harness/permission_test.go`。

1. `internal/harness/permission.go:196` `if readOnlyTool || toolName == "write_file" || toolName == "edit_file" ||`：加入 `write`/`edit`，保留旧名兼容历史。
2. `internal/agent/gate/gate.go:101` `func IsLookOnly(name string) bool {` 与 `internal/agent/gate/gate.go:110` `func Check(in Input) Verdict {`：只读集合补 `list_files`/`search_text`，危险集合已认 `write`/`edit`，补测试锁定。
3. `internal/ui/timeline_rows.go:4939` `"read":              true,` 已覆盖新名字；为历史消息加 `read_file` → `read` 的映射，保证图标一致。

测试（更新 `internal/harness/permission_test.go`）：auto 模式下 `write`/`edit` 不产生审批请求；`read`/`list_files` 为只读；旧名仍按原行为处理。

**任务 6.3 一次性生成与同家族备选**

允许改动：`internal/app/textgen_wiring.go`、`internal/app/fallback.go`（新增）。

1. `internal/app/textgen_wiring.go:91` `// shouldAutoGenerateTitle 判断当前标题是否仍可被 AI 生成的标题覆盖（纯函数）。`：选模型时按目录价格挑最便宜的、满足 `internal/textgen/capabilities.go` 能力的来源。
2. 新增 `internal/app/fallback.go`：主模型返回 429/5xx 时，按同家族（`deepseek*` → `deepseek*`，`claude*` → `claude*`）取下一个可用来源，最多一次，事件里提示。

测试（更新 `internal/app/textgen_wiring_test.go`、新增 `internal/app/fallback_test.go`）：模型挑选遵守价格与能力；429 只重试一次且不跨家族。

## 风险

1. **老会话迁移。** `threads.provider` 存具体 kind（`internal/store/store.go:58` `provider TEXT NOT NULL,`），A 的 kind 与引擎新 kind 同名，所以线程能被接管；但 A 从不落盘，历史只能靠交接。若阶段 1.3 未先落地，引擎会在 `internal/agent/agent.go:120` `if opts.Store != nil && opts.SessionID != "" && opts.Store.Exists(id) {` 判定不存在后静默开新上下文。缓解：阶段 1 先上交接，A 保留到阶段 6。
2. **keyring 账号名。** 已决定迁到 `anthropic.apiKey`，旧名只读回退：`internal/apisource/source.go:65` `anthropicAPIKeyAccount       = "anthropic.apiKey"` 与 `internal/apisource/source.go:66` `anthropicLegacyAPIKeyAccount = "mcode.anthropicApiKey"`。旧名在保留期间不要删、不要写回（`internal/app/mcode_agent.go:29` `const mcodeAnthropicAPIKeyAccount = "mcode.anthropicApiKey"`）；现有迁移只做「旧 service → 当前 service、账号名不变」（`internal/config/secrets.go:231` `func MigrateLegacySecrets(refs []SecretRef) []error {`），不包含这次改名。
3. **Magpie 模型目录不再借用 pi 勾选。** 引擎读各端点的全部模型，`MagpieAgents` 与 `PiModels` 的双写保持原样，不新增 `engine` 键。代价是 pi 未勾选的模型也会出现在引擎选择器里，这是有意为之；真正的风险是目录与请求必须来自同一份端点集合（见第 7 条）。
4. **外部 MCP 工具名。** 引擎只有 `mcode_mcp_list`/`mcode_mcp_call` 两个网关工具，A 的内联工具名消失。审批白名单若按工具名保存，需要一次性按 `mcp` 前缀放行或做映射；UI 图标按名映射（`internal/ui/timeline_rows.go:4939` `"read":              true,`），未知名回退默认图标。
5. **`read_file` → `read`。** A 的名字是 `read_file`（`internal/providers/openai/tools.go:40` `Name: "read_file",`）；引擎是 `read`/`write`/`edit`。自动模式的放行名单只认 `*_file`（`internal/harness/permission.go:196` `if readOnlyTool || toolName == "write_file" || toolName == "edit_file" ||`），不修阶段 6.2 就会反复弹审批；历史消息的旧名字也要映射图标。
6. **cmd/mcode-server。** `cmd/mcode-server/main.go:326` 只在 claude/codex 分支里注册 OpenAI，且没有自定义端点与预设；它也没有 `dataDir` 概念，引擎会话落盘位置需要显式确定（见任务 2.3 第 5 步）。改动时不要顺手扩大 server 的能力面。
7. **目录与请求的端点集合必须同源。** 现在是 `internal/app/mcode_agent.go:109` 用全部端点、`internal/app/mcode_agent.go:243` 用 pi 勾选，选择器里能点到的模型请求时可能 404。阶段 3 把目录改成全部端点后两者才同源；迁移到 `internal/apisource` 时保持这一份集合。
8. **网关价格可选。** `X-Magpie-Prices` 只在网关支持时返回（`internal/providers/magpie/magpie.go:177` `// Price 只在网关被要求报价格时出现（X-Magpie-Prices 头）。`），Cost 为空时费用行必须隐藏。
9. **bee 不是子进程。** `internal/swarm/manager.go:3933` `cmd := procattr.HideWindow(exec.CommandContext(ctx, bin, args...))` 只是 `runGitPaths` 里的 git 调用；bee 是 remote 里的普通线程，provider 来自档位或 lead 的 `SwarmBeesProvider`（`internal/remote/kanban_driver.go:43` `provider = derefString(lead.SwarmBeesProvider)`），swarm 中没有针对 pi 的特殊分支。引擎 bee 不需要绕过子进程模型，真正的缺口是 panic 隔离、停止与只读审批（阶段 4）。

## 已决定

1. **老 A 会话继续时怎么处理？** 一次交接提示，把可见历史带进新会话；复用 `internal/remote/runtime_prompts.go:436` `func buildProviderHandoffPrompt(history []wire.ThreadMessage, prompt string) string {` 已有的交接实现，阶段 1 已落地。
2. **Magpie 连接入口是否并入 API Key 组？** 不并入：保留独立的 Magpie 设置页，API Key 组里不放 Magpie 行。
3. **Anthropic Key 是否迁移账号名？** 迁到 `anthropic.apiKey`，旧账号 `mcode.anthropicApiKey` 只读回退；`internal/apisource` 已按此实现。
4. **默认 API 来源怎么选？** 最近使用的 API 来源；没有记录时按固定顺序兜底：Magpie 端点、anthropic、openai、deepseek、zai、meta、其余自定义端点，与 `internal/apisource/source.go:112` `func Resolve(cfg config.Config, secrets config.SecretStore) []Source {` 的顺序一致。
5. **swarm 的 bee 要不要跑引擎？** 要跑。内置引擎本来就要像 pi 一样给 bee 用；新增阶段 4「bee 默认跑内置引擎」，不再另立项目。
