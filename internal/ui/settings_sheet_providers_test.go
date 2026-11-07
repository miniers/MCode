package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestProviderSheet_OpensOnClick(t *testing.T) {
	tt, st := openProvidersPageTest(t, NewFakeClient())
	clickProviderDetailTabForTest(t, tt, "claude", "")
	waitFor(t, tt, func() bool {
		sheet, _ := settingsTreeSheetState(st)
		_, id := currentProviderSheet(st)
		return sheet == "provider" && id == "claude" && tt.HasText("展开高级")
	})
	closeSettingsSheet(st)
	tt.Frame()
	if err := tt.Click("添加供应商"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		sheet, _ := settingsTreeSheetState(st)
		return sheet == "provider-add" && tt.HasText("CLI 智能体")
	})
}

func TestProviderSheet_ProvidersSingleCard(t *testing.T) {
	tt, _ := openProvidersPageTest(t, NewFakeClient())
	count := 0
	for _, text := range tt.Texts() {
		if text == "设置卡片 sec-providers" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("供应商卡片数量 = %d，期望 1", count)
	}
}

func Test供应商面板_Claude常用节顺序(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.providerRows.currentProvider = "claude"
	st.providerRows.currentProviderKind = "cli"
	tt := ui.NewTester(func(c *ui.Context) {
		renderProviderSheetBody(c, st)
	}, 1000, 4000)
	tt.Frame()
	previous := float32(-1)
	for _, title := range []string{"账号", "高级"} {
		r, ok := tt.Find(title)
		if !ok || r.Y <= previous {
			t.Fatalf("常用节与高级区 %q 顺序错误：%v", title, tt.Texts())
		}
		previous = r.Y
	}
	if tt.HasText("可执行文件路径") {
		t.Fatal("高级默认应收起")
	}
	clickWhenSettled(t, tt, "展开高级")
	waitFor(t, tt, func() bool { return tt.HasText("可执行文件路径") })
}

func TestProviderSheet_AgyAccounts(t *testing.T) {
	tt, st := openProvidersPageTest(t, NewFakeClient())
	clickProviderDetailTabForTest(t, tt, "agy", "")
	waitFor(t, tt, func() bool {
		sheet, _ := settingsTreeSheetState(st)
		_, id := currentProviderSheet(st)
		return sheet == "provider" && id == "agy" && tt.HasText("账号")
	})
}
