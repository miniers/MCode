package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"
)

// runTestGit 在测试仓库中直接执行 git 命令（仅用于测试准备与断言）。
func runTestGit(t *testing.T, dir string, args ...string) string {
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
		t.Fatalf("git %s 执行失败: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRevertTestRepo 创建带首个提交的临时 git 仓库与 Runner。
func newRevertTestRepo(t *testing.T) (*Runner, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 可执行文件不在 PATH 中，跳过测试")
	}
	dir := t.TempDir()
	runTestGit(t, dir, "init", "-q")
	runTestGit(t, dir, "config", "user.name", "mcode-test")
	runTestGit(t, dir, "config", "user.email", "mcode-test@localhost")
	runTestGit(t, dir, "config", "core.autocrlf", "false")
	writeTestFile(t, dir, "seed.txt", "seed\n")
	runTestGit(t, dir, "add", "-A")
	runTestGit(t, dir, "commit", "-q", "-m", "init")
	return NewRunner("git", 2), dir
}

// writeTestFile 以仓库相对路径写入测试文件，必要时创建父目录。
func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		t.Fatalf("写入 %s 失败: %v", name, err)
	}
}

// readTestFile 读取仓库相对路径的文件内容。
func readTestFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", name, err)
	}
	return string(data)
}

func TestPreviewRestoreKeepsSecondEdits(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner, dir := newRevertTestRepo(t)
	ctx := context.Background()

	writeTestFile(t, dir, "changed.txt", "v0\n")
	writeTestFile(t, dir, "clean.txt", "c0\n")
	base, err := runner.Checkpoint(ctx, dir, CheckpointRef("t1", "turn1")+"/start", "base")
	if err != nil {
		t.Fatalf("Checkpoint base: %v", err)
	}

	writeTestFile(t, dir, "changed.txt", "v1\n")
	writeTestFile(t, dir, "clean.txt", "c1\n")
	writeTestFile(t, dir, "added.txt", "new\n")
	finish, err := runner.Checkpoint(ctx, dir, CheckpointRef("t1", "turn1")+"/end", "finish")
	if err != nil {
		t.Fatalf("Checkpoint finish: %v", err)
	}

	// finish 之后用户又手改了 changed.txt，必须触发二次修改保护
	writeTestFile(t, dir, "changed.txt", "user-v2\n")

	preview, err := runner.PreviewRestore(ctx, dir, base, finish)
	if err != nil {
		t.Fatalf("PreviewRestore: %v", err)
	}
	if preview.Base != base || preview.Finish != finish {
		t.Errorf("预览检查点不匹配: base=%s finish=%s", preview.Base, preview.Finish)
	}

	byPath := make(map[string]RestoreFile, len(preview.Files))
	for _, f := range preview.Files {
		byPath[f.Path] = f
	}

	if changed, ok := byPath["changed.txt"]; !ok || !changed.IsKept {
		t.Errorf("changed.txt 应为 IsKept=true: %+v", changed)
	}
	if clean, ok := byPath["clean.txt"]; !ok || clean.IsKept || !clean.InBase {
		t.Errorf("clean.txt 应为 IsKept=false, InBase=true: %+v", clean)
	} else if clean.Additions != 1 || clean.Deletions != 1 {
		t.Errorf("clean.txt 增删行数应为 +1/-1: %+v", clean)
	}
	if added, ok := byPath["added.txt"]; !ok || added.IsKept || added.InBase {
		t.Errorf("added.txt 应为 IsKept=false, InBase=false: %+v", added)
	}
	if got, want := strings.Join(preview.Restorable(), ","), "added.txt,clean.txt"; got != want {
		t.Errorf("Restorable() = %q, want %q", got, want)
	}
	if got, want := strings.Join(preview.Kept(), ","), "changed.txt"; got != want {
		t.Errorf("Kept() = %q, want %q", got, want)
	}
}

