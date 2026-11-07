package mcodeui

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
)

func Test供应商面板_正文高度贴合且不显示空底栏(t *testing.T) {
	modalDragReset("settings-sheet-provider")
	t.Cleanup(func() { modalDragReset("settings-sheet-provider") })
	tt, st := openProvidersPageTest(t, NewFakeClient())
	t.Cleanup(func() { closeSettingsSheet(st) })
	clickProviderDetailTabForTest(t, tt, "claude", "")
	waitFor(t, tt, func() bool {
		sheet, _ := settingsTreeSheetState(st)
		kind, id := currentProviderSheet(st)
		return sheet == "provider" && kind == "cli" && id == "claude" && tt.HasText("展开高级")
	})
	for i := 0; i < 5; i++ {
		tt.Frame()
	}
	card, ok := tt.Find("设置面板 供应商")
	if !ok || card.H < 240 || card.H > 640 {
		t.Fatalf("正文应贴合内容且不超过面板上限：card=%+v，found=%v", card, ok)
	}
	before := card.H
	clickWhenSettled(t, tt, "展开高级")
	waitFor(t, tt, func() bool {
		after, found := tt.Find("设置面板 供应商")
		return found && after.H > before && after.H <= 640
	})
	if _, ok := tt.Find("面板操作栏 供应商"); ok {
		t.Fatal("CLI 供应商面板不应显示空底栏")
	}
}

func Test供应商面板_预设供应商显示底栏(t *testing.T) {
	fake := NewFakeClient()
	fake.providerSettings = []ProviderSetting{{ID: "deepseek", Name: "DeepSeek", Kind: "api", BaseURL: "https://api.deepseek.com/v1", DefaultModel: "deepseek-chat"}}
	tt, st := openProvidersPageTest(t, fake)
	t.Cleanup(func() { closeSettingsSheet(st) })
	openProviderSheet(st, "preset", "deepseek")
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("面板操作栏 供应商")
		return ok && tt.HasText("账号")
	})
}

func Test供应商面板_接口供应商保留操作栏(t *testing.T) {
	for _, tc := range []struct{ name, kind string }{{"预设供应商", "preset"}, {"自定义供应商", "custom"}} {
		t.Run(tc.name, func(t *testing.T) {
			kind := tc.kind
			modalDragReset("settings-sheet-provider")
			st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
			st.presets.loaded = true
			st.presets.items = []ProviderSetting{{ID: "deepseek", Name: "DeepSeek", Kind: "api", DefaultModel: "deepseek-chat"}}
			st.endpoints.loaded = true
			st.endpoints.endpoints = []CustomEndpoint{{ID: "endpoint", Name: "测试端点", BaseURL: "https://example.com", Model: "model"}}
			id := "deepseek"
			if kind == "custom" {
				id = "endpoint"
			}
			openProviderSheet(st, kind, id)
			t.Cleanup(func() { closeSettingsSheet(st) })
			sh, ok := settingsTreeSheetOf("provider")
			if !ok {
				t.Fatal("缺少供应商面板")
			}
			tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreeSheet(c, st, sh) }, 1200, 1000)
			waitFor(t, tt, func() bool {
				_, ok := tt.Find("面板操作栏 供应商")
				return ok && tt.HasText("保存")
			})
		})
	}
}

