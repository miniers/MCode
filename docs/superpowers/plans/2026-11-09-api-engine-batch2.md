# 统一 API 引擎：批次 2 实际进度与剩余计划

本文件替换原计划的任务 2.2、2.3、2.4、3.1。依据当前工作副本静态阅读编写；本次只新增本文档，没有修改 Go 文件，没有运行构建、测试、lint 或其他验证。下列测试均为后续实施要求，未执行。

批次采用最小延续方案：保留已经工作的 app 引擎包装与旧 OpenAI 元数据能力，不要求补建原计划的 `NewForSource`。按 kind 提供模型目录，Magpie 全量模型归各端点自己的 kind，mcode 只保留旧线程兼容入口。API 来源的 kind 与网关注入模型的前缀不是同一个标识。

## 任务 2.2：补齐来源引擎的目录接入与新会话命名

### 现状

- 桌面端运行时注册的是 `*internal/providers/mcode.Harness`。旧 `openai.Harness` 仅提供名称、模型 ID、可用性和刷新；不会执行它的 `Run`。引擎构造已在 app 层完成，原计划的 `internal/providers/mcode/source.go` / `NewForSource` 不存在，不应把缺少该文件判为尚未接线。

  internal/app/engine.go:67 `	return providermcode.New(opts)`

- deepseek、zai、meta：启动时 `registerExtraProviders` → `buildExtraHarnesses` → `buildProviderHarness`，以各自预设 kind 注册内置引擎；预设保存后也调用同一构造函数。

  internal/app/app.go:1870 `	return newSourceEngineHarness(openai.New(opts), cfg, secrets, mcodeRuntimeDataDir(), mcpInjection), true`

  internal/app/provider_settings.go:284 `		} else if h, ok := buildProviderHarness(cfg, c.secrets, spec.id, c.mcp); ok {`

- openai：桌面启动直接包装旧 OpenAI 元数据实例，实际执行内置引擎。保存 OpenAI 配置后的重建路径需要任务 2.3 修复，不能据启动接线推定热更新已覆盖。

  internal/app/app.go:1936 `		backend.RegisterHarness(newSourceEngineHarness(legacy, cfg, secrets, dataDir, mcpInjection))`

- anthropic：不经过 OpenAI 包装，独立构造内置引擎；已启用且有新账号、旧账号或环境变量凭据时注册，执行 `llm.NewAnthropic`。注释称“没有开关”，实际仍受 `Anthropic.IsEnabled()` 约束。

  internal/app/app.go:1947 `		backend.RegisterHarness(newAnthropicEngineHarness(cfg, secrets, dataDir, mcpInjection))`

  internal/app/engine_anthropic.go:64 `	client := llm.NewAnthropic(llm.AnthropicConfig{BaseURL: source.BaseURL, APIKey: source.APIKey(store)})`

  internal/apisource/source.go:219 `	if !cfg.Providers.Anthropic.IsEnabled() {`

- custom-*、magpie、magpie2、magpie3、magpie4：都从启用的自定义端点构建同一种内置引擎包装；Magpie 标记决定 Claude 模型走 Messages、其余走 Chat Completions。旧槽位在桌面启动迁移为同 ID 的自定义端点，因此保留 magpie* kind；普通新增 Magpie 端点仍是 custom-<id>。保存端点已重建对应 kind。

  internal/app/custom_endpoints.go:64 `	return newSourceEngineHarness(legacy, fallback, secrets, dataDir, mcpInjection)`

  internal/app/custom_endpoints.go:283 `			c.backend.RegisterHarness(newCustomEndpointHarness(epCfg, c.secrets, c.dataDir, c.mcp))`

  internal/config/config.go:1439 `			return ep.ID`

  internal/config/config.go:1442 `	return CustomEndpointKind(ep.ID)`

  internal/app/mcode_agent.go:209 `	if isClaudeModel(modelID) {`

- mcode：仍按自身 Enabled 开关注册内置引擎；不是“仅旧线程存在时注册”的纯别名策略。它解析网关注入前缀或 anthropic/ 前缀。

  internal/app/app.go:1942 `	if cfg.Providers.Mcode.Enabled {`

