package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"mcode/internal/git"
)

type gitHistoryClient interface {
	GitLog(context.Context, string, git.LogOptions) ([]git.LogCommit, error)
	GitShowCommit(context.Context, string, string) (git.CommitDetail, error)
	GitCommitDiff(context.Context, string, string, string) ([]git.FileDiff, error)
	GitCreateBranchAt(context.Context, string, string, string, bool) error
	GitTagCreate(context.Context, string, string, string, string) error
	GitCherryPick(context.Context, string, string) error
	GitRevertCommit(context.Context, string, string) error
	GitReset(context.Context, string, string, string) error
}

// shortcut: 面板销毁时不清理，按会话数量级可忽略；若会话量很大再接到 dispose
var gitHistoryStates sync.Map // *gitPaneState → *gitHistoryState

var gitPaneBlameView func(c *ui.Context, s *chatState, threadID string, ps *gitPaneState, path string, onOpenCommit func(sha string))

const gitHistoryPageSize = 200

type gitHistoryState struct {
	mu                                                        sync.Mutex
	opts                                                      git.LogOptions
	head, root                                                string
	initialized, dirty, loading, showLoading, more, resetList bool
	seq                                                       uint64
	cancel                                                    context.CancelFunc
	commits                                                   []git.LogCommit
	graph                                                     []git.GraphRow
	mode, blamePath, returnMode                               string
	sha                                                       string
	detail                                                    git.CommitDetail
	detailDirty, detailLoading, detailIndicator               bool
	detailSeq                                                 uint64
	detailCancel                                              context.CancelFunc
	diffPath                                                  string
	diff                                                      []git.FileDiff
	diffLoading, diffIndicator, diffDirty                     bool
	diffSeq                                                   uint64
	diffCancel                                                context.CancelFunc
	searchOpen                                                bool
	search                                                    string
	searchSeq                                                 uint64
	debounce                                                  *time.Timer
	bodyExpanded                                              bool
	copiedUntil                                               time.Time
	clipboardMessage                                          *string
	dialog, dialogSHA, dialogName                             string
	writing                                                   bool
	// 以下状态只在 UI 线程访问；后台只交付不可变的数据快照。
	list      ui.ListState
	selected  int
	viewSeq   uint64
	diffViews []*editor.DiffState
}

func gitHistoryStateFor(ps *gitPaneState) *gitHistoryState {
	value, _ := gitHistoryStates.LoadOrStore(ps, &gitHistoryState{dirty: true, selected: -1})
	return value.(*gitHistoryState)
}

func gitHistoryInvalidate(ps *gitPaneState) {
	if ps.owner != nil {
		ps.owner.invalidate()
	}
}

func gitHistoryAlive(ps *gitPaneState) bool { ps.mu.Lock(); defer ps.mu.Unlock(); return !ps.disposed }

func gitHistoryContext(ps *gitPaneState) (context.Context, context.CancelFunc, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.disposed {
		return nil, nil, false
	}
	parent := ps.lifeCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	return ctx, cancel, true
}

// 改变过滤条件时立即废弃旧请求，防抖期间也不能覆盖新条件。
func (h *gitHistoryState) resetLocked() {
	if h.cancel != nil {
		h.cancel()
		h.cancel = nil
	}
	h.seq++
	h.loading, h.showLoading, h.dirty, h.resetList = false, false, true, true
	h.commits, h.graph = nil, nil
	h.more = true
}

func (h *gitHistoryState) closeDetailLocked() {
	if h.detailCancel != nil {
		h.detailCancel()
		h.detailCancel = nil
	}
	if h.diffCancel != nil {
		h.diffCancel()
		h.diffCancel = nil
	}
	h.detailSeq++
	h.diffSeq++
	h.detailDirty, h.detailLoading, h.detailIndicator = false, false, false
	h.diffPath, h.diff, h.diffLoading, h.diffIndicator, h.diffDirty = "", nil, false, false, false
	h.detail = git.CommitDetail{}
	h.copiedUntil = time.Time{}
}

func gitHistorySelectSegment(ps *gitPaneState) {
	ps.mu.Lock()
	if !ps.disposed {
		ps.segment = 1
	}
	ps.mu.Unlock()
	gitHistoryInvalidate(ps)
}

func openGitFileHistory(ps *gitPaneState, path string) {
	if ps == nil {
		return
	}
	h := gitHistoryStateFor(ps)
	h.mu.Lock()
	h.opts.Path = path
	h.closeDetailLocked()
	h.mode, h.returnMode = "", ""
	h.resetLocked()
	h.mu.Unlock()
	gitHistorySelectSegment(ps)
}

func openGitCommitDetail(ps *gitPaneState, sha string) {
	if ps == nil || sha == "" {
		return
	}
	h := gitHistoryStateFor(ps)
	h.mu.Lock()
	if h.mode == "blame" {
		h.returnMode = "blame"
	}
	h.closeDetailLocked()
	h.mode, h.sha = "commit", sha
	h.detailDirty, h.bodyExpanded = true, false
	h.mu.Unlock()
	gitHistorySelectSegment(ps)
}

