package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"mcode/internal/providers/claude"
	"mcode/internal/testenv"
	mcodeui "mcode/internal/ui"
)

// stubClaudeAuth 替换登录态检测的环境与探针：用例既不读真实登录 Shell 环境，也不起子进程。
func stubClaudeAuth(t *testing.T, env []string, probe func(ctx context.Context, binary string, env []string) (claude.AuthStatus, bool)) {
	t.Helper()
	prevEnv, prevProbe := claudeAuthEnv, claudeAuthProbe
	claudeAuthEnv = func() []string { return env }
	claudeAuthProbe = probe
	t.Cleanup(func() {
		claudeAuthEnv = prevEnv
		claudeAuthProbe = prevProbe
	})
}

// TestClaudeLoginStateStatuses 覆盖 claudeLoginState 的四种归类，并验证不需要登录的环境会短路。
func TestClaudeLoginStateStatuses(t *testing.T) {
	cases := []struct {
		name   string
		env    []string
		status claude.AuthStatus
		known  bool
		want   string
	}{
		{
			name:   "已登录",
			env:    []string{"HOME=/tmp"},
			status: claude.AuthStatus{LoggedIn: true, Method: "oauth_token"},
			known:  true,
			want:   mcodeui.ProviderLoginStateLoggedIn,
		},
		{
			name:   "未登录",
			env:    []string{"HOME=/tmp"},
			status: claude.AuthStatus{Method: "none"},
			known:  true,
			want:   mcodeui.ProviderLoginStateLoggedOut,
		},
		{
			name:   "检测未上报",
			env:    []string{"HOME=/tmp"},
			status: claude.AuthStatus{},
			known:  false,
			want:   mcodeui.ProviderLoginStateUnknown,
		},
		{
			name:   "API key 环境不需要登录",
			env:    []string{"HOME=/tmp", "ANTHROPIC_API_KEY=sk-ant-test"},
			status: claude.AuthStatus{LoggedIn: true},
			known:  true,
			want:   mcodeui.ProviderLoginStateNotRequired,
		},
		{
			name:   "Bedrock 环境不需要登录",
			env:    []string{"HOME=/tmp", "CLAUDE_CODE_USE_BEDROCK=1"},
			status: claude.AuthStatus{LoggedIn: true},
			known:  true,
			want:   mcodeui.ProviderLoginStateNotRequired,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probes := 0
			stubClaudeAuth(t, tc.env, func(ctx context.Context, binary string, env []string) (claude.AuthStatus, bool) {
				probes++
				return tc.status, tc.known
			})

			if got := claudeLoginState(context.Background(), "claude"); got != tc.want {
				t.Fatalf("claudeLoginState = %q, want %q", got, tc.want)
			}
			skipped := tc.want == mcodeui.ProviderLoginStateNotRequired
			if skipped && probes != 0 {
				t.Fatalf("不需要登录的环境不应执行检测，probes=%d", probes)
			}
			if !skipped && probes != 1 {
				t.Fatalf("应执行一次检测，probes=%d", probes)
			}
		})
	}
}

func TestClaudeLoginStateSettingsAPIKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"env":{"ANTHROPIC_AUTH_TOKEN":"x"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	stubClaudeAuth(t, []string{"CLAUDE_CONFIG_DIR=" + dir}, func(context.Context, string, []string) (claude.AuthStatus, bool) {
		t.Fatal("配置了第三方 API，不应检测登录态")
		return claude.AuthStatus{}, false
	})
	if got := claudeLoginState(context.Background(), "claude"); got != mcodeui.ProviderLoginStateNotRequired {
		t.Fatalf("claudeLoginState = %q, want %q", got, mcodeui.ProviderLoginStateNotRequired)
	}
}

// stubCodexAuth 替换 codex 登录态检测的环境与探针：用例既不读真实登录 Shell 环境，也不起子进程。
func stubCodexAuth(t *testing.T, env []string, probe func(ctx context.Context, binary string, env []string) ([]byte, error)) {
	t.Helper()
	prevEnv, prevProbe := codexAuthEnv, codexAuthProbe
	codexAuthEnv = func() []string { return env }
	codexAuthProbe = probe
	t.Cleanup(func() {
		codexAuthEnv = prevEnv
		codexAuthProbe = prevProbe
	})
}

