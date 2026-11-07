package projctx

import (
	"path/filepath"
	"testing"
)

// TestIsSkillInjectedPath 覆盖：技能是否位于 mcode 注入提示词的根目录之下只按路径判定，
// 与设置页打的 Agent 标签无关；SKILL.md 与所属目录等价；前缀相似的同级目录不误判。
func TestIsSkillInjectedPath(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()

	cases := []struct {
		name       string
		projectDir string
		homeDir    string
		skillPath  string
		want       bool
	}{
		{
			name:      "全局 .agents/skills 下的技能目录",
			homeDir:   home,
			skillPath: filepath.Join(home, ".agents", "skills", "ego-browser"),
			want:      true,
		},
		{
			name:      "全局 .agents/skills 下的 SKILL.md",
			homeDir:   home,
			skillPath: filepath.Join(home, ".agents", "skills", "ego-browser", "SKILL.md"),
			want:      true,
		},
		{
			name:      "全局 .gemini/config/skills 下的技能",
			homeDir:   home,
			skillPath: filepath.Join(home, ".gemini", "config", "skills", "pptgen", "SKILL.md"),
			want:      true,
		},
		{
			name:      "全局 .claude/skills 由 Agent 自行加载",
			homeDir:   home,
			skillPath: filepath.Join(home, ".claude", "skills", "brainstorming", "SKILL.md"),
			want:      false,
		},
		{
			name:      "目录名前缀相似不误判",
			homeDir:   home,
			skillPath: filepath.Join(home, ".agents", "skills2", "fake", "SKILL.md"),
			want:      false,
		},
		{
			name:       "项目级 .agents/skills 下的技能",
			projectDir: project,
			homeDir:    home,
			skillPath:  filepath.Join(project, ".agents", "skills", "local-skill", "SKILL.md"),
			want:       true,
		},
		{
			name:       "项目级 skills 根目录下的技能",
			projectDir: project,
			homeDir:    home,
			skillPath:  filepath.Join(project, "skills", "inline", "SKILL.md"),
			want:       true,
		},
		{
			name:      "home 为空时跳过全局根目录",
			homeDir:   "",
			skillPath: filepath.Join(home, ".agents", "skills", "ego-browser", "SKILL.md"),
			want:      false,
		},
		{
			name:       "home 为空时项目级根目录仍生效",
			projectDir: project,
			homeDir:    "",
			skillPath:  filepath.Join(project, ".agents", "skills", "local-skill", "SKILL.md"),
			want:       true,
		},
		{
			name:       "项目目录之外的项目级路径不算注入",
			projectDir: project,
			homeDir:    home,
			skillPath:  filepath.Join(home, "elsewhere", ".agents", "skills", "x", "SKILL.md"),
			want:       false,
		},
		{
			name:      "空路径不算注入",
			homeDir:   home,
			skillPath: "",
			want:      false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsSkillInjectedPath(c.projectDir, c.homeDir, c.skillPath); got != c.want {
				t.Fatalf("IsSkillInjectedPath(%q, %q, %q) = %v, want %v",
					c.projectDir, c.homeDir, c.skillPath, got, c.want)
			}
		})
	}
}

// TestIsSkillInjectedPathFileAndDirEqual 覆盖：SKILL.md 文件路径与其所在技能目录路径判定一致。
func TestIsSkillInjectedPathFileAndDirEqual(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".agents", "skills", "demo")
	file := filepath.Join(dir, "SKILL.md")

	if !IsSkillInjectedPath("", home, dir) {
		t.Fatalf("技能目录应判定为注入路径：%s", dir)
	}
	if !IsSkillInjectedPath("", home, file) {
		t.Fatalf("SKILL.md 路径应与目录等价：%s", file)
	}
}
