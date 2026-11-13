package projctx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/testenv"
)

func writeSkill(t *testing.T, dir, folder, content string) {
	t.Helper()
	skillDir := filepath.Join(dir, folder)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseSkillFrontmatter(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		wantName string
		wantDesc string
	}{
		{
			name:     "标准 frontmatter",
			content:  "---\nname: pdf-document\ndescription: Produce polished PDF documents.\n---\n\n正文",
			wantName: "pdf-document",
			wantDesc: "Produce polished PDF documents.",
		},
		{
			name:     "带引号与额外字段",
			content:  "---\ntitle: 标题\nname: \"quoted-name\"\ndescription: '单引号描述'\nversion: 1\n---\n正文",
			wantName: "quoted-name",
			wantDesc: "单引号描述",
		},
		{
			name:     "没有 frontmatter",
			content:  "# 只有正文\n没有元数据",
			wantName: "",
			wantDesc: "",
		},
		{
			name:     "frontmatter 未闭合",
			content:  "---\nname: broken\ndescription: 未闭合",
			wantName: "broken",
			wantDesc: "未闭合",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, description := parseSkillFrontmatter(tc.content)
			if name != tc.wantName || description != tc.wantDesc {
				t.Fatalf("parseSkillFrontmatter = (%q, %q), want (%q, %q)", name, description, tc.wantName, tc.wantDesc)
			}
		})
	}
}

func TestParseSkillFrontmatterMultiline(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		wantName string
		wantDesc string
	}{
		{
			name:     "折叠块标量",
			content:  "---\nname: fold-skill\ndescription: >\n  Produce polished\n  PDF documents.\n---\n正文",
			wantName: "fold-skill",
			wantDesc: "Produce polished PDF documents.",
		},
		{
			name:     "字面块标量",
			content:  "---\nname: literal-skill\ndescription: |\n  line one\n  line two\n---\n正文",
			wantName: "literal-skill",
			wantDesc: "line one\nline two",
		},
		{
			name:     "折叠块标量去尾",
			content:  "---\nname: strip-skill\ndescription: >-\n  first part\n  second part\n\n---\n正文",
			wantName: "strip-skill",
			wantDesc: "first part second part",
		},
		{
			name:     "续行中的冒号不作为 key",
			content:  "---\nname: colon-skill\ndescription: >\n  note: keep this\n  foo: bar\nname: tampered\n---\n正文",
			wantName: "colon-skill",
			wantDesc: "note: keep this foo: bar",
		},
		{
			name:     "单行引号值",
			content:  "---\nname: plain-skill\ndescription: \"hello\"\n---\n正文",
			wantName: "plain-skill",
			wantDesc: "hello",
		},
		{
			name:     "空值加续行按折叠处理",
			content:  "---\nname: implicit-skill\ndescription:\n  joined by spaces\n  from two lines\n---\n正文",
			wantName: "implicit-skill",
			wantDesc: "joined by spaces from two lines",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, description := parseSkillFrontmatter(tc.content)
			if name != tc.wantName || description != tc.wantDesc {
				t.Fatalf("parseSkillFrontmatter = (%q, %q), want (%q, %q)", name, description, tc.wantName, tc.wantDesc)
			}
		})
	}
}

func TestBuildSkillsGuidance_NoSkills(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	testenv.SetHome(t, home)

	if guidance := BuildSkillsGuidance(project, home); guidance != "" {
		t.Fatalf("expected empty guidance without skills, got %q", guidance)
	}
}

func TestBuildSkillsGuidance_GlobalAndProjectOverride(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	testenv.SetHome(t, home)

	globalDir := filepath.Join(home, ".agents", "skills")
	writeSkill(t, globalDir, "alpha", "---\nname: alpha\ndescription: 全局 alpha\n---\n正文")
	writeSkill(t, globalDir, "beta", "---\nname: beta\ndescription: 全局 beta\n---\n正文")
	// 项目级 alpha 同名覆盖全局，delta 仅项目级存在。
	projectDir := filepath.Join(project, ".agents", "skills")
	writeSkill(t, projectDir, "alpha", "---\nname: alpha\ndescription: 项目 alpha\n---\n正文")
	writeSkill(t, projectDir, "delta", "---\nname: delta\ndescription: 项目 delta\n---\n正文")

	guidance := BuildSkillsGuidance(project, home)
	if !strings.HasPrefix(guidance, "[mcode 已启用技能]") {
		t.Fatalf("guidance should start with the header, got %q", guidance)
	}
	for _, want := range []string{
		"- alpha（项目）: 项目 alpha (path: " + filepath.Join(projectDir, "alpha", "SKILL.md") + ")",
		"- delta（项目）: 项目 delta (path: " + filepath.Join(projectDir, "delta", "SKILL.md") + ")",
		"- beta（全局）: 全局 beta (path: " + filepath.Join(globalDir, "beta", "SKILL.md") + ")",
	} {
		if !strings.Contains(guidance, want) {
			t.Fatalf("guidance missing %q:\n%s", want, guidance)
		}
	}
	if strings.Contains(guidance, "全局 alpha") {
		t.Fatalf("project skill should override the global one:\n%s", guidance)
	}
	// 项目级技能排在全局之前。
	if strings.Index(guidance, "（项目）") > strings.Index(guidance, "（全局）") {
		t.Fatalf("project skills should come before global ones:\n%s", guidance)
	}
}

