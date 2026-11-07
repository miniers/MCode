package agy

import (
	"slices"
	"testing"

	"mcode/internal/harness"
)

// TestBuildArgsAdditionalDirs 覆盖附加目录映射：工作目录之外的目录逐个生成 --add-dir，
// 空串与工作目录自身跳过；附加目录为空时参数与原来一致。
func TestBuildArgsAdditionalDirs(t *testing.T) {
	base := []string{"--input-format", "stream-json", "--output-format", "stream-json", "--print-timeout", "30m"}

	got := buildArgs("/work/main", "", "", "", harness.ModeSupervised, nil, []string{"/proj/a", "", "/work/main", "/proj/b"})
	want := append(slices.Clone(base), "--add-dir", "/work/main", "--add-dir", "/proj/a", "--add-dir", "/proj/b")
	want = append(want, modeArguments(harness.ModeSupervised)...)
	if !slices.Equal(got, want) {
		t.Fatalf("参数不符:\n得到 %v\n期望 %v", got, want)
	}

	got = buildArgs("/work/main", "", "", "", harness.ModeSupervised, nil, nil)
	want = append(slices.Clone(base), "--add-dir", "/work/main")
	want = append(want, modeArguments(harness.ModeSupervised)...)
	if !slices.Equal(got, want) {
		t.Fatalf("空附加目录参数不符:\n得到 %v\n期望 %v", got, want)
	}
}
