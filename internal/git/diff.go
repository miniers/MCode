package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// FileDiff 表示单个文件的 diff 详细信息。
type FileDiff struct {
	OldPath string // 原始文件路径（新增文件时为空）
	NewPath string // 变更后文件路径（删除文件时为空）
	Status  string // "added", "deleted", "modified", "renamed"
	Binary  bool   // 是否为二进制文件
	Hunks   []Hunk // 变更块列表

	// 延迟解析：单文件补丁超过阈值时不再拆分 Hunk，仅保留原文与增删行数，见 ExpandDeferred
	IsDeferred    bool   // 是否为延迟解析的大文件补丁
	DeferredPatch string // 延迟解析时保存的单文件原始补丁文本
	Additions     int    // 延迟解析时统计的新增行数
	Deletions     int    // 延迟解析时统计的删除行数
}

// Hunk 表示文件 diff 中的一个差异块。
type Hunk struct {
	OldStart int    // 变更前起始行号
	OldLines int    // 变更前行数
	NewStart int    // 变更后起始行号
	NewLines int    // 变更后行数
	Header   string // hunk 头部原始信息（如 "@@ -1,5 +1,6 @@"）
	Lines    []Line // 变更内容行列表
}

// Line 表示差异块中的单行内容。
type Line struct {
	Kind  byte   // 变更类型：' '（上下文行）、'+'（新增行）、'-'（删除行）
	Text  string // 除去首字符类型标记后的文本内容
	OldNo int    // 在原文件中的行号（新增行时为 0）
	NewNo int    // 在新文件中的行号（删除行时为 0）
}

// FileStat 表示单个文件的变更统计摘要。
type FileStat struct {
	Path      string // 文件当前路径
	OldPath   string // 重命名时的原始文件路径（非重命名时为空）
	Additions int    // 新增行数（二进制文件为 -1）
	Deletions int    // 删除行数（二进制文件为 -1）
	Binary    bool   // 是否为二进制文件
}

// captureWorkingTree 使用隔离的临时 index 将工作区的所有变更（包括未跟踪但未被忽略的新增文件）
// 快照为一棵临时 tree 对象并返回其哈希值，供 Diff 比较使用。全程不修改用户 index 和 HEAD。
func (r *Runner) captureWorkingTree(ctx context.Context, root string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "mcode-git-diff-tree-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir for working tree: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tmpIndex := filepath.Join(tmpDir, "index")
	env := []string{"GIT_INDEX_FILE=" + tmpIndex}

	if r.hasCommits(ctx, root) {
		if _, err := r.run(ctx, root, env, "read-tree", "HEAD"); err != nil {
			return "", fmt.Errorf("diff read-tree HEAD: %w", err)
		}
	}
	if _, err := r.run(ctx, root, env, "add", "-A", "--", "."); err != nil {
		return "", fmt.Errorf("diff add working tree: %w", err)
	}
	treeOut, err := r.run(ctx, root, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("diff write-tree: %w", err)
	}
	return strings.TrimSpace(string(treeOut)), nil
}

// maxPathspecChunk 限制单次 git diff 命令携带的 pathspec 数量，避免参数列表过长。
const maxPathspecChunk = 400

// diffArgs 构造 git diff 参数。paths 非空时在子命令前加 --literal-pathspecs（按字面量匹配路径），
// 并在两个版本之后追加 "--" 与路径列表；paths 为空时保持原有行为。
func diffArgs(from, to string, paths []string, numstat bool) []string {
	args := make([]string, 0, 12+len(paths))
	if len(paths) > 0 {
		args = append(args, "--literal-pathspecs")
	}
	args = append(args, "diff", "--no-color", "--no-ext-diff", "-M")
	if numstat {
		args = append(args, "--numstat", "-z")
	} else {
		args = append(args, "--unified=3")
	}
	args = append(args, from, to)
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	return args
}

