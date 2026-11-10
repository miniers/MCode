package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Roster 定义编排子代理的标准名字轮转表（共 25 位）。
var Roster = []string{
	"Aspen", "Birch", "Cove", "Dune", "Ember",
	"Flint", "Grove", "Heath", "Isle", "Jade",
	"Kestrel", "Larch", "Moss", "Nimbus", "Onyx",
	"Pike", "Quill", "Reed", "Sage", "Tide",
	"Umber", "Vale", "Wren", "Yarrow", "Zephyr",
}

// RosterName 根据索引返回协作者名字；超过 25 个后追加轮次后缀（如 "Aspen 2"）。
func RosterName(index int) string {
	count := len(Roster)
	if count == 0 {
		return ""
	}
	baseIdx := ((index % count) + count) % count
	base := Roster[baseIdx]
	round := 0
	if index > 0 {
		round = index / count
	}
	if round > 0 {
		return fmt.Sprintf("%s %d", base, round+1)
	}
	return base
}

// RosterIndex 返回基础名字的索引，显示用的轮次后缀不参与派发。
func RosterIndex(name string) (int, bool) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return 0, false
	}
	bare := trimmed
	if lastSpace := strings.LastIndex(trimmed, " "); lastSpace != -1 {
		suffix := strings.TrimSpace(trimmed[lastSpace+1:])
		if _, err := strconv.Atoi(suffix); err == nil {
			bare = strings.TrimSpace(trimmed[:lastSpace])
		}
	}
	baseIndex := -1
	for i, r := range Roster {
		if strings.EqualFold(r, bare) {
			baseIndex = i
			break
		}
	}
	if baseIndex == -1 {
		return 0, false
	}
	return baseIndex, true
}

// Delegation 描述由主会话声明派发的单个子任务规格。
type Delegation struct {
	Task     string
	Prompt   string
	Name     string
	Project  string
	Provider string
	Model    string
	Profile  string
	Pair     string
	Tier     string
	// Skills 是声明给该 bee 的技能名；为空时 bee 不携带任何技能提示块。
	Skills []string
	// Replaces 是要取代的在跑 bee 的名字，宿主会先停掉该 bee 再启动本条。
	Replaces string
	// ReworkOf 是要返工的已驳回 bee 的名字或 ThreadID；为空时按 brief 里的补丁路径推断。
	ReworkOf string
}

// delegationSkillsMax 是单个条目最多保留的技能名数量，避免一条派发就能把全部技能塞进提示词。
const delegationSkillsMax = 8

// openerRegex 匹配派发块起始行：新围栏 ```swarm，兼容旧围栏 ```hydra 以及 -sent / -leaving 变体。
var openerRegex = regexp.MustCompile("(?i)```[ \\t]*(?:swarm|hydra)(?:-sent|-leaving)?[ \\t]*\\r?\\n")

func findLineStartFences(text string, start int) []int {
	var fences []int
	cursor := start
	for {
		rel := strings.Index(text[cursor:], "```")
		if rel == -1 {
			break
		}
		pos := cursor + rel
		if pos == 0 || text[pos-1] == '\n' || text[pos-1] == '\r' {
			fences = append(fences, pos)
		}
		cursor = pos + 3
	}
	return fences
}

func singleLinePrompt(prompt string, limit int) string {
	s := strings.TrimLeft(prompt, "\r\n")
	if idx := strings.IndexAny(s, "\r\n"); idx != -1 {
		s = s[:idx]
	}
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return s
}

// ParseEntry 以与 ```swarm 块条目相同的清洗规则解析单个条目对象（swarm_delegate 工具的入口）；
// 缺少有效 prompt 时返回 false。
func ParseEntry(m map[string]any) (Delegation, bool) {
	return parseEntry(m)
}

