package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"mcode/internal/providers/codex"
	"mcode/internal/providers/credits"
	"mcode/internal/providers/magpie"
	mcodeui "mcode/internal/ui"
)

func TestFilterAndMapMagpieQuotaWindows(t *testing.T) {
	resetTime := time.Date(2026, 10, 30, 15, 0, 0, 0, time.UTC)
	readAt := time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)
	lastServedAt := time.Date(2026, 10, 30, 14, 55, 0, 0, time.UTC)
	bal := 6291.0

	tests := []struct {
		name      string
		input     []magpie.QuotaWindow
		providers []string
		want      []mcodeui.ProviderQuotaWindow
	}{
		{
			name: "完整映射新扩展字段",
			input: []magpie.QuotaWindow{
				{
					ID:          "credits",
					Account:     "commandcode",
					Label:       "Credits",
					UsedPercent: 47.8,
					Display:     "$4.78 / $10.00",
					Unit:        "USD",
				},
				{
					ID:      "text-win",
					Account: "workbuddy",
					Label:   "每日签到",
					Text:    true,
					Display: "已连续签到 7 天",
				},
				{
					ID:        "unlimited-win",
					Account:   "copilot",
					Label:     "Chat",
					Unlimited: true,
				},
				{
					ID:      "balance-win",
					Account: "supermagic",
					Label:   "Wallet",
					Balance: &bal,
					Unit:    "积分",
				},
			},
			providers: []string{"commandcode", "workbuddy", "copilot", "supermagic"},
			want: []mcodeui.ProviderQuotaWindow{
				{
					ID:          "credits",
					Label:       "Credits",
					UsedPercent: 47.8,
					Account:     "commandcode",
					Source:      "magpie",
					Active:      false,
					Display:     "$4.78 / $10.00",
					Unit:        "USD",
				},
				{
					ID:      "text-win",
					Label:   "每日签到",
					Account: "workbuddy",
					Source:  "magpie",
					Active:  false,
					Text:    true,
					Display: "已连续签到 7 天",
				},
				{
					ID:        "unlimited-win",
					Label:     "Chat",
					Account:   "copilot",
					Source:    "magpie",
					Active:    false,
					Unlimited: true,
				},
				{
					ID:      "balance-win",
					Label:   "Wallet",
					Account: "supermagic",
					Source:  "magpie",
					Active:  false,
					Balance: &bal,
					Unit:    "积分",
				},
			},
		},
		{
			name:      "空输入或空供应商列表返回空",
			input:     nil,
			providers: []string{"codex"},
			want:      nil,
		},
		{
			name: "供应商列表为空切片时返回空",
			input: []magpie.QuotaWindow{
				{ID: "5h", Account: "codex", Label: "5 hours", Period: "5h", UsedPercent: 20},
			},
			providers: []string{},
			want:      nil,
		},
		{
			name: "过滤不在多供应商集合中的卡片",
			input: []magpie.QuotaWindow{
				{ID: "5h", Account: "claude", Label: "5 hours", Period: "5h", UsedPercent: 20},
				{ID: "7d", Account: "gemini · work", Label: "7 days", Period: "7d", UsedPercent: 10},
				{ID: "day", Account: "zai", Label: "daily", Period: "1d", UsedPercent: 5},
				{ID: "5h", Account: "codex", Label: "5 hours", Period: "5h", UsedPercent: 30, ResetsAt: resetTime},
			},
			providers: []string{"codex", "zai"},
			want: []mcodeui.ProviderQuotaWindow{
				{
					ID:          "day",
					Period:      "1d",
					Label:       "daily",
					UsedPercent: 5,
					Account:     "zai",
					Source:      codex.QuotaSourceMagpie,
					Active:      false,
				},
				{
					ID:          "5h",
					Period:      "5h",
					Label:       "5 hours",
					UsedPercent: 30,
					ResetsAt:    resetTime,
					Account:     "codex",
					Source:      codex.QuotaSourceMagpie,
					Active:      false,
				},
			},
		},
		{
			name: "大小写不敏感匹配多供应商集合",
			input: []magpie.QuotaWindow{
				{ID: "5h", Account: "codex", Label: "5 hours", Period: "5h", UsedPercent: 30, ResetsAt: resetTime},
				{ID: "1d", Account: "Codex · 个人号", Label: "1 day", Period: "1d", UsedPercent: 40, ResetsAt: resetTime},
				{ID: "7d", Account: "CODEX · 团队", Label: "7 days", Period: "7d", UsedPercent: 50, ResetsAt: resetTime},
				{ID: "3h", Account: "Claude · pro", Label: "3 hours", Period: "3h", UsedPercent: 15, ResetsAt: resetTime},
			},
			providers: []string{"CoDeX", "CLAUDE"},
			want: []mcodeui.ProviderQuotaWindow{
				{
					ID:          "5h",
					Period:      "5h",
					Label:       "5 hours",
					UsedPercent: 30,
					ResetsAt:    resetTime,
					Account:     "codex",
					Source:      codex.QuotaSourceMagpie,
					Active:      false,
				},
				{
					ID:          "1d",
					Period:      "1d",
					Label:       "1 day",
					UsedPercent: 40,
					ResetsAt:    resetTime,
					Account:     "Codex · 个人号",
					Source:      codex.QuotaSourceMagpie,
					Active:      false,
				},
				{
					ID:          "7d",
					Period:      "7d",
					Label:       "7 days",
					UsedPercent: 50,
					ResetsAt:    resetTime,
					Account:     "CODEX · 团队",
					Source:      codex.QuotaSourceMagpie,
					Active:      false,
				},
				{
					ID:          "3h",
					Period:      "3h",
					Label:       "3 hours",
					UsedPercent: 15,
					ResetsAt:    resetTime,
					Account:     "Claude · pro",
					Source:      codex.QuotaSourceMagpie,
					Active:      false,
				},
			},
		},
		{
			name: "映射卡片身份与最近应答标记",
			input: []magpie.QuotaWindow{
				{
					ID:           "5h",
					Account:      "claude · work",
					Label:        "5 hours",
					Period:       "5h",
					UsedPercent:  10,
					Provider:     "claude",
					Kind:         "subscription",
					User:         "{{EMAIL_mgk2r4hp}}",
					Alias:        "work",
					Plan:         "Max",
					ReadAt:       readAt,
					LastServedAt: lastServedAt,
					Last:         true,
				},
			},
			providers: []string{"claude"},
			want: []mcodeui.ProviderQuotaWindow{
				{
					ID:           "5h",
					Period:       "5h",
					Label:        "5 hours",
					UsedPercent:  10,
					Account:      "claude · work",
					Source:       codex.QuotaSourceMagpie,
					Active:       false,
					Provider:     "claude",
					Kind:         "subscription",
					User:         "{{EMAIL_mgk2r4hp}}",
					Alias:        "work",
					Plan:         "Max",
					ReadAt:       readAt,
					LastServedAt: lastServedAt,
					Last:         true,
				},
			},
		},
		{
			name: "按 Account 加 ID 去重并保留首个",
			input: []magpie.QuotaWindow{
				{ID: "5h", Account: "codex · default", Label: "5 hours", Period: "5h", UsedPercent: 10},
				{ID: "5h", Account: "codex · default", Label: "5 hours 重复项", Period: "5h", UsedPercent: 99},
				{ID: "7d", Account: "codex · default", Label: "7 days", Period: "7d", UsedPercent: 20},
				{ID: "5h", Account: "codex · other", Label: "5 hours 另一个账号", Period: "5h", UsedPercent: 30},
			},
			providers: []string{"codex"},
			want: []mcodeui.ProviderQuotaWindow{
				{
					ID:          "5h",
					Period:      "5h",
					Label:       "5 hours",
					UsedPercent: 10,
					Account:     "codex · default",
					Source:      codex.QuotaSourceMagpie,
					Active:      false,
				},
				{
					ID:          "7d",
					Period:      "7d",
					Label:       "7 days",
					UsedPercent: 20,
					Account:     "codex · default",
					Source:      codex.QuotaSourceMagpie,
					Active:      false,
				},
				{
					ID:          "5h",
					Period:      "5h",
					Label:       "5 hours 另一个账号",
					UsedPercent: 30,
					Account:     "codex · other",
					Source:      codex.QuotaSourceMagpie,
					Active:      false,
				},
			},
		},
		{
			name: "Source 必须为 magpie 且 Active 必须为 false",
			input: []magpie.QuotaWindow{
				{ID: "limit-1", Account: "codex", Label: "Limit", Period: "5h", UsedPercent: 80},
			},
			providers: []string{"codex"},
			want: []mcodeui.ProviderQuotaWindow{
				{
					ID:          "limit-1",
					Period:      "5h",
					Label:       "Limit",
					UsedPercent: 80,
					Account:     "codex",
					Source:      "magpie",
					Active:      false,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterAndMapMagpieQuotaWindows(tt.input, tt.providers)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("filterAndMapMagpieQuotaWindows() =\n%#v\n期望 =\n%#v", got, tt.want)
			}
		})
	}
}

