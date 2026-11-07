package mcodeui

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown/parser"
	"github.com/egoist/mygo/ui/richtext"
)

// messageContextMenu 返回时间线消息的右键菜单构建函数，由 timeline_rows.go 挂载到消息元素上。
func messageContextMenu(c *ui.Context, s *chatState, m Message) func(*ui.Menu) {
	return func(menu *ui.Menu) {
		if menu.Item("复制").Chosen() {
			c.WriteClipboard(markdownToPlain(m.Text))
			s.setToast("已复制")
		}
		if menu.Item("复制 Markdown").Chosen() {
			c.WriteClipboard(m.Text)
			s.setToast("已复制")
		}
		// 仅助手消息提供「复制最后一个代码块」，且只在正文确实含围栏代码块时出现
		if m.Role == "assistant" {
			if code, ok := lastCodeBlock(m.Text); ok {
				if menu.Item("复制最后一个代码块").Chosen() {
					c.WriteClipboard(code)
					s.setToast("已复制")
				}
			}
		}
		if menu.Item("引用到回复").Chosen() {
			s.quoteIntoDraft(m.Text)
		}
		if m.Role == "user" {
			menu.Separator()
			if menu.Item("编辑").Chosen() {
				startInlineEdit(s, m)
			}
		}
		if m.Role == "assistant" && m.IsFinal {
			menu.Separator()
			if menu.Item("分支").Chosen() {
				s.branchFromMessage(m)
			}
			// 只有带轮次 ID 的消息才能定位到该轮的检查点
			if m.TurnID != "" {
				if menu.Item("撤销此轮改动…").Danger().Chosen() {
					s.mu.Lock()
					threadID := s.selectedThreadID
					s.mu.Unlock()
					s.openRevertPreview(threadID, m.TurnID)
				}
			}
		}
		// 只有子会话（Swarm bee）才有父会话可以回报
		if leadID := parentThreadID(s); leadID != "" {
			menu.Separator()
			if menu.Item("发送回父会话").Chosen() {
				s.sendToParentThread(leadID, m)
			}
		}
	}
}

// markdownToPlain 复用视图的 Markdown 解析器，保留代码内容，避免误删标识符中的下划线等字面字符。
func markdownToPlain(text string) string {
	doc := parser.New(parser.Default)
	doc.Set(text)
	var out strings.Builder
	previousCellRow := -1
	for i := 1; i < doc.Len(); i++ {
		block := doc.Block(i)
		if block.Kind == parser.Paragraph && doc.LineCount(i) == 0 {
			continue
		}
		var content strings.Builder
		switch block.Kind {
		case parser.Paragraph, parser.Heading, parser.TableCell:
			for _, inline := range doc.Inlines(i) {
				switch inline.Kind {
				case parser.Text, parser.Code, parser.HTML, parser.Math, parser.FootnoteRef, parser.Mention, parser.Reference:
					content.WriteString(inline.Text)
				case parser.SoftBreak, parser.LineBreak:
					content.WriteByte('\n')
				}
			}
		case parser.CodeBlock, parser.HTMLBlock, parser.MathBlock:
			content.WriteString(strings.TrimSuffix(doc.Lines(i), "\n"))
		default:
			continue
		}
		if out.Len() > 0 {
			if block.Kind == parser.TableCell && block.Parent == previousCellRow {
				out.WriteByte('\t')
			} else {
				out.WriteByte('\n')
			}
		}
		out.WriteString(content.String())
		previousCellRow = -1
		if block.Kind == parser.TableCell {
			previousCellRow = block.Parent
		}
	}
	return out.String()
}

