package app

import (
	"context"
	"path/filepath"
	"testing"

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
