package projctx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuiltinSkillsDiscoveryOverrideAndInjected(t *testing.T) {
	t.Cleanup(func() { SetBuiltinSkillsDir("") })

	builtinDir := t.TempDir()
	home := t.TempDir()
	projectDir := t.TempDir()

	builtinSkillDir := filepath.Join(builtinDir, "design-mockup")
	if err := os.MkdirAll(builtinSkillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	builtinSkillPath := filepath.Join(builtinSkillDir, "SKILL.md")
	if err := os.WriteFile(builtinSkillPath, []byte("---\nname: design-mockup\ndescription: builtin desc\n---\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	SetBuiltinSkillsDir(builtinDir)

	if !IsSkillInjectedPath(projectDir, home, builtinSkillPath) {
		t.Fatalf("IsSkillInjectedPath(%q) = false, want true", builtinSkillPath)
	}
	if !IsSkillInjectedPath(projectDir, home, builtinSkillDir) {
		t.Fatalf("IsSkillInjectedPath(%q) = false, want true", builtinSkillDir)
	}

	skills := DiscoverSkills(projectDir, home)
	if len(skills) != 1 {
		t.Fatalf("DiscoverSkills len = %d, want 1: %+v", len(skills), skills)
	}
	if skills[0].Name != "design-mockup" || skills[0].Scope != SkillScopeGlobal || skills[0].Path != builtinSkillPath {
		t.Fatalf("unexpected builtin skill: %+v", skills[0])
	}

	readDirs := SkillReadDirs(projectDir, home)
	foundBuiltinRoot := false
	for _, d := range readDirs {
		if d == filepath.Clean(builtinDir) {
			foundBuiltinRoot = true
			break
		}
	}
	if !foundBuiltinRoot {
		t.Fatalf("SkillReadDirs missing builtin dir %q: %v", builtinDir, readDirs)
	}

	// 用户全局同名技能应覆盖内置技能。
	userSkillDir := filepath.Join(home, ".agents", "skills", "design-mockup")
	if err := os.MkdirAll(userSkillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userSkillPath := filepath.Join(userSkillDir, "SKILL.md")
	if err := os.WriteFile(userSkillPath, []byte("---\nname: design-mockup\ndescription: user desc\n---\n\nuser\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skills = DiscoverSkills(projectDir, home)
	if len(skills) != 1 || skills[0].Path != userSkillPath || skills[0].Scope != SkillScopeGlobal {
		t.Fatalf("user global skill did not override builtin: %+v", skills)
	}

	// 项目 .agents/skills 下同名技能应覆盖全局（含内置）技能。
	projSkillDir := filepath.Join(projectDir, ".agents", "skills", "design-mockup")
	if err := os.MkdirAll(projSkillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	projSkillPath := filepath.Join(projSkillDir, "SKILL.md")
	if err := os.WriteFile(projSkillPath, []byte("---\nname: design-mockup\ndescription: project desc\n---\n\nproj\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skills = DiscoverSkills(projectDir, home)
	if len(skills) != 1 || skills[0].Path != projSkillPath || skills[0].Scope != SkillScopeProject {
		t.Fatalf("project skill did not override builtin/global: %+v", skills)
	}
}
