package omp

import "testing"

func TestParseModels(t *testing.T) {
	raw := []byte(`{"models":[
		{"id":"gpt-5","name":"GPT-5","provider":"openai","thinking":["low","medium","high"]},
		{"id":"claude-x","name":"Claude X","provider":"anthropic","thinking":["high","max"]},
		{"id":"plain","provider":"anthropic"},
		{"id":"","provider":"openai"}
	]}`)
	models, err := ParseModels(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 {
		t.Fatalf("want 3 models, got %+v", models)
	}
	// 按 provider、名字排序。
	if models[0].ID != "anthropic/Claude X" && models[0].ID != "anthropic/claude-x" {
		t.Fatalf("unexpected first model: %+v", models[0])
	}
	var gpt, claude Model
	for _, m := range models {
		switch m.ID {
		case "openai/gpt-5":
			gpt = m
		case "anthropic/claude-x":
			claude = m
		}
	}
	if gpt.DefaultEffort != "medium" || len(gpt.Efforts) != 3 {
		t.Fatalf("gpt-5 efforts wrong: %+v", gpt)
	}
	if claude.DefaultEffort != "high" {
		t.Fatalf("model without medium should default to its first effort: %+v", claude)
	}
}
