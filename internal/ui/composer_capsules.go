package mcodeui

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
)

// 输入区状态胶囊：待办进度、继续提示与运行中改动统一成同一行小胶囊。
// 胶囊只描述外观与回调，状态仍由 chatState 持有；弹层锚定在胶囊上。
const (
	// composerCapsuleHeight 是胶囊高度。
	composerCapsuleHeight float32 = ControlSM
	// composerCapsuleRadius 是胶囊圆角（完整胶囊形）。
	composerCapsuleRadius float32 = RadiusPill
	// composerCapsuleGap 是胶囊之间、图标与文字之间的间距。
	composerCapsuleGap float32 = 6
	// composerCapsuleIconSize 是胶囊里前导图标与尾随关闭图标的边长。
	composerCapsuleIconSize float32 = 12
	// composerCapsuleDismissSize 是关闭按钮的点击区域边长（图标仍是 12pt）。
	composerCapsuleDismissSize float32 = 18
	// composerCapsuleLiveDotSize 是实时状态胶囊前导脉动圆点的边长。
	composerCapsuleLiveDotSize float32 = 6
)

// composerCapsule 是输入区上方状态胶囊行里的一枚胶囊。
type composerCapsule struct {
	Key      string  // 稳定的元素键，同一行内必须唯一
	Label    string  // 无障碍与测试定位用的唯一文字
	Tooltip  string  // 指针悬停提示，空串表示没有
	Icon     *ui.SVG // 12pt 前导图标
	Text     string  // 单行正文，超长截断
	MaxWidth float32 // 正文最大宽度，0 表示不限制
	OnClick  func()  // 点击胶囊主体，nil 表示不可点

	// Live 标记实时状态胶囊：前导位置换成脉动圆点，描边改用强调色。
	Live bool

	// 可选的尾随关闭按钮：DismissLabel 为空时不渲染。
	DismissLabel string
	OnDismiss    func()

	// 可选的锚定浮层（例如待办清单）：PopoverOpen 为 nil 时不渲染。
	PopoverKey     string
	PopoverOpen    *bool
	PopoverDismiss func()
	PopoverBody    func()
}

// renderComposerCapsuleRow 渲染输入框上方的胶囊行：Wrap 布局、6pt 间距。
// 没有胶囊时整行不渲染，Column 的间距也因此不会多出一段空白。
func renderComposerCapsuleRow(c *ui.Context, s *chatState, capsules []composerCapsule) {
	if len(capsules) == 0 {
		return
	}
	ui.Row(c.Key("composer-capsule-row")).
		Label("输入区状态胶囊").
		Wrap().
		Gap(composerCapsuleGap).
		AlignItems(ui.Center).
		Children(func() {
			for _, capsule := range capsules {
				renderComposerCapsule(c, s, capsule)
			}
		})
}

// renderComposerCapsule 渲染单枚胶囊：图标 + 单行正文 + 可选的关闭按钮，
// 以及可选的锚定浮层（浮层只在 open 为真时构建）。
func renderComposerCapsule(c *ui.Context, s *chatState, capsule composerCapsule) {
	if capsule.Key == "" || capsule.Text == "" {
		return
	}
	// 弹层展开时保持高亮，避免指针移进浮层后胶囊看起来被选中又落空。
	open := capsule.PopoverOpen != nil && *capsule.PopoverOpen
	btn := ui.ButtonBase(c.Key(capsule.Key)).
		Label(capsule.Label).
		Height(composerCapsuleHeight).
		Radius(composerCapsuleRadius).
		Padding(0, Space3).
		Transition(motionFeedback(c))
	// 实时状态胶囊用强调色描边，普通胶囊用通用描边。
	if capsule.Live {
		btn.Border(Hairline, ThemeAccent(c))
	} else {
		btn.Border(Hairline, ThemeBorder(c))
	}
	if capsule.Tooltip != "" {
		btn.Tooltip(capsule.Tooltip)
	}
	if capsule.MaxWidth > 0 {
		btn.MaxWidth(capsule.MaxWidth)
	}
	if open || btn.Hovered() {
		btn.Background(ThemeHover(c))
	} else {
		btn.Background(ThemeSurface(c))
	}
	if capsule.OnClick != nil {
		btn.OnClick(capsule.OnClick)
	}
	btn.Children(func() {
		ui.Row(c.Key(capsule.Key + "-content")).
			Gap(composerCapsuleGap).
			AlignItems(ui.Center).
			Children(func() {
				if capsule.Live {
					renderComposerCapsuleLiveDot(c, capsule)
				} else if capsule.Icon != nil {
					ui.Icon(c, capsule.Icon).
						Size(composerCapsuleIconSize, composerCapsuleIconSize).
						TextColor(ThemeSecondaryText(c))
				}
				ui.Text(c, capsule.Text).
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemePrimaryText(c)).
					SingleLine()
				if capsule.OnDismiss != nil {
					renderComposerCapsuleDismiss(c, capsule)
				}
			})
	})
	if capsule.PopoverOpen != nil && capsule.PopoverBody != nil && capsule.PopoverKey != "" {
		appPopoverAbove(c.Key(capsule.PopoverKey), btn, capsule.PopoverOpen, capsule.PopoverDismiss, capsule.PopoverBody)
	}
}

