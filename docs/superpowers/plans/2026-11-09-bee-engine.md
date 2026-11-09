# 阶段 4：bee 跑内置引擎的剩余缺口与实现计划

调研日期：2026-11-09。以本工作副本当前源码为准。本文只记录静态调研与后续计划；没有修改 Go 文件，没有运行构建、测试、lint、类型检查或其他验证。下文「测试」均为实现后的执行要求，并非已通过的结果。

## 1. 档位解析、配置存储与无 CLI 场景

- 档位保存于数据目录的 `config.json`，字段为 `orchestration.tiers` 和 `orchestration.default_tier`；不是单独的 tier 文件。
  internal/config/load.go:33 `	return filepath.Join(dataDir, configFileName)`
  internal/config/config.go:713 ``	Tiers                 []SwarmTier `json:"tiers,omitempty"`          // 档位定义列表``
  internal/config/config.go:714 ``	DefaultTier           string      `json:"default_tier,omitempty"`   // 默认档位 ID``
- 内置四档为 read（只读）、chore（杂活）、edit（执行）、deep（深度）；provider/model 全空。read/chore/edit 默认 medium，deep 默认 high，默认档位是 edit。deep 允许写方案文档，不能把它当成 ReadOnly 档。
  internal/config/config.go:348 `// 所有档位的 Provider 与 Model 均为空，默认跟随会话生效的子代理模型；`
  internal/config/config.go:349 `// Effort 为各档默认推理强度，深度档用 high，其余用 medium。`
  internal/config/config.go:168 `	DefaultSwarmTierID     = "edit"`
  internal/config/config.go:356 `			ReadOnly:    true,`
  internal/config/config.go:375 `			WritePaths:  defaultDeepWritePaths(),`
  internal/config/config.go:344 `	return []string{"docs/superpowers/plans/**/*.md"}`
- 缺失 tiers（nil）会补内置四档；显式空数组不补。默认档位无效时优先 edit，再选首个非只读档位。
  internal/config/config.go:1776 `	if cfg.Orchestration.Tiers == nil {`
  internal/config/config.go:1777 `		cfg.Orchestration.Tiers = DefaultSwarmTiers()`
  internal/config/config.go:1794 `			cfg.Orchestration.DefaultTier = DefaultSwarmTierID`
  internal/config/config.go:1803 `			cfg.Orchestration.DefaultTier = firstNonReadOnly`
- Profile 是兼容输入，仍可从 `<dataDir>/hydra-profiles.json` 读取；不再作为档位的新存储位置。
  internal/swarm/profile.go:14 `const ProfilesFileName = "hydra-profiles.json"`
  internal/swarm/manager.go:319 `	return filepath.Join(filepath.Dir(m.opts.WorktreeDir), ProfilesFileName)`
- 解析先取 delegation.tier，其次 delegation.profile，再取 DefaultTier；匹配 tier，否则尝试旧 Profile。覆盖顺序是 tier/Profile → 会话子代理模型 → 配对档位覆盖 → 线程档位覆盖 → 用户本条消息明确授权的直属配对/provider/model。后续宿主覆盖再次覆盖非空字段。
  internal/swarm/profile.go:116 `// ResolveBeeConfig 按「档位/Profile → 会话子代理模型 → 配对覆盖 → 线程覆盖 → 委派直属指定」的顺序逐层覆盖`
  internal/swarm/profile.go:129 `		ref = strings.TrimSpace(in.DefaultTier)`
  internal/swarm/profile.go:168 `		applyBeeFields(&cfg, in.SessionBeeProvider, in.SessionBeeModel, in.SessionBeeEffort)`
  internal/swarm/profile.go:181 `					applyBeeFields(&cfg, ov.Provider, ov.Model, ov.Effort)`
  internal/swarm/profile.go:193 `			applyBeeFields(&cfg, ov.Provider, ov.Model, ov.Effort)`
  internal/swarm/profile.go:229 `		if AuthorizeDirectModel(in.UserMessage, d.Provider, d.Model, in.ProviderEnabled) {`
  internal/swarm/manager.go:1680 `	// 宿主代码给的覆盖项在档位解析之后生效：只覆盖非空字段，DirectModel 保持原值。`
- DirectModel 是授权与展示标志，不是另一个引擎开关；只在获授权的直属配对或直属 provider/model 时设 true。普通会话模型、tier、pair/thread override 不因此变成 DirectModel。
  internal/swarm/profile.go:205 `				cfg.DirectModel = true`
  internal/swarm/profile.go:230 `			applyBeeFields(&cfg, d.Provider, d.Model, "")`
  internal/swarm/profile.go:231 `			cfg.DirectModel = true`
- lead 的 SwarmBeesProvider/Model 通过 backend.SwarmThreadContext → app 的 ThreadContext → ResolveInput 进入解析；只有 SessionBeeModel 非空才整组覆盖，单独 SessionBeeEffort 仅改强度。
  internal/remote/memory_backend.go:4801 `		beeProvider = *td.summary.SwarmBeesProvider`
  internal/remote/memory_backend.go:4804 `		beeModel = *td.summary.SwarmBeesModel`
  internal/app/app.go:438 `				msg, pairID, tiers, beeProvider, beeModel, beeEffort := backend.SwarmThreadContext(leadThreadID)`
  internal/swarm/manager.go:1674 `		SessionBeeProvider: tc.BeeProvider,`
  internal/swarm/profile.go:167 `	if in.SessionBeeModel != "" {`
- ResolveBeeConfig 本身没有默认 provider/model；当前全局 orchestration.default_provider/default_model 默认也为空。
  internal/swarm/profile.go:119 `	var cfg BeeConfig`
  internal/config/config.go:2214 `			DefaultProvider:            "",`
  internal/config/config.go:2215 `			DefaultModel:               "",`
- 有一个必须修的预览/实跑偏差：resolveBeeRuntime 会补 settings 默认值，用于 Bee 记录和预览；StartSession 请求却只传 spec 原值。全局显式 default_provider/default_model 可能显示生效、实际仍继承 lead。
  internal/swarm/manager.go:1210 `	provider, model, effort := resolveBeeRuntime(spec.Provider, spec.Model, spec.Effort, settings)`
  internal/swarm/manager.go:1219 `		Provider: spec.Provider,`
  internal/swarm/manager.go:1220 `		Model:    spec.Model,`
  internal/swarm/manager.go:1277 `		Provider:          provider,`
  internal/swarm/manager.go:358 `		provider, model, effort := resolveBeeRuntime(cfg.Provider, cfg.Model, cfg.Effort, st)`
