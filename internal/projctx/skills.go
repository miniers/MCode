package projctx

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SkillScope 表示技能的来源层级。
type SkillScope string

const (
	// SkillScopeProject 表示项目级技能，同名时覆盖全局技能。
	SkillScopeProject SkillScope = "项目"
	// SkillScopeGlobal 表示全局技能。
	SkillScopeGlobal SkillScope = "全局"
)

// SkillInfo 描述一个已发现的技能。
type SkillInfo struct {
	Name        string
	Description string
	Path        string
	Scope       SkillScope
}

// skillScanMaxDepth 是每个技能根目录下最多递归的子目录层数。
const skillScanMaxDepth = 4

// skillFileMaxBytes 是单个 SKILL.md 的读取上限；超出部分截断，避免超大文件把正文整体读进内存。
const skillFileMaxBytes = 256 << 10

// projectSkillRoots 是项目级技能根目录（相对项目根），列表中靠后的根目录
// 覆盖靠前根目录中的同名技能。pi 专属目录放在最前，同名时让 .agents 这类通用目录优先。
var projectSkillRoots = []string{
	filepath.Join(".pi", "skills"),
	filepath.Join(".agents", "skills"),
	"skills",
	filepath.Join(".factory", "skills"),
}

// globalSkillRoots 是用户全局技能根目录（相对 home），覆盖顺序同上。
// pi 的用户级技能目录是 <home>/.pi/agent/skills。
var globalSkillRoots = []string{
	filepath.Join(".pi", "agent", "skills"),
	filepath.Join(".agents", "skills"),
	filepath.Join(".gemini", "antigravity", "skills"),
	filepath.Join(".gemini", "config", "skills"),
}

// BuildSkillsGuidance 扫描全局与项目级技能目录下的 SKILL.md，拼装为
// '[mcode 已启用技能]' 提示块；项目级同名技能覆盖全局技能；没有技能时返回空串。
func BuildSkillsGuidance(projectPath, home string) string {
	return BuildSkillsGuidanceExcluding(projectPath, home, nil)
}

// BuildSkillsGuidanceExcluding 与 BuildSkillsGuidance 相同，但在同名合并之后剔除
// excluded 判定为 true 的技能（用于跳过设置页已停用的技能）：被停用的项目级技能不会让
// 同名全局技能顶上来，因此过滤只能放在合并结果上；excluded 为 nil 时与
// BuildSkillsGuidance 完全一致；技能被全部剔除时返回空串。
func BuildSkillsGuidanceExcluding(projectPath, home string, excluded func(path string) bool) string {
	return formatSkillsGuidance(filterExcludedSkills(DiscoverSkills(projectPath, home), excluded))
}

// filterExcludedSkills 剔除 excluded 判定为 true 的技能；excluded 为 nil 时原样返回。
func filterExcludedSkills(skills []SkillInfo, excluded func(path string) bool) []SkillInfo {
	if excluded == nil {
		return skills
	}
	kept := make([]SkillInfo, 0, len(skills))
	for _, skill := range skills {
		if excluded(skill.Path) {
			continue
		}
		kept = append(kept, skill)
	}
	return kept
}

// skillIndexDescriptionMaxRunes 是精简目录里单条描述保留的最大字符数。
const skillIndexDescriptionMaxRunes = 100

// LeadSkillIndexDescriptionMaxRunes 是主代理（lead）精简目录里单条描述保留的最大字符数。
// 主代理依据目录描述决定是否自动读取某个技能，比子代理（bee）需要更长的描述才能保证
// 技能的自动触发率，因此放宽到 200 个字符。
const LeadSkillIndexDescriptionMaxRunes = 200

// BuildSkillsIndexExcluding 拼装 '[mcode 技能目录]' 精简索引：每个技能一行，只留名称、范围、
// 折叠空白后截断的描述与 SKILL.md 路径，供子代理（bee）在需要时自行读取对应文件；
// excluded 的语义与 BuildSkillsGuidanceExcluding 一致；技能被全部剔除时返回空串。
func BuildSkillsIndexExcluding(projectPath, home string, excluded func(path string) bool) string {
	return BuildSkillsIndexWithLimitExcluding(projectPath, home, excluded, skillIndexDescriptionMaxRunes)
}

// BuildSkillsIndexWithLimitExcluding 与 BuildSkillsIndexExcluding 相同，但单条描述按 maxRunes
// 个字符截断（maxRunes 非正数时不截断，即保留完整描述）。
func BuildSkillsIndexWithLimitExcluding(projectPath, home string, excluded func(path string) bool, maxRunes int) string {
	skills := filterExcludedSkills(DiscoverSkills(projectPath, home), excluded)
	return formatSkillsIndexWithLimit(applySkillOverrides(skills, loadSkillOverrides(home)), maxRunes)
}

