// 工具调用分类、标题和分组。
package mcodeui

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// toolNameKey 规整工具名：小写、去掉 MCP 前缀与命名空间、去掉 _ 与 -，用于归类。
func toolNameKey(name string) string {
	key := strings.ToLower(strings.TrimSpace(name))
	if idx := strings.LastIndex(key, "__"); idx != -1 {
		key = key[idx+2:]
	}
	if idx := strings.LastIndexAny(key, "./:"); idx != -1 {
		key = key[idx+1:]
	}
	key = toolNameKeyStripper.Replace(key)
	switch key {
	case "readfile":
		return "read"
	case "writefile":
		return "write"
	case "editfile":
		return "edit"
	default:
		return key
	}
}

// classifyToolCategory 按工具名归类，供工具组摘要统计使用。
func classifyToolCategory(name string) toolCategory {
	key := toolNameKey(name)
	switch {
	case key == "":
		return toolCategoryOther
	case readToolNames[key]:
		return toolCategoryRead
	case searchToolNames[key]:
		return toolCategorySearch
	case fileEditToolNames[key]:
		return toolCategoryEdit
	case commandToolNames[key]:
		return toolCategoryCommand
	default:
		return toolCategoryOther
	}
}

// countToolGroupCategories 统计一组工具调用（含思考行）里各类别的出现次数。
func countToolGroupCategories(items []Message) toolGroupCounts {
	var counts toolGroupCounts
	for _, m := range items {
		counts.total++
		switch {
		case isReasoningMessage(m):
			counts.thoughts++
		case parseCommandToolInfo(m).IsCommand:
			counts.commands++
		case isFileEditTool(m):
			counts.edits++
		default:
			switch classifyToolCategory(toolMessageName(m)) {
			case toolCategoryCommand:
				counts.commands++
			case toolCategoryEdit:
				counts.edits++
			case toolCategoryRead:
				counts.reads++
			case toolCategorySearch:
				counts.searches++
			default:
				counts.others++
			}
		}
	}
	return counts
}

// toolGroupSummary 按类别统计一组工具调用（含思考行）并拼出中文摘要，计数为 0 的类别省略。
func toolGroupSummary(items []Message) string {
	return toolGroupSummaryText(countToolGroupCategories(items))
}

// toolGroupCategoryNames 只显示组内出现的类别。
func toolGroupCategoryNames(counts toolGroupCounts) string {
	var names []string
	if counts.thoughts > 0 {
		names = append(names, "思考")
	}
	if counts.commands > 0 {
		names = append(names, "命令")
	}
	if counts.edits > 0 {
		names = append(names, "编辑")
	}
	if counts.reads > 0 {
		names = append(names, "读取")
	}
	if counts.searches > 0 {
		names = append(names, "搜索")
	}
	if counts.others > 0 {
		names = append(names, "工具")
	}
	if len(names) == 0 {
		return "工具"
	}
	return strings.Join(names, " · ")
}

// toolGroupSummaryText 把分类计数拼成中文摘要，计数为 0 的类别省略。
func toolGroupSummaryText(counts toolGroupCounts) string {
	parts := make([]string, 0, 6)
	if counts.thoughts > 0 {
		parts = append(parts, fmt.Sprintf("思考 %d 次", counts.thoughts))
	}
	if counts.commands > 0 {
		parts = append(parts, fmt.Sprintf("运行 %d 条命令", counts.commands))
	}
	if counts.edits > 0 {
		parts = append(parts, fmt.Sprintf("编辑 %d 个文件", counts.edits))
	}
	if counts.reads > 0 {
		parts = append(parts, fmt.Sprintf("读取 %d 个文件", counts.reads))
	}
	if counts.searches > 0 {
		parts = append(parts, fmt.Sprintf("搜索 %d 次", counts.searches))
	}
	if counts.others > 0 {
		parts = append(parts, fmt.Sprintf("调用 %d 个工具", counts.others))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("已运行 %d 个工具", counts.total)
	}
	return strings.Join(parts, " · ")
}

