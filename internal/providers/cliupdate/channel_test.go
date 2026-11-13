package cliupdate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"mcode/internal/testenv"
)

// stubEvalSymlinks 替换软链解析，测试结束自动恢复。
func stubEvalSymlinks(t *testing.T, resolve func(string) (string, error)) {
	t.Helper()
	previous := evalSymlinks
	evalSymlinks = resolve
	t.Cleanup(func() { evalSymlinks = previous })
}

// stubClaudeSettings 替换 Claude 设置文件的位置，测试结束自动恢复。
func stubClaudeSettings(t *testing.T, path string) {
	t.Helper()
	previous := claudeSettingsPath
	claudeSettingsPath = func() string { return path }
	t.Cleanup(func() { claudeSettingsPath = previous })
}

// writeClaudeSettings 把设置内容写进临时目录并返回文件路径。
func writeClaudeSettings(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写设置文件出错: %v", err)
	}
	return path
}

func TestDetectChannel(t *testing.T) {
	testenv.TempHome(t)
	tests := []struct {
		name       string
		provider   string
		executable string
		resolved   string
		want       Channel
	}{
		{
			name:       "Homebrew Cellar",
			provider:   "claude",
			executable: "/opt/homebrew/bin/claude",
			resolved:   "/opt/homebrew/Cellar/claude-code/2.0.14/bin/claude",
			want:       ChannelBrew,
		},
		{
			name:       "Homebrew Caskroom",
			provider:   "claude",
			executable: "/usr/local/bin/claude",
			resolved:   "/usr/local/Caskroom/claude-code/2.0.14/claude",
			want:       ChannelBrew,
		},
		{
			name:       "Homebrew libexec 里的 formula",
			provider:   "codex",
			executable: "/opt/homebrew/bin/codex",
			resolved:   "/opt/homebrew/Cellar/codex/0.50.0/libexec/lib/node_modules/@openai/codex/bin/codex.js",
			want:       ChannelBrew,
		},
		{
			name:       "Homebrew node 的 npm 全局不算 brew",
			provider:   "claude",
			executable: "/opt/homebrew/bin/claude",
			resolved:   "/opt/homebrew/lib/node_modules/@anthropic-ai/claude-code/cli.js",
			want:       ChannelNPM,
		},
		{
			name:       "keg-only node 的 npm 全局不算 brew",
			provider:   "claude",
			executable: "/opt/homebrew/opt/node@22/bin/claude",
			resolved:   "/opt/homebrew/Cellar/node@22/22.1.0/lib/node_modules/@anthropic-ai/claude-code/cli.js",
			want:       ChannelNPM,
		},
		{
			name:       "只有 /opt/homebrew 前缀不算 brew",
			provider:   "codex",
			executable: "/opt/homebrew/bin/codex",
			resolved:   "/opt/homebrew/bin/codex",
			want:       ChannelUnknown,
		},
		{
			name:       "npm 全局",
			provider:   "claude",
			executable: "/usr/local/bin/claude",
			resolved:   "/usr/local/lib/node_modules/@anthropic-ai/claude-code/cli.js",
			want:       ChannelNPM,
		},
		{
			name:       "Claude 原生 ~/.claude/local",
			provider:   "claude",
			executable: "/Users/x/.local/bin/claude",
			resolved:   "/Users/x/.claude/local/claude",
			want:       ChannelNative,
		},
		{
			name:       "Claude 原生 ~/.local/share/claude",
			provider:   "claude",
			executable: "/Users/x/.local/bin/claude",
			resolved:   "/Users/x/.local/share/claude/bin/claude",
			want:       ChannelNative,
		},
		{
			name:       "别的 provider 的原生路径算未知",
			provider:   "codex",
			executable: "/Users/x/.local/bin/codex",
			resolved:   "/Users/x/.local/share/claude/bin/codex",
			want:       ChannelUnknown,
		},
		{
			name:       "系统路径不认",
			provider:   "claude",
			executable: "/usr/bin/claude",
			resolved:   "/usr/bin/claude",
			want:       ChannelUnknown,
		},
		{
			name:       "未认出的安装方式",
			provider:   "claude",
			executable: "/Users/x/bin/claude",
			resolved:   "/Users/x/bin/claude",
			want:       ChannelUnknown,
		},
		{
			name:       "空路径不解析软链",
			provider:   "claude",
			executable: "",
			resolved:   "/opt/homebrew/Cellar/claude-code/2.0.14/bin/claude",
			want:       ChannelUnknown,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stubEvalSymlinks(t, func(string) (string, error) { return test.resolved, nil })
			if got := DetectChannel(test.provider, test.executable); got != test.want {
				t.Fatalf("DetectChannel(%q, %q) = %q, want %q", test.provider, test.executable, got, test.want)
			}
		})
	}

	t.Run("解析软链失败时用原路径", func(t *testing.T) {
		stubEvalSymlinks(t, func(string) (string, error) { return "", errors.New("no such file") })
		if got := DetectChannel("claude", "/opt/homebrew/Cellar/claude-code/2.0.14/bin/claude"); got != ChannelBrew {
			t.Fatalf("DetectChannel = %q, want %q", got, ChannelBrew)
		}
	})

	t.Run("软链目标为空时用原路径", func(t *testing.T) {
		stubEvalSymlinks(t, func(string) (string, error) { return "", nil })
		if got := DetectChannel("claude", "/Users/x/lib/node_modules/@anthropic-ai/claude-code/cli.js"); got != ChannelNPM {
			t.Fatalf("DetectChannel = %q, want %q", got, ChannelNPM)
		}
	})
}

