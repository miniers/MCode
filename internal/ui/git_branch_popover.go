package mcodeui

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

type gitBranchClient interface {
	GitBranches(context.Context, string) ([]git.BranchInfo, error)
	GitRecentBranches(context.Context, string, int) ([]string, error)
	GitBranchRename(context.Context, string, string, string) error
	GitBranchDelete(context.Context, string, string, bool) error
	GitCreateBranchAt(context.Context, string, string, string, bool) error
	GitCheckoutRemoteBranch(context.Context, string, string) (string, error)
	GitTags(context.Context, string) ([]git.TagInfo, error)
	GitTagDelete(context.Context, string, string) error
	GitPushTag(context.Context, string, string, string) error
	GitRebase(context.Context, string, string) error
}

const (
	branchTabLocal  = "local"
	branchTabRemote = "remote"
	branchTabTag    = "tag"
	branchTabWT     = "wt"

	branchPopoverListMaxHeight  float32 = 248
	branchPopoverDirtyBgAlpha   float32 = 0.14
	branchPopoverDirtyLineAlpha float32 = 0.30
)

type gitBranchPopupState struct {
	mu                                   sync.Mutex
	query, edit, editName, confirm, menu string
	createOpen                           bool
	createBase, createName               string
	dirtyTarget, dirtyKind               string
	err                                  git.Failure
	retryBranch, busyName                string
	branches                             []git.BranchInfo
	recent                               []string
	tags                                 []git.TagInfo
	loaded, loading, busy                bool
	loadStarted, busyStarted             time.Time
	tab                                  string
	selected                             int
	seq                                  uint64
}

var gitBranchPopups sync.Map

func branchPopoverFor(s *chatState, id string) *gitBranchPopupState {
	v, _ := gitBranchPopups.LoadOrStore(struct {
		s  *chatState
		id string
	}{s, id}, &gitBranchPopupState{tab: branchTabLocal})
	return v.(*gitBranchPopupState)
}

func loadBranchPopover(s *chatState, id string, p *gitBranchPopupState) {
	client, ok := s.client.(gitBranchClient)
	if !ok {
		return
	}
	p.mu.Lock()
	p.seq++
	seq := p.seq
	p.loading = true
	p.loadStarted = time.Now()
	p.err = git.Failure{}
	p.mu.Unlock()
	time.AfterFunc(150*time.Millisecond, s.invalidate)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		branches, err := client.GitBranches(ctx, id)
		var recent []string
		var tags []git.TagInfo
		if err == nil {
			recent, err = client.GitRecentBranches(ctx, id, 5)
		}
		if err == nil {
			tags, err = client.GitTags(ctx, id)
		}
		p.mu.Lock()
		if seq == p.seq {
			p.retryBranch = ""
			p.loading = false
			if err != nil {
				p.err = git.ClassifyError(err)
			} else {
				p.loaded = true
				p.branches = branches
				p.recent = recent
				p.tags = tags
				p.err = git.Failure{}
			}
		}
		p.mu.Unlock()
		s.invalidate()
	}()
}

func openBranchPopover(s *chatState, id string) {
	p := branchPopoverFor(s, id)
	p.mu.Lock()
	p.query = ""
	p.selected = 0
	p.edit = ""
	p.editName = ""
	p.menu = ""
	p.confirm = ""
	p.createOpen = false
	p.createBase = ""
	p.createName = ""
	p.dirtyTarget = ""
	p.dirtyKind = ""
	p.err = git.Failure{}
	p.retryBranch = ""
	p.tab = branchTabLocal
	p.mu.Unlock()
	loadBranchPopover(s, id, p)
}

func branchDeleteNeedsForce(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "not fully merged")
}

func branchMatches(name, query string) bool {
	return strings.Contains(strings.ToLower(name), strings.ToLower(strings.TrimSpace(query)))
}

func branchOtherWorktree(b git.BranchInfo, dir string) bool {
	return b.WorktreePath != "" && filepath.Clean(b.WorktreePath) != filepath.Clean(dir)
}

type branchPopoverItem struct {
	name, kind string
	branch     git.BranchInfo
}

func branchPopoverDivider(c *ui.Context) {
	ui.Box(c).Height(Hairline).FillWidth().Background(ThemeHairline(c))
}

func branchPopoverGitPane(s *chatState, threadID string) *gitPaneState {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	if threadID == "" {
		threadID = s.selectedThreadID
	}
	slot := inspectorSessionLocked(s, threadID)
	if slot.Git == nil {
		slot.Git = &gitPaneState{owner: s, threadID: threadID, progress: git.Progress{Percent: -1}}
	}
	return slot.Git
}

func branchPopoverMonoFont(s *chatState) string {
	if st := getChatSettings(s); st != nil {
		return codeFontFamily(st.cfg.Appearance)
	}
	return "monospace"
}

