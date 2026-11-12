package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpStateOperations(t *testing.T) {
	for _, kind := range []OpKind{OpMerge, OpRebase, OpCherryPick, OpRevert} {
		t.Run(string(kind), func(t *testing.T) {
			r, dir := setupTestRepo(t)
			ctx := context.Background()
			write := func(s string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(s), 0644); err != nil {
					t.Fatal(err)
				}
			}
			branch := strings.TrimSpace(runGitOutput(t, r, dir, "branch", "--show-current"))
			runGitOutput(t, r, dir, "checkout", "-b", "side")
			write("side\n")
			runGitOutput(t, r, dir, "commit", "-am", "side commit")
			sha := strings.TrimSpace(runGitOutput(t, r, dir, "rev-parse", "HEAD"))
			runGitOutput(t, r, dir, "checkout", branch)
			write("main\n")
			runGitOutput(t, r, dir, "commit", "-am", "main commit")
			head := strings.TrimSpace(runGitOutput(t, r, dir, "rev-parse", "HEAD"))
			var err error
			switch kind {
			case OpMerge:
				_, err = r.run(ctx, dir, nil, "merge", "side")
			case OpRebase:
				err = r.Rebase(ctx, dir, "side")
			case OpCherryPick:
				err = r.CherryPick(ctx, dir, sha)
			case OpRevert:
				err = r.RevertCommit(ctx, dir, sha)
			}
			if err == nil {
				t.Fatal("应当产生冲突")
			}
			state, err := r.OpState(ctx, dir)
			if err != nil || state.Kind != kind || state.Branch != branch || state.Onto == "" || len(state.Conflicts) != 1 {
				t.Fatalf("状态：%+v %v", state, err)
			}
			if kind == OpRebase && (state.Step != 1 || state.Total != 1 || state.OursLabel != "上游 "+state.Onto || state.TheirsLabel != "你的提交") {
				t.Fatalf("rebase 状态：%+v", state)
			}
			if err := r.ContinueOp(ctx, dir); err == nil || !strings.Contains(err.Error(), "还有 1 个文件有冲突") {
				t.Fatalf("继续：%v", err)
			}
			if kind == OpMerge && r.SkipOp(ctx, dir) == nil {
				t.Fatal("merge 不应支持跳过")
			}
			if err := r.AbortOp(ctx, dir); err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(runGitOutput(t, r, dir, "rev-parse", "HEAD")); got != head {
				t.Fatalf("中止后 HEAD：%s", got)
			}
			state, err = r.OpState(ctx, dir)
			if err != nil || state.Kind != "" {
				t.Fatalf("中止后状态：%+v %v", state, err)
			}
			switch kind {
			case OpMerge:
				_, err = r.run(ctx, dir, nil, "merge", "side")
			case OpRebase:
				err = r.Rebase(ctx, dir, "side")
			case OpCherryPick:
				err = r.CherryPick(ctx, dir, sha)
			case OpRevert:
				err = r.RevertCommit(ctx, dir, sha)
			}
			if err == nil {
				t.Fatal("应当再次冲突")
			}
			if err = r.ResolveFileSide(ctx, dir, "README.md", false); err != nil {
				t.Fatal(err)
			}
			if err = r.ContinueOp(ctx, dir); err != nil {
				t.Fatal(err)
			}
			state, err = r.OpState(ctx, dir)
			if err != nil || state.Kind != "" {
				t.Fatalf("继续后状态：%+v %v", state, err)
			}
		})
	}
}
func TestResetModes(t *testing.T) {
	for _, mode := range []string{"soft", "mixed", "hard"} {
		t.Run(mode, func(t *testing.T) {
			r, dir := setupTestRepo(t)
			ctx := context.Background()
			p := filepath.Join(dir, "README.md")
			if err := os.WriteFile(p, []byte("changed\n"), 0644); err != nil {
				t.Fatal(err)
			}
			runGitOutput(t, r, dir, "commit", "-am", "change")
			if err := r.Reset(ctx, dir, "HEAD~1", mode); err != nil {
				t.Fatal(err)
			}
			out := runGitOutput(t, r, dir, "status", "--porcelain")
			if (mode == "hard") != (out == "") {
				t.Fatalf("状态：%q", out)
			}
			if mode == "soft" && !strings.HasPrefix(out, "M ") {
				t.Fatalf("状态：%q", out)
			}
			if mode == "mixed" && !strings.HasPrefix(out, " M") {
				t.Fatalf("状态：%q", out)
			}
		})
	}
	r, dir := setupTestRepo(t)
	if r.Reset(context.Background(), dir, "HEAD", "bad") == nil {
		t.Fatal("应拒绝非法模式")
	}
}

func TestSkipOpRebase(t *testing.T) {
	r, dir := setupTestRepo(t)
	ctx := context.Background()
	branch := strings.TrimSpace(runGitOutput(t, r, dir, "branch", "--show-current"))
	runGitOutput(t, r, dir, "checkout", "-b", "side")
	p := filepath.Join(dir, "README.md")
	if err := os.WriteFile(p, []byte("side\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitOutput(t, r, dir, "commit", "-am", "side")
	runGitOutput(t, r, dir, "checkout", branch)
	if err := os.WriteFile(p, []byte("main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitOutput(t, r, dir, "commit", "-am", "main")
	if err := r.Rebase(ctx, dir, "side"); err == nil {
		t.Fatal("应当冲突")
	}
	if err := r.SkipOp(ctx, dir); err != nil {
		t.Fatal(err)
	}
	state, err := r.OpState(ctx, dir)
	if err != nil || state.Kind != "" {
		t.Fatalf("跳过后状态：%+v %v", state, err)
	}
}
