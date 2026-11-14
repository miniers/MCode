package remote

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"mcode/internal/fsutil"
	"mcode/internal/remote/wire"
)

// 工作区文件操作：新建、重命名/移动、移到废纸篓、复制。
// 与 files.read / files.write 共用同一套工作区定位与路径校验：
//   - threadWorkspaceRoot 定位会话工作区；
//   - resolveWorkspacePath 解析已存在的路径，拒绝绝对路径、~、..、越界与指向工作区外的符号链接；
//   - resolveWorkspaceNewPath 解析尚不存在的目标，改为校验父目录，规则与上面一致；
//   - isGitRelated / isProtected 拒绝 .git 内部与凭据类文件。
//
// 删除只允许移到系统废纸篓（macOS 的 ~/.Trash），绝不直接删文件。

// filesOpsMaxPathLength 与 files.read / files.write 的路径长度上限一致。
const filesOpsMaxPathLength = 4096

// FilesCreate 在工作区内新建空文件或目录，父目录必须已经存在。
func (b *MemoryBackend) FilesCreate(ctx context.Context, p wire.FilesCreateParams) (wire.FilesCreateResult, error) {
	root, err := b.threadWorkspaceRoot(p.ThreadID)
	if err != nil {
		return wire.FilesCreateResult{}, err
	}

	rel := strings.TrimSpace(p.Path)
	target, err := resolveWorkspaceNewPath(root, rel)
	if err != nil {
		return wire.FilesCreateResult{}, err
	}
	if target == root {
		return wire.FilesCreateResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "cannot operate on workspace root",
		}
	}
	if err := ensureWorkspaceMutablePath(rel, target); err != nil {
		return wire.FilesCreateResult{}, err
	}

	exists, err := workspacePathExists(target)
	if err != nil {
		return wire.FilesCreateResult{}, &wire.RPCError{
			Code:    wire.ErrCodeOperationRejected,
			Message: fmt.Sprintf("failed to inspect path: %v", err),
		}
	}
	if exists {
		return wire.FilesCreateResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "path already exists",
		}
	}

	if p.Dir {
		if err := os.Mkdir(target, 0o755); err != nil {
			if os.IsExist(err) {
				return wire.FilesCreateResult{}, &wire.RPCError{
					Code:    wire.InvalidParams,
					Message: "path already exists",
				}
			}
			return wire.FilesCreateResult{}, &wire.RPCError{
				Code:    wire.ErrCodeOperationRejected,
				Message: fmt.Sprintf("failed to create directory: %v", err),
			}
		}
	} else {
		f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			if os.IsExist(err) {
				return wire.FilesCreateResult{}, &wire.RPCError{
					Code:    wire.InvalidParams,
					Message: "path already exists",
				}
			}
			return wire.FilesCreateResult{}, &wire.RPCError{
				Code:    wire.ErrCodeOperationRejected,
				Message: fmt.Sprintf("failed to create file: %v", err),
			}
		}
		if err := f.Close(); err != nil {
			return wire.FilesCreateResult{}, &wire.RPCError{
				Code:    wire.ErrCodeOperationRejected,
				Message: fmt.Sprintf("failed to create file: %v", err),
			}
		}
	}

	return wire.FilesCreateResult{Path: workspaceRelPath(root, target)}, nil
}

