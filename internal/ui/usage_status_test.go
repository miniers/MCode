package mcodeui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 场景 1：错误文本能分辨未登录、不支持与普通出错；未登录给出可执行指引。
func TestUsageStatusForErrorKinds(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	windows := []ProviderQuotaWindow{{Label: "5h", UsedPercent: 10}}

	tests := []struct {
		name     string
		errText  string
		want     usageState
		contains string
	}{
		{name: "未登录中文", errText: "未登录，请先登录", want: usageStateNeedsAuth, contains: "请先登录该 Agent 账号"},
		{name: "未登录 401", errText: "HTTP 401 Unauthorized", want: usageStateNeedsAuth, contains: "登录"},
		{name: "不支持", errText: "provider does not support quota", want: usageStateUnsupported, contains: "不支持"},
		{name: "不支持中文", errText: "该后端未实现额度接口", want: usageStateUnsupported, contains: "不支持"},
		{name: "Claude 第三方 API", errText: "claude: 第三方 API 不支持额度查询", want: usageStateUnsupported, contains: "不支持"},
		{name: "普通出错", errText: "connection reset by peer", want: usageStateError, contains: "connection reset by peer"},
	}

	for _, tt := range tests {
		st := usageStatusFor(tt.errText, windows, now, now)
		if st.State != tt.want {
			t.Fatalf("%s：状态应为 %v，得到 %v", tt.name, tt.want, st.State)
		}
		if !strings.Contains(st.Message, tt.contains) {
			t.Fatalf("%s：说明文案应包含 %q，得到 %q", tt.name, tt.contains, st.Message)
		}
		if st.Stale() {
			t.Fatalf("%s：有错误时不应判为过期", tt.name)
		}
	}
}

func TestUsageStatusSettingsButton(t *testing.T) {
	resetSettingsWindowForTest()
	t.Cleanup(resetSettingsWindowForTest)
	owner := newChatState(NewFakeClient())
	t.Cleanup(owner.close)
	st := NewSettingsState(settingsTestDir(t), nil, owner)
	setChatSettings(owner, st)
	status := usageStatusFor("401 Unauthorized", nil, time.Time{}, time.Now())
	tt := ui.NewTester(func(c *ui.Context) {
		renderUsageStatusLine(c, status, TypeFootnote, usageSettingsTarget{owner, "claude"})
	}, 600, 300)
	waitFor(t, tt, func() bool { _, ok := tt.Find("打开供应商设置"); return ok })
	if err := tt.Click("打开供应商设置"); err != nil {
		t.Fatalf("点击供应商设置失败：%v", err)
	}
	st.mu.Lock()
	page, sheet, provider := st.currentPage, st.openSheet, st.providerRows.currentProvider
	st.mu.Unlock()
	if page != "models" || sheet != "provider" || provider != "claude" {
		t.Fatalf("供应商设置错误：页面 %q，面板 %q，供应商 %q", page, sheet, provider)
	}
}

// 场景 2：出错时保留后端原始错误文本。
func TestUsageStatusForKeepsDetail(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	st := usageStatusFor("boom: read timeout", nil, now, now)
	if st.Detail != "boom: read timeout" {
		t.Fatalf("应保留原始错误文本，得到 %q", st.Detail)
	}
	if !strings.Contains(st.Message, "加载额度失败") {
		t.Fatalf("出错说明应包含「加载额度失败」，得到 %q", st.Message)
	}
}

// 场景 3：过期判定覆盖边界——刚好 15 分钟不算过期，超过才是过期。
func TestUsageStatusForStale(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	windows := []ProviderQuotaWindow{{Label: "5h", UsedPercent: 30}}

	// 10 分钟前刷新：数据新鲜。
	if st := usageStatusFor("", windows, now.Add(-10*time.Minute), now); st.State != usageStateOK {
		t.Fatalf("10 分钟前的数据应为正常，得到 %v", st.State)
	}
	// 刚好 15 分钟：边界上不算过期。
	if st := usageStatusFor("", windows, now.Add(-usageStaleAfter), now); st.State != usageStateOK {
		t.Fatalf("刚好 15 分钟不应判为过期，得到 %v", st.State)
	}
	// 16 分钟前刷新：过期，且不产生说明行（只在行尾加后缀）。
	st := usageStatusFor("", windows, now.Add(-16*time.Minute), now)
	if !st.Stale() {
		t.Fatalf("16 分钟前的数据应判为过期，得到 %v", st.State)
	}
	if st.Message != "" {
		t.Fatalf("过期只加行尾后缀，说明文案应为空，得到 %q", st.Message)
	}
	if !strings.Contains(usageStaleSuffix, "数据可能已过期") {
		t.Fatalf("过期后缀文案应为「数据可能已过期」，得到 %q", usageStaleSuffix)
	}
}

// 场景 4：没有数据时不判过期，避免空面板上多出一行过期提示。
func TestUsageStatusForNoData(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	if st := usageStatusFor("", nil, now.Add(-time.Hour), now); st.State != usageStateOK {
		t.Fatalf("没有数据也不应判为过期，得到 %v", st.State)
	}
	if st := usageStatusFor("", []ProviderQuotaWindow{{Label: "5h"}}, time.Time{}, now); st.State != usageStateOK {
		t.Fatalf("刷新时间未知时不判过期，得到 %v", st.State)
	}
}

