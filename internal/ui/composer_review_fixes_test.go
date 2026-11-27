package mcodeui

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestSendFailureRestoresThreadMentions(t *testing.T) {
	fake := NewFakeClient()
	fake.mu.Lock()
	fake.sendErr = errors.New("send failed")
	fake.mu.Unlock()

	s := newChatState(fake)
	s.selectedThreadID = "t1"
	s.threads = []Thread{
		{ID: "t1", ProjectID: "p1", Title: "当前会话"},
		{ID: "t2", ProjectID: "p1", Title: "会话B"},
	}
	fake.SetMessages("t2", []Message{{Role: "user", Text: "历史上下文"}})

	want := []ThreadMention{{ThreadID: "t2", Title: "会话B"}}
	s.mu.Lock()
	s.draftThreadMentions = append([]ThreadMention(nil), want...)
	s.mu.Unlock()

	s.sendMessage("请参考 @会话B")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		sending := s.sending
		s.mu.Unlock()
		if fake.SendCalls() == 1 && !sending {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	s.mu.Lock()
	got := append([]ThreadMention(nil), s.draftThreadMentions...)
	sending := s.sending
	s.mu.Unlock()

	if sending {
		t.Fatal("expected sending to be false after failure")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("draftThreadMentions = %+v, want %+v", got, want)
	}
}

type blockingHistoryFakeClient struct {
	*FakeClient
	mu           sync.Mutex
	callNum      int
	firstStarted chan struct{}
	releaseFirst chan struct{}
	firstDone    chan struct{}
	firstMsgs    []Message
	secondMsgs   []Message
}

func (c *blockingHistoryFakeClient) History(ctx context.Context, threadID string) ([]Message, error) {
	c.mu.Lock()
	c.callNum++
	num := c.callNum
	c.mu.Unlock()

	if num == 1 {
		close(c.firstStarted)
		<-c.releaseFirst
		defer close(c.firstDone)
		return append([]Message(nil), c.firstMsgs...), nil
	}
	return append([]Message(nil), c.secondMsgs...), nil
}

func TestResyncStaleHistoryIgnored(t *testing.T) {
	client := &blockingHistoryFakeClient{
		FakeClient:   NewFakeClient(),
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
		firstDone:    make(chan struct{}),
		firstMsgs:    []Message{{ID: "m-old", Role: "assistant", Kind: "text", Text: "old resync", TurnIndex: 1}},
		secondMsgs:   []Message{{ID: "m-new", Role: "assistant", Kind: "text", Text: "new resync", TurnIndex: 2}},
	}

	s := newChatState(client)
	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1", ProjectID: "p1", Title: "会话1"}}
	s.mu.Unlock()

	s.reloadMessagesAfterResync("t1")
	<-client.firstStarted

	s.reloadMessagesAfterResync("t1")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		n := len(s.messages)
		id := ""
		if n > 0 {
			id = s.messages[0].ID
		}
		s.mu.Unlock()
		if n == 1 && id == "m-new" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	s.mu.Lock()
	revAfterSecond := s.messagesRev
	s.mu.Unlock()

	close(client.releaseFirst)
	<-client.firstDone
	time.Sleep(25 * time.Millisecond)

	s.mu.Lock()
	got := append([]Message(nil), s.messages...)
	revFinal := s.messagesRev
	s.mu.Unlock()

	if len(got) != 1 || got[0].ID != "m-new" || got[0].Text != "new resync" {
		t.Fatalf("stale history overwrote newer messages: got %+v", got)
	}
	if revFinal != revAfterSecond {
		t.Fatalf("messagesRev changed after stale resync: got %d, want %d", revFinal, revAfterSecond)
	}
}

func TestComposerEscWhileComposing(t *testing.T) {
	t.Run("running_turn_not_interrupted", func(t *testing.T) {
		fake := NewFakeClient()
		s := newChatState(fake)
		s.selectedThreadID = "t1"
		s.threads = []Thread{{ID: "t1", ProjectID: "p1", Title: "当前会话", IsRunning: true}}

		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c).Fill().Children(func() { renderComposer(c, s) })
		}, 800, 600)
		tt.Frame()
		tt.Frame()

		// 直接置 s.isComposing 验证 OnTextKey 回调内的组字守卫。
		s.isComposing = true
		tt.Key(0, ui.KeyEscape)
		if got := fake.InterruptCalls(); got != 0 {
			t.Fatalf("InterruptCalls = %d while s.isComposing=true, want 0", got)
		}

		// 同时验证输入法实际处于组字态时的 Esc 行为。
		s.isComposing = false
		tt.Frame()
		tt.Compose("ni", 2)
		tt.Key(0, ui.KeyEscape)
		if got := fake.InterruptCalls(); got != 0 {
			t.Fatalf("InterruptCalls = %d while composing, want 0", got)
		}

		// 结束组字后 Esc 正常中断当前回合。
		tt.Compose("", 0)
		tt.Frame()
		tt.Key(0, ui.KeyEscape)
		waitFor(t, tt, func() bool { return fake.InterruptCalls() == 1 })
	})

	t.Run("queued_edit_not_cancelled", func(t *testing.T) {
		fake := NewFakeClient()
		s := newChatState(fake)
		s.selectedThreadID = "t1"
		s.threads = []Thread{{ID: "t1", ProjectID: "p1", Title: "当前会话", IsRunning: true}}
		s.editingFollowUpID = "q1"
		s.editingFollowUpThread = "t1"

		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c).Fill().Children(func() { renderComposer(c, s) })
		}, 800, 600)
		tt.Frame()
		tt.Frame()

		s.isComposing = true
		tt.Key(0, ui.KeyEscape)
		if got := s.editingQueuedID(); got != "q1" {
			t.Fatalf("editingQueuedID = %q while composing, want %q", got, "q1")
		}

		s.isComposing = false
		tt.Frame()
		tt.Key(0, ui.KeyEscape)
		if got := s.editingQueuedID(); got != "" {
			t.Fatalf("editingQueuedID = %q after Escape, want empty", got)
		}
	})
}

