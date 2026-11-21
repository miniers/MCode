package mcodeui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestTextMeasureCacheShapeAndMeasureHitInvalidation(t *testing.T) {
	t.Run("ShapeText命中、失效与超长文本透传", func(t *testing.T) {
		resetTextMeasureCache()
		baseFont := ui.Font{Size: 13, Weight: 500}
		l1 := cachedShapeText("hello cache", baseFont, 0)
		l2 := cachedShapeText("hello cache", baseFont, 0)
		if l1 == nil || l2 == nil {
			t.Fatal("cachedShapeText returned nil layout")
		}
		w1, h1 := l1.Size()
		w2, h2 := l2.Size()
		wantW, wantH := ui.ShapeText("hello cache", baseFont, 0).Size()
		if w1 != wantW || h1 != wantH || w2 != wantW || h2 != wantH {
			t.Fatalf("cachedShapeText size mismatch: got (%v,%v) (%v,%v), want (%v,%v)", w1, h1, w2, h2, wantW, wantH)
		}
		shapeHits, shapeMisses, _, _ := textMeasureCacheStats()
		if shapeMisses != 1 || shapeHits < 1 {
			t.Fatalf("unexpected shape stats after hit: hits=%d misses=%d", shapeHits, shapeMisses)
		}

		variants := []struct {
			name     string
			text     string
			font     ui.Font
			maxWidth float32
		}{
			{"字号变化", "hello cache", ui.Font{Size: 16, Weight: 500}, 0},
			{"字体变化", "hello cache", ui.Font{Family: "monospace", Size: 13, Weight: 500}, 0},
			{"文本变化", "hello cache updated", baseFont, 0},
			{"字重变化", "hello cache", ui.Font{Size: 13, Weight: 700}, 0},
			{"限宽变化", "hello cache", baseFont, 40},
		}
		prevMisses := shapeMisses
		for _, tc := range variants {
			got := cachedShapeText(tc.text, tc.font, tc.maxWidth)
			gw, gh := got.Size()
			ww, wh := ui.ShapeText(tc.text, tc.font, tc.maxWidth).Size()
			if gw != ww || gh != wh {
				t.Fatalf("%s: size mismatch got (%v,%v), want (%v,%v)", tc.name, gw, gh, ww, wh)
			}
			_, curMisses, _, _ := textMeasureCacheStats()
			if curMisses != prevMisses+1 {
				t.Fatalf("%s: expected miss increment %d -> %d, got %d", tc.name, prevMisses, prevMisses+1, curMisses)
			}
			prevMisses = curMisses
		}

		shapeLenBefore, _ := textMeasureCacheLens()
		longText := strings.Repeat("a", textMeasureCacheMaxTextLen+1)
		longLayout := cachedShapeText(longText, baseFont, 0)
		lw, lh := longLayout.Size()
		ww, wh := ui.ShapeText(longText, baseFont, 0).Size()
		if lw != ww || lh != wh {
			t.Fatalf("long text passthrough size mismatch: got (%v,%v), want (%v,%v)", lw, lh, ww, wh)
		}
		shapeLenAfter, _ := textMeasureCacheLens()
		if shapeLenAfter != shapeLenBefore {
			t.Fatalf("long text should not enter cache: len %d -> %d", shapeLenBefore, shapeLenAfter)
		}
	})

	t.Run("MeasureText重复帧命中、参数失效与nil上下文回退", func(t *testing.T) {
		resetTextMeasureCache()
		if w, h := cachedMeasureText(nil, 0, ui.Span{Text: "nil"}); w != 0 || h != 0 {
			t.Fatalf("expected (0,0) for nil context, got (%v,%v)", w, h)
		}

		type step struct {
			maxWidth float32
			span     ui.Span
		}
		cur := step{
			maxWidth: 0,
			span:     ui.Span{Text: "100% 文本测量", Size: 13},
		}
		var gotW, gotH, wantW, wantH float32
		tt := ui.NewTester(func(c *ui.Context) {
			gotW, gotH = cachedMeasureText(c, cur.maxWidth, cur.span)
			wantW, wantH = c.MeasureText(cur.maxWidth, cur.span)
		}, 400, 120)

		if gotW != wantW || gotH != wantH {
			t.Fatalf("frame 1 mismatch: got (%v,%v), want (%v,%v)", gotW, gotH, wantW, wantH)
		}
		_, _, hits1, misses1 := textMeasureCacheStats()
		if misses1 != 1 {
			t.Fatalf("expected 1 miss after initial frame, got hits=%d misses=%d", hits1, misses1)
		}

		tt.Frame()
		tt.Frame()
		_, _, hits3, misses3 := textMeasureCacheStats()
		if misses3 != 1 || hits3 < hits1+2 {
			t.Fatalf("expected repeated frames to hit cache: hits %d -> %d, misses=%d", hits1, hits3, misses3)
		}

		invalidations := []struct {
			name string
			next step
		}{
			{"宽度变化", step{maxWidth: 36, span: ui.Span{Text: "100% 文本测量", Size: 13}}},
			{"字号变化", step{maxWidth: 36, span: ui.Span{Text: "100% 文本测量", Size: 17}}},
			{"文本变化", step{maxWidth: 36, span: ui.Span{Text: "100% 新文本", Size: 17}}},
			{"特性变化", step{maxWidth: 0, span: ui.Span{Text: "100%", Size: 13, Features: "tnum"}}},
		}
		prevMisses := misses3
		for _, tc := range invalidations {
			cur = tc.next
			tt.Frame()
			if gotW != wantW || gotH != wantH {
				t.Fatalf("%s: got (%v,%v), want (%v,%v)", tc.name, gotW, gotH, wantW, wantH)
			}
			_, _, _, curMisses := textMeasureCacheStats()
			if curMisses != prevMisses+1 {
				t.Fatalf("%s: expected miss increment %d -> %d, got %d", tc.name, prevMisses, prevMisses+1, curMisses)
			}
			prevMisses = curMisses
		}
	})

	t.Run("容量上限约束", func(t *testing.T) {
		resetTextMeasureCache()
		font := ui.Font{Size: 13}
		var ctx *ui.Context
		tt := ui.NewTester(func(c *ui.Context) {
			ctx = c
			for i := 0; i < textMeasureCacheMaxEntries+200; i++ {
				text := fmt.Sprintf("row-%d", i)
				cachedShapeText(text, font, 0)
				cachedMeasureText(c, 0, ui.Span{Text: text, Size: 13})
			}
		}, 200, 80)
		tt.Frame()
		if ctx == nil {
			t.Fatal("tester context was nil")
		}
		shapeLen, measureLen := textMeasureCacheLens()
		if shapeLen == 0 || shapeLen > textMeasureCacheMaxEntries {
			t.Fatalf("shape cache len out of bounds: %d (max %d)", shapeLen, textMeasureCacheMaxEntries)
		}
		if measureLen == 0 || measureLen > textMeasureCacheMaxEntries {
			t.Fatalf("measure cache len out of bounds: %d (max %d)", measureLen, textMeasureCacheMaxEntries)
		}
	})

	t.Run("renderSidebarScrollingTitle悬停多帧重绘命中ShapeText缓存", func(t *testing.T) {
		resetSidebarHoverCardForTest()
		setAppReduceMotion(t, false)
		resetTextMeasureCache()
		longTitle := strings.Repeat("侧栏跑马灯缓存命中测试标题", 4)
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Row(c).Width(100).Children(func() {
				renderSidebarScrollingTitle(c, "cache-marquee", longTitle, true, 0, FontSizeThreadTitle, 0, ThemePrimaryText(c))
			})
		}, 120, 40)

		hits1, misses1, _, _ := textMeasureCacheStats()
		if misses1 != 1 {
			t.Fatalf("expected 1 ShapeText miss on initial frame, got %d", misses1)
		}

		for i := 0; i < 4; i++ {
			tt.Frame()
		}
		hitsAfter, missesAfter, _, _ := textMeasureCacheStats()
		if missesAfter != misses1 {
			t.Fatalf("ShapeText misses should not grow across hover frames: %d -> %d", misses1, missesAfter)
		}
		if hitsAfter < hits1+4 {
			t.Fatalf("expected ShapeText hits to grow by at least 4: %d -> %d", hits1, hitsAfter)
		}
	})
}
