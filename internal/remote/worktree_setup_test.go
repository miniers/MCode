package remote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunWorktreeSetupScripts 验证按顺序执行、注入环境变量、失败不中断后续脚本，
// 以及未勾选「新建工作树时运行」的脚本被跳过。
func TestRunWorktreeSetupScripts(t *testing.T) {
	root := t.TempDir()
	wtPath := filepath.Join(root, "worktree")
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatalf("创建临时 worktree 失败: %v", err)
	}

	scripts := []ProjectScript{
		{ID: "1", Name: "记录环境变量", Command: `printf '%s' "$MCODE_WORKTREE_PATH" > wt_env.txt`, RunOnWorktreeCreate: true},
		{ID: "2", Name: "必然失败", Command: "exit 3", RunOnWorktreeCreate: true},
		{ID: "3", Name: "失败之后仍执行", Command: "echo done > after_failure.txt", RunOnWorktreeCreate: true},
		{ID: "4", Name: "不该执行", Command: "echo nope > skipped.txt", RunOnWorktreeCreate: false},
	}

	results := runWorktreeSetupScripts(context.Background(), root, wtPath, scripts)

	if len(results) != 3 {
		t.Fatalf("应只执行 3 个勾选脚本，实际 %d 个: %+v", len(results), results)
	}
	if !results[0].Success || results[1].Success || !results[2].Success {
		t.Fatalf("成功标记不符合预期: %+v", results)
	}

	envData, err := os.ReadFile(filepath.Join(wtPath, "wt_env.txt"))
	if err != nil {
		t.Fatalf("脚本未在 worktree 中执行: %v", err)
	}
	if string(envData) != wtPath {
		t.Fatalf("MCODE_WORKTREE_PATH 期望 %q，实际 %q", wtPath, string(envData))
	}

	if _, err := os.Stat(filepath.Join(wtPath, "after_failure.txt")); err != nil {
		t.Fatalf("失败脚本之后的脚本未执行: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "skipped.txt")); err == nil {
		t.Fatal("未勾选「新建工作树时运行」的脚本不应执行")
	}
}

// TestRunWorktreeSetupScriptsTruncatesOutput 验证输出被截断到 2KB 以内。
func TestRunWorktreeSetupScriptsTruncatesOutput(t *testing.T) {
	root := t.TempDir()
	wtPath := filepath.Join(root, "worktree")
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatalf("创建临时 worktree 失败: %v", err)
	}

	scripts := []ProjectScript{
		{ID: "1", Name: "大量输出", Command: `head -c 8192 /dev/zero | tr '\0' 'a'`, RunOnWorktreeCreate: true},
	}

	results := runWorktreeSetupScripts(context.Background(), root, wtPath, scripts)
	if len(results) != 1 {
		t.Fatalf("应执行 1 个脚本，实际 %d 个", len(results))
	}
	if !results[0].Success {
		t.Fatalf("脚本应执行成功: %+v", results[0])
	}
	if !strings.Contains(results[0].Output, "输出已截断") {
		t.Fatalf("超长输出应带截断提示，实际长度 %d", len(results[0].Output))
	}
	if len(results[0].Output) > worktreeSetupOutputLimit+64 {
		t.Fatalf("输出未被截断，长度 %d", len(results[0].Output))
	}
}
