package mcodeui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/egoist/mygo/ui"
)

// planPillBuilds 仅用于测试观测 buildPlanPillView 的实际构建次数。
var planPillBuilds atomic.Uint64

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
	// planPillMaxCurrentOps 是弹层里当前步下面最多展示的工具操作条数。
	planPillMaxCurrentOps = 8
	// planPillMilestoneMinItems / planPillMilestoneMaxItems 是胶囊展示里程碑圆点的项数范围。
	planPillMilestoneMinItems = 2
	planPillMilestoneMaxItems = 6
)

// planOpItem 是待办当前步下的一次工具操作摘要。
type planOpItem struct {
	Name     string       // 工具名或动词
	Target   string       // 简短目标（文件路径或命令摘要）
	Category toolCategory // 工具类别，决定图标
	Command  bool         // 是否为命令类工具
}

// planPillView 是一帧待办进度胶囊要展示的内容。
type planPillView struct {
	Visible    bool         // 是否显示胶囊
	Items      []todoItem   // 最新一份待办清单
	Done       int          // 已完成项数
	Total      int          // 总项数
	Current    string       // 进行中那一项的正文（没有进行中项时取第一项未完成的）
	Running    bool         // 会话是否正在运行，决定进度环是否呼吸
	CurrentOps []planOpItem // 当前步开始之后的最近若干条工具操作
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
	items         []todoItem
	ids           []string
	inProgressMsg []int
	created       int
}

func (a *claudeTaskAccumulator) add(m Message) bool {
	return a.addAt(m, -1)
}

func (a *claudeTaskAccumulator) addAt(m Message, msgIdx int) bool {
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
		a.inProgressMsg = append(a.inProgressMsg, -1)
		return true
	}
	for j, id := range a.ids {
		if id != input.TaskID {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(input.Status), "deleted") {
			a.ids = append(a.ids[:j], a.ids[j+1:]...)
			a.items = append(a.items[:j], a.items[j+1:]...)
			a.inProgressMsg = append(a.inProgressMsg[:j], a.inProgressMsg[j+1:]...)
			break
		}
		if input.Subject != nil {
			a.items[j].Text = *input.Subject
		}
		if input.Status != "" {
			st := normalizeTodoStatus(input.Status)
			a.items[j].Status = st
			if st == "in_progress" {
				a.inProgressMsg[j] = msgIdx
			} else {
				a.inProgressMsg[j] = -1
			}
		}
		break
	}
	return true
}

// claudeTaskPlan 按创建顺序聚合 Claude 的增量任务消息。
func claudeTaskPlan(msgs []Message) ([]todoItem, int) {
	items, _, last := claudeTaskPlanWithStarts(msgs)
	return items, last
}

// claudeTaskPlanWithStarts 同时返回每条任务最近一次标为进行中的消息下标。
func claudeTaskPlanWithStarts(msgs []Message) ([]todoItem, []int, int) {
	var tasks claudeTaskAccumulator
	last := -1
	for i, m := range msgs {
		if tasks.addAt(m, i) {
			last = i
		}
	}
	return tasks.items, tasks.inProgressMsg, last
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
	items, idx, _, _ := latestTodoPlanWithStarts(msgs)
	return items, idx
}

func latestTodoPlanWithStarts(msgs []Message) (items []todoItem, latestIdx int, taskStarts []int, taskIndex int) {
	tasks, starts, taskIdx := claudeTaskPlanWithStarts(msgs)
	for i := len(msgs) - 1; i > taskIdx; i-- {
		m := msgs[i]
		if !isTodoToolMessage(m) {
			continue
		}
		if parsed, ok := todoItemsFromMessage(m); ok {
			return parsed, i, starts, taskIdx
		}
	}
	return tasks, taskIdx, starts, taskIdx
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
	planPillBuilds.Add(1)
	items, idx, taskStarts, taskIndex := latestTodoPlanWithStarts(msgs)
	if len(items) == 0 || idx < 0 {
		return planPillView{}
	}
	done, total := todoPlanProgress(items)
	if done >= total {
		return planPillView{}
	}
	if !running && idx <= lastUserMessageIndex(msgs) {
		return planPillView{}
	}
	return planPillView{
		Visible:    true,
		Items:      items,
		Done:       done,
		Total:      total,
		Current:    todoPlanCurrentText(items),
		Running:    running,
		CurrentOps: planCurrentOps(msgs, items, idx, taskStarts, taskIndex),
	}
}

// planPillMilestones 返回胶囊内 2..6 项的步骤里程碑状态；超出范围返回 nil。
func planPillMilestones(v planPillView) []milestoneState {
	if v.Total < planPillMilestoneMinItems || v.Total > planPillMilestoneMaxItems || len(v.Items) != v.Total {
		return nil
	}
	out := make([]milestoneState, len(v.Items))
	for i, it := range v.Items {
		switch it.Status {
		case "completed":
			out[i] = milestoneCompleted
		case "in_progress":
			out[i] = milestoneInProgress
		default:
			out[i] = milestonePending
		}
	}
	return out
}

