// Package cliupdate 检查 provider CLI 的已安装版本与最新版本，并给出对应的一键更新命令。
//
// 版本来源:
//   - 已安装版本: `<executable> --version` 的输出用正则解析
//   - 最新版本: npm registry、Antigravity 清单、Cursor 安装脚本、grok update --check
//   - 版本比较: 只看数字、按位对齐补零，预发布与构建后缀不参与
//   - 一键更新: 各 CLI 自带更新器
package cliupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
)

// ErrUnsupported 表示该 provider 没有最新版本来源: 只展示版本，不做更新检查，
// 例如 droid、cline、devin、omp。
// 这些 provider 仍有自带更新器（见 selfUpdateCommands），只是没有可比的发布源。
var ErrUnsupported = errors.New("cliupdate: 不支持的 provider")

// Info 是一个 provider CLI 的版本与更新信息。
type Info struct {
	Provider        string
	Installed       string
	Latest          string
	ExecutablePath  string
	Channel         string
	UpdateCommand   string
	UpdateAvailable bool
	// CheckedAt 是这次探针的时间；读缓存的调用方可以据此判断结果有多旧。
	CheckedAt time.Time
}

// runCommand 执行命令并返回合并后的 stdout 与 stderr；测试可替换。
var runCommand = func(ctx context.Context, name string, args ...string) (string, error) {
	// GUI 进程的 PATH 不带登录 Shell 的目录，先按登录环境解析绝对路径；
	// 解析失败时保留原名字，兼容测试里传任意命令名的情况。
	bin := name
	if p, err := loginenv.LookPath(name); err == nil {
		bin = p
	}
	cmd := procattr.HideWindow(exec.CommandContext(ctx, bin, args...))
	// CLI 的版本输出可能受登录环境变量影响，统一用登录环境执行。
	cmd.Env = loginenv.Environ()
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// httpClient 是取最新版本用的 HTTP 客户端，超时 20 秒；测试可替换。
var httpClient = &http.Client{Timeout: 20 * time.Second}

// npmRegistryBase 是 npm registry 的根地址；只有 OpenCode 2.x 的动态包名在运行时用它拼 URL，
// 测试可替换（其余 provider 的地址都在 registryURLs 表里）。
var npmRegistryBase = "https://registry.npmjs.org"

// registryURLs 是各 provider 最新版本的 HTTP 来源，键是 normalizeProvider 归一化后的名字。
// 包名：claude/codex/copilot/commandcode/pi 用各家的官方 npm 包；
// opencode 1.x 用 opencode-ai，2.x 在 latestVersion 里换成 @opencode/cli；Antigravity 用平台清单；
// Cursor 用安装脚本。grok 没有 HTTP 来源（跑 grok update --check），不在这张表里。测试可以逐项替换。
var registryURLs = map[string]string{
	"claude":        npmRegistryURL("@anthropic-ai/claude-code", "latest"),
	"claude-stable": npmRegistryURL("@anthropic-ai/claude-code", "stable"), // 选了 stable 渠道的机器看这个 tag
	"codex":         npmRegistryURL("@openai/codex", "latest"),
	"copilot":       npmRegistryURL("@github/copilot", "latest"),
	"commandcode":   npmRegistryURL("command-code", "latest"),
	"pi":            npmRegistryURL("@earendil-works/pi-coding-agent", "latest"),
	"opencode":      npmRegistryURL("opencode-ai", "latest"),
	"opencodego":    npmRegistryURL("opencode-ai", "latest"),
	"antigravity":   antigravityManifestURL(),
	"cursor":        "https://cursor.com/install",
}

// selfUpdateCommands 是各 CLI 自带更新器的命令（程序名 + 参数），
// 程序名就是各 CLI 的可执行文件名。
var selfUpdateCommands = map[string][]string{
	"claude":      {"claude", "update"},
	"codex":       {"codex", "update"},
	"cursor":      {"cursor-agent", "update"},
	"grok":        {"grok", "update"},
	"copilot":     {"copilot", "update"},
	"antigravity": {"agy", "update"},
	"commandcode": {"cmd", "update"},
	"opencode":    {"opencode", "upgrade"},
	"opencodego":  {"opencode", "upgrade"},
	"pi":          {"pi", "update", "self"},
	"omp":         {"omp", "update"},
	"cline":       {"cline", "update"},
}

// ansiRE 去掉 ANSI 颜色转义。
var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")

// versionRE 是解析 --version 输出用的版本正则：至少两段数字，可带一段预发布后缀。
var versionRE = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?(?:[-.][0-9A-Za-z]+)?`)

// ParseVersion 从 --version 输出里取第一个版本号（"2.0.14 (Claude Code)" → "2.0.14"，
// "codex-cli 0.46.0" → "0.46.0"，"v1.2.3" → "1.2.3"）；找不到时返回空串。
func ParseVersion(output string) string {
	return versionRE.FindString(ansiRE.ReplaceAllString(output, ""))
}

// CompareVersions 比较两个版本：只看数字部分
// （v 前缀、"-beta" 之类的后缀都不参与），短的一侧补零；返回 -1、0 或 1。
// 任一侧没有可用的数字版本（少于两段、含非数字段或空串）时返回 0：那时谁都不比谁新。
func CompareVersions(a, b string) int {
	left, right := versionNumbers(a), versionNumbers(b)
	if left == nil || right == nil {
		return 0
	}
	width := max(len(left), len(right))
	for index := 0; index < width; index++ {
		l, r := 0, 0
		if index < len(left) {
			l = left[index]
		}
		if index < len(right) {
			r = right[index]
		}
		switch {
		case l < r:
			return -1
		case l > r:
			return 1
		}
	}
	return 0
}

// Installed 跑 `<executable> --version` 并解析出版本号，
// 同时带上从路径认出的安装渠道与按渠道生成的升级命令。
func Installed(ctx context.Context, provider, executable string) (Info, error) {
	info := Info{Provider: provider, ExecutablePath: executable, CheckedAt: time.Now()}
	if executable == "" {
		return info, errors.New("cliupdate: executable 为空")
	}
	install := DetectInstall(provider, executable)
	info.Channel = string(install.Channel)
	info.UpdateCommand = strings.Join(UpdateCommandFor(provider, install), " ")
	output, err := runCommand(ctx, executable, "--version")
	if err != nil {
		return info, fmt.Errorf("执行 %s --version: %w", executable, err)
	}
	version := ParseVersion(output)
	if version == "" {
		return info, fmt.Errorf("无法从 %s --version 的输出解析版本号", executable)
	}
	info.Installed = version
	return info, nil
}

// Check 在 Installed 的基础上取最新版本，设置 Latest 与 UpdateAvailable。
// provider 不在来源表里时返回 ErrUnsupported；已安装版本读不出来或来源出错时返回对应错误，
// 同时带回已经拿到的部分信息。
func Check(ctx context.Context, provider, executable string) (Info, error) {
	info := Info{Provider: provider, ExecutablePath: executable, CheckedAt: time.Now()}
	key := normalizeProvider(provider)
	if !supportedProvider(key) {
		return info, fmt.Errorf("%w: %s", ErrUnsupported, provider)
	}
	installed, err := Installed(ctx, provider, executable)
	if err != nil {
		return installed, err
	}
	latest, err := latestVersion(ctx, key, executable, installed.Installed)
	if err != nil {
		return installed, fmt.Errorf("获取 %s 最新版本: %w", provider, err)
	}
	installed.Latest = latest
	installed.UpdateAvailable = CompareVersions(latest, installed.Installed) > 0
	return installed, nil
}

// UpdateCommand 返回一键更新该 provider 所用的命令（程序名 + 参数），只返回命令、不执行。
// 没有自带更新器的 provider（droid、devin 等）返回 nil。
// 注意: 装在包管理器下的二进制要先按渠道升级（Homebrew/npm/bun/pnpm），
// 用 DetectInstall 拿到渠道再调 UpdateCommandFor；这里返回的是不依赖安装渠道的那条。
func UpdateCommand(provider string) []string {
	command, ok := selfUpdateCommands[normalizeProvider(provider)]
	if !ok {
		return nil
	}
	return append([]string(nil), command...)
}

// normalizeProvider 归一化 provider 名: 忽略大小写与 -/_/空格；"agy" 是 antigravity 的 CLI 名。
func normalizeProvider(provider string) string {
	key := strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(provider)))
	if key == "agy" {
		return "antigravity"
	}
	return key
}

// supportedProvider 表示能不能做更新检查: grok 走自己的 update --check，其余看 registryURLs；
// 当前平台没有发布源时（例如 Antigravity 在没有清单的平台上）URL 为空，同样算不支持。
func supportedProvider(key string) bool {
	if key == "grok" {
		return true
	}
	url, ok := registryURLs[key]
	return ok && url != ""
}

// latestVersion 取该 provider 频道会安装的最新版本。
func latestVersion(ctx context.Context, provider, executable, installed string) (string, error) {
	if provider == "grok" {
		return grokLatest(ctx, executable, installed)
	}
	url, ok := registryURLs[provider]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnsupported, provider)
	}
	if provider == "opencode" || provider == "opencodego" {
		// OpenCode 2 是另一个包，1.x 的更新器留在旧包上。
		if major(installed) >= 2 {
			url = npmRegistryURL("@opencode/cli", "latest")
		}
	}
	if provider == "claude" && ClaudeReleaseChannel() == "stable" {
		// 选了 stable 渠道的机器看 stable 那个 tag，免得把 latest 的构建推给它。
		if stable, ok := registryURLs["claude-stable"]; ok {
			url = stable
		}
	}
	parse := parseJSONVersion
	if provider == "cursor" {
		parse = parseCursorInstall
	}
	return fetchLatest(ctx, url, parse)
}

// fetchLatest 拉取来源并解析；非 2xx 报错。
func fetchLatest(ctx context.Context, url string, parse func([]byte) (string, error)) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/json")
	response, err := httpClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("最新版本来源返回 %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return "", err
	}
	return parse(body)
}

// parseJSONVersion 读 JSON 的 version 字段: npm registry 与
// Antigravity 清单同构；不是版本号的取值不算最新版本。
func parseJSONVersion(body []byte) (string, error) {
	var payload struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	if versionNumbers(payload.Version) == nil {
		return "", nil
	}
	return payload.Version, nil
}

// cursorVersionRE 取 Cursor 安装脚本里的构建号。
var cursorVersionRE = regexp.MustCompile(`versions/(\d{4}\.\d{2}\.\d{2}-[0-9a-f]+)`)

// parseCursorInstall 从安装脚本里提取构建号；没有时算没有最新版本。
func parseCursorInstall(body []byte) (string, error) {
	match := cursorVersionRE.FindSubmatch(body)
	if match == nil {
		return "", nil
	}
	return string(match[1]), nil
}

// grokVersionRE 是 grok update --check 输出里的版本。
var grokVersionRE = regexp.MustCompile(`\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?`)

// grokLatest 跑 `grok update --check`，取输出里最后一个版本号；没有可用版本时回落已安装版本。
func grokLatest(ctx context.Context, executable, installed string) (string, error) {
	output, err := runCommand(ctx, executable, "update", "--check")
	if err != nil {
		return "", fmt.Errorf("执行 %s update --check: %w", executable, err)
	}
	matches := grokVersionRE.FindAllString(output, -1)
	if len(matches) == 0 {
		return installed, nil
	}
	return matches[len(matches)-1], nil
}

// versionNumbers 把版本串拆成数字段：去掉空白与 v 前缀，
// 取第一个 -/+ 之前的数字段；少于两段或有非数字段时返回 nil。
func versionNumbers(version string) []int {
	trimmed := strings.TrimLeft(strings.TrimSpace(version), "v")
	numeric := trimmed
	if cut := strings.IndexAny(trimmed, "-+"); cut >= 0 {
		numeric = trimmed[:cut]
	}
	parts := strings.Split(numeric, ".")
	if len(parts) < 2 {
		return nil
	}
	numbers := make([]int, 0, len(parts))
	for _, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil {
			return nil
		}
		numbers = append(numbers, value)
	}
	return numbers
}

// major 取版本号的第一段，读不出来算 0。
func major(version string) int {
	numbers := versionNumbers(version)
	if numbers == nil {
		return 0
	}
	return numbers[0]
}

// npmRegistryURL 拼 npm registry 的 URL，包名里的 / 编码成 %2f。
func npmRegistryURL(pkg, tag string) string {
	name := strings.ReplaceAll(pkg, "/", "%2f")
	return npmRegistryBase + "/" + name + "/" + tag
}

// antigravityManifestBase 是 Antigravity 自动更新器读的清单根地址。
const antigravityManifestBase = "https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/"

// antigravityManifestPlatform 把 GOOS/GOARCH 映射成清单文件名里的平台名，形如 darwin_arm64、
// windows_amd64。服务端只提供 darwin、windows、linux 的 amd64 与 arm64 清单，
// 其他组合返回空串（没有可比的发布源）。
func antigravityManifestPlatform(goos, goarch string) string {
	switch goarch {
	case "amd64", "arm64":
	default:
		return ""
	}
	switch goos {
	case "darwin", "windows", "linux":
		return goos + "_" + goarch
	}
	return ""
}

// antigravityManifestURL 是 Antigravity 更新器读的清单地址，按当前平台选；
// 没有对应清单的平台返回空串，由 supportedProvider 当作没有发布源处理。
// 清单里的 url 与 sha512 指向单个安装文件（Windows 上是 .exe），但这里只用 version 比版本，
// 下载与替换二进制由 CLI 自己的 `agy update` 完成。
func antigravityManifestURL() string {
	platform := antigravityManifestPlatform(runtime.GOOS, runtime.GOARCH)
	if platform == "" {
		return ""
	}
	return antigravityManifestBase + platform + ".json"
}
