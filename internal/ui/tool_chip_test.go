package mcodeui

import (
	"bytes"
	"fmt"
	"image"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func finishUnfold(tt *ui.Tester) {
	tt.Advance(motionUnfold + 40*time.Millisecond)
	tt.Frame()
}

func TestConversationArrowsAnimate(t *testing.T) {
	setAppReduceMotion(t, false)
	for _, name := range []string{"group", "queue"} {
		t.Run(name, func(t *testing.T) {
			s := newChatState(NewFakeClient())
			s.embeddedBee = true
			view := func(c *ui.Context) {
				if name == "group" {
					renderToolGroupItem(c, s, []Message{{ID: "tool", ToolName: "read", ToolStatus: "completed"}}, "group", "", false)
				} else {
					renderComposerTabSlot(c, s, "thread", []QueuedFollowUp{{ID: "item", Text: "排队内容"}}, false, nil)
				}
			}
			tt := ui.NewTester(view, 800, 600)
			tt.Frame()
			if name == "group" {
				s.setToolGroupOpen("group", true)
			} else {
				toggleQueueExpanded(s, "thread")
			}
			header := func() []byte {
				img := tt.Image()
				return append([]byte(nil), img.SubImage(image.Rect(0, 0, 800, 24)).(*image.RGBA).Pix...)
			}
			tt.Frame()
			start := header()
			tt.Advance(80 * time.Millisecond)
			middle := header()
			tt.Advance(2 * time.Second)
			end := header()
			fresh := ui.NewTester(view, 800, 600)
			fresh.Frame()
			want := fresh.Image().SubImage(image.Rect(0, 0, 800, 24)).(*image.RGBA).Pix
			if bytes.Equal(start, middle) || bytes.Equal(middle, end) || !bytes.Equal(end, want) {
				t.Fatal("箭头应逐步旋转并到达展开角度")
			}
		})
	}
}

func TestChatOverlaysReducedMotion(t *testing.T) {
	setAppReduceMotion(t, true)
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	options := ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}}
	t.Run("compact", func(t *testing.T) {
		s := newChatState(NewFakeClient())
		s.threads = []Thread{{ID: "t1", UpdatedAt: time.Now().Add(-2 * time.Hour)}}
		s.threadUsage = ThreadUsage{Turns: map[int]TokenUsage{1: {Input: 180_000, ContextWindow: 200_000}}}
		show := false
		tt := ui.NewTesterWith(func(c *ui.Context) {
			if show {
				renderCompactSuggestionCard(c, s, "t1")
			}
		}, 800, 600, options)
		tt.Frame()
		show = true
		tt.Frame()
		if !tt.HasText("这个会话较长，继续之前要先压缩上下文吗？") || tt.FramePending() {
			t.Fatal("减少动效时压缩建议应立即出现且不请求帧")
		}
	})
	t.Run("jump", func(t *testing.T) {
		fake := NewFakeClient()
		var msgs []Message
		for i := 0; i < 80; i++ {
			msgs = append(msgs, Message{ID: fmt.Sprintf("u%02d", i), Role: "user", Text: fmt.Sprintf("消息-%02d", i), TurnIndex: i + 1, IsFinal: true})
		}
		fake.SetMessages("t1", msgs)
		s, view := buildRoot(fake, WithInitialThread("t1"))
		t.Cleanup(s.close)
		tt := ui.NewTesterWith(view, 1000, 700, options)
		waitFor(t, tt, func() bool { return tt.HasText("消息-79") })
		tt.Scroll(600, 300, 0, -3000)
		tt.Frame()
		if !tt.HasText("跳到最新") || tt.FramePending() {
			t.Fatal("减少动效时跳到最新应立即出现且不请求帧")
		}
	})
}

