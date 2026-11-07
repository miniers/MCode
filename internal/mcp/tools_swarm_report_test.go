package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"mcode/internal/store"
)

func callSwarmReportGet(t *testing.T, srv *Server, threadID string, args map[string]any) (string, bool) {
	t.Helper()
	status, res := postThreadRPC(t, srv, threadID, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params":  map[string]any{"name": swarmReportGetToolName, "arguments": args},
	})
	if status != 200 {
		t.Fatalf("tools/call 状态码期望 200, 实际为 %d", status)
	}
	text, isError, _ := parseToolText(t, res)
	return text, isError
}

func TestSwarmReportGetReturnsFullSummaryForOwnLead(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "lead-1", "proj-test-1")
	registerThread(t, st, "lead-2", "proj-test-1")

	full := strings.Repeat("长", 3000)
	if err := st.SaveSwarmReport(context.Background(), store.SwarmReport{
		BeeID: "bee-1", LeadThreadID: "lead-1", Name: "Ada", Task: "改造", Summary: full,
	}); err != nil {
		t.Fatalf("写入存档失败: %v", err)
	}

	text, isError := callSwarmReportGet(t, srv, "lead-1", map[string]any{"bee_id": "bee-1"})
	if isError {
		t.Fatalf("本主会话读取应成功: %s", text)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("结果不是 JSON: %v (%s)", err, text)
	}
	if got["name"] != "Ada" || got["task"] != "改造" || got["summary"] != full {
		t.Fatalf("返回内容不完整: name=%q task=%q summary 长度=%d", got["name"], got["task"], len(got["summary"]))
	}

	// 别的主会话不能读这只 bee 的汇报。
	text, isError = callSwarmReportGet(t, srv, "lead-2", map[string]any{"bee_id": "bee-1"})
	if !isError || !strings.Contains(text, CodeForbidden) {
		t.Fatalf("跨主会话读取应被拒绝, 实际: %v %s", isError, text)
	}

	text, isError = callSwarmReportGet(t, srv, "lead-1", map[string]any{"bee_id": "bee-missing"})
	if !isError || !strings.Contains(text, CodeNotFound) {
		t.Fatalf("不存在的 bee 应返回 not_found, 实际: %v %s", isError, text)
	}
}

// TestSwarmReportGetListSorted bees 列表按 StartedAt 升序返回，字段齐全，未结束时不含 finished_at。
func TestSwarmReportGetListSorted(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	finished := base.Add(30 * time.Minute)
	srv, st, _ := setupTestMCPWithOptions(t, func(o *Options) {
		o.SwarmBees = func(leadThreadID string) []SwarmBeeSummary {
			if leadThreadID != "lead-1" {
				return nil
			}
			return []SwarmBeeSummary{
				{ID: "bee-late", Name: "Late", Task: "第三个", Tier: "worker", Status: "running", StartedAt: base.Add(2 * time.Hour), LastProgressAt: base.Add(150 * time.Minute)},
				{ID: "bee-early", Name: "Early", Task: "第一个", Tier: "researcher", Status: "completed", StartedAt: base, FinishedAt: &finished, LastProgressAt: base.Add(10 * time.Minute)},
				{ID: "bee-mid", Name: "Mid", Task: "第二个", Tier: "reviewer", Status: "failed", StartedAt: base.Add(time.Hour), LastProgressAt: base.Add(70 * time.Minute)},
			}
		}
	})
	registerThread(t, st, "lead-1", "proj-test-1")

	text, isError := callSwarmReportGet(t, srv, "lead-1", map[string]any{})
	if isError {
		t.Fatalf("列表模式应成功: %s", text)
	}
	var got struct {
		Bees []map[string]any `json:"bees"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("结果不是 JSON: %v (%s)", err, text)
	}
	if len(got.Bees) != 3 {
		t.Fatalf("应返回 3 只 bee, 实际 %d: %s", len(got.Bees), text)
	}
	wantIDs := []string{"bee-early", "bee-mid", "bee-late"}
	for i, want := range wantIDs {
		if got.Bees[i]["bee_id"] != want {
			t.Fatalf("第 %d 项应为 %s, 实际 %v", i, want, got.Bees[i]["bee_id"])
		}
	}
	first := got.Bees[0]
	for _, field := range []string{"name", "task", "tier", "status", "started_at", "finished_at", "last_progress_at"} {
		if _, ok := first[field]; !ok {
			t.Fatalf("第一项缺少字段 %s: %v", field, first)
		}
	}
	if first["status"] != "completed" || first["tier"] != "researcher" {
		t.Fatalf("第一项字段不符: %v", first)
	}
	if first["started_at"] != base.Format(time.RFC3339) || first["finished_at"] != finished.Format(time.RFC3339) {
		t.Fatalf("第一项时间格式不符: %v", first)
	}
	for _, item := range got.Bees[1:] {
		if _, ok := item["finished_at"]; ok {
			t.Fatalf("未结束的 bee 不应输出 finished_at: %v", item)
		}
	}
}

// TestSwarmReportGetListUnavailable 未接入 bee 快照时，列表模式返回 internal 错误。
func TestSwarmReportGetListUnavailable(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	registerThread(t, st, "lead-1", "proj-test-1")

	text, isError := callSwarmReportGet(t, srv, "lead-1", map[string]any{})
	if !isError || !strings.Contains(text, CodeInternal) {
		t.Fatalf("未接入 bee 快照时应返回 internal 错误, 实际: %v %s", isError, text)
	}
}

// TestSwarmReportGetListNeedsSessionScope 没有会话作用域身份时，列表模式同样返回 forbidden。
func TestSwarmReportGetListNeedsSessionScope(t *testing.T) {
	srv, _, _ := setupTestMCPWithOptions(t, func(o *Options) {
		o.SwarmBees = func(string) []SwarmBeeSummary {
			return []SwarmBeeSummary{{ID: "bee-1", Status: "running", StartedAt: time.Now()}}
		}
	})
	res := handleSwarmReportGet(ToolContext{Context: context.Background(), Server: srv}, map[string]any{})
	if code := toolErrorCode(res); code != CodeForbidden {
		t.Fatalf("无会话身份应返回 forbidden, 实际: %s", res.Text)
	}
}
