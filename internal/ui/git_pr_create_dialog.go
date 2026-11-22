package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

const (
	modalDragGitPRCreate         = "modal-drag-git-pr-create"
	gitPRCreateWidth     float32 = 560
)

type gitPRCreateClient interface {
	GitPRCreate(context.Context, string, string, string, string) (string, error)
}

type gitPRBranchListClient interface {
	GitBranches(context.Context, string) ([]git.BranchInfo, error)
}

// 输入和生成结果共用锁，关闭后取消生成并丢弃迟到结果。
type gitPRCreateDialog struct {
	mu                         sync.Mutex
	s                          *chatState
	threadID                   string
	state                      GitState
	open, generating, pushing  bool
	creating                   bool
	base                       string
	basePicked                 bool
	baseMenuOpen               bool
	branches                   []string
	title, body, err, readyURL string
	createdURL                 string
	createdNumber              int
	revision                   uint64
	cancel                     context.CancelFunc
	branchCancel               context.CancelFunc
}

func openGitPRCreateDialog(s *chatState, threadID string) {
	if s == nil || s.client == nil || threadID == "" {
		return
	}
	ps := getGitPaneState(s)
	if ps == nil || ps.threadID != threadID {
		return
	}
	if ps.prCreate != nil {
		ps.prCreate.close()
	}
	ps.mu.Lock()
	state := ps.state
	ps.mu.Unlock()
	if state.WorkDir == "" {
		state = s.headerGitState(threadID)
	}
	branches := filterPRBaseBranches(state.Branches, state.Branch)
	d := &gitPRCreateDialog{
		s:        s,
		threadID: threadID,
		state:    state,
		open:     true,
		branches: branches,
		base:     defaultPRBaseBranch(branches),
	}
	ps.prCreate = d
	d.loadBranches()
	d.generate()
	s.invalidate()
}

func (d *gitPRCreateDialog) close() {
	d.mu.Lock()
	d.open = false
	d.revision++
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
	if d.branchCancel != nil {
		d.branchCancel()
		d.branchCancel = nil
	}
	d.generating, d.pushing, d.creating, d.baseMenuOpen = false, false, false, false
	d.mu.Unlock()
	modalDragReset(modalDragGitPRCreate)
	d.s.invalidate()
}

func (d *gitPRCreateDialog) loadBranches() {
	client, ok := d.s.client.(gitPRBranchListClient)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	d.mu.Lock()
	if !d.open {
		d.mu.Unlock()
		cancel()
		return
	}
	d.branchCancel = cancel
	head := d.state.Branch
	d.mu.Unlock()
	go func() {
		defer cancel()
		infos, err := client.GitBranches(ctx, d.threadID)
		if err != nil || ctx.Err() != nil {
			return
		}
		names := make([]string, 0, len(infos))
		for _, b := range infos {
			if !b.Remote {
				names = append(names, b.Name)
			}
		}
		filtered := filterPRBaseBranches(names, head)
		if len(filtered) == 0 {
			return
		}
		d.mu.Lock()
		if !d.open || ctx.Err() == context.Canceled {
			d.mu.Unlock()
			return
		}
		d.branchCancel = nil
		d.branches = filtered
		if !d.basePicked {
			d.base = defaultPRBaseBranch(filtered)
		}
		d.mu.Unlock()
		d.s.invalidate()
	}()
}

