package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
)

// DiffContext 生成 worktree 相对 base 的完整改动，并启用 -W（--function-context），
// 让每个改动块带上改动所在的外层函数，便于审查模型理解上下文。
//
// 改动范围与 prepareMerge 生成的 MergePatch 一致：先以 base 为基准准备临时索引，
// 再对 worktree 执行 git add -A（paths 非空时只暂存这些路径），最后 diff --cached。
// 全程使用临时 GIT_INDEX_FILE，不触碰真实索引，也不修改工作副本。
// base 为空时按 git 默认行为相对 HEAD；paths 为空时覆盖全部改动。
// 输出超过 maxBytes 时按整行截断并把第二个返回值置为 true；maxBytes <= 0 表示不限制。
func (r *Runner) DiffContext(ctx context.Context, worktree, base string, paths []string, maxBytes int) (string, bool, error) {
	if strings.TrimSpace(worktree) == "" {
		return "", false, fmt.Errorf("diff context: empty worktree")
	}

	tmpDir, err := os.MkdirTemp("", "mcode-diff-index-*")
	if err != nil {
		return "", false, fmt.Errorf("diff context: create temp index dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmpDir, "index")}

	trimmedBase := strings.TrimSpace(base)
	// 用 base 的树初始化临时索引；空 base 对应 HEAD。仓库尚无提交时保持空索引，
	// 后续 add -A 会把所有文件当作新增，与 git diff --cached 在无 HEAD 时的语义接近。
	tree := trimmedBase
	if tree == "" {
		tree = "HEAD"
	}
	if _, err := r.run(ctx, worktree, env, "read-tree", tree); err != nil {
		if trimmedBase != "" {
			return "", false, fmt.Errorf("diff context: read-tree %s: %w", tree, err)
		}
	}

	addArgs := []string{"add", "-A"}
	if len(paths) > 0 {
		addArgs = append(addArgs, "--")
		addArgs = append(addArgs, paths...)
	}
	if _, err := r.run(ctx, worktree, env, addArgs...); err != nil {
		return "", false, fmt.Errorf("diff context: add: %w", err)
	}

	args := []string{"diff", "-W", "--cached", "--no-color", "--no-ext-diff"}
	if trimmedBase != "" {
		args = append(args, trimmedBase)
	}
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	out, err := r.run(ctx, worktree, env, args...)
	if err != nil {
		return "", false, fmt.Errorf("diff context: diff: %w", err)
	}

	text, truncated := truncateDiffLines(string(out), maxBytes)
	return text, truncated, nil
}

// DiffBetween 逐个文件比较 oldRoot 与 newRoot 中 paths 的差异，用于返工审查只送增量。
// 某一侧文件不存在时用 os.DevNull 代替，因此新增、删除文件也能生成 diff。
// 输出中的路径统一改写为仓库相对路径（a/<path>、b/<path>），不会泄露绝对路径。
// 超过 maxBytes 时按整行截断；maxBytes <= 0 表示不限制。
func (r *Runner) DiffBetween(ctx context.Context, oldRoot, newRoot string, paths []string, maxBytes int) (string, error) {
	dir := oldRoot
	if !isExistingDir(dir) {
		dir = newRoot
		if !isExistingDir(dir) {
			return "", fmt.Errorf("diff between: neither root exists: %q, %q", oldRoot, newRoot)
		}
	}

	var out strings.Builder
	for _, p := range paths {
		rel := filepath.ToSlash(filepath.Clean(p))
		if rel == "." || rel == "" {
			continue
		}
		oldAbs := filepath.Join(oldRoot, p)
		newAbs := filepath.Join(newRoot, p)
		oldExists := isExistingFile(oldAbs)
		newExists := isExistingFile(newAbs)
		if !oldExists && !newExists {
			continue
		}
		oldArg, newArg := oldAbs, newAbs
		if !oldExists {
			oldArg = os.DevNull
		}
		if !newExists {
			newArg = os.DevNull
		}

		// outsideRepo 等环境差异不应影响结果；quotePath=false 让非 ASCII 路径保持原样，便于改写。
		diffOut, err := r.runAllowExit1(ctx, dir, nil,
			"-c", "core.quotePath=false",
			"diff", "--no-index", "--no-color", "--no-ext-diff",
			"--", oldArg, newArg,
		)
		if err != nil {
			return "", fmt.Errorf("diff between %s: %w", rel, err)
		}
		out.WriteString(rewriteNoIndexPaths(string(diffOut), oldAbs, newAbs, rel))
	}

	text, _ := truncateDiffLines(out.String(), maxBytes)
	return text, nil
}

// runAllowExit1 与 run 相同地执行 git 命令，但把退出码 1 当作正常结果（git diff --no-index
// 用 1 表示「有差异」），只有退出码大于 1 才返回错误。为避免影响现有调用方，单独实现。
func (r *Runner) runAllowExit1(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	}

	cmd := procattr.HideWindow(exec.CommandContext(ctx, r.Bin, args...))
	cmd.Dir = dir
	cmd.Env = append(loginenv.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	if len(env) > 0 {
		cmd.Env = append(cmd.Env, env...)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return stdout.Bytes(), nil
		}
		errText := strings.TrimSpace(stderr.String())
		if len(errText) > 500 {
			errText = errText[len(errText)-500:]
		}
		cmdName := "git"
		if len(args) > 0 {
			cmdName = "git " + args[0]
		}
		if errText != "" {
			return stdout.Bytes(), fmt.Errorf("%s: %w: %s", cmdName, err, errText)
		}
		return stdout.Bytes(), fmt.Errorf("%s: %w", cmdName, err)
	}

	return stdout.Bytes(), nil
}

// truncateDiffLines 按整行把 s 截断到 maxBytes 以内，返回截断结果与是否发生截断。
// 连一行都放不下时返回空串；maxBytes <= 0 表示不限制。
func truncateDiffLines(s string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s, false
	}
	if cut := strings.LastIndexByte(s[:maxBytes], '\n'); cut >= 0 {
		return s[:cut+1], true
	}
	return "", true
}

// rewriteNoIndexPaths 把 git diff --no-index 输出中的绝对路径改写为仓库相对路径。
// git 会把绝对路径去掉开头的 "/" 后放在 a/、b/ 前缀之后，因此分别替换两侧路径。
func rewriteNoIndexPaths(out, oldAbs, newAbs, rel string) string {
	rel = filepath.ToSlash(rel)
	var pairs []string
	for _, abs := range []string{oldAbs, newAbs} {
		trimmed := strings.TrimPrefix(filepath.ToSlash(abs), "/")
		if trimmed == "" {
			continue
		}
		for _, prefix := range []string{"a/", "b/"} {
			pairs = append(pairs, prefix+trimmed, prefix+rel)
		}
	}
	if len(pairs) == 0 {
		return out
	}
	return strings.NewReplacer(pairs...).Replace(out)
}

func isExistingDir(path string) bool {
	if path == "" {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func isExistingFile(path string) bool {
	if path == "" {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}
