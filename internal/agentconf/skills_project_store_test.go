package agentconf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// skillsProjectWriteFile 写测试文件并建好父目录；本文件自包含，不依赖其它测试文件里的辅助函数。
func skillsProjectWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 项目级开关写进数据目录的 skills-projects/<hash>.json，不再往用户仓库里写 .mcode/skills.json。
func TestProjectSkillStoreWritesOutsideRepo(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	dataDir := t.TempDir()
	opts := Options{Home: home, ProjectDir: proj, DataDir: dataDir}

	sk := Skill{Agent: "claude", Scope: "project", Name: "proj-skill"}
	if err := SetSkillEnabled(opts, sk, false); err != nil {
		t.Fatalf("停用项目技能失败：%v", err)
	}
	if IsSkillActive(opts, sk) {
		t.Fatal("项目技能应已停用")
	}

	// 仓库里不得出现 .mcode 目录。
	if _, err := os.Stat(filepath.Join(proj, ".mcode")); !os.IsNotExist(err) {
		t.Fatalf("不应在仓库里创建 .mcode：err=%v", err)
	}

	projPath := getSkillsConfigPath(opts, "project")
	if !strings.HasPrefix(projPath, filepath.Join(dataDir, "skills-projects")) {
		t.Fatalf("项目配置应落在数据目录 skills-projects 下，实际 %q", projPath)
	}
	base := strings.TrimSuffix(filepath.Base(projPath), ".json")
	if len(base) != 16 {
		t.Fatalf("文件名应为 16 位 hex，实际 %q", base)
	}
	for _, r := range base {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("文件名应只含 hex 字符，实际 %q", base)
		}
	}

	set, err := loadDisabledSkills(projPath)
	if err != nil {
		t.Fatalf("读取项目配置失败：%v", err)
	}
	if !set["claude:project:proj-skill"] {
		t.Fatalf("停用键应写进新的项目配置：%v", set)
	}

	// 不同项目互不影响，同一项目在不同工作副本下共用同一份文件。
	if other := getSkillsConfigPath(Options{Home: home, ProjectDir: t.TempDir(), DataDir: dataDir}, "project"); other == projPath {
		t.Fatal("不同项目应使用不同的配置文件")
	}
	if same := getSkillsConfigPath(Options{Home: home, ProjectDir: proj, DataDir: dataDir}, "project"); same != projPath {
		t.Fatalf("同一项目应解析到同一路径：%q vs %q", same, projPath)
	}
}

// DataDir 为空时按全局路径同样的规则回退到 <home>/.mcode。
func TestProjectSkillStoreDataDirFallback(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	t.Setenv("MCODE_DATA", "")

	path := getSkillsConfigPath(Options{Home: home, ProjectDir: proj}, "project")
	if !strings.HasPrefix(path, filepath.Join(home, ".mcode", "skills-projects")) {
		t.Fatalf("DataDir 为空时应回退 <home>/.mcode，实际 %q", path)
	}
}

// 旧版写在仓库里的项目配置继续生效（只读兼容），重新启用时从旧文件里删掉该键。
func TestProjectSkillLegacyFileCompat(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	dataDir := t.TempDir()
	opts := Options{Home: home, ProjectDir: proj, DataDir: dataDir}

	legacyPath := filepath.Join(proj, ".mcode", "skills.json")
	skillsProjectWriteFile(t, legacyPath, `{"disabled": ["claude:project:legacy-skill", "claude:project:keep-skill"]}`)

	legacy := Skill{Agent: "claude", Scope: "project", Name: "legacy-skill"}
	keep := Skill{Agent: "claude", Scope: "project", Name: "keep-skill"}
	if IsSkillActive(opts, legacy) {
		t.Fatal("旧文件里的停用记录应继续生效")
	}

	// 扫描也按旧文件把技能标成 Disabled。
	writeSkillTestDir(t, filepath.Join(proj, ".claude", "skills"), "legacy-skill")
	skills, err := ScanSkills(opts)
	if err != nil {
		t.Fatalf("ScanSkills 失败：%v", err)
	}
	if len(skills) != 1 || !skills[0].Disabled {
		t.Fatalf("旧文件里的停用记录应作用于扫描结果：%+v", skills)
	}

	// 启用时既要清掉新路径（本来没有），也要把旧文件里的键删掉。
	if err := SetSkillEnabled(opts, legacy, true); err != nil {
		t.Fatalf("启用失败：%v", err)
	}
	if !IsSkillActive(opts, legacy) {
		t.Fatal("启用后旧记录不应再生效")
	}
	legacySet, err := loadDisabledSkills(legacyPath)
	if err != nil {
		t.Fatalf("读取旧配置失败：%v", err)
	}
	if legacySet["claude:project:legacy-skill"] {
		t.Fatalf("旧文件里的目标键应已删除：%v", legacySet)
	}
	if !legacySet["claude:project:keep-skill"] {
		t.Fatalf("旧文件里的其它键应保留：%v", legacySet)
	}
	if IsSkillActive(opts, keep) {
		t.Fatal("旧文件里保留的键应继续停用 keep-skill")
	}
}

