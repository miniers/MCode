package kanban

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"mcode/internal/store"
)

// setupAcceptanceService 创建带 worktree 目录配置的看板服务与存储。
func setupAcceptanceService(t *testing.T, worktreeDir string) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "acceptance.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	svc := NewService(Options{
		Store:       st,
		Driver:      newMockDriver(),
		Projects:    &mockProjects{dir: t.TempDir()},
		WorktreeDir: worktreeDir,
	})
	t.Cleanup(func() { svc.Close() })
	return svc, st
}

// createAcceptanceTask 直接写库创建带验收标准的任务。
func createAcceptanceTask(t *testing.T, st *store.Store, taskID, criteria string) store.Task {
	t.Helper()
	projectID := "proj-accept"
	task := store.Task{
		ID:                 taskID,
		ProjectID:          &projectID,
		Title:              "验收命令任务",
		Status:             string(TaskStatusTodo),
		Priority:           string(PriorityNone),
		AcceptanceCriteria: criteria,
	}
	if err := st.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	return task
}

func TestParseAcceptanceCommands(t *testing.T) {
	cases := []struct {
		name     string
		criteria string
		want     []string
	}{
		{name: "空文本", criteria: "", want: []string{}},
		{name: "无命令行", criteria: "- [审阅] 读一遍改动\n- [你验] 手工点界面", want: []string{}},
		{name: "单条命令", criteria: "- [命令] `go test ./internal/kanban/`", want: []string{"go test ./internal/kanban/"}},
		{
			name:     "多条命令",
			criteria: "- [命令] `go build ./...`\n- [审阅] 看 diff\n- [命令] `go test ./...`",
			want:     []string{"go build ./...", "go test ./..."},
		},
		{name: "缺反引号跳过", criteria: "- [命令] go test ./...", want: []string{}},
		{name: "缺命令标签跳过", criteria: "- `go test ./...`", want: []string{}},
		{name: "只有单个反引号跳过", criteria: "- [命令] `go test ./...", want: []string{}},
		{name: "取第一对反引号", criteria: "- [命令] `echo a` 然后 `echo b`", want: []string{"echo a"}},
		{name: "空命令跳过", criteria: "- [命令] ``", want: []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseAcceptanceCommands(tc.criteria)
			if len(got) != len(tc.want) {
				t.Fatalf("期望 %v，实际 %v", tc.want, got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("第 %d 条期望 %q，实际 %q", i, tc.want[i], got[i])
				}
			}
		})
	}
}

func TestFormatAcceptanceCriteria(t *testing.T) {
	got := formatAcceptanceCriteria([]string{"[命令] `go test ./...`", "  ", "[审阅] 看 diff"})
	want := "- [命令] `go test ./...`\n- [审阅] 看 diff"
	if got != want {
		t.Fatalf("期望 %q，实际 %q", want, got)
	}
	if empty := formatAcceptanceCriteria(nil); empty != "" {
		t.Fatalf("空列表期望空串，实际 %q", empty)
	}
	// 拼出的文本应能被解析回命令。
	if cmds := ParseAcceptanceCommands(got); len(cmds) != 1 || cmds[0] != "go test ./..." {
		t.Fatalf("往返解析失败: %v", cmds)
	}
}

func TestTruncateAcceptanceOutput(t *testing.T) {
	short := "编译通过"
	if got := truncateAcceptanceOutput(short); got != short {
		t.Fatalf("短输出不应变化: %q", got)
	}

	// 构造超限输出，末尾是中文，确保按 rune 边界裁剪且结果合法 UTF-8。
	long := strings.Repeat("a", acceptanceOutputLimit-1) + "中文结尾"
	got := truncateAcceptanceOutput(long)
	if !strings.HasPrefix(got, acceptanceTruncatedPrefix) {
		t.Fatalf("截断时应加提示前缀: %q", got)
	}
	body := strings.TrimPrefix(got, acceptanceTruncatedPrefix)
	if !utf8.ValidString(body) {
		t.Fatalf("截断结果不是合法 UTF-8")
	}
	if len(body) > acceptanceOutputLimit {
		t.Fatalf("截断后长度 %d 超过上限 %d", len(body), acceptanceOutputLimit)
	}
	if !strings.HasSuffix(body, "中文结尾") {
		t.Fatalf("应保留末尾内容，实际 %q", body)
	}
}

