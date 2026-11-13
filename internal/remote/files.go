package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"mcode/internal/procattr"
	"mcode/internal/remote/wire"
)

// threadWorkspaceRoot 获取指定会话对应的工作区绝对路径，并完成符号链接解析。
func (b *MemoryBackend) threadWorkspaceRoot(threadID string) (string, error) {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return "", &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}
	rawPath := td.workDir
	if rawPath == "" {
		proj, ok := b.projects[td.summary.ProjectID]
		if !ok {
			b.mu.Unlock()
			return "", &wire.RPCError{
				Code:    wire.ErrCodeResourceNotFound,
				Message: fmt.Sprintf("project %s not found", td.summary.ProjectID),
			}
		}
		rawPath = proj.Path
	}
	b.mu.Unlock()

	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return "", &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: "workspace path is empty",
		}
	}

	absPath, err := filepath.Abs(rawPath)
	if err != nil {
		return "", &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("failed to get absolute workspace path: %v", err),
		}
	}

	evalRoot, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("workspace directory not found: %v", err),
		}
	}
	return evalRoot, nil
}

// resolveWorkspacePath 防护函数：拒绝绝对路径与包含 ".." 的相对路径；
// EvalSymlinks 之后的目标路径必须仍在 root 目录内，否则返回 InvalidParams 错误。
func resolveWorkspacePath(root, rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if strings.Contains(rel, "\x00") || filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "\\") || strings.HasPrefix(rel, "~") {
		return "", &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "invalid relative path",
		}
	}

	parts := strings.FieldsFunc(rel, func(r rune) bool {
		return r == '/' || r == '\\'
	})
	for _, p := range parts {
		if p == ".." {
			return "", &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: "path contains parent directory traversal",
			}
		}
	}

	cleanRel := filepath.Clean(rel)
	if cleanRel == "." || cleanRel == "" {
		return root, nil
	}

	joined := filepath.Join(root, cleanRel)
	evalPath, err := filepath.EvalSymlinks(joined)
	if err != nil {
		if os.IsNotExist(err) {
			return "", &wire.RPCError{
				Code:    wire.ErrCodeFileNotFound,
				Message: fmt.Sprintf("file or directory not found: %s", rel),
			}
		}
		return "", &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: fmt.Sprintf("failed to resolve path: %v", err),
		}
	}

	if evalPath != root && !strings.HasPrefix(evalPath, root+string(filepath.Separator)) {
		return "", &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "path escapes workspace root",
		}
	}
	return evalPath, nil
}

// isSensitiveFileName 检查文件名是否包含密钥、证书或环境配置等敏感信息。
func isSensitiveFileName(name string) bool {
	lower := strings.ToLower(name)
	if lower == ".env" || strings.HasPrefix(lower, ".env.") || strings.HasPrefix(lower, ".env_") || strings.HasPrefix(lower, ".env-") {
		return true
	}
	if strings.HasPrefix(lower, "id_rsa") || strings.HasPrefix(lower, "id_ed25519") {
		return true
	}
	suffixes := []string{".pem", ".key", ".p12", ".pfx", ".crt", ".cer"}
	for _, s := range suffixes {
		if strings.HasSuffix(lower, s) {
			return true
		}
	}
	return false
}

// isCredentialDirectoryName 检查目录名是否为凭据敏感目录（.ssh、.aws、.gnupg）。
func isCredentialDirectoryName(name string) bool {
	lower := strings.ToLower(name)
	return lower == ".ssh" || lower == ".aws" || lower == ".gnupg"
}

// isProtectedPathString 检查路径字符串的各层级中是否包含敏感凭据目录或敏感文件。
func isProtectedPathString(path string) bool {
	parts := strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == filepath.Separator
	})
	for _, p := range parts {
		if isCredentialDirectoryName(p) {
			return true
		}
	}
	if len(parts) > 0 && isSensitiveFileName(parts[len(parts)-1]) {
		return true
	}
	return false
}

// isProtected 综合相对路径与物理绝对路径判断该条目是否受保护。
func isProtected(relPath, canonicalPath string) bool {
	if isProtectedPathString(relPath) {
		return true
	}
	if canonicalPath != "" && isProtectedPathString(canonicalPath) {
		return true
	}
	return false
}

// isGitRelated 检查相对路径或物理路径是否涉及 .git 内部结构。
func isGitRelated(relPath, canonicalPath string) bool {
	parts := strings.FieldsFunc(relPath, func(r rune) bool {
		return r == '/' || r == filepath.Separator
	})
	for _, p := range parts {
		if p == ".git" {
			return true
		}
	}
	if canonicalPath != "" {
		canParts := strings.FieldsFunc(canonicalPath, func(r rune) bool {
			return r == '/' || r == filepath.Separator
		})
		for _, p := range canParts {
			if p == ".git" {
				return true
			}
		}
	}
	return false
}

