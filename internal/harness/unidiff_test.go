package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreviewEditDiff(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.txt")
	content := "hello world\nhello mcode\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	t.Run("Edit 替换成功", func(t *testing.T) {
		diff := PreviewEditDiff(filePath, "hello", "hi", false)
		if diff == "" {
			t.Fatalf("expected non-empty diff")
		}
		if !strings.Contains(diff, "--- a/") || !strings.Contains(diff, "+++ b/") {
			t.Errorf("expected unified diff headers, got:\n%s", diff)
		}
		if !strings.Contains(diff, "-hello world") || !strings.Contains(diff, "+hi world") {
			t.Errorf("expected replacement in diff, got:\n%s", diff)
		}
	})

	t.Run("Edit 全部替换", func(t *testing.T) {
		diff := PreviewEditDiff(filePath, "hello", "hi", true)
		if diff == "" {
			t.Fatalf("expected non-empty diff")
		}
		if !strings.Contains(diff, "+hi world") || !strings.Contains(diff, "+hi mcode") {
			t.Errorf("expected all replacements in diff, got:\n%s", diff)
		}
	})

	t.Run("old_string 不存在时返回空串", func(t *testing.T) {
		diff := PreviewEditDiff(filePath, "nonexistent_string", "new", false)
		if diff != "" {
			t.Errorf("expected empty diff when old_string not found, got:\n%s", diff)
		}
	})
}

func TestPreviewWriteDiff(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("新文件 Write", func(t *testing.T) {
		newFilePath := filepath.Join(tmpDir, "new_file.txt")
		content := "first line\nsecond line\n"
		diff := PreviewWriteDiff(newFilePath, content)
		if diff == "" {
			t.Fatalf("expected non-empty diff for new file")
		}
		if !strings.Contains(diff, "--- /dev/null") {
			t.Errorf("expected --- /dev/null for new file, got:\n%s", diff)
		}
		if !strings.Contains(diff, "+first line") || !strings.Contains(diff, "+second line") {
			t.Errorf("expected added lines in diff, got:\n%s", diff)
		}
	})

	t.Run("已有文件 Write 覆盖", func(t *testing.T) {
		filePath := filepath.Join(tmpDir, "existing.txt")
		if err := os.WriteFile(filePath, []byte("old text\n"), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}
		diff := PreviewWriteDiff(filePath, "new text\n")
		if diff == "" {
			t.Fatalf("expected non-empty diff for overwrite")
		}
		if !strings.Contains(diff, "-old text") || !strings.Contains(diff, "+new text") {
			t.Errorf("expected diff showing change, got:\n%s", diff)
		}
	})
}

func TestPreviewMultiEditDiff(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "multi.txt")
	content := "alpha beta gamma\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	edits := []EditOperation{
		{OldString: "alpha", NewString: "one"},
		{OldString: "beta", NewString: "two"},
	}
	diff := PreviewMultiEditDiff(filePath, edits)
	if diff == "" {
		t.Fatalf("expected non-empty diff")
	}
	if !strings.Contains(diff, "+one two gamma") {
		t.Errorf("expected accumulated replacement, got:\n%s", diff)
	}

	badEdits := []EditOperation{
		{OldString: "alpha", NewString: "one"},
		{OldString: "missing", NewString: "two"},
	}
	if badDiff := PreviewMultiEditDiff(filePath, badEdits); badDiff != "" {
		t.Errorf("expected empty diff on missing string, got:\n%s", badDiff)
	}
}
