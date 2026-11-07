package mcodeui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/providers/agy/quota"
)

func TestTrayQuotaSummary(t *testing.T) {
	tests := []struct {
		name   string
		groups []TrayQuotaGroup
		want   string
	}{
		{
			name:   "空数据返回空串 - nil",
			groups: nil,
			want:   "",
		},
		{
			name:   "空数据返回空串 - 空切片",
			groups: []TrayQuotaGroup{},
			want:   "",
		},
		{
			name: "空数据返回空串 - 仅有带错误的分组",
			groups: []TrayQuotaGroup{
				{
					Provider: "claude",
					Title:    "Claude",
					Err:      "network timeout",
					Windows: []ProviderQuotaWindow{
						{Label: "5h", Period: "5h", UsedPercent: 90},
					},
				},
			},
			want: "",
		},
		{
			name: "空数据返回空串 - 所有账号均已停用",
			groups: []TrayQuotaGroup{
				{
					Provider: "codex",
					Title:    "Codex",
					Windows: []ProviderQuotaWindow{
						{Account: "user1@a.com", Label: "5h", Period: "5h", UsedPercent: 90, Disabled: true},
					},
				},
			},
			want: "",
		},
		{
			name: "跳过辅助窗口",
			groups: []TrayQuotaGroup{
				{
					Provider: "claude",
					Title:    "Claude",
					Windows: []ProviderQuotaWindow{
						{ID: "ai-credits", Label: "AI Credits", Period: "5h", UsedPercent: 99},
						{ID: "extra-usage-1", Label: "Extra", Period: "5h", UsedPercent: 95},
						{ID: "tools-exec", Label: "Tools", Period: "5h", UsedPercent: 92},
						{Label: "user@example.com · 5h", Period: "5h", UsedPercent: 58},
					},
				},
			},
			want: "58%",
		},
		{
			name: "跳过 Disabled",
			groups: []TrayQuotaGroup{
				{
					Provider: "claude",
					Title:    "Claude",
					Windows: []ProviderQuotaWindow{
						{Account: "stopped@a.com", Label: "5h", Period: "5h", UsedPercent: 95, Disabled: true},
						{Account: "running@a.com", Label: "5h", Period: "5h", UsedPercent: 35, Disabled: false},
					},
				},
			},
			want: "35%",
		},
		{
			name: "Active 优先",
			groups: []TrayQuotaGroup{
				{
					Provider: "claude",
					Title:    "Claude",
					Windows: []ProviderQuotaWindow{
						{Account: "inactive@a.com", Label: "5h", Period: "5h", UsedPercent: 90, Active: false},
						{Account: "active@a.com", Label: "5h", Period: "5h", UsedPercent: 45, Active: true},
					},
				},
			},
			want: "45%",
		},
		{
			name: "多组取最大百分比并四舍五入",
			groups: []TrayQuotaGroup{
				{
					Provider: "claude",
					Title:    "Claude",
					Windows: []ProviderQuotaWindow{
						{Label: "5h", Period: "5h", UsedPercent: 42.4},
					},
				},
				{
					Provider: "codex",
					Title:    "Codex",
					Windows: []ProviderQuotaWindow{
						{Label: "5h", Period: "5h", UsedPercent: 57.6},
					},
				},
			},
			want: "58%",
		},
		{
			name: "无 Active 窗口时回退到全部主窗口",
			groups: []TrayQuotaGroup{
				{
					Provider: "claude",
					Title:    "Claude",
					Windows: []ProviderQuotaWindow{
						{Account: "a@a.com", Label: "5h", Period: "5h", UsedPercent: 30, Active: false},
						{Account: "b@a.com", Label: "5h", Period: "5h", UsedPercent: 75, Active: false},
					},
				},
			},
			want: "75%",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := trayQuotaSummary(tt.groups)
			if got != tt.want {
				t.Errorf("trayQuotaSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildTrayQuotaModel(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)

	t.Run("行模型构建 - agy 只保留 Gemini", func(t *testing.T) {
		groups := []TrayQuotaGroup{
			{
				Provider: "agy",
				Title:    "Antigravity",
				Windows: []ProviderQuotaWindow{
					{Account: "dev@gmail.com", Label: quota.GroupGemini, Period: "5h", UsedPercent: 20},
					{Account: "dev@gmail.com", Label: quota.GroupGemini + " 7d", Period: "7d", UsedPercent: 40},
					{Account: "dev@gmail.com", Label: "Claude 3.5 Sonnet", Period: "5h", UsedPercent: 85},
					{Account: "dev@gmail.com", Label: "GPT-4o", Period: "5h", UsedPercent: 90},
				},
			},
		}

		m := buildTrayQuotaModel(groups, now, now, false)
		if len(m.Groups) != 1 {
			t.Fatalf("expected 1 group, got %d", len(m.Groups))
		}
		g := m.Groups[0]
		if len(g.Accounts) != 1 {
			t.Fatalf("expected 1 account, got %d", len(g.Accounts))
		}
		acc := g.Accounts[0]
		if len(acc.Windows) != 2 {
			t.Fatalf("expected 2 Gemini windows, got %d", len(acc.Windows))
		}
		if acc.Windows[0].ShortLabel != "5h" {
			t.Errorf("window 0 label = %q, want '5h'", acc.Windows[0].ShortLabel)
		}
		if acc.Windows[1].ShortLabel != "周" {
			t.Errorf("window 1 label = %q, want '周'", acc.Windows[1].ShortLabel)
		}
	})

	t.Run("行模型构建 - 跳过辅助窗口", func(t *testing.T) {
		groups := []TrayQuotaGroup{
			{
				Provider: "claude",
				Title:    "Claude",
				Windows: []ProviderQuotaWindow{
					{Account: "a@a.com", ID: "ai-credits", Label: "5h", Period: "5h", UsedPercent: 99},
					{Account: "a@a.com", ID: "extra-usage-1", Label: "5h", Period: "5h", UsedPercent: 88},
					{Account: "a@a.com", ID: "tools-1", Label: "5h", Period: "5h", UsedPercent: 77},
					{Account: "a@a.com", Label: "MCP Tools", Period: "5h", UsedPercent: 66},
					{Account: "a@a.com", ID: "main", Label: "5h", Period: "5h", UsedPercent: 50},
				},
			},
		}

		m := buildTrayQuotaModel(groups, now, now, false)
		if len(m.Groups) != 1 || len(m.Groups[0].Accounts) != 1 {
			t.Fatalf("unexpected group/account count")
		}
		wins := m.Groups[0].Accounts[0].Windows
		if len(wins) != 1 {
			t.Fatalf("expected 1 main window, got %d", len(wins))
		}
		if wins[0].UsedPercent != 50 {
			t.Errorf("expected UsedPercent 50, got %v", wins[0].UsedPercent)
		}
	})

	t.Run("行模型构建 - Magpie 来源不显示实心点", func(t *testing.T) {
		groups := []TrayQuotaGroup{
			{
				Provider: "codex",
				Title:    "Codex",
				Windows: []ProviderQuotaWindow{
					{
						Account:     "magpie-acc@a.com",
						Label:       "5h",
						Period:      "5h",
						UsedPercent: 30,
						Active:      true,
						Source:      "magpie",
					},
					{
						Account:     "local-acc@a.com",
						Label:       "5h",
						Period:      "5h",
						UsedPercent: 60,
						Active:      true,
						Source:      "",
					},
				},
			},
		}

		m := buildTrayQuotaModel(groups, now, now, false)
		if len(m.Groups) != 1 || len(m.Groups[0].Accounts) != 2 {
			t.Fatalf("expected 1 group and 2 accounts")
		}
		acc1 := m.Groups[0].Accounts[0]
		acc2 := m.Groups[0].Accounts[1]

		if acc1.Account != "magpie-acc@a.com" || acc1.IsActive {
			t.Errorf("expected magpie account IsActive=false, got %v", acc1.IsActive)
		}
		if acc1.IsRecent {
			t.Errorf("没有 Last 标记时不应判定最近使用，got %v", acc1.IsRecent)
		}
		if acc2.Account != "local-acc@a.com" || !acc2.IsActive {
			t.Errorf("expected local account IsActive=true, got %v", acc2.IsActive)
		}
		if acc2.IsRecent {
			t.Errorf("非 magpie 账号不应判定最近使用，got %v", acc2.IsRecent)
		}
	})

	t.Run("行模型构建 - Magpie 最近应答账号标最近使用", func(t *testing.T) {
		groups := []TrayQuotaGroup{
			{
				Provider: "magpie:claude",
				Title:    "Claude",
				Windows: []ProviderQuotaWindow{
					{Account: "claude · {{EMAIL_ravn8c2m}}", Label: "5h", Period: "5h", UsedPercent: 30, Source: "magpie", Last: true},
					{Account: "claude · {{EMAIL_qw3zk7tb}}", Label: "5h", Period: "5h", UsedPercent: 40, Source: "magpie"},
					{Account: "claude · {{EMAIL_c9ysd4nv}}", Label: "5h", Period: "5h", UsedPercent: 50, Source: "", Active: true},
				},
			},
		}

		m := buildTrayQuotaModel(groups, now, now, false)
		if len(m.Groups) != 1 || len(m.Groups[0].Accounts) != 3 {
			t.Fatalf("expected 1 group and 3 accounts")
		}
		accs := m.Groups[0].Accounts

		recent := accs[0]
		if !recent.IsRecent || recent.IsActive {
			t.Errorf("Last 账号应 IsRecent=true、IsActive=false，得到 IsRecent=%v IsActive=%v", recent.IsRecent, recent.IsActive)
		}
		if !strings.HasSuffix(recent.DisplayName, " · 最近使用") {
			t.Errorf("最近使用账号 DisplayName = %q，want 以 ' · 最近使用' 结尾", recent.DisplayName)
		}

		other := accs[1]
		if other.IsRecent || other.IsActive {
			t.Errorf("未应答账号应 IsRecent=false、IsActive=false，得到 IsRecent=%v IsActive=%v", other.IsRecent, other.IsActive)
		}

		local := accs[2]
		if !local.IsActive || local.IsRecent {
			t.Errorf("非 magpie Active 账号应 IsActive=true、IsRecent=false，得到 IsActive=%v IsRecent=%v", local.IsActive, local.IsRecent)
		}
	})

	t.Run("行模型构建 - Err 分组不展开", func(t *testing.T) {
		tests := []struct {
			name        string
			err         string
			wantAuth    bool
			wantErrText string
		}{
			{
				name:        "认证错误显示重新登录",
				err:         "HTTP 401: unauthorized token expired",
				wantErrText: "需要重新登录",
			},
			{
				name:        "普通错误显示截断文本",
				err:         "dial tcp 127.0.0.1:8080: connect: connection refused",
				wantErrText: truncateRunes("dial tcp 127.0.0.1:8080: connect: connection refused", 24),
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				groups := []TrayQuotaGroup{
					{
						Provider: "claude",
						Title:    "Claude",
						Err:      tc.err,
						Windows: []ProviderQuotaWindow{
							{Account: "acc@a.com", Label: "5h", Period: "5h", UsedPercent: 50},
						},
					},
				}

				m := buildTrayQuotaModel(groups, now, now, false)
				if len(m.Groups) != 1 {
					t.Fatalf("expected 1 group")
				}
				g := m.Groups[0]
				if !g.HasErr {
					t.Errorf("expected HasErr=true")
				}
				if g.ErrText != tc.wantErrText {
					t.Errorf("expected ErrText=%q, got %q", tc.wantErrText, g.ErrText)
				}
				if len(g.Accounts) != 0 {
					t.Errorf("expected Accounts to be empty (not expanded), got %d accounts", len(g.Accounts))
				}
			})
		}
	})

	t.Run("行模型构建 - 更新时间与过期状态判定", func(t *testing.T) {
		// 刚刚更新
		m1 := buildTrayQuotaModel(nil, now.Add(-30*time.Second), now, false)
		if m1.UpdatedText != "刚刚更新" || m1.IsStale {
			t.Errorf("m1: expected '刚刚更新' and IsStale=false, got %q, %v", m1.UpdatedText, m1.IsStale)
		}

		// 5 分钟前更新
		m2 := buildTrayQuotaModel(nil, now.Add(-5*time.Minute), now, false)
		if m2.UpdatedText != "5 分钟前更新" || m2.IsStale {
			t.Errorf("m2: expected '5 分钟前更新' and IsStale=false, got %q, %v", m2.UpdatedText, m2.IsStale)
		}

		// 超过 15 分钟（usageStaleAfter）判定过期
		m3 := buildTrayQuotaModel(nil, now.Add(-20*time.Minute), now, false)
		if m3.UpdatedText != "20 分钟前更新" || !m3.IsStale {
			t.Errorf("m3: expected '20 分钟前更新' and IsStale=true, got %q, %v", m3.UpdatedText, m3.IsStale)
		}
	})

	t.Run("行模型构建 - 提前用尽提示与重置时间", func(t *testing.T) {
		resetsAt := now.Add(2 * time.Hour)
		groups := []TrayQuotaGroup{
			{
				Provider: "claude",
				Title:    "Claude",
				Windows: []ProviderQuotaWindow{
					{
						Account:     "a@a.com",
						Label:       "5h",
						Period:      "5h",
						UsedPercent: 90, // 高用量，会判定为 paceStateEarly
						ResetsAt:    resetsAt,
					},
				},
			},
		}

		m := buildTrayQuotaModel(groups, now, now, false)
		if len(m.Groups) != 1 || len(m.Groups[0].Accounts) != 1 || len(m.Groups[0].Accounts[0].Windows) != 1 {
			t.Fatalf("unexpected model structure")
		}
		win := m.Groups[0].Accounts[0].Windows[0]
		if !win.IsEarlyRunOut {
			t.Errorf("expected IsEarlyRunOut=true")
		}
		if !strings.HasPrefix(win.RightText, "预计") || !strings.HasSuffix(win.RightText, "用尽") {
			t.Errorf("expected pace run out text, got %q", win.RightText)
		}
	})

	t.Run("行模型构建 - IconID 提取", func(t *testing.T) {
		groups := []TrayQuotaGroup{
			{Provider: "magpie:codex", Title: "Codex"},
			{Provider: "claude", Title: "Claude"},
		}
		m := buildTrayQuotaModel(groups, now, now, false)
		if len(m.Groups) != 2 {
			t.Fatalf("expected 2 groups, got %d", len(m.Groups))
		}
		if m.Groups[0].IconID != "codex" {
			t.Errorf("expected IconID 'codex', got %q", m.Groups[0].IconID)
		}
		if m.Groups[1].IconID != "claude" {
			t.Errorf("expected IconID 'claude', got %q", m.Groups[1].IconID)
		}
	})

	t.Run("行模型构建 - HeadText 取最早的重置时间", func(t *testing.T) {
		t1 := now.Add(2 * time.Hour)
		t2 := now.Add(5 * time.Hour)
		tPast := now.Add(-1 * time.Hour)
		groups := []TrayQuotaGroup{
			{
				Provider: "claude",
				Title:    "Claude",
				Windows: []ProviderQuotaWindow{
					{Account: "acc", Label: "5h", Period: "5h", UsedPercent: 10, ResetsAt: t2},
					{Account: "acc", Label: "Weekly", Period: "7d", UsedPercent: 10, ResetsAt: t1},
					{Account: "acc", Label: "Old", Period: "1d", UsedPercent: 10, ResetsAt: tPast},
				},
			},
		}
		m := buildTrayQuotaModel(groups, now, now, false)
		wantHead := formatLimitReset(now, t1)
		if m.Groups[0].HeadText != wantHead {
			t.Errorf("HeadText = %q, want %q", m.Groups[0].HeadText, wantHead)
		}
		if m.Groups[0].HeadWarn {
			t.Errorf("expected HeadWarn=false")
		}
	})

	t.Run("行模型构建 - 提前用尽时 HeadWarn 与 HeadText", func(t *testing.T) {
		resetsAt := now.Add(2 * time.Hour)
		groups := []TrayQuotaGroup{
			{
				Provider: "claude",
				Title:    "Claude",
				Windows: []ProviderQuotaWindow{
					{Account: "acc", Label: "5h", Period: "5h", UsedPercent: 95, ResetsAt: resetsAt},
					{Account: "acc", Label: "Weekly", Period: "7d", UsedPercent: 10, ResetsAt: now.Add(1 * time.Hour)},
				},
			},
		}
		m := buildTrayQuotaModel(groups, now, now, false)
		g := m.Groups[0]
		if !g.HeadWarn {
			t.Errorf("expected HeadWarn=true")
		}
		expectedRight := g.Accounts[0].Windows[0].RightText
		if g.HeadText != expectedRight {
			t.Errorf("HeadText = %q, want %q", g.HeadText, expectedRight)
		}
	})

	t.Run("行模型构建 - Wide 判定", func(t *testing.T) {
		groups := []TrayQuotaGroup{
			{
				Provider: "claude",
				Title:    "Claude",
				Windows: []ProviderQuotaWindow{
					{Account: "acc", Label: "5h", Period: "5h", UsedPercent: 20},
					{Account: "acc", Label: "Weekly", Period: "7d", UsedPercent: 30, Display: "extra"},
					{Account: "acc", Label: "Tokens", Text: true, Display: "1000 tokens"},
					{Account: "acc", Label: "Free", Unlimited: true},
				},
			},
		}
		m := buildTrayQuotaModel(groups, now, now, false)
		wins := m.Groups[0].Accounts[0].Windows
		if len(wins) != 4 {
			t.Fatalf("expected 4 windows, got %d", len(wins))
		}
		if wins[0].Wide {
			t.Errorf("window 0 expected Wide=false, got true")
		}
		if !wins[1].Wide {
			t.Errorf("window 1 expected Wide=true (Display), got false")
		}
		if !wins[2].Wide {
			t.Errorf("window 2 expected Wide=true (NoBar Text), got false")
		}
		if !wins[3].Wide {
			t.Errorf("window 3 expected Wide=true (NoBar Unlimited), got false")
		}
	})

	t.Run("行模型构建 - Tooltip 格式", func(t *testing.T) {
		tReset := now.Add(3 * time.Hour)
		tEarlyReset := now.Add(6 * 24 * time.Hour)
		groups := []TrayQuotaGroup{
			{
				Provider: "claude",
				Title:    "Claude",
				Windows: []ProviderQuotaWindow{
					{Account: "acc", Label: "5h", Period: "5h", UsedPercent: 20, ResetsAt: tReset},
					{Account: "acc", Label: "周", Period: "7d", UsedPercent: 95, ResetsAt: tReset},
					{Account: "acc", Label: "周", Period: "7d", UsedPercent: 90, ResetsAt: tEarlyReset},
					{Account: "acc", Label: "Tokens", Text: true, Display: "1000 tokens"},
				},
			},
		}
		m := buildTrayQuotaModel(groups, now, now, false)
		wins := m.Groups[0].Accounts[0].Windows
		if len(wins) != 4 {
			t.Fatalf("expected 4 windows, got %d", len(wins))
		}

		want1 := "5h · " + formatLimitReset(now, tReset)
		if wins[0].Tooltip != want1 {
			t.Errorf("window 0 Tooltip = %q, want %q", wins[0].Tooltip, want1)
		}

		if wins[1].IsEarlyRunOut {
			t.Errorf("window 1 expected IsEarlyRunOut=false, got true")
		}
		want2 := "周 · " + formatLimitReset(now, tReset)
		if wins[1].Tooltip != want2 {
			t.Errorf("window 1 Tooltip = %q, want %q", wins[1].Tooltip, want2)
		}

		if !wins[2].IsEarlyRunOut {
			t.Errorf("window 2 expected IsEarlyRunOut=true, got false")
		}
		wantEarly := "周 · " + formatLimitReset(now, tEarlyReset) + " · " + wins[2].RightText
		if wins[2].Tooltip != wantEarly {
			t.Errorf("window 2 Tooltip = %q, want %q", wins[2].Tooltip, wantEarly)
		}

		want3 := "Tokens"
		if wins[3].Tooltip != want3 {
			t.Errorf("window 3 Tooltip = %q, want %q", wins[3].Tooltip, want3)
		}
	})

	t.Run("行模型构建 - MultiAccount 判定", func(t *testing.T) {
		gSingle := []TrayQuotaGroup{
			{
				Provider: "claude",
				Title:    "Claude",
				Windows: []ProviderQuotaWindow{
					{Account: "a@a.com", Label: "5h", Period: "5h", UsedPercent: 20},
				},
			},
		}
		mSingle := buildTrayQuotaModel(gSingle, now, now, false)
		if mSingle.Groups[0].MultiAccount {
			t.Errorf("单账号 expected MultiAccount=false, got true")
		}

		gDisabled := []TrayQuotaGroup{
			{
				Provider: "claude",
				Title:    "Claude",
				Windows: []ProviderQuotaWindow{
					{Account: "a@a.com", Label: "5h", Period: "5h", UsedPercent: 20, Disabled: true},
				},
			},
		}
		mDisabled := buildTrayQuotaModel(gDisabled, now, now, false)
		if mDisabled.Groups[0].MultiAccount {
			t.Errorf("单个已停用账号 expected MultiAccount=false, got true")
		}

		gMulti := []TrayQuotaGroup{
			{
				Provider: "claude",
				Title:    "Claude",
				Windows: []ProviderQuotaWindow{
					{Account: "a@a.com", Label: "5h", Period: "5h", UsedPercent: 20},
					{Account: "b@a.com", Label: "5h", Period: "5h", UsedPercent: 20, Disabled: true},
				},
			},
		}
		mMulti := buildTrayQuotaModel(gMulti, now, now, false)
		if !mMulti.Groups[0].MultiAccount {
			t.Errorf("多账号 expected MultiAccount=true, got false")
		}
	})

	t.Run("行模型构建 - 多账号倒计时按账号拆分", func(t *testing.T) {
		t1 := now.Add(2 * time.Hour)
		t1Later := now.Add(6 * time.Hour)
		t2 := now.Add(5 * time.Hour)
		groups := []TrayQuotaGroup{
			{
				Provider: "claude",
				Title:    "Claude",
				Windows: []ProviderQuotaWindow{
					{Account: "acc-a", Label: "5h", Period: "5h", UsedPercent: 10, ResetsAt: t1Later},
					{Account: "acc-a", Label: "Weekly", Period: "7d", UsedPercent: 10, ResetsAt: t1},
					{Account: "acc-b", Label: "5h", Period: "5h", UsedPercent: 10, ResetsAt: t2},
				},
			},
		}
		m := buildTrayQuotaModel(groups, now, now, false)
		g := m.Groups[0]
		if !g.MultiAccount {
			t.Fatalf("expected MultiAccount=true")
		}
		if g.HeadText != "" || g.HeadWarn {
			t.Errorf("多账号组头部不应显示倒计时：HeadText=%q, HeadWarn=%v", g.HeadText, g.HeadWarn)
		}
		if len(g.Accounts) != 2 {
			t.Fatalf("expected 2 accounts, got %d", len(g.Accounts))
		}
		if want := formatLimitReset(now, t1); g.Accounts[0].HeadText != want {
			t.Errorf("account 0 HeadText = %q, want %q", g.Accounts[0].HeadText, want)
		}
		if want := formatLimitReset(now, t2); g.Accounts[1].HeadText != want {
			t.Errorf("account 1 HeadText = %q, want %q", g.Accounts[1].HeadText, want)
		}
	})

	t.Run("行模型构建 - 单账号头部倒计时与账号一致", func(t *testing.T) {
		t1 := now.Add(2 * time.Hour)
		groups := []TrayQuotaGroup{
			{
				Provider: "claude",
				Title:    "Claude",
				Windows: []ProviderQuotaWindow{
					{Account: "acc", Label: "5h", Period: "5h", UsedPercent: 10, ResetsAt: t1},
					{Account: "acc", Label: "Weekly", Period: "7d", UsedPercent: 10, ResetsAt: now.Add(5 * time.Hour)},
				},
			},
		}
		m := buildTrayQuotaModel(groups, now, now, false)
		g := m.Groups[0]
		if g.MultiAccount {
			t.Fatalf("expected MultiAccount=false")
		}
		if g.HeadText == "" {
			t.Fatalf("单账号组头部应有倒计时")
		}
		if g.HeadText != g.Accounts[0].HeadText {
			t.Errorf("group HeadText = %q, account HeadText = %q", g.HeadText, g.Accounts[0].HeadText)
		}
		if want := formatLimitReset(now, t1); g.HeadText != want {
			t.Errorf("group HeadText = %q, want %q", g.HeadText, want)
		}
	})

	t.Run("行模型构建 - 停用账号无头部倒计时", func(t *testing.T) {
		groups := []TrayQuotaGroup{
			{
				Provider: "claude",
				Title:    "Claude",
				Windows: []ProviderQuotaWindow{
					{Account: "acc", Label: "5h", Period: "5h", UsedPercent: 10, Disabled: true, ResetsAt: now.Add(2 * time.Hour)},
				},
			},
		}
		m := buildTrayQuotaModel(groups, now, now, false)
		acc := m.Groups[0].Accounts[0]
		if acc.HeadText != "" || acc.HeadWarn {
			t.Errorf("停用账号不应有头部倒计时：HeadText=%q, HeadWarn=%v", acc.HeadText, acc.HeadWarn)
		}
	})
}

func TestTrayQuotaPanel_CommandCodeOrder(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)
	groups := []TrayQuotaGroup{
		{
			Provider: "commandcode",
			Title:    "Command Code",
			Windows: []ProviderQuotaWindow{
				{Account: "dev", Label: "Credits", Display: "$4.78 / $10.00", UsedPercent: 47.8},
				{Account: "dev", Label: "5 hours", Period: "5h", UsedPercent: 30},
				{Account: "dev", Label: "Weekly", Period: "7d", UsedPercent: 60},
			},
		},
	}

	m := buildTrayQuotaModel(groups, now, now, false)
	if len(m.Groups) != 1 || len(m.Groups[0].Accounts) != 1 {
		t.Fatalf("unexpected model structure")
	}
	wins := m.Groups[0].Accounts[0].Windows
	if len(wins) != 3 {
		t.Fatalf("expected 3 windows, got %d", len(wins))
	}
	if wins[0].ShortLabel != "5h" {
		t.Errorf("window 0 label = %q, want '5h'", wins[0].ShortLabel)
	}
	if wins[1].ShortLabel != "周" {
		t.Errorf("window 1 label = %q, want '周'", wins[1].ShortLabel)
	}
	if wins[2].ShortLabel != "额度" {
		t.Errorf("window 2 label = %q, want '额度'", wins[2].ShortLabel)
	}

	panel := NewTrayQuotaPanel(TrayQuotaPanelOptions{})
	panel.groups = groups
	tt := ui.NewTester(panel.renderView, 400, 480)
	tt.Frame()

	if !tt.HasText("Command Code") {
		t.Errorf("expected 'Command Code'")
	}
	if !tt.HasText("5h") {
		t.Errorf("expected '5h'")
	}
	if !tt.HasText("周") {
		t.Errorf("expected '周'")
	}
	if !tt.HasText("额度") {
		t.Errorf("expected '额度'")
	}
	if !tt.HasText("$4.78 / $10.00") {
		t.Errorf("expected '$4.78 / $10.00'")
	}
}

func TestTrayQuotaPanel_WorkbuddyCredits(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)
	groups := []TrayQuotaGroup{
		{
			Provider: "workbuddy",
			Title:    "WorkBuddy",
			Windows: []ProviderQuotaWindow{
				{
					Account:     "user@workbuddy.cn",
					Label:       "Credits",
					Display:     "17.6 / 2250",
					Unit:        "credits",
					UsedPercent: 0.78,
				},
			},
		},
	}

	m := buildTrayQuotaModel(groups, now, now, false)
	if len(m.Groups) != 1 || len(m.Groups[0].Accounts) != 1 {
		t.Fatalf("unexpected model structure")
	}
	wins := m.Groups[0].Accounts[0].Windows
	if len(wins) != 1 {
		t.Fatalf("expected 1 window, got %d", len(wins))
	}
	if wins[0].ShortLabel != "额度" {
		t.Errorf("window label = %q, want '额度'", wins[0].ShortLabel)
	}
	if wins[0].Display != "17.6 / 2250" {
		t.Errorf("window display = %q, want '17.6 / 2250'", wins[0].Display)
	}

	panel := NewTrayQuotaPanel(TrayQuotaPanelOptions{})
	panel.groups = groups
	tt := ui.NewTester(panel.renderView, 400, 480)
	tt.Frame()

	if !tt.HasText("WorkBuddy") {
		t.Errorf("expected 'WorkBuddy'")
	}
	if !tt.HasText("额度") {
		t.Errorf("expected '额度'")
	}
	if !tt.HasText("17.6 / 2250") {
		t.Errorf("expected '17.6 / 2250'")
	}
}

func TestTrayQuotaPanel_TextAndBalanceNoBar(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)
	bal := 6291.0
	groups := []TrayQuotaGroup{
		{
			Provider: "autoclaw-plugin",
			Title:    "AutoClaw",
			Windows: []ProviderQuotaWindow{
				{
					Account: "bot",
					Label:   "积分余额",
					Display: "1,234 积分",
					Text:    true,
				},
				{
					Account:     "bot",
					Label:       "Wallet",
					Balance:     &bal,
					Unit:        "积分",
					UsedPercent: 0,
				},
			},
		},
	}

	// 1. Text 与 Balance 窗口不参与 trayQuotaSummary
	if summary := trayQuotaSummary(groups); summary != "" {
		t.Errorf("trayQuotaSummary() = %q, want empty string", summary)
	}

	// 混入一个带进度条的 5h 窗口，仅 5h 参与百分比计算
	mixed := []TrayQuotaGroup{
		groups[0],
		{
			Provider: "claude",
			Title:    "Claude",
			Windows: []ProviderQuotaWindow{
				{Account: "acc", Label: "5h", Period: "5h", UsedPercent: 42},
			},
		},
	}
	if summary := trayQuotaSummary(mixed); summary != "42%" {
		t.Errorf("trayQuotaSummary(mixed) = %q, want '42%%'", summary)
	}

	// 2. Text 窗口与 Balance 窗口不画条
	m := buildTrayQuotaModel(groups, now, now, false)
	wins := m.Groups[0].Accounts[0].Windows
	if len(wins) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(wins))
	}
	if !wins[0].NoBar || !wins[0].Text || wins[0].Display != "1,234 积分" {
		t.Errorf("window 0 = %+v, want Text without bar", wins[0])
	}
	if !wins[1].NoBar || wins[1].BalanceText != "剩余 6291 积分" {
		t.Errorf("window 1 = %+v, want Balance without bar", wins[1])
	}

	panel := NewTrayQuotaPanel(TrayQuotaPanelOptions{})
	panel.groups = groups
	tt := ui.NewTester(panel.renderView, 400, 480)
	tt.Frame()

	if !tt.HasText("1,234 积分") {
		t.Errorf("expected '1,234 积分'")
	}
	if !tt.HasText("剩余 6291 积分") {
		t.Errorf("expected '剩余 6291 积分'")
	}

	// 进度条未绘制，也不显示百分比
	if tt.HasText("0%") {
		t.Errorf("Text 窗口不应绘制进度条")
	}
	if tt.HasText("0%") {
		t.Errorf("Balance 窗口不应绘制进度条")
	}
}

