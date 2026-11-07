package loginenv

import "testing"

// scutilProxySample 是一段真实格式的 `scutil --proxy` 输出样例。
const scutilProxySample = `<dictionary> {
  ExceptionsList : <array> {
    0 : localhost
    1 : 127.0.0.1
  }
  HTTPEnable : 1
  HTTPPort : 6152
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 6152
  HTTPSProxy : 127.0.0.1
  SOCKSEnable : 1
  SOCKSPort : 6153
  SOCKSProxy : 127.0.0.1
}
`

func TestParseScutilProxy(t *testing.T) {
	proxy := parseScutilProxy(scutilProxySample)

	want := map[string]string{
		"HTTP_PROXY":  "http://127.0.0.1:6152",
		"http_proxy":  "http://127.0.0.1:6152",
		"HTTPS_PROXY": "http://127.0.0.1:6152",
		"https_proxy": "http://127.0.0.1:6152",
		"ALL_PROXY":   "socks5://127.0.0.1:6153",
		"all_proxy":   "socks5://127.0.0.1:6153",
	}
	if len(proxy) != len(want) {
		t.Fatalf("parseScutilProxy 产出 %d 个 key，期望 %d 个：%v", len(proxy), len(want), proxy)
	}
	for key, value := range want {
		if proxy[key] != value {
			t.Errorf("%s = %q，期望 %q", key, proxy[key], value)
		}
	}
}

func TestParseScutilProxyDisabled(t *testing.T) {
	out := `<dictionary> {
  HTTPEnable : 0
  HTTPPort : 6152
  HTTPProxy : 127.0.0.1
}
`

	proxy := parseScutilProxy(out)
	if _, exists := proxy["HTTP_PROXY"]; exists {
		t.Errorf("HTTPEnable 为 0 时不应产出 HTTP_PROXY，实际：%v", proxy)
	}
	if _, exists := proxy["http_proxy"]; exists {
		t.Errorf("HTTPEnable 为 0 时不应产出 http_proxy，实际：%v", proxy)
	}
}