func parseEntry(m map[string]any) (Delegation, bool) {
	promptRaw, _ := m["prompt"].(string)
	prompt := strings.TrimSpace(promptRaw)
	if prompt == "" {
		return Delegation{}, false
	}

	taskRaw, _ := m["task"].(string)
	task := strings.TrimSpace(taskRaw)
	if task == "" {
		task = singleLinePrompt(prompt, 60)
	}

	// 名字键优先读 bee，兼容历史条目里的 name 与更早的 head。
	nameRaw, _ := m["bee"].(string)
	if strings.TrimSpace(nameRaw) == "" {
		nameRaw, _ = m["name"].(string)
	}
	if strings.TrimSpace(nameRaw) == "" {
		nameRaw, _ = m["head"].(string)
	}
	name := strings.TrimSpace(nameRaw)

	projRaw, _ := m["project"].(string)
	if strings.TrimSpace(projRaw) == "" {
		projRaw, _ = m["repo"].(string)
	}
	project := strings.TrimSpace(projRaw)

	provRaw, _ := m["provider"].(string)
	provider := strings.TrimSpace(provRaw)

	modelRaw, _ := m["model"].(string)
	model := strings.TrimSpace(modelRaw)

	profRaw, _ := m["profile"].(string)
	profile := strings.TrimSpace(profRaw)

	pairRaw, _ := m["pair"].(string)
	pair := strings.TrimSpace(pairRaw)

	tierRaw, _ := m["tier"].(string)
	tier := strings.TrimSpace(tierRaw)

	replacesRaw, _ := m["replaces"].(string)
	replaces := strings.TrimSpace(replacesRaw)

	reworkRaw, _ := m["reworkOf"].(string)
	if strings.TrimSpace(reworkRaw) == "" {
		reworkRaw, _ = m["rework_of"].(string)
	}
	reworkOf := strings.TrimSpace(reworkRaw)

	return Delegation{
		Task:     task,
		Prompt:   prompt,
		Name:     name,
		Project:  project,
		Provider: provider,
		Model:    model,
		Profile:  profile,
		Pair:     pair,
		Tier:     tier,
		Skills:   parseSkills(m["skills"]),
		Replaces: replaces,
		ReworkOf: reworkOf,
	}, true
}

// parseSkills 宽松解析条目里的 skills：接受字符串数组或单个逗号分隔字符串，
// 逐项 TrimSpace、丢空、按小写去重保序，最多保留 delegationSkillsMax 个；
// 类型不对时静默返回 nil，不影响条目本身的有效性。
func parseSkills(raw any) []string {
	var items []string
	switch v := raw.(type) {
	case []any:
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				continue
			}
			items = append(items, s)
		}
	case string:
		items = strings.Split(v, ",")
	default:
		return nil
	}

	skills := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		skills = append(skills, name)
		if len(skills) >= delegationSkillsMax {
			break
		}
	}
	if len(skills) == 0 {
		return nil
	}
	return skills
}

// repairInvalidEscapes 修复 JSON 文本里非法的反斜杠转义：逐字符扫描，
// 字符串字面量内部遇到后接非法转义字符的反斜杠时补成两个反斜杠，
// 使其按字面反斜杠解码（如提示词里的 out\model.onnx）；字符串外部的内容原样保留。
func repairInvalidEscapes(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	inString := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !inString {
			if c == '"' {
				inString = true
			}
			b.WriteByte(c)
			continue
		}
		switch c {
		case '"':
			inString = false
			b.WriteByte(c)
		case '\\':
			if n := jsonEscapeLen(s, i+1); n > 0 {
				b.WriteString(s[i : i+1+n])
				i += n
				continue
			}
			b.WriteString(`\\`)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// jsonEscapeLen 返回 s[i] 起（i 指向前导反斜杠之后）合法 JSON 转义序列的长度，
// 非法转义返回 0。
func jsonEscapeLen(s string, i int) int {
	if i >= len(s) {
		return 0
	}
	switch s[i] {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		return 1
	case 'u':
		if i+4 >= len(s) {
			return 0
		}
		for j := i + 1; j < i+5; j++ {
			c := s[j]
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
				return 0
			}
		}
		return 5
	}
	return 0
}

func parseCandidateBody(body string) (entries []Delegation, emptyArray bool, ok bool, reason string) {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return nil, false, false, "委派块内容为空"
	}

	var raw any
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		// 委派块里的字面反斜杠常被写成非法转义：修复后再试一次，仍失败才按原错误上报。
		firstErr := err
		raw = nil
		if err := json.Unmarshal([]byte(repairInvalidEscapes(trimmed)), &raw); err != nil {
			return nil, false, false, fmt.Sprintf("JSON 反序列化失败: %v", firstErr)
		}
	}

	switch v := raw.(type) {
	case []any:
		if len(v) == 0 {
			return nil, true, true, ""
		}
		var list []Delegation
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if d, valid := parseEntry(m); valid {
					list = append(list, d)
				}
			}
		}
		if len(list) == 0 {
			return nil, false, false, "数组中没有包含有效 prompt 的条目"
		}
		return list, false, true, ""

	case map[string]any:
		if d, valid := parseEntry(v); valid {
			return []Delegation{d}, false, true, ""
		}
		return nil, false, false, "对象条目中缺少有效 prompt"

	default:
		return nil, false, false, "委派块 JSON 不是数组或对象"
	}
}

