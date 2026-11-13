package swarm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"mcode/internal/git"
	"mcode/internal/testenv"
)

// reclaimFixture 搭一个用真实 git 仓库与注入时钟的 Manager，副本一律建在 WorktreeDir 之下。
type reclaimFixture struct {
	mgr     *Manager
	runner  *git.Runner
	repoDir string
	wtRoot  string
	clock   *testClock
	leadID  string
}

func newReclaimFixture(t *testing.T) *reclaimFixture {
	t.Helper()
	testenv.TempHome(t)
	clock := newTestClock()
	runner, repoDir := initTestRepo(t)
	wtRoot := t.TempDir()
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      newFakeDriver(),
		WorktreeDir: wtRoot,
		Now:         clock.Now,
		Persister:   newFakePersister(),
	})
	return &reclaimFixture{mgr: mgr, runner: runner, repoDir: repoDir, wtRoot: wtRoot, clock: clock, leadID: "lead-reclaim"}
}

// addCopy 在 WorktreeDir 下建一个真实 worktree，返回其路径与 HEAD 提交。
func (f *reclaimFixture) addCopy(t *testing.T, name, branch string) (string, string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(f.wtRoot, name)
	if err := f.runner.WorktreeAdd(ctx, f.repoDir, path, branch, "HEAD"); err != nil {
		t.Fatalf("worktree add %s: %v", path, err)
	}
	head, err := f.runner.RevParse(ctx, path, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD in %s: %v", path, err)
	}
	return path, head
}

func (f *reclaimFixture) setBees(bees ...Bee) {
	f.mgr.mu.Lock()
	defer f.mgr.mu.Unlock()
	f.mgr.leads[f.leadID] = &leadState{leadThreadID: f.leadID, leadWorkDir: f.repoDir, bees: bees}
}

func (f *reclaimFixture) appendBee(h Bee) {
	f.mgr.mu.Lock()
	defer f.mgr.mu.Unlock()
	lead := f.mgr.leads[f.leadID]
	lead.bees = append(lead.bees, h)
}

func (f *reclaimFixture) bee(t *testing.T, threadID string) Bee {
	t.Helper()
	return beeByThread(t, f.mgr, f.leadID, threadID)
}

func (f *reclaimFixture) finishedAtAgo(d time.Duration) *time.Time {
	at := f.clock.Now().Add(-d)
	return &at
}

// assertCopyGone 校验副本目录已删除、分支已删除。
func (f *reclaimFixture) assertCopyGone(t *testing.T, path, branch string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("期望副本 %s 已删除，stat err: %v", path, err)
	}
	if branch == "" {
		return
	}
	if _, err := f.runner.RevParse(context.Background(), f.repoDir, "refs/heads/"+branch); err == nil {
		t.Errorf("期望分支 %s 已删除", branch)
	}
}

// 已合并且有落地补丁存档的 bee，静置超过 10 分钟后副本被回收：目录与分支消失，WorktreePath/Branch 清空。
func TestManager_ReclaimBeeCopies_MergedWithLandedPatch(t *testing.T) {
	f := newReclaimFixture(t)
	path, _ := f.addCopy(t, "lead-reclaim-0", "mcode/test-reclaim/0")

	landedPath := filepath.Join(t.TempDir(), "landed.patch")
	if err := os.WriteFile(landedPath, []byte("diff --git a/README.md b/README.md\n"), 0644); err != nil {
		t.Fatalf("write landed patch: %v", err)
	}

	f.setBees(Bee{
		ThreadID:        "bee-merged",
		Index:           0,
		Status:          StatusCompleted,
		Merged:          true,
		ChangedFiles:    []string{"README.md"},
		LandedPatchPath: landedPath,
		WorktreePath:    path,
		Branch:          "mcode/test-reclaim/0",
		FinishedAt:      f.finishedAtAgo(11 * time.Minute),
	})

	f.mgr.reclaimBeeCopies(context.Background())

	f.assertCopyGone(t, path, "mcode/test-reclaim/0")
	h := f.bee(t, "bee-merged")
	if h.WorktreePath != "" || h.Branch != "" {
		t.Fatalf("回收后应清空副本引用，实际 path=%q branch=%q", h.WorktreePath, h.Branch)
	}
	if !h.Merged {
		t.Error("回收只清理副本，不应改变合并标记")
	}
}

