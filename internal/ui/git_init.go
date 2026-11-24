package mcodeui

import (
	"context"
	"time"

	"github.com/egoist/mygo"
)

// gitInitRunOnMain 调度初始化完成后的主线程 UI 状态切换；测试中可替换以避免无主循环时阻塞。
var gitInitRunOnMain = mygo.RunOnMain

type gitInitClient interface {
	GitInit(context.Context, string) error
}

// canGitInit 判断后端客户端是否支持初始化 Git 仓库。
func canGitInit(s *chatState) bool {
	if s == nil || s.client == nil {
		return false
	}
	_, ok := s.client.(gitInitClient)
	return ok
}

// isGitInitBusy 返回指定会话是否正在执行 Git 初始化。
func isGitInitBusy(s *chatState, threadID string) bool {
	if s == nil || threadID == "" {
		return false
	}
	hs := getChatHeaderState(s)
	chatHeaderMu.Lock()
	defer chatHeaderMu.Unlock()
	return hs.initBusy[threadID]
}

// startGitInit 在后台初始化指定会话工作区的 Git 仓库；
// 成功后依次刷新头部 Git 状态、Git 面板并打开右栏 Git 标签，失败时通过 toast 提示错误。
func startGitInit(s *chatState, threadID string) {
	if s == nil || s.client == nil || threadID == "" {
		return
	}
	client, ok := s.client.(gitInitClient)
	if !ok {
		return
	}
	if s.isThreadRunning(threadID) {
		return
	}
	hs := getChatHeaderState(s)
	chatHeaderMu.Lock()
	if hs.initBusy[threadID] {
		chatHeaderMu.Unlock()
		return
	}
	hs.initBusy[threadID] = true
	hs.initConfirmOpen = false
	chatHeaderMu.Unlock()
	s.invalidate()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := client.GitInit(ctx, threadID)
		chatHeaderMu.Lock()
		delete(hs.initBusy, threadID)
		chatHeaderMu.Unlock()
		if err != nil {
			s.setToast("初始化 Git 失败: " + err.Error())
			s.invalidate()
			return
		}
		s.setToast("已初始化 Git 仓库")
		s.loadGitState(threadID)
		if ps := getGitPaneState(s); ps != nil {
			ps.requestRefresh()
		}
		s.invalidate()
		gitInitRunOnMain(func() {
			inspectorSelect(s, sidePaneGitID, false)
		})
	}()
}
