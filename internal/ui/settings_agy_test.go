package mcodeui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/providers/agy/accounts"
	"mcode/internal/providers/agy/quota"
	"mcode/internal/testenv"
)

type blockingAgySubmitClient struct {
	*FakeClient
	started chan struct{}
	done    chan struct{}
}

func (c *blockingAgySubmitClient) AgyLoginSubmit(string) error {
	c.started <- struct{}{}
	<-c.done
	return nil
}

func TestSettingsAgySubmitBusy(t *testing.T) {
	client := &blockingAgySubmitClient{FakeClient: NewFakeClient(), started: make(chan struct{}, 2), done: make(chan struct{})}
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(client))
	st.agy.loginBrowserURL = "https://example.com"
	tt := ui.NewTester(func(c *ui.Context) { renderAgyBrowserLoginBody(c, st) }, 1000, 1000)
	if err := tt.Click("提交"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("提交未启动")
	}
	tt.Frame()
	if !tt.HasText("提交中…") {
		t.Fatal("缺少提交中状态")
	}
	_ = tt.Click("提交")
	select {
	case <-client.started:
		t.Fatal("重复提交")
	default:
	}
	close(client.done)
	waitFor(t, tt, func() bool { st.mu.Lock(); defer st.mu.Unlock(); return !st.agy.loginSubmitBusy })
}

func openAgySettingsTest(t *testing.T, fake *FakeClient) (*ui.Tester, *SettingsState) {
	t.Helper()
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(fake))
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("agy-test-sheet")).Children(func() { renderAgyProviderSheetSections(c, st) })
	}, 1000, 4000)
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.agy.loaded && !st.agy.loading && !st.agy.refreshBusy
	})
	return tt, st
}

// openAgyDispatchForTest 展开 agy 行里的「账号调度」子区（长页面里按钮常在窗口外，直接置状态）。
func openAgyDispatchForTest(tt *ui.Tester, st *SettingsState) {
	st.mu.Lock()
	st.agy.dispatchOpen = true
	st.mu.Unlock()
	tt.Frame()
}

// clickSettingsAdvancedLabel 把设置详情区滚到底，再点击页底高级区里的文案。
// 高级区默认收起且位于页底，展开后常在首屏之外，直接按坐标点击会落到窗口外。
func clickSettingsAdvancedLabel(t *testing.T, tt *ui.Tester, label string, width, height float32) {
	t.Helper()
	if _, ok := tt.Find("设置详情列"); !ok {
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := settingsClick(t, tt, label); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsAgy_ManualSubmissionWaitsForExchange(t *testing.T) {
	fake := NewFakeClient()
	fake.agyLoginWaitDone = make(chan struct{})
	tt, st := openAgySettingsTest(t, fake)
	if err := tt.Click("浏览器登录"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.agy.loginBrowserURL != ""
	})
	// 登录链接改成文本加复制按钮（不再是只读输入框），手动粘贴框同时在等待期间可见。
	for _, text := range []string{"复制链接", "手动粘贴回调 URL 或授权码"} {
		if !tt.HasText(text) {
			t.Fatalf("登录等待中缺少 %q: %v", text, tt.Texts())
		}
	}
	st.mu.Lock()
	st.agy.loginInputCode = "mock-code"
	st.mu.Unlock()
	tt.Frame()
	if err := tt.Click("提交"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.agyLoginSubmitCalls) == 1
	})
	st.mu.Lock()
	busy, code := st.agy.loginBrowserBusy, st.agy.loginInputCode
	st.mu.Unlock()
	if !busy || code != "mock-code" {
		t.Fatal("submission marked login complete before token exchange finished")
	}
	fake.mu.Lock()
	fake.agyLoginWaitErr = fmt.Errorf("token exchange failed")
	close(fake.agyLoginWaitDone)
	fake.mu.Unlock()
	// 失败原因写在行内容里，不再当副标题；面板仍要把它显示出来。
	waitFor(t, tt, func() bool { return tt.HasText("token exchange failed") })
}

