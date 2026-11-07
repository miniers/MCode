package reviewfunnel

import (
	"strings"
	"testing"
)

func TestBuildReworkPreamble(t *testing.T) {
	preamble := BuildReworkPreamble(ReworkInput{
		PreviousReason: "上次驳回：internal/reviewfunnel/l1.go:12 空指针",
		Delta:          "DELTA_MUST_NOT_APPEAR_IN_PREAMBLE",
		OldPatchPath:   "/tmp/review/old.patch",
	})

	for _, want := range []string{
		"internal/reviewfunnel/l1.go:12 空指针",
		"resolved",
		"new problem",
		"unchanged",
		"Ignore every part",
		"/tmp/review/old.patch",
		"delta",
	} {
		if !strings.Contains(preamble, want) {
			t.Fatalf("返工提示词缺少 %q", want)
		}
	}
	if strings.Contains(preamble, "DELTA_MUST_NOT_APPEAR_IN_PREAMBLE") {
		t.Fatalf("BuildReworkPreamble 不应自行拼接 delta")
	}
	if strings.Contains(strings.ToLower(preamble), "full review") || strings.Contains(preamble, "完整审查") {
		t.Fatalf("返工提示词不应出现完整审查措辞")
	}
}

func TestBuildReworkPreambleOldUnavailable(t *testing.T) {
	preamble := BuildReworkPreamble(ReworkInput{
		PreviousReason: "上次驳回原因",
		OldPatchPath:   "/tmp/review/old.patch",
		OldUnavailable: true,
	})
	if !strings.Contains(preamble, "unavailable") {
		t.Fatalf("旧版本不可用时应明确说明: %q", preamble)
	}
	if !strings.Contains(preamble, "previous rejection reason only") {
		t.Fatalf("旧版本不可用时应要求只对照上次驳回原因: %q", preamble)
	}
	if strings.Contains(preamble, "/tmp/review/old.patch") {
		t.Fatalf("旧版本不可用时不应再引用旧补丁路径: %q", preamble)
	}
}

func TestBuildReworkPreambleEmptyReason(t *testing.T) {
	preamble := BuildReworkPreamble(ReworkInput{PreviousReason: "   "})
	if !strings.Contains(preamble, "no previous rejection reason") {
		t.Fatalf("空原因应有兜底说明: %q", preamble)
	}
	if !strings.Contains(preamble, "escalate") {
		t.Fatalf("无法判断时应要求 escalate: %q", preamble)
	}
}
