package mcodeui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/testenv"
)

// /export 接线测试：runLocalSlashCommand 必须清空草稿并调用 exportThread，
// 在注入的保存对话框配合下把会话历史写成 Markdown 文件。
func TestRunLocalSlashCommand_ExportWritesMarkdown(t *testing.T) {
	testenv.TempHome(t)

	fake := NewFakeClient()
	s := newChatState(fake)
	s.threads = []Thread{{
		ID:        "t1",
		ProjectID: "p1",
		Title:     "导出接线会话",
		Provider:  "claude",
		Model:     "claude-sonnet-4",
	}}
	s.selectedThreadID = "t1"
	fake.SetMessages("t1", []Message{
		{ID: "m1", Role: "user", Kind: "text", Text: "导出这条消息"},
		{ID: "m2", Role: "assistant", Kind: "text", Text: "收到"},
	})

	out := filepath.Join(t.TempDir(), "out.md")
	dialogCalled := make(chan struct{}, 1)
	old := saveExportDialog
	saveExportDialog = func(defaultPath string) (string, error) {
		select {
		case dialogCalled <- struct{}{}:
		default:
		}
		return out, nil
	}
	t.Cleanup(func() { saveExportDialog = old })

	s.draft = "/export"
	if !s.runLocalSlashCommand("t1", "/export") {
		t.Fatalf("runLocalSlashCommand('/export') expected true")
	}
	s.mu.Lock()
	draft := s.draft
	s.mu.Unlock()
	if draft != "" {
		t.Fatalf("expected draft cleared after /export, got %q", draft)
	}

	// 导出在后台 goroutine 中执行：先等保存对话框被调用（同时保证注入变量已被读取）
	select {
	case <-dialogCalled:
	case <-time.After(2 * time.Second):
		t.Fatalf("expected saveExportDialog to be called within 2s")
	}

	// 再轮询等待导出文件出现
	var content string
	deadline := time.Now().Add(2 * time.Second)
	for {
		data, err := os.ReadFile(out)
		if err == nil {
			content = string(data)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected export file %s to appear within 2s, err: %v", out, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !strings.Contains(content, "# 导出接线会话") {
		t.Fatalf("expected markdown to contain thread title, got:\n%s", content)
	}
	if !strings.Contains(content, "导出这条消息") {
		t.Fatalf("expected markdown to contain message text, got:\n%s", content)
	}
}
