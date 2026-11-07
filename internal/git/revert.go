package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// cleanAndVerifyPath 检查并规范化路径，确保其严格位于仓库根目录 root 内，
// 防止通过 ".."、绝对路径越界或非法路径逃逸出仓库，返回规范化后的仓库相对路径。
func cleanAndVerifyPath(root, p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("empty path is invalid")
	}

	cleanRoot := filepath.Clean(root)
	var full string

	if filepath.IsAbs(p) {
		full = filepath.Clean(p)
	} else {
		full = filepath.Join(cleanRoot, filepath.Clean(p))
	}

	rel, err := filepath.Rel(cleanRoot, full)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return "", fmt.Errorf("path %q escapes repository root %q", p, root)
	}

	return filepath.ToSlash(rel), nil
}

// RestoreFiles 将工作区中的指定路径文件恢复至 commit 检查点中的状态。
// 如果指定路径在检查点中存在，则按 ls-tree 给出的模式还原其原始内容：
// 普通文件写回内容并还原可执行位，符号链接重建为链接；子模块无法用文件方式还原，跳过并记录。
// 如果在检查点中不存在（说明是该检查点之后新增的未跟踪/已跟踪文件），则从工作区中将其删除。
//
// 实现方案选择说明：
// 此处选择通过 `git show <commit>:<path>` 读取内容并由 Go 直接写入工作区文件，而不是使用 `git checkout`，理由如下：
//  1. 绝对避免暂存区污染：`git checkout` 即使配置临时 GIT_INDEX_FILE，仍有触发 Git 内部状态竞争或触碰用户暂存区的风险；
//     而 `git show` 是纯粹针对目标对象的只读提取，完全不依赖任何 index 文件，从架构根源上杜绝了对暂存区的污染与 index.lock 冲突。
//  2. 干净处理新增文件的删除：若使用 `git checkout`，对检查点中不存在的文件会直接报错退出；而使用只读探测后，
//     不存在时直接调用 os.Remove 删除新增文件，逻辑原子且清晰。
//  3. 文件类型精确还原：配合 `git ls-tree` 可以准确获取提交中记录的文件模式，普通文件按 100644/100755
//     写回并还原可执行位，符号链接（120000）重建为链接，子模块（160000）跳过，避免把链接目标或子模块
//     的 commit 对象当成普通文件内容写进工作区；ls-tree 本身出错时向上返回，而不是当作「检查点中不存在」。
//
// 注意：RestoreFiles 会强制按 commit 还原路径的内容与存在性，不做任何二次修改校验；
// 上层调用前必须先经过 PreviewRestore 预览，并仅传入其 Restorable() 结果，
// 避免覆盖检查点之后用户或其他 Agent 在同一文件上写下的新内容。
func (r *Runner) RestoreFiles(ctx context.Context, root, commit string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}

	cleanRoot := filepath.Clean(root)

	for _, p := range paths {
		relPath, err := cleanAndVerifyPath(cleanRoot, p)
		if err != nil {
			return err
		}

		fullPath := filepath.Join(cleanRoot, filepath.FromSlash(relPath))

		// ls-tree 同时给出该路径在检查点中的存在性与文件模式（100644/100755/120000/160000）。
		entry, exists, treeErr := r.lsTreePath(ctx, cleanRoot, commit, relPath)
		if treeErr != nil {
			return fmt.Errorf("restore read %s mode from commit %s: %w", relPath, commit, treeErr)
		}
		if !exists {
			// 文件在检查点中不存在，代表该文件为该检查点之后创建的新增文件，应将其从工作区移除
			if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove post-checkpoint added file %s: %w", relPath, err)
			}
			continue
		}

		switch entry.Mode {
		case "160000":
			// 子模块：撤销不递归改动其内容，跳过并记录，避免把 commit 对象当文件内容写进工作区
			log.Printf("git: 跳过检查点中的子模块 %s，撤销不会改动子模块", relPath)
			continue
		case "040000":
			log.Printf("git: 跳过检查点中的目录条目 %s", relPath)
			continue
		case "120000":
			// 符号链接：blob 内容是链接目标，重建链接而不是写普通文件
			target, showErr := r.run(ctx, cleanRoot, nil, "show", fmt.Sprintf("%s:%s", commit, relPath))
			if showErr != nil {
				return fmt.Errorf("restore read symlink %s from commit %s: %w", relPath, commit, showErr)
			}
			if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
				return fmt.Errorf("restore mkdir for %s: %w", relPath, err)
			}
			if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("restore replace %s with symlink: %w", relPath, err)
			}
			if err := os.Symlink(strings.TrimSuffix(string(target), "\n"), fullPath); err != nil {
				return fmt.Errorf("restore symlink %s: %w", relPath, err)
			}
			continue
		}

		// 普通文件：读取内容并还原
		content, showErr := r.run(ctx, cleanRoot, nil, "show", fmt.Sprintf("%s:%s", commit, relPath))
		if showErr != nil {
			return fmt.Errorf("restore read %s from commit %s: %w", relPath, commit, showErr)
		}

		// 检查原始文件权限模式
		fileMode := os.FileMode(0644)
		if entry.Mode == "100755" {
			fileMode = 0755
		}

		// 确保父目录存在
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			return fmt.Errorf("restore mkdir for %s: %w", relPath, err)
		}

		// 写回文件内容；os.WriteFile 的权限位只在新文件上生效，已存在的文件需单独 chmod 才能还原可执行位
		if err := os.WriteFile(fullPath, content, fileMode); err != nil {
			return fmt.Errorf("restore write file %s: %w", relPath, err)
		}
		if err := os.Chmod(fullPath, fileMode); err != nil {
			return fmt.Errorf("restore chmod %s: %w", relPath, err)
		}
	}

	return nil
}