func TestResolveEndpointQuotaProviders(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{
			name:  "nil 解析为 codex",
			input: nil,
			want:  []string{"codex"},
		},
		{
			name:  "空切片解析为空集合",
			input: []string{},
			want:  []string{},
		},
		{
			name:  "去空白、转小写、去重并保持顺序",
			input: []string{" Codex ", "CLAUDE", "codex", "  zai  ", "claude"},
			want:  []string{"codex", "claude", "zai"},
		},
		{
			name:  "全空白切片解析为空集合",
			input: []string{"  ", ""},
			want:  []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveEndpointQuotaProviders(tt.input)
			if tt.want == nil {
				if got != nil {
					t.Errorf("resolveEndpointQuotaProviders(%v) = %v, 期望 nil", tt.input, got)
				}
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("resolveEndpointQuotaProviders(%v) = %v, 期望 %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestFilterHiddenDirectProviders(t *testing.T) {
	candidates := []string{"claude", "agy", "zai", "cline", "droid"}

	tests := []struct {
		name       string
		candidates []string
		hidden     []string
		want       []string
	}{
		{
			name:       "nil hidden 不隐藏任何项",
			candidates: candidates,
			hidden:     nil,
			want:       []string{"claude", "agy", "zai", "cline", "droid"},
		},
		{
			name:       "空 hidden 不隐藏任何项",
			candidates: candidates,
			hidden:     []string{},
			want:       []string{"claude", "agy", "zai", "cline", "droid"},
		},
		{
			name:       "隐藏部分直连组（大小写与首尾空白不敏感）",
			candidates: candidates,
			hidden:     []string{" Claude ", "ZAI"},
			want:       []string{"agy", "cline", "droid"},
		},
		{
			name:       "全部隐藏时返回空列表",
			candidates: candidates,
			hidden:     []string{"claude", "agy", "zai", "cline", "droid"},
			want:       []string{},
		},
		{
			name:       "候选为空时返回 nil",
			candidates: nil,
			hidden:     []string{"claude"},
			want:       nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterHiddenDirectProviders(tt.candidates, tt.hidden)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("filterHiddenDirectProviders(%v, %v) = %v, 期望 %v", tt.candidates, tt.hidden, got, tt.want)
			}
		})
	}
}

func TestMagpieProviderDisplayName(t *testing.T) {
	tests := []struct {
		provider string
		want     string
	}{
		{"claude", "Claude"},
		{"codex", "Codex"},
		{"cline", "Cline"},
		{"zai", "Z.ai"},
		{"droid", "Droid"},
		{"gemini", "Gemini"},
		{"custom", "Custom"},
		{"my-provider", "My-provider"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			got := magpieProviderDisplayName(tt.provider)
			if got != tt.want {
				t.Errorf("magpieProviderDisplayName(%q) = %q, 期望 %q", tt.provider, got, tt.want)
			}
		})
	}
}

