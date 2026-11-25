package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

type gitPaneClient interface {
	GitStatusEntries(context.Context, string) ([]git.StatusEntry, error)
	GitPullWith(context.Context, string, string) error
	GitPushForceWithLease(context.Context, string) error
	GitOpState(context.Context, string) (git.OpState, error)
	GitContinueOp(context.Context, string) error
	GitSkipOp(context.Context, string) error
	GitAbortOp(context.Context, string) error
	GitRestoreSnapshot(context.Context, string, *git.Snapshot) error
}

// 分支浮层由分支模块接入；未接入时分支按钮不打开浮层。
var gitPaneBranchPopover func(c *ui.Context, s *chatState, threadID string, width float32, open *bool)

// 远端弹窗由远端模块接入。
var gitPaneRemotesDialog func(c *ui.Context, s *chatState, threadID string, open *bool)

const gitSwitchStashMarker = "mcode 切换前自动贮藏"

func gitPaneSwitchStash(entries []git.StashEntry, branch string) *git.StashEntry {
	if branch == "" {
		return nil
	}
	var latest *git.StashEntry
	for _, entry := range entries {
		if entry.Branch == branch && strings.HasSuffix(entry.Message, gitSwitchStashMarker) && (latest == nil || entry.Index < latest.Index) {
			copy := entry
			latest = &copy
		}
	}
	return latest
}

func gitPaneHasUnmergedBees(threadID string, state SwarmState) bool {
	if threadID == "" || state.LeadThreadID != threadID || state.IsBee {
		return false
	}
	for _, bee := range state.Bees {
		if bee.Status == "completed" && !bee.Merged {
			return true
		}
	}
	return false
}

type gitPaneState struct {
	mu       sync.Mutex
	owner    *chatState
	threadID string
	state    GitState
	entries  []git.StatusEntry
	op       git.OpState
	// pathsSig 是最近一次状态里改动路径的签名，改动面板据此在文件增删时重取「未提交」。
	pathsSig                     string
	syncing                      bool
	cancel                       context.CancelFunc
	progress                     git.Progress
	failure                      *git.Failure
	switchStash                  *git.StashEntry
	fetchPending                 bool
	refreshing, pending, loading bool
	refreshCancel                context.CancelFunc
	refreshSeq                   uint64
	visible, disposed            bool
	lifeCtx                      context.Context
	lifeCancel                   context.CancelFunc
	watchCancel                  context.CancelFunc
	watchRoot                    string
	undoLabel                    string
	undoSnap                     *git.Snapshot
	undoTimer                    *time.Timer
	undoSeq                      uint64
	// 浮层与弹窗只由 UI 线程读写。
	branchOpen, menuOpen, turnsOpen, forceOpen, detailOpen bool
	forceBranch                                            string
	stash                                                  *gitStashDialog
	prCreate                                               *gitPRCreateDialog
	remotesOpen                                            bool
}

func getGitPaneState(s *chatState) *gitPaneState {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	slot := inspectorSessionLocked(s, s.selectedThreadID)
	if slot.Git == nil {
		slot.Git = &gitPaneState{owner: s, threadID: s.selectedThreadID, progress: git.Progress{Percent: -1}}
	}
	return slot.Git
}

func toggleGitPane(s *chatState) {
	s.toggleGitTabScope("all")
}

func gitPaneAgentBusy(s *chatState, threadID string) bool {
	return s != nil && s.isThreadRunning(threadID)
}

func (ps *gitPaneState) fail(err error) {
	var failure *git.Failure
	if err != nil {
		f := git.ClassifyError(err)
		if f.Kind != git.FailureCanceled {
			failure = &f
		}
	}
	ps.mu.Lock()
	if !ps.disposed {
		ps.failure = failure
	}
	ps.mu.Unlock()
	ps.owner.invalidate()
}

// requestRefresh 将在途的任意多个请求合并为一次补跑。
func (ps *gitPaneState) requestRefresh() {
	client, ok := ps.owner.client.(gitPaneClient)
	if !ok || ps.threadID == "" {
		return
	}
	ps.mu.Lock()
	if ps.disposed {
		ps.mu.Unlock()
		return
	}
	if ps.refreshing {
		ps.pending = true
		ps.mu.Unlock()
		return
	}
	ps.refreshing = true
	ps.refreshSeq++
	seq := ps.refreshSeq
	ps.mu.Unlock()
	go ps.refresh(client, seq)
}

func (ps *gitPaneState) refresh(client gitPaneClient, seq uint64) {
	for {
		ps.mu.Lock()
		if ps.disposed || (ps.refreshSeq != seq && !ps.pending) {
			ps.refreshing = false
			ps.mu.Unlock()
			return
		}
		requestSeq := ps.refreshSeq
		ps.pending = false
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		ps.refreshCancel = cancel
		ps.mu.Unlock()
		timer := time.AfterFunc(150*time.Millisecond, func() {
			ps.mu.Lock()
			if !ps.disposed && ps.visible && ps.refreshing && ps.refreshSeq == requestSeq {
				ps.loading = true
			}
			ps.mu.Unlock()
			ps.owner.invalidate()
		})
		state, err := ps.owner.client.GitState(ctx, ps.threadID)
		var entries []git.StatusEntry
		var op git.OpState
		var switchStash *git.StashEntry
		if err == nil {
			entries, err = client.GitStatusEntries(ctx, ps.threadID)
		}
		if err == nil {
			op, err = client.GitOpState(ctx, ps.threadID)
		}
		if err == nil && state.HasStash && !state.Detached {
			if stashClient, ok := ps.owner.client.(gitStashClient); ok {
				if list, listErr := stashClient.GitStashList(ctx, ps.threadID); listErr == nil {
					switchStash = gitPaneSwitchStash(list, state.Branch)
				}
			}
		}
		cancel()
		timer.Stop()
		ps.mu.Lock()
		valid := !ps.disposed && ps.refreshSeq == requestSeq
		if valid {
			if err == nil {
				ps.state, ps.entries, ps.op = state, entries, op
				ps.switchStash = switchStash
				ps.pathsSig = strings.Join(gitChangesPaths(entries), "\x00")
			}
			if errors.Is(err, git.ErrNotRepo) {
				ps.state, ps.entries, ps.op = GitState{}, nil, git.OpState{}
				ps.switchStash = nil
			}
			if err != nil && !errors.Is(err, git.ErrNotRepo) && !errors.Is(err, context.Canceled) {
				f := git.ClassifyError(err)
				ps.failure = &f
			}
		}
		ps.loading = false
		fetch := valid && err == nil && ps.fetchPending
		if fetch {
			ps.fetchPending = false
		}
		ps.refreshCancel = nil
		again := ps.pending && !ps.disposed
		if !again {
			ps.refreshing = false
		} else {
			ps.refreshSeq++
			seq = ps.refreshSeq
		}
		ps.mu.Unlock()
		if valid && (err == nil || errors.Is(err, git.ErrNotRepo)) {
			root := gitPaneWatchRoot(state)
			if err != nil {
				root = ""
			}
			ps.ensureWatch(root)
		}
		ps.owner.invalidate()
		if fetch {
			ps.backgroundFetch()
		}
		if !again {
			return
		}
	}
}