// 刚结束 5 分钟的 bee 副本保留，静置满 10 分钟后才回收。
func TestManager_ReclaimBeeCopies_SkipsRecentlyFinished(t *testing.T) {
	f := newReclaimFixture(t)
	path, _ := f.addCopy(t, "lead-reclaim-0", "mcode/test-reclaim/0")

	f.setBees(Bee{
		ThreadID:     "bee-fresh",
		Status:       StatusCompleted,
		Merged:       true,
		WorktreePath: path,
		Branch:       "mcode/test-reclaim/0",
		FinishedAt:   f.finishedAtAgo(5 * time.Minute),
	})

	f.mgr.reclaimBeeCopies(context.Background())

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("静置不足 10 分钟不应回收副本，stat err: %v", err)
	}
	if h := f.bee(t, "bee-fresh"); h.WorktreePath != path {
		t.Fatalf("不应清空副本引用，实际 %q", h.WorktreePath)
	}

	// 满 10 分钟后同一 bee 可以被回收。
	f.clock.Advance(6 * time.Minute)
	f.mgr.reclaimBeeCopies(context.Background())
	f.assertCopyGone(t, path, "mcode/test-reclaim/0")
}

// 各种「成果可能还没落地」的状态都不回收副本。
func TestManager_ReclaimBeeCopies_SkipsUnsafeBees(t *testing.T) {
	f := newReclaimFixture(t)
	f.setBees()

	cases := []struct {
		name string
		bee  Bee
	}{
		{"待审查", Bee{Status: StatusCompleted, PendingMerge: true}},
		{"有合并错误", Bee{Status: StatusCompleted, Merged: true, MergeError: "git apply failed"}},
		{"有冲突文件", Bee{Status: StatusCompleted, ConflictFiles: []string{"a.go"}}},
		{"重启被打断", Bee{Status: StatusStopped, Merged: true, Interrupted: true}},
		{"仍在运行", Bee{Status: StatusRunning, Merged: true}},
		{"已合并但改动无存档", Bee{Status: StatusCompleted, Merged: true, ChangedFiles: []string{"a.go"}}},
		{"未合并已停止", Bee{Status: StatusStopped, ChangedFiles: []string{"a.go"}}},
		{"只读但改动了文件", Bee{Status: StatusCompleted, ReadOnly: true, ReadOnlyViolation: true, ChangedFiles: []string{"a.go"}}},
	}

	for i := range cases {
		tag := fmt.Sprintf("unsafe%d", i)
		path, _ := f.addCopy(t, "lead-"+tag, "mcode/test-"+tag)
		cases[i].bee.ThreadID = "bee-" + tag
		cases[i].bee.Index = i
		cases[i].bee.WorktreePath = path
		cases[i].bee.Branch = "mcode/test-" + tag
		cases[i].bee.FinishedAt = f.finishedAtAgo(11 * time.Minute)
		f.appendBee(cases[i].bee)
	}

	f.mgr.reclaimBeeCopies(context.Background())

	for _, c := range cases {
		if _, err := os.Stat(c.bee.WorktreePath); err != nil {
			t.Errorf("%s：不应回收副本，stat err: %v", c.name, err)
		}
		if h := f.bee(t, c.bee.ThreadID); h.WorktreePath != c.bee.WorktreePath {
			t.Errorf("%s：不应清空副本引用，实际 %q", c.name, h.WorktreePath)
		}
	}
}

