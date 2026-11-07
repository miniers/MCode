package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

// beeRetentionSwitchLabel 是该开关的无障碍 Label，也是测试点击时的定位依据：
// mygo 的 Tester.Click 按显示文本或 Label 匹配，不认组件 key。
const beeRetentionSwitchLabel = "切换压缩 30 天前的子代理消息"

// TestBeeMessageRetentionSwitch 覆盖会话页里的「压缩 30 天前的子代理消息」开关：
// 默认开启，点一下变成关闭并写进 config.json，再点一下恢复开启。
func TestBeeMessageRetentionSwitch(t *testing.T) {
	s := newChatState(NewFakeClient())
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, s)
	tt := ui.NewTester(func(c *ui.Context) {
		renderPageConversation(c, st)
	}, 1000, 2200)

	if !st.cfg.Retention.BeeMessagesEnabled() {
		t.Fatalf("默认应显示为开启")
	}
	if !tt.HasText("压缩 30 天前的子代理消息") {
		t.Fatalf("会话页缺少子代理消息保留行: %v", tt.Texts())
	}
	if err := tt.Click(beeRetentionSwitchLabel); err != nil {
		t.Fatalf("关闭子代理消息保留开关失败(%s): %v", beeRetentionSwitchLabel, err)
	}
	waitFor(t, tt, func() bool {
		return st.cfg.Retention.BeeMessages != nil && !st.cfg.Retention.BeeMessagesEnabled()
	})
	settingsSaveWriters.Wait()

	saved, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("读取已保存的配置失败: %v", err)
	}
	if saved.Retention.BeeMessagesEnabled() {
		t.Fatalf("开关关闭后未写入 config.json: %+v", saved.Retention)
	}

	if err := tt.Click(beeRetentionSwitchLabel); err != nil {
		t.Fatalf("重新打开子代理消息保留开关失败(%s): %v", beeRetentionSwitchLabel, err)
	}
	waitFor(t, tt, func() bool { return st.cfg.Retention.BeeMessagesEnabled() })
	settingsSaveWriters.Wait()

	saved, err = config.Load(dataDir)
	if err != nil {
		t.Fatalf("读取已保存的配置失败: %v", err)
	}
	if !saved.Retention.BeeMessagesEnabled() {
		t.Fatalf("开关重新打开后未写入 config.json: %+v", saved.Retention)
	}
}
