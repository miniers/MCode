package datadir

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func isolateConfig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("AppData", filepath.Join(home, "config"))
	t.Setenv("MCODE_DATA", "")
	t.Setenv(relaunchParentEnv, "")
	mu.Lock()
	exportedPinned = false
	migrationErr = nil
	mu.Unlock()
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
}

func checkFile(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil || string(b) != want {
		t.Fatalf("读取 %s = %q, %v，期望 %q", path, b, err, want)
	}
}

func TestPointerAndResolve(t *testing.T) {
	isolateConfig(t)
	wantDefault := filepath.Join(os.Getenv("HOME"), ".mcode")
	if got := Resolve("ignored", false); got != wantDefault {
		t.Fatalf("默认目录 = %q", got)
	}
	old, next := t.TempDir(), filepath.Join(t.TempDir(), "next")
	if err := SetPending(old, next); err != nil {
		t.Fatal(err)
	}
	p, err := readPointer()
	if err != nil || p.Path != next || p.MoveFrom != old {
		t.Fatalf("指针读回失败: %+v, %v", p, err)
	}
	path, err := pointerPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, filepath.Join("mcode", "data-dir.json")) {
		t.Fatalf("指针路径 = %q", path)
	}
	if got := Resolve("ignored", false); got != next {
		t.Fatalf("指针优先级 = %q", got)
	}
	t.Setenv("MCODE_DATA", "env-dir")
	if got := Resolve("ignored", false); got != "env-dir" {
		t.Fatalf("环境变量优先级 = %q", got)
	}
	if got := Resolve("flag-dir", true); got != "flag-dir" {
		t.Fatalf("显式参数优先级 = %q", got)
	}
	if got := Resolve("", true); got != "" {
		t.Fatalf("显式空参数 = %q", got)
	}
}

func TestExportAndPrepareRelaunch(t *testing.T) {
	isolateConfig(t)
	if err := Export(filepath.Join(t.TempDir(), "data"), false); err != nil {
		t.Fatal(err)
	}
	if Pinned() {
		t.Fatal("未固定目录不应被标记为 pinned")
	}
	if os.Getenv("MCODE_DATA") == "" {
		t.Fatal("Export 应设置 MCODE_DATA")
	}
	PrepareRelaunch()
	if got := os.Getenv(relaunchParentEnv); got != strconv.Itoa(os.Getpid()) {
		t.Fatalf("重启父进程 PID = %q，期望 %d", got, os.Getpid())
	}
	if got := os.Getenv("MCODE_DATA"); got != "" {
		t.Fatalf("非固定目录重启前应清理 MCODE_DATA: %q", got)
	}

	if err := Export(filepath.Join(t.TempDir(), "data"), true); err != nil {
		t.Fatal(err)
	}
	if !Pinned() {
		t.Fatal("固定目录应被标记为 pinned")
	}
	PrepareRelaunch()
	if got := os.Getenv(relaunchParentEnv); got != strconv.Itoa(os.Getpid()) {
		t.Fatalf("固定目录重启父进程 PID = %q，期望 %d", got, os.Getpid())
	}
	if got := os.Getenv("MCODE_DATA"); got == "" {
		t.Fatal("固定目录重启前不应清理 MCODE_DATA")
	}
}