func TestClaudeReleaseChannel(t *testing.T) {
	t.Run("stable", func(t *testing.T) {
		stubClaudeSettings(t, writeClaudeSettings(t, `{"autoUpdatesChannel":"stable"}`))
		if got := ClaudeReleaseChannel(); got != "stable" {
			t.Fatalf("ClaudeReleaseChannel = %q, want stable", got)
		}
	})
	t.Run("显式 latest", func(t *testing.T) {
		stubClaudeSettings(t, writeClaudeSettings(t, `{"autoUpdatesChannel":"latest"}`))
		if got := ClaudeReleaseChannel(); got != "latest" {
			t.Fatalf("ClaudeReleaseChannel = %q, want latest", got)
		}
	})
	t.Run("别的取值", func(t *testing.T) {
		stubClaudeSettings(t, writeClaudeSettings(t, `{"autoUpdatesChannel":"beta"}`))
		if got := ClaudeReleaseChannel(); got != "latest" {
			t.Fatalf("ClaudeReleaseChannel = %q, want latest", got)
		}
	})
	t.Run("没有这个键", func(t *testing.T) {
		stubClaudeSettings(t, writeClaudeSettings(t, `{"env":{}}`))
		if got := ClaudeReleaseChannel(); got != "latest" {
			t.Fatalf("ClaudeReleaseChannel = %q, want latest", got)
		}
	})
	t.Run("键不是字符串", func(t *testing.T) {
		stubClaudeSettings(t, writeClaudeSettings(t, `{"autoUpdatesChannel":1}`))
		if got := ClaudeReleaseChannel(); got != "latest" {
			t.Fatalf("ClaudeReleaseChannel = %q, want latest", got)
		}
	})
	t.Run("文件缺失", func(t *testing.T) {
		stubClaudeSettings(t, filepath.Join(t.TempDir(), "settings.json"))
		if got := ClaudeReleaseChannel(); got != "latest" {
			t.Fatalf("ClaudeReleaseChannel = %q, want latest", got)
		}
	})
	t.Run("内容不是 JSON", func(t *testing.T) {
		stubClaudeSettings(t, writeClaudeSettings(t, "not json"))
		if got := ClaudeReleaseChannel(); got != "latest" {
			t.Fatalf("ClaudeReleaseChannel = %q, want latest", got)
		}
	})
	t.Run("路径为空", func(t *testing.T) {
		stubClaudeSettings(t, "")
		if got := ClaudeReleaseChannel(); got != "latest" {
			t.Fatalf("ClaudeReleaseChannel = %q, want latest", got)
		}
	})
	t.Run("默认路径是 ~/.claude/settings.json", func(t *testing.T) {
		home := t.TempDir()
		testenv.SetHome(t, home)
		if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
			t.Fatalf("建目录出错: %v", err)
		}
		path := filepath.Join(home, ".claude", "settings.json")
		if err := os.WriteFile(path, []byte(`{"autoUpdatesChannel":"stable"}`), 0o600); err != nil {
			t.Fatalf("写设置文件出错: %v", err)
		}
		if got := ClaudeReleaseChannel(); got != "stable" {
			t.Fatalf("ClaudeReleaseChannel = %q, want stable（默认路径应为 %s）", got, claudeSettingsPath())
		}
	})
}

