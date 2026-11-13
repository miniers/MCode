package mcodeui

import (
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/egoist/mygo/ui"
)

// 会话内查找：聊天区右上角浮动的紧凑查找条（参考 synara 的 ThreadFindBar）。
// 定位到消息级（命中用户与助手的正文，跳到该消息所在的条目），并在 Markdown 源码层
// 给命中文字包 <mark> 做到字级高亮；只在查找条打开期间注入，关闭即恢复原文。
// 查找条贴右缘向左让出一个迷你地图的宽度，避免盖住刻度轨道。

// chatFindState 维护会话内查找条的状态：是否展开、查询串、是否区分大小写、
// 命中消息 ID 列表（按时间顺序）、当前游标与所属会话 ID。
// 状态按 chatState 附加存放（键为 *chatState），与 chatToolbarState 的做法一致。
type chatFindState struct {
	open          bool
	query         string
	prevQuery     string
	caseSensitive bool
	matches       []string // 命中的消息 ID，按时间顺序
	cursor        int      // 当前命中在 matches 中的下标
	dirty         bool     // 查询串或大小写开关变化后待重算命中
	focusPending  bool     // 下一帧给输入框要一次焦点
	threadID      string   // 命中列表所属的会话 ID，切换会话时作废重算
	rev           uint64   // 命中列表对应的消息版本号，新消息落地后重算
}

var (
	chatFindMu     sync.Mutex
	chatFindStates = make(map[*chatState]*chatFindState)
)

// getChatFindState 获取或初始化当前 chatState 的会话内查找状态。
func getChatFindState(s *chatState) *chatFindState {
	if s == nil {
		return nil
	}
	chatFindMu.Lock()
	defer chatFindMu.Unlock()
	st, ok := chatFindStates[s]
	if !ok {
		st = &chatFindState{}
		chatFindStates[s] = st
	}
	return st
}

// findMessageMatches 返回正文包含 query 的用户与助手消息 ID，按时间顺序排列。
// 忽略思考块、工具输出、系统消息与空正文；query 为空（或只有空白）时返回 nil。
// 纯函数，便于单独测试。
func findMessageMatches(msgs []Message, query string, caseSensitive bool) []string {
	needle := strings.TrimSpace(query)
	if needle == "" {
		return nil
	}
	fold := func(s string) string {
		if caseSensitive {
			return s
		}
		return strings.ToLower(s)
	}
	needle = fold(needle)

	var matches []string
	for _, msg := range msgs {
		// 没有消息 ID 的消息无法定位，跳过。
		if msg.ID == "" || !isFindableMessage(msg) {
			continue
		}
		if strings.Contains(fold(msg.Text), needle) {
			matches = append(matches, msg.ID)
		}
	}
	return matches
}

// isFindableMessage 判断消息是否参与会话内查找：
// 只看用户与助手的正文，思考块与工具输出不参与。
func isFindableMessage(msg Message) bool {
	if msg.Role != "user" && msg.Role != "assistant" {
		return false
	}
	if isReasoningMessage(msg) {
		return false
	}
	return strings.TrimSpace(msg.Text) != ""
}

// --- 字级高亮：在 Markdown 源码层注入 <mark> ---
//
// 注入只在查找条打开、查询非空、且该消息在命中列表里时发生：其余时刻原样返回、
// 不做任何扫描，所以未开查找时不会给流式渲染增加开销。
// 遵循计划 D3：当前命中的消息额外用 <u> 标出，其余命中只加 <mark>；
// 代码围栏、行内代码、链接目标与 HTML 标签内部一律不注入。

// findHighlightOpen、findHighlightClose 是素净命中项的包裹标签（mygo 原生解析 mark，
// 主题色见 markdown.Theme.Mark）。
const (
	findHighlightOpen  = "<mark>"
	findHighlightClose = "</mark>"
	// findActiveOpen、findActiveClose 是当前命中项的额外标记（下划线）。
	findActiveOpen  = "<u>"
	findActiveClose = "</u>"
)

