package remote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/agentconf"
	"mcode/internal/harness"
	"mcode/internal/projctx"
	"mcode/internal/remote/wire"
)

// guidanceTestBackend 构建一个绑定了 mock harness 与临时项目目录的后端。
func guidanceTestBackend(t *testing.T, project string, mock *mockHarness) (*MemoryBackend, string) {
	t.Helper()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	t.Cleanup(hub.Close)

	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	backend.SetHarness(mock, project, harness.ModeAgent)

	threadID := "00000000-0000-0000-0000-0000000000c5"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Guidance Thread"}, nil)
	return backend, threadID
}

func sendGuidanceMessage(t *testing.T, backend *MemoryBackend, threadID, text, actionID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     text,
		ActionID: actionID,
	}); err != nil {
		t.Fatalf("SendMessage(%q) failed: %v", text, err)
	}
	waitThreadIdle(t, backend, ctx, threadID)
}

func TestMemoryBackend_InjectsGuidanceOnFirstTurnOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	ctxSvc, err := projctx.New(project)
	if err != nil {
		t.Fatalf("projctx.New failed: %v", err)
	}
	if _, err := ctxSvc.Write("instructions.md", "# 项目规则\n使用简体中文。", ""); err != nil {
		t.Fatal(err)
	}

	mock := &mockHarness{runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		emit(harness.Event{Type: harness.EventText, Text: "好的"})
		return nil
	}}
	backend, threadID := guidanceTestBackend(t, project, mock)

	sendGuidanceMessage(t, backend, threadID, "第一条消息", "00000000-0000-0000-0000-000000000301")
	sendGuidanceMessage(t, backend, threadID, "第二条消息", "00000000-0000-0000-0000-000000000302")

	calls := mock.getTurnCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 harness calls, got %d", len(calls))
	}

	first := calls[0].Prompt
	if !strings.Contains(first, "<project_context>") {
		t.Fatalf("first prompt should carry the project context, got %q", first)
	}
	if !strings.Contains(first, "使用简体中文。") {
		t.Fatalf("first prompt should carry the doc content, got %q", first)
	}
	if strings.Index(first, "<project_context>") > strings.Index(first, "第一条消息") {
		t.Fatalf("project context should precede the user text, got %q", first)
	}
	if !strings.HasSuffix(first, "第一条消息") {
		t.Fatalf("user text should stay at the end of the first prompt, got %q", first)
	}

	second := calls[1].Prompt
	if strings.Contains(second, "<project_context>") {
		t.Fatalf("second prompt should not repeat the project context, got %q", second)
	}
	if !strings.HasSuffix(second, "第二条消息") {
		t.Fatalf("second prompt should end with the plain user text, got %q", second)
	}
}

func TestMemoryBackend_SlashCommandSkipsGuidance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	ctxSvc, err := projctx.New(project)
	if err != nil {
		t.Fatalf("projctx.New failed: %v", err)
	}
	if _, err := ctxSvc.Write("instructions.md", "# 项目规则", ""); err != nil {
		t.Fatal(err)
	}

	mock := &mockHarness{runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		emit(harness.Event{Type: harness.EventText, Text: "好的"})
		return nil
	}}
	backend, threadID := guidanceTestBackend(t, project, mock)

	// 斜杠命令不触发注入，也不消耗首轮交付。
	sendGuidanceMessage(t, backend, threadID, "/compact", "00000000-0000-0000-0000-000000000303")
	sendGuidanceMessage(t, backend, threadID, "普通消息", "00000000-0000-0000-0000-000000000304")

	calls := mock.getTurnCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 harness calls, got %d", len(calls))
	}
	if strings.Contains(calls[0].Prompt, "<project_context>") {
		t.Fatalf("slash command should not carry guidance, got %q", calls[0].Prompt)
	}
	if !strings.Contains(calls[1].Prompt, "<project_context>") {
		t.Fatalf("ordinary message after a slash command should carry guidance, got %q", calls[1].Prompt)
	}
}

func TestMemoryBackend_RetriesGuidanceAfterFailedTurn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	ctxSvc, err := projctx.New(project)
	if err != nil {
		t.Fatalf("projctx.New failed: %v", err)
	}
	if _, err := ctxSvc.Write("instructions.md", "# 项目规则", ""); err != nil {
		t.Fatal(err)
	}

	attempt := 0
	mock := &mockHarness{runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		attempt++
		if attempt == 1 {
			return errors.New("provider 运行失败")
		}
		emit(harness.Event{Type: harness.EventText, Text: "好的"})
		return nil
	}}
	backend, threadID := guidanceTestBackend(t, project, mock)

	sendGuidanceMessage(t, backend, threadID, "第一条消息", "00000000-0000-0000-0000-000000000305")
	sendGuidanceMessage(t, backend, threadID, "第二条消息", "00000000-0000-0000-0000-000000000306")

	calls := mock.getTurnCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 harness calls, got %d", len(calls))
	}
	if !strings.Contains(calls[0].Prompt, "<project_context>") {
		t.Fatalf("first prompt should carry the project context, got %q", calls[0].Prompt)
	}
	if !strings.Contains(calls[1].Prompt, "<project_context>") {
		t.Fatalf("guidance should be retried after a failed turn, got %q", calls[1].Prompt)
	}
}

// 设置页停用的技能不再进入首轮注入的技能清单。
func TestDisabledSkillPathsSkipsDeactivatedSkill(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	t.Setenv("HOME", home)

	skillDir := filepath.Join(home, ".agents", "skills", "foo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: foo\ndescription: 停用后不应注入\n---\n正文"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 设置页用的扫描参数：DataDir 与后端一致，保证读写同一份 skills.json。
	dataDir := filepath.Join(home, ".mcode")
	opts := agentconf.Options{Home: home, ProjectDir: project, DataDir: dataDir}
	skills, err := agentconf.ScanSkills(opts)
	if err != nil {
		t.Fatalf("ScanSkills failed: %v", err)
	}
	var foo agentconf.Skill
	found := false
	for _, sk := range skills {
		if sk.Name == "foo" {
			foo, found = sk, true
		}
	}
	if !found {
		t.Fatalf("未扫描到 foo：%+v", skills)
	}
	if err := agentconf.SetSkillEnabled(opts, foo, false); err != nil {
		t.Fatalf("SetSkillEnabled failed: %v", err)
	}

	if guidance := projctx.BuildSkillsGuidance(project, home); !strings.Contains(guidance, "foo") {
		t.Fatalf("未停用时应能看到 foo：%s", guidance)
	}

	backend := NewMemoryBackend(nil)
	t.Cleanup(backend.Close)
	backend.SetDataDir(dataDir)

	disabled := backend.disabledSkillPaths(home, project)
	if !disabled[normalizeSkillPath(foo.Path)] {
		t.Fatalf("停用技能的路径应在集合里：%+v", disabled)
	}

	excluded := func(path string) bool { return disabled[normalizeSkillPath(path)] }
	if guidance := projctx.BuildSkillsGuidanceExcluding(project, home, excluded); strings.Contains(guidance, "foo") {
		t.Fatalf("停用的技能不应出现在注入清单里：%s", guidance)
	}
}
