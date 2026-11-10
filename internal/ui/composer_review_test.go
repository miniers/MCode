package mcodeui

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestComposer_EditingCmdReturnSavesWhileRunning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := &fakeQueueUpdaterClient{FakeClient: NewFakeClient()}
	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	fake.TriggerRunningUpdate("t1", true)
	fake.setQueue("t1", []QueuedFollowUp{{ID: "q", Text: "queued"}})
	s.beginEditQueued("q")
	// 编辑排队消息的状态提示落在底栏胶囊上（Label 同样是该文本），卡片顶部不再有独立提示行。
	waitFor(t, tt, func() bool {
		return tt.HasText("保存修改") && tt.HasText("编辑排队消息 · Esc 取消")
	})
	tt.Key(ui.Cmd, ui.KeyEnter)
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.updateCalls) == 1
	})
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.enqueueCalls) != 0 {
		t.Fatal("saving an edited queue item must not enqueue another item")
	}
}

func TestComposer_SwitchEditingItemRestoresOriginalDraft(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	s.selectedThreadID = "t1"
	s.draft = "original draft"
	s.queue["t1"] = []QueuedFollowUp{{ID: "a", Text: "first"}, {ID: "b", Text: "second"}}
	s.beginEditQueued("a")
	s.mu.Lock()
	s.draft, _ = s.consumePendingDraftFillLocked()
	s.mu.Unlock()
	s.beginEditQueued("b")
	s.cancelQueuedEdit()
	s.mu.Lock()
	defer s.mu.Unlock()
	if text, ok := s.consumePendingDraftFillLocked(); !ok || text != "original draft" {
		t.Fatalf("lost original draft: %q, %v", text, ok)
	}
}

type blockedQueueUpdateClient struct {
	*FakeClient
	started, release, finished chan struct{}
}

func (f *blockedQueueUpdateClient) UpdateQueued(ctx context.Context, threadID, followUpID, text string) error {
	close(f.started)
	<-f.release
	close(f.finished)
	return nil
}

func TestComposer_AsyncSaveDoesNotOverwriteNewSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := &blockedQueueUpdateClient{NewFakeClient(), make(chan struct{}), make(chan struct{}), make(chan struct{})}
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.selectedThreadID = "t1"
	s.draft = "old draft"
	s.queue["t1"] = []QueuedFollowUp{{ID: "q", Text: "queued"}}
	s.beginEditQueued("q")
	s.saveQueuedEdit("changed")
	<-fake.started
	s.selectThread("t2")
	s.mu.Lock()
	s.pendingDraftFill = nil
	s.draft = "new session draft"
	s.mu.Unlock()
	close(fake.release)
	<-fake.finished
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		done := !s.sending
		fill, draft := s.pendingDraftFill, s.draft
		s.mu.Unlock()
		if done {
			if fill != nil || draft != "new session draft" {
				t.Fatal("old save overwrote the new session draft")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("save did not finish")
}

func TestComposer_RemovedEditingItemRestoresStash(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.startQueueWatch()
	s.selectedThreadID = "t1"
	s.draft = "draft before editing"
	fake.setQueue("t1", []QueuedFollowUp{{ID: "q", Text: "queued"}})
	s.beginEditQueued("q")
	fake.setQueue("t1", nil)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.editingFollowUpID != "" {
		t.Fatal("removed item is still being edited")
	}
	if text, ok := s.consumePendingDraftFillLocked(); !ok || text != "draft before editing" {
		t.Fatalf("removed queue item leaked into composer: %q", text)
	}
}

func TestComposer_HeroFitsNarrowViewport(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	s.selectedThreadID = "t1"
	for _, width := range []int{480, 800} {
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c.Key("hero-host")).Fill().AlignItems(ui.Center).Children(func() {
				renderComposer(c, s)
			})
		}, width, 500)
		rect, ok := tt.Find("消息输入框")
		wantWidth := min(float32(width-40), composerDockMaxWidth)
		if !ok || rect.X < 0 || rect.X+rect.W > float32(width) || rect.W != wantWidth {
			t.Fatalf("composer must fill available width up to composerDockMaxWidth in centered parent, viewport %d: %+v", width, rect)
		}
	}
}

