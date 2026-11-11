package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/git"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	mcodeui "mcode/internal/ui"
)

func TestReviewRevertHunkConversion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := (&Client{}).ReviewRevertHunk(ctx, mcodeui.ReviewRevertHunkParams{}); err == nil {
		t.Fatal("缺少 backend 未报错")
	}
	root := t.TempDir()
	command := func(args ...string) {
		t.Helper()
		gitCtx, gitCancel := context.WithTimeout(ctx, 10*time.Second)
		defer gitCancel()
		cmd := exec.CommandContext(gitCtx, "git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	command("init")
	command("config", "user.name", "review")
	command("config", "user.email", "review@localhost")
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	const old, next = "one\ntwo\nthree\n", "one\nTWO\nthree\n"
	write(old)
	command("add", "--", "a.txt")
	command("commit", "-m", "base")
	hub := remote.NewStreamHub(remote.StreamHubOptions{Interval: time.Hour})
	t.Cleanup(hub.Close)
	backend := remote.NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	backend.SetDataDir(t.TempDir())
	runner := git.NewRunner("git", 2)
	backend.SetGitRunner(runner)
	backend.AddProject(wire.ProjectSummary{ID: "review-project", Path: root})
	thread, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: "review-project"})
	if err != nil {
		t.Fatal(err)
	}
	const turn = "11111111-1111-4111-8111-111111111111"
	if _, err := runner.Checkpoint(ctx, root, git.CheckpointRef(thread.ThreadID, turn)+"/start", "start"); err != nil {
		t.Fatal(err)
	}
	write(next)
	if _, err := runner.Checkpoint(ctx, root, git.CheckpointRef(thread.ThreadID, turn)+"/end", "end"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := backend.ReviewGet(ctx, wire.ThreadsReviewGetParams{ThreadID: thread.ThreadID, Scope: wire.ReviewScopeTurn, TurnID: turn, Path: "a.txt"})
	if err != nil || snapshot.File == nil {
		t.Fatalf("快照不可用: %+v %v", snapshot, err)
	}
	hash := func(text string) string { h := sha256.Sum256([]byte(text)); return hex.EncodeToString(h[:]) }
	hunkHash, err := git.RevertHunkHash(next, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	p := mcodeui.ReviewRevertHunkParams{ThreadID: thread.ThreadID, Scope: "turn", TurnID: turn, Path: "a.txt", BaseRevision: snapshot.BaseRevision, HeadRevision: snapshot.HeadRevision, StartLine: 2, LineCount: 1, HunkHash: hunkHash, ExpectedFileHash: hash(next)}
	client := NewClient(backend)
	// 实际后端逐字段校验，防止适配层漏传参数却仍返回成功。
	for _, tc := range []struct {
		name string
		edit func(*mcodeui.ReviewRevertHunkParams)
	}{
		{"threadID", func(p *mcodeui.ReviewRevertHunkParams) { p.ThreadID = "missing" }},
		{"scope", func(p *mcodeui.ReviewRevertHunkParams) { p.Scope = "invalid" }},
		{"turnID", func(p *mcodeui.ReviewRevertHunkParams) { p.TurnID = "missing" }},
		{"path", func(p *mcodeui.ReviewRevertHunkParams) { p.Path = "missing" }},
		{"baseRevision", func(p *mcodeui.ReviewRevertHunkParams) { p.BaseRevision = "stale" }},
		{"headRevision", func(p *mcodeui.ReviewRevertHunkParams) { p.HeadRevision = "stale" }},
		{"startLine", func(p *mcodeui.ReviewRevertHunkParams) { p.StartLine = 1 }},
		{"lineCount", func(p *mcodeui.ReviewRevertHunkParams) { p.LineCount = 0 }},
		{"hunkHash", func(p *mcodeui.ReviewRevertHunkParams) { p.HunkHash = "stale" }},
		{"expectedFileHash", func(p *mcodeui.ReviewRevertHunkParams) { p.ExpectedFileHash = "stale" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := p
			tc.edit(&bad)
			result, err := client.ReviewRevertHunk(ctx, bad)
			if err == nil || result != (mcodeui.ReviewRevertHunkResult{}) {
				t.Fatalf("参数校验错误未透传: %+v %v", result, err)
			}
		})
	}
	result, err := client.ReviewRevertHunk(ctx, p)
	data, readErr := os.ReadFile(filepath.Join(root, "a.txt"))
	if err != nil || readErr != nil || string(data) != old || result.FileHash != hash(old) {
		t.Fatalf("参数或结果转换错误: %+v %q %v %v", result, data, err, readErr)
	}
}
