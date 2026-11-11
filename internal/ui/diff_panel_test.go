package mcodeui

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

// 嵌入必选接口，确保兼容测试没有可选 ReviewGet 能力。
type legacyDiffClient struct{ Client }

func TestDiffPanel(t *testing.T) {
	fake := NewFakeClient()
	fake.SetThreadDiff("t1", "", ThreadDiff{Diff: toolDiffTestUnified})
	tt := ui.NewTester(Root(legacyDiffClient{fake}), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	tt.Key(ui.Cmd, ui.KeyD)
	tt.Advance(DurSheet + 50*time.Millisecond)
	waitFor(t, tt, func() bool { return tt.HasText("pkg/math.go") && tt.HasText("return a - b") })
	clickText(t, tt, "pkg/math.go")
	if tt.HasText("return a - b") {
		t.Fatal("折叠后不应显示代码")
	}
	fake.SetThreadDiff("t1", "", ThreadDiff{FailureReason: "工作区无法获取 git 差异"})
	clickText(t, tt, "刷新")
	waitFor(t, tt, func() bool { return tt.HasText("工作区无法获取 git 差异") })
	fake.SetThreadDiff("t1", "", ThreadDiff{})
	clickText(t, tt, "刷新")
	waitFor(t, tt, func() bool { return tt.HasText(diffEmptyTitle) })
	tt.Key(ui.Cmd, ui.KeyD)
	waitFor(t, tt, func() bool { return !tt.HasText(diffEmptyTitle) })
	if len(fake.ReviewGetCalls()) != 0 {
		t.Fatal("旧客户端不应读取审阅接口")
	}
}

func TestDiffPanelViewState(t *testing.T) {
	for _, tc := range []struct {
		loading bool
		err     string
		n       int
		want    string
	}{
		{true, "", 0, "loading"}, {true, "", 2, "files"}, {false, "", 0, "empty"}, {false, "", 3, "files"},
		{false, "rpc timeout", 1, "error"}, {false, "fatal: not a git repository", 0, "not-git"}, {false, "当前目录不是 Git 仓库", 0, "not-git"}, {false, "非 Git 目录", 0, "not-git"},
	} {
		if got := diffPanelViewState(tc.loading, tc.err, tc.n); got != tc.want {
			t.Fatalf("%+v: %s", tc, got)
		}
	}
}

func TestDiffPanel_EmptyState(t *testing.T) {
	tt := ui.NewTester(func(c *ui.Context) { renderDiffEmptyState(c) }, 480, 500)
	if !tt.HasText("暂无改动") {
		t.Fatal("缺少空态标题")
	}
}

func TestDiffPanel_HeaderButton(t *testing.T) {
	fake := NewFakeClient()
	fake.SetThreadDiff("t1", "", ThreadDiff{Diff: diffTestPatch("hello.go", 1)})
	tt := ui.NewTester(Root(legacyDiffClient{fake}), 1200, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	tt.Key(ui.Cmd, ui.KeyD)
	tt.Advance(DurSheet + 50*time.Millisecond)
	waitFor(t, tt, func() bool { return tt.HasText("hello.go") })
	clickText(t, tt, "关闭右栏")
	waitFor(t, tt, func() bool { return !tt.HasText("hello.go") })
}

func diffTestPatch(path string, lines int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\nnew file mode 100644\n--- /dev/null\n+++ b/%s\n@@ -0,0 +1,%d @@\n", path, path, path, lines)
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&b, "+line %d\n", i)
	}
	return b.String()
}

func openDiffPanelWithPatch(t *testing.T, path, patch string) (*chatState, *ui.Tester, *diffPanelState) {
	t.Helper()
	fake := NewFakeClient()
	fake.SetThreadDiff("t1", "", ThreadDiff{Diff: patch})
	s, view := buildRoot(legacyDiffClient{fake})
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	tt.Key(ui.Cmd, ui.KeyD)
	tt.Advance(DurSheet + 50*time.Millisecond)
	dp := getDiffPanelState(s)
	waitFor(t, tt, func() bool { diffPanelMu.Lock(); defer diffPanelMu.Unlock(); return !dp.loading && len(dp.files) > 0 })
	waitFor(t, tt, func() bool { return tt.HasText(path) })
	return s, tt, dp
}

