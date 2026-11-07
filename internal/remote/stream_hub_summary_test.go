package remote

import (
	"testing"

	"mcode/internal/remote/wire"
)

// TestThreadSummaryEqual 校验摘要等价判断：MCPDisabled 不同即不相等，完全相同则相等。
func TestThreadSummaryEqual(t *testing.T) {
	base := wire.ThreadSummary{
		ID:          "t1",
		ProjectID:   "p1",
		Title:       "会话",
		IsRunning:   true,
		MCPDisabled: []string{"claude:project:filesystem"},
	}

	same := base
	same.MCPDisabled = []string{"claude:project:filesystem"}
	if !threadSummaryEqual(base, same) {
		t.Fatalf("两份相同的摘要应判为相等: a=%+v b=%+v", base, same)
	}
	identical := base
	if !threadSummaryEqual(base, identical) {
		t.Fatalf("同一份摘要的副本应判为相等: a=%+v b=%+v", base, identical)
	}

	other := base
	other.MCPDisabled = []string{"codex:global:github"}
	if threadSummaryEqual(base, other) {
		t.Fatalf("MCPDisabled 不同应判为不相等: a=%+v b=%+v", base, other)
	}

	// 名单变长也必须判为不相等，避免退化成只比较首元素。
	longer := base
	longer.MCPDisabled = []string{"claude:project:filesystem", "codex:global:github"}
	if threadSummaryEqual(base, longer) {
		t.Fatalf("MCPDisabled 长度不同应判为不相等: a=%+v b=%+v", base, longer)
	}

	// 去掉 MCPDisabled 之外的字段差异同样判为不相等。
	renamed := base
	renamed.Title = "另一个会话"
	if threadSummaryEqual(base, renamed) {
		t.Fatalf("标题不同应判为不相等: a=%+v b=%+v", base, renamed)
	}
}
