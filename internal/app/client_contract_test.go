package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mcode/internal/config"
	"mcode/internal/testenv"
	mcodeui "mcode/internal/ui"
)

func TestClientModelAndSwarmConfigContract(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()
	dir := t.TempDir()
	client := &Client{dataDir: dir}
	first := mcodeui.ModelPin{Provider: "claude", ModelID: "first"}
	second := mcodeui.ModelPin{Provider: "codex", ModelID: "second"}
	for _, pin := range []mcodeui.ModelPin{first, second} {
		if added, err := client.AddModelToList(ctx, pin); err != nil || !added {
			t.Fatalf("add model: added=%v, err=%v", added, err)
		}
	}
	if err := client.ReorderModelList(ctx, []mcodeui.ModelPin{second, first, second}); err != nil {
		t.Fatal(err)
	}
	list, limit, err := client.ModelList(ctx)
	if err != nil || limit != 30 || len(list) != 2 || list[0] != second {
		t.Fatalf("model list: %v, limit=%d, err=%v", list, limit, err)
	}
	effort := "high"
	if err := client.UpdateModelPreference(ctx, first, mcodeui.ModelPreference{Effort: &effort, FastMode: true}); err != nil {
		t.Fatal(err)
	}
	pref, err := client.ModelPreferences(ctx, first)
	if err != nil || pref.Effort == nil || *pref.Effort != effort || !pref.FastMode {
		t.Fatalf("model preference: %+v, err=%v", pref, err)
	}
	if removed, err := client.RemoveModelFromList(ctx, second); err != nil || !removed {
		t.Fatalf("remove model: removed=%v, err=%v", removed, err)
	}
	pair, err := client.AddSwarmPair(ctx, mcodeui.SwarmPair{LeadProvider: "claude", WorkerProvider: "codex", Name: "initial"})
	if err != nil || pair.ID == "" {
		t.Fatalf("add pair: %+v, err=%v", pair, err)
	}
	pair.Name = "updated"
	if err := client.UpdateSwarmPair(ctx, pair); err != nil {
		t.Fatal(err)
	}
	pairs, err := client.SwarmPairs(ctx)
	if err != nil || len(pairs) != 1 || pairs[0].Name != "updated" {
		t.Fatalf("read pairs: %+v, err=%v", pairs, err)
	}
	cfg, err := config.Load(dir)
	if err != nil || len(cfg.Models.Pins) != 1 || !cfg.Models.ModelPreferenceFor(first).FastMode || len(cfg.Orchestration.Pairs) != 1 {
		t.Fatalf("config not persisted: %+v, err=%v", cfg, err)
	}
	if err := client.RemoveSwarmPair(ctx, pair.ID); err != nil {
		t.Fatal(err)
	}
	if err := client.UpdateSwarmPair(ctx, pair); err == nil {
		t.Fatal("updating a removed pair must fail")
	}
}

func TestClientConfigReadsPropagateErrors(t *testing.T) {
	testenv.TempHome(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, client := range []*Client{{}, {dataDir: dir}} {
		if _, _, err := client.ModelList(ctx); err == nil {
			t.Error("ModelList hid missing dataDir or invalid config")
		}
		if _, err := client.ModelPreferences(ctx, mcodeui.ModelPin{}); err == nil {
			t.Error("ModelPreferences hid missing dataDir or invalid config")
		}
		if _, err := client.SwarmPairs(ctx); err == nil {
			t.Error("SwarmPairs hid missing dataDir or invalid config")
		}
	}
}

func TestClientUnimplementedContract(t *testing.T) {
	testenv.TempHome(t)
	client := &Client{}
	ctx := context.Background()
	checks := map[string]func() error{
		"ProviderQuota": func() error { _, err := client.ProviderQuota(ctx, ""); return err },
	}
	for name, check := range checks {
		t.Run(name, func(t *testing.T) {
			if err := check(); !errors.Is(err, mcodeui.ErrNotImplemented) {
				t.Fatalf("expected ErrNotImplemented, got %v", err)
			}
		})
	}

	// PreviewRevert / ExecuteRevert / ProviderAccount 已接线：没有 backend、或 provider
	// 不提供账户信息时返回普通错误，而不再是 ErrNotImplemented。
	implemented := map[string]func() error{
		"PreviewRevert":   func() error { _, err := client.PreviewRevert(ctx, "", ""); return err },
		"ExecuteRevert":   func() error { return client.ExecuteRevert(ctx, "", "") },
		"ProviderAccount": func() error { _, err := client.ProviderAccount(ctx, ""); return err },
	}
	for name, check := range implemented {
		t.Run(name, func(t *testing.T) {
			if err := check(); err == nil || errors.Is(err, mcodeui.ErrNotImplemented) {
				t.Fatalf("expected a non-ErrNotImplemented error, got %v", err)
			}
		})
	}

	// TriggerAutoMerge 已接线：没有 Swarm 流水线时返回普通错误，而不再是 ErrNotImplemented。
	if err := client.TriggerAutoMerge(ctx, "lead"); err == nil || errors.Is(err, mcodeui.ErrNotImplemented) {
		t.Fatalf("expected a non-ErrNotImplemented error without a pipeline, got %v", err)
	}
}
