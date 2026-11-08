package mcodeui

import (
	"context"
	"strings"
)

// pendingThreadState 表示“待创建会话”：点击新建只记下项目与创建选项，
// 首个非空输入（或发送）时才真正在后端建会话，空会话因此不会出现在侧栏。
// 草稿有内容后才在会话列表展示。
type pendingThreadState struct {
	ProjectID string
	Options   NewThreadOptions // 创建时下发的选项，模型/强度等改动会同步写回
	Thread    Thread           // 本地占位会话，供 composer 渲染与偏好编辑
	Worktree  bool             // 创建成功后立即迁入自动命名的 Worktree
	Creating  bool             // 创建请求已发出，重复输入/发送不再建第二个
}

// isPendingThreadLocked 判断 threadID 是否属于待创建会话（新会话页用空 ID 表示）。调用方需持有 s.mu。
func (s *chatState) isPendingThreadLocked(threadID string) bool {
	return threadID == "" && s.pendingThread != nil
}

// pendingComposerThreadLocked 返回待创建会话的占位副本，供 composer 渲染模型/强度等控件。调用方需持有 s.mu。
func (s *chatState) pendingComposerThreadLocked() (*Thread, bool) {
	if s.pendingThread == nil {
		return nil, false
	}
	th := s.pendingThread.Thread
	return &th, true
}