// loadSkillOverrides 读取 <home>/.claude/settings.json 的 skillOverrides（技能名 -> 模式）；
// 文件缺失或解析失败时返回 nil，即不做任何覆盖。
func loadSkillOverrides(home string) map[string]string {
	if strings.TrimSpace(home) == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		return nil
	}
	var settings struct {
		SkillOverrides map[string]string `json:"skillOverrides"`
	}
	if json.Unmarshal(data, &settings) != nil {
		return nil
	}
	return settings.SkillOverrides
}

// applySkillOverrides 按 skillOverrides 精简目录：user-invocable-only 与 off 的技能不让模型
// 自动触发，直接剔除；name-only 只保留名称与路径，去掉描述。键按技能名精确匹配，
// 没有覆盖项（overrides 为空）时原样返回。
func applySkillOverrides(skills []SkillInfo, overrides map[string]string) []SkillInfo {
	if len(overrides) == 0 {
		return skills
	}
	kept := make([]SkillInfo, 0, len(skills))
	for _, skill := range skills {
		switch overrides[skill.Name] {
		case "user-invocable-only", "off":
			continue
		case "name-only":
			skill.Description = ""
		}
		kept = append(kept, skill)
	}
	return kept
}

// truncateRunes 先把 s 中的连续空白（含换行）折叠成单个空格并去掉首尾空白，
// 再按 rune 截断到最多 max 个字符；被截断时在末尾追加省略号。max 非正数时不截断。
func truncateRunes(s string, max int) string {
	collapsed := strings.Join(strings.Fields(s), " ")
	if max <= 0 {
		return collapsed
	}
	runes := []rune(collapsed)
	if len(runes) <= max {
		return collapsed
	}
	return string(runes[:max]) + "…"
}

// BuildSkillsGuidanceFor 只为 names 中列出的技能拼装 '[mcode 任务必读技能]' 提示块
// （用于让 bee 知道本次派发指定必读的技能，并给出完整描述）：清单来自 DiscoverSkills，按 names
// 的顺序、名称大小写不敏感地逐个匹配；找不到的名字静默忽略，重复名字只取一次，
// 一个都没匹配到时返回空串。
func BuildSkillsGuidanceFor(projectPath, home string, names []string) string {
	if len(names) == 0 {
		return ""
	}
	discovered := DiscoverSkills(projectPath, home)
	if len(discovered) == 0 {
		return ""
	}
	byName := make(map[string]SkillInfo, len(discovered))
	for _, skill := range discovered {
		byName[strings.ToLower(skill.Name)] = skill
	}

	selected := make([]SkillInfo, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" || seen[key] {
			continue
		}
		skill, found := byName[key]
		if !found {
			continue
		}
		seen[key] = true
		selected = append(selected, skill)
	}
	return formatRequiredSkillsGuidance(selected)
}

// formatSkillsGuidance 把技能清单拼装为 '[mcode 已启用技能]' 提示块；清单为空时返回空串。
func formatSkillsGuidance(skills []SkillInfo) string {
	if len(skills) == 0 {
		return ""
	}

	lines := []string{
		"[mcode 已启用技能]",
		"以下技能提供领域指令与专门能力：",
	}
	for _, skill := range skills {
		lines = append(lines, fmt.Sprintf("- %s（%s）: %s (path: %s)", skill.Name, skill.Scope, skill.Description, skill.Path))
	}
	lines = append(lines, "当任务匹配某个技能的描述时，读取其说明并应用。")
	return strings.Join(lines, "\n")
}

// formatRequiredSkillsGuidance 把技能清单拼装为 '[mcode 任务必读技能]' 提示块（带完整描述）；
// 清单为空时返回空串。
func formatRequiredSkillsGuidance(skills []SkillInfo) string {
	if len(skills) == 0 {
		return ""
	}

	lines := []string{
		"[mcode 任务必读技能]",
		"本任务指定使用以下技能，开始前先读取各 path 指向的 SKILL.md：",
	}
	for _, skill := range skills {
		lines = append(lines, fmt.Sprintf("- %s（%s）: %s (path: %s)", skill.Name, skill.Scope, skill.Description, skill.Path))
	}
	return strings.Join(lines, "\n")
}