// ParseDelegations 解析回复文本中的编排派发代码块。
// 新围栏是 ```swarm，旧围栏 ```hydra 继续兼容解析。
func ParseDelegations(text string) (entries []Delegation, found bool, err error) {
	loc := openerRegex.FindStringIndex(text)
	if loc == nil {
		return nil, false, nil
	}

	bodyStart := loc[1]
	fences := findLineStartFences(text, bodyStart)

	var ends []int
	for i := len(fences) - 1; i >= 0; i-- {
		ends = append(ends, fences[i])
	}
	ends = append(ends, len(text))

	lastReason := "无有效候选"
	for _, end := range ends {
		if end < bodyStart {
			continue
		}
		candidate := text[bodyStart:end]
		list, emptyArray, ok, reason := parseCandidateBody(candidate)
		if ok {
			if emptyArray {
				return nil, true, nil
			}
			return list, true, nil
		}
		lastReason = reason
	}

	return nil, true, fmt.Errorf("%w: %s", ErrBadDelegationBlock, lastReason)
}

// parseStreamEntry 以与 parseCandidateBody 相同的清洗与校验逻辑解析单个顶层委派对象。
func parseStreamEntry(text string) (Delegation, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return Delegation{}, false
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		// 与 parseCandidateBody 一致：非法反斜杠转义修复后再试一次。
		raw = nil
		if err := json.Unmarshal([]byte(repairInvalidEscapes(trimmed)), &raw); err != nil {
			return Delegation{}, false
		}
	}
	return parseEntry(raw)
}

// StreamParser 增量解析回复文本中的编排派发块。
// 它缓冲已接收的全部文本，定位 ParseDelegations 认可的开启围栏后逐字节跟踪大括号深度
// （尊重 JSON 字符串字面量与反斜杠转义，字符串内的括号不计入），
// 每个顶层对象闭合时立即按 parseEntry 的规则解码并产出，使子代理在整块写完前就能开工。
type StreamParser struct {
	buf         string // 已接收的全部文本
	scanned     int    // 下一个待扫描字节在 buf 中的位置
	opened      bool   // 是否已定位到开启围栏
	closed      bool   // 委派块是否已结束
	emitted     int    // 已产出的条目总数
	shapeKnown  bool   // 是否已确定块体顶层形状（数组或裸对象）
	isArray     bool   // 块体是否为顶层数组
	depth       int    // 当前大括号深度
	inString    bool   // 是否位于 JSON 字符串字面量内
	escaped     bool   // 字符串内上一字节是否为反斜杠转义
	atLineStart bool   // 当前字节是否位于行首
	entryStart  int    // 当前顶层对象起始字节在 buf 中的位置
}

// NewStreamParser 创建增量委派块解析器。
func NewStreamParser() *StreamParser {
	return &StreamParser{atLineStart: true}
}

