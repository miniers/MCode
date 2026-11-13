// 工具调用状态、卡片与差异统计。
package mcodeui

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"math"
	"mcode/internal/config"
	"mcode/internal/git"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// toolBackgroundRunDuration 计算后台任务从创建到现在的运行时长，并按秒请求下一帧，
// 让时长像运行中的命令一样每秒刷新；时间戳缺失或解析失败时 ok 为 false。
func toolBackgroundRunDuration(c *ui.Context, m Message) (string, bool) {
	start, okStart := parseRFC3339Time(m.CreatedAt)
	if !okStart {
		return "", false
	}
	elapsed := c.Now().Sub(start)
	if elapsed < 0 {
		elapsed = 0
	}
	c.After(time.Second)
	return formatToolRunDuration(elapsed), true
}

// toolBackgroundBadge 返回后台任务胶囊上的状态文本「后台 · 12s」；不是后台任务时 ok 为 false。
// 缺少创建时间时只显示「后台」，时长由下一秒的刷新补上。
func toolBackgroundBadge(c *ui.Context, m Message) (text string, ok bool) {
	if !isToolBackground(m) {
		return "", false
	}
	duration, okStart := toolBackgroundRunDuration(c, m)
	if !okStart {
		return "后台", true
	}
	return "后台 · " + duration, true
}

// toolElapsedText 由工具消息的创建与完成时间计算显示用耗时；缺少时间戳时返回空。
func toolElapsedText(m Message) string {
	start, okStart := parseRFC3339Time(m.CreatedAt)
	end, okEnd := parseRFC3339Time(m.ToolCompletedAt)
	if !okStart || !okEnd {
		return ""
	}
	d := end.Sub(start)
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// isToolRunning 判断工具是否正在执行。
func isToolRunning(m Message) bool {
	return strings.EqualFold(strings.TrimSpace(m.ToolStatus), "running")
}

// isToolBackground 判断工具是否是 provider 托管的后台任务：工具已交给供应商在后台继续跑，
// 消息里没有完成时间（ToolCompletedAt 为空），结束后状态会变回 completed / failed。
func isToolBackground(m Message) bool {
	return strings.EqualFold(strings.TrimSpace(m.ToolStatus), "background")
}

// formatToolRunDuration 把运行时长格式化为「12s」「3m05s」「1h02m」；负值按 0 处理。
func formatToolRunDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d/time.Minute), int((d%time.Minute)/time.Second))
	default:
		return fmt.Sprintf("%dh%02dm", int(d/time.Hour), int((d%time.Hour)/time.Minute))
	}
}

// toolIsRunningCommand 判断工具消息是否是一条仍在运行的命令类工具：
// 状态为 running，或状态未知且还没有完成时间戳；已 completed / failed / blocked 的一律算结束。
// provider 托管的后台任务（background）也按结束态处理：它不归本轮管，
// 卡片里另有实时时长，不应再给出终止按钮或输出停滞徽标。
func toolIsRunningCommand(m Message) bool {
	if !isCommandTool(m) {
		return false
	}
	if isToolRunning(m) {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(m.ToolStatus)) {
	case "completed", "failed", "error", "blocked", "cancelled", "canceled", "background":
		return false
	}
	return strings.TrimSpace(m.ToolCompletedAt) == ""
}

// observeToolRun 记录这条运行中命令工具本次的输出长度与变化时刻，返回已运行时长与
// 距上次输出变化的时长；时间戳解析不了时 ok 为 false。
// 每帧渲染时调用一次：输出长度变化就刷新时刻，消息不再运行就删掉记录，避免 map 泄漏。
func (s *chatState) observeToolRun(m Message, now time.Time) (elapsed, idleFor time.Duration, ok bool) {
	start, okStart := parseRFC3339Time(m.CreatedAt)
	if !okStart {
		return 0, 0, false
	}
	elapsed = now.Sub(start)
	if elapsed < 0 {
		elapsed = 0
	}

	s.mu.Lock()
	if !toolIsRunningCommand(m) {
		delete(s.toolRunIdle, m.ID)
		delete(s.commandActivity, m.ID)
		s.mu.Unlock()
		return elapsed, 0, true
	}
	if s.toolRunIdle == nil {
		s.toolRunIdle = make(map[string]toolRunIdleState)
	}
	cur := s.toolRunIdle[m.ID]
	if cur.changedAt.IsZero() || cur.textLen != len(m.Text) {
		cur.textLen = len(m.Text)
		cur.changedAt = now
	}
	s.toolRunIdle[m.ID] = cur
	s.mu.Unlock()

	idleFor = now.Sub(cur.changedAt)
	if idleFor < 0 {
		idleFor = 0
	}
	return elapsed, idleFor, true
}