func TestRunAcceptanceCommands(t *testing.T) {
	wtRoot := t.TempDir()
	svc, st := setupAcceptanceService(t, wtRoot)
	ctx := context.Background()

	taskID := "task-run-commands"
	wtPath := svc.TaskWorktreePath(taskID)
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatalf("创建 worktree 目录失败: %v", err)
	}
	createAcceptanceTask(t, st, taskID, strings.Join([]string{
		"- [命令] `echo hello`",
		"- [审阅] 读一遍改动",
		"- [命令] `printf '%s' \"$MCODE_WORKTREE_PATH\"`",
		"- [命令] `exit 3`",
	}, "\n"))

	results, err := svc.RunAcceptanceCommands(ctx, taskID)
	if err != nil {
		t.Fatalf("执行验收命令失败: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("期望 3 条命令结果，实际 %d: %+v", len(results), results)
	}
	if results[0].ExitCode != 0 || results[0].TimedOut || !strings.Contains(results[0].Output, "hello") {
		t.Fatalf("成功命令结果不正确: %+v", results[0])
	}
	if results[1].Output != wtPath {
		t.Fatalf("期望 MCODE_WORKTREE_PATH=%s，实际输出 %q", wtPath, results[1].Output)
	}
	if results[2].ExitCode != 3 || results[2].Err == "" {
		t.Fatalf("失败命令应带回退出码与错误: %+v", results[2])
	}

	// 没有命令时返回空切片。
	noCmdID := "task-no-commands"
	if err := os.MkdirAll(svc.TaskWorktreePath(noCmdID), 0o755); err != nil {
		t.Fatalf("创建 worktree 目录失败: %v", err)
	}
	createAcceptanceTask(t, st, noCmdID, "- [审阅] 只有人工标准")
	empty, err := svc.RunAcceptanceCommands(ctx, noCmdID)
	if err != nil {
		t.Fatalf("无命令时不应报错: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("无命令时应返回空切片，实际 %+v", empty)
	}

	// worktree 目录不存在时报错。
	missingID := "task-missing-worktree"
	createAcceptanceTask(t, st, missingID, "- [命令] `echo hello`")
	if _, err := svc.RunAcceptanceCommands(ctx, missingID); err == nil {
		t.Fatalf("worktree 不存在时应报错")
	}
}

func TestRunAcceptanceCommandsTimeout(t *testing.T) {
	old := acceptanceCommandTimeout
	acceptanceCommandTimeout = 80 * time.Millisecond
	t.Cleanup(func() { acceptanceCommandTimeout = old })

	svc, st := setupAcceptanceService(t, t.TempDir())
	taskID := "task-command-timeout"
	if err := os.MkdirAll(svc.TaskWorktreePath(taskID), 0o755); err != nil {
		t.Fatalf("创建 worktree 目录失败: %v", err)
	}
	createAcceptanceTask(t, st, taskID, "- [命令] `sleep 5`")

	results, err := svc.RunAcceptanceCommands(context.Background(), taskID)
	if err != nil {
		t.Fatalf("超时不应作为方法错误返回: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("期望 1 条结果，实际 %d", len(results))
	}
	if !results[0].TimedOut || results[0].ExitCode != -1 {
		t.Fatalf("期望命令超时，实际 %+v", results[0])
	}
	if results[0].Duration > 2*time.Second {
		t.Fatalf("超时未生效，耗时 %s", results[0].Duration)
	}
}

// TestRunAcceptanceCommandsTotalTimeout 用极小的合计上限验证：越界后剩余命令不再执行且标记超时。
func TestRunAcceptanceCommandsTotalTimeout(t *testing.T) {
	oldTotal := acceptanceTotalTimeout
	acceptanceTotalTimeout = 0
	t.Cleanup(func() { acceptanceTotalTimeout = oldTotal })

	svc, st := setupAcceptanceService(t, t.TempDir())
	taskID := "task-total-timeout"
	if err := os.MkdirAll(svc.TaskWorktreePath(taskID), 0o755); err != nil {
		t.Fatalf("创建 worktree 目录失败: %v", err)
	}
	marker := filepath.Join(t.TempDir(), "should-not-exist")
	createAcceptanceTask(t, st, taskID, strings.Join([]string{
		"- [命令] `touch " + marker + "`",
		"- [命令] `touch " + marker + ".2`",
	}, "\n"))

	results, err := svc.RunAcceptanceCommands(context.Background(), taskID)
	if err != nil {
		t.Fatalf("合计超时不应作为方法错误返回: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("期望 2 条结果，实际 %d", len(results))
	}
	for _, res := range results {
		if !res.TimedOut {
			t.Fatalf("合计超时后剩余命令应标记超时: %+v", res)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("合计超时后不应再执行命令")
	}
}

func TestBuildStartPromptAcceptanceCriteria(t *testing.T) {
	criteria := "- [命令] `go test ./...`\n- [审阅] 看 diff"
	prompt := buildStartPrompt(store.Task{Title: "任务", AcceptanceCriteria: criteria}, nil, "branch", false)
	if !strings.Contains(prompt, "## Acceptance Criteria") {
		t.Fatalf("缺少验收标准段: %s", prompt)
	}
	if !strings.Contains(prompt, criteria) {
		t.Fatalf("验收标准内容未拼进提示词: %s", prompt)
	}
	plain := buildStartPrompt(store.Task{Title: "任务"}, nil, "branch", false)
	if strings.Contains(plain, "## Acceptance Criteria") {
		t.Fatalf("无验收标准时不应拼该段: %s", plain)
	}
}

func TestTaskWorktreePath(t *testing.T) {
	svc, _ := setupAcceptanceService(t, "")
	if got := svc.TaskWorktreePath("task-1"); got != "" {
		t.Fatalf("未配置 WorktreeDir 时应返回空串，实际 %q", got)
	}
	root := t.TempDir()
	svc2, _ := setupAcceptanceService(t, root)
	if got := svc2.TaskWorktreePath("task-1"); got != filepath.Join(root, "task-1") {
		t.Fatalf("worktree 路径不正确: %q", got)
	}
}

// policyNumber 从经 JSON 往返的 Policy 里取数字值（json 反序列化后为 float64）。
func policyNumber(t *testing.T, policy map[string]any, key string) float64 {
	t.Helper()
	v, ok := policy[key].(float64)
	if !ok {
		t.Fatalf("policy[%s] 不是数字: %v", key, policy[key])
	}
	return v
}

func TestCreateGoalWithOptions(t *testing.T) {
	ctx := context.Background()
	planner := &mockPlanner{result: PlanResult{Summary: "s", Tasks: []PlannedTask{{Title: "任务"}}}}
	st, err := store.Open(filepath.Join(t.TempDir(), "create_goal.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	svc := NewService(Options{Store: st, Driver: newMockDriver(), Projects: &mockProjects{dir: t.TempDir()}, Planner: planner})
	t.Cleanup(func() { svc.Close() })

	goal, err := svc.CreateGoalWithOptions(ctx, CreateGoalOptions{
		ProjectID:    "proj-1",
		Prompt:       "已有规划会话的目标",
		PlanThreadID: " thread-steward ",
		Owner:        "steward",
		Policy:       map[string]any{"maxAttempts": 5},
	})
	if err != nil {
		t.Fatalf("CreateGoalWithOptions 失败: %v", err)
	}
	if goal.Owner != "steward" {
		t.Fatalf("期望 Owner steward，实际 %q", goal.Owner)
	}
	if goal.PlanThreadID == nil || *goal.PlanThreadID != "thread-steward" {
		t.Fatalf("期望 PlanThreadID 已归一化写入，实际 %v", goal.PlanThreadID)
	}
	if policyNumber(t, goal.Policy, "maxAttempts") != 5 {
		t.Fatalf("Policy 覆盖失败: %+v", goal.Policy)
	}
	if policyNumber(t, goal.Policy, "maxConcurrency") != 1 || goal.Policy["previewPlan"] != true ||
		goal.Policy["autoAccept"] != false {
		t.Fatalf("未覆盖的默认 Policy 被改动: %+v", goal.Policy)
	}
	svc.wg.Wait()
	// 规划完成后仍保留总管开的会话，不被规划返回的会话覆盖。
	planned, err := st.GetGoal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("读取目标失败: %v", err)
	}
	if planned.PlanThreadID == nil || *planned.PlanThreadID != "thread-steward" {
		t.Fatalf("规划后应保留原 PlanThreadID，实际 %v", planned.PlanThreadID)
	}
	if planned.Owner != "steward" {
		t.Fatalf("规划后应保留 owner，实际 %q", planned.Owner)
	}

	// 原有 CreateGoal 签名与默认行为保持不变：无 owner、无规划会话、默认四键策略。
	plain, err := svc.CreateGoal(ctx, "proj-1", "普通目标")
	if err != nil {
		t.Fatalf("CreateGoal 失败: %v", err)
	}
	if plain.Owner != "" || plain.PlanThreadID != nil {
		t.Fatalf("普通目标不应带 owner/规划会话: %+v", plain)
	}
	if policyNumber(t, plain.Policy, "maxAttempts") != 2 {
		t.Fatalf("普通目标默认策略不正确: %+v", plain.Policy)
	}
	svc.wg.Wait()
}

func TestReviewRejectCountIncrements(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t)

	task, err := svc.CreateTask(ctx, CreateTaskInput{ProjectID: "proj-1", Title: "打回计数"})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	inReview, err := svc.MoveTask(ctx, task.ID, TaskStatusReview, task.Revision)
	if err != nil {
		t.Fatalf("移至 review 失败: %v", err)
	}
	rejected, err := svc.ReviewTask(ctx, inReview.ID, ReviewReject, "不合格", inReview.Revision)
	if err != nil {
		t.Fatalf("驳回失败: %v", err)
	}
	if rejected.RejectCount != 1 {
		t.Fatalf("首次驳回后期望 RejectCount 1，实际 %d", rejected.RejectCount)
	}

	inReview2, err := svc.MoveTask(ctx, rejected.ID, TaskStatusReview, rejected.Revision)
	if err != nil {
		t.Fatalf("再次移至 review 失败: %v", err)
	}
	rejected2, err := svc.ReviewTask(ctx, inReview2.ID, ReviewReject, "还是不合格", inReview2.Revision)
	if err != nil {
		t.Fatalf("第二次驳回失败: %v", err)
	}
	if rejected2.RejectCount != 2 {
		t.Fatalf("第二次驳回后期望 RejectCount 2，实际 %d", rejected2.RejectCount)
	}

	// accept 不清零打回次数。
	inReview3, err := svc.MoveTask(ctx, rejected2.ID, TaskStatusReview, rejected2.Revision)
	if err != nil {
		t.Fatalf("第三次移至 review 失败: %v", err)
	}
	accepted, err := svc.ReviewTask(ctx, inReview3.ID, ReviewAccept, "通过", inReview3.Revision)
	if err != nil {
		t.Fatalf("评审通过失败: %v", err)
	}
	if accepted.RejectCount != 2 {
		t.Fatalf("accept 不应清零 RejectCount，实际 %d", accepted.RejectCount)
	}
}

func TestParsePlanOutputAcceptanceCriteria(t *testing.T) {
	text := "```json\n{\"summary\":\"s\",\"tasks\":[{\"title\":\"任务\",\"notes\":\"n\",\"dependsOn\":[],\"acceptanceCriteria\":[\"[命令] `go test ./...`\",\"[审阅] 看 diff\"]}]}\n```"
	result, err := parsePlanOutput(text, "thread-1")
	if err != nil {
		t.Fatalf("解析规划失败: %v", err)
	}
	if len(result.Tasks) != 1 || len(result.Tasks[0].AcceptanceCriteria) != 2 {
		t.Fatalf("验收标准未解析: %+v", result.Tasks)
	}
	if result.Tasks[0].AcceptanceCriteria[0] != "[命令] `go test ./...`" {
		t.Fatalf("验收标准内容不正确: %v", result.Tasks[0].AcceptanceCriteria)
	}

	// 字段缺失时保持兼容：不报错、为 nil。
	result2, err := parsePlanOutput(`{"summary":"s","tasks":[{"title":"任务"}]}`, "thread-1")
	if err != nil {
		t.Fatalf("缺字段时应兼容: %v", err)
	}
	if len(result2.Tasks) != 1 || result2.Tasks[0].AcceptanceCriteria != nil {
		t.Fatalf("缺字段时应为 nil: %+v", result2.Tasks)
	}
}

// planReuseDriver 在 SendFollowUp 时把会话切成已完成并返回预设规划文本，模拟复用会话的规划器。
type planReuseDriver struct {
	mu        sync.Mutex
	starts    []SessionRequest
	states    map[string]SessionState
	followUps []pokeCall
	planText  string
}

func newPlanReuseDriver(planText string) *planReuseDriver {
	return &planReuseDriver{states: make(map[string]SessionState), planText: planText}
}

func (d *planReuseDriver) StartSession(ctx context.Context, req SessionRequest) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.starts = append(d.starts, req)
	const threadID = "thread-new-plan"
	d.states[threadID] = SessionState{LastAssistantText: d.planText}
	return threadID, nil
}

func (d *planReuseDriver) SessionState(ctx context.Context, threadID string) (SessionState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if st, ok := d.states[threadID]; ok {
		return st, nil
	}
	return SessionState{}, store.ErrNotFound
}

func (d *planReuseDriver) SendFollowUp(ctx context.Context, threadID, text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.followUps = append(d.followUps, pokeCall{threadID: threadID, text: text})
	d.states[threadID] = SessionState{LastAssistantText: d.planText}
	return nil
}

func (d *planReuseDriver) snapshot() (starts []SessionRequest, followUps []pokeCall) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]SessionRequest(nil), d.starts...), append([]pokeCall(nil), d.followUps...)
}