func TestTrayQuotaPanel_AccountVisibility(t *testing.T) {
	// 单账号分组：面板里不出现账号名
	singleGroup := []TrayQuotaGroup{
		{
			Provider: "claude",
			Title:    "Claude",
			Windows: []ProviderQuotaWindow{
				{Account: "single-user@test.com", Label: "5h", Period: "5h", UsedPercent: 20},
			},
		},
	}

	panel1 := NewTrayQuotaPanel(TrayQuotaPanelOptions{})
	panel1.groups = singleGroup
	tt1 := ui.NewTester(panel1.renderView, 400, 480)
	tt1.Frame()

	if tt1.HasText("single-user@test.com") {
		t.Errorf("单账号分组不应出现账号名 'single-user@test.com'")
	}
	if !tt1.HasText("Claude") {
		t.Errorf("应出现提供方标题 'Claude'")
	}
	if !tt1.HasText("5h") {
		t.Errorf("应出现窗口标签 '5h'")
	}

	// 多账号分组：面板里出现账号名
	multiGroup := []TrayQuotaGroup{
		{
			Provider: "claude",
			Title:    "Claude",
			Windows: []ProviderQuotaWindow{
				{Account: "alpha@test.com", Label: "5h", Period: "5h", UsedPercent: 20},
				{Account: "beta@test.com", Label: "5h", Period: "5h", UsedPercent: 40},
			},
		},
	}

	panel2 := NewTrayQuotaPanel(TrayQuotaPanelOptions{})
	panel2.groups = multiGroup
	tt2 := ui.NewTester(panel2.renderView, 400, 480)
	tt2.Frame()

	if !tt2.HasText("alpha@test.com") {
		t.Errorf("多账号分组应出现账号名 'alpha@test.com'")
	}
	if !tt2.HasText("beta@test.com") {
		t.Errorf("多账号分组应出现账号名 'beta@test.com'")
	}
}

