package remote

import "testing"

func TestEntrySecure(t *testing.T) {
	cases := []struct {
		name  string
		entry Entry
		want  bool
	}{
		{name: "lan", entry: EntryLAN, want: false},
		{name: "tls", entry: EntryTLS, want: true},
		{name: "tailscale", entry: EntryTailscale, want: true},
		{name: "empty", entry: Entry(""), want: false},
	}

	for _, tc := range cases {
		if got := tc.entry.Secure(); got != tc.want {
			t.Errorf("entry %q Secure() = %v, want %v", tc.entry, got, tc.want)
		}
	}
}
