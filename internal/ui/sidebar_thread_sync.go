package mcodeui

import "strings"

// startThreadSummaryWatch 订阅所有会话的运行态切换，用于纠正侧栏的运行图标。
func (s *chatState) startThreadSummaryWatch() {
	s.mu.Lock()
	if s.stopSummaryWatch != nil || s.client == nil || s.closed {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	stop := s.client.WatchThreadSummaries(s.applyThreadSummary)

	s.mu.Lock()
	if s.closed {
		if stop != nil {
			stop()
		}
	} else {
		s.stopSummaryWatch = stop
	}
	s.mu.Unlock()
}

// threadListStaleLocked 判断这条推送是否说明侧栏会话列表已过期、需要重新拉取。
// 调用方必须持有 s.mu。返回 projectID 非空表示只需重拉该项目的列表；
// reloadProjects 为 true 表示推送所属项目本地还不认识，只能整表重载。
// 子线程（bee）由 refreshBeeThreads 单独处理，这里一律跳过。
func (s *chatState) threadListStaleLocked(update Thread) (projectID string, reloadProjects bool) {
	if update.IsHelper || update.ParentThreadID != "" || update.ProjectID == "" ||
		s.client == nil || s.closed || !s.initialized {
		return "", false
	}

	// 去重键：列表里没有这个会话时按 ID 去重（标题此时多半还没生成，不能让它反复触发刷新）；
	// 列表里有但标题变了时把新标题也算进去，标题再变才会再刷新一次。
	key := ""
	found := false
	for _, item := range s.threadsByProj[update.ProjectID] {
		if item.ID != update.ID {
			continue
		}
		found = true
		if update.Title != "" && update.Title != item.Title {
			key = update.ID + "\x00" + update.Title
		}
		break
	}
	if !found {
		key = update.ID
	}
	if key == "" {
		return "", false
	}
	if s.summaryRefreshSeen[key] {
		return "", false
	}
	if s.summaryRefreshSeen == nil {
		s.summaryRefreshSeen = make(map[string]bool)
	}
	s.summaryRefreshSeen[key] = true

	for _, p := range s.projects {
		if p.ID == update.ProjectID {
			return update.ProjectID, false
		}
	}
	return "", true
}

// applyThreadSummary 用后端推送的会话摘要刷新侧栏里的运行标记。
// 侧栏的 IsRunning 只来自某次会话列表加载的快照，没订阅 Watch 的会话（切走后结束、
// 在 web 端被停止等）收不到结束事件，左侧的动态图标会一直转下去，这里以推送为权威纠正。
// 运行相关字段直接按推送覆盖；标题、时间、休眠等仍以列表加载为准，避免推送乱序覆盖本地修改。
// 列表里没有的会话（例如刚在 web 端新建的）或标题变了的会话，不把推送内容直接写进列表，
// 而是重拉一次列表：这样新会话能立刻出现，标题也能跟上，选中会话不受影响。
func (s *chatState) applyThreadSummary(update Thread) {
	if s == nil || update.ID == "" {
		return
	}
	s.mu.Lock()
	changed := false
	// wasRunning 记录推送前本地是否“运行中或仍有子线程在跑”：只认真正的“忙 → 空闲”跳变，
	// 否则每次空闲摘要都会把已看过的会话重新点亮完成角标。
	// SwarmBusy 也算忙：主会话回复结束但子任务/子代理还在跑时，这一轮并未结束。
	// 乐观运行中标记也意味着这一轮正在进行：会话列表先刷成空闲时靠它兜底。
	wasRunning := s.optimisticRunning[update.ID]
	apply := func(item *Thread) {
		if item.ID != update.ID {
			return
		}
		if item.IsRunning || item.SwarmBusy {
			wasRunning = true
		}
		if item.IsRunning != update.IsRunning || item.SwarmBusy != update.SwarmBusy || item.BackgroundTasks != update.BackgroundTasks || item.HasPendingRequest != update.HasPendingRequest ||
			(update.LastStatus != "" && item.LastStatus != update.LastStatus) {
			changed = true
		}
		item.IsRunning = update.IsRunning
		item.SwarmBusy = update.SwarmBusy
		item.BackgroundTasks = update.BackgroundTasks
		item.HasPendingRequest = update.HasPendingRequest
		if update.LastStatus != "" {
			item.LastStatus = update.LastStatus
		}
	}
	for i := range s.threads {
		apply(&s.threads[i])
	}
	for _, list := range s.threadsByProj {
		for i := range list {
			apply(&list[i])
		}
	}
	// 检查当前 Swarm 面板状态中是否有该 bee：若子代理落定，
	// 立即把面板状态里的 bee 置为终态并使派生视图失效，避免界面上头像持续转圈。
	if hp := getSwarmPanelState(s); hp != nil {
		swarmPanelMu.Lock()
		for i := range hp.state.Bees {
			h := &hp.state.Bees[i]
			if h.ThreadID == update.ID {
				if !update.IsRunning && strings.ToLower(h.Status) == "running" {
					if update.LastStatus != "" {
						h.Status = update.LastStatus
					} else {
						h.Status = "completed"
					}
					hp.stateRev++
					hp.view = nil
					changed = true
				}
			}
		}
		swarmPanelMu.Unlock()
	}
	if !update.IsRunning && (update.IsHelper || update.ParentThreadID != "") {
		changed = true
	}
	// 回合结束由后端这一侧确认，乐观运行中标记不必再等 Watch 增量。
	if !update.IsRunning {
		delete(s.optimisticRunning, update.ID)
	}
	// 这一轮真正跑完：主会话与名下子线程都空闲（!IsRunning && !SwarmBusy）时才亮完成角标；
	// 当前没在看的会话才标记，新一轮开始则撤掉。
	switch {
	case update.IsRunning:
		changed = s.clearTurnFinishedLocked(update.ID) || changed
	case wasRunning && !update.SwarmBusy && s.selectedThreadID != update.ID:
		changed = s.markTurnFinishedLocked(update.ID, update.LastStatus) || changed
	}
	// 会话运行态/待处理请求变了，待处理请求集合（见 pendingRequestThreadIDs）需要重算。
	s.bumpThreadsRuntimeLocked()
	// 列表过期（新会话/标题变化）时重拉列表，重拉本身会 invalidate。
	staleProjectID, reloadProjects := s.threadListStaleLocked(update)
	s.mu.Unlock()
	if reloadProjects {
		s.loadProjects()
	}
	if staleProjectID != "" {
		// 不用 refreshProjectThreads：它会把项目强制展开，侧栏会自己跳开。
		s.loadThreads(staleProjectID, false)
	}
	if changed {
		s.invalidate()
	}
}
