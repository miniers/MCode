package mcodeui

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

func newMentionMenuTester(t *testing.T, fake *FakeClient, threads []Thread) (*chatState, *ui.Tester) {
	t.Helper()
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.projects = []Project{{ID: "p1", Name: "mcode", Path: "/repo"}}
	s.threads = threads
	s.selectedThreadID = "t1"
	tt := ui.NewTester(func(c *ui.Context) {
		renderComposer(c, s)
	}, 900, 500)
	return s, tt
}

func TestMentionMenuRootListing(t *testing.T) {
	fake := NewFakeClient()
	fake.SetFileTree(map[string][]FileEntry{
		"": {
			{Name: "README.md", Path: "README.md"},
			{Name: "src", Path: "src", IsDir: true},
			{Name: "docs", Path: "docs", IsDir: true},
			{Name: ".env", Path: ".env", Protected: true},
			{Name: "dist", Path: "dist", IsDir: true, Ignored: true},
			{Name: "a.go", Path: "a.go", Ignored: true},
		},
	})
	now := time.Now()
	s, tt := newMentionMenuTester(t, fake, []Thread{
		{ID: "t1", ProjectID: "p1", Title: "当前会话", UpdatedAt: now},
		{ID: "t2", ProjectID: "p1", Title: "修复构建", UpdatedAt: now.Add(-time.Minute)},
	})

	tt.Type("@")
	waitFor(t, tt, func() bool { return tt.HasText("docs/") && tt.HasText("README.md") })

	for _, tab := range []string{"全部", "文件", "会话"} {
		if !tt.HasText(tab) {
			t.Fatalf("missing tab %q, texts=%v", tab, tt.Texts())
		}
	}
	for _, old := range []string{"▸ 文件", "▸ 会话", "@file:", "@thread:"} {
		if tt.HasText(old) {
			t.Fatalf("legacy category text %q still rendered: %v", old, tt.Texts())
		}
	}
	if tt.HasText(".env") {
		t.Fatalf("protected entry .env must be filtered out: %v", tt.Texts())
	}
	if !tt.HasText("修复构建") {
		t.Fatalf("thread item missing from root listing: %v", tt.Texts())
	}
	if s.draft != "@" {
		t.Fatalf("draft = %q, want @", s.draft)
	}

	s.mu.Lock()
	items := append([]completionItem(nil), s.completionItems...)
	s.mu.Unlock()
	var fileLabels []string
	for _, it := range items {
		if !it.Header && it.ThreadID == "" {
			fileLabels = append(fileLabels, it.Label)
		}
	}
	wantOrder := []string{"docs/", "src/", "dist/", "README.md", "a.go"}
	if len(fileLabels) != len(wantOrder) {
		t.Fatalf("fileLabels = %v, want %v", fileLabels, wantOrder)
	}
	for i, w := range wantOrder {
		if fileLabels[i] != w {
			t.Fatalf("fileLabels[%d] = %q, want %q (full=%v)", i, fileLabels[i], w, fileLabels)
		}
	}
}

func TestMentionMenuTabs(t *testing.T) {
	fake := NewFakeClient()
	fake.SetFileTree(map[string][]FileEntry{
		"": {
			{Name: "src", Path: "src", IsDir: true},
			{Name: "main.go", Path: "main.go"},
		},
	})
	now := time.Now()
	s, tt := newMentionMenuTester(t, fake, []Thread{
		{ID: "t1", ProjectID: "p1", Title: "当前会话", UpdatedAt: now},
		{ID: "t2", ProjectID: "p1", Title: "日志排查", UpdatedAt: now.Add(-time.Minute)},
	})

	tt.Type("@")
	waitFor(t, tt, func() bool { return tt.HasText("main.go") && tt.HasText("日志排查") })

	// → 切换到「文件」页签
	tt.Key(0, ui.KeyRight)
	tt.Frame()
	if !tt.HasText("main.go") || tt.HasText("日志排查") {
		t.Fatalf("file tab should only show files, texts=%v", tt.Texts())
	}
	if strings.Contains(s.draft, "@file:") || strings.Contains(s.draft, "@thread:") {
		t.Fatalf("draft must not contain @file:/@thread:, got %q", s.draft)
	}

	// → 切换到「会话」页签
	tt.Key(0, ui.KeyRight)
	tt.Frame()
	if !tt.HasText("日志排查") || tt.HasText("main.go") || !tt.HasText("最近会话") {
		t.Fatalf("thread tab should only show threads, texts=%v", tt.Texts())
	}
	if strings.Contains(s.draft, "@file:") || strings.Contains(s.draft, "@thread:") {
		t.Fatalf("draft must not contain @file:/@thread:, got %q", s.draft)
	}

	// ← 切回「文件」页签
	tt.Key(0, ui.KeyLeft)
	tt.Frame()
	if !tt.HasText("main.go") || tt.HasText("日志排查") {
		t.Fatalf("KeyLeft back to file tab failed, texts=%v", tt.Texts())
	}
}

