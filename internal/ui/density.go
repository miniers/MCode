package mcodeui

import "github.com/egoist/mygo/ui"

// currentDensity 返回本帧的界面密度：沿用外观设置里的 conversation_density，
// 没有会话状态时按舒适档。密度只由配置决定，参数 c 让所有调用点保持同一写法。
func currentDensity(c *ui.Context, s *chatState) Density {
	return densityFor(s)
}

// isCompact 报告这档密度是否为紧凑档，用于没法直接落成字段的少数分支
// （例如工具输出区的最大高度），调用点不必再各自比对配置。
func (d Density) isCompact() bool {
	return d == DensityCompact
}
