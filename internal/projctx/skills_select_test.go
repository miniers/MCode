package projctx

import (
	"path/filepath"
	"strings"
	"testing"
)

// fixtureBuildSkillsSelect 铺一个含全局与项目级技能的临时目录，返回 (project, home)。
func fixtureBuildSkillsSelect(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	project := t.TempDir()
	t.Setenv("HOME", home)

	globalDir := filepath.Join(home, ".agents", "skills")
	writeSkill(t, globalDir, "alpha", "---\nname: alpha\ndescription: 全局 alpha\n---\n正文")
	writeSkill(t, globalDir, "beta", "---\nname: beta\ndescription: 全局 beta\n---\n正文")
	projectDir := filepath.Join(project, ".agents", "skills")
	writeSkill(t, projectDir, "alpha", "---\nname: alpha\ndescription: 项目 alpha\n---\n正文")
	writeSkill(t, projectDir, "delta", "---\nname: delta\ndescription: 项目 delta\n---\n正文")
	return project, home
}

// 选中顺序按 names 给定顺序，而不是 DiscoverSkills 的排序。
func TestBuildSkillsGuidanceFor_FollowsNamesOrder(t *testing.T) {
	project, home := fixtureBuildSkillsSelect(t)

	guidance := BuildSkillsGuidanceFor(project, home, []string{"beta", "delta"})
	if guidance == "" {
		t.Fatal("应选中两个技能")
	}
	betaAt := strings.Index(guidance, "- beta（全局）: 全局 beta")
	deltaAt := strings.Index(guidance, "- delta（项目）: 项目 delta")
	if betaAt < 0 || deltaAt < 0 {
		t.Fatalf("缺少选中的技能：\n%s", guidance)
	}
	if betaAt > deltaAt {
		t.Fatalf("顺序应按 names 给定顺序（beta 先于 delta）：\n%s", guidance)
	}
	if strings.Contains(guidance, "alpha") {
		t.Fatalf("未声明的技能不应出现：\n%s", guidance)
	}
}

// 名称匹配大小写不敏感。
func TestBuildSkillsGuidanceFor_CaseInsensitive(t *testing.T) {
	project, home := fixtureBuildSkillsSelect(t)

	guidance := BuildSkillsGuidanceFor(project, home, []string{"ALPHA"})
	if !strings.Contains(guidance, "- alpha（项目）: 项目 alpha") {
		t.Fatalf("大小写不敏感匹配失败：\n%s", guidance)
	}
	if strings.Contains(guidance, "全局 alpha") {
		t.Fatalf("项目级同名技能应覆盖全局：\n%s", guidance)
	}
}

// 未知名字静默忽略，不影响已知名字。
func TestBuildSkillsGuidanceFor_UnknownNamesIgnored(t *testing.T) {
	project, home := fixtureBuildSkillsSelect(t)

	guidance := BuildSkillsGuidanceFor(project, home, []string{"nope", "delta", "  ", "also-missing"})
	if strings.Contains(guidance, "nope") || strings.Contains(guidance, "also-missing") {
		t.Fatalf("未知名字不应出现在输出中：\n%s", guidance)
	}
	if !strings.Contains(guidance, "- delta（项目）: 项目 delta") {
		t.Fatalf("已知名字应保留：\n%s", guidance)
	}
	if strings.Contains(guidance, "beta") {
		t.Fatalf("未声明的技能不应出现：\n%s", guidance)
	}
}

// 全部名字都未知、或 names 为空时返回空串。
func TestBuildSkillsGuidanceFor_AllUnknownReturnsEmpty(t *testing.T) {
	project, home := fixtureBuildSkillsSelect(t)

	if got := BuildSkillsGuidanceFor(project, home, []string{"nope", "missing"}); got != "" {
		t.Fatalf("全部未知时应返回空串，实际 %q", got)
	}
	if got := BuildSkillsGuidanceFor(project, home, nil); got != "" {
		t.Fatalf("names 为 nil 时应返回空串，实际 %q", got)
	}
}

// 重复名字只取一次。
func TestBuildSkillsGuidanceFor_DeduplicatesNames(t *testing.T) {
	project, home := fixtureBuildSkillsSelect(t)

	guidance := BuildSkillsGuidanceFor(project, home, []string{"delta", "DELTA", "delta"})
	if got := strings.Count(guidance, "- delta（项目）: 项目 delta"); got != 1 {
		t.Fatalf("重复名字只应出现一次，实际 %d 次：\n%s", got, guidance)
	}
}

// 选中全部技能时输出为必读块：标题、第二行与每个技能行齐全，且顺序与 DiscoverSkills 一致。
func TestBuildSkillsGuidanceFor_MatchesRequiredBlockFormat(t *testing.T) {
	project, home := fixtureBuildSkillsSelect(t)

	var names []string
	for _, skill := range DiscoverSkills(project, home) {
		names = append(names, skill.Name)
	}
	if len(names) != 3 {
		t.Fatalf("前置条件失败：应发现 3 个技能，实际 %d", len(names))
	}

	guidance := BuildSkillsGuidanceFor(project, home, names)
	lines := strings.Split(guidance, "\n")
	if len(lines) != 2+len(names) {
		t.Fatalf("必读块应为标题加第二行加 %d 个技能行，实际 %d 行：\n%s", len(names), len(lines), guidance)
	}
	if lines[0] != "[mcode 任务必读技能]" {
		t.Fatalf("标题行不符：%q\n%s", lines[0], guidance)
	}
	if lines[1] != "本任务指定使用以下技能，开始前先读取各 path 指向的 SKILL.md：" {
		t.Fatalf("第二行不符：%q\n%s", lines[1], guidance)
	}
	if strings.Contains(guidance, "[mcode 已启用技能]") {
		t.Fatalf("必读块不应使用已启用技能清单的标题：\n%s", guidance)
	}

	want := []string{
		"- alpha（项目）: 项目 alpha (path: " + filepath.Join(project, ".agents", "skills", "alpha", "SKILL.md") + ")",
		"- delta（项目）: 项目 delta (path: " + filepath.Join(project, ".agents", "skills", "delta", "SKILL.md") + ")",
		"- beta（全局）: 全局 beta (path: " + filepath.Join(home, ".agents", "skills", "beta", "SKILL.md") + ")",
	}
	for i, wantLine := range want {
		if lines[2+i] != wantLine {
			t.Fatalf("第 %d 个技能行不符：\ngot  %q\nwant %q", i+2, lines[2+i], wantLine)
		}
	}
}
