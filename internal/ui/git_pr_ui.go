package mcodeui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

// PR 只读查看弹窗：左列是当前仓库开放的 PR/MR 列表，右侧是选中项的详情。
// 只做浏览，不做评论、评审与合并；「在浏览器打开」留给用户去网页上处理。

// modalDragGitPR 是「Pull Request 列表」弹窗的拖拽 key。
const modalDragGitPR = "modal-drag-git-pr"

const (
	// gitPRListLimit 是一次读取的 PR/MR 条数上限。
	gitPRListLimit = 30
	// gitPRDialogWidth 是弹窗卡片的宽度；窗口更窄时按窗口宽度收缩。
	gitPRDialogWidth float32 = 860
	// gitPRListPaneWidth 是左侧列表列宽度。
	gitPRListPaneWidth float32 = 250
)

// gitPRClient 是 PR 只读弹窗需要的后端能力，由 app.Client 实现。
// 这里用小接口加类型断言接入，不往 ui/client.go 的大接口上加方法。
type gitPRClient interface {
	// GitPRList 列出会话所在仓库开放的 PR/MR。
	GitPRList(ctx context.Context, threadID string, limit int) ([]git.PRSummary, error)
	// GitPRDetail 读取单条 PR/MR 的详情。
	GitPRDetail(ctx context.Context, threadID string, number int) (git.PRDetail, error)
}

// gitPRDialogState 是 PR 弹窗的状态，按 chatState 附加（与 chatFindStates 的做法一致）。
type gitPRDialogState struct {
	mu sync.Mutex

	open     bool
	threadID string // 列表所属会话，切换会话后弹窗作废

	listLoading bool
	listErr     string
	prs         []git.PRSummary

	selected      int // 选中项编号，0 表示还没选
	detailLoading bool
	detailErr     string
	detail        *git.PRDetail

	listSeq   uint64 // 递增序号，丢弃过期的异步结果
	detailSeq uint64
}

var (
	gitPRDialogMu     sync.Mutex
	gitPRDialogStates = make(map[*chatState]*gitPRDialogState)
)

// getGitPRDialogState 取得或初始化指定会话的 PR 弹窗状态。
func getGitPRDialogState(s *chatState) *gitPRDialogState {
	gitPRDialogMu.Lock()
	defer gitPRDialogMu.Unlock()
	st, ok := gitPRDialogStates[s]
	if !ok {
		st = &gitPRDialogState{}
		gitPRDialogStates[s] = st
	}
	return st
}

// gitPRThreadID 返回当前选中的会话 ID。
func (s *chatState) gitPRThreadID() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selectedThreadID
}

// prClient 从当前会话客户端里取出 PR 读取能力，客户端不支持时返回 false。
func (s *chatState) prClient() (gitPRClient, bool) {
	if s == nil || s.client == nil {
		return nil, false
	}
	client, ok := s.client.(gitPRClient)
	return client, ok
}

// openGitPRDialog 打开弹窗并异步读取开放 PR 列表。
func (s *chatState) openGitPRDialog() {
	if s == nil {
		return
	}
	st := getGitPRDialogState(s)
	threadID := s.gitPRThreadID()
	st.mu.Lock()
	st.open = true
	st.threadID = threadID
	st.prs = nil
	st.selected = 0
	st.detail = nil
	st.detailErr = ""
	st.detailLoading = false
	st.listErr = ""
	st.listSeq++
	st.detailSeq++
	st.mu.Unlock()
	s.invalidate()
	s.fetchGitPRList()
}

// closeGitPRDialog 关闭弹窗，并丢弃在途请求的结果。
func (s *chatState) closeGitPRDialog() {
	if s == nil {
		return
	}
	st := getGitPRDialogState(s)
	st.mu.Lock()
	st.open = false
	st.listLoading = false
	st.detailLoading = false
	st.listErr = ""
	st.detailErr = ""
	st.selected = 0
	st.detail = nil
	st.listSeq++
	st.detailSeq++
	st.mu.Unlock()
	modalDragReset(modalDragGitPR)
	s.invalidate()
}

// refreshGitPRList 重新读取列表；弹窗没打开时什么也不做。
func (s *chatState) refreshGitPRList() {
	st := getGitPRDialogState(s)
	st.mu.Lock()
	open := st.open
	st.mu.Unlock()
	if !open {
		return
	}
	s.fetchGitPRList()
}