func TestUpdateCommandFor(t *testing.T) {
	testenv.TempHome(t)
	missing := filepath.Join(t.TempDir(), "settings.json")
	tests := []struct {
		name     string
		provider string
		channel  Channel
		settings string
		want     []string
	}{
		{
			name:     "Homebrew 的 cask",
			provider: "claude",
			channel:  ChannelBrew,
			want:     []string{"brew", "upgrade", "--cask", "claude-code"},
		},
		{
			name:     "Homebrew 的 formula",
			provider: "codex",
			channel:  ChannelBrew,
			want:     []string{"brew", "upgrade", "--formula", "codex"},
		},
		{
			name:     "Homebrew 表里没有的名字",
			provider: "agy",
			channel:  ChannelBrew,
			want:     []string{"brew", "upgrade", "--formula", "antigravity"},
		},
		{
			name:     "npm 的 Claude 走自有渠道",
			provider: "claude",
			channel:  ChannelNPM,
			settings: `{"autoUpdatesChannel":"stable"}`,
			want:     []string{"npm", "install", "-g", "@anthropic-ai/claude-code@stable"},
		},
		{
			name:     "npm 的 Claude 默认 latest",
			provider: "claude",
			channel:  ChannelNPM,
			want:     []string{"npm", "install", "-g", "@anthropic-ai/claude-code@latest"},
		},
		{
			name:     "npm 的 codex",
			provider: "codex",
			channel:  ChannelNPM,
			want:     []string{"npm", "install", "-g", "@openai/codex@latest"},
		},
		{
			name:     "npm 的 copilot",
			provider: "copilot",
			channel:  ChannelNPM,
			want:     []string{"npm", "install", "-g", "@github/copilot@latest"},
		},
		{
			name:     "npm 的 commandcode",
			provider: "commandcode",
			channel:  ChannelNPM,
			want:     []string{"npm", "install", "-g", "command-code@latest"},
		},
		{
			name:     "npm 的 pi",
			provider: "pi",
			channel:  ChannelNPM,
			want:     []string{"npm", "install", "-g", "@earendil-works/pi-coding-agent@latest"},
		},
		{
			name:     "npm 的 opencode 1.x 包",
			provider: "opencode",
			channel:  ChannelNPM,
			want:     []string{"npm", "install", "-g", "opencode-ai@latest"},
		},
		{
			name:     "npm 的 opencode-go 别名",
			provider: "opencode-go",
			channel:  ChannelNPM,
			want:     []string{"npm", "install", "-g", "opencode-ai@latest"},
		},
		{
			name:     "npm 上表里没有的 provider 回落到自带更新器",
			provider: "antigravity",
			channel:  ChannelNPM,
			want:     []string{"agy", "update"},
		},
		{
			name:     "npm 上没有更新器的 provider 返回 nil",
			provider: "droid",
			channel:  ChannelNPM,
			want:     nil,
		},
		{
			name:     "未知渠道的自带更新器 omp",
			provider: "omp",
			channel:  ChannelUnknown,
			want:     []string{"omp", "update"},
		},
		{
			name:     "未知渠道的自带更新器 cline",
			provider: "cline",
			channel:  ChannelUnknown,
			want:     []string{"cline", "update"},
		},
		{
			name:     "原生安装用自带更新器",
			provider: "claude",
			channel:  ChannelNative,
			want:     []string{"claude", "update"},
		},
		{
			name:     "未知渠道回落到自带更新器",
			provider: "claude",
			channel:  ChannelUnknown,
			want:     []string{"claude", "update"},
		},
		{
			name:     "未知渠道且没有更新器",
			provider: "droid",
			channel:  ChannelUnknown,
			want:     nil,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := missing
			if test.settings != "" {
				path = writeClaudeSettings(t, test.settings)
			}
			stubClaudeSettings(t, path)
			if got := UpdateCommandFor(test.provider, Install{Channel: test.channel}); !slices.Equal(got, test.want) {
				t.Fatalf("UpdateCommandFor(%q, %q) = %v, want %v", test.provider, test.channel, got, test.want)
			}
		})
	}
}

