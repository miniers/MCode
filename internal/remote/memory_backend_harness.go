// 本文件包含 MemoryBackend 的模型运行与模拟回复方法。
package remote

import (
	"context"
	"errors"
	"fmt"
	"log"
	"mcode/internal/git"
	"mcode/internal/harness"
	"mcode/internal/projctx"
	"mcode/internal/remote/wire"
	"mcode/internal/swarm"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// finishTurnEarly 收尾轮末早退分支：仅当该线程没有其他轮次在跑时恢复空闲、发布空闲摘要，
// 并按正常路径的条件启动下一条排队消息，避免 IsRunning 永久卡在 true、跟进队列永不出队。
func (b *MemoryBackend) finishTurnEarly(td *memoryThread, threadID, turnID string, turnIndex int, isNormal, isInterrupted bool) {
	status := "failed"
	if isInterrupted {
		status = "interrupted"
	} else if isNormal {
		status = "completed"
	}
	b.updateTurnFinish(threadID, turnID, turnIndex, status, "")
	b.mu.Lock()
	if td == nil || b.threads[threadID] != td {
		b.mu.Unlock()
		return
	}
	// 该线程已有别的轮次在跑（例如运行中切换 Provider 后新 epoch 启动的新轮次），不能置空闲。
	if td.runTurnID != "" && td.runTurnID != turnID {
		var msgs []wire.ThreadMessage
		for _, msg := range td.messages {
			if msg.TurnID != nil && *msg.TurnID == turnID {
				msgs = append(msgs, msg)
			}
		}
		summary := b.summaryForPublishLocked(td)
		b.mu.Unlock()
		b.hub.Publish(threadID, msgs, nil, summary)
		b.hub.Flush(threadID)
		return
	}
	if td.watchdogStop != nil {
		td.watchdogStop()
		td.watchdogStop = nil
	}
	if td.runCancel != nil {
		td.runCancel()
		td.runCancel = nil
	}
	td.runTurnID = ""
	td.summary.LastActivityText = nil
	td.summary.UpdatedAt = b.nowLocked().Format(time.RFC3339Nano)
	td.setIdleLocked()
	retryUnsolicited := td.takeUnsolicitedPendingLocked()
	if isNormal || isInterrupted {
		td.summary.Attention = "ready"
	} else {
		td.summary.Attention = "failed"
	}
	finalSummary := b.summaryForPublishLocked(td)
	var finalMsgs []wire.ThreadMessage
	for _, msg := range td.messages {
		if msg.TurnID != nil && *msg.TurnID == turnID {
			finalMsgs = append(finalMsgs, msg)
		}
	}
	b.mu.Unlock()
	b.hub.Publish(threadID, finalMsgs, nil, finalSummary)
	b.hub.Flush(threadID)

	hasBees := b.hasWorkingSwarmBees(threadID)
	b.mu.Lock()
	var (
		nextStarted  *startedTurn
		nextText     string
		queueChanged bool
	)
	// 宿主关闭时保留队列不动，与正常结束路径一致。
	if b.threads[threadID] == td && b.ctx.Err() == nil {
		nextStarted, nextText, queueChanged = b.startNextQueuedTurnLocked(td, isNormal && !hasBees, isNormal)
	}
	b.mu.Unlock()

	if nextStarted != nil {
		b.hub.Flush(threadID)
		// 正文先落库再删队列行，崩溃时最多重复出现一条已写正文的排队项，不会丢消息。
		b.persistMessages(threadID, []wire.ThreadMessage{nextStarted.userMsg})
		b.recordDispatchingForTurn(threadID, nextStarted)
	}
	if queueChanged {
		b.commitQueueChange(threadID)
	}
	if nextStarted != nil {
		if nextStarted.titleChanged {
			b.persistThreadTitle(threadID, nextStarted.currentSummary.Title)
		}
		b.hub.BeginRun(threadID)
		b.hub.Publish(threadID, []wire.ThreadMessage{nextStarted.userMsg}, nil, nextStarted.currentSummary)
		b.launchTurnRun(threadID, nextStarted, nextText)
	}
	if retryUnsolicited {
		go b.retryUnsolicitedTurn(threadID)
	}
}

// interactionModeOverride/effortOverride 非空时覆盖会话偏好，仅作用于本轮。
// imageNames 与 imagePaths 等长且同序，元素可能为空串（无原始文件名）。
func (b *MemoryBackend) runHarness(runCtx context.Context, threadID string, turnID string, userText string, turnIndex int, imagePaths []string, imageNames []string, interactionMode, effort string) {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok || td.runTurnID != turnID || runCtx.Err() != nil {
		// runTurnID 已被清空说明没有任何轮次在跑：本轮无人接管，而 startTurnLocked 已置
		// IsRunning，必须补上空闲收尾。本轮 runCtx 已取消但 runTurnID 仍指向本轮时，
		// 中断路径只挂了 watchdog 超时兜底，也要就地收尾，避免线程卡在运行中。
		// finishTurnEarly 自带守卫，另有轮次在跑时会跳过。
		b.mu.Unlock()
		b.finishTurnEarly(td, threadID, turnID, turnIndex, false, runCtx.Err() != nil)
		return
	}
	recordedEpoch := td.sessionEpoch
	// 本轮由排队项出队而来时记下投递行标识；accepted/failed 的迁移在 emit 与收尾处触发。
	deliveryMessageID, deliveryFollowUpID := "", ""
	if td.deliveryTurnID == turnID {
		deliveryMessageID, deliveryFollowUpID = td.deliveryMessageID, td.deliveryFollowUpID
	}
	// attachTurnID 指向本轮时，说明这一轮要接住常驻进程自发跑出的一段输出（不发用户帧）。
	attachTurn := td.attachTurnID == turnID
	// 原生 /compact 必须原样送到 Provider：本轮用户文本去掉首尾空白后恰为该命令时，下面所有装饰
	// （人设前缀、mention/continuation/plan 前缀、附件说明、guidance、decorator、ConciseRepliesPrompt）
	// 一律跳过，否则 Provider 认不出这个斜杠命令。
	compactOnly := strings.TrimSpace(userText) == compactCommandText
	// 本轮快速模式与速率档位取自会话摘要，与 effort 一样在启动锁内固定。
	fastMode := td.summary.FastMode
	speedTier := derefString(td.summary.SpeedTier)
	transient := isHelperThread(td.summary)
	h := b.threadHarnessLocked(td.summary.Provider)
	workDir := b.harnessWorkDir
	turnMode := harness.PermissionMode(td.summary.RuntimeMode)
	if b.harnessMode == harness.ModeReadOnly || b.harnessMode == "readonly" {
		turnMode = harness.ModeReadOnly
	} else if turnMode == "" {
		turnMode = harness.PermissionMode(b.defaultRuntimeModeLocked())
	}
	if !ok || h == nil {
		b.mu.Unlock()
		// 没有可用 harness 等于这一轮没交给 provider：投递行直接记为 failed，不留在 dispatching。
		b.markDeliveryFailed(threadID, turnID, deliveryMessageID, deliveryFollowUpID, "provider 未就绪")
		// 找不到 harness 也要走标准收尾：由 finishTurnEarly 取消本轮、清 runTurnID 并置空闲，
		// 否则 IsRunning 会永久为 true 且缺少空闲摘要。
		b.finishTurnEarly(td, threadID, turnID, turnIndex, false, false)
		return
	}
	if td.workDir != "" {
		workDir = td.workDir
	} else if proj, ok := b.projects[td.summary.ProjectID]; ok && proj.Path != "" {
		workDir = proj.Path
	}
	sessionID := td.sessionID
	needsHandoff := td.needsHandoff
	// 会话文件已丢失时（例如本地会话被清理），把可见历史交接进本轮提示词，避免模型失去上下文。
	// 只影响本轮，不写回线程状态；实现者本轮重建会话后，下一轮就不会再交接。
	if !needsHandoff && sessionID != "" {
		if checker, ok := h.(harness.SessionChecker); ok && !checker.SessionExists(sessionID) {
			needsHandoff = true
		}
	}
	// C-ST-07：本轮启动前确保历史消息在内存中（已归档的空闲会话可能已被淘汰）。
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		log.Printf("ensure messages for thread %s error: %v", threadID, err)
	}
	b.ensureMessagesLoadedLocked(td)
	b.touchThreadLocked(td)
	// 记录本轮开始时刻，供「终止运行中的命令」排除本轮之前就已启动的常驻子进程。
	td.runStartedAt = b.nowLocked()
	var priorHistory []wire.ThreadMessage
	var mentions []mentionedThread
	for _, msg := range td.messages {
		if msg.TurnID == nil || *msg.TurnID != turnID {
			priorHistory = append(priorHistory, msg)
		}
	}
	for id, other := range b.threads {
		// compact 轮次要原样送达命令，不拼 mention 前缀，也就不必扫其它线程。
		if !compactOnly && id != threadID && other.summary.ProjectID == td.summary.ProjectID &&
			(strings.Contains(userText, "@"+other.summary.Title) || strings.Contains(userText, "@"+id)) {
			if err := b.ensureMessagesLocked(other, id); err != nil {
				log.Printf("ensure messages for mentioned thread %s error: %v", id, err)
				continue
			}
			b.ensureMessagesLoadedLocked(other)
			b.touchThreadLocked(other)
			mentions = append(mentions, mentionedThread{ID: id, Title: other.summary.Title, Messages: append([]wire.ThreadMessage(nil), other.messages...)})
		}
	}
	sort.Slice(mentions, func(i, j int) bool { return mentions[i].ID < mentions[j].ID })
	var model string
	if ok && td.summary.Model != nil {
		model = *td.summary.Model
	}
	// Plan 回合按只读跑，但记下用户选的档位：计划获批后 provider 要切回它，否则这一轮剩下的操作会逐个审批。
	var planExitMode harness.PermissionMode
	if interactionMode == "plan" {
		planExitMode = turnMode
		turnMode = harness.ModeReadOnly
	}
	var continuationText string
	if ok && td.continuation != "" && !td.continuationSent {
		continuationText = td.continuation
	}
	gitRunner := b.git
	decorator := b.promptDecorator
	systemPolicyFunc := b.systemPolicyFunc
	// 人设解析器与线程当前人设、provider 快照在锁内取出；解析调用放到解锁之后，避免持锁调用外部函数。
	personaResolver := b.personaTag
	personaID := td.summary.Persona
	personaProvider := td.summary.Provider
	if personaProvider == "" {
		personaProvider = b.defaultProvider
	}
	guidanceBuilder := b.guidanceBuilder
	if guidanceBuilder == nil {
		// 派发给子代理（bee）的线程带精简技能目录，其余线程带全量技能清单。
		if swarm.IsBeePrompt(userText) {
			guidanceBuilder = b.beeGuidanceBuilder
		} else {
			guidanceBuilder = b.defaultGuidanceBuilder
		}
	}
	// 首轮额外段落（普通会话的移交提示）与指导词一起只注入一次。
	firstTurnExtra := b.firstTurnExtra

	// 只有本轮本要续接 provider 会话、或本线程已分割待生效时才询问换新钩子；原生 /compact 要压缩的正是旧会话，不参与。
	// priorHistory 已排除本轮消息（按 TurnID 过滤），所以这里的待分割判定看的是本轮之前的状态。
	splitPending := !compactOnly && pendingSplit(priorHistory)
	var rotator SessionRotator
	var rotationInfo SessionRotationInfo
	if b.sessionRotator != nil && (sessionID != "" || splitPending) && !compactOnly {
		rotator = b.sessionRotator
		rotationInfo = b.sessionRotationInfoLocked(threadID, turnIndex, priorHistory)
		rotationInfo.Manual = splitPending
	}
	b.mu.Unlock()

	// pi 直连后线程里残留的注入模型已失效：回写为默认，界面标签与实际使用的模型一致。
	if personaProvider == "pi" && model != "" && model != "default" && isPiStaleModel(model) {
		model = "default"
		b.resetThreadModel(threadID, model)
	}

	// 会话换新钩子在主锁之外执行。

	// 钩子会读设置，必须在锁外调用；超时或 panic 一律视为不换新。
	rotated := false
	rotationPreamble := ""
	if rotator != nil {
		if rotate, preamble := callSessionRotator(runCtx, rotator, rotationInfo); rotate {
			if extra, ok := b.rotateProviderSession(td, threadID, turnID, sessionID); ok {
				rotated = true
				rotationPreamble = strings.TrimSpace(preamble)
				sessionID = ""
				if continuationText == "" {
					continuationText = extra
				}
				if !rotationInfo.Manual {
					// 手动分割的那条可见消息由 RequestSplit 自己写，不再冒出一条自动换新提示。
					_ = b.AppendNotice(threadID, sessionRotationNotice(rotationInfo))
				}
			}
		}
	}

	var (
		repoRoot string
		gitRun   *git.Runner
	)
	if gitRunner != nil && workDir != "" {
		if root, err := gitRunner.RepoRoot(runCtx, workDir); err == nil && root != "" {
			repoRoot = root
			gitRun = gitRunner
			startRef := git.CheckpointRef(threadID, turnID) + "/start"
			if _, cpErr := gitRun.CheckpointWatched(runCtx, b.watcherFor(repoRoot), repoRoot, startRef, "turn "+turnID+" start"); cpErr != nil {
				log.Printf("checkpoint start failed for thread %s turn %s: %v", threadID, turnID, cpErr)
			}
		}
	}

	// 人设标签前缀只拼在发给 harness 的 Turn.Prompt 最前面：不写进 harnessPrompt，
	// 以免污染时间线里的用户消息、标题生成与搜索。
	personaPrefix := ""
	if !compactOnly && personaResolver != nil && personaID != "" {
		personaPrefix = personaResolver(personaProvider, model, personaID)
	}

	harnessPrompt := userText
	if compactOnly {
		harnessPrompt = compactCommandText
	}
	if rotated && !rotationInfo.Manual {
		// 换新会话只带最近几条对话原文，不叠加整段历史交接；前言在 guidance 注入后补到最前面。
		// 历史先去掉分割线以上的部分，避免分割后又被自动换新把旧内容回放回去。
		harnessPrompt = buildRecentConversationPrompt(historyAfterLastSplit(priorHistory), harnessPrompt)
	} else if !rotated && !compactOnly && !rotationInfo.Manual && needsHandoff && len(priorHistory) > 0 {
		harnessPrompt = buildProviderHandoffPrompt(historyAfterLastSplit(priorHistory), harnessPrompt)
	}
	if contextText := buildMentionContext(mentions); !compactOnly && contextText != "" {
		harnessPrompt = contextText + "\n\n" + harnessPrompt
	}
	if !compactOnly && continuationText != "" {
		harnessPrompt = continuationText + "\n\n" + harnessPrompt
	}
	if !compactOnly && interactionMode == "plan" {
		harnessPrompt = "当前为 Plan 模式：仅分析并制定计划，不修改文件、不执行写操作、不派发会改文件的子任务。\n\n" + harnessPrompt
	}
	if !compactOnly && len(imagePaths) > 0 {
		var filePrompt strings.Builder
		filePrompt.WriteString("\n\n用户附带了以下本地文件附件，可使用文件读取工具查看：")
		for i, filePath := range imagePaths {
			filePrompt.WriteString("\n- ")
			// 附件不止图片（PDF、docx、xlsx 等），带上原始文件名，先让 agent 判断类型再读文件。
			name := ""
			if i < len(imageNames) {
				name = attachmentPromptName(imageNames[i], filePath)
			}
			if name != "" {
				filePrompt.WriteString(name)
				filePrompt.WriteString("：")
			}
			filePrompt.WriteString(filePath)
		}
		harnessPrompt += filePrompt.String()
	}

	// 首轮注入项目 Context 与已启用技能：仅普通消息、未交付过、且该线程尚未有
	// 已完成回复时前置；交付只在本轮运行成功后记录，失败或中断的下一条普通消息会重试。
	guidanceInjected := false
	// 换新后的 provider 会话里没有首轮指导词，不论历史与交付状态都重新注入。
	if !compactOnly && workDir != "" && guidanceBuilder != nil &&
		(rotated || (isGuidanceTriggerText(userText) && b.guidancePending(threadID, priorHistory))) {
		guidance := guidanceBuilder(workDir)
		// 额外的首轮段落（普通会话的「可以交给总管」提示）同样只在首轮出现：
		// 与指导词拼成一段前置，后续轮次不再带，也不会随历史累积。
		extra := ""
		if firstTurnExtra != nil {
			extra = firstTurnExtra(threadID)
		}
		parts := make([]string, 0, 2)
		for _, part := range []string{guidance, extra} {
			if strings.TrimSpace(part) != "" {
				parts = append(parts, part)
			}
		}
		if merged := strings.Join(parts, "\n\n"); merged != "" {
			harnessPrompt = merged + "\n\n" + harnessPrompt
			guidanceInjected = true
		}
	}
	if rotated && rotationPreamble != "" {
		harnessPrompt = rotationPreamble + "\n\n" + harnessPrompt
	}

	// attach 轮不发用户帧，装饰器里带状态的段落（运维提醒等）不能在这里就算「已投递」。
	if !compactOnly && !attachTurn && decorator != nil {
		harnessPrompt = decorator(threadID, harnessPrompt)
	}
	if !compactOnly && b.generalSettings().Session.ConciseReplies() {
		harnessPrompt = ConciseRepliesPrompt + "\n\n" + harnessPrompt
	}

	var (
		currPrompt          = harnessPrompt
		currSessionID       = sessionID
		lostSessionRetried  = false
		crashedRetried      = false
		transientRetryCount = 0

		runErr             error
		receivedErrorEvent bool
		errorEventText     string
		errorEventKind     string
		finalErrText       string
		finalErrKind       string
	)

	// 出队轮次的投递状态每轮只迁移一次：显式回执或首个上游事件把 dispatching 迁到 accepted。
	deliverySettled := deliveryMessageID == ""

	// 本轮开始运行的时刻，用于给上报的用量附上耗时（界面据此算输出速度）。
	turnStartedAt := b.now()
	// attach 只在第一次尝试里生效：那段输出已经读走，重试不该再当 attach 轮。
	firstAttempt := true
	home, _ := os.UserHomeDir()
	readOnlyDirs := projctx.SkillReadDirs(workDir, home)
	// 只有看板任务的执行线程（tasks.thread_id 等于当前线程）才加硬锁；普通交互会话不受影响。
	hardLocked := false
	hardLockOverride := ""
	if b.store != nil {
		if task, err := b.store.TaskByThreadID(runCtx, threadID); err == nil {
			hardLocked = true
			hardLockOverride = taskGateOverride(task.Meta)
		}
	}
	for {
		if runCtx.Err() != nil {
			break
		}

		confirmsDelivery := false
		if dc, ok := h.(harness.DeliveryConfirmer); ok && dc.ConfirmsApprovalDelivery() {
			confirmsDelivery = true
		}

		env := harness.NonInteractiveEnv()
		env = append(env, b.turnMemoryEnv(workDir)...)

		approver := harness.Approver(&threadApprover{b: b, threadID: threadID, turnID: turnID, sessionEpoch: recordedEpoch, confirmsDelivery: confirmsDelivery})
		if hardLocked {
			approver = &hardLockApprover{inner: approver, b: b, threadID: threadID, hardLocked: hardLocked, override: hardLockOverride}
		}

		b.mu.Lock()
		leadPlansOnlyFor := b.swarmLeadPlansOnlyFor
		b.mu.Unlock()
		leadPlansOnly := leadPlansOnlyFor != nil && leadPlansOnlyFor(threadID)
		systemPolicy := ""
		if carrier, ok := h.(harness.SystemPolicyCarrier); ok && carrier.CarriesSystemPolicy(threadID) && systemPolicyFunc != nil {
			systemPolicy = systemPolicyFunc(threadID)
		}

		turn := harness.Turn{
			ID:               turnID,
			ThreadID:         threadID,
			Prompt:           harnessTurnPrompt(compactOnly, personaPrefix, currPrompt),
			SystemPolicy:     systemPolicy,
			WorkDir:          workDir,
			ReadOnlyDirs:     readOnlyDirs,
			SessionID:        currSessionID,
			Model:            model,
			Mode:             turnMode,
			LeadPlansOnly:    leadPlansOnly,
			HardLocked:       hardLocked,
			HardLockOverride: hardLockOverride,
			Approver:         approver,
			Images:           imagePaths,
			InteractionMode:  interactionMode,
			PlanExitMode:     planExitMode,
			Effort:           effort,
			FastMode:         fastMode,
			SpeedTier:        speedTier,
			Transient:        transient,
			// 让 CLI 派生的命令在非交互环境下失败而不是等待 stdin、编辑器或分页器。
			// 同时按项目目录注入 Nowledge Mem 的 NMEM_SPACE（仅当同名空间已存在，本地记忆后端跳过），
			// 避免各项目记忆混在 Default。
			Env: env,
		}
		// attach 轮不发用户帧，只接住常驻进程自发输出的那一段。
		turn.Attach = attachTurn && firstAttempt

		var (
			currentTextMsgID      string
			currentReasoningMsgID string
			toolMsgIDs            = make(map[string]string)
			// toolStreams 收集各工具条目运行期间的增量输出，同一 ToolID 的片段先合并节流再下发。
			toolStreams         = make(map[string]*toolOutputStream)
			streamResetSent     bool
			timingRefreshNeeded = true
		)
		receivedErrorEvent = false
		errorEventText = ""
		errorEventKind = ""

		attemptEpoch := recordedEpoch
		var emitMu sync.Mutex
		acceptEvents := true
		emit := func(e harness.Event) {
			emitMu.Lock()
			defer emitMu.Unlock()
			if !acceptEvents {
				return
			}

			// 出队轮次的投递状态：显式回执或首个上游事件到达表示 provider 已收下 prompt。
			// 失败要等本轮重试耗尽后再由收尾处判定，避免首次报错后重试成功却被记成 failed。
			upstream := e.Type == harness.EventText || e.Type == harness.EventReasoning ||
				e.Type == harness.EventToolStart || e.Type == harness.EventSession
			if !deliverySettled && (e.Type == harness.EventTurnAccepted || upstream) {
				deliverySettled = true
				b.markDeliveryAccepted(threadID, turnID, deliveryMessageID, deliveryFollowUpID)
			}

			// 本轮首个事件到达时先重置助手流式缓冲；事件序列由 emitMu 串行，重置一定在首个正文增量之前。
			if !streamResetSent {
				streamResetSent = true
				var (
					resetHook      func(threadID, projectID string, reset bool, delta string)
					resetProjectID string
				)
				b.mu.Lock()
				if cur, ok := b.threads[threadID]; ok && cur.sessionEpoch == attemptEpoch && cur.runTurnID == turnID {
					resetHook = b.assistantStreamHook
					resetProjectID = cur.summary.ProjectID
				}
				b.mu.Unlock()
				if resetHook != nil {
					resetHook(threadID, resetProjectID, true, "")
				}
			}

			var (
				msgToPublish   []wire.ThreadMessage
				currentSummary wire.ThreadSummary
				streamHook     func(threadID, projectID string, reset bool, delta string)
				streamDelta    string
				streamProject  string
				// bgPersistMsgs 是后台任务事件改写的工具消息：它们可能属于更早回合，回合末的批量落库
				// 按 TurnIndex 筛不到它们，必须解锁后单独写穿到 store。
				bgPersistMsgs []wire.ThreadMessage
			)

			b.mu.Lock()
			td, ok := b.threads[threadID]
			if !ok || td.sessionEpoch != attemptEpoch || td.runTurnID != turnID {
				b.mu.Unlock()
				return
			}
			now := time.Now().UTC().Format(time.RFC3339Nano)

			switch e.Type {
			case harness.EventSession:
				changed := false
				if e.SessionID != "" && td.sessionID != e.SessionID {
					td.sessionID = e.SessionID
					currSessionID = e.SessionID
					changed = true
				}
				s := b.store
				sessID := td.sessionID
				b.mu.Unlock()

				if changed && s != nil {
					pCtx, pCancel := persistCtx()
					if err := s.UpdateThreadSession(pCtx, threadID, sessID); err != nil {
						log.Printf("store update thread session %s error: %v", threadID, err)
					}
					pCancel()
				}
				return

			case harness.EventText:
				// C-RT-20 排版清洗：增量里的破折号先替换为普通连字符，再写入消息与流式缓冲。
				text := withoutEmDashes(e.Text)
				kind := "assistant"
				needNew := currentTextMsgID == ""
				if !needNew && len(td.messages) > 0 {
					lastMsg := td.messages[len(td.messages)-1]
					if lastMsg.Role == "tool" || (lastMsg.Kind != nil && *lastMsg.Kind == "tool") {
						needNew = true
					}
				}

				if needNew {
					// C-RT-21：新建消息时跳过前导空增量——不建消息、不发布、不触发流式钩子，
					// 也不更新 LastActivityText；首个非空增量去掉前导换行后再建消息。
					// 已存在消息上的追加增量不做过滤。
					if isBlankDelta(text) {
						b.mu.Unlock()
						return
					}
					text = strings.TrimLeft(text, "\r\n")
					currentTextMsgID = newUUID()
					msg := wire.ThreadMessage{
						ID:        currentTextMsgID,
						Role:      "assistant",
						Text:      text,
						CreatedAt: now,
						TurnIndex: turnIndex,
						TurnID:    &turnID,
						Kind:      &kind,
					}
					td.messages = append(td.messages, msg)
					msgToPublish = []wire.ThreadMessage{msg}
				} else {
					found := false
					for idx := len(td.messages) - 1; idx >= 0; idx-- {
						if td.messages[idx].ID == currentTextMsgID {
							td.messages[idx].Text += text
							msgToPublish = []wire.ThreadMessage{td.messages[idx]}
							found = true
							break
						}
					}
					if !found {
						currentTextMsgID = newUUID()
						msg := wire.ThreadMessage{
							ID:        currentTextMsgID,
							Role:      "assistant",
							Text:      text,
							CreatedAt: now,
							TurnIndex: turnIndex,
							TurnID:    &turnID,
							Kind:      &kind,
						}
						td.messages = append(td.messages, msg)
						msgToPublish = []wire.ThreadMessage{msg}
					}
				}
				actText := "Generating..."
				td.summary.LastActivityText = &actText
				td.summary.UpdatedAt = now
				currentSummary = b.summaryForPublishLocked(td)
				// EventText 携带的是增量文本（各 Provider 均按 delta 派发），清洗后透传给流式钩子。
				streamHook = b.assistantStreamHook
				streamDelta = text
				streamProject = td.summary.ProjectID

			case harness.EventTextRetract:
				// 上游流中断、残文没被确认：从当前正文消息末尾裁掉这段（写入时做过同样的破折号清洗），
				// 裁完只剩空白就整条移除。新建消息时去掉过前导换行，所以消息全文是残文后缀时也算整条残文。
				if currentTextMsgID == "" {
					b.mu.Unlock()
					return
				}
				retracted := withoutEmDashes(e.Text)
				var removedIDs []string
				for idx := len(td.messages) - 1; idx >= 0; idx-- {
					m := td.messages[idx]
					if m.ID != currentTextMsgID {
						continue
					}
					rest, ok := strings.CutSuffix(m.Text, retracted)
					if !ok {
						if !strings.HasSuffix(retracted, m.Text) {
							log.Printf("thread %s: retracted text is not the tail of message %s, keep it", threadID, m.ID)
							break
						}
						rest = ""
					}
					if isBlankDelta(rest) {
						td.messages = append(td.messages[:idx], td.messages[idx+1:]...)
						td.toolCount.valid = false
						removedIDs = []string{currentTextMsgID}
						currentTextMsgID = ""
					} else {
						td.messages[idx].Text = rest
						msgToPublish = []wire.ThreadMessage{td.messages[idx]}
					}
					break
				}
				if len(removedIDs) == 0 && len(msgToPublish) == 0 {
					b.mu.Unlock()
					return
				}
				td.summary.UpdatedAt = now
				timingCount := len(td.turnTiming)
				b.enrichTurnMessagesLocked(td, msgToPublish)
				timingRefreshNeeded = timingRefreshNeeded || len(td.turnTiming) != timingCount
				currentSummary = b.summaryForPublishLocked(td)
				b.mu.Unlock()
				b.hub.Publish(threadID, msgToPublish, removedIDs, currentSummary)
				return

			case harness.EventReasoning:
				kind := "thinking"
				needNew := currentReasoningMsgID == ""
				if !needNew && len(td.messages) > 0 {
					lastMsg := td.messages[len(td.messages)-1]
					if lastMsg.Role == "tool" || (lastMsg.Kind != nil && *lastMsg.Kind == "tool") {
						needNew = true
					}
				}

				if needNew {
					currentReasoningMsgID = newUUID()
					msg := wire.ThreadMessage{
						ID:        currentReasoningMsgID,
						Role:      "assistant",
						Text:      e.Text,
						CreatedAt: now,
						TurnIndex: turnIndex,
						TurnID:    &turnID,
						Kind:      &kind,
					}
					td.messages = append(td.messages, msg)
					msgToPublish = []wire.ThreadMessage{msg}
				} else {
					found := false
					for idx := len(td.messages) - 1; idx >= 0; idx-- {
						if td.messages[idx].ID == currentReasoningMsgID {
							td.messages[idx].Text += e.Text
							msgToPublish = []wire.ThreadMessage{td.messages[idx]}
							found = true
							break
						}
					}
					if !found {
						currentReasoningMsgID = newUUID()
						msg := wire.ThreadMessage{
							ID:        currentReasoningMsgID,
							Role:      "assistant",
							Text:      e.Text,
							CreatedAt: now,
							TurnIndex: turnIndex,
							TurnID:    &turnID,
							Kind:      &kind,
						}
						td.messages = append(td.messages, msg)
						msgToPublish = []wire.ThreadMessage{msg}
					}
				}
				actText := "Thinking..."
				td.summary.LastActivityText = &actText
				td.summary.UpdatedAt = now
				currentSummary = b.summaryForPublishLocked(td)

			case harness.EventContextCompacted:
				// 上下文压缩是独立边界：先结束正在累积的正文与思考段，再落一条独立系统消息，
				// 后面的正文与思考各自新开一条消息（否则边界会把上一段正文粘到一起）。
				currentTextMsgID = ""
				currentReasoningMsgID = ""
				msg, summary := b.appendSystemMessageLocked(td, contextCompactedKind,
					contextCompactedText(e.CompactTokensBefore, e.CompactTokensAfter), b.nowLocked())
				msgToPublish = []wire.ThreadMessage{msg}
				currentSummary = summary

			case harness.EventToolStart:
				currentTextMsgID = "" // 结束当前正文与思考消息，使工具调用后的正文和思考都新开一条
				currentReasoningMsgID = ""
				toolText := e.ToolName
				if len(e.Input) > 0 {
					inputSummary := string(e.Input)
					if runes := []rune(inputSummary); len(runes) > 200 {
						inputSummary = string(runes[:200])
					}
					if inputSummary != "" {
						toolText += " " + inputSummary
					}
				}
				var toolName *string
				if e.ToolName != "" {
					tn := e.ToolName
					toolName = &tn
				}
				var toolInput *string
				if len(e.Input) > 0 {
					inp := string(e.Input)
					if runes := []rune(inp); len(runes) > 16000 {
						inp = string(runes[:16000])
					}
					toolInput = &inp
				}
				toolStatus := "running"
				toolMsgID := newUUID()
				toolMsgIDs[e.ToolID] = toolMsgID
				toolStreams[e.ToolID] = newToolOutputStream(toolText)
				kind := "tool"
				msg := wire.ThreadMessage{
					ID:         toolMsgID,
					Role:       "tool",
					Text:       toolText,
					CreatedAt:  now,
					TurnIndex:  turnIndex,
					TurnID:     &turnID,
					Kind:       &kind,
					ToolName:   toolName,
					ToolInput:  toolInput,
					ToolStatus: &toolStatus,
				}
				td.messages = append(td.messages, msg)
				msgToPublish = []wire.ThreadMessage{msg}

				// 记录写文件类工具触碰的路径，供运行中轮次的"触碰文件"查询使用。
				if touched := touchedPathsFromToolEvent(e.ToolName, e.Input); len(touched) > 0 {
					td.touchedPaths = append(td.touchedPaths, touched...)
					td.lastFileChangeAt = b.nowLocked()
				}

				// 记录工具读取的文件路径，供 Swarm 报告可信度比对。
				threadWorkDir, _ := b.threadWorkDirLocked(threadID)
				for _, readPath := range extractReadPaths(e.ToolName, e.Input, threadWorkDir) {
					b.recordReadPath(threadID, readPath)
				}

				actText := "Running " + e.ToolName
				td.summary.LastActivityText = &actText
				td.summary.UpdatedAt = now
				currentSummary = b.summaryForPublishLocked(td)

			case harness.EventToolOutput:
				// 运行中的增量输出：按 tool_id 追加到对应条目上，同一 ToolID 的片段先合并节流再下发；
				// 找不到对应条目（tool_start 还没到或条目已结束）时静默忽略。
				stream, streamed := toolStreams[e.ToolID]
				if !streamed {
					b.mu.Unlock()
					return
				}
				nowTime := b.nowLocked()
				if !stream.append(e.Text, nowTime) {
					b.mu.Unlock()
					return
				}
				toolMsgID := toolMsgIDs[e.ToolID]
				for idx, m := range td.messages {
					if m.ID == toolMsgID {
						stream.markFlushed(nowTime)
						td.messages[idx].Text = stream.render()
						msgToPublish = []wire.ThreadMessage{td.messages[idx]}
						break
					}
				}
				if len(msgToPublish) == 0 {
					b.mu.Unlock()
					return
				}
				td.summary.UpdatedAt = now
				currentSummary = b.summaryForPublishLocked(td)

			case harness.EventToolEnd:
				toolMsgID, exists := toolMsgIDs[e.ToolID]
				if exists {
					for idx, m := range td.messages {
						if m.ID == toolMsgID {
							out := e.Output
							if runes := []rune(out); len(runes) > toolOutputStreamMaxRunes {
								out = string(runes[:toolOutputStreamMaxRunes])
							}
							// 运行期间已流出的增量只是过程展示：tool_end 带来最终 output 时用它覆盖，
							// 避免 provider 既推增量又重复一次全量时出现两遍文本；没有最终 output 时保留增量。
							if stream, streamed := toolStreams[e.ToolID]; streamed {
								td.messages[idx].Text = stream.override(out)
							} else if out != "" {
								if td.messages[idx].Text != "" {
									td.messages[idx].Text += "\n" + out
								} else {
									td.messages[idx].Text = out
								}
							}
							// blocked 表示工具被 hook 拦截（或改道），不是工具自身故障：正文不拼 [failed]，状态单列为 blocked。
							if e.IsError && !e.Blocked {
								td.messages[idx].Text += "\n[failed]"
							}
							status := "completed"
							if e.IsError && e.Blocked {
								status = "blocked"
							} else if e.IsError {
								status = "failed"
							}
							// task_started 早于 tool_result 到达时，这里还判不出后台状态：改成 background
							// 且不设完成时间，等 task_notification 按 tool_use_id 回写最终状态。
							background := false
							if !e.IsError && td.backgroundPending[e.ToolID] {
								status = "background"
								background = true
								delete(td.backgroundPending, e.ToolID)
								if td.backgroundTools == nil {
									td.backgroundTools = make(map[string]string)
								}
								td.backgroundTools[e.ToolID] = td.messages[idx].ID
							}
							// 工具自身出错就不会转成后台任务：清掉 pending 与索引，避免残留到后续事件。
							if e.IsError {
								delete(td.backgroundPending, e.ToolID)
								delete(td.backgroundTools, e.ToolID)
							}
							td.messages[idx].ToolStatus = &status
							// 摘要里的后台任务计数按工具状态统计，状态改写后要重新算。
							td.bgCount.invalidate()
							if background {
								td.messages[idx].ToolCompletedAt = nil
							} else {
								completedAt := now
								td.messages[idx].ToolCompletedAt = &completedAt
							}
							msgToPublish = []wire.ThreadMessage{td.messages[idx]}
							break
						}
					}
				}
				// 条目已落定，不再需要增量缓冲。
				delete(toolStreams, e.ToolID)
				td.summary.UpdatedAt = now
				currentSummary = b.summaryForPublishLocked(td)

			case harness.EventBackgroundTaskStart:
				// provider 托管的后台任务开始：CLI 通常先发 tool_result（工具消息已是 completed），
				// 后发 task_started。把发起它的工具消息改成 background 并清空完成时间；tool_end
				// 还没到时先记 pending，等 tool_end 按 background 落定。
				toolMsgID, known := td.backgroundTools[e.ToolID]
				if !known {
					toolMsgID = toolMsgIDs[e.ToolID]
				}
				idx := -1
				for i, m := range td.messages {
					if m.ID == toolMsgID {
						idx = i
						break
					}
				}
				if toolMsgID == "" || idx < 0 {
					// 找不到对应的工具条目（例如 tool_start 已被淘汰）时静默忽略。
					b.mu.Unlock()
					return
				}
				if td.backgroundTools == nil {
					td.backgroundTools = make(map[string]string)
				}
				td.backgroundTools[e.ToolID] = toolMsgID
				if e.TaskID != "" {
					if td.backgroundTaskTools == nil {
						td.backgroundTaskTools = make(map[string]string)
					}
					td.backgroundTaskTools[e.TaskID] = e.ToolID
				}
				if td.messages[idx].ToolStatus != nil && *td.messages[idx].ToolStatus == "running" {
					if td.backgroundPending == nil {
						td.backgroundPending = make(map[string]bool)
					}
					td.backgroundPending[e.ToolID] = true
				} else {
					status := "background"
					td.messages[idx].ToolStatus = &status
					td.messages[idx].ToolCompletedAt = nil
					td.bgCount.invalidate()
					msgToPublish = []wire.ThreadMessage{td.messages[idx]}
					bgPersistMsgs = append(bgPersistMsgs, td.messages[idx])
				}
				td.summary.UpdatedAt = now
				currentSummary = b.summaryForPublishLocked(td)

			case harness.EventBackgroundTaskEnd:
				// provider 托管的后台任务结束：按 tool id 回写最终状态。结束帧常出现在后续 attach
				// 续跑轮次，此时消息可能已被冷会话淘汰，需要先回载。
				if e.ToolID == "" && e.TaskID != "" {
					e.ToolID = td.backgroundTaskTools[e.TaskID]
				}
				delete(td.backgroundTaskTools, e.TaskID)
				toolMsgID, known := td.backgroundTools[e.ToolID]
				if !known {
					toolMsgID = toolMsgIDs[e.ToolID]
				}
				if toolMsgID == "" {
					b.mu.Unlock()
					return
				}
				if td.messagesEvicted || !td.messagesLoaded {
					timingRefreshNeeded = true
				}
				if err := b.ensureMessagesLocked(td, threadID); err != nil {
					b.mu.Unlock()
					return
				}
				b.ensureMessagesLoadedLocked(td)
				idx := -1
				for i, m := range td.messages {
					if m.ID == toolMsgID {
						idx = i
						break
					}
				}
				if idx < 0 {
					delete(td.backgroundTools, e.ToolID)
					delete(td.backgroundPending, e.ToolID)
					b.mu.Unlock()
					return
				}
				// 只回写仍在运行或已标成 background 的条目，避免覆盖之后已经落定的结果。
				current := ""
				if td.messages[idx].ToolStatus != nil {
					current = *td.messages[idx].ToolStatus
				}
				if current != "background" && current != "running" {
					delete(td.backgroundTools, e.ToolID)
					delete(td.backgroundPending, e.ToolID)
					b.mu.Unlock()
					return
				}
				status := "completed"
				if e.TaskStatus != "" && !strings.EqualFold(e.TaskStatus, "completed") {
					status = "failed"
					td.messages[idx].Text += "\n[后台任务结束：" + e.TaskStatus + "]"
				}
				finishBackgroundTaskLocked(td, idx, status, now)
				delete(td.backgroundTools, e.ToolID)
				delete(td.backgroundPending, e.ToolID)
				msgToPublish = []wire.ThreadMessage{td.messages[idx]}
				bgPersistMsgs = append(bgPersistMsgs, td.messages[idx])
				td.summary.UpdatedAt = now
				currentSummary = b.summaryForPublishLocked(td)

			case harness.EventError:
				receivedErrorEvent = true
				errorEventText = e.Err
				// 失败类别随错误正文一起带出去，界面据此直接选横幅样式，不再靠关键词猜。
				errorEventKind = e.ErrKind
				b.mu.Unlock()
				return

			case harness.EventUsage:
				if e.Usage == nil {
					b.mu.Unlock()
					return
				}
				u := *e.Usage
				if elapsed := b.nowLocked().Sub(turnStartedAt).Milliseconds(); elapsed > 0 {
					u.DurationMs = elapsed
				}
				if b.usage == nil {
					b.usage = make(map[string]map[int]harness.Usage)
				}
				if b.usage[threadID] == nil {
					b.usage[threadID] = make(map[int]harness.Usage)
				}
				// provider 最后一条整轮 usage 通常不带逐请求样本，保留本轮已收集的样本，避免被抹掉。
				if len(u.Requests) == 0 {
					if prev, ok := b.usage[threadID][turnIndex]; ok && len(prev.Requests) > 0 {
						u.Requests = append([]harness.RequestSpeed(nil), prev.Requests...)
					}
				}
				b.usage[threadID][turnIndex] = u
				currentSummary = b.summaryForPublishLocked(td)
				b.mu.Unlock()

				b.persistTurnUsage(threadID, turnIndex, u)
				b.hub.Publish(threadID, nil, nil, currentSummary)
				return

			case harness.EventFileChange:
				// Provider 明确上报的文件变更事件（如 Claude 的 Edit/Write/MultiEdit）。
				if e.Path != "" {
					td.touchedPaths = append(td.touchedPaths, e.Path)
					td.lastFileChangeAt = b.nowLocked()
				}
				b.mu.Unlock()
				return

			case harness.EventDone:
				// 忽略或无需单独处理
				b.mu.Unlock()
				return

			default:
				b.mu.Unlock()
				return
			}
			timingCount := len(td.turnTiming)
			b.enrichTurnMessagesLocked(td, msgToPublish)
			if timingRefreshNeeded || len(td.turnTiming) != timingCount {
				for i := range td.messages {
					applyTurnTiming(&td.messages[i], td.turnTiming)
				}
				timingRefreshNeeded = false
			} else {
				for _, msg := range msgToPublish {
					for i := len(td.messages) - 1; i >= 0; i-- {
						if td.messages[i].ID == msg.ID {
							applyTurnTiming(&td.messages[i], td.turnTiming)
							break
						}
					}
				}
			}
			b.mu.Unlock()

			// 后台任务的工具消息可能属于更早回合，回合末批量落库覆盖不到，必须显式写穿。
			if len(bgPersistMsgs) > 0 {
				b.persistMessages(threadID, bgPersistMsgs)
			}

			// 流式钩子在锁外同步调用，保证与事件顺序一致。
			if streamHook != nil {
				streamHook(threadID, streamProject, false, streamDelta)
			}

			if len(msgToPublish) > 0 {
				b.hub.Publish(threadID, msgToPublish, nil, currentSummary)
			}
		}

		runErr = h.Run(runCtx, turn, emit)
		firstAttempt = false
		emitMu.Lock()
		acceptEvents = false
		emitMu.Unlock()

		candidateErr := ""
		candidateErrKind := ""
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			candidateErr = runErr.Error()
			// Run 返回的错误本身不带类别，同一轮里已经收到带类别的错误事件时沿用它。
			if receivedErrorEvent {
				candidateErrKind = errorEventKind
			}
		} else if receivedErrorEvent && errorEventText != "" {
			candidateErr = errorEventText
			candidateErrKind = errorEventKind
		}

		if runCtx.Err() != nil {
			break
		}

		if candidateErr != "" {
			// 1. 会话失效自愈：本轮带着非空 sessionID 恢复会话，并且报错命中失效特征时清空 sessionID，追加 notice 并用重建上下文重跑一次
			if currSessionID != "" && !lostSessionRetried && isLostSessionError(candidateErr) {
				lostSessionRetried = true

				// extra 记录本次需要单独补发到新会话的转发上下文（仅当重跑提示里还没有它时）。
				var extra string
				b.mu.Lock()
				if td, ok := b.threads[threadID]; ok {
					td.sessionID = ""
					td.sessionEpoch++
					// 会话被替换，转发上下文需要在新会话里重新送达。
					td.continuationSent = false
					if continuationText == "" {
						continuationText = td.continuation
						extra = td.continuation
					}
					recordedEpoch = td.sessionEpoch
				}
				s := b.store
				var priorMsgs []wire.ThreadMessage
				if td, ok := b.threads[threadID]; ok {
					for _, m := range td.messages {
						if m.TurnIndex < turnIndex && (m.TurnID == nil || *m.TurnID != turnID) {
							priorMsgs = append(priorMsgs, m)
						}
					}
				}
				b.mu.Unlock()

				if s != nil {
					pCtx, pCancel := persistCtx()
					if err := s.UpdateThreadSession(pCtx, threadID, ""); err != nil {
						log.Printf("store update thread session %s error: %v", threadID, err)
					}
					pCancel()
				}

				_ = b.AppendNotice(threadID, "原会话已失效，已用本地历史重建上下文")

				currSessionID = ""
				currPrompt = buildProviderHandoffPrompt(historyAfterLastSplit(priorMsgs), harnessPrompt)
				if extra != "" {
					currPrompt = extra + "\n\n" + currPrompt
				}
				continue
			}

			// 2. Provider 进程非预期退出自愈 (recoverCrashedCodexTurn)
			if !crashedRetried && isProviderCrashError(candidateErr) && runCtx.Err() == nil {
				crashedRetried = true
				log.Printf("provider crash recovery: thread=%s turn=%s provider=%s model=%q session_present=%t run_error=%t error_kind=%q err=%q", threadID, turnID, h.Kind(), turn.Model, currSessionID != "", runErr != nil, candidateErrKind, RedactSecrets(candidateErr))

				// extra 记录本次需要单独补发到新会话的转发上下文（仅当重跑提示里还没有它时）。
				var extra string
				b.mu.Lock()
				if td, ok := b.threads[threadID]; ok {
					td.sessionID = ""
					td.sessionEpoch++
					// 会话被替换，转发上下文需要在新会话里重新送达。
					td.continuationSent = false
					if continuationText == "" {
						continuationText = td.continuation
						extra = td.continuation
					}
					recordedEpoch = td.sessionEpoch
				}
				s := b.store
				var priorMsgs []wire.ThreadMessage
				if td, ok := b.threads[threadID]; ok {
					for _, m := range td.messages {
						if m.TurnIndex < turnIndex && (m.TurnID == nil || *m.TurnID != turnID) {
							priorMsgs = append(priorMsgs, m)
						}
					}
				}
				b.mu.Unlock()

				if s != nil {
					pCtx, pCancel := persistCtx()
					if err := s.UpdateThreadSession(pCtx, threadID, ""); err != nil {
						log.Printf("store update thread session %s error: %v", threadID, err)
					}
					pCancel()
				}

				_ = b.AppendNotice(threadID, "Provider 进程意外退出，已用本地历史重建上下文")

				currSessionID = ""
				currPrompt = buildProviderHandoffPrompt(historyAfterLastSplit(priorMsgs), harnessPrompt)
				if extra != "" {
					currPrompt = extra + "\n\n" + currPrompt
				}
				continue
			}

			// 网络瞬断最多重试 3 次；计数仅作用于本轮。
			if transientRetryCount < 3 && isTransientNetworkError(candidateErr) {
				transientRetryCount++
				notice := fmt.Sprintf("网络中断，正在重新发送（第 %d 次）", transientRetryCount)
				_ = b.AppendNotice(threadID, notice)

				waitTime := time.Duration(transientRetryCount*3) * time.Second
				retryReady := make(chan struct{})
				stopRetry := b.scheduleAfter(waitTime, func() { close(retryReady) })
				select {
				case <-runCtx.Done():
				case <-retryReady:
				}
				stopRetry()
				if runCtx.Err() != nil {
					break
				}
				continue
			}

			finalErrText = candidateErr
			finalErrKind = candidateErrKind
			if isOutdatedCLIError(candidateErr) {
				_ = b.AppendNotice(threadID, "CLI 版本已过旧，请升级对应 Provider CLI 后重新发送本轮消息；不会自动执行全局安装命令。")
			}
			break
		}

		finalErrText = ""
		finalErrKind = ""
		b.mu.Lock()
		if td.runTurnID == turnID && td.sessionEpoch == recordedEpoch {
			// 换新会话的那一轮若没能建起新会话（provider 没上报会话 ID），保留历史交接，
			// 让下一轮按分割线之后的历史补上下文，而不是把历史凭空丢掉。
			if !rotated || td.sessionID != "" {
				td.needsHandoff = false
			}
		}
		b.mu.Unlock()
		if guidanceInjected {
			b.markGuidanceDelivered(threadID)
		}
		break
	}

	// 重试耗尽仍没有上游事件：provider 启动失败或首个事件就是错误，这一轮没有真正交给 provider，
	// 把出队投递行从 dispatching 记为 failed；已经 accepted（显式回执或上游输出）的轮次不再改判，
	// 已是终态时 store 侧幂等忽略。
	if finalErrText != "" && !deliverySettled {
		b.markDeliveryFailed(threadID, turnID, deliveryMessageID, deliveryFollowUpID, finalErrText)
	}

	b.mu.Lock()
	validTurn := b.threads[threadID] == td && td.sessionEpoch == recordedEpoch && td.runTurnID == turnID
	// 这一轮的 attach 标记用完即清，避免下一轮误当成接自发输出的轮次。
	if td.attachTurnID == turnID {
		td.attachTurnID = ""
	}
	// 送达以 provider 会话已建立为准；失败、未建会话或会话已被替换时保持待送达。
	if validTurn && continuationText != "" && td.sessionID != "" {
		td.continuationSent = true
	}
	b.mu.Unlock()
	if !validTurn {
		// 运行中切换 Provider 会递增 sessionEpoch，本轮随之失效；若新 epoch 没启动新轮次，
		// 必须在这里恢复空闲，否则 IsRunning 永久为 true。
		isNormalEarly, isInterruptedEarly := turnEndState(runCtx, finalErrText)
		b.finishTurnEarly(td, threadID, turnID, turnIndex, isNormalEarly, isInterruptedEarly)
		return
	}
	if gitRun != nil && repoRoot != "" {
		endCtx, endCancel := context.WithTimeout(context.Background(), 30*time.Second)
		endRef := git.CheckpointRef(threadID, turnID) + "/end"
		if _, cpErr := gitRun.CheckpointWatched(endCtx, b.watcherFor(repoRoot), repoRoot, endRef, "turn "+turnID+" end"); cpErr != nil {
			log.Printf("checkpoint end failed for thread %s turn %s: %v", threadID, turnID, cpErr)
		}
		endCancel()
	}

	var (
		finalChangedMsgs []wire.ThreadMessage
		msgsToPersist    []wire.ThreadMessage
		nowTime          time.Time
	)

	b.mu.Lock()
	td, ok = b.threads[threadID]
	if !ok || td.sessionEpoch != recordedEpoch || td.runTurnID != turnID {
		b.mu.Unlock()
		isNormalEarly, isInterruptedEarly := turnEndState(runCtx, finalErrText)
		b.finishTurnEarly(td, threadID, turnID, turnIndex, isNormalEarly, isInterruptedEarly)
		return
	}
	historyOp := make(chan struct{})
	td.historyOp = historyOp

	nowTime = time.Now().UTC()
	now := nowTime.Format(time.RFC3339Nano)

	isInterrupted := runCtx.Err() == context.Canceled && b.ctx.Err() == nil
	if finalErrText != "" && !isInterrupted {
		errKind := "error"
		errMsg := wire.ThreadMessage{
			ID:        newUUID(),
			Role:      "assistant",
			Text:      "出错：" + finalErrText,
			CreatedAt: now,
			TurnIndex: turnIndex,
			TurnID:    &turnID,
			Kind:      &errKind,
		}
		// 类别随消息一起落库，重启后历史里的错误横幅仍能按类别渲染；判不准时不写，界面回退到关键词。
		if finalErrKind != "" {
			kindCopy := finalErrKind
			errMsg.ErrKind = &kindCopy
		}
		td.messages = append(td.messages, errMsg)
		finalChangedMsgs = append(finalChangedMsgs, errMsg)
	}

	// 最后一条 assistant 正文消息的 IsFinalResponse 设为 true
	for i := len(td.messages) - 1; i >= 0; i-- {
		if td.messages[i].TurnID != nil && *td.messages[i].TurnID == turnID && td.messages[i].Role == "assistant" && td.messages[i].Kind != nil && (*td.messages[i].Kind == "assistant" || *td.messages[i].Kind == "text") {
			isFinal := true
			td.messages[i].IsFinalResponse = &isFinal
			finalChangedMsgs = append(finalChangedMsgs, td.messages[i])
			break
		}
	}

	td.summary.UpdatedAt = now

	for _, m := range td.messages {
		if m.TurnIndex == turnIndex {
			msgsToPersist = append(msgsToPersist, m)
		}
	}
	s := b.store
	b.mu.Unlock()

	b.persistMessages(threadID, msgsToPersist)
	if s != nil {
		pCtx, pCancel := persistCtx()
		if err := s.TouchThread(pCtx, threadID, nowTime); err != nil {
			log.Printf("store touch thread %s error: %v", threadID, err)
		}
		pCancel()
	}

	status := "completed"
	if runCtx.Err() != nil {
		status = "interrupted"
	} else if finalErrText != "" {
		status = "failed"
	}
	b.updateTurnFinish(threadID, turnID, turnIndex, status, finalErrText)
	b.mu.Lock()
	var timingSummary wire.ThreadSummary
	if cur := b.threads[threadID]; cur != nil {
		finalChangedMsgs = nil
		for _, msg := range cur.messages {
			if msg.TurnID != nil && *msg.TurnID == turnID {
				finalChangedMsgs = append(finalChangedMsgs, msg)
			}
		}
		timingSummary = b.summaryForPublishLocked(cur)
	}
	b.mu.Unlock()
	b.hub.Publish(threadID, finalChangedMsgs, nil, timingSummary)
	b.hub.Flush(threadID)
	close(historyOp)
	b.mu.Lock()
	if curTd, exists := b.threads[threadID]; exists && curTd.historyOp == historyOp {
		curTd.historyOp = nil
	}
	b.mu.Unlock()

	var finalSummary wire.ThreadSummary
	var (
		nextText     string
		queueChanged bool
		nextStarted  *startedTurn

		hookProjectID         string
		hookLastAssistantText string
		hookCompleted         bool
		turnHook              func(threadID, projectID, assistantText string, completed bool)
		retryUnsolicited      bool
	)
	autoContinueAfterLimit := b.generalSettings().Session.AutoContinueAfterLimit
	b.mu.Lock()
	if td, ok := b.threads[threadID]; ok {
		if td.sessionEpoch != recordedEpoch || td.runTurnID != turnID {
			b.mu.Unlock()
			isNormalEarly, isInterruptedEarly := turnEndState(runCtx, finalErrText)
			b.finishTurnEarly(td, threadID, turnID, turnIndex, isNormalEarly, isInterruptedEarly)
			return
		}
		if td.watchdogStop != nil {
			td.watchdogStop()
			td.watchdogStop = nil
		}
		td.summary.LastActivityText = nil
		td.summary.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		// 注意：td.runCancel() 会取消本轮 runCtx，必须在调用前判断本轮是否被中断。
		isInterrupted := runCtx.Err() != nil
		isNormal := !isInterrupted && finalErrText == ""
		if !isNormal && !isInterrupted && finalErrText != "" {
			if autoContinueAfterLimit && isUsageLimitError(finalErrText) {
				b.scheduleAutoContinueLocked(threadID, finalErrText)
			}
		}
		if td.runCancel != nil {
			td.runCancel()
			td.runCancel = nil
		}
		td.runTurnID = ""
		for i := len(td.messages) - 1; i >= 0; i-- {
			if td.messages[i].Role == "assistant" && td.messages[i].TurnID != nil && *td.messages[i].TurnID == turnID {
				hookLastAssistantText = td.messages[i].Text
				break
			}
		}
		finalSummary = b.summaryForPublishLocked(td)

		hookProjectID = td.summary.ProjectID
		hookCompleted = isNormal
		turnHook = b.turnFinishedHook

		// 派发完成前仍视为运行中，避免跟进消息抢在 bee 前启动。
		b.mu.Unlock()
		// Plan 模式的派发限制由派发器门禁负责。
		if turnHook != nil {
			turnHook(threadID, hookProjectID, hookLastAssistantText, hookCompleted)
		}
		hasBees := b.hasWorkingSwarmBees(threadID)
		b.mu.Lock()
		if b.threads[threadID] != td || td.sessionEpoch != recordedEpoch {
			b.mu.Unlock()
			b.finishTurnEarly(td, threadID, turnID, turnIndex, isNormal, isInterrupted)
			return
		}
		td.setIdleLocked()
		retryUnsolicited = td.takeUnsolicitedPendingLocked()
		if isNormal || isInterrupted {
			td.summary.Attention = "ready"
			// 正常结束（含被中断）时清空错误原文，摘要里不再保留上一轮的失败原因。
			td.summary.LastErrorText = ""
		} else {
			// 出错结束：时间线上已有“Stopped with an error.”提示，
			// 这里用 attention "failed" 把结果带给手机端推送与桌面通知；
			// 错误原文随摘要下发，供额度提醒解析恢复时间。
			td.summary.Attention = "failed"
			td.summary.LastErrorText = truncateLastErrorText(finalErrText)
		}

		finalSummary = b.summaryForPublishLocked(td)

		// 宿主关闭时保留队列不动，避免排队项在关闭途中被移除后既没发送也无法恢复。
		if b.ctx.Err() == nil {
			nextStarted, nextText, queueChanged = b.startNextQueuedTurnLocked(td, isNormal && !hasBees, isNormal)
		}
	}
	b.mu.Unlock()

	b.hub.Publish(threadID, nil, nil, finalSummary)
	if nextStarted != nil {
		b.hub.Flush(threadID)
		// 正文先落库再删队列行，崩溃时最多重复出现一条已写正文的排队项，不会丢消息。
		b.persistMessages(threadID, []wire.ThreadMessage{nextStarted.userMsg})
		b.recordDispatchingForTurn(threadID, nextStarted)
	}
	if queueChanged {
		b.commitQueueChange(threadID)
	}

	if nextStarted != nil {
		if nextStarted.titleChanged {
			b.persistThreadTitle(threadID, nextStarted.currentSummary.Title)
		}
		b.hub.BeginRun(threadID)
		b.hub.Publish(threadID, []wire.ThreadMessage{nextStarted.userMsg}, nil, nextStarted.currentSummary)
		b.launchTurnRun(threadID, nextStarted, nextText)
	} else {
		b.hub.Publish(threadID, nil, nil, finalSummary)
	}
	if retryUnsolicited {
		go b.retryUnsolicitedTurn(threadID)
	}
}

