package remote

import "testing"

// isTransientNetworkError 决定 provider 报错是否按网络瞬断自动重试。
// 这里覆盖 provider 端流空闲超时、流中断，以及一条不该重试的普通错误。
func TestIsTransientNetworkError(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"流空闲超时", "Stream idle timeout after 300s", true},
		{"流空闲超时小写", "provider error: stream idle timeout", true},
		{"流在完成前断开", "stream disconnected before completion: upstream closed", true},
		{"普通鉴权错误不重试", "invalid api key", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientNetworkError(tc.input); got != tc.want {
				t.Fatalf("isTransientNetworkError(%q) = %v，期望 %v", tc.input, got, tc.want)
			}
		})
	}
}
