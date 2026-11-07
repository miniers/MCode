package mcodeui

import "strings"

// swarmPairQuickSaveEffort 返回配对某一侧记录的推理强度；nil 或空串表示未指定。
func swarmPairQuickSaveEffort(effort *string) string {
	if effort == nil {
		return ""
	}
	return strings.TrimSpace(*effort)
}

// swarmPairQuickSaveMatches 判断已存配对与当前主/子模型选择是否一致：
// 两侧 provider 与 model 相同（执行侧为空视作跟随主导），快速模式相同，
// 推理强度相同；配对没记录强度的一侧视为一致，因为应用配对时不会覆盖会话当前值。
func swarmPairQuickSaveMatches(pair SwarmPair, lead, bees modelPickerSide) bool {
	if pair.LeadProvider != lead.Selection.Provider || pair.LeadModel != lead.Selection.Model {
		return false
	}
	workerModel := pair.WorkerModel
	if workerModel == "" {
		workerModel = pair.LeadModel
	}
	if swarmWorkerProvider(pair) != bees.Selection.Provider || workerModel != bees.Selection.Model {
		return false
	}
	if pair.LeadFastMode != lead.Selection.Fast {
		return false
	}
	if !bees.FollowsLead && pair.WorkerFastMode != bees.Selection.Fast {
		return false
	}
	if effort := swarmPairQuickSaveEffort(pair.LeadEffort); effort != "" && effort != modelPickerSideEffort(lead) {
		return false
	}
	if effort := swarmPairQuickSaveEffort(pair.WorkerEffort); effort != "" && !bees.FollowsLead && effort != modelPickerSideEffort(bees) {
		return false
	}
	return true
}

// swarmPairQuickSaveFind 在已存配对里找与当前主/子模型组合一致的一项。
func swarmPairQuickSaveFind(pairs []SwarmPair, lead, bees modelPickerSide) (SwarmPair, bool) {
	for _, pair := range pairs {
		if swarmPairQuickSaveMatches(pair, lead, bees) {
			return pair, true
		}
	}
	return SwarmPair{}, false
}

// swarmPairQuickSaveDuplicate 在已存配对里找与草稿同组合的一项（只看两侧 provider 与 model，
// 与设置页预设组合的 isSwarmPairAdded 同口径），用于保存前拦重复。
func swarmPairQuickSaveDuplicate(draft SwarmPair, pairs []SwarmPair) (SwarmPair, bool) {
	for _, pair := range pairs {
		if pair.LeadProvider == draft.LeadProvider &&
			pair.LeadModel == draft.LeadModel &&
			swarmWorkerProvider(pair) == swarmWorkerProvider(draft) &&
			pair.WorkerModel == draft.WorkerModel {
			return pair, true
		}
	}
	return SwarmPair{}, false
}

// swarmPairQuickSaveDefaultName 生成默认配对名：「主模型短名 + 子模型短名」，如「Opus 5.5 + Sonnet 5.5」。
func swarmPairQuickSaveDefaultName(providers []ProviderInfo, lead, bees modelPickerSide) string {
	leadName := strings.TrimSpace(modelShortDisplayName(providers, lead.Selection.Provider, lead.Selection.Model))
	beesName := strings.TrimSpace(modelShortDisplayName(providers, bees.Selection.Provider, bees.Selection.Model))
	switch {
	case leadName == "" && beesName == "":
		return "模型配对"
	case beesName == "":
		return leadName
	case leadName == "":
		return beesName
	}
	return leadName + " + " + beesName
}

// swarmPairQuickSaveDraft 按当前主/子模型选择构造待保存的配对草稿。
// 执行侧跟随主导时留空 provider/model 与强度，应用配对后仍继续跟随主导。
func swarmPairQuickSaveDraft(p ModelPickerProps, lead, bees modelPickerSide, name string) SwarmPair {
	draft := SwarmPair{
		Name:           strings.TrimSpace(name),
		LeadProvider:   lead.Selection.Provider,
		LeadModel:      lead.Selection.Model,
		LeadFastMode:   lead.Selection.Fast,
		WorkerProvider: p.Bees.Provider,
		WorkerModel:    p.Bees.Model,
	}
	if effort := strings.TrimSpace(modelPickerSideEffort(lead)); effort != "" {
		v := effort
		draft.LeadEffort = &v
	}
	if !bees.FollowsLead {
		draft.WorkerFastMode = bees.Selection.Fast
		if effort := strings.TrimSpace(modelPickerSideEffort(bees)); effort != "" {
			v := effort
			draft.WorkerEffort = &v
		}
	}
	return draft
}

