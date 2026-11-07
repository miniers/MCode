package agentconf

import (
	"os"
	"path/filepath"
	"testing"
)

// pluginSkillWriteFile 写测试文件并建好父目录；本文件自包含，不依赖其它测试文件里的辅助函数。
func pluginSkillWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// pluginSkillTestRoot 造一个已安装的插件目录，含一个技能与可选的 plugin.json。
func pluginSkillTestRoot(t *testing.T, home, pluginID, skillDir, skillName string, manifest string) string {
	t.Helper()
	root := filepath.Join(home, ".claude", "plugins", "cache", "market", pluginID)
	skillPath := filepath.Join(root, skillDir, skillName)
	if err := os.MkdirAll(skillPath, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + skillName + "\ndescription: 插件技能 " + skillName + "\n---\n正文"
	if err := os.WriteFile(filepath.Join(skillPath, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if manifest != "" {
		if err := os.MkdirAll(filepath.Join(root, ".claude-plugin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".claude-plugin", "plugin.json"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// 已启用插件自带的技能要出现在扫描结果里：Agent 为 claude、Scope 为 external、Plugin 为插件名；
// 未启用的插件技能不出现。
func TestScanPluginSkillsOnlyEnabled(t *testing.T) {
	home := t.TempDir()
	onRoot := pluginSkillTestRoot(t, home, "on", "skills", "on-skill", "")
	offRoot := pluginSkillTestRoot(t, home, "off", "skills", "off-skill", "")

	installed := `{"plugins": {
  "on@market": [{"installPath": "` + onRoot + `"}],
  "off@market": [{"installPath": "` + offRoot + `"}]
}}`
	pluginSkillWriteFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), installed)
	pluginSkillWriteFile(t, filepath.Join(home, ".claude", "settings.json"),
		`{"enabledPlugins": {"on@market": true, "off@market": false}}`)

	skills, err := ScanSkills(Options{Home: home, DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("ScanSkills 失败：%v", err)
	}

	var pluginSkills []Skill
	for _, sk := range skills {
		if sk.Scope == "external" {
			pluginSkills = append(pluginSkills, sk)
		}
	}
	if len(pluginSkills) != 1 {
		t.Fatalf("应只扫到 1 条插件技能，实际 %d：%+v", len(pluginSkills), skills)
	}
	sk := pluginSkills[0]
	if sk.Agent != "claude" || sk.Scope != "external" || sk.Plugin != "on" || sk.Name != "on-skill" {
		t.Fatalf("插件技能字段不对：%+v", sk)
	}
	if !sk.Enabled || sk.Disabled {
		t.Fatalf("默认应为启用状态：%+v", sk)
	}
}

// settings.json 缺失时回退用 installed_plugins.json 的 enabledPlugins。
func TestScanPluginSkillsEnabledFallback(t *testing.T) {
	home := t.TempDir()
	root := pluginSkillTestRoot(t, home, "solo", "skills", "solo-skill", "")
	pluginSkillWriteFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"),
		`{"plugins": {"solo@m": [{"installPath": "`+root+`"}]}, "enabledPlugins": {"solo@m": true}}`)

	skills, err := ScanSkills(Options{Home: home, DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("ScanSkills 失败：%v", err)
	}
	found := false
	for _, sk := range skills {
		if sk.Plugin == "solo" && sk.Name == "solo-skill" {
			found = true
		}
	}
	if !found {
		t.Fatalf("installed_plugins.json 的 enabledPlugins 应作为回退依据：%+v", skills)
	}
}

// plugin.json 里字符串类型的 skills 字段指定技能目录时按它扫描，默认目录里的技能不再出现。
func TestScanPluginSkillsCustomDir(t *testing.T) {
	home := t.TempDir()
	root := pluginSkillTestRoot(t, home, "custom", "custom-skills", "custom-skill", `{"skills": "./custom-skills/"}`)
	// 默认 skills 目录里也放一个，应该扫不到。
	defaultDir := filepath.Join(root, "skills", "default-skill")
	if err := os.MkdirAll(defaultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(defaultDir, "SKILL.md"),
		[]byte("---\nname: default-skill\ndescription: 默认目录不应被扫描\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pluginSkillWriteFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"),
		`{"plugins": {"custom@m": [{"installPath": "`+root+`"}]}, "enabledPlugins": {"custom@m": true}}`)

	skills, err := ScanSkills(Options{Home: home, DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("ScanSkills 失败：%v", err)
	}
	var names []string
	for _, sk := range skills {
		if sk.Scope == "external" {
			names = append(names, sk.Name)
		}
	}
	if len(names) != 1 || names[0] != "custom-skill" {
		t.Fatalf("应按 plugin.json 的 skills 字段扫描，实际：%v", names)
	}
}

// 插件技能的停用键为 claude:external:<插件>:<名字>，且不匹配旧的纯名字键。
func TestPluginSkillDisabledKey(t *testing.T) {
	sk := Skill{Agent: "claude", Scope: "external", Plugin: "demo", Name: "foo"}
	if got, want := skillDisabledKey(sk), "claude:external:demo:foo"; got != want {
		t.Fatalf("停用键 = %q，期望 %q", got, want)
	}

	// 旧纯名字条目不应停用插件技能。
	legacy := map[string]bool{"foo": true}
	if isSkillDisabledIn(legacy, sk) {
		t.Fatal("旧名字键不应影响插件技能")
	}
	// 一样的名字但只是普通 claude 技能，旧名字键仍然生效。
	plain := Skill{Agent: "claude", Scope: "global", Name: "foo"}
	if !isSkillDisabledIn(legacy, plain) {
		t.Fatal("旧名字键应对普通技能生效")
	}
	// 插件之间互不影响。
	other := Skill{Agent: "claude", Scope: "external", Plugin: "other", Name: "foo"}
	if isSkillDisabledIn(map[string]bool{"claude:external:demo:foo": true}, other) {
		t.Fatal("不同插件的同名技能不应互相影响")
	}
}

// 设置页停用插件技能后，扫描结果里该技能应为 Disabled，其他插件的同名技能不受影响。
func TestSetPluginSkillDisabled(t *testing.T) {
	home := t.TempDir()
	rootA := pluginSkillTestRoot(t, home, "a", "skills", "shared", "")
	rootB := pluginSkillTestRoot(t, home, "b", "skills", "shared", "")
	pluginSkillWriteFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"),
		`{"plugins": {"a@m": [{"installPath": "`+rootA+`"}], "b@m": [{"installPath": "`+rootB+`"}]},
  "enabledPlugins": {"a@m": true, "b@m": true}}`)

	opts := Options{Home: home, DataDir: t.TempDir()}
	skills, err := ScanSkills(opts)
	if err != nil {
		t.Fatalf("ScanSkills 失败：%v", err)
	}
	var target Skill
	for _, sk := range skills {
		if sk.Plugin == "a" {
			target = sk
		}
	}
	if target.Name == "" {
		t.Fatalf("未扫到插件 a 的技能：%+v", skills)
	}

	if err := SetSkillEnabled(opts, target, false); err != nil {
		t.Fatalf("停用插件技能失败：%v", err)
	}
	if IsSkillActive(opts, target) {
		t.Fatal("插件技能应已停用")
	}

	set, err := loadDisabledSkills(filepath.Join(opts.DataDir, "skills.json"))
	if err != nil {
		t.Fatalf("读取技能配置失败：%v", err)
	}
	if !set["claude:external:a:shared"] {
		t.Fatalf("停用键应写进全局 skills.json：%v", set)
	}

	skills, err = ScanSkills(opts)
	if err != nil {
		t.Fatalf("ScanSkills 失败：%v", err)
	}
	for _, sk := range skills {
		if sk.Scope != "external" {
			continue
		}
		switch sk.Plugin {
		case "a":
			if !sk.Disabled || sk.Enabled {
				t.Fatalf("插件 a 的技能应为 Disabled：%+v", sk)
			}
		case "b":
			if sk.Disabled || !sk.Enabled {
				t.Fatalf("插件 b 的同名技能不应受影响：%+v", sk)
			}
		}
	}
}

// 排序：同 Agent 同 Scope 内先按插件名再按技能名。
func TestSortSkillsByPlugin(t *testing.T) {
	skills := []Skill{
		{Agent: "claude", Scope: "external", Plugin: "b", Name: "a"},
		{Agent: "claude", Scope: "external", Plugin: "a", Name: "z"},
		{Agent: "claude", Scope: "external", Plugin: "a", Name: "a"},
	}
	sortSkills(skills)
	want := []string{"a/a", "a/z", "b/a"}
	for i, sk := range skills {
		if got := sk.Plugin + "/" + sk.Name; got != want[i] {
			t.Fatalf("第 %d 项 = %s，期望 %s", i, got, want[i])
		}
	}
}

// 插件配置文件损坏或缺失时静默跳过，不影响普通技能扫描与错误返回。
func TestPluginSkillsBrokenConfigIgnored(t *testing.T) {
	home := t.TempDir()
	pluginSkillWriteFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), `{"plugins": {`)
	writeSkillTestDir(t, filepath.Join(home, ".claude", "skills"), "plain-skill")

	skills, err := ScanSkills(Options{Home: home, DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("插件配置损坏不应让 ScanSkills 报错：%v", err)
	}
	if len(skills) != 1 || skills[0].Name != "plain-skill" {
		t.Fatalf("应只扫到普通技能：%+v", skills)
	}
}

// writeSkillTestDir 在 root 下造一个只有一个 SKILL.md 的技能目录。
func writeSkillTestDir(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + name + "\n---\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