// foldFindRune 返回比较用的 rune：区分大小写时原样返回，否则折成小写。
func foldFindRune(r rune, caseSensitive bool) rune {
	if caseSensitive {
		return r
	}
	return unicode.ToLower(r)
}

// matchFindAt 判断 runes[pos:] 是否以 needle 开头（按大小写规则，needle 已折叠）。
func matchFindAt(runes []rune, pos int, needle []rune, caseSensitive bool) bool {
	if len(needle) == 0 || pos < 0 || pos+len(needle) > len(runes) {
		return false
	}
	for k, want := range needle {
		if foldFindRune(runes[pos+k], caseSensitive) != want {
			return false
		}
	}
	return true
}

// fenceMarkerLen 判断行首 i 处是不是一行代码围栏（``` 或 ~~~，最多 3 个空格缩进），
// 返回围栏标记的字符数；不是围栏时返回 0。
func fenceMarkerLen(runes []rune, i int) int {
	j := i
	for j < len(runes) && j-i < 3 && runes[j] == ' ' {
		j++
	}
	if j >= len(runes) {
		return 0
	}
	marker := runes[j]
	if marker != '`' && marker != '~' {
		return 0
	}
	n := 0
	for j+n < len(runes) && runes[j+n] == marker {
		n++
	}
	if n < 3 {
		return 0
	}
	return n
}

// markMarkdownSource 在 Markdown 源码里给命中文字包上 <mark>：第 active 个命中
// （从 0 起算）再套一层 <u> 标成当前命中，active < 0 时不加下划线。
// 代码围栏、行内代码、链接目标与 HTML 标签内部不注入；query 为空时原样返回。
// 纯函数，便于单独测试。
func markMarkdownSource(src string, query []rune, caseSensitive bool, active int) string {
	if src == "" || len(query) == 0 {
		return src
	}
	needle := make([]rune, len(query))
	for i, r := range query {
		needle[i] = foldFindRune(r, caseSensitive)
	}

	runes := []rune(src)
	var b strings.Builder
	b.Grow(len(src) + 16)

	inFence := false
	count := 0
	for i := 0; i < len(runes); {
		lineStart := i == 0 || runes[i-1] == '\n'
		if lineStart {
			if marker := fenceMarkerLen(runes, i); marker > 0 {
				// 围栏行整行照抄，并翻转围栏状态。
				j := i
				for j < len(runes) && runes[j] != '\n' {
					j++
				}
				if j < len(runes) {
					j++
				}
				b.WriteString(string(runes[i:j]))
				i = j
				inFence = !inFence
				continue
			}
		}
		if inFence {
			b.WriteRune(runes[i])
			i++
			continue
		}

		switch {
		case runes[i] == '`':
			// 行内代码：跳过成对的反引号串。
			ticks := 0
			for i+ticks < len(runes) && runes[i+ticks] == '`' {
				ticks++
			}
			end := -1
			for k := i + ticks; k < len(runes); k++ {
				if runes[k] != '`' {
					continue
				}
				m := 0
				for k+m < len(runes) && runes[k+m] == '`' {
					m++
				}
				if m == ticks {
					end = k + m
					break
				}
				k += m - 1
			}
			if end > i {
				b.WriteString(string(runes[i:end]))
				i = end
				continue
			}
			b.WriteRune(runes[i])
			i++
			continue
		case runes[i] == ']' && i+1 < len(runes) && runes[i+1] == '(':
			// 链接目标：从 ]( 跳到配对的括号，目标地址里的命中不标记。
			j := i + 2
			depth := 1
			for j < len(runes) && depth > 0 {
				switch runes[j] {
				case '(':
					depth++
				case ')':
					depth--
				}
				j++
			}
			b.WriteString(string(runes[i:j]))
			i = j
			continue
		case runes[i] == '<':
			// HTML 标签与自动链接：< 到同一行内的 > 整段照抄。
			j := i + 1
			for j < len(runes) && runes[j] != '>' && runes[j] != '\n' {
				j++
			}
			if j < len(runes) && runes[j] == '>' {
				j++
				b.WriteString(string(runes[i:j]))
				i = j
				continue
			}
		}

		if matchFindAt(runes, i, needle, caseSensitive) {
			b.WriteString(findHighlightOpen)
			if count == active {
				b.WriteString(findActiveOpen)
			}
			b.WriteString(string(runes[i : i+len(needle)]))
			if count == active {
				b.WriteString(findActiveClose)
			}
			b.WriteString(findHighlightClose)
			count++
			i += len(needle)
			continue
		}
		b.WriteRune(runes[i])
		i++
	}
	return b.String()
}

