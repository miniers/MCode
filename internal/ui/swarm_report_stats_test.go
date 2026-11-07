package mcodeui

import (
	"strings"
	"testing"
)

func TestParseSwarmReportStats(t *testing.T) {
	text := "[编排] 子代理汇报：Quill 已结束工作。\n\n" +
		"## Quill：修复测试 (已完成)\n" +
		"- 合并结果：已合入\n" +
		`- 改动文件：["a.go","b.go"]` + "\n" +
		"- 冲突文件：[]\n" +
		`- 统计：{"durationMs":33000,"toolCalls":9,"linesAdded":3,"linesDeleted":3}` + "\n" +
		"- 汇报总结：完成。\n"

	_, entries := parseSwarmReport(text)
	if len(entries) != 1 {
		t.Fatalf("汇报项数量 = %d，预期 1: %+v", len(entries), entries)
	}

	e := entries[0]
	if e.DurationMs != 33000 {
		t.Errorf("DurationMs = %d，预期 33000", e.DurationMs)
	}
	if e.ToolCalls != 9 {
		t.Errorf("ToolCalls = %d，预期 9", e.ToolCalls)
	}
	if e.LinesAdded != 3 {
		t.Errorf("LinesAdded = %d，预期 3", e.LinesAdded)
	}
	if e.LinesDeleted != 3 {
		t.Errorf("LinesDeleted = %d，预期 3", e.LinesDeleted)
	}
	if !e.statsRecorded {
		t.Error("statsRecorded = false，预期 true")
	}
	if strings.Contains(e.Summary, "统计") {
		t.Errorf("统计行被并入汇报正文: %q", e.Summary)
	}
}

func TestSwarmReportCaptionParts(t *testing.T) {
	_, entries := parseSwarmReport("[编排] 子代理汇报：Quill 已结束工作。\n\n" +
		"## Quill：修复测试 (已完成)\n" +
		"- 合并结果：已合入\n" +
		`- 改动文件：["a.go","b.go"]` + "\n" +
		"- 冲突文件：[]\n" +
		`- 统计：{"durationMs":33000,"toolCalls":9,"linesAdded":3,"linesDeleted":3}` + "\n" +
		"- 汇报总结：完成。\n")
	if len(entries) != 1 {
		t.Fatalf("汇报项数量 = %d，预期 1", len(entries))
	}

	duration, tools, files, added, deleted := swarmReportCaptionParts(entries[0], nil)
	if duration == "" {
		t.Error("duration 为空，预期 33秒")
	}
	if tools != 9 || files != 2 || added != 3 || deleted != 3 {
		t.Errorf("统计值 = tools %d, files %d, added %d, deleted %d，预期 9/2/3/3", tools, files, added, deleted)
	}

	// 旧汇报没有统计行，也没有可回退的 bee 信息。
	_, legacy := parseSwarmReport("[编排] 子代理汇报：Quill 已结束工作。\n\n" +
		"## Quill：修复测试 (已完成)\n" +
		"- 合并结果：已合入\n" +
		`- 改动文件：["a.go","b.go"]` + "\n" +
		"- 冲突文件：[]\n" +
		"- 汇报总结：完成。\n")
	if len(legacy) != 1 {
		t.Fatalf("汇报项数量 = %d，预期 1", len(legacy))
	}
	if legacy[0].statsRecorded {
		t.Error("旧汇报的 statsRecorded = true，预期 false")
	}
	duration, tools, files, added, deleted = swarmReportCaptionParts(legacy[0], nil)
	if tools != 0 || added != 0 || deleted != 0 {
		t.Errorf("无统计行时 = tools %d, added %d, deleted %d，预期全为 0", tools, added, deleted)
	}
	if duration != "" {
		t.Errorf("无统计行且无 bee 时 duration = %q，预期空", duration)
	}
	if files != 2 {
		t.Errorf("files = %d，预期 2（来自汇报里的文件快照）", files)
	}
}