- 独立 server 例外：只有 claude/codex 模式会额外注册旧 `openai.Harness`，仍执行旧 OpenAI 循环。没有注册 deepseek/zai/meta/anthropic/custom-*/magpie*，也不接受它们作为启动 provider。任务 2.3 负责补齐。

  cmd/mcode-server/main.go:326 `			backend.RegisterHarness(openai.New(openai.Options{`

  cmd/mcode-server/main.go:227 `	case "memory", "claude", "codex":`

- 会话没有按 kind 隔离：所有 app API 引擎与 mcode 共用 `dataDir/mcode/sessions`；空 dataDir 则不落盘。新 ID 是无 kind 的 UUID，文件名是 `<id>.jsonl`，`SessionExists` 只查文件存在，不核对来源。UUID 避免正常生成时撞名，不等于来源隔离。正常切换 provider 时 remote 清空 sessionID，因此并不能从共享目录直接推定已有串话事故。

  internal/app/mcode_agent.go:269 `		storeDir = filepath.Join(dataDir, "mcode", "sessions")`

  internal/agent/session/session.go:26 `func NewID() string { return uuid.NewString() }`

  internal/agent/session/session.go:37 `	return filepath.Join(s.dir, id+".jsonl"), nil`

  internal/providers/mcode/shell.go:67 `	return session.NewStore(h.opts.StoreDir).Exists(sessionID)`

  internal/remote/memory_backend.go:4499 `		td.sessionID = ""`

### 剩余步骤

1. internal/app/engine.go:42 `		opts.ModelIDs = models.ModelIDs`：接入任务 3.1 的来源目录函数与任务 2.4 的按 kind 目录注册接口；预设合并完整静态 Models 与端点发现结果，普通自定义保留旧 ModelIDs 的异步发现与配置模型优先语义。将注册回调写成每次读最新配置/凭据；不要只捕获构造时 Source。回调必须区分“不属于 API 来源”与“API 来源目录暂时为空”。
2. internal/app/engine.go:47 `			opts.ModelIDs = func() []string { return mcodeEndpointModelIDs(cfg, endpointID) }`：删除 mcode 勾选覆盖，改取任务 3.1 的该端点全量目录 ID。Magpie 的 RefreshModels 同时刷新 `magpie.FetchModels` 的元数据缓存，不能继续仅刷新旧 OpenAI 的 ID 缓存。
3. internal/app/engine.go:170 `		if ids := mcodeEndpointModelIDs(cfg, endpointID); len(ids) > 0 {`：默认模型按“端点显式默认仍在全量目录中 → 全量目录首项”选择；缓存冷且收到 default 时做一次有超时的同步刷新；失败给出明确错误。已有 mcode 默认偏好仅作兼容排序，不作为目录准入条件。
4. internal/app/engine_anthropic.go:31 `		return source.Models`：把 Anthropic 的三行来源目录注册到同一个按 kind 回调入口，保证 ProviderModels("anthropic") 不再被 default 占位行遮住。保留独立 Messages 客户端构造，不把 Anthropic 送入当前只支持 OpenAI/网关分支的 resolveSourceClient。
5. internal/providers/mcode/mcode.go:216 `		Store: store, SessionID: turn.SessionID, MaxRounds: h.opts.MaxRounds, ContextWindow: contextWindow,`：新会话传给 agent 前生成 `<kind>_<UUID>`；原有非空 sessionID 保持不变。复用现有 session.NewID，不修改通用 agent/session 的生成规则。对已带其他来源前缀的 ID，Run 明确拒绝，SessionExists 返回 false，交由 remote 已有 handoff 处理。
6. internal/providers/mcode/shell.go:67 `	return session.NewStore(h.opts.StoreDir).Exists(sessionID)`：SessionExists 与 Run 共用同一来源前缀检查。保留旧 UUID/旧未加前缀 ID 的读取兼容，目录仍保持原位置。明确这是“新会话按 kind 命名、旧会话兼容读取”，不是旧文件全部实现强隔离；不要静默批量移动历史文件。

