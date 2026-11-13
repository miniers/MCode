package mcodeui

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

const (
	// backgroundTasksShimmerLabel 是后台任务浮层扫光条的无障碍名，供测试与调试定位这条装饰层。
	backgroundTasksShimmerLabel = "后台任务卡片扫光"
	// backgroundTasksTooltipSuffix 是后台任务胶囊 Tooltip 里的口径说明，讲清楚它统计哪些内容。
	backgroundTasksTooltipSuffix = "列出 provider 托管的后台任务、回合结束后仍在运行的命令与运行中的子智能体"
	// backgroundTasksMaxRows 是浮层最多列出的条目数：再多会撑高浮层，其余用一行「还有 N 个」概括。
	backgroundTasksMaxRows = 5
	// backgroundTaskLogMaxLines 限制日志区最多显示的行数。
	backgroundTaskLogMaxLines = 8
	// backgroundTaskLogLineHeight 是日志文字的行高倍数。
	backgroundTaskLogLineHeight = 1.4
	// backgroundWaitShimmerLabel 是时间线等待条扫光的无障碍名，供测试与调试定位这条装饰层。
	backgroundWaitShimmerLabel = "等待后台任务扫光"
)

// backgroundTasksItem 是后台任务浮层当前要展示的内容：provider 托管、仍在运行的工具任务，
// 回合已结束但仍在跑的命令，以及 Swarm 里仍在运行中的子智能体。它不缓存在任何条目上，
// 每次渲染都按当前消息与 Swarm 状态现算，状态一变立刻反映。
type backgroundTasksItem struct {
	threadID string
	leadID   string
	tasks    []Message
	commands []Message
	bees     []SwarmBee
}

// taskCount 返回 provider 托管的后台任务数。
func (it backgroundTasksItem) taskCount() int { return len(it.tasks) }

// commandCount 返回后台命令数。
func (it backgroundTasksItem) commandCount() int { return len(it.commands) }

// beeCount 返回运行中的子智能体数。
func (it backgroundTasksItem) beeCount() int { return len(it.bees) }

// total 返回三类后台项的总数，浮层标题、胶囊正文与「还有 N 个」都用它。
func (it backgroundTasksItem) total() int {
	return len(it.tasks) + len(it.commands) + len(it.bees)
}

// empty 判断三类是否都为空：都为空时胶囊不显示，浮层也不构建。
func (it backgroundTasksItem) empty() bool { return it.total() == 0 }

// title 返回浮层与胶囊标题，如「后台任务 · 2 个运行中」；没有后台项时返回空串。
func (it backgroundTasksItem) title() string {
	return backgroundTasksTitle(it.taskCount(), it.commandCount(), it.beeCount())
}

// lead 返回三类后台项里开始时间最早的一项及其开始时间：provider 托管任务与后台命令取 CreatedAt，
// 子智能体取 StartedAt。没有时间戳的项排在最后；全都没有时间时取第一项，start 为零值。
// ok 表示是否存在后台项，name 与浮层每行显示的标题一致。
func (it backgroundTasksItem) lead() (name string, start time.Time, ok bool) {
	var (
		leadName  string
		leadStart time.Time
		leadKnown bool
		first     string
		haveAny   bool
	)
	consider := func(candidate string, at time.Time, known bool) {
		if !haveAny {
			first, haveAny = candidate, true
		}
		if !known {
			return
		}
		if !leadKnown || at.Before(leadStart) {
			leadName, leadStart, leadKnown = candidate, at, true
		}
	}
	for _, m := range it.tasks {
		at, known := parseRFC3339Time(m.CreatedAt)
		consider(backgroundTaskDisplayTitle(m), at, known)
	}
	for _, m := range it.commands {
		at, known := parseRFC3339Time(m.CreatedAt)
		consider(backgroundTaskDisplayTitle(m), at, known)
	}
	for _, h := range it.bees {
		consider(backgroundBeeDisplayTitle(h), h.StartedAt, !h.StartedAt.IsZero())
	}
	if leadKnown {
		return leadName, leadStart, true
	}
	return first, time.Time{}, haveAny
}

