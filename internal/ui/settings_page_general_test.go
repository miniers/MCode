package mcodeui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestSettingsPageGeneral_Tree 验证「通用」页已经走声明式页面树：
// 四节标题都在、说明句全部消失、锁定档位显示徽标、「全部快捷键」能打开面板。
func TestSettingsPageGeneral_Tree(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), map[string]bool{"mode": true}, newChatState(NewFakeClient()))

	// 页面与面板是两个独立渲染入口：面板由外壳叠在页面之上，测试里自己补一遍。
	tt := ui.NewTester(func(c *ui.Context) {
		renderPageGeneral(c, st)
		if openSheet, _ := settingsTreeSheetState(st); openSheet != "" {
			if sheet, ok := settingsTreeSheetOf(openSheet); ok {
				renderSettingsTreeSheet(c, st, sheet)
			}
		}
	}, 1000, 2400)

	// 节标题与行标题都是独立文案，用精确匹配确认页面结构。
	texts := make(map[string]bool)
	for _, text := range tt.Texts() {
		texts[text] = true
	}
	for _, want := range []string{"权限", "通知与提示音", "启动与托盘", "快捷键", "默认权限", "全部快捷键", "锁定"} {
		if !texts[want] {
			t.Fatalf("通用页缺少 %q: %v", want, tt.Texts())
		}
	}

	// 说明句已全部删除，布局自己解释；原来的检索词移到了行别名里。
	for _, desc := range []string{
		"会话轮次结束时发送系统通知",
		"在菜单栏显示 MCode 图标与运行中会话数，重启后生效",
		"在任意应用中唤起窗口，重启后生效",
	} {
		if tt.HasText(desc) {
			t.Fatalf("通用页不该再有说明句 %q", desc)
		}
	}

	// 「全部快捷键」显示的是数量，点它打开快捷键面板。
	countLabel := ""
	for _, text := range tt.Texts() {
		if strings.HasSuffix(text, " 个") {
			countLabel = text
			break
		}
	}
	if countLabel == "" {
		t.Fatalf("找不到快捷键数量: %v", tt.Texts())
	}
	if err := tt.Click(countLabel); err != nil {
		t.Fatalf("click %q failed: %v", countLabel, err)
	}
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		open := st.openSheet
		st.mu.Unlock()
		// 面板里才有分组里的快捷键行与「全部恢复默认」，页面只剩入口那两行。
		return open == "shortcuts" && tt.HasText("新建会话") && tt.HasText("全部恢复默认")
	})
}

type generalAboutClient struct{ *fakeUpdateClient }

func (generalAboutClient) ExportDiagnosticsTo(string) error { return nil }

func TestSettingsGeneral_AboutFooter(t *testing.T) {
	resetAboutPageStateForTest()
	t.Cleanup(resetAboutPageStateForTest)
	client := &generalAboutClient{&fakeUpdateClient{FakeClient: NewFakeClient(), version: "9.8.7"}}
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(client))
	tt := ui.NewTester(func(c *ui.Context) { renderPageGeneral(c, st) }, 1000, 3000)
	for _, text := range []string{"9.8.7", "数据目录", "导出诊断日志"} {
		if n := countText(tt, text); n != 1 {
			t.Fatalf("%q 出现 %d 次", text, n)
		}
	}
	if !tt.HasText("检查更新") || !tt.HasText("导出…") {
		t.Fatalf("关于操作不可达: %v", tt.Texts())
	}
}
