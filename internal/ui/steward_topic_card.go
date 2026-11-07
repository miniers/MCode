package mcodeui

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// renderStewardTopicCard 把话题任务书卡片固定在当前会话的时间线上方。
func renderStewardTopicCard(c *ui.Context, s *chatState, threadID string) {
	if _, ok := s.stewardTopicsClient(); !ok {
		return
	}
	topic, ok := s.stewardTopicFor(threadID)
	if !ok {
		return
	}

	st := getStewardTopicsState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	collapsed := st.collapsed[threadID]
	errText := st.cardErrs[threadID]
	noteText := st.cardNotes[threadID]
	busy := st.busy[threadID]
	st.mu.Unlock()

	var reviewTasks []KanbanTask
	if !collapsed && (topic.Phase == StewardTopicRunning || topic.Phase == StewardTopicPaused) {
		s.ensureStewardReviewTasks(threadID, topic.ProjectID, topic.GoalID)
		st.mu.Lock()
		reviewTasks = append([]KanbanTask(nil), st.reviewTasks[threadID]...)
		st.mu.Unlock()
	}

	title := topic.Title
	if title == "" {
		title = "未命名话题"
	}
	statusText := stewardTopicStatusText(topic)

	card := ui.Column(c.Key("steward-topic-card-"+threadID)).
		FillWidth().
		MaxWidth(continuationStartCardMaxWidth).
		AlignSelf(ui.Center).
		AlignItems(ui.Start).
		Background(ThemeSurface(c)).
		Border(1, ThemeBorder(c)).
		Radius(RadiusLG).
		Padding(Space4, Space5).
		Gap(Space4).
		Margin(0, 0, Space6, 0).
		Label("话题：" + title)

	card.Children(func() {
		// 卡片头：标题、项目、阶段与折叠箭头，整行可点。
		header := ui.Row(c.Key("steward-topic-card-header-" + threadID)).
			FillWidth().
			Gap(Space3).
			AlignItems(ui.Center).
			Label(title)
		if header.Hovered() {
			header.Background(ThemeHover(c))
		}
		if header.Clicked() {
			st.mu.Lock()
			st.collapsed[threadID] = !st.collapsed[threadID]
			st.mu.Unlock()
			s.invalidate()
		}
		header.Children(func() {
			ui.Text(c, title).
				Grow(1).
				SingleLine().
				FontSize(scaledFont(TypeProse)).
				FontWeight(600).
				TextColor(ThemePrimaryText(c))
			if topic.ProjectName != "" {
				ui.Text(c, topic.ProjectName).
					SingleLine().
					FontSize(scaledFont(FontSizeProjectName)).
					TextColor(ThemeSecondaryText(c))
			}
			ui.Text(c, statusText).
				SingleLine().
				FontSize(scaledFont(FontSizeProjectName)).
				TextColor(ThemeAccent(c))
			// 「更多操作」按钮放在折叠箭头前：任务书为空、没有底部按钮的话题也能从卡片里删除。
			// 按钮自己接管按下的指针，不会连带触发整行的折叠。
			cardMoreBtn := ui.ButtonBase(c.Key("steward-topic-card-more-"+threadID)).
				Label("更多操作").
				Tooltip("更多操作").
				Radius(RadiusSM).
				Padding(Space0, Space1)
			if cardMoreBtn.Hovered() {
				cardMoreBtn.Background(ThemeHover(c))
			}
			cardMoreBtn.Menu(stewardTopicContextMenu(c, s, topic))
			cardMoreBtn.Children(func() {
				ui.Icon(c, IconMoreHorizontal()).
					Size(IconSM, IconSM).
					TextColor(ThemeSecondaryText(c))
			})
			icon := IconChevronRight()
			if !collapsed {
				icon = IconChevronDown()
			}
			ui.Icon(c, icon).
				Size(10, 10).
				TextColor(ThemeSecondaryText(c))
		})

		if topic.TasksTotal > 0 {
			renderStewardTopicProgress(c, threadID, topic.TasksDone, topic.TasksTotal)
		}
		if collapsed {
			return
		}

		if brief := strings.TrimSpace(topic.Brief); brief != "" {
			// 任务书只铺用户要拍板的三节；其余节留在库里给模型看，卡片不整篇展开。
			ui.Scroll(c.Key("steward-topic-brief-" + threadID)).
				FillWidth().
				MaxHeight(220).
				Children(func() {
					markdown.View(c, stewardBriefUserSections(brief), &s.mdStaticOpts)
				})
		}

		// 卡片上只留一个主按钮；同一阶段的次要动作收进「更多操作」菜单。
		if action, ok := stewardTopicPrimaryActionFor(topic); ok {
			ui.Row(c.Key("steward-topic-actions-" + threadID)).
				FillWidth().
				Gap(Space4).
				AlignItems(ui.Center).
				Children(func() {
					if topic.Phase == StewardTopicPlanFailed {
						ui.Text(c, "这次没出方案").
							FontSize(scaledFont(FontSizeProjectName)).
							TextColor(ThemeSecondaryText(c))
					}
					Button(c.Key(action.Key+"-"+threadID), ButtonOpts{
						Text:     stewardTopicActionButtonText(action.Key, action.Text, busy),
						Label:    action.Text,
						Variant:  ButtonPrimary,
						Disabled: busy,
						OnClick:  func() { action.Run(s, topic) },
					})
					if topic.Phase == StewardTopicDrafting && topic.OpenQuestions > 0 {
						ui.Text(c, fmt.Sprintf("还有 %d 个待定问题，将按推荐答案处理", topic.OpenQuestions)).
							FontSize(scaledFont(FontSizeProjectName)).
							TextColor(ThemeSecondaryText(c))
					}
				})
		}

		if topic.Phase == StewardTopicRunning || topic.Phase == StewardTopicPaused {
			ui.Text(c, "待验收任务").
				FontSize(scaledFont(TypeFootnote)).
				FontWeight(600).
				TextColor(ThemePrimaryText(c))
			if len(reviewTasks) == 0 {
				ui.Text(c, "暂无待验收任务").
					FontSize(scaledFont(FontSizeProjectName)).
					TextColor(ThemeSecondaryText(c))
			}
			for _, task := range reviewTasks {
				renderStewardReviewTaskRow(c, s, threadID, task)
			}
		}

		if errText != "" {
			ui.Text(c, errText).
				FontSize(scaledFont(FontSizeProjectName)).
				TextColor(ThemeDanger(c))
		}
		// 成功提示只停留几秒：让用户确认这一下真的生效了，不会被当成当前状态。
		if noteText != "" {
			ui.Text(c, noteText).
				FontSize(scaledFont(FontSizeProjectName)).
				TextColor(ThemeSecondaryText(c))
		}
	})
}

