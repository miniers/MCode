package projctx

import "path/filepath"

// MainCheckout 把 path 规整为主仓库根目录：path 是 Git worktree 时返回其主 checkout，
// 其他情况返回绝对化后的 path。Abs 失败时原样返回入参。
func MainCheckout(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return resolveMainCheckout(abs)
}
