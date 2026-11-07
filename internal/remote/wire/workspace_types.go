package wire

// RemoteFileEntryKind 表示工作区文件条目的类型（目录、普通文件、符号链接）。
type RemoteFileEntryKind = string

const (
	RemoteFileEntryKindDirectory RemoteFileEntryKind = "directory"
	RemoteFileEntryKindFile      RemoteFileEntryKind = "file"
	RemoteFileEntryKindSymlink   RemoteFileEntryKind = "symlink"
)

// RemoteFileEntry 表示工作区中的单个文件或目录条目。
type RemoteFileEntry struct {
	Name      string              `json:"name"`
	Path      string              `json:"path"`
	Kind      RemoteFileEntryKind `json:"kind"`
	Size      int64               `json:"size"`
	Ignored   bool                `json:"ignored"`
	Protected bool                `json:"protected"`
}

// RemoteFileMatch 别名 RemoteFileEntry，用于 files.search 匹配项契约。
type RemoteFileMatch = RemoteFileEntry

// RemoteFilesListParams 对应 files.list 请求参数。
type RemoteFilesListParams struct {
	ThreadID string `json:"threadID"`
	Subpath  string `json:"subpath,omitempty"`
}

// RemoteFilesListResult 对应 files.list 成功返回。
type RemoteFilesListResult struct {
	Path      string            `json:"path"`
	Entries   []RemoteFileEntry `json:"entries"`
	Truncated bool              `json:"truncated"`
}

// RemoteFileContentKind 表示文件内容类型（text、image、binary）。
type RemoteFileContentKind = string

const (
	RemoteFileContentKindText   RemoteFileContentKind = "text"
	RemoteFileContentKindImage  RemoteFileContentKind = "image"
	RemoteFileContentKindBinary RemoteFileContentKind = "binary"
)

// RemoteFilesReadParams 对应 files.read 请求参数。
type RemoteFilesReadParams struct {
	ThreadID string `json:"threadID"`
	Path     string `json:"path"`
	Offset   *int64 `json:"offset,omitempty"`
	Length   *int   `json:"length,omitempty"`
}

// RemoteFilesReadResult 对应 files.read 成功返回。
// Hash 是整文件内容的 SHA-256 小写十六进制，仅在本次结果覆盖了整个文件时返回，
// 可直接作为 files.write 的 expectedHash。
type RemoteFilesReadResult struct {
	Path       string                `json:"path"`
	Kind       RemoteFileContentKind `json:"kind"`
	Text       *string               `json:"text,omitempty"`
	Base64     *string               `json:"base64,omitempty"`
	MimeType   *string               `json:"mimeType,omitempty"`
	Hash       string                `json:"hash,omitempty"`
	TotalBytes int64                 `json:"totalBytes"`
	Offset     int64                 `json:"offset"`
	NextOffset *int64                `json:"nextOffset,omitempty"`
	Truncated  bool                  `json:"truncated"`
}

// RemoteFilesSearchParams 对应 files.search 请求参数。
type RemoteFilesSearchParams struct {
	ThreadID string `json:"threadID"`
	Query    string `json:"query"`
}

// RemoteFilesSearchResult 对应 files.search 成功返回。
type RemoteFilesSearchResult struct {
	Entries   []RemoteFileEntry `json:"entries"`
	Truncated bool              `json:"truncated"`
}

// RemoteGitStatusParams 对应 git.status 请求参数。
type RemoteGitStatusParams struct {
	ThreadID string `json:"threadID"`
}

// RemoteGitStatusResult 对应 git.status 成功返回。
type RemoteGitStatusResult struct {
	ThreadID       string   `json:"threadID"`
	IsRepository   bool     `json:"isRepository"`
	Branch         *string  `json:"branch,omitempty"`
	Upstream       *string  `json:"upstream,omitempty"`
	DefaultBranch  *string  `json:"defaultBranch,omitempty"`
	Ahead          int      `json:"ahead"`
	Behind         int      `json:"behind"`
	ModifiedFiles  []string `json:"modifiedFiles"`
	UntrackedFiles []string `json:"untrackedFiles"`
	IsClean        bool     `json:"isClean"`
	Truncated      *bool    `json:"truncated,omitempty"`
}

