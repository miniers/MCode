package app

import (
	"context"
	"path/filepath"
	"strings"
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
	label := cfg.Orchestration.Steward.Label()
	if got := frontdeskHandoffSection(dir, "ordinary"); got == "" {
		t.Fatal("普通会话应有提示")
	} else if !strings.Contains(got, "「打开"+label+"」") || !strings.Contains(got, "交给「"+label+"」") {
		t.Fatalf("提示应引导按 ⇧⌘T 或命令面板打开总管并移交，实际: %q", got)
	} else if !strings.Contains(got, "要设成定时任务") {
		t.Fatalf("提示应说明定时任务: %q", got)
	}
}
