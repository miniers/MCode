package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestClientTerminalWorkspaceAndScripts(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"scripts":{"build'; echo unsafe":"echo build"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := Open(Options{DataDir: t.TempDir(), Provider: "memory", WorkDir: root, Mode: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	client := a.Client()
	projects, err := client.Projects(ctx)
	if err != nil || len(projects) == 0 {
		t.Fatalf("projects: %v, %v", projects, err)
	}
	id, err := client.CreateThread(ctx, projects[0].ID, "")
	if err != nil {
		t.Fatal(err)
	}
	dir, ok := client.ThreadWorkDir(id)
	if !ok || dir != root {
		t.Fatalf("workspace = %q, %v; want %q", dir, ok, root)
	}
	scripts, err := client.Scripts(ctx, id)
	if err != nil || len(scripts) != 1 {
		t.Fatalf("scripts: %v, %v", scripts, err)
	}
	if scripts[0].Name != "build'; echo unsafe" || scripts[0].Command != "echo build" || scripts[0].Runner != "npm" {
		t.Fatalf("script metadata changed: %+v", scripts[0])
	}
}
