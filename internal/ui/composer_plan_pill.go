package mcodeui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

// 待办进度胶囊：最新一份待办清单（TodoWrite / update_plan 工具）还没做完时，
// 在输入框上方常驻一枚小胶囊，显示「完成数/总数 · 进行中那一项」，
// 点击用弹出层展开完整清单（每项带状态图标，超出高度可滚动）。

const (
	// planPillMaxWidth 是胶囊的最大宽度：正文单行截断，超长待办不会把输入框挤宽。
	planPillMaxWidth float32 = 420
	// planPillTextMaxRunes 是胶囊里待办正文保留的字符数，避免无障碍 Label 过长。
	planPillTextMaxRunes = 60
	// planPopoverMaxHeight 是弹出清单列表的最大高度，超出部分在弹层内滚动。
	planPopoverMaxHeight float32 = 240
	// planPillBadgeSize 是进度环的边长。
	planPillBadgeSize float32 = 16
	// planPopoverMinWidth 是弹出清单的最小宽度，保证长待办正文有足够单行空间。
	planPopoverMinWidth float32 = 260
	// planPopoverMaxWidth 是弹出清单的最大宽度，超出后正文换行。
	planPopoverMaxWidth float32 = 380
)

// planPillView 是一帧待办进度胶囊要展示的内容。
type planPillView struct {
	Visible bool       // 是否显示胶囊
	Items   []todoItem // 最新一份待办清单
	Done    int        // 已完成项数
	Total   int        // 总项数
	Current string     // 进行中那一项的正文（没有进行中项时取第一项未完成的）
	Running bool       // 会话是否正在运行，决定进度环是否呼吸
}

// fraction 返回完成比例，用于画进度环。
func (v planPillView) fraction() float64 {
	if v.Total <= 0 {
		return 0
	}
	return float64(v.Done) / float64(v.Total)
}

// isTodoToolMessage 判断一条消息是否为工具调用消息（待办清单只会出现在工具消息里）。
func isTodoToolMessage(m Message) bool {
	return m.Role == "tool" || m.Kind == "tool"
}

var claudeTaskIDPattern = regexp.MustCompile(`Task #([0-9]+)`)

type claudeTaskAccumulator struct {
	items   []todoItem
	ids     []string
	created int
}

func (a *claudeTaskAccumulator) add(m Message) bool {
	if !isTodoToolMessage(m) {
		return false
	}
	create := strings.EqualFold(m.ToolName, "TaskCreate")
	if !create && !strings.EqualFold(m.ToolName, "TaskUpdate") {
		return false
	}
	var input struct {
		TaskID  string  `json:"taskId"`
		Subject *string `json:"subject"`
		Status  string  `json:"status"`
	}
	if json.Unmarshal([]byte(m.ToolInput), &input) != nil {
		return true
	}
	if create {
		a.created++
		id := strconv.Itoa(a.created)
		if _, output, ok := strings.Cut(m.Text, "\n"); ok {
			if match := claudeTaskIDPattern.FindStringSubmatch(output); match != nil {
				id = match[1]
			}
		}
		text := ""
		if input.Subject != nil {
			text = *input.Subject
		}
		a.ids = append(a.ids, id)
		a.items = append(a.items, todoItem{Text: text, Status: "pending"})
		return true
	}
	for j, id := range a.ids {
		if id != input.TaskID {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(input.Status), "deleted") {
			a.ids = append(a.ids[:j], a.ids[j+1:]...)
			a.items = append(a.items[:j], a.items[j+1:]...)
			break
		}
		if input.Subject != nil {
			a.items[j].Text = *input.Subject
		}
		if input.Status != "" {
			a.items[j].Status = normalizeTodoStatus(input.Status)
		}
		break
	}
	return true
}

// claudeTaskPlan 按创建顺序聚合 Claude 的增量任务消息。
func claudeTaskPlan(msgs []Message) ([]todoItem, int) {
	var tasks claudeTaskAccumulator
	last := -1
	for i, m := range msgs {
		if tasks.add(m) {
			last = i
		}
	}
	return tasks.items, last
}

