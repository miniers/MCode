package git

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
)

// Runner 是基于 git 命令行工具的执行器，内部通过信号量限制命令并发执行数。
type Runner struct {
	Bin        string
	ShadowRoot string // 非空时，对不在任何真实 git 仓库内、但已有影子仓库的目录，自动注入 GIT_DIR 和 GIT_WORK_TREE。
	sem        chan struct{}
}

// NewRunner 创建一个 Git CLI 执行器。
// bin 为空时默认使用 "git"；maxConcurrent 小于等于 0 时默认并发限制为 4。
func NewRunner(bin string, maxConcurrent int) *Runner {
	if bin == "" {
		bin = "git"
	}
	if maxConcurrent <= 0 {
		maxConcurrent = 4
	}
	return &Runner{
		Bin: bin,
		sem: make(chan struct{}, maxConcurrent),
	}
}

// repoWriteSubcommands 列出会修改用户索引、工作区或引用的子命令。
// 这些命令需要按仓库串行，读类命令（status/diff/log/rev-parse 等）不在其中。
var repoWriteSubcommands = map[string]bool{
	"add":          true,
	"am":           true,
	"apply":        true,
	"branch":       true,
	"checkout":     true,
	"cherry-pick":  true,
	"clean":        true,
	"commit":       true,
	"merge":        true,
	"mv":           true,
	"pull":         true,
	"rebase":       true,
	"reset":        true,
	"restore":      true,
	"revert":       true,
	"rm":           true,
	"stash":        true,
	"switch":       true,
	"tag":          true,
	"update-index": true,
	"worktree":     true,
}

// isRepoWriteCall 判断本次调用是否为会改动用户仓库的写操作。
// 先跳过开头的 -c <key=value>（含 -c<key=value> 粘连写法）取第一个子命令；
// 若 env 指定了 GIT_INDEX_FILE，说明命令使用临时 index、不触碰用户索引与引用，无需加锁。
func isRepoWriteCall(env []string, args []string) bool {
	for _, item := range env {
		if strings.HasPrefix(item, "GIT_INDEX_FILE=") {
			return false
		}
	}
	i := 0
	for i < len(args) {
		if len(args[i]) > 2 && strings.HasPrefix(args[i], "-c") {
			i++
			continue
		}
		if args[i] != "-c" || i+1 >= len(args) {
			break
		}
		i += 2
	}
	if i >= len(args) {
		return false
	}
	return repoWriteSubcommands[args[i]]
}

// run 在指定目录下执行 git 命令。
// 内部使用信号量限制并发，统一注入 GIT_TERMINAL_PROMPT=0 与 LC_ALL=C，
// 当命令返回非零退出码时，将 stderr 的末尾错误摘要包装至 error 中返回。
func (r *Runner) run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	// 写操作先按仓库目录取互斥锁，再排队等信号量，保证同一仓库的提交/暂存/引用更新
	// 不会同时进行。run 是叶子函数（不会重入自身），持锁期间只等待信号量，不会死锁。
	if isRepoWriteCall(env, args) {
		defer lockRepo(dir)()
	}

	fn, _ := ctx.Value(progressKey{}).(func(Progress))
	originalArgCount := len(args)
	args = progressArgs(args, fn)
	// 每次尝试独立获取并释放并发信号量，清理残留锁时可以安全调用 r.IndexPath。
	runOnce := func() ([]byte, string, error) {
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case r.sem <- struct{}{}:
			defer func() { <-r.sem }()
		}

		cmd := procattr.TerminateOnCancel(procattr.HideWindow(exec.CommandContext(ctx, r.Bin, args...)))
		cmd.Dir = dir
		// 组合环境变量：继承登录 Shell 环境变量（保证 git hooks 能用到 node 等用户 PATH 下的工具），
		// 注入 Git 非交互与固定语言环境，若调用方未指定 GIT_DIR 则按需注入影子仓库环境，最后追加自定义变量
		mergedEnv := append(loginenv.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
		hasGitDir := false
		for _, item := range env {
			if strings.HasPrefix(item, "GIT_DIR=") {
				hasGitDir = true
				break
			}
		}
		if !hasGitDir {
			if sEnv := r.shadowEnv(dir); len(sEnv) > 0 {
				mergedEnv = append(mergedEnv, sEnv...)
			}
		}
		if len(env) > 0 {
			mergedEnv = append(mergedEnv, env...)
		}
		cmd.Env = mergedEnv

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		var progress *progressWriter
		if len(args) != originalArgCount {
			progress = &progressWriter{stderr: &stderr, fn: fn}
			cmd.Stderr = progress
		}
		err := cmd.Run()
		if progress != nil {
			progress.flush()
		}
		return stdout.Bytes(), stderr.String(), err
	}

	out, stderrText, err := runOnce()
	if err != nil && isRepoWriteCall(env, args) && strings.Contains(stderrText, "index.lock") {
		if cleared, clearErr := r.ClearStaleIndexLock(ctx, dir); clearErr == nil && cleared {
			out, stderrText, err = runOnce()
		}
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if err != nil {
		errText := strings.TrimSpace(stderrText)
		// 截取 stderr 末尾最多 500 个字符，防止错误信息过长冲刷日志
		if len(errText) > 500 {
			errText = errText[len(errText)-500:]
		}
		cmdName := "git"
		if len(args) > 0 {
			cmdName = "git " + args[0]
		}
		if errText != "" {
			return out, fmt.Errorf("%s: %w: %s", cmdName, err, errText)
		}
		return out, fmt.Errorf("%s: %w", cmdName, err)
	}

	return out, nil
}
