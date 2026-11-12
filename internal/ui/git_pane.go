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

type gitPaneState struct {
	mu                           sync.Mutex
	owner                        *chatState
	threadID                     string
	state                        GitState
	entries                      []git.StatusEntry
	op                           git.OpState
	segment                      int
	syncing                      bool
	cancel                       context.CancelFunc
	progress                     git.Progress
	failure                      *git.Failure
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
		if err == nil {
			entries, err = client.GitStatusEntries(ctx, ps.threadID)
		}
		if err == nil {
			op, err = client.GitOpState(ctx, ps.threadID)
		}
		cancel()
		timer.Stop()
		ps.mu.Lock()
		valid := !ps.disposed && ps.refreshSeq == requestSeq
		if valid {
			if err == nil {
				ps.state, ps.entries, ps.op = state, entries, op
			}
			if errors.Is(err, git.ErrNotRepo) {
				ps.state, ps.entries, ps.op = GitState{}, nil, git.OpState{}
			}
			if err != nil && !errors.Is(err, git.ErrNotRepo) && !errors.Is(err, context.Canceled) {
				f := git.ClassifyError(err)
				ps.failure = &f
			}
		}
		ps.loading = false
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
	ps.requestRefresh()
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
				ps.requestRefresh()
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
			toast = "已推送"
			if state.Branch != "" {
				toast += "到 " + state.Branch
			}
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
			case "skip":
				err = client.GitSkipOp(ctx, ps.threadID)
			case "abort":
				err = client.GitAbortOp(ctx, ps.threadID)
			}
		case "undo":
			if undoSnap != nil {
				err = client.GitRestoreSnapshot(ctx, ps.threadID, undoSnap)
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
	if _, ok := s.client.(gitPaneClient); !ok || ps.threadID == "" {
		renderGitPaneEmpty(c, "Git", IconGitBranch())
		return
	}
	ps.setVisible(true)
	ps.mu.Lock()
	state, syncing, progress, loading, segment := ps.state, ps.syncing, ps.progress, ps.loading, ps.segment
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
		ui.Column(c.Key("git-pane-content")).Grow(1).MinHeight(0).FillWidth().Children(func() {
			if loading && state.WorkDir == "" {
				LoadingIndicator(c, "git-pane-loading", "", IconMD)
				return
			}
			if state.WorkDir == "" {
				renderGitPaneEmpty(c, "Git", IconGitBranch())
				return
			}
			if segment == 0 {
				renderGitPaneChanges(c, s, ps.threadID, ps)
			} else {
				renderGitPaneHistory(c, s, ps.threadID, ps)
			}
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
		if btn.Hovered() {
			btn.Background(ThemeHover(c))
		}
		if btn.Clicked() && gitPaneBranchPopover != nil {
			ps.branchOpen = true
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
		segment := ps.segment
		syncing, cancel := ps.syncing, ps.cancel
		ps.mu.Unlock()
		ui.Row(c.Key("git-pane-segments")).Gap(Space0).AlignItems(ui.Center).Children(func() {
			for i, label := range []string{"变更", "历史"} {
				b := ui.ButtonBase(c.Key(fmt.Sprintf("git-pane-segment-%d", i))).Label(label).Height(ControlMD).Padding(0, Space2).Radius(RadiusSM).Gap(Space1).AlignItems(ui.Center).Transition(motionFeedback(c))
				if i == segment {
					b.Background(ThemeSelected(c))
				}
				if b.Clicked() {
					ps.mu.Lock()
					ps.segment = i
					ps.mu.Unlock()
					s.invalidate()
				}
				b.Children(func() {
					ui.Text(c, label).FontSize(uiFont(TypeFootnote))
					if i == 0 {
						rollingNumber(c, fmt.Sprintf("git-pane-count-%p", ps), state.Changes)
					}
				})
			}
		})
		ui.Spacer(c)
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
				icon := ui.Icon(c, IconRefreshCw()).Size(IconSM, IconSM)
				icon.Rotate(motionLoop(c, icon, "git-sync-spin", 4*DurBase, ui.Linear) * 360)
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
		more := IconButton(c, IconButtonProps{Key: "git-pane-more", Icon: IconMoreHorizontal(), Label: "Git 更多操作", Variant: ButtonGhost, Size: ControlMD, OnClick: func() { ps.menuOpen = true }})
		appPopover(c.Key("git-pane-more-popover"), more, &ps.menuOpen, func() { s.cancelPushPRConfirm() }, func() {
			popoverCard(c, ui.Column(c.Key("git-pane-menu"))).Padding(Space1).Gap(Space0).Children(func() {
				for _, item := range []struct{ label, action string }{{"抓取", "fetch"}, {"拉取（变基）", "rebase"}, {"拉取（合并）", "merge"}, {"强制推送…", "force"}, {"贮藏…", "stash"}} {
					item := item
					tip := ""
					blocked := syncing || state.WorkDir == "" || (state.Detached && item.action != "fetch" && item.action != "stash")
					if state.Detached && item.action != "fetch" && item.action != "stash" {
						tip = "分离 HEAD 时无法执行此操作"
					}
					if state.WorkDir == "" {
						tip = "Git 状态不可用"
					}
					if syncing {
						tip = "Git 操作进行中"
					}
					Button(c, ButtonOpts{Key: "git-pane-menu-" + item.action, Text: item.label, Variant: ButtonGhost, Disabled: blocked, Tooltip: tip, OnClick: func() {
						ps.menuOpen = false
						s.cancelPushPRConfirm()
						switch item.action {
						case "force":
							ps.forceBranch, ps.forceOpen = state.Branch, true
						case "stash":
							if ps.stash == nil {
								ps.stash = &gitStashDialog{s: s, threadID: ps.threadID, confirmAt: -1}
							}
							ps.stash.openStashDialog()
						default:
							ps.run(item.action)
						}
					}})
				}
				repoURL := normalizeGitRemoteURL(state.RemoteURL)
				hs := getChatHeaderState(s)
				chatHeaderMu.Lock()
				pushBusy, pushConfirm := hs.pushBusy, hs.pushConfirm
				chatHeaderMu.Unlock()
				for _, item := range []struct{ label, action string }{{"创建 PR…", "pr-create"}, {"推送并创建 PR", "push-pr"}, {"查看 Pull Request…", "pr-view"}, {"打开仓库", "repo"}, {"远端…", "remotes"}} {
					if item.action == "repo" && repoURL == "" || item.action == "remotes" && gitPaneRemotesDialog == nil {
						continue
					}
					blocked, tip := false, ""
					if item.action == "pr-create" && repoURL == "" {
						blocked, tip = true, "没有可用的远端仓库地址"
					}
					if item.action == "push-pr" && (syncing || pushBusy || state.WorkDir == "" || state.Detached) {
						blocked, tip = true, "Git 操作进行中或分支不可用"
					}
					Button(c, ButtonOpts{Key: "git-pane-menu-" + item.action, Text: item.label, Variant: ButtonGhost, Disabled: blocked, Tooltip: tip, OnClick: func() {
						ps.menuOpen = false
						s.cancelPushPRConfirm()
						switch item.action {
						case "pr-create":
							openGitPRCreateDialog(s, ps.threadID)
						case "push-pr":
							ps.menuOpen = true
							s.requestPushPRConfirm()
						case "pr-view":
							s.openGitPRDialog()
						case "repo":
							c.OpenURL(repoURL)
						case "remotes":
							ps.remotesOpen = true
						}
						s.invalidate()
					}})
				}
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
	if len(op.Conflicts) > 0 {
		title += fmt.Sprintf(" · %d 个冲突", len(op.Conflicts))
	}
	if op.Total > 0 {
		title += fmt.Sprintf(" · %d/%d", op.Step, op.Total)
	}
	return title
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
			Button(c, ButtonOpts{Text: "详情", Variant: ButtonGhost, Size: ControlSM, OnClick: func() { ps.detailOpen = !ps.detailOpen }})
			IconButton(c, IconButtonProps{Icon: IconX(), Label: "关闭 Git 错误", Size: ControlSM, Variant: ButtonGhost, OnClick: func() { ps.fail(nil) }})
		})
		switch failure.Kind {
		case git.FailureRejected:
			Button(c, ButtonOpts{Text: "拉取并重试", Variant: ButtonSecondary, Disabled: syncing, Tooltip: tip, OnClick: func() { ps.run("retry") }})
		case git.FailureLease:
			Button(c, ButtonOpts{Text: "强制推送…", Variant: ButtonSecondary, Disabled: syncing, Tooltip: tip, OnClick: func() { ps.mu.Lock(); ps.forceBranch = ps.state.Branch; ps.mu.Unlock(); ps.forceOpen = true }})
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

func renderGitPaneForceConfirm(c *ui.Context, ps *gitPaneState) {
	if !ps.forceOpen {
		return
	}
	ui.DialogBase(c.Key("git-pane-force-dialog"), &ps.forceOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.Color{}.Alpha(ScrimOpacity))
		winW, winH := c.Size()
		panel.MaxWidth(min(inspectorDefaultWidth, winW-Space8)).MaxHeight(winH - Space8).Children(func() {
			ui.Column(c.Key("git-pane-force-card")).MaxWidth(inspectorDefaultWidth).Padding(Space6).Gap(Space4).Radius(RadiusLG).Background(ThemeSurfaceCard(c)).Transition(motionDialog(c)).Children(func() {
				ui.Text(c, "强制推送 "+ps.forceBranch+"？").FontSize(uiFont(TypeTitle))
				ui.Row(c).Gap(Space3).Justify(ui.End).Children(func() {
					Button(c, ButtonOpts{Key: "git-pane-force-cancel", Text: "取消", Variant: ButtonSecondary, OnClick: func() { ps.forceOpen = false }}).AutoFocus()
					ps.mu.Lock()
					blocked := ps.syncing || ps.state.Detached || ps.state.Branch != ps.forceBranch
					tip := ""
					if blocked {
						tip = "Git 操作进行中或分支已变化"
					}
					ps.mu.Unlock()
					Button(c, ButtonOpts{Key: "git-pane-force-confirm", Text: "强制推送", Variant: ButtonDanger, Disabled: blocked, Tooltip: tip, OnClick: func() { ps.forceOpen = false; ps.run("force") }})
				})
			})
		})
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
		ui.Icon(c, icon).Size(Space8, Space8).TextColor(ThemeTertiaryText(c))
		ui.Text(c, title).FontSize(uiFont(TypeBody)).TextColor(ThemeSecondaryText(c))
	})
}
