package harness

import "testing"

func TestWithoutFold(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "无查询串",
			in:   "http://127.0.0.1:8080/mcp/srv",
			want: "http://127.0.0.1:8080/mcp/srv?fold=off",
		},
		{
			name: "已有其他参数",
			in:   "http://127.0.0.1:8080/mcp/srv?token=abc&fold=on",
			want: "http://127.0.0.1:8080/mcp/srv?fold=off&token=abc",
		},
		{
			name: "非法URL原样返回",
			in:   "://bad-url",
			want: "://bad-url",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := WithoutFold(tc.in); got != tc.want {
				t.Fatalf("WithoutFold(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
