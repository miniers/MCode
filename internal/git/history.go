package git

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type LogOptions struct {
	Ref         string
	All         bool
	Path        string
	Query       string
	Skip, Limit int
}
type LogRef struct {
	Name string
	Kind string
}
type LogCommit struct {
	SHA           string
	Parents       []string
	Author, Email string
	AuthorTime    time.Time
	Subject       string
	Refs          []LogRef
	Unpushed      bool
}
type CommitFile struct {
	Path, OrigPath, Status string
	Added, Deleted         int
	Binary                 bool
}
type CommitDetail struct {
	LogCommit
	Body       string
	Committer  string
	CommitTime time.Time
	Files      []CommitFile
}

const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

var shortHash = regexp.MustCompile(`^[a-fA-F0-9]{4,40}$`)

func parseRefs(s string) []LogRef {
	var refs []LogRef
	for _, item := range strings.Split(s, ", ") {
		if item == "" {
			continue
		}
		if strings.HasPrefix(item, "HEAD -> ") {
			refs = append(refs, LogRef{"HEAD", "head"})
			item = strings.TrimPrefix(item, "HEAD -> ")
		} else if item == "HEAD" {
			refs = append(refs, LogRef{"HEAD", "head"})
			continue
		}
		kind := "local"
		if strings.HasPrefix(item, "tag: ") {
			kind = "tag"
			item = strings.TrimPrefix(strings.TrimPrefix(item, "tag: "), "refs/tags/")
		} else if strings.HasPrefix(item, "refs/remotes/") {
			kind = "remote"
			item = strings.TrimPrefix(item, "refs/remotes/")
		} else if strings.HasPrefix(item, "refs/heads/") {
			item = strings.TrimPrefix(item, "refs/heads/")
		} else if strings.HasPrefix(item, "refs/tags/") {
			kind = "tag"
			item = strings.TrimPrefix(item, "refs/tags/")
		}
		refs = append(refs, LogRef{item, kind})
	}
	return refs
}

func parseLog(out []byte) ([]LogCommit, error) {
	var result []LogCommit
	for _, record := range strings.Split(string(out), "\x00") {
		record = strings.TrimPrefix(record, "\n")
		if record == "" {
			continue
		}
		f := strings.Split(record, "\x1f")
		if len(f) != 7 {
			return nil, fmt.Errorf("无法解析提交记录")
		}
		sec, err := strconv.ParseInt(f[4], 10, 64)
		if err != nil {
			return nil, err
		}
		result = append(result, LogCommit{SHA: f[0], Parents: strings.Fields(f[1]), Author: f[2], Email: f[3], AuthorTime: time.Unix(sec, 0), Subject: f[5], Refs: parseRefs(f[6])})
	}
	if result == nil {
		result = []LogCommit{}
	}
	return result, nil
}

func (r *Runner) Log(ctx context.Context, root string, opts LogOptions) ([]LogCommit, error) {
	ref := opts.Ref
	if ref == "" {
		ref = "HEAD"
	}
	rangeArgs := []string{ref}
	if opts.All {
		rangeArgs = []string{"--all"}
	}
	// 空仓库仅在 HEAD 不存在时返回空结果；其他无效引用仍报告错误。
	if !opts.All && opts.Ref == "" {
		if _, err := r.run(ctx, root, nil, "rev-parse", "--verify", "HEAD^{commit}"); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if _, e := r.run(ctx, root, nil, "symbolic-ref", "-q", "HEAD"); e == nil {
				return []LogCommit{}, nil
			}
			return nil, err
		}
	}
	if !opts.All {
		resolved, err := r.run(ctx, root, nil, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
		if err != nil {
			return nil, err
		}
		rangeArgs = []string{strings.TrimSpace(string(resolved))}
	}
	format := "--format=%H%x1f%P%x1f%an%x1f%ae%x1f%at%x1f%s%x1f%D"
	base := []string{"log", "--topo-order", "--decorate=full", "-z", format}
	hashQuery := false
	if opts.Query != "" && shortHash.MatchString(opts.Query) {
		if out, err := r.run(ctx, root, nil, "rev-parse", "--verify", opts.Query+"^{commit}"); err == nil {
			rangeArgs = []string{strings.TrimSpace(string(out))}
			hashQuery = true
			base = append(base, "-1")
		} else if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	skip, limit := opts.Skip, opts.Limit
	if skip < 0 {
		skip = 0
	}
	if limit <= 0 {
		limit = 200
	}
	pagedByGit := opts.Query == ""
	if pagedByGit {
		base = append(base, "--skip="+strconv.Itoa(skip), "--max-count="+strconv.Itoa(limit))
	}
	args := append(append([]string{}, base...), rangeArgs...)
	args = append(args, "--")
	if opts.Path != "" {
		args = append(args, opts.Path)
	}
	out, err := r.run(ctx, root, nil, args...)
	if err != nil {
		return nil, err
	}
	commits, err := parseLog(out)
	if err != nil {
		return nil, err
	}
	if opts.Query != "" && !hashQuery {
		matches := map[string]bool{}
		for _, flag := range []string{"--grep=", "--author="} {
			a := append(append([]string{}, base...), "--regexp-ignore-case", "--fixed-strings", flag+opts.Query)
			a = append(a, rangeArgs...)
			a = append(a, "--")
			if opts.Path != "" {
				a = append(a, opts.Path)
			}
			b, e := r.run(ctx, root, nil, a...)
			if e != nil {
				return nil, e
			}
			found, e := parseLog(b)
			if e != nil {
				return nil, e
			}
			for _, c := range found {
				matches[c.SHA] = true
			}
		}
		filtered := commits[:0]
		for _, c := range commits {
			if matches[c.SHA] {
				filtered = append(filtered, c)
			}
		}
		commits = filtered
	}
	remotes, err := r.run(ctx, root, nil, "for-each-ref", "--format=%(refname)", "refs/remotes/")
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(remotes))) > 0 {
		a := append([]string{"rev-list"}, rangeArgs...)
		a = append(a, "--not", "--remotes", "--")
		b, e := r.run(ctx, root, nil, a...)
		if e != nil {
			return nil, e
		}
		set := map[string]bool{}
		for _, s := range strings.Fields(string(b)) {
			set[s] = true
		}
		for i := range commits {
			commits[i].Unpushed = set[commits[i].SHA]
		}
	}
	if pagedByGit {
		return commits, nil
	}
	if skip > len(commits) {
		skip = len(commits)
	}
	end := skip + limit
	if end < skip || end > len(commits) {
		end = len(commits)
	}
	return commits[skip:end], nil
}

