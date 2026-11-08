// Package patch parses and applies Codex-style apply_patch documents.
package patch

import (
	"fmt"
	"strings"
)

// Kind is a patch file operation.
type Kind int

const (
	Add Kind = iota
	Delete
	Update
)

// HunkLine is one line of an update hunk.
type HunkLine struct {
	Kind byte // ' ', '-' or '+'
	Text string
}

// Hunk is one @@ section of an Update File operation.
type Hunk struct {
	Locator   string
	Lines     []HunkLine
	EndOfFile bool
}

// Op is one file operation in a patch.
type Op struct {
	Kind    Kind
	Path    string
	MoveTo  string
	Content string
	Hunks   []Hunk
}

// FileResult is the computed effect of one operation.
type FileResult struct {
	Path     string
	MoveTo   string
	Original string
	Updated  string
	Added    bool
	Deleted  bool
}

// Parse parses a patch document. A patch may be surrounded by whitespace or
// wrapped in a here-document such as apply_patch <<'EOF' ... EOF.
func Parse(text string) ([]Op, error) {
	lines := splitInput(text)
	start, end := 0, len(lines)
	for start < end && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	if start < end {
		if delim, ok := heredocStart(lines[start]); ok {
			last := end - 1
			for last > start && strings.TrimSpace(lines[last]) == "" {
				last--
			}
			if last > start && heredocEnd(lines[last], delim) {
				start++
				end = last
			}
		}
	}
	for start < end && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	if start >= end {
		return nil, fmt.Errorf("line 1: missing *** Begin Patch")
	}
	if strings.TrimSpace(lines[start]) != "*** Begin Patch" {
		return nil, fmt.Errorf("line %d: expected *** Begin Patch", start+1)
	}
	ops, _, found, err := parseOps(lines, start+1, end)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("line %d: missing *** End Patch", end)
	}
	if len(ops) == 0 {
		return nil, fmt.Errorf("line %d: patch contains no file operations", start+1)
	}
	return ops, nil
}

// Paths returns every path referenced by a patch, including move targets.
func Paths(text string) ([]string, error) {
	ops, err := Parse(text)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, op := range ops {
		paths = append(paths, op.Path)
		if op.MoveTo != "" {
			paths = append(paths, op.MoveTo)
		}
	}
	return paths, nil
}

// Apply computes every operation without touching the filesystem. It returns
// results only when all operations succeed.
func Apply(ops []Op, read func(path string) (string, error)) ([]FileResult, error) {
	results := make([]FileResult, 0, len(ops))
	seen := make(map[string]bool, len(ops))
	for _, op := range ops {
		if seen[op.Path] {
			return nil, fmt.Errorf("%s: duplicate file operation", op.Path)
		}
		seen[op.Path] = true
		switch op.Kind {
		case Add:
			results = append(results, FileResult{Path: op.Path, Updated: op.Content, Added: true})
		case Delete:
			original, err := read(op.Path)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", op.Path, err)
			}
			results = append(results, FileResult{Path: op.Path, Original: original, Deleted: true})
		case Update:
			original, err := read(op.Path)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", op.Path, err)
			}
			updated, err := applyHunks(original, op.Hunks)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", op.Path, err)
			}
			results = append(results, FileResult{
				Path:     op.Path,
				MoveTo:   op.MoveTo,
				Original: original,
				Updated:  updated,
			})
		default:
			return nil, fmt.Errorf("%s: unknown operation", op.Path)
		}
	}
	return results, nil
}

func splitInput(text string) []string {
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return lines
}

func heredocStart(line string) (string, bool) {
	line = strings.TrimSpace(line)
	i := strings.Index(line, "<<")
	if i < 0 {
		return "", false
	}
	delim := strings.TrimSpace(line[i+2:])
	delim = strings.Trim(delim, `'"`)
	delim = strings.TrimSpace(strings.TrimRight(delim, ";"))
	return delim, delim != ""
}

func heredocEnd(line, delim string) bool {
	line = strings.TrimSpace(line)
	line = strings.TrimSpace(strings.TrimSuffix(line, ";"))
	line = strings.Trim(line, `'"`)
	return line == delim
}

func parseOps(lines []string, i, end int) ([]Op, int, bool, error) {
	var ops []Op
	for {
		for i < end && strings.TrimSpace(lines[i]) == "" {
			i++
		}
		if i >= end {
			return ops, i, false, nil
		}
		line := strings.TrimSpace(lines[i])
		if line == "*** End Patch" {
			return ops, i + 1, true, nil
		}
		switch {
		case strings.HasPrefix(line, "*** Add File:"):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Add File:"))
			if path == "" {
				return nil, i, false, fmt.Errorf("line %d: missing file path", i+1)
			}
			content, next, err := parseAdd(lines, i+1, end)
			if err != nil {
				return nil, i, false, err
			}
			ops = append(ops, Op{Kind: Add, Path: path, Content: content})
			i = next
		case strings.HasPrefix(line, "*** Delete File:"):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File:"))
			if path == "" {
				return nil, i, false, fmt.Errorf("line %d: missing file path", i+1)
			}
			ops = append(ops, Op{Kind: Delete, Path: path})
			i++
		case strings.HasPrefix(line, "*** Update File:"):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Update File:"))
			if path == "" {
				return nil, i, false, fmt.Errorf("line %d: missing file path", i+1)
			}
			op, next, err := parseUpdate(lines, i+1, end)
			if err != nil {
				return nil, i, false, err
			}
			op.Path = path
			ops = append(ops, op)
			i = next
		default:
			return nil, i, false, fmt.Errorf("line %d: unexpected line %q", i+1, line)
		}
	}
}

