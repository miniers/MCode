package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mcode/internal/localmem"
	"mcode/internal/nmem"
	"mcode/internal/store"
)

// callMemoryPropose 经共享端点调用 memory_propose，返回原始文本、isError 与解析后的 JSON 载荷。
func callMemoryPropose(t *testing.T, srv *Server, projDir string, args map[string]any) (string, bool, map[string]any) {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	status, res := postRPC(t, ts.URL+"/mcp", srv.Token(), projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params":  map[string]any{"name": "memory_propose", "arguments": args},
	})
	if status != http.StatusOK {
		t.Fatalf("memory_propose 状态码期望 200, 实际为 %d", status)
	}
	return parseToolText(t, res)
}

// memoryProposeEndpoint 启动一个测试用 HTTP 端点，返回 tools/list 用的 MCP 地址。
func memoryProposeEndpoint(t *testing.T, srv *Server) string {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts.URL + "/mcp"
}

func TestMemoryProposeStoresPendingProposal(t *testing.T) {
	srv, s, projDir := setupTestMCP(t)
	ctx := context.Background()

	if !listToolNames(t, memoryProposeEndpoint(t, srv), srv.Token(), projDir)["memory_propose"] {
		t.Fatal("tools/list 应包含 memory_propose")
	}

	text, isError, payload := callMemoryPropose(t, srv, projDir, map[string]any{
		"title":      "先用 worktree 验证再改主干",
		"content":    "风险较大的改动先在 worktree 里跑通测试，再合回主干。",
		"unit_type":  "procedure",
		"importance": 0.9,
		"labels":     []any{"workflow", " testing "},
	})
	if isError {
		t.Fatalf("memory_propose 应成功, 实际为 %q", text)
	}
	id, _ := payload["id"].(string)
	if id == "" || payload["status"] != store.MemoryProposalStatusPending {
		t.Fatalf("期望返回提议 ID 与 pending 状态, 实际为 %v", payload)
	}
	if !strings.Contains(text, "review inbox") {
		t.Fatalf("返回文本应说明已进入待审收件箱, 实际为 %q", text)
	}

	list, err := s.ListMemoryProposals(ctx, projDir, store.MemoryProposalStatusPending, 0)
	if err != nil {
		t.Fatalf("ListMemoryProposals 失败: %v", err)
	}
	if len(list) != 1 || list[0].ID != id {
		t.Fatalf("期望落库一条待审提议, 实际为 %+v", list)
	}
	got := list[0]
	if got.Title != "先用 worktree 验证再改主干" || got.UnitType != "procedure" || got.Importance != 0.9 {
		t.Fatalf("提议字段与参数不一致: %+v", got)
	}
	if got.Source != store.MemoryProposalSourceAgent {
		t.Fatalf("期望来源为 agent, 实际为 %q", got.Source)
	}
	if got.ProjectPath != projDir {
		t.Fatalf("期望项目路径为 %q, 实际为 %q", projDir, got.ProjectPath)
	}
	if got.ThreadID != "" {
		t.Fatalf("共享端点没有会话身份, 期望 ThreadID 为空, 实际为 %q", got.ThreadID)
	}
	if len(got.Labels) != 2 || got.Labels[0] != "workflow" || got.Labels[1] != "testing" {
		t.Fatalf("标签应去空白后原序保存, 实际为 %v", got.Labels)
	}

	// 未提供 unit_type / importance 时使用默认值。
	if _, isError, _ := callMemoryPropose(t, srv, projDir, map[string]any{
		"title":   "默认值提议",
		"content": "正文",
	}); isError {
		t.Fatal("最小参数集也应成功")
	}
	defaults, err := s.ListMemoryProposals(ctx, projDir, store.MemoryProposalStatusPending, 0)
	if err != nil {
		t.Fatalf("ListMemoryProposals 失败: %v", err)
	}
	var found bool
	for _, p := range defaults {
		if p.Title == "默认值提议" {
			found = true
			if p.UnitType != memoryUnitTypeDefault || p.Importance != memoryImportanceDefault || len(p.Labels) != 0 {
				t.Fatalf("默认值不符合预期: %+v", p)
			}
		}
	}
	if !found {
		t.Fatal("未找到使用默认值的提议")
	}
}