func TestExecuteRevertRestoresUnmodifiedFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner, dir := newRevertTestRepo(t)
	ctx := context.Background()

	writeTestFile(t, dir, "changed.txt", "v0\n")
	writeTestFile(t, dir, "clean.txt", "c0\n")
	base, err := runner.Checkpoint(ctx, dir, CheckpointRef("t1", "turn1")+"/start", "base")
	if err != nil {
		t.Fatalf("Checkpoint base: %v", err)
	}

	writeTestFile(t, dir, "changed.txt", "v1\n")
	writeTestFile(t, dir, "clean.txt", "c1\n")
	writeTestFile(t, dir, "added.txt", "new\n")
	finish, err := runner.Checkpoint(ctx, dir, CheckpointRef("t1", "turn1")+"/end", "finish")
	if err != nil {
		t.Fatalf("Checkpoint finish: %v", err)
	}
	writeTestFile(t, dir, "changed.txt", "user-v2\n")

	preview, err := runner.ExecuteRevert(ctx, dir, base, finish)
	if err != nil {
		t.Fatalf("ExecuteRevert: %v", err)
	}
	if preview == nil {
		t.Fatalf("ExecuteRevert 应返回预览结果")
	}

	if got := readTestFile(t, dir, "clean.txt"); got != "c0\n" {
		t.Errorf("clean.txt 应还原为 base 内容, got %q", got)
	}
	if _, err := os.Lstat(filepath.Join(dir, "added.txt")); !os.IsNotExist(err) {
		t.Errorf("added.txt 应被删除, err=%v", err)
	}
	if got := readTestFile(t, dir, "changed.txt"); got != "user-v2\n" {
		t.Errorf("changed.txt 应保持用户版本, got %q", got)
	}
}

func TestDeleteCheckpointsRemovesOnlyTargetThread(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner, dir := newRevertTestRepo(t)
	ctx := context.Background()

	if _, err := runner.Checkpoint(ctx, dir, CheckpointRef("thread-a", "turn1")+"/start", ""); err != nil {
		t.Fatalf("Checkpoint thread-a/start: %v", err)
	}
	if _, err := runner.Checkpoint(ctx, dir, CheckpointRef("thread-a", "turn1")+"/end", ""); err != nil {
		t.Fatalf("Checkpoint thread-a/end: %v", err)
	}
	keep, err := runner.Checkpoint(ctx, dir, CheckpointRef("thread-b", "turn1")+"/start", "")
	if err != nil {
		t.Fatalf("Checkpoint thread-b/start: %v", err)
	}

	if err := runner.DeleteCheckpoints(ctx, dir, "thread-a"); err != nil {
		t.Fatalf("DeleteCheckpoints: %v", err)
	}
	if refs := runTestGit(t, dir, "for-each-ref", "--format=%(refname)", "refs/mcode/checkpoints/thread-a/"); refs != "" {
		t.Errorf("thread-a 的检查点引用应被清空, got %q", refs)
	}
	refs := runTestGit(t, dir, "for-each-ref", "--format=%(refname)", "refs/mcode/checkpoints/thread-b/")
	if !strings.Contains(refs, "refs/mcode/checkpoints/thread-b/turn1/start") {
		t.Errorf("thread-b 的检查点引用应保留, got %q", refs)
	}
	if got := runTestGit(t, dir, "rev-parse", CheckpointRef("thread-b", "turn1")+"/start"); got != keep {
		t.Errorf("thread-b 检查点指向的提交应保持不变: got %s want %s", got, keep)
	}
	if err := runner.DeleteCheckpoints(ctx, dir, "thread-none"); err != nil {
		t.Errorf("无线程引用时应返回 nil, got %v", err)
	}
}

func TestPreviewRestoreNormalizesNFDPaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner, dir := newRevertTestRepo(t)
	ctx := context.Background()

	base, err := runner.Checkpoint(ctx, dir, CheckpointRef("t1", "turn1")+"/start", "base")
	if err != nil {
		t.Fatalf("Checkpoint base: %v", err)
	}

	nfdName := norm.NFD.String("café中文.txt")
	nfcName := norm.NFC.String("café中文.txt")
	if nfdName == nfcName {
		t.Fatalf("测试文件名不包含可分解字符: %q", nfcName)
	}
	writeTestFile(t, dir, nfdName, "hello\n")

	finish, err := runner.Checkpoint(ctx, dir, CheckpointRef("t1", "turn1")+"/end", "finish")
	if err != nil {
		t.Fatalf("Checkpoint finish: %v", err)
	}

	preview, err := runner.PreviewRestore(ctx, dir, base, finish)
	if err != nil {
		t.Fatalf("PreviewRestore: %v", err)
	}

	found := false
	for _, f := range preview.Files {
		if NormalizePath(f.Path) != nfcName {
			continue
		}
		found = true
		if f.IsKept {
			t.Errorf("NFD 路径经规范化后应能匹配 blob（IsKept=false）: %+v", f)
		}
		if f.InBase {
			t.Errorf("新增文件不应出现在 base 中: %+v", f)
		}
	}
	if !found {
		t.Fatalf("预览中缺少 NFC 规范化后的路径 %q: %+v", nfcName, preview.Files)
	}
	if NormalizePath(nfdName) != nfcName {
		t.Errorf("NormalizePath 未将 NFD 转换为 NFC: %q", NormalizePath(nfdName))
	}
	if got := TouchedPaths([]FileDiff{{NewPath: nfdName}}); len(got) != 1 || got[0] != nfcName {
		t.Errorf("TouchedPaths 未做 NFC 规范化: %v", got)
	}
}

func TestDeferredPatchExpansion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner, dir := newRevertTestRepo(t)
	ctx := context.Background()

	writeTestFile(t, dir, "small.txt", "one\n")
	base, err := runner.Checkpoint(ctx, dir, CheckpointRef("t1", "turn1")+"/start", "base")
	if err != nil {
		t.Fatalf("Checkpoint base: %v", err)
	}

	var big strings.Builder
	for i := 0; i < 6000; i++ {
		fmt.Fprintf(&big, "line %d\n", i)
	}
	writeTestFile(t, dir, "big.txt", big.String())
	writeTestFile(t, dir, "small.txt", "two\n")
	finish, err := runner.Checkpoint(ctx, dir, CheckpointRef("t1", "turn1")+"/end", "finish")
	if err != nil {
		t.Fatalf("Checkpoint finish: %v", err)
	}

	diffs, err := runner.Diff(ctx, dir, base, finish, nil)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	var smallSeen, bigSeen bool
	for i := range diffs {
		fd := diffs[i]
		switch fd.NewPath {
		case "small.txt":
			smallSeen = true
			if fd.IsDeferred {
				t.Errorf("小文件不应触发延迟解析: %+v", fd)
			}
			if len(fd.Hunks) == 0 {
				t.Errorf("小文件应完整解析出 hunks")
			}
		case "big.txt":
			bigSeen = true
			if !fd.IsDeferred {
				t.Fatalf("big.txt 应触发延迟解析（超过 %d 行）", deferredPatchLineThreshold)
			}
			if len(fd.Hunks) != 0 {
				t.Errorf("延迟解析不应拆分 hunk, got %d", len(fd.Hunks))
			}
			if fd.DeferredPatch == "" {
				t.Fatalf("延迟解析应保存原始补丁文本")
			}
			if fd.Additions != 6000 || fd.Deletions != 0 {
				t.Errorf("延迟解析统计错误: +%d -%d, want +6000 -0", fd.Additions, fd.Deletions)
			}

			expanded, err := ExpandDeferred(fd)
			if err != nil {
				t.Fatalf("ExpandDeferred: %v", err)
			}
			if expanded.IsDeferred || expanded.DeferredPatch != "" {
				t.Errorf("展开后不应保留延迟标记: %+v", expanded)
			}
			additions := 0
			for _, hunk := range expanded.Hunks {
				for _, line := range hunk.Lines {
					if line.Kind == '+' {
						additions++
					}
				}
			}
			if len(expanded.Hunks) == 0 || additions != 6000 {
				t.Errorf("展开后应解析出完整 hunks: hunks=%d additions=%d", len(expanded.Hunks), additions)
			}
			if expanded.Additions != fd.Additions || expanded.Deletions != fd.Deletions {
				t.Errorf("展开后应保留统计: +%d -%d", expanded.Additions, expanded.Deletions)
			}
		}
	}
	if !smallSeen || !bigSeen {
		t.Fatalf("diff 中缺少预期文件: small=%v big=%v", smallSeen, bigSeen)
	}

	// 未标记延迟的文件原样返回
	plain, err := ExpandDeferred(FileDiff{NewPath: "small.txt", Status: "modified"})
	if err != nil || plain.IsDeferred {
		t.Errorf("非延迟文件应原样返回: %+v err=%v", plain, err)
	}
}

