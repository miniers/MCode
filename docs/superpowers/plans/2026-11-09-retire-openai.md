# 阶段 6：退役旧 OpenAI 包与一次性生成收尾

本计划按当前独立工作副本的源码记录行号。仅完成调研与计划编写；没有修改 Go 文件，没有运行构建、测试、lint 或类型检查。下文的测试命令均为后续实施要求，尚未执行。保持纯 Go，不引入 cgo，不改 kind、凭据账号或历史消息存储。

## 结论与边界

- 删除旧包之前，还要替换 5 个生产文件的依赖和 1 个测试文件的直接 import；预设元数据已经迁到 apisource，旧包中的 Preset 只是类型别名。
  internal/providers/openai/presets.go:11 `type Preset = apisource.Preset`
- 引擎执行已不依赖旧工具循环；剩余 legacy 职责是 kind、显示名、模型目录、刷新、可用性及默认模型回退。
  internal/app/engine.go:39 `func newSourceEngineHarness(legacy harness.Harness, cfg config.Config, secrets config.SecretStore, dataDir string, mcpInjection providerMCP) harness.Harness {`
- 一次性生成尚未统一到来源客户端；API 来源仍走单独的非流式 Chat Completions 实现。
  internal/app/textgen_dispatch.go:305 `		return textgen.GenerateOpenAICompatibleWithOptions(ctx, textgen.OpenAICompatibleOptions{`
- 任务 6.3 尚未实现；当前选择只认显式提交模型或通用 provider，不比较目录价格。
  internal/app/textgen_dispatch.go:153 `	provider = cfg.Models.Provider`
- 原任务 6.2 已有落地依据，本次不重新分派工具名或审批工作；保留历史旧名兼容。
  internal/harness/permission.go:200 `		if readOnlyTool || toolName == "write" || toolName == "edit" || toolName == "write_file" || toolName == "edit_file" ||`

## 1. openai 包逐项依赖清单

### internal/app/app.go

- 直接 import，退役接线完成后删除。
  internal/app/app.go:42 `	"mcode/internal/providers/openai"`
- `openai.New`、`openai.Options`：为官方 OpenAI 构造 legacy；其中携带 BaseURL、配置模型、OPENAI_API_KEY 回退、keyring 账号和 MCP，再将 legacy 包入内置引擎。
  internal/app/app.go:1824 `		legacy := openai.New(openai.Options{`
  internal/app/app.go:1833 `		return newSourceEngineHarness(legacy, cfg, secrets, mcodeRuntimeDataDir(), mcpInjection), true`
- `openai.PresetByID`：识别启用的预设；`openai.PresetOptions`：组装预设 URL、默认模型、凭据回退、额外头，随后覆写 pc.Model、MCP；`openai.New`：仅为引擎提供 legacy 元数据。
  internal/app/app.go:1899 `	preset, ok := openai.PresetByID(id)`
  internal/app/app.go:1903 `	opts := openai.PresetOptions(preset, secrets, pc.BaseURL, pc.CodingPlan)`
  internal/app/app.go:1909 `	return newSourceEngineHarness(openai.New(opts), cfg, secrets, mcodeRuntimeDataDir(), mcpInjection), true`

### internal/app/custom_endpoints.go

- 直接 import；`openai.New`、`openai.Options` 为每个自定义端点构造 legacy。凭据只用该端点账号；fallback 配置包含端点自身，应继续保留。
  internal/app/custom_endpoints.go:17 `	"mcode/internal/providers/openai"`
  internal/app/custom_endpoints.go:52 `	legacy := openai.New(openai.Options{`
  internal/app/custom_endpoints.go:64 `	fallback.Providers.Custom = []config.CustomEndpointConfig{ep}`
  internal/app/custom_endpoints.go:65 `	return newSourceEngineHarness(legacy, fallback, secrets, dataDir, mcpInjection)`
- `[]openai.Preset` 返回类型和局部切片、`openai.Magpie` 枚举旧槽位：只为迁移旧配置，不是运行时生成路径，可直接改为 apisource 的对应类型和函数。
  internal/app/custom_endpoints.go:429 `func legacyMagpiePresets(presets map[string]config.PresetProviderConfig) []openai.Preset {`
  internal/app/custom_endpoints.go:430 `	var res []openai.Preset`
  internal/app/custom_endpoints.go:432 `		preset, ok := openai.Magpie(slot)`
- `openai.Preset` 参数类型：迁移密钥，使用 ID、APIKeyAccount；上游迁移还使用 DisplayName、ResolveBaseURL，均已在 apisource.Preset 中存在。
  internal/app/custom_endpoints.go:491 `func migrateLegacyMagpieSecret(secrets config.SecretStore, preset openai.Preset) error {`
  internal/app/custom_endpoints.go:472 `					BaseURL: preset.ResolveBaseURL(pc.BaseURL, pc.CodingPlan),`
  internal/apisource/presets.go:96 `func (p Preset) ResolveBaseURL(override string, codingPlan bool) string {`

### internal/app/provider_settings.go

- 直接 import；第一处 `openai.PresetByID` 获取预设的 keyring 账号，第二处获取设置页默认地址、默认模型与账号；均直接替换成 apisource.PresetByID，无需构造客户端。
  internal/app/provider_settings.go:12 `	"mcode/internal/providers/openai"`
  internal/app/provider_settings.go:77 `	if preset, ok := openai.PresetByID(id); ok {`
  internal/app/provider_settings.go:149 `		preset, ok := openai.PresetByID(spec.id)`

### internal/app/textgen_dispatch.go

- 直接 import；`openai.PresetByID` 识别预设，`openai.PresetOptions` 仅用于取 URL、APIKeyAccount、环境变量回退和默认模型；没有调用旧 harness 生成文本。
  internal/app/textgen_dispatch.go:13 `	"mcode/internal/providers/openai"`
  internal/app/textgen_dispatch.go:120 `	if preset, ok := openai.PresetByID(provider); ok {`
  internal/app/textgen_dispatch.go:122 `			opts := openai.PresetOptions(preset, secrets, pc.BaseURL, pc.CodingPlan)`
  internal/app/textgen_dispatch.go:125 `				apiKey:  firstNonEmpty(secretValue(secrets, opts.APIKeyAccount), opts.APIKey, opts.FallbackAPIKey),`
  internal/app/textgen_dispatch.go:126 `				model:   firstNonEmpty(overrideModel, pc.Model, opts.Model),`
- 文本请求由 internal/textgen 自己发起，openai 包在这里仅承担配置解析；仅换 import 虽能退役旧包，但仍会留下协议和额外请求头分叉，建议同一步改接 apisource.NewClient。
  internal/app/textgen_dispatch.go:305 `		return textgen.GenerateOpenAICompatibleWithOptions(ctx, textgen.OpenAICompatibleOptions{`