func openGitBlame(ps *gitPaneState, path string) {
	if ps == nil {
		return
	}
	h := gitHistoryStateFor(ps)
	h.mu.Lock()
	h.closeDetailLocked()
	h.mode, h.blamePath, h.returnMode = "blame", path, ""
	h.mu.Unlock()
	gitHistorySelectSegment(ps)
}

func (h *gitHistoryState) load(ps *gitPaneState, client gitHistoryClient, reset bool) {
	ctx, cancel, alive := gitHistoryContext(ps)
	if !alive {
		return
	}
	h.mu.Lock()
	if reset {
		h.resetLocked()
	}
	if h.loading || (!h.dirty && !h.more) {
		h.mu.Unlock()
		cancel()
		return
	}
	opts := h.opts
	opts.Limit, opts.Skip = gitHistoryPageSize, len(h.commits)
	if h.dirty {
		opts.Skip = 0
	}
	h.seq++
	seq := h.seq
	h.cancel, h.loading, h.showLoading, h.dirty = cancel, true, false, false
	h.initialized = true
	h.mu.Unlock()
	go func() {
		timer := time.AfterFunc(150*time.Millisecond, func() {
			h.mu.Lock()
			if h.seq == seq && h.loading && ctx.Err() == nil {
				h.showLoading = true
			}
			h.mu.Unlock()
			gitHistoryInvalidate(ps)
		})
		commits, err := client.GitLog(ctx, ps.threadID, opts)
		if err == nil {
			err = ctx.Err()
		}
		cancel()
		timer.Stop()
		alive := gitHistoryAlive(ps)
		h.mu.Lock()
		valid := h.seq == seq && alive
		if h.seq == seq {
			h.loading, h.showLoading, h.cancel = false, false, nil
		}
		if valid {
			if err == nil {
				if opts.Skip == 0 {
					h.commits = commits
				} else {
					h.commits = append(h.commits, commits...)
				}
				h.graph = git.ComputeGraph(h.commits, 6)
				h.more = len(commits) == gitHistoryPageSize
			} else {
				h.more = false
				h.dirty = errors.Is(err, context.Canceled)
			}
		}
		h.mu.Unlock()
		if valid && err != nil && !errors.Is(err, context.Canceled) {
			ps.fail(err)
		}
		if valid {
			gitHistoryInvalidate(ps)
		}
	}()
}

func (h *gitHistoryState) loadDetail(ps *gitPaneState, client gitHistoryClient) {
	ctx, cancel, alive := gitHistoryContext(ps)
	if !alive {
		return
	}
	h.mu.Lock()
	if !h.detailDirty {
		h.mu.Unlock()
		cancel()
		return
	}
	h.detailDirty, h.detailLoading, h.detailIndicator = false, true, false
	h.detailSeq++
	seq, sha := h.detailSeq, h.sha
	h.detailCancel = cancel
	h.mu.Unlock()
	go func() {
		timer := time.AfterFunc(150*time.Millisecond, func() {
			h.mu.Lock()
			if h.detailSeq == seq && h.detailLoading && ctx.Err() == nil {
				h.detailIndicator = true
			}
			h.mu.Unlock()
			gitHistoryInvalidate(ps)
		})
		detail, err := client.GitShowCommit(ctx, ps.threadID, sha)
		if err == nil {
			err = ctx.Err()
		}
		cancel()
		timer.Stop()
		alive := gitHistoryAlive(ps)
		h.mu.Lock()
		valid := h.detailSeq == seq && alive
		if h.detailSeq == seq {
			h.detailLoading, h.detailIndicator, h.detailCancel = false, false, nil
		}
		if valid {
			if err == nil {
				h.detail = detail
			} else {
				h.detailDirty = errors.Is(err, context.Canceled)
			}
		}
		h.mu.Unlock()
		if valid && err != nil && !errors.Is(err, context.Canceled) {
			ps.fail(err)
		}
		if valid {
			gitHistoryInvalidate(ps)
		}
	}()
}

func (h *gitHistoryState) openDiff(ps *gitPaneState, client gitHistoryClient, path string) {
	ctx, cancel, alive := gitHistoryContext(ps)
	if !alive {
		return
	}
	h.mu.Lock()
	if h.diffCancel != nil {
		h.diffCancel()
	}
	h.diffSeq++
	seq, sha := h.diffSeq, h.sha
	h.diffPath, h.diff, h.diffLoading, h.diffIndicator, h.diffCancel, h.diffDirty = path, nil, true, false, cancel, false
	h.mu.Unlock()
	gitHistoryInvalidate(ps)
	go func() {
		timer := time.AfterFunc(150*time.Millisecond, func() {
			h.mu.Lock()
			if h.diffSeq == seq && h.diffLoading && ctx.Err() == nil {
				h.diffIndicator = true
			}
			h.mu.Unlock()
			gitHistoryInvalidate(ps)
		})
		files, err := client.GitCommitDiff(ctx, ps.threadID, sha, path)
		if err == nil {
			err = ctx.Err()
		}
		// 延迟补丁在后台展开；editor 状态只在 UI 线程创建。
		for i := range files {
			if err == nil && files[i].IsDeferred {
				files[i], err = git.ExpandDeferred(files[i])
			}
		}
		cancel()
		timer.Stop()
		alive := gitHistoryAlive(ps)
		h.mu.Lock()
		valid := h.diffSeq == seq && alive
		if h.diffSeq == seq {
			h.diffLoading, h.diffIndicator, h.diffCancel = false, false, nil
		}
		if valid {
			if err == nil {
				h.diff = files
			} else {
				h.diffDirty = errors.Is(err, context.Canceled)
			}
		}
		h.mu.Unlock()
		if valid && err != nil && !errors.Is(err, context.Canceled) {
			ps.fail(err)
		}
		if valid {
			gitHistoryInvalidate(ps)
		}
	}()
}

