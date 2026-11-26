package mcodeui

import (
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func mustFindRect(t *testing.T, tt *ui.Tester, label string) ui.Rect {
	t.Helper()
	r, ok := tt.Find(label)
	if !ok {
		t.Fatalf("找不到元素 %q，当前文本 %v", label, tt.Texts())
	}
	return r
}

func TestUnfoldMotionFileTreeExpandAnimatesHeight(t *testing.T) {
	setAppReduceMotion(t, false)
	fake := treeTestBase()
	client := newTreeListRecorder(fake, fileManagerTestTree())
	_, tt := openFileTreeForTest(t, client)
	waitFor(t, tt, func() bool { return tt.HasText("sub") && tt.HasText("a.txt") })

	// 先完成一次懒加载并收起，让后续展开同步挂载子行，避免异步回包干扰半程时刻采样。
	if err := tt.Click("sub"); err != nil {
		t.Fatalf("expand sub failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("b.go") })
	tt.Advance(motionUnfold + time.Second)
	if err := tt.Click("sub"); err != nil {
		t.Fatalf("collapse sub failed: %v", err)
	}
	waitFor(t, tt, func() bool { return !tt.HasText("b.go") })
	tt.Advance(motionUnfold + time.Second)

	collapsed := mustFindRect(t, tt, "a.txt").Y
	if err := tt.Click("sub"); err != nil {
		t.Fatalf("re-expand sub failed: %v", err)
	}
	start := mustFindRect(t, tt, "a.txt").Y

	tt.Advance(motionUnfold / 2)
	middle := mustFindRect(t, tt, "a.txt").Y

	tt.Advance(motionUnfold)
	done := mustFindRect(t, tt, "a.txt").Y

	tt.Advance(time.Second)
	settled := mustFindRect(t, tt, "a.txt").Y

	if math.Abs(float64(start-collapsed)) > 0.5 {
		t.Fatalf("首帧下方行位置应接近收起态：start=%v collapsed=%v", start, collapsed)
	}
	if !(middle > start && middle < done) {
		t.Fatalf("展开半程下方行 Y 应介于首末之间：start=%v middle=%v done=%v", start, middle, done)
	}
	if math.Abs(float64(done-settled)) > 0.5 {
		t.Fatalf("动画结束后下方行 Y 应等于最终位置：done=%v settled=%v", done, settled)
	}
	if tt.FramePending() {
		t.Fatal("动画结束后不应继续请求下一帧")
	}
}

func TestUnfoldMotionFileTreeWideNavToggle(t *testing.T) {
	setAppReduceMotion(t, false)
	fake := treeTestBase()
	fake.SetFileContents(map[string]FileContent{
		"a.txt":    {Path: "a.txt", Kind: "text", Text: "hello", TotalSize: 5},
		"sub/b.go": {Path: "sub/b.go", Kind: "text", Text: "world", TotalSize: 5},
	})
	s, tt := openFileTreeForTest(t, fake)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })

	inspectorSession(s).Width = 800
	s.invalidate()
	tt.Advance(DurSheet + 50*time.Millisecond)
	if err := tt.Click("a.txt"); err != nil {
		t.Fatalf("click a.txt failed: %v", err)
	}
	waitForFileManagerText(t, tt, s, "a.txt", "hello")
	tt.Advance(motionUnfold + time.Second)

	if err := tt.Click("展开文件导航"); err != nil {
		t.Fatalf("click expand nav failed: %v", err)
	}
	openStart := mustFindRect(t, tt, "文件导航").W

	tt.Advance(DurBase / 2)
	openMiddle := mustFindRect(t, tt, "文件导航").W

	tt.Advance(DurBase + time.Second)
	openDone := mustFindRect(t, tt, "文件导航").W

	if math.Abs(float64(openStart)) > 0.5 {
		t.Fatalf("展开宽栏导航首帧宽度应接近 0：openStart=%v", openStart)
	}
	if !(openMiddle > 0 && openMiddle < openDone) {
		t.Fatalf("展开宽栏导航半程宽度应介于 0 与最终值之间：middle=%v done=%v", openMiddle, openDone)
	}
	if math.Abs(float64(openDone-fileTreeNavWidth)) > 1 {
		t.Fatalf("展开宽栏导航最终宽度应等于 %v：done=%v", fileTreeNavWidth, openDone)
	}
	if tt.FramePending() {
		t.Fatal("宽栏导航展开结束后不应继续请求下一帧")
	}

	if err := tt.Click("收起文件导航"); err != nil {
		t.Fatalf("click collapse nav failed: %v", err)
	}
	if !tt.FramePending() {
		t.Fatal("收起宽栏导航首帧应启动退场动画")
	}

	tt.Advance(DurSnap / 2)
	if !tt.FramePending() {
		t.Fatal("收起宽栏导航半程应仍在动画中")
	}

	tt.Advance(DurSnap + time.Second)
	if _, ok := tt.Find("文件导航"); ok {
		t.Fatal("收起宽栏导航结束后导航列应卸载")
	}
	if tt.FramePending() {
		t.Fatal("宽栏导航收起结束后不应继续请求下一帧")
	}
}

func TestUnfoldMotionFileTreeReducedMotion(t *testing.T) {
	setAppReduceMotion(t, true)
	fake := treeTestBase()
	client := newTreeListRecorder(fake, fileManagerTestTree())
	s, tt := openFileTreeForTest(t, client)
	waitFor(t, tt, func() bool { return tt.HasText("sub") && tt.HasText("a.txt") })

	if err := tt.Click("sub"); err != nil {
		t.Fatalf("expand sub failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("b.go") })
	tt.Advance(motionUnfold + time.Second)
	if err := tt.Click("sub"); err != nil {
		t.Fatalf("collapse sub failed: %v", err)
	}
	waitFor(t, tt, func() bool { return !tt.HasText("b.go") })
	tt.Advance(motionUnfold + time.Second)

	collapsed := mustFindRect(t, tt, "a.txt").Y
	if err := tt.Click("sub"); err != nil {
		t.Fatalf("re-expand sub failed: %v", err)
	}
	start := mustFindRect(t, tt, "a.txt").Y
	if tt.FramePending() {
		t.Fatal("减少动效开启时展开目录首帧后不应有待画帧")
	}
	tt.Advance(motionUnfold + time.Second)
	done := mustFindRect(t, tt, "a.txt").Y
	if start <= collapsed || math.Abs(float64(start-done)) > 0.5 {
		t.Fatalf("减少动效展开后首帧应即为最终状态：collapsed=%v start=%v done=%v", collapsed, start, done)
	}

	inspectorSession(s).Width = 800
	s.invalidate()
	tt.Frame()
	if err := tt.Click("a.txt"); err != nil {
		t.Fatalf("click a.txt failed: %v", err)
	}
	waitForFileManagerText(t, tt, s, "a.txt", "hello")
	if err := tt.Click("展开文件导航"); err != nil {
		t.Fatalf("click expand nav failed: %v", err)
	}
	openStart := mustFindRect(t, tt, "文件导航").W
	if tt.FramePending() {
		t.Fatal("减少动效开启时展开宽栏导航首帧后不应有待画帧")
	}
	if math.Abs(float64(openStart-fileTreeNavWidth)) > 1 {
		t.Fatalf("减少动效展开宽栏导航首帧应即为最终宽度：openStart=%v want=%v", openStart, fileTreeNavWidth)
	}
}
