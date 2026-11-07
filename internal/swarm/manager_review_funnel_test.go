package swarm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/kanban"
	"mcode/internal/reviewcheck"
	"mcode/internal/reviewfunnel"
	"mcode/internal/textgen"
)

// funnelFake 收集漏斗测试用的注入实现与调用次数，不启动真实 CLI 或 git diff。
type funnelFake struct {
	mu            sync.Mutex
	checkCalls    int
	generateCalls int
	starts        int
	busy          bool
	worktrees     map[string]string
	checkFn       func(ctx context.Context, worktree string, changed []string, configured string, timeout time.Duration) reviewcheck.Result
	generateFn    func(ctx context.Context, provider, model, effort, prompt string) (string, error)
	diffFn        func(ctx context.Context, worktree, base string, paths []string, maxBytes int) (string, bool, error)
	betweenFn     func(ctx context.Context, oldRoot, newRoot string, paths []string, maxBytes int) (string, error)
}

func (f *funnelFake) runCheck(ctx context.Context, worktree string, changed []string, configured string, timeout time.Duration) reviewcheck.Result {
	f.mu.Lock()
	f.checkCalls++
	fn := f.checkFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, worktree, changed, configured, timeout)
	}
	return reviewcheck.Result{OK: true}
}

func (f *funnelFake) generate(ctx context.Context, provider, model, effort, prompt string) (string, error) {
	f.mu.Lock()
	f.generateCalls++
	fn := f.generateFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, provider, model, effort, prompt)
	}
	return approveJSON(0), nil
}

func (f *funnelFake) diff(ctx context.Context, worktree, base string, paths []string, maxBytes int) (string, bool, error) {
	f.mu.Lock()
	fn := f.diffFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, worktree, base, paths, maxBytes)
	}
	return "diff --git a/internal/feature.go b/internal/feature.go\n+changed\n", false, nil
}

func (f *funnelFake) between(ctx context.Context, oldRoot, newRoot string, paths []string, maxBytes int) (string, error) {
	f.mu.Lock()
	fn := f.betweenFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, oldRoot, newRoot, paths, maxBytes)
	}
	return "", nil
}

func (f *funnelFake) calls() (check, generate int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checkCalls, f.generateCalls
}

// newFunnelManager 构造 ai 审查档的管理器：Git 用真实测试仓库，检查与模型全部注入。
func newFunnelManager(t *testing.T, fake *funnelFake) (*Manager, string) {
	t.Helper()
	runner, dir := initTestRepo(t)
	driver := newFakeDriver()
	driver.onStart = func(kanban.SessionRequest) {
		fake.mu.Lock()
		fake.starts++
		fake.mu.Unlock()
	}
	fake.worktrees = make(map[string]string)
	m := NewManager(Options{
		Git: runner, Driver: driver, WorktreeDir: t.TempDir(), Persister: newFakePersister(),
		PollInterval: time.Hour,
		ReviewRunCheck: func(ctx context.Context, worktree string, changed []string, configured string, timeout time.Duration) reviewcheck.Result {
			return fake.runCheck(ctx, worktree, changed, configured, timeout)
		},
		ReviewGenerate: func(ctx context.Context, provider, model, effort, prompt string) (string, error) {
			return fake.generate(ctx, provider, model, effort, prompt)
		},
		ReviewDiff: func(ctx context.Context, worktree, base string, paths []string, maxBytes int) (string, bool, error) {
			return fake.diff(ctx, worktree, base, paths, maxBytes)
		},
		ReviewDiffBetween: func(ctx context.Context, oldRoot, newRoot string, paths []string, maxBytes int) (string, error) {
			return fake.between(ctx, oldRoot, newRoot, paths, maxBytes)
		},
		ReviewProjectConfig: func(dataDir, projectDir string) (reviewcheck.ProjectConfig, error) {
			return reviewcheck.ProjectConfig{}, nil
		},
		LeadTouchedPaths: func(leadThreadID string) ([]string, bool) {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if !fake.busy {
				return nil, false
			}
			return []string{"internal/feature.go"}, true
		},
	})
	m.SetSettingsFunc(func() Settings {
		return Settings{
			Enabled: true, AutoDelegate: true, WorkspaceMode: "worktree",
			ReviewMode: "ai", ReviewProvider: "claude", ReviewModel: "test-model",
			ReviewMaxChangedLines: 40,
		}
	})
	return m, dir
}

