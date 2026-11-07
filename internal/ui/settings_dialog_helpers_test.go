package mcodeui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func settingsVisible(r ui.Rect) bool {
	return r.W > 0 && r.H > 0
}

func settingsRevealInDetail(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	var previous ui.Rect
	var previousFound bool
	for i := 0; i < 80; i++ {
		detail, ok := tt.Find("设置详情列")
		if !ok {
			t.Fatalf("找不到设置详情列: %v", tt.Texts())
		}
		viewport := detail
		if card, found := tt.Find("设置"); found {
			if card.W < detail.W {
				window := tt.Image().Bounds()
				height := min(float32(680), float32(window.Dy())-80)
				card = ui.Rect{X: 0, Y: (float32(window.Dy()) - height) / 2, W: float32(window.Dx()), H: height}
			}
			viewport.X = max(detail.X, card.X)
			viewport.Y = card.Y
			viewport.W = min(detail.X+detail.W, card.X+card.W) - viewport.X
			viewport.H = card.H
		}
		for _, text := range tt.Texts() {
			if strings.HasPrefix(text, "设置面板 ") {
				if sheet, found := tt.Find(text); found {
					viewport = sheet
					viewport.Y += Space5 * 3
					viewport.H -= Space5 * 3
					if footer, found := tt.Find("面板操作栏 " + strings.TrimPrefix(text, "设置面板 ")); found {
						if r, ok := tt.Find(label); ok && settingsVisible(r) && r.Y >= footer.Y && r.Y+r.H <= footer.Y+footer.H {
							return
						}
						viewport.H = min(viewport.H, footer.Y-viewport.Y)
					}
				}
				break
			}
		}
		r, found := tt.Find(label)
		dy := float32(120)
		if i >= 40 {
			dy = -120
		}
		if found && settingsVisible(r) {
			x, y := r.X+r.W/2, r.Y+r.H/2
			if x >= viewport.X && x <= viewport.X+viewport.W && y >= viewport.Y && y <= viewport.Y+viewport.H {
				dy = y - (viewport.Y + viewport.H/2)
				if dy >= -40 && dy <= 40 || i > 0 && previousFound && r == previous {
					return
				}
			} else if y < viewport.Y {
				dy = -120
			} else if y > viewport.Y+viewport.H {
				dy = 120
			}
		}
		previous, previousFound = r, found
		tt.Scroll(viewport.X+viewport.W/2, viewport.Y+viewport.H/2, 0, dy)
		tt.Frame()
	}
	t.Fatalf("设置详情中找不到可见元素 %q: %v", label, tt.Texts())
}

func settingsClickModelPickerID(t *testing.T, tt *ui.Tester, id string) {
	t.Helper()
	waitFor(t, tt, func() bool {
		_, ok := tt.Find(id)
		return ok
	})
	r, _ := tt.Find(id)
	tt.ClickAt(r.X-Space2-1, r.Y+r.H/2)
}

func settingsClick(t *testing.T, tt *ui.Tester, label string) error {
	t.Helper()
	if r, ok := tt.Find(label); ok && settingsVisible(r) {
		x, y := r.X+r.W/2, r.Y+r.H/2
		if detail, found := tt.Find("设置详情列"); found {
			if x < detail.X && y >= detail.Y && y <= detail.Y+detail.H {
				return tt.Click(label)
			}
		}
	}
	settingsRevealInDetail(t, tt, label)
	return tt.Click(label)
}