func TestMemoryProposeUsesThreadScope(t *testing.T) {
	srv, s, projDir := setupTestMCP(t)
	registerThread(t, s, "mem-thread-1", "proj-test-1")

	status, res := postThreadRPC(t, srv, "mem-thread-1", map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{"name": "memory_propose", "arguments": map[string]any{
			"title":   "会话内提议",
			"content": "正文",
		}},
	})
	if status != http.StatusOK {
		t.Fatalf("工具调用状态码期望 200, 实际为 %d", status)
	}
	if text, isError, _ := parseToolText(t, res); isError {
		t.Fatalf("会话内提议应成功, 实际为 %q", text)
	}

	list, err := s.ListMemoryProposals(context.Background(), projDir, "", 0)
	if err != nil {
		t.Fatalf("ListMemoryProposals 失败: %v", err)
	}
	if len(list) != 1 || list[0].ThreadID != "mem-thread-1" || list[0].ProjectPath != projDir {
		t.Fatalf("期望提议记录会话与项目路径, 实际为 %+v", list)
	}
}

func TestMemoryProposeRejectsInvalidArguments(t *testing.T) {
	srv, s, projDir := setupTestMCP(t)

	cases := []struct {
		name string
		args map[string]any
	}{
		{"缺少 title", map[string]any{"content": "正文"}},
		{"title 只有空白", map[string]any{"title": "   ", "content": "正文"}},
		{"缺少 content", map[string]any{"title": "标题"}},
		{"unit_type 非法", map[string]any{"title": "标题", "content": "正文", "unit_type": "reminder"}},
		{"importance 过小", map[string]any{"title": "标题", "content": "正文", "importance": 0.05}},
		{"importance 过大", map[string]any{"title": "标题", "content": "正文", "importance": 1.5}},
	}
	for _, tc := range cases {
		text, isError, payload := callMemoryPropose(t, srv, projDir, tc.args)
		if !isError {
			t.Fatalf("%s 应报错, 实际返回 %q", tc.name, text)
		}
		if payload["code"] != CodeInvalidArguments {
			t.Fatalf("%s 期望错误码 %s, 实际为 %v (text=%q)", tc.name, CodeInvalidArguments, payload["code"], text)
		}
	}

	// 边界值应通过：0.1 与 1.0 都在允许区间内。
	for _, importance := range []any{0.1, 1.0} {
		if text, isError, _ := callMemoryPropose(t, srv, projDir, map[string]any{
			"title":      "边界重要度",
			"content":    "正文",
			"importance": importance,
		}); isError {
			t.Fatalf("importance=%v 应成功, 实际为 %q", importance, text)
		}
	}

	list, err := s.ListMemoryProposals(context.Background(), projDir, "", 0)
	if err != nil {
		t.Fatalf("ListMemoryProposals 失败: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("参数非法的调用不应落库, 期望仅 2 条提议, 实际为 %d 条", len(list))
	}
}

// TestMemoryProposeRejectsOverlongFields 验证 title/content 的长度上限：上限值通过，超一个 rune 被拒。
func TestMemoryProposeRejectsOverlongFields(t *testing.T) {
	srv, s, projDir := setupTestMCP(t)

	longTitle := strings.Repeat("长", memoryProposeTitleMaxRunes+1)
	if text, isError, payload := callMemoryPropose(t, srv, projDir, map[string]any{
		"title":   longTitle,
		"content": "正文",
	}); !isError || payload["code"] != CodeInvalidArguments || !strings.Contains(text, "at most 80") {
		t.Fatalf("title 超长应报 %s 并提示上限, 实际为 isError=%v text=%q", CodeInvalidArguments, isError, text)
	}

	longContent := strings.Repeat("记", memoryProposeContentMaxRunes+1)
	if text, isError, payload := callMemoryPropose(t, srv, projDir, map[string]any{
		"title":   "超长正文",
		"content": longContent,
	}); !isError || payload["code"] != CodeInvalidArguments {
		t.Fatalf("content 超长应报 %s, 实际为 isError=%v text=%q", CodeInvalidArguments, isError, text)
	} else if !strings.Contains(text, "1201") || !strings.Contains(text, "Condense it to the conclusion") {
		t.Fatalf("超长错误信息应写明当前长度并指导精简, 实际为 %q", text)
	}

	// 恰好等于上限应通过。
	if text, isError, _ := callMemoryPropose(t, srv, projDir, map[string]any{
		"title":   strings.Repeat("长", memoryProposeTitleMaxRunes),
		"content": strings.Repeat("记", memoryProposeContentMaxRunes),
	}); isError {
		t.Fatalf("恰好等于上限应成功, 实际为 %q", text)
	}

	list, err := s.ListMemoryProposals(context.Background(), projDir, "", 0)
	if err != nil {
		t.Fatalf("ListMemoryProposals 失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("超长提议不应落库, 期望仅 1 条提议, 实际为 %d 条", len(list))
	}
}

// callMemorySearch 经共享端点调用 memory_search，返回原始文本、isError 与解析后的 JSON 载荷。
func callMemorySearch(t *testing.T, srv *Server, projDir string, args map[string]any) (string, bool, map[string]any) {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	status, res := postRPC(t, ts.URL+"/mcp", srv.Token(), projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "tools/call",
		"params":  map[string]any{"name": "memory_search", "arguments": args},
	})
	if status != http.StatusOK {
		t.Fatalf("memory_search 状态码期望 200, 实际为 %d", status)
	}
	return parseToolText(t, res)
}

func TestMemorySearchWithoutClient(t *testing.T) {
	srvWithStub, _, projDirWithStub := setupTestMCPWithOptions(t, func(o *Options) {
		o.MemorySearch = func(ctx context.Context, projectPath string, q nmem.SearchQuery) ([]nmem.Memory, error) {
			return nil, nil
		}
	})
	if !listToolNames(t, memoryProposeEndpoint(t, srvWithStub), srvWithStub.Token(), projDirWithStub)["memory_search"] {
		t.Fatal("tools/list 应包含 memory_search")
	}

	srv, _, projDir := setupTestMCP(t)

	text, isError, payload := callMemorySearch(t, srv, projDir, map[string]any{"query": "workflow"})
	if !isError {
		t.Fatalf("未接入 Nowledge Mem 时应报错, 实际为 %q", text)
	}
	if payload["code"] != CodeInternal || !strings.Contains(text, "Long-term memory is not configured in mcode") {
		t.Fatalf("期望明确的未配置错误, 实际为 %q", text)
	}
}

func TestMemorySearchFormatsAndTruncates(t *testing.T) {
	longContent := strings.Repeat("很长的记忆内容", 100) // 700 个 rune，超过 400 的上限
	var gotPath, gotQuery string
	var gotLimit int

	srv, _, projDir := setupTestMCPWithOptions(t, func(o *Options) {
		o.MemorySearch = func(ctx context.Context, projectPath string, q nmem.SearchQuery) ([]nmem.Memory, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("memory_search 应给检索加上超时")
			}
			gotPath, gotQuery, gotLimit = projectPath, q.Query, q.Limit
			return []nmem.Memory{
				{
					ID:         "mem-1",
					Title:      "Use worktree branches",
					Content:    "Risky changes run in a worktree first.",
					UnitType:   "decision",
					Importance: 0.9,
					CreatedAt:  time.Date(2026, 10, 27, 6, 28, 0, 0, time.UTC),
				},
				{
					ID:       "mem-2",
					Title:    "Long memory",
					Content:  longContent,
					UnitType: "fact",
				},
			}, nil
		}
	})

	text, isError, _ := callMemorySearch(t, srv, projDir, map[string]any{"query": "workflow"})
	if isError {
		t.Fatalf("memory_search 应成功, 实际为 %q", text)
	}
	if gotPath != projDir || gotQuery != "workflow" || gotLimit != memorySearchDefaultLimit {
		t.Fatalf("期望传入项目路径、查询与默认条数, 实际为 path=%q query=%q limit=%d", gotPath, gotQuery, gotLimit)
	}

	wantHead := "- **Use worktree branches** · decision · importance 0.9 · 2026-10-27"
	if !strings.Contains(text, wantHead) {
		t.Fatalf("标题行格式不符合预期, 实际为 %q", text)
	}
	if !strings.Contains(text, "Risky changes run in a worktree first.") {
		t.Fatalf("应包含记忆摘要, 实际为 %q", text)
	}
	if !strings.Contains(text, "ID: mem-1") || !strings.Contains(text, "ID: mem-2") {
		t.Fatalf("每条结果应附记忆 ID, 实际为 %q", text)
	}

	// 超长内容截断到 400 个 rune 并加省略号。
	lines := strings.Split(text, "\n")
	summary := ""
	for i, line := range lines {
		if strings.Contains(line, "ID: mem-2") && i > 0 {
			summary = strings.TrimSpace(lines[i-1])
		}
	}
	if summary == "" {
		t.Fatalf("未找到第二条记忆的摘要行, 实际为 %q", text)
	}
	if runes := []rune(summary); len(runes) != memorySearchContentMaxRunes+1 || !strings.HasSuffix(summary, "…") {
		t.Fatalf("摘要应截断到 %d 个 rune 并加省略号, 实际长度 %d", memorySearchContentMaxRunes, len(runes))
	}
}