// threadIsBusy 判断指定会话这一轮是否还没结束（会话自己在跑或名下子线程仍在跑）；
// 会话 ID 为空或找不到该会话时按「不在运行」处理。
func (s *chatState) threadIsBusy(threadID string) bool {
	if s == nil || threadID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, th := range s.threads {
		if th.ID == threadID {
			return th.IsRunning || th.SwarmBusy
		}
	}
	return false
}

// runningCommandBadge 返回运行中命令工具胶囊上的时长文本与「长时间无输出」标记；
// 不是运行中的命令工具，或时间戳解析不了时返回空文本。
// 当前会话确实还在运行时才按秒取下一帧（与 renderBeeWindow 同一套机制），让时长每秒刷新；
// 历史/中断会话里状态未知的工具不再触发每秒整窗重建。
func (s *chatState) runningCommandBadge(c *ui.Context, m Message) (text string, stalled bool) {
	if !toolIsRunningCommand(m) {
		return "", false
	}
	elapsed, idleFor, ok := s.observeToolRun(m, c.Now())
	if !ok {
		return "", false
	}
	now := c.Now()
	threadID := selectedThreadIDSnapshot(s)
	s.mu.Lock()
	ensureCommandActivityLocked(s, m)
	s.mu.Unlock()
	if elapsed >= commandActivityStartAfter {
		requestCommandActivity(c, s, threadID, now)
	}
	activity := commandActivitySnapshot(s, m.ID)
	// 状态明确为 running 的工具照旧每秒刷新时长；其余靠兜底判为运行中的工具，
	// 只有当前会话仍在跑（IsRunning 或 SwarmBusy）时才继续每秒重建。
	if isToolRunning(m) || s.threadIsBusy(selectedThreadIDSnapshot(s)) {
		c.After(time.Second)
	}
	text = formatToolRunDuration(elapsed)
	if len(activity.steps) >= 2 && activity.maxStep > 0 {
		text = fmt.Sprintf("%d/%d · %s", activity.maxStep, len(activity.steps), text)
	}
	if activity.estimateSet && activity.estimate > elapsed {
		text += " / ~" + formatToolRunDuration(activity.estimate)
	}
	if activity.hasProcess {
		if activity.sampleCount >= 2 && elapsed >= toolRunStallMinRun && idleFor >= toolRunStallAfter && now.Sub(activity.lastBusyAt) >= toolRunStallAfter {
			return text + " · 空闲", true
		}
		return fmt.Sprintf("%s · CPU %.0f%%", text, activity.cpu), false
	}
	lastStep := ""
	if len(activity.steps) > 0 {
		lastStep = activity.steps[len(activity.steps)-1]
	}
	if elapsed >= toolRunStallMinRun && idleFor >= toolRunStallAfter && !commandActivityNoOutputExpected(lastStep) {
		return text + " · 无输出", true
	}
	return text, false
}

// toolCallRowLabel 把标题拼成无障碍 Label（如 "读取 timeline_rows.go :120–525"），
// 屏幕阅读器与测试都靠它定位整行。
func toolCallRowLabel(verb, target, detail string) string {
	label := strings.TrimSpace(strings.Join([]string{verb, target, detail}, " "))
	if label == "" {
		return "工具调用"
	}
	return label
}

// renderSingleToolItem 渲染单条工具调用：一个左对齐的动作胶囊，点开后下方展开详情（按 2.4 节）。
func renderSingleToolItem(c *ui.Context, s *chatState, m Message) ui.Element {
	// 待办清单类工具（TodoWrite / update_plan）渲染成带状态图标的卡片
	if items, ok := todoItemsFromMessage(m); ok {
		return renderTodoCard(c, s, m, items)
	}

	open := s.toolMessageExpanded(m)

	// 单条工具没有节拍同伴，传自己即可满足“同一节拍只展开一个”的约定
	siblings := []Message{m}
	return ui.Column(c.Key("msg-tool-" + m.ID)).FillWidth().MaxWidth(timelineContentMaxWidth(s)).Gap(Space1).Children(func() {
		ui.Row(c.Key("tool-chips-" + m.ID)).
			Gap(Space2).
			Wrap().
			AlignItems(ui.Center).
			Children(func() {
				renderToolChip(c, s, m, siblings)
			})
		if open {
			renderToolDetail(c, s, m)
		}
	})
}

