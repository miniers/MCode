package mcodeui

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// turnTopToolsLimit 是完成行 Tooltip 里常用工具排行的上限。
const turnTopToolsLimit = 3

// turnToolStats 是某一回合的工具调用统计，供完成行文本与 Tooltip 共用。
type turnToolStats struct {
	calls        int             // 工具调用次数
	changedFiles int             // 编辑 / 写入类工具触及的去重文件数
	topTools     []ToolCallCount // 按调用次数降序，最多 turnTopToolsLimit 项
}

// collectTurnToolStats 统计某一回合内的工具调用：次数按 tool 消息计（与 app.ThreadActivityFromMessages 一致），
// 改动文件数从编辑 / 写入类工具的参数里取路径后去重，常用工具按次数降序、同次数按名称升序排列。
// turnIndex 为负数（消息缺轮次信息）时不统计，避免把整段历史算到同一回合；首轮的 0 是有效轮次。
func collectTurnToolStats(msgs []Message, turnIndex int) turnToolStats {
	var st turnToolStats
	if turnIndex < 0 {
		return st
	}
	counts := make(map[string]int)
	edited := make(map[string]bool)
	for _, m := range msgs {
		if m.Role != "tool" || m.TurnIndex != turnIndex {
			continue
		}
		st.calls++
		name := toolMessageName(m)
		if name == "" {
			name = "未知工具"
		}
		counts[name]++
		if isFileEditTool(m) {
			for _, p := range toolEditedPaths(m) {
				edited[p] = true
			}
		}
	}
	st.changedFiles = len(edited)
	st.topTools = make([]ToolCallCount, 0, len(counts))
	for name, count := range counts {
		st.topTools = append(st.topTools, ToolCallCount{Name: name, Count: count})
	}
	sort.Slice(st.topTools, func(i, j int) bool {
		if st.topTools[i].Count != st.topTools[j].Count {
			return st.topTools[i].Count > st.topTools[j].Count
		}
		return st.topTools[i].Name < st.topTools[j].Name
	})
	if len(st.topTools) > turnTopToolsLimit {
		st.topTools = st.topTools[:turnTopToolsLimit]
	}
	return st
}

// turnEditedPath 是本回合一条被改动路径，带首次出现的工具消息 ID（stat 缓存的键）。
type turnEditedPath struct {
	path   string
	msgID  string
	status string // "A" 新增 / "M" 修改 / "D" 删除 / "" 未知，来自补丁头或回合级 diff
}

// turnEditedFile 是本回合一个被改动且本机真实存在的文件。
type turnEditedFile struct {
	path    string // 工具里记录的原始路径（相对会话工作目录或绝对路径），打开预览时换算成相对路径
	abs     string // 解析后的本机绝对路径，用于访达定位与 Tooltip
	name    string // 展示用的文件名
	adds    int    // 本文件增行数，hasStat 为 false 时无意义
	dels    int    // 本文件删行数，hasStat 为 false 时无意义
	hasStat bool   // 是否已从回合级 git diff 拿到可信的增删统计；零值表示未知
	status  string // "A" 新增 / "M" 修改 / "D" 删除 / "" 未知；回合级 diff 比工具推断更可信
}

// collectTurnEditedPaths 收集本回合成功完成的编辑类工具触及的路径：按首次出现顺序去重，
// TurnIndex 相同才算本回合；失败、被拦截与运行中的工具整条跳过。
// turnIndex 为负数（消息缺轮次信息）时返回空，与 collectTurnToolStats 的约定一致。
func collectTurnEditedPaths(msgs []Message, turnIndex int) []turnEditedPath {
	if turnIndex < 0 {
		return nil
	}
	var out []turnEditedPath
	seen := make(map[string]bool)
	for _, m := range msgs {
		if m.Role != "tool" || m.TurnIndex != turnIndex {
			continue
		}
		if !isFileEditTool(m) || !turnToolSucceeded(m) {
			continue
		}
		for _, p := range toolEditedPaths(m) {
			p = strings.TrimSpace(p)
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, turnEditedPath{path: p, msgID: m.ID, status: turnToolEditedStatus(m, p)})
		}
	}
	return out
}

