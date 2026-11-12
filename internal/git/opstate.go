package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type OpKind string

const (
	OpMerge      OpKind = "merge"
	OpRebase     OpKind = "rebase"
	OpCherryPick OpKind = "cherry-pick"
	OpRevert     OpKind = "revert"
)

type OpState struct {
	Kind                   OpKind
	Branch, Onto           string
	Step, Total            int
	Conflicts              []ConflictFile
	OursLabel, TheirsLabel string
}

var opEnv = []string{"GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true"}

func (r *Runner) OpState(ctx context.Context, root string) (OpState, error) {
	var state OpState
	out, err := r.run(ctx, root, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return state, err
	}
	dir := strings.TrimSpace(string(out))
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		return strings.TrimSpace(string(b))
	}
	exists := func(name string) bool { _, e := os.Stat(filepath.Join(dir, name)); return e == nil }
	branch, _ := r.run(ctx, root, nil, "branch", "--show-current")
	state.Branch = strings.TrimSpace(string(branch))
	rebaseDir := ""
	if exists("rebase-merge") {
		rebaseDir = "rebase-merge"
	} else if exists("rebase-apply") {
		rebaseDir = "rebase-apply"
	}
	switch {
	case rebaseDir != "":
		state.Kind = OpRebase
		state.Branch = strings.TrimPrefix(read(rebaseDir+"/head-name"), "refs/heads/")
		state.Step, _ = strconv.Atoi(read(rebaseDir + "/msgnum"))
		state.Total, _ = strconv.Atoi(read(rebaseDir + "/end"))
		if state.Step == 0 {
			state.Step, _ = strconv.Atoi(read(rebaseDir + "/next"))
			state.Total, _ = strconv.Atoi(read(rebaseDir + "/last"))
		}
		state.Onto = r.opRef(ctx, root, read(rebaseDir+"/onto"))
		state.OursLabel = "上游 " + state.Onto
		state.TheirsLabel = "你的提交"
	case exists("MERGE_HEAD"):
		state.Kind = OpMerge
		state.Onto = r.opRef(ctx, root, strings.SplitN(read("MERGE_HEAD"), "\n", 2)[0])
		msg := strings.Split(read("MERGE_MSG"), "\n")[0]
		if strings.HasPrefix(msg, "Merge branch '") {
			if x := strings.SplitN(strings.TrimPrefix(msg, "Merge branch '"), "'", 2); len(x) > 1 {
				state.Onto = x[0]
			}
		}
		state.OursLabel = state.Branch
		state.TheirsLabel = state.Onto
	case exists("CHERRY_PICK_HEAD"):
		state.Kind = OpCherryPick
		state.Onto = r.opCommit(ctx, root, read("CHERRY_PICK_HEAD"))
		state.OursLabel = state.Branch
		state.TheirsLabel = strings.SplitN(state.Onto, " ", 2)[0]
	case exists("REVERT_HEAD"):
		state.Kind = OpRevert
		state.Onto = r.opCommit(ctx, root, read("REVERT_HEAD"))
		state.OursLabel = state.Branch
		state.TheirsLabel = strings.SplitN(state.Onto, " ", 2)[0]
	default:
		return OpState{}, nil
	}
	state.Conflicts, err = r.ConflictFiles(ctx, root)
	return state, err
}
func (r *Runner) opRef(ctx context.Context, root, rev string) string {
	if rev == "" {
		return ""
	}
	out, err := r.run(ctx, root, nil, "name-rev", "--name-only", "--no-undefined", rev)
	if err == nil {
		s := strings.TrimSpace(string(out))
		if !strings.ContainsAny(s, "~^") {
			return strings.TrimPrefix(s, "remotes/")
		}
	}
	out, err = r.run(ctx, root, nil, "rev-parse", "--short", rev)
	if err != nil {
		return rev
	}
	return strings.TrimSpace(string(out))
}
func (r *Runner) opCommit(ctx context.Context, root, rev string) string {
	out, err := r.run(ctx, root, nil, "show", "-s", "--format=%h %s", rev)
	if err != nil {
		return rev
	}
	return strings.TrimSpace(string(out))
}
func validOpArg(s string) error {
	if s == "" || strings.HasPrefix(s, "-") {
		return fmt.Errorf("无效的 Git 引用：%q", s)
	}
	return nil
}
func (r *Runner) Rebase(ctx context.Context, root, onto string) error {
	if err := validOpArg(onto); err != nil {
		return err
	}
	_, err := r.run(ctx, root, opEnv, "rebase", "--autostash", onto)
	return err
}
func (r *Runner) CherryPick(ctx context.Context, root, sha string) error {
	if err := validOpArg(sha); err != nil {
		return err
	}
	_, err := r.run(ctx, root, opEnv, "cherry-pick", sha)
	return err
}
func (r *Runner) RevertCommit(ctx context.Context, root, sha string) error {
	if err := validOpArg(sha); err != nil {
		return err
	}
	_, err := r.run(ctx, root, opEnv, "revert", "--no-edit", sha)
	return err
}
func (r *Runner) Reset(ctx context.Context, root, rev, mode string) error {
	if err := validOpArg(rev); err != nil {
		return err
	}
	switch mode {
	case "soft", "mixed", "hard":
	default:
		return fmt.Errorf("无效的重置模式：%q", mode)
	}
	_, err := r.run(ctx, root, nil, "reset", "--"+mode, rev, "--")
	return err
}
func (r *Runner) opAction(ctx context.Context, root, action string) error {
	state, err := r.OpState(ctx, root)
	if err != nil {
		return err
	}
	if state.Kind == "" {
		return fmt.Errorf("没有进行中的操作")
	}
	if action == "continue" && len(state.Conflicts) > 0 {
		return fmt.Errorf("还有 %d 个文件有冲突", len(state.Conflicts))
	}
	if action == "skip" && state.Kind == OpMerge {
		return fmt.Errorf("合并操作不能跳过")
	}
	_, err = r.run(ctx, root, opEnv, string(state.Kind), "--"+action)
	return err
}
func (r *Runner) ContinueOp(ctx context.Context, root string) error {
	return r.opAction(ctx, root, "continue")
}
func (r *Runner) SkipOp(ctx context.Context, root string) error { return r.opAction(ctx, root, "skip") }
func (r *Runner) AbortOp(ctx context.Context, root string) error {
	return r.opAction(ctx, root, "abort")
}
