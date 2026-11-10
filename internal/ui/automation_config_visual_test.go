package mcodeui

import (
	"github.com/egoist/mygo/ui"
	"testing"
)

func TestAutomationConfigVisualGroups(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		a := Automation{Prompt: "任务", Enabled: enabled, RunMode: "heartbeat", Model: "model-x", RuntimeMode: "readonly", MaxIterations: 7, CatchUp: true}
		tt := ui.NewTester(func(c *ui.Context) { renderAutomationConfig(c, a) }, 660, 800)
		tt.Frame()
		status := "状态：已暂停"
		if enabled {
			status = "状态：已启用"
		}
		for _, text := range []string{"计划", "执行", "停止与补跑", status, "心跳模式沿用原会话设置", "模型：model-x", "权限：" + automationRuntimeModeLabel("readonly"), "运行次数：最多 7 次", "错过后补跑：开启"} {
			if !tt.HasText(text) {
				t.Errorf("缺少 %s", text)
			}
		}
		a.MaxRuns = 3
		a.CatchUp = false
		tt.Frame()
		for _, text := range []string{"运行次数：最多 3 次", "错过后补跑：关闭"} {
			if !tt.HasText(text) {
				t.Errorf("缺少 %s", text)
			}
		}
	}
}
