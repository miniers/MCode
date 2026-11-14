// 本文件包含 MemoryBackend 的中断、后台任务与轮次撤回方法。
package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"mcode/internal/harness"
	"mcode/internal/procs"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
	"mcode/internal/swarm"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ActionStatus 查询指定动作的执行状态。若动作不存在或已超时，返回 unknown 状态，不返回错误。
func (b *MemoryBackend) ActionStatus(ctx context.Context, actionID string) (wire.ActionStatusResult, error) {
	if res, ok := b.actions.get(actionID); ok {
		return res, nil
	}
	return wire.ActionStatusResult{
		ActionID: actionID,
		State:    wire.ActionStateUnknown,
	}, nil
}

func (td *memoryThread) hasWaitingQuestionLocked() bool {
	for _, pr := range td.pending {
		if !pr.handled && pr.providerWaiting && pr.req.Kind == string(harness.RequestKindQuestion) {
			return true
		}
	}
	return false
}

func (td *memoryThread) setPendingSendLocked(fu wire.FollowUp) {
	if td.pendingSend != nil {
		td.followUps = append([]wire.FollowUp{*td.pendingSend}, td.followUps...)
		td.queueRevision++
	}
	td.pendingSend = &fu
}

func (b *MemoryBackend) interruptLocked(td *memoryThread, turnID string) (wire.InterruptResult, *withdrawBatch, error) {
	b.ensureMessagesLoadedLocked(td)
	if !td.summary.IsRunning {
		// 本轮已结束但仍有遗留卡片时，中断入口也顺手撤掉，避免它们一直挂起。
		batch := b.takeWithdrawBatchLocked(td, withdrawInterrupt)
		turnIDVal := turnID
		if turnIDVal == "" {
			turnIDVal = td.runTurnID
			if turnIDVal == "" {
				for i := len(td.messages) - 1; i >= 0; i-- {
					if td.messages[i].TurnID != nil && *td.messages[i].TurnID != "" {
						turnIDVal = *td.messages[i].TurnID
						break
					}
				}
			}
		}
		return wire.InterruptResult{
			ThreadID: td.summary.ID,
			TurnID:   turnIDVal,
			State:    wire.InterruptStateAlreadyStopped,
		}, batch, nil
	}

	if td.stewardBusy && td.runTurnID == "" && td.runCancel == nil {
		return wire.InterruptResult{ThreadID: td.summary.ID, State: wire.InterruptStateAlreadyStopped}, nil, nil
	}

	if turnID != "" && turnID != td.runTurnID {
		return wire.InterruptResult{}, nil, &wire.RPCError{
			Code:    wire.ErrCodeTurnMismatch,
			Message: fmt.Sprintf("turnID %s does not match active turn %s", turnID, td.runTurnID),
		}
	}

	activeTurnID := td.runTurnID

	if td.watchdogStop != nil {
		return wire.InterruptResult{ThreadID: td.summary.ID, TurnID: activeTurnID, State: wire.InterruptStateStopping}, nil, nil
	}

	// 先撤掉待处理卡片再取消运行：撤卡不依赖 runCancel，卡片会立刻从列表和收件箱消失。
	batch := b.takeWithdrawBatchLocked(td, withdrawInterrupt)

	if td.phase == phaseStarting {
		b.forceInterruptFinishLocked(td, activeTurnID, "")
		return wire.InterruptResult{ThreadID: td.summary.ID, TurnID: activeTurnID, State: wire.InterruptStateStopping}, batch, nil
	}

	// phaseRunning
	if td.runCancel != nil {
		td.runCancel()
	}

	timeout := b.interruptWatchdogTimeout
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	targetThreadID := td.summary.ID
	targetTurnID := activeTurnID
	stopFn := scheduleAfter(b.afterFunc, timeout, func() {
		b.mu.Lock()
		td, ok := b.threads[targetThreadID]
		if !ok || td.runTurnID != targetTurnID || !td.summary.IsRunning {
			b.mu.Unlock()
			return
		}
		batch := b.forceInterruptFinishLocked(td, targetTurnID, "打断超时，已强制结束本轮")
		b.mu.Unlock()
		// 观察者在锁外回调，广播必须在解锁后进行。
		b.finishWithdraw(batch, withdrawInterrupt)
	})
	td.watchdogStop = stopFn

	return wire.InterruptResult{
		ThreadID: td.summary.ID,
		TurnID:   activeTurnID,
		State:    wire.InterruptStateStopping,
	}, batch, nil
}

