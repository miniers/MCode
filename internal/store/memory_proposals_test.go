package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// openMemoryProposalStore 打开一个临时 Store；记忆提议不挂外键，无需预置项目与会话。
func openMemoryProposalStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "memory_proposals.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCreateMemoryProposalDefaults(t *testing.T) {
	ctx := context.Background()
	s := openMemoryProposalStore(t)

	created, err := s.CreateMemoryProposal(ctx, MemoryProposal{
		ProjectPath: "/p1",
		ThreadID:    "t1",
		Title:       "用 bet 分支验证",
		Content:     "改动先在 worktree 里跑一遍测试。",
		Importance:  0.7,
		Labels:      []string{"workflow", "testing"},
	})
	if err != nil {
		t.Fatalf("CreateMemoryProposal 失败: %v", err)
	}
	if created.ID == "" {
		t.Fatal("期望自动生成提议 ID")
	}
	if created.Status != MemoryProposalStatusPending {
		t.Fatalf("期望默认状态为 pending，实际为 %q", created.Status)
	}
	if created.Source != MemoryProposalSourceAgent {
		t.Fatalf("期望默认来源为 agent，实际为 %q", created.Source)
	}
	if created.UnitType != defaultMemoryProposalUnitType {
		t.Fatalf("期望默认类型为 fact，实际为 %q", created.UnitType)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatal("期望写入创建与更新时间")
	}

	got, err := s.GetMemoryProposal(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetMemoryProposal 失败: %v", err)
	}
	if got.Title != "用 bet 分支验证" || got.Content != "改动先在 worktree 里跑一遍测试。" {
		t.Fatalf("标题或正文不一致: %+v", got)
	}
	if got.ProjectPath != "/p1" || got.ThreadID != "t1" {
		t.Fatalf("项目路径或会话 ID 不一致: %+v", got)
	}
	if got.Importance != 0.7 {
		t.Fatalf("期望重要度为 0.7，实际为 %v", got.Importance)
	}
	if len(got.Labels) != 2 || got.Labels[0] != "workflow" || got.Labels[1] != "testing" {
		t.Fatalf("期望标签按序保留，实际为 %v", got.Labels)
	}
	if got.CreatedAt.IsZero() || !got.CreatedAt.Equal(got.UpdatedAt) {
		t.Fatalf("期望创建时间与更新时间一致，实际为 %v / %v", got.CreatedAt, got.UpdatedAt)
	}
}

func TestCreateMemoryProposalKeepsExplicitFields(t *testing.T) {
	ctx := context.Background()
	s := openMemoryProposalStore(t)

	created, err := s.CreateMemoryProposal(ctx, MemoryProposal{
		ID:         "mp-1",
		Title:      "显式 ID",
		Content:    "正文",
		UnitType:   "decision",
		Source:     MemoryProposalSourceUser,
		Status:     MemoryProposalStatusAccepted,
		Importance: 1,
	})
	if err != nil {
		t.Fatalf("CreateMemoryProposal 失败: %v", err)
	}
	if created.ID != "mp-1" || created.UnitType != "decision" ||
		created.Source != MemoryProposalSourceUser || created.Status != MemoryProposalStatusAccepted {
		t.Fatalf("显式字段被改写: %+v", created)
	}
	if created.Labels == nil || len(created.Labels) != 0 {
		t.Fatalf("期望未提供标签时为空数组，实际为 %v", created.Labels)
	}

	if _, err := s.CreateMemoryProposal(ctx, MemoryProposal{Content: "没有标题"}); err == nil {
		t.Fatal("标题为空时应报错")
	}
}

func TestGetMemoryProposalNotFound(t *testing.T) {
	s := openMemoryProposalStore(t)
	if _, err := s.GetMemoryProposal(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，实际为 %v", err)
	}
}

