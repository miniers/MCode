// 本文件包含 MemoryBackend 的Git 仓库与差异方法。
package remote

import (
	"context"
	"errors"
	"fmt"
	"mcode/internal/git"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
	"sort"
	"strings"
	"time"
)

// SetGitRunner 设置 Git 执行器。
func (b *MemoryBackend) SetGitRunner(r *git.Runner) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.git = r
}

// watcherFor 返回指定仓库根目录对应的检查点文件监听器，按需懒创建并缓存。
// 调用方不得持有 b.mu。
func (b *MemoryBackend) watcherFor(root string) git.Watcher {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.watchers == nil {
		b.watchers = make(map[string]git.Watcher)
	}
	if w, ok := b.watchers[root]; ok {
		return w
	}
	w := git.NewWatcher(root)
	b.watchers[root] = w
	return w
}

// resolveTurnRevert 解析撤销指定轮次所需的 git 执行器、仓库根目录、基础/完成检查点与线程全部轮次。
// base 取该轮的 BaseCheckpoint，缺失时回退到 start ref；
// finish 取最后一个非空的 FinishCheckpoint，缺失时回退到最后一轮的 end ref，再缺失时对当前工作区打临时快照。
func (b *MemoryBackend) resolveTurnRevert(ctx context.Context, threadID, turnID string) (*turnRevertTarget, error) {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return nil, fmt.Errorf("thread %s not found", threadID)
	}
	if td.runTurnID != "" || td.summary.IsRunning {
		b.mu.Unlock()
		return nil, errors.New("会话正在运行，无法撤销")
	}
	// C-ST-07：撤销要按内存消息回滚历史，先确保历史已回载。
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		b.mu.Unlock()
		return nil, err
	}
	b.ensureMessagesLoadedLocked(td)
	b.touchThreadLocked(td)
	runner := b.git
	s := b.store
	b.mu.Unlock()

	if runner == nil {
		return nil, errors.New("git 不可用，无法撤销")
	}
	if s == nil {
		return nil, errors.New("store 不可用，无法撤销")
	}

	workDir, ok := b.ThreadWorkDir(threadID)
	if !ok || workDir == "" {
		return nil, fmt.Errorf("thread %s work dir not found", threadID)
	}
	root, err := runner.RepoRoot(ctx, workDir)
	if err != nil {
		return nil, fmt.Errorf("resolve repo root for thread %s: %w", threadID, err)
	}
	if root == "" {
		return nil, fmt.Errorf("thread %s work dir is not a git repository", threadID)
	}

	// 撤销会还原工作区文件：同一工作区还有别的会话在跑，或有子代理正在把改动合并进来时，
	// 先拒绝撤销，避免两边的改动互相覆盖。两处检查都在改动任何文件之前完成。
	b.mu.Lock()
	busyReason := b.revertWorkspaceBusyLocked(threadID, root)
	busyCheck := b.revertBusyCheck
	b.mu.Unlock()
	if busyReason != "" {
		return nil, errors.New(busyReason)
	}
	// 钩子由宿主注入，可能查询编排管理等其他锁，必须放在 b.mu 之外调用。
	if busyCheck != nil {
		if reason := busyCheck(threadID, root); reason != "" {
			return nil, errors.New(reason)
		}
	}

	target := &turnRevertTarget{runner: runner, root: root}

	if t, gerr := s.GetTurn(ctx, turnID); gerr == nil {
		target.base = t.BaseCheckpoint
	} else if !errors.Is(gerr, store.ErrNotFound) {
		return nil, fmt.Errorf("get turn %s: %w", turnID, gerr)
	}
	if target.base == "" {
		if sha, perr := runner.RevParse(ctx, root, git.CheckpointRef(threadID, turnID)+"/start"); perr == nil {
			target.base = sha
		}
	}
	if target.base == "" {
		return nil, fmt.Errorf("turn %s has no base checkpoint, cannot revert", turnID)
	}

	turns, err := s.ListTurns(ctx, threadID)
	if err != nil {
		return nil, fmt.Errorf("list turns for thread %s: %w", threadID, err)
	}
	target.turns = turns
	for _, t := range turns {
		if t.FinishCheckpoint != "" {
			target.finish = t.FinishCheckpoint
		}
	}
	if target.finish == "" && len(turns) > 0 {
		last := turns[len(turns)-1]
		if sha, perr := runner.RevParse(ctx, root, git.CheckpointRef(threadID, last.ID)+"/end"); perr == nil {
			target.finish = sha
		}
	}
	if target.finish == "" {
		// 没有可用的结束检查点时，对当前工作区打一个临时快照作为比较基准。
		snapshotRef := git.CheckpointRef(threadID, "revert-now")
		sha, cerr := runner.CheckpointWatched(ctx, b.watcherFor(root), root, snapshotRef, "revert now")
		if cerr != nil {
			return nil, fmt.Errorf("create fallback checkpoint for thread %s: %w", threadID, cerr)
		}
		target.finish = sha
	}

	return target, nil
}