// backgroundTaskDisplayTitle 返回后台任务或后台命令在浮层与胶囊里显示的短标题，
// 与浮层每一行的标题保持同一套取法；标题为空时退回「后台任务」。
func backgroundTaskDisplayTitle(m Message) string {
	verb, target, _ := toolCallTitle(m)
	verb = toolCallVerb(verb, m)
	short := truncateRunes(toolChipShortTitle(m, verb, target), 40)
	if short == "" {
		return "后台任务"
	}
	return short
}

// backgroundBeeDisplayTitle 返回运行中子智能体在浮层与胶囊里显示的名字。
func backgroundBeeDisplayTitle(h SwarmBee) string { return swarmCapsuleName(h) }

// backgroundTasksStatusText 拼出后台任务胶囊的实时状态文案，如「运行 ui 包全量测试 · 36s +1」：
// 时长用 formatToolRunDuration；hasStart 为假时省略时长，more 为 0 时省略「+N」；
// 名字超过 24 个字符（按 rune 计）时截断并加「…」。
func backgroundTasksStatusText(name string, elapsed time.Duration, hasStart bool, more int) string {
	name = truncateRunes(strings.TrimSpace(name), 24)
	if name == "" {
		name = "后台任务"
	}
	text := name
	if hasStart {
		text += " · " + formatToolRunDuration(elapsed)
	}
	if more > 0 {
		text += fmt.Sprintf(" +%d", more)
	}
	return text
}

// backgroundProviderTasks 返回 provider 托管、仍在运行的工具消息：
// 不看工具类别、不看会话是否在跑、不按回合过滤，只要状态是 background 就计入。
func backgroundProviderTasks(messages []Message) []Message {
	var out []Message
	for _, m := range messages {
		if isToolBackground(m) {
			out = append(out, m)
		}
	}
	return out
}

// backgroundRunningCommands 返回可以算作「后台」的命令行工具消息：
// 命令本身仍在运行，且已经不属于当前正在写的回合（TurnIndex 小于当前最大回合），
// 或会话已经整体不在运行。回合还在跑的命令交给运行中工具组显示，不在两处重复。
// provider 托管的后台任务不算命令（见 toolIsRunningCommand），不会在这里重复。
// threadRunning 为假时 runningTurn 不参与判断；调用方在会话运行时应传入当前最大回合号。
func backgroundRunningCommands(messages []Message, threadRunning bool, runningTurn int) []Message {
	var out []Message
	for _, m := range messages {
		if !toolIsRunningCommand(m) {
			continue
		}
		if threadRunning && m.TurnIndex >= runningTurn {
			continue
		}
		out = append(out, m)
	}
	return out
}

// backgroundRunningBees 过滤出仍在运行中的子智能体。排队（queued）不算「在动」，不计入胶囊数字。
func backgroundRunningBees(bees []SwarmBee) []SwarmBee {
	var out []SwarmBee
	for _, h := range bees {
		if h.Status == "running" {
			out = append(out, h)
		}
	}
	return out
}

// backgroundTasksTitle 拼出后台任务浮层与胶囊标题：provider 托管的后台任务、回合结束后仍在跑的命令
// 与运行中的子智能体合计个数；总数为 0 时返回空串。
func backgroundTasksTitle(tasks, commands, bees int) string {
	total := tasks + commands + bees
	if total <= 0 {
		return ""
	}
	return fmt.Sprintf("后台任务 · %d 个运行中", total)
}

// backgroundTasksFor 按会话当前消息与 Swarm 状态算出后台任务浮层内容。
// runningTurn 取当前最大回合号：回合运行中时，属于这一回合的命令仍在运行中工具组里，
// 不算后台。provider 托管的后台任务（Bash run_in_background 等）按 ToolStatus=background
// 直接识别，不论会话是否在跑都计入。
func backgroundTasksFor(s *chatState, threadID string) backgroundTasksItem {
	if s == nil {
		return backgroundTasksItem{}
	}
	if threadID == "" {
		s.mu.Lock()
		threadID = s.selectedThreadID
		s.mu.Unlock()
	}
	threadRunning := s.isThreadRunning(threadID)
	runningTurn := 0
	if threadRunning {
		runningTurn = s.maxMessagesTurn()
	}
	s.mu.Lock()
	messages := s.messages
	s.mu.Unlock()

	data := backgroundTasksItem{
		threadID: threadID,
		tasks:    backgroundProviderTasks(messages),
		commands: backgroundRunningCommands(messages, threadRunning, runningTurn),
	}
	if view := swarmStateViewFor(s); view != nil {
		data.leadID = view.leadID
		data.bees = backgroundRunningBees(view.active)
	}
	return data
}

