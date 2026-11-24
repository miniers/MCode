package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"mcode/internal/git"
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
		st, stateErr := fetchGitInitState(ctx, s, threadID)
		toast := "已初始化 Git 仓库"
		if stateErr == nil {
			if branch := strings.TrimSpace(st.Branch); branch != "" {
				toast = fmt.Sprintf("已初始化 Git 仓库 (%s)", branch)
			}
		}
		s.setToast(toast)
		if ps := getGitPaneState(s); ps != nil {
			if stateErr == nil {
				ps.mu.Lock()
				if !ps.disposed && ps.threadID == threadID {
					ps.state = st
				}
				ps.mu.Unlock()
			}
			ps.requestRefresh()
		}
		if dp := getDiffPanelState(s); dp != nil {
			diffPanelMu.Lock()
			if dp.scope == "" {
				dp.scope = "all"
			}
			diffPanelMu.Unlock()
		}
		s.loadDiff(threadID, "")
		s.invalidate()
		gitInitRunOnMain(func() {
			inspectorSelect(s, sidePaneGitID, false)
		})
	}()
}

// fetchGitInitState 在初始化成功后同步拉取最新 Git 状态并写入头部缓存。
func fetchGitInitState(ctx context.Context, s *chatState, threadID string) (GitState, error) {
	hs := getChatHeaderState(s)
	chatHeaderMu.Lock()
	hs.gitSeq[threadID]++
	seq := hs.gitSeq[threadID]
	chatHeaderMu.Unlock()

	st, err := s.client.GitState(ctx, threadID)
	chatHeaderMu.Lock()
	if hs.gitSeq[threadID] == seq {
		if err != nil {
			delete(hs.git, threadID)
			delete(hs.pr, threadID)
			if errors.Is(err, git.ErrNotRepo) {
				hs.notGitRepo[threadID] = true
			} else {
				delete(hs.notGitRepo, threadID)
			}
		} else {
			delete(hs.notGitRepo, threadID)
			prev, hadPrev := hs.git[threadID]
			branch := strings.TrimSpace(st.Branch)
			remote := strings.TrimSpace(st.RemoteURL)
			hs.git[threadID] = st
			if !hadPrev || strings.TrimSpace(prev.Branch) != branch || st.Detached || branch == "" || remote == "" {
				delete(hs.pr, threadID)
			}
			if _, hasPRClient := s.client.(gitPRBranchClient); !hasPRClient {
				delete(hs.pr, threadID)
			}
		}
	}
	chatHeaderMu.Unlock()
	return st, err
}
