package kanban

import (
	"testing"
	"time"

	"mcode/internal/store"
)

func TestDeriveDisplayColumnMapping(t *testing.T) {
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		task      store.Task
		rt        RuntimeState
		wantCol   DisplayColumn
		wantFlags []string
	}{
		{
			name:      "todo 默认映射至 todo 列",
			task:      store.Task{ID: "t1", Status: string(TaskStatusTodo)},
			rt:        RuntimeState{},
			wantCol:   DisplayColumnTodo,
			wantFlags: []string{},
		},
		{
			name:      "in_progress 默认映射至 in_progress 列",
			task:      store.Task{ID: "t2", Status: string(TaskStatusInProgress)},
			rt:        RuntimeState{},
			wantCol:   DisplayColumnInProgress,
			wantFlags: []string{},
		},
		{
			name:      "needs_approval 映射至 needs_you 列并附带 awaiting_approval 关注标志",
			task:      store.Task{ID: "t3", Status: string(TaskStatusNeedsApproval)},
			rt:        RuntimeState{},
			wantCol:   DisplayColumnNeedsYou,
			wantFlags: []string{AttentionFlagAwaitingApproval},
		},
		{
			name:      "review 映射至 review 列并附带 needs_review 关注标志",
			task:      store.Task{ID: "t4", Status: string(TaskStatusReview)},
			rt:        RuntimeState{},
			wantCol:   DisplayColumnReview,
			wantFlags: []string{AttentionFlagNeedsReview},
		},
		{
			name:      "done 映射至 done 列",
			task:      store.Task{ID: "t5", Status: string(TaskStatusDone)},
			rt:        RuntimeState{},
			wantCol:   DisplayColumnDone,
			wantFlags: []string{},
		},
		{
			name:      "rejected 映射至 needs_you 列并附带 failed 标志",
			task:      store.Task{ID: "t6", Status: string(TaskStatusRejected)},
			rt:        RuntimeState{},
			wantCol:   DisplayColumnNeedsYou,
			wantFlags: []string{AttentionFlagFailed},
		},
		{
			name:      "线程失败时 todo 提升至 needs_you",
			task:      store.Task{ID: "t7", Status: string(TaskStatusTodo)},
			rt:        RuntimeState{ThreadFailed: true},
			wantCol:   DisplayColumnNeedsYou,
			wantFlags: []string{AttentionFlagFailed},
		},
		{
			name:      "等待用户输入时 in_progress 提升至 needs_you",
			task:      store.Task{ID: "t8", Status: string(TaskStatusInProgress)},
			rt:        RuntimeState{AwaitingInput: true},
			wantCol:   DisplayColumnNeedsYou,
			wantFlags: []string{AttentionFlagAwaitingInput},
		},
		{
			name:      "等待审批时 in_progress 提升至 needs_you",
			task:      store.Task{ID: "t9", Status: string(TaskStatusInProgress)},
			rt:        RuntimeState{AwaitingApproval: true},
			wantCol:   DisplayColumnNeedsYou,
			wantFlags: []string{AttentionFlagAwaitingApproval},
		},
		{
			name:      "严重卡死 IsHardStuck 提升至 needs_you",
			task:      store.Task{ID: "t10", Status: string(TaskStatusInProgress)},
			rt:        RuntimeState{IsHardStuck: true},
			wantCol:   DisplayColumnNeedsYou,
			wantFlags: []string{AttentionFlagStuck},
		},
		{
			name:      "警告卡死 IsWarnStuck 停留在 in_progress 列但打上 stuck 标志",
			task:      store.Task{ID: "t11", Status: string(TaskStatusInProgress)},
			rt:        RuntimeState{IsWarnStuck: true},
			wantCol:   DisplayColumnInProgress,
			wantFlags: []string{AttentionFlagStuck},
		},
		{
			name:      "基于心跳时间推导严重卡死（45分钟无活动）提升至 needs_you",
			task:      store.Task{ID: "t12", Status: string(TaskStatusInProgress)},
			rt:        RuntimeState{Heartbeat: now.Add(-45 * time.Minute), Now: now},
			wantCol:   DisplayColumnNeedsYou,
			wantFlags: []string{AttentionFlagStuck},
		},
		{
			name:      "基于心跳时间推导告警卡死（25分钟无活动）留在 in_progress",
			task:      store.Task{ID: "t13", Status: string(TaskStatusInProgress)},
			rt:        RuntimeState{Heartbeat: now.Add(-25 * time.Minute), Now: now},
			wantCol:   DisplayColumnInProgress,
			wantFlags: []string{AttentionFlagStuck},
		},
		{
			name:      "会话已死亡且有未决审批时转化为 failed 标志并提升至 needs_you",
			task:      store.Task{ID: "t14", Status: string(TaskStatusInProgress)},
			rt:        RuntimeState{SessionIsDead: true, AwaitingApproval: true},
			wantCol:   DisplayColumnNeedsYou,
			wantFlags: []string{AttentionFlagFailed},
		},
		{
			name:      "todo 任务在 IsOptimistic 为 true 时映射至 in_progress 列",
			task:      store.Task{ID: "t15", Status: string(TaskStatusTodo)},
			rt:        RuntimeState{IsOptimistic: true},
			wantCol:   DisplayColumnInProgress,
			wantFlags: []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			derived := Derive(tc.task, nil, tc.rt)
			if derived.DisplayColumn != tc.wantCol {
				t.Errorf("列映射不符: got %s, want %s", derived.DisplayColumn, tc.wantCol)
			}
			if len(derived.AttentionFlags) != len(tc.wantFlags) {
				t.Errorf("关注标志长度不符: got %+v, want %+v", derived.AttentionFlags, tc.wantFlags)
			} else {
				for i := range tc.wantFlags {
					if derived.AttentionFlags[i] != tc.wantFlags[i] {
						t.Errorf("关注标志[%d]不符: got %s, want %s", i, derived.AttentionFlags[i], tc.wantFlags[i])
					}
				}
			}
		})
	}
}

