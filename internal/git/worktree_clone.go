package git

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	cloneIgnoredMu      sync.RWMutex
	globalCloneIgnored  bool
	globalClonePatterns []string
)

// SetCloneIgnored 设置包级默认：创建 worktree 后是否把匹配模式的被忽略条目克隆进新 worktree。
// patterns 为 filepath.Match 风格的模式，匹配被忽略条目的基名。
func SetCloneIgnored(enabled bool, patterns []string) {
	cloneIgnoredMu.Lock()
	defer cloneIgnoredMu.Unlock()
	globalCloneIgnored = enabled
	globalClonePatterns = append([]string(nil), patterns...)
}

// GetCloneIgnored 获取包级默认：是否克隆忽略目录，以及匹配模式副本。
func GetCloneIgnored() (bool, []string) {
	cloneIgnoredMu.RLock()
	defer cloneIgnoredMu.RUnlock()
	return globalCloneIgnored, append([]string(nil), globalClonePatterns...)
}

// cloneIgnoredInto 尽力而为地把源 Checkout 中被忽略、且基名匹配配置模式的条目
// 以写时复制方式带入新 worktree。任何失败只记录日志，不影响调用方。
func (r *Runner) cloneIgnoredInto(ctx context.Context, root, wtPath string) {
	enabled, patterns := GetCloneIgnored()
	if !enabled || len(patterns) == 0 {
		return
	}

	out, err := r.run(ctx, root, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		log.Printf("git: 跳过克隆忽略目录：无法解析源仓库顶层：%v", err)
		return
	}
	top := strings.TrimSpace(string(out))
	if top == "" {
		return
	}

	if ok, reason := cloneVolumeOK(top, wtPath); !ok {
		log.Printf("git: 跳过克隆忽略目录：%s", reason)
		return
	}

	out, err = r.run(ctx, top, nil, "ls-files", "-z", "-o", "-i", "--exclude-standard", "--directory")
	if err != nil {
		log.Printf("git: 跳过克隆忽略目录：列举被忽略条目失败：%v", err)
		return
	}

	start := time.Now()
	cloned := 0
	for _, item := range strings.Split(string(out), "\x00") {
		rel := strings.TrimSuffix(strings.TrimSpace(item), "/")
		if rel == "" || filepath.IsAbs(rel) || hasDotDotSegment(rel) {
			continue
		}
		if !matchAnyPattern(filepath.Base(rel), patterns) {
			continue
		}

		src := filepath.Join(top, rel)
		dst := filepath.Join(wtPath, rel)
		// 防止把 worktree 自身克隆进自己。
		if src == wtPath || isAncestor(src, wtPath) || isAncestor(wtPath, src) {
			continue
		}
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			log.Printf("git: 克隆忽略条目 %s 失败：%v", rel, err)
			continue
		}
		if err := cloneTree(src, dst); err != nil {
			log.Printf("git: 克隆忽略条目 %s 失败：%v", rel, err)
			continue
		}
		cloned++
	}

	if cloned > 0 {
		log.Printf("git: 已克隆 %d 个忽略条目到 %s，用时 %s", cloned, wtPath, time.Since(start).Round(time.Millisecond))
	}
}

// hasDotDotSegment 判断相对路径中是否含有 ".." 段。
func hasDotDotSegment(rel string) bool {
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		if seg == ".." {
			return true
		}
	}
	return false
}

// matchAnyPattern 判断名字是否匹配任一模式。
func matchAnyPattern(name string, patterns []string) bool {
	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}
		if ok, err := filepath.Match(pattern, name); err == nil && ok {
			return true
		}
	}
	return false
}

// isAncestor 判断 parent 是否为 child 的祖先目录。
func isAncestor(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil || rel == "." || rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