func TestBuildTrayQuotaModel_AccountDisplayName(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)
	groups := []TrayQuotaGroup{
		{
			Provider: "codex",
			Title:    "Codex",
			Windows: []ProviderQuotaWindow{
				{Account: "codex · a@x.com", Label: "5h", Period: "5h", UsedPercent: 20},
				{Account: "b@y.com", Label: "5h", Period: "5h", UsedPercent: 30},
				{Account: "other · c@z.com", Label: "5h", Period: "5h", UsedPercent: 40},
				{Account: "codex · {{EMAIL_dn2fh6qk}}", Label: "5h", Period: "5h", UsedPercent: 50, Disabled: true},
			},
		},
	}

	m := buildTrayQuotaModel(groups, now, now, false)
	if len(m.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(m.Groups))
	}
	if len(m.Groups[0].Accounts) != 4 {
		t.Fatalf("expected 4 accounts, got %d", len(m.Groups[0].Accounts))
	}

	byAccount := make(map[string]trayQuotaAccountModel, 4)
	for _, acc := range m.Groups[0].Accounts {
		byAccount[acc.Account] = acc
	}

	// 与提供方同名的前缀被去掉，Account 字段保持原值
	prefixed, ok := byAccount["codex · a@x.com"]
	if !ok {
		t.Fatalf("账号 'codex · a@x.com' 未出现在模型中")
	}
	if prefixed.DisplayName != "a@x.com" {
		t.Errorf("DisplayName = %q, want 'a@x.com'", prefixed.DisplayName)
	}
	if prefixed.Account != "codex · a@x.com" {
		t.Errorf("Account = %q, want 保留原值 'codex · a@x.com'", prefixed.Account)
	}

	// 本来就无前缀的账号名保持不变
	plain, ok := byAccount["b@y.com"]
	if !ok {
		t.Fatalf("账号 'b@y.com' 未出现在模型中")
	}
	if plain.DisplayName != "b@y.com" {
		t.Errorf("DisplayName = %q, want 'b@y.com'", plain.DisplayName)
	}

	// 前缀与提供方无关时保留整串
	other, ok := byAccount["other · c@z.com"]
	if !ok {
		t.Fatalf("账号 'other · c@z.com' 未出现在模型中")
	}
	if other.DisplayName != "other · c@z.com" {
		t.Errorf("DisplayName = %q, want 'other · c@z.com'", other.DisplayName)
	}

	// 已停用账号在名字后追加『已停止』
	disabled, ok := byAccount["codex · {{EMAIL_dn2fh6qk}}"]
	if !ok {
		t.Fatalf("账号 'codex · {{EMAIL_dn2fh6qk}}' 未出现在模型中")
	}
	if !strings.HasSuffix(disabled.DisplayName, " · 已停止") {
		t.Errorf("Disabled DisplayName = %q, want 以 ' · 已停止' 结尾", disabled.DisplayName)
	}
	if disabled.DisplayName != "{{EMAIL_dn2fh6qk}} · 已停止" {
		t.Errorf("Disabled DisplayName = %q, want '{{EMAIL_dn2fh6qk}} · 已停止'", disabled.DisplayName)
	}
}