// PreviewTurnRevert 预览撤销指定轮次及其之后所有轮次的代码变更效果。
func (b *MemoryBackend) PreviewTurnRevert(ctx context.Context, threadID, turnID string) (*git.RestorePreview, error) {
	target, err := b.resolveTurnRevert(ctx, threadID, turnID)
	if err != nil {
		return nil, err
	}
	return target.runner.PreviewRestore(ctx, target.root, target.base, target.finish)
}

// ExecuteTurnRevert 执行撤销指定轮次及其之后所有轮次的代码变更，并同步回滚历史。
// 文件层面只还原自结束检查点以来未被二次修改的内容；历史层面把该轮及其之后的轮次标记为撤回，
// 删除这些轮次的消息与用量，并清空 provider 会话以便下一轮从剩余历史重建上下文。
func (b *MemoryBackend) ExecuteTurnRevert(ctx context.Context, threadID, turnID string) (*git.RestorePreview, error) {
	target, err := b.resolveTurnRevert(ctx, threadID, turnID)
	if err != nil {
		return nil, err
	}

	preview, err := target.runner.ExecuteRevert(ctx, target.root, target.base, target.finish)
	if err != nil {
		return nil, err
	}

	// 目标轮及其之后的所有轮次（ListTurns 已按 TurnIndex 升序）
	var rollbackIDs []string
	targetIndex := -1
	for _, t := range target.turns {
		if t.ID == turnID {
			targetIndex = t.TurnIndex
			break
		}
	}
	if targetIndex >= 0 {
		for _, t := range target.turns {
			if t.TurnIndex >= targetIndex {
				rollbackIDs = append(rollbackIDs, t.ID)
			}
		}
	}

	for _, id := range rollbackIDs {
		if err := b.store.RecallTurn(ctx, threadID, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return preview, fmt.Errorf("take back turn %s: %w", id, err)
		}
	}

	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return preview, nil
	}
	if td.runTurnID != "" || td.summary.IsRunning {
		b.mu.Unlock()
		return preview, errors.New("会话正在运行，无法撤销")
	}
	// C-ST-07：resolveTurnRevert 放锁后会话可能又被淘汰，这里要按内存消息裁剪，需重新确保已回载。
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		b.mu.Unlock()
		return preview, err
	}
	b.ensureMessagesLoadedLocked(td)
	b.touchThreadLocked(td)
	rollbackSet := make(map[string]bool, len(rollbackIDs))
	for _, id := range rollbackIDs {
		rollbackSet[id] = true
	}
	var removedIDs []string
	keptMsgs := make([]wire.ThreadMessage, 0, len(td.messages))
	for _, m := range td.messages {
		drop := targetIndex >= 0 && (m.TurnIndex >= targetIndex || (m.TurnID != nil && rollbackSet[*m.TurnID]))
		if !drop {
			keptMsgs = append(keptMsgs, m)
			continue
		}
		removedIDs = append(removedIDs, m.ID)
		delete(td.persisted, m.ID)
		delete(b.usage[threadID], m.TurnIndex)
	}
	td.messages = keptMsgs
	td.toolCount.valid = false
	if len(rollbackIDs) > 0 {
		if td.takenBack == nil {
			td.takenBack = make(map[string]bool)
		}
		for _, id := range rollbackIDs {
			td.takenBack[id] = true
		}
		// provider 会话里仍保留被回滚的历史，清空会话以便下一轮从剩余历史重建。
		td.sessionID = ""
		td.sessionEpoch++
		// 会话被替换，转发上下文需要在新会话里重新送达。
		td.continuationSent = false
		td.needsHandoff = true
	}
	td.summary.UpdatedAt = b.nowLocked().Format(time.RFC3339Nano)
	summary := b.summaryForPublishLocked(td)
	b.mu.Unlock()

	if len(rollbackIDs) > 0 {
		b.persistThreadSessionReset(threadID)
	}
	if b.hub != nil {
		b.hub.Publish(threadID, nil, removedIDs, summary)
	}

	return preview, nil
}

