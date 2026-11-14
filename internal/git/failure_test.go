package git

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestClassifyError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		s string
		k FailureKind
	}{{"Authentication failed", FailureAuthentication}, {"Host key verification failed", FailureHostKey}, {"Could not resolve host", FailureNetwork}, {"[rejected] (fetch first)", FailureRejected}, {"[rejected] (stale info)", FailureLease}, {"protected branch hook declined", FailureProtected}, {"has no upstream branch", FailureNoUpstream}, {"detached HEAD", FailureDetachedHead}, {"pre-push hook failed", FailureHook}, {"Unable to create index.lock", FailureIndexLock}, {"Pulling is not possible because you have unmerged files", FailureConflict}, {"local changes would be overwritten", FailureWorktree}, {"other", FailureOther}}
	for _, c := range cases {
		t.Run(string(c.k), func(t *testing.T) {
			f := ClassifyError(fmt.Errorf("git push: exit status 1: %s https://user:password@example.com", c.s))
			if f.Kind != c.k || strings.Contains(f.Detail, "user:password") || strings.Contains(f.Detail, "exit status") {
				t.Fatalf("%+v", f)
			}
		})
	}
	if ClassifyError(fmt.Errorf("包装: %w", context.Canceled)).Kind != FailureCanceled {
		t.Fatal("取消未分类")
	}
}

func TestClassifyErrorDiverged(t *testing.T) {
	t.Parallel()
	for _, message := range []string{"fatal: Not possible to fast-forward, aborting.", "fatal: Need to specify how to reconcile divergent branches.", "Your branch and 'origin/main' have diverged"} {
		f := ClassifyError(fmt.Errorf("%s", message))
		if f.Kind != FailureDiverged || f.Title != "本地与远端已分叉" || f.Hint != "选择变基或合并来拉取" {
			t.Fatalf("分叉分类 = %+v", f)
		}
	}
	if f := ClassifyError(fmt.Errorf("[rejected] (non-fast-forward): branches have diverged")); f.Kind != FailureRejected {
		t.Fatalf("推送拒绝被分叉分类覆盖：%+v", f)
	}
}
