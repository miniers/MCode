# mcode 旧配对的模型映射修复方案

本次只设计方案，不修改业务代码。推荐选择 **(b)：在 UI 就绪检查中解析旧配对，保留原配对作为匹配及套用值**。运行时已经支持旧来源映射，不需要为了恢复两条配对而迁移用户配置、历史会话和档位配置。

## 已确认的代码事实

- 普通候选目录整体隐藏 mcode。

  internal/ui/model_picker.go:142 `		if provider.ID == "mcode" {`

- 配对标题先输出，之后才跳过含 mcode 的配对，最后无条件输出模型标题。因此原始列表非空、实际行数为零时仍有空分组。

  internal/ui/model_picker.go:2001 `								renderProviderGroupHeader(c, p.KeyPrefix+"pair-group-header", "配对")`

  internal/ui/model_picker.go:2003 `									if pair.LeadProvider == "mcode" || swarmWorkerProvider(pair) == "mcode" {`

  internal/ui/model_picker.go:2020 `								renderProviderGroupHeader(c, p.KeyPrefix+"pair-models-header", "模型")`

- 旧模型首段是注入来源别名，不一定是配置端点 ID；不能直接给 `magpie-jp` 加 `custom-`。

  internal/magpieinject/magpieinject.go:51 `		providerID := magpie.ProviderID(ep.Name, taken)`

  internal/magpieinject/magpieinject.go:76 `			Endpoint:   ep.ID,`

  internal/magpieinject/magpieinject.go:79 `			ProviderID: providerID,`

  internal/providers/magpie/magpie.go:479 `func ProviderID(name string, taken map[string]bool) string {`

- 新来源以稳定的配置端点 ID 注册。普通自定义端点是 `custom-<ep.ID>`；旧内置 Magpie 槽位 `magpie`、`magpie2`、`magpie3`、`magpie4` 保留原 ID。

  internal/config/config.go:1429 `	return "custom-" + id`

  internal/config/config.go:1437 `		case "magpie", "magpie2", "magpie3", "magpie4":`

  internal/config/config.go:1441 `	return CustomEndpointKind(ep.ID)`

  internal/apisource/source.go:251 `		Kind:           config.CustomEndpointProviderKind(ep),`

- 新来源目录保留裸网关模型 ID；旧 mcode 目录仅增加注入别名前缀，两者使用同一端点模型目录。

  internal/app/api_catalog.go:31 `							return orderCatalog(magpieCatalogTarget(target), source.DefaultModel)`

  internal/app/api_catalog.go:67 `			ID: model.ID, Name: magpie.ShortName(model), Efforts: mcodeEfforts(model),`

  internal/app/mcode_agent.go:354 `			row.ID = target.ProviderID + "/" + row.ID`

  internal/remote/mcode_catalog.go:7 `	SetAPICatalog("mcode", fn)`

  internal/remote/api_catalog.go:15 `func SetAPICatalog(kind string, fn func() []wire.ProviderModel) {`

  internal/remote/models.go:427 `	if rows, registered := apiCatalog(provider); registered {`

- **已有映射函数是 `apisource.NormalizeBeeModel`**。它先把模型按第一个 `/` 切开，再按 `Target.ProviderID → Target.Endpoint → CustomEndpointProviderKind` 找到唯一启用端点。返回的模型保留后续所有 `/`，所以 `auto/deepseek-v4.1-flash` 不可再拆。

  internal/apisource/bee_model.go:11 `func NormalizeBeeModel(cfg config.Config, secrets config.SecretStore, provider, model string) (string, string, bool) {`

  internal/apisource/bee_model.go:15 `	head, rest, ok := strings.Cut(model, "/")`

  internal/apisource/bee_model.go:28 `			if target.ProviderID != head {`

  internal/apisource/bee_model.go:32 `				if ep.ID == target.Endpoint && ep.Enabled && ep.Magpie {`

  internal/apisource/bee_model.go:39 `			return kind, rest, true`

- UI 已有无凭据、只读映射的先例。Magpie 的别名分配不依赖密钥，调用时传 `nil` 即可，无需拉取网络目录或访问 keyring。

  internal/ui/swarm_model_capsule.go:30 `		sel.Provider, sel.Model, _ = apisource.NormalizeBeeModel(st.cfg, nil, sel.Provider, sel.Model)`

  internal/magpieinject/magpieinject.go:68 `		apiKey := ""`

