package mcodeui

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"mcode/internal/swarm"
)

var reMultipleNewlines = regexp.MustCompile(`\n{3,}`)

// cleanExtraBlankLines 规整连续空行，最多保留单个空行（即连续两个换行），并修剪首尾多余换行符。
func cleanExtraBlankLines(text string) string {
	cleaned := reMultipleNewlines.ReplaceAllString(text, "\n\n")
	return strings.Trim(cleaned, "\r\n")
}

// parseFenceOpen 检查当前行是否为代码块开启标记，返回反引号数量、语言/参数和是否开启。
func parseFenceOpen(trimmed string) (ticks int, info string, ok bool) {
	if !strings.HasPrefix(trimmed, "```") {
		return 0, "", false
	}
	cnt := 0
	for cnt < len(trimmed) && trimmed[cnt] == '`' {
		cnt++
	}
	if cnt < 3 {
		return 0, "", false
	}
	info = strings.TrimSpace(trimmed[cnt:])
	if strings.Contains(info, "`") {
		return 0, "", false
	}
	return cnt, info, true
}

// isFenceClose 检查当前行是否为匹配的反引号闭合标记。
func isFenceClose(trimmed string, minTicks int) bool {
	if !strings.HasPrefix(trimmed, "```") {
		return false
	}
	cnt := 0
	for cnt < len(trimmed) && trimmed[cnt] == '`' {
		cnt++
	}
	if cnt < minTicks {
		return false
	}
	rest := strings.TrimSpace(trimmed[cnt:])
	return rest == ""
}

// isOrchestrationFenceInfo 判断围栏 info 是否属于编排派发块或答案块：
// 新名 swarm / swarm-answer 与旧名 hydra / hydra-answer 都认。
func isOrchestrationFenceInfo(info string) bool {
	lower := strings.ToLower(strings.TrimSpace(info))
	return strings.HasPrefix(lower, "swarm") || strings.HasPrefix(lower, "hydra")
}

// mentionsOrchestrationFence 粗筛正文里是否出现编排围栏关键字，避免每帧逐行扫描。
func mentionsOrchestrationFence(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "swarm") || strings.Contains(lower, "hydra")
}

// stripSwarmFence 去掉所有以三个或更多反引号加 swarm（旧名 hydra）开头的代码块，
// 包括流式输出中还没有闭合的尾部代码块，并去掉多余空行。
func stripSwarmFence(text string) string {
	if text == "" {
		return ""
	}
	if !strings.Contains(text, "```") || !mentionsOrchestrationFence(text) {
		return cleanExtraBlankLines(text)
	}

	lines := strings.Split(text, "\n")
	var out []string

	inSwarm := false
	swarmTicks := 0

	inNormal := false
	normalTicks := 0

	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")

		if inSwarm {
			if isFenceClose(trimmed, swarmTicks) {
				inSwarm = false
				swarmTicks = 0
			}
			continue
		}

		if inNormal {
			out = append(out, line)
			if isFenceClose(trimmed, normalTicks) {
				inNormal = false
				normalTicks = 0
			}
			continue
		}

		ticks, info, isFence := parseFenceOpen(trimmed)
		if isFence {
			if isOrchestrationFenceInfo(info) {
				inSwarm = true
				swarmTicks = ticks
				out = append(out, "")
				continue
			} else {
				inNormal = true
				normalTicks = ticks
				out = append(out, line)
				continue
			}
		}

		out = append(out, line)
	}

	result := strings.Join(out, "\n")
	return cleanExtraBlankLines(result)
}

// countSwarmTaskKeys 统计派单块正文里 "task" 键出现的次数：流式中还没闭合的块无法按 JSON 解析，
// 用它近似已经写到第几个条目。
func countSwarmTaskKeys(body string) int {
	return strings.Count(body, `"task"`)
}

