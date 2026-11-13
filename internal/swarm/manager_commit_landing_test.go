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

	"mcode/internal/git"
	"mcode/internal/kanban"
)

const landingBase = "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\n"

func newCommitLandingManager(t *testing.T) (*Manager, *fakeDriver, string) {
	t.Helper()
	runner, root := initTestRepo(t)
	writePipelineFile(t, root, "lines.txt", landingBase)
	gitTestRun(t, root, "add", "lines.txt")
	gitTestRun(t, root, "commit", "-qm", "base lines")
	driver := newFakeDriver()
	m := NewManager(Options{Git: runner, Driver: driver, WorktreeDir: filepath.Join(t.TempDir(), "worktrees")})
	m.SetSettingsFunc(func() Settings { return Settings{AutoMerge: true} })
	return m, driver, root
}

func startCommitLandingBee(t *testing.T, m *Manager, root, lead string, index int) Bee {
	t.Helper()
	bee, err := m.startSingleBee(context.Background(), lead, "project", root, BeeSpec{Name: fmt.Sprintf("Bee-%d", index), Task: "修改文本", Prompt: "修改文本"}, index, lead)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	if m.leads[lead] == nil {
		m.leads[lead] = &leadState{leadThreadID: lead, leadWorkDir: root, enabled: true}
	}
	m.leads[lead].bees = append(m.leads[lead].bees, bee)
	m.mu.Unlock()
	return bee
}

func landCommitBee(m *Manager, root, lead string, bee *Bee) bool {
	mu := m.getLeadMergeMutex(lead)
	mu.Lock()
	defer mu.Unlock()
	return m.mergeBee(context.Background(), lead, root, bee)
}

func assertLandingFile(t *testing.T, root, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "lines.txt"))
	if err != nil || string(data) != want {
		t.Fatalf("文件=%q want=%q err=%v", data, want, err)
	}
}

func assertLandingCommit(t *testing.T, m *Manager, root string, bee Bee) {
	t.Helper()
	if !bee.Merged || bee.LandedCommit == "" || bee.LandedPatchPath == "" {
		t.Fatalf("落地失败: %+v", bee)
	}
	if got := gitTestRun(t, root, "rev-parse", "HEAD"); got != bee.LandedCommit {
		t.Fatalf("HEAD=%s want=%s", got, bee.LandedCommit)
	}
}

func TestManager_CommitLanding_Describe(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message string
		err     error
		nilFunc bool
	}{
		{name: "生成成功", message: "  fix: 更新文本\n\n保留提交正文\n  "},
		{name: "生成失败", message: "不可使用", err: fmt.Errorf("模型不可用")},
		{name: "空说明", message: " \n\t"},
		{name: "未配置", nilFunc: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, root := newCommitLandingManager(t)
			const lead = "lead-describe"
			bee := startCommitLandingBee(t, m, root, lead, 0)
			writePipelineFile(t, root, "lines.txt", strings.Replace(landingBase, "one\n", "user one\n", 1))
			writePipelineFile(t, bee.WorktreePath, "lines.txt", strings.Replace(landingBase, "nine\n", "bee nine\n", 1))
			// 预先建好锁，回调中直接检查是否被持有。
			lockHead(root)()
			called := false
			if !tc.nilFunc {
				m.opts.Describe = func(ctx context.Context, diff string) (string, error) {
					called = true
					deadline, ok := ctx.Deadline()
					if left := time.Until(deadline); !ok || left <= 0 || left > 30*time.Second {
						t.Errorf("生成超时未限制在 30 秒内: %v, %v", deadline, ok)
					}
					if !strings.Contains(diff, "+bee nine") || strings.Contains(diff, "+user one") {
						t.Errorf("生成输入未隔离 bee 改动: %s", diff)
					}
					value, _ := headLocks.Load(filepath.Clean(root))
					mu := value.(*sync.Mutex)
					if !mu.TryLock() {
						t.Error("生成时持有 HEAD 锁")
					} else {
						mu.Unlock()
					}
					if !m.mu.TryLock() {
						t.Error("生成时持有 Manager 状态锁")
					} else {
						m.mu.Unlock()
					}
					return tc.message, tc.err
				}
			}
			if !landCommitBee(m, root, lead, &bee) {
				t.Fatal("落地不应等待")
			}
			assertLandingCommit(t, m, root, bee)
			_, want := defaultCommitMessage([]Bee{bee})
			if tc.err == nil && strings.TrimSpace(tc.message) != "" {
				want = strings.TrimSpace(tc.message)
			}
			if got := gitTestRun(t, root, "show", "-s", "--format=%B", bee.LandedCommit); got != strings.TrimSpace(want) {
				t.Fatalf("提交说明=%q want=%q", got, want)
			}
			if called == tc.nilFunc {
				t.Fatalf("生成调用=%v nil=%v", called, tc.nilFunc)
			}
		})
	}
}