// Feed 追加一段文本，返回本次调用中完成解析的条目，按块内顺序排列。
// 无法解码的对象直接跳过；同一对象不会跨多次调用重复产出。
func (p *StreamParser) Feed(chunk string) []Delegation {
	if p.closed || chunk == "" {
		return nil
	}
	p.buf += chunk

	var out []Delegation
	if !p.opened {
		loc := openerRegex.FindStringIndex(p.buf)
		if loc == nil {
			return nil
		}
		p.opened = true
		p.scanned = loc[1]
		p.atLineStart = true
	}

	for p.scanned < len(p.buf) {
		b := p.buf[p.scanned]

		// 块体开头的空白先跳过，直到确定顶层形状；首个非空白字节不是 '[' 时按裸对象处理。
		if !p.shapeKnown {
			if b == ' ' || b == '\t' || b == '\r' || b == '\n' {
				p.atLineStart = b == '\n'
				p.scanned++
				continue
			}
			p.shapeKnown = true
			p.isArray = b == '['
			if p.isArray {
				p.atLineStart = false
				p.scanned++
				continue
			}
		}

		// 闭合围栏：行首的三连反引号，且不在字符串或任何对象内部。
		if !p.inString && p.depth == 0 && p.atLineStart && b == '`' {
			rest := p.buf[p.scanned:]
			if strings.HasPrefix(rest, "```") {
				p.closed = true
				return out
			}
			if len(rest) < 3 && strings.TrimLeft(rest, "`") == "" {
				// 围栏标记可能被分片截断，等待后续输入
				return out
			}
		}

		if p.inString {
			switch {
			case p.escaped:
				p.escaped = false
			case b == '\\':
				p.escaped = true
			case b == '"':
				p.inString = false
			}
			p.atLineStart = b == '\n'
			p.scanned++
			continue
		}

		switch b {
		case '"':
			p.inString = true
		case '{':
			if p.depth == 0 {
				p.entryStart = p.scanned
			}
			p.depth++
		case '}':
			p.depth--
			if p.depth == 0 {
				if d, ok := parseStreamEntry(p.buf[p.entryStart : p.scanned+1]); ok {
					out = append(out, d)
					p.emitted++
				}
				if !p.isArray {
					// 裸顶层对象收尾即整块结束
					p.closed = true
					return out
				}
			} else if p.depth < 0 {
				// 多余的右括号：块体已破损，停止解析
				p.closed = true
				return out
			}
		case ']':
			if p.depth == 0 {
				p.closed = true
				return out
			}
		}
		p.atLineStart = b == '\n'
		p.scanned++
	}
	return out
}

// Emitted 返回累计已产出的条目数。
func (p *StreamParser) Emitted() int {
	return p.emitted
}

// Closed 报告委派块是否已经结束：闭合围栏、数组收尾、裸对象收尾或结构中断都会置为 true。
// 结束后 Feed 忽略后续文本，包括第二个委派块。
func (p *StreamParser) Closed() bool {
	return p.closed
}

// beeWorkRules 是随委派提示词下发的子代理工作方式，约束其只做交代的改动，只跑有范围的自测。
const beeWorkRules = "- 立即开始，只读完成任务所需的内容，然后动手修改。\n" +
	"- brief 已经点明文件、位置和改法的，直接照做，不再反复读。\n" +
	"- 如果长时间在几种设计之间犹豫，就按 brief 最简单的理解去做，并在汇报里说明这个选择。\n" +
	"- 只做这项任务，不扩大范围，不碰 brief 没提到的文件（确实无法避免的除外），不回退、不重排、不清理别人的改动。\n" +
	"- 可以做有范围的自测：构建、vet 或 lint，以及只针对改动所在包或模块的测试（Go 项目用 go build ./...、go vet、go test ./<包>/ -run <测试名> -timeout 120s），每条命令都要设超时。不跑全量测试套件，不启动 mcode 本体，不访问网络；不为了让测试通过去改任务范围外的测试。全量检查由主代理在自己的检出中执行。\n" +
	"- 不执行 git commit、push、branch、stash、reset、checkout。\n" +
	"- 严禁调用提问工具（如 ask_user_question、ask_user、question 等）向用户或主代理询问确认；需求存在歧义或有多种实现可能时，直接按最合理直接的方案继续分析或改动，并在汇报里列出你的设计选择与未尽事项。\n" +
	"- 只读任务必须使用只读工具（如 read、find、grep、serena 等）查阅代码，严禁执行修改文件或任何未经允许的写命令；只读任务不跑构建与测试。\n" +
	"- 遇到阻塞就停下并说明，不要猜测或绕过。"

