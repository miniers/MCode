package mcodeui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"mcode/internal/openartifact"
)

func newOpenArtifactComposerClient() *fakeOpenArtifactClient {
	fake := NewFakeClient()
	fake.SetCommands([]SlashCommand{
		{Name: "plan", Detail: "制定计划", Kind: "command"},
		{Name: "open-artifact", Detail: "创建交付物", Kind: "command"},
	})
	return &fakeOpenArtifactClient{
		FakeClient: fake,
		artifactTypes: []openartifact.ArtifactType{
			{ID: "data", Name: "Data (dashboards and reports)"},
			{ID: "slides", Name: "Slides (presentation deck)"},
			{ID: "whiteboard", Name: "Whiteboard (shared board)"},
		},
	}
}

func TestComposerOpenArtifactGrid(t *testing.T) {
	setAppReduceMotion(t, true)
	client := newOpenArtifactComposerClient()
	s := newChatState(client)
	tt := composerTester(s)

	// 1. 输入 /交付物 能命中 /open-artifact 及其中文别名提示
	tt.Type("/交付物")
	waitFor(t, tt, func() bool { return tt.HasText("/open-artifact") })
	if !tt.HasText("创建交付物（别名 /交付物）") {
		t.Fatalf("未展示创建交付物详情与别名提示，Texts=%v", tt.Texts())
	}

	// 2. 回车确认进入二级类型网格，且草稿保持原状
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	s.mu.Lock()
	inGrid := s.completionArtifactGrid
	draftInGrid := s.draft
	s.mu.Unlock()
	if !inGrid {
		t.Fatal("确认 /open-artifact 后未进入二级类型网格")
	}
	if draftInGrid != "/交付物" {
		t.Fatalf("进入网格时不应修改草稿，实际 = %q", draftInGrid)
	}
	for _, want := range []string{"看板", "幻灯片", "白板", "不指定类型，直接写需求"} {
		if !tt.HasText(want) {
			t.Fatalf("类型网格缺少 %q，Texts=%v", want, tt.Texts())
		}
	}

	// 3. Backspace 在网格内返回一级补全列表
	tt.Key(0, ui.KeyBackspace)
	tt.Frame()
	s.mu.Lock()
	backToList := !s.completionArtifactGrid && s.completionKind == completionSlash
	draftAfterBack := s.draft
	s.mu.Unlock()
	if !backToList || draftAfterBack != "/交付物" {
		t.Fatalf("网格内按 Backspace 未返回一级列表: back=%v draft=%q", backToList, draftAfterBack)
	}
	if !tt.HasText("/open-artifact") {
		t.Fatalf("返回一级列表后未重新显示 /open-artifact，Texts=%v", tt.Texts())
	}

	// 4. 再次进入网格，方向键右移选 slides 并回车插入带 Tag 的中文胶囊「幻灯片」
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.Key(0, ui.KeyRight)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if s.draft != "幻灯片 " {
		t.Fatalf("选择 slides 后草稿 = %q, want %q", s.draft, "幻灯片 ")
	}
	ranges := s.mentionRanges(ui.RGB(0, 100, 200))
	if len(ranges) != 1 {
		t.Fatalf("选择 slides 后 Tag 数量 = %d, want 1", len(ranges))
	}
	wantTagLen := utf8.RuneCountInString("幻灯片")
	if ranges[0].Start != 0 || ranges[0].End != wantTagLen {
		t.Fatalf("slides Tag 区间 = %+v, want [0,%d)", ranges[0], wantTagLen)
	}
	if gotTag := s.draftText.AttributesAt(ranges[0].Start).Tag; gotTag != "open-artifact slides" {
		t.Fatalf("slides Tag 值 = %q, want %q", gotTag, "open-artifact slides")
	}

	// 5. 清空草稿重新输入 /交付物，选「不指定类型，直接写需求」得到「交付物 」
	s.clearDraftAndCompletion()
	tt.Frame()
	tt.Type("/交付物")
	waitFor(t, tt, func() bool { return tt.HasText("/open-artifact") })
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if s.draft != "交付物 " {
		t.Fatalf("选择不指定类型后草稿 = %q, want %q", s.draft, "交付物 ")
	}
	ranges = s.mentionRanges(ui.RGB(0, 100, 200))
	if len(ranges) != 1 || ranges[0].Start != 0 || ranges[0].End != utf8.RuneCountInString("交付物") {
		t.Fatalf("不指定类型 Tag 区间 = %+v", ranges)
	}
	if gotTag := s.draftText.AttributesAt(ranges[0].Start).Tag; gotTag != "open-artifact" {
		t.Fatalf("不指定类型 Tag 值 = %q, want %q", gotTag, "open-artifact")
	}

	// 6. 删掉尾部空格后在胶囊末尾按退格整段删除胶囊
	tt.Key(0, ui.KeyBackspace)
	tt.Frame()
	if s.draft != "交付物" {
		t.Fatalf("首次退格应只删掉尾部空格，实际 = %q", s.draft)
	}
	tt.Key(0, ui.KeyBackspace)
	tt.Frame()
	if s.draft != "" {
		t.Fatalf("退格未整段删除交付物胶囊，实际 = %q", s.draft)
	}
	if got := s.mentionRanges(ui.RGB(0, 100, 200)); len(got) != 0 {
		t.Fatalf("整段删除后不应残留 Tag: %+v", got)
	}
}

