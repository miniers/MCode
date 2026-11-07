package commandcode

import (
	"slices"
	"testing"

	"mcode/internal/harness"
)

// TestBuildArgsAdditionalDirs 覆盖附加目录映射：工作目录之外的目录逐个生成 --add-dir，
// 空串与工作目录自身跳过；附加目录为空时参数与原来一致。
func TestBuildArgsAdditionalDirs(t *testing.T) {
	withBase := func(rest ...string) []string {
		base := []string{
			"-p", "--output-format", "json", "--skip-onboarding", "--no-auto-update",
			"--trust", "--tools-enable", "todo_write", "--max-turns", "400",
			"--yolo",
		}
		return append(base, rest...)
	}

	turn := harness.Turn{
		Mode:           harness.ModeSupervised,
		WorkDir:        "/work/main",
		AdditionalDirs: []string{"/proj/a", "", "/work/main", "/proj/b"},
	}
	if got, want := buildArgs(Options{}, turn, "", "", ""), withBase("--add-dir", "/proj/a", "--add-dir", "/proj/b"); !slices.Equal(got, want) {
		t.Fatalf("参数不符:\n得到 %v\n期望 %v", got, want)
	}

	turn.AdditionalDirs = nil
	if got, want := buildArgs(Options{}, turn, "", "", ""), withBase(); !slices.Equal(got, want) {
		t.Fatalf("空附加目录参数不符:\n得到 %v\n期望 %v", got, want)
	}
}
