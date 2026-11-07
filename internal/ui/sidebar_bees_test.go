package mcodeui

import (
	"testing"
	"time"
)

// TestGroupThreadsByParent_ParentVisibleAndInvisible 验证父会话在列表中存在（可见）与不存在（不可见）时的分组表现。
func TestGroupThreadsByParent_ParentVisibleAndInvisible(t *testing.T) {
	now := time.Now()

	threads := []Thread{
		{ID: "lead-1", Title: "主会话 1", UpdatedAt: now.Add(-time.Minute)},
		{ID: "lead-2", Title: "主会话 2", UpdatedAt: now.Add(-2 * time.Minute)},
		// 属于 lead-1 的 bee 会话（父会话在列表中）
		{ID: "bee-1", Title: "Aspen: 子任务 1", ParentThreadID: "lead-1", IsHelper: true, UpdatedAt: now.Add(-30 * time.Second)},
		{ID: "bee-2", Title: "Birch: 子任务 2", ParentThreadID: "lead-1", IsHelper: true, UpdatedAt: now.Add(-10 * time.Second)},
		// 属于不存在父会话的孤儿 bee（父会话不在列表中）
		{ID: "bee-orphan", Title: "Cove: 孤儿任务", ParentThreadID: "missing-lead", IsHelper: true, UpdatedAt: now},
		// 普通独立会话（IsHelper 为 false，即使误填 ParentThreadID 也应保留在顶层）
		{ID: "plain-th", Title: "普通独立会话", ParentThreadID: "lead-1", IsHelper: false, UpdatedAt: now.Add(-5 * time.Minute)},
	}

	topLevel, beesByParent := GroupThreadsByParent(threads)

	// 1. 验证顶层会话数量及包含项
	// 应包含: lead-1, lead-2, bee-orphan, plain-th（共 4 个）
	if len(topLevel) != 4 {
		t.Fatalf("topLevel 长度期望 4，实际 %d: %+v", len(topLevel), topLevel)
	}

	topIDs := make(map[string]bool)
	for _, th := range topLevel {
		topIDs[th.ID] = true
	}
	for _, expectedID := range []string{"lead-1", "lead-2", "bee-orphan", "plain-th"} {
		if !topIDs[expectedID] {
			t.Errorf("topLevel 缺少预期的会话: %s", expectedID)
		}
	}
	if topIDs["bee-1"] || topIDs["bee-2"] {
		t.Errorf("找到父会话的 bee-1 或 bee-2 不应出现在 topLevel 中")
	}

	// 2. 验证 beesByParent
	lead1Bees, ok := beesByParent["lead-1"]
	if !ok || len(lead1Bees) != 2 {
		t.Fatalf("beesByParent[lead-1] 期望包含 2 个 bee，实际: %v", lead1Bees)
	}

	// 缺失父会话的 missing-lead 不应有 entry
	if orphanList, ok := beesByParent["missing-lead"]; ok && len(orphanList) > 0 {
		t.Errorf("missing-lead 不应在 beesByParent 中存在，实际: %v", orphanList)
	}
}

// TestOrderSwarmBees 验证 bee 会话排序逻辑：运行中优先，同状态下更新时间最新优先。
func TestOrderSwarmBees(t *testing.T) {
	t0 := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)

	bees := []Thread{
		{ID: "h-idle-old", Title: "Idle Old", IsRunning: false, UpdatedAt: t0.Add(-10 * time.Minute)},
		{ID: "h-running-old", Title: "Running Old", IsRunning: true, UpdatedAt: t0.Add(-5 * time.Minute)},
		{ID: "h-idle-new", Title: "Idle New", IsRunning: false, UpdatedAt: t0.Add(-1 * time.Minute)},
		{ID: "h-running-new", Title: "Running New", IsRunning: true, UpdatedAt: t0},
	}

	ordered := OrderSwarmBees(bees)
	if len(ordered) != 4 {
		t.Fatalf("ordered 长度期望 4，实际: %d", len(ordered))
	}

	// 预期顺序:
	// 1. h-running-new (运行中，最新)
	// 2. h-running-old (运行中，较旧)
	// 3. h-idle-new    (空闲，最新)
	// 4. h-idle-old    (空闲，较旧)
	expectedOrder := []string{"h-running-new", "h-running-old", "h-idle-new", "h-idle-old"}
	for i, wantID := range expectedOrder {
		if ordered[i].ID != wantID {
			t.Errorf("第 %d 个 bee 期望 %s，实际 %s", i, wantID, ordered[i].ID)
		}
	}
}