// treeEntry 是 `git ls-tree <commit> -- <path>` 返回的单条记录中的文件模式（如 100644/120000/160000）。
type treeEntry struct {
	Mode string
}

// lsTreePath 读取 commit 中单个路径的 ls-tree 条目；路径不在该提交中时 exists=false。
// GIT_LITERAL_PATHSPECS 让文件名中的 '*'、'?' 等按字面匹配。
func (r *Runner) lsTreePath(ctx context.Context, root, commit, path string) (treeEntry, bool, error) {
	out, err := r.run(ctx, root, []string{"GIT_LITERAL_PATHSPECS=1"}, "ls-tree", "-z", commit, "--", path)
	if err != nil {
		return treeEntry{}, false, err
	}
	for _, record := range bytes.Split(out, []byte{0}) {
		tab := bytes.IndexByte(record, '\t')
		if tab < 0 {
			continue
		}
		fields := bytes.Fields(record[:tab])
		if len(fields) != 3 {
			continue
		}
		return treeEntry{Mode: string(fields[0])}, true, nil
	}
	return treeEntry{}, false, nil
}

// TouchedPaths 提取 FileDiff 切片中所涉及的所有文件路径（包含重命名场景下的 OldPath 和 NewPath），
// 路径先做 Unicode NFC 规范化（见 NormalizePath），再完成去重和升序排序后返回。
func TouchedPaths(diffs []FileDiff) []string {
	seen := make(map[string]struct{})
	for _, d := range diffs {
		if d.OldPath != "" {
			seen[NormalizePath(d.OldPath)] = struct{}{}
		}
		if d.NewPath != "" {
			seen[NormalizePath(d.NewPath)] = struct{}{}
		}
	}

	result := make([]string, 0, len(seen))
	for p := range seen {
		result = append(result, p)
	}

	sort.Strings(result)
	return result
}