// fetchGitPRList 在后台读取开放 PR 列表；只保留最后一次请求的结果。
func (s *chatState) fetchGitPRList() {
	st := getGitPRDialogState(s)
	client, ok := s.prClient()
	if !ok {
		st.mu.Lock()
		st.listLoading = false
		st.listErr = "当前客户端不支持读取 PR 列表"
		st.mu.Unlock()
		s.invalidate()
		return
	}

	threadID := s.gitPRThreadID()
	st.mu.Lock()
	st.listLoading = true
	st.listSeq++
	seq := st.listSeq
	st.mu.Unlock()
	s.invalidate()

	go func() {
		prs, err := client.GitPRList(context.Background(), threadID, gitPRListLimit)
		st.mu.Lock()
		if st.listSeq != seq {
			// 弹窗已关闭或有更新的请求：丢弃这次结果。
			st.mu.Unlock()
			return
		}
		st.listLoading = false
		if err != nil {
			st.listErr = "读取 PR 列表失败: " + err.Error()
		} else {
			st.prs = prs
			st.listErr = ""
			// 刷新后选中项已不在列表里，清掉右侧详情。
			found := false
			for _, pr := range prs {
				if pr.Number == st.selected {
					found = true
					break
				}
			}
			if !found {
				st.selected = 0
				st.detail = nil
				st.detailErr = ""
			}
		}
		st.mu.Unlock()
		s.invalidate()
	}()
}

// selectGitPR 选中一条 PR 并异步读取详情。
func (s *chatState) selectGitPR(number int) {
	if s == nil || number <= 0 {
		return
	}
	st := getGitPRDialogState(s)
	st.mu.Lock()
	if !st.open {
		st.mu.Unlock()
		return
	}
	st.selected = number
	st.detail = nil
	st.detailErr = ""
	st.detailLoading = true
	st.detailSeq++
	seq := st.detailSeq
	st.mu.Unlock()
	s.invalidate()
	s.fetchGitPRDetail(number, seq)
}

// fetchGitPRDetail 在后台读取详情；序号变化后旧结果直接丢弃。
func (s *chatState) fetchGitPRDetail(number int, seq uint64) {
	st := getGitPRDialogState(s)
	client, ok := s.prClient()
	if !ok {
		st.mu.Lock()
		if st.detailSeq == seq {
			st.detailLoading = false
			st.detailErr = "当前客户端不支持读取 PR 详情"
		}
		st.mu.Unlock()
		s.invalidate()
		return
	}

	threadID := s.gitPRThreadID()
	go func() {
		detail, err := client.GitPRDetail(context.Background(), threadID, number)
		st.mu.Lock()
		if st.detailSeq != seq {
			st.mu.Unlock()
			return
		}
		st.detailLoading = false
		if err != nil {
			st.detailErr = "读取 PR 详情失败: " + err.Error()
		} else {
			loaded := detail
			st.detail = &loaded
			st.detailErr = ""
		}
		st.mu.Unlock()
		s.invalidate()
	}()
}

