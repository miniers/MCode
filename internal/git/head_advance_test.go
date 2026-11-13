package git

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestAdvanceHead(t *testing.T) {
	r, root := newRevertTestRepo(t)
	ctx := context.Background()
	writeTestFile(t, root, "a.txt", "base a\n")
	writeTestFile(t, root, "b.txt", "base b\n")
	runTestGit(t, root, "add", ".")
	runTestGit(t, root, "commit", "-qm", "base")
	old := runTestGit(t, root, "rev-parse", "HEAD")
	writeTestFile(t, root, "a.txt", "bee a\n")
	writeTestFile(t, root, "b.txt", "bee b\n")
	runTestGit(t, root, "add", ".")
	tree := runTestGit(t, root, "write-tree")
	sha, err := r.CommitTree(ctx, root, tree, old, "bee")
	if err != nil {
		t.Fatal(err)
	}
	runTestGit(t, root, "read-tree", old)
	writeTestFile(t, root, "b.txt", "user staged\n")
	runTestGit(t, root, "add", "b.txt")
	staged := runTestGit(t, root, "show", ":b.txt")
	kept, err := r.AdvanceHead(ctx, root, old, sha)
	if err != nil || !reflect.DeepEqual(kept, []string{"b.txt"}) {
		t.Fatalf("kept=%v err=%v", kept, err)
	}
	if got := runTestGit(t, root, "rev-parse", "HEAD"); got != sha {
		t.Fatalf("HEAD=%s want=%s", got, sha)
	}
	if got := runTestGit(t, root, "show", ":a.txt"); got != "bee a" {
		t.Fatalf("未暂存路径的 index 未同步: %q", got)
	}
	if got := runTestGit(t, root, "show", ":b.txt"); got != staged {
		t.Fatalf("已暂存路径被改写: %q", got)
	}
	indexPath, err := r.IndexPath(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.AdvanceHead(ctx, root, old, sha)
	if !errors.Is(err, ErrHeadMoved) {
		t.Fatalf("应返回 ErrHeadMoved: %v", err)
	}
	after, err := os.ReadFile(indexPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("CAS 失败后 index 被改写: %v", err)
	}
	if got := runTestGit(t, root, "rev-parse", "HEAD"); got != sha {
		t.Fatalf("CAS 失败后 HEAD 被改写: %s", got)
	}
}

func TestAdvanceHeadClearsStaleIndexLock(t *testing.T) {
	r, root := newRevertTestRepo(t)
	ctx := context.Background()
	writeTestFile(t, root, "a.txt", "base\n")
	runTestGit(t, root, "add", ".")
	runTestGit(t, root, "commit", "-qm", "base")
	old := runTestGit(t, root, "rev-parse", "HEAD")
	writeTestFile(t, root, "a.txt", "bee\n")
	runTestGit(t, root, "add", ".")
	tree := runTestGit(t, root, "write-tree")
	sha, err := r.CommitTree(ctx, root, tree, old, "bee")
	if err != nil {
		t.Fatal(err)
	}
	runTestGit(t, root, "read-tree", old)
	indexPath, err := r.IndexPath(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := indexPath + ".lock"
	if err := os.WriteFile(lockPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-staleIndexLockAge - time.Second)
	if err := os.Chtimes(lockPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	original := gitProcessRunning
	t.Cleanup(func() { gitProcessRunning = original })
	gitProcessRunning = func(context.Context) (bool, error) { return false, nil }
	if _, err := r.AdvanceHead(ctx, root, old, sha); err != nil {
		t.Fatal(err)
	}
	if got := runTestGit(t, root, "rev-parse", "HEAD"); got != sha {
		t.Fatalf("HEAD=%s want=%s", got, sha)
	}
}

func TestAdvanceHeadIndexLocked(t *testing.T) {
	r, root := newRevertTestRepo(t)
	ctx := context.Background()
	writeTestFile(t, root, "a.txt", "base\n")
	runTestGit(t, root, "add", ".")
	runTestGit(t, root, "commit", "-qm", "base")
	old := runTestGit(t, root, "rev-parse", "HEAD")
	writeTestFile(t, root, "a.txt", "bee\n")
	runTestGit(t, root, "add", ".")
	tree := runTestGit(t, root, "write-tree")
	sha, err := r.CommitTree(ctx, root, tree, old, "bee")
	if err != nil {
		t.Fatal(err)
	}
	runTestGit(t, root, "read-tree", old)
	indexPath, err := r.IndexPath(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := indexPath + ".lock"
	if err := os.WriteFile(lockPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AdvanceHead(ctx, root, old, sha); !errors.Is(err, ErrIndexLocked) {
		t.Fatalf("应返回 ErrIndexLocked: %v", err)
	}
	if got := runTestGit(t, root, "rev-parse", "HEAD"); got != old {
		t.Fatalf("锁定时 HEAD=%s want=%s", got, old)
	}
	after, err := os.ReadFile(indexPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("锁定时 index 被改写: %v", err)
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AdvanceHead(ctx, root, old, sha); err != nil {
		t.Fatal(err)
	}
	if got := runTestGit(t, root, "rev-parse", "HEAD"); got != sha {
		t.Fatalf("HEAD=%s want=%s", got, sha)
	}
	if got := runTestGit(t, root, "status", "--short"); got != "" {
		t.Fatalf("工作区未清洁: %q", got)
	}
}