func TestSettingsAgy_RefreshAndRoutingErrorsVisible(t *testing.T) {
	fake := NewFakeClient()
	fake.agyAccounts = []accounts.Account{{ID: "account-1", Status: accounts.StatusActive, On: true}}
	fake.agyRefreshQuotaErr = fmt.Errorf("quota unavailable")
	fake.agyRoutingErr = fmt.Errorf("routing unreadable")
	tt, _ := openAgySettingsTest(t, fake)
	// 首次加载前没有账号快照，手动刷新已加载的账号以触发额度错误。
	if err := tt.Click("刷新额度"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("刷新额度失败: quota unavailable") })
	waitFor(t, tt, func() bool { return tt.HasText("保存路由配置失败: routing unreadable") })
	// 路由错误挂在开关行下方：行标题与开关都还在，说明错误不是靠副标题撑住的。
	for _, text := range []string{"调度", "冷却账号", "切换冷却账号"} {
		if !tt.HasText(text) {
			t.Fatalf("路由区缺少 %q: %v", text, tt.Texts())
		}
	}
}

func TestSettingsAgy_OptimisticQuotaAndCooldown(t *testing.T) {
	fake := NewFakeClient()
	fake.agyAccounts = []accounts.Account{{
		ID: accounts.SystemAccountID, Status: accounts.StatusActive,
		QuotaReadAt: time.Now().Add(-time.Hour).Format(time.RFC3339),
		Quota: map[string]accounts.QuotaGroup{
			quota.GroupGemini: {UsedPercent: 100, ResetAt: time.Now().Add(-time.Minute).Format(time.RFC3339)},
		},
		CoolingUntil: time.Now().Add(time.Minute),
	}}
	tt, _ := openAgySettingsTest(t, fake)
	waitFor(t, tt, func() bool {
		return tt.HasText("已用 0%") && tt.HasText("路由冷却中")
	})
}

func TestAgyTerminalCommandQuotesPaths(t *testing.T) {
	cmd := agyTerminalCommand("/tmp/agy bin", []string{"HOME=/tmp/user's home", "GEMINI_FORCE_FILE_STORAGE=true"}, []string{"--gemini_dir=/tmp/user's home/.gemini"})
	if !strings.Contains(cmd, "'HOME=/tmp/user'\"'\"'s home'") || !strings.Contains(cmd, "'--gemini_dir=/tmp/user'\"'\"'s home/.gemini'") {
		t.Fatalf("paths not safely quoted: %s", cmd)
	}
	if !strings.Contains(cmd, "'/tmp/agy bin'") {
		t.Fatal("configured binary was not quoted")
	}
}

// (a) 账号列表渲染出两个账号的邮箱、“系统”“默认”徽章和“已用 N%”。
func TestSettingsAgy_AccountListRender(t *testing.T) {
	fake := NewFakeClient()
	fake.agyAccounts = []accounts.Account{
		{
			ID:     accounts.SystemAccountID,
			Email:  "system@example.com",
			Label:  "系统默认账号",
			Status: accounts.StatusActive,
			Quota: map[string]accounts.QuotaGroup{
				quota.GroupGemini: {
					UsedPercent: 25.0,
					ResetAt:     time.Now().Add(2 * time.Hour).Format(time.RFC3339),
				},
			},
			QuotaReadAt: time.Now().Format(time.RFC3339),
		},
		{
			ID:     "acc-personal",
			Email:  "developer@example.com",
			Label:  "个人账号",
			Status: accounts.StatusActive,
			Tier:   "Pro",
			Quota: map[string]accounts.QuotaGroup{
				quota.GroupGemini: {
					UsedPercent: 60.0,
					ResetAt:     time.Now().Add(5 * time.Hour).Format(time.RFC3339),
				},
				quota.GroupGemini + " 7d": {
					UsedPercent: 40.0,
					ResetAt:     time.Now().Add(48 * time.Hour).Format(time.RFC3339),
				},
				quota.GroupClaudeGPT: {
					UsedPercent: 80.0,
					ResetAt:     time.Now().Add(5 * time.Hour).Format(time.RFC3339),
				},
			},
			QuotaReadAt: time.Now().Format(time.RFC3339),
		},
	}
	fake.agyDefaultAccountID = accounts.SystemAccountID

	tt, _ := openAgySettingsTest(t, fake)

	waitFor(t, tt, func() bool {
		return tt.HasText("system@example.com") &&
			tt.HasText("developer@example.com") &&
			tt.HasText("系统") &&
			tt.HasText("默认") &&
			tt.HasText("Gemini:") &&
			tt.HasText("Gemini 周额度:") &&
			tt.HasText("已用 25%") &&
			tt.HasText("已用 60%") &&
			tt.HasText("已用 40%")
	})
	if tt.HasText("Claude/GPT") || tt.HasText("已用 80%") {
		t.Fatalf("settings agy quota must not show Claude/GPT, texts: %q", tt.Texts())
	}
}

