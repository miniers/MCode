package app

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"mcode/internal/attention"
	"mcode/internal/config"
	"mcode/internal/kanban"
	"mcode/internal/store"
	"mcode/internal/swarm"
)

const (
	// noProgressInterval 是无进展巡检间隔；阈值是分钟级，5 分钟粒度足够。
	noProgressInterval = 5 * time.Minute
	// noProgressScanTimeout 是单轮巡检的超时时间。
	noProgressScanTimeout = 30 * time.Second
)

// sessionProgressSource 是看门狗需要的最小会话状态读取能力，*remote.MemoryBackend 满足它。
type sessionProgressSource interface {
	SessionState(ctx context.Context, threadID string) (kanban.SessionState, error)
}

// noProgressCandidate 是一轮巡检里确认仍在运行、值得观察进展的对象。
type noProgressCandidate struct {
	subjectType      attention.SubjectType
	subjectID        string
	threadID         string
	projectID        string
	title            string
	lastFileChangeAt time.Time
	startedAt        time.Time
	boardFingerprint string
}

// noProgressState 是单个对象在状态表里的记录。
type noProgressState struct {
	subjectType    attention.SubjectType
	subjectID      string
	lastProgressAt time.Time
	fingerprint    string
	enteredAt      time.Time
	reported       bool
}

// noProgressAction 是状态机算出的一个动作：消解已有事件，或按 enteredAt 记录新事件。
type noProgressAction struct {
	resolve   bool
	cand      noProgressCandidate
	enteredAt time.Time
}

// noProgressKey 是状态表的对象键。
func noProgressKey(subjectType attention.SubjectType, subjectID string) string {
	return string(subjectType) + "|" + subjectID
}

// taskProgressFingerprint 只拼接有意义的任务字段，作为「看板状态是否变化」的指纹。
// 刻意不拼 UpdatedAt 与 Revision：催促等看门狗自身写库会刷新它们，否则会不断重置计时。
func taskProgressFingerprint(t store.Task) string {
	return strings.Join([]string{
		t.Status,
		derefString(t.ThreadID),
		derefString(t.ReviewThreadID),
		fmt.Sprintf("%d", t.AttemptCount),
		fmt.Sprintf("%d", t.RejectCount),
		derefString(t.ReviewStatus),
		derefString(t.LastFailureReason),
		t.Title,
		t.Notes,
	}, "\x00")
}

// noProgressBaseline 取对象可用的最早进展时刻：文件改动时间与开始时间取较新者。
func noProgressBaseline(cand noProgressCandidate) time.Time {
	at := cand.lastFileChangeAt
	if cand.startedAt.After(at) {
		at = cand.startedAt
	}
	return at
}

// diffNoProgress 按状态机比较上一轮状态与这一轮候选，返回要执行的写库动作。
// 纯函数便于测试：只改 prev 表，不做任何 I/O；记录动作的 reported 由调用方在真正写入后置位。
func diffNoProgress(prev map[string]*noProgressState, cands []noProgressCandidate, now time.Time, threshold time.Duration) []noProgressAction {
	var actions []noProgressAction
	seen := make(map[string]struct{}, len(cands))
	for _, cand := range cands {
		key := noProgressKey(cand.subjectType, cand.subjectID)
		seen[key] = struct{}{}
		st, ok := prev[key]
		if !ok {
			st = &noProgressState{
				subjectType:    cand.subjectType,
				subjectID:      cand.subjectID,
				lastProgressAt: noProgressBaseline(cand),
				fingerprint:    cand.boardFingerprint,
			}
			if st.lastProgressAt.IsZero() {
				st.lastProgressAt = now
			}
			prev[key] = st
		}

		progress := false
		if !cand.lastFileChangeAt.IsZero() && cand.lastFileChangeAt.After(st.lastProgressAt) {
			st.lastProgressAt = cand.lastFileChangeAt
			progress = true
		}
		if cand.boardFingerprint != "" && cand.boardFingerprint != st.fingerprint {
			st.fingerprint = cand.boardFingerprint
			st.lastProgressAt = now
			progress = true
		}
		if progress {
			st.enteredAt = time.Time{}
			if st.reported {
				st.reported = false
				actions = append(actions, noProgressAction{resolve: true, cand: cand})
			}
			continue
		}

		stallAt := st.lastProgressAt.Add(threshold)
		if now.Before(stallAt) {
			continue
		}
		if st.enteredAt.IsZero() {
			st.enteredAt = stallAt
		}
		if !st.reported {
			actions = append(actions, noProgressAction{cand: cand, enteredAt: st.enteredAt})
		}
	}

	for key, st := range prev {
		if _, ok := seen[key]; ok {
			continue
		}
		if st.reported {
			actions = append(actions, noProgressAction{
				resolve: true,
				cand:    noProgressCandidate{subjectType: st.subjectType, subjectID: st.subjectID},
			})
		}
		delete(prev, key)
	}
	return actions
}

