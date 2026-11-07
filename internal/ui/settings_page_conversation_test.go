package mcodeui

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

func conversationSheetsTester(st *SettingsState) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) {
		renderPageConversation(c, st)
		if id, _ := settingsTreeSheetState(st); id != "" {
			if sheet, ok := settingsTreeSheetOf(id); ok {
				renderSettingsTreeSheet(c, st, sheet)
			}
		}
	}, 1000, 2400)
}

func openConversationSheet(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatalf("打开面板 %q 失败: %v", label, err)
	}
	tt.Frame()
}

func TestSettingsConversation_ArchiveAndQuickReplySheets(t *testing.T) {
	seedArchiveState(t, []Thread{{ID: "a", Title: "已归档示例"}}, nil)
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.cfg.QuickReplies = []config.QuickReply{{Label: "示例", Message: "内容"}}
	tt := conversationSheetsTester(st)
	openConversationSheet(t, tt, "快捷回复 1 个")
	if id, _ := settingsTreeSheetState(st); id != "quick-replies" {
		t.Fatalf("快捷回复面板 = %q", id)
	}
	if _, ok := tt.Find("编辑第 1 条标题"); !ok {
		t.Fatal("快捷回复面板缺少编辑字段")
	}
	if err := tt.Click("关闭快捷回复"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	openConversationSheet(t, tt, "已归档会话 1 个")
	if id, _ := settingsTreeSheetState(st); id != "archive" || !tt.HasText("已归档示例") {
		t.Fatalf("归档面板 = %q，内容 = %v", id, tt.Texts())
	}
}

func TestSettingsConversation_AutoDormantPreservesUnknownValue(t *testing.T) {
	resetArchivePageStateForTest()
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	days := 23
	st.cfg.Archive.SidebarAutoDormantDays = &days
	tt := conversationSheetsTester(st)
	if !tt.HasText(fmt.Sprintf("%d 天", days)) {
		t.Fatalf("未知天数未显示: %v", tt.Texts())
	}
	tt.Frame()
	if *st.cfg.Archive.SidebarAutoDormantDays != 23 {
		t.Fatal("未操作时未知天数被修改")
	}
	if err := tt.Click("23 天"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("关闭 (0)"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return st.cfg.Archive.AutoDormantDays() == 0 })
	settingsSaveWriters.Wait()
	cfg, err := config.Load(st.dataDir)
	if err != nil || cfg.Archive.SidebarAutoDormantDays == nil || *cfg.Archive.SidebarAutoDormantDays != 0 {
		t.Fatalf("关闭未持久化: %v", err)
	}
}

// TestSettingsPageConversation_Tree 验证「会话」页已经走声明式页面树：
// 五节标题都在、高级区两行直接可见、页面没有折叠入口、旧说明句已删、切换开关写回配置。
func TestSettingsPageConversation_Tree(t *testing.T) {
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, newChatState(NewFakeClient()))
	tt := ui.NewTester(func(c *ui.Context) { renderPageConversation(c, st) }, 1000, 3000)

	texts := make(map[string]bool)
	for _, text := range tt.Texts() {
		texts[text] = true
	}
	for _, want := range []string{
		"回复与思考", "消息与排队", "完成与提醒", "保留与归档",
		"显示思考过程", "自动折叠思考过程", "简洁 AI 回复",
		"运行中排队消息", "新消息插入当前回合", "发送消息快捷键", "发送后置顶消息", "用尽限额后自动继续",
		"会话完成动作", "删除会话前确认", "快捷回复", "已归档会话", "无消息多少天后自动休眠",
		"高级", "恢复旧会话前询问压缩", "压缩 30 天前的子代理消息",
		// 多选项改成带标签的分段：两个快捷键与两种完成动作都直接显示。
		"Enter", "Shift+Enter", "休眠", "归档",
	} {
		if !texts[want] {
			t.Fatalf("会话页缺少 %q: %v", want, tt.Texts())
		}
	}

	page, ok := settingsTreePageOf("conversation")
	if !ok || len(page.Sections) == 0 || page.Sections[len(page.Sections)-1].ID != "advanced.conversation" {
		t.Fatal("会话页末尾应为高级设置")
	}
	if tt.HasText("高级（3 项）") {
		t.Fatalf("会话页不该再有高级折叠区: %v", tt.Texts())
	}

	// 说明句已经删除，布局自己解释；四节各抽查一条旧 desc。
	for _, desc := range []string{
		"在步骤中展开显示 Agent 的思考过程",
		"思考结束后自动收起，关闭则保持展开",
		"选择发送消息的按键，另一个按键用于换行",
		"一轮结束后是否自动归档该会话",
		"删除会话前弹窗确认一次",
		"子代理会话结束 30 天后，工具输出无损压缩，思考内容会被删除且不可恢复；主会话不受影响",
	} {
		if tt.HasText(desc) {
			t.Fatalf("会话页不该再有说明句 %q: %v", desc, tt.Texts())
		}
	}

	// 点开关后配置立即翻转，并落盘。
	if err := tt.Click("切换显示思考过程"); err != nil {
		t.Fatalf("click reasoning switch failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return st.cfg.Session.ShowReasoning != nil && !st.cfg.Session.ShowReasoningEnabled()
	})
	settingsSaveWriters.Wait()

	saved, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if saved.Session.ShowReasoningEnabled() {
		t.Fatalf("开关关闭后未写入 config.json: %+v", saved.Session)
	}
}
