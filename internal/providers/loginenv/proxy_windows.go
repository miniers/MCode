//go:build windows

package loginenv

import (
	"strconv"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// internetSettingsKey 是 Windows「Internet 选项」里保存代理配置的注册表位置。
const internetSettingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// systemProxyEnvironment 从当前用户注册表读取系统代理并生成代理环境变量。
// 未启用、缺少 ProxyServer 或读取出错时返回 nil，与 darwin 版的结构保持一致：
// HTTP_PROXY/http_proxy、HTTPS_PROXY/https_proxy、ALL_PROXY/all_proxy、NO_PROXY/no_proxy。
func systemProxyEnvironment() map[string]string {
	key, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer key.Close()

	enable, _, err := key.GetIntegerValue("ProxyEnable")
	if err != nil || enable == 0 {
		return nil
	}

	server, _, err := key.GetStringValue("ProxyServer")
	if err != nil {
		return nil
	}
	proxy := parseWindowsProxyServer(server)
	if len(proxy) == 0 {
		return nil
	}

	if override, _, err := key.GetStringValue("ProxyOverride"); err == nil {
		if noProxy := parseWindowsProxyOverride(override); noProxy != "" {
			proxy["NO_PROXY"] = noProxy
			proxy["no_proxy"] = noProxy
		}
	}
	return proxy
}

// parseWindowsProxyServer 解析 ProxyServer 的两种取值：
// 1. 单条 host:port，Windows 对 HTTP 与 HTTPS 都用它；
// 2. 分号分隔的协议列表，如 http=h:p;https=h:p;socks=h:p。
// 每一项都要求 host 非空且端口为正整数，否则跳过该项。
func parseWindowsProxyServer(server string) map[string]string {
	proxy := make(map[string]string)
	server = strings.TrimSpace(server)
	if server == "" {
		return proxy
	}

	if !strings.Contains(server, "=") {
		if url, ok := proxyURL("http", server); ok {
			proxy["HTTP_PROXY"] = url
			proxy["http_proxy"] = url
			proxy["HTTPS_PROXY"] = url
			proxy["https_proxy"] = url
		}
		return proxy
	}

	for _, part := range strings.Split(server, ";") {
		scheme, target, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		scheme = strings.ToLower(strings.TrimSpace(scheme))
		target = strings.TrimSpace(target)
		switch scheme {
		case "http":
			if url, ok := proxyURL("http", target); ok {
				proxy["HTTP_PROXY"] = url
				proxy["http_proxy"] = url
			}
		case "https":
			// HTTPS 代理同样使用 http:// 前缀，与 darwin 版保持一致
			if url, ok := proxyURL("http", target); ok {
				proxy["HTTPS_PROXY"] = url
				proxy["https_proxy"] = url
			}
		case "socks", "socks5":
			if url, ok := proxyURL("socks5", target); ok {
				proxy["ALL_PROXY"] = url
				proxy["all_proxy"] = url
			}
		}
	}
	return proxy
}

// proxyURL 校验 host:port 并拼成代理 URL；host 为空或端口非法时返回 false。
func proxyURL(scheme, target string) (string, bool) {
	host, port, ok := strings.Cut(target, ":")
	if !ok {
		return "", false
	}
	host = strings.TrimSpace(host)
	port = strings.TrimSpace(port)
	if host == "" {
		return "", false
	}
	if n, err := strconv.Atoi(port); err != nil || n <= 0 {
		return "", false
	}
	return scheme + "://" + host + ":" + port, true
}

// parseWindowsProxyOverride 把 ProxyOverride 转成 NO_PROXY：
// 分号换成逗号，<local> 换成回环地址（Windows 用它表示所有不含点的主机名）。
func parseWindowsProxyOverride(override string) string {
	var items []string
	for _, part := range strings.Split(override, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.EqualFold(part, "<local>") {
			part = "localhost,127.0.0.1"
		}
		items = append(items, part)
	}
	return strings.Join(items, ",")
}
