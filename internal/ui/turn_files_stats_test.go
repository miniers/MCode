package mcodeui

import (
	"path/filepath"
	"testing"

	"mcode/internal/git"
)

// turnFilesStatsEdited 构造一个已编辑文件：工具里的相对路径 + 解析后的本机绝对路径。
func turnFilesStatsEdited(dir, rel string) turnEditedFile {
	return turnEditedFile{path: rel, abs: filepath.Join(dir, filepath.FromSlash(rel)), name: filepath.Base(rel)}
}

// TestTurnFileDiffStatsDeferred 验证延迟解析的大文件补丁用 Additions / Deletions，
// 同时绝对路径与仓库相对路径按后缀匹配。
func TestTurnFileDiffStatsDeferred(t *testing.T) {
	dir := t.TempDir()
	edited := turnFilesStatsEdited(dir, "internal/ui/a.go")
	files := []git.FileDiff{{
		NewPath:    "internal/ui/a.go",
		IsDeferred: true,
		Additions:  12,
		Deletions:  3,
	}}
	stats, ok := turnFileDiffStats(files, []turnEditedFile{edited})
	if !ok {
		t.Fatalf("延迟解析文件应全部命中，实际 stats=%+v", stats)
	}
	if got := stats[edited.abs]; got != (turnFileStat{adds: 12, dels: 3}) {
		t.Fatalf("stats[%q] = %+v, want {12 3}", edited.abs, got)
	}
}

// TestTurnFileDiffStatsHunks 验证普通文件从 hunk 行统计增删。
func TestTurnFileDiffStatsHunks(t *testing.T) {
	dir := t.TempDir()
	edited := turnFilesStatsEdited(dir, "b.go")
	files := []git.FileDiff{{
		NewPath: "b.go",
		Hunks: []git.Hunk{{
			Lines: []git.Line{
				{Kind: ' '},
				{Kind: '+'},
				{Kind: '+'},
				{Kind: '-'},
			},
		}},
	}}
	stats, ok := turnFileDiffStats(files, []turnEditedFile{edited})
	if !ok {
		t.Fatalf("hunk 文件应全部命中，实际 stats=%+v", stats)
	}
	if got := stats[edited.abs]; got != (turnFileStat{adds: 2, dels: 1}) {
		t.Fatalf("stats[%q] = %+v, want {2 1}", edited.abs, got)
	}
}

// TestTurnFileDiffStatsBinary 验证二进制文件记为未知：不写统计，整体 ok 为 false。
func TestTurnFileDiffStatsBinary(t *testing.T) {
	dir := t.TempDir()
	edited := turnFilesStatsEdited(dir, "logo.png")
	files := []git.FileDiff{{NewPath: "logo.png", Binary: true}}
	stats, ok := turnFileDiffStats(files, []turnEditedFile{edited})
	if ok {
		t.Fatalf("二进制文件不应算全部命中")
	}
	if _, exists := stats[edited.abs]; exists {
		t.Fatalf("二进制文件不应写入统计，实际 %+v", stats)
	}
}

// TestTurnFileDiffStatsEmptyFiles 验证 diff 为空时直接返回 false，不做统计。
func TestTurnFileDiffStatsEmptyFiles(t *testing.T) {
	dir := t.TempDir()
	edited := turnFilesStatsEdited(dir, "a.go")
	stats, ok := turnFileDiffStats(nil, []turnEditedFile{edited})
	if ok || len(stats) != 0 {
		t.Fatalf("diff 为空应返回 (空, false)，实际 (%+v, %v)", stats, ok)
	}
}

// TestTurnFileDiffStatsPartial 验证部分文件匹配不到时：匹配到的文件有统计，但整体 ok 为 false。
func TestTurnFileDiffStatsPartial(t *testing.T) {
	dir := t.TempDir()
	matched := turnFilesStatsEdited(dir, "a.go")
	missing := turnFilesStatsEdited(dir, "missing.go")
	files := []git.FileDiff{{
		NewPath: "a.go",
		Hunks:   []git.Hunk{{Lines: []git.Line{{Kind: '+'}, {Kind: '-'}}}},
	}}
	stats, ok := turnFileDiffStats(files, []turnEditedFile{matched, missing})
	if ok {
		t.Fatalf("有文件匹配不到时整体不应算全部命中")
	}
	if got := stats[matched.abs]; got != (turnFileStat{adds: 1, dels: 1}) {
		t.Fatalf("stats[%q] = %+v, want {1 1}", matched.abs, got)
	}
	if _, exists := stats[missing.abs]; exists {
		t.Fatalf("匹配不到的文件不应写入统计，实际 %+v", stats)
	}
}

// TestTurnFileDiffStatsZeroZero 验证 0 增 0 删的文件视为未知，不写统计。
func TestTurnFileDiffStatsZeroZero(t *testing.T) {
	dir := t.TempDir()
	edited := turnFilesStatsEdited(dir, "empty.go")
	files := []git.FileDiff{{
		NewPath: "empty.go",
		Hunks:   []git.Hunk{{Lines: []git.Line{{Kind: ' '}}}},
	}}
	stats, ok := turnFileDiffStats(files, []turnEditedFile{edited})
	if ok {
		t.Fatalf("0/0 不应算全部命中")
	}
	if _, exists := stats[edited.abs]; exists {
		t.Fatalf("0/0 不应写入统计，实际 %+v", stats)
	}
}

// TestSumTurnFileStats 验证总计按文件求和。
func TestSumTurnFileStats(t *testing.T) {
	stats := map[string]turnFileStat{
		"/repo/a.go": {adds: 12, dels: 3},
		"/repo/b.go": {adds: 5, dels: 1},
	}
	adds, dels := sumTurnFileStats(stats)
	if adds != 17 || dels != 4 {
		t.Fatalf("sumTurnFileStats = (%d, %d), want (17, 4)", adds, dels)
	}
	if adds, dels := sumTurnFileStats(nil); adds != 0 || dels != 0 {
		t.Fatalf("空统计应为 (0, 0)，实际 (%d, %d)", adds, dels)
	}
}
