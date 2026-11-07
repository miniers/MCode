package mcodeui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// TestSettingsPageSourceControl_Tree 验证版本控制页已经走声明式页面树：
// 原高级区行直接可见、锁定档位显示徽标、开关能写入配置。
func TestSettingsPageSourceControl_Tree(t *testing.T) {
	locked := map[string]bool{"default-workspace-mode": true}
	st := NewSettingsState(settingsTestDir(t), locked, newChatState(NewFakeClient()))

	tt := ui.NewTester(func(c *ui.Context) { renderPageSourceControl(c, st) }, 1000, 3000)

	texts := make(map[string]bool)
	for _, text := range tt.Texts() {
		texts[text] = true
	}
	for _, want := range []string{
		"提交信息生成", "自动生成提交信息", "提交信息附加说明", "生成提交信息与会话标题的模型",
		"工作区与工作树", "默认工作区模式", "本地工作区", "独立工作树",
		"删除会话时自动清理工作树", "自动清理已合并的工作树",
		"Worktree 清理", "锁定", "高级", "新建工作树时初始化子模块", "新建工作树时克隆依赖目录", "复制的目录名单",
	} {
		if !texts[want] {
			t.Fatalf("版本控制页缺少 %q: %v", want, tt.Texts())
		}
	}

	for _, gone := range []string{
		"管理提交信息生成规范与工作树策略",
		"由启动参数指定，无法更改",
		"用 utility 角色模型生成",
		"未选择时沿用 utility 角色模型",
	} {
		if tt.HasText(gone) {
			t.Fatalf("版本控制页不该再有 %q", gone)
		}
	}

	if err := tt.Click("切换删除会话时自动清理工作树"); err != nil {
		t.Fatalf("click cleanup switch failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return st.cfg.SourceControl.AutoCleanupWorktreesOnDelete != nil &&
			!*st.cfg.SourceControl.AutoCleanupWorktreesOnDelete
	})
}

func TestSettingsSourceControl_PageRows(t *testing.T) {
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)

	st := NewSettingsState(dataDir, nil, newChatState(fake))
	tt := ui.NewTester(func(c *ui.Context) { renderPageSourceControl(c, st) }, 1000, 5000)

	waitFor(t, tt, func() bool {
		return tt.HasText("提交信息生成") &&
			tt.HasText("工作区与工作树") &&
			tt.HasText("自动生成提交信息") &&
			tt.HasText("提交信息附加说明") &&
			tt.HasText("生成提交信息与会话标题的模型") &&
			tt.HasText("默认工作区模式") &&
			tt.HasText("删除会话时自动清理工作树") &&
			tt.HasText("自动清理已合并的工作树") &&
			tt.HasText("Worktree 清理")
	})

	// 页面不再展示当前仓库信息区块
	if tt.HasText("当前仓库") || tt.HasText("仓库路径") || tt.HasText("当前分支") {
		t.Errorf("expected the source control page to hide the current repository section")
	}

	// 检查默认配置值
	cfg, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if !cfg.SourceControl.GenerateCommitMessageEnabled() {
		t.Errorf("expected GenerateCommitMessageEnabled to be true by default")
	}
	if !cfg.SourceControl.AutoCleanupWorktreesOnDeleteEnabled() {
		t.Errorf("expected AutoCleanupWorktreesOnDeleteEnabled to be true by default")
	}
	if !cfg.SourceControl.AutoCleanupMergedWorktreesEnabled() {
		t.Errorf("expected AutoCleanupMergedWorktreesEnabled to be true by default")
	}
	if !cfg.SourceControl.InitSubmodulesEnabled() {
		t.Errorf("expected InitSubmodulesEnabled to be true by default")
	}
	if cfg.SourceControl.DefaultWorkspaceMode != "local" {
		t.Errorf("expected DefaultWorkspaceMode to be local by default, got %q", cfg.SourceControl.DefaultWorkspaceMode)
	}
}

func TestSettingsSourceControl_LockedKeys(t *testing.T) {
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)
	locked := map[string]bool{
		"generate-commit-message": true,
		"commit-instructions":     true,
	}

	st := NewSettingsState(dataDir, locked, newChatState(fake))
	tt := ui.NewTester(func(c *ui.Context) { renderPageSourceControl(c, st) }, 1000, 5000)

	waitFor(t, tt, func() bool {
		return tt.HasText("锁定")
	})
	// Badge 给行节点和文字节点登记同一个 Label，tt.Texts() 因此把每个徽章算两次；
	// 相邻去重后数的是徽章个数。
	badges, prev := 0, false
	for _, text := range tt.Texts() {
		match := strings.Contains(text, "锁定")
		if match && !prev {
			badges++
		}
		prev = match
	}
	if badges != 2 {
		t.Fatalf("expected 2 locked badges, got %d: %v", badges, tt.Texts())
	}
	if tt.HasText("由启动参数指定，无法更改") {
		t.Fatalf("锁定不再使用说明句: %v", tt.Texts())
	}
}