// swarmFenceDispatchInfo 判定回复正文里编排派单块（三引号加 swarm，旧名 hydra）的状态。
// 判定规则与 stripSwarmFence 完全一致（共用 parseFenceOpen / isFenceClose），所以「从正文里剥掉的块」
// 与「这里数出来的块」永远是同一批：
//   - open：存在尚未闭合的派单块，即流式输出中正在写的那一段；
//   - closed：存在已经闭合的派单块；
//   - count：已写出的派单条目数，闭合块用 swarm.ParseDelegations 计数，未闭合块数 "task" 键出现次数。
func swarmFenceDispatchInfo(text string) (open bool, closed bool, count int) {
	if text == "" || !strings.Contains(text, "```") || !mentionsOrchestrationFence(text) {
		return false, false, 0
	}

	lines := strings.Split(text, "\n")
	inSwarm := false
	swarmTicks := 0
	swarmInfo := ""
	var body []string

	inNormal := false
	normalTicks := 0

	// flush 结算刚结束的一个派单块：闭合块交给 ParseDelegations 数条目，未闭合块数 "task" 键。
	flush := func(complete bool) {
		joined := strings.Join(body, "\n")
		if !complete {
			open = true
			count += countSwarmTaskKeys(joined)
			return
		}
		closed = true
		block := "```" + swarmInfo + "\n" + joined + "\n```"
		if entries, found, err := swarm.ParseDelegations(block); found && err == nil {
			count += len(entries)
			return
		}
		count += countSwarmTaskKeys(joined)
	}

	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")

		if inSwarm {
			if isFenceClose(trimmed, swarmTicks) {
				flush(true)
				inSwarm = false
				swarmTicks = 0
				swarmInfo = ""
				body = nil
				continue
			}
			body = append(body, line)
			continue
		}

		if inNormal {
			if isFenceClose(trimmed, normalTicks) {
				inNormal = false
				normalTicks = 0
			}
			continue
		}

		ticks, info, isFence := parseFenceOpen(trimmed)
		if !isFence {
			continue
		}
		if isOrchestrationFenceInfo(info) {
			inSwarm = true
			swarmTicks = ticks
			swarmInfo = info
			body = nil
			continue
		}
		inNormal = true
		normalTicks = ticks
	}

	if inSwarm {
		flush(false)
	}
	return open, closed, count
}

// isWordChar 判断字符是否为单词字符（英文字母、数字或下划线）。
func isWordChar(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
}

// SwarmMentionMatch 记录正文中匹配到的子代理提及信息。
type SwarmMentionMatch struct {
	Start int
	End   int
	Bee   SwarmBee
}