func TestDiffPanel_LargeFileCollapsed(t *testing.T) {
	_, tt, _ := openDiffPanelWithPatch(t, "big.go", diffTestPatch("big.go", diffLargeFileLineThreshold+100))
	if !tt.HasText("展开改动") || tt.HasText("line 0") {
		t.Fatal("大文件应延迟展示")
	}
	clickText(t, tt, "展开改动")
	waitFor(t, tt, func() bool { return tt.HasText("line 0") })
}

func TestDiffPanel_DeferredFileExpand(t *testing.T) {
	patch := diffTestPatch("deferred.go", 5100)
	files, err := git.ParseUnifiedDiff([]byte(patch))
	if err != nil || !files[0].IsDeferred {
		t.Fatal("测试数据应延迟解析")
	}
	_, tt, dp := openDiffPanelWithPatch(t, "deferred.go", patch)
	clickText(t, tt, "展开改动")
	waitFor(t, tt, func() bool {
		diffPanelMu.Lock()
		defer diffPanelMu.Unlock()
		return len(dp.expandedDeferred["deferred.go"].Hunks) > 0
	})
	waitFor(t, tt, func() bool { return tt.HasText("line 0") })
}

func setDiffTestWorktree(t *testing.T, s *chatState, tid, path string) {
	t.Helper()
	st := getAIWorktreeState(s)
	s.mu.Lock()
	rev := s.messagesRev
	s.mu.Unlock()
	st.mu.Lock()
	st.gens[tid]++
	st.found[tid] = aiWorktreeInfo{Path: path}
	st.revs[tid] = rev
	st.mu.Unlock()
}

func TestDiffWorktreeResolvedNoReload(t *testing.T) {
	s, tt, dp := openDiffPanelWithPatch(t, "wt.go", diffTestPatch("wt.go", 5))
	fake := s.client.(legacyDiffClient).Client.(*FakeClient)
	path := filepath.Join(t.TempDir(), "wt")
	before := len(fake.DiffCalls())
	setDiffTestWorktree(t, s, "t1", path)
	waitFor(t, tt, func() bool {
		diffPanelMu.Lock()
		defer diffPanelMu.Unlock()
		return !dp.loading && dp.worktreePath == path && dp.worktreeResolved
	})
	if len(fake.DiffCalls()) != before+1 {
		t.Fatal("首次确认应只刷新一次")
	}
	for i := 0; i < 20; i++ {
		value := path
		if i%2 == 0 {
			value = ""
		}
		setDiffTestWorktree(t, s, "t1", value)
		tt.Frame()
	}
	if len(fake.DiffCalls()) != before+1 {
		t.Fatal("空路径抖动不应触发刷新")
	}
}

func TestDiffWorktreePathChangeReloadsOnce(t *testing.T) {
	s, tt, dp := openDiffPanelWithPatch(t, "wt.go", diffTestPatch("wt.go", 5))
	fake := s.client.(legacyDiffClient).Client.(*FakeClient)
	for _, path := range []string{filepath.Join(t.TempDir(), "one"), filepath.Join(t.TempDir(), "two")} {
		before := len(fake.DiffCalls())
		setDiffTestWorktree(t, s, "t1", path)
		waitFor(t, tt, func() bool {
			diffPanelMu.Lock()
			defer diffPanelMu.Unlock()
			return !dp.loading && dp.worktreePath == path
		})
		for i := 0; i < 5; i++ {
			tt.Frame()
		}
		if len(fake.DiffCalls()) != before+1 {
			t.Fatal("路径变化应只刷新一次")
		}
	}
}
