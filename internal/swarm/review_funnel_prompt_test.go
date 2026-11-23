package swarm

import (
	"strings"
	"testing"
)

func TestAcceptanceFailurePromptSuffix(t *testing.T) {
	t.Parallel()
	if !strings.Contains(acceptanceFailurePromptSuffix, "整条验收命令由 mcode 在你汇报后重跑") {
		t.Errorf("acceptanceFailurePromptSuffix 缺少预期提示: %q", acceptanceFailurePromptSuffix)
	}
	if !strings.Contains(acceptanceFailurePromptSuffix, "-run") {
		t.Errorf("acceptanceFailurePromptSuffix 缺少 -run 指引: %q", acceptanceFailurePromptSuffix)
	}
	if strings.Contains(acceptanceFailurePromptSuffix, "用失败的那条命令确认") {
		t.Errorf("acceptanceFailurePromptSuffix 不应再包含旧提示: %q", acceptanceFailurePromptSuffix)
	}
}
