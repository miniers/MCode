package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mcode/internal/git"
	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
	"mcode/internal/remote/wire"
)

// GitPRList 列出会话所在仓库开放的 PR/MR，limit<=0 时由 git 层取默认条数。
// 与贮藏列表一样，仓库根目录由 gitStashTarget 定位（gh/glab 需要在仓库内执行）。
func (c *Client) GitPRList(ctx context.Context, threadID string, limit int) ([]git.PRSummary, error) {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return nil, err
	}
	return runner.PRList(ctx, root, limit)
}

// GitPRForBranch 查询当前分支最近的一条 PR/MR（任意状态，含已合并）。
// 处于游离 HEAD 或空分支时返回 (nil, nil)。
func (c *Client) GitPRForBranch(ctx context.Context, threadID string) (*git.PRSummary, error) {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return nil, err
	}
	branch, err := runner.CurrentBranch(ctx, root)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(branch) == "" {
		return nil, nil
	}
	return runner.PRForBranch(ctx, root, branch)
}

// GitPRCreate 为当前分支创建 PR/MR 并返回其网页地址（推送由调用方先执行）。
func (c *Client) GitPRCreate(ctx context.Context, threadID, title, body, base string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", errors.New("PR 标题不能为空")
	}
	base = strings.TrimSpace(base)
	if base == "" {
		return "", errors.New("目标分支不能为空")
	}
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return "", err
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	head, err := runner.CurrentBranch(callCtx, root)
	if err != nil {
		return "", err
	}
	head = strings.TrimSpace(head)
	if head == "" {
		return "", errors.New("当前处于游离 HEAD 状态，无法创建 PR/MR")
	}
	remoteURL, err := runner.RemoteURL(callCtx, root)
	if err != nil && callCtx.Err() != nil {
		return "", callCtx.Err()
	}
	forge := git.DetectForge(remoteURL)
	if forge == git.ForgeUnknown {
		return "", git.ErrForgeUnsupported
	}
	return git.NewForgeClient(nil).CreatePullRequest(callCtx, root, forge, git.PullRequestSpec{
		Title: title,
		Body:  body,
		Head:  head,
		Base:  base,
	})
}

// GitPRMerge 按指定方式合入 PR/MR，并删除源分支。
func (c *Client) GitPRMerge(ctx context.Context, threadID string, number int, method string) error {
	if method == "" {
		return errors.New("合并方式不能为空")
	}
	if number <= 0 {
		return fmt.Errorf("无效的 PR 编号: %d", number)
	}
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	remoteURL, err := runner.RemoteURL(callCtx, root)
	if err != nil {
		return err
	}
	return git.NewForgeClient(nil).MergePullRequest(callCtx, root, git.DetectForge(remoteURL), strconv.Itoa(number), method)
}

// GitPRReview 提交 PR/MR 审查。
func (c *Client) GitPRReview(ctx context.Context, threadID string, number int, event, body string) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	remoteURL, err := runner.RemoteURL(callCtx, root)
	if err != nil {
		return err
	}
	return git.NewForgeClient(nil).ReviewPullRequest(callCtx, root, git.DetectForge(remoteURL), number, event, body)
}

// GitPRSubmitReview 提交包含行内评论的一次 PR/MR 审查。
func (c *Client) GitPRSubmitReview(ctx context.Context, threadID string, number int, event, body string, comments []git.PRReviewComment) error {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	remoteURL, err := runner.RemoteURL(callCtx, root)
	if err != nil {
		return err
	}
	return git.NewForgeClient(nil).SubmitPullRequestReview(callCtx, root, git.DetectForge(remoteURL), number, event, body, comments)
}

// GitPRConflicts 计算 PR/MR head 与目标分支之间的冲突文件；无冲突时返回空切片。
func (c *Client) GitPRConflicts(ctx context.Context, threadID string, number int, base string) ([]string, error) {
	if number <= 0 {
		return nil, fmt.Errorf("无效的 PR 编号: %d", number)
	}
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	remoteURL, err := runner.RemoteURL(callCtx, root)
	if err != nil {
		return nil, err
	}
	return runner.PRConflicts(callCtx, root, git.DetectForge(remoteURL), number, base)
}

