package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mcode/internal/fsutil"
)

// Snapshot 保存工作区文件的内容与存在性，不修改索引。
type Snapshot struct {
	Root  string
	Files []SnapshotFile
}

// SnapshotFile 的 Blob 为仓库对象库中的内容，Mode 保留权限与符号链接类型。
type SnapshotFile struct {
	Path    string
	Existed bool
	Blob    string
	Mode    os.FileMode
}

// snapshotPath 同时检查词法路径和父目录链接，避免原子写入穿越仓库边界。
func snapshotPath(root, path string) (string, error) {
	rel, err := cleanAndVerifyPath(root, path)
	if err != nil {
		return "", fmt.Errorf("快照路径无效: %w", err)
	}
	if rel == "." || rel == ".git" || strings.HasPrefix(rel, ".git/") {
		return "", fmt.Errorf("快照路径不允许指向仓库目录: %q", path)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("解析仓库路径: %w", err)
	}
	parent := filepath.Dir(filepath.Join(root, filepath.FromSlash(rel)))
	for {
		realParent, err := filepath.EvalSymlinks(parent)
		if os.IsNotExist(err) {
			parent = filepath.Dir(parent)
			continue
		}
		if err != nil {
			return "", fmt.Errorf("解析快照父目录: %w", err)
		}
		parentRel, err := cleanAndVerifyPath(realRoot, realParent)
		if err != nil {
			return "", fmt.Errorf("快照父目录超出仓库: %w", err)
		}
		if parentRel == ".git" || strings.HasPrefix(parentRel, ".git/") {
			return "", fmt.Errorf("快照父目录不允许指向 Git 元数据: %q", path)
		}
		return rel, nil
	}
}

// SnapshotPaths 将文件内容写入 Git 对象库，缺失文件只记录存在性。
func (r *Runner) SnapshotPaths(ctx context.Context, root string, paths []string) (*Snapshot, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("解析仓库路径: %w", err)
	}
	s := &Snapshot{Root: root}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rel, err := snapshotPath(root, path)
		if err != nil {
			return nil, err
		}
		file := SnapshotFile{Path: rel}
		full := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Lstat(full)
		if os.IsNotExist(err) {
			s.Files = append(s.Files, file)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("读取快照文件 %s: %w", rel, err)
		}
		file.Existed, file.Mode = true, info.Mode()
		var blob []byte
		if info.Mode()&os.ModeSymlink != 0 {
			target, readErr := os.Readlink(full)
			if readErr != nil {
				return nil, fmt.Errorf("读取链接 %s: %w", rel, readErr)
			}
			blob, err = r.runWithStdin(ctx, root, nil, []byte(target), "hash-object", "-w", "--stdin")
		} else if info.Mode().IsRegular() {
			// 不运行 clean 过滤器，撤销必须逐字节恢复工作区内容。
			blob, err = r.run(ctx, root, nil, "hash-object", "-w", "--no-filters", "--", rel)
		} else {
			return nil, fmt.Errorf("快照不支持此文件类型: %s", rel)
		}
		if err != nil {
			return nil, fmt.Errorf("保存快照 %s: %w", rel, err)
		}
		file.Blob = strings.TrimSpace(string(blob))
		s.Files = append(s.Files, file)
	}
	return s, nil
}

// RestoreSnapshot 从对象库原子恢复内容、权限与链接，不修改索引。
func (r *Runner) RestoreSnapshot(ctx context.Context, s *Snapshot) error {
	if s == nil {
		return fmt.Errorf("快照不能为空")
	}
	paths := make([]string, len(s.Files))
	for i, file := range s.Files {
		rel, err := snapshotPath(s.Root, file.Path)
		if err != nil {
			return err
		}
		paths[i] = rel
	}
	for i, file := range s.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		full := filepath.Join(s.Root, filepath.FromSlash(paths[i]))
		if !file.Existed {
			if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("删除快照中不存在的文件 %s: %w", file.Path, err)
			}
			continue
		}
		data, err := r.run(ctx, s.Root, nil, "cat-file", "blob", file.Blob)
		if err != nil {
			return fmt.Errorf("读取快照 %s: %w", file.Path, err)
		}
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			return fmt.Errorf("创建快照目录: %w", err)
		}
		info, err := os.Lstat(full)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if file.Mode&os.ModeSymlink != 0 || (info != nil && info.Mode()&os.ModeSymlink != 0) {
			if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("替换快照链接: %w", err)
			}
		}
		if file.Mode&os.ModeSymlink != 0 {
			err = os.Symlink(string(data), full)
		} else {
			err = fsutil.WriteFileAtomic(full, data, file.Mode.Perm())
		}
		if err != nil {
			return fmt.Errorf("恢复快照 %s: %w", file.Path, err)
		}
	}
	return nil
}
