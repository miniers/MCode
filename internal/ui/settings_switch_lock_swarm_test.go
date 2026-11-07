package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

// 编排页这几个开关改为持锁写 cfg 后，行为必须保持不变：
// 点击翻转内存里的字段，并把新值写进 config.json。
// 定位用开关的 Label 文本（mygo 的 Tester.Click 按显示文本或 Label 匹配，不认组件 key）。

// TestSwarmSwitchRetryFailedPersists 覆盖「失败时自动重试」：从关闭切换到开启并落盘。
func TestSwarmSwitchRetryFailedPersists(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.cfg.Orchestration.RetryFailed = false
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(1100).Children(func() { renderPageSwarm(c, st) })
	}, 1200, 7000)
	tt.Frame()

	if st.cfg.Orchestration.RetryFailed {
		t.Fatalf("失败时自动重试应默认关闭")
	}
	if err := tt.Click("切换失败时自动重试"); err != nil {
		t.Fatalf("点击失败时自动重试开关失败: %v", err)
	}
	waitFor(t, tt, func() bool { return st.cfg.Orchestration.RetryFailed })
	settingsSaveWriters.Wait()

	saved, err := config.Load(st.dataDir)
	if err != nil {
		t.Fatalf("读取已保存的配置失败: %v", err)
	}
	if !saved.Orchestration.RetryFailed {
		t.Fatalf("开启失败时自动重试后未写入 config.json: %+v", saved.Orchestration)
	}
}

// TestSwarmSwitchAutoMergePersists 覆盖「自动合并」：从关闭切换到开启并落盘。
func TestSwarmSwitchAutoMergePersists(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.cfg.Orchestration.AutoMerge = false
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(1100).Children(func() { renderPageSwarm(c, st) })
	}, 1200, 7000)
	tt.Frame()

	if st.cfg.Orchestration.AutoMerge {
		t.Fatalf("自动合并应默认关闭")
	}
	if err := tt.Click("切换自动合并"); err != nil {
		t.Fatalf("点击自动合并开关失败: %v", err)
	}
	waitFor(t, tt, func() bool { return st.cfg.Orchestration.AutoMerge })
	settingsSaveWriters.Wait()

	saved, err := config.Load(st.dataDir)
	if err != nil {
		t.Fatalf("读取已保存的配置失败: %v", err)
	}
	if !saved.Orchestration.AutoMerge {
		t.Fatalf("开启自动合并后未写入 config.json: %+v", saved.Orchestration)
	}
	if saved.Orchestration.AutoPush {
		t.Fatalf("只因切换自动合并就被连带动了自动推送: %+v", saved.Orchestration)
	}
}

// TestSwarmSwitchShowsBeeDetailsPersists 覆盖「显示子代理详细步骤」：从关闭切换到开启并落盘，
// 同时不能连带改动相邻的「自动弹出子代理面板」。
func TestSwarmSwitchShowsBeeDetailsPersists(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.cfg.Orchestration.ShowsBeeDetails = false
	st.cfg.Orchestration.AutoPopsBees = false
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(1100).Children(func() { renderPageSwarm(c, st) })
	}, 1200, 7000)
	tt.Frame()

	if st.cfg.Orchestration.ShowsBeeDetails {
		t.Fatalf("显示子代理详细步骤应默认关闭")
	}
	if err := tt.Click("切换显示子代理详细步骤"); err != nil {
		t.Fatalf("点击显示子代理详细步骤开关失败: %v", err)
	}
	waitFor(t, tt, func() bool { return st.cfg.Orchestration.ShowsBeeDetails })
	settingsSaveWriters.Wait()

	saved, err := config.Load(st.dataDir)
	if err != nil {
		t.Fatalf("读取已保存的配置失败: %v", err)
	}
	if !saved.Orchestration.ShowsBeeDetails {
		t.Fatalf("开启显示子代理详细步骤后未写入 config.json: %+v", saved.Orchestration)
	}
	if saved.Orchestration.AutoPopsBees {
		t.Fatalf("只因切换显示子代理详细步骤就被连带动了自动弹出子代理面板: %+v", saved.Orchestration)
	}
}
