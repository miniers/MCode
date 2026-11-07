package agentconf

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type skillSource struct {
	agent  string
	scope  string
	plugin string
	path   string
}

type skillsConfigFile struct {
	Disabled []string `json:"disabled"`
}

// toSkill 把 SetSkillEnabled/IsSkillActive 的入参归一成 Skill。
// 只有名字字符串时 Agent 与 Scope 为空，此时禁用键退化为纯名字（见 skillDisabledKey）。
func toSkill(target any) Skill {
	switch v := target.(type) {
	case Skill:
		return v
	case *Skill:
		if v == nil {
			return Skill{}
		}
		return *v
	case string:
		return Skill{Name: v}
	default:
		return Skill{Name: fmt.Sprint(target)}
	}
}

// skillDisabledKey 生成技能在停用集合中的键 <agent>:<scope>:<plugin>:<name>（插件技能）
// 或 <agent>:<scope>:<name>（普通技能），与 ScanSkills 的去重键同构：
// 同名但来源 Agent、作用域或插件不同的技能互不影响。
// Agent、Scope 都为空（只传了名字）时返回纯名字，仍按旧语义作用于所有同名技能。
func skillDisabledKey(skill Skill) string {
	name := skill.ID()
	agent := strings.ToLower(strings.TrimSpace(skill.Agent))
	scope := strings.ToLower(strings.TrimSpace(skill.Scope))
	// 插件技能固定归到 claude/external，即使调用方没填 Agent/Scope 也能拿到稳定的键。
	if plugin := strings.TrimSpace(skill.Plugin); plugin != "" {
		return "claude:external:" + plugin + ":" + name
	}
	if agent == "" && scope == "" {
		return name
	}
	return agent + ":" + scope + ":" + name
}

// hasLegacyNameKey 判断禁用集合里是否有旧版本写下的纯名字条目（不含冒号），
// 这类条目对同名技能全部生效。
func hasLegacyNameKey(set map[string]bool, name string) bool {
	return name != "" && !strings.Contains(name, ":") && set[name]
}

// isSkillDisabledIn 判断禁用集合是否让该技能失效：先匹配完整键，
// 再兼容旧数据里的纯名字条目。插件技能不参与旧名字键匹配：
// 旧记录都是普通技能写的纯名字，命中它会把插件同名技能一并停掉。
func isSkillDisabledIn(set map[string]bool, skill Skill) bool {
	if len(set) == 0 {
		return false
	}
	if set[skillDisabledKey(skill)] {
		return true
	}
	if strings.TrimSpace(skill.Plugin) != "" {
		return false
	}
	return hasLegacyNameKey(set, skill.ID())
}

// legacyProjectSkillsConfigPath 是旧版本写在用户仓库里的项目级技能配置。
// 它会被提交进版本库，新版本只读兼容，不再往里写。
func legacyProjectSkillsConfigPath(opts Options) string {
	if opts.ProjectDir == "" {
		return ""
	}
	return filepath.Join(opts.ProjectDir, ".mcode", "skills.json")
}

// projectSkillsConfigPath 返回项目级技能配置路径：<dataDir>/skills-projects/<hash>.json。
// 按项目绝对路径的 sha256 前 16 位 hex 命名，同一项目在不同工作副本里共用一份开关状态，
// 且不会往用户仓库里写文件。dataDir 解析为空时回退 <home>/.mcode，与全局路径回退一致。
func projectSkillsConfigPath(opts Options) string {
	projectDir := opts.ProjectDir
	if projectDir == "" {
		return ""
	}
	if absProj, err := filepath.Abs(projectDir); err == nil {
		projectDir = absProj
	}
	sum := sha256.Sum256([]byte(projectDir))
	dataDir := resolveDataDir(opts)
	if dataDir == "" {
		home, _, _ := resolveOptions(opts)
		dataDir = filepath.Join(home, ".mcode")
	}
	return filepath.Join(dataDir, "skills-projects", hex.EncodeToString(sum[:8])+".json")
}

func getSkillsConfigPath(opts Options, scope string) string {
	if strings.EqualFold(scope, "project") && opts.ProjectDir != "" {
		return projectSkillsConfigPath(opts)
	}
	dataDir := resolveDataDir(opts)
	if dataDir == "" {
		home, _, _ := resolveOptions(opts)
		dataDir = filepath.Join(home, ".mcode")
	}
	return filepath.Join(dataDir, "skills.json")
}

