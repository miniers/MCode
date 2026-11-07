package mcodeui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/richtext"
	"mcode/internal/agentconf"
)

type completionKind int

const (
	completionNone completionKind = iota
	completionSlash
	completionMention
	// completionSkill 是 '$' 触发的技能补全；追加在末尾，保持既有取值的字面量不变。
	completionSkill
)

// completionItem 描述单个候选补全项。
type completionItem struct {
	Label    string // 菜单显示名称（斜杠带 "/"，文件为 Path，目录带 "/"）
	Detail   string // 次要说明文字
	Insert   string // 插入草稿的文本（斜杠 "/"+Name，提及 "@"+Path）
	Hint     string // 高亮时显示在该行右侧的参数提示或说明
	ThreadID string // 会话提及项对应的会话 ID；非空表示这是 @ 会话引用
	Icon     string // 可选的候选类型图标，目前仅 "thread"（会话气泡）
	Source   string // 技能来源（project/global/external），用于右侧来源胶囊；非技能项为空
	// Category 表示这是一行分类（文件/会话/项目/全局/外部）：确认后只把分类前缀写进草稿并保持面板打开。
	Category bool
}

// ThreadMention 是一条被引用的会话：胶囊展示 Title，发送时按 ThreadID 展开上下文块。
type ThreadMention struct {
	ThreadID string
	Title    string
}

// slashCommandAliases 斜杠命令别名表，key 为命令名（不带 '/'），value 为别名（同样不带 '/'）。
// 别名只参与过滤与菜单展示，插入草稿时始终使用命令本名。
var slashCommandAliases = map[string][]string{
	"model":   {"m"},
	"effort":  {"think", "reasoning"},
	"fast":    {"speed"},
	"mode":    {"build"},
	"plan":    {"arc"},
	"export":  {"save", "dump"},
	"clear":   {"new", "reset"},
	"compact": {"summarize"},
	"pair":    {"swarm", "hydra"},
}

// slashCommandArgHints 斜杠命令的参数示例或说明，key 为命令名（不带 '/'）。
var slashCommandArgHints = map[string]string{
	"model":   "打开模型选择",
	"effort":  "low | medium | high",
	"export":  "导出为 Markdown/JSON",
	"copy":    "复制会话 Markdown",
	"plan":    "<任务描述>",
	"compact": "[保留重点]",
}

// detectCompletion 检测草稿末尾的补全触发符及参数。
// 斜杠补全：草稿必须以 '/' 开头且整个草稿不含空白，query 为 '/' 之后的文本；
// 提及补全：草稿最后一个空白分隔的 token 以 '@' 开头（'@' 位于开头或前一字符为空白），query 为 '@' 之后的文本。
func detectCompletion(draft string) (kind completionKind, start int, query string) {
	if draft == "" {
		return completionNone, 0, ""
	}

	// 斜杠补全：必须以 '/' 开头且不含任何空白字符；排除 Markdown 代码块围栏与注释开头（```、//、/*），否则粘贴代码时会误弹命令列表
	if strings.HasPrefix(draft, "/") && strings.IndexFunc(draft, unicode.IsSpace) < 0 &&
		!strings.HasPrefix(draft, "//") && !strings.HasPrefix(draft, "/*") {
		return completionSlash, 0, draft[1:]
	}

	// 提及补全：看最后一个空白分隔的 token
	lastRune, _ := utf8.DecodeLastRuneInString(draft)
	if unicode.IsSpace(lastRune) {
		return completionNone, 0, ""
	}

	lastSpace := strings.LastIndexFunc(draft, unicode.IsSpace)
	var token string
	if lastSpace == -1 {
		start = 0
		token = draft
	} else {
		_, size := utf8.DecodeRuneInString(draft[lastSpace:])
		start = lastSpace + size
		token = draft[start:]
	}

	if strings.HasPrefix(token, "@") {
		return completionMention, start, token[1:]
	}

	// 技能补全：与 @ 同规则，token 以 '$' 开头
	if strings.HasPrefix(token, "$") {
		return completionSkill, start, token[1:]
	}

	return completionNone, 0, ""
}

// localSlashCommands 是界面自己执行、后端命令目录里没有的命令，补全候选要把它们并进去，
// 否则用户只能靠记忆手打（/split 只在总管会话里真正生效，详见 runLocalSlashCommand）。
var localSlashCommands = []SlashCommand{
	{Name: "split", Detail: "分割会话（仅总管）", IsBuiltIn: true},
}

// stewardOnlyLocalSlashCommands 记录只在总管会话里列出的本地命令：普通会话里选了也只会弹
// 「分割目前只对总管会话开放」，不如干脆不给这个候选。
var stewardOnlyLocalSlashCommands = map[string]bool{"split": true}

// withLocalSlashCommands 把本地命令并进后端返回的目录，同名的以后端为准，避免重复候选。
// isSteward 表示当前会话是不是总管会话，为 false 时跳过 stewardOnlyLocalSlashCommands 里的命令。
func withLocalSlashCommands(cmds []SlashCommand, isSteward bool) []SlashCommand {
	if len(localSlashCommands) == 0 {
		return cmds
	}
	out := make([]SlashCommand, 0, len(cmds)+len(localSlashCommands))
	out = append(out, cmds...)
	for _, local := range localSlashCommands {
		if stewardOnlyLocalSlashCommands[local.Name] && !isSteward {
			continue
		}
		seen := false
		for _, cmd := range cmds {
			if cmd.Name == local.Name {
				seen = true
				break
			}
		}
		if !seen {
			out = append(out, local)
		}
	}
	return out
}

// filterCommands 根据 query 对斜杠指令做大小写不敏感过滤，并按匹配优先级排序：
// 名称前缀 > 别名前缀 > 名称包含 > 别名包含 > Detail 包含 > 名称子序列（query 字符按顺序出现在名称里），
// 同分保持传入顺序，最多返回 limit 条；query 为空时保持原顺序截断。
func filterCommands(cmds []SlashCommand, query string, limit int) []SlashCommand {
	if limit <= 0 {
		limit = 8
	}
	q := strings.ToLower(query)
	if q == "" {
		if len(cmds) > limit {
			return append([]SlashCommand(nil), cmds[:limit]...)
		}
		return append([]SlashCommand(nil), cmds...)
	}

	type scored struct {
		cmd   SlashCommand
		score int
	}
	matched := make([]scored, 0, len(cmds))
	for _, cmd := range cmds {
		if score, ok := slashCommandMatchScore(cmd, q); ok {
			matched = append(matched, scored{cmd: cmd, score: score})
		}
	}
	sort.SliceStable(matched, func(i, j int) bool { return matched[i].score < matched[j].score })

	res := make([]SlashCommand, 0, len(matched))
	for _, m := range matched {
		if len(res) == limit {
			break
		}
		res = append(res, m.cmd)
	}
	return res
}