func TestManager_CommitLanding_DescribeWaitReuse(t *testing.T) {
	m, _, root := newCommitLandingManager(t)
	const lead = "lead-describe-wait"
	const message = "fix: 更新文本"
	bee := startCommitLandingBee(t, m, root, lead, 0)
	writePipelineFile(t, root, "lines.txt", strings.Replace(landingBase, "nine\n", "user nine\n", 1))
	writePipelineFile(t, bee.WorktreePath, "lines.txt", strings.Replace(landingBase, "nine\n", "bee nine\n", 1))
	calls := 0
	m.opts.Describe = func(context.Context, string) (string, error) {
		calls++
		return message, nil
	}
	for i := 0; i < 2; i++ {
		if landCommitBee(m, root, lead, &bee) {
			t.Fatal("冲突时应等待")
		}
	}
	if calls != 1 {
		t.Fatalf("生成调用次数=%d want=1", calls)
	}
	writePipelineFile(t, root, "lines.txt", landingBase)
	if !landCommitBee(m, root, lead, &bee) {
		t.Fatal("冲突解除后应落地")
	}
	assertLandingCommit(t, m, root, bee)
	if calls != 1 {
		t.Fatalf("生成调用次数=%d want=1", calls)
	}
	if got := gitTestRun(t, root, "show", "-s", "--format=%B", bee.LandedCommit); got != message {
		t.Fatalf("提交说明=%q want=%q", got, message)
	}
}

func TestManager_CommitLanding_DescribeHeadMoved(t *testing.T) {
	m, _, root := newCommitLandingManager(t)
	const lead = "lead-describe-head"
	bee := startCommitLandingBee(t, m, root, lead, 0)
	writePipelineFile(t, bee.WorktreePath, "lines.txt", strings.Replace(landingBase, "nine\n", "bee nine\n", 1))
	var movedHead string
	m.opts.Describe = func(context.Context, string) (string, error) {
		unlock := lockHead(root)
		defer unlock()
		writePipelineFile(t, root, "other.txt", "other lead\n")
		gitTestRun(t, root, "add", "other.txt")
		gitTestRun(t, root, "commit", "-qm", "其他 lead 提交")
		movedHead = gitTestRun(t, root, "rev-parse", "HEAD")
		return "fix: 更新文本", nil
	}
	if !landCommitBee(m, root, lead, &bee) {
		t.Fatal("落地不应等待")
	}
	assertLandingCommit(t, m, root, bee)
	if parent := gitTestRun(t, root, "rev-parse", "HEAD^"); parent != movedHead {
		t.Fatalf("落地父提交=%s want=%s", parent, movedHead)
	}
	if got := gitTestRun(t, root, "show", "HEAD:other.txt"); got != "other lead" {
		t.Fatalf("其他 lead 改动丢失: %s", got)
	}
}

func TestManager_CommitLanding_DescribeBoundedDiff(t *testing.T) {
	m, _, root := newCommitLandingManager(t)
	const lead = "lead-describe-limit"
	bee := startCommitLandingBee(t, m, root, lead, 0)
	writePipelineFile(t, bee.WorktreePath, "lines.txt", strings.Repeat("新增文本\n", 4000))
	called := false
	m.opts.Describe = func(_ context.Context, diff string) (string, error) {
		called = true
		if len(diff) > 12000 || !strings.Contains(diff, "+新增文本") || !strings.HasSuffix(diff, "\n") {
			t.Errorf("diff 未按完整行截断: 字节数=%d", len(diff))
		}
		return "feat: 新增文本", nil
	}
	if !landCommitBee(m, root, lead, &bee) {
		t.Fatal("落地不应等待")
	}
	assertLandingCommit(t, m, root, bee)
	if !called {
		t.Fatal("未生成提交说明")
	}
}

