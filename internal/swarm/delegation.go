package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Roster 定义编排子代理的标准名字轮转表（共 25 位）。
var Roster = []string{
	"Aria", "Bram", "Cora", "Dean", "Eva",
	"Finn", "Gwen", "Hugo", "Iris", "Jude",
	"Kai", "Leo", "Milo", "Nora", "Owen",
	"Pia", "Quinn", "Remy", "Sora", "Theo",
	"Uma", "Vera", "Wynn", "Yuki", "Zoe",
}

// RosterName 根据索引返回协作者名字；超过 25 个后追加轮次后缀（如 "Aria 2"）。
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
	Skills     []string
	Acceptance []string
	// Files 是可选的仓库相对文件路径列表，用于并发派发时的写冲突排队（不同于档位的写入白名单）。
	Files []string
	// Replaces 是要取代的在跑 bee 的名字，宿主会先停掉该 bee 再启动本条。
	Replaces string
	// ReworkOf 是要返工的已驳回 bee 的名字或 ThreadID；为空时按 brief 里的补丁路径推断。
	ReworkOf string
	// Rerun 为 true 时与 ReworkOf/Acceptance 配合，在目标 bee 保留的副本上重跑验收命令，不启动新 bee。
	Rerun bool
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
	rerun, _ := m["rerun"].(bool)

	return Delegation{
		Task:       task,
		Prompt:     prompt,
		Name:       name,
		Project:    project,
		Provider:   provider,
		Model:      model,
		Profile:    profile,
		Pair:       pair,
		Tier:       tier,
		Skills:     parseSkills(m["skills"]),
		Acceptance: parseAcceptance(m["acceptance"]),
		Files:      parseFiles(m["files"]),
		Replaces:   replaces,
		ReworkOf:   reworkOf,
		Rerun:      rerun,
	}, true
}

// parseAcceptance 保留 shell 命令原文（逗号不分割），只清理首尾空白。
func parseAcceptance(raw any) []string {
	var items []string
	switch v := raw.(type) {
	case string:
		items = []string{v}
	case []string:
		items = v
	case []any:
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				continue
			}
			items = append(items, s)
		}
	default:
		return nil
	}
	var commands []string
	for _, item := range items {
		if command := strings.TrimSpace(item); command != "" {
			commands = append(commands, command)
			if len(commands) == 8 {
				break
			}
		}
	}
	return commands
}

var scopeLineSuffixRegex = regexp.MustCompile(`:\d+(?:-\d+)?$`)

// normalizeScopePath 将用户/模型传入的路径归一为仓库相对文件路径；
// 拒绝空串、绝对路径、URL、目录路径、以及 `.` / `..` / 越界路径，并剥离末尾行号后缀。
func normalizeScopePath(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.Trim(s, "`'\"")
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\\", "/")
	if s == "" || strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~") || strings.Contains(s, "://") || strings.HasSuffix(s, "/") {
		return ""
	}
	s = scopeLineSuffixRegex.ReplaceAllString(s, "")
	if s == "" || strings.HasSuffix(s, "/") {
		return ""
	}
	for strings.HasPrefix(s, "./") {
		s = strings.TrimPrefix(s, "./")
	}
	if s == "" {
		return ""
	}
	s = path.Clean(s)
	if s == "" || s == "." || s == ".." || strings.HasPrefix(s, "../") || strings.HasPrefix(s, "/") {
		return ""
	}
	return s
}

func parseFiles(raw any) []string {
	var items []string
	switch v := raw.(type) {
	case string:
		items = []string{v}
	case []string:
		items = v
	case []any:
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				continue
			}
			items = append(items, s)
		}
	default:
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	var files []string
	for _, item := range items {
		p := normalizeScopePath(item)
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		files = append(files, p)
	}
	return files
}

