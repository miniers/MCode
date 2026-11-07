package mcodeui

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// TestChatHeaderLeadingInset 验证标题栏左边距随侧边栏显示进度在交通灯避让与常规内边距之间插值。
func TestChatHeaderLeadingInset(t *testing.T) {
	tests := []struct {
		progress float32
		want     float32
	}{
		{0, TrafficLightInset},                               // 侧边栏完全隐藏：让出交通灯区域
		{1, chatHeaderBaseInset},                             // 侧边栏完全显示：恢复常规内边距
		{0.5, (TrafficLightInset + chatHeaderBaseInset) / 2}, // 动画中：按进度插值
		{-1, TrafficLightInset},                              // 下界钳位
		{2, chatHeaderBaseInset},                             // 上界钳位
	}
	for _, tc := range tests {
		if got := chatHeaderLeadingInset(tc.progress); got != tc.want {
			t.Errorf("chatHeaderLeadingInset(%v) = %v, want %v", tc.progress, got, tc.want)
		}
	}
}

// TestShouldShowJumpToLatest 验证回到底部按钮的显示判定。
func TestShouldShowJumpToLatest(t *testing.T) {
	tests := []struct {
		name     string
		atEnd    bool
		distance float32
		want     bool
	}{
		{"停在底部时不显示", true, 0, false},
		{"停在底部且距离未知时不显示", true, -1, false},
		{"距离 121 时显示", false, 121, true},
		{"距离 120 时不显示", false, 120, false},
		{"距离 40 时不显示", false, 40, false},
		{"距离未知（ListState 只暴露 AtEnd）时按未到底处理", false, -1, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldShowJumpToLatest(tc.atEnd, tc.distance); got != tc.want {
				t.Errorf("shouldShowJumpToLatest(%v, %v) = %v, want %v", tc.atEnd, tc.distance, got, tc.want)
			}
		})
	}
}

// TestCompactSuggestionThreshold 验证按模型窗口换算的压缩建议阈值。
func TestCompactSuggestionThreshold(t *testing.T) {
	tests := []struct {
		name  string
		limit int64
		want  int64
	}{
		{"窗口未知时用固定 100k", 0, 100_000},
		{"32k 窗口取 70%", 32_000, 22_400},
		{"200k 窗口封顶 100k", 200_000, 100_000},
		{"1M 窗口封顶 100k", 1_000_000, 100_000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := compactSuggestionThreshold(tc.limit); got != tc.want {
				t.Errorf("compactSuggestionThreshold(%d) = %d, want %d", tc.limit, got, tc.want)
			}
		})
	}
}

// TestShouldOfferCompact 验证压缩建议卡片的判定条件。
func TestShouldOfferCompact(t *testing.T) {
	tests := []struct {
		name          string
		askCompact    bool
		dismissed     bool
		contextTokens int64
		limit         int64
		idle          time.Duration
		draft         string
		want          bool
	}{
		{"会话较旧且用量达标时提示", true, false, 150_000, 0, 2 * time.Hour, "", true},
		{"总量刚好达标且空闲刚好达标时提示", true, false, 100_000, 0, time.Hour, "继续", true},
		{"设置关闭时不提示", false, false, 150_000, 0, 2 * time.Hour, "", false},
		{"本会话已忽略时不提示", true, true, 150_000, 0, 2 * time.Hour, "", false},
		{"用量不足时不提示", true, false, 99_999, 0, 2 * time.Hour, "", false},
		{"空闲不足时不提示", true, false, 150_000, 0, 30 * time.Minute, "", false},
		{"命令草稿时不提示", true, false, 150_000, 0, 2 * time.Hour, "/compact", false},
		{"带空白的命令草稿也不提示", true, false, 150_000, 0, 2 * time.Hour, "  /help", false},
		{"1M 窗口上下文 120k 时提示（阈值 100k）", true, false, 120_000, 1_000_000, 2 * time.Hour, "", true},
		{"32k 窗口上下文 25k 时提示（阈值 22.4k）", true, false, 25_000, 32_000, 2 * time.Hour, "", true},
		{"32k 窗口上下文 20k 时不提示", true, false, 20_000, 32_000, 2 * time.Hour, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldOfferCompact(tc.askCompact, tc.dismissed, tc.contextTokens, tc.limit, tc.idle, tc.draft)
			if got != tc.want {
				t.Errorf("shouldOfferCompact(%v, %v, %d, %d, %v, %q) = %v, want %v",
					tc.askCompact, tc.dismissed, tc.contextTokens, tc.limit, tc.idle, tc.draft, got, tc.want)
			}
		})
	}
}