// lastCodeBlock 从 Markdown 文本中提取最后一个围栏代码块的内容：
// 支持三个及以上反引号或波浪线围栏，围栏后的语言标记丢弃；闭合围栏须与开启字符相同且长度不小于开启长度；
// 未闭合的最后一个块也算，内容取到文末；返回内容不含围栏行，末尾换行已去掉。行内代码不算代码块。
func lastCodeBlock(text string) (string, bool) {
	var (
		last     string
		found    bool
		inBlock  bool
		fenceCh  byte
		fenceLen int
		content  []string
	)
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSuffix(raw, "\r")
		// 列表项等缩进场景下围栏前可能有空白；围栏判定忽略行首缩进，块内容保留原样
		trimmed := strings.TrimLeft(line, " \t")
		ch, n := fenceRun(trimmed)
		if inBlock {
			// 闭合围栏其后只允许空白，否则按块内容处理
			if ch == fenceCh && n >= fenceLen && strings.TrimSpace(trimmed[n:]) == "" {
				last, found, inBlock = strings.Join(content, "\n"), true, false
				content = nil
				continue
			}
			content = append(content, line)
			continue
		}
		// 反引号围栏的语言标记里不能再出现反引号，否则是行内代码（如 ```x```）
		if n >= 3 && (ch == '~' || (ch == '`' && !strings.Contains(trimmed[n:], "`"))) {
			inBlock, fenceCh, fenceLen = true, ch, n
			content = nil
		}
	}
	// 未闭合的最后一个块取到文末
	if inBlock {
		last, found = strings.Join(content, "\n"), true
	}
	if !found {
		return "", false
	}
	return strings.TrimSuffix(last, "\n"), true
}

// fenceRun 返回一行开头连续的围栏字符（反引号或波浪线）及其长度；不是围栏行时为 0。
func fenceRun(line string) (byte, int) {
	if line == "" || (line[0] != '`' && line[0] != '~') {
		return 0, 0
	}
	ch := line[0]
	n := 0
	for n < len(line) && line[n] == ch {
		n++
	}
	return ch, n
}

// branchFromMessage 从指定的助手最终答复分支出新会话并切换过去。
func (s *chatState) branchFromMessage(m Message) {
	s.forkFromMessage(m.ID)
}

// forkFromMessage 分支到指定消息（messageID 为空则新建会话）并切换过去。
// 成功后若用户没有切换会话或继续操作，才切换界面；否则只在侧边栏给出新会话。
func (s *chatState) forkFromMessage(messageID string) {
	s.mu.Lock()
	tid := s.selectedThreadID
	seq := s.watchSeq
	busy := s.sending || s.sentDraft != nil
	var projectID, provider string
	for _, th := range s.threads {
		if th.ID == tid {
			projectID = th.ProjectID
			provider = th.Provider
			break
		}
	}
	s.mu.Unlock()

	if tid == "" {
		s.setToast("分支失败：未选中会话")
		return
	}
	if busy {
		s.setToast("请等待消息发送完成后重试")
		return
	}

	go func() {
		ctx := context.Background()
		newThreadID, err := s.branchOrCreateThread(ctx, tid, projectID, provider, messageID)
		if err != nil {
			s.setToast("分支失败：" + err.Error())
			return
		}

		s.updateWorkspace(func() {
			s.mu.Lock()
			unchanged := !s.closed && s.selectedThreadID == tid && s.watchSeq == seq
			s.mu.Unlock()
			if !unchanged {
				s.setToast("已创建新会话，请从侧边栏打开")
				return
			}
			s.selectThread(newThreadID)
			s.setToast("已创建分支会话")
		})
	}()
}

// branchOrCreateThread 分支到 messageID（为空时新建会话），并刷新该项目的会话列表，返回新会话 ID。
func (s *chatState) branchOrCreateThread(ctx context.Context, tid, projectID, provider, messageID string) (string, error) {
	var newThreadID string
	var err error
	if messageID == "" {
		newThreadID, err = s.client.CreateThread(ctx, projectID, provider)
	} else {
		newThreadID, err = s.client.BranchThread(ctx, tid, messageID)
	}
	if err != nil {
		return "", err
	}

	if projectID != "" {
		threads, err := s.client.Threads(ctx, projectID)
		if err == nil {
			s.mu.Lock()
			s.threadsByProj[projectID] = SortThreads(threads)
			var all []Thread
			for _, list := range s.threadsByProj {
				all = append(all, list...)
			}
			s.threads = SortThreads(all)
			// 新建/分支后的列表重载可能带来新的待处理请求/运行标记。
			s.bumpThreadsRuntimeLocked()
			s.mu.Unlock()
		}
	}
	return newThreadID, nil
}

