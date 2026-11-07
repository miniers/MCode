package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/attention"
	"mcode/internal/config"
	"mcode/internal/kanban"
	"mcode/internal/store"
	"mcode/internal/swarm"
)

// fakeSessionProgress 是测试用的会话状态源，未登记的会话返回错误。
type fakeSessionProgress struct {
	states map[string]kanban.SessionState
}

func (f *fakeSessionProgress) SessionState(ctx context.Context, threadID string) (kanban.SessionState, error) {
	st, ok := f.states[threadID]
	if !ok {
		return kanban.SessionState{}, fmt.Errorf("unknown thread %s", threadID)
	}
	return st, nil
}

// newProgressTestWiring 用临时库构造接线；不启动后台 goroutine，测试直接调巡检函数。
func newProgressTestWiring(t *testing.T, sessions sessionProgressSource) *attentionWiring {
	t.Helper()
	return newProgressTestWiringWithDataDir(t, sessions, "")
}

// newProgressTestWiringWithDataDir 额外指定数据目录，供需要现读配置的巡检测试用。
func newProgressTestWiringWithDataDir(t *testing.T, sessions sessionProgressSource, dataDir string) *attentionWiring {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "progress.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	svc := attention.New(st, attention.Options{})
	wiring := newAttentionWiring(attentionWiringOptions{
		Service:  svc,
		Store:    st,
		Sessions: sessions,
		DataDir:  dataDir,
		Now:      time.Now,
	})
	t.Cleanup(func() {
		wiring.Stop()
		if err := st.Close(); err != nil {
			t.Logf("close store: %v", err)
		}
	})
	return wiring
}

// createInProgressTask 写入一条处于进行中、绑定了执行会话的任务。
func createInProgressTask(t *testing.T, wiring *attentionWiring, taskID, threadID string) {
	t.Helper()
	projectID := "proj-1"
	task := store.Task{
		ID:        taskID,
		ProjectID: &projectID,
		ThreadID:  &threadID,
		Title:     "实现看门狗",
		Status:    string(kanban.TaskStatusInProgress),
	}
	if err := wiring.store.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}
}

// 同一段停滞只产出一次记录动作；置位 reported 后不再重复产出。
func TestDiffNoProgressReportsOnce(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	threshold := 45 * time.Minute
	prev := map[string]*noProgressState{}
	cand := noProgressCandidate{
		subjectType:      attention.SubjectThread,
		subjectID:        "thread-1",
		threadID:         "thread-1",
		lastFileChangeAt: now.Add(-2 * time.Hour),
		boardFingerprint: "board-1",
	}

	actions := diffNoProgress(prev, []noProgressCandidate{cand}, now, threshold)
	if len(actions) != 1 {
		t.Fatalf("首次越过阈值应产出一个动作，实际 %d 个", len(actions))
	}
	if actions[0].resolve {
		t.Fatalf("首次越过阈值应产出记录动作，不应是消解动作")
	}
	wantEnteredAt := cand.lastFileChangeAt.Add(threshold)
	if !actions[0].enteredAt.Equal(wantEnteredAt) {
		t.Fatalf("进入时刻应为 %v，实际 %v", wantEnteredAt, actions[0].enteredAt)
	}

	prev[noProgressKey(cand.subjectType, cand.subjectID)].reported = true
	if again := diffNoProgress(prev, []noProgressCandidate{cand}, now.Add(10*time.Minute), threshold); len(again) != 0 {
		t.Fatalf("已上报后同一段停滞不应再产出动作，实际 %d 个", len(again))
	}
}

// 文件改动时间前进视为进展：消解已上报事件并清空进入时刻。
func TestDiffNoProgressResetsAfterFileChange(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	threshold := 45 * time.Minute
	cand := noProgressCandidate{
		subjectType:      attention.SubjectThread,
		subjectID:        "thread-1",
		lastFileChangeAt: now.Add(-2 * time.Hour),
		boardFingerprint: "board-1",
	}
	prev := map[string]*noProgressState{}
	diffNoProgress(prev, []noProgressCandidate{cand}, now, threshold)
	key := noProgressKey(cand.subjectType, cand.subjectID)
	prev[key].reported = true

	cand.lastFileChangeAt = now
	actions := diffNoProgress(prev, []noProgressCandidate{cand}, now, threshold)
	if len(actions) != 1 || !actions[0].resolve {
		t.Fatalf("文件改动后应产出一个消解动作，实际 %+v", actions)
	}
	st := prev[key]
	if st.reported {
		t.Fatalf("消解后不应仍标记已上报")
	}
	if !st.enteredAt.IsZero() {
		t.Fatalf("消解后应清空进入时刻，实际 %v", st.enteredAt)
	}
	if !st.lastProgressAt.Equal(now) {
		t.Fatalf("进展时刻应更新为文件改动时刻，实际 %v", st.lastProgressAt)
	}
}

