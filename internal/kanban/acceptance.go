package kanban

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"mcode/internal/procattr"
	"mcode/internal/procutil"
	"mcode/internal/providers/loginenv"
	"mcode/internal/store"
)

// 验收命令执行超时默认值。
const (
	// acceptanceCommandTimeout 为单条验收命令的执行超时。
	acceptanceCommandTimeout = 5 * time.Minute
	// acceptanceTotalTimeout 为一次验收中所有命令的合计上限，超出后剩余命令记为超时并不再执行。
	acceptanceTotalTimeout = 15 * time.Minute
)

const (
	// acceptanceOutputLimit 为单条命令保留的输出上限（4KB）。
	acceptanceOutputLimit = 4 * 1024
	// acceptanceCaptureLimit 为单条命令运行期间捕获的输出上限（256KB）。
	// shortcut: 只捕获最后 256KB，更早的报错会丢；遇到真实漏报再改为流式提取报错行
	acceptanceCaptureLimit = 256 * 1024
	// acceptanceTruncatedPrefix 是输出被截断时加在内容前面的提示。
	acceptanceTruncatedPrefix = "…（前文已截断）\n"
	// acceptanceEllipsisLine 是报错区块之间、区块与末尾之间存在省略时插入的分隔行。
	acceptanceEllipsisLine = "…"
	// acceptanceCommandTag 是验收标准里标记「宿主执行命令」的行首标签。
	acceptanceCommandTag = "[命令]"
	// acceptanceUserVerifyTag 是验收标准里标记「需要用户确认」的行首标签。
	acceptanceUserVerifyTag = "[你验]"
	// acceptanceReviewTag 是需要人阅读改动后判断的行首标签。
	acceptanceReviewTag   = "[审阅]"
	acceptanceRunningText = "正在跑验收命令…"
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

// hasUserVerify 判断任务是否需要用户确认或人工审阅，不能自动接受。
func hasUserVerify(task store.Task) bool {
	return strings.Contains(task.AcceptanceCriteria, acceptanceUserVerifyTag) || strings.Contains(task.AcceptanceCriteria, acceptanceReviewTag)
}

// startAcceptanceCommands 只在完成落库后调用，命令执行不阻塞调度循环。
func (s *Service) startAcceptanceCommands(task store.Task, autoAccept bool) {
	s.mu.Lock()
	s.acceptanceMu.Lock()
	if s.ctx.Err() != nil || s.acceptanceRunning[task.ID] {
		s.acceptanceMu.Unlock()
		s.mu.Unlock()
		return
	}
	latest, err := s.opts.Store.GetTask(s.ctx, task.ID)
	if err == nil && latest.Status == string(TaskStatusReview) && latest.Revision == task.Revision {
		text := acceptanceRunningText
		latest.LastActivityText = &text
		_, err = s.opts.Store.UpdateTask(s.ctx, latest, latest.Revision)
		if err == nil {
			s.revision.Add(1)
		}
	} else if err == nil {
		err = store.ErrRevisionConflict
	}
	if err != nil {
		s.acceptanceMu.Unlock()
		s.mu.Unlock()
		log.Printf("kanban: 启动任务 %s 验收失败: %v", task.ID, err)
		return
	}
	if s.acceptanceRunning == nil {
		s.acceptanceRunning = make(map[string]bool)
	}
	s.acceptanceRunning[task.ID] = true
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.acceptanceMu.Lock()
			delete(s.acceptanceRunning, task.ID)
			s.acceptanceMu.Unlock()
			s.notifyChange()
		}()
		s.finishAcceptanceCommands(task, autoAccept)
	}()
	s.acceptanceMu.Unlock()
	s.mu.Unlock()
	s.notifyChange()
}

func (s *Service) isAcceptanceRunning(id string) bool {
	s.acceptanceMu.Lock()
	defer s.acceptanceMu.Unlock()
	return s.acceptanceRunning[id]
}

