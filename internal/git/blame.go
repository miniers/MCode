package git

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type BlameLine struct {
	Line       int
	SHA        string
	Author     string
	AuthorTime time.Time
	Summary    string
	Text       string
}
type BlameBlock struct {
	Start, End int
	SHA        string
}

func (r *Runner) Blame(ctx context.Context, root, path, rev string) ([]BlameLine, error) {
	args := []string{"blame", "--porcelain"}
	if rev != "" {
		out, err := r.run(ctx, root, nil, "rev-parse", "--verify", "--end-of-options", rev+"^{commit}")
		if err != nil {
			return nil, err
		}
		args = append(args, strings.TrimSpace(string(out)))
	}
	args = append(args, "--", path)
	out, err := r.run(ctx, root, nil, args...)
	if err != nil {
		return nil, err
	}
	cache := map[string]BlameLine{}
	var result []BlameLine
	var current BlameLine
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "\t") {
			current.Text = line[1:]
			if strings.Trim(current.SHA, "0") == "" {
				current.Author = "未提交"
			}
			cache[current.SHA] = current
			result = append(result, current)
			continue
		}
		f := strings.Fields(line)
		if len(f) >= 3 && len(f[0]) >= 40 && shortHash.MatchString(f[0]) {
			n, e := strconv.Atoi(f[2])
			if e != nil {
				return nil, e
			}
			current = cache[f[0]]
			current.SHA = f[0]
			current.Line = n
			continue
		}
		if strings.HasPrefix(line, "author ") {
			current.Author = strings.TrimPrefix(line, "author ")
		} else if strings.HasPrefix(line, "author-time ") {
			n, e := strconv.ParseInt(strings.TrimPrefix(line, "author-time "), 10, 64)
			if e != nil {
				return nil, fmt.Errorf("无法解析作者时间: %w", e)
			}
			current.AuthorTime = time.Unix(n, 0)
		} else if strings.HasPrefix(line, "summary ") {
			current.Summary = strings.TrimPrefix(line, "summary ")
		}
	}
	if result == nil {
		result = []BlameLine{}
	}
	return result, nil
}
func BlameBlocks(lines []BlameLine) []BlameBlock {
	blocks := []BlameBlock{}
	for _, line := range lines {
		n := len(blocks)
		if n > 0 && blocks[n-1].SHA == line.SHA && blocks[n-1].End+1 == line.Line {
			blocks[n-1].End = line.Line
		} else {
			blocks = append(blocks, BlameBlock{line.Line, line.Line, line.SHA})
		}
	}
	return blocks
}