const (
	acceptanceErrGrepQuiet = "不要用 grep -q，它会吞掉失败输出"
	acceptanceErrTimeout   = "macOS 没有 timeout 命令，用工具自己的超时参数（如 go test -timeout）"
	acceptanceErrGoTestRun = "go test 要用 -run 限定到本次改动的测试；整包检查由项目检查和主代理最后的完整检查负责"
)

// AcceptanceRejection 检查委派条目的验收命令是否合规；有问题时返回给主代理的拒绝提示，没问题返回空串。
func AcceptanceRejection(d Delegation) string {
	for _, cmd := range d.Acceptance {
		if reason := acceptanceProblem(cmd); reason != "" {
			return fmt.Sprintf("条目「%s」未启动：验收命令 `%s` %s，改正后重新派发", acceptanceEntryLabel(d), cmd, reason)
		}
	}
	return ""
}

func acceptanceEntryLabel(d Delegation) string {
	label := strings.TrimSpace(d.Task)
	if label == "" {
		return singleLinePrompt(d.Prompt, 30)
	}
	return singleLinePrompt(label, 30)
}

// acceptanceProblem 派发前检查单条验收命令：只查 grep -q/--quiet/--silent、命令段以 timeout/gtimeout 开头、
// 以及 go test 缺少 -run 三类问题，没问题返回空串。
func acceptanceProblem(command string) string {
	return inspectAcceptanceCommand(command, 0)
}

func inspectAcceptanceCommand(command string, depth int) string {
	if depth > 3 {
		return ""
	}
	for _, seg := range splitShellSegments(command) {
		trimmed := strings.TrimSpace(seg)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "timeout ") || strings.HasPrefix(trimmed, "gtimeout ") || trimmed == "timeout" || trimmed == "gtimeout" {
			return acceptanceErrTimeout
		}
		words := splitShellWords(trimmed)
		if len(words) == 0 {
			continue
		}
		cmdIdx := shellCommandIndex(words)
		if cmdIdx >= len(words) {
			continue
		}
		base := path.Base(words[cmdIdx])
		if base == "timeout" || base == "gtimeout" {
			return acceptanceErrTimeout
		}
		if base == "grep" || base == "egrep" || base == "fgrep" {
			if grepHasQuietFlag(words[cmdIdx+1:]) {
				return acceptanceErrGrepQuiet
			}
		}
		if base == "go" && cmdIdx+1 < len(words) && words[cmdIdx+1] == "test" {
			if !goTestHasRunFlag(words[cmdIdx+2:]) {
				return acceptanceErrGoTestRun
			}
		}
		if base == "sh" || base == "bash" {
			if script, ok := shellDashCScript(words[cmdIdx+1:]); ok {
				if reason := inspectAcceptanceCommand(script, depth+1); reason != "" {
					return reason
				}
			}
		}
	}
	return ""
}

// commandUsesGoTest 判断验收命令是否在任意命令段中调用了 go test。
func commandUsesGoTest(command string) bool {
	return inspectCommandUsesGoTest(command, 0)
}

func inspectCommandUsesGoTest(command string, depth int) bool {
	if depth > 3 {
		return false
	}
	for _, seg := range splitShellSegments(command) {
		words := splitShellWords(seg)
		if len(words) == 0 {
			continue
		}
		cmdIdx := shellCommandIndex(words)
		if cmdIdx >= len(words) {
			continue
		}
		base := path.Base(words[cmdIdx])
		if base == "go" && cmdIdx+1 < len(words) && words[cmdIdx+1] == "test" {
			return true
		}
		if base == "sh" || base == "bash" {
			if script, ok := shellDashCScript(words[cmdIdx+1:]); ok {
				if inspectCommandUsesGoTest(script, depth+1) {
					return true
				}
			}
		}
	}
	return false
}

func shellCommandIndex(words []string) int {
	i := 0
	for i < len(words) {
		w := words[i]
		if w == "!" || w == "{" || w == "(" || isShellEnvAssign(w) {
			i++
			continue
		}
		break
	}
	return i
}