// renderGitPRDialog 渲染 PR 只读弹窗，由会话头部每帧调用。
func renderGitPRDialog(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	st := getGitPRDialogState(s)
	st.mu.Lock()
	open := st.open
	threadID := st.threadID
	listLoading := st.listLoading
	listErr := st.listErr
	prs := append([]git.PRSummary(nil), st.prs...)
	selected := st.selected
	detailLoading := st.detailLoading
	detailErr := st.detailErr
	var detail *git.PRDetail
	if st.detail != nil {
		loaded := *st.detail
		detail = &loaded
	}
	st.mu.Unlock()

	if !open {
		modalDragReset(modalDragGitPR)
		return
	}
	// 切换会话后旧列表没有意义，直接关掉。
	if threadID != "" && threadID != s.gitPRThreadID() {
		s.closeGitPRDialog()
		return
	}

	winW, winH := c.Size()
	cardW, cardH := modalResizeSize(modalDragGitPR, gitPRDialogWidth, max(400, winH-120), 360, 240, winW, winH)
	contentH := max(0, cardH-180)

	modalDragModal(c, modalDragGitPR, &open, func() {
		if c.Shortcut(0, ui.KeyEscape) {
			s.closeGitPRDialog()
			return
		}

		ui.Column(c.Key("modal-git-pr-card")).
			Width(cardW).
			Height(cardH).
			Background(ThemeSurface(c)).
			BorderColor(ThemeBorder(c)).
			BorderWidth(Hairline).
			Radius(RadiusLG).
			Padding(Space5).
			Gap(Space4).
			Children(func() {
				// 标题行同时是弹窗的拖拽把手。
				modalDragHandle(c, modalDragGitPR, ui.Row(c.Key("modal-git-pr-title-row")).
					FillWidth().
					AlignItems(ui.Center).
					Gap(Space3).
					Children(func() {
						ui.Text(c, "Pull Request").
							FontSize(TypeTitle).
							FontWeight(600).
							TextColor(ThemePrimaryText(c)).
							SingleLine()
						ui.Text(c, "只读查看").
							FontSize(TypeCaption).
							TextColor(ThemeSecondaryText(c)).
							SingleLine()
						ui.Spacer(c)
						if listLoading || detailLoading {
							ui.Spinner(c).Size(IconSM, IconSM).TextColor(ThemeAccent(c))
						}
					}))

				ui.Row(c.Key("modal-git-pr-body")).
					FillWidth().
					Gap(Space4).
					AlignItems(ui.Start).
					Children(func() {
						renderGitPRListPane(c, s, prs, selected, listLoading, listErr, contentH)
						renderGitPRDetailPane(c, s, detail, selected, detailLoading, detailErr, contentH)
					})

				ui.Row(c.Key("modal-git-pr-actions")).
					FillWidth().
					Gap(Space3).
					AlignItems(ui.Center).
					Justify(ui.End).
					Children(func() {
						ui.Text(c, "Esc 关闭").
							FontSize(TypeCaption).
							TextColor(ThemeSecondaryText(c))
						url := ""
						if detail != nil {
							url = detail.URL
						}
						if url == "" && selected > 0 {
							for _, pr := range prs {
								if pr.Number == selected {
									url = pr.URL
									break
								}
							}
						}
						Button(c, ButtonOpts{
							Key:      "modal-git-pr-open-browser",
							Text:     "在浏览器打开",
							Variant:  ButtonSecondary,
							Disabled: url == "",
							OnClick: func() {
								if url != "" {
									c.OpenURL(url)
								}
							},
						})
						Button(c, ButtonOpts{
							Key:     "modal-git-pr-close",
							Text:    "关闭",
							Variant: ButtonPrimary,
							OnClick: func() {
								s.closeGitPRDialog()
							},
						})
					})
				modalResizeGrips(c, modalDragGitPR, cardW, cardH)
			})
	})
	// DialogBase 已经把遮罩点击与 Esc 写回 open，这里据此关闭。
	if !open {
		s.closeGitPRDialog()
	}
}

// renderGitPRListPane 渲染左列：标题与刷新按钮、加载/出错/空列表提示、PR 列表。
func renderGitPRListPane(c *ui.Context, s *chatState, prs []git.PRSummary, selected int, loading bool, errText string, contentH float32) {
	ui.Column(c.Key("modal-git-pr-list-pane")).
		Width(gitPRListPaneWidth).
		Gap(Space2).
		Children(func() {
			ui.Row(c.Key("modal-git-pr-list-head")).
				FillWidth().
				AlignItems(ui.Center).
				Gap(Space2).
				Children(func() {
					ui.Text(c, "开放的 PR/MR").
						FontSize(TypeCaption).
						FontWeight(600).
						TextColor(ThemeSecondaryText(c)).
						SingleLine()
					ui.Spacer(c)
					Button(c, ButtonOpts{
						Key:      "modal-git-pr-refresh",
						Text:     "刷新",
						Variant:  ButtonSecondary,
						Disabled: loading,
						OnClick: func() {
							s.refreshGitPRList()
						},
					})
				})

			switch {
			case loading && len(prs) == 0:
				ui.Row(c.Key("modal-git-pr-list-loading")).
					Gap(Space2).
					AlignItems(ui.Center).
					Children(func() {
						ui.Spinner(c).Size(IconSM, IconSM).TextColor(ThemeAccent(c))
						ui.Text(c, "正在读取 PR 列表…").
							FontSize(TypeFootnote).
							TextColor(ThemeSecondaryText(c))
					})
			case errText != "" && len(prs) == 0:
				ui.Text(c, errText).
					FontSize(TypeFootnote).
					TextColor(ThemeDanger(c)).
					MaxLines(4)
			case len(prs) == 0:
				ui.Text(c, "这个仓库没有开放的 PR/MR").
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c))
			default:
				ui.Scroll(c.Key("modal-git-pr-list-scroll")).
					FillWidth().
					MaxHeight(contentH).
					Children(func() {
						ui.Column(c.Key("modal-git-pr-list")).
							FillWidth().
							Gap(Space1).
							Children(func() {
								for _, pr := range prs {
									renderGitPRListRow(c, s, pr, pr.Number == selected)
								}
							})
					})
			}

			// 列表已有内容时，错误只作为底部提示，不顶掉列表。
			if errText != "" && len(prs) > 0 {
				ui.Text(c, errText).
					FontSize(TypeCaption).
					TextColor(ThemeDanger(c)).
					MaxLines(3)
			}
		})
}

