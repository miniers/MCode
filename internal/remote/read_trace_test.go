package remote

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// TestExtractReadPath_Keys 验证各候选键的提取与优先级顺序。
func TestExtractReadPath_Keys(t *testing.T) {
	workDir := t.TempDir()

	tests := []struct {
		name     string
		input    string
		workDir  string
		wantPath string
		wantOK   bool
	}{
		{
			name:     "file_path key",
			input:    `{"file_path": "internal/foo.go"}`,
			workDir:  workDir,
			wantPath: "internal/foo.go",
			wantOK:   true,
		},
		{
			name:     "filePath key",
			input:    `{"filePath": "internal/bar.go"}`,
			workDir:  workDir,
			wantPath: "internal/bar.go",
			wantOK:   true,
		},
		{
			name:     "path key",
			input:    `{"path": "cmd/main.go"}`,
			workDir:  workDir,
			wantPath: "cmd/main.go",
			wantOK:   true,
		},
		{
			name:     "absolute_path key under workDir",
			input:    fmt.Sprintf(`{"absolute_path": %q}`, filepath.Join(workDir, "pkg", "util.go")),
			workDir:  workDir,
			wantPath: "pkg/util.go",
			wantOK:   true,
		},
		{
			name:     "AbsolutePath key under workDir",
			input:    fmt.Sprintf(`{"AbsolutePath": %q}`, filepath.Join(workDir, "pkg", "clean.go")),
			workDir:  workDir,
			wantPath: "pkg/clean.go",
			wantOK:   true,
		},
		{
			name:     "target_file key",
			input:    `{"target_file": "internal/target.go"}`,
			workDir:  workDir,
			wantPath: "internal/target.go",
			wantOK:   true,
		},
		{
			name:     "TargetFile key",
			input:    `{"TargetFile": "internal/target2.go"}`,
			workDir:  workDir,
			wantPath: "internal/target2.go",
			wantOK:   true,
		},
		{
			name:     "key precedence: file_path precedes AbsolutePath",
			input:    `{"AbsolutePath": "/workspace/project/other.go", "file_path": "first.go"}`,
			workDir:  workDir,
			wantPath: "first.go",
			wantOK:   true,
		},
		{
			name:     "absolute path outside workDir",
			input:    `{"file_path": "/etc/config.json"}`,
			workDir:  workDir,
			wantPath: "/etc/config.json",
			wantOK:   true,
		},
		{
			name:     "empty input",
			input:    "",
			workDir:  workDir,
			wantPath: "",
			wantOK:   false,
		},
		{
			name:     "no matching keys",
			input:    `{"other": "value", "count": 123}`,
			workDir:  workDir,
			wantPath: "",
			wantOK:   false,
		},
		{
			name:     "cleaning dots and redundant slashes",
			input:    `{"file_path": "a/./b/../c/file.go"}`,
			workDir:  workDir,
			wantPath: "a/c/file.go",
			wantOK:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotPath, ok := extractReadPath(json.RawMessage(tc.input), tc.workDir)
			if ok != tc.wantOK {
				t.Fatalf("extractReadPath ok = %v, want %v", ok, tc.wantOK)
			}
			if gotPath != tc.wantPath {
				t.Fatalf("extractReadPath path = %q, want %q", gotPath, tc.wantPath)
			}
		})
	}
}

// TestMemoryBackend_RecordReadPaths 验证记录路径去重、字典序排序与条目上限。
func TestMemoryBackend_RecordReadPaths(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	threadID := "thread-test-1"

	// 未记录时返回 nil
	if paths := backend.ThreadReadPaths(threadID); paths != nil {
		t.Fatalf("期望未记录时返回 nil，实际: %v", paths)
	}

	// 记录多条路径，包含重复
	backend.recordReadPath(threadID, "pkg/b.go")
	backend.recordReadPath(threadID, "internal/a.go")
	backend.recordReadPath(threadID, "pkg/b.go") // 重复项
	backend.recordReadPath(threadID, "cmd/main.go")

	got := backend.ThreadReadPaths(threadID)
	want := []string{"cmd/main.go", "internal/a.go", "pkg/b.go"}
	if len(got) != len(want) {
		t.Fatalf("期望长度 %d，实际: %v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("索引 %d 期望 %q，实际: %q", i, want[i], got[i])
		}
	}

	// 验证 1000 条上限保护
	cappedThread := "thread-capped"
	for i := 0; i < 1050; i++ {
		backend.recordReadPath(cappedThread, fmt.Sprintf("file_%04d.go", i))
	}
	cappedPaths := backend.ThreadReadPaths(cappedThread)
	if len(cappedPaths) != 1000 {
		t.Fatalf("期望达到上限 1000 条，实际: %d", len(cappedPaths))
	}
}

// TestMemoryBackend_ToolStartExtractAndDeduplicate 验证基于 extractReadPath 驱动的工具事件读取记录与去重。
func TestMemoryBackend_ToolStartExtractAndDeduplicate(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	workDir := filepath.Join(t.TempDir(), "workspace")
	threadID := "thread-tool-events"

	inputs := []json.RawMessage{
		json.RawMessage(fmt.Sprintf(`{"file_path": %q}`, filepath.Join(workDir, "internal/server.go"))),
		json.RawMessage(fmt.Sprintf(`{"AbsolutePath": %q}`, filepath.Join(workDir, "internal/server.go"))), // 重复
		json.RawMessage(fmt.Sprintf(`{"path": %q}`, filepath.Join(workDir, "config/config.json"))),
	}

	for _, in := range inputs {
		if p, ok := extractReadPath(in, workDir); ok {
			backend.recordReadPath(threadID, p)
		}
	}

	got := backend.ThreadReadPaths(threadID)
	want := []string{"config/config.json", "internal/server.go"}
	if len(got) != len(want) {
		t.Fatalf("期望长度 %d，实际: %v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("索引 %d 期望 %q，实际: %q", i, want[i], got[i])
		}
	}
}
