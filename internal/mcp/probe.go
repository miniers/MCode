package mcp

import (
	"context"
	"slices"
	"time"
)

const (
	// probeDefaultTimeout 单次探测的整体预算。
	probeDefaultTimeout = 90 * time.Second
	// probeOAuthTimeout 经 npx mcp-remote 转发的 OAuth 服务器首次运行需要下载依赖，放宽预算。
	probeOAuthTimeout = 240 * time.Second
)

// ProbeResult 表示对上游 MCP 服务探测的结果。
type ProbeResult struct {
	ServerName    string `json:"serverName,omitempty"`
	ServerVersion string `json:"serverVersion,omitempty"`
	Tools         []Tool `json:"tools"`
}

// Probe 对指定的上游配置发起临时探测，执行 initialize 与 tools/list。
// 整体超时为 90 秒（mcp-remote 形态为 240 秒），探测结束后临时客户端会自动关闭。
func Probe(ctx context.Context, spec UpstreamSpec, tokens TokenSource) (ProbeResult, error) {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout(spec))
	defer cancel()

	client, err := createClient(spec, tokens)
	if err != nil {
		return ProbeResult{}, err
	}
	defer client.close()

	if err := client.initialize(probeCtx); err != nil {
		return ProbeResult{}, err
	}

	name, ver := client.serverInfo()

	tools, err := client.listTools(probeCtx)
	if err != nil {
		return ProbeResult{}, err
	}

	return ProbeResult{
		ServerName:    name,
		ServerVersion: ver,
		Tools:         tools,
	}, nil
}

// probeTimeout 依据上游是否为 mcp-remote 形态选择探测预算。
func probeTimeout(spec UpstreamSpec) time.Duration {
	if slices.Contains(spec.Args, "mcp-remote") {
		return probeOAuthTimeout
	}
	return probeDefaultTimeout
}