func TestDetectInstall(t *testing.T) {
	testenv.TempHome(t)
	tests := []struct {
		name       string
		provider   string
		executable string
		resolved   string
		want       Install
	}{
		{
			name:       "Homebrew cask 从 Caskroom 取 token",
			provider:   "claude",
			executable: "/opt/homebrew/bin/claude",
			resolved:   "/opt/homebrew/Caskroom/claude-code/2.0.14/claude",
			want:       Install{Channel: ChannelBrew, Token: "claude-code", Cask: true},
		},
		{
			name:       "Homebrew formula 从 Cellar 取 token",
			provider:   "codex",
			executable: "/opt/homebrew/bin/codex",
			resolved:   "/opt/homebrew/Cellar/openai-codex/0.50.0/bin/codex",
			want:       Install{Channel: ChannelBrew, Token: "openai-codex"},
		},
		{
			name:       "npm 全局带 prefix 与包名",
			provider:   "claude",
			executable: "/opt/homebrew/bin/claude",
			resolved:   "/opt/homebrew/lib/node_modules/@anthropic-ai/claude-code/cli.js",
			want:       Install{Channel: ChannelNPM, Package: "@anthropic-ai/claude-code", Prefix: "/opt/homebrew"},
		},
		{
			name:       "bun 全局安装",
			provider:   "opencode",
			executable: "/Users/x/.bun/bin/opencode",
			resolved:   "/Users/x/.bun/install/global/node_modules/opencode-ai/bin/opencode.js",
			want:       Install{Channel: ChannelBun, Package: "opencode-ai"},
		},
		{
			name:       "pnpm 全局安装",
			provider:   "codex",
			executable: "/Users/x/.local/share/pnpm/codex",
			resolved:   "/Users/x/.local/share/pnpm/global/5/node_modules/@openai/codex/bin/codex.js",
			want:       Install{Channel: ChannelPnpm, Package: "@openai/codex"},
		},
		{
			name:       "/usr/bin 系统路径不算 npm",
			provider:   "codex",
			executable: "/usr/bin/codex",
			resolved:   "/usr/bin/codex",
			want:       Install{},
		},
		{
			name:       "/usr/bin 下的 node_modules 也不碰",
			provider:   "codex",
			executable: "/usr/bin/codex",
			resolved:   "/usr/bin/lib/node_modules/@openai/codex/bin/codex.js",
			want:       Install{},
		},
		{
			name:       "/nix 路径不生成 npm 命令",
			provider:   "claude",
			executable: "/run/current-system/sw/bin/claude",
			resolved:   "/nix/store/abc123-claude-code/bin/claude",
			want:       Install{},
		},
		{
			name:       "Claude 原生安装",
			provider:   "claude",
			executable: "/Users/x/.local/bin/claude",
			resolved:   "/Users/x/.claude/local/claude",
			want:       Install{Channel: ChannelNative},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stubEvalSymlinks(t, func(string) (string, error) { return test.resolved, nil })
			if got := DetectInstall(test.provider, test.executable); got != test.want {
				t.Fatalf("DetectInstall(%q, %q) = %+v, want %+v", test.provider, test.executable, got, test.want)
			}
		})
	}
}

