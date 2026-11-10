package mcodeui

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/egoist/mygo/ui/markdown/parser"
)

// inlinePathSuffix 匹配行内代码路径末尾的行号定位后缀。
var inlinePathSuffix = regexp.MustCompile(`(?::\d+(?::\d+|-\d+)?|#L\d+(?:-L\d+)?)$`)

func inlinePathLineRange(code string) (int, int) {
	suffix := inlinePathSuffix.FindString(strings.TrimSpace(code))
	if strings.HasPrefix(suffix, "#") {
		return localFileLinkLineRange(suffix)
	}
	if !strings.HasPrefix(suffix, ":") {
		return 0, 0
	}
	parts := strings.SplitN(suffix[1:], ":", 2)
	line := strings.SplitN(parts[0], "-", 2)
	fragment := "#L" + line[0]
	if len(line) == 2 {
		fragment += "-L" + line[1]
	}
	return localFileLinkLineRange(fragment)
}

// resolveInlinePath 判断行内代码是不是指向本机已存在的文件或目录，返回清理后的绝对路径。
// 空白、通配符、URL 与其他地址一律不算路径；相对路径与纯文件名只在 workDir 下解析。
func resolveInlinePath(code, workDir string) (string, bool) {
	p := strings.TrimSpace(code)
	if p == "" || strings.Contains(p, "://") {
		return "", false
	}
	if strings.ContainsAny(p, "*?<>|") || strings.ContainsFunc(p, unicode.IsSpace) {
		return "", false
	}
	p = inlinePathSuffix.ReplaceAllString(p, "")
	if p == "" {
		return "", false
	}
	switch {
	case strings.HasPrefix(p, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		p = filepath.Join(home, filepath.FromSlash(p[2:]))
	case filepath.IsAbs(p):
		// 绝对路径直接使用。
	default:
		if workDir == "" {
			return "", false
		}
		p = filepath.Join(workDir, filepath.FromSlash(p))
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", false
	}
	abs = filepath.Clean(abs)
	info, err := os.Stat(abs)
	if err != nil || !(info.Mode().IsRegular() || info.IsDir()) {
		return "", false
	}
	return abs, true
}

// inlinePathSpan 是原文里一处待改写的行内代码路径：字节区间与目标绝对路径。
type inlinePathSpan struct {
	start, end int
	target     string
	fragment   string
}

// linkifyInlinePaths 把文本里指向本地已存在文件的行内代码改写成 Markdown 链接：
// 链接文字保留原来的反引号代码，地址用 file:// 加绝对路径。围栏代码块与链接内部的代码不改写。
func linkifyInlinePaths(text, workDir string) string {
	if !strings.Contains(text, "`") {
		return text
	}
	doc := parser.New(parser.Default)
	doc.Set(text)
	var spans []inlinePathSpan
	for i := 1; i < doc.Len(); i++ {
		switch doc.Block(i).Kind {
		case parser.Paragraph, parser.Heading, parser.TableCell:
		default:
			continue
		}
		in := doc.Inlines(i)
		for k := 0; k < len(in); k++ {
			n := &in[k]
			if n.Kind == parser.Link || n.Kind == parser.Image {
				if n.Next > k+1 {
					k = n.Next - 1 // 跳过链接文字里的行内代码
				}
				continue
			}
			if n.Kind != parser.Code || n.Start < 0 || n.Start >= n.End || n.End > len(text) {
				continue
			}
			abs, ok := resolveInlinePath(n.Text, workDir)
			if !ok {
				continue
			}
			first, last := inlinePathLineRange(n.Text)
			fragment := ""
			if first > 0 {
				fragment = fmt.Sprintf("L%d", first)
				if last != first {
					fragment += fmt.Sprintf("-L%d", last)
				}
			}
			spans = append(spans, inlinePathSpan{start: n.Start, end: n.End, target: abs, fragment: fragment})
		}
	}
	if len(spans) == 0 {
		return text
	}
	sort.Slice(spans, func(a, b int) bool { return spans[a].start < spans[b].start })
	var b strings.Builder
	b.Grow(len(text) + 32*len(spans))
	last := 0
	for _, sp := range spans {
		if sp.start < last {
			continue // 与上一处改写重叠，跳过
		}
		b.WriteString(text[last:sp.start])
		b.WriteString("[")
		b.WriteString(text[sp.start:sp.end])
		b.WriteString("](")
		b.WriteString((&url.URL{Scheme: "file", Path: sp.target, Fragment: sp.fragment}).String())
		b.WriteString(")")
		last = sp.end
	}
	b.WriteString(text[last:])
	return b.String()
}

// inlinePathCacheLimit 是改写结果缓存的条目上限，超过后整体清空。
const inlinePathCacheLimit = 512

var inlinePathCache = struct {
	mu sync.Mutex
	m  map[string]string
}{m: make(map[string]string)}

// linkifyMessageText 按当前会话的工作目录改写消息里的本地路径行内代码，结果按内容缓存。
// 不含反引号的文本原样返回，不进入缓存。
func linkifyMessageText(s *chatState, text string) string {
	if !strings.Contains(text, "`") {
		return text
	}
	_, workDir := getSelectedThreadWorkDir(s)
	key := workDir + "\x00" + text
	inlinePathCache.mu.Lock()
	if got, ok := inlinePathCache.m[key]; ok {
		inlinePathCache.mu.Unlock()
		return got
	}
	inlinePathCache.mu.Unlock()
	out := linkifyInlinePaths(text, workDir)
	inlinePathCache.mu.Lock()
	if len(inlinePathCache.m) >= inlinePathCacheLimit {
		inlinePathCache.m = make(map[string]string)
	}
	inlinePathCache.m[key] = out
	inlinePathCache.mu.Unlock()
	return out
}