func Test设置面板_长内容封顶且正文可滚动(t *testing.T) {
	const id = "fit-long-fixture"
	const key = "settings-sheet-" + id
	modalDragReset(key)
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	openSettingsSheet(st, id, "")
	t.Cleanup(func() { closeSettingsSheet(st) })
	var viewport, content ui.Rect
	sh := settingsSheet{
		ID:             id,
		Title:          "长内容",
		FitContent:     true,
		BodyOwnsScroll: true,
		Footer: func(c *ui.Context, st *SettingsState) {
			ui.Text(c, "固定操作")
		},
		Body: func(c *ui.Context, st *SettingsState) {
			scroll := ui.Scroll(c.Key("fit-long-scroll")).Grow(1).Shrink(1).FillWidth().Clip()
			viewport = scroll.Bounds()
			scroll.Children(func() {
				column := ui.Column(c.Key("fit-long-content")).FillWidth()
				content = column.Bounds()
				column.Children(func() {
					for i := 0; i < 60; i++ {
						ui.Text(c, fmt.Sprintf("长正文第%d行", i+1)).Height(32).Shrink(0)
					}
				})
				reportSettingsSheetContentHeight(id, content.H)
			})
		},
	}
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreeSheet(c, st, sh) }, 1200, 1000)
	for i := 0; i < 5; i++ {
		tt.Frame()
	}
	card, ok := tt.Find("设置面板 长内容")
	if !ok || card.H <= 0 || card.H > 640 {
		t.Fatalf("长内容高度应封顶：card=%+v，found=%v", card, ok)
	}
	if viewport.H <= 0 || viewport.H >= content.H {
		t.Fatalf("正文应形成滚动区：viewport=%+v，content=%+v", viewport, content)
	}
	footer, ok := tt.Find("面板操作栏 长内容")
	if !ok || footer.Y+footer.H > card.Y+card.H+1 {
		t.Fatalf("操作栏应留在卡片内：footer=%+v，card=%+v，found=%v", footer, card, ok)
	}
	before := content.Y
	tt.Scroll(viewport.X+viewport.W/2, viewport.Y+viewport.H/2, 0, 100000)
	tt.Frame()
	tt.Frame()
	if content.Y >= before {
		t.Fatalf("正文未向上滚动：之前=%v，之后=%v", before, content.Y)
	}
}

func Test供应商面板_用户高度优先于内容高度(t *testing.T) {
	tt, st := openProvidersPageTest(t, NewFakeClient())
	t.Cleanup(func() { closeSettingsSheet(st) })
	t.Cleanup(func() { modalDragReset("settings-sheet-provider") })
	clickProviderDetailTabForTest(t, tt, "claude", "")
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("设置面板 供应商")
		return ok && tt.HasText("展开高级")
	})
	modalDragMu.Lock()
	modalDragOffsets["settings-sheet-provider"] = &modalDragOffset{rh: 600}
	modalDragMu.Unlock()
	for i := 0; i < 5; i++ {
		tt.Frame()
	}
	card, ok := tt.Find("设置面板 供应商")
	if !ok {
		t.Fatal("未找到供应商面板")
	}
	if card.H != 600 {
		t.Fatalf("用户调整后的高度 = %v，期望 600", card.H)
	}
}

func Test设置面板_用户高度优先且其他面板保持固定高度(t *testing.T) {
	for _, tc := range []struct {
		name         string
		fit          bool
		userHeight   float32
		windowHeight int
		want         float32
	}{
		{"用户高度", true, 700, 1000, 700},
		{"用户高度受窗口限制", true, 700, 600, 600 - modalDragPanelInset},
		{"普通面板仍为固定高度", false, 0, 1000, 640},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const id = "fit-size-fixture"
			const key = "settings-sheet-" + id
			modalDragReset(key)
			modalDragMu.Lock()
			modalDragOffsets[key] = &modalDragOffset{rh: tc.userHeight}
			modalDragMu.Unlock()
			st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
			openSettingsSheet(st, id, "")
			t.Cleanup(func() { closeSettingsSheet(st) })
			sh := settingsSheet{
				ID:         id,
				Title:      "尺寸测试",
				FitContent: tc.fit,
				Body: func(c *ui.Context, st *SettingsState) {
					ui.Text(c, "短正文")
				},
			}
			tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreeSheet(c, st, sh) }, 1200, tc.windowHeight)
			tt.Frame()
			card, ok := tt.Find("设置面板 尺寸测试")
			if !ok || card.H != tc.want {
				t.Fatalf("固定高度未保留：card=%+v，found=%v，期望高度=%v", card, ok, tc.want)
			}
		})
	}
}
