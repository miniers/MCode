package mcodeui

import "testing"

func TestChangelogVersionsParsedOnce(t *testing.T) {
	v1 := changelogVersions()
	v2 := changelogVersions()
	if len(v1) == 0 {
		t.Fatal("expected non-empty changelog versions")
	}
	if len(v1) != len(v2) || &v1[0] != &v2[0] {
		t.Fatalf("expected changelogVersions() to return cached slice backing array")
	}
}
