// 工具输入解析和待办卡片。
package mcodeui

import (
	"encoding/json"
	"fmt"
	"github.com/egoist/mygo/ui"
	"strconv"
	"strings"
	"time"
)

// toolInputFields 解析工具消息中的完整输入 JSON 为 map[string]any，结果按消息 ID 缓存。
// 返回的 map 只读，调用方不得修改；m.ID 为空时不走缓存。
func toolInputFields(m Message) map[string]any {
	if m.ID == "" {
		return toolInputFieldsUncached(m)
	}
	toolParseCacheMu.Lock()
	e, hit := toolParseCacheLookup(m)
	if hit && e.hasFields {
		fields := e.fields
		toolParseCacheMu.Unlock()
		return fields
	}
	toolParseCacheMu.Unlock()

	// 解析在锁外进行：parseCommandToolInfoUncached 内部还会回调本函数，持锁调用会死锁。
	fields := toolInputFieldsUncached(m)

	toolParseCacheMu.Lock()
	if hit && e.matches(m) {
		e.fields, e.hasFields = fields, true
	} else {
		e = newToolParseCacheEntry(m)
		e.fields, e.hasFields = fields, true
		toolParseCachePut(m, e)
	}
	toolParseCacheMu.Unlock()
	return fields
}

// toolInputFieldsUncached 解析工具消息中的完整输入 JSON 为 map[string]any。
// 若 m.ToolInput 为空或解析失败，返回 nil。
func toolInputFieldsUncached(m Message) map[string]any {
	input := strings.TrimSpace(m.ToolInput)
	if input == "" {
		return nil
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(input), &fields); err != nil {
		return nil
	}
	return fields
}

// parseRFC3339Time 尝试用 RFC3339Nano 或 RFC3339 解析时间字符串。
func parseRFC3339Time(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// isToolBlockedByHook 判断工具结果是否由 PreToolUse hook 拦截/改道而来（例如 context-mode 插件把
// WebFetch 改道），这种结果是主动拦截而不是工具故障。新数据以 ToolStatus == "blocked" 为准；
// 老数据仍是 failed 并在正文里留一行 hook 错误，因此保留逐行文本嗅探作历史兜底。
// 逐行扫描而不是整段匹配，避免正文里零散的提及造成误判。
func isToolBlockedByHook(m Message) bool {
	if strings.EqualFold(strings.TrimSpace(m.ToolStatus), "blocked") {
		return true
	}
	// 老数据兜底：正文里单独成行的 PreToolUse hook 错误。
	for _, line := range strings.Split(m.Text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "PreToolUse:") && strings.Contains(line, " hook error:") {
			return true
		}
	}
	return false
}

// isToolFailed 判断工具执行是否失败。m.ToolStatus == "failed" 时视为失败，与现有 [failed] 判断取或；
// 被 PreToolUse hook 拦截/改道的结果虽以 [failed] 结尾，但它不是故障，不算失败。
func isToolFailed(m Message) bool {
	if isToolBlockedByHook(m) {
		return false
	}
	return m.ToolStatus == "failed" || strings.Contains(m.Text, "[failed]")
}

// isCommandTool 判断消息是否属于命令执行类工具。
func isCommandTool(m Message) bool {
	if m.Role != "" && m.Role != "tool" {
		return false
	}
	if strings.EqualFold(m.Kind, "command") {
		return true
	}
	return commandToolNames[toolNameKey(toolMessageName(m))]
}

// truncateCommandLine 将命令正文规整为单行并在超出最大长度时截断。
func truncateCommandLine(cmd string, maxLen int) string {
	cmd = strings.TrimSpace(cmd)
	cmd = strings.ReplaceAll(cmd, "\r", "")
	cmd = strings.ReplaceAll(cmd, "\n", " ")
	cmd = strings.Join(strings.Fields(cmd), " ")
	runes := []rune(cmd)
	if len(runes) > maxLen {
		return string(runes[:maxLen]) + "..."
	}
	return cmd
}

