package mcodeui

import "context"

// FileOpsClient 是工作区文件写操作的可选客户端能力：新建、重命名/移动、
// 移到废纸篓、复制。与 AttentionClient、StewardClient 一样按类型断言取用，
// 不往 Client 接口里加方法，旧客户端和测试替身可以不实现。
type FileOpsClient interface {
	// FilesCreate 在指定会话工作区内新建空文件（dir 为 false）或目录，返回相对工作区的路径。
	FilesCreate(ctx context.Context, threadID, path string, dir bool) (string, error)
	// FilesRename 重命名或移动工作区内的条目，返回新的相对路径。
	FilesRename(ctx context.Context, threadID, path, newPath string) (string, error)
	// FilesTrash 把工作区内的条目移到系统废纸篓。
	FilesTrash(ctx context.Context, threadID, path string) error
	// FilesDuplicate 在同一目录下复制普通文件，返回副本的相对路径。
	FilesDuplicate(ctx context.Context, threadID, path string) (string, error)
}

// fileOpsClient 返回实现了文件操作接口的客户端；旧客户端或测试替身返回 false。
func fileOpsClient(s *chatState) (FileOpsClient, bool) {
	if s == nil || s.client == nil {
		return nil, false
	}
	c, ok := s.client.(FileOpsClient)
	return c, ok
}
