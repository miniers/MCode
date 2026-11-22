package mcodeui

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

type AvatarProps struct {
	Key                    any
	Name, Text             string
	Icon                   *ui.SVG
	Size, FontSize         float32
	Foreground, Background ui.Color
	Border                 ui.Color
	Gradient               bool
}

func Avatar(c *ui.Context, p AvatarProps) ui.Element {
	ctx := c
	if p.Key != nil {
		ctx = c.Key(p.Key)
	}
	box := ui.Box(ctx).
		Size(p.Size, p.Size).
		Radius(p.Size / 2).
		Justify(ui.Center).
		AlignItems(ui.Center)
	if p.Gradient && p.Background.A > 0 {
		box.Draw(func(painter *ui.Painter, r ui.Rect) {
			var top, bottom ui.Color
			if float32(p.Background.A)/255 < 0.9 {
				top = p.Background
				bottom = p.Background.Alpha(0.45)
			} else {
				top = p.Background.Mix(ColorSurfaceCardLight, 0.14)
				bottom = p.Background.Mix(ColorSurfaceCanvasDark, 0.16)
			}
			painter.FillGradient(r, ui.LinearGradient{
				Angle: 135,
				Stops: []ui.GradientStop{{Color: top, At: 0}, {Color: bottom, At: 1}},
			}, r.W/2)
			if p.Border.A > 0 {
				painter.Stroke(ui.Rect{X: r.X + 0.5, Y: r.Y + 0.5, W: r.W - 1, H: r.H - 1}, p.Border, (r.W-1)/2, 1)
			}
		})
	} else {
		box.Background(p.Background)
		if p.Border.A != 0 {
			box.Border(1, p.Border)
		}
	}
	box.Children(func() {
		if p.Icon != nil {
			ui.Icon(c, p.Icon).Size(p.Size*0.66, p.Size*0.66).TextColor(p.Foreground)
			return
		}
		text := p.Text
		if text == "" {
			for _, r := range strings.TrimSpace(p.Name) {
				text = string(r)
				break
			}
		}
		if text != "" {
			ui.Text(c, text).FontSize(p.FontSize).FontWeight(600).TextColor(p.Foreground)
		}
	})
	return box
}