func TestTrayQuotaPanel_AccountRowLayout(t *testing.T) {
	groups := []TrayQuotaGroup{
		{
			Provider: "codex",
			Title:    "Codex",
			Windows: []ProviderQuotaWindow{
				{Account: "codex · a@x.com", Label: "5h", Period: "5h", UsedPercent: 20, Active: true},
				{Account: "codex · a@x.com", Label: "Weekly", Period: "7d", UsedPercent: 40},
				{Account: "codex · b@y.com", Label: "5h", Period: "5h", UsedPercent: 60},
			},
		},
	}

	panel := NewTrayQuotaPanel(TrayQuotaPanelOptions{})
	panel.groups = groups
	tt := ui.NewTester(panel.renderView, 400, 480)
	tt.Frame()

	// ui.Tester.Find 只能按文字标签精确查找，这里按文字位置断言布局
	mustFind := func(label string) ui.Rect {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("expected text %q, texts: %q", label, tt.Texts())
		}
		return r
	}
	right := func(r ui.Rect) float32 { return r.X + r.W }

	title := mustFind("Codex")
	name1 := mustFind("a@x.com")
	name2 := mustFind("b@y.com")
	p20 := mustFind("20%")
	p40 := mustFind("40%")
	p60 := mustFind("60%")

	// a) 同一账号相邻的两个画条窗口并排一行，落单的画条窗口独占左半格：百分比按半格成列
	if d := p40.Y - p20.Y; d > 1 || d < -1 {
		t.Errorf("账号 1 的两个额度窗口应在同一行：p20.Y=%v, p40.Y=%v, texts: %q", p20.Y, p40.Y, tt.Texts())
	}
	if !(p60.Y > p40.Y) {
		t.Errorf("账号 2 的额度行应在账号 1 之后：p40.Y=%v, p60.Y=%v, texts: %q", p40.Y, p60.Y, tt.Texts())
	}
	if right(p40) <= right(p20) {
		t.Errorf("右半格百分比应在左半格右侧：p20.right=%v, p40.right=%v, texts: %q", right(p20), right(p40), tt.Texts())
	}
	if d := right(p60) - right(p20); d > 1 || d < -1 {
		t.Errorf("落单窗口未与上一行左半格对齐：p20.right=%v, p60.right=%v, texts: %q", right(p20), right(p60), tt.Texts())
	}

	// b) 账号名在各自额度行之上，账号 2 排在账号 1 的额度行之后
	if !(name1.Y < p20.Y && name1.Y < p40.Y) {
		t.Errorf("账号 1 的名字应在自己的额度行之上：name.Y=%v, p20.Y=%v, p40.Y=%v, texts: %q", name1.Y, p20.Y, p40.Y, tt.Texts())
	}
	if !(name2.Y > p40.Y && name2.Y < p60.Y) {
		t.Errorf("账号 2 的名字应在账号 1 的额度行之后、自己的额度行之前：name2.Y=%v, p40.Y=%v, p60.Y=%v, texts: %q", name2.Y, p40.Y, p60.Y, tt.Texts())
	}

	// c) 左半格窗口标签的左缩进与供应商名对齐（右半格从行中间开始）
	label5h := mustFind("5h")
	if d := label5h.X - title.X; d > 2 || d < -2 {
		t.Errorf("窗口标签未与供应商名对齐：label.X=%v, title.X=%v, texts: %q", label5h.X, title.X, tt.Texts())
	}

	if tt.HasText("○") || tt.HasText("●") {
		t.Errorf("面板不应再出现 ○/● 文字，texts: %q", tt.Texts())
	}
	if !tt.HasText("a@x.com") {
		t.Errorf("应出现去掉前缀的账号名 'a@x.com'，texts: %q", tt.Texts())
	}
	if !tt.HasText("b@y.com") {
		t.Errorf("应出现去掉前缀的账号名 'b@y.com'，texts: %q", tt.Texts())
	}
	if tt.HasText("codex · a@x.com") {
		t.Errorf("账号名不应再带 'codex · ' 前缀，texts: %q", tt.Texts())
	}
}

