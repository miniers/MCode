package mcodeui

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

const (
	modalDragGitPRCreate         = "modal-drag-git-pr-create"
	gitPRCreateWidth     float32 = 480
)

// 输入和生成结果共用锁，关闭后取消生成并丢弃迟到结果。
type gitPRCreateDialog struct {
	mu               sync.Mutex
	s                *chatState
	threadID         string
	state            GitState
	open, generating bool
	title, body, err string
	revision         uint64
	cancel           context.CancelFunc
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
	d := &gitPRCreateDialog{s: s, threadID: threadID, state: state, open: true}
	ps.prCreate = d
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
	d.generating = false
	d.mu.Unlock()
	modalDragReset(modalDragGitPRCreate)
	d.s.invalidate()
}

func (d *gitPRCreateDialog) generate() {
	if !isGenerateCommitMessageEnabled(d.s) {
		return
	}
	d.mu.Lock()
	if !d.open || d.generating {
		d.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	d.cancel, d.generating = cancel, true
	revision := d.revision
	d.mu.Unlock()
	go func() {
		defer cancel()
		message, err := d.s.client.GenerateCommitMessage(ctx, d.threadID)
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

func renderGitPRCreateDialog(c *ui.Context, d *gitPRCreateDialog) {
	if d == nil {
		return
	}
	d.mu.Lock()
	open, title, body, generating, errText := d.open, d.title, d.body, d.generating, d.err
	d.mu.Unlock()
	if !open {
		return
	}
	winW, _ := c.Size()
	width := min(gitPRCreateWidth, max(0, winW-modalDragPanelInset))
	modalDragModal(c, modalDragGitPRCreate, &open, func() {
		ui.Column(c.Key("git-pr-create-card")).Width(width).Padding(Space5).Gap(Space4).Radius(RadiusLG).Background(ThemeSurface(c)).Border(Hairline, ThemeBorder(c)).Transition(motionDialog(c)).Children(func() {
			modalDragHandle(c, modalDragGitPRCreate, ui.Row(c.Key("git-pr-create-header")).FillWidth().AlignItems(ui.Center).Children(func() {
				ui.Text(c, "创建 PR").FontSize(uiFont(TypeTitle))
				ui.Spacer(c)
				if generating {
					LoadingIndicator(c, "git-pr-create-loading", "", IconSM)
				}
			}))
			TextField(c, TextFieldProps{Key: "git-pr-create-title", Value: &title, Label: "标题", Placeholder: "标题", FillWidth: true, AutoFocus: true, OnChange: func(value string) {
				d.mu.Lock()
				d.title = value
				d.revision++
				d.mu.Unlock()
			}})
			area := ui.TextArea(c.Key("git-pr-create-body"), &body).Label("正文").Placeholder("正文").Lines(4, 10).FontSize(uiFont(TypeBody)).TextColor(ThemePrimaryText(c)).Background(ThemeHover(c)).Border(Hairline, ThemeBorder(c)).Radius(RadiusSM).Padding(Space2, Space3).FillWidth()
			area.OnChange(func() { d.mu.Lock(); d.body = body; d.revision++; d.mu.Unlock() })
			if errText != "" {
				renderGitError(c, "git-pr-create-error", errText)
			}
			FormActions(c, "git-pr-create-actions", func() {
				Button(c, ButtonOpts{Key: "git-pr-create-cancel", Text: "取消", Variant: ButtonSecondary, OnClick: d.close})
				tip := ""
				if generating {
					tip = "正在生成 PR 内容"
				}
				Button(c, ButtonOpts{Key: "git-pr-create-submit", Text: "创建", Variant: ButtonPrimary, Disabled: generating, Tooltip: tip, OnClick: func() {
					d.mu.Lock()
					target := gitPullRequestFormURL(normalizeGitRemoteURL(d.state.RemoteURL), d.state.Branch, d.title, d.body)
					if target == "" {
						d.err = "没有识别到可用的远端仓库地址"
					}
					d.mu.Unlock()
					if target == "" {
						d.s.invalidate()
						return
					}
					c.OpenURL(target)
					d.close()
				}})
			})
		})
	})
	if !open {
		d.close()
	}
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