// GitPRResolveWorktree 为 PR/MR 创建冲突解决 worktree，返回 worktree 路径。
func (c *Client) GitPRResolveWorktree(ctx context.Context, threadID string, number int, head, base string) (string, error) {
	if number <= 0 {
		return "", fmt.Errorf("无效的 PR 编号: %d", number)
	}
	dataDir := c.effectiveDataDir()
	if dataDir == "" {
		return "", errors.New("数据目录未初始化，无法创建 Worktree")
	}
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return "", err
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	mainRoot, err := runner.MainRepoRoot(callCtx, root)
	if err != nil {
		return "", err
	}
	path := prWorktreePath(dataDir, mainRoot, number)
	if err := runner.PRResolveWorktree(callCtx, mainRoot, path, number, head, base); err != nil {
		return "", err
	}
	return path, nil
}

func prWorktreePath(dataDir, mainRoot string, number int) string {
	return filepath.Join(dataDir, "worktrees", filepath.Base(mainRoot)+"-pr-"+strconv.Itoa(number))
}

// GitPRCheckoutWorktree 为 PR/MR 检出 worktree（不合并 base），返回 worktree 路径。
func (c *Client) GitPRCheckoutWorktree(ctx context.Context, threadID string, number int, head string, crossRepo bool) (string, error) {
	if number <= 0 {
		return "", fmt.Errorf("无效的 PR 编号: %d", number)
	}
	dataDir := c.effectiveDataDir()
	if dataDir == "" {
		return "", errors.New("数据目录未初始化，无法创建 Worktree")
	}
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return "", err
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	mainRoot, err := runner.MainRepoRoot(callCtx, root)
	if err != nil {
		return "", err
	}
	path := prWorktreePath(dataDir, mainRoot, number)
	remoteURL, err := runner.RemoteURL(callCtx, mainRoot)
	if err != nil {
		return "", err
	}
	if err := runner.PRCheckoutWorktree(callCtx, mainRoot, path, git.DetectForge(remoteURL), number, head, crossRepo); err != nil {
		return "", err
	}
	return path, nil
}

// GitPRAIReview 从当前会话里 agent 输出的最后一个 pr-review 代码围栏块取行内审查结果，
// 按 PR diff 校验后返回草稿，由用户确认后再提交。
// 这是用户主动发起的操作，不检查「生成提交信息」开关。
func (c *Client) GitPRAIReview(ctx context.Context, threadID string, number int) ([]git.PRReviewComment, error) {
	if number <= 0 {
		return nil, fmt.Errorf("无效的 PR 编号: %d", number)
	}
	msgs, err := c.History(ctx, threadID)
	if err != nil {
		return nil, err
	}
	var block string
	found := false
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "assistant" {
			continue
		}
		if b, ok := prReviewBlock(msgs[i].Text); ok {
			block, found = b, true
			break
		}
	}
	if !found {
		return nil, errors.New("当前会话没有 AI 审查结果")
	}
	detail, err := c.GitPRDetail(ctx, threadID, number)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(detail.BaseBranch) == "" {
		return nil, errors.New("PR base 分支为空")
	}
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	remoteURL, err := runner.RemoteURL(callCtx, root)
	if err != nil {
		return nil, err
	}
	forge := git.DetectForge(remoteURL)
	if forge == git.ForgeUnknown {
		return nil, git.ErrForgeUnsupported
	}
	head, err := runner.FetchPRHead(callCtx, root, forge, number)
	if err != nil {
		return nil, err
	}
	base, err := runner.ReviewPRBase(callCtx, root, detail.BaseBranch, head)
	if err != nil {
		return nil, err
	}
	patch, err := gitPRDiff(callCtx, root, base, head)
	if err != nil {
		return nil, err
	}
	if patch == "" {
		return []git.PRReviewComment{}, nil
	}
	return parsePRAIReview(block, patch)
}