func TestManager_ReclaimBeeCopies_ArchivesStaleUnmerged(t *testing.T) {
	cases := []struct {
		name       string
		bee        Bee
		useStarted bool
	}{
		{name: "stopped", bee: Bee{Status: StatusStopped}},
		{name: "completed", bee: Bee{Status: StatusCompleted}},
		{name: "failed", bee: Bee{Status: StatusFailed}},
		{name: "interrupted", bee: Bee{Status: StatusStopped, Interrupted: true}},
		{name: "merge-error-conflict-unreported", bee: Bee{Status: StatusStopped, MergeError: "merge failed", ConflictFiles: []string{"new.txt"}}},
		{name: "missing-finished-at", bee: Bee{Status: StatusStopped}, useStarted: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newReclaimFixture(t)
			humanReviewSettings(f.mgr)
			f.mgr.opts.OnReport = func(string, string) error { return nil }
			path, base := f.addCopy(t, "lead-stale-0", "mcode/test-stale/0")
			if err := os.WriteFile(filepath.Join(path, "new.txt"), []byte("stale bee changes\n"), 0644); err != nil {
				t.Fatal(err)
			}
			h := tc.bee
			h.ThreadID = "bee-stale"
			h.WorktreePath = path
			h.Branch = "mcode/test-stale/0"
			h.BaseCommit = base
			h.ChangedFiles = []string{"new.txt"}
			h.StartedAt = f.clock.Now().Add(-48 * time.Hour)
			h.FinishedAt = f.finishedAtAgo(23 * time.Hour)
			if tc.useStarted {
				h.StartedAt = *h.FinishedAt
				h.FinishedAt = nil
			}
			f.setBees(h)
			ctx := context.Background()
			f.mgr.reclaimBeeCopies(ctx)
			if got := f.bee(t, h.ThreadID); !reflect.DeepEqual(got, h.clone()) {
				t.Fatalf("23 小时时不得改变 bee：%+v", got)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("23 小时时应保留副本: %v", err)
			}

			f.clock.Advance(time.Hour)
			f.mgr.reclaimBeeCopies(ctx)
			f.assertCopyGone(t, path, h.Branch)
			got := f.bee(t, h.ThreadID)
			patchPath := filepath.Join(filepath.Dir(f.wtRoot), "swarm-patches", f.leadID+"-0.patch")
			want := h.clone()
			want.WorktreePath, want.Branch = "", ""
			want.ArchivedPatchPath = patchPath
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("回收只能更新副本引用与存档路径，got=%+v want=%+v", got, want)
			}
			patch, err := os.ReadFile(got.ArchivedPatchPath)
			if err != nil || !strings.Contains(string(patch), "+stale bee changes") {
				t.Fatalf("未存档真实改动：%q, %v", patch, err)
			}
			diff, err := f.mgr.MergeDiff(ctx, f.leadID, h.ThreadID)
			if err != nil || string(diff) != string(patch) {
				t.Fatalf("MergeDiff 应读取归档补丁：%q, %v", diff, err)
			}
			records, err := f.mgr.opts.Persister.LoadLeads(ctx)
			if err != nil || len(records) != 1 || len(records[0].Bees) != 1 || !reflect.DeepEqual(records[0].Bees[0], want) {
				t.Fatalf("存档路径与副本清空应一起持久化：%+v, %v", records, err)
			}
			if _, err := f.mgr.integratePatch(ctx, f.leadID, f.repoDir, &got); err == nil || !strings.Contains(err.Error(), "worktree path is empty") {
				t.Fatalf("回收后的整合应明确拒绝：%v", err)
			}
			if _, err := f.mgr.ApproveMerge(ctx, f.leadID, h.ThreadID); !errors.Is(err, ErrNoPendingMerge) {
				t.Fatalf("回收后的 bee 不应进入批准合并：%v", err)
			}
			if after := f.bee(t, h.ThreadID); !reflect.DeepEqual(after, want) || !reflect.DeepEqual(got, want) {
				t.Fatal("回收后的合并入口不得改变 bee 状态")
			}
		})
	}
}

func TestManager_ReclaimBeeCopies_StaleActiveBeesKept(t *testing.T) {
	f := newReclaimFixture(t)
	f.setBees()
	for i, h := range []Bee{
		{Status: StatusRunning},
		{Status: StatusCompleted, PendingMerge: true},
		{Status: StatusCompleted, ReviewState: ReviewRunning},
	} {
		name := fmt.Sprintf("active-%d", i)
		path, base := f.addCopy(t, name, "mcode/test-"+name)
		h.ThreadID, h.Index = name, i
		h.WorktreePath, h.BaseCommit, h.Branch = path, base, "mcode/test-"+name
		h.ChangedFiles = []string{"new.txt"}
		h.FinishedAt = f.finishedAtAgo(48 * time.Hour)
		f.appendBee(h)
	}
	f.mgr.reclaimBeeCopies(context.Background())
	for i := 0; i < 3; i++ {
		h := f.bee(t, fmt.Sprintf("active-%d", i))
		if _, err := os.Stat(h.WorktreePath); err != nil || h.ArchivedPatchPath != "" {
			t.Fatalf("运行中、待合并或审查中的副本不应回收：%+v, %v", h, err)
		}
	}
}

func TestManager_ReclaimBeeCopies_InvalidGitMetadata(t *testing.T) {
	for _, changed := range []bool{false, true} {
		for _, relative := range []bool{false, true} {
			t.Run(fmt.Sprintf("changed=%t/relative=%t", changed, relative), func(t *testing.T) {
				f := newReclaimFixture(t)
				path, base := f.addCopy(t, "lead-invalid-0", "mcode/test-invalid/0")
				gitDir := filepath.Join(t.TempDir(), "missing-admin")
				if relative {
					var err error
					gitDir, err = filepath.Rel(path, gitDir)
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(path, ".git"), []byte("gitdir: "+gitDir+"\n"), 0644); err != nil {
					t.Fatal(err)
				}
				h := Bee{ThreadID: "bee-invalid", Status: StatusStopped, WorktreePath: path, BaseCommit: base, Branch: "mcode/test-invalid/0", FinishedAt: f.finishedAtAgo(24 * time.Hour)}
				if changed {
					h.ChangedFiles = []string{"new.txt"}
				}
				f.setBees(h)
				for i := 0; i < 2; i++ {
					f.mgr.reclaimBeeCopies(context.Background())
					f.clock.Advance(time.Minute)
				}
				got := f.bee(t, h.ThreadID)
				if changed {
					if _, err := os.Stat(path); err != nil || !reflect.DeepEqual(got, h.clone()) {
						t.Fatalf("元数据失效但有改动时必须保留副本与状态：%+v, %v", got, err)
					}
				} else {
					// Git 元数据已损坏时，只要求残留目录与 bee 引用被清理。
					f.assertCopyGone(t, path, "")
					if got.WorktreePath != "" || got.Branch != "" || got.ArchivedPatchPath != "" {
						t.Fatalf("元数据失效且无改动时应直接回收：%+v", got)
					}
				}
				f.mgr.mu.Lock()
				logged := f.mgr.invalidBeeCopyLogged[path]
				f.mgr.mu.Unlock()
				if !logged {
					t.Fatal("失效路径应记入日志去重集合")
				}
			})
		}
	}
}