func isShellEnvAssign(word string) bool {
	eq := strings.IndexByte(word, '=')
	if eq <= 0 {
		return false
	}
	for i := 0; i < eq; i++ {
		c := word[i]
		isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
		isDigit := c >= '0' && c <= '9'
		if !isLetter && !(i > 0 && isDigit) {
			return false
		}
	}
	return true
}

func grepHasQuietFlag(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return false
		}
		if arg == "--quiet" || arg == "--silent" {
			return true
		}
		if arg == "-e" || arg == "-f" || arg == "-m" || arg == "-A" || arg == "-B" || arg == "-C" || arg == "--regexp" || arg == "--file" {
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && len(arg) > 1 {
			if strings.ContainsRune(arg[1:], 'q') {
				return true
			}
		}
	}
	return false
}

func goTestHasRunFlag(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return false
		}
		if arg == "-run" || arg == "--run" {
			return i+1 < len(args) && strings.TrimSpace(args[i+1]) != ""
		}
		if strings.HasPrefix(arg, "-run=") || strings.HasPrefix(arg, "--run=") {
			_, val, _ := strings.Cut(arg, "=")
			return strings.TrimSpace(val) != ""
		}
	}
	return false
}

func shellDashCScript(args []string) (string, bool) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return "", false
		}
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.ContainsRune(arg[1:], 'c') {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", false
		}
	}
	return "", false
}

// splitShellSegments 按未引用的 &&、||、;、| 与换行切分命令段。
func splitShellSegments(command string) []string {
	var segments []string
	var cur strings.Builder
	inSingle := false
	inDouble := false
	escaped := false

	for i := 0; i < len(command); i++ {
		c := command[i]
		if escaped {
			cur.WriteByte(c)
			escaped = false
			continue
		}
		if inSingle {
			cur.WriteByte(c)
			if c == '\'' {
				inSingle = false
			}
			continue
		}
		if inDouble {
			cur.WriteByte(c)
			if c == '\\' {
				escaped = true
			} else if c == '"' {
				inDouble = false
			}
			continue
		}
		switch c {
		case '\\':
			cur.WriteByte(c)
			escaped = true
		case '\'':
			cur.WriteByte(c)
			inSingle = true
		case '"':
			cur.WriteByte(c)
			inDouble = true
		case ';', '\n', '\r':
			segments = append(segments, cur.String())
			cur.Reset()
		case '&':
			if i+1 < len(command) && command[i+1] == '&' {
				segments = append(segments, cur.String())
				cur.Reset()
				i++
			} else {
				cur.WriteByte(c)
			}
		case '|':
			segments = append(segments, cur.String())
			cur.Reset()
			if i+1 < len(command) && command[i+1] == '|' {
				i++
			}
		default:
			cur.WriteByte(c)
		}
	}
	segments = append(segments, cur.String())
	return segments
}

