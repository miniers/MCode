package omp

// 本文件读 omp 自己上报的、它已登录的每个底层 provider 的订阅窗口
// （`omp usage --json --no-extensions`），归一化成
// internal/providers/credits 的 Window 额度契约。
//
// omp 把登录存在自己的 agent.db 里（那个库不该被外部读），所以额度也从 omp 自己问出来，
// 而不是像 pi 那样读 auth.json。窗口的展示细节（detail 文案、planName）在 credits.Window
// 里没有落点，见 README 的尚未实现的能力清单。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
	"unicode"

	"mcode/internal/procattr"
	"mcode/internal/providers/credits"
	"mcode/internal/providers/loginenv"
)

const (
	// ompUsageTimeout 是一次 `omp usage` 读取的超时（45 秒）。
	ompUsageTimeout = 45 * time.Second
	// ompMaxReportAge 是 omp 断网时给出的 last-good 报告的新鲜度上限：比这更旧的窗口读起来
	// 像活的，所以丢掉（新鲜度上限 6 小时）。
	ompMaxReportAge = 6 * time.Hour
)

// ompUsageArgs 是读取用量与订阅窗口的子命令与参数。
var ompUsageArgs = []string{"usage", "--json", "--no-extensions"}

// ompProviderNames 是 provider 标识到展示名的映射。
var ompProviderNames = map[string]string{
	"anthropic":          "Claude",
	"openai-codex":       "Codex",
	"github-copilot":     "Copilot",
	"google-gemini-cli":  "Gemini",
	"google-antigravity": "Gemini",
	"zai":                "Z.ai",
	"commandcode":        "Command Code",
}

// PlanLimitsRunner 执行一次 omp 子命令并返回它的 stdout。
// 测试注入替身，不启动真实 omp、不读真实账号。
type PlanLimitsRunner func(ctx context.Context, executable string, args []string, dir string, env []string) ([]byte, error)

// PlanLimitsOptions 是一次额度读取的依赖与来源配置，测试时全部可替换。
type PlanLimitsOptions struct {
	// Executable 是 omp 可执行文件路径；为空时用 DefaultBinary。
	Executable string
	// Env 是环境变量来源；为 nil 时读进程环境。
	Env map[string]string
	// HomeDir 覆盖 omp 子进程的工作目录；为空时用 env["HOME"] 或 os.UserHomeDir()。
	HomeDir string
	// Run 覆盖命令执行器；为空时以 exec.CommandContext 启动 omp。
	Run PlanLimitsRunner
	// Now 覆盖当前时间；为空时用 time.Now。
	Now func() time.Time
	// MaxReportAge 覆盖 last-good 报告的新鲜度上限；为零值时用 ompMaxReportAge。
	MaxReportAge time.Duration
	// Timeout 覆盖单次读取的超时；为零值时用 ompUsageTimeout。
	Timeout time.Duration
}

func (o PlanLimitsOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o PlanLimitsOptions) maxReportAge() time.Duration {
	if o.MaxReportAge > 0 {
		return o.MaxReportAge
	}
	return ompMaxReportAge
}

// home 返回 omp 子进程的工作目录：HomeDir 优先，其次 env["HOME"]，最后 os.UserHomeDir。
func (o PlanLimitsOptions) home() string {
	if base := strings.TrimSpace(o.HomeDir); base != "" {
		return base
	}
	if o.Env != nil {
		if base := strings.TrimSpace(o.Env["HOME"]); base != "" {
			return base
		}
	}
	resolved, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return resolved
}

// environ 返回子进程的环境变量列表：Env 非 nil 时按键排序输出（测试可复现），
// 否则继承登录 shell 环境（GUI 进程自己的 PATH 里没有 node 全局 bin）。
func (o PlanLimitsOptions) environ() []string {
	if o.Env == nil {
		return loginenv.Environ()
	}
	keys := make([]string, 0, len(o.Env))
	for key := range o.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+o.Env[key])
	}
	return env
}

