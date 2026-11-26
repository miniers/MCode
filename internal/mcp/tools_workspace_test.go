package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"mcode/internal/store"
)

func callWorkspaceHandler(t *testing.T, srv *Server, name string, tc ToolContext, args map[string]any) ToolResult {
	t.Helper()
	tool, ok := srv.registry.Get(name)
	if !ok {
		t.Fatalf("未注册工具 %s", name)
	}
	if invalid := validateArguments(tool, args); invalid != nil {
		return *invalid
	}
	return tool.Handler(tc, args)
}

func TestWorkspaceSearchLiteralAndRegex(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "main.go"), "package main\nfunc helloWorld() {}\nfunc other_123() {}\n")
	writeTestFile(t, filepath.Join(dir, "sub", "note.txt"), "helloWorld literal match\nfoo_456 bar\n")

	srv, _, _ := setupTestMCP(t)
	res := callWorkspaceHandler(t, srv, "workspace_search", ToolContext{WorkDir: dir}, map[string]any{
		"queries": []any{
			map[string]any{"pattern": "helloWorld"},
			map[string]any{"pattern": `_[0-9]+`, "regex": true},
		},
	})
	if res.IsError {
		t.Fatalf("workspace_search 失败: %s", res.Text)
	}
	for _, want := range []string{
		"## helloWorld (2 matches)",
		"main.go:2: func helloWorld() {}",
		"sub/note.txt:1: helloWorld literal match",
		"## _[0-9]+ (2 matches)",
		"main.go:3: func other_123() {}",
		"sub/note.txt:2: foo_456 bar",
	} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("输出缺少 %q:\n%s", want, res.Text)
		}
	}
}

func TestWorkspaceSearchWalkDirFallback(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "a.go"), "package a\n// needle_token_1\n")
	writeTestFile(t, filepath.Join(dir, "sub", "b.txt"), "prefix needle_token_2 suffix\n")
	// WalkDir 回退必须跳过 .git、node_modules、>1MB 文件和含 NUL 的二进制文件。
	writeTestFile(t, filepath.Join(dir, ".git", "ignored.txt"), "needle_token_git\n")
	writeTestFile(t, filepath.Join(dir, "node_modules", "pkg.js"), "needle_token_nm\n")
	big := append(bytes.Repeat([]byte("x"), 1<<20+10), []byte("\nneedle_token_big\n")...)
	writeTestFileBytes(t, filepath.Join(dir, "big.txt"), big)
	bin := append([]byte("needle_token_bin"), 0)
	writeTestFileBytes(t, filepath.Join(dir, "bin.dat"), bin)

	srv, _, _ := setupTestMCP(t)
	rgRes := callWorkspaceHandler(t, srv, "workspace_search", ToolContext{WorkDir: dir}, map[string]any{
		"queries": []any{
			map[string]any{"pattern": "needle_token_1"},
			map[string]any{"pattern": `needle_token_[12]`, "regex": true},
		},
	})
	if rgRes.IsError {
		t.Fatalf("rg 路径失败: %s", rgRes.Text)
	}

	origLookup := lookupRGPath
	lookupRGPath = func() string { return "" }
	t.Cleanup(func() { lookupRGPath = origLookup })

	walkRes := callWorkspaceHandler(t, srv, "workspace_search", ToolContext{WorkDir: dir}, map[string]any{
		"queries": []any{
			map[string]any{"pattern": "needle_token_1"},
			map[string]any{"pattern": `needle_token_[12]`, "regex": true},
		},
	})
	if walkRes.IsError {
		t.Fatalf("WalkDir 回退失败: %s", walkRes.Text)
	}
	if walkRes.Text != rgRes.Text {
		t.Fatalf("WalkDir 结果与 rg 不一致:\n--- rg ---\n%s\n--- walk ---\n%s", rgRes.Text, walkRes.Text)
	}

	skipRes := callWorkspaceHandler(t, srv, "workspace_search", ToolContext{WorkDir: dir}, map[string]any{
		"queries": []any{
			map[string]any{"pattern": "needle_token_"},
		},
	})
	if skipRes.IsError {
		t.Fatalf("WalkDir 过滤校验失败: %s", skipRes.Text)
	}
	for _, forbidden := range []string{"needle_token_git", "needle_token_nm", "needle_token_big", "needle_token_bin"} {
		if strings.Contains(skipRes.Text, forbidden) {
			t.Fatalf("WalkDir 不应命中 %s:\n%s", forbidden, skipRes.Text)
		}
	}
}