// toolCallFields 返回工具调用的输入字段：优先解析 m.ToolInput 的完整 JSON，
// 拿不到（旧消息或输入被截断）时回退到从 m.Text 首行提取。
func toolCallFields(m Message) map[string]any {
	if fields := toolInputFields(m); len(fields) > 0 {
		return fields
	}
	return toolTextFieldsCached(m)
}

// toolTextFieldsCached 从 m.Text 首行提取回退字段，结果按消息 ID 缓存；
// 结果只取决于 m.Text，因此单帧重复调用不再重复解码 JSON。
// 返回的 map 只读，调用方不得修改；m.ID 为空时不走缓存。
func toolTextFieldsCached(m Message) map[string]any {
	if m.ID == "" {
		return toolDisplayFieldsFromText(m.Text)
	}
	toolParseCacheMu.Lock()
	e, hit := toolParseCacheLookup(m)
	if hit && e.hasTextFields {
		fields := e.textFields
		toolParseCacheMu.Unlock()
		return fields
	}
	toolParseCacheMu.Unlock()

	// 解析在锁外进行，避免与其它缓存路径嵌套加锁。
	fields := toolDisplayFieldsFromText(m.Text)

	toolParseCacheMu.Lock()
	if hit && e.matches(m) {
		e.textFields, e.hasTextFields = fields, true
	} else {
		// 失配（Text 或 ToolInput 变了）时新建条目覆盖；输入字段只取决于 ToolInput，
		// 它没变就沿用旧条目上已算好的结果，不丢也不重算。
		rebuilt := newToolParseCacheEntry(m)
		if hit && e.ToolInput == m.ToolInput {
			rebuilt.hasFields, rebuilt.fields = e.hasFields, e.fields
		}
		rebuilt.textFields, rebuilt.hasTextFields = fields, true
		toolParseCachePut(m, rebuilt)
	}
	toolParseCacheMu.Unlock()
	return fields
}

// toolFieldString 不区分大小写地取输入字段的第一个非空字符串值：
// 各家工具的字段名大小写并不统一（Path/path/filePath）。
func toolFieldString(fields map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		for k, v := range fields {
			if !strings.EqualFold(k, key) {
				continue
			}
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s), true
			}
		}
	}
	return "", false
}

// toolFieldInt 不区分大小写地取输入字段的整数值：JSON 数字与数字字符串都接受。
func toolFieldInt(fields map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		for k, v := range fields {
			if !strings.EqualFold(k, key) {
				continue
			}
			switch n := v.(type) {
			case float64:
				return int(n), true
			case string:
				if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
					return i, true
				}
			}
		}
	}
	return 0, false
}

// toolCallFirstStringValue 取输入里第一个非空字符串值（按字段名排序，保证每次渲染结果一致）。
func toolCallFirstStringValue(fields map[string]any) string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if s, ok := fields[k].(string); ok && strings.TrimSpace(s) != "" {
			return truncateCommandLine(s, 80)
		}
	}
	return ""
}

// toolCallPath 返回工具调用涉及的文件路径（完整路径，供 tooltip 与目录展示）；不涉及文件时返回空。
func toolCallPath(m Message) string {
	if p, ok := toolFieldString(toolCallFields(m), toolCallPathKeys...); ok {
		return p
	}
	// apply_patch 这类把路径写在补丁头或补丁正文里的工具交给提取逻辑
	if paths := toolCallEditPaths(m); len(paths) > 0 {
		return paths[0]
	}
	return ""
}

// toolPatchBodyPaths 从工具输入、文本与输出里的补丁正文中提取被修改的路径（去重，保持出现顺序）。
// 按 +++ b/（含删除文件时的 --- a/）→ diff --git → *** File: 的顺序尝试，先有结果的一层即返回。
func toolPatchBodyPaths(m Message) []string {
	src := m.ToolInput + "\n" + m.Text
	// 补丁正文有时被塞进 JSON 字符串，换行是转义后的 \n，先还原成真实换行再按行匹配
	src = strings.ReplaceAll(src, `\n`, "\n")
	for _, re := range []*regexp.Regexp{toolPatchFileLineRe, toolPatchGitLineRe, toolPatchHdrLineRe} {
		var paths []string
		seen := make(map[string]bool)
		for _, match := range re.FindAllStringSubmatch(src, -1) {
			p := strings.TrimSpace(match[1])
			if p == "" || p == "/dev/null" || seen[p] {
				continue
			}
			seen[p] = true
			paths = append(paths, p)
		}
		if len(paths) > 0 {
			return paths
		}
	}
	return nil
}