// renderGitPRListRow 渲染列表中的一条：#编号与标题在上，状态/检查标签与作者在下。
func renderGitPRListRow(c *ui.Context, s *chatState, pr git.PRSummary, active bool) {
	key := fmt.Sprintf("modal-git-pr-row-%d", pr.Number)
	row := ui.ButtonBase(c.Key(key)).
		Label(fmt.Sprintf("查看 PR #%d %s", pr.Number, pr.Title)).
		Padding(Space2, Space3).
		Radius(RadiusMD).
		FillWidth().
		Justify(ui.Start).
		AlignItems(ui.Start).
		OnClick(func() {
			s.selectGitPR(pr.Number)
		})
	if active {
		row.Background(ThemeSelected(c))
	} else if row.Hovered() {
		row.Background(ThemeHover(c).Alpha(0.6))
	}
	row.Children(func() {
		ui.Column(c.Key(key + "-text")).
			FillWidth().
			Gap(Space0).
			Children(func() {
				ui.Row(c.Key(key + "-title-row")).
					FillWidth().
					Gap(Space2).
					AlignItems(ui.Center).
					Children(func() {
						ui.Text(c, fmt.Sprintf("#%d", pr.Number)).
							FontSize(TypeCaption).
							TextColor(ThemeSecondaryText(c)).
							SingleLine()
						ui.Text(c, pr.Title).
							FontSize(TypeFootnote).
							TextColor(ThemePrimaryText(c)).
							Grow(1).
							SingleLine()
					})
				ui.Row(c.Key(key + "-meta")).
					FillWidth().
					Gap(Space1).
					AlignItems(ui.Center).
					Children(func() {
						renderGitPRTag(c, key+"-tag-state", gitPRStateLabel(pr.State, pr.IsDraft))
						renderGitPRTag(c, key+"-tag-checks", gitPRChecksLabel(pr.ChecksState))
						if author := strings.TrimSpace(pr.Author); author != "" {
							ui.Text(c, author).
								FontSize(TypeCaption).
								TextColor(ThemeSecondaryText(c)).
								SingleLine()
						}
					})
			})
	})
}

// renderGitPRTag 渲染一个小号状态标签。
func renderGitPRTag(c *ui.Context, key, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	Badge(c, key, text, ToneNeutral)
}

// renderGitPRDetailPane 渲染右列：未选中/加载/出错提示，或选中项的详情。
func renderGitPRDetailPane(c *ui.Context, s *chatState, detail *git.PRDetail, selected int, loading bool, errText string, contentH float32) {
	ui.Column(c.Key("modal-git-pr-detail-pane")).
		Grow(1).
		Gap(Space3).
		Children(func() {
			switch {
			case selected == 0:
				ui.Text(c, "从左侧选择一条 PR 查看详情").
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c))
			case loading && detail == nil:
				ui.Row(c.Key("modal-git-pr-detail-loading")).
					Gap(Space2).
					AlignItems(ui.Center).
					Children(func() {
						ui.Spinner(c).Size(IconSM, IconSM).TextColor(ThemeAccent(c))
						ui.Text(c, "正在读取 PR 详情…").
							FontSize(TypeFootnote).
							TextColor(ThemeSecondaryText(c))
					})
			case errText != "" && detail == nil:
				ui.Text(c, errText).
					FontSize(TypeFootnote).
					TextColor(ThemeDanger(c)).
					MaxLines(4)
			case detail == nil:
				ui.Text(c, "没有可显示的详情").
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c))
			default:
				renderGitPRDetail(c, *detail, contentH)
			}

			if errText != "" && detail != nil {
				ui.Text(c, errText).
					FontSize(TypeCaption).
					TextColor(ThemeDanger(c)).
					MaxLines(3)
			}
		})
}

