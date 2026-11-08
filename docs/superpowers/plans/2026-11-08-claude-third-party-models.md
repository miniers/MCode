# Claude 第三方 API 模型目录诊断与修复计划

## 结论与证据

1. **根因尚不能唯一确定**。已确认存在独立于认证方式的目录刷新缺陷：选择器的“重新探测”只重读缓存，失败后 10 分钟内不会重新启动握手；设置页可能固定住探测完成之前的空快照。若后台握手曾成功但页面仍为空，后者可以解释现象；若握手失败，需先取得失败原因，不能把失败直接归因于未登录或第三方 API。

   internal/ui/composer.go:2002 `		OnRefreshCatalog:  s.refreshProviders,`

   internal/ui/model.go:775 `	go s.reloadProviderCatalog()`

   internal/ui/model.go:781 `	providers, err := s.client.Providers(context.Background())`

   internal/providers/modelcache/cache.go:11 `const retryDelay = 10 * time.Minute`

   internal/providers/modelcache/cache.go:37 `	if len(c.models) == 0 && !c.fetching && (c.failedAt.IsZero() || time.Since(c.failedAt) >= retryDelay) {`

   internal/ui/settings_models_data.go:67 `	if len(ps.providers) > 0 {`

   internal/ui/settings_models_data.go:143 `				ps.providers = r.providers`

   页面首次读取如果拿到含 Claude 空目录的供应商列表，会优先保留 `ps.providers`；聊天侧后台补拉更新 `chat.providers` 本身不会递增页面目录版本。因此设置页详情和添加收藏可能持续使用旧快照，即使后台已成功。该问题取决于时序，并非已确认的用户现场根因。

2. **握手不以 `loginPresent()` 为前提**。首次 `CachedCatalog` 调用由缓存异步发起握手；`loginPresent` 只在握手出错、目录为空时决定提示文本。因此第三方用户即使 CLI 握手因路径、超时或输出等其他原因失败，也可能被误提示为“未登录 Claude Code”。`readLogin` 的检测还按进程缓存。握手返回空目录也会被缓存当成失败。

   internal/providers/claude/models.go:117 `func CachedCatalog() modelcache.Snapshot[Model] {`

   internal/providers/claude/models.go:93 `var catalogFetch = fetchModels`

   internal/providers/claude/models.go:158 `	if snap.Err != nil && !loginPresent() {`

   internal/providers/modelcache/cache.go:69 `		err = errors.New("模型目录为空")`

3. **自动探测和强制刷新使用的二进制路径不同**。自动探测固定查找默认 `claude`，强制刷新则使用 harness 的配置路径。用户若仅在设置中配置了自定义 CLI 路径，自动探测可能找不到可执行文件，或找到不同版本；这与第三方网关无必然关系。

   internal/providers/claude/models.go:173 `	return fetchModelsWithBinary(ctx, DefaultBinary)`

   internal/providers/claude/claude.go:128 `func (h *Harness) CatalogBinary() string { return h.opts.Binary }`

   internal/remote/refresh_models.go:44 `			refresh = func(ctx context.Context) error { return claude.RefreshModels(ctx, binary) }`

4. **环境与握手**。`buildEnv` 只移除继承环境中的 `PWD`，再添加工作目录及两个 Claude 开关；不会主动删除或设置 `ANTHROPIC_BASE_URL`、`ANTHROPIC_AUTH_TOKEN` 或 `ANTHROPIC_API_KEY`，也不读取 `settings.json` 的 env。握手由 CLI 使用其用户配置读取 settings；但父进程已有同名变量、`CLAUDE_CONFIG_DIR`、home 工作目录及项目设置可能使实际运行环境和隔离实验不同，不能仅凭源码断言 CLI 的优先级或现场设置实际生效。

   internal/providers/claude/claude.go:390 `		if !strings.HasPrefix(e, "PWD=") {`

   internal/providers/claude/models.go:81 `	"--setting-sources", "user,project,local",`

   internal/providers/claude/models.go:199 `	cmd.Dir = home`

   internal/providers/claude/models.go:201 `	cmd.Env = buildEnv(loginenv.Environ(), home, nil)`

   internal/providers/claude/models.go:88 `const handshakeTimeout = 20 * time.Second`

5. **实际登录门槛在启动可用性判断，而非模型缓存**。`AuthNotRequired` 只查看传入的进程环境，不直接读取 settings.json；当令牌仅在 settings.json 时，仍会调用 CLI 登录态检测。如果该检测明确返回未登录，启动判断将 Claude 视为不可用，并可能不注册其 harness。若 CLI 超时或未上报则按可用处理。用户现场已经显示连接，不能直接断定是此分支；settings 详情的登录态同样只控制状态展示。

   internal/providers/claude/auth.go:114 `func AuthNotRequired(env []string) bool {`

   internal/app/app.go:288 `	if claude.AuthNotRequired(claudeEnv) {`

   internal/app/app.go:296 `	if status, known := claudeProbeReadStatus(authCtx, claudeBin, claudeEnv); known && !status.LoggedIn {`

   internal/app/app.go:991 `	claudeAvailable := claudeErr == nil && claudeAuthErr == nil`

   internal/app/app.go:1041 `				if claudeAvailable {`

   internal/app/claude_auth_status.go:36 `	if claude.AuthNotRequired(env) {`

   internal/ui/settings_page_providers_agents.go:828 `	loggedOut := strings.TrimSpace(in.LoginState) == ProviderLoginStateLoggedOut`

   `PlanLimits` 属于额度读取路径，所查模型目录调用链未将它作为过滤条件。