func TestManager_ReclaimBeeCopies_StaleArchiveFailureKeepsCopy(t *testing.T) {
	for _, failure := range []string{"add", "diff", "save"} {
		t.Run(failure, func(t *testing.T) {
			f := newReclaimFixture(t)
			path, base := f.addCopy(t, "lead-failure-0", "mcode/test-failure/0")
			if err := os.WriteFile(filepath.Join(path, "new.txt"), []byte("keep me\n"), 0644); err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "add":
				f.runner.Bin = filepath.Join(t.TempDir(), "missing-git")
			case "diff":
				base = "invalid-base-commit"
			case "save":
				if err := os.WriteFile(filepath.Join(filepath.Dir(f.wtRoot), "swarm-patches"), []byte("not a directory"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			h := Bee{ThreadID: "bee-failure", Status: StatusFailed, WorktreePath: path, BaseCommit: base, Branch: "mcode/test-failure/0", ChangedFiles: []string{"new.txt"}, FinishedAt: f.finishedAtAgo(24 * time.Hour)}
			f.setBees(h)
			f.mgr.reclaimBeeCopies(context.Background())
			if _, err := os.Stat(path); err != nil || !reflect.DeepEqual(f.bee(t, h.ThreadID), h.clone()) {
				t.Fatalf("存档失败时应保留副本与 bee 状态：%v", err)
			}
		})
	}
}

func TestManager_ReclaimBeeCopies_StaleEmptyPatch(t *testing.T) {
	f := newReclaimFixture(t)
	path, base := f.addCopy(t, "lead-empty-0", "mcode/test-empty/0")
	f.setBees(Bee{ThreadID: "bee-empty", Status: StatusStopped, WorktreePath: path, BaseCommit: base, Branch: "mcode/test-empty/0", FinishedAt: f.finishedAtAgo(24 * time.Hour)})
	f.mgr.reclaimBeeCopies(context.Background())
	f.assertCopyGone(t, path, "mcode/test-empty/0")
	if h := f.bee(t, "bee-empty"); h.WorktreePath != "" || h.Branch != "" || h.ArchivedPatchPath != "" {
		t.Fatalf("空补丁可直接回收且不写存档路径：%+v", h)
	}
}

func TestManager_ReclaimBeeCopies_RechecksAfterMergeLock(t *testing.T) {
	f := newReclaimFixture(t)
	path, base := f.addCopy(t, "lead-resumed-0", "mcode/test-resumed/0")
	f.setBees(Bee{ThreadID: "bee-resumed", Status: StatusStopped, WorktreePath: path, BaseCommit: base, FinishedAt: f.finishedAtAgo(24 * time.Hour)})
	mergeMu := f.mgr.getLeadMergeMutex(f.leadID)
	mergeMu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.mgr.reclaimBeeCopies(context.Background())
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mgr.mu.Lock()
		collected := !f.mgr.lastBeeCopyReclaim.IsZero()
		if collected {
			f.mgr.leads[f.leadID].bees[0].Status = StatusRunning
		}
		f.mgr.mu.Unlock()
		if collected {
			break
		}
		if time.Now().After(deadline) {
			mergeMu.Unlock()
			<-done
			t.Fatal("回收没有收集候选")
		}
		time.Sleep(time.Millisecond)
	}
	mergeMu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("回收未结束")
	}
	if _, err := os.Stat(path); err != nil || f.bee(t, "bee-resumed").WorktreePath != path {
		t.Fatalf("等待合并锁时已恢复运行的 bee 不得回收：%v", err)
	}
}

// 只读且没有任何改动的 bee 即使没有合并标记，也可以回收副本。
func TestManager_ReclaimBeeCopies_ReadOnlyWithoutChanges(t *testing.T) {
	f := newReclaimFixture(t)
	path, _ := f.addCopy(t, "lead-readonly-0", "mcode/test-readonly/0")

	f.setBees(Bee{
		ThreadID:     "bee-readonly",
		Status:       StatusCompleted,
		ReadOnly:     true,
		WorktreePath: path,
		Branch:       "mcode/test-readonly/0",
		FinishedAt:   f.finishedAtAgo(11 * time.Minute),
	})

	f.mgr.reclaimBeeCopies(context.Background())

	f.assertCopyGone(t, path, "mcode/test-readonly/0")
	if h := f.bee(t, "bee-readonly"); h.WorktreePath != "" {
		t.Fatalf("只读无改动的副本应被回收，实际 %q", h.WorktreePath)
	}
}

