package remote

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	simulatedReplyStepDelay = time.Millisecond

	// handoff 预览默认调用模型生成分支名（最长 8 秒）；测试统一替换为空返回值，
	// 避免调用真实 CLI 并让预览回退到本地 slug。
	generateWorktreeMoveBranch = func(ctx context.Context, provider, title string) string { return "" }

	// 隔离 HOME 与数据目录，避免测试读取开发者真实的 ~/.agents/skills 技能清单、
	// ~/.mcode 配置（agentconf 会读 MCODE_DATA 或回退到 ~/.mcode），污染 prompt 与用例结果。
	root, err := os.MkdirTemp("", "mcode-remote-test-home-")
	if err == nil {
		_ = os.Setenv("HOME", root)
		_ = os.Setenv("MCODE_DATA", filepath.Join(root, ".mcode"))
		// 测试不应依赖开发者真实的 shell 配置：/bin/sh 加载登录环境快且确定，
		// 避免 zsh 插件初始化拖慢 loginenv 并让依赖 2 秒超时的用例失败。
		_ = os.Setenv("SHELL", "/bin/sh")
		_ = os.Unsetenv("ZDOTDIR")
	}

	code := m.Run()

	if err == nil {
		_ = os.RemoveAll(root)
	}
	os.Exit(code)
}