// (b) 系统账号没有可用删除按钮，点非系统账号删除→确认后假 Client 记录到 AgyRemove 调用。
func TestSettingsAgy_DeleteSystemAndNonSystemAccount(t *testing.T) {
	fake := NewFakeClient()
	fake.agyAccounts = []accounts.Account{
		{
			ID:     accounts.SystemAccountID,
			Email:  "sys@example.com",
			Status: accounts.StatusActive,
		},
		{
			ID:     "acc-to-delete",
			Email:  "del@example.com",
			Status: accounts.StatusActive,
		},
	}
	fake.agyDefaultAccountID = accounts.SystemAccountID
	fake.agyBoundConversations = map[string][]string{
		"acc-to-delete": {"conv-1", "conv-2"},
	}

	tt, _ := openAgySettingsTest(t, fake)

	waitFor(t, tt, func() bool {
		return tt.HasText("sys@example.com") && tt.HasText("del@example.com")
	})

	// 确认系统账号提示“系统账号不可删除”且无可用删除按钮
	if !tt.HasText("系统账号不可删除") {
		t.Fatalf("expected system account to display '系统账号不可删除'")
	}

	// 点非系统账号的“删除”按钮进入确认态
	if err := tt.Click("删除"); err != nil {
		t.Fatalf("click '删除' failed: %v", err)
	}

	// 验证确认态提示及绑定会话提示
	waitFor(t, tt, func() bool {
		return tt.HasText("确认删除") && tt.HasText("2 个会话绑定")
	})

	// 点击“确认删除”
	if err := tt.Click("确认删除"); err != nil {
		t.Fatalf("click '确认删除' failed: %v", err)
	}

	// 验证假 Client 记录到 AgyRemove 调用
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		for _, id := range fake.agyRemovedIDs {
			if id == "acc-to-delete" {
				return true
			}
		}
		return false
	})
}

// (c) AgyAccounts 返回错误时显示错误文本和“重试”。
func TestSettingsAgy_AccountsErrorAndRetry(t *testing.T) {
	fake := NewFakeClient()
	fake.agyAccountsErr = fmt.Errorf("网络连接超时无法拉取账号")

	tt, _ := openAgySettingsTest(t, fake)

	// 验证显示错误文本和“重试”按钮
	waitFor(t, tt, func() bool {
		return tt.HasText("网络连接超时无法拉取账号") && tt.HasText("重试")
	})

	// 修复错误并点击“重试”
	fake.mu.Lock()
	fake.agyAccountsErr = nil
	fake.agyAccounts = []accounts.Account{
		{
			ID:    accounts.SystemAccountID,
			Email: "recovered@example.com",
		},
	}
	fake.mu.Unlock()

	if err := tt.Click("重试"); err != nil {
		t.Fatalf("click '重试' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("recovered@example.com")
	})
}

