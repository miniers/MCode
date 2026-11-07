package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"mcode/internal/providers/loginenv"
)

// prCommandTimeout 是单次 gh/glab 调用的超时：托管平台接口偶尔很慢，
// 但界面不该一直卡在「正在读取」上。
const prCommandTimeout = 20 * time.Second

// prDefaultListLimit 是未指定条数时列出的 PR/MR 数量。
const prDefaultListLimit = 30

// 检查汇总状态：由 statusCheckRollup / head_pipeline 归并出的四种取值。
const (
	prChecksNone    = "none"
	prChecksSuccess = "success"
	prChecksFailure = "failure"
	prChecksPending = "pending"
)

// PRSummary 是 PR/MR 列表里的一条摘要。
type PRSummary struct {
	Number         int       // 编号（GitLab 为 iid）
	Title          string    // 标题
	State          string    // 归一化后的状态：open / closed / merged / locked
	IsDraft        bool      // 是否草稿
	Author         string    // 作者登录名
	HeadBranch     string    // 源分支
	BaseBranch     string    // 目标分支
	URL            string    // 网页地址
	UpdatedAt      time.Time // 最后更新时间（解析不到时为零值）
	ReviewDecision string    // 评审结论：APPROVED / CHANGES_REQUESTED / REVIEW_REQUIRED，未知为空
	ChecksState    string    // CI 汇总：success / failure / pending / none
}

// PRCheck 是一条 CI 检查及其汇总后的状态（success / failure / pending）。
type PRCheck struct {
	Name  string
	State string
}

// PRComment 是 PR/MR 下的一条评论。
type PRComment struct {
	Author    string
	Body      string
	CreatedAt time.Time
}

// PRDetail 是 PR/MR 详情：摘要之外还有正文、增删行数与检查、评论。
type PRDetail struct {
	PRSummary
	Body         string
	Additions    int
	Deletions    int
	ChangedFiles int
	Checks       []PRCheck
	Comments     []PRComment
}

// ghPRListFields 是 gh pr list --json 请求的字段，详情在这些字段之上再加正文、行数与评论。
const ghPRListFields = "number,title,state,isDraft,author,headRefName,baseRefName,url,updatedAt,reviewDecision,statusCheckRollup"

// ghPRViewFields 是 gh pr view --json 请求的字段。
const ghPRViewFields = ghPRListFields + ",body,additions,deletions,changedFiles,comments"

// PRList 列出仓库当前开放的 PR/MR；limit<=0 时默认 30 条。
// GitHub 走 gh pr list，GitLab 走 glab mr list -F json（该命令不接受条数参数，结果在这里截断）。
// 没有远端、平台不支持、CLI 缺失或未登录时返回中文错误。
func (r *Runner) PRList(ctx context.Context, root string, limit int) ([]PRSummary, error) {
	forge, err := r.prForge(ctx, root)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = prDefaultListLimit
	}
	callCtx, cancel := context.WithTimeout(ctx, prCommandTimeout)
	defer cancel()

	switch forge {
	case ForgeGitHub:
		out, err := runForgeCLI(callCtx, root, "gh", "pr", "list",
			"--state", "open", "--limit", strconv.Itoa(limit), "--json", ghPRListFields)
		if err != nil {
			return nil, forgeCLIError("gh", "读取 PR 列表", out, err)
		}
		return parseGHPRList(out)
	case ForgeGitLab:
		out, err := runForgeCLI(callCtx, root, "glab", "mr", "list", "-F", "json")
		if err != nil {
			return nil, forgeCLIError("glab", "读取 MR 列表", out, err)
		}
		items, err := parseGLabMRList(out)
		if err != nil {
			return nil, err
		}
		if len(items) > limit {
			items = items[:limit]
		}
		return items, nil
	default:
		return nil, ErrForgeUnsupported
	}
}