// editAnchorID 返回该消息之前最近的助手最终答复 ID；没有则返回空（表示需要新建会话）。
func (s *chatState) editAnchorID(msgID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, msg := range s.messages {
		if msg.ID == msgID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ""
	}
	for i := idx - 1; i >= 0; i-- {
		if s.messages[i].Role == "assistant" && s.messages[i].IsFinal {
			return s.messages[i].ID
		}
	}
	return ""
}

// branchPopoverMaxWidth 是「从这条回复分支」浮层的最大宽度。
const branchPopoverMaxWidth float32 = 280

// inlineEditState 保存某条用户消息的就地编辑状态：正在编辑的消息 ID 与草稿文本。
// TextArea 绑定 &draft，因此该结构跨帧保持同一地址。
type inlineEditState struct {
	msgID string
	draft string
}

var (
	inlineEditMu     sync.Mutex
	inlineEditStates = make(map[*chatState]*inlineEditState)
)

// startInlineEdit 进入就地编辑：在该用户消息行原位显示预填原文的多行输入框。
func startInlineEdit(s *chatState, m Message) {
	if s == nil || m.Role != "user" {
		return
	}
	inlineEditMu.Lock()
	inlineEditStates[s] = &inlineEditState{msgID: m.ID, draft: m.Text}
	inlineEditMu.Unlock()
	s.invalidate()
}

// inlineEditOf 返回当前就地编辑状态；没有正在编辑的消息时 ok 为 false。
func inlineEditOf(s *chatState) (*inlineEditState, bool) {
	if s == nil {
		return nil, false
	}
	inlineEditMu.Lock()
	defer inlineEditMu.Unlock()
	st, ok := inlineEditStates[s]
	if !ok || st == nil || st.msgID == "" {
		return nil, false
	}
	return st, true
}

// cancelInlineEdit 退出就地编辑，丢弃编辑中的文本。
func cancelInlineEdit(s *chatState) {
	if s == nil {
		return
	}
	inlineEditMu.Lock()
	delete(inlineEditStates, s)
	inlineEditMu.Unlock()
	s.invalidate()
}

// submitInlineEdit 提交就地编辑：分支到该消息之前最近的助手最终答复（没有则新建会话），
// 并把编辑后的文本直接发送到新会话。
func (s *chatState) submitInlineEdit(msgID, text string) {
	// 就地编辑的正文里可能有长文本粘贴占位符，交后端前先展开全文。
	text = s.expandPastedPlaceholders(text)
	text = strings.TrimSpace(text)
	if msgID == "" || text == "" {
		return
	}
	anchorID := s.editAnchorID(msgID)

	s.mu.Lock()
	tid := s.selectedThreadID
	seq := s.watchSeq
	busy := s.sending || s.sentDraft != nil
	var projectID, provider string
	for _, th := range s.threads {
		if th.ID == tid {
			projectID = th.ProjectID
			provider = th.Provider
			break
		}
	}
	s.mu.Unlock()

	if tid == "" {
		s.setToast("编辑失败：未选中会话")
		return
	}
	if busy {
		s.setToast("请等待消息发送完成后重试")
		return
	}
	if projectID == "" {
		s.setToast("编辑失败：未找到会话所属项目")
		return
	}

	go func() {
		ctx := context.Background()
		newThreadID, err := s.branchOrCreateThread(ctx, tid, projectID, provider, anchorID)
		if err != nil {
			s.setToast("分支失败：" + err.Error())
			return
		}
		if _, err := s.client.Send(ctx, newThreadID, text); err != nil {
			s.setToast("发送失败：" + err.Error())
			return
		}

		s.updateWorkspace(func() {
			s.mu.Lock()
			unchanged := !s.closed && s.selectedThreadID == tid && s.watchSeq == seq
			s.mu.Unlock()
			// 用户已切换会话或退出编辑时不抢占界面，只在侧边栏给出新会话
			if cur, ok := inlineEditOf(s); !unchanged || !ok || cur.msgID != msgID {
				s.setToast("已创建新会话，请从侧边栏打开")
				return
			}
			cancelInlineEdit(s)
			s.selectThread(newThreadID)
			s.setToast("已发送到新会话")
		})
	}()
}

