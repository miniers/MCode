package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mcode/internal/config"
)

func TestMain(m *testing.M) {
	if runFakeProvider() {
		os.Exit(0)
	}
	// 隔离 HOME/USERPROFILE 与数据目录，避免测试读取开发者真实的 ~/.agents/skills 技能清单、
	// ~/.mcode 配置（agentconf 会读 MCODE_DATA 或回退到 ~/.mcode），污染 prompt 与用例结果。
	root, err := os.MkdirTemp("", "mcode-app-test-home-")
	if err == nil {
		_ = os.Setenv("HOME", root)
		_ = os.Setenv("USERPROFILE", root)
		_ = os.Setenv("MCODE_DATA", filepath.Join(root, "mcode-data"))
	}

	// 测试中禁用自动标题的真实模型调用，避免拉起真实的 claude/codex CLI 发送标题生成请求。
	autoTitleGenerateText = func(ctx context.Context, cfg config.Config, secrets config.SecretStore, provider, model, effort, prompt string) (string, error) {
		return "", errors.New("auto title disabled in tests")
	}

	code := m.Run()

	if err == nil {
		_ = os.RemoveAll(root)
	}
	os.Exit(code)
}