// formatSkillsIndexWithLimit 把技能清单拼装为 '[mcode 技能目录]' 精简目录：每个技能一行，
// 描述折叠空白并截断到 maxRunes 个字符；清单为空时返回空串。
func formatSkillsIndexWithLimit(skills []SkillInfo, maxRunes int) string {
	if len(skills) == 0 {
		return ""
	}

	lines := []string{
		"[mcode 技能目录]",
		"以下为可用技能的精简目录；当任务匹配某个技能时，先读取其 path 指向的 SKILL.md 再应用：",
	}
	for _, skill := range skills {
		description := truncateRunes(skill.Description, maxRunes)
		if description == "" {
			lines = append(lines, fmt.Sprintf("- %s（%s） (path: %s)", skill.Name, skill.Scope, skill.Path))
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s（%s）: %s (path: %s)", skill.Name, skill.Scope, description, skill.Path))
	}
	return strings.Join(lines, "\n")
}

// DiscoverSkills 返回全局与项目级技能清单：项目级优先、同名覆盖全局，各自按名称升序。
func DiscoverSkills(projectPath, home string) []SkillInfo {
	var global []SkillInfo
	if strings.TrimSpace(home) != "" {
		global = scanSkillRoots(home, globalSkillRoots, SkillScopeGlobal)
	}
	var project []SkillInfo
	if strings.TrimSpace(projectPath) != "" {
		project = scanSkillRoots(projectPath, projectSkillRoots, SkillScopeProject)
	}

	merged := make(map[string]SkillInfo, len(global)+len(project))
	for _, skill := range global {
		merged[strings.ToLower(skill.Name)] = skill
	}
	for _, skill := range project {
		merged[strings.ToLower(skill.Name)] = skill
	}

	skills := make([]SkillInfo, 0, len(merged))
	for _, skill := range merged {
		skills = append(skills, skill)
	}
	sort.Slice(skills, func(i, j int) bool {
		if skills[i].Scope != skills[j].Scope {
			return skillScopePriority(skills[i].Scope) < skillScopePriority(skills[j].Scope)
		}
		return skills[i].Name < skills[j].Name
	})
	return skills
}

// scanSkillRoots 依次扫描 base 下的各个技能根目录。
func scanSkillRoots(base string, roots []string, scope SkillScope) []SkillInfo {
	var skills []SkillInfo
	for _, root := range roots {
		skills = append(skills, scanSkillDir(filepath.Join(base, root), scope)...)
	}
	return skills
}

// scanSkillDir 递归扫描 dir 下的 SKILL.md，最多深入 skillScanMaxDepth 层子目录。
func scanSkillDir(dir string, scope SkillScope) []SkillInfo {
	return walkSkillDir(dir, 0, scope, make(map[string]bool))
}

// walkSkillDir 递归扫描 dir；visited 记录已进入过的真实路径，用于符号链接成环时终止递归。
func walkSkillDir(dir string, depth int, scope SkillScope, visited map[string]bool) []SkillInfo {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	real := skillRealPath(dir)
	if visited[real] {
		return nil
	}
	visited[real] = true
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var skills []SkillInfo
	if skill, ok := readSkillFile(filepath.Join(dir, "SKILL.md"), filepath.Base(dir), scope); ok {
		skills = append(skills, skill)
	}
	if depth >= skillScanMaxDepth {
		return skills
	}
	for _, entry := range entries {
		// 隐藏目录（如 .git）不进入。
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		child := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			skills = append(skills, walkSkillDir(child, depth+1, scope, visited)...)
			continue
		}
		// 全局技能常以符号链接安装（如 ~/.agents/skills/ego-browser 指向真实目录），只有一个
		// 可验证的来源是用户自己，可以跟随；项目级技能目录来自不可信仓库，符号链接可能指向
		// 仓库外，一律不跟随。
		if scope != SkillScopeGlobal || entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		target, statErr := os.Stat(child)
		if statErr != nil || !target.IsDir() {
			continue
		}
		skills = append(skills, walkSkillDir(child, depth+1, scope, visited)...)
	}
	return skills
}

// skillRealPath 返回用于防环去重的规范路径：优先解析符号链接，解析失败时退化为 Clean。
func skillRealPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// readSkillFile 读取并解析一个 SKILL.md；name 缺失时回退目录名，description 缺失或为空时
// 回退正文第一段。项目级技能目录来自不可信仓库，只接受普通文件，符号链接会跟随到仓库外；
// 全局作用域下允许 SKILL.md 本身是符号链接（用户自己安装的技能），但目标必须是普通文件。
func readSkillFile(path, fallbackName string, scope SkillScope) (SkillInfo, bool) {
	info, err := os.Lstat(path)
	if err != nil {
		return SkillInfo{}, false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if scope != SkillScopeGlobal {
			return SkillInfo{}, false
		}
		target, statErr := os.Stat(path)
		if statErr != nil || !target.Mode().IsRegular() {
			return SkillInfo{}, false
		}
	} else if !info.Mode().IsRegular() {
		return SkillInfo{}, false
	}
	file, err := os.Open(path)
	if err != nil {
		return SkillInfo{}, false
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, skillFileMaxBytes))
	if err != nil {
		return SkillInfo{}, false
	}
	content := string(data)
	name, description := parseSkillFrontmatter(content)
	if strings.TrimSpace(name) == "" {
		name = fallbackName
	}
	if strings.TrimSpace(description) == "" {
		description = firstSkillParagraph(skillBody(content))
	}
	return SkillInfo{
		Name:        strings.TrimSpace(name),
		Description: strings.TrimSpace(description),
		Path:        path,
		Scope:       scope,
	}, true
}

