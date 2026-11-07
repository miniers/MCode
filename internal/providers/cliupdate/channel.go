package cliupdate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Channel 是 provider CLI 的安装渠道，决定判定规则与各渠道的升级命令。
// 升级必须走安装它的那个渠道，否则二进制留在 PATH 上的还是旧的那份。
type Channel string

const (
	// ChannelBrew 是 Homebrew 的 formula 或 cask 安装。
	ChannelBrew Channel = "brew"
	// ChannelNPM 是 npm 全局安装。
	ChannelNPM Channel = "npm"
	// ChannelBun 是 bun 全局安装。
	ChannelBun Channel = "bun"
	// ChannelPnpm 是 pnpm 全局安装。
	ChannelPnpm Channel = "pnpm"
	// ChannelNative 是 CLI 自己安装器装的。
	ChannelNative Channel = "native"
	// ChannelUnknown 是没认出来的安装方式: 只能展示版本，升级回落到 CLI 自带更新器。
	ChannelUnknown Channel = ""
)

// evalSymlinks 解析软链；测试可替换。
var evalSymlinks = filepath.EvalSymlinks

// claudeSettingsPath 是 Claude Code 用户设置文件的位置，即
// homeDirectory/.claude/settings.json；测试可替换。
var claudeSettingsPath = func() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".claude", "settings.json")
}

// Install 是一条安装渠道连同等它才决定得了的升级参数: Homebrew 的 formula/cask 名、
// npm 家族的包名与 npm 全局 prefix 都从二进制的真实路径里取。
// 只给 Channel 不够: 用 Homebrew 的 node 做 npm 全局安装时，不带 --prefix 会装到
// PATH 到不了的第二份副本上。
type Install struct {
	Channel Channel
	// Token 是 Homebrew 的 formula/cask 名，只在 ChannelBrew 时有值。
	Token string
	// Cask 表示这个 token 是 Caskroom 下的 cask（升级要用 --cask）。
	Cask bool
	// Package 是 npm/bun/pnpm 全局安装的包名，只在 npm 家族渠道时有值。
	Package string
	// Prefix 是 npm 全局安装的 prefix（node_modules 的上一级目录）。
	Prefix string
}

// DetectInstall 从二进制的真实路径判断安装渠道与升级参数：
// 先解析软链，系统包管理器（/usr/bin、/nix/）的路径不认，再依次看 Homebrew 的
// Caskroom/Cellar、node_modules（npm/bun/pnpm），最后是 Claude 自己的安装器
// （~/.claude/local 与 ~/.local/share/claude），其余算未知。
func DetectInstall(provider, executablePath string) Install {
	if executablePath == "" {
		return Install{}
	}
	resolved := executablePath
	if target, err := evalSymlinks(executablePath); err == nil && target != "" {
		resolved = target
	}
	parts := strings.Split(resolved, "/")
	// 系统包管理器（/usr/bin、Nix）装的二进制不归 npm 管，也不该被塞进一条全局 npm 命令里。
	// 放在 node_modules 判定之前: 这种路径下的 node_modules 升级了也没用。
	if isSystemPath(resolved) {
		return Install{}
	}
	if install, ok := brewInstall(parts); ok {
		return install
	}
	if install, ok := nodeInstall(resolved, parts); ok {
		return install
	}
	if isClaudeNativePath(provider, resolved) {
		return Install{Channel: ChannelNative}
	}
	return Install{}
}

// DetectChannel 只取渠道，判定见 DetectInstall。
func DetectChannel(provider, executablePath string) Channel {
	return DetectInstall(provider, executablePath).Channel
}

// brewInstall 判断二进制是不是 Homebrew 的 cask 或 formula 自己装的:
// `Caskroom/<token>/<version>/…` 或 `Cellar/<token>/<version>/{bin,libexec}/…`，token 就是包名。
// 不能只看 /opt/homebrew/ 前缀，也不能见到 Cellar 就算: 用 Homebrew 的 node 做 npm 全局安装时
// 路径是 /opt/homebrew/lib/node_modules/…，keg-only node 则在 Cellar/node@22/…/lib/node_modules，
// 这两种都归 npm。
func brewInstall(parts []string) (Install, bool) {
	for i, part := range parts {
		if (part != "Caskroom" && part != "Cellar") || i == 0 || i+3 >= len(parts) {
			continue
		}
		cask := part == "Caskroom"
		if !cask && parts[i+3] != "bin" && parts[i+3] != "libexec" {
			continue
		}
		token := parts[i+1]
		if !brewToken(token) {
			continue
		}
		return Install{Channel: ChannelBrew, Token: token, Cask: cask}, true
	}
	return Install{}, false
}

// brewToken 判断 Cellar/Caskroom 下的一段路径是不是合法的 formula/cask 名
// （只接受字母、数字与 + - _ . @）。
func brewToken(token string) bool {
	if token == "" || strings.HasSuffix(token, ".rb") {
		return false
	}
	for _, r := range token {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '+', r == '-', r == '_', r == '.', r == '@':
		default:
			return false
		}
	}
	return true
}

