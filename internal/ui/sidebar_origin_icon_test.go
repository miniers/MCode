package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestThreadOriginIcon(t *testing.T) {
	tests := []struct {
		name string
		th   Thread
		want *ui.SVG
	}{
		{
			name: "普通会话",
			th:   Thread{},
			want: nil,
		},
		{
			name: "转发会话",
			th: Thread{
				ForkedFromThreadID: "th-origin",
			},
			want: IconReply(),
		},
		{
			name: "分支会话",
			th: Thread{
				ForkedFromThreadID:  "th-origin",
				ForkedFromMessageID: "msg-123",
			},
			want: IconGitBranch(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := threadOriginIcon(tt.th)
			if got != tt.want {
				t.Errorf("threadOriginIcon(%+v) = %p, want %p", tt.th, got, tt.want)
			}
		})
	}
}