func (h *gitHistoryState) setSearch(ps *gitPaneState, value string, immediate bool) {
	h.mu.Lock()
	h.search, h.opts.Query = value, value
	h.searchSeq++
	seq := h.searchSeq
	if h.debounce != nil {
		h.debounce.Stop()
		h.debounce = nil
	}
	h.resetLocked()
	// 搜索防抖期间不发送请求，由定时器只设置 dirty 后交回 UI 线程。
	h.dirty = immediate
	if !immediate {
		h.debounce = time.AfterFunc(250*time.Millisecond, func() {
			h.mu.Lock()
			if h.searchSeq == seq {
				h.dirty, h.debounce = true, nil
			}
			h.mu.Unlock()
			gitHistoryInvalidate(ps)
		})
	}
	h.mu.Unlock()
	gitHistoryInvalidate(ps)
}

func gitHistoryRefs(refs []git.LogRef, branch string) ([]git.LogRef, int) {
	visible := make([]git.LogRef, 0, len(refs))
	for _, ref := range refs {
		if ref.Kind != "head" {
			visible = append(visible, ref)
		}
	}
	sort.SliceStable(visible, func(i, j int) bool { return visible[i].Name == branch && visible[j].Name != branch })
	extra := max(0, len(visible)-2)
	if len(visible) > 2 {
		visible = visible[:2]
	}
	return visible, extra
}

func gitHistoryTimeVisible(width float32) bool { return width >= 400 }

func renderGitPaneHistory(c *ui.Context, s *chatState, threadID string, ps *gitPaneState) {
	client, ok := s.client.(gitHistoryClient)
	if !ok {
		renderGitPaneEmpty(c, "暂无提交", IconGitBranch())
		return
	}
	h := gitHistoryStateFor(ps)
	h.mu.Lock()
	clipboardMessage := h.clipboardMessage
	h.clipboardMessage = nil
	h.mu.Unlock()
	if clipboardMessage != nil {
		c.WriteClipboard(*clipboardMessage)
	}
	ps.mu.Lock()
	head, root, branch := ps.state.Head, ps.state.WorkDir, ps.state.Branch
	ps.mu.Unlock()
	h.mu.Lock()
	if h.head != head || h.root != root {
		h.head, h.root = head, root
		h.resetLocked()
	}
	dirty, detailDirty, diffDirty, diffPath := h.dirty, h.detailDirty, h.diffDirty, h.diffPath
	h.mu.Unlock()
	if dirty {
		h.load(ps, client, false)
	}
	if detailDirty {
		h.loadDetail(ps, client)
	}
	if diffDirty && diffPath != "" {
		h.openDiff(ps, client, diffPath)
	}
	pane := ui.Column(c.Key("git-history")).Fill().MinWidth(0).MinHeight(0)
	width, height := pane.Bounds().W, pane.Bounds().H
	if width <= 0 {
		width = inspectorSession(s).Width
		if width <= 0 {
			width, _ = c.Size()
		}
	}
	h.mu.Lock()
	mode, sha, blamePath := h.mode, h.sha, h.blamePath
	dialog := h.dialog
	searchOpen := h.searchOpen
	h.mu.Unlock()
	if dialog == "" && c.Shortcut(ui.Cmd, ui.KeyF) {
		h.mu.Lock()
		h.searchOpen = true
		h.mode = ""
		h.mu.Unlock()
		searchOpen = true
		mode = ""
	}
	if dialog == "" && (!searchOpen || mode == "blame" || (mode == "commit" && width < 640)) && mode != "" && c.Shortcut(0, ui.KeyEscape) {
		h.back(ps)
		h.mu.Lock()
		mode = h.mode
		h.mu.Unlock()
	}
	pane.Children(func() {
		if mode == "blame" {
			gitHistoryBackButton(c, "git-history-blame-back", func() { h.back(ps) })
			ui.Text(c, filepath.Base(blamePath)).FontSize(uiFont(TypeFootnote)).SingleLine()
			if gitPaneBlameView != nil {
				gitPaneBlameView(c, s, threadID, ps, blamePath, func(sha string) { openGitCommitDetail(ps, sha) })
			} else {
				renderGitPaneEmpty(c, "暂无提交", IconGitBranch())
			}
			return
		}
		if mode == "commit" && width < 640 {
			ui.Column(c.Key("git-history-slide-" + sha)).Fill().MinHeight(0).Transition(motionSidePanel(c, false)).Children(func() {
				h.mu.Lock()
				diffActive := h.diffPath != ""
				h.mu.Unlock()
				if !diffActive {
					gitHistoryBackButton(c, "git-history-detail-back", func() { h.back(ps) })
				}
				renderGitHistoryDetail(c, s, ps, h, client)
			})
			return
		}
		renderGitHistoryFilters(c, ps, h)
		listPane := ui.Column(c.Key("git-history-list-pane")).FillWidth().MinHeight(0)
		if mode == "commit" {
			listPane.Height(max(RowHeight, height*.45)).Shrink(0)
		} else {
			listPane.Grow(1)
		}
		listPane.Children(func() { renderGitHistoryList(c, s, ps, h, client, width, branch) })
		if mode == "commit" {
			ui.Column(c.Key("git-history-detail-pane")).FillWidth().Grow(1).MinHeight(0).BorderWidth(Hairline, 0, 0, 0).BorderColor(ThemeBorder(c)).Children(func() { renderGitHistoryDetail(c, s, ps, h, client) })
		}
	})
	renderGitHistoryDialog(c, s, ps, h, client)
}