func TestMemorySearchClampsLimit(t *testing.T) {
	var gotLimit int
	srv, _, projDir := setupTestMCPWithOptions(t, func(o *Options) {
		o.MemorySearch = func(ctx context.Context, projectPath string, q nmem.SearchQuery) ([]nmem.Memory, error) {
			gotLimit = q.Limit
			return nil, nil
		}
	})

	cases := []struct {
		limit any
		want  int
	}{
		{float64(0), memorySearchMinLimit},
		{float64(-5), memorySearchMinLimit},
		{float64(5), 5},
		{float64(99), memorySearchMaxLimit},
	}
	for _, tc := range cases {
		if _, isError, _ := callMemorySearch(t, srv, projDir, map[string]any{"query": "q", "limit": tc.limit}); isError {
			t.Fatalf("limit=%v 应成功", tc.limit)
		}
		if gotLimit != tc.want {
			t.Fatalf("limit=%v 期望夹值为 %d, 实际为 %d", tc.limit, tc.want, gotLimit)
		}
	}
}

func TestMemorySearchEmptyResults(t *testing.T) {
	srv, _, projDir := setupTestMCPWithOptions(t, func(o *Options) {
		o.MemorySearch = func(ctx context.Context, projectPath string, q nmem.SearchQuery) ([]nmem.Memory, error) {
			return nil, nil
		}
	})

	text, isError, _ := callMemorySearch(t, srv, projDir, map[string]any{"query": "nothing"})
	if isError {
		t.Fatalf("空结果不应报错, 实际为 %q", text)
	}
	if text != "No matching memories." {
		t.Fatalf("空结果文本应为 No matching memories., 实际为 %q", text)
	}
}