func TestMentionMenuDirectoryBrowse(t *testing.T) {
	fake := NewFakeClient()
	fake.SetFileTree(map[string][]FileEntry{
		"": {
			{Name: "internal", Path: "internal", IsDir: true},
			{Name: "go.mod", Path: "go.mod"},
		},
		"internal": {
			{Name: "ui", Path: "internal/ui", IsDir: true},
			{Name: "app.go", Path: "internal/app.go"},
		},
		"internal/ui": {
			{Name: "composer.go", Path: "internal/ui/composer.go"},
		},
	})
	fake.SetFiles([]FileMatch{
		{Name: "composer.go", Path: "internal/ui/composer.go"},
		{Name: "main.go", Path: "cmd/main.go"},
	})
	fake.SetFileContents(map[string]FileContent{
		"internal/ui/composer.go": {Path: "internal/ui/composer.go", Kind: "text", Text: "package mcodeui\n"},
	})

	s, tt := newMentionMenuTester(t, fake, []Thread{{ID: "t1", ProjectID: "p1", Title: "当前会话"}})

	tt.Type("@")
	waitFor(t, tt, func() bool { return tt.HasText("internal/") })

	// 选中目录 internal/ 后按 Enter 进入，草稿仍为 "@"
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool { return tt.HasText("ui/") && tt.HasText("app.go") })
	if s.draft != "@" || s.mentionDir != "internal" {
		t.Fatalf("after enter internal/: draft=%q dir=%q", s.draft, s.mentionDir)
	}

	// 再进入 internal/ui
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool { return tt.HasText("composer.go") })
	if s.mentionDir != "internal/ui" || s.draft != "@" {
		t.Fatalf("after enter internal/ui: draft=%q dir=%q", s.draft, s.mentionDir)
	}

	// 在目录内输入文字，触发 SearchFileEntriesIn(internal/ui, comp)
	tt.Type("comp")
	waitFor(t, tt, func() bool {
		for _, call := range fake.SearchFileCalls() {
			if call.Dir == "internal/ui" && call.Query == "comp" {
				return tt.HasText("composer.go")
			}
		}
		return false
	})
	if tt.HasText("main.go") {
		t.Fatalf("scoped search leaked out-of-dir file main.go: %v", tt.Texts())
	}

	// 退格清空查询回到空查询后，再按 Backspace 返回上一级目录且不删 '@'
	for i := 0; i < len("comp"); i++ {
		tt.Key(0, ui.KeyBackspace)
		tt.Frame()
	}
	if s.draft != "@" {
		t.Fatalf("draft after clearing query = %q, want @", s.draft)
	}
	tt.Key(0, ui.KeyBackspace)
	waitFor(t, tt, func() bool { return tt.HasText("app.go") })
	if s.mentionDir != "internal" || s.draft != "@" {
		t.Fatalf("backspace at empty query must go parent dir: dir=%q draft=%q", s.mentionDir, s.draft)
	}

	// Tab 在目录上直接插入 @internal/ui/ 并关闭菜单（带 Tag）
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if s.draft != "@internal/ui/ " || s.completionKind != completionNone {
		t.Fatalf("Tab on dir: draft=%q kind=%v", s.draft, s.completionKind)
	}
	if ranges := s.mentionRanges(ui.RGB(0, 100, 200)); len(ranges) != 1 || s.draftText.AttributesAt(ranges[0].Start).Tag != "internal/ui/" {
		t.Fatalf("dir Tab must keep Tag: %+v", ranges)
	}

	// 重新触发 @，进入 internal/ui 并在文件上按 Enter 插入完整路径且带 Tag
	tt.Type("@")
	waitFor(t, tt, func() bool { return tt.HasText("internal/") })
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool { return tt.HasText("ui/") })
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool { return tt.HasText("composer.go") })
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	wantDraft := "@internal/ui/ @internal/ui/composer.go "
	if s.draft != wantDraft || s.completionKind != completionNone {
		t.Fatalf("Enter on subdir file: draft=%q want=%q kind=%v", s.draft, wantDraft, s.completionKind)
	}
	if ranges := s.mentionRanges(ui.RGB(0, 100, 200)); len(ranges) != 2 || s.draftText.AttributesAt(ranges[1].Start).Tag != "internal/ui/composer.go" {
		t.Fatalf("subdir file Enter must insert full path Tag: %+v", ranges)
	}
	_ = utf8.RuneCountInString(wantDraft)
}

