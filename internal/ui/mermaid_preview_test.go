package mcodeui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

func TestMermaidPreviewHTML(t *testing.T) {
	script := []byte("window.mermaid={};/*</script>*/")
	code := "graph TD\nA-->B<script>"

	darkDoc := mermaidPreviewHTML(script, code, true)
	for _, want := range []string{
		"A--&gt;B&lt;script&gt;",
		"securityLevel:'strict'",
		`<\/script>`,
		"theme:'dark'",
	} {
		if !strings.Contains(darkDoc, want) {
			t.Fatalf("dark HTML 缺少 %q:\n%s", want, darkDoc)
		}
	}
	if strings.Contains(darkDoc, "/*</script>*/") {
		t.Fatalf("script 内容里的 </script> 未转义:\n%s", darkDoc)
	}

	lightDoc := mermaidPreviewHTML(script, code, false)
	if !strings.Contains(lightDoc, "theme:'default'") {
		t.Fatalf("light HTML 缺少 theme:'default':\n%s", lightDoc)
	}
}

func TestMermaidPreviewOpenHTMLUntrusted(t *testing.T) {
	s := sidePaneTestState(t)
	defer s.close()

	openPreviewHTMLWith(s, "<html></html>", true)
	st := getPreviewState(s)
	if st == nil || !st.untrusted {
		t.Fatalf("期望 untrusted=true，实际 %v", st != nil && st.untrusted)
	}

	openPreviewHTML(s, "<html></html>")
	if st.untrusted {
		t.Fatal("openPreviewHTML 应保持 untrusted=false")
	}
}

func TestMermaidPreviewCodeBlockStreamingAndClosed(t *testing.T) {
	s := sidePaneTestState(t)
	defer s.close()

	if s.mdOpts.Code["mermaid"] == nil || s.mdStaticOpts.Code["mermaid"] == nil {
		t.Fatal("mermaid 渲染器未注册到 mdOpts.Code 或 mdStaticOpts.Code")
	}

	source := "```mermaid\ngraph TD\nA-->B\n"
	tester := ui.NewTester(func(c *ui.Context) {
		markdown.View(c, source, &s.mdOpts)
	}, 500, 300)

	if tester.HasText("预览") {
		t.Fatal("流式未闭合的 mermaid 代码块不应显示预览按钮")
	}

	source += "```\n"
	tester.Frame()
	if !tester.HasText("预览") {
		t.Fatal("闭合后的 mermaid 代码块应显示预览按钮")
	}
}
