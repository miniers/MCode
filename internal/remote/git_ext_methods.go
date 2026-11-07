package remote

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"mcode/internal/git"
	"mcode/internal/remote/wire"
)

// 远程 git 扩展方法：分文件提交、贮藏管理、PR/MR 只读查看与 Git 写操作。
//
// 能力分布：
//   - git.changedFiles / git.commitSelected / git.stash.pop 直接落在 MemoryBackend 上，
//     用接口断言取用；
//   - git.stash.list / git.stash.push / git.stash.apply / git.stash.drop / git.pr.* 与
//     git.push / git.pull / git.fetch / git.checkout / git.createBranch / git.merge /
//     git.generateCommitMessage 只在桌面端 *Client 上（依赖 Client 自持的 git 执行器与仓库定位），
//     由 app 层以 GitExtServiceName 注入服务后取用。
//
// 错误处理：参数问题用 ExtInvalidParams；服务未注入用 ExtNotImplemented；
// gh / glab 未安装或未登录等外部命令错误按原样返回——Dispatcher 走普通错误分支，
// 回包保留原始文案（仅做敏感信息脱敏），前端据此提示用户去安装或登录。
// 网络写操作（推送、拉取、抓取、提交信息生成）在 handler 内套一层超时，避免请求悬挂。

// GitExtServiceName 是 app 层注入 git 扩展服务的服务名。
const GitExtServiceName = "git"

const (
	// gitExtPRListDefaultLimit 是 git.pr.list 未指定条数时的默认返回条数。
	gitExtPRListDefaultLimit = 20
	// gitExtPRListMaxLimit 是 git.pr.list 单次最多返回的条数。
	gitExtPRListMaxLimit = 100
	// gitExtNetworkTimeout 是携带网络往返的写操作（推送、拉取、抓取、生成提交信息）的上限。
	// 大仓库或慢网络下耗时可能较长，给足时间但不无限等待。
	gitExtNetworkTimeout = 5 * time.Minute
	// gitExtLocalTimeout 是纯本地操作的超时：切换分支、新建分支、合并与生成 PR 链接。
	gitExtLocalTimeout = 2 * time.Minute
)

// gitExtService 是只在桌面端 *Client 上可用的 git 能力，由 app 层注入。
type gitExtService interface {
	GitStashList(ctx context.Context, threadID string) ([]git.StashEntry, error)
	GitStashPush(ctx context.Context, threadID, message string) error
	GitStashApply(ctx context.Context, threadID string, index int) error
	GitStashDrop(ctx context.Context, threadID string, index int) error
	GitPRList(ctx context.Context, threadID string, limit int) ([]git.PRSummary, error)
	GitPRDetail(ctx context.Context, threadID string, number int) (git.PRDetail, error)
	GitPush(ctx context.Context, threadID string) error
	GitPull(ctx context.Context, threadID string) error
	GitFetch(ctx context.Context, threadID string) error
	GitCheckout(ctx context.Context, threadID, branch string) error
	GitCreateBranch(ctx context.Context, threadID, name string) error
	GitMergeFrom(ctx context.Context, threadID, branch string) error
	GitGenerateCommitMessage(ctx context.Context, threadID string) (string, error)
	// GitPullRequestCreateURL 返回创建 PR/MR 的网页地址与预填的标题、正文
	// （宿主没有预填内容时后两者为空）。
	GitPullRequestCreateURL(ctx context.Context, threadID string) (url, title, body string, err error)
}

// gitExtChangedFilesBackend 是 git.changedFiles 需要的 MemoryBackend 能力。
type gitExtChangedFilesBackend interface {
	GitChangedFiles(ctx context.Context, threadID string) ([]git.ChangedFile, error)
}

// gitExtCommitBackend 是 git.commitSelected 需要的 MemoryBackend 能力。
type gitExtCommitBackend interface {
	GitCommitSelected(ctx context.Context, threadID string, paths []string, message string) error
}

// gitExtStashPopBackend 是 git.stash.pop 需要的 MemoryBackend 能力。
type gitExtStashPopBackend interface {
	GitStashPop(ctx context.Context, threadID string) error
}

