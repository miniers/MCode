package app

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"mcode/internal/config"
)

func disableStewardForTest(t *testing.T, dataDir string) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Orchestration.Steward.Enabled = false
	cfg.Orchestration.Steward.EnabledChoice = true
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("写入关闭总管的配置失败: %v", err)
	}
}

func TestNotifyOwner(t *testing.T) {
	var a App
	if err := a.notifyOwner(context.Background(), "t", "b", "id"); err != errOwnerNotifierUnavailable {
		t.Fatalf("通知器未接入: %v", err)
	}
	var gotTitle, gotBody, gotID string
	a.SetOwnerNotifier(func(title, body, id string) error {
		gotTitle, gotBody, gotID = title, body, id
		return nil
	})
	if err := a.notifyOwner(context.Background(), "t", "b", "id"); err != nil || gotTitle != "t" || gotBody != "b" || gotID != "id" {
		t.Fatalf("通知转发失败: %q %q %q %v", gotTitle, gotBody, gotID, err)
	}
	a.SetOwnerNotifier(func(string, string, string) error { return fmt.Errorf("抑制: %w", ErrOwnerNotifySuppressed) })
	if err := a.notifyOwner(context.Background(), "t", "b", "id"); err == nil || !strings.Contains(err.Error(), "请直接在主聊天里告诉用户") {
		t.Fatalf("通知抑制结果: %v", err)
	}
	a.SetOwnerNotifier(nil)
	if err := a.notifyOwner(context.Background(), "t", "b", "id"); err != errOwnerNotifierUnavailable {
		t.Fatalf("清除通知器: %v", err)
	}
}