func TestDeriveBlocked(t *testing.T) {
	projA := "proj-a"
	projB := "proj-b"

	allTasks := map[string]store.Task{
		"done-1":     {ID: "done-1", ProjectID: &projA, Status: string(TaskStatusDone)},
		"done-2":     {ID: "done-2", ProjectID: &projA, Status: string(TaskStatusDone)},
		"todo-dep":   {ID: "todo-dep", ProjectID: &projA, Status: string(TaskStatusTodo)},
		"prog-dep":   {ID: "prog-dep", ProjectID: &projA, Status: string(TaskStatusInProgress)},
		"other-proj": {ID: "other-proj", ProjectID: &projB, Status: string(TaskStatusDone)},
	}

	tests := []struct {
		name        string
		task        store.Task
		wantBlocked bool
	}{
		{
			name:        "无依赖任务不阻塞",
			task:        store.Task{ID: "t1", ProjectID: &projA, DependsOn: []string{}},
			wantBlocked: false,
		},
		{
			name:        "所有依赖均已完成（done）不阻塞",
			task:        store.Task{ID: "t2", ProjectID: &projA, DependsOn: []string{"done-1", "done-2"}},
			wantBlocked: false,
		},
		{
			name:        "依赖未完成（todo）阻塞",
			task:        store.Task{ID: "t3", ProjectID: &projA, DependsOn: []string{"done-1", "todo-dep"}},
			wantBlocked: true,
		},
		{
			name:        "依赖进行中（in_progress）阻塞",
			task:        store.Task{ID: "t4", ProjectID: &projA, DependsOn: []string{"prog-dep"}},
			wantBlocked: true,
		},
		{
			name:        "缺失依赖任务阻塞",
			task:        store.Task{ID: "t5", ProjectID: &projA, DependsOn: []string{"non-existing-id"}},
			wantBlocked: true,
		},
		{
			name:        "自依赖任务阻塞",
			task:        store.Task{ID: "t6", ProjectID: &projA, DependsOn: []string{"t6"}},
			wantBlocked: true,
		},
		{
			name:        "跨项目依赖阻塞",
			task:        store.Task{ID: "t7", ProjectID: &projA, DependsOn: []string{"other-proj"}},
			wantBlocked: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			derived := Derive(tc.task, allTasks, RuntimeState{})
			if derived.Blocked != tc.wantBlocked {
				t.Errorf("Blocked 状态不符合期望: got %v, want %v", derived.Blocked, tc.wantBlocked)
			}
		})
	}
}

