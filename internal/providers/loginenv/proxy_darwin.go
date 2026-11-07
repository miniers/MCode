package loginenv

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"mcode/internal/procattr"
)

// 打包后的 app 从 Finder 启动时进程环境里没有 HTTP_PROXY 等变量，
// 而 pi（Node）子进程虽带 NODE_OPTIONS=--use-env-proxy 仍只读环境变量，
// 这里用 scutil 读取系统代理设置补齐。读取失败时返回空 map。
func systemProxyEnvironment() map[string]string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	out, err := procattr.HideWindow(exec.CommandContext(ctx, "/usr/sbin/scutil", "--proxy")).Output()
	if err != nil {
		return map[string]string{}
	}
	return parseScutilProxy(string(out))
}

// parseScutilProxy 解析 `scutil --proxy` 的输出并生成代理环境变量。
// 输出形如：
//
//	<dictionary> {
//	  ExceptionsList : <array> {
//	    0 : localhost
//	  }
//	  HTTPEnable : 1
//	  ...
//	}
//
// 只取顶层标量行：跳过含 <array>/<dictionary>/大括号的行，以及 key 为纯数字的数组元素行。
func parseScutilProxy(out string) map[string]string {
	settings := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.ContainsAny(line, "{}") ||
			strings.Contains(line, "<array>") || strings.Contains(line, "<dictionary>") {
			continue
		}
		key, value, ok := strings.Cut(line, " : ")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		// 数组元素形如 "0 : localhost"，key 是行号，直接跳过
		if key == "" || isDigitKey(key) {
			continue
		}
		settings[key] = value
	}

	proxy := make(map[string]string)
	if host, port, ok := proxyTarget(settings, "HTTPEnable", "HTTPProxy", "HTTPPort"); ok {
		url := "http://" + host + ":" + strconv.Itoa(port)
		proxy["HTTP_PROXY"] = url
		proxy["http_proxy"] = url
	}
	if host, port, ok := proxyTarget(settings, "HTTPSEnable", "HTTPSProxy", "HTTPSPort"); ok {
		// HTTPS 代理同样使用 http:// 前缀
		url := "http://" + host + ":" + strconv.Itoa(port)
		proxy["HTTPS_PROXY"] = url
		proxy["https_proxy"] = url
	}
	if host, port, ok := proxyTarget(settings, "SOCKSEnable", "SOCKSProxy", "SOCKSPort"); ok {
		url := "socks5://" + host + ":" + strconv.Itoa(port)
		proxy["ALL_PROXY"] = url
		proxy["all_proxy"] = url
	}
	return proxy
}

// proxyTarget 校验某一类代理：Enable 为 1、host 非空、端口解析为正整数才视为可用。
func proxyTarget(settings map[string]string, enableKey, hostKey, portKey string) (string, int, bool) {
	if settings[enableKey] != "1" {
		return "", 0, false
	}
	host := settings[hostKey]
	if host == "" {
		return "", 0, false
	}
	port, err := strconv.Atoi(settings[portKey])
	if err != nil || port <= 0 {
		return "", 0, false
	}
	return host, port, true
}

// isDigitKey 判断 key 是否为纯数字（数组元素行号）。
func isDigitKey(key string) bool {
	for _, r := range key {
		if r < '0' || r > '9' {
			return false
		}
	}
	return key != ""
}