func TestMemorySearchRejectsEmptyQuery(t *testing.T) {
	srv, _, projDir := setupTestMCPWithOptions(t, func(o *Options) {
		o.MemorySearch = func(ctx context.Context, projectPath string, q nmem.SearchQuery) ([]nmem.Memory, error) {
			t.Error("空查询不应调用 MemorySearch")
			return nil, nil
		}
	})

	for _, args := range []map[string]any{{}, {"query": ""}, {"query": "   "}} {
		text, isError, payload := callMemorySearch(t, srv, projDir, args)
		if !isError {
			t.Fatalf("空查询应报错, 实际为 %q", text)
		}
		if payload["code"] != CodeInvalidArguments {
			t.Fatalf("期望错误码 %s, 实际为 %v (text=%q)", CodeInvalidArguments, payload["code"], text)
		}
	}
}

// callMemoryTool 经共享端点调用任一记忆工具，返回原始文本、isError 与解析后的 JSON 载荷。
func callMemoryTool(t *testing.T, srv *Server, projDir, name string, args map[string]any) (string, bool, map[string]any) {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	status, res := postRPC(t, ts.URL+"/mcp", srv.Token(), projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      5,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": args},
	})
	if status != http.StatusOK {
		t.Fatalf("%s 状态码期望 200, 实际为 %d", name, status)
	}
	return parseToolText(t, res)
}

