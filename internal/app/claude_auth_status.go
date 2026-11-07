package app

import (
	"context"
	"strings"
	"time"

	"mcode/internal/providers/claude"
	"mcode/internal/providers/loginenv"
	mcodeui "mcode/internal/ui"
)

// 设置页的 claude 行要区分「已安装可用」与「已安装但没登录」：没登录时 CLI 要等到真正
// 发消息才在错误里报出来，行摘要必须提前提示。检测逻辑复用 internal/providers/claude 的
// AuthChecker（60 秒缓存 + 并发去重），这里只负责把结果归类成设置页可渲染的登录态。

// claudeLoginProbeBudget 是设置页单次登录态检测的等待上限：与 claude 包内
// `claude auth status --json` 的硬超时一致，超时按 unknown 处理，摘要保持现状。
const claudeLoginProbeBudget = 5 * time.Second

var (
	// claudeAuthEnv 提供检测子进程的环境：默认取登录 Shell 环境，与启动路径的 claudeEnv
	// 以及真正启动 CLI 时使用的 loginenv.Environ() 一致；单测可替换。
	claudeAuthEnv = loginenv.Environ
	// claudeAuthProbe 执行一次登录态检测；单测可替换成桩，避免起子进程。
	claudeAuthProbe = func(ctx context.Context, binary string, env []string) (claude.AuthStatus, bool) {
		return claude.NewAuthChecker(binary, env).Check(ctx)
	}
)

// claudeLoginState 返回 claude 的本机登录态，取值见 mcodeui.ProviderLoginState*。
// 本地走 API key / 自定义网关 / Bedrock / Vertex 时不需要 claude.ai 登录，直接返回 not_required，
// 不起子进程；检测未上报（超时、命令不存在、输出非 JSON）返回 unknown，由调用方保持原摘要。
func claudeLoginState(ctx context.Context, binary string) string {
	env := claudeAuthEnv()
	if claude.AuthNotRequired(env) {
		return mcodeui.ProviderLoginStateNotRequired
	}
	probeCtx, cancel := context.WithTimeout(ctx, claudeLoginProbeBudget)
	defer cancel()
	status, known := claudeAuthProbe(probeCtx, binary, env)
	if !known {
		return mcodeui.ProviderLoginStateUnknown
	}
	if status.LoggedIn {
		return mcodeui.ProviderLoginStateLoggedIn
	}
	return mcodeui.ProviderLoginStateLoggedOut
}

// ProviderLoginState 返回指定 Provider 的本机登录态（取值见 mcodeui.ProviderLoginState*）。
// 目前只有 claude 做真实检测，其余 Provider 返回空串，设置页据此保持原有摘要。
// 检测会起子进程（claude auth status --json），调用方必须放到后台 goroutine 里，
// 不要放在渲染线程上；检测失败或超时返回 unknown。
func (c *Client) ProviderLoginState(ctx context.Context, provider string) string {
	if strings.ToLower(strings.TrimSpace(provider)) != "claude" {
		return ""
	}
	binary := ""
	if c != nil {
		// 与版本探测同源：优先用设置里配置的可执行文件，留空时按登录 Shell 的 PATH 查找。
		if resolved, err := c.providerExecutable("claude"); err == nil {
			binary = resolved
		}
	}
	return claudeLoginState(ctx, binary)
}