func parseAdd(lines []string, i, end int) (string, int, error) {
	var content []string
	for i < end {
		line := lines[i]
		if strings.HasPrefix(strings.TrimSpace(line), "***") {
			break
		}
		if line == "" {
			content = append(content, "")
			i++
			continue
		}
		if line[0] != '+' {
			return "", i, fmt.Errorf("line %d: added file line must start with +", i+1)
		}
		content = append(content, line[1:])
		i++
	}
	for len(content) > 0 && content[len(content)-1] == "" {
		content = content[:len(content)-1]
	}
	if len(content) == 0 {
		return "", i, nil
	}
	return strings.Join(content, "\n") + "\n", i, nil
}

func parseUpdate(lines []string, i, end int) (Op, int, error) {
	var op Op
	if i < end {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "*** Move to:") {
			op.MoveTo = strings.TrimSpace(strings.TrimPrefix(line, "*** Move to:"))
			if op.MoveTo == "" {
				return op, i, fmt.Errorf("line %d: missing move target", i+1)
			}
			i++
		}
	}
	for i < end {
		line := strings.TrimSpace(lines[i])
		if line == "*** End Patch" || strings.HasPrefix(line, "*** Add File:") ||
			strings.HasPrefix(line, "*** Delete File:") || strings.HasPrefix(line, "*** Update File:") {
			break
		}
		if !strings.HasPrefix(line, "@@") {
			return op, i, fmt.Errorf("line %d: expected @@ or End Patch, got %q", i+1, line)
		}
		hunk := Hunk{Locator: strings.TrimSpace(strings.TrimPrefix(line, "@@"))}
		i++
		for i < end {
			raw := lines[i]
			line = strings.TrimSpace(raw)
			if strings.HasPrefix(line, "@@") || strings.HasPrefix(line, "***") {
				break
			}
			if raw == "" {
				hunk.Lines = append(hunk.Lines, HunkLine{Kind: ' '})
				i++
				continue
			}
			switch raw[0] {
			case ' ', '-', '+':
				hunk.Lines = append(hunk.Lines, HunkLine{Kind: raw[0], Text: raw[1:]})
			default:
				return op, i, fmt.Errorf("line %d: hunk line must start with space, - or +", i+1)
			}
			i++
		}
		if i < end && strings.TrimSpace(lines[i]) == "*** End of File" {
			hunk.EndOfFile = true
			i++
		}
		for len(hunk.Lines) > 0 {
			last := hunk.Lines[len(hunk.Lines)-1]
			if last.Kind != ' ' || last.Text != "" {
				break
			}
			hunk.Lines = hunk.Lines[:len(hunk.Lines)-1]
		}
		op.Hunks = append(op.Hunks, hunk)
	}
	return op, i, nil
}

func applyHunks(original string, hunks []Hunk) (string, error) {
	lines := splitKeep(original)
	eol := "\n"
	if strings.Contains(original, "\r\n") {
		eol = "\r\n"
	}
	search := 0
	for _, hunk := range hunks {
		var oldBlock []string
		for _, line := range hunk.Lines {
			if line.Kind != '+' {
				oldBlock = append(oldBlock, line.Text)
			}
		}
		from := search
		if hunk.Locator != "" {
			found := -1
			for j := search; j < len(lines); j++ {
				if strings.TrimSpace(lineText(lines[j])) == hunk.Locator {
					found = j
					break
				}
			}
			if found < 0 {
				return "", fmt.Errorf("locator not found: %q", hunk.Locator)
			}
			from = found
		}
		if hunk.EndOfFile {
			from = len(lines) - len(oldBlock)
			if from < search {
				from = search
			}
		}
		at, _, ok := findHunk(lines, from, oldBlock)
		if !ok {
			return "", fmt.Errorf("hunk not found:\n%s", strings.Join(oldBlock, "\n"))
		}
		if hunk.EndOfFile && at+len(oldBlock) != len(lines) {
			return "", fmt.Errorf("hunk not found at end of file:\n%s", strings.Join(oldBlock, "\n"))
		}
		var replacement []string
		oldIndex := 0
		for _, line := range hunk.Lines {
			switch line.Kind {
			case ' ':
				replacement = append(replacement, lines[at+oldIndex])
				oldIndex++
			case '-':
				oldIndex++
			case '+':
				replacement = append(replacement, line.Text+eol)
			}
		}
		for k := 0; k+1 < len(replacement); k++ {
			if !strings.HasSuffix(replacement[k], "\n") {
				replacement[k] += eol
			}
		}
		rest := make([]string, len(lines[at+len(oldBlock):]))
		copy(rest, lines[at+len(oldBlock):])
		lines = append(lines[:at:at], append(replacement, rest...)...)
		search = at + len(replacement)
	}
	return strings.Join(lines, ""), nil
}

func splitKeep(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func lineText(line string) string {
	line = strings.TrimSuffix(line, "\n")
	return strings.TrimSuffix(line, "\r")
}

func findHunk(lines []string, from int, oldBlock []string) (int, int, bool) {
	if len(oldBlock) == 0 {
		if from > len(lines) {
			return 0, 0, false
		}
		return from, 0, true
	}
	for mode := 0; mode < 3; mode++ {
		for at := from; at+len(oldBlock) <= len(lines); at++ {
			ok := true
			for k, want := range oldBlock {
				if !matches(lineText(lines[at+k]), want, mode) {
					ok = false
					break
				}
			}
			if ok {
				return at, mode, true
			}
		}
	}
	return 0, 0, false
}

func matches(have, want string, mode int) bool {
	switch mode {
	case 0:
		return have == want
	case 1:
		return strings.TrimRight(have, " \t") == strings.TrimRight(want, " \t")
	default:
		return strings.TrimSpace(have) == strings.TrimSpace(want)
	}
}
