package remote

import (
	"context"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/kanban"
	"mcode/internal/remote/wire"
)

// StartSession 的 RuntimeMode 指定：非空时以该权限模式创建会话；
// 为空时保持原有行为，沿用后端默认权限模式。
func TestStartSession_RuntimeModeOverride(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()

	b.SetHarness(&mockHarness{kind: "claude"}, t.TempDir(), harness.ModeFullAccess)
	b.AddProject(wire.ProjectSummary{ID: "p1", Path: t.TempDir()})

	// 1. 不指定 RuntimeMode：沿用后端默认权限模式（与改动前一致）
	defID, err := b.StartSession(ctx, kanban.SessionRequest{
		ProjectID: "p1", WorkDir: t.TempDir(), Prompt: "执行任务",
	})
	if err != nil {
		t.Fatal(err)
	}
	def := waitThreadIdle(t, b, ctx, defID)
	if def.RuntimeMode != string(harness.ModeFullAccess) {
		t.Fatalf("未指定 RuntimeMode 时应沿用后端默认权限模式: %q", def.RuntimeMode)
	}

	// 2. 指定 RuntimeMode=auto：新会话以 auto 权限模式创建
	autoID, err := b.StartSession(ctx, kanban.SessionRequest{
		ProjectID: "p1", WorkDir: t.TempDir(), Prompt: "执行任务", RuntimeMode: "auto",
	})
	if err != nil {
		t.Fatal(err)
	}
	auto := waitThreadIdle(t, b, ctx, autoID)
	if auto.RuntimeMode != "auto" {
		t.Fatalf("RuntimeMode 未生效: %q", auto.RuntimeMode)
	}

	// 3. 非法取值沿用 createThread 既有校验，直接返回错误
	if _, err := b.StartSession(ctx, kanban.SessionRequest{
		ProjectID: "p1", WorkDir: t.TempDir(), Prompt: "执行任务", RuntimeMode: "bogus",
	}); err == nil {
		t.Fatalf("非法 RuntimeMode 应返回错误")
	}
}
