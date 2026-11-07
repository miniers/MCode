package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrNotRepo 表示当前目录不在 Git 仓库内。
var ErrNotRepo = errors.New("not a git repository")

// StatusEntry 表示 git status 输出中单个文件的状态信息。
type StatusEntry struct {
	Path     string // 文件当前路径
	OrigPath string // 重命名或复制时的源文件路径（如无则为空）
	Index    byte   // 暂存区状态字符（'M', 'A', 'D', 'R', ' ', '?' 等）
	Worktree byte   // 工作区状态字符（'M', 'A', 'D', 'R', ' ', '?' 等）
}

// RepoRoot 探测并返回包含 dir 的 Git 仓库根目录绝对路径。
// 若当前路径不在任何 Git 仓库内，返回 ErrNotRepo。
func (r *Runner) RepoRoot(ctx context.Context, dir string) (string, error) {
	out, err := r.run(ctx, dir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", ErrNotRepo
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", ErrNotRepo
	}
	return filepath.Clean(root), nil
}

// IsRealRepo 判断 dir 本身是否位于真实 Git 仓库内，忽略已存在的影子仓库。
// RepoRoot 在带 ShadowRoot 的 Runner 上会为已有影子仓库的普通目录注入 GIT_DIR，
// 使这些目录看起来也是仓库；这里改用不带影子环境的 Runner 副本探测，只看目录自身的 .git。
func (r *Runner) IsRealRepo(ctx context.Context, dir string) bool {
	if r == nil || dir == "" {
		return false
	}
	// 副本共用同一把并发信号量，仅清空 ShadowRoot，使 run 不再注入影子仓库环境。
	plain := *r
	plain.ShadowRoot = ""
	_, err := plain.RepoRoot(ctx, dir)
	return err == nil
}

// CurrentBranch 获取指定仓库根目录当前所在的分支名称。
// 若当前处于 detached HEAD 状态，则返回空字符串与 nil 错误。
func (r *Runner) CurrentBranch(ctx context.Context, root string) (string, error) {
	out, err := r.run(ctx, root, nil, "branch", "--show-current")
	if err == nil {
		branch := strings.TrimSpace(string(out))
		if branch != "" {
			return branch, nil
		}
	}

	// 备选回退：通过 symbolic-ref 探测符号引用
	symOut, symErr := r.run(ctx, root, nil, "symbolic-ref", "--short", "-q", "HEAD")
	if symErr == nil {
		branch := strings.TrimSpace(string(symOut))
		if branch != "" {
			return branch, nil
		}
	}

	// detached HEAD 状态下没有分支名称，返回空字符串
	return "", nil
}

// Status 获取工作区当前的文件变更状态列表。
// 内部使用 `git status --porcelain=v1 -z -uall`，准确支持带空格路径与文件重命名解析。
func (r *Runner) Status(ctx context.Context, root string) ([]StatusEntry, error) {
	out, err := r.run(ctx, root, nil, "status", "--porcelain=v1", "-z", "-uall")
	if err != nil {
		return nil, err
	}

	if len(out) == 0 {
		return []StatusEntry{}, nil
	}

	records := bytes.Split(out, []byte{0})
	var entries []StatusEntry

	for i := 0; i < len(records); i++ {
		rec := records[i]
		if len(rec) < 3 {
			continue
		}
		idxState := rec[0]
		workState := rec[1]
		// rec[2] 为分隔空格，后续为路径
		path := string(rec[3:])
		origPath := ""

		// 当存在重命名 (R) 或复制 (C) 时，紧随其后的下一个以 \0 分隔的字段为源路径
		if idxState == 'R' || idxState == 'C' || workState == 'R' || workState == 'C' {
			if i+1 < len(records) {
				i++
				origPath = string(records[i])
			}
		}

		entries = append(entries, StatusEntry{
			Path:     path,
			OrigPath: origPath,
			Index:    idxState,
			Worktree: workState,
		})
	}

	return entries, nil
}

// Checkout 在指定仓库根目录切换本地分支，不猜测远端分支或回退为文件恢复。
// branch 为空或以 '-' 开头（防止被当作命令行选项）时返回错误。
func (r *Runner) Checkout(ctx context.Context, root, branch string) error {
	if branch == "" || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("invalid branch name: %q", branch)
	}
	_, err := r.run(ctx, root, nil, "switch", "--no-guess", branch)
	return err
}

// Commit 暂存工作区全部改动（git add -A）后以 message 创建一次提交。
// message 去除首尾空白后为空时返回错误。
func (r *Runner) Commit(ctx context.Context, root, message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return errors.New("commit message is empty")
	}
	if _, err := r.run(ctx, root, nil, "add", "-A"); err != nil {
		return err
	}
	_, err := r.run(ctx, root, nil, "commit", "-m", message)
	return err
}

// Push 推送当前分支。已配置上游时显式推送到上游的 remote 与目标 ref；
// 否则执行 git push --set-upstream origin <branch>。detached HEAD 下返回错误。
func (r *Runner) Push(ctx context.Context, root string) error {
	branch, err := r.CurrentBranch(ctx, root)
	if err != nil {
		return err
	}
	if branch == "" {
		return errors.New("cannot push in detached HEAD state")
	}
	out, err := r.run(ctx, root, nil, "for-each-ref", "--format=%(upstream:remotename)%00%(upstream:remoteref)", "refs/heads/"+branch)
	if err != nil {
		return err
	}
	remote, ref, ok := strings.Cut(strings.TrimSpace(string(out)), "\x00")
	if ok && remote != "" && ref != "" {
		_, err = r.run(ctx, root, nil, "push", "--", remote, "HEAD:"+ref)
		return err
	}
	_, err = r.run(ctx, root, nil, "push", "--set-upstream", "--", "origin", "HEAD:refs/heads/"+branch)
	return err
}

// CreateBranch 在指定目录创建并切换到新分支（git switch -c <name>）。
// name 为空或以 '-' 开头时返回错误。
func (r *Runner) CreateBranch(ctx context.Context, dir, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || strings.HasPrefix(name, "-") {
		return fmt.Errorf("invalid branch name: %q", name)
	}
	_, err := r.run(ctx, dir, nil, "switch", "-c", name)
	return err
}

// RemoteURL 获取指定仓库 origin 远端的 URL。若获取失败或不存在返回空字符串与 error。
func (r *Runner) RemoteURL(ctx context.Context, dir string) (string, error) {
	out, err := r.run(ctx, dir, nil, "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