func TestSetPendingRejectsTarget(t *testing.T) {
	isolateConfig(t)
	old := t.TempDir()
	nonempty := t.TempDir()
	writeTestFile(t, filepath.Join(nonempty, "data"), "keep")
	for _, next := range []string{"relative", old, filepath.Join(old, "child"), nonempty} {
		if err := SetPending(old, next); err == nil {
			t.Errorf("应拒绝目标 %q", next)
		}
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(old, alias); err == nil {
		if err := SetPending(old, filepath.Join(alias, "child")); err == nil {
			t.Fatal("应拒绝经符号链接进入旧目录")
		}
	}
	checkFile(t, filepath.Join(nonempty, "data"), "keep")
}

func TestApplyPendingMove(t *testing.T) {
	for _, copyFallback := range []bool{false, true} {
		name := "rename"
		if copyFallback {
			name = "copy"
		}
		t.Run(name, func(t *testing.T) {
			isolateConfig(t)
			if copyFallback {
				original := rename
				rename = func(string, string) error { return errors.New("跨盘重命名失败") }
				t.Cleanup(func() { rename = original })
			}
			old, next := t.TempDir(), t.TempDir()
			writeTestFile(t, filepath.Join(old, "nested", "config.json"), "config")
			link := filepath.Join(old, "link")
			hasLink := os.Symlink(filepath.Join("nested", "config.json"), link) == nil
			if err := SetPending(old, next); err != nil {
				t.Fatal(err)
			}
			got, err := ApplyPending()
			if err != nil || got != next {
				t.Fatalf("迁移失败: %q, %v", got, err)
			}
			checkFile(t, filepath.Join(next, "nested", "config.json"), "config")
			if runtime.GOOS != "windows" {
				info, err := os.Stat(filepath.Join(next, "nested", "config.json"))
				if err != nil || info.Mode().Perm() != 0o640 {
					t.Fatalf("文件权限未保留: %v, %v", info, err)
				}
			}
			if hasLink {
				if target, err := os.Readlink(filepath.Join(next, "link")); err != nil || target != filepath.Join("nested", "config.json") {
					t.Fatalf("符号链接未保留: %q, %v", target, err)
				}
			}
			if entries, err := os.ReadDir(old); err != nil || len(entries) != 0 {
				t.Fatalf("旧目录仍有条目: %v, %v", entries, err)
			}
			if p, err := readPointer(); err != nil || p.Path != next || p.MoveFrom != "" {
				t.Fatalf("完成指针错误: %+v, %v", p, err)
			}
			if got, err := ApplyPending(); err != nil || got != next {
				t.Fatalf("重复启动失败: %q, %v", got, err)
			}
		})
	}
}

func TestApplyPendingWaitsForParentBeforeMoving(t *testing.T) {
	for _, parentPID := range []string{strconv.Itoa(os.Getpid()), "", "invalid", "0", "-1", "99999999999999999999999999"} {
		t.Run("pid="+parentPID, func(t *testing.T) {
			isolateConfig(t)
			old, next := t.TempDir(), t.TempDir()
			writeTestFile(t, filepath.Join(old, "data"), "keep")
			if err := SetPending(old, next); err != nil {
				t.Fatal(err)
			}
			t.Setenv(relaunchParentEnv, parentPID)
			original := waitParentExit
			var calls int
			waitParentExit = func(pid int, timeout time.Duration) {
				calls++
				if pid != os.Getpid() || timeout != 30*time.Second {
					t.Errorf("等待参数 = (%d, %v)", pid, timeout)
				}
				if _, ok := os.LookupEnv(relaunchParentEnv); ok {
					t.Error("等待前应已清理父进程环境变量")
				}
				checkFile(t, filepath.Join(old, "data"), "keep")
				if _, err := os.Stat(filepath.Join(next, "data")); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("等待前不应搬动条目: %v", err)
				}
			}
			t.Cleanup(func() { waitParentExit = original })
			if got, err := ApplyPending(); err != nil || got != next {
				t.Fatalf("迁移失败: %q, %v", got, err)
			}
			wantCalls := 0
			if parentPID == strconv.Itoa(os.Getpid()) {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Errorf("等待次数 = %d，期望 %d", calls, wantCalls)
			}
			if _, ok := os.LookupEnv(relaunchParentEnv); ok {
				t.Error("迁移后应已清理父进程环境变量")
			}
			checkFile(t, filepath.Join(next, "data"), "keep")
		})
	}
}

