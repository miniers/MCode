package agentconf

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mcode/internal/config"
)

func TestEmptyConfig(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()

	skills, err := ScanSkills(Options{Home: home, ProjectDir: proj})
	if err != nil {
		t.Fatalf("expected nil error on empty config, got: %v", err)
	}
	if len(skills) != 0 {
		t.Fatalf("expected 0 skills, got: %d", len(skills))
	}

	servers, err := ScanMCP(Options{Home: home, ProjectDir: proj})
	if err != nil {
		t.Fatalf("expected nil error on empty config, got: %v", err)
	}
	if len(servers) != 0 {
		t.Fatalf("expected 0 servers, got: %d", len(servers))
	}

	// 测试 ProjectDir 为空时只扫全局
	skillsGlobal, err := ScanSkills(Options{Home: home})
	if err != nil || len(skillsGlobal) != 0 {
		t.Fatalf("expected 0 skills and nil err, got err=%v len=%d", err, len(skillsGlobal))
	}

	serversGlobal, err := ScanMCP(Options{Home: home})
	if err != nil || len(serversGlobal) != 0 {
		t.Fatalf("expected 0 servers and nil err, got err=%v len=%d", err, len(serversGlobal))
	}
}

func TestScanSkills(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()

	// 1. claude 全局 skill
	claudeGlobalSkillDir := filepath.Join(home, ".claude", "skills", "claude-global-skill")
	if err := os.MkdirAll(claudeGlobalSkillDir, 0755); err != nil {
		t.Fatal(err)
	}
	claudeGlobalSkillMD := filepath.Join(claudeGlobalSkillDir, "SKILL.md")
	if err := os.WriteFile(claudeGlobalSkillMD, []byte(`---
name: claude-global-skill
description: "Claude Global Skill Description"
---
# Skill Content
`), 0644); err != nil {
		t.Fatal(err)
	}

	// 2. claude 项目 skill：缺 name，回退目录名
	claudeProjSkillDir := filepath.Join(proj, ".claude", "skills", "fallback-name-skill")
	if err := os.MkdirAll(claudeProjSkillDir, 0755); err != nil {
		t.Fatal(err)
	}
	claudeProjSkillMD := filepath.Join(claudeProjSkillDir, "SKILL.md")
	if err := os.WriteFile(claudeProjSkillMD, []byte(`---
description: 'Fallback Name Project Skill'
---
# Content
`), 0644); err != nil {
		t.Fatal(err)
	}

	// 3. codex 全局 skills: 块标量测试与去重
	// 3a. '>' 块标量
	gtSkillDir := filepath.Join(home, ".codex", "skills", "block-gt-dir")
	if err := os.MkdirAll(gtSkillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gtSkillDir, "SKILL.md"), []byte(`---
name: block-gt
description: >
  Line one
  Line two
---
`), 0644); err != nil {
		t.Fatal(err)
	}

	// 3b. '>-' 块标量
	gtStripSkillDir := filepath.Join(home, ".codex", "skills", "block-gt-strip-dir")
	if err := os.MkdirAll(gtStripSkillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gtStripSkillDir, "SKILL.md"), []byte(`---
name: block-gt-strip
description: >-
  First line
  Second line
---
`), 0644); err != nil {
		t.Fatal(err)
	}

	// 3c. '|' 块标量
	pipeSkillDir := filepath.Join(home, ".agents", "skills", "block-pipe-dir")
	if err := os.MkdirAll(pipeSkillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pipeSkillDir, "SKILL.md"), []byte(`---
name: block-pipe
description: |
  Header line
    Indented line
---
`), 0644); err != nil {
		t.Fatal(err)
	}

	// 3d. '|-' 块标量
	pipeStripSkillDir := filepath.Join(home, ".agents", "skills", "block-pipe-strip-dir")
	if err := os.MkdirAll(pipeStripSkillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pipeStripSkillDir, "SKILL.md"), []byte(`---
name: block-pipe-strip
description: |-
  Alpha
  Beta
---
`), 0644); err != nil {
		t.Fatal(err)
	}

	// 3e. 超长 description（> 300 rune，截断并加 …）
	longDescSkillDir := filepath.Join(home, ".agents", "skills", "long-desc-dir")
	if err := os.MkdirAll(longDescSkillDir, 0755); err != nil {
		t.Fatal(err)
	}
	longText := strings.Repeat("字", 350)
	if err := os.WriteFile(filepath.Join(longDescSkillDir, "SKILL.md"), []byte(fmt.Sprintf(`---
name: long-desc
description: "%s"
---
`, longText)), 0644); err != nil {
		t.Fatal(err)
	}

	// 3f. 同一 Agent+Scope 去重测试（先出现的保留）
	dupSkillDir1 := filepath.Join(home, ".codex", "skills", "dup-skill-dir")
	if err := os.MkdirAll(dupSkillDir1, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dupSkillDir1, "SKILL.md"), []byte(`---
name: dup-skill
description: First occurrence
---
`), 0644); err != nil {
		t.Fatal(err)
	}

	dupSkillDir2 := filepath.Join(home, ".agents", "skills", "dup-skill-dir-ignored")
	if err := os.MkdirAll(dupSkillDir2, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dupSkillDir2, "SKILL.md"), []byte(`---
name: dup-skill
description: Second occurrence should be ignored
---
`), 0644); err != nil {
		t.Fatal(err)
	}

	// 4. codex 项目 skill：符号链接目录测试
	externalSkillDir := filepath.Join(proj, "external-skills", "target-skill")
	if err := os.MkdirAll(externalSkillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(externalSkillDir, "SKILL.md"), []byte(`---
name: symlinked-skill
description: Skill resolved through symlink
---
`), 0644); err != nil {
		t.Fatal(err)
	}

	agentsProjectSkillsDir := filepath.Join(proj, ".agents", "skills")
	if err := os.MkdirAll(agentsProjectSkillsDir, 0755); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(agentsProjectSkillsDir, "symlink-skill")
	if err := os.Symlink(externalSkillDir, symlinkPath); err != nil {
		t.Fatal(err)
	}

	skills, err := ScanSkills(Options{Home: home, ProjectDir: proj})
	if err != nil {
		t.Fatalf("ScanSkills error: %v", err)
	}

	// 验证数量：
	// claude global: 1 (claude-global-skill)
	// claude project: 1 (fallback-name-skill)
	// codex global: 6 (block-gt, block-gt-strip, block-pipe, block-pipe-strip, long-desc, dup-skill)
	// codex project: 1 (symlinked-skill)
	// 总共 9 个
	if len(skills) != 9 {
		t.Fatalf("expected 9 skills, got %d", len(skills))
	}

	skillMap := make(map[string]Skill)
	for _, s := range skills {
		key := s.Agent + ":" + s.Scope + ":" + s.Name
		skillMap[key] = s
	}

	// 验证缺 name 回退
	if s, ok := skillMap["claude:project:fallback-name-skill"]; !ok {
		t.Errorf("missing fallback-name-skill")
	} else if s.Description != "Fallback Name Project Skill" {
		t.Errorf("unexpected description: %q", s.Description)
	}

	// 验证 '>' 块标量
	if s, ok := skillMap["codex:global:block-gt"]; !ok {
		t.Errorf("missing block-gt")
	} else if s.Description != "Line one Line two" {
		t.Errorf("unexpected block-gt desc: %q", s.Description)
	}

	// 验证 '>-' 块标量
	if s, ok := skillMap["codex:global:block-gt-strip"]; !ok {
		t.Errorf("missing block-gt-strip")
	} else if s.Description != "First line Second line" {
		t.Errorf("unexpected block-gt-strip desc: %q", s.Description)
	}

	// 验证 '|' 块标量
	if s, ok := skillMap["codex:global:block-pipe"]; !ok {
		t.Errorf("missing block-pipe")
	} else if s.Description != "Header line\n  Indented line" {
		t.Errorf("unexpected block-pipe desc: %q", s.Description)
	}

	// 验证 '|-' 块标量
	if s, ok := skillMap["codex:global:block-pipe-strip"]; !ok {
		t.Errorf("missing block-pipe-strip")
	} else if s.Description != "Alpha\nBeta" {
		t.Errorf("unexpected block-pipe-strip desc: %q", s.Description)
	}

	// 验证超长截断
	if s, ok := skillMap["codex:global:long-desc"]; !ok {
		t.Errorf("missing long-desc")
	} else {
		runes := []rune(s.Description)
		if len(runes) != 301 || !strings.HasSuffix(s.Description, "…") {
			t.Errorf("expected 301 runes ending with …, got length %d: %q", len(runes), s.Description)
		}
	}

	// 验证去重
	if s, ok := skillMap["codex:global:dup-skill"]; !ok {
		t.Errorf("missing dup-skill")
	} else if s.Description != "First occurrence" {
		t.Errorf("expected first occurrence to be preserved, got %q", s.Description)
	}

	// 验证符号链接
	if s, ok := skillMap["codex:project:symlinked-skill"]; !ok {
		t.Errorf("missing symlinked-skill")
	} else if !filepath.IsAbs(s.Path) || !strings.HasSuffix(s.Path, "SKILL.md") {
		t.Errorf("expected absolute path to SKILL.md, got %q", s.Path)
	}

	// 验证排序：Agent (claude < codex), Scope (global < project), Name
	for i := 1; i < len(skills); i++ {
		prev := skills[i-1]
		curr := skills[i]
		if prev.Agent > curr.Agent {
			t.Errorf("ordering violation on Agent at index %d: %s > %s", i, prev.Agent, curr.Agent)
		} else if prev.Agent == curr.Agent {
			if prev.Scope == "project" && curr.Scope == "global" {
				t.Errorf("ordering violation on Scope at index %d: project before global", i)
			} else if prev.Scope == curr.Scope {
				if prev.Name > curr.Name {
					t.Errorf("ordering violation on Name at index %d: %s > %s", i, prev.Name, curr.Name)
				}
			}
		}
	}
}

