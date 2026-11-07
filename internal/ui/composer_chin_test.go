package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestComposerChinTier(t *testing.T) {
	for _, tc := range []struct {
		width float32
		want  composerChinSize
	}{
		{576, composerChinFull},
		{575, composerChinNoShortcut},
		{480, composerChinNoShortcut},
		{479, composerChinIcons},
		{384, composerChinIcons},
		{383, composerChinNarrow},
		{320, composerChinNarrow},
	} {
		if got := composerChinTier(tc.width); got != tc.want {
			t.Errorf("composerChinTier(%v) = %v, want %v", tc.width, got, tc.want)
		}
	}
}

// TestComposerChinTierForEmbedded 验证浮动面板里的子代理输入框放宽一档：
// 同一宽度下比主输入框少收一档，主干档位逻辑本身不变。
func TestComposerChinTierForEmbedded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	plain := &chatState{}
	embedded := &chatState{embeddedBee: true}
	for _, tc := range []struct {
		name  string
		state *chatState
		width float32
		want  composerChinSize
	}{
		{"nil 状态", nil, 400, composerChinIcons},
		{"普通宽", plain, 600, composerChinFull},
		{"普通窄", plain, 400, composerChinIcons},
		{"浮窗 600", embedded, 600, composerChinFull},
		{"浮窗 500", embedded, 500, composerChinFull},
		{"浮窗 420", embedded, 420, composerChinNoShortcut},
		{"浮窗 390", embedded, 390, composerChinNoShortcut},
		{"浮窗 300", embedded, 300, composerChinIcons},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := composerChinTierFor(tc.state, tc.width); got != tc.want {
				t.Fatalf("composerChinTierFor(%v) = %v, want %v", tc.width, got, tc.want)
			}
		})
	}
	s.embeddedBee = true
	th := &Thread{ID: "t1", Provider: "agy"}
	SetChatQuotaWindowsForProvider(s, "agy", []ProviderQuotaWindow{
		{Label: "a@example.com · Gemini Models", UsedPercent: 42, Active: true},
		{Label: "a@example.com · Gemini Models 7d", UsedPercent: 58, Active: true},
	})
	t.Cleanup(func() { SetChatQuotaWindowsForProvider(s, "agy", nil) })
	tt := ui.NewTester(func(c *ui.Context) {
		renderComposerQuotaStrip(c, s, th, composerChinTierFor(s, 400))
	}, 400, 400)
	for _, want := range []string{"42%", "58%"} {
		if !tt.HasText(want) {
			t.Fatalf("浮窗额度条应显示 %s: %v", want, tt.Texts())
		}
	}
}

func TestComposerChinQuotaReadouts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	th := &Thread{ID: "t1", Provider: "agy"}
	SetChatQuotaWindowsForProvider(s, "agy", []ProviderQuotaWindow{
		{Label: "a@example.com · Gemini Models", UsedPercent: 42, Active: true},
		{Label: "a@example.com · Gemini Models 7d", UsedPercent: 58, Active: true},
	})
	t.Cleanup(func() { SetChatQuotaWindowsForProvider(s, "agy", nil) })
	for _, width := range []int{600, 520, 420, 320} {
		t.Run(composerChinTierName(width), func(t *testing.T) {
			tt := ui.NewTester(func(c *ui.Context) {
				renderComposerQuotaStrip(c, s, th, composerChinTier(float32(width)))
			}, width, 400)
			if width >= 384 && !tt.HasText("58%") {
				t.Fatalf("额度读数不可见: %v", tt.Texts())
			}
			if width < 384 && (tt.HasText("42%") || tt.HasText("58%")) {
				t.Fatalf("最窄档不应显示百分比: %v", tt.Texts())
			}
			if width < 480 {
				if _, ok := tt.Find("composer-quota-ring-agy"); !ok {
					t.Fatal("窄档缺少额度圆环")
				}
				if width >= 384 && tt.HasText("42%") {
					t.Fatalf("图标档只应显示最高用量: %v", tt.Texts())
				}
			}
		})
	}
}

func composerChinTierName(width int) string {
	switch composerChinTier(float32(width)) {
	case composerChinFull:
		return "完整"
	case composerChinNoShortcut:
		return "隐藏快捷键"
	case composerChinIcons:
		return "图标"
	default:
		return "最窄"
	}
}

func TestComposerChinContextMeterAlwaysVisible(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	th := &Thread{ID: "t1"}
	for _, used := range []int64{0, 20, 85, 86} {
		s.threadUsage = ThreadUsage{Turns: map[int]TokenUsage{0: {ContextTokens: used, ContextWindow: 100}}}
		tt := ui.NewTester(func(c *ui.Context) {
			renderContextMeter(c, s, th)
		}, 320, 400)
		if _, found := tt.Find("上下文用量"); !found {
			t.Errorf("用量 %d%% 时圆环不可见", used)
		}
	}
}

func TestComposerChinNonNarrowHasContextMeterWithoutMore(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	th := &Thread{ID: "t1"}
	s.threadUsage = ThreadUsage{Turns: map[int]TokenUsage{0: {ContextTokens: 20, ContextWindow: 100}}}
	for _, tier := range []composerChinSize{composerChinFull, composerChinNoShortcut, composerChinIcons} {
		tt := ui.NewTester(func(c *ui.Context) {
			renderContextMeter(c, s, th)
		}, 600, 400)
		if _, found := tt.Find("上下文用量"); !found {
			t.Errorf("档位 %v 低用量时应显示圆环", tier)
		}
	}
}