// slashCommandMatchScore 返回命令与 query（已转小写）的匹配档位，档位越小越靠前；不匹配时返回 false。
func slashCommandMatchScore(cmd SlashCommand, q string) (int, bool) {
	name := strings.ToLower(cmd.Name)
	switch {
	case strings.HasPrefix(name, q):
		return 0, true
	case slashCommandAliasPrefixMatch(cmd.Name, q):
		return 1, true
	case strings.Contains(name, q):
		return 2, true
	case slashCommandAliasContainsMatch(cmd.Name, q):
		return 3, true
	case strings.Contains(strings.ToLower(cmd.Detail), q):
		return 4, true
	case isSubsequence(q, name):
		return 5, true
	}
	return 0, false
}

// slashCommandAliasPrefixMatch 判断命令是否存在以 query（已转小写）开头的别名。
func slashCommandAliasPrefixMatch(name, q string) bool {
	for _, alias := range slashCommandAliases[name] {
		if strings.HasPrefix(strings.ToLower(alias), q) {
			return true
		}
	}
	return false
}

// slashCommandAliasContainsMatch 判断命令是否存在包含 query（已转小写）的别名。
func slashCommandAliasContainsMatch(name, q string) bool {
	for _, alias := range slashCommandAliases[name] {
		if strings.Contains(strings.ToLower(alias), q) {
			return true
		}
	}
	return false
}

// matchingAlias 返回命令中与 query（已转小写）匹配的别名，前缀匹配优先；无匹配时返回空字符串。
func matchingAlias(name, q string) string {
	for _, alias := range slashCommandAliases[name] {
		if strings.HasPrefix(strings.ToLower(alias), q) {
			return alias
		}
	}
	for _, alias := range slashCommandAliases[name] {
		if strings.Contains(strings.ToLower(alias), q) {
			return alias
		}
	}
	return ""
}

// isSubsequence 判断 q 的字符是否按顺序出现在 s 中（不要求连续），两者均按字节比较。
func isSubsequence(q, s string) bool {
	if q == "" {
		return true
	}
	qi := 0
	for i := 0; i < len(s) && qi < len(q); i++ {
		if s[i] == q[qi] {
			qi++
		}
	}
	return qi == len(q)
}

// slashCommandsToItems 把命令转换为补全项；query 用于识别靠别名命中的命令，
// 这类命令的 Detail 末尾会追加「（别名 /xx）」；Hint 取自参数提示表。
func slashCommandsToItems(cmds []SlashCommand, query string) []completionItem {
	q := strings.ToLower(query)
	items := make([]completionItem, len(cmds))
	for i, cmd := range cmds {
		detail := cmd.Detail
		if q != "" {
			name := strings.ToLower(cmd.Name)
			if !strings.HasPrefix(name, q) && !strings.Contains(name, q) {
				if alias := matchingAlias(cmd.Name, q); alias != "" {
					detail += "（别名 /" + alias + "）"
				}
			}
		}
		items[i] = completionItem{
			Label:  "/" + cmd.Name,
			Detail: detail,
			Insert: "/" + cmd.Name,
			Hint:   slashCommandArgHints[cmd.Name],
		}
	}
	return items
}

func fileMatchesToItems(matches []FileMatch) []completionItem {
	items := make([]completionItem, len(matches))
	for i, m := range matches {
		label := m.Path
		insert := "@" + m.Path
		detail := ""
		if m.IsDir {
			if !strings.HasSuffix(label, "/") {
				label += "/"
			}
			if !strings.HasSuffix(insert, "/") {
				insert += "/"
			}
			detail = "目录"
		}
		items[i] = completionItem{
			Label:  label,
			Detail: detail,
			Insert: insert,
		}
	}
	return items
}

// completionScopeNames 是补全分类前缀，@ 与 $ 共用：草稿里的 "@file:" 表示进入「文件」这一层。
var completionScopeNames = []string{"file", "thread", "project", "global", "external"}

// completionScope 解析分类前缀，返回分类名（file/thread/project/global/external）与前缀之后的查询。
// 没有前缀时返回空分类与完整查询。
func completionScope(query string) (scope, rest string) {
	for _, name := range completionScopeNames {
		prefix := name + ":"
		if len(query) >= len(prefix) && strings.EqualFold(query[:len(prefix)], prefix) {
			return name, query[len(prefix):]
		}
	}
	return "", query
}

// completionCategoryItems 返回某类补全的分类行：@ 下分「文件」「会话」，$ 下分「项目」「全局」「外部」。
// 分类行的 Insert 是「触发符 + 分类前缀」，确认后草稿里留下前缀，面板按前缀继续下钻。
func completionCategoryItems(kind completionKind) []completionItem {
	switch kind {
	case completionMention:
		return []completionItem{
			{Label: "▸ 文件", Detail: "@file:", Insert: "@file:", Category: true},
			{Label: "▸ 会话", Detail: "@thread:", Insert: "@thread:", Category: true},
		}
	case completionSkill:
		return []completionItem{
			{Label: "▸ 项目", Detail: "$project:", Insert: "$project:", Category: true},
			{Label: "▸ 全局", Detail: "$global:", Insert: "$global:", Category: true},
			{Label: "▸ 外部", Detail: "$external:", Insert: "$external:", Category: true},
		}
	}
	return nil
}

// mentionCompletionItems 组装 @ 补全候选：分类行（仅空查询）、会话引用与文件结果按分类前缀取舍。
func mentionCompletionItems(kind completionKind, scope string, categories bool, threadItems, fileItems []completionItem) []completionItem {
	items := make([]completionItem, 0, len(threadItems)+len(fileItems)+2)
	if categories {
		items = append(items, completionCategoryItems(kind)...)
	}
	if scope != "file" {
		items = append(items, threadItems...)
	}
	if scope != "thread" {
		items = append(items, fileItems...)
	}
	return items
}

// filterSkillsByScope 按分类前缀过滤技能；scope 为空时原样返回。
func filterSkillsByScope(skills []agentconf.Skill, scope string) []agentconf.Skill {
	if scope == "" {
		return skills
	}
	filtered := make([]agentconf.Skill, 0, len(skills))
	for _, sk := range skills {
		if strings.EqualFold(sk.Scope, scope) {
			filtered = append(filtered, sk)
		}
	}
	return filtered
}