func acceptanceFailureFeedback(commands []string, results []AcceptanceCommandResult, err error) string {
	var failures []string
	if err != nil {
		failures = append(failures, fmt.Sprintf("验收命令执行失败（%s）：%v", strings.Join(commands, "；"), err))
	}
	for _, result := range results {
		if result.ExitCode == 0 && !result.TimedOut && result.Err == "" {
			continue
		}
		status := fmt.Sprintf("退出码 %d", result.ExitCode)
		if result.TimedOut {
			status = "超时"
		}
		failures = append(failures, fmt.Sprintf("命令 `%s` 失败（%s）：%s\n%s", result.Command, status, result.Err, result.Output))
	}
	return strings.Join(failures, "\n\n")
}

func (s *Service) finishAcceptanceCommands(original store.Task, autoAccept bool) {
	results, runErr := s.RunAcceptanceCommands(s.ctx, original.ID)
	// 服务退出不算验收失败，保留运行中标记供重启后续跑。
	if s.ctx.Err() != nil {
		return
	}
	feedback := acceptanceFailureFeedback(ParseAcceptanceCommands(original.AcceptanceCriteria), results, runErr)
	defer s.notifyChange()
	s.mu.Lock()
	task, err := s.opts.Store.GetTask(s.ctx, original.ID)
	if err != nil {
		s.mu.Unlock()
		return
	}
	if task.Status != string(TaskStatusReview) {
		if task.LastActivityText != nil && *task.LastActivityText == acceptanceRunningText {
			task.LastActivityText = nil
			if _, err = s.opts.Store.UpdateTask(s.ctx, task, task.Revision); err == nil {
				s.revision.Add(1)
			} else {
				log.Printf("kanban: 清理任务 %s 验收状态失败: %v", task.ID, err)
			}
		}
		s.mu.Unlock()
		return
	}
	var decision ReviewDecision
	if feedback != "" && task.RejectCount == 0 {
		decision = ReviewReject
	} else if feedback == "" && autoAccept && !hasUserVerify(task) {
		decision = ReviewAccept
	}
	text := feedback
	if text == "" {
		text = "验收命令全部通过"
		if original.LastActivityText != nil && *original.LastActivityText != "" && *original.LastActivityText != acceptanceRunningText {
			text += "\n" + *original.LastActivityText
		}
	}
	task.LastActivityText = &text
	task, err = s.opts.Store.UpdateTask(s.ctx, task, task.Revision)
	if err == nil {
		s.revision.Add(1)
	}
	s.mu.Unlock()
	if err == nil && decision != "" {
		_, err = s.ReviewTask(s.ctx, task.ID, decision, feedback, task.Revision)
	}
	if err != nil {
		log.Printf("kanban: 更新任务 %s 验收结果失败: %v", task.ID, err)
	}
}

func (s *Service) acceptanceTimeouts() (time.Duration, time.Duration) {
	cmdTimeout := acceptanceCommandTimeout
	if s != nil && s.acceptanceCommandTimeout > 0 {
		cmdTimeout = s.acceptanceCommandTimeout
	}
	totalTimeout := acceptanceTotalTimeout
	if s != nil && s.acceptanceTotalTimeout > 0 {
		totalTimeout = s.acceptanceTotalTimeout
	}
	return cmdTimeout, totalTimeout
}

// RunAcceptanceCommands 在任务 worktree 里依次执行验收标准里的 [命令] 条目。
// 没有命令时返回空切片；单条命令超时 5 分钟，全部命令合计上限 15 分钟，
// 超出合计上限后剩余命令直接记为超时且不再执行。本方法不改变任务状态，调用方负责接线。
func (s *Service) RunAcceptanceCommands(ctx context.Context, taskID string) ([]AcceptanceCommandResult, error) {
	cmdTimeout, totalTimeout := s.acceptanceTimeouts()
	return s.runAcceptanceCommands(ctx, taskID, cmdTimeout, totalTimeout)
}

