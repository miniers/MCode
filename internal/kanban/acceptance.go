package kanban

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
	"mcode/internal/store"
)

// 验收命令执行超时；用变量声明以便测试注入更短的超时。
var (
	// acceptanceCommandTimeout 为单条验收命令的执行超时。
	acceptanceCommandTimeout = 5 * time.Minute
	// acceptanceTotalTimeout 为一次验收中所有命令的合计上限，超出后剩余命令记为超时并不再执行。
	acceptanceTotalTimeout = 15 * time.Minute
)

const (
	// acceptanceOutputLimit 为单条命令保留的输出上限（4KB，只留末尾）。
	acceptanceOutputLimit = 4 * 1024
	// acceptanceTruncatedPrefix 是输出被截断时加在末尾内容前面的提示。
	acceptanceTruncatedPrefix = "…（前文已截断）\n"
	// acceptanceCommandTag 是验收标准里标记「宿主执行命令」的行首标签。
	acceptanceCommandTag = "[命令]"
	// acceptanceUserVerifyTag 是验收标准里标记「需要用户确认」的行首标签。
	acceptanceUserVerifyTag = "[你验]"
)

// AcceptanceCommandResult 描述一条验收命令的执行结果。
// ExitCode 为 -1 表示命令没有产出退出码（启动失败或超时）。
type AcceptanceCommandResult struct {
	Command  string
	ExitCode int
	Output   string
	TimedOut bool
	Duration time.Duration
	Err      string
}

// formatAcceptanceCriteria 把规划给出的验收标准逐条写成一行「- 条目」，写入任务的可读文本字段。
func formatAcceptanceCriteria(criteria []string) string {
	lines := make([]string, 0, len(criteria))
	for _, item := range criteria {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		lines = append(lines, "- "+item)
	}
	return strings.Join(lines, "\n")
}

// ParseAcceptanceCommands 从任务验收标准文本里提取需要宿主执行的命令。
// 只有行内含 [命令] 标签且含一对反引号时，才取第一对反引号之间的文本作为命令；其余行跳过。
func ParseAcceptanceCommands(criteria string) []string {
	commands := make([]string, 0)
	for _, line := range strings.Split(criteria, "\n") {
		if !strings.Contains(line, acceptanceCommandTag) {
			continue
		}
		start := strings.Index(line, "`")
		if start < 0 {
			continue
		}
		end := strings.Index(line[start+1:], "`")
		if end < 0 {
			continue
		}
		command := strings.TrimSpace(line[start+1 : start+1+end])
		if command == "" {
			continue
		}
		commands = append(commands, command)
	}
	return commands
}

// hasGatedAcceptance 判断验收标准里是否有需要宿主执行或需要用户确认的条目（[命令]、[你验]）。
// 这类任务不能自动接受，必须走评审与验收流程。
func hasGatedAcceptance(task store.Task) bool {
	for _, line := range strings.Split(task.AcceptanceCriteria, "\n") {
		if strings.Contains(line, acceptanceCommandTag) || strings.Contains(line, acceptanceUserVerifyTag) {
			return true
		}
	}
	return false
}

// RunAcceptanceCommands 在任务 worktree 里依次执行验收标准里的 [命令] 条目。
// 没有命令时返回空切片；单条命令超时 5 分钟，全部命令合计上限 15 分钟，
// 超出合计上限后剩余命令直接记为超时且不再执行。本方法不改变任务状态，调用方负责接线。
func (s *Service) RunAcceptanceCommands(ctx context.Context, taskID string) ([]AcceptanceCommandResult, error) {
	if s == nil || s.opts.Store == nil {
		return nil, errors.New("kanban: store is nil")
	}
	task, err := s.opts.Store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	commands := ParseAcceptanceCommands(task.AcceptanceCriteria)
	if len(commands) == 0 {
		return []AcceptanceCommandResult{}, nil
	}

	dir := s.TaskWorktreePath(taskID)
	if dir == "" {
		return nil, fmt.Errorf("kanban: worktree dir is not configured for task %s", taskID)
	}
	if fi, statErr := os.Stat(dir); statErr != nil || !fi.IsDir() {
		return nil, fmt.Errorf("kanban: worktree for task %s does not exist: %s", taskID, dir)
	}

	results := make([]AcceptanceCommandResult, 0, len(commands))
	deadline := time.Now().Add(acceptanceTotalTimeout)
	for i, command := range commands {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			// 合计超时：剩余命令不再执行，统一记为超时。
			for _, rest := range commands[i:] {
				results = append(results, AcceptanceCommandResult{
					Command:  rest,
					ExitCode: -1,
					TimedOut: true,
					Err:      "acceptance command skipped: total time limit exceeded",
				})
			}
			break
		}

		timeout := acceptanceCommandTimeout
		if timeout <= 0 || timeout > remaining {
			timeout = remaining
		}

		started := time.Now()
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		cmd := procattr.HideWindow(exec.CommandContext(runCtx, "/bin/sh", "-c", command))
		cmd.Dir = dir
		// 与 gitCmd 一致继承登录 Shell 环境变量，并告知命令自己运行在任务 worktree 里。
		cmd.Env = append(loginenv.Environ(), "MCODE_WORKTREE_PATH="+dir)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		runErr := cmd.Run()
		timedOut := runCtx.Err() == context.DeadlineExceeded
		cancel()

		if ctxErr := ctx.Err(); ctxErr != nil {
			// 调用方取消了验收：不把取消当成命令失败返回结果。
			return nil, ctxErr
		}

		res := AcceptanceCommandResult{
			Command:  command,
			Output:   truncateAcceptanceOutput(out.String()),
			Duration: time.Since(started),
		}
		var exitErr *exec.ExitError
		switch {
		case runErr == nil:
			res.ExitCode = 0
		case timedOut:
			res.ExitCode = -1
			res.TimedOut = true
			res.Err = "acceptance command timed out"
		case errors.As(runErr, &exitErr):
			res.ExitCode = exitErr.ExitCode()
			res.Err = runErr.Error()
		default:
			res.ExitCode = -1
			res.Err = runErr.Error()
		}
		results = append(results, res)
	}
	return results, nil
}

// truncateAcceptanceOutput 只保留输出末尾 4KB，并按 UTF-8 rune 边界裁剪，截断时在开头加提示。
func truncateAcceptanceOutput(out string) string {
	if len(out) <= acceptanceOutputLimit {
		return out
	}
	cut := out[len(out)-acceptanceOutputLimit:]
	// 丢掉落在 rune 中间的起始字节，再削掉尾部不完整 rune，保证结果仍是合法 UTF-8。
	for len(cut) > 0 && !utf8.RuneStart(cut[0]) {
		cut = cut[1:]
	}
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return acceptanceTruncatedPrefix + cut
}