// PRDetail 读取单条 PR/MR 的详情；number 必须是正整数。
func (r *Runner) PRDetail(ctx context.Context, root string, number int) (PRDetail, error) {
	if number <= 0 {
		return PRDetail{}, fmt.Errorf("无效的 PR 编号: %d", number)
	}
	forge, err := r.prForge(ctx, root)
	if err != nil {
		return PRDetail{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, prCommandTimeout)
	defer cancel()

	ref := strconv.Itoa(number)
	switch forge {
	case ForgeGitHub:
		out, err := runForgeCLI(callCtx, root, "gh", "pr", "view", ref, "--json", ghPRViewFields)
		if err != nil {
			return PRDetail{}, forgeCLIError("gh", "读取 PR 详情", out, err)
		}
		return parseGHPRDetail(out)
	case ForgeGitLab:
		out, err := runForgeCLI(callCtx, root, "glab", "mr", "view", ref, "-F", "json")
		if err != nil {
			return PRDetail{}, forgeCLIError("glab", "读取 MR 详情", out, err)
		}
		return parseGLabMRDetail(out)
	default:
		return PRDetail{}, ErrForgeUnsupported
	}
}

// prForge 读取 origin 远端并识别托管平台；没有远端或平台未知时返回中文错误。
func (r *Runner) prForge(ctx context.Context, root string) (Forge, error) {
	if r == nil {
		return ForgeUnknown, errors.New("git runner is nil")
	}
	remoteURL, err := r.RemoteURL(ctx, root)
	if err != nil || strings.TrimSpace(remoteURL) == "" {
		return ForgeUnknown, errors.New("当前仓库没有远端（origin），无法读取 PR/MR 列表")
	}
	forge := DetectForge(remoteURL)
	if forge == ForgeUnknown {
		return ForgeUnknown, fmt.Errorf("暂不支持该托管平台的 PR/MR：%s", strings.TrimSpace(remoteURL))
	}
	return forge, nil
}

// runForgeCLI 执行 gh/glab 并返回合并后的输出；CLI 未安装时直接返回中文错误。
func runForgeCLI(ctx context.Context, dir, tool string, args ...string) ([]byte, error) {
	if err := ensureForgeCLI(tool); err != nil {
		return nil, err
	}
	return defaultCommandExecutor(ctx, dir, tool, args...)
}

// ensureForgeCLI 检查 gh/glab 是否已安装（按登录 Shell 的 PATH 查找）。
func ensureForgeCLI(tool string) error {
	if _, err := loginenv.LookPath(tool); err == nil {
		return nil
	}
	switch tool {
	case "gh":
		return errors.New("未安装 GitHub CLI(gh)")
	case "glab":
		return errors.New("未安装 GitLab CLI(glab)")
	}
	return fmt.Errorf("未安装 %s", tool)
}

// forgeCLIError 把 gh/glab 的失败输出翻译成中文提示，其余情况保留原始输出摘要。
func forgeCLIError(tool, action string, out []byte, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s超时，请稍后重试", action)
	}
	text := strings.ToLower(string(out))
	switch {
	case strings.Contains(text, "auth login"), strings.Contains(text, "not logged in"),
		strings.Contains(text, "authentication required"), strings.Contains(text, "no oauth token"):
		return fmt.Errorf("%s 未登录，请先运行 %s auth login", tool, tool)
	case strings.Contains(text, "no such host"), strings.Contains(text, "could not resolve host"),
		strings.Contains(text, "connection refused"), strings.Contains(text, "network is unreachable"):
		return fmt.Errorf("%s失败，请检查网络或代理: %s", action, outputSummary(out))
	case strings.Contains(text, "no git remotes"), strings.Contains(text, "not a git repository"):
		return errors.New("当前目录不是有效的 Git 仓库或没有远端")
	}
	detail := outputSummary(out)
	if detail == "" {
		return fmt.Errorf("%s失败: %w", action, err)
	}
	return fmt.Errorf("%s失败: %w: %s", action, err, detail)
}

// ghAuthor 是 gh 输出里的作者对象。
type ghAuthor struct {
	Login string `json:"login"`
}