### 允许改动的文件

- internal/app/engine.go
- internal/app/engine_test.go
- internal/app/engine_anthropic.go
- internal/app/engine_anthropic_test.go
- internal/providers/mcode/mcode.go
- internal/providers/mcode/shell.go
- internal/providers/mcode/shell_test.go
- internal/providers/mcode/session_kind_test.go（新增）

### 测试

后续执行：`go test ./internal/providers/mcode/... ./internal/app/...`。

补充有意义的回归：预设完整模型集与配置默认模型优先；Magpie 未勾选也能默认运行；冷缓存默认模型刷新有界；两种协议实际请求路径与来源鉴权；新 ID 带 kind；错误 kind 前缀不读盘；旧 UUID 会话仍能续聊；保存后新实例继续读取原有会话。已有 engine 测试不能继续把“等于旧 ModelIDs”当成完整预设目录的验收标准。

### 验收标准

桌面所有已启用 API 来源实际执行内置引擎；所有来源有按 kind 的目录回调；Magpie 运行默认值不依赖 mcode/pi 勾选。新会话带来源前缀，既有无前缀会话可继续，历史兼容边界在变更说明中保留。

## 任务 2.3：收口 app 接线与独立 server

### 现状

- app 启动与自定义端点保存后的引擎替换已经完成，不应重复新建一套 buildAPIHarnesses 再替换全部入口。

  internal/app/app.go:1953 `			backend.RegisterHarness(newCustomEndpointHarness(ep, secrets, dataDir, mcpInjection))`

- mcodeTargets 已经只是 apisource.MagpieTargets 的薄封装，不再自己解析配置；删除封装属于任务 3.1 所拥有的 mcode_agent.go 内收尾。

  internal/app/mcode_agent.go:95 `	return apisource.MagpieTargets(cfg, secrets)`

- 引擎已不调用 piMagpieAllTargets。该函数仍服务 pi 判断与 Magpie 设置页，不能按原任务标题全局删除。

  internal/app/pi_magpie.go:271 `	for _, target := range piMagpieAllTargets(cfg, secrets) {`

  internal/app/pi_magpie.go:415 `	targets := piMagpieAllTargets(cfg, secrets)`

- 独立 server 的旧 OpenAI 注册尚未替换；它已经有 *dataFlag，不需要猜测配置目录。server 未依赖 app 包，不能为了复用私有构造函数而引入桌面/UI 启动依赖。

  cmd/mcode-server/main.go:162 `	dataFlag := flag.String("data", defaultDataDir, "mcode data directory")`

- OpenAI 保存热更新有遗漏：通用保存分支调用 buildProviderHarness，但此函数的专门分支没有 openai，最终要求该 ID 存在于 Presets；正常 OpenAI 配置并不在 Presets 中。

  internal/app/provider_settings.go:284 `		} else if h, ok := buildProviderHarness(cfg, c.secrets, spec.id, c.mcp); ok {`

  internal/app/app.go:1856 `	pc, ok := cfg.Providers.Presets[id]`

### 剩余步骤

