package mcp

import "mcode/internal/harness"

// Servers 组装本轮内置与外部 MCP 服务端点。
func Servers(turn harness.Turn, builtin harness.BuiltinMCP, external []harness.MCPEndpoint) []Server {
	var out []Server
	if url, token, workdirHeader := builtin.Endpoint(turn); url != "" {
		headers := map[string]string{}
		if workdirHeader {
			headers["X-Mcode-Workdir"] = turn.WorkDir
		}
		out = append(out, Server{Name: "mcode", URL: url, Token: token, Headers: headers, Builtin: true})
	}
	for _, endpoint := range external {
		if endpoint.Name == "mcode" || endpoint.Name == "mcode_approval" {
			continue
		}
		headers := map[string]string{}
		if endpoint.Token != "" {
			headers["Authorization"] = "Bearer " + endpoint.Token
		}
		out = append(out, Server{Name: endpoint.Name, URL: endpoint.URL, Headers: headers})
	}
	return out
}