// nodeInstall 从 `…/node_modules/<包名>/…` 里取出包名，并按路径认出 bun / pnpm / npm
// ；不是 node 全局安装的布局时返回 ok=false。
func nodeInstall(resolved string, parts []string) (Install, bool) {
	index := slices.Index(parts, "node_modules")
	if index < 0 || index+1 >= len(parts) {
		return Install{}, false
	}
	name := parts[index+1]
	if strings.HasPrefix(name, "@") && index+2 < len(parts) {
		name += "/" + parts[index+2]
	}
	switch {
	case strings.Contains(resolved, "/.bun/install/global/"):
		return Install{Channel: ChannelBun, Package: name}, true
	case strings.Contains(resolved, "/pnpm/"):
		return Install{Channel: ChannelPnpm, Package: name}, true
	case index >= 2 && parts[index-1] == "lib":
		prefix := strings.Join(parts[:index-1], "/")
		return Install{Channel: ChannelNPM, Package: name, Prefix: prefix}, true
	}
	return Install{}, false
}

// isSystemPath 判断是不是系统包管理器（/usr/bin、Nix）装的: 这类二进制不由 npm 升级。
func isSystemPath(path string) bool {
	return strings.HasPrefix(path, "/usr/bin") || strings.HasPrefix(path, "/nix/")
}

// isClaudeNativePath 判断是不是 Claude Code 自己安装器的目录。
// 这里只认 Claude 的两个安装目录，别的一律算未知。
func isClaudeNativePath(provider, path string) bool {
	if normalizeProvider(provider) != "claude" {
		return false
	}
	return strings.Contains(path, "/.claude/local/") ||
		strings.Contains(path, "/.local/share/claude/")
}

// ClaudeReleaseChannel 读 Claude Code 设置里的 autoUpdatesChannel 并返回发布渠道:
// 值为 "stable" 时返回 "stable"，其余情况（缺文件、坏 JSON、没有这个键、别的取值）
// 一律返回 "latest"。
func ClaudeReleaseChannel() string {
	path := claudeSettingsPath()
	if path == "" {
		return "latest"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "latest"
	}
	var settings struct {
		AutoUpdatesChannel string `json:"autoUpdatesChannel"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return "latest"
	}
	if settings.AutoUpdatesChannel == "stable" {
		return "stable"
	}
	return "latest"
}

// brewPackages 是各 CLI 在 Homebrew 里的 formula/cask 名与类型的兜底表: 正常情况下 token 与
// 是否 cask 由 DetectInstall 从 Caskroom/Cellar 路径推出来，
// 只有拿不到路径时（例如直接按渠道拼命令）才查这张表，表里没有的按归一化后的 provider 名当 formula。
var brewPackages = map[string]struct {
	token string
	cask  bool
}{
	"claude": {token: "claude-code", cask: true},
}

// npmPackages 是各 CLI 在 npm 上的包名；
// OpenCode 这里按 1.x 的包名，2.x 的包名要看已安装版本。
var npmPackages = map[string]string{
	"claude":      "@anthropic-ai/claude-code",
	"codex":       "@openai/codex",
	"copilot":     "@github/copilot",
	"commandcode": "command-code",
	"pi":          "@earendil-works/pi-coding-agent",
	"opencode":    "opencode-ai",
	"opencodego":  "opencode-ai",
}

// UpdateCommandFor 返回按安装渠道升级该 provider 的命令（程序名 + 参数），只返回命令、不执行。
// 渠道对应的命令：
// brew upgrade、npm install -g --prefix、bun/pnpm add -g、CLI 自带更新器；未知渠道回落到 UpdateCommand。
func UpdateCommandFor(provider string, install Install) []string {
	key := normalizeProvider(provider)
	tag := npmTag(provider)
	switch install.Channel {
	case ChannelBrew:
		token, cask := install.Token, install.Cask
		if token == "" {
			token, cask = brewPackage(key)
		}
		flag := "--formula"
		if cask {
			flag = "--cask"
		}
		return []string{"brew", "upgrade", flag, token}
	case ChannelNPM:
		pkg, ok := installPackage(install, key)
		if !ok {
			return UpdateCommand(provider)
		}
		// 带上 CLI 现在所在的那个 prefix，免得另一份 npm（nvm、Homebrew node）装出一份
		// PATH 到不了的副本。
		command := []string{"npm", "install", "-g"}
		if install.Prefix != "" {
			command = append(command, "--prefix", install.Prefix)
		}
		return append(command, pkg+"@"+tag)
	case ChannelBun:
		pkg, ok := installPackage(install, key)
		if !ok {
			return UpdateCommand(provider)
		}
		return []string{"bun", "add", "-g", pkg + "@" + tag}
	case ChannelPnpm:
		pkg, ok := installPackage(install, key)
		if !ok {
			return UpdateCommand(provider)
		}
		return []string{"pnpm", "add", "-g", pkg + "@" + tag}
	default:
		return UpdateCommand(provider)
	}
}

// installPackage 优先用路径里认出来的包名，拿不到时查 npm 包名表。
func installPackage(install Install, key string) (string, bool) {
	if install.Package != "" {
		return install.Package, true
	}
	pkg, ok := npmPackages[key]
	return pkg, ok
}

// brewPackage 取 Homebrew 的 formula/cask 名与类型，表里没有的用归一化后的 provider 名。
func brewPackage(key string) (string, bool) {
	if pkg, ok := brewPackages[key]; ok {
		return pkg.token, pkg.cask
	}
	return key, false
}

// npmTag 是 npm 安装时跟的 dist-tag: Claude 跟随它自己的发布渠道，其余 CLI 都用
// latest。
func npmTag(provider string) string {
	if normalizeProvider(provider) == "claude" {
		return ClaudeReleaseChannel()
	}
	return "latest"
}
