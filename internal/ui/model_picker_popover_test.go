package mcodeui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

// TestModelPickerDarkPanelTextColors 守卫深色模型面板的文字与图标配色。
func TestModelPickerDarkPanelTextColors(t *testing.T) {
	if modelPickerTextPrimary != ColorTextPrimaryDark || modelPickerTextPrimary == ColorTextPrimaryLight {
		t.Fatal("模型名称和选中图标应使用深色面主文字色")
	}
	if modelPickerTextSecondary != ColorTextSecondaryDark || modelPickerTextSecondary == ColorTextSecondaryLight {
		t.Fatal("模型副标题和分组标题应使用深色面次要文字色")
	}
	source, err := os.ReadFile("model_picker.go")
	if err != nil {
		t.Fatal(err)
	}
	// 触发胶囊位于面板外，允许继续随主题变化。
	parts := strings.SplitN(string(source), "// modelPickerPanelBg 是模型面板的底色", 2)
	if len(parts) != 2 {
		t.Fatal("找不到模型面板定义")
	}
	for _, call := range []string{"ThemePrimaryText(", "ThemeSecondaryText("} {
		if strings.Contains(parts[1], call) {
			t.Fatalf("深色模型面板不应调用 %s", call)
		}
	}
}

// clickWhenSettled 等待元素位置稳定后再点击。
func clickWhenSettled(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	waitFor(t, tt, func() bool {
		first, ok := tt.Find(label)
		if !ok {
			return false
		}
		tt.Frame()
		second, ok := tt.Find(label)
		return ok && second == first
	})
	if err := tt.Click(label); err != nil {
		t.Fatalf("点击 %q 失败: %v", label, err)
	}
}

func modelPickerPageTester(t *testing.T, fake *FakeClient, providers []ProviderInfo) *ui.Tester {
	t.Helper()
	fake.SetProviders(providers)
	tt, _ := modelsPageWithProvidersTester(t, fake, providers)
	return tt
}

func TestModelPicker_OnlyAddedModelsListed(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	pin := ModelPin{Provider: "claude", ModelID: "sonnet"}
	if _, err := fake.AddModelToList(context.Background(), pin); err != nil {
		t.Fatal(err)
	}
	providers := []ProviderInfo{{ID: "claude", Name: "Claude", Models: []ModelInfo{
		{ID: "sonnet", Name: "Sonnet"}, {ID: "haiku", Name: "Haiku"},
	}}}
	tt := modelPickerPageTester(t, fake, providers)
	waitFor(t, tt, func() bool {
		return tt.HasText("收藏模型") && tt.HasText("Sonnet") && tt.HasText("移除收藏 Claude Sonnet")
	})
	if tt.HasText("Haiku") {
		t.Fatalf("未加入的模型不应出现在页面上: %v", tt.Texts())
	}
	pins, _, err := fake.ModelList(context.Background())
	if err != nil || len(pins) != 1 || pins[0] != pin {
		t.Fatalf("收藏列表不正确: %+v, %v", pins, err)
	}
}

func TestModelPicker_RemoveButtonCallsClient(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	pin := ModelPin{Provider: "claude", ModelID: "sonnet"}
	if _, err := fake.AddModelToList(context.Background(), pin); err != nil {
		t.Fatal(err)
	}
	providers := []ProviderInfo{{ID: "claude", Name: "Claude", Models: []ModelInfo{{ID: "sonnet", Name: "Sonnet"}, {ID: "haiku", Name: "Haiku"}}}}
	tt := modelPickerPageTester(t, fake, providers)
	clickWhenSettled(t, tt, "移除收藏 Claude Sonnet")
	waitFor(t, tt, func() bool {
		pins, _, err := fake.ModelList(context.Background())
		return err == nil && len(pins) == 0 && tt.HasText("尚未选择模型") && !tt.HasText("Sonnet")
	})
}

func TestModelPicker_AddAndSearch(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	providers := []ProviderInfo{{ID: "claude", Name: "Claude", Models: []ModelInfo{{ID: "sonnet", Name: "Sonnet"}, {ID: "haiku", Name: "Haiku"}}}}
	tt := modelPickerPageTester(t, fake, providers)
	clickWhenSettled(t, tt, "添加收藏")
	waitFor(t, tt, func() bool { return tt.HasText("Sonnet") && tt.HasText("Haiku") })
	search := "input-provider-models-search-models-add-pin"
	if err := tt.Click(search); err != nil {
		t.Fatal(err)
	}
	tt.Type("sonnet")
	waitFor(t, tt, func() bool { return tt.HasText("Sonnet") && !tt.HasText("Haiku") })
	if err := tt.Click("Sonnet"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		pins, _, err := fake.ModelList(context.Background())
		return err == nil && len(pins) == 1 && pins[0].Key() == "claude/sonnet" && tt.HasText("移除收藏 Claude Sonnet")
	})
	if _, ok := tt.Find(search); !ok {
		t.Fatalf("勾选后浮层应保持打开: %v", tt.Texts())
	}
}