// NormalizePath 将路径的 Unicode 序列规范化为 NFC（预组合）形式。
// macOS 文件系统与 git 对中文等含组合字符的文件名可能分别使用 NFD/NFC 两种形式，
// 统一为 NFC 后，跨来源的路径比较与去重才能对齐；非 UTF-8 的原始字节路径原样返回。
func NormalizePath(p string) string {
	if p == "" || !utf8.ValidString(p) {
		return p
	}
	return norm.NFC.String(p)
}

// RestoreFile 描述撤销预览中的单个文件。
type RestoreFile struct {
	Path      string // 仓库相对路径（diff 给出的原始形式，可直接交给 git 使用）
	Additions int    // base 到 finish 区间的新增行数（二进制文件记 0）
	Deletions int    // base 到 finish 区间的删除行数（二进制文件记 0）
	IsKept    bool   // 该文件在终态之后又被修改过：回退时必须原样保留
	InBase    bool   // 该路径在 base 检查点中存在（否则为区间内新增，回退时删除）
}

// RestorePreview 是执行回退前的差异预览，记录比较所用的 base/finish 检查点。
type RestorePreview struct {
	Base   string
	Finish string
	Files  []RestoreFile
}

// Restorable 返回将被还原的仓库相对路径（即未被二次修改保护的文件）。
func (p *RestorePreview) Restorable() []string {
	paths := make([]string, 0, len(p.Files))
	for _, file := range p.Files {
		if !file.IsKept {
			paths = append(paths, file.Path)
		}
	}
	return paths
}

// Kept 返回因终态之后又被修改而必须保留、不参与还原的仓库相对路径。
func (p *RestorePreview) Kept() []string {
	paths := make([]string, 0, len(p.Files))
	for _, file := range p.Files {
		if file.IsKept {
			paths = append(paths, file.Path)
		}
	}
	return paths
}

// PreviewRestore 预览把工作区从 base 检查点回退到 finish 终态所涉及的文件变动。
// 变动文件与增删行数来自 `git diff --numstat -z --no-renames base finish`（二进制文件按 0 行计）。
// 二次修改保护：若某文件在 finish 中的 blob 与当前工作区中的 blob 不一致，说明它在轮次结束后
// 又被用户或其他 Agent 修改过，标记 IsKept=true，该文件不参与回退。
// InBase 表示路径在 base 检查点中是否存在；不存在即区间内新增的文件，回退时会被删除。
// 越界路径（逃出仓库根目录）直接跳过。
func (r *Runner) PreviewRestore(ctx context.Context, root, base, finish string) (*RestorePreview, error) {
	numstatOut, err := r.run(ctx, root, nil, "diff", "--numstat", "-z", "--no-renames", base, finish)
	if err != nil {
		return nil, fmt.Errorf("preview restore numstat: %w", err)
	}
	stats, err := ParseNumStat(numstatOut)
	if err != nil {
		return nil, fmt.Errorf("preview restore parse numstat: %w", err)
	}

	preview := &RestorePreview{Base: base, Finish: finish, Files: []RestoreFile{}}
	seen := make(map[string]struct{}, len(stats))
	var paths []string
	for _, stat := range stats {
		relPath, err := cleanAndVerifyPath(root, stat.Path)
		if err != nil {
			continue
		}
		key := NormalizePath(relPath)
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		paths = append(paths, relPath)

		additions, deletions := stat.Additions, stat.Deletions
		if stat.Binary || additions < 0 || deletions < 0 {
			additions, deletions = 0, 0
		}
		preview.Files = append(preview.Files, RestoreFile{
			Path:      relPath,
			Additions: additions,
			Deletions: deletions,
		})
	}
	if len(paths) == 0 {
		return preview, nil
	}

	leftBlobs, err := r.treeBlobs(ctx, root, finish, paths)
	if err != nil {
		return nil, fmt.Errorf("preview restore read finish blobs: %w", err)
	}
	nowBlobs, err := r.workingBlobs(ctx, root, paths)
	if err != nil {
		return nil, fmt.Errorf("preview restore hash worktree files: %w", err)
	}
	baseBlobs, err := r.treeBlobs(ctx, root, base, paths)
	if err != nil {
		return nil, fmt.Errorf("preview restore read base blobs: %w", err)
	}

	for i := range preview.Files {
		key := NormalizePath(preview.Files[i].Path)
		preview.Files[i].IsKept = leftBlobs[key] != nowBlobs[key]
		_, inBase := baseBlobs[key]
		preview.Files[i].InBase = inBase
	}
	return preview, nil
}

