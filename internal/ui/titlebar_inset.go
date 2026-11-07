package mcodeui

import (
	"runtime"

	"github.com/egoist/mygo/ui"
)

// titleBarGOOS 是标题栏避让逻辑判断平台用的 GOOS，测试里可以注入 "windows" / "darwin"。
var titleBarGOOS = runtime.GOOS

// titleBarInsets 是自绘标题栏要给窗口系统按钮让出的三段距离（单位 pt）：
// Leading 左侧、Top 顶部、Trailing 右侧。
type titleBarInsets struct {
	Leading  float32
	Top      float32
	Trailing float32
}

// resolveTitleBarInsets 把平台与窗口按钮占用区域换算成标题栏的避让距离。
// macOS 的交通灯固定在左上角，直接沿用既有常量，保证像素级不变；
// 其他平台按 mygo 报告的按钮区域让位：Windows 的按钮在右侧，只让出尾部边距，
// 顶部占位只在按钮确实位于左侧（部分 Linux 桌面）时才需要，全屏时按钮区域为 0，三项也全为 0。
func resolveTitleBarInsets(goos string, bar ui.TitleBar) titleBarInsets {
	if goos == "darwin" {
		return titleBarInsets{Leading: TrafficLightInset, Top: TrafficLightTopOffset}
	}
	top := float32(0)
	if bar.Left > 0 {
		top = bar.Height
	}
	return titleBarInsets{Leading: bar.Left, Top: top, Trailing: bar.Right}
}

// titleBarInsetsOf 取当前窗口的标题栏避让距离。
func titleBarInsetsOf(c *ui.Context) titleBarInsets {
	if c == nil {
		return titleBarInsets{}
	}
	return resolveTitleBarInsets(titleBarGOOS, c.TitleBar())
}

// titleBarLeadingInset 是标题栏左侧要空出的宽度：macOS 为交通灯，Windows 为 0。
func titleBarLeadingInset(c *ui.Context) float32 { return titleBarInsetsOf(c).Leading }

// titleBarTopOffset 是内容顶部要空出的高度：macOS 为交通灯，Windows 的按钮在右侧，为 0。
func titleBarTopOffset(c *ui.Context) float32 { return titleBarInsetsOf(c).Top }

// titleBarTrailingInset 是标题栏右侧要空出的宽度：Windows 为系统按钮的宽度，macOS 为 0。
func titleBarTrailingInset(c *ui.Context) float32 { return titleBarInsetsOf(c).Trailing }

// hiddenTitleBarHeight 返回窗口选项 TitleBarHeight 的取值：Windows 上系统按钮画在自绘
// 标题栏里并填满这个高度，设成标题行的实际高度才能和标题行里的按钮垂直居中对齐；
// 其他平台不需要（macOS 用 TrafficLightPosition），返回 0 即不设置。
func hiddenTitleBarHeight(goos string, height int) int {
	if goos == "windows" {
		return height
	}
	return 0
}
