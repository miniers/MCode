package mcodeui

import (
	"github.com/egoist/mygo/ui"
)

// 桌面配额进度条共用尺寸：高度、圆角、时间进度竖线宽度（像素）与底槽遮罩不透明度。
const (
	quotaBarHeight       float32 = 4
	quotaBarRadius       float32 = 2
	quotaMarkerWidth     float32 = 1.5
	quotaBarTrackOpacity float32 = 0.10
)

// quotaLevel 按已用百分比分档：<50 为 low，<80 为 mid，<95 为 high，其余为 critical。
func quotaLevel(percent float64) string {
	if percent < 50 {
		return "low"
	}
	if percent < 80 {
		return "mid"
	}
	if percent < 95 {
		return "high"
	}
	return "critical"
}

// quotaLevelColor 把百分比分档映射为填充色：low→成功色，mid→黄色档，high→警告色，critical→危险色。
func quotaLevelColor(c *ui.Context, percent float64) ui.Color {
	switch quotaLevel(percent) {
	case "low":
		return ThemeSuccess(c)
	case "mid":
		return ThemeCaution(c)
	case "high":
		return ThemeWarning(c)
	default:
		return ThemeDanger(c)
	}
}

// quotaPercentColor 返回百分比文字的读数色：low 档保持次要文本色，其余跟随分档色。
func quotaPercentColor(c *ui.Context, percent float64) ui.Color {
	if quotaLevel(percent) == "low" {
		return ThemeSecondaryText(c)
	}
	return quotaLevelColor(c, percent)
}

// quotaBarTrackColor 返回配额进度条的底槽颜色（10% 遮罩叠层）。
func quotaBarTrackColor(c *ui.Context) ui.Color {
	return ThemeOverlay(c, quotaBarTrackOpacity)
}
