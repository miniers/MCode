package swarm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/git"
	"mcode/internal/testenv"
)

// gitTestRun 在指定目录直接执行 git 命令，仅用于测试准备与断言。
func gitTestRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=mcode-test",
		"GIT_AUTHOR_EMAIL=mcode-test@localhost",
		"GIT_COMMITTER_NAME=mcode-test",
		"GIT_COMMITTER_EMAIL=mcode-test@localhost",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s 失败: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// writePipelineFile 以仓库相对路径写入测试文件。
func writePipelineFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("写入 %s 失败: %v", name, err)
	}
}

// pipelineForgeExec 是测试用的 fake CommandExecutor：记录调用并按 handler 返回结果。
type pipelineForgeExec struct {
	mu      sync.Mutex
	calls   [][]string
	handler func(args []string) ([]byte, error)
}

func (e *pipelineForgeExec) run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	e.mu.Lock()
	e.calls = append(e.calls, append([]string{name}, args...))
	handler := e.handler
	e.mu.Unlock()
	if handler != nil {
		return handler(args)
	}
	return []byte{}, nil
}

func (e *pipelineForgeExec) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
}

func (e *pipelineForgeExec) snapshot() [][]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([][]string, len(e.calls))
	for i, call := range e.calls {
		out[i] = append([]string(nil), call...)
	}
	return out
}

// anyContains 判断通知列表中是否有消息包含指定子串。
func anyContains(messages []string, substr string) bool {
	for _, msg := range messages {
		if strings.Contains(msg, substr) {
			return true
		}
	}
	return false
}

func newPipelineFileBatch(dir string, bees ...Bee) LandedBatch {
	return LandedBatch{
		LeadThreadID:  "lead-12345678",
		TargetWorkDir: dir,
		Bees:          bees,
	}
}

func addNewFilePatch(t *testing.T, b *LandedBatch) {
	t.Helper()
	for i := range b.Bees {
		name := b.Bees[i].ChangedFiles[0]
		data, err := os.ReadFile(filepath.Join(b.TargetWorkDir, name))
		if err != nil {
			t.Fatal(err)
		}
		patch := fmt.Sprintf("diff --git a/%s b/%s\nnew file mode 100644\n--- /dev/null\n+++ b/%s\n@@ -0,0 +1 @@\n+%s", name, name, name, data)
		path := filepath.Join(t.TempDir(), "landed.patch")
		if err := os.WriteFile(path, []byte(patch), 0644); err != nil {
			t.Fatal(err)
		}
		b.Bees[i].LandedPatchPath = path
	}
}

// (a) 两个开关都关闭时，自动批次不做任何事：无提交、不 push、不调用 forge。
func TestPipeline_BothSwitchesOffDoesNothing(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	writePipelineFile(t, dir, "a.txt", "a\n")
	beeBefore := gitTestRun(t, dir, "rev-parse", "HEAD")

	var pushCalls int
	exec := &pipelineForgeExec{}
	p := NewPipeline(PipelineOptions{
		Git:      runner,
		Forge:    git.NewForgeClient(exec.run),
		Settings: func() Settings { return Settings{} },
		Push: func(context.Context, string, string, string, string) error {
			pushCalls++
			return nil
		},
	})

	b := newPipelineFileBatch(dir, Bee{Name: "A", Task: "修改 a.txt", ChangedFiles: []string{"a.txt"}, Merged: true})
	if err := p.RunBatch(ctx, b, false); err != nil {
		t.Fatalf("双开关关闭时 RunBatch 应直接返回: %v", err)
	}
	if beeNow := gitTestRun(t, dir, "rev-parse", "HEAD"); beeNow != beeBefore {
		t.Errorf("不应产生提交: %s -> %s", beeBefore, beeNow)
	}
	if pushCalls != 0 {
		t.Errorf("不应调用 push，实际 %d 次", pushCalls)
	}
	if exec.callCount() != 0 {
		t.Errorf("不应调用 forge，实际 %d 次", exec.callCount())
	}
	if status := gitTestRun(t, dir, "status", "--porcelain"); !strings.Contains(status, "a.txt") {
		t.Errorf("文件应保持未提交，status:\n%s", status)
	}
}