// shouldShowBackgroundWait 判断是否在时间线末尾显示等待后台任务条：
// 会话已不在运行（回合结束）且仍有后台项时才显示；运行中的后台项已由运行中工具组表达。
func shouldShowBackgroundWait(running bool, data backgroundTasksItem) bool {
	return !running && !data.empty()
}

// renderBackgroundWaitBar 渲染时间线末尾的等待后台任务条：一行扫光文字（时钟、任务数、
// 最早任务名与实时计时）。
// 整行可点，点击打开输入区胶囊上已有的后台任务浮层，不新建浮层状态。没有后台项时
// 返回空元素；有开始时间时每秒请求重绘，让计时逐秒走动。
func renderBackgroundWaitBar(c *ui.Context, s *chatState, key string) ui.Element {
	data := backgroundTasksFor(s, "")
	if data.empty() {
		return ui.Element{}
	}
	if key == "" {
		key = backgroundWaitKey("")
	}
	name, start, ok := data.lead()
	hasStart := ok && !start.IsZero()
	var elapsed time.Duration
	if hasStart {
		elapsed = c.Now().Sub(start)
		c.After(time.Second)
	}
	// 总数已经写在前面，状态文案不再带「+N」，避免重复计数。
	text := fmt.Sprintf("等待 %d 个后台任务 · %s", data.total(),
		backgroundTasksStatusText(name, elapsed, hasStart, 0))
	return ui.Column(c.Key(key)).FillWidth().Gap(Space0).Children(func() {
		btn := ui.ButtonBase(c.Key(key+"-button")).
			FillWidth().
			Padding(Space0, 0).
			Radius(RadiusMD).
			AlignItems(ui.Center).
			Justify(ui.Start).
			Cursor(ui.CursorPointer).
			Label("等待后台任务").
			Tooltip("查看后台任务")
		if btn.Hovered() {
			btn.Background(ThemeHover(c).Alpha(0.5))
		}
		if btn.Clicked() {
			// 复用输入区胶囊的浮层状态：单击只负责打开，浮层的关闭仍由胶囊负责。
			s.composerView.backgroundTasksOpen = true
			s.invalidate()
		}
		btn.Children(func() {
			renderShimmerTitle(c, key+"/shimmer", backgroundWaitShimmerLabel, true, func() ui.Element {
				return ui.Row(c.Key(key + "/line")).Gap(Space2).AlignItems(ui.Center).Children(func() {
					// 时钟图标与后台工具胶囊保持同一套语义：还在等它跑完。
					ui.Icon(c, IconClock()).Size(11, 11).TextColor(ThemeAccent(c))
					ui.Text(c, text).
						FontSize(scaledFont(TypeFootnote)).
						TextColor(ThemeSecondaryText(c)).
						SingleLine()
				})
			})
		})
	})
}

