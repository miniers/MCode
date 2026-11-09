package mcodeui

import (
	"fmt"
	"math"

	"github.com/egoist/mygo/ui"
)

type ProgressBarProps struct {
	Key           any
	Label         string
	Ratio, Height float32
	Track, Fill   ui.Color
}

func ProgressBar(c *ui.Context, p ProgressBarProps) ui.Element {
	ratio := p.Ratio
	if math.IsNaN(float64(ratio)) {
		ratio = 0
	}
	ratio = max(0, min(1, ratio))
	ctx := c
	if p.Key != nil {
		ctx = c.Key(p.Key)
	}
	track := ui.Row(ctx).FillWidth().Height(p.Height).Radius(RadiusPill).
		Background(p.Track).Label(fmt.Sprintf("%s %.0f%%", p.Label, ratio*100))
	return track.Children(func() {
		if ratio > 0 {
			ui.Box(c).Label(p.Label+" 填充").Height(p.Height).Radius(RadiusPill).
				Grow(ratio).Background(p.Fill)
		}
		if ratio < 1 {
			ui.Box(c).Height(p.Height).Grow(1 - ratio)
		}
	})
}
