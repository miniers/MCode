package remote

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
)

// WorkspaceGeneration 返回会话工作区的变化代次。
// 工作区代次由监听器给出；Git 仓库再混入 HEAD、当前分支引用、packed-refs 与 index 的元数据，
// 这样只改暂存区或只移动分支引用（空提交、amend、rebase）、不改工作区文件时也能被发现。
// 非 Git 目录只返回监听器代次，不报错。
func (b *MemoryBackend) WorkspaceGeneration(ctx context.Context, threadID string) (uint64, error) {
	root, err := b.threadWorkspaceRoot(threadID)
	if err != nil {
		return 0, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	generation, err := b.watcherFor(root).Generation(ctx)
	if err != nil {
		return 0, err
	}

	b.mu.Lock()
	runner := b.git
	b.mu.Unlock()
	if runner == nil {
		return generation, nil
	}
	indexPath, err := runner.IndexPath(ctx, root)
	if err != nil {
		// 普通目录没有 Git 元数据，工作区代次本身就够用。
		return generation, nil
	}

	h := fnv.New64a()
	var encoded [8]byte
	binary.LittleEndian.PutUint64(encoded[:], generation)
	_, _ = h.Write(encoded[:])
	binary.LittleEndian.PutUint64(encoded[:], gitMetadataGeneration(indexPath))
	_, _ = h.Write(encoded[:])
	return h.Sum64(), nil
}

// gitMetadataGeneration 只返回 Git 元数据的代次，不含监听器代次，
// 便于验证提交只改分支引用时代次会变化，且不依赖文件系统事件。
func gitMetadataGeneration(indexPath string) uint64 {
	h := fnv.New64a()
	writeGitMetadataStamps(h, indexPath)
	return h.Sum64()
}

// writeGitMetadataStamps 把 HEAD、当前分支的 loose ref、packed-refs 与 index 的文件戳写进哈希。
// 分支引用的 loose ref 与 packed-refs 在仓库共用目录下：worktree 的 gitdir 通过 commondir 指回主仓库，
// 而空提交只重写 loose ref 或 packed-refs，HEAD 文件内容仍是 "ref: refs/heads/<分支>"。
func writeGitMetadataStamps(h hash.Hash64, indexPath string) {
	gitDir := filepath.Dir(indexPath)
	writeFileStamp(h, filepath.Join(gitDir, "HEAD"))
	if ref := headReference(gitDir); ref != "" {
		commonDir := commonGitDir(gitDir)
		writeFileStamp(h, filepath.Join(commonDir, filepath.FromSlash(ref)))
		writeFileStamp(h, filepath.Join(commonDir, "packed-refs"))
	}
	writeFileStamp(h, indexPath)
}

// headReference 返回 HEAD 指向的引用名（如 refs/heads/main）；
// 分离 HEAD、指向引用之外的内容或读取失败时返回空。
func headReference(gitDir string) string {
	data, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	const prefix = "ref: refs/"
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, prefix) {
		return ""
	}
	return strings.TrimPrefix(line, "ref: ")
}

// commonGitDir 返回仓库共用目录：gitdir 下有 commondir 时按其内容解析（相对路径相对 gitdir），
// 没有时共用目录就是 gitdir 本身。
func commonGitDir(gitDir string) string {
	data, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return gitDir
	}
	common := strings.TrimSpace(string(data))
	if common == "" {
		return gitDir
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitDir, common)
	}
	return filepath.Clean(common)
}

// writeFileStamp 把文件的大小与修改时间写进哈希；文件缺失时也写入稳定标记。
func writeFileStamp(h hash.Hash64, path string) {
	info, err := os.Stat(path)
	if err != nil {
		_, _ = fmt.Fprintf(h, "%s\x00missing\x00", path)
		return
	}
	_, _ = fmt.Fprintf(h, "%s\x00%d\x00%d\x00", path, info.Size(), info.ModTime().UnixNano())
}