func (ps *gitPaneState) setVisible(visible bool) {
	if visible {
		if _, ok := ps.owner.client.(gitPaneClient); !ok || ps.threadID == "" {
			return
		}
	}
	ps.mu.Lock()
	if ps.disposed || ps.visible == visible {
		ps.mu.Unlock()
		return
	}
	ps.visible = visible
	if !visible {
		if ps.lifeCancel != nil {
			ps.lifeCancel()
			ps.lifeCancel = nil
		}
		if ps.watchCancel != nil {
			ps.watchCancel()
			ps.watchCancel = nil
		}
		if ps.refreshCancel != nil {
			ps.refreshCancel()
		}
		ps.watchRoot = ""
		ps.pending, ps.loading = false, false
		ps.refreshSeq++ // 隐藏前的读取结果不得覆盖重新打开后的状态。
		ps.mu.Unlock()
		if value, ok := gitChangesStates.Load(ps); ok {
			st := value.(*gitChangesState)
			st.mu.Lock()
			st.autoGenerated = false
			st.mu.Unlock()
		}
		if ps.stash != nil {
			ps.stash.closeStashDialog()
		}
		if ps.prCreate != nil {
			ps.prCreate.close()
		}
		ps.owner.cancelPushPRConfirm()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	ps.lifeCtx, ps.lifeCancel = ctx, cancel
	root := gitPaneWatchRoot(ps.state)
	ps.mu.Unlock()
	ps.observe(ctx)
	ps.ensureWatch(root)
	ps.requestBackgroundFetch()
}

func (ps *gitPaneState) requestBackgroundFetch() {
	ps.mu.Lock()
	ps.fetchPending = true
	ps.mu.Unlock()
	ps.requestRefresh()
}

func (ps *gitPaneState) backgroundFetch() {
	if !gitAutoFetchEnabled(ps.owner) {
		return
	}
	ps.mu.Lock()
	if ps.disposed || !ps.visible || ps.syncing || ps.state.WorkDir == "" || ps.state.Detached || ps.lifeCtx == nil || ps.lifeCtx.Err() != nil {
		ps.mu.Unlock()
		return
	}
	root, lifeCtx := gitPaneWatchRoot(ps.state), ps.lifeCtx
	if !gitAutoFetchClaim(root, time.Now()) {
		ps.mu.Unlock()
		return
	}
	ps.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(lifeCtx, 30*time.Second)
		defer cancel()
		if ps.owner.client.GitFetch(ctx, ps.threadID) != nil || ctx.Err() != nil {
			return
		}
		ps.requestRefresh()
		ps.owner.loadGitState(ps.threadID)
	}()
}

func (ps *gitPaneState) observe(ctx context.Context) {
	stop := ps.owner.client.WatchThreadSummaries(func(th Thread) {
		if ctx.Err() == nil && th.ID == ps.threadID && !th.IsRunning && !th.SwarmBusy {
			ps.requestRefresh()
		}
	})
	var off func()
	if win := ps.owner.currentWindow(); win != nil {
		off = win.OnFocus(func() {
			if ctx.Err() == nil {
				ps.requestBackgroundFetch()
			}
		})
	}
	go func() {
		<-ctx.Done()
		if stop != nil {
			stop()
		}
		if off != nil {
			off()
		}
	}()
}

func gitPaneWatchIgnore(path string, _ bool) bool {
	path = strings.TrimPrefix(strings.ReplaceAll(path, "\\", "/"), "./")
	return path == ".git/objects" || strings.HasPrefix(path, ".git/objects/") ||
		path == ".git/logs" || strings.HasPrefix(path, ".git/logs/") ||
		path == "node_modules" || strings.HasPrefix(path, "node_modules/") || strings.Contains(path, "/node_modules/") || strings.HasSuffix(path, "/node_modules")
}

func gitPaneWatchRoot(state GitState) string {
	for _, worktree := range state.Worktrees {
		if worktree.Current && worktree.Path != "" {
			return worktree.Path
		}
	}
	return state.WorkDir
}

