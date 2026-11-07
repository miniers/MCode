package wire

// --- 远程添加项目（扩展方法 projects.add / projects.validatePath）---
//
// 远程客户端给 Mac 端添加项目目录时，参数路径与桌面端选择目录得到的是同一种输入：
// Mac 本机上的目录路径。校验通过后走 MemoryBackend.AddProjectPath，与桌面端
// internal/ui/model.go:1654 的 client.AddProject 落到同一条写入路径。

// ProjectExtAddParams 对应 projects.add 请求参数。
// Path 必须是 Mac 上的绝对路径，允许以 ~/ 开头（服务端展开为当前用户主目录）。
type ProjectExtAddParams struct {
	Path string `json:"path"`
}

// ProjectExtAddResult 对应 projects.add 成功结果；重复添加同一路径时返回已有项目。
type ProjectExtAddResult struct {
	Project ProjectSummary `json:"project"`
}

// ProjectExtValidateParams 对应 projects.validatePath 请求参数。
type ProjectExtValidateParams struct {
	Path string `json:"path"`
}

// ProjectExtValidateResult 对应 projects.validatePath 成功结果。
// OK 为真时 NormalizedPath 是展开 ~ 并 Clean 后的绝对路径；OK 为假时 Reason 是中文原因。
// 该方法只做校验，不写任何数据。
type ProjectExtValidateResult struct {
	OK             bool   `json:"ok"`
	NormalizedPath string `json:"normalizedPath"`
	Reason         string `json:"reason"`
}

// ProjectExtChangedParams 是 projects.changed 通知的参数，携带新增或复用的项目 ID。
type ProjectExtChangedParams struct {
	ProjectID string `json:"projectID"`
}

// --- 远程克隆项目（扩展方法 projects.clone）---
//
// 与桌面端「从 GitHub 克隆…」（internal/ui/github_clone.go）同一条流程：
// 在宿主上 git clone 到父目录下的仓库名子目录，成功后按 projects.add 同一路径登记项目。

// ProjectExtCloneParams 对应 projects.clone 请求参数。
// URL 只接受 https:// 与 git@ 形式的 Git 地址；ParentPath 缺省为用户主目录；
// Name 缺省为从 URL 解析出的仓库名。
type ProjectExtCloneParams struct {
	URL        string  `json:"url"`
	ParentPath *string `json:"parentPath,omitempty"`
	Name       *string `json:"name,omitempty"`
}

// ProjectExtCloneResult 与 projects.add 结果同型：克隆成功后返回登记好的项目摘要。
type ProjectExtCloneResult = ProjectExtAddResult

// --- 远程浏览目录（扩展方法 projects.listDir）---

// ProjectExtListDirParams 对应 projects.listDir 请求参数。
// Path 缺省为用户主目录，且必须位于用户主目录之内；ShowHidden 为真时列出以点开头的目录。
type ProjectExtListDirParams struct {
	Path       *string `json:"path,omitempty"`
	ShowHidden bool    `json:"showHidden,omitempty"`
}

// ProjectExtDirEntry 是 projects.listDir 返回的单个子目录。
type ProjectExtDirEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	IsGitRepo bool   `json:"isGitRepo"`
}

// ProjectExtListDirResult 对应 projects.listDir 成功结果。
// Path 是解析符号链接后的当前目录；Parent 在父目录仍位于用户主目录内时给出，否则省略。
type ProjectExtListDirResult struct {
	Path      string               `json:"path"`
	Parent    *string              `json:"parent,omitempty"`
	Entries   []ProjectExtDirEntry `json:"entries"`
	Truncated bool                 `json:"truncated"`
}