// noProgressDetail 生成事件正文，分钟数取实际阈值。
func noProgressDetail(c noProgressCandidate, threshold time.Duration) string {
	minutes := int(threshold / time.Minute)
	if minutes <= 0 {
		minutes = 1
	}
	detail := fmt.Sprintf("连续 %d 分钟没有文件改动，也没有看板状态变化", minutes)
	return detail
}

// noProgressInput 把候选与进入时刻组装成待处理事件。
func noProgressInput(cand noProgressCandidate, enteredAt time.Time, threshold time.Duration) attention.Input {
	return attention.Input{
		Kind:        attention.KindThreadNoProgress,
		SubjectType: cand.subjectType,
		SubjectID:   cand.subjectID,
		ProjectID:   cand.projectID,
		ThreadID:    cand.threadID,
		Title:       cand.title,
		Detail:      noProgressDetail(cand, threshold),
		EnteredAt:   enteredAt,
	}
}

// noProgressLoop 每 5 分钟巡检一次；未接入存储或会话状态时直接退出。
func (w *attentionWiring) noProgressLoop() {
	defer w.wg.Done()
	if w.store == nil || w.sessions == nil {
		return
	}
	ticker := time.NewTicker(noProgressInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.done:
			return
		case <-ticker.C:
			w.checkNoProgress()
		}
	}
}

// checkNoProgress 巡检一轮：阈值与开关每次现读配置，改设置后无需重启。
// 关闭时对所有已上报对象产出消解动作并清空状态表。
func (w *attentionWiring) checkNoProgress() {
	if w.store == nil || w.sessions == nil || w.dataDir == "" {
		return
	}
	cfg, err := config.Load(w.dataDir)
	if err != nil {
		log.Printf("attention: 读取配置失败，跳过本轮无进展巡检: %v", err)
		return
	}
	threshold := cfg.Orchestration.Steward.NoProgressAfter()
	now := w.nowTime()

	ctx, cancel := context.WithTimeout(context.Background(), noProgressScanTimeout)
	defer cancel()

	var cands []noProgressCandidate
	if threshold > 0 {
		cands = w.noProgressCandidates(ctx, cfg, now, threshold)
	}
	w.mu.Lock()
	actions := diffNoProgress(w.noProgress, cands, now, threshold)
	w.mu.Unlock()
	w.applyNoProgressActions(actions, threshold)
}

// isIgnored 判断会话是否被忽略；isIgnoredLocked 要求持有 mu，这里统一加锁。
func (w *attentionWiring) isIgnored(threadID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.isIgnoredLocked(threadID)
}

