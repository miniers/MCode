package harness

import "testing"

func TestIsCLICatalogProvider(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"commandcode", true}, {"cline", true}, {"pi", true}, {"omp", true},
		{"copilot", true}, {"droid", true}, {"cursor", true}, {"opencode", true},
		{"grok", true}, {"devin", true}, {"mcode", false}, {"custom-test", false},
		{"anthropic", false}, {"deepseek", false},
	} {
		if got := IsCLICatalogProvider(tc.id); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.id, got, tc.want)
		}
	}
}
