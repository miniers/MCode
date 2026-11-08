# Magpie 网关变更后刷新模型快照

日期：2026-11-08。状态：完成静态诊断，待实现与验证。本次仅新增本方案文件；未修改代码，未运行构建、测试、lint 或其他验证。

## 结论

- **已证实的 bug：网关新增、编辑、删除、启停后，只重读 `piMagpie`，没有重读模型矩阵实际使用的 `agentMagpie`。** 已加载的空端点快照不会因重绘而重拉。因此网关行可以显示 7 个模型，矩阵同时显示“网关没有返回模型”。编辑和删除也会留下旧模型或旧网关。
- **选择器需要区分正常过滤与刷新问题。** “在模型选择器中显示”关闭时，只隐藏独立直连 Magpie provider；不会隐藏 mcode、Claude、Codex、pi 分组里的注入模型。网关返回模型也不等于已经勾选模型，新增网关不会自动把全部模型加入这些分组。
- **不能把当前代码概括为“选择器完全没有刷新”或“后端仅启动时构建端点”。** 保存已经同步刷新一次 `chat.providers`，并调用 `st.refreshModels()` 强制刷新后端目录。问题是网关变更没有复用完整的 Magpie 快照刷新和延后目录重读链路，且矩阵漏刷会让用户无法正常完成勾选。现有代码不足以单独证明“开启显示、已勾选且目录刷新成功后，仍必须重启”的另一条独立根因。该组合需要主代理实测。

最小修复是复用现有加载、重读与强制刷新函数，把网关变更后的刷新链补齐；不重建运行时系统，不增加事件总线、常驻轮询、缓存层，也不自动勾选模型。

## 代码证据与数据流

### 1. 添加网关、保存配置的完整路径

添加按钮打开空 ID 的网关 sheet。

internal/ui/settings_page_magpie.go:55 `openMagpieGatewaySheet(st, "")`

sheet body 通过 `magpieOpenGatewayDraft` 打开 Magpie 范围的自定义端点草稿。

internal/ui/settings_magpie_data.go:147 `openCustomEndpointForm(st, nil, endpointFormScopeMagpie)`

新草稿默认启用，并通过 scope 设置 Magpie 标记。

internal/ui/settings_providers.go:96 `st.endpoints.editMagpie = scope == endpointFormScopeMagpie`

internal/ui/settings_providers.go:99 `st.endpoints.editEnabled = true`

网关表单的“保存”和首次连接的“连接”共用保存入口。

internal/ui/settings_magpie_hub.go:513 `saveCustomEndpoint(st)`

UI 保存函数读取草稿后，调用桌面 Client，随后重读自定义端点列表。

internal/ui/settings_providers.go:293 `saved, err := client.SaveCustomEndpoint(context.Background(), ep, apiKey, webKey)`

internal/ui/settings_providers.go:303 `list, lErr := client.CustomEndpoints(context.Background())`

实际持久化入口是 app 的 `SaveCustomEndpoint`，不是 `internal/magpiecfg/connect.go`。后者用于接管 agent 全局配置文件，与新增网关配置不是同一条路径。

internal/app/custom_endpoints.go:258 `if err := config.Update(c.dataDir, func(latest *config.Config) error {`

internal/app/custom_endpoints.go:266 `latest.Providers.Custom = append(latest.Providers.Custom, epCfg)`

非空 API Key 保存到 SecretStore；编辑留空则保留已有凭据。

internal/app/custom_endpoints.go:214 `if err := c.secrets.Set(account, trimmedKey); err != nil {`

保存成功就注册或替换该端点的 harness；禁用时注销，删除也注销。不存在“新增端点只在下次启动注册”的证据。

internal/app/custom_endpoints.go:283 `c.backend.RegisterHarness(newCustomEndpointHarness(epCfg, c.secrets, c.dataDir, c.mcp))`

internal/app/custom_endpoints.go:287 `c.backend.UnregisterHarness(config.CustomEndpointProviderKind(epCfg))`

internal/app/custom_endpoints.go:408 `c.backend.UnregisterHarness(config.CustomEndpointProviderKind(removed))`

### 2. 网关行的模型数量来自 piMagpie