// TestShouldOfferForward 验证“转发到新会话”建议的判定条件。
func TestShouldOfferForward(t *testing.T) {
	tests := []struct {
		name      string
		dismissed bool
		running   bool
		used      int64
		limit     int64
		draft     string
		want      bool
	}{
		{"占用 79% 时不提示", false, false, 79_000, 100_000, "", false},
		{"占用达 80% 时提示", false, false, 80_000, 100_000, "", true},
		{"占用超过 80% 时提示", false, false, 170_000, 200_000, "继续", true},
		{"本会话已忽略时不提示", true, false, 80_000, 100_000, "", false},
		{"会话运行中不提示", false, true, 80_000, 100_000, "", false},
		{"命令草稿时不提示", false, false, 80_000, 100_000, "/compact", false},
		{"带空白的命令草稿也不提示", false, false, 80_000, 100_000, "  /help", false},
		{"窗口上限未知时不提示", false, false, 80_000, 0, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldOfferForward(tc.dismissed, tc.running, tc.used, tc.limit, tc.draft)
			if got != tc.want {
				t.Errorf("shouldOfferForward(%v, %v, %d, %d, %q) = %v, want %v",
					tc.dismissed, tc.running, tc.used, tc.limit, tc.draft, got, tc.want)
			}
		})
	}
}

// TestChatEmptyStatePromptAndProjectSwitch 验证空态引导屏与项目切换浮层。
func TestChatEmptyStatePromptAndProjectSwitch(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	fake := NewFakeClient()
	now := time.Now()
	fake.SetProjectsAndThreads([]Project{
		{ID: "p1", Name: "项目Alpha", Path: "/path/to/alpha"},
		{ID: "p2", Name: "项目Beta", Path: "/path/to/beta"},
	}, map[string][]Thread{
		"p1": {{ID: "t1", ProjectID: "p1", Title: "空会话", UpdatedAt: now}},
		"p2": {{ID: "t2", ProjectID: "p2", Title: "已有会话", UpdatedAt: now}},
	})

	state, view := buildRoot(fake, WithInitialThread("t1"))
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1200, 800)

	// 空态显示大标语与项目名
	waitFor(t, tt, func() bool {
		return tt.HasText(" 中构建什么？") && tt.HasText("项目Alpha")
	})

	// 点击项目名弹出切换浮层
	if err := tt.Click("切换项目"); err != nil {
		t.Fatalf("点击项目名失败: %v", err)
	}
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	if !tt.HasText("添加项目…") || !tt.HasText("从 GitHub 克隆…") {
		t.Fatalf("项目切换浮层缺少入口: %q", tt.Texts())
	}

	// 选择另一个项目后切换到该项目最近的会话
	if err := tt.Click("切换到项目 项目Beta"); err != nil {
		t.Fatalf("点击项目 Beta 失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.selectedThreadID == "t2"
	})
}

// TestChatJumpToLatestButton 验证时间线上滚后出现回到底部按钮，点击后回到最新。
func TestChatJumpToLatestButton(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	fake := NewFakeClient()
	var msgs []Message
	for i := 0; i < 80; i++ {
		msgs = append(msgs, Message{
			ID:        fmt.Sprintf("u%02d", i),
			Role:      "user",
			Text:      fmt.Sprintf("消息-%02d", i),
			TurnIndex: i + 1,
			IsFinal:   true,
		})
	}
	fake.SetMessages("t1", msgs)

	state, view := buildRoot(fake, WithInitialThread("t1"))
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("消息-79") })
	// 按按钮的无障碍 Label 定位。
	jumpLabel := "跳到最新"

	// 停在底部时没有回到底部按钮
	if _, ok := tt.Find(jumpLabel); ok {
		t.Fatalf("停在底部时不应显示回到底部按钮")
	}

	// 上滚后按钮出现
	tt.Scroll(600, 300, 0, -3000)
	for i := 0; i < 5; i++ {
		tt.Frame()
	}
	if _, ok := tt.Find(jumpLabel); !ok {
		t.Fatalf("上滚后应显示回到底部按钮，当前文本: %q", tt.Texts())
	}

	// 点击后回到最新
	if err := tt.Click(jumpLabel); err != nil {
		t.Fatalf("点击回到底部按钮失败: %v", err)
	}
	for i := 0; i < 5; i++ {
		tt.Frame()
	}
	if !tt.HasText("消息-79") {
		t.Fatalf("点击回到底部后应显示最新消息")
	}
}

