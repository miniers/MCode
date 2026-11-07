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

// PromoteReadOnlySession 只把只读档会话升到 auto；其他档位是用户选过的，保持不变。
func TestPromoteReadOnlySession(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()

	b.SetHarness(&mockHarness{kind: "claude"}, t.TempDir(), harness.ModeFullAccess)
	b.AddProject(wire.ProjectSummary{ID: "p1", Path: t.TempDir()})

	startWith := func(mode harness.PermissionMode) string {
		t.Helper()
		id, err := b.StartSession(ctx, kanban.SessionRequest{
			ProjectID: "p1", WorkDir: t.TempDir(), Prompt: "话题任务", RuntimeMode: string(mode),
		})
		if err != nil {
			t.Fatalf("StartSession 失败: %v", err)
		}
		th := waitThreadIdle(t, b, ctx, id)
		if th.RuntimeMode != string(mode) {
			t.Fatalf("会话初始权限档应为 %q，实际: %q", mode, th.RuntimeMode)
		}
		return id
	}

	roID := startWith(harness.ModeReadOnly)
	if err := b.PromoteReadOnlySession(ctx, roID); err != nil {
		t.Fatalf("升档失败: %v", err)
	}
	promoted, err := b.GetThread(ctx, roID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if promoted.RuntimeMode != string(harness.ModeAuto) {
		t.Fatalf("只读档应升为 auto，实际: %q", promoted.RuntimeMode)
	}
	// 重复调用是安全的：已经不是只读档时不再改动
	if err := b.PromoteReadOnlySession(ctx, roID); err != nil {
		t.Fatalf("重复升档应返回 nil: %v", err)
	}
	again, err := b.GetThread(ctx, roID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if again.RuntimeMode != string(harness.ModeAuto) {
		t.Fatalf("重复升档后仍应为 auto，实际: %q", again.RuntimeMode)
	}

	// 其他档位是用户选过的，不能被改动
	for _, mode := range []string{"supervised", "autoAcceptEdits", "auto", "fullAccess"} {
		id := startWith(harness.PermissionMode(mode))
		if err := b.PromoteReadOnlySession(ctx, id); err != nil {
			t.Fatalf("升档 %q 会话不应报错: %v", mode, err)
		}
		th, err := b.GetThread(ctx, id)
		if err != nil {
			t.Fatalf("读取会话失败: %v", err)
		}
		if th.RuntimeMode != mode {
			t.Fatalf("%q 会话不应被改动，实际: %q", mode, th.RuntimeMode)
		}
	}
}