// GetDiff 获取会话整库或指定轮次的代码差异。
func (b *MemoryBackend) GetDiff(ctx context.Context, params wire.ThreadsGetDiffParams) (wire.ThreadsGetDiffResult, error) {
	state, err := b.diffThreadState(params.ThreadID)
	if err != nil {
		return wire.ThreadsGetDiffResult{}, err
	}
	workDir, gitRunner, msgs := state.workDir, state.git, state.messages
	isRunning, runningTurnID := state.running, state.runningTurnID
	takenBack, s := state.takenBack, state.store
	storeTurns := make(map[string]store.Turn)
	if s != nil {
		turns, err := s.ListTurns(ctx, params.ThreadID)
		if err != nil {
			return wire.ThreadsGetDiffResult{}, err
		}
		for _, turn := range turns {
			if turn.Status == "taken_back" {
				takenBack[turn.ID] = true
			}
			storeTurns[turn.ID] = turn
		}
	}

	neutralResult := func(reason string) (wire.ThreadsGetDiffResult, error) {
		return wire.ThreadsGetDiffResult{
			ThreadID:      params.ThreadID,
			TurnID:        params.TurnID,
			Diff:          "",
			Truncated:     false,
			FilesChanged:  []string{},
			Turns:         []wire.ReviewTurn{},
			FailureReason: &reason,
		}, nil
	}

	if gitRunner == nil {
		return neutralResult("git is not available")
	}
	if workDir == "" {
		return neutralResult("work directory not specified")
	}

	repoRoot, err := gitRunner.RepoRoot(ctx, workDir)
	if err != nil {
		return neutralResult("not a git repository")
	}

	prefix := fmt.Sprintf("refs/mcode/checkpoints/%s/", params.ThreadID)
	refs, err := gitRunner.ListRefs(ctx, repoRoot, prefix)
	if err != nil {
		return neutralResult("failed to list checkpoint refs: " + err.Error())
	}

	type turnRefPair struct {
		turnID   string
		startRef string
		endRef   string
	}
	turnMap := make(map[string]*turnRefPair)
	for _, ref := range refs {
		rest := strings.TrimPrefix(ref.Name, prefix)
		parts := strings.Split(rest, "/")
		if len(parts) != 2 {
			continue
		}
		tID, kind := parts[0], parts[1]
		if takenBack[tID] {
			continue
		}
		tr, exists := turnMap[tID]
		if !exists {
			tr = &turnRefPair{turnID: tID}
			turnMap[tID] = tr
		}
		if kind == "start" {
			tr.startRef = ref.Name
		} else if kind == "end" {
			tr.endRef = ref.Name
		}
	}

	// 收集会话历史消息中涉及的 turnID 的出现顺序
	var orderedTurnIDs []string
	turnIndices := make(map[string]int)
	turnStatus := make(map[string]string)
	seenTurn := make(map[string]bool)

	for _, msg := range msgs {
		if msg.TurnID != nil && *msg.TurnID != "" {
			tID := *msg.TurnID
			if !seenTurn[tID] {
				seenTurn[tID] = true
				orderedTurnIDs = append(orderedTurnIDs, tID)
				turnIndices[tID] = msg.TurnIndex
			}
			if msg.Kind != nil && *msg.Kind == "error" {
				turnStatus[tID] = "failed"
			}
		}
	}

	var orderedTurns []*turnRefPair
	processed := make(map[string]bool)

	// 首先按会话消息中 turn 的出现顺序排序
	for _, tID := range orderedTurnIDs {
		if tr, ok := turnMap[tID]; ok {
			orderedTurns = append(orderedTurns, tr)
			processed[tID] = true
		}
	}

	// 对于不在消息历史中但在 Git 检查点中存在的 turn（例如测试或未记录消息的轮次），
	// 按 turnID 升序排序作为保底，确保返回顺序确定性
	var remaining []*turnRefPair
	for tID, tr := range turnMap {
		if !processed[tID] {
			remaining = append(remaining, tr)
		}
	}
	if len(remaining) > 0 {
		sort.Slice(remaining, func(i, j int) bool {
			return remaining[i].turnID < remaining[j].turnID
		})
		for _, tr := range remaining {
			orderedTurns = append(orderedTurns, tr)
		}
	}

	// 检查点优先取 store 中持久化的字段，缺失时回退到 refs，兼容旧会话数据。
	turnStart := func(tr *turnRefPair) string {
		if st, ok := storeTurns[tr.turnID]; ok && st.BaseCheckpoint != "" {
			return st.BaseCheckpoint
		}
		return tr.startRef
	}
	turnFinish := func(tr *turnRefPair) string {
		if st, ok := storeTurns[tr.turnID]; ok && st.FinishCheckpoint != "" {
			return st.FinishCheckpoint
		}
		return tr.endRef
	}
	// turnPaths 返回该轮记录的文件集合（相对仓库根目录规范化）；空集合表示历史数据缺失。
	turnPaths := func(turnID string) []string {
		st, ok := storeTurns[turnID]
		if !ok || len(st.TouchedFiles) == 0 {
			return nil
		}
		return normalizeTouchedFiles(repoRoot, repoRoot, st.TouchedFiles)
	}

	// 构造 turns 摘要列表（只统计最近 turnMetadataLimit 轮：截断后的轮次不会下发，
	// 长会话里为每一轮起一个 git numstat 子进程代价过高）。
	statTurns := orderedTurns
	if len(statTurns) > turnMetadataLimit {
		statTurns = statTurns[:turnMetadataLimit]
	}
	var turns []wire.ReviewTurn
	for idx, tr := range statTurns {
		tIdx := idx
		if ti, ok := turnIndices[tr.turnID]; ok {
			tIdx = ti
		}
		status := "completed"
		if isRunning && runningTurnID == tr.turnID {
			status = "running"
		} else if st, ok := turnStatus[tr.turnID]; ok {
			status = st
		}

		hasChanges := false
		if start := turnStart(tr); start != "" {
			stats, sErr := gitRunner.NumStat(ctx, repoRoot, start, turnFinish(tr), turnPaths(tr.turnID))
			if sErr == nil && len(stats) > 0 {
				hasChanges = true
			}
		}

		turns = append(turns, wire.ReviewTurn{
			TurnID:     tr.turnID,
			Index:      tIdx,
			Status:     status,
			HasChanges: hasChanges,
		})
	}
	if turns == nil {
		turns = []wire.ReviewTurn{}
	}

	var metadataTruncated *bool
	if len(orderedTurns) > turnMetadataLimit {
		mt := true
		metadataTruncated = &mt
	}

	// 计算 diff 和 filesChanged
	var (
		diffFrom     string
		diffTo       string
		diffText     string
		truncated    bool
		diffPaths    []string
		filesChanged = []string{}
	)

	const maxDiffBytes = 512 * 1024

	if params.TurnID != nil && *params.TurnID != "" {
		tID := *params.TurnID
		if tr, ok := turnMap[tID]; ok {
			diffFrom = turnStart(tr)
			diffTo = turnFinish(tr) // 若该轮没有结束检查点，则与当前工作区比较
		} else if st, ok := storeTurns[tID]; ok && !takenBack[tID] {
			diffFrom = st.BaseCheckpoint
			diffTo = st.FinishCheckpoint
		}
		diffPaths = turnPaths(tID)
	} else if len(orderedTurns) > 0 {
		firstIdx := -1
		for i, tr := range orderedTurns {
			if turnStart(tr) != "" {
				firstIdx = i
				break
			}
		}
		if firstIdx >= 0 {
			diffFrom = turnStart(orderedTurns[firstIdx])
			lastEndIdx := -1
			for i, tr := range orderedTurns {
				if end := turnFinish(tr); end != "" {
					lastEndIdx = i
					diffTo = end
				}
			}
			endIdx := lastEndIdx
			if endIdx < firstIdx {
				// 没有任何轮次记录结束检查点：退回与当前工作区比较。
				diffTo = ""
				endIdx = len(orderedTurns) - 1
			}
			// 只有区间内所有轮次都有 TouchedFiles 时才按路径过滤；历史数据缺少该字段时
			// 退回全库 diff，避免旧会话因过滤变成空态。
			complete := true
			var union []string
			for _, tr := range orderedTurns[firstIdx : endIdx+1] {
				paths := turnPaths(tr.turnID)
				if len(paths) == 0 {
					complete = false
					break
				}
				union = append(union, paths...)
			}
			if complete {
				diffPaths = normalizeTouchedFiles(repoRoot, repoRoot, union)
			}
		}
	}

	if diffFrom != "" {
		dt, trunc, dErr := gitRunner.DiffText(ctx, repoRoot, diffFrom, diffTo, diffPaths, maxDiffBytes)
		if dErr != nil {
			return neutralResult("git diff failed: " + dErr.Error())
		}
		diffText = dt
		truncated = trunc

		stats, sErr := gitRunner.NumStat(ctx, repoRoot, diffFrom, diffTo, diffPaths)
		if sErr == nil {
			seenPaths := make(map[string]bool)
			for _, st := range stats {
				if st.Path != "" && !seenPaths[st.Path] {
					seenPaths[st.Path] = true
					filesChanged = append(filesChanged, st.Path)
				}
				if st.OldPath != "" && !seenPaths[st.OldPath] {
					seenPaths[st.OldPath] = true
					filesChanged = append(filesChanged, st.OldPath)
				}
			}
			sort.Strings(filesChanged)
		}
	}

	return wire.ThreadsGetDiffResult{
		ThreadID:          params.ThreadID,
		TurnID:            params.TurnID,
		Diff:              diffText,
		Truncated:         truncated,
		FilesChanged:      filesChanged,
		Turns:             turns,
		MetadataTruncated: metadataTruncated,
	}, nil
}