### internal/remote/models.go

- 直接 import；唯一包符号调用 `openai.PresetByID` 判断是否属于兼容 API 来源，以便无目录时返回空数组。换 apisource.PresetByID 即可，保留 custom- 前缀及旧 Magpie 槽位识别。
  internal/remote/models.go:14 `	"mcode/internal/providers/openai"`
  internal/remote/models.go:418 `	_, ok := openai.PresetByID(provider)`
- 同文件已用 apisource 构造静态预设目录；已注册的 API 目录优先，无需新增 remote 对 app 的依赖。
  internal/remote/models.go:428 `	if rows, registered := apiCatalog(provider); registered {`
  internal/remote/models.go:431 `	if preset, ok := apisource.PresetByID(provider); ok && len(preset.Models) > 0 {`
- `openaiModels` 是本文件的切片变量，不是旧包导出符号；保留官方来源的起始目录。`openai` 来源 ID、协议名和 models.dev provider 键也不应批量删除。
  internal/remote/models.go:154 `var openaiModels = []wire.ProviderModel{`
  internal/remote/models.go:597 `		src = openaiModels`

### 包外测试：唯一直接 import 在 internal/app/engine_test.go

- import 与预设用例：使用 PresetByID、PresetOptions、New 创建 legacy；改为来源 kind 构造，显示名直接与来源元数据比。
  internal/app/engine_test.go:21 `	"mcode/internal/providers/openai"`
  internal/app/engine_test.go:105 `	preset, ok := openai.PresetByID("deepseek")`
  internal/app/engine_test.go:109 `	legacyOpts := openai.PresetOptions(preset, secrets, server.URL, false)`
  internal/app/engine_test.go:110 `	legacyOpts.Model = "deepseek-flash"`
  internal/app/engine_test.go:111 `	legacy := openai.New(legacyOpts)`
  internal/app/engine_test.go:113 `	h := newSourceEngineHarness(legacy, cfg, secrets, "", providerMCP{})`
  internal/app/engine_test.go:117 `	if got, want := h.(interface{ DisplayName() string }).DisplayName(), legacy.DisplayName(); got != want {`
- 换 Key 用例：PresetByID、PresetOptions、New 仅为生成待测 harness。
  internal/app/engine_test.go:549 `	preset, _ := openai.PresetByID("deepseek")`
  internal/app/engine_test.go:550 `	h := newSourceEngineHarness(openai.New(openai.PresetOptions(preset, secrets, server.URL, false)), cfg, secrets, "", providerMCP{})`
- 停用来源用例：PresetByID、PresetOptions、New 生成 legacy，再传 resolveSourceClient；移除 legacy 参数后保留停用断言。
  internal/app/engine_test.go:571 `	preset, _ := openai.PresetByID("deepseek")`
  internal/app/engine_test.go:572 `	legacy := openai.New(openai.PresetOptions(preset, secrets, "", false))`
  internal/app/engine_test.go:576 `	_, _, err := resolveSourceClient(harness.Kind("deepseek"), legacy, cfg, secrets, "deepseek-chat", "")`
- 同文件没有 import 依赖但受签名修改影响的 nil legacy 调用也要更新：Magpie 缓存默认、冷启动默认、普通来源默认、官方 OpenAI 默认。
  internal/app/engine_test.go:296 `	_, model, err := resolveSourceClient("magpie", nil, config.Defaults(), secrets, "default", "")`
  internal/app/engine_test.go:344 `			_, model, err := resolveSourceClient("custom-cold", nil, cfg, secrets, "default", "")`
  internal/app/engine_test.go:584 `	if got := sourceEngineDefaultModel(apisource.Source{DefaultModel: " deepseek-flash "}, harness.Kind("deepseek"), cfg, nil); got != "deepseek-flash" {`
  internal/app/engine_test.go:587 `	if got := sourceEngineDefaultModel(apisource.Source{BaseURL: "https://gateway.example/v1"}, harness.Kind("openai"), cfg, nil); got != "gpt-4o" {`
- 其余有关测试通过 app 函数或目录 API 间接覆盖，不能随旧包一起删除；尤其 textgen_mcode_test.go 已测新客户端，不是旧包测试。
  internal/app/provider_settings_test.go:83 `	if models := harnessModelIDs(t, h); !reflect.DeepEqual(models, []string{"deepseek-v4-pro", "deepseek-flash", "deepseek-chat", "deepseek-reasoner"}) {`
  internal/app/custom_endpoints_test.go:660 `func TestMigrateLegacyMagpieEndpoints(t *testing.T) {`
  internal/app/textgen_dispatch_test.go:125 `func TestGenerateTextCustomEndpoint(t *testing.T) {`
  internal/app/textgen_mcode_test.go:39 `	out, err := generateText(context.Background(), cfg, secrets, config.ProviderMcode, "magpie-mcode/plain-model", "high", "给这段对话起标题")`
  internal/remote/models_test.go:101 `func TestProviderModelsOpenAICompatibleStayEmpty(t *testing.T) {`
- remote 测试里的局部变量 openai 不是包引用，不要误改。
  internal/remote/providers_notice_test.go:61 `	if len(openai.Models) == 0 {`
- 包内测试随旧目录删除：history_cancel_test.go、mcp_test.go、models_test.go、openai_test.go、presets_test.go、sse_test.go、tools_test.go。模型请求的有效行为测试先迁到 apisource；旧 ModelCache 若无新调用者，不必保留其实现和全部内部测试。
  internal/providers/openai/models_test.go:31 `func TestListModels(t *testing.T) {`
  internal/providers/openai/models_test.go:72 `func TestModelCacheHitAndExpiry(t *testing.T) {`

### 旧工具名与注释残留

- 上述 5 个生产文件没有调用旧工具定义的包符号。旧名字定义仍在待删除目录；历史兼容继续由 harness/UI 处理，不迁移历史消息。
  internal/providers/openai/tools.go:40 `			Name: "read_file",`
  internal/providers/openai/tools.go:109 `			Name:        "write_file",`
  internal/providers/openai/tools.go:128 `			Name:        "edit_file",`
- 两处配置注释和一处 apisource 注释仍提旧包，属于文档残留而非编译依赖；实施时一并改指新来源层。历史计划中的旧路径保留为历史记录。
  internal/config/config.go:1390 `// key 为 internal/providers/openai 的预设 ID。零值表示未启用。`
  internal/config/config.go:2271 `// ACP Agent ID 与 internal/providers/acp.Agents() 保持一致，预设 ID 与 internal/providers/openai.PresetByID 一致。`
  internal/apisource/source.go:56 `	// openaiDefaultBaseURL 与 openai.New 的默认地址一致。`

