package app

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/providers/agy/accounts"
	"mcode/internal/providers/agy/quota"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	"mcode/internal/testenv"
)

func TestClientProviderQuota_UsesAccountSnapshots(t *testing.T) {
	testenv.TempHome(t)
	dir := t.TempDir()
	reset := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	reset7d := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	index := accounts.Index{Version: 1, Accounts: []accounts.Account{{
		ID: "account", Label: "测试账号", Quota: map[string]accounts.QuotaGroup{
			quota.GroupGemini:         {UsedPercent: 62, ResetAt: reset.Format(time.RFC3339)},
			quota.GroupGemini + " 7d": {UsedPercent: 35, ResetAt: reset7d.Format(time.RFC3339)},
			quota.GroupClaudeGPT:      {UsedPercent: 10},
			"gemini-3.8-flash-high":   {UsedPercent: 5},
		},
	}}}
	data, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agy-accounts.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := accounts.NewManager(accounts.Options{DataDir: dir, HostHome: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{agy: manager}
	windows, err := client.ProviderQuota(context.Background(), "agy")
	if err != nil || len(windows) != 2 {
		t.Fatalf("quota windows: %+v, %v", windows, err)
	}
	if windows[0].Label != "测试账号 · "+quota.GroupGemini || windows[0].UsedPercent != 62 || !windows[0].ResetsAt.Equal(reset) {
		t.Fatalf("wrong quota mapping or order: %+v", windows)
	}
	if windows[1].Label != "测试账号 · "+quota.GroupGemini+" 7d" || windows[1].UsedPercent != 35 || !windows[1].ResetsAt.Equal(reset7d) {
		t.Fatalf("wrong quota mapping or order: %+v", windows)
	}
	for _, w := range windows {
		if strings.Contains(w.Label, quota.GroupClaudeGPT) || strings.Contains(w.ID, quota.GroupClaudeGPT) {
			t.Fatalf("Claude/GPT quota must not be shown: %+v", w)
		}
		if strings.Contains(w.Label, "gemini-3.8-flash-high") {
			t.Fatalf("per-model quota must not be shown: %+v", w)
		}
	}
}

func TestClientRecallTurn_DelegatesToBackend(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()
	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	defer hub.Close()
	backend := remote.NewMemoryBackend(hub)
	defer backend.Close()
	for _, client := range []*Client{{}, {backend: backend}} {
		if _, err := client.RecallTurn(ctx, "missing"); err == nil {
			t.Fatal("missing backend or thread must not report a successful take back")
		}
	}
}

func TestClientQueuedAttachments_LoadAndRemove(t *testing.T) {
	testenv.TempHome(t)
	ctx := context.Background()
	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	defer hub.Close()
	backend := remote.NewMemoryBackend(hub)
	defer backend.Close()
	backend.AddThread(wire.ThreadSummary{ID: "t", IsRunning: true}, nil)
	client := &Client{backend: backend}
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	att, err := client.UploadAttachment(ctx, "image.png", "image/png", data.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Enqueue(ctx, "t", "queued", []string{att.ID}); err != nil {
		t.Fatal(err)
	}
	queue, err := client.Queue(ctx, "t")
	if err != nil || len(queue) != 1 {
		t.Fatalf("queue: %+v, %v", queue, err)
	}
	attachments, err := client.QueuedAttachments(ctx, "t", queue[0].ID)
	if err != nil || len(attachments) != 1 || attachments[0].ID != att.ID || attachments[0].ByteCount != data.Len() {
		t.Fatalf("queued attachment metadata: %+v, %v", attachments, err)
	}
	if err := client.UpdateQueuedWithAttachments(ctx, "t", queue[0].ID, "updated", nil); err != nil {
		t.Fatal(err)
	}
	attachments, err = client.QueuedAttachments(ctx, "t", queue[0].ID)
	if err != nil || len(attachments) != 0 {
		t.Fatalf("attachments were not cleared: %+v, %v", attachments, err)
	}
}
