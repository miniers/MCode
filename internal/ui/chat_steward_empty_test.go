package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 空的总管会话不说「要构建什么？」：标题用名字打招呼，副标题说清它能干什么。
// 普通会话保持原来的构建标语。
func TestStewardEmptyThreadPrompt(t *testing.T) {
	const stewardMainThreadID = "steward-main"
	now := time.Now()
	fake := newStewardFakeClient(stewardMainThreadID)
	fake.SetProjectsAndThreads(
		[]Project{
			{ID: "p-home", Name: "总管的家", Path: "/path/home"},
			{ID: "p1", Name: "普通项目", Path: "/path/p1"},
		},
		map[string][]Thread{
			// 总管会话更新更近，启动时默认选中它。
			"p-home": {{ID: stewardMainThreadID, ProjectID: "p-home", Title: "总管会话", UpdatedAt: now}},
			"p1":     {{ID: "t-plain", ProjectID: "p1", Title: "普通会话", UpdatedAt: now.Add(-time.Hour)}},
		},
	)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)

	waitFor(t, tt, func() bool {
		return selectedThreadForTest(state) == stewardMainThreadID && tt.HasText("在这儿")
	})
	if !tt.HasText("总管在这儿") {
		t.Fatalf("空的总管会话应显示「总管在这儿」，当前文本: %q", tt.Texts())
	}
	if !tt.HasText("我能读代码、改代码、跑测试") || !tt.HasText("定时任务") {
		t.Fatalf("空的总管会话应说明能做什么、包括定时任务，当前文本: %q", tt.Texts())
	}
	if tt.HasText("构建什么") {
		t.Fatalf("总管会话不应再显示「要构建什么？」，当前文本: %q", tt.Texts())
	}

	if err := tt.Click("普通会话"); err != nil {
		t.Fatalf("点击普通会话失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return selectedThreadForTest(state) == "t-plain" && tt.HasText("构建什么")
	})
	if tt.HasText("在这儿") {
		t.Fatalf("普通会话不应显示总管空态，当前文本: %q", tt.Texts())
	}
}