// splitPathspecs 按 maxPathspecChunk 将路径列表分块，供调用方逐块执行后拼接结果。
func splitPathspecs(paths []string) [][]string {
	if len(paths) == 0 {
		return nil
	}
	chunks := make([][]string, 0, (len(paths)+maxPathspecChunk-1)/maxPathspecChunk)
	for len(paths) > 0 {
		n := len(paths)
		if n > maxPathspecChunk {
			n = maxPathspecChunk
		}
		chunks = append(chunks, paths[:n])
		paths = paths[n:]
	}
	return chunks
}

// Diff 比较两个 commit/ref 之间的差异。
// 当 to 为空时表示比较目标为当前工作区，此时会先通过临时 index 生成包含未跟踪文件的 tree 再执行 diff。
// paths 非空时仅比较这些路径（相对仓库根目录，按字面量匹配）；超过 400 条时分块执行并合并结果。
// 内部使用 `git diff --no-color --no-ext-diff -M --unified=3 from to` 并解析返回结构化的 FileDiff 列表。
func (r *Runner) Diff(ctx context.Context, root, from, to string, paths []string) ([]FileDiff, error) {
	targetTo := to
	if targetTo == "" {
		worktreeTree, err := r.captureWorkingTree(ctx, root)
		if err != nil {
			return nil, err
		}
		targetTo = worktreeTree
	}

	if len(paths) == 0 {
		out, err := r.run(ctx, root, nil, diffArgs(from, targetTo, nil, false)...)
		if err != nil {
			return nil, err
		}
		return ParseUnifiedDiff(out)
	}

	var diffs []FileDiff
	for _, chunk := range splitPathspecs(paths) {
		out, err := r.run(ctx, root, nil, diffArgs(from, targetTo, chunk, false)...)
		if err != nil {
			return nil, err
		}
		parsed, err := ParseUnifiedDiff(out)
		if err != nil {
			return nil, err
		}
		diffs = append(diffs, parsed...)
	}
	return diffs, nil
}

// NumStat 获取两个版本之间的文件统计数据（增删行数）。
// 当 to 为空时代表当前工作区。paths 非空时仅统计这些路径，超过 400 条时分块执行并合并结果。
// 二进制文件的 Additions 和 Deletions 设为 -1。
func (r *Runner) NumStat(ctx context.Context, root, from, to string, paths []string) ([]FileStat, error) {
	targetTo := to
	if targetTo == "" {
		worktreeTree, err := r.captureWorkingTree(ctx, root)
		if err != nil {
			return nil, err
		}
		targetTo = worktreeTree
	}

	if len(paths) == 0 {
		out, err := r.run(ctx, root, nil, diffArgs(from, targetTo, nil, true)...)
		if err != nil {
			return nil, err
		}
		return ParseNumStat(out)
	}

	var stats []FileStat
	for _, chunk := range splitPathspecs(paths) {
		out, err := r.run(ctx, root, nil, diffArgs(from, targetTo, chunk, true)...)
		if err != nil {
			return nil, err
		}
		parsed, err := ParseNumStat(out)
		if err != nil {
			return nil, err
		}
		stats = append(stats, parsed...)
	}
	return stats, nil
}

// ParseNumStat 解析 `git diff --numstat -z` 输出为结构化的 FileStat 切片。
func ParseNumStat(data []byte) ([]FileStat, error) {
	if len(data) == 0 {
		return []FileStat{}, nil
	}

	parts := bytes.Split(data, []byte{0})
	var stats []FileStat

	for i := 0; i < len(parts); i++ {
		part := parts[i]
		if len(part) == 0 {
			continue
		}

		fields := strings.SplitN(string(part), "\t", 3)
		if len(fields) < 3 {
			continue
		}

		addStr := fields[0]
		delStr := fields[1]
		pathStr := fields[2]

		oldPath := ""
		newPath := ""

		if pathStr == "" {
			// 在 --numstat -z 格式下，重命名的路径紧跟两个以 \0 结尾的字段
			if i+1 < len(parts) {
				i++
				oldPath = string(parts[i])
			}
			if i+1 < len(parts) {
				i++
				newPath = string(parts[i])
			}
		} else {
			newPath = pathStr
		}

		if addStr == "-" && delStr == "-" {
			stats = append(stats, FileStat{
				Path:      newPath,
				OldPath:   oldPath,
				Additions: -1,
				Deletions: -1,
				Binary:    true,
			})
		} else {
			add, _ := strconv.Atoi(addStr)
			del, _ := strconv.Atoi(delStr)
			stats = append(stats, FileStat{
				Path:      newPath,
				OldPath:   oldPath,
				Additions: add,
				Deletions: del,
				Binary:    false,
			})
		}
	}

	return stats, nil
}