- 最终 StartSession 先取 lead.SwarmBeesProvider/Model，provider 空则取 lead.Provider/Model，再由请求显式 provider/model 覆盖。请求 provider 非空、model 为空时会清除继承模型，交给该来源的默认模型。
  internal/remote/kanban_driver.go:43 `		provider = derefString(lead.SwarmBeesProvider)`
  internal/remote/kanban_driver.go:46 `			provider = lead.Provider`
  internal/remote/kanban_driver.go:61 `		provider = req.Provider`
  internal/remote/kanban_driver.go:62 `		model = req.Model`
- 结论：没有安装 CLI，但 lead 已明确选中有有效凭据/模型的 API 来源时，空档位 bee 会继承该来源，已经可以跑内置引擎；这条路径不要求安装 pi。若 lead 是旧 mcode，则仍通过别名跑；若 lead 指向缺失 CLI，则不会自动切 API，可能创建失败或运行失败。
  internal/app/engine.go:116 `	return providermcode.New(opts)`
  internal/remote/kanban_driver.go:46 `			provider = lead.Provider`
  internal/remote/memory_backend.go:4027 `				Message: fmt.Sprintf("unknown provider %q", reqProvider),`
- 不能据此宣称「全新安装只配 API，任意新建 swarm 都自动成功」。无 claude/codex 时仍注册 ACP，ACP 未配置关闭也照样注册；第一个注册项可成为默认，当前 ACP 首项为 cline。通用新会话默认来源是原阶段 5 的依赖，阶段 4 应明确使用 API lead 验收，并单独记录此依赖。
  internal/app/app.go:991 `		if claudeAvailable || codexErr == nil {`
  internal/app/app.go:1048 `		agyAccountsMgr, agyHarness = registerExtraProviders(backend, cfg, opts.DataDir, opts.AgyAccountsOptions, mcpInjection)`
  internal/app/app.go:1910 `		if ok && !pCfg.Enabled {`
  internal/app/app.go:1936 `		backend.RegisterHarness(acp.New(opts))`
  internal/providers/acp/agents.go:24 `		AgentCline,`
  internal/remote/memory_backend.go:1906 `		b.defaultProvider = kind`

## 2. mcode 别名与真实 kind

### 2.1 先区分标签与存储字段

- `mcode/magpie-jp/auto/<模型>` 是 provider/model 拼接后的展示形状；运行所需字段为 provider=`mcode`、model=`magpie-jp/auto/<模型>`。provider=`mcode` 时把完整展示形状再放进 model，会多一层 `mcode/`，现有别名解析器不能识别。
  internal/swarm/profile.go:258 `	return provider + "/" + model`
  internal/app/mcode_agent.go:150 `	if target, modelID, ok := magpieinject.ProviderOf(model, targets); ok {`
  internal/magpieinject/magpieinject.go:104 `	providerID, rest, ok := strings.Cut(model, "/")`
- `<kind>/<模型>` 也应理解为展示标签或兼容输入；标准存储是独立的 Provider、Model 字段。当前 ResolveBeeConfig 不拆模型前缀，因此单独把 `kind/model` 写到 Model 并不能自动切 provider。
  internal/swarm/profile.go:268 `		cfg.Provider = provider`
  internal/swarm/profile.go:271 `		cfg.Model = model`
- 特别注意：Magpie 的注入 ProviderID（如 magpie-jp）不等于 harness kind。旧别名要按 ProviderID 找 Target.Endpoint，再按该 endpoint 的配置求 CustomEndpointProviderKind；不能直接把第一段当成 kind，也不能按端点名称猜 ID。
  internal/magpieinject/magpieinject.go:51 `		providerID := magpie.ProviderID(ep.Name, taken)`
  internal/magpieinject/magpieinject.go:76 `			Endpoint:   ep.ID,`
  internal/config/config.go:1439 `			return ep.ID`
  internal/config/config.go:1442 `	return CustomEndpointKind(ep.ID)`

### 2.2 可观察差异

| 项目 | provider=mcode，model=端点 ProviderID/模型 | provider=真实 kind，model=来源模型 |
| --- | --- | --- |
| 上游 | 旧解析器仅覆盖 Magpie 与 anthropic/；空模型走旧 Magpie 默认逻辑 | 按该来源解析配置，覆盖预设/OpenAI/Anthropic/自定义/Magpie |
| 会话 ID | 新会话以 mcode_ 开头 | 新会话以 kind_ 开头 |
| 目录 | 别名聚合目录，模型 ID 带端点前缀 | 单来源目录，模型 ID 不含端点前缀；auto/ 等网关路由仍保留 |
| 会话文件目录 | dataDir/mcode/sessions | 同一个 dataDir/mcode/sessions；不是每来源新建目录 |
| 额度 key | mcode:端点 ProviderID | Magpie 来源直接用 kind（magpie、magpie2 或 custom-id） |
| 显示名 | harness 默认 mcode，模型标签容易保留长前缀 | 来源名称与网关模型短名 |

上述各行证据：

- 别名仅接受已知 Magpie 或 anthropic/，不是所有来源的通用路由器。
  internal/app/mcode_agent.go:157 `	if strings.HasPrefix(model, "anthropic/") {`
  internal/app/mcode_agent.go:172 `	return nil, "", fmt.Errorf("没有找到模型 %s 对应的 Magpie 端点，请在设置中连接 Magpie", model)`
  internal/app/engine.go:138 `	source, ok := findAPISource(apisource.Resolve(current, store), kind)`
- 会话前缀由 harness kind 决定，前缀不同不复用同一个旧 session；无需迁已有线程与 JSONL。
  internal/providers/mcode/mcode.go:140 `		turn.SessionID = string(h.Kind()) + "_" + session.NewID()`
  internal/providers/mcode/shell.go:84 `	case "mcode", "openai", "anthropic", "deepseek", "zai", "meta", "magpie", "magpie2", "magpie3", "magpie4":`
  internal/providers/mcode/shell.go:85 `		return false`
- 两种 harness 共用会话存储根，文件名为 sessionID.jsonl。
  internal/app/mcode_agent.go:270 `		storeDir = filepath.Join(dataDir, "mcode", "sessions")`
  internal/app/engine.go:41 `	opts := mcodeEngineOptions(secrets, dataDir, mcpInjection)`
  internal/agent/session/session.go:37 `	return filepath.Join(s.dir, id+".jsonl"), nil`
- 别名模型目录加端点前缀；来源目录保留网关原始 ID 和短名。
  internal/app/mcode_agent.go:347 `			row.ID = target.ProviderID + "/" + row.ID`
  internal/app/api_catalog.go:67 `			ID: model.ID, Name: magpie.ShortName(model), Efforts: mcodeEfforts(model),`
