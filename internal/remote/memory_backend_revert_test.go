package remote

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/git"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// newRevertTestEnv 构造撤销轮次测试所需的临时环境：git 仓库、store、带 git runner 的 MemoryBackend。
func newRevertTestEnv(t *testing.T) (*MemoryBackend, *store.Store, *git.Runner, string) {
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

	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	t.Cleanup(hub.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "revert.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	if err := backend.AttachStore(context.Background(), st); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}
	backend.SetGitRunner(runner)
	backend.AddProject(wire.ProjectSummary{ID: "p1", Name: "Project 1", Path: repoDir})

	return backend, st, runner, repoDir
}

func writeRepoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func readRepoFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func checkpoint(t *testing.T, runner *git.Runner, root, threadID, turnID, kind string) string {
	t.Helper()
	ref := git.CheckpointRef(threadID, turnID) + "/" + kind
	commit, err := runner.Checkpoint(context.Background(), root, ref, "turn "+turnID+" "+kind)
	if err != nil {
		t.Fatalf("checkpoint %s: %v", ref, err)
	}
	return commit
}

// TestMemoryBackend_ExecuteTurnRevert 验证撤销执行后：该轮改动被还原、被二次修改的文件保留、
// 该轮及其之后的轮次全部撤回。
func TestMemoryBackend_ExecuteTurnRevert(t *testing.T) {
	backend, st, runner, repoDir := newRevertTestEnv(t)
	ctx := context.Background()

	res, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	thID := res.ThreadID

	const turn1 = "revert-turn-1"
	const turn2 = "revert-turn-2"

	// 第 1 轮：先写入 one.txt=v1，轮内改为 v2
	writeRepoFile(t, repoDir, "one.txt", "v1")
	start1 := checkpoint(t, runner, repoDir, thID, turn1, "start")
	writeRepoFile(t, repoDir, "one.txt", "v2")
	end1 := checkpoint(t, runner, repoDir, thID, turn1, "end")

	// 第 2 轮：one.txt 改为 v3，并新增 two.txt=v1
	start2 := checkpoint(t, runner, repoDir, thID, turn2, "start")
	writeRepoFile(t, repoDir, "one.txt", "v3")
	writeRepoFile(t, repoDir, "two.txt", "v1")
	end2 := checkpoint(t, runner, repoDir, thID, turn2, "end")

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

	// 用户在最后一轮结束后又改了 two.txt，属于二次修改，应保留
	writeRepoFile(t, repoDir, "two.txt", "user-change")

	preview, err := backend.PreviewTurnRevert(ctx, thID, turn1)
	if err != nil {
		t.Fatalf("PreviewTurnRevert: %v", err)
	}
	byPath := make(map[string]git.RestoreFile, len(preview.Files))
	for _, f := range preview.Files {
		byPath[f.Path] = f
	}
	if f, ok := byPath["two.txt"]; !ok || !f.IsKept {
		t.Fatalf("expected two.txt kept in preview, got %+v", byPath)
	}
	if f, ok := byPath["one.txt"]; !ok || f.IsKept {
		t.Fatalf("expected one.txt restorable in preview, got %+v", byPath)
	}

	if _, err := backend.ExecuteTurnRevert(ctx, thID, turn1); err != nil {
		t.Fatalf("ExecuteTurnRevert: %v", err)
	}

	if got := readRepoFile(t, repoDir, "one.txt"); got != "v1" {
		t.Fatalf("one.txt = %q, want restored %q", got, "v1")
	}
	if got := readRepoFile(t, repoDir, "two.txt"); got != "user-change" {
		t.Fatalf("two.txt = %q, want kept %q", got, "user-change")
	}

	// store 中该轮及其之后的轮次均被撤回（保留审计行，状态为 taken_back）
	for _, id := range []string{turn1, turn2} {
		tu, err := st.GetTurn(ctx, id)
		if err != nil {
			t.Fatalf("GetTurn %s: %v", id, err)
		}
		if tu.Status != "taken_back" {
			t.Fatalf("turn %s status = %q, want taken_back", id, tu.Status)
		}
	}
}

// TestMemoryBackend_TurnRevertRefFallback 验证 store 中缺少检查点字段时回退到 start/end ref 解析。
func TestMemoryBackend_TurnRevertRefFallback(t *testing.T) {
	backend, st, runner, repoDir := newRevertTestEnv(t)
	ctx := context.Background()

	res, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	thID := res.ThreadID

	const turnID = "revert-ref-turn"
	writeRepoFile(t, repoDir, "one.txt", "v1")
	checkpoint(t, runner, repoDir, thID, turnID, "start")
	writeRepoFile(t, repoDir, "one.txt", "v2")
	checkpoint(t, runner, repoDir, thID, turnID, "end")

	// 轮次记录不含 BaseCheckpoint / FinishCheckpoint，只能通过 ref 回退解析
	if err := st.CreateTurn(ctx, store.Turn{
		ID: turnID, ThreadID: thID, TurnIndex: 0, Status: "completed", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateTurn: %v", err)
	}

	if _, err := backend.PreviewTurnRevert(ctx, thID, turnID); err != nil {
		t.Fatalf("PreviewTurnRevert: %v", err)
	}
	if _, err := backend.ExecuteTurnRevert(ctx, thID, turnID); err != nil {
		t.Fatalf("ExecuteTurnRevert: %v", err)
	}
	if got := readRepoFile(t, repoDir, "one.txt"); got != "v1" {
		t.Fatalf("one.txt = %q, want restored %q", got, "v1")
	}
}

// TestMemoryBackend_TurnRevertRejectsRunning 验证会话正在运行时拒绝预览与执行撤销。
func TestMemoryBackend_TurnRevertRejectsRunning(t *testing.T) {
	backend, _, _, _ := newRevertTestEnv(t)
	ctx := context.Background()

	res, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "p1"})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	thID := res.ThreadID

	backend.mu.Lock()
	backend.threads[thID].runTurnID = "running-turn"
	backend.threads[thID].summary.IsRunning = true
	backend.mu.Unlock()

	_, err = backend.PreviewTurnRevert(ctx, thID, "running-turn")
	if err == nil || !strings.Contains(err.Error(), "会话正在运行") {
		t.Fatalf("PreviewTurnRevert error = %v, want running guard", err)
	}
	_, err = backend.ExecuteTurnRevert(ctx, thID, "running-turn")
	if err == nil || !strings.Contains(err.Error(), "会话正在运行") {
		t.Fatalf("ExecuteTurnRevert error = %v, want running guard", err)
	}
}
