package mcodeui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 聊天时间线工具行的帧级基准。
//
// 场景：窗口只开着一个会话的聊天区，时间线里 200 条工具消息（bash 长输出、读取大文件、
// apply_patch 补丁、ToolInput 为空的旧消息）四类循环，中间穿插少量助手正文，
// 每条工具各占一个回合，因此不会被聚合成工具组或折进回合折叠区，而是逐条走
// renderSingleToolItem → renderToolChip → parseCommandToolInfo / toolCallFields。
// 用来测“重建时间线 + 渲染一帧”的开销，并配合 -cpuprofile 找热点：
//
//	go test -run '^$' -bench TimelineToolRows -benchmem -cpuprofile /tmp/timeline.cpu -timeout 300s ./internal/ui/

const (
	benchTimelineThreadID  = "t1"
	benchTimelineToolCount = 200
)

// benchTimelineBlob 生成约 size 字节的多行文本；每行都不同，避免内容因为重复而被优化掉。
func benchTimelineBlob(seed string, size int) string {
	var b strings.Builder
	b.Grow(size + 64)
	for i := 0; b.Len() < size; i++ {
		b.WriteString(seed)
		b.WriteString(" 第 ")
		b.WriteString(strconv.Itoa(i))
		b.WriteString(" 行：ok  mcode/internal/ui  0.0")
		b.WriteString(strconv.Itoa(i % 10))
		b.WriteString("s\n")
	}
	return b.String()
}

// benchTimelineAssistantText 是一小段助手正文，穿插在工具之间把消息分布到多轮。
func benchTimelineAssistantText(i int) string {
	return "第 " + strconv.Itoa(i) + " 轮核对：已看过 `renderSingleToolItem` 的调用链，" +
		"确认工具行每帧都会重新渲染，命令正文、退出码与耗时都取自消息字段。\n" +
		"接下来继续核对读取与补丁的展示，长输出不应该把整帧拖慢。\n"
}

// benchTimelinePatchInput 用 encoding/json 造 apply_patch 的结构化输入：
// patch 字段里是多行 diff，带 diff --git 与 +++ b/ 头，路径从补丁正文里提取。
func benchTimelinePatchInput(b *testing.B, i int) string {
	b.Helper()
	line := strconv.Itoa(100 + i)
	patch := "diff --git a/internal/ui/timeline_rows.go b/internal/ui/timeline_rows.go\n" +
		"--- a/internal/ui/timeline_rows.go\n" +
		"+++ b/internal/ui/timeline_rows.go\n" +
		"@@ -" + line + ",4 +" + line + ",5 @@\n" +
		" func renderToolChip(c *ui.Context, s *chatState, m Message, beatTools []Message) {\n" +
		"+\t// 基准用例：第 " + strconv.Itoa(i) + " 次补丁，核对胶囊标签与耗时文本。\n" +
		" \tverb, target, detail := toolCallTitle(m)\n" +
		"-\tcmdInfo := parseCommandToolInfo(m)\n" +
		"+\tcmdInfo := parseCommandToolInfo(m) // 命令正文按消息 ID 缓存\n"
	raw, err := json.Marshal(map[string]string{
		"file_path": "internal/ui/timeline_rows.go",
		"patch":     patch,
	})
	if err != nil {
		b.Fatalf("构造补丁输入失败：%v", err)
	}
	return string(raw)
}

