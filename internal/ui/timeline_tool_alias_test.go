package mcodeui

import "testing"

func TestTimelineToolAliases(t *testing.T) {
	for _, tc := range []struct{ old, current string }{
		{"read_file", "read"},
		{"write_file", "write"},
		{"edit_file", "edit"},
	} {
		t.Run(tc.old, func(t *testing.T) {
			if got, want := toolNameKey(tc.old), toolNameKey(tc.current); got != want {
				t.Errorf("toolNameKey(%q) = %q, want %q", tc.old, got, want)
			}
			old := Message{Role: "tool", ToolName: tc.old, ToolInput: `{"path":"a.go"}`}
			current := Message{Role: "tool", ToolName: tc.current, ToolInput: old.ToolInput}
			if got, want := classifyToolCategory(tc.old), classifyToolCategory(tc.current); got != want {
				t.Errorf("category = %v, want %v", got, want)
			}
			got, _, _ := toolCallTitle(old)
			want, _, _ := toolCallTitle(current)
			if got != want {
				t.Errorf("title prefix = %q, want %q", got, want)
			}
		})
	}
}