// (d) 空列表显示“还没有 agy 账号”。
func TestSettingsAgy_EmptyAccounts(t *testing.T) {
	fake := NewFakeClient()
	fake.agyAccounts = []accounts.Account{}

	tt, _ := openAgySettingsTest(t, fake)

	waitFor(t, tt, func() bool {
		return tt.HasText("还没有 agy 账号")
	})
}

// (e) 页面打开时先用 provider-status-cache.json 里的账号快照渲染：
// 账号管理器未接线（AgyAccounts 报错）时也不应显示「未登录」。
func TestSettingsAgy_SwarmtesAccountsFromCache(t *testing.T) {
	testenv.TempHome(t)
	dataDir := settingsTestDir(t)

	updateProviderStatusCache(dataDir, func(cache *providerStatusCache) {
		cache.AgyAccounts = []accounts.Account{
			{ID: "cached-1", Email: "cached@example.com", Status: accounts.StatusActive},
		}
		cache.AgyDefaultID = "cached-1"
	})

	fake := NewFakeClient()
	fake.agyAccountsErr = errors.New("agy accounts are not configured")

	st := NewSettingsState(dataDir, nil, newChatState(fake))
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("agy-cache-test-sheet")).Children(func() { renderAgyProviderSheetSections(c, st) })
	}, 1000, 4000)

	// 刷新失败不会清空缓存里的账号快照
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return len(st.agy.accounts) == 1 && st.agy.accounts[0].ID == "cached-1" && st.agy.err != ""
	})

	// 刷新失败时面板仍展示缓存账号及错误。
	waitFor(t, tt, func() bool { return tt.HasText("cached@example.com") && tt.HasText("agy accounts are not configured") })
}

// (g) 关掉的账号（On=false）徽章显示「已停用」：启用开关只写 On，状态仍是 active。
func TestSettingsAgy_DisabledAccountBadge(t *testing.T) {
	fake := NewFakeClient()
	fake.agyAccounts = []accounts.Account{{
		ID:     "acc-off",
		Email:  "off@example.com",
		Status: accounts.StatusActive,
		On:     false,
	}}
	tt, _ := openAgySettingsTest(t, fake)
	waitFor(t, tt, func() bool {
		return tt.HasText("off@example.com") && tt.HasText("已停用")
	})
}

// (h2) 只有系统账号且本机没有凭证时：状态徽章显示「未登录」、卡片提示缺少凭证，刷新额度跳过系统账号。
func TestSettingsAgy_SystemAccountWithoutLocalLogin(t *testing.T) {
	fake := NewFakeClient()
	// agy 已安装，只是本机没有登录凭证：供应商列表要明确报告可用，
	// 否则行摘要会按「未安装」渲染。
	fake.providers = []ProviderInfo{{ID: "agy", Name: "Antigravity", Available: true}}
	fake.agyAccounts = []accounts.Account{{
		ID:     accounts.SystemAccountID,
		Label:  "系统账号",
		Status: accounts.StatusActive,
		On:     true,
	}}
	tt, st := openAgySettingsTest(t, fake)

	// 本机凭证由宿主环境决定，测试里固定为「未登录」。
	st.mu.Lock()
	st.agy.localLogin = false
	st.mu.Unlock()
	st.invalidate()

	// 本机登录凭证与「刷新额度」都在「账号与额度」页签。

	waitFor(t, tt, func() bool {
		return tt.HasText("本机未检测到 agy 登录凭证") && tt.HasText("未登录")
	})

	fake.mu.Lock()
	before := len(fake.agyRefreshCalls)
	fake.mu.Unlock()

	// 按钮的 Label 是可见文案「刷新额度」，Key（agy-top-refresh-quota-btn）不参与查找，
	// 所以按文案点击。
	if err := tt.Click("刷新额度"); err != nil {
		t.Fatalf("click '刷新额度' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.agy.refreshBusy
	})

	fake.mu.Lock()
	after := append([]string(nil), fake.agyRefreshCalls[before:]...)
	fake.mu.Unlock()
	if len(after) != 0 {
		t.Fatalf("本机未登录时刷新额度应跳过系统账号，got calls %v", after)
	}
}

