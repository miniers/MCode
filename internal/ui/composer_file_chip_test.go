package mcodeui

import (
	"testing"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"mcode/internal/agentconf"
)

func TestComposerTagRunDeletesWhole(t *testing.T) {
	tagColor := ui.RGB(0, 100, 200)
	setupMention := func(t *testing.T) (*chatState, *ui.Tester) {
		t.Helper()
		fake := NewFakeClient()
		fake.SetFiles([]FileMatch{{Name: "my file.md", Path: "docs/my file.md"}})
		s := newChatState(fake)
		tt := composerTester(s)
		tt.Type("hello  world")
		for i := 0; i < 6; i++ {
			tt.Key(0, ui.KeyLeft)
		}
		tt.Type("@docs")
		waitFor(t, tt, func() bool { return tt.HasText("docs/my file.md") })
		tt.Key(0, ui.KeyEnter)
		wantDraft := "hello @docs/my file.md world"
		wantCaret := utf8.RuneCountInString("hello @docs/my file.md")
		if s.draft != wantDraft {
			t.Fatalf("draft after confirm = %q, want %q", s.draft, wantDraft)
		}
		if s.completionCaret != wantCaret {
			t.Fatalf("caret after confirm = %d, want %d", s.completionCaret, wantCaret)
		}
		if ranges := s.mentionRanges(tagColor); len(ranges) != 1 {
			t.Fatalf("mentionRanges = %+v, want 1 tag range", ranges)
		}
		return s, tt
	}

	t.Run("backspace at end deletes whole tag", func(t *testing.T) {
		s, tt := setupMention(t)
		tt.Key(0, ui.KeyBackspace)
		wantCaret := utf8.RuneCountInString("hello ")
		if s.draft != "hello  world" {
			t.Fatalf("draft = %q, want %q", s.draft, "hello  world")
		}
		if s.completionCaret != wantCaret {
			t.Fatalf("caret = %d, want %d", s.completionCaret, wantCaret)
		}
		if ranges := s.mentionRanges(tagColor); len(ranges) != 0 {
			t.Fatalf("mentionRanges = %+v, want empty", ranges)
		}
	})

	t.Run("backspace in middle deletes whole tag", func(t *testing.T) {
		s, tt := setupMention(t)
		for i := 0; i < 4; i++ {
			tt.Key(0, ui.KeyLeft)
		}
		if want := utf8.RuneCountInString("hello @docs/my fil"); s.completionCaret != want {
			t.Fatalf("caret before backspace = %d, want %d", s.completionCaret, want)
		}
		tt.Key(0, ui.KeyBackspace)
		wantCaret := utf8.RuneCountInString("hello ")
		if s.draft != "hello  world" {
			t.Fatalf("draft = %q, want %q", s.draft, "hello  world")
		}
		if s.completionCaret != wantCaret {
			t.Fatalf("caret = %d, want %d", s.completionCaret, wantCaret)
		}
		if ranges := s.mentionRanges(tagColor); len(ranges) != 0 {
			t.Fatalf("mentionRanges = %+v, want empty", ranges)
		}
	})

	t.Run("delete at start deletes whole tag", func(t *testing.T) {
		s, tt := setupMention(t)
		tagLen := utf8.RuneCountInString("@docs/my file.md")
		for i := 0; i < tagLen; i++ {
			tt.Key(0, ui.KeyLeft)
		}
		wantCaret := utf8.RuneCountInString("hello ")
		if s.completionCaret != wantCaret {
			t.Fatalf("caret before delete = %d, want %d", s.completionCaret, wantCaret)
		}
		tt.Key(0, ui.KeyDelete)
		if s.draft != "hello  world" {
			t.Fatalf("draft = %q, want %q", s.draft, "hello  world")
		}
		if s.completionCaret != wantCaret {
			t.Fatalf("caret = %d, want %d", s.completionCaret, wantCaret)
		}
		if ranges := s.mentionRanges(tagColor); len(ranges) != 0 {
			t.Fatalf("mentionRanges = %+v, want empty", ranges)
		}

		// 边界保护：片段起点 Backspace 与片段末尾 Delete 不命中整段删除
		sBoundary, _ := setupMention(t)
		if _, _, ok := sBoundary.deleteTagRunAt(wantCaret, false); ok {
			t.Fatal("backspace at tag start must not delete tag")
		}
		endCaret := utf8.RuneCountInString("hello @docs/my file.md")
		if _, _, ok := sBoundary.deleteTagRunAt(endCaret, true); ok {
			t.Fatal("delete at tag end must not delete tag")
		}
		if sBoundary.draft != "hello @docs/my file.md world" {
			t.Fatalf("boundary miss mutated draft: %q", sBoundary.draft)
		}
	})

	t.Run("backspace after plain text deletes single char", func(t *testing.T) {
		s, tt := setupMention(t)
		for i := 0; i < 6; i++ {
			tt.Key(0, ui.KeyRight)
		}
		tt.Key(0, ui.KeyBackspace)
		wantDraft := "hello @docs/my file.md worl"
		if s.draft != wantDraft {
			t.Fatalf("draft = %q, want %q", s.draft, wantDraft)
		}
		if ranges := s.mentionRanges(tagColor); len(ranges) != 1 {
			t.Fatalf("mentionRanges = %+v, want 1 preserved tag", ranges)
		}
	})

	t.Run("backspace at skill tag deletes whole tag", func(t *testing.T) {
		origScan := scanSkills
		resetSkillCache()
		t.Cleanup(func() {
			scanSkills = origScan
			resetSkillCache()
		})
		scanSkills = func(opts agentconf.Options) ([]agentconf.Skill, error) {
			return []agentconf.Skill{
				{Agent: "claude", Scope: "project", Name: "design-mockup", Description: "设计稿", Path: opts.ProjectDir + "/d/SKILL.md"},
			}, nil
		}

		s := newChatState(NewFakeClient())
		s.projects = []Project{{ID: "p1", Name: "repo", Path: "/repo"}}
		s.threads = []Thread{{ID: "t1", ProjectID: "p1"}}
		tt := composerTester(s)
		tt.Type("hello  world")
		for i := 0; i < 6; i++ {
			tt.Key(0, ui.KeyLeft)
		}
		tt.Type("$")
		waitFor(t, tt, func() bool { return tt.HasText("$design-mockup") })
		tt.Key(0, ui.KeyEnter)
		if s.draft != "hello $design-mockup world" {
			t.Fatalf("skill draft after confirm = %q", s.draft)
		}
		tt.Key(0, ui.KeyBackspace)
		wantCaret := utf8.RuneCountInString("hello ")
		if s.draft != "hello  world" {
			t.Fatalf("skill draft after backspace = %q, want %q", s.draft, "hello  world")
		}
		if s.completionCaret != wantCaret {
			t.Fatalf("skill caret after backspace = %d, want %d", s.completionCaret, wantCaret)
		}
		if ranges := s.mentionRanges(tagColor); len(ranges) != 0 {
			t.Fatalf("mentionRanges = %+v, want empty", ranges)
		}
	})
}
