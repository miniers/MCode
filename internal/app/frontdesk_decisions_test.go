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
	for i, answer := range []string{"甲", "乙"} {
		if err := svc.AnswerDecision(ctx, decision.ID, answer, "owner"); err != nil {
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
		}
		if err := json.Unmarshal([]byte(message.Text), &payload); err != nil || message.Role != "system" || message.Kind != "steward_decision_answer" || payload.ID != decision.ID || payload.Answer != answer {
			t.Fatalf("答卡消息错误：%+v，内容 %+v，错误 %v", message, payload, err)
		}
	}
}
