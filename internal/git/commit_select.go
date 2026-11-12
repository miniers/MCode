package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ChangedFile 表示工作区里一个可供勾选提交的改动文件。
type ChangedFile struct {
	Path     string // 文件当前路径（相对仓库根目录）
	OrigPath string // 重命名或复制时的来源路径，如无则为空
	Status   string // 单字符状态：M / A / D / R / ? 等
}

// ChangedFiles 列出仓库工作区里的全部改动文件。
// 解析沿用 Status 的 `git status --porcelain=v1 -z -uall` 结果，因此带空格或引号的路径保持原样，
// 重命名与复制会一并给出来源路径。
func (r *Runner) ChangedFiles(ctx context.Context, root string) ([]ChangedFile, error) {
	entries, err := r.Status(ctx, root)
	if err != nil {
		return nil, err
	}

	files := make([]ChangedFile, 0, len(entries))
	for _, entry := range entries {
		files = append(files, ChangedFile{
			Path:     entry.Path,
			OrigPath: entry.OrigPath,
			Status:   changedFileStatus(entry),
		})
	}
	return files, nil
}

// changedFileStatus 把 status 的两个状态字符压成一个：优先取工作区状态，
// 工作区干净（改动已全部暂存）时取暂存区状态；未跟踪文件统一记为 "?"。
// 重命名/复制优先于工作区修改状态：`git status --porcelain=v1` 对“重命名后又改了内容”
// 给出 Index='R'、Worktree='M'，此时应展示 R（或 C），而不是被误判成普通修改。
func changedFileStatus(entry StatusEntry) string {
	switch {
	case entry.Index == '?' || entry.Worktree == '?':
		return "?"
	case entry.Index == 'R' || entry.Index == 'C':
		return string(entry.Index)
	case entry.Worktree == 'R' || entry.Worktree == 'C':
		return string(entry.Worktree)
	case entry.Worktree != ' ' && entry.Worktree != 0:
		return string(entry.Worktree)
	case entry.Index != ' ' && entry.Index != 0:
		return string(entry.Index)
	default:
		return "?"
	}
}

// CommitSelected 只提交 paths 指定的路径，重命名需要同时包含新旧路径。
// 只暂存并提交所选路径，保留其他路径已有的暂存状态。
func (r *Runner) CommitSelected(ctx context.Context, root string, paths []string, message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return errors.New("提交信息不能为空")
	}
	paths = normalizePaths(paths)
	if len(paths) == 0 {
		return errors.New("提交路径不能为空")
	}

	var addPaths []string
	for _, path := range paths {
		rel, err := cleanAndVerifyPath(root, path)
		if err != nil {
			return err
		}
		// 已暂存的删除或重命名旧路径不再属于索引，不能再次 add。
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); os.IsNotExist(err) {
			tracked, err := r.run(ctx, root, []string{"GIT_LITERAL_PATHSPECS=1"}, "ls-files", "-z", "--", rel)
			if err != nil {
				return err
			}
			if len(tracked) == 0 {
				continue
			}
		} else if err != nil {
			return err
		}
		addPaths = append(addPaths, rel)
	}
	if len(addPaths) > 0 {
		if err := r.StageFiles(ctx, root, addPaths); err != nil {
			return fmt.Errorf("暂存所选路径: %w", err)
		}
	}
	args := append([]string{"commit", "--only", "-m", message, "--"}, paths...)
	_, err := r.run(ctx, root, []string{"GIT_LITERAL_PATHSPECS=1"}, args...)
	return err
}