// splitShellWords 将单个命令段按空白与括号拆词，同时剥离外部引号并保留引号内字面内容。
func splitShellWords(segment string) []string {
	var words []string
	var cur strings.Builder
	inToken := false
	inSingle := false
	inDouble := false
	escaped := false

	flush := func() {
		if inToken {
			words = append(words, cur.String())
			cur.Reset()
			inToken = false
		}
	}

	for i := 0; i < len(segment); i++ {
		c := segment[i]
		if escaped {
			cur.WriteByte(c)
			escaped = false
			inToken = true
			continue
		}
		if inSingle {
			if c == '\'' {
				inSingle = false
			} else {
				cur.WriteByte(c)
			}
			inToken = true
			continue
		}
		if inDouble {
			if c == '\\' && i+1 < len(segment) && (segment[i+1] == '"' || segment[i+1] == '\\' || segment[i+1] == '$' || segment[i+1] == '`') {
				escaped = true
			} else if c == '"' {
				inDouble = false
			} else {
				cur.WriteByte(c)
			}
			inToken = true
			continue
		}
		if unicode.IsSpace(rune(c)) {
			flush()
			continue
		}
		switch c {
		case '\\':
			escaped = true
			inToken = true
		case '\'':
			inSingle = true
			inToken = true
		case '"':
			inDouble = true
			inToken = true
		case '(', ')', '{', '}':
			flush()
		default:
			cur.WriteByte(c)
			inToken = true
		}
	}
	flush()
	return words
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

// beeWorkRulesFor 返回随委派提示词下发的工作方式：先是该档位类型专属的几条，再是三类共通的几条。
// 类型判断：ReadOnly 为只读类；否则有 WritePaths 为方案类；其余为改代码类。
func beeWorkRulesFor(cfg BeeConfig, hasAcceptance bool) string {
	var rules []string
	switch {
	case cfg.ReadOnly:
		rules = []string{
			"- 立即开始，只读回答问题所需的内容。",
			"- 只用只读工具（如 read、find、grep、serena 等）、只读命令和有范围的 go build、go test、go vet 查阅与复现，不修改任何文件。",
		}
	case len(cfg.WritePaths) > 0:
		rules = []string{
			"- 立即开始，先读代码定位问题，再设计改法；不改代码。",
			"- 只能写入这些路径：" + strings.Join(cfg.WritePaths, "、") + "；其他路径的改动会被丢弃。",
			"- 需要复现时可以跑有范围的测试：只针对相关包或模块，每条命令都要设超时，不依赖网络，不跑全量测试套件，不启动 mcode 本体。",
		}
	default:
		selfCheckRule := "- 可以做有范围的自测：构建、vet 或 lint，以及只针对改动所在包或模块的测试（Go 项目用 go build ./...、go vet、go test ./<包>/ -run <测试名> -timeout 120s），每条命令都要设超时。自测不依赖网络，不跑全量测试套件，不启动 mcode 本体；不为了让测试通过去改任务范围外的测试。全量检查由主代理在自己的检出中执行。"
		if hasAcceptance {
			selfCheckRule = "- 汇报前不跑测试，测试由验收命令负责。可以跑构建和 vet（Go 项目用 go build ./...、go vet ./<包>/，vet 会编译测试文件），每条命令都要设超时，不跑全量测试套件，不启动 mcode 本体。验收失败、输出发回给你之后，再跑相关测试定位。"
		}
		rules = []string{
			"- 立即开始，只读完成任务所需的内容，然后动手修改。",
			"- 任务说明已经点明文件、位置和改法的，直接照做，不再反复读。",
			"- 不碰任务说明没提到的文件（确实无法避免的除外），不回退、不重排、不清理别人的改动。",
			selfCheckRule,
		}
	}
	rules = append(rules,
		"- 只做这项任务，不扩大范围。",
		"- 查文档、核对外部库或网页信息时，可以用 web_fetch、web_search 等联网工具或 curl（必须带 -m 超时）；只读不写，不向外部服务提交、发布或上传内容，不发送密钥或用户信息。",
		"- 不执行 git commit、push、branch、stash、reset、checkout。",
	)
	if cfg.Interactive && !cfg.ReadOnly {
		rules = append(rules, "- 需要用户拍板的细节，用提问工具给 2 到 4 个选项让用户选，推荐项放第一个；能从任务说明或代码里确定的不问。")
	} else {
		rules = append(rules,
			"- 严禁调用提问工具（如 ask_user_question、ask_user、question 等）向用户或主代理询问确认。",
			"- 任务说明有歧义或有多种做法时，按最直接的理解继续，并在汇报里写明你的选择。",
		)
	}
	rules = append(rules,
		"- 遇到阻塞（缺文件、命令无法运行、任务说明与代码事实对不上）就停下并在汇报里说明，不要猜测或绕过。",
	)
	if strings.TrimSpace(cfg.SystemPrompt) != "" {
		rules = append(rules, "- 档位要求与以上规则冲突时，以档位要求为准。")
	}
	return strings.Join(rules, "\n")
}

// beeReportRulesFor 返回随委派提示词下发的汇报规范：第一条按档位类型，其余共通。
// 子代理的最后一条回复会原样展示给用户。
func beeReportRulesFor(cfg BeeConfig, hasAcceptance bool) string {
	const header = "- 汇报只写一次，以一行 `## 结论` 标题开头，结论不超过 5 行，第一行用一句话写结果（不超过 40 字）；之后另起一行 `## 详情` 标题，"
	var first string
	switch {
	case cfg.ReadOnly:
		// 1000 字的阈值是为了让结论与详情关键内容留在 ReportSummaryLimit（1500）以内。
		first = header + "用不超过 1000 字按任务说明的问题编号写出结论展开以及阻塞与环境差异（如副本缺文件、命令无法运行），再逐条给出对应的引用，不要整段转述源码。"
	case len(cfg.WritePaths) > 0:
		// 1000 字的阈值是为了让结论与关键内容留在 ReportSummaryLimit（1500）以内。
		first = header + "先写根因，再写编号步骤（每步给出位置、改法和验收标准）。汇报正文超过约 1000 字时，把完整方案写进允许写入的路径，详情里只留根因、各步标题和该文件路径。"
	default:
		first = header + "几行即可，写成主代理能直接照着处理的内容：改了什么、在哪个文件、跑了哪些自测命令及结果（没跑就写未跑），以及未决事项。"
		if hasAcceptance {
			first = header + "几行即可，写成主代理能直接照着处理的内容：改了什么、在哪个文件、跑了哪些构建或 vet 命令及结果，以及未决事项；测试结果由 mcode 验收给出，不用写。"
		}
	}
	return strings.Join([]string{
		first,
		"- 明确列出注意事项和未完成的部分，不要省略。",
		"- 不要开场白，不要复述任务，不贴日志，不贴大段代码。",
		"- 引用代码一律写成 `相对工作副本根目录的完整路径:行号`，例如 internal/ui/sidebar.go:865；禁止只写文件名，禁止绝对路径。",
		"- 每条引用写在自己的一行或一个句段里，紧跟该行源码原文并用反引号包住，原样复制，不改写、不加省略号；引用路径前后不要紧贴中文括号或全角标点。",
		"- 只写单个行号，不要写 559-561 这样的范围。",
	}, "\n")
}

// beePrompt 为子会话生成包含角色前言、任务、档位要求与工作规则的完整 Prompt。
// 首行始终以 BeePromptPrefix 开头，IsBeePrompt 才能识别。
func beePrompt(name string, d Delegation, cfg BeeConfig) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s，由主代理派发，完成以下任务。\n\n## 任务：%s\n\n%s", BeePromptPrefix, name, d.Task, d.Prompt)
	if sys := strings.TrimSpace(cfg.SystemPrompt); sys != "" {
		b.WriteString("\n\n## 档位要求\n" + sys)
	}
	hasAcceptance := len(d.Acceptance) > 0
	if hasAcceptance {
		b.WriteString("\n\n## 验收\nmcode 在你汇报后于工作副本根目录逐条运行以下命令，全部退出码为 0 才合并；汇报前不要自己跑这些命令或同一批测试，失败时 mcode 会把输出发回给你修。命令由 sh 执行（Windows 上为 Git Bash）：")
		for _, command := range d.Acceptance {
			fmt.Fprintf(&b, "\n- `%s`", command)
		}
	}
	b.WriteString("\n\n## 工作方式\n" + beeWorkRulesFor(cfg, hasAcceptance))
	b.WriteString("\n\n## 汇报\n" + beeReportRulesFor(cfg, hasAcceptance))
	return b.String()
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
