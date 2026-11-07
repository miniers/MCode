package wire

// files.write 的远程扩展类型，由 internal/remote/files_write_ext_methods.go 注册。
// 只允许覆盖工作区内已存在的文本文件：内容上限与 files.read 的文本分块上限一致（512 KiB），
// 二进制、目录与不存在的路径一律拒绝。符号链接按解析后的真实路径判断是否落在工作区内。

// FilesWriteExtParams 对应 files.write 请求参数。
type FilesWriteExtParams struct {
	ThreadID string `json:"threadID"`
	// Path 是相对工作区的路径，不接受绝对路径、~ 与越界路径。
	Path string `json:"path"`
	// Content 是完整的新内容，按 UTF-8 文本处理并整体覆盖原文件。
	Content string `json:"content"`
	// ExpectedHash 可选：非空时先与磁盘当前内容比对，
	// 不一致返回 -32009（版本冲突），避免覆盖别人刚写过的改动。
	// 取值来自 files.read 结果里的 hash 字段（整文件 SHA-256 小写十六进制）。
	ExpectedHash string `json:"expectedHash,omitempty"`
}

// FilesWriteExtResult 对应 files.write 成功返回。
// Hash 是写入后整文件内容的 SHA-256 小写十六进制，可直接作为下一次写入的 expectedHash；
// Size 是写入的字节数。
type FilesWriteExtResult struct {
	Hash string `json:"hash"`
	Size int64  `json:"size"`
}