func (b *MemoryBackend) forceInterruptFinishLocked(td *memoryThread, turnID string, noticeText string) *withdrawBatch {
	b.ensureMessagesLoadedLocked(td)
	// 强制收尾同样要先撤卡：卡片不再可答，Provider 侧也不必等 runCancel 生效。
	batch := b.takeWithdrawBatchLocked(td, withdrawInterrupt)
	if td.watchdogStop != nil {
		td.watchdogStop()
		td.watchdogStop = nil
	}
	if td.runCancel != nil {
		td.runCancel()
		td.runCancel = nil
	}
	td.sessionEpoch++
	// 会话被替换，转发上下文需要在新会话里重新送达。
	td.continuationSent = false
	td.runTurnID = ""
	td.setIdleLocked()
	retryUnsolicited := td.takeUnsolicitedPendingLocked()
	// 强制收尾不会经过 Interact 的清理块，这里显式复位等待标记，避免摘要一直显示"等待审批"。
	td.summary.HasPendingRequest = false
	td.summary.LastActivityText = nil
	now := b.nowLocked().Format(time.RFC3339Nano)
	td.summary.UpdatedAt = now
	turnIndex := 0
	for _, m := range td.messages {
		if m.TurnID != nil && *m.TurnID == turnID {
			turnIndex = m.TurnIndex
		}
	}
	var msgs []wire.ThreadMessage
	if noticeText != "" {
		kind := "info"
		msg := wire.ThreadMessage{ID: newUUID(), Role: "system", Text: noticeText, CreatedAt: now, TurnIndex: turnIndex, TurnID: &turnID, Kind: &kind}
		td.messages = append(td.messages, msg)
		msgs = append(msgs, msg)
	}
	var msgsToPersist []wire.ThreadMessage
	for _, msg := range td.messages {
		if msg.TurnID != nil && *msg.TurnID == turnID {
			msgsToPersist = append(msgsToPersist, msg)
		}
	}
	threadID := td.summary.ID
	h, sessionID := b.threadHarnessLocked(td.summary.Provider), td.sessionID
	previousHistory := td.historyOp
	done := make(chan struct{})
	td.historyOp = done
	if noticeText != "" {
		td.sessionID = ""
		td.needsHandoff = true
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		if noticeText != "" {
			if stopper, ok := h.(harness.TurnStopper); ok {
				stopper.ForceStopTurn(turnID)
			}
			if sessionID != "" {
				b.persistThreadSessionReset(threadID)
			}
		}
		if previousHistory != nil {
			<-previousHistory
		}
		b.persistMessages(threadID, msgsToPersist)
		b.updateTurnFinish(threadID, turnID, turnIndex, "interrupted", noticeText)
		b.mu.Lock()
		msgs = nil
		for _, msg := range td.messages {
			if msg.TurnID != nil && *msg.TurnID == turnID {
				msgs = append(msgs, msg)
			}
		}
		summary := b.summaryForPublishLocked(td)
		b.mu.Unlock()
		b.hub.Publish(threadID, msgs, nil, summary)
		b.hub.Flush(threadID)
		b.mu.Lock()
		if td.historyOp == done {
			td.historyOp = nil
		}
		close(done)
		b.mu.Unlock()
		b.mu.Lock()
		var next *startedTurn
		var text string
		var changed bool
		if b.threads[threadID] == td && !td.summary.IsRunning && !td.takingBack && b.ctx.Err() == nil {
			// 强制打断路径：priority 汇报也不立即开轮，留给用户下一句话。
			next, text, changed = b.startNextQueuedTurnLocked(td, false, false)
		}
		b.mu.Unlock()
		if next != nil {
			// 正文先落库再删队列行，崩溃时最多重复出现一条已写正文的排队项，不会丢消息。
			b.persistMessages(threadID, []wire.ThreadMessage{next.userMsg})
			b.recordDispatchingForTurn(threadID, next)
		}
		if changed {
			b.commitQueueChange(threadID)
		}
		if next != nil {
			b.hub.BeginRun(threadID)
			b.hub.Publish(threadID, []wire.ThreadMessage{next.userMsg}, nil, next.currentSummary)
			b.launchTurnRun(threadID, next, text)
		}
		if retryUnsolicited {
			go b.retryUnsolicitedTurn(threadID)
		}
	}()
	return batch
}