func TestMentionMenuErrorInPanel(t *testing.T) {
	fake := NewFakeClient()
	fake.SetSearchFilesError(errors.New("permission denied"))
	s, tt := newMentionMenuTester(t, fake, []Thread{{ID: "t1", ProjectID: "p1"}})

	tt.Type("@main")
	waitFor(t, tt, func() bool { return tt.HasText("搜索出错") })
	if !tt.HasText("permission denied") {
		t.Fatalf("error detail missing: %v", tt.Texts())
	}
	if tt.HasText("没有匹配的文件") {
		t.Fatalf("error must not render empty state: %v", tt.Texts())
	}
	s.mu.Lock()
	toast := s.toastMessage
	s.mu.Unlock()
	if toast != "" {
		t.Fatalf("mention error must not set toast, got %q", toast)
	}
}

func mentionMenuMeasuredHeight(t *testing.T, tt *ui.Tester) float32 {
	t.Helper()
	top, okTop := tt.Find("全部")
	bot, okBot := tt.Find("esc 关闭")
	if !okTop || !okBot {
		t.Fatalf("menu bounds not found: top=%v bot=%v texts=%v", okTop, okBot, tt.Texts())
	}
	return (bot.Y + bot.H) - top.Y
}

func TestMentionMenuStableHeight(t *testing.T) {
	fake := NewFakeClient()
	fake.SetFileTree(map[string][]FileEntry{
		"": {
			{Name: "internal", Path: "internal", IsDir: true},
			{Name: "go.mod", Path: "go.mod"},
		},
		"internal": {
			{Name: "a.go", Path: "internal/a.go"},
		},
	})
	fake.SetFileContents(map[string]FileContent{
		"go.mod": {Path: "go.mod", Kind: "text", Text: "module mcode\n\ngo 1.24\n"},
	})
	now := time.Now()
	fake.SetMessages("t2", []Message{
		{Role: "user", Text: "需要修复高度抖动"},
		{Role: "assistant", Text: "已固定面板高度"},
	})
	_, tt := newMentionMenuTester(t, fake, []Thread{
		{ID: "t1", ProjectID: "p1", Title: "当前会话", UpdatedAt: now},
		{ID: "t2", ProjectID: "p1", Title: "历史会话", UpdatedAt: now.Add(-time.Minute)},
	})

	tt.Type("@")
	waitFor(t, tt, func() bool { return tt.HasText("go.mod") && tt.HasText("历史会话") })
	tt.Advance(DurBase + 50*time.Millisecond)
	tt.Frame()

	h0 := mentionMenuMeasuredHeight(t, tt)

	// 在会话、目录、文件之间移动选中项
	for i := 0; i < 2; i++ {
		tt.Key(0, ui.KeyDown)
		tt.Advance(DurBase + 50*time.Millisecond)
		tt.Frame()
		h := mentionMenuMeasuredHeight(t, tt)
		if math.Abs(float64(h-h0)) > 0.5 {
			t.Fatalf("height changed after KeyDown %d: got %.2f, want %.2f", i+1, h, h0)
		}
	}

	// 切换「文件」「会话」「全部」页签
	for i := 0; i < 3; i++ {
		tt.Key(0, ui.KeyRight)
		tt.Advance(DurBase + 50*time.Millisecond)
		tt.Frame()
		h := mentionMenuMeasuredHeight(t, tt)
		if math.Abs(float64(h-h0)) > 0.5 {
			t.Fatalf("height changed after tab switch %d: got %.2f, want %.2f", i+1, h, h0)
		}
	}
}