func approveJSON(indexes ...int) string {
	parts := make([]string, 0, len(indexes))
	for _, idx := range indexes {
		parts = append(parts, fmt.Sprintf(`{"index":%d,"verdict":"approve","reason":"L2 通过"}`, idx))
	}
	return `{"reviews":[` + strings.Join(parts, ",") + `]}`
}

func escalateJSON(index int, reason string) string {
	return fmt.Sprintf(`{"reviews":[{"index":%d,"verdict":"escalate","reason":%q}]}`, index, reason)
}

func dispatchFunnelBee(t *testing.T, m *Manager, fake *funnelFake, leadID, name, dir, task string) Bee {
	t.Helper()
	return dispatchFunnelBeePrompt(t, m, fake, leadID, name, dir, task, "")
}

// dispatchFunnelBeePrompt 允许短标题与完整 brief 不同，用于验证漏斗读取的是 Prompt。
func dispatchFunnelBeePrompt(t *testing.T, m *Manager, fake *funnelFake, leadID, name, dir, task, prompt string) Bee {
	t.Helper()
	bees, err := m.Dispatch(context.Background(), DispatchRequest{
		LeadThreadID: leadID, LeadWorkDir: dir,
		Bees: []BeeSpec{{Name: name, Task: task, Prompt: prompt}},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	fake.mu.Lock()
	fake.worktrees[bees[0].ThreadID] = bees[0].WorktreePath
	fake.mu.Unlock()
	return bees[0]
}

func writeBeeFile(t *testing.T, bee Bee, rel, content string) {
	t.Helper()
	path := filepath.Join(bee.WorktreePath, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func completeBee(t *testing.T, m *Manager, bee Bee, summary string) {
	t.Helper()
	m.opts.Driver.(*fakeDriver).setSessionState(bee.ThreadID, kanban.SessionState{
		Running: false, LastAssistantText: summary,
	})
}

// waitFunnel 反复 Tick 并等待 bee 满足条件；漏斗 worker 是异步的，L2 也需要下一个 Tick 发车。
func waitFunnel(t *testing.T, m *Manager, leadID, threadID string, cond func(Bee) bool) Bee {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		h := beeByThread(t, m, leadID, threadID)
		if cond(h) {
			return h
		}
		m.Tick(context.Background())
		time.Sleep(5 * time.Millisecond)
	}
	h := beeByThread(t, m, leadID, threadID)
	t.Fatalf("等待漏斗结果超时: %+v", h)
	return Bee{}
}

func waitFunnelIndex(t *testing.T, m *Manager, leadID string, index int, cond func(Bee) bool) Bee {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		for _, b := range m.State(leadID).Bees {
			if b.Index == index && cond(b) {
				return b
			}
		}
		m.Tick(context.Background())
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待第 %d 个 bee 的漏斗结果超时: %+v", index, m.State(leadID).Bees)
	return Bee{}
}

func isApproved(h Bee) bool { return h.ReviewState == ReviewApproved && h.Merged }
func isRejected(h Bee) bool { return h.ReviewState == ReviewRejected }
func isNeedsHuman(h Bee) bool {
	return h.ReviewState == ReviewNeedsHuman
}
func isL3(h Bee) bool { return h.ReviewStage == ReviewStageL3 }

func containsReviewConcern(concerns []string, want string) bool {
	for _, c := range concerns {
		if strings.Contains(c, want) {
			return true
		}
	}
	return false
}

// 硬越界但检查通过的改动进 L2，不会被 L0/L1 直接驳回。
func TestReviewFunnelL0ScopeDoesNotReject(t *testing.T) {
	fake := &funnelFake{}
	m, dir := newFunnelManager(t, fake)
	fake.generateFn = func(context.Context, string, string, string, string) (string, error) {
		return approveJSON(0), nil
	}
	bee := dispatchFunnelBee(t, m, fake, "lead-scope", "Aspen", dir, "修改 internal/feature.go")
	writeBeeFile(t, bee, "internal/other.go", "package other\n")
	completeBee(t, m, bee, "完成")

	h := waitFunnel(t, m, "lead-scope", bee.ThreadID, isApproved)
	if h.ReviewLayer != "l2" {
		t.Fatalf("硬越界应走 L2，实际 %q (%+v)", h.ReviewLayer, h)
	}
	if check, generate := fake.calls(); check != 1 || generate != 1 {
		t.Fatalf("应各调用一次检查与模型，实际 check=%d generate=%d", check, generate)
	}
}

// 检查命令失败时直接驳回，原因包含命令与输出尾部。
func TestReviewFunnelL0CheckFailureRejects(t *testing.T) {
	fake := &funnelFake{}
	m, dir := newFunnelManager(t, fake)
	fake.checkFn = func(context.Context, string, []string, string, time.Duration) reviewcheck.Result {
		return reviewcheck.Result{OK: false, Display: "go test ./...", Output: "line1\nFAIL: boom"}
	}
	bee := dispatchFunnelBee(t, m, fake, "lead-check", "Aspen", dir, "修改 internal/feature.go")
	writeBeeFile(t, bee, "internal/feature.go", "package feature\n")
	completeBee(t, m, bee, "完成")

	h := waitFunnel(t, m, "lead-check", bee.ThreadID, isRejected)
	if h.ReviewLayer != "l0" {
		t.Fatalf("检查失败应为 l0 驳回，实际 %q", h.ReviewLayer)
	}
	for _, want := range []string{"go test ./...", "FAIL: boom"} {
		if !strings.Contains(h.ReviewReason, want) {
			t.Fatalf("驳回原因缺少 %q: %s", want, h.ReviewReason)
		}
	}
	if _, generate := fake.calls(); generate != 0 {
		t.Fatalf("检查失败不应调用模型，实际 %d 次", generate)
	}
}

// 只改计划文档时 L1 直接放行，不调用检查命令与模型。
func TestReviewFunnelL1PlanDocApproves(t *testing.T) {
	fake := &funnelFake{}
	m, dir := newFunnelManager(t, fake)
	bee := dispatchFunnelBee(t, m, fake, "lead-plan", "Aspen", dir, "写计划 docs/superpowers/plans/2026-11-03-x.md")
	writeBeeFile(t, bee, "docs/superpowers/plans/2026-11-03-x.md", "# 计划\n\n- 步骤一\n")
	completeBee(t, m, bee, "完成")

	h := waitFunnel(t, m, "lead-plan", bee.ThreadID, isApproved)
	if h.ReviewLayer != "l1" {
		t.Fatalf("计划文档应由 L1 放行，实际 %q (%+v)", h.ReviewLayer, h)
	}
	if check, generate := fake.calls(); check != 0 || generate != 0 {
		t.Fatalf("计划文档不应跑检查或模型，实际 check=%d generate=%d", check, generate)
	}
	if _, err := os.Stat(filepath.Join(dir, "docs/superpowers/plans/2026-11-03-x.md")); err != nil {
		t.Fatalf("计划文档未合入: %v", err)
	}
}

// 配置了检查命令时，计划文档仍然跳过命令。
func TestReviewFunnelPlanSkipsCheckCommand(t *testing.T) {
	fake := &funnelFake{}
	m, dir := newFunnelManager(t, fake)
	m.SetSettingsFunc(func() Settings {
		return Settings{
			Enabled: true, AutoDelegate: true, WorkspaceMode: "worktree",
			ReviewMode: "ai", ReviewProvider: "claude", ReviewModel: "test-model",
			ReviewMaxChangedLines: 40, ReviewCheckCommand: "go test ./...",
		}
	})
	bee := dispatchFunnelBee(t, m, fake, "lead-plan-check", "Aspen", dir, "写计划 docs/superpowers/plans/2026-11-03-y.md")
	writeBeeFile(t, bee, "docs/superpowers/plans/2026-11-03-y.md", "# 计划\n")
	completeBee(t, m, bee, "完成")

	if h := waitFunnel(t, m, "lead-plan-check", bee.ThreadID, isApproved); h.ReviewLayer != "l1" {
		t.Fatalf("计划文档应由 L1 放行，实际 %+v", h)
	}
	if check, _ := fake.calls(); check != 0 {
		t.Fatalf("计划文档不应执行配置的检查命令，实际 %d 次", check)
	}
}

// 40 行以内由 L1 放行，41 行进入 L2。
func TestReviewFunnelL1LineThreshold(t *testing.T) {
	for _, tc := range []struct {
		lines int
		layer string
	}{{40, "l1"}, {41, "l2"}} {
		t.Run(fmt.Sprintf("%d行", tc.lines), func(t *testing.T) {
			fake := &funnelFake{}
			m, dir := newFunnelManager(t, fake)
			bee := dispatchFunnelBee(t, m, fake, "lead-lines", "Aspen", dir, "修改 internal/feature.go")
			content := strings.Repeat("line\n", tc.lines)
			writeBeeFile(t, bee, "internal/feature.go", content)
			completeBee(t, m, bee, "完成")

			h := waitFunnel(t, m, "lead-lines", bee.ThreadID, isApproved)
			if h.ReviewLayer != tc.layer {
				t.Fatalf("%d 行应走 %s，实际 %q (%+v)", tc.lines, tc.layer, h.ReviewLayer, h)
			}
		})
	}
}

// 派发短标题里没有路径时，L1 应读取 Prompt 里的完整 brief 核对范围。
func TestReviewFunnelL1UsesBriefPrompt(t *testing.T) {
	fake := &funnelFake{}
	m, dir := newFunnelManager(t, fake)
	brief := "修改 internal/feature.go：把常量改成 2"
	bee := dispatchFunnelBeePrompt(t, m, fake, "lead-brief", "Aspen", dir, "实现功能", brief)
	writeBeeFile(t, bee, "internal/feature.go", "package feature\n\nconst X = 2\n")
	completeBee(t, m, bee, "完成")

	h := waitFunnel(t, m, "lead-brief", bee.ThreadID, isApproved)
	if h.ReviewLayer != "l1" {
		t.Fatalf("brief 给出范围时应由 L1 放行，实际 %q (%+v)", h.ReviewLayer, h)
	}
	if _, generate := fake.calls(); generate != 0 {
		t.Fatalf("L1 放行不应调用模型，实际 %d 次", generate)
	}
}

// L2 请求里的 Brief 应取 bee 的 Prompt 原文，而不是短标题。
func TestReviewFunnelL2PromptContainsBrief(t *testing.T) {
	fake := &funnelFake{}
	m, dir := newFunnelManager(t, fake)
	brief := "修改 internal/other.go：补充 X 的注释"
	prompts := make(chan string, 1)
	fake.generateFn = func(_ context.Context, _, _, _, prompt string) (string, error) {
		select {
		case prompts <- prompt:
		default:
		}
		return approveJSON(0), nil
	}
	bee := dispatchFunnelBeePrompt(t, m, fake, "lead-brief-l2", "Aspen", dir, "完善代码", brief)
	writeBeeFile(t, bee, "internal/other.go", strings.Repeat("// line\n", 49)+"package other\n\nvar X = 1\n")
	completeBee(t, m, bee, "完成")

	h := waitFunnel(t, m, "lead-brief-l2", bee.ThreadID, isApproved)
	if h.ReviewLayer != "l2" {
		t.Fatalf("大改动应走 L2，实际 %q (%+v)", h.ReviewLayer, h)
	}
	select {
	case prompt := <-prompts:
		if !strings.Contains(prompt, brief) {
			t.Fatalf("L2 prompt 未包含 brief 原文: %s", prompt)
		}
	case <-time.After(time.Second):
		t.Fatal("L2 未调用模型")
	}
}

// L2 approve 后合入主工作区，并记录层、tokens 与 concerns。
func TestReviewFunnelL2ApproveMerges(t *testing.T) {
	fake := &funnelFake{}
	m, dir := newFunnelManager(t, fake)
	fake.generateFn = func(context.Context, string, string, string, string) (string, error) {
		return `{"reviews":[{"index":0,"verdict":"approve","reason":"看过了","concerns":["internal/feature.go:2 变量命名"]}]}`, nil
	}
	bee := dispatchFunnelBee(t, m, fake, "lead-approve", "Aspen", dir, "修改 internal/other.go")
	writeBeeFile(t, bee, "internal/other.go", strings.Repeat("// line\n", 49)+"package other\n\nvar X = 1\n")
	completeBee(t, m, bee, "完成")

	h := waitFunnel(t, m, "lead-approve", bee.ThreadID, isApproved)
	if h.ReviewLayer != "l2" || h.ReviewTokens <= 0 || !h.ReviewTokensEstimated {
		t.Fatalf("L2 结论字段不完整: %+v", h)
	}
	if !containsReviewConcern(h.ReviewConcerns, "变量命名") {
		t.Fatalf("L2 concerns 未写回: %+v", h.ReviewConcerns)
	}
	got, err := os.ReadFile(filepath.Join(dir, "internal/other.go"))
	if err != nil || !strings.Contains(string(got), "var X = 1") {
		t.Fatalf("L2 通过后未合入: %q, %v", got, err)
	}
}

// L2 escalate 升级到 L3，只启动一次审查会话。
func TestReviewFunnelL2EscalateStartsSession(t *testing.T) {
	fake := &funnelFake{}
	m, dir := newFunnelManager(t, fake)
	fake.generateFn = func(context.Context, string, string, string, string) (string, error) {
		return escalateJSON(0, "需要人看"), nil
	}
	bee := dispatchFunnelBee(t, m, fake, "lead-escalate", "Aspen", dir, "修改 internal/other.go")
	writeBeeFile(t, bee, "internal/other.go", strings.Repeat("// line\n", 50))
	completeBee(t, m, bee, "完成")

	h := waitFunnel(t, m, "lead-escalate", bee.ThreadID, func(b Bee) bool { return b.ReviewThreadID != "" })
	if h.ReviewLayer != "l3" || h.ReviewState != ReviewRunning {
		t.Fatalf("escalate 后应为 l3 审查中: %+v", h)
	}
	fake.mu.Lock()
	starts := fake.starts
	fake.mu.Unlock()
	if starts != 2 {
		t.Fatalf("应只启动 1 个 L3 会话（加派发共 2 次），实际 %d", starts)
	}
	// L3 会话已存在时，后续 Tick 不会重复启动。
	m.Tick(context.Background())
	fake.mu.Lock()
	starts = fake.starts
	fake.mu.Unlock()
	if starts != 2 {
		t.Fatalf("L3 会话被重复启动，实际 %d", starts)
	}
}

// L2 通过但主工作区未落定时只重试合入，不再调用模型。
func TestReviewFunnelL2ApproveRetriesSettle(t *testing.T) {
	fake := &funnelFake{busy: true}
	m, dir := newFunnelManager(t, fake)
	bee := dispatchFunnelBee(t, m, fake, "lead-retry", "Aspen", dir, "修改 internal/feature.go")
	writeBeeFile(t, bee, "internal/feature.go", strings.Repeat("// line\n", 50))
	completeBee(t, m, bee, "完成")

	h := waitFunnel(t, m, "lead-retry", bee.ThreadID, func(b Bee) bool { return b.ReviewApplyPending })
	if h.ReviewState != ReviewRunning || h.ReviewLayer != "l2" {
		t.Fatalf("未落定时应保持 L2 审查中: %+v", h)
	}
	fake.mu.Lock()
	fake.busy = false
	fake.mu.Unlock()

	h = waitFunnel(t, m, "lead-retry", bee.ThreadID, isApproved)
	if h.ReviewApplyPending {
		t.Fatalf("合入后应清除重试标记: %+v", h)
	}
	if _, generate := fake.calls(); generate != 1 {
		t.Fatalf("重试合入不应再调用模型，实际 %d 次", generate)
	}
}

// 审查期间工作副本被改动时，approve 结论不再应用，转人工。
func TestReviewFunnelApproveRejectsChangedWorktree(t *testing.T) {
	fake := &funnelFake{}
	m, dir := newFunnelManager(t, fake)
	fake.generateFn = func(_ context.Context, _, _, _, _ string) (string, error) {
		var worktree string
		fake.mu.Lock()
		for _, wt := range fake.worktrees {
			worktree = wt
		}
		fake.mu.Unlock()
		if worktree != "" {
			_ = os.WriteFile(filepath.Join(worktree, "late.txt"), []byte("late\n"), 0644)
		}
		return approveJSON(0), nil
	}
	bee := dispatchFunnelBee(t, m, fake, "lead-changed", "Aspen", dir, "修改 internal/other.go")
	writeBeeFile(t, bee, "internal/other.go", strings.Repeat("// line\n", 50))
	completeBee(t, m, bee, "完成")

	h := waitFunnel(t, m, "lead-changed", bee.ThreadID, isNeedsHuman)
	if h.Merged || !strings.Contains(h.ReviewReason, "快照已变化") {
		t.Fatalf("改动后的快照不应合入: %+v", h)
	}
}

// 整批解析失败时升级 L3。
func TestReviewFunnelL2ParseFailurePromotesL3(t *testing.T) {
	fake := &funnelFake{}
	m, dir := newFunnelManager(t, fake)
	fake.generateFn = func(context.Context, string, string, string, string) (string, error) {
		return "这不是 JSON", nil
	}
	bee := dispatchFunnelBee(t, m, fake, "lead-parse", "Aspen", dir, "修改 internal/other.go")
	writeBeeFile(t, bee, "internal/other.go", strings.Repeat("// line\n", 50))
	completeBee(t, m, bee, "完成")

	h := waitFunnel(t, m, "lead-parse", bee.ThreadID, isL3)
	if !strings.Contains(h.ReviewReason, "无法解析") {
		t.Fatalf("L3 原因应说明解析失败: %s", h.ReviewReason)
	}
}

// 返工 bee 的 L2 输入只带旧原因与增量 diff。
func TestReviewFunnelReworkUsesDelta(t *testing.T) {
	fake := &funnelFake{}
	m, _ := newFunnelManager(t, fake)
	oldDir := t.TempDir()
	newDir := t.TempDir()
	fake.betweenFn = func(_ context.Context, oldRoot, newRoot string, _ []string, _ int) (string, error) {
		if oldRoot != oldDir || newRoot != newDir {
			t.Fatalf("DiffBetween 参数错误: %s %s", oldRoot, newRoot)
		}
		return "DELTA-ONLY\n", nil
	}
	old := Bee{ThreadID: "old-thread", Name: "Aspen", ReviewState: ReviewRejected, WorktreePath: oldDir}
	cur := Bee{
		Index: 1, ThreadID: "new-thread", Name: "Birch", WorktreePath: newDir,
		ChangedFiles: []string{"internal/feature.go"},
		ReworkOf:     "old-thread", ReworkReason: "旧原因", ReworkPatchPath: "/tmp/old.patch",
	}
	m.mu.Lock()
	m.leads["lead-rework"] = &leadState{leadThreadID: "lead-rework", bees: []Bee{old, cur}}
	m.mu.Unlock()

	req, ok := m.buildL2Request(context.Background(), "lead-rework", cur, reviewfunnel.DefaultBudget())
	if !ok || req.Rework == nil {
		t.Fatalf("返工请求未生成: ok=%v req=%+v", ok, req)
	}
	if req.Rework.PreviousReason != "旧原因" || req.Rework.Delta != "DELTA-ONLY\n" || req.Rework.OldUnavailable {
		t.Fatalf("返工输入错误: %+v", req.Rework)
	}

	old.WorktreePath = filepath.Join(oldDir, "missing")
	m.mu.Lock()
	m.leads["lead-rework"].bees[0].WorktreePath = old.WorktreePath
	m.mu.Unlock()
	req, ok = m.buildL2Request(context.Background(), "lead-rework", cur, reviewfunnel.DefaultBudget())
	if !ok || req.Rework == nil || !req.Rework.OldUnavailable {
		t.Fatalf("旧工作副本不存在时应标记降级: %+v", req.Rework)
	}
}

// L0 跑 3 分钟不会被 2 分钟宽限误判；L2 超过 90 秒则转人工。
func TestReviewFunnelPollReviewSkipsGraceForL0AndL2(t *testing.T) {
	m, _, bee := pendingReviewBee(t)
	ctx := context.Background()
	started := time.Now().Add(-3 * time.Minute)
	queued := time.Now().Add(-3 * time.Minute)
	m.mu.Lock()
	target := &m.leads["lead-review"].bees[0]
	target.ReviewState = ReviewRunning
	target.PendingMerge = true
	target.ReviewStage = ReviewStageL0
	target.ReviewStartedAt = &started
	target.ReviewQueuedAt = &queued
	m.mu.Unlock()

	m.Tick(ctx)
	h := beeByThread(t, m, "lead-review", bee.ThreadID)
	if h.ReviewState != ReviewRunning {
		t.Fatalf("L0 未超预算时不应误判为未启动: %+v", h)
	}

	l2Start := time.Now().Add(-2 * time.Minute)
	m.mu.Lock()
	target = &m.leads["lead-review"].bees[0]
	target.ReviewStage = ReviewStageL2
	target.ReviewStartedAt = &l2Start
	target.ReviewQueuedAt = &l2Start
	m.mu.Unlock()
	m.Tick(ctx)
	got := beeByThread(t, m, "lead-review", bee.ThreadID)
	if got.ReviewState != ReviewNeedsHuman || !strings.Contains(got.ReviewReason, "L2 审查超时") {
		t.Fatalf("L2 超时应转人工: %+v", got)
	}
}

// Stop 取消漏斗 ctx，正在跑的 L0 检查立即退出。
func TestReviewFunnelStopCancelsCheck(t *testing.T) {
	fake := &funnelFake{}
	m, dir := newFunnelManager(t, fake)
	entered := make(chan struct{})
	fake.checkFn = func(ctx context.Context, _ string, _ []string, _ string, _ time.Duration) reviewcheck.Result {
		close(entered)
		<-ctx.Done()
		return reviewcheck.Result{OK: true}
	}
	m.Start()
	bee := dispatchFunnelBee(t, m, fake, "lead-stop", "Aspen", dir, "修改 internal/feature.go")
	writeBeeFile(t, bee, "internal/feature.go", "package feature\n")
	completeBee(t, m, bee, "完成")
	m.Tick(context.Background())

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("检查命令未启动")
	}
	done := make(chan struct{})
	go func() {
		m.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop 未取消正在运行的检查")
	}
}

// 晚到的 L2 结论在写回前发现 stage 已变时直接丢弃。
func TestReviewFunnelLateResultIgnored(t *testing.T) {
	fake := &funnelFake{}
	m, dir := newFunnelManager(t, fake)
	var threadID string
	fake.generateFn = func(context.Context, string, string, string, string) (string, error) {
		m.markReviewNeedsHuman("lead-late", threadID, "人工已接管")
		return approveJSON(0), nil
	}
	bee := dispatchFunnelBee(t, m, fake, "lead-late", "Aspen", dir, "修改 internal/other.go")
	threadID = bee.ThreadID
	writeBeeFile(t, bee, "internal/other.go", strings.Repeat("// line\n", 50))
	completeBee(t, m, bee, "完成")

	h := waitFunnel(t, m, "lead-late", bee.ThreadID, isNeedsHuman)
	if h.Merged || h.ReviewLayer == "l2" {
		t.Fatalf("晚到结论应被丢弃: %+v", h)
	}
}

// BuildReport 的审查结论与疑点行。
func TestBuildReportReviewFunnelLines(t *testing.T) {
	approved := Bee{
		Name: "Aspen", Task: "改代码", Status: StatusCompleted,
		Merged: true, ReviewState: ReviewApproved, ReviewLayer: "l2",
		ReviewTokens: 3200, ReviewTokensEstimated: true,
		ReviewConcerns: []string{"internal/a.go:12 缺少错误处理", "internal/b.go:7 未覆盖分支"},
	}
	report := BuildReport([]Bee{approved})
	for _, want := range []string{"审查：L2 通过（约 3.2k tokens）", "审查疑点：internal/a.go:12 缺少错误处理", "审查疑点：internal/b.go:7 未覆盖分支"} {
		if !strings.Contains(report, want) {
			t.Fatalf("汇报缺少 %q:\n%s", want, report)
		}
	}

	human := Bee{Name: "Birch", Task: "改代码", Status: StatusCompleted, ReviewState: ReviewApproved, ReviewLayer: "human"}
	if got := BuildReport([]Bee{human}); !strings.Contains(got, "审查：人工批准") {
		t.Fatalf("人工审查结论缺失:\n%s", got)
	}

	off := Bee{Name: "Cove", Task: "改代码", Status: StatusCompleted}
	if got := BuildReport([]Bee{off}); strings.Contains(got, "审查：") {
		t.Fatalf("off 档不应输出审查行:\n%s", got)
	}
}

// planEntry 把 reworkOf 或 brief 里的补丁路径关联到被驳回的 bee。
func TestPlanEntryLinksReworkOf(t *testing.T) {
	fake := &funnelFake{}
	m, dir := newFunnelManager(t, fake)
	old := dispatchFunnelBee(t, m, fake, "lead-rework-entry", "Aspen", dir, "旧任务")
	patchPath := filepath.Join(t.TempDir(), "old.patch")
	m.mu.Lock()
	target := &m.leads["lead-rework-entry"].bees[0]
	target.ReviewState = ReviewRejected
	target.ReviewReason = "缺少测试"
	target.ReviewPatchPath = patchPath
	m.mu.Unlock()
	completeBee(t, m, old, "完成")

	ctx := context.Background()
	plan := m.planEntry(ctx, "lead-rework-entry", Delegation{Prompt: "返工", ReworkOf: old.Name}, nil, "", dir, map[int]BeeStatus{}, 0)
	if plan.spec.ReworkOf != old.ThreadID || plan.spec.ReworkPatchPath != patchPath || plan.spec.ReworkReason != "缺少测试" {
		t.Fatalf("reworkOf 未关联: %+v", plan.spec)
	}

	plan = m.planEntry(ctx, "lead-rework-entry", Delegation{Prompt: "按 " + patchPath + " 返工"}, nil, "", dir, map[int]BeeStatus{}, 0)
	if plan.spec.ReworkOf != old.ThreadID || plan.spec.ReworkPatchPath != patchPath {
		t.Fatalf("brief 里的补丁路径未关联: %+v", plan.spec)
	}

	plan = m.planEntry(ctx, "lead-rework-entry", Delegation{Prompt: "普通任务"}, nil, "", dir, map[int]BeeStatus{}, 0)
	if plan.spec.ReworkOf != "" || plan.spec.ReworkPatchPath != "" || plan.spec.ReworkReason != "" {
		t.Fatalf("找不到返工对象时应留空: %+v", plan.spec)
	}
}

// 回退到其他 provider 时不能沿用显式 provider 的模型与 effort。
func TestReviewModelForFallback(t *testing.T) {
	if model, effort := reviewModelFor("codex", "codex", "gpt-6.1-sol", "high"); model != "gpt-6.1-sol" || effort != "high" {
		t.Fatalf("显式 provider 应使用配置: model=%q effort=%q", model, effort)
	}
	if model, effort := reviewModelFor("claude", "codex", "gpt-6.1-sol", "high"); model != textgen.CheapestModel("claude") || effort != "" {
		t.Fatalf("回退 provider 应改用最便宜模型且不传 effort: model=%q effort=%q", model, effort)
	}
	if model, effort := reviewModelFor("claude", "", "gpt-6.1-sol", "high"); model != textgen.CheapestModel("claude") || effort != "" {
		t.Fatalf("未配置 provider 时应忽略 model 与 effort: model=%q effort=%q", model, effort)
	}
}
