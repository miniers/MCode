package app

import (
	"testing"

	"mcode/internal/config"
)

func disableStewardForTest(t *testing.T, dataDir string) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Orchestration.Steward.Enabled = false
	cfg.Orchestration.Steward.EnabledChoice = true
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("写入关闭总管的配置失败: %v", err)
	}
}