// 停用的项目级技能被剔除后，同名的全局技能不得顶上来；过滤作用于同名合并之后。
func TestBuildSkillsGuidanceExcluding_DropsProjectSkillWithoutGlobalFallback(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	testenv.SetHome(t, home)

	globalDir := filepath.Join(home, ".agents", "skills")
	writeSkill(t, globalDir, "alpha", "---\nname: alpha\ndescription: 全局 alpha\n---\n正文")
	writeSkill(t, globalDir, "beta", "---\nname: beta\ndescription: 全局 beta\n---\n正文")
	projectDir := filepath.Join(project, ".agents", "skills")
	writeSkill(t, projectDir, "alpha", "---\nname: alpha\ndescription: 项目 alpha\n---\n正文")

	var projectAlphaPath string
	for _, skill := range DiscoverSkills(project, home) {
		if skill.Name == "alpha" {
			projectAlphaPath = skill.Path
		}
	}
	if projectAlphaPath != filepath.Join(projectDir, "alpha", "SKILL.md") {
		t.Fatalf("unexpected project alpha path: %q", projectAlphaPath)
	}

	excluded := func(path string) bool { return path == projectAlphaPath }
	guidance := BuildSkillsGuidanceExcluding(project, home, excluded)
	if strings.Contains(guidance, "alpha") {
		t.Fatalf("被剔除的项目级技能不应让同名全局技能顶上来：\n%s", guidance)
	}
	if !strings.Contains(guidance, "- beta（全局）: 全局 beta") {
		t.Fatalf("未剔除的技能应保留：\n%s", guidance)
	}

	// 全部剔除时返回空串。
	if all := BuildSkillsGuidanceExcluding(project, home, func(string) bool { return true }); all != "" {
		t.Fatalf("技能被全部剔除时应返回空串，实际 %q", all)
	}
}

// excluded 为 nil 时与 BuildSkillsGuidance 完全一致。
func TestBuildSkillsGuidanceExcluding_NilMatchesDefault(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	testenv.SetHome(t, home)

	writeSkill(t, filepath.Join(home, ".agents", "skills"), "gamma", "---\nname: gamma\ndescription: 全局 gamma\n---\n正文")
	writeSkill(t, filepath.Join(project, "skills"), "delta", "---\nname: delta\ndescription: 项目 delta\n---\n正文")

	want := BuildSkillsGuidance(project, home)
	if want == "" {
		t.Fatal("前置条件失败：应扫描到技能")
	}
	if got := BuildSkillsGuidanceExcluding(project, home, nil); got != want {
		t.Fatalf("excluded 为 nil 时输出应与 BuildSkillsGuidance 一致：\ngot  %q\nwant %q", got, want)
	}
}

// pi 的两个专属技能目录（全局 .pi/agent/skills、项目级 .pi/skills）也要被扫描到，
// 并认定为会注入提示词的路径。
func TestDiscoverSkillsIncludesPiRoots(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	testenv.SetHome(t, home)

	globalDir := filepath.Join(home, ".pi", "agent", "skills")
	writeSkill(t, globalDir, "pi-global", "---\nname: pi-global\ndescription: pi 全局技能\n---\n正文")
	projectDir := filepath.Join(project, ".pi", "skills")
	writeSkill(t, projectDir, "pi-project", "---\nname: pi-project\ndescription: pi 项目技能\n---\n正文")

	found := map[string]SkillInfo{}
	for _, skill := range DiscoverSkills(project, home) {
		found[skill.Name] = skill
	}

	if got, ok := found["pi-global"]; !ok || got.Scope != SkillScopeGlobal {
		t.Fatalf("应扫描到全局 pi 技能且 scope 为全局，实际 %+v", found["pi-global"])
	}
	if got, ok := found["pi-project"]; !ok || got.Scope != SkillScopeProject {
		t.Fatalf("应扫描到项目级 pi 技能且 scope 为项目，实际 %+v", found["pi-project"])
	}

	for _, path := range []string{
		filepath.Join(globalDir, "pi-global", "SKILL.md"),
		filepath.Join(projectDir, "pi-project", "SKILL.md"),
	} {
		if !IsSkillInjectedPath(project, home, path) {
			t.Fatalf("pi 技能应判定为注入路径：%s", path)
		}
	}
}

func TestDiscoverSkills_FallsBackToFolderName(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	testenv.SetHome(t, home)

	globalDir := filepath.Join(home, ".agents", "skills")
	writeSkill(t, globalDir, "fallback", "没有 frontmatter 的技能")

	skills := DiscoverSkills(project, home)
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Name != "fallback" || skills[0].Scope != SkillScopeGlobal {
		t.Fatalf("unexpected skill: %+v", skills[0])
	}
}

// 技能文件是符号链接时不得跟随读取：仓库里放一个指向仓库外私钥的链接，
// 就能把任意文件内容当 description 拼进提示词。
func TestDiscoverSkillsIgnoresSymlinkedSkillFile(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	testenv.SetHome(t, home)

	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("-----BEGIN PRIVATE KEY-----"), 0o600); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(project, ".agents", "skills", "linked")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(skillDir, "SKILL.md")); err != nil {
		t.Fatalf("创建符号链接失败：%v", err)
	}

	if skills := DiscoverSkills(project, home); len(skills) != 0 {
		t.Fatalf("符号链接的技能文件应被跳过，实际 %+v", skills)
	}
	if guidance := BuildSkillsGuidance(project, home); strings.Contains(guidance, "PRIVATE KEY") {
		t.Fatalf("不得把链接目标读进提示词：%s", guidance)
	}
}