## 现场模型对应关系与证据限制

任务提供的运行模型 `custom-432db46a/auto/deepseek-v4.1-flash` 应解释为 provider `custom-432db46a`、model `auto/deepseek-v4.1-flash`。若它确实来自所述旧配对，则配置端点 ID 是 `432db46a`，该端点名称生成的注入别名是 `magpie-jp`。这两个字符串属于同一个端点的不同命名空间，不是两个模型，也不是端点 ID 改名。

| 旧 provider | 旧 model | 新 provider（按任务提供的运行来源） | 新 model |
| --- | --- | --- | --- |
| mcode | magpie-jp/auto/deepseek-v4.1-flash | custom-432db46a | auto/deepseek-v4.1-flash |
| mcode | magpie-jp/auto/gpt-6.1-terra | custom-432db46a | auto/gpt-6.1-terra |
| mcode | magpie-jp/auto/gpt-6.1-sol | custom-432db46a | auto/gpt-6.1-sol |

注意：读取用户配置的只读工具调用被拒绝，本任务没有重试或绕过，也没有修改配置。因此未直接核实真实端点记录；表中的具体端点关联依据任务提供的运行模型与上述代码推导。实现必须调用现有解析函数，不能硬编码 `432db46a`，也不能只凭模型后缀猜测来源。主代理如需确认现场关联，需在其已授权环境中核对端点 ID、名称、启用状态及重复名称顺序。

## 后端是否还支持旧配对

- **配对匹配仍支持旧值，但不比较新旧别名的等价性。** 配置转换保留 `WorkerProvider/WorkerModel`；`MatchPair` 对 provider/model 做字符串比较。旧配对与旧会话偏好一致时能够命中。若只把 UI 套用值改成 `custom-432db46a + auto/...`，配置仍是 `mcode + magpie-jp/auto/...`，显式 PairID 也会因模型不匹配而失效，进而丢失配对档位覆盖。

  internal/app/swarm_wiring.go:102 `			BeeProvider:  p.WorkerProvider,`

  internal/remote/memory_backend.go:4859 `			BeeProvider:  p.WorkerProvider,`

  internal/swarm/pairs.go:86 `			if workerProvider == beeProvider && workerModel == beeModel {`

  internal/remote/memory_backend.go:4833 `	if p, ok := swarm.FindPair(pairs, explicitID); ok && swarm.PairStillMatches(p, leadProvider, leadModel, beeProvider, beeModel) {`

- **套用旧配对仍能保存旧 worker 值。** UI 直接把原字段写进会话偏好；后端持久化这些字段，没有在这里拒绝 mcode 或强制改成新来源。

  internal/ui/composer.go:1714 `		prefs.BeesProvider = p.WorkerProvider`

  internal/ui/composer.go:1715 `		prefs.BeesModel = p.WorkerModel`

  internal/remote/memory_backend.go:4657 `	td.summary.SwarmBeesProvider = optionalString(p.BeesProvider)`

  internal/remote/memory_backend.go:4658 `	td.summary.SwarmBeesModel = optionalString(p.BeesModel)`

- **bee 派发已经映射旧 mcode worker。** app 注入的 `NormalizeBee` 使用最新配置解析旧值；`ResolveBeeConfig` 每层应用字段前调用它，Manager 在生成 BeeSpec 前再次归一化。只要别名仍能唯一对应启用端点、来源可用且凭据有效，旧配对会生成直连 provider 的 bee，无需提前迁移配置。

  internal/app/app.go:437 `			NormalizeBee: func(provider, model string) (string, string) {`

  internal/app/app.go:443 `				provider, model, _ = apisource.NormalizeBeeModel(cfg, config.NewSecretStore("mcode"), provider, model)`

  internal/swarm/profile.go:123 `			provider, model = in.NormalizeBee(provider, model)`

  internal/swarm/manager.go:1719 `		cfg.Provider, cfg.Model = m.opts.NormalizeBee(cfg.Provider, cfg.Model)`

  internal/swarm/manager.go:1743 `		Provider:    cfg.Provider,`

