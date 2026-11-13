package webassets

import "testing"

func TestDistIncludesGitkeep(t *testing.T) {
	if _, err := distFS.Open("dist/.gitkeep"); err != nil {
		t.Fatal(err)
	}
}