- 额度已兼容两种 key；实际都读同一端点，按线程 ID 标记在用账号。无需为档位迁移另造 api: 前缀。
  internal/ui/composer_usage_panel.go:177 `				return th.Provider + ":" + head`
  internal/ui/composer_usage_panel.go:181 `	return th.Provider`
  internal/app/engine_quota.go:65 `	if providerID, ok := strings.CutPrefix(key, engineMcodeQuotaPrefix); ok {`
  internal/app/engine_quota.go:83 `		if source.Kind != key || !source.Magpie {`
  internal/app/engine_quota.go:159 `	return c.readMagpieQuotaWindows(ctx, endpoint.baseURL, endpoint.apiKey, "", mcodeui.QuotaThread(ctx))`
- 来源 harness 继承来源显示名；别名默认显示 mcode。
  internal/app/engine.go:44 `		opts.DisplayName = named.DisplayName()`
  internal/providers/mcode/shell.go:21 `	return "mcode"`

### 2.3 最小迁移方案

- 新建档位无需新增选择器或引擎专属模式。未选择模型时继续存空 Provider/Model，表示运行时动态默认；明确选择时存来源 prov.ID 与来源 m.ID，例如 provider=`custom-jp`、model=`auto/claude-sonnet-5-5`。不能丢掉模型自己的 auto/ 或 group/ 段。
  internal/ui/settings_swarm_tiers.go:334 `								uiSt.draft.Provider = next.Provider`
  internal/ui/settings_swarm_tiers.go:335 `								uiSt.draft.Model = next.Model`
  internal/ui/swarm_model_capsule.go:81 `					Provider: prov.ID,`
  internal/ui/swarm_model_capsule.go:82 `					Model:    m.ID,`
- 设置页档位选择器已经复用聊天的 provider 目录，不读 hydra-profiles.json，也不单独读 pi 勾选。无聊天目录时仅有 Claude/Codex/agy 的降级占位，此时不能声称 API 目录已加载。
  internal/ui/settings_swarm_tiers.go:333 `							renderSwarmModelCapsuleEx(c, st, "swarm-tier-model-capsule", "tier-model-"+popKey, tierSel, "跟随会话子代理模型", "选择档位模型", func(next ModelSelection) {`
  internal/ui/swarm_model_capsule.go:24 `	renderSwarmModelCapsuleCatalog(c, st, key, scopeKey, sel, emptyLabel, triggerLabel, onChange, settingsModelPickerCatalog(st), false)`
  internal/ui/model_picker_catalog.go:71 `			cat = modelPickerCatalogFor(chat)`
  internal/ui/model_picker_catalog.go:50 `	cat.Providers = append([]ProviderInfo(nil), s.providers...)`
  internal/ui/model_picker_catalog.go:76 `			{ID: config.ProviderClaude, Name: "Claude"},`
- 新目录已隐藏 mcode，来源目录由 app 注册到 remote。新选择的存储路径无需改动；只补旧选择的读时展示兼容。
  internal/ui/model_picker.go:142 `		if provider.ID == "mcode" {`
  internal/ui/model_picker.go:143 `			continue`
  internal/app/engine.go:59 `	remote.SetAPICatalog(string(kind), catalog)`
  internal/remote/models.go:428 `	if rows, registered := apiCatalog(provider); registered {`
- 老档位不做批量落盘迁移。新增一个只做解析的共享 helper（建议放 internal/apisource/bee_model.go）：provider=mcode 时拆端点 ProviderID，经 Target.Endpoint 映射实际 kind；anthropic/ 映射 anthropic；provider 空且 model 明确以已知真实 kind 开头时拆成字段；显式 pi/其他 CLI 完全原样。无法唯一识别的旧字符串保留旧别名，不猜来源、不改旧线程。
  internal/apisource/source.go:251 `		Kind:           config.CustomEndpointProviderKind(ep),`
  internal/magpieinject/magpieinject.go:110 `			return target, rest, true`
  internal/app/app.go:1989 `	backend.RegisterHarness(buildMcodeHarness(cfg, secrets, dataDir, mcpInjection))`
- helper 在 swarm 最终有效配置解析和 TierPreview 共用的读路径中使用，且宿主覆盖之后也要规范化；UI 胶囊读旧值时调用同一 helper，只有用户主动保存才写标准字段。只规范化最终配置不足以解决「仅 model 覆盖仍残留前层 provider」的情况，执行顺序应为：逐层识别明确的来源形式，再用现有 applyBeeFields 覆盖，最终检查一次；保持既有层级优先级。
  internal/swarm/profile.go:134 `		applyBeeFields(&cfg, tier.Provider, tier.Model, tier.Effort)`
  internal/swarm/profile.go:265 `		if provider != cfg.Provider && model == "" {`
  internal/swarm/manager.go:1681 `	if host != nil {`
  internal/ui/swarm_model_capsule.go:46 `		sel = resolveModelPickerSelection(providers, sel)`

## 3. 运行时逐项结论

### 3a. panic 隔离：主链路已有保障，但没有覆盖全部 goroutine

- 无需重写 Run 的 recover。同步客户端 Stream、流式 delta 回调、串行工具和 emit 都在 Run 的调用栈内，外层 recover 可兜底；已存在 Stream/串行工具 panic 的回归测试，但本次没有执行。
  internal/providers/mcode/mcode.go:131 `		if recovered := recover(); recovered != nil {`
  internal/agent/agent.go:200 `			response, err = opts.Client.Stream(ctx, request, func(delta llm.Delta) {`
  internal/agent/agent.go:314 `					result = runTool(ctx, opts, call)`
  internal/agent/llm/openai.go:307 `					onDelta(Delta{Type: "text", Text: d.Content})`
  internal/agent/llm/anthropic.go:276 `						onDelta(Delta{Type: "text", Text: block.Text})`
  internal/providers/mcode/mcode_test.go:405 `func TestRunRecoversStreamPanic(t *testing.T) {`
  internal/providers/mcode/mcode_test.go:419 `func TestRunRecoversToolPanic(t *testing.T) {`
- 并发只读工具各自有 recover，结果按批次收集，事件在 Wait 后串行发出。该项无需改动。
  internal/agent/agent.go:339 `						if r := recover(); r != nil {`
  internal/agent/agent.go:340 `							results[index] = tools.Result{Text: fmt.Sprintf("tool panicked: %v", r), IsError: true}`
  internal/agent/agent.go:348 `			wg.Wait()`
  internal/agent/agent_test.go:476 `func TestParallelToolPanicIsRecovered(t *testing.T) {`
- MCP 发现 worker 没有 recover，且从 Run 的 ExtraTools 实际可达；其 panic 不会被 Run recover 捕获。需在 worker 内恢复成该来源发现错误，确保 wg.Done 执行，不把 panic 静默当成功。
  internal/app/mcode_agent.go:289 `			return mcp.Tools(ctx, mcp.Servers(turn, mcpInjection.Builtin, external))`
  internal/agent/mcp/tools.go:162 `		go func(index int, server Server) {`
  internal/agent/mcp/tools.go:164 `			loadedServers[index] = discoverServer(ctx, server)`
