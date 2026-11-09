package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"mcode/internal/kanban"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

func TestFrontdeskAnswerDecisionAppendFailure(t *testing.T) {
	ctx := context.Background()
	db, _, svc := newFrontdeskAdapterFixture(t)
	if err := db.SetSetting(ctx, "steward.feed_thread_id", "missing"); err != nil {
		t.Fatal(err)
	}
	decision, err := db.CreateStewardDecision(ctx, store.StewardDecision{Question: "选择？", OptionsJSON: `[]`})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AnswerDecision(ctx, decision.ID, "甲", "owner"); err != nil {
		t.Fatal(err)
	}
	after, err := db.GetStewardDecision(ctx, decision.ID)
	if err != nil || after.Status != store.DecisionStatusAnswered || after.Answer != "甲" {
		t.Fatal(after, err)
	}
}

func TestFrontdeskRecentIncludesSystemResults(t *testing.T) {
	ctx := context.Background()
	_, backend, svc := newFrontdeskAdapterFixture(t)
	feed, err := backend.EnsureStewardFeedThread(ctx, "home")
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []struct{ kind, text string }{
		{"steward_result", "检查任务\n完成"},
		{"steward_decision_answer", `{"id":"D-0001","answer":"选甲"}`},
		{"other", "不应出现"},
	} {
		if err := backend.AppendStewardMessage(ctx, feed, "system", msg.kind, msg.text); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := svc.Recent(ctx, feed, 10)
	if err != nil || len(messages) != 2 {
		t.Fatal(messages, err)
	}
	if messages[0].Content[0].Text != "任务结果：检查任务 完成" || messages[1].Content[0].Text != "决策 D-0001 已答：选甲" {
		t.Fatal(messages)
	}
}

func TestFrontdeskAnswerDecisionAppendsMessages(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	defer hub.Close()
	backend := remote.NewMemoryBackend(hub)
	defer backend.Close()
	if err := backend.AttachStore(ctx, db); err != nil {
		t.Fatal(err)
	}
	backend.AddProject(wire.ProjectSummary{ID: "home", Name: "总管", Path: filepath.Join(dir, "home")})
	feedID, err := backend.EnsureStewardFeedThread(ctx, "home")
	if err != nil {
		t.Fatal(err)
	}
	decision, err := db.CreateStewardDecision(ctx, store.StewardDecision{Question: "怎么做？", OptionsJSON: `[]`})
	if err != nil {
		t.Fatal(err)
	}
	svc := &frontdeskServices{store: db, backend: backend, kanban: kanban.NewService(kanban.Options{Store: db})}
	for i, entry := range []struct{ answer, by string }{{"甲", "owner"}, {"乙", "remote"}} {
		if err := svc.AnswerDecision(ctx, decision.ID, entry.answer, entry.by); err != nil {
			t.Fatal(err)
		}
		messages, _, err := db.ListMessages(ctx, feedID, 0, 10)
		if err != nil || len(messages) != i+1 {
			t.Fatalf("消息数量错误：%d，错误 %v", len(messages), err)
		}
		message := messages[i]
		var payload struct {
			ID     string `json:"id"`
			Answer string `json:"answer"`
			By     string `json:"by"`
		}
		if err := json.Unmarshal([]byte(message.Text), &payload); err != nil || message.Role != "system" || message.Kind != "steward_decision_answer" || payload.ID != decision.ID || payload.Answer != entry.answer || payload.By != entry.by {
			t.Fatalf("答卡消息错误：%+v，内容 %+v，错误 %v", message, payload, err)
		}
	}
}