func TestTrayQuotaPanel_MultiAccountCountdownOncePerAccount(t *testing.T) {
	// 账号内两个窗口重置时间相同，账号之间不同：倒计时应各只在账号名行出现一次。
	base := time.Now()
	t1 := base.Add(2 * time.Hour).Truncate(time.Minute)
	t2 := base.Add(5 * time.Hour).Truncate(time.Minute)
	groups := []TrayQuotaGroup{
		{
			Provider: "claude",
			Title:    "Claude",
			Windows: []ProviderQuotaWindow{
				{Account: "acc-a", Label: "5h", Period: "5h", UsedPercent: 10, ResetsAt: t1},
				{Account: "acc-a", Label: "Weekly", Period: "7d", UsedPercent: 10, ResetsAt: t1},
				{Account: "acc-b", Label: "5h", Period: "5h", UsedPercent: 10, ResetsAt: t2},
				{Account: "acc-b", Label: "Weekly", Period: "7d", UsedPercent: 10, ResetsAt: t2},
			},
		},
	}

	m := buildTrayQuotaModel(groups, time.Time{}, time.Now(), false)
	g := m.Groups[0]
	if !g.MultiAccount || len(g.Accounts) != 2 {
		t.Fatalf("unexpected model structure")
	}
	heads := []string{g.Accounts[0].HeadText, g.Accounts[1].HeadText}
	for _, head := range heads {
		if head == "" {
			t.Fatalf("每个账号都应有倒计时文案")
		}
	}

	panel := NewTrayQuotaPanel(TrayQuotaPanelOptions{})
	panel.groups = groups
	tt := ui.NewTester(panel.renderView, 400, 480)
	tt.Frame()

	for _, head := range heads {
		count := 0
		for _, text := range tt.Texts() {
			if text == head {
				count++
			}
		}
		if count != 1 {
			t.Errorf("倒计时 %q 应只出现一次，实际 %d 次，texts: %q", head, count, tt.Texts())
		}
	}
}

