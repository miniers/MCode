package git

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// stashFieldSep 是 `git stash list --format` 输出里各字段之间的分隔符（ASCII 单元分隔符），
// 说明文字里几乎不会出现它，避免用空格或冒号切分时被说明内容带跑偏。
const stashFieldSep = "\x1f"

// StashEntry 是一条贮藏记录的元信息。
type StashEntry struct {
	Index     int       // 贮藏序号，对应 stash@{Index}，0 是最新一条
	Ref       string    // 完整引用，例如 stash@{0}
	Message   string    // 去掉「WIP on <branch>:」前缀后的说明文字
	Branch    string    // 贮藏时的来源分支，解析不到时为空
	CreatedAt time.Time // 贮藏创建时间
}

// StashList 按 git 的顺序（最新在前）列出仓库的贮藏记录。
func (r *Runner) StashList(ctx context.Context, root string) ([]StashEntry, error) {
	format := "%gd" + stashFieldSep + "%gs" + stashFieldSep + "%ct"
	out, err := r.run(ctx, root, nil, "stash", "list", "--format="+format)
	if err != nil {
		return nil, err
	}

	var entries []StashEntry
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, stashFieldSep)
		if len(fields) < 3 {
			continue
		}
		// 分隔符万一出现在说明文字里，时间戳始终是最后一段，中间的都算说明。
		secText := strings.TrimSpace(fields[len(fields)-1])
		entry := StashEntry{
			Ref:     strings.TrimSpace(fields[0]),
			Index:   len(entries),
			Message: strings.TrimSpace(strings.Join(fields[1:len(fields)-1], stashFieldSep)),
		}
		if n, ok := parseStashIndex(entry.Ref); ok {
			entry.Index = n
		}
		entry.Branch, entry.Message = parseStashSubject(entry.Message)
		if sec, err := strconv.ParseInt(secText, 10, 64); err == nil && sec > 0 {
			entry.CreatedAt = time.Unix(sec, 0)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// StashApply 应用指定序号的贮藏，但保留记录本身；有冲突时返回 git 的原始错误。
func (r *Runner) StashApply(ctx context.Context, root string, index int) error {
	ref, err := stashRef(index)
	if err != nil {
		return err
	}
	_, err = r.run(ctx, root, nil, "stash", "apply", ref)
	return err
}

// StashDrop 删除指定序号的贮藏记录，工作区不受影响。
func (r *Runner) StashDrop(ctx context.Context, root string, index int) error {
	ref, err := stashRef(index)
	if err != nil {
		return err
	}
	_, err = r.run(ctx, root, nil, "stash", "drop", ref)
	return err
}

// StashPopAt 应用并删除指定序号的贮藏；有冲突时返回 git 的原始错误。
func (r *Runner) StashPopAt(ctx context.Context, root string, index int) error {
	ref, err := stashRef(index)
	if err != nil {
		return err
	}
	_, err = r.run(ctx, root, nil, "stash", "pop", ref)
	return err
}

// stashRef 把序号换算成 stash@{n} 引用；序号必须是非负整数，
// 引用整体作为单个参数传给 git，不接受任何外部拼接的字符串。
func stashRef(index int) (string, error) {
	if index < 0 {
		return "", fmt.Errorf("无效的贮藏序号: %d", index)
	}
	return fmt.Sprintf("stash@{%d}", index), nil
}

// parseStashIndex 从 stash@{n} 里取出序号。
func parseStashIndex(ref string) (int, bool) {
	if !strings.HasPrefix(ref, "stash@{") || !strings.HasSuffix(ref, "}") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(ref, "stash@{"), "}"))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// parseStashSubject 解析 %gs 的说明文字：
// 默认贮藏是「WIP on <branch>: <hash> <说明>」，带 -m 的贮藏是「On <branch>: <说明>」。
// 返回来源分支与去掉前缀后的说明；匹配不上时分支为空、说明原样返回。
func parseStashSubject(subject string) (branch, message string) {
	subject = strings.TrimSpace(subject)
	var rest string
	switch {
	case strings.HasPrefix(subject, "WIP on "):
		rest = strings.TrimPrefix(subject, "WIP on ")
	case strings.HasPrefix(subject, "On "):
		rest = strings.TrimPrefix(subject, "On ")
	default:
		return "", subject
	}
	idx := strings.Index(rest, ": ")
	if idx < 0 {
		return strings.TrimSpace(rest), ""
	}
	return strings.TrimSpace(rest[:idx]), strings.TrimSpace(rest[idx+2:])
}
