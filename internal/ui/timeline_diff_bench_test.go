package mcodeui

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 时间线内联 diff 的帧级基准。
//
// 场景：会话里有 10 条自带 unified diff 的文件修改工具消息，每条的内嵌补丁 2000 行，
// 每帧都会走到 renderInlineToolDiff（内联 diff 展开状态），用来测“解析 + 统计 + 显示行
// 组装 + 渲染一帧”的成本：
//
//	go test -run '^$' -bench InlineToolDiff -benchmem -timeout 300s ./internal/ui/

// benchInlineToolDiffPatch 生成单文件 lines 行的 unified diff，行序按「上下文/删除/新增/上下文」循环。
func benchInlineToolDiffPatch(seed string, lines int) string {
	var b strings.Builder
	b.Grow(lines*48 + 128)
	b.WriteString("diff --git a/pkg/bench.go b/pkg/bench.go\n")
	b.WriteString("--- a/pkg/bench.go\n")
	b.WriteString("+++ b/pkg/bench.go\n")
	// 每 4 行一组：1 行上下文、1 行删除、1 行新增、1 行上下文，两侧行数都是 lines*3/4
	side := lines * 3 / 4
	b.WriteString("@@ -1," + strconv.Itoa(side) + " +1," + strconv.Itoa(side) + " @@\n")
	for i := 0; i < lines; i++ {
		switch i % 4 {
		case 0:
			b.WriteString(" \tvalue := " + seed + strconv.Itoa(i) + "\n")
		case 1:
			b.WriteString("-\toldValue := " + seed + strconv.Itoa(i) + "\n")
		case 2:
			b.WriteString("+\tnewValue := " + seed + strconv.Itoa(i) + "\n")
		default:
			b.WriteString(" \treturn value\n")
		}
	}
	return b.String()
}

// benchInlineToolDiffMessages 造 count 条内联 diff 工具消息，每条补丁 lines 行，消息 ID 各不相同。
func benchInlineToolDiffMessages(count, lines int) []Message {
	msgs := make([]Message, count)
	for i := range msgs {
		msgs[i] = Message{
			ID:        fmt.Sprintf("bench-diff-%d", i),
			Role:      "tool",
			Text:      "apply_patch {}\n" + benchInlineToolDiffPatch(fmt.Sprintf("bench%02d-", i), lines),
			TurnIndex: i + 1,
		}
	}
	return msgs
}

// BenchmarkInlineToolDiffFrame10msgs 测量 10 个含 2000 行内嵌补丁的工具消息静止一帧的耗时与分配。
func BenchmarkInlineToolDiffFrame10msgs(b *testing.B) {
	s := newChatState(NewFakeClient())
	msgs := benchInlineToolDiffMessages(10, 2000)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() {
			for _, m := range msgs {
				renderInlineToolDiff(c, s, m)
			}
		})
	}, 1200, 900)
	// 先空跑两帧预热，让测试窗口与节点树稳定
	tt.Frame()
	tt.Frame()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tt.Frame()
	}
}

// BenchmarkInlineToolDiffData10msgs 测量 10 条含 2000 行内嵌补丁消息的取数成本（缓存命中）：
// 只做指纹比对、折叠状态比对与显示行复用，不建任何控件。
func BenchmarkInlineToolDiffData10msgs(b *testing.B) {
	s := newChatState(NewFakeClient())
	msgs := benchInlineToolDiffMessages(10, 2000)
	for _, m := range msgs {
		inlineToolDiffLoadForTest(s, m)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, m := range msgs {
			inlineToolDiffLoadForTest(s, m)
		}
	}
}

// BenchmarkInlineToolDiffData10msgsUncached 每轮清空缓存后再取数，等价于改动前「每帧重新解析、
// 重新筛选、重新算显示行」的成本，用作缓存命中基准的对照。
func BenchmarkInlineToolDiffData10msgsUncached(b *testing.B) {
	s := newChatState(NewFakeClient())
	msgs := benchInlineToolDiffMessages(10, 2000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.mu.Lock()
		s.toolDiff.inline = nil
		s.toolDiff.inlineSeq = nil
		s.mu.Unlock()
		for _, m := range msgs {
			inlineToolDiffLoadForTest(s, m)
		}
	}
}
