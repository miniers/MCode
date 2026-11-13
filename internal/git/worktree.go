package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

var (
	submodulesMu         sync.RWMutex
	globalInitSubmodules bool
)

// SetInitSubmodules 设置包级默认：在创建 worktree 后是否自动初始化子模块。
func SetInitSubmodules(enabled bool) {
	submodulesMu.Lock()
	defer submodulesMu.Unlock()
	globalInitSubmodules = enabled
}

// GetInitSubmodules 获取包级默认：在创建 worktree 后是否自动初始化子模块。
func GetInitSubmodules() bool {
	submodulesMu.RLock()
	defer submodulesMu.RUnlock()
	return globalInitSubmodules
}

// WorktreeAddOption 为 WorktreeAdd 的可选配置函数。
type WorktreeAddOption func(*worktreeAddOptions)

type worktreeAddOptions struct {
	initSubmodules *bool
}

// WithInitSubmodules 显式指定是否初始化子模块。
func WithInitSubmodules(enabled bool) WorktreeAddOption {
	return func(o *worktreeAddOptions) {
		o.initSubmodules = &enabled
	}
}

// HasSubmodules 检查指定根目录或工作树路径下是否存在 .gitmodules 文件且非目录。
func HasSubmodules(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".gitmodules"))
	return err == nil && !info.IsDir()
}

// Worktree 表示单个 Git worktree 的信息。
type Worktree struct {
	Path     string
	Head     string
	Branch   string
	Bare     bool
	Detached bool
	Locked   bool
	Prunable bool
}

// WorktreeAdd 在指定根目录下添加一个新的 worktree。
// branch 非空时执行 git worktree add -b <branch> <path> <base>（base 为空则省略，从 HEAD 创建）；
// branch 为空时执行 git worktree add --detach <path> [base]。
// path 必须为绝对路径，否则返回错误。
// 创建完成后，若仓库包含 .gitmodules 且设置了初始化子模块，则自动执行 git submodule update --init --recursive；
// 若开启了克隆忽略目录，还会在 macOS APFS 上把匹配的被忽略条目以写时复制带入新 worktree，失败只记日志。
func (r *Runner) WorktreeAdd(ctx context.Context, root, path, branch, base string, opts ...WorktreeAddOption) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("worktree path must be absolute: %s", path)
	}

	if pathUnderBase(path, filepath.Join(root, ".mcode", "worktrees")) {
		EnsureGitExclude(root, "/.mcode/worktrees/")
	}

	var args []string
	if runtime.GOOS == "windows" {
		args = append(args, "-c", "core.longpaths=true")
	}
	args = append(args, "worktree", "add")
	if branch != "" {
		args = append(args, "-b", branch, "--", path)
	} else {
		args = append(args, "--detach", "--", path)
	}
	if base != "" {
		args = append(args, base)
	}

	if _, err := r.run(ctx, root, nil, args...); err != nil {
		return fmt.Errorf("git worktree add failed: %w", err)
	}

	var cfg worktreeAddOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	shouldInit := GetInitSubmodules()
	if cfg.initSubmodules != nil {
		shouldInit = *cfg.initSubmodules
	}

	if shouldInit && HasSubmodules(path) {
		if _, err := r.run(ctx, path, nil, "submodule", "update", "--init", "--recursive"); err != nil {
			return fmt.Errorf("git submodule update --init --recursive failed: %w", err)
		}
	}

	r.cloneIgnoredInto(ctx, root, path)

	return nil
}

// WorktreeRemove 仅移除指定的 worktree；force 允许丢弃其中未提交的文件。
func (r *Runner) WorktreeRemove(ctx context.Context, root, path string, force bool) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("worktree path must be absolute: %s", path)
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, "--", path)

	if _, err := r.run(ctx, root, nil, args...); err != nil {
		return fmt.Errorf("git worktree remove failed: %w", err)
	}

	return nil
}

// WorktreeList 获取指定仓库根目录下所有的 worktree 列表。
func (r *Runner) WorktreeList(ctx context.Context, root string) ([]Worktree, error) {
	out, err := r.run(ctx, root, nil, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, fmt.Errorf("git worktree list failed: %w", err)
	}
	return ParseWorktreeList(out), nil
}

// ParseWorktreeList 支持普通和 -z porcelain 输出，保留特殊字符路径。
func ParseWorktreeList(data []byte) []Worktree {
	var worktrees []Worktree
	var current Worktree
	inEntry := false

	flush := func() {
		if inEntry && current.Path != "" {
			worktrees = append(worktrees, current)
		}
		current = Worktree{}
		inEntry = false
	}

	nulSeparated := strings.ContainsRune(string(data), '\x00')
	separator := "\n"
	if nulSeparated {
		separator = "\x00"
	}
	lines := strings.Split(string(data), separator)
	for _, rawLine := range lines {
		line := rawLine
		if !nulSeparated {
			line = strings.TrimSuffix(line, "\r")
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			flush()
			continue
		}

		switch {
		case strings.HasPrefix(line, "worktree "):
			if inEntry && current.Path != "" {
				flush()
			}
			current.Path = strings.TrimPrefix(line, "worktree ")
			if !nulSeparated && strings.HasPrefix(current.Path, `"`) {
				if path, err := strconv.Unquote(current.Path); err == nil {
					current.Path = path
				}
			}
			inEntry = true
		case strings.HasPrefix(line, "HEAD "):
			current.Head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			rawBranch := strings.TrimPrefix(line, "branch ")
			current.Branch = strings.TrimPrefix(rawBranch, "refs/heads/")
		case trimmed == "bare" || strings.HasPrefix(trimmed, "bare "):
			current.Bare = true
		case trimmed == "detached" || strings.HasPrefix(trimmed, "detached "):
			current.Detached = true
		case trimmed == "locked" || strings.HasPrefix(trimmed, "locked "):
			current.Locked = true
		case trimmed == "prunable" || strings.HasPrefix(trimmed, "prunable "):
			current.Prunable = true
		}
	}
	flush()

	if worktrees == nil {
		return []Worktree{}
	}
	return worktrees
}