func TestScanMCP(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	absProj, err := filepath.Abs(proj)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Home/.claude.json：全局与 projects 段
	claudeJSONContent := fmt.Sprintf(`{
  "mcpServers": {
    "global-stdio-server": {
      "command": "node",
      "args": ["server.js", "--flag"],
      "env": {
        "Z_KEY": "valZ",
        "A_KEY": "valA",
        "M_KEY": 123
      }
    }
  },
  "projects": {
    %s: {
      "mcpServers": {
        "proj-sse-server": {
          "type": "sse",
          "url": "https://example.com/sse",
          "headers": {
            "Authorization": "Bearer token"
          }
        }
      }
    }
  }
}`, mustMarshalJSON(absProj))

	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(claudeJSONContent), 0644); err != nil {
		t.Fatal(err)
	}

	// 2. ProjectDir/.mcp.json：url 推断 http
	mcpJSONContent := `{
  "mcpServers": {
    "proj-url-server": {
      "url": "http://localhost:3000/mcp"
    }
  }
}`
	if err := os.WriteFile(filepath.Join(proj, ".mcp.json"), []byte(mcpJSONContent), 0644); err != nil {
		t.Fatal(err)
	}

	// 3. Home/.codex/config.toml：带引号表名、跨多行 args、注释、inline env、.env 子表
	codexGlobalDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexGlobalDir, 0755); err != nil {
		t.Fatal(err)
	}
	codexGlobalTOML := `# 这是顶层注释，应当忽略
[model]
name = "claude-3-opus"

[mcp_servers."quoted-server"]
command = "echo #not-a-comment"
args = [
  "start",
  "--port", # 跨行数组中的注释
  "8080"
]
env = { K2 = "v2", K1 = "v1" }

[mcp_servers."quoted-server".env]
K3 = "v3"
`
	if err := os.WriteFile(filepath.Join(codexGlobalDir, "config.toml"), []byte(codexGlobalTOML), 0644); err != nil {
		t.Fatal(err)
	}

	// 4. ProjectDir/.codex/config.toml：url 推断 http
	codexProjDir := filepath.Join(proj, ".codex")
	if err := os.MkdirAll(codexProjDir, 0755); err != nil {
		t.Fatal(err)
	}
	codexProjTOML := `[mcp_servers.proj-codex-http]
url = "https://codex.test/mcp"
`
	if err := os.WriteFile(filepath.Join(codexProjDir, "config.toml"), []byte(codexProjTOML), 0644); err != nil {
		t.Fatal(err)
	}

	servers, err := ScanMCP(Options{Home: home, ProjectDir: proj})
	if err != nil {
		t.Fatalf("ScanMCP error: %v", err)
	}

	// 预期 5 个服务：
	// claude global: 1 (global-stdio-server)
	// claude project: 2 (proj-sse-server, proj-url-server)
	// codex global: 1 (quoted-server)
	// codex project: 1 (proj-codex-http)
	if len(servers) != 5 {
		t.Fatalf("expected 5 servers, got %d", len(servers))
	}

	serverMap := make(map[string]MCPServer)
	for _, s := range servers {
		key := s.Agent + ":" + s.Scope + ":" + s.Name
		serverMap[key] = s
	}

	// 验证 global-stdio-server
	if s, ok := serverMap["claude:global:global-stdio-server"]; !ok {
		t.Errorf("missing global-stdio-server")
	} else {
		if s.Transport != "stdio" {
			t.Errorf("expected stdio transport, got %s", s.Transport)
		}
		if s.Command != "node" {
			t.Errorf("expected command node, got %s", s.Command)
		}
		if !reflect.DeepEqual(s.Args, []string{"server.js", "--flag"}) {
			t.Errorf("unexpected args: %v", s.Args)
		}
		expectedEnv := []string{"A_KEY", "M_KEY", "Z_KEY"}
		if !reflect.DeepEqual(s.EnvKeys, expectedEnv) {
			t.Errorf("expected sorted env keys %v, got %v", expectedEnv, s.EnvKeys)
		}
		expectedSource, _ := filepath.Abs(filepath.Join(home, ".claude.json"))
		if s.Source != expectedSource {
			t.Errorf("expected source %s, got %s", expectedSource, s.Source)
		}
	}

	// 验证 proj-sse-server
	if s, ok := serverMap["claude:project:proj-sse-server"]; !ok {
		t.Errorf("missing proj-sse-server")
	} else {
		if s.Transport != "sse" {
			t.Errorf("expected sse transport, got %s", s.Transport)
		}
		if s.URL != "https://example.com/sse" {
			t.Errorf("expected url https://example.com/sse, got %s", s.URL)
		}
	}

	// 验证 proj-url-server (推断 http)
	if s, ok := serverMap["claude:project:proj-url-server"]; !ok {
		t.Errorf("missing proj-url-server")
	} else {
		if s.Transport != "http" {
			t.Errorf("expected http transport, got %s", s.Transport)
		}
		if s.URL != "http://localhost:3000/mcp" {
			t.Errorf("expected url http://localhost:3000/mcp, got %s", s.URL)
		}
	}

	// 验证 quoted-server (TOML 注释、多行 args、inline env、.env 子表)
	if s, ok := serverMap["codex:global:quoted-server"]; !ok {
		t.Errorf("missing quoted-server")
	} else {
		if s.Transport != "stdio" {
			t.Errorf("expected stdio transport, got %s", s.Transport)
		}
		if s.Command != "echo #not-a-comment" {
			t.Errorf("expected command 'echo #not-a-comment', got %q", s.Command)
		}
		expectedArgs := []string{"start", "--port", "8080"}
		if !reflect.DeepEqual(s.Args, expectedArgs) {
			t.Errorf("expected args %v, got %v", expectedArgs, s.Args)
		}
		expectedEnv := []string{"K1", "K2", "K3"}
		if !reflect.DeepEqual(s.EnvKeys, expectedEnv) {
			t.Errorf("expected sorted env keys %v, got %v", expectedEnv, s.EnvKeys)
		}
	}

	// 验证 proj-codex-http
	if s, ok := serverMap["codex:project:proj-codex-http"]; !ok {
		t.Errorf("missing proj-codex-http")
	} else {
		if s.Transport != "http" {
			t.Errorf("expected http transport, got %s", s.Transport)
		}
		if s.URL != "https://codex.test/mcp" {
			t.Errorf("expected url https://codex.test/mcp, got %s", s.URL)
		}
	}

	// 验证排序：Agent (claude < codex), Scope (global < project), Name
	for i := 1; i < len(servers); i++ {
		prev := servers[i-1]
		curr := servers[i]
		if prev.Agent > curr.Agent {
			t.Errorf("ordering violation on Agent at index %d: %s > %s", i, prev.Agent, curr.Agent)
		} else if prev.Agent == curr.Agent {
			if prev.Scope == "project" && curr.Scope == "global" {
				t.Errorf("ordering violation on Scope at index %d: project before global", i)
			} else if prev.Scope == curr.Scope {
				if prev.Name > curr.Name {
					t.Errorf("ordering violation on Name at index %d: %s > %s", i, prev.Name, curr.Name)
				}
			}
		}
	}
}

