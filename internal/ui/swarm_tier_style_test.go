package mcodeui

import "testing"

func TestSwarmTierColorUsesKnownIdentityColors(t *testing.T) {
	for _, key := range []string{"eva", "gwen", "hugo"} {
		if _, ok := identityPalette[key]; !ok {
			t.Fatalf("identityPalette missing key %q", key)
		}
	}

	choreColor := swarmTierColor(nil, "chore")
	if choreColor == ColorTextSecondaryLight {
		t.Fatalf("swarmTierColor(nil, %q) returned fallback ColorTextSecondaryLight", "chore")
	}
	if want := identityPalette["eva"]; choreColor != want {
		t.Fatalf("swarmTierColor(nil, %q) = %v, want %v", "chore", choreColor, want)
	}

	var hitGwen, hitHugo bool
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		col := swarmTierColor(nil, id)
		if col == ColorTextSecondaryLight {
			t.Fatalf("swarmTierColor(nil, %q) returned fallback ColorTextSecondaryLight", id)
		}
		if col == identityPalette["gwen"] {
			hitGwen = true
		}
		if col == identityPalette["hugo"] {
			hitHugo = true
		}
	}
	if !hitGwen || !hitHugo {
		t.Fatalf("expected custom IDs to cover gwen (%v) and hugo (%v) branches", hitGwen, hitHugo)
	}
}