// collapseTodoMessages 保留每轮各类待办的最后一条，Claude 任务跨轮累计。
func collapseTodoMessages(messages []Message) []Message {
	result := make([]Message, 0, len(messages))
	keep := make([]bool, 0, len(messages))
	var tasks claudeTaskAccumulator
	lastPlan, lastTask := -1, -1
	flushTask := func() {
		if lastTask < 0 {
			return
		}
		if len(tasks.items) == 0 {
			keep[lastTask] = false
			return
		}
		todos := make([]map[string]string, 0, len(tasks.items))
		for _, item := range tasks.items {
			todos = append(todos, map[string]string{"content": item.Text, "status": item.Status})
		}
		input, _ := json.Marshal(map[string]any{"todos": todos})
		result[lastTask].ToolInput = string(input)
	}
	for _, m := range messages {
		if m.Role == "user" {
			flushTask()
			lastPlan, lastTask = -1, -1
		}
		result = append(result, m)
		keep = append(keep, true)
		idx := len(result) - 1
		if tasks.add(m) {
			if lastTask >= 0 {
				keep[lastTask] = false
			}
			lastTask = idx
		} else if isTodoToolMessage(m) {
			if _, ok := todoItemsFromMessage(m); ok {
				if lastPlan >= 0 {
					keep[lastPlan] = false
				}
				lastPlan = idx
			}
		}
	}
	flushTask()
	filtered := make([]Message, 0, len(result))
	for i, m := range result {
		if keep[i] {
			filtered = append(filtered, m)
		}
	}
	return filtered
}

// latestTodoPlan 返回最新的整份清单或增量任务聚合结果及其下标。
func latestTodoPlan(msgs []Message) ([]todoItem, int) {
	tasks, taskIndex := claudeTaskPlan(msgs)
	for i := len(msgs) - 1; i > taskIndex; i-- {
		m := msgs[i]
		if !isTodoToolMessage(m) {
			continue
		}
		if items, ok := todoItemsFromMessage(m); ok {
			return items, i
		}
	}
	return tasks, taskIndex
}

// todoPlanProgress 统计待办清单的已完成数与总项数。
func todoPlanProgress(items []todoItem) (done, total int) {
	for _, it := range items {
		if it.Status == "completed" {
			done++
		}
	}
	return done, len(items)
}

// todoPlanCurrentText 返回当前进行中那一项的正文；没有进行中项时取第一项未完成的，
// 全部完成时返回空文本。
func todoPlanCurrentText(items []todoItem) string {
	for _, it := range items {
		if it.Status == "in_progress" {
			return it.Text
		}
	}
	for _, it := range items {
		if it.Status != "completed" {
			return it.Text
		}
	}
	return ""
}

// lastUserMessageIndex 返回最后一条用户消息的下标，没有用户消息时返回 -1。
func lastUserMessageIndex(msgs []Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return i
		}
	}
	return -1
}

// shouldShowPlanPill 决定是否显示待办进度胶囊：有待办、未全部完成，
// 而且会话正在运行，或最新待办出现在最后一条用户消息之后（这一轮的新进度）。
// 全部完成或没有待办时都不显示。
func shouldShowPlanPill(msgs []Message, running bool) bool {
	items, idx := latestTodoPlan(msgs)
	if len(items) == 0 || idx < 0 {
		return false
	}
	done, total := todoPlanProgress(items)
	if done >= total {
		return false
	}
	if running {
		return true
	}
	return idx > lastUserMessageIndex(msgs)
}

// buildPlanPillView 汇总一帧胶囊要展示的内容。
func buildPlanPillView(msgs []Message, running bool) planPillView {
	if !shouldShowPlanPill(msgs, running) {
		return planPillView{}
	}
	items, _ := latestTodoPlan(msgs)
	done, total := todoPlanProgress(items)
	return planPillView{
		Visible: true,
		Items:   items,
		Done:    done,
		Total:   total,
		Current: todoPlanCurrentText(items),
		Running: running,
	}
}

// planPillText 拼出胶囊正文：「N/M · 进行中那一项」。
func planPillText(v planPillView) string {
	text := fmt.Sprintf("%d/%d", v.Done, v.Total)
	if current := truncateRunes(v.Current, planPillTextMaxRunes); current != "" {
		text += " · " + current
	}
	return text
}