// TestBeeFoldSummary 验证折叠统计：bee 总数、是否有正在运行的 bee、折叠文案。
func TestBeeFoldSummary(t *testing.T) {
	// 1. 多个 bee 且有运行中的 bee
	beesWithRunning := []Thread{
		{ID: "h1", Title: "H1", IsRunning: false},
		{ID: "h2", Title: "H2", IsRunning: true},
		{ID: "h3", Title: "H3", IsRunning: false},
	}
	count, hasRunning, label := BeeFoldSummary(beesWithRunning)
	if count != 3 {
		t.Errorf("count 期望 3，实际 %d", count)
	}
	if !hasRunning {
		t.Errorf("hasRunning 期望 true，实际 false")
	}
	if label != "3 个子代理" {
		t.Errorf("label 期望 '3 个子代理'，实际 %q", label)
	}

	// 2. 1 个 bee 且无运行中
	beesSingleIdle := []Thread{
		{ID: "h1", Title: "H1", IsRunning: false},
	}
	count1, hasRunning1, label1 := BeeFoldSummary(beesSingleIdle)
	if count1 != 1 || hasRunning1 || label1 != "1 个子代理" {
		t.Errorf("单 bee 结果异常: count=%d, hasRunning=%v, label=%q", count1, hasRunning1, label1)
	}

	// 3. 空列表
	count0, hasRunning0, label0 := BeeFoldSummary(nil)
	if count0 != 0 || hasRunning0 || label0 != "0 个子代理" {
		t.Errorf("空列表结果异常: count=%d, hasRunning=%v, label=%q", count0, hasRunning0, label0)
	}
}

// TestBeeFoldState 验证折叠状态的默认展开、切换与重置。
func TestBeeFoldState(t *testing.T) {
	ResetBeeFolded()
	defer ResetBeeFolded()

	parentID := "lead-fold-test"

	// 默认状态应为展开 (false)
	if IsBeeFolded(parentID) {
		t.Errorf("默认状态期望展开 (false)，实际为折叠 (true)")
	}

	// 切换为折叠
	ToggleBeeFold(parentID)
	if !IsBeeFolded(parentID) {
		t.Errorf("切换后状态期望折叠 (true)，实际为展开 (false)")
	}

	// 再次切换恢复展开
	ToggleBeeFold(parentID)
	if IsBeeFolded(parentID) {
		t.Errorf("再次切换后期望恢复展开 (false)")
	}

	// 手动设为折叠
	SetBeeFolded(parentID, true)
	if !IsBeeFolded(parentID) {
		t.Errorf("SetBeeFolded(true) 后期望为折叠 (true)")
	}

	// 重置
	ResetBeeFolded()
	if IsBeeFolded(parentID) {
		t.Errorf("重置后期望恢复默认展开 (false)")
	}
}

// TestExtractBeeName 验证从会话标题中提取 bee 名字。
func TestExtractBeeName(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"Aspen: 子任务 1", "Aspen"},
		{"Birch：数据分析", "Birch"},
		{"  Cove : 无人区  ", "Cove"},
		{"Zephyr", "Zephyr"},
		{"", ""},
	}

	for _, tc := range cases {
		got := extractBeeName(tc.input)
		if got != tc.want {
			t.Errorf("extractBeeName(%q) = %q, 期望 %q", tc.input, got, tc.want)
		}
	}
}