// 不在 WorktreeDir 之下的路径不回收，避免误删主工作区或用户的目录。
func TestManager_ReclaimBeeCopies_SkipsPathOutsideWorktreeDir(t *testing.T) {
	f := newReclaimFixture(t)
	path := filepath.Join(t.TempDir(), "lead-outside-0")
	if err := f.runner.WorktreeAdd(context.Background(), f.repoDir, path, "mcode/test-outside/0", "HEAD"); err != nil {
		t.Fatalf("worktree add outside: %v", err)
	}

	f.setBees(Bee{
		ThreadID:     "bee-outside",
		Status:       StatusCompleted,
		Merged:       true,
		WorktreePath: path,
		Branch:       "mcode/test-outside/0",
		FinishedAt:   f.finishedAtAgo(11 * time.Minute),
	})

	f.mgr.reclaimBeeCopies(context.Background())

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("WorktreeDir 之外的路径不应回收，stat err: %v", err)
	}
	if h := f.bee(t, "bee-outside"); h.WorktreePath != path {
		t.Fatalf("不应清空副本引用，实际 %q", h.WorktreePath)
	}
}

// 回收限流：一分钟内连续两次调用只真正跑一次。
func TestManager_ReclaimBeeCopies_ThrottledToOneRunPerMinute(t *testing.T) {
	f := newReclaimFixture(t)
	first, _ := f.addCopy(t, "lead-throttle-0", "mcode/test-throttle/0")

	f.setBees(Bee{
		ThreadID:     "bee-throttle-0",
		Status:       StatusCompleted,
		Merged:       true,
		WorktreePath: first,
		Branch:       "mcode/test-throttle/0",
		FinishedAt:   f.finishedAtAgo(11 * time.Minute),
	})
	f.mgr.reclaimBeeCopies(context.Background())
	f.assertCopyGone(t, first, "mcode/test-throttle/0")

	second, _ := f.addCopy(t, "lead-throttle-1", "mcode/test-throttle/1")
	f.appendBee(Bee{
		ThreadID:     "bee-throttle-1",
		Index:        1,
		Status:       StatusCompleted,
		Merged:       true,
		WorktreePath: second,
		Branch:       "mcode/test-throttle/1",
		FinishedAt:   f.finishedAtAgo(11 * time.Minute),
	})

	// 30 秒后再调一次：还在同一分钟内，整轮回收被跳过。
	f.clock.Advance(30 * time.Second)
	f.mgr.reclaimBeeCopies(context.Background())
	if _, err := os.Stat(second); err != nil {
		t.Fatalf("一分钟内第二次调用不应再回收，stat err: %v", err)
	}

	// 累计 70 秒后放行。
	f.clock.Advance(40 * time.Second)
	f.mgr.reclaimBeeCopies(context.Background())
	f.assertCopyGone(t, second, "mcode/test-throttle/1")
}

// 审查模式下结果还没汇报给用户的 bee 不回收，汇报后才回收。
func TestManager_ReclaimBeeCopies_ReviewModeWaitsForReport(t *testing.T) {
	testenv.TempHome(t)
	clock := newTestClock()
	runner, repoDir := initTestRepo(t)
	wtRoot := t.TempDir()
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      newFakeDriver(),
		WorktreeDir: wtRoot,
		Now:         clock.Now,
		Persister:   newFakePersister(),
		OnReport:    func(leadThreadID, message string) error { return nil },
	})
	humanReviewSettings(mgr)

	path := filepath.Join(wtRoot, "lead-review-0")
	if err := runner.WorktreeAdd(context.Background(), repoDir, path, "mcode/test-review/0", "HEAD"); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	at := clock.Now().Add(-11 * time.Minute)
	mgr.mu.Lock()
	mgr.leads["lead-review"] = &leadState{
		leadThreadID: "lead-review",
		leadWorkDir:  repoDir,
		bees: []Bee{{
			ThreadID:     "bee-review",
			Status:       StatusCompleted,
			Merged:       true,
			WorktreePath: path,
			Branch:       "mcode/test-review/0",
			FinishedAt:   &at,
		}},
	}
	mgr.mu.Unlock()

	mgr.reclaimBeeCopies(context.Background())
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("未汇报时不应回收副本，stat err: %v", err)
	}

	mgr.mu.Lock()
	mgr.leads["lead-review"].bees[0].Reported = true
	mgr.mu.Unlock()

	// 越过一分钟的回收限流窗口。
	clock.Advance(2 * time.Minute)
	mgr.reclaimBeeCopies(context.Background())
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("汇报后应回收副本，stat err: %v", err)
	}
}