// 单文件补丁延迟解析阈值：行数或字节数超过任一阈值时不再拆分 hunk，
// 仅标记 IsDeferred 并保留原文与增删行数，需要时由 ExpandDeferred 展开。
const (
	deferredPatchLineThreshold = 5000
	deferredPatchByteThreshold = 512 * 1024
)

// ParseUnifiedDiff 是纯函数，负责将 git diff 的统一差异输出解析为结构化的 FileDiff 列表。
// 正确处理 new/deleted/renamed/binary、转义路径、省略行数的 hunk 头以及无末尾换行标记。
// 单文件补丁超过延迟解析阈值时，仅设置 IsDeferred/DeferredPatch 并统计增删行数，不拆分 hunk。
func ParseUnifiedDiff(data []byte) ([]FileDiff, error) {
	return parseUnifiedDiff(data, true)
}

// parseUnifiedDiff 是 ParseUnifiedDiff 的内部实现；deferLarge 为 false 时对所有单文件补丁
// 都完整拆分 hunk（供 ExpandDeferred 展开延迟补丁时使用）。
func parseUnifiedDiff(data []byte, deferLarge bool) ([]FileDiff, error) {
	if len(data) == 0 {
		return []FileDiff{}, nil
	}

	lines := splitLines(data)
	var diffs []FileDiff

	var current *FileDiff
	var currentHunk *Hunk
	var oldNo, newNo int
	var fallbackOld, fallbackNew string

	// 当前单文件补丁的原文与统计，用于延迟解析
	var fileLines []string
	var fileLineCount, fileByteCount int
	var fileAdditions, fileDeletions int
	var inHunk bool

	flushHunk := func() {
		if currentHunk != nil && current != nil {
			current.Hunks = append(current.Hunks, *currentHunk)
			currentHunk = nil
		}
	}

	flushFile := func() {
		if current == nil {
			return
		}
		flushHunk()

		// 规范化文件状态
		if current.Status == "" {
			current.Status = "modified"
		}

		switch current.Status {
		case "added":
			current.OldPath = ""
			if current.NewPath == "" {
				current.NewPath = fallbackNew
			}
		case "deleted":
			current.NewPath = ""
			if current.OldPath == "" {
				current.OldPath = fallbackOld
			}
		case "renamed":
			if current.OldPath == "" {
				current.OldPath = fallbackOld
			}
			if current.NewPath == "" {
				current.NewPath = fallbackNew
			}
		default: // "modified"
			if current.OldPath == "" {
				current.OldPath = fallbackOld
			}
			if current.NewPath == "" {
				current.NewPath = fallbackNew
			}
			if current.OldPath == "" && current.NewPath != "" {
				current.OldPath = current.NewPath
			} else if current.NewPath == "" && current.OldPath != "" {
				current.NewPath = current.OldPath
			}
		}

		if current.IsDeferred {
			current.Hunks = []Hunk{}
			current.DeferredPatch = strings.Join(fileLines, "\n")
			current.Additions = fileAdditions
			current.Deletions = fileDeletions
		}

		diffs = append(diffs, *current)
		current = nil
		fallbackOld = ""
		fallbackNew = ""

		fileLines = nil
		fileLineCount = 0
		fileByteCount = 0
		fileAdditions = 0
		fileDeletions = 0
		inHunk = false
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			flushFile()
			fOld, fNew := parseDiffGitPaths(line)
			fallbackOld = fOld
			fallbackNew = fNew
			current = &FileDiff{
				OldPath: fOld,
				NewPath: fNew,
				Status:  "modified",
				Binary:  false,
				Hunks:   []Hunk{},
			}
			// 该行本身也是当前单文件补丁原文的一部分
			fileLines = append(fileLines, line)
			fileLineCount = 1
			fileByteCount = len(line) + 1
			continue
		}

		if current == nil {
			continue
		}

		// 累积单文件补丁原文并统计增删行数；超过阈值时切换为延迟解析
		fileLines = append(fileLines, line)
		fileLineCount++
		fileByteCount += len(line) + 1
		isHunkHeader := strings.HasPrefix(line, "@@")
		if isHunkHeader {
			inHunk = true
		} else if inHunk && len(line) > 0 {
			switch line[0] {
			case '+':
				fileAdditions++
			case '-':
				fileDeletions++
			}
		}
		if deferLarge && !current.IsDeferred && (fileLineCount > deferredPatchLineThreshold || fileByteCount > deferredPatchByteThreshold) {
			current.IsDeferred = true
			currentHunk = nil
			current.Hunks = []Hunk{}
		}

		// 处于文件头阶段（尚未进入 hunk）
		if currentHunk == nil && !isHunkHeader {
			if strings.HasPrefix(line, "new file mode") {
				current.Status = "added"
			} else if strings.HasPrefix(line, "deleted file mode") {
				current.Status = "deleted"
			} else if strings.HasPrefix(line, "rename from ") {
				current.Status = "renamed"
				current.OldPath = cleanDiffPath(line[len("rename from "):])
			} else if strings.HasPrefix(line, "rename to ") {
				current.Status = "renamed"
				current.NewPath = cleanDiffPath(line[len("rename to "):])
			} else if strings.HasPrefix(line, "Binary files ") || strings.HasPrefix(line, "GIT binary patch") {
				current.Binary = true
			} else if strings.HasPrefix(line, "--- ") {
				pathPart := strings.TrimSpace(line[4:])
				if pathPart == "/dev/null" {
					current.Status = "added"
				} else {
					current.OldPath = cleanDiffPath(pathPart)
				}
			} else if strings.HasPrefix(line, "+++ ") {
				pathPart := strings.TrimSpace(line[4:])
				if pathPart == "/dev/null" {
					current.Status = "deleted"
				} else {
					current.NewPath = cleanDiffPath(pathPart)
				}
			}
			continue
		}

		// Hunk 头部，例如 `@@ -1,5 +1,6 @@` 或 `@@ -1 +1 @@`
		if isHunkHeader {
			if current.IsDeferred {
				// 延迟解析：后续 hunk 不再拆分，仅保留原文与增删统计
				currentHunk = nil
				continue
			}
			flushHunk()
			oldStart, oldLines, newStart, newLines, err := parseHunkHeader(line)
			if err != nil {
				// 若 hunk 头无法识别则作为普通文本跳过
				continue
			}
			oldNo = oldStart
			newNo = newStart
			currentHunk = &Hunk{
				OldStart: oldStart,
				OldLines: oldLines,
				NewStart: newStart,
				NewLines: newLines,
				Header:   line,
				Lines:    []Line{},
			}
			continue
		}

		// 处理 Hunk 内部的行内容
		if currentHunk != nil {
			if len(line) == 0 {
				continue
			}
			prefix := line[0]
			switch prefix {
			case '+':
				currentHunk.Lines = append(currentHunk.Lines, Line{
					Kind:  '+',
					Text:  line[1:],
					OldNo: 0,
					NewNo: newNo,
				})
				newNo++
			case '-':
				currentHunk.Lines = append(currentHunk.Lines, Line{
					Kind:  '-',
					Text:  line[1:],
					OldNo: oldNo,
					NewNo: 0,
				})
				oldNo++
			case ' ':
				currentHunk.Lines = append(currentHunk.Lines, Line{
					Kind:  ' ',
					Text:  line[1:],
					OldNo: oldNo,
					NewNo: newNo,
				})
				oldNo++
				newNo++
			case '\\':
				// 例如 `\ No newline at end of file`，属于 diff 注记，不计入行号与代码行
				continue
			default:
				// 非预期字符，视为非 hunk 区域
			}
		}
	}

	flushFile()
	return diffs, nil
}

