package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// withTitleBarGOOS 在测试期间把平台判断切成 goos，结束后还原。
func withTitleBarGOOS(t *testing.T, goos string) {
	t.Helper()
	old := titleBarGOOS
	titleBarGOOS = goos
	t.Cleanup(func() { titleBarGOOS = old })
}

// TestResolveTitleBarInsets 验证各平台的标题栏避让距离：macOS 用交通灯常量，
// Windows 只让出右侧按钮，按钮在左侧时才额外让出顶部高度，全屏时三项全为 0。
func TestResolveTitleBarInsets(t *testing.T) {
	tests := []struct {
		name string
		goos string
		bar  ui.TitleBar
		want titleBarInsets
	}{
		{"macOS 交通灯", "darwin", ui.TitleBar{Height: 32, Left: 78}, titleBarInsets{Leading: TrafficLightInset, Top: TrafficLightTopOffset}},
		{"macOS 全屏也保持常量", "darwin", ui.TitleBar{}, titleBarInsets{Leading: TrafficLightInset, Top: TrafficLightTopOffset}},
		{"Windows 按钮在右侧", "windows", ui.TitleBar{Height: 53, Right: 138}, titleBarInsets{Trailing: 138}},
		{"Windows 全屏", "windows", ui.TitleBar{}, titleBarInsets{}},
		{"Linux 按钮在左侧", "linux", ui.TitleBar{Height: 32, Left: 78}, titleBarInsets{Leading: 78, Top: 32}},
		{"Linux 按钮在两侧", "linux", ui.TitleBar{Height: 32, Left: 40, Right: 60}, titleBarInsets{Leading: 40, Top: 32, Trailing: 60}},
	}
	for _, tc := range tests {
		if got := resolveTitleBarInsets(tc.goos, tc.bar); got != tc.want {
			t.Errorf("%s: resolveTitleBarInsets(%q, %+v) = %+v, want %+v", tc.name, tc.goos, tc.bar, got, tc.want)
		}
	}
}

// TestTitleBarInsetsHelpers 验证三个便捷取值按注入的平台与窗口按钮区域返回。
func TestTitleBarInsetsHelpers(t *testing.T) {
	// SetTitleBar 会立刻渲染一帧：便捷取值只在渲染时读到新的窗口按钮区域，
	// 所以每帧把三个取值存到外部变量，SetTitleBar 之后断言这份快照，
	// 而不是复用第一帧回调里留下的 ctx（那样读到的是过期数据）。
	var ctx *ui.Context
	var leading, top, trailing float32
	tt := ui.NewTester(func(c *ui.Context) {
		ctx = c
		leading, top, trailing = titleBarLeadingInset(c), titleBarTopOffset(c), titleBarTrailingInset(c)
	}, 800, 600)
	tt.Frame()
	if ctx == nil {
		t.Fatal("测试框架没有回调视图，取不到 ui 上下文")
	}

	// Windows：最小化/最大化/关闭画在右上角，占高 53、宽 138。
	withTitleBarGOOS(t, "windows")
	tt.SetTitleBar(ui.TitleBar{Height: 53, Right: 138})
	if leading != 0 {
		t.Errorf("windows titleBarLeadingInset = %v, want 0", leading)
	}
	if top != 0 {
		t.Errorf("windows titleBarTopOffset = %v, want 0", top)
	}
	if trailing != 138 {
		t.Errorf("windows titleBarTrailingInset = %v, want 138", trailing)
	}

	// Windows 全屏：按钮隐藏，三项都是 0。
	tt.SetTitleBar(ui.TitleBar{})
	if got := (titleBarInsets{Leading: leading, Top: top, Trailing: trailing}); got != (titleBarInsets{}) {
		t.Errorf("windows 全屏 = %+v, want 零值", got)
	}

	// macOS：忽略窗口报告的按钮区域，仍返回既有常量，布局与改动前一致。
	withTitleBarGOOS(t, "darwin")
	tt.SetTitleBar(ui.TitleBar{Height: 32, Left: 78})
	if leading != TrafficLightInset {
		t.Errorf("darwin titleBarLeadingInset = %v, want %v", leading, TrafficLightInset)
	}
	if top != TrafficLightTopOffset {
		t.Errorf("darwin titleBarTopOffset = %v, want %v", top, TrafficLightTopOffset)
	}
	if trailing != 0 {
		t.Errorf("darwin titleBarTrailingInset = %v, want 0", trailing)
	}

	// 没有上下文的兜底：不 panic，返回全 0。
	if got := titleBarInsetsOf(nil); got != (titleBarInsets{}) {
		t.Errorf("titleBarInsetsOf(nil) = %+v, want 零值", got)
	}
}

func TestSidebarTopBarHeight(t *testing.T) {
	var height float32
	tt := ui.NewTester(func(c *ui.Context) {
		height = sidebarTopBarHeight(titleBarTopOffset(c))
	}, 800, 600)

	withTitleBarGOOS(t, "windows")
	tt.SetTitleBar(ui.TitleBar{Height: 53, Right: 138})
	if height != HeaderHeight {
		t.Errorf("windows sidebarTopBarHeight = %v, want %v", height, HeaderHeight)
	}

	withTitleBarGOOS(t, "darwin")
	tt.SetTitleBar(ui.TitleBar{Height: 32, Left: 78})
	if height != 0 {
		t.Errorf("darwin sidebarTopBarHeight = %v, want 0", height)
	}
}

// TestHiddenTitleBarHeight 验证只有 Windows 设置 TitleBarHeight。
func TestHiddenTitleBarHeight(t *testing.T) {
	if got := hiddenTitleBarHeight("windows", 53); got != 53 {
		t.Errorf("windows hiddenTitleBarHeight = %v, want 53", got)
	}
	if got := hiddenTitleBarHeight("darwin", 53); got != 0 {
		t.Errorf("darwin hiddenTitleBarHeight = %v, want 0", got)
	}
	if got := hiddenTitleBarHeight("linux", 53); got != 0 {
		t.Errorf("linux hiddenTitleBarHeight = %v, want 0", got)
	}
}

// TestChatHeaderLeadingInsetFrom 验证前导边距插值：Windows 上从 0 起步，macOS 与旧函数一致。
func TestChatHeaderLeadingInsetFrom(t *testing.T) {
	tests := []struct {
		name     string
		leading  float32
		progress float32
		want     float32
	}{
		{"Windows 侧边栏隐藏", 0, 0, 0},
		{"Windows 侧边栏显示", 0, 1, chatHeaderBaseInset},
		{"Windows 动画中", 0, 0.5, chatHeaderBaseInset / 2},
		{"macOS 侧边栏隐藏", TrafficLightInset, 0, TrafficLightInset},
		{"下界钳位", TrafficLightInset, -1, TrafficLightInset},
		{"上界钳位", 0, 2, chatHeaderBaseInset},
	}
	for _, tc := range tests {
		if got := chatHeaderLeadingInsetFrom(tc.leading, tc.progress); got != tc.want {
			t.Errorf("%s: chatHeaderLeadingInsetFrom(%v, %v) = %v, want %v", tc.name, tc.leading, tc.progress, got, tc.want)
		}
	}
	if got := chatHeaderLeadingInset(0.5); got != chatHeaderLeadingInsetFrom(TrafficLightInset, 0.5) {
		t.Errorf("chatHeaderLeadingInset 与 macOS 前导边距版本不一致: %v", got)
	}
}