// parseCommandToolInfo 解析命令工具消息中的命令正文、退出码和耗时，结果按消息 ID 缓存。
// 返回的 ExitCode 指针只读，调用方不得写入其指向的值；m.ID 为空时不走缓存。
func parseCommandToolInfo(m Message) CommandToolInfo {
	if m.ID == "" {
		return parseCommandToolInfoUncached(m)
	}
	toolParseCacheMu.Lock()
	e, hit := toolParseCacheLookup(m)
	if hit && e.hasInfo {
		info := e.info
		toolParseCacheMu.Unlock()
		return info
	}
	toolParseCacheMu.Unlock()

	// 解析在锁外进行，避免嵌套加锁。
	info := parseCommandToolInfoUncached(m)

	toolParseCacheMu.Lock()
	if hit && e.matches(m) {
		e.info, e.hasInfo = info, true
	} else {
		e = newToolParseCacheEntry(m)
		e.info, e.hasInfo = info, true
		toolParseCachePut(m, e)
	}
	toolParseCacheMu.Unlock()
	return info
}

// parseCommandToolInfoUncached 解析命令工具消息中的命令正文、退出码和耗时。
func parseCommandToolInfoUncached(m Message) CommandToolInfo {
	if !isCommandTool(m) {
		return CommandToolInfo{IsCommand: false}
	}

	info := CommandToolInfo{
		IsCommand: true,
		Failed:    isToolFailed(m),
	}

	// 1. 提取命令正文
	firstLine := m.Text
	if idx := strings.IndexByte(firstLine, '\n'); idx != -1 {
		firstLine = firstLine[:idx]
	}

	// 优先在 m.ToolInput（完整 JSON）上用 encoding/json 解析提取
	if fields := toolInputFields(m); fields != nil {
		for _, key := range []string{"command", "cmd", "CommandLine", "command_line", "commandline", "input", "script", "code"} {
			if val, ok := fields[key]; ok {
				if s, ok := val.(string); ok && strings.TrimSpace(s) != "" {
					info.Command = s
					break
				}
			}
		}
		if info.Command == "" {
			for _, key := range []string{"args", "argv"} {
				if val, ok := fields[key]; ok {
					switch arr := val.(type) {
					case []any:
						var parts []string
						for _, item := range arr {
							if item != nil {
								parts = append(parts, fmt.Sprint(item))
							}
						}
						if len(parts) > 0 {
							info.Command = strings.Join(parts, " ")
						}
					case []string:
						if len(arr) > 0 {
							info.Command = strings.Join(arr, " ")
						}
					}
					if info.Command != "" {
						break
					}
				}
			}
		}
	}

	// 解析失败或 ToolInput 为空时，回退到现有基于 Text 的正则逻辑
	if info.Command == "" {
		if match := toolCommandKeyRe.FindStringSubmatch(firstLine); len(match) > 1 {
			raw := match[1]
			if unq, err := strconv.Unquote(`"` + raw + `"`); err == nil {
				raw = unq
			}
			info.Command = raw
		} else if match := toolCommandArgsRe.FindStringSubmatch(firstLine); len(match) > 1 {
			var args []string
			for _, m := range toolCommandArgItemRe.FindAllStringSubmatch(match[1], -1) {
				arg := m[1]
				if unq, err := strconv.Unquote(`"` + arg + `"`); err == nil {
					arg = unq
				}
				args = append(args, arg)
			}
			if len(args) > 0 {
				info.Command = strings.Join(args, " ")
			}
		}
	}

	if info.Command == "" {
		tName := toolMessageName(m)
		trimmedFirst := strings.TrimSpace(firstLine)
		if strings.HasPrefix(trimmedFirst, tName) {
			rest := strings.TrimSpace(trimmedFirst[len(tName):])
			if !strings.HasPrefix(rest, "{") && rest != "" {
				info.Command = rest
			}
		}
	}

	if info.Command == "" {
		info.Command = toolSummary(m.Text)
	}
	info.Command = truncateCommandLine(info.Command, 80)

	// 2. 提取退出码 (ExitCode，沿用现有正则逻辑)
	if match := toolExitCodeJSONRe.FindStringSubmatch(m.Text); len(match) > 1 {
		if code, err := strconv.Atoi(match[1]); err == nil {
			info.ExitCode = &code
		}
	} else if match := toolExitCodeTextRe.FindStringSubmatch(m.Text); len(match) > 1 {
		if code, err := strconv.Atoi(match[1]); err == nil {
			info.ExitCode = &code
		}
	}

	// 3. 提取耗时 (Duration)
	// 优先由 ToolCompletedAt 与 CreatedAt 计算差值
	if start, ok1 := parseRFC3339Time(m.CreatedAt); ok1 {
		if end, ok2 := parseRFC3339Time(m.ToolCompletedAt); ok2 {
			dur := end.Sub(start)
			if dur < 0 {
				dur = 0
			}
			info.Duration = fmt.Sprintf("%.1fs", dur.Seconds())
		}
	}
	// 否则回退到现有正则逻辑
	if info.Duration == "" {
		if match := toolDurationMsJSONRe.FindStringSubmatch(m.Text); len(match) > 1 {
			if ms, err := strconv.ParseFloat(match[1], 64); err == nil {
				info.Duration = fmt.Sprintf("%.1fs", ms/1000.0)
			}
		} else if match := toolDurationStrJSONRe.FindStringSubmatch(m.Text); len(match) > 1 {
			strVal := strings.TrimSpace(match[1])
			if strings.HasSuffix(strVal, "ms") {
				numPart := strings.TrimSuffix(strVal, "ms")
				if ms, err := strconv.ParseFloat(strings.TrimSpace(numPart), 64); err == nil {
					info.Duration = fmt.Sprintf("%.1fs", ms/1000.0)
				}
			} else if strings.HasSuffix(strVal, "s") {
				numPart := strings.TrimSuffix(strVal, "s")
				if s, err := strconv.ParseFloat(strings.TrimSpace(numPart), 64); err == nil {
					info.Duration = fmt.Sprintf("%.1fs", s)
				}
			}
		} else if match := toolDurationSecJSONRe.FindStringSubmatch(m.Text); len(match) > 1 {
			if sec, err := strconv.ParseFloat(match[1], 64); err == nil {
				info.Duration = fmt.Sprintf("%.1fs", sec)
			}
		} else if match := toolDurationTextRe.FindStringSubmatch(m.Text); len(match) > 1 {
			strVal := strings.TrimSpace(match[1])
			if strings.HasSuffix(strVal, "ms") {
				numPart := strings.TrimSuffix(strVal, "ms")
				if ms, err := strconv.ParseFloat(strings.TrimSpace(numPart), 64); err == nil {
					info.Duration = fmt.Sprintf("%.1fs", ms/1000.0)
				}
			} else if strings.HasSuffix(strVal, "s") {
				numPart := strings.TrimSuffix(strVal, "s")
				if s, err := strconv.ParseFloat(strings.TrimSpace(numPart), 64); err == nil {
					info.Duration = fmt.Sprintf("%.1fs", s)
				}
			}
		}
	}

	return info
}