// gitCheckIgnored 通过 git check-ignore -z --stdin 批量查询路径是否被忽略。
func gitCheckIgnored(ctx context.Context, root string, paths []string) map[string]bool {
	if len(paths) == 0 {
		return nil
	}
	cmd := procattr.HideWindow(exec.CommandContext(ctx, "git", "check-ignore", "-z", "--stdin"))
	cmd.Dir = root
	var stdin bytes.Buffer
	for _, p := range paths {
		stdin.WriteString(p)
		stdin.WriteByte(0)
	}
	cmd.Stdin = &stdin
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil && stdout.Len() == 0 {
		return nil
	}

	ignored := make(map[string]bool)
	records := bytes.Split(stdout.Bytes(), []byte{0})
	for _, rec := range records {
		if len(rec) > 0 {
			ignored[string(rec)] = true
		}
	}
	return ignored
}

// detectImageKind 根据文件头与扩展名探测图片 MIME 类型。
func detectImageKind(header []byte, ext string) (bool, string) {
	if len(header) >= 3 && bytes.Equal(header[:3], []byte{0xFF, 0xD8, 0xFF}) {
		return true, "image/jpeg"
	}
	if len(header) >= 8 && bytes.Equal(header[:8], []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}) {
		return true, "image/png"
	}
	if len(header) >= 4 && bytes.Equal(header[:4], []byte{0x47, 0x49, 0x46, 0x38}) {
		return true, "image/gif"
	}
	if len(header) >= 12 && bytes.Equal(header[:4], []byte("RIFF")) && bytes.Equal(header[8:12], []byte("WEBP")) {
		return true, "image/webp"
	}

	cleanExt := strings.TrimPrefix(strings.ToLower(ext), ".")
	switch cleanExt {
	case "jpg", "jpeg":
		if len(header) >= 2 && bytes.Equal(header[:2], []byte{0xFF, 0xD8}) {
			return true, "image/jpeg"
		}
	case "png":
		if len(header) >= 2 && bytes.Equal(header[:2], []byte{0x89, 0x50}) {
			return true, "image/png"
		}
	case "gif":
		if len(header) >= 2 && bytes.Equal(header[:2], []byte{0x47, 0x49}) {
			return true, "image/gif"
		}
	case "webp":
		if len(header) >= 4 && bytes.Equal(header[:4], []byte("RIFF")) {
			return true, "image/webp"
		}
	}

	mime := http.DetectContentType(header)
	if strings.HasPrefix(mime, "image/") {
		return true, mime
	}
	return false, ""
}

// cleanUTF8Chunk 检查并裁剪分块末尾被截断的 UTF-8 字符（回退 0-3 字节）。
func cleanUTF8Chunk(data []byte) (string, int, bool) {
	if utf8.Valid(data) {
		return string(data), len(data), true
	}
	for drop := 1; drop <= 3 && drop < len(data); drop++ {
		sub := data[:len(data)-drop]
		if utf8.Valid(sub) {
			return string(sub), len(sub), true
		}
	}
	return "", 0, false
}

// fuzzyMatch 不区分大小写的子序列模糊匹配（兼顾子串匹配）。
func fuzzyMatch(pattern, target string) bool {
	pattern = strings.ToLower(pattern)
	target = strings.ToLower(target)
	if strings.Contains(target, pattern) {
		return true
	}
	pRunes := []rune(pattern)
	tRunes := []rune(target)
	pIdx := 0
	for _, r := range tRunes {
		if pIdx < len(pRunes) && r == pRunes[pIdx] {
			pIdx++
		}
	}
	return pIdx == len(pRunes)
}

