package kanban

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/store"
)

// repairTestPlanJSON 是补交成功时模型应输出的合法计划。
const repairTestPlanJSON = `{"summary":"补交成功","tasks":[{"title":"任务一"}]}`

// repairSequenceDriver 按调用次序返回预设会话状态，并记录追加消息与新建会话。
type repairSequenceDriver struct {
	mu        sync.Mutex
	states    []SessionState
	idx       int
	starts    []SessionRequest
	followUps []string
}

func newRepairSequenceDriver(states ...SessionState) *repairSequenceDriver {
	return &repairSequenceDriver{states: states}
}

func (d *repairSequenceDriver) StartSession(ctx context.Context, req SessionRequest) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.starts = append(d.starts, req)
	return "thread-repair-plan", nil
}

func (d *repairSequenceDriver) SessionState(ctx context.Context, threadID string) (SessionState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.states) == 0 {
		return SessionState{}, nil
	}
	if d.idx >= len(d.states) {
		// 序列用完后保持最后一个状态，方便模拟「一直输出非法文本」。
		return d.states[len(d.states)-1], nil
	}
	st := d.states[d.idx]
	d.idx++
	return st, nil
}

func (d *repairSequenceDriver) SendFollowUp(ctx context.Context, threadID, text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.followUps = append(d.followUps, text)
	return nil
}

func (d *repairSequenceDriver) followUpSnapshot() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.followUps...)
}

// plainSequenceDriver 只实现 SessionDriver，用来验证驱动不支持 SessionMessenger 时不补交。
type plainSequenceDriver struct {
	inner *repairSequenceDriver
}

func (d *plainSequenceDriver) StartSession(ctx context.Context, req SessionRequest) (string, error) {
	return d.inner.StartSession(ctx, req)
}

func (d *plainSequenceDriver) SessionState(ctx context.Context, threadID string) (SessionState, error) {
	return d.inner.SessionState(ctx, threadID)
}

// TestPlanWaitsForNewTurnWhenReusedSessionBusy：复用会话追加时正在运行，第一次停止的文本是任务书，
// 应等下一回合再解析，而不是直接判失败或发补交提示。
func TestPlanWaitsForNewTurnWhenReusedSessionBusy(t *testing.T) {
	driver := newRepairSequenceDriver(
		// 追加提示词时会话仍在运行，文本只是流式输出到一半。
		SessionState{Running: true, LastAssistantText: "任务书开头"},
		// 总管回合结束，停止文本是任务书而不是计划。
		SessionState{LastAssistantText: "任务书：请规划这个目标"},
		SessionState{LastAssistantText: "任务书：请规划这个目标"},
		SessionState{LastAssistantText: "任务书：请规划这个目标"},
		// 规划器重新开跑并输出合法计划。
		SessionState{Running: true},
		SessionState{LastAssistantText: repairTestPlanJSON},
	)

	threadID := "thread-plan-busy"
	planner := NewAgentPlanner(driver, &mockProjects{dir: t.TempDir()})
	planner.PollInterval = time.Millisecond
	planner.ReplanStartTimeout = 500 * time.Millisecond

	goal := store.Goal{ID: "goal-repair-1", ProjectID: "proj-1", Title: "目标", Prompt: "做一个功能", PlanThreadID: &threadID}
	res, err := planner.Plan(context.Background(), goal, "")
	if err != nil {
		t.Fatalf("等待新回合后应规划成功: %v", err)
	}
	if res.ThreadID != threadID {
		t.Fatalf("期望复用会话 %s，实际 %s", threadID, res.ThreadID)
	}
	if len(res.Tasks) != 1 {
		t.Fatalf("期望 1 个任务，实际 %+v", res.Tasks)
	}

	followUps := driver.followUpSnapshot()
	if len(followUps) != 1 {
		t.Fatalf("只应发送最初一次规划提示词，实际 %d 次: %+v", len(followUps), followUps)
	}
	if strings.Contains(followUps[0], "无法解析为计划") {
		t.Fatalf("等待到新回合时不应发送补交提示: %s", followUps[0])
	}
}

