package mcodeui

import "testing"

func TestGitPaneWiring(t *testing.T) {
	if gitPaneDiffView == nil || gitPaneBlameView == nil || gitPaneBranchPopover == nil || gitPaneRemotesDialog == nil {
		t.Fatal("Git 面板入口未接线")
	}
}

func TestGitRelativeFilePath(t *testing.T) {
	for _, tc := range []struct {
		root, file, want string
		ok               bool
	}{
		{"/repo", "/repo/src/a.go", "src/a.go", true},
		{"/repo", "/repo/../other/a.go", "", false},
		{"/repo", "/repo-other/a.go", "", false},
		{"/repo", "/repo", "", false},
		{"", "/repo/a", "", false},
	} {
		got, ok := gitRelativeFilePath(tc.root, tc.file)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%q %q: %q %v", tc.root, tc.file, got, ok)
		}
	}
}
