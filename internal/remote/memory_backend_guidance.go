// 本文件包含 MemoryBackend 的配置与记忆注入方法。
package remote

import (
	"log"
	"mcode/internal/agentconf"
	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/memspace"
	"mcode/internal/nmem"
	"mcode/internal/projctx"
	"mcode/internal/remote/wire"
	"os"
	"strings"
)

// SetPromptDecorator 设置发送给 Provider 之前的 Prompt 装饰钩子。
func (b *MemoryBackend) SetPromptDecorator(fn func(threadID, prompt string) string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.promptDecorator = fn
}

// SetSystemPolicyFunc 设置追加到 Provider 系统提示的静态规则生成函数。
func (b *MemoryBackend) SetSystemPolicyFunc(fn func(threadID string) string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.systemPolicyFunc = fn
}

// CarriesSystemPolicy 按会话当前的 Provider 路由判断系统规则承载能力。
func (b *MemoryBackend) CarriesSystemPolicy(threadID string) bool {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	var h harness.Harness
	if ok {
		h = b.threadHarnessLocked(td.summary.Provider)
	}
	b.mu.Unlock()
	carrier, ok := h.(harness.SystemPolicyCarrier)
	return ok && carrier.CarriesSystemPolicy(threadID)
}

// SetNowledge 注入 Nowledge Mem 客户端。为 nil 时所有 Nowledge 功能（首轮 Working Memory
// 注入、会话同步）一律关闭，不发起任何网络请求。
func (b *MemoryBackend) SetNowledge(c *nmem.Client) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nowledge = c
}

// SetLocalMemory 注入本地记忆的全局规则生成函数。
func (b *MemoryBackend) SetLocalMemory(guidance func(maxRunes int) string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.localMemoryGuidance = guidance
}

// nowledgeClient 返回已注入的 Nowledge 客户端；未注入时返回 nil。
// 只持锁取出指针，实际请求在锁外进行。
func (b *MemoryBackend) nowledgeClient() *nmem.Client {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nowledge
}

// turnMemoryEnv 返回本轮额外注入的环境变量：本地记忆后端自己管理存储空间，不需要
// Nowledge Mem 的 NMEM_SPACE；只有未注册本地回调（可继续使用本机 nmem CLI）时才按项目目录
// 注入该变量。
func (b *MemoryBackend) turnMemoryEnv(workDir string) []string {
	b.mu.Lock()
	guidance := b.localMemoryGuidance
	b.mu.Unlock()
	if guidance != nil {
		return nil
	}
	return memspace.Env(workDir)
}

// SetPersonaTagResolver 设置线程人设的标签前缀解析钩子：按会话的 provider/model 把 persona
// 换算成拼在 Turn.Prompt 最前面的前缀，返回空串表示本轮不注入；nil 或未设置时不注入。
func (b *MemoryBackend) SetPersonaTagResolver(fn func(provider, model, persona string) string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.personaTag = fn
}

// SetGuidanceBuilder 覆盖首轮项目 Context 指导词的构建函数，便于测试注入。
func (b *MemoryBackend) SetGuidanceBuilder(fn func(workDir string) string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.guidanceBuilder = fn
}

// SetFirstTurnExtra 设置随首轮注入一起前置的额外段落（普通会话里「可以交给总管」的提示），
// 返回空串表示本轮不附加；nil 表示永不附加。只在本轮触发首轮指导词注入时生效。
func (b *MemoryBackend) SetFirstTurnExtra(fn func(threadID string) string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.firstTurnExtra = fn
}

// defaultGuidanceBuilder 组装首轮注入的内容：项目 Context 指导词加技能精简目录（描述放宽到
// projctx.LeadSkillIndexDescriptionMaxRunes 个字符），空的部分省略。
func (b *MemoryBackend) defaultGuidanceBuilder(workDir string) string {
	return b.buildGuidance(workDir, projctx.LeadSkillIndexDescriptionMaxRunes, false)
}

// beeGuidanceBuilder 组装派发给子代理（bee）的首轮注入内容：在项目 Context 指导词之外附带
// 一份精简技能目录（每个技能一行，描述截断），避免全量描述挤占 bee 的上下文，bee 需要时
// 自行读取对应 SKILL.md。
func (b *MemoryBackend) beeGuidanceBuilder(workDir string) string {
	return b.buildGuidance(workDir, beeSkillIndexDescriptionMaxRunes, true)
}