func (b *MemoryBackend) persistThreadSessionReset(threadID string) {
	if b.store != nil {
		ctx, cancel := persistCtx()
		defer cancel()
		if err := b.store.UpdateThreadSession(ctx, threadID, ""); err != nil {
			log.Printf("store reset session %s: %v", threadID, err)
		}
	}
}

// Interrupt 中断指定线程的当前回复轮次。
func (b *MemoryBackend) Interrupt(ctx context.Context, params wire.InterruptParams) (wire.InterruptResult, error) {
	b.mu.Lock()

	b.cancelAutoContinueLocked(params.ThreadID)

	td, ok := b.threads[params.ThreadID]
	if !ok {
		b.mu.Unlock()
		return wire.InterruptResult{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", params.ThreadID),
		}
	}

	res, batch, err := b.interruptLocked(td, params.TurnID)
	b.mu.Unlock()
	// 先解锁再广播：观察者可能在回调里重新进入后端。
	b.finishWithdraw(batch, withdrawInterrupt)
	return res, err
}

// InterruptCurrent 中断指定线程当前正在运行的这一轮，不校验 turnID；未运行时返回 already_stopped。
func (b *MemoryBackend) InterruptCurrent(ctx context.Context, threadID string) (wire.InterruptResult, error) {
	b.mu.Lock()

	b.cancelAutoContinueLocked(threadID)

	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return wire.InterruptResult{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}

	res, batch, err := b.interruptLocked(td, "")
	b.mu.Unlock()
	b.finishWithdraw(batch, withdrawInterrupt)
	return res, err
}

// KillRunningCommand 终止指定会话当前正在运行的命令，不中断本轮对话。
// 没有运行中的轮次或 Provider 不支持时返回 killed=0，不返回错误。
func (b *MemoryBackend) KillRunningCommand(ctx context.Context, threadID, messageID string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return 0, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}
	if td.runTurnID == "" {
		b.mu.Unlock()
		return 0, nil
	}
	b.ensureMessagesLoadedLocked(td)
	// 首选用工具消息的创建时间：它就是这条命令开始跑的时刻。拿不到时退回本轮开始时间。
	since := td.runStartedAt
	if messageID != "" {
		for _, msg := range td.messages {
			if msg.ID != messageID {
				continue
			}
			if t, err := time.Parse(time.RFC3339Nano, msg.CreatedAt); err == nil {
				since = t
			}
			break
		}
	}
	h := b.threadHarnessLocked(td.summary.Provider)
	killer, _ := h.(harness.CommandKiller)
	rooter, _ := h.(harness.ProcessRooter)
	b.mu.Unlock()

	if killer != nil {
		killed, err := killer.KillRunningCommands(threadID, killRunningCommandGrace)
		if errors.Is(err, procs.ErrNotSupported) {
			return 0, nil
		}
		return killed, err
	}
	if rooter == nil || since.IsZero() {
		return 0, nil
	}
	var (
		killed   int
		firstErr error
	)
	for _, root := range rooter.ProcessRootPIDs(threadID) {
		n, err := procs.KillTreesStartedAfter(root, since, killRunningCommandGrace)
		if err != nil {
			// 平台不支持时不报错，其余错误先记下，成功杀掉进程时不再上抛。
			if !errors.Is(err, procs.ErrNotSupported) && firstErr == nil {
				firstErr = err
			}
			continue
		}
		killed += n
	}
	if killed == 0 && firstErr != nil {
		return 0, firstErr
	}
	return killed, nil
}

