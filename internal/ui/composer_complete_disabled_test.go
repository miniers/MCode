package mcodeui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 场景 A7：@ 面板打开期间会话被删除，失效行显示原因且键盘/回车都插不进去。
func TestComposerCompletionDisabledThreadMention(t *testing.T) {
	s := newChatState(NewFakeClient())
	now := time.Now()
	tt := composerTester(s)
	// composerTester 会把 threads 重置成只有当前会话，这里补上第二个会话。
	s.mu.Lock()
	s.threads = []Thread{
		{ID: "t1"},
		{ID: "t2", Title: "第二个会话", UpdatedAt: now},
	}
	s.mu.Unlock()

	tt.Type("@")
	// 等文件搜索返回并且会话候选项已经渲染出来，再模拟面板打开期间的删除。
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		loaded := s.completionLoaded && !s.completionLoading
		s.mu.Unlock()
		return loaded && tt.HasText("第二个会话")
	})

	s.mu.Lock()
	s.threads = []Thread{{ID: "t1"}}
	badIdx := -1
	for i, it := range s.completionItems {
		if it.ThreadID == "t2" {
			badIdx = i
		}
	}
	items := append([]completionItem(nil), s.completionItems...)
	s.mu.Unlock()
	s.invalidate()
	tt.Frame()
	if badIdx < 0 {
		t.Fatalf("快照里应保留失效会话，items=%+v", items)
	}

	// 失效行可见：右侧原因与行 Label 都带说明。多个失效会话时 Label 会重复，因此只用 HasText。
	if !tt.HasText("该会话已不存在") {
		t.Fatal("失效会话没有显示原因")
	}
	if !tt.HasText("会话引用已失效 · 该会话已不存在") {
		t.Fatal("失效行的 Label 没有带上原因")
	}

	// 上下键不会停在失效行。
	for i := 0; i < 4; i++ {
		tt.Key(0, ui.KeyDown)
		tt.Frame()
		s.mu.Lock()
		selected := s.completionSelected
		s.mu.Unlock()
		if selected == badIdx {
			t.Fatalf("Down 停在了失效行 %d", badIdx)
		}
	}

	// 回车不会把失效会话插进草稿。
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	s.mu.Lock()
	mentions := append([]ThreadMention(nil), s.draftThreadMentions...)
	draft := s.draft
	s.mu.Unlock()
	for _, m := range mentions {
		if m.ThreadID == "t2" {
			t.Fatalf("失效会话被插入: %+v", m)
		}
	}
	if strings.Contains(draft, "第二个会话") {
		t.Fatalf("草稿里出现了失效会话引用: %q", draft)
	}
}