1. internal/app/app.go:1794 `	switch id {`：补 OpenAI 专门构造分支，使用启动时相同 Options 与 newSourceEngineHarness；启动注册改复用该分支，避免再次分叉。保留现有预设与自定义端点调用链。
2. internal/app/app.go:1942 `	if cfg.Providers.Mcode.Enabled {`：mcode 改为兼容解析入口，保留注册供旧线程恢复，不依赖用户已不再需要的 Mcode.Enabled。新列表的隐藏由任务 2.4 承担；本批不做扫描数据库后条件注册。不要让这个别名抢在实际来源之前成为默认 provider。
3. internal/app/custom_endpoints.go:283 `			c.backend.RegisterHarness(newCustomEndpointHarness(epCfg, c.secrets, c.dataDir, c.mcp))`：保留已完成的保存后重建，只补集成回归；关闭/删除端点时清理任务 2.4 的目录回调，避免静态 ProviderModels 暴露残留目录。旧槽位启动迁移保持原样。
4. cmd/mcode-server/main.go:326 `			backend.RegisterHarness(openai.New(openai.Options{`：替换为命令包内轻量 API 引擎接线函数，遍历 apisource.Resolve，构造 providermcode.Options 并注册。旧 OpenAI 实例仅保留元数据能力；Anthropic 直接构造 Messages 客户端；Magpie 按模型选协议并携带亲和头，普通来源走 Chat Completions。新辅助文件不得调用 legacy.Run，不引入 app/UI 或 cgo。目录接任务 2.4 的注册接口，全量 Magpie 规则与任务 3.1 相同。
5. cmd/mcode-server/main.go:227 `	case "memory", "claude", "codex":`：除保留现有三种模式，还接受 Resolve 返回的 API kind；在 API 模式显式 SetHarness 选中目标来源，校验工作目录与权限；claude/codex 模式继续附加所有已配置 API 来源，memory 演示模式保持原语义。同步 provider flag 帮助、演示项目/线程初始化，不能只放宽校验却仍创建 memory 线程。
6. cmd/mcode-server/main.go:303 `	if settings.Provider == "claude" || settings.Provider == "codex" {`：把 API 接线从 ACP/CLI 分支中拆出；显式设置 StoreDir 为 filepath.Join(*dataFlag, "mcode", "sessions")，复用任务 2.2 的 kind 会话命名。按回调读取最新配置和凭据。server 不会调用桌面的旧槽位迁移：对仍留在 Presets 的 magpie*，在命令辅助函数中做只读兼容 Source 解析，并沿用旧凭据账号/环境回退；不要静默遗漏，也不要额外写用户配置。

### 允许改动的文件

- internal/app/app.go
- internal/app/custom_endpoints.go
- internal/app/custom_endpoints_test.go
- internal/app/provider_settings_test.go
- cmd/mcode-server/main.go
- cmd/mcode-server/settings.go
- cmd/mcode-server/settings_test.go
- cmd/mcode-server/api_engine.go（新增）
- cmd/mcode-server/api_engine_test.go（新增）

本任务不改 internal/app/mcode_agent.go，也不删除 internal/app/pi_magpie.go。server 使用命令包内接线是本批的最小选择；以后若要消除 app/server 的少量协议接线重复，应另列重构任务。

### 测试

后续执行：`go test ./internal/app/... ./cmd/mcode-server/...`。

覆盖 OpenAI 保存后依旧注册内置引擎；端点保存/关闭/删除同步目录；server API-only 启动、claude/codex 附加来源、memory 模式不变；两类协议的请求与会话持久化；旧 magpie* Presets 的只读兼容；未知/禁用来源报错；mcode 别名在其旧开关为 false 时仍能恢复旧线程。

### 验收标准

已接线的桌面路径不重复改造；OpenAI 热更新缺口关闭。server 的 API 来源均执行内置引擎并落盘，支持直接选 API kind。pi/settings 的全端点能力保留，mcode 不成为新会话默认入口。

## 任务 2.4：统一 ProviderModels、列表和选择器

### 现状

ProviderModels 是包级目录函数，不访问后端已注册 harness；ListProviders 先调用它，只有结果为空才从 harness.ModelIDs 补行。两者现在不是同一目录。

internal/remote/memory_backend.go:8515 `			models = ProviderModels(k)`

internal/remote/memory_backend.go:8539 `			if len(models) == 0 {`