func gitHistoryBackButton(c *ui.Context, key string, back func()) {
	Button(c, ButtonOpts{Key: key, Text: "‹ 返回", Variant: ButtonGhost, Size: ControlMD, OnClick: back})
}

func (h *gitHistoryState) back(ps *gitPaneState) {
	h.mu.Lock()
	if h.diffPath != "" {
		h.diffPath = ""
		h.diffSeq++
		if h.diffCancel != nil {
			h.diffCancel()
		}
		h.diffLoading, h.diffIndicator, h.diffDirty = false, false, false
	} else {
		mode := h.returnMode
		h.closeDetailLocked()
		h.mode, h.returnMode = mode, ""
	}
	h.mu.Unlock()
	gitHistoryInvalidate(ps)
}

func renderGitHistoryFilters(c *ui.Context, ps *gitPaneState, h *gitHistoryState) {
	h.mu.Lock()
	searchOpen, search, all, path := h.searchOpen, h.search, h.opts.All, h.opts.Path
	h.mu.Unlock()
	closeSearch := func() { h.mu.Lock(); h.searchOpen = false; h.mu.Unlock(); h.setSearch(ps, "", true) }
	if searchOpen && c.Shortcut(0, ui.KeyEscape) {
		closeSearch()
		searchOpen = false
	}
	ui.Row(c.Key("git-history-filters")).FillWidth().Height(ControlLG).Shrink(0).Padding(0, Space2).Gap(Space2).AlignItems(ui.Center).Children(func() {
		if searchOpen {
			SearchField(c, TextFieldProps{Key: "git-history-search", Value: &search, Label: "搜索提交", Placeholder: "搜索提交", Grow: true, AutoFocus: true, Size: ControlMD, OnChange: func(value string) { h.setSearch(ps, value, false) }, OnKeyDown: func(k ui.TextKey) bool {
				if k.Key == ui.KeyEscape {
					closeSearch()
					return true
				}
				return false
			}})
		} else {
			b := ui.ButtonBase(c.Key("git-history-search-button")).Label("搜索提交").Tooltip("搜索提交 ⌘F").Size(ControlMD, ControlMD).Radius(RadiusSM).Transition(motionFeedback(c))
			b.Children(func() { ui.Icon(c, IconSearch()).Size(IconSM, IconSM) })
			b.OnClick(func() { h.mu.Lock(); h.searchOpen = true; h.mu.Unlock(); gitHistoryInvalidate(ps) })
			ui.Spacer(c)
		}
		label := "当前分支"
		if all {
			label = "所有分支"
		}
		b := ui.ButtonBase(c.Key("git-history-scope")).Label(label).Height(ControlMD).Padding(0, Space2).Radius(RadiusSM).Gap(Space1).AlignItems(ui.Center)
		b.Children(func() {
			ui.Text(c, label).FontSize(uiFont(TypeFootnote))
			ui.Icon(c, IconChevronDown()).Size(IconSM, IconSM)
		})
		b.Menu(func(m *ui.Menu) {
			for i, label := range []string{"当前分支", "所有分支"} {
				if m.Item(label).Chosen() {
					h.mu.Lock()
					h.opts.All = i == 1
					h.resetLocked()
					h.mu.Unlock()
					gitHistoryInvalidate(ps)
				}
			}
		})
		if path != "" {
			chip := ui.ButtonBase(c.Key("git-history-file-chip")).Label("清除文件过滤").Tooltip(path).Height(ControlSM).MaxWidth(TasksSearchWidth).MinWidth(0).Padding(0, Space2).Radius(RadiusPill).Background(ThemeHover(c)).Gap(Space1).AlignItems(ui.Center)
			chip.Children(func() {
				ui.Text(c, filepath.Base(path)).SingleLine().MinWidth(0).Shrink(1).FontSize(uiFont(TypeCaption))
				ui.Icon(c, IconX()).Size(IconSM, IconSM)
			})
			chip.OnClick(func() { h.mu.Lock(); h.opts.Path = ""; h.resetLocked(); h.mu.Unlock(); gitHistoryInvalidate(ps) })
		}
	})
}