// matchSwarmMentions 按词边界在 text 中匹配已派出的子代理名字，
// 优先匹配最长名字（确保“Aspen 2”优先于“Aspen”），并防止在更长英文单词或数字内部误匹配。
func matchSwarmMentions(text string, bees []SwarmBee) []SwarmMentionMatch {
	if text == "" || len(bees) == 0 {
		return nil
	}

	var candidates []SwarmBee
	seen := make(map[string]bool)
	for _, h := range bees {
		name := strings.TrimSpace(h.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		candidates = append(candidates, h)
	}
	if len(candidates) == 0 {
		return nil
	}

	// 优先匹配最长名字
	sort.SliceStable(candidates, func(i, j int) bool {
		return len(candidates[i].Name) > len(candidates[j].Name)
	})

	var matches []SwarmMentionMatch
	i := 0
	for i < len(text) {
		var matchedBee *SwarmBee
		for idx := range candidates {
			cand := &candidates[idx]
			if strings.HasPrefix(text[i:], cand.Name) {
				// 汉字没有空格分词：名字本身以汉字开头/结尾时，要求相邻字符也不是汉字，
				// 否则“测试”会命中“重试测试”“补测试用例”这类普通词组。
				nameFirst, _ := utf8.DecodeRuneInString(cand.Name)
				nameLast, _ := utf8.DecodeLastRuneInString(cand.Name)
				nameStartsHan := unicode.Is(unicode.Han, nameFirst)
				nameEndsHan := unicode.Is(unicode.Han, nameLast)
				// 前边界检查：不能紧挨着单词字符；汉字开头的名字还要求前一个字符不是汉字
				beforeOK := true
				if i > 0 {
					prevRune, _ := utf8.DecodeLastRuneInString(text[:i])
					if isWordChar(prevRune) || (nameStartsHan && unicode.Is(unicode.Han, prevRune)) {
						beforeOK = false
					}
				}
				// 后边界检查：不能紧挨着单词字符；汉字结尾的名字还要求后一个字符不是汉字
				afterOK := true
				after := i + len(cand.Name)
				if after < len(text) {
					nextRune, _ := utf8.DecodeRuneInString(text[after:])
					if isWordChar(nextRune) || (nameEndsHan && unicode.Is(unicode.Han, nextRune)) {
						afterOK = false
					}
				}

				if beforeOK && afterOK {
					matchedBee = cand
					break
				}
			}
		}

		if matchedBee != nil {
			matchLen := len(matchedBee.Name)
			matches = append(matches, SwarmMentionMatch{
				Start: i,
				End:   i + matchLen,
				Bee:   *matchedBee,
			})
			i += matchLen
		} else {
			_, size := utf8.DecodeRuneInString(text[i:])
			if size == 0 {
				break
			}
			i += size
		}
	}

	return matches
}

// getSwarmBeesForChat 获取当前 chatState 对应的已派出子代理列表。
// swarmMentionCacheMax 是提及缓存的最大条目数：超过后整表清空重来，宁可多算一次也不让它无限增长。
const swarmMentionCacheMax = 64

// swarmMentionMatches 返回正文里提到的子代理；结果按“编排派生视图 + 正文”缓存。
// 每个可见的助手消息每帧都要问一次“有没有 @ 子代理”，30 个子代理时匹配本身要建名字索引、
// 排序与去重，每条消息逐帧重算纯属浪费。视图指针变了（子代理名单/状态落地新版本）就整表作废。
func (s *chatState) swarmMentionMatches(text string) []SwarmMentionMatch {
	if s == nil || text == "" {
		return nil
	}
	view := swarmStateViewFor(s)
	if view == nil || len(view.active) == 0 {
		return nil
	}
	s.mu.Lock()
	if s.swarmMentionView != view {
		s.swarmMentionView = view
		s.swarmMentionCache = nil
	}
	if matches, ok := s.swarmMentionCache[text]; ok {
		s.mu.Unlock()
		return matches
	}
	s.mu.Unlock()

	matches := matchSwarmMentions(text, view.active)

	s.mu.Lock()
	if s.swarmMentionView == view {
		if s.swarmMentionCache == nil {
			s.swarmMentionCache = make(map[string][]SwarmMentionMatch, swarmMentionCacheMax)
		} else if len(s.swarmMentionCache) >= swarmMentionCacheMax {
			s.swarmMentionCache = make(map[string][]SwarmMentionMatch, swarmMentionCacheMax)
		}
		s.swarmMentionCache[text] = matches
	}
	s.mu.Unlock()
	return matches
}

// getSwarmBeesForChat 返回当前会话的有效子代理列表（直接读派生视图，不再每帧拷贝整份列表），
// 发现编排状态还没加载到当前会话时顺手触发一次后台加载。
func getSwarmBeesForChat(s *chatState) []SwarmBee {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	selectedID := s.selectedThreadID
	s.mu.Unlock()

	hp := getSwarmPanelState(s)
	if hp == nil {
		return nil
	}

	swarmPanelMu.Lock()
	loadedFor := hp.loadedFor
	loading := hp.loading
	view := hp.viewLocked()
	swarmPanelMu.Unlock()

	if selectedID != "" && loadedFor != selectedID && !loading {
		s.loadSwarm(selectedID)
	}

	if view == nil {
		return nil
	}
	return view.active
}

// swarmMentionIconSize 返回正文里 bee 头像的边长：与名字同处一行的 12pt 行内对象，高度贴合文字。
func swarmMentionIconSize() float32 {
	return scaledFont(12)
}

// mentionIconSlot 是一个 bee 头像的落点：行内对象占位元素本帧的字框就是图标的位置，
// 图标与颜色由段落在绘制时叠画，图标缺失时退化成同色圆点。
// 字框只有布局之后才有，所以坐标不能在建视图时算，只能留到 DrawOver 里取。
type mentionIconSlot struct {
	slot  ui.Element
	icon  *ui.SVG
	color ui.Color
	size  float32
}

// paint 在占位框（本帧字框）里叠画头像：图标按 12pt 缩放后垂直居中，颜色用人设色。
func (slot mentionIconSlot) paint(p *ui.Painter) {
	box := slot.slot.LayoutBounds()
	if box.W <= 0 || box.H <= 0 {
		return
	}
	r := ui.Rect{X: box.X, Y: box.Y + (box.H-slot.size)/2, W: slot.size, H: slot.size}
	if slot.icon != nil {
		p.Icon(slot.icon, r, slot.color)
		return
	}
	// 图标资源缺失时退化成同色圆点，占位宽度不变，折行结果也不变。
	p.Fill(r, slot.color, slot.size/2)
}

// renderSwarmMentionInline 把 bee 名字建成段落里的行内文字：名字前是 12pt 的行内对象占位
// （U+FFFC，按对象宽度占位、行会绕开它折行），名字本身仍是专属人设颜色加粗、悬停时叠加
// 主题悬停底色、Tooltip 与点击弹出独立窗口，与前后正文连排、一起折行。
// 段落里只放得下文本元素，图标画不进来：返回占位元素，段落用 DrawOver 把头像叠画在它的字框里。
func renderSwarmMentionInline(c *ui.Context, s *chatState, key string, bee SwarmBee) mentionIconSlot {
	persona := findSwarmPersona(bee.Name)
	color := persona.Color
	if color.A == 0 {
		color = swarmAvatarColor(bee.Name)
	}

	open := func() {
		if s != nil && bee.ThreadID != "" {
			s.popOutSwarmBee(bee.ThreadID)
		}
	}

	// 占位元素自己的文字是对象替换符，只有它的对象宽高算进段落；点图标与点名字一样弹出窗口。
	size := swarmMentionIconSize()
	slot := ui.RichText(c.Key(key+"-icon"), ui.Span{
		Text:         "\uFFFC",
		ObjectWidth:  size,
		ObjectHeight: size,
	}).Tooltip("在独立窗口中打开").OnClick(open)

	name := ui.Text(c.Key(key), bee.Name).
		FontWeight(700).
		TextColor(color).
		Tooltip("在独立窗口中打开").
		OnClick(open)
	if name.Hovered() {
		name.Background(ThemeHover(c))
	} else {
		// 行内元素没有圆角，只用很淡的同色底色把名字标出来。
		name.Background(color.Alpha(0.12))
	}
	return mentionIconSlot{slot: slot, icon: safePersonaIcon(persona.Asset), color: color, size: size}
}

// renderSwarmPersonaIcon 画 bee 的人设图标：正文行内与底部胶囊共用同一套图标，无圆底，
// 颜色用人设色（人设为空时退回按名字取色），图标资源缺失时退化成同色圆点。
func renderSwarmPersonaIcon(c *ui.Context, key, name string, size float32) {
	persona := findSwarmPersona(name)
	color := persona.Color
	if color.A == 0 {
		color = swarmAvatarColor(name)
	}
	if icon := safePersonaIcon(persona.Asset); icon != nil {
		ui.Icon(c.Key(key), icon).Size(size, size).TextColor(color)
		return
	}
	ui.Box(c.Key(key)).Size(size, size).Radius(size / 2).Background(color)
}

// mentionPiece 表示单行富文本中的切图片段。
type mentionPiece struct {
	isBee bool
	text  string
	bee   SwarmBee
}

// splitLineByMentions 将单行文本按匹配到的 mentions 切分为普通文本片段与子代理芯片。
func splitLineByMentions(line string, matches []SwarmMentionMatch) []mentionPiece {
	if len(matches) == 0 {
		return []mentionPiece{{isBee: false, text: line}}
	}

	var pieces []mentionPiece
	lastIdx := 0
	for _, m := range matches {
		if m.Start > lastIdx {
			pieces = append(pieces, mentionPiece{
				isBee: false,
				text:  line[lastIdx:m.Start],
			})
		}
		pieces = append(pieces, mentionPiece{
			isBee: true,
			bee:   m.Bee,
		})
		lastIdx = m.End
	}
	if lastIdx < len(line) {
		pieces = append(pieces, mentionPiece{
			isBee: false,
			text:  line[lastIdx:],
		})
	}
	return pieces
}

// contentChunk 区分代码块与普通文本区域。
type contentChunk struct {
	isCode bool
	text   string
}

// splitTextByCodeBlocks 将正文划分为围栏代码块和普通文本块。
func splitTextByCodeBlocks(text string) []contentChunk {
	if !strings.Contains(text, "```") {
		return []contentChunk{{isCode: false, text: text}}
	}

	lines := strings.Split(text, "\n")
	var chunks []contentChunk
	var curLines []string
	inCode := false
	codeTicks := 0

	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if inCode {
			curLines = append(curLines, line)
			if isFenceClose(trimmed, codeTicks) {
				chunks = append(chunks, contentChunk{
					isCode: true,
					text:   strings.Join(curLines, "\n"),
				})
				curLines = nil
				inCode = false
				codeTicks = 0
			}
			continue
		}

		ticks, _, isFence := parseFenceOpen(trimmed)
		if isFence {
			if len(curLines) > 0 {
				chunks = append(chunks, contentChunk{
					isCode: false,
					text:   strings.Join(curLines, "\n"),
				})
				curLines = nil
			}
			curLines = append(curLines, line)
			inCode = true
			codeTicks = ticks
			continue
		}

		curLines = append(curLines, line)
	}

	if len(curLines) > 0 {
		chunks = append(chunks, contentChunk{
			isCode: inCode,
			text:   strings.Join(curLines, "\n"),
		})
	}
	return chunks
}

