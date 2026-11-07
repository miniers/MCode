package omp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/providers/credits"
)

// planTestNow 是方案额度测试里的固定“现在”（2026-10-18T12:00:00Z）。
func planTestNow() time.Time { return time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC) }

// loadOmpTestdata 读取 testdata 下的 fixture。
func loadOmpTestdata(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("testdata", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 fixture %s 失败: %v", path, err)
	}
	return data
}

func TestPlanLimitsReadsOmpUsage(t *testing.T) {
	fixture := loadOmpTestdata(t, "usage_reports.json")
	home := t.TempDir()

	var (
		gotExecutable string
		gotArgs       []string
		gotDir        string
		gotEnv        []string
	)
	opts := PlanLimitsOptions{
		Executable: "/opt/omp/bin/omp",
		Env:        map[string]string{"HOME": home, "PATH": "/usr/bin"},
		HomeDir:    home,
		Now:        planTestNow,
		Run: func(_ context.Context, executable string, args []string, dir string, env []string) ([]byte, error) {
			gotExecutable, gotArgs, gotDir, gotEnv = executable, args, dir, env
			return fixture, nil
		},
	}
	windows, err := PlanLimits(context.Background(), opts)
	if err != nil {
		t.Fatalf("PlanLimits 失败: %v", err)
	}

	if gotExecutable != "/opt/omp/bin/omp" {
		t.Errorf("可执行文件 = %q", gotExecutable)
	}
	if strings.Join(gotArgs, " ") != "usage --json --no-extensions" {
		t.Errorf("参数 = %v", gotArgs)
	}
	if gotDir != home {
		t.Errorf("工作目录 = %q，期望 %q", gotDir, home)
	}
	if strings.Join(gotEnv, " ") != "HOME="+home+" PATH=/usr/bin" {
		t.Errorf("环境变量 = %v", gotEnv)
	}

	want := []credits.Window{
		{Label: "Claude · 5-hour limit", UsedPercent: 12.5, ResetsAt: time.UnixMilli(1792348800000)},
		{Label: "Claude · Weekly · claude-opus-4-6", UsedPercent: 25},
		{Label: "Claude · Extra usage", UsedPercent: 100},
		{Label: "Codex · 5-hour limit", UsedPercent: 25, ResetsAt: time.UnixMilli(1792411200000)},
	}
	if len(windows) != len(want) {
		t.Fatalf("窗口数量应为 %d，得到 %d: %+v", len(want), len(windows), windows)
	}
	for i, expected := range want {
		window := windows[i]
		if window.Label != expected.Label || window.UsedPercent != expected.UsedPercent {
			t.Errorf("第 %d 个窗口 = %+v，期望 %s / %v", i, window, expected.Label, expected.UsedPercent)
		}
		if !window.ResetsAt.Equal(expected.ResetsAt) {
			t.Errorf("第 %d 个窗口的重置时间 = %v，期望 %v", i, window.ResetsAt, expected.ResetsAt)
		}
	}
}

func TestPlanLimitsWithoutOmp(t *testing.T) {
	opts := PlanLimitsOptions{
		Run: func(context.Context, string, []string, string, []string) ([]byte, error) {
			return nil, errors.New("omp not found")
		},
	}
	windows, err := PlanLimits(context.Background(), opts)
	if err != nil {
		t.Fatalf("omp 缺失不应算错误: %v", err)
	}
	if windows != nil {
		t.Fatalf("omp 缺失时应返回 nil 窗口: %+v", windows)
	}
}

func TestPlanLimitsWithoutUsableWindows(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{name: "JSON 之前只有日志", payload: "omp: refreshing usage cache\n"},
		{name: "没有报告", payload: `{"reports":[]}`},
		{name: "报告没有窗口", payload: `{"reports":[{"provider":"anthropic","limits":[]}]}`},
		{name: "窗口缺少已用比例", payload: `{"reports":[{"provider":"anthropic","limits":[{"id":"a","window":{"durationMs":18000000},"amount":{"unit":"usd"}}]}]}`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			opts := PlanLimitsOptions{
				Now: planTestNow,
				Run: func(context.Context, string, []string, string, []string) ([]byte, error) {
					return []byte(test.payload), nil
				},
			}
			windows, err := PlanLimits(context.Background(), opts)
			if err != nil {
				t.Fatalf("PlanLimits 失败: %v", err)
			}
			if windows != nil {
				t.Fatalf("没有可用窗口时应返回 nil: %+v", windows)
			}
		})
	}
}

