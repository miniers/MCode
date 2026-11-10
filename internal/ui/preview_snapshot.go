package mcodeui

import (
	"time"

	"github.com/egoist/mygo"
)

// previewPage 只暴露轮询需要的能力，测试可用阻塞页面替代原生页面。
type previewPage interface {
	Eval(string) (any, error)
}

type previewPageEvents interface {
	previewPage
	OnDidNavigate(func(string)) func()
	OnDidFinishLoad(func()) func()
}

type previewPageToken struct {
	pageID, generation uint64
	consoleRevision    uint64
	annotRevision      uint64
}

// previewSnapshot 是一帧共享的渲染数据；输入框只修改副本，再按令牌提交。
type previewSnapshot struct {
	token         previewPageToken
	visible       bool
	url, html     string
	web           *mygo.WebView
	width         float32
	addr, err     string
	webFailed     bool
	viewport      previewViewportMode
	consoleErrors []previewConsoleError
	consoleOpen   bool
	annotating    bool
	annotations   []previewAnnotation
	annotOpen     bool
}

// previewSnapshotLocked 调用方须持有 previewMu。
func previewSnapshotLocked(st *previewState) previewSnapshot {
	return previewSnapshot{
		token: previewPageTokenLocked(st), visible: st.visible,
		url: st.url, html: st.html, web: st.web, width: st.width,
		addr: st.addr, err: st.err, webFailed: st.webFailed, viewport: st.viewport,
		consoleErrors: append([]previewConsoleError(nil), st.consoleErrors...),
		consoleOpen:   st.consoleOpen, annotating: st.annotating,
		annotations: append([]previewAnnotation(nil), st.annotations...), annotOpen: st.annotOpen,
	}
}

func previewPageTokenLocked(st *previewState) previewPageToken {
	return previewPageToken{st.pageID, st.generation, st.consoleRevision, st.annotRevision}
}

func previewStateForTokenLocked(s *chatState, token previewPageToken) *previewState {
	st := previewStates[s]
	if st == nil || st.pageID != token.pageID || st.generation != token.generation {
		return nil
	}
	return st
}

// previewAdvanceGenerationLocked 使旧页面结果失效，同时清理旧页的显示和轮询状态。
func previewAdvanceGenerationLocked(st *previewState) {
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
	navigateOff := page.OnDidNavigate(func(string) {
		previewMu.Lock()
		st := previewStateForTokenLocked(s, token)
		if st == nil || st.page == nil {
			previewMu.Unlock()
			return
		}
		off := st.pageOff
		st.pageOff = nil
		previewAdvanceGenerationLocked(st)
		next := previewPageTokenLocked(st)
		previewMu.Unlock()
		if off != nil {
			off()
		}
		previewBindPageCallbacks(s, next, page)
		s.invalidate()
	})
	finishOff := page.OnDidFinishLoad(func() {
		if !previewConsoleCaptureEnabled(s) {
			return
		}
		previewMu.Lock()
		st := previewStateForTokenLocked(s, token)
		if st == nil || st.page == nil || st.consolePolling {
			previewMu.Unlock()
			return
		}
		st.consolePolling = true
		st.consoleLastPoll = time.Now()
		current := previewPageTokenLocked(st)
		previewMu.Unlock()
		go previewPollConsoleErrors(s, current, page)
	})
	off := func() { navigateOff(); finishOff() }
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