// renderBackgroundTasksPanel 渲染输入区后台任务浮层的表头、分隔线和条目列表：
// 表头是状态点、「后台任务」标题与实时总数胶囊；条目列表始终展开，逐条列出后台任务、
// 回合结束后仍在跑的命令与运行中的子智能体，超过 backgroundTasksMaxRows 条时只列前几条，
// 其余用一行「还有 N 个」概括。
// 卡片外观由 popoverCard 提供（appPopover 不画底色），面板只约束宽度。没有后台项时返回空元素。
func renderBackgroundTasksPanel(c *ui.Context, s *chatState, key string, data backgroundTasksItem, onNavigate func()) ui.Element {
	title := data.title()
	if title == "" {
		return ui.Element{}
	}
	if key == "" {
		key = "background-tasks-panel"
	}
	return popoverCard(c, ui.Column(c.Key(key)).
		Width(360).
		Gap(0).
		Label(title).
		Clip()).
		Children(func() {
			ui.Row(c.Key(key+"-header")).
				FillWidth().
				Padding(Space3, Space4).
				Gap(Space3).
				AlignItems(ui.Center).
				Justify(ui.Start).
				Children(func() {
					// 浮层存在就说明至少有一项在跑，状态点与标题扫光常驻；减少动效时只显示静态标题。
					ui.Box(c.Key(key+"/dot")).Size(6, 6).Radius(RadiusXS).Background(ThemeAccent(c))
					renderShimmerTitle(c, key+"/shimmer", backgroundTasksShimmerLabel, true, func() ui.Element {
						return ui.Text(c, "后台任务").FontSize(scaledFont(TypeFootnote)).FontWeight(600).TextColor(ThemePrimaryText(c))
					})
					ui.Row(c.Key(key+"/count")).
						Padding(Space0, Space2).
						Radius(RadiusMD).
						Background(ThemeHover(c).Alpha(0.6)).
						Children(func() {
							ui.Text(c, fmt.Sprintf("%d 个运行中", data.total())).
								FontSize(scaledFont(TypeCaption)).
								TextColor(ThemeSecondaryText(c)).
								SingleLine()
						})
				})
			ui.Box(c.Key(key + "/divider")).FillWidth().Height(Hairline).Background(ThemeBorder(c).Alpha(0.6))
			ui.Column(c.Key(key+"/body")).FillWidth().Padding(Space1, Space2, Space2, Space2).Gap(0).Children(func() {
				shown := 0
				for _, m := range data.tasks {
					if shown >= backgroundTasksMaxRows {
						break
					}
					renderBackgroundTaskRow(c, s, key, data.threadID, m, true)
					shown++
				}
				for _, m := range data.commands {
					if shown >= backgroundTasksMaxRows {
						break
					}
					renderBackgroundTaskRow(c, s, key, data.threadID, m, false)
					shown++
				}
				for _, h := range data.bees {
					if shown >= backgroundTasksMaxRows {
						break
					}
					renderBackgroundBeeRow(c, s, key, shown, data.leadID, h, onNavigate)
					shown++
				}
				if rest := data.total() - shown; rest > 0 {
					ui.Row(c.Key(key+"/more")).Padding(Space1, Space3).Children(func() {
						ui.Text(c, fmt.Sprintf("还有 %d 个", rest)).
							FontSize(scaledFont(TypeCaption)).
							TextColor(ThemeSecondaryText(c))
					})
				}
			})
		})
}

// closeBackgroundTasks 收起浮层并使未完成的日志请求失效。
func closeBackgroundTasks(s *chatState) {
	s.mu.Lock()
	s.composerView.backgroundTasksOpen = false
	s.composerView.backgroundTaskExpanded = ""
	s.composerView.backgroundTaskGeneration++
	s.mu.Unlock()
}

// requestBackgroundTaskLog 在展开期间按秒请求日志，同一时间只允许一个请求。
func requestBackgroundTaskLog(c *ui.Context, s *chatState, threadID, messageID string) {
	s.mu.Lock()
	v := &s.composerView
	if !v.backgroundTasksOpen || v.backgroundTaskExpanded != messageID || v.backgroundTaskLogPending || c.Now().Before(v.backgroundTaskLogNext) || s.client == nil {
		s.mu.Unlock()
		return
	}
	v.backgroundTaskLogPending = true
	v.backgroundTaskLogNext = c.Now().Add(time.Second)
	generation := v.backgroundTaskGeneration
	client := s.client
	s.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), killCommandTimeout)
		defer cancel()
		text, available, err := client.BackgroundTaskLog(ctx, threadID, messageID)
		s.mu.Lock()
		v := &s.composerView
		v.backgroundTaskLogPending = false
		if s.selectedThreadID == threadID && v.backgroundTaskGeneration == generation && v.backgroundTaskExpanded == messageID && err == nil {
			v.backgroundTaskLogID = messageID
			v.backgroundTaskLog = ""
			if available {
				v.backgroundTaskLog = text
			}
		}
		s.mu.Unlock()
		s.invalidate()
	}()
}