- shell 的 Wait、后台 reaper、日志清理均另起 goroutine。后台 cleanup/onExit 和前台 outputWriter 的回调不能依赖 Run 的 recover。尤其 outputWriter.Write 由 os/exec 的输出拷贝 goroutine 调用，在 cmd.Wait 外包 recover 不能捕获它内部的 panic；应在 Write 的回调边界转换为 I/O 错误，并在自建 worker 边界保证退出通知与清理。
  internal/agent/shell/shell.go:110 `		w.onOutput(string(p))`
  internal/agent/shell/shell.go:125 `		shellLogCleanupOnce.Do(func() { go cleanupOldShellLogs(dir) })`
  internal/agent/shell/shell.go:209 `	go func() { done <- cmd.Wait() }()`
  internal/agent/shell/background.go:177 `	go func() {`
  internal/agent/shell/background.go:189 `			tracker.onExit(pid)`
  internal/agent/shell/background.go:192 `			cleanup()`
- 隔离范围限引擎控制的 goroutine 与回调；不承诺拦截任意外部库自行启动的 goroutine、runtime fatal 或进程被外部终止。现有串行 emit 本身无需新增 goroutine。
  internal/harness/harness.go:222 `		emit(e)`

### 3b. 停止与进程组：前台已完整，后台有明确缺口

- StopBee/StopAll 调用 app.StopSession → backend.InterruptCurrent → runCancel；前台 shell.Run 监听取消，Unix 设置独立进程组并按负 PID 先 TERM、后 KILL，连忽略 TERM 的后台子孙也再清一次。此路径无需另加遍历杀进程。
  internal/swarm/manager.go:4306 `		_ = m.opts.StopSession(ctx, beeThreadID)`
  internal/swarm/manager.go:4371 `			_ = m.opts.StopSession(ctx, id)`
  internal/app/app.go:441 `			StopSession:        func(ctx context.Context, id string) error { _, err := backend.InterruptCurrent(ctx, id); return err },`
  internal/remote/memory_backend.go:5292 `		td.runCancel()`
  internal/agent/shell/shell.go:219 `	case <-ctx.Done():`
  internal/agent/shell/shell.go:221 `		err = stopProcess(cmd, done)`
  internal/agent/shell/shell_unix.go:24 `	cmd.SysProcAttr.Setpgid = true`
  internal/agent/shell/shell_unix.go:28 `	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)`
  internal/agent/shell/shell_unix.go:34 `		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)`
- ProcessRootPIDs 已按 threadID 跟踪前台和后台根 PID；当前调用方是「只停止这条命令」的 KillRunningCommand，不是 StopBee 的取消收尾。该工具按启动时间杀子树，与取消整只 bee 的语义不同。
  internal/providers/mcode/shell.go:136 `func (h *Harness) ProcessRootPIDs(threadID string) []int {`
  internal/agent/shell/background.go:175 `		tracker.onStart(pid)`
  internal/remote/memory_backend.go:5533 `	for _, root := range rooter.ProcessRootPIDs(threadID) {`
  internal/remote/memory_backend.go:5534 `		n, err := procs.KillTreesStartedAfter(root, since, killRunningCommandGrace)`
- run_in_background=true 进入 shell.Start；该 API 明确不绑定 ctx，现有测试也要求取消后存活。因此现在 StopBee 不能保证没有后台 bash。需要按线程拥有者清理，不能仅因已有 ProcessRootPIDs 就判定已解决。
  internal/agent/tools/bash.go:100 `		id, pid, err := shell.Start(ctx, opts)`
  internal/agent/shell/background.go:109 `// 任务不绑定 ctx 取消：本轮结束后进程继续运行，可用 Kill 停止。`
  internal/agent/shell/background_test.go:52 `func TestBackgroundSurvivesContextCancel(t *testing.T) {`
- 最小选择：在 harness.Run 收尾时，若 ctx 已取消或 turn.Transient 为真，按 threadID 终止该线程拥有的全部后台任务；普通交互线程正常结束仍可跨轮保留后台任务。bee 正常结束也清理后台服务，避免完成后无法再经 StopBee 清理。不能简单给 shell.Start 加永久 ctx.Done 监听，因为 remote 正常收尾也会 cancel 本轮。
  internal/harness/harness.go:109 `	Transient bool`
  internal/providers/mcode/mcode.go:238 `	runCtx := h.withProcessTracker(ctx, turn.ThreadID)`
  internal/remote/memory_backend.go:7678 `		if td.runCancel != nil {`
  internal/remote/memory_backend.go:7679 `			td.runCancel()`
- Windows 已用 CREATE_NEW_PROCESS_GROUP 与 taskkill /T /F；纯 Go 实现可复用，无需 cgo。本阶段不扩展到主动 setsid/daemonize 脱离进程组的进程治理。
  internal/agent/shell/shell_windows.go:26 `	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP`
  internal/agent/shell/shell_windows.go:30 `	killer := procattr.HideWindow(exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)))`

### 3c. 只读与无人值守：审批通常不挂起，但当前只读档没有硬约束

- harness.Decide 的 readonly 会直接拒绝写工具，auto 已识别新 write/edit；无需修改这份旧权限裁决表。引擎实际走 gate.Check，不能把 Decide 的保护算给引擎。
  internal/harness/permission.go:141 `	if mode == ModeReadOnly && !readOnlyTool {`
  internal/harness/permission.go:142 `		return DecisionDeny`
  internal/harness/permission.go:200 `		if readOnlyTool || toolName == "write" || toolName == "edit" || toolName == "write_file" || toolName == "edit_file" ||`
  internal/agent/agent.go:419 `	verdict := gate.Check(gate.Input{Mode: opts.Mode, Plan: opts.Plan, HardLocked: opts.HardLocked, HardLockOverride: opts.HardLockOverride, ToolName: call.Name, ToolID: call.ID, Args: call.Arguments, WorkDir: opts.Env.WorkDir, AdditionalDirs: opts.Env.AdditionalDirs})`
- 引擎 gate 对 readonly 的非只读调用没有硬 Deny 分支，最后产生 Ask。executeTool 会交给 Approver；若是正常交互线程，会等待审批，甚至可能获批后执行。
  internal/agent/gate/gate.go:210 `	return Verdict{Action: Ask, Request: req, SessionKey: key}`
  internal/agent/agent.go:431 `	if verdict.Action == gate.Ask && !opts.Gate.Allowed(verdict.SessionKey) {`
  internal/agent/agent.go:435 `			response, err = interactor.Interact(ctx, verdict.Request)`