func renderGitHistoryList(c *ui.Context, s *chatState, ps *gitPaneState, h *gitHistoryState, client gitHistoryClient, width float32, branch string) {
	h.mu.Lock()
	commits, graph, loading, indicator, query, reset, more := h.commits, h.graph, h.loading, h.showLoading, h.opts.Query, h.resetList, h.more
	h.resetList = false
	h.mu.Unlock()
	if reset {
		h.list = ui.ListState{}
		h.selected = -1
	}
	if len(commits) == 0 {
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
	h.list.Selected = &h.selected
	h.list.Key = func(i int) any { return commits[i].SHA }
	h.list.Label = func(i int) string { return commits[i].Subject }
	graphWidth := gitGraphWidth(graph)
	list := ui.List(c.Key("git-history-list"), &h.list, len(commits), func(i int) {
		commit := commits[i]
		row := ui.Row(c.Key("git-history-row-"+commit.SHA)).FillWidth().Height(RowHeight).AlignItems(ui.Center).Gap(Space2).Padding(0, Space2, 0, 0).Transition(motionFeedback(c))
		background := ThemeWindowBg(c)
		if i == h.selected {
			background = ThemeSelected(c)
		} else if row.Hovered() {
			background = ThemeHover(c)
		}
		row.Background(background)
		row.ContextMenu(gitHistoryMenu(c, s, ps, h, client, commit))
		row.Children(func() {
			renderGitGraphRow(c, graph[i], graphWidth, commit.Unpushed, background)
			ui.Text(c, commit.Subject).FontSize(uiFont(TypeFootnote)).TextColor(ThemePrimaryText(c)).SingleLine().Grow(1).MinWidth(0).Shrink(1)
			refs, extra := gitHistoryRefs(commit.Refs, branch)
			for n, ref := range refs {
				ui.Row(c.Key(fmt.Sprintf("git-history-ref-%s-%d", commit.SHA, n))).Height(ControlXS).MaxWidth(TasksSearchWidth).MinWidth(0).Padding(0, Space1).Gap(Space1).AlignItems(ui.Center).Radius(RadiusXS).Background(ThemeHover(c)).Children(func() {
					colors := gitLaneColors(c)
					color := colors[0]
					if ref.Kind == "tag" {
						color = colors[2]
					} else if ref.Kind == "remote" {
						color = colors[1]
					}
					ui.Box(c).Size(Space2, Space2).Radius(RadiusPill).Background(color).Shrink(0)
					ui.Text(c, ref.Name).FontSize(uiFont(TypeCaption)).SingleLine().Shrink(1).MinWidth(0)
				})
			}
			if extra > 0 {
				ui.Text(c, fmt.Sprintf("+%d", extra)).FontSize(uiFont(TypeCaption)).TextColor(ThemeTertiaryText(c))
			}
			if gitHistoryTimeVisible(width) {
				ui.Text(c, formatRelativeTime(commit.AuthorTime)).FontSize(uiFont(TypeCaption)).Font("monospace").TextColor(ThemeTertiaryText(c)).Shrink(0)
			}
		})
	}).FillWidth().Grow(1).MinHeight(0)
	if list.Submitted() && h.selected >= 0 && h.selected < len(commits) {
		openGitCommitDetail(ps, commits[h.selected].SHA)
	}
	_, last := h.list.Visible()
	if more && !loading && last >= len(commits)-20 {
		h.load(ps, client, false)
	}
	if indicator {
		LoadingIndicator(c, "git-history-more", "", IconSM)
	}
}

// SetPatch 保留每个 hunk 的文件行号，并让 DiffView 原生处理只读、滚动和行内差异。
func newGitHistoryDiffView(file git.FileDiff) *editor.DiffState {
	patch := editor.FilePatch{OldName: file.OldPath, NewName: file.NewPath, Binary: file.Binary}
	for _, hunk := range file.Hunks {
		h := editor.PatchHunk{OldStart: hunk.OldStart, OldLines: hunk.OldLines, NewStart: hunk.NewStart, NewLines: hunk.NewLines}
		if parts := strings.SplitN(hunk.Header, "@@", 3); len(parts) == 3 {
			h.Section = strings.TrimSpace(parts[2])
		}
		for _, line := range hunk.Lines {
			h.Lines = append(h.Lines, string(line.Kind)+line.Text)
		}
		patch.Hunks = append(patch.Hunks, h)
	}
	view := &editor.DiffState{Layout: editor.DiffUnified, Inline: editor.DiffInlineWordAlt, Indicators: editor.DiffClassic, Font: ui.Font{Size: uiFont(TypeCode)}}
	view.SetPatch(patch)
	language := editor.LanguageFor(diffRowFilePath(file))
	view.Old.SetLanguage(language)
	view.New.SetLanguage(language)
	return view
}

func renderGitHistoryDetail(c *ui.Context, s *chatState, ps *gitPaneState, h *gitHistoryState, client gitHistoryClient) {
	h.mu.Lock()
	detail, loading, indicator, expanded, copied := h.detail, h.detailLoading, h.detailIndicator, h.bodyExpanded, time.Now().Before(h.copiedUntil)
	diffPath, files, diffLoading, diffIndicator, diffSeq := h.diffPath, h.diff, h.diffLoading, h.diffIndicator, h.diffSeq
	h.mu.Unlock()
	if diffPath != "" {
		gitHistoryBackButton(c, "git-history-diff-back", func() { h.back(ps) })
		ui.Text(c, diffPath).FontSize(uiFont(TypeFootnote)).SingleLine()
		if diffLoading {
			if diffIndicator {
				LoadingIndicator(c, "git-history-diff-loading", "", IconSM)
			}
			return
		}
		if h.viewSeq != diffSeq {
			h.viewSeq, h.diffViews = diffSeq, nil
			for _, file := range files {
				h.diffViews = append(h.diffViews, newGitHistoryDiffView(file))
			}
		}
		ui.Column(c.Key("git-history-diff-body")).FillWidth().Grow(1).MinHeight(0).Children(func() {
			for i, file := range files {
				if file.Binary {
					renderGitPaneEmpty(c, "二进制文件", IconFile())
					continue
				}
				view := h.diffViews[i]
				view.Font.Size = uiFont(TypeCode)
				editor.DiffView(c.Key(fmt.Sprintf("git-history-diff-%s-%d", detail.SHA+":"+diffPath, i)), view).FillWidth().Grow(1).MinHeight(0)
			}
		})
		return
	}
	if loading {
		if indicator {
			LoadingIndicator(c, "git-history-detail-loading", "", IconSM)
		}
		return
	}
	if detail.SHA == "" {
		return
	}
	ui.Scroll(c.Key("git-history-detail-scroll-" + detail.SHA)).FillWidth().Grow(1).MinHeight(0).Children(func() {
		ui.Column(c.Key("git-history-detail-body-" + detail.SHA)).FillWidth().Padding(Space3).Gap(Space2).Children(func() {
			ui.Text(c, detail.Subject).FontSize(uiFont(TypeTitle)).TextColor(ThemePrimaryText(c)).Selectable()
			body := strings.TrimSpace(strings.TrimPrefix(detail.Body, detail.Subject))
			if body != "" {
				text := ui.Text(c, body).FontSize(uiFont(TypeFootnote)).TextColor(ThemePrimaryText(c)).Selectable()
				if !expanded {
					text.MaxLines(6)
				}
				// 用完整正文的排版高度判断是否超出六行，不能用已截断的元素高度猜测。
				width := text.Bounds().W
				if width <= 0 {
					c.Invalidate()
				}
				_, fullHeight := c.MeasureText(width, ui.Span{Text: body, Size: uiFont(TypeFootnote)})
				_, lineHeight := c.MeasureText(0, ui.Span{Text: "字", Size: uiFont(TypeFootnote)})
				if expanded || strings.Count(body, "\n") >= 6 || (width > 0 && fullHeight > lineHeight*6+Hairline) {
					label := "展开"
					if expanded {
						label = "收起"
					}
					Button(c, ButtonOpts{Key: "git-history-body-expand", Text: label, Variant: ButtonGhost, OnClick: func() { h.mu.Lock(); h.bodyExpanded = !h.bodyExpanded; h.mu.Unlock(); gitHistoryInvalidate(ps) }})
				}
			}
			ui.Row(c.Key("git-history-meta")).FillWidth().Wrap().Gap(Space2).AlignItems(ui.Center).Children(func() {
				initial, _ := utf8.DecodeRuneInString(detail.Author)
				if initial == utf8.RuneError || initial == 0 {
					initial = '?'
				}
				ui.Box(c).Size(ControlSM, ControlSM).Radius(RadiusPill).Background(ThemeHover(c)).AlignItems(ui.Center).Justify(ui.Center).Children(func() { ui.Text(c, strings.ToUpper(string(initial))).FontSize(uiFont(TypeCaption)) })
				ui.Text(c, detail.Author+" · "+detail.AuthorTime.Local().Format("2006-01-02 15:04")+" ·").FontSize(uiFont(TypeCaption)).TextColor(ThemeSecondaryText(c))
				CopyButton(c, CopyButtonProps{Key: "git-history-copy-sha", Label: "复制 SHA", Text: gitHistoryShortSHA(detail.SHA), Mode: CopyInline, Copied: copied, Value: func() string { return detail.SHA }, OnCopy: func() {
					h.mu.Lock()
					h.copiedUntil = time.Now().Add(3 * DurBase)
					h.mu.Unlock()
					time.AfterFunc(3*DurBase, func() { gitHistoryInvalidate(ps) })
					gitHistoryInvalidate(ps)
				}})
				for _, parent := range detail.Parents {
					Button(c, ButtonOpts{Key: "git-history-parent-" + parent, Text: gitHistoryShortSHA(parent), Variant: ButtonGhost, Size: ControlSM, OnClick: func() { openGitCommitDetail(ps, parent) }})
				}
			})
			for _, file := range detail.Files {
				row := ui.ButtonBase(c.Key("git-history-file-" + file.Path)).Label(file.Path).FillWidth().Height(RowHeight).Gap(Space2).AlignItems(ui.Center).Radius(RadiusSM).Transition(motionFeedback(c))
				if row.Hovered() {
					row.Background(ThemeHover(c))
				}
				row.Children(func() {
					status := byte(' ')
					if file.Status != "" {
						status = file.Status[0]
					}
					letter, color := gitStatusPresentation(c, status)
					ui.Text(c, letter).Width(IconMD).FontSize(uiFont(TypeCaption)).TextColor(color)
					ui.Text(c, filepath.Base(file.Path)).FontSize(uiFont(TypeFootnote)).SingleLine().Shrink(1).MinWidth(0)
					dir := filepath.Dir(file.Path)
					if dir != "." {
						ui.Text(c, dir).FontSize(uiFont(TypeCaption)).TextColor(ThemeTertiaryText(c)).SingleLine().Grow(1).Shrink(1).MinWidth(0)
					} else {
						ui.Spacer(c)
					}
					if !file.Binary {
						ui.Text(c, fmt.Sprintf("+%d", file.Added)).FontSize(uiFont(TypeCaption)).TextColor(ThemeSuccess(c))
						ui.Text(c, fmt.Sprintf("−%d", file.Deleted)).FontSize(uiFont(TypeCaption)).TextColor(ThemeDanger(c))
					}
				})
				row.OnClick(func() { h.openDiff(ps, client, file.Path) })
			}
		})
	})
}

func gitHistoryShortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func gitHistoryMenu(c *ui.Context, s *chatState, ps *gitPaneState, h *gitHistoryState, client gitHistoryClient, commit git.LogCommit) func(*ui.Menu) {
	return func(m *ui.Menu) {
		if m.Item("复制 SHA").Chosen() {
			c.WriteClipboard(commit.SHA)
		}
		if m.Item("复制信息").Chosen() {
			go func() {
				ctx, cancel, ok := gitHistoryContext(ps)
				if !ok {
					return
				}
				defer cancel()
				detail, err := client.GitShowCommit(ctx, ps.threadID, commit.SHA)
				if err != nil {
					ps.fail(err)
					return
				} // 后台只交付正文，剪贴板写入交回 UI 线程。
				h.mu.Lock()
				h.clipboardMessage = &detail.Body
				h.mu.Unlock()
				gitHistoryInvalidate(ps)
			}()
		}
		blocked, tip := gitHistoryWriteBlocked(s, ps, h, true)
		item := m.Item("检出（分离 HEAD）").Disabled(blocked).Tooltip(tip)
		if item.Chosen() {
			h.write(ps, client, true, func(ctx context.Context) error { return s.client.GitCheckout(ctx, ps.threadID, commit.SHA) })
		}
		for _, kind := range []string{"branch", "tag"} {
			label := "基于此新建分支…"
			if kind == "tag" {
				label = "打标签…"
			}
			if m.Item(label).Disabled(gitHistoryBusy(ps, h)).Chosen() {
				h.mu.Lock()
				h.dialog, h.dialogSHA, h.dialogName = kind, commit.SHA, ""
				h.mu.Unlock()
				gitHistoryInvalidate(ps)
			}
		}
		if m.Item("cherry-pick").Disabled(blocked).Tooltip(tip).Chosen() {
			h.write(ps, client, true, func(ctx context.Context) error { return client.GitCherryPick(ctx, ps.threadID, commit.SHA) })
		}
		if m.Item("还原（revert）").Disabled(blocked).Tooltip(tip).Chosen() {
			h.write(ps, client, true, func(ctx context.Context) error { return client.GitRevertCommit(ctx, ps.threadID, commit.SHA) })
		}
		if m.Item("让 AI 解释此提交").Chosen() {
			s.applyQuickReply("解释提交 " + commit.SHA + "：" + commit.Subject)
		}
		m.Submenu("重置到此", func(sub *ui.Menu) {
			for _, option := range []struct{ label, mode string }{{"保留改动（soft）", "soft"}, {"保留工作区（mixed）", "mixed"}, {"丢弃全部…（hard）", "hard"}} {
				if sub.Item(option.label).Disabled(blocked).Tooltip(tip).Chosen() {
					h.resetTo(ps, client, commit.SHA, option.mode)
				}
			}
		}).Disabled(blocked).Tooltip(tip)
	}
}

func gitHistoryBusy(ps *gitPaneState, h *gitHistoryState) bool {
	ps.mu.Lock()
	busy := ps.syncing || ps.disposed
	ps.mu.Unlock()
	return busy || h.isWriting()
}

func (h *gitHistoryState) isWriting() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.writing }

