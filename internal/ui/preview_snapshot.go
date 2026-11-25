package mcodeui

import (
	"strings"
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

func (p *nativePreviewPage) Destroy()                     { p.web.Destroy() }
func (p *nativePreviewPage) CapturePage() ([]byte, error) { return p.web.CapturePage() }

type previewCapturePage interface{ CapturePage() ([]byte, error) }

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
	consoleFilter                    string
	consoleSelected                  map[uint64]bool
	consoleResetSeq                  uint64
	annotating                       bool
	annotations                      []previewAnnotation
	annotSending                     bool
	annotFocus                       uint64
	annotPageHover                   uint64
	annotCollapsed                   bool
	feedbackThreadID                 string
	feedbackText                     string
	feedbackSending                  bool
	designPick                       previewDesignPickState
}

func previewFeedbackThreadIDLocked(st *browserTab, ownerThreadID string) string {
	if st == nil || !st.localPreview {
		return ""
	}
	ownerID := strings.TrimSpace(ownerThreadID)
	if ownerID == "" && st.owner != nil {
		ownerID = strings.TrimSpace(st.owner.threadID)
	}
	beeID := strings.TrimSpace(st.previewThreadID)
	if beeID != "" && beeID != ownerID {
		return beeID
	}
	if lp := strings.TrimSpace(st.localPath); lp != "" {
		ensurePreviewLocalPathBeesLoadedLocked()
		if mapped := strings.TrimSpace(previewLocalPathBees[lp]); mapped != "" {
			return mapped
		}
	}
	return ownerID
}

// previewSnapshotLocked 调用方须持有 previewMu。
func previewSnapshotLocked(st *browserTab) previewSnapshot {
	selected := make(map[uint64]bool, len(st.consoleSelected))
	for seq, value := range st.consoleSelected {
		selected[seq] = value
	}
	return previewSnapshot{
		token: previewPageTokenLocked(st), visible: st.visible,
		url: st.url, html: st.html, web: st.web,
		addr: st.addr, err: st.err, webFailed: st.webFailed, viewport: st.viewport,
		addressEditing: st.addressEditing, canGoBack: st.canGoBack, canGoForward: st.canGoForward, loading: st.loading,
		consoleErrors: append([]previewConsoleError(nil), st.consoleErrors...),
		consoleOpen:   st.consoleOpen, annotating: st.annotating,
		consoleFilter: st.consoleFilter, consoleSelected: selected, consoleResetSeq: st.consoleResetSeq,
		annotations: append([]previewAnnotation(nil), st.annotations...), annotSending: st.annotSending, annotFocus: st.annotFocus,
		annotPageHover: st.annotPageHover, annotCollapsed: st.annotCollapsed,
		feedbackThreadID: previewFeedbackThreadIDLocked(st, ""),
		feedbackText:     st.feedbackText,
		feedbackSending:  st.feedbackSending,
		designPick:       previewDesignPickClone(st.designPick),
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
	if st.pollCancel != nil {
		st.pollCancel()
		st.pollCancel = nil
	}
	st.consoleErrors = nil
	st.consoleOpen = false
	st.consolePolling = false
	st.consoleLastPoll = time.Time{}
	st.consoleResetSeq++
	// 隐藏也会换代但文档未变，保留水位；新文档由 drain 的 document 标识重置。
	st.annotPolling = false
	st.designPick = previewDesignPickState{}
	st.designPickValid = false
	st.designPickPolling = false
	st.designPickLastPoll = time.Time{}
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
		view := swarmStateViewFor(s)
		reduced := appReduceMotion.Load()
		back, forward := browserPageHistory(page)
		previewMu.Lock()
		st := previewStateForTokenLocked(s, token)
		if st == nil || st.page == nil {
			previewMu.Unlock()
			return
		}
		st.loading = false
		st.canGoBack, st.canGoForward = back, forward
		feedbackID := previewFeedbackThreadIDLocked(st, "")
		runDesignPick := feedbackID != ""
		finalized := runDesignPick && view != nil && view.finalized[feedbackID]
		var pickToken previewPageToken
		if runDesignPick {
			st.designPickPolling = true
			st.designPickLastPoll = time.Now()
			pickToken = previewPageTokenLocked(st)
		}
		runConsole := captureConsole && st.visible && !st.consolePolling
		var consoleToken previewPageToken
		if runConsole {
			st.consolePolling = true
			st.consoleLastPoll = time.Now()
			consoleToken = previewPageTokenLocked(st)
		}
		previewMu.Unlock()
		if runDesignPick {
			go previewInitDesignPickOnLoad(s, pickToken, page, reduced, finalized)
		}
		if runConsole {
			go previewPollConsoleErrors(s, consoleToken, page)
		}
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