// 对象从候选集中消失时消解并删除状态。
func TestDiffNoProgressResolvesWhenSubjectGone(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	threshold := 45 * time.Minute
	cand := noProgressCandidate{
		subjectType:      attention.SubjectBee,
		subjectID:        "bee-1",
		threadID:         "lead-1",
		lastFileChangeAt: now.Add(-2 * time.Hour),
	}
	prev := map[string]*noProgressState{}
	diffNoProgress(prev, []noProgressCandidate{cand}, now, threshold)
	key := noProgressKey(cand.subjectType, cand.subjectID)
	prev[key].reported = true

	actions := diffNoProgress(prev, nil, now, threshold)
	if len(actions) != 1 || !actions[0].resolve {
		t.Fatalf("对象消失时应产出一个消解动作，实际 %+v", actions)
	}
	if actions[0].cand.subjectID != "bee-1" || actions[0].cand.subjectType != attention.SubjectBee {
		t.Fatalf("消解动作应指向原对象，实际 %+v", actions[0].cand)
	}
	if _, ok := prev[key]; ok {
		t.Fatalf("对象消失后应删除状态")
	}
}

// 恢复进展后再停滞，应得到新的进入时刻并再报一次。
func TestDiffNoProgressNewStallAfterRecovery(t *testing.T) {
	start := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	threshold := 45 * time.Minute
	cand := noProgressCandidate{
		subjectType:      attention.SubjectThread,
		subjectID:        "thread-1",
		lastFileChangeAt: start,
		boardFingerprint: "board-1",
	}
	prev := map[string]*noProgressState{}

	first := diffNoProgress(prev, []noProgressCandidate{cand}, start.Add(time.Hour), threshold)
	if len(first) != 1 || first[0].resolve {
		t.Fatalf("第一段停滞应产出一个记录动作，实际 %+v", first)
	}
	key := noProgressKey(cand.subjectType, cand.subjectID)
	prev[key].reported = true

	recoveredAt := start.Add(2 * time.Hour)
	cand.lastFileChangeAt = recoveredAt
	if acts := diffNoProgress(prev, []noProgressCandidate{cand}, recoveredAt, threshold); len(acts) != 1 || !acts[0].resolve {
		t.Fatalf("恢复进展应产出消解动作，实际 %+v", acts)
	}

	second := diffNoProgress(prev, []noProgressCandidate{cand}, recoveredAt.Add(time.Hour), threshold)
	if len(second) != 1 || second[0].resolve {
		t.Fatalf("恢复后再停滞应再产出记录动作，实际 %+v", second)
	}
	wantEnteredAt := recoveredAt.Add(threshold)
	if !second[0].enteredAt.Equal(wantEnteredAt) {
		t.Fatalf("新一段停滞的进入时刻应为 %v，实际 %v", wantEnteredAt, second[0].enteredAt)
	}
	if second[0].enteredAt.Equal(first[0].enteredAt) {
		t.Fatalf("新一段停滞不应沿用上一段的进入时刻")
	}
}

// 关闭无进展看门狗后，下一轮巡检应消解已上报对象并清空状态表。
func TestCheckNoProgressOffResolvesReported(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.Defaults()
	cfg.Orchestration.Steward.NoProgressOff = true
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	sessions := &fakeSessionProgress{states: map[string]kanban.SessionState{}}
	wiring := newProgressTestWiringWithDataDir(t, sessions, dataDir)

	key := noProgressKey(attention.SubjectThread, "thread-1")
	wiring.noProgress[key] = &noProgressState{
		subjectType: attention.SubjectThread,
		subjectID:   "thread-1",
		reported:    true,
	}

	wiring.checkNoProgress()

	wiring.mu.Lock()
	remaining := len(wiring.noProgress)
	wiring.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("关闭看门狗后应清空状态表，实际还剩 %d 条", remaining)
	}
}

// 事件正文的分钟数取实际阈值。
func TestNoProgressDetailIncludesThreshold(t *testing.T) {
	cand := noProgressCandidate{title: "实现看门狗"}
	if detail := noProgressDetail(cand, 45*time.Minute); !strings.Contains(detail, "45 分钟") {
		t.Fatalf("正文应包含 45 分钟，实际 %q", detail)
	}
	if detail := noProgressDetail(cand, 12*time.Minute); !strings.Contains(detail, "12 分钟") {
		t.Fatalf("正文应包含 12 分钟，实际 %q", detail)
	}
	if detail := noProgressDetail(cand, 45*time.Minute); !strings.Contains(detail, "没有文件改动") || !strings.Contains(detail, "没有看板状态变化") {
		t.Fatalf("正文应说明无文件改动且无看板变化，实际 %q", detail)
	}
}

