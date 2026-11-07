package mcodeui

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

// 本文件是改动面板虚拟化的帧基准：用无头窗口（ui.Tester）跑真实的 build/layout，
// 对照「改前逐个文件铺满全部行」与「改后只渲染视口附近的行」。
// 绝对毫秒数随机器变化，重点是同一台机器上的回归比值。

// benchDiffFiles 构造 n 个文件、每个文件 lines 行改动的 FileDiff，
// 一个文件一个 hunk，块内是「上下文 / 删 / 增 / 上下文」混合行。
func benchDiffFiles(n, lines int) []git.FileDiff {
	files := make([]git.FileDiff, 0, n)
	for i := 0; i < n; i++ {
		path := fmt.Sprintf("internal/pkg%d/file%d.go", i%7, i)
		hunkLines := make([]git.Line, 0, lines)
		for j := 0; j < lines; j++ {
			line := git.Line{OldNo: j + 1, NewNo: j + 1}
			switch j % 4 {
			case 0:
				line.Kind = ' '
				line.Text = fmt.Sprintf("    ctx := context.Background() // %d", j)
			case 1:
				line.Kind = '-'
				line.Text = fmt.Sprintf("    oldValue%d := compute(ctx, %d)", j, i)
			case 2:
				line.Kind = '+'
				line.Text = fmt.Sprintf("    newValue%d := computeV2(ctx, %d)", j, i)
			default:
				line.Kind = ' '
				line.Text = fmt.Sprintf("    return newValue%d", j)
			}
			hunkLines = append(hunkLines, line)
		}
		files = append(files, git.FileDiff{
			OldPath: path,
			NewPath: path,
			Status:  "modified",
			Hunks: []git.Hunk{{
				Header: fmt.Sprintf("@@ -1,%d +1,%d @@ func compute%d()", lines, lines, i),
				Lines:  hunkLines,
			}},
		})
	}
	prefillDiffStats(files)
	return files
}

// benchDiffPanelState 构造带行模型缓存的改动面板状态。
func benchDiffPanelState(files []git.FileDiff) *diffPanelState {
	dp := &diffPanelState{
		collapsed:     make(map[string]bool),
		expandedLarge: make(map[string]bool),
	}
	dp.files = files
	dp.rebuildDiffRowsLocked()
	return dp
}

// benchDiffPanelView 照改动面板的结构渲染：根 Column → 虚拟行列表。
// gen 每帧自增，用来模拟切轮次后整棵元素树重建（根 key 变化）。
func benchDiffPanelView(dp *diffPanelState, files []git.FileDiff, gen *int) func(*ui.Context) {
	return func(c *ui.Context) {
		c.DisableAnimations()
		ui.Column(c.Key("diff-bench-root-" + strconv.Itoa(*gen))).
			Fill().
			Children(func() {
				rows := diffPanelRowsFor(dp, files)
				dp.rowsList.Key = func(i int) any {
					if i >= 0 && i < len(rows) {
						return rows[i].ID
					}
					return i
				}
				ui.List(c.Key("diff-bench-rows"), &dp.rowsList, len(rows), func(i int) {
					renderDiffRow(c, nil, dp, rows[i])
				}).
					Grow(1).
					MinHeight(0).
					FillWidth().
					Padding(12, 14)
			})
		*gen = *gen + 1
	}
}

// BenchmarkDiffPanelFrame50x100：50 个文件各 100 行、静止一帧（验收 < 2ms、< 3000 次分配）。
func BenchmarkDiffPanelFrame50x100(b *testing.B) {
	files := benchDiffFiles(50, 100)
	dp := benchDiffPanelState(files)
	gen := 0
	tt := ui.NewTester(benchDiffPanelView(dp, files, &gen), 1920, 1080)
	tt.Frame()
	tt.Frame()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tt.Frame()
	}
}

// BenchmarkDiffPanelFrame50x100Collapsed：全部折叠，只剩 50 个文件头，
// 作为「成本落在行上」的对照基线。
func BenchmarkDiffPanelFrame50x100Collapsed(b *testing.B) {
	files := benchDiffFiles(50, 100)
	dp := benchDiffPanelState(files)
	for i := range files {
		dp.collapsed[files[i].NewPath] = true
	}
	dp.rebuildDiffRowsLocked()
	gen := 0
	tt := ui.NewTester(benchDiffPanelView(dp, files, &gen), 1920, 1080)
	tt.Frame()
	tt.Frame()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tt.Frame()
	}
}

// BenchmarkDiffPanelFrame50x100ContentChanged：每一帧内容全变（切轮次），
// 行模型重建 + 整棵树重建（验收 < 60ms）。
func BenchmarkDiffPanelFrame50x100ContentChanged(b *testing.B) {
	files := benchDiffFiles(50, 100)
	dp := benchDiffPanelState(files)
	gen := 0
	tt := ui.NewTester(benchDiffPanelView(dp, files, &gen), 1920, 1080)
	tt.Frame()
	tt.Frame()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// 内容全变：文本换掉，行模型作废重建（等同于重新加载一批改动）
		for fi := range files {
			for hi := range files[fi].Hunks {
				for li := range files[fi].Hunks[hi].Lines {
					line := &files[fi].Hunks[hi].Lines[li]
					line.Text = "gen" + strconv.Itoa(i) + " " + line.Text
				}
			}
		}
		diffPanelMu.Lock()
		dp.rowsRev++
		dp.rebuildDiffRowsLocked()
		diffPanelMu.Unlock()
		tt.Frame()
	}
}

// BenchmarkDiffPanelFrame1x4990：单文件 4990 行（低于延迟解析阈值 5000 行），
// 超过大文件折叠阈值 2000 行，默认只显示文件头 + 折叠占位行（验收 < 2ms）。
func BenchmarkDiffPanelFrame1x4990(b *testing.B) {
	files := benchDiffFiles(1, 4990)
	dp := benchDiffPanelState(files)
	gen := 0
	tt := ui.NewTester(benchDiffPanelView(dp, files, &gen), 1920, 1080)
	tt.Frame()
	tt.Frame()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tt.Frame()
	}
}

// BenchmarkDiffPanelFrame1x4990Expanded：用户手动展开大文件后，4990 行全部进入行模型，
// 但仍只渲染视口附近的行。
func BenchmarkDiffPanelFrame1x4990Expanded(b *testing.B) {
	files := benchDiffFiles(1, 4990)
	dp := benchDiffPanelState(files)
	dp.expandedLarge[files[0].NewPath] = true
	dp.rebuildDiffRowsLocked()
	gen := 0
	tt := ui.NewTester(benchDiffPanelView(dp, files, &gen), 1920, 1080)
	tt.Frame()
	tt.Frame()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tt.Frame()
	}
}

// BenchmarkBuildDiffRows5000 单独测行模型重建（切轮次或折叠/展开时的一次性成本）。
func BenchmarkBuildDiffRows5000(b *testing.B) {
	files := benchDiffFiles(50, 100)
	collapsed := make(map[string]bool)
	expandedLarge := make(map[string]bool)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// 5000 行改动 + 50 个文件头 + 50 个 hunk 头
		if rows := buildDiffRows(files, collapsed, nil, expandedLarge, nil, nil); len(rows) != 5100 {
			b.Fatalf("rows = %d, want 5100", len(rows))
		}
	}
}