// PlanLimits 读取 omp 已登录的底层 provider 的订阅窗口并归一化成 credits.Window。
//
// 没有 omp、命令失败或没有可上报的窗口时返回 (nil, nil)：面板什么都不显示，而不是提示登录
// （不显示任何窗口）。命令执行器、home、env 与时间都可注入。
func PlanLimits(ctx context.Context, opts PlanLimitsOptions) ([]credits.Window, error) {
	executable := opts.Executable
	if executable == "" {
		executable = DefaultBinary
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = ompUsageTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	run := opts.Run
	if run == nil {
		run = ompCommandRunner
	}
	stdout, err := run(runCtx, executable, ompUsageArgs, opts.home(), opts.environ())
	if err != nil {
		// omp 缺失、失败或没有额度可报时按“没有窗口”处理，不是错误。
		return nil, nil
	}
	windows := planWindows(stdout, opts.now(), opts.maxReportAge())
	if len(windows) == 0 {
		return nil, nil
	}
	return windows, nil
}

// ompCommandRunner 是默认的命令执行器：启动真实的 omp 子命令并读回 stdout。
func ompCommandRunner(ctx context.Context, executable string, args []string, dir string, env []string) ([]byte, error) {
	cmd := procattr.HideWindow(exec.CommandContext(ctx, executable, args...))
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("omp: %s %s: %w", executable, strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

// planWindows 是 `omp usage --json` 载荷到 credits.Window 的纯映射：
// 每个 report 的每个 limit 各是一个窗口，断网时的旧报告被丢弃，同一 provider 的多个账号
// 用账号标签区分重名窗口。
func planWindows(payload []byte, now time.Time, maxAge time.Duration) []credits.Window {
	// omp 会在 JSON 之前打日志，从第一个 "{" 开始解析。
	start := bytes.IndexByte(payload, '{')
	if start < 0 {
		return nil
	}
	root := jsonObject(payload[start:])
	if root == nil {
		return nil
	}

	type row struct {
		window       credits.Window
		provider     string
		account      string
		accountLabel string
	}
	var rows []row
	for index, raw := range jsonArray(root["reports"]) {
		report := jsonObject(raw)
		provider := jsonText(report, "provider")
		limits := jsonArray(report["limits"])
		if provider == "" || len(limits) == 0 {
			continue
		}
		if fetched := jsonFloat(report["fetchedAt"]); fetched != nil {
			if now.Sub(time.UnixMilli(int64(*fetched))) > maxAge {
				continue
			}
		}
		metadata := jsonObject(report["metadata"])
		email := jsonText(metadata, "email")
		account := firstNonEmpty(email, jsonText(metadata, "accountId"), fmt.Sprintf("%d", index))
		label := "account " + fmt.Sprintf("%d", index+1)
		if email != "" {
			label = strings.Split(email, "@")[0]
		}
		prefix := ompProviderPrefix(provider)
		for _, rawLimit := range limits {
			limit := jsonObject(rawLimit)
			id := jsonText(limit, "id")
			if id == "" {
				continue
			}
			window := ompWindow(limit, prefix, now)
			if window == nil {
				continue
			}
			rows = append(rows, row{window: *window, provider: provider, account: account, accountLabel: label})
		}
	}
	if len(rows) == 0 {
		return nil
	}

	// 同一 provider 的两个账号会出现一模一样的标题，加上账号标签区分。
	accounts := map[string]map[string]bool{}
	for _, entry := range rows {
		key := entry.window.Label + "\x00" + entry.provider
		if accounts[key] == nil {
			accounts[key] = map[string]bool{}
		}
		accounts[key][entry.account] = true
	}
	windows := make([]credits.Window, 0, len(rows))
	for _, entry := range rows {
		window := entry.window
		if len(accounts[window.Label+"\x00"+entry.provider]) > 1 {
			window.Label += " (" + entry.accountLabel + ")"
		}
		windows = append(windows, window)
	}
	return windows
}

// ompWindow 把一个 limit 归一化为 credits.Window；取不到已用比例时返回 nil
// （取不到已用比例时返回 nil）。credits.Window 没有 detail 与窗口 id 字段，
// 那部分文案与标识不上报。
func ompWindow(limit map[string]json.RawMessage, prefix string, now time.Time) *credits.Window {
	amount := jsonObject(limit["amount"])
	unit := jsonText(amount, "unit")
	if unit == "" {
		unit = "unknown"
	}
	used, hasUsed := ompNumber(amount, "used")
	limitValue, hasLimit := ompNumber(amount, "limit")

	fraction, ok := ompUsedFraction(amount, unit, used, hasUsed, limitValue, hasLimit, jsonText(limit, "status"))
	if !ok {
		return nil
	}

	window := jsonObject(limit["window"])
	title := ""
	if durationMs, hasDuration := ompNumber(window, "durationMs"); hasDuration {
		title = ompWindowTitle(int(durationMs / 60_000))
	} else {
		title = jsonText(limit, "label")
		if strings.HasPrefix(title, prefix+" ") {
			title = title[len(prefix)+1:]
		}
		if title == "" {
			title = "Usage limit"
		}
	}

	scope := jsonObject(limit["scope"])
	extra := firstNonEmpty(jsonText(scope, "tier"), jsonText(scope, "modelId"))
	if extra != "" && !strings.Contains(strings.ToLower(title), strings.ToLower(extra)) {
		title += " · " + extra
	}

	var resetsAt time.Time
	if at, ok := ompNumber(window, "resetsAt"); ok {
		if parsed := time.UnixMilli(int64(at)); parsed.After(now) {
			resetsAt = parsed
		}
	}

	return &credits.Window{
		Label:       prefix + " · " + title,
		UsedPercent: math.Min(math.Max(fraction, 0), 1) * 100,
		ResetsAt:    resetsAt,
	}
}

// ompUsedFraction 按 omp 自己的 resolveUsedFraction 顺序求已用比例。
func ompUsedFraction(amount map[string]json.RawMessage, unit string, used float64, hasUsed bool, limitValue float64, hasLimit bool, status string) (float64, bool) {
	if value, ok := ompNumber(amount, "usedFraction"); ok {
		return value, true
	}
	if hasUsed && hasLimit && limitValue > 0 {
		return used / limitValue, true
	}
	if unit == "percent" && hasUsed {
		return used / 100, true
	}
	if remaining, ok := ompNumber(amount, "remainingFraction"); ok {
		return 1 - remaining, true
	}
	if status == "exhausted" {
		return 1, true
	}
	return 0, false
}

// ompWindowTitle 返回用量窗口的展示标题（pi 包的同名映射没有导出，这里按 omp
// 的路径复刻）。
func ompWindowTitle(minutes int) string {
	switch minutes {
	case 300:
		return "5-hour limit"
	case 1440:
		return "Daily"
	case 10080:
		return "Weekly"
	}
	if minutes >= 40320 && minutes <= 44640 {
		return "Monthly"
	}
	if minutes%1440 == 0 {
		return fmt.Sprintf("%d-day limit", minutes/1440)
	}
	hours := minutes / 60
	if hours < 1 {
		hours = 1
	}
	return fmt.Sprintf("%d-hour limit", hours)
}

// ompProviderPrefix 返回窗口标题里的 provider 展示名：查表命中用表里的名字，
// 否则首字母大写。
func ompProviderPrefix(provider string) string {
	if name, ok := ompProviderNames[provider]; ok {
		return name
	}
	runes := []rune(provider)
	if len(runes) == 0 {
		return provider
	}
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

// ompNumber 取对象里某个键的数值；缺失或不是数字时返回 (0, false)。
func ompNumber(object map[string]json.RawMessage, key string) (float64, bool) {
	if object == nil {
		return 0, false
	}
	value := jsonFloat(object[key])
	if value == nil {
		return 0, false
	}
	return *value, true
}
