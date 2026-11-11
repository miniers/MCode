package remote

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash"
	"hash/fnv"
	"os"
	"path/filepath"
)

// WorkspaceGeneration 返回会话工作区的变化代次。
// 工作区代次由监听器给出；Git 仓库再混入 HEAD 与 index 的元数据，
// 这样只改暂存区或提交、不改工作区文件时也能被发现。
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
	writeFileStamp(h, filepath.Join(filepath.Dir(indexPath), "HEAD"))
	writeFileStamp(h, indexPath)
	return h.Sum64(), nil
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