func TestTrayQuotaPanel_MagpieRecentBadge(t *testing.T) {
	groups := []TrayQuotaGroup{
		{
			Provider: "magpie:claude",
			Title:    "Claude",
			Windows: []ProviderQuotaWindow{
				{Account: "claude · {{EMAIL_t3kv8s2p}}", Label: "5h", Period: "5h", UsedPercent: 30, Source: "magpie", Last: true},
				{Account: "claude · {{EMAIL_m7hq2x9r}}", Label: "5h", Period: "5h", UsedPercent: 40, Source: "magpie"},
			},
		},
	}

	panel := NewTrayQuotaPanel(TrayQuotaPanelOptions{})
	panel.groups = groups
	tt := ui.NewTester(panel.renderView, 400, 480)
	tt.Frame()

	if !tt.HasText("{{EMAIL_t3kv8s2p}} · 最近使用") {
		t.Errorf("最近应答账号应显示『最近使用』，texts: %q", tt.Texts())
	}
	if !tt.HasText("{{EMAIL_m7hq2x9r}}") {
		t.Errorf("未应答账号应正常显示，texts: %q", tt.Texts())
	}
	if tt.HasText("{{EMAIL_m7hq2x9r}} · 最近使用") {
		t.Errorf("未应答账号不应显示『最近使用』，texts: %q", tt.Texts())
	}
}

func TestTrayQuotaPanel_WideFirstAccountNameOnTop(t *testing.T) {
	groups := []TrayQuotaGroup{
		{
			Provider: "magpie:x",
			Title:    "X",
			Windows: []ProviderQuotaWindow{
				// Text 窗口与下一个窗口同为 5h 周期，稳定排序保留它在前，从而覆盖「首个窗口是宽窗口」的分支
				{Account: "a@x.com", Label: "余额", Period: "5h", Text: true, Display: "12 积分"},
				{Account: "a@x.com", Label: "5h", Period: "5h", UsedPercent: 10},
				{Account: "b@x.com", Label: "5h", Period: "5h", UsedPercent: 30},
			},
		},
	}

	panel := NewTrayQuotaPanel(TrayQuotaPanelOptions{})
	panel.groups = groups
	tt := ui.NewTester(panel.renderView, 400, 480)
	tt.Frame()

	nameR, ok := tt.Find("a@x.com")
	if !ok {
		t.Fatalf("expected account name 'a@x.com', texts: %q", tt.Texts())
	}
	wideR, ok := tt.Find("12 积分")
	if !ok {
		t.Fatalf("expected wide window text '12 积分', texts: %q", tt.Texts())
	}

	// 多账号时账号名必须是该账号的第一行
	if !(nameR.Y < wideR.Y) {
		t.Errorf("账号名应在宽窗口之上：name.Y=%v, wide.Y=%v, texts: %q", nameR.Y, wideR.Y, tt.Texts())
	}
}