// 审查模式下用户放弃合并后，bee 副本可以回收：目录与分支消失，MergeDiscarded 保留；尚未汇报的仍不回收。
func TestManager_ReclaimBeeCopies_DiscardedReviewBee(t *testing.T) {
	testenv.TempHome(t)
	clock := newTestClock()
	runner, repoDir := initTestRepo(t)
	wtRoot := t.TempDir()
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      newFakeDriver(),
		WorktreeDir: wtRoot,
		Now:         clock.Now,
		Persister:   newFakePersister(),
		OnReport:    func(leadThreadID, message string) error { return nil },
	})
	humanReviewSettings(mgr)
	leadID := "lead-discard"
	ctx := context.Background()

	discardPath := filepath.Join(wtRoot, "lead-discard-0")
	if err := runner.WorktreeAdd(ctx, repoDir, discardPath, "mcode/test-discard/0", "HEAD"); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	unreportedPath := filepath.Join(wtRoot, "lead-discard-1")
	if err := runner.WorktreeAdd(ctx, repoDir, unreportedPath, "mcode/test-discard/1", "HEAD"); err != nil {
		t.Fatalf("worktree add: %v", err)
	}

	at := clock.Now().Add(-11 * time.Minute)
	const patch = "diff --git a/README.md b/README.md\n+discarded\n"
	mgr.mu.Lock()
	mgr.leads[leadID] = &leadState{
		leadThreadID: leadID,
		leadWorkDir:  repoDir,
		bees: []Bee{
			{
				ThreadID:     "bee-discard",
				Status:       StatusCompleted,
				PendingMerge: true,
				Reported:     true,
				MergePatch:   []byte(patch),
				WorktreePath: discardPath,
				Branch:       "mcode/test-discard/0",
				FinishedAt:   &at,
			},
			{
				ThreadID:       "bee-unreported",
				Status:         StatusCompleted,
				MergeDiscarded: true,
				Reported:       false,
				WorktreePath:   unreportedPath,
				Branch:         "mcode/test-discard/1",
				FinishedAt:     &at,
			},
		},
	}
	mgr.mu.Unlock()

	// 待审查期间即使静置够久也不回收。
	mgr.reclaimBeeCopies(ctx)
	if _, err := os.Stat(discardPath); err != nil {
		t.Fatalf("待审查时不应回收副本，stat err: %v", err)
	}

	if err := mgr.DiscardMerge(ctx, leadID, "bee-discard"); err != nil {
		t.Fatalf("DiscardMerge: %v", err)
	}
	// 越过一分钟的回收限流窗口。
	clock.Advance(2 * time.Minute)
	mgr.reclaimBeeCopies(ctx)

	if _, err := os.Stat(discardPath); !os.IsNotExist(err) {
		t.Fatalf("放弃审查后应回收副本，stat err: %v", err)
	}
	h := beeByThread(t, mgr, leadID, "bee-discard")
	if h.WorktreePath != "" || h.Branch != "" {
		t.Fatalf("回收后应清空副本引用，实际 path=%q branch=%q", h.WorktreePath, h.Branch)
	}
	if !h.MergeDiscarded {
		t.Fatalf("回收后 MergeDiscarded 应保持为 true")
	}
	// 副本回收后仍能通过记录里的补丁查看 diff。
	diff, err := mgr.MergeDiff(ctx, leadID, "bee-discard")
	if err != nil || string(diff) != patch {
		t.Fatalf("放弃后应仍能查看 diff，实际 %q, %v", string(diff), err)
	}

	// 反例：审查模式下尚未汇报时，即使已放弃也不回收。
	if _, err := os.Stat(unreportedPath); err != nil {
		t.Fatalf("未汇报时不应回收副本，stat err: %v", err)
	}
}

