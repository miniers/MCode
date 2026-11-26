package remote

import (
	"context"
	"strings"
	"testing"
	"time"

	"mcode/internal/agentconf"
	"mcode/internal/harness"
	"mcode/internal/projctx"
	"mcode/internal/remote/wire"
	"mcode/internal/testenv"
)

func TestThreadCommandsOpenArtifact(t *testing.T) {
	testenv.TempHome(t)
	project := t.TempDir()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	projectID := newUUID()
	threadID := newUUID()
	backend.AddProject(wire.ProjectSummary{ID: projectID, Name: "OpenArtifact", Path: project})
	backend.AddThread(wire.ThreadSummary{ID: threadID, ProjectID: projectID, Provider: "claude", Title: "Cmd Thread"}, nil)

	origScanSkills := scanSkills
	defer func() { scanSkills = origScanSkills }()
	scanSkills = func(opts agentconf.Options) ([]agentconf.Skill, error) {
		return []agentconf.Skill{
			{Agent: "claude", Scope: "global", Name: "custom-skill", Description: "Custom skill"},
		}, nil
	}

	ctx := context.Background()
	params := wire.ThreadsCommandsParams{ThreadID: &threadID}

	// extraGuidance 为空时不追加 open-artifact
	resEmpty, err := backend.ThreadCommands(ctx, params)
	if err != nil {
		t.Fatalf("ThreadCommands (empty extraGuidance) failed: %v", err)
	}
	if cmd := commandByName(resEmpty, "open-artifact"); cmd != nil {
		t.Fatalf("expected no open-artifact when extraGuidance is empty, got: %+v", cmd)
	}

	// extraGuidance 非空时在末尾追加 open-artifact，Kind 为 command
	backend.SetExtraGuidance(func() string { return "# Make an artifact: `/open-artifact`" })
	resWith, err := backend.ThreadCommands(ctx, params)
	if err != nil {
		t.Fatalf("ThreadCommands (with extraGuidance) failed: %v", err)
	}
	if len(resWith.Commands) == 0 {
		t.Fatalf("expected non-empty commands")
	}
	last := resWith.Commands[len(resWith.Commands)-1]
	if last.Name != "open-artifact" || last.Kind != "command" || last.IsBuiltIn || last.Detail != "创建交付物" {
		t.Fatalf("expected open-artifact command at end, got: %+v", last)
	}

	// 已有同名磁盘技能时不重复追加
	scanSkills = func(opts agentconf.Options) ([]agentconf.Skill, error) {
		return []agentconf.Skill{
			{Agent: "claude", Scope: "global", Name: "open-artifact", Description: "Disk skill"},
		}, nil
	}
	resDup, err := backend.ThreadCommands(ctx, params)
	if err != nil {
		t.Fatalf("ThreadCommands (duplicate disk skill) failed: %v", err)
	}
	count := 0
	for _, cmd := range resDup.Commands {
		if cmd.Name == "open-artifact" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 open-artifact command, got %d", count)
	}
}

func TestRewriteOpenArtifactCommand(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantText  string
		wantMatch bool
	}{
		{
			name:      "with args",
			input:     "/open-artifact slides 做季度汇报",
			wantText:  "用 /open-artifact 制作：slides 做季度汇报",
			wantMatch: true,
		},
		{
			name:      "leading whitespace no args",
			input:     "  /open-artifact",
			wantText:  "用 /open-artifact 制作交付物",
			wantMatch: true,
		},
		{
			name:      "trailing spaces only",
			input:     "/open-artifact   ",
			wantText:  "用 /open-artifact 制作交付物",
			wantMatch: true,
		},
		{
			name:      "similar prefix not matched",
			input:     "/open-artifactx",
			wantText:  "/open-artifactx",
			wantMatch: false,
		},
		{
			name:      "plain text not matched",
			input:     "普通文本",
			wantText:  "普通文本",
			wantMatch: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotText, gotMatch := rewriteOpenArtifactCommand(tc.input)
			if gotMatch != tc.wantMatch || gotText != tc.wantText {
				t.Fatalf("rewriteOpenArtifactCommand(%q) = (%q, %v), want (%q, %v)", tc.input, gotText, gotMatch, tc.wantText, tc.wantMatch)
			}
		})
	}
}

func TestMemoryBackendOpenArtifactCommandInjectsGuidance(t *testing.T) {
	testenv.TempHome(t)
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
	extraDoc := "# Make an artifact: `/open-artifact <type> <what to make>`"
	backend.SetExtraGuidance(func() string { return extraDoc })

	sendGuidanceMessage(t, backend, threadID, "/open-artifact slides 做季度汇报", "00000000-0000-0000-0000-000000000399")

	calls := mock.getTurnCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 harness call, got %d", len(calls))
	}
	prompt := calls[0].Prompt
	if !strings.Contains(prompt, extraDoc) {
		t.Fatalf("first prompt should contain extraGuidance, got %q", prompt)
	}
	if !strings.Contains(prompt, "用 /open-artifact 制作：slides 做季度汇报") {
		t.Fatalf("first prompt should contain rewritten text, got %q", prompt)
	}
}