// skillCompletionItems 把技能转换成 $ 补全项，按「名称前缀 > 名称包含 > 路径包含」排序，最多 20 条。
// 名字里的空格插入时替换为 '-'：detectCompletion 以空白切 token，含空格的 token 插入后会被当成两个 token。
func skillCompletionItems(skills []agentconf.Skill, query string) []completionItem {
	type scored struct {
		skill agentconf.Skill
		name  string
		score int
	}

	q := strings.ToLower(strings.TrimSpace(query))
	matched := make([]scored, 0, len(skills))
	seen := make(map[string]bool, len(skills))
	for _, sk := range skills {
		if sk.Disabled {
			continue
		}
		name := strings.TrimSpace(sk.ID())
		if name == "" || (sk.Name == "" && sk.Path == "") {
			continue
		}
		// 同名技能在不同 scope 各留一条（Detail 区分来源），完全相同的来源只留一条。
		key := sk.Agent + "\x00" + sk.Scope + "\x00" + sk.Plugin + "\x00" + name
		if seen[key] {
			continue
		}
		seen[key] = true
		score := 0
		if q != "" {
			var ok bool
			if score, ok = skillMatchScore(sk, name, q); !ok {
				continue
			}
		}
		matched = append(matched, scored{skill: sk, name: name, score: score})
	}
	sort.SliceStable(matched, func(i, j int) bool { return matched[i].score < matched[j].score })
	if len(matched) > 20 {
		matched = matched[:20]
	}

	items := make([]completionItem, len(matched))
	for i, m := range matched {
		items[i] = completionItem{
			Label:  "$" + m.name,
			Detail: m.skill.Agent + " · " + m.skill.Scope,
			Insert: "$" + strings.ReplaceAll(m.name, " ", "-"),
			Source: m.skill.Scope,
		}
	}
	return items
}

// skillMatchScore 返回技能与 query（已转小写）的匹配档位，档位越小越靠前；不匹配时返回 false。
func skillMatchScore(sk agentconf.Skill, name, q string) (int, bool) {
	lower := strings.ToLower(name)
	switch {
	case strings.HasPrefix(lower, q):
		return 0, true
	case strings.Contains(lower, q):
		return 1, true
	case strings.Contains(strings.ToLower(sk.Path), q):
		return 2, true
	}
	return 0, false
}

// completionKeepsPanelOpen 判断确认该项后是否保留面板：分类行总是下钻；
// 目录项只在 Tab 时「进目录」，Enter 仍是「插入并关闭」。
func completionKeepsPanelOpen(kind completionKind, item completionItem, viaTab bool) bool {
	if item.Category {
		return true
	}
	return viaTab && kind == completionMention && strings.HasSuffix(item.Insert, "/")
}

// completionThreadUnavailableLocked 判断会话引用项是否已失效：会话已从 s.threads 里消失时返回原因。
// 调用方需持有 s.mu；非会话项（ThreadID 为空）永远可用。
func completionThreadUnavailableLocked(s *chatState, item completionItem) string {
	if item.ThreadID == "" {
		return ""
	}
	for i := range s.threads {
		if s.threads[i].ID == item.ThreadID {
			return ""
		}
	}
	return "该会话已不存在"
}

// completionThreadUnavailable 是渲染路径用的版本：自行加锁。
func completionThreadUnavailable(s *chatState, item completionItem) string {
	if item.ThreadID == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return completionThreadUnavailableLocked(s, item)
}

// selectAdjacentCompletion 沿 step 方向（+1 向下、-1 向上）循环移动选中项，跳过失效的会话引用；
// 没有可用项时保持原值。调用方需持有 s.mu。
func (s *chatState) selectAdjacentCompletion(step int) {
	n := len(s.completionItems)
	if n == 0 {
		return
	}
	start := s.completionSelected
	if start < 0 || start >= n {
		start = 0
	}
	for i := 1; i <= n; i++ {
		idx := ((start+step*i)%n + n) % n
		if completionThreadUnavailableLocked(s, s.completionItems[idx]) == "" {
			s.completionSelected = idx
			return
		}
	}
}

// nextUsableCompletionIndex 从 from 的下一项开始循环查找可用项（reasons 中为空串），找不到返回 -1。
// 先向下找，绕回开头后继续找。
func nextUsableCompletionIndex(reasons []string, from int) int {
	n := len(reasons)
	if n == 0 {
		return -1
	}
	for i := 1; i <= n; i++ {
		idx := ((from+i)%n + n) % n
		if reasons[idx] == "" {
			return idx
		}
	}
	return -1
}

// updateCompletion 在每帧渲染输入框后，根据最新 draft 更新补全状态。
func (s *chatState) updateCompletion(threadID string) {
	s.mu.Lock()
	draft := s.draft
	dismissedFor := s.completionDismissedFor
	kind, start, query := detectCompletion(draft)
	if s.isComposing || s.completionSuspended {
		kind = completionNone
	}
	if kind == completionNone || (dismissedFor != "" && dismissedFor == draft[start:] && s.completionThreadID == threadID) {
		wasOpen := s.completionKind != completionNone
		s.completionKind = completionNone
		s.completionItems = nil
		s.completionLoading = false
		s.completionLoaded = false
		s.mentionSeq++
		if kind == completionNone && !s.isComposing {
			s.completionDismissedFor = ""
		}
		s.mu.Unlock()
		if wasOpen {
			s.invalidate()
		}
		return
	}

	// 分类前缀（file:/thread:/project:/global:/external:）表达层级：草稿本身就是层级的真相，
	// 前缀之后的 rest 才是用于过滤的查询。
	scope, rest := completionScope(query)
	// 分类行只在刚敲下触发符（查询为空）时出现，有查询字符后只剩叶子项。
	categories := query == "" && (kind == completionMention || kind == completionSkill)
	effective := query
	if scope != "" {
		effective = rest
	}

	changed := s.completionKind != kind || s.completionQuery != query || s.completionThreadID != threadID
	if changed {
		s.completionItems = nil
		s.completionSelected = 0
		s.completionLoading = false
		s.completionLoaded = false
		s.mentionSeq++
		noteCompletionLevel(kind, query)
	}
	s.completionDismissedFor = ""
	s.completionKind = kind
	s.completionStart = start
	s.completionQuery = query
	s.completionThreadID = threadID

	if kind == completionSlash {
		if cmds, cached := s.commandsCache[threadID]; cached {
			if changed || !s.completionLoaded {
				s.completionItems = slashCommandsToItems(filterCommands(withLocalSlashCommands(cmds, stewardSelected(s, threadID)), query, 8), query)
				s.completionLoaded = true
			}
			s.mu.Unlock()
			if changed {
				s.invalidate()
			}
			return
		}
	}

	// 技能候选来自磁盘扫描（带 60 秒缓存），不走后端请求；未就绪时面板显示 loading 行。
	if kind == completionSkill {
		skills, ready := s.skillsForProject(s.projectPathForThreadLocked(threadID))
		if changed || !s.completionLoaded || s.completionItems == nil {
			items := skillCompletionItems(filterSkillsByScope(skills, scope), effective)
			if categories {
				items = append(completionCategoryItems(kind), items...)
			}
			s.completionItems = items
		}
		s.completionLoading = !ready
		s.completionLoaded = ready
		s.mu.Unlock()
		if changed {
			s.invalidate()
		}
		return
	}

	threadItems := func() []completionItem {
		return recentThreadMentionItems(s.threads, s.projectIDForThreadLocked(threadID), threadID, effective, time.Now(), 5)
	}
	// 只看会话分类时不需要请求文件搜索。
	if kind == completionMention && scope == "thread" {
		s.completionItems = mentionCompletionItems(kind, scope, categories, threadItems(), nil)
		s.completionLoading = false
		s.completionLoaded = true
		s.mu.Unlock()
		if changed {
			s.invalidate()
		}
		return
	}
	// 会话引用项由本地 s.threads 直接算出，文件搜索结果返回前就能先展示。
	if kind == completionMention && (changed || !s.completionLoaded) {
		s.completionItems = mentionCompletionItems(kind, scope, categories, threadItems(), nil)
	}
	if s.completionLoading || s.completionLoaded {
		s.mu.Unlock()
		return
	}
	if s.isPendingThreadLocked(threadID) {
		// 待创建会话还没有后端会话：创建完成后再拉取命令/文件补全，避免无效请求。
		s.mu.Unlock()
		return
	}
	s.completionLoading = true
	seq := s.mentionSeq
	s.mu.Unlock()

	go func() {
		var items []completionItem
		var fileItems []completionItem
		var err error
		var cmds []SlashCommand
		if kind == completionSlash {
			cmds, err = s.client.Commands(context.Background(), threadID)
			items = slashCommandsToItems(filterCommands(withLocalSlashCommands(cmds, stewardSelected(s, threadID)), query, 8), query)
		} else {
			var matches []FileMatch
			matches, err = s.client.SearchFiles(context.Background(), threadID, effective)
			// 叶子项上限 20 条，面板可见行数由 renderCompletionMenu 封顶并滚动。
			if len(matches) > 20 {
				matches = matches[:20]
			}
			fileItems = fileMatchesToItems(matches)
		}
		s.mu.Lock()
		if s.mentionSeq == seq && s.completionThreadID == threadID && s.completionKind == kind && s.completionQuery == query {
			s.completionLoading = false
			s.completionLoaded = true
			if err == nil {
				if kind == completionSlash {
					s.commandsCache[threadID] = cmds
					s.completionItems = items
				} else {
					s.completionItems = mentionCompletionItems(kind, scope, categories, threadItems(), fileItems)
				}
			} else {
				s.toastMessage = "补全加载失败: " + err.Error()
			}
		}
		s.mu.Unlock()
		s.invalidate()
	}()
}

