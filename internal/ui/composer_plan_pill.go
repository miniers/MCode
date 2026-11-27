package mcodeui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"
)

// planPillBuilds 仅用于测试观测 buildPlanPillView 的实际构建次数。
var planPillBuilds atomic.Uint64

// 待办进度条：最新一份待办清单（TodoWrite / update_plan 工具）还没做完时，
// 在输入卡片正上方常驻一条，显示「进度环 · 完成数/总数 · 进行中那一项」，
// 点条头在原位展开完整清单（每项带状态图标，超出高度可滚动）。

const (
	// planPillBadgeSize 是进度环的边长。
	planPillBadgeSize float32 = 16
	// planPillMaxCurrentOps 是进行中待办项下最多展示的工具操作条数。
	planPillMaxCurrentOps = 1
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
	Visible    bool         // 是否显示待办条（清单还没做完）
	Complete   bool         // 清单刚全部完成（Visible 为 false）：条只在已显示时停留片刻
	Items      []todoItem   // 最新一份待办清单
	Done       int          // 已完成项数
	Total      int          // 总项数
	Current    string       // 进行中那一项的正文（没有进行中项时取第一项未完成的）
	Running    bool         // 会话是否正在运行，决定进行中图标是否转动
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

// lastUserMessageIndex 返回最后一条真正用户消息的下标，没有时返回 -1（子代理 bee 汇报不算用户消息）。
func lastUserMessageIndex(msgs []Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && !isSwarmReportMessage(msgs[i]) {
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
	if !running && idx <= lastUserMessageIndex(msgs) {
		return planPillView{}
	}
	if done >= total {
		return planPillView{Complete: true, Items: items, Done: done, Total: total}
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

// overlayMergedBees 用已合入工作区的 bee 覆盖待办条里对应的未完成项：
// 复制 view.Items 后将匹配到非空 Merged bee.Task 的非 completed 项置为 completed，
// 有改动时重算进度、当前项与可见/完成状态；无改动时原样返回 view。
func overlayMergedBees(view planPillView, bees []SwarmBee) planPillView {
	if len(view.Items) == 0 || len(bees) == 0 {
		return view
	}
	var mergedTasks map[string]struct{}
	for _, b := range bees {
		if !b.Merged {
			continue
		}
		if task := strings.TrimSpace(b.Task); task != "" {
			if mergedTasks == nil {
				mergedTasks = make(map[string]struct{}, len(bees))
			}
			mergedTasks[task] = struct{}{}
		}
	}
	if len(mergedTasks) == 0 {
		return view
	}
	var items []todoItem
	changed := false
	for i, item := range view.Items {
		if item.Status == "completed" {
			continue
		}
		if _, ok := mergedTasks[strings.TrimSpace(item.Text)]; !ok {
			continue
		}
		if !changed {
			items = append([]todoItem(nil), view.Items...)
			changed = true
		}
		items[i].Status = "completed"
	}
	if !changed {
		return view
	}
	done, total := todoPlanProgress(items)
	if done >= total {
		return planPillView{Complete: true, Items: items, Done: done, Total: total}
	}
	view.Visible = true
	view.Complete = false
	view.Items = items
	view.Done = done
	view.Total = total
	view.Current = todoPlanCurrentText(items)
	return view
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
// 收集它之后的工具调用消息（排除待办工具本身），最多保留最近 planPillMaxCurrentOps 条。
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

const (
	// planBarOverlap 是待办条被输入卡片盖住的高度：条像从卡片后面露出来。
	planBarOverlap float32 = 8
	// planBarListMaxHeight 是展开清单的最大高度，超出部分滚动。
	planBarListMaxHeight float32 = 154
	// planBarFadeHeight 是清单滚动区上下渐隐遮罩的高度。
	planBarFadeHeight float32 = 16
	// planBarDoneHold 是全部完成后条停留的时间，之后收起消失。
	planBarDoneHold = 2400 * time.Millisecond
)

// resetPlanBar 把待办条的展开、停留与行展开状态复位（条消失或换会话时调用）。
func (v *composerViewState) resetPlanBar() {
	v.planPillOpen = false
	v.planBarShown = false
	v.planBarHiding = false
	v.planBarDoneAt = time.Time{}
	v.planBarRowOpen = nil
	v.planBarScroll = ui.ScrollState{}
}

// planCurrentIndex 返回当前项下标：进行中的那一项，没有时取第一项未完成的，全部完成时返回 -1。
func planCurrentIndex(items []todoItem) int {
	for i, it := range items {
		if it.Status == "in_progress" {
			return i
		}
	}
	for i, it := range items {
		if it.Status != "completed" {
			return i
		}
	}
	return -1
}

// renderComposerPlanBar 在输入卡片正上方渲染待办条：清单没做完时常驻；
// 可见的清单变成全部完成后，条显示「全部完成」停留 planBarDoneHold 再收起。
// 打开线程时清单本来就已全部完成的不显示。
func renderComposerPlanBar(c *ui.Context, s *chatState, threadID string) {
	// 浮动面板里的子代理输入框空间太小，不显示待办条。
	if s == nil || s.embeddedBee {
		return
	}
	v := &s.composerView
	view, ok := composerPlanView(s, threadID)
	switch {
	case ok && view.Visible:
		v.planBarShown, v.planBarThread, v.planBarTotal = true, threadID, view.Total
		v.planBarDoneAt, v.planBarHiding = time.Time{}, false
	case ok && view.Complete && v.planBarShown && v.planBarThread == threadID && v.planBarTotal == view.Total:
		if v.planBarDoneAt.IsZero() {
			v.planBarDoneAt = c.Now()
		}
		if rest := planBarDoneHold - c.Now().Sub(v.planBarDoneAt); rest > 0 {
			c.After(rest)
		} else {
			v.planBarHiding = true
		}
	default:
		if v.planBarShown || v.planPillOpen {
			v.resetPlanBar()
		}
		return
	}
	if v.planPillOpen && c.Shortcut(0, ui.KeyEscape) {
		v.planPillOpen = false
	}

	wrap := ui.Column(c.Key("composer-plan-bar")).FillWidth().Shrink(0).Padding(0, Space4)
	show := float32(1)
	if v.planBarHiding {
		show = 0
	}
	k := wrap.AnimateTo("composer-plan-bar/show", show, ui.Animation{Duration: animateDuration(c, DurBase), Ease: motionEaseStandard})
	if v.planBarHiding && k == 0 {
		wrap.Hidden(true)
		v.resetPlanBar()
		return
	}
	// 负下边距抵掉列间距并让输入卡片盖住条的最后 planBarOverlap；收起过程中随 k 收到只抵间距。
	wrap.Margin(0, 0, -(Space3 + planBarOverlap*k), 0)
	wrap.Children(func() {
		strip := ui.Column(c.Key("composer-plan-bar-strip")).
			FillWidth().
			Shrink(0).
			Padding(0, 0, planBarOverlap, 0).
			Radius(RadiusLG, RadiusLG, 0, 0).
			Border(Hairline, ThemeBorder(c)).
			BorderWidth(Hairline, Hairline, 0, Hairline).
			Background(ThemeOverlayHover(c).Over(ThemeSurface(c)))
		strip.Children(func() {
			renderPlanBarHead(c, s, view)
			motionUnfoldBody(c, "composer-plan-bar-body", v.planPillOpen, func() {
				renderPlanBarList(c, s, view)
			})
		})
		if h := strip.LayoutBounds().H; h > 0 && k < 1 {
			wrap.ClipY().Height(k * h).Opacity(k)
		}
	})
}

// renderPlanBarHead 渲染条头：进度环、完成数、当前项与 chevron；点击展开或收起。
func renderPlanBarHead(c *ui.Context, s *chatState, view planPillView) {
	v := &s.composerView
	open := v.planPillOpen
	head := ui.ButtonBase(c.Key("composer-plan-bar-head")).
		Label(fmt.Sprintf("待办进度 %d/%d", view.Done, view.Total)).
		FillWidth().
		Height(ControlLG).
		Padding(0, Space3, 0, Space4).
		Radius(RadiusLG, RadiusLG, 0, 0).
		Transition(motionTransition(c, ui.ElementTransition{Duration: DurFast, Ease: EaseOut, Colors: true})).
		OnClick(func() {
			v.planPillOpen = !v.planPillOpen
			if v.planPillOpen {
				// 展开时让当前项落在第二行。
				v.planBarScroll.Y = float32(max(planCurrentIndex(view.Items)-1, 0)) * RowHeight
			} else {
				v.planBarRowOpen = nil
			}
		})
	if head.Hovered() {
		head.Background(ThemeOverlayHover(c))
	} else {
		head.Background(ui.Transparent)
	}
	turn := float32(1)
	if open {
		turn = 0
	}
	turn = head.AnimateTo("composer-plan-bar/chevron", turn, ui.Animation{Duration: animateDuration(c, DurBase), Ease: EaseOut})
	head.Children(func() {
		ui.Row(c.Key("composer-plan-bar-head-content")).
			FillWidth().
			Gap(composerCapsuleGap).
			AlignItems(ui.Center).
			Children(func() {
				if view.Complete {
					ui.Box(c.Key("composer-plan-bar-done")).Size(planPillBadgeSize, planPillBadgeSize).Center().Shrink(0).Children(func() {
						ui.Icon(c, IconCheck()).Size(IconSM, IconSM).TextColor(ThemeSuccess(c))
					})
				} else {
					// 进度环复用输入框底栏上下文用量表的圆弧画法，尺寸缩到 16pt。
					ui.Image(c, makeContextMeterSVG(isDark(c), view.fraction(), false)).
						Size(planPillBadgeSize, planPillBadgeSize).
						Fit(ui.Contain).
						Shrink(0)
				}
				ui.Text(c, fmt.Sprintf("%d/%d", view.Done, view.Total)).
					FontSize(uiFont(TypeFootnote)).
					FontFeatures("tnum").
					TextColor(ThemeSecondaryText(c)).
					SingleLine().
					Shrink(0)
				current := view.Current
				if view.Complete {
					current = "全部完成"
				}
				ui.Text(c, current).
					FontSize(uiFont(TypeFootnote)).
					TextColor(ThemePrimaryText(c)).
					SingleLine().
					Grow(1).
					MinWidth(0).
					Ellipsis("…")
				ui.Icon(c, IconChevronDown()).
					Size(IconSM, IconSM).
					TextColor(ThemeSecondaryText(c)).
					Rotate(180 * turn).
					Shrink(0)
			})
	})
}

// renderPlanBarList 渲染展开的清单：行高 RowHeight，最高 planBarListMaxHeight，
// 滚动区上下在还有内容时画渐隐遮罩。
func renderPlanBarList(c *ui.Context, s *chatState, view planPillView) {
	v := &s.composerView
	bg := ThemeOverlayHover(c).Over(ThemeSurface(c))
	ui.Column(c.Key("composer-plan-bar-list")).FillWidth().Padding(0, Space2, Space1, Space2).Children(func() {
		ui.Column(c.Key("composer-plan-bar-list-host")).FillWidth().Children(func() {
			ui.Scroll(c.Key("composer-plan-bar-scroll")).
				FillWidth().
				MaxHeight(planBarListMaxHeight).
				TrackScroll(&v.planBarScroll).
				Children(func() {
					ui.Column(c.Key("composer-plan-bar-rows")).FillWidth().Children(func() {
						shownOps := false
						for i := range view.Items {
							renderPlanBarRow(c, s, view, i, !shownOps)
							if view.Items[i].Status == "in_progress" {
								shownOps = true
							}
						}
					})
				})
			if v.planBarScroll.Y > 2 {
				renderPlanBarFade(c, "composer-plan-bar-fade-top", bg, true)
			}
			if v.planBarScroll.MaxY-v.planBarScroll.Y > 2 {
				renderPlanBarFade(c, "composer-plan-bar-fade-bottom", bg, false)
			}
		})
	})
}

// renderPlanBarFade 在滚动区的上沿或下沿画一条渐隐遮罩（不拦截点击与滚轮）。
func renderPlanBarFade(c *ui.Context, key string, bg ui.Color, top bool) {
	box := ui.Box(c.Key(key)).Absolute().Left(0).Right(0).Height(planBarFadeHeight).PassThrough()
	angle := float32(0)
	if top {
		box.Top(0)
		angle = 180
	} else {
		box.Bottom(0)
	}
	box.Draw(func(p *ui.Painter, r ui.Rect) {
		p.FillGradient(r, ui.LinearGradient{
			Angle: angle,
			Stops: []ui.GradientStop{{Color: bg, At: 0}, {Color: bg.Alpha(0), At: 1}},
		}, 0)
	})
}

// renderPlanBarRow 渲染清单里的一行：状态图标 + 正文，进行中项高亮并在正文下显示最近一条工具操作。
// 正文默认单行省略，点一行展开成多行，再点收回。
func renderPlanBarRow(c *ui.Context, s *chatState, view planPillView, i int, withOps bool) {
	v := &s.composerView
	it := view.Items[i]
	doing := it.Status == "in_progress"
	expanded := v.planBarRowOpen[i]
	row := ui.ButtonBase(c.Key(fmt.Sprintf("composer-plan-bar-row-%d", i))).
		Label(it.Text).
		Tooltip(it.Text).
		FillWidth().
		MinHeight(RowHeight).
		Padding(Space1, Space3).
		Radius(RadiusSM).
		Transition(motionTransition(c, ui.ElementTransition{Duration: DurFast, Ease: EaseOut, Colors: true})).
		OnClick(func() {
			if v.planBarRowOpen == nil {
				v.planBarRowOpen = map[int]bool{}
			}
			v.planBarRowOpen[i] = !v.planBarRowOpen[i]
		})
	switch {
	case doing:
		row.Background(ThemeAccentSubtle(c))
	case row.Hovered():
		row.Background(ThemeOverlayHover(c))
	default:
		row.Background(ui.Transparent)
	}
	row.Children(func() {
		ui.Row(c.Key(fmt.Sprintf("composer-plan-bar-row-content-%d", i))).
			FillWidth().
			Gap(Space3).
			AlignItems(ui.Start).
			Children(func() {
				renderPlanBarStatusIcon(c, fmt.Sprintf("composer-plan-bar-icon-%d", i), it.Status, view.Running)
				textColor := ThemeSecondaryText(c)
				switch it.Status {
				case "completed":
					textColor = ThemeTertiaryText(c)
				case "in_progress":
					textColor = ThemePrimaryText(c)
				}
				ui.Column(c.Key(fmt.Sprintf("composer-plan-bar-text-%d", i))).Grow(1).MinWidth(0).Children(func() {
					text := ui.Text(c, it.Text).
						FontSize(uiFont(TypeFootnote)).
						LineHeight(1.4).
						TextColor(textColor).
						MinWidth(0)
					if !expanded {
						text.SingleLine().Ellipsis("…")
					}
					if doing && withOps {
						for j, op := range view.CurrentOps {
							renderPlanBarOp(c, j, op)
						}
					}
				})
			})
	})
}

// renderPlanBarStatusIcon 渲染行首状态图标：已完成是绿色对勾（不套圆圈，与设计稿一致），
// 其余沿用时间线待办卡片的图标。
func renderPlanBarStatusIcon(c *ui.Context, key, status string, run bool) {
	if status != "completed" {
		renderTodoStatusIcon(c, key, status, run)
		return
	}
	ui.Box(c.Key(key+"-box")).Size(11, 16).Center().Children(func() {
		ui.Icon(c, IconCheck()).Size(11, 11).TextColor(ThemeSuccess(c))
	})
}

// renderPlanBarOp 渲染进行中待办项下的一条工具操作：图标/工具名 + 路径芯片目标。
func renderPlanBarOp(c *ui.Context, idx int, op planOpItem) {
	label := strings.TrimSpace(op.Name + " " + op.Target)
	ui.Row(c.Key(fmt.Sprintf("composer-plan-bar-op-%d", idx))).
		Label(label).
		FillWidth().
		Gap(Space2).
		AlignItems(ui.Center).
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
				ui.Row(c.Key(fmt.Sprintf("composer-plan-bar-op-target-%d", idx))).
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