| API kind | 当前 ProviderModels(kind) | harness.ModelIDs / 当前 ListProviders 的效果 |
| --- | --- | --- |
| deepseek / zai / meta | 空数组：识别为 OpenAI 兼容预设 | ModelIDs 为配置模型（缺省为预设默认）+ 后台 /models 发现结果；ListProviders 补这些 ID。没有消费完整 apisource.Preset.Models，断网不能保证完整预设列表。 |
| openai | 静态 openaiModels 起始表 | ModelIDs 同样为配置模型 + /models；ListProviders 专门把配置/发现结果并入静态表。 |
| anthropic | default 占位行 | 引擎 ModelIDs 有三种裸 Claude ID，但占位行非空，阻止 ListProviders 补这三行。 |
| 普通 custom-* | 空数组 | ListProviders 用旧 OpenAI 元数据的配置模型 + /models 补行。 |
| Magpie custom-* / magpie* | 空数组 | 引擎覆盖 ModelIDs，只返回此端点为 mcode 勾选的 ID（按配置兼容可能回退 pi）；ListProviders 补裸 ID，没有 mcode 聚合行上的完整网关元数据。 |
| mcode | SetMcodeModels 的聚合行；为空则 default 占位行 | buildMcodeHarness 未设 ModelIDs；聚合目录来自 Magpie 勾选模型，另加可用 Anthropic 的三个带 anthropic/ 前缀模型。 |

- mcode 目录仍使用独立全局 hook；generic API 目录尚不存在。

  internal/remote/mcode_catalog.go:15 `func SetMcodeModels(fn func() []wire.ProviderModel) {`

- Magpie 目前同时可作为端点 provider 和 mcode 的聚合模型出现。端点 provider 是 config.CustomEndpointProviderKind；mcode 行 ID 是 `target.ProviderID + "/" + model.ID`。target.ProviderID 按端点名称分配，是 CLI 注入路由前缀，不能当成稳定 API kind。

  internal/app/mcode_agent.go:374 `				ID:           target.ProviderID + "/" + model.ID,`

  internal/magpieinject/magpieinject.go:51 `		providerID := magpie.ProviderID(ep.Name, taken)`

- hideMagpieModelsInPicker 当前含义是“隐藏独立 Magpie 端点 provider”，并不隐藏 pi/claude/codex 的注入行，也不隐藏 mcode。用户只有一个 custom-<id> Magpie 端点且开关为 true 时，端点自己的 kind 被整组移除；能否在 mcode 下看到模型还取决于勾选。不能把该开关解释成“隐藏 CLI 重复行”。在 mcode 只作别名之后，继续按当前含义过滤会隐藏用户唯一的 API 来源，已不合理。

  internal/ui/model_picker.go:145 `			f.providers[config.CustomEndpointProviderKind(ep)] = true`

  internal/ui/model_picker.go:157 `		if f.providers[provider.ID] {`

  internal/ui/model_picker_test.go:46 `	// 独立直连 Magpie 的端点整体隐藏；pi/claude/codex 下的网关注入行全部保留。`

- CLI 动态目录校验重复且不一致：后端包含 cursor/opencode/grok/devin，UI 不包含；UI 又额外包含 mcode 与所有 custom-*。这影响模型校验、最近使用恢复和配对过滤，不只是展示。

  internal/remote/models.go:428 `	case "commandcode", "cline", "pi", "omp", "copilot", "droid", "cursor", "opencode", "grok", "devin":`

  internal/ui/model_picker.go:665 `	case "commandcode", "cline", "mcode", "pi", "omp", "copilot", "droid":`

  internal/ui/model_picker.go:668 `	return strings.HasPrefix(provider, "custom-")`

### 剩余步骤

