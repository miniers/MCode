package mcodeui

import (
	"runtime"
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

func TestSettingsSearchPreventSleepTargetsGeneral(t *testing.T) {
	hit := searchSettingsItem(t, "保持唤醒", "row-automation-prevent-sleep")
	if hit.Item.Page != "general" || hit.Item.Section != "启动与托盘" {
		t.Fatalf("保持唤醒定位到 %q/%q，期望 general/启动与托盘", hit.Item.Page, hit.Item.Section)
	}
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.focusSettingsItem(hit)
	if st.currentPage != "general" || st.jumpToItemID != "row-automation-prevent-sleep" {
		t.Fatalf("保持唤醒跳转到 %q/%q", st.currentPage, st.jumpToItemID)
	}
}

type generalAboutClient struct{ *fakeUpdateClient }

func (generalAboutClient) ExportDiagnosticsTo(string) error { return nil }

func TestSettingsGeneral_AboutFooter(t *testing.T) {
	resetAboutPageStateForTest()
	t.Cleanup(resetAboutPageStateForTest)
	client := &generalAboutClient{&fakeUpdateClient{FakeClient: NewFakeClient(), version: "9.8.7"}}
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(client))
	general := ui.NewTester(func(c *ui.Context) { renderPageGeneral(c, st) }, 1000, 3000)
	if general.HasText("版本号") || general.HasText("检查更新") {
		t.Fatal("通用页不应显示关于信息")
	}
	tt := ui.NewTester(func(c *ui.Context) { renderPageAbout(c, st) }, 1000, 3000)
	for _, text := range []string{"9.8.7", "数据目录", "导出诊断日志"} {
		if n := countText(tt, text); n != 1 {
			t.Fatalf("%q 出现 %d 次", text, n)
		}
	}
	if !tt.HasText("检查更新") || !tt.HasText("导出…") {
		t.Fatalf("关于操作不可达: %v", tt.Texts())
	}
}

func TestSettingsTrayPlatform(t *testing.T) {
	if got := trayIconTitle("windows"); got != "托盘图标" {
		t.Errorf("Windows 托盘标题: %q", got)
	}
	if got := trayIconTitle("darwin"); got != "菜单栏图标" {
		t.Errorf("macOS 托盘标题: %q", got)
	}
	for _, section := range pageGeneral.Sections {
		for _, row := range section.Rows {
			if row.ID == "row-tray-quota" {
				if got := settingVisible(nil, row); got != (runtime.GOOS == "darwin") {
					t.Errorf("额度开关可见性: %v", got)
				}
				return
			}
		}
	}
	t.Fatal("缺少额度开关")
}

func TestUIReview14_GeneralGlobalShortcutStackedLayout(t *testing.T) {
	var found bool
	for _, section := range pageGeneral.Sections {
		for _, row := range section.Rows {
			if row.ID != "row-global-shortcut" {
				continue
			}
			found = true
			cc, ok := row.Control.(customControl)
			if !ok || !cc.fullWidth {
				t.Fatalf("全局快捷键行应使用 customFullWidth 走堆叠布局，实际: %T %+v", row.Control, row.Control)
			}
		}
	}
	if !found {
		t.Fatal("缺少全局快捷键行声明")
	}

	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.open = true
	st.currentPage = "general"
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsWindow(c, st) }, 640, 1400)
	tt.Frame()

	titleRect, ok := tt.Find("全局快捷键")
	if !ok {
		t.Fatalf("找不到全局快捷键标题: %v", tt.Texts())
	}
	swRect, ok := tt.Find("切换全局快捷键")
	if !ok {
		t.Fatal("找不到切换全局快捷键开关")
	}
	inputRect, ok := tt.Find("全局快捷键输入")
	if !ok {
		t.Fatal("找不到全局快捷键输入框")
	}
	cardRect, ok := tt.Find("设置卡片 general.shortcuts")
	if !ok {
		t.Fatal("找不到快捷键设置卡片")
	}

	if swRect.Y <= titleRect.Y || inputRect.Y <= titleRect.Y {
		t.Fatalf("全局快捷键行应上下堆叠: title=%+v sw=%+v input=%+v", titleRect, swRect, inputRect)
	}
	wantInputW := settingsTextFieldWidth - 2*Space3 - 2
	if inputRect.W != wantInputW {
		t.Fatalf("全局快捷键输入框宽度 = %v，期望 %v", inputRect.W, wantInputW)
	}
	if inputRect.X+inputRect.W > cardRect.X+cardRect.W || inputRect.X+inputRect.W > 640 {
		t.Fatalf("640pt 窄窗下全局快捷键输入框溢出: input=%+v card=%+v", inputRect, cardRect)
	}
}