func TestListMemoryProposalsFiltersAndOrder(t *testing.T) {
	ctx := context.Background()
	s := openMemoryProposalStore(t)

	seed := []MemoryProposal{
		{ID: "old", ProjectPath: "/p1", Title: "旧提议", Content: "c", Status: MemoryProposalStatusAccepted},
		{ID: "new", ProjectPath: "/p1", Title: "新提议", Content: "c", Status: MemoryProposalStatusPending},
		{ID: "other", ProjectPath: "/p2", Title: "别的项目", Content: "c", Status: MemoryProposalStatusPending},
	}
	for _, p := range seed {
		if _, err := s.CreateMemoryProposal(ctx, p); err != nil {
			t.Fatalf("CreateMemoryProposal %s 失败: %v", p.ID, err)
		}
	}
	// 列表按 created_at 倒序，建立明确的时间先后（同一毫秒内写入也保持稳定顺序）。
	past := time.Now().UTC().Add(-2 * time.Hour)
	if _, err := s.db.ExecContext(ctx,
		"UPDATE memory_proposals SET created_at = ? WHERE id = ?", formatTime(past), "old"); err != nil {
		t.Fatalf("调整 created_at 失败: %v", err)
	}

	all, err := s.ListMemoryProposals(ctx, "", "", 0)
	if err != nil {
		t.Fatalf("ListMemoryProposals 失败: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("期望 3 条提议，实际为 %d 条: %+v", len(all), all)
	}
	if all[0].ID == "old" || all[len(all)-1].ID != "old" {
		t.Fatalf("期望按创建时间倒序且旧提议在最后，实际为 %v", proposalIDs(all))
	}

	byProject, err := s.ListMemoryProposals(ctx, "/p1", "", 0)
	if err != nil {
		t.Fatalf("按项目过滤失败: %v", err)
	}
	if len(byProject) != 2 {
		t.Fatalf("期望 /p1 有 2 条提议，实际为 %v", proposalIDs(byProject))
	}

	byStatus, err := s.ListMemoryProposals(ctx, "", MemoryProposalStatusPending, 0)
	if err != nil {
		t.Fatalf("按状态过滤失败: %v", err)
	}
	if len(byStatus) != 2 {
		t.Fatalf("期望 2 条待审提议，实际为 %v", proposalIDs(byStatus))
	}

	both, err := s.ListMemoryProposals(ctx, "/p1", MemoryProposalStatusPending, 0)
	if err != nil {
		t.Fatalf("组合过滤失败: %v", err)
	}
	if len(both) != 1 || both[0].ID != "new" {
		t.Fatalf("期望只剩 new，实际为 %v", proposalIDs(both))
	}

	limited, err := s.ListMemoryProposals(ctx, "", "", 1)
	if err != nil {
		t.Fatalf("limit 查询失败: %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("期望 limit 生效为 1 条，实际为 %v", proposalIDs(limited))
	}

	empty, err := s.ListMemoryProposals(ctx, "/none", "", 0)
	if err != nil {
		t.Fatalf("空结果查询失败: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("期望没有匹配项目时返回空列表，实际为 %v", proposalIDs(empty))
	}
}

func TestUpdateMemoryProposalStatus(t *testing.T) {
	ctx := context.Background()
	s := openMemoryProposalStore(t)

	created, err := s.CreateMemoryProposal(ctx, MemoryProposal{
		Title:   "待审提议",
		Content: "正文",
		Labels:  []string{"a"},
	})
	if err != nil {
		t.Fatalf("CreateMemoryProposal 失败: %v", err)
	}

	p := created
	p.Title = "改过的标题"
	p.Content = "改过的正文"
	p.UnitType = "decision"
	p.Importance = 0.9
	p.Labels = []string{"a", "b"}
	p.Status = MemoryProposalStatusAccepted
	p.RemoteMemoryID = "mem-9"
	if err := s.UpdateMemoryProposal(ctx, p); err != nil {
		t.Fatalf("UpdateMemoryProposal 失败: %v", err)
	}

	got, err := s.GetMemoryProposal(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetMemoryProposal 失败: %v", err)
	}
	if got.Title != "改过的标题" || got.Content != "改过的正文" || got.UnitType != "decision" {
		t.Fatalf("可编辑字段未更新: %+v", got)
	}
	if got.Importance != 0.9 || got.Status != MemoryProposalStatusAccepted || got.RemoteMemoryID != "mem-9" {
		t.Fatalf("状态字段未更新: %+v", got)
	}
	if len(got.Labels) != 2 || got.Labels[1] != "b" {
		t.Fatalf("标签未更新: %v", got.Labels)
	}
	if !got.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("UpdatedAt 应被刷新: 旧 %v 新 %v", created.UpdatedAt, got.UpdatedAt)
	}
	if !got.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("CreatedAt 不应变化: 旧 %v 新 %v", created.CreatedAt, got.CreatedAt)
	}

	if err := s.UpdateMemoryProposal(ctx, MemoryProposal{ID: "missing", Title: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("更新不存在的提议应返回 ErrNotFound，实际为 %v", err)
	}
}

func TestDeleteMemoryProposal(t *testing.T) {
	ctx := context.Background()
	s := openMemoryProposalStore(t)

	created, err := s.CreateMemoryProposal(ctx, MemoryProposal{Title: "要删掉", Content: "c"})
	if err != nil {
		t.Fatalf("CreateMemoryProposal 失败: %v", err)
	}
	if err := s.DeleteMemoryProposal(ctx, created.ID); err != nil {
		t.Fatalf("DeleteMemoryProposal 失败: %v", err)
	}
	if _, err := s.GetMemoryProposal(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后应返回 ErrNotFound，实际为 %v", err)
	}
	if err := s.DeleteMemoryProposal(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复删除应返回 ErrNotFound，实际为 %v", err)
	}
}

func TestCountPendingMemoryProposals(t *testing.T) {
	ctx := context.Background()
	s := openMemoryProposalStore(t)

	if count, err := s.CountPendingMemoryProposals(ctx, "/p1"); err != nil || count != 0 {
		t.Fatalf("期望初始待审数为 0，实际为 %d, %v", count, err)
	}

	for _, p := range []MemoryProposal{
		{ProjectPath: "/p1", Title: "待审 1", Content: "c"},
		{ProjectPath: "/p1", Title: "待审 2", Content: "c", Status: MemoryProposalStatusPending},
		{ProjectPath: "/p1", Title: "已接受", Content: "c", Status: MemoryProposalStatusAccepted},
		{ProjectPath: "/p2", Title: "别的项目待审", Content: "c"},
	} {
		if _, err := s.CreateMemoryProposal(ctx, p); err != nil {
			t.Fatalf("CreateMemoryProposal 失败: %v", err)
		}
	}

	count, err := s.CountPendingMemoryProposals(ctx, "/p1")
	if err != nil {
		t.Fatalf("CountPendingMemoryProposals 失败: %v", err)
	}
	if count != 2 {
		t.Fatalf("期望 /p1 有 2 条待审提议，实际为 %d", count)
	}

	// 项目路径为空表示统计全库。
	total, err := s.CountPendingMemoryProposals(ctx, "")
	if err != nil {
		t.Fatalf("全库统计失败: %v", err)
	}
	if total != 3 {
		t.Fatalf("期望全库 3 条待审提议，实际为 %d", total)
	}
}

// proposalIDs 提取提议 ID，便于断言列表内容。
func proposalIDs(list []MemoryProposal) []string {
	ids := make([]string, 0, len(list))
	for _, p := range list {
		ids = append(ids, p.ID)
	}
	return ids
}