- bee 不固定 fullAccess，而是继承 lead.RuntimeMode，宿主可覆盖；全局默认 runtimeMode 解析失败或为空时为 auto。ReadOnly 档当前没有映射成 readonly runtimeMode，而是提示词加事后丢弃改动。
  internal/remote/kanban_driver.go:51 `		runtimeMode = lead.RuntimeMode`
  internal/remote/kanban_driver.go:72 `		runtimeMode = req.RuntimeMode`
  internal/remote/memory_backend.go:1989 `		return "auto"`
  internal/swarm/manager.go:1227 `	// 不能卡在审批上），只读约束由提示词加完成后的 ReadOnlyViolation 检查保证。`
  internal/swarm/manager.go:2482 `				tgt.bee.ReadOnlyViolation = true`
- 关系标记建立后，readonly helper 的写审批直接拒绝；其他 bee 的非敏感审批自动允许、敏感审批自动拒绝，一般不会卡在工具审批。提问是例外：开启自动转交时会等待主线程处理，不能承诺所有 Ask 都不等待。
  internal/remote/approvals.go:212 `	if (td.isHelper || td.parentThreadID != "") && td.summary.RuntimeMode == string(harness.ModeReadOnly) {`
  internal/remote/approvals.go:220 `				Decision: harness.DecisionDeny,`
  internal/remote/approvals.go:229 `	if td.isHelper || td.parentThreadID != "" {`
  internal/remote/approvals.go:230 `		if req.Sensitive != "" {`
  internal/remote/approvals.go:237 `		if kind == harness.RequestKindQuestion && !autoForwardQuestion {`
  internal/remote/approvals.go:248 `			return harness.ApprovalResponse{Decision: harness.DecisionAllow}, nil`
- 还有首轮竞态：StartSession 内先 SendMessage，返回后 Manager 才 SetThreadRelation。快模型在关系标记前发起 Ask，可能落进交互审批。应在 StartSession 中、发送首条 prompt 之前设置 helper/parent；保留 Manager 对其他 driver 和 persister 的关系写入。
  internal/remote/kanban_driver.go:122 `	_, err = b.SendMessage(ctx, wire.SendMessageParams{`
  internal/swarm/manager.go:1243 `	threadID, err := m.opts.Driver.StartSession(ctx, sessionReq)`
  internal/swarm/manager.go:1256 `			s.SetThreadRelation(threadID, leadThreadID, true)`
- 最小修复为两端补齐：ReadOnly 档强制传 readonly（优先于继承和宿主给出的可写模式），gate 对 readonly 非只读工具硬拒绝，允许明确只读的工具、命令与取网页；不能用 fullAccess 解决无人值守。保留事后 ReadOnlyViolation 作为兜底，deep 的 WritePaths 不变。
  internal/swarm/manager.go:1224 `		RuntimeMode: spec.RuntimeMode,`
  internal/agent/gate/gate.go:164 `	reads := IsLookOnly(in.ToolName) || kind == harness.RequestKindCommand && onlyReads(command) || webFetch`
  internal/swarm/manager.go:2461 `		if tgt.bee.ReadOnly {`

### 3d. 多 bee 共享 harness：核心状态可重入，后台任务还不隔离

- 无需为每个 bee 新建 harness。opts 在构造后只读；gates/pids 按 threadID，runs 按 sessionID，不是都按 threadID。各 map 访问有共享 mutex，activeRun 和 gate.Session 也各有锁；会话使用独立 UUID。
  internal/providers/mcode/mcode.go:58 `	mu    *sync.Mutex`
  internal/providers/mcode/mcode.go:293 `	h.runs[sessionID] = run`
  internal/providers/mcode/mcode.go:324 `		h.gates[threadID] = &gate.Session{}`
  internal/providers/mcode/shell.go:113 `	set := h.pids[threadID]`
  internal/providers/mcode/mcode.go:75 `	r.mu.Lock()`
  internal/agent/gate/gate.go:62 `	mu      sync.RWMutex`
- 重注册 harness 共享同一把锁与同一组 map；会话文件跨 Store 实例也有全局锁，未发现需要每 bee 独立实例的证据。前提仍是 backend 保持同线程同一轮次，不并发运行同 session。
  internal/providers/mcode/shell.go:51 `		h.mu = old.mu`
  internal/providers/mcode/shell.go:52 `		h.gates = old.gates`
  internal/providers/mcode/shell.go:53 `		h.runs = old.runs`
  internal/providers/mcode/shell.go:54 `		h.pids = old.pids`
  internal/agent/session/session.go:20 `var storeMu sync.Mutex`
  internal/remote/memory_backend.go:3399 `	if td.runTurnID != "" || td.summary.IsRunning || td.takingBack {`
- 但 shell 后台表是整个进程共享，Output/Kill 只按 bgID 访问，没有 threadID 校验；当前 bee 能误读或停止另一 bee 的后台任务。这是工具层隔离缺口，不是 harness map 的数据竞争。需将拥有者随 ctx 传入 shell，并在 bash_output/kill_bash 检查；不能仅通过换 harness 实例解决。
  internal/agent/shell/background.go:93 `	backgroundJobs = map[string]*backgroundJob{}`
  internal/agent/shell/background.go:201 `	job := backgroundJobs[id]`
  internal/agent/shell/background.go:213 `	job := backgroundJobs[id]`
  internal/agent/tools/bash.go:140 `	text, running, exitCode, err := shell.Output(args.ID)`
  internal/agent/tools/bash.go:165 `	if err := shell.Kill(args.ID); err != nil {`

## 4. 默认来源与模型的明确选择

- 不给 BeeConfig 字段硬编码 mcode、pi、端点 ID 或某个模型名；profile.go 是解析层，不应自己读配置/密钥或做网络请求。用可注入的默认来源/规范化输入补齐，预览和派发共用。
  internal/swarm/profile.go:98 `// ResolveInput 是解析一次 bee 派发所需的全部输入。`
  internal/swarm/profile.go:119 `	var cfg BeeConfig`
  internal/app/swarm_wiring.go:38 `		DefaultProvider: c.DefaultProvider,`
- 默认策略：保留既有显式 tier/Profile/session/pair/thread/直属/宿主优先级；显式全局 default_provider/default_model 作为其后的配置默认；都没有时，优先 lead 正在使用的有效 API 来源与模型，再按 apisource.Resolve 固定顺序选择第一个启用且有凭据的来源。显式 pi/其他 CLI 的 provider 或模型不替换。只有 provider 显式、model 空时，让该 provider 自己取默认，避免拼进其他来源的默认模型。
  internal/swarm/profile.go:117 `// （非空才覆盖），返回最终配置以及给 Lead 的中文提示。这里不套用任何全局默认 Provider。`
  internal/swarm/profile.go:265 `		if provider != cfg.Provider && model == "" {`
  internal/apisource/source.go:125 `// Resolve 按固定顺序返回所有已启用的来源：Magpie 端点（按配置顺序）、`
  internal/apisource/source.go:126 `// anthropic、openai、deepseek、zai、meta、其余自定义端点（按配置顺序）。`
  internal/apisource/source.go:98 `func (s Source) APIKey(secrets config.SecretStore) string {`