## 2. engine.go 的全部 legacy 职责与替代

1. kind：legacy.Kind() 决定 Options.Kind 与后续目录/来源解析。改构造函数第一参为 `kind harness.Kind`，由调用方传原 kind，后续继续实时 Resolve。
   internal/app/engine.go:40 `	kind := legacy.Kind()`
2. 显示名：legacy 可选接口提供名称。改取已解析 Source.Label，不造临时 harness。
   internal/app/engine.go:43 `	if named, ok := legacy.(interface{ DisplayName() string }); ok {`
   internal/app/engine.go:44 `		opts.DisplayName = named.DisplayName()`
   internal/apisource/source.go:28 `	Label string`
3. 模型 ID：保存 legacy.ModelIDs 方法，在每次 catalog 回调中读取，作为 apiSourceCatalog 第四参；替换为独立来源模型缓存返回的 fetched IDs。第四参可仅重命名为 fetchedModelIDs，保持目录聚合及测试接口稳定。
   internal/app/engine.go:47 `	if models, ok := legacy.(interface{ ModelIDs() []string }); ok {`
   internal/app/engine.go:48 `		legacyModels = models.ModelIDs`
   internal/app/engine.go:55 `			ids = legacyModels()`
   internal/app/engine.go:57 `		return apiSourceCatalog(current, store, string(kind), ids)`
4. 刷新：legacy.RefreshModels 直接赋值。普通 API 来源改接 apisource.ListModels + app 缓存；Magpie 保留当前专用 FetchModels，因为其目录含价格、窗口与能力；Anthropic 使用现有静态表，不请求 OpenAI /models。
   internal/app/engine.go:68 `	if refresh, ok := legacy.(interface {`
   internal/app/engine.go:71 `		opts.RefreshModels = refresh.RefreshModels`
   internal/app/engine.go:81 `			_, err := magpie.FetchModels(ctx, currentSource.BaseURL, currentSource.APIKey(store))`
5. 可用性：legacy.Available 直接赋值。改为当前启用来源与 Source.APIKey 的结果，但保留后台钥匙串探测/缓存，避免每次 providers.list 同步阻塞。不能把 Available 留空，否则引擎默认视为始终可用。
   internal/app/engine.go:85 `	if available, ok := legacy.(interface{ Available() (bool, string) }); ok {`
   internal/app/engine.go:86 `		opts.Available = available.Available`
   internal/providers/mcode/mcode.go:33 `	// Available 报告该来源是否可用，不可用时给出原因；为空表示始终可用。`
   internal/providers/openai/openai.go:163 `	availabilityCacheTTL = 60 * time.Second`
6. ResolveClient：闭包将 legacy 传给 resolveSourceClient；函数自身仅在普通来源默认模型分支继续传给 sourceEngineDefaultModel。删除这两个函数的 legacy 参数，按来源缓存和目录解默认值。
   internal/app/engine.go:89 `		return resolveSourceClient(kind, legacy, cfg, secrets, model, affinityKey)`
   internal/app/engine.go:135 `func resolveSourceClient(kind harness.Kind, legacy harness.Harness, cfg config.Config, secrets config.SecretStore, model, affinityKey string) (llm.Client, string, error) {`
   internal/app/engine.go:169 `			modelID = sourceEngineDefaultModel(source, kind, current, legacy)`
7. 默认模型：显式 Source.DefaultModel → OpenAI kind 或官方主机的 gpt-4o → 目录第一个非空 ID；目录为空时做一次有界同步刷新。当前 cfg 参数在 sourceEngineDefaultModel 函数体中未使用，可连同 legacy 参数重塑为实际需要的来源/缓存输入。保持 kind=openai 即默认 gpt-4o 的现有语义，即使其 BaseURL 指向网关。
   internal/app/engine.go:235 `func sourceEngineDefaultModel(source apisource.Source, kind harness.Kind, cfg config.Config, legacy harness.Harness) string {`
   internal/app/engine.go:236 `	if model := strings.TrimSpace(source.DefaultModel); model != "" {`
   internal/app/engine.go:240 `		return "gpt-4o"`
   internal/app/engine.go:242 `	models, ok := legacy.(interface{ ModelIDs() []string })`
   internal/app/engine.go:246 `	ids := models.ModelIDs()`
   internal/app/engine.go:248 `		if refresher, ok := legacy.(interface {`
   internal/app/engine.go:251 `			_ = refresher.RefreshModels(context.Background())`
   internal/app/engine.go:252 `			ids = models.ModelIDs()`
   internal/app/engine.go:257 `			return id`

### 普通来源模型请求的最小提取方案

- `apiSourceCatalog` 已具备静态来源、默认模型置顶、去重及 Magpie 元数据目录；普通 /models 网络拉取仍由 legacy 触发，不能只删第四参而丢掉用户自定义端点的动态模型。
  internal/app/api_catalog.go:38 `		ids := append([]string(nil), source.Models...)`
  internal/app/api_catalog.go:40 `			ids = append(ids, legacyModelIDs...)`
  internal/app/api_catalog.go:49 `		return orderCatalog(rows, source.DefaultModel)`
- 旧通用请求函数在 openai/models.go，最小新签名建议为 `func ListModels(ctx context.Context, client *http.Client, source Source, apiKey string) ([]string, error)`，放 `internal/apisource/models.go`（新增）。用 Source 而不是裸 URL 只为一并支持现有 Source.Headers；不引入 options 框架或 app/remote 依赖。
  internal/providers/openai/models.go:28 `func ListModels(ctx context.Context, client *http.Client, baseURL, apiKey string) ([]string, error) {`
  internal/apisource/source.go:40 `	Headers map[string]string`
- 保留去除 /chat/completions 后再拼 /models、Bearer 认证、1 MiB 上限、ID 去重排序。错误只保留安全的状态/诊断，不直接搬运旧实现的原始响应体错误到新用户提示。
  internal/providers/openai/models.go:23 `	base = strings.TrimSuffix(base, "/chat/completions")`
  internal/providers/openai/models.go:39 `		req.Header.Set("Authorization", "Bearer "+key)`
  internal/providers/openai/models.go:48 `	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))`
  internal/providers/openai/models.go:85 `	sort.Strings(models)`
  internal/providers/openai/models.go:60 `		return nil, fmt.Errorf("openai: models HTTP %d: %s", resp.StatusCode, msg)`
