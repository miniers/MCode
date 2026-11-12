package git

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type BranchInfo struct {
	Name, FullRef string
	Remote, Head  bool
	Upstream      string
	Ahead, Behind int
	UpstreamGone  bool
	WorktreePath  string
	CommitTime    time.Time
	Subject       string
}
type TagInfo struct {
	Name, SHA, Subject string
	Annotated          bool
	Time               time.Time
}
type RemoteInfo struct{ Name, FetchURL, PushURL string }

func (r *Runner) Branches(ctx context.Context, root string) ([]BranchInfo, error) {
	out, err := r.run(ctx, root, nil, "for-each-ref", "--format=%(refname)%00%(refname:short)%00%(HEAD)%00%(upstream:short)%00%(upstream:track,nobracket)%00%(worktreepath)%00%(committerdate:unix)%00%(subject)", "refs/heads", "refs/remotes")
	if err != nil {
		return nil, err
	}
	top, err := r.run(ctx, root, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	own := strings.TrimSpace(string(top))
	own, _ = filepath.EvalSymlinks(own)
	result := []BranchInfo{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		p := strings.SplitN(line, "\x00", 8)
		if len(p) != 8 {
			continue
		}
		remote := strings.HasPrefix(p[0], "refs/remotes/")
		if remote && strings.HasSuffix(p[0], "/HEAD") {
			continue
		}
		b := BranchInfo{FullRef: p[0], Name: p[1], Remote: remote, Head: p[2] == "*", Upstream: p[3], UpstreamGone: strings.Contains(p[4], "gone"), WorktreePath: p[5], Subject: p[7]}
		for _, part := range strings.Split(p[4], ", ") {
			if strings.HasPrefix(part, "ahead ") {
				b.Ahead, _ = strconv.Atoi(strings.TrimPrefix(part, "ahead "))
			}
			if strings.HasPrefix(part, "behind ") {
				b.Behind, _ = strconv.Atoi(strings.TrimPrefix(part, "behind "))
			}
		}
		if b.WorktreePath != "" {
			path, e := filepath.EvalSymlinks(b.WorktreePath)
			if e == nil && path == own {
				b.WorktreePath = ""
			}
		}
		if n, e := strconv.ParseInt(p[6], 10, 64); e == nil {
			b.CommitTime = time.Unix(n, 0)
		}
		result = append(result, b)
	}
	return result, nil
}
func (r *Runner) RecentBranches(ctx context.Context, root string, limit int) ([]string, error) {
	if limit <= 0 {
		return []string{}, nil
	}
	branches, err := r.Branches(ctx, root)
	if err != nil {
		return nil, err
	}
	exists := map[string]bool{}
	for _, b := range branches {
		if !b.Remote {
			exists[b.Name] = true
		}
	}
	out, err := r.run(ctx, root, nil, "reflog", "show", "--format=%gs", "HEAD")
	if err != nil {
		return nil, err
	}
	result := []string{}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "checkout: moving from ") {
			continue
		}
		_, name, ok := strings.Cut(strings.TrimPrefix(line, "checkout: moving from "), " to ")
		if ok && exists[name] && !seen[name] {
			result = append(result, name)
			seen[name] = true
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}
func validateArgs(args ...string) error {
	for _, a := range args {
		if err := validBranchArg(a); err != nil {
			return err
		}
	}
	return nil
}
func (r *Runner) BranchRename(ctx context.Context, root, old, new string) error {
	if e := validateArgs(old, new); e != nil {
		return e
	}
	_, e := r.run(ctx, root, nil, "branch", "-m", "--", old, new)
	return e
}
func (r *Runner) SetUpstream(ctx context.Context, root, branch, upstream string) error {
	if e := validateArgs(branch, upstream); e != nil {
		return e
	}
	_, e := r.run(ctx, root, nil, "branch", "--set-upstream-to="+upstream, "--", branch)
	return e
}
func (r *Runner) UnsetUpstream(ctx context.Context, root, branch string) error {
	if e := validateArgs(branch); e != nil {
		return e
	}
	_, e := r.run(ctx, root, nil, "branch", "--unset-upstream", "--", branch)
	return e
}
func (r *Runner) CreateBranchAt(ctx context.Context, root, name, startPoint string, checkout bool) error {
	if e := validateArgs(name, startPoint); e != nil {
		return e
	}
	args := []string{"branch", "--", name, startPoint}
	if checkout {
		args = []string{"checkout", "-b", name, startPoint, "--"}
	}
	_, e := r.run(ctx, root, nil, args...)
	return e
}
func (r *Runner) CheckoutRemoteBranch(ctx context.Context, root, remoteRef string) (string, error) {
	if e := validateArgs(remoteRef); e != nil {
		return "", e
	}
	_, name, ok := strings.Cut(remoteRef, "/")
	if !ok || validateArgs(name) != nil {
		return "", errors.New("远程分支名称无效")
	}
	_, e := r.run(ctx, root, nil, "checkout", "-b", name, "--track", remoteRef, "--")
	return name, e
}
func (r *Runner) DeleteRemoteBranch(ctx context.Context, root, remote, branch string) error {
	if e := validateArgs(remote, branch); e != nil {
		return e
	}
	_, e := r.run(ctx, root, nil, "push", "--", remote, ":refs/heads/"+branch)
	return e
}
func (r *Runner) Tags(ctx context.Context, root string) ([]TagInfo, error) {
	out, e := r.run(ctx, root, nil, "for-each-ref", "--format=%(refname:strip=2)%00%(objectname)%00%(objecttype)%00%(creatordate:unix)%00%(subject)", "refs/tags")
	if e != nil {
		return nil, e
	}
	tags := []TagInfo{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		p := strings.SplitN(line, "\x00", 5)
		if len(p) != 5 {
			continue
		}
		tag := TagInfo{Name: p[0], SHA: p[1], Annotated: p[2] == "tag", Subject: p[4]}
		if n, e := strconv.ParseInt(p[3], 10, 64); e == nil {
			tag.Time = time.Unix(n, 0)
		}
		tags = append(tags, tag)
	}
	return tags, nil
}
func (r *Runner) TagCreate(ctx context.Context, root, name, rev, message string) error {
	if e := validateArgs(name, rev); e != nil {
		return e
	}
	args := []string{"tag"}
	if message != "" {
		args = append(args, "-a", "-m", message)
	}
	args = append(args, "--", name, rev)
	_, e := r.run(ctx, root, nil, args...)
	return e
}
func (r *Runner) TagDelete(ctx context.Context, root, name string) error {
	if e := validateArgs(name); e != nil {
		return e
	}
	_, e := r.run(ctx, root, nil, "tag", "-d", "--", name)
	return e
}
func (r *Runner) PushTag(ctx context.Context, root, remote, name string) error {
	if e := validateArgs(remote, name); e != nil {
		return e
	}
	_, e := r.run(ctx, root, nil, "push", "--", remote, "refs/tags/"+name+":refs/tags/"+name)
	return e
}
func (r *Runner) DeleteRemoteTag(ctx context.Context, root, remote, name string) error {
	if e := validateArgs(remote, name); e != nil {
		return e
	}
	_, e := r.run(ctx, root, nil, "push", "--", remote, ":refs/tags/"+name)
	return e
}
func (r *Runner) Remotes(ctx context.Context, root string) ([]RemoteInfo, error) {
	out, e := r.run(ctx, root, nil, "remote")
	if e != nil {
		return nil, e
	}
	result := []RemoteInfo{}
	for _, name := range strings.Fields(string(out)) {
		fetch, e := r.run(ctx, root, nil, "remote", "get-url", "--", name)
		if e != nil {
			return nil, e
		}
		push, e := r.run(ctx, root, nil, "remote", "get-url", "--push", "--", name)
		if e != nil {
			return nil, e
		}
		result = append(result, RemoteInfo{name, strings.TrimSpace(string(fetch)), strings.TrimSpace(string(push))})
	}
	return result, nil
}
func (r *Runner) RemoteAdd(ctx context.Context, root, name, url string) error {
	if e := validateArgs(name, url); e != nil {
		return e
	}
	_, e := r.run(ctx, root, nil, "remote", "add", "--", name, url)
	return e
}
func (r *Runner) RemoteRemove(ctx context.Context, root, name string) error {
	if e := validateArgs(name); e != nil {
		return e
	}
	_, e := r.run(ctx, root, nil, "remote", "remove", "--", name)
	return e
}
func (r *Runner) RemoteRename(ctx context.Context, root, old, new string) error {
	if e := validateArgs(old, new); e != nil {
		return e
	}
	_, e := r.run(ctx, root, nil, "remote", "rename", "--", old, new)
	return e
}
func (r *Runner) RemoteSetURL(ctx context.Context, root, name, url string) error {
	if e := validateArgs(name, url); e != nil {
		return e
	}
	_, e := r.run(ctx, root, nil, "remote", "set-url", "--", name, url)
	return e
}
func (r *Runner) Pull(ctx context.Context, root, strategy string) error {
	branch, e := r.CurrentBranch(ctx, root)
	if e != nil {
		return e
	}
	if branch == "" {
		return errors.New("分离 HEAD 状态无法拉取")
	}
	_, _, _, ok, e := r.gitMenuUpstream(ctx, root, branch)
	if e != nil {
		return e
	}
	if !ok {
		return fmt.Errorf("分支 %s 没有配置上游，无法拉取", branch)
	}
	if strategy == "" {
		out, err := r.run(ctx, root, nil, "config", "--bool", "--get", "pull.rebase")
		if err != nil && !isExitCode(err, 1) {
			return err
		}
		if strings.TrimSpace(string(out)) == "true" {
			strategy = "rebase"
		} else {
			strategy = "ff-only"
		}
	}
	args := []string{"pull"}
	switch strategy {
	case "ff-only":
		args = append(args, "--ff-only")
	case "rebase":
		args = append(args, "--rebase", "--autostash")
	case "merge":
		args = append(args, "--no-rebase", "--no-edit")
	default:
		return fmt.Errorf("无效的拉取策略：%s", strategy)
	}
	_, e = r.run(ctx, root, nil, args...)
	return e
}
func (r *Runner) PushForceWithLease(ctx context.Context, root string) error {
	branch, e := r.CurrentBranch(ctx, root)
	if e != nil {
		return e
	}
	if branch == "" {
		return errors.New("分离 HEAD 状态无法推送")
	}
	remote, remoteBranch, ref, ok, e := r.gitMenuUpstream(ctx, root, branch)
	if e != nil {
		return e
	}
	if !ok {
		remote = "origin"
		remoteBranch = branch
		ref = remote + "/" + branch
	}
	remoteRef := "refs/heads/" + remoteBranch
	out, e := r.run(ctx, root, nil, "rev-parse", "--verify", "--quiet", "refs/remotes/"+ref)
	sha := ""
	if e == nil {
		sha = strings.TrimSpace(string(out))
	} else if !isExitCode(e, 1) {
		return e
	}
	args := []string{"push", "--force-with-lease=" + remoteRef + ":" + sha}
	if !ok {
		args = append(args, "--set-upstream")
	}
	args = append(args, "--", remote, "HEAD:"+remoteRef)
	_, e = r.run(ctx, root, nil, args...)
	return e
}