// stewardBriefUserSections 从任务书里只挑出用户要拍板的三节（目标、范围、验收），
// 按原顺序拼成新的 markdown。其余节（现状、已定决策、约束、待定问题）留在库里给模型看，
// 卡片不整篇铺开。一节都挑不出来时原样返回，避免卡片空白。
func stewardBriefUserSections(brief string) string {
	var kept []string
	var block []string
	wanted := false
	flush := func() {
		if len(block) > 0 && wanted {
			kept = append(kept, strings.TrimSpace(strings.Join(block, "\n")))
		}
		block = nil
		wanted = false
	}
	for _, line := range strings.Split(brief, "\n") {
		if title, ok := stewardBriefHeadingTitle(line); ok {
			flush()
			wanted = stewardBriefSectionWanted(title)
		}
		block = append(block, line)
	}
	flush()
	if len(kept) == 0 {
		return brief
	}
	return strings.Join(kept, "\n\n")
}

// stewardBriefHeadingTitle 报告这一行是不是二级标题，并返回去掉序号后的标题文字。
// 只认两个井号：三级及更深的标题算正文。
func stewardBriefHeadingTitle(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "## ") {
		return "", false
	}
	title := strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))
	// 标题带序号时（「1. 目标」「一、范围」）去掉序号，后续关键词匹配才不落空。
	title = strings.TrimSpace(strings.TrimLeft(title, "0123456789.．、）) "))
	return title, true
}

// stewardBriefSectionWanted 报告这一节是否属于卡片要展示的三类，匹配要宽松（「验收标准」也算验收）。
func stewardBriefSectionWanted(title string) bool {
	for _, keyword := range []string{"目标", "范围", "验收"} {
		if strings.Contains(title, keyword) {
			return true
		}
	}
	return false
}

// stewardTopicActionButtonText 返回按钮文案：正在提交时换成进行中的说法（「开工中…」），
// 其余时候还是原来的动作名。
func stewardTopicActionButtonText(key, text string, busy bool) string {
	if !busy {
		return text
	}
	if busyText := stewardTopicBusyText(key); busyText != "" {
		return busyText
	}
	return text
}

