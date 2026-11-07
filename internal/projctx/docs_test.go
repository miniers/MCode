package projctx

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocs_StandardDefinition(t *testing.T) {
	docs := StandardDocs()
	if len(docs) != 4 {
		t.Fatalf("expected 4 standard documents, got %d", len(docs))
	}
	want := []struct{ name, file, title, icon string }{
		{"instructions", "instructions.md", "Instructions", IconShield},
		{"overview", "overview.md", "Overview", IconMap},
		{"decisions", "decisions.md", "Decisions", IconSeal},
		{"notes", "notes.md", "Notes", IconNote},
	}
	for i, w := range want {
		d := docs[i]
		if d.Name != w.name || d.File != w.file || d.Title != w.title || d.Icon != w.icon {
			t.Fatalf("standard doc %d = %+v, want %+v", i, d, w)
		}
		if d.Subtitle == "" || !strings.HasPrefix(d.Template, "# ") {
			t.Fatalf("standard doc %q needs a subtitle and a markdown template: %+v", d.Name, d)
		}
		if got := d.RelativePath(); got != ".mcode/context/"+w.file {
			t.Fatalf("relative path of %q = %q", d.Name, got)
		}
	}
}

func TestDocs_ParseRef(t *testing.T) {
	cases := []struct {
		raw      string
		file     string
		standard bool
	}{
		{"instructions", "instructions.md", true},
		{"Instructions.md", "instructions.md", true},
		{"notes", "notes.md", true},
		{"notes.md", "notes.md", true},
		{"docs/notes.md", "notes.md", false},
		{"docs/notes", "notes.md", false},
		{".mcode/context/docs/notes.md", "notes.md", false},
		{"docs/instructions", "instructions.md", false},
		{"docs/api-spec.md", "api-spec.md", false},
		{"api-spec", "api-spec.md", false},
		{"ARCHITECTURE.md", "ARCHITECTURE.md", false},
	}
	for _, tc := range cases {
		ref, err := ParseRef(tc.raw)
		if err != nil {
			t.Fatalf("ParseRef(%q) failed: %v", tc.raw, err)
		}
		if ref.Name != tc.file || ref.Standard != tc.standard {
			t.Fatalf("ParseRef(%q) = %+v", tc.raw, ref)
		}
	}
	for _, bad := range []string{"", "  ", "docs/", "../escape.md", ".hidden.md", "a/b.md", "docs/../x.md"} {
		if _, err := ParseRef(bad); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("ParseRef(%q) should fail with ErrInvalidName, got %v", bad, err)
		}
	}
}

func TestDocs_ManifestListsStandardAlways(t *testing.T) {
	project := t.TempDir()
	s, err := New(project)
	if err != nil {
		t.Fatal(err)
	}

	// 目录尚未创建时也列出四篇标准文档，且不创建任何文件。
	m, err := s.Manifest()
	if err != nil {
		t.Fatalf("Manifest failed: %v", err)
	}
	if len(m.Standard()) != 4 || len(m.Custom()) != 0 {
		t.Fatalf("unexpected manifest: %+v", m)
	}
	for _, d := range m.Standard() {
		if d.Exists {
			t.Fatalf("standard doc %q should not exist yet", d.Name)
		}
	}
	if _, err := os.Stat(filepath.Join(project, ".mcode")); !os.IsNotExist(err) {
		t.Fatalf("Manifest must not create files, stat err=%v", err)
	}

	if _, err := s.WriteRef("instructions", "# Rules", ""); err != nil {
		t.Fatalf("WriteRef standard failed: %v", err)
	}
	if _, err := s.WriteRef("api-spec", "# API", ""); err != nil {
		t.Fatalf("WriteRef custom failed: %v", err)
	}
	m, err = s.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	inst, ok := m.Find("instructions")
	if !ok || !inst.Exists || inst.Hash == "" || inst.RelativePath() != ".mcode/context/instructions.md" {
		t.Fatalf("unexpected standard doc info: %+v", inst)
	}
	if data, err := os.ReadFile(filepath.Join(project, ".mcode", "context", "instructions.md")); err != nil || string(data) != "# Rules" {
		t.Fatalf("standard doc should live at .mcode/context/instructions.md: %v", err)
	}
	customs := m.Custom()
	if len(customs) != 1 || customs[0].RefID() != "docs/api-spec.md" || customs[0].Title != "Api Spec" {
		t.Fatalf("unexpected custom docs: %+v", customs)
	}
	if customs[0].RelativePath() != ".mcode/context/docs/api-spec.md" {
		t.Fatalf("unexpected custom path: %q", customs[0].RelativePath())
	}
}

