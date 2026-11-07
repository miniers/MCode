package magpieinject

// 本文件把 pi 原有的 NO_PROXY 补全逻辑原样搬过来，供 claude 等 agent 共用。
// 来源：internal/providers/pi/pi.go 的 mergeNoProxy 与 withLoopbackNoProxy。
// pi 自己那份保持不动，避免行为回归。

import "strings"

// MergeNoProxy 把 localhost、127.0.0.1、::1 合并进逗号分隔的代理忽略名单：
// 保留原值，已有的项不重复追加（大小写不敏感）。
// 来源：internal/providers/pi/pi.go:196 的 mergeNoProxy。
func MergeNoProxy(val string) string {
	required := []string{"localhost", "127.0.0.1", "::1"}
	if strings.TrimSpace(val) == "" {
		return strings.Join(required, ",")
	}
	parts := strings.Split(val, ",")
	seen := make(map[string]bool)
	for _, p := range parts {
		trimmed := strings.ToLower(strings.TrimSpace(p))
		if trimmed != "" {
			seen[trimmed] = true
		}
	}
	res := val
	for _, req := range required {
		if !seen[strings.ToLower(req)] {
			if res != "" && !strings.HasSuffix(res, ",") {
				res += ","
			}
			res += req
			seen[strings.ToLower(req)] = true
		}
	}
	return res
}

// WithLoopbackNoProxy 把 localhost、127.0.0.1、::1 合并进 NO_PROXY 和 no_proxy：
// 保留原值，已有的项不重复追加，变量不存在就新建。返回新切片，不修改入参。
// 来源：internal/providers/pi/pi.go:224 的 withLoopbackNoProxy。
func WithLoopbackNoProxy(env []string) []string {
	keys := []string{"NO_PROXY", "no_proxy"}
	result := make([]string, len(env))
	copy(result, env)

	for _, key := range keys {
		prefix := key + "="
		found := false
		for i := len(result) - 1; i >= 0; i-- {
			if strings.HasPrefix(result[i], prefix) {
				val := result[i][len(prefix):]
				result[i] = prefix + MergeNoProxy(val)
				found = true
				break
			}
		}
		if !found {
			result = append(result, prefix+MergeNoProxy(""))
		}
	}
	return result
}