// TestMapBeeStatus 验证状态映射逻辑：running、waiting_review、completed、errored、idle 以及未知状态。
func TestMapBeeStatus(t *testing.T) {
	cases := []struct {
		name      string
		status    string
		isRunning bool
		want      string
	}{
		// 1. running 状态
		{"explicit running", "running", false, "running"},
		{"uppercase RUNNING", "RUNNING", false, "running"},
		{"running with space", "  running  ", false, "running"},
		{"fallback running when empty", "", true, "running"},
		{"fallback running when unknown", "non_existent_status", true, "running"},

		// 2. completed 状态
		{"explicit completed", "completed", false, "completed"},
		{"uppercase COMPLETED", "COMPLETED", false, "completed"},
		{"titlecase Completed", "Completed", false, "completed"},

		// 3. failed 状态归一到 errored
		{"explicit failed", "failed", false, "errored"},
		{"uppercase FAILED", "FAILED", false, "errored"},
		{"titlecase Failed", "Failed", false, "errored"},

		// 4. stopped 状态归一到 idle
		{"explicit stopped", "stopped", false, "idle"},
		{"uppercase STOPPED", "STOPPED", false, "idle"},
		{"titlecase Stopped", "Stopped", false, "idle"},

		// 5. 待审查状态
		{"explicit waiting_review", "waiting_review", false, "waiting_review"},
		{"needs_human", "needs_human", false, "waiting_review"},

		// 6. 未知状态归一到 idle
		{"unknown string", "unknown", false, "idle"},
		{"idle string", "idle", false, "idle"},
		{"empty string not running", "", false, "idle"},
		{"arbitrary unknown string", "waiting_for_approval", false, "idle"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MapBeeStatus(tc.status, tc.isRunning)
			if got != tc.want {
				t.Errorf("MapBeeStatus(%q, %v) = %q, 期望 %q", tc.status, tc.isRunning, got, tc.want)
			}
		})
	}
}

// TestMergeSwarmStateBees 验证 SwarmState 兜底合并、去重及以列表中线程为准的逻辑。
func TestMergeSwarmStateBees(t *testing.T) {
	now := time.Now()
	leadID := "lead-main"
	projID := "proj-main"

	// 准备列表中的 bee 线程（已存在于列表中）
	listBees := []Thread{
		{
			ID:             "bee-1",
			ProjectID:      projID,
			ParentThreadID: leadID,
			Title:          "Aspen: 列表中已有标题",
			IsHelper:       true,
			IsRunning:      false,
			UpdatedAt:      now.Add(-2 * time.Minute),
		},
	}

	// 准备 SwarmState 中的 bees：
	// bee-1: 列表中已有，测试去重且以列表中的线程为准（列表标题、属性优先）
	// bee-2: 刚派出（不在列表中，运行中），测试兜底合并与运行中状态
	// bee-3: 刚派出（不在列表中，已完成），测试兜底合并
	swarmBees := []SwarmBee{
		{
			ThreadID:  "bee-1",
			Name:      "Aspen",
			Task:      "来自 SwarmState 的任务描述",
			Status:    "running", // 标题等属性以列表为准，运行标记以 SwarmState 为准
			StartedAt: now.Add(-5 * time.Minute),
		},
		{
			ThreadID:  "bee-2",
			Name:      "Birch",
			Task:      "子任务 2",
			Status:    "running",
			StartedAt: now.Add(-30 * time.Second),
		},
		{
			ThreadID:  "bee-3",
			Name:      "Jesse",
			Task:      "子任务 3",
			Status:    "completed",
			StartedAt: now.Add(-10 * time.Second),
		},
	}

	merged := MergeSwarmStateBees(listBees, swarmBees, leadID, projID)

	// 验证数量：去重后应为 3 个
	if len(merged) != 3 {
		t.Fatalf("合并后长度期望 3，实际 %d: %+v", len(merged), merged)
	}

	// 验证以列表中的线程为准：bee-1 的 Title 应该是列表中的 "Aspen: 列表中已有标题"
	var found1 *Thread
	for i := range merged {
		if merged[i].ID == "bee-1" {
			found1 = &merged[i]
			break
		}
	}
	if found1 == nil {
		t.Fatalf("缺少 bee-1")
	}
	if found1.Title != "Aspen: 列表中已有标题" {
		t.Errorf("bee-1 应以列表中的线程为准，实际 Title: %q", found1.Title)
	}

	// 验证新补充的 bee-2
	var found2 *Thread
	for i := range merged {
		if merged[i].ID == "bee-2" {
			found2 = &merged[i]
			break
		}
	}
	if found2 == nil {
		t.Fatalf("缺少补充的 bee-2")
	}
	if found2.Title != "Birch" || !found2.IsRunning || found2.ParentThreadID != leadID || found2.ProjectID != projID {
		t.Errorf("bee-2 补充字段不符合预期: %+v", found2)
	}

	// 验证新补充的 bee-3：已完成的 bee 必须带有正确的 LastStatus，防止切走会话后状态回退丢失
	var found3 *Thread
	for i := range merged {
		if merged[i].ID == "bee-3" {
			found3 = &merged[i]
			break
		}
	}
	if found3 == nil {
		t.Fatalf("缺少补充的 bee-3")
	}
	if found3.LastStatus != "completed" {
		t.Errorf("bee-3 补充后应带有 LastStatus: 'completed'，实际 %q", found3.LastStatus)
	}

	// 验证排序：运行中优先（bee-2 在运行中，排在已停止/完成的 bee 前面）
	if !merged[0].IsRunning {
		t.Errorf("第一个 bee 应当是运行中的 bee，实际: %+v", merged[0])
	}

	// 测试空列表情况
	emptyMerged := MergeSwarmStateBees(nil, swarmBees, leadID, projID)
	if len(emptyMerged) != 3 {
		t.Errorf("空列表兜底合并期望 3，实际 %d", len(emptyMerged))
	}

	// 测试空 SwarmState 情况
	noneMerged := MergeSwarmStateBees(listBees, nil, leadID, projID)
	if len(noneMerged) != 1 || noneMerged[0].ID != "bee-1" {
		t.Errorf("空 SwarmState 合并结果异常: %+v", noneMerged)
	}
}

