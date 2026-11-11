package mcodeui

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// planTitle 取首个一至三级 Markdown 标题，忽略代码围栏中的标题。
func planTitle(plan string) string {
	var fence byte
	var fenceLen int
	for _, line := range strings.Split(plan, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			marker := line[0]
			n := len(line) - len(strings.TrimLeft(line, string(marker)))
			if fence == 0 {
				fence, fenceLen = marker, n
			} else if marker == fence && n >= fenceLen && strings.TrimSpace(line[n:]) == "" {
				fence = 0
			}
			continue
		}
		if fence != 0 {
			continue
		}
		n := len(line) - len(strings.TrimLeft(line, "#"))
		if n < 1 || n > 3 || n == len(line) || (line[n] != ' ' && line[n] != '\t') {
			continue
		}
		title := strings.TrimSpace(line[n:])
		// ATX 结束标记只在前面有空白时生效。
		end := strings.TrimRight(title, "#")
		if len(end) < len(title) && (end == "" || strings.HasSuffix(end, " ") || strings.HasSuffix(end, "\t")) {
			title = strings.TrimSpace(end)
		}
		if title != "" {
			return title
		}
	}
	return "计划"
}

// renderPlanMarkdown 渲染计划正文；maxHeight > 0 时放进限高滚动区，否则整篇铺开。
func renderPlanMarkdown(c *ui.Context, s *chatState, key, plan string, maxHeight float32) {
	if plan == "" {
		return
	}
	if maxHeight <= 0 {
		markdown.View(c.Key(key+"-md"), plan, &s.mdStaticOpts)
		return
	}
	ui.Scroll(c.Key(key)).FillWidth().MaxHeight(maxHeight).Children(func() {
		markdown.View(c.Key(key+"-md"), plan, &s.mdStaticOpts)
	})
}