// stewardTopicPrimaryAction 描述话题卡上唯一的主按钮：键前缀、文案与点击后要做的事。
type stewardTopicPrimaryAction struct {
	Key  string
	Text string
	Run  func(s *chatState, topic StewardTopic)
}

// stewardTopicPrimaryActionFor 是「阶段 → 主按钮」的唯一映射。同一阶段有多个动作时只留主按钮，
// 次要动作（例如计划阶段的「审查计划」）收进「更多操作」菜单。
func stewardTopicPrimaryActionFor(topic StewardTopic) (stewardTopicPrimaryAction, bool) {
	switch topic.Phase {
	case StewardTopicDrafting:
		// 待定问题清零后宿主会自动开工，「直接开工」只是手动兜底。
		if strings.TrimSpace(topic.Brief) == "" {
			return stewardTopicPrimaryAction{}, false
		}
		// 还有未定问题时「按推荐答案兜底」是刻意设计，不禁用按钮，只把文案说清并加一次轻确认。
		if topic.OpenQuestions > 0 {
			return stewardTopicPrimaryAction{
				Key:  "steward-topic-confirm",
				Text: "按推荐答案开工",
				Run:  func(s *chatState, t StewardTopic) { s.openStewardTopicStartConfirm(t) },
			}, true
		}
		return stewardTopicPrimaryAction{
			Key:  "steward-topic-confirm",
			Text: "直接开工",
			Run:  func(s *chatState, t StewardTopic) { s.confirmStewardTopic(t.ThreadID) },
		}, true
	case StewardTopicPlanReview:
		// 计划本该由总管自审后直接开工，卡片只留「直接开工」。
		if topic.GoalID == "" {
			return stewardTopicPrimaryAction{}, false
		}
		return stewardTopicPrimaryAction{
			Key:  "steward-topic-start",
			Text: "直接开工",
			Run:  func(s *chatState, t StewardTopic) { s.startStewardTopicGoal(t.ThreadID) },
		}, true
	case StewardTopicPlanFailed:
		// 规划没成功：只有「重新规划」一条路，任务书保留，不需要用户重写。
		return stewardTopicPrimaryAction{
			Key:  "steward-topic-replan",
			Text: "重新规划",
			Run:  func(s *chatState, t StewardTopic) { s.replanStewardTopic(t.ThreadID) },
		}, true
	case StewardTopicCompleted:
		if topic.Closed {
			return stewardTopicPrimaryAction{}, false
		}
		return stewardTopicPrimaryAction{
			Key:  "steward-topic-close",
			Text: "确认结项",
			Run:  func(s *chatState, t StewardTopic) { s.closeStewardTopic(t.ThreadID) },
		}, true
	}
	return stewardTopicPrimaryAction{}, false
}

func renderStewardTopicProgress(c *ui.Context, threadID string, done, total int) {
	if total <= 0 {
		return
	}
	ratio := float32(done) / float32(total)
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	ui.Row(c.Key("steward-topic-progress-" + threadID)).
		FillWidth().
		Gap(Space3).
		AlignItems(ui.Center).
		Children(func() {
			track := ui.Row(c.Key("steward-topic-progress-track-" + threadID)).
				Grow(1).
				Height(Space1).
				Radius(RadiusPill).
				Background(ThemeHover(c))
			track.Children(func() {
				ui.Box(c.Key("steward-topic-progress-fill-" + threadID)).
					FillHeight().
					Width(0).
					Grow(ratio).
					Radius(RadiusPill).
					Background(ThemeAccent(c))
				ui.Box(c.Key("steward-topic-progress-rest-" + threadID)).
					Width(0).
					Grow(1 - ratio)
			})
			ui.Text(c, fmt.Sprintf("%d/%d", done, total)).
				FontSize(scaledFont(FontSizeRelativeTime)).
				TextColor(ThemeSecondaryText(c))
		})
}