// backgroundTaskMessageLocked 查找后台任务消息，调用方持有 b.mu。
func (b *MemoryBackend) backgroundTaskMessageLocked(threadID, messageID string) (*memoryThread, int, error) {
	td, ok := b.threads[threadID]
	if !ok {
		return nil, -1, &wire.RPCError{Code: wire.ErrCodeResourceNotFound, Message: "会话不存在"}
	}
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		return nil, -1, err
	}
	b.ensureMessagesLoadedLocked(td)
	for i := range td.messages {
		if td.messages[i].ID == messageID {
			return td, i, nil
		}
	}
	return nil, -1, &wire.RPCError{Code: wire.ErrCodeResourceNotFound, Message: "消息不存在"}
}

// BackgroundTaskLog 读取后台任务日志的末尾。
func (b *MemoryBackend) BackgroundTaskLog(ctx context.Context, threadID, messageID string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	b.mu.Lock()
	td, idx, err := b.backgroundTaskMessageLocked(threadID, messageID)
	var text string
	if err == nil && td.messages[idx].ToolStatus != nil && *td.messages[idx].ToolStatus == "background" {
		text = td.messages[idx].Text
	}
	b.mu.Unlock()
	if err != nil {
		return "", false, err
	}
	return readBackgroundTaskOutput(text)
}

// readBackgroundTaskOutput 校验输出路径并读取后台任务日志的末尾，调用方不得持有 b.mu。
func readBackgroundTaskOutput(text string) (string, bool, error) {
	_, path := backgroundTaskDetails(text)
	if !filepath.IsAbs(path) {
		return "", false, nil
	}
	path = filepath.Clean(path)
	if filepath.Base(filepath.Dir(path)) != "tasks" || !backgroundTaskFilePattern.MatchString(filepath.Base(path)) {
		return "", false, nil
	}
	validParent := false
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if strings.HasPrefix(part, "claude-") {
			validParent = true
			break
		}
	}
	if !validParent {
		return "", false, nil
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", true, nil
	}
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, nil
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", true, nil
	}
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	openedInfo, err := f.Stat()
	if err != nil {
		return "", false, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return "", false, nil
	}
	offset := openedInfo.Size() - backgroundTaskLogMaxBytes
	if offset < 0 {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return "", false, err
	}
	data, err := io.ReadAll(io.LimitReader(f, backgroundTaskLogMaxBytes))
	if err != nil {
		return "", false, err
	}
	if openedInfo.Size() > backgroundTaskLogMaxBytes {
		if newline := bytes.IndexByte(data, '\n'); newline >= 0 {
			data = data[newline+1:]
		} else {
			data = nil
		}
	}
	lines := strings.Split(string(data), "\n")
	end := len(lines)
	if end > 0 && lines[end-1] == "" {
		end--
	}
	if end > backgroundTaskLogMaxLines {
		lines = lines[end-backgroundTaskLogMaxLines:]
	}
	return strings.Join(lines, "\n"), true, nil
}