func gitHistoryWriteBlocked(s *chatState, ps *gitPaneState, h *gitHistoryState, worktree bool) (bool, string) {
	if worktree && gitPaneAgentBusy(s, ps.threadID) {
		return true, "Agent 正在修改此工作区"
	}
	ps.mu.Lock()
	syncing, disposed := ps.syncing, ps.disposed
	ps.mu.Unlock()
	if syncing || h.isWriting() {
		return true, "Git 操作进行中"
	}
	return disposed, ""
}

func (h *gitHistoryState) write(ps *gitPaneState, client gitHistoryClient, worktree bool, op func(context.Context) error) {
	if blocked, _ := gitHistoryWriteBlocked(ps.owner, ps, h, worktree); blocked {
		return
	}
	ctx, cancel, alive := gitHistoryContext(ps)
	if !alive {
		return
	}
	// 与面板工具栏共用写操作槽，避免检出与同步同时修改仓库。
	ps.mu.Lock()
	if ps.syncing || ps.disposed {
		ps.mu.Unlock()
		cancel()
		return
	}
	ps.syncing, ps.cancel, ps.failure, ps.progress = true, cancel, nil, git.Progress{Percent: -1}
	ps.mu.Unlock()
	h.mu.Lock()
	h.writing = true
	h.mu.Unlock()
	gitHistoryInvalidate(ps)
	go func() {
		err := op(ctx)
		cancel()
		ps.mu.Lock()
		ps.syncing, ps.cancel = false, nil
		if err == nil {
			ps.failure = nil
		}
		disposed := ps.disposed
		ps.mu.Unlock()
		h.mu.Lock()
		h.writing = false
		if err == nil {
			h.resetLocked()
		}
		h.mu.Unlock()
		if disposed {
			return
		}
		if err != nil {
			ps.fail(err)
		} else {
			ps.requestRefresh()
		}
		gitHistoryInvalidate(ps)
	}()
}