// RemoteGitBranchesParams 对应 git.branches 请求参数。
type RemoteGitBranchesParams struct {
	ThreadID string `json:"threadID"`
}

// RemoteGitBranchesResult 对应 git.branches 成功返回。
type RemoteGitBranchesResult struct {
	ThreadID     string   `json:"threadID"`
	IsRepository bool     `json:"isRepository"`
	Current      *string  `json:"current,omitempty"`
	Local        []string `json:"local"`
	Remote       []string `json:"remote"`
	Truncated    *bool    `json:"truncated,omitempty"`
}

// ScriptsListParams 对应 scripts.list 请求参数。
type ScriptsListParams struct {
	ThreadID string `json:"threadID"`
}

// ScriptsPackageScript 对应 package.json 中声明的单个脚本。
type ScriptsPackageScript struct {
	Name       string `json:"name"`
	Command    string `json:"command"`
	IsFavorite bool   `json:"isFavorite"`
}

// ScriptsProjectScript 对应项目自定义工程脚本。
// Command 与 RunOnWorktreeCreate 是 scripts.save 与桌面端编辑弹窗需要的字段，
// 老客户端忽略它们即可；Symbol 保持原样（无图标时为 null）。
type ScriptsProjectScript struct {
	ID                  string  `json:"id"`
	Name                string  `json:"name"`
	Symbol              *string `json:"symbol"`
	Command             string  `json:"command,omitempty"`
	RunOnWorktreeCreate bool    `json:"runOnWorktreeCreate,omitempty"`
}

// ScriptsListResult 对应 scripts.list 成功返回。
// Revision 是项目脚本列表内容的乐观锁版本号，可直接作为 scripts.save 的 expectedRevision。
type ScriptsListResult struct {
	Directory      string                 `json:"directory"`
	Runner         string                 `json:"runner"`
	PackageScripts []ScriptsPackageScript `json:"packageScripts"`
	ProjectScripts []ScriptsProjectScript `json:"projectScripts"`
	Revision       string                 `json:"revision,omitempty"`
}

// ScriptsSaveParams 对应 scripts.save 请求参数：整表替换某个项目的项目级脚本。
// ExpectedRevision 可选，给出时与当前 revision 不一致则返回版本冲突（-32009）。
type ScriptsSaveParams struct {
	ProjectID        string              `json:"projectID"`
	Scripts          []ScriptsSaveScript `json:"scripts"`
	ExpectedRevision *string             `json:"expectedRevision,omitempty"`
}

// ScriptsSaveScript 是 scripts.save 里的单个脚本，字段与桌面端脚本编辑弹窗一致。
type ScriptsSaveScript struct {
	ID                  string  `json:"id,omitempty"`
	Name                string  `json:"name"`
	Command             string  `json:"command"`
	Symbol              *string `json:"symbol,omitempty"`
	RunOnWorktreeCreate bool    `json:"runOnWorktreeCreate,omitempty"`
}

// ScriptsSaveResult 与 scripts.list 同型：返回保存后的完整脚本列表与新的 revision。
type ScriptsSaveResult = ScriptsListResult

// ScriptsRunParams 对应 scripts.run 请求参数。
type ScriptsRunParams struct {
	ThreadID string  `json:"threadID"`
	Kind     string  `json:"kind"`
	Name     *string `json:"name,omitempty"`
	ScriptID *string `json:"scriptID,omitempty"`
}

// ScriptsRunResult 对应 scripts.run 成功返回；RunID 供 scripts.runStatus 查询运行结果。
type ScriptsRunResult struct {
	OK    bool   `json:"ok"`
	RunID string `json:"runID"`
}

// ScriptsRunStatusParams 对应 scripts.runStatus 请求参数。
type ScriptsRunStatusParams struct {
	ThreadID string `json:"threadID"`
	RunID    string `json:"runID"`
}