func TestWorkspacePathRejectsTraversalAndAbsolute(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	absFile := filepath.Join(dir, "a.go")
	writeTestFile(t, absFile, "package a\n")
	srv, _, _ := setupTestMCP(t)

	// 空 WorkDir 与空 Project.Path 必须报 CodeInvalidArguments。
	emptyRes := callWorkspaceHandler(t, srv, "workspace_outline", ToolContext{}, map[string]any{
		"paths": []any{"a.go"},
	})
	if !emptyRes.IsError || toolErrorCode(emptyRes) != CodeInvalidArguments {
		t.Fatalf("缺少根目录应返回 %s, 实际: %+v", CodeInvalidArguments, emptyRes)
	}

	// WorkDir 为空时应回退到 Project.Path。
	fallbackRes := callWorkspaceHandler(t, srv, "workspace_outline", ToolContext{
		Project: store.Project{Path: dir},
	}, map[string]any{
		"paths": []any{"a.go"},
	})
	if fallbackRes.IsError {
		t.Fatalf("回退 Project.Path 失败: %s", fallbackRes.Text)
	}

	for _, bad := range []string{"../x", "sub/../../x", absFile} {
		searchRes := callWorkspaceHandler(t, srv, "workspace_search", ToolContext{WorkDir: dir}, map[string]any{
			"queries": []any{map[string]any{"pattern": "package", "path": bad}},
		})
		if !searchRes.IsError || toolErrorCode(searchRes) != CodeInvalidArguments {
			t.Fatalf("workspace_search 应拒绝路径 %q 并返回 %s, 实际: %+v", bad, CodeInvalidArguments, searchRes)
		}

		outlineRes := callWorkspaceHandler(t, srv, "workspace_outline", ToolContext{WorkDir: dir}, map[string]any{
			"paths": []any{bad},
		})
		if !outlineRes.IsError || toolErrorCode(outlineRes) != CodeInvalidArguments {
			t.Fatalf("workspace_outline 应拒绝路径 %q 并返回 %s, 实际: %+v", bad, CodeInvalidArguments, outlineRes)
		}
	}
}

func TestWorkspacePathRejectsExternalSymlink(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上跳过符号链接测试")
	}
	dir := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.go")
	writeTestFile(t, outsideFile, "package secret\nfunc Leak() {}\n")
	if err := os.Symlink(outside, filepath.Join(dir, "extdir")); err != nil {
		t.Fatalf("创建目录软链失败: %v", err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(dir, "ext.go")); err != nil {
		t.Fatalf("创建文件软链失败: %v", err)
	}

	srv, _, _ := setupTestMCP(t)
	searchRes := callWorkspaceHandler(t, srv, "workspace_search", ToolContext{WorkDir: dir}, map[string]any{
		"queries": []any{map[string]any{"pattern": "Leak", "path": "extdir"}},
	})
	if !searchRes.IsError || toolErrorCode(searchRes) != CodeInvalidArguments {
		t.Fatalf("workspace_search 应拒绝指向工作区外的符号链接, 实际: %+v", searchRes)
	}

	outlineRes := callWorkspaceHandler(t, srv, "workspace_outline", ToolContext{WorkDir: dir}, map[string]any{
		"paths": []any{"ext.go"},
	})
	if !outlineRes.IsError || toolErrorCode(outlineRes) != CodeInvalidArguments {
		t.Fatalf("workspace_outline 应拒绝指向工作区外的符号链接, 实际: %+v", outlineRes)
	}
}