func TestMemorySearchPassesFilters(t *testing.T) {
	var got nmem.SearchQuery
	srv, _, projDir := setupTestMCPWithOptions(t, func(o *Options) {
		o.MemorySearch = func(ctx context.Context, projectPath string, q nmem.SearchQuery) ([]nmem.Memory, error) {
			got = q
			return nil, nil
		}
	})

	if _, isError, _ := callMemorySearch(t, srv, projDir, map[string]any{
		"query": "q", "unit_type": "decision", "labels": []any{"ui"}, "mode": "fast",
	}); isError {
		t.Fatal("带过滤条件的检索应成功")
	}
	if got.UnitType != "decision" || len(got.Labels) != 1 || got.Labels[0] != "ui" || got.Mode != "fast" {
		t.Fatalf("过滤条件未透传, 实际为 %+v", got)
	}

	for _, args := range []map[string]any{
		{"query": "q", "unit_type": "insight"},
		{"query": "q", "mode": "slow"},
	} {
		if _, isError, payload := callMemorySearch(t, srv, projDir, args); !isError || payload["code"] != CodeInvalidArguments {
			t.Fatalf("非法过滤条件 %v 应报参数错误", args)
		}
	}
}

func TestMemoryGetAcceptsLinkAndMapsNotFound(t *testing.T) {
	var gotID string
	srv, _, projDir := setupTestMCPWithOptions(t, func(o *Options) {
		o.MemoryGet = func(ctx context.Context, projectPath, id string) (nmem.Memory, error) {
			gotID = id
			if id == "missing" {
				return nmem.Memory{}, &nmem.APIError{Status: http.StatusNotFound}
			}
			return nmem.Memory{ID: id, Title: "Worktree first", Content: "Full body.", UnitType: "decision", Importance: 0.8, Labels: []string{"git"}}, nil
		}
	})

	text, isError, _ := callMemoryTool(t, srv, projDir, "memory_get", map[string]any{"id": "nowledgemem://memory/mem-9"})
	if isError {
		t.Fatalf("memory_get 应成功, 实际为 %q", text)
	}
	if gotID != "mem-9" {
		t.Fatalf("链接应去掉 nowledgemem://memory/ 前缀, 实际 ID 为 %q", gotID)
	}
	for _, want := range []string{"# Worktree first", "ID: mem-9", "decision · importance 0.8", "Labels: git", "Full body."} {
		if !strings.Contains(text, want) {
			t.Fatalf("结果应包含 %q, 实际为 %q", want, text)
		}
	}

	if _, isError, payload := callMemoryTool(t, srv, projDir, "memory_get", map[string]any{"id": "missing"}); !isError || payload["code"] != CodeNotFound {
		t.Fatalf("远端 404 应映射为 not_found, 实际为 %v", payload)
	}
}

func TestThreadSearchAndFetch(t *testing.T) {
	var gotOffset, gotLimit int
	srv, _, projDir := setupTestMCPWithOptions(t, func(o *Options) {
		o.ThreadSearch = func(ctx context.Context, projectPath, query string, limit int) ([]nmem.ThreadHit, error) {
			return []nmem.ThreadHit{{
				ThreadID: "mcode-t1", Title: "Fix merge", Source: "mcode", MessageCount: 30,
				Matches: []nmem.ThreadMatch{{Index: 12, Role: "assistant", Snippet: "rebased onto main"}},
			}}, nil
		}
		o.ThreadFetch = func(ctx context.Context, projectPath, threadID string, offset, limit int) (nmem.ThreadPage, error) {
			gotOffset, gotLimit = offset, limit
			return nmem.ThreadPage{ThreadID: threadID, Title: "Fix merge", Total: 30, Offset: offset,
				Messages: []nmem.Message{{Role: "user", Content: "why?"}, {Role: "assistant", Content: "because"}}}, nil
		}
	})

	text, isError, _ := callMemoryTool(t, srv, projDir, "thread_search", map[string]any{"query": "merge"})
	if isError || !strings.Contains(text, "thread_id: mcode-t1") || !strings.Contains(text, "[#12 assistant] rebased onto main") {
		t.Fatalf("thread_search 结果不符合预期: %q", text)
	}

	text, isError, _ = callMemoryTool(t, srv, projDir, "thread_fetch_messages", map[string]any{"thread_id": "mcode-t1", "offset": float64(12), "limit": float64(99)})
	if isError {
		t.Fatalf("thread_fetch_messages 应成功, 实际为 %q", text)
	}
	if gotOffset != 12 || gotLimit != threadFetchMaxLimit {
		t.Fatalf("offset/limit 未正确透传或夹值: offset=%d limit=%d", gotOffset, gotLimit)
	}
	for _, want := range []string{"messages 12-13 of 30", "## #12 user", "## #13 assistant", "offset 14"} {
		if !strings.Contains(text, want) {
			t.Fatalf("结果应包含 %q, 实际为 %q", want, text)
		}
	}
}

