package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// 验证 queue_json 列的往返：空串落为 '[]'，非空按原样写回与读出。
func TestSwarmLeadQueueJSONRoundTrip(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "swarm_queue_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	queueJSON := `[{"index":3,"projectId":"proj-1","leadWorkDir":"/work/q","spec":{"name":"Cedar","task":"排队中"}}]`
	t1 := time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)
	if err := s.SaveSwarmLead(ctx, SwarmLeadRecord{
		LeadThreadID: "lead-q1",
		Enabled:      true,
		ProjectID:    "proj-1",
		LeadWorkDir:  "/work/q",
		BeesJSON:     "[]",
		QueueJSON:    queueJSON,
		UpdatedAt:    t1,
	}); err != nil {
		t.Fatalf("SaveSwarmLead lead-q1 failed: %v", err)
	}

	// 不写 QueueJSON 时默认落 '[]'，重启后能直接按空队列解析。
	t2 := time.Date(2026, 2, 1, 11, 0, 0, 0, time.UTC)
	if err := s.SaveSwarmLead(ctx, SwarmLeadRecord{
		LeadThreadID: "lead-q2",
		Enabled:      true,
		ProjectID:    "proj-2",
		LeadWorkDir:  "/work/q2",
		BeesJSON:     "[]",
		UpdatedAt:    t2,
	}); err != nil {
		t.Fatalf("SaveSwarmLead lead-q2 failed: %v", err)
	}

	leads, err := s.ListSwarmLeads(ctx)
	if err != nil {
		t.Fatalf("ListSwarmLeads failed: %v", err)
	}
	byID := make(map[string]SwarmLeadRecord, len(leads))
	for _, rec := range leads {
		byID[rec.LeadThreadID] = rec
	}
	if got := byID["lead-q1"].QueueJSON; got != queueJSON {
		t.Fatalf("queue_json 往返不一致，期望 %s，实际 %s", queueJSON, got)
	}
	if got := byID["lead-q2"].QueueJSON; got != "[]" {
		t.Fatalf("空 queue_json 应落为 []，实际 %q", got)
	}

	// 覆盖更新时新队列值必须生效，不能残留旧值。
	if err := s.SaveSwarmLead(ctx, SwarmLeadRecord{
		LeadThreadID: "lead-q1",
		Enabled:      true,
		ProjectID:    "proj-1",
		LeadWorkDir:  "/work/q",
		BeesJSON:     "[]",
		UpdatedAt:    t2,
	}); err != nil {
		t.Fatalf("SaveSwarmLead update lead-q1 failed: %v", err)
	}
	leads, err = s.ListSwarmLeads(ctx)
	if err != nil {
		t.Fatalf("ListSwarmLeads after update failed: %v", err)
	}
	for _, rec := range leads {
		if rec.LeadThreadID == "lead-q1" && rec.QueueJSON != "[]" {
			t.Fatalf("覆盖更新后 queue_json 期望 []，实际 %q", rec.QueueJSON)
		}
	}
}

func TestSwarmLeadStore(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "swarm_test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	// 1. Initial list should be empty
	leads, err := s.ListSwarmLeads(ctx)
	if err != nil {
		t.Fatalf("ListSwarmLeads failed: %v", err)
	}
	if len(leads) != 0 {
		t.Fatalf("expected 0 leads, got %d", len(leads))
	}

	// 2. Save a lead with empty BeesJSON (should default to '[]')
	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	rec1 := SwarmLeadRecord{
		LeadThreadID: "lead-1",
		Enabled:      true,
		ProjectID:    "proj-1",
		LeadWorkDir:  "/work/1",
		NextIndex:    3,
		BeesJSON:     "",
		UpdatedAt:    t1,
	}
	if err := s.SaveSwarmLead(ctx, rec1); err != nil {
		t.Fatalf("SaveSwarmLead rec1 failed: %v", err)
	}

	leads, err = s.ListSwarmLeads(ctx)
	if err != nil {
		t.Fatalf("ListSwarmLeads after rec1 failed: %v", err)
	}
	if len(leads) != 1 {
		t.Fatalf("expected 1 lead, got %d", len(leads))
	}
	if leads[0].LeadThreadID != "lead-1" || !leads[0].Enabled || leads[0].ProjectID != "proj-1" ||
		leads[0].LeadWorkDir != "/work/1" || leads[0].NextIndex != 3 || leads[0].BeesJSON != "[]" {
		t.Fatalf("unexpected lead data: %+v", leads[0])
	}
	if !leads[0].UpdatedAt.Equal(t1) {
		t.Fatalf("expected UpdatedAt %v, got %v", t1, leads[0].UpdatedAt)
	}

	// 3. Upsert update existing lead
	t2 := time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC)
	rec1Updated := SwarmLeadRecord{
		LeadThreadID: "lead-1",
		Enabled:      false,
		ProjectID:    "proj-1-edit",
		LeadWorkDir:  "/work/1-edit",
		NextIndex:    5,
		BeesJSON:     `[{"name":"Aspen"}]`,
		UpdatedAt:    t2,
	}
	if err := s.SaveSwarmLead(ctx, rec1Updated); err != nil {
		t.Fatalf("SaveSwarmLead update failed: %v", err)
	}

	leads, err = s.ListSwarmLeads(ctx)
	if err != nil {
		t.Fatalf("ListSwarmLeads after update failed: %v", err)
	}
	if len(leads) != 1 {
		t.Fatalf("expected 1 lead, got %d", len(leads))
	}
	if leads[0].Enabled != false || leads[0].NextIndex != 5 || leads[0].BeesJSON != `[{"name":"Aspen"}]` {
		t.Fatalf("unexpected updated lead data: %+v", leads[0])
	}

	// 4. Save second lead
	t3 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	rec2 := SwarmLeadRecord{
		LeadThreadID: "lead-2",
		Enabled:      true,
		ProjectID:    "proj-2",
		LeadWorkDir:  "/work/2",
		NextIndex:    1,
		BeesJSON:     `[{"name":"Birch"}]`,
		UpdatedAt:    t3,
	}
	if err := s.SaveSwarmLead(ctx, rec2); err != nil {
		t.Fatalf("SaveSwarmLead rec2 failed: %v", err)
	}

	leads, err = s.ListSwarmLeads(ctx)
	if err != nil {
		t.Fatalf("ListSwarmLeads after rec2 failed: %v", err)
	}
	if len(leads) != 2 {
		t.Fatalf("expected 2 leads, got %d", len(leads))
	}
	// Ordered by updated_at DESC: rec2 first, then rec1
	if leads[0].LeadThreadID != "lead-2" || leads[1].LeadThreadID != "lead-1" {
		t.Fatalf("expected order [lead-2, lead-1], got [%s, %s]", leads[0].LeadThreadID, leads[1].LeadThreadID)
	}

	// 5. Delete lead-1
	if err := s.DeleteSwarmLead(ctx, "lead-1"); err != nil {
		t.Fatalf("DeleteSwarmLead failed: %v", err)
	}

	leads, err = s.ListSwarmLeads(ctx)
	if err != nil {
		t.Fatalf("ListSwarmLeads after delete failed: %v", err)
	}
	if len(leads) != 1 || leads[0].LeadThreadID != "lead-2" {
		t.Fatalf("expected only lead-2 left, got %+v", leads)
	}

	// 6. Delete nonexistent lead is a no-op
	if err := s.DeleteSwarmLead(ctx, "nonexistent"); err != nil {
		t.Fatalf("DeleteSwarmLead nonexistent failed: %v", err)
	}
}