// ExpandDeferred 按需展开延迟解析的 FileDiff，解析 DeferredPatch 得到完整 hunks。
// 未标记 IsDeferred 的文件原样返回；展开结果保留延迟阶段的增删行数统计，
// 并清空 IsDeferred 与 DeferredPatch。
func ExpandDeferred(fd FileDiff) (FileDiff, error) {
	if !fd.IsDeferred {
		return fd, nil
	}
	if strings.TrimSpace(fd.DeferredPatch) == "" {
		return fd, fmt.Errorf("expand deferred diff %s: empty deferred patch", fd.NewPath)
	}

	parsed, err := parseUnifiedDiff([]byte(fd.DeferredPatch), false)
	if err != nil {
		return fd, fmt.Errorf("expand deferred diff %s: %w", fd.NewPath, err)
	}
	if len(parsed) != 1 {
		return fd, fmt.Errorf("expand deferred diff %s: parsed %d files, want 1", fd.NewPath, len(parsed))
	}

	expanded := parsed[0]
	expanded.Additions = fd.Additions
	expanded.Deletions = fd.Deletions
	return expanded, nil
}

// splitLines 将 byte 切片按行拆分，并去除换行符 \r 与 \n
func splitLines(data []byte) []string {
	var lines []string
	var start int
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			end := i
			if end > start && data[end-1] == '\r' {
				end--
			}
			lines = append(lines, string(data[start:end]))
			start = i + 1
		}
	}
	if start < len(data) {
		end := len(data)
		if end > start && data[end-1] == '\r' {
			end--
		}
		lines = append(lines, string(data[start:end]))
	}
	return lines
}

