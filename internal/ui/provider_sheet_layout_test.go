package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/providers/agy/accounts"
	"mcode/internal/testenv"
)

func TestProviderSheetSectionForRow(t *testing.T) {
	cases := []struct {
		name string
		kind string
		id   string
		row  string
		want string
	}{
		{"预设的模型行", "preset", "openai", "row-preset-form-model", "sec-provider-models"},
		{"自定义端点的地址行", "custom", "my-endpoint", "row-endpoint-form-baseurl", "sec-provider-account"},
		{"Claude 的 Magpie 接入行", "cli", "claude", "row-provider-magpie", ""},
		{"Claude 命令行行", "cli", "claude", "row-claude-bin", "sec-provider-cli"},
		{"通用命令行行", "cli", "codex", "row-provider-bin", "sec-provider-cli"},
		{"模型行", "cli", "claude", "provider-model-sonnet", "sec-provider-models"},
		{"账号行", "cli", "claude", "row-provider-account", "sec-provider-account"},
		{"agy 调度策略", "cli", "agy", "sec-agy-routing-strategy", "sec-agy-strategy"},
		{"agy 会话保持模式", "cli", "agy", "sec-agy-routing-affinity", "sec-agy-affinity"},
		{"agy 冷却账号", "cli", "agy", "sec-agy-routing-sink", "sec-agy-routing"},
		{"agy 自动预热", "cli", "agy", "sec-agy-auto-warmup", "sec-agy-routing"},
		{"agy 浏览器登录", "cli", "agy", "row-agy-browser-login", "sec-agy-accounts"},
		{"agy 终端登录", "cli", "agy", "row-agy-terminal-login", "sec-agy-advanced-login"},
		{"未知供应商类型", "unknown", "x", "row-provider-magpie", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := providerSheetSectionForRow(tc.kind, tc.id, tc.row); got != tc.want {
				t.Fatalf("providerSheetSectionForRow(%q, %q, %q) = %q，期望 %q", tc.kind, tc.id, tc.row, got, tc.want)
			}
		})
	}
}

func Test供应商面板_身份区(t *testing.T) {
	tt, st := openProvidersPageTest(t, NewFakeClient())
	clickProviderDetailTabForTest(t, tt, "claude", "")
	waitFor(t, tt, func() bool {
		sheet, _ := settingsTreeSheetState(st)
		_, id := currentProviderSheet(st)
		return sheet == "provider" && id == "claude" && tt.HasText("关闭供应商")
	})
	for _, want := range []string{"Claude", "未安装", "安装"} {
		if !tt.HasText(want) {
			t.Fatalf("身份区缺少 %q：%v", want, tt.Texts())
		}
	}
}

func Test供应商面板_单页与高级折叠(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetProviders([]ProviderInfo{{ID: "claude", Name: "Claude", Available: true, Models: []ModelInfo{{ID: "test-model", Name: "测试模型"}}}})
	tt, st := openProvidersPageTest(t, fake)
	clickProviderDetailTabForTest(t, tt, "claude", "")
	waitFor(t, tt, func() bool { return tt.HasText("账号") && tt.HasText("模型") && tt.HasText("展开高级") })
	if tt.HasText("返回概览") || tt.HasText("可执行文件路径") {
		t.Fatalf("初始页面应直接显示常用节并收起高级：%v", tt.Texts())
	}
	clickWhenSettled(t, tt, "展开高级")
	waitFor(t, tt, func() bool { return tt.HasText("可执行文件路径") && tt.HasText("收起高级") })
	if !providerSheetAdvancedOpen(st, "cli", "claude") {
		t.Fatal("高级展开状态未保存")
	}
	clickWhenSettled(t, tt, "收起高级")
	waitFor(t, tt, func() bool { return !tt.HasText("可执行文件路径") && tt.HasText("展开高级") })
}

func Test供应商面板_重新打开收起高级(t *testing.T) {
	tt, st := openProvidersPageTest(t, NewFakeClient())
	clickProviderDetailTabForTest(t, tt, "claude", "命令行")
	waitFor(t, tt, func() bool { return tt.HasText("可执行文件路径") })
	closeSettingsSheet(st)
	tt.Frame()
	openProviderSheet(st, "cli", "claude")
	waitFor(t, tt, func() bool {
		return tt.HasText("展开高级") && !tt.HasText("可执行文件路径") && !providerSheetAdvancedOpen(st, "cli", "claude")
	})
}