网关行调用 `magpieEndpointsFor(st, "pi")`，该分支读取独立的 `st.piMagpie.endpoints`。

internal/ui/settings_magpie_data.go:162 `endpoints, _, _, _, _ := magpieEndpointsFor(st, "pi")`

internal/ui/settings_pi_magpie.go:286 `endpoints = append([]PiMagpieEndpoint(nil), st.piMagpie.endpoints...)`

数量是匹配端点的 `Models` 长度，不是已勾选数量，也不是 Composer 目录长度。

internal/ui/settings_magpie_hub.go:82 `Text: fmt.Sprintf("已连接 · %d 个模型", len(match.Models)),`

保存成功明确刷新这份状态；异步请求完成后替换 endpoints 并标记 loaded。

internal/ui/settings_providers.go:318 `st.loadPiMagpieEndpoints()`

internal/ui/settings_pi_magpie.go:38 `endpoints, err := client.PiMagpieEndpoints(context.Background())`

internal/ui/settings_pi_magpie.go:49 `st.piMagpie.endpoints = endpoints`

后端每次读取当前配置，再拉启用网关的全部可选模型，不按 pi 勾选过滤。

internal/app/pi_magpie.go:406 `cfg, ok := c.loadConfig()`

internal/app/pi_magpie.go:415 `targets := piMagpieAllTargets(cfg, secrets)`

internal/app/pi_magpie.go:430 `models, err := magpie.FetchModels(fetchCtx, target.baseURL, target.apiKey)`

### 3. 矩阵读取另一份 agentMagpie，保存后没有刷新

矩阵固定有 mcode、claude、codex、pi 四种列候选，mcode 始终保留，其余按安装与启用状态筛选。

internal/ui/settings_magpie_matrix.go:15 `var magpieMatrixAgents = []string{magpieBuiltinAgent, "claude", "codex", "pi"}`

`magpieMatrixData` 的每一列，包括 pi 列，都读取 `st.agentMagpieFor(agent).setting.Endpoints`，不会从 `st.piMagpie.endpoints` 取得新模型。

internal/ui/settings_magpie_matrix.go:95 `state := st.agentMagpieFor(agent)`

internal/ui/settings_magpie_matrix.go:97 `endpoints := make([]PiMagpieEndpoint, len(state.setting.Endpoints))`

首次未加载才自动拉取。先前已经成功加载的空列表会一直保留。

internal/ui/settings_magpie_matrix.go:104 `if !loaded && !loading && loadErr == "" {`

另一个确保加载的入口也明确跳过 loaded 状态。

internal/ui/settings_agent_magpie.go:541 `if loading || loaded || errMsg != "" {`

实际拉取 agent 快照使用独立的 Client 方法，而不是 pi 端点方法。

internal/ui/settings_agent_magpie.go:86 `setting, err := client.AgentMagpie(context.Background(), agent)`

internal/ui/settings_agent_magpie.go:111 `state.setting = setting`

后端该方法同样读取最新配置和全部可用网关模型，所以重读它即可补齐矩阵，无需重建后端。

internal/app/agent_magpie_settings.go:37 `cfg, ok := c.loadConfig()`

internal/app/agent_magpie_settings.go:46 `targets := magpieinject.Targets(cfg, secrets, nil)`

internal/app/agent_magpie_settings.go:71 `models, err := magpie.FetchModels(fetchCtx, target.BaseURL, target.APIKey)`

当前保存只额外作废 hub 的 claude/codex/cline 快照；这些字段与 `agentMagpie` 是不同结构。

internal/ui/settings_providers.go:319 `markMagpieHubClaudeStale(st)`

internal/ui/settings_magpie_hub.go:38 `st.magpieHub.claudeLoaded = false`

internal/ui/settings_magpie_hub.go:39 `st.magpieHub.codexLoaded = false`

internal/ui/settings_magpie_hub.go:40 `st.magpieHub.clineLoaded = false`

删除和启停沿用了相同的漏刷路径。

internal/ui/settings_providers.go:379 `st.loadPiMagpieEndpoints()`

internal/ui/settings_providers.go:443 `st.loadPiMagpieEndpoints()`