1. internal/remote/mcode_catalog.go:15 `func SetMcodeModels(fn func() []wire.ProviderModel) {`：新增按 kind 的目录 hook 注册表，接口定为 SetAPICatalog(kind string, fn func() []wire.ProviderModel)；nil 清除。查询返回 (rows, registered)，区分已注册但冷缓存为空。SetMcodeModels 保留为注册 mcode 的薄封装。锁只保护取回调，不在执行回调时持锁；返回目录副本，避免 ProviderModels 补元数据污染共享缓存。
2. internal/remote/models.go:435 `	base := nativeProviderModels(provider)`：API hook 优先于旧静态/default 分支。无运行时 hook 的预设用 apisource.Preset.Models 作为离线目录，Anthropic 使用来源模型表；openai 保留起始表兜底。普通 custom-* 无 hook 时维持空目录，不伪造模型；Magpie 有 hook 但冷缓存为空时维持空结果，不把 default 当真实模型。统一由 ProviderModels 补 models.dev 能力。
3. internal/remote/memory_backend.go:8516 `			if k == "openai" {`：API hook 已提供最终目录时跳过 OpenAI 专门合并和 ModelIDs 二次补行；没安装 hook 的旧 harness 保留兼容补行。ListProviders 与 ProviderModels 对同一 API kind 返回相同模型 ID/默认值/元数据。mcode 继续下发供旧线程解析，选择列表由 UI 排除，不从 harness 注册表删除。
4. internal/remote/refresh_models.go:102 `			if r, ok := h.(interface{ RefreshModels(context.Context) error }); ok {`：保留已经存在的通用刷新，不重写成一张 API kind 白名单；任务 2.2/2.3 提供真正刷新目录缓存的回调。mcode 别名无需重复请求相同端点；端点刷新后别名读取同一缓存。可用性检查、超时上下文与错误聚合沿用原逻辑。
5. internal/remote/models.go:426 `func cliCatalogProvider(provider string) bool {`：把 CLI provider ID 判定提取到无 UI/remote 依赖的 internal/harness/catalog.go，remote 与 UI 共用。保留“空目录/default 占位放行”的规则；API 的非空目录按真实行校验，不再将 custom-* 和 mcode 冒充 CLI。固定模型的既有例外继续由各层现有 pin 逻辑处理。
6. internal/ui/model_picker.go:151 `func filterModelPickerMagpie(providers []ProviderInfo, f modelPickerMagpieFilter) []ProviderInfo {`：独立 API kind 总是可见，停止按 hideMagpieModelsInPicker 移除这些组；在选择器统一排除 mcode 别名，包括固定条目和新选择入口。保留原始 provider 数据供旧会话标签/解析使用，别把 UI 过滤结果写回模型目录。moveMcodeProviderLast 的调用相应移除。
7. internal/ui/settings_page_magpie.go:75 `			{ID: "row-magpie-model-picker-visible", Title: "在模型选择器中显示", Aliases: []string{"Magpie", "模型选择器", "显示"}, Control: toggle(inverted(val(func(cfg *config.Config) *bool { return &cfg.Providers.HideMagpieModelsInPicker }, true)))},`：移除已失去意义的显示开关；JSON 字段保留读取兼容但不再控制独立 API 来源，不重解释成另一种隐藏行为，不迁移或覆盖用户配置。调整依赖旧过滤语义的现有测试。

### 允许改动的文件

- internal/remote/api_catalog.go（新增）
- internal/remote/api_catalog_test.go（新增）
- internal/remote/mcode_catalog.go
- internal/remote/models.go
- internal/remote/models_test.go
- internal/remote/memory_backend.go
- internal/remote/refresh_models.go
- internal/remote/refresh_models_test.go
- internal/harness/catalog.go（新增）
- internal/harness/catalog_test.go（新增）
- internal/ui/model_picker.go
- internal/ui/model_picker_test.go
- internal/ui/settings_page_magpie.go
- internal/ui/settings_page_magpie_test.go
- internal/ui/settings_models_test.go
- internal/ui/steward_model_picker_test.go

### 测试

后续执行：`go test ./internal/harness/... ./internal/remote/... ./internal/ui/...`。

表驱动覆盖所有 API kind 的 ProviderModels 与 ListProviders 一致、静态预设断网可用、Anthropic 三行不被占位遮蔽、回调删除/并发读取/副本隔离、Magpie 冷缓存与刷新。选择器覆盖单一 custom Magpie + hide=true 仍有可选模型、mcode 不出现在新选择和固定区、旧线程仍能解析名称。前后端使用同一组 CLI 分类测试数据。

### 验收标准

deepseek、普通自定义端点和 Magpie 端点各自以真实 kind 出现；模型 ID 为端点内裸 ID。mcode 只用于旧线程兼容，不提供新选择入口。ProviderModels、ListProviders 和刷新共同指向同一来源目录。用户现有 hide=true 不再遮住唯一 API 端点。

## 任务 3.1：Magpie 全量目录与 mcode 兼容投影

### 现状