func TestComposerOpenArtifactChipExpandsOnSend(t *testing.T) {
	setAppReduceMotion(t, true)
	client := newOpenArtifactComposerClient()
	s := newChatState(client)
	t.Cleanup(s.close)
	tt := composerTester(s)

	// 1. 选 slides 胶囊后接着输入描述并发送，收到展开后的命令文本且草稿清空
	tt.Type("/交付物")
	waitFor(t, tt, func() bool { return tt.HasText("/open-artifact") })
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.Key(0, ui.KeyRight)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.Type("做季度汇报")
	tt.Frame()

	if err := tt.Click("发送"); err != nil {
		t.Fatalf("点击发送失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		cleared := s.draft == "" && s.sentDraft == nil && !s.sending
		s.mu.Unlock()
		return client.SendCalls() == 1 && cleared
	})
	if got := client.LastSentText(); got != "/open-artifact slides 做季度汇报" {
		t.Fatalf("发送文本 = %q, want %q", got, "/open-artifact slides 做季度汇报")
	}

	// 2. 回合结束后发送普通文本（无胶囊），发送保持原样
	s.mu.Lock()
	s.markThreadIdleLocked("t1")
	s.mu.Unlock()
	tt.Frame()

	if err := tt.Click("消息输入框"); err != nil {
		t.Fatalf("聚焦消息输入框失败: %v", err)
	}
	tt.Type("普通消息内容")
	tt.Frame()
	if err := tt.Click("发送"); err != nil {
		t.Fatalf("点击发送普通文本失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		cleared := s.draft == "" && s.sentDraft == nil && !s.sending
		s.mu.Unlock()
		return client.SendCalls() == 2 && cleared
	})
	if got := client.LastSentText(); got != "普通消息内容" {
		t.Fatalf("普通文本发送 = %q, want %q", got, "普通消息内容")
	}
}

func TestComposerOpenArtifactChipExpandsOnEnqueue(t *testing.T) {
	setAppReduceMotion(t, true)
	client := newOpenArtifactComposerClient()
	s := newChatState(client)
	t.Cleanup(s.close)
	tt := composerTester(s)

	// 让会话处于运行中，经网格选 slides 并输入描述后触发排队，确认入队文本已展开胶囊。
	s.mu.Lock()
	s.markThreadRunningOptimisticallyLocked("t1")
	s.mu.Unlock()
	tt.Frame()

	tt.Type("/交付物")
	waitFor(t, tt, func() bool { return tt.HasText("/open-artifact") })
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.Key(0, ui.KeyRight)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.Type("做季度汇报")
	tt.Frame()

	s.mu.Lock()
	draft := strings.TrimSpace(s.draft)
	s.mu.Unlock()
	s.enqueueMessage(draft)

	waitFor(t, tt, func() bool {
		client.mu.Lock()
		enqueueCount := len(client.enqueueCalls)
		client.mu.Unlock()
		s.mu.Lock()
		cleared := s.draft == "" && s.sentDraft == nil && !s.sending
		s.mu.Unlock()
		return enqueueCount == 1 && cleared
	})

	client.mu.Lock()
	got := client.enqueueCalls[0].Text
	client.mu.Unlock()
	if got != "/open-artifact slides 做季度汇报" {
		t.Fatalf("入队文本 = %q, want %q", got, "/open-artifact slides 做季度汇报")
	}
}

func TestComposerOpenArtifactGridAnimationSettles(t *testing.T) {
	t.Run("normal", func(t *testing.T) {
		setAppReduceMotion(t, false)
		s := newChatState(newOpenArtifactComposerClient())
		tt := composerTester(s)

		tt.Type("/交付物")
		waitFor(t, tt, func() bool { return tt.HasText("/open-artifact") })
		tt.Advance(DurBase + 50*time.Millisecond)
		tt.Frame()

		tt.Key(0, ui.KeyEnter)
		tt.Frame()
		if !tt.FramePending() {
			t.Fatal("进入类型网格滑入动画期间应请求下一帧")
		}

		tt.Advance(DurFast + DurBase + 50*time.Millisecond)
		tt.Frame()
		if tt.FramePending() {
			t.Fatal("滑入动画结束后不应继续请求下一帧")
		}

		tt.Key(0, ui.KeyEscape)
		tt.Advance(DurFast + 50*time.Millisecond)
		tt.Frame()
		if tt.FramePending() {
			t.Fatal("Esc 收起网格动画结束后不应继续请求下一帧")
		}
	})

	t.Run("reduceMotion", func(t *testing.T) {
		setAppReduceMotion(t, true)
		s := newChatState(newOpenArtifactComposerClient())
		tt := composerTester(s)

		tt.Type("/交付物")
		waitFor(t, tt, func() bool { return tt.HasText("/open-artifact") })
		tt.Key(0, ui.KeyEnter)
		tt.Frame()
		if tt.FramePending() {
			t.Fatal("开启 reduceMotion 时进入类型网格不应请求下一帧")
		}
	})
}