// quoteForReply 把消息纯文本转成 Markdown 引用块：每行前加「> 」。
func quoteForReply(text string) string {
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = "> " + strings.TrimRight(line, "\r")
	}
	return strings.Join(lines, "\n")
}

// quoteIntoDraft 把消息纯文本作为引用块追加到当前输入框草稿末尾。
func (s *chatState) quoteIntoDraft(text string) {
	quote := quoteForReply(markdownToPlain(text))
	if quote == "" {
		return
	}
	s.mu.Lock()
	if strings.TrimSpace(s.draft) == "" {
		s.draft = quote
	} else {
		s.draft = strings.TrimRight(s.draft, "\n") + "\n\n" + quote
	}
	s.draftText = richtext.NewText(s.draft)
	s.completionKind = completionNone
	s.mu.Unlock()
	s.setToast("已引用到输入框")
}

// parentThreadID 返回当前会话所属父（Lead）会话的线程 ID；当前会话不是子会话时返回空。
// 状态取自 Swarm 面板缓存；该会话还没加载过时先触发一次加载，加载完成后按钮自然出现。
func parentThreadID(s *chatState) string {
	if s == nil || s.client == nil {
		return ""
	}
	s.mu.Lock()
	selected := s.selectedThreadID
	s.mu.Unlock()
	if selected == "" {
		return ""
	}
	hp := getSwarmPanelState(s)
	if hp == nil {
		return ""
	}
	swarmPanelMu.Lock()
	loadedFor := hp.loadedFor
	loading := hp.loading
	lead := hp.state.LeadThreadID
	isBee := hp.state.IsBee
	swarmPanelMu.Unlock()
	if loadedFor != selected {
		s.loadSwarm(selected)
		return ""
	}
	if loading || !isBee || lead == "" || lead == selected {
		return ""
	}
	return lead
}

// parentReportText 拼装回传父会话的文本：「[来自子会话 <标题>]」加回复全文。
func parentReportText(title, reply string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "未命名会话"
	}
	return fmt.Sprintf("[来自子会话 %s] %s", title, reply)
}

// sendToParentThread 把回复全文发送到父（Lead）会话。
func (s *chatState) sendToParentThread(leadID string, m Message) {
	if s == nil || s.client == nil || leadID == "" {
		return
	}
	s.mu.Lock()
	title := ""
	for _, th := range s.threads {
		if th.ID == s.selectedThreadID {
			title = th.Title
			break
		}
	}
	s.mu.Unlock()

	text := parentReportText(title, m.Text)
	go func() {
		ctx := context.Background()
		if _, err := s.client.Send(ctx, leadID, text); err != nil {
			s.setToast("发送到父会话失败：" + err.Error())
			return
		}
		s.setToast("已发送到父会话")
	}()
}

// renderSendToParentButton 渲染助手操作栏的「回报父会话」按钮，只在子会话中显示。
func renderSendToParentButton(c *ui.Context, s *chatState, m Message, leadID string) {
	ui.ButtonBase(c.Key("send-parent-btn-"+m.ID)).
		Label("回报父会话").
		Tooltip("发送回父会话").
		Padding(Space0, Space2).
		Radius(RadiusXS).
		Background(ThemeHover(c)).
		OnClick(func() {
			s.sendToParentThread(leadID, m)
		}).
		Children(func() {
			ui.Row(c.Key("send-parent-row-" + m.ID)).Gap(Space1).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, IconReply()).Size(inlineIconSize, inlineIconSize).TextColor(ThemeSecondaryText(c))
				ui.Text(c, "回报父会话").FontSize(TypeCaption).TextColor(ThemeSecondaryText(c))
			})
		})
}