// renderComposerPlanPill 在输入框上方渲染待办进度胶囊（没有可显示的待办时整行不渲染）。
func renderComposerPlanPill(c *ui.Context, s *chatState, threadID string) {
	// 浮动面板里的子代理输入框空间太小，不显示待办胶囊。
	if s == nil || s.embeddedBee {
		return
	}
	// isThreadRunning 内部自己加锁，先取运行状态，再在锁内读消息快照。
	running := s.isThreadRunning(threadID)
	s.mu.Lock()
	view := buildPlanPillView(s.messages, running)
	s.mu.Unlock()
	if !view.Visible {
		// 胶囊消失时顺带收起弹层，避免下次出现时突然弹开。
		s.composerView.planPillOpen = false
		return
	}

	text := planPillText(view)
	pill := ui.ButtonBase(c.Key("composer-plan-pill")).
		Label(fmt.Sprintf("待办进度 %d/%d", view.Done, view.Total)).
		Tooltip("查看待办清单").
		Height(ControlMD).
		Radius(RadiusPill).
		Padding(Space1, Space3).
		MaxWidth(planPillMaxWidth).
		Border(Hairline, ThemeBorder(c)).
		OnClick(func() {
			s.composerView.planPillOpen = !s.composerView.planPillOpen
		})
	// 与排队、改动胶囊同一套浅底胶囊的悬停/展开反馈。
	composerHoverBg(c, pill, pill.Hovered() || s.composerView.planPillOpen)

	scale := motionPressScale(c, pill, "button-press-scale", true && pill.Pressed())
	pill.Scale(scale, scale)
	pill.Children(func() {
		ui.Row(c.Key("composer-plan-pill-content")).
			Gap(Space2).
			AlignItems(ui.Center).
			Children(func() {
				// 进度环复用输入框底栏上下文用量表的圆弧画法（stroke-dasharray 的圆环），
				// 只是尺寸缩到 16pt；已完成比例直接决定圆环长度。
				ring := ui.Image(c, makeContextMeterSVG(isDark(c), view.fraction(), false)).
					Size(planPillBadgeSize, planPillBadgeSize).
					Fit(ui.Contain)
				if view.Running {
					// 运行中才呼吸：沿用派发占位胶囊的低频三角波，每周期请求 8 次重绘。
					if !reduceMotion(c) {
						ring.Opacity(float32(swarmDispatchPulseAlpha(c.Now())))
						c.After(swarmDispatchPulsePeriod / swarmDispatchPulseSteps)
					} else {
						ring.Opacity(1)
					}
				} else {
					ring.Opacity(0.9)
				}
				ui.Text(c, text).
					FontSize(scaledFont(TypeCaption)).
					TextColor(ThemePrimaryText(c)).
					SingleLine()
			})
	})

	appPopover(c.Key("composer-plan-popover"), pill, &s.composerView.planPillOpen, func() {
		s.composerView.planPillOpen = false
	}, func() {
		renderPlanPopover(c, view)
	})
}

// renderPlanPopover 渲染点击胶囊后弹出的完整待办清单：每项带状态图标，
// 列表超过最大高度后在弹层内滚动。
func renderPlanPopover(c *ui.Context, view planPillView) {
	popoverCard(c, ui.Column(c.Key("composer-plan-popover-card")).
		MinWidth(planPopoverMinWidth).
		MaxWidth(planPopoverMaxWidth).
		Padding(Space3, Space4, Space3, Space4).
		Gap(Space3)).
		Children(func() {
			ui.Row(c.Key("composer-plan-popover-header")).
				AlignItems(ui.Center).
				Justify(ui.SpaceBetween).
				Children(func() {
					ui.Row(c.Key("composer-plan-popover-title")).Gap(Space2).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, IconListTodo()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
						ui.Text(c, "待办清单").
							FontSize(scaledFont(TypeFootnote)).
							FontWeight(500).
							TextColor(ThemeSecondaryText(c))
					})
					ui.Text(c, fmt.Sprintf("%d/%d", view.Done, view.Total)).
						FontSize(scaledFont(TypeCaption)).
						TextColor(ThemeSecondaryText(c))
				})

			ui.Scroll(c.Key("composer-plan-popover-scroll")).
				MaxHeight(planPopoverMaxHeight).
				Children(func() {
					ui.Column(c.Key("composer-plan-popover-list")).Gap(Space2).Children(func() {
						for i, it := range view.Items {
							it := it
							ui.Row(c.Key(fmt.Sprintf("composer-plan-popover-item-%d", i))).
								Gap(Space3).
								AlignItems(ui.Start).
								Children(func() {
									renderTodoStatusIcon(c, fmt.Sprintf("composer-plan-popover-icon-%d", i), it.Status, view.Running)
									textColor := ThemePrimaryText(c)
									if it.Status == "completed" {
										textColor = ThemeSecondaryText(c).Alpha(0.75)
									}
									text := ui.Text(c, it.Text).
										FontSize(scaledFont(TypeFootnote)).
										LineHeight(1.4).
										TextColor(textColor)
									if it.Status == "completed" {
										text.Strikethrough()
									}
								})
						}
					})
				})
		})
}