- Magpie 来源优先用配置的 DefaultModel，未指定时使用该来源目录默认/首项；缓存未热时可让 model 留空由引擎在运行时限时取目录。不能要求用户先在 pi 勾选模型。其他来源同样交给来源默认规则。没有可用 API 来源时返回明确配置错误，不默默落回缺失 CLI；显式 CLI 配置的原有失败行为保持。
  internal/app/engine.go:157 `				if candidate.ID == strings.TrimSpace(source.DefaultModel) {`
  internal/app/engine.go:165 `					modelID = models[0].ID`
  internal/app/engine.go:148 `				ctx, cancel := context.WithTimeout(context.Background(), magpieDefaultModelTimeout)`
  internal/app/engine.go:169 `			modelID = sourceEngineDefaultModel(source, kind, current, legacy)`
  internal/app/engine.go:236 `	if model := strings.TrimSpace(source.DefaultModel); model != "" {`
- 动态默认只影响新派发，不回填每个老档位和线程；新建档位空值继续表达动态默认。显式给「default」占位应谨慎：目录热起来后校验可能不再接受字面 default，直接留空更稳妥。
  internal/remote/kanban_driver.go:81 `		Model:       optionalString(model),`
  internal/remote/memory_backend.go:4081 `	modelVal := DefaultModel(provider)`
  internal/remote/models.go:690 `	for _, m := range models {`
  internal/remote/models.go:691 `		if m.ID == model {`
- 原阶段 5 的「通用新会话最近 API 来源」与本阶段 bee 默认不重复实现；本阶段只保证 API lead 能派发、CLI lead 在 bee 没显式配置时能选有效 API。通用 UI 新建 lead 的默认选择仍需阶段 5 完成。
  internal/remote/memory_backend.go:4018 `	if b.defaultProvider != "" {`
  internal/remote/memory_backend.go:4019 `		provider = b.defaultProvider`

## 5. 可并行任务与文件边界

三个任务可同时实现，文件集合不重叠。A 负责只读档的运行模式与首轮关系标记，C 负责门禁；二者合入后联合验收。B 独立。新文件均为建议新增路径，不表示现在已存在。测试由主代理在自己的检出执行。

### 任务 A：统一 bee 的有效来源解析、别名读兼容和派发接线

**现状**

- UI 新选择已经保存真实 kind，旧别名仍可执行；缺少规范化和动态 API 默认，预览/实跑默认值不一致；只读 runtimeMode 和 helper 标记时序还未落实。
  internal/ui/swarm_model_capsule.go:81 `					Provider: prov.ID,`
  internal/swarm/manager.go:1219 `		Provider: spec.Provider,`
  internal/swarm/manager.go:1227 `	// 不能卡在审批上），只读约束由提示词加完成后的 ReadOnlyViolation 检查保证。`

**剩余步骤**

1. 在来源解析层新增纯函数规范化 helper，处理 mcode/端点 ProviderID、anthropic/ 与明确真实 kind 前缀；通过 Endpoint ID 找真实 kind，不改输入配置。未知、失效或歧义旧值保持别名，并给派发方明确的解析结果；显式 CLI 完全不动。
   internal/magpieinject/magpieinject.go:104 `	providerID, rest, ok := strings.Cut(model, "/")`
   internal/magpieinject/magpieinject.go:110 `			return target, rest, true`
   internal/config/config.go:1435 `func CustomEndpointProviderKind(ep CustomEndpointConfig) string {`
2. app 新增 bee 默认解析函数，从最新配置/凭据和 lead 读取默认候选，不依赖 CLI 是否安装；通过 swarm.Options 的可选回调与 ResolveInput 的默认字段/规范化函数注入。保持 swarm 不依赖 app/remote；没有 API 候选时明确报错，不给显式 CLI 自动换来源。
   internal/app/app.go:437 `			ThreadContext: func(leadThreadID string) swarm.ThreadContext {`
   internal/swarm/swarm.go:404 `type Options struct {`
   internal/swarm/profile.go:99 `type ResolveInput struct {`
   internal/apisource/source.go:131 `func Resolve(cfg config.Config, secrets config.SecretStore) []Source {`
3. 共用一条有效配置解析入口，按第 4 节顺序应用默认；让 TierPreview、派发记录与 SessionRequest 使用同一个最终 provider/model。逐层处理兼容前缀，宿主覆盖后再次规范化；provider 切换时不能混用旧模型。设置默认值只补尚未显式选择的项，不改变旧层级顺序。
   internal/swarm/manager.go:345 `		cfg, _ := ResolveBeeConfig(ResolveInput{`
   internal/swarm/manager.go:1663 `	cfg, resolveNotices := ResolveBeeConfig(ResolveInput{`
   internal/swarm/manager.go:1680 `	// 宿主代码给的覆盖项在档位解析之后生效：只覆盖非空字段，DirectModel 保持原值。`
   internal/swarm/manager.go:1210 `	provider, model, effort := resolveBeeRuntime(spec.Provider, spec.Model, spec.Effort, settings)`
   internal/swarm/manager.go:1219 `		Provider: spec.Provider,`
4. spec.ReadOnly 为真时强制 SessionRequest.RuntimeMode=readonly，其他档保留继承/宿主模式；deep.WritePaths 不变。StartSession 创建后、SendMessage 之前写入 helper/parent。Manager 原有对泛型 driver/persister 的 SetThreadRelation 保留。
   internal/swarm/manager.go:1224 `		RuntimeMode: spec.RuntimeMode,`
   internal/remote/kanban_driver.go:87 `	threadID := res.ThreadID`
   internal/remote/kanban_driver.go:122 `	_, err = b.SendMessage(ctx, wire.SendMessageParams{`
   internal/swarm/manager.go:1261 `			_ = s.SetThreadRelation(ctx, threadID, leadThreadID, true)`
5. 设置页读取旧胶囊选择时用同一 helper 转成真实字段供显示与编辑；新建/保存继续使用现有 prov.ID/m.ID。无需新组件、无需重做目录、无需写配置迁移或迁线程/session。
   internal/ui/swarm_model_capsule.go:46 `		sel = resolveModelPickerSelection(providers, sel)`
   internal/ui/settings_swarm_tiers.go:334 `								uiSt.draft.Provider = next.Provider`
   internal/ui/settings_swarm_tiers.go:335 `								uiSt.draft.Model = next.Model`

**允许改动的文件**