// buildGuidance 组装首轮注入的内容：项目 Context 指导词加技能段落，空的部分省略。
// maxSkillRunes 大于 0 时技能段落输出精简技能目录（描述按该值截断），等于 0 时输出带完整
// 描述的全量清单。forBee 为 true 时代表为子代理（bee）组装，跳过记忆工具说明等仅主会话适用的段落。
func (b *MemoryBackend) buildGuidance(workDir string, maxSkillRunes int, forBee bool) string {
	var parts []string
	workbench := b.workbenchSettings()
	if workbench.ContextGuidanceEnabled() {
		maxChars := workbench.ContextInjectMaxCharacters
		if maxChars <= 0 {
			maxChars = config.DefaultContextInjectMaxCharacters
		}
		guidance, err := projctx.BuildGuidanceWithOptions(workDir, projctx.GuidanceOptions{
			MaxChars:   maxChars,
			CustomDocs: workbench.ContextInjectCustomDocs,
		})
		if err == nil && strings.TrimSpace(guidance) != "" {
			parts = append(parts, guidance)
		}
	}
	if b.generalSettings().Session.SkillsInjectPromptEnabled() {
		if home, err := os.UserHomeDir(); err == nil {
			// 停用状态由设置页写入 <dataDir>/skills-projects/ 下，这里只做剔除，不改注入格式。
			disabled := b.disabledSkillPaths(home, workDir)
			var excluded func(path string) bool
			if len(disabled) > 0 {
				excluded = func(path string) bool { return disabled[normalizeSkillPath(path)] }
			}
			var skills string
			if maxSkillRunes > 0 {
				skills = projctx.BuildSkillsIndexWithLimitExcluding(workDir, home, excluded, maxSkillRunes)
			} else {
				skills = projctx.BuildSkillsGuidanceExcluding(workDir, home, excluded)
			}
			if strings.TrimSpace(skills) != "" {
				parts = append(parts, skills)
			}
		}
	}
	// Nowledge 已接入且非 bee 会话时，若配置开启则说明记忆工具的用法：用户要求记住的内容走 memory_propose，不写 agent 自己的记忆文件。
	if b.nowledgeClient() != nil && !forBee && b.nowledgeSettings().MemoryToolsNoteEnabled() {
		parts = append(parts, nowledgeMemoryToolsNote)
	}
	// 本地记忆后端：工具说明只给非 bee 会话，全局规则对 bee 与会话都注入（Working Memory 同样
	// 注入给 bee），空规则不追加；guidance 回调在锁外调用。
	if b.nowledgeClient() == nil {
		b.mu.Lock()
		guidance := b.localMemoryGuidance
		b.mu.Unlock()
		if guidance != nil {
			settings := b.nowledgeSettings()
			if !forBee && settings.MemoryToolsNoteEnabled() {
				parts = append(parts, localMemoryToolsNote)
			}
			if rules := guidance(settings.WorkingMemoryBudget()); strings.TrimSpace(rules) != "" {
				parts = append(parts, rules)
			}
		}
	}
	// Nowledge Working Memory 放在最后，作为项目文档与技能目录之后的背景参考。
	if wm := b.nowledgeWorkingMemory(workDir); wm != "" {
		parts = append(parts, wm)
	}
	return strings.Join(parts, "\n\n")
}

// disabledSkillPaths 返回设置页已停用技能的规范路径集合（键为 SKILL.md 所在目录）。
// 扫描参数与设置页一致（Home/ProjectDir/DataDir），保证读到同一份停用配置（项目级停用现在
// 存放在 <dataDir>/skills-projects/ 下，仍兼容读旧的 <项目>/.mcode/skills.json）；
// 扫描失败时返回 nil，由调用方不过滤（注入全部技能）。
func (b *MemoryBackend) disabledSkillPaths(home, workDir string) map[string]bool {
	b.mu.Lock()
	dataDir := b.dataDir
	b.mu.Unlock()

	skills, err := agentconf.ScanSkills(agentconf.Options{Home: home, ProjectDir: workDir, DataDir: dataDir})
	if err != nil {
		log.Printf("扫描技能停用状态失败，改为注入全部技能: %v", err)
		return nil
	}
	disabled := make(map[string]bool)
	for _, skill := range skills {
		// ScanSkills 已按 IsSkillActive 填好 Disabled（同时看项目级与全局 skills.json）。
		if skill.Disabled {
			disabled[normalizeSkillPath(skill.Path)] = true
		}
	}
	return disabled
}