func Test供应商面板_模型节顶部Magpie接入(t *testing.T) {
	fake := NewFakeClient()
	setting := routeTestSetting("claude", agentMagpieModeInject)
	fake.SetAgentMagpie(setting)
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(fake))
	openProviderSheet(st, "cli", "claude")
	body := ui.NewTester(func(c *ui.Context) { renderProviderSheetBody(c, st) }, 1200, 4000)
	waitFor(t, body, func() bool { value, known := magpieAgentRouteValue(st, "claude"); return known && value == "仅 mcode" })
	if !body.HasText("Magpie 接入") {
		t.Fatalf("模型节缺少 Magpie 接入行：%v", body.Texts())
	}
	if _, ok := body.Find(magpieAgentRouteLabel(magpieAgentTitle("claude"))); ok {
		t.Fatalf("模型节不应再内联路由分段：%v", body.Texts())
	}
	for _, text := range body.Texts() {
		if text == "路由" {
			t.Fatal("不应有独立路由节标题")
		}
	}
	// 标题栏不再重复分段。
	hero := ui.NewTester(func(c *ui.Context) { renderProviderSheetHero(c, st) }, 1200, 2400)
	hero.Frame()
	if _, ok := hero.Find(magpieAgentRouteLabel(magpieAgentTitle("claude"))); ok {
		t.Fatalf("标题栏不应再显示路由分段：%v", hero.Texts())
	}
}

func Test供应商面板_搜索命令行自动展开高级(t *testing.T) {
	tt, st := openProvidersPageTest(t, NewFakeClient())
	openProviderSheet(st, "cli", "claude")
	openSettingsSheetFrom(st, "provider", "row-claude-bin", "")
	st.mu.Lock()
	st.jumpToItemID = "row-claude-bin"
	st.mu.Unlock()
	waitFor(t, tt, func() bool {
		return providerSheetAdvancedOpen(st, "cli", "claude") && tt.HasText("可执行文件路径")
	})
	tt.Frame()
	st.mu.Lock()
	jump := st.jumpToItemID
	st.mu.Unlock()
	if jump != "" {
		t.Fatalf("命令行目标行渲染后应消费跳转标记，实际 %q", jump)
	}
}

func Test供应商面板_模型目录只读并显示模型ID(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetProviders([]ProviderInfo{{
		ID: "claude", Name: "Claude", Available: true,
		Models: []ModelInfo{
			{ID: "sonnet-catalog", Name: "目录 Sonnet"},
			{ID: "unnamed-catalog"},
		},
	}})
	tt, _ := openProvidersPageTest(t, fake)
	clickProviderDetailTabForTest(t, tt, "claude", "")
	waitFor(t, tt, func() bool { return tt.HasText("目录 Sonnet") && tt.HasText("sonnet-catalog") })
	if !tt.HasText("只读") {
		t.Fatalf("模型目录缺少只读说明：%v", tt.Texts())
	}
	if !tt.HasText("unnamed-catalog") {
		t.Fatalf("无名称的模型应使用 ID 作为标题：%v", tt.Texts())
	}
}

func Test供应商面板_预设没有高级折叠区(t *testing.T) {
	fake := NewFakeClient()
	fake.providerSettings = []ProviderSetting{{ID: "deepseek", Name: "DeepSeek", Kind: "api", BaseURL: "https://api.deepseek.com/v1", DefaultModel: "deepseek-chat"}}
	tt, st := openProvidersPageTest(t, fake)
	openProviderSheet(st, "preset", "deepseek")
	waitFor(t, tt, func() bool {
		return tt.HasText("账号") && tt.HasText("模型") && tt.HasText("接口地址（BaseURL）")
	})
	if tt.HasText("展开高级") || tt.HasText("返回概览") {
		t.Fatalf("预设应直接显示全部内容：%v", tt.Texts())
	}
}

func Test供应商面板_agy滚动后展开高级(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.agyAccounts = []accounts.Account{{ID: "acc-1", Email: "agy-user@example.com", Status: accounts.StatusActive}}
	fake.agyDefaultAccountID = "acc-1"
	tt, st := openProvidersPageTest(t, fake)
	clickProviderDetailTabForTest(t, tt, "agy", "")
	waitFor(t, tt, func() bool { return tt.HasText("agy-user@example.com") && tt.HasText("展开高级") })
	login, ok := tt.Find("浏览器登录")
	if !ok {
		t.Fatalf("账号节缺少浏览器登录按钮：%v", tt.Texts())
	}
	// 从账号节的可视位置滚到底，再通过真实点击展开高级。
	tt.Scroll(login.X+login.W/2, login.Y+login.H/2, 0, 1e6)
	tt.Frame()
	clickWhenSettled(t, tt, "展开高级")
	waitFor(t, tt, func() bool { return tt.HasText("收起高级") && tt.HasText("终端登录（备用）") })
	if kind, id := currentProviderSheet(st); kind != "cli" || id != "agy" {
		t.Fatalf("展开高级后 agy 面板应仍然打开，当前为 %q/%q", kind, id)
	}
}

func Test供应商面板_agy常用节与高级提示(t *testing.T) {
	tt, _ := openProvidersPageTest(t, NewFakeClient())
	clickProviderDetailTabForTest(t, tt, "agy", "")
	waitFor(t, tt, func() bool {
		return tt.HasText("调度策略") && tt.HasText("会话保持模式") && tt.HasText("展开高级")
	})
	if !tt.HasText("调度、终端登录") || tt.HasText("终端登录（备用）") {
		t.Fatalf("agy 高级折叠提示或默认状态错误：%v", tt.Texts())
	}
}