6. **三个界面共享目录**。`Client.Providers` 调用后端 `ListProviders`；后端仅对可用 provider 取目录，Claude 目录来自 `CachedModels` 的转换。详情浮层及添加收藏通过 `modelsPageLibrary` 使用页面快照；添加收藏跳过关闭或目录为空的供应商。聊天选择器用聊天侧供应商快照，空目录时只在可用且有原因、非搜索态显示提示；没有按 `ProviderLoginState` 或额度过滤已返回的模型。

   internal/app/client.go:381 `	res, err := c.backend.ListProviders(ctx)`

   internal/remote/memory_backend.go:8531 `		if available {`

   internal/remote/memory_backend.go:8532 `			models = ProviderModels(k)`

   internal/remote/models.go:532 `		src = mergedClaudeModels(claude.CachedModels())`

   internal/ui/provider_sheet_layout.go:38 `		lib = modelsPageLibrary(c, st)`

   internal/ui/settings_page_providers_agents.go:1719 `	view.models, view.hasModels = providerModelsFor(lib, id)`

   internal/ui/settings_page_providers_agents.go:1782 `					if len(view.models.Models) == 0 {`

   internal/ui/settings_page_models.go:586 `	lib := modelsPageLibrary(c, st)`

   internal/ui/settings_page_models.go:615 `		if providerDisabled(enabled, provider.ID) || len(provider.Models) == 0 {`

   internal/ui/model_picker.go:2577 `			if !searching && prov.Available && prov.UnavailableReason != "" {`

   internal/ui/model.go:766 `var providerCatalogRetryDelays = []time.Duration{5 * time.Second, 15 * time.Second, 40 * time.Second}`

7. **Magpie 是独立的模型来源**。路由激活时以勾选模型作为目录行；未激活则返回 CLI 基础目录，普通第三方网关不会自动获得 Magpie 勾选行。Magpie 配置计划会写入档位模型与 `modelPicker`；`ANTHROPIC_CUSTOM_MODEL_OPTION` 是用于判断是否覆盖内置选项的已有用户变量，并非这里写入的模型目录。CLI 仍可能从它自己的 settings 或握手输出模型，不能断言普通网关一定没有模型。

   internal/app/agent_magpie.go:149 `	if !routing.Active() {`

   internal/remote/models.go:437 `	rows, active := magpieCatalogRows(provider)`

   internal/remote/models.go:448 `		return base`

   internal/magpiecfg/claude.go:94 `		key := "env." + TierEnv(tier)`

   internal/magpiecfg/claude.go:155 `	if own, _ := GetJSON(path, "env."+claudeCustomOptionEnv); strings.TrimSpace(own) == "" {`

## 修复步骤与测试

1. 将选择器“重新探测”接到 `Client.RefreshProviders`，使用真正的强制刷新并把返回快照更新到聊天状态；保留错误提示和并发保护。它会刷新所有动态供应商，评估现有 60 秒调用预算及部分失败时仍返回的快照，不要把普通后台补拉全部改成强制刷新。

   internal/app/client.go:390 `	refreshErr := c.backend.RefreshProviderModels(ctx)`

   internal/ui/settings_page_models.go:93 `		providers, err := chat.client.RefreshProviders(ctx)`

   测试：模拟首次失败进入退避，点击选择器重试后确认 fetch 再次执行并显示新目录；部分供应商失败仍保留成功结果和错误反馈。

2. 让后台目录更新同步通知设置页，使详情和添加收藏使用最新目录，同时保留收藏、偏好及加载期间旧快照；避免每帧发起请求和旧异步结果覆盖新版本。

   internal/ui/settings_models_data.go:27 `	catalogVersion := st.modelCatalogVersion`

   internal/ui/settings_models_data.go:143 `				ps.providers = r.providers`

   测试：先加载 Claude 空目录，再模拟后台握手成功及聊天快照更新，保持设置页打开，验证详情及添加收藏均出现模型，收藏不丢失；验证乱序响应不会倒退。

3. 自动握手使用与运行 harness 一致的 `CatalogBinary`，处理路径变更后已有缓存及失败退避的失效；避免不同路径复用过期目录。覆盖默认路径、自定义路径、路径修改和手动刷新。

   internal/providers/claude/models.go:173 `	return fetchModelsWithBinary(ctx, DefaultBinary)`

4. 在 Claude 启动可用性、设置页登录态和目录错误提示中，识别有效的第三方配置；优先复用现有 settings env 读取与环境优先级，尊重 `CLAUDE_CONFIG_DIR`，不能打印或泄露令牌。保留确实未登录用户的提示；握手其他错误不得一概误报未登录。注意第三方配置只用于免 Claude.ai 登录，不代表 CLI 或网关一定可用。

   internal/providers/claude/gateway.go:160 `// gatewayConfig 合并进程环境与 settings.json 的 env 块（进程环境优先）。`

   internal/providers/claude/models.go:158 `	if snap.Err != nil && !loginPresent() {`

   测试：仅 settings.json 配置 token 或 API key、环境配置、空值、缺失、显式覆盖、非默认配置目录及真正无认证；模拟握手失败验证不会错误提示登录；模拟登录态明确未登录验证已配置网关仍可注册并请求目录。

5. 先定位真实握手错误再决定是否需要额外网关模型发现机制；不要直接伪造模型或依赖 Magpie。测试握手 JSON 解析、空目录、超时和自定义 binary；比较隔离 CLI 与 mcode 的 binary、工作目录和无敏感值的环境键，避免输出密钥。

## 验证状态

本次按工作方式要求未运行构建、测试、lint 或任何验证命令，也未操作真实 `~/.claude`。用户提供的隔离 CLI 握手成功是既有实验结果，本次未复现；因此无法确认用户现场具体失败原因。以上行号与源码来自只读检查。