// guidancePending 判断该线程是否仍应注入首轮指导词：尚未成功交付，且历史里还不存在
// 已获得成功回复的普通用户消息。只有正文回复（assistant/text）才算成功回复，错误、
// 思考与工具消息都不算；上一轮失败或中断时保持待注入，下一条普通消息会重试。
func (b *MemoryBackend) guidancePending(threadID string, priorHistory []wire.ThreadMessage) bool {
	b.mu.Lock()
	delivered := b.guidanceDelivered[threadID]
	b.mu.Unlock()
	if delivered {
		return false
	}

	ordinaryUserSeen := false
	answered := false
	for _, msg := range priorHistory {
		switch msg.Role {
		case "user":
			if isGuidanceTriggerText(msg.Text) {
				ordinaryUserSeen = true
			}
		case "assistant":
			if msg.Kind == nil || (*msg.Kind != "assistant" && *msg.Kind != "text") {
				continue
			}
			if ordinaryUserSeen && strings.TrimSpace(msg.Text) != "" {
				answered = true
			}
		}
	}
	return !answered
}

// markGuidanceDelivered 记录线程已成功交付首轮指导词。
func (b *MemoryBackend) markGuidanceDelivered(threadID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.guidanceDelivered == nil {
		b.guidanceDelivered = make(map[string]bool)
	}
	b.guidanceDelivered[threadID] = true
}

// SetGeneralSettingsFunc 设置通用设置获取函数（返回整份配置，调用方按段取用）。
func (b *MemoryBackend) SetGeneralSettingsFunc(fn func() config.Config) {
	if fn == nil {
		b.generalSettingsFunc.Store(nil)
		return
	}
	b.generalSettingsFunc.Store(&fn)
}

// generalSettings 返回当前通用配置（若未设置则返回零值配置）。
// 本函数不加 b.mu，可在持锁时调用，但传入的回调本身不得再获取 b.mu。
func (b *MemoryBackend) generalSettings() config.Config {
	if p := b.generalSettingsFunc.Load(); p != nil && *p != nil {
		return (*p)()
	}
	return config.Config{}
}

// SetWorkbenchSettingsFunc 设置工作台设置获取函数。
func (b *MemoryBackend) SetWorkbenchSettingsFunc(fn func() config.WorkbenchConfig) {
	if fn == nil {
		b.workbenchSettingsFunc.Store(nil)
		return
	}
	b.workbenchSettingsFunc.Store(&fn)
}

// SetNowledgeSettingsFunc 设置 Nowledge Mem 集成设置的获取函数（含两个开关、注入预算与空间覆盖）。
func (b *MemoryBackend) SetNowledgeSettingsFunc(fn func() config.MemoryConfig) {
	if fn == nil {
		b.nowledgeSettingsFunc.Store(nil)
		return
	}
	b.nowledgeSettingsFunc.Store(&fn)
}

// workbenchSettings 返回当前工作台配置（若未设置则返回零值配置）。
// 本函数不加 b.mu，可在持锁时调用，但传入的回调本身不得再获取 b.mu。
func (b *MemoryBackend) workbenchSettings() config.WorkbenchConfig {
	if p := b.workbenchSettingsFunc.Load(); p != nil && *p != nil {
		return (*p)()
	}
	return config.WorkbenchConfig{}
}

// nowledgeSettings 返回当前 Nowledge Mem 集成设置（若未设置则返回零值配置）。
// 本函数不加 b.mu，可在持锁时调用，但传入的回调本身不得再获取 b.mu。
func (b *MemoryBackend) nowledgeSettings() config.MemoryConfig {
	if p := b.nowledgeSettingsFunc.Load(); p != nil && *p != nil {
		return (*p)()
	}
	return config.MemoryConfig{}
}

// SetSwarmConfigFunc 设置编排（Swarm）配置获取函数。
func (b *MemoryBackend) SetSwarmConfigFunc(fn func() config.OrchestrationConfig) {
	if fn == nil {
		b.swarmConfigFunc.Store(nil)
		return
	}
	b.swarmConfigFunc.Store(&fn)
}

// swarmConfig 返回当前 Swarm 配置（若未设置则返回零值配置）。
// 本函数不加 b.mu，可在持锁时调用，但传入的回调本身不得再获取 b.mu。
func (b *MemoryBackend) swarmConfig() config.OrchestrationConfig {
	if p := b.swarmConfigFunc.Load(); p != nil && *p != nil {
		return (*p)()
	}
	return config.OrchestrationConfig{}
}

