package mcodeui

import (
	"path/filepath"
	"testing"
)

func TestGitPaneWiring(t *testing.T) {
	if gitPaneDiffView == nil || gitPaneBlameView == nil || gitPaneBranchPopover == nil || gitPaneRemotesDialog == nil {
		t.Fatal("Git 面板入口未接线")
	}
}

func TestGitRelativeFilePath(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		root, file, want string
		ok               bool
	}{
		{root, filepath.Join(root, "src", "a.go"), "src/a.go", true},
		{root, filepath.Join(root, "..", "other", "a.go"), "", false},
		{root, filepath.Join(root+"-other", "a.go"), "", false},
		{root, root, "", false},
		{"", filepath.Join(root, "a"), "", false},
	} {
		got, ok := gitRelativeFilePath(tc.root, tc.file)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%q %q: %q %v", tc.root, tc.file, got, ok)
		}
	}
}
