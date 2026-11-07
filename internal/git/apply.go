package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ApplyResult 描述应用补丁的执行结果。
type ApplyResult struct {
	Conflicted bool
	Output     string
}

// AddAll 执行 git add -A，将指定仓库工作区的全部变动暂存。
func (r *Runner) AddAll(ctx context.Context, root string) error {
	_, err := r.run(ctx, root, nil, "add", "-A")
	return err
}

// DiffCachedBinary 执行 git diff --cached --binary <base>，生成暂存区与 base 之间的二进制差异补丁。
func (r *Runner) DiffCachedBinary(ctx context.Context, root, base string) ([]byte, error) {
	args := []string{"diff", "--cached", "--binary"}
	if base != "" {
		args = append(args, base)
	}
	return r.run(ctx, root, nil, args...)
}

// DiffCachedNames 执行 git diff --cached --name-only -z <base>，获取暂存区与 base 相比变更的文件相对路径列表。
// 使用 -z 输出原始字节路径，避免 core.quotePath 对非 ASCII 文件名加引号转义后无法直接用于文件读取。
func (r *Runner) DiffCachedNames(ctx context.Context, root, base string) ([]string, error) {
	args := []string{"diff", "--cached", "--name-only", "-z"}
	if base != "" {
		args = append(args, base)
	}
	out, err := r.run(ctx, root, nil, args...)
	if err != nil {
		return nil, err
	}
	paths := []string{}
	for _, seg := range strings.Split(string(out), "\x00") {
		if seg != "" {
			paths = append(paths, seg)
		}
	}
	return paths, nil
}

// IndexPath 返回仓库索引文件（GIT_INDEX_FILE）的绝对路径。
func (r *Runner) IndexPath(ctx context.Context, root string) (string, error) {
	out, err := r.run(ctx, root, nil, "rev-parse", "--git-path", "index")
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return "", fmt.Errorf("empty git index path")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	return path, nil
}

// ApplyPatch3Way 将 patch 写入临时文件，再执行 git apply --3way --binary --whitespace=nowarn <tmpfile>。
// 三方合并走独立的临时索引（GIT_INDEX_FILE）：先把临时索引同步为当前工作区的状态
// （真实索引存在则复制，否则在已有提交时 read-tree HEAD），因此合并结果只写入工作区，
// 不会在主仓库索引里留下暂存或未合并条目，后续补丁也能继续落到同一个文件上。
// 当 git 退出码非零但输出包含冲突标记（如 "with conflicts" 或 "U "）时，返回 Conflicted=true 且 err=nil；
// 否则返回非冲突错误。
func (r *Runner) ApplyPatch3Way(ctx context.Context, root string, patch []byte) (ApplyResult, error) {
	tmpFile, err := os.CreateTemp("", "mcode-patch-*")
	if err != nil {
		return ApplyResult{}, fmt.Errorf("create temp patch file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(patch); err != nil {
		_ = tmpFile.Close()
		return ApplyResult{}, fmt.Errorf("write temp patch file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return ApplyResult{}, fmt.Errorf("close temp patch file: %w", err)
	}

	tmpIndexDir, err := os.MkdirTemp("", "mcode-apply-index-*")
	if err != nil {
		return ApplyResult{}, fmt.Errorf("create temp index dir: %w", err)
	}
	defer os.RemoveAll(tmpIndexDir)

	tmpIndexPath := filepath.Join(tmpIndexDir, "index")
	env := []string{"GIT_INDEX_FILE=" + tmpIndexPath}

	if err := r.syncTempIndex(ctx, root, tmpIndexPath, env); err != nil {
		return ApplyResult{}, err
	}

	out, runErr := r.run(ctx, root, env, "apply", "--3way", "--binary", "--whitespace=nowarn", tmpFile.Name())
	if runErr == nil {
		return ApplyResult{Conflicted: false, Output: string(out)}, nil
	}

	outStr := strings.TrimSpace(string(out))
	combined := outStr
	if combined == "" {
		combined = runErr.Error()
	} else {
		combined = combined + "\n" + runErr.Error()
	}

	if strings.Contains(combined, "with conflicts") || strings.Contains(combined, "U ") {
		return ApplyResult{Conflicted: true, Output: combined}, nil
	}
	return ApplyResult{Conflicted: false, Output: combined}, runErr
}

// syncTempIndex 让临时索引匹配当前工作区：复制真实索引（若存在），
// 否则在仓库已有提交时用 read-tree HEAD 初始化，最后 add -A 吸收工作区改动。
func (r *Runner) syncTempIndex(ctx context.Context, root, tmpIndexPath string, env []string) error {
	realIndexPath, err := r.IndexPath(ctx, root)
	if err != nil {
		return fmt.Errorf("locate git index: %w", err)
	}

	data, readErr := os.ReadFile(realIndexPath)
	switch {
	case readErr == nil:
		if err := os.WriteFile(tmpIndexPath, data, 0600); err != nil {
			return fmt.Errorf("copy git index: %w", err)
		}
	case os.IsNotExist(readErr):
		if _, err := r.run(ctx, root, env, "rev-parse", "--verify", "--quiet", "HEAD"); err == nil {
			if _, err := r.run(ctx, root, env, "read-tree", "HEAD"); err != nil {
				return fmt.Errorf("populate temp index: %w", err)
			}
		}
	default:
		return fmt.Errorf("read git index: %w", readErr)
	}

	if _, err := r.run(ctx, root, env, "add", "-A", "--", "."); err != nil {
		return fmt.Errorf("sync temp index with work tree: %w", err)
	}
	return nil
}

// ScanConflictMarkers 纯文件扫描，不调用 git。对每个相对路径读取文件，
// 同时出现以 "<<<<<<<" 开头的行、恰好等于 "=======" 的行、以 ">>>>>>>" 开头的行，才算冲突文件。
// 文件不存在就跳过。返回排好序的相对路径列表。
func ScanConflictMarkers(root string, paths []string) ([]string, error) {
	var conflicted []string
	seen := make(map[string]struct{})

	for _, p := range paths {
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}

		fullPath := filepath.Join(root, filepath.FromSlash(p))
		data, err := os.ReadFile(fullPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}

		if hasConflictMarkers(data) {
			conflicted = append(conflicted, p)
		}
	}

	sort.Strings(conflicted)
	if conflicted == nil {
		conflicted = []string{}
	}
	return conflicted, nil
}

func hasConflictMarkers(data []byte) bool {
	lines := strings.Split(string(data), "\n")
	var hasStart, hasMid, hasEnd bool
	for _, rawLine := range lines {
		line := strings.TrimSuffix(rawLine, "\r")
		if strings.HasPrefix(line, "<<<<<<<") {
			hasStart = true
		} else if line == "=======" {
			hasMid = true
		} else if strings.HasPrefix(line, ">>>>>>>") {
			hasEnd = true
		}
		if hasStart && hasMid && hasEnd {
			return true
		}
	}
	return false
}
