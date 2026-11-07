package wire

// 分文件提交、贮藏、PR/MR 查看与 Git 写操作的远程扩展方法参数与结果类型。
// 这些方法由 internal/remote/git_ext_methods.go 注册，字段与
// docs/remote-control.schema.json 中的 RemoteGit* 定义一一对应，按 schema 的兼容规则演进。

// GitExtChangedFilesParams 对应 git.changedFiles 请求参数。
type GitExtChangedFilesParams struct {
	ThreadID string `json:"threadID"`
}

// GitExtChangedFile 是工作区里的单个改动文件，字段与 git.ChangedFile 一致。
type GitExtChangedFile struct {
	Path     string `json:"path"`
	OrigPath string `json:"origPath,omitempty"`
	Status   string `json:"status"`
}

// GitExtChangedFilesResult 对应 git.changedFiles 成功返回。
type GitExtChangedFilesResult struct {
	ThreadID string              `json:"threadID"`
	Files    []GitExtChangedFile `json:"files"`
}

// GitExtCommitSelectedParams 对应 git.commitSelected 请求参数。
// Paths 不能为空，Message 去掉首尾空白后不能为空。
// PushAfter 为真时提交成功后继续推送当前分支。
type GitExtCommitSelectedParams struct {
	ThreadID  string   `json:"threadID"`
	Paths     []string `json:"paths"`
	Message   string   `json:"message"`
	PushAfter bool     `json:"pushAfter,omitempty"`
}

// GitExtCommitSelectedResult 对应 git.commitSelected 成功返回。
// OK 恒为 true，表示提交已成功；PushError 非空表示提交成功但随后的推送失败，
// 其文案已脱敏，可直接展示给用户。
type GitExtCommitSelectedResult struct {
	OK        bool   `json:"ok"`
	PushError string `json:"pushError,omitempty"`
}

// GitExtStashListParams 对应 git.stash.list 请求参数。
type GitExtStashListParams struct {
	ThreadID string `json:"threadID"`
}

// GitExtStashEntry 是一条贮藏记录，字段与 git.StashEntry 一致。
type GitExtStashEntry struct {
	Index     int    `json:"index"`
	Ref       string `json:"ref"`
	Message   string `json:"message"`
	Branch    string `json:"branch,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"` // RFC3339；时间解析不到时为空
}

// GitExtStashListResult 对应 git.stash.list 成功返回，最新一条在最前。
type GitExtStashListResult struct {
	ThreadID string             `json:"threadID"`
	Entries  []GitExtStashEntry `json:"entries"`
}

// GitExtStashPushParams 对应 git.stash.push 请求参数，Message 可选。
type GitExtStashPushParams struct {
	ThreadID string `json:"threadID"`
	Message  string `json:"message,omitempty"`
}

// GitExtStashPopParams 对应 git.stash.pop 请求参数。
type GitExtStashPopParams struct {
	ThreadID string `json:"threadID"`
}

// GitExtStashIndexParams 对应 git.stash.apply 与 git.stash.drop 请求参数。
type GitExtStashIndexParams struct {
	ThreadID string `json:"threadID"`
	Index    int    `json:"index"`
}

// GitExtStashActionResult 对应贮藏写操作（push / pop / apply / drop）成功返回。
type GitExtStashActionResult struct {
	OK bool `json:"ok"`
}

// GitExtPRListParams 对应 git.pr.list 请求参数，Limit 缺省为 20。
type GitExtPRListParams struct {
	ThreadID string `json:"threadID"`
	Limit    int    `json:"limit,omitempty"`
}

// GitExtPRSummary 是 PR/MR 摘要，字段与 git.PRSummary 一致。
type GitExtPRSummary struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	State          string `json:"state"`
	IsDraft        bool   `json:"isDraft"`
	Author         string `json:"author"`
	HeadBranch     string `json:"headBranch"`
	BaseBranch     string `json:"baseBranch"`
	URL            string `json:"url,omitempty"`
	UpdatedAt      string `json:"updatedAt,omitempty"`      // RFC3339；解析不到时为空
	ReviewDecision string `json:"reviewDecision,omitempty"` // APPROVED / CHANGES_REQUESTED / REVIEW_REQUIRED
	ChecksState    string `json:"checksState,omitempty"`    // success / failure / pending / none
}

// GitExtPRListResult 对应 git.pr.list 成功返回。
type GitExtPRListResult struct {
	ThreadID string            `json:"threadID"`
	Items    []GitExtPRSummary `json:"items"`
}

// GitExtPRDetailParams 对应 git.pr.detail 请求参数。
type GitExtPRDetailParams struct {
	ThreadID string `json:"threadID"`
	Number   int    `json:"number"`
}

// GitExtPRCheck 是一条 CI 检查及其汇总后的状态。
type GitExtPRCheck struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

// GitExtPRComment 是 PR/MR 下的一条评论。
type GitExtPRComment struct {
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt,omitempty"` // RFC3339；解析不到时为空
}

// GitExtPRDetail 对应 git.pr.detail 成功返回：摘要字段内联展开，另加正文、行数、检查与评论。
type GitExtPRDetail struct {
	GitExtPRSummary
	Body         string            `json:"body"`
	Additions    int               `json:"additions"`
	Deletions    int               `json:"deletions"`
	ChangedFiles int               `json:"changedFiles"`
	Checks       []GitExtPRCheck   `json:"checks"`
	Comments     []GitExtPRComment `json:"comments"`
}

// GitExtPushParams 对应 git.push 请求参数：推送会话所在仓库的当前分支。
type GitExtPushParams struct {
	ThreadID string `json:"threadID"`
}

// GitExtPullParams 对应 git.pull 请求参数：快进拉取当前分支的上游。
type GitExtPullParams struct {
	ThreadID string `json:"threadID"`
}

// GitExtFetchParams 对应 git.fetch 请求参数：抓取远程分支。
type GitExtFetchParams struct {
	ThreadID string `json:"threadID"`
}

// GitExtCheckoutParams 对应 git.checkout 请求参数，Branch 必须是合法的分支名。
type GitExtCheckoutParams struct {
	ThreadID string `json:"threadID"`
	Branch   string `json:"branch"`
}

// GitExtCreateBranchParams 对应 git.createBranch 请求参数，Name 必须是合法的分支名。
type GitExtCreateBranchParams struct {
	ThreadID string `json:"threadID"`
	Name     string `json:"name"`
}

// GitExtMergeParams 对应 git.merge 请求参数：把 Branch 合并进当前分支。
type GitExtMergeParams struct {
	ThreadID string `json:"threadID"`
	Branch   string `json:"branch"`
}

// GitExtGenerateCommitMessageParams 对应 git.generateCommitMessage 请求参数。
type GitExtGenerateCommitMessageParams struct {
	ThreadID string `json:"threadID"`
}

// GitExtWriteActionResult 对应 git.push / git.pull / git.fetch / git.checkout /
// git.createBranch / git.merge 成功返回。
type GitExtWriteActionResult struct {
	OK bool `json:"ok"`
}

// GitExtGenerateCommitMessageResult 对应 git.generateCommitMessage 成功返回。
type GitExtGenerateCommitMessageResult struct {
	Message string `json:"message"`
}

// GitExtPRCreateURLParams 对应 git.pr.createUrl 请求参数。
type GitExtPRCreateURLParams struct {
	ThreadID string `json:"threadID"`
}

// GitExtPRCreateURLResult 对应 git.pr.createUrl 成功返回：URL 是创建 PR/MR 的网页地址，
// Title / Body 是宿主预填的表单字段，宿主没有预填内容时为空。
type GitExtPRCreateURLResult struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
}