// (h) 采纳终端登录后额度刷新失败要写行级提示，不能只显示「尚未拉取额度」。
func TestSettingsAgy_AdoptQuotaFailureVisible(t *testing.T) {
	fake := NewFakeClient()
	acc := accounts.Account{ID: "acc-term", Email: "term@example.com", Status: accounts.StatusActive, On: true}
	fake.agyAdoptAccount = acc
	fake.agyRefreshQuotaErr = fmt.Errorf("quota endpoint down")
	tt, st := openAgySettingsTest(t, fake)
	fake.mu.Lock()
	fake.agyAccounts = []accounts.Account{acc}
	fake.mu.Unlock()

	// 面板直接显示终端登录，设置标识以模拟用户完成终端授权。
	// 直接给出终端登录标识，跳过「终端登录（备用）」按钮（该文案同时是行标题，点不准）。
	st.mu.Lock()
	st.agy.terminalLoginID = "term-mock-id"
	st.mu.Unlock()
	tt.Frame()

	if !tt.HasText("复制命令") {
		t.Fatalf("终端登录命令旁应有复制按钮: %v", tt.Texts())
	}
	clickSettingsAdvancedLabel(t, tt, "我已完成登录", 1000, 4000)
	waitFor(t, tt, func() bool {
		return tt.HasText("账号已添加，额度拉取失败") && tt.HasText("term@example.com")
	})
	accountSection, ok := tt.Find("账号")
	if !ok {
		t.Fatalf("供应商面板缺少账号节: %v", tt.Texts())
	}
	account, ok := tt.Find("term@example.com")
	if !ok || account.Y <= accountSection.Y {
		t.Fatalf("采纳的账号未显示在账号节: %v", tt.Texts())
	}
}

// 验证顶部“批量预热”与单账号“预热”按钮调用及失败错误提示
func TestSettingsAgy_WarmupButtonsAndErrorVisible(t *testing.T) {
	fake := NewFakeClient()
	acc := accounts.Account{
		ID:     "acc-warmup-1",
		Label:  "预热测试账号",
		Status: accounts.StatusActive,
		On:     true,
	}
	fake.agyAccounts = []accounts.Account{acc}
	fake.agyWarmupErr = fmt.Errorf("warmup backend error")

	tt, st := openAgySettingsTest(t, fake)

	// 点击批量预热
	if err := tt.Click("批量预热"); err != nil {
		t.Fatalf("click '批量预热' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.agy.warmupBusy && tt.HasText("批量预热失败: warmup backend error")
	})

	// 点击单账号预热
	if err := tt.Click("预热"); err != nil {
		t.Fatalf("click '预热' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return tt.HasText("预热失败: warmup backend error")
	})

	fake.mu.Lock()
	calls := append([]string(nil), fake.agyWarmupCalls...)
	fake.mu.Unlock()

	if len(calls) < 2 || calls[0] != "" || calls[1] != "acc-warmup-1" {
		t.Fatalf("expected warmup calls [\"\", \"acc-warmup-1\"], got: %v", calls)
	}
}

// 验证卡片内预热状态展示
func TestSettingsAgy_WarmupStateDisplay(t *testing.T) {
	fake := NewFakeClient()
	acc := accounts.Account{
		ID:     "acc-warmup-state",
		Label:  "状态展示账号",
		Status: accounts.StatusActive,
		On:     true,
		LastWarmup: &accounts.WarmupState{
			Model:  "gemini-2.5-flash",
			Status: "success",
		},
	}
	fake.agyAccounts = []accounts.Account{acc}
	tt, _ := openAgySettingsTest(t, fake)

	waitFor(t, tt, func() bool {
		return tt.HasText("gemini-2.5-flash") && tt.HasText("新周期已生效")
	})
}

