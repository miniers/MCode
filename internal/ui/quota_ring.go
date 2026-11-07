package mcodeui

import (
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
)

// 圆环以 24 点视图为基准，分别使用 10、6 点半径和 2.2 点线宽。
const quotaRingOuterRadius = 10.0
const quotaRingInnerRadius = 6.0
const quotaRingStrokeWidth = 2.2

var quotaRingCache = struct {
	sync.Mutex
	items map[string]*ui.SVG
}{items: make(map[string]*ui.SVG)}

func makeQuotaRingSVG(dark bool, outer, inner float64, hasOuter, hasInner bool, outerColor, innerColor ui.Color) *ui.SVG {
	outer = math.Round(math.Max(0, math.Min(1, outer))*100) / 100
	inner = math.Round(math.Max(0, math.Min(1, inner))*100) / 100
	key := fmt.Sprintf("%t|%.2f|%.2f|%t|%t|%v|%v", dark, outer, inner, hasOuter, hasInner, outerColor, innerColor)
	quotaRingCache.Lock()
	defer quotaRingCache.Unlock()
	if svg := quotaRingCache.items[key]; svg != nil {
		return svg
	}
	track := "rgba(0, 0, 0, 0.12)"
	if dark {
		track = "rgba(255, 255, 255, 0.12)"
	}
	var content strings.Builder
	draw := func(radius, fraction float64, color ui.Color) {
		circumference := 2 * math.Pi * radius
		fmt.Fprintf(&content, `<circle cx="12" cy="12" r="%g" fill="none" stroke="%s" stroke-width="%g"/>`, radius, track, quotaRingStrokeWidth)
		if fraction > 0 {
			fmt.Fprintf(&content, `<circle cx="12" cy="12" r="%g" fill="none" stroke="#%02X%02X%02X" stroke-width="%g" stroke-linecap="round" stroke-dasharray="%.2f %.2f" transform="rotate(-90 12 12)"/>`, radius, color.R, color.G, color.B, quotaRingStrokeWidth, circumference*fraction, circumference*(1-fraction))
		}
	}
	if hasOuter {
		draw(quotaRingOuterRadius, outer, outerColor)
	}
	if hasInner {
		draw(quotaRingInnerRadius, inner, innerColor)
	}
	svg := ui.MustParseSVG([]byte(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24">%s</svg>`, content.String())))
	if len(quotaRingCache.items) >= 512 {
		clear(quotaRingCache.items)
	}
	quotaRingCache.items[key] = svg
	return svg
}

type quotaRingWindow struct {
	label   string
	used    float64
	minutes int
	scope   int
}

func quotaRingWindows(provider string, windows []ProviderQuotaWindow) (outer, inner *quotaRingWindow) {
	for _, w := range windows {
		if w.Text || w.Unlimited || quotaWindowIsAuxiliary(w) {
			continue
		}
		label, minutes, ok := quotaStripWindowKey(w)
		if provider == "agy" {
			label, ok = agyQuotaStripWindowKey(w.Label)
			minutes = 5 * quotaHourMinutes
			if label == "周" {
				minutes = quotaWeekMinutes
			}
		}
		if !ok {
			continue
		}
		candidate := &quotaRingWindow{label: label, used: math.Max(0, math.Min(100, w.UsedPercent)), minutes: minutes, scope: quotaWindowScopeRank(w.Label)}
		var slot **quotaRingWindow
		if minutes <= quotaDayMinutes {
			slot = &outer
		} else if minutes >= quotaWeekMinutes {
			slot = &inner
		} else {
			continue
		}
		if *slot == nil || candidate.minutes < (*slot).minutes || candidate.minutes == (*slot).minutes && candidate.scope < (*slot).scope {
			*slot = candidate
		}
	}
	// 仅有周窗口时也使用外环，保持单窗口形态一致。
	if outer == nil && inner != nil {
		outer, inner = inner, nil
	}
	return
}

func renderComposerQuotaRing(c *ui.Context, key string, outer, inner *quotaRingWindow) {
	host := ui.Box(c.Key("composer-quota-ring-"+key)).Label("composer-quota-ring-"+key).Size(IconMD, IconMD)
	var outerValue, innerValue float32
	if outer != nil {
		outerValue = float32(outer.used / 100)
	}
	if inner != nil {
		innerValue = float32(inner.used / 100)
	}
	outerValue = host.AnimateTo("outer", outerValue, ui.Animation{Duration: animateDuration(c, DurSlow), Ease: ui.EaseOut})
	innerValue = host.AnimateTo("inner", innerValue, ui.Animation{Duration: animateDuration(c, DurSlow), Ease: ui.EaseOut})
	outerColor, innerColor := ThemeSecondaryText(c), ThemeSecondaryText(c)
	if outer != nil {
		outerColor = quotaLevelColor(c, outer.used)
	}
	if inner != nil {
		innerColor = quotaLevelColor(c, inner.used)
	}
	host.Children(func() {
		ui.Image(c, makeQuotaRingSVG(isDark(c), float64(outerValue), float64(innerValue), outer != nil, inner != nil, outerColor, innerColor)).Size(IconMD, IconMD).Fit(ui.Contain)
	})
}