// loadSkillDisabledSets 一次性读出某个作用域下所有生效的停用集合：全局/新项目路径在前，
// 旧的项目内 .mcode/skills.json 在后（只读兼容）。读取失败的文件直接跳过，
// 不影响其余来源，与 IsSkillActive 逐文件容错的旧行为一致。
func loadSkillDisabledSets(filePaths ...string) []map[string]bool {
	sets := make([]map[string]bool, 0, len(filePaths))
	for _, path := range filePaths {
		if path == "" {
			continue
		}
		set, err := loadDisabledSkills(path)
		if err != nil {
			continue
		}
		sets = append(sets, set)
	}
	return sets
}

// isSkillDisabledInAny 判断多个停用集合里是否有任意一个让该技能失效。
func isSkillDisabledInAny(sets []map[string]bool, skill Skill) bool {
	for _, set := range sets {
		if isSkillDisabledIn(set, skill) {
			return true
		}
	}
	return false
}

// activeSkillDisabledSets 汇总扫描期间要用的全部停用集合：全局、项目新路径与旧项目文件。
func activeSkillDisabledSets(opts Options) []map[string]bool {
	var paths []string
	paths = append(paths, getSkillsConfigPath(opts, "global"))
	if opts.ProjectDir != "" {
		paths = append(paths, projectSkillsConfigPath(opts), legacyProjectSkillsConfigPath(opts))
	}
	return loadSkillDisabledSets(paths...)
}

func loadDisabledSkills(filePath string) (map[string]bool, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]bool), nil
		}
		return nil, err
	}
	var cfg skillsConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(cfg.Disabled))
	for _, id := range cfg.Disabled {
		set[id] = true
	}
	return set, nil
}

func saveDisabledSkills(filePath string, set map[string]bool) error {
	list := make([]string, 0, len(set))
	for k, v := range set {
		if v {
			list = append(list, k)
		}
	}
	sort.Strings(list)
	data, err := json.MarshalIndent(skillsConfigFile{Disabled: list}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filePath, data, 0644)
}

// SetSkillEnabled 设置指定技能的启用/停用状态。
// target 可以是 skill 名称、ID、Skill 结构体或 *Skill：传 Skill/*Skill 时按
// <agent>:<scope>:<name>（插件技能为 claude:external:<plugin>:<name>）精确到其中一条，
// 只影响它自己；只传名称时无法判断是哪一条，保持旧语义，写纯名字键，对所有同名技能一起生效。
// 作用域决定写哪个配置文件：external（插件技能）与 global 写全局 skills.json，
// project 写 <dataDir>/skills-projects/<hash>.json，不再往用户仓库里写 .mcode/skills.json。
func SetSkillEnabled(opts Options, target any, enabled bool) error {
	sk := toSkill(target)
	name := sk.ID()
	if name == "" {
		return errors.New("skill identifier cannot be empty")
	}
	filePath := getSkillsConfigPath(opts, sk.Scope)
	// 读不出来时不能当成「没有禁用项」写回，否则会把已有的停用列表整体覆盖掉。
	set, err := loadDisabledSkills(filePath)
	if err != nil {
		return fmt.Errorf("读取技能配置文件 %s 失败：%w", filePath, err)
	}

	key := skillDisabledKey(sk)
	// 旧版项目级配置写在仓库里，这里单独记下它的路径，启用时需要一并清理。
	legacyProjectPath := ""
	if strings.EqualFold(sk.Scope, "project") && sk.Plugin == "" {
		legacyProjectPath = legacyProjectSkillsConfigPath(opts)
	}
	if sk.Agent == "" && sk.Scope == "" {
		key = name
	} else if sk.Plugin == "" && hasLegacyNameKey(set, name) {
		// 旧数据只有技能名，会连带停用同名技能：先展开成新键（其它同名技能保持停用），
		// 再删掉旧条目，本次开关只改变被操作的那一条；展开与写盘在同一次原子写盘里完成。
		// 展开时要跳过插件技能：旧名字键从未作用于它们（见 isSkillDisabledIn），
		// 展开时一并写进去等于偷停用一批用户没动过的插件技能；
		// 目标本身是插件技能时也不走这里，因为它不被旧名字键影响。
		// 扫描失败时必须整条放弃：此时拿不到同名技能清单，若照旧删旧条目、只写目标键，
		// 旧条目的停用范围就被收窄成停用目标那一条，其它同名技能被悄悄重新启用，
		// 所以这里保持集合原样、不写盘，直接上报错误。
		scanned, scanErr := ScanSkills(opts)
		if scanErr != nil {
			return fmt.Errorf("读取技能列表失败，无法迁移旧的停用记录：%w", scanErr)
		}
		delete(set, name)
		for _, sv := range scanned {
			if sv.ID() == name && strings.TrimSpace(sv.Plugin) == "" {
				set[skillDisabledKey(sv)] = true
			}
		}
	}

	if enabled {
		delete(set, key)
	} else {
		set[key] = true
	}
	if err := saveDisabledSkills(filePath, set); err != nil {
		return err
	}
	if enabled {
		// 旧记录写在用户仓库里的项目配置里，只删不改：文件不存在就不创建。
		// 旧版本写的可能是完整键，也可能只有纯名字（还没展开），两种都要清。
		return removeSkillDisabledKeys(legacyProjectPath, key, name)
	}
	return nil
}