// toolCallEditPaths 返回编辑类工具涉及的全部文件路径（去重，保持出现顺序）：
// 先按大小写不敏感的路径字段（Path/path/filePath 等）取结构化的单个路径——此时以 ToolInput
// 为准，避免被截断的 Text 影响；其次用输入字段与 apply_patch 补丁头（toolEditedPaths），
// 拿不到时再从工具文本和输出里的补丁正文提取——入参为 "apply_patch {}" 时路径只存在于正文里。
func toolCallEditPaths(m Message) []string {
	// 这里直接读字段而不调 toolCallPath：toolCallPath 会回调本函数，互相调用会形成递归
	if isFileEditTool(m) {
		if p, ok := toolFieldString(toolCallFields(m), toolCallPathKeys...); ok {
			return []string{p}
		}
	}
	if paths := toolEditedPaths(m); len(paths) > 0 {
		return paths
	}
	if !isFileEditTool(m) {
		return nil
	}
	return toolPatchBodyPaths(m)
}

// toolCallEditVerb 区分「新建」与「编辑」：写入类工具返回「新建」，其余修改类工具返回「编辑」。
func toolCallEditVerb(name string) string {
	if createToolNames[toolNameKey(name)] {
		return "新建"
	}
	return "编辑"
}

// toolCallLineRange 返回读取类工具的行范围后缀（如 ":120–525"）：
// 优先 StartLine/EndLine，其次 offset/limit（offset 为起始行偏移），都没有时返回空。
func toolCallLineRange(fields map[string]any) string {
	start, hasStart := toolFieldInt(fields, "start_line", "startline", "start")
	end, hasEnd := toolFieldInt(fields, "end_line", "endline", "end")
	switch {
	case hasStart && hasEnd:
		return fmt.Sprintf(":%d–%d", start, end)
	case hasStart:
		return fmt.Sprintf(":%d", start)
	case hasEnd:
		return fmt.Sprintf(":1–%d", end)
	}
	offset, hasOffset := toolFieldInt(fields, "offset")
	limit, hasLimit := toolFieldInt(fields, "limit")
	switch {
	case hasOffset && hasLimit && limit > 0:
		return fmt.Sprintf(":%d–%d", offset, offset+limit)
	case hasOffset:
		return fmt.Sprintf(":%d", offset)
	}
	return ""
}

// toolCallTitle 生成工具行的可读标题，取代原来直翻的「工具名 {JSON}」截断文本：
// verb 是动词（读取/编辑/新建/搜索，命令为 "$"，其余工具用工具名），
// target 是主要对象（文件名、模式串、命令正文），detail 是补充信息（行范围、搜索范围）。
// 只从工具输入里取字段，不回显原始 JSON；字段名不区分大小写。
func toolCallTitle(m Message) (verb, target, detail string) {
	name := strings.TrimSpace(toolMessageName(m))
	originalName := name
	switch name {
	case "read_file", "write_file", "edit_file":
		name = toolNameKey(name)
	}
	fields := toolCallFields(m)

	switch classifyToolCategory(name) {
	case toolCategoryRead:
		if path := toolCallPath(m); path != "" {
			return "读取", filepath.Base(path), toolCallLineRange(fields)
		}
	case toolCategoryEdit:
		if paths := toolCallEditPaths(m); len(paths) > 0 {
			if len(paths) > 1 {
				return toolCallEditVerb(name), fmt.Sprintf("%d 个文件", len(paths)), ""
			}
			return toolCallEditVerb(name), filepath.Base(paths[0]), ""
		}
	case toolCategorySearch:
		if pattern, ok := toolFieldString(fields, toolCallPatternKeys...); ok {
			path, _ := toolFieldString(fields, toolCallSearchPathKeys...)
			return "搜索", `"` + truncateCommandLine(pattern, 60) + `"`, path
		}
	case toolCategoryCommand:
		if info := parseCommandToolInfo(m); info.Command != "" {
			return "$", info.Command, ""
		}
	}
	if isCommandTool(m) {
		if info := parseCommandToolInfo(m); info.Command != "" {
			return "$", info.Command, ""
		}
	}
	// 其余工具：动词用工具名，对象取第一个字符串参数；
	// 没有结构化输入时退回工具文本首行剩下的部分（旧消息或非 JSON 工具，如 "ls -la"），不回显 JSON
	if value := toolCallFirstStringValue(fields); value != "" {
		return name, value, ""
	}
	return name, toolCallTextRemainder(m, originalName), ""
}

