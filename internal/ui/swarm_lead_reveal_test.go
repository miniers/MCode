package mcodeui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

func newLeadRevealState() *chatState {
	s := newChatState(NewFakeClient())
	s.selectedThreadID = "lead"
	s.threads = []Thread{{ID: "lead", IsRunning: true}}
	s.markRevealBaselineLocked(nil)
	return s
}

func setLeadRevealText(s *chatState, text string) {
	s.mu.Lock()
	if len(s.messages) == 0 {
		s.messages = []Message{{ID: "lead-answer", Role: "assistant", Kind: "text", TurnIndex: 1}}
	}
	s.messages[0].Text = text
	s.messagesRev++
	s.mu.Unlock()
}

func leadTexts(tt *ui.Tester) string {
	return strings.Join(tt.Texts(), " ")
}

func TestSwarmLeadRevealFinishesWhileBeeRuns(t *testing.T) {
	s := newLeadRevealState()
	tt := ui.NewTester(func(c *ui.Context) { renderChat(c, s) }, 900, 700)
	setLeadRevealText(s, "开始处理，")
	tt.Frame()
	tt.Advance(3 * time.Second)
	if !strings.Contains(leadTexts(tt), streamingCursor) {
		t.Fatalf("主线程运行时应保留光标：%v", tt.Texts())
	}
	setLeadRevealText(s, "开始处理，使用 `inline` 完成最后半句")
	tt.Frame()
	setLeadRevealText(s, "开始处理，使用 `inline` 完成最后半句\n```swarm\n{\"task\":\"检查\"}")
	tt.Frame()
	setLeadRevealText(s, "开始处理，使用 `inline` 完成最后半句\n```swarm\n{\"task\":\"检查\"}\n```")
	tt.Frame()
	// 模拟上游缩短派单块，并在闭合块后追加可见正文。
	setLeadRevealText(s, "开始处理，使用 `inline` 完成最后半句\n```swarm\n{}\n```\n追加正文")
	tt.Frame()

	s.mu.Lock()
	s.threads[0].IsRunning = false
	s.threads[0].SwarmBusy = true
	s.mu.Unlock()
	for i := 0; i < 8; i++ {
		tt.Advance(500 * time.Millisecond)
	}
	tt.Frame()
	text := leadTexts(tt)
	for _, part := range []string{"开始处理", "inline", "完成最后半句", "追加正文"} {
		if !strings.Contains(text, part) {
			t.Fatalf("结束后缺少正文 %q：%v", part, tt.Texts())
		}
	}
	if strings.Contains(text, streamingCursor) {
		t.Fatalf("主线程结束后不应有光标：%v", tt.Texts())
	}
	if !s.assistantRevealed("lead-answer") {
		t.Fatal("正文应完全揭示")
	}
	if !strings.Contains(text, "复制") {
		t.Fatalf("正文完成后操作栏应恢复：%v", tt.Texts())
	}
}

func TestSwarmLeadReduceMotionAndHistory(t *testing.T) {
	setAppReduceMotion(t, true)
	s := newLeadRevealState()
	visible := true
	tt := ui.NewTester(func(c *ui.Context) {
		if visible {
			renderChat(c, s)
		}
	}, 900, 700)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
	text := "减少动效时首帧显示完整正文"
	setLeadRevealText(s, text)
	tt.Frame()
	if !strings.Contains(leadTexts(tt), text) {
		t.Fatalf("减少动效时正文应立即显示：%v", tt.Texts())
	}
	if s.mdOpts.Reveal.Unit != markdown.AtOnce || s.mdOpts.Reveal.Fade != -1 {
		t.Fatal("减少动效时应立即揭示且关闭淡入")
	}
	appReduceMotion.Store(false)
	tt.SetPreferences(ui.Preferences{TextScale: 1})
	tt.Frame()
	if s.mdOpts.Reveal.Unit != markdown.ByCharacter {
		t.Fatal("恢复动效后应恢复逐字揭示")
	}

	visible = false
	tt.Frame()
	s.resetRevealBaseline()
	visible = true
	tt.Frame()
	if !strings.Contains(leadTexts(tt), text) {
		t.Fatalf("视图重建后不应重播：%v", tt.Texts())
	}
}

func TestSwarmLeadHistoryBaselineUsesVisibleText(t *testing.T) {
	s := newLeadRevealState()
	history := "历史正文\n```swarm\n{\"task\":\"检查\"}\n```"
	setLeadRevealText(s, history)
	s.mu.Lock()
	s.markRevealBaselineLocked(s.messages)
	s.mu.Unlock()
	tt := ui.NewTester(func(c *ui.Context) { renderChat(c, s) }, 900, 700)
	if !strings.Contains(leadTexts(tt), "历史正文") {
		t.Fatalf("历史正文应立即显示：%v", tt.Texts())
	}
	setLeadRevealText(s, history+"\n新正文")
	tt.Frame()
	s.mu.Lock()
	progress := s.revealProgress["lead-answer"]
	streaming := progress != nil && progress.streaming
	s.mu.Unlock()
	if !streaming {
		t.Fatal("围栏后的新增可见正文应进入揭示阶段")
	}
}