// TestChatJumpToLatestUnreadLabel 验证离开底部后收到新消息时，回到底部胶囊显示未读条数。
func TestChatJumpToLatestUnreadLabel(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	fake := NewFakeClient()
	var msgs []Message
	for i := 0; i < 80; i++ {
		msgs = append(msgs, Message{
			ID:        fmt.Sprintf("u%02d", i),
			Role:      "user",
			Text:      fmt.Sprintf("消息-%02d", i),
			TurnIndex: i + 1,
			IsFinal:   true,
		})
	}
	fake.SetMessages("t1", msgs)

	state, view := buildRoot(fake, WithInitialThread("t1"))
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("消息-79") })
	// 上滚离开底部，这一帧记下未读基线。
	tt.Scroll(600, 300, 0, -3000)
	for i := 0; i < 5; i++ {
		tt.Frame()
	}

	// 后端推来一条新消息后，按钮文字换成未读条数。
	fake.TriggerUpdate(ThreadUpdate{
		ThreadID: "t1",
		Thread:   Thread{ID: "t1", ProjectID: "p1", Title: "测试会话1"},
		Messages: []Message{{ID: "u80", Role: "user", Text: "消息-80", TurnIndex: 81, IsFinal: true}},
	})
	want := unreadJumpLabel(1)
	waitFor(t, tt, func() bool { return tt.HasText(want) })

	// 点击未读胶囊回到最新并清空未读。
	if err := tt.Click(want); err != nil {
		t.Fatalf("点击未读胶囊失败: %v", err)
	}
	for i := 0; i < 5; i++ {
		tt.Frame()
	}
	if tt.HasText(want) {
		t.Fatalf("回到最新后未读胶囊应消失")
	}
}

// TestCompactSuggestionCardDismissAndCompact 验证压缩建议卡片的出现、忽略与本会话不再提示。
func TestCompactSuggestionCardDismissAndCompact(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	fake := NewFakeClient()
	fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}},
		map[string][]Thread{
			"p1": {{ID: "t1", ProjectID: "p1", Title: "旧会话", UpdatedAt: time.Now().Add(-2 * time.Hour)}},
		})
	fake.SetMessages("t1", []Message{{ID: "m1", Role: "assistant", Text: "历史回复", TurnIndex: 1, IsFinal: true}})
	fake.SetThreadUsage("t1", ThreadUsage{Total: TokenUsage{Input: 120_000, Output: 5_000}})

	state, view := buildRoot(fake, WithInitialThread("t1"))
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("这个会话较长，继续之前要先压缩上下文吗？")
	})

	// 点击“不用了”后卡片消失，本会话不再提示
	if err := tt.Click("不用了"); err != nil {
		t.Fatalf("点击“不用了”失败: %v", err)
	}
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	if tt.HasText("这个会话较长，继续之前要先压缩上下文吗？") {
		t.Fatalf("忽略后卡片应消失")
	}
	if !compactSuggestionDismissed(state, "t1") {
		t.Fatalf("忽略状态应记录在会话上")
	}
}

// TestCompactSuggestionCardAcceptsCompact 验证点击“压缩”会发送 /compact 命令。
func TestCompactSuggestionCardAcceptsCompact(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	fake := NewFakeClient()
	fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}},
		map[string][]Thread{
			"p1": {{ID: "t1", ProjectID: "p1", Title: "旧会话", UpdatedAt: time.Now().Add(-2 * time.Hour)}},
		})
	fake.SetMessages("t1", []Message{{ID: "m1", Role: "assistant", Text: "历史回复", TurnIndex: 1, IsFinal: true}})
	fake.SetThreadUsage("t1", ThreadUsage{Total: TokenUsage{Input: 120_000}})

	state, view := buildRoot(fake, WithInitialThread("t1"))
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("这个会话较长，继续之前要先压缩上下文吗？")
	})

	if err := tt.Click("压缩"); err != nil {
		t.Fatalf("点击“压缩”失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return fake.SendCalls() > 0 && fake.LastSentText() == "/compact"
	})
}

// forwardCallsContain 在加锁的前提下读取假客户端的转发调用记录。
func forwardCallsContain(fake *FakeClient, threadID string) bool {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, id := range fake.ForwardCalls {
		if id == threadID {
			return true
		}
	}
	return false
}

