package swarm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/config"
	"mcode/internal/kanban"
)

func TestManager_DataDir(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		opts Options
		want string
	}{
		{"显式值优先", Options{DataDir: "/data", WorktreeDir: "/other/copies"}, "/data"},
		{"仅显式值", Options{DataDir: "/data"}, "/data"},
		{"旧推导", Options{WorktreeDir: filepath.Join("legacy", "copies")}, "legacy"},
		{"空目录", Options{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(tc.opts)
			if got := m.DataDir(); got != tc.want {
				t.Fatalf("DataDir=%q，期望 %q", got, tc.want)
			}
			wantProfiles := ""
			if tc.want != "" {
				wantProfiles = filepath.Join(tc.want, ProfilesFileName)
			}
			if got := m.profilesPath(); got != wantProfiles {
				t.Fatalf("profilesPath=%q，期望 %q", got, wantProfiles)
			}
		})
	}
}

func TestManager_DataDirArtifacts(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	m := NewManager(Options{DataDir: dataDir, WorktreeDir: filepath.Join(t.TempDir(), "copies")})
	for _, tc := range []struct {
		dir  string
		save func(string, int, []byte) (string, error)
	}{
		{"swarm-patches", m.savePatch},
		{"swarm-landed", m.saveLandedPatch},
		{"swarm-reviews", m.saveReviewPatch},
	} {
		got, err := tc.save("lead", 1, []byte("patch"))
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(dataDir, tc.dir, "lead-1.patch"); got != want {
			t.Errorf("补丁路径=%q，期望 %q", got, want)
		}
	}
	if got := m.reviewProjectRuntime(context.Background(), "lead", "").dataDir; got != dataDir {
		t.Errorf("审查数据目录=%q，期望 %q", got, dataDir)
	}
}

func TestManager_IsBeeCopyPath(t *testing.T) {
	t.Parallel()
	m := NewManager(Options{WorktreeDir: filepath.Join("central", "copies")})
	for _, tc := range []struct {
		path string
		want bool
	}{
		{filepath.Join("central", "copies", "lead-0"), true},
		{"/repo/.mcode/worktrees/swarm/lead-0", true},
		{`C:\repo\.mcode\worktrees\swarm\lead-0`, true},
		{".mcode/worktrees/swarm/lead-0", true},
		{"/repo/.mcode/worktrees/swarm", false},
		{"/repo/.mcode/worktrees/other/lead-0", false},
		{"/repo/.mcode/worktrees/swarmish/lead-0", false},
		{"/repo/.mcode/worktrees/swarm/../other", false},
		{filepath.Join("central", "copies"), false},
		{"", false},
	} {
		if got := m.isBeeCopyPath(tc.path); got != tc.want {
			t.Errorf("isBeeCopyPath(%q)=%v，期望 %v", tc.path, got, tc.want)
		}
	}
}

func TestManager_ProjectBeeCopy(t *testing.T) {
	t.Parallel()
	runner, repo := initTestRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	workDir := filepath.Join(repo, "nested")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	driver := newFakeDriver()
	location := config.WorktreeLocationProject
	m := NewManager(Options{
		Git: runner, Driver: driver, WorktreeDir: t.TempDir(),
		WorktreeLocation: func() string { return location },
	})
	m.leads["lead"] = &leadState{leadWorkDir: workDir}
	driver.onStart = func(req kanban.SessionRequest) {
		// 会话启动期间尚未写入 bees，必须由 startingWorkDirs 保护副本。
		removed, err := m.SweepOrphanCopies(ctx)
		if err != nil || len(removed) != 0 {
			t.Errorf("清理误删正在启动的副本：%v，%v", removed, err)
		}
		if _, err := os.Stat(req.WorkDir); err != nil {
			t.Errorf("正在启动的副本不可用：%v", err)
		}
	}
	bee, err := m.startSingleBee(ctx, "lead", "project", workDir, BeeSpec{ReadOnly: true}, 0, "lead")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(repo, ".mcode", "worktrees", "swarm", "lead-0")
	if !sweepPathReferenced(sweepPathSet([]string{want}), bee.WorktreePath) {
		t.Fatalf("副本路径=%q，期望 %q", bee.WorktreePath, want)
	}
	exclude, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if err != nil || !strings.Contains(string(exclude), "/.mcode/worktrees/\n") {
		t.Fatalf("项目内副本未排除：%q，%v", exclude, err)
	}
	m.leads["lead"].bees = []Bee{bee}
	location = config.WorktreeLocationCentral
	centralBee, err := m.startSingleBee(ctx, "lead", "project", workDir, BeeSpec{ReadOnly: true}, 1, "lead")
	if err != nil {
		t.Fatal(err)
	}
	if !pathUnderDir(m.opts.WorktreeDir, centralBee.WorktreePath) {
		t.Fatalf("切换位置后副本未使用集中目录：%q", centralBee.WorktreePath)
	}
}

func TestManager_ProjectBeeCopyStartFailure(t *testing.T) {
	t.Parallel()
	runner, repo := initTestRepo(t)
	m := NewManager(Options{
		Git: runner, Driver: &resumeFailDriver{fakeDriver: newFakeDriver()}, WorktreeDir: t.TempDir(),
		WorktreeLocation: func() string { return config.WorktreeLocationProject },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := m.startSingleBee(ctx, "lead", "project", repo, BeeSpec{ReadOnly: true}, 0, "lead"); err == nil {
		t.Fatal("会话启动失败应返回错误")
	}
	copyPath := filepath.Join(repo, ".mcode", "worktrees", "swarm", "lead-0")
	if _, err := os.Stat(copyPath); !os.IsNotExist(err) {
		t.Fatalf("启动失败未清理副本：%v", err)
	}
	if len(m.startingWorkDirs) != 0 {
		t.Fatalf("启动失败仍留下路径登记：%v", m.startingWorkDirs)
	}
	if _, err := runner.RevParse(ctx, repo, "refs/heads/mcode/swarm/lead/0"); err == nil {
		t.Fatal("启动失败仍留下副本分支")
	}
}

func TestManager_ProjectBeeCopyConcurrentSweep(t *testing.T) {
	t.Parallel()
	runner, repo := initTestRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	driver := newFakeDriver()
	started := make(chan string, 1)
	release := make(chan struct{})
	driver.onStart = func(req kanban.SessionRequest) {
		started <- req.WorkDir
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	m := NewManager(Options{
		Git: runner, Driver: driver, WorktreeDir: t.TempDir(),
		WorktreeLocation: func() string { return config.WorktreeLocationProject },
	})
	m.leads["lead"] = &leadState{leadWorkDir: repo}
	done := make(chan error, 1)
	go func() {
		_, err := m.startSingleBee(ctx, "lead", "project", repo, BeeSpec{ReadOnly: true}, 0, "lead")
		done <- err
	}()
	select {
	case copyPath := <-started:
		removed, err := m.SweepOrphanCopies(ctx)
		if err != nil || len(removed) != 0 {
			t.Errorf("并发清理误删正在启动的副本：%v，%v", removed, err)
		}
		if _, err := os.Stat(copyPath); err != nil {
			t.Errorf("正在启动的副本不可用：%v", err)
		}
	case <-ctx.Done():
		t.Error("等待会话启动超时")
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("副本启动未结束")
	}
}