- 缓存/后台调度留在 app 新增 api_model_cache.go：保留成功 5 分钟、失败 30 秒、失败保留旧目录、5 秒请求超时、同一来源单次在途刷新及过期请求结果隔离。标识至少覆盖 kind、URL、凭据身份与相关请求头；每次刷新前 Resolve 当前配置/Key，变更后不混用旧目录，不把 Key 写日志。目录读取只读缓存并触发后台预热；执行默认模型解析才允许有界同步等待。复用旧 readhttp 客户端包装，不搬旧工具/会话状态。
  internal/providers/openai/openai.go:156 `	modelListCacheTTL = 5 * time.Minute`
  internal/providers/openai/openai.go:158 `	modelListFailureTTL = 30 * time.Second`
  internal/providers/openai/openai.go:160 `	modelListRequestTimeout = 5 * time.Second`
  internal/providers/openai/openai.go:188 `		go h.refreshModelsInBackground()`
  internal/providers/openai/openai.go:249 `	if gen != h.modelGen {`
  internal/providers/openai/openai.go:115 `		modelHTTPClient: readhttp.NewClient(opts.HTTPClient),`
- /models 并非只在旧包有实现：设置页 TestCustomEndpoint 已有独立请求，含状态、耗时、诊断及更严格的 body/data 校验。本次保留其 UI 测试职责，不强行合并两个返回契约；它不能代替日常目录缓存。
  internal/app/custom_endpoints.go:525 `func (c *Client) TestCustomEndpoint(ctx context.Context, ep mcodeui.CustomEndpoint, apiKey string) (mcodeui.CustomEndpointTestResult, error) {`
  internal/app/custom_endpoints.go:547 `	targetURL := strings.TrimRight(rawBaseURL, "/") + "/models"`
  internal/app/custom_endpoints.go:581 `	if readErr != nil || len(body) > 1<<20 {`

## 3. 一次性文本生成现状与最小方案

### 当前路径

- 标题：generateAutoTitle → textgenModelSelection → autoTitleGenerateText（默认 generateText）；总预算 65 秒。提交信息和记忆精简共用同一选择与生成入口，不能只改标题。
  internal/app/textgen_wiring.go:179 `var autoTitleGenerateText = generateText`
  internal/app/textgen_wiring.go:197 `	provider, model, effort := textgenModelSelection(cfg)`
  internal/app/textgen_wiring.go:200 `	genCtx, cancel := context.WithTimeout(ctx, 65*time.Second)`
  internal/app/textgen_wiring.go:203 `	out, err := autoTitleGenerateText(genCtx, cfg, config.NewSecretStore("mcode"), provider, model, effort, prompt)`
  internal/app/client.go:2831 `	out, err := generateText(ctx, cfg, c.textgenSecrets(), provider, model, effort, prompt)`
  internal/app/client.go:3543 `	out, err := generateText(ctx, cfg, c.textgenSecrets(), provider, model, effort, prompt)`
- provider 优先级为显式提交信息选择 > cfg.Models.Provider；空 model 交后端取默认值。utility 角色实际不参与，部分调用点注释已过时。
  internal/app/textgen_dispatch.go:154 `	if sel := cfg.SourceControl.CommitMessageModel; strings.TrimSpace(sel.Provider) != "" {`
  internal/app/textgen_dispatch_test.go:213 `	// 未指定提交信息模型：即使配置了 utility 角色，也不再读它。`
- CLI/memory 走 textgen.GenerateWithOptions；mcode 别名走 mcodeGenerateText；官方 OpenAI/预设/自定义/Magpie 来源则经 resolveOpenAICompatible 进入非流式 Chat Completions。Anthropic 来源未在该 resolver 中单列，因而会落入 CLI/mcode 兜底，不能认为已经直连 Anthropic。
  internal/app/textgen_dispatch.go:275 `		return textgen.GenerateWithOptions(ctx, piTextgenOptions(cfg, model, effort), prompt)`
  internal/app/textgen_dispatch.go:296 `		return mcodeGenerateText(ctx, cfg, secrets, model, effort, prompt)`
  internal/app/textgen_dispatch.go:301 `	if target, ok := resolveOpenAICompatible(cfg, secrets, raw, model); ok {`
  internal/app/textgen_dispatch.go:314 `	out, errClaude := textgen.GenerateWithOptions(ctx, textgen.Options{Provider: "claude", Effort: effort, BinaryPath: cfg.Providers.Claude.Bin}, prompt)`
- textgen 层的 CheapestModel 仍是静态 CLI 名称，不读目录价格；旧非流式 HTTP 错误只有格式化文本，没有 APIError 状态类型；现有第三层兜底是未知来源的 CLI 顺序尝试，不是 API 429/5xx 备选。
  internal/textgen/generate.go:24 `func CheapestModel(provider string) string {`
  internal/textgen/generate.go:27 `		return "haiku"`
  internal/textgen/generate.go:29 `		return "gpt-5.4-mini"`
  internal/textgen/generate.go:336 `		return "", fmt.Errorf("openai-compatible textgen: HTTP %d", resp.StatusCode)`
- 已有 mcode 注册生成器服务不依赖 app 的调用方；它只解析 mcode 别名的 Magpie/Anthropic 模型，不能用裸 deepseek model 直接替代全部 API 来源，也不应因原计划措辞删除整个 mcode 分支和已有测试。
  internal/app/mcode_agent.go:81 `	textgen.RegisterGenerator(config.ProviderMcode, func(ctx context.Context, model, effort, prompt string) (string, error) {`
  internal/app/mcode_agent.go:83 `		return mcodeGenerateText(ctx, current, mcodeCurrentSecrets(secrets), model, effort, prompt)`
  internal/app/mcode_agent.go:172 `	return nil, "", fmt.Errorf("没有找到模型 %s 对应的 Magpie 端点，请在设置中连接 Magpie", model)`

### 退役后的调用方式

- 最小选择：API 来源直接 `apisource.Resolve` → 选 Source/模型 → `apisource.NewClient` → 无工具的 `llm.Request` → `Client.Stream(..., nil)` → 拼接最终消息 text。复用 textgen_mcode.go 的轻量生成模式，不创建有工具、会话或审批的 agent。保留 CLI、memory、mcode 注册生成器及原有清洗函数。
  internal/apisource/client.go:22 `func NewClient(src Source, key, model string, opts ClientOptions) llm.Client {`
  internal/app/textgen_mcode.go:20 `	resp, err := client.Stream(ctx, llm.Request{`
  internal/app/textgen_mcode.go:25 `			Content: []llm.Part{{Type: "text", Text: prompt}},`
  internal/app/textgen_mcode.go:27 `	}, nil)`
- NewClient 已处理原生 Anthropic、Magpie Claude 的 Anthropic/Bearer 和普通 OpenAI；也保留来源额外头、DeepSeek 方言与 effort 转换入口。普通 API 默认模型复用退役后引擎的同一规则，避免再写一份 PresetOptions。
  internal/apisource/client.go:27 `	if src.Protocol == ProtocolAnthropic {`
  internal/apisource/client.go:30 `	if src.Magpie && IsClaudeModel(model) {`
  internal/apisource/client.go:33 `	return llm.NewOpenAI(llm.OpenAIConfig{BaseURL: src.BaseURL, APIKey: key, Headers: headers, EchoReasoning: strings.Contains(strings.ToLower(model), "deepseek"), Effort: opts.Effort})`