### 4. allLoaded/loadErrs 为什么没有阻止错误空态

`renderMagpieModelMatrix` 一方面通过 `magpieEndpointsFor` 读加载状态；pi 分支读 piMagpie。另一方面又检查每个 agentMagpie 的 loaded/loading/err。两份快照都可以是“加载成功”，但数据属于不同时间。

internal/ui/settings_magpie_matrix.go:302 `_, loading, loaded, loadErr, _ := magpieEndpointsFor(st, agent)`

internal/ui/settings_magpie_matrix.go:306 `if state.loading || !state.loaded || state.err != "" {`

`loadErrs` 保存加载错误；`matrixErrs` 保存勾选保存错误。两者均不能识别成功但过期的空快照。

internal/ui/settings_magpie_matrix.go:320 `loadErrs[agent] = "加载失败：" + loadErr`

internal/ui/settings_magpie_matrix.go:323 `matrixErrs[agent] = matrixErr`

当旧 agent 快照都已 loaded、无 error、搜索为空，而矩阵数据仍为空时，恰好满足错误空态条件。

internal/ui/settings_magpie_matrix.go:378 `if len(agents) > 0 && allLoaded && len(loadErrs) == 0 && strings.TrimSpace(*query) == "" {`

internal/ui/settings_magpie_matrix.go:379 `ui.Text(c, "网关没有返回模型").`

因此应修刷新链，不应删除这段空态或把 loaded 强行改成 true。

### 5. Composer 目录与现有刷新路径

通用选择器的 provider 目录来自 chatState，与设置页两种 Magpie 端点快照都不同。

internal/ui/model_picker_catalog.go:30 `cat.Providers = append([]ProviderInfo(nil), s.providers...)`

普通端点刷新函数重读 `client.Providers`，直接替换 chat 的目录。

internal/ui/settings_providers.go:215 `providers, err := client.Providers(context.Background())`

internal/ui/settings_providers.go:226 `chat.providers = providers`

新增/编辑已经调用这个函数。保存末尾还调用强制刷新；不能漏看这行后断言后端目录从未更新。

internal/ui/settings_providers.go:304 `refreshCustomEndpointProviders(st, client)`

internal/ui/settings_providers.go:321 `st.refreshModels()`

强制刷新通过 `RefreshProviders` 更新动态目录，再经有序应用入口更新 chat 和设置页目录版本。

internal/ui/settings_page_models.go:94 `providers, err := chat.client.RefreshProviders(ctx)`

internal/ui/settings_page_models.go:96 `chat.applyProviderCatalog(seq, providers, st)`

internal/app/client.go:390 `refreshErr := c.backend.RefreshProviderModels(ctx)`

现有 Magpie 保存刷新函数则立即读一次目录，并按既定延迟补读，处理后端异步目录落地。

internal/ui/settings_pi_magpie.go:227 `refreshCatalogProviders(st, client)`

internal/ui/settings_pi_magpie.go:229 `for _, delay := range piMagpieCatalogRefreshDelays {`

internal/ui/settings_pi_magpie.go:240 `st.catalogStale = true`

internal/ui/settings_pi_magpie.go:242 `refreshCustomEndpointProviders(st, client)`

网关变更尚未使用这条完整链路；新增/编辑之外的删除和启停还没有 `st.refreshModels()`。

### 6. 后端不是一次性启动快照，但 pi 有缓存

启动时安装的是回调，回调运行时仍重新读取配置。

internal/app/app.go:1279 `remote.SetPiMagpieInjection(piMagpieInjection)`

internal/app/app.go:1282 `remote.SetMagpieCatalog(magpieCatalogRows)`

internal/app/agent_magpie.go:24 `cfg, err := config.Load(dataDir)`

内置引擎来源的模型目录同样读取当前 mcode 勾选。

internal/app/engine.go:47 `opts.ModelIDs = func() []string { return mcodeEndpointModelIDs(cfg, endpointID) }`

internal/app/engine.go:156 `setting := config.MagpieAgentSetting(mcodeCurrentConfig(cfg, nil), config.ProviderMcode)`

