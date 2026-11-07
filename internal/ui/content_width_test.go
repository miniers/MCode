package mcodeui

import (
	"testing"

	"mcode/internal/config"
)

// newContentWidthTestState 返回一个注册了设置状态的会话，供内容宽度用例读写外观配置。
func newContentWidthTestState(t *testing.T, width string) *chatState {
	t.Helper()
	state := &chatState{}
	st := &SettingsState{}
	st.cfg.Appearance.ContentWidth = width
	setChatSettings(state, st)
	t.Cleanup(func() {
		settingsMu.Lock()
		delete(settingsList, state)
		settingsMu.Unlock()
	})
	return state
}

// newProportionalWidthTestState 返回一个处于「按比例」档（配置值 full）的会话：
// percent 为百分比，paneW 为聊天区可用宽度（0 表示本帧还没测出来）。
func newProportionalWidthTestState(t *testing.T, percent int, paneW float32) *chatState {
	t.Helper()
	state := newContentWidthTestState(t, config.ContentWidthFull)
	getChatSettings(state).cfg.Appearance.ContentWidthPercent = percent
	setChatPaneWidth(state, paneW)
	return state
}

func TestTimelineContentMaxWidth(t *testing.T) {
	cases := []struct {
		name string
		// width 为空时连同 state 一起置 nil，覆盖根本没有设置状态的情况。
		nilState bool
		width    string
		want     float32
	}{
		{name: "无会话状态", nilState: true, want: timelineContentMaxWidthStandard},
		{name: "标准", width: config.ContentWidthStandard, want: 768},
		{name: "宽", width: config.ContentWidthWide, want: 1120},
		{name: "未知值按标准", width: "ultra_wide", want: timelineContentMaxWidthStandard},
		{name: "空值按标准", width: "", want: timelineContentMaxWidthStandard},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var state *chatState
			if !tc.nilState {
				state = newContentWidthTestState(t, tc.width)
			}
			if got := timelineContentMaxWidth(state); got != tc.want {
				t.Fatalf("timelineContentMaxWidth() = %v, want %v", got, tc.want)
			}
		})
	}
}

// 按比例档：内容列宽度 = 聊天区宽度 × 百分比（再补回列自身的左右 padding），
// 100% 时等于整个聊天区宽度；窄窗口退化为标准档或铺满，不会比标准档更窄。
func TestTimelineContentMaxWidthProportional(t *testing.T) {
	const padX = contentColumnPadX
	cases := []struct {
		name    string
		percent int
		paneW   float32
		want    float32
	}{
		{name: "1600 宽 85%", percent: 85, paneW: 1600, want: 1600*85/100 + 2*padX},
		{name: "100% 等同全宽", percent: 100, paneW: 1600, want: 1600 + 2*padX},
		{name: "缺失百分比按默认 85", percent: 0, paneW: 1600, want: 1600*85/100 + 2*padX},
		{name: "百分比过小按 60", percent: 20, paneW: 1600, want: 1600*60/100 + 2*padX},
		{name: "百分比过大按 100", percent: 200, paneW: 1600, want: 1600 + 2*padX},
		{name: "900 宽 60% 不低于标准", percent: 60, paneW: 900, want: timelineContentMaxWidthStandard},
		{name: "600 宽退化为铺满", percent: 60, paneW: 600, want: 600},
		{name: "尚未测出宽度按标准", percent: 85, paneW: 0, want: timelineContentMaxWidthStandard},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := newProportionalWidthTestState(t, tc.percent, tc.paneW)
			if got := timelineContentMaxWidth(state); got != tc.want {
				t.Fatalf("timelineContentMaxWidth() = %v, want %v", got, tc.want)
			}
		})
	}
}

// 输入框宽度固定为 composerDockMaxWidth：正文随内容宽度变宽时，输入框仍停在 768。
func TestComposerDockMaxWidthIgnoresContentWidth(t *testing.T) {
	states := []struct {
		name  string
		state *chatState
	}{
		{"宽档", newContentWidthTestState(t, config.ContentWidthWide)},
		{"按比例 100%", newProportionalWidthTestState(t, 100, 1600)},
	}
	for _, tc := range states {
		t.Run(tc.name, func(t *testing.T) {
			if body := timelineContentMaxWidth(tc.state); body <= composerDockMaxWidth {
				t.Fatalf("timelineContentMaxWidth() = %v, want > %v", body, composerDockMaxWidth)
			}
			if composerDockMaxWidth != 768 {
				t.Fatalf("composerDockMaxWidth = %v, want 768", composerDockMaxWidth)
			}
		})
	}
}

func TestUserBubbleMaxWidth(t *testing.T) {
	cases := []struct {
		name     string
		nilState bool
		width    string
		want     float32
	}{
		{name: "无会话状态", nilState: true, want: 768 * userBubbleRatio},
		{name: "标准", width: config.ContentWidthStandard, want: 768 * userBubbleRatio},
		{name: "宽", width: config.ContentWidthWide, want: 1120 * userBubbleRatio},
		{name: "未知值按标准", width: "ultra_wide", want: 768 * userBubbleRatio},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var state *chatState
			if !tc.nilState {
				state = newContentWidthTestState(t, tc.width)
			}
			if got := userBubbleMaxWidth(state); got != tc.want {
				t.Fatalf("userBubbleMaxWidth() = %v, want %v", got, tc.want)
			}
		})
	}
}

// 按比例档的气泡宽度取内容宽度的 75%，上限 960，下限标准档的 600。
func TestUserBubbleMaxWidthProportional(t *testing.T) {
	cases := []struct {
		name    string
		percent int
		paneW   float32
		want    float32
	}{
		{name: "1000 宽 85% 取内容宽度 75%", percent: 85, paneW: 1000, want: 637.5},
		{name: "宽屏不超过 960", percent: 100, paneW: 2000, want: userBubbleMaxWidthFull},
		{name: "窄窗口不低于 600", percent: 60, paneW: 600, want: userBubbleFullMin},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := newProportionalWidthTestState(t, tc.percent, tc.paneW)
			if got := userBubbleMaxWidth(state); got != tc.want {
				t.Fatalf("userBubbleMaxWidth() = %v, want %v", got, tc.want)
			}
		})
	}
}