// parseHunkHeader 解析 `@@ -oldStart[,oldLines] +newStart[,newLines] @@` 头部。
// 若省略行数（例如 `@@ -1 +1 @@`），则默认对应行数为 1。
func parseHunkHeader(line string) (oldStart, oldLines, newStart, newLines int, err error) {
	if !strings.HasPrefix(line, "@@") {
		return 0, 0, 0, 0, fmt.Errorf("invalid hunk header: %s", line)
	}

	rest := strings.TrimPrefix(line, "@@")
	idx := strings.Index(rest, "@@")
	if idx == -1 {
		return 0, 0, 0, 0, fmt.Errorf("invalid hunk header: %s", line)
	}

	rangeStr := strings.TrimSpace(rest[:idx])
	parts := strings.Fields(rangeStr)
	if len(parts) < 2 {
		return 0, 0, 0, 0, fmt.Errorf("invalid hunk header ranges: %s", rangeStr)
	}

	// 解析旧版本行范围（形如 "-1,5" 或 "-1"）
	oldPart := strings.TrimPrefix(parts[0], "-")
	if strings.Contains(oldPart, ",") {
		sub := strings.SplitN(oldPart, ",", 2)
		oldStart, _ = strconv.Atoi(sub[0])
		oldLines, _ = strconv.Atoi(sub[1])
	} else {
		oldStart, _ = strconv.Atoi(oldPart)
		oldLines = 1
	}

	// 解析新版本行范围（形如 "+1,6" 或 "+1"）
	newPart := strings.TrimPrefix(parts[1], "+")
	if strings.Contains(newPart, ",") {
		sub := strings.SplitN(newPart, ",", 2)
		newStart, _ = strconv.Atoi(sub[0])
		newLines, _ = strconv.Atoi(sub[1])
	} else {
		newStart, _ = strconv.Atoi(newPart)
		newLines = 1
	}

	return oldStart, oldLines, newStart, newLines, nil
}

