package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func callPreviewOpen(t *testing.T, srv *Server, threadID, workDir string, args map[string]any) (string, bool) {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      previewOpenToolName,
			"arguments": args,
		},
	})
	if err != nil {
		t.Fatalf("marshal request failed: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if workDir != "" {
		req.Header.Set(WorkDirHeader, workDir)
	}
	rec := httptest.NewRecorder()
	if threadID != "" {
		srv.ServeThread(rec, req, threadID)
	} else {
		req.Header.Set("Authorization", "Bearer "+srv.Token())
		srv.Handler().ServeHTTP(rec, req)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("tools/call 状态码期望 200, 实际为 %d", rec.Code)
	}
	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}
	text, isError, _ := parseToolText(t, res)
	return text, isError
}

func TestPreviewOpenWorkspaceHTML(t *testing.T) {
	t.Parallel()
	var gotThread, gotTarget string
	calls := 0
	srv, st, projDir := setupTestMCPWithOptions(t, func(o *Options) {
		o.OpenPreview = func(threadID, target string) error {
			calls++
			gotThread = threadID
			gotTarget = target
			return nil
		}
	})
	registerThread(t, st, "thread-1", "proj-test-1")

	subDir := filepath.Join(projDir, "docs", "mockups")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	relFile := filepath.Join(subDir, "design.html")
	if err := os.WriteFile(relFile, []byte("<h1>hi</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	xhtmlFile := filepath.Join(projDir, "page.XHTML")
	if err := os.WriteFile(xhtmlFile, []byte("<html/>"), 0o644); err != nil {
		t.Fatal(err)
	}

	text, isError := callPreviewOpen(t, srv, "thread-1", projDir, map[string]any{"target": "docs/mockups/design.html"})
	if isError || text != "已在右侧预览打开" {
		t.Fatalf("相对路径调用失败: isError=%v text=%q", isError, text)
	}
	if calls != 1 || gotThread != "thread-1" || gotTarget != "docs/mockups/design.html" {
		t.Fatalf("回调参数异常: calls=%d thread=%q target=%q", calls, gotThread, gotTarget)
	}

	text, isError = callPreviewOpen(t, srv, "thread-1", projDir, map[string]any{"target": xhtmlFile})
	if isError || text != "已在右侧预览打开" {
		t.Fatalf("工作区内绝对路径调用失败: isError=%v text=%q", isError, text)
	}
	if calls != 2 || gotTarget != "page.XHTML" {
		t.Fatalf("绝对路径未转成相对 slash 路径: calls=%d target=%q", calls, gotTarget)
	}
}

func TestPreviewOpenRejectsInvalidTargets(t *testing.T) {
	t.Parallel()
	calls := 0
	srv, st, projDir := setupTestMCPWithOptions(t, func(o *Options) {
		o.OpenPreview = func(threadID, target string) error {
			calls++
			return nil
		}
	})
	registerThread(t, st, "thread-1", "proj-test-1")

	if err := os.WriteFile(filepath.Join(projDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(projDir, "dir.html"), 0o755); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(t.TempDir(), "outside.html")
	if err := os.WriteFile(outsideFile, []byte("<h1>outside</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		threadID string
		workDir  string
		target   string
	}{
		{name: "parent traversal", threadID: "thread-1", workDir: projDir, target: "../x.html"},
		{name: "outside absolute path", threadID: "thread-1", workDir: projDir, target: outsideFile},
		{name: "non-html extension", threadID: "thread-1", workDir: projDir, target: "main.go"},
		{name: "missing file", threadID: "thread-1", workDir: projDir, target: "missing.html"},
		{name: "directory instead of file", threadID: "thread-1", workDir: projDir, target: "dir.html"},
		{name: "empty workdir non-html file", threadID: "thread-1", workDir: "", target: "main.go"},
		{name: "external https url", threadID: "thread-1", workDir: projDir, target: "https://example.com"},
		{name: "spoofed localhost suffix", threadID: "thread-1", workDir: projDir, target: "http://localhost.evil.com/"},
		{name: "missing thread scope", threadID: "", workDir: projDir, target: "http://localhost:5173/"},
		{name: "empty target", threadID: "thread-1", workDir: projDir, target: "   "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, isError := callPreviewOpen(t, srv, tc.threadID, tc.workDir, map[string]any{"target": tc.target})
			if !isError {
				t.Fatalf("%s 预期报错，实际成功: %q", tc.name, text)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("非法目标不应触发回调，实际触发 %d 次", calls)
	}
}

func TestPreviewOpenLocalURL(t *testing.T) {
	t.Parallel()
	var gotTargets []string
	srv, st, projDir := setupTestMCPWithOptions(t, func(o *Options) {
		o.OpenPreview = func(threadID, target string) error {
			gotTargets = append(gotTargets, target)
			return nil
		}
	})
	registerThread(t, st, "thread-1", "proj-test-1")

	urls := []string{
		"http://localhost:5173/",
		"http://127.0.0.1:3000/app",
		"http://[::1]:5173/",
	}
	for _, raw := range urls {
		text, isError := callPreviewOpen(t, srv, "thread-1", projDir, map[string]any{"target": raw})
		if isError || text != "已在右侧预览打开" {
			t.Fatalf("本机 URL %q 打开失败: isError=%v text=%q", raw, isError, text)
		}
	}
	if len(gotTargets) != len(urls) {
		t.Fatalf("回调次数不符: got=%d want=%d", len(gotTargets), len(urls))
	}
	for i, want := range urls {
		if gotTargets[i] != want {
			t.Fatalf("回调第 %d 个 URL=%q, want %q", i, gotTargets[i], want)
		}
	}
}

func TestPreviewOpenUnavailableAndCallbackError(t *testing.T) {
	t.Parallel()
	srvNil, stNil, projDirNil := setupTestMCP(t)
	registerThread(t, stNil, "thread-1", "proj-test-1")
	text, isError := callPreviewOpen(t, srvNil, "thread-1", projDirNil, map[string]any{"target": "http://localhost:5173/"})
	if !isError || !strings.Contains(text, "桌面预览不可用") {
		t.Fatalf("OpenPreview 为 nil 时应返回桌面预览不可用, 实际 isError=%v text=%q", isError, text)
	}

	srvErr, stErr, projDirErr := setupTestMCPWithOptions(t, func(o *Options) {
		o.OpenPreview = func(threadID, target string) error {
			return errors.New("桌面窗口未打开")
		}
	})
	registerThread(t, stErr, "thread-1", "proj-test-1")
	text, isError = callPreviewOpen(t, srvErr, "thread-1", projDirErr, map[string]any{"target": "http://localhost:5173/"})
	if !isError || !strings.Contains(text, "桌面窗口未打开") {
		t.Fatalf("回调报错应原样返回, 实际 isError=%v text=%q", isError, text)
	}
}

func TestPreviewOpenListedInOrdinaryThread(t *testing.T) {
	t.Parallel()
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "thread-ordinary", "proj-test-1")

	names := threadToolNames(t, srv, "thread-ordinary")
	if !names[previewOpenToolName] {
		t.Fatalf("普通会话 tools/list 应包含 %s, 实际为 %v", previewOpenToolName, names)
	}
}

func TestPreviewOpenWithoutWorkDir(t *testing.T) {
	t.Parallel()
	var gotThread, gotTarget string
	calls := 0
	srv, st, _ := setupTestMCPWithOptions(t, func(o *Options) {
		o.OpenPreview = func(threadID, target string) error {
			calls++
			gotThread = threadID
			gotTarget = target
			return nil
		}
	})
	registerThread(t, st, "thread-1", "proj-test-1")

	text, isError := callPreviewOpen(t, srv, "thread-1", "", map[string]any{"target": "docs/a.html"})
	if isError || text != "已在右侧预览打开" {
		t.Fatalf("无 WorkDir 打开 HTML 失败: isError=%v text=%q", isError, text)
	}
	if calls != 1 || gotThread != "thread-1" || gotTarget != "docs/a.html" {
		t.Fatalf("回调参数异常: calls=%d thread=%q target=%q", calls, gotThread, gotTarget)
	}

	text, isError = callPreviewOpen(t, srv, "thread-1", "", map[string]any{"target": "a.go"})
	if !isError {
		t.Fatalf("无 WorkDir 打开非 HTML 文件应报错, 实际成功: %q", text)
	}
	if calls != 1 {
		t.Fatalf("非 HTML 文件不应触发回调, calls=%d", calls)
	}
}