// renderComposerCapsuleLiveDot 渲染实时状态胶囊前导的脉动圆点：透明度沿用派发占位胶囊的低频三角波，
// 每周期请求 8 次重绘，表达「进行中」；胶囊消失后本函数不再运行，动画也就自然停下。
func renderComposerCapsuleLiveDot(c *ui.Context, capsule composerCapsule) {
	pulse := 1.0
	if !reduceMotion(c) {
		pulse = swarmDispatchPulseAlpha(c.Now())
		c.After(swarmDispatchPulsePeriod / swarmDispatchPulseSteps)
	}
	ui.Box(c.Key(capsule.Key+"-live-dot")).
		Size(composerCapsuleLiveDotSize, composerCapsuleLiveDotSize).
		Radius(RadiusPill).
		Background(ThemeAccent(c).Alpha(float32(pulse)))
}

// renderComposerCapsuleDismiss 渲染胶囊尾随的关闭按钮：点击只关闭胶囊本身。
// 框架按命中链最内层的可交互元素派发点击，所以点关闭不会同时触发胶囊主体的点击。
func renderComposerCapsuleDismiss(c *ui.Context, capsule composerCapsule) {
	closeBtn := ui.ButtonBase(c.Key(capsule.Key+"-dismiss")).
		Label(capsule.DismissLabel).
		Size(composerCapsuleDismissSize, composerCapsuleDismissSize).
		Radius(RadiusPill).
		Background(ui.Transparent).
		OnClick(capsule.OnDismiss)
	composerHoverBg(c, closeBtn, closeBtn.Hovered())
	closeBtn.Children(func() {
		ui.Icon(c, IconX()).
			Size(composerCapsuleIconSize, composerCapsuleIconSize).
			TextColor(ThemeSecondaryText(c))
	})
}

// composerCapsules 收集本帧要显示的胶囊，排序固定为状态类（待办进度、后台任务、继续提示）
// 在左、内容类（运行中改动）在右。
func composerCapsules(c *ui.Context, s *chatState, threadID string, continueKind continuePillKind, lastMessageID string, isRunning, isEditing, hasContent bool) []composerCapsule {
	var capsules []composerCapsule
	if capsule, ok := composerPlanCapsule(c, s, threadID); ok {
		capsules = append(capsules, capsule)
	}
	if capsule, ok := composerBackgroundTasksCapsule(c, s, threadID); ok {
		capsules = append(capsules, capsule)
	}
	if capsule, ok := composerContinueCapsule(s, threadID, continueKind, lastMessageID, isRunning, isEditing, hasContent); ok {
		capsules = append(capsules, capsule)
	}
	if capsule, ok := composerRunningChangesCapsule(s, threadID); ok {
		capsules = append(capsules, capsule)
	}
	return capsules
}

// composerPlanCapsule 返回待办进度胶囊：最新一份待办清单没做完时显示，
// 点击沿用待办弹层（锚定在胶囊上）。没有待办或已全部完成时 ok=false。
func composerPlanCapsule(c *ui.Context, s *chatState, threadID string) (composerCapsule, bool) {
	if s == nil {
		return composerCapsule{}, false
	}
	// isThreadRunning 内部自己加锁，先取运行状态，再在锁内读消息快照。
	running := s.isThreadRunning(threadID)
	s.mu.Lock()
	view := buildPlanPillView(s.messages, running)
	s.mu.Unlock()
	if !view.Visible {
		// 胶囊消失（做完或换会话）时同步收起弹层，避免下次出现时突然弹开。
		s.composerView.planPillOpen = false
		return composerCapsule{}, false
	}
	return composerCapsule{
		Key:      "composer-plan-pill",
		Label:    fmt.Sprintf("待办进度 %d/%d", view.Done, view.Total),
		Tooltip:  "查看待办清单",
		Icon:     IconListTodo(),
		Text:     planPillText(view),
		MaxWidth: planPillMaxWidth,
		OnClick: func() {
			s.composerView.planPillOpen = !s.composerView.planPillOpen
		},
		PopoverKey:     "composer-plan-popover",
		PopoverOpen:    &s.composerView.planPillOpen,
		PopoverDismiss: func() { s.composerView.planPillOpen = false },
		PopoverBody:    func() { renderPlanPopover(c, view) },
	}, true
}