// mentionBlockKind 区分渲染块类型：只有非代码块才可能走「文字+徽标」行内混排。
type mentionBlockKind int

const (
	mentionBlockProse mentionBlockKind = iota // 普通正文（标题、列表、段落）
	mentionBlockCode                          // 围栏代码块
	mentionBlockTable                         // 连续表格行
)

// mentionBlock 是正文按空行切分出的一个渲染块。
type mentionBlock struct {
	kind mentionBlockKind
	text string
	// hasBee 表示块内出现已派出的 bee 名字：为真才走行内混排，否则整块交给 Markdown。
	hasBee bool
}

// splitMentionBlocks 将助手正文切分为渲染块：围栏代码块整体成块（内部空行不拆），
// 连续表格行整体成块（流式中途的空行不拆），其余按空行切分。
func splitMentionBlocks(text string, bees []SwarmBee) []mentionBlock {
	var blocks []mentionBlock
	for _, chunk := range splitTextByCodeBlocks(text) {
		if chunk.isCode {
			if block, ok := newMentionBlock(mentionBlockCode, chunk.text, bees); ok {
				blocks = append(blocks, block)
			}
			continue
		}
		blocks = append(blocks, splitProseMentionBlocks(chunk.text, bees)...)
	}
	return blocks
}

// splitProseMentionBlocks 把非代码区正文按空行切块；以 | 开头的表格行即使被空行分开也留在同一块内。
func splitProseMentionBlocks(text string, bees []SwarmBee) []mentionBlock {
	var blocks []mentionBlock
	var cur []string
	inTable := false
	flush := func() {
		if len(cur) > 0 {
			if block, ok := newMentionBlock(mentionBlockProse, strings.Join(cur, "\n"), bees); ok {
				if inTable {
					block.kind = mentionBlockTable
				}
				blocks = append(blocks, block)
			}
			cur = nil
		}
		inTable = false
	}

	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			// 表格行之间的空行不打断表格
			if inTable {
				continue
			}
			flush()
			continue
		}
		isTable := strings.HasPrefix(trimmed, "|")
		if isTable != inTable && len(cur) > 0 {
			flush()
		}
		inTable = isTable
		cur = append(cur, line)
	}
	flush()
	return blocks
}