func TestPlanLimitsSkipsStaleReports(t *testing.T) {
	// fetchedAt 距“现在”20 小时，超过 6 小时的 last-good 上限。
	payload := `{"reports":[{"provider":"anthropic","fetchedAt":1792250000000,"limits":[{"id":"a","window":{"durationMs":18000000},"amount":{"usedFraction":0.5}}]}]}`
	run := func(context.Context, string, []string, string, []string) ([]byte, error) {
		return []byte(payload), nil
	}

	windows, err := PlanLimits(context.Background(), PlanLimitsOptions{Now: planTestNow, Run: run})
	if err != nil {
		t.Fatalf("PlanLimits 失败: %v", err)
	}
	if windows != nil {
		t.Fatalf("过旧的报告应被丢弃: %+v", windows)
	}

	// 上限放宽后同一个报告可用
	windows, err = PlanLimits(context.Background(), PlanLimitsOptions{Now: planTestNow, Run: run, MaxReportAge: 24 * time.Hour})
	if err != nil {
		t.Fatalf("PlanLimits 失败: %v", err)
	}
	if len(windows) != 1 || windows[0].Label != "Claude · 5-hour limit" || windows[0].UsedPercent != 50 {
		t.Fatalf("放宽上限后应保留窗口: %+v", windows)
	}
}

func TestPlanLimitsPercentUnitAndProviderPrefix(t *testing.T) {
	payload := `{"reports":[{"provider":"commandcode","limits":[{"id":"a","window":{"durationMs":18000000},"amount":{"unit":"percent","used":33}}]}]}`
	opts := PlanLimitsOptions{
		Now: planTestNow,
		Run: func(context.Context, string, []string, string, []string) ([]byte, error) {
			return []byte(payload), nil
		},
	}
	windows, err := PlanLimits(context.Background(), opts)
	if err != nil {
		t.Fatalf("PlanLimits 失败: %v", err)
	}
	if len(windows) != 1 || windows[0].Label != "Command Code · 5-hour limit" || windows[0].UsedPercent != 33 {
		t.Fatalf("percent 单位窗口错误: %+v", windows)
	}
}

func TestPlanLimitsDuplicateAccounts(t *testing.T) {
	payload := `{"reports":[
		{"provider":"anthropic","metadata":{"email":"miniers@example.com"},"limits":[{"id":"a","window":{"durationMs":18000000},"amount":{"usedFraction":0.25}}]},
		{"provider":"anthropic","limits":[{"id":"a","window":{"durationMs":18000000},"amount":{"usedFraction":0.5}}]}
	]}`
	opts := PlanLimitsOptions{
		Now: planTestNow,
		Run: func(context.Context, string, []string, string, []string) ([]byte, error) {
			return []byte(payload), nil
		},
	}
	windows, err := PlanLimits(context.Background(), opts)
	if err != nil {
		t.Fatalf("PlanLimits 失败: %v", err)
	}
	if len(windows) != 2 {
		t.Fatalf("窗口数量应为 2，得到 %d: %+v", len(windows), windows)
	}
	if windows[0].Label != "Claude · 5-hour limit (miniers)" || windows[0].UsedPercent != 25 {
		t.Errorf("第一个账号的窗口错误: %+v", windows[0])
	}
	if windows[1].Label != "Claude · 5-hour limit (account 2)" || windows[1].UsedPercent != 50 {
		t.Errorf("第二个账号的窗口错误: %+v", windows[1])
	}
}

func TestOmpWindowTitle(t *testing.T) {
	cases := []struct {
		minutes int
		want    string
	}{
		{300, "5-hour limit"},
		{1440, "Daily"},
		{10080, "Weekly"},
		{43200, "Monthly"},
		{44640, "Monthly"},
		{2880, "2-day limit"},
		{90, "1-hour limit"},
		{120, "2-hour limit"},
	}
	for _, test := range cases {
		if got := ompWindowTitle(test.minutes); got != test.want {
			t.Errorf("ompWindowTitle(%d) = %q，期望 %q", test.minutes, got, test.want)
		}
	}
}

func TestOmpProviderPrefix(t *testing.T) {
	cases := []struct {
		provider string
		want     string
	}{
		{"anthropic", "Claude"},
		{"openai-codex", "Codex"},
		{"zai", "Z.ai"},
		{"commandcode", "Command Code"},
		{"cursor", "Cursor"},
		{"", ""},
	}
	for _, test := range cases {
		if got := ompProviderPrefix(test.provider); got != test.want {
			t.Errorf("ompProviderPrefix(%q) = %q，期望 %q", test.provider, got, test.want)
		}
	}
}