// turnPatchFileRe 从 apply_patch 补丁头里提取动作与路径，用于在不跑 git 的前提下推断 A / M / D。
var turnPatchFileRe = regexp.MustCompile(`\*\*\* (Add|Update|Delete) File: ([^\n"\\]+)`)

// turnToolEditedStatus 只从工具调用本身推断改动状态：apply_patch 的补丁头是显式的
// Add / Update / Delete 标记，只有「创建文件」语义的工具才按新增算；普通 write / edit
// 分不清新建与覆盖，返回空串，交给回合级 diff 的状态补足（拿不到就保持未知）。
func turnToolEditedStatus(m Message, path string) string {
	patch := m.ToolInput
	if strings.TrimSpace(patch) == "" {
		patch = m.Text
	}
	for _, match := range turnPatchFileRe.FindAllStringSubmatch(patch, -1) {
		if strings.TrimSpace(match[2]) != path {
			continue
		}
		switch match[1] {
		case "Add":
			return "A"
		case "Delete":
			return "D"
		default:
			return "M"
		}
	}
	if toolNameKey(toolMessageName(m)) == "createfile" {
		return "A"
	}
	return ""
}

// collectTurnEditedFiles 只返回本回合成功改动过的文件路径（去重、保持首次出现顺序）。
func collectTurnEditedFiles(msgs []Message, turnIndex int) []string {
	paths := collectTurnEditedPaths(msgs, turnIndex)
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, p.path)
	}
	return out
}

// turnToolSucceeded 判断工具是否已成功完成：失败、被拦截、取消、运行中与 provider 托管的后台任务
// （background 仍未结束，结束后会变回 completed / failed）都不算成功，
// 状态缺失的旧消息按正文里的 [failed] 标记判断。
func turnToolSucceeded(m Message) bool {
	switch strings.ToLower(strings.TrimSpace(m.ToolStatus)) {
	case "failed", "error", "blocked", "cancelled", "canceled", "running", "pending", "background":
		return false
	}
	return !isToolFailed(m) && !isToolBlockedByHook(m) && !isToolRunning(m)
}

