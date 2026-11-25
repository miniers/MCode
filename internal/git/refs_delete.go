package git

import (
	"bytes"
	"context"
	"fmt"
	"strings"
)

const mcodeRefPrefix = "refs/mcode/"

// DeleteRefsWithPrefix 批量删除指定前缀下的 mcode 内部 Git 引用。
// 所有前缀必须以 "refs/mcode/" 开头；无匹配引用时直接返回 nil。
func (r *Runner) DeleteRefsWithPrefix(ctx context.Context, root string, prefixes ...string) error {
	if len(prefixes) == 0 {
		return nil
	}
	for _, prefix := range prefixes {
		if !strings.HasPrefix(prefix, mcodeRefPrefix) || strings.Contains(prefix, "..") || strings.ContainsAny(prefix, "\r\n\t ") {
			return fmt.Errorf("delete refs: invalid prefix %q: must start with %s", prefix, mcodeRefPrefix)
		}
	}

	args := make([]string, 0, 2+len(prefixes))
	args = append(args, "for-each-ref", "--format=%(refname)")
	args = append(args, prefixes...)

	out, err := r.run(ctx, root, nil, args...)
	if err != nil {
		return err
	}

	var stdin bytes.Buffer
	count := 0
	for _, rawLine := range bytes.Split(out, []byte{'\n'}) {
		ref := strings.TrimSpace(string(bytes.TrimRight(rawLine, "\r")))
		if ref == "" {
			continue
		}
		if !strings.HasPrefix(ref, mcodeRefPrefix) {
			return fmt.Errorf("delete refs: unexpected ref %q outside %s", ref, mcodeRefPrefix)
		}
		fmt.Fprintf(&stdin, "delete %s\n", ref)
		count++
	}
	if count == 0 {
		return nil
	}

	if _, err := r.runWithStdin(ctx, root, nil, stdin.Bytes(), "update-ref", "--stdin"); err != nil {
		return err
	}
	return nil
}