// projectIDForThreadLocked 返回指定会话所属的项目 ID，调用方需持有 s.mu；会话不存在时返回空字符串。
func (s *chatState) projectIDForThreadLocked(threadID string) string {
	for i := range s.threads {
		if s.threads[i].ID == threadID {
			return s.threads[i].ProjectID
		}
	}
	return ""
}

// syncDraftTags 将普通编辑同步到富文本模型，保留未被修改的文件 Tag。
// TextArea 保留原生组字和按键路径，Tag 通过 TextRanges 呈现。
func (s *chatState) syncDraftTags() {
	old, next := []rune(s.draftText.String()), []rune(s.draft)
	start := 0
	for start < len(old) && start < len(next) && old[start] == next[start] {
		start++
	}
	if start == len(old) && start == len(next) {
		return
	}
	a, b := len(old), len(next)
	for a > start && b > start && old[a-1] == next[b-1] {
		a--
		b--
	}
	for at, run := range s.draftText.Runs(0, s.draftText.Len()) {
		end := at + utf8.RuneCountInString(run.Text)
		if run.Attributes.Tag != "" && start < end && a > at {
			s.draftText = s.draftText.Format(at, end, func(attrs *richtext.Attributes) { attrs.Tag = "" })
		}
	}
	s.draftText = s.draftText.Replace(start, a, richtext.NewText(string(next[start:b])))
}

func (s *chatState) mentionRanges(color ui.Color) []ui.TextRange {
	var ranges []ui.TextRange
	for start, run := range s.draftText.Runs(0, s.draftText.Len()) {
		if run.Attributes.Tag != "" {
			ranges = append(ranges, ui.TextRange{Start: start, End: start + utf8.RuneCountInString(run.Text), Color: color, Weight: 600})
		}
	}
	return ranges
}

func (s *chatState) confirmCompletion(item completionItem) string {
	return s.confirmCompletionKeepOpen(item, false)
}

// confirmCompletionKeepOpen 把选中项写进草稿。keepOpen 为 true 时用于分类下钻：
// 只把分类前缀（如 "@file:"）写进草稿且不补尾随空格，让下一帧的 detectCompletion
// 仍然命中触发符；候选列表保留到下一帧重建，避免面板闪一下。
func (s *chatState) confirmCompletionKeepOpen(item completionItem, keepOpen bool) string {
	s.syncDraftTags()
	start := utf8.RuneCountInString(s.draft[:s.completionStart])
	if s.completionKind == completionMention && item.ThreadID != "" {
		// 会话引用改成独立胶囊：不再把长标题插进正文，只把触发用的 "@查询" 从草稿里去掉。
		s.addDraftThreadMention(item.ThreadID, item.Label)
		if start > utf8.RuneCountInString(s.draft) {
			start = utf8.RuneCountInString(s.draft)
		}
		s.draftText = s.draftText.Replace(start, s.draftText.Len(), richtext.NewText(""))
		s.draft = s.draftText.String()
		s.completionKind = completionNone
		s.completionItems = nil
		s.completionDismissedFor = ""
		s.mentionSeq++
		s.completionCaretToEnd = true
		return s.draft
	}
	insert := richtext.NewText(item.Insert)
	if s.completionKind == completionMention && !item.Category {
		tag := strings.TrimPrefix(item.Insert, "@")
		insert = insert.Format(0, insert.Len(), func(a *richtext.Attributes) {
			a.Tag = tag
		})
	}
	if keepOpen {
		s.draftText = s.draftText.Replace(start, s.draftText.Len(), insert)
		s.draft = s.draftText.String()
		sigil := "@"
		if s.completionKind == completionSkill {
			sigil = "$"
		}
		s.completionQuery = strings.TrimPrefix(item.Insert, sigil)
		s.completionLoaded = false
		s.completionLoading = false
		s.completionSelected = 0
		s.mentionSeq++
		s.completionCaretToEnd = true
		return s.draft
	}
	s.draftText = s.draftText.Replace(start, s.draftText.Len(), insert.Append(" ", richtext.Attributes{}))
	s.draft = s.draftText.String()
	s.completionKind = completionNone
	s.completionItems = nil
	s.completionDismissedFor = ""
	s.mentionSeq++
	s.completionCaretToEnd = true
	return s.draft
}

// addDraftThreadMention 把会话加进草稿的引用胶囊；同一会话只保留一颗，标题以最新一次补全为准。
// 调用方需持有 s.mu（补全确认路径已加锁）。
func (s *chatState) addDraftThreadMention(threadID, title string) {
	if threadID == "" {
		return
	}
	for i := range s.draftThreadMentions {
		if s.draftThreadMentions[i].ThreadID == threadID {
			s.draftThreadMentions[i].Title = title
			return
		}
	}
	s.draftThreadMentions = append(s.draftThreadMentions, ThreadMention{ThreadID: threadID, Title: title})
}