// gitPRDiff 在仓库根目录取 base 到 head 的补丁。
func gitPRDiff(ctx context.Context, root, base, head string) (string, error) {
	cmd := procattr.TerminateOnCancel(procattr.HideWindow(exec.CommandContext(
		ctx, "git", "-c", "core.quotePath=false", "diff",
		"--no-color", "--no-ext-diff", "--src-prefix=a/", "--dst-prefix=b/", base, head,
	)))
	cmd.Dir = root
	cmd.Env = loginenv.Environ()
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("读取 PR diff: %w", err)
	}
	return strings.TrimSpace(out.String()), nil
}

// prReviewBlock 返回 text 中最后一个 pr-review 代码围栏块的内容（不含围栏行），
// 没有块或块未闭合时返回 false。
func prReviewBlock(text string) (string, bool) {
	lines := strings.Split(text, "\n")
	open := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "```pr-review" {
			open = i
		}
	}
	if open < 0 {
		return "", false
	}
	for i := open + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "```" {
			return strings.Join(lines[open+1:i], "\n"), true
		}
	}
	return "", false
}

// prReviewResult 是模型输出的一条审查意见。
type prReviewResult struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Body string `json:"body"`
}

// parsePRAIReview 解析模型输出，同时兼容新版结论对象（含 comments 字段）与旧版评论数组，
// 容忍代码围栏与前后的说明文字，只保留落在 diff 新侧有效行号上的评论，
// 按 path+line 去重，最多 10 条。
func parsePRAIReview(out, patch string) ([]git.PRReviewComment, error) {
	var items []prReviewResult
	var lastErr error
	parsed, err := findPRAIReviewJSON(out, '{', func(dec *json.Decoder) (bool, error) {
		var candidate struct {
			Comments *[]prReviewResult `json:"comments"`
		}
		if err := dec.Decode(&candidate); err != nil {
			return false, err
		}
		if candidate.Comments == nil {
			return false, nil
		}
		items = *candidate.Comments
		return true, nil
	})
	if !parsed {
		if err != nil {
			lastErr = err
		}
		var arrErr error
		parsed, arrErr = findPRAIReviewJSON(out, '[', func(dec *json.Decoder) (bool, error) {
			var candidate []prReviewResult
			if err := dec.Decode(&candidate); err != nil {
				return false, err
			}
			items = candidate
			return true, nil
		})
		if !parsed && arrErr != nil {
			lastErr = arrErr
		}
	}
	if !parsed {
		if lastErr != nil {
			return nil, fmt.Errorf("AI 审查结果无法解析: %w", lastErr)
		}
		return nil, errors.New("AI 审查结果无法解析")
	}
	valid := prDiffNewLines(patch)
	seen := make(map[string]bool, len(items))
	comments := make([]git.PRReviewComment, 0, len(items))
	for _, item := range items {
		path := strings.TrimSpace(item.Path)
		body := strings.TrimSpace(item.Body)
		if path == "" || body == "" || item.Line <= 0 || !valid[path][item.Line] {
			continue
		}
		key := path + "\x00" + strconv.Itoa(item.Line)
		if seen[key] {
			continue
		}
		seen[key] = true
		comments = append(comments, git.PRReviewComment{Path: path, Line: item.Line, Side: "RIGHT", Body: body})
		if len(comments) == 10 {
			break
		}
	}
	return comments, nil
}

// findPRAIReviewJSON 从 out 中按指定起始分隔符依次尝试 JSON 解码，直到 decode 返回 true。
func findPRAIReviewJSON(out string, open byte, decode func(*json.Decoder) (bool, error)) (bool, error) {
	var lastErr error
	for offset := 0; offset < len(out); {
		rel := strings.IndexByte(out[offset:], open)
		if rel < 0 {
			break
		}
		start := offset + rel
		dec := json.NewDecoder(strings.NewReader(out[start:]))
		ok, err := decode(dec)
		if err == nil && ok {
			return true, nil
		}
		if err != nil {
			lastErr = err
		}
		offset = start + 1
	}
	return false, lastErr
}