func TestConversationArrowsReducedMotion(t *testing.T) {
	setAppReduceMotion(t, true)
	options := ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}}
	for _, name := range []string{"group", "queue"} {
		t.Run(name, func(t *testing.T) {
			s := newChatState(NewFakeClient())
			s.embeddedBee = true
			view := func(c *ui.Context) {
				if name == "group" {
					renderToolGroupItem(c, s, []Message{{ID: "tool", ToolName: "read", ToolStatus: "completed"}}, "group", "", false)
				} else {
					renderComposerTabSlot(c, s, "thread", []QueuedFollowUp{{ID: "item", Text: "排队内容"}}, false, nil)
				}
			}
			tester := ui.NewTesterWith(view, 800, 600, options)
			tester.Frame()
			if name == "group" {
				s.setToolGroupOpen("group", true)
			} else {
				toggleQueueExpanded(s, "thread")
			}
			tester.Frame()
			fresh := ui.NewTesterWith(view, 800, 600, options)
			fresh.Frame()
			if !bytes.Equal(tester.Image().Pix, fresh.Image().Pix) || tester.FramePending() {
				t.Fatal("减少动效时展开应立即到达终态且不请求帧")
			}
		})
	}
}

func TestToolDetailUnfoldMotion(t *testing.T) {
	setAppReduceMotion(t, false)
	s := newChatState(NewFakeClient())
	m := Message{ID: "motion-tool", Role: "tool", ToolName: "read", ToolStatus: "completed", ToolInput: `{"path":"motion-detail-file"}`}
	tt := ui.NewTester(func(c *ui.Context) { renderSingleToolItem(c, s, m) }, 800, 600)
	tt.Frame()
	s.toggleToolChip(m, []Message{m})
	tt.Frame()
	tt.Advance(motionUnfold / 2)
	if !tt.FramePending() {
		t.Fatal("展开时应请求动画帧")
	}
	tt.Advance(motionUnfold + time.Millisecond)
	s.toggleToolChip(m, []Message{m})
	tt.Frame()
	if !tt.FramePending() {
		t.Fatal("收起时应请求动画帧")
	}
}

func TestChatFindReducedMotion(t *testing.T) {
	setAppReduceMotion(t, true)
	s := newChatState(NewFakeClient())
	tester := ui.NewTesterWith(func(c *ui.Context) {
		renderChatFindBar(c, s, "thread", 0, nil, nil)
	}, 800, 600, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	tester.Frame()
	s.openChatFind()
	tester.Frame()
	if tester.FramePending() {
		t.Fatal("减少动效时查找条出现不应请求动画帧")
	}
	s.closeChatFind()
	tester.Frame()
	if tester.FramePending() {
		t.Fatal("减少动效时查找条消失不应请求动画帧")
	}
}

func TestToolMessageState(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   ToolState
	}{
		{"pending", ToolPending}, {"running", ToolRunning}, {"background", ToolRunning},
		{"completed", ToolDone}, {"failed", ToolError}, {"blocked", ToolError},
	} {
		if got := toolMessageState(Message{ToolStatus: tc.status}); got != tc.want {
			t.Errorf("状态 %q = %v，期望 %v", tc.status, got, tc.want)
		}
	}
}

func TestPendingToolInputClosesOnCompletion(t *testing.T) {
	s := newChatState(NewFakeClient())
	m := Message{ID: "pending-input", Role: "tool", ToolName: "read", ToolStatus: "pending", ToolInput: `{"path":"unique-pending-file"}`}
	tt := ui.NewTester(func(c *ui.Context) { renderSingleToolItem(c, s, m) }, 800, 600)
	if !tt.HasText("unique-pending-file") {
		t.Fatal("等待时应显示入参")
	}
	m.ToolStatus = "completed"
	tt = ui.NewTester(func(c *ui.Context) { renderSingleToolItem(c, s, m) }, 800, 600)
	if tt.HasText(m.ToolInput) {
		t.Fatal("完成后应收起入参")
	}
	s.toggleToolChip(m, []Message{m})
	if !s.toolMessageExpanded(m) {
		t.Fatal("手动展开应保留")
	}
}