func TestMentionMenuMotionSettles(t *testing.T) {
	setAppReduceMotion(t, false)
	fake := NewFakeClient()
	fake.SetFileTree(map[string][]FileEntry{
		"": {
			{Name: "internal", Path: "internal", IsDir: true},
		},
		"internal": {
			{Name: "a.go", Path: "internal/a.go"},
		},
	})
	fake.SetFileContents(map[string]FileContent{
		"internal/a.go": {Path: "internal/a.go", Kind: "text", Text: "package internal\n"},
	})
	_, tt := newMentionMenuTester(t, fake, []Thread{{ID: "t1", ProjectID: "p1"}})

	tt.Type("@")
	waitFor(t, tt, func() bool { return tt.HasText("internal/") })
	tt.Advance(DurBase + 50*time.Millisecond)
	tt.Frame()
	if tt.FramePending() {
		t.Fatal("open animation must settle after DurBase")
	}

	// 进入目录后推进动画到结束
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool { return tt.HasText("a.go") })
	tt.Advance(DurSnap + 50*time.Millisecond)
	tt.Frame()
	if tt.FramePending() {
		t.Fatal("directory slide animation must settle and stop requesting frames")
	}

	// 切换页签后推进动画到结束
	tt.Key(0, ui.KeyRight)
	tt.Frame()
	tt.Advance(DurSnap + 50*time.Millisecond)
	tt.Frame()
	if tt.FramePending() {
		t.Fatal("tab indicator animation must settle and stop requesting frames")
	}
}

func TestMentionMenuTabEnterHeaderAndOutOfBounds(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)

	for _, tc := range []struct {
		name     string
		selected int
		key      ui.Key
	}{
		{name: "tab_on_header", selected: 0, key: ui.KeyTab},
		{name: "enter_on_header", selected: 0, key: ui.KeyEnter},
		{name: "tab_out_of_bounds_low", selected: -1, key: ui.KeyTab},
		{name: "enter_out_of_bounds_high", selected: 99, key: ui.KeyEnter},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.mu.Lock()
			s.draft = "@"
			s.completionKind = completionMention
			s.completionStart = 0
			s.completionItems = []completionItem{
				{Label: "文件", Header: true},
				{Label: "main.go", Path: "main.go", Insert: "@main.go"},
			}
			s.completionSelected = tc.selected
			s.completionScrollSelection = false
			s.mu.Unlock()

			newText, handled := s.handleCompletionKey(tc.key, 0)
			if !handled {
				t.Fatalf("expected %v to be handled", tc.key)
			}
			if newText != "@main.go " || s.draft != "@main.go " {
				t.Fatalf("got newText=%q draft=%q, want @main.go ", newText, s.draft)
			}
			if !s.completionScrollSelection {
				t.Fatalf("expected completionScrollSelection=true")
			}
		})
	}
}

type blockingMentionPreviewClient struct {
	*FakeClient
	started chan struct{}
	gate    chan struct{}
}

func (c *blockingMentionPreviewClient) FilesRead(ctx context.Context, threadID, path string, offset, length int64) (FileContent, error) {
	if c.started != nil {
		select {
		case c.started <- struct{}{}:
		default:
		}
	}
	if c.gate != nil {
		select {
		case <-c.gate:
		case <-ctx.Done():
			return FileContent{}, ctx.Err()
		}
	}
	return c.FakeClient.FilesRead(ctx, threadID, path, offset, length)
}