// newMentionBlock 构造渲染块：去掉首尾换行，并判定块内是否提到已派出的 bee；纯空白块被丢弃。
func newMentionBlock(kind mentionBlockKind, text string, bees []SwarmBee) (mentionBlock, bool) {
	text = strings.Trim(text, "\n")
	if strings.TrimSpace(text) == "" {
		return mentionBlock{}, false
	}
	return mentionBlock{kind: kind, text: text, hasBee: len(matchSwarmMentions(text, bees)) > 0}, true
}

// mentionStrongWeight 是行内 ** 粗体与标题的文字粗细，与 Markdown 主题的 StrongWeight 一致。
const mentionStrongWeight = 600

// mentionListIndentEm 是列表圆点的缩进宽度（Markdown 主题的 ListIndent），单位是正文的 em。
const mentionListIndentEm = 1.6

// mentionHeadingEm 返回标题相对正文的字号倍数，与 ensureChatMarkdown 里的标题字号保持一致。
func mentionHeadingEm(level int) float32 {
	switch level {
	case 1:
		return 1.30
	case 2:
		return 1.15
	default:
		return 1.00
	}
}

// mentionLineStyle 记录一行在渲染前剥离出的行级 Markdown 语义。
type mentionLineStyle struct {
	heading int    // 标题级别，0 表示不是标题
	list    bool   // 是否无序列表项
	content string // 剥离行首标记后的正文
}

