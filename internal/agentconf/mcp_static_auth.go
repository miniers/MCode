package agentconf

import "strings"

// StaticAuthServerKeys 返回用静态请求头鉴权的远程 MCP 服务键集合。
// 键为 Agent + ":" + Scope + ":" + Name，与 ScanMCP 内部的去重键一致。
// 这类服务（如带 Authorization 或 X-*-API-Key 头）不需要 OAuth 授权，
// 设置页据此不显示「登录」按钮，免得用户点进去空等五分钟。
func StaticAuthServerKeys(opts Options) map[string]bool {
	// 设置页正常扫描不带凭据，这里必须单独置位才看得到 Headers 的取值。
	credOpts := opts
	credOpts.WithCredentials = true

	servers, err := ScanMCP(credOpts)
	if err != nil {
		return map[string]bool{}
	}

	keys := make(map[string]bool)
	for _, srv := range servers {
		if strings.TrimSpace(srv.URL) == "" || !hasStaticAuthHeader(srv.Headers) {
			continue
		}
		keys[srv.Agent+":"+srv.Scope+":"+srv.Name] = true
	}
	return keys
}

// hasStaticAuthHeader 判断请求头里是否带了可用的静态凭据。
func hasStaticAuthHeader(headers map[string]string) bool {
	for key, value := range headers {
		if !isSecretHeaderKey(key) {
			continue
		}
		if trimAuthScheme(value) != "" {
			return true
		}
	}
	return false
}

// trimAuthScheme 去掉首尾空白与前缀 Bearer，返回真正参与鉴权的凭据文本。
func trimAuthScheme(value string) string {
	trimmed := strings.TrimSpace(value)
	const scheme = "Bearer"
	if len(trimmed) >= len(scheme) && strings.EqualFold(trimmed[:len(scheme)], scheme) {
		trimmed = strings.TrimSpace(trimmed[len(scheme):])
	}
	return trimmed
}
