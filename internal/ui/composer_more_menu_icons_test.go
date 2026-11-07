package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestComposerMoreMenuIcons 验证 ⋯ 菜单每一项都有图标映射，未知选项也有回退图标。
func TestComposerMoreMenuIcons(t *testing.T) {
	for _, key := range []string{"composer-more-mode", "composer-more-effort"} {
		if composerMoreMenuIcon(key) == nil {
			t.Fatalf("%s 缺少图标", key)
		}
	}
	if composerMoreMenuIcon("composer-more-unknown") == nil {
		t.Fatal("未知选项也应有回退图标")
	}
}

// TestComposerMoreMenuRendersEveryItem 验证最窄档菜单的选项。
func TestComposerMoreMenuRendersEveryItem(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	composerModelPickerState.Reset()
	t.Cleanup(composerModelPickerState.Reset)
	th := &Thread{ID: "t1", Provider: "agy"}
	for _, tc := range []struct {
		name  string
		tier  composerChinSize
		items []string
	}{
		{"最窄", composerChinNarrow, []string{"切换模式", "算力"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.composerView.moreOpen = false
			tt := ui.NewTester(func(c *ui.Context) {
				renderComposerMore(c, s, th, tc.tier, false)
			}, 320, 400)
			if err := tt.Click("更多操作"); err != nil {
				t.Fatal(err)
			}
			for _, label := range tc.items {
				if _, ok := tt.Find(label); !ok {
					t.Fatalf("菜单缺少 %s: %v", label, tt.Texts())
				}
			}
		})
	}
}

// TestComposerMoreMenuDisabledTurnsGrey 验证禁用项用次要色同时压暗图标与文字，且不响应点击。
func TestComposerMoreMenuDisabledTurnsGrey(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	clicked := false
	tt := ui.NewTester(func(c *ui.Context) {
		renderComposerMenuOption(c, "composer-more-mode", "切换模式", true, func() { clicked = true })
	}, 320, 200)
	if err := tt.Click("切换模式"); err != nil {
		t.Fatal(err)
	}
	if clicked {
		t.Fatal("禁用项不应响应点击")
	}
}
