package mcodeui

import (
	"sync"

	"mcode/internal/config"
)

// 「文字大小」的进程内当前档位（百分比）。它只缩放侧栏会话列表与聊天内容，
// 标题栏、Composer、设置页与弹层保持原字号；白名单守卫见 text_scale_guard_test.go。
//
// 与快捷键覆盖（见 shortcuts.go 的 setShortcutOverrides）同一套做法：启动加载配置
// （WithSettings）、设置页重新读取配置（SettingsState.Open）与保存设置（SettingsState.save）
// 时刷新；时间线与侧边栏每帧渲染都读它，改档位后下一帧就生效。
var (
	textScaleMu      sync.RWMutex
	textScalePercent = config.DefaultTextScale
)

// setTextScale 记录当前文字大小档位；越界值按配置规则归一化（不在档位里按 100）。
func setTextScale(percent int) {
	appearance := config.AppearanceConfig{TextScale: percent}
	textScaleMu.Lock()
	textScalePercent = appearance.EffectiveTextScale()
	textScaleMu.Unlock()
}

// textScaleFactor 返回当前文字大小倍数，1 即 100%。
func textScaleFactor() float32 {
	textScaleMu.RLock()
	defer textScaleMu.RUnlock()
	return float32(textScalePercent) / 100
}

// scaledFont 把写死的字号按当前文字大小缩放：侧栏会话行与时间线里原来的
// FontSize(13) 改为 FontSize(scaledFont(13))，行高按同一比例缩放以免文字被截断。
func scaledFont(size float32) float32 {
	return size * textScaleFactor()
}
