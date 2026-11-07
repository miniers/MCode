package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/git"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
	mcodeui "mcode/internal/ui"
)

// newClientRevertEnv 构造撤销轮次测试所需的临时环境：git 仓库、store、带 git runner 的
// MemoryBackend，并把它装进 Client 的 backend 字段（对照
// internal/remote/memory_backend_revert_test.go 的 newRevertTestEnv）。
func newClientRevertEnv(t *testing.T) (*Client, *store.Store, *git.Runner, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git 可执行文件在 PATH 中未找到，跳过测试")
	}

	repoDir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	if out, err := exec.Command(gitBin, "init", repoDir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	runner := git.NewRunner(gitBin, 4)

	hub := remote.NewStreamHub(remote.StreamHubOptions{Interval: time.Hour})
	t.Cleanup(hub.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "revert.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	backend := remote.NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	if err := backend.AttachStore(context.Background(), st); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}
	backend.SetGitRunner(runner)
	backend.AddProject(wire.ProjectSummary{ID: "p1", Name: "Project 1", Path: repoDir})

	return NewClient(backend), st, runner, repoDir
}

func writeRevertRepoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func readRevertRepoFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func revertCheckpoint(t *testing.T, runner *git.Runner, root, threadID, turnID, kind string) string {
	t.Helper()
	ref := git.CheckpointRef(threadID, turnID) + "/" + kind
	commit, err := runner.Checkpoint(context.Background(), root, ref, "turn "+turnID+" "+kind)
	if err != nil {
		t.Fatalf("checkpoint %s: %v", ref, err)
	}
	return commit
}

// TestClient_PreviewAndExecuteRevert 验证 client 层的撤销预览与执行：
// 预览带 IsKept 与 Conflicts，执行后该轮改动被还原、被二次修改的文件保留。
func TestClient_PreviewAndExecuteRevert(t *testing.T) {
	client, st, runner, repoDir := newClientRevertEnv(t)
	ctx := context.Background()

	res, err := client.backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	thID := res.ThreadID

	const turn1 = "client-revert-turn-1"
	const turn2 = "client-revert-turn-2"

	// 第 1 轮：one.txt v1 → v2
	writeRevertRepoFile(t, repoDir, "one.txt", "v1")
	start1 := revertCheckpoint(t, runner, repoDir, thID, turn1, "start")
	writeRevertRepoFile(t, repoDir, "one.txt", "v2")
	end1 := revertCheckpoint(t, runner, repoDir, thID, turn1, "end")

	// 第 2 轮：one.txt 改为 v3，并新增 two.txt
	start2 := revertCheckpoint(t, runner, repoDir, thID, turn2, "start")
	writeRevertRepoFile(t, repoDir, "one.txt", "v3")
	writeRevertRepoFile(t, repoDir, "two.txt", "v1")
	end2 := revertCheckpoint(t, runner, repoDir, thID, turn2, "end")

	now := time.Now().UTC()
	if err := st.CreateTurn(ctx, store.Turn{
		ID: turn1, ThreadID: thID, TurnIndex: 0, Status: "completed", StartedAt: now,
		BaseCheckpoint: start1, FinishCheckpoint: end1, UserPrompt: "u1",
	}); err != nil {
		t.Fatalf("CreateTurn turn1: %v", err)
	}
	if err := st.CreateTurn(ctx, store.Turn{
		ID: turn2, ThreadID: thID, TurnIndex: 1, Status: "completed", StartedAt: now,
		BaseCheckpoint: start2, FinishCheckpoint: end2, UserPrompt: "u2",
	}); err != nil {
		t.Fatalf("CreateTurn turn2: %v", err)
	}

	// 用户在最后一轮结束后又改了 two.txt，属于二次修改，预览应标记 IsKept。
	writeRevertRepoFile(t, repoDir, "two.txt", "user-change")

	preview, err := client.PreviewRevert(ctx, thID, turn1)
	if err != nil {
		t.Fatalf("PreviewRevert: %v", err)
	}
	if preview.TurnID != turn1 {
		t.Fatalf("TurnID = %q，期望 %q", preview.TurnID, turn1)
	}
	byPath := make(map[string]mcodeui.RevertFile, len(preview.Files))
	for _, f := range preview.Files {
		byPath[f.Path] = f
	}
	if f, ok := byPath["one.txt"]; !ok || f.IsKept {
		t.Fatalf("one.txt 应可还原，得到 %+v", byPath)
	}
	if f, ok := byPath["two.txt"]; !ok || !f.IsKept {
		t.Fatalf("two.txt 应标记 IsKept，得到 %+v", byPath)
	}
	foundConflict := false
	for _, path := range preview.Conflicts {
		if path == "two.txt" {
			foundConflict = true
		}
	}
	if !foundConflict {
		t.Fatalf("Conflicts 应包含 two.txt，得到 %v", preview.Conflicts)
	}

	if err := client.ExecuteRevert(ctx, thID, turn1); err != nil {
		t.Fatalf("ExecuteRevert: %v", err)
	}
	if got := readRevertRepoFile(t, repoDir, "one.txt"); got != "v1" {
		t.Fatalf("one.txt = %q，期望还原为 %q", got, "v1")
	}
	if got := readRevertRepoFile(t, repoDir, "two.txt"); got != "user-change" {
		t.Fatalf("two.txt = %q，期望保留 %q", got, "user-change")
	}

	// 该轮及其之后的轮次在 store 中被撤回（保留审计行，状态为 taken_back）。
	for _, id := range []string{turn1, turn2} {
		tu, err := st.GetTurn(ctx, id)
		if err != nil {
			t.Fatalf("GetTurn %s: %v", id, err)
		}
		if tu.Status != "taken_back" {
			t.Fatalf("turn %s status = %q，期望 taken_back", id, tu.Status)
		}
	}
}

// TestClient_RevertWithoutBackend 验证 backend 缺失时返回普通错误，而不是 ErrNotImplemented。
func TestClient_RevertWithoutBackend(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := &Client{}
	ctx := context.Background()

	if _, err := client.PreviewRevert(ctx, "thread", "turn"); err == nil || errors.Is(err, mcodeui.ErrNotImplemented) {
		t.Fatalf("PreviewRevert 期望非 ErrNotImplemented 的错误，得到 %v", err)
	}
	if err := client.ExecuteRevert(ctx, "thread", "turn"); err == nil || errors.Is(err, mcodeui.ErrNotImplemented) {
		t.Fatalf("ExecuteRevert 期望非 ErrNotImplemented 的错误，得到 %v", err)
	}
}