// SetProviderEnabledFunc 设置 provider 启用状态获取函数（键为 provider kind，false 表示在桌面设置里已关闭）。
// 未注入时所有 provider 都视为启用。
func (b *MemoryBackend) SetProviderEnabledFunc(fn func() map[string]bool) {
	if fn == nil {
		b.providerEnabledFunc.Store(nil)
		return
	}
	b.providerEnabledFunc.Store(&fn)
}

// providerEnabled 返回 provider 启用状态表；未注入时返回 nil（调用方按「未知视为启用」处理）。
// 本函数不加 b.mu，可在持锁时调用，但传入的回调本身不得再获取 b.mu。
func (b *MemoryBackend) providerEnabled() map[string]bool {
	if p := b.providerEnabledFunc.Load(); p != nil && *p != nil {
		return (*p)()
	}
	return nil
}

// SetDraftDefaultsFunc 设置新建会话草稿默认值构建函数（桌面模型记忆 + 编排选择）。
// 未注入时 providers.list 的 draft 回落到宿主默认 provider 与默认模型。
func (b *MemoryBackend) SetDraftDefaultsFunc(fn func() wire.ComposerDraftDefaults) {
	if fn == nil {
		b.draftDefaultsFunc.Store(nil)
		return
	}
	b.draftDefaultsFunc.Store(&fn)
}

// draftDefaults 返回注入的草稿默认值；未注入时 ok 为 false。
// 本函数不加 b.mu，可在持锁时调用，但传入的回调本身不得再获取 b.mu。
func (b *MemoryBackend) draftDefaults() (wire.ComposerDraftDefaults, bool) {
	if p := b.draftDefaultsFunc.Load(); p != nil && *p != nil {
		return (*p)(), true
	}
	return wire.ComposerDraftDefaults{}, false
}

// fillThreadCreateDefaultsLocked 用宿主记忆（draft defaults）补齐用户新建会话没传的选择。
// 按组补齐，避免把属于别的模型/别的组合的取值套上去：
//   - 主导组：params.Model 与 Options 里的 provider、model 都没给时，
//     provider、model、effort、fastMode 一起取默认值；只要给了 provider 或 model 中的任意一个，
//     effort 与 fastMode 一律不补。
//   - 子代理组：三个字段都为 nil 时一起取默认值（nil 表示空串，provider 与 model 都空表示跟随主导）。
//   - 编排开关：Options.SwarmEnabled 为 nil 时取默认值。
//
// 补齐的 provider 必须已注册、model 必须通过 validModel（目录命中或已固定），否则主导组保持原样（回到默认值）。
// 调用方必须持有 b.mu。
func (b *MemoryBackend) fillThreadCreateDefaultsLocked(params *wire.ThreadCreateParams) {
	draft, ok := b.draftDefaults()
	if !ok {
		return
	}
	opts := wire.ComposerOptions{}
	if params.Options != nil {
		opts = *params.Options
	}
	if params.Model == nil && opts.Provider == nil && opts.Model == nil && draft.Provider != "" {
		_, providerKnown := b.harnesses[draft.Provider]
		draftModel := derefString(draft.Model)
		if providerKnown && (draftModel == "" || b.validModel(draft.Provider, draftModel)) {
			provider := draft.Provider
			opts.Provider = &provider
			opts.Model = draft.Model
			opts.Effort = draft.Effort
			fastMode := draft.FastMode
			opts.FastMode = &fastMode
		}
	}
	if opts.SwarmBeesProvider == nil && opts.SwarmBeesModel == nil && opts.SwarmBeesEffort == nil {
		opts.SwarmBeesProvider = draft.SwarmBeesProvider
		opts.SwarmBeesModel = draft.SwarmBeesModel
		opts.SwarmBeesEffort = draft.SwarmBeesEffort
	}
	if opts.SwarmEnabled == nil {
		swarmEnabled := draft.SwarmEnabled
		opts.SwarmEnabled = &swarmEnabled
	}
	params.Options = &opts
}

// SetRememberSelectionFunc 设置 providers.rememberSelection 写回宿主记忆的回调。
func (b *MemoryBackend) SetRememberSelectionFunc(fn func(params wire.ProvidersRememberSelectionParams)) {
	if fn == nil {
		b.rememberSelectionFunc.Store(nil)
		return
	}
	b.rememberSelectionFunc.Store(&fn)
}

// rememberSelection 返回写回宿主记忆的回调；未注入时返回 nil。
func (b *MemoryBackend) rememberSelection() func(params wire.ProvidersRememberSelectionParams) {
	if p := b.rememberSelectionFunc.Load(); p != nil && *p != nil {
		return *p
	}
	return nil
}
