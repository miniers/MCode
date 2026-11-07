package mcodeui

import "github.com/egoist/mygo/ui"

// ShadowLayer 描述一层阴影。
type ShadowLayer struct {
	Y     float32
	Blur  float32
	Alpha float32
}

type Elevation [2]ShadowLayer

var (
	ElevPopover = Elevation{{Y: 2, Blur: 6, Alpha: 0.08}, {Y: 8, Blur: 24, Alpha: 0.12}}
	ElevDialog  = Elevation{{Y: 2, Blur: 8, Alpha: 0.08}, {Y: 12, Blur: 32, Alpha: 0.16}}
	ElevFloat   = ElevDialog
	ElevNone    Elevation
)

const ScrimOpacity float32 = 0.35

func elevationAlpha(alpha float32, layer int, dark bool) float32 {
	if dark {
		if layer == 0 {
			return alpha * (20.0 / 8.0)
		}
		return alpha * (32.0 / 12.0)
	}
	return alpha
}

func applyElevation(c *ui.Context, el ui.Element, e Elevation) ui.Element {
	if e == ElevNone {
		return el
	}
	for i, layer := range e {
		el = el.Shadow(0, layer.Y, layer.Blur, 0, ui.RGBA(0, 0, 0, elevationAlpha(layer.Alpha, i, isDark(c))))
	}
	return el
}