// toolCallVerbTense 把工具行动词改成与当前状态一致的时态：运行中用「正在读取」「正在运行」，
// 结束后用「已读取」「已运行」，失败时按已完成（失败本身就是一种结束）处理。
// 命令行的「$」、未知工具名与空动词原样返回：这两类不是中文动词，分词时态反而难读。
func toolCallVerbTense(verb string, running, failed bool) string {
	done, doing, known := toolCallVerbTensePair(verb)
	switch {
	case !known:
		return verb
	case failed:
		return done
	case running:
		return doing
	default:
		return done
	}
}

// toolCallVerbTensePair 返回一个中文动词的完成体与进行体；不是内置动词时 known 为 false。
func toolCallVerbTensePair(verb string) (done, doing string, known bool) {
	switch verb {
	case "读取":
		return "已读取", "正在读取", true
	case "编辑":
		return "已编辑", "正在编辑", true
	case "新建":
		return "已新建", "正在新建", true
	case "搜索":
		return "已搜索", "正在搜索", true
	}
	return "", "", false
}

// toolCallVerb 返回工具行的中文动词：按工具状态选时态，命令行统一用 "$"。
func toolCallVerb(verb string, m Message) string {
	// 后台任务仍由 provider 继续执行，按进行时显示（「正在读取」），和运行中一致。
	return toolCallVerbTense(verb, isToolRunning(m) || isToolBackground(m), isToolFailed(m))
}

// toolCallTextRemainder 返回工具文本首行去掉工具名后的剩余部分；没有剩余内容或剩余内容以 JSON 开头时返回空。
func toolCallTextRemainder(m Message, name string) string {
	line := m.Text
	if idx := strings.IndexByte(line, '\n'); idx != -1 {
		line = line[:idx]
	}
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), name))
	if rest == "" || strings.HasPrefix(rest, "{") {
		return ""
	}
	return truncateCommandLine(rest, 80)
}

// toolGroupStartTime 返回组内第一条工具消息的时间戳；组里没有带时间戳的工具行时返回零值。
func toolGroupStartTime(tools []Message) time.Time {
	for _, m := range tools {
		if m.Role != "tool" {
			continue
		}
		if at, ok := messageTime(m); ok {
			return at
		}
	}
	return time.Time{}
}

// toolGroupClock 把组内首条工具消息的时刻格式化为组头右侧的 HH:mm（本地时区），零值返回空。
func toolGroupClock(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.Local().Format("15:04")
}

// toolGroupHeaderTooltip 给组头摘要补上完整日期时间，方便悬停确认这组命令是什么时候跑的。
func toolGroupHeaderTooltip(summary string, at time.Time) string {
	if at.IsZero() {
		return summary
	}
	return summary + " · " + at.Local().Format("2006-01-02 15:04:05")
}

