package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunner_DiffPathspecFilter 验证 Diff/DiffText/NumStat 的路径过滤：
// 只返回指定路径，包含空格的路径按字面量匹配，空 paths 保持全量行为。
func TestRunner_DiffPathspecFilter(t *testing.T) {
	t.Parallel()
	runner, dir := initGitRepo(t)
	ctx := context.Background()

	files := map[string]string{
		"keep.txt":       "keep v1\n",
		"with space.txt": "space v1\n",
		"skip.txt":       "skip v1\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	startRef := CheckpointRef("thread-pathspec", "turn-1") + "/start"
	if _, err := runner.Checkpoint(ctx, dir, startRef, "start"); err != nil {
		t.Fatalf("checkpoint start: %v", err)
	}

	for name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("changed "+name+"\n"), 0644); err != nil {
			t.Fatalf("update %s: %v", name, err)
		}
	}

	paths := []string{"keep.txt", "with space.txt"}

	text, _, err := runner.DiffText(ctx, dir, startRef, "", paths, 1024*1024)
	if err != nil {
		t.Fatalf("DiffText with paths: %v", err)
	}
	if !strings.Contains(text, "keep.txt") || !strings.Contains(text, "with space.txt") {
		t.Fatalf("expected selected paths in diff text, got:\n%s", text)
	}
	if strings.Contains(text, "skip.txt") {
		t.Fatalf("unexpected unselected path in diff text, got:\n%s", text)
	}

	diffs, err := runner.Diff(ctx, dir, startRef, "", paths)
	if err != nil {
		t.Fatalf("Diff with paths: %v", err)
	}
	if len(diffs) != 2 {
		t.Fatalf("expected 2 filtered file diffs, got %d", len(diffs))
	}

	stats, err := runner.NumStat(ctx, dir, startRef, "", paths)
	if err != nil {
		t.Fatalf("NumStat with paths: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("expected 2 filtered stats, got %d: %+v", len(stats), stats)
	}
	got := map[string]bool{}
	for _, st := range stats {
		got[st.Path] = true
	}
	if !got["keep.txt"] || !got["with space.txt"] || got["skip.txt"] {
		t.Fatalf("unexpected filtered stats: %+v", stats)
	}

	// 空 paths 保持原行为：返回全部变更。
	allText, _, err := runner.DiffText(ctx, dir, startRef, "", nil, 1024*1024)
	if err != nil {
		t.Fatalf("DiffText without paths: %v", err)
	}
	if !strings.Contains(allText, "skip.txt") {
		t.Fatalf("expected unfiltered diff to contain skip.txt, got:\n%s", allText)
	}
}

// TestRunner_DiffPathspecChunking 验证路径数超过 maxPathspecChunk 时分块执行并拼接结果。
func TestRunner_DiffPathspecChunking(t *testing.T) {
	t.Parallel()
	runner, dir := initGitRepo(t)
	ctx := context.Background()

	total := maxPathspecChunk + 1
	paths := make([]string, 0, total)
	for i := 0; i < total; i++ {
		name := fmt.Sprintf("chunk-%03d.txt", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("v1\n"), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		paths = append(paths, name)
	}

	startRef := CheckpointRef("thread-chunk", "turn-1") + "/start"
	if _, err := runner.Checkpoint(ctx, dir, startRef, "start"); err != nil {
		t.Fatalf("checkpoint start: %v", err)
	}
	for _, name := range paths {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("v2\n"), 0644); err != nil {
			t.Fatalf("update %s: %v", name, err)
		}
	}

	diffs, err := runner.Diff(ctx, dir, startRef, "", paths)
	if err != nil {
		t.Fatalf("Diff chunked: %v", err)
	}
	if len(diffs) != total {
		t.Fatalf("expected %d chunked file diffs, got %d", total, len(diffs))
	}

	stats, err := runner.NumStat(ctx, dir, startRef, "", paths)
	if err != nil {
		t.Fatalf("NumStat chunked: %v", err)
	}
	if len(stats) != total {
		t.Fatalf("expected %d chunked stats, got %d", total, len(stats))
	}

	text, _, err := runner.DiffText(ctx, dir, startRef, "", paths, 1024*1024)
	if err != nil {
		t.Fatalf("DiffText chunked: %v", err)
	}
	if got := strings.Count(text, "diff --git "); got != total {
		t.Fatalf("expected %d file sections in chunked diff text, got %d", total, got)
	}
}