// toolMessageExpanded 默认展开等待审批的入参，用户选择优先。
func (s *chatState) toolMessageExpanded(m Message) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	open, explicit := s.expandedTool[m.ID]
	return open || !explicit && toolMessageState(m) == ToolPending
}

// toggleToolChip 展开或收起一个动作胶囊的详情：同一节拍同一时刻只展开一个，
// 点开的胶囊同时收起同节拍里的其它胶囊；编辑类工具展开时直接带上这次调用的改动。
func (s *chatState) toggleToolChip(m Message, beatTools []Message) {
	s.mu.Lock()
	if s.expandedTool == nil {
		s.expandedTool = make(map[string]bool)
	}
	previous, explicit := s.expandedTool[m.ID]
	open := !(previous || !explicit && toolMessageState(m) == ToolPending)
	for _, other := range beatTools {
		if other.ID != m.ID {
			s.expandedTool[other.ID] = false
		}
	}
	s.expandedTool[m.ID] = open
	s.expandedRev++
	s.mu.Unlock()
	s.holdChatPlace()

	// 编辑类工具：展开胶囊时直接显示这次调用的改动，收起时一并关掉
	// （走 toggleToolDiff 以复用它在重新展开时清掉失败缓存的逻辑）。
	if isFileEditTool(m) {
		if open != s.isToolDiffOpen(m.ID) {
			s.toggleToolDiff(m)
			return
		}
	}
	s.invalidate()
}

// hideToolDetail 收起某个工具的详情区与内嵌改动。
func (s *chatState) hideToolDetail(m Message) {
	s.mu.Lock()
	if s.expandedTool == nil {
		s.expandedTool = make(map[string]bool)
	}
	s.expandedTool[m.ID] = false
	if s.toolDiff.open != nil {
		s.toolDiff.open[m.ID] = false
	}
	s.expandedRev++
	s.mu.Unlock()
	s.holdChatPlace()
	s.invalidate()
}

// toolChipShortTitle 生成胶囊里的短标题（基于 toolCallTitle）：命令优先用输入里的 description
// （自然语言意图，截断到 40 个字符），没有 description 时从命令正文里语义提取出主命令
// （截断到 48 个字符）；读取与编辑显示文件名，搜索显示 pattern（截断到 24 个字符），
// 其余工具显示工具名加对象。
func toolChipShortTitle(m Message, verb, target string) string {
	category := classifyToolCategory(toolMessageName(m))
	if parseCommandToolInfo(m).IsCommand || category == toolCategoryCommand {
		if desc := toolChipDescription(m); desc != "" {
			return truncateRunes(desc, 40)
		}
		return truncateRunes(toolChipCommandShort(target), 48)
	}
	switch category {
	case toolCategoryRead, toolCategoryEdit:
		return target
	case toolCategorySearch:
		return truncateRunes(strings.Trim(target, `"`), 24)
	}
	return strings.TrimSpace(strings.TrimSpace(verb) + " " + target)
}

// toolChipDescription 取命令工具输入里的 description 字段（各家 agent 用它写一句自然语言意图），
// 规整为单行；没有该字段或内容为空时返回空串。
func toolChipDescription(m Message) string {
	desc, ok := toolFieldString(toolCallFields(m), "description")
	if !ok {
		return ""
	}
	return strings.Join(strings.Fields(desc), " ")
}