- 移除 app 对旧非流式函数的调用，不扩大为删除 internal/textgen 的通用 OpenAI 函数；该函数本身不 import 旧包。API 单次请求保留 30 秒上限，备选共用调用方总 deadline，不能给第二次请求重置总预算。
  internal/textgen/generate.go:301 `	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)`

### 原任务 6.3 的最小落地政策

- 明确设计选择：保留用户显式提交模型的优先级。显式给了真实模型就以其为主；未指定模型或为 default 时，自动从可用目录候选中选。未指定提交 provider 时允许在已启用、可调用的来源间比较；显式只指定 provider 时限制在该来源。找不到可靠价格时沿用现有配置/默认模型路径，不将未知价算作免费，不恢复 utility 角色。
  internal/app/textgen_dispatch.go:154 `	if sel := cfg.SourceControl.CommitMessageModel; strings.TrimSpace(sel.Provider) != "" {`
  internal/app/textgen_dispatch.go:67 `	if strings.EqualFold(model, "default") {`
- 候选目录使用 apiSourceCatalog/Magpie 缓存及 remote.ProviderModels 的价格补全；保留来源目录已有 Cost，仅在其为空时取补全价，避免参考价覆盖网关实价。必须从当前 Resolve 得到的启用 API 来源与已确认可用 CLI 收集，不从全体静态 provider 名称推断账号可用。来源关闭、无 Key、占位 default、仅嵌入/无文本能力模型均不参与。CLI 的 SupportsProvider/AvailableProvider 是 CLI/注册生成器判定，不能用它排除全部 API 来源；API 路径由 Resolve、APIKey 和 NewClient 提供能力。
  internal/textgen/capabilities.go:21 `func SupportsProvider(provider string) bool {`
  internal/textgen/capabilities.go:37 `func AvailableProvider(provider string) bool {`
  internal/apisource/source.go:131 `func Resolve(cfg config.Config, secrets config.SecretStore) []Source {`
  internal/remote/models.go:360 `	applyModelsDev(provider, models)`
  internal/modelsdev/catalog.go:79 `	InputModalities []string`
- “最便宜”采用明确、稳定的比较口径：目录每百万 token 的 Input + Output，等权作为一次性短文本的排序代理；同价按来源固定顺序、再按模型 ID。目录非 nil 的零价有效；nil 表示未知，排在已知价之后。Magpie 用网关实价，缺价时才用可查的参考价。当前 wire 目录不含完整文本输出能力，models.dev Info 也只有输入模态；已知非文本模型排除，不能宣称有完整输出能力证明，未知模型仅允许用户显式指定或现有默认兜底。
  internal/remote/wire/types.go:811 ``	Cost *ModelCost `json:"cost,omitempty"` ``
  internal/remote/wire/types.go:816 ``	Input      float64 `json:"input"` ``
  internal/remote/wire/types.go:817 ``	Output     float64 `json:"output"` ``
  internal/modelsdev/catalog.go:76 `	Cost            *Cost // 价格；models.dev 未提供或输入输出价格都为 0 时为 nil`
  internal/app/api_catalog.go:68 `			ContextLimit: model.ContextWindow, Cost: magpieModelCost(model),`
- 模型目录冷启动先做一次有界加载再选；价格或候选不足时按上述默认规则降级。候选保留 provider kind、实际模型 ID 和端点身份，避免同名模型跨端点请求时错发 Key；不为自动选择发探测生成请求。
  internal/app/api_catalog.go:60 `	if !hit {`
  internal/app/api_catalog.go:61 `		warmMagpieModels(target)`
- 同家族备选放 app/fallback.go：`errors.As(err, *llm.APIError)` 后仅 Status==429 或 500<=Status<=599 触发；不依据 Retryable 单独判断，因为网络断流也可能 Retryable。候选排除刚失败的来源+模型组合；按相同价格排序优先找另一个可用来源，再考虑同来源其他同家族模型；最多再请求一次。401/403/404、网络错误、取消、超时、无同家族候选均直接返回，不接上无限/跨家族 CLI 回退。
  internal/agent/llm/types.go:82 `type APIError struct {`
  internal/agent/llm/types.go:83 ``	Status    int    `json:"status"` ``
  internal/agent/llm/openai.go:259 `		return nil, &APIError{Body: err.Error(), Retryable: true}`
- 家族使用模型实际名称而非来源名称：先对明确支持的 deepseek、claude、gpt 等族做保守归一化；已知 family 元数据用于补充无法从名称判定的情况。Claude CLI haiku/sonnet/opus 别名先映射到 claude；先剥离网关路由前缀再判断实际模型名。无法确定家族时不自动切换。按原需求，deepseek* 可互为备选、claude* 可互为备选，不要求精确小版本相同。
  internal/modelsdev/catalog.go:70 `	Family          string`
  internal/remote/models.go:231 `	"default": "claude-sonnet",`
  docs/superpowers/plans/2026-11-08-unified-api-engine.md:360 ``2. 新增 `internal/app/fallback.go`：主模型返回 429/5xx 时，按同家族（`deepseek*` → `deepseek*`，`claude*` → `claude*`）取下一个可用来源，最多一次，事件里提示。``
- 提示最小方案：生成入口从 context 接收可选的、仅本次调用有效的 fallback 通知回调；标题入口和提交入口有 threadID，用 backend.AppendNotice 追加一次“已切换到 …”系统提示，既持久化也推送现有事件，无需新增 JSON-RPC 契约。记忆精简没有 threadID，保留回调出口但不把提示混进生成正文。不得记录错误响应体或 Key。这里需要额外修改 client.go，原计划仅允许 wiring.go/fallback.go 不足以覆盖提交入口。
  internal/app/textgen_wiring.go:158 `		cleaned, err := generateAutoTitle(ctx, dataDir, in)`
  internal/app/client.go:2802 `func (c *Client) GenerateCommitMessage(ctx context.Context, threadID string) (string, error) {`
  internal/remote/memory_backend.go:1663 `func (b *MemoryBackend) AppendNotice(threadID, text string) error {`

## 4. 可分派任务与依赖

建议顺序：T1 与 T2 并行 → T3 → T4 → T5 → T6。T1/T2 的允许文件集合互不重叠；T3 不改 T2 的文件。T4/T5 都需要改 textgen_dispatch.go，必须串行。T6 是最后一步，须在全部替换接线合入后删除目录。各任务的“测试”由主代理在自己的检出执行，本次调研不执行。

