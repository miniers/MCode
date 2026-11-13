//go:build windows

package loginenv

import (
	"mcode/internal/procattr"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// GitBash 返回缓存的 Git Bash 路径。
var GitBash = sync.OnceValue(findBash)

func init() { procattr.SetWindowsBashResolver(GitBash) }

// findBash 返回 bash 工具使用的 bash.exe，找不到时返回空串（回退 PowerShell）。
// 优先用 Git for Windows 自带的 bash：GUI 进程的 PATH 通常只有 <Git>\cmd，没有 bash。
// 不能直接取 PATH 里的 bash.exe：WindowsApps 与 System32 下的 bash.exe 是 WSL 启动器，
// 命令会跑进 Linux 发行版，工作目录与 Windows 上的工具链都对不上。
func findBash() string {
	if git, err := LookPath("git"); err == nil {
		if bash := gitBashFor(git, fileExists); bash != "" {
			return bash
		}
	}
	if path, err := exec.LookPath("bash.exe"); err == nil && !isWSLLauncher(path) {
		return path
	}
	return ""
}

// gitBashFor 由 git.exe 的位置推出同一安装里的 bash.exe。
// git.exe 可能在 <Git>\cmd、<Git>\bin 或 <Git>\mingw64\bin；<Git>\bin\bash.exe 会先补好
// usr\bin 等 PATH 再启动真正的 bash，所以排在 usr\bin\bash.exe 之前。
func gitBashFor(git string, exists func(string) bool) string {
	dir := filepath.Dir(git)
	roots := []string{filepath.Dir(dir)}
	if strings.EqualFold(filepath.Base(dir), "bin") && strings.EqualFold(filepath.Base(filepath.Dir(dir)), "mingw64") {
		roots = append(roots, filepath.Dir(filepath.Dir(dir)))
	}
	for _, root := range roots {
		for _, candidate := range []string{
			filepath.Join(root, "bin", "bash.exe"),
			filepath.Join(root, "usr", "bin", "bash.exe"),
		} {
			if exists(candidate) {
				return candidate
			}
		}
	}
	return ""
}

// isWSLLauncher 判断 bash.exe 是否是 WSL 的启动器（System32 或 WindowsApps 下）。
func isWSLLauncher(path string) bool {
	dir := strings.ToLower(filepath.Dir(path))
	if strings.HasSuffix(dir, `\windowsapps`) {
		return true
	}
	if root := os.Getenv("SystemRoot"); root != "" && strings.EqualFold(dir, filepath.Join(root, "System32")) {
		return true
	}
	return false
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