- 全部启用端点的枚举已统一，尚未统一模型集合。mcodeModelsForConfig 仍遍历 Selections，再用 Selected 取勾选子集；newSourceEngineHarness 也单独覆盖为该端点的 mcode 勾选。这件事应首先落在 custom-*/magpie* 各自目录上，不能只扩充 mcode 聚合目录。

  internal/apisource/magpie.go:10 `	return magpieinject.Targets(cfg, secrets, nil)`

  internal/app/mcode_agent.go:349 `	for _, selection := range setting.Selections {`

  internal/app/mcode_agent.go:369 `		for _, model := range magpieinject.Selected(ids, fetched) {`

- 网关 Levels、ContextWindow、价格映射和后台预热已经存在；不应再次按“尚未实现”立项。mcodeEfforts 保持网关顺序后按引擎可接受档位过滤；元数据缺失的推理模型仍有五档兜底，不是所有模型固定五档。

  internal/app/mcode_agent.go:376 `				Efforts:      mcodeEfforts(model),`

  internal/app/mcode_agent.go:377 `				ContextLimit: model.ContextWindow,`

  internal/app/mcode_agent.go:378 `				Cost:         magpieModelCost(model),`

  internal/app/mcode_agent.go:465 `		return magpieEffortsFor(config.ProviderMcode, model.Levels)`

  internal/app/agent_magpie.go:126 `func warmMagpieModels(target magpieinject.Target) {`

- 当前只有进入勾选端点分支才预热，没有勾选就不会靠 mcode 目录发现端点全部模型。预热本身已带超时和同端点并发去重；它不显式发 provider 刷新通知，需要在集成验收确认下一次目录拉取能观察缓存结果，不能承诺已有即时推送。

  internal/app/mcode_agent.go:366 `			warmMagpieModels(target)`

  internal/app/agent_magpie.go:141 `		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)`

### 剩余步骤

1. internal/app/mcode_agent.go:343 `	for _, target := range mcodeTargets(cfg, secrets) {`：新增 app 来源目录辅助文件 api_catalog.go，按当前 apisource.Resolve 的 Source.Kind 生成完整 wire.ProviderModel 行。Magpie 用端点配置 ID 关联 MagpieTargets.Endpoint，读取该端点全部 CachedModels，不用 target.ProviderID 查 API kind。非 Magpie 静态预设合并 Source.Models 与传入的 legacy ModelIDs；Anthropic 用 Source.Models；普通 custom 用 legacy ModelIDs；统一配置默认优先与去重。
2. internal/app/mcode_agent.go:369 `		for _, model := range magpieinject.Selected(ids, fetched) {`：端点目录直接遍历全部 fetched，ID 用 model.ID；复用 mcodeEfforts、ContextWindow、magpieModelCost。没有 Key 的端点不返回虚假可用模型；缓存冷时对每个启用且有 Key 的端点调用 warmMagpieModels，先返回空目录，后续拉取补齐；不再用勾选列表伪造全量结果。
3. internal/app/mcode_agent.go:339 `	setting := config.MagpieAgentSetting(cfg, config.ProviderMcode)`：重写 mcodeModelsForConfig 为各端点目录的兼容投影；保留旧 target.ProviderID/modelID 形状和 anthropic/ 形状以恢复旧线程。旧 mcode 默认仅影响聚合排序，不能过滤集合。端点自己的目录始终用裸 ID；不能把旧注入路由前缀改成 custom-* 后让老线程失效。
4. internal/app/mcode_agent.go:95 `	return apisource.MagpieTargets(cfg, secrets)`：将本文件所有 mcodeTargets 调用直接替换为 apisource.MagpieTargets，删除薄封装。本批不动仍被 pi/settings 使用的 piMagpieAllTargets。mcodeSelectionModels 在任务 2.2 不再引用后删除；选择集合不再参与引擎目录/默认模型判定。
5. internal/app/mcode_agent.go:296 `	remote.SetMcodeModels(mcodeModelCatalog)`：适配任务 2.4 的薄封装，别名目录读同一份全量缓存；提供给任务 2.2 的来源注册辅助函数，内部调用 SetAPICatalog(kind, callback)。注册后的回调重新读配置/凭据并检测来源是否仍启用，不能让删除端点继续出行。元数据生成留在 app，不把 wire/remote 依赖反向塞进 apisource。
6. internal/app/mcode_agent.go:119 `		models, hit := magpie.CachedModels(target.BaseURL, target.APIKey)`：默认模型与全量目录共用端点缓存；保留老 mcode 显式默认的兼容解析。通过刷新/重新拉取目录使预热结果可见；若现有 UI 拉取时机不足，由任务 2.4 在刷新流程内触发已有目录重新请求，不新增常驻轮询。