// TestUpdateCommandForInstall 验证路径里认出来的 token/包名/prefix 确实进了升级命令。
func TestUpdateCommandForInstall(t *testing.T) {
	testenv.TempHome(t)
	stubClaudeSettings(t, filepath.Join(t.TempDir(), "settings.json"))
	tests := []struct {
		name     string
		provider string
		install  Install
		want     []string
	}{
		{
			name:     "npm 带 --prefix",
			provider: "claude",
			install:  Install{Channel: ChannelNPM, Package: "@anthropic-ai/claude-code", Prefix: "/opt/homebrew"},
			want:     []string{"npm", "install", "-g", "--prefix", "/opt/homebrew", "@anthropic-ai/claude-code@latest"},
		},
		{
			name:     "keg-only node 的 npm prefix",
			provider: "codex",
			install:  Install{Channel: ChannelNPM, Package: "@openai/codex", Prefix: "/opt/homebrew/Cellar/node@22/22.1.0"},
			want:     []string{"npm", "install", "-g", "--prefix", "/opt/homebrew/Cellar/node@22/22.1.0", "@openai/codex@latest"},
		},
		{
			name:     "Homebrew 用路径里的 cask 名",
			provider: "claude",
			install:  Install{Channel: ChannelBrew, Token: "claude-code", Cask: true},
			want:     []string{"brew", "upgrade", "--cask", "claude-code"},
		},
		{
			name:     "Homebrew 用路径里的 formula 名",
			provider: "codex",
			install:  Install{Channel: ChannelBrew, Token: "openai-codex"},
			want:     []string{"brew", "upgrade", "--formula", "openai-codex"},
		},
		{
			name:     "bun 全局升级",
			provider: "opencode",
			install:  Install{Channel: ChannelBun, Package: "opencode-ai"},
			want:     []string{"bun", "add", "-g", "opencode-ai@latest"},
		},
		{
			name:     "pnpm 全局升级",
			provider: "pi",
			install:  Install{Channel: ChannelPnpm, Package: "@earendil-works/pi-coding-agent"},
			want:     []string{"pnpm", "add", "-g", "@earendil-works/pi-coding-agent@latest"},
		},
		{
			name:     "bun 但包里没有包名时回落到自带更新器",
			provider: "antigravity",
			install:  Install{Channel: ChannelBun},
			want:     []string{"agy", "update"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := UpdateCommandFor(test.provider, test.install); !slices.Equal(got, test.want) {
				t.Fatalf("UpdateCommandFor(%q, %+v) = %v, want %v", test.provider, test.install, got, test.want)
			}
		})
	}
}

func TestCheckStableChannel(t *testing.T) {
	testenv.TempHome(t)
	const (
		latestPath = "/@anthropic-ai%2fclaude-code/latest"
		stablePath = "/@anthropic-ai%2fclaude-code/stable"
	)
	tests := []struct {
		name     string
		settings string
		wantPath string
		wantTag  string
	}{
		{
			name:     "stable 渠道查 stable 来源",
			settings: `{"autoUpdatesChannel":"stable"}`,
			wantPath: stablePath,
			wantTag:  "stable",
		},
		{
			name:     "缺省渠道查 latest 来源",
			settings: "",
			wantPath: latestPath,
			wantTag:  "latest",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var path string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.EscapedPath()
				fmt.Fprint(w, `{"version":"2.0.16"}`)
			}))
			defer server.Close()
			stubHTTPClient(t, server.Client())
			stubRegistryURL(t, "claude", server.URL+latestPath)
			stubRegistryURL(t, "claude-stable", server.URL+stablePath)
			settingsPath := filepath.Join(t.TempDir(), "settings.json")
			if test.settings != "" {
				settingsPath = writeClaudeSettings(t, test.settings)
			}
			stubClaudeSettings(t, settingsPath)
			// 目标路径本身就是真实路径，解析软链失败时按原路径判定渠道。
			stubEvalSymlinks(t, func(string) (string, error) { return "", errors.New("no such file") })
			stubRunCommand(t, versionCommand("2.0.15 (Claude Code)"))

			info, err := Check(context.Background(), "claude", "/Users/x/lib/node_modules/@anthropic-ai/claude-code/cli.js")
			if err != nil {
				t.Fatalf("Check 出错: %v", err)
			}
			if path != test.wantPath {
				t.Fatalf("请求路径 = %q, want %q", path, test.wantPath)
			}
			if info.Channel != string(ChannelNPM) {
				t.Fatalf("Info.Channel = %q, want %q", info.Channel, ChannelNPM)
			}
			if info.Latest != "2.0.16" || !info.UpdateAvailable {
				t.Fatalf("版本结果不对: %+v", info)
			}
			// 已安装环境是 /Users/x 下的 npm 全局，命令里带上路径推出的 prefix。
			want := "npm install -g --prefix /Users/x @anthropic-ai/claude-code@" + test.wantTag
			if info.UpdateCommand != want {
				t.Fatalf("UpdateCommand = %q, want %q", info.UpdateCommand, want)
			}
		})
	}
}