// parseTodoItems 解析 TodoWrite / update_plan 这类工具的输入 JSON（含 todos 或 plan 数组，
// 每项有 content/step 与 status），返回待办项列表；不是待办工具时 ok 为 false。
func parseTodoItems(input string) ([]todoItem, bool) {
	start := strings.IndexByte(input, '{')
	if start < 0 {
		return nil, false
	}
	var payload struct {
		Todos []map[string]any `json:"todos"`
		Plan  []map[string]any `json:"plan"`
	}
	if err := json.NewDecoder(strings.NewReader(input[start:])).Decode(&payload); err != nil {
		return nil, false
	}
	raw := payload.Todos
	if len(raw) == 0 {
		raw = payload.Plan
	}
	if len(raw) == 0 {
		return nil, false
	}

	items := make([]todoItem, 0, len(raw))
	for _, entry := range raw {
		text := firstTodoText(entry)
		if strings.TrimSpace(text) == "" {
			continue
		}
		status := "pending"
		if s, ok := entry["status"].(string); ok {
			status = normalizeTodoStatus(s)
		}
		items = append(items, todoItem{Text: text, Status: status})
	}
	if len(items) == 0 {
		return nil, false
	}
	return items, true
}

// todoItemsUncached 从一条消息里解析待办清单：优先解析结构化输入 ToolInput，
// 拿不到（旧消息或输入被截断）时回退到正文 m.Text。
func todoItemsUncached(m Message) ([]todoItem, bool) {
	if m.ToolInput != "" {
		if items, ok := parseTodoItems(m.ToolInput); ok {
			return items, true
		}
	}
	return parseTodoItems(m.Text)
}

