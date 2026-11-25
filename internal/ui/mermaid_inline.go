package mcodeui

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

const (
	mermaidInlineHostWidth   = 960
	mermaidInlineEvalTimeout = 10 * time.Second
	mermaidInlineCacheMax    = 64
)

type mermaidRenderStatus int

const (
	mermaidStatusPending mermaidRenderStatus = iota
	mermaidStatusDone
	mermaidStatusFailed
)

type mermaidCacheKey struct {
	digest [sha256.Size]byte
	dark   bool
}

type mermaidCacheEntry struct {
	status mermaidRenderStatus
	svg    *ui.SVG
	err    error
}

type mermaidRenderTask struct {
	key      mermaidCacheKey
	code     string
	dark     bool
	entry    *mermaidCacheEntry
	services ui.Services
}

type mermaidInlineRendererState struct {
	mu        sync.Mutex
	entries   map[mermaidCacheKey]*mermaidCacheEntry
	order     []mermaidCacheKey
	queue     []mermaidRenderTask
	running   bool
	seq       uint64
	idleTimer *time.Timer
	idleSeq   uint64

	win     *mygo.Window
	wv      *mygo.WebView
	wvHost  *mygo.Window
	loadCh  chan struct{}
	loadOff func()
}

var mermaidInlineRenderer = &mermaidInlineRendererState{
	entries: make(map[mermaidCacheKey]*mermaidCacheEntry),
}

// mermaidInlineIdleRelease 控制隐藏 WebView 在渲染队列空闲多久后自动释放。
var mermaidInlineIdleRelease = 60 * time.Second

// mermaidRenderSVG 在后台隐藏 WebView 中将 Mermaid 源码渲染为 SVG 字符串；测试替换它以隔离原生 WebView。
var mermaidRenderSVG = renderMermaidSVGWithWebView

// mermaidReleaseIdleWebView 触发空闲隐藏 WebView 回收；测试可替换以观测回收行为。
var mermaidReleaseIdleWebView = func() {
	mermaidInlineRenderer.releaseIdle()
}

func makeMermaidCacheKey(code string, dark bool) mermaidCacheKey {
	return mermaidCacheKey{
		digest: sha256.Sum256([]byte(code)),
		dark:   dark,
	}
}

