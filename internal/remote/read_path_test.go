package remote

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestExtractReadPaths 验证按工具类型与 shell 命令识别被读取的文件路径。
func TestExtractReadPaths(t *testing.T) {
	workDir := t.TempDir()
	// 真实存在的目录：路径是目录时不应被记为「读过」。
	dirPath := filepath.Join(workDir, "internal")
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		t.Fatalf("准备目录失败: %v", err)
	}

	// 命令里带绝对路径，必须用 json.Marshal 生成，避免内层双引号破坏 JSON。
	bashAbsInput, err := json.Marshal(map[string]string{
		"command": "cat " + filepath.Join(workDir, "internal", "abs2.go"),
	})
	if err != nil {
		t.Fatalf("构造 Bash 绝对路径入参失败: %v", err)
	}

	tests := []struct {
		name     string
		toolName string
		input    string
		want     []string
	}{
		{
			name:     "Read 记录 file_path",
			toolName: "Read",
			input:    `{"file_path": "internal/foo/bar.go"}`,
			want:     []string{"internal/foo/bar.go"},
		},
		{
			name:     "Grep 的 path 不作为读取记录",
			toolName: "Grep",
			input:    `{"pattern": "TODO", "path": "internal/foo/bar.go"}`,
			want:     nil,
		},
		{
			name:     "Grep 指向目录时丢弃",
			toolName: "Grep",
			input:    fmt.Sprintf(`{"pattern": "TODO", "path": %q}`, dirPath),
			want:     nil,
		},
		{
			name:     "Read 指向真实目录时丢弃",
			toolName: "Read",
			input:    fmt.Sprintf(`{"file_path": %q}`, dirPath),
			want:     nil,
		},
		{
			name:     "Edit 不记录",
			toolName: "Edit",
			input:    `{"file_path": "internal/foo/bar.go", "old_string": "a", "new_string": "b"}`,
			want:     nil,
		},
		{
			name:     "Write 不记录",
			toolName: "Write",
			input:    `{"file_path": "internal/foo/bar.go", "content": "hi"}`,
			want:     nil,
		},
		{
			name:     "Bash sed 读取文件",
			toolName: "Bash",
			input:    `{"command": "sed -n '1,20p' internal/foo/bar.go"}`,
			want:     []string{"internal/foo/bar.go"},
		},
		{
			name:     "Bash 管道读取文件",
			toolName: "Bash",
			input:    `{"command": "cat a.go | head -5"}`,
			want:     []string{"a.go"},
		},
		{
			name:     "Bash 多处读取去重",
			toolName: "Bash",
			input:    `{"command": "cat a.go && head -1 a.go; wc -l b/c.txt"}`,
			want:     []string{"a.go", "b/c.txt"},
		},
		{
			name:     "Bash 使用 cmd 字段",
			toolName: "shell",
			input:    `{"cmd": "tail -n 2 internal/foo/bar.go"}`,
			want:     []string{"internal/foo/bar.go"},
		},
		{
			name:     "command 为字符串数组（bash -lc 形式）",
			toolName: "shell",
			input:    `{"command": ["bash", "-lc", "sed -n '1,20p' internal/array.go | head -3"]}`,
			want:     []string{"internal/array.go"},
		},
		{
			name:     "command 为字符串数组（命令与参数分开）",
			toolName: "shell",
			input:    `{"command": ["cat", "internal/split.go"]}`,
			want:     []string{"internal/split.go"},
		},
		{
			name:     "绝对路径在 workDir 内换算为相对路径",
			toolName: "Read",
			input:    fmt.Sprintf(`{"AbsolutePath": %q}`, filepath.Join(workDir, "internal", "abs.go")),
			want:     []string{"internal/abs.go"},
		},
		{
			name:     "Bash 绝对路径在 workDir 内换算为相对路径",
			toolName: "Bash",
			input:    string(bashAbsInput),
			want:     []string{"internal/abs2.go"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractReadPaths(tc.toolName, json.RawMessage(tc.input), workDir)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("extractReadPaths(%q) = %v, want %v", tc.toolName, got, tc.want)
			}
		})
	}
}