// 场景 5：错误优先于过期——出错时按错误状态展示，不再叠加过期后缀。
func TestUsageStatusForErrorBeatsStale(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	windows := []ProviderQuotaWindow{{Label: "5h", UsedPercent: 30}}

	st := usageStatusFor("401 Unauthorized", windows, now.Add(-time.Hour), now)
	if st.State != usageStateNeedsAuth {
		t.Fatalf("错误应优先于过期，得到 %v", st.State)
	}
}

// 场景 6：usageStatusForData 与 usageStatusFor 对同一份数据判定一致。
func TestUsageStatusForDataMatchesWindows(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	windows := []ProviderQuotaWindow{{Label: "5h", UsedPercent: 30}}
	refreshedAt := now.Add(-30 * time.Minute)

	if got, want := usageStatusForData("", len(windows) > 0, refreshedAt, now), usageStatusFor("", windows, refreshedAt, now); got.State != want.State {
		t.Fatalf("usageStatusForData 与 usageStatusFor 判定应一致，得到 %v / %v", got.State, want.State)
	}
	if st := usageStatusForData("", false, refreshedAt, now); st.State != usageStateOK {
		t.Fatalf("没有数据时 usageStatusForData 应为正常，得到 %v", st.State)
	}
}

// 场景 7：chatUsageStatus 在没有会话与加载中时都返回正常状态。
func TestChatUsageStatusNilAndLoading(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	if st := chatUsageStatus(nil, "claude", now); st.State != usageStateOK {
		t.Fatalf("没有会话时应返回正常状态，得到 %v", st.State)
	}

	s := &chatState{
		quotaLoadingProviders: map[string]bool{"claude": true},
		quotaErrs:             map[string]string{"claude": "401 Unauthorized"},
		quotaLoadedProviders:  map[string]time.Time{"claude": now.Add(-time.Hour)},
	}
	if st := chatUsageStatus(s, "claude", now); st.State != usageStateOK {
		t.Fatalf("加载中不判状态，得到 %v", st.State)
	}
}

// 场景 8：额度条短标签与颜色按状态区分。
func TestUsageStatusStripLabel(t *testing.T) {
	if got := usageStatusStripLabel(usageStatus{State: usageStateNeedsAuth}); got != "需登录" {
		t.Fatalf("未登录短标签应为「需登录」，得到 %q", got)
	}
	if got := usageStatusStripLabel(usageStatus{State: usageStateUnsupported}); got != "不支持额度" {
		t.Fatalf("不支持短标签应为「不支持额度」，得到 %q", got)
	}
	if got := usageStatusStripLabel(usageStatus{State: usageStateError}); got != "额度不可用" {
		t.Fatalf("出错短标签应为「额度不可用」，得到 %q", got)
	}
	if got := usageStatusStripLabel(usageStatus{State: usageStateOK}); got != "" {
		t.Fatalf("正常状态不应有短标签，得到 %q", got)
	}
}

// Magpie 卡片带来源端读取时间：数据本身超过阈值没更新时算过期，未知读取时间不判过期。
func TestUsageStatusForMagpieReadAt(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	old := now.Add(-time.Hour)
	fresh := now.Add(-2 * time.Minute)

	stale := []ProviderQuotaWindow{{Label: "5 hours", UsedPercent: 10, Source: "magpie", ReadAt: old}}
	// 本地刚重读过缓存（refreshedAt=now），但卡片自己的读取时间很旧：仍然说过期。
	if st := usageStatusFor("", stale, now, now); st.State != usageStateStale {
		t.Fatalf("读取时间过期的卡片应判过期，得到 %v", st.State)
	}

	current := []ProviderQuotaWindow{{Label: "5 hours", UsedPercent: 10, Source: "magpie", ReadAt: fresh}}
	if st := usageStatusFor("", current, now, now); st.State == usageStateStale {
		t.Fatalf("卡片数据新鲜时不应因为本地读取时间而判过期")
	}

	unknown := []ProviderQuotaWindow{
		{Label: "5 hours", UsedPercent: 10, Source: "magpie", ReadAt: fresh},
		{Label: "30 days", UsedPercent: 0, Source: "magpie"},
	}
	if st := usageStatusFor("", unknown, now, now); st.State == usageStateStale {
		t.Fatalf("有未知读取时间时不应猜过期，得到 %v", st.State)
	}
	if st := usageStatusFor("", nil, now.Add(-time.Hour), now); st.State != usageStateOK {
		t.Fatalf("无数据时保持原判定，得到 %v", st.State)
	}
}

// 来源判断只看窗口字段，不看 provider 名。
func TestQuotaWindowsFromMagpie(t *testing.T) {
	if quotaWindowsFromMagpie(nil) {
		t.Error("空窗口不应算 Magpie")
	}
	if quotaWindowsFromMagpie([]ProviderQuotaWindow{{Label: "5 hours", Source: "openai"}}) {
		t.Error("直连窗口不应算 Magpie")
	}
	if !quotaWindowsFromMagpie([]ProviderQuotaWindow{{Label: "5 hours"}, {Label: "7 days", Source: "magpie"}}) {
		t.Error("任一窗口来自 Magpie 即算 Magpie")
	}
}
