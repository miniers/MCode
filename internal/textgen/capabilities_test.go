package textgen

import (
	"errors"
	"reflect"
	"testing"
)

func TestReviewProviderOrder(t *testing.T) {
	cases := []struct {
		name     string
		explicit string
		model    string
		want     []string
	}{
		{"default-order", "", "", []string{"claude", "codex"}},
		{"explicit-codex-first", "codex", "", []string{"codex", "claude"}},
		{"explicit-claude-deduped", "claude", "", []string{"claude", "codex"}},
		{"explicit-unsupported-ignored", "agy", "", []string{"claude", "codex"}},
		{"explicit-memory-ignored", "memory", "", []string{"claude", "codex"}},
		{"explicit-pi-with-model", "pi", "sonnet", []string{"pi", "claude", "codex"}},
		{"explicit-pi-without-model", "pi", "", []string{"claude", "codex"}},
		{"model-enables-pi-fallback", "", "sonnet", []string{"claude", "codex", "pi"}},
		{"explicit-normalized", " Codex ", "", []string{"codex", "claude"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ReviewProviderOrder(tc.explicit, tc.model)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ReviewProviderOrder(%q, %q) = %v, want %v", tc.explicit, tc.model, got, tc.want)
			}
		})
	}
}

func TestAvailableProviderUsesLookPath(t *testing.T) {
	orig := lookPath
	t.Cleanup(func() { lookPath = orig })

	var looked []string
	lookPath = func(name string) (string, error) {
		looked = append(looked, name)
		if name == "claude" {
			return "/opt/bin/claude", nil
		}
		return "", errors.New("not found")
	}

	if !AvailableProvider("claude") {
		t.Fatalf("claude should be available when lookPath succeeds")
	}
	if !AvailableProvider(" Claude ") {
		t.Fatalf("provider should be normalized before lookup")
	}
	if AvailableProvider("codex") {
		t.Fatalf("codex should be unavailable when lookPath fails")
	}
	if AvailableProvider("memory") {
		t.Fatalf("memory has no CLI and should never be available")
	}
	if AvailableProvider("agy") {
		t.Fatalf("unsupported provider should not be available")
	}

	wantLooked := []string{"claude", "claude", "codex"}
	if !reflect.DeepEqual(looked, wantLooked) {
		t.Fatalf("lookPath calls = %v, want %v", looked, wantLooked)
	}
}
