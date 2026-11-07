package projctx

import (
	"os"
	"path/filepath"
	"testing"
)

// 全局技能常以符号链接安装（如 ~/.agents/skills/ego-browser -> 真实目录），应被发现。
func TestDiscoverSkills_全局符号链接技能目录被发现(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	t.Setenv("HOME", home)

	// 真实技能目录放在技能根目录之外，通过符号链接挂进全局根。
	sourceDir := filepath.Join(t.TempDir(), "ego-browser")
	writeSkill(t, filepath.Dir(sourceDir), "ego-browser", "---\nname: ego-browser\ndescription: 全局链接技能\n---\n正文")
	globalDir := filepath.Join(home, ".agents", "skills")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatalf("创建全局技能根目录失败：%v", err)
	}
	linkPath := filepath.Join(globalDir, "ego-browser")
	if err := os.Symlink(sourceDir, linkPath); err != nil {
		t.Fatalf("创建目录符号链接失败：%v", err)
	}

	skills := DiscoverSkills(project, home)
	if len(skills) != 1 {
		t.Fatalf("期望发现 1 个技能，实际 %+v", skills)
	}
	if skills[0].Name != "ego-browser" || skills[0].Scope != SkillScopeGlobal {
		t.Fatalf("unexpected skill: %+v", skills[0])
	}
	// Path 记录未解析的原始路径，由调用方自行 EvalSymlinks。
	if want := filepath.Join(linkPath, "SKILL.md"); skills[0].Path != want {
		t.Fatalf("技能路径应为原始链接路径 %q，实际 %q", want, skills[0].Path)
	}
}

// 项目级技能目录来自不可信仓库，符号链接目录一律不跟随，避免扫到仓库外。
func TestDiscoverSkills_项目符号链接技能目录不被发现(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	t.Setenv("HOME", home)

	sourceDir := filepath.Join(t.TempDir(), "outside")
	writeSkill(t, filepath.Dir(sourceDir), "outside", "---\nname: outside\ndescription: 仓库外的技能\n---\n正文")
	projectDir := filepath.Join(project, ".agents", "skills")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("创建项目技能根目录失败：%v", err)
	}
	if err := os.Symlink(sourceDir, filepath.Join(projectDir, "outside")); err != nil {
		t.Fatalf("创建目录符号链接失败：%v", err)
	}

	if skills := DiscoverSkills(project, home); len(skills) != 0 {
		t.Fatalf("项目级符号链接技能目录应被跳过，实际 %+v", skills)
	}
}

// 目录符号链接指回上层会成环：扫描必须终止，且同一个技能不重复出现。
func TestDiscoverSkills_目录符号链接成环时可终止且不重复(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	t.Setenv("HOME", home)

	globalDir := filepath.Join(home, ".agents", "skills")
	writeSkill(t, globalDir, "loop-skill", "---\nname: loop-skill\ndescription: 成环技能\n---\n正文")
	// 技能目录内的链接指回技能根目录，形成 a -> 根 -> a 的环。
	if err := os.Symlink(globalDir, filepath.Join(globalDir, "loop-skill", "up")); err != nil {
		t.Fatalf("创建成环符号链接失败：%v", err)
	}

	skills := DiscoverSkills(project, home)
	if len(skills) != 1 {
		t.Fatalf("成环扫描应只得到 1 个技能，实际 %+v", skills)
	}
	if skills[0].Name != "loop-skill" {
		t.Fatalf("unexpected skill: %+v", skills[0])
	}
}

// 作用域决定 SKILL.md 本身的符号链接是否跟随：全局允许（用户自己安装），项目禁止。
func TestReadSkillFile_符号链接SKILL文件按作用域区分(t *testing.T) {
	root := t.TempDir()
	content := "---\nname: linked-file\ndescription: 链接的技能文件\n---\n正文"
	outside := filepath.Join(t.TempDir(), "real-skill.md")
	if err := os.WriteFile(outside, []byte(content), 0o644); err != nil {
		t.Fatalf("写入链接目标失败：%v", err)
	}
	skillDir := filepath.Join(root, "linked-file")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("创建技能目录失败：%v", err)
	}
	linkPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Fatalf("创建文件符号链接失败：%v", err)
	}

	global, ok := readSkillFile(linkPath, "linked-file", SkillScopeGlobal)
	if !ok {
		t.Fatal("全局作用域下符号链接的 SKILL.md 应可读取")
	}
	if global.Name != "linked-file" || global.Description != "链接的技能文件" || global.Path != linkPath {
		t.Fatalf("unexpected skill: %+v", global)
	}

	if _, ok := readSkillFile(linkPath, "linked-file", SkillScopeProject); ok {
		t.Fatal("项目作用域下符号链接的 SKILL.md 不应被读取")
	}
}