func TestManager_CommitLanding_DescribeTimeout(t *testing.T) {
	m, _, root := newCommitLandingManager(t)
	const lead = "lead-describe-timeout"
	bee := startCommitLandingBee(t, m, root, lead, 0)
	writePipelineFile(t, bee.WorktreePath, "lines.txt", strings.Replace(landingBase, "nine\n", "bee nine\n", 1))
	called := false
	m.opts.Describe = func(ctx context.Context, _ string) (string, error) {
		called = true
		<-ctx.Done()
		// 超时后的响应即使没有返回错误，也不能覆盖默认说明。
		return "超时响应", nil
	}
	if !landCommitBee(m, root, lead, &bee) {
		t.Fatal("生成超时后应继续落地")
	}
	assertLandingCommit(t, m, root, bee)
	_, want := defaultCommitMessage([]Bee{bee})
	if got := gitTestRun(t, root, "show", "-s", "--format=%B", bee.LandedCommit); got != strings.TrimSpace(want) {
		t.Fatalf("提交说明=%q want=%q", got, want)
	}
	if !called {
		t.Fatal("未调用生成器")
	}
}

func TestManager_CommitLanding_TwoBees(t *testing.T) {
	m, _, root := newCommitLandingManager(t)
	before := gitTestRun(t, root, "rev-parse", "HEAD")
	a := startCommitLandingBee(t, m, root, "lead-two", 0)
	b := startCommitLandingBee(t, m, root, "lead-two", 1)
	aContent := strings.Replace(landingBase, "one\n", "bee one\n", 1)
	bContent := strings.Replace(landingBase, "nine\n", "bee nine\n", 1)
	writePipelineFile(t, a.WorktreePath, "lines.txt", aContent)
	writePipelineFile(t, b.WorktreePath, "lines.txt", bContent)
	if !landCommitBee(m, root, "lead-two", &a) {
		t.Fatal("首个 bee 不应等待")
	}
	assertLandingCommit(t, m, root, a)
	if got := gitTestRun(t, root, "show", a.LandedCommit+":lines.txt"); got != strings.TrimSpace(aContent) {
		t.Fatal("首个提交含其他改动")
	}
	if !landCommitBee(m, root, "lead-two", &b) {
		t.Fatal("第二个 bee 不应等待")
	}
	assertLandingCommit(t, m, root, b)
	if got := gitTestRun(t, root, "diff", a.LandedCommit, b.LandedCommit); strings.Contains(got, "+bee one") || !strings.Contains(got, "+bee nine") {
		t.Fatalf("第二个提交的改动不独立: %s", got)
	}
	if got := gitTestRun(t, root, "rev-list", "--count", before+"..HEAD"); got != "2" {
		t.Fatalf("提交数=%s", got)
	}
	if got := gitTestRun(t, root, "status", "--porcelain"); got != "" {
		t.Fatalf("工作区不干净: %s", got)
	}
}

func TestManager_CommitLanding_Handback(t *testing.T) {
	m, _, root := newCommitLandingManager(t)
	before := gitTestRun(t, root, "rev-parse", "HEAD")
	a := startCommitLandingBee(t, m, root, "lead-conflict", 0)
	b := startCommitLandingBee(t, m, root, "lead-conflict", 1)
	first := strings.Replace(landingBase, "one\n", "first\n", 1)
	writePipelineFile(t, a.WorktreePath, "lines.txt", first)
	writePipelineFile(t, b.WorktreePath, "lines.txt", strings.Replace(landingBase, "one\n", "second\n", 1))
	if !landCommitBee(m, root, "lead-conflict", &a) {
		t.Fatal("首个 bee 等待")
	}
	if !landCommitBee(m, root, "lead-conflict", &b) || b.Merged || b.ConflictHandbacks != 1 {
		t.Fatalf("未交回: %+v", b)
	}
	if b.BaseCommit != a.LandedCommit {
		t.Fatalf("交回基线=%s want=%s", b.BaseCommit, a.LandedCommit)
	}
	if got := gitTestRun(t, root, "rev-list", "--count", before+"..HEAD"); got != "1" {
		t.Fatalf("提交数=%s", got)
	}
	assertLandingFile(t, root, first)
	if got := gitTestRun(t, root, "status", "--porcelain"); got != "" {
		t.Fatalf("工作区不干净: %s", got)
	}
}