// removeDraftThreadMention 移除指定会话的引用胶囊（点击胶囊上的 ✕）。
func (s *chatState) removeDraftThreadMention(threadID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.draftThreadMentions {
		if s.draftThreadMentions[i].ThreadID == threadID {
			s.draftThreadMentions = append(s.draftThreadMentions[:i], s.draftThreadMentions[i+1:]...)
			break
		}
	}
	s.invalidate()
}

// removeLastDraftThreadMention 删除最后一颗引用胶囊（光标在起始处按 Backspace），返回是否删掉了胶囊。
func (s *chatState) removeLastDraftThreadMention() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.draftThreadMentions)
	if n == 0 {
		return false
	}
	s.draftThreadMentions = s.draftThreadMentions[:n-1]
	s.invalidate()
	return true
}

// expandDraftThreadMentions 把引用胶囊展开为发送文本。
// 胶囊不在正文里，所以先把缺失的引用还原成 "@标题" 追加到正文末尾（正文里已经写了同一个
// 触发词的则不再重复追加），再交给 expandThreadMentions 生成链接与上下文块。
func expandDraftThreadMentions(text string, mentions []ThreadMention, titleOf func(id string) string, history func(id string) ([]Message, error)) string {
	if len(mentions) == 0 {
		return text
	}
	keys := make(map[string]string, len(mentions))
	tokens := make([]string, 0, len(mentions))
	for _, m := range mentions {
		if m.ThreadID == "" {
			continue
		}
		title := m.Title
		if title == "" {
			title = "未命名会话"
		}
		key := "@" + strings.Map(mentionTitleRune, title)
		if _, seen := keys[key]; seen {
			// 同名会话只能落成一个触发词，保留先出现的那个。
			continue
		}
		keys[key] = m.ThreadID
		if !strings.Contains(text, key) {
			tokens = append(tokens, key)
		}
	}
	if len(keys) == 0 {
		return text
	}
	expanded := text
	if len(tokens) > 0 {
		expanded = strings.TrimRight(text, " \t\n")
		if expanded != "" {
			expanded += " "
		}
		expanded += strings.Join(tokens, " ")
	}
	return expandThreadMentions(expanded, keys, titleOf, history)
}

