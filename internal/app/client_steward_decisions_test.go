package app

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"mcode/internal/remote"
	"mcode/internal/store"
)

func TestStewardOpenDecisions(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	t.Cleanup(hub.Close)
	backend := remote.NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	if err := backend.AttachStore(ctx, db); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	d1, err := db.CreateStewardDecision(ctx, store.StewardDecision{
		TaskID:      "task-1",
		ThreadID:    "steward-feed",
		Question:    "是否继续发布？",
		Context:     "单测已通过",
		OptionsJSON: `["继续发布","暂缓"]`,
		Recommended: "继续发布",
		Status:      store.DecisionStatusOpen,
		CreatedAt:   now.Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	d2, err := db.CreateStewardDecision(ctx, store.StewardDecision{
		ThreadID:    "steward-feed",
		Question:    "选项损坏容错",
		OptionsJSON: `invalid-json`,
		Status:      store.DecisionStatusOpen,
		CreatedAt:   now,
	})
	if err != nil {
		t.Fatal(err)
	}
	answeredAt := now.Add(time.Second)
	if _, err := db.CreateStewardDecision(ctx, store.StewardDecision{
		ThreadID:    "steward-feed",
		Question:    "已回答的问题",
		OptionsJSON: `["甲","乙"]`,
		Status:      store.DecisionStatusAnswered,
		Answer:      "甲",
		AnsweredAt:  &answeredAt,
		CreatedAt:   now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	items, err := (&Client{backend: backend}).StewardOpenDecisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("未答决策卡数量 = %d，期望 2: %+v", len(items), items)
	}
	if items[0].ID != d2.ID || len(items[0].Options) != 0 {
		t.Fatalf("首条决策卡不符: %+v", items[0])
	}
	if items[1].ID != d1.ID || items[1].TaskID != "task-1" || items[1].ThreadID != "steward-feed" ||
		items[1].Question != "是否继续发布？" || items[1].Context != "单测已通过" ||
		items[1].Recommended != "继续发布" || items[1].Status != store.DecisionStatusOpen ||
		!reflect.DeepEqual(items[1].Options, []string{"继续发布", "暂缓"}) {
		t.Fatalf("第二条决策卡不符: %+v", items[1])
	}

	empty, err := (&Client{}).StewardOpenDecisions(ctx)
	if err != nil || len(empty) != 0 {
		t.Fatalf("未挂载存储时应返回空切片，得到 %+v err=%v", empty, err)
	}
}
