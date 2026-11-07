package remote

import (
	"context"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"mcode/internal/procattr"
)

const (
	// worktreeSetupTimeout 为单个「新建工作树时运行」脚本的执行超时。
	worktreeSetupTimeout = 10 * time.Minute
	// worktreeSetupOutputLimit 为结果里保留的脚本输出上限（2KB）。
	worktreeSetupOutputLimit = 2 * 1024
)

// WorktreeScriptResult 描述一个「新建工作树时运行」脚本的执行结果。
type WorktreeScriptResult struct {
	Name    string
	Success bool
	Output  string
}

// RunWorktreeSetupScripts 读取项目脚本并同步执行其中勾选了「新建工作树时运行」的脚本。
// 任何脚本失败或超时都只记日志并继续执行后续脚本，绝不影响 worktree 创建；
// 调用方应自行放进 goroutine 里异步执行。
func (b *MemoryBackend) RunWorktreeSetupScripts(ctx context.Context, projectID, projectRoot, worktreePath string) {
	results := b.runWorktreeSetupScriptsForProject(ctx, projectID, projectRoot, worktreePath)
	for _, res := range results {
		status := "成功"
		if !res.Success {
			status = "失败"
		}
		log.Printf("worktree 初始化脚本「%s」%s（%s）", res.Name, status, worktreePath)
	}
}

// runWorktreeSetupScriptsForProject 读取项目脚本、筛选出需要在新 worktree 里运行的脚本并依次执行。
func (b *MemoryBackend) runWorktreeSetupScriptsForProject(ctx context.Context, projectID, projectRoot, worktreePath string) []WorktreeScriptResult {
	if strings.TrimSpace(worktreePath) == "" {
		return nil
	}
	scripts, err := b.projectScriptsByProjectID(projectID)
	if err != nil {
		log.Printf("读取项目脚本失败，跳过 worktree 初始化：%v", err)
		return nil
	}
	return runWorktreeSetupScripts(ctx, projectRoot, worktreePath, scripts)
}

// runWorktreeSetupScripts 按顺序在新 worktree 里执行勾选了「新建工作树时运行」的脚本。
// 每个脚本独立超时，失败只记录日志并继续下一个；返回每个脚本的名称、成功与否和截断后的输出。
func runWorktreeSetupScripts(ctx context.Context, projectRoot, worktreePath string, scripts []ProjectScript) []WorktreeScriptResult {
	if ctx == nil {
		ctx = context.Background()
	}
	env := append(os.Environ(),
		"MCODE_WORKTREE_PATH="+worktreePath,
		"MCODE_PROJECT_ROOT="+projectRoot,
	)

	var results []WorktreeScriptResult
	for _, sc := range scripts {
		if !sc.RunOnWorktreeCreate {
			continue
		}
		res := WorktreeScriptResult{Name: sc.Name}

		runCtx, cancel := context.WithTimeout(ctx, worktreeSetupTimeout)
		// Windows 没有 /bin/sh，改用 PowerShell；其余平台保持 sh -c。
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = procattr.HideWindow(exec.CommandContext(runCtx, "powershell.exe", "-NoProfile", "-Command", sc.Command))
		} else {
			cmd = procattr.HideWindow(exec.CommandContext(runCtx, "/bin/sh", "-c", sc.Command))
		}
		cmd.Dir = worktreePath
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		timedOut := runCtx.Err() == context.DeadlineExceeded
		cancel()

		res.Output = truncateWorktreeSetupOutput(string(out))
		res.Success = err == nil
		switch {
		case err == nil:
			// 成功执行，无需额外提示。
		case timedOut:
			log.Printf("worktree 初始化脚本「%s」超时（%s），继续执行后续脚本", sc.Name, worktreeSetupTimeout)
		default:
			log.Printf("worktree 初始化脚本「%s」执行失败：%v", sc.Name, err)
		}
		results = append(results, res)
	}
	return results
}

// truncateWorktreeSetupOutput 把脚本输出裁剪到 2KB 以内，避免占用过多内存且保证是合法 UTF-8。
func truncateWorktreeSetupOutput(out string) string {
	if len(out) <= worktreeSetupOutputLimit {
		return out
	}
	cut := out[:worktreeSetupOutputLimit]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "\n…（输出已截断）"
}