func (s *Service) runAcceptanceCommands(ctx context.Context, taskID string, commandTimeout, totalTimeout time.Duration) ([]AcceptanceCommandResult, error) {
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
	deadline := time.Now().Add(totalTimeout)
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

		timeout := commandTimeout
		if timeout <= 0 || timeout > remaining {
			timeout = remaining
		}

		started := time.Now()
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		cmd := procattr.ShellCommand(runCtx, command)
		cmd.Dir = dir
		// 与 gitCmd 一致继承登录 Shell 环境变量，并告知命令自己运行在任务 worktree 里。
		cmd.Env = append(loginenv.Environ(), "MCODE_WORKTREE_PATH="+dir)
		procutil.SetProcessGroup(cmd)
		cmd.Cancel = func() error { return procutil.KillProcessGroup(cmd, os.Kill) }
		cmd.WaitDelay = 2 * time.Second
		out := procutil.NewTailBuffer(acceptanceCaptureLimit)
		cmd.Stdout = out
		cmd.Stderr = out
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

// acceptanceErrorPattern 匹配常见编译与测试报错关键词，用于在截断时保留报错上下文。
var acceptanceErrorPattern = regexp.MustCompile(`(?i)(?:error|fail|panic|fatal|undefined:|cannot |exception|traceback)`)

type acceptanceLineRange struct {
	start int
	end   int
}

// truncateAcceptanceOutput 在输出超过 4KB 时进行截断，按 UTF-8 rune 边界保证结果合法且不超过上限：
// 无报错行时只保留末尾并加前缀；有报错行时保留至少一半预算的末尾完整行，
// 其余预算按出现顺序填入报错行及其前后各 3 行上下文，省略处以 "…" 行分隔且不与末尾重复。
func truncateAcceptanceOutput(out string) string {
	if len(out) <= acceptanceOutputLimit {
		return out
	}

	// 捕获缓冲区可能从多字节字符中间截断，先剥离开头不完整的 UTF-8 续字节。
	for len(out) > 0 && !utf8.RuneStart(out[0]) {
		out = out[1:]
	}
	if len(out) <= acceptanceOutputLimit {
		return out
	}

	lines := strings.Split(out, "\n")
	anchors := collectAcceptanceErrorAnchors(lines)
	if len(anchors) == 0 {
		return acceptanceTruncatedPrefix + trimTailUTF8(out, acceptanceOutputLimit-len(acceptanceTruncatedPrefix))
	}

	tailBudget := acceptanceOutputLimit / 2
	tailText, tailStart, tailHasPrefixCut := takeAcceptanceTail(lines, tailBudget)

	maxBodyBytes := acceptanceOutputLimit - len(acceptanceTruncatedPrefix)
	avail := maxBodyBytes - len(tailText)

	var selected []string
	var lastEnd int
	usedBytes := 0
	ellipsisSep := "\n" + acceptanceEllipsisLine + "\n"

	for _, anchor := range anchors {
		// 仅保留不与末尾重叠的前半部分；若末尾单行被截断且报错就在该行，则保留其前缀片段。
		var blockLines []string
		end := anchor.end
		if anchor.start >= tailStart {
			if !tailHasPrefixCut || anchor.start > tailStart {
				continue
			}
			blockLines = lines[tailStart : tailStart+1]
			end = tailStart + 1
		} else {
			if end > tailStart {
				end = tailStart
			}
			blockLines = lines[anchor.start:end]
		}

		prefixSepLen := 0
		if len(selected) > 0 {
			prefixSepLen = len(ellipsisSep)
		}
		tailSepLen := len(ellipsisSep)
		if !tailHasPrefixCut && end == tailStart {
			tailSepLen = 1
		}
		maxBlockLen := avail - usedBytes - prefixSepLen - tailSepLen
		if maxBlockLen <= 0 {
			continue
		}

		blockText, ok := fitAcceptanceAnchorBlock(blockLines, maxBlockLen)
		if !ok {
			continue
		}

		if len(selected) > 0 {
			selected = append(selected, acceptanceEllipsisLine)
		}
		selected = append(selected, blockText)
		usedBytes += prefixSepLen + len(blockText)
		lastEnd = end
	}

	if len(selected) == 0 {
		return acceptanceTruncatedPrefix + tailText
	}

	var b strings.Builder
	b.Grow(acceptanceOutputLimit)
	b.WriteString(acceptanceTruncatedPrefix)
	for i, part := range selected {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(part)
	}
	if !tailHasPrefixCut && lastEnd == tailStart {
		b.WriteByte('\n')
	} else {
		b.WriteString(ellipsisSep)
	}
	b.WriteString(tailText)
	return b.String()
}

// collectAcceptanceErrorAnchors 找出所有报错行并向前后各扩展 3 行，合并相邻或重叠的区间。
func collectAcceptanceErrorAnchors(lines []string) []acceptanceLineRange {
	var anchors []acceptanceLineRange
	for i, line := range lines {
		if !acceptanceErrorPattern.MatchString(line) {
			continue
		}
		s := max(0, i-3)
		e := min(len(lines), i+4)
		if len(anchors) > 0 && s <= anchors[len(anchors)-1].end {
			if e > anchors[len(anchors)-1].end {
				anchors[len(anchors)-1].end = e
			}
			continue
		}
		anchors = append(anchors, acceptanceLineRange{start: s, end: e})
	}
	return anchors
}

// takeAcceptanceTail 从后往前按完整行收集末尾内容，单行超出预算时按 rune 边界裁剪尾部。
func takeAcceptanceTail(lines []string, tailBudget int) (text string, start int, hasPrefixCut bool) {
	start = len(lines)
	tailLen := 0
	for i := len(lines) - 1; i >= 0; i-- {
		add := len(lines[i])
		if start < len(lines) {
			add++
		}
		if tailLen+add > tailBudget {
			break
		}
		tailLen += add
		start = i
	}

	if start == len(lines) {
		idx := len(lines) - 1
		return trimTailUTF8(lines[idx], tailBudget), idx, true
	}
	if start == len(lines)-1 && lines[start] == "" && len(lines) >= 2 && lines[len(lines)-2] != "" {
		idx := len(lines) - 2
		return trimTailUTF8(lines[idx], tailBudget-1) + "\n", idx, true
	}
	return strings.Join(lines[start:], "\n"), start, false
}

// fitAcceptanceAnchorBlock 尽量完整放入报错区块；存在超出所剩预算的单行时按 rune 边界截断。
func fitAcceptanceAnchorBlock(blockLines []string, maxLen int) (string, bool) {
	text := strings.Join(blockLines, "\n")
	if len(text) <= maxLen {
		return text, true
	}

	hasOversizeLine := false
	for _, line := range blockLines {
		if len(line) > maxLen {
			hasOversizeLine = true
			break
		}
	}
	if !hasOversizeLine {
		return "", false
	}

	// 计算非超长行与换行符占用的空间，尽量保留上下文并对超长行按 rune 边界截断。
	fixedLen := len(blockLines) - 1
	oversizeCount := 0
	for _, line := range blockLines {
		if len(line) > maxLen {
			oversizeCount++
		} else {
			fixedLen += len(line)
		}
	}

	if oversizeCount == 1 && fixedLen < maxLen {
		out := make([]string, len(blockLines))
		for i, line := range blockLines {
			if len(line) > maxLen {
				out[i] = trimHeadUTF8(line, maxLen-fixedLen)
			} else {
				out[i] = line
			}
		}
		return strings.Join(out, "\n"), true
	}

	// 多行超长时优先保留首条匹配报错关键词的行并裁剪至可用预算。
	for _, line := range blockLines {
		if acceptanceErrorPattern.MatchString(line) {
			return trimHeadUTF8(line, maxLen), true
		}
	}
	return trimHeadUTF8(blockLines[0], maxLen), true
}

// trimTailUTF8 从字符串末尾截取不超过 maxBytes 字节，并按 rune 边界对齐以保证合法 UTF-8。
func trimTailUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) > maxBytes {
		s = s[len(s)-maxBytes:]
	}
	// 丢掉落在 rune 中间的起始字节，再削掉尾部不完整 rune，保证结果仍是合法 UTF-8。
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// trimHeadUTF8 从字符串开头截取不超过 maxBytes 字节，并按 rune 边界对齐以保证合法 UTF-8。
func trimHeadUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	if len(s) > maxBytes {
		s = s[:maxBytes]
	}
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