func TestApplyPendingWithoutMigrationSkipsParentWait(t *testing.T) {
	for _, hasPointer := range []bool{false, true} {
		t.Run(strconv.FormatBool(hasPointer), func(t *testing.T) {
			isolateConfig(t)
			want := defaultDir()
			if hasPointer {
				want = t.TempDir()
				if err := writePointer(pointer{Path: want}); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv(relaunchParentEnv, strconv.Itoa(os.Getpid()))
			original := waitParentExit
			waitParentExit = func(int, time.Duration) { t.Error("没有迁移时不应等待父进程") }
			t.Cleanup(func() { waitParentExit = original })
			if got, err := ApplyPending(); err != nil || got != want {
				t.Fatalf("目录解析失败: %q, %v", got, err)
			}
			if _, ok := os.LookupEnv(relaunchParentEnv); ok {
				t.Error("没有迁移时也应清理父进程环境变量")
			}
		})
	}
}

func TestMigrationErrorTracksApplyPending(t *testing.T) {
	isolateConfig(t)
	if err := MigrationError(); err != nil {
		t.Fatalf("迁移前不应有错误: %v", err)
	}
	path, err := pointerPath()
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, "{")
	t.Setenv(relaunchParentEnv, strconv.Itoa(os.Getpid()))
	if _, err := ApplyPending(); err == nil || MigrationError() != err {
		t.Fatalf("应记录读取指针失败: %v, %v", err, MigrationError())
	}
	if _, ok := os.LookupEnv(relaunchParentEnv); ok {
		t.Error("读取指针失败时也应清理父进程环境变量")
	}
	old, next := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(old, "mcode.db"), "corrupt database")
	if err := SetPending(old, next); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPending(); err == nil || MigrationError() != err {
		t.Fatalf("应记录迁移回滚后的错误: %v, %v", err, MigrationError())
	}
	if _, err := ApplyPending(); err != nil || MigrationError() != nil {
		t.Fatalf("没有迁移时应清除错误: %v, %v", err, MigrationError())
	}
	if err := SetPending(old, next); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPending(); err == nil {
		t.Fatal("坏库应使迁移失败")
	}
	if err := os.Remove(filepath.Join(old, "mcode.db")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(old, "data"), "keep")
	if err := SetPending(old, next); err != nil {
		t.Fatal(err)
	}
	if got, err := ApplyPending(); err != nil || got != next || MigrationError() != nil {
		t.Fatalf("成功迁移后应清除错误: %q, %v, %v", got, err, MigrationError())
	}
}

func TestApplyPendingRollback(t *testing.T) {
	isolateConfig(t)
	old, next := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(old, "a"), "first")
	writeTestFile(t, filepath.Join(old, "b"), "second")
	if err := SetPending(old, next); err != nil {
		t.Fatal(err)
	}
	original := rename
	rename = func(src, dst string) error {
		if src == filepath.Join(old, "b") {
			if err := os.MkdirAll(filepath.Join(dst, "blocked"), 0o700); err != nil {
				t.Fatal(err)
			}
			return errors.New("注入复制失败")
		}
		return original(src, dst)
	}
	t.Cleanup(func() { rename = original })
	if got, err := ApplyPending(); err == nil || got != old {
		t.Fatalf("应迁移失败并返回旧目录: %q, %v", got, err)
	}
	checkFile(t, filepath.Join(old, "a"), "first")
	checkFile(t, filepath.Join(old, "b"), "second")
	if p, err := readPointer(); err != nil || p.Path != old || p.MoveFrom != "" {
		t.Fatalf("回退指针错误: %+v, %v", p, err)
	}
}

func TestApplyPendingRevalidatesTarget(t *testing.T) {
	isolateConfig(t)
	old, next := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(old, "data"), "old")
	if err := SetPending(old, next); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(next, "data"), "new")
	if got, err := ApplyPending(); err == nil || got != old {
		t.Fatalf("应拒绝启动时已非空的目标: %q, %v", got, err)
	}
	checkFile(t, filepath.Join(old, "data"), "old")
	checkFile(t, filepath.Join(next, "data"), "new")
}