// skillBody 返回 SKILL.md 去掉开头 YAML frontmatter 后的正文；没有 frontmatter 时原样返回。
func skillBody(content string) string {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return content
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return strings.Join(lines[i+1:], "\n")
		}
	}
	// frontmatter 未闭合：整段都算元数据，没有正文。
	return ""
}

// firstSkillParagraph 返回正文的第一段文本：跳过标题行与代码围栏，取连续非空行并用单个
// 空格连接，遇到空行即结束；没有可用文本时返回空串。
func firstSkillParagraph(body string) string {
	var parts []string
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			if len(parts) > 0 {
				break
			}
			continue
		}
		parts = append(parts, trimmed)
	}
	return strings.Join(parts, " ")
}

// parseSkillFrontmatter 解析 SKILL.md 开头 YAML frontmatter 中的 name 与 description，
// 不引入 YAML 依赖，因此只处理一个子集：
//   - 顶层 `key: value` 单行值（首尾引号会被去掉）；
//   - 以空格或制表符开头、且上一个 key 已给出的行视为该 key 的续行，续行里的冒号不会
//     被当成新 key，其他 key 的续行直接跳过；
//   - 折叠块标量 `>`/`>-`/`>+`：续行逐行 TrimSpace、丢弃空行后用单个空格连接；
//   - 字面块标量 `|`/`|-`/`|+`：去掉非空续行的公共最小缩进后用换行连接，末尾空行去掉；
//   - 值为空但紧跟缩进续行时，按折叠块标量处理。
//
// name 与 description 同名 key 以第一次出现的非空值为准。
func parseSkillFrontmatter(content string) (name, description string) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", ""
	}

	for i := 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "---" {
			break
		}
		// 空行与缩进行不是顶层 key 行；它们的冒号不会开启新 key。
		if strings.TrimSpace(line) == "" || isSkillContinuationLine(line) {
			continue
		}
		key, rawValue, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		rawValue = strings.TrimSpace(rawValue)

		// 收集紧随其后的续行：空行与缩进行，遇到顶层 key 行或结束标记为止。
		var continuation []string
		j := i + 1
		for ; j < len(lines); j++ {
			next := lines[j]
			if strings.TrimSpace(next) == "---" {
				break
			}
			if strings.TrimSpace(next) != "" && !isSkillContinuationLine(next) {
				break
			}
			continuation = append(continuation, next)
		}
		i = j - 1

		var value string
		switch rawValue {
		case ">", ">-", ">+":
			value = foldSkillScalarLines(continuation)
		case "|", "|-", "|+":
			value = literalSkillScalarLines(continuation)
		case "":
			// 空值若带缩进续行，按折叠块标量处理。
			if len(continuation) > 0 {
				value = foldSkillScalarLines(continuation)
			}
		default:
			value = strings.Trim(strings.TrimSpace(rawValue), `"'`)
		}

		switch key {
		case "name":
			if name == "" {
				name = value
			}
		case "description":
			if description == "" {
				description = value
			}
		}
	}
	return name, description
}

// isSkillContinuationLine 判断该行是否为缩进续行（以空格或制表符开头）。
func isSkillContinuationLine(line string) bool {
	return strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")
}

// foldSkillScalarLines 实现折叠块标量：逐行 TrimSpace，丢弃空行，用单个空格连接。
func foldSkillScalarLines(lines []string) string {
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, " ")
}

// literalSkillScalarLines 实现字面块标量：去掉非空行的公共最小缩进，用换行连接，去掉末尾空白。
func literalSkillScalarLines(lines []string) string {
	indent := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		width := len(line) - len(strings.TrimLeft(line, " \t"))
		if indent < 0 || width < indent {
			indent = width
		}
	}
	if indent < 0 {
		return ""
	}

	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			parts = append(parts, "")
			continue
		}
		if len(line) >= indent {
			line = line[indent:]
		} else {
			line = strings.TrimLeft(line, " \t")
		}
		parts = append(parts, line)
	}
	return strings.TrimRight(strings.Join(parts, "\n"), " \t\r\n")
}

func skillScopePriority(scope SkillScope) int {
	if scope == SkillScopeProject {
		return 0
	}
	return 1
}
