package swarm

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mcode/internal/git"
)

func amFixCommitFile(t *testing.T, dir, name, content, msg string) string {
	t.Helper()
	writePipelineFile(t, dir, name, content)
	gitTestRun(t, dir, "add", name)
	gitTestRun(t, dir, "commit", "-m", msg)
	return gitTestRun(t, dir, "rev-parse", "HEAD")
}

func TestRunLocalLandedCommitsRejectsForeignHead(t *testing.T) {
	t.Parallel()
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	shaA := amFixCommitFile(t, dir, "a.txt", "a\n", "commit A")
	shaB := amFixCommitFile(t, dir, "b.txt", "b\n", "commit B")

	var pushCalls atomic.Int32
	var notified []string
	var results []BatchResult
	p := NewPipeline(PipelineOptions{
		Git:      runner,
		Forge:    git.NewForgeClient(nil),
		Settings: func() Settings { return Settings{AutoMerge: true, AutoPush: true} },
		Notify:   func(_, msg string) { notified = append(notified, msg) },
		Push: func(context.Context, string, string, string, string) error {
			pushCalls.Add(1)
			return nil
		},
		OnResult: func(_ LandedBatch, r BatchResult) { results = append(results, r) },
	})

	foreignBatch := newPipelineFileBatch(dir, Bee{Name: "A", Task: "做 A", Merged: true, LandedCommit: shaA})
	prep, err := p.runLocal(ctx, foreignBatch)
	if err == nil {
		t.Fatalf("HEAD=%s 非本批次提交 %s 时 runLocal 应返回错误，实际 prep=%+v", shaB, shaA, prep)
	}
	if !strings.Contains(err.Error(), "非本批次") {
		t.Fatalf("错误信息应说明非本批次提交，实际: %v", err)
	}
	if !anyContains(notified, "合并已停止") {
		t.Fatalf("应发送合并已停止通知，实际: %v", notified)
	}

	if err := p.RunBatch(ctx, foreignBatch, false); err == nil {
		t.Fatal("HEAD 前进到他人提交时 RunBatch 应返回错误")
	}
	if pushCalls.Load() != 0 {
		t.Fatalf("不应触发 push，实际 %d 次", pushCalls.Load())
	}
	if len(results) != 1 || results[0].State != AutoMergeFailed {
		t.Fatalf("应上报失败结果，实际 %+v", results)
	}

	matchingBatch := newPipelineFileBatch(dir, Bee{Name: "B", Task: "做 B", Merged: true, LandedCommit: shaB})
	prep, err = p.runLocal(ctx, matchingBatch)
	if err != nil {
		t.Fatalf("HEAD 等于 LandedCommit 时 runLocal 不应失败: %v", err)
	}
	if prep == nil || prep.sha != shaB {
		t.Fatalf("prep.sha = %v, want %s", prep, shaB)
	}
}

func TestPipelineOnBatchLandedAfterStopIsNoop(t *testing.T) {
	t.Parallel()
	runner, dir := initTestRepo(t)
	sha := amFixCommitFile(t, dir, "a.txt", "a\n", "commit A")

	var settingsCalls, pushCalls, reportCalls atomic.Int32
	p := NewPipeline(PipelineOptions{
		Git: runner,
		Settings: func() Settings {
			settingsCalls.Add(1)
			return Settings{AutoMerge: true, AutoPush: true}
		},
		Push: func(context.Context, string, string, string, string) error {
			pushCalls.Add(1)
			return nil
		},
		OnResult: func(LandedBatch, BatchResult) {
			reportCalls.Add(1)
		},
	})
	p.Start()
	p.Stop()

	b := newPipelineFileBatch(dir, Bee{Name: "A", Task: "做 A", Merged: true, LandedCommit: sha})
	p.OnBatchLanded(context.Background(), b)

	time.Sleep(50 * time.Millisecond)
	p.Stop()

	if settingsCalls.Load() != 0 || pushCalls.Load() != 0 || reportCalls.Load() != 0 {
		t.Fatalf("Stop 后 OnBatchLanded 应为空操作，实际 settings=%d push=%d report=%d",
			settingsCalls.Load(), pushCalls.Load(), reportCalls.Load())
	}
}

func TestPipelineResumeBatchAfterStopIsNoop(t *testing.T) {
	t.Parallel()
	runner, dir := initTestRepo(t)
	sha := amFixCommitFile(t, dir, "a.txt", "a\n", "commit A")

	var settingsCalls, pushCalls, reportCalls atomic.Int32
	p := NewPipeline(PipelineOptions{
		Git: runner,
		Settings: func() Settings {
			settingsCalls.Add(1)
			return Settings{AutoMerge: true, AutoPush: true}
		},
		Push: func(context.Context, string, string, string, string) error {
			pushCalls.Add(1)
			return nil
		},
		OnResult: func(LandedBatch, BatchResult) {
			reportCalls.Add(1)
		},
	})
	p.Start()
	p.Stop()

	b := newPipelineFileBatch(dir, Bee{Name: "A", Task: "做 A", Merged: true, LandedCommit: sha})
	p.ResumeBatch(context.Background(), b, sha, StagePushing)
	p.ResumeBatch(context.Background(), b, "", StageGathering)

	time.Sleep(50 * time.Millisecond)
	p.Stop()

	if settingsCalls.Load() != 0 || pushCalls.Load() != 0 || reportCalls.Load() != 0 {
		t.Fatalf("Stop 后 ResumeBatch 应为空操作，实际 settings=%d push=%d report=%d",
			settingsCalls.Load(), pushCalls.Load(), reportCalls.Load())
	}
}
