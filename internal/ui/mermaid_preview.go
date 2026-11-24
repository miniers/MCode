package mcodeui

import (
	"fmt"
	"html"
	"io/fs"
	"strings"

	"mcode/internal/webassets"
)

const mermaidVendorScriptPath = "vendor/mermaid.min.js"

// escapeInlineScript 转义内联脚本中的 </script 闭合片段，避免提前截断 <script> 标签。
func escapeInlineScript(raw []byte) string {
	s := string(raw)
	var b strings.Builder
	for i := 0; i < len(s); {
		if i+8 <= len(s) && strings.EqualFold(s[i:i+8], "</script") {
			if b.Len() == 0 {
				b.Grow(len(s) + 8)
				b.WriteString(s[:i])
			}
			b.WriteString(`<\/`)
			b.WriteString(s[i+2 : i+8])
			i += 8
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(s[i])
		}
		i++
	}
	if b.Len() == 0 {
		return s
	}
	return b.String()
}

// mermaidPreviewHTML 构造包含 Mermaid 运行时与严格配置的单页 HTML 预览文档。
func mermaidPreviewHTML(script []byte, code string, dark bool) string {
	theme := "default"
	bg := "#ffffff"
	fg := "#18181b"
	if dark {
		theme = "dark"
		bg = "#18181b"
		fg = "#f4f4f5"
	}
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<style>
html,body{margin:0;padding:0;background:%s;color:%s;}
body{min-height:100vh;box-sizing:border-box;padding:24px;display:flex;justify-content:center;align-items:flex-start;}
.mermaid{max-width:100%%;margin:0;}
svg{max-width:100%%;height:auto;}
</style>
<script>%s</script>
</head>
<body>
<pre class="mermaid">%s</pre>
<script>mermaid.initialize({startOnLoad:true, securityLevel:'strict', theme:'%s'});</script>
</body>
</html>`, bg, fg, escapeInlineScript(script), html.EscapeString(code), theme)
}

// readMermaidVendorScript 从嵌入前端资源读取 Mermaid 运行时脚本。
func readMermaidVendorScript() ([]byte, error) {
	script, err := fs.ReadFile(webassets.FS(), mermaidVendorScriptPath)
	if err != nil {
		return nil, err
	}
	if len(script) == 0 {
		return nil, fmt.Errorf("mermaid 资源为空: %s", mermaidVendorScriptPath)
	}
	return script, nil
}

// openPreviewMermaid 从嵌入前端资源加载 Mermaid 并在隔离预览面板中渲染图表。
func openPreviewMermaid(s *chatState, code string) {
	if s == nil {
		return
	}
	script, err := readMermaidVendorScript()
	if err != nil {
		s.setToast("Mermaid 资源缺失")
		return
	}
	doc := mermaidPreviewHTML(script, code, themeDark.Load())
	openPreviewHTMLWith(s, doc, true)
}