// renderToolGroupItem 渲染多个连续工具调用的合并组头（按 2.5 节）：一行文字摘要（如「用时 3.2s · 思考 4 次 · 运行 5 条命令」），
// 后面紧跟展开箭头，整行可点击展开或收起。展开后的节拍由列表虚拟化为独立行（renderToolBeatItem）。
// liveID 命中组内消息，或这个组就是运行中会话末尾的活动段时，摘要前显示运行中的点阵图标，
// 摘要带「工作中 · 」前缀、耗时每秒刷新，折叠态还会在组头下方列出最近几条工具行的只读预览。
func renderToolGroupItem(c *ui.Context, s *chatState, tools []Message, groupID string, liveID string, defaultOpen bool) ui.Element {
	// 展开状态与 buildTimelineItems 的判定保持一致：用户显式设置过就以设置为准，
	// 否则用默认值（所有组默认折叠，运行中的组靠组头实时耗时与折叠预览显示进度）。
	s.mu.Lock()
	explicitOpen, hasExplicit := s.expandedTool[groupID]
	s.mu.Unlock()
	open := defaultOpen
	if hasExplicit {
		open = explicitOpen
	}

	// 关闭“显示思考过程”时，摘要不能再统计思考次数
	summaryTools := tools
	if !isShowReasoningEnabled(s) {
		filtered := make([]Message, 0, len(tools))
		for _, m := range tools {
			if !isReasoningMessage(m) {
				filtered = append(filtered, m)
			}
		}
		summaryTools = filtered
	}
	btnLabel := "展开"
	arrowIcon := IconChevronRight()
	if open {
		btnLabel = "收起"
		arrowIcon = IconChevronDown()
	}
	running := toolGroupLive(s, tools, liveID)
	headerText := toolGroupCategoryNames(countToolGroupCategories(summaryTools))
	groupAt := toolGroupStartTime(tools)

	return ui.Column(c.Key("msg-group-" + groupID)).FillWidth().MaxWidth(timelineContentMaxWidth(s)).Children(func() {
		header := ui.Row(c.Key("group-header-"+groupID)).
			Gap(Space2).
			AlignItems(ui.Center).
			Padding(Space0, Space2).
			Radius(RadiusSM).
			MinHeight(24).
			Cursor(ui.CursorPointer).
			Label(btnLabel).
			Tooltip(toolGroupHeaderTooltip(headerText, groupAt)).
			OnClick(func() {
				s.setToolGroupOpen(groupID, !open)
			})
		// hover 时摘要变为主色，只铺一层很淡的底，避免组头看起来像一个按钮
		hovered := header.Hovered()
		textColor := ThemeSecondaryText(c)
		if hovered {
			header.Background(ThemeHover(c).Alpha(0.35))
			textColor = ThemePrimaryText(c)
		}
		header.Children(func() {
			if running {
				// running 成立即保证组内至少有一条消息，首条消息 ID 在同一视图内唯一。
				renderRunningGlyph(c, "tool-group-run-"+tools[0].ID, 12, ThemeSecondaryText(c))
				// 实时耗时每秒走一格，请求下一秒再重绘一行
				c.After(time.Second)
			}
			// 摘要已带上“工作中 · / 用时 Xs · ”前缀，耗时不再单独显示一次
			ui.Text(c, headerText).FontSize(scaledFont(TypeBody)).TextColor(textColor)
			ui.Icon(c, arrowIcon).Size(11, 11).TextColor(textColor)
			// 右侧时刻位：默认透明，组头 hover 或展开时淡入；组内没有时间戳就不画。
			if clock := toolGroupClock(groupAt); clock != "" {
				ui.Spacer(c)
				reveal := float32(0)
				if hovered || open {
					reveal = 1
				}
				stamp := ui.Text(c.Key("group-time-"+groupID), clock).
					FontSize(scaledFont(TypeCaption)).
					TextColor(ThemeSecondaryText(c)).
					Width(44).
					TextAlign(ui.End)
				stamp.Opacity(stamp.AnimateTo("tool-group-time-reveal", reveal, ui.Animation{Duration: animateDuration(c, motionFast), Ease: motionEaseStandard}))
			}
		})
		// 运行中且折叠时，组头下方给最近几条工具行做只读预览；组跑完或展开后不再渲染。
		if running && !open {
			renderToolGroupPreview(c, s, tools, groupID)
		}
	})
}