func renderStewardReviewTaskRow(c *ui.Context, s *chatState, threadID string, task KanbanTask) {
	key := threadID + "/" + task.ID
	st := getStewardTopicsState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	busy := st.busy[key]
	st.mu.Unlock()

	title := task.Title
	if title == "" {
		title = "未命名任务"
	}
	ui.Row(c.Key("steward-topic-task-" + key)).
		FillWidth().
		Gap(Space3).
		AlignItems(ui.Center).
		Children(func() {
			ui.Text(c, title).
				Grow(1).
				SingleLine().
				FontSize(scaledFont(TypeFootnote)).
				TextColor(ThemePrimaryText(c))
			Button(c.Key("steward-topic-task-accept-"+key), ButtonOpts{
				Text:     stewardTopicActionButtonText("steward-topic-task-accept", "通过", busy),
				Label:    "通过 " + title,
				Variant:  ButtonSecondary,
				Disabled: busy,
				OnClick:  func() { s.acceptStewardTask(threadID, task) },
			})
			Button(c.Key("steward-topic-task-reject-"+key), ButtonOpts{
				Text:     "打回",
				Label:    "打回 " + title,
				Variant:  ButtonDanger,
				Disabled: busy,
				OnClick:  func() { s.openStewardRejectDialog(threadID, task) },
			})
		})
}

// renderStewardTopicOverlays 渲染话题相关的弹窗；挂在根层（renderSidebarModals），
// 这样侧栏被动画裁切、或主区切到自动化视图时弹窗依然完整可见。
func renderStewardTopicOverlays(c *ui.Context, s *chatState) {
	// 「审查计划」的来处记录只在这一趟任务板里有效：用户自己离开任务板就清掉，
	// 返回条不该在下一趟打开任务板时又冒出来。这里每帧都会跑到（侧栏与主区都在根层），
	// 所以放在客户端判断之前，关掉总管之后也不会漏清。
	syncStewardPlanReviewOrigin(s)
	if _, ok := s.stewardTopicsClient(); !ok {
		return
	}
	renderStewardStartTopicDialog(c, s)
	renderStewardRejectDialog(c, s)
	renderStewardDeleteTopicModal(c, s)
	renderStewardTopicStartConfirmModal(c, s)
}

// modalDragStewardTopicStart 是「按推荐答案开工」确认弹窗的拖拽 key。
// 只有这一个弹窗用得上，就近定义，与 modal_drag.go 的常量同构。
const modalDragStewardTopicStart = "modal-drag-steward-topic-start"

// renderStewardTopicStartConfirmModal 是「按推荐答案开工」前的轻确认：任务书还有未定问题时，
// 点主按钮不直接开工，先说清这一下会按推荐答案做、之后还能改。
func renderStewardTopicStartConfirmModal(c *ui.Context, s *chatState) {
	st := getStewardTopicsState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	open := st.startConfirmOpen
	questions := st.startConfirmQuestions
	st.mu.Unlock()
	if !open {
		// 关着的时候也走一遍，顺带复位拖拽偏移。
		RenderConfirmDialog(c, ConfirmDialog{Key: modalDragStewardTopicStart})
		return
	}

	nickname, _ := s.stewardIdentity()
	name := stewardName(nickname)
	RenderConfirmDialog(c, ConfirmDialog{
		Key:          modalDragStewardTopicStart,
		Open:         true,
		Tone:         ConfirmSafe,
		Title:        "按推荐答案开工？",
		Body:         fmt.Sprintf("还有 %d 个问题没定，%s会先按推荐答案做，之后可以随时改。", questions, name),
		ConfirmLabel: "开工",
		CancelLabel:  "再想想",
		OnConfirm:    func() { s.confirmStewardTopicStart() },
		OnCancel:     func() { s.closeStewardTopicStartConfirm() },
	})
}

const modalDragStewardStart = "modal-drag-steward-start"
const modalDragStewardReject = "modal-drag-steward-reject"

