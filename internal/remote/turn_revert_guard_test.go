package remote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/git"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// prepareRevertTurn 为会话写入一个可撤销轮次：one.txt 先写 v1 并打 start 检查点，
// 再改成 v2 并打 end 检查点，撤销后 one.txt 应回到 v1。
func prepareRevertTurn(t *testing.T, backend *MemoryBackend, st *store.Store, runner *git.Runner, repoDir, threadID, turnID string) {
	t.Helper()
	ctx := context.Background()

	writeRepoFile(t, repoDir, "one.txt", "v1")
	start := checkpoint(t, runner, repoDir, threadID, turnID, "start")
	writeRepoFile(t, repoDir, "one.txt", "v2")
	end := checkpoint(t, runner, repoDir, threadID, turnID, "end")

	if err := st.CreateTurn(ctx, store.Turn{
		ID: turnID, ThreadID: threadID, TurnIndex: 0, Status: "completed", StartedAt: time.Now().UTC(),
		BaseCheckpoint: start, FinishCheckpoint: end, UserPrompt: "u1",
	}); err != nil {
		t.Fatalf("CreateTurn %s: %v", turnID, err)
	}
}

// markThreadRunning 把一个会话标记为运行中，模拟同一工作区里另一个会话正在进行。
func markThreadRunning(t *testing.T, backend *MemoryBackend, threadID, runTurnID string) {
	t.Helper()
	backend.mu.Lock()
	defer backend.mu.Unlock()
	td, ok := backend.threads[threadID]
	if !ok {
		t.Fatalf("thread %s not found", threadID)
	}
	td.runTurnID = runTurnID
	td.summary.IsRunning = true
}

// TestTurnRevertRejectsBusyWorkspace 验证同一仓库下另一个会话在运行时撤销被拒绝，且工作区文件未被改动。
func TestTurnRevertRejectsBusyWorkspace(t *testing.T) {
	backend, st, runner, repoDir := newRevertTestEnv(t)
	ctx := context.Background()

	res, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	thID := res.ThreadID
	const turnID = "guard-turn-busy"
	prepareRevertTurn(t, backend, st, runner, repoDir, thID, turnID)

	// 同一仓库（同一项目目录）下另一个会话正在运行
	other, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
	if err != nil {
		t.Fatalf("CreateThread other: %v", err)
	}
	markThreadRunning(t, backend, other.ThreadID, "other-running")

	_, err = backend.ExecuteTurnRevert(ctx, thID, turnID)
	if err == nil || !strings.Contains(err.Error(), "同一工作区") {
		t.Fatalf("ExecuteTurnRevert error = %v, want 同一工作区拦截", err)
	}
	if got := readRepoFile(t, repoDir, "one.txt"); got != "v2" {
		t.Fatalf("one.txt = %q, want 未被改动 %q", got, "v2")
	}
}

// TestTurnRevertAllowsOtherRepoRunning 验证别的仓库里有会话在运行时撤销不被拦截。
func TestTurnRevertAllowsOtherRepoRunning(t *testing.T) {
	backend, st, runner, repoDir := newRevertTestEnv(t)
	ctx := context.Background()

	res, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	thID := res.ThreadID
	const turnID = "guard-turn-other-repo"
	prepareRevertTurn(t, backend, st, runner, repoDir, thID, turnID)

	otherRepo := filepath.Join(t.TempDir(), "repo2")
	if err := os.MkdirAll(otherRepo, 0o755); err != nil {
		t.Fatalf("mkdir repo2: %v", err)
	}
	backend.AddProject(wire.ProjectSummary{ID: "p2", Name: "Project 2", Path: otherRepo})
	other, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p2"})
	if err != nil {
		t.Fatalf("CreateThread other: %v", err)
	}
	markThreadRunning(t, backend, other.ThreadID, "other-running")

	if _, err := backend.ExecuteTurnRevert(ctx, thID, turnID); err != nil {
		t.Fatalf("ExecuteTurnRevert: %v", err)
	}
	if got := readRepoFile(t, repoDir, "one.txt"); got != "v1" {
		t.Fatalf("one.txt = %q, want 撤销后 %q", got, "v1")
	}
}

// TestTurnRevertRejectsBusyHook 验证宿主注入的占用钩子返回原因时撤销被拒绝，且工作区文件未被改动。
func TestTurnRevertRejectsBusyHook(t *testing.T) {
	backend, st, runner, repoDir := newRevertTestEnv(t)
	ctx := context.Background()

	res, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	thID := res.ThreadID
	const turnID = "guard-turn-hook"
	prepareRevertTurn(t, backend, st, runner, repoDir, thID, turnID)

	const reason = "这个工作区还有子代理在运行，改动会合并回来"
	backend.SetRevertBusyCheck(func(threadID, root string) string {
		if threadID != thID {
			t.Errorf("钩子 threadID = %q, want %q", threadID, thID)
		}
		if !dirWithinRoot(repoDir, root) {
			t.Errorf("钩子 root = %q, want 指向 %q", root, repoDir)
		}
		return reason
	})

	_, err = backend.ExecuteTurnRevert(ctx, thID, turnID)
	if err == nil || !strings.Contains(err.Error(), reason) {
		t.Fatalf("ExecuteTurnRevert error = %v, want 钩子原因 %q", err, reason)
	}
	if got := readRepoFile(t, repoDir, "one.txt"); got != "v2" {
		t.Fatalf("one.txt = %q, want 未被改动 %q", got, "v2")
	}
}

// TestTurnRevertSucceedsWithoutBusy 验证没有钩子、没有其他会话在运行时撤销照常成功。
func TestTurnRevertSucceedsWithoutBusy(t *testing.T) {
	backend, st, runner, repoDir := newRevertTestEnv(t)
	ctx := context.Background()

	res, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	thID := res.ThreadID
	const turnID = "guard-turn-idle"
	prepareRevertTurn(t, backend, st, runner, repoDir, thID, turnID)

	if _, err := backend.ExecuteTurnRevert(ctx, thID, turnID); err != nil {
		t.Fatalf("ExecuteTurnRevert: %v", err)
	}
	if got := readRepoFile(t, repoDir, "one.txt"); got != "v1" {
		t.Fatalf("one.txt = %q, want 撤销后 %q", got, "v1")
	}
}
