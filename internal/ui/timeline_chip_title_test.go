package mcodeui

import (
	"strings"
	"testing"
)

// TestToolChipCommandShort 验证胶囊标题对命令正文的语义提取：剥掉 shell 包装与 cd/export 之类的噪声段、
// 取循环体里的主命令、去掉 rtk/sudo 等包装前缀与 KEY=VALUE 赋值，最后把路径换成文件名。
func TestToolChipCommandShort(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want string
	}{
		{"单条命令：路径换成文件名", "gofmt -l internal/ui/a.go", "gofmt -l a.go"},
		{"跳过 cd 后取主命令，最多 4 个 token", "cd /x/y && go test ./internal/ui -run Foo -v", "go test ui -run"},
		{"for 循环取循环体里的主命令", "for f in a b; do gofmt -l $f; done", "for: gofmt -l $f"},
		{"去掉 rtk 包装前缀", "rtk git diff --stat", "git diff --stat"},
		{"去掉环境变量赋值", "GOFLAGS=-mod=mod go build ./...", "go build ..."},
		{"bash -lc 取引号内的脚本", "bash -lc 'cd x && npm test'", "npm test"},
		{"管道只取第一段", "cat a.txt | grep foo", "cat a.txt"},
		{"空命令", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolChipCommandShort(tc.cmd); got != tc.want {
				t.Fatalf("toolChipCommandShort(%q) = %q, want %q", tc.cmd, got, tc.want)
			}
		})
	}
}

// TestToolChipShortTitleDescription 验证命令胶囊优先显示输入里的 description（自然语言意图，压成单行并截断），
// 没有 description 时退回语义提取的命令摘要；读取等其他工具类别的标题不受影响。
func TestToolChipShortTitleDescription(t *testing.T) {
	cases := []struct {
		name string
		msg  Message
		want string
	}{
		{
			name: "命令：优先用 description 并压成单行",
			msg: Message{
				Role:     "tool",
				ToolName: "run_command",
				ToolInput: `{"command":"cd /x && go test ./internal/ui",` +
					`"description":"运行 ui 包的\n单元测试"}`,
			},
			want: "运行 ui 包的 单元测试",
		},
		{
			name: "命令：description 超长截断到 40 个字符",
			msg: Message{
				Role:      "tool",
				ToolName:  "run_command",
				ToolInput: `{"command":"ls","description":"` + strings.Repeat("测", 45) + `"}`,
			},
			want: strings.Repeat("测", 40) + "…",
		},
		{
			name: "命令：没有 description 时用语义提取的摘要",
			msg: Message{
				Role:      "tool",
				ToolName:  "run_command",
				ToolInput: `{"command":"for f in a b; do gofmt -l $f; done"}`,
			},
			want: "for: gofmt -l $f",
		},
		{
			name: "读取工具：标题仍是文件名",
			msg: Message{
				Role:      "tool",
				ToolName:  "read_file",
				ToolInput: `{"file_path":"/repo/pkg/math.go","description":"读取数学包"}`,
			},
			want: "math.go",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verb, target, _ := toolCallTitle(tc.msg)
			if got := toolChipShortTitle(tc.msg, verb, target); got != tc.want {
				t.Fatalf("toolChipShortTitle(...) = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestToolCallTitleWithoutJSONInput 验证 ToolInput 不是 JSON 时不会把输出文本按工具名字节数切开：
// 只有首行确实以工具名开头才剥掉工具名，否则保持原文，中文不会被切成乱码。
func TestToolCallTitleWithoutJSONInput(t *testing.T) {
	cases := []struct {
		name string
		msg  Message
		want string
	}{
		{
			name: "输出不以工具名开头时保持原文",
			msg:  Message{ID: "chip-title-output", Role: "tool", Kind: "tool", ToolName: "bash", ToolInput: "演示命令", Text: "演示输出：completed"},
			want: "演示输出：completed",
		},
		{
			name: "首行以工具名开头时剥掉工具名",
			msg:  Message{ID: "chip-title-prefix", Role: "tool", Kind: "tool", ToolName: "bash", Text: "bash 列出 文件"},
			want: "列出 文件",
		},
		{
			name: "JSON 输入优先于输出文本",
			msg:  Message{ID: "chip-title-json", Role: "tool", Kind: "tool", ToolName: "bash", ToolInput: `{"command":"演示命令"}`, Text: "演示输出：completed"},
			want: "演示命令",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, target, _ := toolCallTitle(tc.msg); target != tc.want {
				t.Fatalf("toolCallTitle target = %q, want %q", target, tc.want)
			}
		})
	}
}
