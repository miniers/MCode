package mcodeui

import (
	"sync"
	"sync/atomic"

	"github.com/egoist/mygo/ui"
)

const (
	textMeasureCacheMaxEntries = 1024
	textMeasureCacheMaxTextLen = 8192
)

type shapeTextCacheKey struct {
	text        string
	maxWidth    float32
	family      string
	size        float32
	weight      int
	italic      bool
	features    string
	lang        string
	lineHeight  float32
	thicken     bool
	antialiased bool
}

type measureTextCacheKey struct {
	maxWidth        float32
	themeFont       string
	themeFontSize   float32
	locale          string
	text            string
	font            string
	size            float32
	weight          int
	italic          bool
	letterSpacing   float32
	noLetterSpacing bool
	features        string
	lang            string
	objectWidth     float32
	objectHeight    float32
	objectDescent   float32
	baselineShift   float32
	paddingX        float32
	paddingStart    float32
}

type measureTextCacheValue struct {
	w float32
	h float32
}

type textMeasureCache struct {
	mu      sync.RWMutex
	shape   map[shapeTextCacheKey]*ui.TextLayout
	measure map[measureTextCacheKey]measureTextCacheValue

	shapeHits     atomic.Uint64
	shapeMisses   atomic.Uint64
	measureHits   atomic.Uint64
	measureMisses atomic.Uint64
}

var textMeasureCacheStore = &textMeasureCache{
	shape:   make(map[shapeTextCacheKey]*ui.TextLayout, 256),
	measure: make(map[measureTextCacheKey]measureTextCacheValue, 256),
}

func newShapeTextCacheKey(text string, font ui.Font, maxWidth float32) shapeTextCacheKey {
	return shapeTextCacheKey{
		text:        text,
		maxWidth:    maxWidth,
		family:      font.Family,
		size:        font.Size,
		weight:      font.Weight,
		italic:      font.Italic,
		features:    font.Features,
		lang:        font.Lang,
		lineHeight:  font.LineHeight,
		thicken:     font.Thicken,
		antialiased: font.Antialiased,
	}
}

func newMeasureTextCacheKey(maxWidth float32, themeFont string, themeFontSize float32, locale string, span ui.Span) measureTextCacheKey {
	return measureTextCacheKey{
		maxWidth:        maxWidth,
		themeFont:       themeFont,
		themeFontSize:   themeFontSize,
		locale:          locale,
		text:            span.Text,
		font:            span.Font,
		size:            span.Size,
		weight:          span.Weight,
		italic:          span.Italic,
		letterSpacing:   span.LetterSpacing,
		noLetterSpacing: span.NoLetterSpacing,
		features:        span.Features,
		lang:            span.Lang,
		objectWidth:     span.ObjectWidth,
		objectHeight:    span.ObjectHeight,
		objectDescent:   span.ObjectDescent,
		baselineShift:   span.BaselineShift,
		paddingX:        span.PaddingX,
		paddingStart:    span.PaddingStart,
	}
}

func evictHalfMap[K comparable, V any](m map[K]V) {
	evict := len(m) / 2
	for k := range m {
		delete(m, k)
		evict--
		if evict <= 0 {
			break
		}
	}
}

// cachedShapeText 缓存 ui.ShapeText 的有界结果，避免悬停跑马灯与列表重绘时重复做文本整形。
func cachedShapeText(text string, font ui.Font, maxWidth float32) *ui.TextLayout {
	if len(text) > textMeasureCacheMaxTextLen {
		textMeasureCacheStore.shapeMisses.Add(1)
		return ui.ShapeText(text, font, maxWidth)
	}
	key := newShapeTextCacheKey(text, font, maxWidth)

	textMeasureCacheStore.mu.RLock()
	layout, ok := textMeasureCacheStore.shape[key]
	textMeasureCacheStore.mu.RUnlock()
	if ok {
		textMeasureCacheStore.shapeHits.Add(1)
		return layout
	}

	layout = ui.ShapeText(text, font, maxWidth)
	textMeasureCacheStore.mu.Lock()
	if existing, exists := textMeasureCacheStore.shape[key]; exists {
		textMeasureCacheStore.mu.Unlock()
		textMeasureCacheStore.shapeHits.Add(1)
		return existing
	}
	if len(textMeasureCacheStore.shape) >= textMeasureCacheMaxEntries {
		evictHalfMap(textMeasureCacheStore.shape)
	}
	textMeasureCacheStore.shape[key] = layout
	textMeasureCacheStore.mu.Unlock()
	textMeasureCacheStore.shapeMisses.Add(1)
	return layout
}

// cachedMeasureText 缓存单 span 的 c.MeasureText 测量结果，按上下文主题字体与 Span 排版字段隔离键。
func cachedMeasureText(c *ui.Context, maxWidth float32, span ui.Span) (float32, float32) {
	if c == nil {
		return 0, 0
	}
	t := c.Theme()
	if t == nil {
		return 0, 0
	}
	if len(span.Text) > textMeasureCacheMaxTextLen {
		textMeasureCacheStore.measureMisses.Add(1)
		return c.MeasureText(maxWidth, span)
	}
	key := newMeasureTextCacheKey(maxWidth, t.Font, t.FontSize, c.Locale(), span)

	textMeasureCacheStore.mu.RLock()
	size, ok := textMeasureCacheStore.measure[key]
	textMeasureCacheStore.mu.RUnlock()
	if ok {
		textMeasureCacheStore.measureHits.Add(1)
		return size.w, size.h
	}

	w, h := c.MeasureText(maxWidth, span)
	textMeasureCacheStore.mu.Lock()
	if existing, exists := textMeasureCacheStore.measure[key]; exists {
		textMeasureCacheStore.mu.Unlock()
		textMeasureCacheStore.measureHits.Add(1)
		return existing.w, existing.h
	}
	if len(textMeasureCacheStore.measure) >= textMeasureCacheMaxEntries {
		evictHalfMap(textMeasureCacheStore.measure)
	}
	textMeasureCacheStore.measure[key] = measureTextCacheValue{w: w, h: h}
	textMeasureCacheStore.mu.Unlock()
	textMeasureCacheStore.measureMisses.Add(1)
	return w, h
}

func resetTextMeasureCache() {
	textMeasureCacheStore.mu.Lock()
	clear(textMeasureCacheStore.shape)
	clear(textMeasureCacheStore.measure)
	textMeasureCacheStore.mu.Unlock()
	textMeasureCacheStore.shapeHits.Store(0)
	textMeasureCacheStore.shapeMisses.Store(0)
	textMeasureCacheStore.measureHits.Store(0)
	textMeasureCacheStore.measureMisses.Store(0)
}

func textMeasureCacheStats() (shapeHits, shapeMisses, measureHits, measureMisses uint64) {
	return textMeasureCacheStore.shapeHits.Load(),
		textMeasureCacheStore.shapeMisses.Load(),
		textMeasureCacheStore.measureHits.Load(),
		textMeasureCacheStore.measureMisses.Load()
}

func textMeasureCacheLens() (shapeLen, measureLen int) {
	textMeasureCacheStore.mu.RLock()
	shapeLen = len(textMeasureCacheStore.shape)
	measureLen = len(textMeasureCacheStore.measure)
	textMeasureCacheStore.mu.RUnlock()
	return shapeLen, measureLen
}