func TestChatHeaderEnterWhileComposing(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)
	th := Thread{ID: "t1", ProjectID: "p1", Title: "原标题"}
	s.selectedThreadID = "t1"
	s.threads = []Thread{th}

	hs := getChatHeaderState(s)
	chatHeaderMu.Lock()
	hs.editing = true
	hs.editThreadID = "t1"
	hs.editOrig = "原标题"
	hs.editTitle = "新标题"
	chatHeaderMu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Row(c).Fill().Children(func() { renderChatHeaderTitle(c, s, th) })
	}, 600, 120)
	tt.Frame()
	tt.Frame()

	s.isComposing = true
	tt.Key(0, ui.KeyEnter)
	if calls := renameCallsOf(fake); len(calls) != 0 {
		t.Fatalf("RenameThread called on Enter while s.isComposing=true: %+v", calls)
	}
	chatHeaderMu.Lock()
	editingAfterEnter := hs.editing
	chatHeaderMu.Unlock()
	if !editingAfterEnter {
		t.Fatal("header editing ended on Enter while composing")
	}

	tt.Key(0, ui.KeyEscape)
	chatHeaderMu.Lock()
	editingAfterEsc := hs.editing
	chatHeaderMu.Unlock()
	if !editingAfterEsc {
		t.Fatal("header editing cancelled on Escape while composing")
	}

	s.isComposing = false
	tt.Frame()
	tt.Compose("新", 1)
	tt.Key(0, ui.KeyEnter)
	if calls := renameCallsOf(fake); len(calls) != 0 {
		t.Fatalf("RenameThread called on Enter while IME composing: %+v", calls)
	}

	tt.Compose("", 0)
	tt.Type("新标题")
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool {
		calls := renameCallsOf(fake)
		return len(calls) == 1 && calls[0].Title == "新标题"
	})
}

func TestChatFindEnterWhileComposing(t *testing.T) {
	msgs := []Message{
		{ID: "m1", Role: "user", Text: "first keyword", TurnIndex: 1},
		{ID: "m2", Role: "assistant", Kind: "text", Text: "second keyword", TurnIndex: 2, IsFinal: true},
	}
	items := buildTimelineItems(msgs)
	s := newChatState(NewFakeClient())
	f := getChatFindState(s)
	f.query = "keyword"

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("host")).Fill().Children(func() {
			renderChatFindBar(c, s, "t1", 1, msgs, items)
		})
	}, 600, 200)

	s.openChatFind()
	tt.Frame()
	tt.Frame()
	if f.cursor != 0 || len(f.matches) != 2 {
		t.Fatalf("initial find state: cursor=%d matches=%v, want 0 and 2 matches", f.cursor, f.matches)
	}

	s.isComposing = true
	tt.Key(0, ui.KeyEnter)
	if f.cursor != 0 {
		t.Fatalf("cursor jumped to %d on Enter while s.isComposing=true, want 0", f.cursor)
	}

	tt.Key(0, ui.KeyEscape)
	if !f.open {
		t.Fatal("chat find closed on Escape while s.isComposing=true")
	}

	s.isComposing = false
	tt.Frame()
	tt.Compose("key", 3)
	tt.Key(0, ui.KeyEnter)
	if f.cursor != 0 {
		t.Fatalf("cursor jumped to %d on Enter while IME composing, want 0", f.cursor)
	}

	tt.Compose("", 0)
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	if f.cursor != 1 {
		t.Fatalf("cursor = %d after Enter once committed, want 1", f.cursor)
	}
}