// mermaidInlineHostHTML 构造只包含 Mermaid 运行时与固定宽度容器的离屏文档。
func mermaidInlineHostHTML(script []byte) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<style>
html,body{margin:0;padding:0;width:%dpx;}
#mermaid-host{width:%dpx;}
</style>
<script>%s</script>
</head>
<body>
<div id="mermaid-host"></div>
</body>
</html>`, mermaidInlineHostWidth, mermaidInlineHostWidth, escapeInlineScript(script))
}

// parseMermaidInlineSVG 解析 Mermaid 输出的 SVG，遇到解析失败或含 foreignObject 时判定为失败。
func parseMermaidInlineSVG(raw string) (*ui.SVG, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("mermaid inline: empty svg")
	}
	svg, err := ui.ParseSVG([]byte(raw))
	if err != nil {
		return nil, err
	}
	for _, tag := range svg.Unsupported() {
		if strings.Contains(strings.ToLower(tag), "foreignobject") {
			return nil, fmt.Errorf("mermaid inline: unsupported %s", tag)
		}
	}
	return svg, nil
}

func (r *mermaidInlineRendererState) ensureLoadedWebView() (*mygo.WebView, <-chan struct{}, error) {
	if !mygo.WebViewAvailable() {
		return nil, nil, errors.New("mermaid inline: webview unavailable")
	}

	r.mu.Lock()
	win := r.win
	if win == nil || win.IsDestroyed() {
		r.mu.Unlock()
		activeWin, _ := activeChatWindow()
		r.mu.Lock()
		if r.win == nil || r.win.IsDestroyed() {
			r.win = activeWin
		}
		win = r.win
	}
	if win == nil || win.IsDestroyed() {
		r.mu.Unlock()
		return nil, nil, errors.New("mermaid inline: no host window")
	}
	if r.wv != nil && !r.wv.IsDestroyed() && r.wvHost == win && r.loadCh != nil {
		wv, loadCh := r.wv, r.loadCh
		r.mu.Unlock()
		return wv, loadCh, nil
	}
	oldWV, oldOff := r.takeWebViewLocked()
	r.mu.Unlock()

	destroyMermaidWebView(oldWV, oldOff)

	script, err := readMermaidVendorScript()
	if err != nil {
		return nil, nil, err
	}
	wv, err := newPreviewWebView(win, mygo.WebViewOptions{
		Page: mygo.PageOptions{
			Untrusted:            true,
			BackgroundThrottling: mygo.ThrottlingOff,
		},
	})
	if err != nil {
		return nil, nil, err
	}
	page := wv.Page()
	if page == nil {
		wv.Destroy()
		return nil, nil, errors.New("mermaid inline: webview page unavailable")
	}

	loadCh := make(chan struct{})
	var loadOnce sync.Once
	off := page.OnDidFinishLoad(func() {
		loadOnce.Do(func() { close(loadCh) })
	})
	page.LoadHTML(mermaidInlineHostHTML(script), "")

	r.mu.Lock()
	r.wv = wv
	r.wvHost = win
	r.loadCh = loadCh
	r.loadOff = off
	r.mu.Unlock()

	return wv, loadCh, nil
}

func (r *mermaidInlineRendererState) takeWebViewLocked() (*mygo.WebView, func()) {
	wv, off := r.wv, r.loadOff
	r.wv, r.wvHost, r.loadCh, r.loadOff = nil, nil, nil, nil
	return wv, off
}

func destroyMermaidWebView(wv *mygo.WebView, off func()) {
	if off != nil {
		off()
	}
	if wv != nil {
		wv.Destroy()
	}
}

func (r *mermaidInlineRendererState) stopIdleTimerLocked() {
	r.idleSeq++
	if r.idleTimer != nil {
		r.idleTimer.Stop()
		r.idleTimer = nil
	}
}

func (r *mermaidInlineRendererState) scheduleIdleReleaseLocked() {
	r.stopIdleTimerLocked()
	delay := mermaidInlineIdleRelease
	releaseFn := mermaidReleaseIdleWebView
	if delay <= 0 || releaseFn == nil {
		return
	}
	seq := r.idleSeq
	r.idleTimer = time.AfterFunc(delay, func() {
		r.mu.Lock()
		stale := r.idleSeq != seq || r.running || len(r.queue) > 0
		if !stale {
			r.idleTimer = nil
		}
		r.mu.Unlock()
		if stale {
			return
		}
		releaseFn()
	})
}

func (r *mermaidInlineRendererState) releaseIdle() {
	r.mu.Lock()
	if r.running || len(r.queue) > 0 {
		r.mu.Unlock()
		return
	}
	r.idleTimer = nil
	wv, off := r.takeWebViewLocked()
	r.mu.Unlock()

	destroyMermaidWebView(wv, off)
}

func (r *mermaidInlineRendererState) discardWebView(wv *mygo.WebView) {
	r.mu.Lock()
	if r.wv != wv {
		r.mu.Unlock()
		return
	}
	_, off := r.takeWebViewLocked()
	r.mu.Unlock()

	destroyMermaidWebView(wv, off)
}

func (r *mermaidInlineRendererState) nextSeq() uint64 {
	r.mu.Lock()
	r.seq++
	seq := r.seq
	r.mu.Unlock()
	return seq
}

func renderMermaidSVGWithWebView(code string, dark bool) (string, error) {
	wv, loadCh, err := mermaidInlineRenderer.ensureLoadedWebView()
	if err != nil {
		return "", err
	}

	loadCtx, cancelLoad := context.WithTimeout(context.Background(), mermaidInlineEvalTimeout)
	defer cancelLoad()
	select {
	case <-loadCh:
	case <-loadCtx.Done():
		mermaidInlineRenderer.discardWebView(wv)
		return "", loadCtx.Err()
	}

	page := wv.Page()
	if page == nil || wv.IsDestroyed() {
		mermaidInlineRenderer.discardWebView(wv)
		return "", errors.New("mermaid inline: webview destroyed")
	}

	theme := "default"
	if dark {
		theme = "dark"
	}
	codeJSON, err := json.Marshal(code)
	if err != nil {
		return "", err
	}
	seq := mermaidInlineRenderer.nextSeq()
	js := fmt.Sprintf(
		`(async()=>{mermaid.initialize({startOnLoad:false,securityLevel:'strict',theme:%q,htmlLabels:false,flowchart:{htmlLabels:false}}); const r = await mermaid.render('m'+%d, %s); return r.svg})()`,
		theme, seq, string(codeJSON),
	)

	evalCtx, cancelEval := context.WithTimeout(context.Background(), mermaidInlineEvalTimeout)
	defer cancelEval()
	res, err := page.EvalContext(evalCtx, js)
	if err != nil {
		if wv.IsDestroyed() {
			mermaidInlineRenderer.discardWebView(wv)
		}
		return "", err
	}
	svg, ok := res.(string)
	if !ok || strings.TrimSpace(svg) == "" {
		return "", errors.New("mermaid inline: empty eval result")
	}
	return svg, nil
}

func (r *mermaidInlineRendererState) evictOldEntriesLocked() {
	if len(r.entries) <= mermaidInlineCacheMax {
		return
	}
	kept := r.order[:0]
	for _, k := range r.order {
		e, ok := r.entries[k]
		if !ok {
			continue
		}
		if len(r.entries) > mermaidInlineCacheMax && e.status != mermaidStatusPending {
			delete(r.entries, k)
			continue
		}
		kept = append(kept, k)
	}
	for i := len(kept); i < len(r.order); i++ {
		r.order[i] = mermaidCacheKey{}
	}
	r.order = kept
}

func (r *mermaidInlineRendererState) lookupOrStart(services ui.Services, win *mygo.Window, code string, dark bool) mermaidCacheEntry {
	key := makeMermaidCacheKey(code, dark)

	r.mu.Lock()
	if win != nil {
		r.win = win
	}
	if r.entries == nil {
		r.entries = make(map[mermaidCacheKey]*mermaidCacheEntry)
	}
	if e, ok := r.entries[key]; ok {
		snap := *e
		r.mu.Unlock()
		return snap
	}
	r.stopIdleTimerLocked()
	e := &mermaidCacheEntry{status: mermaidStatusPending}
	r.entries[key] = e
	r.order = append(r.order, key)
	r.evictOldEntriesLocked()
	r.queue = append(r.queue, mermaidRenderTask{
		key:      key,
		code:     code,
		dark:     dark,
		entry:    e,
		services: services,
	})
	if !r.running {
		r.running = true
		go r.runWorker()
	}
	snap := *e
	r.mu.Unlock()
	return snap
}

func (r *mermaidInlineRendererState) runWorker() {
	for {
		r.mu.Lock()
		if len(r.queue) == 0 {
			r.running = false
			r.scheduleIdleReleaseLocked()
			r.mu.Unlock()
			return
		}
		task := r.queue[0]
		copy(r.queue, r.queue[1:])
		r.queue[len(r.queue)-1] = mermaidRenderTask{}
		r.queue = r.queue[:len(r.queue)-1]
		r.mu.Unlock()

		var svg *ui.SVG
		rawSVG, err := mermaidRenderSVG(task.code, task.dark)
		if err == nil {
			svg, err = parseMermaidInlineSVG(rawSVG)
		}

		r.mu.Lock()
		if err != nil {
			task.entry.status = mermaidStatusFailed
			task.entry.svg = nil
			task.entry.err = err
		} else {
			task.entry.status = mermaidStatusDone
			task.entry.svg = svg
			task.entry.err = nil
		}
		r.evictOldEntriesLocked()
		r.mu.Unlock()

		if task.services != (ui.Services{}) {
			task.services.Invalidate()
		}
	}
}

func (r *mermaidInlineRendererState) getEntry(code string, dark bool) (mermaidCacheEntry, bool) {
	key := makeMermaidCacheKey(code, dark)
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[key]
	if !ok {
		return mermaidCacheEntry{}, false
	}
	return *e, true
}

func (r *mermaidInlineRendererState) resetForTest() {
	r.mu.Lock()
	r.stopIdleTimerLocked()
	wv, off := r.takeWebViewLocked()
	r.entries = make(map[mermaidCacheKey]*mermaidCacheEntry)
	r.order = nil
	r.queue = nil
	r.win = nil
	r.mu.Unlock()

	destroyMermaidWebView(wv, off)
}

func (s *chatState) isMermaidSourceShown(key string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	shown := s.mermaidShowSource[key]
	s.mu.Unlock()
	return shown
}

func (s *chatState) toggleMermaidSource(key string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.mermaidShowSource == nil {
		s.mermaidShowSource = make(map[string]bool)
	}
	s.mermaidShowSource[key] = !s.mermaidShowSource[key]
	s.mu.Unlock()
	s.invalidate()
}

func renderMermaidFallbackCodeBlock(c *ui.Context, s *chatState, b *markdown.CodeBlockView, code string) {
	ui.Column(c).AlignSelf(ui.Stretch).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Justify(ui.End).Padding(0, 0, Space1, 0).Children(func() {
			Button(c, ButtonOpts{
				Text:    "预览",
				Label:   "预览 Mermaid",
				Size:    ControlXS,
				Variant: ButtonSecondary,
				OnClick: func() {
					openPreviewMermaid(s, code)
				},
			})
		})
		markdown.DefaultCodeBlock(c, b)
	})
}

// renderMermaidCodeBlock 渲染时间线中的 mermaid 代码块：流式保持源码，闭合后内联显示 SVG 并支持源码/右栏预览切换。
func renderMermaidCodeBlock(c *ui.Context, s *chatState, b *markdown.CodeBlockView) {
	if b == nil {
		return
	}
	if b.Open {
		markdown.DefaultCodeBlock(c, b)
		return
	}

	code := b.Code()
	key := makeMermaidCacheKey(code, themeDark.Load())
	entry := mermaidInlineRenderer.lookupOrStart(c.Services(), s.currentWindow(), code, key.dark)
	if entry.status != mermaidStatusDone || entry.svg == nil {
		renderMermaidFallbackCodeBlock(c, s, b, code)
		return
	}

	toggleKey := fmt.Sprintf("%x:%d", key.digest, b.Index)
	showSource := s.isMermaidSourceShown(toggleKey)
	services := c.Services()
	cardKey := fmt.Sprintf("mermaid-inline-%d", b.Index)

	ui.Column(c.Key(cardKey)).
		Direction(ui.LeftToRight).
		AlignSelf(ui.Stretch).
		Background(ThemeSurface(c)).
		Border(1, ThemeBorder(c)).
		Radius(RadiusLG).
		Clip().
		Children(func() {
			ui.Row(c.Key(cardKey+"-header")).
				Height(codeBlockHeaderHeight).
				AlignItems(ui.Center).
				Padding(Space1, Space2).
				Gap(Space1).
				Background(ThemeHover(c)).
				BorderWidth(0, 0, 1, 0).
				BorderColor(ThemeBorder(c)).
				Children(func() {
					ui.Text(c, "mermaid").FontSize(scaledFont(TypeCaption)).TextColor(ThemeSecondaryText(c)).Unselectable()
					ui.Spacer(c)
					toggleText, toggleLabel := "源码", "显示 Mermaid 源码"
					if showSource {
						toggleText, toggleLabel = "图表", "显示 Mermaid 图表"
					}
					Button(c.Key(cardKey+"-toggle"), ButtonOpts{
						Text:    toggleText,
						Label:   toggleLabel,
						Size:    ControlXS,
						Variant: ButtonSecondary,
						OnClick: func() {
							s.toggleMermaidSource(toggleKey)
							if services != (ui.Services{}) {
								services.Invalidate()
							}
						},
					})
					Button(c.Key(cardKey+"-preview"), ButtonOpts{
						Text:    "预览",
						Label:   "预览 Mermaid",
						Size:    ControlXS,
						Variant: ButtonSecondary,
						OnClick: func() {
							openPreviewMermaid(s, code)
						},
					})
				})
			if showSource {
				b.Body(c)
				return
			}
			ui.Column(c.Key(cardKey + "-body")).
				AlignSelf(ui.Stretch).
				AlignItems(ui.Center).
				Padding(Space3).
				Cursor(ui.CursorPointer).
				OnClick(func() {
					openPreviewMermaid(s, code)
				}).
				Children(func() {
					img := ui.Image(c, entry.svg).MaxWidthPercent(100).Role(ui.RoleImage).Label("Mermaid 图表")
					if w, h := entry.svg.Size(); w > 0 && h > 0 {
						img.AspectRatio(w / h)
					}
				})
		})
}