// isPlanTodoTool 判断工具消息是否属于待办清单工具本身，用于从当前步操作列表里排除。
func isPlanTodoTool(m Message) bool {
	if !isTodoToolMessage(m) {
		return false
	}
	if _, ok := todoItemsFromMessage(m); ok {
		return true
	}
	switch toolNameKey(toolMessageName(m)) {
	case "todowrite", "updateplan", "taskcreate", "taskupdate":
		return true
	}
	return false
}

// planCurrentOps 取最近一条把当前进行中项标为 in_progress 的待办消息，
// 收集它之后的工具调用消息（排除待办工具本身），最多保留最近 8 条。
func planCurrentOps(msgs []Message, items []todoItem, latestIdx int, taskStarts []int, taskIndex int) []planOpItem {
	currentIdx := -1
	currentText := ""
	for i, it := range items {
		if it.Status == "in_progress" {
			currentIdx = i
			currentText = it.Text
			break
		}
	}
	if currentIdx < 0 {
		return nil
	}

	startIdx := -1
	if latestIdx == taskIndex {
		if currentIdx < len(taskStarts) {
			startIdx = taskStarts[currentIdx]
		}
	} else {
		for i := latestIdx; i > taskIndex; i-- {
			m := msgs[i]
			if !isTodoToolMessage(m) {
				continue
			}
			prevItems, ok := todoItemsFromMessage(m)
			if !ok {
				continue
			}
			for _, it := range prevItems {
				if it.Text == currentText && it.Status == "in_progress" {
					startIdx = i
					break
				}
			}
			if startIdx >= 0 {
				break
			}
		}
	}
	if startIdx < 0 {
		return nil
	}

	var ops []planOpItem
	for i := startIdx + 1; i < len(msgs); i++ {
		m := msgs[i]
		if !isTodoToolMessage(m) || isPlanTodoTool(m) {
			continue
		}
		ops = append(ops, buildPlanOpItem(m))
	}
	if len(ops) > planPillMaxCurrentOps {
		ops = ops[len(ops)-planPillMaxCurrentOps:]
	}
	return ops
}

// buildPlanOpItem 复用时间线工具行标题/摘要函数提取单条工具操作的工具名与简短目标。
func buildPlanOpItem(m Message) planOpItem {
	verb, target, _ := toolCallTitle(m)
	name := strings.TrimSpace(toolMessageName(m))
	category := classifyToolCategory(name)
	isCmd := parseCommandToolInfo(m).IsCommand || category == toolCategoryCommand
	shortTarget := target
	switch {
	case isCmd, category == toolCategoryRead, category == toolCategoryEdit, category == toolCategorySearch:
		shortTarget = toolChipShortTitle(m, verb, target)
	default:
		if verb != "" {
			name = verb
		}
		shortTarget = truncateCommandLine(target, 48)
	}
	if verb != "" && (category != toolCategoryOther || isCmd) {
		name = verb
	}
	if name == "" {
		name = "工具"
	}
	return planOpItem{
		Name:     name,
		Target:   shortTarget,
		Category: category,
		Command:  isCmd,
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
					ring.Opacity(1)
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
					shownCurrentOps := false
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
							if it.Status == "in_progress" && !shownCurrentOps {
								shownCurrentOps = true
								if len(view.CurrentOps) > 0 {
									ui.Column(c.Key("composer-plan-popover-ops")).
										FillWidth().
										Padding(0, 0, 0, Space6).
										Gap(Space1).
										Transition(motionCollapse(c)).
										Children(func() {
											for j, op := range view.CurrentOps {
												renderPlanPopoverOp(c, j, op)
											}
										})
								}
							}
						}
					})
				})
		})
}

// renderPlanPopoverOp 渲染进行中待办项下的一条工具操作：图标/工具名 + 路径芯片目标。
func renderPlanPopoverOp(c *ui.Context, idx int, op planOpItem) {
	label := strings.TrimSpace(op.Name + " " + op.Target)
	ui.Row(c.Key(fmt.Sprintf("composer-plan-popover-op-%d", idx))).
		Label(label).
		FillWidth().
		Gap(Space2).
		AlignItems(ui.Center).
		Transition(motionCollapse(c)).
		Children(func() {
			renderToolChipCategoryMark(c, op.Category, op.Command, "monospace")
			if op.Name != "" && !op.Command {
				ui.Text(c, op.Name).
					FontSize(scaledFont(TypeCaption)).
					TextColor(ThemeSecondaryText(c)).
					SingleLine().
					Shrink(0)
			}
			if op.Target != "" {
				ui.Row(c.Key(fmt.Sprintf("composer-plan-popover-op-target-%d", idx))).
					Label(op.Target).
					Padding(Space0, Space2).
					Radius(RadiusXS).
					Background(ThemeOverlayHover(c)).
					Shrink(1).
					MinWidth(0).
					Children(func() {
						targetText := ui.Text(c, op.Target).
							FontSize(scaledFont(TypeCaption)).
							TextColor(ThemeSecondaryText(c)).
							SingleLine().
							Shrink(1).
							MinWidth(0).
							Ellipsis("…")
						if op.Command {
							targetText.Font("monospace")
						}
					})
			}
		})
}
