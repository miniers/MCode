package mcodeui

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func unfoldMotionDistance(t *testing.T, tt *ui.Tester, topText, bottomText string) float32 {
	t.Helper()
	top, ok := tt.Find(topText)
	if !ok {
		t.Fatalf("找不到上方元素 %q，当前文本 %v", topText, tt.Texts())
	}
	bottom, ok := tt.Find(bottomText)
	if !ok {
		t.Fatalf("找不到下方元素 %q，当前文本 %v", bottomText, tt.Texts())
	}
	return bottom.Y - top.Y
}

func unfoldMotionToolBeatMessages() []Message {
	return []Message{
		{ID: "u1", Role: "user", Text: "看看状态", TurnIndex: 1, IsFinal: true},
		{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "先看仓库状态", TurnIndex: 1},
		{ID: "t1", Role: "tool", Text: `Bash {"command":"git status"}` + "\nOUT-STATUS-1\nOUT-STATUS-2\nOUT-STATUS-3", TurnIndex: 1},
		{ID: "t2", Role: "tool", Text: `Bash {"command":"git diff"}` + "\nOUT-DIFF", TurnIndex: 1},
	}
}

func unfoldMotionErrorMessages() []Message {
	return []Message{
		{ID: "u1", Role: "user", Text: "跑一下命令", TurnIndex: 1, IsFinal: true},
		{ID: "e1", Role: "assistant", Kind: "error", Text: "ERR-SUMMARY-LINE\nERR-DETAIL-1\nERR-DETAIL-2\nERR-DETAIL-3", TurnIndex: 1},
		{ID: "u2", Role: "user", Text: "继续看看", TurnIndex: 2, IsFinal: true},
		{ID: "a2", Role: "assistant", Kind: "text", Text: "ERR-BELOW-MARKER", TurnIndex: 2, IsFinal: true},
	}
}

func setupUnfoldMotionTurnFilesTester(t *testing.T) *ui.Tester {
	t.Helper()
	paths := make([]string, 8)
	stats := make(map[string][2]int, 8)
	for i := range paths {
		paths[i] = fmt.Sprintf("f%d.go", i)
		stats[paths[i]] = [2]int{i + 1, 1}
	}
	fake := setupTurnFilesDiffFake(t, paths, turnFilesTestDiff(stats))
	fake.messages["t1"] = append(fake.messages["t1"],
		Message{ID: "u2", Role: "user", Kind: "text", Text: "下一轮", TurnIndex: 2, IsFinal: true},
		Message{ID: "a2", Role: "assistant", Kind: "text", Text: "BELOW-FILES-MARKER", TurnIndex: 2, IsFinal: true},
	)
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool {
		return tt.HasText("BELOW-FILES-MARKER") && turnFilesHasExactText(tt, "还有 2 个")
	})
	tt.Advance(motionUnfold + time.Second)
	return tt
}

func assertUnfoldMotionAnimates(t *testing.T, tt *ui.Tester, toggleText, topText, bottomText string) {
	t.Helper()
	collapsed := unfoldMotionDistance(t, tt, topText, bottomText)
	clickText(t, tt, toggleText)
	start := unfoldMotionDistance(t, tt, topText, bottomText)

	tt.Advance(motionUnfold / 2)
	middle := unfoldMotionDistance(t, tt, topText, bottomText)

	tt.Advance(motionUnfold)
	done := unfoldMotionDistance(t, tt, topText, bottomText)

	tt.Advance(time.Second)
	settled := unfoldMotionDistance(t, tt, topText, bottomText)

	if start-collapsed > 6 {
		t.Fatalf("首帧距离应接近收起态：start=%v collapsed=%v", start, collapsed)
	}
	if !(middle > start && middle < done) {
		t.Fatalf("展开半程高度应介于首末之间：start=%v middle=%v done=%v", start, middle, done)
	}
	if math.Abs(float64(done-settled)) > 0.5 {
		t.Fatalf("动画结束后距离应等于最终高度：done=%v settled=%v", done, settled)
	}
	if tt.FramePending() {
		t.Fatal("动画结束后不应再请求下一帧")
	}
}