// TestCodexLoginState 覆盖 codexLoginState 的三种结果及 Client.ProviderLoginState 对 codex 的分派。
func TestCodexLoginState(t *testing.T) {
	cases := []struct {
		name      string
		cancelCtx bool
		out       string
		err       error
		want      string
	}{
		{
			name: "已登录",
			out:  "Logged in using ChatGPT\n",
			err:  nil,
			want: mcodeui.ProviderLoginStateLoggedIn,
		},
		{
			name: "未登录",
			out:  "Not logged in\n",
			err:  errors.New("exit status 1"),
			want: mcodeui.ProviderLoginStateLoggedOut,
		},
		{
			name: "找不到命令",
			out:  "",
			err:  exec.ErrNotFound,
			want: mcodeui.ProviderLoginStateUnknown,
		},
		{
			name:      "超时或取消",
			cancelCtx: true,
			out:       "Not logged in",
			err:       context.DeadlineExceeded,
			want:      mcodeui.ProviderLoginStateUnknown,
		},
		{
			name: "其他错误输出",
			out:  "unexpected failure",
			err:  errors.New("exit status 2"),
			want: mcodeui.ProviderLoginStateUnknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBinary string
			var hasDeadline bool
			stubCodexAuth(t, []string{"HOME=/tmp"}, func(ctx context.Context, binary string, env []string) ([]byte, error) {
				gotBinary = binary
				_, hasDeadline = ctx.Deadline()
				return []byte(tc.out), tc.err
			})
			ctx := context.Background()
			if tc.cancelCtx {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if got := codexLoginState(ctx, ""); got != tc.want {
				t.Fatalf("codexLoginState = %q, want %q", got, tc.want)
			}
			if gotBinary != "codex" {
				t.Fatalf("binary = %q, want %q", gotBinary, "codex")
			}
			if !hasDeadline {
				t.Fatal("应为 codex 检测设置 5 秒超时")
			}
		})
	}

	t.Run("Client分派codex", func(t *testing.T) {
		testenv.TempHome(t)
		stubProviderBinaryFinder(t)
		probes := 0
		stubCodexAuth(t, []string{"HOME=/tmp"}, func(ctx context.Context, binary string, env []string) ([]byte, error) {
			probes++
			return []byte("Not logged in\n"), errors.New("exit status 1")
		})
		client := &Client{}
		if got := client.ProviderLoginState(context.Background(), " Codex "); got != mcodeui.ProviderLoginStateLoggedOut {
			t.Fatalf("ProviderLoginState(codex) = %q, want %q", got, mcodeui.ProviderLoginStateLoggedOut)
		}
		if probes != 1 {
			t.Fatalf("codex 应执行一次检测，probes=%d", probes)
		}
	})
}

// TestProviderLoginStateUnsupported 验证未接入登录态检测的 Provider 返回空串，claude/codex 正常分派。
func TestProviderLoginStateUnsupported(t *testing.T) {
	testenv.TempHome(t)
	stubProviderBinaryFinder(t)

	claudeProbes := 0
	stubClaudeAuth(t, []string{"HOME=/tmp"}, func(ctx context.Context, binary string, env []string) (claude.AuthStatus, bool) {
		claudeProbes++
		return claude.AuthStatus{Method: "none"}, true
	})
	codexProbes := 0
	stubCodexAuth(t, []string{"HOME=/tmp"}, func(ctx context.Context, binary string, env []string) ([]byte, error) {
		codexProbes++
		return []byte("Logged in\n"), nil
	})
	client := &Client{}

	if got := client.ProviderLoginState(context.Background(), "pi"); got != "" {
		t.Fatalf("pi 没有登录态检测，got %q", got)
	}
	if claudeProbes != 0 || codexProbes != 0 {
		t.Fatalf("不支持的 provider 不应触发检测，claude=%d codex=%d", claudeProbes, codexProbes)
	}

	// ID 大小写与空格不影响识别。
	if got := client.ProviderLoginState(context.Background(), " Claude "); got != mcodeui.ProviderLoginStateLoggedOut {
		t.Fatalf("claude 未登录应返回 %q，got %q", mcodeui.ProviderLoginStateLoggedOut, got)
	}
	if got := client.ProviderLoginState(context.Background(), "codex"); got != mcodeui.ProviderLoginStateLoggedIn {
		t.Fatalf("codex 已登录应返回 %q，got %q", mcodeui.ProviderLoginStateLoggedIn, got)
	}
	if claudeProbes != 1 || codexProbes != 1 {
		t.Fatalf("claude 与 codex 应各执行一次检测，claude=%d codex=%d", claudeProbes, codexProbes)
	}
}
