package agentconf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinSkillsScanDisableAndRejectDelete(t *testing.T) {
	home := t.TempDir()
	dataDir := t.TempDir()

	skillDir := filepath.Join(dataDir, "builtin-skills", "design-mockup")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("---\nname: design-mockup\ndescription: builtin desc\n---\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	opts := Options{Home: home, DataDir: dataDir}
	skills, err := ScanSkills(opts)
	if err != nil {
		t.Fatalf("ScanSkills: %v", err)
	}

	var found *Skill
	for i := range skills {
		if skills[i].Agent == "mcode" && skills[i].Name == "design-mockup" {
			found = &skills[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("builtin skill not found in ScanSkills result: %+v", skills)
	}
	if found.Scope != "global" || !found.Enabled || found.Disabled {
		t.Fatalf("unexpected builtin skill state: %+v", *found)
	}

	if err := SetSkillEnabled(opts, *found, false); err != nil {
		t.Fatalf("SetSkillEnabled(false): %v", err)
	}
	skillsAfterDisable, err := ScanSkills(opts)
	if err != nil {
		t.Fatalf("ScanSkills after disable: %v", err)
	}
	if len(skillsAfterDisable) != 1 || !skillsAfterDisable[0].Disabled || skillsAfterDisable[0].Enabled {
		t.Fatalf("builtin skill should be disabled: %+v", skillsAfterDisable)
	}

	err = DeleteSkill(opts, *found)
	if err == nil {
		t.Fatal("DeleteSkill succeeded for builtin skill, want error")
	}
	if !strings.Contains(err.Error(), "内置技能不能删除，可停用") {
		t.Fatalf("DeleteSkill error = %q, want containing %q", err.Error(), "内置技能不能删除，可停用")
	}
	if _, statErr := os.Stat(skillPath); statErr != nil {
		t.Fatalf("builtin SKILL.md should still exist after rejected delete: %v", statErr)
	}
}