func (b *MemoryBackend) simulateAssistantReply(runCtx context.Context, threadID string, turnID string, userText string, turnIndex int) {
	fullReply := "echo: " + userText
	runes := []rune(fullReply)
	n := len(runes)

	segments := make([]string, 4)
	for i := 1; i <= 4; i++ {
		end := (n * i) / 4
		if i == 4 {
			end = n
		}
		segments[i-1] = string(runes[:end])
	}

	assistantMsgID := newUUID()
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)

	// 模拟回复的分段是累积前缀：先以 reset=true 重置流式缓冲，再逐段给出增量后缀。
	var (
		streamHook      func(threadID, projectID string, reset bool, delta string)
		streamProjectID string
	)
	b.mu.Lock()
	if td, ok := b.threads[threadID]; ok {
		streamHook = b.assistantStreamHook
		streamProjectID = td.summary.ProjectID
	}
	b.mu.Unlock()
	if streamHook != nil {
		streamHook(threadID, streamProjectID, true, "")
	}
	prevText := ""

loop:
	for i, seg := range segments {
		select {
		case <-time.After(simulatedReplyStepDelay):
		case <-runCtx.Done():
			break loop
		}
		if runCtx.Err() != nil {
			break loop
		}

		// seg 是累积前缀，只有新增部分作为流式增量。
		delta := seg
		if suffix, ok := strings.CutPrefix(seg, prevText); ok {
			delta = suffix
		}
		prevText = seg

		b.mu.Lock()
		td, ok := b.threads[threadID]
		if !ok || td.runTurnID != turnID {
			b.mu.Unlock()
			b.finishTurnEarly(td, threadID, turnID, turnIndex, false, runCtx.Err() != nil)
			return
		}

		now := time.Now().UTC().Format(time.RFC3339Nano)
		kind := "text"
		isFinal := i == 3
		assistantMsg := wire.ThreadMessage{
			ID:              assistantMsgID,
			Role:            "assistant",
			Text:            seg,
			CreatedAt:       createdAt,
			TurnIndex:       turnIndex,
			TurnID:          &turnID,
			Kind:            &kind,
			IsFinalResponse: &isFinal,
		}

		applyTurnTiming(&assistantMsg, td.turnTiming)
		found := false
		for idx, m := range td.messages {
			if m.ID == assistantMsgID {
				td.messages[idx] = assistantMsg
				found = true
				break
			}
		}
		if !found {
			td.messages = append(td.messages, assistantMsg)
		}

		td.summary.UpdatedAt = now
		if !isFinal {
			actText := "Generating..."
			td.summary.LastActivityText = &actText
		}
		currentSummary := b.summaryForPublishLocked(td)
		b.mu.Unlock()

		if streamHook != nil && delta != "" {
			streamHook(threadID, streamProjectID, false, delta)
		}
		if !isFinal {
			b.hub.Publish(threadID, []wire.ThreadMessage{assistantMsg}, nil, currentSummary)
		}
	}

	// 结束时先落库，再把线程摘要里表示运行状态的字段改回空闲并 Publish
	var (
		msgsToPersist []wire.ThreadMessage
		nowTime       time.Time
	)
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok || td.runTurnID != turnID {
		b.mu.Unlock()
		b.finishTurnEarly(td, threadID, turnID, turnIndex, false, runCtx.Err() != nil)
		return
	}
	nowTime = time.Now().UTC()
	now := nowTime.Format(time.RFC3339Nano)
	td.summary.UpdatedAt = now

	for _, m := range td.messages {
		if m.TurnIndex == turnIndex {
			msgsToPersist = append(msgsToPersist, m)
		}
	}
	s := b.store
	b.mu.Unlock()

	b.persistMessages(threadID, msgsToPersist)
	if s != nil {
		pCtx, pCancel := persistCtx()
		if err := s.TouchThread(pCtx, threadID, nowTime); err != nil {
			log.Printf("store touch thread %s error: %v", threadID, err)
		}
		pCancel()
	}

	status := "completed"
	if runCtx.Err() != nil {
		status = "interrupted"
	}
	b.updateTurnFinish(threadID, turnID, turnIndex, status, "")
	var finalTurnMsg []wire.ThreadMessage
	b.mu.Lock()
	if cur := b.threads[threadID]; cur != nil {
		for _, msg := range cur.messages {
			if msg.TurnID != nil && *msg.TurnID == turnID {
				finalTurnMsg = append(finalTurnMsg, msg)
			}
		}
	}
	projectionSummary := b.summaryForPublishLocked(td)
	b.mu.Unlock()
	b.hub.Publish(threadID, finalTurnMsg, nil, projectionSummary)
	b.hub.Flush(threadID)
	var finalSummary wire.ThreadSummary
	var (
		nextText     string
		queueChanged bool
		nextStarted  *startedTurn

		hookProjectID         string
		hookLastAssistantText string
		hookCompleted         bool
		turnHook              func(threadID, projectID, assistantText string, completed bool)
		retryUnsolicited      bool
	)
	b.mu.Lock()
	if td, ok := b.threads[threadID]; ok && td.runTurnID == turnID {
		td.summary.LastActivityText = nil
		td.summary.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		// 注意：td.runCancel() 会取消本轮 runCtx，必须在调用前判断本轮是否被中断。
		isInterrupted := runCtx.Err() != nil
		isNormal := !isInterrupted
		if td.runCancel != nil {
			td.runCancel()
			td.runCancel = nil
		}
		td.runTurnID = ""
		finalSummary = b.summaryForPublishLocked(td)

		hookProjectID = td.summary.ProjectID
		hookCompleted = isNormal
		for i := len(td.messages) - 1; i >= 0; i-- {
			if td.messages[i].Role == "assistant" && td.messages[i].TurnID != nil && *td.messages[i].TurnID == turnID {
				hookLastAssistantText = td.messages[i].Text
				break
			}
		}
		turnHook = b.turnFinishedHook

		// 派发完成前仍视为运行中，避免跟进消息抢在 bee 前启动。
		b.mu.Unlock()
		if turnHook != nil {
			turnHook(threadID, hookProjectID, hookLastAssistantText, hookCompleted)
		}
		hasBees := b.hasWorkingSwarmBees(threadID)
		b.mu.Lock()
		if b.threads[threadID] != td {
			b.mu.Unlock()
			return
		}
		td.setIdleLocked()
		retryUnsolicited = td.takeUnsolicitedPendingLocked()
		finalSummary = b.summaryForPublishLocked(td)

		// 宿主关闭时保留队列不动，避免排队项在关闭途中被移除后既没发送也无法恢复。
		if b.ctx.Err() == nil {
			nextStarted, nextText, queueChanged = b.startNextQueuedTurnLocked(td, isNormal && !hasBees, isNormal)
		}
	}
	b.mu.Unlock()

	b.hub.Publish(threadID, nil, nil, finalSummary)
	if nextStarted != nil {
		b.hub.Flush(threadID)
		// 正文先落库再删队列行，崩溃时最多重复出现一条已写正文的排队项，不会丢消息。
		b.persistMessages(threadID, []wire.ThreadMessage{nextStarted.userMsg})
		b.recordDispatchingForTurn(threadID, nextStarted)
	}
	if queueChanged {
		b.commitQueueChange(threadID)
	}

	if nextStarted != nil {
		if nextStarted.titleChanged {
			b.persistThreadTitle(threadID, nextStarted.currentSummary.Title)
		}
		b.hub.Flush(threadID)
		b.hub.BeginRun(threadID)
		b.hub.Publish(threadID, []wire.ThreadMessage{nextStarted.userMsg}, nil, nextStarted.currentSummary)
		b.launchTurnRun(threadID, nextStarted, nextText)
	} else {
		b.hub.Publish(threadID, nil, nil, finalSummary)
	}
	if retryUnsolicited {
		go b.retryUnsolicitedTurn(threadID)
	}
}