func TestPlanReusesPlanThreadForFirstPlan(t *testing.T) {
	ctx := context.Background()
	driver := newPlanReuseDriver(`{"summary":"复用会话规划","tasks":[{"title":"任务一","notes":"n","dependsOn":[],"acceptanceCriteria":["[命令] ` + "`go test ./...`" + `"]}]}`)
	threadID := "thread-plan"
	driver.states[threadID] = SessionState{LastAssistantText: ""}

	planner := NewAgentPlanner(driver, &mockProjects{dir: t.TempDir()})
	planner.PollInterval = time.Millisecond

	goal := store.Goal{ID: "goal-1", ProjectID: "proj-1", Title: "目标", Prompt: "做一个看板功能", PlanThreadID: &threadID}
	result, err := planner.Plan(ctx, goal, "")
	if err != nil {
		t.Fatalf("复用规划会话失败: %v", err)
	}
	if result.ThreadID != threadID {
		t.Fatalf("期望复用会话 %s，实际 %s", threadID, result.ThreadID)
	}
	if len(result.Tasks) != 1 || len(result.Tasks[0].AcceptanceCriteria) != 1 {
		t.Fatalf("规划结果不正确: %+v", result.Tasks)
	}

	starts, followUps := driver.snapshot()
	if len(starts) != 0 {
		t.Fatalf("复用会话时不应新开会话，实际 %d 次", len(starts))
	}
	if len(followUps) != 1 || followUps[0].threadID != threadID {
		t.Fatalf("期望向原会话追加一次首次规划提示词，实际 %+v", followUps)
	}
	if !strings.Contains(followUps[0].text, "acceptanceCriteria") || !strings.Contains(followUps[0].text, "做一个看板功能") {
		t.Fatalf("首次规划文案不完整: %s", followUps[0].text)
	}
	if strings.Contains(followUps[0].text, "用户看过你的计划") {
		t.Fatalf("无反馈时不应发送重规划文案: %s", followUps[0].text)
	}

	// 有反馈时仍走重规划文案。
	driver2 := newPlanReuseDriver(`{"summary":"重规划","tasks":[{"title":"任务一"}]}`)
	threadID2 := "thread-plan-2"
	driver2.states[threadID2] = SessionState{LastAssistantText: ""}
	planner2 := NewAgentPlanner(driver2, &mockProjects{dir: t.TempDir()})
	planner2.PollInterval = time.Millisecond
	goal2 := store.Goal{ID: "goal-2", ProjectID: "proj-1", Title: "目标", Prompt: "p", PlanThreadID: &threadID2}
	if _, err := planner2.Plan(ctx, goal2, "把任务拆细"); err != nil {
		t.Fatalf("复用会话重规划失败: %v", err)
	}
	_, followUps2 := driver2.snapshot()
	if len(followUps2) != 1 || !strings.Contains(followUps2[0].text, "用户看过你的计划") {
		t.Fatalf("有反馈时应发送重规划文案，实际 %+v", followUps2)
	}
}