- 新增：internal/apisource/bee_model.go、internal/apisource/bee_model_test.go、internal/app/swarm_engine.go、internal/app/swarm_engine_test.go。
- 既有：internal/app/app.go、internal/app/swarm_wiring.go、internal/app/swarm_wiring_test.go、internal/swarm/swarm.go、internal/swarm/profile.go、internal/swarm/profile_test.go、internal/swarm/manager.go、internal/swarm/manager_tiers_test.go、internal/remote/kanban_driver.go、internal/ui/swarm_model_capsule.go、internal/ui/swarm_model_capsule_test.go。
- 新增集成测试：internal/remote/bee_engine_session_test.go。无需修改 config.DefaultSwarmTiers、已有线程记录与新选择器存储代码。

**测试**

- 纯函数表：老 mcode/magpie-jp/auto 模型 → 对应 Endpoint 的真实 kind + 原样 auto/模型；端点名冲突分配 ProviderID；magpie 老槽位；custom-id；Anthropic；未知前缀；显式 pi/CLI 不动；裸模型不随意拆段。
- 解析优先级：空配置、tier、Profile、全局默认、会话模型、pair/thread override、DirectModel、host override；只配 provider 时不混入其他来源模型。TierPreview/Bee 记录/实际 StartSession 参数一致。
- 真实 MemoryBackend + 假 API harness：API lead 无 CLI 完成派发；CLI lead 无显式 bee 配置时选 API；API 不可用时报明确错误；显式 pi 仍走 pi。
- 首次 Run 中立即发审批，断言 parent/helper 在审批前已经设置；ReadOnly tier 创建为 readonly；deep 仍可写允许的方案路径。UI 旧值显示真实来源，新选值保存真实字段，配置无自动批量重写。

**验收标准**

- 没装 CLI、API lead 已就绪时，默认 bee 能完成并上报；全局默认不再出现预览/实跑偏差；显式 CLI 兼容；旧档位无须用户重填。
- 最终 bee 配置能在档位预览、会话摘要和真实 harness 上对应；不迁旧线程，不新增 mcode UI 入口。
- 首轮审批不受关系标记竞态影响；只读档 runtimeMode 正确，配合 C 保证写工具直接失败。

### 任务 B：补异步 panic 边界、后台任务拥有者和取消收尾

**现状**

- Run 和并发工具已有 recover；MCP 发现与 shell worker/输出回调尚有未隔离边界；后台任务全局共享且取消后存活。
  internal/providers/mcode/mcode.go:131 `		if recovered := recover(); recovered != nil {`
  internal/agent/mcp/tools.go:162 `		go func(index int, server Server) {`
  internal/agent/shell/background.go:93 `	backgroundJobs = map[string]*backgroundJob{}`
  internal/agent/shell/background.go:109 `// 任务不绑定 ctx 取消：本轮结束后进程继续运行，可用 Kill 停止。`

**剩余步骤**

1. MCP 发现 worker 增加局部恢复，转成带错误的 loadedServer，始终 Done。shell 自建 worker 和 outputWriter.Write 的用户回调边界恢复成错误/退出状态，保证 done、PID 注销、cleanup 的顺序，不因恢复而让 stopProcess 等不到结果。正常 Stream/emit 和并发只读工具的 recover 无需改动。
   internal/agent/mcp/tools.go:163 `			defer wg.Done()`
   internal/agent/mcp/tools.go:164 `			loadedServers[index] = discoverServer(ctx, server)`
   internal/agent/shell/shell.go:110 `		w.onOutput(string(p))`
   internal/agent/shell/shell.go:209 `	go func() { done <- cmd.Wait() }()`
   internal/agent/shell/background.go:187 `		job.done <- err`
   internal/agent/shell/background.go:192 `			cleanup()`
2. 给后台任务登记 threadID 拥有者，沿 harness.withProcessTracker 的上下文传递；提供有 ctx/owner 校验的 Output/Kill，以及按 owner 终止全部任务的方法。无 owner 的底层调用保留当前语义，避免破坏独立 shell 使用者；真实引擎工具必须带 owner，不允许越过拥有者校验。
   internal/providers/mcode/shell.go:97 `	return shell.WithProcessTracker(ctx,`
   internal/agent/shell/background.go:171 `	backgroundJobs[id] = job`
   internal/agent/tools/bash.go:140 `	text, running, exitCode, err := shell.Output(args.ID)`
   internal/agent/tools/bash.go:165 `	if err := shell.Kill(args.ID); err != nil {`
3. harness.Run 在执行前注册收尾：ctx 取消、panic 或 Transient 会话结束时清理该 threadID 的后台任务；普通线程正常结束保留。注意 defer 的后进先出顺序：当前最外层 recover 最后执行，新增清理 defer 不能在恢复前只检查 retErr 来判断 panic；应调整顺序或由 recover 显式通知清理。不要清空其他线程的 pids，不要先 EvictThread 再杀进程。复用 shell 的单任务 killMu 与进程组停止，避免多个清理者争抢 done。
   internal/providers/mcode/mcode.go:126 `func (h *Harness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) (retErr error) {`
   internal/providers/mcode/mcode.go:238 `	runCtx := h.withProcessTracker(ctx, turn.ThreadID)`
   internal/agent/shell/background.go:218 `	job.killMu.Lock()`
   internal/agent/shell/background.go:223 `	err := stopProcess(job.cmd, job.done)`
   internal/providers/mcode/shell.go:164 `	delete(h.pids, threadID)`
4. 保留前台进程组停止和 ProcessRootPIDs 的现有实现；只补异步边界与后台归属/收尾。Unix 负 PID 终止、Windows taskkill 继续复用，禁止 cgo。日志清理后台任务也要有明确的局部错误出口，不能把整个进程崩溃当作日志清理失败处理。
   internal/agent/shell/shell_unix.go:28 `	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)`
   internal/agent/shell/shell_windows.go:30 `	killer := procattr.HideWindow(exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)))`
   internal/agent/shell/shell.go:125 `		shellLogCleanupOnce.Do(func() { go cleanupOldShellLogs(dir) })`

**允许改动的文件**

- internal/providers/mcode/mcode.go、internal/providers/mcode/shell.go、internal/providers/mcode/mcode_test.go、internal/providers/mcode/shell_test.go。
- internal/agent/mcp/tools.go；新增 internal/agent/mcp/tools_panic_test.go。
- internal/agent/shell/shell.go、internal/agent/shell/background.go、internal/agent/shell/shell_test.go、internal/agent/shell/background_test.go；必要时 internal/agent/shell/shell_unix.go、internal/agent/shell/shell_windows.go。
- internal/agent/tools/bash.go；新增 internal/agent/tools/bash_owner_test.go。

**测试**

- panic：同步 Stream、串行工具、并行只读工具原测试继续通过；新增 MCP 发现 panic、shell 输出回调 panic、后台 cleanup/onExit panic，断言错误可见、没有挂住或二次 panic，其他 bee 仍可完成。
- Unix 真实子进程：两个线程各启动后台 bash，包含忽略 TERM 的子进程；取消 A 后 A 进程组消失、A 的 ProcessRootPIDs 清空、B 保持运行；重复停止不死锁。
- 归属：A 不能用 B 的 bgID 读输出或停止；同线程跨轮可读取其正常保留的后台任务。底层无 owner 的 Start 仍满足原来的 survives-cancel 测试。
- Transient 完成后无后台进程；普通正常结束后后台仍在；普通线程中断/引擎 panic 后后台被清理。执行相关包 race 测试，检查并发 Stop/Kill/Wait/重注册。

