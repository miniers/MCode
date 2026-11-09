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
}

func Avatar(c *ui.Context, p AvatarProps) ui.Element {
	ctx := c
	if p.Key != nil {
		ctx = c.Key(p.Key)
	}
	box := ui.Box(ctx).
		Size(p.Size, p.Size).
		Radius(p.Size / 2).
		Background(p.Background).
		Justify(ui.Center).
		AlignItems(ui.Center)
	if p.Border.A != 0 {
		box.Border(1, p.Border)
	}
	box.Children(func() {
		if p.Icon != nil {
			ui.Icon(c, p.Icon).Size(p.Size*0.75, p.Size*0.75).TextColor(p.Foreground)
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