func init() {
	RegisterMethod("git.changedFiles", handleGitExtChangedFiles)
	RegisterMethod("git.commitSelected", handleGitExtCommitSelected)
	RegisterMethod("git.stash.list", handleGitExtStashList)
	RegisterMethod("git.stash.push", handleGitExtStashPush)
	RegisterMethod("git.stash.pop", handleGitExtStashPop)
	RegisterMethod("git.stash.apply", handleGitExtStashApply)
	RegisterMethod("git.stash.drop", handleGitExtStashDrop)
	RegisterMethod("git.pr.list", handleGitExtPRList)
	RegisterMethod("git.pr.detail", handleGitExtPRDetail)
	RegisterMethod("git.push", handleGitExtPush)
	RegisterMethod("git.pull", handleGitExtPull)
	RegisterMethod("git.fetch", handleGitExtFetch)
	RegisterMethod("git.checkout", handleGitExtCheckout)
	RegisterMethod("git.createBranch", handleGitExtCreateBranch)
	RegisterMethod("git.merge", handleGitExtMerge)
	RegisterMethod("git.generateCommitMessage", handleGitExtGenerateCommitMessage)
	RegisterMethod("git.pr.createUrl", handleGitExtPRCreateURL)
}

// gitExtBackend 把后端断言成一项内存后端能力，未实现时返回 false。
func gitExtBackend[T any](d *Dispatcher) (T, bool) {
	var zero T
	if d == nil || d.backend == nil {
		return zero, false
	}
	svc, ok := d.backend.(T)
	return svc, ok
}

// gitExtServiceFrom 取出 app 层注入的 git 扩展服务。
func gitExtServiceFrom(d *Dispatcher) (gitExtService, bool) {
	if d == nil {
		return nil, false
	}
	return backendService[gitExtService](d.backend, GitExtServiceName)
}

// gitExtThreadIDParam 校验 threadID 字段：必须是合法 UUID。
func gitExtThreadIDParam(threadID string) error {
	if !isValidUUID(threadID) {
		return ExtInvalidParams("Invalid params: 'threadID' must be a valid UUID")
	}
	return nil
}

// gitExtBranchNameValid 判断分支名是否满足 git 引用命名的基本约束：
// 非空、不以 '-' 开头（否则会被当成命令行选项）、不含空白与控制字符，
// 也不含 git 拒绝的符号。与 desktop 端 git.Runner 的检查同源，此处提前拦截以便返回参数错误。
func gitExtBranchNameValid(name string) bool {
	if name == "" || strings.HasPrefix(name, "-") {
		return false
	}
	if strings.ContainsAny(name, " \t\r\n\x00~^:?*[\\") {
		return false
	}
	if strings.Contains(name, "..") || strings.Contains(name, "//") || strings.Contains(name, "@{") {
		return false
	}
	if strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock") {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// gitExtBranchParam 校验分支名字段：field 用于错误文案，返回去掉首尾空白后的名字。
func gitExtBranchParam(field, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ExtInvalidParams("Invalid params: '" + field + "' must not be empty")
	}
	if !gitExtBranchNameValid(name) {
		return "", ExtInvalidParams("Invalid params: '" + field + "' is not a valid git branch name")
	}
	return name, nil
}

// gitExtChangedFile 把 git.ChangedFile 转成协议层结构。
func gitExtChangedFile(f git.ChangedFile) wire.GitExtChangedFile {
	return wire.GitExtChangedFile{
		Path:     f.Path,
		OrigPath: f.OrigPath,
		Status:   f.Status,
	}
}