type composerQuotaClient struct {
	*FakeClient
	calls chan string
}

func (f *composerQuotaClient) ProviderQuota(ctx context.Context, provider string) ([]ProviderQuotaWindow, error) {
	f.calls <- provider
	return []ProviderQuotaWindow{{Label: provider + " quota", UsedPercent: 25}}, nil
}

func (f *composerQuotaClient) ProviderCredits(ctx context.Context, provider string) (ProviderCredits, error) {
	return ProviderCredits{}, ErrNotImplemented
}

func TestComposer_QuotaLoadsFromClientAndChangesProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := &composerQuotaClient{NewFakeClient(), make(chan string, 2)}
	s := newChatState(fake)
	t.Cleanup(s.close)
	th := &Thread{ID: "t1", Provider: "agy"}
	tt := ui.NewTester(func(c *ui.Context) { renderComposerQuotaStrip(c, s, th) }, 400, 400)
	waitFor(t, tt, func() bool {
		windows := getChatQuotaWindowsForProvider(s, "agy")
		return len(windows) == 1 && windows[0].Label == "agy quota"
	})
	if provider := <-fake.calls; provider != "agy" {
		t.Fatalf("unexpected provider: %s", provider)
	}
	th.Provider = "claude"
	waitFor(t, tt, func() bool {
		windows := getChatQuotaWindowsForProvider(s, "claude")
		return len(windows) == 1 && windows[0].Label == "claude quota"
	})
	if provider := <-fake.calls; provider != "claude" {
		t.Fatalf("unexpected provider: %s", provider)
	}
}

type queueAttachmentsClient struct {
	*fakeQueueUpdaterClient
	savedAttachments []string
}

func (f *queueAttachmentsClient) QueuedAttachments(ctx context.Context, threadID, followUpID string) ([]Attachment, error) {
	return []Attachment{{ID: "queued-image", Name: "queued.png", MimeType: "image/png", ByteCount: 10}}, nil
}

func (f *queueAttachmentsClient) UpdateQueuedWithAttachments(ctx context.Context, threadID, followUpID, text string, ids []string) error {
	f.mu.Lock()
	f.savedAttachments = append([]string{}, ids...)
	f.mu.Unlock()
	return f.UpdateQueued(ctx, threadID, followUpID, text)
}

func TestComposer_QueueAttachmentEditingRestoresDraftAttachments(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := &queueAttachmentsClient{fakeQueueUpdaterClient: &fakeQueueUpdaterClient{FakeClient: NewFakeClient()}}
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.selectedThreadID = "t1"
	s.draft = "draft before edit"
	s.attachments = []composerAttachment{{ID: "draft-image", LocalID: "original", Name: "draft.png"}}
	s.queue["t1"] = []QueuedFollowUp{{ID: "q", Text: "queued", AttachmentCount: 1}}
	s.beginEditQueued("q")
	tt := ui.NewTester(func(c *ui.Context) { renderComposer(c, s) }, 800, 500)
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.sending
	})
	s.mu.Lock()
	attachments := append([]composerAttachment(nil), s.attachments...)
	s.mu.Unlock()
	if len(attachments) != 1 || attachments[0].ID != "queued-image" {
		t.Fatalf("queued attachments were not filled: %+v", attachments)
	}
	localID := attachments[0].LocalID
	s.removeAttachment(localID)
	s.saveQueuedEdit("changed")
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.sending && s.pendingDraftFill == nil
	})
	s.mu.Lock()
	draft := s.draft
	attachments = append([]composerAttachment(nil), s.attachments...)
	s.mu.Unlock()
	if draft != "draft before edit" || len(attachments) != 1 || attachments[0].ID != "draft-image" {
		t.Fatalf("draft or original attachments lost: %q %+v", draft, attachments)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.savedAttachments == nil || len(fake.savedAttachments) != 0 || len(fake.updateCalls) != 1 {
		t.Fatal("removing all queued attachments must save an explicit empty attachment list")
	}
}
