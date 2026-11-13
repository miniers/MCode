package app

import (
	"context"
	"os"
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

// TestProviderLoginStateOnlyClaude 验证可选接口只对 claude 起检测，其他 Provider 返回空串。
func TestProviderLoginStateOnlyClaude(t *testing.T) {
	testenv.TempHome(t)
	stubProviderBinaryFinder(t)

	probes := 0
	stubClaudeAuth(t, []string{"HOME=/tmp"}, func(ctx context.Context, binary string, env []string) (claude.AuthStatus, bool) {
		probes++
		return claude.AuthStatus{Method: "none"}, true
	})
	client := &Client{}

	if got := client.ProviderLoginState(context.Background(), "codex"); got != "" {
		t.Fatalf("codex 没有登录态检测，got %q", got)
	}
	if probes != 0 {
		t.Fatalf("codex 不应触发检测，probes=%d", probes)
	}

	// ID 大小写与空格不影响识别。
	if got := client.ProviderLoginState(context.Background(), " Claude "); got != mcodeui.ProviderLoginStateLoggedOut {
		t.Fatalf("claude 未登录应返回 %q，got %q", mcodeui.ProviderLoginStateLoggedOut, got)
	}
	if probes != 1 {
		t.Fatalf("claude 应执行一次检测，probes=%d", probes)
	}
}
