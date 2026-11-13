package mcodeui

import (
	"bytes"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestMotionLoadingMigrationReduced(t *testing.T) {
	setAppReduceMotion(t, true)
	cases := []struct {
		name  string
		label string
		view  func(*ui.Context, bool)
	}{
		{"文件预览", "正在读取文件…", func(c *ui.Context, loading bool) {
			renderFileManagerFileBody(c, nil, fileManagerTabSnapshot{fileLoading: loading}, "test-")
		}},
		{"PR 列表", "正在读取 PR 列表…", func(c *ui.Context, loading bool) {
			renderGitPRListPane(c, nil, nil, 0, loading, "", 200)
		}},
		{"PR 详情", "正在读取 PR 详情…", func(c *ui.Context, loading bool) {
			renderGitPRDetailPane(c, nil, nil, 1, loading, "", 200)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loading := true
			tester := ui.NewTesterWith(func(c *ui.Context) {
				tc.view(c, loading)
			}, 500, 300, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
			tester.Frame()
			if _, ok := tester.Find(tc.label); !ok {
				t.Fatalf("找不到加载状态 %q", tc.label)
			}
			if tester.FramePending() {
				t.Fatal("减少动效时加载状态仍请求后续帧")
			}
			loading = false
			tester.Frame()
			if _, ok := tester.Find(tc.label); ok {
				t.Fatalf("加载结束后仍显示 %q", tc.label)
			}
		})
	}
}

func TestSettingsSearchFocusReduced(t *testing.T) {
	old := settingsRowFocus
	t.Cleanup(func() { settingsRowFocus = old })
	settingsRowFocus.jumpID = "target"
	settingsRowFocus.highlightID = "target"
	settingsRowFocus.dynamicJump = false
	jumps := 0
	settingsRowFocus.onJump = func(string) { jumps++ }
	options := ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}}
	actual := ui.NewTesterWith(func(c *ui.Context) {
		ui.Scroll(c).Fill().Children(func() {
			row := ui.Row(c.Key("target")).FillWidth().Height(ControlMD).Background(ThemeSelected(c))
			applySettingsRowFocus(c, row, "target").Children(func() { ui.Text(c, "目标") })
		})
	}, 300, 100, options)
	expected := ui.NewTesterWith(func(c *ui.Context) {
		ui.Scroll(c).Fill().Children(func() {
			ui.Row(c.Key("target")).FillWidth().Height(ControlMD).Background(ThemeSelected(c)).
				Border(settingsSearchPulseBorder, ThemeAccent(c).Alpha(0)).Radius(settingsSearchPulseRadius).
				Children(func() { ui.Text(c, "目标") })
		})
	}, 300, 100, options)
	actual.Frame()
	expected.Frame()
	if jumps != 1 || settingsRowFocus.jumpID != "" {
		t.Fatalf("跳转未完成：次数=%d，目标=%q", jumps, settingsRowFocus.jumpID)
	}
	if !bytes.Equal(actual.Image().Pix, expected.Image().Pix) {
		t.Fatal("减少动效时搜索高亮未停在透明末帧或改变了选中底色")
	}
	if actual.FramePending() {
		t.Fatal("减少动效时搜索高亮仍请求后续帧")
	}
}