// (b) AutoMerge 开、AutoPush 关：只创建本地提交，push 与 forge 零调用。
func TestPipeline_AutoMergeOnlyCommitsLocally(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	writePipelineFile(t, dir, "a.txt", "a\n")
	writePipelineFile(t, dir, "b.txt", "b\n")
	writePipelineFile(t, dir, "other.txt", "o\n")
	beeBefore := gitTestRun(t, dir, "rev-parse", "HEAD")

	var pushCalls, cleanupCalls int
	var notified []string
	exec := &pipelineForgeExec{}
	p := NewPipeline(PipelineOptions{
		Git:      runner,
		Forge:    git.NewForgeClient(exec.run),
		Settings: func() Settings { return Settings{AutoMerge: true} },
		Notify:   func(_, msg string) { notified = append(notified, msg) },
		Push: func(context.Context, string, string, string, string) error {
			pushCalls++
			return nil
		},
		Cleanup: func(context.Context, LandedBatch) { cleanupCalls++ },
	})

	b := newPipelineFileBatch(dir,
		Bee{Name: "A", Task: "做 A", ChangedFiles: []string{"a.txt"}, Merged: true},
		Bee{Name: "B", Task: "做 B", ChangedFiles: []string{"b.txt"}, Merged: true},
	)
	addNewFilePatch(t, &b)
	if err := p.RunBatch(ctx, b, false); err != nil {
		t.Fatalf("RunBatch failed: %v", err)
	}

	beeAfter := gitTestRun(t, dir, "rev-parse", "HEAD")
	if beeAfter == beeBefore {
		t.Fatal("AutoMerge 开启时应创建本地提交")
	}
	committed := strings.Split(gitTestRun(t, dir, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD"), "\n")
	sort.Strings(committed)
	if !reflect.DeepEqual(committed, []string{"a.txt", "b.txt"}) {
		t.Errorf("提交内容 = %v, want [a.txt b.txt]", committed)
	}
	if subject := gitTestRun(t, dir, "log", "-1", "--format=%s"); subject != "feat: 做 A; 做 B" {
		t.Errorf("提交说明首行 = %q", subject)
	}
	status := gitTestRun(t, dir, "status", "--porcelain")
	if !strings.Contains(status, "other.txt") {
		t.Errorf("未列入批次的 other.txt 应保持未提交，status:\n%s", status)
	}
	if strings.Contains(status, "a.txt") || strings.Contains(status, "b.txt") {
		t.Errorf("批次内文件提交后应干净，status:\n%s", status)
	}
	if pushCalls != 0 {
		t.Errorf("AutoPush 关闭时不应 push，实际 %d 次", pushCalls)
	}
	if exec.callCount() != 0 {
		t.Errorf("AutoPush 关闭时不应调用 forge，实际 %d 次", exec.callCount())
	}
	if cleanupCalls != 1 {
		t.Errorf("本地落地后应清理 worktree，实际 %d 次", cleanupCalls)
	}
	if !anyContains(notified, "自动推送未开启") {
		t.Errorf("应通知自动推送未开启，实际 %v", notified)
	}
}

// (c) AutoMerge + AutoPush 都开启：push 一次 mcode/swarm- 分支，forge 先 create 再 merge。
// origin 指向 github URL（供 DetectForge 识别），push 使用 fake 实现，绝不触网；
// initTestRepo 的 protocol.https.allow=never 让同步阶段的 fetch 立即被拒绝，保证测试完全离线。
func TestPipeline_AutoPushPushesAndOpensThenMerges(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	gitTestRun(t, dir, "remote", "add", "origin", "https://github.com/x/y.git")
	writePipelineFile(t, dir, "a.txt", "a\n")

	const prURL = "https://github.com/x/y/pull/9"
	exec := &pipelineForgeExec{handler: func(args []string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "pr" && args[1] == "create" {
			return []byte(prURL + "\n"), nil
		}
		return []byte(""), nil
	}}
	var pushCalls int
	var pushRemote, pushLocalRef, pushBranch string
	var notified []string
	p := NewPipeline(PipelineOptions{
		Git:      runner,
		Forge:    git.NewForgeClient(exec.run),
		Settings: func() Settings { return Settings{AutoMerge: true, AutoPush: true} },
		Notify:   func(_, msg string) { notified = append(notified, msg) },
		Push: func(_ context.Context, root, remote, localRef, remoteBranch string) error {
			pushCalls++
			pushRemote, pushLocalRef, pushBranch = remote, localRef, remoteBranch
			return nil
		},
	})

	b := newPipelineFileBatch(dir, Bee{Name: "A", Task: "做 A", ChangedFiles: []string{"a.txt"}, Merged: true})
	addNewFilePatch(t, &b)
	if err := p.RunBatch(ctx, b, false); err != nil {
		t.Fatalf("RunBatch failed: %v", err)
	}

	if pushCalls != 1 {
		t.Fatalf("push 应调用 1 次，实际 %d 次", pushCalls)
	}
	// 推送的是本批次的提交（而不是字面 HEAD），因此 localRef 应等于当前 HEAD 的 sha。
	batchSHA := gitTestRun(t, dir, "rev-parse", "HEAD")
	if pushRemote != "origin" || pushLocalRef != batchSHA {
		t.Errorf("push 参数 remote=%q localRef=%q，期望推本批次提交 %q", pushRemote, pushLocalRef, batchSHA)
	}
	if !strings.HasPrefix(pushBranch, "mcode/swarm-lead-123-") {
		t.Errorf("分支名 = %q，应以 mcode/swarm-lead-123- 开头", pushBranch)
	}

	calls := exec.snapshot()
	if len(calls) != 2 {
		t.Fatalf("forge 应被调用 2 次（create + merge），实际 %v", calls)
	}
	if calls[0][0] != "gh" || calls[0][1] != "pr" || calls[0][2] != "create" {
		t.Errorf("第一次调用应为 gh pr create，实际 %v", calls[0])
	}
	if calls[1][0] != "gh" || calls[1][1] != "pr" || calls[1][2] != "merge" {
		t.Errorf("第二次调用应为 gh pr merge，实际 %v", calls[1])
	}
	if len(calls[1]) < 4 || calls[1][3] != prURL {
		t.Errorf("merge 应使用 create 返回的 URL，实际 %v", calls[1])
	}
	if !anyContains(notified, prURL) {
		t.Errorf("通知应包含 PR URL，实际 %v", notified)
	}
	if committed := gitTestRun(t, dir, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD"); committed != "a.txt" {
		t.Errorf("提交内容 = %q, want a.txt", committed)
	}
}

// (d) 手动 Trigger 在两个开关关闭时仍会提交、push 并调用 forge。
func TestPipeline_ManualTriggerIgnoresSwitches(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	gitTestRun(t, dir, "remote", "add", "origin", "https://github.com/x/y.git")
	writePipelineFile(t, dir, "a.txt", "a\n")
	beeBefore := gitTestRun(t, dir, "rev-parse", "HEAD")

	const prURL = "https://github.com/x/y/pull/11"
	exec := &pipelineForgeExec{handler: func(args []string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "pr" && args[1] == "create" {
			return []byte(prURL + "\n"), nil
		}
		return []byte(""), nil
	}}
	var pushCalls int
	p := NewPipeline(PipelineOptions{
		Git:      runner,
		Forge:    git.NewForgeClient(exec.run),
		Settings: func() Settings { return Settings{} },
		Notify:   func(string, string) {},
		Push: func(context.Context, string, string, string, string) error {
			pushCalls++
			return nil
		},
	})

	b := newPipelineFileBatch(dir, Bee{Name: "A", Task: "做 A", ChangedFiles: []string{"a.txt"}, Merged: true})
	addNewFilePatch(t, &b)
	if err := p.Trigger(ctx, []LandedBatch{b}); err != nil {
		t.Fatalf("Trigger failed: %v", err)
	}
	if beeAfter := gitTestRun(t, dir, "rev-parse", "HEAD"); beeAfter == beeBefore {
		t.Error("手动触发应创建本地提交")
	}
	if pushCalls != 1 {
		t.Errorf("手动触发应 push 1 次，实际 %d 次", pushCalls)
	}
	if exec.callCount() != 2 {
		t.Errorf("手动触发应调用 forge 2 次（create + merge），实际 %d 次", exec.callCount())
	}
}

// (e) 可重试的 forge 错误会安排 60s 后的重试；重试触发前关闭 AutoPush 则直接丢弃。
func TestPipeline_RetryScheduledAndDroppedWhenAutoPushOff(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	gitTestRun(t, dir, "remote", "add", "origin", "https://github.com/x/y.git")
	writePipelineFile(t, dir, "a.txt", "a\n")

	settings := Settings{AutoMerge: true, AutoPush: true}
	exec := &pipelineForgeExec{handler: func(args []string) ([]byte, error) {
		return []byte("GraphQL: 405 Checking in progress"), errors.New("exit status 1")
	}}
	type scheduledJob struct {
		delay time.Duration
		fire  func()
	}
	var scheduled []scheduledJob
	var pushCalls int
	var notified []string
	p := NewPipeline(PipelineOptions{
		Git:      runner,
		Forge:    git.NewForgeClient(exec.run),
		Settings: func() Settings { return settings },
		Notify:   func(_, msg string) { notified = append(notified, msg) },
		Push: func(context.Context, string, string, string, string) error {
			pushCalls++
			return nil
		},
		AfterFunc: func(d time.Duration, f func()) func() bool {
			scheduled = append(scheduled, scheduledJob{delay: d, fire: f})
			return func() bool { return true }
		},
	})

	b := newPipelineFileBatch(dir, Bee{Name: "A", Task: "做 A", ChangedFiles: []string{"a.txt"}, Merged: true})
	addNewFilePatch(t, &b)
	if err := p.RunBatch(ctx, b, false); err == nil {
		t.Fatal("可重试失败应返回错误")
	}
	if len(scheduled) != 1 {
		t.Fatalf("应安排 1 个重试任务，实际 %d", len(scheduled))
	}
	if scheduled[0].delay != 60*time.Second {
		t.Errorf("首次重试延迟 = %v, want 60s", scheduled[0].delay)
	}
	if pushCalls != 1 || exec.callCount() != 1 {
		t.Fatalf("首次执行 push=%d forge=%d，期望各 1 次", pushCalls, exec.callCount())
	}

	// 重试触发前关闭 AutoPush：任务应被丢弃
	settings = Settings{AutoMerge: true, AutoPush: false}
	scheduled[0].fire()
	if exec.callCount() != 1 {
		t.Errorf("AutoPush 关闭后不应继续调用 forge，实际 %d 次", exec.callCount())
	}
	if pushCalls != 1 {
		t.Errorf("AutoPush 关闭后不应再次 push，实际 %d 次", pushCalls)
	}
	if !anyContains(notified, "自动推送已关闭") {
		t.Errorf("应通知已取消重试，实际 %v", notified)
	}
}

// (f) 重试触发时 AutoPush 仍开启：从失败阶段恢复，不重复 push 与本地提交。
func TestPipeline_RetryResumesFromFailedStage(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	gitTestRun(t, dir, "remote", "add", "origin", "https://github.com/x/y.git")
	writePipelineFile(t, dir, "a.txt", "a\n")

	const prURL = "https://github.com/x/y/pull/21"
	createCalls := 0
	exec := &pipelineForgeExec{handler: func(args []string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "pr" && args[1] == "create" {
			createCalls++
			if createCalls == 1 {
				return []byte("connection reset by peer"), errors.New("exit status 1")
			}
			return []byte(prURL + "\n"), nil
		}
		return []byte(""), nil
	}}
	var scheduled []func()
	var pushCalls int
	var notified []string
	p := NewPipeline(PipelineOptions{
		Git:      runner,
		Forge:    git.NewForgeClient(exec.run),
		Settings: func() Settings { return Settings{AutoMerge: true, AutoPush: true} },
		Notify:   func(_, msg string) { notified = append(notified, msg) },
		Push: func(context.Context, string, string, string, string) error {
			pushCalls++
			return nil
		},
		AfterFunc: func(d time.Duration, f func()) func() bool {
			scheduled = append(scheduled, f)
			return func() bool { return true }
		},
	})

	b := newPipelineFileBatch(dir, Bee{Name: "A", Task: "做 A", ChangedFiles: []string{"a.txt"}, Merged: true})
	addNewFilePatch(t, &b)
	beeBefore := gitTestRun(t, dir, "rev-parse", "HEAD")
	if err := p.RunBatch(ctx, b, false); err == nil {
		t.Fatal("可重试失败应返回错误")
	}
	if len(scheduled) != 1 {
		t.Fatalf("应安排 1 个重试任务，实际 %d", len(scheduled))
	}
	beeAfter := gitTestRun(t, dir, "rev-parse", "HEAD")

	// 触发重试：应跳过 push 与提交，直接从创建合并请求阶段恢复
	scheduled[0]()
	if pushCalls != 1 {
		t.Errorf("恢复时不应重复 push，实际 %d 次", pushCalls)
	}
	if beeNow := gitTestRun(t, dir, "rev-parse", "HEAD"); beeNow != beeAfter {
		t.Errorf("恢复时不应再次提交: %s -> %s", beeAfter, beeNow)
	}
	if beeAfter == beeBefore {
		t.Error("首次执行应已创建本地提交")
	}
	calls := exec.snapshot()
	if len(calls) != 3 {
		t.Fatalf("期望 create(失败) -> create(成功) -> merge 共 3 次调用，实际 %v", calls)
	}
	if calls[1][2] != "create" || calls[2][2] != "merge" {
		t.Errorf("恢复调用顺序不正确: %v", calls)
	}
	if !anyContains(notified, prURL) {
		t.Errorf("通知应包含 PR URL，实际 %v", notified)
	}
}

// 手动触发且没有可提交改动时，仍然推送当前 HEAD。
func TestPipeline_ManualTriggerWithoutCommitPushesBee(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	gitTestRun(t, dir, "remote", "add", "origin", "https://github.com/x/y.git")
	beeBefore := gitTestRun(t, dir, "rev-parse", "HEAD")

	exec := &pipelineForgeExec{handler: func(args []string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "pr" && args[1] == "create" {
			return []byte("https://github.com/x/y/pull/31\n"), nil
		}
		return []byte(""), nil
	}}
	var pushCalls int
	p := NewPipeline(PipelineOptions{
		Git:      runner,
		Forge:    git.NewForgeClient(exec.run),
		Settings: func() Settings { return Settings{} },
		Notify:   func(string, string) {},
		Push: func(context.Context, string, string, string, string) error {
			pushCalls++
			return nil
		},
	})

	// ChangedFiles 指向的文件没有实际改动 -> 不产生提交
	writePipelineFile(t, dir, "README.md", "# Demo Repo\n")
	b := newPipelineFileBatch(dir, Bee{Name: "A", Task: "无事发生", ChangedFiles: []string{"README.md"}, Merged: true})
	if err := p.Trigger(ctx, []LandedBatch{b}); err != nil {
		t.Fatalf("Trigger failed: %v", err)
	}
	if beeNow := gitTestRun(t, dir, "rev-parse", "HEAD"); beeNow != beeBefore {
		t.Errorf("没有改动时不应创建提交: %s -> %s", beeBefore, beeNow)
	}
	if pushCalls != 1 {
		t.Errorf("手动触发应推送当前 HEAD，实际 push %d 次", pushCalls)
	}
}

func TestBatchesFromState_GroupsByTargetWorkDir(t *testing.T) {
	testenv.TempHome(t)

	st := State{
		LeadThreadID: "lead-1",
		Bees: []Bee{
			{Name: "A", TargetWorkDir: "/p1", ProjectID: "proj-1", Merged: true},
			{Name: "B", TargetWorkDir: "/p1", ProjectID: "proj-1", Merged: true},
			{Name: "C", Merged: true},
			{Name: "D", TargetWorkDir: "/p2", Merged: true},
			{Name: "E", TargetWorkDir: "/p2", Merged: false},
			{Name: "F", TargetWorkDir: "/p1", Merged: true, MergeError: "boom"},
			// 带冲突文件的 bee 不能入批：否则整批会被冲突标记扫描拦下，连干净成员的成果也提交不了。
			{Name: "G", TargetWorkDir: "/p1", Merged: true, ConflictFiles: []string{"shared.go"}},
		},
	}
	batches := BatchesFromState(st, "/lead", "proj-lead")
	if len(batches) != 3 {
		t.Fatalf("期望 3 个批次，实际 %d: %+v", len(batches), batches)
	}

	if batches[0].TargetWorkDir != "/p1" || len(batches[0].Bees) != 2 {
		t.Errorf("第一批次 = %+v，期望 /p1 下 2 个 bees", batches[0])
	}
	for _, h := range batches[0].Bees {
		if h.Name == "G" {
			t.Errorf("带冲突文件的 bee 不应入批: %+v", h)
		}
	}
	if batches[0].ProjectID != "proj-1" {
		t.Errorf("批次 ProjectID = %q, want proj-1", batches[0].ProjectID)
	}
	if batches[1].TargetWorkDir != "/lead" || batches[1].ProjectID != "proj-lead" || len(batches[1].Bees) != 1 {
		t.Errorf("第二批次 = %+v，期望回退 leadWorkDir 与项目 ID", batches[1])
	}
	if batches[2].TargetWorkDir != "/p2" || len(batches[2].Bees) != 1 {
		t.Errorf("第三批次 = %+v，未合并 bees 应被排除", batches[2])
	}
	if batches[0].LeadThreadID != "lead-1" {
		t.Errorf("批次 LeadThreadID = %q", batches[0].LeadThreadID)
	}
}

func TestPipeline_StartStop(t *testing.T) {
	testenv.TempHome(t)
	p := NewPipeline(PipelineOptions{Settings: func() Settings { return Settings{} }})
	p.Start()
	p.Start() // 重复启动应无害
	p.Stop()
	p.Stop() // 重复停止应无害
}

// (g) AutoMerge 开、AutoPush 关：本地提交完成后上报 done，并带上本批次的提交 sha。
func TestPipeline_ReportsDoneAfterLocalCommit(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	writePipelineFile(t, dir, "a.txt", "a\n")

	var results []BatchResult
	p := NewPipeline(PipelineOptions{
		Git:      runner,
		Settings: func() Settings { return Settings{AutoMerge: true} },
		Notify:   func(string, string) {},
		Push:     func(context.Context, string, string, string, string) error { return nil },
		OnResult: func(_ LandedBatch, r BatchResult) { results = append(results, r) },
	})

	b := newPipelineFileBatch(dir, Bee{Name: "A", Task: "做 A", ChangedFiles: []string{"a.txt"}, Merged: true})
	addNewFilePatch(t, &b)
	if err := p.RunBatch(ctx, b, false); err != nil {
		t.Fatalf("RunBatch failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("应上报 1 次结果，实际 %d: %+v", len(results), results)
	}
	if results[0].State != AutoMergeDone {
		t.Errorf("State = %q，期望 %q", results[0].State, AutoMergeDone)
	}
	if want := gitTestRun(t, dir, "rev-parse", "HEAD"); results[0].SHA == "" || results[0].SHA != want {
		t.Errorf("SHA = %q，期望本地提交 %q", results[0].SHA, want)
	}
}

// (h) AutoMerge 关闭：批次不上报 done 而是 skipped，也不产生提交。
func TestPipeline_ReportsSkippedWhenAutoMergeOff(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	writePipelineFile(t, dir, "a.txt", "a\n")
	beeBefore := gitTestRun(t, dir, "rev-parse", "HEAD")

	var results []BatchResult
	p := NewPipeline(PipelineOptions{
		Git:      runner,
		Settings: func() Settings { return Settings{} },
		Notify:   func(string, string) {},
		OnResult: func(_ LandedBatch, r BatchResult) { results = append(results, r) },
	})

	b := newPipelineFileBatch(dir, Bee{Name: "A", Task: "做 A", ChangedFiles: []string{"a.txt"}, Merged: true})
	if err := p.RunBatch(ctx, b, false); err != nil {
		t.Fatalf("RunBatch failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("应上报 1 次结果，实际 %d: %+v", len(results), results)
	}
	if results[0].State != AutoMergeSkipped {
		t.Errorf("State = %q，期望 %q", results[0].State, AutoMergeSkipped)
	}
	if results[0].SHA != "" {
		t.Errorf("skipped 不应带 sha，实际 %q", results[0].SHA)
	}
	if beeNow := gitTestRun(t, dir, "rev-parse", "HEAD"); beeNow != beeBefore {
		t.Errorf("AutoMerge 关闭时不应创建提交: %s -> %s", beeBefore, beeNow)
	}
}

// (i) 补丁提交：用户未提交改动保留在工作区，HEAD 仅合入补丁改动。
func TestPipeline_AutoMergeWithPatchesPreservesUncommittedChanges(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	// 1. 初始化文件 a.txt 并提交到 HEAD
	initialContent := "line 1: base\nline 2: base\nline 3: base\n"
	writePipelineFile(t, dir, "a.txt", initialContent)
	gitTestRun(t, dir, "add", "a.txt")
	gitTestRun(t, dir, "commit", "-m", "init a.txt")

	// 2. 用户在 a.txt 第 1 行做未提交改动，bee 修改第 3 行且改动已落地到工作区
	worktreeContent := "line 1: user modified\nline 2: base\nline 3: bee modified\n"
	writePipelineFile(t, dir, "a.txt", worktreeContent)

	// 3. 构造 bee 落地补丁（修改第 3 行），写入 LandedPatchPath
	patchContent := "--- a/a.txt\n+++ b/a.txt\n@@ -1,3 +1,3 @@\n line 1: base\n line 2: base\n-line 3: base\n+line 3: bee modified\n"
	patchDir := t.TempDir()
	patchPath := filepath.Join(patchDir, "landed.patch")
	if err := os.WriteFile(patchPath, []byte(patchContent), 0644); err != nil {
		t.Fatalf("写入补丁文件失败: %v", err)
	}

	p := NewPipeline(PipelineOptions{
		Git:      runner,
		Settings: func() Settings { return Settings{AutoMerge: true} },
		Notify:   func(string, string) {},
		Push:     func(context.Context, string, string, string, string) error { return nil },
	})

	b := newPipelineFileBatch(dir, Bee{
		Name:            "Bee-1",
		Task:            "修改第 3 行",
		ChangedFiles:    []string{"a.txt"},
		LandedPatchPath: patchPath,
		Merged:          true,
	})

	if err := p.RunBatch(ctx, b, false); err != nil {
		t.Fatalf("RunBatch failed: %v", err)
	}

	// HEAD 中的 a.txt 只多了第 3 行的改动
	headContent := gitTestRun(t, dir, "show", "HEAD:a.txt")
	wantHead := "line 1: base\nline 2: base\nline 3: bee modified"
	if headContent != wantHead {
		t.Errorf("HEAD 中的 a.txt = %q，期望 %q", headContent, wantHead)
	}

	// 工作区仍然保留第 1 行的未提交改动
	worktreeBytes, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatalf("读取工作区 a.txt 失败: %v", err)
	}
	if string(worktreeBytes) != worktreeContent {
		t.Errorf("工作区 a.txt = %q，期望保留用户改动 %q", string(worktreeBytes), worktreeContent)
	}

	// git diff 只显示第 1 行
	diff := gitTestRun(t, dir, "diff")
	hasUserMod := false
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+line 1: user modified") {
			hasUserMod = true
		}
		if strings.HasPrefix(line, "+line 3") || strings.HasPrefix(line, "-line 3") {
			t.Errorf("git diff 不应包含已提交的第 3 行改动，实际 diff:\n%s", diff)
			break
		}
	}
	if !hasUserMod {
		t.Errorf("git diff 应包含第 1 行用户改动，实际 diff:\n%s", diff)
	}
}

// (j) 批次内全为 SharedNoGit 或 ShadowRepo 的 bee 时，不创建提交、不返回错误且不发通知。
func TestPipeline_SkipsNonGitBees(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	writePipelineFile(t, dir, "a.txt", "initial\n")
	gitTestRun(t, dir, "add", "a.txt")
	gitTestRun(t, dir, "commit", "-m", "initial commit")
	beeBefore := gitTestRun(t, dir, "rev-parse", "HEAD")

	var notified []string
	var results []BatchResult
	p := NewPipeline(PipelineOptions{
		Git:      runner,
		Settings: func() Settings { return Settings{AutoMerge: true} },
		Notify:   func(_, msg string) { notified = append(notified, msg) },
		OnResult: func(_ LandedBatch, r BatchResult) { results = append(results, r) },
	})

	b := newPipelineFileBatch(dir,
		Bee{Name: "ShadowBee", Task: "影子项目", ShadowRepo: true, Merged: true},
		Bee{Name: "SharedBee", Task: "共享无git项目", SharedNoGit: true, Merged: true},
	)

	// 自动执行
	if err := p.RunBatch(ctx, b, false); err != nil {
		t.Fatalf("全是非 git 项目时 RunBatch 不应返回错误: %v", err)
	}
	if beeNow := gitTestRun(t, dir, "rev-parse", "HEAD"); beeNow != beeBefore {
		t.Errorf("不应创建提交: %s -> %s", beeBefore, beeNow)
	}
	if len(notified) != 0 {
		t.Errorf("不应发送通知，实际通知: %v", notified)
	}

	// 手动触发
	if err := p.RunBatch(ctx, b, true); err != nil {
		t.Fatalf("全是非 git 项目时手动 RunBatch 不应返回错误: %v", err)
	}
	if beeNow := gitTestRun(t, dir, "rev-parse", "HEAD"); beeNow != beeBefore {
		t.Errorf("手动触发时不应创建提交: %s -> %s", beeBefore, beeNow)
	}
	if len(notified) != 0 {
		t.Errorf("手动触发时不应发送通知，实际通知: %v", notified)
	}
}

// 缺少或无法读取补丁存档时，保留工作区且不创建提交。
func TestPipeline_SkipsUnavailableLandedPatch(t *testing.T) {
	for _, patchPath := range []string{"", "/不存在的补丁"} {
		t.Run(fmt.Sprintf("path=%q", patchPath), func(t *testing.T) {
			runner, dir := initTestRepo(t)
			writePipelineFile(t, dir, "a.txt", "user change\n")
			before := gitTestRun(t, dir, "rev-parse", "HEAD")
			p := NewPipeline(PipelineOptions{Git: runner, Settings: func() Settings { return Settings{AutoMerge: true} }})
			b := newPipelineFileBatch(dir, Bee{Name: "A", Merged: true, ChangedFiles: []string{"a.txt"}, LandedPatchPath: patchPath})
			if err := p.RunBatch(context.Background(), b, false); err != nil {
				t.Fatal(err)
			}
			if got := gitTestRun(t, dir, "rev-parse", "HEAD"); got != before {
				t.Fatalf("HEAD 改变: %s", got)
			}
			data, err := os.ReadFile(filepath.Join(dir, "a.txt"))
			if err != nil || string(data) != "user change\n" {
				t.Fatalf("工作区内容=%q err=%v", data, err)
			}
			if status := gitTestRun(t, dir, "status", "--porcelain"); !strings.Contains(status, "a.txt") {
				t.Fatalf("用户改动未保留: %s", status)
			}
		})
	}
}

func TestPipeline_AlreadyCommittedAndMissingPatch(t *testing.T) {
	runner, dir := initTestRepo(t)
	writePipelineFile(t, dir, "a.txt", "bee change\n")
	gitTestRun(t, dir, "add", "a.txt")
	gitTestRun(t, dir, "commit", "-m", "bee")
	sha := gitTestRun(t, dir, "rev-parse", "HEAD")
	writePipelineFile(t, dir, "a.txt", "user change\n")
	var results []BatchResult
	p := NewPipeline(PipelineOptions{Git: runner, Settings: func() Settings { return Settings{AutoMerge: true} }, OnResult: func(_ LandedBatch, r BatchResult) { results = append(results, r) }})
	b := newPipelineFileBatch(dir, Bee{Name: "已提交", Merged: true, LandedCommit: sha}, Bee{Name: "无补丁", Merged: true, ChangedFiles: []string{"a.txt"}})
	if err := p.RunBatch(context.Background(), b, false); err != nil {
		t.Fatal(err)
	}
	if got := gitTestRun(t, dir, "rev-parse", "HEAD"); got != sha {
		t.Fatalf("HEAD 改变: %s", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || string(data) != "user change\n" {
		t.Fatalf("工作区内容=%q err=%v", data, err)
	}
	if status := gitTestRun(t, dir, "status", "--porcelain"); !strings.Contains(status, "a.txt") {
		t.Fatalf("用户改动未保留: %s", status)
	}
	if len(results) != 1 || results[0].State != AutoMergeDone || results[0].SHA != sha {
		t.Fatalf("结果=%+v", results)
	}
}

// (l) 走补丁提交时，describe 生成提交说明使用补丁文本，不混入工作区未提交改动。
func TestPipeline_DescribeUsesPatchContent(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	writePipelineFile(t, dir, "a.txt", "line 1\nline 2\nline 3\n")
	gitTestRun(t, dir, "add", "a.txt")
	gitTestRun(t, dir, "commit", "-m", "init a.txt")

	// 工作区有混杂的用户未提交改动
	writePipelineFile(t, dir, "a.txt", "user dirty line 1\nline 2\nbee modified line 3\n")

	// bee 补丁只改动第 3 行
	patchContent := "--- a/a.txt\n+++ b/a.txt\n@@ -1,3 +1,3 @@\n line 1\n line 2\n-line 3\n+bee modified line 3\n"
	patchDir := t.TempDir()
	patchPath := filepath.Join(patchDir, "landed.patch")
	if err := os.WriteFile(patchPath, []byte(patchContent), 0644); err != nil {
		t.Fatalf("写入补丁文件失败: %v", err)
	}

	var describeInput string
	p := NewPipeline(PipelineOptions{
		Git:      runner,
		Settings: func() Settings { return Settings{AutoMerge: true} },
		Notify:   func(string, string) {},
		Push:     func(context.Context, string, string, string, string) error { return nil },
		Describe: func(ctx context.Context, diff string) (string, error) {
			describeInput = diff
			return "生成的补丁提交说明", nil
		},
	})

	b := newPipelineFileBatch(dir, Bee{
		Name:            "Bee-Describe",
		Task:            "测试 describe",
		ChangedFiles:    []string{"a.txt"},
		LandedPatchPath: patchPath,
		Merged:          true,
	})
	if err := p.RunBatch(ctx, b, false); err != nil {
		t.Fatalf("RunBatch failed: %v", err)
	}

	if !strings.Contains(describeInput, "bee modified line 3") {
		t.Errorf("describe 输入应包含补丁内容，实际输入: %q", describeInput)
	}
	if strings.Contains(describeInput, "user dirty") {
		t.Errorf("describe 输入不应混入工作区用户的未提交改动，实际输入: %q", describeInput)
	}
	subject := gitTestRun(t, dir, "log", "-1", "--format=%s")
	if subject != "生成的补丁提交说明" {
		t.Errorf("提交说明 = %q, 期望由 describe 生成的说明", subject)
	}
}

// (m) 补丁在 HEAD 上应用冲突且工作区里这些文件已没有改动时，停止合并、发送冲突通知并保留工作区改动。
func TestPipeline_CommitPatchesConflictReportsError(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	writePipelineFile(t, dir, "a.txt", "line 1: conflict base\n")
	gitTestRun(t, dir, "add", "a.txt")
	gitTestRun(t, dir, "commit", "-m", "init a.txt")

	// 补丁是基于另一个完全不匹配的内容生成的
	patchContent := "--- a/a.txt\n+++ b/a.txt\n@@ -1,1 +1,1 @@\n-different base\n+bee changed\n"
	patchDir := t.TempDir()
	patchPath := filepath.Join(patchDir, "conflict.patch")
	if err := os.WriteFile(patchPath, []byte(patchContent), 0644); err != nil {
		t.Fatalf("写入补丁文件失败: %v", err)
	}

	var notified []string
	p := NewPipeline(PipelineOptions{
		Git:      runner,
		Settings: func() Settings { return Settings{AutoMerge: true} },
		Notify:   func(_, msg string) { notified = append(notified, msg) },
		Push:     func(context.Context, string, string, string, string) error { return nil },
	})

	b := newPipelineFileBatch(dir, Bee{
		Name:            "Bee-Conflict",
		Task:            "产生冲突",
		ChangedFiles:    []string{"a.txt"},
		LandedPatchPath: patchPath,
		Merged:          true,
	})
	if err := p.RunBatch(ctx, b, false); err == nil {
		t.Fatal("补丁冲突时 RunBatch 应返回错误")
	}

	if !anyContains(notified, "合并已停止") || !anyContains(notified, "冲突") {
		t.Errorf("应发送合并已停止与冲突相关的通知，实际通知: %v", notified)
	}
}

// setupPatchConflictRepo 准备补丁与 HEAD 冲突的仓库：bee 补丁基于含用户未提交改动的快照生成，
// 工作区里 a.txt 同时有用户改动与 bee 改动，b.txt 另有本批次没碰过的未提交改动。
func setupPatchConflictRepo(t *testing.T) (*git.Runner, string, LandedBatch) {
	t.Helper()
	runner, dir := initTestRepo(t)

	writePipelineFile(t, dir, "a.txt", "line 1: base\n")
	writePipelineFile(t, dir, "b.txt", "b base\n")
	gitTestRun(t, dir, "add", "a.txt", "b.txt")
	gitTestRun(t, dir, "commit", "-m", "init")

	writePipelineFile(t, dir, "a.txt", "line 1: user\nline 2: bee\n")
	writePipelineFile(t, dir, "b.txt", "b user dirty\n")

	patchContent := "--- a/a.txt\n+++ b/a.txt\n@@ -1 +1,2 @@\n line 1: user\n+line 2: bee\n"
	patchPath := filepath.Join(t.TempDir(), "landed.patch")
	if err := os.WriteFile(patchPath, []byte(patchContent), 0644); err != nil {
		t.Fatalf("写入补丁文件失败: %v", err)
	}
	b := newPipelineFileBatch(dir, Bee{
		Name:            "Bee-Dirty",
		Task:            "在有未提交改动的文件上追加一行",
		ChangedFiles:    []string{"a.txt"},
		LandedPatchPath: patchPath,
		Merged:          true,
	})
	return runner, dir, b
}

// (n) 补丁与 HEAD 冲突时停止，工作区内容不能作为回退提交。
func TestPipeline_CommitPatchesConflictFallsBackToWorktree(t *testing.T) {
	testenv.TempHome(t)
	runner, dir, b := setupPatchConflictRepo(t)
	before := gitTestRun(t, dir, "rev-parse", "HEAD")
	index := gitTestRun(t, dir, "write-tree")
	var notified []string
	var results []BatchResult
	p := NewPipeline(PipelineOptions{
		Git:      runner,
		Settings: func() Settings { return Settings{AutoMerge: true} },
		Notify:   func(_, msg string) { notified = append(notified, msg) },
		OnResult: func(_ LandedBatch, r BatchResult) { results = append(results, r) },
	})
	if err := p.RunBatch(context.Background(), b, false); err == nil {
		t.Fatal("补丁冲突应返回错误")
	}
	if after := gitTestRun(t, dir, "rev-parse", "HEAD"); after != before {
		t.Fatal("补丁冲突移动了 HEAD")
	}
	if after := gitTestRun(t, dir, "write-tree"); after != index {
		t.Fatal("补丁冲突改写了 index")
	}
	if got := gitTestRun(t, dir, "diff", "--name-only"); got != "a.txt\nb.txt" {
		t.Fatalf("用户改动未保留: %s", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || string(data) != "line 1: user\nline 2: bee\n" {
		t.Fatalf("工作区被改写: %q err=%v", data, err)
	}
	if !anyContains(notified, "合并已停止") || !anyContains(notified, "需要手动处理") {
		t.Fatalf("通知=%v", notified)
	}
	if anyContains(notified, "按工作区内容提交") {
		t.Fatal("不应按工作区内容提交")
	}
	if len(results) != 1 || results[0].State != AutoMergeFailed {
		t.Fatalf("结果=%+v", results)
	}
}

// (o) 补丁冲突且引用被锁时，仍直接停止，不尝试按工作区提交。
func TestPipeline_CommitPatchesConflictFallbackFailureReportsFailed(t *testing.T) {
	testenv.TempHome(t)
	runner, dir, b := setupPatchConflictRepo(t)
	ctx := context.Background()
	before := gitTestRun(t, dir, "rev-parse", "HEAD")

	// 锁住当前分支引用；补丁冲突分支不应尝试移动它
	ref := gitTestRun(t, dir, "symbolic-ref", "HEAD")
	lockPath := filepath.Join(dir, ".git", filepath.FromSlash(ref)+".lock")
	if err := os.WriteFile(lockPath, nil, 0644); err != nil {
		t.Fatalf("创建引用锁失败: %v", err)
	}

	var notified []string
	var results []BatchResult
	p := NewPipeline(PipelineOptions{
		Git:      runner,
		Settings: func() Settings { return Settings{AutoMerge: true} },
		Notify:   func(_, msg string) { notified = append(notified, msg) },
		OnResult: func(_ LandedBatch, r BatchResult) { results = append(results, r) },
		Push:     func(context.Context, string, string, string, string) error { return nil },
	})
	err := p.RunBatch(ctx, b, false)
	if err == nil {
		t.Fatal("回退提交失败时 RunBatch 应返回错误")
	}
	if !strings.Contains(err.Error(), "补丁应用存在冲突") {
		t.Errorf("错误应说明补丁冲突，实际: %v", err)
	}

	if !anyContains(notified, "合并已停止") || !anyContains(notified, "需要手动处理") {
		t.Errorf("应发送合并已停止且需要手动处理的通知，实际通知: %v", notified)
	}
	if anyContains(notified, "按工作区内容提交") {
		t.Errorf("回退失败时不应发送回退成功通知，实际通知: %v", notified)
	}
	if len(results) != 1 || results[0].State != AutoMergeFailed || !strings.Contains(results[0].Err, "补丁应用存在冲突") {
		t.Errorf("results = %+v, want 一次 failed 且 Err 说明补丁冲突", results)
	}

	if err := os.Remove(lockPath); err != nil {
		t.Fatalf("移除引用锁失败: %v", err)
	}
	if after := gitTestRun(t, dir, "rev-parse", "HEAD"); after != before {
		t.Errorf("回退失败时不应移动 HEAD: %s -> %s", before, after)
	}
	data, rerr := os.ReadFile(filepath.Join(dir, "a.txt"))
	if rerr != nil {
		t.Fatalf("读取工作区 a.txt 失败: %v", rerr)
	}
	if string(data) != "line 1: user\nline 2: bee\n" {
		t.Errorf("工作区 a.txt = %q，改动应保留", string(data))
	}
}

func TestPipeline_AllBeesAlreadyCommitted(t *testing.T) {
	runner, root := initTestRepo(t)
	sha := gitTestRun(t, root, "rev-parse", "HEAD")
	// 将 bee 路径替换为目录；若读取工作区或扫描标记就会失败。
	if err := os.Mkdir(filepath.Join(root, "unreadable.txt"), 0755); err != nil {
		t.Fatal(err)
	}
	var results []BatchResult
	var notices []string
	p := NewPipeline(PipelineOptions{
		Git:      runner,
		Settings: func() Settings { return Settings{AutoMerge: true} },
		Describe: func(context.Context, string) (string, error) {
			t.Fatal("已提交批次不应读取 diff")
			return "", nil
		},
		Notify:   func(_, msg string) { notices = append(notices, msg) },
		OnResult: func(_ LandedBatch, result BatchResult) { results = append(results, result) },
	})
	b := newPipelineFileBatch(root, Bee{Name: "Bee", Merged: true, LandedCommit: sha, ChangedFiles: []string{"unreadable.txt"}, LandedPatchPath: "/不存在的补丁"})
	if err := p.RunBatch(context.Background(), b, false); err != nil {
		t.Fatal(err)
	}
	if got := gitTestRun(t, root, "rev-parse", "HEAD"); got != sha {
		t.Fatal("重复创建了提交")
	}
	if len(results) != 1 || results[0].State != AutoMergeDone || results[0].SHA != sha {
		t.Fatalf("结果=%+v", results)
	}
	if !anyContains(notices, "1 个提交") || !anyContains(notices, shortSHA(sha)) {
		t.Fatalf("通知=%v", notices)
	}
}

// defaultCommitMessage 按改动性质生成 Conventional Commits 类型前缀。
func TestDefaultCommitMessageType(t *testing.T) {
	tests := []struct {
		name string
		bees []Bee
		want string
	}{
		{
			name: "修复类任务",
			bees: []Bee{{Name: "A", Task: "修复登录崩溃", ChangedFiles: []string{"internal/app.go"}}},
			want: "fix: 修复登录崩溃",
		},
		{
			name: "只改测试文件",
			bees: []Bee{{Name: "A", Task: "补齐断言", ChangedFiles: []string{"internal/app_test.go"}}},
			want: "test: 补齐断言",
		},
		{
			name: "格式化任务",
			bees: []Bee{{Name: "A", Task: "gofmt 清理", ChangedFiles: []string{"internal/app.go"}}},
			want: "style: gofmt 清理",
		},
		{
			name: "任务自带带 scope 的前缀",
			bees: []Bee{{Name: "A", Task: "fix(ui): 弹出层偏移", ChangedFiles: []string{"internal/ui/popover.go"}}},
			want: "fix: 弹出层偏移",
		},
		{
			name: "feat 与 fix 混合取最高优先级",
			bees: []Bee{
				{Name: "A", Task: "新增导出接口", ChangedFiles: []string{"internal/api.go"}},
				{Name: "B", Task: "修复空指针", ChangedFiles: []string{"internal/b.go"}},
			},
			want: "feat: 新增导出接口; 修复空指针",
		},
		{
			name: "没有任务文本",
			bees: []Bee{{}},
			want: "chore: 合并子代理改动",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subject, _ := defaultCommitMessage(tt.bees)
			if subject != tt.want {
				t.Fatalf("首行 = %q, want %q", subject, tt.want)
			}
		})
	}
}