// findMessageCursor 返回消息 ID 在命中列表里的下标，找不到时返回 -1。
func findMessageCursor(f *chatFindState, messageID string) int {
	if f == nil || messageID == "" {
		return -1
	}
	for i, id := range f.matches {
		if id == messageID {
			return i
		}
	}
	return -1
}

// findHighlightSource 是渲染层的高亮入口：查找条关闭、查询为空、或该消息不在命中列表里时
// 原样返回 src（不扫描）；命中时当前消息的第一个命中加下划线、其余命中只加 <mark>。
// 调用方（assistant 正文的四处 Markdown 渲染）只需把源码包一层，不改渲染流程。
func findHighlightSource(s *chatState, messageID, src string) string {
	f := getChatFindState(s)
	if f == nil || !f.open || src == "" {
		return src
	}
	query := strings.TrimSpace(f.query)
	if query == "" {
		return src
	}
	idx := findMessageCursor(f, messageID)
	if idx < 0 {
		return src
	}
	// 游标停留在本条消息时把第一个命中标成当前项；其余命中消息只做背景高亮。
	active := -1
	if idx == f.cursor {
		active = 0
	}
	return markMarkdownSource(src, []rune(query), f.caseSensitive, active)
}

// moveCursor 循环移动命中游标（delta < 0 向前、delta > 0 向后），返回是否移动成功。
// 没有命中时不移动。
func (f *chatFindState) moveCursor(delta int) bool {
	if f == nil || len(f.matches) == 0 {
		return false
	}
	n := len(f.matches)
	f.cursor = ((f.cursor+delta)%n + n) % n
	return true
}

// openChatFind 打开查找条：保留上次的查询串，重算命中并给输入框要一次焦点。
func (s *chatState) openChatFind() {
	f := getChatFindState(s)
	if f == nil {
		return
	}
	f.open = true
	f.focusPending = true
	f.dirty = true
	s.invalidate()
}

// closeChatFind 关闭查找条。
func (s *chatState) closeChatFind() {
	f := getChatFindState(s)
	if f == nil || !f.open {
		return
	}
	f.open = false
	f.focusPending = false
	s.invalidate()
}

// timelineItemContainsMessageID 判断条目是否覆盖某条消息：直接命中 msg，或该消息在
// 条目的整组工具、节拍工具、汇报消息里，或落在此项的回合折叠区（workItems）里。
// 折叠项只保留组头，被收起的消息不在 items 顶层露面，所以必须递归查。
func timelineItemContainsMessageID(item timelineItem, id string) bool {
	if id == "" {
		return false
	}
	if item.msg.ID == id {
		return true
	}
	for _, list := range [][]Message{item.tools, item.beatTools, item.reportMsgs} {
		for _, m := range list {
			if m.ID == id {
				return true
			}
		}
	}
	for _, inner := range item.workItems {
		if timelineItemContainsMessageID(inner, id) {
			return true
		}
	}
	return false
}

