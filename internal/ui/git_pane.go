package mcodeui

import (
	"context"
	"errors"
	"fmt"
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

var gitPaneFetchThrottle = struct {
	sync.Mutex
	last map[string]time.Time
}{last: make(map[string]time.Time)}

func gitPaneFetchDue(last, now time.Time) bool {
	return last.IsZero() || now.Sub(last) >= 5*time.Minute
}

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
	branchOpen, menuOpen, forceOpen, detailOpen bool
	forceBranch                                 string
	stash                                       *gitStashDialog
	prCreate                                    *gitPRCreateDialog
	remotesOpen                                 bool
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
	if getGitPaneState(s) != nil {
		inspectorSelect(s, sidePaneGitID, true)
	}
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
	ps.mu.Lock()
	if ps.disposed || !ps.visible || ps.syncing || ps.state.WorkDir == "" || ps.state.Detached || ps.lifeCtx == nil || ps.lifeCtx.Err() != nil {
		ps.mu.Unlock()
		return
	}
	root, lifeCtx := gitPaneWatchRoot(ps.state), ps.lifeCtx
	gitPaneFetchThrottle.Lock()
	now := time.Now()
	if !gitPaneFetchDue(gitPaneFetchThrottle.last[root], now) {
		gitPaneFetchThrottle.Unlock()
		ps.mu.Unlock()
		return
	}
	gitPaneFetchThrottle.last[root] = now
	gitPaneFetchThrottle.Unlock()
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
	if ps.disposed || !ps.visible || root == ps.watchRoot {
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

func renderGitPane(c *ui.Context, s *chatState) {
	ps := getGitPaneState(s)
	if ps == nil {
		return
	}
	if ps.threadID == "" {
		renderGitPaneEmpty(c, "未选中会话", IconGitBranch())
		return
	}
	if _, ok := s.client.(gitPaneClient); !ok {
		renderGitPaneEmpty(c, "Git", IconGitBranch())
		return
	}
	ps.setVisible(true)
	ps.mu.Lock()
	state, syncing, progress, loading := ps.state, ps.syncing, ps.progress, ps.loading
	ps.mu.Unlock()
	pane := ui.Column(c.Key("git-pane")).Fill().MinWidth(0)
	width := pane.Bounds().W
	if width <= 0 {
		width = inspectorSession(s).Width
	}
	pane.Children(func() {
		renderGitPaneToolbar(c, s, ps, state, width)
		if syncing && progress.Percent >= 0 {
			ProgressBar(c, ProgressBarProps{Key: "git-pane-progress", Label: "Git 同步进度", Ratio: float32(progress.Percent) / 100, Height: Space0, Fill: ThemeAccent(c), Track: ThemeBorder(c)})
		}
		renderGitPaneOpBanner(c, s, ps)
		renderGitPaneFailure(c, ps)
		renderGitPaneSwitchStash(c, ps)
		ui.Column(c.Key("git-pane-content")).Grow(1).MinHeight(0).FillWidth().Children(func() {
			if loading && state.WorkDir == "" {
				LoadingIndicator(c, "git-pane-loading", "", IconMD)
				return
			}
			if state.WorkDir == "" {
				renderGitPaneEmpty(c, "不是 Git 仓库", IconGitBranch())
				return
			}
			renderGitPaneHistory(c, s, ps.threadID, ps)
		})
		renderGitPaneUndo(c, ps)
	})
	renderGitPaneForceConfirm(c, ps)
	if ps.stash != nil {
		renderGitStashDialog(c, ps.stash)
	}
	renderGitPRCreateDialog(c, ps.prCreate)
	if gitPaneRemotesDialog != nil && ps.remotesOpen {
		gitPaneRemotesDialog(c, s, ps.threadID, &ps.remotesOpen)
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
const gitPaneMenuWidth = 200

func renderGitPaneToolbar(c *ui.Context, s *chatState, ps *gitPaneState, state GitState, width float32) {
	ui.Row(c.Key("git-pane-toolbar")).FillWidth().Height(diffHeaderHeight).Shrink(0).Padding(0, Space3).Gap(Space2).AlignItems(ui.Center).Children(func() {
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
		ps.mu.Lock()
		syncing, cancel := ps.syncing, ps.cancel
		ps.mu.Unlock()
		label, action := gitPaneSyncAction(state)
		tip := label
		disabled := !syncing && (state.Detached || state.WorkDir == "")
		if state.WorkDir == "" {
			tip = "Git 状态不可用"
		}
		if state.Detached {
			tip = "分离 HEAD 时无法同步"
		}
		b := ui.ButtonBase(c.Key("git-pane-sync")).Label(label).Tooltip(tip).Height(ControlMD).Padding(0, Space2).Radius(RadiusSM).Gap(Space1).AlignItems(ui.Center).Disabled(disabled).Transition(motionFeedback(c))
		scale = motionPressScale(c, b, "git-pane-sync-press", !disabled && b.Pressed())
		b.Scale(scale, scale)
		if b.Hovered() {
			b.Background(ThemeHover(c))
		}
		stop := syncing && b.Hovered()
		if syncing {
			b.Label("停止").Tooltip("停止")
		}
		if b.Clicked() && !disabled {
			if syncing {
				if cancel != nil {
					cancel()
				}
			} else {
				ps.run(action)
			}
		}
		b.Children(func() {
			switch {
			case stop:
				ui.Icon(c, IconSquare()).Size(IconSM, IconSM)
			case syncing:
				renderGitSyncSpin(c)
			default:
				ui.Icon(c, IconRefreshCw()).Size(IconSM, IconSM)
			}
			if width >= KanbanBoardPaneMinWidth && (action != "fetch" || syncing) {
				if stop {
					label = "停止"
				}
				ui.Text(c, label).FontSize(uiFont(TypeFootnote))
			}
		})
		more := IconButton(c, IconButtonProps{Key: "git-pane-more", Icon: IconMoreHorizontal(), Label: "Git 更多操作", Variant: ButtonGhost, Size: ControlMD, OnClick: func() { ps.menuOpen = true; s.invalidate() }})
		appPopover(c.Key("git-pane-more-popover"), more, &ps.menuOpen, func() { s.cancelPushPRConfirm() }, func() {
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
			popoverCard(c, ui.Column(c.Key("git-pane-menu")).Width(gitPaneMenuWidth)).Padding(Space1).Gap(Space0).Children(func() {
				// 菜单项左对齐，按同步、贮藏、PR、仓库分组，组间用细分隔线；禁用时保留原因提示。
				option := func(key, label string, blocked bool, tip string, onClick func()) {
					el := menuRow(c, menuRowProps{
						Key:      "git-pane-menu-" + key,
						Label:    label,
						Title:    label,
						Disabled: blocked,
						OnClick: func() {
							ps.menuOpen = false
							s.cancelPushPRConfirm()
							onClick()
						},
					})
					if tip != "" {
						el.Tooltip(tip)
					}
				}
				ui.Column(c.Key("git-pane-menu-rows")).FillWidth().Gap(Space0).FocusGroup(ui.Vertical).Children(func() {
					syncTip := ""
					switch {
					case syncing:
						syncTip = "Git 操作进行中"
					case state.WorkDir == "":
						syncTip = "Git 状态不可用"
					case state.Detached:
						syncTip = "分离 HEAD 时无法执行此操作"
					}
					option("pull", "拉取", syncTip != "", syncTip, func() { ps.run("pull") })
					option("force", "强制推送…", syncTip != "", syncTip, func() {
						ps.mu.Lock()
						ps.forceBranch, ps.forceOpen = state.Branch, true
						ps.mu.Unlock()
						s.invalidate()
					})
					gitMenuDivider(c)
					stashTip := ""
					switch {
					case syncing:
						stashTip = "Git 操作进行中"
					case state.WorkDir == "":
						stashTip = "Git 状态不可用"
					}
					option("stash", "贮藏…", stashTip != "", stashTip, func() {
						if ps.stash == nil {
							ps.stash = &gitStashDialog{s: s, threadID: ps.threadID, confirmAt: -1}
						}
						ps.stash.openStashDialog()
					})
					gitMenuDivider(c)
					prTip := ""
					if repoURL == "" {
						prTip = "没有可用的远端仓库地址"
					}
					option("pr-create", "创建 PR…", prTip != "", prTip, func() {
						openGitPRCreateDialog(s, ps.threadID)
						s.invalidate()
					})
					if canMergeBees {
						mergeTip := ""
						if syncing || pushBusy || state.WorkDir == "" || state.Detached {
							mergeTip = "Git 操作进行中或分支不可用"
						}
						option("push-pr", "合并子代理改动（PR）", mergeTip != "", mergeTip, func() {
							ps.menuOpen = true
							s.requestPushPRConfirm()
							s.invalidate()
						})
					}
					option("pr-view", "查看 Pull Request…", false, "", func() {
						s.openGitPRDialog()
						s.invalidate()
					})
					showRepo, showRemotes := repoURL != "", gitPaneRemotesDialog != nil
					if showRepo || showRemotes {
						gitMenuDivider(c)
						if showRepo {
							option("repo", "打开仓库", false, "", func() {
								c.OpenURL(repoURL)
								s.invalidate()
							})
						}
						if showRemotes {
							option("remotes", "远端…", false, "", func() {
								ps.remotesOpen = true
								s.invalidate()
							})
						}
					}
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