// StopBackgroundTask 请求 provider 停止任务并回写工具消息。
func (b *MemoryBackend) StopBackgroundTask(ctx context.Context, threadID, messageID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	b.mu.Lock()
	td, idx, err := b.backgroundTaskMessageLocked(threadID, messageID)
	if err != nil {
		b.mu.Unlock()
		return false, err
	}
	msg := td.messages[idx]
	if msg.ToolStatus == nil || *msg.ToolStatus != "background" {
		b.mu.Unlock()
		return false, errors.New("任务未在后台运行")
	}
	taskID, _ := backgroundTaskDetails(msg.Text)
	if taskID == "" {
		for id, toolID := range td.backgroundTaskTools {
			if td.backgroundTools[toolID] == messageID {
				taskID = id
				break
			}
		}
	}
	stopper, ok := b.threadHarnessLocked(td.summary.Provider).(backgroundTaskStopper)
	b.mu.Unlock()
	if taskID == "" {
		return false, errors.New("找不到后台任务 ID")
	}
	if !ok {
		return false, errors.New("当前 provider 不支持停止后台任务")
	}
	if err := stopper.StopBackgroundTask(ctx, threadID, taskID); err != nil {
		return false, err
	}
	b.mu.Lock()
	td, idx, err = b.backgroundTaskMessageLocked(threadID, messageID)
	if err != nil {
		b.mu.Unlock()
		return false, err
	}
	if td.messages[idx].ToolStatus == nil || *td.messages[idx].ToolStatus != "background" {
		b.mu.Unlock()
		return true, nil
	}
	now := formatTime(b.nowLocked())
	finishBackgroundTaskLocked(td, idx, "stopped", now)
	for toolID, id := range td.backgroundTools {
		if id == messageID {
			delete(td.backgroundTools, toolID)
			delete(td.backgroundPending, toolID)
			for id, taskToolID := range td.backgroundTaskTools {
				if taskToolID == toolID {
					delete(td.backgroundTaskTools, id)
				}
			}
		}
	}
	td.summary.UpdatedAt = now
	msg = td.messages[idx]
	messages := []wire.ThreadMessage{msg}
	b.enrichTurnMessagesLocked(td, messages)
	summary := b.summaryForPublishLocked(td)
	b.mu.Unlock()
	b.persistMessages(threadID, messages)
	b.hub.Publish(threadID, messages, nil, summary)
	return true, nil
}

// RecallTurn 收回指定会话正在运行的一轮：打断并等待空闲，从消息与数据库中删除该轮消息，将 turns 状态改为 taken_back，返回原 Prompt。
func (b *MemoryBackend) RecallTurn(ctx context.Context, threadID string) (wire.TurnRecallResult, error) {
	return b.recallTurn(ctx, threadID, false)
}