// noProgressCandidates 收集本轮仍在运行、且不属于卡住提醒范围的任务会话与 bee。
func (w *attentionWiring) noProgressCandidates(ctx context.Context, cfg config.Config, now time.Time, threshold time.Duration) []noProgressCandidate {
	var cands []noProgressCandidate

	if w.store != nil {
		tasks, err := w.store.ListTasks(ctx, "")
		if err != nil {
			log.Printf("attention: 列出看板任务失败，跳过本轮任务候选: %v", err)
		}
		for _, task := range tasks {
			if task.Status != string(kanban.TaskStatusInProgress) {
				continue
			}
			if task.ThreadID == nil || *task.ThreadID == "" {
				continue
			}
			threadID := *task.ThreadID
			if w.isIgnored(threadID) {
				continue
			}
			st, err := w.sessions.SessionState(ctx, threadID)
			if err != nil {
				continue
			}
			if !st.Running || st.AwaitingApproval || st.AwaitingInput || st.Failed {
				continue
			}
			if w.withinStalledAlert(cfg, threadID, now) {
				continue
			}
			cands = append(cands, noProgressCandidate{
				subjectType:      attention.SubjectThread,
				subjectID:        threadID,
				threadID:         threadID,
				projectID:        derefString(task.ProjectID),
				title:            task.Title,
				lastFileChangeAt: st.LastFileChangeAt,
				boardFingerprint: taskProgressFingerprint(task),
			})
		}
	}

	for leadThreadID, bees := range w.runningBeesSnapshot() {
		for _, bee := range bees {
			if bee.ReadOnly || bee.ThreadID == "" {
				continue
			}
			threadID := bee.ThreadID
			if w.isIgnored(threadID) {
				continue
			}
			st, err := w.sessions.SessionState(ctx, threadID)
			if err != nil {
				continue
			}
			if st.AwaitingApproval || st.AwaitingInput {
				continue
			}
			lastFileChangeAt := bee.LastFileChangeAt
			if st.LastFileChangeAt.After(lastFileChangeAt) {
				lastFileChangeAt = st.LastFileChangeAt
			}
			cands = append(cands, noProgressCandidate{
				subjectType:      attention.SubjectBee,
				subjectID:        threadID,
				threadID:         leadThreadID,
				projectID:        bee.ProjectID,
				title:            bee.Task,
				lastFileChangeAt: lastFileChangeAt,
				startedAt:        bee.StartedAt,
			})
		}
	}
	return cands
}

// withinStalledAlert 判断会话是否已进入卡住提醒范围；是则交给 thread_stalled，避免同一段停滞报两次。
func (w *attentionWiring) withinStalledAlert(cfg config.Config, threadID string, now time.Time) bool {
	if !cfg.Notifications.StalledAlertEnabled() {
		return false
	}
	last, ok := w.alerts.LastActivity(threadID)
	if !ok {
		return false
	}
	return now.Sub(last) >= cfg.Notifications.StalledAlertAfter()
}

// runningBeesSnapshot 取运行中的 bee，按 lead 会话 ID 分组；未接入 swarm 时返回空。
func (w *attentionWiring) runningBeesSnapshot() map[string][]swarm.Bee {
	if w.runningBees != nil {
		return w.runningBees()
	}
	if w.swarm == nil {
		return nil
	}
	return w.swarm.RunningBees()
}

// applyNoProgressActions 执行状态机动作：消解或写事件，写成功后把对象标记为已上报。
// 被每日上限截停时保持未上报，下一轮再试。
func (w *attentionWiring) applyNoProgressActions(actions []noProgressAction, threshold time.Duration) {
	for _, action := range actions {
		if action.resolve {
			w.resolveSubject(action.cand.subjectType, action.cand.subjectID, attention.KindThreadNoProgress)
			continue
		}
		if !w.record(noProgressInput(action.cand, action.enteredAt, threshold)) {
			continue
		}
		key := noProgressKey(action.cand.subjectType, action.cand.subjectID)
		w.mu.Lock()
		if st, ok := w.noProgress[key]; ok {
			st.reported = true
		}
		w.mu.Unlock()
	}
}