// swarmPairQuickSaveSettingsPairs 复制设置里的配对列表，避免渲染期间直接共享底层切片。
func swarmPairQuickSaveSettingsPairs(st *SettingsState) []SwarmPair {
	if st == nil {
		return nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]SwarmPair(nil), st.cfg.Orchestration.Pairs...)
}

// swarmPairQuickSaveError 返回设置保存失败时的错误文案，没有错误时返回空串。
func swarmPairQuickSaveError(st *SettingsState) string {
	if st == nil {
		return ""
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.errorMessage
}

// swarmPairFavPlan 描述一次收藏点击应对已存配对做的改动。
type swarmPairFavPlan struct {
	Matched   bool      // 当前组合已存为配对：应删除 Pair
	Pair      SwarmPair // Matched 时为命中的已存配对
	Draft     SwarmPair // 需要新增的配对草稿，名称取默认名
	Duplicate SwarmPair // 同 provider/model 组合已存在但强度/快速模式不同：只提示，不覆盖
	HasDup    bool      // Duplicate 是否命中
}

// swarmPairFavPlanFor 按已存配对与当前主/子模型选择计算收藏点击的改动。
func swarmPairFavPlanFor(pairs []SwarmPair, p ModelPickerProps, lead, bees modelPickerSide) swarmPairFavPlan {
	if current, ok := swarmPairQuickSaveFind(pairs, lead, bees); ok {
		return swarmPairFavPlan{Matched: true, Pair: current}
	}
	draft := swarmPairQuickSaveDraft(p, lead, bees, swarmPairQuickSaveDefaultName(p.Providers, lead, bees))
	plan := swarmPairFavPlan{Draft: draft}
	if existing, ok := swarmPairQuickSaveDuplicate(draft, pairs); ok {
		plan.Duplicate = existing
		plan.HasDup = true
	}
	return plan
}

// swarmPairFavProps 构造主导侧卡片说明行左侧的收藏按钮状态：Matched 表示当前主/子模型组合
// 已存为配对，Name 为配对显示名。设置尚未加载时返回 nil，卡片退回原来的 Swarm 标记。
func swarmPairFavProps(p ModelPickerProps, lead, bees modelPickerSide) *pairFavProps {
	st := getChatSettings(activeChatState())
	if st == nil {
		return nil
	}
	fav := &pairFavProps{OnToggle: func() { swarmPairFavToggle(p, lead, bees) }}
	if current, ok := swarmPairQuickSaveFind(swarmPairQuickSaveSettingsPairs(st), lead, bees); ok {
		fav.Matched = true
		fav.Name = swarmPairTitle(current, p.Providers)
	}
	return fav
}

// swarmPairFavToggle 处理说明行收藏按钮的点击：当前组合已存为配对时删除它，否则按默认名新增。
// 新增与持久化复用设置页的 swarmPairSave，删除复用 swarmPairDelete（同一条 RemovePair 落盘路径）。
func swarmPairFavToggle(p ModelPickerProps, lead, bees modelPickerSide) {
	chat := activeChatState()
	notify := func(msg string) {
		if chat != nil && msg != "" {
			chat.setToast(msg)
		}
	}
	st := getChatSettings(chat)
	if st == nil {
		notify("设置尚未加载，无法保存配对")
		return
	}
	plan := swarmPairFavPlanFor(swarmPairQuickSaveSettingsPairs(st), p, lead, bees)
	switch {
	case plan.Matched:
		if swarmPairDelete(st, plan.Pair.ID) {
			notify("已取消配对 " + swarmPairTitle(plan.Pair, p.Providers))
		} else if msg := swarmPairQuickSaveError(st); msg != "" {
			notify("取消配对失败：" + msg)
		}
	case plan.HasDup:
		notify("已存在配对 " + swarmPairTitle(plan.Duplicate, p.Providers))
	default:
		added := swarmPairSave(st, plan.Draft, true)
		if msg := swarmPairQuickSaveError(st); msg != "" {
			notify("保存失败：" + msg)
		} else {
			notify("已保存配对 " + swarmPairTitle(added, p.Providers))
		}
	}
	// 设置页列表读同一份 st.cfg，保存或删除后即刻可见。
	st.invalidate()
	modelPickerInvalidate(p)
}