func (ps *gitPaneState) ensureWatch(root string) {
	ps.mu.Lock()
	if ps.disposed || !ps.visible || root == ps.watchRoot || ps.lifeCtx == nil || ps.lifeCtx.Err() != nil {
		ps.mu.Unlock()
		return
	}
	if ps.watchCancel != nil {
		ps.watchCancel()
	}
	ps.watchRoot, ps.watchCancel = root, nil
	if root == "" {
		ps.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(ps.lifeCtx)
	ps.watchCancel, ps.watchRoot = cancel, root
	ps.mu.Unlock()
	go func() {
		watcher, err := mygo.WatchFiles(ctx, root, mygo.FileWatchOptions{Recursive: true, HintsOnly: true, Debounce: DurBase, Ignore: gitPaneWatchIgnore})
		if err != nil {
			if ctx.Err() == nil {
				ps.fail(err)
			}
			return
		}
		defer watcher.Close()
		for {
			_, err := watcher.Next(ctx)
			if err != nil {
				if ctx.Err() == nil {
					ps.fail(err)
				}
				return
			}
			if ctx.Err() == nil {
				ps.requestRefresh()
			}
		}
	}()
}

func (ps *gitPaneState) dispose() {
	ps.setVisible(false)
	ps.mu.Lock()
	ps.disposed = true
	if ps.cancel != nil {
		ps.cancel()
	}
	if ps.refreshCancel != nil {
		ps.refreshCancel()
	}
	if ps.undoTimer != nil {
		ps.undoTimer.Stop()
		ps.undoTimer = nil
	}
	ps.mu.Unlock()
	if ps.stash != nil {
		ps.stash.closeStashDialog()
	}
	if ps.prCreate != nil {
		ps.prCreate.close()
	}
}

func (ps *gitPaneState) showUndo(label string, snap *git.Snapshot) {
	ps.mu.Lock()
	if ps.disposed {
		ps.mu.Unlock()
		return
	}
	ps.undoSeq++
	seq := ps.undoSeq
	ps.undoLabel, ps.undoSnap = label, snap
	if ps.undoTimer != nil {
		ps.undoTimer.Stop()
	}
	ps.undoTimer = time.AfterFunc(10*time.Second, func() {
		ps.mu.Lock()
		if ps.undoSeq == seq {
			ps.undoLabel, ps.undoSnap, ps.undoTimer = "", nil, nil
		}
		ps.mu.Unlock()
		ps.owner.invalidate()
	})
	ps.mu.Unlock()
	ps.owner.invalidate()
}

func gitPaneSyncAction(state GitState) (label, action string) {
	switch {
	case !state.HasUpstream:
		return "发布", "push"
	case state.Ahead > 0 && state.Behind > 0:
		return "同步", "sync"
	case state.Behind > 0:
		return fmt.Sprintf("拉取 ↓%d", state.Behind), "pull"
	case state.Ahead > 0:
		return fmt.Sprintf("推送 ↑%d", state.Ahead), "push"
	default:
		return "抓取", "fetch"
	}
}

func (ps *gitPaneState) run(action string) {
	client, ok := ps.owner.client.(gitPaneClient)
	if !ok {
		return
	}
	ps.mu.Lock()
	if ps.syncing || ps.disposed {
		ps.mu.Unlock()
		return
	}
	ps.syncing, ps.failure, ps.progress = true, nil, git.Progress{Percent: -1}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	ps.cancel = cancel
	state := ps.state
	undoSnap, undoSeq := ps.undoSnap, ps.undoSeq
	ps.mu.Unlock()
	ps.owner.invalidate()
	go func() {
		defer cancel()
		ctx = git.WithProgress(ctx, func(p git.Progress) {
			ps.mu.Lock()
			ps.progress = p
			ps.mu.Unlock()
			ps.owner.invalidate()
		})
		var err error
		toast := ""
		switch action {
		case "fetch":
			err = ps.owner.client.GitFetch(ctx, ps.threadID)
			toast = "已抓取"
		case "pull", "rebase", "merge", "sync", "retry":
			strategy := ""
			if action == "rebase" || action == "merge" {
				strategy = action
			}
			err = client.GitPullWith(ctx, ps.threadID, strategy)
			toast = "已拉取"
			if err == nil && (action == "sync" || action == "retry") {
				err = ps.owner.client.GitPush(ctx, ps.threadID)
				toast = "已同步"
			}
		case "push":
			err = ps.owner.client.GitPush(ctx, ps.threadID)
			if err == nil {
				ps.owner.loadGitState(ps.threadID)
			}
			toast = "已推送"
			if state.Branch != "" {
				toast += "到 " + state.Branch
			}
		case "restore-switch-stash":
			stashClient, ok := ps.owner.client.(gitStashClient)
			if !ok {
				err = errors.New("贮藏不可用")
				break
			}
			// 分支与贮藏序号都可能变化，恢复前重新核对。
			var current GitState
			current, err = ps.owner.client.GitState(ctx, ps.threadID)
			if err == nil && (current.Detached || current.Branch != state.Branch || current.WorkDir != state.WorkDir) {
				err = errors.New("分支已变化，请刷新后重试")
			}
			if err == nil && !current.HasStash {
				err = errors.New("切换前的贮藏已变化，请刷新后重试")
			}
			if err == nil {
				var entries []git.StashEntry
				entries, err = stashClient.GitStashList(ctx, ps.threadID)
				if err == nil {
					if entry := gitPaneSwitchStash(entries, current.Branch); entry != nil {
						err = stashClient.GitStashPopAt(ctx, ps.threadID, entry.Index)
					} else {
						err = errors.New("切换前的贮藏已变化，请刷新后重试")
					}
				}
			}
			toast = "已恢复改动"
		case "force":
			err = client.GitPushForceWithLease(ctx, ps.threadID)
			toast = "已强制推送"
		case "continue", "skip", "abort":
			if gitPaneAgentBusy(ps.owner, ps.threadID) {
				err = errors.New("Agent 正在修改此工作区")
				break
			}
			switch action {
			case "continue":
				err = client.GitContinueOp(ctx, ps.threadID)
				toast = "已继续"
			case "skip":
				err = client.GitSkipOp(ctx, ps.threadID)
				toast = "已跳过"
			case "abort":
				err = client.GitAbortOp(ctx, ps.threadID)
				toast = "已中止"
			}
		case "undo":
			if undoSnap != nil {
				err = client.GitRestoreSnapshot(ctx, ps.threadID, undoSnap)
				toast = "已撤销"
			}
			if err == nil {
				ps.mu.Lock()
				if ps.undoSeq == undoSeq {
					ps.undoLabel, ps.undoSnap = "", nil
				}
				ps.mu.Unlock()
			}
		}
		ps.mu.Lock()
		if err != nil && !ps.disposed {
			failure := git.ClassifyError(err)
			if failure.Kind != git.FailureCanceled {
				ps.failure = &failure
			}
		}
		ps.syncing, ps.cancel = false, nil
		disposed := ps.disposed
		ps.mu.Unlock()
		if disposed {
			return
		}
		if err == nil && toast != "" {
			ps.owner.setToast(toast)
		}
		ps.requestRefresh()
		ps.owner.invalidate()
	}()
}

func (ps *gitPaneState) stopSyncAndRefresh(s *chatState, dp *diffPanelState) {
	ps.mu.Lock()
	cancel := ps.cancel
	refreshCancel := ps.refreshCancel
	ps.pending = false
	ps.loading = false
	ps.refreshSeq++
	ps.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if refreshCancel != nil {
		refreshCancel()
	}
	if s != nil {
		s.cancelDiffLoad(dp)
	} else if dp != nil {
		diffPanelMu.Lock()
		dp.requestGen++
		dp.loading = false
		diffPanelMu.Unlock()
	}
}

func renderGitPane(c *ui.Context, s *chatState) {
	ps := getGitPaneState(s)
	if ps == nil {
		return
	}
	if ps.threadID == "" {
		renderGitPaneEmpty(c, "未选中会话", IconGitBranch())
		return
	}
	dp := getDiffPanelState(s)
	if dp == nil {
		return
	}
	diffPanelMu.Lock()
	if dp.scope == "" {
		dp.scope = "all"
	}
	scope := reviewScope(dp.scope)
	turnID := dp.turnID
	diffLoading := dp.loading
	hasLoadedReview := dp.requestGen > 0
	diffPanelMu.Unlock()

	_, hasGit := s.client.(gitPaneClient)
	_, modern := s.client.(reviewClient)
	_, hasHist := s.client.(gitHistoryClient)
	if !hasGit && scope == "all" && !(modern && hasLoadedReview) {
		renderGitPaneEmpty(c, "Git", IconGitBranch())
		return
	}
	if hasGit {
		ps.setVisible(true)
	}
	var state GitState
	var syncing, loading bool
	var progress git.Progress
	if hasGit {
		ps.mu.Lock()
		state, syncing, progress, loading = ps.state, ps.syncing, ps.progress, ps.loading
		ps.mu.Unlock()
	}
	notRepo := hasGit && scope == "all" && !loading && state.WorkDir == ""
	if hasGit && modern && hasHist && state.WorkDir != "" && scope == "all" {
		handleGitHistorySearchShortcut(c, gitHistoryStateFor(ps))
	}
	pane := ui.Column(c.Key("git-pane")).Fill().MinWidth(0)
	pane.Draw(func(_ *ui.Painter, r ui.Rect) {
		if updateReviewPanelWidth(dp, r.W) {
			c.Invalidate()
		}
	})
	width := pane.Bounds().W
	if width <= 0 {
		width = inspectorSession(s).Width
	}
	pane.Children(func() {
		if notRepo {
			renderGitPaneNotRepoEmpty(c, s, ps.threadID)
			return
		}
		if hasGit {
			renderGitPaneToolbar(c, s, ps, state, width)
			if syncing && progress.Percent >= 0 {
				ProgressBar(c, ProgressBarProps{Key: "git-pane-progress", Label: "Git 同步进度", Ratio: float32(progress.Percent) / 100, Height: Space0, Fill: ThemeAccent(c), Track: ThemeBorder(c)})
			}
			renderGitPaneOpBanner(c, s, ps)
			renderGitPaneFailure(c, ps)
			renderGitPaneSwitchStash(c, ps)
		}
		if modern {
			ui.Row(c.Key("git-pane-filter-bar")).
				FillWidth().
				MinWidth(0).
				Height(diffHeaderHeight).
				Shrink(0).
				Padding(0, Space2, 0, Space3).
				BorderWidth(0, 0, Hairline, 0).
				BorderColor(ThemeBorder(c)).
				AlignItems(ui.Center).
				Gap(Space1).
				Children(func() {
					if !hasGit {
						renderReviewScope(c, s, dp)
						ui.Spacer(c).Grow(1).Shrink(1).MinWidth(0)
						renderDiffHeaderActions(c, s, dp, ps.threadID, turnID, reviewTurns(s), diffLoading, true)
						return
					}
					renderGitPaneFilterBar(c, s, ps, dp, state, scope, hasHist)
				})
			if hasGit {
				renderGitPaneCommentsStrip(c, s, dp, ps.threadID)
			}
		}
		content := ui.Column(c.Key("git-pane-content")).Grow(1).MinHeight(0).FillWidth()
		contentH := content.Bounds().H
		if contentH <= 0 {
			_, winH := c.Size()
			contentH = max(0, winH-HeaderHeight-diffHeaderHeight*2)
		}
		content.Children(func() {
			if scope != "all" {
				if modern {
					inspectorFollowSurface(c, s, ps.threadID, "review-follow-surface").Children(func() {
						renderDiffPanelBody(c, s)
					})
				} else {
					renderDiffPanelBody(c, s)
				}
				return
			}
			if !hasGit {
				inspectorFollowSurface(c, s, ps.threadID, "review-follow-surface").Children(func() {
					renderReviewPanelBody(c, s, dp)
				})
				return
			}
			if loading && state.WorkDir == "" {
				LoadingIndicator(c, "git-pane-loading", "", IconMD)
				return
			}
			if state.WorkDir == "" {
				renderGitPaneNotRepoEmpty(c, s, ps.threadID)
				return
			}
			renderGitPaneAllScope(c, s, ps, dp, pane, contentH, modern)
		})
		if hasGit {
			renderGitPaneUndo(c, ps)
		}
	})
	if hasGit {
		renderGitPaneForceConfirm(c, ps)
		if ps.stash != nil {
			renderGitStashDialog(c, ps.stash)
		}
		renderGitPRCreateDialog(c, ps.prCreate)
		if gitPaneRemotesDialog != nil && ps.remotesOpen {
			gitPaneRemotesDialog(c, s, ps.threadID, &ps.remotesOpen)
		}
	}
}

func renderGitPaneFilterBar(c *ui.Context, s *chatState, ps *gitPaneState, dp *diffPanelState, state GitState, scope string, hasHist bool) {
	if scope != "all" || !hasHist || state.WorkDir == "" {
		renderReviewScope(c, s, dp)
		ui.Spacer(c).Grow(1).Shrink(1).MinWidth(0)
		return
	}
	h := gitHistoryStateFor(ps)
	h.mu.Lock()
	searchOpen, search, all, path, writeIndicator := h.searchOpen, h.search, h.opts.All, h.opts.Path, h.writeIndicator
	h.mu.Unlock()
	closeSearch := func() { gitHistoryCloseSearch(ps, h) }
	if searchOpen && c.Shortcut(0, ui.KeyEscape) {
		closeSearch()
		searchOpen = false
	}
	if searchOpen {
		renderGitHistorySearchField(c, ps, h, search, closeSearch)
		renderGitHistoryFileChip(c, ps, h, path)
		renderGitHistoryScopeDropdown(c, ps, h, all)
		if writeIndicator {
			LoadingIndicator(c, "git-history-writing", "", IconSM)
		}
		return
	}
	renderReviewScope(c, s, dp)
	ui.Spacer(c).Grow(1).Shrink(1).MinWidth(0)
	renderGitHistoryFileChip(c, ps, h, path)
	renderGitHistorySearchButton(c, ps, h)
	renderGitHistoryScopeDropdown(c, ps, h, all)
	if writeIndicator {
		LoadingIndicator(c, "git-history-writing", "", IconSM)
	}
}

func renderGitPaneCommentsStrip(c *ui.Context, s *chatState, dp *diffPanelState, tid string) {
	count, queueable, busy, show := reviewCommentsActionState(s, dp, tid)
	if !show {
		return
	}
	ui.Row(c.Key("git-pane-comments-strip")).
		FillWidth().
		Height(ControlLG).
		Shrink(0).
		Padding(0, Space3).
		BorderWidth(0, 0, Hairline, 0).
		BorderColor(ThemeBorder(c)).
		Background(ThemeAccent(c).Alpha(0.08)).
		AlignItems(ui.Center).
		Transition(motionCollapse(c)).
		Children(func() {
			renderReviewCommentsActionButton(c, s, dp, tid, count, queueable, busy)
		})
}

type gitTimelineRowKey string

func renderGitTimelineWipHead(c *ui.Context, tid string, files []ReviewFile, entries []git.StatusEntry, graphWidth float32, g *reviewGit, connectDown bool) {
	fileCount := len(files)
	if fileCount == 0 {
		fileCount = len(entries)
	}
	var totalAdd, totalDel int
	for _, f := range files {
		totalAdd += f.Additions
		totalDel += f.Deletions
	}
	ui.Row(c.Key("git-all-wip-head")).FillWidth().Height(RowHeight).AlignItems(ui.Stretch).Background(ThemeWindowBg(c)).Children(func() {
		renderGitTimelineRailNode(c, graphWidth, ThemeWindowBg(c), connectDown)
		ui.Row(c.Key("git-all-wip-head-content")).Grow(1).MinWidth(0).FillHeight().Padding(0, Space2, 0, Space1).Gap(Space2).AlignItems(ui.Center).BorderWidth(0, 0, Hairline, 0).BorderColor(ThemeBorder(c)).Children(func() {
			ui.Text(c, "未提交").FontSize(uiFont(TypeFootnote)).FontWeight(600).TextColor(ThemePrimaryText(c)).SingleLine()
			if fileCount > 0 {
				ui.Text(c, fmt.Sprintf("%d 个文件", fileCount)).FontSize(uiFont(TypeCaption)).TextColor(ThemeTertiaryText(c)).SingleLine().Shrink(1).MinWidth(0)
			}
			if totalAdd > 0 {
				ui.Text(c, "+"+strconv.Itoa(totalAdd)).Font("monospace").FontSize(uiFont(TypeCaption)).TextColor(ThemeSuccess(c)).Shrink(0)
			}
			if totalDel > 0 {
				ui.Text(c, "−"+strconv.Itoa(totalDel)).Font("monospace").FontSize(uiFont(TypeCaption)).TextColor(ThemeDanger(c)).Shrink(0)
			}
			ui.Spacer(c)
			if g != nil {
				groups := gitChangesGroups(entries)
				if len(groups[2].entries) > 0 {
					Button(c, ButtonOpts{
						Key:      "git-all-stage-all",
						Text:     "全部暂存",
						Variant:  ButtonGhost,
						Size:     ControlXS,
						Disabled: g.busy,
						OnClick: func() {
							gitChangesRun(g.ps, g.st, "group:2", func(ctx context.Context) error {
								return g.client.GitStageFiles(ctx, tid, gitChangesPaths(groups[2].entries))
							})
						},
					})
				} else if len(groups[1].entries) > 0 {
					Button(c, ButtonOpts{
						Key:      "git-all-unstage-all",
						Text:     "全部取消暂存",
						Variant:  ButtonGhost,
						Size:     ControlXS,
						Disabled: g.busy,
						OnClick: func() {
							gitChangesRun(g.ps, g.st, "group:1", func(ctx context.Context) error {
								return g.client.GitUnstageFiles(ctx, tid, gitChangesPaths(groups[1].entries))
							})
						},
					})
				}
			}
		})
	})
}

func renderGitPaneAllScope(c *ui.Context, s *chatState, ps *gitPaneState, dp *diffPanelState, host ui.Element, contentH float32, modern bool) {
	tid := ps.threadID
	var g *reviewGit
	var detail, errMsg string
	var snapshot ReviewSnapshot
	if modern {
		diffPanelMu.Lock()
		needLoad := dp.threadID != tid || dp.requestGen == 0
		diffPanelMu.Unlock()
		if needLoad {
			s.loadDiff(tid, "")
		}
		diffPanelMu.Lock()
		diffLoading := dp.loading
		detail = dp.gitDetail
		snapshot = dp.snapshot
		errMsg = dp.err
		diffPanelMu.Unlock()
		g = reviewGitFor(s, "all")
		if g != nil {
			reviewGitSyncPaths(s, dp, g, tid, diffLoading)
		}
	}
	surface := inspectorFollowSurface(c, s, tid, "review-follow-surface")
	if g != nil && detail != "" && gitPaneDiffView != nil {
		surface.Children(func() {
			gitPaneDiffView(c, s, tid, g.ps, detail, false, func() {
				diffPanelMu.Lock()
				dp.gitDetail = ""
				diffPanelMu.Unlock()
				s.loadDiff(tid, "")
			})
		})
		return
	}

	h := gitHistoryStateFor(ps)
	histClient, hasHist := s.client.(gitHistoryClient)
	var branch string
	var width, height float32
	var mode, sha, blamePath string
	if hasHist {
		branch, width, height, mode, sha, blamePath = syncGitHistoryState(c, s, ps, h, histClient, surface, contentH)
	} else {
		width = surface.Bounds().W
		if width <= 0 {
			width = inspectorSession(s).Width
			if width <= 0 {
				width, _ = c.Size()
			}
		}
	}

	surface.Children(func() {
		if hasHist && mode == "blame" {
			renderGitHistoryBlamePane(c, s, tid, ps, h, blamePath)
			return
		}
		if hasHist && mode == "commit" && width < 640 {
			renderGitHistoryNarrowCommitPane(c, s, ps, h, histClient, sha)
			return
		}

		listPane := ui.Column(c.Key("git-all-timeline-pane")).FillWidth().MinHeight(0)
		if hasHist && mode == "commit" {
			listPane.Height(max(RowHeight, height*.45)).Shrink(0)
		} else {
			listPane.Grow(1)
		}
		listPane.Children(func() {
			ps.mu.Lock()
			entries := append([]git.StatusEntry(nil), ps.entries...)
			ps.mu.Unlock()
			showComposer := false
			if g != nil {
				g.st.mu.Lock()
				showComposer = len(entries) > 0 || g.st.amend || time.Now().Before(g.st.successUntil)
				g.st.mu.Unlock()
			}
			var files []ReviewFile
			if modern && errMsg == "" && snapshot.Unavailable == "" {
				files = snapshot.Files
			}
			hasStatusErr := modern && (errMsg != "" || snapshot.Unavailable != "")
			hasWorkspace := len(files) > 0 || showComposer || hasStatusErr

			var commits []git.LogCommit
			var graph []git.GraphRow
			var loading, indicator, reset, more bool
			var query string
			if hasHist {
				h.mu.Lock()
				commits, graph, loading, indicator, query, reset, more = h.commits, h.graph, h.loading, h.showLoading, h.opts.Query, h.resetList, h.more
				h.resetList = false
				h.mu.Unlock()
			}
			if reset {
				h.list = ui.ListState{}
				h.selected = -1
			}
			if !hasWorkspace && len(commits) == 0 {
				if indicator {
					LoadingIndicator(c, "git-history-loading", "", IconSM)
				} else if !loading {
					label := "暂无提交"
					if query != "" {
						label = "没有匹配的提交"
					}
					renderGitPaneEmpty(c, label, IconGitBranch())
				}
				return
			}

			var nodeHeadRows, statusRowCount, composerRows int
			if hasWorkspace {
				nodeHeadRows = 1
			}
			if hasStatusErr && len(files) == 0 {
				statusRowCount = 1
			}
			if showComposer {
				composerRows = 1
			}
			filesOffset := nodeHeadRows + statusRowCount
			fileRowCount := 2 * len(files)
			composerOffset := filesOffset + fileRowCount
			commitOffset := composerOffset + composerRows
			totalRows := commitOffset + len(commits)
			graphWidth := gitGraphWidth(graph)

			if hasHist && len(commits) > 0 {
				h.list.Selected = &h.selected
			} else {
				h.list.Selected = nil
				h.selected = -1
			}
			h.list.Key = func(row int) any {
				switch {
				case nodeHeadRows == 1 && row == 0:
					return gitTimelineRowKey("wip-head")
				case statusRowCount == 1 && row == nodeHeadRows:
					return gitTimelineRowKey("wip-status")
				case row >= filesOffset && row < filesOffset+fileRowCount:
					rel := row - filesOffset
					return reviewRowKey{files[rel/2].Path, rel%2 == 1}
				case composerRows == 1 && row == composerOffset:
					return gitTimelineRowKey("composer")
				default:
					return commits[row-commitOffset].SHA
				}
			}
			h.list.Header = func(row int) bool {
				return row >= filesOffset && row < filesOffset+fileRowCount && (row-filesOffset)%2 == 0
			}
			h.list.Label = func(row int) string {
				switch {
				case nodeHeadRows == 1 && row == 0:
					return "未提交"
				case statusRowCount == 1 && row == nodeHeadRows:
					if errMsg != "" {
						return errMsg
					}
					return snapshot.Unavailable
				case row >= filesOffset && row < filesOffset+fileRowCount:
					rel := row - filesOffset
					if rel%2 == 0 {
						return files[rel/2].Path
					}
					return ""
				case composerRows == 1 && row == composerOffset:
					return "提交信息"
				default:
					return commits[row-commitOffset].Subject
				}
			}
			if reduceMotion(c) {
				h.list.MoveAnimation = nil
			} else {
				h.list.MoveAnimation = &springGentle
			}

			var ctrl reviewCardsController
			if len(files) > 0 {
				ctrl = prepareReviewCards(c, s, dp, host, tid, snapshot, g, reviewHost{}, &h.list, filesOffset, graphWidth)
			}

			list := ui.List(c.Key("git-all-timeline"), &h.list, totalRows, func(row int) {
				switch {
				case nodeHeadRows == 1 && row == 0:
					renderGitTimelineWipHead(c, tid, files, entries, graphWidth, g, totalRows > 1)
				case statusRowCount == 1 && row == nodeHeadRows:
					ui.Row(c.Key("git-all-wip-status")).FillWidth().AlignItems(ui.Stretch).Background(ThemeWindowBg(c)).Children(func() {
						if composerRows == 1 || len(commits) > 0 {
							renderGitTimelineRailLine(c, graphWidth)
						} else {
							ui.Box(c).Width(graphWidth).Shrink(0)
						}
						ui.Column(c.Key("git-all-wip-status-content")).Grow(1).MinWidth(0).Padding(Space3).BorderWidth(0, 0, Hairline, 0).BorderColor(ThemeBorder(c)).Children(func() {
							if errMsg != "" {
								renderDiffStatusState(c, "review-error", IconOctagonAlert(), errMsg, "", func() { s.loadDiff(tid, "") })
							} else {
								renderDiffStatusState(c, "review-unavailable", iconFileSearch, snapshot.Unavailable, "", nil)
							}
						})
					})
				case row >= filesOffset && row < filesOffset+fileRowCount:
					ctrl.renderRow(row - filesOffset)
				case composerRows == 1 && row == composerOffset:
					ui.Row(c.Key("git-all-composer-row")).FillWidth().AlignItems(ui.Stretch).Background(ThemeWindowBg(c)).Children(func() {
						renderGitTimelineRailLine(c, graphWidth)
						col := ui.Column(c.Key("git-all-composer-content")).Grow(1).MinWidth(0)
						if len(commits) > 0 {
							col.BorderWidth(0, 0, Hairline, 0).BorderColor(ThemeBorder(c))
						}
						col.Children(func() {
							renderGitCommitComposerCard(c, s, tid, g.ps, host)
						})
					})
				default:
					j := row - commitOffset
					renderGitHistoryRow(c, s, ps, h, histClient, branch, width, graphWidth, commits[j], graph[j], row == h.selected, j == 0 && hasWorkspace)
				}
			}).FillWidth().Grow(1).MinHeight(0)

			if h.selected >= 0 && h.selected < commitOffset {
				h.selected = -1
				c.Invalidate()
			}
			if hasHist && list.Submitted() && h.selected >= commitOffset && h.selected < commitOffset+len(commits) {
				openGitCommitDetail(ps, commits[h.selected-commitOffset].SHA)
			}
			if hasHist {
				_, last := h.list.Visible()
				if more && !loading && len(commits) > 0 && last >= commitOffset+len(commits)-20 {
					h.load(ps, histClient, false)
				}
				if indicator {
					LoadingIndicator(c, "git-history-more", "", IconSM)
				}
			}
		})

		if hasHist && mode == "commit" {
			ui.Column(c.Key("git-history-detail-pane")).FillWidth().Grow(1).MinHeight(0).BorderWidth(Hairline, 0, 0, 0).BorderColor(ThemeBorder(c)).Children(func() { renderGitHistoryDetail(c, s, ps, h, histClient) })
		}
	})

	if hasHist {
		renderGitHistoryDialog(c, s, ps, h, histClient)
	}
	if g != nil {
		renderGitChangesDialogsFor(c, s, tid, g.ps)
	}
}

func renderGitSyncSpin(c *ui.Context) {
	if reduceMotion(c) {
		ui.Icon(c, IconRefreshCw()).Size(IconSM, IconSM)
		return
	}
	color := c.Theme().Text
	ui.Box(c).Size(IconSM, IconSM).Draw(func(p *ui.Painter, r ui.Rect) {
		angle := float32(p.Now().UnixNano()%int64(4*DurBase)) / float32(4*DurBase) * 360
		p.Transform(ui.Rotation(angle), r.X+r.W/2, r.Y+r.H/2, func() {
			p.Icon(IconRefreshCw(), r, color)
		})
		p.AnimationFrameRate(30)
	})
}

// gitPaneMenuWidth 是 Git 面板更多菜单的固定列宽，能放下最长的「合并子代理改动（PR）」。
const gitPaneMenuWidth = 224

func renderGitPaneToolbar(c *ui.Context, s *chatState, ps *gitPaneState, state GitState, width float32) {
	ui.Row(c.Key("git-pane-toolbar")).FillWidth().Height(diffHeaderHeight).Shrink(0).Padding(0, Space3).Gap(Space2).AlignItems(ui.Center).BorderWidth(0, 0, Hairline, 0).BorderColor(ThemeBorder(c)).Children(func() {
		branch := state.Branch
		if state.Detached {
			branch = state.Head
			if len(branch) > 7 {
				branch = branch[:7]
			}
		}
		if branch == "" {
			branch = "Git"
		}
		btn := ui.ButtonBase(c.Key("git-pane-branch-btn")).Label(branch).Height(ControlMD).Padding(0, Space2).Radius(RadiusSM).MaxWidth(width * 0.4).MinWidth(0).Shrink(1).Gap(Space2).AlignItems(ui.Center).Transition(motionFeedback(c))
		scale := motionPressScale(c, btn, "git-pane-branch-press", btn.Pressed())
		btn.Scale(scale, scale)
		if btn.Hovered() {
			btn.Background(ThemeHover(c))
		}
		if btn.Clicked() && gitPaneBranchPopover != nil {
			ps.branchOpen = true
			s.invalidate()
		}
		btn.Children(func() {
			ui.Icon(c, IconGitBranch()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
			ui.Text(c, branch).FontSize(uiFont(TypeFootnote)).SingleLine().Shrink(1).MinWidth(0)
			if state.HasUpstream {
				if state.Ahead > 0 {
					ui.Text(c, fmt.Sprintf("↑%d", state.Ahead)).FontSize(uiFont(TypeCaption)).TextColor(ThemeSuccess(c))
				}
				if state.Behind > 0 {
					ui.Text(c, fmt.Sprintf("↓%d", state.Behind)).FontSize(uiFont(TypeCaption)).TextColor(ThemeWarning(c))
				}
			}
		})
		appPopover(c.Key("git-pane-branch-popover"), btn, &ps.branchOpen, nil, func() {
			if gitPaneBranchPopover != nil {
				gitPaneBranchPopover(c, s, ps.threadID, width, &ps.branchOpen)
			}
		})
		ui.Spacer(c)
		dp := getDiffPanelState(s)
		var diffLoading bool
		var turnID string
		scope := "all"
		if dp != nil {
			diffPanelMu.Lock()
			diffLoading = dp.loading
			turnID = dp.turnID
			scope = gitPaneCurrentScopeLocked(dp)
			diffPanelMu.Unlock()
		}
		ps.mu.Lock()
		syncing := ps.syncing
		ps.mu.Unlock()
		busy := syncing || diffLoading
		label, action := gitPaneSyncAction(state)
		iconOnly := state.WorkDir == "" || state.Detached || action == "fetch"
		btnLabel, tip := label, label
		if iconOnly {
			btnLabel, tip = "刷新", "刷新"
		}
		disabled := !busy && state.WorkDir == ""
		if state.WorkDir == "" && !busy {
			tip = "Git 状态不可用"
		}
		b := ui.ButtonBase(c.Key("git-pane-sync")).Label(btnLabel).Tooltip(tip).Height(ControlMD).Padding(0, Space2).Radius(RadiusSM).Gap(Space1).AlignItems(ui.Center).Disabled(disabled).Transition(motionFeedback(c))
		scale = motionPressScale(c, b, "git-pane-sync-press", !disabled && b.Pressed())
		b.Scale(scale, scale)
		if b.Hovered() {
			b.Background(ThemeHover(c))
		}
		stop := busy && b.Hovered()
		if busy {
			b.Label("停止").Tooltip("停止")
		}
		if b.Clicked() && !disabled {
			if busy {
				ps.stopSyncAndRefresh(s, dp)
			} else if state.Detached {
				s.loadDiff(ps.threadID, turnID)
				ps.requestRefresh()
			} else if action == "fetch" {
				s.loadDiff(ps.threadID, turnID)
				ps.requestRefresh()
				ps.run("fetch")
			} else {
				ps.run(action)
			}
		}
		b.Children(func() {
			switch {
			case stop:
				ui.Icon(c, IconSquare()).Size(IconSM, IconSM)
			case busy:
				renderGitSyncSpin(c)
			default:
				ui.Icon(c, IconRefreshCw()).Size(IconSM, IconSM)
			}
			if width >= KanbanBoardPaneMinWidth {
				switch {
				case stop:
					ui.Text(c, "停止").FontSize(uiFont(TypeFootnote))
				case busy:
					text := "刷新中"
					if syncing && !iconOnly {
						text = label
					}
					ui.Text(c, text).FontSize(uiFont(TypeFootnote))
				case !iconOnly:
					ui.Text(c, label).FontSize(uiFont(TypeFootnote))
				}
			}
		})
		more := IconButton(c, IconButtonProps{Key: "git-pane-more", Icon: IconMoreHorizontal(), Label: "Git 更多操作", Tooltip: "Git 更多操作", Variant: ButtonGhost, Size: ControlMD, OnClick: func() { ps.menuOpen = true; s.invalidate() }})
		appPopover(c.Key("git-pane-more-popover"), more, &ps.menuOpen, func() {
			s.cancelPushPRConfirm()
			ps.turnsOpen = false
		}, func() {
			repoURL := normalizeGitRemoteURL(state.RemoteURL)
			hs := getChatHeaderState(s)
			chatHeaderMu.Lock()
			pushBusy, pushConfirm := hs.pushBusy, hs.pushConfirm
			chatHeaderMu.Unlock()
			swarmPanelMu.Lock()
			canMergeBees := false
			if swarm := swarmPanelStates[ps.owner]; swarm != nil {
				canMergeBees = gitPaneHasUnmergedBees(ps.threadID, swarm.state)
			}
			swarmPanelMu.Unlock()
			_, modern := s.client.(reviewClient)
			var reviewData reviewHeaderMenuData
			if modern && dp != nil {
				reviewData = buildReviewHeaderMenuData(s, dp, ps.threadID, reviewTurns(s), turnID, scope)
			}
			_, winH := c.Size()
			popoverCard(c, ui.Column(c.Key("git-pane-menu")).Width(gitPaneMenuWidth)).Padding(Space1).Gap(Space0).Children(func() {
				// 菜单项左对齐，按刷新与同步、视图与轮次、暂存与贮藏、PR、仓库分组，组间用细分隔线；禁用时保留原因提示。
				option := func(key, label string, blocked, checked, danger bool, tip string, onClick func()) {
					el := menuRow(c, menuRowProps{
						Key:      "git-pane-menu-" + key,
						Label:    label,
						Title:    label,
						Checked:  checked,
						Danger:   danger,
						Disabled: blocked,
						OnClick: func() {
							ps.menuOpen = false
							ps.turnsOpen = false
							s.cancelPushPRConfirm()
							onClick()
						},
					})
					if tip != "" {
						el.Tooltip(tip)
					}
				}
				ui.Scroll(c.Key("git-pane-menu-scroll")).MaxHeight(max(120, winH-80)).FillWidth().Children(func() {
					ui.Column(c.Key("git-pane-menu-rows")).FillWidth().Gap(Space0).FocusGroup(ui.Vertical).Children(func() {
						// 1. 刷新与同步
						option("refresh", "刷新改动", ps.threadID == "", false, false, "", func() {
							s.loadDiff(ps.threadID, turnID)
							ps.requestRefresh()
						})
						syncTip := ""
						switch {
						case syncing:
							syncTip = "Git 操作进行中"
						case state.WorkDir == "":
							syncTip = "Git 状态不可用"
						case state.Detached:
							syncTip = "分离 HEAD 时无法执行此操作"
						}
						option("fetch", "抓取", syncTip != "", false, false, syncTip, func() { ps.run("fetch") })
						option("pull", "拉取", syncTip != "", false, false, syncTip, func() { ps.run("pull") })
						option("force", "强制推送…", syncTip != "", false, false, syncTip, func() {
							ps.mu.Lock()
							ps.forceBranch, ps.forceOpen = state.Branch, true
							ps.mu.Unlock()
							s.invalidate()
						})

						// 2. 视图与轮次
						if modern && dp != nil {
							gitMenuDivider(c)
							for _, it := range reviewData.viewItems {
								it := it
								tip := ""
								if it.disabled {
									tip = it.tooltip
								}
								option(it.key, it.label, it.disabled, it.checked, it.danger, tip, it.onClick)
							}
							if len(reviewData.turns) > 0 {
								currentTurn := "最新一轮"
								for _, opt := range reviewData.turns {
									if opt.checked {
										currentTurn = opt.label
										break
									}
								}
								menuRow(c, menuRowProps{
									Key:      "git-pane-menu-turns",
									Label:    "轮次",
									Title:    "轮次",
									Trailing: currentTurn + " ▸",
									OnClick: func() {
										ps.menuOpen = true
										ps.turnsOpen = !ps.turnsOpen
										s.invalidate()
									},
								})
								if ps.turnsOpen {
									for i, opt := range reviewData.turns {
										opt := opt
										option(fmt.Sprintf("turn-%d", i), opt.label, false, opt.checked, false, "", opt.onClick)
									}
								}
							}
						}

						// 3. 暂存与贮藏
						gitMenuDivider(c)
						for _, it := range reviewData.stageItems {
							it := it
							option(it.key, it.label, it.disabled, it.checked, it.danger, "", it.onClick)
						}
						stashTip := ""
						switch {
						case syncing:
							stashTip = "Git 操作进行中"
						case state.WorkDir == "":
							stashTip = "Git 状态不可用"
						}
						option("stash", "贮藏…", stashTip != "", false, false, stashTip, func() {
							if ps.stash == nil {
								ps.stash = &gitStashDialog{s: s, threadID: ps.threadID, confirmAt: -1}
							}
							ps.stash.openStashDialog()
						})
						if it := reviewData.discardAll; it != nil {
							option(it.key, it.label, it.disabled, it.checked, it.danger, "", it.onClick)
						}

						// 4. PR
						gitMenuDivider(c)
						prTip := ""
						if repoURL == "" {
							prTip = "没有可用的远端仓库地址"
						}
						option("pr-create", "创建 PR…", prTip != "", false, false, prTip, func() {
							openGitPRCreateDialog(s, ps.threadID)
							s.invalidate()
						})
						if canMergeBees {
							mergeTip := ""
							if syncing || pushBusy || state.WorkDir == "" || state.Detached {
								mergeTip = "Git 操作进行中或分支不可用"
							}
							option("push-pr", "合并子代理改动（PR）", mergeTip != "", false, false, mergeTip, func() {
								ps.menuOpen = true
								s.requestPushPRConfirm()
								s.invalidate()
							})
						}
						option("pr-view", "查看 Pull Request…", false, false, false, "", func() {
							s.openGitPRDialog()
							s.invalidate()
						})

						// 5. 仓库
						showRepo, showRemotes := repoURL != "", gitPaneRemotesDialog != nil
						if showRepo || showRemotes {
							gitMenuDivider(c)
							if showRepo {
								option("repo", "打开仓库", false, false, false, "", func() {
									c.OpenURL(repoURL)
									s.invalidate()
								})
							}
							if showRemotes {
								option("remotes", "远端…", false, false, false, "", func() {
									ps.remotesOpen = true
									s.invalidate()
								})
							}
						}
					})
				})
				if pushConfirm {
					renderGitPushPRConfirm(c, s, state)
				}
			})
		})
	})
}

func gitPaneOpTitle(op git.OpState) string {
	title := ""
	switch op.Kind {
	case git.OpRebase:
		title = "正在变基 " + op.Branch
		if op.Onto != "" {
			title += " → " + op.Onto
		}
	case git.OpMerge:
		title = "正在合并 " + op.Onto
	case git.OpCherryPick:
		title = "正在 cherry-pick"
	case git.OpRevert:
		title = "正在还原"
	}
	if op.Total > 0 {
		title += fmt.Sprintf(" · %d/%d", op.Step, op.Total)
	}
	return title
}

// gitPaneAIResolvePrompt 生成交给 AI 的冲突解决草稿：列出全部冲突文件，并要求只解决冲突、不推进操作。
func gitPaneAIResolvePrompt(conflicts []git.ConflictFile) string {
	var b strings.Builder
	b.WriteString("请解决以下文件的 Git 冲突，保留双方意图，删除所有冲突标记（<<<<<<<、=======、>>>>>>>），解决后用 git add 标记已解决。不要执行 merge --continue、commit 或 push，我会检查后再继续。\n\n")
	for _, file := range conflicts {
		b.WriteString("- " + file.Path + "\n")
	}
	return b.String()
}

func renderGitPaneOpBanner(c *ui.Context, s *chatState, ps *gitPaneState) {
	ps.mu.Lock()
	op, syncing := ps.op, ps.syncing
	ps.mu.Unlock()
	if op.Kind == "" {
		return
	}
	busy := gitPaneAgentBusy(s, ps.threadID)
	ui.Row(c.Key("git-pane-op-banner")).FillWidth().Height(ControlLG).Shrink(0).Radius(RadiusMD).Background(ThemeWarning(c).Alpha(0.12)).Padding(0, Space3).Gap(Space2).AlignItems(ui.Center).Transition(motionCollapse(c)).Children(func() {
		ui.Text(c, gitPaneOpTitle(op)).FontSize(uiFont(TypeFootnote)).SingleLine().Grow(1).Shrink(1).MinWidth(0)
		if len(op.Conflicts) > 0 {
			Button(c, ButtonOpts{Key: "git-pane-op-conflicts", Text: fmt.Sprintf("· %d 个冲突", len(op.Conflicts)), Variant: ButtonGhost, Size: ControlSM, OnClick: func() {
				openReviewUncommitted(s, op.Conflicts[0].Path)
			}})
			// 禁用条件与「中止」一致：Git 操作进行中或 Agent 正在修改工作区。
			tip := ""
			if syncing {
				tip = "Git 操作进行中"
			}
			if busy {
				tip = "Agent 正在修改此工作区"
			}
			Button(c, ButtonOpts{Key: "git-pane-op-ai-resolve", Text: "交给 AI 解决", Variant: ButtonGhost, Size: ControlSM, Disabled: syncing || busy, Tooltip: tip, OnClick: func() {
				s.sendDraft(ps.threadID, gitPaneAIResolvePrompt(op.Conflicts))
			}})
		}
		for _, item := range []struct{ label, action string }{{"中止", "abort"}, {"跳过", "skip"}, {"继续", "continue"}} {
			item := item
			if item.action == "skip" && op.Kind == git.OpMerge {
				continue
			}
			disabled := syncing || busy
			tip := ""
			if syncing {
				tip = "Git 操作进行中"
			}
			variant := ButtonGhost
			if item.action == "continue" {
				if len(op.Conflicts) > 0 {
					disabled = true
					tip = fmt.Sprintf("还有 %d 个冲突", len(op.Conflicts))
				} else {
					variant = ButtonPrimary
				}
			}
			if busy {
				tip = "Agent 正在修改此工作区"
			}
			Button(c, ButtonOpts{Key: "git-pane-op-" + item.action, Text: item.label, Size: ControlSM, Variant: variant, Disabled: disabled, Tooltip: tip, OnClick: func() { ps.run(item.action) }})
		}
	})
}

func renderGitPaneFailure(c *ui.Context, ps *gitPaneState) {
	ps.mu.Lock()
	failure, syncing := ps.failure, ps.syncing
	ps.mu.Unlock()
	if failure == nil || failure.Kind == git.FailureCanceled {
		return
	}
	tip := ""
	if syncing {
		tip = "Git 操作进行中"
	}
	ui.Column(c.Key("git-pane-error")).FillWidth().Padding(Space3).Gap(Space2).Radius(RadiusMD).Background(ThemeDanger(c).Alpha(0.12)).Transition(motionCollapse(c)).Children(func() {
		ui.Row(c).FillWidth().Gap(Space2).AlignItems(ui.Center).Children(func() {
			ui.Text(c, failure.Title).FontSize(uiFont(TypeBody)).Grow(1).Shrink(1).MinWidth(0)
			Button(c, ButtonOpts{Text: "详情", Variant: ButtonGhost, Size: ControlSM, OnClick: func() {
				ps.detailOpen = !ps.detailOpen
				if ps.owner != nil {
					ps.owner.invalidate()
				}
			}})
			IconButton(c, IconButtonProps{Icon: IconX(), Label: "关闭 Git 错误", Size: ControlSM, Variant: ButtonGhost, OnClick: func() { ps.fail(nil) }})
		})
		switch failure.Kind {
		case git.FailureDiverged:
			ui.Row(c).Gap(Space2).Children(func() {
				Button(c, ButtonOpts{Text: "变基拉取", Variant: ButtonSecondary, Disabled: syncing, Tooltip: tip, OnClick: func() { ps.run("rebase") }})
				Button(c, ButtonOpts{Text: "合并拉取", Variant: ButtonSecondary, Disabled: syncing, Tooltip: tip, OnClick: func() { ps.run("merge") }})
			})
		case git.FailureNoUpstream:
			Button(c, ButtonOpts{Text: "发布", Variant: ButtonSecondary, Disabled: syncing, Tooltip: tip, OnClick: func() { ps.run("push") }})
		case git.FailureRejected:
			Button(c, ButtonOpts{Text: "拉取并重试", Variant: ButtonSecondary, Disabled: syncing, Tooltip: tip, OnClick: func() { ps.run("retry") }})
		case git.FailureLease:
			Button(c, ButtonOpts{Text: "强制推送…", Variant: ButtonSecondary, Disabled: syncing, Tooltip: tip, OnClick: func() {
				ps.mu.Lock()
				ps.forceBranch, ps.forceOpen = ps.state.Branch, true
				ps.mu.Unlock()
				if ps.owner != nil {
					ps.owner.invalidate()
				}
			}})
		default:
			if failure.Hint != "" {
				ui.Text(c, failure.Hint).FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
			}
		}
		if ps.detailOpen {
			ui.Scroll(c.Key("git-pane-error-detail")).FillWidth().MaxHeight(inspectorDefaultWidth).Children(func() {
				ui.Text(c, failure.Detail).Font("monospace").FontSize(uiFont(TypeCode)).Selectable()
			})
		}
	})
}

func renderGitPaneSwitchStash(c *ui.Context, ps *gitPaneState) {
	ps.mu.Lock()
	entry, syncing := ps.switchStash, ps.syncing
	ps.mu.Unlock()
	if entry == nil {
		return
	}
	ui.Row(c.Key("git-pane-switch-stash")).FillWidth().Height(ControlLG).Shrink(0).Padding(0, Space3).Gap(Space2).AlignItems(ui.Center).Background(ThemeSelected(c)).Transition(motionCollapse(c)).Children(func() {
		ui.Text(c, "有切换分支前的改动").FontSize(uiFont(TypeFootnote)).Grow(1).Shrink(1).MinWidth(0)
		Button(c, ButtonOpts{Text: "恢复", Variant: ButtonGhost, Size: ControlSM, Disabled: syncing, OnClick: func() { ps.run("restore-switch-stash") }})
	})
}

func renderGitPaneForceConfirm(c *ui.Context, ps *gitPaneState) {
	ps.mu.Lock()
	blocked := ps.syncing || ps.state.Detached || ps.state.Branch != ps.forceBranch
	open, branch := ps.forceOpen, ps.forceBranch
	ps.mu.Unlock()
	tip := ""
	if blocked {
		tip = "Git 操作进行中或分支已变化"
	}
	RenderConfirmDialog(c, ConfirmDialog{
		Key:             "git-pane-force-dialog",
		Open:            open,
		Tone:            ConfirmDanger,
		Title:           "强制推送 " + branch + "？",
		ConfirmLabel:    "强制推送",
		CancelLabel:     "取消",
		CancelAutoFocus: true,
		ConfirmDisabled: blocked,
		ConfirmTooltip:  tip,
		OnConfirm: func() {
			ps.mu.Lock()
			ps.forceOpen = false
			ps.mu.Unlock()
			if ps.owner != nil {
				ps.owner.invalidate()
			}
			ps.run("force")
		},
		OnCancel: func() {
			ps.mu.Lock()
			ps.forceOpen = false
			ps.mu.Unlock()
			if ps.owner != nil {
				ps.owner.invalidate()
			}
		},
	})
}

func renderGitPaneUndo(c *ui.Context, ps *gitPaneState) {
	ps.mu.Lock()
	label, snap, syncing := ps.undoLabel, ps.undoSnap, ps.syncing
	ps.mu.Unlock()
	if snap == nil {
		return
	}
	tip := ""
	if syncing {
		tip = "Git 操作进行中"
	}
	ui.Row(c.Key("git-pane-undo")).FillWidth().Padding(Space3).Gap(Space2).AlignItems(ui.Center).Background(ThemeSelected(c)).Transition(motionCollapse(c)).Children(func() {
		ui.Text(c, label+" ·").FontSize(uiFont(TypeFootnote))
		Button(c, ButtonOpts{Text: "撤销", Variant: ButtonGhost, Size: ControlSM, Disabled: syncing, Tooltip: tip, OnClick: func() { ps.run("undo") }})
	})
}

func renderGitPaneEmpty(c *ui.Context, title string, icon *ui.SVG) {
	ui.Column(c.Key("git-pane-empty")).Fill().Center().Gap(Space3).Children(func() {
		EmptyState(c, EmptyStateProps{Icon: icon, Title: title})
	})
}

func renderGitPaneNotRepoEmpty(c *ui.Context, s *chatState, threadID string) {
	ui.Column(c.Key("git-pane-empty")).Fill().Center().Gap(Space3).Children(func() {
		var action func()
		if canGitInit(s) {
			initBusy := isGitInitBusy(s, threadID)
			threadRunning := s != nil && s.isThreadRunning(threadID)
			tooltip := ""
			if threadRunning {
				tooltip = "会话运行中，暂不可初始化"
			}
			btnText := "初始化 Git"
			if initBusy {
				btnText = "正在初始化…"
			}
			action = func() {
				Button(c, ButtonOpts{
					Key:      "git-pane-init-btn",
					Text:     btnText,
					Icon:     IconPlus(),
					Variant:  ButtonPrimary,
					Size:     ControlSM,
					Disabled: threadRunning,
					Loading:  initBusy,
					Tooltip:  tooltip,
					OnClick: func() {
						startGitInit(s, threadID)
					},
				})
			}
		}
		EmptyState(c, EmptyStateProps{Icon: IconGitBranch(), Title: "当前目录不是 Git 仓库", Action: action})
	})
}