// beginPendingThread 进入待创建会话状态：只记录项目与创建选项并清空聊天区，
// 不建后端会话、不改侧栏会话列表、也不选中任何已有会话。
func (s *chatState) beginPendingThread(projectID string, opts NewThreadOptions, worktree bool) {
	if projectID == "" {
		s.setToast("新建会话失败: 项目不能为空")
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.pendingThread = &pendingThreadState{
		ProjectID: projectID,
		Options:   opts,
		Worktree:  worktree,
		Thread: Thread{
			ProjectID:       projectID,
			Provider:        opts.Provider,
			Model:           opts.Model,
			Effort:          opts.Effort,
			BeesProvider:    opts.BeesProvider,
			BeesModel:       opts.BeesModel,
			BeesEffort:      opts.BeesEffort,
			FastMode:        opts.Fast,
			InteractionMode: "build", // Plan 只在当前会话生效，新建会话一律从 build 开始
		},
	}
	// 点击“新建会话”时指针按下会把焦点从输入框移走，而 mygo 的 AutoFocus 只在元素首次出现的那一帧生效；
	// 待创建会话与新会话页共用同一个输入框元素，不重新要焦点的话点完新建就无法直接打字。
	s.composerFocusRequest = true
	s.mu.Unlock()
	// 复用切换会话的清理：写回上一个会话草稿、停掉 Watch、清空消息，并回填新会话页的草稿。
	s.selectThread("")
	s.invalidate()
}

// createPendingThread 把待创建会话真正建到后端：成功后选中新会话，afterCreate 拿到新会话 ID
// 继续执行（发送路径用它把首条消息发出去）；失败时提示并保留待创建状态与草稿，可再次触发重试。
func (s *chatState) createPendingThread(afterCreate func(threadID string)) {
	s.mu.Lock()
	p := s.pendingThread
	if p == nil || p.Creating {
		s.mu.Unlock()
		return
	}
	p.Creating = true
	projectID := p.ProjectID
	opts := p.Options
	prefs := composerPrefsOf(p.Thread)
	beeTiers := p.Thread.BeeTiers
	runtimeMode := p.Thread.RuntimeMode
	s.mu.Unlock()

	// 建会话不改写默认 Agent 与模型记忆，只有用户在选择器里主动切换时才写（见 composer.go 里对 rememberUsedProvider 的调用）。

	go func() {
		ctx := context.Background()
		newThreadID, err := s.client.CreateThreadWithOptions(ctx, projectID, opts)
		if err != nil {
			s.mu.Lock()
			if s.pendingThread == p {
				p.Creating = false
			}
			s.mu.Unlock()
			s.setToast("新建会话失败: " + err.Error())
			return
		}
		if p.Worktree {
			if err := s.client.MoveToWorktree(ctx, newThreadID, "", false); err != nil {
				s.setToast("Worktree 创建失败：" + err.Error())
			}
		}
		// 推理强度、Build/Plan、人设与档位覆盖只能创建后补写：CreateThread 只认 FastMode，没有这些字段。
		// SetThreadComposerPrefs 整体覆盖，必须带上创建时写入的 Bees 模型，否则会被清空。
		if prefs.Effort != "" || prefs.BeesEffort != "" || prefs.BeesProvider != "" || prefs.BeesModel != "" || prefs.Persona != "" || prefs.InteractionMode == "plan" {
			if err := s.client.SetThreadComposerPrefs(newThreadID, prefs); err != nil {
				s.setToast("设置推理强度失败: " + err.Error())
			}
		}
		if beeTiers != "" {
			if err := s.client.SetThreadBeeTiers(newThreadID, beeTiers); err != nil {
				s.setToast("更新档位配置失败: " + err.Error())
			}
		}
		if runtimeMode != "" {
			if err := s.client.SetThreadRuntimeMode(newThreadID, runtimeMode); err != nil {
				s.setToast("设置权限模式失败: " + err.Error())
			}
		}
		if opts.SwarmEnabled {
			if _, err := s.client.SetSwarmEnabled(ctx, newThreadID, true); err != nil {
				s.setToast("开启编排失败: " + err.Error())
			}
		}

		// 新会话出现在会话列表后才选中它，侧栏随之多出这一行。
		// 这里不写“上次使用”的记忆：主导/子线程的改动在用户改动的当下已经记过，建会话不算用户操作。
		s.refreshProjectThreads(ctx, projectID)

		if !s.promotePendingThread(p, newThreadID) {
			return
		}
		if afterCreate != nil {
			afterCreate(newThreadID)
		}
	}()
}

// promotePendingThread 把待创建会话交给新建的真实会话：输入框里的文本与附件改挂到新会话，
// 再按正常切换流程选中它，从而不丢已输入的内容。
// 返回 false 表示期间用户已切到别的会话，待创建状态已被丢弃。
func (s *chatState) promotePendingThread(p *pendingThreadState, newThreadID string) bool {
	s.mu.Lock()
	if s.pendingThread != p {
		s.mu.Unlock()
		return false
	}
	s.pendingThread = nil
	if s.drafts == nil {
		s.drafts = make(map[string]composerDraft)
	}
	// 待创建会话的草稿挂在空字符串键上：改挂到新会话，选中后由 selectThread 回填。
	s.drafts[newThreadID] = composerDraft{
		Text:        s.draft,
		Attachments: append([]composerAttachment(nil), s.attachments...),
	}
	s.mu.Unlock()

	s.selectThread(newThreadID)

	// 输入框内容已在 s.draft/s.attachments 里（待创建时就写在那里），不再用草稿回填：
	// 否则发送成功后回填会把刚清空的输入框又填回已发送的文本。
	// selectThread 还会把旧草稿写回空字符串键，一并清掉，避免下次新建会话回填已发送的内容。
	s.mu.Lock()
	s.pendingDraftFill = nil
	delete(s.drafts, "")
	s.mu.Unlock()
	s.persistDraftsAsync()
	return true
}

// applyPendingComposerPrefsLocked 把 composer 偏好写进待创建会话（占位会话与创建选项各写一份），
// 创建时随会话一起下发。调用方需持有 s.mu。
func (s *chatState) applyPendingComposerPrefsLocked(prefs ComposerPrefs) {
	p := s.pendingThread
	if p == nil {
		return
	}
	p.Thread.InteractionMode = prefs.InteractionMode
	p.Thread.Effort = prefs.Effort
	p.Thread.BeesProvider = prefs.BeesProvider
	p.Thread.BeesModel = prefs.BeesModel
	p.Thread.BeesEffort = prefs.BeesEffort
	// 人设只有会话上有字段，创建选项里没有，所以只在占位会话上记一份，创建后由补写路径持久化。
	p.Thread.Persona = prefs.Persona
	p.Options.Effort = prefs.Effort
	p.Options.BeesProvider = prefs.BeesProvider
	p.Options.BeesModel = prefs.BeesModel
	p.Options.BeesEffort = prefs.BeesEffort
}

// setPendingAgentLocked 记录待创建会话的 Lead 模型。调用方需持有 s.mu。
func (s *chatState) setPendingAgentLocked(provider, model string) {
	p := s.pendingThread
	if p == nil {
		return
	}
	if provider != "" {
		p.Thread.Provider = provider
		p.Options.Provider = provider
	}
	p.Thread.Model = model
	p.Options.Model = model
}

// setPendingFastLocked 记录待创建会话的快速模式。调用方需持有 s.mu。
func (s *chatState) setPendingFastLocked(fast bool) {
	p := s.pendingThread
	if p == nil {
		return
	}
	p.Thread.FastMode = fast
	p.Options.Fast = fast
}

// setPendingSwarmEnabledLocked 记录待创建会话的 Swarm 开关。调用方需持有 s.mu。
func (s *chatState) setPendingSwarmEnabledLocked(enabled bool) {
	if p := s.pendingThread; p != nil {
		p.Options.SwarmEnabled = enabled
	}
}

// setPendingBeeTiersLocked 记录待创建会话的档位覆盖。调用方需持有 s.mu。
func (s *chatState) setPendingBeeTiersLocked(payload string) {
	if p := s.pendingThread; p != nil {
		p.Thread.BeeTiers = payload
	}
}

// setPendingRuntimeModeLocked 记录待创建会话的权限档位（创建后补写）。调用方需持有 s.mu。
func (s *chatState) setPendingRuntimeModeLocked(mode string) {
	if p := s.pendingThread; p != nil {
		p.Thread.RuntimeMode = mode
	}
}

// applyPendingSwarmPairLocked 把 Swarm 配对写进待创建会话：没有后端会话可写，创建时再一并下发。
func (s *chatState) applyPendingSwarmPairLocked(pair SwarmPair, leadEffort, workerEffort string) {
	p := s.pendingThread
	if p == nil {
		return
	}
	if pair.LeadProvider != "" {
		p.Thread.Provider = pair.LeadProvider
		p.Options.Provider = pair.LeadProvider
	}
	if pair.LeadModel != "" {
		p.Thread.Model = pair.LeadModel
		p.Options.Model = pair.LeadModel
	}
	if leadEffort != "" {
		p.Thread.Effort = leadEffort
		p.Options.Effort = leadEffort
	}
	p.Thread.BeesProvider = pair.WorkerProvider
	p.Thread.BeesModel = pair.WorkerModel
	p.Thread.BeesEffort = workerEffort
	// 配对变更会清空档位覆盖，并记下选中的配对，与真实会话的处理一致。
	p.Thread.BeeTiers = ""
	p.Thread.PairID = pair.ID
	p.Options.BeesProvider = pair.WorkerProvider
	p.Options.BeesModel = pair.WorkerModel
	p.Options.BeesEffort = workerEffort
	p.Options.PairID = pair.ID
	p.Options.SwarmEnabled = true
}

// pendingDraftHasContent 判断新会话页草稿是否有可发送内容（非空文本或已上传附件）。
func pendingDraftHasContent(text string) bool {
	return strings.TrimSpace(text) != ""
}