- **bee_tiers 同样已经受支持。** 配对档位原样转入运行时；会话选中的配对按档位应用覆盖时也走 `apply → NormalizeBee`；用户授权的显式配对派发同样走这条路径。档位的 effort、fast mode 与优先级不需要改动。

  internal/app/swarm_wiring.go:106 `			BeeTiers:     swarmBeeTiersFromConfig(p.BeeTiers),`

  internal/swarm/profile.go:188 `					apply(ov.Provider, ov.Model, ov.Effort)`

  internal/swarm/profile.go:215 `						apply(ov.Provider, ov.Model, ov.Effort)`

- 旧 mcode harness 本身也保留前缀解析，用于已有主线程；这与 bee 派发前转换到直连来源是两条兼容路径。

  internal/app/mcode_agent.go:150 `	if target, modelID, ok := magpieinject.ProviderOf(model, targets); ok {`

以上是静态代码结论，不是本任务已运行的验证结果。

## 编号实施步骤

1. **把现有解析函数接入 `readySwarmPairs`，只改变就绪判定。**

   锚点：

   internal/ui/model_picker.go:615 `func readySwarmPairs(pairs []SwarmPair, providers []ProviderInfo, pins []ModelPin) []SwarmPair {`

   internal/ui/model_picker.go:618 `		leadProv, ok := findProviderInfo(providers, p.LeadProvider)`

   internal/ui/model_picker.go:622 `		workerProv, ok := findProviderInfo(providers, swarmWorkerProvider(p))`

   internal/ui/model_picker.go:626 `		out = append(out, p)`

   给 `readySwarmPairs` 增加一个显式 `cfg config.Config` 参数，并导入 `internal/apisource`。对每条配对使用局部 provider/model 变量调用 `apisource.NormalizeBeeModel(cfg, nil, provider, model)`。Lead 与 Worker 的显式 mcode 来源都解析；WorkerProvider 为空时沿用现有 `swarmWorkerProvider` 回退规则。WorkerModel 为空时保留现有跟随规则，解析 mcode 时使用其有效跟随模型，不写回配对。

   对解析后的 provider/model 使用原有 `findProviderInfo` 和 `modelUsable` 检查。只要求主、执行两侧就绪，不因某个 bee_tiers 模型暂未加载而隐藏整条配对。保留普通模型的空值、CLI 动态模型及 pin 放行语义；映射成功后，原始 provider/model 的 pin 也应继续放行模型检查，避免旧 mcode pin 因来源表示改变而失效，但 pin 不得绕过目标 provider 必须存在的检查。对于旧 mcode 侧，需要映射成功且目标 provider 在候选目录存在，不以 mcode 的占位目录兜底猜测目标。mcode 空模型没有可唯一解析的端点时暂不展示。

   返回值仍追加原始 `p`；不修改 `Pairs` 切片元素、`BeeTiers` map、effort/fast 指针或配置。**禁止把解析副本作为套用参数返回**：原值负责 PairID 匹配及配置覆盖，派发时现有 NormalizeBee 再生成直连值。Lead 为 mcode 时，除映射目标就绪外，还应保留对原 mcode provider 的存在检查，避免显示无法通过 `SetThreadAgent` 套用的主代理组合。

   新旧 provider 的普通模型列表仍执行既有过滤。配对行使用原始兼容目录中的模型名称与现有标签函数，不新增第二套展示状态或配置迁移版本。