// TestPlanStartsNewSessionWithoutPlanThread 校验没有规划会话时仍新开 plan 会话。
func TestPlanStartsNewSessionWithoutPlanThread(t *testing.T) {
	driver := newPlanReuseDriver(`{"summary":"新会话","tasks":[{"title":"任务一"}]}`)
	planner := NewAgentPlanner(driver, &mockProjects{dir: t.TempDir()})
	planner.PollInterval = time.Millisecond
	goal := store.Goal{ID: "goal-3", ProjectID: "proj-1", Title: "目标", Prompt: "p"}
	result, err := planner.Plan(context.Background(), goal, "")
	if err != nil {
		t.Fatalf("新会话规划失败: %v", err)
	}
	starts, followUps := driver.snapshot()
	if len(starts) != 1 || starts[0].InteractionMode != "plan" {
		t.Fatalf("期望新开 plan 会话，实际 %+v", starts)
	}
	if len(followUps) != 0 {
		t.Fatalf("新会话不应追加跟进消息: %+v", followUps)
	}
	if result.ThreadID != "thread-new-plan" {
		t.Fatalf("期望返回新会话 ID，实际 %s", result.ThreadID)
	}
}

// TestRunPlanKeepsPlannedAcceptanceCriteria 校验规划的验收标准落到任务上。
func TestRunPlanKeepsPlannedAcceptanceCriteria(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "planned_criteria.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	planner := &mockPlanner{result: PlanResult{
		Summary: "带验收标准的规划",
		Tasks: []PlannedTask{{
			Title:              "任务一",
			Notes:              "n",
			AcceptanceCriteria: []string{"[命令] `go test ./...`", "[审阅] 看 diff"},
		}},
	}}
	svc := NewService(Options{Store: st, Driver: newMockDriver(), Projects: &mockProjects{dir: t.TempDir()}, Planner: planner})
	t.Cleanup(func() { svc.Close() })

	goal, err := svc.CreateGoal(ctx, "proj-1", "验收标准透传")
	if err != nil {
		t.Fatalf("创建目标失败: %v", err)
	}
	svc.wg.Wait()

	taskIDs, err := st.ListGoalTaskIDs(ctx, goal.ID)
	if err != nil || len(taskIDs) != 1 {
		t.Fatalf("期望 1 个任务，实际 %v，错误: %v", taskIDs, err)
	}
	task, err := st.GetTask(ctx, taskIDs[0])
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	want := "- [命令] `go test ./...`\n- [审阅] 看 diff"
	if task.AcceptanceCriteria != want {
		t.Fatalf("期望验收标准 %q，实际 %q", want, task.AcceptanceCriteria)
	}
}
