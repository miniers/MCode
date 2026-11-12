package mcodeui

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

const modalDragGitRemotes = "modal-drag-git-remotes"
const gitRemotesListMaxHeight float32 = 320

type gitRemotesClient interface {
	GitRemotes(context.Context, string) ([]git.RemoteInfo, error)
	GitRemoteAdd(context.Context, string, string, string) error
	GitRemoteRemove(context.Context, string, string) error
	GitRemoteRename(context.Context, string, string, string) error
	GitRemoteSetURL(context.Context, string, string, string) error
}

type gitRemotesState struct {
	mu                    sync.Mutex
	active, loading, busy bool
	seq                   uint64
	entries               []git.RemoteInfo
	err                   string
	confirm               string
	confirmUntil          time.Time
	loadingSince          time.Time
	completed             bool
	editing, name, url    string
	adding                bool
}

var gitRemotesStates = struct {
	sync.Mutex
	states map[*bool]*gitRemotesState
}{states: make(map[*bool]*gitRemotesState)}

func gitRemotesStateFor(open *bool) *gitRemotesState {
	gitRemotesStates.Lock()
	defer gitRemotesStates.Unlock()
	st := gitRemotesStates.states[open]
	if st == nil {
		st = &gitRemotesState{}
		gitRemotesStates.states[open] = st
	}
	return st
}

func (st *gitRemotesState) fetch(s *chatState, client gitRemotesClient, threadID string) {
	st.mu.Lock()
	st.loading = true
	st.loadingSince = time.Now()
	st.seq++
	seq := st.seq
	st.mu.Unlock()
	time.AfterFunc(150*time.Millisecond, func() {
		st.mu.Lock()
		show := st.active && st.loading && st.seq == seq
		st.mu.Unlock()
		if show {
			s.invalidate()
		}
	})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		entries, err := client.GitRemotes(ctx, threadID)
		st.mu.Lock()
		if !st.active || st.seq != seq {
			st.mu.Unlock()
			return
		}
		st.loading = false
		if err != nil {
			st.err = git.ClassifyError(err).Title
		} else {
			st.entries = entries
		}
		st.mu.Unlock()
		s.invalidate()
	}()
}

func (st *gitRemotesState) run(s *chatState, client gitRemotesClient, threadID string, op func(context.Context) error) {
	st.mu.Lock()
	if st.busy {
		st.mu.Unlock()
		return
	}
	st.busy = true
	st.loading = false
	st.err = ""
	st.confirm = ""
	st.seq++
	seq := st.seq
	st.mu.Unlock()
	s.invalidate()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := op(ctx)
		entries, loadErr := client.GitRemotes(ctx, threadID)
		st.mu.Lock()
		if !st.active || st.seq != seq {
			st.mu.Unlock()
			return
		}
		st.busy = false
		if loadErr == nil {
			st.entries = entries
		}
		if err != nil {
			st.err = git.ClassifyError(err).Title
		} else if loadErr != nil {
			st.err = git.ClassifyError(loadErr).Title
		} else {
			st.completed = true
		}
		st.mu.Unlock()
		s.invalidate()
	}()
}

func gitRemoteSave(ctx context.Context, client gitRemotesClient, threadID string, old git.RemoteInfo, name, url string) error {
	if name != old.Name {
		if err := client.GitRemoteRename(ctx, threadID, old.Name, name); err != nil {
			return err
		}
	}
	if url != old.FetchURL {
		return client.GitRemoteSetURL(ctx, threadID, name, url)
	}
	return nil
}

func gitRemoteValid(name, url string) bool {
	return strings.TrimSpace(name) != "" && strings.TrimSpace(url) != ""
}