`ListProviders` 每次复制当前注册表，缺少静态目录时再读 harness 的 ModelIDs。

internal/remote/memory_backend.go:8550 `harnesses[k] = h`

internal/remote/memory_backend.go:8628 `for _, id := range mi.ModelIDs() {`

pi 确实有可保留六小时的 CLI 目录缓存，作废不是“必须重建整个 provider”。现有强制刷新已经能重载它。

internal/remote/cli_catalog.go:21 `cliCatalogFreshness = 6 * time.Hour`

internal/remote/refresh_models.go:55 `return piCatalog.reload(ctx, func(ctx context.Context) ([]wire.ProviderModel, error) {`

保存 pi 勾选还有已有的专用缓存作废入口。该入口服务勾选保存，不需要为此次网关修复另造一份。

internal/app/pi_magpie.go:483 `remote.InvalidatePiCatalog()`

### 7. 选择器不可见的正常行为边界

“在模型选择器中显示”是 HideMagpieModelsInPicker 的反向开关。关闭时 hide 为 true。

internal/ui/settings_page_magpie.go:75 `{ID: "row-magpie-model-picker-visible", Title: "在模型选择器中显示", Aliases: []string{"Magpie", "模型选择器", "显示"}, Control: toggle(inverted(val(func(cfg *config.Config) *bool { return &cfg.Providers.HideMagpieModelsInPicker }, true)))},`

过滤表仅收录配置中 Magpie 自定义端点对应的独立 provider ID，并整组移除命中的 provider。

internal/ui/model_picker.go:138 `f := modelPickerMagpieFilter{hide: cfg.Providers.HideMagpieModelsInPicker}`

internal/ui/model_picker.go:145 `f.providers[config.CustomEndpointProviderKind(ep)] = true`

internal/ui/model_picker.go:157 `if f.providers[provider.ID] {`

已有测试明确约定：独立直连端点隐藏，pi/claude/codex 下的注入行保留。该开关也没有参与矩阵数据读取，因此不能解释矩阵为空。

internal/ui/model_picker_test.go:46 `// 独立直连 Magpie 的端点整体隐藏；pi/claude/codex 下的网关注入行全部保留。`

另外，“返回 7 个模型”只证明网关目录可读。内置引擎与 agent 的选择器目录按勾选生成，新增网关没有自动勾选步骤。

internal/app/engine.go:154 `// 默认模型排在最前；没有勾选时返回空列表。`

internal/app/agent_routing.go:42 `if setting.Mode == config.MagpieModeOff || len(setting.Selections) == 0 {`

结论：截图里的关闭开关可以解释独立网关分组隐藏；没有勾选可以解释注入分组没有新模型。这两项属于正常行为。只有在开关与勾选满足显示条件后目录仍不更新，才是选择器刷新 bug。当前静态证据确证的是矩阵漏刷，以及网关操作未统一使用现成的 Magpie 刷新链。

## 编号修改步骤

1. **集中网关变更后的现有刷新调用，新增一个薄的编排函数。** 文件 `internal/ui/settings_magpie_data.go`，行锚点 `magpieGateways`（当前第 10 行）之前，增加 `refreshMagpieGatewaySnapshots(st *SettingsState, client Client)`。函数依次调用 `st.loadPiMagpieEndpoints()`；遍历现有 `magpieMatrixAgents`，对每项调用 `st.reloadAgentMagpie(agent)`；调用 `markMagpieHubClaudeStale(st)`；调用 `refreshCatalogAfterPiMagpieSave(st, client)`；调用 `st.refreshModels()`。这是现有入口的组合，不新增缓存或调度机制。必须包含 mcode 与 pi 两列；仅调用 `refreshPiMagpieSnapshots` 只能覆盖 pi，仍会漏掉其他列。调用这些入口时不持有 `st.mu`，因为它们自己加锁。保留原缓存直至异步成功替换，利用已有 loading 状态隐藏临时空态。该函数只供成功的网关变更调用。