// parseMentionLineStyle 剥离行首的标题井号与无序列表圆点标记，让行内混排按语义渲染，
// 不再显示字面的 ## 与 - **。表格行（以 | 开头）与普通正文原样返回。
func parseMentionLineStyle(line string) mentionLineStyle {
	trimmed := strings.TrimLeft(line, " \t")
	if strings.HasPrefix(trimmed, "|") {
		return mentionLineStyle{content: trimmed}
	}

	// 标题：1-6 个 # 后紧跟空格（或行尾）
	if n := countLeadingHashes(trimmed); n > 0 {
		rest := trimmed[n:]
		if rest == "" || rest[0] == ' ' || rest[0] == '\t' {
			return mentionLineStyle{heading: n, content: strings.TrimLeft(rest, " \t")}
		}
	}

	// 无序列表：- / * / + 后紧跟空格
	for _, marker := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(trimmed, marker) {
			return mentionLineStyle{list: true, content: strings.TrimSpace(trimmed[len(marker):])}
		}
	}
	return mentionLineStyle{content: trimmed}
}

// countLeadingHashes 返回行首连续的 # 个数（最多 6），不构成标题时返回 0。
func countLeadingHashes(trimmed string) int {
	n := 0
	for n < len(trimmed) && trimmed[n] == '#' {
		n++
	}
	if n == 0 || n > 6 {
		return 0
	}
	return n
}

// renderAssistantContentWithMentions 为助手回复正文渲染 Markdown 与 Swarm Bee 提及富文本，
// 返回正文这一帧是否已完全显示（markdown.Revealed），供上层决定何时露出时间与操作栏。
func renderAssistantContentWithMentions(c *ui.Context, s *chatState, m Message, isStreamingNow bool, lastLiveID string) bool {
	bees := getSwarmBeesForChat(s)
	// 若无已派出 bee，或正文中未提及任何 bee，完全走原有 markdown 渲染
	if len(bees) == 0 || len(s.swarmMentionMatches(m.Text)) == 0 {
		return renderAssistantMarkdownDefault(c, s, m, isStreamingNow, lastLiveID)
	}

	// 按块渲染：代码块与不含 bee 名字的块交给 Markdown（标题、列表、表格、链接照常渲染），
	// 含 bee 名字的块才做「文字+徽标」行内混排。块 key 只按块序号区分，
	// 流式只会在末尾追加新块，已有块的 key 不变，避免整段重建闪烁。
	blocks := splitMentionBlocks(m.Text, bees)
	if len(blocks) == 0 {
		return renderAssistantMarkdownDefault(c, s, m, isStreamingNow, lastLiveID)
	}
	// 只有走 Markdown 的块带揭示动画：所有块都显示完才算整条正文已完全显示。
	revealed := true
	for blockIdx, block := range blocks {
		blockKey := fmt.Sprintf("mention-block-%s-%d", m.ID, blockIdx)
		if block.kind == mentionBlockCode || !block.hasBee {
			if !markdown.Revealed(markdown.View(c.Key(blockKey), block.text, &s.mdStaticOpts)) {
				revealed = false
			}
			continue
		}
		renderMentionBlock(c, s, blockKey, block.text)
	}

	// 流式输出末尾光标
	if isStreamingNow {
		ui.Text(c.Key("msg-cursor-"+m.ID), streamingCursor).
			FontSize(scaledFont(13)).
			TextColor(ThemePrimaryText(c))
	}
	return revealed
}