func renderStewardStartTopicDialog(c *ui.Context, s *chatState) {
	st := getStewardTopicsState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	open := st.startOpen
	projectName := st.startProjectName
	errText := st.startErr
	busy := st.startBusy
	st.mu.Unlock()
	if !open {
		modalDragReset(modalDragStewardStart)
		return
	}
	nickname, _ := s.stewardIdentity()
	name := stewardName(nickname)
	winW, winH := c.Size()
	width, height := modalResizeSize(modalDragStewardStart, 520, 380, min(360, max(0, winW-modalDragPanelInset)), min(240, max(0, winH-modalDragPanelInset)), winW, winH)

	dialogOpen := true
	ui.DialogBase(c, &dialogOpen, func(backdrop, panel ui.Element) {
		backdrop.Absolute().Left(0).Top(0).Right(0).Bottom(0).Center()
		_ = ui.Box(c.Key("steward-start-backdrop")).Fill().Background(ui.Transparent).OnClick(func() {
			dialogOpen = false
		})
		if !dialogOpen {
			return
		}
		// 焦点不在输入框时（如刚点过卡片），Esc 由这里关闭；与 sidebar.go 的重命名弹窗同一写法。
		if c.Shortcut(0, ui.KeyEscape) {
			dialogOpen = false
			return
		}
		panel.Key("steward-start-panel").Label("steward-start-panel").Padding(0).Gap(0)
		modalDragPanel(c, modalDragStewardStart, panel)
		card := applyElevation(c, ui.Column(c.Key("steward-start-card")).
			Label("steward-start-card").
			Width(width).
			Height(height).
			Padding(Space6).
			Radius(RadiusLG).
			Background(ThemeWindowBg(c)).
			BorderWidth(1).
			BorderColor(ThemeBorder(c)), ElevDialog)
		card.Gap(Space4).
			Children(func() {
				modalDragHandle(c, modalDragStewardStart, ui.Row(c.Key("steward-start-header")).FillWidth().Children(func() {
					ui.Text(c, "交给"+name).FontSize(scaledFont(TypeTitle)).FontWeight(600).TextColor(ThemePrimaryText(c))
				}))
				ui.Text(c, "把这件事交给"+name+"：它会先跟你确认要做什么，再出方案、执行。").
					FontSize(scaledFont(FontSizeProjectName)).
					TextColor(ThemeSecondaryText(c))
				if projectName != "" {
					ui.Text(c, "项目："+projectName).
						FontSize(scaledFont(FontSizeProjectName)).
						TextColor(ThemeSecondaryText(c))
				}
				in := ui.TextArea(c.Key("steward-start-input"), &st.startText).
					Label("steward-start-input").
					Grow(1).
					FillWidth().
					FontSize(scaledFont(TypeBody)).
					Placeholder("想做什么？").
					AutoFocus()
				// 输入框有焦点时 Esc 先到这里：关闭弹窗并清掉错误提示与草稿，
				// 与 sidebar.go 的重命名弹窗一致。
				in.OnTextKey(func(k ui.TextKey) bool {
					if k.Key == ui.KeyEscape {
						s.closeStewardStartTopicDialog()
						return true
					}
					return false
				})
				if errText != "" {
					ui.Text(c, errText).
						FontSize(scaledFont(FontSizeProjectName)).
						TextColor(ThemeDanger(c))
				}
				ui.Row(c.Key("steward-start-footer")).
					FillWidth().
					AlignItems(ui.Center).
					Children(func() {
						ui.Box(c.Key("steward-start-footer-spacer")).Grow(1)
						Button(c.Key("steward-start-cancel"), ButtonOpts{
							Text:    "取消",
							Label:   "取消",
							Variant: ButtonSecondary,
							OnClick: func() {
								s.closeStewardStartTopicDialog()
							},
						})
						Button(c.Key("steward-start-submit"), ButtonOpts{
							Text:     "交给它",
							Label:    "交给它",
							Variant:  ButtonPrimary,
							Disabled: busy,
							OnClick:  func() { s.submitStewardStartTopic() },
						})
					})
				modalResizeGrips(c, modalDragStewardStart, width, height)
			})
	})
	if !dialogOpen {
		s.closeStewardStartTopicDialog()
	}
}