// ExecuteRevert 在 PreviewRestore 的二次修改保护之下执行回退：
// 先取回预览，再仅对 Restorable() 中的路径调用 RestoreFiles 还原
// （路径在 base 中存在的还原内容，不存在的作为区间内新增文件删除），
// IsKept 的文件保持工作区中的用户版本不变。返回预览结果供上层展示。
func (r *Runner) ExecuteRevert(ctx context.Context, root, base, finish string) (*RestorePreview, error) {
	preview, err := r.PreviewRestore(ctx, root, base, finish)
	if err != nil {
		return nil, err
	}
	if err := r.RestoreFiles(ctx, root, base, preview.Restorable()); err != nil {
		return preview, fmt.Errorf("execute revert: %w", err)
	}
	return preview, nil
}

// treeBlobs 读取 ref 中给定路径对应的对象哈希（普通文件为 blob，子模块为 commit）。
// 返回的 map 以 NFC 规范化后的路径为键；路径在 ref 中不存在时不会出现在结果中。
// 命令分块执行以规避超长命令行，GIT_LITERAL_PATHSPECS 让文件名中的 '*'、'?' 等按字面匹配。
func (r *Runner) treeBlobs(ctx context.Context, root, ref string, paths []string) (map[string]string, error) {
	const chunkSize = 400
	result := make(map[string]string, len(paths))
	env := []string{"GIT_LITERAL_PATHSPECS=1"}
	for start := 0; start < len(paths); start += chunkSize {
		end := start + chunkSize
		if end > len(paths) {
			end = len(paths)
		}
		args := append([]string{"ls-tree", "-r", "-z", ref, "--"}, paths[start:end]...)
		out, err := r.run(ctx, root, env, args...)
		if err != nil {
			if _, chkErr := r.run(ctx, root, nil, "cat-file", "-e", ref); chkErr != nil {
				return nil, fmt.Errorf("%w: %s", ErrCheckpointPruned, ref)
			}
			return nil, err
		}
		for _, entry := range bytes.Split(out, []byte{0}) {
			tab := bytes.IndexByte(entry, '\t')
			if tab < 0 {
				continue
			}
			fields := bytes.Fields(entry[:tab])
			if len(fields) != 3 {
				continue
			}
			result[NormalizePath(string(entry[tab+1:]))] = string(fields[2])
		}
	}
	return result, nil
}

// workingBlobs 计算工作区中给定路径当前内容的 blob 哈希，返回以 NFC 规范化路径为键的 map。
// 仅统计存在且为普通文件的路径；目录、符号链接与缺失文件不会出现（视为工作区无此内容）。
func (r *Runner) workingBlobs(ctx context.Context, root string, paths []string) (map[string]string, error) {
	const chunkSize = 400
	result := make(map[string]string, len(paths))
	var existing []string
	for _, p := range paths {
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		existing = append(existing, p)
	}
	for start := 0; start < len(existing); start += chunkSize {
		end := start + chunkSize
		if end > len(existing) {
			end = len(existing)
		}
		args := append([]string{"hash-object", "--"}, existing[start:end]...)
		out, err := r.run(ctx, root, nil, args...)
		if err != nil {
			return nil, err
		}
		hashes := strings.Fields(string(out))
		if len(hashes) != end-start {
			return nil, fmt.Errorf("hash-object answered with %d hashes for %d files", len(hashes), end-start)
		}
		for i, hash := range hashes {
			result[NormalizePath(existing[start+i])] = hash
		}
	}
	return result, nil
}
