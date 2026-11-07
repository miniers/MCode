package projctx

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestMemo_Draft_FixedNowAndRegex(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}

	fixedTime := time.Date(2026, 10, 17, 10, 30, 45, 0, time.UTC)
	s.now = func() time.Time { return fixedTime }

	pattern := regexp.MustCompile(`^\d{8}-\d{6}-[a-z0-9-]+\.md$`)

	// 1. 使用英文 title
	item, err := s.DraftMemo("Body text", "Feature Spec")
	if err != nil {
		t.Fatalf("DraftMemo failed: %v", err)
	}
	if !pattern.MatchString(item.Name) {
		t.Fatalf("filename %q does not match regex", item.Name)
	}
	if item.Name != "20261017-103045-feature-spec.md" {
		t.Fatalf("unexpected filename: %q", item.Name)
	}
	if !strings.HasPrefix(item.Content, "# Feature Spec\n\n") {
		t.Fatalf("content should have title heading: %q", item.Content)
	}
	if !strings.HasSuffix(item.Content, "\n") {
		t.Fatal("content should end with newline")
	}

	// 2. 中文 title 保留 Unicode 字母，不塌缩为 "memo"
	itemCN, err := s.DraftMemo("中文正文", "你好世界")
	if err != nil {
		t.Fatalf("DraftMemo failed: %v", err)
	}
	if itemCN.Name != "20261017-103045-你好世界.md" {
		t.Fatalf("unexpected filename for chinese title: %q", itemCN.Name)
	}

	// 3. 无 title，从正文第一行提取 # 标题
	s.now = func() time.Time { return fixedTime.Add(time.Second) }
	itemHeading, err := s.DraftMemo("# First Heading\nSome details", "")
	if err != nil {
		t.Fatalf("DraftMemo failed: %v", err)
	}
	if itemHeading.Name != "20261017-103046-first-heading.md" {
		t.Fatalf("unexpected filename from heading: %q", itemHeading.Name)
	}
	if itemHeading.Content != "# First Heading\nSome details\n" {
		t.Fatalf("unexpected content: %q", itemHeading.Content)
	}
}

func TestMemo_DuplicateInSameSecond(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}

	fixedTime := time.Date(2026, 10, 17, 10, 30, 45, 0, time.UTC)
	s.now = func() time.Time { return fixedTime }

	item1, err := s.DraftMemo("memo 1", "same")
	if err != nil {
		t.Fatal(err)
	}
	if item1.Name != "20261017-103045-same.md" {
		t.Fatalf("item1 name mismatch: %q", item1.Name)
	}

	item2, err := s.DraftMemo("memo 2", "same")
	if err != nil {
		t.Fatal(err)
	}
	if item2.Name != "20261017-103045-same-2.md" {
		t.Fatalf("item2 expected -2 suffix, got %q", item2.Name)
	}

	item3, err := s.DraftMemo("memo 3", "same")
	if err != nil {
		t.Fatal(err)
	}
	if item3.Name != "20261017-103045-same-3.md" {
		t.Fatalf("item3 expected -3 suffix, got %q", item3.Name)
	}
}

func TestMemo_Empty(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.DraftMemo("", "title"); !errors.Is(err, ErrEmptyMemo) {
		t.Fatalf("expected ErrEmptyMemo, got %v", err)
	}
	if _, err := s.DraftMemo("   \n\t  ", "title"); !errors.Is(err, ErrEmptyMemo) {
		t.Fatalf("expected ErrEmptyMemo for whitespace, got %v", err)
	}
}

func TestMemo_FileInbox(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}

	// 1. 归入不存在的目标文档：直接创建
	item1, err := s.DraftMemo("Initial notes", "Notes")
	if err != nil {
		t.Fatal(err)
	}
	doc1, err := s.FileInbox(item1.Name, "target.md")
	if err != nil {
		t.Fatalf("FileInbox into new doc failed: %v", err)
	}
	if doc1.Name != "target.md" {
		t.Fatalf("expected target.md, got %q", doc1.Name)
	}
	expectedContent1 := strings.TrimSpace(item1.Content) + "\n"
	if doc1.Content != expectedContent1 {
		t.Fatalf("got %q, want %q", doc1.Content, expectedContent1)
	}
	// 归入后 inbox 文件被删除
	if _, err := os.Stat(filepath.Join(s.InboxPath(), item1.Name)); !os.IsNotExist(err) {
		t.Fatalf("inbox file should be deleted, got err: %v", err)
	}

	// 2. 归入已有文档：带 \n---\n\n 分隔
	item2, err := s.DraftMemo("Followup points", "Followup")
	if err != nil {
		t.Fatal(err)
	}
	doc2, err := s.FileInbox(item2.Name, "target.md")
	if err != nil {
		t.Fatalf("FileInbox into existing doc failed: %v", err)
	}
	expectedContent2 := expectedContent1 + "\n---\n\n" + strings.TrimSpace(item2.Content) + "\n"
	if doc2.Content != expectedContent2 {
		t.Fatalf("got %q, want %q", doc2.Content, expectedContent2)
	}
	if _, err := os.Stat(filepath.Join(s.InboxPath(), item2.Name)); !os.IsNotExist(err) {
		t.Fatalf("inbox file 2 should be deleted, got err: %v", err)
	}

	// 3. 不存在的 inbox 项返回 ErrNotFound
	if _, err := s.FileInbox("notexist.md", "target.md"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing inbox item, got %v", err)
	}
}

func TestMemo_RefusesSymlinkedInbox(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Ensure(); err != nil {
		t.Fatal(err)
	}

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "leak.md"), []byte("outside secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(s.InboxPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, s.InboxPath()); err != nil {
		t.Fatal(err)
	}

	items, err := s.ListInbox()
	if err != nil {
		t.Fatalf("ListInbox with symlinked inbox failed: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected no inbox items for symlinked inbox dir, got %d (outside content leaked)", len(items))
	}

	if _, err := s.DraftMemo("new memo", "Symlink"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("DraftMemo into symlinked inbox expected ErrInvalidName, got %v", err)
	}
	if _, err := s.FileInbox("leak.md", "target.md"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("FileInbox from symlinked inbox expected ErrInvalidName, got %v", err)
	}
}

func TestMemo_DiscardInbox(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}

	item, err := s.DraftMemo("To be discarded", "Discard Me")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.DiscardInbox(item.Name); err != nil {
		t.Fatalf("DiscardInbox failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.InboxPath(), item.Name)); !os.IsNotExist(err) {
		t.Fatalf("expected inbox file deleted, got err: %v", err)
	}

	// 再次丢弃返回 ErrNotFound
	if err := s.DiscardInbox(item.Name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on discarding missing item, got %v", err)
	}
}

func TestMemo_ListInbox(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	item1, err := s.DraftMemo("memo 1", "first")
	if err != nil {
		t.Fatal(err)
	}

	s.now = func() time.Time { return now.Add(time.Second) }
	item2, err := s.DraftMemo("memo 2", "second")
	if err != nil {
		t.Fatal(err)
	}

	items, err := s.ListInbox()
	if err != nil {
		t.Fatalf("ListInbox failed: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	// 倒序排列，最新（item2）在前
	if items[0].Name != item2.Name || items[1].Name != item1.Name {
		t.Fatalf("expected descending order [%s, %s], got [%s, %s]", item2.Name, item1.Name, items[0].Name, items[1].Name)
	}
}