// 审查未通过的 bee 汇报后按放弃的规则回收副本，补丁仍可查 diff。
func TestManager_ReclaimBeeCopies_RejectedReviewBee(t *testing.T) {
	testenv.TempHome(t)
	clock := newTestClock()
	runner, repoDir := initTestRepo(t)
	wtRoot := t.TempDir()
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      newFakeDriver(),
		WorktreeDir: wtRoot,
		Now:         clock.Now,
		Persister:   newFakePersister(),
		OnReport:    func(leadThreadID, message string) error { return nil },
	})
	humanReviewSettings(mgr)
	leadID := "lead-reject"
	ctx := context.Background()

	rejectedPath := filepath.Join(wtRoot, "lead-reject-0")
	if err := runner.WorktreeAdd(ctx, repoDir, rejectedPath, "mcode/test-reject/0", "HEAD"); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	unreportedPath := filepath.Join(wtRoot, "lead-reject-1")
	if err := runner.WorktreeAdd(ctx, repoDir, unreportedPath, "mcode/test-reject/1", "HEAD"); err != nil {
		t.Fatalf("worktree add: %v", err)
	}

	at := clock.Now().Add(-11 * time.Minute)
	const patch = "diff --git a/README.md b/README.md\n+rejected\n"
	patchPath := filepath.Join(t.TempDir(), "lead-reject-0.patch")
	mgr.mu.Lock()
	mgr.leads[leadID] = &leadState{
		leadThreadID: leadID,
		leadWorkDir:  repoDir,
		bees: []Bee{
			{
				ThreadID:        "bee-rejected",
				Status:          StatusCompleted,
				Reported:        true,
				ReviewState:     ReviewRejected,
				ReviewPatchPath: patchPath,
				MergePatch:      []byte(patch),
				WorktreePath:    rejectedPath,
				Branch:          "mcode/test-reject/0",
				FinishedAt:      &at,
			},
			{
				ThreadID:     "bee-unreported",
				Status:       StatusCompleted,
				Reported:     false,
				ReviewState:  ReviewRejected,
				MergePatch:   []byte(patch),
				WorktreePath: unreportedPath,
				Branch:       "mcode/test-reject/1",
				FinishedAt:   &at,
			},
		},
	}
	mgr.mu.Unlock()

	mgr.reclaimBeeCopies(ctx)

	if _, err := os.Stat(rejectedPath); !os.IsNotExist(err) {
		t.Fatalf("审查未通过汇报后应回收副本，stat err: %v", err)
	}
	h := beeByThread(t, mgr, leadID, "bee-rejected")
	if h.WorktreePath != "" || h.Branch != "" {
		t.Fatalf("回收后应清空副本引用，实际 path=%q branch=%q", h.WorktreePath, h.Branch)
	}
	if h.ReviewState != ReviewRejected {
		t.Fatalf("回收后 ReviewState 应仍为 rejected，实际 %s", h.ReviewState)
	}
	if h.ReviewPatchPath != patchPath {
		t.Fatalf("回收后应保留补丁路径，实际 %q", h.ReviewPatchPath)
	}
	if len(h.MergePatch) == 0 {
		t.Fatal("回收后 MergePatch 应仍在")
	}
	// 副本回收后仍能通过记录里的补丁查看 diff。
	diff, err := mgr.MergeDiff(ctx, leadID, "bee-rejected")
	if err != nil || string(diff) != patch {
		t.Fatalf("审查未通过后应仍能查看 diff，实际 %q, %v", string(diff), err)
	}

	// 反例：审查模式下尚未汇报时，即使审查未通过也不回收。
	if _, err := os.Stat(unreportedPath); err != nil {
		t.Fatalf("未汇报时不应回收副本，stat err: %v", err)
	}
}

// 副本回收之后，已合并的 bee 仍能通过 LandedPatchPath 查看 diff。
func TestManager_MergeDiff_UsesLandedPatchAfterReclaim(t *testing.T) {
	f := newReclaimFixture(t)
	path, _ := f.addCopy(t, "lead-diff-0", "mcode/test-diff/0")

	landedPath := filepath.Join(t.TempDir(), "landed-diff.patch")
	const content = "diff --git a/README.md b/README.md\n+landed\n"
	if err := os.WriteFile(landedPath, []byte(content), 0644); err != nil {
		t.Fatalf("write landed patch: %v", err)
	}

	f.setBees(Bee{
		ThreadID:        "bee-diff",
		Status:          StatusCompleted,
		Merged:          true,
		ChangedFiles:    []string{"README.md"},
		LandedPatchPath: landedPath,
		WorktreePath:    path,
		Branch:          "mcode/test-diff/0",
		FinishedAt:      f.finishedAtAgo(11 * time.Minute),
	})
	f.mgr.reclaimBeeCopies(context.Background())

	patch, err := f.mgr.MergeDiff(context.Background(), f.leadID, "bee-diff")
	if err != nil {
		t.Fatalf("MergeDiff 返回错误: %v", err)
	}
	if string(patch) != content {
		t.Fatalf("应返回落地补丁内容，实际 %q", string(patch))
	}

	// 既没有补丁也没有副本时维持原有行为：返回空且不报错。
	f.setBees(Bee{ThreadID: "bee-empty", Status: StatusCompleted, Merged: true})
	patch, err = f.mgr.MergeDiff(context.Background(), f.leadID, "bee-empty")
	if err != nil || patch != nil {
		t.Fatalf("无补丁无副本时应返回 nil, nil，实际 %q, %v", string(patch), err)
	}
}

