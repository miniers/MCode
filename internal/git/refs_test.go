package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunner_ListRefs(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 可执行文件在 PATH 中未找到，跳过测试")
	}

	runner, dir := initGitRepo(t)
	ctx := context.Background()

	// 创建初始文件并打检查点
	fileA := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(fileA, []byte("hello a\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	refStart := CheckpointRef("thread-test", "turn-1") + "/start"
	refEnd := CheckpointRef("thread-test", "turn-1") + "/end"
	refTurn2 := CheckpointRef("thread-test", "turn-2") + "/start"

	if _, err := runner.Checkpoint(ctx, dir, refStart, "turn 1 start"); err != nil {
		t.Fatalf("checkpoint refStart: %v", err)
	}

	if err := os.WriteFile(fileA, []byte("hello a updated\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if _, err := runner.Checkpoint(ctx, dir, refEnd, "turn 1 end"); err != nil {
		t.Fatalf("checkpoint refEnd: %v", err)
	}

	if _, err := runner.Checkpoint(ctx, dir, refTurn2, "turn 2 start"); err != nil {
		t.Fatalf("checkpoint refTurn2: %v", err)
	}

	// 查特定前缀
	refs, err := runner.ListRefs(ctx, dir, "refs/mcode/checkpoints/thread-test/")
	if err != nil {
		t.Fatalf("ListRefs failed: %v", err)
	}

	if len(refs) != 3 {
		t.Fatalf("expected 3 refs, got %d", len(refs))
	}

	// 验证按 Name 排序
	for i := 1; i < len(refs); i++ {
		if refs[i-1].Name >= refs[i].Name {
			t.Fatalf("expected sorted refs, got %s >= %s", refs[i-1].Name, refs[i].Name)
		}
	}

	if refs[0].Name != refEnd || refs[1].Name != refStart || refs[2].Name != refTurn2 {
		t.Fatalf("unexpected refs order: %+v", refs)
	}
}

func TestRunner_DiffText_Truncation(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 可执行文件在 PATH 中未找到，跳过测试")
	}

	runner, dir := initGitRepo(t)
	ctx := context.Background()

	docFile := filepath.Join(dir, "large.txt")
	var initial strings.Builder
	for i := 0; i < 50; i++ {
		initial.WriteString("line from initial state\n")
	}
	if err := os.WriteFile(docFile, []byte(initial.String()), 0644); err != nil {
		t.Fatalf("write doc: %v", err)
	}

	refStart := CheckpointRef("thread-diff", "turn-1") + "/start"
	if _, err := runner.Checkpoint(ctx, dir, refStart, "start"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	// 在工作区写入多行追加内容
	var updated strings.Builder
	updated.WriteString(initial.String())
	for i := 0; i < 50; i++ {
		updated.WriteString("newly added line in working tree\n")
	}
	if err := os.WriteFile(docFile, []byte(updated.String()), 0644); err != nil {
		t.Fatalf("write doc updated: %v", err)
	}

	// 不截断的 diff
	fullDiff, truncated, err := runner.DiffText(ctx, dir, refStart, "", nil, 1024*1024)
	if err != nil {
		t.Fatalf("DiffText full: %v", err)
	}
	if truncated {
		t.Fatalf("expected truncated=false for large maxBytes")
	}
	if !strings.Contains(fullDiff, "newly added line in working tree") {
		t.Fatalf("expected diff to contain added lines")
	}

	// 测试截断
	maxBytes := 200
	truncDiff, truncated, err := runner.DiffText(ctx, dir, refStart, "", nil, maxBytes)
	if err != nil {
		t.Fatalf("DiffText truncated: %v", err)
	}
	if !truncated {
		t.Fatalf("expected truncated=true")
	}
	if len(truncDiff) > maxBytes {
		t.Fatalf("expected diff length <= %d, got %d", maxBytes, len(truncDiff))
	}
	if !strings.HasSuffix(truncDiff, "\n") {
		t.Fatalf("expected truncated diff to end with newline (clean line cut)")
	}
}
