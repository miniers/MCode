package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
)

// ErrNoHead 表示仓库还没有任何提交。
var ErrNoHead = errors.New("仓库还没有任何提交")

// ErrNoChanges 表示工作区没有未提交的改动。
var ErrNoChanges = errors.New("没有检测到未提交的代码改动")

// CommitPatchResult 描述只基于补丁向 HEAD 提交的执行结果。
type CommitPatchResult struct {
	SHA        string   // 没有实际改动时为空
	Paths      []string // 本次提交涉及的路径
	Conflicts  []string // 补丁无法干净应用的路径
	StagedKept []string // 用户 index 里已暂存了其他内容、因此未同步的路径
}

// CommitPatchesOnHead 将指定补丁仅应用到 HEAD 并创建新提交，绝不修改用户工作区文件。
// 当用户在相同文件中存在未提交改动时，未提交改动在提交后仍保持未提交。
// 若仓库为空（无 HEAD），返回 ErrNoHead。
// 若存在冲突或无法应用，返回 Conflicts 且不修改仓库引用。
func (r *Runner) CommitPatchesOnHead(ctx context.Context, root string, patches [][]byte, message string) (CommitPatchResult, error) {
	return r.commitPatchesOnHead(ctx, root, patches, message, "")
}

// commitPatchesOnHead 是内部实现。expectedHead 非空时用于 CAS 比较基准，便于测试并发移动 HEAD 的场景。
func (r *Runner) commitPatchesOnHead(ctx context.Context, root string, patches [][]byte, message string, expectedHead string) (CommitPatchResult, error) {
	// (a) 解析 oldHead（rev-parse --verify HEAD），失败时返回 ErrNoHead
	var oldHead string
	if expectedHead != "" {
		oldHead = strings.TrimSpace(expectedHead)
	} else {
		out, err := r.run(ctx, root, nil, "rev-parse", "--verify", "HEAD")
		if err != nil {
			return CommitPatchResult{}, ErrNoHead
		}
		oldHead = strings.TrimSpace(string(out))
		if oldHead == "" {
			return CommitPatchResult{}, ErrNoHead
		}
	}

	// (b) 新建临时目录，以 GIT_INDEX_FILE=临时 index 执行 read-tree oldHead
	tmpDir, err := os.MkdirTemp("", "mcode-commit-patch-*")
	if err != nil {
		return CommitPatchResult{}, fmt.Errorf("create temp index dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tmpIndex := filepath.Join(tmpDir, "index")
	env := []string{"GIT_INDEX_FILE=" + tmpIndex}

	if _, err := r.run(ctx, root, env, "read-tree", oldHead); err != nil {
		return CommitPatchResult{}, fmt.Errorf("read-tree %s: %w", oldHead, err)
	}

	// (c) 对每个非空补丁，在同一个临时 index 上执行 git apply --cached --3way --whitespace=nowarn，补丁从 stdin 传入。
	// 冲突或失败时，收集涉及的路径到 Conflicts，直接返回，不创建 commit、不动任何 ref。
	for _, p := range patches {
		if len(bytes.TrimSpace(p)) == 0 {
			continue
		}
		patchData := p
		if len(patchData) > 0 && patchData[len(patchData)-1] != '\n' {
			patchData = append(bytes.Clone(patchData), '\n')
		}
		_, applyErr := r.runWithStdin(ctx, root, env, patchData, "apply", "--cached", "--3way", "--whitespace=nowarn", "-")
		if applyErr != nil {
			conflicts, _ := r.unmergedPaths(ctx, root, env)
			if len(conflicts) == 0 {
				conflicts = parseStderrPaths(applyErr.Error())
			}
			if len(conflicts) == 0 {
				conflicts = extractPatchPaths(patchData)
			}
			sort.Strings(conflicts)
			if conflicts == nil {
				conflicts = []string{}
			}
			return CommitPatchResult{
				Conflicts:  conflicts,
				Paths:      []string{},
				StagedKept: []string{},
			}, nil
		}
	}

	// (d) 执行 write-tree；如果 tree 等于 oldHead 的 tree，返回空 SHA
	treeOut, err := r.run(ctx, root, env, "write-tree")
	if err != nil {
		return CommitPatchResult{}, fmt.Errorf("write-tree: %w", err)
	}
	tree := strings.TrimSpace(string(treeOut))

	oldHeadTreeOut, err := r.run(ctx, root, nil, "rev-parse", oldHead+"^{tree}")
	if err != nil {
		return CommitPatchResult{}, fmt.Errorf("resolve oldHead tree: %w", err)
	}
	oldHeadTree := strings.TrimSpace(string(oldHeadTreeOut))

	if tree == oldHeadTree {
		return CommitPatchResult{
			Paths:      []string{},
			Conflicts:  []string{},
			StagedKept: []string{},
		}, nil
	}

	// (e) 执行 commit-tree <tree> -p oldHead -m message。
	// commit-tree 为底层的 plumbing 命令，不触发 pre-commit 等客户端钩子。
	// 作者和提交者身份使用用户自己的 git 配置（保持 env 为 nil），与 CommitPaths 一致，不强制注入 mcode 身份。
	newSHA, err := r.CommitTree(ctx, root, tree, oldHead, message)
	if err != nil {
		return CommitPatchResult{}, err
	}
	paths, err := r.DiffTreeNames(ctx, root, oldHead, newSHA)
	if err != nil {
		return CommitPatchResult{}, err
	}
	stagedKept, err := r.AdvanceHead(ctx, root, oldHead, newSHA)
	if err != nil {
		return CommitPatchResult{}, err
	}

	return CommitPatchResult{
		SHA:        newSHA,
		Paths:      paths,
		Conflicts:  []string{},
		StagedKept: stagedKept,
	}, nil
}

const patchBatchSize = 100

type indexMeta struct {
	mode string
	sha  string
}

// syncRealIndex 同步用户真实的索引文件：对 paths 中的每个路径，
// 比较用户真实 index 中的条目（ls-files -s）与 oldHead 中的条目（ls-tree）。
// 两者相同（或都不存在）时，说明用户没有额外暂存该路径，通过 git reset -q <new> -- <path>
// 将 index 条目同步为新提交内容；两者不同时保持用户暂存不动，记录到 StagedKept。
// 整个过程不触碰工作区文件。
func (r *Runner) syncRealIndex(ctx context.Context, root, oldHead, newSHA string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return []string{}, nil
	}

	var stagedKept []string
	var resetPaths []string

	for i := 0; i < len(paths); i += patchBatchSize {
		end := i + patchBatchSize
		if end > len(paths) {
			end = len(paths)
		}
		batch := paths[i:end]

		// 1. 查询真实 index 中这批路径的状态
		indexEntries, unmerged, err := r.queryIndexEntries(ctx, root, batch)
		if err != nil {
			return nil, err
		}

		// 2. 查询 oldHead 树中这批路径的状态
		headEntries, err := r.queryHeadEntries(ctx, root, oldHead, batch)
		if err != nil {
			return nil, err
		}

		// 3. 逐个路径比对
		for _, p := range batch {
			if unmerged[p] {
				// 真实索引中有冲突未决条目，用户暂存区处于特殊状态，予以保留
				stagedKept = append(stagedKept, p)
				continue
			}
			idx, idxOk := indexEntries[p]
			hd, hdOk := headEntries[p]

			if !idxOk && !hdOk {
				// 两者都不存在（例如补丁新增的文件且用户未暂存同名文件）：用户未暂存，需更新 index
				resetPaths = append(resetPaths, p)
			} else if idxOk && hdOk && idx.mode == hd.mode && idx.sha == hd.sha {
				// 两者相同：用户没有暂存新的内容，将 index 条目更新为新提交内容
				resetPaths = append(resetPaths, p)
			} else {
				// 两者不同：用户 index 中已暂存了其他内容，保持不动
				stagedKept = append(stagedKept, p)
			}
		}
	}

	// 4. 对无需保留用户暂存的路径，分批执行 git reset -q <new> -- <paths>
	for i := 0; i < len(resetPaths); i += patchBatchSize {
		end := i + patchBatchSize
		if end > len(resetPaths) {
			end = len(resetPaths)
		}
		batch := resetPaths[i:end]
		resetArgs := append([]string{"reset", "-q", newSHA, "--"}, batch...)
		if _, err := r.runWithLockRetry(ctx, root, nil, resetArgs...); err != nil {
			return stagedKept, fmt.Errorf("git reset paths: %w", err)
		}
	}

	sort.Strings(stagedKept)
	if stagedKept == nil {
		stagedKept = []string{}
	}
	return stagedKept, nil
}

// queryIndexEntries 查询真实索引中指定路径的模式、哈希与未合并状态
func (r *Runner) queryIndexEntries(ctx context.Context, root string, paths []string) (map[string]indexMeta, map[string]bool, error) {
	args := append([]string{"ls-files", "-s", "-z", "--"}, paths...)
	out, err := r.run(ctx, root, nil, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("ls-files: %w", err)
	}

	entries := make(map[string]indexMeta)
	unmerged := make(map[string]bool)

	for _, item := range strings.Split(string(out), "\x00") {
		if item == "" {
			continue
		}
		parts := strings.SplitN(item, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		filePath := parts[1]
		metaFields := strings.Fields(parts[0])
		if len(metaFields) < 3 {
			continue
		}
		mode := metaFields[0]
		sha := metaFields[1]
		stage := metaFields[2]

		if stage == "0" {
			entries[filePath] = indexMeta{mode: mode, sha: sha}
		} else {
			unmerged[filePath] = true
		}
	}
	return entries, unmerged, nil
}

// queryHeadEntries 查询指定树中路径的模式与对象哈希
func (r *Runner) queryHeadEntries(ctx context.Context, root, treeish string, paths []string) (map[string]indexMeta, error) {
	args := append([]string{"ls-tree", "-r", "-z", treeish, "--"}, paths...)
	out, err := r.run(ctx, root, nil, args...)
	if err != nil {
		return nil, fmt.Errorf("ls-tree: %w", err)
	}

	entries := make(map[string]indexMeta)
	for _, item := range strings.Split(string(out), "\x00") {
		if item == "" {
			continue
		}
		parts := strings.SplitN(item, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		filePath := parts[1]
		metaFields := strings.Fields(parts[0])
		if len(metaFields) < 3 {
			continue
		}
		mode := metaFields[0]
		sha := metaFields[2]
		entries[filePath] = indexMeta{mode: mode, sha: sha}
	}
	return entries, nil
}

// unmergedPaths 从临时 index 中读取三方合并冲突的高阶条目路径（git ls-files -u -z）
func (r *Runner) unmergedPaths(ctx context.Context, root string, env []string) ([]string, error) {
	out, err := r.run(ctx, root, env, "ls-files", "-u", "-z")
	if err != nil {
		return nil, err
	}
	var paths []string
	seen := make(map[string]bool)
	for _, item := range strings.Split(string(out), "\x00") {
		if item == "" {
			continue
		}
		parts := strings.SplitN(item, "\t", 2)
		if len(parts) == 2 {
			p := parts[1]
			if !seen[p] {
				seen[p] = true
				paths = append(paths, p)
			}
		}
	}
	return paths, nil
}

// parseStderrPaths 从 git apply 错误输出中提取报错的文件路径
func parseStderrPaths(errText string) []string {
	var paths []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(errText, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "error: patch failed: ") {
			target := strings.TrimPrefix(line, "error: patch failed: ")
			if idx := strings.LastIndex(target, ":"); idx != -1 {
				target = target[:idx]
			}
			target = strings.TrimSpace(target)
			if target != "" && !seen[target] {
				seen[target] = true
				paths = append(paths, target)
			}
		} else if strings.HasPrefix(line, "error: ") {
			target := strings.TrimPrefix(line, "error: ")
			if idx := strings.Index(target, ":"); idx != -1 {
				filePath := strings.TrimSpace(target[:idx])
				if filePath != "" && !seen[filePath] && !strings.Contains(filePath, " ") {
					seen[filePath] = true
					paths = append(paths, filePath)
				}
			}
		}
	}
	return paths
}

// extractPatchPaths 从 git 补丁文本中提取涉及的文件相对路径
func extractPatchPaths(patch []byte) []string {
	var paths []string
	seen := make(map[string]bool)
	addPath := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || p == "/dev/null" {
			return
		}
		if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
			p = strings.Trim(p, "\"")
		}
		p = filepath.Clean(filepath.ToSlash(p))
		if p == "." || p == "" {
			return
		}
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}

	lines := strings.Split(string(patch), "\n")
	for _, rawLine := range lines {
		line := strings.TrimRight(rawLine, "\r")
		switch {
		case strings.HasPrefix(line, "diff --git "):
			rest := strings.TrimPrefix(line, "diff --git ")
			parts := splitDiffGitHeader(rest)
			for _, part := range parts {
				addPath(part)
			}
		case strings.HasPrefix(line, "--- "):
			target := strings.TrimPrefix(line, "--- ")
			target = strings.TrimPrefix(target, "a/")
			addPath(target)
		case strings.HasPrefix(line, "+++ "):
			target := strings.TrimPrefix(line, "+++ ")
			target = strings.TrimPrefix(target, "b/")
			addPath(target)
		case strings.HasPrefix(line, "rename to "):
			target := strings.TrimPrefix(line, "rename to ")
			addPath(target)
		case strings.HasPrefix(line, "rename from "):
			target := strings.TrimPrefix(line, "rename from ")
			addPath(target)
		}
	}
	return paths
}

