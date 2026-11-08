package projctx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSkillReadDirs(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ext, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(home, ".agents", "skills", "a")
	b := filepath.Join(home, ".pi", "agent", "skills", "b")
	target := filepath.Join(ext, "b")
	for _, dir := range []string{a, b, target} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(a, "SKILL.md"), []byte("技能 a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("技能 b"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(target, "SKILL.md"), filepath.Join(b, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	dirs := SkillReadDirs("", home)
	seen := make(map[string]bool)
	for _, dir := range dirs {
		if seen[dir] {
			t.Fatalf("目录重复: %s", dir)
		}
		seen[dir] = true
	}
	for _, dir := range []string{
		filepath.Join(home, ".pi", "agent", "skills"),
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".gemini", "antigravity", "skills"),
		filepath.Join(home, ".gemini", "config", "skills"),
		a, b, target,
	} {
		if !seen[dir] {
			t.Errorf("缺少目录: %s", dir)
		}
	}
}
