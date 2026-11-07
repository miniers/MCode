package magpieinject

import (
	"reflect"
	"testing"
)

// TestWithLoopbackNoProxyAddsMissingKeys 校验没有 NO_PROXY / no_proxy 时两个键都会新建。
func TestWithLoopbackNoProxyAddsMissingKeys(t *testing.T) {
	got := WithLoopbackNoProxy([]string{"PATH=/bin"})
	want := []string{
		"PATH=/bin",
		"NO_PROXY=localhost,127.0.0.1,::1",
		"no_proxy=localhost,127.0.0.1,::1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WithLoopbackNoProxy = %#v，想要 %#v", got, want)
	}

	// nil 也应得到两个新键。
	got = WithLoopbackNoProxy(nil)
	if !reflect.DeepEqual(got, want[1:]) {
		t.Fatalf("WithLoopbackNoProxy(nil) = %#v，想要 %#v", got, want[1:])
	}
}

// TestWithLoopbackNoProxyMergesExisting 校验已有值保留原样并去掉重复项，
// 两个键都处理，其它变量不受影响。
func TestWithLoopbackNoProxyMergesExisting(t *testing.T) {
	env := []string{
		"PATH=/bin",
		"NO_PROXY=example.com,127.0.0.1",
		"no_proxy=localhost",
	}
	got := WithLoopbackNoProxy(env)
	want := []string{
		"PATH=/bin",
		"NO_PROXY=example.com,127.0.0.1,localhost,::1",
		"no_proxy=localhost,127.0.0.1,::1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WithLoopbackNoProxy = %#v，想要 %#v", got, want)
	}

	// 入参不能被修改。
	if env[1] != "NO_PROXY=example.com,127.0.0.1" || env[2] != "no_proxy=localhost" {
		t.Fatalf("入参被修改: %#v", env)
	}
}

// TestMergeNoProxy 校验合并规则：空值、大小写去重、已有三项时不重复追加。
func TestMergeNoProxy(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "localhost,127.0.0.1,::1"},
		{"   ", "localhost,127.0.0.1,::1"},
		{"example.com,LocalHost", "example.com,LocalHost,127.0.0.1,::1"},
		{"example.com, 127.0.0.1 ", "example.com, 127.0.0.1 ,localhost,::1"},
		{"localhost,127.0.0.1,::1", "localhost,127.0.0.1,::1"},
		{"example.com,", "example.com,localhost,127.0.0.1,::1"},
	}
	for _, c := range cases {
		if got := MergeNoProxy(c.in); got != c.want {
			t.Errorf("MergeNoProxy(%q) = %q，想要 %q", c.in, got, c.want)
		}
	}
}
