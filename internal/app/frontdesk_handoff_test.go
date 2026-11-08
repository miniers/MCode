package app

import (
	"context"
	"path/filepath"
	"testing"

	"mcode/internal/config"
	"mcode/internal/store"
)

func TestFrontdeskHandoffSection(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Orchestration.Steward.Enabled = false
	cfg.Orchestration.Steward.EnabledChoice = true
	if err := config.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	if got := frontdeskHandoffSection(dir, "ordinary"); got != "" {
		t.Fatalf("未启用时不应提示: %q", got)
	}
	cfg.Orchestration.Steward.Enabled = true
	if err := config.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "mcode.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(context.Background(), "steward.feed_thread_id", "feed"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if got := frontdeskHandoffSection(dir, "feed"); got != "" {
		t.Fatalf("前台会话不应提示: %q", got)
	}
	if got := frontdeskHandoffSection(dir, "ordinary"); got == "" {
		t.Fatal("普通会话应有提示")
	}
}