// toolChipCommandShort 从命令正文里语义提取最能说明意图的一段，用于胶囊标题：
// 先取首行并剥掉 bash -c / sh -lc 这类包装，再按 && || ; | 切段跳过 cd、export 之类的噪声段，
// for/while 循环取循环体里的主命令（前缀 for: / while:），然后去掉 rtk/env/sudo 等包装前缀
// 与 KEY=VALUE 赋值，最后保留最多 4 个 token（其中的路径换成文件名）。
// 提取不到时退回旧行为：首行的前两个 token。
func toolChipCommandShort(cmd string) string {
	line := cmd
	if idx := strings.IndexByte(line, '\n'); idx != -1 {
		line = line[:idx]
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}

	segments := toolChipCommandSegments(toolChipShellScript(line))
	main, prefix := "", ""
	for _, seg := range segments {
		if toolChipNoiseSegment(seg) {
			continue
		}
		main = seg
		if strings.HasPrefix(seg, "for ") || strings.HasPrefix(seg, "while ") {
			prefix = "for: "
			if strings.HasPrefix(seg, "while ") {
				prefix = "while: "
			}
			if body := toolChipLoopBody(segments); body != "" {
				main = body
			}
		}
		break
	}

	tokens := toolChipUnwrapTokens(strings.Fields(main))
	if len(tokens) == 0 {
		return toolChipCommandBeeShort(line)
	}
	if len(tokens) > 4 {
		tokens = tokens[:4]
	}
	toolChipBaseNames(tokens)
	return prefix + strings.Join(tokens, " ")
}

// toolChipShellScript 剥掉 `bash -c '...'`、`sh -lc "..."` 这类包装，返回引号内的脚本；
// 不是这种包装时原样返回。只去掉最外层引号，不解析转义。
func toolChipShellScript(cmd string) string {
	fields := strings.Fields(cmd)
	if len(fields) < 3 {
		return cmd
	}
	switch fields[0] {
	case "bash", "sh", "zsh":
	default:
		return cmd
	}
	scriptAt := -1
	for i := 1; i < len(fields); i++ {
		if strings.HasPrefix(fields[i], "-") && strings.HasSuffix(fields[i], "c") {
			scriptAt = i
			break
		}
	}
	if scriptAt < 0 || scriptAt == len(fields)-1 {
		return cmd
	}
	script := strings.Join(fields[scriptAt+1:], " ")
	if len(script) >= 2 {
		first, last := script[0], script[len(script)-1]
		if (first == '\'' && last == '\'') || (first == '"' && last == '"') {
			script = script[1 : len(script)-1]
		}
	}
	return strings.TrimSpace(script)
}

// toolChipCommandSegments 按顶层分隔符 && || ; | 把脚本切成命令段（不处理引号内的分隔符）。
func toolChipCommandSegments(script string) []string {
	replacer := strings.NewReplacer("&&", "\n", "||", "\n", ";", "\n", "|", "\n")
	var segments []string
	for _, seg := range strings.Split(replacer.Replace(script), "\n") {
		if seg = strings.TrimSpace(seg); seg != "" {
			segments = append(segments, seg)
		}
	}
	return segments
}

// toolChipNoiseSegment 判断命令段是否只是噪声（空段，或 cd/export/set -x 之类的准备动作）。
func toolChipNoiseSegment(seg string) bool {
	seg = strings.TrimSpace(seg)
	if seg == "" {
		return true
	}
	for _, p := range []string{"cd ", "pushd ", "export ", "set -", "source ", ". "} {
		if strings.HasPrefix(seg, p) {
			return true
		}
	}
	return false
}

// toolChipLoopBody 返回 for/while 循环体里的第一个非噪声命令段，找不到时返回空串。
func toolChipLoopBody(segments []string) string {
	for i, seg := range segments {
		body := ""
		switch {
		case seg == "do":
		case strings.HasPrefix(seg, "do "):
			body = strings.TrimSpace(strings.TrimPrefix(seg, "do "))
		default:
			continue
		}
		if body != "" && !toolChipNoiseSegment(body) {
			return body
		}
		for _, next := range segments[i+1:] {
			if next == "done" {
				return ""
			}
			if !toolChipNoiseSegment(next) {
				return next
			}
		}
		return ""
	}
	return ""
}

// toolChipUnwrapTokens 反复去掉开头的包装前缀 token（rtk/env/time/sudo 等）与 KEY=VALUE 环境变量赋值。
func toolChipUnwrapTokens(fields []string) []string {
	for len(fields) > 0 {
		switch fields[0] {
		case "rtk", "env", "time", "sudo", "nohup", "exec":
			fields = fields[1:]
			continue
		}
		if toolChipEnvAssignment(fields[0]) {
			fields = fields[1:]
			continue
		}
		break
	}
	return fields
}