func (st *gitRemotesState) deleteClick(s *chatState, client gitRemotesClient, threadID, name string) {
	st.mu.Lock()
	if st.busy {
		st.mu.Unlock()
		return
	}
	confirmed := st.confirm == name && time.Now().Before(st.confirmUntil)
	if !confirmed {
		st.confirm = name
		st.confirmUntil = time.Now().Add(3 * time.Second)
	}
	deadline := st.confirmUntil
	st.mu.Unlock()
	if confirmed {
		st.run(s, client, threadID, func(ctx context.Context) error { return client.GitRemoteRemove(ctx, threadID, name) })
		return
	}
	time.AfterFunc(3*time.Second, func() {
		st.mu.Lock()
		changed := st.confirm == name && st.confirmUntil == deadline
		if changed {
			st.confirm = ""
		}
		st.mu.Unlock()
		if changed {
			s.invalidate()
		}
	})
	s.invalidate()
}

func renderGitRemotesDialog(c *ui.Context, s *chatState, threadID string, open *bool) {
	if open == nil || s == nil {
		return
	}
	st := gitRemotesStateFor(open)
	if !*open {
		st.mu.Lock()
		st.active = false
		st.seq++
		st.mu.Unlock()
		gitRemotesStates.Lock()
		delete(gitRemotesStates.states, open)
		gitRemotesStates.Unlock()
		modalDragReset(modalDragGitRemotes)
		return
	}
	client, supported := s.client.(gitRemotesClient)
	st.mu.Lock()
	first := !st.active
	if first {
		st.active = true
	}
	st.mu.Unlock()
	if first && supported {
		st.fetch(s, client, threadID)
	}
	st.mu.Lock()
	entries := append([]git.RemoteInfo(nil), st.entries...)
	busy, loading, errText, confirm := st.busy, st.loading, st.err, st.confirm
	showLoading := loading && time.Since(st.loadingSince) >= 150*time.Millisecond
	completed := st.completed
	st.completed = false
	st.mu.Unlock()
	if completed {
		st.editing = ""
		st.adding = false
	}
	modalDragModal(c, modalDragGitRemotes, open, func() {
		ui.Column(c.Key("git-remotes-card")).Width(gitStashCardWidth).Background(ThemeSurface(c)).BorderColor(ThemeBorder(c)).BorderWidth(Hairline).Radius(RadiusLG).Padding(Space5).Gap(Space4).Children(func() {
			modalDragHandle(c, modalDragGitRemotes, ui.Row(c.Key("git-remotes-title")).FillWidth().AlignItems(ui.Center).Children(func() {
				ui.Text(c, "远端").FontSize(uiFont(TypeTitle)).FontWeight(600).TextColor(ThemePrimaryText(c))
				ui.Spacer(c)
			}))
			if showLoading {
				LoadingIndicator(c, nil, "", IconSM)
			}
			if len(entries) == 0 && !loading {
				ui.Text(c, "暂无远端").FontSize(uiFont(TypeBody)).TextColor(ThemeSecondaryText(c))
			}
			ui.Scroll(c.Key("git-remotes-scroll")).FillWidth().MaxHeight(gitRemotesListMaxHeight).Children(func() {
				ui.Column(c.Key("git-remotes-list")).FillWidth().Gap(Space1).Children(func() {
					for _, entry := range entries {
						entry := entry
						key := "git-remotes-" + entry.Name
						row := ui.Row(c.Key(key)).FillWidth().Height(RowHeight).Gap(Space2).AlignItems(ui.Center).Transition(motionListItem(c))
						row.Children(func() {
							if st.editing == entry.Name {
								gitRemoteInputs(c, st, key, busy, func() {
									if gitRemoteValid(st.name, st.url) {
										name, url := st.name, st.url
										st.run(s, client, threadID, func(ctx context.Context) error { return gitRemoteSave(ctx, client, threadID, entry, name, url) })
									}
								})
								return
							}
							ui.Text(c, entry.Name).FontSize(uiFont(TypeBody)).TextColor(ThemePrimaryText(c)).SingleLine()
							tip := entry.FetchURL
							if entry.PushURL != entry.FetchURL {
								tip = "Fetch: " + entry.FetchURL + "\nPush: " + entry.PushURL
							}
							url := []rune(entry.FetchURL)
							shown := entry.FetchURL
							if len(url) > 42 {
								shown = "…" + string(url[len(url)-41:])
							}
							ui.Text(c, shown).Tooltip(tip).Font("monospace").FontSize(uiFont(TypeCaption)).TextColor(ThemeTertiaryText(c)).SingleLine().Grow(1).Shrink(1).MinWidth(0)
							edit := ui.ButtonBase(c.Key(key+"-edit")).Label("编辑").Tooltip("编辑").Height(ControlSM).Padding(Space0, Space2).Disabled(busy).Transition(motionFeedback(c))
							if !row.Hovered() && !edit.Focused() && confirm != entry.Name {
								edit.Opacity(0)
							}
							edit.Children(func() { ui.Text(c, "编辑").FontSize(uiFont(TypeCaption)).TextColor(ThemeSecondaryText(c)) })
							if edit.Clicked() {
								st.editing = entry.Name
								st.name = entry.Name
								st.url = entry.FetchURL
								st.adding = false
							}
							label := "删除"
							if confirm == entry.Name {
								label = "确认删除"
							}
							del := ui.ButtonBase(c.Key(key+"-delete")).Label(label).Height(ControlSM).Padding(Space0, Space2).Disabled(busy).Transition(motionFeedback(c))
							if !row.Hovered() && !del.Focused() && confirm != entry.Name {
								del.Opacity(0)
							}
							del.Children(func() { ui.Text(c, label).FontSize(uiFont(TypeCaption)).TextColor(ThemeDanger(c)) })
							if del.Clicked() {
								st.deleteClick(s, client, threadID, entry.Name)
							}
						})
					}
				})
			})
			if supported {
				if st.adding {
					ui.Row(c.Key("git-remotes-add-form")).FillWidth().Height(RowHeight).Gap(Space2).AlignItems(ui.Center).Children(func() {
						gitRemoteInputs(c, st, "git-remotes-add", busy, func() {
							if gitRemoteValid(st.name, st.url) {
								name, url := st.name, st.url
								st.run(s, client, threadID, func(ctx context.Context) error { return client.GitRemoteAdd(ctx, threadID, name, url) })
							}
						})
						Button(c, ButtonOpts{Key: "git-remotes-add-submit", Text: "添加", Variant: ButtonPrimary, Disabled: busy || !gitRemoteValid(st.name, st.url), OnClick: func() {
							name, url := st.name, st.url
							st.run(s, client, threadID, func(ctx context.Context) error { return client.GitRemoteAdd(ctx, threadID, name, url) })
						}})
					})
				} else {
					Button(c, ButtonOpts{Key: "git-remotes-add", Text: "＋ 添加远端", Variant: ButtonSecondary, Disabled: busy, OnClick: func() { st.adding = true; st.editing = ""; st.name = ""; st.url = "" }})
				}
			}
			if errText != "" {
				ui.Text(c, errText).FontSize(uiFont(TypeFootnote)).TextColor(ThemeDanger(c)).SingleLine()
			}
		})
	})
}

func gitRemoteInputs(c *ui.Context, st *gitRemotesState, key string, busy bool, submit func()) {
	keyHandler := func(value *string) func(ui.TextKey) bool {
		return func(k ui.TextKey) bool {
			if k.Key == ui.KeyEscape {
				st.editing = ""
				st.adding = false
				return true
			}
			if k.Key == ui.KeyEnter && !busy {
				*value = k.Text()
				submit()
				return true
			}
			return false
		}
	}
	ui.TextInput(c.Key(key+"-name"), &st.name).Label("名称").Placeholder("origin").Grow(1).MinWidth(0).Disabled(busy).OnTextKey(keyHandler(&st.name))
	ui.TextInput(c.Key(key+"-url"), &st.url).Label("URL").Placeholder("https://… 或 git@…").Grow(2).MinWidth(0).Disabled(busy).OnTextKey(keyHandler(&st.url))
}

func init() { gitPaneRemotesDialog = renderGitRemotesDialog }
