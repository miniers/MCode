package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

func TestSettingsAdvancedPage_Sections(t *testing.T) {
	if _, ok := settingsTreePageOf("advanced"); ok {
		t.Fatal("旧高级页不应再注册")
	}
	for pageID, want := range map[string]struct{ id, title string }{
		"conversation": {"advanced.conversation", "高级"},
		"swarm":        {"advanced.orchestration", "高级"},
		"workbench":    {"advanced.workbench", "高级"},
		"extensions":   {"advanced.memory", "记忆高级"},
	} {
		page, ok := settingsTreePageOf(pageID)
		if !ok || len(page.Sections) == 0 {
			t.Fatalf("页面 %q 未注册或没有分节", pageID)
		}
		section := page.Sections[len(page.Sections)-1]
		if section.ID != want.id || section.Title != want.title {
			t.Errorf("页面 %q 最后分节 = %q %q", pageID, section.ID, section.Title)
		}
	}
}

func TestSettingsAdvancedPage_TogglePersists(t *testing.T) {
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, newChatState(NewFakeClient()))
	tt := ui.NewTester(func(c *ui.Context) { renderPageNowledge(c, st) }, 1000, 4000)
	if err := tt.Click("切换向新会话说明记忆工具用法"); err != nil {
		t.Fatalf("切换记忆工具用法失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return st.cfg.Memory.InjectMemoryToolsNote != nil && !*st.cfg.Memory.InjectMemoryToolsNote
	})
	settingsSaveWriters.Wait()
	saved, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("读取已保存的配置失败: %v", err)
	}
	if saved.Memory.InjectMemoryToolsNote == nil || *saved.Memory.InjectMemoryToolsNote {
		t.Fatalf("关闭开关后未写入配置: %+v", saved.Memory)
	}
}