// ghCheck 是一条 statusCheckRollup 条目：CheckRun 用 name/status/conclusion，
// StatusContext 用 context/state，两者靠 __typename 区分。
type ghCheck struct {
	TypeName   string `json:"__typename"`
	Name       string `json:"name"`
	Context    string `json:"context"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
}

// ghComment 是 gh 输出里的一条评论。
type ghComment struct {
	Author    ghAuthor `json:"author"`
	Body      string   `json:"body"`
	CreatedAt string   `json:"createdAt"`
}

// ghPR 是 gh 输出的 PR 主体；列表与详情共用，详情多带正文、行数与评论字段。
type ghPR struct {
	Number            int         `json:"number"`
	Title             string      `json:"title"`
	State             string      `json:"state"`
	IsDraft           bool        `json:"isDraft"`
	Author            ghAuthor    `json:"author"`
	HeadRefName       string      `json:"headRefName"`
	BaseRefName       string      `json:"baseRefName"`
	URL               string      `json:"url"`
	UpdatedAt         string      `json:"updatedAt"`
	ReviewDecision    string      `json:"reviewDecision"`
	StatusCheckRollup []ghCheck   `json:"statusCheckRollup"`
	Body              string      `json:"body"`
	Additions         int         `json:"additions"`
	Deletions         int         `json:"deletions"`
	ChangedFiles      int         `json:"changedFiles"`
	Comments          []ghComment `json:"comments"`
}

// summary 把 gh 的 PR 主体折算成摘要。
func (p ghPR) summary() PRSummary {
	return PRSummary{
		Number:         p.Number,
		Title:          strings.TrimSpace(p.Title),
		State:          normalizePRState(p.State),
		IsDraft:        p.IsDraft,
		Author:         strings.TrimSpace(p.Author.Login),
		HeadBranch:     strings.TrimSpace(p.HeadRefName),
		BaseBranch:     strings.TrimSpace(p.BaseRefName),
		URL:            strings.TrimSpace(p.URL),
		UpdatedAt:      parseForgeTime(p.UpdatedAt),
		ReviewDecision: strings.TrimSpace(p.ReviewDecision),
		ChecksState:    summarizeCheckStates(ghCheckStates(p.StatusCheckRollup)),
	}
}

// parseGHPRList 解析 gh pr list --json 的输出。
func parseGHPRList(out []byte) ([]PRSummary, error) {
	var items []ghPR
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, fmt.Errorf("解析 gh pr list 输出失败: %w", err)
	}
	summaries := make([]PRSummary, 0, len(items))
	for _, item := range items {
		summaries = append(summaries, item.summary())
	}
	return summaries, nil
}

// parseGHPRDetail 解析 gh pr view --json 的输出。
func parseGHPRDetail(out []byte) (PRDetail, error) {
	var item ghPR
	if err := json.Unmarshal(out, &item); err != nil {
		return PRDetail{}, fmt.Errorf("解析 gh pr view 输出失败: %w", err)
	}
	return PRDetail{
		PRSummary:    item.summary(),
		Body:         strings.TrimSpace(item.Body),
		Additions:    item.Additions,
		Deletions:    item.Deletions,
		ChangedFiles: item.ChangedFiles,
		Checks:       ghChecks(item.StatusCheckRollup),
		Comments:     ghComments(item.Comments),
	}, nil
}

// ghCheckStates 返回每条检查归一化后的状态，交给 summarizeCheckStates 汇总。
func ghCheckStates(checks []ghCheck) []string {
	if len(checks) == 0 {
		return nil
	}
	states := make([]string, 0, len(checks))
	for _, check := range checks {
		states = append(states, normalizeGHCheckState(check))
	}
	return states
}

// ghChecks 返回逐项检查的名称与归一化状态，名称优先取 CheckRun 的 name，其次 StatusContext 的 context。
func ghChecks(checks []ghCheck) []PRCheck {
	if len(checks) == 0 {
		return nil
	}
	items := make([]PRCheck, 0, len(checks))
	for _, check := range checks {
		name := strings.TrimSpace(check.Name)
		if name == "" {
			name = strings.TrimSpace(check.Context)
		}
		state := normalizeGHCheckState(check)
		if state == "" {
			state = prChecksPending
		}
		items = append(items, PRCheck{Name: name, State: state})
	}
	return items
}

// normalizeGHCheckState 把一条检查的原始字段归一化成 success / failure / pending，
// 无法判断时返回空字符串（汇总时按「进行中」处理）。
func normalizeGHCheckState(check ghCheck) string {
	if strings.EqualFold(check.TypeName, "StatusContext") || (check.State != "" && check.Conclusion == "") {
		switch strings.ToUpper(strings.TrimSpace(check.State)) {
		case "SUCCESS":
			return prChecksSuccess
		case "PENDING", "EXPECTED":
			return prChecksPending
		case "FAILURE", "ERROR":
			return prChecksFailure
		}
		return ""
	}
	// CheckRun：未完成即进行中，完成后看 conclusion。
	if !strings.EqualFold(strings.TrimSpace(check.Status), "COMPLETED") {
		return prChecksPending
	}
	switch strings.ToUpper(strings.TrimSpace(check.Conclusion)) {
	case "SUCCESS", "NEUTRAL", "SKIPPED":
		return prChecksSuccess
	case "FAILURE", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE", "STALE":
		return prChecksFailure
	}
	return ""
}

// summarizeCheckStates 把各条检查的状态汇总成 success / failure / pending / none：
// 任意一条失败即失败，其次任意一条（含未知）未完成即进行中，否则通过；没有检查时是 none。
// 纯函数，便于单测。
func summarizeCheckStates(states []string) string {
	if len(states) == 0 {
		return prChecksNone
	}
	pending := false
	success := false
	for _, state := range states {
		switch strings.ToLower(strings.TrimSpace(state)) {
		case prChecksFailure:
			return prChecksFailure
		case prChecksSuccess:
			success = true
		default:
			pending = true
		}
	}
	if pending {
		return prChecksPending
	}
	if success {
		return prChecksSuccess
	}
	return prChecksPending
}

// ghComments 把 gh 的评论折算成评论列表，跳过空正文。
func ghComments(comments []ghComment) []PRComment {
	if len(comments) == 0 {
		return nil
	}
	items := make([]PRComment, 0, len(comments))
	for _, comment := range comments {
		body := strings.TrimSpace(comment.Body)
		if body == "" {
			continue
		}
		items = append(items, PRComment{
			Author:    strings.TrimSpace(comment.Author.Login),
			Body:      body,
			CreatedAt: parseForgeTime(comment.CreatedAt),
		})
	}
	return items
}

// glabAuthor 是 glab 输出里的作者对象。
type glabAuthor struct {
	Username string `json:"username"`
	Name     string `json:"name"`
}

// glabPipeline 是 glab 输出里的流水线对象，只用到状态。
type glabPipeline struct {
	Status string `json:"status"`
}

// glabMR 是 glab 输出的 MR 主体；列表与详情共用，详情多带 description。
type glabMR struct {
	IID            int           `json:"iid"`
	Number         int           `json:"number"`
	Title          string        `json:"title"`
	State          string        `json:"state"`
	Draft          bool          `json:"draft"`
	WorkInProgress bool          `json:"work_in_progress"`
	Author         glabAuthor    `json:"author"`
	SourceBranch   string        `json:"source_branch"`
	TargetBranch   string        `json:"target_branch"`
	WebURL         string        `json:"web_url"`
	UpdatedAt      string        `json:"updated_at"`
	Description    string        `json:"description"`
	HeadPipeline   *glabPipeline `json:"head_pipeline"`
}

// summary 把 glab 的 MR 主体折算成摘要；glab 没有评审结论字段，留空。
func (m glabMR) summary() PRSummary {
	number := m.IID
	if number == 0 {
		number = m.Number
	}
	author := strings.TrimSpace(m.Author.Username)
	if author == "" {
		author = strings.TrimSpace(m.Author.Name)
	}
	checks := prChecksNone
	if m.HeadPipeline != nil {
		checks = normalizeGLabPipelineState(m.HeadPipeline.Status)
	}
	return PRSummary{
		Number:         number,
		Title:          strings.TrimSpace(m.Title),
		State:          normalizePRState(m.State),
		IsDraft:        m.Draft || m.WorkInProgress,
		Author:         author,
		HeadBranch:     strings.TrimSpace(m.SourceBranch),
		BaseBranch:     strings.TrimSpace(m.TargetBranch),
		URL:            strings.TrimSpace(m.WebURL),
		UpdatedAt:      parseForgeTime(m.UpdatedAt),
		ReviewDecision: "",
		ChecksState:    checks,
	}
}

// parseGLabMRList 解析 glab mr list -F json 的输出。
func parseGLabMRList(out []byte) ([]PRSummary, error) {
	var items []glabMR
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, fmt.Errorf("解析 glab mr list 输出失败: %w", err)
	}
	summaries := make([]PRSummary, 0, len(items))
	for _, item := range items {
		summaries = append(summaries, item.summary())
	}
	return summaries, nil
}

// parseGLabMRDetail 解析 glab mr view -F json 的输出。
// glab 不提供增删行数、逐项检查与评论，这些字段留空（不报错）。
func parseGLabMRDetail(out []byte) (PRDetail, error) {
	var item glabMR
	if err := json.Unmarshal(out, &item); err != nil {
		return PRDetail{}, fmt.Errorf("解析 glab mr view 输出失败: %w", err)
	}
	return PRDetail{
		PRSummary: item.summary(),
		Body:      strings.TrimSpace(item.Description),
	}, nil
}

// normalizeGLabPipelineState 把 GitLab 流水线状态归一化成 success / failure / pending / none。
func normalizeGLabPipelineState(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "":
		return prChecksNone
	case "success", "passed":
		return prChecksSuccess
	case "failed", "canceled", "cancelled":
		return prChecksFailure
	default:
		// created / waiting_for_resource / preparing / pending / running / manual / skipped 都算未完成
		return prChecksPending
	}
}

// normalizePRState 把各平台的状态归一化成 open / closed / merged / locked，未知时返回小写原文。
func normalizePRState(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "open", "opened":
		return "open"
	case "merged":
		return "merged"
	case "closed":
		return "closed"
	case "locked":
		return "locked"
	default:
		return strings.ToLower(strings.TrimSpace(state))
	}
}

// parseForgeTime 解析 gh/glab 输出里的时间，解析不出来时返回零值（调用方按「无时间」处理）。
func parseForgeTime(raw string) time.Time {
	text := strings.TrimSpace(raw)
	if text == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05 -0700 MST",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, text); err == nil {
			return t
		}
	}
	return time.Time{}
}