2. **新增和编辑共用保存成功分支。** 文件 `internal/ui/settings_providers.go`，行锚点 `saveCustomEndpoint` 中当前第 304 行及第 317 行：原第 304 行普通 provider 同步改为仅非 Magpie 保存时执行；在端点列表更新并解锁后，把 `if saved.Magpie` 的两次调用替换为步骤 1 的编排函数。将原第 321 行的 `st.refreshModels()` 放进非 Magpie 的 else 分支，避免 Magpie 强制刷新调用两次。保持现有错误返回、关闭表单、列表错误展示和 invalidate。保存已成功但列表重读失败时仍刷新已持久化的网关状态；不能因为 `lErr` 失败跳过刷新。

3. **删除后清除矩阵里的旧网关。** 文件 `internal/ui/settings_providers.go`，行锚点 `deleteCustomEndpoint` 中当前第 361 行及第 377 行：原目录同步只在 `!wasMagpie` 时执行；将 `if wasMagpie` 的 pi/hub 刷新替换为步骤 1 的编排函数。沿用已经在锁内计算的 `wasMagpie`，不要删除成功后再从新列表判断。不要修改保存的历史模型引用或勾选配置。backend 已注销 harness，不增加第二次注册/注销。

4. **启停沿用同一链路。** 文件 `internal/ui/settings_providers.go`，行锚点 `toggleCustomEndpointEnabled` 中当前第 429 行及第 441 行：原目录同步只在 `!ep.Magpie` 时执行；将 Magpie 分支替换为步骤 1 的编排函数。保存失败维持原本乐观状态回滚，不发起成功刷新。这样编辑 BaseURL、API Key、启停和删除都不会让矩阵继续使用旧 agent 快照。

5. **保持选择器过滤与后端构建逻辑。** `internal/ui/model_picker.go`、`internal/app/custom_endpoints.go`、`internal/remote` 不需要为此次修复修改。不要为了让所有新模型立刻可见而自动勾选，不要强行打开显示开关。已有保存勾选路径继续调用 `refreshCatalogAfterPiMagpieSave`。网关变更后的编排既强制刷新现有动态目录，又复用原有延后目录重读，让符合模式、勾选和显示开关条件的模型在当前进程出现。

6. **避免新增刷新覆盖旧的异步结果。** 复用延后目录重读后，需要让 `refreshCustomEndpointProviders` 也通过现成的目录序号入口应用结果。文件 `internal/ui/settings_providers.go`，行锚点当前第 214 行：先取得 `st.chat`，chat 为空直接返回；网络请求前取得 `seq := chat.nextProviderCatalogRequest()`；`client.Providers` 成功后调用 `chat.applyProviderCatalog(seq, providers, st)`，若返回 false 则结束。删除手工的 `chat.providers = providers` 赋值；保留原有 newThreadProvider 不存在时回退的逻辑，并在其锁内使用当前 `chat.providers` 判断存在性及计算默认值。这样旧的补读结果不会覆盖较新的强制刷新结果，同时复用已有 invalidate 和模型库目录版本更新。保持 `refreshCatalogProviders` 的 catalogStale 标记，不另造版本号。

## 测试修改清单（由主代理或实现代理编写并运行）

1. 文件 `internal/ui/settings_magpie_matrix_test.go`，新增 `TestMagpieGatewaySaveRefreshesLoadedMatrix`。使用既有 `matrixTestState` 建立四列均 `loaded=true` 的空旧快照，piMagpie 也先加载为空；FakeClient 的 AgentMagpie 与 PiMagpieEndpoints 改成有新网关、7 个模型，再经 `openCustomEndpointForm` 与 `saveCustomEndpoint` 真实保存入口触发刷新。新建使用明确的测试 ID 对应 FakeClient 生成的 ID（首次保存为 ep1），编辑子场景使用既有 ID。断言无需创建新 SettingsState 或重启进程，piMagpie 有 7 个模型，四个 agent 快照均包含新端点，`magpieMatrixData` 有 7 行，页面不再显示“网关没有返回模型”。通过已加载旧快照而非首次加载，确保旧实现必然暴露漏刷。另断言加载期间没有成功空态闪现。

