package mcodeui

import "testing"

// runtimeModeTitle 把权限档位翻成中文：readonly 显示「只读」，空值回退「自动接受编辑」，
// 未知档位不把原始英文模式名回显给用户。
func TestRuntimeModeTitle(t *testing.T) {
	for _, tc := range []struct{ mode, want string }{
		{mode: "", want: "自动接受编辑"},
		{mode: "auto", want: "自动"},
		{mode: "fullAccess", want: "完全访问"},
		{mode: "readonly", want: "只读"},
		{mode: "futureMode", want: "自定义"},
	} {
		if got := runtimeModeTitle(tc.mode); got != tc.want {
			t.Fatalf("runtimeModeTitle(%q) = %q，期望 %q", tc.mode, got, tc.want)
		}
	}
}

// lastRuntimeModeCall 获取假 Client 最后一次收到的 SetThreadRuntimeMode 调用信息。
func lastRuntimeModeCall(fake *FakeClient) (string, string) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.setRuntimeModeCalls) == 0 {
		return "", ""
	}
	last := fake.setRuntimeModeCalls[len(fake.setRuntimeModeCalls)-1]
	return last.ThreadID, last.Mode
}
