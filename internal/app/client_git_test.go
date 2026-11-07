package app

import (
	"testing"

	"mcode/internal/config"
	"mcode/internal/git"
)

// TestClientGitRunnerSharesRunnerAndSyncsSubmodules 覆盖「Client 懒建第二个执行器」与
// 「新建工作树时初始化子模块」只在启动时同步一次这两条：Client 必须复用装配处注入的执行器，
// 并且配置文件被设置页改过之后，取执行器时要把开关重新推给 git 包。
func TestClientGitRunnerSharesRunnerAndSyncsSubmodules(t *testing.T) {
	prev := git.GetInitSubmodules()
	t.Cleanup(func() { git.SetInitSubmodules(prev) })

	dataDir := t.TempDir()
	cfg := config.Defaults()
	disabled := false
	cfg.SourceControl.InitSubmodules = &disabled
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	client := NewClient(nil)
	client.SetProviderStore(dataDir, nil)

	runner := git.NewRunner("", 4)
	client.SetGit(runner)
	if got := client.gitRunner(); got != runner {
		t.Fatalf("gitRunner 应复用装配处注入的执行器")
	}
	if git.GetInitSubmodules() {
		t.Fatalf("SetGit 应把已关闭的子模块开关推给 git 包")
	}

	// 设置页直接写 config.json，不通知 Client。
	enabled := true
	cfg.SourceControl.InitSubmodules = &enabled
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("save config again: %v", err)
	}
	client.gitRunner()
	if !git.GetInitSubmodules() {
		t.Fatalf("配置改过后取执行器应重新回推子模块开关")
	}
}