func TestDeriveCapabilities(t *testing.T) {
	tests := []struct {
		name        string
		task        store.Task
		all         map[string]store.Task
		rt          RuntimeState
		wantStart   bool
		wantMove    bool
		wantReview  bool
		wantApprove bool
	}{
		{
			name:        "todo 未阻塞未运行：可启动，可移动，不可评审，不可审批",
			task:        store.Task{ID: "c1", Status: string(TaskStatusTodo)},
			rt:          RuntimeState{Running: false},
			wantStart:   true,
			wantMove:    true,
			wantReview:  false,
			wantApprove: false,
		},
		{
			name:        "todo 阻塞未运行：不可启动，可移动",
			task:        store.Task{ID: "c2", Status: string(TaskStatusTodo), DependsOn: []string{"missing"}},
			all:         map[string]store.Task{},
			rt:          RuntimeState{Running: false},
			wantStart:   false,
			wantMove:    true,
			wantReview:  false,
			wantApprove: false,
		},
		{
			name:        "todo 运行中：不可启动，不可移动",
			task:        store.Task{ID: "c3", Status: string(TaskStatusTodo)},
			rt:          RuntimeState{Running: true},
			wantStart:   false,
			wantMove:    false,
			wantReview:  false,
			wantApprove: false,
		},
		{
			name:        "in_progress 任务：不可启动，不可移动",
			task:        store.Task{ID: "c4", Status: string(TaskStatusInProgress)},
			rt:          RuntimeState{Running: false},
			wantStart:   false,
			wantMove:    false,
			wantReview:  false,
			wantApprove: false,
		},
		{
			name:        "needs_approval 任务：不可启动，不可移动",
			task:        store.Task{ID: "c5", Status: string(TaskStatusNeedsApproval)},
			rt:          RuntimeState{Running: false},
			wantStart:   false,
			wantMove:    false,
			wantReview:  false,
			wantApprove: false,
		},
		{
			name:        "review 未运行：可评审，可审批，可移动，不可启动",
			task:        store.Task{ID: "c6", Status: string(TaskStatusReview)},
			rt:          RuntimeState{Running: false},
			wantStart:   false,
			wantMove:    true,
			wantReview:  true,
			wantApprove: true,
		},
		{
			name:        "review 运行中：全部能力位关闭",
			task:        store.Task{ID: "c7", Status: string(TaskStatusReview)},
			rt:          RuntimeState{Running: true},
			wantStart:   false,
			wantMove:    false,
			wantReview:  false,
			wantApprove: false,
		},
		{
			name:        "done 未运行：可移动，不可启动/评审/审批",
			task:        store.Task{ID: "c8", Status: string(TaskStatusDone)},
			rt:          RuntimeState{Running: false},
			wantStart:   false,
			wantMove:    true,
			wantReview:  false,
			wantApprove: false,
		},
		{
			name:        "rejected 未运行：可移动，不可启动/评审/审批",
			task:        store.Task{ID: "c9", Status: string(TaskStatusRejected)},
			rt:          RuntimeState{Running: false},
			wantStart:   false,
			wantMove:    true,
			wantReview:  false,
			wantApprove: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			derived := Derive(tc.task, tc.all, tc.rt)
			if derived.CanStart != tc.wantStart {
				t.Errorf("CanStart 不符: got %v, want %v", derived.CanStart, tc.wantStart)
			}
			if derived.CanMove != tc.wantMove {
				t.Errorf("CanMove 不符: got %v, want %v", derived.CanMove, tc.wantMove)
			}
			if derived.CanReview != tc.wantReview {
				t.Errorf("CanReview 不符: got %v, want %v", derived.CanReview, tc.wantReview)
			}
			if derived.CanApprove != tc.wantApprove {
				t.Errorf("CanApprove 不符: got %v, want %v", derived.CanApprove, tc.wantApprove)
			}
		})
	}
}

func TestDetectCycle(t *testing.T) {
	tests := []struct {
		name      string
		tasks     map[string]store.Task
		wantCycle bool
	}{
		{
			name:      "空任务无环",
			tasks:     map[string]store.Task{},
			wantCycle: false,
		},
		{
			name: "线性依赖链无环 A -> B -> C",
			tasks: map[string]store.Task{
				"A": {ID: "A", DependsOn: []string{"B"}},
				"B": {ID: "B", DependsOn: []string{"C"}},
				"C": {ID: "C", DependsOn: []string{}},
			},
			wantCycle: false,
		},
		{
			name: "菱形依赖无环",
			tasks: map[string]store.Task{
				"A": {ID: "A", DependsOn: []string{"B", "C"}},
				"B": {ID: "B", DependsOn: []string{"D"}},
				"C": {ID: "C", DependsOn: []string{"D"}},
				"D": {ID: "D", DependsOn: []string{}},
			},
			wantCycle: false,
		},
		{
			name: "自环 A -> A",
			tasks: map[string]store.Task{
				"A": {ID: "A", DependsOn: []string{"A"}},
			},
			wantCycle: true,
		},
		{
			name: "两节点环 A -> B -> A",
			tasks: map[string]store.Task{
				"A": {ID: "A", DependsOn: []string{"B"}},
				"B": {ID: "B", DependsOn: []string{"A"}},
			},
			wantCycle: true,
		},
		{
			name: "三节点环 A -> B -> C -> A",
			tasks: map[string]store.Task{
				"A": {ID: "A", DependsOn: []string{"B"}},
				"B": {ID: "B", DependsOn: []string{"C"}},
				"C": {ID: "C", DependsOn: []string{"A"}},
			},
			wantCycle: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cycle := DetectCycle(tc.tasks)
			hasCycle := len(cycle) > 0
			if hasCycle != tc.wantCycle {
				t.Fatalf("成环检测不符: got cycle=%v (has=%v), want=%v", cycle, hasCycle, tc.wantCycle)
			}
			if tc.wantCycle {
				// 环路径首尾应相同
				if cycle[0] != cycle[len(cycle)-1] {
					t.Errorf("环路径首尾应闭合: got %v", cycle)
				}
			}
		})
	}
}