// 旧文件不存在时启用不得凭空创建它。
func TestProjectSkillLegacyFileNotCreated(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	dataDir := t.TempDir()
	opts := Options{Home: home, ProjectDir: proj, DataDir: dataDir}

	sk := Skill{Agent: "claude", Scope: "project", Name: "fresh-skill"}
	if err := SetSkillEnabled(opts, sk, true); err != nil {
		t.Fatalf("启用失败：%v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, ".mcode", "skills.json")); !os.IsNotExist(err) {
		t.Fatalf("旧文件不存在时不应创建：err=%v", err)
	}
}

// 旧文件损坏时启用仍应成功，只是无法清理旧记录，不能因此报错挡住开关。
func TestProjectSkillLegacyFileCorruptIgnored(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	dataDir := t.TempDir()
	opts := Options{Home: home, ProjectDir: proj, DataDir: dataDir}

	legacyPath := filepath.Join(proj, ".mcode", "skills.json")
	skillsProjectWriteFile(t, legacyPath, `{"disabled": [`)

	sk := Skill{Agent: "claude", Scope: "project", Name: "foo"}
	if err := SetSkillEnabled(opts, sk, true); err != nil {
		t.Fatalf("旧文件损坏不应挡住启用：%v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, ".mcode", "skills.json")); err != nil {
		t.Fatalf("损坏的旧文件不应被删除或改写：%v", err)
	}
}

// parseFrontmatter：嵌套键里的 name 不能覆盖顶层 name；顶层多行普通标量按单空格拼接。
func TestParseFrontmatterNestedAndMultiline(t *testing.T) {
	content := `---
name: real-name
metadata:
  name: nested-name
  description: 嵌套描述
description: 第一行说明
  第二行继续
  第三行收尾
license: MIT
---
正文
`
	name, desc := parseFrontmatter(content, "fallback")
	if name != "real-name" {
		t.Fatalf("嵌套的 name 不应覆盖顶层 name，实际 %q", name)
	}
	if desc != "第一行说明 第二行继续 第三行收尾" {
		t.Fatalf("多行普通标量应拼成一行，实际 %q", desc)
	}
}

// 普通标量续行遇到下一个顶层键就停下，不会把后面的键值吞进描述。
func TestParseFrontmatterMultilineStopsAtNextKey(t *testing.T) {
	content := `---
description: 首行
  续行
name: after-key
---
`
	name, desc := parseFrontmatter(content, "fallback")
	if name != "after-key" {
		t.Fatalf("续行后的顶层键仍应解析，实际 name=%q", name)
	}
	if desc != "首行 续行" {
		t.Fatalf("多行普通标量应只含续行，实际 %q", desc)
	}
}

// 块标量指示符仍然按块标量处理，不会被多行普通标量规则吃掉；引号值也不受影响。
func TestParseFrontmatterBlockAndQuoteUnchanged(t *testing.T) {
	blocked := "---\nname: b\ndescription: >\n  一行\n  二行\n---\n"
	if name, desc := parseFrontmatter(blocked, "fallback"); name != "b" || desc != "一行 二行" {
		t.Fatalf("块标量解析变化：name=%q desc=%q", name, desc)
	}

	quoted := "---\nname: q\ndescription: \"带 # 井号的说明\"\n---\n"
	if _, desc := parseFrontmatter(quoted, "fallback"); desc != "带 # 井号的说明" {
		t.Fatalf("引号值不应被注释剥离规则破坏：%q", desc)
	}

	plain := "---\nname: p\ndescription: 说明 # 注释\n---\n"
	if _, desc := parseFrontmatter(plain, "fallback"); desc != "说明" {
		t.Fatalf("未加引号值的行尾注释应剥离：%q", desc)
	}
}
