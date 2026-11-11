package mcodeui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func benchReviewContent(lines int) ReviewFileContent {
	var old, next strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&old, "old %d\n", i)
		fmt.Fprintf(&next, "new %d\n", i)
	}
	return ReviewFileContent{Path: "bench.go", OldText: old.String(), NewText: next.String()}
}

// 只创建选中文件的 Buffer；比较固定全文、切版本与大文件可见区域的分配。
func benchmarkReviewFrame(b *testing.B, lines int, changing, split bool) {
	content := benchReviewContent(lines)
	dp := &diffPanelState{}
	key := reviewDiffKey{workspace: "bench", scope: "turn", head: "0", path: content.Path}
	view := reviewViewFor(dp, key, content, "go")
	tt := ui.NewTester(func(c *ui.Context) { renderReviewDiffView(c, "bench", view, split, 800).Fill() }, 800, 600)
	tt.Frame()
	tt.Frame()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if changing {
			key.head = fmt.Sprintf("revision-%d", i)
			view = reviewViewFor(dp, key, content, "go")
		}
		tt.Frame()
	}
}

func BenchmarkReviewUnifiedFrame100Lines(b *testing.B) { benchmarkReviewFrame(b, 100, false, false) }
func BenchmarkReviewSplitFrame100Lines(b *testing.B)   { benchmarkReviewFrame(b, 100, false, true) }
func BenchmarkReviewChangedRevisionFrame100Lines(b *testing.B) {
	benchmarkReviewFrame(b, 100, true, false)
}
func BenchmarkReviewUnifiedFrame4990Lines(b *testing.B) { benchmarkReviewFrame(b, 4990, false, false) }
func BenchmarkReviewSplitFrame4990Lines(b *testing.B)   { benchmarkReviewFrame(b, 4990, false, true) }
func BenchmarkReviewFullText5000(b *testing.B) {
	content := benchReviewContent(5000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		view := newReviewDiffView(content, "go")
		if len(view.state.Hunks()) == 0 {
			b.Fatal("缺少改动")
		}
	}
}
