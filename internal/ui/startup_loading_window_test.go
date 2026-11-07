package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// startupProgressLabel 在拿不到总量、进行中与完成时的文字。
func TestStartupProgressLabel(t *testing.T) {
	const mb = int64(1024 * 1024)
	for _, tc := range []struct {
		name  string
		done  int64
		total int64
		want  string
	}{
		{"总量未知", 0, 0, "请稍候…"},
		{"进行中", 270 * mb, 640 * mb, "已完成 42%（270 MB / 640 MB）"},
		{"完成", 640 * mb, 640 * mb, "已完成 100%（640 MB / 640 MB）"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := startupProgressLabel(tc.done, tc.total); got != tc.want {
				t.Fatalf("startupProgressLabel(%d, %d) = %q, 期望 %q", tc.done, tc.total, got, tc.want)
			}
		})
	}
}

// 加载窗口的内容里能看到主提示、副提示与进度文字。
func TestStartupLoadingWindowContent(t *testing.T) {
	tt := ui.NewTester(func(c *ui.Context) {
		renderStartupLoading(c, 0, 0)
	}, startupLoadingWindowWidth, startupLoadingWindowHeight)

	waitFor(t, tt, func() bool { return tt.HasText("正在整理数据库…") })
	for _, want := range []string{"仅首次升级时进行一次，请勿关闭", "请稍候…"} {
		if !tt.HasText(want) {
			t.Fatalf("加载窗口缺少文本 %q，当前文本: %q", want, tt.Texts())
		}
	}
}

// 有进度时窗口内容跟着更新。
func TestStartupLoadingWindowShowsProgress(t *testing.T) {
	const mb = int64(1024 * 1024)
	tt := ui.NewTester(func(c *ui.Context) {
		renderStartupLoading(c, 270*mb, 640*mb)
	}, startupLoadingWindowWidth, startupLoadingWindowHeight)

	waitFor(t, tt, func() bool { return tt.HasText("已完成 42%（270 MB / 640 MB）") })
}