func TestTrayQuotaPanel_WideRowLayout(t *testing.T) {
	groups := []TrayQuotaGroup{
		{
			Provider: "commandcode",
			Title:    "Commandcode-plan",
			Windows: []ProviderQuotaWindow{
				{Account: "dev", Label: "5 hours", Period: "5h", UsedPercent: 0},
				{Account: "dev", Label: "Weekly", Period: "7d", UsedPercent: 89},
				// 用不与面板顶部标题重名的标签，避免 Find 取到标题文字
				{Account: "dev", Label: "Tokens", Display: "31.31 / 70.00 credits", UsedPercent: 45},
			},
		},
	}

	panel := NewTrayQuotaPanel(TrayQuotaPanelOptions{})
	panel.groups = groups
	tt := ui.NewTester(panel.renderView, 400, 480)
	tt.Frame()

	// a) Display 不再被模型截断
	if !tt.HasText("31.31 / 70.00 credits") {
		t.Errorf("expected full display '31.31 / 70.00 credits', texts: %q", tt.Texts())
	}

	mustFind := func(label string) ui.Rect {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("expected text %q, texts: %q", label, tt.Texts())
		}
		return r
	}
	centerY := func(r ui.Rect) float32 { return r.Y + r.H/2 }
	nearY := func(a, b float32) bool {
		if a > b {
			return a-b <= 2
		}
		return b-a <= 2
	}

	// 宽窗口行标签为 "Tokens"，避免与面板顶部标题 "额度" 冲突
	label := mustFind("Tokens")
	display := mustFind("31.31 / 70.00 credits")
	p45 := mustFind("45%")
	p89 := mustFind("89%")

	// b) 标签未被压到 18 宽，最新读数与标签同一行
	if label.W < 18 {
		t.Errorf("宽窗口标签被压缩：label.W=%v, texts: %q", label.W, tt.Texts())
	}
	if !nearY(centerY(label), centerY(display)) {
		t.Errorf("标签与内容文字不同行：label.Y=%v, display.Y=%v, texts: %q", label.Y, display.Y, tt.Texts())
	}

	// c) 百分比与标签同一行；宽窗口独占一行，与上一行并排的画条窗口不同行，百分比列右对齐
	if !nearY(centerY(label), centerY(p45)) {
		t.Errorf("百分比应与标签同一行：label.Y=%v, p45.Y=%v, texts: %q", label.Y, p45.Y, tt.Texts())
	}
	if nearY(centerY(p45), centerY(p89)) {
		t.Errorf("宽窗口与并排的画条窗口应分处两行：p45.Y=%v, p89.Y=%v, texts: %q", p45.Y, p89.Y, tt.Texts())
	}
	if d := p45.X + p45.W - (p89.X + p89.W); d > 1 || d < -1 {
		t.Errorf("宽窗口百分比未与固定格百分比列对齐：p45.right=%v, p89.right=%v, texts: %q", p45.X+p45.W, p89.X+p89.W, tt.Texts())
	}
}

// TestTrayQuotaPanelKeepsGroupsOnEmptyRefresh 验证刷新拿到空结果时保留上次数据与刷新时间，
// 在面板上提示当前展示的是旧数据；下次成功拿到非空数据时提示消失。
func TestTrayQuotaPanelKeepsGroupsOnEmptyRefresh(t *testing.T) {
	panel := NewTrayQuotaPanel(TrayQuotaPanelOptions{})
	firstAt := time.Now().Add(-time.Minute)

	panel.mu.Lock()
	panel.mergeTrayQuotaFetch([]TrayQuotaGroup{{Provider: "codex", Title: "Codex"}}, firstAt)
	panel.mu.Unlock()

	panel.mu.Lock()
	panel.mergeTrayQuotaFetch(nil, time.Now())
	groups := append([]TrayQuotaGroup(nil), panel.groups...)
	refreshedAt := panel.refreshedAt
	lastErr := panel.lastErr
	panel.mu.Unlock()

	if len(groups) != 1 || groups[0].Title != "Codex" {
		t.Fatalf("空刷新不应清掉旧数据，实际 %+v", groups)
	}
	if !refreshedAt.Equal(firstAt) {
		t.Fatalf("保留旧数据时刷新时间应不变，got %v want %v", refreshedAt, firstAt)
	}
	if lastErr != "刷新失败，显示的是上次数据" {
		t.Fatalf("应记下旧数据提示，got %q", lastErr)
	}

	tt := ui.NewTester(panel.renderView, 400, 480)
	tt.Frame()
	if !tt.HasText("刷新失败，显示的是上次数据") {
		t.Fatalf("面板应用次要文字提示旧数据，当前文本: %q", tt.Texts())
	}

	// 下次成功拿到非空数据时清掉提示并接受新数据。
	panel.mu.Lock()
	panel.mergeTrayQuotaFetch([]TrayQuotaGroup{{Provider: "claude", Title: "Claude"}}, time.Now())
	groups = append([]TrayQuotaGroup(nil), panel.groups...)
	lastErr = panel.lastErr
	panel.mu.Unlock()
	if lastErr != "" {
		t.Fatalf("成功刷新后应清掉旧数据提示，got %q", lastErr)
	}
	if len(groups) != 1 || groups[0].Title != "Claude" {
		t.Fatalf("成功刷新后应接受新数据，实际 %+v", groups)
	}
}

func TestBuildTrayQuotaModelElapsed(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)
	groups := []TrayQuotaGroup{
		{
			Provider: "claude",
			Title:    "Claude",
			Windows: []ProviderQuotaWindow{
				// 5 小时窗口还剩 2 小时重置：时间已过 3/5。
				{Account: "claude · a@x.com", Label: "5-hour limit", Period: "5h", UsedPercent: 20, ResetsAt: now.Add(2 * time.Hour)},
				// 没有重置时间：无法算出时间进度。
				{Account: "claude · a@x.com", Label: "Weekly", Period: "7d", UsedPercent: 40},
			},
		},
	}

	m := buildTrayQuotaModel(groups, now, now, false)
	if len(m.Groups) != 1 || len(m.Groups[0].Accounts) != 1 {
		t.Fatalf("unexpected group/account count: %+v", m.Groups)
	}
	wins := m.Groups[0].Accounts[0].Windows
	if len(wins) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(wins))
	}
	byLabel := map[string]trayQuotaWindowModel{}
	for _, w := range wins {
		byLabel[w.ShortLabel] = w
	}
	fiveHour, ok := byLabel["5h"]
	if !ok {
		t.Fatalf("应保留 5h 窗口，实际 %+v", byLabel)
	}
	if !fiveHour.hasElapsed {
		t.Errorf("有重置时间时 hasElapsed 应为 true，实际 false")
	}
	if r := fiveHour.elapsedRatio; r < 0.6-1e-6 || r > 0.6+1e-6 {
		t.Errorf("elapsedRatio = %v, want ≈0.6", r)
	}
	weekly := byLabel["周"]
	if weekly.hasElapsed {
		t.Errorf("没有重置时间时 hasElapsed 应为 false，实际 true（ratio=%v）", weekly.elapsedRatio)
	}
}

func TestTrayQuotaPanel_PaceMarker(t *testing.T) {
	mkGroups := func(resetsAt time.Time) []TrayQuotaGroup {
		return []TrayQuotaGroup{
			{
				Provider: "claude",
				Title:    "Claude",
				Windows: []ProviderQuotaWindow{
					{Account: "claude · a@x.com", Label: "5-hour limit", Period: "5h", UsedPercent: 20, ResetsAt: resetsAt},
				},
			},
		}
	}

	panel := NewTrayQuotaPanel(TrayQuotaPanelOptions{})
	panel.groups = mkGroups(time.Now().Add(2 * time.Hour))
	tt := ui.NewTester(panel.renderView, 400, 480)
	tt.Frame()
	if _, ok := tt.Find("tray-quota-pace-marker"); !ok {
		t.Errorf("有重置时间时应渲染时间进度竖线，texts: %q", tt.Texts())
	}

	// 没有重置时间：保持旧渲染，不出现竖线。
	panelNoReset := NewTrayQuotaPanel(TrayQuotaPanelOptions{})
	panelNoReset.groups = mkGroups(time.Time{})
	ttNoReset := ui.NewTester(panelNoReset.renderView, 400, 480)
	ttNoReset.Frame()
	if _, ok := ttNoReset.Find("tray-quota-pace-marker"); ok {
		t.Errorf("没有重置时间时不应出现时间进度竖线，texts: %q", ttNoReset.Texts())
	}
}