**验收标准**

- StopBee/StopAll 和蜂群正常完成均不遗留该 bee 的后台 bash；不会停止其他 bee 的进程；普通聊天跨轮后台能力保留。
- 引擎控制的异步 worker 和回调 panic 都被局部隔离，退出通知、错误结果和 PID 注销完整。
- 无需每 bee 新建 harness；前台停止路径与会话目录不变。

### 任务 C：readonly 在引擎门禁内直接拒绝写工具

**现状**

- Decide 已硬拒绝，但引擎用 gate.Check；gate 对 readonly 写工具返回 Ask，随后可能依赖 remote helper 拒绝。应将 readonly 的语义放到实际执行前的 gate 中。
  internal/harness/permission.go:142 `		return DecisionDeny`
  internal/agent/gate/gate.go:210 `	return Verdict{Action: Ask, Request: req, SessionKey: key}`
  internal/agent/agent.go:428 `	if verdict.Action == gate.Deny {`

**剩余步骤**

1. gate.Check 对 ModeReadOnly、非明确只读调用返回 Deny，放在可被 Session.Remember 或审批放行之前。保留敏感内容规则，允许已有 IsLookOnly、严格识别的只读 bash 和取网页；write/edit/apply_patch、修改性 bash、未知 MCP 调用一律拒绝。
   internal/agent/gate/gate.go:164 `	reads := IsLookOnly(in.ToolName) || kind == harness.RequestKindCommand && onlyReads(command) || webFetch`
   internal/agent/gate/gate.go:177 `		if IsLookOnly(in.ToolName) || in.Mode == harness.ModeFullAccess || webFetch && (in.Mode == harness.ModeAuto || in.Mode == harness.ModeAutoAcceptEdits || in.Mode == harness.ModeReadOnly) || (in.Mode == harness.ModeAuto || in.Mode == harness.ModeAutoAcceptEdits) && kind == harness.RequestKindFileChange || (in.Mode == harness.ModeAuto || in.Mode == harness.ModeAutoAcceptEdits || in.Mode == harness.ModeReadOnly) && kind == harness.RequestKindCommand && onlyReads(command) {`
   internal/agent/agent.go:431 `	if verdict.Action == gate.Ask && !opts.Gate.Allowed(verdict.SessionKey) {`
2. 对 MCP 已有只读注解的工具，需要把 tool.ReadOnly 作为 gate.Input 的可选只读提示传入，避免所有非内置名字都误判为写。提示仅证明已解析工具的属性，不能由模型参数伪造；外部网关若尚无明确只读声明，保持拒绝。kill_bash 继续受任务 B 的拥有者校验，不能因为它在 IsLookOnly 中就绕过归属。
   internal/agent/mcp/tools.go:185 `					descriptor.Annotations.ReadOnlyHint,`
   internal/agent/agent.go:419 `	verdict := gate.Check(gate.Input{Mode: opts.Mode, Plan: opts.Plan, HardLocked: opts.HardLocked, HardLockOverride: opts.HardLockOverride, ToolName: call.Name, ToolID: call.ID, Args: call.Arguments, WorkDir: opts.Env.WorkDir, AdditionalDirs: opts.Env.AdditionalDirs})`
   internal/agent/gate/gate.go:109 `	case "read", "list_files", "search_text", "grep", "find", "ls", "mcode_mcp_list", "ask_user_question", "todo_write", "vision", "bash_output", "kill_bash":`
3. 保持 auto/fullAccess 的现有行为与 remote 无人值守审批规则；无需修改 harness.Decide，也不把所有 bee 升为 fullAccess。配合 A 的 RuntimeMode 与关系标记修复，验证 readonly 工具失败不会创建待审批项；提问自动转交仍是独立交互流程。
   internal/agent/agent.go:429 `		return tools.Result{Text: verdict.Reason, IsError: true}`
   internal/remote/approvals.go:248 `			return harness.ApprovalResponse{Decision: harness.DecisionAllow}, nil`

**允许改动的文件**

- internal/agent/gate/gate.go、internal/agent/gate/gate_test.go、internal/agent/agent.go。
- 新增 internal/agent/readonly_tools_test.go。无需修改 internal/harness/permission.go、internal/remote/approvals.go，也不改 A/B 的文件。

**测试**

- gate 表：readonly write/edit/apply_patch/修改性 bash/未知 MCP 为 Deny；read/list_files/search_text/明确只读 MCP/只读 bash 为 Allow，敏感读遵守既有敏感规则；记住过的授权不能放行 readonly 写调用。
- agent 假工具 + 计数 Approver：readonly 写工具返回工具错误、实现不执行、Approve/Interact 调用次数为 0，模型可继续输出结论。
- auto/fullAccess、plan/hard-lock 现有用例不退化。A 合入后在真实 backend 验证 ReadOnly bee 的首轮写调用不产生 pending。

**验收标准**

- readonly 写操作在引擎执行前直接拒绝，不依赖等用户审批；ReadOnly 档运行时强约束与事后检查一致。
- 只读调研仍可正常读文件、搜索、运行明确只读命令和使用有只读声明的工具。

## 6. 优先级与未完成事项

- 建议最先派发任务 B：前台停止已完善，但后台取消后存活和异步 panic 都直接破坏「bee 失败不拖垮 app、停止后无遗留进程」的验收；A、C 可以并行启动。A/C 必须联合验收只读档，不能只合其中一半。
  internal/agent/shell/background.go:109 `// 任务不绑定 ctx 取消：本轮结束后进程继续运行，可用 Kill 停止。`
  internal/agent/mcp/tools.go:162 `		go func(index int, server Server) {`
- 明确无需改动：同步 Run recover、并行只读工具 recover、前台进程组停止、harness 的核心并发 map、会话存储目录、额度 key、新档位选择器存储规则、harness.Decide 的 write/edit 表。证据已分别列在第 2/3 节，不重复实现。
- 本调研没有访问用户实际 config.json；brief 中的 mcode/magpie-jp/auto 作为旧配置形状分析，不能声称用户当前所有档位都已跑引擎。是否存在坏 endpoint、过期模型或重复 ProviderID，仅在后续配置驱动测试与实际派发中核实。
- 尚未完成：上述 A/B/C 的实现、所有测试和真实 UI/API 验收；通用新建 lead 的 API 默认来源仍属于阶段 5。没有运行任何验证，也没有遗留由本调研启动的后台任务。
