package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/config"
	"mcode/internal/remote"
	"mcode/internal/store"
)

// TestClientOpenStewardThreadNotWired 覆盖未接线时报错，而不是静默返回空 ID。
func TestClientOpenStewardThreadNotWired(t *testing.T) {
	client := &Client{}
	id, err := client.OpenStewardThread(context.Background())
	if err == nil {
		t.Fatalf("未接线时应报错，实际返回 %q", id)
	}
	if id != "" {
		t.Fatalf("未接线时应返回空串，实际 %q", id)
	}
}

func TestClientOpenStewardThreadEnsures(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Models.Provider = "claude"
	cfg.Orchestration.Steward.Enabled = true
	cfg.Orchestration.Steward.HomeDir = filepath.Join(dir, "home")
	if err := config.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
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
	client := &Client{dataDir: dir, backend: backend}
	id, err := client.OpenStewardThread(ctx)
	if err != nil || id == "" {
		t.Fatalf("创建会话失败: %q %v", id, err)
	}
	stored, _, err := db.GetSetting(ctx, "steward.feed_thread_id")
	if err != nil || stored != id {
		t.Fatalf("会话设置错误: %q %v", stored, err)
	}
	if got := client.StewardThreadID(); got != id {
		t.Fatalf("会话 ID = %q，期望 %q", got, id)
	}
	if _, err := db.GetThread(ctx, id); err != nil {
		t.Fatal(err)
	}
	again, err := client.OpenStewardThread(ctx)
	if err != nil || again != id {
		t.Fatalf("重复打开结果错误: %q %v", again, err)
	}
}

// TestClientOpenStewardThreadDisabled 覆盖未启用：Ensure 返回空串且不报错时给出提示性错误。
func TestClientOpenStewardThreadDisabled(t *testing.T) {
	client := &Client{}
	id, err := client.OpenStewardThread(context.Background())
	if err == nil {
		t.Fatalf("未启用时应报错，实际返回 %q", id)
	}
	if !strings.Contains(err.Error(), "未启用") {
		t.Fatalf("错误应提示未启用，实际 %q", err.Error())
	}
}

func TestClientOpenStewardThreadEnsureError(t *testing.T) {
	client := newRemoteSettingsTestClient(t, func(cfg *config.Config) {
		cfg.Orchestration.Steward.Enabled = true
	})
	id, err := client.OpenStewardThread(context.Background())
	if err == nil || id != "" || !strings.Contains(err.Error(), "会话后端未接入") {
		t.Fatalf("缺少后端时应报错: %q %v", id, err)
	}
}

// TestClientStewardIdentity 覆盖总管的昵称与头像读取：读不到配置时两者都为空（昵称空由界面显示「总管」），
// 读到配置时返回昵称原文与头像。
func TestClientStewardIdentity(t *testing.T) {
	if name, avatar := (&Client{}).StewardIdentity(); name != "" || avatar != "" {
		t.Fatalf("读不到配置时应返回空昵称与空头像，实际 name=%q avatar=%q", name, avatar)
	}

	dataDir := t.TempDir()
	cfg := config.Defaults()
	cfg.Orchestration.Steward.Name = " 小助 "
	cfg.Orchestration.Steward.Avatar = "🤖"
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	name, avatar := (&Client{dataDir: dataDir}).StewardIdentity()
	if name != "小助" || avatar != "🤖" {
		t.Fatalf("应返回配置里的昵称与头像，实际 name=%q avatar=%q", name, avatar)
	}

	// 昵称留空时返回空串，界面才能显示兜底名「总管」；头像留空同理交给界面处理。
	blank := config.Defaults()
	blank.Orchestration.Steward.Name = "   "
	blank.Orchestration.Steward.Avatar = ""
	if err := config.Save(dataDir, blank); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	name, avatar = (&Client{dataDir: dataDir}).StewardIdentity()
	if name != "" || avatar != "" {
		t.Fatalf("昵称留空时应返回空昵称与空头像，实际 name=%q avatar=%q", name, avatar)
	}
}
