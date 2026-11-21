package app

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"mcode/internal/procattr"
	"mcode/internal/providers/claude"
	"mcode/internal/providers/loginenv"
	mcodeui "mcode/internal/ui"
)

// 设置页与新手引导的 claude / codex 行要区分「已安装可用」与「已安装但没登录」：没登录时 CLI 要等到真正
// 发消息才在错误里报出来，行摘要必须提前提示。claude 复用 internal/providers/claude 的
// AuthChecker（60 秒缓存 + 并发去重），codex 执行 `codex login status`，这里统一归类成登录态常量。

const (
	// claudeLoginProbeBudget 是设置页单次 claude 登录态检测的等待上限：与 claude 包内
	// `claude auth status --json` 的硬超时一致，超时按 unknown 处理，摘要保持现状。
	claudeLoginProbeBudget = 5 * time.Second
	// codexLoginProbeBudget 是单次 `codex login status` 检测的超时上限。
	codexLoginProbeBudget = 5 * time.Second
)

var (
	// claudeAuthEnv 提供检测子进程的环境：默认取登录 Shell 环境，与启动路径的 claudeEnv
	// 以及真正启动 CLI 时使用的 loginenv.Environ() 一致；单测可替换。
	claudeAuthEnv = loginenv.Environ
	// claudeAuthProbe 执行一次登录态检测；单测可替换成桩，避免起子进程。
	claudeAuthProbe = func(ctx context.Context, binary string, env []string) (claude.AuthStatus, bool) {
		return claude.NewAuthChecker(binary, env).Check(ctx)
	}
	// codexAuthEnv 提供 codex 检测子进程的环境；单测可替换。
	codexAuthEnv = loginenv.Environ
	// codexAuthProbe 执行一次 `codex login status` 检测并返回合并输出与错误；单测可替换成桩。
	codexAuthProbe = func(ctx context.Context, binary string, env []string) ([]byte, error) {
		cmd := procattr.HideWindow(exec.CommandContext(ctx, binary, "login", "status"))
		if len(env) > 0 {
			cmd.Env = env
		}
		return cmd.CombinedOutput()
	}
)

// claudeLoginState 返回 claude 的本机登录态，取值见 mcodeui.ProviderLoginState*。
// 本地走 API key / 自定义网关 / Bedrock / Vertex 时不需要 claude.ai 登录，直接返回 not_required，
// 不起子进程；检测未上报（超时、命令不存在、输出非 JSON）返回 unknown，由调用方保持原摘要。
func claudeLoginState(ctx context.Context, binary string) string {
	env := claudeAuthEnv()
	if claude.UsesThirdPartyAPI(env) {
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

// codexLoginState 返回 codex 的本机登录态，取值见 mcodeui.ProviderLoginState*。
// 执行 `<codex> login status`（带 5 秒超时）：退出码 0 返回 logged_in；
// 退出码非 0 且输出包含 "Not logged in" 返回 logged_out；找不到命令、超时或其他情况返回 unknown。
func codexLoginState(ctx context.Context, binary string) string {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(binary) == "" {
		binary = "codex"
	}
	probeCtx, cancel := context.WithTimeout(ctx, codexLoginProbeBudget)
	defer cancel()
	out, err := codexAuthProbe(probeCtx, binary, codexAuthEnv())
	if probeCtx.Err() != nil {
		return mcodeui.ProviderLoginStateUnknown
	}
	if err == nil {
		return mcodeui.ProviderLoginStateLoggedIn
	}
	if strings.Contains(string(out), "Not logged in") {
		return mcodeui.ProviderLoginStateLoggedOut
	}
	return mcodeui.ProviderLoginStateUnknown
}

// ProviderLoginState 返回指定 Provider 的本机登录态（取值见 mcodeui.ProviderLoginState*）。
// 支持 claude（`claude auth status --json`）与 codex（`codex login status`），其余 Provider 返回空串，
// 设置页据此保持原有摘要。检测会起子进程，调用方必须放到后台 goroutine 里，不要放在渲染线程上；
// 检测失败或超时返回 unknown。
func (c *Client) ProviderLoginState(ctx context.Context, provider string) string {
	key := strings.ToLower(strings.TrimSpace(provider))
	if key != "claude" && key != "codex" {
		return ""
	}
	binary := ""
	if c != nil {
		// 与版本探测同源：优先用设置里配置的可执行文件，留空时按登录 Shell 的 PATH 查找。
		if resolved, err := c.providerExecutable(key); err == nil {
			binary = resolved
		}
	}
	if key == "codex" {
		return codexLoginState(ctx, binary)
	}
	return claudeLoginState(ctx, binary)
}