// toolGroupLive 判断这个工具组是不是当前会话末尾仍在进行的活动组：组内命中了正在流式输出的消息，
// 或线程仍在运行且组内最后一条消息就是整个会话的最后一条消息（即末尾活动段由它收尾）。
// 每次渲染都重新判定，组跑完或回合结束时立刻回到普通折叠行，不依赖缓存里的旧状态。
func toolGroupLive(s *chatState, tools []Message, liveID string) bool {
	if len(tools) == 0 {
		return false
	}
	if liveID != "" && containsMessageID(tools, liveID) {
		return true
	}
	s.mu.Lock()
	lastID := ""
	if len(s.messages) > 0 {
		lastID = s.messages[len(s.messages)-1].ID
	}
	threadID := s.selectedThreadID
	s.mu.Unlock()
	if lastID == "" || lastID != tools[len(tools)-1].ID {
		return false
	}
	return s.isThreadRunning(threadID)
}

// toolGroupPreviewTools 返回折叠预览要显示的工具行：组内最后 limit 条工具消息，按原始顺序排列；
// 工具不足 limit 条时返回全部。
func toolGroupPreviewTools(tools []Message, limit int) []Message {
	if limit <= 0 {
		return nil
	}
	picked := make([]Message, 0, limit)
	for i := len(tools) - 1; i >= 0 && len(picked) < limit; i-- {
		if tools[i].Role != "tool" {
			continue
		}
		picked = append(picked, tools[i])
	}
	slices.Reverse(picked)
	return picked
}

// renderToolGroupPreview 渲染运行中工具组折叠态的预览：以节拍相同的缩进和间距竖排最近几条工具。
// 预览只读，不挂点击与展开，组跑完或用户展开后自然消失。
func renderToolGroupPreview(c *ui.Context, s *chatState, tools []Message, groupID string) {
	preview := toolGroupPreviewTools(tools, toolGroupPreviewCount)
	if len(preview) == 0 {
		return
	}
	ui.Column(c.Key("group-preview-"+groupID)).
		FillWidth().
		Gap(Space1).
		Padding(Hairline, Space2, Space0, toolBeatPadLeft).
		Children(func() {
			for _, m := range preview {
				renderToolChipReadOnly(c, s, m)
			}
		})
}

// renderToolChipReadOnly 渲染预览里的只读胶囊：状态字形、类别标记、短标题与耗时与普通胶囊一致，
// 但不响应点击，也不展开工具详情。
func renderToolChipReadOnly(c *ui.Context, s *chatState, m Message) {
	verb, target, detail := toolCallTitle(m)
	if isToolBackground(m) {
		// 后台任务仍由 provider 继续执行，动词用进行时，与普通胶囊一致。
		verb = toolCallVerb(verb, m)
	}
	cmdInfo := parseCommandToolInfo(m)
	category := classifyToolCategory(toolMessageName(m))
	duration := cmdInfo.Duration
	if duration == "" {
		duration = toolElapsedText(m)
	}
	// 只读预览里的后台任务同样显示实时运行时长，避免被误读成已完成。
	if toolText, ok := toolBackgroundBadge(c, m); ok {
		duration = toolText
	}

	var appCfg config.AppearanceConfig
	if st := getChatSettings(s); st != nil {
		appCfg = st.cfg.Appearance
	}
	fontFam := codeFontFamily(appCfg)
	label := "预览 " + toolCallRowLabel(verb, target, detail)

	chip := ui.Row(c.Key("tool-chip-preview-"+m.ID)).
		AlignSelf(ui.Start).
		Gap(Space2).
		Height(toolChipHeight).
		Radius(toolChipHeight/2).
		Padding(0, Space3).
		Label(label).
		Tooltip(toolCallRowLabel(verb, target, detail)).
		Background(ThemeSecondaryText(c).Alpha(0.06))
	if isToolFailed(m) {
		chip.Border(1, ThemeDanger(c))
	}

	chip.Children(func() {
		renderToolChipStatus(c, m)
		renderToolChipCategoryMark(c, category, cmdInfo.IsCommand, fontFam)
		title := ui.Text(c, toolChipShortTitle(m, verb, target)).
			FontSize(scaledFont(TypeFootnote)).
			TextColor(ThemeSecondaryText(c)).
			SingleLine()
		if cmdInfo.IsCommand && toolChipDescription(m) == "" {
			title.Font(fontFam)
		}
		if duration != "" {
			ui.Text(c, duration).
				FontSize(scaledFont(TypeCaption)).
				TextColor(ThemeSecondaryText(c).Alpha(0.85))
		}
	})
}

