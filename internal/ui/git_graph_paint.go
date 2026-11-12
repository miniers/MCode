package mcodeui

import (
	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

const gitGraphLaneWidth = Space4

// 坐标保留半个泳道的边距，让节点外圈不会被裁切。
func gitGraphLaneX(lane int) float32 {
	return Space1 + gitGraphLaneWidth/2 + float32(lane)*gitGraphLaneWidth
}

type gitGraphSegment struct {
	from, to int
	color    int
	lower    bool
}

// Upper 连接行顶与节点高度，Lower 连接节点高度与行底；转弯只发生在下半行。
func gitGraphSegments(row git.GraphRow) []gitGraphSegment {
	segments := make([]gitGraphSegment, 0, len(row.Upper)+len(row.Lower))
	for _, e := range row.Upper {
		segments = append(segments, gitGraphSegment{e.From, e.To, e.Color, false})
	}
	for _, e := range row.Lower {
		segments = append(segments, gitGraphSegment{e.From, e.To, e.Color, true})
	}
	return segments
}

func gitGraphWidth(rows []git.GraphRow) float32 {
	lanes := 1
	for _, r := range rows {
		lanes = max(lanes, r.Lane+1)
		for _, s := range gitGraphSegments(r) {
			lanes = max(lanes, s.from+1, s.to+1)
		}
	}
	return float32(lanes)*gitGraphLaneWidth + Space1*2
}

func renderGitGraphRow(c *ui.Context, row git.GraphRow, width float32, unpushed bool, background ui.Color) {
	colors := gitLaneColors(c)
	ui.Box(c).Width(width).Height(RowHeight).Shrink(0).Draw(func(p *ui.Painter, r ui.Rect) {
		for _, s := range gitGraphSegments(row) {
			x1, x2 := r.X+gitGraphLaneX(s.from), r.X+gitGraphLaneX(s.to)
			y1, y2 := r.Y, r.Y+r.H/2
			if s.lower {
				y1, y2 = y2, r.Y+r.H
			}
			var path ui.Path
			path.MoveTo(x1, y1)
			if x1 == x2 {
				path.LineTo(x2, y2)
			} else {
				path.CubeTo(x1, (y1+y2)/2, x2, (y1+y2)/2, x2, y2)
			}
			p.StrokePath(&path, Space0, colors[s.color%len(colors)])
		}
		x, y := r.X+gitGraphLaneX(row.Lane), r.Y+r.H/2
		radius := float32(3.5)
		var halo, node ui.Path
		halo.Circle(x, y, radius+Space0)
		p.FillPath(&halo, background)
		if unpushed {
			node.Circle(x, y, radius-Space0/2)
			p.StrokePath(&node, Space0, colors[row.Color%len(colors)])
		} else {
			node.Circle(x, y, radius)
			p.FillPath(&node, colors[row.Color%len(colors)])
		}
	})
}
