package app

import (
	"context"
	"fmt"
	"mcode/internal/git"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/remote/wire"
)

func TestGitStatusEntriesNilBackend(t *testing.T) {
	if _, err := NewClient(nil).GitStatusEntries(context.Background(), "thread"); err == nil {
		t.Fatal("应拒绝不可用的 backend")
	}
}

func TestGitStageAndRestore(t *testing.T) {
	client, _, _, root := newClientRevertEnv(t)
	ctx := context.Background()
	thread, err := client.backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	path := "file.txt"
	writeRevertRepoFile(t, root, path, "暂存内容\n")
	if err := client.GitStageFiles(ctx, thread.ThreadID, []string{path}); err != nil {
		t.Fatal(err)
	}
	entries, err := client.GitStatusEntries(ctx, thread.ThreadID)
	if err != nil || len(entries) != 1 || entries[0].Index != 'A' {
		t.Fatalf("状态不符: %+v %v", entries, err)
	}
	writeRevertRepoFile(t, root, path, "修改内容\n")
	snap, err := client.GitDiscardFiles(ctx, thread.ThreadID, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if got := readRevertRepoFile(t, root, path); got != "暂存内容\n" {
		t.Fatalf("丢弃结果: %q", got)
	}
	other := *snap
	other.Root = filepath.Join(root, "other")
	if err := client.GitRestoreSnapshot(ctx, thread.ThreadID, &other); err == nil {
		t.Fatal("应拒绝其他仓库的快照")
	}
	if err := client.GitRestoreSnapshot(ctx, thread.ThreadID, nil); err == nil {
		t.Fatal("应拒绝空快照")
	}
	if err := client.GitRestoreSnapshot(ctx, thread.ThreadID, snap); err != nil {
		t.Fatal(err)
	}
	if got := readRevertRepoFile(t, root, path); got != "修改内容\n" {
		t.Fatalf("恢复结果: %q", got)
	}
}

func TestGitCommitDiffRootAndOrdinaryCommit(t *testing.T) {
	client, _, runner, root := newClientRevertEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	thread, err := client.backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	branchGit(t, root, "config", "user.name", "测试")
	branchGit(t, root, "config", "user.email", "test@example.com")
	for i, content := range []string{"第一版\n", "第二版\n"} {
		writeRevertRepoFile(t, root, "file.txt", content)
		if err := runner.Commit(ctx, root, fmt.Sprintf("提交 %d", i)); err != nil {
			t.Fatal(err)
		}
		commits, err := client.GitLog(ctx, thread.ThreadID, git.LogOptions{Limit: 1})
		if err != nil || len(commits) != 1 {
			t.Fatalf("读取提交: %v %v", commits, err)
		}
		for _, path := range []string{"", "file.txt"} {
			files, err := client.GitCommitDiff(ctx, thread.ThreadID, commits[0].SHA, path)
			if err != nil || len(files) != 1 || len(files[0].Hunks) != 1 {
				t.Fatalf("第 %d 个提交 %q: %+v %v", i, path, files, err)
			}
			var additions, deletions []string
			for _, line := range files[0].Hunks[0].Lines {
				if line.Kind == '+' {
					additions = append(additions, line.Text)
				}
				if line.Kind == '-' {
					deletions = append(deletions, line.Text)
				}
			}
			if strings.Join(additions, "\n") != strings.TrimSpace(content) {
				t.Fatalf("新增行: %v", additions)
			}
			if i == 0 && len(deletions) != 0 || i == 1 && strings.Join(deletions, "\n") != "第一版" {
				t.Fatalf("删除行: %v", deletions)
			}
		}
		if files, err := client.GitCommitDiff(ctx, thread.ThreadID, commits[0].SHA, "missing.txt"); err != nil || len(files) != 0 {
			t.Fatalf("路径过滤: %+v %v", files, err)
		}
	}
	if _, err := client.GitCommitDiff(ctx, thread.ThreadID, "不存在", ""); err == nil {
		t.Fatal("无效提交应失败")
	}
}