// TestPlanRepairsInvalidPlanOutput：新会话第一次输出非法，补交提示后输出合法计划，Plan 应成功。
func TestPlanRepairsInvalidPlanOutput(t *testing.T) {
	driver := newRepairSequenceDriver(
		SessionState{LastAssistantText: "这不是 JSON"},
		SessionState{LastAssistantText: repairTestPlanJSON},
	)
	planner := NewAgentPlanner(driver, &mockProjects{dir: t.TempDir()})
	planner.PollInterval = time.Millisecond
	planner.ReplanStartTimeout = 50 * time.Millisecond

	res, err := planner.Plan(context.Background(), store.Goal{
		ID: "goal-repair-2", ProjectID: "proj-1", Title: "目标", Prompt: "做一个功能",
	}, "")
	if err != nil {
		t.Fatalf("补交后应规划成功: %v", err)
	}
	if len(res.Tasks) != 1 {
		t.Fatalf("期望 1 个任务，实际 %+v", res.Tasks)
	}

	followUps := driver.followUpSnapshot()
	if len(followUps) != 1 {
		t.Fatalf("期望发送 1 次补交提示，实际 %d 次: %+v", len(followUps), followUps)
	}
	if !strings.Contains(followUps[0], "无法解析为计划") || !strings.Contains(followUps[0], "请只按原格式重新输出") {
		t.Fatalf("补交提示应说明无法解析并要求按原格式重出: %s", followUps[0])
	}
}

// TestPlanGivesUpAfterTwoRepairs：连续非法输出时补交 2 次后返回原解析错误。
func TestPlanGivesUpAfterTwoRepairs(t *testing.T) {
	driver := newRepairSequenceDriver(SessionState{LastAssistantText: "始终不是 JSON"})
	planner := NewAgentPlanner(driver, &mockProjects{dir: t.TempDir()})
	planner.PollInterval = time.Millisecond
	planner.ReplanStartTimeout = 5 * time.Millisecond

	_, err := planner.Plan(context.Background(), store.Goal{
		ID: "goal-repair-3", ProjectID: "proj-1", Title: "目标", Prompt: "做一个功能",
	}, "")
	if err == nil {
		t.Fatal("连续非法输出应返回错误")
	}
	if !strings.Contains(err.Error(), "no valid JSON object") {
		t.Fatalf("应返回原解析错误，实际: %v", err)
	}

	followUps := driver.followUpSnapshot()
	if len(followUps) != 2 {
		t.Fatalf("期望补交 2 次，实际 %d 次: %+v", len(followUps), followUps)
	}
	for _, text := range followUps {
		if !strings.Contains(text, "无法解析为计划") {
			t.Fatalf("补交提示内容不正确: %s", text)
		}
	}
}

// TestPlanFailsWithoutMessengerOnInvalidOutput：驱动不支持 SessionMessenger 时，非法输出直接返回错误。
func TestPlanFailsWithoutMessengerOnInvalidOutput(t *testing.T) {
	inner := newRepairSequenceDriver(SessionState{LastAssistantText: "这不是 JSON"})
	planner := NewAgentPlanner(&plainSequenceDriver{inner: inner}, &mockProjects{dir: t.TempDir()})
	planner.PollInterval = time.Millisecond
	planner.ReplanStartTimeout = 5 * time.Millisecond

	_, err := planner.Plan(context.Background(), store.Goal{
		ID: "goal-repair-4", ProjectID: "proj-1", Title: "目标", Prompt: "做一个功能",
	}, "")
	if err == nil {
		t.Fatal("驱动不支持补交时应直接返回解析错误")
	}
	if !strings.Contains(err.Error(), "no valid JSON object") {
		t.Fatalf("应返回原解析错误，实际: %v", err)
	}
	if followUps := inner.followUpSnapshot(); len(followUps) != 0 {
		t.Fatalf("不支持 SessionMessenger 时不应追加消息，实际: %+v", followUps)
	}
}