### 允许改动的文件

- internal/app/mcode_agent.go
- internal/app/mcode_agent_test.go
- internal/app/api_catalog.go（新增）
- internal/app/api_catalog_test.go（新增）

复用 internal/apisource/magpie.go 与 internal/app/agent_magpie.go 的现有实现，无需为“全端点/预热已存在”修改它们。本任务不改 engine.go、remote 或 UI；这些接入分别由任务 2.2、2.4 拥有。

### 测试

后续执行：`go test ./internal/app/...`。

两个启用端点共五个模型，mcode/pi 勾选为空时，每个端点目录仍返回自己的全部行，聚合别名共五行；同名模型在不同端点不串来源；关闭/删除/无 Key 端点不出可用行；冷缓存触发后台预热并在后续读取补齐；Levels 顺序、无推理档位、窗口、价格保持元数据；聚合前缀兼容，端点 ID 始终为裸模型 ID。

### 验收标准

新增 Magpie 端点不用 mcode 或 pi 勾选就能获得全部模型。全量集合定义在端点目录；mcode 仅为它们的兼容投影。保留已有档位/窗口/价格/预热能力，不重复实现网络缓存。

## 依赖、接口与并行安排

四个任务的允许写文件集合互不重叠，可以分配四个工作副本。共享 API 约定如下，先按约定并行写，合并后再由主代理统一构建和测试。

| 任务 | 提供的约定 | 依赖与合并顺序 |
| --- | --- | --- |
| 2.4 | remote.SetAPICatalog(kind, fn)，nil 清理；查询区分已注册空目录；共享 CLI 判定 | 最先确定并合入目录接口。可以与 3.1 并行。 |
| 3.1 | app 中按 Source.Kind 生成 wire 行的辅助函数；来源回调注册辅助；mcode 聚合兼容投影 | 按 2.4 接口编写；可先完成纯目录生成与测试。 |
| 2.2 | app 两类来源构造安装目录回调；Magpie ModelIDs/默认值/刷新消费全量目录；provider 新会话前缀 | 依赖 2.4、3.1；会话命名部分可先独立完成。 |
| 2.3 | app OpenAI 热更新与别名注册；server 引擎接线和 API provider 启动 | 按 2.2/2.4 的行为约定编写；server 元数据规则与 3.1 对齐，合并后集成验证。 |

优先做的两个任务是 2.4 与 3.1：前者修复“端点被隐藏、Anthropic 被占位遮挡、ProviderModels 不认来源”，后者消除勾选对全量模型的限制。二者完成后再接入 2.2，最后合并 2.3 做 app/server 集成验收；四组可并行编写，但不应把未接完的目录接口单独发布。

注意事项与未尽部分：

- 本次只做静态核对；用户“仅一个 Magpie 自定义端点、hide=true”来自 brief，没有读取本机私密配置或发起网关请求。实际端点模型数量、凭据状态和缓存内容未验证。
- 保留 mcode 别名下发用于旧线程解析，UI 隐藏新入口；本批没有扩展到所有设置页和远程网页的全面去 mcode 工作，原阶段 5/6 继续承担这些清理。
- 新会话 kind 前缀不能证明旧无前缀文件的来源；本批保留其兼容读取，不声称历史存储实现强隔离。
- server 采用命令包轻量接线，避免为此引入 app/UI；需要用协议请求测试约束其与 app 行为一致。尚未实现或验证。
- 本文件不承诺预热完成后已有实时推送；任务 2.4/3.1 必须验证正常目录刷新能看见缓存结果。
