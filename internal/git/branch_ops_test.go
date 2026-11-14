package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBranchManagement(t *testing.T) {
	t.Parallel()
	r, dir, bare := initSyncRemote(t)
	ctx := context.Background()
	if e := r.CreateBranchAt(ctx, dir, "feature", "HEAD", true); e != nil {
		t.Fatal(e)
	}
	if e := r.BranchRename(ctx, dir, "feature", "renamed"); e != nil {
		t.Fatal(e)
	}
	if e := r.SetUpstream(ctx, dir, "renamed", "origin/main"); e != nil {
		t.Fatal(e)
	}
	runTestGit(t, dir, "commit", "--allow-empty", "-m", "ahead")
	branches, e := r.Branches(ctx, dir)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, b := range branches {
		if b.Name == "renamed" {
			found = true
			if b.Ahead != 1 || !b.Head || b.Upstream != "origin/main" {
				t.Fatalf("%+v", b)
			}
		}
	}
	if !found {
		t.Fatal("分支不存在")
	}
	peer := initSecondaryCheckout(t, bare)
	runTestGit(t, peer, "commit", "--allow-empty", "-m", "remote")
	runTestGit(t, peer, "push", "origin", "HEAD:main")
	runTestGit(t, dir, "fetch", "origin")
	branches, e = r.Branches(ctx, dir)
	if e != nil {
		t.Fatal(e)
	}
	for _, b := range branches {
		if b.Name == "renamed" && (b.Ahead != 1 || b.Behind != 1) {
			t.Fatalf("%+v", b)
		}
	}
	if e = r.UnsetUpstream(ctx, dir, "renamed"); e != nil {
		t.Fatal(e)
	}
	runTestGit(t, dir, "checkout", "main")
	recent, e := r.RecentBranches(ctx, dir, 3)
	if e != nil || len(recent) == 0 || recent[0] != "main" {
		t.Fatalf("%v %v", recent, e)
	}
	other := filepath.Join(t.TempDir(), "other")
	runTestGit(t, dir, "worktree", "add", other, "renamed")
	branches, e = r.Branches(ctx, dir)
	if e != nil {
		t.Fatal(e)
	}
	for _, b := range branches {
		resolvedOther, _ := filepath.EvalSymlinks(other)
		resolvedPath, _ := filepath.EvalSymlinks(b.WorktreePath)
		if b.Name == "renamed" && resolvedPath != resolvedOther {
			t.Fatalf("%+v", b)
		}
	}
	if e = r.SetUpstream(ctx, dir, "renamed", "origin/main"); e != nil {
		t.Fatal(e)
	}
	runTestGit(t, dir, "update-ref", "-d", "refs/remotes/origin/main")
	branches, e = r.Branches(ctx, dir)
	if e != nil {
		t.Fatal(e)
	}
	for _, b := range branches {
		if b.Name == "renamed" && !b.UpstreamGone {
			t.Fatalf("%+v", b)
		}
	}
	if e = r.RemoteAdd(ctx, dir, "extra", bare); e != nil {
		t.Fatal(e)
	}
	if e = r.RemoteRename(ctx, dir, "extra", "other"); e != nil {
		t.Fatal(e)
	}
	if e = r.RemoteSetURL(ctx, dir, "other", bare); e != nil {
		t.Fatal(e)
	}
	remotes, e := r.Remotes(ctx, dir)
	if e != nil || len(remotes) != 2 {
		t.Fatalf("%v %v", remotes, e)
	}
	if e = r.RemoteRemove(ctx, dir, "other"); e != nil {
		t.Fatal(e)
	}
}
func TestTagManagement(t *testing.T) {
	t.Parallel()
	r, dir, _ := initSyncRemote(t)
	ctx := context.Background()
	if e := r.TagCreate(ctx, dir, "v1", "HEAD", "版本一"); e != nil {
		t.Fatal(e)
	}
	tags, e := r.Tags(ctx, dir)
	if e != nil || len(tags) != 1 || !tags[0].Annotated {
		t.Fatalf("%v %v", tags, e)
	}
	if e = r.PushTag(ctx, dir, "origin", "v1"); e != nil {
		t.Fatal(e)
	}
	if e = r.DeleteRemoteTag(ctx, dir, "origin", "v1"); e != nil {
		t.Fatal(e)
	}
	if e = r.TagDelete(ctx, dir, "v1"); e != nil {
		t.Fatal(e)
	}
}
func TestPullStrategies(t *testing.T) {
	t.Parallel()
	for _, strategy := range []string{"", "ff-only", "rebase", "merge"} {
		t.Run(strategy, func(t *testing.T) {
			r, dir, _ := initSyncRemote(t)
			if e := r.Pull(context.Background(), dir, strategy); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestForceLease(t *testing.T) {
	t.Parallel()
	r, dir, bare := initSyncRemote(t)
	ctx := context.Background()
	runTestGit(t, dir, "commit", "--allow-empty", "-m", "local")
	if e := r.PushForceWithLease(ctx, dir); e != nil {
		t.Fatal(e)
	}
	other := initSecondaryCheckout(t, bare)
	runTestGit(t, other, "commit", "--allow-empty", "-m", "other")
	runTestGit(t, other, "push", "origin", "HEAD:main")
	runTestGit(t, dir, "commit", "--amend", "--allow-empty", "--no-edit")
	e := r.PushForceWithLease(ctx, dir)
	if e == nil || ClassifyError(e).Kind != FailureLease {
		t.Fatalf("%v", e)
	}
	runTestGit(t, dir, "fetch", "origin")
	if e = r.PushForceWithLease(ctx, dir); e != nil {
		t.Fatal(e)
	}
}
func TestRemoteBranchOperations(t *testing.T) {
	t.Parallel()
	r, dir, bare := initSyncRemote(t)
	ctx := context.Background()
	runTestGit(t, dir, "push", "origin", "HEAD:refs/heads/other")
	runTestGit(t, dir, "fetch", "origin")
	name, e := r.CheckoutRemoteBranch(ctx, dir, "origin/other")
	if e != nil || name != "other" {
		t.Fatalf("%q %v", name, e)
	}
	if e = r.DeleteRemoteBranch(ctx, dir, "origin", "other"); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(bare, "refs", "heads", "other")); !os.IsNotExist(e) {
		t.Fatalf("远程分支仍存在: %v", e)
	}
	if e = r.BranchRename(ctx, dir, "other", "-bad"); e == nil || !strings.Contains(e.Error(), "invalid") {
		t.Fatal(e)
	}
}
