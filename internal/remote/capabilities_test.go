package remote

import "testing"

func TestDefaultFeaturesIncludesTransportTailscale(t *testing.T) {
	features := DefaultFeatures()
	if len(features) == 0 {
		t.Fatal("DefaultFeatures() returned no features")
	}
	if last := features[len(features)-1]; last != "transport.tailscale" {
		t.Fatalf("expected last feature to be %q (appended at the end), got %q", "transport.tailscale", last)
	}
}
