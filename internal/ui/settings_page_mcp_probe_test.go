package mcodeui

import (
	"errors"
	"testing"

	"mcode/internal/agentconf"
)

// TestWithProbeCredentials 覆盖：设置页列表里的服务已脱敏，探测前要补回 Env 与 Headers；
// 只补这两个字段，重扫失败或找不到目标时原样返回。
func TestWithProbeCredentials(t *testing.T) {
	orig := scanMCP
	t.Cleanup(func() { scanMCP = orig })

	listed := agentconf.MCPServer{
		Agent: "claude", Scope: "global", Name: "nowledge-mem",
		Transport: "http", URL: "https://mem.example.com/mcp",
	}
	var gotWithCreds bool
	scanMCP = func(opts agentconf.Options) ([]agentconf.MCPServer, error) {
		gotWithCreds = opts.WithCredentials
		other := listed
		other.Name = "other"
		other.Headers = map[string]string{"Authorization": "Bearer other"}
		full := listed
		full.Untrusted = true // 以列表里的信任判定为准，不应被重扫结果带偏
		full.Headers = map[string]string{"Authorization": "Bearer secret"}
		full.Env = map[string]string{"TOKEN": "t"}
		return []agentconf.MCPServer{other, full}, nil
	}

	got := withProbeCredentials(agentconf.Options{}, listed)
	if !gotWithCreds {
		t.Fatal("重扫必须开启 WithCredentials")
	}
	if got.Headers["Authorization"] != "Bearer secret" || got.Env["TOKEN"] != "t" {
		t.Fatalf("未补回凭据：headers=%v env=%v", got.Headers, got.Env)
	}
	if got.Untrusted {
		t.Fatal("不应覆盖列表里的 Untrusted 判定")
	}

	scanMCP = func(agentconf.Options) ([]agentconf.MCPServer, error) { return nil, errors.New("boom") }
	if got := withProbeCredentials(agentconf.Options{}, listed); got.Headers != nil || got.Env != nil {
		t.Fatalf("重扫失败应原样返回：%+v", got)
	}

	scanMCP = func(agentconf.Options) ([]agentconf.MCPServer, error) { return nil, nil }
	if got := withProbeCredentials(agentconf.Options{}, listed); got.Headers != nil || got.Env != nil {
		t.Fatalf("找不到目标应原样返回：%+v", got)
	}
}