func TestMemoryProposeUserRequestedCommitsImmediately(t *testing.T) {
	var committed string
	srv, s, projDir := setupTestMCPWithOptions(t, func(o *Options) {
		o.MemoryCommit = func(ctx context.Context, proposalID string) (store.MemoryProposal, string, error) {
			committed = proposalID
			p, err := o.Store.GetMemoryProposal(ctx, proposalID)
			if err != nil {
				return p, "", err
			}
			p.Status = store.MemoryProposalStatusAccepted
			p.RemoteMemoryID = "remote-1"
			return p, "global", o.Store.UpdateMemoryProposal(ctx, p)
		}
	})

	text, isError, payload := callMemoryPropose(t, srv, projDir, map[string]any{
		"title": "默认中文回复", "content": "所有项目默认用中文回复。", "scope": "global", "user_requested": true,
	})
	if isError || payload["success"] != true || payload["space"] != "global" {
		t.Fatalf("用户要求记住时应直接写入, 实际为 %q", text)
	}
	// 文案对两个后端通用：不提 Nowledge Mem，空间非空时写进文案。
	if msg, _ := payload["message"].(string); !strings.Contains(msg, "long-term memory") || strings.Contains(msg, "Nowledge Mem") || !strings.Contains(msg, "space global") {
		t.Fatalf("提交成功文案应提到长期记忆与写入空间, 实际为 %q", msg)
	}
	got, err := s.GetMemoryProposal(context.Background(), committed)
	if err != nil {
		t.Fatalf("读取提议失败: %v", err)
	}
	if got.Scope != store.MemoryProposalScopeGlobal || got.Source != store.MemoryProposalSourceUser || got.Status != store.MemoryProposalStatusAccepted {
		t.Fatalf("提议的范围、来源或状态不符合预期: %+v", got)
	}

	// 不带 user_requested 时仍只进收件箱。
	_, _, payload = callMemoryPropose(t, srv, projDir, map[string]any{"title": "普通提议", "content": "正文"})
	if payload["status"] != store.MemoryProposalStatusPending {
		t.Fatalf("普通提议应保持 pending, 实际为 %v", payload)
	}
	if _, isError, payload := callMemoryPropose(t, srv, projDir, map[string]any{"title": "t", "content": "c", "scope": "team"}); !isError || payload["code"] != CodeInvalidArguments {
		t.Fatal("非法 scope 应报参数错误")
	}
}

func TestMemoryProposeTargetMustBeReadable(t *testing.T) {
	srv, s, projDir := setupTestMCPWithOptions(t, func(o *Options) {
		o.MemoryGet = func(ctx context.Context, projectPath, id string) (nmem.Memory, error) {
			if id == "mem-ok" {
				return nmem.Memory{ID: id}, nil
			}
			// 本地后端的错误也是带 404 的 *nmem.APIError，MCP 层必须映射为参数错误。
			return nmem.Memory{}, localmem.ErrNotFound
		}
	})

	if _, isError, payload := callMemoryPropose(t, srv, projDir, map[string]any{"title": "t", "content": "c", "target_memory_id": "mem-other"}); !isError || payload["code"] != CodeInvalidArguments {
		t.Fatal("改写别的空间里的记忆应被拒绝")
	}
	_, isError, payload := callMemoryPropose(t, srv, projDir, map[string]any{"title": "t", "content": "c", "target_memory_id": "nowledgemem://memory/mem-ok"})
	if isError {
		t.Fatalf("改写可读的记忆应成功: %v", payload)
	}
	got, err := s.GetMemoryProposal(context.Background(), payload["id"].(string))
	if err != nil || got.TargetMemoryID != "mem-ok" {
		t.Fatalf("改写目标应落库为裸 ID, 实际为 %+v (%v)", got, err)
	}
}