// TestFilterIdleSwarmBees 验证隐藏空闲子代理的过滤规则：运行中、等待回答、当前查看的保留，其余剔除。
func TestFilterIdleSwarmBees(t *testing.T) {
	bees := []Thread{
		{ID: "running", Title: "Aspen: 运行中", IsRunning: true},
		{ID: "done", Title: "Birch: 已完成", LastStatus: "completed"},
		{ID: "pending", Title: "Cove: 等待回答", HasPendingRequest: true},
		{ID: "failed", Title: "Grace: 失败", LastStatus: "failed"},
		{ID: "stopped", Title: "Linus: 停止", LastStatus: "stopped"},
	}

	tests := []struct {
		name    string
		bees    []Thread
		keepID  string
		wantIDs []string
		wantNil bool
	}{
		{name: "运行中保留", bees: bees[:1], wantIDs: []string{"running"}},
		{name: "已完成剔除", bees: bees[1:2], wantIDs: []string{}},
		{name: "等待回答保留", bees: bees[2:3], wantIDs: []string{"pending"}},
		{name: "keepID 命中的已完成项保留", bees: bees[1:2], keepID: "done", wantIDs: []string{"done"}},
		{name: "全部状态混合", bees: bees, keepID: "stopped", wantIDs: []string{"running", "pending", "stopped"}},
		{name: "空输入返回 nil", bees: nil, wantNil: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FilterIdleSwarmBees(tc.bees, tc.keepID)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("期望 nil，实际 %v", got)
				}
				return
			}
			if len(got) != len(tc.wantIDs) {
				t.Fatalf("期望 %d 项 %v，实际 %d 项 %+v", len(tc.wantIDs), tc.wantIDs, len(got), got)
			}
			for i, wantID := range tc.wantIDs {
				if got[i].ID != wantID {
					t.Errorf("第 %d 项期望 %s，实际 %s（应保持原顺序）", i, wantID, got[i].ID)
				}
			}
		})
	}
}
