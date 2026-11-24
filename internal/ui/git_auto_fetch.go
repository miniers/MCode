package mcodeui

import (
	"context"
	"sync"
	"time"
)

const gitAutoFetchInterval = 5 * time.Minute

// gitPaneFetchThrottle 是全局节流表：同一仓库根在 gitAutoFetchInterval 内只抓取一次，
// Git 面板与聊天窗口的自动抓取共用，避免重复请求远端。
var gitPaneFetchThrottle = struct {
	sync.Mutex
	last map[string]time.Time
}{last: make(map[string]time.Time)}

func gitPaneFetchDue(last, now time.Time) bool {
	return last.IsZero() || now.Sub(last) >= gitAutoFetchInterval
}

// gitAutoFetchClaim 在节流窗口外时登记本次抓取并返回 true；调用方随后必须真正抓取。
func gitAutoFetchClaim(root string, now time.Time) bool {
	gitPaneFetchThrottle.Lock()
	defer gitPaneFetchThrottle.Unlock()
	if !gitPaneFetchDue(gitPaneFetchThrottle.last[root], now) {
		return false
	}
	gitPaneFetchThrottle.last[root] = now
	return true
}

// gitAutoFetchEnabled 读取「自动抓取」开关；设置尚未注册时按默认开启。
func gitAutoFetchEnabled(s *chatState) bool {
	st := getChatSettings(s)
	if st == nil {
		return true
	}
	st.cfgMu.Lock()
	defer st.cfgMu.Unlock()
	return st.cfg.SourceControl.AutoFetchEnabled()
}

// gitAutoFetch 对当前会话的仓库做一次后台抓取，成功后刷新 Git 状态；返回是否发起了抓取。
// 失败静默，与 Git 面板的后台抓取一致。
func (s *chatState) gitAutoFetch() bool {
	if s == nil || s.client == nil || !gitAutoFetchEnabled(s) {
		return false
	}
	s.mu.Lock()
	threadID, closed := s.selectedThreadID, s.closed
	s.mu.Unlock()
	if closed || threadID == "" {
		return false
	}
	state := s.headerGitState(threadID)
	if state.WorkDir == "" || state.RemoteURL == "" || state.Detached || threadGitSyncing(s, threadID) {
		return false
	}
	if !gitAutoFetchClaim(gitPaneWatchRoot(state), time.Now()) {
		return false
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if s.client.GitFetch(ctx, threadID) != nil || ctx.Err() != nil {
			return
		}
		s.loadGitState(threadID)
	}()
	return true
}

// startGitAutoFetch 每 gitAutoFetchInterval 在窗口激活时抓取一次；返回的 stop 在窗口关闭时调用，
// 幂等。用 time.Ticker 而非组件级动画，静止时不触发重绘。
func startGitAutoFetch(s *chatState) (stop func()) {
	done := make(chan struct{})
	var once sync.Once
	go func() {
		ticker := time.NewTicker(gitAutoFetchInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if s.windowActive.Load() {
					s.gitAutoFetch()
				}
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}