func (b *MemoryBackend) recallTurn(ctx context.Context, threadID string, draftOnly bool) (wire.TurnRecallResult, error) {
	if err := ctx.Err(); err != nil {
		return wire.TurnRecallResult{}, err
	}
	hasBees := b.hasWorkingSwarmBees(threadID)
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return wire.TurnRecallResult{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}
	b.ensureMessagesLoadedLocked(td)

	if td.takingBack || !td.summary.IsRunning || td.runTurnID == "" {
		b.mu.Unlock()
		return wire.TurnRecallResult{}, &wire.RPCError{
			Code:    wire.ErrCodeOperationRejected,
			Message: "no running turn to take back",
		}
	}

	var draftMessageID string
	if draftOnly {
		if len(td.messages) == 0 || td.pendingSend != nil || len(td.pending) > 0 {
			b.mu.Unlock()
			return wire.TurnRecallResult{}, errors.New("当前轮次不可撤回")
		}
		last := td.messages[len(td.messages)-1]
		if last.Role != "user" || last.TurnID == nil || *last.TurnID != td.runTurnID || len(last.Media) > 0 {
			b.mu.Unlock()
			return wire.TurnRecallResult{}, errors.New("当前轮次已有输出或附件，不能撤回")
		}
		draftMessageID = last.ID
	}

	if hasBees {
		b.mu.Unlock()
		return wire.TurnRecallResult{}, &wire.RPCError{
			Code:    wire.ErrCodeOperationRejected,
			Message: "cannot take back turn while swarm bees are working",
		}
	}

	turnID := td.runTurnID
	var userPrompt string
	var attachments []wire.MediaRef
	for i := 0; i < len(td.messages); i++ {
		if td.messages[i].TurnID != nil && *td.messages[i].TurnID == turnID && td.messages[i].Role == "user" {
			userPrompt = td.messages[i].Text
			attachments = append([]wire.MediaRef{}, td.messages[i].Media...)
			break
		}
	}

	if swarm.IsNotice(userPrompt) || swarm.IsBeePrompt(userPrompt) {
		b.mu.Unlock()
		return wire.TurnRecallResult{}, &wire.RPCError{
			Code:    wire.ErrCodeOperationRejected,
			Message: "cannot take back turn dispatched by swarm",
		}
	}

	td.takingBack = true
	defer func(target *memoryThread) {
		b.mu.Lock()
		target.takingBack = false
		retryUnsolicited := b.threads[threadID] == target && target.takeUnsolicitedPendingLocked()
		b.mu.Unlock()
		if retryUnsolicited {
			go b.retryUnsolicitedTurn(threadID)
		}
	}(td)
	b.cancelAutoContinueLocked(threadID)
	_, batch, err := b.interruptLocked(td, turnID)
	if err != nil {
		b.mu.Unlock()
		return wire.TurnRecallResult{}, err
	}

	var waitCh chan struct{}
	if td.phase != phaseIdle {
		waitCh = make(chan struct{})
		td.idleWaiters = append(td.idleWaiters, waitCh)
	}
	b.mu.Unlock()
	// 撤回整轮时顺手撤下的卡片只从列表消失，不额外宣称"已中断"。
	b.publishWithdrawn(batch)

	timeoutCh := make(chan struct{})
	stopTimer := b.scheduleAfter(10*time.Second, func() {
		close(timeoutCh)
	})
	defer stopTimer()
	if waitCh != nil {
		defer func(target *memoryThread) {
			b.mu.Lock()
			for i, waiter := range target.idleWaiters {
				if waiter == waitCh {
					target.idleWaiters = append(target.idleWaiters[:i], target.idleWaiters[i+1:]...)
					break
				}
			}
			b.mu.Unlock()
		}(td)
		select {
		case <-waitCh:
		case <-timeoutCh:
			return wire.TurnRecallResult{}, errors.New("timeout waiting for turn to become idle")
		case <-ctx.Done():
			return wire.TurnRecallResult{}, ctx.Err()
		}
	}

	b.mu.Lock()
	td, ok = b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return wire.TurnRecallResult{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}

	historyOp := td.historyOp
	b.mu.Unlock()
	if historyOp != nil {
		select {
		case <-historyOp:
		case <-timeoutCh:
			return wire.TurnRecallResult{}, errors.New("timeout waiting for turn history")
		case <-ctx.Done():
			return wire.TurnRecallResult{}, ctx.Err()
		}
	}
	b.mu.Lock()
	if b.threads[threadID] != td {
		b.mu.Unlock()
		return wire.TurnRecallResult{}, store.ErrNotFound
	}
	if draftOnly && (len(td.messages) == 0 || td.messages[len(td.messages)-1].ID != draftMessageID) {
		b.mu.Unlock()
		return wire.TurnRecallResult{}, errors.New("当前轮次已有输出，不能撤回")
	}
	if b.store != nil {
		pCtx, pCancel := persistCtx()
		err := b.store.RecallTurn(pCtx, threadID, turnID)
		pCancel()
		if err != nil {
			b.mu.Unlock()
			return wire.TurnRecallResult{}, err
		}
	}
	var removedIDs []string
	var keptMsgs []wire.ThreadMessage
	for _, m := range td.messages {
		if m.TurnID != nil && *m.TurnID == turnID {
			removedIDs = append(removedIDs, m.ID)
			delete(td.persisted, m.ID)
			delete(b.usage[threadID], m.TurnIndex)
			if m.Role == "user" && userPrompt == "" {
				userPrompt = m.Text
			}
		} else {
			keptMsgs = append(keptMsgs, m)
		}
	}
	td.messages = keptMsgs
	td.toolCount.valid = false
	if td.takenBack == nil {
		td.takenBack = make(map[string]bool)
	}
	td.takenBack[turnID] = true
	td.summary.UpdatedAt = b.nowLocked().Format(time.RFC3339Nano)
	summary := b.summaryForPublishLocked(td)
	td.sessionID = ""
	td.sessionEpoch++
	// 会话被替换，转发上下文需要在新会话里重新送达。
	td.continuationSent = false
	td.needsHandoff = true
	b.mu.Unlock()

	b.persistThreadSessionReset(threadID)

	if b.hub != nil {
		b.hub.Publish(threadID, nil, removedIDs, summary)
	}

	return wire.TurnRecallResult{
		ThreadID:    threadID,
		TurnID:      turnID,
		Prompt:      userPrompt,
		Attachments: attachments,
	}, nil
}
