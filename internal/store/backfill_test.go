package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestBackfillSwarmLeadHelpers 验证 hydra_leads.heads_json 中的 bee 线程回填 parent_thread_id 与 is_helper。
func TestBackfillSwarmLeadHelpers(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "backfill_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()

	if err := s.UpsertProject(ctx, Project{ID: "p1", Name: "Project 1", Path: "/tmp/p1"}); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}

	leadThread := Thread{
		ID:        "lead-1",
		ProjectID: "p1",
		Title:     "主会话",
		Provider:  "codex",
	}
	if err := s.CreateThread(ctx, leadThread); err != nil {
		t.Fatalf("CreateThread lead 失败: %v", err)
	}

	// 历史 bee 会话 1：parent_thread_id 为空，is_helper 为 0
	bee1 := Thread{
		ID:             "bee-1",
		ProjectID:      "p1",
		Title:          "Aspen: 子任务 1",
		Provider:       "agy",
		RuntimeMode:    "readonly",
		ParentThreadID: "",
		IsHelper:       false,
	}
	if err := s.CreateThread(ctx, bee1); err != nil {
		t.Fatalf("CreateThread bee1 失败: %v", err)
	}

	// 历史 bee 会话 2：parent_thread_id 为空，is_helper 为 0
	bee2 := Thread{
		ID:             "bee-2",
		ProjectID:      "p1",
		Title:          "Birch: 子任务 2",
		Provider:       "agy",
		RuntimeMode:    "readonly",
		ParentThreadID: "",
		IsHelper:       false,
	}
	if err := s.CreateThread(ctx, bee2); err != nil {
		t.Fatalf("CreateThread bee2 失败: %v", err)
	}

	// 独立已有父级的会话 3：parent_thread_id 已指定，不应被覆盖
	bee3 := Thread{
		ID:             "bee-3",
		ProjectID:      "p1",
		Title:          "Cove: 已有关联",
		Provider:       "agy",
		RuntimeMode:    "readonly",
		ParentThreadID: "existing-parent",
		IsHelper:       true,
	}
	if err := s.CreateThread(ctx, bee3); err != nil {
		t.Fatalf("CreateThread bee3 失败: %v", err)
	}

	// 写入 hydra_leads 记录，关联 bee-1 与 bee-2 到 lead-1
	beesJSON := `[{"threadId":"bee-1","name":"Aspen"},{"threadId":"bee-2","name":"Birch"},{"threadId":"bee-3","name":"Cove"}]`
	rec := SwarmLeadRecord{
		LeadThreadID: "lead-1",
		Enabled:      true,
		ProjectID:    "p1",
		BeesJSON:     beesJSON,
		UpdatedAt:    time.Now().UTC(),
	}
	if err := s.SaveSwarmLead(ctx, rec); err != nil {
		t.Fatalf("SaveSwarmLead 失败: %v", err)
	}

	// 执行幂等回填
	if err := s.backfillSwarmLeadHelpers(ctx); err != nil {
		t.Fatalf("backfillSwarmLeadHelpers 失败: %v", err)
	}

	// 验证 bee-1 被回填
	got1, err := s.GetThread(ctx, "bee-1")
	if err != nil {
		t.Fatalf("GetThread bee-1 失败: %v", err)
	}
	if got1.ParentThreadID != "lead-1" || !got1.IsHelper {
		t.Errorf("bee-1 回填后 ParentThreadID=%q, IsHelper=%v, 期望 lead-1 / true", got1.ParentThreadID, got1.IsHelper)
	}

	// 验证 bee-2 被回填
	got2, err := s.GetThread(ctx, "bee-2")
	if err != nil {
		t.Fatalf("GetThread bee-2 失败: %v", err)
	}
	if got2.ParentThreadID != "lead-1" || !got2.IsHelper {
		t.Errorf("bee-2 回填后 ParentThreadID=%q, IsHelper=%v, 期望 lead-1 / true", got2.ParentThreadID, got2.IsHelper)
	}

	// 验证已有父会话的 bee-3 未被覆盖
	got3, err := s.GetThread(ctx, "bee-3")
	if err != nil {
		t.Fatalf("GetThread bee-3 失败: %v", err)
	}
	if got3.ParentThreadID != "existing-parent" {
		t.Errorf("bee-3 不应被覆盖，ParentThreadID=%q", got3.ParentThreadID)
	}

	// 验证幂等性：再次调用不会报错或改变结果
	if err := s.backfillSwarmLeadHelpers(ctx); err != nil {
		t.Fatalf("再次执行 backfillSwarmLeadHelpers 失败: %v", err)
	}
	got1Again, _ := s.GetThread(ctx, "bee-1")
	if got1Again.ParentThreadID != "lead-1" || !got1Again.IsHelper {
		t.Errorf("再次回填后 bee-1 状态异常: %+v", got1Again)
	}

	// 验证 ListThreads 也会正确读出这两列
	list, err := s.ListThreads(ctx, "p1", false)
	if err != nil {
		t.Fatalf("ListThreads 失败: %v", err)
	}
	foundBee1 := false
	for _, th := range list {
		if th.ID == "bee-1" {
			foundBee1 = true
			if th.ParentThreadID != "lead-1" || !th.IsHelper {
				t.Errorf("ListThreads bee-1 ParentThreadID=%q, IsHelper=%v", th.ParentThreadID, th.IsHelper)
			}
		}
	}
	if !foundBee1 {
		t.Error("ListThreads 未能找到 bee-1")
	}
}

func TestBackfillSwarmLeadHelpersDuplicateAndInvalidJSON(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "backfill_duplicate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertProject(ctx, Project{ID: "p", Name: "p", Path: "/p"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"bee", "untouched"} {
		if err := s.CreateThread(ctx, Thread{ID: id, ProjectID: "p", Title: id, Provider: "codex"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, lead := range []SwarmLeadRecord{
		{LeadThreadID: "z-first", BeesJSON: `[{"threadId":"bee"}]`},
		{LeadThreadID: "a-second", BeesJSON: `[{"threadId":"bee"}]`},
		{LeadThreadID: "invalid", BeesJSON: `[{"threadId":"untouched"}`},
	} {
		if err := s.SaveSwarmLead(ctx, lead); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.backfillSwarmLeadHelpers(ctx); err != nil {
		t.Fatal(err)
	}
	bee, err := s.GetThread(ctx, "bee")
	if err != nil {
		t.Fatal(err)
	}
	if bee.ParentThreadID != "z-first" || !bee.IsHelper {
		t.Fatalf("bee: %+v", bee)
	}
	untouched, err := s.GetThread(ctx, "untouched")
	if err != nil {
		t.Fatal(err)
	}
	if untouched.ParentThreadID != "" || untouched.IsHelper {
		t.Fatalf("untouched: %+v", untouched)
	}
}