// parseDiffGitPaths 从 `diff --git <pathA> <pathB>` 行中解析出初步的旧路径与新路径。
// 支持解析带双引号与八进制转义的带空格路径。
func parseDiffGitPaths(line string) (string, string) {
	rest := strings.TrimSpace(strings.TrimPrefix(line, "diff --git "))
	if strings.HasPrefix(rest, "\"") {
		endIdx := findClosingQuote(rest, 0)
		if endIdx != -1 {
			oldRaw := rest[:endIdx+1]
			after := strings.TrimSpace(rest[endIdx+1:])
			if strings.HasPrefix(after, "\"") {
				newEndIdx := findClosingQuote(after, 0)
				if newEndIdx != -1 {
					newRaw := after[:newEndIdx+1]
					return cleanDiffPath(oldRaw), cleanDiffPath(newRaw)
				}
			}
			return cleanDiffPath(oldRaw), cleanDiffPath(after)
		}
	}

	if idx := strings.LastIndex(rest, " b/"); idx != -1 {
		oldPart := rest[:idx]
		newPart := rest[idx+len(" b/"):]
		return cleanDiffPath(oldPart), cleanDiffPath(newPart)
	}

	parts := strings.Fields(rest)
	if len(parts) >= 2 {
		return cleanDiffPath(parts[0]), cleanDiffPath(parts[1])
	}
	if len(parts) == 1 {
		p := cleanDiffPath(parts[0])
		return p, p
	}
	return "", ""
}

// findClosingQuote 寻找与指定起始引号匹配的闭合双引号索引，跳过转义符。
func findClosingQuote(s string, start int) int {
	for i := start + 1; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == '"' {
			return i
		}
	}
	return -1
}

// cleanDiffPath 清理 diff 中的路径表示，剥离两端双引号并反转义，去除前导 "a/" 或 "b/"，
// 最后做 Unicode NFC 规范化（与 TouchedPaths、PreviewRestore 的路径比较保持一致）。
func cleanDiffPath(p string) string {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "\"") && strings.HasSuffix(p, "\"") && len(p) >= 2 {
		p = unescapeGitPath(p[1 : len(p)-1])
	}
	if strings.HasPrefix(p, "a/") || strings.HasPrefix(p, "b/") {
		p = p[2:]
	}
	return NormalizePath(p)
}

// unescapeGitPath 对 Git 路径中的 C 风格反斜杠转义进行解码（支持 \t, \n, \\, \" 及八进制字节 \ooo）。
func unescapeGitPath(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}

	var buf bytes.Buffer
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			c := s[i]
			switch c {
			case 'n':
				buf.WriteByte('\n')
			case 't':
				buf.WriteByte('\t')
			case 'r':
				buf.WriteByte('\r')
			case '\\':
				buf.WriteByte('\\')
			case '"':
				buf.WriteByte('"')
			case '0', '1', '2', '3', '4', '5', '6', '7':
				val := int(c - '0')
				count := 1
				for count < 3 && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '7' {
					i++
					val = (val << 3) | int(s[i]-'0')
					count++
				}
				buf.WriteByte(byte(val))
			default:
				buf.WriteByte('\\')
				buf.WriteByte(c)
			}
		} else {
			buf.WriteByte(s[i])
		}
	}
	return buf.String()
}

// RawDiff 执行 `git diff --no-color <from>`（from 为空时用 HEAD），返回原始 unified diff。
func (r *Runner) RawDiff(ctx context.Context, root, from string) ([]byte, error) {
	targetFrom := from
	if targetFrom == "" {
		targetFrom = "HEAD"
	}
	return r.run(ctx, root, nil, "diff", "--no-color", targetFrom)
}