func splitDiffGitHeader(rest string) []string {
	var results []string
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return nil
	}
	if strings.HasPrefix(rest, "\"a/") {
		idx := strings.Index(rest[1:], "\"")
		if idx != -1 {
			p1 := rest[3 : idx+1]
			results = append(results, p1)
			rem := strings.TrimSpace(rest[idx+2:])
			if strings.HasPrefix(rem, "\"b/") {
				idx2 := strings.Index(rem[1:], "\"")
				if idx2 != -1 {
					results = append(results, rem[3:idx2+1])
				}
			}
			return results
		}
	}
	if strings.HasPrefix(rest, "a/") {
		if idx := strings.Index(rest, " b/"); idx != -1 {
			results = append(results, rest[2:idx])
			results = append(results, rest[idx+3:])
			return results
		}
	}
	return results
}

// runWithStdin 在指定目录下执行 git 命令，并将 stdin 传入子进程标准输入。
// 内部使用与 run 相同的信号量并发限制、环境配置与互斥锁逻辑。
func (r *Runner) runWithStdin(ctx context.Context, dir string, env []string, stdin []byte, args ...string) ([]byte, error) {
	if isRepoWriteCall(env, args) {
		defer lockRepo(dir)()
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	}

	cmd := procattr.HideWindow(exec.CommandContext(ctx, r.Bin, args...))
	cmd.Dir = dir
	if len(stdin) > 0 {
		cmd.Stdin = bytes.NewReader(stdin)
	}

	mergedEnv := append(loginenv.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	hasGitDir := false
	for _, item := range env {
		if strings.HasPrefix(item, "GIT_DIR=") {
			hasGitDir = true
			break
		}
	}
	if !hasGitDir {
		if sEnv := r.shadowEnv(dir); len(sEnv) > 0 {
			mergedEnv = append(mergedEnv, sEnv...)
		}
	}
	if len(env) > 0 {
		mergedEnv = append(mergedEnv, env...)
	}
	cmd.Env = mergedEnv

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		errText := strings.TrimSpace(stderr.String())
		if len(errText) > 500 {
			errText = errText[len(errText)-500:]
		}
		cmdName := "git"
		if len(args) > 0 {
			cmdName = "git " + args[0]
		}
		if errText != "" {
			return stdout.Bytes(), fmt.Errorf("%s: %w: %s", cmdName, err, errText)
		}
		return stdout.Bytes(), fmt.Errorf("%s: %w", cmdName, err)
	}

	return stdout.Bytes(), nil
}