// 验证设置页自动预热开关切换及错误展示
func TestSettingsAgy_AutoWarmupSwitch(t *testing.T) {
	fake := NewFakeClient()
	fake.agyAutoWarmup = true

	tt, st := openAgySettingsTest(t, fake)

	// 自动预热开关在 agy 行的「账号调度」子区里（「高级」页签），先展开。
	openAgyDispatchForTest(tt, st)

	// 等待加载完成并展示自动预热配置项
	waitFor(t, tt, func() bool {
		return tt.HasText("到期自动预热") && tt.HasText("切换到期自动预热")
	})

	// 切换自动预热开关为关闭
	st.toggleAgyAutoWarmup(false)
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.agy.autoWarmup && !st.agy.autoWarmupBusy
	})

	fake.mu.Lock()
	if fake.agyAutoWarmup {
		t.Fatal("expected fake agyAutoWarmup to be false")
	}
	fake.mu.Unlock()

	// 模拟切换失败并验证错误提示展示
	fake.mu.Lock()
	fake.agySetAutoWarmupErr = fmt.Errorf("disk write failure")
	fake.mu.Unlock()

	st.toggleAgyAutoWarmup(true)
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return tt.HasText("保存自动预热配置失败: disk write failure")
	})
}

// 验证批量预热与单账号预热按钮在进行中（busy）状态下的文案与禁用表现
func TestSettingsAgy_WarmupButtonBusyState(t *testing.T) {
	fake := NewFakeClient()
	acc := accounts.Account{
		ID:     "acc-warmup-busy",
		Label:  "忙碌测试账号",
		Status: accounts.StatusActive,
		On:     true,
	}
	fake.agyAccounts = []accounts.Account{acc}

	tt, st := openAgySettingsTest(t, fake)

	// 设置单账号预热进行中
	st.mu.Lock()
	st.agy.warmupBusyIDs = map[string]bool{"acc-warmup-busy": true}
	st.agy.warmupBusy = true
	st.mu.Unlock()
	tt.Frame()

	// 验证单账号与顶部批量按钮均处于 busy 文案状态
	waitFor(t, tt, func() bool {
		return (tt.HasText("预热中…") || tt.HasText("预热中...")) && (tt.HasText("正在预热…") || tt.HasText("正在预热..."))
	})

	// 验证 busy 状态下按钮禁用，点击不触发新的预热请求
	_ = tt.Click("正在预热…")
	_ = tt.Click("预热中…")
	fake.mu.Lock()
	if len(fake.agyWarmupCalls) != 0 {
		t.Fatalf("expected no warmup calls when buttons are busy, got: %v", fake.agyWarmupCalls)
	}
	fake.mu.Unlock()
}

// agy 面板的节定义顺序固定：账号 → 调度策略 → 会话保持模式 → 调度 → 终端登录。
func TestAgyProviderSheetSectionsOrder(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	defs := agyProviderSheetSections(st)
	wantIDs := []string{"sec-agy-accounts", "sec-agy-strategy", "sec-agy-affinity", "sec-agy-routing", "sec-agy-advanced-login"}
	wantTitles := []string{"账号", "调度策略", "会话保持模式", "调度", "终端登录"}
	if len(defs) != len(wantIDs) {
		t.Fatalf("节数量 = %d, want %d", len(defs), len(wantIDs))
	}
	for i, def := range defs {
		if def.ID != wantIDs[i] || def.Title != wantTitles[i] {
			t.Fatalf("第 %d 节 = %q/%q, want %q/%q", i, def.ID, def.Title, wantIDs[i], wantTitles[i])
		}
		if def.Render == nil {
			t.Fatalf("第 %d 节 %q 缺少 Render", i, def.ID)
		}
	}
}
