package projctx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGoReadDirsWithoutGoMod(t *testing.T) {
	if dirs := GoReadDirs(t.TempDir()); dirs != nil {
		t.Fatalf("GoReadDirs = %v, want nil", dirs)
	}
}

func TestGoReadDirs(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not found")
	}
	projectPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectPath, "go.mod"), []byte("module example.com/x\n\ngo 1.20\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "env", "GOMODCACHE")
	cmd.Dir = projectPath
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	cacheDir := strings.TrimSpace(string(output))
	dirs := GoReadDirs(projectPath)
	found := false
	for _, dir := range dirs {
		info, err := os.Stat(dir)
		if !filepath.IsAbs(dir) || err != nil || !info.IsDir() {
			t.Fatalf("invalid directory %q: %v", dir, err)
		}
		if dir == filepath.Clean(cacheDir) {
			found = true
		}
	}
	if info, err := os.Stat(cacheDir); err == nil && info.IsDir() && !found {
		t.Fatalf("GoReadDirs = %v, missing module cache %q", dirs, cacheDir)
	}
}