// TestMemoryToolsListFilterUnconfigured 校验未配置四个检索回调时从 tools/list 隐藏，配置后全部可见。
func TestMemoryToolsListFilterUnconfigured(t *testing.T) {
	unconfiguredTools := []string{"memory_search", "memory_get", "memory_forget", "thread_search", "thread_fetch_messages"}

	// 1. 未设置四个回调时，tools/list 包含 memory_propose，但不包含检索工具。
	srvUnconfigured, _, projDir := setupTestMCP(t)
	namesUnconfigured := listToolNames(t, memoryProposeEndpoint(t, srvUnconfigured), srvUnconfigured.Token(), projDir)

	if !namesUnconfigured["memory_propose"] {
		t.Fatal("未接入 Nowledge Mem 时 tools/list 应包含 memory_propose")
	}
	for _, name := range unconfiguredTools {
		if namesUnconfigured[name] {
			t.Fatalf("未接入 Nowledge Mem 时 tools/list 不应包含 %s", name)
		}
	}

	// 2. 设置了四个回调时，四个检索与对话工具以及 memory_propose 都在。
	srvConfigured, _, projDir2 := setupTestMCPWithOptions(t, func(o *Options) {
		o.MemorySearch = func(ctx context.Context, projectPath string, q nmem.SearchQuery) ([]nmem.Memory, error) {
			return nil, nil
		}
		o.MemoryGet = func(ctx context.Context, projectPath, id string) (nmem.Memory, error) {
			return nmem.Memory{}, nil
		}
		o.MemoryForget = func(ctx context.Context, projectPath, id string) (bool, error) {
			return true, nil
		}
		o.ThreadSearch = func(ctx context.Context, projectPath, query string, limit int) ([]nmem.ThreadHit, error) {
			return nil, nil
		}
		o.ThreadFetch = func(ctx context.Context, projectPath, threadID string, offset, limit int) (nmem.ThreadPage, error) {
			return nmem.ThreadPage{}, nil
		}
	})
	namesConfigured := listToolNames(t, memoryProposeEndpoint(t, srvConfigured), srvConfigured.Token(), projDir2)

	if !namesConfigured["memory_propose"] {
		t.Fatal("接入 Nowledge Mem 时 tools/list 应包含 memory_propose")
	}
	for _, name := range unconfiguredTools {
		if !namesConfigured[name] {
			t.Fatalf("接入 Nowledge Mem 时 tools/list 应包含 %s", name)
		}
	}
}

func TestMemoryForgetDeletesAndDropsPendingProposals(t *testing.T) {
	var gotID string
	srv, st, projDir := setupTestMCPWithOptions(t, func(o *Options) {
		o.MemoryForget = func(ctx context.Context, projectPath, id string) (bool, error) {
			gotID = id
			return true, nil
		}
	})

	ctx := context.Background()
	// 一条指向该记忆的待审提议，忘掉后应一并清除；另建一条无关提议作为对照。
	targeted, err := st.CreateMemoryProposal(ctx, store.MemoryProposal{
		ProjectPath: projDir, Title: "旧结论", Content: "旧正文", TargetMemoryID: "mem-1",
	})
	if err != nil {
		t.Fatalf("CreateMemoryProposal 失败: %v", err)
	}
	unrelated, err := st.CreateMemoryProposal(ctx, store.MemoryProposal{
		ProjectPath: projDir, Title: "另一条", Content: "无关正文", TargetMemoryID: "mem-2",
	})
	if err != nil {
		t.Fatalf("CreateMemoryProposal 失败: %v", err)
	}

	text, isError, _ := callMemoryTool(t, srv, projDir, "memory_forget", map[string]any{
		"id": "nowledgemem://memory/mem-1", "reason": "已过时",
	})
	if isError {
		t.Fatalf("memory_forget 应成功, 实际为 %q", text)
	}
	if gotID != "mem-1" {
		t.Fatalf("链接应去掉前缀后传入, 实际 ID 为 %q", gotID)
	}
	for _, want := range []string{"deleted", "已过时"} {
		if !strings.Contains(text, want) {
			t.Fatalf("结果应包含 %q, 实际为 %q", want, text)
		}
	}

	if _, err := st.GetMemoryProposal(ctx, targeted.ID); err == nil {
		t.Fatal("指向该记忆的待审提议应被删除")
	}
	if _, err := st.GetMemoryProposal(ctx, unrelated.ID); err != nil {
		t.Fatalf("无关提议应保留, 实际为 %v", err)
	}
}

