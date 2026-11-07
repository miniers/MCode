package wire

// files.create / files.rename / files.trash / files.duplicate 的远程扩展类型，
// 由 internal/remote/files_ops_ext_methods.go 注册，实现在 internal/remote/files_ops.go。
// 所有路径都是相对工作区根目录的路径，不接受绝对路径、~ 与越界路径；
// 返回的 Path 同样相对工作区根目录，并统一使用正斜杠。

// FilesCreateParams 对应 files.create 请求参数。
type FilesCreateParams struct {
	ThreadID string `json:"threadID"`
	// Path 是待创建的相对路径，父目录必须已经存在。
	Path string `json:"path"`
	// Dir 为 true 时创建目录，否则创建空文件。
	Dir bool `json:"dir,omitempty"`
}

// FilesCreateResult 对应 files.create 成功返回。
// Path 是新建条目的相对路径（正斜杠）。
type FilesCreateResult struct {
	Path string `json:"path"`
}

// FilesRenameParams 对应 files.rename 请求参数。
type FilesRenameParams struct {
	ThreadID string `json:"threadID"`
	// Path 是已存在的源路径。
	Path string `json:"path"`
	// NewPath 是目标路径，不能已存在。
	NewPath string `json:"newPath"`
}

// FilesRenameResult 对应 files.rename 成功返回。
// Path 是重命名后的相对路径（正斜杠）。
type FilesRenameResult struct {
	Path string `json:"path"`
}

// FilesTrashParams 对应 files.trash 请求参数。
type FilesTrashParams struct {
	ThreadID string `json:"threadID"`
	// Path 是要移到废纸篓的相对路径。
	Path string `json:"path"`
}

// FilesTrashResult 对应 files.trash 成功返回，没有额外字段。
type FilesTrashResult struct{}

// FilesDuplicateParams 对应 files.duplicate 请求参数。
type FilesDuplicateParams struct {
	ThreadID string `json:"threadID"`
	// Path 是要复制的普通文件路径。
	Path string `json:"path"`
}

// FilesDuplicateResult 对应 files.duplicate 成功返回。
// Path 是副本的相对路径（正斜杠），形如 "a copy.txt" 或 "a copy 2.txt"。
type FilesDuplicateResult struct {
	Path string `json:"path"`
}
