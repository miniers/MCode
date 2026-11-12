package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mcode/internal/fsutil"
)

type ConflictFile struct{ Path, Code string }
type ConflictRegion struct {
	Start, End             int
	Ours, Base, Theirs     []string
	HasBase                bool
	OursLabel, TheirsLabel string
}
type ConflictChoice string

const (
	ConflictOurs   ConflictChoice = "ours"
	ConflictTheirs ConflictChoice = "theirs"
	ConflictBoth   ConflictChoice = "both"
	ConflictBase   ConflictChoice = "base"
)

func (r *Runner) ConflictFiles(ctx context.Context, root string) ([]ConflictFile, error) {
	out, err := r.run(ctx, root, nil, "status", "--porcelain=v1", "-z")
	if err != nil {
		return nil, err
	}
	var files []ConflictFile
	entries := strings.Split(string(out), "\x00")
	for i := 0; i < len(entries); i++ {
		s := entries[i]
		if len(s) < 3 {
			continue
		}
		code := s[:2]
		switch code {
		case "UU", "AA", "DD", "AU", "UA", "DU", "UD":
			files = append(files, ConflictFile{Path: s[3:], Code: code})
		}
		if strings.ContainsAny(code, "RC") {
			i++
		}
	}
	return files, nil
}

func ParseConflicts(text string) []ConflictRegion {
	lines := strings.SplitAfter(text, "\n")
	var result []ConflictRegion
	clean := func(s string) string { return strings.TrimSuffix(strings.TrimSuffix(s, "\n"), "\r") }
	marker := func(s string, ch byte, n int) (string, bool) {
		s = clean(s)
		if len(s) < n || s[:n] != strings.Repeat(string(ch), n) {
			return "", false
		}
		if len(s) > n && s[n] != ' ' {
			return "", false
		}
		return strings.TrimSpace(s[n:]), true
	}
	for i := 0; i < len(lines); i++ {
		line := clean(lines[i])
		n := 0
		for n < len(line) && line[n] == '<' {
			n++
		}
		if n < 7 {
			continue
		}
		label, ok := marker(lines[i], '<', n)
		if !ok {
			continue
		}
		reg := ConflictRegion{Start: i, OursLabel: label}
		phase := 0
		for j := i + 1; j < len(lines); j++ {
			if phase == 0 {
				if _, ok := marker(lines[j], '|', n); ok {
					reg.HasBase = true
					phase = 1
					continue
				}
			}
			if phase < 2 {
				if _, ok := marker(lines[j], '=', n); ok {
					phase = 2
					continue
				}
			}
			if phase == 2 {
				if label, ok := marker(lines[j], '>', n); ok {
					reg.TheirsLabel = label
					reg.End = j
					result = append(result, reg)
					i = j
					break
				}
			}
			switch phase {
			case 0:
				reg.Ours = append(reg.Ours, lines[j])
			case 1:
				reg.Base = append(reg.Base, lines[j])
			case 2:
				reg.Theirs = append(reg.Theirs, lines[j])
			}
		}
	}
	return result
}

func ResolveRegions(text string, choices map[int]ConflictChoice) string {
	lines := strings.SplitAfter(text, "\n")
	regions := ParseConflicts(text)
	var b strings.Builder
	pos := 0
	for i, reg := range regions {
		b.WriteString(strings.Join(lines[pos:reg.Start], ""))
		switch choices[i] {
		case ConflictOurs:
			b.WriteString(strings.Join(reg.Ours, ""))
		case ConflictTheirs:
			b.WriteString(strings.Join(reg.Theirs, ""))
		case ConflictBoth:
			b.WriteString(strings.Join(reg.Ours, ""))
			b.WriteString(strings.Join(reg.Theirs, ""))
		case ConflictBase:
			if reg.HasBase {
				b.WriteString(strings.Join(reg.Base, ""))
			} else {
				b.WriteString(strings.Join(lines[reg.Start:reg.End+1], ""))
			}
		default:
			b.WriteString(strings.Join(lines[reg.Start:reg.End+1], ""))
		}
		pos = reg.End + 1
	}
	b.WriteString(strings.Join(lines[pos:], ""))
	return b.String()
}

func (r *Runner) ConflictSides(ctx context.Context, root, path string) (ours, base, theirs []byte, err error) {
	out, err := r.run(ctx, root, []string{"GIT_LITERAL_PATHSPECS=1"}, "ls-files", "--unmerged", "-z", "--", path)
	if err != nil {
		return nil, nil, nil, err
	}
	stages := map[byte]bool{}
	for _, entry := range strings.Split(string(out), "\x00") {
		if tab := strings.IndexByte(entry, '\t'); tab >= 2 && entry[tab+1:] == path {
			stages[entry[tab-1]] = true
		}
	}
	values := []*[]byte{&base, &ours, &theirs}
	for i, p := range values {
		stage := byte('1' + i)
		if stages[stage] {
			*p, err = r.run(ctx, root, nil, "show", fmt.Sprintf(":%c:%s", stage, path))
			if err != nil {
				return nil, nil, nil, err
			}
			if *p == nil {
				*p = []byte{}
			}
		}
	}
	return ours, base, theirs, nil
}
func (r *Runner) ResolveFileSide(ctx context.Context, root, path string, ours bool) error {
	a, _, b, err := r.ConflictSides(ctx, root, path)
	if err != nil {
		return err
	}
	side, data := "--theirs", b
	if ours {
		side, data = "--ours", a
	}
	if data == nil {
		_, err = r.run(ctx, root, []string{"GIT_LITERAL_PATHSPECS=1"}, "rm", "--", path)
		return err
	}
	if _, err = r.run(ctx, root, []string{"GIT_LITERAL_PATHSPECS=1"}, "checkout", side, "--", path); err != nil {
		return err
	}
	return r.MarkResolved(ctx, root, []string{path})
}
func (r *Runner) MarkResolved(ctx context.Context, root string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	_, err := r.run(ctx, root, []string{"GIT_LITERAL_PATHSPECS=1"}, append([]string{"add", "--"}, paths...)...)
	return err
}

// WriteWorktreeFile 原子写入工作区文件，并保留原有权限。
func (r *Runner) WriteWorktreeFile(ctx context.Context, root, path string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rel, err := snapshotPath(root, path)
	if err != nil {
		return err
	}
	full := filepath.Join(root, filepath.FromSlash(rel))
	if info, err := os.Lstat(full); err == nil && info.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(full)
		if err != nil {
			return err
		}
		if _, err := snapshotPath(root, target); err != nil {
			return err
		}
		full = target
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(full); statErr == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(full, data, mode)
}