// todoItemsFromMessage 解析一条消息的待办清单，结果按消息 ID 缓存：
// 时间线每帧都会对每条工具消息解析一次 JSON，而结果只取决于 ToolInput 与 Text。
// 返回的切片只读，调用方不得修改；m.ID 为空时不走缓存。
func todoItemsFromMessage(m Message) ([]todoItem, bool) {
	if m.ID == "" {
		return todoItemsUncached(m)
	}
	toolParseCacheMu.Lock()
	e, hit := toolParseCacheLookup(m)
	if hit && e.hasTodo {
		items, ok := e.todoItems, e.todoOK
		toolParseCacheMu.Unlock()
		return items, ok
	}
	toolParseCacheMu.Unlock()

	// 解析在锁外进行，避免与其它缓存路径嵌套加锁。
	items, ok := todoItemsUncached(m)

	toolParseCacheMu.Lock()
	if hit && e.matches(m) {
		e.todoItems, e.todoOK, e.hasTodo = items, ok, true
	} else {
		// 失配（Text 或 ToolInput 变了）时新建条目覆盖；输入字段只取决于 ToolInput，
		// 它没变就沿用旧条目上已算好的结果，不丢也不重算。
		// 待办结果同时取决于 ToolInput 与 Text，本次已经重新解析，无需沿用旧值。
		rebuilt := newToolParseCacheEntry(m)
		if hit && e.ToolInput == m.ToolInput {
			rebuilt.hasFields, rebuilt.fields = e.hasFields, e.fields
		}
		rebuilt.todoItems, rebuilt.todoOK, rebuilt.hasTodo = items, ok, true
		toolParseCachePut(m, rebuilt)
	}
	toolParseCacheMu.Unlock()
	return items, ok
}

// firstTodoText 取待办项第一个非空的正文字段。
func firstTodoText(entry map[string]any) string {
	for _, key := range todoTextKeys {
		if v, ok := entry[key].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// normalizeTodoStatus 把待办状态归一到 pending / in_progress / completed。
func normalizeTodoStatus(status string) string {
	s := strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(status)))
	switch s {
	case "completed", "complete", "done", "finished":
		return "completed"
	case "in_progress", "inprogress", "active", "running", "doing":
		return "in_progress"
	default:
		return "pending"
	}
}

// todoCardKey 返回待办卡片开合状态在 s.expandedTool 里的键（与工具胶囊共用同一张表）。
func todoCardKey(msgID string) string { return "todo-" + msgID }

// todoProgress 返回待办清单的完成数与总数。
func todoProgress(items []todoItem) (done, total int) {
	for _, it := range items {
		if it.Status == "completed" {
			done++
		}
	}
	return done, len(items)
}

// todoProgressRatio 返回进度条的填充比例（0..1）：完成数除以总数，空列表为 0。
func todoProgressRatio(items []todoItem) float32 {
	done, total := todoProgress(items)
	if total <= 0 {
		return 0
	}
	return float32(done) / float32(total)
}

// todoFocusItem 返回收起摘要要显示的那一项：优先进行中，其次第一项未完成；
// 全部完成或空列表时 ok 为 false。
func todoFocusItem(items []todoItem) (todoItem, bool) {
	for _, it := range items {
		if it.Status == "in_progress" {
			return it, true
		}
	}
	for _, it := range items {
		if it.Status != "completed" {
			return it, true
		}
	}
	return todoItem{}, false
}

// todoCollapsedSummary 返回收起态那一行摘要：「完成数/总数 · 当前那一项」，
// 全部完成时显示「已全部完成」，空列表显示「暂无待办」。
func todoCollapsedSummary(items []todoItem) string {
	done, total := todoProgress(items)
	if total == 0 {
		return "暂无待办"
	}
	if done == total {
		return fmt.Sprintf("%d/%d · 已全部完成", done, total)
	}
	if it, ok := todoFocusItem(items); ok {
		return fmt.Sprintf("%d/%d · %s", done, total, strings.Join(strings.Fields(it.Text), " "))
	}
	return fmt.Sprintf("%d/%d", done, total)
}

