package app

import (
	"strings"
	"testing"
	"time"

	"mcode/internal/nmem"
)

// TestRenderMemoryExportEmpty 验证空列表也能生成带说明的 Markdown。
func TestRenderMemoryExportEmpty(t *testing.T) {
	exportedAt := time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC)
	got := renderMemoryExport("mcode", exportedAt, nil)

	for _, want := range []string{
		"# 记忆导出",
		"空间：mcode · 导出时间：2026-11-01 09:30 · 共 0 条",
		"（没有可导出的记忆）",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("导出结果应包含 %q，实际为 %q", want, got)
		}
	}
	if strings.Contains(got, "## ") {
		t.Fatalf("空列表不应有条目小节，实际为 %q", got)
	}
}

// TestRenderMemoryExportTwoRecords 验证两条记忆各自成节，元信息与正文按预期渲染。
func TestRenderMemoryExportTwoRecords(t *testing.T) {
	exportedAt := time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC)
	memories := []nmem.Memory{
		{
			ID:         "m1",
			Title:      "先用 worktree 验证再改主干",
			Content:    "风险较大的改动先在 worktree 里跑通测试。",
			UnitType:   "procedure",
			Importance: 0.9,
			UpdatedAt:  time.Date(2026, 10, 31, 7, 0, 0, 0, time.UTC),
		},
		{
			ID:         "m2",
			Title:      "记忆导出用 Markdown",
			Content:    "导出要能被人工阅读。",
			UnitType:   "decision",
			Importance: 0.5,
			CreatedAt:  time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC),
		},
	}

	got := renderMemoryExport("mcode", exportedAt, memories)
	for _, want := range []string{
		"共 2 条",
		"## 先用 worktree 验证再改主干",
		"ID：m1 · 类型：procedure · 重要度：0.9 · 更新时间：2026-10-31 07:00",
		"风险较大的改动先在 worktree 里跑通测试。",
		"## 记忆导出用 Markdown",
		// 只有 CreatedAt 时更新时间回落到创建时间。
		"ID：m2 · 类型：decision · 重要度：0.5 · 更新时间：2026-10-20 08:00",
		"导出要能被人工阅读。",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("导出结果应包含 %q，实际为 %q", want, got)
		}
	}
	if strings.Count(got, "## ") != 2 {
		t.Fatalf("应有两条记忆小节，实际为 %q", got)
	}
}

// TestRenderMemoryExportMissingFields 验证字段缺失时的兜底文案。
func TestRenderMemoryExportMissingFields(t *testing.T) {
	got := renderMemoryExport("", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), []nmem.Memory{
		{Title: "  ", Content: ""},
	})

	for _, want := range []string{
		"空间：（默认空间）",
		"## 未命名记忆",
		"（无正文）",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("导出结果应包含 %q，实际为 %q", want, got)
		}
	}
	for _, unwanted := range []string{"ID：", "类型：", "重要度：", "更新时间："} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("缺字段时不应出现 %q，实际为 %q", unwanted, got)
		}
	}
}