// turnWorkItemOpen 读取回合折叠项的展开状态（与 renderTurnWorkItem 同源：s.expandedTool[item.id]）。
// 用户显式设置过就用设置值；否则关闭「自动折叠思考内容」时默认展开。
func turnWorkItemOpen(s *chatState, item timelineItem) bool {
	s.mu.Lock()
	v, ok := s.expandedTool[item.id]
	s.mu.Unlock()
	// isAutoCollapseReasoningEnabled 会锁设置状态的锁，必须在释放 s.mu 之后调用。
	if ok {
		return v
	}
	return !isAutoCollapseReasoningEnabled(s)
}

// toolGroupItemOpen 读取工具组的展开状态：显式设置过就按设置，否则按构建时的默认值。
func toolGroupItemOpen(s *chatState, item timelineItem) bool {
	if item.groupID == "" {
		return true
	}
	s.mu.Lock()
	v, ok := s.expandedTool[item.groupID]
	s.mu.Unlock()
	if !ok {
		return item.groupExpanded
	}
	return v
}

// expandTimelineItemForJump 把包含跳转目标的条目按需展开：回合折叠项与工具组都用
// 同包的 setToolGroupOpen 打开（折叠项把开合状态也存在同一张表里，键是 item.id）。
// 只展开"包含目标"的这一串条目——展开它不改变 items 的行数（折叠区是行内展开体），
// 所以不会让刚算出的行下标失效；顺带展开落点上方的工具组会新增行，反而会错位，故不做。
func expandTimelineItemForJump(s *chatState, item timelineItem) {
	if s == nil {
		return
	}
	switch {
	case item.kind == timelineItemTurnWork:
		// 运行中的回合头没有可展开的过程，不能按普通折叠项打开。
		if !item.workRunning && !turnWorkItemOpen(s, item) {
			s.setToolGroupOpen(item.id, true)
		}
	case item.groupID != "":
		if !toolGroupItemOpen(s, item) {
			s.setToolGroupOpen(item.groupID, true)
		}
	}
	for _, inner := range item.workItems {
		expandTimelineItemForJump(s, inner)
	}
}

// scrollToTimelineRow 滚动到指定条目，并按需展开包住该条目的回合折叠项或工具组。
// jumpToAdjacentUserMessage 也走这里，落到折叠区内的消息时同样会先展开再定位。
func scrollToTimelineRow(s *chatState, items []timelineItem, row int) bool {
	if s == nil || row < 0 || row >= len(items) {
		return false
	}
	expandTimelineItemForJump(s, items[row])
	// 居中对齐让目标落在视口中部，而不是贴着顶边。
	s.chatListState.ScrollTo(row, ui.Center)
	return true
}

// timelineRowForMessageID 返回包含该消息的条目下标（找不到返回 -1），并把落点需要的
// 折叠项展开。按 items[i].msg.ID == id 直接比会漏掉被收进回合折叠项或工具组里的消息，
// 因此这里递归检查 workItems 与各组的消息列表。
func timelineRowForMessageID(s *chatState, items []timelineItem, id string) int {
	if s == nil || id == "" {
		return -1
	}
	for i := range items {
		if timelineItemContainsMessageID(items[i], id) {
			return i
		}
	}
	return -1
}

// jumpFlashDuration 是跳转落点高亮的持续时长：与复制反馈一致，1.4s 内从强调色淡出到透明。
const jumpFlashDuration = 1400 * time.Millisecond

// scrollToMessageID 把时间线滚动到指定消息所在的条目（与迷你地图点击跳转一致，
// 用 chatListState.ScrollTo 把目标条目带到视口中央）；命中消息落在折叠区或工具组里时
// 先展开该条目再滚动。找不到时返回 false。
func scrollToMessageID(s *chatState, items []timelineItem, id string) bool {
	row := timelineRowForMessageID(s, items, id)
	if row < 0 {
		return false
	}
	if !scrollToTimelineRow(s, items, row) {
		return false
	}
	s.flashJumpTarget(id)
	return true
}

