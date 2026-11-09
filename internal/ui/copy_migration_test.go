package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

func TestCodeBlockCopyStreamingAndToast(t *testing.T) {
	state := &chatState{}
	source := "```go\nprintln(1)\n"
	open := false
	opts := &markdown.Options{CodeBlock: func(c *ui.Context, block *markdown.CodeBlockView) {
		open = block.Open
		renderCodeBlockContainer(c, state, block)
	}}
	tester := ui.NewTester(func(c *ui.Context) {
		markdown.View(c, source, opts)
	}, 500, 300)
	if !open {
		t.Fatal("未闭合的代码块应处于流式状态")
	}
	if err := tester.Click("复制代码"); err != nil {
		t.Fatal(err)
	}
	if state.toastMessage != "" {
		t.Fatalf("流式代码块显示了提示：%q", state.toastMessage)
	}
	source += "```\n"
	tester.Frame()
	if open {
		t.Fatal("闭合后的代码块仍处于流式状态")
	}
	if err := tester.Click("复制代码"); err != nil {
		t.Fatal(err)
	}
	if state.toastMessage != "已复制" {
		t.Fatalf("复制提示 = %q", state.toastMessage)
	}
}