func TestTrayQuotaPanel_GroupPlanText(t *testing.T) {
	groups := []TrayQuotaGroup{
		{
			Provider: "claude",
			Title:    "Claude",
			Windows: []ProviderQuotaWindow{
				{Account: "a@x.com", Label: "5h", Period: "5h", UsedPercent: 20, Plan: "plus"},
				{Account: "a@x.com", Label: "Weekly", Period: "7d", UsedPercent: 40, Plan: "plus"},
			},
		},
	}

	panel := NewTrayQuotaPanel(TrayQuotaPanelOptions{})
	panel.groups = groups
	tt := ui.NewTester(panel.renderView, 400, 480)
	tt.Frame()

	if !tt.HasText("Claude") {
		t.Errorf("应出现供应商名 'Claude'，texts: %q", tt.Texts())
	}
	if !tt.HasText("· plus") {
		t.Errorf("单账号分组应在供应商名后显示套餐名 '· plus'，texts: %q", tt.Texts())
	}
}

func TestTrayQuotaPanel_WindowRows(t *testing.T) {
	groups := []TrayQuotaGroup{
		{
			Provider: "claude",
			Title:    "Claude",
			Windows: []ProviderQuotaWindow{
				{Account: "acc", Label: "5h", Period: "5h", UsedPercent: 20},
				{Account: "acc", Label: "Weekly", Period: "7d", UsedPercent: 40},
			},
		},
	}

	panel := NewTrayQuotaPanel(TrayQuotaPanelOptions{})
	panel.groups = groups
	tt := ui.NewTester(panel.renderView, 400, 480)
	tt.Frame()

	mustFind := func(label string) ui.Rect {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("expected text %q, texts: %q", label, tt.Texts())
		}
		return r
	}
	centerY := func(r ui.Rect) float32 { return r.Y + r.H/2 }

	title := mustFind("Claude")
	label5h := mustFind("5h")
	labelWeekly := mustFind("周")
	p20 := mustFind("20%")
	p40 := mustFind("40%")

	// 两个画条窗口并排在同一行：左半是 5h，右半是周
	if d := centerY(p20) - centerY(p40); d > 1 || d < -1 {
		t.Errorf("两个额度窗口应在同一行：p20.Y=%v, p40.Y=%v, texts: %q", p20.Y, p40.Y, tt.Texts())
	}
	if !(label5h.X < labelWeekly.X) {
		t.Errorf("5h 标签应在周标签左侧：label5h.X=%v, labelWeekly.X=%v, texts: %q", label5h.X, labelWeekly.X, tt.Texts())
	}
	if !(p40.X > p20.X) {
		t.Errorf("40%% 应在 20%% 右侧：p20.X=%v, p40.X=%v, texts: %q", p20.X, p40.X, tt.Texts())
	}

	// 左半格窗口名左边缘与供应商名对齐
	if d := label5h.X - title.X; d > 2 || d < -2 {
		t.Errorf("窗口名未与供应商名对齐：label.X=%v, title.X=%v, texts: %q", label5h.X, title.X, tt.Texts())
	}
}

// TestTrayQuotaPanel_WindowPairAlignment 验证奇数个画条窗口时最后一个独占左半格，
// 右侧留空占位，使它的百分比与上一行左半格按列对齐。
func TestTrayQuotaPanel_WindowPairAlignment(t *testing.T) {
	groups := []TrayQuotaGroup{
		{
			Provider: "claude",
			Title:    "Claude",
			Windows: []ProviderQuotaWindow{
				{Account: "acc", Label: "5h", Period: "5h", UsedPercent: 20},
				{Account: "acc", Label: "Weekly", Period: "7d", UsedPercent: 40},
				// 第三个画条窗口：30d 周期映射成「月」，稳定排序排在周之后。
				{Account: "acc", Label: "Opus", Period: "30d", UsedPercent: 60},
			},
		},
	}

	panel := NewTrayQuotaPanel(TrayQuotaPanelOptions{})
	panel.groups = groups
	tt := ui.NewTester(panel.renderView, 400, 480)
	tt.Frame()

	mustFind := func(label string) ui.Rect {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("expected text %q, texts: %q", label, tt.Texts())
		}
		return r
	}
	centerY := func(r ui.Rect) float32 { return r.Y + r.H/2 }

	p20 := mustFind("20%")
	p40 := mustFind("40%")
	p60 := mustFind("60%")

	if d := centerY(p20) - centerY(p40); d > 1 || d < -1 {
		t.Errorf("前两个画条窗口应在同一行：p20.Y=%v, p40.Y=%v, texts: %q", p20.Y, p40.Y, tt.Texts())
	}
	if !(centerY(p60) > centerY(p40)+1) {
		t.Errorf("第三个画条窗口应在下一行：p40.Y=%v, p60.Y=%v, texts: %q", p40.Y, p60.Y, tt.Texts())
	}
	if d := p60.X + p60.W - (p20.X + p20.W); d > 1 || d < -1 {
		t.Errorf("落单的半格未与上一行左半格对齐：p20.right=%v, p60.right=%v, texts: %q", p20.X+p20.W, p60.X+p60.W, tt.Texts())
	}
}

func TestBuildTrayQuotaModel_Plan(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)

	single := buildTrayQuotaModel([]TrayQuotaGroup{
		{
			Provider: "claude",
			Title:    "Claude",
			Windows: []ProviderQuotaWindow{
				{Account: "a", Label: "5h", Period: "5h", UsedPercent: 20, Plan: "plus"},
				{Account: "a", Label: "Weekly", Period: "7d", UsedPercent: 40, Plan: "plus"},
			},
		},
	}, now, now, false)
	if len(single.Groups) != 1 || len(single.Groups[0].Accounts) != 1 {
		t.Fatalf("unexpected model structure: %+v", single.Groups)
	}
	if single.Groups[0].Plan != "plus" {
		t.Errorf("单账号分组 Plan = %q, want 'plus'", single.Groups[0].Plan)
	}
	if single.Groups[0].Accounts[0].Plan != "plus" {
		t.Errorf("账号 Plan = %q, want 'plus'", single.Groups[0].Accounts[0].Plan)
	}

	multi := buildTrayQuotaModel([]TrayQuotaGroup{
		{
			Provider: "claude",
			Title:    "Claude",
			Windows: []ProviderQuotaWindow{
				{Account: "a", Label: "5h", Period: "5h", UsedPercent: 20, Plan: "plus"},
				{Account: "b", Label: "5h", Period: "5h", UsedPercent: 40, Plan: "pro"},
			},
		},
	}, now, now, false)
	if len(multi.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(multi.Groups))
	}
	if !multi.Groups[0].MultiAccount {
		t.Fatalf("expected MultiAccount=true")
	}
	if multi.Groups[0].Plan != "" {
		t.Errorf("多账号分组不应设置 Group.Plan，got %q", multi.Groups[0].Plan)
	}
	plans := make(map[string]string, len(multi.Groups[0].Accounts))
	for _, acc := range multi.Groups[0].Accounts {
		plans[acc.Account] = acc.Plan
	}
	if plans["a"] != "plus" || plans["b"] != "pro" {
		t.Errorf("多账号应各自填充 Account.Plan，got %+v", plans)
	}
}