// flashJumpTarget 把跳转落点记为高亮目标并推进序号，1.4s 后按序号清除。
func (s *chatState) flashJumpTarget(id string) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	s.flashMessageID = id
	s.flashSeq++
	seq := s.flashSeq
	s.mu.Unlock()
	s.invalidate()
	time.AfterFunc(jumpFlashDuration, func() { clearJumpFlash(s, seq) })
}

// clearJumpFlash 清除第 seq 次跳转的落点高亮：序号对不上说明期间又跳了一次，
// 这时不清，否则会把新一次跳转的高亮提前抹掉。
func clearJumpFlash(s *chatState, seq uint64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	cleared := s.flashSeq == seq && s.flashMessageID != ""
	if s.flashSeq == seq {
		s.flashMessageID = ""
	}
	s.mu.Unlock()
	if cleared {
		s.invalidate()
	}
}

// messageJump 是一次待消费的跨会话消息跳转：目标会话与目标消息 ID，以及登记时间。
type messageJump struct {
	threadID  string
	messageID string
	at        time.Time
}

var (
	messageJumpMu sync.Mutex
	messageJumps  = make(map[*chatState]*messageJump)
)

// messageJumpTTL 是跳转登记的有效期：超过这个时间还没在时间线里等到目标消息就作废，
// 免得历史一直没加载到目标消息时，之后某一帧突然跳走。
const messageJumpTTL = 15 * time.Second

// requestMessageJump 登记一次跨会话消息跳转（命令面板的消息搜索结果点击后调用），
// 等目标会话的消息落地后在渲染时消费。
func (s *chatState) requestMessageJump(threadID, messageID string) {
	if s == nil || threadID == "" || messageID == "" {
		return
	}
	messageJumpMu.Lock()
	messageJumps[s] = &messageJump{threadID: threadID, messageID: messageID, at: time.Now()}
	messageJumpMu.Unlock()
}

// messageJumpApplies 判定一条登记是否应该在本帧消费：当前选中会话已是目标会话、
// 且登记还没有过期。纯函数，便于单独测试。
func messageJumpApplies(j *messageJump, now time.Time, selectedID string) bool {
	if j == nil || selectedID == "" {
		return false
	}
	if j.threadID != selectedID {
		return false
	}
	return now.Sub(j.at) <= messageJumpTTL
}

// timelineHasMessageID 判断时间线里是否已经有这条消息的条目。
func timelineHasMessageID(items []timelineItem, id string) bool {
	if id == "" {
		return false
	}
	// 折叠区与工具组里的消息不在顶层露面，按 msg.ID 直比会漏掉。
	for i := range items {
		if timelineItemContainsMessageID(items[i], id) {
			return true
		}
	}
	return false
}

// consumeMessageJump 消费待执行的跨会话消息跳转：只有目标会话已选中、且目标消息
// 已经出现在时间线里时才滚动过去，历史还没加载完时保留登记继续等。
func consumeMessageJump(s *chatState, selectedID string, items []timelineItem) {
	if s == nil {
		return
	}
	now := time.Now()

	messageJumpMu.Lock()
	j := messageJumps[s]
	if j == nil {
		messageJumpMu.Unlock()
		return
	}
	if !messageJumpApplies(j, now, selectedID) {
		// 过期就没必要再等了；会话还没切过去则继续等。
		if now.Sub(j.at) > messageJumpTTL {
			delete(messageJumps, s)
		}
		messageJumpMu.Unlock()
		return
	}
	if !timelineHasMessageID(items, j.messageID) {
		messageJumpMu.Unlock()
		return
	}
	messageID := j.messageID
	delete(messageJumps, s)
	messageJumpMu.Unlock()

	// selectThread 会把 scrollToEnd 置真，消费时又会把时间线抢回底部；
	// 命中时先清掉它，让本次定位的方向生效。
	s.mu.Lock()
	s.scrollToEnd = false
	s.mu.Unlock()

	scrollToMessageID(s, items, messageID)
}

