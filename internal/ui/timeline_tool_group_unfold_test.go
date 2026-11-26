package mcodeui

import (
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func topLevelToolGroupUnfoldMessages() []Message {
	return []Message{
		{ID: "u1", Role: "user", Text: "请查看仓库状态", TurnIndex: 1, IsFinal: true},
		{ID: "a1", Role: "assistant", Kind: "text", Text: "ABOVE-ANSWER", TurnIndex: 1, IsFinal: true},
		{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "THINKING-TOP-BEAT", TurnIndex: 1},
		{ID: "t1", Role: "tool", Text: `Bash {"command":"git status"}`, TurnIndex: 1, ToolStatus: "completed"},
		{ID: "t2", Role: "tool", Text: `Read {"file_path":"/workspace/main.go"}`, TurnIndex: 1, ToolStatus: "completed"},
		{ID: "u2", Role: "user", Text: "下一轮问题", TurnIndex: 2, IsFinal: true},
		{ID: "a2", Role: "assistant", Kind: "text", Text: "BELOW-ANSWER", TurnIndex: 2, IsFinal: true},
	}
}

func turnWorkToolGroupUnfoldMessages() []Message {
	return []Message{
		{ID: "u1", Role: "user", Text: "请查看仓库状态", TurnIndex: 1, IsFinal: true},
		{ID: "r1", Role: "assistant", Kind: "reasoning", Text: "THINKING-TURNWORK-BEAT", TurnIndex: 1},
		{ID: "t1", Role: "tool", Text: `Bash {"command":"git status"}`, TurnIndex: 1, ToolStatus: "completed"},
		{ID: "t2", Role: "tool", Text: `Read {"file_path":"/workspace/main.go"}`, TurnIndex: 1, ToolStatus: "completed"},
		{ID: "a1", Role: "assistant", Kind: "text", Text: "TURNWORK-BELOW", TurnIndex: 1, IsFinal: true},
		{ID: "t3", Role: "tool", Text: `Bash {"command":"echo done"}`, TurnIndex: 1, ToolStatus: "completed"},
	}
}

func toolGroupDistance(t *testing.T, tt *ui.Tester, headerText, belowText string) float32 {
	t.Helper()
	header, ok := tt.Find(headerText)
	if !ok {
		t.Fatalf("找不到工具组头 %q，当前文本 %v", headerText, tt.Texts())
	}
	below, ok := tt.Find(belowText)
	if !ok {
		t.Fatalf("找不到下方正文 %q，当前文本 %v", belowText, tt.Texts())
	}
	return below.Y - header.Y
}

// TestToolGroupUnfoldAnimatesBeatHeight 验证 List 顶层工具组点开时，节拍行从 0 展开到完整高度，
// 首帧不撑开下方行、半程介于首末之间、完成后停止请求帧。
func TestToolGroupUnfoldAnimatesBeatHeight(t *testing.T) {
	setAppReduceMotion(t, false)
	tt := newTimelineTester(t, topLevelToolGroupUnfoldMessages(), "BELOW-ANSWER")
	const headerText = "思考 · 命令 · 读取"

	collapsed := toolGroupDistance(t, tt, headerText, "BELOW-ANSWER")
	clickText(t, tt, headerText)
	start := toolGroupDistance(t, tt, headerText, "BELOW-ANSWER")

	tt.Advance(motionUnfold / 2)
	middle := toolGroupDistance(t, tt, headerText, "BELOW-ANSWER")

	tt.Advance(motionUnfold)
	done := toolGroupDistance(t, tt, headerText, "BELOW-ANSWER")

	tt.Advance(time.Second)
	settled := toolGroupDistance(t, tt, headerText, "BELOW-ANSWER")

	if start-collapsed > 6 {
		t.Fatalf("首帧组头到下方正文的距离应接近收起态：start=%v collapsed=%v", start, collapsed)
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

// TestToolGroupUnfoldInsideTurnWork 验证运行中「已处理」回合头内的工具组点开时，
// 节拍行按高度展开，半程介于首末之间，结束后不再请求帧。
func TestToolGroupUnfoldInsideTurnWork(t *testing.T) {
	setAppReduceMotion(t, false)
	running := Thread{ID: "t1", ProjectID: "p1", Title: "运行中", Provider: "claude", UpdatedAt: time.Now(), IsRunning: true}
	fake := NewFakeClient()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "Proj1", Path: "/workspace"}},
		map[string][]Thread{"p1": {running}},
	)
	fake.messages["t1"] = turnWorkToolGroupUnfoldMessages()
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("TURNWORK-BELOW") })
	const headerText = "思考 · 命令 · 读取"

	if !tt.HasText("已处理") {
		t.Fatalf("应存在运行中回合头「已处理」: %v", tt.Texts())
	}

	collapsed := toolGroupDistance(t, tt, headerText, "TURNWORK-BELOW")
	clickText(t, tt, headerText)
	start := toolGroupDistance(t, tt, headerText, "TURNWORK-BELOW")

	tt.Advance(motionUnfold / 2)
	middle := toolGroupDistance(t, tt, headerText, "TURNWORK-BELOW")

	tt.Advance(motionUnfold)
	done := toolGroupDistance(t, tt, headerText, "TURNWORK-BELOW")

	tt.Advance(time.Second)
	settled := toolGroupDistance(t, tt, headerText, "TURNWORK-BELOW")

	if start-collapsed > 12 {
		t.Fatalf("首帧组头到下方正文的距离应接近收起态：start=%v collapsed=%v", start, collapsed)
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

// TestToolGroupUnfoldReducedMotion 验证减少动效开启时，点开工具组首帧即为最终高度，且没有待画帧。
func TestToolGroupUnfoldReducedMotion(t *testing.T) {
	setAppReduceMotion(t, true)
	tt := newTimelineTester(t, topLevelToolGroupUnfoldMessages(), "BELOW-ANSWER")
	const headerText = "思考 · 命令 · 读取"

	collapsed := toolGroupDistance(t, tt, headerText, "BELOW-ANSWER")
	clickText(t, tt, headerText)
	start := toolGroupDistance(t, tt, headerText, "BELOW-ANSWER")
	if tt.FramePending() {
		t.Fatal("减少动效开启时点击展开后不应有待画帧")
	}

	tt.Advance(motionUnfold + time.Second)
	done := toolGroupDistance(t, tt, headerText, "BELOW-ANSWER")

	if start <= collapsed {
		t.Fatalf("减少动效展开后首帧应大于收起态高度：start=%v collapsed=%v", start, collapsed)
	}
	if math.Abs(float64(start-done)) > 0.5 {
		t.Fatalf("减少动效展开后首帧应即为最终高度：start=%v done=%v", start, done)
	}
}