2. **在 composer 读取配置快照并传给就绪检查，保留后续匹配与套用值。**

   锚点：

   internal/ui/composer.go:1908 `	pairs := readySwarmPairs(swarmPairsFromSettings(s), providers, pinnedModelPins(s))`

   internal/ui/composer.go:1913 `		if pair, ok := currentSwarmPairFor(pairs, currentThread.PairID, currentProvider, leadSelection.Model, currentThread.BeesProvider, currentThread.BeesModel); ok {`

   internal/ui/composer.go:2047 `			s.applySwarmPair(threadID, pair)`

   internal/ui/composer.go:2398 `func swarmPairsFromSettings(s *chatState) []SwarmPair {`

   把 `swarmPairsFromSettings` 的签名改为接收 `providers []ProviderInfo` 和 `pins []ModelPin`。在函数内按 `cfgMu` 现有约定读取 `st.cfg` 快照，再调用第 1 步的新 `readySwarmPairs` 签名，返回就绪的原始配对。将 composer 调用改为 `pairs := swarmPairsFromSettings(s, providers, pinnedModelPins(s))`。这样配置与配对来自同一次设置读取。设置尚未加载时沿用空列表行为。更新函数注释为“返回当前目录就绪的原始配对”。不要为此访问磁盘或 SecretStore。

   `currentSwarmPairFor`、`OnApplyPair`、`applySwarmPair`、待创建会话路径均继续接收原配对。这样旧会话偏好仍与配置匹配，现有 PairID、bee_tiers、会话级覆盖及记忆行为不改变。

   同步修改已存在的 `readySwarmPairs` 测试调用，传入空配置或测试端点配置。

   internal/ui/composer_model_pair_test.go:185 `	ready := readySwarmPairs(pairs, providers, nil)`

   internal/ui/model_picker_test.go:345 `	if ready := readySwarmPairs(pairs, providers, pins); len(ready) != 1 || ready[0].ID != "pinned-lead" {`

   internal/ui/model_picker_test.go:348 `	if ready := readySwarmPairs(pairs, providers, nil); len(ready) != 0 {`

3. **删除配对行的 mcode 一刀切过滤，按实际渲染行数输出分组标题。**

   锚点：

   internal/ui/model_picker.go:1847 `func renderModelPickerListPage(c *ui.Context, p ModelPickerProps, state *ModelPickerState, side ModelPickerSide) {`

   internal/ui/model_picker.go:1996 `							if p.Mode == ModelPickerSwarm && side == ModelPickerSideLead && len(p.Pairs) > 0 && !searching {`

   internal/ui/model_picker.go:2001 `								renderProviderGroupHeader(c, p.KeyPrefix+"pair-group-header", "配对")`

   internal/ui/model_picker.go:2003 `									if pair.LeadProvider == "mcode" || swarmWorkerProvider(pair) == "mcode" {`

   internal/ui/model_picker.go:2018 `									renderModelListPairRow(c, p, fmt.Sprintf("%spair-row-%d", p.KeyPrefix, idx), pair, selected, onEnter)`

   internal/ui/model_picker.go:2020 `								renderProviderGroupHeader(c, p.KeyPrefix+"pair-models-header", "模型")`

   删掉循环内对 mcode 的 `continue`，让就绪配对统一进入原有行渲染与回调。标题使用局部实际行计数：只有即将渲染第一条有效行时输出“配对”；每渲染一行递增计数；循环结束仅在计数大于零时输出“模型”。外层仍保留 Swarm、Lead、非搜索三个条件。不要再把 `len(p.Pairs) > 0` 当作标题显示的充分条件。

   保持现有 row key、点击后 Reset/关闭浮层、当前配对高亮行为。`filterModelPickerMagpie` 继续隐藏普通列表中的 mcode，避免重新引入重复模型目录。更新 `ModelPickerProps.Pairs` 注释，明确传入已完成就绪检查的原始配对列表。

4. **补充 UI 与映射回归测试。**

   既有测试入口：

   internal/apisource/bee_model_test.go:9 `func TestNormalizeBeeModel(t *testing.T) {`

   internal/ui/composer_model_pair_test.go:161 `func TestReadySwarmPairs(t *testing.T) {`

   internal/ui/composer_model_pair_test.go:260 `func TestApplySwarmPair_SwitchesLeadAndWritesBeesPrefs(t *testing.T) {`

   internal/ui/model_picker_test.go:796 `func TestModelPickerExcludesMcode(t *testing.T) {`

   - 更新 `internal/apisource/bee_model_test.go`：配置端点 `ID: "432db46a", Name: "Magpie JP", Enabled: true, Magpie: true`，不传 secrets；验证 `mcode + magpie-jp/auto/deepseek-v4.1-flash` 得到 `custom-432db46a + auto/deepseek-v4.1-flash`，验证 terra/sol 同理；覆盖旧槽位 ID、未知前缀、禁用端点、带后续 `/` 的模型、CLI 来源不拆分。重复端点名称按现有 taken 后缀规则处理，不添加字符串替换规则。
   - 更新 `internal/ui/composer_model_pair_test.go`：候选目录仅含 Claude 和直连端点时，worker 为 mcode 的两条旧配对仍就绪；返回的 worker 字段和 BeeTiers 保持原样。缺少目标 provider、未知别名、禁用端点时不就绪。验证继承 WorkerProvider/WorkerModel、CLI 与 pin 旧行为不变；特别覆盖只有原 mcode pin 的模型仍可通过就绪检查，但目标端点缺失时不能仅靠 pin 显示配对。
   - 在同文件补充实际点击/套用用例：旧配对行可见，点击后 Lead 按原逻辑切换、PairID 正确、Bees 偏好保存原始 `mcode + magpie-jp/auto/...`；高亮匹配仍命中；第二条配对的 bee_tiers 未被 UI 修改。验证待创建会话也携带原 PairID 与 worker 字段。
   - 更新 `internal/ui/model_picker_test.go`：用 Tester 打开 Swarm 主导列表；零就绪配对时两个分组标题 key 都不存在；一条和两条旧配对就绪时有对应行且两个标题各出现一次；有效与无效配对混合时只显示有效行；搜索、Bees 页及 Simple 模式不显示配对分组。继续保留 `TestModelPickerExcludesMcode`，断言普通 provider 列表仍没有 mcode。
   - 回归测试不要靠完整 UI 文案猜测标题，可按 `pair-group-header`、`pair-models-header`、`pair-row-*` 的元素 key 验证，避免把模型名或其它“模型”文本误判成分组标题。