// ScriptsRunStatusResult 对应 scripts.runStatus 成功返回。
// status 为 running 时没有 exitCode，output 为空串；finished 时 exitCode 为进程退出码，
// rejected 表示等待并发额度超时，output 为拒绝原因。
type ScriptsRunStatusResult struct {
	RunID    string `json:"runID"`
	Label    string `json:"label"`
	Status   string `json:"status"`
	ExitCode *int   `json:"exitCode,omitempty"`
	Output   string `json:"output"`
}

// ScriptsSetFavoriteParams 对应 scripts.setFavorite 请求参数。
type ScriptsSetFavoriteParams struct {
	ThreadID   string `json:"threadID"`
	Name       string `json:"name"`
	IsFavorite bool   `json:"isFavorite"`
}

// ScriptsSetFavoriteResult 对应 scripts.setFavorite 成功返回。
type ScriptsSetFavoriteResult struct {
	Favorites []string `json:"favorites"`
}

// RemoteComposerQuotaParams 对应 composer.quota 请求参数（threadID 与 projectID 二选一）。
type RemoteComposerQuotaParams struct {
	ThreadID  string `json:"threadID,omitempty"`
	ProjectID string `json:"projectID,omitempty"`
}

// RemoteQuotaWindow 对应额度窗口周期与用量百分比。
// 除 id/period/title/usedPercent/resetsAt 以外的字段都是可选的（omitempty）：
// 数据源没给就不出现，老客户端忽略即可。
// UsedPercent 是浮点数以保留小数；整数值在 JSON 里仍写成 42 这样的整数形式。
type RemoteQuotaWindow struct {
	ID          *string `json:"id,omitempty"`
	Period      *string `json:"period"`
	Title       string  `json:"title"`
	UsedPercent float64 `json:"usedPercent"`
	ResetsAt    *string `json:"resetsAt"`
	// Label 与 Title 同义（窗口展示名），供按桌面端字段名取用的客户端使用。
	Label string `json:"label,omitempty"`
	// Account 是该窗口所属账号，与 budget.account 一致；为空表示不分账号。
	Account string `json:"account,omitempty"`
	// Active 表示这是当前在用的账号（与 budget.active 同义）。
	Active bool `json:"active,omitempty"`
	// Disabled 表示账号已停用（路由覆盖关了它），界面显示「已停止」。
	Disabled bool `json:"disabled,omitempty"`
	// Source 是额度来源：magpie 表示 Magpie 网关缓存，空表示直连本机登录。
	Source string `json:"source,omitempty"`
	// Plan 是该窗口所属账号的套餐，为空时不展示。
	Plan string `json:"plan,omitempty"`
	// Display 是数据源给的展示文字，例如 $12.30 / $40。
	Display string `json:"display,omitempty"`
	// Text 为 true 表示只有文字、没有占比，UsedPercent 不可用。
	Text bool `json:"text,omitempty"`
	// Unlimited 为 true 表示不限量。
	Unlimited bool `json:"unlimited,omitempty"`
	// Balance 是剩余数量（如钱包余额/点数），nil 表示未提供。
	Balance *float64 `json:"balance,omitempty"`
	// Unit 是额度单位（如 "credits"）。
	Unit string `json:"unit,omitempty"`
	// ReadAt 是数据在来源端的读取时间，缺省表示未知。
	ReadAt *string `json:"readAt,omitempty"`
}

// RemoteQuotaBudget 对应模型供应商预算与配额详情。
type RemoteQuotaBudget struct {
	Provider     string              `json:"provider"`
	ProviderName string              `json:"providerName"`
	Role         string              `json:"role"`
	Profile      *string             `json:"profile"`
	State        string              `json:"state"`
	Windows      []RemoteQuotaWindow `json:"windows"`
	Amount       *string             `json:"amount"`
	ReadAt       *string             `json:"readAt"`
	Account      *string             `json:"account,omitempty"`
	Active       *bool               `json:"active,omitempty"`
}

// RemoteComposerQuotaResult 对应 composer.quota 成功返回。
type RemoteComposerQuotaResult struct {
	Budgets []RemoteQuotaBudget `json:"budgets"`
}