// stopBackgroundTask 异步停止指定任务，失败时保留条目并提示错误。
func stopBackgroundTask(s *chatState, threadID, messageID string) {
	s.mu.Lock()
	v := &s.composerView
	if v.backgroundTaskStopping[messageID] || s.client == nil {
		s.mu.Unlock()
		return
	}
	if v.backgroundTaskStopping == nil {
		v.backgroundTaskStopping = make(map[string]bool)
	}
	v.backgroundTaskStopping[messageID] = true
	client := s.client
	s.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), killCommandTimeout)
		defer cancel()
		stopped, err := client.StopBackgroundTask(ctx, threadID, messageID)
		s.mu.Lock()
		v := &s.composerView
		delete(v.backgroundTaskStopping, messageID)
		if s.selectedThreadID == threadID {
			if err != nil {
				s.toastMessage = "停止后台任务失败: " + err.Error()
			} else if !stopped {
				s.toastMessage = "未能停止后台任务"
			} else {
				if v.backgroundTaskStopped == nil {
					v.backgroundTaskStopped = make(map[string]time.Time)
				}
				v.backgroundTaskStopped[messageID] = time.Now()
				if v.backgroundTaskExpanded == messageID {
					v.backgroundTaskExpanded = ""
					v.backgroundTaskGeneration++
				}
			}
		}
		s.mu.Unlock()
		s.invalidate()
	}()
}

