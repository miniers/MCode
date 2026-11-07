package remote

import (
	"context"
	"encoding/json"
	"strings"

	"mcode/internal/remote/wire"
)

// files.create / files.rename / files.trash / files.duplicate：
// 桌面端文件树的写操作入口，实现在 internal/remote/files_ops.go。
// 参数校验与 files.write 保持一致：threadID 必须是 UUID、路径非空且不超过 4096 字符，
// 未知字段直接拒绝；路径本身的安全校验（.git、受保护文件、越界）由后端方法完成。

const (
	// filesCreateMethod 是新建文件或目录的方法名。
	filesCreateMethod = "files.create"
	// filesRenameMethod 是重命名/移动的方法名。
	filesRenameMethod = "files.rename"
	// filesTrashMethod 是移到废纸篓的方法名。
	filesTrashMethod = "files.trash"
	// filesDuplicateMethod 是复制文件的方法名。
	filesDuplicateMethod = "files.duplicate"
)

func init() {
	RegisterMethod(filesCreateMethod, handleFilesCreate)
	RegisterMethod(filesRenameMethod, handleFilesRename)
	RegisterMethod(filesTrashMethod, handleFilesTrash)
	RegisterMethod(filesDuplicateMethod, handleFilesDuplicate)
}

// handleFilesCreate 严格校验参数后交给 MemoryBackend.FilesCreate。
func handleFilesCreate(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.FilesCreateParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if !isValidUUID(p.ThreadID) {
		return nil, ExtInvalidParams("Invalid params: 'threadID' must be a valid UUID")
	}
	path := strings.TrimSpace(p.Path)
	if path == "" || len(p.Path) > filesOpsMaxPathLength {
		return nil, ExtInvalidParams("Invalid params: 'path' is required and must be at most 4096 characters")
	}

	mb, ok := d.memoryBackend()
	if !ok {
		return nil, &ExtError{Code: wire.ErrCodeOperationRejected, Message: "workspace file operations unavailable"}
	}
	p.Path = path
	res, err := mb.FilesCreate(ctx, p)
	if err != nil {
		return nil, filesWriteExtError(err)
	}
	return res, nil
}

// handleFilesRename 严格校验参数后交给 MemoryBackend.FilesRename。
func handleFilesRename(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.FilesRenameParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if !isValidUUID(p.ThreadID) {
		return nil, ExtInvalidParams("Invalid params: 'threadID' must be a valid UUID")
	}
	path := strings.TrimSpace(p.Path)
	if path == "" || len(p.Path) > filesOpsMaxPathLength {
		return nil, ExtInvalidParams("Invalid params: 'path' is required and must be at most 4096 characters")
	}
	newPath := strings.TrimSpace(p.NewPath)
	if newPath == "" || len(p.NewPath) > filesOpsMaxPathLength {
		return nil, ExtInvalidParams("Invalid params: 'newPath' is required and must be at most 4096 characters")
	}

	mb, ok := d.memoryBackend()
	if !ok {
		return nil, &ExtError{Code: wire.ErrCodeOperationRejected, Message: "workspace file operations unavailable"}
	}
	p.Path = path
	p.NewPath = newPath
	res, err := mb.FilesRename(ctx, p)
	if err != nil {
		return nil, filesWriteExtError(err)
	}
	return res, nil
}

// handleFilesTrash 严格校验参数后交给 MemoryBackend.FilesTrash。
func handleFilesTrash(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.FilesTrashParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if !isValidUUID(p.ThreadID) {
		return nil, ExtInvalidParams("Invalid params: 'threadID' must be a valid UUID")
	}
	path := strings.TrimSpace(p.Path)
	if path == "" || len(p.Path) > filesOpsMaxPathLength {
		return nil, ExtInvalidParams("Invalid params: 'path' is required and must be at most 4096 characters")
	}

	mb, ok := d.memoryBackend()
	if !ok {
		return nil, &ExtError{Code: wire.ErrCodeOperationRejected, Message: "workspace file operations unavailable"}
	}
	p.Path = path
	res, err := mb.FilesTrash(ctx, p)
	if err != nil {
		return nil, filesWriteExtError(err)
	}
	return res, nil
}

// handleFilesDuplicate 严格校验参数后交给 MemoryBackend.FilesDuplicate。
func handleFilesDuplicate(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.FilesDuplicateParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if !isValidUUID(p.ThreadID) {
		return nil, ExtInvalidParams("Invalid params: 'threadID' must be a valid UUID")
	}
	path := strings.TrimSpace(p.Path)
	if path == "" || len(p.Path) > filesOpsMaxPathLength {
		return nil, ExtInvalidParams("Invalid params: 'path' is required and must be at most 4096 characters")
	}

	mb, ok := d.memoryBackend()
	if !ok {
		return nil, &ExtError{Code: wire.ErrCodeOperationRejected, Message: "workspace file operations unavailable"}
	}
	p.Path = path
	res, err := mb.FilesDuplicate(ctx, p)
	if err != nil {
		return nil, filesWriteExtError(err)
	}
	return res, nil
}
