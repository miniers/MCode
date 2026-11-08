package mcodeui

import (
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

// settingsResetBoolPtr / settingsResetStringPtr 给测试构造三态与指针字段。
func settingsResetBoolPtr(v bool) *bool { return &v }

func settingsResetStringPtr(v string) *string { return &v }

// TestResetPreferenceFields 覆盖重置辅助函数的四条规则：标量与指针按默认值覆盖、列表类数据保留、
// 跳过名单保留、嵌套结构体递归生效。
func TestResetPreferenceFields(t *testing.T) {
	dst := config.Defaults()
	def := config.Defaults()

	// 标量与三态指针：都应回到默认值。
	dst.Session.ShowReasoning = settingsResetBoolPtr(false)
	dst.Session.QueueWhileRunning = !def.Session.QueueWhileRunning
	dst.Session.ComposerSendKey = config.ComposerSendKeyShiftEnter
	dst.Session.ComposerAttachmentLimit = 2
	dst.Notifications.ChimeWhenAttentionNeeded = nil

	// 嵌套结构体（总管）里的标量要递归重置。
	dst.Orchestration.Steward.Enabled = false
	dst.Orchestration.Steward.DailyWakeLimit = 99

	// 跳过名单：迁移标记与用户目录、目录名单都保留。
	dst.Orchestration.Steward.EnabledChoice = true
	dst.Orchestration.Steward.HomeDir = "/tmp/steward-home"
	dst.SourceControl.CloneIgnoredPatterns = settingsResetStringPtr("node_modules")

	// 列表类数据：切片与 map 都保留用户原值。
	dst.Orchestration.Pairs = []config.SwarmPair{{ID: "mine", Name: "我的组合"}}
	dst.Orchestration.Tiers = []config.SwarmTier{{ID: "tier-mine", Name: "我的档位"}}
	dst.Memory.SpaceOverrides = map[string]string{"space": "abcdef"}

	resetPreferenceFields(reflect.ValueOf(&dst).Elem(), reflect.ValueOf(def))

	if p := dst.Session.ShowReasoning; p == nil || !*p {
		t.Fatalf("ShowReasoning 应重置为默认 true，实际 %v", p)
	}
	if dst.Session.QueueWhileRunning != def.Session.QueueWhileRunning {
		t.Fatalf("QueueWhileRunning 应重置为 %v，实际 %v", def.Session.QueueWhileRunning, dst.Session.QueueWhileRunning)
	}
	if dst.Session.ComposerSendKey != def.Session.ComposerSendKey {
		t.Fatalf("ComposerSendKey 应重置为 %v，实际 %v", def.Session.ComposerSendKey, dst.Session.ComposerSendKey)
	}
	if dst.Session.ComposerAttachmentLimit != def.Session.ComposerAttachmentLimit {
		t.Fatalf("ComposerAttachmentLimit 应重置为 %v，实际 %v", def.Session.ComposerAttachmentLimit, dst.Session.ComposerAttachmentLimit)
	}
	if p := dst.Notifications.ChimeWhenAttentionNeeded; p == nil || !*p {
		t.Fatalf("指针字段为 nil 时应写入默认值，实际 %v", p)
	}

	if !dst.Orchestration.Steward.Enabled {
		t.Fatal("嵌套的 Steward.Enabled 未递归重置为默认 true")
	}
	if dst.Orchestration.Steward.DailyWakeLimit != def.Orchestration.Steward.DailyWakeLimit {
		t.Fatalf("嵌套的 Steward.DailyWakeLimit 应重置为 %d，实际 %d",
			def.Orchestration.Steward.DailyWakeLimit, dst.Orchestration.Steward.DailyWakeLimit)
	}

	if !dst.Orchestration.Steward.EnabledChoice {
		t.Fatal("Steward.EnabledChoice 是迁移标记，重置时必须保留")
	}
	if dst.Orchestration.Steward.HomeDir != "/tmp/steward-home" {
		t.Fatalf("总管主目录属于用户路径，重置时必须保留，实际 %q", dst.Orchestration.Steward.HomeDir)
	}
	if p := dst.SourceControl.CloneIgnoredPatterns; p == nil || *p != "node_modules" {
		t.Fatalf("CloneIgnoredPatterns 属于用户名单，重置时必须保留，实际 %v", p)
	}

	if len(dst.Orchestration.Pairs) != 1 || dst.Orchestration.Pairs[0].ID != "mine" {
		t.Fatalf("Pairs 是列表类数据，重置时必须保留，实际 %#v", dst.Orchestration.Pairs)
	}
	if len(dst.Orchestration.Tiers) != 1 || dst.Orchestration.Tiers[0].ID != "tier-mine" {
		t.Fatalf("Tiers 是列表类数据，重置时必须保留，实际 %#v", dst.Orchestration.Tiers)
	}
	if got, ok := dst.Memory.SpaceOverrides["space"]; !ok || got != "abcdef" {
		t.Fatalf("SpaceOverrides 是 map，重置时必须保留，实际 %#v", dst.Memory.SpaceOverrides)
	}
}

// openSettingsResetDialog 点开当前页的「恢复本页默认」。
func openSettingsResetDialog(t *testing.T, tt *ui.Tester, st *SettingsState, page string) {
	t.Helper()
	if err := settingsClick(t, tt, "恢复本页默认"); err != nil {
		t.Fatalf("点击恢复本页默认失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("恢复默认") })
}

// confirmSettingsReset 在确认框里点「恢复默认」并等待待确认状态清空。
func confirmSettingsReset(t *testing.T, tt *ui.Tester, st *SettingsState) {
	t.Helper()
	if err := tt.Click("恢复默认"); err != nil {
		t.Fatalf("点击确认按钮失败: %v", err)
	}
	waitFor(t, tt, func() bool { return st.resetPendingPage == "" })
}

// TestSettingsReset_SupportedPages 断言有可重置普通行的页面渲染按钮、其余页不渲染。
func TestSettingsReset_SupportedPages(t *testing.T) {
	tt, st := newSettingsAdvancedTester(t)
	for _, page := range []string{"conversation", "general", "appearance", "workbench", "swarm", "sourceControl", "nowledge", "remote"} {
		openSettingsAdvancedPage(t, tt, st, page)
		if !tt.HasText("恢复本页默认") {
			t.Fatalf("%s 页底部应有「恢复本页默认」按钮", page)
		}
	}
	for _, page := range []string{"magpie", "models", "extensions"} {
		openSettingsAdvancedPage(t, tt, st, page)
		if tt.HasText("恢复本页默认") {
			t.Fatalf("%s 页不应有「恢复本页默认」按钮", page)
		}
	}
}

// TestSettingsReset_ConversationPage 会话页重置只影响 cfg.Session，其他段的改动保持。
func TestSettingsReset_ConversationPage(t *testing.T) {
	tt, st := newSettingsAdvancedTester(t)
	openSettingsAdvancedPage(t, tt, st, "conversation")
	waitFor(t, tt, func() bool { return tt.HasText("恢复本页默认") })

	def := config.Defaults()
	// 会话页字段改成非默认值，同时改一处外观页字段，用来确认重置不越界。
	st.cfg.Session.QueueWhileRunning = !def.Session.QueueWhileRunning
	st.cfg.Appearance.Theme = config.ThemeDark
	tt.Frame()

	openSettingsResetDialog(t, tt, st, "conversation")
	// 确认框要说清重置范围，并点明列表类数据不受影响。
	if !tt.HasText("只把本页对应的设置改回默认值，其他页不受影响。") {
		t.Fatalf("确认框正文缺少适用范围说明: %q", tt.Texts())
	}
	if !tt.HasText("重置范围：回复与思考、消息与排队、完成与提醒、保留与归档") {
		t.Fatalf("确认框没有列出会话页的重置范围: %q", tt.Texts())
	}
	if !tt.HasText("快捷回复、模型组合、档位等列表类数据保留。") {
		t.Fatalf("确认框没说明列表类数据不受影响: %q", tt.Texts())
	}
	confirmSettingsReset(t, tt, st)

	waitFor(t, tt, func() bool { return st.cfg.Session.QueueWhileRunning == def.Session.QueueWhileRunning })
	if st.cfg.Session.QueueWhileRunning != def.Session.QueueWhileRunning {
		t.Fatalf("QueueWhileRunning 应恢复默认 %v，实际 %v", def.Session.QueueWhileRunning, st.cfg.Session.QueueWhileRunning)
	}
	if st.cfg.Appearance.Theme != config.ThemeDark {
		t.Fatalf("会话页重置不应改动外观页设置，实际主题 %q", st.cfg.Appearance.Theme)
	}
	if tt.HasText("恢复默认") {
		t.Fatalf("确认后弹窗应关闭: %q", tt.Texts())
	}
}

// TestSettingsReset_CancelKeepsConfig 取消确认时配置不变。
func TestSettingsReset_CancelKeepsConfig(t *testing.T) {
	tt, st := newSettingsAdvancedTester(t)
	openSettingsAdvancedPage(t, tt, st, "conversation")
	waitFor(t, tt, func() bool { return tt.HasText("恢复本页默认") })

	st.cfg.Session.QueueWhileRunning = !config.Defaults().Session.QueueWhileRunning
	tt.Frame()

	openSettingsResetDialog(t, tt, st, "conversation")
	if err := tt.Click("取消"); err != nil {
		t.Fatalf("点击取消失败: %v", err)
	}
	waitFor(t, tt, func() bool { return st.resetPendingPage == "" })

	if st.cfg.Session.QueueWhileRunning == config.Defaults().Session.QueueWhileRunning {
		t.Fatal("取消确认后会话页设置不应被重置")
	}
	if tt.HasText("恢复默认") {
		t.Fatalf("取消后弹窗应关闭: %q", tt.Texts())
	}
}

// TestSettingsReset_SwarmKeepsLists 编排页重置保留列表类数据（模型组合、档位）。
func TestSettingsResetTree_SwarmKeepsLists(t *testing.T) {
	tt, st := newSettingsAdvancedTester(t)
	openSettingsAdvancedPage(t, tt, st, "swarm")
	waitFor(t, tt, func() bool { return tt.HasText("恢复本页默认") })

	st.cfg.Orchestration.Pairs = []config.SwarmPair{{ID: "mine", Name: "我的组合"}}
	st.cfg.Orchestration.Tiers = []config.SwarmTier{{ID: "tier-mine", Name: "我的档位"}}
	st.cfg.Orchestration.Steward.EnabledChoice = true
	// 选一个树里实现了 reset 且未锁定的开关，用来确认重置确实执行过。
	st.cfg.Orchestration.ShowsBeeDetails = false
	tt.Frame()

	openSettingsResetDialog(t, tt, st, "swarm")
	confirmSettingsReset(t, tt, st)

	if len(st.cfg.Orchestration.Pairs) != 1 || st.cfg.Orchestration.Pairs[0].ID != "mine" {
		t.Fatalf("编排页重置不应改动模型组合，实际 %#v", st.cfg.Orchestration.Pairs)
	}
	if len(st.cfg.Orchestration.Tiers) != 1 || st.cfg.Orchestration.Tiers[0].ID != "tier-mine" {
		t.Fatalf("编排页重置不应改动档位，实际 %#v", st.cfg.Orchestration.Tiers)
	}
	if !st.cfg.Orchestration.ShowsBeeDetails {
		t.Fatal("ShowsBeeDetails 应随编排页重置回到默认 true")
	}
	if !st.cfg.Orchestration.Steward.EnabledChoice {
		t.Fatal("迁移标记 EnabledChoice 必须保留，否则后续迁移会改写总管开关")
	}
}
