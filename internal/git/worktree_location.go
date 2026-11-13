package git

import (
	"path"
	"path/filepath"
	"strings"

	"mcode/internal/config"
)

// WorktreeBase 返回新 worktree 的存储根目录。
func WorktreeBase(dataDir, repoRoot, location string) string {
	if location == config.WorktreeLocationProject {
		return filepath.Join(repoRoot, ".mcode", "worktrees")
	}
	return filepath.Join(dataDir, "worktrees")
}

// IsProjectWorktreePath 按路径段识别项目内 worktree 的子路径，兼容 Windows 分隔符。
func IsProjectWorktreePath(value string) bool {
	parts := strings.Split(path.Clean(strings.ReplaceAll(strings.TrimSpace(value), `\`, "/")), "/")
	for i := 1; i+2 < len(parts); i++ {
		if parts[i] == ".mcode" && parts[i+1] == "worktrees" {
			return true
		}
	}
	return false
}
