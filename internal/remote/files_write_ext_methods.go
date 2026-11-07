package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"mcode/internal/remote/wire"
)

// files.write：远程覆盖保存工作区文件，对应桌面端内置编辑器的保存动作。
// 读取入口是 files.read（internal/remote/files.go），这里刻意复用同一套工作区定位与路径校验：
//   - threadWorkspaceRoot 定位会话工作区；
//   - resolveWorkspacePath 拒绝绝对路径、~、..、越界与指向工作区外的符号链接；
//   - isGitRelated / isProtected 与读取保持一致，.git 内部与凭据类文件既不可读也不可写。
//
// 只覆盖「已存在的普通文本文件」：不创建新文件、不写目录、不写二进制，
// 内容上限与 files.read 的文本分块上限一致（512 KiB），保证读出来的内容一定写得回去。

const (
	// filesWriteMethod 是覆盖保存文件的方法名。
	filesWriteMethod = "files.write"
	// filesWriteMaxContentBytes 与 files.read 的文本分块上限一致（512 KiB）。
	filesWriteMaxContentBytes = 512 * 1024
	// filesWriteMaxPathLength 与 files.read 的路径长度上限一致。
	filesWriteMaxPathLength = 4096
	// filesWriteHeaderBytes 是二进制探测的头部字节数，与 files.read 一致。
	filesWriteHeaderBytes = 512
)

func init() {
	RegisterMethod(filesWriteMethod, handleFilesWrite)
}

// handleFilesWrite 严格校验参数后交给 MemoryBackend.FilesWrite 落盘。
func handleFilesWrite(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.FilesWriteExtParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if !isValidUUID(p.ThreadID) {
		return nil, ExtInvalidParams("Invalid params: 'threadID' must be a valid UUID")
	}
	path := strings.TrimSpace(p.Path)
	if path == "" || len(p.Path) > filesWriteMaxPathLength {
		return nil, ExtInvalidParams("Invalid params: 'path' is required and must be at most 4096 characters")
	}
	if len(p.Content) > filesWriteMaxContentBytes {
		return nil, ExtInvalidParams(fmt.Sprintf("Invalid params: 'content' must be at most %d bytes", filesWriteMaxContentBytes))
	}
	if !utf8.ValidString(p.Content) {
		return nil, ExtInvalidParams("Invalid params: 'content' must be valid UTF-8 text")
	}
	if p.ExpectedHash != "" {
		if _, err := normalizeFileHash(p.ExpectedHash); err != nil {
			return nil, ExtInvalidParams("Invalid params: 'expectedHash' must be a 64-character hex SHA-256")
		}
	}

	mb, ok := d.memoryBackend()
	if !ok {
		return nil, &ExtError{Code: wire.ErrCodeOperationRejected, Message: "workspace file operations unavailable"}
	}
	p.Path = path
	res, err := mb.FilesWrite(ctx, p)
	if err != nil {
		return nil, filesWriteExtError(err)
	}
	return res, nil
}