// renderGitPRDetail 渲染详情：标题、元信息、状态标签、改动统计、检查、正文与评论。
func renderGitPRDetail(c *ui.Context, detail git.PRDetail, contentH float32) {
	ui.Scroll(c.Key("modal-git-pr-detail-scroll")).
		FillWidth().
		MaxHeight(contentH).
		Children(func() {
			ui.Column(c.Key("modal-git-pr-detail")).
				FillWidth().
				Gap(Space3).
				Children(func() {
					ui.Text(c, fmt.Sprintf("#%d %s", detail.Number, detail.Title)).
						FontSize(TypeProse).
						FontWeight(600).
						TextColor(ThemePrimaryText(c))

					if meta := gitPRMetaLine(detail.PRSummary); meta != "" {
						ui.Text(c, meta).
							FontSize(TypeCaption).
							TextColor(ThemeSecondaryText(c))
					}

					ui.Row(c.Key("modal-git-pr-detail-tags")).
						FillWidth().
						Gap(Space1).
						AlignItems(ui.Center).
						Children(func() {
							renderGitPRTag(c, "modal-git-pr-detail-tag-state", gitPRStateLabel(detail.State, detail.IsDraft))
							renderGitPRTag(c, "modal-git-pr-detail-tag-review", gitPRReviewLabel(detail.ReviewDecision))
							renderGitPRTag(c, "modal-git-pr-detail-tag-checks", gitPRChecksLabel(detail.ChecksState))
							ui.Text(c, gitPRChangeStats(detail.Additions, detail.Deletions, detail.ChangedFiles)).
								FontSize(TypeCaption).
								TextColor(ThemeSecondaryText(c)).
								SingleLine()
						})

					if len(detail.Checks) > 0 {
						renderGitPRSectionTitle(c, "CI 检查")
						ui.Column(c.Key("modal-git-pr-detail-checks")).
							FillWidth().
							Gap(Space0).
							Children(func() {
								for i, check := range detail.Checks {
									renderGitPRCheckRow(c, fmt.Sprintf("modal-git-pr-detail-check-%d", i), check)
								}
							})
					}

					renderGitPRSectionTitle(c, "正文")
					if detail.Body == "" {
						ui.Text(c, "没有填写正文").
							FontSize(TypeFootnote).
							TextColor(ThemeSecondaryText(c))
					} else {
						ui.Text(c, detail.Body).
							FontSize(TypeFootnote).
							TextColor(ThemePrimaryText(c))
					}

					renderGitPRSectionTitle(c, fmt.Sprintf("评论（%d）", len(detail.Comments)))
					if len(detail.Comments) == 0 {
						ui.Text(c, "还没有评论").
							FontSize(TypeFootnote).
							TextColor(ThemeSecondaryText(c))
						return
					}
					ui.Column(c.Key("modal-git-pr-detail-comments")).
						FillWidth().
						Gap(Space2).
						Children(func() {
							for i, comment := range detail.Comments {
								renderGitPRCommentRow(c, fmt.Sprintf("modal-git-pr-detail-comment-%d", i), comment)
							}
						})
				})
		})
}

// renderGitPRSectionTitle 渲染小节标题。
func renderGitPRSectionTitle(c *ui.Context, text string) {
	ui.Text(c, text).
		FontSize(TypeCaption).
		FontWeight(600).
		TextColor(ThemeSecondaryText(c)).
		SingleLine()
}

