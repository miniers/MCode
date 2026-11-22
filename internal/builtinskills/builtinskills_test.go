package builtinskills

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/projctx"
)

func TestBuiltinInstallAndIdempotent(t *testing.T) {
	t.Cleanup(func() { projctx.SetBuiltinSkillsDir("") })

	if err := Install(""); err != nil {
		t.Fatalf("Install(\"\") = %v, want nil", err)
	}

	dataDir := t.TempDir()
	if err := Install(dataDir); err != nil {
		t.Fatalf("first Install: %v", err)
	}

	target := filepath.Join(Dir(dataDir), "design-mockup", "SKILL.md")
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read installed SKILL.md: %v", err)
	}
	want, err := skillsFS.ReadFile("skills/design-mockup/SKILL.md")
	if err != nil {
		t.Fatalf("read embedded SKILL.md: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("installed content mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}

	infoBefore, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat before second Install: %v", err)
	}
	if err := Install(dataDir); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	infoAfter, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat after second Install: %v", err)
	}
	if !infoBefore.ModTime().Equal(infoAfter.ModTime()) {
		t.Fatalf("idempotent Install rewrote file: before=%v after=%v", infoBefore.ModTime(), infoAfter.ModTime())
	}

	// 磁盘内容被篡改时应恢复为内置版本。
	if err := os.WriteFile(target, []byte("stale"), 0o644); err != nil {
		t.Fatalf("corrupt target: %v", err)
	}
	if err := Install(dataDir); err != nil {
		t.Fatalf("repair Install: %v", err)
	}
	repaired, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read repaired SKILL.md: %v", err)
	}
	if !bytes.Equal(repaired, want) {
		t.Fatalf("repaired content mismatch")
	}
}

func TestBuiltinDesignMockupFrontmatter(t *testing.T) {
	data, err := skillsFS.ReadFile("skills/design-mockup/SKILL.md")
	if err != nil {
		t.Fatalf("read embedded SKILL.md: %v", err)
	}
	content := string(data)
	lines := strings.Split(content, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		t.Fatalf("missing frontmatter header in embedded SKILL.md")
	}
	var name string
	closed := false
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			closed = true
			break
		}
		if key, val, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(key) == "name" {
			name = strings.Trim(strings.TrimSpace(val), `"'`)
		}
	}
	if !closed {
		t.Fatalf("unclosed frontmatter in embedded SKILL.md")
	}
	if name != "design-mockup" {
		t.Fatalf("frontmatter name = %q, want %q", name, "design-mockup")
	}
}