// FilesList 列出指定会话工作区目录下的单层文件与目录。
func (b *MemoryBackend) FilesList(ctx context.Context, p wire.RemoteFilesListParams) (wire.RemoteFilesListResult, error) {
	root, err := b.threadWorkspaceRoot(p.ThreadID)
	if err != nil {
		return wire.RemoteFilesListResult{}, err
	}

	targetDir, err := resolveWorkspacePath(root, p.Subpath)
	if err != nil {
		return wire.RemoteFilesListResult{}, err
	}

	if isGitRelated(p.Subpath, targetDir) {
		return wire.RemoteFilesListResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "cannot list .git internals",
		}
	}

	fi, err := os.Stat(targetDir)
	if err != nil {
		return wire.RemoteFilesListResult{}, &wire.RPCError{
			Code:    wire.ErrCodeFileNotFound,
			Message: fmt.Sprintf("directory not found: %s", p.Subpath),
		}
	}
	if !fi.IsDir() {
		return wire.RemoteFilesListResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "target is not a directory",
		}
	}

	entries, err := os.ReadDir(targetDir)
	if err != nil {
		return wire.RemoteFilesListResult{}, &wire.RPCError{
			Code:    wire.InternalError,
			Message: fmt.Sprintf("failed to read directory: %v", err),
		}
	}

	cleanSub := filepath.Clean(p.Subpath)
	if cleanSub == "." {
		cleanSub = ""
	}

	var rawEntries []wire.RemoteFileEntry
	for _, entry := range entries {
		name := entry.Name()
		if name == ".git" {
			continue
		}

		itemRelPath := name
		if cleanSub != "" {
			itemRelPath = filepath.Join(cleanSub, name)
		}
		itemAbsPath := filepath.Join(targetDir, name)

		lfi, lerr := os.Lstat(itemAbsPath)
		if lerr != nil {
			continue
		}

		if lfi.Mode()&os.ModeSymlink != 0 {
			targetCanonical, err := filepath.EvalSymlinks(itemAbsPath)
			if isGitRelated(itemRelPath, targetCanonical) {
				continue
			}
			isWithin := err == nil && (targetCanonical == root || strings.HasPrefix(targetCanonical, root+string(filepath.Separator)))
			isProt := !isWithin || isProtected(itemRelPath, targetCanonical)
			rawEntries = append(rawEntries, wire.RemoteFileEntry{
				Name:      name,
				Path:      itemRelPath,
				Kind:      wire.RemoteFileEntryKindSymlink,
				Size:      lfi.Size(),
				Ignored:   false,
				Protected: isProt,
			})
		} else if lfi.IsDir() {
			isProt := isProtected(itemRelPath, itemAbsPath)
			rawEntries = append(rawEntries, wire.RemoteFileEntry{
				Name:      name,
				Path:      itemRelPath,
				Kind:      wire.RemoteFileEntryKindDirectory,
				Size:      0,
				Ignored:   false,
				Protected: isProt,
			})
		} else {
			isProt := isProtected(itemRelPath, itemAbsPath)
			rawEntries = append(rawEntries, wire.RemoteFileEntry{
				Name:      name,
				Path:      itemRelPath,
				Kind:      wire.RemoteFileEntryKindFile,
				Size:      lfi.Size(),
				Ignored:   false,
				Protected: isProt,
			})
		}
	}

	sort.Slice(rawEntries, func(i, j int) bool {
		aIsDir := rawEntries[i].Kind == wire.RemoteFileEntryKindDirectory
		bIsDir := rawEntries[j].Kind == wire.RemoteFileEntryKindDirectory
		if aIsDir != bIsDir {
			return aIsDir
		}
		return strings.ToLower(rawEntries[i].Name) < strings.ToLower(rawEntries[j].Name)
	})

	truncated := false
	const maxListCount = 1000
	if len(rawEntries) > maxListCount {
		truncated = true
		rawEntries = rawEntries[:maxListCount]
	}

	paths := make([]string, len(rawEntries))
	for i, e := range rawEntries {
		paths[i] = e.Path
	}
	ignoredMap := gitCheckIgnored(ctx, root, paths)
	if ignoredMap != nil {
		for i := range rawEntries {
			if ignoredMap[rawEntries[i].Path] {
				rawEntries[i].Ignored = true
			}
		}
	}

	if rawEntries == nil {
		rawEntries = make([]wire.RemoteFileEntry, 0)
	}

	return wire.RemoteFilesListResult{
		Path:      cleanSub,
		Entries:   rawEntries,
		Truncated: truncated,
	}, nil
}