// FilesRename 重命名或移动工作区内的条目，目标不能已存在。
func (b *MemoryBackend) FilesRename(ctx context.Context, p wire.FilesRenameParams) (wire.FilesRenameResult, error) {
	root, err := b.threadWorkspaceRoot(p.ThreadID)
	if err != nil {
		return wire.FilesRenameResult{}, err
	}

	rel := strings.TrimSpace(p.Path)
	source, err := resolveWorkspacePath(root, rel)
	if err != nil {
		return wire.FilesRenameResult{}, err
	}
	if source == root {
		return wire.FilesRenameResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "cannot rename workspace root",
		}
	}
	if err := ensureWorkspaceMutablePath(rel, source); err != nil {
		return wire.FilesRenameResult{}, err
	}

	newRel := strings.TrimSpace(p.NewPath)
	target, err := resolveWorkspaceNewPath(root, newRel)
	if err != nil {
		return wire.FilesRenameResult{}, err
	}
	if target == root {
		return wire.FilesRenameResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "cannot rename workspace root",
		}
	}
	if err := ensureWorkspaceMutablePath(newRel, target); err != nil {
		return wire.FilesRenameResult{}, err
	}

	exists, err := workspacePathExists(target)
	if err != nil {
		return wire.FilesRenameResult{}, &wire.RPCError{
			Code:    wire.ErrCodeOperationRejected,
			Message: fmt.Sprintf("failed to inspect path: %v", err),
		}
	}
	if exists {
		return wire.FilesRenameResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "path already exists",
		}
	}

	if err := os.Rename(source, target); err != nil {
		return wire.FilesRenameResult{}, &wire.RPCError{
			Code:    wire.ErrCodeOperationRejected,
			Message: fmt.Sprintf("failed to rename path: %v", err),
		}
	}

	return wire.FilesRenameResult{Path: workspaceRelPath(root, target)}, nil
}

// FilesTrash 把工作区内的条目移到系统废纸篓或回收站。
func (b *MemoryBackend) FilesTrash(ctx context.Context, p wire.FilesTrashParams) (wire.FilesTrashResult, error) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		return wire.FilesTrashResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "trash is not supported on this platform",
		}
	}

	root, err := b.threadWorkspaceRoot(p.ThreadID)
	if err != nil {
		return wire.FilesTrashResult{}, err
	}

	rel := strings.TrimSpace(p.Path)
	source, err := resolveWorkspacePath(root, rel)
	if err != nil {
		return wire.FilesTrashResult{}, err
	}
	if source == root {
		return wire.FilesTrashResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "cannot trash workspace root",
		}
	}
	if err := ensureWorkspaceMutablePath(rel, source); err != nil {
		return wire.FilesTrashResult{}, err
	}

	if err := fsutil.MoveToTrash(source); err != nil {
		return wire.FilesTrashResult{}, &wire.RPCError{
			Code:    wire.ErrCodeOperationRejected,
			Message: err.Error(),
		}
	}

	return wire.FilesTrashResult{}, nil
}

// FilesDuplicate 在同一目录下复制普通文件，副本命名为 "名字 copy.ext"，
// 已存在时依次尝试 "名字 copy 2.ext"、"名字 copy 3.ext"。
func (b *MemoryBackend) FilesDuplicate(ctx context.Context, p wire.FilesDuplicateParams) (wire.FilesDuplicateResult, error) {
	root, err := b.threadWorkspaceRoot(p.ThreadID)
	if err != nil {
		return wire.FilesDuplicateResult{}, err
	}

	rel := strings.TrimSpace(p.Path)
	source, err := resolveWorkspacePath(root, rel)
	if err != nil {
		return wire.FilesDuplicateResult{}, err
	}
	if source == root {
		return wire.FilesDuplicateResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "cannot duplicate workspace root",
		}
	}
	if err := ensureWorkspaceMutablePath(rel, source); err != nil {
		return wire.FilesDuplicateResult{}, err
	}

	fi, err := os.Stat(source)
	if err != nil {
		return wire.FilesDuplicateResult{}, &wire.RPCError{
			Code:    wire.ErrCodeOperationRejected,
			Message: fmt.Sprintf("failed to stat path: %v", err),
		}
	}
	if fi.IsDir() {
		return wire.FilesDuplicateResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "cannot duplicate a directory",
		}
	}
	if !fi.Mode().IsRegular() {
		return wire.FilesDuplicateResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "only regular files can be duplicated",
		}
	}

	dir := filepath.Dir(source)
	base := filepath.Base(source)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	target := ""
	for i := 1; i <= 1000; i++ {
		suffix := " copy"
		if i > 1 {
			suffix = fmt.Sprintf(" copy %d", i)
		}
		candidate := filepath.Join(dir, stem+suffix+ext)
		exists, err := workspacePathExists(candidate)
		if err != nil {
			return wire.FilesDuplicateResult{}, &wire.RPCError{
				Code:    wire.ErrCodeOperationRejected,
				Message: fmt.Sprintf("failed to inspect path: %v", err),
			}
		}
		if !exists {
			target = candidate
			break
		}
	}
	if target == "" {
		return wire.FilesDuplicateResult{}, &wire.RPCError{
			Code:    wire.ErrCodeOperationRejected,
			Message: "too many copies with the same name",
		}
	}

	perm := fi.Mode().Perm()
	if perm == 0 {
		perm = 0o644
	}
	if err := copyWorkspaceFile(source, target, perm); err != nil {
		return wire.FilesDuplicateResult{}, &wire.RPCError{
			Code:    wire.ErrCodeOperationRejected,
			Message: fmt.Sprintf("failed to duplicate file: %v", err),
		}
	}

	return wire.FilesDuplicateResult{Path: workspaceRelPath(root, target)}, nil
}

