package app

import (
	"os"
	"path/filepath"
	"testing"
)

// writePiPackage 造一个 pi 包目录，并在 agentDir 下写 settings.json 声明它。
func writePiPackage(t *testing.T, agentDir, pkgDir string, decls ...string) {
	t.Helper()
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := `{"packages":[`
	for i, decl := range decls {
		if i > 0 {
			settings += ","
		}
		settings += decl
	}
	settings += `]}`
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mkdirAll(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPiPackageSkillRootsNpmConventional(t *testing.T) {
	agentDir := t.TempDir()
	pkgDir := filepath.Join(agentDir, "npm", "node_modules", "pi-web-access")
	mkdirAll(t, filepath.Join(pkgDir, "skills", "fetch"))
	writePiPackage(t, agentDir, pkgDir, `"npm:pi-web-access"`)

	roots, ok := piPackageSkillRoots(agentDir, t.TempDir())
	if !ok {
		t.Fatal("conventional skills dir should resolve")
	}
	want := filepath.Join(pkgDir, "skills")
	if len(roots) != 1 || roots[0] != want {
		t.Fatalf("roots = %v, want [%s]", roots, want)
	}
}

func TestPiPackageSkillRootsScopedVersioned(t *testing.T) {
	agentDir := t.TempDir()
	pkgDir := filepath.Join(agentDir, "npm", "node_modules", "@scope", "pkg")
	mkdirAll(t, filepath.Join(pkgDir, "skills"))
	writePiPackage(t, agentDir, pkgDir, `"npm:@scope/pkg@1.2.3"`)

	roots, ok := piPackageSkillRoots(agentDir, t.TempDir())
	if !ok || len(roots) != 1 || roots[0] != filepath.Join(pkgDir, "skills") {
		t.Fatalf("roots = %v ok = %v", roots, ok)
	}
}

func TestPiPackageSkillRootsGitSource(t *testing.T) {
	agentDir := t.TempDir()
	pkgDir := filepath.Join(agentDir, "git", "github.com", "real-wudaoshi", "better-custom")
	mkdirAll(t, filepath.Join(pkgDir, "skills"))
	writePiPackage(t, agentDir, pkgDir, `"https://github.com/real-wudaoshi/better-custom"`)

	roots, ok := piPackageSkillRoots(agentDir, t.TempDir())
	if !ok || len(roots) != 1 || roots[0] != filepath.Join(pkgDir, "skills") {
		t.Fatalf("roots = %v ok = %v", roots, ok)
	}
}

func TestPiPackageSkillRootsManifest(t *testing.T) {
	agentDir := t.TempDir()
	pkgDir := filepath.Join(agentDir, "npm", "node_modules", "pi-subagents")
	mkdirAll(t, filepath.Join(pkgDir, "resources", "skills"))
	manifest := `{"pi":{"skills":["./resources/skills"]}}`
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	writePiPackage(t, agentDir, pkgDir, `"npm:pi-subagents"`)

	roots, ok := piPackageSkillRoots(agentDir, t.TempDir())
	if !ok || len(roots) != 1 || roots[0] != filepath.Join(pkgDir, "resources", "skills") {
		t.Fatalf("roots = %v ok = %v", roots, ok)
	}
}

func TestPiPackageSkillRootsRejectsFilteredDeclaration(t *testing.T) {
	agentDir := t.TempDir()
	pkgDir := filepath.Join(agentDir, "npm", "node_modules", "pkg")
	mkdirAll(t, filepath.Join(pkgDir, "skills"))
	writePiPackage(t, agentDir, pkgDir, `{"source":"npm:pkg","skills":[]}`)

	if _, ok := piPackageSkillRoots(agentDir, t.TempDir()); ok {
		t.Fatal("filtered declaration must fall back to native discovery")
	}
}

func TestPiPackageSkillRootsSkipsMissingPackage(t *testing.T) {
	agentDir := t.TempDir()
	writePiPackage(t, agentDir, filepath.Join(agentDir, "unused"), `"npm:not-installed"`)

	roots, ok := piPackageSkillRoots(agentDir, t.TempDir())
	if !ok {
		t.Fatal("missing package should be skipped, not force a fallback")
	}
	if len(roots) != 0 {
		t.Fatalf("roots = %v, want none", roots)
	}
}

func TestPiPackageSkillRootsLocalPath(t *testing.T) {
	agentDir := t.TempDir()
	workDir := t.TempDir()
	// 相对路径按「声明它的 settings.json 所在目录」解析，这里就是 <workDir>/.pi。
	pkgDir := filepath.Join(workDir, ".pi", "tools")
	mkdirAll(t, filepath.Join(pkgDir, "skills"))
	if err := os.WriteFile(filepath.Join(workDir, ".pi", "settings.json"), []byte(`{"packages":["./tools"]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	roots, ok := piPackageSkillRoots(agentDir, workDir)
	if !ok || len(roots) != 1 || roots[0] != filepath.Join(pkgDir, "skills") {
		t.Fatalf("roots = %v ok = %v", roots, ok)
	}
}

func TestPiSettingsDeclareSkillsIgnoresPackages(t *testing.T) {
	agentDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{"packages":["npm:pkg"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if piSettingsDeclareSkills(agentDir, t.TempDir()) {
		t.Fatal("packages alone must not disable the skill whitelist")
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{"skills":["./my-skill"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !piSettingsDeclareSkills(agentDir, t.TempDir()) {
		t.Fatal("explicit skills entries must still fall back")
	}
}