// 看板指纹忽略 UpdatedAt 与 Revision，只跟随有意义的字段。
func TestTaskProgressFingerprintIgnoresUpdatedAtAndRevision(t *testing.T) {
	status := "review"
	threadID := "thread-1"
	base := store.Task{
		ID:           "task-1",
		Status:       "in_progress",
		ThreadID:     &threadID,
		ReviewStatus: &status,
		Title:        "实现看门狗",
		Notes:        "先做状态机",
		Revision:     1,
		UpdatedAt:    time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC),
	}
	touched := base
	touched.Revision = 7
	touched.UpdatedAt = base.UpdatedAt.Add(time.Hour)
	if taskProgressFingerprint(base) != taskProgressFingerprint(touched) {
		t.Fatalf("UpdatedAt 与 Revision 变化不应改变指纹")
	}

	changed := base
	changed.Notes = "先做筛选规则"
	if taskProgressFingerprint(base) == taskProgressFingerprint(changed) {
		t.Fatalf("Notes 变化应改变指纹")
	}
}

// 等用户回答的会话不进入候选。
func TestNoProgressCandidatesSkipAwaitingInput(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	sessions := &fakeSessionProgress{states: map[string]kanban.SessionState{
		"thread-1": {Running: true, AwaitingInput: true},
	}}
	wiring := newProgressTestWiring(t, sessions)
	createInProgressTask(t, wiring, "task-1", "thread-1")

	cands := wiring.noProgressCandidates(context.Background(), config.Defaults(), now, 45*time.Minute)
	if len(cands) != 0 {
		t.Fatalf("等回答的会话不应进入候选，实际 %+v", cands)
	}
}

// 已进入卡住提醒范围的静默会话交给 thread_stalled，不进入候选。
func TestNoProgressCandidatesSkipSilentSession(t *testing.T) {
	clock := time.Now()
	sessions := &fakeSessionProgress{states: map[string]kanban.SessionState{
		"thread-1": {Running: true, LastFileChangeAt: clock.Add(-2 * time.Hour)},
	}}
	wiring := newProgressTestWiring(t, sessions)
	createInProgressTask(t, wiring, "task-1", "thread-1")
	wiring.alerts.Observe("thread-1", "attention.changed", nil)

	// 距最后一次会话事件已超过默认 10 分钟，属于 thread_stalled 的范围。
	later := clock.Add(time.Hour)
	cands := wiring.noProgressCandidates(context.Background(), config.Defaults(), later, 45*time.Minute)
	if len(cands) != 0 {
		t.Fatalf("静默会话应交给卡住提醒，实际候选 %+v", cands)
	}
}

// bee 候选的 subjectID 是 bee 会话、threadID 是 lead 会话。
func TestNoProgressCandidatesIncludeBeeWithLeadThread(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	sessions := &fakeSessionProgress{states: map[string]kanban.SessionState{
		"bee-1": {Running: true, LastFileChangeAt: now.Add(-time.Hour)},
	}}
	wiring := newProgressTestWiring(t, sessions)
	wiring.runningBees = func() map[string][]swarm.Bee {
		return map[string][]swarm.Bee{
			"lead-1": {
				{ThreadID: "bee-1", Task: "实现筛选规则", StartedAt: now.Add(-3 * time.Hour), ProjectID: "proj-1"},
			},
		}
	}

	cands := wiring.noProgressCandidates(context.Background(), config.Defaults(), now, 45*time.Minute)
	if len(cands) != 1 {
		t.Fatalf("应产出一个 bee 候选，实际 %+v", cands)
	}
	cand := cands[0]
	if cand.subjectType != attention.SubjectBee {
		t.Fatalf("对象类型应为 bee，实际 %v", cand.subjectType)
	}
	if cand.subjectID != "bee-1" {
		t.Fatalf("对象 ID 应为 bee 会话，实际 %q", cand.subjectID)
	}
	if cand.threadID != "lead-1" {
		t.Fatalf("跳转会话应为 lead 会话，实际 %q", cand.threadID)
	}
	if cand.title != "实现筛选规则" {
		t.Fatalf("标题应取 bee 的任务描述，实际 %q", cand.title)
	}
}
