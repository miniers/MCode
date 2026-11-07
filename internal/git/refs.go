package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Ref 表示 Git 中的一个引用与其指向的对象哈希。
type Ref struct {
	Name   string
	Commit string
}

// captureWorkingTreeForDiff 使用隔离的临时 index 将当前工作区的全部文件状态（含未跟踪新增文件）
// 快照为一棵临时 tree 对象，供 DiffText 对比工作区时使用。全程不修改用户 index 和 HEAD。
func (r *Runner) captureWorkingTreeForDiff(ctx context.Context, root string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "mcode-git-difftext-tree-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir for working tree: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tmpIndex := filepath.Join(tmpDir, "index")
	env := []string{"GIT_INDEX_FILE=" + tmpIndex}

	if r.hasCommits(ctx, root) {
		if _, err := r.run(ctx, root, env, "read-tree", "HEAD"); err != nil {
			return "", fmt.Errorf("difftext read-tree HEAD: %w", err)
		}
	}
	if _, err := r.run(ctx, root, env, "add", "-A", "--", "."); err != nil {
		return "", fmt.Errorf("difftext add working tree: %w", err)
	}
	treeOut, err := r.run(ctx, root, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("difftext write-tree: %w", err)
	}
	return strings.TrimSpace(string(treeOut)), nil
}

// ListRefs 列出匹配指定前缀的 Git 引用，并按 refname 字典序升序排序。
func (r *Runner) ListRefs(ctx context.Context, root, prefix string) ([]Ref, error) {
	args := []string{"for-each-ref", "--format=%(refname)%00%(objectname)"}
	if prefix != "" {
		args = append(args, prefix)
	}

	out, err := r.run(ctx, root, nil, args...)
	if err != nil {
		return nil, err
	}

	lines := bytes.Split(out, []byte{'\n'})
	var refs []Ref
	for _, line := range lines {
		line = bytes.TrimRight(line, "\r")
		if len(line) == 0 {
			continue
		}
		parts := bytes.SplitN(line, []byte{0}, 2)
		if len(parts) == 2 {
			refs = append(refs, Ref{
				Name:   string(parts[0]),
				Commit: string(parts[1]),
			})
		}
	}

	sort.Slice(refs, func(i, j int) bool {
		return refs[i].Name < refs[j].Name
	})

	if refs == nil {
		refs = []Ref{}
	}
	return refs, nil
}

// DiffText 比较两个 commit/ref 之间的统一差异文本。
// 当 to 为空时表示比较目标为当前工作区（通过临时 index 生成 tree 对象进行比较）。
// paths 非空时仅比较这些路径（相对仓库根目录，按字面量匹配）；超过 400 条时分块执行并拼接结果。
// 若 diff 字节数超过 maxBytes（maxBytes > 0），则截断至最后一个完整行，并返回 truncated=true。
func (r *Runner) DiffText(ctx context.Context, root, from, to string, paths []string, maxBytes int) (text string, truncated bool, err error) {
	targetTo := to
	if targetTo == "" {
		worktreeTree, err := r.captureWorkingTreeForDiff(ctx, root)
		if err != nil {
			return "", false, err
		}
		targetTo = worktreeTree
	}

	var out []byte
	if len(paths) == 0 {
		out, err = r.run(ctx, root, nil, diffArgs(from, targetTo, nil, false)...)
		if err != nil {
			return "", false, err
		}
	} else {
		var buf bytes.Buffer
		for _, chunk := range splitPathspecs(paths) {
			chunkOut, runErr := r.run(ctx, root, nil, diffArgs(from, targetTo, chunk, false)...)
			if runErr != nil {
				return "", false, runErr
			}
			buf.Write(chunkOut)
		}
		out = buf.Bytes()
	}

	if maxBytes > 0 && len(out) > maxBytes {
		sliced := out[:maxBytes]
		lastNL := bytes.LastIndexByte(sliced, '\n')
		if lastNL >= 0 {
			return string(sliced[:lastNL+1]), true, nil
		}
		return "", true, nil
	}

	return string(out), false, nil
}

// RevParse 解析指定的引用或提交，并返回其对象哈希。
func (r *Runner) RevParse(ctx context.Context, root, rev string) (string, error) {
	out, err := r.run(ctx, root, nil, "rev-parse", rev)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// BranchDelete 删除指定的分支；force 为 true 时使用 -D 强制删除，否则使用 -d。
func (r *Runner) BranchDelete(ctx context.Context, root, branch string, force bool) error {
	args := []string{"branch"}
	if force {
		args = append(args, "-D")
	} else {
		args = append(args, "-d")
	}
	args = append(args, "--", branch)
	_, err := r.run(ctx, root, nil, args...)
	return err
}