// resolveWorkspaceNewPath 解析一个尚不存在的目标路径。
// resolveWorkspacePath 要求目标已经存在，这里改为先解析父目录：父目录同样走
// resolveWorkspacePath，因此绝对路径、~、".."、越界与指向工作区外的符号链接都在这里被拒绝；
// 目标本身已存在时再按真实路径校验一次，避免通过符号链接落到工作区外。
func resolveWorkspaceNewPath(root, rel string) (string, error) {
	trimmed := strings.TrimSpace(rel)
	if strings.Contains(trimmed, "\x00") ||
		strings.HasPrefix(trimmed, "~") ||
		filepath.IsAbs(trimmed) ||
		strings.HasPrefix(trimmed, "/") ||
		strings.HasPrefix(trimmed, "\\") {
		return "", &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "invalid relative path",
		}
	}

	cleanRel := filepath.Clean(trimmed)
	if cleanRel == "." || cleanRel == "" {
		return root, nil
	}

	parent, err := resolveWorkspacePath(root, filepath.Dir(cleanRel))
	if err != nil {
		return "", err
	}
	base := filepath.Base(cleanRel)
	if base == "." || base == ".." || base == "" || strings.ContainsAny(base, `/\`) {
		return "", &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "invalid relative path",
		}
	}

	target := filepath.Join(parent, base)
	evalTarget, err := filepath.EvalSymlinks(target)
	if err == nil {
		if evalTarget != root && !strings.HasPrefix(evalTarget, root+string(filepath.Separator)) {
			return "", &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: "path escapes workspace root",
			}
		}
		return evalTarget, nil
	}
	if !os.IsNotExist(err) {
		return "", &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: fmt.Sprintf("failed to resolve path: %v", err),
		}
	}
	return target, nil
}

// ensureWorkspaceMutablePath 拒绝 .git 内部与受保护凭据路径，读写两侧规则保持一致。
func ensureWorkspaceMutablePath(relPath, canonicalPath string) error {
	if isGitRelated(relPath, canonicalPath) {
		return &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "cannot modify .git internals",
		}
	}
	if isProtected(relPath, canonicalPath) {
		return &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "File is protected and cannot be modified",
		}
	}
	return nil
}

// workspacePathExists 判断路径是否存在，不跟随最后的符号链接。
func workspacePathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// workspaceRelPath 返回相对工作区根目录的正斜杠路径；根目录本身返回空串。
func workspaceRelPath(root, target string) string {
	if target == root {
		return ""
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return filepath.ToSlash(target)
	}
	return filepath.ToSlash(rel)
}

// copyWorkspaceFile 逐字节复制文件并在落盘前保留源文件权限；
// 目标用 O_EXCL 打开，避免覆盖刚被别人创建的文件。
func copyWorkspaceFile(source, target string, perm os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(target)
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(target)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(target)
		return err
	}
	return nil
}