// gitExtStashEntry 把 git.StashEntry 转成协议层结构，创建时间按 RFC3339 输出。
func gitExtStashEntry(e git.StashEntry) wire.GitExtStashEntry {
	out := wire.GitExtStashEntry{
		Index:   e.Index,
		Ref:     e.Ref,
		Message: e.Message,
		Branch:  e.Branch,
	}
	if !e.CreatedAt.IsZero() {
		out.CreatedAt = e.CreatedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// gitExtPRSummary 把 git.PRSummary 转成协议层结构。
func gitExtPRSummary(p git.PRSummary) wire.GitExtPRSummary {
	out := wire.GitExtPRSummary{
		Number:         p.Number,
		Title:          p.Title,
		State:          p.State,
		IsDraft:        p.IsDraft,
		Author:         p.Author,
		HeadBranch:     p.HeadBranch,
		BaseBranch:     p.BaseBranch,
		URL:            p.URL,
		ReviewDecision: p.ReviewDecision,
		ChecksState:    p.ChecksState,
	}
	if !p.UpdatedAt.IsZero() {
		out.UpdatedAt = p.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// gitExtPRDetail 把 git.PRDetail 转成协议层结构。
func gitExtPRDetail(detail git.PRDetail) wire.GitExtPRDetail {
	out := wire.GitExtPRDetail{
		GitExtPRSummary: gitExtPRSummary(detail.PRSummary),
		Body:            detail.Body,
		Additions:       detail.Additions,
		Deletions:       detail.Deletions,
		ChangedFiles:    detail.ChangedFiles,
		Checks:          make([]wire.GitExtPRCheck, 0, len(detail.Checks)),
		Comments:        make([]wire.GitExtPRComment, 0, len(detail.Comments)),
	}
	for _, check := range detail.Checks {
		out.Checks = append(out.Checks, wire.GitExtPRCheck{Name: check.Name, State: check.State})
	}
	for _, comment := range detail.Comments {
		item := wire.GitExtPRComment{Author: comment.Author, Body: comment.Body}
		if !comment.CreatedAt.IsZero() {
			item.CreatedAt = comment.CreatedAt.UTC().Format(time.RFC3339)
		}
		out.Comments = append(out.Comments, item)
	}
	return out
}

// handleGitExtChangedFiles 实现 git.changedFiles：列出工作区改动文件，供提交时逐个勾选。
func handleGitExtChangedFiles(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.GitExtChangedFilesParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	backend, ok := gitExtBackend[gitExtChangedFilesBackend](d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	files, err := backend.GitChangedFiles(ctx, p.ThreadID)
	if err != nil {
		return nil, err
	}
	res := wire.GitExtChangedFilesResult{
		ThreadID: p.ThreadID,
		Files:    make([]wire.GitExtChangedFile, 0, len(files)),
	}
	for _, file := range files {
		res.Files = append(res.Files, gitExtChangedFile(file))
	}
	return res, nil
}

// handleGitExtCommitSelected 实现 git.commitSelected：只提交 paths 指定的改动。
func handleGitExtCommitSelected(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.GitExtCommitSelectedParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(p.Paths))
	for _, path := range p.Paths {
		if path = strings.TrimSpace(path); path != "" {
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		return nil, ExtInvalidParams("Invalid params: 'paths' must not be empty")
	}
	message := strings.TrimSpace(p.Message)
	if message == "" {
		return nil, ExtInvalidParams("Invalid params: 'message' must not be empty")
	}
	backend, ok := gitExtBackend[gitExtCommitBackend](d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	if err := backend.GitCommitSelected(ctx, p.ThreadID, paths, message); err != nil {
		return nil, err
	}
	res := wire.GitExtCommitSelectedResult{OK: true}
	if !p.PushAfter {
		return res, nil
	}
	// 提交已经成功，推送失败不能把整次调用当成失败，否则前端会以为提交没生效；
	// 用 pushError 把失败原因带回去，文案与普通错误一样做脱敏。
	svc, ok := gitExtServiceFrom(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	pushCtx, cancel := context.WithTimeout(ctx, gitExtNetworkTimeout)
	defer cancel()
	if err := svc.GitPush(pushCtx, p.ThreadID); err != nil {
		res.PushError = RedactSecrets(err.Error())
	}
	return res, nil
}

// handleGitExtStashList 实现 git.stash.list：列出贮藏记录，最新一条在最前。
func handleGitExtStashList(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.GitExtStashListParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	svc, ok := gitExtServiceFrom(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	entries, err := svc.GitStashList(ctx, p.ThreadID)
	if err != nil {
		return nil, err
	}
	res := wire.GitExtStashListResult{
		ThreadID: p.ThreadID,
		Entries:  make([]wire.GitExtStashEntry, 0, len(entries)),
	}
	for _, entry := range entries {
		res.Entries = append(res.Entries, gitExtStashEntry(entry))
	}
	return res, nil
}

// handleGitExtStashPush 实现 git.stash.push：贮藏当前工作区改动，message 可选。
func handleGitExtStashPush(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.GitExtStashPushParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	svc, ok := gitExtServiceFrom(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	if err := svc.GitStashPush(ctx, p.ThreadID, strings.TrimSpace(p.Message)); err != nil {
		return nil, err
	}
	return wire.GitExtStashActionResult{OK: true}, nil
}

// handleGitExtStashPop 实现 git.stash.pop：恢复最近一条贮藏并删除该记录。
func handleGitExtStashPop(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.GitExtStashPopParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	backend, ok := gitExtBackend[gitExtStashPopBackend](d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	if err := backend.GitStashPop(ctx, p.ThreadID); err != nil {
		return nil, err
	}
	return wire.GitExtStashActionResult{OK: true}, nil
}

// handleGitExtStashApply 实现 git.stash.apply：应用指定序号的贮藏，记录保留。
func handleGitExtStashApply(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.GitExtStashIndexParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	if p.Index < 0 {
		return nil, ExtInvalidParams("Invalid params: 'index' must not be negative")
	}
	svc, ok := gitExtServiceFrom(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	if err := svc.GitStashApply(ctx, p.ThreadID, p.Index); err != nil {
		return nil, err
	}
	return wire.GitExtStashActionResult{OK: true}, nil
}

// handleGitExtStashDrop 实现 git.stash.drop：删除指定序号的贮藏记录，不动工作区。
func handleGitExtStashDrop(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.GitExtStashIndexParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	if p.Index < 0 {
		return nil, ExtInvalidParams("Invalid params: 'index' must not be negative")
	}
	svc, ok := gitExtServiceFrom(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	if err := svc.GitStashDrop(ctx, p.ThreadID, p.Index); err != nil {
		return nil, err
	}
	return wire.GitExtStashActionResult{OK: true}, nil
}

// handleGitExtPRList 实现 git.pr.list：列出仓库开放的 PR/MR，limit 缺省 20、上限 100。
func handleGitExtPRList(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.GitExtPRListParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	limit := p.Limit
	if limit <= 0 {
		limit = gitExtPRListDefaultLimit
	}
	if limit > gitExtPRListMaxLimit {
		limit = gitExtPRListMaxLimit
	}
	svc, ok := gitExtServiceFrom(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	prs, err := svc.GitPRList(ctx, p.ThreadID, limit)
	if err != nil {
		return nil, err
	}
	res := wire.GitExtPRListResult{
		ThreadID: p.ThreadID,
		Items:    make([]wire.GitExtPRSummary, 0, len(prs)),
	}
	for _, pr := range prs {
		res.Items = append(res.Items, gitExtPRSummary(pr))
	}
	return res, nil
}

// handleGitExtPRDetail 实现 git.pr.detail：读取单条 PR/MR 的详情。
func handleGitExtPRDetail(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.GitExtPRDetailParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	if p.Number <= 0 {
		return nil, ExtInvalidParams("Invalid params: 'number' must be positive")
	}
	svc, ok := gitExtServiceFrom(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	detail, err := svc.GitPRDetail(ctx, p.ThreadID, p.Number)
	if err != nil {
		return nil, err
	}
	return gitExtPRDetail(detail), nil
}

// handleGitExtPush 实现 git.push：推送当前分支到远程（网络操作，套超时）。
func handleGitExtPush(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.GitExtPushParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	svc, ok := gitExtServiceFrom(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	pushCtx, cancel := context.WithTimeout(ctx, gitExtNetworkTimeout)
	defer cancel()
	if err := svc.GitPush(pushCtx, p.ThreadID); err != nil {
		return nil, err
	}
	return wire.GitExtWriteActionResult{OK: true}, nil
}

// handleGitExtPull 实现 git.pull：快进拉取当前分支的上游（有冲突时不改写工作区，由 git 层报错）。
func handleGitExtPull(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.GitExtPullParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	svc, ok := gitExtServiceFrom(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	pullCtx, cancel := context.WithTimeout(ctx, gitExtNetworkTimeout)
	defer cancel()
	if err := svc.GitPull(pullCtx, p.ThreadID); err != nil {
		return nil, err
	}
	return wire.GitExtWriteActionResult{OK: true}, nil
}

// handleGitExtFetch 实现 git.fetch：抓取远程分支，不改动工作区。
func handleGitExtFetch(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.GitExtFetchParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	svc, ok := gitExtServiceFrom(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	fetchCtx, cancel := context.WithTimeout(ctx, gitExtNetworkTimeout)
	defer cancel()
	if err := svc.GitFetch(fetchCtx, p.ThreadID); err != nil {
		return nil, err
	}
	return wire.GitExtWriteActionResult{OK: true}, nil
}

// handleGitExtCheckout 实现 git.checkout：切换到本地已有分支，不猜远端分支。
func handleGitExtCheckout(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.GitExtCheckoutParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	branch, err := gitExtBranchParam("branch", p.Branch)
	if err != nil {
		return nil, err
	}
	svc, ok := gitExtServiceFrom(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	checkoutCtx, cancel := context.WithTimeout(ctx, gitExtLocalTimeout)
	defer cancel()
	if err := svc.GitCheckout(checkoutCtx, p.ThreadID, branch); err != nil {
		return nil, err
	}
	return wire.GitExtWriteActionResult{OK: true}, nil
}

// handleGitExtCreateBranch 实现 git.createBranch：从当前分支新建并切换过去。
func handleGitExtCreateBranch(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.GitExtCreateBranchParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	name, err := gitExtBranchParam("name", p.Name)
	if err != nil {
		return nil, err
	}
	svc, ok := gitExtServiceFrom(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	createCtx, cancel := context.WithTimeout(ctx, gitExtLocalTimeout)
	defer cancel()
	if err := svc.GitCreateBranch(createCtx, p.ThreadID, name); err != nil {
		return nil, err
	}
	return wire.GitExtWriteActionResult{OK: true}, nil
}

// handleGitExtMerge 实现 git.merge：把 branch 合并进当前分支（与桌面端「合并」一致）。
func handleGitExtMerge(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.GitExtMergeParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	branch, err := gitExtBranchParam("branch", p.Branch)
	if err != nil {
		return nil, err
	}
	svc, ok := gitExtServiceFrom(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	mergeCtx, cancel := context.WithTimeout(ctx, gitExtLocalTimeout)
	defer cancel()
	if err := svc.GitMergeFrom(mergeCtx, p.ThreadID, branch); err != nil {
		return nil, err
	}
	return wire.GitExtWriteActionResult{OK: true}, nil
}

// handleGitExtGenerateCommitMessage 实现 git.generateCommitMessage：为未提交改动生成提交信息。
// 生成走模型调用，耗时不定，按网络操作的超时处理；未开启该功能或没有改动时保留客户端原文案。
func handleGitExtGenerateCommitMessage(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.GitExtGenerateCommitMessageParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	svc, ok := gitExtServiceFrom(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	genCtx, cancel := context.WithTimeout(ctx, gitExtNetworkTimeout)
	defer cancel()
	message, err := svc.GitGenerateCommitMessage(genCtx, p.ThreadID)
	if err != nil {
		return nil, err
	}
	return wire.GitExtGenerateCommitMessageResult{Message: strings.TrimSpace(message)}, nil
}

// handleGitExtPRCreateURL 实现 git.pr.createUrl：按会话所在仓库的远端地址与当前分支
// 生成创建 PR/MR 的网页地址，复用桌面端「创建合并请求」的规则；没有远端或识别不到
// 托管平台时返回错误，由前端提示用户。
func handleGitExtPRCreateURL(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.GitExtPRCreateURLParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if err := gitExtThreadIDParam(p.ThreadID); err != nil {
		return nil, err
	}
	svc, ok := gitExtServiceFrom(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	urlCtx, cancel := context.WithTimeout(ctx, gitExtLocalTimeout)
	defer cancel()
	target, title, body, err := svc.GitPullRequestCreateURL(urlCtx, p.ThreadID)
	if err != nil {
		return nil, err
	}
	if target == "" {
		return nil, ExtInvalidParams("Invalid params: cannot build a pull request URL, the repository has no recognizable remote")
	}
	return wire.GitExtPRCreateURLResult{URL: target, Title: title, Body: body}, nil
}
