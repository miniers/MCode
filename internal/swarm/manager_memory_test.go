package swarm

import (
	"context"
	"strings"
	"testing"
	"time"

	"mcode/internal/kanban"
)

// 注入 MemoryRecall 且返回内容时，bee 启动提示词末尾追加带标题的记忆段落。
func TestManager_DispatchSingleEntryWithMemoryRecall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	runner, dir := initTestRepo(t)

	driver := newFakeDriver()
	var started kanban.SessionRequest
	driver.onStart = func(req kanban.SessionRequest) { started = req }

	const block = "- **用 worktree 隔离改动**（decision）：风险高的改动先开 worktree。"
	var calls int
	var gotWorkDir, gotQuery string
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		MemoryRecall: func(_ context.Context, workDir, query string) string {
			calls++
			gotWorkDir = workDir
			gotQuery = query
			return block
		},
	})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{Enabled: true, AutoDelegate: true, WorkspaceMode: "worktree"}
	})

	entry := Delegation{Task: "T", Prompt: "P"}
	if _, _, err := mgr.DispatchSingleEntry(ctx, SingleEntryRequest{
		LeadThreadID: "lead-memory",
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Entry:        entry,
	}); err != nil {
		t.Fatalf("DispatchSingleEntry failed: %v", err)
	}

	if calls != 1 {
		t.Fatalf("MemoryRecall 应被调用 1 次，实际 %d", calls)
	}
	if gotWorkDir != dir {
		t.Fatalf("MemoryRecall 工作目录 = %q, want %q", gotWorkDir, dir)
	}
	if want := beeRecallQuery(entry); gotQuery != want {
		t.Fatalf("MemoryRecall query = %q, want %q", gotQuery, want)
	}
	want := beePrompt(RosterName(0), entry, BeeConfig{}) + "\n\n" + beeRecallHeader + "\n\n" + block
	if started.Prompt != want {
		t.Fatalf("提示词应在末尾附带记忆段落：\ngot  %q\nwant %q", started.Prompt, want)
	}
	if !strings.HasSuffix(started.Prompt, beeRecallHeader+"\n\n"+block) {
		t.Fatalf("记忆段落应在提示词末尾: %q", started.Prompt)
	}
}

// 钩子为 nil 或返回空串时，提示词与 beePrompt 逐字一致。
func TestManager_DispatchSingleEntryWithoutMemoryRecall(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		recall func(ctx context.Context, workDir, query string) string
	}{
		{name: "nil hook", recall: nil},
		{name: "empty block", recall: func(context.Context, string, string) string { return "" }},
		{name: "blank block", recall: func(context.Context, string, string) string { return "  \n\t" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			runner, dir := initTestRepo(t)

			driver := newFakeDriver()
			var started kanban.SessionRequest
			driver.onStart = func(req kanban.SessionRequest) { started = req }

			mgr := NewManager(Options{
				Git:          runner,
				Driver:       driver,
				WorktreeDir:  t.TempDir(),
				MemoryRecall: tc.recall,
			})
			mgr.SetSettingsFunc(func() Settings {
				return Settings{Enabled: true, AutoDelegate: true, WorkspaceMode: "worktree"}
			})

			entry := Delegation{Task: "T", Prompt: "P"}
			if _, _, err := mgr.DispatchSingleEntry(ctx, SingleEntryRequest{
				LeadThreadID: "lead-memory",
				ProjectID:    "proj-1",
				LeadWorkDir:  dir,
				Entry:        entry,
			}); err != nil {
				t.Fatalf("DispatchSingleEntry failed: %v", err)
			}
			if strings.Contains(started.Prompt, beeRecallHeader) {
				t.Fatalf("无记忆时提示词不应含记忆段落: %q", started.Prompt)
			}
			if want := beePrompt(RosterName(0), entry, BeeConfig{}); started.Prompt != want {
				t.Fatalf("无记忆时提示词应为 beePrompt，got %q want %q", started.Prompt, want)
			}
		})
	}
}

// SetMemoryRecall 在接线后注入的钩子同样生效。
func TestManager_SetMemoryRecallAfterConstruction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	runner, dir := initTestRepo(t)

	driver := newFakeDriver()
	var started kanban.SessionRequest
	driver.onStart = func(req kanban.SessionRequest) { started = req }

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
	})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{Enabled: true, AutoDelegate: true, WorkspaceMode: "worktree"}
	})
	mgr.SetMemoryRecall(func(context.Context, string, string) string { return "- 历史" })

	if _, _, err := mgr.DispatchSingleEntry(ctx, SingleEntryRequest{
		LeadThreadID: "lead-memory",
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Entry:        Delegation{Task: "T", Prompt: "P"},
	}); err != nil {
		t.Fatalf("DispatchSingleEntry failed: %v", err)
	}
	if !strings.HasSuffix(started.Prompt, beeRecallHeader+"\n\n- 历史") {
		t.Fatalf("注入钩子后提示词应带记忆段落: %q", started.Prompt)
	}
}

// beeRecallQuery 由任务与提示词前 300 个 rune 组成。
func TestBeeRecallQuery(t *testing.T) {
	t.Parallel()
	short := Delegation{Task: "修复派发", Prompt: "  让 bee 保留缩进  "}
	if got, want := beeRecallQuery(short), "修复派发\n让 bee 保留缩进"; got != want {
		t.Fatalf("beeRecallQuery = %q, want %q", got, want)
	}

	long := strings.Repeat("长", beeRecallQueryRunes+50)
	got := beeRecallQuery(Delegation{Task: "任务", Prompt: long})
	want := "任务\n" + strings.Repeat("长", beeRecallQueryRunes)
	if got != want {
		t.Fatalf("beeRecallQuery 截断不正确：got 长度 %d want 长度 %d", len([]rune(got)), len([]rune(want)))
	}
	if strings.Contains(got, strings.Repeat("长", beeRecallQueryRunes+1)) {
		t.Fatal("beeRecallQuery 不应包含超过 300 个 rune 的提示词")
	}

	empty := beeRecallQuery(Delegation{Task: "  ", Prompt: ""})
	if empty != "" {
		t.Fatalf("beeRecallQuery 对空任务与空提示词应返回空串，got %q", empty)
	}
}

// beeRecallBlock 把 3 秒预算写进 ctx 传给钩子，并原样带上标题。
func TestBeeRecallBlockDeadline(t *testing.T) {
	t.Parallel()
	var deadline time.Time
	block := beeRecallBlock(context.Background(), func(ctx context.Context, _, query string) string {
		deadline, _ = ctx.Deadline()
		if query != "T\nP" {
			t.Errorf("query = %q, want %q", query, "T\nP")
		}
		return "  - 历史  "
	}, "/tmp", Delegation{Task: "T", Prompt: "P"})
	if want := beeRecallHeader + "\n\n- 历史"; block != want {
		t.Fatalf("beeRecallBlock = %q, want %q", block, want)
	}
	if deadline.IsZero() {
		t.Fatal("钩子应拿到带超时的 ctx")
	}
	if left := time.Until(deadline); left > beeRecallTimeout || left < beeRecallTimeout-time.Second {
		t.Fatalf("钩子 ctx 剩余 %v, want 约 %v", left, beeRecallTimeout)
	}
}