// RestoreFiles 按 ls-tree 给出的模式还原：可执行位保留、符号链接重建、子模块跳过；
// ls-tree 本身出错时向上返回，而不是把路径当成「检查点中不存在」删掉工作区文件。
func TestRestoreFilesKeepsFileTypes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner, dir := newRevertTestRepo(t)
	ctx := context.Background()

	writeTestFile(t, dir, "scripts/run.sh", "#!/bin/sh\necho ok\n")
	execPath := filepath.Join(dir, "scripts", "run.sh")
	if err := os.Chmod(execPath, 0755); err != nil {
		t.Fatalf("chmod 失败: %v", err)
	}
	linkPath := filepath.Join(dir, "current")
	if err := os.Symlink("scripts/run.sh", linkPath); err != nil {
		t.Fatalf("创建符号链接失败: %v", err)
	}
	runTestGit(t, dir, "add", "-A")
	runTestGit(t, dir, "commit", "-q", "-m", "types")
	commit := runTestGit(t, dir, "rev-parse", "HEAD")

	// 破坏文件类型：脚本丢失可执行位，符号链接被换成普通文件
	if err := os.WriteFile(execPath, []byte("broken\n"), 0644); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := os.Remove(linkPath); err != nil {
		t.Fatalf("删除链接失败: %v", err)
	}
	writeTestFile(t, dir, "current", "scripts/run.sh\n")

	if err := runner.RestoreFiles(ctx, dir, commit, []string{"scripts/run.sh", "current"}); err != nil {
		t.Fatalf("RestoreFiles: %v", err)
	}
	info, err := os.Stat(execPath)
	if err != nil {
		t.Fatalf("stat 脚本失败: %v", err)
	}
	if info.Mode().Perm()&0100 == 0 {
		t.Errorf("可执行位应被还原，实际 %v", info.Mode())
	}
	if got := readTestFile(t, dir, "scripts/run.sh"); got != "#!/bin/sh\necho ok\n" {
		t.Errorf("脚本内容未还原: %q", got)
	}
	linkInfo, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("lstat 链接失败: %v", err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("符号链接应重建为链接，实际 %v", linkInfo.Mode())
	}
	if target, err := os.Readlink(linkPath); err != nil || target != "scripts/run.sh" {
		t.Errorf("链接目标 = %q, %v", target, err)
	}

	// 子模块（gitlink）跳过：不能把 commit 对象当文件内容写进工作区
	runTestGit(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+commit+",vendor/sub")
	runTestGit(t, dir, "commit", "-q", "-m", "add gitlink")
	gitlinkCommit := runTestGit(t, dir, "rev-parse", "HEAD")
	if err := runner.RestoreFiles(ctx, dir, gitlinkCommit, []string{"vendor/sub"}); err != nil {
		t.Fatalf("跳过子模块不应报错: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "vendor", "sub")); !os.IsNotExist(err) {
		t.Errorf("子模块不应被写成普通文件: %v", err)
	}

	// 检查点不可读时返回错误，不能把工作区文件当成新增文件删掉
	if err := runner.RestoreFiles(ctx, dir, "0000000000000000000000000000000000000000", []string{"seed.txt"}); err == nil {
		t.Errorf("不存在的检查点应返回错误")
	}
	if _, err := os.Stat(filepath.Join(dir, "seed.txt")); err != nil {
		t.Errorf("检查点不可读时不应删除工作区文件: %v", err)
	}
}