// newForwardSuggestionFixture 构造一个上下文占用 85%（窗口 100k）、最近刚活动的会话，
// 使压缩卡片因“会话不够久未活动”不显示，只剩转发建议卡片。
func newForwardSuggestionFixture(t *testing.T) (*FakeClient, *chatState, func(c *ui.Context)) {
	t.Helper()
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	fake := NewFakeClient()
	fake.SetProviders([]ProviderInfo{{
		ID:     "claude",
		Models: []ModelInfo{{ID: "", IsDefault: true, ContextLimit: 100_000}},
	}})
	fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}},
		map[string][]Thread{
			"p1": {{
				ID:        "t1",
				ProjectID: "p1",
				Title:     "长会话",
				Provider:  "claude",
				UpdatedAt: time.Now(),
			}},
		})
	fake.SetMessages("t1", []Message{{ID: "m1", Role: "assistant", Text: "历史回复", TurnIndex: 1, IsFinal: true}})
	fake.SetThreadUsage("t1", ThreadUsage{
		Turns: map[int]TokenUsage{0: {Input: 85_000}},
		Total: TokenUsage{Input: 85_000},
	})

	state, view := buildRoot(fake, WithInitialThread("t1"))
	t.Cleanup(state.close)
	return fake, state, view
}

// TestForwardSuggestionCardDismiss 验证上下文占用达 80% 时出现转发建议，点“不用了”后本会话不再提示。
func TestForwardSuggestionCardDismiss(t *testing.T) {
	_, state, view := newForwardSuggestionFixture(t)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("上下文已用 85%，可以转发到新会话继续。")
	})

	if err := tt.Click("不用了"); err != nil {
		t.Fatalf("点击“不用了”失败: %v", err)
	}
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	if tt.HasText("上下文已用 85%，可以转发到新会话继续。") {
		t.Fatalf("忽略后卡片应消失")
	}
	if !forwardSuggestionDismissed(state, "t1") {
		t.Fatalf("忽略状态应记录在会话上")
	}
}

// TestForwardSuggestionCardAcceptsForward 验证点击“转发到新会话”会创建新会话。
func TestForwardSuggestionCardAcceptsForward(t *testing.T) {
	fake, state, view := newForwardSuggestionFixture(t)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("上下文已用 85%，可以转发到新会话继续。")
	})

	if err := tt.Click("转发到新会话"); err != nil {
		t.Fatalf("点击“转发到新会话”失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return forwardCallsContain(fake, "t1")
	})
	if !forwardSuggestionDismissed(state, "t1") {
		t.Fatalf("转发后应记录本会话不再提示")
	}
}

// TestCompactSuggestionCardForwards 验证压缩卡片也能直接转发，且不会同时出现转发建议卡片。
func TestCompactSuggestionCardForwards(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	fake := NewFakeClient()
	fake.SetProviders([]ProviderInfo{{
		ID:     "claude",
		Models: []ModelInfo{{ID: "", IsDefault: true, ContextLimit: 200_000}},
	}})
	fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}},
		map[string][]Thread{
			"p1": {{
				ID:        "t1",
				ProjectID: "p1",
				Title:     "旧会话",
				Provider:  "claude",
				UpdatedAt: time.Now().Add(-2 * time.Hour),
			}},
		})
	fake.SetMessages("t1", []Message{{ID: "m1", Role: "assistant", Text: "历史回复", TurnIndex: 1, IsFinal: true}})
	// 最新一轮占用 180k / 200k = 90%，两张卡片的阈值都满足。
	fake.SetThreadUsage("t1", ThreadUsage{
		Turns: map[int]TokenUsage{0: {Input: 180_000}},
		Total: TokenUsage{Input: 180_000},
	})

	state, view := buildRoot(fake, WithInitialThread("t1"))
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("这个会话较长，继续之前要先压缩上下文吗？")
	})
	if tt.HasText("上下文已用 90%，可以转发到新会话继续。") {
		t.Fatalf("压缩卡片显示时不应再出现转发建议卡片")
	}

	if err := tt.Click("转发到新会话"); err != nil {
		t.Fatalf("点击“转发到新会话”失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return forwardCallsContain(fake, "t1")
	})
	if !compactSuggestionDismissed(state, "t1") {
		t.Fatalf("转发后应记录压缩建议已忽略")
	}
}
