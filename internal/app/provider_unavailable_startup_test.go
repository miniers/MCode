package app

import (
	"context"
	"testing"

	"mcode/internal/config"
	"mcode/internal/providers/claude"
)

// missingCLIBin 是一个必然不存在于 PATH 的可执行文件名，避免用例受开发机已装 CLI 的影响。
const missingCLIBin = "mcode-not-installed-cli-测试占位"

// TestOpenContinuesWhenProviderCLIUnavailable 覆盖「所选 provider 的 CLI 缺失时仍能启动」：
// 以前这些情况会让 Open 直接返回致命错误，桌面端在没装 CLI 的机器上根本打不开。
func TestOpenContinuesWhenProviderCLIUnavailable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	existingBin := testClaudeBin(t)

	cases := []struct {
		name        string
		provider    string
		claudeBin   string
		codexBin    string
		wantAbsent  []string
		wantPresent []string
	}{
		{
			name:       "claude 缺失",
			provider:   "claude",
			claudeBin:  missingCLIBin,
			codexBin:   missingCLIBin,
			wantAbsent: []string{"claude", "codex"},
		},
		{
			// 选 claude 但 claude 不可用、codex 可用时，现有逻辑把 codex 设为主 harness。
			name:        "claude 缺失但 codex 可用",
			provider:    "claude",
			claudeBin:   missingCLIBin,
			codexBin:    existingBin,
			wantAbsent:  []string{"claude"},
			wantPresent: []string{"codex"},
		},
		{
			name:       "codex 缺失",
			provider:   "codex",
			claudeBin:  missingCLIBin,
			codexBin:   missingCLIBin,
			wantAbsent: []string{"claude", "codex"},
		},
		{
			name:       "agy 未启用",
			provider:   "agy",
			claudeBin:  missingCLIBin,
			codexBin:   missingCLIBin,
			wantAbsent: []string{"agy", "claude", "codex"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			cfg := config.Defaults()
			// agy 默认启用；关掉它既能覆盖「provider agy 不可用」，也避免用例初始化账号管理器。
			cfg.Providers.Agy.Enabled = false
			if err := config.Save(dataDir, cfg); err != nil {
				t.Fatalf("config.Save failed: %v", err)
			}

			appl, err := Open(Options{
				DataDir:   dataDir,
				Provider:  tc.provider,
				WorkDir:   t.TempDir(),
				Mode:      "agent",
				ClaudeBin: tc.claudeBin,
				CodexBin:  tc.codexBin,
			})
			if err != nil {
				t.Fatalf("Open 应在 provider %q 不可用时仍启动成功，实际返回: %v", tc.provider, err)
			}
			defer appl.Close()

			for _, kind := range tc.wantAbsent {
				if h := appl.backend.RegisteredHarness(kind); h != nil {
					t.Fatalf("期望 %s 未注册，实际注册了 %T", kind, h)
				}
			}
			for _, kind := range tc.wantPresent {
				if h := appl.backend.RegisteredHarness(kind); h == nil {
					t.Fatalf("期望 %s 已注册，实际未注册", kind)
				}
			}
		})
	}
}

// TestOpenContinuesWhenClaudeLoggedOut 覆盖「claude 已安装但未登录」：以前 Open 返回 ErrNotLoggedIn，
// 现在只记警告，不注册 claude harness。
func TestOpenContinuesWhenClaudeLoggedOut(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// 进程环境与登录环境都没有凭据，登录态检测上报「未登录」。
	notLoggedIn := func(ctx context.Context, binary string, env []string) (claude.AuthStatus, bool) {
		return claude.AuthStatus{}, true
	}
	stubClaudeProbe(t, []string{"HOME=/tmp"}, []string{"HOME=/tmp"}, notLoggedIn, notLoggedIn)

	dataDir := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers.Agy.Enabled = false
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("config.Save failed: %v", err)
	}

	appl, err := Open(Options{
		DataDir:   dataDir,
		Provider:  "claude",
		WorkDir:   t.TempDir(),
		Mode:      "agent",
		ClaudeBin: testClaudeBin(t),
		CodexBin:  missingCLIBin,
	})
	if err != nil {
		t.Fatalf("Open 应在 claude 未登录时仍启动成功，实际返回: %v", err)
	}
	defer appl.Close()

	if h := appl.backend.RegisteredHarness("claude"); h != nil {
		t.Fatalf("期望 claude 未注册，实际注册了 %T", h)
	}
}