func renderGitBranchPopoverContent(c *ui.Context, s *chatState, threadID string, width float32, open *bool) {
	renderGitBranchPopoverBody(c, s, threadID, width, open, false)
}

func renderGitBranchPopoverBody(c *ui.Context, s *chatState, threadID string, width float32, open *bool, withCard bool) {
	p := branchPopoverFor(s, threadID)
	client, advanced := s.client.(gitBranchClient)
	state := s.headerGitState(threadID)

	p.mu.Lock()
	query, failure, retryBranch, edit, editName, confirm, menu := p.query, p.err, p.retryBranch, p.edit, p.editName, p.confirm, p.menu
	createOpen, createBase, createName := p.createOpen, p.createBase, p.createName
	dirtyTarget, dirtyKind := p.dirtyTarget, p.dirtyKind
	branches := append([]git.BranchInfo(nil), p.branches...)
	recent := append([]string(nil), p.recent...)
	tags := append([]git.TagInfo(nil), p.tags...)
	loaded, busy, tab, selected := p.loaded, p.busy, p.tab, p.selected
	loading := p.loading && time.Since(p.loadStarted) >= 150*time.Millisecond
	busyName := p.busyName
	showBusy := p.busy && time.Since(p.busyStarted) >= 150*time.Millisecond
	p.mu.Unlock()

	if tab == "" {
		tab = branchTabLocal
	}
	if !advanced || !loaded {
		branches = nil
		for _, name := range state.Branches {
			branches = append(branches, git.BranchInfo{Name: name, Head: name == state.Branch})
		}
	}

	runWithBranch := func(closeOnSuccess bool, branch, kind, toast string, op func(context.Context) error) {
		p.mu.Lock()
		if p.busy {
			p.mu.Unlock()
			return
		}
		p.busy = true
		p.busyName = branch
		p.busyStarted = time.Now()
		p.err = git.Failure{}
		p.retryBranch = ""
		p.mu.Unlock()
		s.invalidate()
		time.AfterFunc(150*time.Millisecond, s.invalidate)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err := op(ctx)
			p.mu.Lock()
			p.busy = false
			p.busyName = ""
			if err != nil {
				p.err = git.ClassifyError(err)
				if p.err.Kind == git.FailureWorktree {
					p.retryBranch = branch
					p.dirtyTarget = branch
					p.dirtyKind = kind
				}
			} else {
				p.dirtyTarget = ""
				p.dirtyKind = ""
				if closeOnSuccess {
					p.confirm = "close"
				}
			}
			p.mu.Unlock()
			if err == nil {
				if toast != "" {
					s.setToast(toast)
				}
				s.loadGitState(threadID)
				if !closeOnSuccess {
					loadBranchPopover(s, threadID, p)
				}
			}
			s.invalidate()
		}()
	}

	if confirm == "close" {
		*open = false
		p.mu.Lock()
		p.confirm = ""
		p.mu.Unlock()
		return
	}

	applyChoose := func(item branchPopoverItem) {
		switch item.kind {
		case "create":
			runWithBranch(true, item.name, "local", "已创建分支 "+item.name, func(ctx context.Context) error {
				return s.client.GitCreateBranch(ctx, threadID, item.name)
			})
		case "remote":
			runWithBranch(true, item.name, item.kind, "已切换到 "+item.name, func(ctx context.Context) error {
				_, err := client.GitCheckoutRemoteBranch(ctx, threadID, item.name)
				return err
			})
		case "tag":
			runWithBranch(true, item.name, item.kind, "已切换到 "+item.name, func(ctx context.Context) error {
				return s.client.GitCheckout(ctx, threadID, item.name)
			})
		default:
			if branchOtherWorktree(item.branch, state.WorkDir) {
				runWithBranch(true, item.name, item.kind, "已切换到 "+item.name, func(ctx context.Context) error {
					return s.client.GitSwitchWorktree(ctx, threadID, item.branch.WorktreePath)
				})
			} else {
				runWithBranch(true, item.name, item.kind, "已切换到 "+item.name, func(ctx context.Context) error {
					return s.client.GitCheckout(ctx, threadID, item.name)
				})
			}
		}
	}

	choose := func(item branchPopoverItem) {
		if busy {
			return
		}
		if item.kind == "create" {
			applyChoose(item)
			return
		}
		current := item.kind == "local" && (item.branch.Head || item.name == state.Branch)
		if current {
			p.mu.Lock()
			p.dirtyTarget = ""
			p.dirtyKind = ""
			p.retryBranch = ""
			p.err = git.Failure{}
			p.mu.Unlock()
			*open = false
			return
		}
		if state.Changes > 0 {
			p.mu.Lock()
			p.dirtyTarget = item.name
			p.dirtyKind = item.kind
			p.retryBranch = ""
			p.err = git.Failure{}
			p.mu.Unlock()
			s.invalidate()
			return
		}
		applyChoose(item)
	}

	submitCreate := func(raw string) {
		name := strings.TrimSpace(raw)
		if name == "" || busy {
			return
		}
		base := createBase
		p.mu.Lock()
		p.createOpen = false
		p.createBase = ""
		p.createName = ""
		p.mu.Unlock()
		if base != "" && advanced {
			runWithBranch(true, name, "local", "已创建分支 "+name, func(ctx context.Context) error {
				return client.GitCreateBranchAt(ctx, threadID, name, base, true)
			})
			return
		}
		runWithBranch(true, name, "local", "已创建分支 "+name, func(ctx context.Context) error {
			return s.client.GitCreateBranch(ctx, threadID, name)
		})
	}

	needle := strings.TrimSpace(query)
	exists := false
	var localsAll, remotesAll, wtAll []git.BranchInfo
	var locals, remotes, tagItems, wtItems, recentItems, otherLocals []branchPopoverItem
	byName := map[string]git.BranchInfo{}
	for _, b := range branches {
		if b.Name == needle {
			exists = true
		}
		if !b.Remote {
			byName[b.Name] = b
			localsAll = append(localsAll, b)
		} else {
			remotesAll = append(remotesAll, b)
		}
		if b.WorktreePath != "" {
			wtAll = append(wtAll, b)
		}
		if !branchMatches(b.Name, needle) {
			continue
		}
		item := branchPopoverItem{name: b.Name, kind: "local", branch: b}
		if b.Remote {
			item.kind = "remote"
			remotes = append(remotes, item)
		} else {
			locals = append(locals, item)
			if b.WorktreePath != "" {
				wtItems = append(wtItems, item)
			}
		}
	}
	if needle == "" {
		seenRecent := make(map[string]bool, len(recent))
		for _, name := range recent {
			if seenRecent[name] {
				continue
			}
			if b, ok := byName[name]; ok {
				seenRecent[name] = true
				recentItems = append(recentItems, branchPopoverItem{name: name, kind: "local", branch: b})
			}
		}
		for _, item := range locals {
			if !seenRecent[item.name] {
				otherLocals = append(otherLocals, item)
			}
		}
	}
	for _, t := range tags {
		if branchMatches(t.Name, needle) {
			tagItems = append(tagItems, branchPopoverItem{name: t.Name, kind: "tag"})
		}
	}

	var items []branchPopoverItem
	if needle != "" {
		if !exists {
			items = append(items, branchPopoverItem{name: needle, kind: "create"})
		}
		items = append(items, locals...)
		items = append(items, remotes...)
		items = append(items, tagItems...)
	} else {
		switch tab {
		case branchTabRemote:
			items = append(items, remotes...)
		case branchTabTag:
			items = append(items, tagItems...)
		case branchTabWT:
			items = append(items, wtItems...)
		default:
			items = append(items, recentItems...)
			items = append(items, otherLocals...)
		}
	}
	if selected >= len(items) {
		selected = 0
	}

	_, winW := c.Size()
	col := ui.Column(c.Key("git-branch-content"))
	if withCard {
		col = popoverCard(c, col).Width(max(280, min(width, 360))).Border(Hairline, ThemeBorder(c))
	} else {
		w := width
		if winW > 0 {
			w = max(240, min(width, winW-Space6))
		}
		col.Width(w)
	}

	monoFont := branchPopoverMonoFont(s)
	col.Label("分支列表").MaxWidthPercent(100).Children(func() {
		// a. 搜索栏
		ui.Row(c.Key("git-branch-search-bar")).FillWidth().Padding(Space2).Gap(Space1).AlignItems(ui.Center).Children(func() {
			SearchField(c, TextFieldProps{
				Key:         "git-branch-search",
				Value:       &query,
				Label:       "搜索分支",
				Placeholder: "搜索或输入新分支名…",
				Grow:        true,
				AutoFocus:   !createOpen && edit == "",
				OnChange: func(v string) {
					p.mu.Lock()
					p.query = v
					p.selected = 0
					p.mu.Unlock()
				},
				OnKeyDown: func(k ui.TextKey) bool {
					switch k.Key {
					case ui.KeyEscape:
						*open = false
						return true
					case ui.KeyUp:
						p.mu.Lock()
						if p.selected > 0 {
							p.selected--
						}
						p.mu.Unlock()
						return true
					case ui.KeyDown:
						p.mu.Lock()
						if p.selected+1 < len(items) {
							p.selected++
						}
						p.mu.Unlock()
						return true
					case ui.KeyEnter:
						if len(items) > 0 {
							choose(items[selected])
						}
						return true
					}
					return false
				},
			}).Radius(RadiusSM).MinWidth(0).Shrink(1)
			IconButton(c, IconButtonProps{
				Key:      "git-branch-create-toggle",
				Icon:     IconPlus(),
				Label:    "新建分支",
				Tooltip:  "新建分支",
				Size:     ControlMD,
				Variant:  ButtonGhost,
				Active:   createOpen,
				Disabled: busy,
				OnClick: func() {
					p.mu.Lock()
					p.createOpen = !p.createOpen
					p.createBase = ""
					if !p.createOpen {
						p.createName = ""
					}
					p.menu = ""
					p.mu.Unlock()
				},
			}).Shrink(0)
		})
		branchPopoverDivider(c)

		// b. 内联新建表单（展开时渲染）
		if createOpen {
			ui.Column(c.Key("git-branch-inline-create")).FillWidth().Padding(Space2).Gap(Space2).Background(ThemeSurfaceMain(c)).Children(func() {
				TextField(c, TextFieldProps{
					Key:         "git-branch-create-input",
					Value:       &createName,
					Icon:        IconGitBranch(),
					Label:       "新分支名",
					Placeholder: "输入新分支名，如 feat/nav",
					FillWidth:   true,
					AutoFocus:   true,
					Disabled:    busy,
					OnChange: func(v string) {
						p.mu.Lock()
						p.createName = v
						p.mu.Unlock()
					},
					OnKeyDown: func(k ui.TextKey) bool {
						switch k.Key {
						case ui.KeyEscape:
							p.mu.Lock()
							p.createOpen = false
							p.createBase = ""
							p.createName = ""
							p.mu.Unlock()
							return true
						case ui.KeyEnter:
							submitCreate(k.Text())
							return true
						}
						return false
					},
				}).Radius(RadiusSM)
				ui.Row(c.Key("git-branch-create-actions")).FillWidth().AlignItems(ui.Center).Gap(Space2).Children(func() {
					baseLabel := createBase
					if baseLabel == "" {
						baseLabel = state.Branch
					}
					if baseLabel == "" {
						baseLabel = state.Head
					}
					ui.Text(c, "基于 "+baseLabel).FontSize(uiFont(TypeCaption)).TextColor(ThemeSecondaryText(c)).SingleLine().Ellipsis("…").Grow(1).Shrink(1).MinWidth(0)
					ui.Row(c.Key("git-branch-create-buttons")).Gap(Space1).Shrink(0).Children(func() {
						Button(c, ButtonOpts{
							Key:      "git-branch-create-cancel",
							Text:     "取消",
							Variant:  ButtonSecondary,
							Size:     ControlSM,
							Disabled: busy,
							OnClick: func() {
								p.mu.Lock()
								p.createOpen = false
								p.createBase = ""
								p.createName = ""
								p.mu.Unlock()
							},
						})
						Button(c, ButtonOpts{
							Key:      "git-branch-create-submit",
							Text:     "创建并切换",
							Variant:  ButtonPrimary,
							Size:     ControlSM,
							Disabled: busy || strings.TrimSpace(createName) == "",
							OnClick:  func() { submitCreate(createName) },
						})
					})
				})
			})
			branchPopoverDivider(c)
		}

		// c. 当前分支同步条
		curBranch := state.Branch
		if state.Detached || curBranch == "" {
			curBranch = state.Head
		}
		syncLabel, syncAction := gitPaneSyncAction(state)
		if !state.HasUpstream {
			syncLabel = "发布分支"
		}
		ps := branchPopoverGitPane(s, threadID)
		syncing := false
		if ps != nil {
			ps.mu.Lock()
			syncing = ps.syncing
			ps.mu.Unlock()
		}
		syncBtnText := syncLabel
		if syncing {
			syncBtnText = "同步中"
		}
		syncStatus := "尚未发布到远端"
		if state.HasUpstream {
			if state.Ahead == 0 && state.Behind == 0 {
				syncStatus = "与远端一致"
			} else {
				syncStatus = fmt.Sprintf("↑%d 领先 · ↓%d 落后", state.Ahead, state.Behind)
			}
		}
		if state.Changes > 0 {
			syncStatus += fmt.Sprintf(" · %d 改动", state.Changes)
		}
		ui.Row(c.Key("git-branch-sync-strip")).FillWidth().Padding(Space2, Space3).Gap(Space2).AlignItems(ui.Center).Background(ThemeOverlayHover(c)).Children(func() {
			ui.Column(c.Key("git-branch-sync-meta")).Grow(1).Shrink(1).MinWidth(0).Gap(Space0).Children(func() {
				ui.Row(c.Key("git-branch-sync-branch-row")).FillWidth().AlignItems(ui.Center).Gap(Space1).Children(func() {
					ui.Text(c, curBranch).FontSize(uiFont(TypeFootnote)).FontWeight(weightFor(c, 600)).TextColor(ThemePrimaryText(c)).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
					IconButton(c, IconButtonProps{
						Key:      "git-branch-copy-current",
						Icon:     IconCopy(),
						Label:    "复制当前分支名",
						Tooltip:  "复制分支名",
						Size:     ControlXS,
						Variant:  ButtonGhost,
						Disabled: curBranch == "",
						OnClick: func() {
							c.WriteClipboard(curBranch)
							s.setToast("已复制分支名")
						},
					}).Shrink(0)
				})
				ui.Text(c, syncStatus).FontSize(uiFont(TypeCaption)).TextColor(ThemeSecondaryText(c)).SingleLine().Ellipsis("…")
			})
			Button(c, ButtonOpts{
				Key:      "git-branch-sync-btn",
				Text:     syncBtnText,
				Icon:     IconRefreshCw(),
				Variant:  ButtonPrimary,
				Size:     ControlSM,
				Loading:  syncing,
				Disabled: busy || state.WorkDir == "" || state.Detached,
				OnClick: func() {
					if ps != nil {
						ps.run(syncAction)
					}
				},
			}).Shrink(0)
		})
		branchPopoverDivider(c)

		// d. 分段 Tab（搜索词为空时显示）
		if needle == "" {
			tabs := []struct {
				id, label string
				count     int
			}{
				{branchTabLocal, "本地", len(localsAll)},
				{branchTabRemote, "远端", len(remotesAll)},
				{branchTabTag, "标签", len(tags)},
				{branchTabWT, "Worktree", len(wtAll)},
			}
			ui.Row(c.Key("git-branch-tabs")).Role(ui.RoleTabList).FillWidth().Padding(Space1, Space2).Gap(Space0).AlignItems(ui.Center).Children(func() {
				for _, tb := range tabs {
					tb := tb
					active := tab == tb.id
					tabBtn := ui.ButtonBase(c.Key("git-branch-tab-"+tb.id)).
						Role(ui.RoleTab).
						Label(tb.label).
						Selected(active).
						Height(ControlXS).
						Padding(0, Space2).
						Radius(RadiusSM).
						Gap(Space1).
						AlignItems(ui.Center).
						Transition(motionFeedback(c)).
						OnClick(func() {
							p.mu.Lock()
							p.tab = tb.id
							p.selected = 0
							p.menu = ""
							p.mu.Unlock()
						})
					fg := ThemeSecondaryText(c)
					if active {
						tabBtn.Background(ThemeOverlaySelected(c))
						fg = ThemePrimaryText(c)
					} else if tabBtn.Hovered() {
						tabBtn.Background(ThemeOverlayHover(c))
						fg = ThemePrimaryText(c)
					}
					focusRing(c, tabBtn)
					tabBtn.Children(func() {
						label := ui.Text(c, tb.label).FontSize(uiFont(TypeCaption)).TextColor(fg).SingleLine()
						if active {
							label.FontWeight(weightFor(c, 500))
						}
						ui.Text(c, strconv.Itoa(tb.count)).FontSize(uiFont(TypeCaption)).TextColor(ThemeTertiaryText(c)).SingleLine()
					})
				}
			})
			branchPopoverDivider(c)
		}

		// e. 列表
		ui.Scroll(c.Key("git-branch-list-scroll")).Label("分支").FillWidth().MaxHeight(branchPopoverListMaxHeight).Children(func() {
			ui.Column(c.Key("git-branch-list")).FillWidth().Padding(Space1).Gap(Space0).Children(func() {
				index := 0
				renderGroupLabel := func(title string) {
					ui.Row(c.Key("branch-group-label-"+title)).FillWidth().Padding(Space1, Space2).Children(func() {
						ui.Text(c, title).FontSize(uiFont(TypeCaption)).FontWeight(weightFor(c, 500)).TextColor(ThemeTertiaryText(c)).SingleLine()
					})
				}
				renderItem := func(item branchPopoverItem) {
					idx := index
					index++
					key := fmt.Sprintf("branch-%s-%s-%d", item.kind, item.name, idx)
					if item.kind == "create" {
						menuRow(c, menuRowProps{
							Key:         key,
							Label:       "新建分支 " + item.name,
							Icon:        IconPlus(),
							Title:       "新建分支 “" + item.name + "”",
							Trailing:    "Enter",
							Highlighted: idx == selected,
							Disabled:    busy,
							OnClick:     func() { choose(item) },
						})
						return
					}
					if item.kind == "local" && edit == "rename:"+item.name {
						ui.Row(c.Key(key+"-rename")).FillWidth().Padding(Space0, Space1).Children(func() {
							TextField(c, TextFieldProps{
								Key:         "branch-rename-input",
								Value:       &editName,
								Label:       "重命名分支 " + item.name,
								Placeholder: "新分支名",
								FillWidth:   true,
								AutoFocus:   true,
								Disabled:    busy,
								OnChange: func(v string) {
									p.mu.Lock()
									p.editName = v
									p.mu.Unlock()
								},
								OnKeyDown: func(k ui.TextKey) bool {
									if k.Key == ui.KeyEscape {
										p.mu.Lock()
										p.edit = ""
										p.mu.Unlock()
										return true
									}
									if k.Key == ui.KeyEnter {
										next := strings.TrimSpace(k.Text())
										if next != "" {
											oldName := item.name
											p.mu.Lock()
											p.edit = ""
											p.mu.Unlock()
											runWithBranch(false, oldName, "local", "已重命名为 "+next, func(ctx context.Context) error {
												return client.GitBranchRename(ctx, threadID, oldName, next)
											})
										}
										return true
									}
									return false
								},
							}).Radius(RadiusSM)
						})
						return
					}
					current := item.kind == "local" && (item.branch.Head || item.name == state.Branch)
					type rowAction struct {
						label  string
						danger bool
						run    func()
					}
					var actions []rowAction
					if item.kind == "local" && advanced {
						actions = append(actions,
							rowAction{label: "基于此新建分支", run: func() {
								p.mu.Lock()
								p.createOpen = true
								p.createBase = item.name
								p.createName = item.name + "-copy"
								p.menu = ""
								p.mu.Unlock()
							}},
							rowAction{label: "复制分支名", run: func() {
								c.WriteClipboard(item.name)
								s.setToast("已复制分支名")
							}},
							rowAction{label: "重命名", run: func() {
								p.mu.Lock()
								p.edit = "rename:" + item.name
								p.editName = item.name
								p.menu = ""
								p.mu.Unlock()
							}},
						)
						if !current {
							actions = append(actions,
								rowAction{label: "删除…", danger: true, run: func() {
									p.mu.Lock()
									p.menu = ""
									p.confirm = "delete-branch:" + item.name
									p.mu.Unlock()
									*open = false
									s.invalidate()
								}},
								rowAction{label: "合并到当前", run: func() {
									runWithBranch(false, item.name, item.kind, "已合并 "+item.name, func(ctx context.Context) error {
										return s.client.GitMergeFrom(ctx, threadID, item.name)
									})
								}},
								rowAction{label: "将当前变基到此", run: func() {
									runWithBranch(false, item.name, item.kind, "已变基到 "+item.name, func(ctx context.Context) error {
										return client.GitRebase(ctx, threadID, item.name)
									})
								}},
							)
						}
					}

					row := ui.Row(c.Key(key + "-row")).FillWidth().Height(RowHeight).AlignItems(ui.Center).Radius(RadiusSM).Transition(motionFeedback(c))
					if idx == selected && needle != "" {
						row.Background(ThemeHover(c))
					} else if current {
						row.Background(ThemeSelected(c))
					} else if idx == selected {
						row.Background(ThemeHover(c))
					} else if row.Hovered() {
						row.Background(ThemeHover(c))
					}
					row.Children(func() {
						btn := ui.ButtonBase(c.Key(key)).Label("切换到分支 "+item.name).Grow(1).Shrink(1).MinWidth(0).Height(RowHeight).Padding(0, Space2).Justify(ui.Start).Disabled(busy)
						scale := motionPressScale(c, btn, "button-press-scale", !busy && btn.Pressed())
						btn.Scale(scale, scale)
						btn.OnClick(func() { choose(item) })
						if len(actions) > 0 {
							btn.ContextMenu(func(m *ui.Menu) {
								for _, a := range actions {
									entry := m.Item(a.label).Disabled(busy)
									if entry.Chosen() {
										a.run()
									}
								}
							})
						}
						btn.Children(func() {
							ui.Row(c.Key(key + "-text")).FillWidth().MinWidth(0).AlignItems(ui.Center).Gap(Space2).Children(func() {
								ui.Box(c.Key(key + "-check-slot")).Width(IconSM).Height(IconSM).Center().Shrink(0).Children(func() {
									if current {
										ui.Icon(c, IconCheck()).Size(IconSM, IconSM).TextColor(ThemeAccent(c))
									}
								})
								ui.Text(c, item.name).FontSize(uiFont(TypeFootnote)).TextColor(ThemePrimaryText(c)).Grow(1).Shrink(1).MinWidth(0).SingleLine().Ellipsis("…")
								if showBusy && busyName == item.name {
									LoadingIndicator(c, nil, "", IconSM)
								}
								if branchOtherWorktree(item.branch, state.WorkDir) {
									ui.Row(c.Key(key+"-wt-badge")).Padding(0, Space1).Radius(RadiusXS).Background(ThemeOverlayHover(c)).Tooltip(item.branch.WorktreePath).Shrink(0).Children(func() {
										ui.Text(c, "WT").Font(monoFont).FontSize(uiFont(TypeMicro)).TextColor(ThemeSecondaryText(c)).SingleLine()
									})
								}
								if item.branch.Ahead > 0 {
									ui.Text(c, fmt.Sprintf("↑%d", item.branch.Ahead)).FontSize(uiFont(TypeCaption)).TextColor(ThemeSuccess(c)).Shrink(0)
								}
								if item.branch.Behind > 0 {
									ui.Text(c, fmt.Sprintf("↓%d", item.branch.Behind)).FontSize(uiFont(TypeCaption)).TextColor(ThemeWarning(c)).Shrink(0)
								}
								if item.branch.UpstreamGone {
									ui.Icon(c, IconAlertTriangle()).Size(IconSM, IconSM).TextColor(ThemeWarning(c)).Tooltip("上游已删除")
								}
							})
						})
						if len(actions) > 0 {
							more := IconButton(c, IconButtonProps{Key: key + "-more", Icon: IconMoreHorizontal(), Label: "分支操作 " + item.name, Disabled: busy, Variant: ButtonGhost, Size: ControlSM, OnClick: func() {
								p.mu.Lock()
								p.menu = key
								p.mu.Unlock()
							}}).Shrink(0)
							if !row.Hovered() && !more.Hovered() && menu != key {
								more.Opacity(0)
							}
							menuOpen := menu == key
							appPopover(c.Key(key+"-menu"), more, &menuOpen, func() {
								p.mu.Lock()
								if p.menu == key {
									p.menu = ""
								}
								p.mu.Unlock()
							}, func() {
								popoverCard(c, ui.Column(c.Key(key+"-menu-content"))).Padding(Space1).Gap(Space0).FocusGroup(ui.Vertical).Children(func() {
									for _, a := range actions {
										a := a
										menuRow(c, menuRowProps{
											Key:      key + a.label,
											Label:    a.label,
											Title:    a.label,
											Danger:   a.danger,
											Disabled: busy,
											OnClick:  func() { p.mu.Lock(); p.menu = ""; p.mu.Unlock(); a.run() },
										})
									}
								})
							})
						}
					})
				}

				if loading && !loaded {
					ui.Row(c.Key("git-branch-loading")).FillWidth().Padding(Space2).Center().Children(func() {
						LoadingIndicator(c, nil, "正在加载…", IconSM)
					})
				} else if len(items) == 0 && needle == "" {
					ui.Row(c.Key("git-branch-empty")).FillWidth().Padding(Space4).Center().Children(func() {
						ui.Text(c, "无匹配项").FontSize(uiFont(TypeFootnote)).TextColor(ThemeTertiaryText(c))
					})
				} else if needle != "" {
					for _, item := range items {
						renderItem(item)
					}
				} else if tab == branchTabLocal {
					if len(recentItems) > 0 {
						renderGroupLabel("最近")
						for _, item := range recentItems {
							renderItem(item)
						}
						if len(otherLocals) > 0 {
							renderGroupLabel("全部本地")
						}
					}
					for _, item := range otherLocals {
						renderItem(item)
					}
				} else {
					for _, item := range items {
						renderItem(item)
					}
				}
			})
		})

		// f. 脏工作区提示条
		alertTarget := dirtyTarget
		alertKind := dirtyKind
		if alertTarget == "" && failure.Kind == git.FailureWorktree {
			alertTarget = retryBranch
		}
		if alertTarget != "" {
			push, canPush := s.client.(gitStashPushClient)
			stash, canPop := s.client.(gitStashClient)
			alertMsg := "当前有未提交改动，切换到 " + alertTarget + " 前需先处理。"
			if state.Changes > 0 {
				alertMsg = fmt.Sprintf("当前有 %d 个未提交改动，切换到 %s 前需先处理。", state.Changes, alertTarget)
			}
			ui.Column(c.Key("git-branch-dirty-alert")).FillWidth().Padding(Space1).Children(func() {
				ui.Column(c.Key("git-branch-dirty-box")).
					FillWidth().
					Padding(Space2).
					Gap(Space2).
					Radius(RadiusMD).
					Background(ThemeWarning(c).Alpha(branchPopoverDirtyBgAlpha)).
					Border(Hairline, ThemeWarning(c).Alpha(branchPopoverDirtyLineAlpha)).
					Children(func() {
						ui.Text(c, alertMsg).FontSize(uiFont(TypeFootnote)).TextColor(ThemePrimaryText(c))
						ui.Row(c.Key("git-branch-dirty-actions")).Gap(Space2).AlignItems(ui.Center).Children(func() {
							Button(c, ButtonOpts{
								Key:      "branch-stash-switch",
								Text:     "贮藏后切换",
								Variant:  ButtonPrimary,
								Size:     ControlSM,
								Disabled: busy || !canPush || !canPop,
								OnClick: func() {
									runWithBranch(true, alertTarget, alertKind, "", func(ctx context.Context) error {
										if err := push.GitStashPush(ctx, threadID, gitSwitchStashMarker); err != nil {
											return err
										}
										var checkoutErr error
										if alertKind == "remote" && advanced {
											_, checkoutErr = client.GitCheckoutRemoteBranch(ctx, threadID, alertTarget)
										} else {
											checkoutErr = s.client.GitCheckout(ctx, threadID, alertTarget)
										}
										if checkoutErr != nil {
											if popErr := stash.GitStashPopAt(ctx, threadID, 0); popErr != nil {
												s.setToast("恢复贮藏失败，改动保留在贮藏中")
											}
											return checkoutErr
										}
										s.setToast("已贮藏改动并切换")
										return nil
									})
								},
							})
							Button(c, ButtonOpts{
								Key:      "branch-dirty-view-changes",
								Text:     "查看改动",
								Variant:  ButtonSecondary,
								Size:     ControlSM,
								Disabled: busy,
								OnClick: func() {
									p.mu.Lock()
									p.dirtyTarget = ""
									p.dirtyKind = ""
									p.retryBranch = ""
									p.err = git.Failure{}
									p.mu.Unlock()
									*open = false
									inspectorSelect(s, sidePaneGitID, false)
								},
							})
						})
					})
			})
		} else if failure.Title != "" && failure.Kind != git.FailureWorktree {
			ui.Column(c.Key("git-branch-error-wrap")).FillWidth().Padding(Space1, Space2).Gap(Space1).Children(func() {
				renderGitError(c, "branch-error", failure.Title)
				if failure.Hint != "" {
					ui.Text(c, failure.Hint).FontSize(uiFont(TypeCaption)).TextColor(ThemeSecondaryText(c))
				}
			})
		}

		// g. 底部
		branchPopoverDivider(c)
		ui.Column(c.Key("git-branch-footer")).FillWidth().Padding(Space1).Children(func() {
			menuRow(c, menuRowProps{
				Key:      "git-branch-open-pane",
				Icon:     IconPanelRight(),
				Title:    "打开 Git 面板",
				Trailing: "⇧⌘G",
				OnClick: func() {
					*open = false
					inspectorSelect(s, sidePaneGitID, false)
				},
			})
		})
	})
}