func TestManager_CommitLanding_UserChanges(t *testing.T) {
	for _, staged := range []bool{false, true} {
		t.Run(fmt.Sprintf("staged=%v", staged), func(t *testing.T) {
			m, _, root := newCommitLandingManager(t)
			var reports []string
			m.opts.OnReport = func(_, message string) error { reports = append(reports, message); return nil }
			bee := startCommitLandingBee(t, m, root, "lead-user", 0)
			user := strings.Replace(landingBase, "one\n", "user one\n", 1)
			writePipelineFile(t, root, "lines.txt", user)
			var index string
			if staged {
				gitTestRun(t, root, "add", "lines.txt")
				index = gitTestRun(t, root, "rev-parse", ":lines.txt")
			}
			writePipelineFile(t, bee.WorktreePath, "lines.txt", strings.Replace(landingBase, "nine\n", "bee nine\n", 1))
			if !landCommitBee(m, root, "lead-user", &bee) {
				t.Fatal("不同行改动不应等待")
			}
			assertLandingCommit(t, m, root, bee)
			if got := gitTestRun(t, root, "show", "HEAD:lines.txt"); strings.Contains(got, "user one") || !strings.Contains(got, "bee nine") {
				t.Fatalf("提交混入用户改动: %s", got)
			}
			assertLandingFile(t, root, strings.Replace(user, "nine\n", "bee nine\n", 1))
			if staged {
				if got := gitTestRun(t, root, "rev-parse", ":lines.txt"); got != index {
					t.Fatal("暂存内容被改写")
				}
				if !anyContains(reports, "未同步暂存区") {
					t.Fatalf("缺少暂存保留通知: %v", reports)
				}
			} else if got := gitTestRun(t, root, "diff", "--cached"); got != "" {
				t.Fatalf("用户改动不应被暂存: %s", got)
			}
		})
	}
}

func TestManager_CommitLanding_UserConflictWaitsForTick(t *testing.T) {
	m, driver, root := newCommitLandingManager(t)
	bee := startCommitLandingBee(t, m, root, "lead-wait", 0)
	before := gitTestRun(t, root, "rev-parse", "HEAD")
	user := strings.Replace(landingBase, "one\n", "user\n", 1)
	writePipelineFile(t, root, "lines.txt", user)
	beeText := strings.Replace(landingBase, "one\n", "bee\n", 1)
	writePipelineFile(t, bee.WorktreePath, "lines.txt", beeText)
	driver.setSessionState(bee.ThreadID, kanban.SessionState{Running: false, LastAssistantText: "完成"})
	m.Tick(context.Background())
	got := m.State("lead-wait").Bees[0]
	if got.Status != StatusRunning || got.MergeWaitSince == nil || !strings.Contains(got.MergeError, "未提交改动冲突") {
		t.Fatalf("未保持 merge-wait: %+v", got)
	}
	if head := gitTestRun(t, root, "rev-parse", "HEAD"); head != before {
		t.Fatal("等待时移动了 HEAD")
	}
	assertLandingFile(t, root, user)
	writePipelineFile(t, root, "lines.txt", landingBase)
	m.Tick(context.Background())
	got = m.State("lead-wait").Bees[0]
	assertLandingCommit(t, m, root, got)
	assertLandingFile(t, root, beeText)
}

func TestManager_CommitLanding_BaseIsHead(t *testing.T) {
	m, _, root := newCommitLandingManager(t)
	head := gitTestRun(t, root, "rev-parse", "HEAD")
	writePipelineFile(t, root, "lines.txt", "user dirty\n")
	writePipelineFile(t, root, "user.txt", "untracked\n")
	bee := startCommitLandingBee(t, m, root, "lead-base", 0)
	if bee.BaseCommit != head {
		t.Fatalf("基线=%s want=%s", bee.BaseCommit, head)
	}
	assertLandingFile(t, bee.WorktreePath, landingBase)
	if _, err := os.Stat(filepath.Join(bee.WorktreePath, "user.txt")); !os.IsNotExist(err) {
		t.Fatalf("用户未提交文件进入 bee 副本: %v", err)
	}
}