func TestApplyPendingConcurrent(t *testing.T) {
	isolateConfig(t)
	old, next := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(old, "config.json"), "config")
	if err := SetPending(old, next); err != nil {
		t.Fatal(err)
	}
	t.Setenv(relaunchParentEnv, strconv.Itoa(os.Getpid()))
	original := waitParentExit
	var waitCalls int
	waitParentExit = func(pid int, timeout time.Duration) {
		waitCalls++
		if pid != os.Getpid() || timeout != 30*time.Second {
			t.Errorf("并发迁移等待参数 = (%d, %v)", pid, timeout)
		}
		if b, err := os.ReadFile(filepath.Join(old, "config.json")); err != nil || string(b) != "config" {
			t.Errorf("并发迁移必须先等待再搬动条目: %q, %v", b, err)
		}
	}
	t.Cleanup(func() { waitParentExit = original })
	type result struct {
		dir string
		err error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			dir, err := ApplyPending()
			err = errors.Join(err, MigrationError())
			results <- result{dir, err}
		}()
	}
	for i := 0; i < 2; i++ {
		if got := <-results; got.err != nil || got.dir != next {
			t.Errorf("并发迁移失败: %+v", got)
		}
	}
	if waitCalls != 1 {
		t.Errorf("并发迁移等待次数 = %d，期望 1", waitCalls)
	}
	checkFile(t, filepath.Join(next, "config.json"), "config")
}

func TestApplyPendingDatabase(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "rewrite", true: "rollback"}[corrupt], func(t *testing.T) {
			isolateConfig(t)
			old, next := t.TempDir(), t.TempDir()
			path := filepath.Join(old, "mcode.db")
			if corrupt {
				writeTestFile(t, path, "corrupt database")
			} else {
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`CREATE TABLE threads (worktree_path TEXT); INSERT INTO threads VALUES (?)`, filepath.Join(old, "worktrees", "bee")); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			writeTestFile(t, filepath.Join(old, "config.json"), "config")
			if err := SetPending(old, next); err != nil {
				t.Fatal(err)
			}
			got, err := ApplyPending()
			if corrupt {
				if err == nil || got != old {
					t.Fatalf("坏库应使迁移回退: %q, %v", got, err)
				}
				checkFile(t, path, "corrupt database")
				checkFile(t, filepath.Join(old, "config.json"), "config")
				return
			}
			if err != nil || got != next {
				t.Fatalf("数据库迁移失败: %q, %v", got, err)
			}
			db, err := sql.Open("sqlite", filepath.Join(next, "mcode.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var value string
			if err := db.QueryRow(`SELECT worktree_path FROM threads`).Scan(&value); err != nil || value != filepath.Join(next, "worktrees", "bee") {
				t.Fatalf("数据库路径未改写: %q, %v", value, err)
			}
		})
	}
}

func TestApplyPendingRepairsWorktrees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("未安装 git")
	}
	isolateConfig(t)
	repo, old, next := t.TempDir(), t.TempDir(), t.TempDir()
	nextResolved, err := filepath.EvalSymlinks(next)
	if err != nil {
		t.Fatal(err)
	}
	runGit := func(dir string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s, %v", args, out, err)
		}
		return string(out)
	}
	runGit(repo, "init")
	runGit(repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "test")
	for _, root := range []string{"worktrees", "swarm-worktrees"} {
		runGit(repo, "worktree", "add", "--detach", filepath.Join(old, root, "bee"), "HEAD")
	}
	if err := SetPending(old, next); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPending(); err != nil {
		t.Fatal(err)
	}
	list := runGit(repo, "worktree", "list", "--porcelain")
	for _, root := range []string{"worktrees", "swarm-worktrees"} {
		path := filepath.Join(next, root, "bee")
		if !strings.Contains(list, filepath.ToSlash(filepath.Join(nextResolved, root, "bee"))) {
			t.Errorf("worktree 未修复: %s", list)
		}
		runGit(path, "status", "--porcelain")
	}
}