// FilesRead 读取工作区文本或图片分块。
func (b *MemoryBackend) FilesRead(ctx context.Context, p wire.RemoteFilesReadParams) (wire.RemoteFilesReadResult, error) {
	root, err := b.threadWorkspaceRoot(p.ThreadID)
	if err != nil {
		return wire.RemoteFilesReadResult{}, err
	}

	targetFile, err := resolveWorkspacePath(root, p.Path)
	if err != nil {
		return wire.RemoteFilesReadResult{}, err
	}

	if isGitRelated(p.Path, targetFile) {
		return wire.RemoteFilesReadResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "cannot read .git internals",
		}
	}

	if isProtected(p.Path, targetFile) {
		return wire.RemoteFilesReadResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "File is protected and cannot be read",
		}
	}

	f, err := os.Open(targetFile)
	if err != nil {
		return wire.RemoteFilesReadResult{}, &wire.RPCError{
			Code:    wire.ErrCodeFileNotFound,
			Message: fmt.Sprintf("failed to open file: %v", err),
		}
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return wire.RemoteFilesReadResult{}, &wire.RPCError{
			Code:    wire.InternalError,
			Message: fmt.Sprintf("failed to stat file: %v", err),
		}
	}

	if fi.IsDir() {
		return wire.RemoteFilesReadResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "Not a regular file",
		}
	}

	totalBytes := fi.Size()
	reqOffset := int64(0)
	if p.Offset != nil {
		reqOffset = *p.Offset
	}
	if reqOffset < 0 || reqOffset > totalBytes {
		return wire.RemoteFilesReadResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "offset out of bounds",
		}
	}

	cleanPath := filepath.Clean(p.Path)

	if totalBytes == 0 {
		emptyStr := ""
		plainMime := "text/plain; charset=utf-8"
		return wire.RemoteFilesReadResult{
			Path:       cleanPath,
			Kind:       wire.RemoteFileContentKindText,
			Text:       &emptyStr,
			MimeType:   &plainMime,
			Hash:       fileContentHashHex(nil),
			TotalBytes: 0,
			Offset:     0,
			Truncated:  false,
		}, nil
	}

	header := make([]byte, 512)
	nHeader, _ := f.ReadAt(header, 0)
	header = header[:nHeader]

	isImg, imgMime := detectImageKind(header, filepath.Ext(cleanPath))

	if isImg {
		const maxImageBytes = 8 * 1024 * 1024
		if totalBytes > maxImageBytes {
			return wire.RemoteFilesReadResult{}, &wire.RPCError{
				Code:    wire.ErrCodeMediaTooLarge,
				Message: fmt.Sprintf("image file exceeds %d bytes limit", maxImageBytes),
			}
		}

		chunkLimit := 256 * 1024
		if p.Length != nil && *p.Length > 0 && *p.Length < chunkLimit {
			chunkLimit = *p.Length
		}

		bytesToRead := int64(chunkLimit)
		if totalBytes-reqOffset < bytesToRead {
			bytesToRead = totalBytes - reqOffset
		}

		buf := make([]byte, bytesToRead)
		n, err := f.ReadAt(buf, reqOffset)
		if err != nil && err != io.EOF && int64(n) < bytesToRead {
			return wire.RemoteFilesReadResult{}, &wire.RPCError{
				Code:    wire.InternalError,
				Message: fmt.Sprintf("failed to read image chunk: %v", err),
			}
		}
		buf = buf[:n]

		b64 := base64.StdEncoding.EncodeToString(buf)
		nextOffset := reqOffset + int64(n)
		hasNext := nextOffset < totalBytes

		var nextOffsetPtr *int64
		if hasNext {
			nextOffsetPtr = &nextOffset
		}

		return wire.RemoteFilesReadResult{
			Path:       cleanPath,
			Kind:       wire.RemoteFileContentKindImage,
			Base64:     &b64,
			MimeType:   &imgMime,
			TotalBytes: totalBytes,
			Offset:     reqOffset,
			NextOffset: nextOffsetPtr,
			Truncated:  hasNext,
		}, nil
	}

	// 检查是否为二进制文件（前 512 字节包含 0x00）
	if bytes.IndexByte(header, 0) != -1 {
		return wire.RemoteFilesReadResult{
			Path:       cleanPath,
			Kind:       wire.RemoteFileContentKindBinary,
			TotalBytes: totalBytes,
			Offset:     0,
			Truncated:  false,
		}, nil
	}

	// 文本文件读取
	chunkLimit := 512 * 1024
	if p.Length != nil && *p.Length > 0 && *p.Length < chunkLimit {
		chunkLimit = *p.Length
	}

	bytesToRead := int64(chunkLimit)
	if totalBytes-reqOffset < bytesToRead {
		bytesToRead = totalBytes - reqOffset
	}

	buf := make([]byte, bytesToRead)
	n, err := f.ReadAt(buf, reqOffset)
	if err != nil && err != io.EOF && int64(n) < bytesToRead {
		return wire.RemoteFilesReadResult{}, &wire.RPCError{
			Code:    wire.InternalError,
			Message: fmt.Sprintf("failed to read text chunk: %v", err),
		}
	}
	buf = buf[:n]

	cleanText, validByteCount, ok := cleanUTF8Chunk(buf)
	if !ok {
		return wire.RemoteFilesReadResult{
			Path:       cleanPath,
			Kind:       wire.RemoteFileContentKindBinary,
			TotalBytes: totalBytes,
			Offset:     0,
			Truncated:  false,
		}, nil
	}

	nextOffset := reqOffset + int64(validByteCount)
	hasNext := nextOffset < totalBytes
	var nextOffsetPtr *int64
	if hasNext {
		nextOffsetPtr = &nextOffset
	}

	// 只有一次读完整个文件时才给 hash：它对应客户端手上的完整内容，可直接作为
	// files.write 的 expectedHash；分块读取时省略，避免每次分块都整文件重算。
	fullHash := ""
	if reqOffset == 0 && !hasNext {
		fullHash = fileContentHashHex(buf)
	}

	plainMime := "text/plain; charset=utf-8"
	return wire.RemoteFilesReadResult{
		Path:       cleanPath,
		Kind:       wire.RemoteFileContentKindText,
		Text:       &cleanText,
		MimeType:   &plainMime,
		Hash:       fullHash,
		TotalBytes: totalBytes,
		Offset:     reqOffset,
		NextOffset: nextOffsetPtr,
		Truncated:  hasNext,
	}, nil
}