func renderStewardRejectDialog(c *ui.Context, s *chatState) {
	st := getStewardTopicsState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	open := st.rejectOpen
	title := st.rejectTitle
	errText := st.rejectErr
	busy := st.rejectBusy
	st.mu.Unlock()
	if !open {
		modalDragReset(modalDragStewardReject)
		return
	}
	if title == "" {
		title = "未命名任务"
	}
	winW, winH := c.Size()
	width, height := modalResizeSize(modalDragStewardReject, 460, 320, min(360, max(0, winW-modalDragPanelInset)), min(240, max(0, winH-modalDragPanelInset)), winW, winH)

	dialogOpen := true
	ui.DialogBase(c, &dialogOpen, func(backdrop, panel ui.Element) {
		backdrop.Absolute().Left(0).Top(0).Right(0).Bottom(0).Center()
		_ = ui.Box(c.Key("steward-reject-backdrop")).Fill().Background(ui.Transparent).OnClick(func() {
			dialogOpen = false
		})
		if !dialogOpen {
			return
		}
		// 焦点不在输入框时（如刚点过卡片），Esc 由这里关闭；与 sidebar.go 的重命名弹窗同一写法。
		if c.Shortcut(0, ui.KeyEscape) {
			dialogOpen = false
			return
		}
		panel.Key("steward-reject-panel").Label("steward-reject-panel").Padding(0).Gap(0)
		modalDragPanel(c, modalDragStewardReject, panel)
		card := applyElevation(c, ui.Column(c.Key("steward-reject-card")).
			Label("steward-reject-card").
			Width(width).
			Height(height).
			Padding(Space6).
			Radius(RadiusLG).
			Background(ThemeWindowBg(c)).
			BorderWidth(1).
			BorderColor(ThemeBorder(c)), ElevDialog)
		card.Gap(Space4).
			Children(func() {
				modalDragHandle(c, modalDragStewardReject, ui.Row(c.Key("steward-reject-header")).FillWidth().Children(func() {
					ui.Text(c, "打回任务").FontSize(scaledFont(TypeTitle)).FontWeight(600).TextColor(ThemePrimaryText(c))
				}))
				ui.Text(c, title).
					SingleLine().
					FontSize(scaledFont(FontSizeProjectName)).
					TextColor(ThemeSecondaryText(c))
				in := ui.TextArea(c.Key("steward-reject-input"), &st.rejectText).
					Label("steward-reject-input").
					Grow(1).
					FillWidth().
					FontSize(scaledFont(TypeBody)).
					Placeholder("哪里不合格？要它怎么改？").
					AutoFocus()
				// 输入框有焦点时 Esc 先到这里：关闭弹窗并清掉错误提示与草稿，
				// 与 sidebar.go 的重命名弹窗一致。
				in.OnTextKey(func(k ui.TextKey) bool {
					if k.Key == ui.KeyEscape {
						s.closeStewardRejectDialog()
						return true
					}
					return false
				})
				if errText != "" {
					ui.Text(c, errText).
						FontSize(scaledFont(FontSizeProjectName)).
						TextColor(ThemeDanger(c))
				}
				ui.Row(c.Key("steward-reject-footer")).
					FillWidth().
					AlignItems(ui.Center).
					Children(func() {
						ui.Box(c.Key("steward-reject-footer-spacer")).Grow(1)
						Button(c.Key("steward-reject-cancel"), ButtonOpts{
							Text:    "取消",
							Label:   "取消",
							Variant: ButtonSecondary,
							OnClick: func() {
								s.closeStewardRejectDialog()
							},
						})
						Button(c.Key("steward-reject-submit"), ButtonOpts{
							Text:     "确认打回",
							Label:    "确认打回",
							Variant:  ButtonDanger,
							Disabled: busy,
							OnClick:  func() { s.submitStewardReject() },
						})
					})
				modalResizeGrips(c, modalDragStewardReject, width, height)
			})
	})
	if !dialogOpen {
		s.closeStewardRejectDialog()
	}
}

// renderStewardDeleteTopicModal 是删除话题的确认弹窗：话题会话、话题记录与账本条目会一并移除，
// 已建的目标会被取消，运行中的任务会被中断，所以必须二次确认。
func renderStewardDeleteTopicModal(c *ui.Context, s *chatState) {
	st := getStewardTopicsState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	open := st.deleteOpen
	title := st.deleteTitle
	errText := st.deleteErr
	busy := st.deleteBusy
	st.mu.Unlock()
	if !open {
		modalDragReset(modalDragDeleteTopic)
		return
	}

	desc := "删除后无法恢复。话题会话、话题记录和账本条目都会移除。已建的目标会被取消，运行中的任务会被中断；任务会话和它们的改动会保留。"
	if title != "" {
		desc = fmt.Sprintf("话题「%s」删除后无法恢复。话题会话、话题记录和账本条目都会移除。已建的目标会被取消，运行中的任务会被中断；任务会话和它们的改动会保留。", title)
	}

	RenderConfirmDialog(c, ConfirmDialog{
		Key:          modalDragDeleteTopic,
		Open:         true,
		Tone:         ConfirmDanger,
		Title:        "删除话题",
		Body:         desc,
		ConfirmLabel: "删除",
		CancelLabel:  "取消",
		// 删除进行中禁用确认，防止重复提交。
		ConfirmDisabled: busy,
		Footer: func() {
			if errText == "" {
				return
			}
			ui.Text(c, errText).
				FontSize(scaledFont(FontSizeProjectName)).
				TextColor(ThemeDanger(c))
		},
		OnConfirm: func() { s.confirmStewardDeleteTopic() },
		OnCancel:  func() { s.closeStewardDeleteTopicModal() },
	})
}