// TestScanSkillsPiSources 覆盖 Pi 的两个技能来源：用户级 <home>/.pi/agent/skills
// 与项目级 <project>/.pi/skills。它们与其它 Agent 的技能各自独立计数与停用。
func TestScanSkillsPiSources(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	dataDir := t.TempDir()

	writeSkill := func(root, dir, name string) {
		t.Helper()
		path := filepath.Join(root, dir)
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
		content := fmt.Sprintf("---\nname: %s\ndescription: %s desc\n---\n", name, name)
		if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	writeSkill(home, filepath.Join(".pi", "agent", "skills", "pi-global"), "pi-global")
	writeSkill(proj, filepath.Join(".pi", "skills", "pi-project"), "pi-project")

	opts := Options{Home: home, ProjectDir: proj, DataDir: dataDir}
	skills, err := ScanSkills(opts)
	if err != nil {
		t.Fatalf("ScanSkills error: %v", err)
	}

	byKey := make(map[string]Skill, len(skills))
	for _, s := range skills {
		byKey[s.Agent+":"+s.Scope+":"+s.Name] = s
	}
	if _, ok := byKey["pi:global:pi-global"]; !ok {
		t.Errorf("缺少 Pi 全局技能: %v", skills)
	}
	projSkill, ok := byKey["pi:project:pi-project"]
	if !ok {
		t.Fatalf("缺少 Pi 项目级技能: %v", skills)
	}

	// 停用 Pi 项目级技能后，同名的其它 Agent 技能不受影响。
	if err := SetSkillEnabled(opts, projSkill, false); err != nil {
		t.Fatalf("SetSkillEnabled 失败：%v", err)
	}
	if IsSkillActive(opts, projSkill) {
		t.Fatal("Pi 项目级技能应已停用")
	}
	sameName := Skill{Agent: "codex", Scope: "project", Name: projSkill.Name}
	if !IsSkillActive(opts, sameName) {
		t.Fatal("同名的 codex 技能不应被连带停用")
	}
}

func mustMarshalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestSkillEnabledPersistence(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	dataDir := t.TempDir()

	opts := Options{Home: home, ProjectDir: proj, DataDir: dataDir}

	// 默认处于激活状态
	if !IsSkillActive(opts, "my-skill") {
		t.Fatalf("expected default active")
	}

	// 禁用全局技能
	if err := SetSkillEnabled(opts, "my-skill", false); err != nil {
		t.Fatalf("SetSkillEnabled failed: %v", err)
	}
	if IsSkillActive(opts, "my-skill") {
		t.Fatalf("expected skill to be inactive after disable")
	}

	// 重新启用
	if err := SetSkillEnabled(opts, "my-skill", true); err != nil {
		t.Fatalf("SetSkillEnabled enable failed: %v", err)
	}
	if !IsSkillActive(opts, "my-skill") {
		t.Fatalf("expected skill to be active after enable")
	}

	// 项目级技能禁用
	projSkill := Skill{
		Agent: "codex",
		Scope: "project",
		Name:  "proj-skill",
	}
	if err := SetSkillEnabled(opts, projSkill, false); err != nil {
		t.Fatalf("SetSkillEnabled on project skill failed: %v", err)
	}
	if IsSkillActive(opts, projSkill) {
		t.Fatalf("expected project skill to be inactive")
	}
}

// TestSkillDisabledKeyPerAgent 覆盖 S-114：停用键按 <agent>:<scope>:<name> 区分来源，
// 同名但来自不同 Agent 的技能互不影响；旧配置里的纯名字条目仍对所有同名技能生效。
func TestSkillDisabledKeyPerAgent(t *testing.T) {
	writeSkill := func(root, name string) {
		t.Helper()
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		content := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n", name, name)
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	home := t.TempDir()
	dataDir := t.TempDir()
	opts := Options{Home: home, DataDir: dataDir}
	writeSkill(filepath.Join(home, ".claude", "skills"), "foo")
	writeSkill(filepath.Join(home, ".cursor", "skills"), "foo")

	skillOf := func(agent string) Skill {
		t.Helper()
		skills, err := ScanSkills(opts)
		if err != nil {
			t.Fatalf("ScanSkills error: %v", err)
		}
		for _, sk := range skills {
			if sk.Agent == agent && sk.Name == "foo" {
				return sk
			}
		}
		t.Fatalf("未扫描到 %s 的 foo 技能：%+v", agent, skills)
		return Skill{}
	}

	// 1. 停用 claude 的 foo，cursor 的同名 foo 仍启用
	if err := SetSkillEnabled(opts, skillOf("claude"), false); err != nil {
		t.Fatalf("停用 claude 的 foo 失败：%v", err)
	}
	if IsSkillActive(opts, skillOf("claude")) {
		t.Fatal("claude 的 foo 应已停用")
	}
	if !IsSkillActive(opts, skillOf("cursor")) {
		t.Fatal("cursor 的 foo 不应受影响")
	}

	// 2. 旧配置的纯名字条目对两条都生效，再启用 claude 的只放开它自己
	legacy := []byte("{\n  \"disabled\": [\n    \"foo\"\n  ]\n}")
	if err := os.WriteFile(filepath.Join(dataDir, "skills.json"), legacy, 0644); err != nil {
		t.Fatal(err)
	}
	if IsSkillActive(opts, skillOf("claude")) || IsSkillActive(opts, skillOf("cursor")) {
		t.Fatal("旧的纯名字条目应对所有同名技能生效")
	}
	if err := SetSkillEnabled(opts, skillOf("claude"), true); err != nil {
		t.Fatalf("启用 claude 的 foo 失败：%v", err)
	}
	if !IsSkillActive(opts, skillOf("claude")) {
		t.Fatal("claude 的 foo 应已启用")
	}
	if IsSkillActive(opts, skillOf("cursor")) {
		t.Fatal("cursor 的 foo 应保持停用")
	}

	// 3. 落盘结果是展开后的新键，旧条目已删除
	set, err := loadDisabledSkills(filepath.Join(dataDir, "skills.json"))
	if err != nil {
		t.Fatalf("读取技能配置失败：%v", err)
	}
	if _, ok := set["foo"]; ok {
		t.Fatal("旧的纯名字条目应已展开删除")
	}
	if !set["cursor:global:foo"] || set["claude:global:foo"] {
		t.Fatalf("禁用集合应只剩 cursor 的 foo：%v", set)
	}
}

// TestSkillLegacyExpandScanFailure 覆盖展开旧停用条目时扫描失败：
// 拿不到同名技能清单就不该动配置，应返回错误且 skills.json 保持原样。
func TestSkillLegacyExpandScanFailure(t *testing.T) {
	home := t.TempDir()
	dataDir := t.TempDir()

	// 把所有技能来源目录换成普通文件，ScanSkills 的每一次 ReadDir 都会失败（ENOTDIR）。
	for _, src := range globalSkillSources(home) {
		if err := os.MkdirAll(filepath.Dir(src.path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(src.path, []byte("not a directory"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	opts := Options{Home: home, DataDir: dataDir}
	if _, err := ScanSkills(opts); err == nil {
		t.Fatal("前置条件不成立：所有来源都读失败时 ScanSkills 应返回错误")
	}

	legacyPath := filepath.Join(dataDir, "skills.json")
	legacy := []byte("{\n  \"disabled\": [\n    \"foo\"\n  ]\n}")
	if err := os.WriteFile(legacyPath, legacy, 0644); err != nil {
		t.Fatal(err)
	}

	err := SetSkillEnabled(opts, Skill{Agent: "claude", Scope: "global", Name: "foo"}, true)
	if err == nil {
		t.Fatal("扫描失败时 SetSkillEnabled 应返回错误，而不是丢掉其它同名技能的停用状态")
	}
	if !strings.Contains(err.Error(), "无法迁移旧的停用记录") {
		t.Fatalf("错误信息应说明旧的停用记录无法迁移，实际：%v", err)
	}
	after, readErr := os.ReadFile(legacyPath)
	if readErr != nil {
		t.Fatalf("读取 skills.json 失败：%v", readErr)
	}
	if string(after) != string(legacy) {
		t.Fatalf("扫描失败时不应改写 skills.json，实际内容：%s", after)
	}
}

func TestMCPServerPersistenceAndSecrets(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	dataDir := t.TempDir()

	memSecrets := config.NewMemorySecretStore()
	SetSecretStore(memSecrets)
	defer func() {
		SetSecretStore(config.NewSecretStore("mcode.mcp"))
	}()

	opts := Options{Home: home, ProjectDir: proj, DataDir: dataDir}

	srv := MCPServer{
		Name:      "test-custom",
		Transport: "stdio",
		Command:   "node",
		Args:      []string{"server.js"},
		Scope:     "global",
		Env: map[string]string{
			"NORMAL_VAR": "hello",
			"API_KEY":    "secret-token-12345",
		},
	}

	if err := SaveMCPServer(opts, srv); err != nil {
		t.Fatalf("SaveMCPServer failed: %v", err)
	}

	// 验证敏感变量存入 SecretStore，而不是在配置文件明文存留
	val, err := memSecrets.Get("mcp.test-custom.API_KEY")
	if err != nil || val != "secret-token-12345" {
		t.Fatalf("expected secret stored in SecretStore, got val=%q, err=%v", val, err)
	}

	// ScanMCP 普通模式（不带 WithCredentials）不暴露敏感值
	servers, err := ScanMCP(opts)
	if err != nil {
		t.Fatalf("ScanMCP failed: %v", err)
	}
	var found *MCPServer
	for i := range servers {
		if servers[i].Name == "test-custom" {
			found = &servers[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected to find test-custom in ScanMCP")
	}
	if found.Env != nil {
		t.Fatalf("expected nil Env without credentials")
	}

	// ScanMCP WithCredentials 模式恢复敏感环境变量
	credOpts := opts
	credOpts.WithCredentials = true
	credServers, err := ScanMCP(credOpts)
	if err != nil {
		t.Fatalf("ScanMCP with credentials failed: %v", err)
	}
	var credFound *MCPServer
	for i := range credServers {
		if credServers[i].Name == "test-custom" {
			credFound = &credServers[i]
			break
		}
	}
	if credFound == nil || credFound.Env["API_KEY"] != "secret-token-12345" || credFound.Env["NORMAL_VAR"] != "hello" {
		t.Fatalf("expected restored env with secret, got: %v", credFound)
	}

	// 测试服务禁用开关
	if err := SetMCPServerEnabled(opts, "test-custom", false); err != nil {
		t.Fatalf("SetMCPServerEnabled failed: %v", err)
	}
	serversDisabled, _ := ScanMCP(opts)
	for _, s := range serversDisabled {
		if s.Name == "test-custom" && !s.Disabled {
			t.Fatalf("expected test-custom to be disabled")
		}
	}

	// 测试服务删除
	if err := DeleteMCPServer(opts, "test-custom"); err != nil {
		t.Fatalf("DeleteMCPServer failed: %v", err)
	}
	serversAfterDel, _ := ScanMCP(opts)
	for _, s := range serversAfterDel {
		if s.Name == "test-custom" {
			t.Fatalf("expected test-custom to be deleted")
		}
	}
}

func TestListMCPTools(t *testing.T) {
	srv := MCPServer{Name: "unconnected-srv", Transport: "stdio", Command: "dummy"}
	origLister := DefaultToolLister
	DefaultToolLister = nil
	defer func() { DefaultToolLister = origLister }()

	_, err := ListMCPTools(context.Background(), srv)
	if err == nil {
		t.Fatalf("expected error when no connection available")
	}

	DefaultToolLister = func(ctx context.Context, s MCPServer) ([]MCPToolInfo, error) {
		return []MCPToolInfo{
			{Name: "test_tool", Description: "测试工具"},
		}, nil
	}
	tools, err := ListMCPTools(context.Background(), srv)
	if err != nil || len(tools) != 1 || tools[0].Name != "test_tool" {
		t.Fatalf("expected mock tools returned, got tools=%v err=%v", tools, err)
	}
}

// TestScanSkillsExternalHarnesses 验证 cursor、factory、antigravity、gemini 等外部 Harness 的技能来源。
func TestScanSkillsExternalHarnesses(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()

	writeSkill := func(dir, name, description string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		content := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n", name, description)
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// 全局来源：cursor、factory、antigravity（普通与 builtin 两个目录）、gemini
	writeSkill(filepath.Join(home, ".cursor", "skills", "cursor-skill"), "cursor-skill", "Cursor 技能")
	writeSkill(filepath.Join(home, ".factory", "skills", "factory-skill"), "factory-skill", "Factory 技能")
	writeSkill(filepath.Join(home, ".gemini", "antigravity", "skills", "ag-skill"), "ag-skill", "Antigravity 技能")
	writeSkill(filepath.Join(home, ".gemini", "antigravity", "builtin", "skills", "ag-builtin-skill"), "ag-builtin-skill", "Antigravity 内置技能")
	writeSkill(filepath.Join(home, ".gemini", "config", "skills", "gemini-skill"), "gemini-skill", "Gemini 技能")

	// antigravity 两个目录同名技能：保留先扫描到的（antigravity/skills 在前）
	writeSkill(filepath.Join(home, ".gemini", "antigravity", "skills", "ag-dup"), "ag-dup", "First occurrence")
	writeSkill(filepath.Join(home, ".gemini", "antigravity", "builtin", "skills", "ag-dup-ignored"), "ag-dup", "Second occurrence should be ignored")

	// 项目来源：factory
	writeSkill(filepath.Join(proj, ".factory", "skills", "factory-proj-skill"), "factory-proj-skill", "Factory 项目技能")

	skills, err := ScanSkills(Options{Home: home, ProjectDir: proj})
	if err != nil {
		t.Fatalf("ScanSkills error: %v", err)
	}

	want := map[string]string{
		"cursor:global:cursor-skill":          filepath.Join(".cursor", "skills", "cursor-skill", "SKILL.md"),
		"factory:global:factory-skill":        filepath.Join(".factory", "skills", "factory-skill", "SKILL.md"),
		"antigravity:global:ag-skill":         filepath.Join(".gemini", "antigravity", "skills", "ag-skill", "SKILL.md"),
		"antigravity:global:ag-builtin-skill": filepath.Join(".gemini", "antigravity", "builtin", "skills", "ag-builtin-skill", "SKILL.md"),
		"gemini:global:gemini-skill":          filepath.Join(".gemini", "config", "skills", "gemini-skill", "SKILL.md"),
		"antigravity:global:ag-dup":           filepath.Join(".gemini", "antigravity", "skills", "ag-dup", "SKILL.md"),
		"factory:project:factory-proj-skill":  filepath.Join(".factory", "skills", "factory-proj-skill", "SKILL.md"),
	}

	if len(skills) != len(want) {
		t.Fatalf("expected %d skills, got %d: %+v", len(want), len(skills), skills)
	}

	got := make(map[string]Skill, len(skills))
	for _, s := range skills {
		got[s.Agent+":"+s.Scope+":"+s.Name] = s
	}
	for key, suffix := range want {
		s, ok := got[key]
		if !ok {
			t.Errorf("missing skill %s", key)
			continue
		}
		if !filepath.IsAbs(s.Path) || !strings.HasSuffix(s.Path, suffix) {
			t.Errorf("skill %s path = %q, want suffix %q", key, s.Path, suffix)
		}
	}

	// 同名技能去重：保留首次出现（antigravity/skills），描述为第一条
	if s, ok := got["antigravity:global:ag-dup"]; !ok {
		t.Errorf("missing antigravity:global:ag-dup")
	} else if s.Description != "First occurrence" {
		t.Errorf("expected first occurrence to be preserved, got %q", s.Description)
	}
}