func (s *chatState) handleCompletionKey(key ui.Key, mods ui.Modifiers) (newText string, handled bool) {
	// MyGo 的文本编辑器先于窗口快捷键处理方向键，需在现有输入入口转交导航。
	if mods == ui.Cmd|ui.Alt && (key == ui.KeyUp || key == ui.KeyDown) {
		s.mu.Lock()
		composing := s.isComposing
		s.mu.Unlock()
		if !composing {
			offset := 1
			if key == ui.KeyUp {
				offset = -1
			}
			return "", s.selectAdjacentThread(offset)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.isComposing || s.completionSuspended {
		return "", false
	}

	if s.completionKind == completionNone {
		return "", false
	}

	n := len(s.completionItems)
	if key == ui.KeyEscape && mods == 0 {
		if s.completionStart >= 0 && s.completionStart <= len(s.draft) {
			s.completionDismissedFor = s.draft[s.completionStart:]
		}
		s.completionKind = completionNone
		s.completionItems = nil
		s.mentionSeq++
		return "", true
	}
	if n == 0 {
		return "", false
	}

	switch {
	case key == ui.KeyUp && mods == 0:
		// 会话引用失效时跳过该行；全部失效则保持在原地。
		s.selectAdjacentCompletion(-1)
		return "", true

	case key == ui.KeyDown && mods == 0:
		s.selectAdjacentCompletion(1)
		return "", true

	case key == ui.KeyTab && mods == 0:
		// Tab 只把选中项补进草稿，不执行；无参数本地命令留给 Enter 触发。
		// 分类行与目录项在 Tab 时保持面板打开：前者下钻一层，后者进入子目录。
		if s.completionSelected < 0 || s.completionSelected >= n {
			s.completionSelected = 0
		}
		item := s.completionItems[s.completionSelected]
		if completionThreadUnavailableLocked(s, item) != "" {
			// 失效的会话引用不插入，按键已消费。
			return "", true
		}
		return s.confirmCompletionKeepOpen(item, completionKeepsPanelOpen(s.completionKind, item, true)), true

	case key == ui.KeyEnter && mods == 0:
		if s.completionSelected < 0 || s.completionSelected >= n {
			s.completionSelected = 0
		}
		item := s.completionItems[s.completionSelected]
		if completionThreadUnavailableLocked(s, item) != "" {
			return "", true
		}
		kind := s.completionKind
		tid := s.completionThreadID
		newDraft := s.confirmCompletionKeepOpen(item, completionKeepsPanelOpen(kind, item, false))
		if kind == completionSlash && isNoArgLocalSlashCommand(item.Insert) {
			s.mu.Unlock()
			s.runLocalSlashCommand(tid, item.Insert)
			s.mu.Lock()
			return "", true
		}
		return newDraft, true
	}

	return "", false
}

// isNoArgLocalSlashCommand 判断是否为无参数本地命令。
func isNoArgLocalSlashCommand(insert string) bool {
	cmd := strings.TrimSpace(insert)
	switch cmd {
	case "/mode", "/model", "/effort", "/fast", "/pair", "/clear", "/copy", "/split":
		return true
	default:
		return false
	}
}

// runLocalSlashCommand 尝试拦截并本地执行斜杠命令。
// 草稿去掉首尾空白后，若第一个 token 是 /mode、/model、/effort、/fast、/pair、/export、/clear、/copy、/split 之一，
// 就在本地执行对应操作、清空草稿并返回 true；未匹配则返回 false，plan、compact 和 provider 命令仍作为文本发给后端。
func (s *chatState) runLocalSlashCommand(threadID, draft string) bool {
	if s == nil {
		return false
	}
	trimmed := strings.TrimSpace(draft)
	if !strings.HasPrefix(trimmed, "/") {
		return false
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return false
	}
	cmd := fields[0]

	switch cmd {
	case "/mode":
		s.mu.Lock()
		if s.selectedThreadID == "" && threadID != "" {
			s.selectedThreadID = threadID
		}
		s.mu.Unlock()
		s.toggleInteractionMode()
		s.clearDraftAndCompletion()
		return true

	case "/model":
		s.mu.Lock()
		tid := threadID
		if tid == "" {
			tid = s.selectedThreadID
		}
		s.mu.Unlock()
		composerModelPickerState.Tab = "lead"
		if tid != "" {
			composerModelPickerState.setListOpen(tid, ModelPickerSideLead, true)
		}
		composerModelPickerState.MenuOpen = true
		s.clearDraftAndCompletion()
		return true

	case "/effort":
		s.mu.Lock()
		tid := threadID
		if tid == "" {
			tid = s.selectedThreadID
		}
		s.mu.Unlock()
		composerModelPickerState.Tab = "lead"
		if tid != "" {
			composerModelPickerState.setListOpen(tid, ModelPickerSideLead, false)
			composerModelPickerState.setListOpen(tid, ModelPickerSideBees, false)
		}
		composerModelPickerState.MenuOpen = true
		s.clearDraftAndCompletion()
		return true

	case "/fast":
		s.mu.Lock()
		tid := threadID
		if tid == "" {
			tid = s.selectedThreadID
		}
		var targetThread *Thread
		for i := range s.threads {
			if s.threads[i].ID == tid {
				targetThread = &s.threads[i]
				break
			}
		}
		provider := ""
		model := ""
		if targetThread != nil {
			provider = targetThread.Provider
			model = targetThread.Model
		}
		if provider == "" {
			provider = s.newThreadProvider
		}
		if provider == "" && len(s.providers) > 0 {
			provider = s.providers[0].ID
		}
		if provider == "" {
			provider = "claude"
		}
		pin := modelPreferencePin(provider, model)
		// 记忆值与模型胶囊一致：主导 provider/模型 + 当前强度 + 翻转后的快速模式。
		effort := ""
		if targetThread != nil {
			effort = targetThread.Effort
		} else if p := s.pendingThread; p != nil {
			effort = p.Thread.Effort
		}
		s.mu.Unlock()

		// 一次 /fast 恰好翻转一次：toggleModelFastModeNow 在锁内同步改缓存并登记落盘，
		// 返回值就是最终生效的快速模式，写入记忆的 Fast 与它一致。
		fast := s.toggleModelFastModeNow(pin)
		// 用户主动翻转主导侧快速模式：只在这一刻写入“上次使用”。
		if provider != "" && model != "" {
			rememberModelSelection(ModelSelection{Provider: provider, Model: model, Effort: effort, Fast: fast})
		}
		s.clearDraftAndCompletion()
		return true

	case "/pair":
		composerModelPickerState.Tab = "bees"
		s.setSwarmEnabled(true)
		composerModelPickerState.MenuOpen = true
		s.clearDraftAndCompletion()
		return true

	case "/export":
		// 导出当前会话为 Markdown/JSON（见 thread_export.go:exportThread）
		s.clearDraftAndCompletion()
		s.exportThread(threadID)
		return true

	case "/copy":
		// 复制当前会话的预算版 Markdown；写剪贴板需要 Context，交给下一帧渲染落地
		// （见 composer_complete.go:renderCompletionMenu 与 thread_export.go:copyThreadMarkdown）。
		tid := threadID
		if tid == "" {
			s.mu.Lock()
			tid = s.selectedThreadID
			s.mu.Unlock()
		}
		s.clearDraftAndCompletion()
		requestCopyThreadMarkdown(tid)
		return true

	case "/clear":
		s.createNewThread()
		s.clearDraftAndCompletion()
		return true

	case "/split":
		// 分割目前只对总管会话开放：普通会话里只提示，不把文本发给后端。
		s.mu.Lock()
		tid := threadID
		if tid == "" {
			tid = s.selectedThreadID
		}
		s.mu.Unlock()
		if tid == "" || tid != s.stewardThreadID() {
			s.setToast("分割目前只对总管会话开放")
			s.clearDraftAndCompletion()
			return true
		}
		s.clearDraftAndCompletion()
		splitStewardSession(s, tid)
		return true

	default:
		return false
	}
}

// clearDraftAndCompletion 清空输入框草稿、引用胶囊及当前补全浮层。
func (s *chatState) clearDraftAndCompletion() {
	s.mu.Lock()
	s.draft = ""
	s.draftText = richtext.NewText("")
	s.draftThreadMentions = nil
	s.completionKind = completionNone
	s.completionItems = nil
	s.completionDismissedFor = ""
	s.mu.Unlock()
	s.invalidate()
}

const composerCompleteWidth = 460

// composerCompleteCategoryWidth 是只展示分类行时的窄面板宽度：分类行只有“▸ 名称”一项，不需要 460 的宽。
const composerCompleteCategoryWidth = 220

// composerCompleteMaxRows 是面板可见行数上限，超出的候选在面板内部滚动。
const composerCompleteMaxRows = 9

// composerCompleteRowH 是单行高度估值（行高 27 + 行距 2），用于面板高度动画。
const composerCompleteRowH = 29

// composerCompleteEmptyH 是空态行高：没有候选时面板保留，只换成一句说明。
const composerCompleteEmptyH = 44

// composerCompleteFooterH 是键位页脚行高，不含顶部 1pt 分隔线。
const composerCompleteFooterH = 24

// completionEmptyLabel 按补全类型返回空态文案：文件、会话、指令/技能各一句。
func completionEmptyLabel(kind completionKind, scope string) string {
	switch kind {
	case completionSkill:
		return "没有匹配的指令"
	case completionMention:
		if scope == "thread" {
			return "没有匹配的会话"
		}
		return "没有匹配的文件"
	default:
		return "没有匹配的指令"
	}
}

// completionItemIcon 返回叶子候选左侧的矢量图标：文件、会话、指令、技能各用一个。
func completionItemIcon(kind completionKind, item completionItem) *ui.SVG {
	switch {
	case kind == completionSkill:
		return IconSparkles()
	case kind == completionSlash:
		return IconTerminal()
	case item.ThreadID != "" || item.Icon == "thread":
		return IconMessageSquarePlus()
	default:
		return IconFile()
	}
}

// completionSourceLabel 把技能 Scope 映射成来源胶囊文案；未知来源返回空串，不画胶囊。
func completionSourceLabel(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "project":
		return "项目"
	case "global":
		return "全局"
	case "external":
		return "外部"
	case "builtin", "internal", "system":
		return "内置"
	}
	return ""
}

// renderCompletionFooterHint 渲染页脚的一组键帽加说明文字，整组带无障碍标签便于测试与读屏。
func renderCompletionFooterHint(c *ui.Context, key, kbd, label string) {
	ui.Row(c.Key(key)).Gap(Space1).AlignItems(ui.Center).Label(kbd + " " + label).Children(func() {
		Kbd(c, key+"-kbd", kbd)
		ui.Text(c, label).FontSize(TypeCaption).TextColor(ThemeSecondaryText(c))
	})
}

// renderCompletionFooter 渲染面板底部键位页脚：24pt 高，顶部 1pt 分隔线。
func renderCompletionFooter(c *ui.Context) {
	ui.Column(c.Key("composer-complete-footer")).FillWidth().Gap(0).Children(func() {
		ui.Box(c.Key("composer-complete-footer-divider")).FillWidth().Height(Hairline).Background(ThemeBorder(c))
		ui.Row(c.Key("composer-complete-footer-row")).
			FillWidth().
			Height(composerCompleteFooterH).
			Padding(0, Space3).
			AlignItems(ui.Center).
			Gap(Space4).
			Children(func() {
				renderCompletionFooterHint(c, "composer-complete-footer-nav", "↑↓", "选择")
				renderCompletionFooterHint(c, "composer-complete-footer-tab", "Tab", "补全")
				renderCompletionFooterHint(c, "composer-complete-footer-enter", "↵", "确认")
				renderCompletionFooterHint(c, "composer-complete-footer-esc", "esc", "关闭")
			})
	})
}

// completionLevel 记录上一次的补全层级（触发符 + 分类前缀）与它的切换序号。
// chatState 不能加字段（本次只改补全相关文件），所以放包级状态；只在渲染线程读写。
var completionLevel = struct {
	sync.Mutex
	key string
	run uint64
}{}

// noteCompletionLevel 在补全层级变化时递增序号，让面板做一次横向滑入；
// 同层级内继续输入不重播动画。
func noteCompletionLevel(kind completionKind, query string) {
	scope, _ := completionScope(query)
	key := fmt.Sprintf("%d:%s", kind, scope)
	completionLevel.Lock()
	if completionLevel.key != key {
		completionLevel.key = key
		completionLevel.run++
	}
	completionLevel.Unlock()
}

// completionLevelRun 返回当前层级切换序号。
func completionLevelRun() uint64 {
	completionLevel.Lock()
	defer completionLevel.Unlock()
	return completionLevel.run
}

// copyMarkdownRequest 是一条待执行的「复制会话 Markdown」请求。
// 斜杠命令在输入提交路径里同步执行、拿不到 ui.Context，而写剪贴板需要 Context，
// 所以先记下目标会话，由下一帧渲染补全弹层时（那里持有 c）消费。
type copyMarkdownRequest struct {
	mu   sync.Mutex
	tid  string
	wait bool
}

var pendingCopyMarkdown copyMarkdownRequest

// requestCopyThreadMarkdown 登记一次待执行的「复制会话 Markdown」。
func requestCopyThreadMarkdown(threadID string) {
	pendingCopyMarkdown.mu.Lock()
	pendingCopyMarkdown.tid = threadID
	pendingCopyMarkdown.wait = true
	pendingCopyMarkdown.mu.Unlock()
}

// consumeCopyThreadMarkdown 取走待执行的复制目标，第二个返回值表示是否有待执行请求。
func consumeCopyThreadMarkdown() (string, bool) {
	pendingCopyMarkdown.mu.Lock()
	defer pendingCopyMarkdown.mu.Unlock()
	if !pendingCopyMarkdown.wait {
		return "", false
	}
	tid := pendingCopyMarkdown.tid
	pendingCopyMarkdown.tid = ""
	pendingCopyMarkdown.wait = false
	return tid, true
}

// renderCompletionMenu 渲染补全候选列表弹层。
// 关闭时不用框架的 Exit 复制体（兄弟节点会瞬间补位，顶部直接贴到输入框边缘），
// 而是保留最后一次的候选快照，用 Animate 驱动高度、外边距与透明度一起收到 0，卡片随之逐帧上移。
func renderCompletionMenu(c *ui.Context, s *chatState, card ui.Element) {
	// /copy 的复制请求在这里落地：本函数每帧都被输入框渲染路径调用，持有可用的 Context。
	if tid, ok := consumeCopyThreadMarkdown(); ok {
		s.copyThreadMarkdown(c, tid)
	}
	s.mu.Lock()
	loading := s.completionLoading
	loaded := s.completionLoaded
	kind := s.completionKind
	scope, _ := completionScope(s.completionQuery)
	// 补全中：有触发字符（kind 非 none），且没有被组合输入（IME）或 esc 挂起。
	completing := kind != completionNone && !s.isComposing && !s.completionSuspended
	// 加载完成后即使没有候选也要保留面板：空态本身就是一次搜索结果（技能扫描未就绪时仍显示 loading 行）。
	visible := completing && !(len(s.completionItems) == 0 && !loading && !loaded)
	if visible {
		if len(s.completionItems) > 0 {
			s.completionSnap = append(s.completionSnap[:0], s.completionItems...)
		} else {
			// 空结果要清掉上一次的快照，否则面板会继续显示旧候选。
			s.completionSnap = s.completionSnap[:0]
		}
	}
	// 失效原因与会话快照在同一把锁内生成，保证和插入时看到的会话列一致。
	reasons := make([]string, len(s.completionSnap))
	for i := range s.completionSnap {
		reasons[i] = completionThreadUnavailableLocked(s, s.completionSnap[i])
	}
	if visible && len(reasons) > 0 && s.completionSelected >= 0 && s.completionSelected < len(reasons) && reasons[s.completionSelected] != "" {
		// 选中项已失效时移到最近的可用项；全部失效则保持原值。
		if next := nextUsableCompletionIndex(reasons, s.completionSelected); next >= 0 {
			s.completionSelected = next
		}
	}
	items := append([]completionItem(nil), s.completionSnap...)
	selected := s.completionSelected
	draft := s.draft
	s.mu.Unlock()

	target := float32(0)
	if visible {
		target = 1
	}
	// 没在补全、也没有上一帧的候选可收起时，整块浮层都不建：分屏时每个输入框都会往
	// overlay 根上挂一个面板，同 key 会让 mygo 立刻 panic；空态也因此只在补全中显示。
	if !visible && len(items) == 0 {
		return
	}
	// 只有分类行时用窄面板；出现叶子候选后展宽。
	width := float32(composerCompleteWidth)
	if len(items) > 0 && allCategoryItems(items) {
		width = composerCompleteCategoryWidth
	}
	// 行高约 27pt、行距 2pt、内边距 4pt×2、边框 1pt×2，略高估即可；行数封顶后由 Scroll 内部滚动。
	rows := len(items)
	if loading {
		rows++
	}
	if rows > composerCompleteMaxRows {
		rows = composerCompleteMaxRows
	}
	contentH := float32(rows) * composerCompleteRowH
	// 空态只在面板可见时渲染；关闭动画里没有候选就直接收起，不闪现文案。
	empty := visible && len(items) == 0 && !loading
	if empty {
		contentH = composerCompleteEmptyH
	}
	// 只有分类行时是下钻入口，不显示键位页脚；叶子列表与空态都带页脚。
	showFooter := empty || (len(items) > 0 && !allCategoryItems(items))
	footerH := float32(0)
	if showFooter {
		// 1pt 分隔线 + 24pt 页脚行 + 2pt 列间距，略高估即可。
		footerH = composerCompleteFooterH + 3
	}
	fullH := contentH + 12 + footerH
	// 层级切换时 12pt 横向滑入，Run 变化重播；同层级内继续输入不重播。
	run := completionLevelRun()
	// 浮层挂在输入卡片上沿，向上展开，不占用聊天区布局空间。
	ui.Overlay(c, func() {
		// 面板挂在窗口 overlay 根上，分屏时多个输入框各有一个；key 必须带上输入框实例标识，
		// 否则两个面板会在同一父节点下撞 key（two elements under one parent have the key）。
		col := ui.Column(c.Key(fmt.Sprintf("composer-complete-menu-%p", s)))
		p := col.Animate("open", target, animateDuration(c, 220*time.Millisecond))
		if !visible && p <= 0.01 {
			s.mu.Lock()
			s.completionSnap = nil
			s.mu.Unlock()
			return
		}
		slide := col.Keyframes("composer-complete-slide", []ui.Keyframe{
			{At: 0, Value: 12},
			{At: 180 * time.Millisecond, Value: 0, Ease: ui.EaseOut},
		}, ui.KeyframeOptions{Run: run})
		col.AttachTo(card, ui.AnchorTopLeft, ui.AnchorBottomLeft).Width(width).Margin(Space2, 0, Space2, 0).
			Transition(ui.ElementTransition{
				Duration: 220 * time.Millisecond,
				// 只让颜色走过渡：位置与尺寸已由 Animate 逐帧驱动，再叠加过渡会让浮层滞后于卡片，关闭时越过输入框。
				Colors: true,
				Enter:  &ui.Motion{Y: -6, Opacity: 0},
			}).
			Translate(slide, 0).
			// 透明度按平方衰减，收起时先于高度消失，不会在贴近输入框时仍可见。
			Opacity(p*p).
			ClipY().
			MaxHeight(fullH*p).
			Border(Hairline, ThemeBorder(c)).
			Radius(RadiusLG).
			Background(ThemeSurface(c)).
			Padding(Space1).
			Gap(Space0).
			Children(func() {
				scrollable := rows < len(items)+btoi(loading)
				list := func() {
					for i, item := range items {
						idx := i
						it := item
						reason := ""
						if idx < len(reasons) {
							reason = reasons[idx]
						}
						row := ui.Row(c.Key(fmt.Sprintf("composer-complete-%d", idx))).
							Padding(Space1, Space3).
							Radius(RadiusSM).
							AlignItems(ui.Center).
							Gap(Space3)
						if it.Category {
							// 分类行是“进目录”的入口：点一下只把前缀写进草稿，面板保持打开。
							row.OnClick(func() {
								s.mu.Lock()
								if s.draft == draft && s.completionKind != completionNone && !s.isComposing && !s.completionSuspended {
									s.confirmCompletionKeepOpen(it, true)
								}
								s.mu.Unlock()
								s.invalidate()
							})
						} else if reason == "" {
							// 失效的会话引用不注册点击，鼠标也插不进去。
							row.OnClick(func() {
								s.mu.Lock()
								if s.draft == draft && s.completionKind != completionNone && !s.isComposing && !s.completionSuspended {
									kind := s.completionKind
									tid := s.completionThreadID
									s.confirmCompletionKeepOpen(it, completionKeepsPanelOpen(kind, it, false))
									if kind == completionSlash && isNoArgLocalSlashCommand(it.Insert) {
										s.mu.Unlock()
										s.runLocalSlashCommand(tid, it.Insert)
										s.mu.Lock()
									}
								}
								s.mu.Unlock()
								s.invalidate()
							})
						}

						if idx == selected {
							row.Background(ThemeHover(c))
							if scrollable {
								// 键盘移动选中行时让它留在可见区域。
								row.ScrollIntoView()
							}
						}

						row.Children(func() {
							if it.Category {
								// 分类行单行展示，用次要色与更小字号，和叶子候选区分开。
								ui.Text(c, it.Label).
									FontSize(TypeFootnote).
									TextColor(ThemeSecondaryText(c))
								return
							}
							ui.Icon(c, completionItemIcon(kind, it)).
								Size(IconSM, IconSM).
								TextColor(ThemeSecondaryText(c))
							label := it.Label
							if reason != "" {
								// 失效行带上原因，便于定位；标题用次要色表示不可用。
								label = "会话引用已失效 · " + reason
							}
							labelColor := ThemePrimaryText(c)
							if reason != "" {
								labelColor = ThemeSecondaryText(c)
							}
							ui.Text(c, label).
								FontSize(TypeBody).
								TextColor(labelColor)

							if reason != "" {
								// 右侧说明换成失效原因，用危险色提示。
								ui.Spacer(c)
								ui.Text(c, reason).
									FontSize(TypeFootnote).
									TextColor(ThemeDanger(c))
							} else if it.Detail != "" || it.Source != "" {
								ui.Spacer(c)
								// 技能行的来源胶囊：项目 / 全局 / 外部（未知来源不画）。
								if source := completionSourceLabel(it.Source); source != "" {
									ui.Row(c.Key(fmt.Sprintf("composer-complete-source-%d", idx))).
										Padding(0, Space1).
										Radius(RadiusXS).
										Background(ThemeHover(c).Alpha(0.6)).
										Children(func() {
											ui.Text(c, source).
												FontSize(TypeCaption).
												TextColor(ThemeSecondaryText(c))
										})
								}
								if it.Detail != "" {
									ui.Text(c, it.Detail).
										FontSize(TypeFootnote).
										TextColor(ThemeSecondaryText(c))
								}
							}

							// 高亮行的参数提示贴在该行右侧，沿用 Detail 的次要颜色与字号。
							if idx == selected && it.Hint != "" && reason == "" {
								ui.Text(c, it.Hint).
									FontSize(TypeFootnote).
									TextColor(ThemeSecondaryText(c))
							}
						})
					}
					if loading {
						ui.Row(c.Key("composer-complete-loading")).
							Padding(Space1, Space3).
							Children(func() {
								ui.Text(c, "正在加载…").
									FontSize(TypeFootnote).
									TextColor(ThemeSecondaryText(c))
							})
					}
				}
				if empty {
					// 没有候选时保留面板，用 44pt 空态说明「搜过但是没有结果」。
					ui.Row(c.Key("composer-complete-empty")).
						Height(composerCompleteEmptyH).
						Padding(Space1, Space3).
						AlignItems(ui.Center).
						Children(func() {
							ui.Text(c, completionEmptyLabel(kind, scope)).
								FontSize(TypeFootnote).
								TextColor(ThemeSecondaryText(c))
						})
				} else if scrollable {
					ui.Scroll(c.Key("composer-complete-scroll")).
						MaxHeight(float32(composerCompleteMaxRows) * composerCompleteRowH).
						Children(func() {
							ui.Column(c.Key("composer-complete-list")).Gap(Space0).Children(list)
						})
				} else {
					list()
				}
				if showFooter {
					renderCompletionFooter(c)
				}
			})
	})
}

// allCategoryItems 判断候选是否全是分类行（此时面板用窄宽度）。
func allCategoryItems(items []completionItem) bool {
	for _, it := range items {
		if !it.Category {
			return false
		}
	}
	return true
}

// btoi 把布尔值转成 0/1，用于行数计算。
func btoi(v bool) int {
	if v {
		return 1
	}
	return 0
}
