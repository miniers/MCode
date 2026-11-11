package mcodeui

import (
	"time"

	"github.com/egoist/mygo"
)

// previewPage 只暴露轮询需要的能力，测试可用阻塞页面替代原生页面。
type previewPage interface {
	Eval(string) (any, error)
	Destroy()
}

type nativePreviewPage struct {
	*mygo.Page
	web *mygo.WebView
}

func (p *nativePreviewPage) Destroy() { p.web.Destroy() }

type previewPageEvents interface {
	previewPage
	OnDidNavigate(func(string)) func()
	OnDidFinishLoad(func()) func()
}

type previewNavigablePage interface {
	previewPage
	LoadURL(string) error
	LoadHTML(string, string)
}

type previewPageToken struct {
	state              *browserTab
	pageID, generation uint64
	consoleRevision    uint64
	annotRevision      uint64
}

// previewSnapshot 是一帧共享的渲染数据；输入框只修改副本，再按令牌提交。
type previewSnapshot struct {
	token                            previewPageToken
	visible                          bool
	url, html                        string
	web                              *mygo.WebView
	addr, err                        string
	addressEditing                   bool
	canGoBack, canGoForward, loading bool
	webFailed                        bool
	viewport                         previewViewportMode
	consoleErrors                    []previewConsoleError
	consoleOpen                      bool
	annotating                       bool
	annotations                      []previewAnnotation
	annotOpen                        bool
}

// previewSnapshotLocked 调用方须持有 previewMu。
func previewSnapshotLocked(st *browserTab) previewSnapshot {
	return previewSnapshot{
		token: previewPageTokenLocked(st), visible: st.visible,
		url: st.url, html: st.html, web: st.web,
		addr: st.addr, err: st.err, webFailed: st.webFailed, viewport: st.viewport,
		addressEditing: st.addressEditing, canGoBack: st.canGoBack, canGoForward: st.canGoForward, loading: st.loading,
		consoleErrors: append([]previewConsoleError(nil), st.consoleErrors...),
		consoleOpen:   st.consoleOpen, annotating: st.annotating,
		annotations: append([]previewAnnotation(nil), st.annotations...), annotOpen: st.annotOpen,
	}
}

func previewPageTokenLocked(st *browserTab) previewPageToken {
	return previewPageToken{st, st.pageID, st.generation, st.consoleRevision, st.annotRevision}
}

func previewStateForTokenLocked(s *chatState, token previewPageToken) *browserTab {
	st := token.state
	if st == nil || st.pageID != token.pageID || st.generation != token.generation {
		return nil
	}
	return st
}

// previewAdvanceGenerationLocked 使旧页面结果失效，同时清理旧页的显示和轮询状态。
func previewAdvanceGenerationLocked(st *browserTab) {
	st.generation++
	st.consoleErrors = nil
	st.consoleOpen = false
	st.consolePolling = false
	st.consoleLastPoll = time.Time{}
	st.annotPolling = false
	previewResetAnnotationsLocked(st)
}

// previewBindPageCallbacks 每次换代重新绑定，使已排队的旧回调也不能操作新页。
func previewBindPageCallbacks(s *chatState, token previewPageToken, page previewPageEvents) {
	navigateOff := page.OnDidNavigate(func(target string) {
		previewMu.Lock()
		live := previewStateForTokenLocked(s, token) != nil
		previewMu.Unlock()
		if !live {
			return
		}
		back, forward := browserPageHistory(page)
		previewMu.Lock()
		st := previewStateForTokenLocked(s, token)
		if st == nil || st.page == nil {
			previewMu.Unlock()
			return
		}
		off := st.pageOff
		st.pageOff = nil
		previewAdvanceGenerationLocked(st)
		browserCommitNavigationLocked(st, target, back, forward)
		st.loading = true
		next := previewPageTokenLocked(st)
		previewMu.Unlock()
		if off != nil {
			off()
		}
		previewBindPageCallbacks(s, next, page)
		s.invalidate()
	})
	finishOff := page.OnDidFinishLoad(func() {
		previewMu.Lock()
		live := previewStateForTokenLocked(s, token) != nil
		previewMu.Unlock()
		if !live {
			return
		}
		captureConsole := previewConsoleCaptureEnabled(s)
		back, forward := browserPageHistory(page)
		previewMu.Lock()
		st := previewStateForTokenLocked(s, token)
		if st == nil || st.page == nil {
			previewMu.Unlock()
			return
		}
		st.loading = false
		st.canGoBack, st.canGoForward = back, forward
		if !captureConsole || !st.visible || st.consolePolling {
			previewMu.Unlock()
			s.invalidate()
			return
		}
		st.consolePolling = true
		st.consoleLastPoll = time.Now()
		current := previewPageTokenLocked(st)
		previewMu.Unlock()
		go previewPollConsoleErrors(s, current, page)
		s.invalidate()
	})
	titleOff := browserBindTitle(s, token, page)
	off := func() { navigateOff(); finishOff(); titleOff() }
	previewMu.Lock()
	st := previewStateForTokenLocked(s, token)
	if st != nil && st.page != nil {
		st.pageOff = off
		previewMu.Unlock()
		return
	}
	previewMu.Unlock()
	off()
}