func TestModelPicker_Flyout(t *testing.T) {
	testenv.TempHome(t)
	entry, capture := setupPopoverFlyoutTest(t)
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	ps := newModelsPageState()
	items := make([]modelPickerItem, 30)
	for i := range items {
		name := fmt.Sprintf("Model %02d", i)
		items[i] = modelPickerItem{Key: name, Name: name, ID: name}
	}
	selected := false
	host := ui.NewTester(func(c *ui.Context) {
		entry.services = c.Services()
		anchor := renderModelPickerAddButton(c, st, &ps, "test-picker", "打开模型")
		renderModelPickerPopover(c, st, &ps, "test-picker", anchor, items, "空", func(_ modelPickerItem, checked bool) {
			selected = checked
			items[0].Checked = checked
			if !checked {
				ps.openPopoverKey = ""
			}
		}, modelPickerCustom{})
	}, 600, 300)
	if err := host.Click("打开模型"); err != nil {
		t.Fatal(err)
	}
	if len(capture.options) != 1 || capture.options[0].Width != 420 || !capture.options[0].Focusable || capture.options[0].Content == nil {
		t.Fatalf("浮层参数错误: %+v", capture.options)
	}
	menu := ui.NewTester(popoverTestView(t, entry.services, "popover-test-picker"), 420, 3000)
	first, ok := menu.Find("Model 00")
	if !ok {
		t.Fatalf("缺少模型: %v", menu.Texts())
	}
	last, ok := menu.Find("Model 29")
	if !ok || last.Y-first.Y <= 360 {
		t.Fatalf("模型列表未按自然高度展开: 首项 %+v，末项 %+v", first, last)
	}
	if err := menu.Click("input-provider-models-search-test-picker"); err != nil {
		t.Fatal(err)
	}
	menu.Type("Model 00")
	menu.Frame()
	if !menu.HasText("Model 00") || menu.HasText("Model 29") {
		t.Fatalf("搜索未过滤模型: %v", menu.Texts())
	}
	if err := menu.Click("Model 00"); err != nil {
		t.Fatal(err)
	}
	if !selected || ps.openPopoverKey != "test-picker" || len(capture.destroyedWindows) != 0 {
		t.Fatal("勾选模型后状态未更新或浮层未保持打开")
	}
	menu.Frame()
	if err := menu.Click("Model 00"); err != nil {
		t.Fatal(err)
	}
	if selected || ps.openPopoverKey != "test-picker" || len(capture.destroyedWindows) != 0 {
		t.Fatal("取消勾选模型后状态未更新或浮层未保持打开")
	}
}

func TestModelPicker_LimitDisablesUnchecked(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	for i := 0; i < 30; i++ {
		if _, err := fake.AddModelToList(context.Background(), ModelPin{Provider: "test", ModelID: fmt.Sprintf("model-%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	providers := []ProviderInfo{{ID: "claude", Name: "Claude", Models: []ModelInfo{{ID: "sonnet", Name: "Sonnet"}}}}
	tt := modelPickerPageTester(t, fake, providers)
	clickWhenSettled(t, tt, "添加收藏")
	waitFor(t, tt, func() bool {
		return tt.HasText("收藏模型已达上限（30 个），先移除一些再添加") && tt.HasText("Sonnet")
	})
	if err := tt.Click("Sonnet"); err != nil {
		t.Fatal(err)
	}
	if pins, _, err := fake.ModelList(context.Background()); err != nil || len(pins) != 30 {
		t.Fatalf("达到上限后添加改变了列表: %+v, %v", pins, err)
	}
}

func TestModelPicker_GlobalSearchStillListsModels(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	providers := []ProviderInfo{{ID: "claude", Name: "Claude", Models: []ModelInfo{{ID: "sonnet", Name: "Sonnet"}, {ID: "haiku", Name: "Haiku"}}}}
	tt := modelPickerPageTester(t, fake, providers)
	clickWhenSettled(t, tt, "添加收藏")
	search := "input-provider-models-search-models-add-pin"
	clickWhenSettled(t, tt, search)
	tt.Type("sonnet")
	waitFor(t, tt, func() bool { return tt.HasText("Sonnet") && !tt.HasText("Haiku") })
	if err := tt.Click("Sonnet"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		pins, _, err := fake.ModelList(context.Background())
		return err == nil && len(pins) == 1 && pins[0].Key() == "claude/sonnet" && tt.HasText("移除收藏 Claude Sonnet")
	})
}

func TestModelPicker_PopoverClosesOnEscapeAndOutsideClick(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	providers := []ProviderInfo{{ID: "claude", Name: "Claude", Models: []ModelInfo{{ID: "sonnet", Name: "Sonnet"}, {ID: "haiku", Name: "Haiku"}}}}
	tt := modelPickerPageTester(t, fake, providers)
	search := "input-provider-models-search-models-add-pin"
	openPicker := func() {
		clickWhenSettled(t, tt, "添加收藏")
		waitFor(t, tt, func() bool { _, ok := tt.Find(search); return ok && tt.HasText("Sonnet") })
	}
	closed := func() bool { _, ok := tt.Find(search); return !ok && !tt.HasText("Sonnet") }
	openPicker()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	waitFor(t, tt, closed)
	openPicker()
	if err := tt.Click(search); err != nil {
		t.Fatal(err)
	}
	tt.Type("so")
	waitFor(t, tt, func() bool { return tt.HasText("Sonnet") && !tt.HasText("Haiku") })
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	waitFor(t, tt, closed)
	openPicker()
	heading, ok := tt.Find("收藏模型")
	if !ok {
		t.Fatal("找不到收藏模型标题")
	}
	tt.ClickAt(heading.X+heading.W/2, heading.Y+heading.H/2)
	waitFor(t, tt, closed)
	openPicker()
	clickWhenSettled(t, tt, "添加收藏")
	waitFor(t, tt, closed)
}