// renderBackgroundTaskRow 渲染可展开的任务或命令，只有任务可以停止。
func renderBackgroundTaskRow(c *ui.Context, s *chatState, key, threadID string, m Message, canStop bool) {
	rowKey := key + "/task-" + m.ID
	s.mu.Lock()
	v := &s.composerView
	expanded := v.backgroundTaskExpanded == m.ID
	if expanded {
		v.backgroundTaskScrollOwner = m.ID
	}
	scrollOwner := v.backgroundTaskScrollOwner == m.ID && (expanded || v.backgroundTaskExpanded == "")
	log := ""
	if v.backgroundTaskLogID == m.ID {
		log = v.backgroundTaskLog
	}
	stoppedAt, stopped := v.backgroundTaskStopped[m.ID]
	stopping := v.backgroundTaskStopping[m.ID]
	s.mu.Unlock()
	if stopped && (reduceMotion(c) || c.Now().Sub(stoppedAt) >= DurFast) {
		return
	}
	if stopped {
		c.After(DurFast)
	}
	if expanded {
		c.After(time.Second)
		requestBackgroundTaskLog(c, s, threadID, m.ID)
	}
	row := ui.Column(c.Key(rowKey)).FillWidth().Clip()
	target := float32(1)
	if stopped {
		target = 0
	}
	fade := row.AnimateTo("stop", target, ui.Animation{Duration: animateDuration(c, DurFast), Ease: ui.EaseOut})
	row.Opacity(fade)
	row.Children(func() {
		ui.Row(c.Key(rowKey + "/header")).FillWidth().Height(ControlMD).AlignItems(ui.Center).Children(func() {
			btn := ui.ButtonBase(c.Key(rowKey+"/button")).Grow(1).Shrink(1).Height(ControlMD).
				Padding(0, Space3).Radius(RadiusMD).Gap(Space3).AlignItems(ui.Center).
				Justify(ui.Start).Cursor(ui.CursorPointer).Label(backgroundTaskDisplayTitle(m))
			if btn.Hovered() {
				btn.Background(ThemeHover(c).Alpha(0.5))
			}
			if btn.Clicked() {
				s.mu.Lock()
				v := &s.composerView
				if v.backgroundTaskExpanded == m.ID {
					v.backgroundTaskExpanded = ""
				} else {
					v.backgroundTaskExpanded = m.ID
					v.backgroundTaskScrollOwner = m.ID
				}
				v.backgroundTaskGeneration++
				v.backgroundTaskLogNext = time.Time{}
				s.mu.Unlock()
				s.invalidate()
			}
			btn.Children(func() {
				ui.Row(c.Key(rowKey+"/icon")).Size(18, 18).Radius(RadiusXS).
					Background(ThemeHover(c).Alpha(0.6)).Justify(ui.Center).AlignItems(ui.Center).
					Children(func() { ui.Icon(c, IconTerminal()).Size(10, 10).TextColor(ThemeSecondaryText(c)) })
				ui.Row(c.Key(rowKey + "/title")).Grow(1).Shrink(1).Clip().AlignItems(ui.Center).Children(func() {
					ui.Text(c, backgroundTaskDisplayTitle(m)).FontSize(scaledFont(TypeFootnote)).FontWeight(500).TextColor(ThemePrimaryText(c)).SingleLine()
				})
				if duration, ok := toolBackgroundRunDuration(c, m); ok {
					ui.Text(c, duration).FontSize(scaledFont(TypeCaption)).TextColor(ThemeSecondaryText(c)).SingleLine()
				}
			})
			if canStop {
				stop := ui.ButtonBase(c.Key(rowKey+"/stop")).Size(18, 18).Label("停止").Tooltip("停止").Cursor(ui.CursorPointer)
				scale := motionPressScale(c, stop, "button-press-scale", !stopping && stop.Pressed())
				stop.Scale(scale, scale)
				if stopping {
					stop.Opacity(0.5)
				}
				if stop.Clicked() && !stopping {
					stopBackgroundTask(s, threadID, m.ID)
				}
				stop.Children(func() { ui.Icon(c, IconStopFilled()).Size(10, 10).TextColor(ThemeSecondaryText(c)) })
			}
		})
		if log == "" {
			log = "暂无输出"
		}
		lines := float32(strings.Count(log, "\n") + 1)
		if lines > backgroundTaskLogMaxLines {
			lines = backgroundTaskLogMaxLines
		}
		height := float32(0)
		if expanded {
			height = lines*scaledFont(TypeCaption)*backgroundTaskLogLineHeight + Space2
		}
		wrap := ui.Column(c.Key(rowKey + "/log-wrap")).FillWidth().Clip()
		animatedHeight := wrap.AnimateTo("expand", height, ui.Animation{Duration: animateDuration(c, DurBase), Ease: ui.EaseOut})
		wrap.Height(animatedHeight)
		wrap.Children(func() {
			if scrollOwner && (expanded || animatedHeight > 0.5) {
				if v.backgroundTaskScrolledLog != log {
					v.backgroundTaskScroll.Y = math.MaxFloat32
					v.backgroundTaskScrolledLog = log
				}
				ui.Scroll(c.Key(rowKey + "/log")).FillWidth().Label("后台任务日志 " + m.ID).MaxHeight(backgroundTaskLogMaxLines * scaledFont(TypeCaption) * backgroundTaskLogLineHeight).
					TrackScroll(&v.backgroundTaskScroll).Children(func() {
					ui.Text(c, log).Font(terminalFontFamily()).FontSize(scaledFont(TypeCaption)).LineHeight(backgroundTaskLogLineHeight).TextColor(ThemeSecondaryText(c)).Selectable()
				})
			}
		})
	})
	if stopped {
		// 停止时展开已收起，行高就是 ControlMD；淡出同时把行高收到 0，避免动画结束后删行时列表跳一下。
		// 插值结果先存到变量再交给 Height，与 motionUnfoldBody 的收起写法一致，避免令牌算术被计入设计字面量。
		fadeHeight := fade * ControlMD
		row.Height(fadeHeight)
	}
}