func assertUnfoldMotionReduced(t *testing.T, tt *ui.Tester, toggleText, topText, bottomText string) {
	t.Helper()
	collapsed := unfoldMotionDistance(t, tt, topText, bottomText)
	clickText(t, tt, toggleText)
	start := unfoldMotionDistance(t, tt, topText, bottomText)
	if tt.FramePending() {
		t.Fatal("减少动效开启时点击展开后不应有待画帧")
	}

	tt.Advance(motionUnfold + time.Second)
	done := unfoldMotionDistance(t, tt, topText, bottomText)

	if start <= collapsed {
		t.Fatalf("减少动效展开后首帧应大于收起态高度：start=%v collapsed=%v", start, collapsed)
	}
	if math.Abs(float64(start-done)) > 0.5 {
		t.Fatalf("减少动效展开后首帧应即为最终高度：start=%v done=%v", start, done)
	}
}

// TestUnfoldMotionTimelineToolBeatDetail 验证节拍内工具胶囊点开详情时的展开动效及结束后停止请求帧。
func TestUnfoldMotionTimelineToolBeatDetail(t *testing.T) {
	setAppReduceMotion(t, false)
	tt := newTimelineTester(t, unfoldMotionToolBeatMessages(), "思考 · 命令")
	clickText(t, tt, "思考 · 命令")
	tt.Advance(motionUnfold + time.Second)

	assertUnfoldMotionAnimates(t, tt, "$ git status", "$ git status", "$ git diff")
}

// TestUnfoldMotionTimelineErrorDetail 验证错误卡片点开「详情」时的展开动效及结束后停止请求帧。
func TestUnfoldMotionTimelineErrorDetail(t *testing.T) {
	setAppReduceMotion(t, false)
	tt := newTimelineTester(t, unfoldMotionErrorMessages(), "ERR-BELOW-MARKER")
	tt.Advance(motionUnfold + time.Second)

	assertUnfoldMotionAnimates(t, tt, "详情", "ERR-SUMMARY-LINE", "ERR-BELOW-MARKER")
}

// TestUnfoldMotionTimelineTurnFilesCard 验证改动文件卡片点开「还有 N 个」时的展开动效及结束后停止请求帧。
func TestUnfoldMotionTimelineTurnFilesCard(t *testing.T) {
	setAppReduceMotion(t, false)
	tt := setupUnfoldMotionTurnFilesTester(t)

	assertUnfoldMotionAnimates(t, tt, "还有 2 个", "改动文件", "BELOW-FILES-MARKER")
}

// TestUnfoldMotionTimelineReducedMotion 验证减少动效开启时，三处展开后首帧即为最终高度且无待画帧。
func TestUnfoldMotionTimelineReducedMotion(t *testing.T) {
	setAppReduceMotion(t, true)

	t.Run("ToolBeatDetail", func(t *testing.T) {
		tt := newTimelineTester(t, unfoldMotionToolBeatMessages(), "思考 · 命令")
		clickText(t, tt, "思考 · 命令")
		tt.Advance(motionUnfold + time.Second)
		assertUnfoldMotionReduced(t, tt, "$ git status", "$ git status", "$ git diff")
	})

	t.Run("ErrorDetail", func(t *testing.T) {
		fake := NewFakeClient()
		fake.messages["t1"] = unfoldMotionErrorMessages()
		_, view := buildRoot(fake)
		tt := ui.NewTesterWith(view, 1000, 700, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
		waitFor(t, tt, func() bool { return tt.HasText("ERR-BELOW-MARKER") })
		tt.Advance(motionUnfold + time.Second)
		assertUnfoldMotionReduced(t, tt, "详情", "ERR-SUMMARY-LINE", "ERR-BELOW-MARKER")
	})

	t.Run("TurnFilesCard", func(t *testing.T) {
		tt := setupUnfoldMotionTurnFilesTester(t)
		assertUnfoldMotionReduced(t, tt, "还有 2 个", "改动文件", "BELOW-FILES-MARKER")
	})
}