// renderMentionBlock 渲染含 bee 提及的块：逐行做「正文+bee 名字」行内混排。
func renderMentionBlock(c *ui.Context, s *chatState, blockKey, text string) {
	lines := strings.Split(text, "\n")
	ui.Column(c.Key(blockKey)).AlignSelf(ui.Stretch).Gap(4).Margin(2, 0).Children(func() {
		for lineIdx, line := range lines {
			if strings.TrimSpace(line) == "" {
				continue
			}
			renderMentionLine(c, s, fmt.Sprintf("%s-l-%d", blockKey, lineIdx), line)
		}
	})
}

// renderMentionLine 渲染一条含 bee 提及的行：标题行去掉井号并按 Markdown 标题的字号与字重渲染，
// 列表项补上与正文列表一致的圆点缩进，行内 ** 粗体标记剥离后加粗，bee 名字是段落里的行内文字。
// 每行只建一个段落：普通文字与 bee 名字同属一段、一起自然折行，
// 不会因为标签放不下就整块换行、把后面的正文挤到下一行行首。
func renderMentionLine(c *ui.Context, s *chatState, lineKey, line string) {
	style := parseMentionLineStyle(line)
	// 正文基准字号同样跟随「文字大小」，与同一块里的 Markdown 正文保持一致。
	baseSize := scaledFont(13)
	size := baseSize
	weight := 0
	if style.heading > 0 {
		size = baseSize * mentionHeadingEm(style.heading)
		weight = mentionStrongWeight
	}

	pieces := splitLineByMentions(style.content, s.swarmMentionMatches(style.content))
	if !style.list {
		para := renderMentionParagraph(c, s, lineKey, pieces, size, weight)
		if style.heading > 0 {
			// 标题上方留白略大，与 Markdown 标题一致
			para.Margin(baseSize*0.46, 0, 0, 0)
		}
		return
	}

	// 列表项：圆点占固定宽度、位于行首，正文是可伸缩的段落，
	// 折行时留在圆点右侧，不会回到圆点下方。
	row := ui.Row(c.Key(lineKey)).AlignSelf(ui.Stretch).Gap(2)
	row.Children(func() {
		ui.Text(c, "•").
			FontSize(baseSize).
			TextColor(ThemeSecondaryText(c)).
			Width(baseSize * mentionListIndentEm).
			TextAlign(ui.Center).
			AlignSelf(ui.Start)
		renderMentionParagraph(c, s, lineKey+"-text", pieces, size, weight).Grow(1)
	})
}