5. **补充后端兼容测试，不修改已工作的运行时映射。**

   既有入口：

   internal/remote/swarm_pair_effective_test.go:44 `func TestSwarmThreadContext_InfersPairFromModels(t *testing.T) {`

   internal/remote/swarm_pair_effective_test.go:68 `func TestSwarmThreadContext_ExplicitPairWinsOverInference(t *testing.T) {`

   internal/swarm/profile_test.go:139 `func TestResolveBeeConfig(t *testing.T) {`

   internal/swarm/tiers_test.go:181 `	t.Run("pair override", func(t *testing.T) {`

   - 更新 `internal/remote/swarm_pair_effective_test.go`：配置保留旧 mcode worker，会话经过 `SetThreadComposerPrefs` 保存同样旧值；显式 PairID 与无 PairID 推断均命中该配对。第二条配对必须保持选中，不能因另一个相同 Lead 配对抢先而丢失档位覆盖。
   - 更新 `internal/swarm/profile_test.go`：使用真实 `apisource.NormalizeBeeModel` 构造 `NormalizeBee` 回调，传入旧会话 worker；验证解析结果是直连 provider 与裸模型。另测用户明确授权配对的派发路径会解析配对的旧 worker 字段，且不改变授权要求。
   - 更新 `internal/swarm/tiers_test.go`：选中旧配对并使用其 `bee_tiers`，验证 sol 等模型解析到同一端点，effort/fast mode 保留；线程覆盖继续优先于配对覆盖。验证配对对象和 BeeTiers map 没有被归一化回写。

## 为什么不选一次性配置迁移

当前问题的业务运行链已兼容旧值，故 UI 就绪检查复用映射、渲染删除额外过滤即可闭环。一次性改写配置只解决了一半：已有会话和偏好仍可能是旧值，`MatchPair` 会因此失配。完整迁移还需要迁移或归一化比较所有历史会话侧值、处理默认来源和保存时机；这超出本次缺失配对行的修复范围。

配置层不能直接调用 `apisource.NormalizeBeeModel`，因为 apisource 已依赖 config；为这次 UI 修复挪动解析层会增加无必要的结构变更。UI 既有 `normalizeSwarmSelection` 采用只读解析，沿用这一方式即可。

该选择的明确限制：用户以后手动将 Bees 改为新直连表示时，旧配置配对不会自动按别名等价性高亮；当前后端本来如此。不要在本补丁中悄悄把点击旧配对改为写新值。如后续要求“新旧表示均自动命中同一配对”，应另做统一匹配归一化并覆盖 UI、remote、swarm 三层，而不能只修改一个显示函数。

## 验证状态与注意事项

本任务只读取代码并新增本文件。没有修改用户配置或业务代码，没有执行构建、测试、lint、类型检查、git 操作或其它验证，没有启动后台任务。上面列的是实现时需要新增/更新的测试，不代表测试已通过。现场端点记录因只读工具拒绝未能直接核实；主代理需保留此证据限制。