// fileContentHashHex 返回整段内容的 SHA-256 小写十六进制。
// files.read 的 hash 字段与 files.write 的结果 hash 都用它，客户端可直接回传做冲突检测。
func fileContentHashHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// FilesSearch 在工作区内根据文件名和相对路径进行模糊搜索。
func (b *MemoryBackend) FilesSearch(ctx context.Context, p wire.RemoteFilesSearchParams) (wire.RemoteFilesSearchResult, error) {
	query := strings.TrimSpace(p.Query)
	if len([]rune(query)) > 256 {
		return wire.RemoteFilesSearchResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "query must be at most 256 characters",
		}
	}
	// 空查询直接返回空结果而不是报错
	if query == "" {
		return wire.RemoteFilesSearchResult{
			Entries:   make([]wire.RemoteFileEntry, 0),
			Truncated: false,
		}, nil
	}

	root, err := b.threadWorkspaceRoot(p.ThreadID)
	if err != nil {
		return wire.RemoteFilesSearchResult{}, err
	}

	const maxResults = 200
	const scanBudget = 10000
	scannedCount := 0
	truncated := false
	var matches []wire.RemoteFileEntry

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		name := d.Name()
		if path == root {
			return nil
		}

		scannedCount++
		if scannedCount > scanBudget {
			truncated = true
			return filepath.SkipAll
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}

		if d.IsDir() {
			if name == ".git" || name == "node_modules" || (name == "worktrees" && filepath.Base(filepath.Dir(path)) == ".mcode") || isCredentialDirectoryName(name) || isProtectedPathString(rel) {
				return filepath.SkipDir
			}
		}

		if isProtected(rel, path) || isGitRelated(rel, path) {
			return nil
		}

		if fuzzyMatch(query, name) || fuzzyMatch(query, rel) {
			lfi, lerr := os.Lstat(path)
			if lerr != nil {
				return nil
			}

			var kind wire.RemoteFileEntryKind
			var size int64
			if d.IsDir() {
				kind = wire.RemoteFileEntryKindDirectory
				size = 0
			} else if lfi.Mode()&os.ModeSymlink != 0 {
				kind = wire.RemoteFileEntryKindSymlink
				size = lfi.Size()
			} else {
				kind = wire.RemoteFileEntryKindFile
				size = lfi.Size()
			}

			matches = append(matches, wire.RemoteFileEntry{
				Name:      name,
				Path:      rel,
				Kind:      kind,
				Size:      size,
				Ignored:   false,
				Protected: false,
			})

			if len(matches) >= maxResults {
				truncated = true
				return filepath.SkipAll
			}
		}

		return nil
	})

	if err != nil && err != filepath.SkipAll && err != context.Canceled {
		return wire.RemoteFilesSearchResult{}, &wire.RPCError{
			Code:    wire.InternalError,
			Message: fmt.Sprintf("search failed: %v", err),
		}
	}

	if len(matches) > 0 {
		paths := make([]string, len(matches))
		for i, m := range matches {
			paths[i] = m.Path
		}
		ignoredMap := gitCheckIgnored(ctx, root, paths)
		if ignoredMap != nil {
			for i := range matches {
				if ignoredMap[matches[i].Path] {
					matches[i].Ignored = true
				}
			}
		}
	}

	if matches == nil {
		matches = make([]wire.RemoteFileEntry, 0)
	}

	return wire.RemoteFilesSearchResult{
		Entries:   matches,
		Truncated: truncated,
	}, nil
}