2. 同文件新增 `TestMagpieGatewayMutationRefreshesLoadedMatrix`，表驱动覆盖 edit/delete/disable/enable。先给四列旧模型；fake 返回修改后的端点集合，再调用真实变更入口。edit 使用同 ID 的新模型，断言旧模型退出、新模型进入；delete/disable 断言旧网关从矩阵退出；enable 断言重新出现。断言 piMagpie 与 agentMagpie["pi"] 均同步，不能只断言网关状态文字。

3. 文件 `internal/ui/settings_providers_test.go`，新增 `TestMagpieGatewayMutationRefreshesPickerCatalog`。表驱动覆盖新增、编辑、删除、启停。chat.providers 初始为旧目录，fake 提供变更后的 provider/model 目录。通过真实保存或删除入口后，断言 `modelPickerCatalogFor(chat).Providers` 更新且目录版本增加；相应模型可在选择器显示分组找到或被移除。新模型夹具必须预设有效勾选和使用模式；该测试验证刷新，不测试自动选择全部网关模型。加普通非 Magpie 端点回归子场景，保持现有刷新行为。

4. 文件 `internal/ui/settings_providers_test.go`，新增 `TestRefreshCustomEndpointProvidersIgnoresOlderCatalog`。使用嵌入 FakeClient 的测试 Client 和 channel 控制 `Providers` 返回先后：较旧请求先发起但最后返回，新请求先返回并通过现成 applyProviderCatalog 应用。断言晚到的旧结果不能覆盖新模型、不能回退新线程 provider；不可仅检查调用次数。保留并更新既有 `TestSettingsProviders_RefreshSelector`，继续断言失效的新线程 provider 会回退。

5. 文件 `internal/ui/model_picker_test.go`，更新既有 `TestModelPickerHideMagpieModels`：在夹具和期望中加入带网关模型的 mcode 分组，明确关闭显示时只移除独立 Magpie provider，mcode/pi/claude/codex 注入行保留；开启后独立 provider 恢复；原切片不变。该测试是正常过滤语义的回归约束。

6. 文件 `internal/ui/settings_page_magpie_test.go`，在既有 `TestMagpieModelPickerVisibilityToggle` 旁新增 `TestMagpieGatewayRefreshDoesNotChangePickerVisibility`：分别预设显示开关开/关，完成网关刷新后断言值保持不变；两种情况下矩阵都能显示未勾选的可选模型。目录中只把已勾选模型作为可选模型返回，断言刷新不会隐式勾选新返回的所有模型。

测试夹具优先使用现有 `routeTestClient`、`FakeClient.SetAgentMagpie`、`SetPiMagpieEndpoints`、`SetProviders`、`matrixTestState` 和 `waitMagpie`。延后重读测试使用 channel 控制结果与短延迟；其他测试沿用现有做法暂设 `piMagpieCatalogRefreshDelays=nil` 并在 Cleanup 恢复，避免留下延迟任务。不要靠真实网络或重启应用使测试通过。

## 注意事项与待确认项

- “立即”指当前进程在已有异步请求完成后更新；不承诺同一渲染帧返回网络模型。保留错误与加载状态。
- 当前 `reloadAgentMagpie` 会把 loading 设为 false 后重拉；`loadPiMagpieEndpoints` 则跳过在途请求。此次方案沿用现有入口。若主代理复现“新增时恰有旧加载在途，旧结果覆盖新快照”，需要另补最小的请求先后顺序保护；本次没有运行并发复现，不能宣称这项边界已解决。不要仅清除 loading 来掩盖旧请求。
- 选择器过滤使用 SettingsState.cfg 的 Custom 列表，而端点操作更新的是 endpoints 列表；新增的独立 provider 在配置快照未重载前，隐藏表还可能缺少它。这倾向于导致“本应隐藏却显示”，不能作为当前“模型看不到”的根因。本次不扩展成设置配置同步重构。
- 保存路径已经存在强制目录刷新。主代理应实测：显示开启、有有效勾选、等待刷新完成后，是否仍有独立的选择器问题。若仍存在，记录具体 provider/model ID、模式和接口结果，再据证据修改后端；不要预设需要重新安装启动钩子。
- 本次没有执行任何测试或 GUI 实测。ponytail 技能文件读取被工具的允许目录限制拒绝，因此没有读取或应用其正文，也没有用 shell 绕过。