func TestIsQuotaSkipError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil 错误不跳过",
			err:  nil,
			want: false,
		},
		{
			name: "哨兵错误 ErrNotImplemented 跳过",
			err:  mcodeui.ErrNotImplemented,
			want: true,
		},
		{
			name: "包装的 ErrNotImplemented 跳过",
			err:  fmt.Errorf("ProviderQuota: %w", mcodeui.ErrNotImplemented),
			want: true,
		},
		{
			name: "哨兵错误 ErrUnsupported 跳过",
			err:  credits.ErrUnsupported,
			want: true,
		},
		{
			name: "包装的 ErrUnsupported 跳过",
			err:  fmt.Errorf("read credits: %w", credits.ErrUnsupported),
			want: true,
		},
		{
			name: "哨兵错误 ErrMissingKey 跳过",
			err:  credits.ErrMissingKey,
			want: true,
		},
		{
			name: "文本提示未找到登录凭据跳过",
			err:  errors.New("claude: 未找到 /Users/demo/.claude/.credentials.json 中的登录凭据"),
			want: true,
		},
		{
			name: "文本提示未安装跳过",
			err:  errors.New("droid: 未安装 (executable file not found in $PATH)"),
			want: true,
		},
		{
			name: "文本提示未登录跳过",
			err:  errors.New("claude: not logged in, please log in first"),
			want: true,
		},
		{
			name: "文本提示 401 unauthorized 跳过",
			err:  errors.New("HTTP 401: unauthorized token expired"),
			want: true,
		},
		{
			name: "文本提示不支持跳过",
			err:  errors.New("this provider does not support quota query"),
			want: true,
		},
		{
			name: "文本提示缺少凭据跳过",
			err:  errors.New("read quotas failed: missing key for endpoint"),
			want: true,
		},
		{
			name: "真实网络超时错误不跳过",
			err:  context.DeadlineExceeded,
			want: false,
		},
		{
			name: "真实连接拒绝错误不跳过",
			err:  errors.New("dial tcp 127.0.0.1:8080: connect: connection refused"),
			want: false,
		},
		{
			name: "真实网关 502 错误不跳过",
			err:  errors.New("magpie: 502 Bad Gateway"),
			want: false,
		},
		{
			name: "真实服务器 500 内部错误不跳过",
			err:  errors.New("server returned 500 internal server error"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isQuotaSkipError(tt.err)
			if got != tt.want {
				t.Errorf("isQuotaSkipError(%v) = %v，期望 %v", tt.err, got, tt.want)
			}
		})
	}
}