### T1：提取普通 API 模型请求

**现状**：通用 ListModels、地址规则、排序和认证在旧包；来源类型已具备 URL 和 Headers。

internal/providers/openai/models.go:28 `func ListModels(ctx context.Context, client *http.Client, baseURL, apiKey string) ([]string, error) {`

**剩余步骤**：

1. 新增 apisource/models.go，实现前述 `ListModels(ctx, client, source, apiKey)`，沿用模型地址归一化、排序去重和有限响应读取，支持 Source.Headers，明确请求超时由调用方上下文控制，错误不回传原始敏感 body。
   internal/providers/openai/models.go:21 `func modelsURL(baseURL string) string {`
2. 新增来源层请求测试，迁移行为断言而非复制旧 harness；覆盖尾斜杠、完整 chat/completions URL、认证/额外头、去重排序、空列表、非 2xx、坏 JSON、超限 body、ctx 取消。
   internal/providers/openai/models_test.go:14 `func TestModelsURL(t *testing.T) {`
3. 更新来源包“只解析不发请求”和 old openai.New 相关注释，避免新职责与包说明矛盾；不在此任务改引擎调用方，不搬旧工具循环或未被使用的通用 ModelCache。
   internal/apisource/source.go:2 `// 本包只做数据解析：不发起请求，也不依赖 app 或 remote 层。`

**允许改动的文件**：internal/apisource/models.go（新增）、internal/apisource/models_test.go（新增）、internal/apisource/source.go（仅包说明和默认 URL 注释）。

**测试**：`go test ./internal/apisource/...`。

**验收标准**：apisource 可独立拉取普通 /models，不依赖旧包、app 或 remote；输入 URL、凭据和额外头来自同一个 Source；有界读和取消可测。

### T2：清理设置、远程目录的元数据依赖

**现状**：设置两处查预设、remote 一处识别预设仍绕过旧包别名；不需要网络或 legacy harness。

internal/providers/openai/presets.go:23 `func PresetByID(id string) (Preset, bool) { return apisource.PresetByID(id) }`

**剩余步骤**：

1. 两处改 apisource.PresetByID，删 openai import；保留 keyring 账号、默认地址和模型行为。
   internal/app/provider_settings.go:77 `	if preset, ok := openai.PresetByID(id); ok {`
   internal/app/provider_settings.go:149 `		preset, ok := openai.PresetByID(spec.id)`
2. remote 判断改 apisource.PresetByID，更新注释为“API 来源/来源目录”，保持 custom-*、magpie..magpie4 的空数组契约与静态 OpenAI 目录。
   internal/remote/models.go:418 `	_, ok := openai.PresetByID(provider)`
   internal/remote/models_test.go:104 `		if len(models) != 0 || models == nil {`
3. 配置注释改指 apisource，不改配置结构和兼容 ID。
   internal/config/config.go:1390 `// key 为 internal/providers/openai 的预设 ID。零值表示未启用。`
   internal/config/config.go:2271 `// ACP Agent ID 与 internal/providers/acp.Agents() 保持一致，预设 ID 与 internal/providers/openai.PresetByID 一致。`

**允许改动的文件**：internal/app/provider_settings.go、internal/app/provider_settings_test.go、internal/remote/models.go、internal/remote/models_test.go、internal/config/config.go（仅上述注释）。

**测试**：`go test ./internal/remote/...`；`go test ./internal/app -run 'Test.*ProviderSetting|TestBuildProviderHarness'`。已有等价覆盖足够时不为 import 替换增加镜像测试。

**验收标准**：这两个生产文件不再 import 旧包；预设设置与自定义/Magpie 空目录契约保持；可与 T1 独立完成。

### T3：引擎脱离 legacy，接管模型缓存与默认值

依赖 T1；与 T2 文件集合互不重叠。

**现状**：newSourceEngineHarness 仍索取完整 legacy；目录、刷新、可用性和默认值都部分取自它。

internal/app/engine.go:39 `func newSourceEngineHarness(legacy harness.Harness, cfg config.Config, secrets config.SecretStore, dataDir string, mcpInjection providerMCP) harness.Harness {`

**剩余步骤**：

1. 改构造入口第一参为 kind；从 Resolve 获取 Source.Label 和来源元数据；新建 api_model_cache.go 承载普通目录/可用性状态，实时解析配置及凭据，保留非阻塞预热、TTL、失败旧值和并发隔离。apiSourceCatalog 第四参仅改名为 fetchedModelIDs，合并缓存结果；Magpie/Anthropic 保留专用目录。
   internal/app/engine.go:57 `		return apiSourceCatalog(current, store, string(kind), ids)`
   internal/app/api_catalog.go:14 `func apiSourceCatalog(cfg config.Config, secrets config.SecretStore, kind string, legacyModelIDs []string) []wire.ProviderModel {`
2. opts.RefreshModels 接新缓存刷新，opts.Available 接来源探测；保留 remote 通用刷新接口和现有 Magpie 元数据请求。
   internal/app/engine.go:71 `		opts.RefreshModels = refresh.RefreshModels`
   internal/app/engine.go:86 `		opts.Available = available.Available`
   internal/remote/refresh_models.go:102 `			if r, ok := h.(interface{ RefreshModels(context.Context) error }); ok {`
3. 删除 resolveSourceClient/sourceEngineDefaultModel 的 legacy 参数；默认值按既有规则解析，普通目录冷启动有界刷新一次；不能让目录读取网络阻塞启动，也不能把 context.Background 的无界等待带到新实现。
   internal/app/engine.go:169 `			modelID = sourceEngineDefaultModel(source, kind, current, legacy)`
   internal/app/engine.go:251 `			_ = refresher.RefreshModels(context.Background())`
4. app.go 的官方和预设分支、custom_endpoints.go 的自定义分支直接传 kind，不再造 Options/New。保留 dataDir fallback、MCP 内置引擎注入、kind 和实时换 Key 行为。
   internal/app/app.go:1824 `		legacy := openai.New(openai.Options{`
   internal/app/app.go:1909 `	return newSourceEngineHarness(openai.New(opts), cfg, secrets, mcodeRuntimeDataDir(), mcpInjection), true`
   internal/app/custom_endpoints.go:65 `	return newSourceEngineHarness(legacy, fallback, secrets, dataDir, mcpInjection)`
5. Magpie 迁移参数/切片改 apisource.Preset，槽位枚举改 apisource.Magpie，删除两个文件的 openai import；保留目标密钥优先、旧凭据删除和幂等性。
   internal/app/custom_endpoints.go:432 `		preset, ok := openai.Magpie(slot)`
   internal/app/custom_endpoints.go:491 `func migrateLegacyMagpieSecret(secrets config.SecretStore, preset openai.Preset) error {`
