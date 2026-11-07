package mcodeui

import (
	"strings"
	"testing"
)

// TestToolCallTitle 验证工具调用行的可读标题：动词、对象（文件名/模式串/命令正文）与补充信息
// （行范围、搜索范围）都从输入字段生成，不再回显被截断的原始 JSON。
func TestToolCallTitle(t *testing.T) {
	cases := []struct {
		name   string
		msg    Message
		want   [3]string // verb, target, detail
		absent string    // 标题里不应出现的片段（一般为原始 JSON）
	}{
		{
			name: "读取工具：字段名首字母大写也能取到路径与结束行",
			msg: Message{
				Role: "tool",
				Text: `read_file {"EndLine":525,"Path":"/home/user/mcode/internal/ui/timeline_rows.go"}`,
			},
			want:   [3]string{"读取", "timeline_rows.go", ":1–525"},
			absent: "{",
		},
		{
			name: "读取工具：StartLine/EndLine 显示为行范围",
			msg: Message{
				Role:      "tool",
				ToolName:  "read_file",
				ToolInput: `{"file_path":"/repo/pkg/math.go","start_line":120,"end_line":525}`,
			},
			want: [3]string{"读取", "math.go", ":120–525"},
		},
		{
			name: "读取工具：offset/limit 换算成结束行",
			msg: Message{
				Role: "tool",
				Text: `mcp__fs__read_file {"path":"a.go","offset":10,"limit":5}`,
			},
			want: [3]string{"读取", "a.go", ":10–15"},
		},
		{
			name: "编辑工具：用 ToolInput 的 Path 字段（截断的 Text 不影响标题）",
			msg: Message{
				Role:      "tool",
				ToolName:  "edit_file",
				ToolInput: `{"Path":"/repo/pkg/math.go","OldString":"a","NewString":"b"}`,
				Text:      `edit_file {"Path":"/repo/pkg/math.go","OldStr`,
			},
			want:   [3]string{"编辑", "math.go", ""},
			absent: "{",
		},
		{
			name: "写入工具算新建",
			msg: Message{
				Role:      "tool",
				ToolName:  "write_file",
				ToolInput: `{"path":"pkg/new.go","content":"x"}`,
			},
			want: [3]string{"新建", "new.go", ""},
		},
		{
			name: "apply_patch 的路径来自补丁头",
			msg: Message{
				Role: "tool",
				Text: `apply_patch {"input":"*** Begin Patch\n*** Update File: src/a.go\n"}`,
			},
			want: [3]string{"编辑", "a.go", ""},
		},
		{
			name: "apply_patch 入参无路径时从 diff --git 正文取文件名",
			msg: Message{
				Role: "tool",
				Text: "apply_patch {}\ndiff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-EMBED-OLD\n+EMBED-NEW\n",
			},
			want:   [3]string{"编辑", "x.go", ""},
			absent: "{",
		},
		{
			name: "apply_patch 入参无路径时从 *** Update File 正文取文件名",
			msg: Message{
				Role: "tool",
				Text: "apply_patch {}\n*** Begin Patch\n*** Update File: src/a.go\n@@\n-old\n+new\n*** End Patch\n",
			},
			want:   [3]string{"编辑", "a.go", ""},
			absent: "{",
		},
		{
			name: "apply_patch 正文含多个文件时标题只给文件数",
			msg: Message{
				Role: "tool",
				Text: "apply_patch {}\ndiff --git a/pkg/a.go b/pkg/a.go\n--- a/pkg/a.go\n+++ b/pkg/a.go\n@@ -1 +1 @@\n-A\n+B\ndiff --git a/pkg/b.go b/pkg/b.go\n--- a/pkg/b.go\n+++ b/pkg/b.go\n@@ -1 +1 @@\n-C\n+D\n",
			},
			want:   [3]string{"编辑", "2 个文件", ""},
			absent: "{",
		},
		{
			name: "apply_patch 入参补丁头含多个文件时标题只给文件数",
			msg: Message{
				Role: "tool",
				Text: "apply_patch {}\n*** Begin Patch\n*** Update File: src/a.go\n*** Add File: src/b.go\n*** End Patch\n",
			},
			want: [3]string{"编辑", "2 个文件", ""},
		},
		{
			name: "搜索工具：pattern 加引号，路径作为范围",
			msg: Message{
				Role: "tool",
				Text: `Grep {"pattern":"todo","path":"internal/ui"}`,
			},
			want: [3]string{"搜索", `"todo"`, "internal/ui"},
		},
		{
			name: "命令工具保持 $ 命令",
			msg: Message{
				Role: "tool",
				Text: `Bash {"command":"rg -n TIMELINE internal/ui"}`,
			},
			want: [3]string{"$", "rg -n TIMELINE internal/ui", ""},
		},
		{
			name: "未知工具用工具名与第一个字符串参数",
			msg: Message{
				Role: "tool",
				Text: `mcp__foo__bar {"x":"hello","y":1}`,
			},
			want:   [3]string{"mcp__foo__bar", "hello", ""},
			absent: "{",
		},
		{
			name: "没有任何输入时只给动词",
			msg:  Message{Role: "tool", Text: `list_things`},
			want: [3]string{"list_things", "", ""},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verb, target, detail := toolCallTitle(tc.msg)
			if got := [3]string{verb, target, detail}; got != tc.want {
				t.Fatalf("toolCallTitle() = %#v, want %#v", got, tc.want)
			}
			if tc.absent != "" {
				for _, part := range []string{verb, target, detail} {
					if strings.Contains(part, tc.absent) {
						t.Fatalf("标题不应包含 %q: %#v", tc.absent, []string{verb, target, detail})
					}
				}
			}
		})
	}
}