// renderGitPRCheckRow 渲染一条 CI 检查：左侧名称，右侧状态文案。
func renderGitPRCheckRow(c *ui.Context, key string, check git.PRCheck) {
	ui.Row(c.Key(key)).
		FillWidth().
		Padding(Space0, Space2).
		Radius(RadiusXS).
		Background(ThemeHover(c).Alpha(0.4)).
		Gap(Space3).
		AlignItems(ui.Center).
		Children(func() {
			name := strings.TrimSpace(check.Name)
			if name == "" {
				name = "未命名检查"
			}
			ui.Text(c, name).
				FontSize(TypeFootnote).
				TextColor(ThemePrimaryText(c)).
				Grow(1).
				SingleLine()
			ui.Text(c, gitPRChecksLabel(check.State)).
				FontSize(TypeCaption).
				TextColor(ThemeSecondaryText(c)).
				SingleLine()
		})
}

// renderGitPRCommentRow 渲染一条评论：作者与时间在上，正文在下。
func renderGitPRCommentRow(c *ui.Context, key string, comment git.PRComment) {
	ui.Column(c.Key(key)).
		FillWidth().
		Padding(Space2, Space3).
		Radius(RadiusSM).
		Background(ThemeHover(c).Alpha(0.4)).
		Gap(Space0).
		Children(func() {
			author := strings.TrimSpace(comment.Author)
			if author == "" {
				author = "匿名"
			}
			ui.Row(c.Key(key + "-head")).
				FillWidth().
				Gap(Space2).
				AlignItems(ui.Center).
				Children(func() {
					ui.Text(c, author).
						FontSize(TypeCaption).
						FontWeight(600).
						TextColor(ThemePrimaryText(c)).
						SingleLine()
					if rel := gitPRUpdatedLabel(comment.CreatedAt); rel != "" {
						ui.Text(c, rel).
							FontSize(TypeCaption).
							TextColor(ThemeSecondaryText(c)).
							SingleLine()
					}
				})
			ui.Text(c, comment.Body).
				FontSize(TypeFootnote).
				TextColor(ThemePrimaryText(c))
		})
}

// gitPRStateLabel 返回状态标签文案：草稿优先，其次按归一化状态显示中文。
func gitPRStateLabel(state string, draft bool) string {
	if draft {
		return "草稿"
	}
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "open":
		return "开放"
	case "closed":
		return "已关闭"
	case "merged":
		return "已合并"
	case "locked":
		return "已锁定"
	case "":
		return "未知状态"
	}
	return strings.TrimSpace(state)
}

// gitPRChecksLabel 返回 CI 汇总状态的文案。
func gitPRChecksLabel(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "success":
		return "检查通过"
	case "failure":
		return "检查失败"
	case "pending":
		return "检查进行中"
	case "none":
		return "无检查"
	}
	return "检查未知"
}

// gitPRReviewLabel 返回评审结论的文案。
func gitPRReviewLabel(decision string) string {
	switch strings.ToUpper(strings.TrimSpace(decision)) {
	case "APPROVED":
		return "评审通过"
	case "CHANGES_REQUESTED":
		return "评审要求修改"
	case "REVIEW_REQUIRED":
		return "等待评审"
	case "":
		return "暂无评审"
	}
	return "评审未定"
}

// gitPRChangeStats 拼出改动统计：「+120 · -8 · 5 个文件」。
func gitPRChangeStats(additions, deletions, changedFiles int) string {
	parts := []string{fmt.Sprintf("+%d", additions), fmt.Sprintf("-%d", deletions)}
	if changedFiles > 0 {
		parts = append(parts, fmt.Sprintf("%d 个文件", changedFiles))
	}
	return strings.Join(parts, " · ")
}

// gitPRUpdatedLabel 把时间格式化成中文短格式；零值返回空字符串。
func gitPRUpdatedLabel(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return formatRelativeTime(t.Local())
}

// gitPRMetaLine 拼出详情里的元信息行：作者、分支、更新时间。
func gitPRMetaLine(summary git.PRSummary) string {
	var parts []string
	if author := strings.TrimSpace(summary.Author); author != "" {
		parts = append(parts, "作者 "+author)
	}
	head := strings.TrimSpace(summary.HeadBranch)
	base := strings.TrimSpace(summary.BaseBranch)
	switch {
	case head != "" && base != "":
		parts = append(parts, head+" → "+base)
	case head != "":
		parts = append(parts, head)
	case base != "":
		parts = append(parts, "→ "+base)
	}
	if rel := gitPRUpdatedLabel(summary.UpdatedAt); rel != "" {
		parts = append(parts, "更新于 "+rel)
	}
	return strings.Join(parts, " · ")
}
