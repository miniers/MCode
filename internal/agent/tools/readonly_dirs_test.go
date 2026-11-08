package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadOnlyDirs(t *testing.T) {
	workDir := t.TempDir()
	skillDir := t.TempDir()
	outsideDir := t.TempDir()
	skillFile := filepath.Join(skillDir, "skill.txt")
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	put(t, skillFile, "skill content\n")
	put(t, outsideFile, "outside content\n")
	env := Env{WorkDir: workDir, ReadOnlyDirs: []string{skillDir}}

	t.Run("read and search", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			args map[string]any
			want string
		}{
			{"read", map[string]any{"path": skillFile}, "skill content"},
			{"grep", map[string]any{"path": skillDir, "pattern": "skill content"}, "skill content"},
			{"find", map[string]any{"path": skillDir, "pattern": "*.txt"}, "skill.txt"},
			{"ls", map[string]any{"path": skillDir}, "skill.txt"},
		} {
			result := runTool(t, env, tc.name, tc.args)
			if result.IsError || !strings.Contains(result.Text, tc.want) {
				t.Fatalf("%s: %+v", tc.name, result)
			}
		}
	})

	t.Run("write denied", func(t *testing.T) {
		result := runTool(t, env, "edit", map[string]any{
			"path": skillFile, "edits": []textEdit{{"skill content", "changed"}},
		})
		if !result.IsError || !strings.Contains(result.Text, "outside allowed directories") {
			t.Fatalf("result = %+v", result)
		}
		data, err := os.ReadFile(skillFile)
		if err != nil || string(data) != "skill content\n" {
			t.Fatalf("content = %q, err = %v", data, err)
		}
	})

	t.Run("outside read denied", func(t *testing.T) {
		result := runTool(t, env, "read", map[string]any{"path": outsideFile})
		if !result.IsError || !strings.Contains(result.Text, "outside allowed directories") {
			t.Fatalf("result = %+v", result)
		}
	})
}
