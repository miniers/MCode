package git

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestClassifyError(t *testing.T) {
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
