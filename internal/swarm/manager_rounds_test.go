package swarm

import (
	"context"
	"errors"
	"testing"

	"mcode/internal/testenv"
)

const maxRoundsReply = "计划如下：\n```swarm\n" + `[{"task": "first", "prompt": "first"}]` + "\n```"

// 派发轮次上限：该 Lead 已派发轮次达到 Settings.MaxRounds 后，DispatchFromReply 不再派发并返回 ErrMaxRounds。
func TestDispatchFromReply_MaxRounds(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      newFakeDriver(),
		WorktreeDir: t.TempDir(),
	})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{Enabled: true, AutoDelegate: true, MaxRounds: 1}
	})

	leadID := "lead-max-rounds"
	bees, notices, err := mgr.DispatchFromReply(ctx, ReplyDispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		ReplyText:    maxRoundsReply,
	})
	if err != nil || len(bees) != 1 || len(notices) != 0 {
		t.Fatalf("上限内应派出 1 个 bee: bees=%d notices=%v err=%v", len(bees), notices, err)
	}
	mgr.NoteDelegationRound(leadID)

	bees, _, err = mgr.DispatchFromReply(ctx, ReplyDispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		ReplyText:    maxRoundsReply,
	})
	if !errors.Is(err, ErrMaxRounds) {
		t.Fatalf("达到上限应返回 ErrMaxRounds，实际 err=%v", err)
	}
	if len(bees) != 0 {
		t.Fatalf("达到上限后不应再派出 bee，实际 %d 个", len(bees))
	}
}

// MaxRounds 为 0 表示不限制：轮次继续累计也不阻止派发。
func TestDispatchFromReply_MaxRoundsUnlimited(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      newFakeDriver(),
		WorktreeDir: t.TempDir(),
	})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{Enabled: true, AutoDelegate: true, MaxRounds: 0}
	})

	leadID := "lead-rounds-unlimited"
	for i := 0; i < MaxDelegationRounds+2; i++ {
		mgr.NoteDelegationRound(leadID)
	}

	bees, notices, err := mgr.DispatchFromReply(ctx, ReplyDispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		ReplyText:    maxRoundsReply,
	})
	if err != nil || len(bees) != 1 || len(notices) != 0 {
		t.Fatalf("未配置上限时应继续派出 bee: bees=%d notices=%v err=%v", len(bees), notices, err)
	}
}

// 流式路径在派发前先记轮次：上限内的条目照常派出，超出上限的那一轮返回 ErrMaxRounds。
func TestDispatchSingleEntry_MaxRounds(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      newFakeDriver(),
		WorktreeDir: t.TempDir(),
	})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{Enabled: true, AutoDelegate: true, MaxRounds: 1}
	})

	leadID := "lead-stream-max-rounds"
	mgr.BeginLeadRequest(leadID)
	mgr.NoteDelegationRound(leadID)

	bee, notices, err := mgr.DispatchSingleEntry(ctx, SingleEntryRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Entry:        Delegation{Task: "first", Prompt: "first"},
	})
	if err != nil || bee.ThreadID == "" || len(notices) != 0 {
		t.Fatalf("上限内应派出 bee: bee=%+v notices=%v err=%v", bee, notices, err)
	}

	mgr.NoteDelegationRound(leadID)
	bee, _, err = mgr.DispatchSingleEntry(ctx, SingleEntryRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Entry:        Delegation{Task: "second", Prompt: "second"},
	})
	if !errors.Is(err, ErrMaxRounds) {
		t.Fatalf("达到上限应返回 ErrMaxRounds，实际 err=%v", err)
	}
	if bee.ThreadID != "" {
		t.Fatalf("达到上限后不应再派出 bee，实际 %s", bee.ThreadID)
	}
}