// FilesWrite 原子覆盖工作区内已存在的文本文件。
// expectedHash 非空时先比对磁盘当前内容的 SHA-256，不一致返回版本冲突。
func (b *MemoryBackend) FilesWrite(ctx context.Context, p wire.FilesWriteExtParams) (wire.FilesWriteExtResult, error) {
	root, err := b.threadWorkspaceRoot(p.ThreadID)
	if err != nil {
		return wire.FilesWriteExtResult{}, err
	}

	// 复用读取侧的路径校验：绝对路径、~、.. 与解析后越界的符号链接都在这里被拒绝。
	targetFile, err := resolveWorkspacePath(root, p.Path)
	if err != nil {
		return wire.FilesWriteExtResult{}, err
	}

	if isGitRelated(p.Path, targetFile) {
		return wire.FilesWriteExtResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "cannot write .git internals",
		}
	}
	if isProtected(p.Path, targetFile) {
		return wire.FilesWriteExtResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "File is protected and cannot be written",
		}
	}

	f, err := os.Open(targetFile)
	if err != nil {
		if os.IsNotExist(err) {
			// 只允许覆盖已存在的文件，不通过远程写入创建新文件。
			return wire.FilesWriteExtResult{}, &wire.RPCError{
				Code:    wire.ErrCodeFileNotFound,
				Message: fmt.Sprintf("file not found: %s", p.Path),
			}
		}
		return wire.FilesWriteExtResult{}, &wire.RPCError{
			Code:    wire.ErrCodeFileNotFound,
			Message: fmt.Sprintf("failed to open file: %v", err),
		}
	}

	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return wire.FilesWriteExtResult{}, &wire.RPCError{
			Code:    wire.InternalError,
			Message: fmt.Sprintf("failed to stat file: %v", err),
		}
	}
	if !fi.Mode().IsRegular() {
		_ = f.Close()
		return wire.FilesWriteExtResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "Not a regular file",
		}
	}

	// 二进制探测与读取侧同一套判据：头部含 0x00 或不是合法 UTF-8 就按二进制拒绝。
	if fi.Size() > 0 {
		header := make([]byte, filesWriteHeaderBytes)
		n, readErr := f.ReadAt(header, 0)
		if readErr != nil && readErr != io.EOF {
			_ = f.Close()
			return wire.FilesWriteExtResult{}, &wire.RPCError{
				Code:    wire.InternalError,
				Message: fmt.Sprintf("failed to read file header: %v", readErr),
			}
		}
		header = header[:n]
		binary := bytes.IndexByte(header, 0) != -1
		if !binary {
			if _, _, ok := cleanUTF8Chunk(header); !ok {
				binary = true
			}
		}
		if binary {
			_ = f.Close()
			return wire.FilesWriteExtResult{}, &wire.RPCError{
				Code:    wire.ErrCodeOperationRejected,
				Message: "cannot write binary file",
			}
		}
	}

	// ReadAt 不移动文件游标，这里从 0 开始整文件哈希，用于冲突检测。
	expectedHash := ""
	if p.ExpectedHash != "" {
		expectedHash, err = normalizeFileHash(p.ExpectedHash)
		if err != nil {
			_ = f.Close()
			return wire.FilesWriteExtResult{}, &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: "expectedHash must be a 64-character hex SHA-256",
			}
		}
		sum := sha256.New()
		if _, err := io.Copy(sum, f); err != nil {
			_ = f.Close()
			return wire.FilesWriteExtResult{}, &wire.RPCError{
				Code:    wire.InternalError,
				Message: fmt.Sprintf("failed to hash file: %v", err),
			}
		}
		if hex.EncodeToString(sum.Sum(nil)) != expectedHash {
			_ = f.Close()
			return wire.FilesWriteExtResult{}, &wire.RPCError{
				Code:    wire.ErrCodeVersionConflict,
				Message: "file content changed since it was read; reload and retry",
			}
		}
	}
	_ = f.Close()

	data := []byte(p.Content)
	perm := fi.Mode().Perm()
	if perm == 0 {
		perm = 0o644
	}
	if err := writeWorkspaceFileAtomic(targetFile, data, perm); err != nil {
		return wire.FilesWriteExtResult{}, &wire.RPCError{
			Code:    wire.ErrCodeOperationRejected,
			Message: fmt.Sprintf("failed to write file: %v", err),
		}
	}

	return wire.FilesWriteExtResult{
		Hash: fileContentHashHex(data),
		Size: int64(len(data)),
	}, nil
}

// writeWorkspaceFileAtomic 同目录写临时文件再 rename，保证读者不会看到写了一半的内容；
// 落盘前显式 chmod 回原文件权限位，避免 CreateTemp 的 0600 覆盖原权限。
// 与 project_scripts.go 的 writeFileAtomic 分开：后者不保留权限，专用于 mcode 自有配置。
func writeWorkspaceFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".mcode-write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		_ = os.Remove(tmpName)
		return err
	}

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fail(err)
	}
	return nil
}

// normalizeFileHash 规范化客户端传入的哈希：去空白、转小写，并校验为 64 位十六进制。
func normalizeFileHash(hash string) (string, error) {
	clean := strings.ToLower(strings.TrimSpace(hash))
	if len(clean) != 64 {
		return "", errors.New("hash must be 64 hex characters")
	}
	if _, err := hex.DecodeString(clean); err != nil {
		return "", err
	}
	return clean, nil
}

// filesWriteExtError 保留 file 侧的错误码（InvalidParams、FileNotFound、版本冲突等），
// 其余错误统一按业务拒绝返回，避免被外层归成笼统的内部错误。
func filesWriteExtError(err error) error {
	if err == nil {
		return nil
	}
	var extErr *ExtError
	if errors.As(err, &extErr) {
		return extErr
	}
	var rpcErr *wire.RPCError
	if errors.As(err, &rpcErr) {
		return &ExtError{Code: rpcErr.Code, Message: rpcErr.Message}
	}
	return &ExtError{Code: wire.ErrCodeOperationRejected, Message: RedactSecrets(err.Error())}
}