func (r *Runner) CommitBase(ctx context.Context, root, sha string) (string, error) {
	out, err := r.run(ctx, root, nil, "rev-list", "--parents", "-n", "1", "--end-of-options", sha)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", fmt.Errorf("提交不存在")
	}
	if len(fields) > 1 {
		return fields[1], nil
	}
	return emptyTree, nil
}
func (r *Runner) ShowCommit(ctx context.Context, root, sha string) (CommitDetail, error) {
	var d CommitDetail
	// 先把用户输入解析为完整提交 ID，后续命令只接收确定的对象 ID。
	out, err := r.run(ctx, root, nil, "rev-parse", "--verify", "--end-of-options", sha+"^{commit}")
	if err != nil {
		return d, err
	}
	id := strings.TrimSpace(string(out))
	commits, err := r.Log(ctx, root, LogOptions{Ref: id, Limit: 1})
	if err != nil {
		return d, err
	}
	if len(commits) == 0 {
		return d, fmt.Errorf("提交不存在")
	}
	d.LogCommit = commits[0]
	out, err = r.run(ctx, root, nil, "show", "-s", "--format=%B%x00%cn%x00%ct", id, "--")
	if err != nil {
		return d, err
	}
	f := strings.SplitN(string(out), "\x00", 3)
	if len(f) != 3 {
		return d, fmt.Errorf("无法解析提交详情")
	}
	d.Body = strings.TrimSuffix(f[0], "\n")
	d.Committer = f[1]
	sec, e := strconv.ParseInt(strings.TrimSpace(f[2]), 10, 64)
	if e != nil {
		return d, e
	}
	d.CommitTime = time.Unix(sec, 0)
	base, err := r.CommitBase(ctx, root, id)
	if err != nil {
		return d, err
	}
	out, err = r.run(ctx, root, nil, "diff", "--name-status", "-z", "-M", "-C", base, id, "--")
	if err != nil {
		return d, err
	}
	parts := strings.Split(string(out), "\x00")
	for i := 0; i < len(parts) && parts[i] != ""; {
		status := parts[i]
		i++
		if i >= len(parts) {
			return d, fmt.Errorf("无法解析文件状态")
		}
		file := CommitFile{Status: status[:1]}
		if file.Status == "R" || file.Status == "C" {
			file.OrigPath = parts[i]
			i++
			if i >= len(parts) {
				return d, fmt.Errorf("无法解析重命名路径")
			}
		}
		file.Path = parts[i]
		i++
		d.Files = append(d.Files, file)
	}
	out, err = r.run(ctx, root, nil, "diff", "--numstat", "-z", "-M", "-C", base, id, "--")
	if err != nil {
		return d, err
	}
	parts = strings.Split(string(out), "\x00")
	for i := 0; i < len(parts) && parts[i] != ""; i++ {
		fields := strings.SplitN(parts[i], "\t", 3)
		if len(fields) != 3 {
			return d, fmt.Errorf("无法解析文件统计")
		}
		path := fields[2]
		if path == "" {
			if i+2 >= len(parts) {
				return d, fmt.Errorf("无法解析重命名统计")
			}
			path = parts[i+2]
			i += 2
		}
		for j := range d.Files {
			if d.Files[j].Path == path {
				d.Files[j].Binary = fields[0] == "-"
				d.Files[j].Added, _ = strconv.Atoi(fields[0])
				d.Files[j].Deleted, _ = strconv.Atoi(fields[1])
				break
			}
		}
	}
	return d, nil
}
