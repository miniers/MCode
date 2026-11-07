package projctx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 精简目录的固定格式：标题、第二行，每个技能一行且含名称与 path。
func TestBuildSkillsIndexExcluding_Format(t *testing.T) {
	project, home := fixtureBuildSkillsSelect(t)

	index := BuildSkillsIndexExcluding(project, home, nil)
	lines := strings.Split(index, "\n")
	if len(lines) != 2+3 {
		t.Fatalf("精简目录应为标题加第二行加 3 个技能行，实际 %d 行：\n%s", len(lines), index)
	}
	if lines[0] != "[mcode 技能目录]" {
		t.Fatalf("标题行不符：%q\n%s", lines[0], index)
	}
	if lines[1] != "以下为可用技能的精简目录；当任务匹配某个技能时，先读取其 path 指向的 SKILL.md 再应用：" {
		t.Fatalf("第二行不符：%q\n%s", lines[1], index)
	}
	for _, want := range []string{
		"- alpha（项目）: 项目 alpha (path: " + filepath.Join(project, ".agents", "skills", "alpha", "SKILL.md") + ")",
		"- beta（全局）: 全局 beta (path: " + filepath.Join(home, ".agents", "skills", "beta", "SKILL.md") + ")",
		"- delta（项目）: 项目 delta (path: " + filepath.Join(project, ".agents", "skills", "delta", "SKILL.md") + ")",
	} {
		if !strings.Contains(index, want) {
			t.Fatalf("缺少技能行 %q：\n%s", want, index)
		}
	}
	if strings.Contains(index, "[mcode 已启用技能]") {
		t.Fatalf("精简目录不应使用已启用技能清单的标题：\n%s", index)
	}
}

// 描述超过 100 个字符时按 rune 截断为 100 个字符并追加省略号。
func TestBuildSkillsIndexExcluding_TruncatesLongDescription(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	t.Setenv("HOME", home)

	long := strings.Repeat("长", 150)
	writeSkill(t, filepath.Join(home, ".agents", "skills"), "long", "---\nname: long\ndescription: "+long+"\n---\n正文")

	index := BuildSkillsIndexExcluding(project, home, nil)
	if !strings.Contains(index, strings.Repeat("长", 100)+"…") {
		t.Fatalf("描述应截断为 100 个字符加省略号：\n%s", index)
	}
	if strings.Contains(index, strings.Repeat("长", 101)) {
		t.Fatalf("描述不应保留超过 100 个字符：\n%s", index)
	}
}

// 描述超过 200 个字符时按 rune 截断为 200 个字符并追加省略号（主代理目录的放宽口径）。
func TestBuildSkillsIndexWithLimitExcluding_LeadLimit(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	t.Setenv("HOME", home)

	long := strings.Repeat("长", 250)
	writeSkill(t, filepath.Join(home, ".agents", "skills"), "long", "---\nname: long\ndescription: "+long+"\n---\n正文")

	index := BuildSkillsIndexWithLimitExcluding(project, home, nil, LeadSkillIndexDescriptionMaxRunes)
	if !strings.Contains(index, strings.Repeat("长", 200)+"…") {
		t.Fatalf("描述应截断为 200 个字符加省略号：\n%s", index)
	}
	if strings.Contains(index, strings.Repeat("长", 201)) {
		t.Fatalf("描述不应保留超过 200 个字符：\n%s", index)
	}
}

// 描述中的换行与连续空白折叠为单个空格。
func TestBuildSkillsIndexExcluding_CollapsesWhitespace(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	t.Setenv("HOME", home)

	writeSkill(t, filepath.Join(home, ".agents", "skills"), "wrapped",
		"---\nname: wrapped\ndescription: 第一行  第二行\t第三行\n---\n正文")

	index := BuildSkillsIndexExcluding(project, home, nil)
	if !strings.Contains(index, "- wrapped（全局）: 第一行 第二行 第三行 (path: ") {
		t.Fatalf("描述应折叠为单行：\n%s", index)
	}
}

// excluded 过滤生效：被剔除的技能既不出现在精简目录里，也不影响其余技能。
func TestBuildSkillsIndexExcluding_FiltersExcluded(t *testing.T) {
	project, home := fixtureBuildSkillsSelect(t)

	disabled := filepath.Join(home, ".agents", "skills", "beta", "SKILL.md")
	index := BuildSkillsIndexExcluding(project, home, func(path string) bool { return path == disabled })
	if strings.Contains(index, "beta") {
		t.Fatalf("被剔除的技能不应出现：\n%s", index)
	}
	if !strings.Contains(index, "- delta（项目）: 项目 delta") {
		t.Fatalf("未被剔除的技能应保留：\n%s", index)
	}

	all := BuildSkillsIndexExcluding(project, home, func(string) bool { return true })
	if all != "" {
		t.Fatalf("全部剔除时应返回空串，实际 %q", all)
	}
}

// 没有任何技能时返回空串。
func TestBuildSkillsIndexExcluding_NoSkillsReturnsEmpty(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	if got := BuildSkillsIndexExcluding(project, home, nil); got != "" {
		t.Fatalf("没有技能时应返回空串，实际 %q", got)
	}
}

// 长描述下精简目录明显短于全量技能清单。
func TestBuildSkillsIndexExcluding_ShorterThanGuidance(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, ".agents", "skills")
	description := strings.Repeat("很长的技能描述", 40)
	for _, name := range []string{"one", "two", "three"} {
		writeSkill(t, root, name, "---\nname: "+name+"\ndescription: "+description+"\n---\n正文")
	}

	index := BuildSkillsIndexExcluding(project, home, nil)
	guidance := BuildSkillsGuidanceExcluding(project, home, nil)
	if index == "" || guidance == "" {
		t.Fatalf("前置条件失败：index=%q guidance=%q", index, guidance)
	}
	if len(index) >= len(guidance) {
		t.Fatalf("精简目录应明显短于全量清单：index=%d 字符，guidance=%d 字符", len(index), len(guidance))
	}
	if len(index) > len(guidance)/2 {
		t.Fatalf("精简目录应明显短于全量清单（当前未过半）：index=%d 字符，guidance=%d 字符", len(index), len(guidance))
	}
}

// ~/.claude/settings.json 的 skillOverrides：user-invocable-only 与 off 不进目录，
// name-only 只留名称与路径，其余技能不受影响。
func TestBuildSkillsIndexExcluding_AppliesClaudeSkillOverrides(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	t.Setenv("HOME", home)

	for _, name := range []string{"keep", "hidden", "disabled", "brief"} {
		writeSkill(t, filepath.Join(home, ".agents", "skills"), name, "---\nname: "+name+"\ndescription: 描述 "+name+"\n---\n正文")
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	settings := `{"skillOverrides":{"hidden":"user-invocable-only","disabled":"off","brief":"name-only"}}`
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}

	index := BuildSkillsIndexExcluding(project, home, nil)
	if !strings.Contains(index, "- keep（全局）: 描述 keep (path: ") {
		t.Fatalf("未覆盖的技能应保留描述：\n%s", index)
	}
	for _, gone := range []string{"- hidden（", "- disabled（"} {
		if strings.Contains(index, gone) {
			t.Fatalf("%s 应被剔除：\n%s", gone, index)
		}
	}
	if !strings.Contains(index, "- brief（全局） (path: ") || strings.Contains(index, "描述 brief") {
		t.Fatalf("name-only 应只留名称与路径：\n%s", index)
	}
}