// TestToolCallDiffStat 验证差异角标只统计该次调用自身的改动：
// 输入里的 old/new 文本、写入内容或本次输出自带的 unified diff；拿不到数据时不显示（0/0），
// 绝不回退到整轮汇总——那正是之前所有 edit 行都显示同一对数字的原因。
func TestToolCallDiffStat(t *testing.T) {
	cases := []struct {
		name string
		msg  Message
		adds int
		dels int
	}{
		{
			name: "单行替换",
			msg: Message{
				Role:      "tool",
				ToolName:  "edit_file",
				ToolInput: `{"Path":"a.go","OldString":"a\nb\nc","NewString":"a\nB\nc"}`,
			},
			adds: 1,
			dels: 1,
		},
		{
			name: "尾部新增两行",
			msg: Message{
				Role:      "tool",
				ToolName:  "edit",
				ToolInput: `{"file_path":"a.go","old_string":"a\nb","new_string":"a\nb\nc\nd"}`,
			},
			adds: 2,
		},
		{
			name: "写入文件按内容行数计新增",
			msg: Message{
				Role:      "tool",
				ToolName:  "write_file",
				ToolInput: `{"path":"pkg/new.go","content":"x\ny\nz"}`,
			},
			adds: 3,
		},
		{
			name: "第二次编辑同一文件只算自己那几行改动",
			msg: Message{
				Role:      "tool",
				ToolName:  "edit_file",
				ToolInput: `{"Path":"a.go","OldString":"x","NewString":"x\ny"}`,
			},
			adds: 1,
		},
		{
			name: "只有路径没有改动内容时不显示",
			msg: Message{
				Role:      "tool",
				ToolName:  "edit_file",
				ToolInput: `{"Path":"/repo/a.go"}`,
			},
		},
		{
			name: "apply_patch 按补丁行统计",
			msg: Message{
				Role:      "tool",
				ToolName:  "apply_patch",
				ToolInput: `{"input":"*** Begin Patch\n*** Update File: src/a.go\n@@\n-old\n+new1\n+new2\n*** End Patch"}`,
			},
			adds: 2,
			dels: 1,
		},
		{
			name: "输出自带 unified diff 时直接解析",
			msg: Message{
				Role: "tool",
				Text: "apply_patch {}\ndiff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-OLD\n+NEW\n",
			},
			adds: 1,
			dels: 1,
		},
		{
			name: "非文件修改工具没有差异角标",
			msg:  Message{Role: "tool", Text: `Bash {"command":"ls"}`},
		},
		{
			name: "输出里带 diff 但工具不是文件修改类时不算",
			msg:  Message{Role: "tool", Text: `Bash {"command":"git diff"}`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adds, dels := toolCallDiffStat(tc.msg)
			if adds != tc.adds || dels != tc.dels {
				t.Fatalf("toolCallDiffStat() = +%d −%d, want +%d −%d", adds, dels, tc.adds, tc.dels)
			}
		})
	}
}

// TestLineDiffStat 验证按「去掉公共前后缀行」统计单次编辑的增删行数。
func TestLineDiffStat(t *testing.T) {
	cases := []struct {
		name     string
		old, new string
		adds     int
		dels     int
	}{
		{name: "空旧文本算纯新增", new: "a\nb", adds: 2},
		{name: "空新文本算纯删除", old: "a\nb", dels: 2},
		{name: "完全相同没有改动", old: "a\nb", new: "a\nb"},
		{name: "中间替换", old: "a\nb\nc", new: "a\nB\nc", adds: 1, dels: 1},
		{name: "结尾换行不影响行数", old: "a\nb\n", new: "a\nb\n", adds: 0, dels: 0},
		{name: "CRLF 与 LF 视作同一行", old: "a\r\nb", new: "a\nb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adds, dels := lineDiffStat(tc.old, tc.new)
			if adds != tc.adds || dels != tc.dels {
				t.Fatalf("lineDiffStat() = +%d −%d, want +%d −%d", adds, dels, tc.adds, tc.dels)
			}
		})
	}
}

// TestToolGroupCountsChips 验证组头分类计数与文字摘要口径一致。
func TestToolGroupCountsChips(t *testing.T) {
	items := []Message{
		{Role: "assistant", Kind: "reasoning", Text: "想一想"},
		{Role: "tool", Text: `Bash {"command":"go build ./..."}`},
		{Role: "tool", Text: `Edit {"file_path":"a.go"}`},
		{Role: "tool", Text: `Read {"file_path":"a.go"}`},
		{Role: "tool", Text: `Grep {"pattern":"todo"}`},
		{Role: "tool", Text: `mcp__foo__bar {"x":"1"}`},
	}
	counts := countToolGroupCategories(items)
	want := toolGroupCounts{thoughts: 1, commands: 1, edits: 1, reads: 1, searches: 1, others: 1, total: 6}
	if counts != want {
		t.Fatalf("countToolGroupCategories() = %#v, want %#v", counts, want)
	}
	if got := toolGroupSummaryText(counts); got != toolGroupSummary(items) {
		t.Fatalf("摘要与计数口径不一致: %q vs %q", got, toolGroupSummary(items))
	}
}