// removeSkillDisabledKeys 从旧配置里删掉若干停用键；文件不存在、读不出来或本来就没有这些键时什么都不做。
func removeSkillDisabledKeys(filePath string, keys ...string) error {
	if filePath == "" {
		return nil
	}
	set, err := loadDisabledSkills(filePath)
	if err != nil || len(set) == 0 {
		return nil
	}
	changed := false
	for _, key := range keys {
		if key != "" && set[key] {
			delete(set, key)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return saveDisabledSkills(filePath, set)
}

// IsSkillActive 判断技能是否处于启用状态（未被禁用）。
// target 可以是 skill 名称、ID、Skill 结构体或 *Skill：传 Skill/*Skill 时按
// <agent>:<scope>:<name>（插件技能为 claude:external:<plugin>:<name>）判断，
// 同名但来自其它 Agent 或插件的技能不受影响；只传名称时按名字判断，
// 任何同名非插件技能被停用都算停用（旧行为）。
func IsSkillActive(opts Options, target any) bool {
	sk := toSkill(target)
	if sk.ID() == "" {
		return true
	}
	// 1. 优先检查项目级配置（若在项目中）：新路径 + 旧的文件内配置
	if opts.ProjectDir != "" {
		sets := loadSkillDisabledSets(projectSkillsConfigPath(opts), legacyProjectSkillsConfigPath(opts))
		if isSkillDisabledInAny(sets, sk) {
			return false
		}
	}
	// 2. 检查全局配置
	globalPath := getSkillsConfigPath(opts, "global")
	if set, err := loadDisabledSkills(globalPath); err == nil && isSkillDisabledIn(set, sk) {
		return false
	}
	return true
}

// globalSkillSources 返回全局层的技能来源目录：
// Claude、Codex、Cursor、Factory、Antigravity、Gemini、Pi。
// Pi 的用户级技能目录是 <home>/.pi/agent/skills（pi 的 agentDir，可被 PI_CODING_AGENT_DIR 覆盖）。
func globalSkillSources(home string) []skillSource {
	return []skillSource{
		{agent: "claude", scope: "global", path: filepath.Join(home, ".claude", "skills")},
		{agent: "codex", scope: "global", path: filepath.Join(home, ".codex", "skills")},
		{agent: "codex", scope: "global", path: filepath.Join(home, ".agents", "skills")},
		{agent: "cursor", scope: "global", path: filepath.Join(home, ".cursor", "skills")},
		{agent: "factory", scope: "global", path: filepath.Join(home, ".factory", "skills")},
		{agent: "antigravity", scope: "global", path: filepath.Join(home, ".gemini", "antigravity", "skills")},
		{agent: "antigravity", scope: "global", path: filepath.Join(home, ".gemini", "antigravity", "builtin", "skills")},
		{agent: "gemini", scope: "global", path: filepath.Join(home, ".gemini", "config", "skills")},
		{agent: "pi", scope: "global", path: filepath.Join(home, ".pi", "agent", "skills")},
	}
}

// projectSkillSources 返回项目层的技能来源目录，只包含参考项目
// SkillCatalogService.scanProjectSkills 实际支持的项目级目录（Pi 的项目级目录是 .pi/skills）。
func projectSkillSources(projectDir string) []skillSource {
	return []skillSource{
		{agent: "claude", scope: "project", path: filepath.Join(projectDir, ".claude", "skills")},
		{agent: "codex", scope: "project", path: filepath.Join(projectDir, ".codex", "skills")},
		{agent: "codex", scope: "project", path: filepath.Join(projectDir, ".agents", "skills")},
		{agent: "factory", scope: "project", path: filepath.Join(projectDir, ".factory", "skills")},
		{agent: "pi", scope: "project", path: filepath.Join(projectDir, ".pi", "skills")},
	}
}

// scanSkillRoot 遍历一个技能根目录，把每个子目录里的 SKILL.md 解析成 Skill 追加到 skills。
// 普通来源与插件来源共用这段逻辑：入口只在 Agent/Scope/Plugin 上不同，解析与去重规则完全一致。
func scanSkillRoot(src skillSource, disabled []map[string]bool, skills []Skill, seen map[string]bool) ([]Skill, error) {
	entries, err := os.ReadDir(src.path)
	if err != nil {
		return skills, err
	}

	for _, entry := range entries {
		subDirPath := filepath.Join(src.path, entry.Name())
		fi, err := os.Stat(subDirPath)
		if err != nil || !fi.IsDir() {
			continue
		}

		skillMDPath := filepath.Join(subDirPath, "SKILL.md")
		sfi, err := os.Stat(skillMDPath)
		if err != nil || sfi.IsDir() {
			continue
		}

		absSkillPath, err := filepath.Abs(skillMDPath)
		if err != nil {
			absSkillPath = skillMDPath
		}

		contentBytes, err := os.ReadFile(skillMDPath)
		if err != nil {
			continue
		}

		name, desc := parseFrontmatter(string(contentBytes), entry.Name())
		if name == "" {
			name = entry.Name()
		}

		dedupKey := src.agent + ":" + src.scope + ":" + src.plugin + ":" + name
		if seen[dedupKey] {
			continue
		}
		seen[dedupKey] = true

		sk := Skill{
			Agent:       src.agent,
			Scope:       src.scope,
			Plugin:      src.plugin,
			Name:        name,
			Description: desc,
			Path:        absSkillPath,
		}
		active := !isSkillDisabledInAny(disabled, sk)
		sk.Enabled = active
		sk.Disabled = !active
		skills = append(skills, sk)
	}
	return skills, nil
}

// ScanSkills 扫描 Claude、Codex、Cursor、Factory、Antigravity、Gemini、Pi 等 Harness 的 Skills 配置，
// 以及已启用的 Claude Code 插件自带的技能（Scope 为 external，Plugin 为插件名）。
func ScanSkills(opts Options) ([]Skill, error) {
	home, projectDir, homeErr := resolveOptions(opts)

	var sources []skillSource
	if homeErr == nil && home != "" {
		sources = append(sources, globalSkillSources(home)...)
		for _, ps := range pluginSkillSources(home) {
			sources = append(sources, skillSource{agent: "claude", scope: "external", plugin: ps.plugin, path: ps.path})
		}
	}
	if projectDir != "" {
		sources = append(sources, projectSkillSources(projectDir)...)
	}

	var readErrors []error
	attemptedCount := len(sources)
	if homeErr != nil && opts.Home == "" {
		// home 不可解析时全局来源被整体跳过，按跳过数量计入尝试次数，全部失败时才上报错误。
		skipped := globalSkillSources("")
		attemptedCount += len(skipped)
		for range skipped {
			readErrors = append(readErrors, homeErr)
		}
	}

	// 停用集合在扫描开始时一次性读好，避免每个技能都重新解析一遍配置文件。
	disabled := activeSkillDisabledSets(opts)

	var skills []Skill
	seen := make(map[string]bool)

	for _, src := range sources {
		var err error
		skills, err = scanSkillRoot(src, disabled, skills, seen)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			readErrors = append(readErrors, err)
			continue
		}
	}

	if attemptedCount > 0 && len(readErrors) == attemptedCount {
		return nil, errors.Join(readErrors...)
	}

	sortSkills(skills)
	if skills == nil {
		skills = []Skill{}
	}
	return skills, nil
}

// parseFrontmatter 解析 SKILL.md 开头由 --- 包围的 YAML frontmatter 中的 name 与 description。
func parseFrontmatter(content, fallbackName string) (name, desc string) {
	content = strings.TrimPrefix(content, "\ufeff")
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return fallbackName, ""
	}

	firstIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if trimmed == "---" {
			firstIdx = i
			break
		}
		return fallbackName, ""
	}
	if firstIdx == -1 {
		return fallbackName, ""
	}

	endIdx := -1
	for i := firstIdx + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			endIdx = i
			break
		}
	}
	if endIdx == -1 {
		return fallbackName, ""
	}

	fmLines := lines[firstIdx+1 : endIdx]
	for i := 0; i < len(fmLines); i++ {
		line := fmLines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// 只认顶层键：缩进行属于上一个键的嵌套内容（如 metadata.name），
		// 让它参与匹配会把真正的 name / description 覆盖掉。
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}

		if strings.HasPrefix(trimmed, "name:") {
			val := strings.TrimSpace(strings.TrimPrefix(trimmed, "name:"))
			name = unquote(val)
			continue
		}

		if strings.HasPrefix(trimmed, "description:") {
			val := strings.TrimSpace(strings.TrimPrefix(trimmed, "description:"))
			if isBlockScalarIndicator(val) {
				blockType := val[0]
				var blockLines []string
				j := i + 1
				for ; j < len(fmLines); j++ {
					bLine := fmLines[j]
					bTrimmed := strings.TrimSpace(bLine)
					if bTrimmed == "" {
						blockLines = append(blockLines, "")
						continue
					}
					if !strings.HasPrefix(bLine, " ") && !strings.HasPrefix(bLine, "\t") {
						break
					}
					blockLines = append(blockLines, bLine)
				}
				i = j - 1

				if blockType == '>' {
					var parts []string
					for _, bl := range blockLines {
						t := strings.TrimSpace(bl)
						if t != "" {
							parts = append(parts, t)
						}
					}
					desc = strings.Join(parts, " ")
				} else {
					minIndent := -1
					for _, bl := range blockLines {
						if strings.TrimSpace(bl) == "" {
							continue
						}
						indent := 0
						for _, r := range bl {
							if r == ' ' || r == '\t' {
								indent++
							} else {
								break
							}
						}
						if minIndent == -1 || indent < minIndent {
							minIndent = indent
						}
					}
					if minIndent < 0 {
						minIndent = 0
					}
					var stripped []string
					for _, bl := range blockLines {
						if len(bl) >= minIndent {
							stripped = append(stripped, bl[minIndent:])
						} else {
							stripped = append(stripped, strings.TrimSpace(bl))
						}
					}
					desc = strings.TrimSpace(strings.Join(stripped, "\n"))
				}
			} else if val != "" {
				// YAML 多行普通标量：首行有值时，紧跟的缩进续行 TrimSpace 后用单个空格拼上。
				raw := []string{val}
				j := i + 1
				for ; j < len(fmLines); j++ {
					next := fmLines[j]
					if strings.TrimSpace(next) == "" {
						continue
					}
					if !strings.HasPrefix(next, " ") && !strings.HasPrefix(next, "\t") {
						break
					}
					raw = append(raw, strings.TrimSpace(next))
				}
				i = j - 1
				desc = unquote(strings.Join(raw, " "))
			} else {
				desc = ""
			}
			continue
		}
	}

	if name == "" {
		name = fallbackName
	}
	desc = cleanDescription(desc)
	return name, desc
}

// isBlockScalarIndicator 判断值是否是 YAML 块标量指示符（> / | 带可选的 chomping 标记）。
func isBlockScalarIndicator(val string) bool {
	switch val {
	case ">", ">-", ">+", "|", "|-", "|+":
		return true
	}
	return false
}

// cleanDescription 去首尾空白，超过 300 个字符（按 rune）截断并加 …。
func cleanDescription(desc string) string {
	desc = strings.TrimSpace(desc)
	runes := []rune(desc)
	if len(runes) > 300 {
		return string(runes[:300]) + "…"
	}
	return desc
}

// unquote 去除单引号、双引号及行尾可能存在的非引号内注释。
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if s[0] == '"' && s[len(s)-1] == '"' {
			if unq, err := strconv.Unquote(s); err == nil {
				return unq
			}
			return s[1 : len(s)-1]
		}
		if s[0] == '\'' && s[len(s)-1] == '\'' {
			return s[1 : len(s)-1]
		}
	}
	if idx := strings.Index(s, " #"); idx != -1 {
		s = strings.TrimSpace(s[:idx])
	}
	return s
}
