package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// changesTabDiff 是改动统计 Tab 测试用的统一 diff：1 个文件、+1/-1。
const changesTabDiff = `diff --git a/pkg/math.go b/pkg/math.go
--- a/pkg/math.go
+++ b/pkg/math.go
@@ -1,3 +1,3 @@
 func Add(a, b int) int {
-	return a - b
+	return a + b
 }
`

// TestComposerChangesTab_ShowsStatsAndOpensDiffPanel 测试无排队消息时输入框上方显示改动统计 Tab，
// 点击后打开改动面板。
func TestComposerChangesTab_ShowsStatsAndOpensDiffPanel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fake.SetThreadDiff("t1", "", ThreadDiff{
		Diff:         changesTabDiff,
		FilesChanged: []string{"pkg/math.go"},
	})

	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	// 后台统计加载完成后显示 "1 个文件"、"+1"、"-1"
	waitFor(t, tt, func() bool {
		return tt.HasText("1 个文件") && tt.HasText("+1") && tt.HasText("-1")
	})

	// 点击统计 Tab 打开改动面板，面板展示对应文件
	if err := tt.Click("1 个文件"); err != nil {
		t.Fatalf("click 1 个文件 tab failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		dp := getDiffPanelState(s)
		return dp != nil && dp.IsOpen() && tt.HasText("pkg/math.go")
	})
}

// TestComposerChangesTab_QueueTakesPrecedence 测试有排队消息时优先显示排队 Tab，
// 改动统计 Tab 让位。
func TestComposerChangesTab_QueueTakesPrecedence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fake.SetThreadDiff("t1", "", ThreadDiff{
		Diff:         changesTabDiff,
		FilesChanged: []string{"pkg/math.go"},
	})

	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})
	waitFor(t, tt, func() bool {
		return tt.HasText("1 个文件")
	})

	fake.setQueue("t1", []QueuedFollowUp{{ID: "q1", Text: "排队的一条"}})

	waitFor(t, tt, func() bool {
		return tt.HasText("排队 1") && !tt.HasText("1 个文件")
	})
}
