package app

import (
	"testing"

	"mcode/internal/config"
)

// disableStewardForTest 在 dataDir 写一份关闭总管的配置，供与总管无关的测试使用。
// 总管默认启用，Open 会在后台准备总管会话，并经 EnsureHomeProject 把总管主目录
// 登记成一个项目；这个过程与断言项目数量的测试赛跑，会让项目数随机多 1。
// enabled_choice 一并置 true，避免旧配置迁移又把总管打开。
func disableStewardForTest(t *testing.T, dataDir string) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Orchestration.Steward.Enabled = false
	cfg.Orchestration.Steward.EnabledChoice = true
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("写入关闭总管的配置失败: %v", err)
	}
}
