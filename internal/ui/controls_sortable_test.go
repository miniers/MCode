package mcodeui

import "testing"

func TestSortableAccept(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload sortablePayload
		scope   string
		target  string
		wantID  string
		wantOK  bool
	}{
		{"其他作用域", sortablePayload{"other", "a"}, "list", "b", "", false},
		{"空 ID", sortablePayload{"list", ""}, "list", "b", "", false},
		{"自身", sortablePayload{"list", "a"}, "list", "a", "", false},
		{"同域", sortablePayload{"list", "a"}, "list", "b", "a", true},
		{"容器", sortablePayload{"list", "a"}, "list", "", "a", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, ok := sortableAccept(tc.payload, tc.scope, tc.target)
			if id != tc.wantID || ok != tc.wantOK {
				t.Fatalf("sortableAccept = %q, %v; want %q, %v", id, ok, tc.wantID, tc.wantOK)
			}
		})
	}
}