func TestEnumsValid(t *testing.T) {
	// 验证 TaskStatus
	if !TaskStatusTodo.Valid() || !TaskStatusInProgress.Valid() || !TaskStatusNeedsApproval.Valid() ||
		!TaskStatusReview.Valid() || !TaskStatusDone.Valid() || !TaskStatusRejected.Valid() {
		t.Errorf("TaskStatus 枚举 Valid 应当为 true")
	}
	if TaskStatus("invalid_status").Valid() {
		t.Errorf("无效 TaskStatus 应当返回 false")
	}

	// 验证 Priority
	if !PriorityUrgent.Valid() || !PriorityHigh.Valid() || !PriorityMedium.Valid() ||
		!PriorityLow.Valid() || !PriorityNone.Valid() {
		t.Errorf("Priority 枚举 Valid 应当为 true")
	}
	if Priority("super_high").Valid() {
		t.Errorf("无效 Priority 应当返回 false")
	}

	// 验证 DisplayColumn
	if !DisplayColumnTodo.Valid() || !DisplayColumnInProgress.Valid() || !DisplayColumnNeedsYou.Valid() ||
		!DisplayColumnReview.Valid() || !DisplayColumnDone.Valid() {
		t.Errorf("DisplayColumn 枚举 Valid 应当为 true")
	}
	if DisplayColumn("unknown_column").Valid() {
		t.Errorf("无效 DisplayColumn 应当返回 false")
	}

	// 验证 GoalStatus
	if !GoalStatusPlanning.Valid() || !GoalStatusPlanFailed.Valid() || !GoalStatusPlanReview.Valid() ||
		!GoalStatusRunning.Valid() || !GoalStatusPaused.Valid() || !GoalStatusCompleted.Valid() || !GoalStatusCancelled.Valid() {
		t.Errorf("GoalStatus 枚举 Valid 应当为 true")
	}
	if GoalStatus("pending").Valid() {
		t.Errorf("无效 GoalStatus 应当返回 false")
	}
}

func TestDeriveValidatesFullDependencyChain(t *testing.T) {
	project := "p"
	other := "other"
	tests := []struct {
		name string
		all  map[string]store.Task
	}{
		{"projectless dependency", map[string]store.Task{
			"b": {ID: "b", Status: "done"},
		}},
		{"missing transitive dependency", map[string]store.Task{
			"b": {ID: "b", ProjectID: &project, Status: "done", DependsOn: []string{"missing"}},
		}},
		{"cross-project transitive dependency", map[string]store.Task{
			"b": {ID: "b", ProjectID: &project, Status: "done", DependsOn: []string{"c"}},
			"c": {ID: "c", ProjectID: &other, Status: "done"},
		}},
		{"cycle with root absent from map", map[string]store.Task{
			"b": {ID: "b", ProjectID: &project, Status: "done", DependsOn: []string{"a"}},
		}},
		{"cycle with stale root in map", map[string]store.Task{
			"a": {ID: "a", ProjectID: &project, Status: "todo"},
			"b": {ID: "b", ProjectID: &project, Status: "done", DependsOn: []string{"a"}},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			task := store.Task{ID: "a", ProjectID: &project, Status: "todo", DependsOn: []string{"b"}}
			got := Derive(task, tc.all, RuntimeState{})
			if !got.Blocked || got.CanStart {
				t.Fatalf("invalid dependency chain allowed start: %+v", got)
			}
		})
	}
}

func TestDeriveHeartbeatBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		age   time.Duration
		col   DisplayColumn
		stuck bool
	}{
		{-time.Second, DisplayColumnInProgress, false},
		{StuckWarnInterval - time.Nanosecond, DisplayColumnInProgress, false},
		{StuckWarnInterval, DisplayColumnInProgress, true},
		{StuckHardInterval - time.Nanosecond, DisplayColumnInProgress, true},
		{StuckHardInterval, DisplayColumnNeedsYou, true},
	} {
		t.Run(tc.age.String(), func(t *testing.T) {
			got := Derive(store.Task{Status: "in_progress"}, nil, RuntimeState{Heartbeat: now.Add(-tc.age), Now: now})
			if got.DisplayColumn != tc.col || contains(got.AttentionFlags, AttentionFlagStuck) != tc.stuck {
				t.Fatalf("heartbeat boundary mismatch: %+v", got)
			}
		})
	}
}