// GitStatus 读取工作区 Git 仓库的本地状态摘要。
func (b *MemoryBackend) GitStatus(ctx context.Context, p wire.RemoteGitStatusParams) (wire.RemoteGitStatusResult, error) {
	root, err := b.threadWorkspaceRoot(p.ThreadID)
	if err != nil {
		return wire.RemoteGitStatusResult{}, err
	}

	isRepo := false
	if b.git != nil {
		_, gerr := b.git.RepoRoot(ctx, root)
		isRepo = (gerr == nil)
	} else {
		cmd := procattr.HideWindow(exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel"))
		cmd.Dir = root
		if err := cmd.Run(); err == nil {
			isRepo = true
		}
	}

	if !isRepo {
		return wire.RemoteGitStatusResult{
			ThreadID:       p.ThreadID,
			IsRepository:   false,
			Ahead:          0,
			Behind:         0,
			ModifiedFiles:  make([]string, 0),
			UntrackedFiles: make([]string, 0),
			IsClean:        false,
		}, nil
	}

	// 1. 获取当前分支
	var branchPtr *string
	if b.git != nil {
		br, _ := b.git.CurrentBranch(ctx, root)
		if br != "" {
			branchPtr = &br
		}
	} else {
		cmd := procattr.HideWindow(exec.CommandContext(ctx, "git", "branch", "--show-current"))
		cmd.Dir = root
		if out, err := cmd.Output(); err == nil {
			br := strings.TrimSpace(string(out))
			if br != "" {
				branchPtr = &br
			}
		}
	}

	// 2. 探测 upstream 分支
	var upstreamPtr *string
	uCmd := procattr.HideWindow(exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "@{u}"))
	uCmd.Dir = root
	if uOut, err := uCmd.Output(); err == nil {
		up := strings.TrimSpace(string(uOut))
		if up != "" {
			upstreamPtr = &up
		}
	}

	// 3. 计算 ahead / behind
	ahead := 0
	behind := 0
	if upstreamPtr != nil {
		cCmd := procattr.HideWindow(exec.CommandContext(ctx, "git", "rev-list", "--left-right", "--count", "HEAD...@{u}"))
		cCmd.Dir = root
		if cOut, err := cCmd.Output(); err == nil {
			parts := strings.Fields(strings.TrimSpace(string(cOut)))
			if len(parts) >= 2 {
				ahead, _ = strconv.Atoi(parts[0])
				behind, _ = strconv.Atoi(parts[1])
			}
		}
	}

	// 4. 解析 defaultBranch（纯本地探测，不触发任何网络 fetch）
	defaultBranch := "main"
	headCmd := procattr.HideWindow(exec.CommandContext(ctx, "git", "symbolic-ref", "--short", "-q", "refs/remotes/origin/HEAD"))
	headCmd.Dir = root
	if hOut, err := headCmd.Output(); err == nil {
		hStr := strings.TrimSpace(string(hOut))
		if strings.HasPrefix(hStr, "origin/") {
			defaultBranch = strings.TrimPrefix(hStr, "origin/")
		} else if hStr != "" {
			defaultBranch = hStr
		}
	} else {
		// 先看 origin 远端跟踪分支，再看本地 main / master
		resolved := false
		for _, prefix := range []string{"refs/remotes/origin/", "refs/heads/"} {
			for _, candidate := range []string{"main", "master"} {
				vCmd := procattr.HideWindow(exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--quiet", prefix+candidate))
				vCmd.Dir = root
				if vCmd.Run() == nil {
					defaultBranch = candidate
					resolved = true
					break
				}
			}
			if resolved {
				break
			}
		}
	}

	// 5. 获取文件状态变更列表（优先复用 internal/git 的 Status，不触发任何网络 fetch）
	var rawModified []string
	var rawUntracked []string

	var statusErr error
	if b.git != nil {
		entries, gerr := b.git.Status(ctx, root)
		if gerr != nil {
			statusErr = gerr
		} else {
			for _, e := range entries {
				if e.Path == "" {
					continue
				}
				if e.Index == '?' && e.Worktree == '?' {
					rawUntracked = append(rawUntracked, e.Path)
				} else {
					rawModified = append(rawModified, e.Path)
				}
			}
		}
	} else {
		sCmd := procattr.HideWindow(exec.CommandContext(ctx, "git", "status", "--porcelain=v1", "-z", "--untracked-files=all"))
		sCmd.Dir = root
		sOut, err := sCmd.Output()
		if err != nil {
			statusErr = err
		} else if len(sOut) > 0 {
			records := bytes.Split(sOut, []byte{0})
			for i := 0; i < len(records); i++ {
				rec := records[i]
				if len(rec) < 3 {
					continue
				}
				code := string(rec[:2])
				filePath := string(rec[3:])
				if filePath == "" {
					continue
				}

				if code == "??" {
					rawUntracked = append(rawUntracked, filePath)
				} else {
					rawModified = append(rawModified, filePath)
					if (strings.Contains(code, "R") || strings.Contains(code, "C")) && i+1 < len(records) {
						i++ // 跳过重命名/复制的源路径记录
					}
				}
			}
		}
	}
	if statusErr != nil {
		return wire.RemoteGitStatusResult{}, &wire.RPCError{
			Code:    wire.InternalError,
			Message: fmt.Sprintf("git status failed: %v", statusErr),
		}
	}

	isClean := len(rawModified) == 0 && len(rawUntracked) == 0
	truncated := false
	const maxFilesCount = 2000
	const maxFilesBytes = 512 * 1024

	// 截断文件数量
	if len(rawModified) > maxFilesCount {
		rawModified = rawModified[:maxFilesCount]
		truncated = true
	}
	if len(rawUntracked) > maxFilesCount {
		rawUntracked = rawUntracked[:maxFilesCount]
		truncated = true
	}

	// 截断体积
	currentBytes := 0
	var safeModified []string
	for _, f := range rawModified {
		bLen := len(f)
		if currentBytes+bLen > maxFilesBytes {
			truncated = true
			break
		}
		currentBytes += bLen
		safeModified = append(safeModified, f)
	}
	if len(safeModified) < len(rawModified) {
		rawModified = safeModified
		truncated = true
	}

	var safeUntracked []string
	for _, f := range rawUntracked {
		bLen := len(f)
		if currentBytes+bLen > maxFilesBytes {
			truncated = true
			break
		}
		currentBytes += bLen
		safeUntracked = append(safeUntracked, f)
	}
	if len(safeUntracked) < len(rawUntracked) {
		rawUntracked = safeUntracked
		truncated = true
	}

	if rawModified == nil {
		rawModified = make([]string, 0)
	}
	if rawUntracked == nil {
		rawUntracked = make([]string, 0)
	}

	var truncatedPtr *bool
	if truncated {
		tVal := true
		truncatedPtr = &tVal
	}

	return wire.RemoteGitStatusResult{
		ThreadID:       p.ThreadID,
		IsRepository:   true,
		Branch:         branchPtr,
		Upstream:       upstreamPtr,
		DefaultBranch:  &defaultBranch,
		Ahead:          ahead,
		Behind:         behind,
		ModifiedFiles:  rawModified,
		UntrackedFiles: rawUntracked,
		IsClean:        isClean,
		Truncated:      truncatedPtr,
	}, nil
}

// GitBranches 获取工作区本地与远程分支列表。
func (b *MemoryBackend) GitBranches(ctx context.Context, p wire.RemoteGitBranchesParams) (wire.RemoteGitBranchesResult, error) {
	root, err := b.threadWorkspaceRoot(p.ThreadID)
	if err != nil {
		return wire.RemoteGitBranchesResult{}, err
	}

	isRepo := false
	if b.git != nil {
		_, gerr := b.git.RepoRoot(ctx, root)
		isRepo = (gerr == nil)
	} else {
		cmd := procattr.HideWindow(exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel"))
		cmd.Dir = root
		if err := cmd.Run(); err == nil {
			isRepo = true
		}
	}

	if !isRepo {
		return wire.RemoteGitBranchesResult{
			ThreadID:     p.ThreadID,
			IsRepository: false,
			Local:        make([]string, 0),
			Remote:       make([]string, 0),
		}, nil
	}

	// 当前分支（优先复用 internal/git 的 CurrentBranch）
	var currentPtr *string
	if b.git != nil {
		if cur, err := b.git.CurrentBranch(ctx, root); err == nil && cur != "" {
			currentPtr = &cur
		}
	} else {
		cCmd := procattr.HideWindow(exec.CommandContext(ctx, "git", "symbolic-ref", "--short", "-q", "HEAD"))
		cCmd.Dir = root
		if cOut, err := cCmd.Output(); err == nil {
			cur := strings.TrimSpace(string(cOut))
			if cur != "" {
				currentPtr = &cur
			}
		}
	}

	// 本地分支
	var localBranches []string
	lCmd := procattr.HideWindow(exec.CommandContext(ctx, "git", "for-each-ref", "refs/heads", "--format=%(refname:short)"))
	lCmd.Dir = root
	if lOut, err := lCmd.Output(); err == nil {
		lines := strings.Split(string(lOut), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" {
				localBranches = append(localBranches, line)
			}
		}
	}

	// 远程分支
	var remoteBranches []string
	rCmd := procattr.HideWindow(exec.CommandContext(ctx, "git", "for-each-ref", "refs/remotes", "--format=%(refname:short)"))
	rCmd.Dir = root
	if rOut, err := rCmd.Output(); err == nil {
		lines := strings.Split(string(rOut), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" && line != "HEAD" && !strings.HasSuffix(line, "/HEAD") {
				remoteBranches = append(remoteBranches, line)
			}
		}
	}

	truncated := false
	const maxBranchesCount = 2000
	if len(localBranches) > maxBranchesCount {
		localBranches = localBranches[:maxBranchesCount]
		truncated = true
	}
	if len(remoteBranches) > maxBranchesCount {
		remoteBranches = remoteBranches[:maxBranchesCount]
		truncated = true
	}

	if localBranches == nil {
		localBranches = make([]string, 0)
	}
	if remoteBranches == nil {
		remoteBranches = make([]string, 0)
	}

	var truncatedPtr *bool
	if truncated {
		tVal := true
		truncatedPtr = &tVal
	}

	return wire.RemoteGitBranchesResult{
		ThreadID:     p.ThreadID,
		IsRepository: true,
		Current:      currentPtr,
		Local:        localBranches,
		Remote:       remoteBranches,
		Truncated:    truncatedPtr,
	}, nil
}