func TestMemoryForgetMarksDeprecatedWhenUnsupported(t *testing.T) {
	srv, _, projDir := setupTestMCPWithOptions(t, func(o *Options) {
		o.MemoryForget = func(ctx context.Context, projectPath, id string) (bool, error) {
			return false, nil
		}
	})

	text, isError, _ := callMemoryTool(t, srv, projDir, "memory_forget", map[string]any{"id": "mem-7"})
	if isError {
		t.Fatalf("服务端不支持删除时也应成功, 实际为 %q", text)
	}
	for _, want := range []string{"deprecated", nmem.DeprecatedTitlePrefix} {
		if !strings.Contains(text, want) {
			t.Fatalf("结果应包含 %q, 实际为 %q", want, text)
		}
	}
}

func TestMemoryForgetRejectsInvalidID(t *testing.T) {
	calls := 0
	srv, _, projDir := setupTestMCPWithOptions(t, func(o *Options) {
		o.MemoryForget = func(ctx context.Context, projectPath, id string) (bool, error) {
			calls++
			return true, nil
		}
	})

	for _, args := range []map[string]any{{}, {"id": ""}, {"id": "   "}, {"id": "nowledgemem://memory/"}} {
		_, isError, payload := callMemoryTool(t, srv, projDir, "memory_forget", args)
		if !isError {
			t.Fatalf("非法 ID %v 应报错", args)
		}
		if payload["code"] != CodeInvalidArguments {
			t.Fatalf("期望错误码 %s, 实际为 %v", CodeInvalidArguments, payload["code"])
		}
	}
	if calls != 0 {
		t.Fatalf("ID 非法时不应调用远端, 实际调用 %d 次", calls)
	}

	// 远端不存在时映射为 not_found。
	srvMissing, _, projDir2 := setupTestMCPWithOptions(t, func(o *Options) {
		o.MemoryForget = func(ctx context.Context, projectPath, id string) (bool, error) {
			return false, &nmem.APIError{Status: http.StatusNotFound}
		}
	})
	if _, isError, payload := callMemoryTool(t, srvMissing, projDir2, "memory_forget", map[string]any{"id": "mem-x"}); !isError || payload["code"] != CodeNotFound {
		t.Fatalf("远端 404 应映射为 not_found, 实际为 %v", payload)
	}
}

func TestMemorySearchDropsDeprecatedMemories(t *testing.T) {
	srv, _, projDir := setupTestMCPWithOptions(t, func(o *Options) {
		o.MemorySearch = func(ctx context.Context, projectPath string, q nmem.SearchQuery) ([]nmem.Memory, error) {
			return []nmem.Memory{
				{ID: "keep", Title: "仍然有效", Content: "保留的正文", UnitType: "fact", Importance: 0.6},
				{ID: "gone", Title: " " + nmem.DeprecatedTitlePrefix + " 已过时", Content: "废弃的正文", UnitType: "fact", Importance: 0.6},
			}, nil
		}
	})

	text, isError, _ := callMemorySearch(t, srv, projDir, map[string]any{"query": "q"})
	if isError {
		t.Fatalf("检索应成功, 实际为 %q", text)
	}
	if !strings.Contains(text, "仍然有效") {
		t.Fatalf("结果应包含有效记忆, 实际为 %q", text)
	}
	if strings.Contains(text, "已过时") {
		t.Fatalf("结果不应包含已废弃的记忆, 实际为 %q", text)
	}
}