// benchTimelineMessages 一次性造出基准用的会话记录：200 条工具消息（四类循环）＋穿插的助手正文，
// 回合号递增，使消息分布在一个会话的多轮里；末尾留一条正在流式输出的助手正文。
// setup 只调用一次，循环里不再重新生成这些消息。
func benchTimelineMessages(b *testing.B) []Message {
	b.Helper()
	msgs := make([]Message, 0, benchTimelineToolCount+benchTimelineToolCount/5+1)
	base := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	for i := 0; i < benchTimelineToolCount; i++ {
		// 每条工具独占一个回合：同回合的活动消息满两条才会聚成工具组，
		// 一条时按单行渲染，正好走到工具行的解析与胶囊渲染。
		turn := i + 1
		if i%5 == 0 {
			msgs = append(msgs, Message{
				ID: "tl-asst-" + strconv.Itoa(i), Role: "assistant", Kind: "text",
				TurnIndex: turn, IsFinal: true, Text: benchTimelineAssistantText(i),
			})
		}
		start := base.Add(time.Duration(i) * time.Minute)
		done := start.Add(time.Duration(900+i*10) * time.Millisecond)
		id := "tl-tool-" + strconv.Itoa(i)
		switch i % 4 {
		case 0:
			// bash：结构化输入 + 约 4KB 多行输出，末尾带退出码文本。
			msgs = append(msgs, Message{
				ID: id, Role: "tool", Kind: "tool", TurnIndex: turn, IsFinal: true,
				ToolName:        "bash",
				ToolStatus:      "completed",
				ToolInput:       fmt.Sprintf(`{"command":"go test ./internal/ui/ -run '^$' -bench Timeline -count=%d"}`, i%5+1),
				Text:            benchTimelineBlob("ok  ", 4096) + "exit code: 0\n",
				CreatedAt:       start.Format(time.RFC3339Nano),
				ToolCompletedAt: done.Format(time.RFC3339Nano),
			})
		case 1:
			// read：路径输入 + 约 8KB 文件正文。
			msgs = append(msgs, Message{
				ID: id, Role: "tool", Kind: "tool", TurnIndex: turn, IsFinal: true,
				ToolName:   "read_file",
				ToolStatus: "completed",
				ToolInput:  fmt.Sprintf(`{"file_path":"internal/ui/timeline_rows.go","offset":%d,"limit":400}`, 100+i),
				Text:       benchTimelineBlob("1\t", 8192),
			})
		case 2:
			// edit：patch 输入（多行 diff）+ 约 2KB 改动摘要。
			msgs = append(msgs, Message{
				ID: id, Role: "tool", Kind: "tool", TurnIndex: turn, IsFinal: true,
				ToolName:   "apply_patch",
				ToolStatus: "completed",
				ToolInput:  benchTimelinePatchInput(b, i),
				Text:       benchTimelineBlob("+", 2048),
			})
		default:
			// 旧消息：ToolInput 为空，Text 首行是「工具名 + JSON」，解析要走文本回退。
			msgs = append(msgs, Message{
				ID: id, Role: "tool", Kind: "tool", TurnIndex: turn, IsFinal: true,
				ToolName:   "bash",
				ToolStatus: "completed",
				Text:       `bash {"command":"ls -la internal/ui"}` + "\n" + benchTimelineBlob("drwxr-xr-x", 1024) + "exit code: 0\n",
			})
		}
	}
	// 末尾留一条正在流式输出的正文，流式增量基准往它后面追加。
	msgs = append(msgs, Message{
		ID: "tl-live", Role: "assistant", Kind: "text",
		TurnIndex: benchTimelineToolCount + 1, Text: benchTimelineAssistantText(0),
	})
	return msgs
}

// timelineFrameBenchState 是基准的固定场景：一个开着单会话聊天区的测试窗口。
type timelineFrameBenchState struct {
	s  *chatState
	tt *ui.Tester
	th Thread
	// baseText 是末尾流式正文的初始内容：增量在它后面追加一段固定长度的新文本，
	// 每次迭代的正文长度稳定，基准数值不会随迭代次数增长。
	baseText string
}

