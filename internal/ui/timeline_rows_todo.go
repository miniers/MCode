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

// isToolFailed 判断工具执行是否失败：显式 ToolStatus 非空时以它是否等于 failed 为准，
// 避免 session_get 等工具返回的历史正文里旧 [failed] 标记误报失败；空状态的老消息仍按正文 [failed] 兜底。
// 被 PreToolUse hook 拦截/改道的结果不是故障，不算失败。
func isToolFailed(m Message) bool {
	if isToolBlockedByHook(m) {
		return false
	}
	if m.ToolStatus != "" {
		return m.ToolStatus == "failed"
	}
	return strings.Contains(m.Text, "[failed]")
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

// todoCardExpanded 返回待办卡片此刻是否展开：用户在步骤入口点过就以用户选择为准，
// 否则一律默认收起（运行中的实时进度由输入区胶囊显示）。
// running 参数为兼容调用方保留，不再参与默认开合判断。
func (s *chatState) todoCardExpanded(m Message, running bool) bool {
	s.mu.Lock()
	open, decided := s.expandedTool[todoCardKey(m.ID)]
	s.mu.Unlock()
	if decided {
		return open
	}
	return false
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

// renderTodoCard 在回合头正下方渲染逐行打勾的清单卡片：展开/收起走 motionUnfoldBody，
// 每步一行（左侧状态图标 + 步骤文本），不放标题行、摘要与进度条。
func renderTodoCard(c *ui.Context, s *chatState, m Message, items []todoItem) {
	running := s.isThreadRunning(selectedThreadIDSnapshot(s))
	expanded := s.todoCardExpanded(m, running)

	motionUnfoldBody(c, "todo-body-"+m.ID, expanded, func() {
		ui.Column(c.Key("msg-todo-"+m.ID)).
			FillWidth().
			MaxWidth(timelineContentMaxWidth(s)).
			Padding(Space3, Space4).
			Radius(RadiusLG).
			Background(ThemeSurface(c)).
			Border(1, ThemeBorder(c)).
			Gap(Space2).
			Children(func() {
				for i, it := range items {
					ui.Row(c.Key(fmt.Sprintf("todo-item-%s-%d", m.ID, i))).
						Gap(Space3).
						AlignItems(ui.Start).
						Children(func() {
							renderTodoStatusIcon(c, fmt.Sprintf("todo-icon-%s-%d", m.ID, i), it.Status, running)
							ui.Text(c, it.Text).
								FontSize(scaledFont(TypeFootnote)).
								LineHeight(1.4).
								TextColor(ThemePrimaryText(c))
						})
				}
			})
	})
}

// renderTodoStatusIcon 渲染待办项前的状态图标：待处理为空心圆，进行中在运行时为点阵动效、
// 停止后为强调色实心点，已完成为绿色圆圈对勾。
func renderTodoStatusIcon(c *ui.Context, key, status string, run bool) {
	switch status {
	case "completed":
		ui.Box(c.Key(key+"-box")).Size(11, 16).Center().Children(func() {
			ui.Icon(c, IconCircleCheck()).Size(11, 11).TextColor(ThemeSuccess(c))
		})
	case "in_progress":
		ui.Box(c.Key(key+"-box")).Size(11, 16).Center().Children(func() {
			if run {
				renderRunningGlyph(c, key+"-run", 9, ThemeAccent(c))
				return
			}
			ui.Box(c.Key(key+"-dot")).Size(9, 9).Radius(RadiusPill).Background(ThemeAccent(c))
		})
	default:
		ui.Box(c.Key(key+"-box")).Size(11, 16).Center().Children(func() {
			ui.Box(c.Key(key+"-ring")).Size(9, 9).Radius(RadiusPill).Border(1.5, ThemeBorder(c))
		})
	}
}