func TestManager_CommitLanding_HeadMoved(t *testing.T) {
	m, _, root := newCommitLandingManager(t)
	bee := startCommitLandingBee(t, m, root, "lead-cas", 0)
	old := gitTestRun(t, root, "rev-parse", "HEAD")
	tree := gitTestRun(t, root, "rev-parse", "HEAD^{tree}")
	other, err := m.opts.Git.CommitTree(context.Background(), root, tree, old, "外部提交")
	if err != nil {
		t.Fatal(err)
	}
	realGit := m.opts.Git.Bin
	scriptDir := t.TempDir()
	flag := filepath.Join(scriptDir, "once")
	if err := os.WriteFile(flag, nil, 0600); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(scriptDir, "git")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = update-ref ] && [ "$4" = HEAD ] && [ -f %q ]; then
 rm %q
 %q update-ref HEAD %q "$6" || exit 1
fi
exec %q "$@"
`, flag, flag, realGit, other, realGit)
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	m.opts.Git = git.NewRunner(wrapper, 4)
	beeText := strings.Replace(landingBase, "one\n", "bee\n", 1)
	writePipelineFile(t, bee.WorktreePath, "lines.txt", beeText)
	if landCommitBee(m, root, "lead-cas", &bee) || bee.MergeWaitSince == nil {
		t.Fatalf("CAS 失败应等待: %+v", bee)
	}
	if got := gitTestRun(t, root, "rev-parse", "HEAD"); got != other {
		t.Fatalf("HEAD=%s want=%s", got, other)
	}
	assertLandingFile(t, root, landingBase)
	if got := gitTestRun(t, root, "status", "--porcelain"); got != "" {
		t.Fatalf("CAS 失败污染 index: %s", got)
	}
	if !landCommitBee(m, root, "lead-cas", &bee) {
		t.Fatal("重试仍等待")
	}
	assertLandingCommit(t, m, root, bee)
	assertLandingFile(t, root, beeText)
}

func TestManager_CommitLanding_SerialAcrossManagers(t *testing.T) {
	m, _, root := newCommitLandingManager(t)
	second := NewManager(Options{Git: m.opts.Git, Driver: newFakeDriver(), WorktreeDir: filepath.Join(t.TempDir(), "worktrees")})
	second.SetSettingsFunc(func() Settings { return Settings{AutoMerge: true} })
	a := startCommitLandingBee(t, m, root, "lead-a", 0)
	b := startCommitLandingBee(t, second, root, "lead-b", 0)
	before := gitTestRun(t, root, "rev-parse", "HEAD")
	writePipelineFile(t, a.WorktreePath, "lines.txt", strings.Replace(landingBase, "one\n", "bee one\n", 1))
	writePipelineFile(t, b.WorktreePath, "lines.txt", strings.Replace(landingBase, "nine\n", "bee nine\n", 1))
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); landCommitBee(m, root, "lead-a", &a) }()
	go func() { defer wg.Done(); landCommitBee(second, root, "lead-b", &b) }()
	wg.Wait()
	if !a.Merged || !b.Merged {
		t.Fatalf("落地失败: a=%+v b=%+v", a, b)
	}
	if got := gitTestRun(t, root, "rev-list", "--count", before+"..HEAD"); got != "2" {
		t.Fatalf("提交数=%s", got)
	}
	if got := gitTestRun(t, root, "status", "--porcelain"); got != "" {
		t.Fatalf("工作区不干净: %s", got)
	}
}

func TestManager_CommitLanding_ConflictMarkersAndTimeout(t *testing.T) {
	m, _, root := newCommitLandingManager(t)
	now := time.Now()
	m.opts.Now = func() time.Time { return now }
	m.opts.SettleTimeout = time.Minute
	bee := startCommitLandingBee(t, m, root, "lead-marker", 0)
	before := gitTestRun(t, root, "rev-parse", "HEAD")
	markers := strings.Replace(landingBase, "one\n", "<<<<<<< user\na\n=======\nb\n>>>>>>> other\n", 1)
	writePipelineFile(t, root, "lines.txt", markers)
	writePipelineFile(t, bee.WorktreePath, "lines.txt", strings.Replace(landingBase, "nine\n", "bee\n", 1))
	if landCommitBee(m, root, "lead-marker", &bee) {
		t.Fatal("冲突标记应等待")
	}
	assertLandingFile(t, root, markers)
	now = now.Add(2 * time.Minute)
	if !landCommitBee(m, root, "lead-marker", &bee) || bee.Merged || bee.MergeWaitSince != nil {
		t.Fatalf("超时未结束: %+v", bee)
	}
	if got := gitTestRun(t, root, "rev-parse", "HEAD"); got != before {
		t.Fatal("超时移动 HEAD")
	}
	assertLandingFile(t, root, markers)
}
