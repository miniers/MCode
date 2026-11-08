package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

type textEdit struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}
type span struct {
	start, end int
	text       string
}

// normalized 同时保留每个输出字节对应的原文边界。
func normalized(s string) (string, []int, []int) {
	var out strings.Builder
	var starts, ends []int
	for pos := 0; pos < len(s); {
		start := pos
		r, n := utf8.DecodeRuneInString(s[pos:])
		pos += n
		if r == '\r' && pos < len(s) && s[pos] == '\n' {
			pos++
			r = '\n'
		}
		if r == '\n' {
			for out.Len() > 0 && len(starts) > 0 {
				v := out.String()
				if v[len(v)-1] != ' ' && v[len(v)-1] != '\t' {
					break
				}
				out.Reset()
				out.WriteString(v[:len(v)-1])
				starts = starts[:len(starts)-1]
				ends = ends[:len(ends)-1]
			}
		}
		var value string
		switch r {
		case '‘', '’', '‚', '‛':
			value = "'"
		case '“', '”', '„', '‟':
			value = "\""
		case '‐', '‑', '‒', '–', '—', '―', '−':
			value = "-"
		default:
			if r != '\n' && r != '\t' && unicode.IsSpace(r) {
				value = " "
			} else if r == '\n' {
				value = "\n"
			} else {
				for pos < len(s) {
					next, size := utf8.DecodeRuneInString(s[pos:])
					if !unicode.Is(unicode.M, next) {
						break
					}
					pos += size
				}
				value = norm.NFKC.String(s[start:pos])
			}
		}
		out.WriteString(value)
		for range []byte(value) {
			starts = append(starts, start)
			ends = append(ends, pos)
		}
	}
	return out.String(), starts, ends
}
func locate(original, needle string) []span {
	var matches []span
	for from := 0; from <= len(original)-len(needle); {
		i := strings.Index(original[from:], needle)
		if i < 0 {
			break
		}
		i += from
		matches = append(matches, span{start: i, end: i + len(needle)})
		from = i + 1
	}
	return matches
}
func editFile(ctx context.Context, env Env, raw json.RawMessage) (Result, error) {
	var a struct {
		Path  string     `json:"path"`
		Edits []textEdit `json:"edits"`
	}
	if e := parse(raw, &a); e != nil {
		return failure(e)
	}
	if a.Path == "" || len(a.Edits) == 0 {
		return failure("path and edits are required")
	}
	if e := ctx.Err(); e != nil {
		return Result{}, e
	}
	p, e := resolve(env, a.Path)
	if e != nil {
		return failure(e)
	}
	data, e := os.ReadFile(p)
	if e != nil {
		return failure(e)
	}
	info, err := os.Stat(p)
	if err != nil {
		return failure(err)
	}
	const bom = "\xef\xbb\xbf"
	hasBOM := strings.HasPrefix(string(data), bom)
	original := strings.TrimPrefix(string(data), bom)
	var changes []span
	for i, edit := range a.Edits {
		if edit.OldText == "" {
			return failure(fmt.Sprintf("edit %d: oldText is empty", i+1))
		}
		found := locate(original, edit.OldText)
		if len(found) == 0 {
			normalizedOriginal, starts, ends := normalized(original)
			needle, _, _ := normalized(edit.OldText)
			if needle != "" {
				for _, m := range locate(normalizedOriginal, needle) {
					found = append(found, span{start: starts[m.start], end: ends[m.end-1]})
				}
			}
		}
		if len(found) != 1 {
			return failure(fmt.Sprintf("edit %d: oldText occurred %d times", i+1, len(found)))
		}
		replacement := edit.NewText
		crlf := strings.Contains(original, "\r\n") && !strings.Contains(strings.ReplaceAll(original, "\r\n", ""), "\n")
		if crlf {
			replacement = strings.ReplaceAll(strings.ReplaceAll(replacement, "\r\n", "\n"), "\n", "\r\n")
		} else {
			replacement = strings.ReplaceAll(replacement, "\r\n", "\n")
		}
		found[0].text = replacement
		for j, prev := range changes {
			if found[0].start < prev.end && prev.start < found[0].end {
				return failure(fmt.Sprintf("edit %d overlaps edit %d", i+1, j+1))
			}
		}
		changes = append(changes, found[0])
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].start > changes[j].start })
	updated := original
	for _, c := range changes {
		updated = updated[:c.start] + c.text + updated[c.end:]
	}
	output := updated
	if hasBOM {
		output = bom + updated
	}
	if e = os.WriteFile(p, []byte(output), info.Mode()); e != nil {
		return failure(e)
	}
	return Result{Text: "File edited.", Change: &FileChange{Path: p, Patch: unifiedDiff(p, original, updated)}}, nil
}