func (h *gitHistoryState) resetTo(ps *gitPaneState, client gitHistoryClient, sha, mode string) {
	if blocked, _ := gitHistoryWriteBlocked(ps.owner, ps, h, true); blocked {
		return
	}
	if mode == "hard" {
		h.mu.Lock()
		h.dialog, h.dialogSHA = "hard", sha
		h.mu.Unlock()
		gitHistoryInvalidate(ps)
		return
	}
	h.write(ps, client, true, func(ctx context.Context) error { return client.GitReset(ctx, ps.threadID, sha, mode) })
}

func renderGitHistoryDialog(c *ui.Context, s *chatState, ps *gitPaneState, h *gitHistoryState, client gitHistoryClient) {
	h.mu.Lock()
	kind, sha, name := h.dialog, h.dialogSHA, h.dialogName
	h.mu.Unlock()
	if kind == "" {
		return
	}
	open := true
	close := func() { h.mu.Lock(); h.dialog = ""; h.mu.Unlock(); gitHistoryInvalidate(ps) }
	modalDragModal(c, "git-history-dialog", &open, func() {
		title, confirm := "基于此新建分支", "创建"
		if kind == "tag" {
			title = "打标签"
		} else if kind == "hard" {
			title = "重置到 " + gitHistoryShortSHA(sha) + " 并丢弃全部改动？"
			confirm = "丢弃并重置"
		}
		ui.Column(c.Key("git-history-dialog-card")).Width(KanbanDetailMinWidth).MaxWidth(KanbanDetailMaxWidth).Padding(Space5).Gap(Space4).Radius(RadiusLG).Background(ThemeSurface(c)).BorderWidth(Hairline).BorderColor(ThemeBorder(c)).Transition(motionDialog(c)).Children(func() {
			ui.Text(c, title).FontSize(uiFont(TypeTitle)).TextColor(ThemePrimaryText(c))
			if kind != "hard" {
				TextField(c, TextFieldProps{Key: "git-history-name", Label: "名称", Value: &name, AutoFocus: true, FillWidth: true, OnChange: func(value string) { h.mu.Lock(); h.dialogName = value; h.mu.Unlock() }})
			}
			ui.Row(c.Key("git-history-dialog-actions")).FillWidth().Justify(ui.End).Gap(Space2).Children(func() {
				cancel := Button(c, ButtonOpts{Key: "git-history-dialog-cancel", Text: "取消", Variant: ButtonSecondary, OnClick: close})
				if kind == "hard" {
					cancel.AutoFocus()
				}
				disabled, tip := gitHistoryWriteBlocked(s, ps, h, kind == "hard")
				if kind != "hard" && strings.TrimSpace(name) == "" {
					disabled = true
					tip = "请输入名称"
				}
				variant := ButtonPrimary
				if kind == "hard" {
					variant = ButtonDanger
				}
				Button(c, ButtonOpts{Key: "git-history-dialog-confirm", Text: confirm, Variant: variant, Disabled: disabled, OnClick: func() {
					close()
					h.write(ps, client, kind == "hard", func(ctx context.Context) error {
						switch kind {
						case "hard":
							return client.GitReset(ctx, ps.threadID, sha, "hard")
						case "branch":
							return client.GitCreateBranchAt(ctx, ps.threadID, strings.TrimSpace(name), sha, false)
						default:
							return client.GitTagCreate(ctx, ps.threadID, strings.TrimSpace(name), sha, "")
						}
					})
				}}).Tooltip(tip)
			})
		})
	})
	if !open {
		close()
	}
}