// renderGitBranchConfirmDialog 在浮层外部渲染分支删除确认框，保证关闭浮层后 DialogBase 仍能显示。
func renderGitBranchConfirmDialog(c *ui.Context, s *chatState, threadID string) {
	if s == nil || threadID == "" {
		return
	}
	client, advanced := s.client.(gitBranchClient)
	if !advanced {
		return
	}
	p := branchPopoverFor(s, threadID)
	p.mu.Lock()
	confirm := p.confirm
	p.mu.Unlock()

	var kind, name, confirmLabel, confirmTitle string
	switch {
	case strings.HasPrefix(confirm, "delete-branch:"):
		kind = "delete-branch"
		name = strings.TrimPrefix(confirm, "delete-branch:")
		confirmLabel = "删除"
		confirmTitle = "删除分支 " + name + "？"
	case strings.HasPrefix(confirm, "force-branch:"):
		kind = "force-branch"
		name = strings.TrimPrefix(confirm, "force-branch:")
		confirmLabel = "强制删除"
		confirmTitle = "强制删除 " + name + "？"
	case strings.HasPrefix(confirm, "tag:"):
		kind = "tag"
		name = strings.TrimPrefix(confirm, "tag:")
		confirmLabel = "删除"
		confirmTitle = "删除标签 " + name + "？"
	}

	dialogOpen := kind != ""
	cancelConfirm := func() {
		p.mu.Lock()
		p.confirm = ""
		p.mu.Unlock()
	}

	RenderConfirmDialog(c, ConfirmDialog{
		Key:             "git-branch-confirm",
		Open:            dialogOpen,
		Tone:            ConfirmDanger,
		Title:           confirmTitle,
		ConfirmLabel:    confirmLabel,
		CancelLabel:     "取消",
		CancelAutoFocus: true,
		OnConfirm: func() {
			cancelConfirm()
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				var err error
				switch kind {
				case "delete-branch":
					err = client.GitBranchDelete(ctx, threadID, name, false)
					if branchDeleteNeedsForce(err) {
						p.mu.Lock()
						p.confirm = "force-branch:" + name
						p.mu.Unlock()
						s.invalidate()
						return
					}
				case "force-branch":
					err = client.GitBranchDelete(ctx, threadID, name, true)
				case "tag":
					err = client.GitTagDelete(ctx, threadID, name)
				}
				if err != nil {
					s.setToast("删除失败: " + err.Error())
				} else {
					s.setToast("已删除 " + name)
					s.loadGitState(threadID)
					loadBranchPopover(s, threadID, p)
				}
				s.invalidate()
			}()
		},
		OnCancel: cancelConfirm,
	})
}

func init() {
	gitPaneBranchPopover = func(c *ui.Context, s *chatState, threadID string, width float32, open *bool) {
		renderGitBranchPopoverBody(c, s, threadID, width, open, true)
	}
}
