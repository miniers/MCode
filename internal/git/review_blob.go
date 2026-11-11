package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ReviewMaxTextBytes 是每侧完整文本的上限，超过后不返回任何一侧的正文。
const ReviewMaxTextBytes = 2 * 1024 * 1024
const reviewGitTimeout = 30 * time.Second

// ReviewBlobFile 是两个固定版本之间的文件元数据。
type ReviewBlobFile struct {
	Path, OrigPath, Status    string
	OldHash, NewHash          string
	OldSize, NewSize          int64
	Binary, TooLarge, Missing bool
}

// ReviewRevision 固定引用对应的对象；空 HEAD 在未出生分支上对应空树。
func (r *Runner) ReviewRevision(ctx context.Context, root, revision string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, reviewGitTimeout)
	defer cancel()
	out, err := r.run(ctx, root, nil, "rev-parse", "--verify", "--end-of-options", revision+"^{object}")
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	if revision != "HEAD" || ctx.Err() != nil {
		return "", err
	}
	// 只有指向不存在分支的符号 HEAD 才是未出生分支。
	ref, e := r.run(ctx, root, nil, "symbolic-ref", "--quiet", "HEAD")
	if e != nil {
		return "", err
	}
	_, e = r.run(ctx, root, nil, "show-ref", "--verify", "--quiet", strings.TrimSpace(string(ref)))
	var exitErr *exec.ExitError
	if !errors.As(e, &exitErr) || exitErr.ExitCode() != 1 {
		return "", err
	}
	out, e = r.run(ctx, root, nil, "mktree")
	return strings.TrimSpace(string(out)), e
}

// ReviewWorkingTree 固定工作树版本，并记录当前未跟踪路径；不修改用户索引、HEAD 或引用。
func (r *Runner) ReviewWorkingTree(ctx context.Context, root string) (string, map[string]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, reviewGitTimeout)
	defer cancel()
	out, err := r.run(ctx, root, nil, "ls-files", "--others", "--exclude-standard", "-z", "--")
	if err != nil {
		return "", nil, err
	}
	untracked := make(map[string]bool)
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) > 0 {
			untracked[string(p)] = true
		}
	}
	tree, err := r.captureWorkingTree(ctx, root)
	return tree, untracked, err
}

// ReviewFiles 只读取对象大小与差异元数据，不把完整 blob 加载到 Go 内存。
func (r *Runner) ReviewFiles(ctx context.Context, root, base, head string, untracked map[string]bool) ([]ReviewBlobFile, error) {
	ctx, cancel := context.WithTimeout(ctx, reviewGitTimeout)
	defer cancel()
	out, err := r.run(ctx, root, nil, "diff", "--raw", "--no-abbrev", "--no-ext-diff", "--no-textconv", "-z", "-M", base, head, "--")
	if err != nil && ctx.Err() == nil {
		// 缺 blob 时重命名检测可能失败，仍可从树中读出路径与对象号。
		out, err = r.run(ctx, root, nil, "diff", "--raw", "--no-abbrev", "--no-ext-diff", "--no-textconv", "-z", "--no-renames", base, head, "--")
	}
	if err != nil {
		return nil, err
	}
	stats, statErr := r.NumStat(ctx, root, base, head, nil)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	binary := make(map[string]bool, len(stats))
	for _, s := range stats {
		binary[s.Path] = s.Binary
	}
	parts := bytes.Split(out, []byte{0})
	files := make([]ReviewBlobFile, 0)
	missingBlob := false
	for i := 0; i < len(parts) && len(parts[i]) > 0; {
		fields := strings.Fields(string(parts[i]))
		i++
		if len(fields) != 5 || i >= len(parts) {
			return nil, fmt.Errorf("无效的 Git 审阅元数据")
		}
		f := ReviewBlobFile{Path: string(parts[i]), OldHash: fields[2], NewHash: fields[3], Status: fields[4][:1]}
		i++
		if f.Status == "R" || f.Status == "C" {
			if i >= len(parts) {
				return nil, fmt.Errorf("缺少重命名目标")
			}
			f.OrigPath = f.Path
			f.Path = string(parts[i])
			i++
			f.Status = "R"
		}
		if strings.Trim(f.OldHash, "0") == "" {
			f.OldHash = ""
		}
		if strings.Trim(f.NewHash, "0") == "" {
			f.NewHash = ""
		}
		if untracked[f.Path] {
			f.Status = "?"
		}
		if f.Status == "T" {
			f.Status = "M"
		}
		if f.Status != "M" && f.Status != "A" && f.Status != "D" && f.Status != "R" && f.Status != "?" {
			return nil, fmt.Errorf("不支持的 Git 审阅状态 %s", f.Status)
		}
		f.Binary = binary[f.Path]
		for _, side := range []struct {
			hash string
			size *int64
		}{{f.OldHash, &f.OldSize}, {f.NewHash, &f.NewSize}} {
			if side.hash == "" {
				continue
			}
			size, e := r.run(ctx, root, nil, "cat-file", "-s", side.hash)
			if e != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				f.Missing = true
				continue
			}
			n, e := strconv.ParseInt(strings.TrimSpace(string(size)), 10, 64)
			if e != nil {
				return nil, e
			}
			*side.size = n
			if n > ReviewMaxTextBytes {
				f.TooLarge = true
			}
		}
		// 子模块记录不是 blob，不能把 commit 的正文当作文件内容。
		if fields[0] == ":160000" || fields[1] == "160000" {
			f.Missing = true
		}
		files = append(files, f)
		missingBlob = missingBlob || f.Missing
	}
	if statErr != nil && !missingBlob {
		return nil, statErr
	}
	return files, nil
}

// ReviewTexts 按需读取固定 blob；任一侧不可用时清空两侧，不返回片段冒充全文。
func (r *Runner) ReviewTexts(ctx context.Context, root string, f ReviewBlobFile) (oldText, newText string, binary, tooLarge, missing bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, reviewGitTimeout)
	defer cancel()
	binary, tooLarge, missing = f.Binary, f.TooLarge, f.Missing
	if binary || tooLarge || missing {
		return
	}
	texts := []*string{&oldText, &newText}
	for i, hash := range []string{f.OldHash, f.NewHash} {
		if hash == "" {
			continue
		}
		var out []byte
		out, err = r.run(ctx, root, nil, "cat-file", "blob", hash)
		if err != nil {
			if ctx.Err() != nil {
				err = ctx.Err()
				return
			}
			missing = true
			err = nil
			break
		}
		if len(out) > ReviewMaxTextBytes {
			tooLarge = true
			break
		}
		if bytes.IndexByte(out, 0) >= 0 || !utf8.Valid(out) {
			binary = true
			break
		}
		*texts[i] = string(out)
	}
	if binary || tooLarge || missing {
		oldText = ""
		newText = ""
	}
	return
}
