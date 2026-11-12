package mcodeui

import (
	"context"
	"fmt"
	"path/filepath"
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

type gitBranchPopupState struct {
	mu                                        sync.Mutex
	query, err, edit, editName, confirm, menu string
	branches                                  []git.BranchInfo
	recent                                    []string
	tags                                      []git.TagInfo
	loaded, loading, busy                     bool
	loadStarted                               time.Time
	local, remote, tag                        bool
	selected                                  int
	seq                                       uint64
}

var gitBranchPopups sync.Map

func branchPopoverFor(s *chatState, id string) *gitBranchPopupState {
	v, _ := gitBranchPopups.LoadOrStore(struct {
		s  *chatState
		id string
	}{s, id}, &gitBranchPopupState{local: true})
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
			p.loading = false
			p.loaded = err == nil
			if err != nil {
				p.err = git.ClassifyError(err).Title
			} else {
				p.branches = branches
				p.recent = recent
				p.tags = tags
				p.err = ""
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
	p.menu = ""
	p.confirm = ""
	p.loaded = false
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

func renderGitBranchPopoverContent(c *ui.Context, s *chatState, threadID string, width float32, open *bool) {
	p := branchPopoverFor(s, threadID)
	client, advanced := s.client.(gitBranchClient)
	state := s.headerGitState(threadID)
	p.mu.Lock()
	query, errMsg, edit, editName, confirm, menu := p.query, p.err, p.edit, p.editName, p.confirm, p.menu
	branches := append([]git.BranchInfo(nil), p.branches...)
	recent := append([]string(nil), p.recent...)
	tags := append([]git.TagInfo(nil), p.tags...)
	loaded, busy, local, remote, tag, selected := p.loaded, p.busy, p.local, p.remote, p.tag, p.selected
	loading := p.loading && time.Since(p.loadStarted) >= 150*time.Millisecond
	p.mu.Unlock()
	if !advanced || !loaded {
		branches = nil
		for _, name := range state.Branches {
			branches = append(branches, git.BranchInfo{Name: name, Head: name == state.Branch})
		}
	}
	run := func(closeOnSuccess bool, op func(context.Context) error) {
		p.mu.Lock()
		if p.busy {
			p.mu.Unlock()
			return
		}
		p.busy = true
		p.err = ""
		p.mu.Unlock()
		s.invalidate()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err := op(ctx)
			p.mu.Lock()
			p.busy = false
			if err != nil {
				p.err = git.ClassifyError(err).Title
			} else if closeOnSuccess {
				p.confirm = "close"
			}
			p.mu.Unlock()
			if err == nil {
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
	choose := func(item branchPopoverItem) {
		if busy {
			return
		}
		switch item.kind {
		case "create":
			run(true, func(ctx context.Context) error { return s.client.GitCreateBranch(ctx, threadID, item.name) })
		case "remote":
			run(true, func(ctx context.Context) error {
				_, err := client.GitCheckoutRemoteBranch(ctx, threadID, item.name)
				return err
			})
		case "tag":
			run(true, func(ctx context.Context) error { return s.client.GitCheckout(ctx, threadID, item.name) })
		default:
			run(true, func(ctx context.Context) error {
				if branchOtherWorktree(item.branch, state.WorkDir) {
					return s.client.GitSwitchWorktree(ctx, threadID, item.branch.WorktreePath)
				}
				return s.client.GitCheckout(ctx, threadID, item.name)
			})
		}
	}
	needle := strings.TrimSpace(query)
	exists := false
	var locals, remotes, tagItems, recentItems []branchPopoverItem
	byName := map[string]git.BranchInfo{}
	for _, b := range branches {
		if b.Name == needle {
			exists = true
		}
		if !b.Remote {
			byName[b.Name] = b
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
		}
	}
	for _, name := range recent {
		if b, ok := byName[name]; ok && branchMatches(name, needle) {
			recentItems = append(recentItems, branchPopoverItem{name: name, kind: "local", branch: b})
		}
	}
	for _, t := range tags {
		if branchMatches(t.Name, needle) {
			tagItems = append(tagItems, branchPopoverItem{name: t.Name, kind: "tag"})
		}
	}
	var items []branchPopoverItem
	if needle != "" && !exists {
		items = append(items, branchPopoverItem{name: needle, kind: "create"})
	}
	items = append(items, recentItems...)
	if local || needle != "" {
		items = append(items, locals...)
	}
	if remote || needle != "" {
		items = append(items, remotes...)
	}
	if tag || needle != "" {
		items = append(items, tagItems...)
	}
	if selected >= len(items) {
		selected = 0
	}
	ui.Column(c.Key("git-branch-content")).Width(width).Gap(Space1).Children(func() {
		SearchField(c, TextFieldProps{Key: "git-branch-search", Value: &query, Label: "搜索分支", Placeholder: "切换或新建分支…", FillWidth: true, AutoFocus: edit == "", OnChange: func(v string) { p.mu.Lock(); p.query = v; p.selected = 0; p.mu.Unlock() }, OnKeyDown: func(k ui.TextKey) bool {
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
		}})
		ui.Scroll(c.Key("git-branch-list-scroll")).FillWidth().MaxHeight(480).Children(func() {
			ui.Column(c.Key("git-branch-list")).FillWidth().Children(func() {
				index := 0
				renderItem := func(item branchPopoverItem) {
					idx := index
					index++
					key := fmt.Sprintf("branch-%s-%s-%d", item.kind, item.name, idx)
					if item.kind == "create" {
						renderGitMenuOption(c, key, "新建分支 "+item.name, "＋ 新建分支 “"+item.name+"”", "", false, busy, func() { choose(item) })
						return
					}
					current := item.kind == "local" && (item.branch.Head || item.name == state.Branch)
					type rowAction struct {
						label    string
						disabled bool
						run      func()
					}
					actions := []rowAction{{"切换", false, func() { choose(item) }}}
					if item.kind == "remote" {
						actions[0].label = "检出为本地分支"
					}
					if item.kind == "tag" {
						actions[0].label = "检出"
					}
					if item.kind == "local" {
						actions = append(actions,
							rowAction{"合并到当前", current, func() {
								run(false, func(ctx context.Context) error { return s.client.GitMergeFrom(ctx, threadID, item.name) })
							}},
							rowAction{"将当前变基到此", current, func() {
								run(false, func(ctx context.Context) error { return client.GitRebase(ctx, threadID, item.name) })
							}},
							rowAction{"基于此新建分支", false, func() { p.mu.Lock(); p.edit = "create:" + item.name; p.editName = ""; p.mu.Unlock() }},
							rowAction{"重命名", false, func() { p.mu.Lock(); p.edit = "rename:" + item.name; p.editName = item.name; p.mu.Unlock() }},
						)
					}
					if item.kind != "tag" {
						actions = append(actions, rowAction{"复制名称", false, func() { c.WriteClipboard(item.name) }})
					} else {
						actions = append(actions, rowAction{"推送", false, func() {
							run(false, func(ctx context.Context) error { return client.GitPushTag(ctx, threadID, "origin", item.name) })
						}})
					}
					if item.kind != "remote" {
						actions = append(actions, rowAction{"删除…", current, func() {
							if item.kind == "tag" {
								p.mu.Lock()
								p.confirm = "tag:" + item.name
								p.mu.Unlock()
								return
							}
							run(false, func(ctx context.Context) error {
								err := client.GitBranchDelete(ctx, threadID, item.name, false)
								if branchDeleteNeedsForce(err) {
									p.mu.Lock()
									p.confirm = "branch:" + item.name
									p.mu.Unlock()
									return nil
								}
								return err
							})
						}})
					}
					row := ui.Row(c.Key(key + "-row")).FillWidth().Height(RowHeight).AlignItems(ui.Center)
					row.Children(func() {
						btn := ui.ButtonBase(c.Key(key)).Label("切换到分支 " + item.name).Grow(1).Height(RowHeight).Radius(RadiusSM).Justify(ui.Start).Disabled(busy).Transition(motionFeedback(c))
						if idx == selected {
							btn.Background(ThemeSelected(c))
						} else if btn.Hovered() {
							btn.Background(ThemeHover(c))
						}
						btn.OnClick(func() { choose(item) })
						if advanced {
							btn.ContextMenu(func(m *ui.Menu) {
								for _, a := range actions {
									entry := m.Item(a.label).Disabled(a.disabled || busy)
									if a.disabled {
										entry.Tooltip("当前分支不可执行此操作")
									}
									if entry.Chosen() {
										a.run()
									}
								}
							})
						}

						btn.Children(func() {
							ui.Row(c.Key(key + "-text")).FillWidth().AlignItems(ui.Center).Gap(Space2).Children(func() {
								mark := ""
								if item.branch.Head || (item.kind == "local" && item.name == state.Branch) {
									mark = "✓"
								}
								ui.Text(c, mark).FontSize(uiFont(TypeFootnote)).TextColor(ThemeAccent(c))
								ui.Text(c, item.name).FontSize(uiFont(TypeFootnote)).TextColor(ThemePrimaryText(c)).Grow(1).SingleLine()
								if item.branch.Ahead > 0 {
									ui.Text(c, fmt.Sprintf("↑%d", item.branch.Ahead)).FontSize(uiFont(TypeCaption)).TextColor(ThemeSuccess(c))
								}
								if item.branch.Behind > 0 {
									ui.Text(c, fmt.Sprintf("↓%d", item.branch.Behind)).FontSize(uiFont(TypeCaption)).TextColor(ThemeWarning(c))
								}
								if item.branch.UpstreamGone {
									ui.Text(c, "⚠").Tooltip("上游已删除").TextColor(ThemeWarning(c))
								}
								if branchOtherWorktree(item.branch, state.WorkDir) {
									ui.Text(c, "◫").Tooltip(item.branch.WorktreePath)
								}
							})
						})
						if advanced {
							more := Button(c, ButtonOpts{Key: key + "-more", Text: "⋯", Label: "分支操作 " + item.name, Disabled: busy, Variant: ButtonSecondary, Size: ControlSM, OnClick: func() {
								p.mu.Lock()
								if p.menu == key {
									p.menu = ""
								} else {
									p.menu = key
								}
								p.mu.Unlock()
							}})
							if !row.Hovered() && !more.Hovered() && menu != key {
								more.Opacity(0)
							}
						}
					})
					if menu == key && advanced {
						for _, a := range actions {
							a := a
							opt := ui.ButtonBase(c.Key(key + a.label)).Label(a.label).FillWidth().Height(RowHeight).Justify(ui.Start).Disabled(a.disabled || busy).Radius(RadiusSM)
							if a.disabled {
								opt.Tooltip("当前分支不可执行此操作")
							} else if opt.Hovered() {
								opt.Background(ThemeHover(c))
							}
							opt.OnClick(a.run).Children(func() { ui.Text(c, a.label).FontSize(uiFont(TypeFootnote)).TextColor(ThemePrimaryText(c)) })
						}
					}
				}
				if needle != "" && !exists {
					renderItem(items[0])
				}
				group := func(title string, list []branchPopoverItem, expanded bool, set func()) {
					if len(list) == 0 && needle != "" {
						return
					}
					renderGitMenuOption(c, "branch-group-"+title, title, fmt.Sprintf("%s (%d)", title, len(list)), "", false, false, set)
					motionUnfoldBody(c, "branch-group-body-"+title, expanded || needle != "", func() {
						for _, item := range list {
							renderItem(item)
						}
					})
				}
				if len(recentItems) > 0 {
					ui.Text(c, "最近").FontSize(uiFont(TypeCaption)).TextColor(ThemeSecondaryText(c))
					for _, item := range recentItems {
						renderItem(item)
					}
				}
				group("本地", locals, local, func() { p.mu.Lock(); p.local = !p.local; p.mu.Unlock() })
				if advanced {
					group("远端", remotes, remote, func() { p.mu.Lock(); p.remote = !p.remote; p.mu.Unlock() })
					group("标签", tagItems, tag, func() { p.mu.Lock(); p.tag = !p.tag; p.mu.Unlock() })
				}
			})
		})
		if loading {
			LoadingIndicator(c, nil, "正在加载…", IconSM)
		}
		if edit != "" {
			TextField(c, TextFieldProps{Key: "branch-edit", Value: &editName, Label: "分支名", Placeholder: "分支名", FillWidth: true, AutoFocus: true, OnChange: func(v string) { p.mu.Lock(); p.editName = v; p.mu.Unlock() }, OnKeyDown: func(k ui.TextKey) bool {
				if k.Key == ui.KeyEscape {
					p.mu.Lock()
					p.edit = ""
					p.mu.Unlock()
					return true
				}
				if k.Key == ui.KeyEnter {
					name := strings.TrimSpace(k.Text())
					kind, old, _ := strings.Cut(edit, ":")
					if name != "" {
						p.mu.Lock()
						p.edit = ""
						p.mu.Unlock()
						run(kind == "create", func(ctx context.Context) error {
							if kind == "create" {
								return client.GitCreateBranchAt(ctx, threadID, name, old, true)
							}
							return client.GitBranchRename(ctx, threadID, old, name)
						})
					}
					return true
				}
				return false
			}})
		}
		if errMsg != "" {
			renderGitError(c, "branch-error", errMsg)
		}
	})
	if strings.HasPrefix(confirm, "branch:") || strings.HasPrefix(confirm, "tag:") {
		kind, name, _ := strings.Cut(confirm, ":")
		label := "删除"
		title := "删除标签 " + name + "？"
		if kind == "branch" {
			label = "强制删除"
			title = "强制删除 " + name + "？"
		}
		dialogOpen := true
		cancel := func() { p.mu.Lock(); p.confirm = ""; p.mu.Unlock() }
		modalDragModal(c, "git-branch-confirm", &dialogOpen, func() {
			ui.Column(c.Key("git-branch-confirm-card")).Width(360).Padding(Space6).Gap(Space4).Radius(RadiusLG).Background(ThemeSurface(c)).Children(func() {
				ui.Text(c, title).FontSize(uiFont(TypeTitle)).TextColor(ThemePrimaryText(c))
				ui.Row(c.Key("git-branch-confirm-actions")).FillWidth().Justify(ui.End).Gap(Space3).Children(func() {
					Button(c, ButtonOpts{Key: "git-branch-confirm-cancel", Text: "取消", Variant: ButtonSecondary, OnClick: cancel}).AutoFocus()
					Button(c, ButtonOpts{Key: "git-branch-confirm-accept", Text: label, Variant: ButtonDanger, OnClick: func() {
						cancel()
						run(false, func(ctx context.Context) error {
							if kind == "branch" {
								return client.GitBranchDelete(ctx, threadID, name, true)
							}
							return client.GitTagDelete(ctx, threadID, name)
						})
					}})
				})
			})
		})
		if !dialogOpen {
			cancel()
		}
	}
}

func init() { gitPaneBranchPopover = renderGitBranchPopoverContent }