func TestWorkspaceOutlineGoAndMarkdown(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	goSrc := strings.Join([]string{
		"package demo",
		"",
		"const Version = 1",
		"var globalName = \"mcode\"",
		"",
		"type Server struct{}",
		"",
		"func (s *Server) Start() {}",
		"func Helper() {}",
	}, "\n") + "\n"
	writeTestFile(t, filepath.Join(dir, "srv.go"), goSrc)

	mdSrc := strings.Join([]string{
		"# Title",
		"intro text",
		"```sh",
		"# not a heading",
		"```",
		"## Section Two",
	}, "\n") + "\n"
	writeTestFile(t, filepath.Join(dir, "README.md"), mdSrc)
	writeTestFile(t, filepath.Join(dir, "app.ts"), "export async function bootApp() {\n  return 1\n}\n")

	srv, _, _ := setupTestMCP(t)
	res := callWorkspaceHandler(t, srv, "workspace_outline", ToolContext{WorkDir: dir}, map[string]any{
		"paths": []any{"srv.go", "README.md", "app.ts", "missing.go"},
	})
	if res.IsError {
		t.Fatalf("workspace_outline 失败: %s", res.Text)
	}
	for _, want := range []string{
		"## srv.go",
		"L3 const Version",
		"L4 var globalName",
		"L6 type Server",
		"L8 func (*Server) Start",
		"L9 func Helper",
		"## README.md",
		"L1 # Title",
		"L6 ## Section Two",
		"## app.ts",
		"L1 export async function bootApp() {",
		"## missing.go",
		"error:",
	} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("outline 缺少 %q:\n%s", want, res.Text)
		}
	}
	if strings.Contains(res.Text, "# not a heading") {
		t.Fatalf("不应把代码块中的 # 注释当作 Markdown 标题:\n%s", res.Text)
	}
}

func TestWorkspaceRunChecksNotRegistered(t *testing.T) {
	t.Parallel()
	srv, _, _ := setupTestMCP(t)
	if _, ok := srv.registry.Get("workspace_run_checks"); ok {
		t.Fatal("workspace_run_checks 不应再被注册")
	}
	for _, name := range []string{"workspace_search", "workspace_outline"} {
		if _, ok := srv.registry.Get(name); !ok {
			t.Fatalf("工具 %s 应保持注册", name)
		}
	}
}

func TestWorkspaceOutputTruncation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString("func 中文函数名称测试长输出占位符() {}\n")
	}
	writeTestFile(t, filepath.Join(dir, "long.go"), "package long\n"+sb.String())

	srv, _, _ := setupTestMCP(t)
	res := callWorkspaceHandler(t, srv, "workspace_search", ToolContext{WorkDir: dir}, map[string]any{
		"queries": []any{
			map[string]any{"pattern": "中文函数名称测试长输出占位符"},
		},
		"max_matches": float64(200),
	})
	if res.IsError {
		t.Fatalf("workspace_search 失败: %s", res.Text)
	}
	if len(res.Text) > workspaceInlineMaxBytes {
		t.Fatalf("输出字节数 %d 超过上限 %d", len(res.Text), workspaceInlineMaxBytes)
	}
	if !utf8.ValidString(res.Text) {
		t.Fatal("截断后输出不是合法 UTF-8")
	}
	if !strings.Contains(res.Text, "…(truncated ") || !strings.HasSuffix(res.Text, " bytes)") {
		t.Fatalf("截断后缺少 truncated 标记:\n%s", res.Text[max(0, len(res.Text)-100):])
	}
}

func TestWorkspaceReadOnlyTool(t *testing.T) {
	t.Parallel()
	tests := []struct {
		tool string
		want bool
	}{
		{tool: "mcp__mcode__workspace_search", want: true},
		{tool: "mcp_mcode_workspace_search", want: true},
		{tool: "mcode/workspace_search", want: true},
		{tool: "mcp__mcode__workspace_outline", want: true},
		{tool: "mcp_mcode_workspace_outline", want: true},
		{tool: "mcode/workspace_outline", want: true},
		{tool: "workspace_search", want: false},
	}
	for _, tc := range tests {
		if got := ReadOnlyTool(tc.tool, json.RawMessage(`{}`)); got != tc.want {
			t.Fatalf("ReadOnlyTool(%q) = %v, want %v", tc.tool, got, tc.want)
		}
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	writeTestFileBytes(t, path, []byte(content))
}

func writeTestFileBytes(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