var (
	branchPopoverMu sync.Mutex
	branchPopovers  = make(map[string]*bool)
)

// renderEditMessageButton 渲染用户消息操作栏的「修改」按钮：进入就地编辑，
// 与右键菜单的「编辑」项同一条件（仅用户消息）与同一入口 startInlineEdit。
func renderEditMessageButton(c *ui.Context, s *chatState, m Message) {
	ui.ButtonBase(c.Key("edit-btn-"+m.ID)).
		Label("修改").
		Tooltip("修改这条提问").
		Padding(Space0, Space2).
		Radius(RadiusXS).
		Background(ThemeHover(c)).
		OnClick(func() {
			startInlineEdit(s, m)
		}).
		Children(func() {
			ui.Row(c.Key("edit-btn-row-" + m.ID)).Gap(Space1).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, IconPencilLine()).Size(inlineIconSize, inlineIconSize).TextColor(ThemeSecondaryText(c))
				ui.Text(c, "修改").FontSize(TypeCaption).TextColor(ThemeSecondaryText(c))
			})
		})
}

// branchPopoverState 返回某条消息分支确认浮层的开合状态；每条消息一份，跨帧地址稳定。
func branchPopoverState(msgID string) *bool {
	branchPopoverMu.Lock()
	defer branchPopoverMu.Unlock()
	open, ok := branchPopovers[msgID]
	if !ok {
		open = new(bool)
		branchPopovers[msgID] = open
	}
	return open
}

// renderBranchActionButton 渲染助手操作栏的分支按钮：点击弹出确认浮层，确认后从这条回复分出新会话。
func renderBranchActionButton(c *ui.Context, s *chatState, m Message) {
	open := branchPopoverState(m.ID)

	btn := ui.ButtonBase(c.Key("branch-btn-"+m.ID)).
		Label("分支").
		Tooltip("从这条回复分支新会话").
		Padding(Space0, Space2).
		Radius(RadiusXS).
		Background(ThemeHover(c)).
		OnClick(func() {
			*open = true
			s.invalidate()
		})
	btn.Children(func() {
		ui.Row(c.Key("branch-btn-row-" + m.ID)).Gap(Space1).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, IconGitBranch()).Size(inlineIconSize, inlineIconSize).TextColor(ThemeSecondaryText(c))
			ui.Text(c, "分支").FontSize(TypeCaption).TextColor(ThemeSecondaryText(c))
		})
	})

	appPopover(c.Key("branch-popover-"+m.ID), btn, open, nil, func() {
		popoverCard(c, ui.Column(c.Key("branch-popover-body-"+m.ID)).Gap(Space3).Padding(Space3).MaxWidth(branchPopoverMaxWidth)).Children(func() {
			ui.Text(c, "从这条回复分支？").FontSize(TypeBody).FontWeight(600).TextColor(ThemePrimaryText(c))
			ui.Text(c, "新会话将包含到这条回复为止的对话。").
				FontSize(TypeFootnote).
				LineHeight(1.4).
				TextColor(ThemeSecondaryText(c))
			ui.Row(c.Key("branch-popover-actions-" + m.ID)).Gap(Space3).Justify(ui.End).Children(func() {
				Button(c, ButtonOpts{
					Key:     "branch-cancel-" + m.ID,
					Text:    "取消",
					Label:   "取消分支",
					Variant: ButtonSecondary,
					OnClick: func() {
						*open = false
						s.invalidate()
					},
				})
				Button(c, ButtonOpts{
					Key:     "branch-confirm-" + m.ID,
					Text:    "分支",
					Label:   "确认分支",
					Variant: ButtonPrimary,
					OnClick: func() {
						*open = false
						s.branchFromMessage(m)
					},
				})
			})
		})
	})
}