// toolGroupElapsedText 返回整组耗时（如 "3.2s"）。
// 运行中的组用 now 减去组内第一条工具消息的开始时间（实时耗时，随每秒重绘增长）；
// 跑完的组由首尾消息的时间戳相减得出。拿不到时间时返回空。
func toolGroupElapsedText(tools []Message, running bool, now time.Time) string {
	if len(tools) == 0 {
		return ""
	}
	if running {
		// 组首可能是思考行，实时耗时从第一条工具调用开始算
		startMsg := tools[0]
		for _, m := range tools {
			if m.Role == "tool" {
				startMsg = m
				break
			}
		}
		start, ok := parseRFC3339Time(startMsg.CreatedAt)
		if !ok || !now.After(start) {
			return ""
		}
		return formatShortDuration(now.Sub(start))
	}
	if len(tools) < 2 {
		return ""
	}
	start, ok := parseRFC3339Time(tools[0].CreatedAt)
	if !ok {
		return ""
	}
	last := tools[len(tools)-1]
	end, ok := parseRFC3339Time(last.ToolCompletedAt)
	if !ok {
		end, ok = parseRFC3339Time(last.CreatedAt)
	}
	if !ok || !end.After(start) {
		return ""
	}
	return formatShortDuration(end.Sub(start))
}

// toolGroupRunningHint 返回最近一条运行中命令的摘要与并行条数：
// 摘要复用胶囊标题（有 description 时用 description，否则是精简后的命令本身），不携带命令输出。
func toolGroupRunningHint(tools []Message) (string, int) {
	snippet := ""
	running := 0
	for _, m := range tools {
		if !toolIsRunningCommand(m) {
			continue
		}
		running++
		verb, target, _ := toolCallTitle(m)
		if text := strings.TrimSpace(toolChipShortTitle(m, verb, target)); text != "" {
			snippet = truncateRunes(text, 24)
		}
	}
	return snippet, running
}

// toolGroupHeaderSummaryText 给组头摘要加上状态前缀：运行中为「工作中 · 」并在末尾报告正在跑的命令，
// 完成且有耗时为「用时 Xs · 」，否则只有现有的分类计数文本。
func toolGroupHeaderSummaryText(counts toolGroupCounts, running bool, elapsed string, tools []Message) string {
	summary := toolGroupSummaryText(counts)
	if running {
		text := "工作中 · " + summary
		if snippet, n := toolGroupRunningHint(tools); snippet != "" {
			text += " · 正在运行 " + snippet
			if n > 1 {
				text += fmt.Sprintf(" 等 %d 条", n)
			}
		}
		return text
	}
	if elapsed != "" {
		return "用时 " + elapsed + " · " + summary
	}
	return summary
}

// toolGroupOpen 返回工具组的实际开合状态：用户显式设置过就以设置为准，
// 没有设置过时用默认值（所有组默认折叠，运行中的组靠组头耗时与预览显示进度）。
func toolGroupOpen(expanded map[string]bool, groupID string, defaultOpen bool) bool {
	if v, ok := expanded[groupID]; ok {
		return v
	}
	return defaultOpen
}

// setToolGroupOpen 显式记录工具组的开合状态，覆盖默认值
// （运行中的活动组默认展开，用户点一下后按用户设置收起）。
func (s *chatState) setToolGroupOpen(groupID string, open bool) {
	s.mu.Lock()
	if s.expandedTool == nil {
		s.expandedTool = make(map[string]bool)
	}
	s.expandedTool[groupID] = open
	s.expandedRev++
	s.mu.Unlock()
	s.holdChatPlace()
	s.invalidate()
}