6. 更新 engine_test.go 所有旧包及 nil legacy 参数调用，以来源 Label 取代 legacy.DisplayName 断言；补缓存及普通自定义端点无配置模型时 /models 取首项的用例。保留 Magpie 默认、能力、费用和换 Key 测试；api_catalog 测试中的“legacy”字符串仅是普通 ID，可按可读性改名。
   internal/app/engine_test.go:117 `	if got, want := h.(interface{ DisplayName() string }).DisplayName(), legacy.DisplayName(); got != want {`
   internal/app/engine_test.go:540 `func TestSourceEngineUsesRotatedSecret(t *testing.T) {`
   internal/app/api_catalog_test.go:131 `	rows = apiSourceCatalog(cfg, secrets, "deepseek", []string{"legacy", "configured", "legacy"})`

**允许改动的文件**：internal/app/engine.go、internal/app/engine_test.go、internal/app/api_catalog.go、internal/app/api_catalog_test.go、internal/app/api_model_cache.go（新增）、internal/app/api_model_cache_test.go（新增）、internal/app/app.go、internal/app/custom_endpoints.go、internal/app/custom_endpoints_test.go。

**测试**：`go test ./internal/app -run 'TestSourceEngine|TestAPISourceCatalog|TestEngineReasoningEffort|TestCustomEndpoint|TestCustomEndpoints|TestMigrateLegacyMagpie|TestAPISourceModelCache'`；缓存状态用可控时钟/同步请求测试成功过期、失败保留、并发刷新、URL/Key 变更和旧结果隔离；随后主代理执行 app/remote 整包检查。

**验收标准**：无 legacy harness 参数或 openai.New/PresetOptions；源目录和默认模型一致；providers.list 不等待 /models；来源停用/改 Key/改 URL 后不使用旧来源结果；旧 Magpie 迁移保持；engine_test.go 不再 import 旧包。

### T4：API 一次性生成改接来源客户端

依赖 T3 的来源缓存和默认模型规则；T4 完成之前不实施 T5 的接线修改。

**现状**：textgen_dispatch.go 重新解析配置并忽略 Source.Protocol/Headers；mcode 别名已有轻量 Client.Stream 生成实现。

internal/app/textgen_dispatch.go:99 `func resolveOpenAICompatible(cfg config.Config, secrets config.SecretStore, provider, model string) (openAICompatTarget, bool) {`

**剩余步骤**：

1. API 路径通过 Resolve 查启用来源，复用 T3 默认模型规则；删 openAICompatTarget/resolveOpenAICompatible 的手写 URL/凭据/预设解析，去掉 openai import。自定义 kind 比较沿用原大小写容忍规则，Key 不回退官方环境变量。
   internal/app/textgen_dispatch.go:133 `		if !strings.EqualFold(config.CustomEndpointProviderKind(ep), provider) {`
   internal/apisource/source.go:257 `		APIKeyAccounts: []string{config.CustomEndpointSecretAccount(ep.ID)},`
2. 用 NewClient + 无工具 Stream 请求；在 textgen_mcode.go 抽取小型共享请求/拼文本 helper，让原 mcodeGenerateText 继续调用原 resolver，再交给 helper。API 请求有 30 秒预算，effort 经现有转换器；保持 CLI、memory、mcode 注册入口和输出清洗。
   internal/app/textgen_mcode.go:16 `	client, modelID, err := resolveMcodeClient(cfg, secrets, model, "")`
   internal/app/textgen_mcode.go:20 `	resp, err := client.Stream(ctx, llm.Request{`
   internal/app/engine.go:279 `func engineReasoningEffort(baseURL string) func(model, effort string) (string, bool) {`
3. API 调用错误原样保留 llm.APIError，给 T5 依据状态切换；不通过字符串解析 HTTP 码；更新现有非流式 JSON 假服务为 SSE/Anthropic 流式响应，改 resolver 测试断言。
   internal/app/textgen_dispatch_test.go:138 ``		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":" hi "}}]}`))``
4. 覆盖普通自定义、预设额外头、DeepSeek effort、Anthropic 来源、Magpie Claude 走 Messages、禁用/缺 Key、默认模型和完整 URL；不删除 textgen_mcode_test.go，不扩大删除 internal/textgen 通用生成实现。
   internal/app/textgen_mcode_test.go:22 `func TestMcodeGenerateTextViaDispatch(t *testing.T) {`
   internal/apisource/client.go:31 `		return llm.NewAnthropic(llm.AnthropicConfig{BaseURL: magpieinject.AnthropicBaseURL(src.BaseURL), APIKey: key, Headers: headers, BearerAuth: true})`

**允许改动的文件**：internal/app/textgen_dispatch.go、internal/app/textgen_dispatch_test.go、internal/app/textgen_mcode.go、internal/app/textgen_mcode_test.go。

**测试**：`go test ./internal/app -run 'TestGenerateText|TestResolveOpenAICompatible|TestMcodeGenerateText|TestCLITextgen|TestPiTextgen|TestTextgenModelSelection'`（重命名测试后相应更新过滤）；`go test ./internal/textgen/... ./internal/agent/llm/...`。

**验收标准**：app 的 API 一次性生成不再经过旧包或单独 Chat Completions 实现；同一 Source 决定协议、Key、Headers 和模型；请求无工具/会话；CLI 与 mcode 别名行为仍可用；上游状态可由 errors.As 识别。

### T5：按目录价格选择与一次同家族备选

依赖 T4；覆盖标题、提交信息及共用入口，明确保留显式模型优先。

**现状**：只有静态 CheapestModel，未实现目录排序、错误分类备选、切换提示。

internal/textgen/generate.go:24 `func CheapestModel(provider string) string {`

**剩余步骤**：

1. 新增 app/fallback.go，提供纯候选排序/家族匹配和最多两次调用的执行器；把本文的 Input+Output 口径、nil 价、显式模型优先、同价稳定排序写成测试。GenerateText 在已有共用入口接自动选择，不只改标题；textgenModelSelection 保留现有配置语义。
   internal/app/textgen_dispatch.go:152 `func textgenModelSelection(cfg config.Config) (provider, model, effort string) {`
   internal/app/client.go:3538 `	provider, model, effort := textgenModelSelection(cfg)`
2. 候选收集使用当前启用来源/CLI 可用性与目录能力；必要时用 T3 有界刷新补冷缓存；API 不用 CLI 的 SupportsProvider 错判。网关模型按实际 ID 判 family，过滤非文本/占位模型；未知家族不自动备选，未知价不当零价。
   internal/textgen/capabilities.go:27 `	case "claude", "codex", "pi", "memory":`
   internal/app/api_catalog.go:61 `		warmMagpieModels(target)`
