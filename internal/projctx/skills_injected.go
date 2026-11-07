package projctx

import (
	"path/filepath"
	"strings"
)

// IsSkillInjectedPath 判断一个技能是否位于 mcode 会注入提示词的技能根目录之下（即
// BuildSkillsGuidance 会把它的名字写进 '[mcode 已启用技能]' 块）。
//
// skillPath 既可以是 SKILL.md 的路径，也可以是技能目录的路径；判定只做词法前缀匹配，
// 不解析符号链接，与技能发现逻辑按目录词法扫描的口径保持一致。
//
// 判定不能改用 agentconf.Skill.Agent：设置页会按来源给技能打 Agent 标签（~/.agents/skills
// 被标成 codex），但那一批目录恰恰是注入的主力，标记与注入与否并不等价。
//
// projectPath 或 home 为空串时跳过对应的一组根目录。
func IsSkillInjectedPath(projectPath, home, skillPath string) bool {
	path := strings.TrimSpace(skillPath)
	if path == "" {
		return false
	}
	if filepath.Base(path) == "SKILL.md" {
		path = filepath.Dir(path)
	}
	path = filepath.Clean(path)

	if base := strings.TrimSpace(projectPath); base != "" {
		if skillPathUnder(path, filepath.Clean(base), projectSkillRoots) {
			return true
		}
	}
	if base := strings.TrimSpace(home); base != "" {
		if skillPathUnder(path, filepath.Clean(base), globalSkillRoots) {
			return true
		}
	}
	return false
}

// skillPathUnder 判断 path 是否落在 base 下 roots 中的某个根目录之内（根目录自身也算）。
// 前缀比较必须带上路径分隔符边界，否则 /a/skills 会误匹配 /a/skills2 这类同级目录。
func skillPathUnder(path, base string, roots []string) bool {
	for _, root := range roots {
		dir := filepath.Clean(filepath.Join(base, root))
		if path == dir || strings.HasPrefix(path, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