// newTimelineFrameBenchState 构造基准场景并跑几帧热起来（字体度量、时间线缓存、滚动到底）。
func newTimelineFrameBenchState(b *testing.B) *timelineFrameBenchState {
	b.Helper()
	// TestMain 把 Stat 限频置 0 方便用例改写文件即生效；基准要反映线上行为，故恢复成线上默认的 1s。
	lastModelSelectionMu.Lock()
	lastSelectionStatInterval = time.Second
	lastModelSelectionMu.Unlock()
	b.Cleanup(func() {
		lastModelSelectionMu.Lock()
		lastSelectionStatInterval = 0
		lastModelSelectionMu.Unlock()
	})
	// TestMain 把自建 worktree 重新识别限频也置 0 了；基准要反映线上行为，同样恢复成线上默认的 5s。
	aiWorktreeRedetectInterval = 5 * time.Second
	b.Cleanup(func() {
		aiWorktreeRedetectInterval = 0
	})

	fake := NewFakeClient()
	fake.SetProviders(benchSwarmProviders())
	fake.SetMessages(benchTimelineThreadID, benchTimelineMessages(b))

	s, view := buildRoot(fake)
	st := NewSettingsState(b.TempDir(), nil, s)
	setChatSettings(s, st)

	frozen := func(c *ui.Context) {
		// 基准只量“静止的一帧”：动画（侧边栏滑动、时间线淡入）会让测试框架反复请求下一帧，
		// 关闭动画后每次 Frame 就是一次完整的界面构建，数值才是可比较的单帧成本。
		c.DisableAnimations()
		view(c)
	}
	tt := ui.NewTester(frozen, 1920, 1080)
	// 会话列表与历史消息都是异步加载的，等会话选中并且 200 条工具消息都进了时间线。
	waitBench(b, "会话选中与时间线工具消息加载", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.selectedThreadID != benchTimelineThreadID {
			return false
		}
		tools := 0
		for _, m := range s.messages {
			if m.Role == "tool" {
				tools++
			}
		}
		return tools == benchTimelineToolCount
	})

	var th Thread
	s.mu.Lock()
	for _, t := range s.threads {
		if t.ID == benchTimelineThreadID {
			th = t
			break
		}
	}
	baseText := ""
	if n := len(s.messages); n > 0 {
		baseText = s.messages[n-1].Text
	}
	s.mu.Unlock()

	// 热两帧：首帧建字体度量与时间线缓存，第二帧才回到稳态。
	tt.Frame()
	tt.Frame()

	// 场景校验：工具行确实逐条进了时间线（没有被聚合成工具组或被回合折叠收起来），
	// 否则基准测的就不是工具行渲染，数值会误导人。
	s.mu.Lock()
	singleTools := 0
	for _, item := range s.timelineCacheItems {
		if item.kind == timelineItemSingle && item.msg.Role == "tool" {
			singleTools++
		}
	}
	s.mu.Unlock()
	if singleTools != benchTimelineToolCount {
		b.Fatalf("基准场景应有 %d 条单行工具条目，实际 %d", benchTimelineToolCount, singleTools)
	}

	return &timelineFrameBenchState{s: s, tt: tt, th: th, baseText: baseText}
}

// forceTimelineRebuild 强制下一帧全量重建时间线。
// 窗口空闲、消息没有变化时，renderChat 会因为 messagesRev 与 timelineCacheRev 一致而复用时间线条目，
// 一帧只剩可见行的绘制；这里把版本号往前推、并清掉指纹快照，让每帧都走 buildTimelineItemsOpts
// 的全量重建分支，基准测到的就是“重建时间线条目 + 渲染可见行”的真实单帧成本。
func (f *timelineFrameBenchState) forceTimelineRebuild() {
	f.s.mu.Lock()
	f.s.messagesRev++
	f.s.timelineSnapshotMsgs = nil
	f.s.mu.Unlock()
}

// appendStreamDelta 模拟末尾助手消息的流式增量：换成“原文 + 一段新增量”，
// 再走一遍与后端 Watch 增量相同的更新路径（会请求重绘）。
func (f *timelineFrameBenchState) appendStreamDelta(i int) {
	f.s.mu.Lock()
	if len(f.s.messages) == 0 {
		f.s.mu.Unlock()
		return
	}
	last := f.s.messages[len(f.s.messages)-1]
	last.Text = f.baseText + "\n\n增量片段 " + strconv.Itoa(i) +
		"：继续核对工具行的解析与渲染，确认命令正文、退出码与耗时都取自消息字段。"
	th := f.th
	f.s.mu.Unlock()
	f.s.onWatchUpdate(ThreadUpdate{ThreadID: th.ID, Thread: th, Messages: []Message{last}})
}

// BenchmarkTimelineToolRowsFrame 测量时间线里 200 条工具行重建并渲染一帧的耗时与分配。
func BenchmarkTimelineToolRowsFrame(b *testing.B) {
	f := newTimelineFrameBenchState(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f.forceTimelineRebuild()
		f.tt.Frame()
	}
	b.StopTimer()
	if f.tt.FramePending() {
		b.Logf("注意：静止帧之后仍有一帧待渲染")
	}
}

// BenchmarkTimelineToolRowsStreamDelta 测量末尾正文追加一段流式增量后重绘一帧的耗时与分配。
func BenchmarkTimelineToolRowsStreamDelta(b *testing.B) {
	f := newTimelineFrameBenchState(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f.appendStreamDelta(i)
		f.tt.Frame()
	}
}
