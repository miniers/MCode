package swarm

import (
	"context"
	"strings"
	"testing"
)

// TestTextgenDescriber_MemoryProvider 用 memory provider（textgen 的模拟回显，
// 不调用外部程序）验证返回非空且无错误。
func TestTextgenDescriber_MemoryProvider(t *testing.T) {
	describe := TextgenDescriber("memory", "")
	msg, err := describe(context.Background(), "diff --git a/a.go b/a.go")
	if err != nil {
		t.Fatalf("TextgenDescriber(memory) error: %v", err)
	}
	if strings.TrimSpace(msg) == "" {
		t.Fatal("TextgenDescriber(memory) 返回空提交信息")
	}
}

// TestTextgenDescriber_EmptyProvider provider 为空时返回错误。
func TestTextgenDescriber_EmptyProvider(t *testing.T) {
	describe := TextgenDescriber("", "")
	if _, err := describe(context.Background(), "diff"); err == nil {
		t.Fatal("provider 为空时应返回错误，实际为 nil")
	}
}