// stepChatFind 把游标循环移动一格并跳到对应消息。
func stepChatFind(s *chatState, f *chatFindState, items []timelineItem, delta int) {
	if !f.moveCursor(delta) {
		return
	}
	scrollToMessageID(s, items, f.matches[f.cursor])
}

// chatFindCounterText 返回查找条的命中计数文案：有命中时是「n/m」，无命中时查询非空显示「0/0」。
func chatFindCounterText(f *chatFindState) string {
	if f == nil || strings.TrimSpace(f.query) == "" {
		return ""
	}
	if len(f.matches) == 0 {
		return "0/0"
	}
	cursor := f.cursor
	if cursor < 0 || cursor >= len(f.matches) {
		cursor = 0
	}
	return fmt.Sprintf("%d/%d", cursor+1, len(f.matches))
}

// renderChatFindBar 渲染会话内查找条：浮在聊天区右上角、横向让出一个迷你地图的宽度，
// 不占列表布局宽度。输入框获得焦点时 Enter 跳到下一个命中、⇧↩ 跳到上一个、Esc 关闭。
// rev 是消息版本号（新消息落地后重算命中），messages 用于重算命中，items 用于把命中消息滚动到视口。
func renderChatFindBar(c *ui.Context, s *chatState, threadID string, rev uint64, messages []Message, items []timelineItem) {
	// 命令面板点进来的跨会话消息跳转在这里落地：必须放在任何提前返回之前，
	// 否则查找条没展开时跳转永远不会被消费。
	consumeMessageJump(s, threadID, items)

	f := getChatFindState(s)
	if f == nil || !f.open {
		return
	}

	// 切换会话后旧的命中列表作废，按新会话重算。
	if f.threadID != threadID {
		f.threadID = threadID
		f.matches = nil
		f.cursor = 0
		f.dirty = true
	}

	// 输入框失焦时 Esc 也能关掉查找条。
	if c.Shortcut(0, ui.KeyEscape) {
		s.closeChatFind()
		return
	}

	ui.Row(c.Key("chat-find-bar")).
		Absolute().
		Right(minimapRailWidth+12).
		Top(8).
		Width(248).
		Height(ControlLG).
		Radius(RadiusMD).
		Padding(0, Space2).
		Gap(Space2).
		AlignItems(ui.Center).
		Background(ThemeSurface(c)).
		Border(Hairline, ThemeBorder(c)).
		Children(func() {
			ui.Icon(c, IconSearch()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))

			in := ui.TextInput(c.Key("chat-find-input"), &f.query).
				Label("在会话中查找").
				Placeholder("在会话中查找").
				FontSize(uiFont(FontSizeCapsuleText)).
				TextColor(ThemePrimaryText(c)).
				Background(ui.Transparent).
				BorderWidth(0).
				Padding(0).
				Grow(1)

			if f.focusPending {
				in.Focus()
				f.focusPending = false
			}

			if in.Changed() || f.query != f.prevQuery {
				f.prevQuery = f.query
				f.dirty = true
			}

			// 查询串变了就重算命中并跳到第一条；新消息落地时只重算、保持当前定位（本版只做消息级定位）。
			if f.dirty || f.rev != rev {
				jump := f.dirty
				f.dirty = false
				f.rev = rev
				f.matches = findMessageMatches(messages, f.query, f.caseSensitive)
				if len(f.matches) == 0 {
					f.cursor = 0
				} else if f.cursor >= len(f.matches) {
					f.cursor = len(f.matches) - 1
				} else if f.cursor < 0 {
					f.cursor = 0
				}
				if jump {
					f.cursor = 0
					if len(f.matches) > 0 {
						scrollToMessageID(s, items, f.matches[0])
					}
				}
			}

			in.OnTextKey(func(k ui.TextKey) bool {
				switch k.Key {
				case ui.KeyEnter:
					if k.Mods&ui.Shift != 0 {
						stepChatFind(s, f, items, -1)
					} else {
						stepChatFind(s, f, items, 1)
					}
					s.invalidate()
					return true
				case ui.KeyEscape:
					s.closeChatFind()
					return true
				case ui.KeyDown:
					stepChatFind(s, f, items, 1)
					s.invalidate()
					return true
				case ui.KeyUp:
					stepChatFind(s, f, items, -1)
					s.invalidate()
					return true
				}
				return false
			})

			if counter := chatFindCounterText(f); counter != "" {
				ui.Text(c, counter).
					FontSize(uiFont(FontSizeCapsuleText)).
					TextColor(ThemeSecondaryText(c)).
					SingleLine()
			}

			// 上一个 / 下一个命中
			prevBtn := ui.ButtonBase(c.Key("chat-find-prev-btn")).
				Size(ControlXS, ControlXS).
				Radius(RadiusXS).
				Center().
				Label("上一个匹配 " + shortcutDisplay("⇧↩")).
				Tooltip("上一个匹配 " + shortcutDisplay("⇧↩"))
			{
				scale := motionPressScale(c, prevBtn, "button-press-scale", prevBtn.Pressed())
				prevBtn.Scale(scale, scale)
			}
			if prevBtn.Hovered() {
				prevBtn.Background(ThemeHover(c))
			}
			if prevBtn.Clicked() {
				stepChatFind(s, f, items, -1)
				s.invalidate()
			}
			prevBtn.Children(func() {
				ui.Icon(c, IconChevronDown()).Size(IconSM, IconSM).Rotate(180).TextColor(ThemeSecondaryText(c))
			})

			nextBtn := ui.ButtonBase(c.Key("chat-find-next-btn")).
				Size(ControlXS, ControlXS).
				Radius(RadiusXS).
				Center().
				Label("下一个匹配 " + shortcutDisplay("↩")).
				Tooltip("下一个匹配 " + shortcutDisplay("↩"))
			{
				scale := motionPressScale(c, nextBtn, "button-press-scale", nextBtn.Pressed())
				nextBtn.Scale(scale, scale)
			}
			if nextBtn.Hovered() {
				nextBtn.Background(ThemeHover(c))
			}
			if nextBtn.Clicked() {
				stepChatFind(s, f, items, 1)
				s.invalidate()
			}
			nextBtn.Children(func() {
				ui.Icon(c, IconChevronDown()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
			})

			// 区分大小写开关
			caseBtn := ui.ButtonBase(c.Key("chat-find-case-btn")).
				Size(ControlXS, ControlXS).
				Radius(RadiusXS).
				Center().
				Label("区分大小写").
				Tooltip("区分大小写")
			{
				scale := motionPressScale(c, caseBtn, "button-press-scale", caseBtn.Pressed())
				caseBtn.Scale(scale, scale)
			}
			if f.caseSensitive {
				caseBtn.Background(ThemeSelected(c))
			} else if caseBtn.Hovered() {
				caseBtn.Background(ThemeHover(c))
			}
			if caseBtn.Clicked() {
				f.caseSensitive = !f.caseSensitive
				f.dirty = true
				s.invalidate()
			}
			caseBtn.Children(func() {
				color := ThemeSecondaryText(c)
				if f.caseSensitive {
					color = ThemeAccent(c)
				}
				ui.Text(c, "Aa").FontSize(uiFont(TypeCaption)).TextColor(color)
			})

			IconButton(c.Key("chat-find-close-btn"), IconButtonProps{
				Icon:  IconX(),
				Label: "关闭查找 Esc",
				OnClick: func() {
					s.closeChatFind()
				},
				Variant: ButtonGhost,
				Size:    ControlXS,
			})
		})
}