// composerBackgroundTasksCapsule 返回后台任务胶囊：存在 provider 托管的后台任务、回合结束后
// 仍在跑的命令或运行中的子智能体时显示，正文是「最早任务名 · 计时 +N」，点击在胶囊上弹出浮层列出各项。
// 胶囊是实时状态胶囊：强调色描边加脉动圆点，计时每秒刷新；后台项清空后胶囊消失并收起浮层。
func composerBackgroundTasksCapsule(c *ui.Context, s *chatState, threadID string) (composerCapsule, bool) {
	// 浮动面板里的子代理输入框空间太小，不显示后台任务胶囊。
	if s == nil || s.embeddedBee {
		return composerCapsule{}, false
	}
	s.mu.Lock()
	if s.composerView.backgroundTaskThread != threadID {
		s.composerView.backgroundTaskThread = threadID
		s.composerView.backgroundTaskExpanded = ""
		s.composerView.backgroundTaskGeneration++
		s.composerView.backgroundTaskStopped = nil
		s.composerView.backgroundTasksOpen = false
	}
	s.mu.Unlock()
	data := backgroundTasksFor(s, threadID)
	if data.empty() {
		// 胶囊消失（跑完或换会话）时同步收起浮层，避免下次出现时突然弹开。
		closeBackgroundTasks(s)
		return composerCapsule{}, false
	}
	// 测试会传 nil：此时按 time.Now() 算计时，也不请求下一帧。
	now := time.Now()
	if c != nil {
		now = c.Now()
	}
	name, start, ok := data.lead()
	hasStart := ok && !start.IsZero()
	var elapsed time.Duration
	if hasStart {
		elapsed = now.Sub(start)
		if c != nil {
			// 计时每秒刷新一次，与浮层里后台任务行的时长口径一致。
			c.After(time.Second)
		}
	}
	return composerCapsule{
		Key:     "composer-background-tasks-pill",
		Label:   data.title(),
		Tooltip: backgroundTasksTooltipSuffix,
		Live:    true,
		Text:    backgroundTasksStatusText(name, elapsed, hasStart, data.total()-1),
		OnClick: func() {
			if s.composerView.backgroundTasksOpen {
				closeBackgroundTasks(s)
			} else {
				s.composerView.backgroundTasksOpen = true
			}
		},
		PopoverKey:     "composer-background-tasks-popover",
		PopoverOpen:    &s.composerView.backgroundTasksOpen,
		PopoverDismiss: func() { closeBackgroundTasks(s) },
		PopoverBody: func() {
			renderBackgroundTasksPanel(c, s, "background-tasks-panel", data, func() {
				closeBackgroundTasks(s)
			})
		},
	}, true
}

// composerContinueCapsule 返回「上一轮已中断 / 上一轮失败」的继续胶囊。
// 显示条件与原来一致：未运行、未在编辑排队消息、草稿为空；点主体继续上一轮，
// 点关闭则忽略这一次异常结束。
func composerContinueCapsule(s *chatState, threadID string, kind continuePillKind, lastMessageID string, isRunning, isEditing, hasContent bool) (composerCapsule, bool) {
	if s == nil || kind == continuePillNone || isRunning || isEditing || hasContent || threadID == "" {
		return composerCapsule{}, false
	}
	statusText, tooltipText := continuePillLabels(kind)
	text := statusText + " · 继续"
	return composerCapsule{
		Key:     "composer-continue-pill",
		Label:   text,
		Tooltip: tooltipText,
		Icon:    iconPlayFilled,
		Text:    text,
		OnClick: func() { s.continueLastTurn() },
		// 关闭时同时写入忽略记录：这一轮不再提示，出现新消息后自动恢复。
		DismissLabel: "关闭继续提示",
		OnDismiss:    func() { s.dismissContinuePill(threadID, lastMessageID) },
	}, true
}

// composerRunningChangesCapsule 返回运行中回合的改动胶囊：复用回合文件收集与回合级
// diff 缓存算出「N 个文件 +A −D」。文件数为 0 时不显示，统计未就绪时只说「改动中」；
// 回合结束后由时间线里的改动卡接管。点击打开现有的改动面板。
func composerRunningChangesCapsule(s *chatState, threadID string) (composerCapsule, bool) {
	if s == nil || s.embeddedBee || threadID == "" {
		return composerCapsule{}, false
	}
	running, turnIndex := s.runningTurn(threadID)
	if !running || turnIndex <= 0 {
		return composerCapsule{}, false
	}
	summary := turnFileChangeSummaryForTurn(s, threadID, turnIndex)
	if summary.Files == 0 {
		return composerCapsule{}, false
	}
	return composerCapsule{
		Key:     "composer-running-changes-pill",
		Label:   "运行中改动",
		Tooltip: "查看本回合改动",
		Icon:    IconFileDiff(),
		Text:    summary.text(),
		OnClick: func() { s.toggleDiffPanel() },
	}, true
}

// text 返回改动胶囊的正文：统计就绪时为「N 个文件 +A −D」（为 0 的那一侧省略），
// 否则只说「改动中」，不给出不可信的数字。
func (summary turnFileChangeSummary) text() string {
	if !summary.StatsReady {
		return "改动中"
	}
	text := fmt.Sprintf("%d 个文件", summary.Files)
	if summary.Adds > 0 {
		text += fmt.Sprintf(" +%d", summary.Adds)
	}
	if summary.Dels > 0 {
		text += fmt.Sprintf(" −%d", summary.Dels)
	}
	return text
}