func TestManager_MergeDiff_FallsBackToArchivedPatch(t *testing.T) {
	f := newReclaimFixture(t)
	archivedPath := filepath.Join(t.TempDir(), "archived.patch")
	const content = "diff --git a/new.txt b/new.txt\n+archived\n"
	if err := os.WriteFile(archivedPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	landedPath := filepath.Join(t.TempDir(), "landed.patch")
	for _, landed := range []string{"", landedPath, archivedPath} {
		f.setBees(Bee{ThreadID: "bee-archived", Status: StatusStopped, LandedPatchPath: landed, ArchivedPatchPath: archivedPath})
		diff, err := f.mgr.MergeDiff(context.Background(), f.leadID, "bee-archived")
		if err != nil || string(diff) != content {
			t.Fatalf("落地路径为空或不可读时应读取归档：%q, %v", diff, err)
		}
	}
	if err := os.WriteFile(landedPath, nil, 0644); err != nil {
		t.Fatal(err)
	}
	f.setBees(Bee{ThreadID: "bee-archived", Status: StatusStopped, LandedPatchPath: landedPath, ArchivedPatchPath: archivedPath})
	diff, err := f.mgr.MergeDiff(context.Background(), f.leadID, "bee-archived")
	if err != nil || string(diff) != content {
		t.Fatalf("落地补丁为空时应读取归档：%q, %v", diff, err)
	}
}

// 删除 lead 时一并回收 bee 副本：已合并的直接删，未落地的先把改动存档到 swarm-patches 再删。
func TestManager_BeeThreadDeleted_ReclaimsBeeCopies(t *testing.T) {
	f := newReclaimFixture(t)

	mergedPath, _ := f.addCopy(t, "lead-delete-0", "mcode/test-delete/0")
	dirtyPath, dirtyBase := f.addCopy(t, "lead-delete-1", "mcode/test-delete/1")
	if err := os.WriteFile(filepath.Join(dirtyPath, "new.txt"), []byte("未落地的改动\n"), 0644); err != nil {
		t.Fatalf("write new.txt: %v", err)
	}

	f.setBees(
		Bee{
			ThreadID:     "bee-merged",
			Index:        0,
			Status:       StatusCompleted,
			Merged:       true,
			WorktreePath: mergedPath,
			Branch:       "mcode/test-delete/0",
			FinishedAt:   f.finishedAtAgo(11 * time.Minute),
		},
		Bee{
			ThreadID:     "bee-dirty",
			Index:        1,
			Status:       StatusCompleted,
			ChangedFiles: []string{"new.txt"},
			WorktreePath: dirtyPath,
			Branch:       "mcode/test-delete/1",
			BaseCommit:   dirtyBase,
			FinishedAt:   f.finishedAtAgo(11 * time.Minute),
		},
	)

	f.mgr.BeeThreadDeleted(context.Background(), f.leadID)

	f.assertCopyGone(t, mergedPath, "mcode/test-delete/0")
	f.assertCopyGone(t, dirtyPath, "mcode/test-delete/1")

	patchPath := filepath.Join(filepath.Dir(f.wtRoot), "swarm-patches", f.leadID+"-1.patch")
	patch, err := os.ReadFile(patchPath)
	if err != nil {
		t.Fatalf("期望未落地改动被存档到 %s: %v", patchPath, err)
	}
	if !strings.Contains(string(patch), "new.txt") {
		t.Fatalf("存档补丁应包含新文件，实际 %q", string(patch))
	}
}

// 删除 lead 时仍未合并的 bee，即使 ChangedFiles 还没更新为空，也要先采集 worktree 里的真实改动存档，再删副本。
func TestManager_BeeThreadDeleted_ArchivesUnmergedBeeWithStaleChangedFiles(t *testing.T) {
	f := newReclaimFixture(t)

	stalePath, staleBase := f.addCopy(t, "lead-stale-0", "mcode/test-stale/0")
	if err := os.WriteFile(filepath.Join(stalePath, "stale-new.txt"), []byte("未上报的改动\n"), 0644); err != nil {
		t.Fatalf("write stale-new.txt: %v", err)
	}

	f.setBees(Bee{
		ThreadID:     "bee-stale",
		Index:        0,
		Status:       StatusCompleted,
		WorktreePath: stalePath,
		Branch:       "mcode/test-stale/0",
		BaseCommit:   staleBase,
		FinishedAt:   f.finishedAtAgo(11 * time.Minute),
	})

	f.mgr.BeeThreadDeleted(context.Background(), f.leadID)

	patchPath := filepath.Join(filepath.Dir(f.wtRoot), "swarm-patches", f.leadID+"-0.patch")
	patch, err := os.ReadFile(patchPath)
	if err != nil {
		t.Fatalf("ChangedFiles 为空但副本有实际改动时应先存档到 %s: %v", patchPath, err)
	}
	if !strings.Contains(string(patch), "stale-new.txt") {
		t.Fatalf("存档补丁应包含真实改动文件，实际 %q", string(patch))
	}
	f.assertCopyGone(t, stalePath, "mcode/test-stale/0")
}