// toolChipEnvAssignment 判断 token 是否是 KEY=VALUE 形式的环境变量赋值。
func toolChipEnvAssignment(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 || strings.HasPrefix(tok, "-") {
		return false
	}
	for _, r := range tok[:eq] {
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// toolChipBaseNames 把 token 列表里含路径分隔符的路径换成文件名（原地修改）。
func toolChipBaseNames(tokens []string) {
	for i, tok := range tokens {
		if !strings.HasPrefix(tok, "-") && strings.ContainsAny(tok, `/\`) {
			tokens[i] = filepath.Base(tok)
		}
	}
}

// toolChipCommandBeeShort 是旧行为：取命令首行的前两个 token，并把其中的路径换成文件名。
func toolChipCommandBeeShort(line string) string {
	fields := strings.Fields(line)
	if len(fields) > 2 {
		fields = fields[:2]
	}
	toolChipBaseNames(fields)
	return strings.Join(fields, " ")
}

// truncateRunes 把文本截断到最多 n 个字符，超出时补省略号。
func truncateRunes(text string, n int) string {
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[:n]) + "…"
}

// toolMessageState 将消息状态映射为统一的工具状态。
func toolMessageState(m Message) ToolState {
	switch {
	case isToolFailed(m) || isToolBlockedByHook(m):
		return ToolError
	case strings.EqualFold(strings.TrimSpace(m.ToolStatus), "pending"):
		return ToolPending
	case isToolRunning(m) || isToolBackground(m):
		return ToolRunning
	default:
		return ToolDone
	}
}

func toolStateMarker(state ToolState) (toolMarkerShape, Tone) {
	switch state {
	case ToolRunning:
		return toolMarkerSpinner, ToneAccent
	case ToolDone:
		return toolMarkerCheck, ToneSuccess
	case ToolError:
		return toolMarkerAlert, ToneDanger
	default:
		return toolMarkerHollow, ToneNeutral
	}
}

// renderToolChipStatus 渲染工具状态形状及语义色。
func renderToolChipStatus(c *ui.Context, m Message) {
	shape, tone := toolStateMarker(toolMessageState(m))
	color := ThemeTertiaryText(c)
	switch tone {
	case ToneAccent:
		color = ThemeAccent(c)
	case ToneSuccess:
		color = ThemeSuccess(c)
	case ToneDanger:
		color = ThemeDanger(c)
	}
	switch shape {
	case toolMarkerSpinner:
		LoadingIndicator(c, nil, "", IconSM, color)
	case toolMarkerHollow:
		ui.Box(c).Size(IconSM, IconSM).Radius(RadiusPill).Border(Space1/4, color)
	case toolMarkerCheck:
		ui.Icon(c, IconCheck()).Size(IconSM, IconSM).TextColor(color)
	case toolMarkerAlert:
		ui.Icon(c, IconAlertTriangle()).Size(IconSM, IconSM).TextColor(color)
	}
}

// renderToolChipCategoryMark 渲染胶囊里的类别标记：命令用等宽的 $，编辑用笔图标，读取用眼睛图标，
// 搜索用放大镜图标，其余工具用扳手图标。
func renderToolChipCategoryMark(c *ui.Context, category toolCategory, isCommand bool, fontFam string) {
	if isCommand {
		ui.Text(c, "$").Font(fontFam).FontSize(scaledFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
		return
	}
	icon := IconWrench()
	switch category {
	case toolCategoryEdit:
		icon = IconPencilLine()
	case toolCategoryRead:
		icon = IconEye()
	case toolCategorySearch:
		icon = IconGlobe()
	}
	ui.Icon(c, icon).Size(11, 11).TextColor(ThemeSecondaryText(c))
}

// renderToolChipTitle 渲染工具胶囊的标题：运行中时在标题上扫一道高光。扫光动画与常量
// 统一交给 renderShimmerTitle，与后台任务卡、运行中的回合头共用同一套实现；
// 离开 running / background 状态后高光消失。
func renderToolChipTitle(c *ui.Context, m Message, fn func() ui.Element) {
	renderShimmerTitle(c, "tool-shimmer-"+m.ID, toolShimmerBandLabel, isToolRunning(m) || isToolBackground(m), fn)
}

// renderToolChip 渲染一个动作胶囊：状态字形、类别标记、短标题、diff 统计与耗时，点击展开或收起这个工具的详情。
// beatTools 是同一节拍里的全部工具，用于保证同一节拍同时只展开一个胶囊。
// 编辑类工具的胶囊本身就是这次改动的开关，它的 Label 用「查看改动 <文件名>」/「收起改动 <文件名>」，
// 其余工具用完整标题（toolCallRowLabel），测试与屏幕阅读器都按它定位。
func renderToolChip(c *ui.Context, s *chatState, m Message, beatTools []Message) {
	verb, target, detail := toolCallTitle(m)
	// 动词按时态显示：运行中「正在读取」、完成后「已读取」；命令行「$」与未知工具名原样保留。
	verb = toolCallVerb(verb, m)
	cmdInfo := parseCommandToolInfo(m)
	category := classifyToolCategory(toolMessageName(m))
	adds, dels := toolCallDiffStat(m)
	duration := cmdInfo.Duration
	if duration == "" {
		duration = toolElapsedText(m)
	}
	// 运行中的命令工具改显示「已运行多久」；长时间没有新输出时改警告色并标注「无输出」。
	runStalled := false
	if toolText, stalled := s.runningCommandBadge(c, m); toolText != "" {
		duration, runStalled = toolText, stalled
	}
	// provider 托管的后台任务还在跑，耗时改成「后台 · 已运行时长」，每秒刷新。
	if toolText, ok := toolBackgroundBadge(c, m); ok {
		duration = toolText
	}

	open := s.toolMessageExpanded(m)
	failed := isToolFailed(m)

	label := toolCallRowLabel(verb, target, detail)
	if isFileEditTool(m) {
		label = "查看改动"
		if open {
			label = "收起改动"
		}
		if target != "" {
			label += " " + target
		}
	}

	var appCfg config.AppearanceConfig
	if st := getChatSettings(s); st != nil {
		appCfg = st.cfg.Appearance
	}
	fontFam := codeFontFamily(appCfg)

	chip := ui.ButtonBase(c.Key("tool-chip-"+m.ID)).
		Gap(Space2).
		Height(toolChipHeight).
		Radius(toolChipHeight/2).
		Padding(0, Space3).
		Cursor(ui.CursorPointer).
		Label(label).
		Tooltip(toolCallRowLabel(verb, target, detail)).
		OnClick(func() {
			s.toggleToolChip(m, beatTools)
		})
	// 底色按状态分层：常态最淡，悬停加深，详情已展开时最深；失败的工具再加一圈红描边
	switch {
	case open:
		chip.Background(ThemeSurfaceRaised(c))
	case chip.Hovered():
		chip.Background(ThemeSecondaryText(c).Alpha(0.14))
	default:
		chip.Background(ThemeSurfaceCard(c))
	}
	if failed {
		chip.Border(1, ThemeDanger(c))
	} else if toolMessageState(m) == ToolPending {
		chip.Border(1, ThemeOverlay(c, toolPendingBorderOpacity))
	}

	scale := motionPressScale(c, chip, "button-press-scale", true && chip.Pressed())
	chip.Scale(scale, scale)
	chip.Children(func() {
		renderToolChipStatus(c, m)
		renderToolChipCategoryMark(c, category, cmdInfo.IsCommand, fontFam)
		// 运行中的工具在标题上扫一道高光，动词时态也在这里变成「正在读取」这类现在进行时。
		renderToolChipTitle(c, m, func() ui.Element {
			title := ui.Text(c, toolChipShortTitle(m, verb, target)).
				FontSize(scaledFont(TypeFootnote)).
				TextColor(ThemePrimaryText(c)).
				SingleLine()
			if cmdInfo.IsCommand && toolChipDescription(m) == "" {
				// 命令文本用等宽字体，和终端的观感保持一致；有 description 时是自然语言，用默认字体
				title.Font(fontFam)
			}
			return title
		})
		if adds > 0 || dels > 0 {
			ui.Row(c.Key("tool-chip-diff-" + m.ID)).Gap(Space1).AlignItems(ui.Center).Children(func() {
				if adds > 0 {
					ui.Text(c, fmt.Sprintf("+%d", adds)).
						Font(fontFam).
						FontSize(scaledFont(TypeCaption)).
						FontWeight(500).
						TextColor(ThemeSuccess(c))
				}
				if dels > 0 {
					ui.Text(c, fmt.Sprintf("−%d", dels)).
						Font(fontFam).
						FontSize(scaledFont(TypeCaption)).
						FontWeight(500).
						TextColor(ThemeDanger(c))
				}
			})
		}
		if duration != "" {
			color := ThemeSecondaryText(c).Alpha(0.85)
			if runStalled {
				color = ThemeWarning(c)
			}
			ui.Text(c, duration).FontSize(scaledFont(TypeCaption)).TextColor(color)
		}
	})
}

// renderToolDetail 渲染一个工具调用的详情区：左侧缩进 12pt 的圆角浅底卡片，
// 编辑类工具先在顶部给「收起改动」按钮与这次调用的行级 diff，下面是等宽小号字体的输出正文
// （超过 300 字符时默认折叠，用户可就地展开全文）。
// 工具组里的胶囊详情与未分组的单条工具共用这套渲染。
func renderToolDetail(c *ui.Context, s *chatState, m Message) {
	lines := strings.Split(m.Text, "\n")
	// 被 hook 拦截/改道的结果末尾会跟一行 [failed]，它只是改道的标记，详情区不显示这一行
	blockedByHook := isToolBlockedByHook(m)
	if blockedByHook && len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "[failed]" {
		lines = lines[:len(lines)-1]
	}
	diffOpen := isFileEditTool(m) && s.isToolDiffOpen(m.ID)

	var appCfg config.AppearanceConfig
	if st := getChatSettings(s); st != nil {
		appCfg = st.cfg.Appearance
	}
	fontFam := codeFontFamily(appCfg)
	codeSz := codeFontSize()

	// 详情区卡片的内边距跟界面密度走；outputMax 是正文滚动区的最大高度，紧凑档更矮，避免占满时间线。
	dens := currentDensity(c, s)
	detailPad := dens.PadX
	outputMax := toolOutputMaxHeight
	if dens.isCompact() {
		outputMax = toolOutputMaxHeightCompact
	}

	ui.Column(c.Key("tool-detail-wrap-"+m.ID)).
		FillWidth().
		Margin(0, 0, 0, toolBeatPadLeft).
		Children(func() {
			ui.Column(c.Key("tool-detail-"+m.ID)).
				FillWidth().
				Padding(detailPad).
				Radius(RadiusMD).
				Background(ThemeHover(c)).
				Border(1, ThemeBorder(c)).
				Gap(Space2).
				Children(func() {
					if toolMessageState(m) == ToolPending && strings.TrimSpace(m.ToolInput) != "" {
						ui.Text(c, m.ToolInput).Font(fontFam).FontSize(scaledFont(codeSz)).TextColor(ThemeSecondaryText(c)).Selectable()
					}
					// 运行中的命令：顶部是步骤列表，右上角是「终止此命令」图标按钮，只杀这一条命令，不中断本轮。
					if toolIsRunningCommand(m) {
						renderRunningCommandHeader(c, s, m, fontFam)
					}
					// 编辑类工具：胶囊展开后直接看到这次改动的行级 diff，顶部保留收起入口
					if diffOpen {
						pressButton0 := ui.ButtonBase(c.Key("tool-detail-diff-toggle-"+m.ID)).
							Label("收起改动").
							Tooltip("收起改动").
							Padding(0, Space0).
							Cursor(ui.CursorPointer).
							OnClick(func() {
								s.hideToolDetail(m)
							})
						pressScale0 := motionPressScale(c, pressButton0, "button-press-scale", true && pressButton0.Pressed())
						pressButton0.Scale(pressScale0, pressScale0)
						pressButton0.Children(func() {
							ui.Text(c, "收起改动").
								FontSize(scaledFont(TypeCaption)).
								TextColor(ThemeAccent(c))
						})
						renderInlineToolDiff(c, s, m)
					}

					if isCommandTool(m) && isToolRunning(m) {
						renderRunningCommandActivity(c, s, m, fontFam, outputMax)
					} else if strings.TrimSpace(m.Text) != "" {
						// 输出正文：等宽小号字，超长时保留前 300 个字符；
						// 正文区限高后可滚动，运行中默认贴底跟随新输出，用户上滚则停止跟随。
						// 拦截标记那行已从 lines 去掉，正文按处理后的行拼回
						text := strings.Join(lines, "\n")
						foldKey := "tool-output-fold-" + m.ID
						s.mu.Lock()
						expanded := s.expandedTool[foldKey]
						s.mu.Unlock()
						runes := []rune(text)
						folded := len(runes) > foldRunThreshold && !expanded
						preview := text
						if folded {
							preview = string(runes[:foldRunThreshold])
						}
						st := s.toolOutputScrollStateFor(m.ID)
						if !folded && isToolRunning(m) && (st.MaxY <= 0 || st.Y >= st.MaxY-toolOutputFollowThreshold) {
							st.Y = math.MaxFloat32
						}
						ui.Box(c.Key("tool-output-wrap-" + m.ID)).FillWidth().Children(func() {
							ui.Scroll(c.Key("tool-output-scroll-" + m.ID)).FillWidth().MaxHeight(outputMax).TrackScroll(st).Children(func() {
								ui.Text(c, preview).Font(fontFam).FontSize(scaledFont(codeSz)).LineHeight(1.4).TextColor(ThemeSecondaryText(c)).Selectable()
							})
							if folded {
								color := ThemeHover(c)
								ui.Box(c.Key("tool-output-fade-" + m.ID)).Absolute().Left(0).Right(0).Bottom(0).Height(toolOutputFadeHeight).LinearGradient(ui.LinearGradient{Angle: 180, From: color.Alpha(0), To: color}).PassThrough()
							}
						})
						if folded {
							Capsule(c, CapsuleProps{Text: "展开全部", Interactive: true, OnClick: func() {
								s.mu.Lock()
								if s.expandedTool == nil {
									s.expandedTool = make(map[string]bool)
								}
								s.expandedTool[foldKey] = true
								s.expandedRev++
								s.mu.Unlock()
								s.holdChatPlace()
								s.invalidate()
							}})
						}
					}
				})
		})
}

// toolCallDiffStat 返回「这一次」工具调用自身的增删行数，供工具行右侧的差异角标使用。
//
// 只看这次调用的输入（编辑的 old/new 文本、写入内容、补丁正文），或这次调用输出里自带的
// unified diff。取不到就返回 0（不显示），绝不回退到整轮汇总：整轮 diff 是之前所有 edit 行
// 都显示同一对数字（如 +596 −106）的根因——同一文件在一轮里被改多次时，每行拿到的都是
// 该文件在本轮的累计改动；路径提取失败时（例如输入字段写作 "Path"）更是整轮所有文件的合计。
func toolCallDiffStat(m Message) (adds, dels int) {
	if !isFileEditTool(m) {
		return 0, 0
	}
	if raw := toolEmbeddedDiff(m.Text); raw != "" {
		if files, err := git.ParseUnifiedDiff([]byte(raw)); err == nil && len(files) > 0 {
			return diffStatTotals(files)
		}
	}
	fields := toolCallFields(m)
	if len(fields) == 0 {
		return 0, 0
	}
	if patch, ok := toolFieldString(fields, "patch", "input", "diff", "patch_text"); ok {
		if a, d, ok := patchDiffStat(patch); ok {
			return a, d
		}
	}
	oldText, hasOld := toolFieldString(fields, toolCallOldTextKeys...)
	newText, hasNew := toolFieldString(fields, toolCallNewTextKeys...)
	switch {
	case hasOld && hasNew:
		return lineDiffStat(oldText, newText)
	case hasNew:
		return len(splitDiffLines(newText)), 0
	case hasOld:
		return 0, len(splitDiffLines(oldText))
	}
	return 0, 0
}

// splitDiffLines 把文本切成按行的列表：统一 CRLF，忽略结尾换行造成的空行；空文本返回 nil。
func splitDiffLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// lineDiffStat 用「去掉公共前缀与后缀行」的近似算法统计一次改动的增删行数：
// 中间不重合的行按纯新增/纯删除计。对单次编辑足够精确，且不需要完整 LCS。
func lineDiffStat(oldText, newText string) (adds, dels int) {
	oldLines := splitDiffLines(oldText)
	newLines := splitDiffLines(newText)
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}
	return len(newLines) - prefix - suffix, len(oldLines) - prefix - suffix
}

// patchDiffStat 从补丁文本（apply_patch 风格或 unified diff）统计增删行数；没有可识别的改动行时 ok 为 false。
func patchDiffStat(text string) (adds, dels int, ok bool) {
	for _, line := range splitDiffLines(text) {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			continue
		case strings.HasPrefix(line, "+"):
			adds++
		case strings.HasPrefix(line, "-"):
			dels++
		}
	}
	return adds, dels, adds > 0 || dels > 0
}

// diffStatTotals 汇总一组文件改动的增删行数。
func diffStatTotals(files []git.FileDiff) (adds, dels int) {
	for _, file := range files {
		for _, hunk := range file.Hunks {
			for _, line := range hunk.Lines {
				switch line.Kind {
				case '+':
					adds++
				case '-':
					dels++
				}
			}
		}
	}
	return adds, dels
}
