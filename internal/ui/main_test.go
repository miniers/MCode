package mcodeui

import (
	"os"
	"testing"

	"github.com/egoist/mygo"
)

func TestMain(m *testing.M) {
	applyThemeSource = func(mygo.ThemeSource) {}
	// 休眠提示音在测试里静音，避免测试进程打开音频设备。
	playDormantChimeFn = func() {}

	// 限频 Stat ui-state.json 会推迟发现外部改动；测试需要写文件后立刻生效，故把间隔置 0。
	lastSelectionStatInterval = 0
	// 自建 worktree 重新识别也有限频；测试需要每次 messagesRev 变化都立即重新识别，故置 0。
	aiWorktreeRedetectInterval = 0

	// 配置会从数据目录读取，开启界面状态持久化的测试会读写 ui-state.json；
	// 为避免测试受开发者本机数据（~/.mcode）影响，未显式指定时隔离到临时目录。
	cleanup := func() {}
	if os.Getenv("MCODE_DATA") == "" {
		if dir, err := os.MkdirTemp("", "mcode-ui-test"); err == nil {
			_ = os.Setenv("MCODE_DATA", dir)
			cleanup = func() { _ = os.RemoveAll(dir) }
		}
	}

	// 测试没有 mygo 主循环，RunOnMain 会阻塞。
	settingsWindowOpener = openSettingsWindowHeadless
	code := m.Run()
	// 测试期间可能还有后台写 ui-state.json 的 goroutine 未结束，先等它们写完再删临时数据目录。
	waitUIStateWrites()
	cleanup()
	os.Exit(code)
}

// settingsTestDir 返回测试用数据目录：删除目录前先等设置页后台写缓存的 goroutine 结束，
// 否则它们可能在 RemoveAll 期间写入文件，导致 "directory not empty" 偶发失败。
func settingsTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// t.Cleanup 后进先出：此处注册的等待先于 TempDir 的删除执行。
	t.Cleanup(func() {
		settingsSaveWriters.Wait()
		providerStatusCacheWriters.Wait()
		waitUIStateWrites()
	})
	return dir
}