// todoInputLooksLikePlan 廉价预判一段文本是否可能是待办工具输入，
// 让 latestTodoMessageID 不必为整段历史做 JSON 解析。
func todoInputLooksLikePlan(raw string) bool {
	if raw == "" || !strings.Contains(raw, `"status"`) {
		return false
	}
	return strings.Contains(raw, `"todos"`) || strings.Contains(raw, `"plan"`)
}

// latestTodoMessageID 返回给定消息里最后一条待办消息的 ID；没有待办消息时返回空。
// 倒序扫描，命中即停。
func latestTodoMessageID(messages []Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		for _, raw := range [2]string{m.ToolInput, m.Text} {
			if !todoInputLooksLikePlan(raw) {
				continue
			}
			if _, ok := parseTodoItems(raw); ok {
				return m.ID
			}
		}
	}
	return ""
}

// todoCardExpanded 返回待办卡片此刻是否展开：用户在卡片头部点过就以用户选择为准；
// 否则只有「当前线程最新的一条待办，且线程正在运行」时默认展开，其余情况默认收起。
// running 由调用方算好传入（s.isThreadRunning 内部自己加锁，不能在这里持锁调用）。
func (s *chatState) todoCardExpanded(m Message, running bool) bool {
	s.mu.Lock()
	open, decided := s.expandedTool[todoCardKey(m.ID)]
	latest := latestTodoMessageID(s.messages)
	s.mu.Unlock()
	if decided {
		return open
	}
	return running && latest == m.ID
}

// toggleTodoCard 记录用户对某条待办卡片开合的显式选择。
func (s *chatState) toggleTodoCard(msgID string, open bool) {
	s.mu.Lock()
	if s.expandedTool == nil {
		s.expandedTool = make(map[string]bool)
	}
	s.expandedTool[todoCardKey(msgID)] = open
	s.expandedRev++
	s.mu.Unlock()
	s.holdChatPlace()
	s.invalidate()
}

// renderTodoCard 把待办清单工具调用渲染成卡片：头部可折叠、下方一条 2px 进度条；
// 展开时每项前是状态图标（空心圆、进行中、对勾），已完成项置灰并划掉；
// 收起时只留一行摘要。
func renderTodoCard(c *ui.Context, s *chatState, m Message, items []todoItem) ui.Element {
	running := s.isThreadRunning(selectedThreadIDSnapshot(s))
	expanded := s.todoCardExpanded(m, running)
	done, total := todoProgress(items)

	return ui.Column(c.Key("msg-todo-"+m.ID)).
		FillWidth().
		MaxWidth(timelineContentMaxWidth(s)).
		Padding(Space4).
		Radius(RadiusLG).
		Background(ThemeSurface(c)).
		Border(1, ThemeBorder(c)).
		Gap(Space2).
		Children(func() {
			renderTodoCardHeader(c, s, m, done, total, expanded)
			renderTodoProgressBar(c, m, items)
			if !expanded {
				renderTodoCardSummary(c, m, items, running)
				return
			}
			for i, it := range items {
				ui.Row(c.Key(fmt.Sprintf("todo-item-%s-%d", m.ID, i))).
					Gap(Space3).
					AlignItems(ui.Start).
					Children(func() {
						renderTodoStatusIcon(c, fmt.Sprintf("todo-icon-%s-%d", m.ID, i), it.Status, running)
						textColor := ThemePrimaryText(c)
						if it.Status == "completed" {
							textColor = ThemeSecondaryText(c).Alpha(0.75)
						}
						text := ui.Text(c, it.Text).FontSize(scaledFont(TypeFootnote)).LineHeight(1.4).TextColor(textColor)
						if it.Status == "completed" {
							text.Strikethrough()
						}
					})
			}
		})
}