// --- Dispatcher Handlers ---

func (d *Dispatcher) memoryBackend() (*MemoryBackend, bool) {
	if d.backend == nil {
		return nil, false
	}
	mb, ok := d.backend.(*MemoryBackend)
	return mb, ok
}

func (d *Dispatcher) handleFilesList(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.RemoteFilesListParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if len(p.Subpath) > 4096 {
		return replyErr(wire.InvalidParams, "Invalid params: 'subpath' must be at most 4096 characters")
	}

	mb, ok := d.memoryBackend()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "workspace file operations unavailable")
	}
	res, err := mb.FilesList(ctx, p)
	if err != nil {
		return replyWithBackendErr(err)
	}
	return replySuccess(res)
}

func (d *Dispatcher) handleFilesRead(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.RemoteFilesReadParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if strings.TrimSpace(p.Path) == "" || len(p.Path) > 4096 {
		return replyErr(wire.InvalidParams, "Invalid params: 'path' is required and must be at most 4096 characters")
	}
	if p.Offset != nil && *p.Offset < 0 {
		return replyErr(wire.InvalidParams, "Invalid params: 'offset' must be non-negative")
	}
	if p.Length != nil && (*p.Length <= 0 || *p.Length > 512*1024) {
		return replyErr(wire.InvalidParams, "Invalid params: 'length' must be between 1 and 524288")
	}

	mb, ok := d.memoryBackend()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "workspace file operations unavailable")
	}
	res, err := mb.FilesRead(ctx, p)
	if err != nil {
		return replyWithBackendErr(err)
	}
	return replySuccess(res)
}

func (d *Dispatcher) handleFilesSearch(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.RemoteFilesSearchParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if len([]rune(strings.TrimSpace(p.Query))) > 256 {
		return replyErr(wire.InvalidParams, "Invalid params: 'query' must be at most 256 characters")
	}

	mb, ok := d.memoryBackend()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "workspace file operations unavailable")
	}
	res, err := mb.FilesSearch(ctx, p)
	if err != nil {
		return replyWithBackendErr(err)
	}
	return replySuccess(res)
}

func (d *Dispatcher) handleGitStatus(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.RemoteGitStatusParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}

	mb, ok := d.memoryBackend()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "git operations unavailable")
	}
	res, err := mb.GitStatus(ctx, p)
	if err != nil {
		return replyWithBackendErr(err)
	}
	return replySuccess(res)
}

func (d *Dispatcher) handleGitBranches(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.RemoteGitBranchesParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}

	mb, ok := d.memoryBackend()
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "git operations unavailable")
	}
	res, err := mb.GitBranches(ctx, p)
	if err != nil {
		return replyWithBackendErr(err)
	}
	return replySuccess(res)
}