3. 只针对 APIError.Status 的 429/5xx，选同家族下一个可调用来源/模型，最多再试一次，共享总 ctx；备选失败即返回错误，不能再次进入旧的跨 provider 第三层回退。网络错误、认证错误及取消不触发备选。
   internal/agent/llm/types.go:83 ``	Status    int    `json:"status"` ``
   internal/app/textgen_dispatch.go:314 `	out, errClaude := textgen.GenerateWithOptions(ctx, textgen.Options{Provider: "claude", Effort: effort, BinaryPath: cfg.Providers.Claude.Bin}, prompt)`
4. 增加 context 内的单次切换回调；自动标题、手动重生成标题与提交信息入口关联 threadID，用 AppendNotice 推送一次提示，不改变生成正文。为无 threadID 的记忆精简保留安静调用，不借用任意线程。
   internal/app/textgen_wiring.go:158 `		cleaned, err := generateAutoTitle(ctx, dataDir, in)`
   internal/app/textgen_wiring.go:227 `	title, err := generateAutoTitle(ctx, dataDir, collectAutoTitleInput(hist, ""))`
   internal/app/client.go:2831 `	out, err := generateText(ctx, cfg, c.textgenSecrets(), provider, model, effort, prompt)`
   internal/remote/memory_backend.go:1663 `func (b *MemoryBackend) AppendNotice(threadID, text string) error {`
5. 测试并发调用通知互不串线；一次切换只提示一次；429/500/504/529 只发第二次请求；401/403/404、ctx 取消、断流、无同族不切换；备选同样失败不出现第三次请求。清理标题/提交调用点“utility 角色”的旧注释。
   internal/app/textgen_wiring.go:195 `	// 与 Git 提交信息生成共用同一个模型配置（设置页指定模型优先，否则沿用 utility 角色模型），`
   internal/app/client.go:2827 `	// 与会话标题自动生成共用同一套模型选择：设置页指定模型优先，否则沿用 utility 角色模型。`

**允许改动的文件**：internal/app/fallback.go（新增）、internal/app/fallback_test.go（新增）、internal/app/textgen_dispatch.go、internal/app/textgen_dispatch_test.go、internal/app/textgen_wiring.go、internal/app/textgen_wiring_test.go、internal/app/client.go（仅上述生成入口与注释）、internal/app/textgen_fallback_wiring_test.go（新增）。

**测试**：`go test ./internal/app -run 'Test.*Fallback|TestTextgenModelSelection|TestGenerateAutoTitle|TestGenerateText|Test.*CommitMessage'`；主代理随后执行 `go test ./internal/app/... ./internal/textgen/...`。测试用本地假服务/注入客户端及内存目录，不请求真实供应商。

**验收标准**：自动模式选择当前目录最低已知排序价的可调用文本模型；显式选择仍优先；API 限流/服务故障最多切一次同家族；其他错误不切；标题/提交时间线收到一次安全提示；未知价格、冷缓存和无候选有明确默认兜底。CLI 进程错误缺少结构化 HTTP 状态，不据 stderr 猜测 429，备选只作用于取得 APIError 的请求。

### T6：最后删除旧目录并做整体验收

依赖 T1–T5 全部完成，最后执行。

**现状**：旧目录仍保留完整工具循环、MCP、SSE、会话历史及模型请求；只有模型请求行为需要提前提取，其余随目录删除。

internal/providers/openai/openai.go:93 `func New(opts Options) *Harness {`
internal/providers/openai/presets.go:26 `func PresetOptions(p Preset, secrets config.SecretStore, baseURLOverride string, codingPlan bool) Options {`

**剩余步骤**：

1. 确认本清单 5 个生产 import 和 engine_test.go import 都已清零；默认模型、可用性、刷新、生成协议和 Magpie 迁移都已有替代。残留引用应交回对应前置任务，不能在删除任务临时加入新的兼容壳。
   internal/app/engine_test.go:21 `	"mcode/internal/providers/openai"`
2. 删除 internal/providers/openai 整个目录，包括 models.go、presets.go、openai.go、tools.go、mcp.go、sse.go 及全部包内测试。保留历史记录中的旧工具名、官方 openai provider ID、llm.NewOpenAI 和 internal/textgen 的通用 API。
   internal/providers/openai/tools.go:40 `			Name: "read_file",`
   internal/apisource/client.go:33 `	return llm.NewOpenAI(llm.OpenAIConfig{BaseURL: src.BaseURL, APIKey: key, Headers: headers, EchoReasoning: strings.Contains(strings.ToLower(model), "deepseek"), Effort: opts.Effort})`
3. 主代理执行有范围的源码 import/符号搜索，确认无编译引用，再执行 `go build ./...` 和全套测试；结果与失败项记录到交付说明，不把本次“未执行”当通过。
   docs/superpowers/plans/2026-11-08-unified-api-engine.md:328 ``- `go build ./...` 与 `go test ./...` 通过，`internal/providers/openai` 目录不存在。``

**允许改动的文件**：仅删除 internal/providers/openai/ 下现有全部文件。前置任务未清理的外部文件先回对应任务处理。

**测试**：主代理运行 `go build ./...`、`go test ./...`；使用 `rg -n 'mcode/internal/providers/openai' --glob '*.go' internal cmd` 确认无引用；检查旧目录不存在。源码注释引用由 T1/T2 清理，历史 plans 中的路径不作为构建失败。

**验收标准**：旧目录不存在，Go 源码无旧包 import/调用，`go build ./...` 与 `go test ./...` 通过；旧来源 kind 的线程可继续；新目录刷新和默认模型可用；一次性生成主/备选均走正确来源及协议。

## 注意事项与未完成部分

- 本次只交付计划。所有构建和测试结果均待主代理执行；未验证真实供应商、GUI 及冷启动耗时。
- 本计划选择直接使用 apisource.NewClient 的轻量请求，保留 mcode 注册生成器；没有采用为每个来源额外注册全局生成器的方案。
  internal/apisource/client.go:22 `func NewClient(src Source, key, model string, opts ClientOptions) llm.Client {`
- Input+Output 等权价格是明确的排序代理，不是对任意实际 token 比例的最低费用保证。当前公开目录不足以精确证明所有模型的文本输出能力；实施时应保守过滤、用测试锁定未知模型的兜底行为，不扩大 wire 契约。
  internal/modelsdev/catalog.go:79 `	InputModalities []string`
- 提示通过现有系统消息推送，需修改 client.go 的提交入口及 wiring.go 的两个标题入口；记忆精简无 threadID，不产生会话提示。该行为是对原计划“事件里提示”的最小解释。
  internal/remote/memory_backend.go:1662 `// AppendNotice 向会话时间线追加一条系统提示条目，并进行持久化与增量推送。`