// renderTodoCardHeader 渲染待办卡片头部：整行可点击折叠，右侧是完成数与折叠箭头。
func renderTodoCardHeader(c *ui.Context, s *chatState, m Message, done, total int, expanded bool) {
	toggleLabel := "展开待办清单"
	chevronTarget := float32(0)
	if expanded {
		toggleLabel = "收起待办清单"
		chevronTarget = 90
	}
	btn := ui.ButtonBase(c.Key("todo-header-" + m.ID)).
		FillWidth().
		Label(toggleLabel).
		Tooltip(toggleLabel).
		Cursor(ui.CursorPointer).
		OnClick(func() {
			s.toggleTodoCard(m.ID, !expanded)
		})
	// 展开/收起箭头：同一个右箭头按顺时针 90 度插值旋转成下箭头
	rotation := btn.AnimateTo("todo-chevron-"+m.ID, chevronTarget, ui.Animation{Duration: animateDuration(c, motionFast), Ease: motionEaseStandard})
	btn.Children(func() {
		ui.Row(c.Key("todo-header-row-" + m.ID)).FillWidth().Gap(Space2).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, IconListTodo()).Size(12, 12).TextColor(ThemeSecondaryText(c))
			ui.Text(c, "待办清单").FontSize(scaledFont(TypeFootnote)).FontWeight(500).TextColor(ThemeSecondaryText(c))
			ui.Spacer(c)
			ui.Text(c, fmt.Sprintf("%d/%d", done, total)).FontSize(scaledFont(TypeCaption)).TextColor(ThemeSecondaryText(c))
			ui.Icon(c, IconChevronRight()).Size(11, 11).TextColor(ThemeSecondaryText(c)).Rotate(rotation)
		})
	})
}

// renderTodoProgressBar 渲染头部下方的 2px 进度条：轨道用分隔线色、填充用强调色，
// 两段的 Grow 权重就是 todoProgressRatio。
func renderTodoProgressBar(c *ui.Context, m Message, items []todoItem) {
	ProgressBar(c, ProgressBarProps{
		Key: "todo-progress-" + m.ID, Label: "待办进度", Ratio: todoProgressRatio(items),
		Height: todoProgressBarHeight, Track: ThemeDivider(c), Fill: ThemeAccent(c),
	})
}

// renderTodoCardSummary 渲染收起态的一行摘要：状态图标加 todoCollapsedSummary 的文本。
func renderTodoCardSummary(c *ui.Context, m Message, items []todoItem, running bool) {
	status := "pending"
	if it, ok := todoFocusItem(items); ok {
		status = it.Status
	} else if done, total := todoProgress(items); total > 0 && done == total {
		status = "completed"
	}
	ui.Row(c.Key("todo-summary-" + m.ID)).Gap(Space3).AlignItems(ui.Center).Children(func() {
		renderTodoStatusIcon(c, "todo-summary-icon-"+m.ID, status, running)
		ui.Text(c, todoCollapsedSummary(items)).
			FontSize(scaledFont(TypeFootnote)).
			LineHeight(1.4).
			TextColor(ThemeSecondaryText(c)).
			SingleLine()
	})
}

// renderTodoStatusIcon 渲染待办项前的状态图标：待处理为空心圆，进行中为强调色圆点，已完成为绿色对勾。
// 进行中的图标只在线程确实还在运行时才转（run 为 true）；线程停下后显示静态圆点，避免假装仍在进行。
func renderTodoStatusIcon(c *ui.Context, key, status string, run bool) {
	switch status {
	case "completed":
		ui.Box(c.Key(key+"-box")).Size(11, 11).Center().Children(func() {
			ui.Icon(c, IconCheck()).Size(11, 11).TextColor(ThemeSuccess(c))
		})
	case "in_progress":
		ui.Box(c.Key(key+"-box")).Size(11, 11).Center().Children(func() {
			if run {
				renderRunningGlyph(c, key+"-run", 9, ThemeAccent(c))
				return
			}
			ui.Box(c.Key(key+"-dot")).Size(9, 9).Radius(RadiusPill).Background(ThemeAccent(c))
		})
	default:
		ui.Box(c.Key(key+"-box")).Size(11, 11).Center().Children(func() {
			ui.Box(c.Key(key+"-ring")).Size(9, 9).Radius(RadiusPill).Border(1.5, ThemeBorder(c))
		})
	}
}