// prDiffNewLines 解析 unified diff，返回每个文件新侧的有效行号集合
// （hunk 内的新增行与上下文行）。
func prDiffNewLines(patch string) map[string]map[int]bool {
	lines := make(map[string]map[int]bool)
	path := ""
	newLine := 0
	inHunk := false
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			inHunk = false
			path = ""
			newLine = 0
		case !inHunk && strings.HasPrefix(line, "+++ "):
			path = ""
			if rest, ok := strings.CutPrefix(line, "+++ b/"); ok {
				path = strings.TrimSpace(strings.SplitN(rest, "\t", 2)[0])
				if path != "" && lines[path] == nil {
					lines[path] = make(map[int]bool)
				}
			}
		case strings.HasPrefix(line, "@@ "):
			newLine, inHunk = prHunkNewStart(line)
		case !inHunk || path == "":
			continue
		case strings.HasPrefix(line, "+"):
			lines[path][newLine] = true
			newLine++
		case strings.HasPrefix(line, " "):
			lines[path][newLine] = true
			newLine++
		}
	}
	return lines
}

// prHunkNewStart 解析 @@ -old,count +new,count @@ 里的新侧起始行号。
func prHunkNewStart(line string) (int, bool) {
	_, rest, ok := strings.Cut(line, "+")
	if !ok {
		return 0, false
	}
	if end := strings.IndexAny(rest, ", "); end >= 0 {
		rest = rest[:end]
	}
	n, err := strconv.Atoi(rest)
	if err != nil {
		return 0, false
	}
	return n, true
}

// GitPRCleanupWorktree 在合并后把空闲会话切回主仓库，再清理安全的 PR worktree。
func (c *Client) GitPRCleanupWorktree(ctx context.Context, threadID string, number int) (string, error) {
	if number <= 0 {
		return "", fmt.Errorf("无效的 PR 编号: %d", number)
	}
	dataDir := c.effectiveDataDir()
	if dataDir == "" {
		return "", errors.New("数据目录未初始化，无法清理 Worktree")
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	runner, root, err := c.gitStashTarget(callCtx, threadID)
	if err != nil {
		return "", err
	}
	mainRoot, err := runner.MainRepoRoot(callCtx, root)
	if err != nil {
		return "", err
	}
	path := prWorktreePath(dataDir, mainRoot, number)
	remoteURL, err := runner.RemoteURL(callCtx, mainRoot)
	if err != nil {
		return "", err
	}
	if reason, err := runner.PRWorktreeKeepReason(callCtx, mainRoot, path, git.DetectForge(remoteURL), number); err != nil || reason != "" {
		return reason, err
	}
	includeArchived := true
	threads, err := c.backend.ListThreads(callCtx, wire.ThreadListParams{IncludeArchived: &includeArchived})
	if err != nil {
		return "", err
	}
	cleanPath := func(path string) string {
		if real, err := filepath.EvalSymlinks(path); err == nil {
			path = real
		}
		return filepath.Clean(path)
	}
	var affected []string
	for _, thread := range threads.Threads {
		workDir, ok := c.ThreadWorkDir(thread.ID)
		if !ok || cleanPath(workDir) != cleanPath(path) {
			continue
		}
		if thread.IsRunning {
			return "会话正在运行", nil
		}
		affected = append(affected, thread.ID)
	}
	for _, id := range affected {
		if err := c.GitSwitchWorktree(callCtx, id, mainRoot); err != nil {
			return "", err
		}
	}
	return "", runner.PRRemoveWorktree(callCtx, mainRoot, path, number)
}

// GitPRDetail 读取单条 PR/MR 的详情。
func (c *Client) GitPRDetail(ctx context.Context, threadID string, number int) (git.PRDetail, error) {
	runner, root, err := c.gitStashTarget(ctx, threadID)
	if err != nil {
		return git.PRDetail{}, err
	}
	return runner.PRDetail(ctx, root, number)
}