func TestMentionMenuPreviewCacheResets(t *testing.T) {
	t.Run("reload_after_reopen", func(t *testing.T) {
		fake := NewFakeClient()
		fake.SetFileTree(map[string][]FileEntry{
			"": {
				{Name: "main.go", Path: "main.go"},
			},
		})
		fake.SetFileContents(map[string]FileContent{
			"main.go": {Path: "main.go", Kind: "text", Text: "old preview line\n"},
		})
		s, tt := newMentionMenuTester(t, fake, []Thread{{ID: "t1", ProjectID: "p1", Title: "当前会话"}})

		tt.Type("@")
		waitFor(t, tt, func() bool { return tt.HasText("main.go") && tt.HasText("old preview line") })

		fake.SetFileContents(map[string]FileContent{
			"main.go": {Path: "main.go", Kind: "text", Text: "new preview line\n"},
		})

		tt.Key(0, ui.KeyEscape)
		tt.Frame()
		if s.completionKind != completionNone {
			t.Fatalf("expected menu closed after Escape, got %v", s.completionKind)
		}
		tt.Key(0, ui.KeyBackspace)
		tt.Frame()
		tt.Type("@")
		waitFor(t, tt, func() bool { return tt.HasText("main.go") && tt.HasText("new preview line") })
		if tt.HasText("old preview line") {
			t.Fatalf("stale preview rendered after reopen: %v", tt.Texts())
		}
	})

	t.Run("stale_inflight_does_not_overwrite_after_reset", func(t *testing.T) {
		waitInvalidate := func(s *chatState, before uint64) {
			t.Helper()
			deadline := time.Now().Add(2 * time.Second)
			for s.invalidateGen.Load() == before {
				if time.Now().After(deadline) {
					t.Fatal("timed out waiting for preview goroutine invalidate")
				}
				time.Sleep(time.Millisecond)
			}
		}

		// 1) 复位后缓存为 nil，旧请求返回后不得重新创建缓存。
		fake1 := NewFakeClient()
		fake1.SetFileContents(map[string]FileContent{
			"main.go": {Path: "main.go", Kind: "text", Text: "stale nil case\n"},
		})
		client1 := &blockingMentionPreviewClient{
			FakeClient: fake1,
			started:    make(chan struct{}, 1),
			gate:       make(chan struct{}),
		}
		s1 := newChatState(client1)
		t.Cleanup(s1.close)

		s1.mu.Lock()
		s1.completionKind = completionMention
		s1.completionThreadID = "t1"
		s1.completionItems = []completionItem{{Label: "main.go", Path: "main.go", Insert: "@main.go"}}
		s1.completionSelected = 0
		s1.ensureMentionSelectionPreviewLocked()
		s1.mu.Unlock()

		select {
		case <-client1.started:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for FilesRead start")
		}

		s1.mu.Lock()
		s1.resetMentionStateLocked()
		s1.mu.Unlock()

		before1 := s1.invalidateGen.Load()
		close(client1.gate)
		waitInvalidate(s1, before1)

		s1.mu.Lock()
		cacheAfterNilReset := s1.mentionPreviewCache
		s1.mu.Unlock()
		if cacheAfterNilReset != nil {
			t.Fatalf("expected mentionPreviewCache to remain nil after reset, got %v", cacheAfterNilReset)
		}

		// 2) 复位后已有新 entry 占位，旧请求返回不得覆盖新 entry。
		fake2 := NewFakeClient()
		fake2.SetFileContents(map[string]FileContent{
			"main.go": {Path: "main.go", Kind: "text", Text: "stale entry case\n"},
		})
		client2 := &blockingMentionPreviewClient{
			FakeClient: fake2,
			started:    make(chan struct{}, 1),
			gate:       make(chan struct{}),
		}
		s2 := newChatState(client2)
		t.Cleanup(s2.close)

		s2.mu.Lock()
		s2.completionKind = completionMention
		s2.completionThreadID = "t1"
		s2.completionItems = []completionItem{{Label: "main.go", Path: "main.go", Insert: "@main.go"}}
		s2.completionSelected = 0
		s2.ensureMentionSelectionPreviewLocked()
		s2.mu.Unlock()

		select {
		case <-client2.started:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for second FilesRead start")
		}

		key := mentionPreviewCacheKey("t1", "main.go", false)
		newEntry := &mentionItemPreview{loading: true}
		s2.mu.Lock()
		s2.resetMentionStateLocked()
		s2.mentionPreviewCache = map[string]*mentionItemPreview{key: newEntry}
		s2.mu.Unlock()

		before2 := s2.invalidateGen.Load()
		close(client2.gate)
		waitInvalidate(s2, before2)

		s2.mu.Lock()
		got := s2.mentionPreviewCache[key]
		s2.mu.Unlock()
		if got != newEntry {
			t.Fatalf("stale inflight request overwrote new cache entry: got=%+v want=%+v", got, newEntry)
		}
	})
}