// beeReportRules 是随委派提示词下发的汇报规范：子代理的最后一条回复会原样展示给用户。
const beeReportRules = "- 汇报只写一次，几行即可，写成主代理能直接照着处理的内容：改了什么、在哪个文件、跑了哪些自测命令及结果（没跑就写未跑），以及仍未解决或不确定的地方。\n" +
	"- 明确列出注意事项和未完成的部分，不要省略。\n" +
	"- 不要开场白，不要复述任务，不贴日志，不贴大段代码。\n" +
	"- 引用代码一律写成 `相对工作副本根目录的完整路径:行号`，例如 internal/ui/sidebar.go:865；禁止只写文件名，禁止绝对路径。\n" +
	"- 每条引用写在自己的一行或一个句段里，紧跟该行源码原文并用反引号包住，原样复制，不改写、不加省略号；引用路径前后不要紧贴中文括号或全角标点。\n" +
	"- 只写单个行号，不要写 559-561 这样的范围。\n" +
	"- 只读或调研类任务，用要点列出结论和对应的引用，不要整段转述源码。"

// beePrompt 为子会话生成包含角色前言与委派提示词的完整 Prompt。
func beePrompt(name string, d Delegation) string {
	return fmt.Sprintf("%s%s，由主代理派发，在独立工作副本中完成以下任务。\n\n## 任务：%s\n\n%s\n\n## 工作方式\n%s\n\n## 汇报\n%s", BeePromptPrefix, name, d.Task, d.Prompt, beeWorkRules, beeReportRules)
}

const (
	// beeRecallHeader 是附加到 bee 提示词末尾的相关记忆段落标题。
	beeRecallHeader = "## 相关记忆（来自 Nowledge Mem，仅供参考，以当前代码为准）"
	// beeRecallQueryRunes 是拼进检索 query 的提示词前缀 rune 上限。
	beeRecallQueryRunes = 300
	// beeRecallTimeout 是派发前检索记忆的总时长；超时即当作没有相关记忆。
	beeRecallTimeout = 3 * time.Second
)

// beeRecallQuery 用任务标题加上提示词前缀组成检索 query，控制单次检索的输入长度。
func beeRecallQuery(d Delegation) string {
	runes := []rune(strings.TrimSpace(d.Prompt))
	if len(runes) > beeRecallQueryRunes {
		runes = runes[:beeRecallQueryRunes]
	}
	return strings.TrimSpace(strings.TrimSpace(d.Task) + "\n" + string(runes))
}

// beeRecallBlock 在超时预算内调用记忆检索钩子，返回带标题的提示词段落；失败或空结果返回空串。
func beeRecallBlock(ctx context.Context, recall func(ctx context.Context, workDir, query string) string, workDir string, d Delegation) string {
	recallCtx, cancel := context.WithTimeout(ctx, beeRecallTimeout)
	defer cancel()

	var block string
	done := make(chan struct{})
	go func() {
		defer close(done)
		block = recall(recallCtx, workDir, beeRecallQuery(d))
	}()
	select {
	case <-done:
	case <-recallCtx.Done():
		// 钩子不保证尊重 ctx：超时后直接放弃本次检索，避免拖住派发。
		return ""
	}
	block = strings.TrimSpace(block)
	if block == "" {
		return ""
	}
	return beeRecallHeader + "\n\n" + block
}