func (d *gitPRCreateDialog) generate() {
	if !isGenerateCommitMessageEnabled(d.s) {
		return
	}
	d.mu.Lock()
	if !d.open || d.generating || d.pushing || d.creating {
		d.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	d.cancel, d.generating = cancel, true
	d.err = ""
	revision := d.revision
	d.mu.Unlock()
	go func() {
		defer cancel()
		message, err := d.s.client.GeneratePRDescription(ctx, d.threadID)
		if err == nil {
			err = ctx.Err()
		}
		d.mu.Lock()
		if !d.open || ctx.Err() == context.Canceled {
			d.mu.Unlock()
			return
		}
		d.cancel, d.generating = nil, false
		if err != nil {
			d.err = "AI 生成失败: " + err.Error()
		} else if d.revision == revision {
			title, body, _ := strings.Cut(strings.TrimSpace(message), "\n")
			d.title, d.body = strings.TrimSpace(title), strings.TrimSpace(body)
		}
		d.mu.Unlock()
		d.s.invalidate()
	}()
}

func (d *gitPRCreateDialog) submit(c *ui.Context) {
	d.mu.Lock()
	if !d.open || d.generating || d.pushing || d.creating || strings.TrimSpace(d.title) == "" {
		d.mu.Unlock()
		return
	}
	title := strings.TrimSpace(d.title)
	body := d.body
	base := strings.TrimSpace(d.base)
	state := d.state
	needsPush := !state.HasUpstream || state.Ahead > 0
	prClient, hasPRCreate := d.s.client.(gitPRCreateClient)
	if !hasPRCreate {
		target := gitPullRequestFormURL(normalizeGitRemoteURL(state.RemoteURL), state.Branch, d.title, d.body)
		if target == "" {
			d.err = "没有识别到可用的远端仓库地址"
			d.mu.Unlock()
			d.s.invalidate()
			return
		}
		if !needsPush {
			d.mu.Unlock()
			c.OpenURL(target)
			d.close()
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		d.cancel, d.pushing, d.err = cancel, true, ""
		d.mu.Unlock()
		d.s.invalidate()
		go func() {
			defer cancel()
			err := d.s.client.GitPush(ctx, d.threadID)
			d.mu.Lock()
			if !d.open || ctx.Err() == context.Canceled {
				d.mu.Unlock()
				return
			}
			d.cancel, d.pushing = nil, false
			if err != nil {
				d.err = git.ClassifyError(err).Title
			} else if ctx.Err() != nil {
				d.err = git.ClassifyError(ctx.Err()).Title
			} else {
				d.readyURL = target
			}
			d.mu.Unlock()
			d.s.invalidate()
		}()
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	d.cancel = cancel
	d.err = ""
	d.baseMenuOpen = false
	if needsPush {
		d.pushing = true
	} else {
		d.creating = true
	}
	d.mu.Unlock()
	d.s.invalidate()

	go func() {
		defer cancel()
		if needsPush {
			err := d.s.client.GitPush(ctx, d.threadID)
			if err == nil {
				err = ctx.Err()
			}
			if err != nil {
				d.mu.Lock()
				if !d.open || ctx.Err() == context.Canceled {
					d.mu.Unlock()
					return
				}
				d.cancel, d.pushing, d.creating = nil, false, false
				d.err = git.ClassifyError(err).Title
				d.mu.Unlock()
				d.s.invalidate()
				return
			}
			d.mu.Lock()
			if !d.open || ctx.Err() == context.Canceled {
				d.mu.Unlock()
				return
			}
			d.pushing = false
			d.creating = true
			d.state.HasUpstream = true
			d.state.Ahead = 0
			d.mu.Unlock()
			d.s.invalidate()
		}

		prURL, err := prClient.GitPRCreate(ctx, d.threadID, title, body, base)
		if err == nil {
			err = ctx.Err()
		}
		d.mu.Lock()
		if !d.open || ctx.Err() == context.Canceled {
			d.mu.Unlock()
			return
		}
		d.cancel, d.pushing, d.creating = nil, false, false
		if err != nil {
			if errors.Is(err, git.ErrForgeUnsupported) {
				target := gitPullRequestFormURL(normalizeGitRemoteURL(state.RemoteURL), state.Branch, d.title, d.body)
				if target == "" {
					d.err = "没有识别到可用的远端仓库地址"
				} else {
					d.readyURL = target
				}
			} else {
				d.err = err.Error()
			}
			d.mu.Unlock()
			d.s.invalidate()
			return
		}

		d.createdURL = strings.TrimSpace(prURL)
		d.createdNumber = parsePRNumberFromURL(d.createdURL)
		d.mu.Unlock()
		d.s.loadGitState(d.threadID)
		d.s.invalidate()
	}()
}

func renderGitPRCreateDialog(c *ui.Context, d *gitPRCreateDialog) {
	if d == nil {
		return
	}
	d.mu.Lock()
	open := d.open
	state := d.state
	title, body := d.title, d.body
	generating, pushing, creating := d.generating, d.pushing, d.creating
	errText, readyURL := d.err, d.readyURL
	createdURL, createdNumber := d.createdURL, d.createdNumber
	base := d.base
	branches := append([]string(nil), d.branches...)
	d.readyURL = ""
	d.mu.Unlock()
	if !open {
		return
	}
	if readyURL != "" {
		c.OpenURL(readyURL)
		d.close()
		return
	}

	monoFont := "monospace"
	if st := getChatSettings(d.s); st != nil {
		st.mu.Lock()
		monoFont = codeFontFamily(st.cfg.Appearance)
		st.mu.Unlock()
	}

	winW, _ := c.Size()
	width := min(gitPRCreateWidth, max(0, winW-modalDragPanelInset))
	modalDragModal(c, modalDragGitPRCreate, &open, func() {
		ui.Column(c.Key("git-pr-create-card")).Width(width).Padding(Space5).Gap(Space4).Radius(RadiusLG).Background(ThemeSurface(c)).Border(Hairline, ThemeBorder(c)).Transition(motionDialog(c)).Children(func() {
			if createdURL != "" || createdNumber > 0 {
				statusText := "已创建"
				if createdNumber > 0 {
					statusText = fmt.Sprintf("#%d 已创建", createdNumber)
				}
				modalDragHandle(c, modalDragGitPRCreate, ui.Column(c.Key("git-pr-create-success")).
					FillWidth().
					MinHeight(200).
					Center().
					Gap(Space4).
					Transition(motionFeedback(c)).
					Children(func() {
						ui.Box(c.Key("git-pr-create-success-badge")).
							Size(44, 44).
							Radius(RadiusPill).
							Center().
							Background(ThemeSuccess(c).Alpha(0.14)).
							Children(func() {
								ui.Icon(c, IconCheck()).Size(IconLG, IconLG).TextColor(ThemeSuccess(c))
							})
						ui.Text(c, statusText).
							FontSize(uiFont(TypeTitle)).
							TextColor(ThemePrimaryText(c))
						ui.Row(c.Key("git-pr-create-success-actions")).
							Center().
							Gap(Space3).
							Children(func() {
								Button(c, ButtonOpts{
									Key:     "git-pr-create-success-close",
									Text:    "关闭",
									Variant: ButtonSecondary,
									OnClick: d.close,
								})
								Button(c, ButtonOpts{
									Key:     "git-pr-create-success-view",
									Text:    "查看",
									Variant: ButtonPrimary,
									OnClick: func() {
										if createdNumber > 0 {
											s := d.s
											d.close()
											s.openGitPRDialog()
											s.selectGitPR(createdNumber)
											return
										}
										if createdURL != "" {
											c.OpenURL(createdURL)
										}
										d.close()
									},
								})
							})
					}))
				return
			}

			busy := pushing || creating
			ui.Column(c.Key("git-pr-create-form")).FillWidth().Gap(Space4).Transition(motionFeedback(c)).Children(func() {
				headDisplay := state.Branch
				if headDisplay == "" {
					headDisplay = state.Head
				}
				if headDisplay == "" {
					headDisplay = "HEAD"
				}
				baseDisplay := base
				if baseDisplay == "" {
					baseDisplay = "选择分支"
				}

				modalDragHandle(c, modalDragGitPRCreate, ui.Row(c.Key("git-pr-create-header")).
					FillWidth().
					AlignItems(ui.Center).
					Gap(Space2).
					Children(func() {
						ui.Text(c, headDisplay).
							Font(monoFont).
							FontSize(uiFont(TypeCode)).
							TextColor(ThemeSecondaryText(c)).
							SingleLine().
							Ellipsis("…").
							Shrink(1)
						ui.Text(c, "→").
							FontSize(uiFont(TypeFootnote)).
							TextColor(ThemeSecondaryText(c)).
							Shrink(0)
						baseBtn := ui.ButtonBase(c.Key("git-pr-create-base-btn")).
							Label("目标分支").
							Height(ControlSM).
							Padding(0, Space2).
							Gap(Space1).
							Radius(RadiusSM).
							AlignItems(ui.Center).
							Shrink(0).
							Border(Hairline, ThemeBorder(c)).
							Background(ThemeOverlay(c, OverlaySubtle)).
							Disabled(busy).
							Transition(motionFeedback(c)).
							OnClick(func() {
								togglePopoverOpen(&d.baseMenuOpen)
								d.s.invalidate()
							})
						if !busy && baseBtn.Hovered() {
							baseBtn.Background(ThemeHover(c))
						}
						if !busy {
							focusRing(c, baseBtn)
						}
						baseBtn.Children(func() {
							ui.Text(c, baseDisplay).
								Font(monoFont).
								FontSize(uiFont(TypeCode)).
								TextColor(ThemePrimaryText(c)).
								SingleLine().
								Ellipsis("…")
							ui.Icon(c, IconChevronDown()).
								Size(IconSM, IconSM).
								TextColor(ThemeSecondaryText(c))
						})
						appPopover(c.Key("git-pr-create-base-popover"), baseBtn, &d.baseMenuOpen, func() {
							d.s.invalidate()
						}, func() {
							popoverCard(c, ui.Column(c.Key("git-pr-create-base-menu"))).
								Width(220).
								Padding(Space1).
								Gap(Space0).
								FocusGroup(ui.Vertical).
								Children(func() {
									if len(branches) == 0 {
										ui.Text(c, "没有可选分支").
											FontSize(uiFont(TypeCaption)).
											TextColor(ThemeTertiaryText(c)).
											Padding(Space2, Space3)
										return
									}
									ui.Scroll(c.Key("git-pr-create-base-scroll")).FillWidth().MaxHeight(240).Children(func() {
										ui.Column(c.Key("git-pr-create-base-list")).FillWidth().Children(func() {
											for _, branch := range branches {
												branch := branch
												menuRow(c, menuRowProps{
													Key:     "git-pr-create-base-opt-" + branch,
													Title:   branch,
													Checked: branch == base,
													OnClick: func() {
														d.mu.Lock()
														d.base = branch
														d.basePicked = true
														d.baseMenuOpen = false
														d.mu.Unlock()
														d.s.invalidate()
													},
												})
											}
										})
									})
								})
						})
					}))

				TextField(c, TextFieldProps{
					Key:         "git-pr-create-title",
					Value:       &title,
					Placeholder: "标题",
					FillWidth:   true,
					AutoFocus:   true,
					Disabled:    busy,
					OnChange: func(value string) {
						d.mu.Lock()
						d.title = value
						d.revision++
						d.mu.Unlock()
					},
				})

				ui.Column(c.Key("git-pr-create-body-wrap")).FillWidth().Children(func() {
					const bodyRightPadding = ControlSM + Space3
					area := ui.TextArea(c.Key("git-pr-create-body"), &body).
						Placeholder("正文").
						Lines(4, 10).
						FontSize(uiFont(TypeBody)).
						TextColor(ThemePrimaryText(c)).
						Background(ThemeHover(c)).
						Border(Hairline, ThemeBorder(c)).
						Radius(RadiusSM).
						Padding(Space2, bodyRightPadding, Space2, Space3).
						FillWidth().
						Disabled(busy)
					area.OnChange(func() {
						d.mu.Lock()
						d.body = body
						d.revision++
						d.mu.Unlock()
					})
					IconButton(c, IconButtonProps{
						Key:      "git-pr-create-ai",
						Icon:     IconSparkles(),
						Label:    "AI 生成",
						Tooltip:  "AI 生成",
						Variant:  ButtonGhost,
						Size:     ControlSM,
						Loading:  generating,
						Disabled: busy || !isGenerateCommitMessageEnabled(d.s),
						OnClick:  d.generate,
					}).Absolute().Top(Space2).Right(Space2)
				})

				if errText != "" {
					ui.Text(c, errText).
						FontSize(uiFont(TypeCaption)).
						TextColor(ThemeDanger(c)).
						FillWidth().
						MaxLines(3)
				}

				submitText := "创建"
				if !state.HasUpstream || state.Ahead > 0 {
					submitText = "推送并创建"
				}
				tip := ""
				if generating {
					tip = "正在生成 PR 内容"
				}
				FormActions(c, "git-pr-create-actions", func() {
					Button(c, ButtonOpts{
						Key:     "git-pr-create-cancel",
						Text:    "取消",
						Variant: ButtonSecondary,
						OnClick: d.close,
					})
					Button(c, ButtonOpts{
						Key:      "git-pr-create-submit",
						Text:     submitText,
						Variant:  ButtonPrimary,
						Disabled: strings.TrimSpace(title) == "" || generating || busy,
						Loading:  busy,
						Tooltip:  tip,
						OnClick:  func() { d.submit(c) },
					})
				})
			})
		})
	})
	if !open {
		d.close()
	}
}

func filterPRBaseBranches(candidates []string, head string) []string {
	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, len(candidates))
	for _, b := range candidates {
		b = strings.TrimSpace(b)
		if b == "" || b == head {
			continue
		}
		if _, ok := seen[b]; ok {
			continue
		}
		seen[b] = struct{}{}
		out = append(out, b)
	}
	return out
}

func defaultPRBaseBranch(branches []string) string {
	hasMaster := false
	for _, b := range branches {
		if b == "main" {
			return "main"
		}
		if b == "master" {
			hasMaster = true
		}
	}
	if hasMaster {
		return "master"
	}
	if len(branches) > 0 {
		return branches[0]
	}
	return ""
}

func parsePRNumberFromURL(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	path := raw
	if u, err := url.Parse(raw); err == nil && u.Path != "" {
		path = u.Path
	} else if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	path = strings.TrimRight(path, "/")
	if path == "" {
		return 0
	}
	idx := strings.LastIndex(path, "/")
	seg := path[idx+1:]
	if seg == "" {
		return 0
	}
	n, err := strconv.Atoi(seg)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// GitPullRequestCreateURL 按桌面端「创建合并请求」的规则，把远端地址与分支名拼成
// 创建 PR/MR 的网页地址；remoteURL 可以是 ssh / https / SCP 等任意 git remote 形式。
// 供远程协议（git.pr.createUrl）复用，避免另一处再实现一套规则；
// 地址为空或识别不到托管平台时返回空串。
func GitPullRequestCreateURL(remoteURL, branch string) string {
	return gitPullRequestFormURL(normalizeGitRemoteURL(remoteURL), branch, "", "")
}

// gitPullRequestFormURL 在创建合并请求的地址上追加标题与正文，让浏览器预填两者的表单。
func gitPullRequestFormURL(repoURL, branch, title, body string) string {
	base := gitPullRequestURL(repoURL, branch)
	if base == "" {
		return ""
	}
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	if title == "" && body == "" {
		return base
	}

	values := url.Values{}
	if strings.Contains(strings.ToLower(repoURL), "gitlab") {
		if title != "" {
			values.Set("merge_request[title]", title)
		}
		if body != "" {
			values.Set("merge_request[description]", body)
		}
	} else {
		if title != "" {
			values.Set("title", title)
		}
		if body != "" {
			values.Set("body", body)
		}
	}

	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + values.Encode()
}