// renderBackgroundBeeRow 渲染后台任务浮层里的一个运行中子智能体：人设图标、名字、当前活动、右侧时长与右箭头。
// 整行左对齐并固定行高，状态文字在可伸缩容器里紧跟名字，长活动不会挤掉右侧元素。
// 整行可点，复用既有浮窗 openSwarmFloatForBee；点完调用 onNavigate（不为 nil 时）关闭后台任务浮层，
// 避免两层浮层叠在一起。按钮 Label 带名字，保证同页可唯一定位。
func renderBackgroundBeeRow(c *ui.Context, s *chatState, key string, index int, leadID string, h SwarmBee, onNavigate func()) {
	name := backgroundBeeDisplayTitle(h)
	status := strings.TrimSpace(h.Activity)
	if status == "" {
		status = "运行中"
	}
	status = truncateRunes(status, 40)

	beeKey := fmt.Sprintf("%s/bee-%d", key, index)
	btn := ui.ButtonBase(c.Key(beeKey)).
		FillWidth().
		Height(ControlMD).
		Padding(0, Space3).
		Radius(RadiusMD).
		Gap(Space3).
		AlignItems(ui.Center).
		Justify(ui.Start).
		Cursor(ui.CursorPointer).
		Label(backgroundTasksBeeLabel(name)).
		Tooltip(name + " · " + status)
	if btn.Hovered() {
		btn.Background(ThemeHover(c).Alpha(0.5))
	}
	if btn.Clicked() {
		openSwarmFloatForBee(leadID, h.ThreadID)
		s.invalidate()
		if onNavigate != nil {
			onNavigate()
		}
	}
	btn.Children(func() {
		renderSwarmPersonaIcon(c, beeKey+"-avatar", name, 18)
		ui.Text(c, name).FontSize(scaledFont(TypeFootnote)).FontWeight(600).TextColor(ThemePrimaryText(c)).SingleLine()
		ui.Row(c.Key(beeKey + "/status")).Grow(1).Shrink(1).Clip().AlignItems(ui.Center).Justify(ui.Start).Children(func() {
			ui.Text(c, status).FontSize(scaledFont(TypeFootnote)).TextColor(ThemeSecondaryText(c)).SingleLine()
		})
		if duration := formatSwarmShortDuration(h.StartedAt, nil); duration != "" {
			// 按秒请求下一帧，让子智能体时长与后台任务一样每秒刷新：
			// 减少动效时没有扫光也不会停住（做法同 timeline_rows.go 中 toolBackgroundRunDuration 的 c.After(time.Second)）。
			c.After(time.Second)
			ui.Text(c, duration).FontSize(scaledFont(TypeCaption)).TextColor(ThemeSecondaryText(c)).SingleLine()
		}
		ui.Icon(c, IconChevronRight()).Size(9, 9).TextColor(ThemeSecondaryText(c))
	})
}

// backgroundTasksBeeLabel 返回子智能体行的按钮 Label：带上名字，屏幕阅读器与 Tester 都能唯一定位。
// Swarm 在派发时会给重名子线程加序号后缀（如「Birch 2」），同一会话内名字不会重复。
func backgroundTasksBeeLabel(name string) string {
	return "打开子智能体 " + name + " 的浮窗"
}

// renderShimmerTitle 在标题上扫过一道浅色高光，提示内容仍在运行。running 为假或用户要求减少动效时
// 直接调用 fn，不注册循环；运行中才由框架逐帧推进这一个元素，不轮询、不重建整条时间线。
// key 与 bandLabel 由调用方给出：前者保证同页多个微光元素互不冲突，后者供测试定位扫光条。
func renderShimmerTitle(c *ui.Context, key, bandLabel string, running bool, fn func() ui.Element) ui.Element {
	if !running || reduceMotion(c) {
		return fn()
	}
	row := ui.Row(c.Key(key)).Clip()
	row.Children(func() {
		title := fn()
		width := title.LayoutBounds().W
		if width <= 0 {
			return
		}
		// 一个 motionSweep 周期从左走到右；只在运行中出现。
		k := motionLoop(c, title, key+"/shimmer-pos", motionSweep, ui.Linear)
		x := k*(width+2*toolVerbShimmerWidth) - toolVerbShimmerWidth
		// 三段渐变：中间是扫光，两头透明，扫过时像一道高光从文字上滑过。
		tint := ThemeAccent(c).Alpha(0.22)
		ui.Box(c.Key(key + "/shimmer-band")).
			Label(bandLabel).
			Absolute().
			Top(0).
			Left(x).
			Width(toolVerbShimmerWidth).
			Height(toolChipHeight).
			LinearGradient(ui.LinearGradient{Angle: 90, Stops: []ui.GradientStop{
				{Color: tint.Alpha(0), At: 0},
				{Color: tint, At: 0.5},
				{Color: tint.Alpha(0), At: 1},
			}})
	})
	return row
}