// renderMentionParagraph 把一行的片段建成一个段落：普通文字按 ** 切分加粗，bee 名字是段落里的
// 行内文字，名字前是 12pt 的头像占位（行内对象），头像本身在段落画完文字后叠画在占位框上。
// 字号、行高与字重由段落统一给出，行内片段只覆盖自己的颜色与粗细。
func renderMentionParagraph(c *ui.Context, s *chatState, key string, pieces []mentionPiece, size float32, weight int) ui.Element {
	para := ui.RichText(c.Key(key)).
		AlignSelf(ui.Stretch).
		FontSize(size).
		LineHeight(1.45).
		TextColor(ThemePrimaryText(c))
	if weight > 0 {
		para.FontWeight(weight)
	}
	var icons []mentionIconSlot
	para.Children(func() {
		bold := false
		for pIdx, piece := range pieces {
			itemKey := fmt.Sprintf("%s-it-%d", key, pIdx)
			if piece.isBee {
				icons = append(icons, renderSwarmMentionInline(c, s, itemKey, piece.bee))
				continue
			}
			bold = renderMentionText(c, itemKey, piece.text, size, weight, bold)
		}
	})
	if len(icons) > 0 {
		// 头像叠画在占位框上：占位框的位置要等布局定下折行才知道，
		// 段落画完文字与子元素之后按本帧字框补画图标。
		para.DrawOver(func(p *ui.Painter, _ ui.Rect) {
			for _, slot := range icons {
				slot.paint(p)
			}
		})
	}
	return para
}

// renderMentionText 渲染一段不含 bee 名字的文本，作为段落的行内子元素：** 粗体标记被剥离并按加粗渲染。
// 返回值是这段文本末尾的加粗状态，供同一行的后续片段延续（** 可以跨过 bee 名字）。
func renderMentionText(c *ui.Context, key, text string, size float32, weight int, bold bool) bool {
	for segIdx, seg := range strings.Split(text, "**") {
		if segIdx > 0 {
			bold = !bold
		}
		if seg == "" {
			continue
		}
		w := weight
		if bold && w < mentionStrongWeight {
			w = mentionStrongWeight
		}
		el := ui.Text(c.Key(fmt.Sprintf("%s-b-%d", key, segIdx)), seg).
			FontSize(size).
			LineHeight(1.45).
			TextColor(ThemePrimaryText(c))
		if w > 0 {
			el.FontWeight(w)
		}
	}
	return bold
}

// renderLiveMarkdown 渲染正在流式输出的正文：需要逐字揭示时走 markdown.Stream，
// 否则（打开会话时已有的正文、视图重建后已显示过的正文）整段显示，不重播打字机动画。
// takeStreamReveal 会推进基线，每帧每条消息只调用一次。
func renderLiveMarkdown(c *ui.Context, s *chatState, m Message, o *markdown.Options) bool {
	if s.takeStreamReveal(m) {
		return markdown.Revealed(markdown.Stream(c, m.Text, o))
	}
	return markdown.Revealed(markdown.View(c, m.Text, o))
}

// renderAssistantMarkdownDefault 执行原本的 Markdown 渲染，分三种情况：
//  1. 本条正在流式输出且需要逐字揭示：走 markdown.Stream，带末尾光标；
//  2. 本条正在流式输出但内容在打开会话时就已存在（takeStreamReveal 返回 false，
//     为避免打字机重播）：直接完整渲染，但仍保留末尾流式光标；
//  3. 非流式消息：最近一条实时消息用 mdOpts，其余用 mdStaticOpts。
//
// 四处文本都包一层 findHighlightSource：查找条打开且本条命中时它会在源码层注入 <mark>，
// 查找条关闭时原样返回、不扫描（字级高亮的来源见 chat_find.go）。
func renderAssistantMarkdownDefault(c *ui.Context, s *chatState, m Message, isStreamingNow bool, lastLiveID string) bool {
	if isStreamingNow {
		// takeStreamReveal 会推进基线，每帧只调用一次。
		if s.takeStreamReveal(m) {
			return markdown.Revealed(markdown.Stream(c, findHighlightSource(s, m.ID, m.Text+streamingCursor), &s.mdOpts))
		}
		return markdown.Revealed(markdown.View(c, findHighlightSource(s, m.ID, m.Text+streamingCursor), &s.mdStaticOpts))
	}
	switch {
	case lastLiveID != "" && m.ID == lastLiveID:
		return markdown.Revealed(markdown.View(c, findHighlightSource(s, m.ID, linkifyMessageText(s, m.Text)), &s.mdOpts))
	default:
		return markdown.Revealed(markdown.View(c, findHighlightSource(s, m.ID, linkifyMessageText(s, m.Text)), &s.mdStaticOpts))
	}
}
