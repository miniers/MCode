package mcodeui

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
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
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetThreadDiff("t1", "", ThreadDiff{
		Diff:         changesTabDiff,
		FilesChanged: []string{"pkg/math.go"},
	})
	// 统计胶囊只取三个数字，走独立的 statOnly 通道。
	fake.SetThreadDiffStats("t1", ThreadDiffStats{Files: 1, Additions: 1, Deletions: 1})

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
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetThreadDiff("t1", "", ThreadDiff{
		Diff:         changesTabDiff,
		FilesChanged: []string{"pkg/math.go"},
	})
	// 统计胶囊只取三个数字，走独立的 statOnly 通道。
	fake.SetThreadDiffStats("t1", ThreadDiffStats{Files: 1, Additions: 1, Deletions: 1})

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

type blockingDiffStatsClient struct {
	*FakeClient
	calls  atomic.Int32
	onCall func(ctx context.Context, n int)
}

func (c *blockingDiffStatsClient) DiffStats(ctx context.Context, threadID string) (ThreadDiffStats, error) {
	n := int(c.calls.Add(1))
	if c.onCall != nil {
		c.onCall(ctx, n)
	}
	return c.FakeClient.DiffStats(ctx, threadID)
}

// TestThreadChangeStatsEvictionKeepsCurrent 验证缓存满时新建当前会话条目不会被自身淘汰，
// 加载落地后可命中缓存且不会循环重复拉取。
func TestThreadChangeStatsEvictionKeepsCurrent(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	s, _ := buildRoot(fake)
	t.Cleanup(s.close)
	t.Cleanup(func() { releaseThreadChangeStats(s) })

	waitStats := func(threadID string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, _, _, ok := threadChangeStatsFor(s, threadID); ok {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
		t.Fatalf("会话 %s 的改动统计未加载", threadID)
	}

	for i := 1; i <= kThreadChangeStatsCache; i++ {
		id := fmt.Sprintf("t-evict-%d", i)
		s.ensureThreadChangeStats(id, false)
		waitStats(id)
	}

	const keepID = "t-evict-51"
	fake.SetThreadDiffStats(keepID, ThreadDiffStats{Files: 2, Additions: 3, Deletions: 1})
	s.ensureThreadChangeStats(keepID, false)
	waitStats(keepID)
	if add, del, files, ok := threadChangeStatsFor(s, keepID); !ok || add != 3 || del != 1 || files != 2 {
		t.Fatalf("%s 统计 = %d/%d/%d ok=%v，期望 3/1/2 true", keepID, add, del, files, ok)
	}

	for i := 0; i < 5; i++ {
		s.ensureThreadChangeStats(keepID, false)
	}
	time.Sleep(50 * time.Millisecond)
	calls := 0
	for _, id := range fake.DiffStatsCalls() {
		if id == keepID {
			calls++
		}
	}
	if calls != 1 {
		t.Fatalf("%s DiffStats 调用次数 = %d，期望 1", keepID, calls)
	}
}

// TestThreadChangeStatsRunEndDuringLoad 验证在途加载期间运行结束（isRunning 由 true 变 false）
// 不会丢失刷新，待当前加载落地后再次调用 ensure 会触发第二次 DiffStats。
func TestThreadChangeStatsRunEndDuringLoad(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetThreadDiffStats("t1", ThreadDiffStats{Files: 1, Additions: 1, Deletions: 1})

	started1 := make(chan struct{})
	release1 := make(chan struct{})
	started2 := make(chan struct{})
	client := &blockingDiffStatsClient{
		FakeClient: fake,
		onCall: func(ctx context.Context, n int) {
			switch n {
			case 1:
				close(started1)
				select {
				case <-release1:
				case <-ctx.Done():
				}
			case 2:
				close(started2)
			}
		},
	}

	s, _ := buildRoot(client)
	t.Cleanup(s.close)
	t.Cleanup(func() { releaseThreadChangeStats(s) })

	s.ensureThreadChangeStats("t1", false)
	select {
	case <-started1:
	case <-time.After(5 * time.Second):
		t.Fatal("第一次 DiffStats 未按预期开始")
	}

	s.ensureThreadChangeStats("t1", true)
	s.ensureThreadChangeStats("t1", false)

	fake.SetThreadDiffStats("t1", ThreadDiffStats{Files: 2, Additions: 5, Deletions: 1})
	close(release1)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, _, ok := threadChangeStatsFor(s, "t1"); ok {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if _, _, _, ok := threadChangeStatsFor(s, "t1"); !ok {
		t.Fatal("第一次 DiffStats 放行后未写入缓存")
	}

	s.ensureThreadChangeStats("t1", false)
	select {
	case <-started2:
	case <-time.After(5 * time.Second):
		t.Fatal("运行结束后的第二次 DiffStats 未触发")
	}

	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if add, del, files, ok := threadChangeStatsFor(s, "t1"); ok && add == 5 && del == 1 && files == 2 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	add, del, files, ok := threadChangeStatsFor(s, "t1")
	t.Fatalf("第二次 DiffStats 后统计 = %d/%d/%d ok=%v，期望 5/1/2 true", add, del, files, ok)
}