func TestDocs_CreateFromTemplate(t *testing.T) {
	project := t.TempDir()
	s, err := New(project)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := s.CreateDoc("decisions")
	if err != nil {
		t.Fatalf("CreateDoc(decisions) failed: %v", err)
	}
	if !strings.Contains(doc.Content, "# 决策记录") {
		t.Fatalf("standard doc should use the built-in template, got %q", doc.Content)
	}
	if _, err := s.CreateDoc("decisions.md"); !errors.Is(err, ErrDocExists) || !errors.Is(err, os.ErrExist) {
		t.Fatalf("creating an existing doc should fail with ErrDocExists, got %v", err)
	}
	custom, err := s.CreateDoc("design-notes")
	if err != nil {
		t.Fatalf("CreateDoc(custom) failed: %v", err)
	}
	if custom.Content != "# Design Notes\n\n" {
		t.Fatalf("unexpected custom template: %q", custom.Content)
	}
	if data, err := os.ReadFile(filepath.Join(project, ".mcode", "context", "docs", "design-notes.md")); err != nil || string(data) != custom.Content {
		t.Fatalf("custom doc should live under docs/: %v", err)
	}
	prefixed, err := s.CreateDoc("docs/notes")
	if err != nil || prefixed.Name != "notes.md" || prefixed.Content != "# Notes\n\n" {
		t.Fatalf("docs/ prefix should create a custom doc: %+v, %v", prefixed, err)
	}
	if _, err := os.Stat(filepath.Join(project, ".mcode", "context", "notes.md")); !os.IsNotExist(err) {
		t.Fatalf("docs/notes must not create the root notes.md: %v", err)
	}
}

func TestDocs_ReadOrDefault(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	doc, err := s.ReadOrDefault("notes")
	if err != nil {
		t.Fatalf("ReadOrDefault(notes) failed: %v", err)
	}
	if doc.Hash != "" || !strings.HasPrefix(doc.Content, "# 笔记") {
		t.Fatalf("missing standard doc should fall back to its template: %+v", doc)
	}
	if _, err := s.ReadOrDefault("docs/missing.md"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing custom doc should report ErrNotFound, got %v", err)
	}
}

func TestDocs_WriteRefCAS(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	created, err := s.WriteRef("overview", "v1", "")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if _, err := s.WriteRef("overview", "v2", created.Hash); err != nil {
		t.Fatalf("CAS save failed: %v", err)
	}
	if _, err := s.WriteRef("overview", "v3", created.Hash); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale hash should conflict, got %v", err)
	}
	again, err := s.WriteRef("overview", "v2", "")
	if err != nil || again.Hash != HashContent("v2") {
		t.Fatalf("rewriting identical content should succeed: %+v, %v", again, err)
	}
	current, err := s.ReadRef("overview")
	if err != nil || current.Content != "v2" {
		t.Fatalf("unexpected doc after CAS writes: %+v, %v", current, err)
	}
}

func TestDocs_FileInboxIntoStandardDoc(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteRef("notes", "# Existing\n", ""); err != nil {
		t.Fatal(err)
	}
	memo, err := s.DraftMemo("memo body", "Memo Title")
	if err != nil {
		t.Fatal(err)
	}
	if memo.Title() != "Memo Title" {
		t.Fatalf("inbox item title should drop the timestamp prefix, got %q", memo.Title())
	}
	doc, err := s.FileInbox(memo.Name, "notes")
	if err != nil {
		t.Fatalf("FileInbox into standard doc failed: %v", err)
	}
	if !strings.HasPrefix(doc.Content, "# Existing") || !strings.Contains(doc.Content, "memo body") {
		t.Fatalf("unexpected merged content: %q", doc.Content)
	}
	if items, err := s.ListInbox(); err != nil || len(items) != 0 {
		t.Fatalf("memo should leave the inbox after merge: %v, %+v", err, items)
	}
}

func TestDocs_TitleOf(t *testing.T) {
	cases := map[string]string{
		"api-spec.md":                    "Api Spec",
		"20260101-120000-design_note.md": "Design Note",
		"README.MD":                      "README",
	}
	for in, want := range cases {
		if got := TitleOf(in); got != want {
			t.Fatalf("TitleOf(%q) = %q, want %q", in, got, want)
		}
	}
}