// turnToolStatsFor 读取会话消息并统计本回合的工具调用（需要持 chatState 锁）。
func turnToolStatsFor(s *chatState, turnIndex int) turnToolStats {
	if s == nil {
		return turnToolStats{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return collectTurnToolStats(s.messages, turnIndex)
}

// shortenModelName 把模型名压缩成适合一行的短名：去掉末尾的 -YYYYMMDD 日期后缀，超长时截断加省略号。
func shortenModelName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	name = trimModelDateSuffix(name)
	const maxRunes = 24
	if utf8.RuneCountInString(name) > maxRunes {
		name = string([]rune(name)[:maxRunes-1]) + "…"
	}
	return name
}

// trimModelDateSuffix 去掉模型 ID 末尾的 -YYYYMMDD 日期后缀（如 claude-sonnet-4-5-20250929）。
func trimModelDateSuffix(name string) string {
	if len(name) < 9 || name[len(name)-9] != '-' {
		return name
	}
	for _, r := range name[len(name)-8:] {
		if r < '0' || r > '9' {
			return name
		}
	}
	return name[:len(name)-9]
}

// turnFinishedCost 按模型参考价估算本回合费用（美元），复用会话统计浮层的 estimateThreadCost；
// 拿不到模型单价或本回合没有任何 token 时返回 false。
func turnFinishedCost(s *chatState, m Message, usage TokenUsage) (float64, bool) {
	if s == nil || (usage.Input <= 0 && usage.Output <= 0) {
		return 0, false
	}
	provider, model, _ := assistantMetaSource(s, m)
	s.mu.Lock()
	providers := s.providers
	s.mu.Unlock()
	prov, ok := findProviderInfo(providers, provider)
	if !ok {
		return 0, false
	}
	info, found := findModelInfo(prov.Models, model)
	if !found || info.Cost == nil {
		return 0, false
	}
	return estimateThreadCost(usage, *info.Cost)
}

// turnSpanFromMessages 从消息时间戳推导本回合的开始与结束时间：开始取同回合用户消息的最早时间，
// 结束取同回合所有消息的最晚时间；拿不到时返回零值，调用方省略时间行。
func turnSpanFromMessages(msgs []Message, turnIndex int) (start, end time.Time) {
	return turnSpanForMessage(msgs, Message{TurnIndex: turnIndex})
}

func sameTurn(m, target Message) bool {
	if target.TurnID != "" {
		return m.TurnID == target.TurnID
	}
	return target.TurnIndex >= 0 && m.TurnID == "" && m.TurnIndex == target.TurnIndex
}

func turnAuthoritativeSpan(msgs []Message, target Message) (start, end time.Time) {
	for _, m := range msgs {
		if !sameTurn(m, target) {
			continue
		}
		if at, err := time.Parse(time.RFC3339Nano, m.TurnStartedAt); err == nil && start.IsZero() {
			start = at
		}
		if at, err := time.Parse(time.RFC3339Nano, m.TurnCompletedAt); err == nil && end.IsZero() {
			end = at
		}
	}
	return start, end
}

func turnSpanForMessage(msgs []Message, target Message) (start, end time.Time) {
	if target.TurnID == "" && target.TurnIndex < 0 {
		return time.Time{}, time.Time{}
	}
	start, end = turnAuthoritativeSpan(msgs, target)
	if !start.IsZero() && !end.IsZero() && !end.Before(start) {
		return start, end
	}
	start, end = time.Time{}, time.Time{}
	for _, m := range msgs {
		if !sameTurn(m, target) {
			continue
		}
		at, ok := messageTime(m)
		if !ok {
			continue
		}
		if m.Role == "user" && (start.IsZero() || at.Before(start)) {
			start = at
		}
		if end.IsZero() || at.After(end) {
			end = at
		}
	}
	return start, end
}

// turnFinishedSpanFor 读取会话消息并取本回合的起止时间（需要持 chatState 锁）；
// 时间线里拿不到时退回完成消息自身的时间。
func turnFinishedSpanFor(s *chatState, m Message) (time.Time, time.Time) {
	var start, end time.Time
	if s != nil {
		s.mu.Lock()
		start, end = turnSpanForMessage(s.messages, m)
		s.mu.Unlock()
	}
	at := messageFinishedAt(m)
	if end.IsZero() {
		end = at
	}
	if start.IsZero() {
		start = at
	}
	return start, end
}

// formatTurnFinishedSummary 拼装完成行主体文本：模型短名、用时、工具调用、改动文件数与输出 tokens，
// 缺数据的分段与「已完成」前缀一起省略（全部缺失时返回空字符串）。顺序与短行一致，便于一眼扫读。
func formatTurnFinishedSummary(model string, stats turnToolStats, usage TokenUsage, duration time.Duration) string {
	parts := make([]string, 0, 5)
	if name := strings.TrimSpace(model); name != "" {
		parts = append(parts, name)
	}
	if duration >= time.Second {
		parts = append(parts, "用时 "+formatShortDuration(duration))
	}
	if stats.calls > 0 {
		parts = append(parts, fmt.Sprintf("工具 %d", stats.calls))
	}
	if stats.changedFiles > 0 {
		parts = append(parts, fmt.Sprintf("改动 %d 个文件", stats.changedFiles))
	}
	if usage.Output > 0 {
		parts = append(parts, "输出 "+formatTokens(usage.Output))
	}
	if len(parts) == 0 {
		return ""
	}
	return "已完成 · " + strings.Join(parts, " · ")
}

// formatTurnFinishedBlockTextWithInfo 在完成行主体文本后追加输出速度（能算得时），
// 形如「已完成 · Opus 4.5 · 用时 12s · 工具 12 · 改动 3 个文件 · 输出 456 · 38 t/s」。
// 有逐请求样本时显示平均值与范围，见 appendTurnOutputSpeed。
func formatTurnFinishedBlockTextWithInfo(model string, stats turnToolStats, usage TokenUsage, duration time.Duration) string {
	text := formatTurnFinishedSummary(model, stats, usage, duration)
	return appendTurnOutputSpeed(text, usage, duration)
}

// appendTurnEndClock 在完成行文本末尾追加具体结束时间（本地时区，带月日），
// 形如「…… · 结束 10-23 08:16:27」。
// text 为空（本块整体跳过）或 end 为零值时原样返回，避免只有结束时间也显示一行。
func appendTurnEndClock(text string, end time.Time) string {
	if text == "" || end.IsZero() {
		return text
	}
	return text + " · 结束 " + end.Local().Format("01-02 15:04:05")
}

// turnFinishedInfo 是完成行 Tooltip 的原始数据，交给纯函数拼文本，便于单元测试。
type turnFinishedInfo struct {
	usage      TokenUsage
	stats      turnToolStats
	costUSD    float64
	hasCost    bool
	start, end time.Time
}

// formatTurnFinishedTooltip 拼装完成行的多行 Tooltip：token 明细与缓存命中率、输出速度样本、
// 本回合估算费用、常用工具 Top3、开始与结束时间（本地时区的 MM-DD HH:MM:SS）。
// 没有可用数据的行整行省略，全部为空时返回空字符串。
func formatTurnFinishedTooltip(info turnFinishedInfo) string {
	lines := make([]string, 0, 5)
	u := info.usage
	if u.Input > 0 || u.Output > 0 || u.Cached > 0 {
		parts := make([]string, 0, 3)
		if u.Input > 0 {
			parts = append(parts, "输入 "+formatTokens(u.Input))
		}
		if u.Output > 0 {
			parts = append(parts, "输出 "+formatTokens(u.Output))
		}
		if u.Cached > 0 {
			parts = append(parts, "缓存 "+formatTokens(u.Cached))
		}
		lines = append(lines, strings.Join(parts, " · ")+" tokens")
	}
	if st, ok := requestSpeedStatsFor(u.Requests); ok {
		lines = append(lines, formatRequestSpeedDetail(st))
	}
	extra := make([]string, 0, 2)
	if hit := turnCacheHitRate(u); hit != "" {
		extra = append(extra, "缓存命中 "+hit)
	}
	if info.hasCost {
		extra = append(extra, "估算 "+formatTurnCost(info.costUSD))
	}
	if len(extra) > 0 {
		lines = append(lines, strings.Join(extra, " · "))
	}
	if len(info.stats.topTools) > 0 {
		parts := make([]string, 0, len(info.stats.topTools))
		for _, t := range info.stats.topTools {
			parts = append(parts, fmt.Sprintf("%s×%d", t.Name, t.Count))
		}
		lines = append(lines, "工具 "+strings.Join(parts, " · "))
	}
	times := make([]string, 0, 2)
	if !info.start.IsZero() {
		times = append(times, "开始 "+info.start.Local().Format("01-02 15:04:05"))
	}
	if !info.end.IsZero() {
		times = append(times, "结束 "+info.end.Local().Format("01-02 15:04:05"))
	}
	if len(times) > 0 {
		lines = append(lines, strings.Join(times, " · "))
	}
	return strings.Join(lines, "\n")
}

// turnCacheHitRate 返回缓存命中率文本，口径与会话统计浮层一致（缓存 / (输入 + 缓存)）；
// 没有缓存或总量为 0 时返回空字符串。
func turnCacheHitRate(u TokenUsage) string {
	total := u.Input + u.Cached
	if total <= 0 || u.Cached <= 0 {
		return ""
	}
	return fmt.Sprintf("%.1f%%", float64(u.Cached)*100/float64(total))
}

// formatTurnCost 把单回合估算费用格式化成「≈ $0.01」：不足 1 美分时保留 4 位小数，
// 避免小额回合一律显示成 $0.00。
func formatTurnCost(usd float64) string {
	if usd >= 0.01 {
		return fmt.Sprintf("≈ $%.2f", usd)
	}
	return fmt.Sprintf("≈ $%.4f", usd)
}
