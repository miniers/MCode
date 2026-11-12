package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ErrEmptySelection 表示所选范围中没有可应用的改动行。
var ErrEmptySelection = errors.New("所选范围没有改动行")

// LineSel 使用从 1 开始、包含两端的行号；Old 为 true 时选择旧侧。
type LineSel struct {
	Old        bool
	Start, End int
}

// StageFiles 暂存指定路径的新增、修改与删除。
func (r *Runner) StageFiles(ctx context.Context, root string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	_, err := r.run(ctx, root, []string{"GIT_LITERAL_PATHSPECS=1"}, append([]string{"add", "-A", "--"}, paths...)...)
	return err
}

// UnstageFiles 撤销指定路径的暂存，保留工作区内容。
func (r *Runner) UnstageFiles(ctx context.Context, root string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	args := []string{"restore", "--staged", "--"}
	if !r.hasCommits(ctx, root) {
		args = []string{"rm", "--cached", "-r", "-f", "--"}
	}
	_, err := r.run(ctx, root, []string{"GIT_LITERAL_PATHSPECS=1"}, append(args, paths...)...)
	return err
}

// DiscardFiles 保存快照后，把工作区恢复至索引；未跟踪文件直接删除。
func (r *Runner) DiscardFiles(ctx context.Context, root string, paths []string) (*Snapshot, error) {
	s, err := r.SnapshotPaths(ctx, root, paths)
	if err != nil {
		return nil, err
	}
	for _, file := range s.Files {
		tracked, err := r.run(ctx, s.Root, []string{"GIT_LITERAL_PATHSPECS=1"}, "ls-files", "-z", "--", file.Path)
		if err != nil {
			return s, err
		}
		if len(tracked) > 0 {
			_, err = r.run(ctx, s.Root, []string{"GIT_LITERAL_PATHSPECS=1"}, "restore", "--worktree", "--", file.Path)
		} else {
			err = ctx.Err()
			if err == nil {
				err = os.Remove(filepath.Join(s.Root, filepath.FromSlash(file.Path)))
				if os.IsNotExist(err) {
					err = nil
				}
			}
		}
		if err != nil {
			return s, fmt.Errorf("丢弃文件 %s: %w", file.Path, err)
		}
	}
	return s, nil
}

// CommitIndex 提交当前索引，amend 为 true 时修改最近一次提交。
func (r *Runner) CommitIndex(ctx context.Context, root, message string, amend bool) (string, error) {
	if strings.TrimSpace(message) == "" {
		return "", errors.New("提交信息不能为空")
	}
	args := []string{"commit", "-m", message}
	if amend {
		args = append(args, "--amend")
	}
	if _, err := r.run(ctx, root, nil, args...); err != nil {
		return "", err
	}
	out, err := r.run(ctx, root, nil, "rev-parse", "HEAD")
	return strings.TrimSpace(string(out)), err
}

// LastCommitMessage 返回最近一次提交的完整信息，去掉 Git 输出的末尾换行。
func (r *Runner) LastCommitMessage(ctx context.Context, root string) (string, error) {
	out, err := r.run(ctx, root, nil, "log", "-1", "--format=%B")
	return strings.TrimRight(string(out), "\n"), err
}

// HeadPushed 检查本地已知的任一远端分支是否包含 HEAD。
func (r *Runner) HeadPushed(ctx context.Context, root string) (bool, error) {
	out, err := r.run(ctx, root, nil, "branch", "-r", "--contains", "HEAD")
	return strings.TrimSpace(string(out)) != "", err
}

var partialHunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(.*)$`)

// PartialPatch 只保留所选改动；reverse 时以新侧为不变基准生成可反向应用的补丁。
func PartialPatch(filePatch []byte, sels []LineSel, reverse bool) ([]byte, error) {
	for _, sel := range sels {
		if sel.Start < 1 || sel.End < sel.Start {
			return nil, errors.New("选择行号必须从 1 开始且范围有效")
		}
	}
	selected := func(old bool, line int) bool {
		for _, sel := range sels {
			if sel.Old == old && line >= sel.Start && line <= sel.End {
				return true
			}
		}
		return false
	}
	lines := strings.Split(strings.TrimSuffix(string(filePatch), "\n"), "\n")
	var header, hunks []string
	delta, totalOld, totalNew := 0, 0, 0
	for i := 0; i < len(lines); {
		if !strings.HasPrefix(lines[i], "@@ ") {
			if len(hunks) > 0 {
				return nil, errors.New("补丁必须只包含一个文件")
			}
			header = append(header, lines[i])
			i++
			continue
		}
		m := partialHunkHeader.FindStringSubmatch(lines[i])
		if m == nil {
			return nil, errors.New("补丁块头无效")
		}
		nums := [4]int{0, 1, 0, 1}
		for j := range nums {
			if m[j+1] != "" {
				var err error
				nums[j], err = strconv.Atoi(m[j+1])
				if err != nil {
					return nil, errors.New("补丁行号超出范围")
				}
			}
		}
		oldLine, newLine := nums[0], nums[2]
		if nums[1] == 0 {
			oldLine++
		}
		if nums[3] == 0 {
			newLine++
		}
		oldStart, newStart := oldLine, newLine
		if reverse {
			oldStart = newStart - delta
		} else {
			newStart = oldStart + delta
		}
		oldCount, newCount, changes := 0, 0, 0
		seenOld, seenNew := 0, 0
		var body []string
		kept := false
		i++
		for i < len(lines) && !strings.HasPrefix(lines[i], "@@ ") {
			line := lines[i]
			i++
			if line == `\ No newline at end of file` {
				if kept {
					body = append(body, line)
				}
				continue
			}
			if line == "" {
				return nil, errors.New("补丁包含无效空行")
			}
			kind := line[0]
			keep, outKind := true, kind
			switch kind {
			case ' ':
				oldLine++
				newLine++
				seenOld++
				seenNew++
			case '-':
				if !selected(true, oldLine) {
					if reverse {
						keep = false
					} else {
						outKind = ' '
					}
				}
				oldLine++
				seenOld++
			case '+':
				if !selected(false, newLine) {
					if reverse {
						outKind = ' '
					} else {
						keep = false
					}
				}
				newLine++
				seenNew++
			default:
				return nil, errors.New("补丁包含不支持的内容")
			}
			kept = keep
			if !keep {
				continue
			}
			body = append(body, string(outKind)+line[1:])
			if outKind != '+' {
				oldCount++
			}
			if outKind != '-' {
				newCount++
			}
			if outKind != ' ' {
				changes++
			}
		}
		if seenOld != nums[1] || seenNew != nums[3] {
			return nil, errors.New("补丁块行数与块头不一致")
		}
		if changes == 0 {
			continue
		}
		if oldCount == 0 {
			oldStart--
		}
		if newCount == 0 {
			newStart--
		}
		hunks = append(hunks, fmt.Sprintf("@@ -%d,%d +%d,%d @@%s", oldStart, oldCount, newStart, newCount, m[5]))
		hunks = append(hunks, body...)
		delta += newCount - oldCount
		totalOld += oldCount
		totalNew += newCount
	}
	if len(hunks) == 0 {
		return nil, ErrEmptySelection
	}
	// 部分删除保留文件，部分反向新增也保留文件，不能沿用整文件创建/删除头。
	var oldPath, newPath string
	for _, line := range header {
		if strings.HasPrefix(line, "--- ") {
			oldPath = strings.TrimPrefix(line, "--- ")
		}
		if strings.HasPrefix(line, "+++ ") {
			newPath = strings.TrimPrefix(line, "+++ ")
		}
	}
	if oldPath == "" || newPath == "" {
		return nil, errors.New("缺少补丁文件头")
	}
	var out []string
	for _, line := range header {
		if strings.HasPrefix(line, "index ") {
			continue
		}
		if oldPath == "/dev/null" && totalOld > 0 {
			if strings.HasPrefix(line, "new file mode ") {
				continue
			}
			if line == "--- /dev/null" {
				line = "--- " + strings.Replace(newPath, "b/", "a/", 1)
			}
		}
		if newPath == "/dev/null" && totalNew > 0 {
			if strings.HasPrefix(line, "deleted file mode ") {
				continue
			}
			if line == "+++ /dev/null" {
				line = "+++ " + strings.Replace(oldPath, "a/", "b/", 1)
			}
		}
		out = append(out, line)
	}
	return []byte(strings.Join(append(out, hunks...), "\n") + "\n"), nil
}

func (r *Runner) applySelectedLines(ctx context.Context, root, path string, sels []LineSel, cached, reverse bool) error {
	if len(sels) == 0 {
		return ErrEmptySelection
	}
	rel, err := snapshotPath(root, path)
	if err != nil {
		return err
	}
	env := []string{"GIT_LITERAL_PATHSPECS=1"}
	if !cached && !reverse {
		tracked, err := r.run(ctx, root, env, "ls-files", "-z", "--", rel)
		if err != nil {
			return err
		}
		if len(tracked) == 0 {
			if _, err := r.run(ctx, root, env, "add", "-N", "--", rel); err != nil {
				return err
			}
		}
	}
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--src-prefix=a/", "--dst-prefix=b/"}
	if cached {
		args = append(args, "--cached")
	}
	patch, err := r.run(ctx, root, env, append(args, "--", rel)...)
	if err != nil {
		return err
	}
	patch, err = PartialPatch(patch, sels, reverse)
	if err != nil {
		return err
	}
	args = []string{"apply", "--unidiff-zero", "--whitespace=nowarn"}
	if cached || !reverse {
		args = append(args, "--cached")
	}
	if reverse {
		args = append(args, "--reverse")
	}
	_, err = r.runWithStdin(ctx, root, nil, patch, args...)
	return err
}

// StageLines 暂存工作区中所选的改动行。
func (r *Runner) StageLines(ctx context.Context, root, path string, sels []LineSel) error {
	return r.applySelectedLines(ctx, root, path, sels, false, false)
}

// UnstageLines 从索引中撤销所选的改动行。
func (r *Runner) UnstageLines(ctx context.Context, root, path string, sels []LineSel) error {
	return r.applySelectedLines(ctx, root, path, sels, true, true)
}

// DiscardLines 保存快照后丢弃工作区中所选的改动行。
func (r *Runner) DiscardLines(ctx context.Context, root, path string, sels []LineSel) (*Snapshot, error) {
	s, err := r.SnapshotPaths(ctx, root, []string{path})
	if err != nil {
		return nil, err
	}
	err = r.applySelectedLines(ctx, root, path, sels, false, true)
	return s, err
}
